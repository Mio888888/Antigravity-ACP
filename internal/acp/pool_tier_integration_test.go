package acp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"antigravity-gateway/internal/acp/ctxpatch"
)

// TestResolveTierBinaryRealAgy exercises the version/tier materialization against the real
// agy binary. It is slow (copies the ~200MB binary) and gated behind AG_TIER_IT=1.
func TestResolveTierBinaryRealAgy(t *testing.T) {
	if os.Getenv("AG_TIER_IT") != "1" {
		t.Skip("set AG_TIER_IT=1 to run the real-agy tier integration test")
	}
	base, err := filepath.Abs("../../bin/agy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(base); err != nil {
		t.Skipf("bin/agy not present: %v", err)
	}

	mp := &MultiAccountPool{
		dataDir:     t.TempDir(),
		versionUsed: make(map[string]time.Time),
	}

	ver := agyVersionKey(base)
	t.Logf("agy version key = %q", ver)
	if ver == "unknown" || ver == "" {
		t.Fatalf("expected a version key from agy --version, got %q", ver)
	}

	// First tier: materializes the version from base.
	p200, err := mp.resolveTierBinary(base, 200000)
	if err != nil {
		t.Fatalf("resolveTierBinary(200000): %v", err)
	}
	t.Logf("200000 tier -> %s", p200)
	if mp.activeVersion != ver {
		t.Fatalf("active version = %q, want %q", mp.activeVersion, ver)
	}

	// Second tier: must be derived from the first (same-version donor) and land in the
	// same version directory.
	p400, err := mp.resolveTierBinary(base, 400000)
	if err != nil {
		t.Fatalf("resolveTierBinary(400000): %v", err)
	}
	t.Logf("400000 tier -> %s", p400)
	if filepath.Dir(filepath.Dir(p200)) != filepath.Dir(filepath.Dir(p400)) {
		t.Fatalf("tiers landed in different version dirs: %s vs %s", p200, p400)
	}

	// Both copies must actually be patched to their respective ceilings. Re-applying the
	// same value must report no change (idempotent), proving the ceiling is already set.
	res200, err := ctxpatch.Apply(p200, 200000)
	if err != nil {
		t.Fatalf("verify 200000: %v", err)
	}
	if res200.Changed {
		t.Fatalf("200000 tier was not already patched to 200000: %+v", res200)
	}
	res400, err := ctxpatch.Apply(p400, 400000)
	if err != nil {
		t.Fatalf("verify 400000: %v", err)
	}
	if res400.Changed {
		t.Fatalf("400000 tier was not already patched to 400000: %+v", res400)
	}

	// Fast path: unchanged base returns the same tier binary without rework.
	again, err := mp.resolveTierBinary(base, 200000)
	if err != nil || again != p200 {
		t.Fatalf("fast path mismatch: got %q err=%v, want %q", again, err, p200)
	}
}
