package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/server"
	"antigravity-gateway/internal/storage"
)

// rotationAgent is a fake ACP agent that answers session/prompt on a goroutine-like
// thread, so session/cancel and session/close still arrive while a tool call is parked.
// A "LOOP_TOOL" prompt calls client_tools read_file until the turn is cancelled.
const rotationAgent = `
import sys, json, threading, urllib.request
LOG = __LOG_PATH__
lock = threading.Lock()
sessions = {}
seq = [0]

def out(obj):
    with lock:
        sys.stdout.write(json.dumps(obj) + "\n")
        sys.stdout.flush()

def log(line):
    with lock:
        with open(LOG, "a") as f:
            f.write(line + "\n")

def call_tool(sid, n):
    body = json.dumps({"jsonrpc": "2.0", "id": n, "method": "tools/call", "params": {"name": "read_file", "arguments": {"n": n}}}).encode("utf-8")
    req = urllib.request.Request(sessions[sid]["url"], data=body, headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode("utf-8"))

def run_prompt(req_id, sid, text):
    stop = "end_turn"
    if "LOOP_TOOL" in text and sessions[sid]["url"]:
        n = 0
        while True:
            n += 1
            r = call_tool(sid, n)
            if "error" in r or r.get("result", {}).get("isError"):
                log("toolerr " + sid)
            if sessions[sid]["cancelled"]:
                stop = "cancelled"
                break
            if "error" in r:
                break
    else:
        out({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": sid, "update": {"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": "Reply from " + sid}}}})
    out({"jsonrpc": "2.0", "id": req_id, "result": {"stopReason": stop}})

for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    msg = json.loads(line)
    req_id = msg.get("id")
    method = msg.get("method", "")
    params = msg.get("params", {})
    if method == "initialize":
        out({"jsonrpc": "2.0", "id": req_id, "result": {"protocolVersion": 1, "agentInfo": {"name": "Antigravity ACP Server", "version": "1.4.5"}}})
    elif method == "session/new":
        seq[0] += 1
        sid = "s%d" % seq[0]
        mcps = params.get("mcpServers", [])
        url = mcps[0].get("url", "") if mcps else ""
        sessions[sid] = {"url": url, "cancelled": False}
        log("new %s %s" % (sid, url))
        out({"jsonrpc": "2.0", "id": req_id, "result": {"sessionId": sid, "models": {"currentModelId": "gemini-2.5-pro", "availableModels": [{"modelId": "gemini-2.5-pro", "name": "Gemini 2.5 Pro"}]}}})
    elif method == "session/cancel":
        sid = params.get("sessionId", "")
        log("cancel " + sid)
        if sid in sessions:
            sessions[sid]["cancelled"] = True
    elif method == "session/close":
        log("close " + params.get("sessionId", ""))
        out({"jsonrpc": "2.0", "id": req_id, "result": {}})
    elif method == "session/prompt":
        text = "".join(b.get("text", "") for b in params.get("prompt", []) if b.get("type") == "text")
        threading.Thread(target=run_prompt, args=(req_id, params.get("sessionId", ""), text), daemon=True).start()
    elif req_id is not None:
        out({"jsonrpc": "2.0", "id": req_id, "result": {}})
`

// TestClientCompactionRetiresLongToolLoopSession reproduces a Sailry review: a tool loop
// that outlives the per-session key index, followed by a client compaction. The ACP
// session holding the pre-compaction history must be cancelled and closed, its parked
// call released, and its bridge generation must reject further calls.
func TestClientCompactionRetiresLongToolLoopSession(t *testing.T) {
	t.Setenv("AG_CLIENT_COMPACTION_MODE", "")
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "agent.log")
	logLit, _ := json.Marshal(logPath)
	script := strings.Replace(rotationAgent, "__LOG_PATH__", string(logLit), 1)

	cfg := &config.Config{
		Server: config.ServerConfig{Host: "127.0.0.1", DataDir: tmpDir, AdminToken: "secret-admin", CORSAllowedOrigins: []string{"*"}, SSEKeepAliveSec: 15},
		ACPPool: config.ACPPoolConfig{
			BinaryPath:         "python3",
			WorkspaceDir:       filepath.Join(tmpDir, "workspaces"),
			ClientName:         "zed",
			ClientVersion:      "0.175.0",
			MaxWorkers:         2,
			MaxIdleTimeSec:     60,
			ToolParkTimeoutSec: 30,
			ExtraArgs:          []string{"-u", "-c", script},
			Env:                map[string]string{},
		},
		PromptPolicy:  config.PromptPolicyConfig{Mode: "pure_passthrough", DisableBuiltinTools: true},
		SandboxPolicy: config.SandboxPolicyConfig{SessionMode: "yolo", InternalApproval: "auto_approve"},
	}
	store, err := storage.NewStore(filepath.Join(tmpDir, "gateway.db"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()
	accounts, _ := store.ListAccounts(false)
	hub := server.NewWiretapHub()
	pool, err := acp.NewMultiAccountPool(tmpDir, cfg.ACPPool, cfg.SandboxPolicy, accounts, hub.Broadcast)
	if err != nil {
		t.Fatalf("NewMultiAccountPool failed: %v", err)
	}
	defer pool.Close()
	sm := acp.NewSessionManager(cfg.ACPPool.WorkspaceDir, 30*time.Second)
	defer sm.Close()
	gw := server.NewGatewayServer(cfg, store, pool, sm, hub, fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	httpSrv := httptest.NewServer(gw.Handler())
	defer httpSrv.Close()
	gw.SetLoopbackBaseURL(httpSrv.URL)

	admin := func(path, body string) []byte {
		req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret-admin")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s failed: resp=%v err=%v", path, resp, err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return buf.Bytes()
	}
	admin("/api/admin/accounts", `{"id":"acc_rot","name":"Rotation","auth_mode":"oauth_personal","max_workers":2,"weight":1,"enabled":true}`)
	var key struct {
		PlainKey string `json:"plain_key"`
	}
	_ = json.Unmarshal(admin("/api/admin/keys", `{"name":"Sailry","bound_account_id":"acc_rot","rate_limit_rpm":1000}`), &key)

	type outputItem struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
	}
	respond := func(payload map[string]any) []outputItem {
		t.Helper()
		payload["model"] = "gemini-2.5-pro"
		payload["stream"] = false
		payload["prompt_cache_key"] = "conv-rotation"
		raw, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key.PlainKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /v1/responses failed: resp=%v err=%v", resp, err)
		}
		defer resp.Body.Close()
		var parsed struct {
			Output []outputItem `json:"output"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&parsed)
		return parsed.Output
	}
	callIDOf := func(items []outputItem) string {
		t.Helper()
		for _, it := range items {
			if it.Type == "function_call" && it.CallID != "" {
				return it.CallID
			}
		}
		t.Fatalf("expected a function_call output item, got %+v", items)
		return ""
	}
	readLog := func() string {
		raw, _ := os.ReadFile(logPath)
		return string(raw)
	}

	tools := []map[string]any{{"type": "function", "name": "read_file", "description": "Read a file", "parameters": map[string]any{"type": "object"}}}
	input := []any{map[string]any{"role": "user", "content": "LOOP_TOOL review the change"}}
	out := respond(map[string]any{"input": input, "tools": tools})
	// More responses than the per-session key index holds.
	for i := 0; i < 40; i++ {
		callID := callIDOf(out)
		input = append(input,
			map[string]any{"type": "function_call", "call_id": callID, "name": "read_file", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": callID, "output": "ok"},
		)
		out = respond(map[string]any{"input": input, "tools": tools})
	}
	callIDOf(out)

	var loopSess, loopURL string
	for _, line := range strings.Split(readLog(), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == "new" {
			loopSess, loopURL = f[1], f[2]
			break
		}
	}
	if loopSess == "" {
		t.Fatalf("tool-loop ACP session not found in agent log:\n%s", readLog())
	}
	if strings.Contains(readLog(), "close "+loopSess) {
		t.Fatalf("tool-loop session closed before compaction:\n%s", readLog())
	}

	respond(map[string]any{
		"instructions": sailryCompactionSystemPrompt(),
		"input":        []any{map[string]any{"role": "user", "content": "Please summarize the conversation history provided in the system prompt."}},
	})

	deadline := time.Now().Add(3 * time.Second)
	for !(strings.Contains(readLog(), "cancel "+loopSess) && strings.Contains(readLog(), "close "+loopSess) && strings.Contains(readLog(), "toolerr "+loopSess)) {
		if time.Now().After(deadline) {
			t.Fatalf("compaction did not cancel, close and release the parked call of %s:\n%s", loopSess, readLog())
		}
		time.Sleep(20 * time.Millisecond)
	}

	staleCall := `{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"read_file","arguments":{}}}`
	resp, err := http.Post(loopURL, "application/json", strings.NewReader(staleCall))
	if err != nil {
		t.Fatalf("POST stale bridge failed: %v", err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(buf.String(), "bridge retired") {
		t.Fatalf("expected the retired session's bridge to reject calls immediately, got %s", buf.String())
	}
}
