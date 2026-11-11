package acp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
)

func TestBuildCommandAndArgsContextSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a unix shell")
	}
	// The flag is only emitted when the binary advertises --context-size support.
	restore := acp.SetContextSizeProbeForTest(func(string) bool { return true })
	defer restore()
	agy := writeFakeAgy(t, t.TempDir(), "  --context-size   Context window size in tokens")

	_, args := acp.BuildCommandAndArgs(acp.WorkerLaunchOptions{
		BinaryPath:  agy,
		ContextSize: 128000,
	})
	if !strings.Contains(strings.Join(args, " "), "--context-size 128000") {
		t.Fatalf("expected --context-size 128000 in args %v", args)
	}

	_, args = acp.BuildCommandAndArgs(acp.WorkerLaunchOptions{
		BinaryPath:  agy,
		ContextSize: 128000,
		ExtraArgs:   []string{"--context-size=64000"},
	})
	if joined := strings.Join(args, " "); strings.Contains(joined, "128000") || !strings.Contains(joined, "--context-size=64000") {
		t.Fatalf("expected extra_args --context-size to win, got %v", args)
	}

	_, args = acp.BuildCommandAndArgs(acp.WorkerLaunchOptions{
		BinaryPath:  "/opt/agy_acp_server.par",
		ContextSize: 128000,
	})
	if strings.Contains(strings.Join(args, " "), "--context-size") {
		t.Fatalf("standalone .par backend must not receive --context-size, got %v", args)
	}

	// A binary that does not support the flag must not receive it even when a size is set.
	legacy := writeFakeAgy(t, t.TempDir(), "  --add-dir   Add a directory")
	restore2 := acp.SetContextSizeProbeForTest(func(string) bool { return false })
	defer restore2()
	_, args = acp.BuildCommandAndArgs(acp.WorkerLaunchOptions{
		BinaryPath:  legacy,
		ContextSize: 128000,
	})
	if strings.Contains(strings.Join(args, " "), "--context-size") {
		t.Fatalf("binary without --context-size support must not receive the flag, got %v", args)
	}
}

func writeFakeAgy(t *testing.T, dir, helpText string) string {
	t.Helper()
	bin := filepath.Join(dir, "agy")
	script := "#!/bin/sh\necho '" + helpText + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0755); err != nil {
		t.Fatalf("write fake agy: %v", err)
	}
	return bin
}

func TestResolveWorkerContextSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a unix shell")
	}
	supported := writeFakeAgy(t, t.TempDir(), "  --context-size   Context window size in tokens")
	legacy := writeFakeAgy(t, t.TempDir(), "  --add-dir   Add a directory")

	cfg := config.ACPPoolConfig{BackendType: "cli", BinaryPath: supported, ContextSize: 64000}
	if got := acp.ResolveWorkerContextSize(cfg, 128000); got != 128000 {
		t.Fatalf("client context window should take precedence, got %d", got)
	}
	if got := acp.ResolveWorkerContextSize(cfg, 0); got != 64000 {
		t.Fatalf("expected configured fallback 64000, got %d", got)
	}
	cfg.ContextSize = 0
	if got := acp.ResolveWorkerContextSize(cfg, 0); got != 0 {
		t.Fatalf("expected 0 without client or configured size, got %d", got)
	}

	pinned := config.ACPPoolConfig{BackendType: "cli", BinaryPath: supported, ExtraArgs: []string{"--context-size", "32000"}}
	if got := acp.ResolveWorkerContextSize(pinned, 128000); got != 0 {
		t.Fatalf("extra_args --context-size must disable auto sync, got %d", got)
	}

	old := config.ACPPoolConfig{BackendType: "cli", BinaryPath: legacy}
	if got := acp.ResolveWorkerContextSize(old, 128000); got != 0 {
		t.Fatalf("binary without --context-size support must not receive the flag, got %d", got)
	}
}

func TestNearestContextPreset(t *testing.T) {
	cases := []struct {
		in   int64
		want int64
	}{
		{0, 0},
		{-5, 0},
		{1, 128000},
		{128000, 128000},
		{160000, 128000}, // closer to 128000 than 200000
		{164001, 200000}, // past the midpoint
		{180000, 200000},
		{228000, 256000},
		{330000, 400000},
		{900000, 1000000},
		{5000000, 1000000},
	}
	for _, c := range cases {
		if got := acp.NearestContextPreset(c.in); got != c.want {
			t.Fatalf("NearestContextPreset(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestResolveContextSizeSnapsWhenPatched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a unix shell")
	}
	bin := writeFakeAgy(t, t.TempDir(), "  --add-dir   Add a directory")
	poolCfg := config.ACPPoolConfig{
		BackendType:        "cli",
		BinaryPath:         bin,
		ContextPatch:       true,
		ContextPatchTokens: 400000,
	}
	mp, err := acp.NewMultiAccountPool(t.TempDir(), poolCfg, config.SandboxPolicyConfig{}, nil, nil)
	if err != nil {
		t.Fatalf("NewMultiAccountPool failed: %v", err)
	}
	defer mp.Close()

	// A client window with no exact preset snaps to the nearest tier.
	if got := mp.ResolveContextSize(180000); got != 200000 {
		t.Fatalf("expected 180000 to snap to 200000, got %d", got)
	}
	// No client window falls back to the configured default tier.
	if got := mp.ResolveContextSize(0); got != 400000 {
		t.Fatalf("expected default tier 400000, got %d", got)
	}
}

func TestPoolMatchesWorkersByContextSize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a unix shell")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	restore := acp.SetContextSizeProbeForTest(func(string) bool { return true })
	defer restore()

	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	pyPath := filepath.Join(dir, "worker.py")
	py := `
import sys, json
with open(` + "'" + argvLog + "'" + `, "a") as f:
    f.write(" ".join(sys.argv[1:]) + "\n")
for line in sys.stdin:
    line = line.strip()
    if not line.startswith("{"):
        continue
    msg = json.loads(line)
    if msg.get("method") == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg.get("id"), "result": {"protocolVersion": 1}}) + "\n")
        sys.stdout.flush()
`
	if err := os.WriteFile(pyPath, []byte(py), 0644); err != nil {
		t.Fatalf("write worker.py: %v", err)
	}
	bin := filepath.Join(dir, "agy")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec python3 -u "+pyPath+" \"$@\"\n"), 0755); err != nil {
		t.Fatalf("write fake agy: %v", err)
	}

	poolCfg := config.ACPPoolConfig{
		BackendType:    "cli",
		BinaryPath:     bin,
		WorkspaceDir:   t.TempDir(),
		MaxWorkers:     1,
		MaxIdleTimeSec: 60,
	}
	accounts := []storage.Account{{ID: "acc", Name: "A", NetworkMode: "direct", MaxWorkers: 1, Weight: 1, Enabled: true, Status: "ready"}}
	mp, err := acp.NewMultiAccountPool(t.TempDir(), poolCfg, config.SandboxPolicyConfig{InternalApproval: "auto_approve"}, accounts, nil)
	if err != nil {
		t.Fatalf("NewMultiAccountPool failed: %v", err)
	}
	defer mp.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	size128 := mp.ResolveContextSize(128000)
	if size128 != 128000 {
		t.Fatalf("expected ResolveContextSize(128000)=128000, got %d", size128)
	}
	w1, _, err := mp.AcquireForAccountWithContextSize(ctx, "acc", false, "", size128)
	if err != nil {
		t.Fatalf("acquire 128000 failed: %v", err)
	}
	if w1.ContextSize != 128000 {
		t.Fatalf("expected worker ContextSize=128000, got %d", w1.ContextSize)
	}
	mp.Release(w1)

	again, _, err := mp.AcquireForAccountWithContextSize(ctx, "acc", false, "", size128)
	if err != nil || again != w1 {
		t.Fatalf("expected idle worker with matching context size to be reused, got %v err=%v", again, err)
	}
	mp.Release(again)

	// max_workers=1: the idle 128000 worker must be evicted to serve a different size.
	w2, _, err := mp.AcquireForAccountWithContextSize(ctx, "acc", false, "", mp.ResolveContextSize(200000))
	if err != nil {
		t.Fatalf("acquire 200000 failed: %v", err)
	}
	if w2 == w1 || w2.ContextSize != 200000 {
		t.Fatalf("expected a new worker with ContextSize=200000, got same=%v size=%d", w2 == w1, w2.ContextSize)
	}
	mp.Release(w2)

	raw, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	logged := string(raw)
	if !strings.Contains(logged, "--context-size 128000") || !strings.Contains(logged, "--context-size 200000") {
		t.Fatalf("expected both workers launched with --context-size, argv log:\n%s", logged)
	}
}
