// Package ctxpatch rewrites the Antigravity CLI ("agy") context-compaction ceiling
// in place so ACP sessions are not forced to compact at the hardcoded 256,000-token
// limit. It is a pure-Go port of the externally verified agy-patch.py "Gate 3" logic:
//
//   - Gate 3A (utils.GetCheckpointConfig): the function computes
//     min(contextWindow - max(maxOutputTokens, 40000), 256000) and stores the result
//     as CheckpointConfig.MaxTokenLimit. We either drop the 256000 clamp entirely
//     (letting the model's full physical window stand) or rewrite the constant to a
//     chosen cap.
//   - Gate 3B (config.applyFeatureProviderCheckpointOverrideConfig): the
//     CASCADE_USE_EXPERIMENT_CHECKPOINTER profile re-imposes 256000 via
//     MergeConfigsDedup; we zero the experiment's MaxTokenLimit so the Gate 3A value
//     survives.
//
// Symbols are resolved with debug/gosym against the Go pclntab, so the patch follows
// the compiler across releases instead of depending on fixed offsets. Only the two
// functions above are modified; everything else is byte-identical. On macOS the binary
// is ad-hoc re-signed (identity preserved) so Gatekeeper still runs it.
//
// Supported targets: arm64 and amd64, in Mach-O / ELF / PE containers. The patch is
// idempotent and re-applies cleanly after an agy upgrade.
package ctxpatch

import (
	"bytes"
	"debug/elf"
	"debug/gosym"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	symCheckpoint    = "utils.GetCheckpointConfig"
	symExpCheckpoint = "config.applyFeatureProviderCheckpointOverrideConfig"

	defaultClampTokens = 256000
)

// Result describes what Apply changed.
type Result struct {
	Changed   bool
	Arch      string
	MaxTokens int64 // target ceiling applied; 0 means the clamp was removed (full window)
	Gate3A    string
	Gate3B    string
}

// IsCLIBinary reports whether path's basename looks like the Antigravity CLI, the only
// flavor this patch applies to (the standalone agy_acp_server.par is a different binary).
func IsCLIBinary(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	base = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd"), ".bat")
	return base == "agy" || base == "antigravity"
}

// EnsurePatched applies the context patch to binPath when enabled and binPath looks
// like the Antigravity CLI. It is a no-op (Result{}, nil) when disabled, when binPath
// is empty, or when the target is not the CLI flavor (e.g. agy_acp_server.par). tokens
// <= 0 removes the 256K ceiling; tokens > 0 sets that ceiling. Because Apply is
// idempotent, callers may invoke this on every startup and after every agy update.
func EnsurePatched(binPath string, enabled bool, tokens int64) (Result, error) {
	if !enabled || strings.TrimSpace(binPath) == "" {
		return Result{}, nil
	}
	if !IsCLIBinary(binPath) {
		return Result{}, nil
	}
	return Apply(binPath, tokens)
}

// Apply patches path in place. maxTokens <= 0 removes the 256K ceiling so the model's
// full physical window applies; maxTokens > 0 rewrites the ceiling to that token count.
// The write is atomic (temp file + rename) and, on macOS, re-signed. Apply is
// idempotent: a binary already carrying the requested ceiling returns Changed=false.
func Apply(path string, maxTokens int64) (Result, error) {
	if maxTokens < 0 {
		maxTokens = 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	buf := make([]byte, len(data))
	copy(buf, data)

	textAddr, textOff, arch, err := locateText(buf)
	if err != nil {
		return Result{}, err
	}
	if arch != "arm64" && arch != "amd64" {
		return Result{}, fmt.Errorf("context patch unsupported on arch %q", arch)
	}
	tab, err := symbolTable(buf, textAddr)
	if err != nil {
		return Result{}, err
	}

	res := Result{Arch: arch, MaxTokens: maxTokens}

	cpEntry, cpEnd, err := findFunc(tab, symCheckpoint)
	if err != nil {
		return Result{}, err
	}
	changedA, msgA, err := patchGate3A(buf, vaToOff(cpEntry, textAddr, textOff), int(cpEnd-cpEntry), cpEntry, arch, maxTokens)
	if err != nil {
		return Result{}, err
	}
	res.Gate3A = msgA

	expEntry, expEnd, err := findFunc(tab, symExpCheckpoint)
	if err != nil {
		return Result{}, err
	}
	changedB, msgB, err := patchGate3B(buf, vaToOff(expEntry, textAddr, textOff), int(expEnd-expEntry), expEntry, arch)
	if err != nil {
		return Result{}, err
	}
	res.Gate3B = msgB

	if !changedA && !changedB {
		return res, nil
	}
	if err := writeAtomicSigned(path, buf); err != nil {
		return Result{}, err
	}
	res.Changed = true
	return res, nil
}

func vaToOff(va, textAddr, textOff uint64) int { return int(va - textAddr + textOff) }

func findFunc(tab *gosym.Table, suffix string) (entry, end uint64, err error) {
	var match *gosym.Func
	for i := range tab.Funcs {
		f := &tab.Funcs[i]
		// Match the package-qualified trailing name (e.g. ".../cortex/utils/utils.Foo")
		// on a package-path ('/') or member ('.') boundary to avoid false positives.
		if !strings.HasSuffix(f.Name, suffix) {
			continue
		}
		if len(f.Name) > len(suffix) {
			if b := f.Name[len(f.Name)-len(suffix)-1]; b != '/' && b != '.' {
				continue
			}
		}
		if match != nil && match.Entry != f.Entry {
			return 0, 0, fmt.Errorf("ambiguous symbol %q", suffix)
		}
		match = f
	}
	if match == nil {
		return 0, 0, fmt.Errorf("symbol not found: %q", suffix)
	}
	return match.Entry, match.End, nil
}

// --------------------------------------------------------------------------
// Container parsing: text section + Go pclntab
// --------------------------------------------------------------------------

func locateText(data []byte) (textAddr, textOff uint64, arch string, err error) {
	switch {
	case len(data) >= 4 && data[0] == 0xcf && data[1] == 0xfa && data[2] == 0xed && data[3] == 0xfe:
		return machoText(data)
	case len(data) >= 4 && data[0] == 0x7f && data[1] == 'E' && data[2] == 'L' && data[3] == 'F':
		return elfText(data)
	case len(data) >= 2 && data[0] == 'M' && data[1] == 'Z':
		return peText(data)
	}
	return 0, 0, "", fmt.Errorf("unrecognised binary container")
}

func machoCPUArch(c macho.Cpu) string {
	switch c {
	case macho.CpuArm64:
		return "arm64"
	case macho.CpuAmd64:
		return "amd64"
	}
	return ""
}

func machoText(data []byte) (uint64, uint64, string, error) {
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return 0, 0, "", err
	}
	defer f.Close()
	sec := f.Section("__text")
	if sec == nil {
		return 0, 0, "", fmt.Errorf("__text section not found")
	}
	return sec.Addr, uint64(sec.Offset), machoCPUArch(f.Cpu), nil
}

func elfText(data []byte) (uint64, uint64, string, error) {
	f, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return 0, 0, "", err
	}
	defer f.Close()
	var arch string
	switch f.Machine {
	case elf.EM_AARCH64:
		arch = "arm64"
	case elf.EM_X86_64:
		arch = "amd64"
	}
	sec := f.Section(".text")
	if sec == nil {
		return 0, 0, "", fmt.Errorf(".text section not found")
	}
	return sec.Addr, sec.Offset, arch, nil
}

func peText(data []byte) (uint64, uint64, string, error) {
	f, err := pe.NewFile(bytes.NewReader(data))
	if err != nil {
		return 0, 0, "", err
	}
	defer f.Close()
	var arch string
	switch f.Machine {
	case pe.IMAGE_FILE_MACHINE_ARM64:
		arch = "arm64"
	case pe.IMAGE_FILE_MACHINE_AMD64:
		arch = "amd64"
	}
	var imageBase uint64
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		imageBase = oh.ImageBase
	default:
		return 0, 0, "", fmt.Errorf("only PE32+ is supported")
	}
	sec := f.Section(".text")
	if sec == nil {
		return 0, 0, "", fmt.Errorf(".text section not found")
	}
	return imageBase + uint64(sec.VirtualAddress), uint64(sec.Offset), arch, nil
}

// pclntabMagics covers the Go 1.18/1.20+ pcHeader magics.
var pclntabMagics = [][]byte{
	{0xf1, 0xff, 0xff, 0xff, 0x00, 0x00}, // go1.20+
	{0xf0, 0xff, 0xff, 0xff, 0x00, 0x00}, // go1.18/1.19
	{0xfa, 0xff, 0xff, 0xff, 0x00, 0x00}, // go1.16/1.17
}

func symbolTable(data []byte, textAddr uint64) (*gosym.Table, error) {
	for _, magic := range pclntabMagics {
		pos := 0
		for {
			i := bytesIndexFrom(data, magic, pos)
			if i < 0 {
				break
			}
			pos = i + 1
			if i+8 > len(data) {
				continue
			}
			minLC := data[i+6]
			ptrSize := data[i+7]
			if (minLC != 1 && minLC != 2 && minLC != 4) || ptrSize != 8 {
				continue
			}
			lt := gosym.NewLineTable(data[i:], textAddr)
			tab, err := gosym.NewTable(nil, lt)
			if err != nil || len(tab.Funcs) == 0 {
				continue
			}
			return tab, nil
		}
	}
	return nil, fmt.Errorf("Go pclntab not found")
}

func bytesIndexFrom(haystack, needle []byte, from int) int {
	if from < 0 {
		from = 0
	}
	if from >= len(haystack) {
		return -1
	}
	idx := bytes.Index(haystack[from:], needle)
	if idx < 0 {
		return -1
	}
	return from + idx
}

// --------------------------------------------------------------------------
// Gate 3A: rewrite the 256,000 ceiling in utils.GetCheckpointConfig
// --------------------------------------------------------------------------

func patchGate3A(buf []byte, fnOff, size int, fnVA uint64, arch string, maxTokens int64) (bool, string, error) {
	if arch == "arm64" {
		return patchGate3AArm64(buf, fnOff, size, fnVA, maxTokens)
	}
	return patchGate3AAmd64(buf, fnOff, size, fnVA, maxTokens)
}

func nopARM64() []byte { return []byte{0x1f, 0x20, 0x03, 0xd5} }

func desiredGate3AArm64(maxTokens int64) []byte {
	if maxTokens <= 0 {
		out := make([]byte, 0, 24)
		for i := 0; i < 6; i++ {
			out = append(out, nopARM64()...)
		}
		return out
	}
	lo := uint32(maxTokens & 0xffff)
	hi := uint32((maxTokens >> 16) & 0xffff)
	movz := 0xD2800004 | (lo << 5)
	movk := 0xF2A00004 | (hi << 5)
	words := []uint32{movz, movk, 0x6B04007F, movz, movk, 0x9A843063}
	out := make([]byte, 24)
	for i, w := range words {
		binary.LittleEndian.PutUint32(out[i*4:], w)
	}
	return out
}

// findClampArm64 locates the structural movz/movk/cmp/movz/movk/csel clamp regardless
// of the constant, returning its offset within body and the encoded constant.
func findClampArm64(body []byte) (rel int, constVal int64, ok bool) {
	for rel = 0; rel+24 <= len(body); rel += 4 {
		w := func(k int) uint32 { return binary.LittleEndian.Uint32(body[rel+k*4:]) }
		if w(0)&0xFFE0001F != 0xD2800004 { // movz x4,#lo
			continue
		}
		if w(1)&0xFFE0001F != 0xF2A00004 { // movk x4,#hi,lsl16
			continue
		}
		if w(2) != 0x6B04007F { // cmp w3,w4
			continue
		}
		if w(3)&0xFFE0001F != 0xD2800004 || w(4)&0xFFE0001F != 0xF2A00004 {
			continue
		}
		if w(5) != 0x9A843063 { // csel x3,x3,x4,lo
			continue
		}
		lo := int64((w(0) >> 5) & 0xffff)
		hi := int64((w(1) >> 5) & 0xffff)
		return rel, lo | (hi << 16), true
	}
	return 0, 0, false
}

func patchGate3AArm64(buf []byte, fnOff, size int, fnVA uint64, maxTokens int64) (bool, string, error) {
	body := buf[fnOff : fnOff+size]
	desired := desiredGate3AArm64(maxTokens)

	rel, _, ok := findClampArm64(body)
	if ok {
		if bytes.Equal(body[rel:rel+24], desired) {
			return false, fmt.Sprintf("Gate 3A: already at ceiling (%d) @ %#x", maxTokens, fnVA+uint64(rel)), nil
		}
		copy(body[rel:rel+24], desired)
		return true, gate3AMsg(fnVA+uint64(rel), maxTokens), nil
	}
	// Re-patch case: a previous full-window patch left 6 nops where the clamp was.
	nops := bytes.Repeat(nopARM64(), 6)
	if idx := bytes.Index(body, nops); idx >= 0 {
		if bytes.Equal(desired, nops) {
			return false, fmt.Sprintf("Gate 3A: already full-window @ %#x", fnVA+uint64(idx)), nil
		}
		copy(body[idx:idx+24], desired)
		return true, gate3AMsg(fnVA+uint64(idx), maxTokens), nil
	}
	return false, "", fmt.Errorf("Gate 3A: 256K clamp pattern not found (unsupported agy build?)")
}

func desiredGate3AAmd64(maxTokens int64) []byte {
	if maxTokens <= 0 {
		out := []byte{0x89, 0xd6} // mov %edx,%esi
		for len(out) < 14 {
			out = append(out, 0x90)
		}
		return out
	}
	n := uint32(maxTokens)
	out := make([]byte, 0, 14)
	out = append(out, 0x81, 0xfa) // cmp $n,%edx
	out = binary.LittleEndian.AppendUint32(out, n)
	out = append(out, 0xbe) // mov $n,%esi
	out = binary.LittleEndian.AppendUint32(out, n)
	out = append(out, 0x0f, 0x42, 0xf2) // cmovb %edx,%esi
	return out
}

// findClampAmd64 locates `cmp $imm,%edx ; mov $imm,%esi ; cmovb %edx,%esi` for any imm.
func findClampAmd64(body []byte) (rel int, ok bool) {
	for rel = 0; rel+14 <= len(body); rel++ {
		if body[rel] == 0x81 && body[rel+1] == 0xfa &&
			body[rel+6] == 0xbe &&
			body[rel+11] == 0x0f && body[rel+12] == 0x42 && body[rel+13] == 0xf2 {
			return rel, true
		}
	}
	return 0, false
}

func patchGate3AAmd64(buf []byte, fnOff, size int, fnVA uint64, maxTokens int64) (bool, string, error) {
	body := buf[fnOff : fnOff+size]
	desired := desiredGate3AAmd64(maxTokens)

	if rel, ok := findClampAmd64(body); ok {
		if bytes.Equal(body[rel:rel+14], desired) {
			return false, fmt.Sprintf("Gate 3A: already at ceiling (%d) @ %#x", maxTokens, fnVA+uint64(rel)), nil
		}
		copy(body[rel:rel+14], desired)
		return true, gate3AMsg(fnVA+uint64(rel), maxTokens), nil
	}
	full := desiredGate3AAmd64(0)
	if idx := bytes.Index(body, full); idx >= 0 {
		if bytes.Equal(desired, full) {
			return false, fmt.Sprintf("Gate 3A: already full-window @ %#x", fnVA+uint64(idx)), nil
		}
		copy(body[idx:idx+14], desired)
		return true, gate3AMsg(fnVA+uint64(idx), maxTokens), nil
	}
	return false, "", fmt.Errorf("Gate 3A: 256K clamp pattern not found (unsupported agy build?)")
}

func gate3AMsg(va uint64, maxTokens int64) string {
	if maxTokens <= 0 {
		return fmt.Sprintf("Gate 3A: removed 256K clamp (full window) @ %#x", va)
	}
	return fmt.Sprintf("Gate 3A: set ceiling %d @ %#x", maxTokens, va)
}

// --------------------------------------------------------------------------
// Gate 3B: stop the experiment checkpointer from resetting the ceiling to 256K
// --------------------------------------------------------------------------

func patchGate3B(buf []byte, fnOff, size int, fnVA uint64, arch string) (bool, string, error) {
	if arch == "arm64" {
		return patchGate3BArm64(buf, fnOff, size, fnVA)
	}
	return patchGate3BAmd64(buf, fnOff, size, fnVA)
}

func patchGate3BArm64(buf []byte, fnOff, size int, fnVA uint64) (bool, string, error) {
	body := buf[fnOff : fnOff+size]
	const strWzrX0 = 0xB9000C1F // str wzr,[x0,#0xc]
	for rel := 4; rel+16 <= len(body); rel += 4 {
		wPrev := binary.LittleEndian.Uint32(body[rel-4:])
		w0 := binary.LittleEndian.Uint32(body[rel:])
		w1 := binary.LittleEndian.Uint32(body[rel+4:])
		if wPrev&0xFC000000 != 0x94000000 { // prev must be `bl`
			continue
		}
		if w0&0xFF00001F == 0xB4000000 && w1 == strWzrX0 {
			return false, fmt.Sprintf("Gate 3B: already patched @ %#x", fnVA+uint64(rel)), nil
		}
		if w0&0xFF00001F != 0xB4000001 { // cbz x1,<merge>
			continue
		}
		imm19 := int32((w0 >> 5) & 0x7ffff)
		if imm19&0x40000 != 0 {
			imm19 -= 0x80000
		}
		span := int(imm19) * 4
		if span < 8 || span > 64 || rel+span+8 > len(body) {
			continue
		}
		wm0 := binary.LittleEndian.Uint32(body[rel+span:])
		wm1 := binary.LittleEndian.Uint32(body[rel+span+4:])
		if wm0&0xFFC003FF != 0xF94003E1 || wm1 != 0xAA0003E2 { // ldr x1,[sp,#imm]; mov x2,x0
			continue
		}
		cbzX0 := w0 & 0xFFFFFFE0 // same target, Rt -> x0
		blob := make([]byte, span)
		binary.LittleEndian.PutUint32(blob[0:], cbzX0)
		binary.LittleEndian.PutUint32(blob[4:], strWzrX0)
		for i := 8; i < span; i += 4 {
			copy(blob[i:], nopARM64())
		}
		copy(body[rel:rel+span], blob)
		return true, fmt.Sprintf("Gate 3B: zeroed experiment ceiling @ %#x", fnVA+uint64(rel)), nil
	}
	return false, "", fmt.Errorf("Gate 3B: override site not found (unsupported agy build?)")
}

func patchGate3BAmd64(buf []byte, fnOff, size int, fnVA uint64) (bool, string, error) {
	body := buf[fnOff : fnOff+size]
	// Bypass the `test %al,%al ; jne` guard that applies the experiment override.
	for rel := 0; rel+2 <= len(body); rel++ {
		if body[rel] != 0x84 || body[rel+1] != 0xc0 { // test %al,%al
			continue
		}
		j := rel + 2
		if j+2 <= len(body) && body[j] == 0x90 && body[j+1] == 0x90 {
			return false, fmt.Sprintf("Gate 3B: already patched @ %#x", fnVA+uint64(j)), nil
		}
		if j < len(body) && body[j] == 0x75 { // jne rel8
			body[j] = 0x90
			body[j+1] = 0x90
			return true, fmt.Sprintf("Gate 3B: bypassed experiment override @ %#x", fnVA+uint64(j)), nil
		}
		if j+6 <= len(body) && body[j] == 0x0f && body[j+1] == 0x85 { // jne rel32
			for k := 0; k < 6; k++ {
				body[j+k] = 0x90
			}
			return true, fmt.Sprintf("Gate 3B: bypassed experiment override @ %#x", fnVA+uint64(j)), nil
		}
	}
	return false, "", fmt.Errorf("Gate 3B: override site not found (unsupported agy build?)")
}

// --------------------------------------------------------------------------
// Atomic write + macOS re-sign
// --------------------------------------------------------------------------

func writeAtomicSigned(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".ag-ctxpatch-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err := resignDarwin(tmpName, dst); err != nil {
			return err
		}
	}
	return os.Rename(tmpName, dst)
}

var identifierRe = regexp.MustCompile(`(?m)^Identifier=(.+)$`)

func resignDarwin(tmpPath, origPath string) error {
	if _, err := exec.LookPath("codesign"); err != nil {
		return nil // no codesign available; leave the unsigned temp to run ad-hoc
	}
	args := []string{"--force", "--sign", "-", "--options", "runtime"}
	if id := readCodesignIdentifier(origPath); id != "" {
		args = append(args, "--identifier", id)
	}
	args = append(args, tmpPath)
	if out, err := exec.Command("codesign", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("codesign", "--verify", "--verbose=1", tmpPath).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign verify failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func readCodesignIdentifier(path string) string {
	out, _ := exec.Command("codesign", "-d", "--verbose=2", path).CombinedOutput()
	if m := identifierRe.FindSubmatch(out); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}
