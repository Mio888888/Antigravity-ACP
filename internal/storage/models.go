package storage

import (
	"net/url"
	"strings"
	"time"
)

type Account struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	AuthMode         string    `json:"auth_mode"` // "oauth_personal" | "oauth_business" | "gemini_api_key" | "agent_platform"
	APIKey           string    `json:"api_key,omitempty"`
	GCPProject       string    `json:"gcp_project,omitempty"`
	GCPLocation      string    `json:"gcp_location,omitempty"`
	IsolationEnabled bool      `json:"isolation_enabled"`
	NetworkMode      string    `json:"network_mode"` // "direct" | "proxy" | "bind_ip" | "netns"
	ProxyURL         string    `json:"proxy_url,omitempty"`
	BindIP           string    `json:"bind_ip,omitempty"`
	NetNSName        string    `json:"netns_name,omitempty"`
	MaxWorkers       int       `json:"max_workers"`
	Weight           int       `json:"weight"`
	Enabled          bool      `json:"enabled"`
	Status           string    `json:"status"` // "ready" | "auth_required" | "error"
	LastEgressIP     string    `json:"last_egress_ip,omitempty"`
	DisabledModels   []string  `json:"disabled_models"`
	// HasAPIKey is set on redacted copies so clients can tell a key is stored.
	HasAPIKey bool      `json:"has_api_key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MaskedSecret replaces secrets in admin API output; sending it back means "unchanged".
const MaskedSecret = "********"

// Redacted returns a copy safe to return from admin APIs: the API key is dropped
// and any proxy password is masked.
func (a Account) Redacted() Account {
	a.HasAPIKey = strings.TrimSpace(a.APIKey) != ""
	a.APIKey = ""
	a.ProxyURL = MaskProxyURL(a.ProxyURL)
	return a
}

// MaskProxyURL replaces the password component of a proxy URL with MaskedSecret.
func MaskProxyURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPwd := u.User.Password(); !hasPwd {
		return raw
	}
	user := url.User(u.User.Username()).String()
	rest := *u
	rest.User = nil
	s := rest.String()
	prefix := u.Scheme + "://"
	if !strings.HasPrefix(s, prefix) {
		return raw
	}
	return prefix + user + ":" + MaskedSecret + "@" + strings.TrimPrefix(s, prefix)
}

// RestoreMaskedProxyURL returns incoming with the masked password replaced by the
// password from existing, when both point at the same proxy user and host.
func RestoreMaskedProxyURL(incoming, existing string) string {
	in, err := url.Parse(strings.TrimSpace(incoming))
	if err != nil || in.User == nil {
		return incoming
	}
	if pwd, ok := in.User.Password(); !ok || pwd != MaskedSecret {
		return incoming
	}
	ex, err := url.Parse(strings.TrimSpace(existing))
	if err != nil || ex.User == nil {
		return incoming
	}
	exPwd, ok := ex.User.Password()
	if !ok || ex.User.Username() != in.User.Username() || !strings.EqualFold(ex.Host, in.Host) || !strings.EqualFold(ex.Scheme, in.Scheme) {
		return incoming
	}
	in.User = url.UserPassword(in.User.Username(), exPwd)
	return in.String()
}

func (a Account) IsModelDisabled(modelID string) bool {
	target := strings.ToLower(strings.TrimSpace(modelID))
	if target == "" || len(a.DisabledModels) == 0 {
		return false
	}
	baseTarget := stripModelThinkingSuffix(target)
	for _, dm := range a.DisabledModels {
		d := strings.ToLower(strings.TrimSpace(dm))
		if d == "" {
			continue
		}
		if d == target || d == baseTarget || stripModelThinkingSuffix(d) == baseTarget {
			return true
		}
	}
	return false
}

func stripModelThinkingSuffix(modelID string) string {
	for _, suffix := range []string{"-minimal", "-low", "-medium", "-high", "-max"} {
		if strings.HasSuffix(modelID, suffix) && len(modelID) > len(suffix) {
			return strings.TrimSuffix(modelID, suffix)
		}
	}
	return modelID
}

type QuotaWindow struct {
	Label        string  `json:"label"`
	RemainingPct float64 `json:"remaining_pct"`
	ResetText    string  `json:"reset_text"`
}

type ModelQuotaGroup struct {
	GroupName string        `json:"group_name"`
	Windows   []QuotaWindow `json:"windows"`
}

type AccountUsageStats struct {
	TotalRequests         int               `json:"total_requests"`
	Requests24h           int               `json:"requests_24h"`
	SuccessRequests       int               `json:"success_requests"`
	ErrorRequests         int               `json:"error_requests"`
	SuccessRatePct        float64           `json:"success_rate_pct"`
	PromptTokens          int64             `json:"prompt_tokens"`
	CompletionTokens      int64             `json:"completion_tokens"`
	TotalTokens           int64             `json:"total_tokens"`
	Tokens24h             int64             `json:"tokens_24h"`
	TodaySuccess          int               `json:"today_success"`
	TodayFailed           int               `json:"today_failed"`
	TodayPromptTokens     int64             `json:"today_prompt_tokens"`
	TodayCompletionTokens int64             `json:"today_completion_tokens"`
	WeekPromptTokens      int64             `json:"week_prompt_tokens"`
	WeekCompletionTokens  int64             `json:"week_completion_tokens"`
	AvgLatencyMs          float64           `json:"avg_latency_ms"`
	LastUsedAt            string            `json:"last_used_at,omitempty"`
	QuotaReadAt           string            `json:"quota_read_at,omitempty"`
	LastCheckAt           string            `json:"last_check_at,omitempty"`
	LastCheckMsg          string            `json:"last_check_msg,omitempty"`
	QuotaGroups           []ModelQuotaGroup `json:"quota_groups"`
}

type APIKey struct {
	ID                  int64     `json:"id"`
	KeyHash             string    `json:"-"`
	KeyPrefix           string    `json:"key_prefix"`
	Name                string    `json:"name"`
	BoundAccountID      string    `json:"bound_account_id"`
	RateLimitRPM        int       `json:"rate_limit_rpm"`
	PromptMode          string    `json:"prompt_mode"`           // "pure_passthrough" | "prepend_global" | "native_hybrid"
	SessionMode         string    `json:"session_mode"`          // "yolo" | "stateless_single_turn" | "sticky_conversation"
	GlobalPrependPrompt string    `json:"global_prepend_prompt"` // Per-key system prompt prefix
	DisableBuiltinTools bool      `json:"disable_builtin_tools"`
	ContextWindow       int64     `json:"context_window"` // Per-key context window override (tokens); 0 = client-provided/auto
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
}

type PresetSkill struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"` // "instruction" | "mcp_server"
	ContentJSON string    `json:"content_json"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AuditLog struct {
	RequestID        string    `json:"request_id"`
	Protocol         string    `json:"protocol"` // "openai" | "openai_responses" | "claude" | "acp_ws"
	AccountID        string    `json:"account_id"`
	SessionID        string    `json:"session_id"`
	WorkerPID        int       `json:"worker_pid"`
	ModelName        string    `json:"model_name"`
	ThinkingLevel    string    `json:"thinking_level,omitempty"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CachedTokens     int64     `json:"cached_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	LatencyMs        int64     `json:"latency_ms"`
	StatusCode       int       `json:"status_code"`
	ErrorMsg         string    `json:"error_msg"`
	SystemPrompt     string    `json:"system_prompt,omitempty"`
	PromptText       string    `json:"prompt_text,omitempty"`
	ThinkingText     string    `json:"thinking_text,omitempty"`
	ResponseText     string    `json:"response_text,omitempty"`
	ToolCallsJSON    string    `json:"tool_calls_json,omitempty"`
	ContextJSON      string    `json:"context_json,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type OverviewStats struct {
	TotalAccounts   int     `json:"total_accounts"`
	ReadyAccounts   int     `json:"ready_accounts"`
	TotalRequests   int     `json:"total_requests"`
	SuccessRequests int     `json:"success_requests"`
	TotalTokens     int64   `json:"total_tokens"`
	AvgLatencyMs    float64 `json:"avg_latency_ms"`
}
