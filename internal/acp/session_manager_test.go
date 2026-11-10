package acp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
)

func TestBuildIsolatedEnvAndEgressRelay(t *testing.T) {
	acc := storage.Account{
		ID:               "acc_iso_1",
		AuthMode:         "gemini_api_key",
		APIKey:           "AIzaTestKey123",
		IsolationEnabled: true,
		NetworkMode:      "proxy",
	}
	env := acp.BuildIsolatedEnv(map[string]string{"BASE_VAR": "1"}, acc, "/data/accounts/acc_iso_1/.gemini", "http://127.0.0.1:45678")
	if env["GEMINI_HOME"] != "/data/accounts/acc_iso_1/.gemini" {
		t.Fatalf("expected isolated GEMINI_HOME, got %q", env["GEMINI_HOME"])
	}
	if env["AGY_ACP_FORCE_FILE_STORAGE"] != "1" {
		t.Fatalf("expected AGY_ACP_FORCE_FILE_STORAGE=1, got %q", env["AGY_ACP_FORCE_FILE_STORAGE"])
	}
	if env["GEMINI_API_KEY"] != "AIzaTestKey123" {
		t.Fatalf("expected GEMINI_API_KEY=AIzaTestKey123, got %q", env["GEMINI_API_KEY"])
	}
	if env["HTTP_PROXY"] != "http://127.0.0.1:45678" || env["NO_PROXY"] != "127.0.0.1,localhost,::1" {
		t.Fatalf("unexpected proxy/no_proxy env: %+v", env)
	}

	// Start a target HTTP server returning a simulated public egress IP
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("203.0.113.88"))
	}))
	defer targetSrv.Close()

	// Start a mock upstream HTTP forward proxy
	var upstreamHits atomic.Int32
	upstreamProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer upstreamProxy.Close()

	relay, err := acp.NewEgressRelay("acc_iso_1", "proxy", upstreamProxy.URL, "")
	if err != nil {
		t.Fatalf("NewEgressRelay failed: %v", err)
	}
	defer relay.Close()

	if !strings.HasPrefix(relay.ListenURL(), "http://127.0.0.1:") {
		t.Fatalf("unexpected relay ListenURL: %q", relay.ListenURL())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probedIP, err := relay.ProbeTargetURL(ctx, targetSrv.URL)
	if err != nil {
		t.Fatalf("ProbeTargetURL failed: %v", err)
	}
	if strings.TrimSpace(probedIP) != "203.0.113.88" {
		t.Fatalf("expected probed IP '203.0.113.88', got %q", probedIP)
	}
	if upstreamHits.Load() == 0 {
		t.Fatalf("expected request to traverse upstream proxy, got 0 hits")
	}

	// Also test bind_ip mode with 127.0.0.1
	bindRelay, err := acp.NewEgressRelay("acc_bind_1", "bind_ip", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("NewEgressRelay(bind_ip) failed: %v", err)
	}
	defer bindRelay.Close()
	ip2, err := bindRelay.ProbeTargetURL(ctx, targetSrv.URL)
	if err != nil || strings.TrimSpace(ip2) != "203.0.113.88" {
		t.Fatalf("bindRelay ProbeTargetURL failed: ip=%q, err=%v", ip2, err)
	}
	_ = net.IPv4zero
}

func TestLoopbackMCPBridgeParkResumeAndTimeoutGC(t *testing.T) {
	baseDir := t.TempDir()
	sm := acp.NewSessionManager(baseDir, 150*time.Millisecond)
	defer sm.Close()

	wsDir, err := sm.EnsureSessionWorkspace("acc_1", "bridge_1")
	if err != nil || wsDir == "" {
		t.Fatalf("EnsureSessionWorkspace failed: dir=%q, err=%v", wsDir, err)
	}

	tools := []acp.ToolDefinition{
		{
			Name:        "read_local_file",
			Description: "Read a file on user laptop",
			InputSchema: map[string]any{"type": "object"},
		},
	}
	sess := sm.RegisterOrMatchSession("acc_1", "conv_1", "hash_1", nil, "acp_sess_1", tools)
	if sess == nil || sess.BridgeID == "" {
		t.Fatalf("expected registered ManagedSession")
	}
	if sess.LastTranscriptStep != -1 {
		t.Fatalf("expected LastTranscriptStep initialized to -1, got %d", sess.LastTranscriptStep)
	}
	expectedAccWs := acp.EnsureAccountWorkspace(baseDir, "acc_1")
	if sess.WorkspaceDir != expectedAccWs {
		t.Fatalf("expected session WorkspaceDir %q to match canonical account workspace %q (so worker cwd == session cwd for relative paths)", sess.WorkspaceDir, expectedAccWs)
	}
	if resolved, err := filepath.EvalSymlinks(sess.WorkspaceDir); err == nil && resolved != sess.WorkspaceDir {
		t.Fatalf("expected session WorkspaceDir to have symlinks resolved (%q != %q)", sess.WorkspaceDir, resolved)
	}

	mcpSrv := httptest.NewServer(acp.MCPBridgeHandler(sm, nil))
	defer mcpSrv.Close()

	// 1. Test tools/list
	listReq := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	resp, err := http.Post(fmt.Sprintf("%s/mcp/sessions/%s", mcpSrv.URL, sess.BridgeID), "application/json", strings.NewReader(listReq))
	if err != nil {
		t.Fatalf("MCP tools/list POST failed: %v", err)
	}
	defer resp.Body.Close()
	rawList, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rawList), "read_local_file") {
		t.Fatalf("expected read_local_file in tools/list response: %s", string(rawList))
	}

	// 2. Test tools/call Park & Resume
	type callResp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	callDone := make(chan callResp, 1)
	go func() {
		callBody := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_local_file","arguments":{"path":"main.go"}}}`
		r, err := http.Post(fmt.Sprintf("%s/mcp/sessions/%s", mcpSrv.URL, sess.BridgeID), "application/json", bytes.NewBufferString(callBody))
		if err != nil {
			return
		}
		defer r.Body.Close()
		var cr callResp
		_ = json.NewDecoder(r.Body).Decode(&cr)
		callDone <- cr
	}()

	// Wait for ParkedToolCall notification on sess.ToolCallNotifyCh
	var parked *acp.ParkedToolCall
	select {
	case parked = <-sess.ToolCallNotifyCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for ParkedToolCall notification")
	}
	if parked.ToolName != "read_local_file" || !strings.Contains(parked.ArgsJSON, "main.go") {
		t.Fatalf("unexpected parked tool call: %+v", parked)
	}

	// Resume the parked tool call
	resumedSess, err := sm.ResumeToolCall(parked.CallID, "package main\nfunc main() {}", false)
	if err != nil || resumedSess.BridgeID != sess.BridgeID {
		t.Fatalf("ResumeToolCall failed: sess=%+v, err=%v", resumedSess, err)
	}

	select {
	case cr := <-callDone:
		if len(cr.Result.Content) == 0 || !strings.Contains(cr.Result.Content[0].Text, "package main") {
			t.Fatalf("unexpected MCP tools/call result after resume: %+v", cr)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for MCP tools/call HTTP response")
	}

	// 3. Test ParkedToolCall GC timeout (Review Focus #3)
	parkedTimeout, err := sm.ParkToolCall(sess.BridgeID, "slow_tool", `{}`)
	if err != nil {
		t.Fatalf("ParkToolCall failed: %v", err)
	}
	select {
	case res := <-parkedTimeout.ResultCh:
		if !res.IsError || !strings.Contains(strings.ToLower(res.Content), "timeout") {
			t.Fatalf("expected timeout error from GC, got: %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected GC to expire parked tool call within 2s")
	}
}

func TestMultiAccountPoolRoutingAndFailover(t *testing.T) {
	pyWorker := `
import sys, json
for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    msg = json.loads(line)
    if msg.get("method") == "initialize":
        sys.stdout.write(json.dumps({
            "jsonrpc": "2.0",
            "id": msg.get("id"),
            "result": {
                "protocolVersion": 1,
                "agentInfo": {"name": "Antigravity ACP Server", "version": "1.4.5"}
            }
        }) + "\n")
        sys.stdout.flush()
`
	poolCfg := config.ACPPoolConfig{
		BinaryPath:         "python3",
		WorkspaceDir:       t.TempDir(),
		ClientName:         "zed",
		ClientVersion:      "0.175.0",
		MinIdleWorkers:     0,
		MaxWorkers:         3,
		MaxIdleTimeSec:     60,
		ToolParkTimeoutSec: 60,
		ExtraArgs:          []string{"-u", "-c", pyWorker},
	}
	sandboxCfg := config.SandboxPolicyConfig{
		SessionMode:      "yolo",
		InternalApproval: "auto_approve",
	}
	accounts := []storage.Account{
		{ID: "acc_a", Name: "Account A", IsolationEnabled: true, NetworkMode: "direct", MaxWorkers: 2, Weight: 1, Enabled: true, Status: "ready"},
		{ID: "acc_b", Name: "Account B", IsolationEnabled: true, NetworkMode: "direct", MaxWorkers: 2, Weight: 1, Enabled: true, Status: "ready"},
	}

	mp, err := acp.NewMultiAccountPool(t.TempDir(), poolCfg, sandboxCfg, accounts, nil)
	if err != nil {
		t.Fatalf("NewMultiAccountPool failed: %v", err)
	}
	defer mp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Explicit routing to acc_b
	wB, accB, err := mp.AcquireForAccount(ctx, "acc_b", false)
	if err != nil {
		t.Fatalf("AcquireForAccount(acc_b) failed: %v", err)
	}
	if accB.ID != "acc_b" || wB.AccountID != "acc_b" {
		t.Fatalf("expected account acc_b, got acc=%s worker.AccountID=%s", accB.ID, wB.AccountID)
	}
	mp.Release(wB)

	// 2. Mark acc_a as auth_required and verify auto-routing selects healthy acc_b (Review Focus #4)
	mp.MarkAccountAuthRequired("acc_a")
	wAuto, accAuto, err := mp.AcquireForAccount(ctx, "", true)
	if err != nil {
		t.Fatalf("AcquireForAccount(auto) failed: %v", err)
	}
	if accAuto.ID != "acc_b" {
		t.Fatalf("expected auto failover to select healthy acc_b when acc_a is auth_required, got %s", accAuto.ID)
	}
	mp.Release(wAuto)
}

func TestValidateProbeURLAndParallelAutoResume(t *testing.T) {
	// 1. ValidateProbeURL blocks metadata/link-local and non-HTTP schemes
	if err := acp.ValidateProbeURL("file:///etc/passwd"); err == nil {
		t.Fatalf("expected file:// scheme to be rejected")
	}
	if err := acp.ValidateProbeURL("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatalf("expected link-local metadata IP 169.254.169.254 to be rejected")
	}
	if err := acp.ValidateProbeURL("http://metadata.google.internal/computeMetadata/v1/"); err == nil {
		t.Fatalf("expected metadata.google.internal to be rejected")
	}
	if err := acp.ValidateProbeURL("https://api.ipify.org?format=json"); err != nil {
		t.Fatalf("expected public probe URL to be allowed, got: %v", err)
	}

	// 2. Verify AutoResumeRemainingParkedCalls & CancelParkedToolCall prevent worker deadlocks
	sm := acp.NewSessionManager(t.TempDir(), 5*time.Second)
	defer sm.Close()

	sess := sm.RegisterOrMatchSession("default", "sess-1", "h1", nil, "acp-1", nil)
	p1, err := sm.ParkToolCall(sess.BridgeID, "read_file", `{"path":"a"}`)
	if err != nil {
		t.Fatalf("ParkToolCall p1 failed: %v", err)
	}
	p2, err := sm.ParkToolCall(sess.BridgeID, "read_file", `{"path":"b"}`)
	if err != nil {
		t.Fatalf("ParkToolCall p2 failed: %v", err)
	}
	p3, err := sm.ParkToolCall(sess.BridgeID, "read_file", `{"path":"c"}`)
	if err != nil {
		t.Fatalf("ParkToolCall p3 failed: %v", err)
	}

	sm.MarkToolCallDispatched(p1.CallID)
	sm.MarkToolCallDispatched(p2.CallID)
	sm.CancelParkedToolCall(p3.CallID)
	pUndispatched, err := sm.ParkToolCall(sess.BridgeID, "read_file", `{"path":"d"}`)
	if err != nil {
		t.Fatalf("ParkToolCall pUndispatched failed: %v", err)
	}
	if _, err := sm.ResumeToolCall(p1.CallID, "ok-a", false); err != nil {
		t.Fatalf("ResumeToolCall p1 failed: %v", err)
	}
	sm.AutoResumeRemainingParkedCalls(sess.BridgeID, "fallback omitted")
	res2 := <-p2.ResultCh
	if !res2.IsError || res2.Content != "fallback omitted" {
		t.Fatalf("unexpected auto-resumed result: %+v", res2)
	}
	if sm.ParkedCount() != 1 {
		t.Fatalf("expected 1 undispatched parked call preserved for next turn, got %d", sm.ParkedCount())
	}
	sm.CancelParkedToolCall(pUndispatched.CallID)
	if sm.ParkedCount() != 0 {
		t.Fatalf("expected 0 remaining parked calls, got %d", sm.ParkedCount())
	}
}
