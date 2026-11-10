package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"antigravity-gateway/internal/acp"
)

type openAIRawRequest struct {
	Model           string `json:"model"`
	Stream          bool   `json:"stream"`
	ReasoningEffort string `json:"reasoning_effort"`
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
	PromptCacheKey     string             `json:"prompt_cache_key"`
	User               string             `json:"user"`
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
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
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
	if effort == "" && raw.OutputConfig != nil && strings.TrimSpace(raw.OutputConfig.Effort) != "" {
		effort = strings.TrimSpace(raw.OutputConfig.Effort)
	}

	normalizedThinking := NormalizeThinkingLevel(effort, budget)
	cleanModel, finalThinking := SplitModelAndThinkingSuffix(raw.Model, normalizedThinking)

	clientSessID := ExtractClientSessionID(sessionHeader, raw.Metadata, raw.SessionID, raw.ConversationID, raw.PromptCacheKey, raw.User)
	effSession := strings.TrimSpace(sessionHeader)
	if effSession == "" {
		effSession = clientSessID
	}

	req := &CanonicalRequest{
		Protocol:        "openai",
		Model:           cleanModel,
		Stream:          raw.Stream,
		ThinkingLevel:   finalThinking,
		ContextWindow:   ExtractContextWindowFromMetadata(raw.Metadata, raw.ContextWindow, raw.ModelContextWindow, raw.MaxContextTokens, raw.MaxInputTokens),
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
	lastRoleWasTool := false

	for _, m := range raw.Messages {
		text, blocks := parseOpenAIContentBlocks(m.Content)
		switch m.Role {
		case "system", "developer":
			if strings.TrimSpace(text) != "" {
				sysParts = append(sysParts, strings.TrimSpace(text))
			}
		case "tool":
			req.ToolResults = append(req.ToolResults, CanonicalToolResult{
				CallID:  strings.TrimSpace(m.ToolCallID),
				Content: text,
			})
			lastRoleWasTool = true
			req.Messages = append(req.Messages, CanonicalMessage{Role: "tool", Content: text})
		case "user":
			if lastRoleWasTool && len(req.ToolResults) > 0 {
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
				lastRoleWasTool = false
			}
			req.Messages = append(req.Messages, CanonicalMessage{Role: "user", Content: text})
			lastUserBlocks = blocks
		default:
			req.ToolResults = nil
			lastRoleWasTool = false
			if len(m.ToolCalls) > 0 {
				var tcParts []string
				if text != "" {
					tcParts = append(tcParts, text)
				}
				for _, tc := range m.ToolCalls {
					tcParts = append(tcParts, tc.Function.Name+"("+tc.Function.Arguments+")")
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
				if fname == "" {
					fname = "attachment"
				}
				blocks = append(blocks, acp.PromptBlock{
					Type: "resource",
					Resource: &acp.EmbeddedResource{
						URI:  "file:///" + fname,
						Blob: fdata,
					},
				})
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

func (s *OpenAISSEWriter) writeChunkWithUsage(delta map[string]any, finishReason any, usage map[string]any) {
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
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", string(raw))
	s.flush()
}

func (s *OpenAISSEWriter) writeChunk(delta map[string]any, finishReason any) {
	s.writeChunkWithUsage(delta, finishReason, nil)
}

func (s *OpenAISSEWriter) WriteStart() {
	s.writeChunk(map[string]any{"role": "assistant", "content": ""}, nil)
}

func (s *OpenAISSEWriter) WriteKeepAlive() {
	_, _ = fmt.Fprintf(s.w, ": ping\n\n")
	s.flush()
}

func (s *OpenAISSEWriter) WriteThinkingDelta(thinking string) {
	if thinking == "" || s.textStarted {
		return
	}
	s.writeChunk(map[string]any{
		"reasoning_content": thinking,
		"reasoning":         thinking,
	}, nil)
}

func (s *OpenAISSEWriter) WriteTextDelta(text string) {
	if text == "" {
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
	var tcList []map[string]any
	for i, c := range calls {
		if c.CallID == "" || c.ToolName == "" {
			continue
		}
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
	if len(tcList) > 0 {
		s.writeChunk(map[string]any{
			"tool_calls": tcList,
		}, nil)
	}
	var usage map[string]any
	if inputTokens > 0 || outputTokens > 0 {
		usage = buildOpenAIUsageMap(inputTokens, cachedTokens, outputTokens, 0)
	}
	s.writeChunkWithUsage(map[string]any{}, "tool_calls", usage)
	_, _ = fmt.Fprintf(s.w, "data: [DONE]\n\n")
	s.flush()
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
	var usage map[string]any
	if inputTokens > 0 || outputTokens > 0 {
		usage = buildOpenAIUsageMap(inputTokens, cachedTokens, outputTokens, 0)
	}
	s.writeChunkWithUsage(map[string]any{}, "stop", usage)
	_, _ = fmt.Fprintf(s.w, "data: [DONE]\n\n")
	s.flush()
}

func (s *OpenAISSEWriter) WriteStop() {
	s.WriteStopWithUsage(0, 0, 0)
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

func BuildOpenAIJSONResponseMulti(reqID, model, thinking, text string, calls []CanonicalToolCall, usageOverride ...int64) map[string]any {
	msg := map[string]any{
		"role":    "assistant",
		"content": text,
	}
	reasoningTokens := estimateAdapterTokens(thinking)
	textTokens := estimateAdapterTokens(text)
	compTokens := reasoningTokens + textTokens
	if thinking != "" {
		msg["reasoning_content"] = thinking
		msg["reasoning"] = thinking
	}
	finishReason := "stop"
	var tcList []map[string]any
	for _, c := range calls {
		if c.CallID != "" && c.ToolName != "" {
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
	}
	if len(tcList) > 0 {
		finishReason = "tool_calls"
		msg["tool_calls"] = tcList
	}
	var promptTokens, cachedTokens int64
	if len(usageOverride) >= 1 && usageOverride[0] > 0 {
		promptTokens = usageOverride[0]
	}
	if len(usageOverride) >= 2 && usageOverride[1] > 0 {
		cachedTokens = usageOverride[1]
	}
	if len(usageOverride) >= 3 && usageOverride[2] > 0 {
		compTokens = usageOverride[2]
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
