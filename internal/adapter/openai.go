package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"antigravity-gateway/internal/acp"
)

// flexInt64 decodes JSON numbers (including float spellings such as 200000.0),
// numeric strings and null. Unparseable values decode to 0 instead of failing the request.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			*f = 0
			return nil
		}
		s = strings.TrimSpace(str)
	}
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexInt64(v)
	return nil
}

// flexJSONString decodes either a JSON string or any other JSON value (object, array, ...),
// the latter re-marshalled into its compact string form.
type flexJSONString string

func (f *flexJSONString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "" || s == "null":
		*f = ""
	case strings.HasPrefix(s, `"`):
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*f = flexJSONString(str)
	default:
		var buf bytes.Buffer
		if err := json.Compact(&buf, b); err != nil {
			*f = flexJSONString(s)
			return nil
		}
		*f = flexJSONString(buf.String())
	}
	return nil
}

// argsToJSONString converts a decoded tool-call arguments value into a JSON string.
func argsToJSONString(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	default:
		raw, err := json.Marshal(val)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

// normalizeToolArgs maps empty or `null` arguments to an empty JSON object.
func normalizeToolArgs(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	return args
}

// validToolCalls drops calls without an id or name and normalizes their arguments.
func validToolCalls(calls []CanonicalToolCall) []CanonicalToolCall {
	var out []CanonicalToolCall
	for _, c := range calls {
		if strings.TrimSpace(c.CallID) == "" || strings.TrimSpace(c.ToolName) == "" {
			continue
		}
		c.ArgsJSON = normalizeToolArgs(c.ArgsJSON)
		out = append(out, c)
	}
	return out
}

func normalizeACPStopReason(s string) string {
	switch strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "_", "") {
	case "maxtokens":
		return "max_tokens"
	case "maxturnrequests":
		return "max_turn_requests"
	case "refusal":
		return "refusal"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return "end_turn"
	}
}

func chatFinishReasonForStop(acpStopReason string) string {
	switch normalizeACPStopReason(acpStopReason) {
	case "max_tokens", "max_turn_requests":
		return "length"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

func fileURI(name string) string {
	segments := strings.Split(strings.TrimLeft(name, "/"), "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return "file:///" + strings.Join(segments, "/")
}

// fileAttachmentBlock converts an OpenAI file part into an ACP prompt block. Attachments
// without inline data or a URL become a text placeholder rather than an empty resource.
func fileAttachmentBlock(filename, fileData, fileID, fileURL string) acp.PromptBlock {
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "attachment"
	}
	if fileData != "" {
		if mime, b64, ok := ParseDataURI(fileData); ok {
			return acp.PromptBlock{
				Type:     "resource",
				Resource: &acp.EmbeddedResource{URI: fileURI(name), MimeType: mime, Blob: b64},
			}
		}
		return acp.PromptBlock{
			Type:     "resource",
			Resource: &acp.EmbeddedResource{URI: fileURI(name), Blob: fileData},
		}
	}
	if strings.TrimSpace(fileURL) != "" {
		return acp.PromptBlock{Type: "resource_link", URI: strings.TrimSpace(fileURL), Name: name}
	}
	label := strings.TrimSpace(fileID)
	if label == "" {
		label = name
	}
	return acp.PromptBlock{Type: "text", Text: fmt.Sprintf("[file attachment: %s not available]", label)}
}

// describeNonTextBlock renders a textual stand-in for a non-text prompt block, used where
// only text can be carried (tool result content).
func describeNonTextBlock(b acp.PromptBlock) string {
	switch b.Type {
	case "image":
		mime := b.MimeType
		if mime == "" {
			mime = "image"
		}
		return fmt.Sprintf("[image omitted: %s]", mime)
	case "audio":
		mime := b.MimeType
		if mime == "" {
			mime = "audio"
		}
		return fmt.Sprintf("[audio omitted: %s]", mime)
	case "resource":
		if b.Resource == nil {
			return ""
		}
		if b.Resource.Text != "" {
			return fmt.Sprintf("[file: %s]\n%s", b.Resource.URI, b.Resource.Text)
		}
		mime := b.Resource.MimeType
		if mime == "" {
			mime = "application/octet-stream"
		}
		return fmt.Sprintf("[file omitted: %s (%s)]", b.Resource.URI, mime)
	case "resource_link":
		return fmt.Sprintf("[attachment: %s]", b.URI)
	}
	return ""
}

// blocksAsToolResultText flattens prompt blocks into text pieces suitable for appending to a tool result.
func blocksAsToolResultText(blocks []acp.PromptBlock) []string {
	var out []string
	for _, b := range blocks {
		var s string
		if b.Type == "text" {
			s = b.Text
		} else {
			s = describeNonTextBlock(b)
		}
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func appendToLastToolResult(results []CanonicalToolResult, extras []string) {
	if len(results) == 0 || len(extras) == 0 {
		return
	}
	addition := strings.Join(extras, "\n\n")
	last := &results[len(results)-1]
	if last.Content != "" {
		last.Content += "\n\n" + addition
	} else {
		last.Content = addition
	}
}

// toolResultsPromptBlocks renders the trailing tool results as the turn's prompt, so that a
// fresh prompt (e.g. when the parked call has expired) carries the tool output rather than
// replaying the earlier user question.
func toolResultsPromptBlocks(results []CanonicalToolResult) []acp.PromptBlock {
	if len(results) == 0 {
		return nil
	}
	parts := make([]string, 0, len(results))
	for _, tr := range results {
		header := fmt.Sprintf("[tool_result:%s]", tr.CallID)
		if tr.IsError {
			header += " (error)"
		}
		parts = append(parts, header+"\n"+tr.Content)
	}
	return []acp.PromptBlock{{Type: "text", Text: strings.Join(parts, "\n\n")}}
}

type openAIRawRequest struct {
	Model           string `json:"model"`
	Stream          bool   `json:"stream"`
	ReasoningEffort string `json:"reasoning_effort"`
	Reasoning       *struct {
		Effort    string    `json:"effort"`
		MaxTokens flexInt64 `json:"max_tokens"`
	} `json:"reasoning"`
	Thinking *struct {
		Type         string    `json:"type"`
		BudgetTokens flexInt64 `json:"budget_tokens"`
		Effort       string    `json:"effort"`
	} `json:"thinking"`
	OutputConfig *struct {
		Effort string `json:"effort"`
	} `json:"output_config"`
	ContextWindow      flexInt64          `json:"context_window"`
	ModelContextWindow flexInt64          `json:"model_context_window"`
	MaxInputTokens     flexInt64          `json:"max_input_tokens"`
	MaxContextTokens   flexInt64          `json:"max_context_tokens"`
	SessionID          string             `json:"session_id"`
	ConversationID     string             `json:"conversation_id"`
	PromptCacheKey     string             `json:"prompt_cache_key"`
	Metadata           map[string]any     `json:"metadata"`
	Messages           []openAIRawMessage `json:"messages"`
	Tools              []openAIRawTool    `json:"tools"`
}

type openAIRawMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string         `json:"name"`
			Arguments flexJSONString `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
}

type openAIRawTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func ParseOpenAIRequest(body []byte, sessionHeader string) (*CanonicalRequest, error) {
	var raw openAIRawRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode openai request: %w", err)
	}
	if raw.Model == "" {
		raw.Model = "gemini-2.5-pro"
	}

	effort := strings.TrimSpace(raw.ReasoningEffort)
	budget := 0
	if raw.Reasoning != nil {
		if effort == "" {
			effort = strings.TrimSpace(raw.Reasoning.Effort)
		}
		if budget == 0 && raw.Reasoning.MaxTokens > 0 {
			budget = int(raw.Reasoning.MaxTokens)
		}
	}
	if raw.Thinking != nil {
		if effort == "" && strings.TrimSpace(raw.Thinking.Effort) != "" {
			effort = strings.TrimSpace(raw.Thinking.Effort)
		}
		if budget == 0 && raw.Thinking.BudgetTokens > 0 {
			budget = int(raw.Thinking.BudgetTokens)
		}
		if effort == "" && budget == 0 && strings.TrimSpace(raw.Thinking.Type) != "" {
			effort = strings.TrimSpace(raw.Thinking.Type)
		}
	}
	if effort == "" && raw.OutputConfig != nil && strings.TrimSpace(raw.OutputConfig.Effort) != "" {
		effort = strings.TrimSpace(raw.OutputConfig.Effort)
	}

	normalizedThinking := NormalizeThinkingLevel(effort, budget)
	cleanModel, finalThinking := SplitModelAndThinkingSuffix(raw.Model, normalizedThinking)

	clientSessID := ExtractClientSessionID(sessionHeader, raw.Metadata, raw.SessionID, raw.ConversationID, raw.PromptCacheKey)
	effSession := strings.TrimSpace(sessionHeader)
	if effSession == "" {
		effSession = clientSessID
	}

	req := &CanonicalRequest{
		Protocol:        "openai",
		Model:           cleanModel,
		Stream:          raw.Stream,
		ThinkingLevel:   finalThinking,
		ContextWindow:   ExtractContextWindowFromMetadata(raw.Metadata, int64(raw.ContextWindow), int64(raw.ModelContextWindow), int64(raw.MaxContextTokens), int64(raw.MaxInputTokens)),
		SessionHeader:   effSession,
		ClientSessionID: clientSessID,
	}

	for _, t := range raw.Tools {
		if t.Function.Name != "" {
			req.Tools = append(req.Tools, ToMCPToolDefinition(t.Function.Name, t.Function.Description, t.Function.Parameters))
		}
	}

	var sysParts []string
	var lastUserBlocks []acp.PromptBlock

	for _, m := range raw.Messages {
		text, blocks := parseOpenAIContentBlocks(m.Content)
		switch m.Role {
		case "system", "developer":
			if strings.TrimSpace(text) != "" {
				sysParts = append(sysParts, strings.TrimSpace(text))
			}
		case "tool":
			content := text
			var extras []string
			for _, b := range blocks {
				if b.Type != "text" {
					if d := describeNonTextBlock(b); d != "" {
						extras = append(extras, d)
					}
				}
			}
			if len(extras) > 0 {
				if content != "" {
					content += "\n\n"
				}
				content += strings.Join(extras, "\n\n")
			}
			req.ToolResults = append(req.ToolResults, CanonicalToolResult{
				CallID:  strings.TrimSpace(m.ToolCallID),
				Content: content,
			})
			req.Messages = append(req.Messages, CanonicalMessage{Role: "tool", Content: content})
		case "user":
			// A user message directly following tool results (e.g. injected <turn-context>)
			// belongs to the same turn and is carried along with the last tool result.
			if len(req.ToolResults) > 0 {
				appendToLastToolResult(req.ToolResults, blocksAsToolResultText(blocks))
			}
			req.Messages = append(req.Messages, CanonicalMessage{Role: "user", Content: text})
			lastUserBlocks = blocks
		default:
			req.ToolResults = nil
			if len(m.ToolCalls) > 0 {
				var tcParts []string
				if text != "" {
					tcParts = append(tcParts, text)
				}
				for _, tc := range m.ToolCalls {
					tcParts = append(tcParts, tc.Function.Name+"("+normalizeToolArgs(string(tc.Function.Arguments))+")")
				}
				text = strings.Join(tcParts, "\n")
			}
			req.Messages = append(req.Messages, CanonicalMessage{Role: m.Role, Content: text})
		}
	}

	req.SystemPrompt = strings.Join(sysParts, "\n\n")
	if len(lastUserBlocks) == 0 && len(req.Messages) > 0 {
		lastText := req.Messages[len(req.Messages)-1].Content
		if lastText != "" {
			lastUserBlocks = []acp.PromptBlock{{Type: "text", Text: lastText}}
		}
	}
	req.PromptBlocks = lastUserBlocks
	if len(req.ToolResults) > 0 {
		req.PromptBlocks = toolResultsPromptBlocks(req.ToolResults)
	}
	req.SessionTitle = ExtractSessionTitleFromMessages(raw.Metadata, req.SystemPrompt, req.Messages)
	req.HistoryHashBefore, req.HistoryHashFull = ComputeHistoryHashes(req.SystemPrompt, req.Messages)
	return req, nil
}

func parseOpenAIContentBlocks(raw json.RawMessage) (string, []acp.PromptBlock) {
	if len(raw) == 0 {
		return "", nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if str == "" {
			return "", nil
		}
		return str, []acp.PromptBlock{{Type: "text", Text: str}}
	}

	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil
	}

	var textParts []string
	var blocks []acp.PromptBlock

	for _, p := range parts {
		typ, _ := p["type"].(string)
		switch typ {
		case "text":
			t, _ := p["text"].(string)
			if t != "" {
				textParts = append(textParts, t)
				blocks = append(blocks, acp.PromptBlock{Type: "text", Text: t})
			}
		case "image_url":
			if imgObj, ok := p["image_url"].(map[string]any); ok {
				urlStr, _ := imgObj["url"].(string)
				if mime, b64, ok := ParseDataURI(urlStr); ok {
					blocks = append(blocks, acp.PromptBlock{Type: "image", MimeType: mime, Data: b64})
				} else if urlStr != "" {
					blocks = append(blocks, acp.PromptBlock{Type: "resource_link", URI: urlStr, Name: "image"})
				}
			}
		case "input_audio":
			if audObj, ok := p["input_audio"].(map[string]any); ok {
				data, _ := audObj["data"].(string)
				format, _ := audObj["format"].(string)
				mime := "audio/wav"
				if format != "" {
					mime = "audio/" + format
				}
				if data != "" {
					blocks = append(blocks, acp.PromptBlock{Type: "audio", MimeType: mime, Data: data})
				}
			}
		case "file":
			if fileObj, ok := p["file"].(map[string]any); ok {
				fname, _ := fileObj["filename"].(string)
				fdata, _ := fileObj["file_data"].(string)
				fid, _ := fileObj["file_id"].(string)
				blocks = append(blocks, fileAttachmentBlock(fname, fdata, fid, ""))
			}
		}
	}
	return strings.Join(textParts, "\n"), blocks
}

type OpenAISSEWriter struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	reqID       string
	model       string
	created     int64
	textStarted bool
	finished    bool
	stopReason  string
	thinkingBuf strings.Builder
}

func NewOpenAISSEWriter(w http.ResponseWriter, reqID, model string) *OpenAISSEWriter {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fl, _ := w.(http.Flusher)
	return &OpenAISSEWriter{
		w:       w,
		flusher: fl,
		reqID:   reqID,
		model:   model,
		created: time.Now().Unix(),
	}
}

func (s *OpenAISSEWriter) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *OpenAISSEWriter) writeData(payload any) {
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", string(raw))
	s.flush()
}

func (s *OpenAISSEWriter) writeChunkWithUsage(delta map[string]any, finishReason any, usage map[string]any) {
	if s.finished {
		return
	}
	payload := map[string]any{
		"id":      s.reqID,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		payload["usage"] = usage
	}
	s.writeData(payload)
}

func (s *OpenAISSEWriter) writeChunk(delta map[string]any, finishReason any) {
	s.writeChunkWithUsage(delta, finishReason, nil)
}

// finishStream emits the standalone `choices: []` usage chunk (when usage is known) and [DONE].
func (s *OpenAISSEWriter) finishStream(usage map[string]any) {
	if s.finished {
		return
	}
	if usage != nil {
		s.writeData(map[string]any{
			"id":      s.reqID,
			"object":  "chat.completion.chunk",
			"created": s.created,
			"model":   s.model,
			"choices": []any{},
			"usage":   usage,
		})
	}
	_, _ = fmt.Fprintf(s.w, "data: [DONE]\n\n")
	s.flush()
	s.finished = true
}

func (s *OpenAISSEWriter) streamUsage(inputTokens, cachedTokens, outputTokens int64) map[string]any {
	if inputTokens <= 0 && outputTokens <= 0 {
		return nil
	}
	reasoningTokens := estimateAdapterTokens(s.thinkingBuf.String())
	if outputTokens < 0 {
		outputTokens = 0
	}
	return buildOpenAIUsageMap(inputTokens, cachedTokens, outputTokens+reasoningTokens, reasoningTokens)
}

// SetStopReason records the ACP stop reason used by subsequent non-tool finish writes.
func (s *OpenAISSEWriter) SetStopReason(acpStopReason string) {
	s.stopReason = acpStopReason
}

func (s *OpenAISSEWriter) WriteStart() {
	s.writeChunk(map[string]any{"role": "assistant", "content": ""}, nil)
}

func (s *OpenAISSEWriter) WriteKeepAlive() {
	if s.finished {
		return
	}
	_, _ = fmt.Fprintf(s.w, ": ping\n\n")
	s.flush()
}

func (s *OpenAISSEWriter) WriteThinkingDelta(thinking string) {
	if thinking == "" || s.textStarted || s.finished {
		return
	}
	s.thinkingBuf.WriteString(thinking)
	s.writeChunk(map[string]any{
		"reasoning_content": thinking,
	}, nil)
}

func (s *OpenAISSEWriter) WriteTextDelta(text string) {
	if text == "" || s.finished {
		return
	}
	s.textStarted = true
	s.writeChunk(map[string]any{"content": text}, nil)
}

func buildOpenAIUsageMap(inputTokens, cachedTokens, outputTokens, reasoningTokens int64) map[string]any {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > inputTokens {
		cachedTokens = inputTokens
	}
	if outputTokens < 0 {
		outputTokens = 0
	}
	if reasoningTokens < 0 {
		reasoningTokens = 0
	}
	if reasoningTokens > outputTokens {
		reasoningTokens = outputTokens
	}
	return map[string]any{
		"prompt_tokens":     inputTokens,
		"completion_tokens": outputTokens,
		"total_tokens":      inputTokens + outputTokens,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": cachedTokens,
		},
		"completion_tokens_details": map[string]any{
			"reasoning_tokens": reasoningTokens,
		},
	}
}

func (s *OpenAISSEWriter) WriteToolCallsAndFinishWithUsage(calls []CanonicalToolCall, inputTokens, cachedTokens, outputTokens int64) {
	if s.finished {
		return
	}
	valid := validToolCalls(calls)
	if len(valid) == 0 {
		s.WriteStopWithUsage(inputTokens, cachedTokens, outputTokens)
		return
	}
	tcList := make([]map[string]any, 0, len(valid))
	for i, c := range valid {
		tcList = append(tcList, map[string]any{
			"index": i,
			"id":    c.CallID,
			"type":  "function",
			"function": map[string]any{
				"name":      c.ToolName,
				"arguments": c.ArgsJSON,
			},
		})
	}
	s.writeChunk(map[string]any{
		"tool_calls": tcList,
	}, nil)
	usage := s.streamUsage(inputTokens, cachedTokens, outputTokens)
	s.writeChunkWithUsage(map[string]any{}, "tool_calls", usage)
	s.finishStream(usage)
}

func (s *OpenAISSEWriter) WriteToolCallsAndFinish(calls []CanonicalToolCall) {
	s.WriteToolCallsAndFinishWithUsage(calls, 0, 0, 0)
}

func (s *OpenAISSEWriter) WriteToolCallAndFinish(callID, toolName, argsJSON string) {
	s.WriteToolCallsAndFinish([]CanonicalToolCall{
		{CallID: callID, ToolName: toolName, ArgsJSON: argsJSON},
	})
}

func (s *OpenAISSEWriter) WriteStopWithUsage(inputTokens, cachedTokens, outputTokens int64) {
	if s.finished {
		return
	}
	usage := s.streamUsage(inputTokens, cachedTokens, outputTokens)
	s.writeChunkWithUsage(map[string]any{}, chatFinishReasonForStop(s.stopReason), usage)
	s.finishStream(usage)
}

func (s *OpenAISSEWriter) WriteStop() {
	s.WriteStopWithUsage(0, 0, 0)
}

// WriteError terminates the stream with an error chunk followed by [DONE]. It is a no-op
// once the stream has already been finished.
func (s *OpenAISSEWriter) WriteError(message string) {
	if s.finished {
		return
	}
	s.writeData(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
		},
	})
	s.finishStream(nil)
}

func estimateAdapterTokens(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runes := int64(utf8.RuneCountInString(trimmed))
	est := (runes + 2) / 3
	if est < 1 {
		est = 1
	}
	return est
}

func BuildOpenAIJSONResponse(reqID, model, thinking, text, toolCallID, toolName, toolArgs string) map[string]any {
	var calls []CanonicalToolCall
	if toolCallID != "" && toolName != "" {
		calls = []CanonicalToolCall{{CallID: toolCallID, ToolName: toolName, ArgsJSON: toolArgs}}
	}
	return BuildOpenAIJSONResponseMulti(reqID, model, thinking, text, calls)
}

// BuildOpenAIJSONResponseMulti builds a non-stream chat.completion. usageOverride is
// (promptTokens, cachedTokens, completionTokens); an overridden completionTokens excludes
// reasoning, which is added on top so completion_tokens always covers reasoning_tokens.
func BuildOpenAIJSONResponseMulti(reqID, model, thinking, text string, calls []CanonicalToolCall, usageOverride ...int64) map[string]any {
	valid := validToolCalls(calls)
	msg := map[string]any{
		"role":    "assistant",
		"content": text,
	}
	reasoningTokens := estimateAdapterTokens(thinking)
	textTokens := estimateAdapterTokens(text)
	compTokens := reasoningTokens + textTokens
	if thinking != "" {
		msg["reasoning_content"] = thinking
	}
	finishReason := "stop"
	if len(valid) > 0 {
		tcList := make([]map[string]any, 0, len(valid))
		for _, c := range valid {
			compTokens += estimateAdapterTokens(c.ArgsJSON) + 6
			tcList = append(tcList, map[string]any{
				"id":   c.CallID,
				"type": "function",
				"function": map[string]any{
					"name":      c.ToolName,
					"arguments": c.ArgsJSON,
				},
			})
		}
		finishReason = "tool_calls"
		msg["tool_calls"] = tcList
		if text == "" {
			msg["content"] = nil
		}
	}
	var promptTokens, cachedTokens int64
	if len(usageOverride) >= 1 && usageOverride[0] > 0 {
		promptTokens = usageOverride[0]
	}
	if len(usageOverride) >= 2 && usageOverride[1] > 0 {
		cachedTokens = usageOverride[1]
	}
	if len(usageOverride) >= 3 && usageOverride[2] > 0 {
		compTokens = usageOverride[2] + reasoningTokens
	}
	return map[string]any{
		"id":      reqID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": finishReason,
			},
		},
		"usage": buildOpenAIUsageMap(promptTokens, cachedTokens, compTokens, reasoningTokens),
	}
}

// ApplyOpenAIStopReason maps an ACP stop reason onto a response built by
// BuildOpenAIJSONResponseMulti. A `tool_calls` finish reason is left untouched.
func ApplyOpenAIStopReason(resp map[string]any, acpStopReason string) map[string]any {
	if resp == nil {
		return resp
	}
	finish := chatFinishReasonForStop(acpStopReason)
	apply := func(choice map[string]any) {
		if choice == nil || choice["finish_reason"] == "tool_calls" {
			return
		}
		choice["finish_reason"] = finish
	}
	switch choices := resp["choices"].(type) {
	case []map[string]any:
		for _, c := range choices {
			apply(c)
		}
	case []any:
		for _, c := range choices {
			if m, ok := c.(map[string]any); ok {
				apply(m)
			}
		}
	}
	return resp
}
