package server

import (
	"encoding/json"
	"net/http"
	"os"
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
	if qAcc := strings.TrimSpace(r.URL.Query().Get("account_id")); qAcc != "" {
		preferredAcc = qAcc
		allowFailover = qAcc == "auto"
	}

	accCfg, found := gs.pool.SelectHealthyAccount(preferredAcc, allowFailover)
	if !found {
		writeJSONError(w, http.StatusServiceUnavailable, "no healthy ACP account available for WebSocket tunnel")
		return
	}
	acctID := accCfg.Account.ID

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	wsDir, _ := gs.sm.EnsureSessionWorkspace(acctID, reqID)
	if wsDir != "" {
		defer func() { _ = os.RemoveAll(wsDir) }()
	}
	poolCfg := gs.cfg.GetACPPool()
	sandboxCfg := gs.cfg.GetSandboxPolicy()
	env := acp.BuildIsolatedEnv(poolCfg.Env, accCfg.Account, accCfg.GEMINIHome, accCfg.RelayListen)
	var netns string
	if accCfg.Account.IsolationEnabled && accCfg.Account.NetworkMode == "netns" {
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
		BinaryPath:          poolCfg.BinaryPath,
		WorkspaceDir:        wsDir,
		AdditionalDirs:      poolCfg.AdditionalDirs,
		AgentName:           poolCfg.AgentName,
		ProcessPerSession:   poolCfg.ProcessPerSession,
		SkipPermissionsFlag: sandboxCfg.SkipPermissionsFlag,
		EnableSandbox:       sandboxCfg.EnableSandbox,
		DisableTelemetry:    poolCfg.DisableTelemetry,
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
			select {
			case rawOutCh <- rawLine:
			case <-stopCh:
			case <-time.After(50 * time.Millisecond):
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

func (gs *GatewayServer) rewriteRemoteACPCwdIfNeeded(raw []byte, fallbackRemoteDir string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	method, _ := obj["method"].(string)
	if method != "session/new" && method != "session/resume" {
		return raw
	}
	params, ok := obj["params"].(map[string]any)
	if !ok {
		return raw
	}
	cwd, _ := params["cwd"].(string)
	if cwd == "" {
		params["cwd"] = fallbackRemoteDir
	} else if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		params["cwd"] = fallbackRemoteDir
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return out
}
