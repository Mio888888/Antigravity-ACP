package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	gatewayembed "antigravity-gateway"
	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/acp/ctxpatch"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/server"
	"antigravity-gateway/internal/storage"
	"antigravity-gateway/internal/updater"
	"antigravity-gateway/internal/version"
)

func main() {
	updater.CleanupStaleExecutableArtifacts("")
	cfgPath := flag.String("config", "config/config.yaml", "Path to gateway config.yaml")
	backendFlag := flag.String("backend", "", "ACP backend type override: auto | zed | zen | cli")
	acpBinFlag := flag.String("acp-bin", "", "Explicit path to ACP backend binary (agy_acp_server.par or agy)")
	syncOnlyFlag := flag.Bool("sync-only", false, "Run continuous transcript.jsonl -> gateway.db token reconciler")
	patchACPFlag := flag.String("patch-acp", "", "Apply the context-window patch to the given agy binary and exit (used by deploy scripts)")
	patchTokensFlag := flag.Int64("patch-tokens", -1, "Context ceiling in tokens for --patch-acp (0 = full window, <0 = use config default)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("Failed to load config %s: %v", *cfgPath, err)
	}
	if *backendFlag != "" {
		cfg.ACPPool.BackendType = *backendFlag
	}
	if *acpBinFlag != "" {
		cfg.ACPPool.BinaryPath = *acpBinFlag
	}

	// One-shot patch mode for deploy/update scripts: patch the given agy binary and exit.
	if strings.TrimSpace(*patchACPFlag) != "" {
		tokens := cfg.ACPPool.ContextPatchTokens
		if *patchTokensFlag >= 0 {
			tokens = *patchTokensFlag
		}
		target := *patchACPFlag
		if !ctxpatch.IsCLIBinary(target) {
			log.Printf("[ctxpatch] %s is not an Antigravity CLI binary; skipping patch", target)
			return
		}
		res, perr := ctxpatch.Apply(target, tokens)
		if perr != nil {
			log.Fatalf("[ctxpatch] patch failed for %s: %v", target, perr)
		}
		if res.Changed {
			log.Printf("[ctxpatch] patched %s (%s): %s; %s", target, res.Arch, res.Gate3A, res.Gate3B)
		} else {
			log.Printf("[ctxpatch] %s already patched at target; no change", target)
		}
		return
	}

	if err := os.MkdirAll(cfg.Server.DataDir, 0700); err != nil {
		log.Fatalf("Failed to create data_dir %s: %v", cfg.Server.DataDir, err)
	}
	if err := os.Chmod(cfg.Server.DataDir, 0700); err != nil {
		log.Printf("Warning: could not restrict data_dir %s to 0700: %v", cfg.Server.DataDir, err)
	}

	dbPath := filepath.Join(cfg.Server.DataDir, "gateway.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		log.Fatalf("Failed to open SQLite store %s: %v", dbPath, err)
	}
	defer store.Close()

	// Apply runtime overrides persisted from the admin UI (ACP backend + context patch),
	// unless a CLI flag or env var already pinned the value.
	if *backendFlag == "" && os.Getenv("AG_ACP_BACKEND") == "" {
		if bt, ok := store.GetSetting(updater.SettingACPBackendType); ok && strings.TrimSpace(bt) != "" {
			cfg.ACPPool.BackendType = strings.TrimSpace(bt)
		}
	}
	if os.Getenv("AG_ACP_CONTEXT_PATCH") == "" {
		if v, ok := store.GetSetting(updater.SettingACPContextPatch); ok && strings.TrimSpace(v) != "" {
			cfg.ACPPool.ContextPatch = strings.TrimSpace(v) == "1" || strings.EqualFold(strings.TrimSpace(v), "true")
		}
	}
	if os.Getenv("AG_ACP_CONTEXT_PATCH_TOKENS") == "" {
		if v, ok := store.GetSetting(updater.SettingACPContextPatchTokens); ok && strings.TrimSpace(v) != "" {
			if n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr == nil && n >= 0 {
				cfg.ACPPool.ContextPatchTokens = n
			}
		}
	}

	if *acpBinFlag == "" && os.Getenv("AG_ACP_BINARY_PATH") == "" {
		if overrideBin := updater.ResolveStartupACPOverride(store, cfg.ACPPool.BackendType, cfg.ACPPool.BinaryPath); overrideBin != "" {
			cfg.ACPPool.BinaryPath = overrideBin
		}
	}

	if cfg.ACPPool.ContextPatch {
		patchBin := acp.ResolveACPBinaryPath(cfg.ACPPool.BackendType, cfg.ACPPool.BinaryPath)
		if res, err := ctxpatch.EnsurePatched(patchBin, true, cfg.ACPPool.ContextPatchTokens); err != nil {
			log.Printf("[ctxpatch] startup patch skipped for %s: %v", patchBin, err)
		} else if res.Changed {
			log.Printf("[ctxpatch] patched %s (%s): %s; %s", patchBin, res.Arch, res.Gate3A, res.Gate3B)
		}
	}

	if *syncOnlyFlag {
		server.SyncTranscriptTokens(cfg.Server.DataDir, store, 10000)
		ticker := time.NewTicker(1000 * time.Millisecond)
		defer ticker.Stop()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		for {
			select {
			case <-ticker.C:
				server.SyncTranscriptTokens(cfg.Server.DataDir, store, 40)
			case <-sigCh:
				return
			}
		}
	}

	bgStopCh := make(chan struct{})
	go func() {
		server.SyncTranscriptTokens(cfg.Server.DataDir, store, 120)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				server.SyncTranscriptTokens(cfg.Server.DataDir, store, 30)
			case <-bgStopCh:
				return
			}
		}
	}()

	accounts, err := store.ListAccounts(false)
	if err != nil {
		log.Fatalf("Failed to list accounts: %v", err)
	}

	hub := server.NewWiretapHub()
	pool, err := acp.NewMultiAccountPool(cfg.Server.DataDir, cfg.ACPPool, cfg.SandboxPolicy, accounts, hub.Broadcast)
	if err != nil {
		log.Fatalf("Failed to initialize MultiAccountPool: %v", err)
	}
	defer pool.Close()

	// Pre-warm ACP model discovery asynchronously on startup so /v1/models responds immediately (<1ms)
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if models, _, err := pool.GetOrDiscoverModels(warmCtx, "", true); err == nil && len(models) > 0 {
			log.Printf("Pre-warmed %d ACP models into cache", len(models))
		}
	}()

	parkTimeout := time.Duration(cfg.ACPPool.ToolParkTimeoutSec) * time.Second
	sm := acp.NewSessionManager(cfg.ACPPool.WorkspaceDir, parkTimeout)
	defer sm.Close()

	staticFS, err := gatewayembed.WebDistFS()
	if err != nil {
		log.Fatalf("Failed to mount embedded web/dist FS: %v", err)
	}

	gw := server.NewGatewayServer(cfg, store, pool, sm, hub, staticFS)
	restartCh := make(chan string, 1)
	gw.SetRestartFunc(func(execPath string) {
		select {
		case restartCh <- execPath:
		default:
		}
	})
	gw.LogStartupAdminCredentials()

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutSec) * time.Second,
		IdleTimeout:       120 * time.Second,
		// WriteTimeout is intentionally 0 so long-running SSE streams and /v1/acp/ws
		// WebSocket sessions are not abruptly severed after WriteTimeoutSec.
	}

	go func() {
		log.Printf("Antigravity ACP Gateway %s listening on http://%s (ReUI Admin Console + /v1/responses + /v1/chat/completions + /v1/messages + /v1/models + /v1/acp/ws)", version.Version, addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
		log.Println("Shutting down Antigravity ACP Gateway gracefully...")
		close(bgStopCh)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	case targetBin := <-restartCh:
		log.Printf("Shutting down Antigravity ACP Gateway for in-place update restart -> %s ...", targetBin)
		close(bgStopCh)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = httpSrv.Shutdown(ctx)
		cancel()
		sm.Close()
		pool.Close()
		_ = store.Close()
		if err := updater.ExecOrSpawnSelf(targetBin, os.Args, os.Environ()); err != nil {
			log.Fatalf("Failed to restart updated binary %s: %v", targetBin, err)
		}
	}
}
