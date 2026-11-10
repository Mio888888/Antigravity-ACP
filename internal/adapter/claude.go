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
		Effort    string `json:"effort"`
		MaxTokens int    `json:"max_tokens"`
	} `json:"reasoning"`
	Thinking *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
		Effort       string `json:"effort"`
	} `json:"thinking"`
	OutputConfig *struct {
		Effort string `json:"effort"`
	} `json:"output_config"`
	ContextWindow      int64              `json:"context_window"`
	ModelContextWindow int64              `json:"model_context_window"`
	MaxInputTokens     int64              `json:"max_input_tokens"`
	MaxContextTokens   int64              `json:"max_context_tokens"`
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
			budget = raw.Reasoning.MaxTokens
		}
	}
	if raw.Thinking != nil {
		if effort == "" && strings.TrimSpace(raw.Thinking.Effort) != "" {
			effort = strings.TrimSpace(raw.Thinking.Effort)
		}
		if budget == 0 && raw.Thinking.BudgetTokens > 0 {
			budget = raw.Thinking.BudgetTokens
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
		ContextWindow:   ExtractContextWindowFromMetadata(raw.Metadata, raw.ContextWindow, raw.ModelContextWindow, raw.MaxContextTokens, raw.MaxInputTokens),
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
	lastHadToolResults := false
	for _, m := range raw.Messages {
		text, blocks, toolResults := parseClaudeContentBlocks(m.Content)
		if len(toolResults) > 0 {
			req.ToolResults = append(req.ToolResults, toolResults...)
			lastHadToolResults = true
		} else if m.Role == "user" && lastHadToolResults && len(req.ToolResults) > 0 {
			if text != "" {
				lastIdx := len(req.ToolResults) - 1
				if req.ToolResults[lastIdx].Content != "" {
					req.ToolResults[lastIdx].Content += "\n\n" + text
				} else {
					req.ToolResults[lastIdx].Content = text
				}
			}
		} else {
			req.ToolResults = nil
			lastHadToolResults = false
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
			inRaw, _ := json.Marshal(item["input"])
			if tName != "" {
				texts = append(texts, tName+"("+string(inRaw)+")")
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
			title, _ := item["title"].(string)
			if title == "" {
				title = "document.txt"
			}
			if src, ok := item["source"].(map[string]any); ok {
				srcType, _ := src["type"].(string)
				mediaType, _ := src["media_type"].(string)
				data, _ := src["data"].(string)
				res := &acp.EmbeddedResource{
					URI:      "file:///" + title,
					MimeType: mediaType,
				}
				if srcType == "base64" {
					res.Blob = data
				} else {
					res.Text = data
				}
				blocks = append(blocks, acp.PromptBlock{
					Type:     "resource",
					Resource: res,
				})
			}
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
	return strings.Join(texts, "\n"), blocks, results
}

func extractClaudeToolResultContent(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []any:
		var parts []string
		for _, elem := range val {
			if m, ok := elem.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		raw, _ := json.Marshal(v)
		return string(raw)
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
	textStarted  bool
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

func (c *ClaudeSSEWriter) WriteStart() {
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
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	})
}

func (c *ClaudeSSEWriter) WriteKeepAlive() {
	c.writeEvent("ping", map[string]any{"type": "ping"})
}

func (c *ClaudeSSEWriter) ensureBlockType(blockType string) {
	if c.hasOpenBlock && c.currentBlock == blockType {
		return
	}
	if c.hasOpenBlock {
		c.writeEvent("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": c.blockIdx,
		})
		c.blockIdx++
		c.hasOpenBlock = false
	}

	var contentBlock map[string]any
	if blockType == "thinking" {
		contentBlock = map[string]any{"type": "thinking", "thinking": ""}
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
}

func (c *ClaudeSSEWriter) WriteThinkingDelta(thinking string) {
	if thinking == "" || c.textStarted {
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
	if text == "" {
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

func (c *ClaudeSSEWriter) WriteToolUsesAndStopWithUsage(calls []CanonicalToolCall, inputTokens, cachedTokens, outputTokens int64) {
	if c.hasOpenBlock {
		c.writeEvent("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": c.blockIdx,
		})
		c.blockIdx++
		c.hasOpenBlock = false
	}

	for _, call := range calls {
		if call.CallID == "" || call.ToolName == "" {
			continue
		}
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
	}
	c.writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "tool_use",
			"stop_sequence": nil,
		},
		"usage": buildClaudeUsageMap(inputTokens, cachedTokens, outputTokens),
	})
	c.writeEvent("message_stop", map[string]any{"type": "message_stop"})
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
	if !c.hasOpenBlock {
		c.ensureBlockType("text")
	}
	c.writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": c.blockIdx,
	})
	c.hasOpenBlock = false
	c.writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": buildClaudeUsageMap(inputTokens, cachedTokens, outputTokens),
	})
	c.writeEvent("message_stop", map[string]any{"type": "message_stop"})
}

func (c *ClaudeSSEWriter) WriteStop() {
	c.WriteStopWithUsage(0, 0, 1)
}

func BuildClaudeJSONResponse(msgID, model, thinking, text, toolCallID, toolName, toolArgs string) map[string]any {
	var calls []CanonicalToolCall
	if toolCallID != "" && toolName != "" {
		calls = []CanonicalToolCall{{CallID: toolCallID, ToolName: toolName, ArgsJSON: toolArgs}}
	}
	return BuildClaudeJSONResponseMulti(msgID, model, thinking, text, calls)
}

func BuildClaudeJSONResponseMulti(msgID, model, thinking, text string, calls []CanonicalToolCall, usageOverride ...int64) map[string]any {
	var content []map[string]any
	if thinking != "" {
		content = append(content, map[string]any{
			"type":     "thinking",
			"thinking": thinking,
		})
	}
	if text != "" || (len(calls) == 0 && len(content) == 0) {
		content = append(content, map[string]any{
			"type": "text",
			"text": text,
		})
	}
	stopReason := "end_turn"
	for _, call := range calls {
		if call.CallID != "" && call.ToolName != "" {
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
