package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
)

func newSecurityTestServer(t *testing.T) (*GatewayServer, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.NewStore(filepath.Join(dir, "gateway.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{Server: config.ServerConfig{
		Host:          "127.0.0.1",
		DataDir:       dir,
		AdminToken:    "admin-token-0123456789",
		AdminPassword: "admin-password-01",
	}}
	return NewGatewayServer(cfg, store, nil, nil, nil, nil), store
}

func TestAdminPasswordIsNotABearerToken(t *testing.T) {
	gs, _ := newSecurityTestServer(t)
	h := gs.withAdminAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, tc := range []struct {
		bearer string
		want   int
	}{
		{"admin-token-0123456789", http.StatusOK},
		{"admin-password-01", http.StatusUnauthorized},
		{"", http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
		if tc.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("bearer %q: got %d, want %d", tc.bearer, rec.Code, tc.want)
		}
	}

	if !gs.verifyAdminLoginCredential("admin-password-01") {
		t.Fatalf("password must still be accepted by the login endpoint")
	}
}

func TestAdminBearerGuessesAreRateLimited(t *testing.T) {
	gs, _ := newSecurityTestServer(t)
	h := gs.withAdminAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	var last int
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
		req.Header.Set("Authorization", "Bearer wrong-guess")
		rec := httptest.NewRecorder()
		h(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated bad bearer tokens, got %d", last)
	}
}

func TestAdminRejectsCrossOriginStateChange(t *testing.T) {
	gs, _ := newSecurityTestServer(t)
	h := gs.withAdminAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodPost, "http://gw.local/api/admin/workers/scale", nil)
	req.Header.Set("Authorization", "Bearer admin-token-0123456789")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin admin POST, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "http://gw.local/api/admin/workers/scale", nil)
	req.Header.Set("Authorization", "Bearer admin-token-0123456789")
	req.Header.Set("Origin", "http://gw.local")
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected same-origin admin POST to pass, got %d", rec.Code)
	}
}

func TestNorthboundBootstrapOnlyFromLoopback(t *testing.T) {
	gs, _ := newSecurityTestServer(t)
	h := gs.withNorthboundAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected loopback bootstrap to be allowed with no API keys, got %d", rec.Code)
	}

	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.RemoteAddr = "203.0.113.7:5555" },
		func(r *http.Request) { r.RemoteAddr = "127.0.0.1:5555"; r.Header.Set("X-Forwarded-For", "203.0.113.7") },
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		mutate(req)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected remote unauthenticated bootstrap to be rejected, got %d", rec.Code)
		}
	}
}

func TestBoundKeyCannotSelectAnotherAccount(t *testing.T) {
	gs, _ := newSecurityTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?account_id=other", nil)
	req.Header.Set("X-ACP-Account-ID", "other")
	ctx := context.WithValue(req.Context(), ctxKeyAPIKey, &storage.APIKey{ID: 7, BoundAccountID: "acc_bound"})
	acc, failover := gs.resolvePreferredAccount(req.WithContext(ctx))
	if acc != "acc_bound" || failover {
		t.Fatalf("bound key must stay on its account, got %q failover=%v", acc, failover)
	}

	ctx = context.WithValue(req.Context(), ctxKeyAPIKey, &storage.APIKey{ID: 8})
	acc, _ = gs.resolvePreferredAccount(req.WithContext(ctx))
	if acc != "other" {
		t.Fatalf("unbound key may choose an account, got %q", acc)
	}
}

func TestACPWebSocketCwdIsConfinedToWorkspace(t *testing.T) {
	gs, _ := newSecurityTestServer(t)
	ws := t.TempDir()
	inside := filepath.Join(ws, "sub")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	rewrite := func(cwd string, extra []string) map[string]any {
		raw, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "session/new",
			"params": map[string]any{"cwd": cwd, "additionalDirectories": extra},
		})
		var out map[string]any
		_ = json.Unmarshal(gs.rewriteRemoteACPCwdIfNeeded(raw, ws), &out)
		return out["params"].(map[string]any)
	}

	resolvedWS, _ := filepath.EvalSymlinks(ws)
	if p := rewrite(outside, nil); p["cwd"] != ws {
		t.Fatalf("existing directory outside workspace must be rewritten, got %v", p["cwd"])
	}
	if p := rewrite("/etc/../etc", nil); p["cwd"] != ws {
		t.Fatalf("traversal path must be rewritten, got %v", p["cwd"])
	}
	p := rewrite(inside, []string{outside, inside})
	if got := p["cwd"].(string); got != inside && !strings.HasPrefix(got, resolvedWS) {
		t.Fatalf("path inside workspace must be kept, got %v", got)
	}
	dirs := p["additionalDirectories"].([]any)
	if len(dirs) != 1 || dirs[0] != inside {
		t.Fatalf("additionalDirectories outside workspace must be dropped, got %v", dirs)
	}
}

func TestAdminSecretHashRoundTripAndLegacyMigration(t *testing.T) {
	enc, err := hashAdminSecret("s3cret-value")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyAdminSecretHash("s3cret-value", enc) || verifyAdminSecretHash("other", enc) {
		t.Fatalf("PBKDF2 hash round trip failed")
	}
	enc2, _ := hashAdminSecret("s3cret-value")
	if enc == enc2 {
		t.Fatalf("hashes must be salted")
	}

	gs, store := newSecurityTestServer(t)
	_ = store.SetSetting(adminCredentialsLockedKey, "1")
	_ = store.SetSetting(adminPassSettingKey, sha256Hex("legacy-password"))
	if !gs.verifyAdminPassword("legacy-password") {
		t.Fatalf("legacy sha256 password must still verify")
	}
	if v, ok := store.GetSetting(adminPassHashSettingKey); !ok || !strings.HasPrefix(v, "pbkdf2-sha256$") {
		t.Fatalf("legacy hash must be migrated to PBKDF2, got %q", v)
	}
	if _, ok := store.GetSetting(adminPassSettingKey); ok {
		t.Fatalf("legacy hash must be cleared after migration")
	}
	if !gs.verifyAdminPassword("legacy-password") || gs.verifyAdminPassword("nope") {
		t.Fatalf("migrated hash must verify")
	}
}

func TestResolveThinkingLevelHonoursLow(t *testing.T) {
	gs, _ := newSecurityTestServer(t)
	if got := gs.resolveThinkingLevel("claude-sonnet-4-5", "low"); got != "low" {
		t.Fatalf("explicit low must be honoured, got %q", got)
	}
	if got := gs.resolveThinkingLevel("claude-sonnet-4-5", ""); got != "high" {
		t.Fatalf("unset must default to high, got %q", got)
	}
}
