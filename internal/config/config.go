package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Host               string   `yaml:"host" json:"host"`
	Port               int      `yaml:"port" json:"port"`
	PublicBaseURL      string   `yaml:"public_base_url" json:"public_base_url"`
	DataDir            string   `yaml:"data_dir" json:"data_dir"`
	AdminToken         string   `yaml:"admin_token" json:"admin_token"`
	AdminPassword      string   `yaml:"admin_password" json:"admin_password"`
	CORSAllowedOrigins []string `yaml:"cors_allowed_origins" json:"cors_allowed_origins"`
	SSEKeepAliveSec    int      `yaml:"sse_keepalive_sec" json:"sse_keepalive_sec"`
	ReadTimeoutSec     int      `yaml:"read_timeout_sec" json:"read_timeout_sec"`
	WriteTimeoutSec    int      `yaml:"write_timeout_sec" json:"write_timeout_sec"`
}

type ACPPoolConfig struct {
	BackendType        string            `yaml:"backend_type" json:"backend_type"` // "auto" | "zed" | "zen" | "cli"
	BinaryPath         string            `yaml:"binary_path" json:"binary_path"`
	WorkspaceDir       string            `yaml:"workspace_dir" json:"workspace_dir"`
	AdditionalDirs     []string          `yaml:"additional_dirs" json:"additional_dirs"`
	AgentName          string            `yaml:"agent_name" json:"agent_name"`
	ClientName         string            `yaml:"client_name" json:"client_name"`
	ClientVersion      string            `yaml:"client_version" json:"client_version"`
	ProcessPerSession  bool              `yaml:"process_per_session" json:"process_per_session"`
	DisableTelemetry   bool              `yaml:"disable_telemetry" json:"disable_telemetry"`
	LogFile            string            `yaml:"log_file" json:"log_file"`
	IDEMCPPort         int               `yaml:"ide_mcp_port" json:"ide_mcp_port"`
	MinIdleWorkers     int               `yaml:"min_idle_workers" json:"min_idle_workers"`
	MaxWorkers         int               `yaml:"max_workers" json:"max_workers"`
	MaxIdleTimeSec     int               `yaml:"max_idle_time_sec" json:"max_idle_time_sec"`
	ToolParkTimeoutSec int               `yaml:"tool_park_timeout_sec" json:"tool_park_timeout_sec"`
	ContextSize        int64             `yaml:"context_size" json:"context_size"`
	ContextPatch       bool              `yaml:"context_patch" json:"context_patch"`
	ContextPatchTokens int64             `yaml:"context_patch_tokens" json:"context_patch_tokens"`
	ExtraArgs          []string          `yaml:"extra_args" json:"extra_args"`
	Env                map[string]string `yaml:"env" json:"env"`
}

type PromptPolicyConfig struct {
	Mode                string `yaml:"mode" json:"mode"`
	DisableBuiltinTools bool   `yaml:"disable_builtin_tools" json:"disable_builtin_tools"`
	GlobalPrependPrompt string `yaml:"global_prepend_prompt" json:"global_prepend_prompt"`
}

type SandboxPolicyConfig struct {
	SessionMode         string `yaml:"session_mode" json:"session_mode"`
	EnableSandbox       bool   `yaml:"enable_sandbox" json:"enable_sandbox"`
	SkipPermissionsFlag bool   `yaml:"skip_permissions_flag" json:"skip_permissions_flag"`
	InternalApproval    string `yaml:"internal_approval" json:"internal_approval"`
}

type Config struct {
	mu            sync.RWMutex        `yaml:"-" json:"-"`
	Server        ServerConfig        `yaml:"server" json:"server"`
	ACPPool       ACPPoolConfig       `yaml:"acp_pool" json:"acp_pool"`
	PromptPolicy  PromptPolicyConfig  `yaml:"prompt_policy" json:"prompt_policy"`
	SandboxPolicy SandboxPolicyConfig `yaml:"sandbox_policy" json:"sandbox_policy"`
}

func ValidatePromptPolicyMode(mode string) error {
	switch mode {
	case "pure_passthrough", "prepend_global", "native_hybrid":
		return nil
	default:
		return fmt.Errorf("invalid prompt policy mode %q (must be pure_passthrough, prepend_global, or native_hybrid)", mode)
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{
		Server: ServerConfig{
			Host:               "0.0.0.0",
			Port:               8080,
			PublicBaseURL:      "http://localhost:8080",
			DataDir:            "./data",
			AdminToken:         "auto",
			AdminPassword:      "auto",
			CORSAllowedOrigins: []string{"*"},
			SSEKeepAliveSec:    15,
			ReadTimeoutSec:     600,
			WriteTimeoutSec:    600,
		},
		ACPPool: ACPPoolConfig{
			BackendType:        "cli",
			WorkspaceDir:       "/tmp/antigravity-gateway-workspace",
			ClientName:         "zed",
			ClientVersion:      "0.175.0",
			ProcessPerSession:  true,
			DisableTelemetry:   true,
			MinIdleWorkers:     1,
			MaxWorkers:         5,
			MaxIdleTimeSec:     600,
			ToolParkTimeoutSec: 180,
			ContextPatch:       true,
			ContextPatchTokens: 400000,
			Env:                map[string]string{},
		},
		PromptPolicy: PromptPolicyConfig{
			Mode:                "pure_passthrough",
			DisableBuiltinTools: true,
		},
		SandboxPolicy: SandboxPolicyConfig{
			SessionMode:      "yolo",
			InternalApproval: "auto_approve",
		},
	}

	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}
	if envToken := os.Getenv("AG_ADMIN_TOKEN"); envToken != "" {
		cfg.Server.AdminToken = envToken
	}
	if envPwd := os.Getenv("AG_ADMIN_PASSWORD"); envPwd != "" {
		cfg.Server.AdminPassword = envPwd
	}
	if envHost := strings.TrimSpace(os.Getenv("AG_HOST")); envHost != "" {
		cfg.Server.Host = envHost
	}
	if envPort := strings.TrimSpace(os.Getenv("AG_PORT")); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			cfg.Server.Port = p
		}
	}
	if envDataDir := strings.TrimSpace(os.Getenv("AG_DATA_DIR")); envDataDir != "" {
		cfg.Server.DataDir = envDataDir
	}
	if envBackend := strings.TrimSpace(os.Getenv("AG_ACP_BACKEND")); envBackend != "" {
		cfg.ACPPool.BackendType = envBackend
	}
	if envBin := strings.TrimSpace(os.Getenv("AG_ACP_BINARY_PATH")); envBin != "" {
		cfg.ACPPool.BinaryPath = envBin
	}
	if envWs := strings.TrimSpace(os.Getenv("AG_WORKSPACE_DIR")); envWs != "" {
		cfg.ACPPool.WorkspaceDir = envWs
	}
	if envCtx := strings.TrimSpace(os.Getenv("AG_ACP_CONTEXT_SIZE")); envCtx != "" {
		if n, err := strconv.ParseInt(envCtx, 10, 64); err == nil && n >= 0 {
			cfg.ACPPool.ContextSize = n
		}
	}
	if envPatch := strings.TrimSpace(os.Getenv("AG_ACP_CONTEXT_PATCH")); envPatch != "" {
		if b, err := strconv.ParseBool(envPatch); err == nil {
			cfg.ACPPool.ContextPatch = b
		}
	}
	if envPatchTok := strings.TrimSpace(os.Getenv("AG_ACP_CONTEXT_PATCH_TOKENS")); envPatchTok != "" {
		if n, err := strconv.ParseInt(envPatchTok, 10, 64); err == nil && n >= 0 {
			cfg.ACPPool.ContextPatchTokens = n
		}
	}
	if strings.TrimSpace(cfg.ACPPool.BackendType) == "" {
		cfg.ACPPool.BackendType = "cli"
	}
	if cfg.ACPPool.Env == nil {
		cfg.ACPPool.Env = map[string]string{}
	}
	if err := ValidatePromptPolicyMode(cfg.PromptPolicy.Mode); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) GetPromptPolicy() PromptPolicyConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.PromptPolicy
}

func (c *Config) UpdatePromptPolicy(p PromptPolicyConfig) error {
	if err := ValidatePromptPolicyMode(p.Mode); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.PromptPolicy = p
	return nil
}

func (c *Config) GetACPBinaryPath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ACPPool.BinaryPath
}

func (c *Config) SetACPBinaryPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ACPPool.BinaryPath = path
}

func (c *Config) GetACPPool() ACPPoolConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := c.ACPPool
	envCopy := make(map[string]string, len(c.ACPPool.Env))
	for k, v := range c.ACPPool.Env {
		envCopy[k] = v
	}
	cp.Env = envCopy
	return cp
}

func (c *Config) SetBackendType(backendType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if bt := strings.TrimSpace(backendType); bt != "" {
		c.ACPPool.BackendType = bt
	}
}

func (c *Config) GetContextPatch() (enabled bool, tokens int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ACPPool.ContextPatch, c.ACPPool.ContextPatchTokens
}

func (c *Config) SetContextPatch(enabled bool, tokens int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ACPPool.ContextPatch = enabled
	if tokens < 0 {
		tokens = 0
	}
	c.ACPPool.ContextPatchTokens = tokens
}

func (c *Config) UpdateACPEnv(updates map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ACPPool.Env == nil {
		c.ACPPool.Env = make(map[string]string)
	}
	for k, v := range updates {
		if v == "" {
			delete(c.ACPPool.Env, k)
		} else {
			c.ACPPool.Env[k] = v
		}
	}
}

func (c *Config) GetSandboxPolicy() SandboxPolicyConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SandboxPolicy
}

func (c *Config) UpdateSandboxPolicy(sessionMode, internalApproval string) SandboxPolicyConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionMode != "" {
		c.SandboxPolicy.SessionMode = sessionMode
	}
	if internalApproval != "" {
		c.SandboxPolicy.InternalApproval = internalApproval
	}
	return c.SandboxPolicy
}

func IsDynamicCredentialValue(val string) bool {
	v := strings.TrimSpace(val)
	return v == "" || strings.EqualFold(v, "auto") || v == "ag-admin-secret-key"
}

func (c *Config) GetAdminToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Server.AdminToken
}

func (c *Config) SetAdminToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Server.AdminToken = token
}

func (c *Config) GetRawAdminPassword() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Server.AdminPassword
}

func (c *Config) GetAdminPassword() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.Server.AdminPassword != "" && !strings.EqualFold(c.Server.AdminPassword, "auto") {
		return c.Server.AdminPassword
	}
	return c.Server.AdminToken
}

func (c *Config) SetAdminPassword(pwd string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Server.AdminPassword = pwd
}

