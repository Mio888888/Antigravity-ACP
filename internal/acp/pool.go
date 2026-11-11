package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"
)

const DefaultModelsCacheTTL = 5 * time.Minute

type WorkerBoundSession struct {
	BridgeID     string    `json:"bridge_id"`
	ACPSessionID string    `json:"acp_session_id,omitempty"`
	ExplicitKey  string    `json:"explicit_key,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type WorkerSnapshot struct {
	ID            string               `json:"id"`
	AccountID     string               `json:"account_id"`
	PID           int                  `json:"pid"`
	State         WorkerState          `json:"state"`
	LastActiveAt  time.Time            `json:"last_active_at"`
	BoundSessions []WorkerBoundSession `json:"bound_sessions,omitempty"`
}

type AccountRuntimeSnapshot struct {
	Account        storage.Account           `json:"account"`
	GEMINIHome     string                    `json:"gemini_home"`
	RelayListen    string                    `json:"relay_listen"`
	WorkerCount    int                       `json:"worker_count"`
	BusyWorkers    int                       `json:"busy_workers"`
	LoginState     string                    `json:"login_state"`
	LoginAuthURL   string                    `json:"login_auth_url,omitempty"`
	Models         []ModelInfo               `json:"models"`
	ModelsCachedAt string                    `json:"models_cached_at,omitempty"`
	Usage          storage.AccountUsageStats `json:"usage"`
}

type TerminalLoginStatus struct {
	AccountID string `json:"account_id"`
	State     string `json:"state"` // "idle" | "waiting_code" | "success" | "error"
	AuthURL   string `json:"auth_url,omitempty"`
	Output    string `json:"output,omitempty"`
}

type accountSubPool struct {
	account          storage.Account
	geminiHome       string
	relay            *EgressRelay
	workers          map[string]*WorkerClient
	discoveredModels []ModelInfo
	modelsCachedAt   time.Time
	loginCmd         *exec.Cmd
	loginStdin       io.WriteCloser
	loginDoneCh      chan struct{}
	loginStatus      TerminalLoginStatus
	// pending counts workers being spawned so max_workers holds across the unlocked spawn.
	pending int
	// gen increments on every account config change; workers from older generations
	// are retired when released.
	gen uint64
	// retiredRelays are replaced relays still used by in-flight workers.
	retiredRelays []*EgressRelay
}

// accountNetNS returns the network namespace a subprocess for acc must run in.
func accountNetNS(acc storage.Account) string {
	if acc.NetworkMode != "netns" {
		return ""
	}
	return strings.TrimSpace(acc.NetNSName)
}

func sameEgressConfig(a, b storage.Account) bool {
	return a.NetworkMode == b.NetworkMode &&
		strings.TrimSpace(a.ProxyURL) == strings.TrimSpace(b.ProxyURL) &&
		strings.TrimSpace(a.BindIP) == strings.TrimSpace(b.BindIP)
}

// sweepRetiredRelaysLocked closes replaced relays no live worker still uses.
func (sp *accountSubPool) sweepRetiredRelaysLocked() {
	if len(sp.retiredRelays) == 0 {
		return
	}
	kept := sp.retiredRelays[:0]
	for _, r := range sp.retiredRelays {
		inUse := false
		for _, w := range sp.workers {
			if w.egressRelay == r && w.GetState() != StateDead {
				inUse = true
				break
			}
		}
		if inUse {
			kept = append(kept, r)
		} else {
			_ = r.Close()
		}
	}
	sp.retiredRelays = kept
}

type MultiAccountPool struct {
	mu               sync.Mutex
	discoverMu       sync.Mutex
	dataDir          string
	poolCfg          config.ACPPoolConfig
	sandboxCfg       config.SandboxPolicyConfig
	wiretap          WiretapCallback
	subPools         map[string]*accountSubPool
	workerSeq        atomic.Int64
	rrCounter        atomic.Uint64
	discoveredModels []ModelInfo
	modelsCachedAt   time.Time
	modelsCacheTTL   time.Duration
	stopCh           chan struct{}
	closeOnce        sync.Once
}

func NewMultiAccountPool(dataDir string, poolCfg config.ACPPoolConfig, sandboxCfg config.SandboxPolicyConfig, accounts []storage.Account, wiretap WiretapCallback) (*MultiAccountPool, error) {
	if dataDir == "" {
		dataDir = "./data"
	}
	mp := &MultiAccountPool{
		dataDir:        dataDir,
		poolCfg:        poolCfg,
		sandboxCfg:     sandboxCfg,
		wiretap:        wiretap,
		subPools:       make(map[string]*accountSubPool),
		modelsCacheTTL: DefaultModelsCacheTTL,
		stopCh:         make(chan struct{}),
	}

	if len(accounts) == 0 {
		accounts = []storage.Account{
			{
				ID:          "default",
				Name:        "Default Host Account",
				AuthMode:    "oauth_personal",
				NetworkMode: "direct",
				MaxWorkers:  poolCfg.MaxWorkers,
				Weight:      1,
				Enabled:     true,
				Status:      "ready",
			},
		}
	}

	for _, acc := range accounts {
		if err := mp.UpsertAccountPool(acc); err != nil {
			return nil, err
		}
	}
	go mp.idleGCLoop()
	return mp, nil
}

func (mp *MultiAccountPool) UpdateSandboxPolicy(sandboxCfg config.SandboxPolicyConfig) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if sandboxCfg.SessionMode != "" {
		mp.sandboxCfg.SessionMode = sandboxCfg.SessionMode
	}
	if sandboxCfg.InternalApproval != "" {
		mp.sandboxCfg.InternalApproval = sandboxCfg.InternalApproval
		for _, sp := range mp.subPools {
			for _, w := range sp.workers {
				w.SetInternalApproval(sandboxCfg.InternalApproval)
			}
		}
	}
}

func (mp *MultiAccountPool) idleGCLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			mp.reapIdleWorkers()
		case <-mp.stopCh:
			return
		}
	}
}

func (mp *MultiAccountPool) reapIdleWorkers() {
	mp.mu.Lock()
	maxIdleSec := mp.poolCfg.MaxIdleTimeSec
	minIdle := mp.poolCfg.MinIdleWorkers
	maxIdleDur := time.Duration(maxIdleSec) * time.Second
	now := time.Now()
	var toClose []*WorkerClient

	for _, sp := range mp.subPools {
		idleCount := 0
		for id, w := range sp.workers {
			st := w.GetState()
			if st == StateDead || (st == StateIdle && w.poolGen != sp.gen && w.TryMarkBusy()) {
				delete(sp.workers, id)
				toClose = append(toClose, w)
				continue
			}
			if st == StateIdle {
				idleCount++
			}
		}
		sp.sweepRetiredRelaysLocked()
		if maxIdleSec <= 0 {
			continue
		}
		// First pass: reap unbound idle workers that exceeded maxIdleDur
		for id, w := range sp.workers {
			if idleCount <= minIdle {
				break
			}
			if w.GetState() == StateIdle && w.BoundSessionCount() == 0 && now.Sub(w.LastActiveAt()) > maxIdleDur && w.TryMarkBusy() {
				delete(sp.workers, id)
				toClose = append(toClose, w)
				idleCount--
			}
		}
		// Second pass: only reap session-bound idle workers if they have been idle longer than session TTL (30m)
		boundIdleDur := maxIdleDur
		if boundIdleDur < 30*time.Minute {
			boundIdleDur = 30 * time.Minute
		}
		for id, w := range sp.workers {
			if idleCount <= minIdle {
				break
			}
			if w.GetState() == StateIdle && w.BoundSessionCount() > 0 && now.Sub(w.LastActiveAt()) > boundIdleDur && w.TryMarkBusy() {
				delete(sp.workers, id)
				toClose = append(toClose, w)
				idleCount--
			}
		}
	}
	mp.mu.Unlock()

	for _, w := range toClose {
		_ = w.Close()
	}
}

func (mp *MultiAccountPool) UpsertAccountPool(acc storage.Account) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	existing, hadExisting := mp.subPools[acc.ID]

	// Build the new relay before touching the live one so a bad proxy config leaves
	// the account running on its previous settings. Proxy/bind_ip egress applies
	// whether or not credential isolation is enabled.
	var relay *EgressRelay
	reuseRelay := hadExisting && existing.relay != nil && sameEgressConfig(existing.account, acc)
	if reuseRelay {
		relay = existing.relay
	} else if acc.NetworkMode == "proxy" || acc.NetworkMode == "bind_ip" {
		r, err := NewEgressRelay(acc.ID, acc.NetworkMode, acc.ProxyURL, acc.BindIP)
		if err != nil {
			return fmt.Errorf("init egress relay for account %s: %w", acc.ID, err)
		}
		relay = r
	}

	if hadExisting {
		if existing.relay != nil && !reuseRelay {
			existing.retiredRelays = append(existing.retiredRelays, existing.relay)
		}
		for id, w := range existing.workers {
			if w.TryMarkBusy() {
				delete(existing.workers, id)
				go func(w *WorkerClient) { _ = w.Close() }(w)
			}
		}
	}

	absData, err := filepath.Abs(mp.dataDir)
	if err != nil {
		absData = mp.dataDir
	}
	geminiHome := filepath.Join(absData, "accounts", acc.ID, ".gemini")
	_ = os.MkdirAll(filepath.Join(geminiHome, "config", "skills"), 0700)
	_ = os.MkdirAll(filepath.Join(geminiHome, "skills"), 0700)
	if acc.IsolationEnabled {
		_ = os.MkdirAll(filepath.Join(geminiHome, "antigravity-acp"), 0700)
		_ = os.MkdirAll(filepath.Join(geminiHome, "antigravity-cli"), 0700)
		acpTok := filepath.Join(geminiHome, "antigravity-acp", "acp_token.json")
		cliTok := filepath.Join(geminiHome, "antigravity-cli", "acp_token.json")
		if _, err := os.Stat(acpTok); err == nil {
			if _, err2 := os.Stat(cliTok); os.IsNotExist(err2) {
				if raw, rerr := os.ReadFile(acpTok); rerr == nil {
					_ = os.WriteFile(cliTok, raw, 0600)
				}
			}
		} else if _, err := os.Stat(cliTok); err == nil {
			if raw, rerr := os.ReadFile(cliTok); rerr == nil {
				_ = os.WriteFile(acpTok, raw, 0600)
			}
		}
	} else if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		hostGemini := filepath.Join(userHome, ".gemini")
		for _, authEntry := range []string{"antigravity-acp", "antigravity-cli", "oauth_creds.json", "google_accounts.json", "installation_id", "state.json"} {
			src := filepath.Join(hostGemini, authEntry)
			dst := filepath.Join(geminiHome, authEntry)
			if _, err := os.Stat(src); err == nil {
				if fi, lerr := os.Lstat(dst); lerr == nil && fi.IsDir() && (fi.Mode()&os.ModeSymlink == 0) {
					_ = os.RemoveAll(dst)
				}
				_ = os.Symlink(src, dst)
			}
		}
	}

	sp, exists := mp.subPools[acc.ID]
	if !exists {
		sp = &accountSubPool{
			workers:     make(map[string]*WorkerClient),
			loginStatus: TerminalLoginStatus{AccountID: acc.ID, State: "idle"},
		}
		mp.subPools[acc.ID] = sp
	}
	sp.account = acc
	sp.geminiHome = geminiHome
	sp.relay = relay
	sp.gen++
	sp.sweepRetiredRelaysLocked()
	return nil
}

func (mp *MultiAccountPool) RemoveAccountPool(accountID string) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	sp, ok := mp.subPools[accountID]
	if !ok {
		return nil
	}
	if sp.loginCmd != nil && sp.loginCmd.Process != nil {
		_ = sp.loginCmd.Process.Kill()
	}
	for _, w := range sp.workers {
		_ = w.Close()
	}
	if sp.relay != nil {
		_ = sp.relay.Close()
	}
	for _, r := range sp.retiredRelays {
		_ = r.Close()
	}
	delete(mp.subPools, accountID)
	return nil
}

func (mp *MultiAccountPool) SelectHealthyAccount(preferredAccountID string, allowFailover bool) (AccountRuntimeSnapshot, bool) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	candidates := mp.selectCandidateAccountsLocked(preferredAccountID, allowFailover)
	if len(candidates) == 0 {
		return AccountRuntimeSnapshot{}, false
	}
	targetID := candidates[0]
	if sp, ok := mp.subPools[targetID]; ok {
		return mp.buildAccountSnapshotLocked(sp), true
	}
	return AccountRuntimeSnapshot{}, false
}

func (mp *MultiAccountPool) AcquireForAccount(ctx context.Context, preferredAccountID string, allowFailover bool) (*WorkerClient, storage.Account, error) {
	return mp.AcquireForAccountForModel(ctx, preferredAccountID, allowFailover, "")
}

func (mp *MultiAccountPool) AcquireForAccountForModel(ctx context.Context, preferredAccountID string, allowFailover bool, modelID string) (*WorkerClient, storage.Account, error) {
	mp.mu.Lock()
	if modelID != "" && preferredAccountID != "" && preferredAccountID != "auto" && !allowFailover {
		if sp, ok := mp.subPools[preferredAccountID]; ok && sp.account.Enabled && sp.account.IsModelDisabled(modelID) {
			mp.mu.Unlock()
			return nil, sp.account, fmt.Errorf("model %q is disabled on account %q", modelID, preferredAccountID)
		}
	}
	candidates := mp.selectCandidateAccountsForModelLocked(preferredAccountID, allowFailover, modelID)
	mp.mu.Unlock()

	if len(candidates) == 0 {
		if modelID != "" {
			return nil, storage.Account{}, fmt.Errorf("model %q is disabled or unavailable on target ACP account(s)", modelID)
		}
		return nil, storage.Account{}, fmt.Errorf("no available ACP accounts for %q", preferredAccountID)
	}

	var lastErr error
	for _, accID := range candidates {
		w, acc, err := mp.acquireFromSpecificAccount(ctx, accID)
		if err == nil {
			return w, acc, nil
		}
		lastErr = err
		if IsAuthRequiredError(err) {
			mp.MarkAccountAuthRequired(accID)
		}
	}
	return nil, storage.Account{}, lastErr
}

func (mp *MultiAccountPool) selectCandidateAccountsLocked(preferred string, allowFailover bool) []string {
	return mp.selectCandidateAccountsForModelLocked(preferred, allowFailover, "")
}

func (mp *MultiAccountPool) selectCandidateAccountsForModelLocked(preferred string, allowFailover bool, modelID string) []string {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" && preferred != "auto" {
		if sp, ok := mp.subPools[preferred]; ok && sp.account.Enabled {
			if modelID != "" && sp.account.IsModelDisabled(modelID) {
				if !allowFailover {
					return nil
				}
			} else if !allowFailover {
				return []string{preferred}
			}
		} else if !allowFailover {
			return nil
		}
	}

	type scored struct {
		id     string
		busy   int
		weight int
	}
	var healthy []scored
	var fallback []string

	for id, sp := range mp.subPools {
		if !sp.account.Enabled {
			continue
		}
		if modelID != "" && sp.account.IsModelDisabled(modelID) {
			continue
		}
		busy := 0
		for _, w := range sp.workers {
			st := w.GetState()
			if st == StateGenerating || st == StateParked {
				busy++
			}
		}
		if sp.account.Status == "auth_required" {
			fallback = append(fallback, id)
			continue
		}
		w := sp.account.Weight
		if w <= 0 {
			w = 1
		}
		healthy = append(healthy, scored{id: id, busy: busy, weight: w})
	}

	sort.Slice(healthy, func(i, j int) bool {
		lhs := healthy[i].busy * healthy[j].weight
		rhs := healthy[j].busy * healthy[i].weight
		if lhs != rhs {
			return lhs < rhs
		}
		if healthy[i].weight != healthy[j].weight {
			return healthy[i].weight > healthy[j].weight
		}
		return healthy[i].id < healthy[j].id
	})

	if len(healthy) > 1 {
		idx := int(mp.rrCounter.Add(1)-1) % len(healthy)
		// If top candidates have identical weighted load, rotate among them
		if healthy[idx].busy*healthy[0].weight == healthy[0].busy*healthy[idx].weight {
			healthy[0], healthy[idx] = healthy[idx], healthy[0]
		}
	}

	var out []string
	if preferred != "" && preferred != "auto" {
		if sp, ok := mp.subPools[preferred]; ok && sp.account.Enabled && (modelID == "" || !sp.account.IsModelDisabled(modelID)) {
			out = append(out, preferred)
		}
	}
	for _, h := range healthy {
		if h.id != preferred {
			out = append(out, h.id)
		}
	}
	for _, f := range fallback {
		if f != preferred {
			out = append(out, f)
		}
	}
	return out
}

func (mp *MultiAccountPool) acquireFromSpecificAccount(ctx context.Context, accountID string) (*WorkerClient, storage.Account, error) {
	mp.mu.Lock()
	sp, ok := mp.subPools[accountID]
	if !ok {
		mp.mu.Unlock()
		return nil, storage.Account{}, fmt.Errorf("account %q not found", accountID)
	}

	// Clean dead workers and prefer an unbound idle worker so new requests do not steal
	// a worker process that already holds a sticky session in memory.
	var idle []*WorkerClient
	for id, w := range sp.workers {
		st := w.GetState()
		if st == StateDead {
			delete(sp.workers, id)
			go func(w *WorkerClient) { _ = w.Close() }(w)
			continue
		}
		if st == StateIdle && w.poolGen == sp.gen {
			idle = append(idle, w)
		}
	}
	sort.Slice(idle, func(i, j int) bool {
		bi, bj := idle[i].BoundSessionCount(), idle[j].BoundSessionCount()
		if bi != bj {
			return bi < bj
		}
		return idle[i].LastActiveAt().Before(idle[j].LastActiveAt())
	})
	for _, cand := range idle {
		// Session-affinity reuse in the handler claims workers without holding mp.mu,
		// so the Idle->Generating transition must be atomic.
		if !cand.TryMarkBusy() {
			continue
		}
		acc := sp.account
		wt := mp.wiretap
		mp.mu.Unlock()
		if wt != nil {
			wt(cand.ID, "STATE", string(StateGenerating))
		}
		return cand, acc, nil
	}

	maxW := sp.account.MaxWorkers
	if maxW <= 0 {
		maxW = mp.poolCfg.MaxWorkers
	}
	if maxW > 0 && len(sp.workers)+sp.pending >= maxW {
		acc := sp.account
		mp.mu.Unlock()
		return nil, acc, fmt.Errorf("account %q reached max_workers limit (%d)", accountID, maxW)
	}
	sp.pending++
	reserved := true
	defer func() {
		if reserved {
			mp.mu.Lock()
			sp.pending--
			mp.mu.Unlock()
		}
	}()

	acc := sp.account
	geminiHome := sp.geminiHome
	relay := sp.relay
	gen := sp.gen
	var relayURL string
	if relay != nil {
		relayURL = relay.ListenURL()
	}
	workerID := fmt.Sprintf("w_%s_%d", acc.ID, mp.workerSeq.Add(1))
	poolCfg := mp.poolCfg
	sandboxCfg := mp.sandboxCfg
	wiretap := mp.wiretap
	mp.mu.Unlock()

	env := BuildIsolatedEnv(poolCfg.Env, acc, geminiHome, relayURL)
	netns := accountNetNS(acc)

	accountWorkDir := EnsureAccountWorkspace(poolCfg.WorkspaceDir, acc.ID)

	w, err := NewWorkerClient(ctx, workerID, WorkerLaunchOptions{
		AccountID:           acc.ID,
		BackendType:         poolCfg.BackendType,
		BinaryPath:          ResolveACPBinaryPath(poolCfg.BackendType, poolCfg.BinaryPath),
		WorkspaceDir:        accountWorkDir,
		AdditionalDirs:      poolCfg.AdditionalDirs,
		AgentName:           poolCfg.AgentName,
		ProcessPerSession:   poolCfg.ProcessPerSession,
		SkipPermissionsFlag: sandboxCfg.SkipPermissionsFlag,
		EnableSandbox:       sandboxCfg.EnableSandbox,
		DisableTelemetry:    poolCfg.DisableTelemetry,
		LogFile:             poolCfg.LogFile,
		IDEMCPPort:          poolCfg.IDEMCPPort,
		ExtraArgs:           poolCfg.ExtraArgs,
		Env:                 env,
		NetNSName:           netns,
		InternalApproval:    sandboxCfg.InternalApproval,
	}, wiretap)
	if err != nil {
		return nil, acc, err
	}

	if _, err := w.Initialize(ctx, poolCfg.ClientName, poolCfg.ClientVersion); err != nil {
		_ = w.Close()
		return nil, acc, err
	}

	w.SetState(StateGenerating)
	w.poolGen = gen
	w.egressRelay = relay
	mp.mu.Lock()
	sp.pending--
	reserved = false
	if spNow, exists := mp.subPools[accountID]; exists && spNow == sp {
		spNow.workers[w.ID] = w
	}
	mp.mu.Unlock()
	if wiretap != nil {
		wiretap(w.ID, "STATE", string(StateGenerating))
	}

	return w, acc, nil
}

func (mp *MultiAccountPool) Release(w *WorkerClient) {
	if w == nil {
		return
	}
	mp.mu.Lock()
	sp, exists := mp.subPools[w.AccountID]
	tracked := exists && sp.workers[w.ID] == w
	// Workers started under an older account config (egress, credentials) are retired.
	retire := w.GetState() == StateDead || !tracked || w.poolGen != sp.gen
	if retire {
		if tracked {
			delete(sp.workers, w.ID)
		}
		w.SetState(StateDead)
	}
	wt := mp.wiretap
	mp.mu.Unlock()
	if retire {
		_ = w.Close()
		if exists {
			mp.mu.Lock()
			sp.sweepRetiredRelaysLocked()
			mp.mu.Unlock()
		}
		if wt != nil {
			wt(w.ID, "STATE", string(StateDead))
		}
		return
	}
	w.DrainEvents()
	w.SetState(StateIdle)
	if wt != nil {
		wt(w.ID, "STATE", string(StateIdle))
	}
}

func (mp *MultiAccountPool) KillWorker(workerID string) bool {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	for _, sp := range mp.subPools {
		if w, ok := sp.workers[workerID]; ok {
			_ = w.Close()
			delete(sp.workers, workerID)
			return true
		}
	}
	return false
}

func (mp *MultiAccountPool) RestartIdleWorkers() int {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	killed := 0
	for _, sp := range mp.subPools {
		for id, w := range sp.workers {
			if w.TryMarkBusy() {
				delete(sp.workers, id)
				go func(w *WorkerClient) { _ = w.Close() }(w)
				killed++
			}
		}
	}
	return killed
}

// UpdateACPBinary updates the pool's active ACP binary path, bumps subpool generations so
// in-flight workers retire gracefully on Release, closes idle workers immediately, and
// invalidates cached model discovery metadata.
func (mp *MultiAccountPool) UpdateACPBinary(backendType, binaryPath string) int {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if strings.TrimSpace(backendType) != "" {
		mp.poolCfg.BackendType = strings.TrimSpace(backendType)
	}
	if strings.TrimSpace(binaryPath) != "" {
		mp.poolCfg.BinaryPath = strings.TrimSpace(binaryPath)
	}
	mp.modelsCachedAt = time.Time{}
	retired := 0
	for _, sp := range mp.subPools {
		sp.gen++
		sp.modelsCachedAt = time.Time{}
		for id, w := range sp.workers {
			if w.TryMarkBusy() {
				delete(sp.workers, id)
				go func(w *WorkerClient) { _ = w.Close() }(w)
				retired++
			}
		}
	}
	return retired
}

func (mp *MultiAccountPool) MarkAccountAuthRequired(accountID string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if sp, ok := mp.subPools[accountID]; ok {
		sp.account.Status = "auth_required"
	}
}

func (mp *MultiAccountPool) MarkAccountReady(accountID string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if sp, ok := mp.subPools[accountID]; ok {
		sp.account.Status = "ready"
	}
}

func (mp *MultiAccountPool) SetAccountDisabledModels(accountID string, disabledModels []string) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if sp, ok := mp.subPools[accountID]; ok {
		copied := make([]string, len(disabledModels))
		copy(copied, disabledModels)
		sp.account.DisabledModels = copied
	}
}

func (mp *MultiAccountPool) IsModelDisabledForAccount(accountID, modelID string) bool {
	if strings.TrimSpace(modelID) == "" || strings.TrimSpace(accountID) == "" || accountID == "auto" {
		return false
	}
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if sp, ok := mp.subPools[accountID]; ok {
		return sp.account.IsModelDisabled(modelID)
	}
	return false
}

func (mp *MultiAccountPool) FilterAllowedModels(preferredAccountID string, models []ModelInfo) []ModelInfo {
	if len(models) == 0 {
		return []ModelInfo{}
	}
	mp.mu.Lock()
	defer mp.mu.Unlock()

	preferred := strings.TrimSpace(preferredAccountID)
	if preferred != "" && preferred != "auto" {
		sp, ok := mp.subPools[preferred]
		if !ok {
			return models
		}
		out := make([]ModelInfo, 0, len(models))
		for _, m := range models {
			if !sp.account.IsModelDisabled(m.ModelID) {
				out = append(out, m)
			}
		}
		return out
	}

	// For auto load-balancing, a model is available if at least one enabled account does NOT disable it.
	var enabledAccounts []storage.Account
	for _, sp := range mp.subPools {
		if sp.account.Enabled {
			enabledAccounts = append(enabledAccounts, sp.account)
		}
	}
	if len(enabledAccounts) == 0 {
		return models
	}

	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		allowedOnAtLeastOne := false
		for _, acc := range enabledAccounts {
			if !acc.IsModelDisabled(m.ModelID) {
				allowedOnAtLeastOne = true
				break
			}
		}
		if allowedOnAtLeastOne {
			out = append(out, m)
		}
	}
	return out
}

func (mp *MultiAccountPool) SetModelsCacheTTL(ttl time.Duration) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if ttl <= 0 {
		ttl = DefaultModelsCacheTTL
	}
	mp.modelsCacheTTL = ttl
}

func (mp *MultiAccountPool) UpdateDiscoveredModels(models []ModelInfo) {
	mp.UpdateAccountDiscoveredModels("", models)
}

func (mp *MultiAccountPool) UpdateAccountDiscoveredModels(accountID string, models []ModelInfo) {
	if len(models) == 0 {
		return
	}
	now := time.Now()
	mp.mu.Lock()
	defer mp.mu.Unlock()
	copied := make([]ModelInfo, len(models))
	copy(copied, models)
	mp.discoveredModels = copied
	mp.modelsCachedAt = now

	if accountID != "" {
		if sp, ok := mp.subPools[accountID]; ok {
			accCopy := make([]ModelInfo, len(models))
			copy(accCopy, models)
			sp.discoveredModels = accCopy
			sp.modelsCachedAt = now
		}
	}
}

func (mp *MultiAccountPool) ListModels() []ModelInfo {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	out := make([]ModelInfo, len(mp.discoveredModels))
	copy(out, mp.discoveredModels)
	return out
}

func (mp *MultiAccountPool) ModelsCacheInfo() ([]ModelInfo, time.Time, time.Duration) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	out := make([]ModelInfo, len(mp.discoveredModels))
	copy(out, mp.discoveredModels)
	return out, mp.modelsCachedAt, mp.modelsCacheTTL
}

// GetOrDiscoverModels returns the cached ACP models if within the cache TTL,
// or queries the ACP backend in real time via `session/new` to discover the
// available models and refreshes the cache.
func (mp *MultiAccountPool) GetOrDiscoverModels(ctx context.Context, preferredAccountID string, forceRefresh bool) ([]ModelInfo, time.Time, error) {
	preferred := strings.TrimSpace(preferredAccountID)
	specificAccount := preferred != "" && preferred != "auto"

	mp.mu.Lock()
	ttl := mp.modelsCacheTTL
	if ttl <= 0 {
		ttl = DefaultModelsCacheTTL
	}
	if !forceRefresh {
		if specificAccount {
			if sp, ok := mp.subPools[preferred]; ok && len(sp.discoveredModels) > 0 && !sp.modelsCachedAt.IsZero() && time.Since(sp.modelsCachedAt) < ttl {
				out := make([]ModelInfo, len(sp.discoveredModels))
				copy(out, sp.discoveredModels)
				cachedAt := sp.modelsCachedAt
				mp.mu.Unlock()
				return out, cachedAt, nil
			}
		}
		if len(mp.discoveredModels) > 0 && !mp.modelsCachedAt.IsZero() && time.Since(mp.modelsCachedAt) < ttl {
			out := make([]ModelInfo, len(mp.discoveredModels))
			copy(out, mp.discoveredModels)
			cachedAt := mp.modelsCachedAt
			mp.mu.Unlock()
			return out, cachedAt, nil
		}
	}
	mp.mu.Unlock()

	mp.discoverMu.Lock()
	defer mp.discoverMu.Unlock()

	mp.mu.Lock()
	ttl = mp.modelsCacheTTL
	if ttl <= 0 {
		ttl = DefaultModelsCacheTTL
	}
	if !forceRefresh {
		if specificAccount {
			if sp, ok := mp.subPools[preferred]; ok && len(sp.discoveredModels) > 0 && !sp.modelsCachedAt.IsZero() && time.Since(sp.modelsCachedAt) < ttl {
				out := make([]ModelInfo, len(sp.discoveredModels))
				copy(out, sp.discoveredModels)
				cachedAt := sp.modelsCachedAt
				mp.mu.Unlock()
				return out, cachedAt, nil
			}
		}
		if len(mp.discoveredModels) > 0 && !mp.modelsCachedAt.IsZero() && time.Since(mp.modelsCachedAt) < ttl {
			out := make([]ModelInfo, len(mp.discoveredModels))
			copy(out, mp.discoveredModels)
			cachedAt := mp.modelsCachedAt
			mp.mu.Unlock()
			return out, cachedAt, nil
		}
	}
	staleModels := make([]ModelInfo, len(mp.discoveredModels))
	copy(staleModels, mp.discoveredModels)
	staleCachedAt := mp.modelsCachedAt
	if specificAccount {
		if sp, ok := mp.subPools[preferred]; ok && len(sp.discoveredModels) > 0 {
			staleModels = make([]ModelInfo, len(sp.discoveredModels))
			copy(staleModels, sp.discoveredModels)
			staleCachedAt = sp.modelsCachedAt
		}
	}
	workDir := mp.poolCfg.WorkspaceDir
	mp.mu.Unlock()

	dCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		dCtx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}

	var (
		worker *WorkerClient
		acc    storage.Account
		err    error
	)
	if specificAccount {
		worker, acc, err = mp.acquireFromSpecificAccount(dCtx, preferred)
	} else {
		worker, acc, err = mp.AcquireForAccount(dCtx, preferred, true)
	}
	if err != nil {
		return staleModels, staleCachedAt, err
	}
	defer mp.Release(worker)

	workDir = EnsureAccountWorkspace(workDir, acc.ID)

	worker.DrainEvents()
	sessRes, err := worker.NewSession(dCtx, NewSessionParams{
		Cwd:        workDir,
		MCPServers: []MCPServerEndpoint{},
	})
	if err != nil {
		if IsAuthRequiredError(err) {
			mp.MarkAccountAuthRequired(acc.ID)
		}
		return staleModels, staleCachedAt, err
	}
	if sessRes != nil && sessRes.SessionID != "" {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		_ = worker.CloseSession(closeCtx, sessRes.SessionID)
		cancelClose()
	}

	discovered := ExtractModelsFromSessionResult(sessRes)
	if len(discovered) > 0 {
		mp.UpdateAccountDiscoveredModels(acc.ID, discovered)
		models, cachedAt, _ := mp.ModelsCacheInfo()
		return models, cachedAt, nil
	}

	return staleModels, staleCachedAt, nil
}

func (mp *MultiAccountPool) ProbeAccountNetwork(ctx context.Context, accountID string, probeURL string) (string, error) {
	mp.mu.Lock()
	sp, ok := mp.subPools[accountID]
	if !ok {
		mp.mu.Unlock()
		return "", fmt.Errorf("account %q not found", accountID)
	}
	relay := sp.relay
	netns := accountNetNS(sp.account)
	mp.mu.Unlock()

	var ip string
	var err error
	if netns != "" {
		ip, err = probeInNetNS(ctx, netns, probeURL)
	} else {
		ip, err = relay.ProbeTargetURL(ctx, probeURL)
	}
	if err != nil {
		return "", err
	}
	mp.mu.Lock()
	if spNow, exists := mp.subPools[accountID]; exists {
		spNow.account.LastEgressIP = ip
	}
	mp.mu.Unlock()
	return ip, nil
}

// probeInNetNS measures egress from inside a network namespace, where the
// gateway's own sockets would not reflect the account's route.
func probeInNetNS(ctx context.Context, netns, targetURL string) (string, error) {
	if strings.TrimSpace(targetURL) == "" {
		targetURL = "https://api.ipify.org"
	}
	if err := ValidateProbeURL(targetURL); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", netns, "curl", "-fsS", "--max-time", "10", "--max-redirs", "0", "--", targetURL)
	cmd.Env = SanitizedParentEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("egress probe in netns %q failed: %w", netns, err)
	}
	ip := strings.TrimSpace(string(out))
	if len(ip) > 256 {
		ip = ip[:256]
	}
	return ip, nil
}

var urlRegex = regexp.MustCompile(`https://[^\s"']+`)

func (mp *MultiAccountPool) StartTerminalLogin(accountID string) (TerminalLoginStatus, error) {
	if accountID == "" {
		accountID = "default"
	}
	mp.mu.Lock()
	sp, ok := mp.subPools[accountID]
	if !ok {
		mp.mu.Unlock()
		return TerminalLoginStatus{}, fmt.Errorf("account %q not found", accountID)
	}
	if sp.loginCmd != nil && sp.loginCmd.Process != nil {
		_ = sp.loginCmd.Process.Kill()
	}

	var relayURL string
	if sp.relay != nil {
		relayURL = sp.relay.ListenURL()
	}
	env := BuildIsolatedEnv(mp.poolCfg.Env, sp.account, sp.geminiHome, relayURL)
	bin, args := BuildCommandAndArgs(WorkerLaunchOptions{
		AccountID:   accountID,
		BackendType: mp.poolCfg.BackendType,
		BinaryPath:  mp.poolCfg.BinaryPath,
		ExtraArgs:   []string{"--login"},
		NetNSName:   accountNetNS(sp.account),
	})
	cmd := exec.Command(bin, args...)
	envList := SanitizedParentEnv()
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	cmd.Env = envList

	stdin, err := cmd.StdinPipe()
	if err != nil {
		mp.mu.Unlock()
		return TerminalLoginStatus{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		mp.mu.Unlock()
		return TerminalLoginStatus{}, err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		mp.mu.Unlock()
		return TerminalLoginStatus{}, err
	}

	loginDoneCh := make(chan struct{})
	sp.loginCmd = cmd
	sp.loginStdin = stdin
	sp.loginDoneCh = loginDoneCh
	sp.loginStatus = TerminalLoginStatus{
		AccountID: accountID,
		State:     "waiting_code",
		Output:    "Starting agy acp --login...\n",
	}
	urlReadyCh := make(chan struct{}, 1)
	mp.mu.Unlock()

	go func() {
		defer close(loginDoneCh)
		scanner := bufio.NewScanner(stdout)
		// A newer login attempt replaces sp.loginCmd; this goroutine must then stop
		// writing into the shared status.
		for scanner.Scan() {
			line := scanner.Text()
			mp.mu.Lock()
			if sp.loginCmd == cmd {
				if len(sp.loginStatus.Output) < 32*1024 {
					sp.loginStatus.Output += line + "\n"
				}
				if sp.loginStatus.AuthURL == "" {
					if match := urlRegex.FindString(line); match != "" {
						sp.loginStatus.AuthURL = match
						select {
						case urlReadyCh <- struct{}{}:
						default:
						}
					}
				}
			}
			mp.mu.Unlock()
		}
		err := cmd.Wait()
		mp.mu.Lock()
		if sp.loginCmd == cmd {
			if err == nil {
				sp.loginStatus.State = "success"
				sp.account.Status = "ready"
			} else if sp.loginStatus.State != "success" {
				sp.loginStatus.State = "error"
			}
			sp.loginStdin = nil
		}
		mp.mu.Unlock()
	}()

	select {
	case <-urlReadyCh:
	case <-loginDoneCh:
	case <-time.After(1500 * time.Millisecond):
	}
	return mp.GetTerminalLoginStatus(accountID), nil
}

func (mp *MultiAccountPool) SubmitTerminalLoginCode(accountID, code string) (TerminalLoginStatus, error) {
	if accountID == "" {
		accountID = "default"
	}
	mp.mu.Lock()
	sp, ok := mp.subPools[accountID]
	if !ok || sp.loginStdin == nil {
		mp.mu.Unlock()
		return TerminalLoginStatus{}, fmt.Errorf("no active login session for account %q", accountID)
	}
	stdin := sp.loginStdin
	doneCh := sp.loginDoneCh
	mp.mu.Unlock()

	if _, err := stdin.Write([]byte(strings.TrimSpace(code) + "\n")); err != nil {
		return TerminalLoginStatus{}, err
	}
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(3 * time.Second):
		}
	} else {
		time.Sleep(500 * time.Millisecond)
	}
	return mp.GetTerminalLoginStatus(accountID), nil
}

func (mp *MultiAccountPool) GetTerminalLoginStatus(accountID string) TerminalLoginStatus {
	if accountID == "" {
		accountID = "default"
	}
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if sp, ok := mp.subPools[accountID]; ok {
		return sp.loginStatus
	}
	return TerminalLoginStatus{AccountID: accountID, State: "idle"}
}

func (mp *MultiAccountPool) Snapshot() []WorkerSnapshot {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	var list []WorkerSnapshot
	for _, sp := range mp.subPools {
		for id, w := range sp.workers {
			st := w.GetState()
			if st == StateDead {
				delete(sp.workers, id)
				go func(w *WorkerClient) { _ = w.Close() }(w)
				continue
			}
			list = append(list, WorkerSnapshot{
				ID:           w.ID,
				AccountID:    w.AccountID,
				PID:          w.PID,
				State:        st,
				LastActiveAt: w.LastActiveAt(),
			})
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].ID < list[j].ID
	})
	return list
}

func (mp *MultiAccountPool) buildAccountSnapshotLocked(sp *accountSubPool) AccountRuntimeSnapshot {
	busy := 0
	for id, w := range sp.workers {
		st := w.GetState()
		if st == StateDead {
			delete(sp.workers, id)
			go func(w *WorkerClient) { _ = w.Close() }(w)
			continue
		}
		if st == StateGenerating || st == StateParked {
			busy++
		}
	}
	var relayListen string
	if sp.relay != nil {
		relayListen = sp.relay.ListenURL()
	}
	var accModels []ModelInfo
	var accCachedAt time.Time
	if len(sp.discoveredModels) > 0 {
		accModels = make([]ModelInfo, len(sp.discoveredModels))
		copy(accModels, sp.discoveredModels)
		accCachedAt = sp.modelsCachedAt
	} else if len(mp.discoveredModels) > 0 {
		accModels = make([]ModelInfo, len(mp.discoveredModels))
		copy(accModels, mp.discoveredModels)
		accCachedAt = mp.modelsCachedAt
	} else {
		accModels = []ModelInfo{}
	}
	var accCachedAtStr string
	if !accCachedAt.IsZero() {
		accCachedAtStr = accCachedAt.UTC().Format(time.RFC3339)
	}
	if sp.account.DisabledModels == nil {
		sp.account.DisabledModels = []string{}
	}
	return AccountRuntimeSnapshot{
		Account:        sp.account,
		GEMINIHome:     sp.geminiHome,
		RelayListen:    relayListen,
		WorkerCount:    len(sp.workers),
		BusyWorkers:    busy,
		LoginState:     sp.loginStatus.State,
		LoginAuthURL:   sp.loginStatus.AuthURL,
		Models:         accModels,
		ModelsCachedAt: accCachedAtStr,
	}
}

func (mp *MultiAccountPool) AccountSnapshots() []AccountRuntimeSnapshot {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	out := make([]AccountRuntimeSnapshot, 0, len(mp.subPools))
	for _, sp := range mp.subPools {
		out = append(out, mp.buildAccountSnapshotLocked(sp))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account.ID == "default" {
			return true
		}
		if out[j].Account.ID == "default" {
			return false
		}
		return out[i].Account.ID < out[j].Account.ID
	})
	return out
}

func (mp *MultiAccountPool) Close() error {
	mp.closeOnce.Do(func() {
		if mp.stopCh != nil {
			close(mp.stopCh)
		}
	})
	mp.mu.Lock()
	defer mp.mu.Unlock()
	for _, sp := range mp.subPools {
		if sp.loginCmd != nil && sp.loginCmd.Process != nil {
			_ = sp.loginCmd.Process.Kill()
		}
		for _, w := range sp.workers {
			_ = w.Close()
		}
		if sp.relay != nil {
			_ = sp.relay.Close()
		}
		for _, r := range sp.retiredRelays {
			_ = r.Close()
		}
	}
	return nil
}

var (
	pctRegex   = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)%\s*remaining`)
	resetRegex = regexp.MustCompile(`\(resets\s+([^)]+)\)`)
)

func translateGroupName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "gemini models":
		return "Gemini 模型"
	case "claude and gpt models", "claude / gpt models":
		return "Claude / GPT 模型"
	default:
		return strings.TrimSpace(raw)
	}
}

func translateBucketLabel(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(lower, "weekly"):
		return "每周"
	case strings.Contains(lower, "five hour") || strings.Contains(lower, "5h") || strings.Contains(lower, "5 hour"):
		return "5 小时"
	default:
		return strings.TrimSpace(raw)
	}
}

func formatResetCountdown(rawReset string) string {
	rawReset = strings.TrimSpace(rawReset)
	if rawReset == "" {
		return ""
	}
	layouts := []string{
		"Jan 02, 2006, 15:04 UTC",
		"Jan 2, 2006, 15:04 UTC",
		time.RFC3339,
	}
	var parsed time.Time
	var err error
	for _, layout := range layouts {
		parsed, err = time.Parse(layout, rawReset)
		if err == nil {
			break
		}
	}
	if err != nil {
		return rawReset + " 重置"
	}
	rem := time.Until(parsed)
	if rem <= 0 {
		return "即将重置"
	}
	if rem >= 24*time.Hour {
		totalHours := int(rem.Hours())
		days := totalHours / 24
		hours := totalHours % 24
		return fmt.Sprintf("%d 天 %d 小时后重置", days, hours)
	}
	totalMins := int(rem.Minutes())
	hours := totalMins / 60
	mins := totalMins % 60
	if hours > 0 {
		return fmt.Sprintf("%d 小时 %d 分后重置", hours, mins)
	}
	return fmt.Sprintf("%d 分后重置", mins)
}

func ParseACPUsageMarkdown(md string) ([]storage.ModelQuotaGroup, string) {
	if groups, ok := parseCLIUsageTSV(md); ok {
		return groups, "额度读取正常"
	}

	idx := strings.Index(md, "### Quota Limits")
	if idx == -1 {
		trimmed := strings.TrimSpace(md)
		if strings.Contains(strings.ToLower(trimmed), "authentication required") ||
			strings.Contains(strings.ToLower(trimmed), "not logged in") ||
			strings.Contains(strings.ToLower(trimmed), "please log in") {
			return nil, "账户待登录 (Authentication required)"
		}
		return nil, "未返回 Quota Limits 段落"
	}
	section := md[idx+len("### Quota Limits"):]
	lines := strings.Split(section, "\n")

	var groups []storage.ModelQuotaGroup
	var currentGroup *storage.ModelQuotaGroup

	for _, rawLine := range lines {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" || strings.HasPrefix(trimmed, "*(") {
			continue
		}
		if strings.HasPrefix(trimmed, "### ") {
			break
		}
		if strings.HasPrefix(trimmed, "- Unable to retrieve quota status") ||
			strings.HasPrefix(trimmed, "- Not applicable") ||
			strings.HasPrefix(trimmed, "- No quota limits") ||
			strings.HasPrefix(trimmed, "- data not available") {
			return nil, strings.TrimPrefix(trimmed, "- ")
		}

		// Group header: - **Gemini Models**
		if strings.HasPrefix(trimmed, "- **") && strings.HasSuffix(trimmed, "**") && !strings.Contains(trimmed, ":**") {
			name := strings.TrimSuffix(strings.TrimPrefix(trimmed, "- **"), "**")
			groups = append(groups, storage.ModelQuotaGroup{
				GroupName: translateGroupName(name),
				Windows:   []storage.QuotaWindow{},
			})
			currentGroup = &groups[len(groups)-1]
			continue
		}

		// Bucket line: - **Weekly Limit Remaining:** 84% remaining (resets Nov 14, 2026, 00:12 UTC)
		if strings.HasPrefix(trimmed, "- **") && strings.Contains(trimmed, ":**") && currentGroup != nil {
			parts := strings.SplitN(strings.TrimPrefix(trimmed, "- **"), ":**", 2)
			if len(parts) != 2 {
				continue
			}
			bucketName := parts[0]
			rest := strings.TrimSpace(parts[1])

			var pct float64 = 100
			if m := pctRegex.FindStringSubmatch(rest); len(m) == 2 {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil {
					pct = v
				}
			}
			var resetText string
			if m := resetRegex.FindStringSubmatch(rest); len(m) == 2 {
				resetText = formatResetCountdown(m[1])
			}

			currentGroup.Windows = append(currentGroup.Windows, storage.QuotaWindow{
				Label:        translateBucketLabel(bucketName),
				RemainingPct: pct,
				ResetText:    resetText,
			})
		}
	}

	if len(groups) == 0 {
		return nil, "未解析到有效额度分组"
	}
	return groups, "额度读取正常"
}

// parseCLIUsageTSV parses the tab-separated output produced by `agy -p "/usage"`:
// Gemini Models\tWeekly Limit Remaining\t65%\t2026-11-14T00:12:59Z
func parseCLIUsageTSV(raw string) ([]storage.ModelQuotaGroup, bool) {
	lines := strings.Split(raw, "\n")
	var groups []storage.ModelQuotaGroup
	groupIndex := make(map[string]int)

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || !strings.Contains(line, "\t") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 3 {
			continue
		}
		rawGroup := strings.TrimSpace(cols[0])
		rawBucket := strings.TrimSpace(cols[1])
		rawPct := strings.TrimSuffix(strings.TrimSpace(cols[2]), "%")
		pct, err := strconv.ParseFloat(rawPct, 64)
		if err != nil || rawGroup == "" || rawBucket == "" {
			continue
		}
		var resetText string
		if len(cols) >= 4 {
			resetText = formatResetCountdown(strings.TrimSpace(cols[3]))
		}
		translatedGroup := translateGroupName(rawGroup)
		idx, exists := groupIndex[translatedGroup]
		if !exists {
			idx = len(groups)
			groupIndex[translatedGroup] = idx
			groups = append(groups, storage.ModelQuotaGroup{
				GroupName: translatedGroup,
				Windows:   []storage.QuotaWindow{},
			})
		}
		groups[idx].Windows = append(groups[idx].Windows, storage.QuotaWindow{
			Label:        translateBucketLabel(rawBucket),
			RemainingPct: pct,
			ResetText:    resetText,
		})
	}
	return groups, len(groups) > 0
}

func (mp *MultiAccountPool) tryFetchQuotaViaCLIPrint(ctx context.Context, accountID string) ([]storage.ModelQuotaGroup, string, bool, error) {
	if accountID == "" {
		accountID = "default"
	}
	mp.mu.Lock()
	sp, ok := mp.subPools[accountID]
	if !ok {
		mp.mu.Unlock()
		return nil, "", false, nil
	}
	resolvedBin := ResolveACPBinaryPath(mp.poolCfg.BackendType, mp.poolCfg.BinaryPath)
	base := strings.ToLower(filepath.Base(resolvedBin))
	baseNoExt := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd"), ".bat")
	if baseNoExt != "agy" && baseNoExt != "antigravity" {
		mp.mu.Unlock()
		return nil, "", false, nil
	}
	var relayURL string
	if sp.relay != nil {
		relayURL = sp.relay.ListenURL()
	}
	env := BuildIsolatedEnv(mp.poolCfg.Env, sp.account, sp.geminiHome, relayURL)
	netNS := accountNetNS(sp.account)
	needModelDiscovery := len(sp.discoveredModels) == 0 && len(mp.discoveredModels) == 0
	mp.mu.Unlock()

	cmdBin := resolvedBin
	cmdArgs := []string{"-p", "/usage"}
	if netNS != "" {
		cmdBin = "ip"
		cmdArgs = []string{"netns", "exec", netNS, resolvedBin, "-p", "/usage"}
	}
	cmd := exec.CommandContext(ctx, cmdBin, cmdArgs...)
	envList := SanitizedParentEnv()
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}
	cmd.Env = envList

	outBytes, err := cmd.CombinedOutput()
	outStr := string(outBytes)
	if groups, ok := parseCLIUsageTSV(outStr); ok {
		if needModelDiscovery {
			go func(accID string) {
				bgCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				_, _, _ = mp.GetOrDiscoverModels(bgCtx, accID, false)
			}(accountID)
		}
		return groups, "额度读取正常", true, nil
	}
	lowerOut := strings.ToLower(outStr)
	if strings.Contains(lowerOut, "authentication required") ||
		strings.Contains(lowerOut, "not logged in") ||
		strings.Contains(lowerOut, "please log in") ||
		strings.Contains(lowerOut, "unauthenticated") {
		mp.MarkAccountAuthRequired(accountID)
		authErr := fmt.Errorf("authentication required: %s", strings.TrimSpace(outStr))
		return nil, "账户待登录 (Authentication required)", true, authErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, "读取 /usage 超时", true, ctx.Err()
		}
		return nil, "执行 agy -p /usage 失败: " + strings.TrimSpace(outStr), true, err
	}
	return nil, "", false, nil
}

func (mp *MultiAccountPool) FetchAccountQuotaViaACP(ctx context.Context, accountID string) ([]storage.ModelQuotaGroup, string, error) {
	if groups, msg, handled, err := mp.tryFetchQuotaViaCLIPrint(ctx, accountID); handled {
		return groups, msg, err
	}

	worker, _, err := mp.acquireFromSpecificAccount(ctx, accountID)
	if err != nil {
		if IsAuthRequiredError(err) {
			mp.MarkAccountAuthRequired(accountID)
			return nil, "账户待登录 (Authentication required)", err
		}
		return nil, "启动 ACP 进程失败: " + err.Error(), err
	}
	defer mp.Release(worker)

	workDir := EnsureAccountWorkspace(mp.poolCfg.WorkspaceDir, accountID)

	worker.DrainEvents()
	sessRes, err := worker.NewSession(ctx, NewSessionParams{
		Cwd:        workDir,
		MCPServers: []MCPServerEndpoint{},
	})
	if err != nil {
		if IsAuthRequiredError(err) {
			mp.MarkAccountAuthRequired(accountID)
			return nil, "账户待登录 (Authentication required)", err
		}
		return nil, "创建 ACP 会话失败: " + err.Error(), err
	}
	if discovered := ExtractModelsFromSessionResult(sessRes); len(discovered) > 0 {
		mp.UpdateAccountDiscoveredModels(accountID, discovered)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = worker.CloseSession(closeCtx, sessRes.SessionID)
	}()

	doneCh := make(chan error, 1)
	go func() {
		_, pErr := worker.Call(ctx, "session/prompt", PromptParams{
			SessionID: sessRes.SessionID,
			Prompt:    []PromptBlock{{Type: "text", Text: "/usage"}},
		})
		doneCh <- pErr
	}()

	var fullText strings.Builder
	for {
		select {
		case ev := <-worker.Events():
			if ev == nil || ev.Method != "session/update" {
				continue
			}
			var up SessionUpdateParams
			if json.Unmarshal(ev.Params, &up) == nil && up.Update.SessionUpdate == "agent_message_chunk" {
				fullText.WriteString(up.Update.Content.Text)
			}
		case pErr := <-doneCh:
		drainRemaining:
			for {
				select {
				case ev := <-worker.Events():
					if ev != nil && ev.Method == "session/update" {
						var up SessionUpdateParams
						if json.Unmarshal(ev.Params, &up) == nil && up.Update.SessionUpdate == "agent_message_chunk" {
							fullText.WriteString(up.Update.Content.Text)
						}
					}
				default:
					break drainRemaining
				}
			}
			if pErr != nil {
				return nil, "执行 /usage 失败: " + pErr.Error(), pErr
			}
			groups, statusMsg := ParseACPUsageMarkdown(fullText.String())
			return groups, statusMsg, nil
		case <-ctx.Done():
			return nil, "读取 /usage 超时", ctx.Err()
		}
	}
}
