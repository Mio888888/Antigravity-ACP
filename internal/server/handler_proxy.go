package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"antigravity-gateway/internal/acp"
	"antigravity-gateway/internal/adapter"
	"antigravity-gateway/internal/config"
	"antigravity-gateway/internal/storage"

	_ "modernc.org/sqlite"
)

const universalThinkingSystemPrompt = "EXECUTION & REASONING RULES:\n" +
	"1. BATCH PARALLEL TOOL CALLS & DEDICATED TOOLS: Invoke ALL independent tools in parallel within a SINGLE turn rather than sequentially across many turns. Prefer dedicated tools (`read_file`, `search_files`, `find_files`, `code_context`, `code_outline`) with a generous `limit` (800-2000 lines) over ad-hoc `python3 -c` or shell scripts for reading or searching code.\n" +
	"2. SINGLE INITIAL REASONING ONLY: On the VERY FIRST turn of a user request (before any tool has been called), you may output ONE brief 1-2 sentence reasoning summary in the user's language enclosed in ⟦REASONING⟧ and ⟦/REASONING⟧ at the very start of your output. After ANY tool result is returned, or on your final response, NEVER output ⟦REASONING⟧ blocks or intermediate status prose before tool calls—invoke the next batch of tools or deliver the final answer directly so consecutive tool executions fold into a single clean progress group.\n" +
	"3. EXACT MCP TOOL NAMES: When invoking any MCP tool from client_tools (names starting with mcp_client_tools_), ALWAYS use the exact registered tool name with its mcp_client_tools_ prefix."

var (
	baseWorkspaceDirsOnce   sync.Once
	cachedBaseWorkspaceDirs []string
)

func defaultAllowedWorkspaceDirs(extra []string) []string {
	baseWorkspaceDirsOnce.Do(func() {
		candidates := []string{
			"/Users",
			"/home",
			"/tmp",
			"/private/tmp",
			"/var/folders",
			"/opt",
			"/workspace",
			"/workspaces",
			"/app",
			"/srv",
			"/data",
		}
		seen := make(map[string]bool, len(candidates))
		for _, dir := range candidates {
			cleaned := filepath.Clean(strings.TrimSpace(dir))
			if cleaned == "" || cleaned == "/" || cleaned == "." || seen[cleaned] {
				continue
			}
			if info, err := os.Stat(cleaned); err == nil && info.IsDir() {
				seen[cleaned] = true
				cachedBaseWorkspaceDirs = append(cachedBaseWorkspaceDirs, cleaned)
			}
		}
	})
	if len(extra) == 0 {
		out := make([]string, len(cachedBaseWorkspaceDirs))
		copy(out, cachedBaseWorkspaceDirs)
		return out
	}
	seen := make(map[string]bool, len(cachedBaseWorkspaceDirs)+len(extra))
	out := make([]string, 0, len(cachedBaseWorkspaceDirs)+len(extra))
	for _, dir := range cachedBaseWorkspaceDirs {
		seen[dir] = true
		out = append(out, dir)
	}
	for _, dir := range extra {
		cleaned := filepath.Clean(strings.TrimSpace(dir))
		if cleaned == "" || cleaned == "/" || cleaned == "." || seen[cleaned] {
			continue
		}
		if info, err := os.Stat(cleaned); err == nil && info.IsDir() {
			seen[cleaned] = true
			out = append(out, cleaned)
		}
	}
	return out
}

func genReqID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func estimateTokens(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runes := int64(utf8.RuneCountInString(trimmed))
	bytesLen := int64(len(trimmed))
	est := (bytesLen + runes + 6) / 7
	if est < 1 {
		est = 1
	}
	return est
}

func estimateToolCallsJSONTokens(toolCallsJSON string) int64 {
	trimmed := strings.TrimSpace(toolCallsJSON)
	if trimmed == "" {
		return 0
	}
	if strings.HasPrefix(trimmed, "[") {
		var calls []adapter.CanonicalToolCall
		if json.Unmarshal([]byte(trimmed), &calls) == nil && len(calls) > 0 {
			var sum int64
			for _, c := range calls {
				sum += estimateTokens(c.ArgsJSON) + 6
			}
			return sum
		}
	}
	var single struct {
		Arguments string `json:"arguments"`
		ArgsJSON  string `json:"args_json"`
	}
	if json.Unmarshal([]byte(trimmed), &single) == nil {
		argStr := single.Arguments
		if argStr == "" {
			argStr = single.ArgsJSON
		}
		if argStr != "" {
			return estimateTokens(argStr) + 6
		}
	}
	return estimateTokens(trimmed)
}

// estimatePureOutputTokens calculates actual output tokens (assistant response text + tool call arguments),
// strictly excluding model thinking/reasoning tokens.
func estimatePureOutputTokens(responseText, toolCallsJSON string) int64 {
	return estimateTokens(responseText) + estimateToolCallsJSONTokens(toolCallsJSON)
}

// EstimatePromptTokens estimates northbound prompt tokens without double-counting
// PromptBlocks and ToolResults that are already included in CanonicalRequest.Messages.
func EstimatePromptTokens(canReq *adapter.CanonicalRequest) int64 {
	if canReq == nil {
		return 0
	}
	var total int64
	total += estimateTokens(canReq.SystemPrompt)
	if len(canReq.Messages) > 0 {
		for _, m := range canReq.Messages {
			total += estimateTokens(m.Content) + 4
		}
	} else {
		for _, b := range canReq.PromptBlocks {
			total += estimateTokens(b.Text)
		}
		for _, tr := range canReq.ToolResults {
			total += estimateTokens(tr.Content) + 4
		}
	}
	if total < 1 {
		total = 1
	}
	return total
}

func estimatePromptTokens(canReq *adapter.CanonicalRequest) int64 {
	return EstimatePromptTokens(canReq)
}

func extractPromptPreview(canReq *adapter.CanonicalRequest) string {
	if canReq == nil {
		return ""
	}
	var parts []string
	if len(canReq.ToolResults) > 0 {
		for _, tr := range canReq.ToolResults {
			if t := strings.TrimSpace(tr.Content); t != "" {
				parts = append(parts, fmt.Sprintf("[tool_result:%s] %s", tr.CallID, t))
			}
		}
		return strings.Join(parts, "\n\n")
	}
	for _, b := range canReq.PromptBlocks {
		if t := strings.TrimSpace(b.Text); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 && len(canReq.Messages) > 0 {
		last := canReq.Messages[len(canReq.Messages)-1]
		if t := strings.TrimSpace(last.Content); t != "" {
			parts = append(parts, fmt.Sprintf("[%s] %s", last.Role, t))
		}
	}
	return strings.Join(parts, "\n\n")
}

func truncateUTF8Middle(s string, maxBytes int, marker string) string {
	if len(s) <= maxBytes || maxBytes <= 0 {
		return s
	}
	if maxBytes <= len(marker)+32 {
		cut := maxBytes
		for cut > 0 && !utf8.ValidString(s[:cut]) {
			cut--
		}
		return s[:cut]
	}
	avail := maxBytes - len(marker)
	headLen := (avail * 2) / 3
	tailLen := avail - headLen
	for headLen > 0 && !utf8.ValidString(s[:headLen]) {
		headLen--
	}
	tailStart := len(s) - tailLen
	for tailStart < len(s) && !utf8.ValidString(s[tailStart:]) {
		tailStart++
	}
	return s[:headLen] + marker + s[tailStart:]
}

func stripTurnContextBlocks(s string) string {
	start := strings.Index(s, "<turn-context>")
	if start < 0 {
		return strings.TrimSpace(s)
	}
	var sb strings.Builder
	sb.Grow(len(s))
	pos := 0
	for {
		idx := strings.Index(s[pos:], "<turn-context>")
		if idx < 0 {
			sb.WriteString(s[pos:])
			break
		}
		absStart := pos + idx
		sb.WriteString(s[pos:absStart])
		end := strings.Index(s[absStart:], "</turn-context>")
		if end < 0 {
			break
		}
		pos = absStart + end + len("</turn-context>")
	}
	return strings.TrimSpace(sb.String())
}

func stripStaleHandoffOpenTasks(s string) string {
	if !strings.Contains(s, "# Handoff Summary") {
		return s
	}
	for _, marker := range []string{"\nOpen tasks:", "\nNext step:", "\n## Open tasks", "\n## Next step"} {
		idx := strings.Index(s, marker)
		if idx < 0 {
			continue
		}
		rest := s[idx+len(marker):]
		// Only trim up to the next section heading ("\n## "), preserving subsequent
		// Constraints, State, Decisions, and Working set sections.
		if nextSec := strings.Index(rest, "\n## "); nextSec >= 0 {
			s = s[:idx] + rest[nextSec:]
		} else {
			s = strings.TrimSpace(s[:idx])
		}
	}
	return strings.TrimSpace(s)
}

func isSyntheticCompactionAckAssistant(content string) bool {
	lower := strings.ToLower(strings.TrimSpace(content))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "your context was compacted") ||
		strings.Contains(lower, "the previous message contains a summary") ||
		strings.Contains(lower, "context has been compacted")
}

func isToolCallOnlyAssistant(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "[tool_call") {
			continue
		}
		openIdx := strings.Index(l, "(")
		if openIdx > 0 && strings.HasSuffix(l, ")") {
			fnName := l[:openIdx]
			validFn := true
			for _, r := range fnName {
				if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == ':' || r == '.') {
					validFn = false
					break
				}
			}
			if validFn {
				continue
			}
		}
		return false
	}
	return true
}

const (
	// acpCompactionTriggerTokens is the ACP-session input-token watermark (~225K) at which
	// the gateway rotates the ACP session onto a locally compacted history on the next
	// turn, before agy_acp_server.par (`localharness`) hits its internal ~255K-260K
	// CHECKPOINT threshold.
	acpCompactionTriggerTokens int64 = 225000
	// maxCompactionSystemPromptBytes caps oversized raw conversation history stuffed into
	// SystemPrompt during a client compaction request (~120KB ≈ ~35K tokens) so the
	// compaction turn itself finishes in a single fast step without triggering a double
	// internal ACP CHECKPOINT.
	maxCompactionSystemPromptBytes = 120 * 1024
)

// IsClientCompactionRequest detects when a northbound client (Sailry, Codex CLI, Claude Code, etc.)
// is executing its own context compaction / handoff summary request.
func IsClientCompactionRequest(canReq *adapter.CanonicalRequest) bool {
	if canReq == nil || len(canReq.ToolResults) > 0 {
		return false
	}
	var lastPrompt string
	for _, b := range canReq.PromptBlocks {
		if strings.TrimSpace(b.Text) != "" {
			lastPrompt += b.Text + "\n"
		}
	}
	if lastPrompt == "" && len(canReq.Messages) > 0 {
		lastPrompt = canReq.Messages[len(canReq.Messages)-1].Content
	}
	lowerPrompt := strings.ToLower(strings.TrimSpace(lastPrompt))
	lowerSys := strings.ToLower(canReq.SystemPrompt)

	if strings.Contains(lowerPrompt, "summarize the conversation history provided in the system prompt") ||
		strings.Contains(lowerPrompt, "summarize the conversation so far") ||
		strings.Contains(lowerPrompt, "create a handoff summary") ||
		strings.Contains(lowerPrompt, "compact the conversation") {
		return true
	}
	if (strings.Contains(lowerSys, "writing a handoff for yourself") ||
		strings.Contains(lowerSys, "conversation has reached its context budget") ||
		strings.Contains(lowerSys, "context compaction")) &&
		len(canReq.Tools) == 0 {
		return true
	}
	return false
}

// IsPostCompactionResumeRequest detects the first turn after a client has completed
// its own context compaction and replaced the prior history with a handoff summary.
func IsPostCompactionResumeRequest(canReq *adapter.CanonicalRequest) bool {
	if canReq == nil || len(canReq.ToolResults) > 0 {
		return false
	}
	// Only the first turn after the summary counts: once a substantive assistant reply
	// follows the handoff summary, the conversation has already resumed. Synthetic
	// transition messages ("Your context was compacted...") are skipped.
	for i := len(canReq.Messages) - 1; i >= 0; i-- {
		m := canReq.Messages[i]
		if strings.EqualFold(strings.TrimSpace(m.Role), "assistant") {
			if isSyntheticCompactionAckAssistant(m.Content) {
				continue
			}
			break
		}
		if strings.Contains(m.Content, "# Resuming from a compaction") ||
			strings.Contains(m.Content, "# Handoff Summary") {
			return true
		}
	}
	for _, b := range canReq.PromptBlocks {
		if strings.Contains(b.Text, "# Resuming from a compaction") ||
			strings.Contains(b.Text, "# Handoff Summary") {
			return true
		}
	}
	return false
}

// compactOversizedClientCompactionSystemPrompt bounds a massive raw conversation dump
// inside canReq.SystemPrompt during a client compaction turn so `agy acp` does not
// exceed its 260K internal CHECKPOINT threshold while summarizing for the client.
func compactOversizedClientCompactionSystemPrompt(sys string) string {
	cleaned := stripTurnContextBlocks(sys)
	if len(cleaned) <= maxCompactionSystemPromptBytes {
		return cleaned
	}
	headBudget := 36 * 1024
	tailBudget := maxCompactionSystemPromptBytes - headBudget - 128
	for headBudget > 0 && !utf8.ValidString(cleaned[:headBudget]) {
		headBudget--
	}
	tailStart := len(cleaned) - tailBudget
	for tailStart < len(cleaned) && !utf8.ValidString(cleaned[tailStart:]) {
		tailStart++
	}
	return cleaned[:headBudget] + "\n\n...[middle of raw conversation history omitted to stay within compaction budget]...\n\n" + cleaned[tailStart:]
}

// acpCompactionWatermark returns the ACP-session token count at which the gateway
// compacts locally: acpCompactionTriggerTokens, or 85% of a smaller client window.
func acpCompactionWatermark(canReq *adapter.CanonicalRequest) int64 {
	watermark := acpCompactionTriggerTokens
	if canReq != nil && canReq.ContextWindow > 0 {
		if winTrigger := (canReq.ContextWindow * 85) / 100; winTrigger > 0 && winTrigger < watermark {
			watermark = winTrigger
		}
	}
	return watermark
}

// needsLocalACPCompaction reports whether the ACP session crossed the compaction
// watermark and the client sent enough history to rebuild it locally.
func needsLocalACPCompaction(canReq *adapter.CanonicalRequest, sess *acp.ManagedSession) bool {
	if canReq == nil || sess == nil || len(canReq.Messages) <= 1 {
		return false
	}
	requested, _ := sess.GetClientCompactionState()
	lastIn, _ := sess.GetLastTokenUsage()
	return requested || lastIn >= acpCompactionWatermark(canReq)
}

// computeNorthboundUsageInputTokens returns the input token count to report in the
// northbound SSE/JSON `usage` object. The real ACP usage is reported as-is; crossing
// the compaction watermark only marks the session so the next turn rotates it onto a
// locally compacted history (BuildPromptBlocksWithHistory) instead of forcing the
// client into a slow model-generated compaction.
// When `stepInfo.inputTokens` is not yet flushed to `transcript.jsonl` during a fast tool call,
// current session tokens are estimated from `sess.GetLastTokenUsage() + estimateTurnDeltaTokens(canReq)`
// so the client never sees an artificially tiny delta token count (e.g. 3K instead of 230K).
func computeNorthboundUsageInputTokens(
	canReq *adapter.CanonicalRequest,
	sess *acp.ManagedSession,
	effectivePromptTokens int64,
	cachedTokens int64,
) (reportedInputTokens int64, reportedCachedTokens int64) {
	reportedInputTokens = effectivePromptTokens
	reportedCachedTokens = cachedTokens
	if canReq == nil || sess == nil {
		return reportedInputTokens, reportedCachedTokens
	}
	if IsClientCompactionRequest(canReq) {
		return reportedInputTokens, reportedCachedTokens
	}

	lastIn, lastCached := sess.GetLastTokenUsage()
	if reportedInputTokens < lastIn && lastIn > 0 {
		delta := estimateTurnDeltaTokens(canReq)
		reportedInputTokens = lastIn + delta
		if reportedCachedTokens == 0 && lastCached > 0 {
			reportedCachedTokens = lastCached
		}
	}

	alreadyRequested, _ := sess.GetClientCompactionState()
	if reportedInputTokens >= acpCompactionWatermark(canReq) && !alreadyRequested {
		sess.SetClientCompactionState(true, 0)
	}
	if reportedCachedTokens > reportedInputTokens {
		reportedCachedTokens = reportedInputTokens
	}
	return reportedInputTokens, reportedCachedTokens
}

const (
	// clientCompactionModeEnv selects how client compaction requests are answered:
	// "local" (default) builds the handoff in the gateway without any model call;
	// "model" forwards the request to an ACP session with host-isolation rules.
	clientCompactionModeEnv = "AG_CLIENT_COMPACTION_MODE"
	maxLocalHandoffBytes    = 32 * 1024
)

// clientCompactionHostIsolationRules keeps agy host details out of model-written handoffs.
const clientCompactionHostIsolationRules = "HANDOFF ISOLATION RULES:\n" +
	"1. Summarize ONLY the client conversation history provided above. The ACP host environment (e.g. `Command Working Directory: /Users`, host skills such as `using-superpowers`, `.gemini` directories, artifact directories, and `transcript.jsonl` paths) is transport plumbing, not part of the user's task.\n" +
	"2. NEVER list host environment details, host skills, or transcript paths as files, pending tasks, open tasks, or next steps.\n" +
	"3. Do NOT call tools. Output the handoff text only."

func localClientCompactionEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(clientCompactionModeEnv)), "model")
}

type compactionHistoryEntry struct {
	role     string // "user" | "assistant" | "tool_call" | "tool_result"
	content  string
	toolName string
}

func isCompactionInstruction(lower string) bool {
	return strings.Contains(lower, "summarize the conversation history provided in the system prompt") ||
		strings.Contains(lower, "summarize the conversation so far") ||
		strings.Contains(lower, "create a handoff summary") ||
		strings.Contains(lower, "compact the conversation")
}

// parseCompactionHistory flattens the history a client sends for compaction. Sailry-style
// clients dump it into the system prompt as `[user]: ...` / `[assistant]: tool_request(name): {...}` /
// `[user]: tool_response: ...` lines; other clients send it as regular messages.
func parseCompactionHistory(canReq *adapter.CanonicalRequest) []compactionHistoryEntry {
	if canReq == nil {
		return nil
	}
	var entries []compactionHistoryEntry
	add := func(role, content string) {
		content = strings.TrimSpace(content)
		for {
			lower := strings.ToLower(content)
			if strings.HasPrefix(lower, "[user]:") || strings.HasPrefix(lower, "[assistant]:") {
				content = strings.TrimSpace(content[strings.IndexByte(content, ':')+1:])
				continue
			}
			break
		}
		if content == "" {
			return
		}
		lower := strings.ToLower(content)
		switch {
		case role == "tool" || strings.HasPrefix(lower, "tool_response:") || strings.HasPrefix(lower, "[tool_result:"):
			content = strings.TrimSpace(strings.TrimPrefix(content, "tool_response:"))
			entries = append(entries, compactionHistoryEntry{role: "tool_result", content: content})
		case role == "assistant" && strings.HasPrefix(lower, "tool_request("):
			for _, part := range strings.Split(content, "\ntool_request(") {
				part = strings.TrimPrefix(strings.TrimSpace(part), "tool_request(")
				name, args := part, ""
				if closeIdx := strings.Index(part, "):"); closeIdx >= 0 {
					name, args = part[:closeIdx], strings.TrimSpace(part[closeIdx+2:])
				}
				entries = append(entries, compactionHistoryEntry{role: "tool_call", toolName: strings.TrimSpace(name), content: args})
			}
		case role == "assistant" && isToolCallOnlyAssistant(content):
			name := content
			if openIdx := strings.IndexByte(content, '('); openIdx > 0 {
				name = content[:openIdx]
			}
			entries = append(entries, compactionHistoryEntry{role: "tool_call", toolName: strings.TrimSpace(name), content: content})
		case role == "assistant":
			if !isSyntheticCompactionAckAssistant(content) {
				entries = append(entries, compactionHistoryEntry{role: "assistant", content: content})
			}
		default:
			if !isCompactionInstruction(lower) {
				entries = append(entries, compactionHistoryEntry{role: "user", content: stripTurnContextBlocks(content)})
			}
		}
	}

	sys := stripTurnContextBlocks(canReq.SystemPrompt)
	if idx := strings.Index(sys, "**Conversation History:**"); idx >= 0 {
		sys = sys[idx+len("**Conversation History:**"):]
	}
	curRole := ""
	var cur strings.Builder
	flush := func() {
		if curRole != "" {
			add(curRole, cur.String())
		}
		cur.Reset()
	}
	for _, ln := range strings.Split(sys, "\n") {
		lower := strings.ToLower(strings.TrimSpace(ln))
		switch {
		case strings.HasPrefix(lower, "[user]:"):
			flush()
			curRole = "user"
		case strings.HasPrefix(lower, "[assistant]:"):
			flush()
			curRole = "assistant"
		case strings.HasPrefix(lower, "[tool]:"):
			flush()
			curRole = "tool"
		}
		if curRole != "" {
			cur.WriteString(ln)
			cur.WriteString("\n")
		}
	}
	flush()

	for _, m := range canReq.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "assistant" && role != "tool" {
			role = "user"
		}
		add(role, m.Content)
	}
	return entries
}

func isHostEnvironmentPath(p string) bool {
	switch strings.TrimRight(p, "/") {
	case "", ".", "/Users", "/home", "/tmp", "/private/tmp", "/var/folders":
		return true
	}
	return mentionsHostEnvironment(p)
}

func mentionsHostEnvironment(s string) bool {
	return strings.Contains(s, "/.gemini/") ||
		strings.Contains(s, "transcript.jsonl") ||
		strings.Contains(s, "using-superpowers") ||
		strings.Contains(s, "Command Working Directory:")
}

func isMutatingToolName(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range []string{"edit", "write", "replace", "patch", "create", "delete", "remove", "move", "rename"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func extractToolArgPaths(args string) []string {
	var obj map[string]any
	if json.Unmarshal([]byte(args), &obj) != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"path", "file_path", "filePath", "target_file", "TargetFile", "Path", "paths", "files"} {
		switch v := obj[key].(type) {
		case string:
			out = append(out, strings.TrimSpace(v))
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					out = append(out, strings.TrimSpace(s))
				}
			}
		}
	}
	return out
}

// BuildLocalClientCompactionSummary answers a client compaction request locally in
// well under a millisecond: it extracts the user's messages, the files read/modified
// through tool calls, the latest assistant progress, and recent tool results from the
// history, and renders them as a `# Handoff Summary` without spawning an ACP session.
func BuildLocalClientCompactionSummary(canReq *adapter.CanonicalRequest) string {
	entries := parseCompactionHistory(canReq)

	var userMessages []string
	var priorHandoff string
	seenUser := make(map[string]bool)
	var readFiles, modifiedFiles []string
	seenRead := make(map[string]bool)
	seenModified := make(map[string]bool)
	lastAssistant := ""
	type toolActivity struct {
		name, args, result string
		host               bool
	}
	var activity []toolActivity

	for _, e := range entries {
		switch e.role {
		case "user":
			if strings.Contains(e.content, "# Handoff Summary") {
				priorHandoff = e.content
				continue
			}
			msg := truncateUTF8Middle(e.content, 1200, "\n...[truncated]...\n")
			if !seenUser[msg] {
				seenUser[msg] = true
				userMessages = append(userMessages, msg)
			}
		case "assistant":
			lastAssistant = e.content
		case "tool_call":
			if mentionsHostEnvironment(e.content) {
				activity = append(activity, toolActivity{name: e.toolName, host: true})
				continue
			}
			for _, p := range extractToolArgPaths(e.content) {
				if isHostEnvironmentPath(p) {
					continue
				}
				if isMutatingToolName(e.toolName) {
					if !seenModified[p] {
						seenModified[p] = true
						modifiedFiles = append(modifiedFiles, p)
					}
				} else if !seenRead[p] {
					seenRead[p] = true
					readFiles = append(readFiles, p)
				}
			}
			activity = append(activity, toolActivity{name: e.toolName, args: e.content})
		case "tool_result":
			if n := len(activity); n > 0 && activity[n-1].result == "" {
				activity[n-1].result = e.content
			} else {
				activity = append(activity, toolActivity{name: "tool_result", result: e.content})
			}
			if n := len(activity); mentionsHostEnvironment(e.content) {
				activity[n-1].host = true
			}
		}
	}
	kept := activity[:0]
	for _, a := range activity {
		if !a.host {
			kept = append(kept, a)
		}
	}
	activity = kept

	if len(userMessages) > 12 {
		userMessages = append(userMessages[:1], userMessages[len(userMessages)-11:]...)
	}
	if len(readFiles) > 60 {
		readFiles = readFiles[len(readFiles)-60:]
	}
	if len(activity) > 8 {
		activity = activity[len(activity)-8:]
	}

	var sb strings.Builder
	sb.WriteString("# Handoff Summary\n\n")
	sb.WriteString("Compacted locally by the gateway from the conversation history.\n\n")

	sb.WriteString("## User intent\n")
	switch len(userMessages) {
	case 0:
		sb.WriteString("- Continue the current task.\n")
	case 1:
		sb.WriteString(fmt.Sprintf("- Request: %s\n", userMessages[0]))
	default:
		sb.WriteString(fmt.Sprintf("- Original request: %s\n", userMessages[0]))
		sb.WriteString(fmt.Sprintf("- Latest request: %s\n", userMessages[len(userMessages)-1]))
	}
	sb.WriteString("\n")

	if len(userMessages) > 1 {
		sb.WriteString("## User messages\n")
		for i, m := range userMessages {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, m))
		}
		sb.WriteString("\n")
	}

	latestGoal := "the current task"
	if len(userMessages) > 0 {
		latestGoal = truncateUTF8Middle(userMessages[len(userMessages)-1], 400, " ... ")
	}
	sb.WriteString("Open tasks:\n")
	sb.WriteString(fmt.Sprintf("- Continue the latest user request from the current state: %s\n\n", latestGoal))

	sb.WriteString("## State\n")
	if lastAssistant != "" {
		sb.WriteString("Latest assistant progress:\n")
		sb.WriteString(truncateUTF8Middle(lastAssistant, 4096, "\n...[truncated]...\n"))
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("No assistant conclusion yet; the task was mid tool loop when compacted.\n\n")
	}

	if len(activity) > 0 {
		sb.WriteString("## Recent tool activity\n")
		for _, a := range activity {
			line := "- " + a.name
			if args := strings.TrimSpace(a.args); args != "" {
				line += " " + truncateUTF8Middle(args, 300, " ... ")
			}
			sb.WriteString(line + "\n")
			if res := strings.TrimSpace(a.result); res != "" {
				sb.WriteString("  result: " + strings.ReplaceAll(truncateUTF8Middle(res, 700, " ... "), "\n", "\n  ") + "\n")
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Working set\n")
	if len(modifiedFiles) > 0 {
		sb.WriteString("Files modified:\n")
		for _, f := range modifiedFiles {
			sb.WriteString("- " + f + "\n")
		}
	}
	if len(readFiles) > 0 {
		sb.WriteString("Files already read:\n")
		for _, f := range readFiles {
			sb.WriteString("- " + f + "\n")
		}
	}
	if len(modifiedFiles) == 0 && len(readFiles) == 0 {
		sb.WriteString("- (no files recorded)\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## Constraints\n")
	sb.WriteString("- Files in the working set were already read in this session; do not re-read them unless exact current lines are needed for an edit.\n")
	sb.WriteString("- Do not re-run discovery (find_files / git_status / search) already reflected above.\n")

	if priorHandoff != "" {
		sb.WriteString("\n## Prior handoff\n")
		sb.WriteString(truncateUTF8Middle(strings.TrimSpace(strings.Replace(priorHandoff, "# Handoff Summary", "", 1)), 8*1024, "\n...[truncated]...\n"))
		sb.WriteString("\n")
	}

	return truncateUTF8Middle(strings.TrimSpace(sb.String()), maxLocalHandoffBytes, "\n...[handoff truncated]...\n")
}

// respondLocalClientCompaction writes the locally built handoff in the client's protocol.
func (gs *GatewayServer) respondLocalClientCompaction(w http.ResponseWriter, reqID string, startTime time.Time, canReq *adapter.CanonicalRequest, accountID string, summary string) {
	promptTokens := estimatePromptTokens(canReq)
	compTokens := estimateTokens(summary)
	if compTokens < 1 {
		compTokens = 1
	}
	ttfbMs := time.Since(startTime).Milliseconds()
	if ttfbMs < 1 {
		ttfbMs = 1
	}
	gs.recordAuditWithContext(reqID, canReq, accountID, "", 0, promptTokens, promptTokens, 0, compTokens, ttfbMs, startTime, http.StatusOK, "", "", summary, "")

	if canReq.Stream {
		switch canReq.Protocol {
		case "openai":
			sse := adapter.NewOpenAISSEWriter(w, reqID, canReq.Model)
			sse.WriteStart()
			sse.WriteTextDelta(summary)
			sse.WriteStopWithUsage(promptTokens, 0, compTokens)
		case "openai_responses":
			sse := adapter.NewOpenAIResponsesSSEWriter(w, reqID, canReq.Model)
			sse.WriteStart()
			sse.WriteTextDelta(summary)
			sse.WriteStopWithCache("", summary, promptTokens, 0, compTokens)
		default:
			sse := adapter.NewClaudeSSEWriter(w, reqID, canReq.Model)
			sse.SetInputTokens(promptTokens)
			sse.WriteStart()
			sse.WriteTextDelta(summary)
			sse.WriteStopWithUsage(promptTokens, 0, compTokens)
		}
		return
	}
	switch canReq.Protocol {
	case "openai":
		writeJSON(w, http.StatusOK, adapter.BuildOpenAIJSONResponseMulti(reqID, canReq.Model, "", summary, nil, promptTokens, 0, compTokens))
	case "openai_responses":
		writeJSON(w, http.StatusOK, adapter.BuildOpenAIResponsesJSONResponseMulti(reqID, canReq.Model, "", summary, nil, promptTokens, 0, compTokens))
	default:
		writeJSON(w, http.StatusOK, adapter.BuildClaudeJSONResponseMulti(reqID, canReq.Model, "", summary, nil, promptTokens, 0, compTokens))
	}
}

// BuildPromptBlocksWithHistory constructs a bounded `<conversation_history>` prompt
// when starting a fresh ACP session for an ongoing multi-turn conversation.
// Crucially, `agy_acp_server.par` hard-truncates any `USER_INPUT` exceeding ~184KB
// from the tail (`<truncated N bytes>`), which would destroy the latest turns and
// the final user instruction. We enforce a strict ~100KB budget that:
//  1. Strips bloated `<turn-context>` blocks and obsolete `Open tasks:` from prior handoff summaries.
//  2. Drops intermediate `[TOOL]` and tool-call-only `[ASSISTANT]` scratchpad steps from already-completed
//     prior turns (before the last prose `[ASSISTANT]` response preceding the active `[USER]` request),
//     preventing the model from re-running finished code reviews when asked to fix/commit/restart.
//  3. Preserves the preceding `[ASSISTANT]` final report intact (up to 24KB) and highlights the active
//     `[Current User Goal]` when resuming after a dynamic tool unlock (`tool_search`).
func BuildPromptBlocksWithHistory(canReq *adapter.CanonicalRequest) []acp.PromptBlock {
	if canReq == nil || len(canReq.Messages) == 0 {
		if canReq == nil {
			return nil
		}
		return canReq.PromptBlocks
	}

	maxInitialGoalBytes := 12 * 1024
	maxMiddleBudget := 56 * 1024
	maxFinalTurnBytes := 36 * 1024
	if canReq.ContextWindow >= 400000 {
		maxInitialGoalBytes = 20 * 1024
		maxMiddleBudget = 96 * 1024
		maxFinalTurnBytes = 48 * 1024
	} else if canReq.ContextWindow >= 200000 {
		maxInitialGoalBytes = 16 * 1024
		maxMiddleBudget = 76 * 1024
		maxFinalTurnBytes = 42 * 1024
	}

	type cleanMsg struct {
		role    string
		content string
	}
	var cleaned []cleanMsg
	for _, m := range canReq.Messages {
		role := strings.ToUpper(strings.TrimSpace(m.Role))
		if role == "" {
			role = "USER"
		}
		content := strings.TrimSpace(m.Content)
		if role == "USER" {
			content = stripTurnContextBlocks(content)
		}
		if content == "" {
			continue
		}
		cleaned = append(cleaned, cleanMsg{role: role, content: content})
	}
	if len(cleaned) == 0 {
		return canReq.PromptBlocks
	}

	lastRealUserIdx := -1
	for i := len(cleaned) - 1; i >= 0; i-- {
		if cleaned[i].role == "USER" && !strings.Contains(cleaned[i].content, "# Handoff Summary") {
			lastRealUserIdx = i
			break
		}
	}
	if lastRealUserIdx < 0 {
		for i := len(cleaned) - 1; i >= 0; i-- {
			if cleaned[i].role == "USER" {
				lastRealUserIdx = i
				break
			}
		}
	}

	// Find the last substantive prose ASSISTANT reply before lastRealUserIdx.
	// Any intermediate TOOL calls, tool-call-only ASSISTANT messages, or synthetic
	// compaction ack messages prior to that reply do not count as task completion.
	lastProseAsstBeforeUserIdx := -1
	if lastRealUserIdx > 0 {
		for i := lastRealUserIdx - 1; i >= 0; i-- {
			if cleaned[i].role == "ASSISTANT" &&
				!isToolCallOnlyAssistant(cleaned[i].content) &&
				!isSyntheticCompactionAckAssistant(cleaned[i].content) {
				lastProseAsstBeforeUserIdx = i
				break
			}
		}
	}

	// Only strip obsolete `Open tasks:` / `Next step:` from a Handoff Summary when a
	// substantive assistant conclusion followed that summary (meaning the prior task
	// finished and the user issued a brand-new follow-up instruction). During an active
	// post-compaction resume turn, preserve the entire `# Handoff Summary` intact.
	if lastProseAsstBeforeUserIdx > 0 {
		for i := 0; i < lastProseAsstBeforeUserIdx; i++ {
			if cleaned[i].role == "USER" {
				cleaned[i].content = stripStaleHandoffOpenTasks(cleaned[i].content)
			}
		}
	}

	lastIdx := len(cleaned) - 1
	lastMsg := cleaned[lastIdx]
	hasTailTurn := lastMsg.role == "USER" || lastMsg.role == "TOOL"

	historySlice := cleaned
	if hasTailTurn {
		historySlice = cleaned[:lastIdx]
	}

	var sb strings.Builder
	sb.WriteString("<conversation_history>\n")

	if len(historySlice) > 0 {
		startRecentFloor := 0
		if historySlice[0].role == "USER" && (lastProseAsstBeforeUserIdx < 0 || !strings.Contains(historySlice[0].content, "# Handoff Summary")) {
			goalBudget := maxInitialGoalBytes
			if strings.Contains(historySlice[0].content, "# Handoff Summary") && goalBudget < 32*1024 {
				goalBudget = 32 * 1024
			}
			goalText := truncateUTF8Middle(historySlice[0].content, goalBudget, "\n...[initial message truncated]...\n")
			sb.WriteString(fmt.Sprintf("[USER]:\n%s\n\n", goalText))
			startRecentFloor = 1
		}

		// Tool outputs of the active task (after the last real user turn, or the whole
		// history when no prior task completed) were just read by the model; keep the
		// most recent ones large enough that a mid-task rebuild (tool_search unlock or
		// local compaction) does not force the model to re-read them.
		const maxActiveTaskTools = 12
		activeTaskTools := 0
		for i := len(historySlice) - 1; i >= startRecentFloor && activeTaskTools < maxActiveTaskTools; i-- {
			if historySlice[i].role == "TOOL" && (i > lastRealUserIdx || lastProseAsstBeforeUserIdx < 0) {
				activeTaskTools++
			}
		}
		activeToolBytes := 1400
		if activeTaskTools > 0 {
			activeToolBytes = (maxMiddleBudget * 3 / 4) / activeTaskTools
			if activeToolBytes > 8000 {
				activeToolBytes = 8000
			} else if activeToolBytes < 1400 {
				activeToolBytes = 1400
			}
		}

		var recentFormatted []string
		usedBytes := 0
		skippedCount := 0
		recentToolCount := 0
		for i := len(historySlice) - 1; i >= startRecentFloor; i-- {
			m := historySlice[i]
			if m.role == "ASSISTANT" && isSyntheticCompactionAckAssistant(m.content) {
				continue
			}
			if lastProseAsstBeforeUserIdx >= 0 && i < lastProseAsstBeforeUserIdx {
				if m.role == "TOOL" || (m.role == "ASSISTANT" && isToolCallOnlyAssistant(m.content)) {
					continue
				}
			}
			inActiveTask := i > lastRealUserIdx || lastProseAsstBeforeUserIdx < 0
			if m.role == "TOOL" && inActiveTask {
				recentToolCount++
			}
			distFromEnd := len(historySlice) - 1 - i
			maxEntryContent := 4000
			switch {
			case strings.Contains(m.content, "# Handoff Summary"):
				maxEntryContent = 32 * 1024
			case i == lastProseAsstBeforeUserIdx:
				maxEntryContent = 24 * 1024
			case i == lastRealUserIdx:
				maxEntryContent = 16 * 1024
			case distFromEnd < 4:
				maxEntryContent = 8000
			case m.role == "TOOL" && inActiveTask && recentToolCount <= maxActiveTaskTools:
				maxEntryContent = activeToolBytes
			case m.role == "TOOL":
				maxEntryContent = 1400
			}
			c := truncateUTF8Middle(m.content, maxEntryContent, "\n...[historical output compressed]...\n")
			entry := fmt.Sprintf("[%s]:\n%s\n\n", m.role, c)
			if usedBytes+len(entry) > maxMiddleBudget && len(recentFormatted) >= 2 {
				skippedCount = i - startRecentFloor + 1
				break
			}
			recentFormatted = append(recentFormatted, entry)
			usedBytes += len(entry)
		}

		if skippedCount > 0 {
			sb.WriteString(fmt.Sprintf("[... %d earlier historical messages omitted to stay within context window ...]\n\n", skippedCount))
		}
		for i := len(recentFormatted) - 1; i >= 0; i-- {
			sb.WriteString(recentFormatted[i])
		}
	}

	const historyContinuityNote = "[Context Continuity Note: All files, tool outputs, and # Handoff Summary entries in <conversation_history> above have already been read in this session. Continue directly from this state—do NOT redundantly re-run find_files / git_status or re-read already summarized files.]\n\n"
	if hasTailTurn {
		sb.WriteString("</conversation_history>\n\n")
		sb.WriteString(historyContinuityNote)
		tailContent := truncateUTF8Middle(lastMsg.content, maxFinalTurnBytes, "\n...[middle of large input truncated]...\n")
		if lastMsg.role == "TOOL" {
			if lastRealUserIdx >= 0 && lastRealUserIdx < lastIdx {
				activeGoal := truncateUTF8Middle(cleaned[lastRealUserIdx].content, 4096, "\n...[truncated]...\n")
				sb.WriteString("[Current User Goal]\n")
				sb.WriteString(activeGoal)
				sb.WriteString("\n\n")
			}
			sb.WriteString("[Tool Execution Result]\n")
			sb.WriteString(tailContent)
			sb.WriteString("\n\nPlease continue fulfilling the Current User Goal based on the tool execution result above. Do NOT re-run already completed tasks from earlier turns.")
		} else {
			sb.WriteString(tailContent)
		}
	} else {
		sb.WriteString("</conversation_history>\n\n")
		sb.WriteString(historyContinuityNote)
		sb.WriteString("Please continue.")
	}

	blocks := []acp.PromptBlock{{Type: "text", Text: sb.String()}}
	for _, b := range canReq.PromptBlocks {
		if b.Type != "text" {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

func buildPromptBlocksWithHistory(canReq *adapter.CanonicalRequest) []acp.PromptBlock {
	return BuildPromptBlocksWithHistory(canReq)
}

// ComputeNorthboundUsageInputTokens is exported for unit testing the client-first,
// ACP-fallback compaction watermark calculation.
func ComputeNorthboundUsageInputTokens(
	canReq *adapter.CanonicalRequest,
	sess *acp.ManagedSession,
	effectivePromptTokens int64,
	cachedTokens int64,
) (int64, int64) {
	return computeNorthboundUsageInputTokens(canReq, sess, effectivePromptTokens, cachedTokens)
}

func formatMultiProtocolModelEntry(m acp.ModelInfo, ts time.Time) map[string]any {
	if ts.IsZero() {
		ts = time.Now()
	}
	displayName := strings.TrimSpace(m.Name)
	if displayName == "" {
		displayName = m.ModelID
	}
	thinkingLevels := m.ThinkingLevels
	if len(thinkingLevels) == 0 {
		thinkingLevels = []string{"low", "medium", "high"}
	}
	defaultLevel := strings.TrimSpace(m.DefaultThinkingLevel)
	if defaultLevel == "" {
		defaultLevel = "high"
	}
	hasDefault := false
	for _, lv := range thinkingLevels {
		if strings.EqualFold(lv, defaultLevel) {
			defaultLevel = lv
			hasDefault = true
			break
		}
	}
	if !hasDefault && len(thinkingLevels) > 0 {
		defaultLevel = thinkingLevels[len(thinkingLevels)-1]
	}

	supportedReasoningLevels := make([]map[string]string, 0, len(thinkingLevels))
	for _, lv := range thinkingLevels {
		lv = strings.TrimSpace(lv)
		if lv == "" {
			continue
		}
		supportedReasoningLevels = append(supportedReasoningLevels, map[string]string{
			"effort": lv,
		})
	}

	lowerID := strings.ToLower(m.ModelID)
	hasVision := !strings.Contains(lowerID, "gpt-oss")
	inputModalities := []string{"text", "image"}
	modalityStr := "text+image->text"
	if !hasVision {
		inputModalities = []string{"text"}
		modalityStr = "text->text"
	}
	outputModalities := []string{"text"}

	contextWindow := 1048576
	maxOutputTokens := 65536
	switch {
	case strings.Contains(lowerID, "gpt-oss"):
		contextWindow = 128000
		maxOutputTokens = 32768
	case strings.Contains(lowerID, "claude"):
		contextWindow = 200000
		maxOutputTokens = 64000
	}

	reasoningValues := make([]string, 0, len(thinkingLevels)+1)
	reasoningValues = append(reasoningValues, "default")
	for _, lv := range thinkingLevels {
		lv = strings.TrimSpace(lv)
		if lv != "" && !strings.EqualFold(lv, "default") {
			reasoningValues = append(reasoningValues, lv)
		}
	}

	textCapabilities := map[string]any{
		"operations":       []string{"text_generation"},
		"streaming":        true,
		"inputModalities":  inputModalities,
		"outputModalities": outputModalities,
		"imageInput":       hasVision,
		"contextWindow":    contextWindow,
		"maxOutputTokens":  maxOutputTokens,
		"temperature": map[string]any{
			"min": 0,
			"max": 2,
		},
		"reasoning": map[string]any{
			"supported": len(thinkingLevels) > 0,
			"control":   "reasoning_effort",
			"values":    reasoningValues,
		},
	}

	capabilities := map[string]any{
		"operations":       []string{"text_generation"},
		"streaming":        true,
		"inputModalities":  inputModalities,
		"outputModalities": outputModalities,
		"imageInput":       hasVision,
		"contextWindow":    contextWindow,
		"maxOutputTokens":  maxOutputTokens,
		"temperature": map[string]any{
			"min": 0,
			"max": 2,
		},
		"reasoning": map[string]any{
			"supported": len(thinkingLevels) > 0,
			"control":   "reasoning_effort",
			"values":    reasoningValues,
		},
		"vision":           hasVision,
		"tool_call":        true,
		"function_calling": true,
		"text":             textCapabilities,
	}

	entry := map[string]any{
		"id":                          m.ModelID,
		"object":                      "model",
		"type":                        "model",
		"created":                     ts.Unix(),
		"created_at":                  ts.UTC().Format(time.RFC3339),
		"owned_by":                    "antigravity-acp",
		"display_name":                displayName,
		"displayName":                 displayName,
		"name":                        displayName,
		"services":                    []string{"text"},
		"operations":                  []string{"text_generation"},
		"streaming":                   true,
		"thinking_levels":             thinkingLevels,
		"default_thinking_level":      defaultLevel,
		"supported_reasoning_levels":  supportedReasoningLevels,
		"default_reasoning_level":     defaultLevel,
		"supported_reasoning_efforts": thinkingLevels,
		"reasoning":                   true,
		"thinking":                    len(thinkingLevels) > 0,
		"tool_call":                   true,
		"vision":                      hasVision,
		"image_input":                 hasVision,
		"imageInput":                  hasVision,
		"input_modalities":            inputModalities,
		"output_modalities":           outputModalities,
		"inputModalities":             inputModalities,
		"outputModalities":            outputModalities,
		"context_window":              contextWindow,
		"contextWindow":               contextWindow,
		"context_length":              contextWindow,
		"max_input_tokens":            contextWindow,
		"inputTokenLimit":             contextWindow,
		"max_output_tokens":           maxOutputTokens,
		"maxOutputTokens":             maxOutputTokens,
		"max_completion_tokens":       maxOutputTokens,
		"outputTokenLimit":            maxOutputTokens,
		"maxTemperature":              2.0,
		"supportedGenerationMethods":  []string{"generateContent", "streamGenerateContent"},
		"capabilities":                capabilities,
		"architecture": map[string]any{
			"modality":          modalityStr,
			"input_modalities":  inputModalities,
			"output_modalities": outputModalities,
		},
	}
	if strings.TrimSpace(m.Description) != "" {
		entry["description"] = m.Description
	}
	return entry
}

func (gs *GatewayServer) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")

	qRefresh := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("refresh")))
	forceRefresh := qRefresh == "1" || qRefresh == "true"

	preferredAccID, _ := gs.resolvePreferredAccount(r)
	rawModels, cachedAt, err := gs.pool.GetOrDiscoverModels(r.Context(), preferredAccID, forceRefresh)
	if cachedAt.IsZero() {
		cachedAt = time.Now()
	}
	models := gs.pool.FilterAllowedModels(preferredAccID, rawModels)

	// Check if single-model lookup: /v1/models/{model_id}, /v1/v1/models/{model_id}, /v1beta/models/{model_id}, or /models/{model_id}
	isGeminiBeta := strings.HasPrefix(r.URL.Path, "/v1beta/models")
	var targetModelID string
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1beta/models/"):
		targetModelID = strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1beta/models/"), "/")
	case strings.HasPrefix(r.URL.Path, "/v1/v1/models/"):
		targetModelID = strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/v1/models/"), "/")
	case strings.HasPrefix(r.URL.Path, "/v1/models/"):
		targetModelID = strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/models/"), "/")
	case strings.HasPrefix(r.URL.Path, "/models/"):
		targetModelID = strings.Trim(strings.TrimPrefix(r.URL.Path, "/models/"), "/")
	}

	if targetModelID != "" {
		cleanTarget, _ := adapter.SplitModelAndThinkingSuffix(targetModelID, "")
		for _, m := range models {
			if m.ModelID == targetModelID || m.ModelID == cleanTarget {
				entry := formatMultiProtocolModelEntry(m, cachedAt)
				if isGeminiBeta {
					entry["name"] = "models/" + m.ModelID
				}
				writeJSON(w, http.StatusOK, entry)
				return
			}
		}
		for _, m := range rawModels {
			if m.ModelID == targetModelID || m.ModelID == cleanTarget {
				writeJSONError(w, http.StatusForbidden, fmt.Sprintf("model %q is disabled on target ACP account", targetModelID))
				return
			}
		}
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("model %q not found in ACP backend", targetModelID))
		return
	}

	data := make([]map[string]any, 0, len(models))
	geminiModels := make([]map[string]any, 0, len(models))
	var firstID, lastID string
	for i, m := range models {
		if i == 0 {
			firstID = m.ModelID
		}
		lastID = m.ModelID
		entry := formatMultiProtocolModelEntry(m, cachedAt)
		data = append(data, entry)

		gemEntry := make(map[string]any, len(entry)+1)
		for k, v := range entry {
			gemEntry[k] = v
		}
		gemEntry["name"] = "models/" + m.ModelID
		geminiModels = append(geminiModels, gemEntry)
	}

	_, _, ttl := gs.pool.ModelsCacheInfo()
	resp := map[string]any{
		"object":            "list",
		"data":              data,
		"models":            geminiModels,
		"first_id":          firstID,
		"last_id":           lastID,
		"has_more":          false,
		"cached_at":         cachedAt.UTC().Format(time.RFC3339),
		"cache_ttl_seconds": int(ttl.Seconds()),
	}
	if err != nil && len(data) == 0 {
		resp["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func extractClientSessionHeader(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, h := range []string{
		"X-ACP-Session-ID",
		"X-Session-ID",
		"Session-ID",
		"Session_id",
		"X-Conversation-ID",
		"Conversation-ID",
		"Conversation_id",
		"OpenAI-Conversation-ID",
		"X-Thread-ID",
		"X-Client-Session-ID",
	} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	return ""
}

func extractSessionTitleHeader(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, h := range []string{"X-Session-Title", "X-Conversation-Title"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	return ""
}

func (gs *GatewayServer) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sessionHeader := extractClientSessionHeader(r)
	canReq, err := adapter.ParseOpenAIResponsesRequest(body, sessionHeader)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if canReq.SessionTitle == "" {
		canReq.SessionTitle = extractSessionTitleHeader(r)
	}
	gs.executeNorthboundTurn(w, r, canReq)
}

func (gs *GatewayServer) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	sessionHeader := extractClientSessionHeader(r)
	canReq, err := adapter.ParseOpenAIRequest(body, sessionHeader)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if canReq.SessionTitle == "" {
		canReq.SessionTitle = extractSessionTitleHeader(r)
	}
	gs.executeNorthboundTurn(w, r, canReq)
}

func (gs *GatewayServer) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeProtocolError(w, "claude", http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024*1024)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeProtocolError(w, "claude", http.StatusBadRequest, err.Error())
		return
	}
	sessionHeader := extractClientSessionHeader(r)
	canReq, err := adapter.ParseClaudeRequest(body, sessionHeader)
	if err != nil {
		writeProtocolError(w, "claude", http.StatusBadRequest, err.Error())
		return
	}
	if canReq.SessionTitle == "" {
		canReq.SessionTitle = extractSessionTitleHeader(r)
	}
	gs.executeNorthboundTurn(w, r, canReq)
}

// resolvePreferredAccount picks the account for a request. An API key bound to an
// account always wins; clients can only choose an account (X-ACP-Account-ID header
// or ?account_id=) when their key is unbound or they use admin credentials.
func (gs *GatewayServer) resolvePreferredAccount(r *http.Request) (preferred string, allowFailover bool) {
	keyRec, hasKey := r.Context().Value(ctxKeyAPIKey).(*storage.APIKey)
	hasKey = hasKey && keyRec != nil
	if hasKey {
		if bound := strings.TrimSpace(keyRec.BoundAccountID); bound != "" && bound != "auto" {
			return bound, false
		}
	}
	requested := strings.TrimSpace(r.Header.Get("X-ACP-Account-ID"))
	if requested == "" {
		requested = strings.TrimSpace(r.URL.Query().Get("account_id"))
	}
	if requested != "" {
		return requested, requested == "auto"
	}
	if !hasKey && gs.store != nil {
		if primaryKey, ok := gs.store.GetPrimaryAPIKey(); ok && primaryKey != nil {
			if strings.TrimSpace(primaryKey.BoundAccountID) != "" && primaryKey.BoundAccountID != "auto" {
				return strings.TrimSpace(primaryKey.BoundAccountID), false
			}
		}
	}
	return "", true
}

type reqTurnMeta struct {
	policy      config.PromptPolicyConfig
	sessionMode string
	keyName     string
	clientLabel string
	thinking    string
	prepMs      int64
	stepIndex   int
	subToolStep bool
	sess        *acp.ManagedSession
}

func extractContextWindowHeader(r *http.Request) int64 {
	if r == nil {
		return 0
	}
	for _, h := range []string{
		"X-ACP-Context-Window",
		"X-ACP-Context-Size",
		"X-Context-Window",
		"X-Context-Size",
		"X-Model-Context-Window",
		"X-Max-Input-Tokens",
	} {
		val := strings.TrimSpace(r.Header.Get(h))
		if val == "" {
			continue
		}
		var parsed int64
		if _, err := fmt.Sscanf(val, "%d", &parsed); err == nil && parsed > 0 {
			return parsed
		}
	}
	if r.URL != nil {
		q := r.URL.Query()
		for _, k := range []string{"context_size", "context_window"} {
			val := strings.TrimSpace(q.Get(k))
			if val == "" {
				continue
			}
			var parsed int64
			if _, err := fmt.Sscanf(val, "%d", &parsed); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

var activeReqTurnMeta sync.Map

func detectRequestClientLabel(r *http.Request, canReq *adapter.CanonicalRequest, protocol string, keyName string) string {
	if r != nil {
		for _, hdr := range []string{"X-Client-Name", "X-App-Name", "X-Title"} {
			if v := strings.TrimSpace(r.Header.Get(hdr)); v != "" {
				return v
			}
		}
		ua := strings.ToLower(strings.TrimSpace(r.Header.Get("User-Agent")))
		switch {
		case strings.Contains(ua, "sailry"):
			return "Sailry"
		case strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude-code"):
			return "Claude Code"
		case strings.Contains(ua, "cursor"):
			return "Cursor"
		case strings.Contains(ua, "cline"):
			return "Cline"
		case strings.Contains(ua, "roo"):
			return "Roo Code"
		case strings.Contains(ua, "opencode"):
			return "OpenCode"
		case strings.Contains(ua, "codex"):
			return "Codex CLI"
		}
	}
	if canReq != nil {
		if strings.Contains(canReq.SystemPrompt, "You are Sailry") || strings.Contains(canReq.SystemPrompt, "data from Sailry") {
			return "Sailry"
		}
		for i := len(canReq.Messages) - 1; i >= 0; i-- {
			if strings.Contains(canReq.Messages[i].Content, "data from Sailry") || strings.Contains(canReq.Messages[i].Content, "<turn-context>") {
				return "Sailry"
			}
		}
	}
	if strings.TrimSpace(keyName) != "" {
		return strings.TrimSpace(keyName)
	}
	p := strings.ToLower(strings.TrimSpace(protocol))
	switch {
	case strings.Contains(p, "responses"):
		return "Responses API"
	case strings.Contains(p, "claude") || strings.Contains(p, "anthropic"):
		return "Claude API"
	case strings.Contains(p, "openai"):
		return "OpenAI API"
	case strings.Contains(p, "acp"):
		return "ACP Studio"
	}
	return "API Client"
}

func (gs *GatewayServer) resolveRequestPolicy(r *http.Request) (config.PromptPolicyConfig, string, string) {
	policy := gs.cfg.GetPromptPolicy()
	sessionMode := gs.cfg.GetSandboxPolicy().SessionMode
	var keyName string
	keyRec, ok := r.Context().Value(ctxKeyAPIKey).(*storage.APIKey)
	if (!ok || keyRec == nil) && gs.store != nil {
		keyRec, _ = gs.store.GetPrimaryAPIKey()
	}
	if keyRec != nil {
		keyName = strings.TrimSpace(keyRec.Name)
		if strings.TrimSpace(keyRec.PromptMode) != "" {
			policy.Mode = strings.TrimSpace(keyRec.PromptMode)
		}
		policy.GlobalPrependPrompt = keyRec.GlobalPrependPrompt
		policy.DisableBuiltinTools = keyRec.DisableBuiltinTools
		if strings.TrimSpace(keyRec.SessionMode) != "" {
			sessionMode = strings.TrimSpace(keyRec.SessionMode)
		}
	}
	return policy, sessionMode, keyName
}

var thinkingLevelOrder = []string{"minimal", "low", "medium", "high", "extra_high"}

func thinkingLevelRank(level string) int {
	l := strings.ToLower(strings.TrimSpace(level))
	if l == "max" || l == "xhigh" {
		l = "extra_high"
	}
	for i, v := range thinkingLevelOrder {
		if v == l {
			return i
		}
	}
	return -1
}

// resolveThinkingLevel honours the client's requested level. Unset defaults to
// "high"; a level the model does not offer maps to the nearest supported one
// (preferring higher).
func (gs *GatewayServer) resolveThinkingLevel(model, requested string) string {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		return "high"
	}
	var supported []string
	if gs.pool != nil && model != "" {
		for _, m := range gs.pool.ListModels() {
			if strings.EqualFold(m.ModelID, model) {
				supported = m.ThinkingLevels
				break
			}
		}
	}
	if len(supported) == 0 {
		if requested == "medium" && strings.HasPrefix(strings.ToLower(model), "gemini-") {
			return "high"
		}
		return requested
	}
	want := thinkingLevelRank(requested)
	best, bestRank := "", -1
	below, belowRank := "", -1
	for _, s := range supported {
		if strings.EqualFold(s, requested) {
			return s
		}
		r := thinkingLevelRank(s)
		if r < 0 || want < 0 {
			continue
		}
		if r > want && (bestRank < 0 || r < bestRank) {
			best, bestRank = s, r
		}
		if r < want && r > belowRank {
			below, belowRank = s, r
		}
	}
	if best != "" {
		return best
	}
	if below != "" {
		return below
	}
	return requested
}

func (gs *GatewayServer) executeNorthboundTurn(w http.ResponseWriter, r *http.Request, canReq *adapter.CanonicalRequest) {
	startTime := time.Now()
	idPrefix := "req_"
	if canReq.Protocol == "openai_responses" {
		idPrefix = "resp_"
	}
	reqID := genReqID(idPrefix)
	ctx := r.Context()
	if keyRec, ok := ctx.Value(ctxKeyAPIKey).(*storage.APIKey); ok && keyRec != nil {
		canReq.SessionNamespace = fmt.Sprintf("k%d:", keyRec.ID)
	}

	preferredAccID, allowFailover := gs.resolvePreferredAccount(r)
	if canReq.ContextWindow <= 0 {
		if hdrWin := extractContextWindowHeader(r); hdrWin > 0 {
			canReq.ContextWindow = hdrWin
		}
	}
	// Per-key context-window override: when the client did not specify one, fall back to
	// the window configured on the API key (admin UI presets).
	if canReq.ContextWindow <= 0 {
		if keyRec, ok := ctx.Value(ctxKeyAPIKey).(*storage.APIKey); ok && keyRec != nil && keyRec.ContextWindow > 0 {
			canReq.ContextWindow = keyRec.ContextWindow
		}
	}
	isClientCompaction := IsClientCompactionRequest(canReq)
	isPostCompactionResume := !isClientCompaction && IsPostCompactionResumeRequest(canReq)
	var localHandoff string
	if isClientCompaction {
		if localClientCompactionEnabled() {
			localHandoff = BuildLocalClientCompactionSummary(canReq)
		}
		canReq.SystemPrompt = compactOversizedClientCompactionSystemPrompt(canReq.SystemPrompt)
		canReq.Tools = nil
	}
	promptTokens := estimatePromptTokens(canReq)

	policy, sessionMode, keyName := gs.resolveRequestPolicy(r)
	if isClientCompaction {
		policy.DisableBuiltinTools = true
		sessionMode = "stateless_single_turn"
	}
	clientLabel := detectRequestClientLabel(r, canReq, canReq.Protocol, keyName)
	skills, _ := gs.store.ListSkills(true)
	effectiveSys := canReq.SystemPrompt
	var acpSessionSys string
	if isClientCompaction {
		// Always neutralize the agy host identity for model-written handoffs, even in
		// native_hybrid mode, so host details never end up as pending tasks.
		compactionPolicy := policy
		compactionPolicy.Mode = "pure_passthrough"
		acpSessionSys = adapter.BuildACPSystemPrompt(effectiveSys, compactionPolicy, clientCompactionHostIsolationRules)
	} else {
		effectiveSys = adapter.BuildEffectiveSystemPrompt(canReq.SystemPrompt, policy, skills)
		if effectiveSys != "" {
			canReq.SystemPrompt = effectiveSys
		}
		acpSessionSys = adapter.BuildACPSystemPrompt(effectiveSys, policy, universalThinkingSystemPrompt)
	}

	effectiveThinking := canReq.ThinkingLevel
	if isClientCompaction {
		effectiveThinking = "low"
	} else {
		effectiveThinking = gs.resolveThinkingLevel(canReq.Model, effectiveThinking)
	}
	if canReq.ThinkingLevel == "" {
		canReq.ThinkingLevel = effectiveThinking
	}
	activeReqTurnMeta.Store(reqID, reqTurnMeta{
		policy:      policy,
		sessionMode: sessionMode,
		keyName:     keyName,
		clientLabel: clientLabel,
		thinking:    effectiveThinking,
	})
	defer activeReqTurnMeta.Delete(reqID)

	var sess *acp.ManagedSession
	var worker *acp.WorkerClient
	var acc storage.Account
	var err error
	var sysDelta string
	sameWorker := false

	// Priority 1: If this request carries tool_results, check parked tool call session!
	if len(canReq.ToolResults) > 0 {
		var parkedSess *acp.ManagedSession
		for _, tr := range canReq.ToolResults {
			if tr.CallID == "" {
				continue
			}
			if s := gs.sm.PeekParkedSession(tr.CallID); s != nil {
				parkedSess = s
				break
			}
		}

		var toolsAdded bool
		// Once the ACP session crossed the compaction watermark, rotate it onto a locally
		// compacted history right away instead of waiting for the client to compact.
		needLocalCompaction := false
		if parkedSess != nil {
			toolsAdded, sysDelta = gs.sm.UpdateSessionToolsAndSystem(parkedSess, canReq.Tools, acpSessionSys)
			needLocalCompaction = needsLocalACPCompaction(canReq, parkedSess)
			parkedWorker := parkedSess.GetWorker()
			parkedACPSessID := parkedSess.GetACPSessionID()
			if (toolsAdded || needLocalCompaction) && parkedWorker != nil && parkedACPSessID != "" {
				// Cancel the active prompt BEFORE unblocking tools/call so agy_acp_server.par
				// aborts the stale session immediately upon receiving the HTTP response.
				_ = parkedWorker.CancelSession(parkedACPSessID)
			}
		}

		var resumedSess *acp.ManagedSession
		for i, tr := range canReq.ToolResults {
			if tr.CallID == "" {
				continue
			}
			content := tr.Content
			if !toolsAdded && sysDelta != "" && i == len(canReq.ToolResults)-1 {
				content = content + "\n\n<additional_instructions>\n" + sysDelta + "\n</additional_instructions>"
			}
			s, rErr := gs.sm.ResumeToolCall(tr.CallID, content, tr.IsError)
			if rErr == nil && s != nil {
				resumedSess = s
			}
		}
		if parkedSess != nil {
			gs.sm.AutoResumeRemainingParkedCalls(parkedSess.BridgeID, "Error: tool result omitted by client in parallel batch")
		}

		if resumedSess != nil && resumedSess.GetWorker() != nil {
			if resumedSess != parkedSess && !needLocalCompaction && needsLocalACPCompaction(canReq, resumedSess) {
				needLocalCompaction = true
				if rWorker := resumedSess.GetWorker(); rWorker != nil && resumedSess.GetACPSessionID() != "" {
					_ = rWorker.CancelSession(resumedSess.GetACPSessionID())
				}
			}

			if !toolsAdded && !needLocalCompaction {
				if canReq.SessionTitle != "" {
					resumedSess.SetSessionTitle(canReq.SessionTitle)
				}
				gs.sm.UpdateSessionHash(resumedSess, canReq.ScopedSessionKey(canReq.HistoryHashFull))
				if canReq.Protocol == "openai_responses" {
					gs.sm.BindExplicitKey(resumedSess, canReq.ScopedSessionKey(reqID))
				}
				if v, ok := activeReqTurnMeta.Load(reqID); ok {
					if m, ok2 := v.(reqTurnMeta); ok2 {
						m.prepMs = time.Since(startTime).Milliseconds()
						m.sess = resumedSess
						activeReqTurnMeta.Store(reqID, m)
					}
				}
				gs.streamOrCollectWorkerOutput(ctx, w, reqID, startTime, canReq, resumedSess, resumedSess.GetPromptDoneCh())
				return
			}

			// New tools were dynamically unlocked or the session crossed the compaction
			// watermark. Wait briefly for the cancelled prompt to exit and drain any leftover
			// events, then start a fresh ACP session from BuildPromptBlocksWithHistory.
			if pCh := resumedSess.GetPromptDoneCh(); pCh != nil {
				select {
				case <-pCh:
				case <-time.After(2 * time.Second):
				}
			}
			rWorker := resumedSess.GetWorker()
			for {
				select {
				case <-rWorker.Events():
				default:
					goto drainedParkedEvents
				}
			}
		drainedParkedEvents:
			resumedSess.SetACPSession("", -1)
			if needLocalCompaction {
				resumedSess.ResetTokenUsage()
			}
			sess = resumedSess
			worker = rWorker
			worker.SetState(acp.StateGenerating)
			acc.ID = resumedSess.AccountID
			sameWorker = true
			sysDelta = ""
		}
	}

	// Priority 2: Lookup existing session affinity (controlled by per-key sessionMode)
	effectiveSessionKey := strings.TrimSpace(canReq.SessionHeader)
	if effectiveSessionKey == "" && strings.TrimSpace(canReq.ClientSessionID) != "" {
		effectiveSessionKey = strings.TrimSpace(canReq.ClientSessionID)
	}
	effectiveSessionKey = canReq.ScopedSessionKey(effectiveSessionKey)
	scopedHashBefore := canReq.ScopedSessionKey(canReq.HistoryHashBefore)
	scopedHashFull := canReq.ScopedSessionKey(canReq.HistoryHashFull)
	// If the client initiated context compaction (or sent a new non-tool-result turn while
	// the session still had parked tool calls), immediately abort any parked tool calls so
	// the worker is freed in <50ms instead of hanging for parkTimeout (180s).
	if isClientCompaction || isPostCompactionResume {
		var priorSess *acp.ManagedSession
		if effectiveSessionKey != "" {
			priorSess, _ = gs.sm.LookupSession(preferredAccID, effectiveSessionKey, scopedHashBefore)
		}
		// Without an identified prior session, aborting by account would cancel other
		// clients' tool calls, so only the matched session is touched.
		if priorSess != nil {
			gs.sm.AbortParkedCallsForSession(priorSess, priorSess.AccountID, "Error: tool call aborted because client initiated context compaction")
			if isPostCompactionResume {
				priorSess.SetACPSession("", -1)
				priorSess.ResetTokenUsage()
			}
		}
	}
	if localHandoff != "" {
		gs.respondLocalClientCompaction(w, reqID, startTime, canReq, preferredAccID, localHandoff)
		return
	}
	workerContextSize := gs.pool.ResolveContextSize(canReq.ContextWindow)
	if sess == nil {
		var found bool
		switch sessionMode {
		case "stateless_single_turn":
			found = false
		case "sticky_conversation":
			if effectiveSessionKey != "" {
				sess, found = gs.sm.LookupSession(preferredAccID, effectiveSessionKey, "")
			}
		default:
			sess, found = gs.sm.LookupSession(preferredAccID, effectiveSessionKey, scopedHashBefore)
		}
		if found && sess != nil && gs.sm.HasParkedCallsForSession(sess.BridgeID) {
			gs.sm.AbortParkedCallsForSession(sess, sess.AccountID, "Error: tool call superseded by new user turn")
		}
		if found && sess != nil && canReq.Model != "" && gs.pool.IsModelDisabledForAccount(sess.AccountID, canReq.Model) {
			found = false
			sess = nil
		}
		forkedHistory := found && sess != nil && sessionMode != "sticky_conversation" && gs.sm.IsStaleHistoryHash(sess, scopedHashBefore)
		if found && sess != nil && sess.GetWorker() != nil && sess.GetWorker().ContextSize == workerContextSize && sess.GetWorker().TryMarkBusy() {
			worker = sess.GetWorker()
			acc.ID = sess.AccountID
			sameWorker = true
		} else {
			worker, acc, err = gs.pool.AcquireForAccountWithContextSize(ctx, preferredAccID, allowFailover, canReq.Model, workerContextSize)
			if err != nil {
				status := http.StatusServiceUnavailable
				if strings.Contains(err.Error(), "is disabled") {
					status = http.StatusForbidden
				}
				gs.recordAudit(reqID, canReq, preferredAccID, "", 0, promptTokens, 0, startTime, status, err.Error(), "", "", "")
				writeProtocolError(w, canReq.Protocol, status, fmt.Sprintf("acquire ACP worker failed: %v", err))
				return
			}
		}

		if found && sess != nil {
			var toolsAdded bool
			toolsAdded, sysDelta = gs.sm.UpdateSessionToolsAndSystem(sess, canReq.Tools, acpSessionSys)
			compReq, _ := sess.GetClientCompactionState()
			lastIn, _ := sess.GetLastTokenUsage()
			clientTruncatedHistory := compReq && len(canReq.Messages) > 0 && lastIn > 0 && promptTokens < (lastIn*55)/100
			needLocalCompaction := false
			if isPostCompactionResume || clientTruncatedHistory || forkedHistory {
				// The live ACP session holds turns the client no longer has; rebuild it
				// from the client's history.
				sess.SetACPSession("", -1)
				sysDelta = ""
				if isPostCompactionResume || clientTruncatedHistory {
					sess.ResetTokenUsage()
				}
			} else if needsLocalACPCompaction(canReq, sess) {
				// The ACP session crossed the compaction watermark: rotate it onto a locally
				// compacted history (BuildPromptBlocksWithHistory) on this turn.
				needLocalCompaction = true
			}
			if toolsAdded || !sameWorker || needLocalCompaction {
				// If new tools were added, the session moved to a different worker process,
				// or the session needs local compaction, start a fresh ACP session.
				sess.SetACPSession("", -1)
				sysDelta = ""
			}
			if needLocalCompaction {
				sess.ResetTokenUsage()
			}
			sess = gs.sm.RebindSession(sess, acc.ID, effectiveSessionKey, sess.HistoryHash, worker, sess.GetACPSessionID(), canReq.Tools)
			gs.sm.UpdateSessionHash(sess, scopedHashFull)
		} else {
			sess = gs.sm.RegisterOrMatchSession(acc.ID, effectiveSessionKey, "", worker, "", canReq.Tools)
			_, _ = gs.sm.UpdateSessionToolsAndSystem(sess, canReq.Tools, acpSessionSys)
			gs.sm.UpdateSessionHash(sess, scopedHashFull)
		}
	} else {
		if effectiveSessionKey != "" {
			gs.sm.BindExplicitKey(sess, effectiveSessionKey)
		}
		gs.sm.UpdateSessionHash(sess, scopedHashFull)
	}

	if canReq.SessionTitle != "" && sess != nil {
		sess.SetSessionTitle(canReq.SessionTitle)
	}
	if canReq.Protocol == "openai_responses" {
		gs.sm.BindExplicitKey(sess, canReq.ScopedSessionKey(reqID))
	}
	loopbackBase := gs.getLoopbackBaseURL(r)
	mcpServers := gs.buildSessionMCPServers(loopbackBase, sess.BridgeID)
	eagerServers := []string{"client_tools"}
	if isClientCompaction {
		mcpServers = []acp.MCPServerEndpoint{}
		eagerServers = []string{}
	}

	emptySkills := []string{}
	mcpMaxOutputBytes := 262144 // 256KB so localharness does not spill MCP tool outputs >10KB to unreachable host temp files
	meta := &acp.RequestMeta{
		Antigravity: &acp.AntigravityMeta{
			EagerMCPServers:   eagerServers,
			SkillDirectories:  &emptySkills,
			McpMaxOutputBytes: &mcpMaxOutputBytes,
		},
	}
	meta.Antigravity.SystemPrompt = &acp.SystemPromptMeta{Append: acpSessionSys}
	if policy.DisableBuiltinTools {
		emptySlice := []string{}
		meta.Antigravity.EnabledTools = &emptySlice
	}
	allowedDirs := defaultAllowedWorkspaceDirs(gs.cfg.GetACPPool().AdditionalDirs)

	isNewACPSession := false
	// IMPORTANT: When sess.GetACPSessionID() != "" and sameWorker == true, the ACP session is ALREADY
	// open in memory on this worker process. Do NOT call worker.ResumeSession because session/resume
	// in agy_acp_server.par reloads transcript.jsonl from disk and wipes _meta.antigravity.systemPrompt.append!
	if sess.GetACPSessionID() == "" {
		isNewACPSession = true
		var sessionRes *acp.NewSessionResult
		sessionRes, err = worker.NewSession(ctx, acp.NewSessionParams{
			Cwd:                   sess.WorkspaceDir,
			AdditionalDirectories: allowedDirs,
			MCPServers:            mcpServers,
			Meta:                  meta,
		})
		if err != nil && acp.IsAuthRequiredError(err) && allowFailover {
			gs.pool.MarkAccountAuthRequired(acc.ID)
			_ = gs.store.UpdateAccountStatus(acc.ID, "auth_required", "")
			gs.pool.Release(worker)
			// Failover to next healthy account
			worker2, acc2, err2 := gs.pool.AcquireForAccountWithContextSize(ctx, "", true, canReq.Model, workerContextSize)
			if err2 == nil {
				worker = worker2
				acc = acc2
				sess = gs.sm.RegisterOrMatchSession(acc.ID, effectiveSessionKey, "", worker, "", canReq.Tools)
				_, _ = gs.sm.UpdateSessionToolsAndSystem(sess, canReq.Tools, acpSessionSys)
				gs.sm.UpdateSessionHash(sess, scopedHashFull)
				if canReq.Protocol == "openai_responses" {
					gs.sm.BindExplicitKey(sess, canReq.ScopedSessionKey(reqID))
				}
				mcpServers = gs.buildSessionMCPServers(loopbackBase, sess.BridgeID)
				sessionRes, err = worker.NewSession(ctx, acp.NewSessionParams{
					Cwd:                   sess.WorkspaceDir,
					AdditionalDirectories: allowedDirs,
					MCPServers:            mcpServers,
					Meta:                  meta,
				})
			}
		}
		if err != nil {
			gs.pool.Release(worker)
			status := http.StatusBadGateway
			if acp.IsAuthRequiredError(err) {
				status = http.StatusUnauthorized
				gs.pool.MarkAccountAuthRequired(acc.ID)
				_ = gs.store.UpdateAccountStatus(acc.ID, "auth_required", "")
			}
			gs.recordAudit(reqID, canReq, acc.ID, "", worker.PID, promptTokens, 0, startTime, status, err.Error(), "", "", "")
			writeProtocolError(w, canReq.Protocol, status, err.Error())
			return
		}
		sess.SetACPSession(sessionRes.SessionID, -1)
		if discovered := acp.ExtractModelsFromSessionResult(sessionRes); len(discovered) > 0 {
			gs.pool.UpdateAccountDiscoveredModels(acc.ID, discovered)
		}
	}

	// Set config options: mode, model, thinking_level (only when changed or on a new ACP session)
	acpSessID := sess.GetACPSessionID()
	prevMode, prevModel, prevThinking := sess.GetAppliedConfig()
	var nextMode string
	if sessionMode != "" {
		acpMode := sessionMode
		if acpMode == "stateless_single_turn" || acpMode == "sticky_conversation" {
			acpMode = "yolo"
		}
		nextMode = acpMode
		if isNewACPSession || prevMode != acpMode {
			_ = worker.SetConfigOption(ctx, acpSessID, "mode", acpMode)
		}
	}
	if canReq.Model != "" && (isNewACPSession || prevModel != canReq.Model) {
		_ = worker.SetConfigOption(ctx, acpSessID, "model", canReq.Model)
	}
	if isNewACPSession || prevThinking != effectiveThinking {
		_ = worker.SetConfigOption(ctx, acpSessID, "thinking_level", effectiveThinking)
	}
	sess.SetAppliedConfig(nextMode, canReq.Model, effectiveThinking)

	promptDoneCh := make(chan error, 1)
	sess.SetPromptDoneCh(promptDoneCh)
	sess.SetLastPromptUsage(nil)

	var blocks []acp.PromptBlock
	if isNewACPSession && (len(canReq.Messages) > 1 || len(canReq.ToolResults) > 0) {
		blocks = buildPromptBlocksWithHistory(canReq)
	} else {
		blocks = canReq.PromptBlocks
		if sysDelta != "" {
			deltaBlock := acp.PromptBlock{
				Type: "text",
				Text: "<additional_instructions>\n" + sysDelta + "\n</additional_instructions>",
			}
			blocks = append([]acp.PromptBlock{deltaBlock}, blocks...)
		}
	}
	if len(blocks) == 0 {
		blocks = []acp.PromptBlock{{Type: "text", Text: "Continue"}}
	}
	if v, ok := activeReqTurnMeta.Load(reqID); ok {
		if m, ok2 := v.(reqTurnMeta); ok2 {
			m.prepMs = time.Since(startTime).Milliseconds()
			m.sess = sess
			activeReqTurnMeta.Store(reqID, m)
		}
	}
	sess.SetLastStopReason("")
	go func(sID string) {
		resp, pErr := worker.Call(context.Background(), "session/prompt", acp.PromptParams{
			SessionID: sID,
			Prompt:    blocks,
		})
		if pErr == nil && resp != nil && len(resp.Result) > 0 {
			var pr acp.PromptResult
			if json.Unmarshal(resp.Result, &pr) == nil {
				if pr.Usage != nil {
					sess.SetLastPromptUsage(pr.Usage)
				}
				sess.SetLastStopReason(pr.StopReason)
			}
		}
		promptDoneCh <- pErr
	}(acpSessID)

	gs.streamOrCollectWorkerOutput(ctx, w, reqID, startTime, canReq, sess, promptDoneCh)
}

func (gs *GatewayServer) buildSessionMCPServers(loopbackBase, bridgeID string) []acp.MCPServerEndpoint {
	return []acp.MCPServerEndpoint{
		{
			Name:    "client_tools",
			Type:    "http",
			URL:     fmt.Sprintf("%s/mcp/sessions/%s", strings.TrimRight(loopbackBase, "/"), bridgeID),
			Headers: acp.MCPHeaders{},
		},
	}
}

const (
	thinkStateInit        = 0
	thinkStateInside      = 1
	thinkStatePassthrough = 2
)

type thinkTagPair struct {
	open  string
	close string
}

var supportedThinkTags = []thinkTagPair{
	{open: "⟦REASONING⟧", close: "⟦/REASONING⟧"},
	{open: "<think>", close: "</think>"},
	{open: "<thinking>", close: "</thinking>"},
}

type transcriptStepInfo struct {
	hasTranscriptFile bool
	reasoning         string
	stepIndex         int
	expectedToolCalls int
	inputTokens       int64
	cachedTokens      int64
	outputTokens      int64
}

func estimateTurnDeltaTokens(canReq *adapter.CanonicalRequest) int64 {
	if canReq == nil {
		return 0
	}
	var delta int64
	for _, b := range canReq.PromptBlocks {
		delta += estimateTokens(b.Text)
	}
	for _, tr := range canReq.ToolResults {
		delta += estimateTokens(tr.Content) + 4
	}
	if delta == 0 && len(canReq.Messages) > 0 {
		last := canReq.Messages[len(canReq.Messages)-1]
		delta = estimateTokens(last.Content) + 4
	}
	if delta < 128 {
		delta = 128
	}
	return delta
}

type cachedLastToolStepEntry struct {
	modTimeNano int64
	size        int64
	step        transcriptStepInfo
}

var (
	lastToolStepCacheMu sync.Mutex
	lastToolStepCache   = make(map[string]cachedLastToolStepEntry)
)

func applyMinStepIndexFilter(step transcriptStepInfo, minStepIndex int) transcriptStepInfo {
	if step.stepIndex >= 0 && step.stepIndex <= minStepIndex {
		step.reasoning = ""
		step.expectedToolCalls = 0
		step.outputTokens = 0
	}
	return step
}

func resolveTranscriptPath(dataDir, accountID, acpSessionID string) (string, os.FileInfo, error) {
	if accountID == "" {
		accountID = "default"
	}
	var candidates []string
	if dataDir != "" {
		for _, subDir := range []string{"antigravity-cli", "antigravity-acp"} {
			candidates = append(candidates, filepath.Join(dataDir, "accounts", accountID, ".gemini", subDir, "brain", acpSessionID, ".system_generated", "logs", "transcript.jsonl"))
		}
	}
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		for _, subDir := range []string{"antigravity-cli", "antigravity-acp"} {
			candidates = append(candidates, filepath.Join(userHome, ".gemini", subDir, "brain", acpSessionID, ".system_generated", "logs", "transcript.jsonl"))
		}
	}
	var lastErr error
	for _, p := range candidates {
		st, err := os.Stat(p)
		if err == nil && st.Size() > 0 {
			return p, st, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return "", nil, lastErr
}

func resolveCLIConversationDBPath(dataDir, accountID, acpSessionID string) (string, os.FileInfo, error) {
	if accountID == "" {
		accountID = "default"
	}
	var candidates []string
	if dataDir != "" {
		for _, subDir := range []string{"antigravity-cli", "antigravity-acp"} {
			candidates = append(candidates, filepath.Join(dataDir, "accounts", accountID, ".gemini", subDir, "conversations", acpSessionID+".db"))
		}
	}
	if userHome, err := os.UserHomeDir(); err == nil && userHome != "" {
		for _, subDir := range []string{"antigravity-cli", "antigravity-acp"} {
			candidates = append(candidates, filepath.Join(userHome, ".gemini", subDir, "conversations", acpSessionID+".db"))
		}
	}
	var lastErr error
	for _, p := range candidates {
		st, err := os.Stat(p)
		if err == nil && st.Size() > 0 {
			return p, st, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return "", nil, lastErr
}

func readProtoVarint(buf []byte, pos int) (uint64, int, bool) {
	var val uint64
	var shift uint
	for pos < len(buf) {
		b := buf[pos]
		pos++
		val |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return val, pos, true
		}
		shift += 7
		if shift >= 64 {
			return 0, pos, false
		}
	}
	return 0, pos, false
}

func parseCLIStepMetadataProto(meta []byte) (createdAt time.Time, totalIn, cachedIn, outTok int64) {
	pos := 0
	var uncachedIn, cacheRead int64
	for pos < len(meta) {
		tagWire, nextPos, ok := readProtoVarint(meta, pos)
		if !ok {
			break
		}
		pos = nextPos
		fieldNum := tagWire >> 3
		wireType := tagWire & 0x7
		switch wireType {
		case 0:
			_, nextPos, ok = readProtoVarint(meta, pos)
			if !ok {
				return
			}
			pos = nextPos
		case 1:
			pos += 8
		case 5:
			pos += 4
		case 2:
			ln, nextPos2, ok2 := readProtoVarint(meta, pos)
			if !ok2 || nextPos2+int(ln) > len(meta) {
				return
			}
			sub := meta[nextPos2 : nextPos2+int(ln)]
			pos = nextPos2 + int(ln)
			switch fieldNum {
			case 1: // created_at google.protobuf.Timestamp
				var sec, nanos int64
				sPos := 0
				for sPos < len(sub) {
					tw, np, ok3 := readProtoVarint(sub, sPos)
					if !ok3 {
						break
					}
					sPos = np
					if tw&0x7 == 0 {
						v, np2, ok4 := readProtoVarint(sub, sPos)
						if !ok4 {
							break
						}
						sPos = np2
						if (tw >> 3) == 1 {
							sec = int64(v)
						} else if (tw >> 3) == 2 {
							nanos = int64(v)
						}
					} else {
						break
					}
				}
				if sec > 0 {
					createdAt = time.Unix(sec, nanos).UTC()
				}
			case 9: // ModelUsage submessage: 2=input_tokens (uncached), 3=output_tokens, 5=cache_read_tokens
				sPos := 0
				for sPos < len(sub) {
					tw, np, ok3 := readProtoVarint(sub, sPos)
					if !ok3 {
						break
					}
					sPos = np
					wt := tw & 0x7
					fn := tw >> 3
					if wt == 0 {
						v, np2, ok4 := readProtoVarint(sub, sPos)
						if !ok4 {
							break
						}
						sPos = np2
						switch fn {
						case 2:
							uncachedIn = int64(v)
						case 3:
							outTok = int64(v)
						case 5:
							cacheRead = int64(v)
						}
					} else if wt == 2 {
						l2, np2, ok4 := readProtoVarint(sub, sPos)
						if !ok4 || np2+int(l2) > len(sub) {
							break
						}
						sPos = np2 + int(l2)
					} else if wt == 1 {
						sPos += 8
					} else if wt == 5 {
						sPos += 4
					} else {
						break
					}
				}
			}
		default:
			return
		}
	}
	cachedIn = cacheRead
	totalIn = uncachedIn + cacheRead
	return
}

func parseCLIStepPayloadProto(payload []byte) (thinking string, toolCallCount int) {
	pos := 0
	for pos < len(payload) {
		tagWire, nextPos, ok := readProtoVarint(payload, pos)
		if !ok {
			break
		}
		pos = nextPos
		fieldNum := tagWire >> 3
		wireType := tagWire & 0x7
		switch wireType {
		case 0:
			_, nextPos, ok = readProtoVarint(payload, pos)
			if !ok {
				return
			}
			pos = nextPos
		case 1:
			pos += 8
		case 5:
			pos += 4
		case 2:
			ln, nextPos2, ok2 := readProtoVarint(payload, pos)
			if !ok2 || nextPos2+int(ln) > len(payload) {
				return
			}
			sub := payload[nextPos2 : nextPos2+int(ln)]
			pos = nextPos2 + int(ln)
			if fieldNum == 20 { // PlannerResponse submessage: 3=thinking, 7=tool_calls
				sPos := 0
				for sPos < len(sub) {
					tw, np, ok3 := readProtoVarint(sub, sPos)
					if !ok3 {
						break
					}
					sPos = np
					wt := tw & 0x7
					fn := tw >> 3
					if wt == 0 {
						_, np2, ok4 := readProtoVarint(sub, sPos)
						if !ok4 {
							break
						}
						sPos = np2
					} else if wt == 2 {
						l2, np2, ok4 := readProtoVarint(sub, sPos)
						if !ok4 || np2+int(l2) > len(sub) {
							break
						}
						valBytes := sub[np2 : np2+int(l2)]
						sPos = np2 + int(l2)
						if fn == 3 && thinking == "" {
							thinking = strings.TrimSpace(string(valBytes))
						} else if fn == 7 {
							toolCallCount++
						}
					} else if wt == 1 {
						sPos += 8
					} else if wt == 5 {
						sPos += 4
					} else {
						break
					}
				}
			}
		default:
			return
		}
	}
	return
}

func extractCurrentToolStepInfoFromCLIDB(dataDir, accountID, acpSessionID string, minStepIndex int) (transcriptStepInfo, bool) {
	info := transcriptStepInfo{stepIndex: -1}
	dbPath, st, err := resolveCLIConversationDBPath(dataDir, accountID, acpSessionID)
	if err != nil {
		return info, false
	}
	modNano := st.ModTime().UnixNano()
	size := st.Size()
	if walSt, wErr := os.Stat(dbPath + "-wal"); wErr == nil {
		if walSt.ModTime().UnixNano() > modNano {
			modNano = walSt.ModTime().UnixNano()
		}
		size += walSt.Size()
	}

	lastToolStepCacheMu.Lock()
	if cached, ok := lastToolStepCache[dbPath]; ok && cached.modTimeNano == modNano && cached.size == size {
		step := cached.step
		lastToolStepCacheMu.Unlock()
		return applyMinStepIndexFilter(step, minStepIndex), true
	}
	lastToolStepCacheMu.Unlock()

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(1000)", filepath.ToSlash(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return info, false
	}
	defer db.Close()

	info.hasTranscriptFile = true
	var idx int
	var meta, payload []byte
	err = db.QueryRow(`SELECT idx, metadata, step_payload FROM steps WHERE step_type = 15 ORDER BY idx DESC LIMIT 1`).Scan(&idx, &meta, &payload)
	if err == nil {
		_, totalIn, cachedIn, outTok := parseCLIStepMetadataProto(meta)
		thinking, tcCount := parseCLIStepPayloadProto(payload)
		info.stepIndex = idx
		info.inputTokens = totalIn
		info.cachedTokens = cachedIn
		info.outputTokens = outTok
		info.reasoning = thinking
		info.expectedToolCalls = tcCount
	}

	lastToolStepCacheMu.Lock()
	if len(lastToolStepCache) > 256 {
		lastToolStepCache = make(map[string]cachedLastToolStepEntry, 64)
	}
	lastToolStepCache[dbPath] = cachedLastToolStepEntry{
		modTimeNano: modNano,
		size:        size,
		step:        info,
	}
	lastToolStepCacheMu.Unlock()
	return applyMinStepIndexFilter(info, minStepIndex), true
}

func extractCurrentToolStepInfo(dataDir, accountID, acpSessionID string, minStepIndex int) transcriptStepInfo {
	info := transcriptStepInfo{stepIndex: -1}
	if accountID == "" {
		accountID = "default"
	}
	if dataDir == "" || acpSessionID == "" {
		return info
	}
	transcriptPath, st, err := resolveTranscriptPath(dataDir, accountID, acpSessionID)
	if err != nil {
		if cliInfo, ok := extractCurrentToolStepInfoFromCLIDB(dataDir, accountID, acpSessionID, minStepIndex); ok {
			return cliInfo
		}
		return info
	}
	modNano := st.ModTime().UnixNano()
	size := st.Size()

	lastToolStepCacheMu.Lock()
	if cached, ok := lastToolStepCache[transcriptPath]; ok && cached.modTimeNano == modNano && cached.size == size {
		step := cached.step
		lastToolStepCacheMu.Unlock()
		return applyMinStepIndexFilter(step, minStepIndex)
	}
	lastToolStepCacheMu.Unlock()

	f, err := os.Open(transcriptPath)
	if err != nil {
		return info
	}
	defer f.Close()

	info.hasTranscriptFile = true

	const maxTailBytes = 128 * 1024
	var raw []byte
	if size > maxTailBytes {
		if _, err := f.Seek(size-maxTailBytes, io.SeekStart); err != nil {
			return info
		}
		raw, err = io.ReadAll(f)
		if err != nil || len(raw) == 0 {
			return info
		}
		// Drop partial first line when seeking into the middle of the file
		if nl := strings.IndexByte(string(raw), '\n'); nl >= 0 {
			raw = raw[nl+1:]
		}
	} else {
		raw, err = io.ReadAll(f)
		if err != nil || len(raw) == 0 {
			return info
		}
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.Contains(line, `"PLANNER_RESPONSE"`) {
			continue
		}
		var step struct {
			StepIndex       int    `json:"step_index"`
			Type            string `json:"type"`
			Thinking        string `json:"thinking"`
			InputTokens     int64  `json:"input_tokens"`
			CacheReadTokens int64  `json:"cache_read_tokens"`
			CachedTokens    int64  `json:"cached_tokens"`
			OutputTokens    int64  `json:"output_tokens"`
			Usage           *struct {
				InputTokens     int64 `json:"input_tokens"`
				CacheReadTokens int64 `json:"cache_read_tokens"`
				CachedTokens    int64 `json:"cached_tokens"`
				OutputTokens    int64 `json:"output_tokens"`
			} `json:"usage"`
			ToolCalls []struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal([]byte(line), &step) == nil && step.Type == "PLANNER_RESPONSE" {
			uncachedIn := step.InputTokens
			cacheRead := step.CacheReadTokens
			cachedOther := step.CachedTokens
			outTok := step.OutputTokens
			if step.Usage != nil {
				if uncachedIn == 0 {
					uncachedIn = step.Usage.InputTokens
				}
				if cacheRead == 0 {
					cacheRead = step.Usage.CacheReadTokens
				}
				if cachedOther == 0 {
					cachedOther = step.Usage.CachedTokens
				}
				if outTok == 0 {
					outTok = step.Usage.OutputTokens
				}
			}

			var totalIn, cachedTok int64
			if cacheRead > 0 {
				// In Antigravity ACP transcript.jsonl, input_tokens is uncached input tokens
				// and cache_read_tokens is cached input tokens read from prompt cache.
				cachedTok = cacheRead
				totalIn = uncachedIn + cacheRead
			} else if cachedOther > 0 {
				cachedTok = cachedOther
				if uncachedIn >= cachedOther {
					totalIn = uncachedIn
				} else {
					totalIn = uncachedIn + cachedOther
				}
			} else {
				totalIn = uncachedIn
			}

			info.stepIndex = step.StepIndex
			info.inputTokens = totalIn
			info.cachedTokens = cachedTok
			info.reasoning = strings.TrimSpace(step.Thinking)
			info.expectedToolCalls = len(step.ToolCalls)
			info.outputTokens = outTok
			break
		}
	}

	lastToolStepCacheMu.Lock()
	if len(lastToolStepCache) > 256 {
		lastToolStepCache = make(map[string]cachedLastToolStepEntry, 64)
	}
	lastToolStepCache[transcriptPath] = cachedLastToolStepEntry{
		modTimeNano: modNano,
		size:        size,
		step:        info,
	}
	lastToolStepCacheMu.Unlock()

	return applyMinStepIndexFilter(info, minStepIndex)
}

func longestPartialTagSuffix(s, closeTag string) int {
	maxLen := len(closeTag) - 1
	if len(s) < maxLen {
		maxLen = len(s)
	}
	for k := maxLen; k >= 1; k-- {
		if strings.HasSuffix(s, closeTag[:k]) {
			return k
		}
	}
	return 0
}

func (gs *GatewayServer) streamOrCollectWorkerOutput(
	ctx context.Context,
	w http.ResponseWriter,
	reqID string,
	startTime time.Time,
	canReq *adapter.CanonicalRequest,
	sess *acp.ManagedSession,
	promptDoneCh <-chan error,
) {
	worker := sess.GetWorker()
	acpSessID := sess.GetACPSessionID()
	promptTokens := estimatePromptTokens(canReq)
	keepAliveSec := gs.cfg.Server.SSEKeepAliveSec
	if keepAliveSec <= 0 {
		keepAliveSec = 15
	}
	ticker := time.NewTicker(time.Duration(keepAliveSec) * time.Second)
	defer ticker.Stop()

	var oaSSE *adapter.OpenAISSEWriter
	var oaRespSSE *adapter.OpenAIResponsesSSEWriter
	var clSSE *adapter.ClaudeSSEWriter
	if canReq.Stream {
		switch canReq.Protocol {
		case "openai":
			oaSSE = adapter.NewOpenAISSEWriter(w, reqID, canReq.Model)
			oaSSE.WriteStart()
		case "openai_responses":
			oaRespSSE = adapter.NewOpenAIResponsesSSEWriter(w, reqID, canReq.Model)
			oaRespSSE.WriteStart()
		default:
			clSSE = adapter.NewClaudeSSEWriter(w, reqID, canReq.Model)
			clSSE.SetInputTokens(promptTokens)
			clSSE.WriteStart()
		}
	}

	var fullThinking strings.Builder
	var fullText strings.Builder
	var firstByteMs int64
	var firstOutputByteMs int64

	markFirstByte := func() {
		if firstByteMs == 0 {
			firstByteMs = time.Since(startTime).Milliseconds()
			if firstByteMs < 1 {
				firstByteMs = 1
			}
		}
	}

	markFirstOutputByte := func() {
		markFirstByte()
		if firstOutputByteMs == 0 {
			firstOutputByteMs = time.Since(startTime).Milliseconds()
			if firstOutputByteMs < 1 {
				firstOutputByteMs = 1
			}
		}
	}

	sawNativeThought := false
	thinkState := thinkStateInit
	var prefixBuf string
	var thinkPending string
	closeTag := "⟦/REASONING⟧"
	// On streaming tool-continuation turns, suppress client-facing reasoning blocks
	// so consecutive tool calls across turns fold into one cohesive UI group instead
	// of fragmenting into alternating "Thinking" + 1-tool rows. Full thinking is still
	// captured in fullThinking for audit_logs and token accounting.
	suppressClientThinking := canReq.Stream && len(canReq.ToolResults) > 0

	emitThinking := func(chunk string) {
		if chunk == "" {
			return
		}
		markFirstByte()
		fullThinking.WriteString(chunk)
		// Never emit late thinking deltas over SSE after text output has already started,
		// or during intermediate streaming tool-continuation turns.
		if fullText.Len() > 0 || suppressClientThinking {
			return
		}
		if oaSSE != nil {
			oaSSE.WriteThinkingDelta(chunk)
		} else if oaRespSSE != nil {
			oaRespSSE.WriteThinkingDelta(chunk)
		} else if clSSE != nil {
			clSSE.WriteThinkingDelta(chunk)
		}
	}

	emitText := func(chunk string) {
		if chunk == "" {
			return
		}
		markFirstOutputByte()
		fullText.WriteString(chunk)
		if oaSSE != nil {
			oaSSE.WriteTextDelta(chunk)
		} else if oaRespSSE != nil {
			oaRespSSE.WriteTextDelta(chunk)
		} else if clSSE != nil {
			clSSE.WriteTextDelta(chunk)
		}
	}

	var feedMessageChunk func(chunk string)
	feedThinkInside := func(s string) {
		thinkPending += s
		if idx := strings.Index(thinkPending, closeTag); idx >= 0 {
			if !sawNativeThought {
				emitThinking(strings.TrimRight(thinkPending[:idx], "\r\n"))
			}
			after := strings.TrimLeft(thinkPending[idx+len(closeTag):], " \t\r\n")
			thinkPending = ""
			// Reset to thinkStateInit while no body text has been emitted so consecutive
			// reasoning blocks across internal sub-steps are still parsed rather than leaked.
			thinkState = thinkStateInit
			if after != "" {
				feedMessageChunk(after)
			}
			return
		}
		keep := longestPartialTagSuffix(thinkPending, closeTag)
		safeLen := len(thinkPending) - keep
		if safeLen > 0 {
			// Ensure we don't split a multi-byte UTF-8 rune
			for safeLen > 0 && !utf8.ValidString(thinkPending[:safeLen]) {
				safeLen--
			}
			if safeLen > 0 {
				if !sawNativeThought {
					emitThinking(thinkPending[:safeLen])
				}
				thinkPending = thinkPending[safeLen:]
			}
		}
	}

	feedMessageChunk = func(chunk string) {
		if chunk == "" {
			return
		}
		switch thinkState {
		case thinkStateInit:
			prefixBuf += chunk
			trimmed := strings.TrimLeft(prefixBuf, " \t\r\n")
			if trimmed == "" {
				return
			}
			for _, tag := range supportedThinkTags {
				if strings.HasPrefix(trimmed, tag.open) {
					if !sawNativeThought && fullThinking.Len() > 0 {
						emitThinking("\n")
					}
					thinkState = thinkStateInside
					closeTag = tag.close
					rest := strings.TrimLeft(trimmed[len(tag.open):], "\r\n")
					prefixBuf = ""
					if rest != "" {
						feedThinkInside(rest)
					}
					return
				}
			}
			for _, tag := range supportedThinkTags {
				if strings.HasPrefix(tag.open, trimmed) {
					// Wait for more bytes to disambiguate opening tag
					return
				}
			}
			thinkState = thinkStatePassthrough
			out := prefixBuf
			prefixBuf = ""
			emitText(out)
		case thinkStateInside:
			feedThinkInside(chunk)
		default:
			emitText(chunk)
		}
	}

	flushThinkParser := func() {
		switch thinkState {
		case thinkStateInit:
			if prefixBuf != "" {
				emitText(prefixBuf)
				prefixBuf = ""
			}
			thinkState = thinkStatePassthrough
		case thinkStateInside:
			if thinkPending != "" && !sawNativeThought {
				emitThinking(thinkPending)
			}
			thinkPending = ""
			thinkState = thinkStatePassthrough
		}
	}

	processEvent := func(ev *acp.RPCMessage) {
		if ev == nil || ev.Method != "session/update" {
			return
		}
		var up acp.SessionUpdateParams
		if err := json.Unmarshal(ev.Params, &up); err != nil {
			return
		}
		if up.SessionID != "" && acpSessID != "" && up.SessionID != acpSessID {
			return
		}
		switch up.Update.SessionUpdate {
		case "agent_thought_chunk":
			sawNativeThought = true
			emitThinking(up.Update.Content.Text)
		case "agent_message_chunk":
			feedMessageChunk(up.Update.Content.Text)
		}
	}

	drainEvents := func() {
		for {
			select {
			case ev := <-worker.Events():
				if ev == nil {
					return
				}
				processEvent(ev)
			default:
				return
			}
		}
	}

	for {
		select {
		case ev := <-worker.Events():
			processEvent(ev)

		case parked := <-sess.ToolCallNotifyCh:
			markFirstByte()
			gs.sm.MarkToolCallDispatched(parked.CallID)
			// Drain any parallel tool calls already queued in ToolCallNotifyCh.
			// Note: agy_acp_server.par dispatches MCP tools/call sequentially (waiting for
			// the first HTTP response before sending the next tools/call), so waiting 350-600ms
			// when transcript.jsonl is present only adds dead latency per tool call.
			parkedList := []*acp.ParkedToolCall{parked}
			batchStart := time.Now()
			lastArrival := batchStart
			lastStepIdx := sess.GetLastTranscriptStep()
			stepInfo := extractCurrentToolStepInfo(gs.cfg.Server.DataDir, sess.AccountID, acpSessID, lastStepIdx)

		drainParallelTools:
			for len(parkedList) < 16 {
				// Non-blocking drain of any already-queued tool calls
				for len(parkedList) < 16 {
					select {
					case nextParked := <-sess.ToolCallNotifyCh:
						if nextParked != nil {
							gs.sm.MarkToolCallDispatched(nextParked.CallID)
							parkedList = append(parkedList, nextParked)
							lastArrival = time.Now()
						}
					default:
						goto checkBatchComplete
					}
				}
			checkBatchComplete:
				stepInfo = extractCurrentToolStepInfo(gs.cfg.Server.DataDir, sess.AccountID, acpSessID, lastStepIdx)
				if stepInfo.stepIndex > lastStepIdx && stepInfo.expectedToolCalls > 0 && len(parkedList) >= stepInfo.expectedToolCalls {
					break drainParallelTools
				}
				if stepInfo.hasTranscriptFile {
					// Real agy_acp_server.par (`localharness`) dispatches MCP tools/call strictly
					// sequentially (waiting for the HTTP response of call #1 before sending call #2)
					// and often flushes PLANNER_RESPONSE after the first MCP_TOOL completes.
					// Waiting here only adds dead latency per tool call.
					break drainParallelTools
				} else {
					idleLimit := 25 * time.Millisecond
					maxTotalWait := 120 * time.Millisecond
					if time.Since(lastArrival) >= idleLimit || time.Since(batchStart) >= maxTotalWait {
						break drainParallelTools
					}
				}
				select {
				case ev := <-worker.Events():
					processEvent(ev)
				case nextParked := <-sess.ToolCallNotifyCh:
					if nextParked != nil {
						gs.sm.MarkToolCallDispatched(nextParked.CallID)
						parkedList = append(parkedList, nextParked)
						lastArrival = time.Now()
					}
				case <-time.After(20 * time.Millisecond):
				}
			}
			drainEvents()
			flushThinkParser()

			if stepInfo.stepIndex <= lastStepIdx {
				stepInfo = extractCurrentToolStepInfo(gs.cfg.Server.DataDir, sess.AccountID, acpSessID, lastStepIdx)
			}
			if strings.TrimSpace(fullThinking.String()) == "" && stepInfo.reasoning != "" {
				emitThinking(stepInfo.reasoning)
			}

			calls := make([]adapter.CanonicalToolCall, 0, len(parkedList))
			var argsTokens int64
			for _, p := range parkedList {
				calls = append(calls, adapter.CanonicalToolCall{
					CallID:   p.CallID,
					ToolName: p.ToolName,
					ArgsJSON: p.ArgsJSON,
				})
				argsTokens += estimateTokens(p.ArgsJSON) + 6
			}

			effectivePromptTokens := promptTokens
			var cachedTokens int64
			if stepInfo.inputTokens > 0 {
				effectivePromptTokens = stepInfo.inputTokens
				cachedTokens = stepInfo.cachedTokens
				sess.SetLastTokenUsage(effectivePromptTokens, cachedTokens)
			} else if lastIn, lastCached := sess.GetLastTokenUsage(); lastIn > 0 {
				effectivePromptTokens = lastIn + estimateTurnDeltaTokens(canReq)
				cachedTokens = lastCached
			}
			clientInputTokens, clientCachedTokens := computeNorthboundUsageInputTokens(canReq, sess, effectivePromptTokens, cachedTokens)
			// Pure output tokens strictly exclude thinking tokens (both fullThinking and internal thinking in transcript.jsonl).
			compTokens := estimateTokens(fullText.String()) + argsTokens
			if stepInfo.outputTokens > 0 && stepInfo.outputTokens < compTokens {
				compTokens = stepInfo.outputTokens
			}
			if compTokens < 1 {
				compTokens = 1
			}

			// Detect when this turn is unparking a subsequent tool call from an already-generated
			// PLANNER_RESPONSE step in memory (no LLM call occurred), so we do not double-bill
			// prompt_tokens in audit_logs.
			isToolOnlyTurn := len(canReq.ToolResults) > 0 &&
				!sawNativeThought &&
				strings.TrimSpace(fullThinking.String()) == "" &&
				strings.TrimSpace(fullText.String()) == ""
			isSubToolStep := isToolOnlyTurn && firstByteMs <= 80
			if !isSubToolStep && isToolOnlyTurn && firstByteMs <= 3500 && stepInfo.stepIndex >= 0 && lastStepIdx >= 0 && stepInfo.stepIndex == lastStepIdx {
				isSubToolStep = true
			}

			auditPromptTokens := effectivePromptTokens
			if isSubToolStep {
				auditPromptTokens = 0
			}
			needAsyncTranscriptSync := false
			if stepInfo.stepIndex > lastStepIdx {
				sess.SetLastTranscriptStep(stepInfo.stepIndex)
			} else if stepInfo.hasTranscriptFile && !isSubToolStep {
				needAsyncTranscriptSync = true
			}
			if v, ok := activeReqTurnMeta.Load(reqID); ok {
				if m, ok2 := v.(reqTurnMeta); ok2 {
					m.stepIndex = stepInfo.stepIndex
					m.subToolStep = isSubToolStep
					activeReqTurnMeta.Store(reqID, m)
				}
			}

			var toolCallPayload []byte
			if len(calls) == 1 {
				toolCallPayload, _ = json.Marshal(map[string]string{
					"call_id":   calls[0].CallID,
					"tool_name": calls[0].ToolName,
					"arguments": calls[0].ArgsJSON,
				})
			} else {
				toolCallPayload, _ = json.Marshal(calls)
			}
			gs.recordAuditWithContext(reqID, canReq, sess.AccountID, acpSessID, worker.PID, auditPromptTokens, effectivePromptTokens, cachedTokens, compTokens, firstByteMs, startTime, http.StatusOK, "", fullThinking.String(), fullText.String(), string(toolCallPayload))
			if needAsyncTranscriptSync {
				var savedMeta reqTurnMeta
				if v, ok := activeReqTurnMeta.Load(reqID); ok {
					if m, ok2 := v.(reqTurnMeta); ok2 {
						savedMeta = m
					}
				}
				go gs.reconcileAuditWithTranscript(reqID, canReq, savedMeta, sess, sess.AccountID, acpSessID, lastStepIdx, compTokens, firstByteMs, string(toolCallPayload))
			}
			if canReq.Stream {
				clientThinking := fullThinking.String()
				if suppressClientThinking {
					clientThinking = ""
				}
				if oaSSE != nil {
					oaSSE.WriteToolCallsAndFinishWithUsage(calls, clientInputTokens, clientCachedTokens, compTokens)
				} else if oaRespSSE != nil {
					oaRespSSE.WriteToolCallsAndFinish(calls, clientThinking, fullText.String(), clientInputTokens, clientCachedTokens, compTokens)
				} else if clSSE != nil {
					clSSE.WriteToolUsesAndStopWithUsage(calls, clientInputTokens, clientCachedTokens, compTokens)
				}
				return
			}
			switch canReq.Protocol {
			case "openai":
				writeJSON(w, http.StatusOK, adapter.BuildOpenAIJSONResponseMulti(reqID, canReq.Model, fullThinking.String(), fullText.String(), calls, clientInputTokens, clientCachedTokens, compTokens))
			case "openai_responses":
				writeJSON(w, http.StatusOK, adapter.BuildOpenAIResponsesJSONResponseMulti(reqID, canReq.Model, fullThinking.String(), fullText.String(), calls, clientInputTokens, clientCachedTokens, compTokens))
			default:
				writeJSON(w, http.StatusOK, adapter.BuildClaudeJSONResponseMulti(reqID, canReq.Model, fullThinking.String(), fullText.String(), calls, clientInputTokens, clientCachedTokens, compTokens))
			}
			return

		case err := <-promptDoneCh:
			// Drain any buffered events that arrived right before prompt completion
			drainEvents()
			flushThinkParser()

			gs.pool.Release(worker)
			if fullText.Len() > 0 {
				msgsWithReply := append(append([]adapter.CanonicalMessage(nil), canReq.Messages...), adapter.CanonicalMessage{
					Role:    "assistant",
					Content: fullText.String(),
				})
				_, hashWithReply := adapter.ComputeHistoryHashes(canReq.SystemPrompt, msgsWithReply)
				gs.sm.UpdateSessionHash(sess, canReq.ScopedSessionKey(hashWithReply))
			}

			lastStepIdx := sess.GetLastTranscriptStep()
			stepInfo := extractCurrentToolStepInfo(gs.cfg.Server.DataDir, sess.AccountID, acpSessID, lastStepIdx)
			if stepInfo.hasTranscriptFile && stepInfo.stepIndex <= lastStepIdx {
				// Give localharness a brief window to flush the final PLANNER_RESPONSE line to transcript.jsonl
				for wait := 0; wait < 4 && stepInfo.stepIndex <= lastStepIdx; wait++ {
					time.Sleep(25 * time.Millisecond)
					stepInfo = extractCurrentToolStepInfo(gs.cfg.Server.DataDir, sess.AccountID, acpSessID, lastStepIdx)
				}
			}
			if strings.TrimSpace(fullThinking.String()) == "" && stepInfo.reasoning != "" {
				emitThinking(stepInfo.reasoning)
			}
			effectivePromptTokens := promptTokens
			var cachedTokens int64
			needAsyncTranscriptSync := false
			promptUsage := sess.ConsumeLastPromptUsage()
			if stepInfo.stepIndex > lastStepIdx && stepInfo.inputTokens > 0 {
				effectivePromptTokens = stepInfo.inputTokens
				cachedTokens = stepInfo.cachedTokens
				sess.SetLastTranscriptStep(stepInfo.stepIndex)
				sess.SetLastTokenUsage(effectivePromptTokens, cachedTokens)
			} else if promptUsage != nil && (promptUsage.InputTokens > 0 || promptUsage.CachedReadTokens > 0 || promptUsage.TotalTokens > 0) {
				totalIn := promptUsage.InputTokens + promptUsage.CachedReadTokens
				if totalIn <= 0 && promptUsage.TotalTokens > promptUsage.OutputTokens {
					totalIn = promptUsage.TotalTokens - promptUsage.OutputTokens - promptUsage.ThoughtTokens
				}
				if totalIn > 0 {
					effectivePromptTokens = totalIn
					cachedTokens = promptUsage.CachedReadTokens
					sess.SetLastTokenUsage(effectivePromptTokens, cachedTokens)
				}
				if stepInfo.stepIndex > lastStepIdx {
					sess.SetLastTranscriptStep(stepInfo.stepIndex)
				}
			} else {
				if lastIn, lastCached := sess.GetLastTokenUsage(); lastIn > 0 {
					effectivePromptTokens = lastIn + estimateTurnDeltaTokens(canReq)
					cachedTokens = lastCached
				}
				if stepInfo.hasTranscriptFile {
					needAsyncTranscriptSync = true
				}
			}
			clientInputTokens, clientCachedTokens := computeNorthboundUsageInputTokens(canReq, sess, effectivePromptTokens, cachedTokens)
			compTokens := estimateTokens(fullText.String())
			if stepInfo.stepIndex > lastStepIdx && stepInfo.outputTokens > 0 && stepInfo.outputTokens < compTokens {
				compTokens = stepInfo.outputTokens
			} else if promptUsage != nil && promptUsage.OutputTokens > 0 && promptUsage.OutputTokens < compTokens {
				compTokens = promptUsage.OutputTokens
			}
			effectiveFirstByteMs := firstOutputByteMs
			if effectiveFirstByteMs == 0 {
				effectiveFirstByteMs = firstByteMs
			}
			var savedMeta reqTurnMeta
			if v, ok := activeReqTurnMeta.Load(reqID); ok {
				if m, ok2 := v.(reqTurnMeta); ok2 {
					savedMeta = m
				}
			}
			if err != nil {
				gs.recordAuditWithContext(reqID, canReq, sess.AccountID, acpSessID, worker.PID, effectivePromptTokens, effectivePromptTokens, cachedTokens, compTokens, effectiveFirstByteMs, startTime, http.StatusBadGateway, err.Error(), fullThinking.String(), fullText.String(), "")
				switch {
				case oaSSE != nil:
					oaSSE.WriteError(err.Error())
				case oaRespSSE != nil:
					oaRespSSE.WriteError(err.Error())
				case clSSE != nil:
					clSSE.WriteError(err.Error())
				default:
					writeProtocolError(w, canReq.Protocol, http.StatusBadGateway, err.Error())
				}
				return
			}
			if compTokens < 1 {
				compTokens = 1
			}
			gs.recordAuditWithContext(reqID, canReq, sess.AccountID, acpSessID, worker.PID, effectivePromptTokens, effectivePromptTokens, cachedTokens, compTokens, effectiveFirstByteMs, startTime, http.StatusOK, "", fullThinking.String(), fullText.String(), "")
			if needAsyncTranscriptSync {
				go gs.reconcileAuditWithTranscript(reqID, canReq, savedMeta, sess, sess.AccountID, acpSessID, lastStepIdx, compTokens, effectiveFirstByteMs, "")
			}
			stopReason := sess.LastStopReason()
			if canReq.Stream {
				clientThinking := fullThinking.String()
				if suppressClientThinking {
					clientThinking = ""
				}
				if oaSSE != nil {
					oaSSE.SetStopReason(stopReason)
					oaSSE.WriteStopWithUsage(clientInputTokens, clientCachedTokens, compTokens)
				} else if oaRespSSE != nil {
					oaRespSSE.SetStopReason(stopReason)
					oaRespSSE.WriteStopWithCache(clientThinking, fullText.String(), clientInputTokens, clientCachedTokens, compTokens)
				} else if clSSE != nil {
					clSSE.SetStopReason(stopReason)
					clSSE.WriteStopWithUsage(clientInputTokens, clientCachedTokens, compTokens)
				}
				return
			}
			switch canReq.Protocol {
			case "openai":
				writeJSON(w, http.StatusOK, adapter.ApplyOpenAIStopReason(adapter.BuildOpenAIJSONResponseMulti(reqID, canReq.Model, fullThinking.String(), fullText.String(), nil, clientInputTokens, clientCachedTokens, compTokens), stopReason))
			case "openai_responses":
				writeJSON(w, http.StatusOK, adapter.ApplyResponsesStopReason(adapter.BuildOpenAIResponsesJSONResponseMulti(reqID, canReq.Model, fullThinking.String(), fullText.String(), nil, clientInputTokens, clientCachedTokens, compTokens), stopReason))
			default:
				writeJSON(w, http.StatusOK, adapter.ApplyClaudeStopReason(adapter.BuildClaudeJSONResponseMulti(reqID, canReq.Model, fullThinking.String(), fullText.String(), nil, clientInputTokens, clientCachedTokens, compTokens), stopReason))
			}
			return

		case <-ticker.C:
			if oaSSE != nil {
				oaSSE.WriteKeepAlive()
			} else if oaRespSSE != nil {
				oaRespSSE.WriteKeepAlive()
			} else if clSSE != nil {
				clSSE.WriteKeepAlive()
			}

		case <-ctx.Done():
			_ = worker.CancelSession(acpSessID)
			go func() {
				select {
				case <-promptDoneCh:
				case <-time.After(3 * time.Second):
				}
				for {
					select {
					case <-worker.Events():
					default:
						gs.pool.Release(worker)
						return
					}
				}
			}()
			return
		}
	}
}

type auditContextItem struct {
	Category string `json:"category"` // "system_prompt" | "tools" | "skills" | "files" | "tool_results" | "conversation"
	Tag      string `json:"tag"`      // "system" | "tool" | "skill" | "image" | "file" | "shell" | "result" | "turn"
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Tokens   int64  `json:"tokens"`
	Count    int    `json:"count,omitempty"`
	Content  string `json:"content,omitempty"`
}

type auditContextSnapshot struct {
	InputTokens         int64              `json:"input_tokens"`
	CachedTokens        int64              `json:"cached_tokens"`
	OutputTokens        int64              `json:"output_tokens"`
	TTFBMs              int64              `json:"ttfb_ms"`
	PrepMs              int64              `json:"prep_ms,omitempty"`
	StepIndex           int                `json:"step_index,omitempty"`
	SubToolStep         bool               `json:"sub_tool_step,omitempty"`
	TurnCount           int                `json:"turn_count"`
	ClientSessionID     string             `json:"client_session_id,omitempty"`
	SessionTitle        string             `json:"session_title,omitempty"`
	PromptSummary       string             `json:"prompt_summary,omitempty"`
	PromptMode          string             `json:"prompt_mode,omitempty"`
	SessionMode         string             `json:"session_mode,omitempty"`
	DisableBuiltinTools *bool              `json:"disable_builtin_tools,omitempty"`
	KeyName             string             `json:"key_name,omitempty"`
	ClientLabel         string             `json:"client_label,omitempty"`
	ThinkingLevel       string             `json:"thinking_level,omitempty"`
	ContextCapacity     int64              `json:"context_capacity,omitempty"`
	ClientContextWindow int64              `json:"client_context_window,omitempty"`
	Items               []auditContextItem `json:"items"`
}

func modelContextCapacityForAudit(modelName string, clientOverride int64) int64 {
	if clientOverride > 0 {
		return clientOverride
	}
	lower := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.Contains(lower, "gpt-oss"):
		return 128000
	case strings.Contains(lower, "claude"):
		return 200000
	case strings.Contains(lower, "gpt-5"), strings.Contains(lower, "gpt-6"), strings.Contains(lower, "codex"), strings.Contains(lower, "o3"):
		return 272000
	default:
		return 1048576
	}
}

var (
	toolSchemaTokCacheMu sync.RWMutex
	toolSchemaTokCache   = make(map[string]int64)
)

func estimateToolDefinitionTokens(t acp.ToolDefinition) int64 {
	cacheKey := t.Name + "\x00" + t.Description
	toolSchemaTokCacheMu.RLock()
	if tok, ok := toolSchemaTokCache[cacheKey]; ok {
		toolSchemaTokCacheMu.RUnlock()
		return tok
	}
	toolSchemaTokCacheMu.RUnlock()

	rawBytes, _ := json.Marshal(t.InputSchema)
	tok := estimateTokens(t.Name+" "+t.Description+" "+string(rawBytes)) + 8

	toolSchemaTokCacheMu.Lock()
	if len(toolSchemaTokCache) > 512 {
		toolSchemaTokCache = make(map[string]int64, 128)
	}
	toolSchemaTokCache[cacheKey] = tok
	toolSchemaTokCacheMu.Unlock()
	return tok
}

func extractLatestUserGoalSummary(canReq *adapter.CanonicalRequest) string {
	if canReq == nil {
		return ""
	}
	for i := len(canReq.Messages) - 1; i >= 0; i-- {
		m := canReq.Messages[i]
		if strings.ToLower(strings.TrimSpace(m.Role)) != "user" {
			continue
		}
		cleaned := stripTurnContextBlocks(m.Content)
		if strings.Contains(cleaned, "# Handoff Summary") || strings.Contains(cleaned, "# Resuming from a compaction") {
			continue
		}
		lines := strings.Split(cleaned, "\n")
		for _, ln := range lines {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "Attachment reference:") || strings.HasPrefix(t, "- [Attachment:") {
				continue
			}
			if utf8.RuneCountInString(t) > 38 {
				runes := []rune(t)
				return string(runes[:38]) + "..."
			}
			return t
		}
	}
	return ""
}

func buildRequestContextJSON(
	canReq *adapter.CanonicalRequest,
	turnMeta reqTurnMeta,
	inputTokens int64,
	cachedTokens int64,
	outputTokens int64,
	ttfbMs int64,
	toolCallsJSON string,
) string {
	if canReq == nil {
		return ""
	}
	promptMode := strings.TrimSpace(turnMeta.policy.Mode)
	if promptMode == "" {
		promptMode = "native_hybrid"
	}
	sessionMode := strings.TrimSpace(turnMeta.sessionMode)
	if sessionMode == "" {
		sessionMode = "yolo"
	}
	disableBuiltin := turnMeta.policy.DisableBuiltinTools
	clientLabel := strings.TrimSpace(turnMeta.clientLabel)
	if clientLabel == "" {
		clientLabel = detectRequestClientLabel(nil, canReq, canReq.Protocol, turnMeta.keyName)
	}
	thinkingLvl := strings.TrimSpace(turnMeta.thinking)
	if thinkingLvl == "" {
		thinkingLvl = strings.TrimSpace(canReq.ThinkingLevel)
	}

	sessionTitle := strings.TrimSpace(canReq.SessionTitle)
	if strings.Contains(toolCallsJSON, "set_session_title") {
		var singleTC struct {
			ToolName  string `json:"tool_name"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal([]byte(toolCallsJSON), &singleTC) == nil && strings.Contains(singleTC.ToolName, "set_session_title") {
			var argsObj struct {
				Title string `json:"title"`
			}
			if json.Unmarshal([]byte(singleTC.Arguments), &argsObj) == nil && strings.TrimSpace(argsObj.Title) != "" {
				sessionTitle = strings.TrimSpace(argsObj.Title)
			}
		} else {
			var multiTC []struct {
				ToolName  string `json:"tool_name"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal([]byte(toolCallsJSON), &multiTC) == nil {
				for _, tc := range multiTC {
					if strings.Contains(tc.ToolName, "set_session_title") {
						var argsObj struct {
							Title string `json:"title"`
						}
						if json.Unmarshal([]byte(tc.Arguments), &argsObj) == nil && strings.TrimSpace(argsObj.Title) != "" {
							sessionTitle = strings.TrimSpace(argsObj.Title)
							break
						}
					}
				}
			}
		}
	}
	if turnMeta.sess != nil {
		if sessionTitle != "" {
			turnMeta.sess.SetSessionTitle(sessionTitle)
		} else {
			sessionTitle = turnMeta.sess.GetSessionTitle()
		}
	}

	snap := auditContextSnapshot{
		InputTokens:         inputTokens,
		CachedTokens:        cachedTokens,
		OutputTokens:        outputTokens,
		TTFBMs:              ttfbMs,
		PrepMs:              turnMeta.prepMs,
		StepIndex:           turnMeta.stepIndex,
		SubToolStep:         turnMeta.subToolStep,
		TurnCount:           len(canReq.Messages),
		ClientSessionID:     strings.TrimSpace(canReq.ClientSessionID),
		SessionTitle:        sessionTitle,
		PromptSummary:       extractLatestUserGoalSummary(canReq),
		PromptMode:          promptMode,
		SessionMode:         sessionMode,
		DisableBuiltinTools: &disableBuiltin,
		KeyName:             turnMeta.keyName,
		ClientLabel:         clientLabel,
		ThinkingLevel:       thinkingLvl,
		ContextCapacity:     modelContextCapacityForAudit(canReq.Model, canReq.ContextWindow),
		ClientContextWindow: canReq.ContextWindow,
	}

	var items []auditContextItem

	// 1. System Prompt & Skills strictly aligned with promptMode and actual section sizes
	sysText := strings.TrimSpace(canReq.SystemPrompt)
	splitSystemAndSkills := func(text string) (string, string, int64) {
		for _, marker := range []string{"Available skills", "Project instructions", "Guide: AGENTS.md", "<!-- BEGIN:reui-agent-rules -->"} {
			if idx := strings.Index(text, marker); idx >= 0 {
				mainPart := strings.TrimSpace(text[:idx])
				skillPart := strings.TrimSpace(text[idx:])
				return mainPart, skillPart, estimateTokens(skillPart)
			}
		}
		if strings.Contains(text, "SKILL.md") || strings.Contains(text, "AGENTS.md") {
			return text, text, estimateTokens(text) / 6
		}
		return text, "", 0
	}

	switch promptMode {
	case "pure_passthrough":
		if sysText != "" {
			totalSysTok := estimateTokens(sysText)
			mainSysText, skillText, skillTok := splitSystemAndSkills(sysText)
			if skillTok >= totalSysTok {
				skillTok = totalSysTok / 4
			}
			mainSysTok := totalSysTok - skillTok
			items = append(items, auditContextItem{
				Category: "system_prompt",
				Tag:      "system",
				Title:    "客户端系统提示词",
				Subtitle: "pure_passthrough 原生透传",
				Tokens:   mainSysTok,
				Content:  truncateUTF8Middle(mainSysText, 6000, "\n...[系统提示词已截断]...\n"),
			})
			if skillTok > 0 {
				items = append(items, auditContextItem{
					Category: "skills",
					Tag:      "skill",
					Title:    "AGENTS.md 与技能规范",
					Subtitle: "客户端附带项目规则",
					Tokens:   skillTok,
					Content:  truncateUTF8Middle(skillText, 6000, "\n...[技能规范已截断]...\n"),
				})
			}
		}
	default:
		// prepend_global or native_hybrid
		acpSys := adapter.BuildACPSystemPrompt(sysText, turnMeta.policy, universalThinkingSystemPrompt)
		totalSysTok := estimateTokens(acpSys)
		mainSysText, skillText, skillTok := splitSystemAndSkills(sysText)
		if skillTok >= totalSysTok {
			skillTok = totalSysTok / 4
		}
		rulesText := strings.TrimSpace(turnMeta.policy.GlobalPrependPrompt + "\n\n" + universalThinkingSystemPrompt)
		rulesTok := estimateTokens(universalThinkingSystemPrompt) + estimateTokens(turnMeta.policy.GlobalPrependPrompt)
		if promptMode != "native_hybrid" {
			rulesTok += estimateTokens(adapter.NativeSystemPromptOverride)
			rulesText = strings.TrimSpace(adapter.NativeSystemPromptOverride + "\n\n" + rulesText)
		}
		mainSysTok := totalSysTok - skillTok - rulesTok
		if mainSysTok < 0 {
			mainSysTok = totalSysTok - skillTok
			rulesTok = 0
		}
		if mainSysTok > 0 {
			items = append(items, auditContextItem{
				Category: "system_prompt",
				Tag:      "system",
				Title:    "agent 提示词",
				Subtitle: fmt.Sprintf("策略 %s", promptMode),
				Tokens:   mainSysTok,
				Content:  truncateUTF8Middle(mainSysText, 6000, "\n...[提示词已截断]...\n"),
			})
		}
		if rulesTok > 0 {
			items = append(items, auditContextItem{
				Category: "system_prompt",
				Tag:      "system",
				Title:    "网关系统与思考规则",
				Subtitle: "网关前置与思考标签规范",
				Tokens:   rulesTok,
				Content:  truncateUTF8Middle(rulesText, 4000, "\n...[规则已截断]...\n"),
			})
		}
		if skillTok > 0 {
			items = append(items, auditContextItem{
				Category: "skills",
				Tag:      "skill",
				Title:    "AGENTS.md 与技能规范",
				Subtitle: "项目指令与技能目录",
				Tokens:   skillTok,
				Content:  truncateUTF8Middle(skillText, 6000, "\n...[技能规范已截断]...\n"),
			})
		}
	}

	// 2. Tools strictly aligned with actual canReq.Tools and disableBuiltin
	if len(canReq.Tools) > 0 {
		type toolSizing struct {
			name    string
			desc    string
			brief   string
			content string
			tok     int64
		}
		var toolList []toolSizing
		var totalToolTok int64
		for _, t := range canReq.Tools {
			tok := estimateToolDefinitionTokens(t)
			totalToolTok += tok
			shortName := strings.TrimPrefix(t.Name, "mcp_client_tools_")
			if shortName == "run_command" {
				shortName = "exec"
			}
			schemaBytes, _ := json.MarshalIndent(t.InputSchema, "", "  ")
			var docBuilder strings.Builder
			docBuilder.WriteString(fmt.Sprintf("Tool: %s\n", t.Name))
			if d := strings.TrimSpace(t.Description); d != "" {
				docBuilder.WriteString(fmt.Sprintf("Description: %s\n", d))
			}
			if len(schemaBytes) > 0 && string(schemaBytes) != "null" {
				docBuilder.WriteString(fmt.Sprintf("\nInput Schema:\n%s", string(schemaBytes)))
			}
			firstLineDesc := strings.TrimSpace(strings.Split(strings.TrimSpace(t.Description), "\n")[0])
			toolList = append(toolList, toolSizing{
				name:    shortName,
				desc:    t.Name,
				brief:   firstLineDesc,
				content: docBuilder.String(),
				tok:     tok,
			})
		}
		sort.Slice(toolList, func(i, j int) bool {
			return toolList[i].tok > toolList[j].tok
		})
		var topSum int64
		limit := 3
		if len(toolList) < limit {
			limit = len(toolList)
		}
		for i := 0; i < limit; i++ {
			items = append(items, auditContextItem{
				Category: "tools",
				Tag:      "tool",
				Title:    toolList[i].name,
				Subtitle: toolList[i].desc,
				Tokens:   toolList[i].tok,
				Content:  truncateUTF8Middle(toolList[i].content, 4000, "\n...[工具 Schema 已截断]...\n"),
			})
			topSum += toolList[i].tok
		}
		if len(toolList) > limit && totalToolTok > topSum {
			var remBuilder strings.Builder
			for idx, rt := range toolList[limit:] {
				remBuilder.WriteString(fmt.Sprintf("%d. %s (%s) — %d token\n", idx+1, rt.name, rt.desc, rt.tok))
				if rt.brief != "" {
					remBuilder.WriteString(fmt.Sprintf("   %s\n", rt.brief))
				}
			}
			items = append(items, auditContextItem{
				Category: "tools",
				Tag:      "tool",
				Title:    fmt.Sprintf("另外 %d 个客户端工具", len(toolList)-limit),
				Subtitle: "客户端 MCP 工具定义",
				Tokens:   totalToolTok - topSum,
				Count:    len(toolList) - limit,
				Content:  truncateUTF8Middle(strings.TrimSpace(remBuilder.String()), 6000, "\n...[工具列表已截断]...\n"),
			})
		}
		if !disableBuiltin {
			items = append(items, auditContextItem{
				Category: "tools",
				Tag:      "tool",
				Title:    "ACP 内置工具集",
				Subtitle: "ACP 宿主终端与文件工具",
				Tokens:   1800,
				Count:    12,
				Content:  "ACP 宿主内置终端与文件操作工具集 (12 项内置工具)",
			})
		}
	} else if !disableBuiltin {
		items = append(items,
			auditContextItem{
				Category: "tools",
				Tag:      "tool",
				Title:    "ACP 内置工具集",
				Subtitle: "ACP 宿主终端与文件工具",
				Tokens:   1800,
				Count:    12,
				Content:  "ACP 宿主内置终端与文件操作工具集 (12 项内置工具)",
			},
		)
	}

	// 3. Non-overlapping attribution for Files, Tool Results, and Conversation Turns
	var imageTokens int64
	var imageCount int
	for _, b := range canReq.PromptBlocks {
		if b.Type == "image" {
			imageCount++
			imageTokens += 1400
		}
	}

	fileTokMap := make(map[string]int64)
	fileDirMap := make(map[string]string)
	fileCountMap := make(map[string]int)
	fileSnippetMap := make(map[string]string)

	recordFileHit := func(pathStr string, tok int64, rawContent string) bool {
		cleaned := strings.Trim(pathStr, "\"'`(),:;[]{}<>")
		if cleaned == "" || strings.HasPrefix(cleaned, "http://") || strings.HasPrefix(cleaned, "https://") {
			return false
		}
		base := filepath.Base(cleaned)
		dir := filepath.Dir(cleaned)
		if dir == "." {
			dir = "/"
		} else if !strings.HasPrefix(dir, "/") {
			dir = "/" + dir
		}
		if base == "" || base == "." || base == "/" || !strings.Contains(base, ".") {
			return false
		}
		fileTokMap[base] += tok
		fileDirMap[base] = dir
		fileCountMap[base]++
		if trimmed := strings.TrimSpace(rawContent); trimmed != "" {
			fileSnippetMap[base] = truncateUTF8Middle(trimmed, 3500, "\n...[文件内容已截断]...\n")
		}
		return true
	}

	extractFileReadTarget := func(toolName, toolArgs, content string) string {
		lowerTool := strings.ToLower(toolName)
		if strings.Contains(lowerTool, "read_file") || strings.Contains(lowerTool, "code_context") || strings.Contains(lowerTool, "code_outline") || strings.Contains(lowerTool, "git_diff") || strings.Contains(lowerTool, "read_skill_resource") {
			if toolArgs != "" {
				var parsed map[string]any
				if json.Unmarshal([]byte(toolArgs), &parsed) == nil {
					if p, ok := parsed["path"].(string); ok && strings.TrimSpace(p) != "" {
						return strings.TrimSpace(p)
					}
				}
			}
		}
		if strings.Contains(content, `"kind":"file_lines"`) {
			if idx := strings.Index(content, `"path":"`); idx >= 0 {
				rest := content[idx+len(`"path":"`):]
				if end := strings.IndexByte(rest, '"'); end > 0 {
					return rest[:end]
				}
			}
		}
		lines := strings.SplitN(strings.TrimSpace(content), "\n", 8)
		for i := 0; i < len(lines)-1; i++ {
			ln := strings.TrimSpace(lines[i])
			nextLn := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(nextLn, "Lines ") && !strings.ContainsAny(ln, " \t\"'{}[]") && strings.Contains(ln, ".") {
				return ln
			}
		}
		return ""
	}

	type toolResultAgg struct {
		tok      int64
		count    int
		snippets []string
	}
	toolResByMap := make(map[string]*toolResultAgg)
	var toolResOrder []string
	addToolResult := func(name string, tok int64, rawContent string) {
		short := strings.TrimPrefix(strings.TrimSpace(name), "mcp_client_tools_")
		if short == "" {
			short = "tool_output"
		}
		if short == "run_command" {
			short = "exec"
		}
		agg, ok := toolResByMap[short]
		if !ok {
			agg = &toolResultAgg{}
			toolResByMap[short] = agg
			toolResOrder = append(toolResOrder, short)
		}
		agg.tok += tok
		agg.count++
		if trimmed := strings.TrimSpace(rawContent); trimmed != "" {
			snip := truncateUTF8Middle(trimmed, 1800, "\n...[输出已截断]...\n")
			if len(agg.snippets) < 3 {
				agg.snippets = append(agg.snippets, snip)
			} else {
				agg.snippets[len(agg.snippets)-1] = snip
			}
		}
	}

	type turnItem struct {
		turnNum  int
		role     string
		tok      int64
		preview  string
		fullText string
	}
	var turns []turnItem
	turnIdx := 0

	var lastAsstToolName string
	var lastAsstToolArgs string

	if len(canReq.Messages) > 0 {
		for _, m := range canReq.Messages {
			role := strings.ToLower(strings.TrimSpace(m.Role))
			tok := estimateTokens(m.Content)
			if tok <= 0 {
				continue
			}
			if role == "assistant" {
				trimmed := strings.TrimSpace(m.Content)
				if openIdx := strings.Index(trimmed, "("); openIdx > 0 && strings.HasSuffix(trimmed, ")") {
					candName := strings.TrimSpace(trimmed[:openIdx])
					if !strings.ContainsAny(candName, " \t\n") {
						lastAsstToolName = candName
						lastAsstToolArgs = trimmed[openIdx+1 : len(trimmed)-1]
					}
				}
			}
			if role == "tool" || strings.Contains(m.Content, "[Tool Execution Result]") || strings.Contains(m.Content, "[TOOL]:") {
				if filePath := extractFileReadTarget(lastAsstToolName, lastAsstToolArgs, m.Content); filePath != "" {
					if recordFileHit(filePath, tok, m.Content) {
						continue
					}
				}
				addToolResult(lastAsstToolName, tok, m.Content)
				continue
			}
			turnIdx++
			cleanBody := strings.TrimSpace(stripTurnContextBlocks(m.Content))
			preview := cleanBody
			if utf8.RuneCountInString(preview) > 28 {
				r := []rune(preview)
				preview = string(r[:28]) + "..."
			}
			turns = append(turns, turnItem{
				turnNum:  turnIdx,
				role:     role,
				tok:      tok,
				preview:  preview,
				fullText: truncateUTF8Middle(cleanBody, 2500, "\n...[消息已截断]...\n"),
			})
		}
	} else {
		for _, tr := range canReq.ToolResults {
			tok := estimateTokens(tr.Content)
			if tok <= 0 {
				continue
			}
			if filePath := extractFileReadTarget("", "", tr.Content); filePath != "" {
				if recordFileHit(filePath, tok, tr.Content) {
					continue
				}
			}
			addToolResult("tool_output", tok, tr.Content)
		}
	}

	if imageCount > 0 && imageTokens > 0 {
		items = append(items, auditContextItem{
			Category: "files",
			Tag:      "image",
			Title:    "图片",
			Subtitle: "",
			Tokens:   imageTokens,
			Count:    imageCount,
			Content:  fmt.Sprintf("本轮上下文附带 %d 张图片输入 (约 %d token)", imageCount, imageTokens),
		})
	}

	type fileEntry struct {
		name  string
		dir   string
		tok   int64
		count int
	}
	var fileList []fileEntry
	for k, v := range fileTokMap {
		fileList = append(fileList, fileEntry{name: k, dir: fileDirMap[k], tok: v, count: fileCountMap[k]})
	}
	sort.Slice(fileList, func(i, j int) bool {
		return fileList[i].tok > fileList[j].tok
	})
	for i, f := range fileList {
		if i >= 6 {
			var extraTok int64
			var extraCount int
			var extraBuilder strings.Builder
			for _, rem := range fileList[6:] {
				extraTok += rem.tok
				extraCount++
				extraBuilder.WriteString(fmt.Sprintf("### %s (%s · %d token)\n", rem.name, rem.dir, rem.tok))
				if snip := fileSnippetMap[rem.name]; snip != "" {
					extraBuilder.WriteString(snip + "\n\n")
				}
			}
			if extraTok > 0 {
				items = append(items, auditContextItem{
					Category: "files",
					Tag:      "file",
					Title:    fmt.Sprintf("另外 %d 个文件", extraCount),
					Subtitle: "历史读取文件",
					Tokens:   extraTok,
					Count:    extraCount,
					Content:  truncateUTF8Middle(strings.TrimSpace(extraBuilder.String()), 5000, "\n...[文件内容已截断]...\n"),
				})
			}
			break
		}
		items = append(items, auditContextItem{
			Category: "files",
			Tag:      "file",
			Title:    f.name,
			Subtitle: f.dir,
			Tokens:   f.tok,
			Count:    f.count,
			Content:  fileSnippetMap[f.name],
		})
	}

	for _, tName := range toolResOrder {
		agg := toolResByMap[tName]
		if agg == nil || agg.tok <= 0 {
			continue
		}
		items = append(items, auditContextItem{
			Category: "tool_results",
			Tag:      "result",
			Title:    tName,
			Subtitle: "工具调用返回结果",
			Tokens:   agg.tok,
			Count:    agg.count,
			Content:  truncateUTF8Middle(strings.Join(agg.snippets, "\n\n---\n\n"), 4500, "\n...[工具输出已截断]...\n"),
		})
	}

	startTurn := 0
	if len(turns) > 5 {
		var earlierTok int64
		var earlierBuilder strings.Builder
		for i := 0; i < len(turns)-4; i++ {
			earlierTok += turns[i].tok
			earlierBuilder.WriteString(fmt.Sprintf("### 第 %d 轮 (%s · %d token)\n%s\n\n", turns[i].turnNum, turns[i].role, turns[i].tok, turns[i].fullText))
		}
		if earlierTok > 0 {
			items = append(items, auditContextItem{
				Category: "conversation",
				Tag:      "turn",
				Title:    fmt.Sprintf("更早的 %d 轮", len(turns)-4),
				Subtitle: "历史会话消息",
				Tokens:   earlierTok,
				Count:    len(turns) - 4,
				Content:  truncateUTF8Middle(strings.TrimSpace(earlierBuilder.String()), 5000, "\n...[历史会话已截断]...\n"),
			})
		}
		startTurn = len(turns) - 4
	}
	for i := startTurn; i < len(turns); i++ {
		items = append(items, auditContextItem{
			Category: "conversation",
			Tag:      "turn",
			Title:    fmt.Sprintf("第 %d 轮", turns[i].turnNum),
			Subtitle: turns[i].preview,
			Tokens:   turns[i].tok,
			Content:  fmt.Sprintf("[%s]\n%s", turns[i].role, turns[i].fullText),
		})
	}

	var fixedSum int64
	var dynSum int64
	for _, it := range items {
		if it.Category == "system_prompt" || it.Category == "tools" || it.Category == "skills" {
			fixedSum += it.Tokens
		} else {
			dynSum += it.Tokens
		}
	}
	rawSum := fixedSum + dynSum
	if inputTokens > 0 && rawSum > 0 && inputTokens != rawSum {
		if inputTokens <= rawSum && dynSum > 0 && fixedSum < (inputTokens*75)/100 {
			remDyn := inputTokens - fixedSum
			for i := range items {
				cat := items[i].Category
				if cat == "system_prompt" || cat == "tools" || cat == "skills" {
					continue
				}
				scaled := (items[i].Tokens * remDyn) / dynSum
				if scaled < 1 {
					scaled = 1
				}
				items[i].Tokens = scaled
			}
		} else if inputTokens <= rawSum || dynSum == 0 || fixedSum >= (inputTokens*75)/100 {
			for i := range items {
				scaled := (items[i].Tokens * inputTokens) / rawSum
				if scaled < 1 {
					scaled = 1
				}
				items[i].Tokens = scaled
			}
		} else {
			remDyn := inputTokens - fixedSum
			for i := range items {
				cat := items[i].Category
				if cat == "system_prompt" || cat == "tools" || cat == "skills" {
					continue
				}
				scaled := (items[i].Tokens * remDyn) / dynSum
				if scaled < 1 {
					scaled = 1
				}
				items[i].Tokens = scaled
			}
		}
	}

	snap.Items = items
	raw, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (gs *GatewayServer) recordAudit(
	reqID string,
	canReq *adapter.CanonicalRequest,
	accountID string,
	sessionID string,
	pid int,
	promptTokens int64,
	completionTokens int64,
	start time.Time,
	status int,
	errMsg string,
	thinkingText string,
	responseText string,
	toolCallsJSON string,
) {
	gs.recordAuditWithContext(reqID, canReq, accountID, sessionID, pid, promptTokens, promptTokens, 0, completionTokens, 0, start, status, errMsg, thinkingText, responseText, toolCallsJSON)
}

func (gs *GatewayServer) recordAuditWithContext(
	reqID string,
	canReq *adapter.CanonicalRequest,
	accountID string,
	sessionID string,
	pid int,
	promptTokens int64,
	effectivePromptTokens int64,
	cachedTokens int64,
	completionTokens int64,
	ttfbMs int64,
	start time.Time,
	status int,
	errMsg string,
	thinkingText string,
	responseText string,
	toolCallsJSON string,
) {
	if gs.store == nil {
		return
	}
	var protocol, model, thinkingLevel, sysPrompt, promptText string
	if canReq != nil {
		protocol = canReq.Protocol
		model = canReq.Model
		thinkingLevel = canReq.ThinkingLevel
		sysPrompt = canReq.SystemPrompt
		promptText = extractPromptPreview(canReq)
	}
	contextInputTok := effectivePromptTokens
	if contextInputTok <= 0 {
		contextInputTok = promptTokens
	}
	turnMeta := reqTurnMeta{
		policy:      gs.cfg.GetPromptPolicy(),
		sessionMode: gs.cfg.GetSandboxPolicy().SessionMode,
		thinking:    thinkingLevel,
	}
	if v, ok := activeReqTurnMeta.Load(reqID); ok {
		if m, ok2 := v.(reqTurnMeta); ok2 {
			turnMeta = m
		}
	}
	if promptTokens == 0 && effectivePromptTokens > 0 {
		turnMeta.subToolStep = true
	}
	auditCachedTokens := cachedTokens
	if turnMeta.subToolStep {
		promptTokens = 0
		auditCachedTokens = 0
	}
	contextJSON := buildRequestContextJSON(canReq, turnMeta, contextInputTok, cachedTokens, completionTokens, ttfbMs, toolCallsJSON)
	_ = gs.store.RecordAuditLog(storage.AuditLog{
		RequestID:        reqID,
		Protocol:         protocol,
		AccountID:        accountID,
		SessionID:        sessionID,
		WorkerPID:        pid,
		ModelName:        model,
		ThinkingLevel:    thinkingLevel,
		PromptTokens:     promptTokens,
		CachedTokens:     auditCachedTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		LatencyMs:        time.Since(start).Milliseconds(),
		StatusCode:       status,
		ErrorMsg:         errMsg,
		SystemPrompt:     sysPrompt,
		PromptText:       promptText,
		ThinkingText:     thinkingText,
		ResponseText:     responseText,
		ToolCallsJSON:    toolCallsJSON,
		ContextJSON:      contextJSON,
	})
	if gs.hub != nil {
		gs.hub.Broadcast(accountID, "AUDIT", reqID)
	}
}

func (gs *GatewayServer) reconcileAuditWithTranscript(
	reqID string,
	canReq *adapter.CanonicalRequest,
	turnMeta reqTurnMeta,
	sess *acp.ManagedSession,
	accountID string,
	acpSessID string,
	minStepIdx int,
	initialCompTokens int64,
	ttfbMs int64,
	toolCallsJSON string,
) {
	if gs.store == nil || acpSessID == "" {
		return
	}
	for attempt := 0; attempt < 20; attempt++ {
		time.Sleep(100 * time.Millisecond)
		stepInfo := extractCurrentToolStepInfo(gs.cfg.Server.DataDir, accountID, acpSessID, minStepIdx)
		if stepInfo.stepIndex > minStepIdx && stepInfo.inputTokens > 0 {
			if sess != nil {
				if stepInfo.stepIndex > sess.GetLastTranscriptStep() {
					sess.SetLastTranscriptStep(stepInfo.stepIndex)
				}
				sess.SetLastTokenUsage(stepInfo.inputTokens, stepInfo.cachedTokens)
				if stepInfo.inputTokens >= acpCompactionTriggerTokens {
					alreadyReq, waitTurns := sess.GetClientCompactionState()
					if !alreadyReq {
						sess.SetClientCompactionState(true, waitTurns)
					}
				}
			}
			compTok := initialCompTokens
			if stepInfo.outputTokens > 0 && stepInfo.outputTokens < compTok {
				compTok = stepInfo.outputTokens
			}
			if compTok < 1 {
				compTok = 1
			}
			turnMeta.stepIndex = stepInfo.stepIndex
			ctxJSON := buildRequestContextJSON(canReq, turnMeta, stepInfo.inputTokens, stepInfo.cachedTokens, compTok, ttfbMs, toolCallsJSON)
			auditPrompt := stepInfo.inputTokens
			auditCached := stepInfo.cachedTokens
			if turnMeta.subToolStep {
				auditPrompt = 0
				auditCached = 0
			}
			_ = gs.store.UpdateAuditLogTokens(reqID, auditPrompt, auditCached, compTok, ctxJSON)
			if gs.hub != nil {
				gs.hub.Broadcast(accountID, "AUDIT", reqID)
			}
			return
		}
	}
}

type parsedTranscriptPlannerStep struct {
	stepIndex    int
	createdAt    time.Time
	inputTokens  int64
	cachedTokens int64
	outputTokens int64
}

type cachedTranscriptStepsEntry struct {
	modTimeNano int64
	size        int64
	steps       []parsedTranscriptPlannerStep
}

var (
	transcriptStepsCacheMu sync.Mutex
	transcriptStepsCache   = make(map[string]cachedTranscriptStepsEntry)
)

func readCLIPlannerStepsFromDB(dataDir, accountID, acpSessionID string) []parsedTranscriptPlannerStep {
	dbPath, st, err := resolveCLIConversationDBPath(dataDir, accountID, acpSessionID)
	if err != nil {
		return nil
	}
	modNano := st.ModTime().UnixNano()
	size := st.Size()
	if walSt, wErr := os.Stat(dbPath + "-wal"); wErr == nil {
		if walSt.ModTime().UnixNano() > modNano {
			modNano = walSt.ModTime().UnixNano()
		}
		size += walSt.Size()
	}

	transcriptStepsCacheMu.Lock()
	if cached, ok := transcriptStepsCache[dbPath]; ok && cached.modTimeNano == modNano && cached.size == size {
		steps := cached.steps
		transcriptStepsCacheMu.Unlock()
		return steps
	}
	transcriptStepsCacheMu.Unlock()

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(1000)", filepath.ToSlash(dbPath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`SELECT idx, metadata FROM steps WHERE step_type = 15 ORDER BY idx ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var steps []parsedTranscriptPlannerStep
	for rows.Next() {
		var idx int
		var meta []byte
		if err := rows.Scan(&idx, &meta); err != nil {
			continue
		}
		ts, totalIn, cachedIn, outTok := parseCLIStepMetadataProto(meta)
		if totalIn <= 0 {
			continue
		}
		steps = append(steps, parsedTranscriptPlannerStep{
			stepIndex:    idx,
			createdAt:    ts,
			inputTokens:  totalIn,
			cachedTokens: cachedIn,
			outputTokens: outTok,
		})
	}

	transcriptStepsCacheMu.Lock()
	if len(transcriptStepsCache) > 256 {
		transcriptStepsCache = make(map[string]cachedTranscriptStepsEntry, 64)
	}
	transcriptStepsCache[dbPath] = cachedTranscriptStepsEntry{
		modTimeNano: modNano,
		size:        size,
		steps:       steps,
	}
	transcriptStepsCacheMu.Unlock()
	return steps
}

func readTranscriptPlannerSteps(dataDir, accountID, acpSessionID string) []parsedTranscriptPlannerStep {
	if accountID == "" {
		accountID = "default"
	}
	if dataDir == "" || acpSessionID == "" {
		return nil
	}
	transcriptPath, st, err := resolveTranscriptPath(dataDir, accountID, acpSessionID)
	if err != nil {
		return readCLIPlannerStepsFromDB(dataDir, accountID, acpSessionID)
	}
	modNano := st.ModTime().UnixNano()
	size := st.Size()

	transcriptStepsCacheMu.Lock()
	if cached, ok := transcriptStepsCache[transcriptPath]; ok && cached.modTimeNano == modNano && cached.size == size {
		steps := cached.steps
		transcriptStepsCacheMu.Unlock()
		return steps
	}
	transcriptStepsCacheMu.Unlock()

	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	const maxTailBytes = 512 * 1024
	var raw []byte
	if size > maxTailBytes {
		if _, err := f.Seek(size-maxTailBytes, io.SeekStart); err != nil {
			return nil
		}
		raw, err = io.ReadAll(f)
		if err != nil || len(raw) == 0 {
			return nil
		}
		if nl := strings.IndexByte(string(raw), '\n'); nl >= 0 {
			raw = raw[nl+1:]
		}
	} else {
		raw, err = io.ReadAll(f)
		if err != nil || len(raw) == 0 {
			return nil
		}
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	steps := make([]parsedTranscriptPlannerStep, 0, 32)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, `"PLANNER_RESPONSE"`) {
			continue
		}
		var step struct {
			StepIndex       int    `json:"step_index"`
			Type            string `json:"type"`
			CreatedAt       string `json:"created_at"`
			InputTokens     int64  `json:"input_tokens"`
			CacheReadTokens int64  `json:"cache_read_tokens"`
			CachedTokens    int64  `json:"cached_tokens"`
			OutputTokens    int64  `json:"output_tokens"`
			Usage           *struct {
				InputTokens     int64 `json:"input_tokens"`
				CacheReadTokens int64 `json:"cache_read_tokens"`
				CachedTokens    int64 `json:"cached_tokens"`
				OutputTokens    int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &step) != nil || step.Type != "PLANNER_RESPONSE" {
			continue
		}
		uncachedIn := step.InputTokens
		cacheRead := step.CacheReadTokens
		cachedOther := step.CachedTokens
		outTok := step.OutputTokens
		if step.Usage != nil {
			if uncachedIn == 0 {
				uncachedIn = step.Usage.InputTokens
			}
			if cacheRead == 0 {
				cacheRead = step.Usage.CacheReadTokens
			}
			if cachedOther == 0 {
				cachedOther = step.Usage.CachedTokens
			}
			if outTok == 0 {
				outTok = step.Usage.OutputTokens
			}
		}
		var totalIn, cachedTok int64
		if cacheRead > 0 {
			cachedTok = cacheRead
			totalIn = uncachedIn + cacheRead
		} else if cachedOther > 0 {
			cachedTok = cachedOther
			if uncachedIn >= cachedOther {
				totalIn = uncachedIn
			} else {
				totalIn = uncachedIn + cachedOther
			}
		} else {
			totalIn = uncachedIn
		}
		if totalIn <= 0 {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(step.CreatedAt))
		if err != nil {
			ts, _ = time.Parse(time.RFC3339, strings.TrimSpace(step.CreatedAt))
		}
		steps = append(steps, parsedTranscriptPlannerStep{
			stepIndex:    step.StepIndex,
			createdAt:    ts,
			inputTokens:  totalIn,
			cachedTokens: cachedTok,
			outputTokens: outTok,
		})
	}
	transcriptStepsCacheMu.Lock()
	if len(transcriptStepsCache) > 256 {
		transcriptStepsCache = make(map[string]cachedTranscriptStepsEntry, 64)
	}
	transcriptStepsCache[transcriptPath] = cachedTranscriptStepsEntry{
		modTimeNano: modNano,
		size:        size,
		steps:       steps,
	}
	transcriptStepsCacheMu.Unlock()
	return steps
}

func rescaleAuditSnapshotItems(items []auditContextItem, inputTokens int64) []auditContextItem {
	if inputTokens <= 0 || len(items) == 0 {
		return items
	}
	var fixedSum, dynSum int64
	for _, it := range items {
		if it.Category == "system_prompt" || it.Category == "tools" || it.Category == "skills" {
			fixedSum += it.Tokens
		} else {
			dynSum += it.Tokens
		}
	}
	rawSum := fixedSum + dynSum
	if rawSum <= 0 || rawSum == inputTokens {
		return items
	}
	out := make([]auditContextItem, len(items))
	copy(out, items)
	if dynSum > 0 && fixedSum < (inputTokens*75)/100 {
		remDyn := inputTokens - fixedSum
		for i := range out {
			cat := out[i].Category
			if cat == "system_prompt" || cat == "tools" || cat == "skills" {
				continue
			}
			scaled := (out[i].Tokens * remDyn) / dynSum
			if scaled < 1 {
				scaled = 1
			}
			out[i].Tokens = scaled
		}
	} else {
		for i := range out {
			scaled := (out[i].Tokens * inputTokens) / rawSum
			if scaled < 1 {
				scaled = 1
			}
			out[i].Tokens = scaled
		}
	}
	return out
}

// SyncTranscriptTokens reconciles recent audit_logs rows against the real PLANNER_RESPONSE
// token telemetry (input_tokens + cache_read_tokens, cache_read_tokens, output_tokens)
// written by the ACP backend in transcript.jsonl.
func SyncTranscriptTokens(dataDir string, store *storage.Store, limit int) int {
	if store == nil || dataDir == "" {
		return 0
	}
	if limit <= 0 {
		limit = 40
	}
	logs, err := store.ListAuditLogsForSync(limit)
	if err != nil || len(logs) == 0 {
		return 0
	}

	type sessKey struct {
		accountID string
		sessionID string
	}
	type stepKey struct {
		accountID string
		sessionID string
		stepIndex int
	}
	stepsCache := make(map[sessKey][]parsedTranscriptPlannerStep)
	seenStep := make(map[stepKey]bool)
	updatedCount := 0

	// Process logs from oldest to newest so the primary LLM request claims each (session, step_index)
	// first, and all subsequent tool-call dispatches sharing that step_index are marked as sub-tool steps.
	for idx := len(logs) - 1; idx >= 0; idx-- {
		l := logs[idx]
		sessID := strings.TrimSpace(l.SessionID)
		if sessID == "" {
			continue
		}
		accID := strings.TrimSpace(l.AccountID)
		if accID == "" {
			accID = "default"
		}
		key := sessKey{accountID: accID, sessionID: sessID}
		steps, exists := stepsCache[key]
		if !exists {
			steps = readTranscriptPlannerSteps(dataDir, accID, sessID)
			stepsCache[key] = steps
		}
		if len(steps) == 0 {
			continue
		}

		var snap auditContextSnapshot
		hasValidSnap := false
		if strings.TrimSpace(l.ContextJSON) != "" {
			if json.Unmarshal([]byte(l.ContextJSON), &snap) == nil {
				hasValidSnap = true
			}
		}

		// Detect sub-tool dispatches from an already-generated PLANNER_RESPONSE step:
		// they have no thinking/response text and fast completion (<=1500ms) while unparking a tool call.
		isFastToolOnly := strings.TrimSpace(l.ToolCallsJSON) != "" &&
			strings.TrimSpace(l.ThinkingText) == "" &&
			strings.TrimSpace(l.ResponseText) == "" &&
			l.LatencyMs > 0 && l.LatencyMs <= 1500 &&
			(snap.TTFBMs <= 80)
		isSubTool := isFastToolOnly

		// Compute request start time. In transcript.jsonl, PLANNER_RESPONSE.created_at is the
		// step START timestamp truncated to whole seconds (YYYY-MM-DDTHH:MM:SSZ).
		// Any PLANNER_RESPONSE whose created_at equals l.CreatedAt.Truncate(time.Second) (when
		// reqStart was in an earlier second) actually started in the same whole second that Turn K
		// finished — i.e., it belongs to Turn K+1, not Turn K.
		reqStart := l.CreatedAt
		if l.LatencyMs > 0 {
			reqStart = l.CreatedAt.Add(-time.Duration(l.LatencyMs) * time.Millisecond)
		}
		var cutoff time.Time
		if isSubTool {
			// A sub-tool dispatch belongs to the PLANNER_RESPONSE that started BEFORE this sub-request began.
			cutoff = reqStart.Add(50 * time.Millisecond)
		} else {
			startSec := reqStart.Truncate(time.Second)
			endSec := l.CreatedAt.Truncate(time.Second)
			if startSec.Before(endSec) {
				cutoff = endSec.Add(-1 * time.Millisecond)
			} else {
				cutoff = l.CreatedAt
			}
		}

		var matched *parsedTranscriptPlannerStep
		for i := range steps {
			s := &steps[i]
			if !s.createdAt.IsZero() && !s.createdAt.After(cutoff) {
				matched = s
			} else if !s.createdAt.IsZero() && s.createdAt.After(cutoff) {
				break
			}
		}
		if matched == nil || matched.inputTokens <= 0 {
			continue
		}

		if matched.stepIndex > 0 {
			sk := stepKey{accountID: accID, sessionID: sessID, stepIndex: matched.stepIndex}
			if seenStep[sk] {
				isSubTool = true
			} else {
				seenStep[sk] = true
			}
		}

		targetContextPrompt := matched.inputTokens
		targetContextCached := matched.cachedTokens
		auditPrompt := targetContextPrompt
		auditCached := targetContextCached
		if isSubTool {
			auditPrompt = 0
			auditCached = 0
		}

		pureOut := estimatePureOutputTokens(l.ResponseText, l.ToolCallsJSON)
		targetOut := l.CompletionTokens
		if pureOut > 0 {
			targetOut = pureOut
			if matched.outputTokens > 0 && matched.outputTokens < targetOut {
				targetOut = matched.outputTokens
			}
		} else if matched.outputTokens > 0 && (targetOut <= 0 || matched.outputTokens < targetOut) {
			targetOut = matched.outputTokens
		}
		if targetOut < 1 {
			targetOut = 1
		}

		needUpdate := l.PromptTokens != auditPrompt || l.CachedTokens != auditCached || l.CompletionTokens != targetOut
		if hasValidSnap {
			if snap.InputTokens != targetContextPrompt || snap.CachedTokens != targetContextCached || snap.OutputTokens != targetOut || snap.SubToolStep != isSubTool || (matched.stepIndex > 0 && snap.StepIndex != matched.stepIndex) {
				needUpdate = true
			}
		}
		if !needUpdate {
			continue
		}

		var updatedCtxJSON string
		if hasValidSnap {
			snap.InputTokens = targetContextPrompt
			snap.CachedTokens = targetContextCached
			snap.OutputTokens = targetOut
			if matched.stepIndex > 0 {
				snap.StepIndex = matched.stepIndex
			}
			snap.SubToolStep = isSubTool
			snap.Items = rescaleAuditSnapshotItems(snap.Items, targetContextPrompt)
			if raw, err := json.Marshal(snap); err == nil {
				updatedCtxJSON = string(raw)
			}
		}
		if err := store.UpdateAuditLogTokens(l.RequestID, auditPrompt, auditCached, targetOut, updatedCtxJSON); err == nil {
			updatedCount++
		}
	}
	return updatedCount
}
