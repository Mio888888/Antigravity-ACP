package ctxpatch

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoAgyPath returns the checked-in arm64 agy binary used as a patch fixture.
func repoAgyPath(t *testing.T) string {
	t.Helper()
	// internal/acp/ctxpatch -> repo root is three levels up.
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	p := filepath.Join(root, "bin", "agy")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("fixture bin/agy not present: %v", err)
	}
	return p
}

func clampConst(t *testing.T, path string) (full bool, constVal int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	textAddr, textOff, arch, err := locateText(data)
	if err != nil {
		t.Fatalf("locateText: %v", err)
	}
	if arch != "arm64" {
		t.Skipf("fixture arch %q not arm64", arch)
	}
	tab, err := symbolTable(data, textAddr)
	if err != nil {
		t.Fatalf("symbolTable: %v", err)
	}
	entry, end, err := findFunc(tab, symCheckpoint)
	if err != nil {
		t.Fatalf("findFunc: %v", err)
	}
	body := data[vaToOff(entry, textAddr, textOff):vaToOff(end, textAddr, textOff)]
	rel, cv, ok := findClampArm64(body)
	if ok {
		return false, cv
	}
	if bytes.Index(body, bytes.Repeat(nopARM64(), 6)) >= 0 {
		_ = rel
		return true, 0
	}
	t.Fatalf("no clamp or nop-run found in GetCheckpointConfig")
	return false, 0
}

func TestApplyAgyFixture(t *testing.T) {
	if runtime.GOARCH != "arm64" || runtime.GOOS != "darwin" {
		t.Skip("fixture test targets darwin/arm64 host with codesign")
	}
	src := repoAgyPath(t)
	tmp := filepath.Join(t.TempDir(), "agy")
	in, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	if err := os.WriteFile(tmp, in, 0o755); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	// 1. Full-window patch.
	res, err := Apply(tmp, 0)
	if err != nil {
		t.Fatalf("Apply(full): %v", err)
	}
	if !res.Changed {
		t.Fatalf("expected full-window patch to change the binary")
	}
	if !strings.Contains(res.Gate3A, "0x101354044") {
		t.Errorf("Gate3A offset mismatch vs reference: %q", res.Gate3A)
	}
	if !strings.Contains(res.Gate3B, "0x1014ad568") {
		t.Errorf("Gate3B offset mismatch vs reference: %q", res.Gate3B)
	}
	if full, _ := clampConst(t, tmp); !full {
		t.Errorf("expected clamp removed (nop run) after full patch")
	}

	// 2. Idempotent re-apply.
	res2, err := Apply(tmp, 0)
	if err != nil {
		t.Fatalf("Apply(full) second: %v", err)
	}
	if res2.Changed {
		t.Errorf("expected second full patch to be a no-op")
	}

	// 3. Re-patch from full to a specific 128000 ceiling.
	res3, err := Apply(tmp, 128000)
	if err != nil {
		t.Fatalf("Apply(128000): %v", err)
	}
	if !res3.Changed {
		t.Errorf("expected re-patch to 128000 to change the binary")
	}
	full, cv := clampConst(t, tmp)
	if full || cv != 128000 {
		t.Errorf("expected clamp constant 128000, got full=%v const=%d", full, cv)
	}

	// 4. Idempotent at 128000.
	res4, err := Apply(tmp, 128000)
	if err != nil {
		t.Fatalf("Apply(128000) second: %v", err)
	}
	if res4.Changed {
		t.Errorf("expected second 128000 patch to be a no-op")
	}

	// The patched binary must still be a runnable, signed Mach-O.
	if _, _, _, err := locateText(mustRead(t, tmp)); err != nil {
		t.Errorf("patched binary no longer parses: %v", err)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return b
}

func TestIsCLIBinary(t *testing.T) {
	for _, p := range []string{"/x/agy", "agy.exe", "/opt/antigravity"} {
		if !IsCLIBinary(p) {
			t.Errorf("expected %q recognised as CLI", p)
		}
	}
	for _, p := range []string{"/x/agy_acp_server.par", "localharness_external"} {
		if IsCLIBinary(p) {
			t.Errorf("expected %q NOT recognised as CLI", p)
		}
	}
}
