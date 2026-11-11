package adapter

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"antigravity-gateway/internal/acp"
)

type openAIResponsesRawRequest struct {
	Model              string `json:"model"`
	Stream             bool   `json:"stream"`
	Instructions       string `json:"instructions"`
	PreviousResponseID string `json:"previous_response_id"`
	ReasoningEffort    string `json:"reasoning_effort"`
	Reasoning          *struct {
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
	ContextWindow      flexInt64                `json:"context_window"`
	ContextSize        flexInt64                `json:"context_size"`
	ModelContextWindow flexInt64                `json:"model_context_window"`
	MaxInputTokens     flexInt64                `json:"max_input_tokens"`
	MaxContextTokens   flexInt64                `json:"max_context_tokens"`
	SessionID          string                   `json:"session_id"`
	ConversationID     string                   `json:"conversation_id"`
	PromptCacheKey     string                   `json:"prompt_cache_key"`
	Metadata           map[string]any           `json:"metadata"`
	Input              json.RawMessage          `json:"input"`
	Tools              []openAIResponsesRawTool `json:"tools"`
}

type openAIResponsesRawTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Function    *struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function,omitempty"`
}

// isResponsesToolOutputItem matches function_call_output, custom_tool_call_output and the
// other `*_call_output` input items that carry a client tool result.
func isResponsesToolOutputItem(itemType string) bool {
	return itemType == "function_call_output" || strings.HasSuffix(itemType, "_call_output")
}

func isResponsesToolCallItem(itemType string) bool {
	return itemType == "function_call" || strings.HasSuffix(itemType, "_call")
}

func ParseOpenAIResponsesRequest(body []byte, sessionHeader string) (*CanonicalRequest, error) {
	var raw openAIResponsesRawRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode openai responses request: %w", err)
	}
	if raw.Model == "" {
		raw.Model = "gemini-4-argon"
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

	// Session key precedence: explicit header > top-level session/conversation ids >
	// previous_response_id > ids derived from the free-form metadata map.
	clientSessID := ExtractClientSessionID(sessionHeader, raw.Metadata, raw.SessionID, raw.ConversationID, raw.PromptCacheKey)
	effSession := strings.TrimSpace(sessionHeader)
	if effSession == "" {
		effSession = ExtractClientSessionID("", nil, raw.SessionID, raw.ConversationID, raw.PromptCacheKey)
	}
	if effSession == "" {
		effSession = strings.TrimSpace(raw.PreviousResponseID)
	}
	if effSession == "" {
		effSession = clientSessID
	}

	req := &CanonicalRequest{
		Protocol:        "openai_responses",
		Model:           cleanModel,
		Stream:          raw.Stream,
		ThinkingLevel:   finalThinking,
		ContextWindow:   ExtractContextWindowFromMetadata(raw.Metadata, int64(raw.ContextWindow), int64(raw.ContextSize), int64(raw.ModelContextWindow), int64(raw.MaxContextTokens), int64(raw.MaxInputTokens)),
		SessionHeader:   effSession,
		ClientSessionID: clientSessID,
	}

	for _, t := range raw.Tools {
		tName := t.Name
		tDesc := t.Description
		tParams := t.Parameters
		if tName == "" && t.Function != nil {
			tName = t.Function.Name
			tDesc = t.Function.Description
			tParams = t.Function.Parameters
		}
		if tName != "" {
			req.Tools = append(req.Tools, ToMCPToolDefinition(tName, tDesc, tParams))
		}
	}

	var sysParts []string
	if strings.TrimSpace(raw.Instructions) != "" {
		sysParts = append(sysParts, strings.TrimSpace(raw.Instructions))
	}

	var lastUserBlocks []acp.PromptBlock

	if len(raw.Input) > 0 {
		// Case 1: input is a plain string
		var strInput string
		if err := json.Unmarshal(raw.Input, &strInput); err == nil {
			trimmed := strings.TrimSpace(strInput)
			if trimmed != "" {
				req.Messages = append(req.Messages, CanonicalMessage{Role: "user", Content: trimmed})
				lastUserBlocks = []acp.PromptBlock{{Type: "text", Text: trimmed}}
			}
		} else {
			// Case 2: input is an array of items (messages, function_call, function_call_output, reasoning, input_text, etc.)
			// Tool outputs accumulate across interleaved call/output items; they are reset by
			// any item that starts a new model step (assistant message, reasoning) or a new user turn.
			var items []map[string]any
			if err := json.Unmarshal(raw.Input, &items); err == nil {
				for _, item := range items {
					itemType, _ := item["type"].(string)
					role, _ := item["role"].(string)

					switch {
					case itemType == "reasoning":
						req.ToolResults = nil
						continue
					case itemType == "item_reference":
						continue
					case isResponsesToolOutputItem(itemType):
						callID, _ := item["call_id"].(string)
						outputStr := extractResponsesToolOutput(item["output"])
						req.ToolResults = append(req.ToolResults, CanonicalToolResult{
							CallID:  strings.TrimSpace(callID),
							Content: outputStr,
						})
						req.Messages = append(req.Messages, CanonicalMessage{Role: "tool", Content: outputStr})
						continue
					case isResponsesToolCallItem(itemType):
						fcName, _ := item["name"].(string)
						if fcName != "" {
							argsVal := item["arguments"]
							if argsVal == nil {
								argsVal = item["input"]
							}
							req.Messages = append(req.Messages, CanonicalMessage{
								Role:    "assistant",
								Content: fcName + "(" + normalizeToolArgs(argsToJSONString(argsVal)) + ")",
							})
						}
						continue
					}

					rawContent, _ := json.Marshal(item["content"])
					text, blocks := parseOpenAIResponsesContentBlocks(rawContent)
					if text == "" {
						if directText, ok := item["text"].(string); ok && directText != "" {
							text = directText
							blocks = []acp.PromptBlock{{Type: "text", Text: directText}}
						}
					}

					switch role {
					case "system", "developer":
						if strings.TrimSpace(text) != "" {
							sysParts = append(sysParts, strings.TrimSpace(text))
						}
					case "assistant":
						req.ToolResults = nil
						req.Messages = append(req.Messages, CanonicalMessage{Role: "assistant", Content: text})
					case "user", "":
						if text != "" || len(blocks) > 0 {
							// A user message directly following tool outputs (e.g. injected <turn-context>)
							// belongs to the same turn and is carried along with the last tool result.
							if len(req.ToolResults) > 0 {
								appendToLastToolResult(req.ToolResults, blocksAsToolResultText(blocks))
							}
							req.Messages = append(req.Messages, CanonicalMessage{Role: "user", Content: text})
							lastUserBlocks = blocks
						}
					default:
						req.Messages = append(req.Messages, CanonicalMessage{Role: role, Content: text})
					}
				}
			}
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

// extractResponsesToolOutput flattens a `*_call_output.output` value: strings pass through,
// content-part arrays concatenate their text parts, and images/files become placeholders.
func extractResponsesToolOutput(v any) string {
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
				if s := responsesOutputPartText(e); s != "" {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if s := responsesOutputPartText(val); s != "" {
			return s
		}
		raw, _ := json.Marshal(val)
		return string(raw)
	default:
		raw, _ := json.Marshal(v)
		return string(raw)
	}
}

func responsesOutputPartText(part map[string]any) string {
	if t, ok := part["text"].(string); ok {
		return t
	}
	typ, _ := part["type"].(string)
	switch typ {
	case "input_image", "image", "image_url", "computer_screenshot":
		mime := "image"
		var urlStr string
		if u, ok := part["image_url"].(string); ok {
			urlStr = u
		} else if obj, ok := part["image_url"].(map[string]any); ok {
			urlStr, _ = obj["url"].(string)
		}
		if m, _, ok := ParseDataURI(urlStr); ok && m != "" {
			mime = m
		}
		return fmt.Sprintf("[image omitted: %s]", mime)
	case "input_file", "file":
		name, _ := part["filename"].(string)
		if name == "" {
			name, _ = part["file_id"].(string)
		}
		if name == "" {
			name = "attachment"
		}
		return fmt.Sprintf("[file omitted: %s]", name)
	}
	return ""
}

func parseOpenAIResponsesContentBlocks(raw json.RawMessage) (string, []acp.PromptBlock) {
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
		case "input_text", "output_text", "text":
			t, _ := p["text"].(string)
			if t != "" {
				textParts = append(textParts, t)
				blocks = append(blocks, acp.PromptBlock{Type: "text", Text: t})
			}
		case "input_image", "image_url":
			var urlStr string
			if u, ok := p["image_url"].(string); ok {
				urlStr = u
			} else if imgObj, ok := p["image_url"].(map[string]any); ok {
				urlStr, _ = imgObj["url"].(string)
			}
			if mime, b64, ok := ParseDataURI(urlStr); ok {
				blocks = append(blocks, acp.PromptBlock{Type: "image", MimeType: mime, Data: b64})
			} else if urlStr != "" {
				blocks = append(blocks, acp.PromptBlock{Type: "resource_link", URI: urlStr, Name: "image"})
			}
		case "input_file", "file":
			fname, _ := p["filename"].(string)
			fdata, _ := p["file_data"].(string)
			fid, _ := p["file_id"].(string)
			furl, _ := p["file_url"].(string)
			if fileObj, ok := p["file"].(map[string]any); ok {
				if fname == "" {
					fname, _ = fileObj["filename"].(string)
				}
				if fdata == "" {
					fdata, _ = fileObj["file_data"].(string)
				}
				if fid == "" {
					fid, _ = fileObj["file_id"].(string)
				}
			}
			blocks = append(blocks, fileAttachmentBlock(fname, fdata, fid, furl))
		}
	}
	return strings.Join(textParts, "\n"), blocks
}

// responsesStatusForStop maps an ACP stop reason to a Responses status and incomplete_details.
func responsesStatusForStop(acpStopReason string) (string, map[string]any) {
	switch normalizeACPStopReason(acpStopReason) {
	case "max_tokens", "max_turn_requests":
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case "refusal":
		return "incomplete", map[string]any{"reason": "content_filter"}
	default:
		return "completed", nil
	}
}

func buildResponsesUsage(promptTokens, cachedTokens, compTokens int64, thinking, text string) map[string]any {
	reasoningTokens := estimateAdapterTokens(thinking)
	if compTokens < reasoningTokens {
		compTokens = reasoningTokens + estimateAdapterTokens(text)
		if compTokens < 1 {
			compTokens = 1
		}
	}
	if promptTokens < 0 {
		promptTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > promptTokens && promptTokens > 0 {
		cachedTokens = promptTokens
	}
	return map[string]any{
		"input_tokens": promptTokens,
		"input_tokens_details": map[string]any{
			"cached_tokens": cachedTokens,
		},
		"output_tokens": compTokens,
		"output_tokens_details": map[string]any{
			"reasoning_tokens": reasoningTokens,
		},
		"total_tokens": promptTokens + compTokens,
	}
}

func buildResponsesEnvelope(respID, model string, createdAt int64, status string, output []map[string]any, outputText string, usage map[string]any) map[string]any {
	if output == nil {
		output = []map[string]any{}
	}
	resp := map[string]any{
		"id":                  respID,
		"object":              "response",
		"created_at":          createdAt,
		"status":              status,
		"model":               model,
		"output":              output,
		"output_text":         outputText,
		"error":               nil,
		"incomplete_details":  nil,
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"tools":               []any{},
		"reasoning": map[string]any{
			"summary": "auto",
		},
	}
	if usage != nil {
		resp["usage"] = usage
	}
	return resp
}

func responsesReasoningItem(id, status, text string) map[string]any {
	return map[string]any{
		"id":     id,
		"type":   "reasoning",
		"status": status,
		"summary": []map[string]any{
			{
				"type": "summary_text",
				"text": text,
			},
		},
	}
}

func responsesMessageItem(id, status, text string) map[string]any {
	return map[string]any{
		"id":     id,
		"type":   "message",
		"status": status,
		"role":   "assistant",
		"content": []map[string]any{
			{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			},
		},
	}
}

func responsesFunctionCallItem(c CanonicalToolCall, status string) map[string]any {
	return map[string]any{
		"id":        "fc_" + c.CallID,
		"type":      "function_call",
		"status":    status,
		"call_id":   c.CallID,
		"name":      c.ToolName,
		"arguments": c.ArgsJSON,
	}
}

// OpenAIResponsesSSEWriter implements the official OpenAI `/v1/responses` SSE event stream specification.
// Crucial compatibility details for strict Rust/TypeScript SDK decoders (Codex CLI, OpenAI Agents SDK, etc.):
//  1. Every SSE payload MUST include `"type": event` and a monotonically increasing `"sequence_number": N`.
//  2. Reasoning item MUST be opened with `response.output_item.added` (`type: "reasoning"`) and
//     `response.reasoning_summary_part.added` (`type: "summary_text"`) BEFORE emitting `response.reasoning_summary_text.delta`,
//     and closed with `response.reasoning_summary_text.done`, `response.reasoning_summary_part.done`, and `response.output_item.done`.
//  3. Assistant text message item MUST be opened with `response.output_item.added` (`type: "message"`) and
//     `response.content_part.added` (`type: "output_text"`) BEFORE emitting `response.output_text.delta`,
//     and closed BEFORE emitting any `function_call` item or `response.completed`.
//  4. The terminal response object's `output` lists exactly the streamed items, in output_index order.
type OpenAIResponsesSSEWriter struct {
	w                  http.ResponseWriter
	flusher            http.Flusher
	respID             string
	msgID              string
	reasonID           string
	model              string
	created            int64
	seq                int
	nextOutputIndex    int
	reasoningOpened    bool
	reasoningClosed    bool
	reasoningOutputIdx int
	reasoningBuf       strings.Builder
	messageOpened      bool
	messageClosed      bool
	messageOutputIdx   int
	textBuf            strings.Builder
	outputItems        []map[string]any
	finished           bool
	stopReason         string
}

func NewOpenAIResponsesSSEWriter(w http.ResponseWriter, respID, model string) *OpenAIResponsesSSEWriter {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	fl, _ := w.(http.Flusher)
	shortID := strings.TrimPrefix(respID, "resp_")
	return &OpenAIResponsesSSEWriter{
		w:        w,
		flusher:  fl,
		respID:   respID,
		msgID:    "msg_" + shortID,
		reasonID: "rs_" + shortID,
		model:    model,
		created:  time.Now().Unix(),
	}
}

func (s *OpenAIResponsesSSEWriter) writeEvent(event string, data map[string]any) {
	if data == nil {
		data = make(map[string]any)
	}
	data["type"] = event
	data["sequence_number"] = s.seq
	s.seq++

	raw, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, string(raw))
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *OpenAIResponsesSSEWriter) allocateOutputIndex() int {
	idx := s.nextOutputIndex
	s.nextOutputIndex++
	return idx
}

func (s *OpenAIResponsesSSEWriter) envelope(status string, usage map[string]any) map[string]any {
	output := append([]map[string]any{}, s.outputItems...)
	return buildResponsesEnvelope(s.respID, s.model, s.created, status, output, s.textBuf.String(), usage)
}

// SetStopReason records the ACP stop reason used by subsequent non-tool finish writes.
func (s *OpenAIResponsesSSEWriter) SetStopReason(acpStopReason string) {
	s.stopReason = acpStopReason
}

func (s *OpenAIResponsesSSEWriter) WriteStart() {
	if s.finished {
		return
	}
	s.writeEvent("response.created", map[string]any{
		"response": s.envelope("in_progress", nil),
	})
	s.writeEvent("response.in_progress", map[string]any{
		"response": s.envelope("in_progress", nil),
	})
}

func (s *OpenAIResponsesSSEWriter) WriteKeepAlive() {
	if s.finished {
		return
	}
	_, _ = fmt.Fprintf(s.w, ": ping\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *OpenAIResponsesSSEWriter) ensureReasoningOpened() {
	if s.reasoningOpened {
		return
	}
	s.reasoningOpened = true
	s.reasoningOutputIdx = s.allocateOutputIndex()
	s.writeEvent("response.output_item.added", map[string]any{
		"output_index": s.reasoningOutputIdx,
		"item": map[string]any{
			"id":      s.reasonID,
			"type":    "reasoning",
			"status":  "in_progress",
			"summary": []any{},
		},
	})
	s.writeEvent("response.reasoning_summary_part.added", map[string]any{
		"item_id":       s.reasonID,
		"output_index":  s.reasoningOutputIdx,
		"summary_index": 0,
		"part": map[string]any{
			"type": "summary_text",
			"text": "",
		},
	})
}

func (s *OpenAIResponsesSSEWriter) closeReasoningIfNeeded(status string) {
	if !s.reasoningOpened || s.reasoningClosed {
		return
	}
	s.reasoningClosed = true
	fullReasoning := s.reasoningBuf.String()
	s.writeEvent("response.reasoning_summary_text.done", map[string]any{
		"item_id":       s.reasonID,
		"output_index":  s.reasoningOutputIdx,
		"summary_index": 0,
		"text":          fullReasoning,
	})
	s.writeEvent("response.reasoning_summary_part.done", map[string]any{
		"item_id":       s.reasonID,
		"output_index":  s.reasoningOutputIdx,
		"summary_index": 0,
		"part": map[string]any{
			"type": "summary_text",
			"text": fullReasoning,
		},
	})
	item := responsesReasoningItem(s.reasonID, status, fullReasoning)
	s.writeEvent("response.output_item.done", map[string]any{
		"output_index": s.reasoningOutputIdx,
		"item":         item,
	})
	s.outputItems = append(s.outputItems, item)
}

func (s *OpenAIResponsesSSEWriter) ensureMessageOpened() {
	if s.messageOpened {
		return
	}
	s.closeReasoningIfNeeded("completed")
	// Once message output has started, lock out any late reasoning items so
	// thinking is never emitted after output_text.
	s.reasoningClosed = true
	s.messageOpened = true
	s.messageOutputIdx = s.allocateOutputIndex()
	s.writeEvent("response.output_item.added", map[string]any{
		"output_index": s.messageOutputIdx,
		"item": map[string]any{
			"id":      s.msgID,
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})
	s.writeEvent("response.content_part.added", map[string]any{
		"item_id":       s.msgID,
		"output_index":  s.messageOutputIdx,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        "",
			"annotations": []any{},
		},
	})
}

func (s *OpenAIResponsesSSEWriter) closeMessageIfNeeded(status string) {
	if !s.messageOpened || s.messageClosed {
		return
	}
	s.messageClosed = true
	fullText := s.textBuf.String()
	s.writeEvent("response.output_text.done", map[string]any{
		"item_id":       s.msgID,
		"output_index":  s.messageOutputIdx,
		"content_index": 0,
		"text":          fullText,
	})
	s.writeEvent("response.content_part.done", map[string]any{
		"item_id":       s.msgID,
		"output_index":  s.messageOutputIdx,
		"content_index": 0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        fullText,
			"annotations": []any{},
		},
	})
	item := responsesMessageItem(s.msgID, status, fullText)
	s.writeEvent("response.output_item.done", map[string]any{
		"output_index": s.messageOutputIdx,
		"item":         item,
	})
	s.outputItems = append(s.outputItems, item)
}

func (s *OpenAIResponsesSSEWriter) WriteThinkingDelta(thinking string) {
	if thinking == "" || s.reasoningClosed || s.finished {
		return
	}
	s.ensureReasoningOpened()
	s.reasoningBuf.WriteString(thinking)
	s.writeEvent("response.reasoning_summary_text.delta", map[string]any{
		"item_id":       s.reasonID,
		"output_index":  s.reasoningOutputIdx,
		"summary_index": 0,
		"delta":         thinking,
	})
}

func (s *OpenAIResponsesSSEWriter) WriteTextDelta(text string) {
	if text == "" || s.finished {
		return
	}
	s.ensureMessageOpened()
	s.textBuf.WriteString(text)
	s.writeEvent("response.output_text.delta", map[string]any{
		"item_id":       s.msgID,
		"output_index":  s.messageOutputIdx,
		"content_index": 0,
		"delta":         text,
	})
}

// flushPendingOutput streams thinking/text that the caller accumulated but never streamed.
func (s *OpenAIResponsesSSEWriter) flushPendingOutput(fullThinking, fullText string) {
	if !s.reasoningOpened && !s.messageOpened && fullThinking != "" {
		s.WriteThinkingDelta(fullThinking)
	}
	if !s.messageOpened && fullText != "" {
		s.WriteTextDelta(fullText)
	}
}

func (s *OpenAIResponsesSSEWriter) usageFor(fullThinking, fullText string, promptTokens, cachedTokens, compTokens int64) map[string]any {
	if fullThinking == "" {
		fullThinking = s.reasoningBuf.String()
	}
	if fullText == "" {
		fullText = s.textBuf.String()
	}
	return buildResponsesUsage(promptTokens, cachedTokens, compTokens, fullThinking, fullText)
}

func (s *OpenAIResponsesSSEWriter) WriteToolCallsAndFinish(calls []CanonicalToolCall, fullThinking, fullText string, promptTokens, cachedTokens, compTokens int64) {
	if s.finished {
		return
	}
	valid := validToolCalls(calls)
	if len(valid) == 0 {
		s.WriteStopWithCache(fullThinking, fullText, promptTokens, cachedTokens, compTokens)
		return
	}
	s.flushPendingOutput(fullThinking, fullText)
	s.closeReasoningIfNeeded("completed")
	s.closeMessageIfNeeded("completed")

	for _, c := range valid {
		fcIdx := s.allocateOutputIndex()
		itemID := "fc_" + c.CallID
		s.writeEvent("response.output_item.added", map[string]any{
			"output_index": fcIdx,
			"item": map[string]any{
				"id":        itemID,
				"type":      "function_call",
				"status":    "in_progress",
				"call_id":   c.CallID,
				"name":      c.ToolName,
				"arguments": "",
			},
		})
		s.writeEvent("response.function_call_arguments.delta", map[string]any{
			"item_id":      itemID,
			"output_index": fcIdx,
			"delta":        c.ArgsJSON,
		})
		s.writeEvent("response.function_call_arguments.done", map[string]any{
			"item_id":      itemID,
			"output_index": fcIdx,
			"arguments":    c.ArgsJSON,
		})
		item := responsesFunctionCallItem(c, "completed")
		s.writeEvent("response.output_item.done", map[string]any{
			"output_index": fcIdx,
			"item":         item,
		})
		s.outputItems = append(s.outputItems, item)
	}
	usage := s.usageFor(fullThinking, fullText, promptTokens, cachedTokens, compTokens)
	s.writeEvent("response.completed", map[string]any{
		"response": s.envelope("completed", usage),
	})
	s.finished = true
}

func (s *OpenAIResponsesSSEWriter) WriteToolCallAndFinish(callID, toolName, argsJSON, fullThinking, fullText string, promptTokens, compTokens int64) {
	s.WriteToolCallsAndFinish([]CanonicalToolCall{
		{CallID: callID, ToolName: toolName, ArgsJSON: argsJSON},
	}, fullThinking, fullText, promptTokens, 0, compTokens)
}

func (s *OpenAIResponsesSSEWriter) WriteStopWithCache(fullThinking, fullText string, promptTokens, cachedTokens, compTokens int64) {
	if s.finished {
		return
	}
	status, incomplete := responsesStatusForStop(s.stopReason)
	itemStatus := "completed"
	event := "response.completed"
	if status == "incomplete" {
		itemStatus = "incomplete"
		event = "response.incomplete"
	}
	s.flushPendingOutput(fullThinking, fullText)
	s.closeReasoningIfNeeded("completed")
	s.closeMessageIfNeeded(itemStatus)

	resp := s.envelope(status, s.usageFor(fullThinking, fullText, promptTokens, cachedTokens, compTokens))
	if incomplete != nil {
		resp["incomplete_details"] = incomplete
	}
	s.writeEvent(event, map[string]any{
		"response": resp,
	})
	s.finished = true
}

func (s *OpenAIResponsesSSEWriter) WriteStop(fullThinking, fullText string, promptTokens, compTokens int64) {
	s.WriteStopWithCache(fullThinking, fullText, promptTokens, 0, compTokens)
}

// WriteError closes any open output item as incomplete and terminates the stream with
// `response.failed`. It is a no-op once the stream has already been finished.
func (s *OpenAIResponsesSSEWriter) WriteError(message string) {
	if s.finished {
		return
	}
	s.closeReasoningIfNeeded("incomplete")
	s.closeMessageIfNeeded("incomplete")
	resp := s.envelope("failed", nil)
	resp["error"] = map[string]any{
		"code":    "server_error",
		"message": message,
	}
	s.writeEvent("response.failed", map[string]any{
		"response": resp,
	})
	s.finished = true
}

func BuildOpenAIResponsesJSONResponse(respID, model, thinking, text, toolCallID, toolName, toolArgs string, promptTokens, compTokens int64) map[string]any {
	var calls []CanonicalToolCall
	if toolCallID != "" && toolName != "" {
		calls = []CanonicalToolCall{{CallID: toolCallID, ToolName: toolName, ArgsJSON: toolArgs}}
	}
	return BuildOpenAIResponsesJSONResponseMulti(respID, model, thinking, text, calls, promptTokens, 0, compTokens)
}

func BuildOpenAIResponsesJSONResponseMulti(respID, model, thinking, text string, calls []CanonicalToolCall, promptTokens, cachedTokens, compTokens int64) map[string]any {
	output := []map[string]any{}
	shortID := strings.TrimPrefix(respID, "resp_")
	if thinking != "" {
		output = append(output, responsesReasoningItem("rs_"+shortID, "completed", thinking))
	}
	if text != "" {
		output = append(output, responsesMessageItem("msg_"+shortID, "completed", text))
	}
	for _, c := range validToolCalls(calls) {
		output = append(output, responsesFunctionCallItem(c, "completed"))
	}
	usage := buildResponsesUsage(promptTokens, cachedTokens, compTokens, thinking, text)
	return buildResponsesEnvelope(respID, model, time.Now().Unix(), "completed", output, text, usage)
}

// ApplyResponsesStopReason maps an ACP stop reason onto a response built by
// BuildOpenAIResponsesJSONResponseMulti. Responses that end in function calls stay completed.
func ApplyResponsesStopReason(resp map[string]any, acpStopReason string) map[string]any {
	if resp == nil {
		return resp
	}
	output, _ := resp["output"].([]map[string]any)
	for _, item := range output {
		if item["type"] == "function_call" {
			return resp
		}
	}
	status, incomplete := responsesStatusForStop(acpStopReason)
	resp["status"] = status
	if incomplete != nil {
		resp["incomplete_details"] = incomplete
		for _, item := range output {
			if item["type"] == "message" {
				item["status"] = "incomplete"
			}
		}
	} else {
		resp["incomplete_details"] = nil
	}
	return resp
}
