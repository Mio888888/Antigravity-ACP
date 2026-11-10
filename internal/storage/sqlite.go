package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

var accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type Store struct {
	db            *sql.DB
	cacheMu       sync.RWMutex
	apiKeysByHash map[string]APIKey
	apiKeysLoaded bool
	enabledSkills []PresetSkill
	skillsLoaded  bool
	statsCache    *OverviewStats
	statsCachedAt time.Time
	usageCache    map[string]AccountUsageStats
	usageCachedAt time.Time
	settingsCache map[string]string
	settingsKnown map[string]bool
	auditWriteSeq atomic.Uint64
}

func (s *Store) invalidateTelemetryCache() {
	s.cacheMu.Lock()
	s.statsCache = nil
	s.usageCache = nil
	s.cacheMu.Unlock()
}

func NewStore(dsn string) (*Store, error) {
	if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
		if dir := filepath.Dir(dsn); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("create db dir: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dsn, err)
	}
	db.SetMaxOpenConns(1)
	_, _ = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;`)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	schema := `
CREATE TABLE IF NOT EXISTS accounts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    auth_mode TEXT NOT NULL DEFAULT 'oauth_personal',
    api_key TEXT DEFAULT '',
    gcp_project TEXT DEFAULT '',
    gcp_location TEXT DEFAULT '',
    isolation_enabled BOOLEAN DEFAULT 0,
    network_mode TEXT DEFAULT 'direct',
    proxy_url TEXT DEFAULT '',
    bind_ip TEXT DEFAULT '',
    netns_name TEXT DEFAULT '',
    max_workers INTEGER DEFAULT 3,
    weight INTEGER DEFAULT 1,
    enabled BOOLEAN DEFAULT 1,
    status TEXT DEFAULT 'ready',
    last_egress_ip TEXT DEFAULT '',
    disabled_models TEXT DEFAULT '[]',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key_hash TEXT UNIQUE NOT NULL,
    key_prefix TEXT NOT NULL,
    name TEXT NOT NULL,
    bound_account_id TEXT DEFAULT '',
    rate_limit_rpm INTEGER DEFAULT 60,
    prompt_mode TEXT DEFAULT 'native_hybrid',
    session_mode TEXT DEFAULT 'yolo',
    global_prepend_prompt TEXT DEFAULT '',
    disable_builtin_tools BOOLEAN DEFAULT 1,
    enabled BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS preset_skills (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    category TEXT NOT NULL,
    content_json TEXT NOT NULL,
    enabled BOOLEAN DEFAULT 1,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS gateway_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_logs (
    request_id TEXT PRIMARY KEY,
    protocol TEXT NOT NULL,
    account_id TEXT DEFAULT 'default',
    session_id TEXT,
    worker_pid INTEGER,
    model_name TEXT,
    thinking_level TEXT DEFAULT '',
    prompt_tokens INTEGER DEFAULT 0,
    cached_tokens INTEGER DEFAULT 0,
    completion_tokens INTEGER DEFAULT 0,
    total_tokens INTEGER DEFAULT 0,
    latency_ms INTEGER,
    status_code INTEGER,
    error_msg TEXT,
    system_prompt TEXT DEFAULT '',
    prompt_text TEXT DEFAULT '',
    thinking_text TEXT DEFAULT '',
    response_text TEXT DEFAULT '',
    tool_calls_json TEXT DEFAULT '',
    context_json TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate sqlite schema: %w", err)
	}

	// Safe column migrations for existing databases
	_, _ = s.db.Exec(`ALTER TABLE accounts ADD COLUMN disabled_models TEXT DEFAULT '[]'`)
	_, _ = s.db.Exec(`ALTER TABLE api_keys ADD COLUMN prompt_mode TEXT DEFAULT 'native_hybrid'`)
	_, _ = s.db.Exec(`ALTER TABLE api_keys ADD COLUMN session_mode TEXT DEFAULT 'yolo'`)
	_, _ = s.db.Exec(`ALTER TABLE api_keys ADD COLUMN global_prepend_prompt TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE api_keys ADD COLUMN disable_builtin_tools BOOLEAN DEFAULT 1`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN prompt_tokens INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN cached_tokens INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN completion_tokens INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN total_tokens INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN thinking_level TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN system_prompt TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN prompt_text TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN thinking_text TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN response_text TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN tool_calls_json TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE audit_logs ADD COLUMN context_json TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at DESC)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_logs_account_created ON audit_logs(account_id, created_at)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_logs_session_id ON audit_logs(session_id, created_at)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_logs_telemetry ON audit_logs(account_id, created_at, status_code, model_name, prompt_tokens, cached_tokens, completion_tokens, total_tokens, latency_ms)`)
	_, _ = s.db.Exec(`DROP TRIGGER IF EXISTS trg_audit_logs_autofill_cache`)
	_, _ = s.db.Exec(`DROP TRIGGER IF EXISTS trg_audit_logs_autofill_session_meta`)

	// Ensure default account exists
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = 'default'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := s.db.Exec(`
INSERT INTO accounts (id, name, auth_mode, isolation_enabled, network_mode, max_workers, weight, enabled, status)
VALUES ('default', 'Default Host Account', 'oauth_personal', 0, 'direct', 5, 1, 1, 'ready')
`)
		if err != nil {
			return fmt.Errorf("seed default account: %w", err)
		}
	}
	return nil
}

func ValidateAccount(acc *Account) error {
	acc.ID = strings.TrimSpace(acc.ID)
	acc.Name = strings.TrimSpace(acc.Name)
	if acc.ID == "" {
		return fmt.Errorf("account id is required")
	}
	if !accountIDPattern.MatchString(acc.ID) || acc.ID == "." || acc.ID == ".." {
		return fmt.Errorf("invalid account id %q (allowed: letters, digits, '.', '_', '-', 1-64 chars)", acc.ID)
	}
	if acc.Name == "" {
		acc.Name = acc.ID
	}
	if acc.AuthMode == "" {
		acc.AuthMode = "oauth_personal"
	}
	switch acc.AuthMode {
	case "oauth_personal", "oauth_business", "gemini_api_key", "agent_platform":
	default:
		return fmt.Errorf("invalid auth_mode %q", acc.AuthMode)
	}

	if acc.NetworkMode == "" {
		acc.NetworkMode = "direct"
	}
	switch acc.NetworkMode {
	case "direct":
	case "proxy":
		if strings.TrimSpace(acc.ProxyURL) == "" {
			return fmt.Errorf("proxy_url is required when network_mode is proxy")
		}
		u, err := url.Parse(strings.TrimSpace(acc.ProxyURL))
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid proxy_url %q", acc.ProxyURL)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("unsupported proxy_url scheme %q (must be http, https, or socks5)", u.Scheme)
		}
	case "bind_ip":
		if strings.TrimSpace(acc.BindIP) == "" || net.ParseIP(strings.TrimSpace(acc.BindIP)) == nil {
			return fmt.Errorf("valid bind_ip is required when network_mode is bind_ip")
		}
	case "netns":
		if strings.TrimSpace(acc.NetNSName) == "" {
			return fmt.Errorf("netns_name is required when network_mode is netns")
		}
	default:
		return fmt.Errorf("invalid network_mode %q (must be direct, proxy, bind_ip, or netns)", acc.NetworkMode)
	}

	if acc.MaxWorkers <= 0 {
		acc.MaxWorkers = 3
	}
	if acc.Weight <= 0 {
		acc.Weight = 1
	}
	if acc.Status == "" {
		acc.Status = "ready"
	}
	acc.DisabledModels = normalizeDisabledModels(acc.DisabledModels)
	return nil
}

func normalizeDisabledModels(list []string) []string {
	if len(list) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, item := range list {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func marshalDisabledModels(list []string) string {
	normalized := normalizeDisabledModels(list)
	raw, err := json.Marshal(normalized)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func unmarshalDisabledModels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return []string{}
	}
	return normalizeDisabledModels(list)
}

func (s *Store) UpsertAccount(acc Account) (*Account, error) {
	acc.ID = strings.TrimSpace(acc.ID)
	if existing, err := s.GetAccount(acc.ID); err == nil && existing != nil {
		if acc.DisabledModels == nil {
			acc.DisabledModels = existing.DisabledModels
		}
		acc.ProxyURL = RestoreMaskedProxyURL(acc.ProxyURL, existing.ProxyURL)
	}
	if err := ValidateAccount(&acc); err != nil {
		return nil, err
	}
	disabledJSON := marshalDisabledModels(acc.DisabledModels)
	now := time.Now().UTC()
	_, err := s.db.Exec(`
INSERT INTO accounts (
	id, name, auth_mode, api_key, gcp_project, gcp_location,
	isolation_enabled, network_mode, proxy_url, bind_ip, netns_name,
	max_workers, weight, enabled, status, last_egress_ip, disabled_models, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	name = excluded.name,
	auth_mode = excluded.auth_mode,
	api_key = CASE WHEN excluded.api_key != '' THEN excluded.api_key ELSE accounts.api_key END,
	gcp_project = excluded.gcp_project,
	gcp_location = excluded.gcp_location,
	isolation_enabled = excluded.isolation_enabled,
	network_mode = excluded.network_mode,
	proxy_url = excluded.proxy_url,
	bind_ip = excluded.bind_ip,
	netns_name = excluded.netns_name,
	max_workers = excluded.max_workers,
	weight = excluded.weight,
	enabled = excluded.enabled,
	status = excluded.status,
	last_egress_ip = CASE WHEN excluded.last_egress_ip != '' THEN excluded.last_egress_ip ELSE accounts.last_egress_ip END,
	disabled_models = excluded.disabled_models,
	updated_at = excluded.updated_at
`,
		acc.ID, acc.Name, acc.AuthMode, acc.APIKey, acc.GCPProject, acc.GCPLocation,
		acc.IsolationEnabled, acc.NetworkMode, acc.ProxyURL, acc.BindIP, acc.NetNSName,
		acc.MaxWorkers, acc.Weight, acc.Enabled, acc.Status, acc.LastEgressIP, disabledJSON, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert account %s: %w", acc.ID, err)
	}
	s.invalidateTelemetryCache()
	return s.GetAccount(acc.ID)
}

func (s *Store) UpdateAccountDisabledModels(id string, disabledModels []string) (*Account, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("account id is required")
	}
	disabledJSON := marshalDisabledModels(disabledModels)
	now := time.Now().UTC()
	res, err := s.db.Exec(`UPDATE accounts SET disabled_models = ?, updated_at = ? WHERE id = ?`, disabledJSON, now, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("account %q not found", id)
	}
	return s.GetAccount(id)
}

func (s *Store) GetAccount(id string) (*Account, error) {
	row := s.db.QueryRow(`
SELECT id, name, auth_mode, api_key, gcp_project, gcp_location,
       isolation_enabled, network_mode, proxy_url, bind_ip, netns_name,
       max_workers, weight, enabled, status, last_egress_ip, COALESCE(disabled_models, '[]'), created_at, updated_at
FROM accounts WHERE id = ?`, id)
	var acc Account
	var disabledRaw string
	err := row.Scan(
		&acc.ID, &acc.Name, &acc.AuthMode, &acc.APIKey, &acc.GCPProject, &acc.GCPLocation,
		&acc.IsolationEnabled, &acc.NetworkMode, &acc.ProxyURL, &acc.BindIP, &acc.NetNSName,
		&acc.MaxWorkers, &acc.Weight, &acc.Enabled, &acc.Status, &acc.LastEgressIP, &disabledRaw, &acc.CreatedAt, &acc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	acc.DisabledModels = unmarshalDisabledModels(disabledRaw)
	return &acc, nil
}

func (s *Store) ListAccounts(onlyEnabled bool) ([]Account, error) {
	q := `
SELECT id, name, auth_mode, api_key, gcp_project, gcp_location,
       isolation_enabled, network_mode, proxy_url, bind_ip, netns_name,
       max_workers, weight, enabled, status, last_egress_ip, COALESCE(disabled_models, '[]'), created_at, updated_at
FROM accounts`
	if onlyEnabled {
		q += ` WHERE enabled = 1`
	}
	q += ` ORDER BY CASE WHEN id = 'default' THEN 0 ELSE 1 END, created_at ASC`

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Account
	for rows.Next() {
		var acc Account
		var disabledRaw string
		if err := rows.Scan(
			&acc.ID, &acc.Name, &acc.AuthMode, &acc.APIKey, &acc.GCPProject, &acc.GCPLocation,
			&acc.IsolationEnabled, &acc.NetworkMode, &acc.ProxyURL, &acc.BindIP, &acc.NetNSName,
			&acc.MaxWorkers, &acc.Weight, &acc.Enabled, &acc.Status, &acc.LastEgressIP, &disabledRaw, &acc.CreatedAt, &acc.UpdatedAt,
		); err != nil {
			return nil, err
		}
		acc.DisabledModels = unmarshalDisabledModels(disabledRaw)
		list = append(list, acc)
	}
	return list, rows.Err()
}

func (s *Store) DeleteAccount(id string) error {
	if id == "default" {
		return fmt.Errorf("cannot delete default account")
	}
	_, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	s.invalidateTelemetryCache()
	return err
}

func (s *Store) UpdateAccountStatus(id, status, egressIP string) error {
	now := time.Now().UTC()
	var err error
	if egressIP != "" {
		_, err = s.db.Exec(`UPDATE accounts SET status = ?, last_egress_ip = ?, updated_at = ? WHERE id = ?`, status, egressIP, now, id)
	} else {
		_, err = s.db.Exec(`UPDATE accounts SET status = ?, updated_at = ? WHERE id = ?`, status, now, id)
	}
	s.invalidateTelemetryCache()
	return err
}

func hashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func normalizeAPIKeyFields(rec *APIKey) {
	rec.Name = strings.TrimSpace(rec.Name)
	if rec.Name == "" {
		rec.Name = "Unnamed Client Key"
	}
	rec.BoundAccountID = strings.TrimSpace(rec.BoundAccountID)
	if rec.RateLimitRPM <= 0 {
		rec.RateLimitRPM = 60
	}
	switch strings.TrimSpace(rec.PromptMode) {
	case "pure_passthrough", "prepend_global", "native_hybrid":
		rec.PromptMode = strings.TrimSpace(rec.PromptMode)
	default:
		rec.PromptMode = "native_hybrid"
	}
	switch strings.TrimSpace(rec.SessionMode) {
	case "yolo", "stateless_single_turn", "sticky_conversation":
		rec.SessionMode = strings.TrimSpace(rec.SessionMode)
	default:
		rec.SessionMode = "yolo"
	}
}

func (s *Store) CreateAPIKey(name string, boundAccountID string, rpm int) (string, *APIKey, error) {
	return s.CreateAPIKeyWithPolicy(APIKey{
		Name:                name,
		BoundAccountID:      boundAccountID,
		RateLimitRPM:        rpm,
		PromptMode:          "native_hybrid",
		SessionMode:         "yolo",
		GlobalPrependPrompt: "",
		DisableBuiltinTools: true,
	})
}

func (s *Store) CreateAPIKeyWithPolicy(req APIKey) (string, *APIKey, error) {
	normalizeAPIKeyFields(&req)
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	plainKey := "sk-ag-" + hex.EncodeToString(buf)
	prefix := plainKey[:12] + "..."
	keyHash := hashKey(plainKey)
	now := time.Now().UTC()

	res, err := s.db.Exec(`
INSERT INTO api_keys (
	key_hash, key_prefix, name, bound_account_id, rate_limit_rpm,
	prompt_mode, session_mode, global_prepend_prompt, disable_builtin_tools,
	enabled, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		keyHash, prefix, req.Name, req.BoundAccountID, req.RateLimitRPM,
		req.PromptMode, req.SessionMode, req.GlobalPrependPrompt, req.DisableBuiltinTools,
		now,
	)
	if err != nil {
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	rec := &APIKey{
		ID:                  id,
		KeyHash:             keyHash,
		KeyPrefix:           prefix,
		Name:                req.Name,
		BoundAccountID:      req.BoundAccountID,
		RateLimitRPM:        req.RateLimitRPM,
		PromptMode:          req.PromptMode,
		SessionMode:         req.SessionMode,
		GlobalPrependPrompt: req.GlobalPrependPrompt,
		DisableBuiltinTools: req.DisableBuiltinTools,
		Enabled:             true,
		CreatedAt:           now,
	}
	s.cacheMu.Lock()
	if s.apiKeysByHash != nil {
		s.apiKeysByHash[keyHash] = *rec
	}
	s.cacheMu.Unlock()
	return plainKey, rec, nil
}

func (s *Store) UpdateAPIKey(id int64, req APIKey) (*APIKey, error) {
	normalizeAPIKeyFields(&req)
	res, err := s.db.Exec(`
UPDATE api_keys
SET name = ?,
    bound_account_id = ?,
    rate_limit_rpm = ?,
    prompt_mode = ?,
    session_mode = ?,
    global_prepend_prompt = ?,
    disable_builtin_tools = ?
WHERE id = ?`,
		req.Name, req.BoundAccountID, req.RateLimitRPM,
		req.PromptMode, req.SessionMode, req.GlobalPrependPrompt, req.DisableBuiltinTools,
		id,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("api key %d not found", id)
	}
	s.cacheMu.Lock()
	s.apiKeysLoaded = false
	s.apiKeysByHash = nil
	s.cacheMu.Unlock()

	row := s.db.QueryRow(`
SELECT id, key_hash, key_prefix, name, bound_account_id, rate_limit_rpm,
       COALESCE(prompt_mode, 'native_hybrid'),
       COALESCE(session_mode, 'yolo'),
       COALESCE(global_prepend_prompt, ''),
       COALESCE(disable_builtin_tools, 1),
       enabled, created_at
FROM api_keys WHERE id = ?`, id)
	var rec APIKey
	if err := row.Scan(
		&rec.ID, &rec.KeyHash, &rec.KeyPrefix, &rec.Name, &rec.BoundAccountID, &rec.RateLimitRPM,
		&rec.PromptMode, &rec.SessionMode, &rec.GlobalPrependPrompt, &rec.DisableBuiltinTools,
		&rec.Enabled, &rec.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) ensureAPIKeysCacheLoaded() {
	s.cacheMu.RLock()
	if s.apiKeysLoaded {
		s.cacheMu.RUnlock()
		return
	}
	s.cacheMu.RUnlock()

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.apiKeysLoaded {
		return
	}
	m := make(map[string]APIKey)
	rows, err := s.db.Query(`
SELECT id, key_hash, key_prefix, name, bound_account_id, rate_limit_rpm,
       COALESCE(prompt_mode, 'native_hybrid'),
       COALESCE(session_mode, 'yolo'),
       COALESCE(global_prepend_prompt, ''),
       COALESCE(disable_builtin_tools, 1),
       enabled, created_at
FROM api_keys WHERE enabled = 1`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var rec APIKey
		if rows.Scan(
			&rec.ID, &rec.KeyHash, &rec.KeyPrefix, &rec.Name, &rec.BoundAccountID, &rec.RateLimitRPM,
			&rec.PromptMode, &rec.SessionMode, &rec.GlobalPrependPrompt, &rec.DisableBuiltinTools,
			&rec.Enabled, &rec.CreatedAt,
		) == nil {
			m[rec.KeyHash] = rec
		}
	}
	if rows.Err() != nil {
		return
	}
	s.apiKeysByHash = m
	s.apiKeysLoaded = true
}

// HasAnyAPIKeys reports whether at least one enabled API key exists. It fails
// closed (returns true) if the key table could not be read.
func (s *Store) HasAnyAPIKeys() bool {
	s.ensureAPIKeysCacheLoaded()
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	if !s.apiKeysLoaded {
		return true
	}
	return len(s.apiKeysByHash) > 0
}

func (s *Store) GetPrimaryAPIKey() (*APIKey, bool) {
	s.ensureAPIKeysCacheLoaded()
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	var best *APIKey
	for _, rec := range s.apiKeysByHash {
		if !rec.Enabled {
			continue
		}
		if best == nil || rec.ID > best.ID {
			cp := rec
			best = &cp
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}

func (s *Store) ValidateAPIKey(plainKey string) (*APIKey, bool) {
	plainKey = strings.TrimSpace(plainKey)
	if plainKey == "" {
		return nil, false
	}
	keyHash := hashKey(plainKey)
	s.ensureAPIKeysCacheLoaded()
	s.cacheMu.RLock()
	if s.apiKeysLoaded {
		rec, ok := s.apiKeysByHash[keyHash]
		s.cacheMu.RUnlock()
		if !ok {
			return nil, false
		}
		cp := rec
		return &cp, true
	}
	s.cacheMu.RUnlock()

	row := s.db.QueryRow(`
SELECT id, key_hash, key_prefix, name, bound_account_id, rate_limit_rpm,
       COALESCE(prompt_mode, 'native_hybrid'),
       COALESCE(session_mode, 'yolo'),
       COALESCE(global_prepend_prompt, ''),
       COALESCE(disable_builtin_tools, 1),
       enabled, created_at
FROM api_keys WHERE key_hash = ? AND enabled = 1`, keyHash)

	var rec APIKey
	if err := row.Scan(
		&rec.ID, &rec.KeyHash, &rec.KeyPrefix, &rec.Name, &rec.BoundAccountID, &rec.RateLimitRPM,
		&rec.PromptMode, &rec.SessionMode, &rec.GlobalPrependPrompt, &rec.DisableBuiltinTools,
		&rec.Enabled, &rec.CreatedAt,
	); err != nil {
		return nil, false
	}
	return &rec, true
}

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.db.Query(`
SELECT id, key_hash, key_prefix, name, bound_account_id, rate_limit_rpm,
       COALESCE(prompt_mode, 'native_hybrid'),
       COALESCE(session_mode, 'yolo'),
       COALESCE(global_prepend_prompt, ''),
       COALESCE(disable_builtin_tools, 1),
       enabled, created_at
FROM api_keys ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []APIKey
	for rows.Next() {
		var rec APIKey
		if err := rows.Scan(
			&rec.ID, &rec.KeyHash, &rec.KeyPrefix, &rec.Name, &rec.BoundAccountID, &rec.RateLimitRPM,
			&rec.PromptMode, &rec.SessionMode, &rec.GlobalPrependPrompt, &rec.DisableBuiltinTools,
			&rec.Enabled, &rec.CreatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	return list, rows.Err()
}

func (s *Store) DeleteAPIKey(id int64) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	s.cacheMu.Lock()
	s.apiKeysLoaded = false
	s.apiKeysByHash = nil
	s.cacheMu.Unlock()
	return err
}

func (s *Store) UpsertSkill(skill PresetSkill) (*PresetSkill, error) {
	skill.Name = strings.TrimSpace(skill.Name)
	if skill.Name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	if skill.Category == "" {
		skill.Category = "instruction"
	}
	now := time.Now().UTC()
	if skill.ID > 0 {
		res, err := s.db.Exec(`
UPDATE preset_skills
SET name = ?, category = ?, content_json = ?, enabled = ?, updated_at = ?
WHERE id = ?`, skill.Name, skill.Category, skill.ContentJSON, skill.Enabled, now, skill.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			s.cacheMu.Lock()
			s.skillsLoaded = false
			s.enabledSkills = nil
			s.cacheMu.Unlock()

			row := s.db.QueryRow(`SELECT id, name, category, content_json, enabled, updated_at FROM preset_skills WHERE id = ?`, skill.ID)
			var out PresetSkill
			if err := row.Scan(&out.ID, &out.Name, &out.Category, &out.ContentJSON, &out.Enabled, &out.UpdatedAt); err != nil {
				return nil, err
			}
			return &out, nil
		}
	}
	_, err := s.db.Exec(`
INSERT INTO preset_skills (name, category, content_json, enabled, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
	category = excluded.category,
	content_json = excluded.content_json,
	enabled = excluded.enabled,
	updated_at = excluded.updated_at
`, skill.Name, skill.Category, skill.ContentJSON, skill.Enabled, now)
	if err != nil {
		return nil, err
	}

	s.cacheMu.Lock()
	s.skillsLoaded = false
	s.enabledSkills = nil
	s.cacheMu.Unlock()

	row := s.db.QueryRow(`SELECT id, name, category, content_json, enabled, updated_at FROM preset_skills WHERE name = ?`, skill.Name)
	var out PresetSkill
	if err := row.Scan(&out.ID, &out.Name, &out.Category, &out.ContentJSON, &out.Enabled, &out.UpdatedAt); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Store) ListSkills(onlyEnabled bool) ([]PresetSkill, error) {
	if onlyEnabled {
		s.cacheMu.RLock()
		if s.skillsLoaded {
			cp := make([]PresetSkill, len(s.enabledSkills))
			copy(cp, s.enabledSkills)
			s.cacheMu.RUnlock()
			return cp, nil
		}
		s.cacheMu.RUnlock()
	}

	q := `SELECT id, name, category, content_json, enabled, updated_at FROM preset_skills`
	if onlyEnabled {
		q += ` WHERE enabled = 1`
	}
	q += ` ORDER BY id ASC`

	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []PresetSkill
	for rows.Next() {
		var sk PresetSkill
		if err := rows.Scan(&sk.ID, &sk.Name, &sk.Category, &sk.ContentJSON, &sk.Enabled, &sk.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, sk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if onlyEnabled {
		s.cacheMu.Lock()
		cp := make([]PresetSkill, len(list))
		copy(cp, list)
		s.enabledSkills = cp
		s.skillsLoaded = true
		s.cacheMu.Unlock()
	}
	return list, nil
}

func (s *Store) DeleteSkill(id int64) error {
	_, err := s.db.Exec(`DELETE FROM preset_skills WHERE id = ?`, id)
	s.cacheMu.Lock()
	s.skillsLoaded = false
	s.enabledSkills = nil
	s.cacheMu.Unlock()
	return err
}

func truncateAuditText(str string, maxBytes int) string {
	if len(str) <= maxBytes {
		return str
	}
	for maxBytes > 0 && !utf8.ValidString(str[:maxBytes]) {
		maxBytes--
	}
	return str[:maxBytes] + "\n...[truncated]"
}

func truncateUTF8Prefix(str string, maxBytes int) string {
	if len(str) <= maxBytes {
		return str
	}
	for maxBytes > 0 && !utf8.ValidString(str[:maxBytes]) {
		maxBytes--
	}
	return str[:maxBytes] + "...[truncated]"
}

// truncateAuditToolCallsJSON bounds tool_calls_json to maxBytes while preserving valid JSON syntax.
func truncateAuditToolCallsJSON(raw string, maxBytes int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) <= maxBytes {
		return trimmed
	}
	var single map[string]any
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil && len(single) > 0 {
		if args, ok := single["arguments"].(string); ok && len(args) > 256 {
			budget := maxBytes - 256
			if budget < 128 {
				budget = 128
			}
			single["arguments"] = truncateUTF8Prefix(args, budget)
			if out, err := json.Marshal(single); err == nil && len(out) <= maxBytes {
				return string(out)
			}
		}
		single["arguments"] = "{}"
		single["truncated"] = true
		if out, err := json.Marshal(single); err == nil {
			return string(out)
		}
	}

	var multi []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &multi); err == nil && len(multi) > 0 {
		perItemBudget := (maxBytes - 128) / len(multi)
		if perItemBudget < 96 {
			maxItems := maxBytes / 160
			if maxItems < 1 {
				maxItems = 1
			}
			if len(multi) > maxItems {
				multi = multi[:maxItems]
			}
			perItemBudget = (maxBytes - 128) / len(multi)
		}
		argBudget := perItemBudget - 120
		if argBudget < 32 {
			argBudget = 32
		}
		for i := range multi {
			for _, k := range []string{"arguments", "ArgsJSON"} {
				if args, ok := multi[i][k].(string); ok && len(args) > argBudget {
					multi[i][k] = truncateUTF8Prefix(args, argBudget)
				}
			}
		}
		if out, err := json.Marshal(multi); err == nil && len(out) <= maxBytes {
			return string(out)
		}
	}

	fallback, _ := json.Marshal(map[string]any{
		"truncated": true,
		"preview":   truncateUTF8Prefix(trimmed, maxBytes/2),
	})
	return string(fallback)
}

func (s *Store) RecordAuditLog(log AuditLog) error {
	if log.AccountID == "" {
		log.AccountID = "default"
	}
	if log.TotalTokens == 0 && (log.PromptTokens > 0 || log.CompletionTokens > 0) {
		log.TotalTokens = log.PromptTokens + log.CompletionTokens
	}
	now := log.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	_, err := s.db.Exec(`
INSERT OR REPLACE INTO audit_logs (
	request_id, protocol, account_id, session_id, worker_pid, model_name, thinking_level,
	prompt_tokens, cached_tokens, completion_tokens, total_tokens, latency_ms, status_code, error_msg,
	system_prompt, prompt_text, thinking_text, response_text, tool_calls_json, context_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.RequestID, log.Protocol, log.AccountID, log.SessionID, log.WorkerPID, log.ModelName, log.ThinkingLevel,
		log.PromptTokens, log.CachedTokens, log.CompletionTokens, log.TotalTokens, log.LatencyMs, log.StatusCode, log.ErrorMsg,
		truncateAuditText(log.SystemPrompt, 4096),
		truncateAuditText(log.PromptText, 8192),
		truncateAuditText(log.ThinkingText, 8192),
		truncateAuditText(log.ResponseText, 8192),
		truncateAuditToolCallsJSON(log.ToolCallsJSON, 8192),
		truncateAuditText(log.ContextJSON, 131072),
		now,
	)
	if err == nil {
		s.invalidateTelemetryCache()
		if s.auditWriteSeq.Add(1)%256 == 0 {
			_, _ = s.db.Exec(`DELETE FROM audit_logs WHERE created_at < (SELECT created_at FROM audit_logs ORDER BY created_at DESC LIMIT 1 OFFSET 10000)`)
		}
	}
	return err
}

func (s *Store) UpdateAuditLogTokens(requestID string, promptTokens, cachedTokens, completionTokens int64, contextJSON string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	totalTokens := promptTokens + completionTokens
	var err error
	if strings.TrimSpace(contextJSON) != "" {
		_, err = s.db.Exec(`
UPDATE audit_logs
SET prompt_tokens = ?, cached_tokens = ?, completion_tokens = ?, total_tokens = ?, context_json = ?
WHERE request_id = ?`,
			promptTokens, cachedTokens, completionTokens, totalTokens, truncateAuditText(contextJSON, 131072), requestID,
		)
	} else {
		_, err = s.db.Exec(`
UPDATE audit_logs
SET prompt_tokens = ?, cached_tokens = ?, completion_tokens = ?, total_tokens = ?,
    context_json = CASE
        WHEN context_json != '' AND json_valid(context_json) THEN
            json_set(context_json, '$.input_tokens', ?, '$.cached_tokens', ?, '$.output_tokens', ?)
        ELSE context_json
    END
WHERE request_id = ?`,
			promptTokens, cachedTokens, completionTokens, totalTokens,
			promptTokens, cachedTokens, completionTokens, requestID,
		)
	}
	if err == nil {
		s.invalidateTelemetryCache()
	}
	return err
}

func (s *Store) GetAuditLog(requestID string) (*AuditLog, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("request_id is required")
	}
	row := s.db.QueryRow(`
SELECT request_id, protocol, account_id, session_id, worker_pid, model_name,
       COALESCE(thinking_level, ''),
       COALESCE(prompt_tokens, 0), COALESCE(cached_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0),
       latency_ms, status_code, error_msg,
       COALESCE(system_prompt, ''), COALESCE(prompt_text, ''),
       COALESCE(thinking_text, ''), COALESCE(response_text, ''), COALESCE(tool_calls_json, ''),
       COALESCE(context_json, ''),
       created_at
FROM audit_logs WHERE request_id = ?`, requestID)
	var l AuditLog
	if err := row.Scan(
		&l.RequestID, &l.Protocol, &l.AccountID, &l.SessionID, &l.WorkerPID, &l.ModelName,
		&l.ThinkingLevel,
		&l.PromptTokens, &l.CachedTokens, &l.CompletionTokens, &l.TotalTokens,
		&l.LatencyMs, &l.StatusCode, &l.ErrorMsg,
		&l.SystemPrompt, &l.PromptText,
		&l.ThinkingText, &l.ResponseText, &l.ToolCallsJSON,
		&l.ContextJSON,
		&l.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *Store) ListAuditLogs(limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT request_id, protocol, account_id, session_id, worker_pid, model_name,
       COALESCE(thinking_level, ''),
       COALESCE(prompt_tokens, 0), COALESCE(cached_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0),
       latency_ms, status_code, error_msg,
       COALESCE(system_prompt, ''), COALESCE(prompt_text, ''),
       COALESCE(thinking_text, ''), COALESCE(response_text, ''), COALESCE(tool_calls_json, ''),
       COALESCE(context_json, ''),
       created_at
FROM audit_logs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(
			&l.RequestID, &l.Protocol, &l.AccountID, &l.SessionID, &l.WorkerPID, &l.ModelName,
			&l.ThinkingLevel,
			&l.PromptTokens, &l.CachedTokens, &l.CompletionTokens, &l.TotalTokens,
			&l.LatencyMs, &l.StatusCode, &l.ErrorMsg,
			&l.SystemPrompt, &l.PromptText,
			&l.ThinkingText, &l.ResponseText, &l.ToolCallsJSON,
			&l.ContextJSON,
			&l.CreatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

func (s *Store) ListAuditLogsForSync(limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 40
	}
	rows, err := s.db.Query(`
SELECT request_id, account_id, COALESCE(session_id, ''),
       COALESCE(prompt_tokens, 0), COALESCE(cached_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0),
       COALESCE(latency_ms, 0), COALESCE(thinking_text, ''),
       COALESCE(response_text, ''), COALESCE(tool_calls_json, ''),
       COALESCE(context_json, ''),
       created_at
FROM audit_logs
WHERE session_id != ''
ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]AuditLog, 0, limit)
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(
			&l.RequestID, &l.AccountID, &l.SessionID,
			&l.PromptTokens, &l.CachedTokens, &l.CompletionTokens, &l.TotalTokens,
			&l.LatencyMs, &l.ThinkingText,
			&l.ResponseText, &l.ToolCallsJSON,
			&l.ContextJSON,
			&l.CreatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

func (s *Store) ListAuditLogsCompact(limit int, fullRows int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 24
	}
	if fullRows < 0 {
		fullRows = 0
	}
	if fullRows > limit {
		fullRows = limit
	}
	var head []AuditLog
	if fullRows > 0 {
		var err error
		head, err = s.ListAuditLogs(fullRows)
		if err != nil || len(head) < fullRows || limit <= fullRows {
			return head, err
		}
	}
	remLimit := limit - fullRows
	rows, err := s.db.Query(`
SELECT request_id, protocol, account_id, session_id, worker_pid, model_name,
       COALESCE(thinking_level, ''),
       COALESCE(prompt_tokens, 0), COALESCE(cached_tokens, 0), COALESCE(completion_tokens, 0), COALESCE(total_tokens, 0),
       latency_ms, status_code, error_msg,
       CASE
         WHEN LENGTH(COALESCE(prompt_text, '')) > 850
         THEN SUBSTR(prompt_text, 1, 400) || char(10) || SUBSTR(prompt_text, -450)
         ELSE COALESCE(prompt_text, '')
       END,
       COALESCE(context_json, ''),
       created_at
FROM audit_logs ORDER BY created_at DESC LIMIT ? OFFSET ?`, remLimit, fullRows)
	if err != nil {
		return head, nil
	}
	defer rows.Close()

	list := make([]AuditLog, 0, limit)
	list = append(list, head...)
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(
			&l.RequestID, &l.Protocol, &l.AccountID, &l.SessionID, &l.WorkerPID, &l.ModelName,
			&l.ThinkingLevel,
			&l.PromptTokens, &l.CachedTokens, &l.CompletionTokens, &l.TotalTokens,
			&l.LatencyMs, &l.StatusCode, &l.ErrorMsg,
			&l.PromptText,
			&l.ContextJSON,
			&l.CreatedAt,
		); err != nil {
			return list, err
		}
		list = append(list, l)
	}
	return list, rows.Err()
}

func calcRemainingPct(usedTokens, budgetTokens int64, defaultBase float64) float64 {
	if budgetTokens <= 0 || usedTokens <= 0 {
		return defaultBase
	}
	rem := 100.0 - (float64(usedTokens)/float64(budgetTokens))*100.0
	if rem < 0 {
		rem = 0
	}
	if rem > 100 {
		rem = 100
	}
	return math.Round(rem*10) / 10
}

func BuildQuotaGroups(geminiWeek, gemini5h, claudeWeek, claude5h, argonWeek int64) []ModelQuotaGroup {
	now := time.Now()
	// Calculate remaining time in current 7-day cycle and 5-hour rolling cycle
	weekRemHours := 7*24 - (int(now.Weekday())*24 + now.Hour())
	if weekRemHours <= 0 {
		weekRemHours = 155
	}
	wDays := weekRemHours / 24
	wHours := weekRemHours % 24

	fiveRemMin := 300 - ((now.Hour()%5)*60 + now.Minute())
	if fiveRemMin <= 0 {
		fiveRemMin = 202
	}
	fHours := fiveRemMin / 60
	fMins := fiveRemMin % 60

	return []ModelQuotaGroup{
		{
			GroupName: "Gemini 模型",
			Windows: []QuotaWindow{
				{
					Label:        "每周",
					RemainingPct: calcRemainingPct(geminiWeek, 5_000_000, 99.9),
					ResetText:    fmt.Sprintf("%d 天 %d 小时后重置", wDays, wHours),
				},
				{
					Label:        "5 小时",
					RemainingPct: calcRemainingPct(gemini5h, 500_000, 100.0),
					ResetText:    fmt.Sprintf("%d 小时 %d 分后重置", fHours, fMins),
				},
			},
		},
		{
			GroupName: "Claude / GPT 模型",
			Windows: []QuotaWindow{
				{
					Label:        "每周",
					RemainingPct: calcRemainingPct(claudeWeek, 5_000_000, 100.0),
					ResetText:    fmt.Sprintf("%d 天 %d 小时后重置", wDays, (wHours+12)%24),
				},
				{
					Label:        "5 小时",
					RemainingPct: calcRemainingPct(claude5h, 500_000, 100.0),
					ResetText:    fmt.Sprintf("%d 小时 %d 分后重置", (fHours+1)%5, (fMins+31)%60),
				},
			},
		},
		{
			GroupName: "Gemini 4 Argon",
			Windows: []QuotaWindow{
				{
					Label:        "每周",
					RemainingPct: calcRemainingPct(argonWeek, 2_000_000, 99.9),
					ResetText:    fmt.Sprintf("%d 天 %d 小时后重置", wDays, (wHours+1)%24),
				},
			},
		},
	}
}

func (s *Store) GetAccountUsageMap() (map[string]AccountUsageStats, error) {
	s.cacheMu.RLock()
	if s.usageCache != nil && time.Since(s.usageCachedAt) < 800*time.Millisecond {
		cp := make(map[string]AccountUsageStats, len(s.usageCache))
		for k, v := range s.usageCache {
			cp[k] = v
		}
		s.cacheMu.RUnlock()
		return cp, nil
	}
	s.cacheMu.RUnlock()

	out := make(map[string]AccountUsageStats)
	nowUTC := time.Now().UTC()
	cutoff24h := nowUTC.Add(-24 * time.Hour)
	cutoff7d := nowUTC.Add(-7 * 24 * time.Hour)
	cutoff5h := nowUTC.Add(-5 * time.Hour)

	rows, err := s.db.Query(`
SELECT account_id,
       COUNT(*),
       COALESCE(SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN status_code >= 400 OR status_code < 200 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(prompt_tokens), 0),
       COALESCE(SUM(completion_tokens), 0),
       COALESCE(SUM(total_tokens), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? THEN total_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? AND status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? AND (status_code >= 400 OR status_code < 200) THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? THEN prompt_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? THEN completion_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? THEN prompt_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? THEN completion_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? AND (LOWER(model_name) LIKE '%claude%' OR LOWER(model_name) LIKE '%gpt%') THEN total_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? AND (LOWER(model_name) LIKE '%claude%' OR LOWER(model_name) LIKE '%gpt%') THEN total_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? AND LOWER(model_name) LIKE '%argon%' THEN total_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? AND LOWER(model_name) NOT LIKE '%claude%' AND LOWER(model_name) NOT LIKE '%gpt%' AND LOWER(model_name) NOT LIKE '%argon%' THEN total_tokens ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN created_at >= ? AND LOWER(model_name) NOT LIKE '%claude%' AND LOWER(model_name) NOT LIKE '%gpt%' AND LOWER(model_name) NOT LIKE '%argon%' THEN total_tokens ELSE 0 END), 0),
       COALESCE(AVG(latency_ms), 0),
       COALESCE(MAX(created_at), '')
FROM audit_logs
GROUP BY account_id`,
		cutoff24h, cutoff24h,
		cutoff24h, cutoff24h, cutoff24h, cutoff24h,
		cutoff7d, cutoff7d,
		cutoff7d, cutoff5h, cutoff7d, cutoff7d, cutoff5h,
	)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var accID string
		var u AccountUsageStats
		var claudeWeek, claude5h, argonWeek, geminiWeek, gemini5h int64
		var lastRaw any
		if err := rows.Scan(
			&accID,
			&u.TotalRequests,
			&u.Requests24h,
			&u.SuccessRequests,
			&u.ErrorRequests,
			&u.PromptTokens,
			&u.CompletionTokens,
			&u.TotalTokens,
			&u.Tokens24h,
			&u.TodaySuccess,
			&u.TodayFailed,
			&u.TodayPromptTokens,
			&u.TodayCompletionTokens,
			&u.WeekPromptTokens,
			&u.WeekCompletionTokens,
			&claudeWeek,
			&claude5h,
			&argonWeek,
			&geminiWeek,
			&gemini5h,
			&u.AvgLatencyMs,
			&lastRaw,
		); err != nil {
			return out, err
		}
		if u.TotalRequests > 0 {
			u.SuccessRatePct = math.Round((float64(u.SuccessRequests)/float64(u.TotalRequests))*1000) / 10
		} else {
			u.SuccessRatePct = 100
		}
		u.AvgLatencyMs = math.Round(u.AvgLatencyMs)
		if lastRaw != nil {
			u.LastUsedAt = fmt.Sprintf("%v", lastRaw)
		}
		u.QuotaGroups = BuildQuotaGroups(geminiWeek, gemini5h, claudeWeek, claude5h, argonWeek)
		out[accID] = u
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	s.cacheMu.Lock()
	cp := make(map[string]AccountUsageStats, len(out))
	for k, v := range out {
		cp[k] = v
	}
	s.usageCache = cp
	s.usageCachedAt = time.Now()
	s.cacheMu.Unlock()
	return out, nil
}

func (s *Store) GetOverviewStats() (OverviewStats, error) {
	s.cacheMu.RLock()
	if s.statsCache != nil && time.Since(s.statsCachedAt) < 800*time.Millisecond {
		st := *s.statsCache
		s.cacheMu.RUnlock()
		return st, nil
	}
	s.cacheMu.RUnlock()

	var st OverviewStats
	_ = s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled = 1 AND status = 'ready' THEN 1 ELSE 0 END), 0) FROM accounts`).Scan(&st.TotalAccounts, &st.ReadyAccounts)
	_ = s.db.QueryRow(`
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 400 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(total_tokens), 0),
       COALESCE(AVG(latency_ms), 0)
FROM audit_logs`).Scan(&st.TotalRequests, &st.SuccessRequests, &st.TotalTokens, &st.AvgLatencyMs)
	st.AvgLatencyMs = math.Round(st.AvgLatencyMs)

	s.cacheMu.Lock()
	cached := st
	s.statsCache = &cached
	s.statsCachedAt = time.Now()
	s.cacheMu.Unlock()
	return st, nil
}

func (s *Store) GetSetting(key string) (string, bool) {
	if s == nil || s.db == nil {
		return "", false
	}
	k := strings.TrimSpace(key)
	s.cacheMu.RLock()
	if s.settingsKnown != nil && s.settingsKnown[k] {
		val := s.settingsCache[k]
		s.cacheMu.RUnlock()
		return val, val != ""
	}
	s.cacheMu.RUnlock()

	var val string
	err := s.db.QueryRow(`SELECT value FROM gateway_settings WHERE key = ?`, k).Scan(&val)
	s.cacheMu.Lock()
	if s.settingsCache == nil {
		s.settingsCache = make(map[string]string)
	}
	if s.settingsKnown == nil {
		s.settingsKnown = make(map[string]bool)
	}
	if err != nil {
		s.settingsCache[k] = ""
		s.settingsKnown[k] = true
		s.cacheMu.Unlock()
		return "", false
	}
	s.settingsCache[k] = val
	s.settingsKnown[k] = true
	s.cacheMu.Unlock()
	return val, true
}

func (s *Store) SetSetting(key, value string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store not initialized")
	}
	k := strings.TrimSpace(key)
	now := time.Now().UTC()
	_, err := s.db.Exec(`
INSERT INTO gateway_settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, k, value, now)
	if err == nil {
		s.cacheMu.Lock()
		if s.settingsCache == nil {
			s.settingsCache = make(map[string]string)
		}
		if s.settingsKnown == nil {
			s.settingsKnown = make(map[string]bool)
		}
		s.settingsCache[k] = value
		s.settingsKnown[k] = true
		s.cacheMu.Unlock()
	}
	return err
}
