package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"antigravity-gateway/internal/updater"
)

func (gs *GatewayServer) SetUpdater(u *updater.Updater) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.updater = u
}

func (gs *GatewayServer) getUpdater() *updater.Updater {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return gs.updater
}

func (gs *GatewayServer) SetRestartFunc(fn func(execPath string)) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	gs.restartFn = fn
}

func (gs *GatewayServer) getRestartFunc() func(execPath string) {
	gs.mu.RLock()
	defer gs.mu.RUnlock()
	return gs.restartFn
}

func (gs *GatewayServer) handleAdminUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := gs.getUpdater()
	if u == nil {
		writeJSONError(w, http.StatusInternalServerError, "更新管理器未初始化")
		return
	}
	forceRefresh := r.URL.Query().Get("check") == "1" || r.URL.Query().Get("refresh") == "1"
	poolCfg := gs.cfg.GetACPPool()

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	status := u.GetStatus(ctx, poolCfg.BackendType, poolCfg.BinaryPath, forceRefresh)
	writeJSON(w, http.StatusOK, status)
}

func (gs *GatewayServer) handleAdminUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	u := gs.getUpdater()
	if u == nil {
		writeJSONError(w, http.StatusInternalServerError, "更新管理器未初始化")
		return
	}

	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "无效的更新请求格式")
		return
	}
	target := strings.ToLower(strings.TrimSpace(body.Target))
	if target != "gateway" && target != "acp" && target != "all" {
		writeJSONError(w, http.StatusBadRequest, "请指定有效的更新目标 (gateway | acp | all)")
		return
	}

	poolCfg := gs.cfg.GetACPPool()
	applyCtx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	onACPUpdated := func(_ string, newBinPath string) int {
		gs.cfg.SetACPBinaryPath(newBinPath)
		retired := 0
		if gs.pool != nil {
			retired = gs.pool.UpdateACPBinary("", newBinPath)
			go func() {
				warmCtx, warmCancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer warmCancel()
				_, _, _ = gs.pool.GetOrDiscoverModels(warmCtx, "", true)
			}()
		}
		return retired
	}

	res, err := u.Apply(applyCtx, target, poolCfg.BackendType, poolCfg.BinaryPath, onACPUpdated)
	if err != nil {
		if errors.As(err, &updater.ErrAlreadyUpdating{}) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	restartFn := gs.getRestartFunc()
	willRestart := res.UpdatedGateway && restartFn != nil
	res.Restarting = willRestart

	writeJSON(w, http.StatusOK, res)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	if willRestart {
		execPath := res.ExecutablePath
		go func() {
			time.Sleep(350 * time.Millisecond)
			restartFn(execPath)
		}()
	}
}
