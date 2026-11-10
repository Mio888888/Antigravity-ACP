package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"antigravity-gateway/internal/acp"
)

type claudeRawRequest struct {
	Model           string          `json:"model"`
	Stream          bool            `json:"stream"`
	System          json.RawMessage `json:"system"`
	ReasoningEffort string          `json:"reasoning_effort"`
	Effort          string          `json:"effort"`
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
	Metadata           map[string]any     `json:"metadata"`
	Messages           []claudeRawMessage `json:"messages"`
	Tools              []claudeRawTool    `json:"tools"`
}

type claudeRawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeRawTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func ParseClaudeRequest(body []byte, sessionHeader string) (*CanonicalRequest, error) {
	var raw claudeRawRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode claude request: %w", err)
	}
	if raw.Model == "" {
		raw.Model = "claude-sonnet-4-5"
	}

	effort := strings.TrimSpace(raw.ReasoningEffort)
	if effort == "" {
		effort = strings.TrimSpace(raw.Effort)
	}
	if effort == "" && raw.OutputConfig != nil && strings.TrimSpace(raw.OutputConfig.Effort) != "" {
		effort = strings.TrimSpace(raw.OutputConfig.Effort)
	}
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

	normalizedThinking := NormalizeThinkingLevel(effort, budget)
	cleanModel, finalThinking := SplitModelAndThinkingSuffix(raw.Model, normalizedThinking)

	clientSessID := ExtractClientSessionID(sessionHeader, raw.Metadata, raw.SessionID, raw.ConversationID)
	effSession := strings.TrimSpace(sessionHeader)
	if effSession == "" {
		effSession = clientSessID
	}

	req := &CanonicalRequest{
		Protocol:        "claude",
		Model:           cleanModel,
		Stream:          raw.Stream,
		ThinkingLevel:   finalThinking,
		ContextWindow:   ExtractContextWindowFromMetadata(raw.Metadata, int64(raw.ContextWindow), int64(raw.ModelContextWindow), int64(raw.MaxContextTokens), int64(raw.MaxInputTokens)),
		SessionHeader:   effSession,
		ClientSessionID: clientSessID,
		SystemPrompt:    parseClaudeSystemField(raw.System),
	}

	for _, t := range raw.Tools {
		if t.Name != "" {
			req.Tools = append(req.Tools, ToMCPToolDefinition(t.Name, t.Description, t.InputSchema))
		}
	}

	var lastUserBlocks []acp.PromptBlock
	for _, m := range raw.Messages {
		text, blocks, toolResults := parseClaudeContentBlocks(m.Content)
		switch {
		case len(toolResults) > 0:
			req.ToolResults = append(req.ToolResults, toolResults...)
		case m.Role == "user" && len(req.ToolResults) > 0:
			// A user message directly following tool results (e.g. injected <turn-context>)
			// belongs to the same turn and is carried along with the last tool result.
			appendToLastToolResult(req.ToolResults, blocksAsToolResultText(blocks))
		default:
			req.ToolResults = nil
		}
		req.Messages = append(req.Messages, CanonicalMessage{
			Role:    m.Role,
			Content: text,
		})
		if m.Role == "user" && len(blocks) > 0 {
			lastUserBlocks = blocks
		}
	}

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

func parseClaudeSystemField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, item := range arr {
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, strings.TrimSpace(item.Text))
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// parseClaudeContentBlocks returns the history text, the prompt blocks and the tool results
// of a message. Text, image and document blocks that sit next to tool_result blocks are
// appended to the last tool result so they survive the tool-resume path.
func parseClaudeContentBlocks(raw json.RawMessage) (string, []acp.PromptBlock, []CanonicalToolResult) {
	if len(raw) == 0 {
		return "", nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return "", nil, nil
		}
		return s, []acp.PromptBlock{{Type: "text", Text: s}}, nil
	}

	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return "", nil, nil
	}

	var texts []string
	var blocks []acp.PromptBlock
	var results []CanonicalToolResult

	for _, item := range arr {
		typ, _ := item["type"].(string)
		switch typ {
		case "text":
			t, _ := item["text"].(string)
			if t != "" {
				texts = append(texts, t)
				blocks = append(blocks, acp.PromptBlock{Type: "text", Text: t})
			}
		case "tool_use":
			tName, _ := item["name"].(string)
			if tName != "" {
				texts = append(texts, tName+"("+normalizeToolArgs(argsToJSONString(item["input"]))+")")
			}
		case "image":
			if src, ok := item["source"].(map[string]any); ok {
				srcType, _ := src["type"].(string)
				mediaType, _ := src["media_type"].(string)
				data, _ := src["data"].(string)
				urlStr, _ := src["url"].(string)
				if srcType == "base64" && data != "" {
					if mediaType == "" {
						mediaType = "image/png"
					}
					blocks = append(blocks, acp.PromptBlock{
						Type:     "image",
						MimeType: mediaType,
						Data:     data,
					})
				} else if urlStr != "" {
					blocks = append(blocks, acp.PromptBlock{
						Type: "resource_link",
						URI:  urlStr,
						Name: "image",
					})
				}
			}
		case "document":
			blocks = append(blocks, claudeDocumentBlock(item))
		case "tool_result":
			callID, _ := item["tool_use_id"].(string)
			isErr, _ := item["is_error"].(bool)
			contentStr := extractClaudeToolResultContent(item["content"])
			results = append(results, CanonicalToolResult{
				CallID:  strings.TrimSpace(callID),
				Content: contentStr,
				IsError: isErr,
			})
			texts = append(texts, contentStr)
		}
	}
	if len(results) > 0 {
		appendToLastToolResult(results, blocksAsToolResultText(blocks))
	}
	return strings.Join(texts, "\n"), blocks, results
}

func claudeDocumentBlock(item map[string]any) acp.PromptBlock {
	title, _ := item["title"].(string)
	name := strings.TrimSpace(title)
	if name == "" {
		name = "document"
	}
	placeholder := func(label string) acp.PromptBlock {
		return acp.PromptBlock{Type: "text", Text: fmt.Sprintf("[document attachment: %s not available]", label)}
	}
	src, ok := item["source"].(map[string]any)
	if !ok {
		return placeholder(name)
	}
	srcType, _ := src["type"].(string)
	mediaType, _ := src["media_type"].(string)
	data, _ := src["data"].(string)
	switch srcType {
	case "base64":
		if data == "" {
			return placeholder(name)
		}
		return acp.PromptBlock{
			Type:     "resource",
			Resource: &acp.EmbeddedResource{URI: fileURI(name), MimeType: mediaType, Blob: data},
		}
	case "text":
		if data == "" {
			return placeholder(name)
		}
		if mediaType == "" {
			mediaType = "text/plain"
		}
		return acp.PromptBlock{
			Type:     "resource",
			Resource: &acp.EmbeddedResource{URI: fileURI(name), MimeType: mediaType, Text: data},
		}
	case "content":
		text := extractClaudeToolResultContent(src["content"])
		if strings.TrimSpace(text) == "" {
			return placeholder(name)
		}
		return acp.PromptBlock{
			Type:     "resource",
			Resource: &acp.EmbeddedResource{URI: fileURI(name), MimeType: "text/plain", Text: text},
		}
	case "url":
		urlStr, _ := src["url"].(string)
		if strings.TrimSpace(urlStr) == "" {
			return placeholder(name)
		}
		return acp.PromptBlock{Type: "resource_link", URI: strings.TrimSpace(urlStr), Name: name, MimeType: mediaType}
	case "file":
		if fid, _ := src["file_id"].(string); strings.TrimSpace(fid) != "" {
			return placeholder(strings.TrimSpace(fid))
		}
	}
	return placeholder(name)
}

func extractClaudeToolResultContent(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case []any:
		var parts []string
		for _, elem := range val {
			switch e := elem.(type) {
			case string:
				parts = append(parts, e)
			case map[string]any:
				typ, _ := e["type"].(string)
				switch typ {
				case "image":
					mediaType := "image"
					if src, ok := e["source"].(map[string]any); ok {
						if mt, _ := src["media_type"].(string); mt != "" {
							mediaType = mt
						}
					}
					parts = append(parts, fmt.Sprintf("[image omitted: %s]", mediaType))
				case "document":
					if b := claudeDocumentBlock(e); b.Type == "text" {
						parts = append(parts, b.Text)
					} else if d := describeNonTextBlock(b); d != "" {
						parts = append(parts, d)
					}
				default:
					if t, ok := e["text"].(string); ok {
						parts = append(parts, t)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if t, ok := val["text"].(string); ok {
			return t
		}
		raw, _ := json.Marshal(val)
		return string(raw)
	default:
		raw, _ := json.Marshal(v)
		return string(raw)
	}
}

func claudeStopReasonForStop(acpStopReason string) string {
	switch normalizeACPStopReason(acpStopReason) {
	case "max_tokens", "max_turn_requests":
		return "max_tokens"
	case "refusal":
		return "refusal"
	default:
		return "end_turn"
	}
}

type ClaudeSSEWriter struct {
	w            http.ResponseWriter
	flusher      http.Flusher
	msgID        string
	model        string
	currentBlock string // "" | "thinking" | "text"
	blockIdx     int
	hasOpenBlock bool
	anyBlock     bool
	textStarted  bool
	finished     bool
	stopReason   string
	inputTokens  int64
}

func NewClaudeSSEWriter(w http.ResponseWriter, msgID, model string) *ClaudeSSEWriter {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fl, _ := w.(http.Flusher)
	return &ClaudeSSEWriter{
		w:       w,
		flusher: fl,
		msgID:   msgID,
		model:   model,
	}
}

func (c *ClaudeSSEWriter) writeEvent(event string, data any) {
	raw, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(c.w, "event: %s\ndata: %s\n\n", event, string(raw))
	if c.flusher != nil {
		c.flusher.Flush()
	}
}

// SetInputTokens sets the input token count reported in message_start; it only takes
// effect when called before WriteStart.
func (c *ClaudeSSEWriter) SetInputTokens(n int64) {
	if n < 0 {
		n = 0
	}
	c.inputTokens = n
}

// SetStopReason records the ACP stop reason used by subsequent non-tool finish writes.
func (c *ClaudeSSEWriter) SetStopReason(acpStopReason string) {
	c.stopReason = acpStopReason
}

func (c *ClaudeSSEWriter) WriteStart() {
	if c.finished {
		return
	}
	c.writeEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            c.msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         c.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  c.inputTokens,
				"output_tokens": 0,
			},
		},
	})
}

func (c *ClaudeSSEWriter) WriteKeepAlive() {
	if c.finished {
		return
	}
	c.writeEvent("ping", map[string]any{"type": "ping"})
}

func (c *ClaudeSSEWriter) closeOpenBlock() {
	if !c.hasOpenBlock {
		return
	}
	if c.currentBlock == "thinking" {
		c.writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": c.blockIdx,
			"delta": map[string]any{
				"type":      "signature_delta",
				"signature": "",
			},
		})
	}
	c.writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": c.blockIdx,
	})
	c.blockIdx++
	c.hasOpenBlock = false
	c.currentBlock = ""
}

func (c *ClaudeSSEWriter) ensureBlockType(blockType string) {
	if c.hasOpenBlock && c.currentBlock == blockType {
		return
	}
	c.closeOpenBlock()

	var contentBlock map[string]any
	if blockType == "thinking" {
		contentBlock = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	} else {
		contentBlock = map[string]any{"type": "text", "text": ""}
	}
	c.writeEvent("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         c.blockIdx,
		"content_block": contentBlock,
	})
	c.currentBlock = blockType
	c.hasOpenBlock = true
	c.anyBlock = true
}

func (c *ClaudeSSEWriter) WriteThinkingDelta(thinking string) {
	if thinking == "" || c.textStarted || c.finished {
		return
	}
	c.ensureBlockType("thinking")
	c.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": c.blockIdx,
		"delta": map[string]any{
			"type":     "thinking_delta",
			"thinking": thinking,
		},
	})
}

func (c *ClaudeSSEWriter) WriteTextDelta(text string) {
	if text == "" || c.finished {
		return
	}
	c.textStarted = true
	c.ensureBlockType("text")
	c.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": c.blockIdx,
		"delta": map[string]any{
			"type": "text_delta",
			"text": text,
		},
	})
}

func buildClaudeUsageMap(inputTokens, cachedTokens, outputTokens int64) map[string]any {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > inputTokens {
		cachedTokens = inputTokens
	}
	if outputTokens < 1 {
		outputTokens = 1
	}
	uncached := inputTokens - cachedTokens
	return map[string]any{
		"input_tokens":            uncached,
		"cache_read_input_tokens": cachedTokens,
		"output_tokens":           outputTokens,
	}
}

func (c *ClaudeSSEWriter) writeMessageEnd(stopReason string, inputTokens, cachedTokens, outputTokens int64) {
	c.writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": buildClaudeUsageMap(inputTokens, cachedTokens, outputTokens),
	})
	c.writeEvent("message_stop", map[string]any{"type": "message_stop"})
	c.finished = true
}

func (c *ClaudeSSEWriter) WriteToolUsesAndStopWithUsage(calls []CanonicalToolCall, inputTokens, cachedTokens, outputTokens int64) {
	if c.finished {
		return
	}
	valid := validToolCalls(calls)
	if len(valid) == 0 {
		c.WriteStopWithUsage(inputTokens, cachedTokens, outputTokens)
		return
	}
	c.closeOpenBlock()

	for _, call := range valid {
		c.writeEvent("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": c.blockIdx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    call.CallID,
				"name":  call.ToolName,
				"input": map[string]any{},
			},
		})
		c.writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": c.blockIdx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": call.ArgsJSON,
			},
		})
		c.writeEvent("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": c.blockIdx,
		})
		c.blockIdx++
		c.anyBlock = true
	}
	c.writeMessageEnd("tool_use", inputTokens, cachedTokens, outputTokens)
}

func (c *ClaudeSSEWriter) WriteToolUsesAndStop(calls []CanonicalToolCall) {
	c.WriteToolUsesAndStopWithUsage(calls, 0, 0, 1)
}

func (c *ClaudeSSEWriter) WriteToolUseAndStop(callID, toolName, argsJSON string) {
	c.WriteToolUsesAndStop([]CanonicalToolCall{
		{CallID: callID, ToolName: toolName, ArgsJSON: argsJSON},
	})
}

func (c *ClaudeSSEWriter) WriteStopWithUsage(inputTokens, cachedTokens, outputTokens int64) {
	if c.finished {
		return
	}
	if !c.anyBlock {
		c.ensureBlockType("text")
	}
	c.closeOpenBlock()
	c.writeMessageEnd(claudeStopReasonForStop(c.stopReason), inputTokens, cachedTokens, outputTokens)
}

func (c *ClaudeSSEWriter) WriteStop() {
	c.WriteStopWithUsage(0, 0, 1)
}

// WriteError closes any open content block and terminates the stream with an `error`
// event. It is a no-op once the stream has already been finished.
func (c *ClaudeSSEWriter) WriteError(message string) {
	if c.finished {
		return
	}
	c.closeOpenBlock()
	c.writeEvent("error", map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "api_error",
			"message": message,
		},
	})
	c.finished = true
}

func BuildClaudeJSONResponse(msgID, model, thinking, text, toolCallID, toolName, toolArgs string) map[string]any {
	var calls []CanonicalToolCall
	if toolCallID != "" && toolName != "" {
		calls = []CanonicalToolCall{{CallID: toolCallID, ToolName: toolName, ArgsJSON: toolArgs}}
	}
	return BuildClaudeJSONResponseMulti(msgID, model, thinking, text, calls)
}

func BuildClaudeJSONResponseMulti(msgID, model, thinking, text string, calls []CanonicalToolCall, usageOverride ...int64) map[string]any {
	valid := validToolCalls(calls)
	content := []map[string]any{}
	if thinking != "" {
		content = append(content, map[string]any{
			"type":      "thinking",
			"thinking":  thinking,
			"signature": "",
		})
	}
	if text != "" || (len(valid) == 0 && len(content) == 0) {
		content = append(content, map[string]any{
			"type": "text",
			"text": text,
		})
	}
	stopReason := "end_turn"
	for _, call := range valid {
		stopReason = "tool_use"
		var inputObj map[string]any
		_ = json.Unmarshal([]byte(call.ArgsJSON), &inputObj)
		if inputObj == nil {
			inputObj = map[string]any{}
		}
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    call.CallID,
			"name":  call.ToolName,
			"input": inputObj,
		})
	}
	var inputTok, cachedTok, outputTok int64
	if len(usageOverride) >= 1 && usageOverride[0] > 0 {
		inputTok = usageOverride[0]
	}
	if len(usageOverride) >= 2 && usageOverride[1] > 0 {
		cachedTok = usageOverride[1]
	}
	if len(usageOverride) >= 3 && usageOverride[2] > 0 {
		outputTok = usageOverride[2]
	}
	return map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         buildClaudeUsageMap(inputTok, cachedTok, outputTok),
	}
}

// ApplyClaudeStopReason maps an ACP stop reason onto a response built by
// BuildClaudeJSONResponseMulti. A `tool_use` stop reason is left untouched.
func ApplyClaudeStopReason(resp map[string]any, acpStopReason string) map[string]any {
	if resp == nil || resp["stop_reason"] == "tool_use" {
		return resp
	}
	resp["stop_reason"] = claudeStopReasonForStop(acpStopReason)
	return resp
}
