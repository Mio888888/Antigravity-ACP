package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"antigravity-gateway/internal/storage"
	"antigravity-gateway/internal/version"
)

func buildTarGzArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0755,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar write header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar write body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func buildZipArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.6.0", "v1.6.0", 0},
		{"1.6.0", "v1.6.0", 0},
		{"v1.6.0-dirty", "v1.6.0", 0},
		{"v1.6.0", "v1.6.1", -1},
		{"v1.6.1", "v1.6.0", 1},
		{"v1.9.9", "v1.10.0", -1},
		{"v1.6.0-rc.1", "v1.6.0", -1},
		{"v1.6.0", "v1.6.0-rc.1", 1},
	}
	for _, tc := range cases {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestGatewayUpdateLifecycleAndFailClosedVerification(t *testing.T) {
	selfExec, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	realBinBytes, err := os.ReadFile(selfExec)
	if err != nil {
		t.Fatalf("read test executable: %v", err)
	}

	tmpDir := t.TempDir()
	store, err := storage.NewStore(filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer store.Close()

	execDir := filepath.Join(tmpDir, "install")
	if err := os.MkdirAll(execDir, 0755); err != nil {
		t.Fatalf("mkdir install: %v", err)
	}
	targetExec := filepath.Join(execDir, expectedGatewayBinaryName(runtime.GOOS))
	origSentinel := []byte("ORIGINAL_SENTINEL_BINARY_CONTENT")
	if err := os.WriteFile(targetExec, origSentinel, 0755); err != nil {
		t.Fatalf("write sentinel targetExec: %v", err)
	}

	assetName := expectedGatewayAssetName(runtime.GOOS, runtime.GOARCH)
	gwBinName := expectedGatewayBinaryName(runtime.GOOS)

	var validArchive []byte
	var invalidHeaderArchive []byte
	if runtime.GOOS == "windows" {
		validArchive = buildZipArchive(t, map[string][]byte{gwBinName: realBinBytes})
		invalidHeaderArchive = buildZipArchive(t, map[string][]byte{gwBinName: []byte("NOT_A_VALID_PE_OR_ELF_OR_MACHO")})
	} else {
		validArchive = buildTarGzArchive(t, map[string][]byte{gwBinName: realBinBytes})
		invalidHeaderArchive = buildTarGzArchive(t, map[string][]byte{gwBinName: []byte("NOT_A_VALID_PE_OR_ELF_OR_MACHO")})
	}

	validSHA := sha256.Sum256(validArchive)
	validSHAHex := hex.EncodeToString(validSHA[:])
	invalidHeaderSHA := sha256.Sum256(invalidHeaderArchive)
	invalidHeaderSHAHex := hex.EncodeToString(invalidHeaderSHA[:])

	mode := "no_release"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/Mio888888/Antigravity-ACP/releases/latest":
			if mode == "no_release" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			rel := githubReleasePayload{
				TagName:     "v99.1.0",
				HTMLURL:     srv.URL + "/releases/tag/v99.1.0",
				PublishedAt: "2026-11-11T00:00:00Z",
				Body:        "Automated test release",
				Assets: []githubReleaseAsset{
					{
						Name:               assetName,
						Size:               int64(len(validArchive)),
						BrowserDownloadURL: srv.URL + "/download/" + assetName,
					},
					{
						Name:               "SHA256SUMS",
						Size:               128,
						BrowserDownloadURL: srv.URL + "/download/SHA256SUMS",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rel)
		case r.URL.Path == "/download/SHA256SUMS":
			switch mode {
			case "bad_checksum":
				fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), assetName)
			case "bad_binary_header":
				fmt.Fprintf(w, "%s  %s\n", invalidHeaderSHAHex, assetName)
			default:
				fmt.Fprintf(w, "%s  %s\n", validSHAHex, assetName)
			}
		case r.URL.Path == "/download/"+assetName:
			if mode == "bad_binary_header" {
				_, _ = w.Write(invalidHeaderArchive)
				return
			}
			_, _ = w.Write(validArchive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	u := NewUpdater(filepath.Join(tmpDir, "data"), store)
	u.Repo = "Mio888888/Antigravity-ACP"
	u.GitHubAPIBase = srv.URL
	u.CLIManifestBase = srv.URL + "/cli-manifests"
	u.AllowInsecureHTTP = true
	u.ExecPathOverride = targetExec

	ctx := context.Background()

	// 1. 404 when repository has no GitHub Releases yet
	stNoRel := u.GetStatus(ctx, "cli", "", true)
	if stNoRel.Gateway.ReleaseAvailable || stNoRel.Gateway.UpdateAvailable {
		t.Fatalf("expected no release available on 404, got %+v", stNoRel.Gateway)
	}
	if stNoRel.Gateway.CheckError != "" {
		t.Fatalf("expected empty CheckError on 404 no-release state, got %q", stNoRel.Gateway.CheckError)
	}

	// 2. Checksum mismatch must fail closed without touching existing executable
	mode = "bad_checksum"
	if _, err := u.Apply(ctx, "gateway", "cli", "", nil); err == nil || !strings.Contains(err.Error(), "SHA-256 校验失败") {
		t.Fatalf("expected SHA-256 mismatch error, got: %v", err)
	}
	afterBadChecksum, _ := os.ReadFile(targetExec)
	if !bytes.Equal(afterBadChecksum, origSentinel) {
		t.Fatalf("existing binary was modified despite SHA-256 mismatch")
	}

	// 3. Invalid Mach-O/ELF/PE binary header must fail closed without touching existing executable
	mode = "bad_binary_header"
	if _, err := u.Apply(ctx, "gateway", "cli", "", nil); err == nil || !strings.Contains(err.Error(), "可执行文件头校验") {
		t.Fatalf("expected executable header validation error, got: %v", err)
	}
	afterBadHeader, _ := os.ReadFile(targetExec)
	if !bytes.Equal(afterBadHeader, origSentinel) {
		t.Fatalf("existing binary was modified despite invalid binary header")
	}

	// 4. Valid SHA-256 + valid platform executable header succeeds and replaces binary atomically
	mode = "valid"
	stValid := u.GetStatus(ctx, "cli", "", true)
	if !stValid.Gateway.ReleaseAvailable || !stValid.Gateway.UpdateAvailable || !stValid.Gateway.CanSelfUpdate {
		t.Fatalf("expected gateway update available and self-updatable, got %+v", stValid.Gateway)
	}
	res, err := u.Apply(ctx, "gateway", "cli", "", nil)
	if err != nil {
		t.Fatalf("Apply gateway valid release failed: %v", err)
	}
	if !res.UpdatedGateway || res.GatewayVersion != "v99.1.0" || !res.Restarting {
		t.Fatalf("unexpected ApplyResult: %+v", res)
	}
	updatedBytes, err := os.ReadFile(targetExec)
	if err != nil {
		t.Fatalf("read updated targetExec: %v", err)
	}
	if !bytes.Equal(updatedBytes, realBinBytes) {
		t.Fatalf("updated binary bytes do not match extracted release binary (%d != %d)", len(updatedBytes), len(realBinBytes))
	}
}

func TestACPHotUpdateCLIAndZed(t *testing.T) {
	selfExec, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	realBinBytes, err := os.ReadFile(selfExec)
	if err != nil {
		t.Fatalf("read test executable: %v", err)
	}

	tmpDir := t.TempDir()
	fakeHome := filepath.Join(tmpDir, "home")
	fakePath := filepath.Join(tmpDir, "empty-path")
	if err := os.MkdirAll(fakeHome, 0755); err != nil {
		t.Fatalf("mkdir fakeHome: %v", err)
	}
	if err := os.MkdirAll(fakePath, 0755); err != nil {
		t.Fatalf("mkdir fakePath: %v", err)
	}
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	t.Setenv("LOCALAPPDATA", filepath.Join(fakeHome, "AppData", "Local"))
	t.Setenv("APPDATA", filepath.Join(fakeHome, "AppData", "Roaming"))
	t.Setenv("PATH", fakePath)

	dataDir := filepath.Join(tmpDir, "data")
	store, err := storage.NewStore(filepath.Join(dataDir, "gateway.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	cliBinEntry := "antigravity"
	if runtime.GOOS == "windows" {
		cliBinEntry = "antigravity.exe"
	}
	cliTarGz := buildTarGzArchive(t, map[string][]byte{
		"pkg/" + cliBinEntry: realBinBytes,
	})
	cliSHA512 := sha512.Sum512(cliTarGz)
	cliSHA512Hex := hex.EncodeToString(cliSHA512[:])

	zedParPayload := []byte("#!/usr/bin/env python3\nprint('mock zed acp par')\n")
	zedZip := buildZipArchive(t, map[string][]byte{
		"agy_acp_server.par": zedParPayload,
	})
	zedSHA256 := sha256.Sum256(zedZip)
	zedSHA256Hex := hex.EncodeToString(zedSHA256[:])

	platKey, err := cliPlatformKey(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("platform %s/%s unsupported for CLI manifest test: %v", runtime.GOOS, runtime.GOARCH, err)
	}
	zedTarget, err := zedRegistryTarget(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("platform %s/%s unsupported for Zed registry test: %v", runtime.GOOS, runtime.GOARCH, err)
	}

	var cliDownloadHits atomic.Int32
	var zedDownloadHits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifests/" + platKey + ".json":
			_ = json.NewEncoder(w).Encode(cliManifestPayload{
				Version: "2.4.0",
				URL:     srv.URL + "/downloads/antigravity-cli.tar.gz",
				SHA512:  cliSHA512Hex,
			})
		case "/downloads/antigravity-cli.tar.gz":
			cliDownloadHits.Add(1)
			_, _ = w.Write(cliTarGz)
		case "/registry.json":
			resp := map[string]any{
				"agents": []map[string]any{
					{
						"id":      "antigravity-acp",
						"version": "0.9.5",
						"distribution": map[string]any{
							"binary": map[string]any{
								zedTarget: map[string]string{
									"archive": srv.URL + "/downloads/zed-acp.zip",
									"sha256":  zedSHA256Hex,
								},
							},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/downloads/zed-acp.zip":
			zedDownloadHits.Add(1)
			_, _ = w.Write(zedZip)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	u := NewUpdater(dataDir, store)
	u.GitHubAPIBase = srv.URL
	u.CLIManifestBase = srv.URL + "/manifests"
	u.ZedRegistryURL = srv.URL + "/registry.json"
	u.AllowInsecureHTTP = true

	ctx := context.Background()

	// Test 1: CLI hot-update writes into <dataDir>/bin/agy and persists override path in SQLite
	var callbackPath string
	resCLI, err := u.Apply(ctx, "acp", "cli", filepath.Join(tmpDir, "missing", "agy"), func(flavor, newBinPath string) int {
		if flavor != "cli" {
			t.Errorf("expected cli flavor, got %s", flavor)
		}
		callbackPath = newBinPath
		return 3
	})
	if err != nil {
		t.Fatalf("Apply CLI ACP hot update failed: %v", err)
	}
	if !resCLI.UpdatedACP || resCLI.ACPVersion != "v2.4.0" || resCLI.RetiredWorkers != 3 {
		t.Fatalf("unexpected CLI ApplyResult: %+v", resCLI)
	}
	savedOverride, ok := store.GetSetting(SettingACPOverrideBinPath)
	if !ok || savedOverride != callbackPath {
		t.Fatalf("expected persisted ACP override %q, got %q (ok=%v)", callbackPath, savedOverride, ok)
	}

	// Subsequent GetStatus on the freshly installed CLI binary should recognize stored SHA-256 metadata as up-to-date
	stCLI := u.GetStatus(ctx, "cli", savedOverride, true)
	if stCLI.ACP.CurrentVersion != "v2.4.0" || stCLI.ACP.UpdateAvailable {
		t.Fatalf("expected CLI ACP to report v2.4.0 and up-to-date, got %+v", stCLI.ACP)
	}

	// Re-applying CLI update when the same version is already installed must skip downloading and rotating workers
	callbackCalledAgain := false
	resCLISkip, err := u.Apply(ctx, "acp", "cli", savedOverride, func(_, _ string) int {
		callbackCalledAgain = true
		return 1
	})
	if err != nil {
		t.Fatalf("Apply CLI same-version skip failed: %v", err)
	}
	if resCLISkip.UpdatedACP || callbackCalledAgain || cliDownloadHits.Load() != 1 {
		t.Fatalf("expected CLI same-version update to be skipped without re-downloading, got res=%+v callback=%v downloads=%d",
			resCLISkip, callbackCalledAgain, cliDownloadHits.Load())
	}

	// Test 2: Zed ACP hot-update verifies SHA-256 and installs agy_acp_server.par
	resZed, err := u.Apply(ctx, "acp", "zed", filepath.Join(tmpDir, "missing", "agy_acp_server.par"), nil)
	if err != nil {
		t.Fatalf("Apply Zed ACP hot update failed: %v", err)
	}
	if !resZed.UpdatedACP || resZed.ACPVersion != "v0.9.5" {
		t.Fatalf("unexpected Zed ApplyResult: %+v", resZed)
	}
	gotPar, err := os.ReadFile(resZed.ACPBinaryPath)
	if err != nil || !bytes.Equal(gotPar, zedParPayload) {
		t.Fatalf("installed agy_acp_server.par mismatch: err=%v", err)
	}

	// Re-applying Zed update (even without SQLite metadata, via .zed-acp-version marker) must skip re-downloading
	_ = store.SetSetting(SettingACPInstalledMeta, "")
	resZedSkip, err := u.Apply(ctx, "acp", "zed", resZed.ACPBinaryPath, nil)
	if err != nil {
		t.Fatalf("Apply Zed same-version skip failed: %v", err)
	}
	if resZedSkip.UpdatedACP || zedDownloadHits.Load() != 1 {
		t.Fatalf("expected Zed same-version update to be skipped without re-downloading, got res=%+v downloads=%d",
			resZedSkip, zedDownloadHits.Load())
	}
}

func TestGatewaySkipUpdateWhenSameVersionInstalled(t *testing.T) {
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, expectedGatewayBinaryName(runtime.GOOS))
	if err := os.WriteFile(execPath, []byte("existing-gateway-binary"), 0755); err != nil {
		t.Fatalf("write execPath: %v", err)
	}

	var archiveHits atomic.Int32
	assetName := expectedGatewayAssetName(runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Mio888888/Antigravity-ACP/releases/latest":
			_ = json.NewEncoder(w).Encode(githubReleasePayload{
				TagName: strings.TrimPrefix(version.Version, "v"),
				HTMLURL: srv.URL + "/releases/tag/" + version.Version,
				Assets: []githubReleaseAsset{
					{Name: assetName, Size: 128, BrowserDownloadURL: srv.URL + "/dl/" + assetName},
					{Name: "SHA256SUMS", Size: 64, BrowserDownloadURL: srv.URL + "/dl/SHA256SUMS"},
				},
			})
		default:
			archiveHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	u := NewUpdater(tmpDir, nil)
	u.Repo = "Mio888888/Antigravity-ACP"
	u.GitHubAPIBase = srv.URL
	u.AllowInsecureHTTP = true
	u.ExecPathOverride = execPath

	res, err := u.Apply(context.Background(), "gateway", "cli", "", nil)
	if err != nil {
		t.Fatalf("Apply gateway same-version skip failed: %v", err)
	}
	if res.UpdatedGateway || res.Restarting || archiveHits.Load() != 0 {
		t.Fatalf("expected gateway same-version update to be skipped without downloading, got res=%+v hits=%d",
			res, archiveHits.Load())
	}
}

func TestResolveACPInstallDirProtectsZedExternalRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	zedExtDir := filepath.Join(tmpDir, "Zed", "external_agents", "registry", "antigravity-acp", "v_1.4.5_abc")
	if err := os.MkdirAll(zedExtDir, 0755); err != nil {
		t.Fatalf("mkdir zedExtDir: %v", err)
	}
	zedExtBin := filepath.Join(zedExtDir, "agy_acp_server.par")
	if err := os.WriteFile(zedExtBin, []byte("zed-managed"), 0755); err != nil {
		t.Fatalf("write zedExtBin: %v", err)
	}

	dataDir := filepath.Join(tmpDir, "data")
	u := NewUpdater(dataDir, nil)
	installDir, ok, reason := u.resolveACPInstallDir(zedExtBin)
	if !ok {
		t.Fatalf("expected resolveACPInstallDir to succeed via dataDir/bin, got ok=false reason=%q", reason)
	}
	wantDir, _ := filepath.Abs(filepath.Join(dataDir, "bin"))
	if filepath.Clean(installDir) != filepath.Clean(wantDir) {
		t.Fatalf("expected installDir %q instead of Zed registry dir, got %q", wantDir, installDir)
	}
}
