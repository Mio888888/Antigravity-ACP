package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/adapter"

	"github.com/gorilla/websocket"
)

func (gs *GatewayServer) handleACPWebSocketTunnel(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	reqID := genReqID("ws_")

	preferredAcc, allowFailover := gs.resolvePreferredAccount(r)

	accCfg, found := gs.pool.SelectHealthyAccount(preferredAcc, allowFailover)
	if !found {
		writeJSONError(w, http.StatusServiceUnavailable, "no healthy ACP account available for WebSocket tunnel")
		return
	}
	acctID := accCfg.Account.ID

	wsDir, err := gs.sm.EnsureSessionWorkspace(acctID, reqID)
	if err != nil || wsDir == "" {
		writeJSONError(w, http.StatusInternalServerError, "failed to prepare workspace for WebSocket tunnel")
		return
	}
	defer func() { _ = os.RemoveAll(wsDir) }()

	upgrader := websocket.Upgrader{CheckOrigin: gs.isOriginAllowedForAPI}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	poolCfg := gs.cfg.GetACPPool()
	sandboxCfg := gs.cfg.GetSandboxPolicy()
	env := acp.BuildIsolatedEnv(poolCfg.Env, accCfg.Account, accCfg.GEMINIHome, accCfg.RelayListen)
	var netns string
	if accCfg.Account.NetworkMode == "netns" {
		netns = accCfg.Account.NetNSName
	}

	wsCanReq := &adapter.CanonicalRequest{
		Protocol: "acp_ws",
		Model:    "acp-tunnel",
	}

	var inTokens atomic.Int64
	var outTokens atomic.Int64
	rawOutCh := make(chan string, 1024)
	stopCh := make(chan struct{})
	worker, err := acp.NewWorkerClient(r.Context(), "ws_"+acctID+"_"+reqID, acp.WorkerLaunchOptions{
		AccountID:           acctID,
		BackendType:         poolCfg.BackendType,
		BinaryPath:          poolCfg.BinaryPath,
		WorkspaceDir:        wsDir,
		AdditionalDirs:      poolCfg.AdditionalDirs,
		AgentName:           poolCfg.AgentName,
		ProcessPerSession:   poolCfg.ProcessPerSession,
		SkipPermissionsFlag: sandboxCfg.SkipPermissionsFlag,
		EnableSandbox:       sandboxCfg.EnableSandbox,
		DisableTelemetry:    poolCfg.DisableTelemetry,
		ContextSize:         acp.ResolveWorkerContextSize(poolCfg, extractContextWindowHeader(r)),
		ExtraArgs:           poolCfg.ExtraArgs,
		Env:                 env,
		NetNSName:           netns,
		InternalApproval:    sandboxCfg.InternalApproval,
	}, func(workerID, direction, rawLine string) {
		if gs.hub != nil {
			gs.hub.Broadcast(workerID, direction, rawLine)
		}
		if direction == "IN" {
			outTokens.Add(estimateTokens(rawLine))
			// Block rather than drop: losing a JSON-RPC response would hang the client.
			select {
			case rawOutCh <- rawLine:
			case <-stopCh:
			}
		}
	})
	if err != nil {
		close(stopCh)
		gs.recordAudit(reqID, wsCanReq, acctID, "", 0, 0, 0, startTime, http.StatusInternalServerError, err.Error(), "", "", "")
		return
	}
	defer worker.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case line := <-rawOutCh:
				if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
					// Unblock the reader so the tunnel tears down instead of stalling the worker.
					_ = conn.Close()
					return
				}
			case <-stopCh:
				return
			case <-r.Context().Done():
				return
			}
		}
	}()

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			break
		}
		inTokens.Add(estimateTokens(string(msgBytes)))
		rewritten := gs.rewriteRemoteACPCwdIfNeeded(msgBytes, wsDir)
		if err := worker.SendRawJSONLine(rewritten); err != nil {
			break
		}
	}
	close(stopCh)
	<-done
	gs.recordAudit(reqID, wsCanReq, acctID, "", worker.PID, inTokens.Load(), outTokens.Load(), startTime, http.StatusOK, "", "", "", "")
}

// rewriteRemoteACPCwdIfNeeded confines session/new|resume|load to the gateway-managed
// workspace: any cwd outside workspaceDir is replaced, and additionalDirectories
// outside it are dropped.
func (gs *GatewayServer) rewriteRemoteACPCwdIfNeeded(raw []byte, workspaceDir string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	method, _ := obj["method"].(string)
	if method != "session/new" && method != "session/resume" && method != "session/load" {
		return raw
	}
	params, ok := obj["params"].(map[string]any)
	if !ok {
		return raw
	}
	cwd, _ := params["cwd"].(string)
	if !isPathWithin(cwd, workspaceDir) {
		params["cwd"] = workspaceDir
	}
	if dirs, ok := params["additionalDirectories"].([]any); ok {
		kept := make([]any, 0, len(dirs))
		for _, d := range dirs {
			if s, ok := d.(string); ok && isPathWithin(s, workspaceDir) {
				kept = append(kept, s)
			}
		}
		params["additionalDirectories"] = kept
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}

func isPathWithin(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
