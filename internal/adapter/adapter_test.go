package adapter_test

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/adapter"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
)

func TestParseOpenAIAndClaudeRequestsAndSystemPromptPolicy(t *testing.T) {
	// 1. OpenAI request with system prompt, image_url Data URI, tool definitions, and tool role result
	openaiRaw := `{
		"model": "gemini-2.5-pro",
		"stream": true,
		"reasoning_effort": "extra",
		"messages": [
			{"role": "system", "content": "You are a helpful coding assistant."},
			{"role": "user", "content": [
				{"type": "text", "text": "Inspect this diagram and run check"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
			]},
			{"role": "assistant", "tool_calls": [
				{"id": "call_123", "type": "function", "function": {"name": "run_cmd", "arguments": "{\"cmd\":\"ls\"}"}}
			]},
			{"role": "tool", "tool_call_id": "call_123", "content": "main.go\ngo.mod"}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "run_cmd",
					"description": "Execute command locally",
					"parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}
				}
			}
		]
	}`

	oaReq, err := adapter.ParseOpenAIRequest([]byte(openaiRaw), "sess_oa_1")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest failed: %v", err)
	}
	if oaReq.Model != "gemini-2.5-pro" || !oaReq.Stream || oaReq.ThinkingLevel != "high" {
		t.Fatalf("unexpected parsed OpenAI request header fields: %+v", oaReq)
	}
	if oaReq.SystemPrompt != "You are a helpful coding assistant." {
		t.Fatalf("unexpected OpenAI system prompt: %q", oaReq.SystemPrompt)
	}
	if len(oaReq.Tools) != 1 || oaReq.Tools[0].Name != "run_cmd" {
		t.Fatalf("unexpected OpenAI tools: %+v", oaReq.Tools)
	}
	if len(oaReq.ToolResults) != 1 || oaReq.ToolResults[0].CallID != "call_123" {
		t.Fatalf("unexpected OpenAI tool results: %+v", oaReq.ToolResults)
	}

	// 2. Claude request with polymorphic array system prompt, thinking budget, image + document + tool_result (Review Focus #5)
	claudeRaw := `{
		"model": "claude-sonnet-4-5",
		"stream": true,
		"system": [
			{"type": "text", "text": "System rule 1."},
			{"type": "text", "text": "System rule 2."}
		],
		"thinking": {"type": "enabled", "budget_tokens": 32000},
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Check this file"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "/9j/4AAQ"}},
					{"type": "document", "title": "spec.md", "source": {"type": "text", "media_type": "text/markdown", "data": "# Spec"}},
					{"type": "tool_result", "tool_use_id": "call_claude_99", "content": [{"type": "text", "text": "Tool output ok"}], "is_error": false}
				]
			}
		],
		"tools": [
			{
				"name": "bash",
				"description": "Run bash",
				"input_schema": {"type": "object"}
			}
		]
	}`

	clReq, err := adapter.ParseClaudeRequest([]byte(claudeRaw), "sess_cl_1")
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}
	if clReq.ThinkingLevel != "high" {
		t.Fatalf("expected thinking level 'high' for 32000 budget, got %q", clReq.ThinkingLevel)
	}
	if !strings.Contains(clReq.SystemPrompt, "System rule 1.") || !strings.Contains(clReq.SystemPrompt, "System rule 2.") {
		t.Fatalf("unexpected Claude system prompt: %q", clReq.SystemPrompt)
	}
	// Sibling text/image/document blocks in a tool_result message travel with the last tool result.
	if len(clReq.ToolResults) != 1 || clReq.ToolResults[0].CallID != "call_claude_99" ||
		!strings.HasPrefix(clReq.ToolResults[0].Content, "Tool output ok\n\nCheck this file") ||
		!strings.Contains(clReq.ToolResults[0].Content, "[image omitted: image/jpeg]") ||
		!strings.Contains(clReq.ToolResults[0].Content, "# Spec") {
		t.Fatalf("unexpected Claude tool results: %+v", clReq.ToolResults)
	}
	if len(clReq.PromptBlocks) != 1 || !strings.HasPrefix(clReq.PromptBlocks[0].Text, "[tool_result:call_claude_99]\nTool output ok") {
		t.Fatalf("expected the trailing tool result rendered as the prompt, got %+v", clReq.PromptBlocks)
	}

	// 3. OpenAI Official Responses API request (/v1/responses) with instructions, previous_response_id, input items, and function_call_output
	responsesRaw := `{
		"model": "gemini-4-argon-low",
		"stream": true,
		"instructions": "Follow strict repository style.",
		"previous_response_id": "resp_prev_001",
		"reasoning": {"effort": "xhigh"},
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "Run status check"}]},
			{"type": "function_call_output", "call_id": "call_resp_77", "output": "clean working tree"}
		],
		"tools": [
			{
				"type": "function",
				"name": "git_status",
				"description": "Check git status",
				"parameters": {"type": "object"}
			}
		]
	}`

	respReq, err := adapter.ParseOpenAIResponsesRequest([]byte(responsesRaw), "")
	if err != nil {
		t.Fatalf("ParseOpenAIResponsesRequest failed: %v", err)
	}
	if respReq.Protocol != "openai_responses" || respReq.Model != "gemini-4-argon" || respReq.SessionHeader != "resp_prev_001" || respReq.ThinkingLevel != "high" {
		t.Fatalf("unexpected parsed OpenAI Responses header fields: %+v", respReq)
	}
	if respReq.SystemPrompt != "Follow strict repository style." {
		t.Fatalf("unexpected OpenAI Responses instructions: %q", respReq.SystemPrompt)
	}
	if len(respReq.Tools) != 1 || respReq.Tools[0].Name != "git_status" {
		t.Fatalf("unexpected OpenAI Responses tools: %+v", respReq.Tools)
	}
	if len(respReq.ToolResults) != 1 || respReq.ToolResults[0].CallID != "call_resp_77" || respReq.ToolResults[0].Content != "clean working tree" {
		t.Fatalf("unexpected OpenAI Responses tool results: %+v", respReq.ToolResults)
	}

	// 4. Test 3-Protocol Thinking Effort & Model Suffix Compatibility + Clean Base Model Extraction
	oaNestedReasoning, err := adapter.ParseOpenAIRequest([]byte(`{"model":"gemini-4-argon-medium","messages":[{"role":"user","content":"hi"}]}`), "")
	if err != nil || oaNestedReasoning.Model != "gemini-4-argon" || oaNestedReasoning.ThinkingLevel != "medium" {
		t.Fatalf("expected gemini-4-argon + medium from suffix fallback, got %+v (err=%v)", oaNestedReasoning, err)
	}
	clEffortReq, err := adapter.ParseClaudeRequest([]byte(`{"model":"claude-opus-5-5","output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`), "")
	if err != nil || clEffortReq.Model != "claude-opus-5-5" || clEffortReq.ThinkingLevel != "low" {
		t.Fatalf("expected claude-opus-5-5 + low from output_config.effort, got %+v (err=%v)", clEffortReq, err)
	}

	sessionRes := &acp.NewSessionResult{
		SessionID: "s_1",
		ConfigOptions: []acp.ConfigOption{
			{
				ID:           "model",
				Name:         "Model",
				Category:     "model",
				CurrentValue: "gemini-3.8-flash",
				Options: []acp.ConfigOptionValue{
					{Value: "gemini-4-argon", Name: "Gemini 4 Argon"},
					{Value: "claude-opus-5-5", Name: "Claude Opus 5.5"},
					{Value: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)"},
				},
			},
			{
				ID:           "thinking_level",
				Name:         "Thinking Level",
				Category:     "thought_level",
				CurrentValue: "high",
				Options: []acp.ConfigOptionValue{
					{Value: "low", Name: "Low"},
					{Value: "medium", Name: "Medium"},
					{Value: "high", Name: "High"},
				},
			},
		},
		Models: &acp.SessionModelsState{
			CurrentModelID: "gemini-3.8-flash-high",
			AvailableModels: []acp.ModelInfo{
				{ModelID: "gemini-4-argon-high", Name: "Gemini 4 Argon (High)"},
				{ModelID: "gemini-4-argon-medium", Name: "Gemini 4 Argon (Medium)"},
				{ModelID: "gemini-4-argon-low", Name: "Gemini 4 Argon (Low)"},
				{ModelID: "claude-opus-5-5-high", Name: "Claude Opus 5.5 (High)"},
				{ModelID: "claude-opus-5-5-medium", Name: "Claude Opus 5.5 (Medium)"},
				{ModelID: "claude-opus-5-5-low", Name: "Claude Opus 5.5 (Low)"},
				{ModelID: "gpt-oss-120b-medium", Name: "GPT-OSS 120B (Medium)"},
			},
		},
	}
	cleanModels := acp.ExtractModelsFromSessionResult(sessionRes)
	if len(cleanModels) != 3 {
		t.Fatalf("expected exactly 3 clean base models without -low/-medium/-high duplicates, got %d: %+v", len(cleanModels), cleanModels)
	}
	if cleanModels[0].ModelID != "gemini-4-argon" || len(cleanModels[0].ThinkingLevels) != 3 || cleanModels[0].DefaultThinkingLevel != "high" {
		t.Fatalf("unexpected first clean model: %+v", cleanModels[0])
	}
	if cleanModels[0].ThinkingLevels[0] != "low" || cleanModels[0].ThinkingLevels[1] != "medium" || cleanModels[0].ThinkingLevels[2] != "high" {
		t.Fatalf("expected canonical ascending thinking levels [low medium high] for gemini-4-argon, got %+v", cleanModels[0].ThinkingLevels)
	}

	// 5. Test BuildEffectiveSystemPrompt & BuildACPSystemPrompt
	eff := adapter.BuildEffectiveSystemPrompt("Client Sys", config.PromptPolicyConfig{
		Mode:                "prepend_global",
		GlobalPrependPrompt: "Global Prefix",
	}, []storage.PresetSkill{
		{Name: "skill1", Category: "instruction", ContentJSON: `{"content":"Skill Rule"}`, Enabled: true},
	})
	if !strings.Contains(eff, "Global Prefix") || !strings.Contains(eff, "Skill Rule") || !strings.Contains(eff, "Client Sys") {
		t.Fatalf("unexpected effective system prompt: %q", eff)
	}
	acpPassthrough := adapter.BuildACPSystemPrompt("Client Sys", config.PromptPolicyConfig{Mode: "pure_passthrough"}, "Extra Rule")
	if !strings.Contains(acpPassthrough, adapter.NativeSystemPromptOverride) || !strings.Contains(acpPassthrough, "Client Sys") {
		t.Fatalf("expected pure_passthrough to include NativeSystemPromptOverride, got %q", acpPassthrough)
	}
	acpHybrid := adapter.BuildACPSystemPrompt("Client Sys", config.PromptPolicyConfig{Mode: "native_hybrid"}, "Extra Rule")
	if strings.Contains(acpHybrid, adapter.NativeSystemPromptOverride) || !strings.Contains(acpHybrid, "Client Sys") {
		t.Fatalf("expected native_hybrid to omit NativeSystemPromptOverride, got %q", acpHybrid)
	}

	// 6. Test MCPServerEndpoint headers array serialization & EnsureObjectSchema union type sanitization
	mcpEP := acp.MCPServerEndpoint{
		Name: "client_tools",
		Type: "http",
		URL:  "http://127.0.0.1:8080/mcp/sessions/br_test",
	}
	epRaw, err := json.Marshal(mcpEP)
	if err != nil || !strings.Contains(string(epRaw), `"headers":[]`) {
		t.Fatalf("expected MCPServerEndpoint to serialize \"headers\":[], got %s (err=%v)", string(epRaw), err)
	}

	unionSchema := adapter.EnsureObjectSchema(json.RawMessage(`{"type":"object","properties":{"expected_revision":{"type":["string","null"]}}}`))
	props, _ := unionSchema["properties"].(map[string]any)
	expRev, _ := props["expected_revision"].(map[string]any)
	if expRev["type"] != "string" || expRev["nullable"] != true {
		t.Fatalf("expected union type [\"string\",\"null\"] to normalize to type=string, nullable=true, got %+v", expRev)
	}

	// Gemini rejects empty enum values ("enum[2]: cannot be empty"); they must be stripped,
	// null must become nullable, and an enum left empty must be removed. Nested schemas too.
	enumSchema := adapter.EnsureObjectSchema(json.RawMessage(`{"type":"object","properties":{
		"role":{"type":"string","enum":["explorer","reviewer",""]},
		"mode":{"type":"string","enum":["plan",null]},
		"blank":{"type":"string","enum":["", " "]},
		"agents":{"type":"array","items":{"type":"object","properties":{"role":{"type":"string","enum":["","worker"]}}}}
	}}`))
	eprops, _ := enumSchema["properties"].(map[string]any)
	if got := eprops["role"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{"explorer", "reviewer"}) {
		t.Fatalf("expected empty enum value stripped, got %#v", got)
	}
	if mode := eprops["mode"].(map[string]any); !reflect.DeepEqual(mode["enum"], []any{"plan"}) || mode["nullable"] != true {
		t.Fatalf("expected null enum value to become nullable, got %+v", mode)
	}
	if _, ok := eprops["blank"].(map[string]any)["enum"]; ok {
		t.Fatalf("expected an all-empty enum to be removed, got %+v", eprops["blank"])
	}
	nested := eprops["agents"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["role"].(map[string]any)
	if !reflect.DeepEqual(nested["enum"], []any{"worker"}) {
		t.Fatalf("expected nested empty enum value stripped, got %#v", nested["enum"])
	}

	// 7. Test SessionUpdateParams ContentBlock unmarshaling for both object and tool_call_update array shapes
	var toolUpdate acp.SessionUpdateParams
	if err := json.Unmarshal([]byte(`{"sessionId":"s_1","update":{"sessionUpdate":"tool_call_update","content":[{"type":"content","content":{"type":"text","text":"Load Word skill"}}]}}`), &toolUpdate); err != nil {
		t.Fatalf("expected tool_call_update array content to unmarshal cleanly, got err=%v", err)
	}
	if toolUpdate.Update.Content.Text != "Load Word skill" {
		t.Fatalf("expected extracted tool_call_update content text 'Load Word skill', got %q", toolUpdate.Update.Content.Text)
	}
}

func TestToolResultsPreservedAfterTrailingUserContextAndClearedAcrossTurns(t *testing.T) {
	// Reproduce P0-1 & P1-2 from production audit_logs:
	// 1) Multi-turn tool chain where Turn 1 had function_call (call_old) + function_call_output (old_output),
	//    and Turn 2 has function_call (call_cur) + function_call_output (cur_output) followed immediately
	//    by a trailing "role": "user" <turn-context> block (injected by Sailry after read_skill_resource / load_skill).
	raw := `{
		"model": "gemini-4-argon",
		"stream": true,
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "Review all code"}]},
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "step 1"}]},
			{"type": "function_call", "call_id": "call_old_1", "name": "review_scope", "arguments": "{\"limit\":100}"},
			{"type": "function_call_output", "call_id": "call_old_1", "output": "old_scope_output_json"},
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "step 2"}]},
			{"type": "function_call", "call_id": "call_cur_2", "name": "read_skill_resource", "arguments": "{\"path\":\"rules/index.md\"}"},
			{"type": "function_call_output", "call_id": "call_cur_2", "output": "{\"content\":\"# Review rule index\"}"},
			{"role": "user", "content": [{"type": "input_text", "text": "<turn-context>\nRetained owner context: skill:code-review:rules/index.md\n</turn-context>"}]}
		]
	}`

	req, err := adapter.ParseOpenAIResponsesRequest([]byte(raw), "")
	if err != nil {
		t.Fatalf("ParseOpenAIResponsesRequest failed: %v", err)
	}
	if len(req.ToolResults) != 1 {
		t.Fatalf("expected exactly 1 current-turn ToolResult (call_old_1 cleared, call_cur_2 preserved despite trailing <turn-context>), got %d: %+v", len(req.ToolResults), req.ToolResults)
	}
	if req.ToolResults[0].CallID != "call_cur_2" {
		t.Fatalf("expected CallID 'call_cur_2', got %q", req.ToolResults[0].CallID)
	}
	if !strings.Contains(req.ToolResults[0].Content, "# Review rule index") || !strings.Contains(req.ToolResults[0].Content, "Retained owner context") {
		t.Fatalf("expected ToolResult content to include both tool output and trailing turn-context, got %q", req.ToolResults[0].Content)
	}

	// Also verify OpenAI Chat Completions with trailing user context after tool message
	oaRaw := `{
		"model": "gemini-4-argon",
		"messages": [
			{"role": "user", "content": "Review"},
			{"role": "assistant", "tool_calls": [{"id": "call_oa_old", "type": "function", "function": {"name": "f1", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_oa_old", "content": "old_res"},
			{"role": "assistant", "tool_calls": [{"id": "call_oa_cur", "type": "function", "function": {"name": "f2", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_oa_cur", "content": "cur_res"},
			{"role": "user", "content": "<turn-context>extra context</turn-context>"}
		]
	}`
	oaReq, err := adapter.ParseOpenAIRequest([]byte(oaRaw), "")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest failed: %v", err)
	}
	if len(oaReq.ToolResults) != 1 || oaReq.ToolResults[0].CallID != "call_oa_cur" || !strings.Contains(oaReq.ToolResults[0].Content, "extra context") {
		t.Fatalf("unexpected OpenAI ToolResults with trailing user context: %+v", oaReq.ToolResults)
	}

	// Also verify Claude Messages with trailing user context message after tool_result user message
	clRaw := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "Review"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "call_cl_cur", "name": "f2", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_cl_cur", "content": "cl_res"}]},
			{"role": "user", "content": "<turn-context>claude extra context</turn-context>"}
		]
	}`
	clReq, err := adapter.ParseClaudeRequest([]byte(clRaw), "")
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}
	if len(clReq.ToolResults) != 1 || clReq.ToolResults[0].CallID != "call_cl_cur" || !strings.Contains(clReq.ToolResults[0].Content, "claude extra context") {
		t.Fatalf("unexpected Claude ToolResults with trailing user context: %+v", clReq.ToolResults)
	}
}

func TestMultiToolCallsSSEAndJSONResponses(t *testing.T) {
	calls := []adapter.CanonicalToolCall{
		{CallID: "call_p1", ToolName: "read_review_change", ArgsJSON: `{"offset":0}`},
		{CallID: "call_p2", ToolName: "read_review_change", ArgsJSON: `{"offset":400}`},
	}

	// 1. OpenAI Responses SSE & JSON with multiple parallel tool calls + cached_tokens
	recResp := httptest.NewRecorder()
	respWriter := adapter.NewOpenAIResponsesSSEWriter(recResp, "resp_multi_1", "gemini-4-argon")
	respWriter.WriteStart()
	respWriter.WriteToolCallsAndFinish(calls, "batch reading", "", 1000, 800, 50)
	respSSE := recResp.Body.String()
	if !strings.Contains(respSSE, `"call_id":"call_p1"`) || !strings.Contains(respSSE, `"call_id":"call_p2"`) || !strings.Contains(respSSE, `"cached_tokens":800`) {
		t.Fatalf("expected both parallel tool calls and cached_tokens=800 in Responses SSE:\n%s", respSSE)
	}

	// 2. OpenAI Chat Completions SSE with multiple parallel tool calls
	recOA := httptest.NewRecorder()
	oaWriter := adapter.NewOpenAISSEWriter(recOA, "chatcmpl_multi_1", "gemini-4-argon")
	oaWriter.WriteStart()
	oaWriter.WriteToolCallsAndFinish(calls)
	oaSSE := recOA.Body.String()
	if !strings.Contains(oaSSE, `"id":"call_p1"`) || !strings.Contains(oaSSE, `"id":"call_p2"`) {
		t.Fatalf("expected both parallel tool calls in OpenAI SSE:\n%s", oaSSE)
	}

	// 3. Claude Messages SSE with multiple parallel tool uses
	recCL := httptest.NewRecorder()
	clWriter := adapter.NewClaudeSSEWriter(recCL, "msg_multi_1", "claude-sonnet-4-5")
	clWriter.WriteStart()
	clWriter.WriteToolUsesAndStop(calls)
	clSSE := recCL.Body.String()
	if !strings.Contains(clSSE, `"id":"call_p1"`) || !strings.Contains(clSSE, `"id":"call_p2"`) {
		t.Fatalf("expected both parallel tool uses in Claude SSE:\n%s", clSSE)
	}
}

func TestOpenAIAndClaudeSSEWritersAntiBufferingAndKeepAlive(t *testing.T) {
	// OpenAI SSE Writer
	recOA := httptest.NewRecorder()
	oaWriter := adapter.NewOpenAISSEWriter(recOA, "chatcmpl_1", "gemini-2.5-pro")
	oaWriter.WriteStart()
	oaWriter.WriteKeepAlive()
	oaWriter.WriteThinkingDelta("Thinking step...")
	oaWriter.WriteTextDelta("Final answer")
	oaWriter.WriteToolCallAndFinish("call_abc", "read_file", `{"path":"a.go"}`)

	if recOA.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected X-Accel-Buffering: no on OpenAI SSE response")
	}
	oaBody := recOA.Body.String()
	for _, want := range []string{": ping", `"reasoning_content":"Thinking step..."`, `"content":"Final answer"`, `"tool_calls"`, `[DONE]`} {
		if !strings.Contains(oaBody, want) {
			t.Fatalf("expected %q in OpenAI SSE stream:\n%s", want, oaBody)
		}
	}
	if strings.Contains(oaBody, `"reasoning":`) {
		t.Fatalf("expected only reasoning_content (no duplicate reasoning key) in OpenAI SSE stream:\n%s", oaBody)
	}

	// OpenAI Official Responses SSE Writer
	recResp := httptest.NewRecorder()
	respWriter := adapter.NewOpenAIResponsesSSEWriter(recResp, "resp_101", "gemini-2.5-pro")
	respWriter.WriteStart()
	respWriter.WriteKeepAlive()
	respWriter.WriteThinkingDelta("Reasoning summary...")
	respWriter.WriteTextDelta("Responses output text")
	respWriter.WriteStop("Reasoning summary...", "Responses output text", 12, 8)

	if recResp.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected X-Accel-Buffering: no on OpenAI Responses SSE response")
	}
	respBody := recResp.Body.String()
	for _, want := range []string{"event: response.created", ": ping", "event: response.reasoning_summary_text.delta", "event: response.output_text.delta", "event: response.completed"} {
		if !strings.Contains(respBody, want) {
			t.Fatalf("expected %q in OpenAI Responses SSE stream:\n%s", want, respBody)
		}
	}

	// Claude SSE Writer
	recCL := httptest.NewRecorder()
	clWriter := adapter.NewClaudeSSEWriter(recCL, "msg_1", "claude-sonnet-4-5")
	clWriter.WriteStart()
	clWriter.WriteKeepAlive()
	clWriter.WriteThinkingDelta("Deep thought...")
	clWriter.WriteTextDelta("Claude reply")
	clWriter.WriteToolUseAndStop("call_xyz", "bash", `{"command":"pwd"}`)

	if recCL.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected X-Accel-Buffering: no on Claude SSE response")
	}
	clBody := recCL.Body.String()
	for _, want := range []string{"event: message_start", "event: ping", `"thinking_delta"`, `"text_delta"`, `"tool_use"`, `"stop_reason":"tool_use"`, "event: message_stop"} {
		if !strings.Contains(clBody, want) {
			t.Fatalf("expected %q in Claude SSE stream:\n%s", want, clBody)
		}
	}
}

func TestSSEWritersNeverEmitThinkingAfterText(t *testing.T) {
	// 1. OpenAI Responses SSE: once WriteTextDelta has been called, late WriteThinkingDelta
	// and WriteStopWithCache must NOT emit reasoning deltas or include a reasoning output item
	// in response.completed (which would cause SSE clients to append Thinking after Text and fold the answer).
	recResp := httptest.NewRecorder()
	respWriter := adapter.NewOpenAIResponsesSSEWriter(recResp, "resp_order_1", "gemini-4-argon")
	respWriter.WriteStart()
	respWriter.WriteTextDelta("Final answer text")
	respWriter.WriteThinkingDelta("Late fallback reasoning from transcript")
	respWriter.WriteStopWithCache("Late fallback reasoning from transcript", "Final answer text", 100, 20, 50)

	respSSE := recResp.Body.String()
	if strings.Contains(respSSE, "response.reasoning_summary_text.delta") {
		t.Fatalf("expected no reasoning_summary_text.delta after text has started:\n%s", respSSE)
	}
	if strings.Contains(respSSE, `"type":"reasoning"`) {
		t.Fatalf("expected response.completed to omit reasoning output item when output_text was streamed:\n%s", respSSE)
	}
	if !strings.Contains(respSSE, `"reasoning_tokens":`) {
		t.Fatalf("expected usage.output_tokens_details.reasoning_tokens to still be preserved:\n%s", respSSE)
	}

	// 2. Claude Messages SSE: late WriteThinkingDelta after WriteTextDelta must be ignored.
	recCL := httptest.NewRecorder()
	clWriter := adapter.NewClaudeSSEWriter(recCL, "msg_order_1", "claude-sonnet-4-5")
	clWriter.WriteStart()
	clWriter.WriteTextDelta("Visible answer")
	clWriter.WriteThinkingDelta("Late thought that should not open a trailing thinking block")
	clWriter.WriteStop()

	clSSE := recCL.Body.String()
	if strings.Contains(clSSE, `"thinking_delta"`) {
		t.Fatalf("expected no thinking_delta after text_delta in Claude SSE:\n%s", clSSE)
	}

	// 3. OpenAI Chat Completions SSE: late WriteThinkingDelta after WriteTextDelta must be ignored.
	recOA := httptest.NewRecorder()
	oaWriter := adapter.NewOpenAISSEWriter(recOA, "chatcmpl_order_1", "gemini-4-argon")
	oaWriter.WriteStart()
	oaWriter.WriteTextDelta("Visible answer")
	oaWriter.WriteThinkingDelta("Late thought")
	oaWriter.WriteStop()

	oaSSE := recOA.Body.String()
	if strings.Contains(oaSSE, `"reasoning_content"`) {
		t.Fatalf("expected no reasoning_content chunk after content chunk in OpenAI SSE:\n%s", oaSSE)
	}
}

func TestClientContextWindowExtractionAcrossProtocols(t *testing.T) {
	// 1. OpenAI Responses with model_context_window in JSON body
	respRaw := `{"model":"gemini-4-argon","model_context_window":512000,"input":"hello"}`
	respReq, err := adapter.ParseOpenAIResponsesRequest([]byte(respRaw), "")
	if err != nil {
		t.Fatalf("ParseOpenAIResponsesRequest failed: %v", err)
	}
	if respReq.ContextWindow != 512000 {
		t.Fatalf("expected ContextWindow=512000 from model_context_window, got %d", respReq.ContextWindow)
	}

	// 2. OpenAI Chat Completions with metadata.context_window string
	oaRaw := `{"model":"gemini-4-argon","metadata":{"context_window":"400000"},"messages":[{"role":"user","content":"hi"}]}`
	oaReq, err := adapter.ParseOpenAIRequest([]byte(oaRaw), "")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest failed: %v", err)
	}
	if oaReq.ContextWindow != 400000 {
		t.Fatalf("expected ContextWindow=400000 from metadata.context_window, got %d", oaReq.ContextWindow)
	}

	// 3. Claude Messages with context_window
	clRaw := `{"model":"claude-sonnet-4-5","context_window":180000,"messages":[{"role":"user","content":"hi"}]}`
	clReq, err := adapter.ParseClaudeRequest([]byte(clRaw), "")
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}
	if clReq.ContextWindow != 180000 {
		t.Fatalf("expected ContextWindow=180000 from Claude context_window, got %d", clReq.ContextWindow)
	}
}
