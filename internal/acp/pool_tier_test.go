package acp

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func newVersionPoolForTest(t *testing.T) (*MultiAccountPool, string) {
	t.Helper()
	dataDir := t.TempDir()
	mp := &MultiAccountPool{
		dataDir:     dataDir,
		versionUsed: make(map[string]time.Time),
	}
	root := filepath.Join(dataDir, "acp-ctxpatch")
	now := time.Now()
	// Three versions, each with a single tier copy; lower index = older.
	for i, ver := range []string{"v1.0.0", "v1.1.0", "v1.2.0"} {
		dir := filepath.Join(root, ver, strconv.Itoa(400000))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agy"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		mp.versionUsed[ver] = now.Add(time.Duration(i) * time.Minute)
	}
	return mp, root
}

func remainingVersions(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

func TestPruneVersionsKeepsMostRecent(t *testing.T) {
	mp, root := newVersionPoolForTest(t)
	mp.activeVersion = "v1.2.0"

	mp.ctxPatchMu.Lock()
	mp.pruneVersionsLocked()
	mp.ctxPatchMu.Unlock()

	got := remainingVersions(t, root)
	if len(got) != maxPatchedVersions {
		t.Fatalf("expected %d versions kept, got %d: %v", maxPatchedVersions, len(got), got)
	}
	if !got["v1.1.0"] || !got["v1.2.0"] {
		t.Fatalf("expected the two most recent versions retained, got %v", got)
	}
	if got["v1.0.0"] {
		t.Fatalf("expected oldest version pruned, got %v", got)
	}
	if _, ok := mp.versionUsed["v1.0.0"]; ok {
		t.Fatalf("pruned version should be dropped from use tracking")
	}
}

func TestPruneVersionsNeverRemovesActive(t *testing.T) {
	mp, root := newVersionPoolForTest(t)
	// The active version is also the least-recently-used; it must survive.
	mp.activeVersion = "v1.0.0"

	mp.ctxPatchMu.Lock()
	mp.pruneVersionsLocked()
	mp.ctxPatchMu.Unlock()

	got := remainingVersions(t, root)
	if len(got) != maxPatchedVersions {
		t.Fatalf("expected %d versions kept, got %d: %v", maxPatchedVersions, len(got), got)
	}
	if !got["v1.0.0"] {
		t.Fatalf("the active version must never be pruned, got %v", got)
	}
}

func TestFindVersionTierBinary(t *testing.T) {
	dir := t.TempDir()
	if got := findVersionTierBinary(dir); got != "" {
		t.Fatalf("empty version dir should have no tier, got %q", got)
	}
	tierDir := filepath.Join(dir, "200000")
	if err := os.MkdirAll(tierDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tierDir, "agy")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findVersionTierBinary(dir); got != bin {
		t.Fatalf("expected %q, got %q", bin, got)
	}
	if !dirHasTier(dir) {
		t.Fatalf("dirHasTier should be true when a tier binary exists")
	}
}
