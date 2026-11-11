package server_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/server"
	"antigravity-gateway/internal/storage"
	"antigravity-gateway/internal/updater"
	"antigravity-gateway/internal/version"
)

func makeGatewayArchiveForCurrentOS(t *testing.T, payload []byte) ([]byte, string) {
	t.Helper()
	binName := "antigravity-gateway"
	assetName := fmt.Sprintf("antigravity-gateway-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		binName = "antigravity-gateway.exe"
		assetName = fmt.Sprintf("antigravity-gateway-%s-%s.zip", runtime.GOOS, runtime.GOARCH)
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(binName)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("zip write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
		return buf.Bytes(), assetName
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     binName,
		Mode:     0755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes(), assetName
}

func TestAdminUpdateEndpointsAuthAndRestartHook(t *testing.T) {
	selfExec, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	realBin, err := os.ReadFile(selfExec)
	if err != nil {
		t.Fatalf("read selfExec: %v", err)
	}
	archiveBytes, assetName := makeGatewayArchiveForCurrentOS(t, realBin)
	sum := sha256.Sum256(archiveBytes)
	sumHex := hex.EncodeToString(sum[:])

	var ghSrv *httptest.Server
	ghSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Mio888888/Antigravity-ACP/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     "v99.2.0",
				"html_url":     ghSrv.URL + "/releases/tag/v99.2.0",
				"published_at": "2026-11-11T00:00:00Z",
				"body":         "Release notes",
				"assets": []map[string]any{
					{
						"name":                 assetName,
						"size":                 len(archiveBytes),
						"browser_download_url": ghSrv.URL + "/dl/" + assetName,
					},
					{
						"name":                 "SHA256SUMS",
						"size":                 96,
						"browser_download_url": ghSrv.URL + "/dl/SHA256SUMS",
					},
				},
			})
		case "/dl/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n", sumHex, assetName)
		case "/dl/" + assetName:
			_, _ = w.Write(archiveBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ghSrv.Close()

	tmpDir := t.TempDir()
	adminToken := "adm-test-update-token-123456"
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:          "127.0.0.1",
			Port:          18099,
			DataDir:       filepath.Join(tmpDir, "data"),
			AdminToken:    adminToken,
			AdminPassword: "test-password-1234",
		},
		ACPPool: config.ACPPoolConfig{
			BackendType:  "cli",
			WorkspaceDir: filepath.Join(tmpDir, "ws"),
		},
		PromptPolicy: config.PromptPolicyConfig{
			Mode: "pure_passthrough",
		},
	}

	store, err := storage.NewStore(filepath.Join(cfg.Server.DataDir, "gateway.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	hub := server.NewWiretapHub()
	pool, err := acp.NewMultiAccountPool(cfg.Server.DataDir, cfg.ACPPool, cfg.SandboxPolicy, nil, hub.Broadcast)
	if err != nil {
		t.Fatalf("NewMultiAccountPool: %v", err)
	}
	defer pool.Close()
	sm := acp.NewSessionManager(cfg.ACPPool.WorkspaceDir, 5*time.Minute)
	defer sm.Close()

	gs := server.NewGatewayServer(cfg, store, pool, sm, hub, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>ok</html>")},
	})

	targetExec := filepath.Join(tmpDir, "bin", filepath.Base(selfExec))
	if err := os.MkdirAll(filepath.Dir(targetExec), 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(targetExec, []byte("old-binary"), 0755); err != nil {
		t.Fatalf("seed targetExec: %v", err)
	}

	upd := updater.NewUpdater(cfg.Server.DataDir, store)
	upd.Repo = "Mio888888/Antigravity-ACP"
	upd.GitHubAPIBase = ghSrv.URL
	upd.CLIManifestBase = ghSrv.URL + "/cli"
	upd.AllowInsecureHTTP = true
	upd.ExecPathOverride = targetExec
	gs.SetUpdater(upd)

	restartedCh := make(chan string, 1)
	gs.SetRestartFunc(func(execPath string) {
		select {
		case restartedCh <- execPath:
		default:
		}
	})

	apiSrv := httptest.NewServer(gs.Handler())
	defer apiSrv.Close()

	// 1. /healthz exposes current gateway version
	hzResp, err := http.Get(apiSrv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer hzResp.Body.Close()
	var hz map[string]any
	if err := json.NewDecoder(hzResp.Body).Decode(&hz); err != nil {
		t.Fatalf("decode /healthz: %v", err)
	}
	if hz["version"] != version.Version {
		t.Fatalf("expected /healthz version=%q, got %v", version.Version, hz["version"])
	}

	// 2. Unauthenticated requests to update endpoints must be rejected with 401
	unauthResp, err := http.Get(apiSrv.URL + "/api/admin/update/status")
	if err != nil {
		t.Fatalf("unauth GET status: %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated update status, got %d", unauthResp.StatusCode)
	}

	// 2b. Cross-origin POST to /api/admin/update/apply must be rejected with 403 Forbidden
	csrfReq, _ := http.NewRequest(
		http.MethodPost,
		apiSrv.URL+"/api/admin/update/apply",
		strings.NewReader(`{"target":"gateway"}`),
	)
	csrfReq.Header.Set("Authorization", "Bearer "+adminToken)
	csrfReq.Header.Set("Origin", "https://evil.example.com")
	csrfResp, err := http.DefaultClient.Do(csrfReq)
	if err != nil {
		t.Fatalf("csrf POST apply: %v", err)
	}
	_ = csrfResp.Body.Close()
	if csrfResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-origin update apply, got %d", csrfResp.StatusCode)
	}

	// 3. Authenticated GET /api/admin/update/status?check=1 returns release info
	stReq, _ := http.NewRequest(http.MethodGet, apiSrv.URL+"/api/admin/update/status?check=1", nil)
	stReq.Header.Set("Authorization", "Bearer "+adminToken)
	stResp, err := http.DefaultClient.Do(stReq)
	if err != nil {
		t.Fatalf("auth GET status: %v", err)
	}
	defer stResp.Body.Close()
	if stResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on status, got %d", stResp.StatusCode)
	}
	var stPayload updater.StatusResponse
	if err := json.NewDecoder(stResp.Body).Decode(&stPayload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !stPayload.Gateway.UpdateAvailable || stPayload.Gateway.LatestVersion != "v99.2.0" {
		t.Fatalf("expected gateway update v99.2.0 available, got %+v", stPayload.Gateway)
	}

	// 4. Authenticated POST /api/admin/update/apply replaces binary and triggers asynchronous restartFn
	applyReq, _ := http.NewRequest(
		http.MethodPost,
		apiSrv.URL+"/api/admin/update/apply",
		strings.NewReader(`{"target":"gateway"}`),
	)
	applyReq.Header.Set("Authorization", "Bearer "+adminToken)
	applyReq.Header.Set("Content-Type", "application/json")
	applyResp, err := http.DefaultClient.Do(applyReq)
	if err != nil {
		t.Fatalf("auth POST apply: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on apply, got %d", applyResp.StatusCode)
	}
	var applyRes updater.ApplyResult
	if err := json.NewDecoder(applyResp.Body).Decode(&applyRes); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if !applyRes.UpdatedGateway || !applyRes.Restarting || applyRes.GatewayVersion != "v99.2.0" {
		t.Fatalf("unexpected apply response: %+v", applyRes)
	}

	select {
	case gotExec := <-restartedCh:
		if filepath.Clean(gotExec) != filepath.Clean(targetExec) {
			t.Fatalf("restartFn received %q, want %q", gotExec, targetExec)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for asynchronous restartFn callback")
	}
}
