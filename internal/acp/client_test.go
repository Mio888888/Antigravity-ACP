package acp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
)

func TestBuildCommandAndArgs(t *testing.T) {
	cmdBin, args := acp.BuildCommandAndArgs(acp.WorkerLaunchOptions{
		BinaryPath:          "/usr/local/bin/agy",
		ProcessPerSession:   true,
		SkipPermissionsFlag: true,
		EnableSandbox:       true,
		DisableTelemetry:    true,
		LogFile:             "/tmp/agy.log",
		IDEMCPPort:          54321,
		AgentName:           "reviewer",
		AdditionalDirs:      []string{"/workspace/a", "/workspace/b"},
		ExtraArgs:           []string{"--custom-flag"},
	})
	if cmdBin != "/usr/local/bin/agy" {
		t.Fatalf("expected binary /usr/local/bin/agy, got %q", cmdBin)
	}
	joined := strings.Join(args, " ")
	expectedSubstrings := []string{
		"acp",
		"--process-per-session",
		"--dangerously-skip-permissions",
		"--sandbox",
		"--disable-telemetry",
		"--log-file /tmp/agy.log",
		"--ide-mcp-port 54321",
		"--agent reviewer",
		"--add-dir /workspace/a",
		"--add-dir /workspace/b",
		"--custom-flag",
	}
	for _, sub := range expectedSubstrings {
		if !strings.Contains(joined, sub) {
			t.Fatalf("expected %q in args %q", sub, joined)
		}
	}

	// Test Linux NetNS wrapping
	nsBin, nsArgs := acp.BuildCommandAndArgs(acp.WorkerLaunchOptions{
		BinaryPath:        "/opt/agy_acp_server.par",
		ProcessPerSession: true,
		NetNSName:         "ns_acc_01",
	})
	if nsBin != "ip" {
		t.Fatalf("expected netns binary 'ip', got %q", nsBin)
	}
	if len(nsArgs) < 4 || nsArgs[0] != "netns" || nsArgs[1] != "exec" || nsArgs[2] != "ns_acc_01" || nsArgs[3] != "/opt/agy_acp_server.par" {
		t.Fatalf("unexpected netns args: %v", nsArgs)
	}
}

func TestWorkerClientLifecycleOversizedFramesAndErrorCodes(t *testing.T) {
	pyScript := `
import sys, json

# Startup non-JSON banner noise (Review Focus #2)
sys.stdout.write("Launching localharness_external banner line...\n")
sys.stdout.flush()

for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    msg = json.loads(line)
    method = msg.get("method", "")
    req_id = msg.get("id")
    params = msg.get("params", {})

    if method == "initialize":
        resp = {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": 1,
                "agentInfo": {"name": "Antigravity ACP Server", "version": "1.4.5"},
                "authMethods": [{"id": "terminal-login", "name": "Terminal Login"}]
            }
        }
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()
    elif method == "session/new":
        cwd = params.get("cwd", "")
        if cwd == "/trigger/auth_error":
            err_resp = {
                "jsonrpc": "2.0",
                "id": req_id,
                "error": {"code": -32000, "message": "Authentication required"}
            }
            sys.stdout.write(json.dumps(err_resp) + "\n")
            sys.stdout.flush()
            continue
        resp = {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "sessionId": "sess_mock_1",
                "configOptions": [
                    {"id": "model", "name": "Model", "currentValue": "gemini-2.5-pro"},
                    {"id": "thinking_level", "name": "Thinking Level", "currentValue": "high"}
                ]
            }
        }
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()
    elif method == "session/resume":
        err_resp = {
            "jsonrpc": "2.0",
            "id": req_id,
            "error": {"code": -32002, "message": "Resource not found: session expired"}
        }
        sys.stdout.write(json.dumps(err_resp) + "\n")
        sys.stdout.flush()
    elif method == "session/set_config_option":
        resp = {"jsonrpc": "2.0", "id": req_id, "result": {}}
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()
    elif method == "session/prompt":
        # 1. Request permission from client first
        perm_req = {
            "jsonrpc": "2.0",
            "id": 9001,
            "method": "session/request_permission",
            "params": {"sessionId": "sess_mock_1", "tool": "bash"}
        }
        sys.stdout.write(json.dumps(perm_req) + "\n")
        sys.stdout.flush()
        perm_line = sys.stdin.readline().strip()
        perm_resp = json.loads(perm_line)
        assert perm_resp.get("result", {}).get("outcome", {}).get("outcome") == "approved"

        # 2. Emit >100KB thought chunk (Review Focus #2: oversized frame)
        big_thought = "T" * 120000
        notif1 = {
            "jsonrpc": "2.0",
            "method": "session/update",
            "params": {
                "sessionId": "sess_mock_1",
                "update": {
                    "sessionUpdate": "agent_thought_chunk",
                    "content": {"type": "text", "text": big_thought}
                }
            }
        }
        sys.stdout.write(json.dumps(notif1) + "\n")
        sys.stdout.flush()

        # 3. Emit message chunk
        notif2 = {
            "jsonrpc": "2.0",
            "method": "session/update",
            "params": {
                "sessionId": "sess_mock_1",
                "update": {
                    "sessionUpdate": "agent_message_chunk",
                    "content": {"type": "text", "text": "Hello from mock ACP!"}
                }
            }
        }
        sys.stdout.write(json.dumps(notif2) + "\n")
        sys.stdout.flush()

        # 4. Return final PromptResult
        resp = {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {"stopReason": "end_turn"}
        }
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()
    elif method == "session/close":
        resp = {"jsonrpc": "2.0", "id": req_id, "result": {}}
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()
`

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wiretapCount int
	w, err := acp.NewWorkerClient(ctx, "worker_test_1", acp.WorkerLaunchOptions{
		AccountID:        "acc_test",
		BinaryPath:       "python3",
		ExtraArgs:        []string{"-u", "-c", pyScript},
		InternalApproval: "auto_approve",
	}, func(workerID, direction, rawLine string) {
		wiretapCount++
	})
	if err != nil {
		t.Fatalf("NewWorkerClient failed: %v", err)
	}
	defer w.Close()

	initRes, err := w.Initialize(ctx, "zed", "0.175.0")
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}
	if initRes.ProtocolVersion != 1 || initRes.AgentInfo.Name != "Antigravity ACP Server" {
		t.Fatalf("unexpected InitializeResult: %+v", initRes)
	}

	// Test -32000 AuthRequired error classification
	_, err = w.NewSession(ctx, acp.NewSessionParams{Cwd: "/trigger/auth_error"})
	if !acp.IsAuthRequiredError(err) {
		t.Fatalf("expected IsAuthRequiredError(-32000) == true, got err=%v", err)
	}

	// Test -32002 ResourceNotFound error classification
	_, err = w.ResumeSession(ctx, acp.ResumeSessionParams{SessionID: "sess_expired", Cwd: "/tmp"})
	if !acp.IsResourceNotFoundError(err) {
		t.Fatalf("expected IsResourceNotFoundError(-32002) == true, got err=%v", err)
	}

	// Test valid NewSession
	sessRes, err := w.NewSession(ctx, acp.NewSessionParams{
		Cwd:        "/tmp",
		MCPServers: []acp.MCPServerEndpoint{},
	})
	if err != nil || sessRes.SessionID != "sess_mock_1" {
		t.Fatalf("NewSession failed: res=%+v, err=%v", sessRes, err)
	}

	// Test SetConfigOption (model, thinking_level -> thought_level fallback, mode: yolo -> auto fallback)
	if err := w.SetConfigOption(ctx, sessRes.SessionID, "model", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("SetConfigOption(model) failed: %v", err)
	}
	if err := w.SetConfigOption(ctx, sessRes.SessionID, "thinking_level", "high"); err != nil {
		t.Fatalf("SetConfigOption(thinking_level) failed: %v", err)
	}
	if err := w.SetConfigOption(ctx, sessRes.SessionID, "mode", "yolo"); err != nil {
		t.Fatalf("SetConfigOption(mode=yolo) failed: %v", err)
	}

	// Test session/prompt with >100KB thought chunk + permission auto-approval
	doneCh := make(chan error, 1)
	go func() {
		_, callErr := w.Call(ctx, "session/prompt", acp.PromptParams{
			SessionID: sessRes.SessionID,
			Prompt:    []acp.PromptBlock{{Type: "text", Text: "hi"}},
		})
		doneCh <- callErr
	}()

	var gotThoughtLen int
	var gotText string
	for gotText == "" {
		select {
		case ev := <-w.Events():
			if ev.Method == "session/update" {
				var up acp.SessionUpdateParams
				if err := json.Unmarshal(ev.Params, &up); err != nil {
					t.Fatalf("unmarshal SessionUpdateParams: %v", err)
				}
				switch up.Update.SessionUpdate {
				case "agent_thought_chunk":
					gotThoughtLen += len(up.Update.Content.Text)
				case "agent_message_chunk":
					gotText += up.Update.Content.Text
				}
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for session/update events")
		}
	}

	if err := <-doneCh; err != nil {
		t.Fatalf("session/prompt Call failed: %v", err)
	}
	if gotThoughtLen != 120000 {
		t.Fatalf("expected 120000 bytes of thought chunk, got %d", gotThoughtLen)
	}
	if gotText != "Hello from mock ACP!" {
		t.Fatalf("expected 'Hello from mock ACP!', got %q", gotText)
	}
	if wiretapCount == 0 {
		t.Fatalf("expected wiretap callback to be invoked")
	}
}

func TestParseACPUsageMarkdown(t *testing.T) {
	md := "### Session Usage\n- **Session total:** data not available yet\n\n### Quota Limits\n- **Gemini Models**\n  *(Models within this group: Gemini Flash, Gemini Pro)*\n  - **Weekly Limit Remaining:** 84% remaining (resets Nov 14, 2026, 00:12 UTC)\n  - **Five Hour Limit Remaining:** 97% remaining (resets Nov 08, 2026, 12:25 UTC)\n- **Claude and GPT models**\n  *(Models within this group: Claude Opus, Claude Sonnet, GPT-OSS)*\n  - **Weekly Limit Remaining:** 95% remaining (resets Nov 10, 2026, 21:04 UTC)\n  - **Five Hour Limit Remaining:** 100% remaining (resets Nov 08, 2026, 13:07 UTC)"
	groups, msg := acp.ParseACPUsageMarkdown(md)
	if msg != "额度读取正常" {
		t.Fatalf("expected status 额度读取正常, got %q", msg)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 quota groups, got %d", len(groups))
	}
	if groups[0].GroupName != "Gemini 模型" || len(groups[0].Windows) != 2 {
		t.Fatalf("unexpected group 0: %+v", groups[0])
	}
	if groups[0].Windows[0].Label != "每周" || groups[0].Windows[0].RemainingPct != 84 {
		t.Fatalf("unexpected gemini weekly window: %+v", groups[0].Windows[0])
	}
	if groups[0].Windows[1].Label != "5 小时" || groups[0].Windows[1].RemainingPct != 97 {
		t.Fatalf("unexpected gemini 5h window: %+v", groups[0].Windows[1])
	}
	if groups[1].GroupName != "Claude / GPT 模型" || groups[1].Windows[0].RemainingPct != 95 {
		t.Fatalf("unexpected claude group: %+v", groups[1])
	}

	// Test CLI `agy -p "/usage"` TSV format
	tsv := "Gemini Models\tWeekly Limit Remaining\t65%\t2026-11-14T00:12:59Z\nGemini Models\tFive Hour Limit Remaining\t97%\t2026-11-10T15:14:15Z\nClaude and GPT models\tWeekly Limit Remaining\t95%\t2026-11-10T21:04:14Z\nClaude and GPT models\tFive Hour Limit Remaining\t100%\t2026-11-10T11:10:09Z\n"
	tsvGroups, tsvMsg := acp.ParseACPUsageMarkdown(tsv)
	if tsvMsg != "额度读取正常" {
		t.Fatalf("expected TSV status 额度读取正常, got %q", tsvMsg)
	}
	if len(tsvGroups) != 2 {
		t.Fatalf("expected 2 TSV quota groups, got %d", len(tsvGroups))
	}
	if tsvGroups[0].GroupName != "Gemini 模型" || tsvGroups[0].Windows[0].RemainingPct != 65 || tsvGroups[0].Windows[1].RemainingPct != 97 {
		t.Fatalf("unexpected TSV group 0: %+v", tsvGroups[0])
	}
	if tsvGroups[1].GroupName != "Claude / GPT 模型" || tsvGroups[1].Windows[0].RemainingPct != 95 || tsvGroups[1].Windows[1].RemainingPct != 100 {
		t.Fatalf("unexpected TSV group 1: %+v", tsvGroups[1])
	}
}

func TestExtractModelsFromSessionResultAndPoolCache(t *testing.T) {
	pyModelWorker := `
import sys, json

seq = 0
for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    msg = json.loads(line)
    req_id = msg.get("id")
    method = msg.get("method", "")

    if method == "initialize":
        sys.stdout.write(json.dumps({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {"protocolVersion": 1, "agentInfo": {"name": "ACP", "version": "1.0"}}
        }) + "\n")
        sys.stdout.flush()
    elif method == "session/new":
        seq += 1
        sys.stdout.write(json.dumps({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "sessionId": f"s_{seq}",
                "models": {
                    "currentModelId": f"dynamic-model-{seq}",
                    "availableModels": [
                        {"modelId": f"dynamic-model-{seq}", "name": f"Dynamic Model {seq}"}
                    ]
                }
            }
        }) + "\n")
        sys.stdout.flush()
    elif method == "session/close":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": req_id, "result": {}}) + "\n")
        sys.stdout.flush()
`
	pool, err := acp.NewMultiAccountPool(t.TempDir(), config.ACPPoolConfig{
		BinaryPath:   "python3",
		WorkspaceDir: t.TempDir(),
		MaxWorkers:   2,
		ExtraArgs:    []string{"-u", "-c", pyModelWorker},
	}, config.SandboxPolicyConfig{InternalApproval: "auto_approve"}, nil, nil)
	if err != nil {
		t.Fatalf("NewMultiAccountPool failed: %v", err)
	}
	defer pool.Close()

	pool.SetModelsCacheTTL(200 * time.Millisecond)

	// 1. Cold cache -> queries ACP live (seq=1)
	ctx := context.Background()
	m1, ts1, err := pool.GetOrDiscoverModels(ctx, "", false)
	if err != nil || len(m1) != 1 || m1[0].ModelID != "dynamic-model-1" {
		t.Fatalf("expected dynamic-model-1 on cold cache, got models=%+v err=%v", m1, err)
	}

	// 2. Within TTL -> returns cached dynamic-model-1 without querying ACP
	m2, ts2, err := pool.GetOrDiscoverModels(ctx, "", false)
	if err != nil || len(m2) != 1 || m2[0].ModelID != "dynamic-model-1" || !ts2.Equal(ts1) {
		t.Fatalf("expected cache hit within TTL, got models=%+v ts1=%v ts2=%v", m2, ts1, ts2)
	}

	// 3. Wait for TTL expiration -> queries ACP live again (seq=2)
	time.Sleep(250 * time.Millisecond)
	m3, ts3, err := pool.GetOrDiscoverModels(ctx, "", false)
	if err != nil || len(m3) != 1 || m3[0].ModelID != "dynamic-model-2" || !ts3.After(ts1) {
		t.Fatalf("expected fresh ACP discovery after TTL expiry (dynamic-model-2), got models=%+v err=%v", m3, err)
	}

	// 4. Force refresh within TTL -> queries ACP live immediately (seq=3)
	m4, _, err := pool.GetOrDiscoverModels(ctx, "", true)
	if err != nil || len(m4) != 1 || m4[0].ModelID != "dynamic-model-3" {
		t.Fatalf("expected forceRefresh to bypass cache and return dynamic-model-3, got models=%+v err=%v", m4, err)
	}
}
