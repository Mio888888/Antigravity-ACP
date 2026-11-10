package acp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

type ToolResultPayload struct {
	Content string
	IsError bool
}

type ParkedToolCall struct {
	CallID     string
	BridgeID   string
	AccountID  string
	ToolName   string
	ArgsJSON   string
	Dispatched bool
	ResultCh   chan ToolResultPayload
	CreatedAt  time.Time
}

type ManagedSession struct {
	mu                 sync.RWMutex
	BridgeID           string
	AccountID          string
	ExplicitKey        string
	HistoryHash        string
	explicitKeys       []string
	historyHashes      []string
	// recentHashes lists raw history hashes in the order they were last recorded.
	recentHashes []string
	ACPSessionID       string
	WorkspaceDir       string
	Worker             *WorkerClient
	Tools              []ToolDefinition
	SystemPrompt       string
	SessionTitle       string
	LastMode                  string
	LastModel                 string
	LastThinkingLevel         string
	ToolCallNotifyCh          chan *ParkedToolCall
	PromptDoneCh              chan error
	LastPromptUsage           *PromptUsage
	LastTranscriptStep        int
	LastInputTokens           int64
	LastCachedTokens          int64
	ClientCompactionRequested bool
	ClientCompactionWaitTurns int
	UpdatedAt                 time.Time
	lastStopReason            string
}

// SetLastStopReason records the ACP stopReason of the most recent session/prompt.
func (ms *ManagedSession) SetLastStopReason(reason string) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	ms.lastStopReason = reason
	ms.mu.Unlock()
}

// LastStopReason returns the ACP stopReason of the most recent session/prompt.
func (ms *ManagedSession) LastStopReason() string {
	if ms == nil {
		return ""
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.lastStopReason
}

func (ms *ManagedSession) GetLastTokenUsage() (inputTokens, cachedTokens int64) {
	if ms == nil {
		return 0, 0
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.LastInputTokens, ms.LastCachedTokens
}

func (ms *ManagedSession) SetLastTokenUsage(inputTokens, cachedTokens int64) {
	if ms == nil || inputTokens <= 0 {
		return
	}
	ms.mu.Lock()
	// If token usage dropped by more than 40% (e.g. after an ACP CHECKPOINT compaction),
	// clear the pending client-compaction flag so the new compacted window starts fresh.
	if ms.LastInputTokens > 0 && inputTokens < (ms.LastInputTokens*60)/100 {
		ms.ClientCompactionRequested = false
		ms.ClientCompactionWaitTurns = 0
	}
	ms.LastInputTokens = inputTokens
	if cachedTokens >= 0 {
		ms.LastCachedTokens = cachedTokens
	}
	ms.UpdatedAt = time.Now()
	ms.mu.Unlock()
}

func (ms *ManagedSession) GetClientCompactionState() (requested bool, waitTurns int) {
	if ms == nil {
		return false, 0
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.ClientCompactionRequested, ms.ClientCompactionWaitTurns
}

func (ms *ManagedSession) SetClientCompactionState(requested bool, waitTurns int) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	ms.ClientCompactionRequested = requested
	ms.ClientCompactionWaitTurns = waitTurns
	ms.UpdatedAt = time.Now()
	ms.mu.Unlock()
}

func (ms *ManagedSession) GetSessionTitle() string {
	if ms == nil {
		return ""
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.SessionTitle
}

func (ms *ManagedSession) SetSessionTitle(title string) {
	if ms == nil {
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	ms.mu.Lock()
	ms.SessionTitle = title
	ms.mu.Unlock()
}

func (ms *ManagedSession) GetAppliedConfig() (mode, model, thinking string) {
	if ms == nil {
		return "", "", ""
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.LastMode, ms.LastModel, ms.LastThinkingLevel
}

func (ms *ManagedSession) SetAppliedConfig(mode, model, thinking string) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	if mode != "" {
		ms.LastMode = mode
	}
	if model != "" {
		ms.LastModel = model
	}
	if thinking != "" {
		ms.LastThinkingLevel = thinking
	}
	ms.mu.Unlock()
}

func (ms *ManagedSession) GetTools() []ToolDefinition {
	if ms == nil {
		return nil
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if len(ms.Tools) == 0 {
		return []ToolDefinition{}
	}
	cp := make([]ToolDefinition, len(ms.Tools))
	copy(cp, ms.Tools)
	return cp
}

func (ms *ManagedSession) GetWorker() *WorkerClient {
	if ms == nil {
		return nil
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.Worker
}

func (ms *ManagedSession) SetWorker(w *WorkerClient) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	old := ms.Worker
	ms.Worker = w
	ms.UpdatedAt = time.Now()
	ms.mu.Unlock()
	if old != w {
		if old != nil {
			old.AddBoundSession(-1)
		}
		if w != nil {
			w.AddBoundSession(1)
		}
	}
}

func (ms *ManagedSession) GetACPSessionID() string {
	if ms == nil {
		return ""
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.ACPSessionID
}

func (ms *ManagedSession) SetACPSession(acpSessionID string, lastStep int) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	if acpSessionID == "" || acpSessionID != ms.ACPSessionID {
		ms.LastMode = ""
		ms.LastModel = ""
		ms.LastThinkingLevel = ""
		ms.LastInputTokens = 0
		ms.LastCachedTokens = 0
		ms.LastPromptUsage = nil
		ms.ClientCompactionRequested = false
		ms.ClientCompactionWaitTurns = 0
	}
	ms.ACPSessionID = acpSessionID
	ms.LastTranscriptStep = lastStep
	ms.UpdatedAt = time.Now()
	ms.mu.Unlock()
}

func (ms *ManagedSession) GetLastTranscriptStep() int {
	if ms == nil {
		return -1
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.LastTranscriptStep
}

func (ms *ManagedSession) SetLastTranscriptStep(step int) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	ms.LastTranscriptStep = step
	ms.UpdatedAt = time.Now()
	ms.mu.Unlock()
}

func (ms *ManagedSession) GetPromptDoneCh() chan error {
	if ms == nil {
		return nil
	}
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.PromptDoneCh
}

func (ms *ManagedSession) SetPromptDoneCh(ch chan error) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	ms.PromptDoneCh = ch
	ms.UpdatedAt = time.Now()
	ms.mu.Unlock()
}

func (ms *ManagedSession) SetLastPromptUsage(u *PromptUsage) {
	if ms == nil {
		return
	}
	ms.mu.Lock()
	ms.LastPromptUsage = u
	ms.UpdatedAt = time.Now()
	ms.mu.Unlock()
}

func (ms *ManagedSession) ConsumeLastPromptUsage() *PromptUsage {
	if ms == nil {
		return nil
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	u := ms.LastPromptUsage
	ms.LastPromptUsage = nil
	return u
}

type SessionManager struct {
	mu               sync.RWMutex
	baseWorkspaceDir string
	parkTimeout      time.Duration
	byBridgeID       map[string]*ManagedSession
	byExplicitKey    map[string]*ManagedSession
	byHistoryHash    map[string]*ManagedSession
	parkedCalls      map[string]*ParkedToolCall
	stopCh           chan struct{}
	closeOnce        sync.Once
}

func NewSessionManager(baseWorkspaceDir string, parkTimeout time.Duration) *SessionManager {
	if baseWorkspaceDir == "" {
		baseWorkspaceDir = "/tmp/antigravity-gateway-workspace"
	}
	if parkTimeout <= 0 {
		parkTimeout = 180 * time.Second
	}
	sm := &SessionManager{
		baseWorkspaceDir: baseWorkspaceDir,
		parkTimeout:      parkTimeout,
		byBridgeID:       make(map[string]*ManagedSession),
		byExplicitKey:    make(map[string]*ManagedSession),
		byHistoryHash:    make(map[string]*ManagedSession),
		parkedCalls:      make(map[string]*ParkedToolCall),
		stopCh:           make(chan struct{}),
	}
	go sm.gcLoop()
	return sm
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var resolvedWorkspaceCache sync.Map

// EnsureAccountWorkspace creates and canonicalizes `<baseWorkspaceDir>/<accountID>`
// (resolving macOS `/tmp` -> `/private/tmp` symlinks) so that the worker process
// cwd and every ACP session cwd under that account are identical. This prevents
// agy_acp_server.par's pre-tool hook from rejecting relative paths in MCP tools.
func EnsureAccountWorkspace(baseWorkspaceDir, accountID string) string {
	if baseWorkspaceDir == "" {
		baseWorkspaceDir = "/tmp/antigravity-gateway-workspace"
	}
	if accountID == "" {
		accountID = "default"
	}
	dir := filepath.Join(baseWorkspaceDir, accountID)
	if v, ok := resolvedWorkspaceCache.Load(dir); ok {
		if cached, ok2 := v.(string); ok2 && cached != "" {
			if st, err := os.Stat(cached); err == nil && st.IsDir() {
				return cached
			}
		}
	}
	_ = os.MkdirAll(dir, 0755)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != "" {
		resolvedWorkspaceCache.Store(dir, resolved)
		return resolved
	}
	resolvedWorkspaceCache.Store(dir, dir)
	return dir
}

func (sm *SessionManager) EnsureSessionWorkspace(accountID, bridgeID string) (string, error) {
	if accountID == "" {
		accountID = "default"
	}
	if bridgeID == "" {
		bridgeID = "sess_" + randomHex(6)
	}
	dir := filepath.Join(sm.baseWorkspaceDir, accountID, bridgeID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != "" {
		return resolved, nil
	}
	return dir, nil
}

func scopedKey(accountID, key string) string {
	if key == "" {
		return ""
	}
	if accountID == "" {
		return "::" + key
	}
	return accountID + "::" + key
}

const maxKeysPerSession = 32

func (sm *SessionManager) recordKeyIndexLocked(indexMap map[string]*ManagedSession, slicePtr *[]string, sess *ManagedSession, indexedKey string) {
	if indexedKey == "" {
		return
	}
	indexMap[indexedKey] = sess
	for _, k := range *slicePtr {
		if k == indexedKey {
			return
		}
	}
	*slicePtr = append(*slicePtr, indexedKey)
	if len(*slicePtr) > maxKeysPerSession {
		evict := (*slicePtr)[0]
		*slicePtr = (*slicePtr)[1:]
		if indexMap[evict] == sess {
			delete(indexMap, evict)
		}
	}
}

func (sm *SessionManager) recordExplicitKeyLocked(sess *ManagedSession, explicitKey string) {
	if explicitKey == "" {
		return
	}
	sess.ExplicitKey = explicitKey
	accID := sess.AccountID
	if accID == "" {
		accID = "default"
	}
	sm.recordKeyIndexLocked(sm.byExplicitKey, &sess.explicitKeys, sess, scopedKey(accID, explicitKey))
	sm.recordKeyIndexLocked(sm.byExplicitKey, &sess.explicitKeys, sess, scopedKey("", explicitKey))
}

func (sm *SessionManager) recordHistoryHashLocked(sess *ManagedSession, historyHash string) {
	if historyHash == "" {
		return
	}
	sess.HistoryHash = historyHash
	accID := sess.AccountID
	if accID == "" {
		accID = "default"
	}
	sm.recordKeyIndexLocked(sm.byHistoryHash, &sess.historyHashes, sess, scopedKey(accID, historyHash))
	sm.recordKeyIndexLocked(sm.byHistoryHash, &sess.historyHashes, sess, scopedKey("", historyHash))
	for i, h := range sess.recentHashes {
		if h == historyHash {
			sess.recentHashes = append(sess.recentHashes[:i], sess.recentHashes[i+1:]...)
			break
		}
	}
	sess.recentHashes = append(sess.recentHashes, historyHash)
	if len(sess.recentHashes) > maxKeysPerSession {
		sess.recentHashes = sess.recentHashes[len(sess.recentHashes)-maxKeysPerSession:]
	}
}

// IsStaleHistoryHash reports whether historyHash is a known but older point in the
// session's history, meaning the client forked or regenerated from an earlier turn
// and the live ACP session already contains turns the client discarded. The two
// most recent hashes (the latest request and its recorded reply) are current.
func (sm *SessionManager) IsStaleHistoryHash(sess *ManagedSession, historyHash string) bool {
	if sess == nil || historyHash == "" {
		return false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	n := len(sess.recentHashes)
	for i, h := range sess.recentHashes {
		if h == historyHash {
			return i < n-2
		}
	}
	return false
}

func (sm *SessionManager) RegisterOrMatchSession(accountID, explicitKey, historyHash string, worker *WorkerClient, acpSessionID string, tools []ToolDefinition) *ManagedSession {
	if accountID == "" {
		accountID = "default"
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if explicitKey != "" {
		if existing, ok := sm.byExplicitKey[scopedKey(accountID, explicitKey)]; ok {
			existing.mu.Lock()
			existing.UpdatedAt = time.Now()
			if len(tools) > 0 {
				existing.Tools = tools
			}
			if worker != nil && worker != existing.Worker {
				if existing.Worker != nil {
					existing.Worker.AddBoundSession(-1)
				}
				existing.Worker = worker
				worker.AddBoundSession(1)
			}
			if acpSessionID != "" && acpSessionID != existing.ACPSessionID {
				existing.ACPSessionID = acpSessionID
				existing.LastTranscriptStep = -1
			}
			if historyHash != "" {
				sm.recordHistoryHashLocked(existing, historyHash)
			}
			existing.mu.Unlock()
			return existing
		}
	}

	if historyHash != "" {
		if existing, ok := sm.byHistoryHash[scopedKey(accountID, historyHash)]; ok {
			existing.mu.Lock()
			existing.UpdatedAt = time.Now()
			if len(tools) > 0 {
				existing.Tools = tools
			}
			if worker != nil && worker != existing.Worker {
				if existing.Worker != nil {
					existing.Worker.AddBoundSession(-1)
				}
				existing.Worker = worker
				worker.AddBoundSession(1)
			}
			if acpSessionID != "" && acpSessionID != existing.ACPSessionID {
				existing.ACPSessionID = acpSessionID
				existing.LastTranscriptStep = -1
			}
			if explicitKey != "" {
				sm.recordExplicitKeyLocked(existing, explicitKey)
			}
			existing.mu.Unlock()
			return existing
		}
	}

	bridgeID := "br_" + randomHex(8)
	wsDir := EnsureAccountWorkspace(sm.baseWorkspaceDir, accountID)
	sess := &ManagedSession{
		BridgeID:           bridgeID,
		AccountID:          accountID,
		ExplicitKey:        explicitKey,
		HistoryHash:        historyHash,
		ACPSessionID:       acpSessionID,
		WorkspaceDir:       wsDir,
		Worker:             worker,
		Tools:              tools,
		ToolCallNotifyCh:   make(chan *ParkedToolCall, 16),
		LastTranscriptStep: -1,
		UpdatedAt:          time.Now(),
	}
	sm.byBridgeID[bridgeID] = sess
	if worker != nil {
		worker.AddBoundSession(1)
	}
	if explicitKey != "" {
		sm.recordExplicitKeyLocked(sess, explicitKey)
	}
	if historyHash != "" {
		sm.recordHistoryHashLocked(sess, historyHash)
	}
	return sess
}

func (sm *SessionManager) RebindSession(sess *ManagedSession, accountID, explicitKey, historyHash string, worker *WorkerClient, acpSessionID string, tools []ToolDefinition) *ManagedSession {
	if sess == nil {
		return sm.RegisterOrMatchSession(accountID, explicitKey, historyHash, worker, acpSessionID, tools)
	}
	if accountID == "" {
		accountID = "default"
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sess.mu.Lock()
	sess.UpdatedAt = time.Now()
	if accountID != sess.AccountID {
		oldPrefix := sess.AccountID + "::"
		newPrefix := accountID + "::"
		for i, k := range sess.explicitKeys {
			if strings.HasPrefix(k, oldPrefix) {
				if sm.byExplicitKey[k] == sess {
					delete(sm.byExplicitKey, k)
				}
				rekey := newPrefix + strings.TrimPrefix(k, oldPrefix)
				sess.explicitKeys[i] = rekey
				sm.byExplicitKey[rekey] = sess
			}
		}
		for i, h := range sess.historyHashes {
			if strings.HasPrefix(h, oldPrefix) {
				if sm.byHistoryHash[h] == sess {
					delete(sm.byHistoryHash, h)
				}
				rehash := newPrefix + strings.TrimPrefix(h, oldPrefix)
				sess.historyHashes[i] = rehash
				sm.byHistoryHash[rehash] = sess
			}
		}
		sess.AccountID = accountID
		sess.WorkspaceDir = EnsureAccountWorkspace(sm.baseWorkspaceDir, accountID)
	}
	if len(tools) > 0 {
		sess.Tools = tools
	}
	if worker != nil && worker != sess.Worker {
		if sess.Worker != nil {
			sess.Worker.AddBoundSession(-1)
		}
		sess.Worker = worker
		worker.AddBoundSession(1)
	}
	if acpSessionID != sess.ACPSessionID {
		sess.ACPSessionID = acpSessionID
		sess.LastTranscriptStep = -1
		sess.LastMode = ""
		sess.LastModel = ""
		sess.LastThinkingLevel = ""
	}
	if explicitKey != "" {
		sm.recordExplicitKeyLocked(sess, explicitKey)
	}
	if historyHash != "" {
		sm.recordHistoryHashLocked(sess, historyHash)
	}
	sess.mu.Unlock()
	return sess
}

func (sm *SessionManager) BindExplicitKey(sess *ManagedSession, explicitKey string) {
	if sess == nil || explicitKey == "" {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sess.mu.Lock()
	sess.UpdatedAt = time.Now()
	sm.recordExplicitKeyLocked(sess, explicitKey)
	sess.mu.Unlock()
}

func (sm *SessionManager) UpdateSessionHash(sess *ManagedSession, newHash string) {
	if sess == nil || newHash == "" {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sess.mu.Lock()
	sess.UpdatedAt = time.Now()
	sm.recordHistoryHashLocked(sess, newHash)
	sess.mu.Unlock()
}

// UpdateSessionToolsAndSystem updates the session's tool definitions and system prompt,
// returning whether new tools were dynamically added (e.g. via Sailry's tool_search)
// and any newly appended system instructions delta.
func (sm *SessionManager) UpdateSessionToolsAndSystem(sess *ManagedSession, newTools []ToolDefinition, newSysPrompt string) (toolsAdded bool, sysDelta string) {
	if sess == nil {
		return false, ""
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sess.mu.Lock()
	defer sess.mu.Unlock()

	if len(newTools) > 0 {
		existingNames := make(map[string]bool, len(sess.Tools))
		for _, t := range sess.Tools {
			existingNames[t.Name] = true
		}
		for _, t := range newTools {
			if !existingNames[t.Name] {
				toolsAdded = true
				break
			}
		}
		sess.Tools = newTools
	}

	trimmedNew := strings.TrimSpace(newSysPrompt)
	trimmedOld := strings.TrimSpace(sess.SystemPrompt)
	if trimmedNew != "" {
		if trimmedOld != "" && trimmedNew != trimmedOld {
			if strings.HasPrefix(trimmedNew, trimmedOld) {
				sysDelta = strings.TrimSpace(trimmedNew[len(trimmedOld):])
			} else {
				sysDelta = trimmedNew
			}
		}
		sess.SystemPrompt = trimmedNew
	}
	sess.UpdatedAt = time.Now()
	return toolsAdded, sysDelta
}

func (sm *SessionManager) LookupSession(accountID, explicitKey, historyHash string) (*ManagedSession, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	accountID = strings.TrimSpace(accountID)
	if accountID == "auto" {
		accountID = ""
	}
	if explicitKey != "" {
		if s, ok := sm.byExplicitKey[scopedKey(accountID, explicitKey)]; ok {
			return s, true
		}
	}
	if historyHash != "" {
		if s, ok := sm.byHistoryHash[scopedKey(accountID, historyHash)]; ok {
			return s, true
		}
	}
	return nil, false
}

func (sm *SessionManager) GetSessionByBridgeID(bridgeID string) (*ManagedSession, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.byBridgeID[bridgeID]
	return s, ok
}

func (sm *SessionManager) PeekParkedSession(callID string) *ManagedSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	parked, ok := sm.parkedCalls[callID]
	if !ok {
		return nil
	}
	return sm.byBridgeID[parked.BridgeID]
}

func (sm *SessionManager) ParkToolCall(bridgeID, toolName, argsJSON string) (*ParkedToolCall, error) {
	sm.mu.Lock()
	sess, ok := sm.byBridgeID[bridgeID]
	if !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("unknown session bridge_id %q", bridgeID)
	}
	callID := "call_" + randomHex(8)
	parked := &ParkedToolCall{
		CallID:    callID,
		BridgeID:  bridgeID,
		AccountID: sess.AccountID,
		ToolName:  toolName,
		ArgsJSON:  argsJSON,
		ResultCh:  make(chan ToolResultPayload, 1),
		CreatedAt: time.Now(),
	}
	sm.parkedCalls[callID] = parked
	w := sess.GetWorker()
	if w != nil {
		w.SetState(StateParked)
	}
	sm.mu.Unlock()

	select {
	case sess.ToolCallNotifyCh <- parked:
	default:
	}
	return parked, nil
}

func (sm *SessionManager) CancelParkedToolCall(callID string) {
	if callID == "" {
		return
	}
	sm.mu.Lock()
	parked, ok := sm.parkedCalls[callID]
	if !ok {
		sm.mu.Unlock()
		return
	}
	delete(sm.parkedCalls, callID)
	hasRemaining := false
	for _, p := range sm.parkedCalls {
		if p.BridgeID == parked.BridgeID {
			hasRemaining = true
			break
		}
	}
	if !hasRemaining {
		if sess, exists := sm.byBridgeID[parked.BridgeID]; exists {
			if w := sess.GetWorker(); w != nil {
				w.CompareAndSetState(StateParked, StateIdle)
			}
		}
	}
	sm.mu.Unlock()
}

func (sm *SessionManager) ResumeToolCall(callID, content string, isError bool) (*ManagedSession, error) {
	sm.mu.Lock()
	parked, ok := sm.parkedCalls[callID]
	if !ok {
		sm.mu.Unlock()
		return nil, fmt.Errorf("parked tool call %q not found or expired", callID)
	}
	delete(sm.parkedCalls, callID)
	sess := sm.byBridgeID[parked.BridgeID]
	if sess != nil {
		if w := sess.GetWorker(); w != nil {
			w.SetState(StateGenerating)
		}
	}
	sm.mu.Unlock()

	parked.ResultCh <- ToolResultPayload{
		Content: content,
		IsError: isError,
	}
	return sess, nil
}

func (sm *SessionManager) MarkToolCallDispatched(callID string) {
	if callID == "" {
		return
	}
	sm.mu.Lock()
	if parked, ok := sm.parkedCalls[callID]; ok && parked != nil {
		parked.Dispatched = true
	}
	sm.mu.Unlock()
}

// AutoResumeRemainingParkedCalls unblocks any straggler parallel tool calls on the same
// session bridge that were already dispatched to the client in the previous batch but
// omitted from the client's tool_results payload, preventing session/prompt from deadlocking
// without prematurely aborting newly queued calls still waiting in ToolCallNotifyCh.
func (sm *SessionManager) AutoResumeRemainingParkedCalls(bridgeID, fallbackContent string) {
	if bridgeID == "" {
		return
	}
	sm.mu.Lock()
	var orphans []*ParkedToolCall
	for id, p := range sm.parkedCalls {
		if p.BridgeID == bridgeID && p.Dispatched {
			delete(sm.parkedCalls, id)
			orphans = append(orphans, p)
		}
	}
	sm.mu.Unlock()

	for _, p := range orphans {
		select {
		case p.ResultCh <- ToolResultPayload{
			Content: fallbackContent,
			IsError: true,
		}:
		default:
		}
	}
}

func (sm *SessionManager) ParkedCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.parkedCalls)
}

func (sm *SessionManager) HasParkedCallsForSession(bridgeID string) bool {
	if bridgeID == "" {
		return false
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	for _, p := range sm.parkedCalls {
		if p != nil && p.BridgeID == bridgeID {
			return true
		}
	}
	return false
}

// AbortParkedCallsForSession immediately unblocks any parked MCP tool calls on the given
// session bridge (or account if sess is nil) and cancels the active ACP session prompt so
// the worker returns to StateIdle without hanging for parkTimeout (180s) when a client
// initiates context compaction mid-tool-loop.
func (sm *SessionManager) AbortParkedCallsForSession(sess *ManagedSession, accountID, reason string) int {
	if reason == "" {
		reason = "Error: tool call aborted because client initiated context compaction"
	}
	type abortItem struct {
		sess     *ManagedSession
		worker   *WorkerClient
		acpSess  string
		doneCh   chan error
		resultCh chan ToolResultPayload
	}
	var items []abortItem

	sm.mu.Lock()
	targetBridge := ""
	if sess != nil {
		targetBridge = sess.BridgeID
	}
	for id, p := range sm.parkedCalls {
		if p == nil {
			continue
		}
		match := false
		if targetBridge != "" && p.BridgeID == targetBridge {
			match = true
		} else if targetBridge == "" && accountID != "" && (p.AccountID == accountID || accountID == "auto") {
			match = true
		}
		if !match {
			continue
		}
		delete(sm.parkedCalls, id)
		s := sm.byBridgeID[p.BridgeID]
		var w *WorkerClient
		var acpSess string
		var doneCh chan error
		if s != nil {
			w = s.GetWorker()
			acpSess = s.GetACPSessionID()
			doneCh = s.GetPromptDoneCh()
		}
		items = append(items, abortItem{
			sess:     s,
			worker:   w,
			acpSess:  acpSess,
			doneCh:   doneCh,
			resultCh: p.ResultCh,
		})
	}
	sm.mu.Unlock()

	if len(items) == 0 {
		return 0
	}

	seenSess := make(map[*ManagedSession]bool)
	for _, it := range items {
		if it.worker != nil && it.acpSess != "" && !seenSess[it.sess] {
			_ = it.worker.CancelSession(it.acpSess)
		}
		select {
		case it.resultCh <- ToolResultPayload{
			Content: reason,
			IsError: true,
		}:
		default:
		}
	}

	for _, it := range items {
		if it.sess == nil || seenSess[it.sess] {
			continue
		}
		seenSess[it.sess] = true
		for {
			select {
			case <-it.sess.ToolCallNotifyCh:
			default:
				goto drainedNotify
			}
		}
	drainedNotify:
		if it.doneCh != nil {
			select {
			case <-it.doneCh:
			case <-time.After(600 * time.Millisecond):
			}
		}
		if it.worker != nil {
			it.worker.DrainEvents()
			it.worker.CompareAndSetState(StateParked, StateIdle)
		}
	}
	return len(items)
}

func (sm *SessionManager) WorkerBoundSessionsMap() map[int][]WorkerBoundSession {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	out := make(map[int][]WorkerBoundSession)
	for _, sess := range sm.byBridgeID {
		if sess == nil {
			continue
		}
		sess.mu.RLock()
		w := sess.Worker
		bridgeID := sess.BridgeID
		acpSessID := sess.ACPSessionID
		explicitKey := sess.ExplicitKey
		updatedAt := sess.UpdatedAt
		sess.mu.RUnlock()
		if w == nil || w.PID <= 0 {
			continue
		}
		out[w.PID] = append(out[w.PID], WorkerBoundSession{
			BridgeID:     bridgeID,
			ACPSessionID: acpSessID,
			ExplicitKey:  explicitKey,
			UpdatedAt:    updatedAt,
		})
	}
	return out
}

func (sm *SessionManager) gcLoop() {
	interval := sm.parkTimeout / 2
	if interval < 20*time.Millisecond {
		interval = 20 * time.Millisecond
	}
	if interval > 5*time.Second {
		interval = 5 * time.Second
	}
	sessionTTL := sm.parkTimeout * 10
	if sessionTTL < 30*time.Minute {
		sessionTTL = 30 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()
			type expiredCleanup struct {
				worker   *WorkerClient
				acpSess  string
				doneCh   chan error
				resultCh chan ToolResultPayload
			}
			var cleanups []expiredCleanup

			sm.mu.Lock()
			activeBridges := make(map[string]bool)
			for callID, parked := range sm.parkedCalls {
				if now.Sub(parked.CreatedAt) > sm.parkTimeout {
					delete(sm.parkedCalls, callID)
					var w *WorkerClient
					var acpSess string
					var doneCh chan error
					if sess, ok := sm.byBridgeID[parked.BridgeID]; ok {
						w = sess.GetWorker()
						acpSess = sess.GetACPSessionID()
						doneCh = sess.GetPromptDoneCh()
					}
					cleanups = append(cleanups, expiredCleanup{
						worker:   w,
						acpSess:  acpSess,
						doneCh:   doneCh,
						resultCh: parked.ResultCh,
					})
				} else {
					activeBridges[parked.BridgeID] = true
				}
			}

			for bridgeID, sess := range sm.byBridgeID {
				if activeBridges[bridgeID] {
					continue
				}
				sess.mu.RLock()
				updatedAt := sess.UpdatedAt
				boundWorker := sess.Worker
				ekList := append([]string(nil), sess.explicitKeys...)
				hhList := append([]string(nil), sess.historyHashes...)
				sess.mu.RUnlock()
				if now.Sub(updatedAt) > sessionTTL {
					if boundWorker != nil {
						boundWorker.AddBoundSession(-1)
					}
					delete(sm.byBridgeID, bridgeID)
					for _, k := range ekList {
						if sm.byExplicitKey[k] == sess {
							delete(sm.byExplicitKey, k)
						}
					}
					for _, h := range hhList {
						if sm.byHistoryHash[h] == sess {
							delete(sm.byHistoryHash, h)
						}
					}
				}
			}
			sm.mu.Unlock()

			for _, item := range cleanups {
				select {
				case item.resultCh <- ToolResultPayload{
					Content: "Error: client tool execution timeout expired",
					IsError: true,
				}:
				default:
				}
				if item.worker != nil {
					if item.acpSess != "" {
						_ = item.worker.CancelSession(item.acpSess)
					}
					w := item.worker
					doneCh := item.doneCh
					go func() {
						if doneCh != nil {
							select {
							case <-doneCh:
							case <-time.After(1500 * time.Millisecond):
							}
						}
						w.DrainEvents()
						w.CompareAndSetState(StateParked, StateIdle)
					}()
				}
			}
		case <-sm.stopCh:
			return
		}
	}
}

func (sm *SessionManager) Close() {
	sm.closeOnce.Do(func() {
		close(sm.stopCh)
	})
}
