package server

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
	"antigravity-gateway/internal/updater"
	"antigravity-gateway/internal/version"
)

type ctxKey string

const (
	ctxKeyAPIKey              ctxKey = "api_key_record"
	adminSessionCookie               = "ag_admin_session"
	adminPassSettingKey              = "admin_password_sha256"
	adminTokenSettingKey             = "admin_token_sha256"
	adminPassHashSettingKey          = "admin_password_pbkdf2"
	adminTokenHashSettingKey         = "admin_token_pbkdf2"
	adminCredentialsLockedKey        = "admin_credentials_locked"
	adminCredentialsFileName         = "admin_credentials.txt"
	adminPasswordMinLen              = 8
	adminTokenMinLen                 = 12
	pbkdf2Iterations                 = 600000
)

type rateWindow struct {
	windowStart time.Time
	count       int
}

type loginAttemptWindow struct {
	windowStart time.Time
	failures    int
}

type GatewayServer struct {
	cfg                *config.Config
	store              *storage.Store
	pool               *acp.MultiAccountPool
	sm                 *acp.SessionManager
	hub                *WiretapHub
	staticFS           fs.FS
	mu                 sync.RWMutex
	loopbackBaseURL    string
	accountCheckAt     map[string]string
	accountCheckMsg    map[string]string
	accountQuotaGroups map[string][]storage.ModelQuotaGroup
	quotaRefreshing    map[string]bool
	rateMu             sync.Mutex
	keyWindows         map[int64]*rateWindow
	authMu             sync.Mutex
	adminSessions      map[string]time.Time
	loginAttempts      map[string]*loginAttemptWindow
	// verifiedTokenSHA caches sha256 of the admin token after it matched the stored
	// PBKDF2 hash, so per-request bearer checks don't pay the KDF cost.
	verifiedTokenSHA string
	updater          *updater.Updater
	restartFn        func(execPath string)
}

func NewGatewayServer(
	cfg *config.Config,
	store *storage.Store,
	pool *acp.MultiAccountPool,
	sm *acp.SessionManager,
	hub *WiretapHub,
	staticFS fs.FS,
) *GatewayServer {
	gs := &GatewayServer{
		cfg:                cfg,
		store:              store,
		pool:               pool,
		sm:                 sm,
		hub:                hub,
		staticFS:           staticFS,
		loopbackBaseURL:    fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port),
		updater:            updater.NewUpdater(cfg.Server.DataDir, store),
		accountCheckAt:     make(map[string]string),
		accountCheckMsg:    make(map[string]string),
		accountQuotaGroups: make(map[string][]storage.ModelQuotaGroup),
		quotaRefreshing:    make(map[string]bool),
		keyWindows:         make(map[int64]*rateWindow),
		adminSessions:      make(map[string]time.Time),
		loginAttempts:      make(map[string]*loginAttemptWindow),
	}
	gs.InitOrRotateAdminCredentials()
	return gs
}

func (gs *GatewayServer) allowAPIKeyRequest(rec *storage.APIKey) bool {
	if rec == nil || rec.RateLimitRPM <= 0 {
		return true
	}
	now := time.Now()
	gs.rateMu.Lock()
	defer gs.rateMu.Unlock()
	if gs.keyWindows == nil {
		gs.keyWindows = make(map[int64]*rateWindow)
	}
	win, exists := gs.keyWindows[rec.ID]
	if !exists || now.Sub(win.windowStart) >= time.Minute {
		if len(gs.keyWindows) > 256 {
			for k, w := range gs.keyWindows {
				if now.Sub(w.windowStart) >= time.Minute {
					delete(gs.keyWindows, k)
				}
			}
		}
		gs.keyWindows[rec.ID] = &rateWindow{
			windowStart: now,
			count:       1,
		}
		return true
	}
	if win.count >= rec.RateLimitRPM {
		return false
	}
	win.count++
	return true
}

func (gs *GatewayServer) SetLoopbackBaseURL(url string) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.loopbackBaseURL = strings.TrimRight(url, "/")
}

func (gs *GatewayServer) getLoopbackBaseURL(r *http.Request) string {
	gs.mu.RLock()
	base := gs.loopbackBaseURL
	gs.mu.RUnlock()
	if base != "" && !strings.HasSuffix(base, ":0") {
		return base
	}
	return "http://" + r.Host
}

func (gs *GatewayServer) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": version.Version,
		})
	})

	// Internal loopback MCP server for tool parking & resuming
	var wt acp.WiretapCallback
	if gs.hub != nil {
		wt = gs.hub.Broadcast
	}
	// Only local agent subprocesses may reach the bridge; bridge IDs are not credentials.
	mcpBridge := acp.MCPBridgeHandler(gs.sm, wt)
	mux.HandleFunc("/mcp/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			writeJSONError(w, http.StatusForbidden, "MCP bridge is only reachable from the gateway host")
			return
		}
		mcpBridge.ServeHTTP(w, r)
	})

	// Northbound API endpoints (3 core session protocols + model listing/lookup + ACP WS)
	mux.HandleFunc("/v1/models", gs.withNorthboundAuth(gs.handleModels))
	mux.HandleFunc("/v1/models/", gs.withNorthboundAuth(gs.handleModels))
	mux.HandleFunc("/v1/v1/models", gs.withNorthboundAuth(gs.handleModels))
	mux.HandleFunc("/v1/v1/models/", gs.withNorthboundAuth(gs.handleModels))
	mux.HandleFunc("/v1beta/models", gs.withNorthboundAuth(gs.handleModels))
	mux.HandleFunc("/v1beta/models/", gs.withNorthboundAuth(gs.handleModels))
	mux.HandleFunc("/models", gs.withNorthboundAuth(gs.handleModels))
	mux.HandleFunc("/models/", gs.withNorthboundAuth(gs.handleModels))

	// 1. OpenAI Official Protocol (Responses API)
	mux.HandleFunc("/v1/responses", gs.withNorthboundAuth(gs.handleOpenAIResponses))
	mux.HandleFunc("/v1/v1/responses", gs.withNorthboundAuth(gs.handleOpenAIResponses))
	mux.HandleFunc("/responses", gs.withNorthboundAuth(gs.handleOpenAIResponses))

	// 2. OpenAI Compatible Protocol (Chat Completions API)
	mux.HandleFunc("/v1/chat/completions", gs.withNorthboundAuth(gs.handleOpenAIChatCompletions))
	mux.HandleFunc("/v1/v1/chat/completions", gs.withNorthboundAuth(gs.handleOpenAIChatCompletions))
	mux.HandleFunc("/chat/completions", gs.withNorthboundAuth(gs.handleOpenAIChatCompletions))

	// 3. Claude Protocol (Anthropic Messages API)
	mux.HandleFunc("/v1/messages", gs.withNorthboundAuth(gs.handleClaudeMessages))
	mux.HandleFunc("/v1/v1/messages", gs.withNorthboundAuth(gs.handleClaudeMessages))
	mux.HandleFunc("/messages", gs.withNorthboundAuth(gs.handleClaudeMessages))

	// Native ACP WebSocket Tunnel
	mux.HandleFunc("/v1/acp/ws", gs.withNorthboundAuth(gs.handleACPWebSocketTunnel))
	mux.HandleFunc("/v1/v1/acp/ws", gs.withNorthboundAuth(gs.handleACPWebSocketTunnel))
	mux.HandleFunc("/acp/ws", gs.withNorthboundAuth(gs.handleACPWebSocketTunnel))

	// Admin Auth endpoints (password login / session check / logout / password change)
	mux.HandleFunc("/api/admin/auth/login", gs.handleAdminPasswordLogin)
	mux.HandleFunc("/api/admin/auth/logout", gs.handleAdminLogout)
	mux.HandleFunc("/api/admin/auth/session", gs.withAdminAuth(gs.handleAdminSessionCheck))
	mux.HandleFunc("/api/admin/auth/password", gs.withAdminAuth(gs.handleAdminChangePassword))

	// Admin API endpoints
	mux.HandleFunc("/api/admin/overview", gs.withAdminAuth(gs.handleAdminOverview))
	mux.HandleFunc("/api/admin/status", gs.withAdminAuth(gs.handleAdminOverview))
	mux.HandleFunc("/api/admin/models", gs.withAdminAuth(gs.handleAdminModels))
	mux.HandleFunc("/api/admin/accounts", gs.withAdminAuth(gs.handleAdminAccounts))
	mux.HandleFunc("/api/admin/accounts/", gs.withAdminAuth(gs.handleAdminAccountAction))
	mux.HandleFunc("/api/admin/workers", gs.withAdminAuth(gs.handleAdminWorkers))
	mux.HandleFunc("/api/admin/workers/scale", gs.withAdminAuth(gs.handleAdminWorkersScale))
	mux.HandleFunc("/api/admin/workers/", gs.withAdminAuth(gs.handleAdminWorkerKill))
	mux.HandleFunc("/api/admin/policy/prompt", gs.withAdminAuth(gs.handleAdminPromptPolicy))
	mux.HandleFunc("/api/admin/prompt-policy", gs.withAdminAuth(gs.handleAdminPromptPolicy))
	mux.HandleFunc("/api/admin/policy", gs.withAdminAuth(gs.handleAdminPromptPolicy))
	mux.HandleFunc("/api/admin/auth/env", gs.withAdminAuth(gs.handleAdminAuthEnv))
	mux.HandleFunc("/api/admin/auth/login/start", gs.withAdminAuth(gs.handleAdminDefaultLoginStart))
	mux.HandleFunc("/api/admin/auth/login/submit", gs.withAdminAuth(gs.handleAdminDefaultLoginSubmit))
	mux.HandleFunc("/api/admin/skills", gs.withAdminAuth(gs.handleAdminSkills))
	mux.HandleFunc("/api/admin/skills/", gs.withAdminAuth(gs.handleAdminSkillDelete))
	mux.HandleFunc("/api/admin/keys", gs.withAdminAuth(gs.handleAdminKeys))
	mux.HandleFunc("/api/admin/keys/", gs.withAdminAuth(gs.handleAdminKeyDelete))
	mux.HandleFunc("/api/admin/api-keys", gs.withAdminAuth(gs.handleAdminKeys))
	mux.HandleFunc("/api/admin/api-keys/", gs.withAdminAuth(gs.handleAdminKeyDelete))
	mux.HandleFunc("/api/admin/logs", gs.withAdminAuth(gs.handleAdminLogs))
	mux.HandleFunc("/api/admin/update/status", gs.withAdminAuth(gs.handleAdminUpdateStatus))
	mux.HandleFunc("/api/admin/update/apply", gs.withAdminAuth(gs.handleAdminUpdateApply))
	if gs.hub != nil {
		mux.HandleFunc("/api/admin/ws/wiretap", gs.withAdminAuth(gs.hub.ServeHTTP))
	}

	// Static ReUI frontend
	if gs.staticFS != nil {
		fileServer := http.FileServer(http.FS(gs.staticFS))
		mux.Handle("/", fileServer)
	}

	return gs.withCORS(mux)
}

func (gs *GatewayServer) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := gs.cfg.Server.CORSAllowedOrigins
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		allowOriginHeader := "*"
		if len(allowed) > 0 {
			allowAll := false
			matched := false
			for _, a := range allowed {
				a = strings.TrimSpace(a)
				if a == "*" {
					allowAll = true
					break
				}
				if origin != "" && strings.EqualFold(a, origin) {
					matched = true
				}
			}
			if !allowAll {
				if matched {
					allowOriginHeader = origin
					w.Header().Set("Vary", "Origin")
				} else {
					allowOriginHeader = ""
				}
			}
		}
		if allowOriginHeader != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowOriginHeader)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, x-goog-api-key, anthropic-version, X-ACP-Session-ID, X-ACP-Account-ID, OpenAI-Beta")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractToken returns the credential carried by the request. The `?key=` query
// parameter (Gemini-style clients) is only honoured when allowQueryKey is set,
// i.e. for the northbound API; admin endpoints never read credentials from URLs.
func extractToken(r *http.Request, allowQueryKey bool) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if xKey := strings.TrimSpace(r.Header.Get("x-api-key")); xKey != "" {
		return xKey
	}
	if gKey := strings.TrimSpace(r.Header.Get("x-goog-api-key")); gKey != "" {
		return gKey
	}
	if allowQueryKey {
		if qKey := strings.TrimSpace(r.URL.Query().Get("key")); qKey != "" {
			return qKey
		}
	}
	if c, err := r.Cookie(adminSessionCookie); err == nil {
		if val := strings.TrimSpace(c.Value); val != "" {
			return val
		}
	}
	return ""
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// hashAdminSecret encodes a salted PBKDF2-SHA256 hash as
// "pbkdf2-sha256$<iterations>$<salt hex>$<hash hex>".
func hashAdminSecret(secret string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, secret, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(dk)), nil
}

func verifyAdminSecretHash(secret, encoded string) bool {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 || iter > 10_000_000 {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, secret, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// isLoopbackRequest reports whether the request came directly from this host
// (no proxy forwarding headers).
func isLoopbackRequest(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	ip := net.ParseIP(clientIPFromRequest(r))
	return ip != nil && ip.IsLoopback()
}

// isSameOriginRequest is true when the request has no Origin header (non-browser
// clients) or the Origin's host matches the Host the request was sent to.
func isSameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return !strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// isOriginAllowedForAPI accepts same-origin requests and origins explicitly listed
// in server.cors_allowed_origins (including "*").
func (gs *GatewayServer) isOriginAllowedForAPI(r *http.Request) bool {
	if isSameOriginRequest(r) {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	for _, a := range gs.cfg.Server.CORSAllowedOrigins {
		a = strings.TrimSpace(a)
		if a == "*" || (origin != "" && strings.EqualFold(a, origin)) {
			return true
		}
	}
	return false
}

func generateRandomCredential(prefix string, byteLen int) string {
	buf := make([]byte, byteLen)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buf)
}

func (gs *GatewayServer) AdminCredentialsFilePath() string {
	dataDir := strings.TrimSpace(gs.cfg.Server.DataDir)
	if dataDir == "" {
		dataDir = "./data"
	}
	return filepath.Join(dataDir, adminCredentialsFileName)
}

func (gs *GatewayServer) isAdminCredentialsLocked() bool {
	if gs.store == nil {
		return false
	}
	if locked, ok := gs.store.GetSetting(adminCredentialsLockedKey); ok && strings.TrimSpace(locked) == "1" {
		return true
	}
	for _, k := range []string{adminPassHashSettingKey, adminPassSettingKey} {
		if saved, ok := gs.store.GetSetting(k); ok && strings.TrimSpace(saved) != "" {
			return true
		}
	}
	return false
}

func (gs *GatewayServer) writeAdminCredentialsFile(password, token string, locked bool) {
	filePath := gs.AdminCredentialsFilePath()
	_ = os.MkdirAll(filepath.Dir(filePath), 0700)
	var content string
	nowStr := time.Now().Format(time.RFC3339)
	if locked {
		content = fmt.Sprintf(
			"# Antigravity ACP Gateway — Admin Credentials\n"+
				"STATUS=LOCKED_BY_USER\n"+
				"UPDATED_AT=%s\n"+
				"NOTE=管理员密码与 Token 已由用户主动修改并永久固定（加盐 PBKDF2-SHA256 哈希存储于 gateway.db settings 表），重部署不会再重置。\n",
			nowStr,
		)
	} else {
		content = fmt.Sprintf(
			"# Antigravity ACP Gateway — Dynamic Deployment Credentials\n"+
				"# 提示: 当前密码与 Token 为本次部署动态生成；登录 Web 控制台点击右上角「修改密码」主动修改后即永久固定。\n"+
				"STATUS=DYNAMIC\n"+
				"GENERATED_AT=%s\n"+
				"ADMIN_PASSWORD=%s\n"+
				"ADMIN_TOKEN=%s\n",
			nowStr,
			password,
			token,
		)
	}
	_ = os.WriteFile(filePath, []byte(content), 0600)
	_ = os.Chmod(filePath, 0600)
}

// InitOrRotateAdminCredentials dynamically generates a fresh admin_password and admin_token
// on each deployment/startup until the user actively modifies and locks them.
func (gs *GatewayServer) InitOrRotateAdminCredentials() (password string, token string, locked bool) {
	if gs.isAdminCredentialsLocked() {
		if gs.store != nil {
			_ = gs.store.SetSetting(adminCredentialsLockedKey, "1")
		}
		gs.writeAdminCredentialsFile("", "", true)
		return "", "", true
	}

	rawPwd := strings.TrimSpace(gs.cfg.GetRawAdminPassword())
	rawTok := strings.TrimSpace(gs.cfg.GetAdminToken())
	pwdDynamic := config.IsDynamicCredentialValue(rawPwd)
	tokDynamic := config.IsDynamicCredentialValue(rawTok)

	// If neither is dynamic (e.g. explicitly configured in unit test or custom env), keep as-is
	if !pwdDynamic && !tokDynamic {
		return rawPwd, rawTok, false
	}

	if pwdDynamic {
		rawPwd = generateRandomCredential("agp-", 8)
		gs.cfg.SetAdminPassword(rawPwd)
	}
	if tokDynamic {
		rawTok = generateRandomCredential("agt-", 12)
		gs.cfg.SetAdminToken(rawTok)
	}

	gs.writeAdminCredentialsFile(rawPwd, rawTok, false)
	return rawPwd, rawTok, false
}

func (gs *GatewayServer) LogStartupAdminCredentials() {
	credFile := gs.AdminCredentialsFilePath()
	if gs.isAdminCredentialsLocked() {
		log.Printf("[Admin Auth] 管理员密码与 Token 已由用户主动修改并固定 (状态文件: %s)", credFile)
		return
	}
	log.Printf("[Admin Auth] 本次启动已动态生成管理密码与 Token，已写入 %s (权限 0600)；登录 Web 控制台「修改密码」后即永久固定。", credFile)
}

// verifyStoredSecret checks candidate against the PBKDF2 hash under hashKey, falling
// back to a legacy unsalted sha256 hash under legacyKey (migrated on success).
func (gs *GatewayServer) verifyStoredSecret(candidate, hashKey, legacyKey string) bool {
	if gs.store == nil {
		return false
	}
	if enc, ok := gs.store.GetSetting(hashKey); ok && strings.TrimSpace(enc) != "" {
		return verifyAdminSecretHash(candidate, enc)
	}
	legacy, ok := gs.store.GetSetting(legacyKey)
	if !ok || strings.TrimSpace(legacy) == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(sha256Hex(candidate)), []byte(strings.TrimSpace(legacy))) != 1 {
		return false
	}
	if enc, err := hashAdminSecret(candidate); err == nil {
		if gs.store.SetSetting(hashKey, enc) == nil {
			_ = gs.store.SetSetting(legacyKey, "")
		}
	}
	return true
}

// verifyAdminPassword checks the console login password (never accepted as a bearer token).
func (gs *GatewayServer) verifyAdminPassword(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	if gs.store != nil && gs.isAdminCredentialsLocked() {
		return gs.verifyStoredSecret(candidate, adminPassHashSettingKey, adminPassSettingKey)
	}
	expected := strings.TrimSpace(gs.cfg.GetAdminPassword())
	return expected != "" && !config.IsDynamicCredentialValue(expected) &&
		subtle.ConstantTimeCompare([]byte(sha256Hex(candidate)), []byte(sha256Hex(expected))) == 1
}

// verifyAdminToken checks the static admin API token.
func (gs *GatewayServer) verifyAdminToken(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	candSHA := sha256Hex(candidate)
	if gs.store != nil && gs.isAdminCredentialsLocked() {
		gs.authMu.Lock()
		cached := gs.verifiedTokenSHA
		gs.authMu.Unlock()
		if cached != "" && subtle.ConstantTimeCompare([]byte(candSHA), []byte(cached)) == 1 {
			return true
		}
		if !gs.verifyStoredSecret(candidate, adminTokenHashSettingKey, adminTokenSettingKey) {
			return false
		}
		gs.authMu.Lock()
		gs.verifiedTokenSHA = candSHA
		gs.authMu.Unlock()
		return true
	}
	expected := strings.TrimSpace(gs.cfg.GetAdminToken())
	return expected != "" && !config.IsDynamicCredentialValue(expected) &&
		subtle.ConstantTimeCompare([]byte(candSHA), []byte(sha256Hex(expected))) == 1
}

// persistAdminCredentials stores salted hashes of the new password (and token, if
// given) and locks credentials so restarts no longer rotate them.
func (gs *GatewayServer) persistAdminCredentials(newPwd, newToken string) error {
	passHash, err := hashAdminSecret(newPwd)
	if err != nil {
		return err
	}
	tokenToStore := strings.TrimSpace(newToken)
	if tokenToStore == "" {
		_, hasNew := gs.store.GetSetting(adminTokenHashSettingKey)
		_, hasLegacy := gs.store.GetSetting(adminTokenSettingKey)
		if !hasNew && !hasLegacy {
			tokenToStore = strings.TrimSpace(gs.cfg.GetAdminToken())
			if config.IsDynamicCredentialValue(tokenToStore) {
				tokenToStore = generateRandomCredential("agt-", 24)
			}
		}
	}
	if err := gs.store.SetSetting(adminPassHashSettingKey, passHash); err != nil {
		return err
	}
	_ = gs.store.SetSetting(adminPassSettingKey, "")
	if tokenToStore != "" {
		tokHash, err := hashAdminSecret(tokenToStore)
		if err != nil {
			return err
		}
		if err := gs.store.SetSetting(adminTokenHashSettingKey, tokHash); err != nil {
			return err
		}
		_ = gs.store.SetSetting(adminTokenSettingKey, "")
	}
	gs.authMu.Lock()
	gs.verifiedTokenSHA = ""
	gs.authMu.Unlock()
	return gs.store.SetSetting(adminCredentialsLockedKey, "1")
}

// verifyAdminLoginCredential is used by the login and change-password endpoints,
// which accept either the password or the admin token.
func (gs *GatewayServer) verifyAdminLoginCredential(candidate string) bool {
	return gs.verifyAdminPassword(candidate) || gs.verifyAdminToken(candidate)
}

func (gs *GatewayServer) isValidAdminSession(token string) bool {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "ag_sess_") {
		return false
	}
	now := time.Now()
	gs.authMu.Lock()
	defer gs.authMu.Unlock()
	if gs.adminSessions == nil {
		return false
	}
	exp, exists := gs.adminSessions[token]
	if !exists {
		return false
	}
	if now.After(exp) {
		delete(gs.adminSessions, token)
		return false
	}
	return true
}

func (gs *GatewayServer) issueAdminSession(ttl time.Duration) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	sessToken := "ag_sess_" + hex.EncodeToString(buf)
	now := time.Now().UTC()
	exp := now.Add(ttl)

	gs.authMu.Lock()
	defer gs.authMu.Unlock()
	if gs.adminSessions == nil {
		gs.adminSessions = make(map[string]time.Time)
	}
	for k, v := range gs.adminSessions {
		if now.After(v) {
			delete(gs.adminSessions, k)
		}
	}
	gs.adminSessions[sessToken] = exp
	return sessToken, exp, nil
}

func (gs *GatewayServer) revokeAdminSession(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	gs.authMu.Lock()
	defer gs.authMu.Unlock()
	delete(gs.adminSessions, token)
}

func (gs *GatewayServer) revokeAllAdminSessions() {
	gs.authMu.Lock()
	defer gs.authMu.Unlock()
	gs.adminSessions = make(map[string]time.Time)
}

func clientIPFromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func (gs *GatewayServer) checkLoginRateLimit(ip string) bool {
	now := time.Now()
	gs.authMu.Lock()
	defer gs.authMu.Unlock()
	if gs.loginAttempts == nil {
		gs.loginAttempts = make(map[string]*loginAttemptWindow)
	}
	win, exists := gs.loginAttempts[ip]
	if !exists || now.Sub(win.windowStart) >= 5*time.Minute {
		return true
	}
	return win.failures < 10
}

func (gs *GatewayServer) recordLoginFailure(ip string) {
	now := time.Now()
	gs.authMu.Lock()
	defer gs.authMu.Unlock()
	if gs.loginAttempts == nil {
		gs.loginAttempts = make(map[string]*loginAttemptWindow)
	}
	win, exists := gs.loginAttempts[ip]
	if !exists || now.Sub(win.windowStart) >= 5*time.Minute {
		if len(gs.loginAttempts) > 256 {
			for k, w := range gs.loginAttempts {
				if now.Sub(w.windowStart) >= 5*time.Minute {
					delete(gs.loginAttempts, k)
				}
			}
		}
		gs.loginAttempts[ip] = &loginAttemptWindow{
			windowStart: now,
			failures:    1,
		}
		return
	}
	win.failures++
}

func (gs *GatewayServer) clearLoginFailures(ip string) {
	gs.authMu.Lock()
	defer gs.authMu.Unlock()
	delete(gs.loginAttempts, ip)
}

type adminAuthResult int

const (
	adminAuthMissing adminAuthResult = iota
	adminAuthOK
	adminAuthInvalid
	adminAuthRateLimited
)

// authenticateAdmin accepts a valid admin session (header or cookie) or the admin
// token. Token guesses are rate-limited per client IP; session tokens are random
// 256-bit values and are always checked so a locked-out IP keeps working sessions.
func (gs *GatewayServer) authenticateAdmin(r *http.Request, bucket string) adminAuthResult {
	candidates := make([]string, 0, 2)
	if token := extractToken(r, false); token != "" {
		candidates = append(candidates, token)
	}
	if c, err := r.Cookie(adminSessionCookie); err == nil {
		if v := strings.TrimSpace(c.Value); v != "" && (len(candidates) == 0 || candidates[0] != v) {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		return adminAuthMissing
	}
	for _, c := range candidates {
		if gs.isValidAdminSession(c) {
			return adminAuthOK
		}
	}
	if !gs.checkLoginRateLimit(bucket) {
		return adminAuthRateLimited
	}
	for _, c := range candidates {
		if !strings.HasPrefix(c, "ag_sess_") && gs.verifyAdminToken(c) {
			return adminAuthOK
		}
	}
	gs.recordLoginFailure(bucket)
	return adminAuthInvalid
}

func (gs *GatewayServer) isAdminAuthenticated(r *http.Request) bool {
	return gs.authenticateAdmin(r, clientIPFromRequest(r)) == adminAuthOK
}

func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func (gs *GatewayServer) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isStateChangingMethod(r.Method) && !isSameOriginRequest(r) {
			writeJSONError(w, http.StatusForbidden, "cross-origin admin request rejected")
			return
		}
		switch gs.authenticateAdmin(r, clientIPFromRequest(r)) {
		case adminAuthOK:
			next(w, r)
		case adminAuthRateLimited:
			writeJSONError(w, http.StatusTooManyRequests, "认证失败次数过多，请 5 分钟后再试")
		default:
			writeJSONError(w, http.StatusUnauthorized, "未登录或管理会话已过期，请先登录")
		}
	}
}

func (gs *GatewayServer) withNorthboundAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r, true)
		// Fast path for standard API keys (cached in memory) before checking admin credentials
		if gs.store != nil && token != "" && !strings.HasPrefix(token, "ag_sess_") {
			if rec, ok := gs.store.ValidateAPIKey(token); ok {
				if !gs.allowAPIKeyRequest(rec) {
					writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded (RPM)")
					return
				}
				ctx := context.WithValue(r.Context(), ctxKeyAPIKey, rec)
				next(w, r.WithContext(ctx))
				return
			}
		}
		if token != "" {
			bucket := "nb:" + clientIPFromRequest(r)
			if gs.isValidAdminSession(token) {
				next(w, r)
				return
			}
			if !gs.checkLoginRateLimit(bucket) {
				writeJSONError(w, http.StatusTooManyRequests, "too many invalid API key attempts, retry in 5 minutes")
				return
			}
			if !strings.HasPrefix(token, "ag_sess_") && gs.verifyAdminToken(token) {
				next(w, r)
				return
			}
			gs.recordLoginFailure(bucket)
			writeJSONError(w, http.StatusUnauthorized, "invalid or missing API key")
			return
		}
		// Before the first API key is created, allow unauthenticated bootstrap only from
		// this host; remote callers must use an API key or the admin token.
		if gs.store != nil && !gs.store.HasAnyAPIKeys() && isLoopbackRequest(r) {
			next(w, r)
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "invalid or missing API key")
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeProtocolError writes an error body in the shape the northbound protocol's
// SDKs parse (Anthropic uses a top-level "type":"error" envelope).
func writeProtocolError(w http.ResponseWriter, protocol string, status int, message string) {
	if protocol != "claude" {
		writeJSONError(w, status, message)
		return
	}
	errType := "api_error"
	switch status {
	case http.StatusBadRequest:
		errType = "invalid_request_error"
	case http.StatusUnauthorized:
		errType = "authentication_error"
	case http.StatusForbidden:
		errType = "permission_error"
	case http.StatusNotFound:
		errType = "not_found_error"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
	case http.StatusServiceUnavailable:
		errType = "overloaded_error"
	}
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "gateway_error",
		},
	})
}
