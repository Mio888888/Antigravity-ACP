package storage_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"antigravity-gateway/internal/storage"
)

func TestStoreMultiAccountAPIKeysSkillsAndAuditLogs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	// 1. Verify default account is seeded automatically
	accounts, err := store.ListAccounts(false)
	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "default" {
		t.Fatalf("expected default account to be seeded, got: %+v", accounts)
	}

	// 2. Upsert isolated account with SOCKS5 proxy
	accIso, err := store.UpsertAccount(storage.Account{
		ID:               "acc_us_01",
		Name:             "US Residential Account 1",
		AuthMode:         "oauth_personal",
		IsolationEnabled: true,
		NetworkMode:      "proxy",
		ProxyURL:         "socks5://user:pass@127.0.0.1:1080",
		MaxWorkers:       4,
		Weight:           2,
		Enabled:          true,
		Status:           "ready",
	})
	if err != nil {
		t.Fatalf("UpsertAccount failed: %v", err)
	}
	if !accIso.IsolationEnabled || accIso.NetworkMode != "proxy" || accIso.ProxyURL != "socks5://user:pass@127.0.0.1:1080" {
		t.Fatalf("unexpected upserted account: %+v", accIso)
	}

	// Review Focus #1: Invalid network_mode or malformed proxy_url must be rejected
	if _, err := store.UpsertAccount(storage.Account{
		ID:          "acc_bad_net",
		Name:        "Bad Network Mode",
		NetworkMode: "invalid_mode",
	}); err == nil {
		t.Fatalf("expected error for invalid network_mode, got nil")
	}

	if _, err := store.UpsertAccount(storage.Account{
		ID:               "acc_bad_proxy",
		Name:             "Bad Proxy URL",
		IsolationEnabled: true,
		NetworkMode:      "proxy",
		ProxyURL:         "ftp://invalid-scheme:21",
	}); err == nil {
		t.Fatalf("expected error for unsupported proxy_url scheme, got nil")
	}

	// Update account status and egress IP
	if err := store.UpdateAccountStatus("acc_us_01", "ready", "198.51.100.42"); err != nil {
		t.Fatalf("UpdateAccountStatus failed: %v", err)
	}
	fetchedAcc, err := store.GetAccount("acc_us_01")
	if err != nil || fetchedAcc.LastEgressIP != "198.51.100.42" {
		t.Fatalf("GetAccount after status update failed: %+v, err=%v", fetchedAcc, err)
	}

	// 3. Create API Key bound to acc_us_01 with independent prompt & session policy
	plainKey, keyRec, err := store.CreateAPIKeyWithPolicy(storage.APIKey{
		Name:                "Cursor Laptop",
		BoundAccountID:      "acc_us_01",
		RateLimitRPM:        120,
		PromptMode:          "prepend_global",
		SessionMode:         "sticky_conversation",
		GlobalPrependPrompt: "Always reply in Chinese.",
		DisableBuiltinTools: true,
	})
	if err != nil {
		t.Fatalf("CreateAPIKeyWithPolicy failed: %v", err)
	}
	if plainKey == "" || keyRec.BoundAccountID != "acc_us_01" || keyRec.PromptMode != "prepend_global" || keyRec.SessionMode != "sticky_conversation" {
		t.Fatalf("unexpected created key: plain=%q rec=%+v", plainKey, keyRec)
	}
	validated, ok := store.ValidateAPIKey(plainKey)
	if !ok || validated.BoundAccountID != "acc_us_01" || validated.PromptMode != "prepend_global" || validated.GlobalPrependPrompt != "Always reply in Chinese." {
		t.Fatalf("ValidateAPIKey failed: ok=%v, validated=%+v", ok, validated)
	}
	updatedRec, err := store.UpdateAPIKey(keyRec.ID, storage.APIKey{
		Name:                "Cursor Laptop Pro",
		BoundAccountID:      "acc_us_01",
		RateLimitRPM:        180,
		PromptMode:          "pure_passthrough",
		SessionMode:         "yolo",
		GlobalPrependPrompt: "",
		DisableBuiltinTools: true,
	})
	if err != nil || updatedRec.PromptMode != "pure_passthrough" || updatedRec.SessionMode != "yolo" || updatedRec.RateLimitRPM != 180 {
		t.Fatalf("UpdateAPIKey failed: updatedRec=%+v, err=%v", updatedRec, err)
	}
	validatedAfterUpdate, ok := store.ValidateAPIKey(plainKey)
	if !ok || validatedAfterUpdate.PromptMode != "pure_passthrough" || validatedAfterUpdate.RateLimitRPM != 180 {
		t.Fatalf("ValidateAPIKey after UpdateAPIKey failed: ok=%v, rec=%+v", ok, validatedAfterUpdate)
	}
	if _, ok := store.ValidateAPIKey("sk-ag-invalid-key"); ok {
		t.Fatalf("expected invalid key to fail validation")
	}

	// 4. Upsert and list PresetSkill
	skill, err := store.UpsertSkill(storage.PresetSkill{
		Name:        "code-review-rule",
		Category:    "instruction",
		ContentJSON: `{"content":"Always check error handling"}`,
		Enabled:     true,
	})
	if err != nil || skill.ID == 0 {
		t.Fatalf("UpsertSkill failed: %+v, err=%v", skill, err)
	}
	skills, err := store.ListSkills(true)
	if err != nil || len(skills) != 1 {
		t.Fatalf("ListSkills failed: %+v, err=%v", skills, err)
	}

	// 5. Record and query AuditLog + OverviewStats + GetAccountUsageMap
	err = store.RecordAuditLog(storage.AuditLog{
		RequestID:        "req_1001",
		Protocol:         "claude",
		AccountID:        "acc_us_01",
		SessionID:        "sess_abc",
		WorkerPID:        12345,
		ModelName:        "claude-sonnet-4-5",
		PromptTokens:     120,
		CompletionTokens: 380,
		LatencyMs:        420,
		StatusCode:       200,
	})
	if err != nil {
		t.Fatalf("RecordAuditLog failed: %v", err)
	}

	logs, err := store.ListAuditLogs(10)
	if err != nil || len(logs) != 1 || logs[0].AccountID != "acc_us_01" || logs[0].TotalTokens != 500 {
		t.Fatalf("ListAuditLogs failed: %+v, err=%v", logs, err)
	}

	usageMap, err := store.GetAccountUsageMap()
	if err != nil {
		t.Fatalf("GetAccountUsageMap failed: %v", err)
	}
	u := usageMap["acc_us_01"]
	if u.TotalRequests != 1 || u.PromptTokens != 120 || u.CompletionTokens != 380 || u.TotalTokens != 500 || u.Tokens24h != 500 {
		t.Fatalf("unexpected account usage stats for acc_us_01: %+v", u)
	}

	stats, err := store.GetOverviewStats()
	if err != nil || stats.TotalAccounts != 2 || stats.TotalRequests != 1 || stats.SuccessRequests != 1 || stats.TotalTokens != 500 {
		t.Fatalf("GetOverviewStats unexpected: %+v, err=%v", stats, err)
	}

	// 6. UpdateAccountDisabledModels & IsModelDisabled
	accWithDisabled, err := store.UpdateAccountDisabledModels("acc_us_01", []string{"claude-opus-4-6-thinking", "gemini-2.5-pro"})
	if err != nil || len(accWithDisabled.DisabledModels) != 2 {
		t.Fatalf("UpdateAccountDisabledModels failed: %+v, err=%v", accWithDisabled, err)
	}
	if !accWithDisabled.IsModelDisabled("claude-opus-4-6-thinking") || !accWithDisabled.IsModelDisabled("claude-opus-4-6-thinking-high") {
		t.Fatalf("expected claude-opus-4-6-thinking to be marked disabled")
	}
	if accWithDisabled.IsModelDisabled("claude-sonnet-4-6") {
		t.Fatalf("did not expect claude-sonnet-4-6 to be marked disabled")
	}

	// 7. Oversized ToolCallsJSON (>8KB) must be truncated into valid JSON
	bigArgs := `{"content":"` + strings.Repeat("代码块内容ABC", 1500) + `"}`
	bigToolJSON, _ := json.Marshal(map[string]string{
		"call_id":   "call_big_1",
		"tool_name": "edit_file",
		"arguments": bigArgs,
	})
	if err := store.RecordAuditLog(storage.AuditLog{
		RequestID:        "req_big_json",
		Protocol:         "openai_responses",
		AccountID:        "acc_us_01",
		SessionID:        "sess_abc",
		ModelName:        "gemini-4-argon",
		PromptTokens:     500,
		CompletionTokens: 50,
		LatencyMs:        200,
		StatusCode:       200,
		ToolCallsJSON:    string(bigToolJSON),
	}); err != nil {
		t.Fatalf("RecordAuditLog(req_big_json) failed: %v", err)
	}
	gotBig, err := store.GetAuditLog("req_big_json")
	if err != nil {
		t.Fatalf("GetAuditLog(req_big_json) failed: %v", err)
	}
	if len(gotBig.ToolCallsJSON) > 8192 || !json.Valid([]byte(gotBig.ToolCallsJSON)) {
		t.Fatalf("expected truncated ToolCallsJSON to be <=8192 bytes and valid JSON, len=%d valid=%v", len(gotBig.ToolCallsJSON), json.Valid([]byte(gotBig.ToolCallsJSON)))
	}
}
