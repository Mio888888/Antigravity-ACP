package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"antigravity-gateway/internal/config"
)

func TestLoadAndPromptPolicyValidation(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `
server:
  host: "127.0.0.1"
  port: 9090
  public_base_url: "https://gateway.example.com"
  data_dir: "./data"
  admin_token: "test-token"
  cors_allowed_origins: ["*"]
  sse_keepalive_sec: 15
  read_timeout_sec: 300
  write_timeout_sec: 300
acp_pool:
  binary_path: "/usr/local/bin/agy"
  workspace_dir: "/tmp/ag-ws"
  client_name: "zed"
  client_version: "0.175.0"
  process_per_session: true
  disable_telemetry: true
  min_idle_workers: 1
  max_workers: 4
  max_idle_time_sec: 600
  tool_park_timeout_sec: 120
prompt_policy:
  mode: "pure_passthrough"
  disable_builtin_tools: true
  global_prepend_prompt: "Always respond concisely."
sandbox_policy:
  session_mode: "yolo"
  internal_approval: "auto_approve"
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}

	if cfg.Server.Port != 9090 || cfg.Server.PublicBaseURL != "https://gateway.example.com" {
		t.Fatalf("unexpected server config: %+v", cfg.Server)
	}
	if cfg.ACPPool.ClientName != "zed" || !cfg.ACPPool.ProcessPerSession {
		t.Fatalf("unexpected acp_pool config: %+v", cfg.ACPPool)
	}

	p := cfg.GetPromptPolicy()
	if p.Mode != "pure_passthrough" || !p.DisableBuiltinTools {
		t.Fatalf("unexpected prompt policy: %+v", p)
	}

	// Valid update
	err = cfg.UpdatePromptPolicy(config.PromptPolicyConfig{
		Mode:                "prepend_global",
		DisableBuiltinTools: false,
		GlobalPrependPrompt: "Custom global prompt",
	})
	if err != nil {
		t.Fatalf("expected valid prompt policy update to succeed, got: %v", err)
	}
	if got := cfg.GetPromptPolicy(); got.Mode != "prepend_global" || got.DisableBuiltinTools {
		t.Fatalf("updated prompt policy mismatch: %+v", got)
	}

	// Review Focus #1: Invalid prompt policy mode must be rejected cleanly
	err = cfg.UpdatePromptPolicy(config.PromptPolicyConfig{
		Mode: "invalid_mode_xyz",
	})
	if err == nil {
		t.Fatalf("expected error for invalid prompt policy mode, got nil")
	}
}
