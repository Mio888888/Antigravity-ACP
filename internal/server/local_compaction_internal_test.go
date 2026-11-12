package server

import (
	"encoding/json"
	"strings"
	"testing"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/adapter"
)

// sailryCompactionTemplate is the compaction prompt shipped in sailry-desktop; it is
// validated client-side and rejects anything but one ```json block in this schema.
const sailryCompactionTemplate = "## Task\nYou are writing a handoff for yourself. The conversation below reached its context budget, and the next model request will see only this handoff in place of the history. Produce the handoff only: do not continue the task, answer questions, or call tools.\n\n" +
	"**Conversation History:**\n{{ messages }}\n\n" +
	"## What to keep\nKeep only what is needed to continue the work correctly:\n- Goal: the current request\n\n" +
	"## Output\nOutput exactly one ```json code block and nothing else, matching this schema:\n```json\n{\n" +
	"  \"user_intent\": [\"Goal\"],\n  \"pending_tasks\": [\"open tasks\"],\n  \"user_messages\": [\"constraints\"],\n" +
	"  \"current_work\": \"State\",\n  \"problem_solving\": [\"Decisions\"],\n  \"next_step\": \"next steps\",\n" +
	"  \"files\": [\n    {\n      \"path\": \"path needed to continue\",\n      \"summary\": \"why it matters\"\n    }\n  ],\n" +
	"  \"errors_and_fixes\": [\"errors\"],\n  \"technical_concepts\": [\"identifiers\"]\n}\n```\n" +
	"Every entry is a plain string except `files`. Omit a field rather than inventing content for it. Only the JSON survives, so it must be self-contained."

func sailryCompactionRequest(history string) *adapter.CanonicalRequest {
	prompt := "Please summarize the conversation history provided in the system prompt."
	return &adapter.CanonicalRequest{
		Protocol:     "openai_responses",
		SystemPrompt: strings.Replace(sailryCompactionTemplate, "{{ messages }}", history, 1),
		Messages:     []adapter.CanonicalMessage{{Role: "user", Content: prompt}},
		PromptBlocks: []acp.PromptBlock{{Type: "text", Text: prompt}},
	}
}

// Reproduces session 09004adf: the gateway answered Sailry's compaction with a markdown
// `# Handoff Summary`, Sailry rejected it as "not a structured handoff" and failed the turn.
func TestLocalCompactionEmitsSailryStructuredHandoff(t *testing.T) {
	var hist strings.Builder
	hist.WriteString("[user]: [user]: review一下未提交的变更，检查下是否符合任务文档的要求\n")
	hist.WriteString("[assistant]: tool_request(load_skill): {\"skill\":\"code-review:code-review\"}\n")
	hist.WriteString("[user]: tool_response: {\"skill\":\"code-review:code-review\",\"content\":\"# Code review\"}\n")
	hist.WriteString("[assistant]: tool_request(review_scope): {\"limit\":100}\n")
	hist.WriteString("[user]: tool_response: {\"review_id\":\"8de73dfd-ef43-8cd1-915b-b1ff76b4b126\",\"next\":null}\n")
	for _, p := range []string{"src-tauri/src/lib.rs", "src-tauri/src/mcp/http_server.rs", "src/lib/mcpApi.ts"} {
		hist.WriteString("[assistant]: tool_request(read_review_change): {\"offset\":0,\"part\":\"diff\",\"path\":\"" + p + "\",\"review_id\":\"8de73dfd-ef43-8cd1-915b-b1ff76b4b126\"}\n")
		hist.WriteString("[user]: tool_response: {\"lines\":[\"+ changed\"],\"next\":null}\n")
	}

	req := sailryCompactionRequest(hist.String())
	if !IsClientCompactionRequest(req) {
		t.Fatalf("expected Sailry compaction request to be detected")
	}
	if !wantsStructuredJSONHandoff(req) {
		t.Fatalf("expected Sailry template to require the structured JSON handoff")
	}
	out := BuildLocalClientCompactionSummary(req)

	if !strings.HasPrefix(out, "```json\n") || !strings.HasSuffix(out, "\n```") || strings.Count(out, "```") != 2 {
		t.Fatalf("expected exactly one ```json code block and nothing else, got:\n%s", out)
	}
	var h map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(out, "```json\n"), "\n```")), &h); err != nil {
		t.Fatalf("handoff is not valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"user_intent", "pending_tasks", "current_work", "next_step", "files", "technical_concepts"} {
		if _, ok := h[key]; !ok {
			t.Fatalf("expected %q in structured handoff, got keys %v", key, h)
		}
	}
	allowed := map[string]bool{"user_intent": true, "pending_tasks": true, "user_messages": true, "current_work": true, "problem_solving": true, "next_step": true, "files": true, "errors_and_fixes": true, "technical_concepts": true}
	for key := range h {
		if !allowed[key] {
			t.Fatalf("unexpected key %q outside the client schema", key)
		}
	}
	files, _ := h["files"].([]any)
	if len(files) == 0 {
		t.Fatalf("expected files read during the review")
	}
	for _, f := range files {
		obj, ok := f.(map[string]any)
		if !ok || obj["path"] == "" || obj["summary"] == "" {
			t.Fatalf("expected each file as {path, summary}, got %v", f)
		}
	}
	if !strings.Contains(out, "review_id=8de73dfd-ef43-8cd1-915b-b1ff76b4b126") {
		t.Fatalf("expected the active review_id preserved for the resumed turn")
	}
	if !strings.Contains(out, "review一下未提交的变更") {
		t.Fatalf("expected the user's request in user_intent")
	}
	if strings.Contains(out, "What to keep") || strings.Contains(out, "Output exactly one") {
		t.Fatalf("expected the template instructions after the history not to leak into the handoff")
	}
}

func TestLocalCompactionKeepsMarkdownForOtherClients(t *testing.T) {
	req := &adapter.CanonicalRequest{
		Protocol:     "openai",
		SystemPrompt: "You are writing a handoff for yourself.\n**Conversation History:**\n[user]: fix the bug\n",
		Messages:     []adapter.CanonicalMessage{{Role: "user", Content: "Please summarize the conversation history provided in the system prompt."}},
	}
	if wantsStructuredJSONHandoff(req) {
		t.Fatalf("plain handoff prompt must not switch to JSON")
	}
	if out := BuildLocalClientCompactionSummary(req); !strings.HasPrefix(out, "# Handoff Summary") {
		t.Fatalf("expected markdown handoff, got:\n%s", out)
	}
}

// extractCurrentWork pulls the current_work string from a structured JSON handoff.
func extractCurrentWork(t *testing.T, handoff string) string {
	t.Helper()
	var h map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(handoff, "```json\n"), "\n```")), &h); err != nil {
		t.Fatalf("handoff not valid JSON: %v\n%s", err, handoff)
	}
	cw, _ := h["current_work"].(string)
	return cw
}

// Reproduces session cc1ae556: across repeated compactions the reviewed-file list must
// accumulate instead of resetting to the few files read since the last resume, and the
// handoff must not recursively nest the prior handoff (the bug that caused the re-read loop).
func TestStructuredHandoffAccumulatesReviewedFilesAcrossCompactions(t *testing.T) {
	cycle := func(prior string, files []string) string {
		var hist strings.Builder
		hist.WriteString("[user]: review一下未提交的变更，检查下是否符合任务文档的要求\n")
		if prior != "" {
			// Sailry feeds the previous handoff back as the opening user turn, re-rendering
			// its JSON as markdown whose ## State carries current_work verbatim.
			hist.WriteString("[user]: # Handoff Summary\n## State\n" + prior + "\n")
		}
		hist.WriteString("[assistant]: tool_request(review_scope): {\"limit\":100}\n")
		hist.WriteString("[user]: tool_response: {\"review_id\":\"0076b03b-719f-8551-b464-f5cbd11a8a3e\"}\n")
		for _, p := range files {
			hist.WriteString("[assistant]: tool_request(plugin_x_read_review_change): {\"offset\":0,\"part\":\"diff\",\"path\":\"" + p + "\",\"review_id\":\"0076b03b-719f-8551-b464-f5cbd11a8a3e\"}\n")
			hist.WriteString("[user]: tool_response: {\"review_id\":\"0076b03b\",\"snapshot\":{\"metadata_revision\":\"" + strings.Repeat("a", 500) + "\"},\"next_offset\":null}\n")
		}
		return extractCurrentWork(t, BuildLocalClientCompactionSummary(sailryCompactionRequest(hist.String())))
	}

	cw1 := cycle("", []string{"src-tauri/src/lib.rs", "src-tauri/src/mcp.rs"})
	cw2 := cycle(cw1, []string{"src-tauri/src/drama.rs"})
	cw3 := cycle(cw2, []string{"src/lib/mcpApi.ts"})

	for _, p := range []string{"src-tauri/src/lib.rs", "src-tauri/src/mcp.rs", "src-tauri/src/drama.rs", "src/lib/mcpApi.ts"} {
		if !strings.Contains(cw3, p) {
			t.Fatalf("expected %q to persist in the cumulative reviewed-file list after 3 compactions, got:\n%s", p, cw3)
		}
	}
	// The earliest files must appear exactly once (merged, not duplicated per cycle).
	if n := strings.Count(cw3, "- src-tauri/src/lib.rs\n"); n != 1 {
		t.Fatalf("expected lib.rs listed once, got %d:\n%s", n, cw3)
	}
	// No recursive nesting: the prior handoff is merged, not embedded verbatim.
	if strings.Contains(cw3, "Earlier progress (from the previous handoff)") {
		t.Fatalf("handoff recursively embeds the prior handoff:\n%s", cw3)
	}
	if n := strings.Count(cw3, "Files already read:"); n != 1 {
		t.Fatalf("expected a single reviewed-files section, got %d:\n%s", n, cw3)
	}
	// The verbose tool-result blobs must not be carried into current_work.
	if strings.Contains(cw3, "metadata_revision") {
		t.Fatalf("expected verbose tool results to be omitted from current_work:\n%s", cw3)
	}
	// The active review_id must survive for the resumed turn.
	if !strings.Contains(cw3, "review_id=0076b03b-719f-8551-b464-f5cbd11a8a3e") {
		t.Fatalf("expected the active review_id in current_work:\n%s", cw3)
	}
}

func TestStructuredHandoffStaysWithinBudget(t *testing.T) {
	var hist strings.Builder
	for i := 0; i < 40; i++ {
		hist.WriteString("[user]: " + strings.Repeat("长需求说明 ", 400) + "\n")
		hist.WriteString("[assistant]: " + strings.Repeat("progress note ", 600) + "\n")
		hist.WriteString("[assistant]: tool_request(read_file): {\"path\":\"src/file" + strings.Repeat("x", i) + ".go\"}\n")
		hist.WriteString("[user]: tool_response: " + strings.Repeat("output ", 2000) + "\n")
	}
	out := BuildLocalClientCompactionSummary(sailryCompactionRequest(hist.String()))
	var h map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(out, "```json\n"), "\n```")), &h); err != nil {
		t.Fatalf("handoff is not valid JSON: %v", err)
	}
	if len(out) > 32*1024 {
		t.Fatalf("structured handoff %d bytes exceeds the compaction output budget", len(out))
	}
	if files, _ := h["files"].([]any); len(files) > maxStructuredHandoffFiles {
		t.Fatalf("expected at most %d files, got %d", maxStructuredHandoffFiles, len(files))
	}
}
