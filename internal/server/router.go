package server

import (
	"context"
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
)

type ctxKey string

const (
	ctxKeyAPIKey              ctxKey = "api_key_record"
	adminSessionCookie               = "ag_admin_session"
	adminPassSettingKey              = "admin_password_sha256"
	adminTokenSettingKey             = "admin_token_sha256"
	adminCredentialsLockedKey        = "admin_credentials_locked"
	adminCredentialsFileName         = "admin_credentials.txt"
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
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})

	// Internal loopback MCP server for tool parking & resuming
	var wt acp.WiretapCallback
	if gs.hub != nil {
		wt = gs.hub.Broadcast
	}
	mux.Handle("/mcp/sessions/", acp.MCPBridgeHandler(gs.sm, wt))

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

func extractToken(r *http.Request) string {
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
	if qToken := strings.TrimSpace(r.URL.Query().Get("token")); qToken != "" {
		return qToken
	}
	if qKey := strings.TrimSpace(r.URL.Query().Get("key")); qKey != "" {
		return qKey
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
	if savedPassHash, ok := gs.store.GetSetting(adminPassSettingKey); ok && strings.TrimSpace(savedPassHash) != "" {
		return true
	}
	return false
}

func (gs *GatewayServer) writeAdminCredentialsFile(password, token string, locked bool) {
	filePath := gs.AdminCredentialsFilePath()
	_ = os.MkdirAll(filepath.Dir(filePath), 0755)
	var content string
	nowStr := time.Now().Format(time.RFC3339)
	if locked {
		content = fmt.Sprintf(
			"# Antigravity ACP Gateway — Admin Credentials\n"+
				"STATUS=LOCKED_BY_USER\n"+
				"UPDATED_AT=%s\n"+
				"NOTE=管理员密码与 Token 已由用户主动修改并永久固定（SHA-256 哈希存储于 gateway.db settings 表），重部署不会再重置。\n",
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
	pwd := strings.TrimSpace(gs.cfg.GetAdminPassword())
	tok := strings.TrimSpace(gs.cfg.GetAdminToken())
	log.Printf("==============================================================================")
	log.Printf("[Admin Auth] 本次部署已动态生成管理凭证 (用户主动修改后将永久固定):")
	log.Printf("  -> Web 登录密码 (Admin Password) : %s", pwd)
	log.Printf("  -> API 管理令牌 (Admin Token)    : %s", tok)
	log.Printf("  -> 凭证保存位置 (Credentials File): %s", credFile)
	log.Printf("  -> 提示: 登录 Web 控制台点击右上角「修改密码」主动修改后即永久固定，重部署不再变更。")
	log.Printf("==============================================================================")
}

func (gs *GatewayServer) verifyAdminCredential(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	candHash := sha256Hex(candidate)

	// 1. If the user has actively modified and locked credentials in SQLite, strictly verify against DB hashes only
	if gs.store != nil && gs.isAdminCredentialsLocked() {
		if savedPassHash, ok := gs.store.GetSetting(adminPassSettingKey); ok && savedPassHash != "" {
			if subtle.ConstantTimeCompare([]byte(candHash), []byte(savedPassHash)) == 1 {
				return true
			}
		}
		if savedTokHash, ok := gs.store.GetSetting(adminTokenSettingKey); ok && savedTokHash != "" {
			if subtle.ConstantTimeCompare([]byte(candHash), []byte(savedTokHash)) == 1 {
				return true
			}
		}
		return false
	}

	// 2. Otherwise check active runtime AdminPassword (dynamically generated on deployment)
	expectedPwd := strings.TrimSpace(gs.cfg.GetAdminPassword())
	if expectedPwd != "" && !config.IsDynamicCredentialValue(expectedPwd) &&
		subtle.ConstantTimeCompare([]byte(candHash), []byte(sha256Hex(expectedPwd))) == 1 {
		return true
	}

	// 3. Check active runtime AdminToken (dynamically generated on deployment)
	expectedToken := strings.TrimSpace(gs.cfg.GetAdminToken())
	if expectedToken != "" && !config.IsDynamicCredentialValue(expectedToken) &&
		subtle.ConstantTimeCompare([]byte(candHash), []byte(sha256Hex(expectedToken))) == 1 {
		return true
	}

	return false
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

func (gs *GatewayServer) isAdminAuthenticated(r *http.Request) bool {
	token := extractToken(r)
	if token != "" {
		if gs.isValidAdminSession(token) || gs.verifyAdminCredential(token) {
			return true
		}
	}
	if c, err := r.Cookie(adminSessionCookie); err == nil {
		cookieToken := strings.TrimSpace(c.Value)
		if cookieToken != "" && cookieToken != token {
			if gs.isValidAdminSession(cookieToken) || gs.verifyAdminCredential(cookieToken) {
				return true
			}
		}
	}
	return false
}

func (gs *GatewayServer) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gs.isAdminAuthenticated(r) {
			writeJSONError(w, http.StatusUnauthorized, "未登录或管理会话已过期，请先登录")
			return
		}
		next(w, r)
	}
}

func (gs *GatewayServer) withNorthboundAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
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
		// Allow admin token/session directly
		if token != "" && (gs.isValidAdminSession(token) || gs.verifyAdminCredential(token)) {
			next(w, r)
			return
		}
		// If no API keys exist in DB yet, allow unauthenticated local bootstrap
		if gs.store != nil && !gs.store.HasAnyAPIKeys() && token == "" {
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

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "gateway_error",
		},
	})
}
