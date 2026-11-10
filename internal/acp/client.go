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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type WorkerState string

const (
	StateIdle       WorkerState = "Idle"
	StateGenerating WorkerState = "Generating"
	StateParked     WorkerState = "Parked"
	StateDead       WorkerState = "Dead"
)

// NormalizeBackendType normalizes "zen" -> "zed", "cli" -> "cli", and everything else to "auto".
func NormalizeBackendType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "zed", "zen", "par", "standalone":
		return "zed"
	case "cli", "agy", "antigravity":
		return "cli"
	default:
		return "auto"
	}
}

// isCompatibleExecutable checks whether a file on disk exists, is not a directory,
// and does not have an incompatible OS binary header (e.g., preventing a host macOS Mach-O
// or Windows PE binary in ./bin from being selected inside a Linux Docker container).
func isCompatibleExecutable(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()

	var hdr [4]byte
	n, err := f.Read(hdr[:])
	if err != nil || n < 2 {
		return true
	}
	isELF := n >= 4 && hdr[0] == 0x7f && hdr[1] == 'E' && hdr[2] == 'L' && hdr[3] == 'F'
	isMachO := n >= 4 && ((hdr[0] == 0xcf && hdr[1] == 0xfa && hdr[2] == 0xed && hdr[3] == 0xfe) ||
		(hdr[0] == 0xfe && hdr[1] == 0xed && hdr[2] == 0xfa && hdr[3] == 0xcf) ||
		(hdr[0] == 0xca && hdr[1] == 0xfe && hdr[2] == 0xba && hdr[3] == 0xbe))
	isPE := hdr[0] == 'M' && hdr[1] == 'Z'

	switch runtime.GOOS {
	case "linux":
		if isMachO || isPE {
			return false
		}
	case "darwin":
		if isELF || isPE {
			return false
		}
	case "windows":
		if isELF || isMachO {
			return false
		}
	}
	return true
}

func discoverZedACPBinary() string {
	var candidates []string
	// 1. Local project / container paths
	candidates = append(candidates,
		"./bin/agy_acp_server.par",
		"./bin/agy_acp_server",
		"./bin/agy_acp_server.exe",
		"/usr/local/bin/agy_acp_server.par",
		"/usr/local/bin/agy_acp_server",
		"/opt/antigravity-acp/agy_acp_server.par",
		"/opt/antigravity-acp/agy_acp_server",
	)

	home, _ := os.UserHomeDir()
	var globPatterns []string
	if home != "" {
		switch runtime.GOOS {
		case "darwin":
			globPatterns = append(globPatterns,
				filepath.Join(home, "Library", "Application Support", "Zed", "external_agents", "registry", "antigravity-acp", "*", "agy_acp_server*"),
			)
		case "windows":
			if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
				globPatterns = append(globPatterns,
					filepath.Join(localAppData, "Zed", "external_agents", "registry", "antigravity-acp", "*", "agy_acp_server*"),
				)
			}
			if appData := os.Getenv("APPDATA"); appData != "" {
				globPatterns = append(globPatterns,
					filepath.Join(appData, "Zed", "external_agents", "registry", "antigravity-acp", "*", "agy_acp_server*"),
				)
			}
		default:
			globPatterns = append(globPatterns,
				filepath.Join(home, ".local", "share", "zed", "external_agents", "registry", "antigravity-acp", "*", "agy_acp_server*"),
				filepath.Join(home, ".config", "zed", "external_agents", "registry", "antigravity-acp", "*", "agy_acp_server*"),
			)
		}
	}

	for _, pattern := range globPatterns {
		if matches, err := filepath.Glob(pattern); err == nil && len(matches) > 0 {
			sort.Strings(matches)
			// Prefer newest version (last in lexical sort)
			for i := len(matches) - 1; i >= 0; i-- {
				candidates = append([]string{matches[i]}, candidates...)
			}
		}
	}

	for _, c := range candidates {
		if isCompatibleExecutable(c) {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	if p, err := exec.LookPath("agy_acp_server.par"); err == nil && isCompatibleExecutable(p) {
		return p
	}
	if p, err := exec.LookPath("agy_acp_server"); err == nil && isCompatibleExecutable(p) {
		return p
	}
	return ""
}

func discoverCLIACPBinary() string {
	for _, binName := range []string{"agy", "antigravity", "agy.exe", "antigravity.exe"} {
		if p, err := exec.LookPath(binName); err == nil && p != "" && isCompatibleExecutable(p) {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			candidates = append(candidates,
				filepath.Join(localAppData, "agy", "bin", "agy.exe"),
				filepath.Join(localAppData, "Programs", "Antigravity", "agy.exe"),
				filepath.Join(localAppData, "Programs", "Antigravity", "antigravity.exe"),
			)
		}
		if home != "" {
			candidates = append(candidates,
				filepath.Join(home, ".local", "bin", "agy.exe"),
				filepath.Join(home, ".antigravity", "bin", "agy.exe"),
			)
		}
		candidates = append(candidates,
			`.\bin\agy.exe`,
			`.\bin\antigravity.exe`,
			`C:\Program Files\Google\antigravity-cli\agy.exe`,
		)
	} else {
		candidates = append(candidates,
			"./bin/agy",
			"./bin/antigravity",
			"/usr/local/bin/agy",
			"/usr/local/bin/antigravity",
			"/opt/antigravity-acp/agy",
			"/opt/antigravity-acp/antigravity",
			"/opt/homebrew/bin/agy",
			"/opt/homebrew/bin/antigravity",
		)
		if home != "" {
			candidates = append(candidates,
				filepath.Join(home, ".local", "bin", "agy"),
				filepath.Join(home, ".antigravity", "bin", "agy"),
			)
		}
	}
	for _, c := range candidates {
		if isCompatibleExecutable(c) {
			if abs, err := filepath.Abs(c); err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

// ResolveACPBinaryPath resolves the effective ACP binary path based on BackendType ("auto" | "zed" | "zen" | "cli")
// and configured BinaryPath, supporting automatic cross-platform discovery across macOS, Linux, Windows, and Docker.
func ResolveACPBinaryPath(backendType, configuredPath string) string {
	mode := NormalizeBackendType(backendType)
	trimmed := strings.TrimSpace(configuredPath)
	lowerBase := strings.ToLower(filepath.Base(trimmed))
	isConfiguredZedPar := strings.HasSuffix(lowerBase, ".par") || strings.Contains(lowerBase, "agy_acp_server")

	// Check if configuredPath exists on disk or in PATH and matches current OS
	pathExists := false
	if trimmed != "" && trimmed != "auto" {
		if isCompatibleExecutable(trimmed) {
			pathExists = true
			if abs, err := filepath.Abs(trimmed); err == nil {
				trimmed = abs
			}
		} else if !strings.ContainsAny(trimmed, `/\`) {
			if p, err := exec.LookPath(trimmed); err == nil && isCompatibleExecutable(p) {
				pathExists = true
			}
		}
	}

	switch mode {
	case "cli":
		if pathExists && !isConfiguredZedPar {
			return trimmed
		}
		if found := discoverCLIACPBinary(); found != "" {
			return found
		}
		if pathExists {
			return trimmed
		}
		if found := discoverZedACPBinary(); found != "" {
			return found
		}
		if trimmed != "" && trimmed != "auto" && !isConfiguredZedPar {
			return trimmed
		}
		return "agy"
	case "zed":
		if pathExists && isConfiguredZedPar {
			return trimmed
		}
		if found := discoverZedACPBinary(); found != "" {
			return found
		}
		if trimmed != "" && trimmed != "auto" {
			return trimmed
		}
		return "agy_acp_server.par"
	default:
		if pathExists {
			return trimmed
		}
		if found := discoverCLIACPBinary(); found != "" {
			return found
		}
		if found := discoverZedACPBinary(); found != "" {
			return found
		}
		if trimmed != "" && trimmed != "auto" {
			return trimmed
		}
		return "agy"
	}
}

func BuildCommandAndArgs(opts WorkerLaunchOptions) (string, []string) {
	resolvedBin := opts.BinaryPath
	if strings.TrimSpace(resolvedBin) == "" || strings.TrimSpace(resolvedBin) == "auto" || opts.BackendType != "" {
		resolvedBin = ResolveACPBinaryPath(opts.BackendType, opts.BinaryPath)
	}
	var args []string
	base := strings.ToLower(filepath.Base(resolvedBin))
	baseNoExt := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(base, ".exe"), ".cmd"), ".bat")
	mode := NormalizeBackendType(opts.BackendType)
	isStandalonePar := strings.HasSuffix(base, ".par") || strings.Contains(base, "agy_acp_server") || (mode == "zed" && baseNoExt != "agy" && baseNoExt != "antigravity")
	if baseNoExt == "agy" || baseNoExt == "antigravity" || (mode == "cli" && !isStandalonePar) {
		args = append(args, "acp")
	}
	if !isStandalonePar {
		if opts.ProcessPerSession {
			args = append(args, "--process-per-session")
		}
		if opts.SkipPermissionsFlag {
			args = append(args, "--dangerously-skip-permissions")
		}
		if opts.EnableSandbox {
			args = append(args, "--sandbox")
		}
		if opts.DisableTelemetry {
			args = append(args, "--disable-telemetry")
		}
		if opts.LogFile != "" {
			args = append(args, "--log-file", opts.LogFile)
		}
		if opts.IDEMCPPort > 0 {
			args = append(args, "--ide-mcp-port", strconv.Itoa(opts.IDEMCPPort))
		}
		if opts.AgentName != "" {
			args = append(args, "--agent", opts.AgentName)
		}
		for _, dir := range opts.AdditionalDirs {
			if strings.TrimSpace(dir) != "" {
				args = append(args, "--add-dir", strings.TrimSpace(dir))
			}
		}
	}
	args = append(args, opts.ExtraArgs...)

	if strings.TrimSpace(opts.NetNSName) != "" {
		netnsArgs := append([]string{"netns", "exec", strings.TrimSpace(opts.NetNSName), resolvedBin}, args...)
		return "ip", netnsArgs
	}
	return resolvedBin, args
}

type WorkerClient struct {
	ID               string
	AccountID        string
	PID              int
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	stdout           io.ReadCloser
	writeMu          sync.Mutex
	seqID            atomic.Int64
	pending          sync.Map // map[int64]chan *RPCMessage
	eventsCh         chan *RPCMessage
	stateMu          sync.RWMutex
	state            WorkerState
	lastActiveAt     time.Time
	boundSessions    atomic.Int32
	internalApproval string
	thinkingConfigID string
	supportsYoloMode *bool
	wiretap          WiretapCallback
	closeOnce        sync.Once
	closedCh         chan struct{}
}

func (w *WorkerClient) AddBoundSession(delta int32) {
	if w == nil {
		return
	}
	if w.boundSessions.Add(delta) < 0 {
		w.boundSessions.Store(0)
	}
}

func (w *WorkerClient) BoundSessionCount() int {
	if w == nil {
		return 0
	}
	n := int(w.boundSessions.Load())
	if n < 0 {
		return 0
	}
	return n
}

func NewWorkerClient(ctx context.Context, id string, opts WorkerLaunchOptions, wiretap WiretapCallback) (*WorkerClient, error) {
	bin, args := BuildCommandAndArgs(opts)
	if strings.ContainsAny(bin, `/\`) && !filepath.IsAbs(bin) {
		if absBin, err := filepath.Abs(bin); err == nil {
			bin = absBin
		}
	}
	cmd := exec.Command(bin, args...)
	configureWorkerProcAttr(cmd)
	if opts.WorkspaceDir != "" {
		_ = os.MkdirAll(opts.WorkspaceDir, 0755)
		cmd.Dir = opts.WorkspaceDir
	}

	envList := os.Environ()
	for k, v := range opts.Env {
		envList = append(envList, k+"="+v)
	}
	cmd.Env = envList

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start acp worker (%s): %w", bin, err)
	}

	approval := opts.InternalApproval
	if approval == "" {
		approval = "auto_approve"
	}
	accountID := opts.AccountID
	if accountID == "" {
		accountID = "default"
	}

	w := &WorkerClient{
		ID:               id,
		AccountID:        accountID,
		PID:              cmd.Process.Pid,
		cmd:              cmd,
		stdin:            stdin,
		stdout:           stdout,
		eventsCh:         make(chan *RPCMessage, 512),
		state:            StateIdle,
		lastActiveAt:     time.Now(),
		internalApproval: approval,
		wiretap:          wiretap,
		closedCh:         make(chan struct{}),
	}

	go w.readLoop()
	return w, nil
}

func (w *WorkerClient) GetState() WorkerState {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.state
}

func (w *WorkerClient) SetState(s WorkerState) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	w.state = s
	w.lastActiveAt = time.Now()
}

func (w *WorkerClient) LastActiveAt() time.Time {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.lastActiveAt
}

func (w *WorkerClient) Events() <-chan *RPCMessage {
	return w.eventsCh
}

func (w *WorkerClient) readLoop() {
	defer func() {
		w.SetState(StateDead)
		close(w.closedCh)
		w.pending.Range(func(key, value any) bool {
			if ch, ok := value.(chan *RPCMessage); ok {
				close(ch)
			}
			w.pending.Delete(key)
			return true
		})
	}()

	scanner := bufio.NewScanner(w.stdout)
	// Support up to 32MB per JSON-RPC line
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		if w.wiretap != nil {
			w.wiretap(w.ID, "IN", line)
		}

		var msg RPCMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// Reverse RPC from ACP server (e.g. session/request_permission)
		if msg.ID != nil && msg.Method != "" {
			go w.handleServerReverseRPC(&msg)
			continue
		}

		// Standard response to a gateway request
		if msg.ID != nil && msg.Method == "" {
			if val, ok := w.pending.LoadAndDelete(*msg.ID); ok {
				if ch, ok := val.(chan *RPCMessage); ok {
					ch <- &msg
					close(ch)
				}
			}
			continue
		}

		// Server notification (e.g. session/update)
		select {
		case w.eventsCh <- &msg:
		default:
		}
	}
}

func (w *WorkerClient) handleServerReverseRPC(msg *RPCMessage) {
	w.stateMu.RLock()
	approval := w.internalApproval
	w.stateMu.RUnlock()

	switch msg.Method {
	case "session/request_permission":
		outcome := "approved"
		if approval == "reject" {
			outcome = "cancelled"
		}
		_ = w.sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      *msg.ID,
			"result": map[string]any{
				"outcome": map[string]any{
					"outcome": outcome,
				},
			},
		})
	default:
		_ = w.sendJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      *msg.ID,
			"result":  map[string]any{},
		})
	}
}

func (w *WorkerClient) SetInternalApproval(approval string) {
	if approval == "" {
		return
	}
	w.stateMu.Lock()
	w.internalApproval = approval
	w.stateMu.Unlock()
}

func (w *WorkerClient) sendJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.SendRawJSONLine(raw)
}

func (w *WorkerClient) SendRawJSONLine(raw []byte) error {
	line := strings.TrimSpace(string(raw))
	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	if w.wiretap != nil {
		w.wiretap(w.ID, "OUT", line)
	}
	_, err := w.stdin.Write([]byte(line + "\n"))
	return err
}

func (w *WorkerClient) Call(ctx context.Context, method string, params any) (*RPCMessage, error) {
	id := w.seqID.Add(1)
	ch := make(chan *RPCMessage, 1)
	w.pending.Store(id, ch)
	defer w.pending.Delete(id)

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := w.sendJSON(req); err != nil {
		return nil, fmt.Errorf("send rpc %s: %w", method, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, fmt.Errorf("worker %s closed while waiting for %s", w.ID, method)
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.closedCh:
		return nil, fmt.Errorf("worker %s process exited during %s", w.ID, method)
	}
}

func (w *WorkerClient) Notify(method string, params any) error {
	return w.sendJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (w *WorkerClient) Initialize(ctx context.Context, clientName, clientVersion string) (*InitializeResult, error) {
	if clientName == "" {
		clientName = "zed"
	}
	if clientVersion == "" {
		clientVersion = "0.175.0"
	}
	resp, err := w.Call(ctx, "initialize", InitializeParams{
		ProtocolVersion: 1,
		ClientInfo: ClientInfo{
			Name:    clientName,
			Title:   "Zed",
			Version: clientVersion,
		},
		ClientCapabilities: map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	var res InitializeResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("decode initialize result: %w", err)
	}
	return &res, nil
}

func (w *WorkerClient) NewSession(ctx context.Context, params NewSessionParams) (*NewSessionResult, error) {
	if params.MCPServers == nil {
		params.MCPServers = []MCPServerEndpoint{}
	}
	resp, err := w.Call(ctx, "session/new", params)
	if err != nil {
		return nil, err
	}
	var res NewSessionResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("decode session/new result: %w", err)
	}
	w.recordSessionConfigCapabilities(&res)
	return &res, nil
}

func (w *WorkerClient) ResumeSession(ctx context.Context, params ResumeSessionParams) (*NewSessionResult, error) {
	if params.MCPServers == nil {
		params.MCPServers = []MCPServerEndpoint{}
	}
	resp, err := w.Call(ctx, "session/resume", params)
	if err != nil {
		return nil, err
	}
	var res NewSessionResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return nil, fmt.Errorf("decode session/resume result: %w", err)
	}
	w.recordSessionConfigCapabilities(&res)
	return &res, nil
}

func (w *WorkerClient) recordSessionConfigCapabilities(res *NewSessionResult) {
	if w == nil || res == nil || len(res.ConfigOptions) == 0 {
		return
	}
	var thinkID string
	var hasModeOpt bool
	var hasYolo bool
	for _, opt := range res.ConfigOptions {
		idLower := strings.ToLower(strings.TrimSpace(opt.ID))
		catLower := strings.ToLower(strings.TrimSpace(opt.Category))
		if idLower == "thinking_level" || idLower == "thought_level" || catLower == "thinking_level" || catLower == "thought_level" {
			if strings.TrimSpace(opt.ID) != "" {
				thinkID = strings.TrimSpace(opt.ID)
			}
		}
		if idLower == "mode" || catLower == "mode" {
			if len(opt.Options) > 0 {
				hasModeOpt = true
				for _, v := range opt.Options {
					if strings.EqualFold(strings.TrimSpace(v.Value), "yolo") {
						hasYolo = true
						break
					}
				}
			}
		}
	}
	w.stateMu.Lock()
	if thinkID != "" {
		w.thinkingConfigID = thinkID
	}
	if hasModeOpt {
		yoloSupported := hasYolo
		w.supportsYoloMode = &yoloSupported
	}
	w.stateMu.Unlock()
}

func (w *WorkerClient) CloseSession(ctx context.Context, sessionID string) error {
	_, err := w.Call(ctx, "session/close", CloseSessionParams{SessionID: sessionID})
	return err
}

func (w *WorkerClient) SetConfigOption(ctx context.Context, sessionID, configID, value string) error {
	effectiveID := strings.TrimSpace(configID)
	effectiveVal := strings.TrimSpace(value)

	w.stateMu.RLock()
	thinkID := w.thinkingConfigID
	yoloPtr := w.supportsYoloMode
	w.stateMu.RUnlock()

	if strings.EqualFold(effectiveID, "thinking_level") || strings.EqualFold(effectiveID, "thought_level") {
		if thinkID != "" {
			effectiveID = thinkID
		}
		_, err := w.Call(ctx, "session/set_config_option", SetConfigOptionParams{
			SessionID: sessionID,
			ConfigID:  effectiveID,
			Value:     effectiveVal,
		})
		if err != nil {
			altID := "thought_level"
			if strings.EqualFold(effectiveID, "thought_level") {
				altID = "thinking_level"
			}
			if _, err2 := w.Call(ctx, "session/set_config_option", SetConfigOptionParams{
				SessionID: sessionID,
				ConfigID:  altID,
				Value:     effectiveVal,
			}); err2 == nil {
				w.stateMu.Lock()
				w.thinkingConfigID = altID
				w.stateMu.Unlock()
				return nil
			}
		}
		return err
	}

	if strings.EqualFold(effectiveID, "mode") && strings.EqualFold(effectiveVal, "yolo") {
		if yoloPtr != nil && !*yoloPtr {
			effectiveVal = "auto"
		}
		_, err := w.Call(ctx, "session/set_config_option", SetConfigOptionParams{
			SessionID: sessionID,
			ConfigID:  effectiveID,
			Value:     effectiveVal,
		})
		if err != nil && strings.EqualFold(effectiveVal, "yolo") {
			if _, err2 := w.Call(ctx, "session/set_config_option", SetConfigOptionParams{
				SessionID: sessionID,
				ConfigID:  effectiveID,
				Value:     "auto",
			}); err2 == nil {
				yoloSupported := false
				w.stateMu.Lock()
				w.supportsYoloMode = &yoloSupported
				w.stateMu.Unlock()
				return nil
			}
		}
		return err
	}

	_, err := w.Call(ctx, "session/set_config_option", SetConfigOptionParams{
		SessionID: sessionID,
		ConfigID:  effectiveID,
		Value:     effectiveVal,
	})
	return err
}

func (w *WorkerClient) CancelSession(sessionID string) error {
	return w.Notify("session/cancel", CancelParams{SessionID: sessionID})
}

func (w *WorkerClient) DrainEvents() {
	for {
		select {
		case <-w.eventsCh:
		default:
			return
		}
	}
}

func (w *WorkerClient) Close() error {
	var err error
	w.closeOnce.Do(func() {
		w.SetState(StateDead)
		_ = w.stdin.Close()
		err = terminateWorkerProcess(w.cmd)
	})
	return err
}
