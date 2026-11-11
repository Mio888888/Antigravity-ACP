package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
	"antigravity-gateway/internal/version"
)

func (gs *GatewayServer) setAccountCheckMeta(accountID, msg string, groups []storage.ModelQuotaGroup) {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	if gs.accountCheckAt == nil {
		gs.accountCheckAt = make(map[string]string)
	}
	if gs.accountCheckMsg == nil {
		gs.accountCheckMsg = make(map[string]string)
	}
	if gs.accountQuotaGroups == nil {
		gs.accountQuotaGroups = make(map[string][]storage.ModelQuotaGroup)
	}
	gs.accountCheckAt[accountID] = time.Now().UTC().Format(time.RFC3339)
	gs.accountCheckMsg[accountID] = msg
	if len(groups) > 0 {
		gs.accountQuotaGroups[accountID] = groups
	}
}

func (gs *GatewayServer) RefreshAccountQuota(ctx context.Context, accountID string) ([]storage.ModelQuotaGroup, string, error) {
	qCtx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	groups, msg, err := gs.pool.FetchAccountQuotaViaACP(qCtx, accountID)
	if err != nil {
		gs.setAccountCheckMeta(accountID, msg, nil)
		return nil, msg, err
	}
	gs.setAccountCheckMeta(accountID, msg, groups)
	if len(groups) > 0 && gs.store != nil {
		_ = gs.store.UpdateAccountStatus(accountID, "ready", "")
		gs.pool.MarkAccountReady(accountID)
	}
	return groups, msg, nil
}

func (gs *GatewayServer) enrichAccountSnapshotsWithUsage() []acp.AccountRuntimeSnapshot {
	snaps := gs.pool.AccountSnapshots()
	usageMap := make(map[string]storage.AccountUsageStats)
	if gs.store != nil {
		if m, err := gs.store.GetAccountUsageMap(); err == nil {
			usageMap = m
		}
	}

	// Auto-fetch /usage quota asynchronously in the background for ready accounts when using a real agy binary
	binLower := strings.ToLower(gs.cfg.GetACPBinaryPath())
	isRealAgy := strings.Contains(binLower, "agy") || strings.Contains(binLower, "antigravity") || binLower == "auto"
	if isRealAgy {
		for _, snap := range snaps {
			accID := snap.Account.ID
			if !snap.Account.Enabled || snap.Account.Status != "ready" {
				continue
			}
			gs.mu.Lock()
			_, checked := gs.accountCheckAt[accID]
			if gs.quotaRefreshing == nil {
				gs.quotaRefreshing = make(map[string]bool)
			}
			refreshing := gs.quotaRefreshing[accID]
			if !checked && !refreshing {
				gs.quotaRefreshing[accID] = true
				go func(id string) {
					defer func() {
						gs.mu.Lock()
						delete(gs.quotaRefreshing, id)
						gs.mu.Unlock()
					}()
					_, _, _ = gs.RefreshAccountQuota(context.Background(), id)
				}(accID)
			}
			gs.mu.Unlock()
		}
	}

	gs.mu.RLock()
	defer gs.mu.RUnlock()
	for i := range snaps {
		snaps[i].Account = snaps[i].Account.Redacted()
		accID := snaps[i].Account.ID
		u, ok := usageMap[accID]
		if !ok {
			u = storage.AccountUsageStats{
				SuccessRatePct: 100,
			}
		}
		if qg, exists := gs.accountQuotaGroups[accID]; exists && len(qg) > 0 {
			u.QuotaGroups = qg
		} else {
			u.QuotaGroups = nil
		}
		if ts, exists := gs.accountCheckAt[accID]; exists && ts != "" {
			u.QuotaReadAt = ts
			u.LastCheckAt = ts
		}
		if msg, exists := gs.accountCheckMsg[accID]; exists && msg != "" {
			u.LastCheckMsg = msg
		} else {
			u.LastCheckMsg = "待点击刷新额度读取"
		}
		snaps[i].Usage = u
	}
	return snaps
}

func (gs *GatewayServer) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	stats, _ := gs.store.GetOverviewStats()
	workers := gs.pool.Snapshot()
	if gs.sm != nil && len(workers) > 0 {
		boundMap := gs.sm.WorkerBoundSessionsMap()
		for i := range workers {
			if bs, ok := boundMap[workers[i].PID]; ok && len(bs) > 0 {
				workers[i].BoundSessions = bs
			}
		}
	}
	models, cachedAt, ttl := gs.pool.ModelsCacheInfo()
	if models == nil {
		models = []acp.ModelInfo{}
	}
	// Trigger non-blocking background model discovery if cache is empty or expired,
	// so /api/admin/overview always responds in <2ms without blocking on agy acp subprocess startup.
	if len(models) == 0 || (!cachedAt.IsZero() && ttl > 0 && time.Since(cachedAt) >= ttl) {
		binLower := strings.ToLower(gs.cfg.GetACPBinaryPath())
		if strings.Contains(binLower, "agy") || strings.Contains(binLower, "antigravity") || binLower == "auto" {
			go func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_, _, _ = gs.pool.GetOrDiscoverModels(bgCtx, "", false)
			}()
		} else {
			mCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if discovered, cAt, _ := gs.pool.GetOrDiscoverModels(mCtx, "", false); len(discovered) > 0 {
				models = discovered
				cachedAt = cAt
			}
		}
	}
	accounts := gs.enrichAccountSnapshotsWithUsage()
	var cachedAtStr string
	if !cachedAt.IsZero() {
		cachedAtStr = cachedAt.UTC().Format(time.RFC3339)
	}
	promptPolicy := gs.cfg.GetPromptPolicy()
	sandboxPolicy := gs.cfg.GetSandboxPolicy()
	sessionMode := sandboxPolicy.SessionMode
	if gs.store != nil {
		if primaryKey, ok := gs.store.GetPrimaryAPIKey(); ok && primaryKey != nil {
			if strings.TrimSpace(primaryKey.PromptMode) != "" {
				promptPolicy.Mode = strings.TrimSpace(primaryKey.PromptMode)
			}
			promptPolicy.GlobalPrependPrompt = primaryKey.GlobalPrependPrompt
			promptPolicy.DisableBuiltinTools = primaryKey.DisableBuiltinTools
			if strings.TrimSpace(primaryKey.SessionMode) != "" {
				sessionMode = strings.TrimSpace(primaryKey.SessionMode)
			}
		}
	}
	resp := map[string]any{
		"stats":                stats,
		"active_workers":       len(workers),
		"workers":              workers,
		"parked_tools":         gs.sm.ParkedCount(),
		"models":               models,
		"models_cached_at":     cachedAtStr,
		"models_cache_ttl_sec": int(ttl.Seconds()),
		"accounts":             accounts,
		"prompt_policy":        promptPolicy,
		"session_mode":         sessionMode,
		"public_base_url":      gs.cfg.Server.PublicBaseURL,
		"credentials_locked":   gs.isAdminCredentialsLocked(),
		"gateway_version":      version.Version,
	}
	if r.URL.Query().Get("include_logs") == "1" && gs.store != nil {
		logsLimit, _ := strconv.Atoi(r.URL.Query().Get("logs_limit"))
		if logsLimit <= 0 {
			logsLimit = 60
		}
		if logs, err := gs.store.ListAuditLogsCompact(logsLimit, 1); err == nil {
			resp["logs"] = logs
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (gs *GatewayServer) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	qRefresh := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("refresh")))
	forceRefresh := r.Method == http.MethodPost || qRefresh == "1" || qRefresh == "true"
	accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))

	mCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	models, cachedAt, err := gs.pool.GetOrDiscoverModels(mCtx, accountID, forceRefresh)
	if models == nil {
		models = []acp.ModelInfo{}
	}
	_, _, ttl := gs.pool.ModelsCacheInfo()
	var cachedAtStr string
	if !cachedAt.IsZero() {
		cachedAtStr = cachedAt.UTC().Format(time.RFC3339)
	}
	resp := map[string]any{
		"models":               models,
		"models_cached_at":     cachedAtStr,
		"models_cache_ttl_sec": int(ttl.Seconds()),
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (gs *GatewayServer) handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"accounts": gs.enrichAccountSnapshotsWithUsage(),
		})
	case http.MethodPost:
		var acc storage.Account
		if err := json.NewDecoder(r.Body).Decode(&acc); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		previous, prevErr := gs.store.GetAccount(strings.TrimSpace(acc.ID))
		saved, err := gs.store.UpsertAccount(acc)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := gs.pool.UpsertAccountPool(*saved); err != nil {
			if prevErr == nil && previous != nil {
				_, _ = gs.store.UpsertAccount(*previous)
			} else {
				_ = gs.store.DeleteAccount(saved.ID)
			}
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, saved.Redacted())
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (gs *GatewayServer) handleAdminAccountAction(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/admin/accounts/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONError(w, http.StatusBadRequest, "missing account id")
		return
	}
	accountID := parts[0]

	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := gs.store.DeleteAccount(accountID); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = gs.pool.RemoveAccountPool(accountID)
		writeJSON(w, http.StatusOK, map[string]any{"deleted": accountID})
		return
	}

	action := strings.Join(parts[1:], "/")
	switch action {
	case "login/status", "disabled-models":
	default:
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
	}
	switch action {
	case "login/start", "login":
		st, err := gs.pool.StartTerminalLogin(accountID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "login/submit":
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		st, err := gs.pool.SubmitTerminalLoginCode(accountID, body.Code)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if st.State == "success" {
			_ = gs.store.UpdateAccountStatus(accountID, "ready", "")
			gs.pool.MarkAccountReady(accountID)
			gs.pool.RestartIdleWorkers()
			_, _, _ = gs.RefreshAccountQuota(r.Context(), accountID)
		}
		writeJSON(w, http.StatusOK, st)
	case "login/status":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, gs.pool.GetTerminalLoginStatus(accountID))
	case "probe", "probe-net", "net-check":
		var body struct {
			ProbeURL string `json:"probe_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		ip, err := gs.pool.ProbeAccountNetwork(ctx, accountID, body.ProbeURL)
		if err != nil {
			gs.setAccountCheckMeta(accountID, "出口检测异常: "+err.Error(), nil)
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		_ = gs.store.UpdateAccountStatus(accountID, "ready", ip)
		groups, msg, _ := gs.RefreshAccountQuota(r.Context(), accountID)
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":   accountID,
			"egress_ip":    ip,
			"quota_groups": groups,
			"message":      msg,
		})
	case "refresh-quota":
		groups, msg, err := gs.RefreshAccountQuota(r.Context(), accountID)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, msg)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":   accountID,
			"refreshed":    true,
			"quota_groups": groups,
			"message":      msg,
		})
	case "clear-cooldown":
		_ = gs.store.UpdateAccountStatus(accountID, "ready", "")
		gs.pool.MarkAccountReady(accountID)
		gs.pool.RestartIdleWorkers()
		gs.setAccountCheckMeta(accountID, "冷却已清除", nil)
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":       accountID,
			"cleared_cooldown": true,
		})
	case "models", "refresh-models":
		mCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		models, cachedAt, err := gs.pool.GetOrDiscoverModels(mCtx, accountID, true)
		if models == nil {
			models = []acp.ModelInfo{}
		}
		var cachedAtStr string
		if !cachedAt.IsZero() {
			cachedAtStr = cachedAt.UTC().Format(time.RFC3339)
		}
		resp := map[string]any{
			"account_id":       accountID,
			"models":           models,
			"models_cached_at": cachedAtStr,
		}
		if err != nil {
			resp["error"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
	case "disabled-models":
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			DisabledModels []string `json:"disabled_models"`
			ModelID        string   `json:"model_id"`
			Disabled       *bool    `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		existingAcc, err := gs.store.GetAccount(accountID)
		if err != nil || existingAcc == nil {
			writeJSONError(w, http.StatusNotFound, "account not found")
			return
		}
		nextDisabled := existingAcc.DisabledModels
		if body.DisabledModels != nil {
			nextDisabled = body.DisabledModels
		} else if mID := strings.TrimSpace(body.ModelID); mID != "" {
			currentlyDisabled := existingAcc.IsModelDisabled(mID)
			targetDisabled := !currentlyDisabled
			if body.Disabled != nil {
				targetDisabled = *body.Disabled
			}
			filtered := make([]string, 0, len(existingAcc.DisabledModels)+1)
			for _, dm := range existingAcc.DisabledModels {
				if !strings.EqualFold(strings.TrimSpace(dm), mID) {
					filtered = append(filtered, dm)
				}
			}
			if targetDisabled {
				filtered = append(filtered, mID)
			}
			nextDisabled = filtered
		}
		saved, err := gs.store.UpdateAccountDisabledModels(accountID, nextDisabled)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		gs.pool.SetAccountDisabledModels(accountID, saved.DisabledModels)
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":      accountID,
			"disabled_models": saved.DisabledModels,
			"account":         saved.Redacted(),
		})
	default:
		writeJSONError(w, http.StatusNotFound, "unknown account action")
	}
}

func (gs *GatewayServer) handleAdminWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"workers": gs.pool.Snapshot(),
		})
		return
	}
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (gs *GatewayServer) handleAdminWorkersScale(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	killed := gs.pool.RestartIdleWorkers()
	writeJSON(w, http.StatusOK, map[string]any{"restarted_idle_workers": killed})
}

func (gs *GatewayServer) handleAdminWorkerKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/admin/workers/")
	workerID := strings.TrimSuffix(trimmed, "/kill")
	workerID = strings.Trim(workerID, "/")
	ok := gs.pool.KillWorker(workerID)
	writeJSON(w, http.StatusOK, map[string]any{"killed": ok, "worker_id": workerID})
}

func (gs *GatewayServer) handleAdminPromptPolicy(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p := gs.cfg.GetPromptPolicy()
		sb := gs.cfg.GetSandboxPolicy()
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":                  p.Mode,
			"global_prepend_prompt": p.GlobalPrependPrompt,
			"disable_builtin_tools": p.DisableBuiltinTools,
			"session_mode":          sb.SessionMode,
			"internal_approval":     sb.InternalApproval,
		})
	case http.MethodPut:
		var body struct {
			config.PromptPolicyConfig
			SessionMode      string `json:"session_mode"`
			InternalApproval string `json:"internal_approval"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := gs.cfg.UpdatePromptPolicy(body.PromptPolicyConfig); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		gs.cfg.UpdateSandboxPolicy(body.SessionMode, body.InternalApproval)
		sb := gs.cfg.GetSandboxPolicy()
		if gs.pool != nil {
			gs.pool.UpdateSandboxPolicy(sb)
		}
		p := gs.cfg.GetPromptPolicy()
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":                  p.Mode,
			"global_prepend_prompt": p.GlobalPrependPrompt,
			"disable_builtin_tools": p.DisableBuiltinTools,
			"session_mode":          sb.SessionMode,
			"internal_approval":     sb.InternalApproval,
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (gs *GatewayServer) handleAdminAuthEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var updates map[string]string
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	gs.cfg.UpdateACPEnv(updates)
	restarted := gs.pool.RestartIdleWorkers()
	writeJSON(w, http.StatusOK, map[string]any{"updated": true, "restarted_idle": restarted})
}

func (gs *GatewayServer) handleAdminDefaultLoginStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	st, err := gs.pool.StartTerminalLogin("default")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (gs *GatewayServer) handleAdminDefaultLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	st, err := gs.pool.SubmitTerminalLoginCode("default", body.Code)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (gs *GatewayServer) handleAdminSkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		skills, err := gs.store.ListSkills(false)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"skills": skills})
	case http.MethodPost:
		var sk storage.PresetSkill
		if err := json.NewDecoder(r.Body).Decode(&sk); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		saved, err := gs.store.UpsertSkill(sk)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, saved)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (gs *GatewayServer) handleAdminSkillDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/admin/skills/")
	id, err := strconv.ParseInt(strings.Trim(idStr, "/"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid skill id")
		return
	}
	if err := gs.store.DeleteSkill(id); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (gs *GatewayServer) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		keys, err := gs.store.ListAPIKeys()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "api_keys": keys})
	case http.MethodPost:
		var body struct {
			Name                string `json:"name"`
			BoundAccountID      string `json:"bound_account_id"`
			RateLimitRPM        int    `json:"rate_limit_rpm"`
			PromptMode          string `json:"prompt_mode"`
			SessionMode         string `json:"session_mode"`
			GlobalPrependPrompt string `json:"global_prepend_prompt"`
			DisableBuiltinTools *bool  `json:"disable_builtin_tools"`
			ContextWindow       int64  `json:"context_window"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		defaultPromptPolicy := gs.cfg.GetPromptPolicy()
		disableBuiltin := defaultPromptPolicy.DisableBuiltinTools
		if body.DisableBuiltinTools != nil {
			disableBuiltin = *body.DisableBuiltinTools
		}
		promptMode := strings.TrimSpace(body.PromptMode)
		if promptMode == "" {
			promptMode = defaultPromptPolicy.Mode
		}
		sessionMode := strings.TrimSpace(body.SessionMode)
		if sessionMode == "" {
			sessionMode = gs.cfg.GetSandboxPolicy().SessionMode
		}
		plainKey, rec, err := gs.store.CreateAPIKeyWithPolicy(storage.APIKey{
			Name:                body.Name,
			BoundAccountID:      body.BoundAccountID,
			RateLimitRPM:        body.RateLimitRPM,
			PromptMode:          promptMode,
			SessionMode:         sessionMode,
			GlobalPrependPrompt: body.GlobalPrependPrompt,
			DisableBuiltinTools: disableBuiltin,
			ContextWindow:       body.ContextWindow,
		})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"plain_key": plainKey,
			"record":    rec,
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (gs *GatewayServer) handleAdminKeyDelete(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/admin/keys/"), "/api/admin/api-keys/")
	id, err := strconv.ParseInt(strings.Trim(idStr, "/"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid api key id")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := gs.store.DeleteAPIKey(id); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
	case http.MethodPut, http.MethodPost:
		var body struct {
			Name                string `json:"name"`
			BoundAccountID      string `json:"bound_account_id"`
			RateLimitRPM        int    `json:"rate_limit_rpm"`
			PromptMode          string `json:"prompt_mode"`
			SessionMode         string `json:"session_mode"`
			GlobalPrependPrompt string `json:"global_prepend_prompt"`
			DisableBuiltinTools *bool  `json:"disable_builtin_tools"`
			ContextWindow       int64  `json:"context_window"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		disableBuiltin := gs.cfg.GetPromptPolicy().DisableBuiltinTools
		if body.DisableBuiltinTools != nil {
			disableBuiltin = *body.DisableBuiltinTools
		}
		updated, err := gs.store.UpdateAPIKey(id, storage.APIKey{
			Name:                body.Name,
			BoundAccountID:      body.BoundAccountID,
			RateLimitRPM:        body.RateLimitRPM,
			PromptMode:          body.PromptMode,
			SessionMode:         body.SessionMode,
			GlobalPrependPrompt: body.GlobalPrependPrompt,
			DisableBuiltinTools: disableBuiltin,
			ContextWindow:       body.ContextWindow,
		})
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"record": updated})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (gs *GatewayServer) handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	if reqID := strings.TrimSpace(r.URL.Query().Get("request_id")); reqID != "" {
		logRec, err := gs.store.GetAuditLog(reqID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"log":  logRec,
			"logs": []*storage.AuditLog{logRec},
		})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if r.URL.Query().Get("compact") == "1" {
		logs, err := gs.store.ListAuditLogsCompact(limit, 1)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
		return
	}
	logs, err := gs.store.ListAuditLogs(limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (gs *GatewayServer) handleAdminPasswordLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !isSameOriginRequest(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin admin request rejected")
		return
	}
	clientIP := clientIPFromRequest(r)
	if !gs.checkLoginRateLimit(clientIP) {
		writeJSONError(w, http.StatusTooManyRequests, "登录失败次数过多，请 5 分钟后再试")
		return
	}

	var body struct {
		Password string `json:"password"`
		Token    string `json:"token"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "无效的登录请求格式")
		return
	}
	credential := strings.TrimSpace(body.Password)
	if credential == "" {
		credential = strings.TrimSpace(body.Token)
	}
	if !gs.verifyAdminLoginCredential(credential) {
		gs.recordLoginFailure(clientIP)
		writeJSONError(w, http.StatusUnauthorized, "管理员密码错误，请重新输入")
		return
	}

	gs.clearLoginFailures(clientIP)
	ttl := 24 * time.Hour
	if body.Remember {
		ttl = 7 * 24 * time.Hour
	}
	sessToken, exp, err := gs.issueAdminSession(ttl)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "无法创建登录会话")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    sessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(ttl.Seconds()),
		Expires:  exp,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"session_token": sessToken,
		"expires_at":    exp.Format(time.RFC3339),
	})
}

func (gs *GatewayServer) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !isSameOriginRequest(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin admin request rejected")
		return
	}
	token := extractToken(r, false)
	if token != "" {
		gs.revokeAdminSession(token)
	}
	if c, err := r.Cookie(adminSessionCookie); err == nil && c.Value != "" {
		gs.revokeAdminSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"logged_out": true})
}

func (gs *GatewayServer) handleAdminSessionCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated":      true,
		"credentials_locked": gs.isAdminCredentialsLocked(),
	})
}

func (gs *GatewayServer) handleAdminChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		NewAdminToken   string `json:"new_admin_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "无效的请求参数")
		return
	}
	currentPwd := strings.TrimSpace(body.CurrentPassword)
	newPwd := strings.TrimSpace(body.NewPassword)
	newToken := strings.TrimSpace(body.NewAdminToken)
	clientIP := clientIPFromRequest(r)
	if !gs.checkLoginRateLimit(clientIP) {
		writeJSONError(w, http.StatusTooManyRequests, "认证失败次数过多，请 5 分钟后再试")
		return
	}
	if !gs.verifyAdminLoginCredential(currentPwd) {
		gs.recordLoginFailure(clientIP)
		writeJSONError(w, http.StatusUnauthorized, "当前管理员密码或 Token 不正确")
		return
	}
	if len(newPwd) < adminPasswordMinLen {
		writeJSONError(w, http.StatusBadRequest, "新密码长度不能少于 8 位")
		return
	}
	if newToken != "" && len(newToken) < adminTokenMinLen {
		writeJSONError(w, http.StatusBadRequest, "自定义 Admin Token 长度不能少于 12 位")
		return
	}
	if gs.store != nil {
		if err := gs.persistAdminCredentials(newPwd, newToken); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "保存新凭证失败: "+err.Error())
			return
		}
	}
	gs.cfg.SetAdminPassword(newPwd)
	if newToken != "" {
		gs.cfg.SetAdminToken(newToken)
	}
	gs.writeAdminCredentialsFile("", "", true)
	gs.revokeAllAdminSessions()
	sessToken, exp, err := gs.issueAdminSession(24 * time.Hour)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "密码已更新，但重新签发会话失败")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    sessToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   int((24 * time.Hour).Seconds()),
		Expires:  exp,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"updated":            true,
		"credentials_locked": true,
		"session_token":      sessToken,
		"expires_at":         exp.Format(time.RFC3339),
	})
}
