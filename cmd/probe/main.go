package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"antigravity-gateway/internal/acp"

	"github.com/gorilla/websocket"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "bridge" {
		runBridge(os.Args[2:])
		return
	}
	runProbe(os.Args[1:])
}

func runProbe(args []string) {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	binPath := fs.String("bin", "/Users/wanlitong/Library/Application Support/Zed/external_agents/registry/antigravity-acp/v_1.4.5_6c2906ba8ee98ee2_d38096e037b307b2/agy_acp_server.par", "Path to agy or agy_acp_server.par")
	cwd := fs.String("cwd", "/tmp", "Working directory for session/new")
	clientName := fs.String("client", "zed", "ClientInfo.name to report on initialize")
	_ = fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	w, err := acp.NewWorkerClient(ctx, "probe_1", acp.WorkerLaunchOptions{
		BinaryPath:        *binPath,
		WorkspaceDir:      *cwd,
		ProcessPerSession: true,
		DisableTelemetry:  true,
	}, func(workerID, direction, rawLine string) {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", direction, rawLine)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe launch error: %v\n", err)
		os.Exit(1)
	}
	defer w.Close()

	initRes, err := w.Initialize(ctx, *clientName, "0.175.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize error: %v\n", err)
		os.Exit(1)
	}
	raw, _ := json.MarshalIndent(initRes, "", "  ")
	fmt.Println(string(raw))
}

func runBridge(args []string) {
	fs := flag.NewFlagSet("bridge", flag.ExitOnError)
	wsURL := fs.String("url", "ws://localhost:8080/v1/acp/ws", "Remote gateway ACP WebSocket URL")
	apiKey := fs.String("key", "", "Northbound API Key (Bearer)")
	accountID := fs.String("account", "", "Optional target account ID (X-ACP-Account-ID)")
	_ = fs.Parse(args)

	headers := http.Header{}
	if *apiKey != "" {
		headers.Set("Authorization", "Bearer "+strings.TrimSpace(*apiKey))
	}
	if *accountID != "" {
		headers.Set("X-ACP-Account-ID", strings.TrimSpace(*accountID))
	}

	conn, _, err := websocket.DefaultDialer.Dial(*wsURL, headers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bridge dial %s failed: %v\n", *wsURL, err)
		os.Exit(1)
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			line := strings.TrimSpace(string(msg))
			if line != "" {
				_, _ = os.Stdout.WriteString(line + "\n")
			}
		}
	}()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
			break
		}
	}
	<-done
}
