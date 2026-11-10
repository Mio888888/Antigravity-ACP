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
	"syscall"
	"time"

	gatewayembed "antigravity-gateway"
	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/server"
	"antigravity-gateway/internal/storage"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "Path to gateway config.yaml")
	backendFlag := flag.String("backend", "", "ACP backend type override: auto | zed | zen | cli")
	acpBinFlag := flag.String("acp-bin", "", "Explicit path to ACP backend binary (agy_acp_server.par or agy)")
	syncOnlyFlag := flag.Bool("sync-only", false, "Run continuous transcript.jsonl -> gateway.db token reconciler")
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

	if err := os.MkdirAll(cfg.Server.DataDir, 0755); err != nil {
		log.Fatalf("Failed to create data_dir %s: %v", cfg.Server.DataDir, err)
	}

	dbPath := filepath.Join(cfg.Server.DataDir, "gateway.db")
	store, err := storage.NewStore(dbPath)
	if err != nil {
		log.Fatalf("Failed to open SQLite store %s: %v", dbPath, err)
	}
	defer store.Close()

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

	go func() {
		server.SyncTranscriptTokens(cfg.Server.DataDir, store, 120)
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			server.SyncTranscriptTokens(cfg.Server.DataDir, store, 30)
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
		log.Printf("Antigravity ACP Gateway v1.6.0 listening on http://%s (ReUI Admin Console + /v1/responses + /v1/chat/completions + /v1/messages + /v1/models + /v1/acp/ws)", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down Antigravity ACP Gateway gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}
