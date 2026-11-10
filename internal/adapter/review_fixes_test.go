package adapter_test

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"antigravity-gateway/internal/adapter"
)

type sseEvent struct {
	Event string
	Raw   string
	Data  map[string]any
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" || strings.HasPrefix(frame, ":") {
			continue
		}
		var ev sseEvent
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Raw = strings.TrimPrefix(line, "data: ")
			}
		}
		if ev.Raw != "" && ev.Raw != "[DONE]" {
			if err := json.Unmarshal([]byte(ev.Raw), &ev.Data); err != nil {
				t.Fatalf("invalid SSE JSON %q: %v", ev.Raw, err)
			}
		}
		events = append(events, ev)
	}
	return events
}

func eventNames(events []sseEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Event)
	}
	return out
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func mapAt(v any, keys ...string) map[string]any {
	cur, _ := v.(map[string]any)
	for _, k := range keys {
		cur, _ = cur[k].(map[string]any)
	}
	return cur
}

func toJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// Anthropic Messages SSE writer
// ---------------------------------------------------------------------------

func TestClaudeSSEThinkingTextToolEventOrder(t *testing.T) {
	rec := httptest.NewRecorder()
	w := adapter.NewClaudeSSEWriter(rec, "msg_1", "claude")
	w.SetInputTokens(1234)
	w.WriteStart()
	w.WriteThinkingDelta("hmm")
	w.WriteTextDelta("answer")
	w.WriteToolUsesAndStopWithUsage([]adapter.CanonicalToolCall{
		{CallID: "call_1", ToolName: "bash", ArgsJSON: "null"},
		{CallID: "call_skip", ToolName: "", ArgsJSON: "{}"},
	}, 10, 0, 5)

	events := parseSSE(t, rec.Body.String())
	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	if got := eventNames(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected event order:\n got %v\nwant %v", got, want)
	}
	if n := num(mapAt(events[0].Data, "message", "usage")["input_tokens"]); n != 1234 {
		t.Fatalf("expected message_start input_tokens=1234, got %d", n)
	}
	thinkingStart := mapAt(events[1].Data, "content_block")
	if sig, ok := thinkingStart["signature"]; !ok || sig != "" {
		t.Fatalf("expected thinking content_block_start to carry empty signature, got %+v", thinkingStart)
	}
	if d := mapAt(events[3].Data, "delta"); d["type"] != "signature_delta" {
		t.Fatalf("expected signature_delta before thinking block stop, got %+v", d)
	}
	wantIdx := []int{0, 0, 0, 0, 1, 1, 1, 2, 2, 2}
	for i, idx := range wantIdx {
		if got := num(events[i+1].Data["index"]); got != idx {
			t.Fatalf("event %d (%s): expected index %d, got %d", i+1, events[i+1].Event, idx, got)
		}
	}
	if tb := mapAt(events[8].Data, "content_block"); tb["type"] != "tool_use" || tb["id"] != "call_1" {
		t.Fatalf("unexpected tool_use block: %+v", tb)
	}
	if pj := mapAt(events[9].Data, "delta")["partial_json"]; pj != "{}" {
		t.Fatalf("expected null args normalized to {}, got %v", pj)
	}
	if sr := mapAt(events[11].Data, "delta")["stop_reason"]; sr != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %v", sr)
	}
}

func TestClaudeSSEWriteErrorClosesBlockOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	w := adapter.NewClaudeSSEWriter(rec, "msg_err", "claude")
	w.WriteStart()
	w.WriteThinkingDelta("partial thought")
	w.WriteError("upstream exploded")
	w.WriteError("second error")
	w.WriteTextDelta("late")
	w.WriteStop()
	w.WriteKeepAlive()

	events := parseSSE(t, rec.Body.String())
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "error"}
	if got := eventNames(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected event order:\n got %v\nwant %v", got, want)
	}
	errEv := events[len(events)-1].Data
	if errEv["type"] != "error" || mapAt(errEv, "error")["type"] != "api_error" || mapAt(errEv, "error")["message"] != "upstream exploded" {
		t.Fatalf("unexpected error event: %+v", errEv)
	}

	// Error after text with no open block left: no stray content_block_stop.
	rec2 := httptest.NewRecorder()
	w2 := adapter.NewClaudeSSEWriter(rec2, "msg_err2", "claude")
	w2.WriteStart()
	w2.WriteError("boom")
	if got := eventNames(parseSSE(t, rec2.Body.String())); !reflect.DeepEqual(got, []string{"message_start", "error"}) {
		t.Fatalf("unexpected events for error without content: %v", got)
	}
}

func TestClaudeSSEStopReasonMapping(t *testing.T) {
	cases := map[string]string{
		"":                  "end_turn",
		"end_turn":          "end_turn",
		"max_tokens":        "max_tokens",
		"max_turn_requests": "max_tokens",
		"refusal":           "refusal",
		"cancelled":         "end_turn",
	}
	for acpStop, want := range cases {
		rec := httptest.NewRecorder()
		w := adapter.NewClaudeSSEWriter(rec, "msg", "claude")
		w.WriteStart()
		w.SetStopReason(acpStop)
		w.WriteTextDelta("x")
		w.WriteStopWithUsage(1, 0, 1)
		events := parseSSE(t, rec.Body.String())
		var got any
		for _, e := range events {
			if e.Event == "message_delta" {
				got = mapAt(e.Data, "delta")["stop_reason"]
			}
		}
		if got != want {
			t.Fatalf("acp stop %q: expected %q, got %v", acpStop, want, got)
		}
	}

	// Only invalid tool calls: finish as a normal stop with a (empty) text block.
	rec := httptest.NewRecorder()
	w := adapter.NewClaudeSSEWriter(rec, "msg", "claude")
	w.WriteStart()
	w.WriteToolUsesAndStop([]adapter.CanonicalToolCall{{CallID: "c", ToolName: ""}})
	events := parseSSE(t, rec.Body.String())
	if got := eventNames(events); !reflect.DeepEqual(got, []string{"message_start", "content_block_start", "content_block_stop", "message_delta", "message_stop"}) {
		t.Fatalf("unexpected events: %v", got)
	}
	if sr := mapAt(events[3].Data, "delta")["stop_reason"]; sr != "end_turn" {
		t.Fatalf("expected end_turn when no valid tool calls remain, got %v", sr)
	}
}

func TestClaudeJSONResponseFixes(t *testing.T) {
	resp := toJSONMap(t, adapter.BuildClaudeJSONResponseMulti("msg", "claude", "deep", "", []adapter.CanonicalToolCall{{CallID: "c1", ToolName: ""}}))
	content, ok := resp["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected non-null content with only the thinking block, got %+v", resp["content"])
	}
	if block := content[0].(map[string]any); block["type"] != "thinking" || block["signature"] != "" {
		t.Fatalf("expected thinking block with empty signature, got %+v", block)
	}
	if resp["stop_reason"] != "end_turn" {
		t.Fatalf("expected end_turn when all tool calls are invalid, got %v", resp["stop_reason"])
	}

	stopped := adapter.ApplyClaudeStopReason(adapter.BuildClaudeJSONResponseMulti("msg", "claude", "", "hi", nil), "max_tokens")
	if stopped["stop_reason"] != "max_tokens" {
		t.Fatalf("expected max_tokens, got %v", stopped["stop_reason"])
	}
	tool := adapter.ApplyClaudeStopReason(adapter.BuildClaudeJSONResponseMulti("msg", "claude", "", "", []adapter.CanonicalToolCall{{CallID: "c1", ToolName: "t", ArgsJSON: "null"}}), "refusal")
	if tool["stop_reason"] != "tool_use" {
		t.Fatalf("expected tool_use to be preserved, got %v", tool["stop_reason"])
	}
	toolJSON := toJSONMap(t, tool)
	if in := toolJSON["content"].([]any)[0].(map[string]any)["input"]; !reflect.DeepEqual(in, map[string]any{}) {
		t.Fatalf("expected null args to become {}, got %+v", in)
	}
}

// ---------------------------------------------------------------------------
// OpenAI Chat Completions SSE writer
// ---------------------------------------------------------------------------

func TestOpenAISSEThinkingTextToolAndUsageChunks(t *testing.T) {
	rec := httptest.NewRecorder()
	w := adapter.NewOpenAISSEWriter(rec, "chatcmpl_1", "m")
	w.WriteStart()
	w.WriteThinkingDelta("reasoning about the problem")
	w.WriteTextDelta("answer")
	w.WriteToolCallsAndFinishWithUsage([]adapter.CanonicalToolCall{
		{CallID: "", ToolName: "skip"},
		{CallID: "call_1", ToolName: "read", ArgsJSON: "null"},
	}, 100, 20, 10)

	events := parseSSE(t, rec.Body.String())
	if len(events) != 7 {
		t.Fatalf("expected 7 data frames, got %d:\n%s", len(events), rec.Body.String())
	}
	if events[len(events)-1].Raw != "[DONE]" {
		t.Fatalf("expected [DONE] last, got %q", events[len(events)-1].Raw)
	}
	delta := func(i int) map[string]any {
		return events[i].Data["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	}
	if d := delta(1); d["reasoning_content"] != "reasoning about the problem" || d["reasoning"] != nil {
		t.Fatalf("expected only reasoning_content, got %+v", d)
	}
	tc := delta(3)["tool_calls"].([]any)
	if len(tc) != 1 {
		t.Fatalf("expected the empty-id call to be skipped, got %+v", tc)
	}
	call := tc[0].(map[string]any)
	if num(call["index"]) != 0 || mapAt(call, "function")["arguments"] != "{}" {
		t.Fatalf("expected index 0 and {} arguments, got %+v", call)
	}
	finish := events[4].Data
	if finish["choices"].([]any)[0].(map[string]any)["finish_reason"] != "tool_calls" || finish["usage"] == nil {
		t.Fatalf("expected tool_calls finish chunk with usage, got %+v", finish)
	}
	usageChunk := events[5].Data
	if choices, ok := usageChunk["choices"].([]any); !ok || len(choices) != 0 {
		t.Fatalf("expected standalone usage chunk with choices: [], got %+v", usageChunk)
	}
	usage := mapAt(usageChunk, "usage")
	reasoning := num(mapAt(usage, "completion_tokens_details")["reasoning_tokens"])
	if reasoning <= 0 || num(usage["completion_tokens"]) != 10+reasoning || num(usage["total_tokens"]) != 100+10+reasoning {
		t.Fatalf("expected completion_tokens to include reasoning tokens, got %+v", usage)
	}
	if !reflect.DeepEqual(usage, mapAt(finish, "usage")) {
		t.Fatalf("expected finish-chunk usage to match the usage chunk: %+v vs %+v", finish["usage"], usage)
	}
}

func TestOpenAISSEWriteErrorAndStopReasons(t *testing.T) {
	rec := httptest.NewRecorder()
	w := adapter.NewOpenAISSEWriter(rec, "chatcmpl_err", "m")
	w.WriteStart()
	w.WriteTextDelta("partial")
	w.WriteError("upstream died")
	w.WriteError("again")
	w.WriteTextDelta("late")
	w.WriteStop()

	events := parseSSE(t, rec.Body.String())
	if len(events) != 4 || events[3].Raw != "[DONE]" {
		t.Fatalf("expected start, text, error, [DONE]; got:\n%s", rec.Body.String())
	}
	if e := mapAt(events[2].Data, "error"); e["message"] != "upstream died" || e["type"] != "upstream_error" {
		t.Fatalf("unexpected error chunk: %+v", events[2].Data)
	}

	cases := map[string]string{
		"end_turn":          "stop",
		"max_tokens":        "length",
		"max_turn_requests": "length",
		"refusal":           "content_filter",
		"cancelled":         "stop",
	}
	for acpStop, want := range cases {
		rec := httptest.NewRecorder()
		w := adapter.NewOpenAISSEWriter(rec, "c", "m")
		w.SetStopReason(acpStop)
		w.WriteStopWithUsage(5, 0, 3)
		events := parseSSE(t, rec.Body.String())
		if got := events[0].Data["choices"].([]any)[0].(map[string]any)["finish_reason"]; got != want {
			t.Fatalf("acp stop %q: expected %q, got %v", acpStop, want, got)
		}
	}

	// Only invalid tool calls: a normal stop finish, honoring the stop reason.
	rec2 := httptest.NewRecorder()
	w2 := adapter.NewOpenAISSEWriter(rec2, "c", "m")
	w2.SetStopReason("max_tokens")
	w2.WriteToolCallsAndFinish([]adapter.CanonicalToolCall{{CallID: "c1", ToolName: " "}})
	body := rec2.Body.String()
	if strings.Contains(body, "tool_calls") || !strings.Contains(body, `"finish_reason":"length"`) {
		t.Fatalf("expected a length finish without tool_calls, got:\n%s", body)
	}
}

func TestOpenAIJSONResponseFixes(t *testing.T) {
	resp := toJSONMap(t, adapter.BuildOpenAIJSONResponseMulti("c", "m", "some long reasoning text here", "", []adapter.CanonicalToolCall{{CallID: "call_1", ToolName: "t", ArgsJSON: "null"}}, 100, 0, 2))
	choice := resp["choices"].([]any)[0].(map[string]any)
	msg := choice["message"].(map[string]any)
	if v, ok := msg["content"]; !ok || v != nil {
		t.Fatalf("expected content:null with only tool calls, got %+v", msg)
	}
	if _, dup := msg["reasoning"]; dup || msg["reasoning_content"] == nil {
		t.Fatalf("expected only reasoning_content, got %+v", msg)
	}
	if args := mapAt(msg["tool_calls"].([]any)[0], "function")["arguments"]; args != "{}" {
		t.Fatalf("expected {} arguments, got %v", args)
	}
	usage := mapAt(resp, "usage")
	reasoning := num(mapAt(usage, "completion_tokens_details")["reasoning_tokens"])
	if reasoning <= 0 || reasoning > num(usage["completion_tokens"]) || num(usage["completion_tokens"]) != 2+reasoning {
		t.Fatalf("expected completion_tokens = override + reasoning, got %+v", usage)
	}

	empty := toJSONMap(t, adapter.BuildOpenAIJSONResponseMulti("c", "m", "", "", []adapter.CanonicalToolCall{{CallID: "c1", ToolName: ""}}))
	emptyChoice := empty["choices"].([]any)[0].(map[string]any)
	if emptyChoice["finish_reason"] != "stop" || emptyChoice["message"].(map[string]any)["content"] != "" {
		t.Fatalf("expected stop with empty-string content, got %+v", emptyChoice)
	}

	stopped := adapter.ApplyOpenAIStopReason(adapter.BuildOpenAIJSONResponseMulti("c", "m", "", "hi", nil), "refusal")
	if fr := stopped["choices"].([]map[string]any)[0]["finish_reason"]; fr != "content_filter" {
		t.Fatalf("expected content_filter, got %v", fr)
	}
	tools := adapter.ApplyOpenAIStopReason(adapter.BuildOpenAIJSONResponseMulti("c", "m", "", "", []adapter.CanonicalToolCall{{CallID: "c1", ToolName: "t"}}), "max_tokens")
	if fr := tools["choices"].([]map[string]any)[0]["finish_reason"]; fr != "tool_calls" {
		t.Fatalf("expected tool_calls to be preserved, got %v", fr)
	}
}

// ---------------------------------------------------------------------------
// OpenAI Responses SSE writer
// ---------------------------------------------------------------------------

func assertResponsesSequence(t *testing.T, events []sseEvent) {
	t.Helper()
	for i, e := range events {
		if got := num(e.Data["sequence_number"]); got != i {
			t.Fatalf("event %d (%s): expected sequence_number %d, got %d", i, e.Event, i, got)
		}
		if e.Data["type"] != e.Event {
			t.Fatalf("event %d: type %v != event %s", i, e.Data["type"], e.Event)
		}
	}
}

func doneItems(events []sseEvent) []any {
	var items []any
	for _, e := range events {
		if e.Event == "response.output_item.done" {
			items = append(items, e.Data["item"])
		}
	}
	return items
}

func TestResponsesSSEOutputMatchesStreamedItems(t *testing.T) {
	rec := httptest.NewRecorder()
	w := adapter.NewOpenAIResponsesSSEWriter(rec, "resp_abc", "m")
	w.WriteStart()
	w.WriteThinkingDelta("plan")
	w.WriteTextDelta("doing it")
	w.WriteToolCallsAndFinish([]adapter.CanonicalToolCall{
		{CallID: "call_1", ToolName: "a", ArgsJSON: `{"x":1}`},
		{CallID: "call_bad", ToolName: ""},
		{CallID: "call_2", ToolName: "b", ArgsJSON: "null"},
	}, "plan", "doing it", 50, 10, 5)

	events := parseSSE(t, rec.Body.String())
	assertResponsesSequence(t, events)
	want := []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.reasoning_summary_part.added", "response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done", "response.reasoning_summary_part.done", "response.output_item.done",
		"response.output_item.added", "response.content_part.added", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done",
		"response.completed",
	}
	if got := eventNames(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected event order:\n got %v\nwant %v", got, want)
	}

	var doneIdx []int
	for _, e := range events {
		if e.Event == "response.output_item.done" {
			doneIdx = append(doneIdx, num(e.Data["output_index"]))
		}
	}
	if !reflect.DeepEqual(doneIdx, []int{0, 1, 2, 3}) {
		t.Fatalf("expected output indexes 0..3, got %v", doneIdx)
	}

	created := mapAt(events[0].Data, "response")
	completed := mapAt(events[len(events)-1].Data, "response")
	if created["created_at"] != completed["created_at"] {
		t.Fatalf("created_at mismatch: %v vs %v", created["created_at"], completed["created_at"])
	}
	if !reflect.DeepEqual(completed["output"], doneItems(events)) {
		t.Fatalf("completed output differs from streamed items:\n%+v\nvs\n%+v", completed["output"], doneItems(events))
	}
	output := completed["output"].([]any)
	for i, wantID := range []string{"rs_abc", "msg_abc", "fc_call_1", "fc_call_2"} {
		if id := output[i].(map[string]any)["id"]; id != wantID {
			t.Fatalf("output[%d]: expected id %s, got %v", i, wantID, id)
		}
	}
	if args := output[3].(map[string]any)["arguments"]; args != "{}" {
		t.Fatalf("expected null args normalized to {}, got %v", args)
	}
	if _, ok := completed["reasoning_text"]; ok {
		t.Fatalf("expected no reasoning_text field: %+v", completed)
	}
	if _, ok := mapAt(completed, "reasoning")["effort"]; ok {
		t.Fatalf("expected no hard-coded reasoning.effort: %+v", completed["reasoning"])
	}
	if completed["status"] != "completed" {
		t.Fatalf("expected completed status, got %v", completed["status"])
	}
}

func TestResponsesSSENoEmptyMessageItem(t *testing.T) {
	rec := httptest.NewRecorder()
	w := adapter.NewOpenAIResponsesSSEWriter(rec, "resp_empty", "m")
	w.WriteStart()
	w.WriteStopWithCache("", "", 10, 0, 1)
	events := parseSSE(t, rec.Body.String())
	assertResponsesSequence(t, events)
	if got := eventNames(events); !reflect.DeepEqual(got, []string{"response.created", "response.in_progress", "response.completed"}) {
		t.Fatalf("expected no message item for empty output, got %v", got)
	}
	completed := mapAt(events[2].Data, "response")
	if out, ok := completed["output"].([]any); !ok || len(out) != 0 {
		t.Fatalf("expected empty output array, got %+v", completed["output"])
	}

	// Only invalid tool calls behave like a normal stop.
	rec2 := httptest.NewRecorder()
	w2 := adapter.NewOpenAIResponsesSSEWriter(rec2, "resp_bad", "m")
	w2.WriteStart()
	w2.WriteToolCallsAndFinish([]adapter.CanonicalToolCall{{CallID: "x", ToolName: ""}}, "", "text", 1, 0, 1)
	body := rec2.Body.String()
	if strings.Contains(body, "function_call") || !strings.Contains(body, "event: response.completed") {
		t.Fatalf("expected normal completion without function calls, got:\n%s", body)
	}
}

func TestResponsesSSEWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	w := adapter.NewOpenAIResponsesSSEWriter(rec, "resp_err", "m")
	w.WriteStart()
	w.WriteThinkingDelta("think")
	w.WriteTextDelta("partial")
	w.WriteError("agent crashed")
	w.WriteError("again")
	w.WriteTextDelta("late")
	w.WriteStopWithCache("", "late", 1, 0, 1)

	events := parseSSE(t, rec.Body.String())
	assertResponsesSequence(t, events)
	names := eventNames(events)
	wantTail := []string{"response.output_text.done", "response.content_part.done", "response.output_item.done", "response.failed"}
	if !reflect.DeepEqual(names[len(names)-4:], wantTail) {
		t.Fatalf("unexpected terminal events: %v", names)
	}
	if strings.Count(rec.Body.String(), "response.failed") != 2 { // event line + type field
		t.Fatalf("expected exactly one response.failed event:\n%s", rec.Body.String())
	}
	msgItem := events[len(events)-2].Data["item"].(map[string]any)
	if msgItem["status"] != "incomplete" {
		t.Fatalf("expected the open message to close as incomplete, got %+v", msgItem)
	}
	failed := mapAt(events[len(events)-1].Data, "response")
	if failed["status"] != "failed" || mapAt(failed, "error")["code"] != "server_error" || mapAt(failed, "error")["message"] != "agent crashed" {
		t.Fatalf("unexpected response.failed payload: %+v", failed)
	}
	if !reflect.DeepEqual(failed["output"], doneItems(events)) {
		t.Fatalf("failed output differs from streamed items")
	}

	// Error while only reasoning is open.
	rec2 := httptest.NewRecorder()
	w2 := adapter.NewOpenAIResponsesSSEWriter(rec2, "resp_err2", "m")
	w2.WriteStart()
	w2.WriteThinkingDelta("think")
	w2.WriteError("boom")
	events2 := parseSSE(t, rec2.Body.String())
	assertResponsesSequence(t, events2)
	names2 := eventNames(events2)
	if !reflect.DeepEqual(names2[len(names2)-4:], []string{"response.reasoning_summary_text.done", "response.reasoning_summary_part.done", "response.output_item.done", "response.failed"}) {
		t.Fatalf("unexpected terminal events: %v", names2)
	}
}

func TestResponsesSSEIncompleteStopReasons(t *testing.T) {
	cases := map[string]string{"max_tokens": "max_output_tokens", "max_turn_requests": "max_output_tokens", "refusal": "content_filter"}
	for acpStop, reason := range cases {
		rec := httptest.NewRecorder()
		w := adapter.NewOpenAIResponsesSSEWriter(rec, "resp_inc", "m")
		w.WriteStart()
		w.SetStopReason(acpStop)
		w.WriteTextDelta("cut")
		w.WriteStopWithCache("", "cut", 1, 0, 1)
		events := parseSSE(t, rec.Body.String())
		assertResponsesSequence(t, events)
		last := events[len(events)-1]
		resp := mapAt(last.Data, "response")
		if last.Event != "response.incomplete" || resp["status"] != "incomplete" || mapAt(resp, "incomplete_details")["reason"] != reason {
			t.Fatalf("acp stop %q: unexpected terminal event %s %+v", acpStop, last.Event, resp)
		}
	}
	for _, acpStop := range []string{"end_turn", "cancelled", ""} {
		rec := httptest.NewRecorder()
		w := adapter.NewOpenAIResponsesSSEWriter(rec, "resp_ok", "m")
		w.SetStopReason(acpStop)
		w.WriteStopWithCache("", "ok", 1, 0, 1)
		events := parseSSE(t, rec.Body.String())
		if last := events[len(events)-1]; last.Event != "response.completed" || mapAt(last.Data, "response")["status"] != "completed" {
			t.Fatalf("acp stop %q: expected completed, got %s", acpStop, last.Event)
		}
	}
}

func TestResponsesJSONResponseFixes(t *testing.T) {
	empty := toJSONMap(t, adapter.BuildOpenAIResponsesJSONResponseMulti("resp_1", "m", "", "", []adapter.CanonicalToolCall{{CallID: "c", ToolName: ""}}, 1, 0, 1))
	if out, ok := empty["output"].([]any); !ok || len(out) != 0 {
		t.Fatalf("expected empty (non-null) output array, got %+v", empty["output"])
	}
	if _, ok := empty["reasoning_text"]; ok {
		t.Fatalf("expected no reasoning_text: %+v", empty)
	}
	if _, ok := mapAt(empty, "reasoning")["effort"]; ok {
		t.Fatalf("expected no hard-coded effort: %+v", empty["reasoning"])
	}

	inc := adapter.ApplyResponsesStopReason(adapter.BuildOpenAIResponsesJSONResponseMulti("resp_1", "m", "", "cut", nil, 1, 0, 1), "max_tokens")
	if inc["status"] != "incomplete" || inc["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("unexpected incomplete response: %+v", inc)
	}
	tools := adapter.ApplyResponsesStopReason(adapter.BuildOpenAIResponsesJSONResponseMulti("resp_1", "m", "", "", []adapter.CanonicalToolCall{{CallID: "c", ToolName: "t"}}, 1, 0, 1), "refusal")
	if tools["status"] != "completed" {
		t.Fatalf("expected function-call responses to stay completed, got %v", tools["status"])
	}
}

// ---------------------------------------------------------------------------
// Request parsers
// ---------------------------------------------------------------------------

func TestClaudeToolResultSiblingsAndPlaceholders(t *testing.T) {
	raw := `{
		"model": "claude",
		"messages": [
			{"role": "user", "content": "Run things"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "t1", "name": "a", "input": {}},
				{"type": "tool_use", "id": "t2", "name": "b", "input": null}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "t1"},
				{"type": "tool_result", "tool_use_id": "t2", "is_error": true, "content": [
					{"type": "text", "text": "failed"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "AAAA"}}
				]},
				{"type": "text", "text": "Also look at this"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "BBBB"}}
			]}
		]
	}`
	req, err := adapter.ParseClaudeRequest([]byte(raw), "")
	if err != nil {
		t.Fatalf("ParseClaudeRequest: %v", err)
	}
	if len(req.ToolResults) != 2 {
		t.Fatalf("expected 2 tool results, got %+v", req.ToolResults)
	}
	if req.ToolResults[0].Content != "" || req.ToolResults[0].IsError {
		t.Fatalf("expected missing content to be \"\", got %+v", req.ToolResults[0])
	}
	want := "failed\n[image omitted: image/png]\n\nAlso look at this\n\n[image omitted: image/jpeg]"
	if req.ToolResults[1].Content != want || !req.ToolResults[1].IsError {
		t.Fatalf("unexpected last tool result:\n got %q (is_error=%v)\nwant %q", req.ToolResults[1].Content, req.ToolResults[1].IsError, want)
	}
	if len(req.PromptBlocks) != 1 || req.PromptBlocks[0].Type != "text" {
		t.Fatalf("expected one rendered tool-result text block, got %+v", req.PromptBlocks)
	}
	prompt := req.PromptBlocks[0].Text
	if !strings.HasPrefix(prompt, "[tool_result:t1]\n\n\n[tool_result:t2] (error)\nfailed") || strings.Contains(prompt, "Run things") {
		t.Fatalf("unexpected tool-result prompt:\n%s", prompt)
	}
	if !strings.Contains(req.Messages[1].Content, "b({})") {
		t.Fatalf("expected null tool_use input rendered as {}, got %q", req.Messages[1].Content)
	}
}

func TestClaudeDocumentSources(t *testing.T) {
	raw := `{
		"model": "claude",
		"messages": [{"role": "user", "content": [
			{"type": "document", "title": "My Spec.pdf", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBER"}},
			{"type": "document", "title": "remote", "source": {"type": "url", "url": "https://example.com/a b.pdf"}},
			{"type": "document", "source": {"type": "file", "file_id": "file_123"}},
			{"type": "document", "title": "empty", "source": {"type": "base64", "media_type": "application/pdf"}}
		]}]
	}`
	req, err := adapter.ParseClaudeRequest([]byte(raw), "")
	if err != nil {
		t.Fatalf("ParseClaudeRequest: %v", err)
	}
	b := req.PromptBlocks
	if len(b) != 4 {
		t.Fatalf("expected 4 blocks, got %+v", b)
	}
	if b[0].Type != "resource" || b[0].Resource.URI != "file:///My%20Spec.pdf" || b[0].Resource.Blob != "JVBER" || b[0].Resource.MimeType != "application/pdf" {
		t.Fatalf("unexpected base64 document block: %+v / %+v", b[0], b[0].Resource)
	}
	if b[1].Type != "resource_link" || b[1].URI != "https://example.com/a b.pdf" || b[1].Name != "remote" {
		t.Fatalf("unexpected URL document block: %+v", b[1])
	}
	if b[2].Type != "text" || !strings.Contains(b[2].Text, "file_123") {
		t.Fatalf("expected file_id placeholder, got %+v", b[2])
	}
	if b[3].Type != "text" || b[3].Resource != nil {
		t.Fatalf("expected placeholder (not an empty resource), got %+v", b[3])
	}
}

func TestOpenAIChatFilePartsAndSessionAndLenientArgs(t *testing.T) {
	raw := `{
		"model": "m",
		"user": "end-user-42",
		"context_window": 200000.0,
		"reasoning": {"max_tokens": 2000.0},
		"messages": [
			{"role": "user", "content": [
				{"type": "file", "file": {"filename": "report q1.pdf", "file_data": "data:application/pdf;base64,JVBERi0"}},
				{"type": "file", "file": {"file_id": "file-abc"}}
			]},
			{"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "f", "arguments": {"a": 1}}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "result one"}
		]
	}`
	req, err := adapter.ParseOpenAIRequest([]byte(raw), "")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest: %v", err)
	}
	if req.ClientSessionID != "" || req.SessionHeader != "" {
		t.Fatalf("expected the user field not to be used as a session key, got %q / %q", req.ClientSessionID, req.SessionHeader)
	}
	if req.ContextWindow != 200000 || req.ThinkingLevel != "low" {
		t.Fatalf("expected float integer fields to parse, got ctx=%d thinking=%q", req.ContextWindow, req.ThinkingLevel)
	}
	if req.Messages[1].Content != `f({"a":1})` {
		t.Fatalf("expected object arguments re-marshalled, got %q", req.Messages[1].Content)
	}
	if len(req.PromptBlocks) != 1 || req.PromptBlocks[0].Text != "[tool_result:call_1]\nresult one" {
		t.Fatalf("expected PromptBlocks to render the trailing tool result, got %+v", req.PromptBlocks)
	}

	userOnly, err := adapter.ParseOpenAIRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type": "file", "file": {"filename": "report q1.pdf", "file_data": "data:application/pdf;base64,JVBERi0"}},
		{"type": "file", "file": {"file_id": "file-abc"}}
	]}]}`), "")
	if err != nil {
		t.Fatalf("ParseOpenAIRequest: %v", err)
	}
	b := userOnly.PromptBlocks
	if len(b) != 2 || b[0].Type != "resource" || b[0].Resource.MimeType != "application/pdf" || b[0].Resource.Blob != "JVBERi0" || b[0].Resource.URI != "file:///report%20q1.pdf" {
		t.Fatalf("unexpected file_data block: %+v", b)
	}
	if b[1].Type != "text" || b[1].Text != "[file attachment: file-abc not available]" {
		t.Fatalf("unexpected file_id placeholder: %+v", b[1])
	}
}

func TestResponsesSessionPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		header string
		want   string
	}{
		{"prev beats metadata", `{"previous_response_id":"resp_prev","metadata":{"session_id":"meta_sess"},"input":"hi"}`, "", "resp_prev"},
		{"header beats prev", `{"previous_response_id":"resp_prev","input":"hi"}`, "hdr_sess", "hdr_sess"},
		{"top-level conversation id beats prev", `{"previous_response_id":"resp_prev","conversation_id":"conv_1","input":"hi"}`, "", "conv_1"},
		{"user ignored", `{"user":"end-user","input":"hi"}`, "", ""},
		{"metadata fallback", `{"metadata":{"session_id":"meta_sess"},"input":"hi"}`, "", "meta_sess"},
	}
	for _, tc := range cases {
		req, err := adapter.ParseOpenAIResponsesRequest([]byte(tc.body), tc.header)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if req.SessionHeader != tc.want {
			t.Fatalf("%s: expected session %q, got %q", tc.name, tc.want, req.SessionHeader)
		}
	}
}

func TestResponsesToolOutputsParsing(t *testing.T) {
	raw := `{
		"model": "m",
		"context_window": 128000.0,
		"input": [
			{"role": "user", "content": [{"type": "input_text", "text": "go"}]},
			{"type": "reasoning", "summary": []},
			{"type": "function_call", "call_id": "call_1", "name": "f", "arguments": {"p": "x"}},
			{"type": "function_call_output", "call_id": "call_1", "output": "out one"},
			{"type": "custom_tool_call", "call_id": "call_2", "name": "apply_patch", "input": "*** patch"},
			{"type": "custom_tool_call_output", "call_id": "call_2", "output": [
				{"type": "input_text", "text": "part a"},
				{"type": "input_text", "text": "part b"},
				{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}
			]}
		]
	}`
	req, err := adapter.ParseOpenAIResponsesRequest([]byte(raw), "")
	if err != nil {
		t.Fatalf("ParseOpenAIResponsesRequest: %v", err)
	}
	if req.ContextWindow != 128000 {
		t.Fatalf("expected float context_window to parse, got %d", req.ContextWindow)
	}
	if len(req.ToolResults) != 2 || req.ToolResults[0].CallID != "call_1" || req.ToolResults[1].CallID != "call_2" {
		t.Fatalf("expected interleaved outputs to be collected, got %+v", req.ToolResults)
	}
	if req.ToolResults[1].Content != "part a\npart b\n[image omitted: image/png]" {
		t.Fatalf("unexpected custom tool output content: %q", req.ToolResults[1].Content)
	}
	want := "[tool_result:call_1]\nout one\n\n[tool_result:call_2]\npart a\npart b\n[image omitted: image/png]"
	if len(req.PromptBlocks) != 1 || req.PromptBlocks[0].Text != want {
		t.Fatalf("unexpected PromptBlocks:\n%+v", req.PromptBlocks)
	}
	foundArgs := false
	for _, m := range req.Messages {
		if m.Content == `f({"p":"x"})` {
			foundArgs = true
		}
	}
	if !foundArgs {
		t.Fatalf("expected object arguments re-marshalled in history, got %+v", req.Messages)
	}

	// A new user message after outputs (no tool outputs since) yields a normal prompt.
	fresh, err := adapter.ParseOpenAIResponsesRequest([]byte(`{"input":[
		{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},
		{"type":"function_call_output","call_id":"c","output":"o"},
		{"role":"assistant","content":[{"type":"output_text","text":"done"}]},
		{"role":"user","content":[{"type":"input_text","text":"next question"}]}
	]}`), "")
	if err != nil {
		t.Fatalf("ParseOpenAIResponsesRequest: %v", err)
	}
	if len(fresh.ToolResults) != 0 || len(fresh.PromptBlocks) != 1 || fresh.PromptBlocks[0].Text != "next question" {
		t.Fatalf("expected a plain user prompt, got results=%+v blocks=%+v", fresh.ToolResults, fresh.PromptBlocks)
	}
}

func TestClaudeLenientIntegerFields(t *testing.T) {
	req, err := adapter.ParseClaudeRequest([]byte(`{"model":"claude","context_window":200000.0,"thinking":{"type":"enabled","budget_tokens":8000.0},"messages":[{"role":"user","content":"hi"}]}`), "")
	if err != nil {
		t.Fatalf("ParseClaudeRequest: %v", err)
	}
	if req.ContextWindow != 200000 || req.ThinkingLevel != "medium" {
		t.Fatalf("expected float fields to parse, got ctx=%d thinking=%q", req.ContextWindow, req.ThinkingLevel)
	}
}
