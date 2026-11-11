package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/acp/ctxpatch"
	"antigravity-gateway/internal/updater"
)

// rewarmACPPoolAsync kicks a background model discovery so freshly rotated workers
// are pre-warmed, mirroring the behavior after a hot ACP update.
func (gs *GatewayServer) rewarmACPPoolAsync() {
	if gs.pool == nil {
		return
	}
	go func() {
		warmCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _, _ = gs.pool.GetOrDiscoverModels(warmCtx, "", true)
	}()
}

// acpBinaryAvailable reports whether path resolves to an existing executable, either as
// an absolute/relative file or via PATH lookup.
func acpBinaryAvailable(path string) bool {
	p := strings.TrimSpace(path)
	if p == "" {
		return false
	}
	if strings.ContainsAny(p, `/\`) {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	_, err := exec.LookPath(p)
	return err == nil
}

// handleAdminACPPatch applies (or disables) the context-window binary patch on the
// currently effective agy binary, then hot-rotates idle workers so new sessions pick it
// up. POST body: {"enabled": bool (optional, default true), "tokens": int64}.
func (gs *GatewayServer) handleAdminACPPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
		Tokens  int64 `json:"tokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "无效的打补丁请求格式")
		return
	}
	tokens := body.Tokens
	if tokens < 0 {
		tokens = 0
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	poolCfg := gs.cfg.GetACPPool()
	binPath := acp.ResolveACPBinaryPath(poolCfg.BackendType, poolCfg.BinaryPath)
	if !ctxpatch.IsCLIBinary(binPath) {
		writeJSONError(w, http.StatusBadRequest, "当前 ACP 后端不是 Antigravity CLI (agy)，不支持上下文补丁；请先在账号管理切换后端")
		return
	}

	// Persist the preference so it survives restarts and is re-applied after updates.
	gs.cfg.SetContextPatch(enabled, tokens)
	if gs.store != nil {
		_ = gs.store.SetSetting(updater.SettingACPContextPatch, boolToSetting(enabled))
		_ = gs.store.SetSetting(updater.SettingACPContextPatchTokens, strconv.FormatInt(tokens, 10))
	}

	resp := map[string]any{
		"enabled":     enabled,
		"tokens":      tokens,
		"binary_path": binPath,
	}
	if enabled {
		res, err := ctxpatch.Apply(binPath, tokens)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "打补丁失败: "+err.Error())
			return
		}
		resp["changed"] = res.Changed
		resp["arch"] = res.Arch
		resp["max_tokens"] = res.MaxTokens
		resp["gate3a"] = res.Gate3A
		resp["gate3b"] = res.Gate3B
	}

	retired := 0
	if gs.pool != nil {
		retired = gs.pool.UpdateACPBinary("", binPath)
		gs.rewarmACPPoolAsync()
	}
	resp["retired_workers"] = retired

	writeJSON(w, http.StatusOK, resp)
}

// handleAdminACPBackend switches the global ACP backend engine (Antigravity CLI <-> Zed
// ACP), downloading the target flavor if it is not already installed, then hot-rotates
// workers. If the context patch is enabled and the new backend is the CLI, the patch is
// re-applied. POST body: {"backend_type": "cli" | "zed"}.
func (gs *GatewayServer) handleAdminACPBackend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		BackendType string `json:"backend_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "无效的后端切换请求格式")
		return
	}
	bt := acp.NormalizeBackendType(body.BackendType)
	if bt != "cli" && bt != "zed" {
		writeJSONError(w, http.StatusBadRequest, "请指定有效的 ACP 后端类型 (cli | zed)")
		return
	}

	binPath := acp.ResolveACPBinaryPath(bt, "")
	retired := 0

	if acpBinaryAvailable(binPath) {
		gs.cfg.SetBackendType(bt)
		gs.cfg.SetACPBinaryPath(binPath)
		if gs.pool != nil {
			retired = gs.pool.UpdateACPBinary(bt, binPath)
			gs.rewarmACPPoolAsync()
		}
	} else {
		// Target flavor not installed locally: fetch it via the updater, which also
		// rotates workers and sets the resolved binary path through onACPUpdated.
		u := gs.getUpdater()
		if u == nil {
			writeJSONError(w, http.StatusInternalServerError, "更新管理器未初始化，无法下载目标后端")
			return
		}
		onACPUpdated := func(_ string, newBinPath string) int {
			gs.cfg.SetACPBinaryPath(newBinPath)
			if gs.pool != nil {
				n := gs.pool.UpdateACPBinary(bt, newBinPath)
				gs.rewarmACPPoolAsync()
				return n
			}
			return 0
		}
		applyCtx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		res, err := u.Apply(applyCtx, "acp", bt, "", onACPUpdated)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "下载目标 ACP 后端失败: "+err.Error())
			return
		}
		retired = res.RetiredWorkers
		if strings.TrimSpace(res.ACPBinaryPath) != "" {
			binPath = res.ACPBinaryPath
		}
		gs.cfg.SetBackendType(bt)
	}

	if gs.store != nil {
		_ = gs.store.SetSetting(updater.SettingACPBackendType, bt)
	}

	// Re-apply the context patch on the new binary when enabled (no-op for Zed).
	enabled, tokens := gs.cfg.GetContextPatch()
	if _, err := ctxpatch.EnsurePatched(binPath, enabled, tokens); err == nil && ctxpatch.IsCLIBinary(binPath) && enabled {
		if gs.pool != nil {
			_ = gs.pool.UpdateACPBinary("", binPath)
			gs.rewarmACPPoolAsync()
		}
	}

	flavor := "cli"
	if bt == "zed" {
		flavor = "zed"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"backend_type":    bt,
		"flavor":          flavor,
		"binary_path":     binPath,
		"retired_workers": retired,
		"supports_patch":  ctxpatch.IsCLIBinary(binPath),
	})
}

func boolToSetting(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
