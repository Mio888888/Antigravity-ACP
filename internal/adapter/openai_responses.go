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
	ContextWindow      int64                   `json:"context_window"`
	ModelContextWindow int64                   `json:"model_context_window"`
	MaxInputTokens     int64                   `json:"max_input_tokens"`
	MaxContextTokens   int64                   `json:"max_context_tokens"`
	SessionID          string                  `json:"session_id"`
	ConversationID     string                  `json:"conversation_id"`
	PromptCacheKey     string                  `json:"prompt_cache_key"`
	User               string                  `json:"user"`
	Metadata           map[string]any          `json:"metadata"`
	Input              json.RawMessage         `json:"input"`
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
	if effSession == "" && clientSessID != "" {
		effSession = clientSessID
	}
	if effSession == "" && strings.TrimSpace(raw.PreviousResponseID) != "" {
		effSession = strings.TrimSpace(raw.PreviousResponseID)
	}

	req := &CanonicalRequest{
		Protocol:        "openai_responses",
		Model:           cleanModel,
		Stream:          raw.Stream,
		ThinkingLevel:   finalThinking,
		ContextWindow:   ExtractContextWindowFromMetadata(raw.Metadata, raw.ContextWindow, raw.ModelContextWindow, raw.MaxContextTokens, raw.MaxInputTokens),
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
			var items []map[string]any
			if err := json.Unmarshal(raw.Input, &items); err == nil {
				lastItemWasToolOutput := false
				for _, item := range items {
					itemType, _ := item["type"].(string)
					role, _ := item["role"].(string)

					switch itemType {
					case "reasoning":
						continue
					case "function_call":
						req.ToolResults = nil
						lastItemWasToolOutput = false
						fcName, _ := item["name"].(string)
						fcArgs, _ := item["arguments"].(string)
						req.Messages = append(req.Messages, CanonicalMessage{
							Role:    "assistant",
							Content: fcName + "(" + fcArgs + ")",
						})
						continue
					case "function_call_output":
						callID, _ := item["call_id"].(string)
						outputStr := extractResponsesItemText(item["output"])
						req.ToolResults = append(req.ToolResults, CanonicalToolResult{
							CallID:  strings.TrimSpace(callID),
							Content: outputStr,
						})
						lastItemWasToolOutput = true
						req.Messages = append(req.Messages, CanonicalMessage{Role: "tool", Content: outputStr})
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
						lastItemWasToolOutput = false
						req.Messages = append(req.Messages, CanonicalMessage{Role: "assistant", Content: text})
					case "user", "":
						if text != "" || len(blocks) > 0 {
							if lastItemWasToolOutput && len(req.ToolResults) > 0 {
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
								lastItemWasToolOutput = false
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
	req.SessionTitle = ExtractSessionTitleFromMessages(raw.Metadata, req.SystemPrompt, req.Messages)
	req.HistoryHashBefore, req.HistoryHashFull = ComputeHistoryHashes(req.SystemPrompt, req.Messages)
	return req, nil
}

func extractResponsesItemText(v any) string {
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
		if v == nil {
			return ""
		}
		raw, _ := json.Marshal(v)
		return string(raw)
	}
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
			if fileObj, ok := p["file"].(map[string]any); ok {
				if fname == "" {
					fname, _ = fileObj["filename"].(string)
				}
				if fdata == "" {
					fdata, _ = fileObj["file_data"].(string)
				}
			}
			if fname == "" {
				fname = "attachment"
			}
			if fdata != "" {
				if mime, b64, ok := ParseDataURI(fdata); ok {
					blocks = append(blocks, acp.PromptBlock{
						Type: "resource",
						Resource: &acp.EmbeddedResource{
							URI:      "file:///" + fname,
							MimeType: mime,
							Blob:     b64,
						},
					})
				} else {
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
	}
	return strings.Join(textParts, "\n"), blocks
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

func (s *OpenAIResponsesSSEWriter) WriteStart() {
	baseResp := map[string]any{
		"id":                  s.respID,
		"object":              "response",
		"created_at":          s.created,
		"status":              "in_progress",
		"model":               s.model,
		"output":              []any{},
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"tools":               []any{},
	}
	s.writeEvent("response.created", map[string]any{
		"response": baseResp,
	})
	s.writeEvent("response.in_progress", map[string]any{
		"response": baseResp,
	})
}

func (s *OpenAIResponsesSSEWriter) WriteKeepAlive() {
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

func (s *OpenAIResponsesSSEWriter) closeReasoningIfNeeded() {
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
	s.writeEvent("response.output_item.done", map[string]any{
		"output_index": s.reasoningOutputIdx,
		"item": map[string]any{
			"id":     s.reasonID,
			"type":   "reasoning",
			"status": "completed",
			"summary": []map[string]any{
				{
					"type": "summary_text",
					"text": fullReasoning,
				},
			},
		},
	})
}

func (s *OpenAIResponsesSSEWriter) ensureMessageOpened() {
	if s.messageOpened {
		return
	}
	s.closeReasoningIfNeeded()
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

func (s *OpenAIResponsesSSEWriter) closeMessageIfNeeded() {
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
	s.writeEvent("response.output_item.done", map[string]any{
		"output_index": s.messageOutputIdx,
		"item": map[string]any{
			"id":     s.msgID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{
				{
					"type":        "output_text",
					"text":        fullText,
					"annotations": []any{},
				},
			},
		},
	})
}

func (s *OpenAIResponsesSSEWriter) WriteThinkingDelta(thinking string) {
	if thinking == "" || s.reasoningClosed {
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
	if text == "" {
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

func (s *OpenAIResponsesSSEWriter) WriteToolCallsAndFinish(calls []CanonicalToolCall, fullThinking, fullText string, promptTokens, cachedTokens, compTokens int64) {
	if !s.reasoningOpened && !s.messageOpened && fullThinking != "" {
		s.WriteThinkingDelta(fullThinking)
	}
	s.closeReasoningIfNeeded()
	if !s.messageOpened && fullText != "" {
		s.WriteTextDelta(fullText)
	}
	s.closeMessageIfNeeded()

	for _, c := range calls {
		if c.CallID == "" || c.ToolName == "" {
			continue
		}
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
		s.writeEvent("response.output_item.done", map[string]any{
			"output_index": fcIdx,
			"item": map[string]any{
				"id":        itemID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   c.CallID,
				"name":      c.ToolName,
				"arguments": c.ArgsJSON,
			},
		})
	}
	// When output_text has already been streamed via response.output_text.delta,
	// do not include a "reasoning" item in response.completed.response.output:
	// SSE clients that extract reasoning_from_output on response.completed would
	// otherwise append a Thinking block after the streamed Text block and fold the answer.
	completedThinking := fullThinking
	if s.textBuf.Len() > 0 || fullText != "" {
		completedThinking = ""
	}
	reasoningTokens := estimateAdapterTokens(fullThinking)
	if compTokens < reasoningTokens {
		compTokens = reasoningTokens + estimateAdapterTokens(fullText)
		if compTokens < 1 {
			compTokens = 1
		}
	}
	respObj := BuildOpenAIResponsesJSONResponseMulti(s.respID, s.model, completedThinking, fullText, calls, promptTokens, cachedTokens, compTokens)
	if reasoningTokens > 0 {
		if usageMap, ok := respObj["usage"].(map[string]any); ok {
			usageMap["output_tokens_details"] = map[string]any{
				"reasoning_tokens": reasoningTokens,
			}
		}
	}
	s.writeEvent("response.completed", map[string]any{
		"response": respObj,
	})
}

func (s *OpenAIResponsesSSEWriter) WriteToolCallAndFinish(callID, toolName, argsJSON, fullThinking, fullText string, promptTokens, compTokens int64) {
	s.WriteToolCallsAndFinish([]CanonicalToolCall{
		{CallID: callID, ToolName: toolName, ArgsJSON: argsJSON},
	}, fullThinking, fullText, promptTokens, 0, compTokens)
}

func (s *OpenAIResponsesSSEWriter) WriteStopWithCache(fullThinking, fullText string, promptTokens, cachedTokens, compTokens int64) {
	if !s.reasoningOpened && !s.messageOpened && fullThinking != "" {
		s.WriteThinkingDelta(fullThinking)
	}
	s.closeReasoningIfNeeded()
	if !s.messageOpened {
		if fullText != "" {
			s.WriteTextDelta(fullText)
		} else {
			// Ensure at least an empty completed message item if nothing was emitted
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
	}
	s.closeMessageIfNeeded()

	completedThinking := fullThinking
	if s.textBuf.Len() > 0 || fullText != "" {
		completedThinking = ""
	}
	reasoningTokens := estimateAdapterTokens(fullThinking)
	if compTokens < reasoningTokens {
		compTokens = reasoningTokens + estimateAdapterTokens(fullText)
		if compTokens < 1 {
			compTokens = 1
		}
	}
	respObj := BuildOpenAIResponsesJSONResponseMulti(s.respID, s.model, completedThinking, fullText, nil, promptTokens, cachedTokens, compTokens)
	if reasoningTokens > 0 {
		if usageMap, ok := respObj["usage"].(map[string]any); ok {
			usageMap["output_tokens_details"] = map[string]any{
				"reasoning_tokens": reasoningTokens,
			}
		}
	}
	s.writeEvent("response.completed", map[string]any{
		"response": respObj,
	})
}

func (s *OpenAIResponsesSSEWriter) WriteStop(fullThinking, fullText string, promptTokens, compTokens int64) {
	s.WriteStopWithCache(fullThinking, fullText, promptTokens, 0, compTokens)
}

func BuildOpenAIResponsesJSONResponse(respID, model, thinking, text, toolCallID, toolName, toolArgs string, promptTokens, compTokens int64) map[string]any {
	var calls []CanonicalToolCall
	if toolCallID != "" && toolName != "" {
		calls = []CanonicalToolCall{{CallID: toolCallID, ToolName: toolName, ArgsJSON: toolArgs}}
	}
	return BuildOpenAIResponsesJSONResponseMulti(respID, model, thinking, text, calls, promptTokens, 0, compTokens)
}

func BuildOpenAIResponsesJSONResponseMulti(respID, model, thinking, text string, calls []CanonicalToolCall, promptTokens, cachedTokens, compTokens int64) map[string]any {
	var output []map[string]any
	shortID := strings.TrimPrefix(respID, "resp_")
	reasoningTokens := estimateAdapterTokens(thinking)
	if compTokens < reasoningTokens {
		compTokens = reasoningTokens + estimateAdapterTokens(text)
		if compTokens < 1 {
			compTokens = 1
		}
	}
	if thinking != "" {
		output = append(output, map[string]any{
			"id":     "rs_" + shortID,
			"type":   "reasoning",
			"status": "completed",
			"summary": []map[string]any{
				{
					"type": "summary_text",
					"text": thinking,
				},
			},
		})
	}
	if text != "" || (len(calls) == 0 && len(output) == 0) {
		output = append(output, map[string]any{
			"id":     "msg_" + shortID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{
				{
					"type":        "output_text",
					"text":        text,
					"annotations": []any{},
				},
			},
		})
	}
	for _, c := range calls {
		if c.CallID != "" && c.ToolName != "" {
			output = append(output, map[string]any{
				"id":        "fc_" + c.CallID,
				"type":      "function_call",
				"status":    "completed",
				"call_id":   c.CallID,
				"name":      c.ToolName,
				"arguments": c.ArgsJSON,
			})
		}
	}

	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cachedTokens > promptTokens && promptTokens > 0 {
		cachedTokens = promptTokens
	}

	return map[string]any{
		"id":                  respID,
		"object":              "response",
		"created_at":          time.Now().Unix(),
		"status":              "completed",
		"model":               model,
		"output":              output,
		"output_text":         text,
		"reasoning_text":      thinking,
		"parallel_tool_calls": true,
		"tool_choice":         "auto",
		"tools":               []any{},
		"reasoning": map[string]any{
			"effort":  "high",
			"summary": "auto",
		},
		"usage": map[string]any{
			"input_tokens": promptTokens,
			"input_tokens_details": map[string]any{
				"cached_tokens": cachedTokens,
			},
			"output_tokens": compTokens,
			"output_tokens_details": map[string]any{
				"reasoning_tokens": reasoningTokens,
			},
			"total_tokens": promptTokens + compTokens,
		},
	}
}
