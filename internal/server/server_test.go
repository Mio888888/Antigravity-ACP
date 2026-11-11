package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/adapter"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/server"
	"antigravity-gateway/internal/storage"

	"github.com/gorilla/websocket"
)

func TestBuildPromptBlocksWithHistorySlidingWindowAndTokenEstimation(t *testing.T) {
	// Build a CanonicalRequest with >300KB of historical messages to simulate a 65-turn code review session
	var msgs []adapter.CanonicalMessage
	msgs = append(msgs, adapter.CanonicalMessage{
		Role:    "user",
		Content: "INITIAL_TASK_GOAL: Perform a complete code review of the entire repository.",
	})
	bigChunk := strings.Repeat("func exampleLineOfCode() { println(\"reviewing patch line\"); }\n", 120) // ~7.5KB per turn
	for i := 1; i <= 45; i++ {
		msgs = append(msgs,
			adapter.CanonicalMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("read_review_change({\"turn\":%d})", i),
			},
			adapter.CanonicalMessage{
				Role:    "tool",
				Content: fmt.Sprintf("TOOL_OUTPUT_TURN_%d:\n%s", i, bigChunk),
			},
		)
	}
	finalInstruction := "FINAL_TURN_INSTRUCTION: Now call complete_review with all collected findings."
	msgs = append(msgs, adapter.CanonicalMessage{
		Role:    "user",
		Content: finalInstruction,
	})

	canReq := &adapter.CanonicalRequest{
		Protocol:     "openai_responses",
		SystemPrompt: "System instructions",
		Messages:     msgs,
		PromptBlocks: []acp.PromptBlock{{Type: "text", Text: finalInstruction}},
		ToolResults:  []adapter.CanonicalToolResult{{CallID: "call_last", Content: bigChunk}},
	}

	blocks := server.BuildPromptBlocksWithHistory(canReq)
	if len(blocks) == 0 {
		t.Fatalf("expected non-empty prompt blocks")
	}
	fullPrompt := blocks[0].Text
	// Must stay well below agy_acp_server.par's ~184KB hard truncation threshold
	if len(fullPrompt) > 110*1024 {
		t.Fatalf("expected BuildPromptBlocksWithHistory output <= 110KB to prevent agy_acp_server.par tail truncation, got %d bytes", len(fullPrompt))
	}
	if !strings.Contains(fullPrompt, "INITIAL_TASK_GOAL: Perform a complete code review") {
		t.Fatalf("expected initial user task goal to be preserved at the head of history")
	}
	if !strings.HasSuffix(strings.TrimSpace(fullPrompt), finalInstruction) {
		t.Fatalf("expected final turn instruction to be preserved intact at the very end of prompt, got tail:\n%s", fullPrompt[len(fullPrompt)-200:])
	}
	if !strings.Contains(fullPrompt, "TOOL_OUTPUT_TURN_45") {
		t.Fatalf("expected most recent turn (turn 45) to be preserved in sliding window")
	}

	// Verify EstimatePromptTokens does NOT double-count ToolResults and PromptBlocks when Messages are already populated
	estWithDups := server.EstimatePromptTokens(canReq)
	canReqNoDups := &adapter.CanonicalRequest{
		Protocol:     "openai_responses",
		SystemPrompt: "System instructions",
		Messages:     msgs,
	}
	estClean := server.EstimatePromptTokens(canReqNoDups)
	if estWithDups != estClean {
		t.Fatalf("expected EstimatePromptTokens not to double-count PromptBlocks/ToolResults (%d != %d)", estWithDups, estClean)
	}

	// Scenario 2: Sailry post-review follow-up instruction ("全部修复吧， P0 级严重缺陷（安全与权限绕过）改为账号密码登录")
	// after a completed review report + dynamic tool unlock (`tool_search`).
	reviewReport12KB := "已完成全仓库代码审查，汇总如下：\n" + strings.Repeat("- 缺陷详情与修复建议：请重点关注 P0 认证绕过与 CORS 风险。\n", 140) + "\nFINAL_REVIEW_CONCLUSION_MARKER"
	postReviewMsgs := []adapter.CanonicalMessage{
		{
			Role:    "user",
			Content: "# Handoff Summary\nGoal:\n- Review 所有代码\n\nOpen tasks:\n- 使用 read_review_change 读取 web/src/App.tsx 剩余行",
		},
		{
			Role:    "assistant",
			Content: "read_review_change({\"path\":\"web/src/App.tsx\",\"offset\":800,\"limit\":400})",
		},
		{
			Role:    "tool",
			Content: "OLD_REVIEW_DIFF_SCRATCHPAD_MUST_BE_PRUNED",
		},
		{
			Role:    "assistant",
			Content: reviewReport12KB,
		},
		{
			Role:    "user",
			Content: "全部修复吧， P0 级严重缺陷（安全与权限绕过）改为账号密码登录\n<turn-context>\n{\"git_status\":\"33KB_BLOATED_GIT_STATUS\"}\n</turn-context>",
		},
		{
			Role:    "assistant",
			Content: "tool_search({\"query\":\"exec_command apply_patch\"})",
		},
		{
			Role:    "tool",
			Content: "[tool_result:call_ts] Loaded tools: exec_command, apply_patch",
		},
		{
			Role:    "user",
			Content: "<turn-context>\n{\"git_status\":\"33KB_BLOATED_GIT_STATUS\"}\n</turn-context>",
		},
	}
	postReviewReq := &adapter.CanonicalRequest{
		Protocol:    "openai_responses",
		Messages:    postReviewMsgs,
		ToolResults: []adapter.CanonicalToolResult{{CallID: "call_ts", Content: "Loaded tools: exec_command, apply_patch"}},
	}
	postReviewBlocks := server.BuildPromptBlocksWithHistory(postReviewReq)
	postPrompt := postReviewBlocks[0].Text
	if strings.Contains(postPrompt, "OLD_REVIEW_DIFF_SCRATCHPAD_MUST_BE_PRUNED") {
		t.Fatalf("expected obsolete review tool scratchpad before completed review report to be pruned")
	}
	if strings.Contains(postPrompt, "使用 read_review_change 读取 web/src/App.tsx") {
		t.Fatalf("expected stale Handoff Summary Open tasks to be stripped")
	}
	if strings.Contains(postPrompt, "33KB_BLOATED_GIT_STATUS") {
		t.Fatalf("expected <turn-context> blocks to be stripped from history")
	}
	if !strings.Contains(postPrompt, "FINAL_REVIEW_CONCLUSION_MARKER") {
		t.Fatalf("expected 12KB completed review report to be preserved intact without 3.5KB truncation")
	}
	if !strings.Contains(postPrompt, "[Current User Goal]\n全部修复吧， P0 级严重缺陷（安全与权限绕过）改为账号密码登录") {
		t.Fatalf("expected active [Current User Goal] to be explicitly highlighted when resuming after tool_search, got:\n%s", postPrompt)
	}
}

func TestGatewayServerMultiAccountToolBridgeAndACPWS(t *testing.T) {
	pyWorker := `
import sys, os, json, urllib.request, threading

mcp_url = ""
session_seq = 0
registered_tools = []
last_sys_append = ""
for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    msg = json.loads(line)
    req_id = msg.get("id")
    method = msg.get("method", "")
    params = msg.get("params", {})

    if method == "initialize":
        sys.stdout.write(json.dumps({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": 1,
                "agentInfo": {"name": "Antigravity ACP Server", "version": "1.4.5"}
            }
        }) + "\n")
        sys.stdout.flush()
    elif method == "session/new":
        session_seq += 1
        cwd = params.get("cwd", "")
        if not os.path.isdir(cwd):
            sys.stdout.write(json.dumps({
                "jsonrpc": "2.0",
                "id": req_id,
                "error": {"code": -32602, "message": "cwd does not exist: " + cwd}
            }) + "\n")
            sys.stdout.flush()
            continue
        meta = params.get("_meta", {}).get("antigravity", {})
        last_sys_append = meta.get("systemPrompt", {}).get("append", "")
        mcps = params.get("mcpServers", [])
        if mcps:
            mcp_url = mcps[0].get("url", "")
            try:
                list_req = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}).encode("utf-8")
                req = urllib.request.Request(mcp_url, data=list_req, headers={"Content-Type": "application/json"}, method="POST")
                with urllib.request.urlopen(req, timeout=5) as resp:
                    list_resp = json.loads(resp.read().decode("utf-8"))
                    registered_tools = [t["name"] for t in list_resp.get("result", {}).get("tools", [])]
            except Exception:
                pass
        sys.stdout.write(json.dumps({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "sessionId": "sess_py_1",
                "models": {
                    "currentModelId": "claude-sonnet-4-5",
                    "availableModels": [
                        {"modelId": "claude-sonnet-4-5", "name": "Claude Sonnet 4.5"},
                        {"modelId": "gemini-2.5-pro", "name": "Gemini 2.5 Pro"}
                    ]
                },
                "configOptions": [
                    {
                        "id": "model",
                        "name": "Model",
                        "category": "model",
                        "currentValue": "claude-sonnet-4-5",
                        "options": [
                            {"value": "claude-sonnet-4-5", "name": "Claude Sonnet 4.5", "description": "Anthropic Sonnet via ACP"},
                            {"value": "gpt-5-codex", "name": "GPT-5 Codex", "description": "OpenAI Responses model via ACP"}
                        ]
                    }
                ]
            }
        }) + "\n")
        sys.stdout.flush()
    elif method == "session/resume":
        mcps = params.get("mcpServers", [])
        if mcps:
            mcp_url = mcps[0].get("url", "")
        sys.stdout.write(json.dumps({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {"sessionId": params.get("sessionId", "sess_py_1")}
        }) + "\n")
        sys.stdout.flush()
    elif method == "session/cancel":
        continue
    elif method == "session/close":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": req_id, "result": {}}) + "\n")
        sys.stdout.flush()
    elif method == "session/set_config_option":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": req_id, "result": {}}) + "\n")
        sys.stdout.flush()
    elif method == "session/prompt":
        prompt_blocks = params.get("prompt", [])
        text_input = ""
        for b in prompt_blocks:
            if b.get("type") == "text":
                text_input += b.get("text", "")

        if "PARALLEL_TOOLS_TEST" in text_input and mcp_url:
            results = [None, None]
            def call_tool(idx, path_arg):
                mcp_req = json.dumps({
                    "jsonrpc": "2.0",
                    "id": 90 + idx,
                    "method": "tools/call",
                    "params": {"name": "read_file", "arguments": {"path": path_arg}}
                }).encode("utf-8")
                req = urllib.request.Request(mcp_url, data=mcp_req, headers={"Content-Type": "application/json"}, method="POST")
                with urllib.request.urlopen(req, timeout=10) as resp:
                    mcp_resp = json.loads(resp.read().decode("utf-8"))
                    results[idx] = mcp_resp["result"]["content"][0]["text"]
            t1 = threading.Thread(target=call_tool, args=(0, "a.go"))
            t2 = threading.Thread(target=call_tool, args=(1, "b.go"))
            t1.start()
            t2.start()
            t1.join()
            t2.join()
            notif = {
                "jsonrpc": "2.0",
                "method": "session/update",
                "params": {
                    "sessionId": "sess_py_1",
                    "update": {
                        "sessionUpdate": "agent_message_chunk",
                        "content": {"type": "text", "text": "Parallel results: " + str(results[0]) + " & " + str(results[1])}
                    }
                }
            }
            sys.stdout.write(json.dumps(notif) + "\n")
            sys.stdout.flush()
        elif "DYNAMIC_TOOL_UNLOCK" in text_input and "mcp__desktop__click" not in registered_tools and mcp_url:
            notif = {
                "jsonrpc": "2.0",
                "method": "session/update",
                "params": {
                    "sessionId": "sess_py_1",
                    "update": {
                        "sessionUpdate": "agent_message_chunk",
                        "content": {"type": "text", "text": "⟦REASONING⟧先搜索桌面控制工具⟦/REASONING⟧"}
                    }
                }
            }
            sys.stdout.write(json.dumps(notif) + "\n")
            sys.stdout.flush()
            mcp_req = json.dumps({
                "jsonrpc": "2.0",
                "id": 88,
                "method": "tools/call",
                "params": {"name": "tool_search", "arguments": {"query": "computer"}}
            }).encode("utf-8")
            req = urllib.request.Request(mcp_url, data=mcp_req, headers={"Content-Type": "application/json"}, method="POST")
            with urllib.request.urlopen(req, timeout=10) as resp:
                _ = resp.read()
        elif "DYNAMIC_TOOL_UNLOCK" in text_input and "mcp__desktop__click" in registered_tools:
            notif = {
                "jsonrpc": "2.0",
                "method": "session/update",
                "params": {
                    "sessionId": "sess_py_1",
                    "update": {
                        "sessionUpdate": "agent_message_chunk",
                        "content": {"type": "text", "text": "⟦REASONING⟧已获取桌面工具，准备点击⟦/REASONING⟧Unlocked tools: " + ",".join(registered_tools) + " | SysLen=" + str(len(last_sys_append))}
                    }
                }
            }
            sys.stdout.write(json.dumps(notif) + "\n")
            sys.stdout.flush()
        elif "CALL_LOCAL_TOOL" in text_input and mcp_url:
            mcp_req = json.dumps({
                "jsonrpc": "2.0",
                "id": 77,
                "method": "tools/call",
                "params": {"name": "read_file", "arguments": {"path": "README.md"}}
            }).encode("utf-8")
            req = urllib.request.Request(mcp_url, data=mcp_req, headers={"Content-Type": "application/json"}, method="POST")
            with urllib.request.urlopen(req, timeout=10) as resp:
                mcp_resp = json.loads(resp.read().decode("utf-8"))
            tool_out = mcp_resp["result"]["content"][0]["text"]
            notif = {
                "jsonrpc": "2.0",
                "method": "session/update",
                "params": {
                    "sessionId": "sess_py_1",
                    "update": {
                        "sessionUpdate": "agent_message_chunk",
                        "content": {"type": "text", "text": "Tool said: " + tool_out}
                    }
                }
            }
            sys.stdout.write(json.dumps(notif) + "\n")
            sys.stdout.flush()
        else:
            gem_home = os.environ.get("GEMINI_HOME", "shared")
            notif = {
                "jsonrpc": "2.0",
                "method": "session/update",
                "params": {
                    "sessionId": "sess_py_1",
                    "update": {
                        "sessionUpdate": "agent_message_chunk",
                        "content": {"type": "text", "text": "Reply from " + gem_home}
                    }
                }
            }
            sys.stdout.write(json.dumps(notif) + "\n")
            sys.stdout.flush()

        sys.stdout.write(json.dumps({
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {"stopReason": "end_turn"}
        }) + "\n")
        sys.stdout.flush()
`

	tmpDir := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:               "127.0.0.1",
			Port:               0,
			DataDir:            tmpDir,
			AdminToken:         "secret-admin",
			CORSAllowedOrigins: []string{"*"},
			SSEKeepAliveSec:    15,
		},
		ACPPool: config.ACPPoolConfig{
			BinaryPath:         "python3",
			WorkspaceDir:       filepath.Join(tmpDir, "workspaces"),
			ClientName:         "zed",
			ClientVersion:      "0.175.0",
			ProcessPerSession:  false,
			MinIdleWorkers:     0,
			MaxWorkers:         4,
			MaxIdleTimeSec:     60,
			ToolParkTimeoutSec: 30,
			ExtraArgs:          []string{"-u", "-c", pyWorker},
			Env:                map[string]string{},
		},
		PromptPolicy: config.PromptPolicyConfig{
			Mode:                "pure_passthrough",
			DisableBuiltinTools: true,
		},
		SandboxPolicy: config.SandboxPolicyConfig{
			SessionMode:      "yolo",
			InternalApproval: "auto_approve",
		},
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

	// Verify NO hardcoded models exist on startup
	if initial := pool.ListModels(); len(initial) != 0 {
		t.Fatalf("expected 0 hardcoded models on startup, got %v", initial)
	}

	sm := acp.NewSessionManager(cfg.ACPPool.WorkspaceDir, 30*time.Second)
	defer sm.Close()

	staticFS := fstest.MapFS{
		"index.html": {Data: []byte("<html><body>ReUI Admin</body></html>")},
	}
	gw := server.NewGatewayServer(cfg, store, pool, sm, hub, staticFS)
	httpSrv := httptest.NewServer(gw.Handler())
	defer httpSrv.Close()
	gw.SetLoopbackBaseURL(httpSrv.URL)

	// 1. Create an isolated account via Admin API (Review Focus #4)
	accBody := `{
		"id": "acc_iso",
		"name": "Isolated Team Account",
		"auth_mode": "oauth_personal",
		"isolation_enabled": true,
		"network_mode": "direct",
		"max_workers": 2,
		"weight": 1,
		"enabled": true
	}`
	reqAcc, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/admin/accounts", strings.NewReader(accBody))
	reqAcc.Header.Set("Authorization", "Bearer secret-admin")
	respAcc, err := http.DefaultClient.Do(reqAcc)
	if err != nil || respAcc.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/admin/accounts failed: status=%v, err=%v", respAcc, err)
	}
	_ = respAcc.Body.Close()

	// 2. Create an API key bound to acc_iso via Admin API
	keyBody := `{"name": "Team Iso Key", "bound_account_id": "acc_iso", "rate_limit_rpm": 100}`
	reqKey, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/admin/keys", strings.NewReader(keyBody))
	reqKey.Header.Set("Authorization", "Bearer secret-admin")
	respKey, err := http.DefaultClient.Do(reqKey)
	if err != nil || respKey.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/admin/keys failed: status=%v, err=%v", respKey, err)
	}
	var createdKeyResp struct {
		PlainKey string `json:"plain_key"`
	}
	_ = json.NewDecoder(respKey.Body).Decode(&createdKeyResp)
	_ = respKey.Body.Close()
	if !strings.HasPrefix(createdKeyResp.PlainKey, "sk-ag-") {
		t.Fatalf("expected plain_key with sk-ag- prefix, got %q", createdKeyResp.PlainKey)
	}

	// 2b. Test real-time ACP model discovery + TTL caching across OpenAI & Claude protocols on /v1/models
	reqModels1, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/v1/models", nil)
	reqModels1.Header.Set("x-api-key", createdKeyResp.PlainKey)
	reqModels1.Header.Set("anthropic-version", "2023-06-01")
	resModels1, err := http.DefaultClient.Do(reqModels1)
	if err != nil || resModels1.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models failed: status=%v, err=%v", resModels1, err)
	}
	var parsedModels1 struct {
		Object   string `json:"object"`
		FirstID  string `json:"first_id"`
		LastID   string `json:"last_id"`
		HasMore  bool   `json:"has_more"`
		CachedAt string `json:"cached_at"`
		Data     []struct {
			ID                       string              `json:"id"`
			Object                   string              `json:"object"`
			Type                     string              `json:"type"`
			DisplayName              string              `json:"display_name"`
			OwnedBy                  string              `json:"owned_by"`
			Description              string              `json:"description"`
			Services                 []string            `json:"services"`
			SupportedReasoningLevels []map[string]string `json:"supported_reasoning_levels"`
			DefaultReasoningLevel    string              `json:"default_reasoning_level"`
			Reasoning                bool                `json:"reasoning"`
			ToolCall                 bool                `json:"tool_call"`
			Vision                   bool                `json:"vision"`
			ImageInput               bool                `json:"imageInput"`
			ContextWindow            int                 `json:"contextWindow"`
			MaxOutputTokens          int                 `json:"maxOutputTokens"`
			Capabilities             struct {
				Streaming       bool     `json:"streaming"`
				InputModalities []string `json:"inputModalities"`
				ImageInput      bool     `json:"imageInput"`
				ContextWindow   int      `json:"contextWindow"`
				MaxOutputTokens int      `json:"maxOutputTokens"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resModels1.Body).Decode(&parsedModels1)
	_ = resModels1.Body.Close()
	if parsedModels1.Object != "list" || len(parsedModels1.Data) != 3 {
		t.Fatalf("expected 3 dynamically discovered ACP models (merged from models.availableModels + configOptions), got: %+v", parsedModels1)
	}
	if parsedModels1.Data[0].Object != "model" || parsedModels1.Data[0].Type != "model" || parsedModels1.Data[0].DisplayName == "" {
		t.Fatalf("expected dual OpenAI/Claude compatible model entry, got: %+v", parsedModels1.Data[0])
	}
	if len(parsedModels1.Data[0].SupportedReasoningLevels) != 3 ||
		parsedModels1.Data[0].SupportedReasoningLevels[0]["effort"] != "low" ||
		parsedModels1.Data[0].SupportedReasoningLevels[1]["effort"] != "medium" ||
		parsedModels1.Data[0].SupportedReasoningLevels[2]["effort"] != "high" ||
		parsedModels1.Data[0].DefaultReasoningLevel != "high" ||
		!parsedModels1.Data[0].Reasoning || !parsedModels1.Data[0].ToolCall || !parsedModels1.Data[0].Vision ||
		len(parsedModels1.Data[0].Services) != 1 || parsedModels1.Data[0].Services[0] != "text" ||
		!parsedModels1.Data[0].Capabilities.Streaming || !parsedModels1.Data[0].Capabilities.ImageInput ||
		len(parsedModels1.Data[0].Capabilities.InputModalities) != 2 ||
		parsedModels1.Data[0].Capabilities.ContextWindow <= 0 || parsedModels1.Data[0].Capabilities.MaxOutputTokens <= 0 {
		t.Fatalf("expected supported_reasoning_levels [low medium high] and full capabilities on /v1/models entry, got: %+v", parsedModels1.Data[0])
	}
	if parsedModels1.FirstID != "claude-sonnet-4-5" || parsedModels1.LastID != "gemini-2.5-pro" {
		t.Fatalf("expected first_id=claude-sonnet-4-5 and last_id=gpt-5-codex, got first=%q last=%q", parsedModels1.FirstID, parsedModels1.LastID)
	}

	// Verify single-model endpoint GET /v1/models/gpt-5-codex works
	reqSingleModel, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/v1/models/gpt-5-codex", nil)
	reqSingleModel.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	resSingleModel, err := http.DefaultClient.Do(reqSingleModel)
	if err != nil || resSingleModel.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/models/gpt-5-codex failed: status=%v, err=%v", resSingleModel, err)
	}
	_ = resSingleModel.Body.Close()

	// 3. Call /v1/chat/completions (OpenAI compatible protocol, streaming SSE) with the bound API key and verify it ran in acc_iso's isolated GEMINI_HOME!
	oaBody := `{"model":"gemini-2.5-pro","stream":true,"messages":[{"role":"user","content":"Hello isolated"}]}`
	reqOA, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/chat/completions", strings.NewReader(oaBody))
	reqOA.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	respOA, err := http.DefaultClient.Do(reqOA)
	if err != nil || respOA.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions failed: status=%v, err=%v", respOA, err)
	}
	if respOA.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected X-Accel-Buffering: no header")
	}
	oaSSE, _ := io.ReadAll(respOA.Body)
	_ = respOA.Body.Close()
	if !strings.Contains(string(oaSSE), "acc_iso") {
		t.Fatalf("expected response from acc_iso isolated GEMINI_HOME, got:\n%s", string(oaSSE))
	}

	// 3b. Call /v1/responses (OpenAI Official Responses protocol) and verify response object & session affinity
	respAPIBody := `{"model":"gemini-2.5-pro","stream":false,"instructions":"Be concise","input":"Hello via official responses API"}`
	reqRespAPI, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", strings.NewReader(respAPIBody))
	reqRespAPI.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	resRespAPI, err := http.DefaultClient.Do(reqRespAPI)
	if err != nil || resRespAPI.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/responses failed: status=%v, err=%v", resRespAPI, err)
	}
	var parsedRespAPI struct {
		ID         string `json:"id"`
		Object     string `json:"object"`
		OutputText string `json:"output_text"`
	}
	_ = json.NewDecoder(resRespAPI.Body).Decode(&parsedRespAPI)
	_ = resRespAPI.Body.Close()
	if parsedRespAPI.Object != "response" || !strings.HasPrefix(parsedRespAPI.ID, "resp_") || !strings.Contains(parsedRespAPI.OutputText, "acc_iso") {
		t.Fatalf("unexpected /v1/responses payload: %+v", parsedRespAPI)
	}

	// 4. Test 2-Turn Remote Server <-> Local Client Tool Call Park & Resume over /v1/messages (Claude protocol)
	turn1Body := `{
		"model": "claude-sonnet-4-5",
		"stream": false,
		"messages": [{"role": "user", "content": "CALL_LOCAL_TOOL now"}],
		"tools": [{"name": "read_file", "description": "Read local file", "input_schema": {"type": "object"}}]
	}`
	reqT1, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/messages", bytes.NewBufferString(turn1Body))
	reqT1.Header.Set("x-api-key", createdKeyResp.PlainKey)
	reqT1.Header.Set("X-ACP-Session-ID", "conv_tool_loop")
	respT1, err := http.DefaultClient.Do(reqT1)
	if err != nil || respT1.StatusCode != http.StatusOK {
		t.Fatalf("Turn 1 POST /v1/messages failed: status=%v, err=%v", respT1, err)
	}
	var t1JSON struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content"`
	}
	_ = json.NewDecoder(respT1.Body).Decode(&t1JSON)
	_ = respT1.Body.Close()
	if t1JSON.StopReason != "tool_use" || len(t1JSON.Content) == 0 {
		t.Fatalf("expected stop_reason=tool_use on Turn 1, got %+v", t1JSON)
	}
	callID := t1JSON.Content[len(t1JSON.Content)-1].ID

	// Turn 2: Local client submits tool_result
	turn2Body := `{
		"model": "claude-sonnet-4-5",
		"stream": false,
		"messages": [
			{"role": "user", "content": "CALL_LOCAL_TOOL now"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "` + callID + `", "name": "read_file", "input": {"path": "README.md"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "` + callID + `", "content": "LOCAL_FILE_CONTENTS_42"}]}
		]
	}`
	reqT2, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/messages", bytes.NewBufferString(turn2Body))
	reqT2.Header.Set("x-api-key", createdKeyResp.PlainKey)
	reqT2.Header.Set("X-ACP-Session-ID", "conv_tool_loop")
	respT2, err := http.DefaultClient.Do(reqT2)
	if err != nil || respT2.StatusCode != http.StatusOK {
		t.Fatalf("Turn 2 POST /v1/messages failed: status=%v, err=%v", respT2, err)
	}
	rawT2, _ := io.ReadAll(respT2.Body)
	_ = respT2.Body.Close()
	if !strings.Contains(string(rawT2), "Tool said: LOCAL_FILE_CONTENTS_42") {
		t.Fatalf("expected final answer containing local tool output, got:\n%s", string(rawT2))
	}

	// 4b. Test Sailry-style Dynamic Tool Unlock (tool_search -> new tools + expanded instructions) over /v1/responses
	sailryStep1 := `{
		"model": "gemini-3.8-flash",
		"stream": false,
		"instructions": "Base Sailry Instructions",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "DYNAMIC_TOOL_UNLOCK open browser"}]}
		],
		"tools": [
			{"type": "function", "name": "tool_search", "description": "Search tools", "parameters": {"type": "object"}}
		]
	}`
	reqS1, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", bytes.NewBufferString(sailryStep1))
	reqS1.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	respS1, err := http.DefaultClient.Do(reqS1)
	if err != nil || respS1.StatusCode != http.StatusOK {
		t.Fatalf("Sailry Step 1 POST /v1/responses failed: status=%v, err=%v", respS1, err)
	}
	var s1JSON struct {
		Output []struct {
			Type    string `json:"type"`
			CallID  string `json:"call_id"`
			Name    string `json:"name"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"output"`
	}
	_ = json.NewDecoder(respS1.Body).Decode(&s1JSON)
	_ = respS1.Body.Close()
	var s1Reasoning string
	for _, out := range s1JSON.Output {
		if out.Type == "reasoning" {
			for _, sPart := range out.Summary {
				s1Reasoning += sPart.Text
			}
		}
	}
	if !strings.Contains(s1Reasoning, "先搜索桌面控制工具") {
		t.Fatalf("expected reasoning output item from ⟦REASONING⟧ on Sailry Step 1, got %q", s1Reasoning)
	}
	var searchCallID string
	for _, out := range s1JSON.Output {
		if out.Type == "function_call" && out.Name == "tool_search" {
			searchCallID = out.CallID
		}
	}
	if searchCallID == "" {
		t.Fatalf("expected function_call for tool_search on Sailry Step 1, got %+v", s1JSON.Output)
	}

	// Step 2: Sailry returns function_call_output AND dynamically adds mcp__desktop__click to tools AND grows instructions!
	sailryStep2 := `{
		"model": "gemini-3.8-flash",
		"stream": false,
		"instructions": "Base Sailry Instructions\n\n# Computer Use Plugin Instructions\nAlways call get_app_state first.",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "DYNAMIC_TOOL_UNLOCK open browser"}]},
			{"type": "function_call", "call_id": "` + searchCallID + `", "name": "tool_search", "arguments": "{\"query\":\"computer\"}"},
			{"type": "function_call_output", "call_id": "` + searchCallID + `", "output": "{\"tools\":[{\"name\":\"mcp__desktop__click\"}]}"}
		],
		"tools": [
			{"type": "function", "name": "tool_search", "description": "Search tools", "parameters": {"type": "object"}},
			{"type": "function", "name": "mcp__desktop__click", "description": "Click desktop element", "parameters": {"type": "object"}}
		]
	}`
	reqS2, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", bytes.NewBufferString(sailryStep2))
	reqS2.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	respS2, err := http.DefaultClient.Do(reqS2)
	if err != nil || respS2.StatusCode != http.StatusOK {
		t.Fatalf("Sailry Step 2 POST /v1/responses failed: status=%v, err=%v", respS2, err)
	}
	var s2JSON struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
		} `json:"output"`
	}
	_ = json.NewDecoder(respS2.Body).Decode(&s2JSON)
	_ = respS2.Body.Close()
	if !strings.Contains(s2JSON.OutputText, "mcp__desktop__click") {
		t.Fatalf("expected dynamically added mcp__desktop__click to be registered via tools/list in ACP worker, got %q", s2JSON.OutputText)
	}
	var s2Reasoning string
	for _, out := range s2JSON.Output {
		if out.Type == "reasoning" {
			for _, sPart := range out.Summary {
				s2Reasoning += sPart.Text
			}
		}
	}
	if !strings.Contains(s2Reasoning, "已获取桌面工具") {
		t.Fatalf("expected reasoning output item on Sailry Step 2, got %q", s2Reasoning)
	}

	// 4b-stream. Test Streaming /v1/responses suppresses intermediate reasoning items on tool-continuation turns
	sailryStreamStep1 := `{
		"model": "gemini-3.8-flash",
		"stream": true,
		"instructions": "Base Sailry Stream Instructions",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "DYNAMIC_TOOL_UNLOCK stream check"}]}
		],
		"tools": [
			{"type": "function", "name": "tool_search", "description": "Search tools", "parameters": {"type": "object"}}
		]
	}`
	reqSS1, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", bytes.NewBufferString(sailryStreamStep1))
	reqSS1.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	respSS1, err := http.DefaultClient.Do(reqSS1)
	if err != nil || respSS1.StatusCode != http.StatusOK {
		t.Fatalf("Sailry Stream Step 1 POST /v1/responses failed: status=%v, err=%v", respSS1, err)
	}
	rawSS1, _ := io.ReadAll(respSS1.Body)
	_ = respSS1.Body.Close()
	sseSS1 := string(rawSS1)
	if !strings.Contains(sseSS1, "response.reasoning_summary_text.delta") || !strings.Contains(sseSS1, "先搜索桌面控制工具") {
		t.Fatalf("expected initial streaming turn to emit reasoning summary delta, got:\n%s", sseSS1)
	}
	var streamSearchCallID string
	for _, line := range strings.Split(sseSS1, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"type":"function_call"`) {
			var ev struct {
				Item struct {
					CallID string `json:"call_id"`
				} `json:"item"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil && ev.Item.CallID != "" {
				streamSearchCallID = ev.Item.CallID
				break
			}
		}
	}
	if streamSearchCallID == "" {
		t.Fatalf("expected function_call call_id in initial streaming turn SSE, got:\n%s", sseSS1)
	}

	sailryStreamStep2 := `{
		"model": "gemini-3.8-flash",
		"stream": true,
		"instructions": "Base Sailry Stream Instructions",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "DYNAMIC_TOOL_UNLOCK stream check"}]},
			{"type": "function_call", "call_id": "` + streamSearchCallID + `", "name": "tool_search", "arguments": "{\"query\":\"computer\"}"},
			{"type": "function_call_output", "call_id": "` + streamSearchCallID + `", "output": "{\"tools\":[{\"name\":\"mcp__desktop__click\"}]}"}
		],
		"tools": [
			{"type": "function", "name": "tool_search", "description": "Search tools", "parameters": {"type": "object"}},
			{"type": "function", "name": "mcp__desktop__click", "description": "Click desktop element", "parameters": {"type": "object"}}
		]
	}`
	reqSS2, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", bytes.NewBufferString(sailryStreamStep2))
	reqSS2.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	respSS2, err := http.DefaultClient.Do(reqSS2)
	if err != nil || respSS2.StatusCode != http.StatusOK {
		t.Fatalf("Sailry Stream Step 2 POST /v1/responses failed: status=%v, err=%v", respSS2, err)
	}
	rawSS2, _ := io.ReadAll(respSS2.Body)
	_ = respSS2.Body.Close()
	sseSS2 := string(rawSS2)
	if strings.Contains(sseSS2, "response.reasoning_summary_text.delta") || strings.Contains(sseSS2, `"type":"reasoning"`) {
		t.Fatalf("expected streaming tool-continuation turn to suppress client reasoning items so tool calls fold cleanly, got:\n%s", sseSS2)
	}
	if !strings.Contains(sseSS2, "mcp__desktop__click") {
		t.Fatalf("expected streaming tool-continuation turn to still emit final output text, got:\n%s", sseSS2)
	}

	// 4c. Test Parallel Tool Calls Batching + Trailing <turn-context> User Block on Resume over /v1/responses
	parStep1 := `{
		"model": "gemini-4-argon",
		"stream": false,
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "PARALLEL_TOOLS_TEST read both files"}]}
		],
		"tools": [
			{"type": "function", "name": "read_file", "description": "Read file", "parameters": {"type": "object"}}
		]
	}`
	reqP1, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", bytes.NewBufferString(parStep1))
	reqP1.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	respP1, err := http.DefaultClient.Do(reqP1)
	if err != nil || respP1.StatusCode != http.StatusOK {
		t.Fatalf("Parallel Step 1 POST /v1/responses failed: status=%v, err=%v", respP1, err)
	}
	var p1JSON struct {
		ID     string `json:"id"`
		Output []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Name   string `json:"name"`
		} `json:"output"`
	}
	_ = json.NewDecoder(respP1.Body).Decode(&p1JSON)
	_ = respP1.Body.Close()
	var parCallIDs []string
	for _, out := range p1JSON.Output {
		if out.Type == "function_call" && out.CallID != "" {
			parCallIDs = append(parCallIDs, out.CallID)
		}
	}
	if len(parCallIDs) != 2 {
		t.Fatalf("expected 2 parallel function_call items batched in a single /v1/responses turn, got %d: %+v", len(parCallIDs), p1JSON.Output)
	}

	// Resume both parallel calls WITH a trailing <turn-context> user block (like Sailry does!)
	parStep2 := `{
		"model": "gemini-4-argon",
		"stream": false,
		"previous_response_id": "` + p1JSON.ID + `",
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "PARALLEL_TOOLS_TEST read both files"}]},
			{"type": "function_call", "call_id": "` + parCallIDs[0] + `", "name": "read_file", "arguments": "{\"path\":\"a.go\"}"},
			{"type": "function_call", "call_id": "` + parCallIDs[1] + `", "name": "read_file", "arguments": "{\"path\":\"b.go\"}"},
			{"type": "function_call_output", "call_id": "` + parCallIDs[0] + `", "output": "FILE_A_OK"},
			{"type": "function_call_output", "call_id": "` + parCallIDs[1] + `", "output": "FILE_B_OK"},
			{"role": "user", "content": [{"type": "input_text", "text": "<turn-context>\nRetained owner context\n</turn-context>"}]}
		],
		"tools": [
			{"type": "function", "name": "read_file", "description": "Read file", "parameters": {"type": "object"}}
		]
	}`
	reqP2, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/v1/responses", bytes.NewBufferString(parStep2))
	reqP2.Header.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	respP2, err := http.DefaultClient.Do(reqP2)
	if err != nil || respP2.StatusCode != http.StatusOK {
		t.Fatalf("Parallel Step 2 POST /v1/responses failed: status=%v, err=%v", respP2, err)
	}
	var p2JSON struct {
		OutputText string `json:"output_text"`
	}
	_ = json.NewDecoder(respP2.Body).Decode(&p2JSON)
	_ = respP2.Body.Close()
	if !strings.Contains(p2JSON.OutputText, "FILE_A_OK") || !strings.Contains(p2JSON.OutputText, "FILE_B_OK") {
		t.Fatalf("expected resumed parallel tool outputs in response, got %q", p2JSON.OutputText)
	}
	if sm.ParkedCount() != 0 {
		t.Fatalf("expected 0 leaked parked tool calls after parallel resume, got %d", sm.ParkedCount())
	}

	// 5. Test Remote ACP WebSocket tunnel /v1/acp/ws with non-existent local laptop cwd rewriting (Review Focus #5)
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/v1/acp/ws"
	wsHeaders := http.Header{}
	wsHeaders.Set("Authorization", "Bearer "+createdKeyResp.PlainKey)
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, wsHeaders)
	if err != nil {
		t.Fatalf("Dial /v1/acp/ws failed: %v", err)
	}

	// Send session/new with non-existent laptop path
	_ = wsConn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":10,"method":"session/new","params":{"cwd":"/Users/nonexistent_laptop_user/my_repo","mcpServers":[]}}`))
	_, wsMsg, err := wsConn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage from /v1/acp/ws failed: %v", err)
	}
	if !strings.Contains(string(wsMsg), `"sess_py_1"`) {
		t.Fatalf("expected rewritten remote cwd to allow session/new to succeed, got: %s", string(wsMsg))
	}
	_ = wsConn.Close()

	// 6. Verify rate limiting (RPM) and admin auth protection
	rpmKeyBody := `{"name": "RPM Test Key", "bound_account_id": "acc_iso", "rate_limit_rpm": 2}`
	reqRPMKey, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/admin/keys", strings.NewReader(rpmKeyBody))
	reqRPMKey.Header.Set("Authorization", "Bearer secret-admin")
	respRPMKey, err := http.DefaultClient.Do(reqRPMKey)
	if err != nil || respRPMKey.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/admin/keys for RPM key failed: status=%v, err=%v", respRPMKey, err)
	}
	var rpmKeyResp struct {
		PlainKey string `json:"plain_key"`
	}
	_ = json.NewDecoder(respRPMKey.Body).Decode(&rpmKeyResp)
	_ = respRPMKey.Body.Close()

	for i := 1; i <= 3; i++ {
		reqM, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/v1/models", nil)
		reqM.Header.Set("Authorization", "Bearer "+rpmKeyResp.PlainKey)
		respM, err := http.DefaultClient.Do(reqM)
		if err != nil {
			t.Fatalf("GET /v1/models #%d failed: %v", i, err)
		}
		_ = respM.Body.Close()
		if i <= 2 && respM.StatusCode != http.StatusOK {
			t.Fatalf("expected request #%d within RPM limit to succeed (200), got %d", i, respM.StatusCode)
		}
		if i == 3 && respM.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected request #3 exceeding RPM=2 to return 429 Too Many Requests, got %d", respM.StatusCode)
		}
	}

	// Verify unauthenticated request to /api/admin/overview is rejected with 401
	reqUnauthAdmin, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/api/admin/overview", nil)
	respUnauthAdmin, err := http.DefaultClient.Do(reqUnauthAdmin)
	if err != nil {
		t.Fatalf("GET /api/admin/overview failed: %v", err)
	}
	_ = respUnauthAdmin.Body.Close()
	if respUnauthAdmin.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated /api/admin/overview to return 401, got %d", respUnauthAdmin.StatusCode)
	}

	// 7. Verify Web Admin Password Login, Session Token, Cookie Auth, Password Change, and Logout
	reqBadLogin, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/admin/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
	reqBadLogin.Header.Set("Content-Type", "application/json")
	respBadLogin, err := http.DefaultClient.Do(reqBadLogin)
	if err != nil {
		t.Fatalf("POST /api/admin/auth/login (bad) failed: %v", err)
	}
	_ = respBadLogin.Body.Close()
	if respBadLogin.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected wrong password login to return 401, got %d", respBadLogin.StatusCode)
	}

	reqGoodLogin, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/admin/auth/login", strings.NewReader(`{"password":"secret-admin","remember":true}`))
	reqGoodLogin.Header.Set("Content-Type", "application/json")
	respGoodLogin, err := http.DefaultClient.Do(reqGoodLogin)
	if err != nil || respGoodLogin.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/admin/auth/login (good) failed: status=%v, err=%v", respGoodLogin, err)
	}
	var loginResp struct {
		Authenticated bool   `json:"authenticated"`
		SessionToken  string `json:"session_token"`
	}
	_ = json.NewDecoder(respGoodLogin.Body).Decode(&loginResp)
	_ = respGoodLogin.Body.Close()
	if !loginResp.Authenticated || !strings.HasPrefix(loginResp.SessionToken, "ag_sess_") {
		t.Fatalf("expected valid session_token from login, got %+v", loginResp)
	}

	// Verify session token grants access to /api/admin/auth/session and /api/admin/overview
	reqSessCheck, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/api/admin/auth/session", nil)
	reqSessCheck.Header.Set("Authorization", "Bearer "+loginResp.SessionToken)
	respSessCheck, err := http.DefaultClient.Do(reqSessCheck)
	if err != nil || respSessCheck.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/admin/auth/session with session token failed: status=%v, err=%v", respSessCheck, err)
	}
	_ = respSessCheck.Body.Close()

	// Verify per-account model disabling via PUT /api/admin/accounts/default/disabled-models
	reqDisableModel, _ := http.NewRequest(
		http.MethodPut,
		httpSrv.URL+"/api/admin/accounts/default/disabled-models",
		strings.NewReader(`{"model_id":"gemini-3-flash","disabled":true}`),
	)
	reqDisableModel.Header.Set("Authorization", "Bearer "+loginResp.SessionToken)
	reqDisableModel.Header.Set("Content-Type", "application/json")
	respDisableModel, err := http.DefaultClient.Do(reqDisableModel)
	if err != nil || respDisableModel.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/admin/accounts/default/disabled-models failed: status=%v, err=%v", respDisableModel, err)
	}
	var disableResp struct {
		DisabledModels []string `json:"disabled_models"`
	}
	_ = json.NewDecoder(respDisableModel.Body).Decode(&disableResp)
	_ = respDisableModel.Body.Close()
	if len(disableResp.DisabledModels) != 1 || disableResp.DisabledModels[0] != "gemini-3-flash" {
		t.Fatalf("unexpected disabled_models response: %+v", disableResp)
	}

	// Re-enable all models on default account
	reqEnableAll, _ := http.NewRequest(
		http.MethodPut,
		httpSrv.URL+"/api/admin/accounts/default/disabled-models",
		strings.NewReader(`{"disabled_models":[]}`),
	)
	reqEnableAll.Header.Set("Authorization", "Bearer "+loginResp.SessionToken)
	reqEnableAll.Header.Set("Content-Type", "application/json")
	respEnableAll, err := http.DefaultClient.Do(reqEnableAll)
	if err != nil || respEnableAll.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/admin/accounts/default/disabled-models (clear) failed: status=%v, err=%v", respEnableAll, err)
	}
	_ = respEnableAll.Body.Close()

	// Verify changing password works and revokes old session token
	reqChangePwd, _ := http.NewRequest(
		http.MethodPost,
		httpSrv.URL+"/api/admin/auth/password",
		strings.NewReader(`{"current_password":"secret-admin","new_password":"new-admin-pass-2026"}`),
	)
	reqChangePwd.Header.Set("Authorization", "Bearer "+loginResp.SessionToken)
	reqChangePwd.Header.Set("Content-Type", "application/json")
	respChangePwd, err := http.DefaultClient.Do(reqChangePwd)
	if err != nil || respChangePwd.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/admin/auth/password failed: status=%v, err=%v", respChangePwd, err)
	}
	var changePwdResp struct {
		Updated      bool   `json:"updated"`
		SessionToken string `json:"session_token"`
	}
	_ = json.NewDecoder(respChangePwd.Body).Decode(&changePwdResp)
	_ = respChangePwd.Body.Close()
	if !changePwdResp.Updated || !strings.HasPrefix(changePwdResp.SessionToken, "ag_sess_") {
		t.Fatalf("unexpected password change response: %+v", changePwdResp)
	}

	// Old session token must now be invalid (401)
	reqOldSess, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/api/admin/auth/session", nil)
	reqOldSess.Header.Set("Authorization", "Bearer "+loginResp.SessionToken)
	respOldSess, err := http.DefaultClient.Do(reqOldSess)
	if err != nil {
		t.Fatalf("GET /api/admin/auth/session (old session) failed: %v", err)
	}
	_ = respOldSess.Body.Close()
	if respOldSess.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old session token to be revoked after password change, got %d", respOldSess.StatusCode)
	}

	// Logout the new session token and verify it is invalidated
	reqLogout, _ := http.NewRequest(http.MethodPost, httpSrv.URL+"/api/admin/auth/logout", nil)
	reqLogout.Header.Set("Authorization", "Bearer "+changePwdResp.SessionToken)
	respLogout, err := http.DefaultClient.Do(reqLogout)
	if err != nil || respLogout.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/admin/auth/logout failed: status=%v, err=%v", respLogout, err)
	}
	_ = respLogout.Body.Close()

	reqAfterLogout, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/api/admin/overview", nil)
	reqAfterLogout.Header.Set("Authorization", "Bearer "+changePwdResp.SessionToken)
	respAfterLogout, err := http.DefaultClient.Do(reqAfterLogout)
	if err != nil {
		t.Fatalf("GET /api/admin/overview (after logout) failed: %v", err)
	}
	_ = respAfterLogout.Body.Close()
	if respAfterLogout.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected logged-out session token to return 401, got %d", respAfterLogout.StatusCode)
	}
}

func TestClientFirstACPCompactionLifecycle(t *testing.T) {
	sm := acp.NewSessionManager(t.TempDir(), 5*time.Second)
	defer sm.Close()

	sess := sm.RegisterOrMatchSession("default", "conv_compaction_test", "hash_1", nil, "acp_sess_1", nil)

	// 1. Before hitting the ~225K watermark, northbound token usage should reflect actual tokens
	normalReq := &adapter.CanonicalRequest{
		Protocol:      "openai_responses",
		Model:         "gemini-4-argon",
		ContextWindow: 272000,
		PromptBlocks:  []acp.PromptBlock{{Type: "text", Text: "Read file A"}},
	}
	sess.SetLastTokenUsage(120000, 90000)
	// Simulate a fast tool call turn where transcript.jsonl hasn't flushed stepInfo.inputTokens yet (passes 3000 delta)
	repIn, repCached := server.ComputeNorthboundUsageInputTokens(normalReq, sess, 3000, 0)
	if repIn <= 120000 {
		t.Fatalf("expected un-flushed tool step to use LastInputTokens + delta (>120000), got %d", repIn)
	}
	if repCached != 90000 {
		t.Fatalf("expected cached tokens to carry forward 90000, got %d", repCached)
	}
	if reqFlag, _ := sess.GetClientCompactionState(); reqFlag {
		t.Fatalf("did not expect client compaction to be requested at 120K tokens")
	}

	// 2. When ACP session approaches localharness's ~255K-260K CHECKPOINT threshold (e.g. 232K >= 225K),
	// the gateway marks the session for local compaction on the next turn but reports the real
	// usage instead of inflating it to the client's compaction watermark.
	sess.SetLastTokenUsage(232000, 180000)
	repInHigh, _ := server.ComputeNorthboundUsageInputTokens(normalReq, sess, 232000, 180000)
	reqFlag, waitTurns := sess.GetClientCompactionState()
	if !reqFlag || waitTurns != 0 {
		t.Fatalf("expected ClientCompactionRequested=true and waitTurns=0 at 232K tokens, got req=%v wait=%d", reqFlag, waitTurns)
	}
	if repInHigh != 232000 {
		t.Fatalf("expected reported input tokens to equal the real 232000 ACP usage, got %d", repInHigh)
	}
	carbonReq := &adapter.CanonicalRequest{
		Protocol:     "openai_responses",
		Model:        "gemini-4-carbon",
		PromptBlocks: []acp.PromptBlock{{Type: "text", Text: "continue"}},
	}
	if repCarbon, _ := server.ComputeNorthboundUsageInputTokens(carbonReq, sess, 240000, 0); repCarbon != 240000 {
		t.Fatalf("expected 1M-window model usage to be reported as-is (240000) instead of ~996K, got %d", repCarbon)
	}

	// 3. Verify IsClientCompactionRequest and immediate abort of parked tool calls when client compacts mid-tool-loop
	parked, err := sm.ParkToolCall(sess.BridgeID, "read_file", `{"path":"deploy/deploy.sh"}`)
	if err != nil || parked == nil {
		t.Fatalf("ParkToolCall failed: %v", err)
	}
	if !sm.HasParkedCallsForSession(sess.BridgeID) {
		t.Fatalf("expected session to have parked tool call")
	}

	compactionReq := &adapter.CanonicalRequest{
		Protocol:     "openai_responses",
		Model:        "gemini-4-argon",
		SystemPrompt: "## Task\nYou are writing a handoff for yourself because this conversation has reached its context budget.\n" + strings.Repeat("RAW_HISTORY_LINE ", 12000),
		PromptBlocks: []acp.PromptBlock{{Type: "text", Text: "Please summarize the conversation history provided in the system prompt."}},
	}
	if !server.IsClientCompactionRequest(compactionReq) {
		t.Fatalf("expected IsClientCompactionRequest to return true for Sailry handoff prompt")
	}
	aborted := sm.AbortParkedCallsForSession(sess, "default", "aborted for client compaction")
	if aborted != 1 || sm.ParkedCount() != 0 {
		t.Fatalf("expected 1 parked tool call to be immediately aborted, got aborted=%d remaining=%d", aborted, sm.ParkedCount())
	}
	select {
	case res := <-parked.ResultCh:
		if !res.IsError || !strings.Contains(res.Content, "aborted for client compaction") {
			t.Fatalf("unexpected aborted tool result payload: %+v", res)
		}
	default:
		t.Fatalf("expected parked.ResultCh to receive immediate abort payload without waiting 180s")
	}

	// 4. Verify IsPostCompactionResumeRequest detection
	resumeReq := &adapter.CanonicalRequest{
		Protocol: "openai_responses",
		Model:    "gemini-4-argon",
		Messages: []adapter.CanonicalMessage{
			{Role: "user", Content: "# Resuming from a compaction\n\n# Handoff Summary\nGoal: test"},
		},
	}
	if !server.IsPostCompactionResumeRequest(resumeReq) {
		t.Fatalf("expected IsPostCompactionResumeRequest to return true")
	}

	// 5. Verify automatic reset of ClientCompactionRequested when ACP token usage drops after compaction
	sess.SetLastTokenUsage(32000, 12000)
	if reqAfterDrop, _ := sess.GetClientCompactionState(); reqAfterDrop {
		t.Fatalf("expected ClientCompactionRequested to reset to false when token usage drops from 232K to 32K")
	}

	// 6. After a local rotation the stale watermark must be cleared so the next turn does not rotate again.
	sess.SetLastTokenUsage(240000, 200000)
	sess.SetClientCompactionState(true, 0)
	sess.ResetTokenUsage()
	if in, cached := sess.GetLastTokenUsage(); in != 0 || cached != 0 {
		t.Fatalf("expected ResetTokenUsage to clear token usage, got in=%d cached=%d", in, cached)
	}
	if req, _ := sess.GetClientCompactionState(); req {
		t.Fatalf("expected ResetTokenUsage to clear ClientCompactionRequested")
	}
}

func TestStripStaleHandoffOpenTasksPreservesLaterSections(t *testing.T) {
	handoff := "# Handoff Summary\n## Goal\nFix login\n\nOpen tasks:\n- STALE_OPEN_TASK\n\n## Constraints\n- KEEP_CONSTRAINT\n\nNext step:\n- STALE_NEXT_STEP\n\n## State\nKEEP_STATE\n\n## Working set\n- KEEP_FILE internal/server/router.go"
	msgs := []adapter.CanonicalMessage{
		{Role: "user", Content: handoff},
		{Role: "assistant", Content: "登录已修复并通过测试。TASK_DONE_REPORT"},
		{Role: "user", Content: "NEW_FOLLOWUP_GOAL: 再加一个注销接口"},
	}
	prompt := server.BuildPromptBlocksWithHistory(&adapter.CanonicalRequest{Protocol: "openai_responses", Messages: msgs})[0].Text
	for _, gone := range []string{"STALE_OPEN_TASK", "STALE_NEXT_STEP"} {
		if strings.Contains(prompt, gone) {
			t.Fatalf("expected %s to be stripped after the prior task completed, got:\n%s", gone, prompt)
		}
	}
	for _, kept := range []string{"KEEP_CONSTRAINT", "KEEP_STATE", "KEEP_FILE internal/server/router.go", "TASK_DONE_REPORT", "NEW_FOLLOWUP_GOAL"} {
		if !strings.Contains(prompt, kept) {
			t.Fatalf("expected %s to survive Open tasks stripping, got:\n%s", kept, prompt)
		}
	}
}

func TestPostCompactionResumePreservesFullHandoff(t *testing.T) {
	workingSet := strings.Repeat("- internal/server/handler_proxy.go already read\n", 200) // ~10KB, beyond the default 4000-byte cap
	handoff := "# Handoff Summary\n## Goal\nRefactor compaction\n\nOpen tasks:\n- ACTIVE_OPEN_TASK keep implementing\n\n## Working set\n" + workingSet + "WORKING_SET_TAIL_MARKER"
	msgs := []adapter.CanonicalMessage{
		{Role: "user", Content: "ORIGINAL_GOAL: 优化压缩策略"},
		{Role: "user", Content: handoff},
		{Role: "assistant", Content: "Your context was compacted. The previous message contains a summary of the conversation so far."},
		{Role: "user", Content: "ORIGINAL_GOAL: 优化压缩策略"},
	}
	req := &adapter.CanonicalRequest{
		Protocol:     "openai_responses",
		Messages:     msgs,
		PromptBlocks: []acp.PromptBlock{{Type: "text", Text: "ORIGINAL_GOAL: 优化压缩策略"}},
	}
	if !server.IsPostCompactionResumeRequest(req) {
		t.Fatalf("expected the synthetic compaction ack not to hide the post-compaction resume turn")
	}
	prompt := server.BuildPromptBlocksWithHistory(req)[0].Text
	for _, kept := range []string{"ACTIVE_OPEN_TASK keep implementing", "WORKING_SET_TAIL_MARKER"} {
		if !strings.Contains(prompt, kept) {
			t.Fatalf("expected %s from the active Handoff Summary to be preserved on resume, got:\n%s", kept, prompt)
		}
	}
	if strings.Contains(prompt, "Your context was compacted") {
		t.Fatalf("expected the synthetic compaction ack to be dropped from rebuilt history")
	}
	if !strings.Contains(prompt, "do NOT redundantly re-run find_files / git_status") {
		t.Fatalf("expected anti-rerun continuity note after </conversation_history>")
	}

	resumed := &adapter.CanonicalRequest{
		Protocol: "openai_responses",
		Messages: append(append([]adapter.CanonicalMessage{}, msgs...), adapter.CanonicalMessage{Role: "assistant", Content: "已继续完成第一步。"}),
	}
	if server.IsPostCompactionResumeRequest(resumed) {
		t.Fatalf("expected a substantive assistant reply after the handoff to end the resume window")
	}
}

func TestRebuildKeepsActiveTaskToolOutputs(t *testing.T) {
	msgs := []adapter.CanonicalMessage{{Role: "user", Content: "ACTIVE_GOAL: 修复 handler_proxy 压缩逻辑"}}
	for i := 0; i < 6; i++ {
		body := strings.Repeat("a", 2500) + fmt.Sprintf("MID_FILE_MARKER_%d", i) + strings.Repeat("b", 2500)
		msgs = append(msgs,
			adapter.CanonicalMessage{Role: "assistant", Content: fmt.Sprintf("read_file({\"path\":\"f%d.go\"})", i)},
			adapter.CanonicalMessage{Role: "tool", Content: body},
		)
	}
	msgs = append(msgs,
		adapter.CanonicalMessage{Role: "assistant", Content: "tool_search({\"query\":\"apply_patch\"})"},
		adapter.CanonicalMessage{Role: "tool", Content: "Loaded tools: apply_patch"},
	)
	req := &adapter.CanonicalRequest{
		Protocol:    "openai_responses",
		Messages:    msgs,
		ToolResults: []adapter.CanonicalToolResult{{CallID: "call_ts", Content: "Loaded tools: apply_patch"}},
	}
	prompt := server.BuildPromptBlocksWithHistory(req)[0].Text
	for i := 0; i < 6; i++ {
		if !strings.Contains(prompt, fmt.Sprintf("MID_FILE_MARKER_%d", i)) {
			t.Fatalf("expected tool output %d of the active task to survive a mid-task rebuild without 1400-byte truncation", i)
		}
	}
}

func sailryCompactionSystemPrompt() string {
	return "## Task\nYou are writing a handoff for yourself. The conversation below reached its context budget, and the next model request will see only this handoff in place of the history. Produce the handoff only: do not continue the task, answer questions, or call tools.\n\n" +
		"**Conversation History:**\n" +
		"[user]: [user]: 参考这个方案，在acp启动时，同步客户端 上下文大小配置\n" +
		"[assistant]: tool_request(plugin_x_read_file): {\"path\":\"/Users/dev/proj/internal/acp/pool.go\",\"limit\":800}\n" +
		"[user]: tool_response: {\"kind\":\"file\",\"data\":\"package acp POOL_FILE_BODY\"}\n" +
		"[assistant]: tool_request(plugin_x_read_file): {\"path\":\"/Users/dev/.gemini/antigravity-acp/brain/abc/transcript.jsonl\"}\n" +
		"[user]: tool_response: Command Working Directory: /Users\n" +
		"[assistant]: tool_request(plugin_x_edit_file): {\"path\":\"internal/config/config.go\",\"old\":\"a\",\"new\":\"b\"}\n" +
		"[user]: tool_response: {\"ok\":true}\n" +
		"[assistant]: 已在 config.go 中新增 context_size 字段，下一步修改 pool.go。LATEST_PROGRESS_MARKER\n"
}

func TestLocalClientCompactionSummary(t *testing.T) {
	req := &adapter.CanonicalRequest{
		Protocol:     "openai_responses",
		SystemPrompt: sailryCompactionSystemPrompt(),
		PromptBlocks: []acp.PromptBlock{{Type: "text", Text: "Please summarize the conversation history provided in the system prompt."}},
	}
	if !server.IsClientCompactionRequest(req) {
		t.Fatalf("expected Sailry handoff request to be detected as client compaction")
	}
	summary := server.BuildLocalClientCompactionSummary(req)
	for _, want := range []string{
		"# Handoff Summary",
		"参考这个方案，在acp启动时，同步客户端 上下文大小配置",
		"/Users/dev/proj/internal/acp/pool.go",
		"internal/config/config.go",
		"LATEST_PROGRESS_MARKER",
		"Open tasks:",
		"## Working set",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected local handoff to contain %q, got:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "[user]:") {
		t.Fatalf("expected nested [user]: prefixes to be stripped, got:\n%s", summary)
	}
	for _, host := range []string{"transcript.jsonl", "/.gemini/", "Command Working Directory"} {
		if strings.Contains(summary, host) {
			t.Fatalf("expected host environment detail %q to be excluded from the local handoff, got:\n%s", host, summary)
		}
	}
	if strings.Contains(summary, "Please summarize the conversation history") {
		t.Fatalf("expected the compaction instruction itself not to be listed as a user goal")
	}
	modIdx := strings.Index(summary, "Files modified:")
	readIdx := strings.Index(summary, "Files already read:")
	if modIdx < 0 || readIdx < 0 || !strings.Contains(summary[modIdx:readIdx], "internal/config/config.go") {
		t.Fatalf("expected edited file to be listed under Files modified, got:\n%s", summary)
	}
}

func TestLocalClientCompactionHTTPResponse(t *testing.T) {
	t.Setenv("AG_CLIENT_COMPACTION_MODE", "")
	dataDir := t.TempDir()
	store, err := storage.NewStore(filepath.Join(dataDir, "gateway.db"))
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()
	cfg := &config.Config{
		Server:       config.ServerConfig{Host: "127.0.0.1", DataDir: dataDir, AdminToken: "admin-token-0123456789", AdminPassword: "admin-password-01"},
		PromptPolicy: config.PromptPolicyConfig{Mode: "pure_passthrough"},
	}
	// No ACP pool or session manager: the local handoff must be answered without touching a worker.
	gw := server.NewGatewayServer(cfg, store, nil, nil, nil, nil)
	httpSrv := httptest.NewServer(gw.Handler())
	defer httpSrv.Close()

	post := func(path string, payload map[string]any) (int, string, time.Duration) {
		raw, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, httpSrv.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer admin-token-0123456789")
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s failed: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), time.Since(start)
	}

	status, body, elapsed := post("/v1/responses", map[string]any{
		"model":        "gemini-4-carbon",
		"instructions": sailryCompactionSystemPrompt(),
		"input":        []map[string]any{{"role": "user", "content": "Please summarize the conversation history provided in the system prompt."}},
	})
	if status != http.StatusOK || !strings.Contains(body, "# Handoff Summary") || !strings.Contains(body, "LATEST_PROGRESS_MARKER") {
		t.Fatalf("expected local handoff via /v1/responses, got status=%d body=%s", status, body)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected local compaction to return without a model round-trip, took %v", elapsed)
	}

	status, body, _ = post("/v1/chat/completions", map[string]any{
		"model":  "gemini-4-carbon",
		"stream": true,
		"messages": []map[string]any{
			{"role": "system", "content": sailryCompactionSystemPrompt()},
			{"role": "user", "content": "Please summarize the conversation history provided in the system prompt."},
		},
	})
	if status != http.StatusOK || !strings.Contains(body, "Handoff Summary") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected streamed local handoff via /v1/chat/completions, got status=%d body=%s", status, body)
	}
}

func TestSyncTranscriptTokensSubToolDeduplicationAndStepAlignment(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "gateway.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	acpSessID := "2bbb09f3-test-sync"
	transcriptDir := filepath.Join(dataDir, "accounts", "default", ".gemini", "antigravity-acp", "brain", acpSessID, ".system_generated", "logs")
	if err := os.MkdirAll(transcriptDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	transcriptContent := strings.Join([]string{
		`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-10T09:46:20Z","input_tokens":20491,"cache_read_tokens":0,"output_tokens":606,"tool_calls":[{"name":"t1"},{"name":"t2"}]}`,
		`{"step_index":11,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-10T09:46:37Z","input_tokens":19252,"cache_read_tokens":20492,"output_tokens":502,"tool_calls":[{"name":"t3"}]}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(transcriptDir, "transcript.jsonl"), []byte(transcriptContent), 0644); err != nil {
		t.Fatalf("WriteFile transcript.jsonl failed: %v", err)
	}

	t1Done, _ := time.Parse(time.RFC3339Nano, "2026-11-10T09:46:28.980Z")
	t2Done, _ := time.Parse(time.RFC3339Nano, "2026-11-10T09:46:37.320Z")
	t3Done, _ := time.Parse(time.RFC3339Nano, "2026-11-10T09:46:52.176Z")

	// Row 1: Primary LLM call for step 1 (started 09:46:18, finished 09:46:28.980, latency 10894ms, ttfb 8718ms)
	_ = store.RecordAuditLog(storage.AuditLog{
		RequestID:        "resp_step1_main",
		Protocol:         "openai_responses",
		AccountID:        "default",
		SessionID:        acpSessID,
		ModelName:        "gemini-4-argon",
		PromptTokens:     5000,
		CompletionTokens: 16,
		LatencyMs:        10894,
		StatusCode:       200,
		ToolCallsJSON:    `{"call_id":"c1","tool_name":"t1","arguments":"{}"}`,
		ContextJSON:      `{"input_tokens":5000,"cached_tokens":0,"output_tokens":16,"ttfb_ms":8718,"items":[]}`,
		CreatedAt:        t1Done,
	})
	// Row 2: Sub-tool dispatch #2 for step 1 (finished at 09:46:37.320, latency 967ms, ttfb 3ms — same whole second that step 11 starts!)
	_ = store.RecordAuditLog(storage.AuditLog{
		RequestID:        "resp_step1_sub2",
		Protocol:         "openai_responses",
		AccountID:        "default",
		SessionID:        acpSessID,
		ModelName:        "gemini-4-argon",
		PromptTokens:     39744,
		CachedTokens:     20492,
		CompletionTokens: 22,
		LatencyMs:        967,
		StatusCode:       200,
		ToolCallsJSON:    `{"call_id":"c2","tool_name":"t2","arguments":"{}"}`,
		ContextJSON:      `{"input_tokens":39744,"cached_tokens":20492,"output_tokens":22,"ttfb_ms":3,"items":[]}`,
		CreatedAt:        t2Done,
	})
	// Row 3: Primary LLM call for step 11 (started 09:46:37.385, finished 09:46:52.176, latency 14791ms, ttfb 11900ms)
	_ = store.RecordAuditLog(storage.AuditLog{
		RequestID:        "resp_step11_main",
		Protocol:         "openai_responses",
		AccountID:        "default",
		SessionID:        acpSessID,
		ModelName:        "gemini-4-argon",
		PromptTokens:     5000,
		CompletionTokens: 11,
		LatencyMs:        14791,
		StatusCode:       200,
		ToolCallsJSON:    `{"call_id":"c3","tool_name":"t3","arguments":"{}"}`,
		ContextJSON:      `{"input_tokens":5000,"cached_tokens":0,"output_tokens":11,"ttfb_ms":11900,"items":[]}`,
		CreatedAt:        t3Done,
	})

	server.SyncTranscriptTokens(dataDir, store, 10)

	logs, err := store.ListAuditLogs(10)
	if err != nil {
		t.Fatalf("ListAuditLogs failed: %v", err)
	}
	byID := make(map[string]storage.AuditLog, len(logs))
	for _, l := range logs {
		byID[l.RequestID] = l
	}

	r1 := byID["resp_step1_main"]
	if r1.PromptTokens != 20491 || r1.CachedTokens != 0 {
		t.Fatalf("expected resp_step1_main to have prompt=20491 cached=0, got prompt=%d cached=%d", r1.PromptTokens, r1.CachedTokens)
	}

	r2 := byID["resp_step1_sub2"]
	if r2.PromptTokens != 0 || r2.CachedTokens != 0 || r2.TotalTokens != r2.CompletionTokens {
		t.Fatalf("expected sub-tool row resp_step1_sub2 to have prompt=0 cached=0 total=completion(%d), got prompt=%d cached=%d total=%d", r2.CompletionTokens, r2.PromptTokens, r2.CachedTokens, r2.TotalTokens)
	}
	var r2Snap map[string]any
	if err := json.Unmarshal([]byte(r2.ContextJSON), &r2Snap); err != nil {
		t.Fatalf("unmarshal r2 ContextJSON failed: %v", err)
	}
	if int(r2Snap["input_tokens"].(float64)) != 20491 {
		t.Fatalf("expected resp_step1_sub2 context_json input_tokens=20491 (matched to step 1, not step 11's 39744), got %v", r2Snap["input_tokens"])
	}
	if r2Snap["sub_tool_step"] != true {
		t.Fatalf("expected resp_step1_sub2 context_json sub_tool_step=true, got %v", r2Snap["sub_tool_step"])
	}

	r3 := byID["resp_step11_main"]
	if r3.PromptTokens != 39744 || r3.CachedTokens != 20492 {
		t.Fatalf("expected resp_step11_main to have prompt=39744 cached=20492, got prompt=%d cached=%d", r3.PromptTokens, r3.CachedTokens)
	}

	// Verify antigravity-cli subdirectory resolution (used by Linux/ARM agy CLI backend)
	cliSessID := "3ccc09f3-test-cli"
	cliTranscriptDir := filepath.Join(dataDir, "accounts", "default", ".gemini", "antigravity-cli", "brain", cliSessID, ".system_generated", "logs")
	if err := os.MkdirAll(cliTranscriptDir, 0755); err != nil {
		t.Fatalf("MkdirAll cliTranscriptDir failed: %v", err)
	}
	cliTranscriptContent := strings.Join([]string{
		`{"step_index":3,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-10T09:50:10Z","input_tokens":7481,"cache_read_tokens":32423,"output_tokens":214}`,
		`{"step_index":5,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-10T09:50:15Z","input_tokens":8100,"cache_read_tokens":39904,"output_tokens":180}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(cliTranscriptDir, "transcript.jsonl"), []byte(cliTranscriptContent), 0644); err != nil {
		t.Fatalf("WriteFile cli transcript.jsonl failed: %v", err)
	}
	tCliDone, _ := time.Parse(time.RFC3339Nano, "2026-11-10T09:50:15.500Z")
	tCliNextDone, _ := time.Parse(time.RFC3339Nano, "2026-11-10T09:50:25.200Z")
	_ = store.RecordAuditLog(storage.AuditLog{
		RequestID:        "resp_cli_step3",
		Protocol:         "openai_responses",
		AccountID:        "default",
		SessionID:        cliSessID,
		ModelName:        "gemini-4-argon",
		PromptTokens:     5000,
		CompletionTokens: 20,
		LatencyMs:        4200,
		StatusCode:       200,
		ContextJSON:      `{"input_tokens":5000,"cached_tokens":0,"output_tokens":20,"ttfb_ms":3100,"items":[]}`,
		CreatedAt:        tCliDone,
	})
	_ = store.RecordAuditLog(storage.AuditLog{
		RequestID:        "resp_cli_step5",
		Protocol:         "openai_responses",
		AccountID:        "default",
		SessionID:        cliSessID,
		ModelName:        "gemini-4-argon",
		PromptTokens:     0,
		CompletionTokens: 25,
		LatencyMs:        9400,
		StatusCode:       200,
		ContextJSON:      `{"input_tokens":39904,"cached_tokens":32423,"output_tokens":25,"ttfb_ms":8900,"sub_tool_step":true,"items":[]}`,
		CreatedAt:        tCliNextDone,
	})
	server.SyncTranscriptTokens(dataDir, store, 10)
	logsAfter, _ := store.ListAuditLogs(10)
	for _, l := range logsAfter {
		if l.RequestID == "resp_cli_step3" {
			if l.PromptTokens != 39904 || l.CachedTokens != 32423 {
				t.Fatalf("expected antigravity-cli step 3 to resolve prompt=39904 cached=32423, got prompt=%d cached=%d", l.PromptTokens, l.CachedTokens)
			}
		}
		if l.RequestID == "resp_cli_step5" {
			if l.PromptTokens != 48004 || l.CachedTokens != 39904 {
				t.Fatalf("expected antigravity-cli step 5 (started in same whole second step 3 ended) to resolve prompt=48004 cached=39904, got prompt=%d cached=%d", l.PromptTokens, l.CachedTokens)
			}
		}
	}
}

func TestDynamicAdminCredentialsRotationAndUserLock(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "gateway.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	makeCfg := func() *config.Config {
		return &config.Config{
			Server: config.ServerConfig{
				Host:          "127.0.0.1",
				Port:          0,
				DataDir:       dataDir,
				AdminToken:    "auto",
				AdminPassword: "auto",
			},
			PromptPolicy: config.PromptPolicyConfig{
				Mode: "pure_passthrough",
			},
			SandboxPolicy: config.SandboxPolicyConfig{
				SessionMode:      "yolo",
				InternalApproval: "auto_approve",
			},
		}
	}

	// 1. First deployment: dynamically generates agp-... and agt-...
	cfg1 := makeCfg()
	gw1 := server.NewGatewayServer(cfg1, store, nil, nil, nil, nil)
	pwd1 := cfg1.GetAdminPassword()
	tok1 := cfg1.GetAdminToken()
	if !strings.HasPrefix(pwd1, "agp-") || !strings.HasPrefix(tok1, "agt-") {
		t.Fatalf("expected dynamic agp-/agt- credentials on deploy #1, got pwd=%q tok=%q", pwd1, tok1)
	}
	credRaw1, err := os.ReadFile(gw1.AdminCredentialsFilePath())
	if err != nil || !strings.Contains(string(credRaw1), "STATUS=DYNAMIC") || !strings.Contains(string(credRaw1), pwd1) {
		t.Fatalf("expected admin_credentials.txt with STATUS=DYNAMIC and pwd1, got err=%v content=%s", err, string(credRaw1))
	}

	// 2. Second deployment (before user modification): rotates to new random credentials
	cfg2 := makeCfg()
	gw2 := server.NewGatewayServer(cfg2, store, nil, nil, nil, nil)
	pwd2 := cfg2.GetAdminPassword()
	tok2 := cfg2.GetAdminToken()
	if pwd2 == pwd1 || tok2 == tok1 {
		t.Fatalf("expected rotated credentials on deploy #2 before user lock, got pwd1=%q pwd2=%q tok1=%q tok2=%q", pwd1, pwd2, tok1, tok2)
	}

	ts2 := httptest.NewServer(gw2.Handler())
	defer ts2.Close()

	// Old password from deploy #1 must be rejected on deploy #2
	badLoginReq, _ := http.NewRequest(http.MethodPost, ts2.URL+"/api/admin/auth/login", strings.NewReader(`{"password":"`+pwd1+`"}`))
	badLoginResp, err := http.DefaultClient.Do(badLoginReq)
	if err != nil {
		t.Fatalf("bad login request failed: %v", err)
	}
	_ = badLoginResp.Body.Close()
	if badLoginResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for rotated-out password pwd1, got %d", badLoginResp.StatusCode)
	}

	// Active dynamic password pwd2 succeeds
	goodLoginReq, _ := http.NewRequest(http.MethodPost, ts2.URL+"/api/admin/auth/login", strings.NewReader(`{"password":"`+pwd2+`"}`))
	goodLoginResp, err := http.DefaultClient.Do(goodLoginReq)
	if err != nil {
		t.Fatalf("good login request failed: %v", err)
	}
	defer goodLoginResp.Body.Close()
	if goodLoginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for active dynamic password pwd2, got %d", goodLoginResp.StatusCode)
	}

	// 3. User actively modifies and locks password + custom token via /api/admin/auth/password
	changeBody := `{"current_password":"` + pwd2 + `","new_password":"my-fixed-password-99","new_admin_token":"my-fixed-token-88"}`
	changeReq, _ := http.NewRequest(http.MethodPost, ts2.URL+"/api/admin/auth/password", strings.NewReader(changeBody))
	changeReq.Header.Set("Authorization", "Bearer "+tok2)
	changeResp, err := http.DefaultClient.Do(changeReq)
	if err != nil {
		t.Fatalf("change password request failed: %v", err)
	}
	defer changeResp.Body.Close()
	if changeResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on password change, got %d", changeResp.StatusCode)
	}

	credRawLocked, err := os.ReadFile(gw2.AdminCredentialsFilePath())
	if err != nil || !strings.Contains(string(credRawLocked), "STATUS=LOCKED_BY_USER") {
		t.Fatalf("expected admin_credentials.txt with STATUS=LOCKED_BY_USER, got err=%v content=%s", err, string(credRawLocked))
	}

	// 4. Third deployment (after user modification): credentials stay permanently fixed!
	cfg3 := makeCfg()
	gw3 := server.NewGatewayServer(cfg3, store, nil, nil, nil, nil)
	ts3 := httptest.NewServer(gw3.Handler())
	defer ts3.Close()

	// Fixed password works on deploy #3
	fixedLoginReq, _ := http.NewRequest(http.MethodPost, ts3.URL+"/api/admin/auth/login", strings.NewReader(`{"password":"my-fixed-password-99"}`))
	fixedLoginResp, err := http.DefaultClient.Do(fixedLoginReq)
	if err != nil {
		t.Fatalf("fixed login request failed: %v", err)
	}
	_ = fixedLoginResp.Body.Close()
	if fixedLoginResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for locked user password on deploy #3, got %d", fixedLoginResp.StatusCode)
	}

	// Fixed token works on deploy #3
	fixedTokReq, _ := http.NewRequest(http.MethodGet, ts3.URL+"/api/admin/auth/session", nil)
	fixedTokReq.Header.Set("Authorization", "Bearer my-fixed-token-88")
	fixedTokResp, err := http.DefaultClient.Do(fixedTokReq)
	if err != nil {
		t.Fatalf("fixed token request failed: %v", err)
	}
	_ = fixedTokResp.Body.Close()
	if fixedTokResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for locked user admin_token on deploy #3, got %d", fixedTokResp.StatusCode)
	}
}





