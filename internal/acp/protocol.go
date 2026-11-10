package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ErrCodeAuthRequired     = -32000
	ErrCodeResourceNotFound = -32002
)

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("acp rpc error (%d): %s", e.Code, e.Message)
}

func IsAuthRequiredError(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == ErrCodeAuthRequired {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "authentication required")
}

func IsResourceNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == ErrCodeResourceNotFound {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "resource not found")
}

type RPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type WiretapCallback func(workerID string, direction string, rawLine string)

type WorkerLaunchOptions struct {
	AccountID           string
	BackendType         string // "auto" | "zed" | "zen" | "cli"
	BinaryPath          string
	WorkspaceDir        string
	AdditionalDirs      []string
	AgentName           string
	ProcessPerSession   bool
	SkipPermissionsFlag bool
	EnableSandbox       bool
	DisableTelemetry    bool
	LogFile             string
	IDEMCPPort          int
	ExtraArgs           []string
	Env                 map[string]string
	NetNSName           string
	InternalApproval    string // "auto_approve" | "reject"
}

type ClientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type InitializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientInfo         ClientInfo     `json:"clientInfo"`
	ClientCapabilities map[string]any `json:"clientCapabilities"`
}

type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion   int            `json:"protocolVersion"`
	AgentInfo         ClientInfo     `json:"agentInfo"`
	AgentCapabilities map[string]any `json:"agentCapabilities,omitempty"`
	AuthMethods       []AuthMethod   `json:"authMethods,omitempty"`
}

type MCPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// MCPHeaders represents the ACP McpServerHttp/McpServerSse `headers` field,
// which MUST serialize as a JSON array (`[]` when empty) per the ACP v1 spec,
// while accepting either `[{"name":"k","value":"v"}]` or `{"k":"v"}` on unmarshal.
type MCPHeaders []MCPHeader

func (h MCPHeaders) MarshalJSON() ([]byte, error) {
	if h == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]MCPHeader(h))
}

func (h *MCPHeaders) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*h = MCPHeaders{}
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var slice []MCPHeader
		if err := json.Unmarshal(data, &slice); err != nil {
			return err
		}
		*h = MCPHeaders(slice)
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		out := make(MCPHeaders, 0, len(m))
		for k, v := range m {
			out = append(out, MCPHeader{Name: k, Value: v})
		}
		*h = out
		return nil
	}
	*h = MCPHeaders{}
	return nil
}

type MCPServerEndpoint struct {
	Name    string     `json:"name"`
	Type    string     `json:"type"` // "http" | "sse" | "stdio"
	URL     string     `json:"url,omitempty"`
	Command string     `json:"command,omitempty"`
	Args    []string   `json:"args,omitempty"`
	Headers MCPHeaders `json:"headers"`
}

type SystemPromptMeta struct {
	Append string `json:"append,omitempty"`
}

type AntigravityMeta struct {
	SystemPrompt      *SystemPromptMeta `json:"systemPrompt,omitempty"`
	EnabledTools      *[]string         `json:"enabledTools,omitempty"`
	EagerMCPServers   []string          `json:"eagerMcpServers,omitempty"`
	SkillDirectories  *[]string         `json:"skillDirectories,omitempty"`
	McpMaxOutputBytes *int              `json:"mcpMaxOutputBytes,omitempty"`
}

type RequestMeta struct {
	Antigravity *AntigravityMeta `json:"antigravity,omitempty"`
}

type NewSessionParams struct {
	Cwd                   string              `json:"cwd"`
	AdditionalDirectories []string            `json:"additionalDirectories,omitempty"`
	MCPServers            []MCPServerEndpoint `json:"mcpServers"`
	Meta                  *RequestMeta        `json:"_meta,omitempty"`
}

type ResumeSessionParams struct {
	SessionID             string              `json:"sessionId"`
	Cwd                   string              `json:"cwd"`
	AdditionalDirectories []string            `json:"additionalDirectories,omitempty"`
	MCPServers            []MCPServerEndpoint `json:"mcpServers"`
	Meta                  *RequestMeta        `json:"_meta,omitempty"`
}

type CloseSessionParams struct {
	SessionID string `json:"sessionId"`
}

type ConfigOptionValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ConfigOption struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Category     string              `json:"category,omitempty"`
	Type         string              `json:"type,omitempty"`
	CurrentValue string              `json:"currentValue"`
	Options      []ConfigOptionValue `json:"options,omitempty"`
}

type ModelInfo struct {
	ModelID              string   `json:"modelId"`
	Name                 string   `json:"name"`
	Description          string   `json:"description,omitempty"`
	ThinkingLevels       []string `json:"thinkingLevels,omitempty"`
	DefaultThinkingLevel string   `json:"defaultThinkingLevel,omitempty"`
}

type SessionModelsState struct {
	CurrentModelID  string      `json:"currentModelId"`
	AvailableModels []ModelInfo `json:"availableModels"`
}

type NewSessionResult struct {
	SessionID     string              `json:"sessionId"`
	ConfigOptions []ConfigOption      `json:"configOptions,omitempty"`
	Models        *SessionModelsState `json:"models,omitempty"`
}

var knownThinkingSuffixes = []string{"low", "medium", "high", "minimal", "xhigh", "extra_high", "max"}

func splitThinkingSuffix(modelID string, extraLevels []string) (baseID string, level string, ok bool) {
	allLevels := append([]string{}, extraLevels...)
	allLevels = append(allLevels, knownThinkingSuffixes...)
	lower := strings.ToLower(modelID)
	for _, lv := range allLevels {
		lv = strings.ToLower(strings.TrimSpace(lv))
		if lv == "" {
			continue
		}
		suffix := "-" + lv
		if strings.HasSuffix(lower, suffix) && len(modelID) > len(suffix) {
			return modelID[:len(modelID)-len(suffix)], lv, true
		}
	}
	return modelID, "", false
}

// ExtractModelsFromSessionResult extracts and deduplicates clean base models from
// an ACP session/new or session/resume result (`configOptions` where
// id/category == "model" and `models.availableModels`), while attaching
// supported `thinking_level` options rather than polluting the model list with
// `-low` / `-medium` / `-high` cartesian-product duplicates.
func ExtractModelsFromSessionResult(res *NewSessionResult) []ModelInfo {
	if res == nil {
		return nil
	}

	var globalThinkingLevels []string
	var defaultThinkingLevel string
	seenThinking := make(map[string]bool)
	for _, opt := range res.ConfigOptions {
		if strings.EqualFold(opt.ID, "thinking_level") || strings.EqualFold(opt.Category, "thought_level") || strings.EqualFold(opt.Category, "thinking_level") {
			if defaultThinkingLevel == "" && strings.TrimSpace(opt.CurrentValue) != "" {
				defaultThinkingLevel = strings.TrimSpace(opt.CurrentValue)
			}
			for _, v := range opt.Options {
				lv := strings.TrimSpace(v.Value)
				if lv != "" && !seenThinking[lv] {
					seenThinking[lv] = true
					globalThinkingLevels = append(globalThinkingLevels, lv)
				}
			}
		}
	}
	if len(globalThinkingLevels) == 0 {
		globalThinkingLevels = []string{"low", "medium", "high"}
	}
	if defaultThinkingLevel == "" {
		defaultThinkingLevel = "high"
	}

	var out []ModelInfo
	seen := make(map[string]int)

	addModel := func(id, name, desc string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = id
		}
		desc = strings.TrimSpace(desc)
		if idx, exists := seen[id]; exists {
			if out[idx].Name == out[idx].ModelID && name != id {
				out[idx].Name = name
			}
			if out[idx].Description == "" && desc != "" {
				out[idx].Description = desc
			}
			return
		}
		levelsCopy := append([]string(nil), globalThinkingLevels...)
		seen[id] = len(out)
		out = append(out, ModelInfo{
			ModelID:              id,
			Name:                 name,
			Description:          desc,
			ThinkingLevels:       levelsCopy,
			DefaultThinkingLevel: defaultThinkingLevel,
		})
	}

	// 1. Primary clean base model choices from configOptions[id="model"] (e.g. gemini-4-argon, gemini-3.8-flash, claude-opus-5-5, claude-sonnet-5-5, gpt-oss-120b-medium)
	for _, opt := range res.ConfigOptions {
		if strings.EqualFold(opt.ID, "model") || strings.EqualFold(opt.Category, "model") {
			for _, v := range opt.Options {
				addModel(v.Value, v.Name, v.Description)
			}
		}
	}

	// 2. Inspect models.availableModels:
	//    - Collect per-model supported thinking levels from `<base>-<thinking_level>` variants.
	//    - If an entry is `<base>-<thinking_level>` where `<base>` is already in `seen` (from configOptions[id="model"]),
	//      record `<thinking_level>` for `<base>` and skip adding the suffixed duplicate.
	//    - If `configOptions[id="model"]` was empty, collapse `<base>-{low,medium,high}` variants into `<base>`.
	perModelLevels := make(map[string][]string)
	appendUniqueLevel := func(modelKey, lv string) {
		lv = strings.ToLower(strings.TrimSpace(lv))
		if modelKey == "" || lv == "" {
			return
		}
		for _, existing := range perModelLevels[modelKey] {
			if existing == lv {
				return
			}
		}
		perModelLevels[modelKey] = append(perModelLevels[modelKey], lv)
	}

	if res.Models != nil {
		baseVariantCounts := make(map[string]int)
		for _, m := range res.Models.AvailableModels {
			rawID := strings.TrimSpace(m.ModelID)
			if base, lv, ok := splitThinkingSuffix(rawID, globalThinkingLevels); ok {
				baseVariantCounts[base]++
				if _, rawExists := seen[rawID]; rawExists {
					appendUniqueLevel(rawID, lv)
				} else {
					appendUniqueLevel(base, lv)
				}
			}
		}
		for _, m := range res.Models.AvailableModels {
			rawID := strings.TrimSpace(m.ModelID)
			if rawID == "" {
				continue
			}
			if base, _, ok := splitThinkingSuffix(rawID, globalThinkingLevels); ok {
				if _, baseExists := seen[base]; baseExists {
					// Already have the clean base model from configOptions[id="model"]; ignore suffixed duplicate.
					continue
				}
				if baseVariantCounts[base] > 1 {
					// Multiple thinking-level variants exist for the same base model; collapse into clean base model.
					cleanName := strings.TrimSpace(m.Name)
					for _, suffixLabel := range []string{" (Low)", " (Medium)", " (High)", " - Low", " - Medium", " - High"} {
						if strings.HasSuffix(cleanName, suffixLabel) {
							cleanName = strings.TrimSuffix(cleanName, suffixLabel)
						}
					}
					addModel(base, cleanName, base)
					continue
				}
			}
			addModel(rawID, m.Name, m.Description)
		}
	}

	sortCanonicalThinkingLevels := func(levels []string) []string {
		if len(levels) <= 1 {
			return levels
		}
		canonicalOrder := map[string]int{
			"minimal": 1,
			"low":     2,
			"medium":  3,
			"high":    4,
			"xhigh":   5,
			"max":     6,
		}
		sorted := make([]string, len(levels))
		copy(sorted, levels)
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				oi, okI := canonicalOrder[sorted[i]]
				if !okI {
					oi = 100 + i
				}
				oj, okJ := canonicalOrder[sorted[j]]
				if !okJ {
					oj = 100 + j
				}
				if oi > oj {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		return sorted
	}

	for i := range out {
		out[i].ThinkingLevels = sortCanonicalThinkingLevels(out[i].ThinkingLevels)
		if lvls, ok := perModelLevels[out[i].ModelID]; ok && len(lvls) > 0 {
			lvls = sortCanonicalThinkingLevels(lvls)
			out[i].ThinkingLevels = lvls
			hasDefault := false
			for _, lv := range lvls {
				if strings.EqualFold(lv, out[i].DefaultThinkingLevel) {
					hasDefault = true
					break
				}
			}
			if !hasDefault {
				out[i].DefaultThinkingLevel = lvls[len(lvls)-1]
			}
		} else if _, fixedLv, ok := splitThinkingSuffix(out[i].ModelID, globalThinkingLevels); ok && fixedLv != "" {
			out[i].ThinkingLevels = []string{fixedLv}
			out[i].DefaultThinkingLevel = fixedLv
		}
	}

	if len(out) == 0 {
		for _, opt := range res.ConfigOptions {
			if (strings.EqualFold(opt.ID, "model") || strings.EqualFold(opt.Category, "model")) && strings.TrimSpace(opt.CurrentValue) != "" {
				addModel(opt.CurrentValue, opt.CurrentValue, "")
			}
		}
		if res.Models != nil && strings.TrimSpace(res.Models.CurrentModelID) != "" {
			cur := strings.TrimSpace(res.Models.CurrentModelID)
			if base, _, ok := splitThinkingSuffix(cur, globalThinkingLevels); ok && base != "gpt-oss-120b" {
				cur = base
			}
			addModel(cur, cur, "")
		}
	}

	return out
}

type SetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"` // "model" | "thinking_level" | "mode"
	Value     string `json:"value"`
}

type EmbeddedResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type PromptBlock struct {
	Type     string            `json:"type"` // "text" | "image" | "audio" | "resource" | "resource_link"
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`
	MimeType string            `json:"mimeType,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Name     string            `json:"name,omitempty"`
	Resource *EmbeddedResource `json:"resource,omitempty"`
}

type PromptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []PromptBlock `json:"prompt"`
}

type PromptUsage struct {
	TotalTokens       int64 `json:"totalTokens"`
	InputTokens       int64 `json:"inputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
	ThoughtTokens     int64 `json:"thoughtTokens"`
	CachedReadTokens  int64 `json:"cachedReadTokens"`
	CachedWriteTokens int64 `json:"cachedWriteTokens"`
}

type PromptResult struct {
	StopReason string       `json:"stopReason"`
	Usage      *PromptUsage `json:"usage,omitempty"`
}

type CancelParams struct {
	SessionID string `json:"sessionId"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (c *ContentBlock) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*c = ContentBlock{}
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var plain struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content,omitempty"`
		}
		if err := json.Unmarshal(data, &plain); err != nil {
			return err
		}
		text := plain.Text
		if text == "" && plain.Content != nil {
			text = plain.Content.Text
		}
		*c = ContentBlock{Type: plain.Type, Text: text}
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content,omitempty"`
		}
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil
		}
		var parts []string
		for _, item := range arr {
			t := strings.TrimSpace(item.Text)
			if t == "" && item.Content != nil {
				t = strings.TrimSpace(item.Content.Text)
			}
			if t != "" {
				parts = append(parts, t)
			}
		}
		*c = ContentBlock{
			Type: "text",
			Text: strings.Join(parts, "\n"),
		}
		return nil
	}
	return nil
}

type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

type SessionUpdatePayload struct {
	SessionUpdate string          `json:"sessionUpdate"` // "agent_thought_chunk" | "agent_message_chunk" | "tool_call" | "tool_call_update" | "plan"
	Content       ContentBlock    `json:"content,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	Title         string          `json:"title,omitempty"`
	Kind          string          `json:"kind,omitempty"`
	Status        string          `json:"status,omitempty"`
	RawInput      json.RawMessage `json:"rawInput,omitempty"`
	RawOutput     json.RawMessage `json:"rawOutput,omitempty"`
	Entries       []PlanEntry     `json:"entries,omitempty"`
}

type SessionUpdateParams struct {
	SessionID string               `json:"sessionId"`
	Update    SessionUpdatePayload `json:"update"`
}
