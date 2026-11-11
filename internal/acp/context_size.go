package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"antigravity-gateway/internal/config"
)

const contextSizeFlag = "--context-size"

// ContextWindowPresets are the discrete context-window tiers the gateway snaps to when
// context-window patching is enabled. Kept in sync with CONTEXT_WINDOW_PRESETS in
// web/src/api.ts.
var ContextWindowPresets = []int64{128000, 200000, 256000, 400000, 1000000}

// NearestContextPreset snaps an arbitrary context window to the closest preset tier so a
// client that requests, say, 180000 tokens is served by the 200000 tier. A non-positive
// value returns 0, meaning "unset / full physical window". Ties resolve to the larger
// (more generous) preset.
func NearestContextPreset(v int64) int64 {
	if v <= 0 {
		return 0
	}
	best := ContextWindowPresets[0]
	bestDist := absInt64(v - best)
	for _, p := range ContextWindowPresets[1:] {
		d := absInt64(v - p)
		if d < bestDist || (d == bestDist && p > best) {
			best = p
			bestDist = d
		}
	}
	return best
}

func absInt64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// HasContextSizeArg reports whether args already pin the ACP context size, in which
// case the gateway must not append its own flag.
func HasContextSizeArg(args []string) bool {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == contextSizeFlag || a == "--context_size" ||
			strings.HasPrefix(a, contextSizeFlag+"=") || strings.HasPrefix(a, "--context_size=") {
			return true
		}
	}
	return false
}

var (
	contextSizeProbeMu    sync.Mutex
	contextSizeProbeCache = map[string]bool{}
	// contextSizeProbe is replaceable in tests.
	contextSizeProbe = probeContextSizeFlag
)

// SupportsContextSizeFlag reports whether the ACP binary accepts `acp --context-size`.
// Older agy releases print usage and exit on unknown flags, so the flag is only passed
// after `acp --help` confirms it. Results are cached per binary path, size and mtime so
// an in-place binary update is re-probed.
func SupportsContextSizeFlag(bin string) bool {
	bin = strings.TrimSpace(bin)
	if bin == "" {
		return false
	}
	base := strings.ToLower(filepath.Base(bin))
	baseNoExt := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd"), ".bat")
	if baseNoExt != "agy" && baseNoExt != "antigravity" {
		return false
	}
	resolved := bin
	if !strings.ContainsAny(bin, `/\`) {
		p, err := exec.LookPath(bin)
		if err != nil {
			return false
		}
		resolved = p
	}
	st, err := os.Stat(resolved)
	if err != nil || st.IsDir() {
		return false
	}
	key := fmt.Sprintf("%s|%d|%d", resolved, st.Size(), st.ModTime().UnixNano())

	contextSizeProbeMu.Lock()
	defer contextSizeProbeMu.Unlock()
	if ok, cached := contextSizeProbeCache[key]; cached {
		return ok
	}
	ok := contextSizeProbe(resolved)
	contextSizeProbeCache[key] = ok
	return ok
}

func probeContextSizeFlag(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "acp", "--help")
	cmd.Env = SanitizedParentEnv()
	out, _ := cmd.CombinedOutput()
	return strings.Contains(string(out), contextSizeFlag)
}

// ResolveWorkerContextSize returns the `--context-size` value a worker should be
// started with: the client's context window when known, otherwise the configured
// acp_pool.context_size. It returns 0 (flag omitted) when extra_args already pins the
// size or the ACP binary does not support the flag.
func ResolveWorkerContextSize(cfg config.ACPPoolConfig, clientContextWindow int64) int64 {
	size := clientContextWindow
	if size <= 0 {
		size = cfg.ContextSize
	}
	if size <= 0 || HasContextSizeArg(cfg.ExtraArgs) {
		return 0
	}
	if !SupportsContextSizeFlag(ResolveACPBinaryPath(cfg.BackendType, cfg.BinaryPath)) {
		return 0
	}
	return size
}
