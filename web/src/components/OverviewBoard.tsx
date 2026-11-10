import React, { useMemo, useState } from 'react'
import {
  ChevronDown,
  ChevronRight,
  Cpu,
  Grid,
  LayoutGrid,
  RefreshCw,
  Sparkles,
  Terminal,
} from 'lucide-react'
import {
  AccountSnapshot,
  AuditContextSnapshot,
  AuditLogRecord,
  OverviewResponse,
} from '../api'
import { formatCompact, formatDurationCn } from '../lib/format'
import type { TreemapDetailKey } from './OverviewDetailModal'
import {
  Badge,
  Button,
  Frame,
  FramePanel,
  InlineError,
  Skeleton,
} from './reui/primitives'

export type RequestContextCategory =
  | 'all'
  | 'system_prompt'
  | 'tools'
  | 'skills'
  | 'files'
  | 'tool_results'
  | 'conversation'

export interface NormalizedContextEntry {
  id: string
  category: Exclude<RequestContextCategory, 'all'>
  tag: string
  title: string
  subtitle: string
  tokens: number
  count?: number
  content?: string
  dotClass: string
  barClass: string
}

export interface ContextModuleSelection {
  category: Exclude<RequestContextCategory, 'all'> | 'remaining_capacity'
  title: string
  subtitle?: string
  tag?: string
  tokens: number
  count?: number
  items: NormalizedContextEntry[]
}

interface OverviewBoardProps {
  overview: OverviewResponse | null
  accounts: AccountSnapshot[]
  auditLogs: AuditLogRecord[]
  selectedAuditLog: AuditLogRecord | undefined
  onSelectAuditLogId: (id: string) => void
  gatewayHostPort: string
  loading: boolean
  loadError: string
  syncError: string
  fleetUsageSummary: {
    enabledCount: number
    readyCount: number
    totalRequests: number
    requests24h: number
    errorRequests: number
    promptTokens: number
    completionTokens: number
    totalTokens: number
    tokens24h: number
  }
  onOpenModal: (
    key: TreemapDetailKey,
    logId?: string,
    moduleSelection?: ContextModuleSelection
  ) => void
  onNavigateTab: (tab: 'overview' | 'accounts' | 'clients' | 'keys') => void
  onRefresh: () => void
}

const CATEGORY_STYLE: Record<
  Exclude<RequestContextCategory, 'all'>,
  { dotClass: string; barClass: string; label: string }
> = {
  system_prompt: {
    dotClass: 'bg-violet-500',
    barClass: 'bg-violet-500',
    label: '系统提示词',
  },
  tools: {
    dotClass: 'bg-sky-500',
    barClass: 'bg-sky-500',
    label: '工具',
  },
  skills: {
    dotClass: 'bg-slate-500',
    barClass: 'bg-slate-500',
    label: '技能',
  },
  files: {
    dotClass: 'bg-emerald-500',
    barClass: 'bg-emerald-500',
    label: '文件',
  },
  tool_results: {
    dotClass: 'bg-orange-500',
    barClass: 'bg-orange-500',
    label: '工具结果',
  },
  conversation: {
    dotClass: 'bg-indigo-500',
    barClass: 'bg-indigo-500',
    label: '会话内容',
  },
}

export function formatClientShortName(
  proto?: string,
  keyName?: string,
  log?: AuditLogRecord | null
): string {
  if (log) {
    const sys = log.system_prompt || ''
    const prompt = log.prompt_text || ''
    if (
      sys.includes('You are Sailry') ||
      sys.includes('data from Sailry') ||
      prompt.includes('<turn-context>')
    ) {
      return 'Sailry'
    }
  }
  if (keyName && keyName.trim()) {
    return keyName.trim()
  }
  if (proto) {
    const p = proto.toLowerCase()
    if (p.includes('responses')) return 'Responses API'
    if (p.includes('claude') || p.includes('anthropic')) return 'Claude API'
    if (p.includes('openai')) return 'OpenAI API'
    if (p.includes('acp')) return 'ACP Studio'
    return proto
  }
  return '—'
}

export function getModelContextCapacity(modelName: string): number {
  const lower = (modelName || '').toLowerCase()
  if (lower.includes('gpt-oss')) {
    return 128_000
  }
  if (lower.includes('claude')) {
    return 200_000
  }
  if (lower.includes('gpt-6') || lower.includes('codex') || lower.includes('o3') || lower.includes('gpt-5')) {
    return 272_000
  }
  if (lower.includes('gemini')) {
    return 1_048_576
  }
  return 1_048_576
}

export function resolveEffectiveContextCapacity(
  modelName: string,
  snap?: AuditContextSnapshot | null
): number {
  if (snap?.client_context_window && snap.client_context_window > 0) {
    return snap.client_context_window
  }
  const modelDefault = getModelContextCapacity(modelName)
  if (snap?.context_capacity && snap.context_capacity > 0) {
    const lower = (modelName || '').toLowerCase()
    const isLegacyHardcoded272k =
      snap.context_capacity === 272_000 &&
      (lower.includes('gemini') || lower.includes('gpt-oss') || !lower)
    if (!isLegacyHardcoded272k) {
      return snap.context_capacity
    }
  }
  return modelDefault
}

const CACHE_LIMIT = 256
const parsedSnapshotCache = new Map<string, AuditContextSnapshot | null>()
const promptGoalCache = new Map<string, string>()
const sessionTitleCache = new Map<string, string>()

function boundedCacheSet<V>(cache: Map<string, V>, key: string, value: V): void {
  if (!cache.has(key) && cache.size >= CACHE_LIMIT) {
    const oldestKey = cache.keys().next().value
    if (oldestKey !== undefined) {
      cache.delete(oldestKey)
    }
  }
  cache.set(key, value)
}

function extractSessionTitleFromText(rawText?: string): string {
  if (!rawText) return ''
  const assignedMatch = rawText.match(
    /Session title\s*\r?\n\s*Current assigned title(?:\s*\([^)]*\))?:\s*"((?:[^"\\]|\\.)+)"/i
  )
  if (assignedMatch?.[1]?.trim()) {
    try {
      return JSON.parse(`"${assignedMatch[1]}"`).trim()
    } catch {
      return assignedMatch[1].trim()
    }
  }
  const metaMatch = rawText.match(
    /(?:session_title|conversation_title)\s*[:=]\s*"((?:[^"\\]|\\.)+)"/i
  )
  if (metaMatch?.[1]?.trim()) {
    return metaMatch[1].trim()
  }
  return ''
}

export function extractSessionTitle(
  log?: AuditLogRecord,
  snap?: AuditContextSnapshot | null,
  sessionLogs?: AuditLogRecord[]
): string {
  if (snap?.session_title && snap.session_title.trim()) {
    return snap.session_title.trim()
  }
  const cacheKey = log?.request_id
    ? `${log.request_id}:${log.prompt_text?.length ?? 0}:${sessionLogs?.length ?? 0}`
    : ''
  if (cacheKey && sessionTitleCache.has(cacheKey)) {
    return sessionTitleCache.get(cacheKey)!
  }
  let found =
    extractSessionTitleFromText(log?.prompt_text) ||
    extractSessionTitleFromText(log?.system_prompt)
  if (!found && log?.tool_calls_json && log.tool_calls_json.includes('set_session_title')) {
    const toolMatch = log.tool_calls_json.match(/"title"\s*:\s*"((?:[^"\\]|\\.)+)"/)
    if (toolMatch?.[1]?.trim()) {
      found = toolMatch[1].trim()
    }
  }
  if (!found && sessionLogs && sessionLogs.length > 0) {
    for (const sLog of sessionLogs) {
      const sSnap = parseAuditContextSnapshot(sLog)
      if (sSnap?.session_title && sSnap.session_title.trim()) {
        found = sSnap.session_title.trim()
        break
      }
      const fromText =
        extractSessionTitleFromText(sLog.prompt_text) ||
        extractSessionTitleFromText(sLog.system_prompt)
      if (fromText) {
        found = fromText
        break
      }
    }
  }
  if (cacheKey && found) {
    boundedCacheSet(sessionTitleCache, cacheKey, found)
  }
  return found
}

export function formatShortSessionId(rawId?: string): string {
  if (!rawId) return '00000000'
  return rawId.replace(/^(?:req_|resp_|sess_)/, '').slice(0, 8)
}

export function parseAuditContextSnapshot(
  log?: AuditLogRecord | null
): AuditContextSnapshot | null {
  if (!log?.context_json) return null
  const cacheKey = `${log.request_id}:${log.context_json.length}:${log.cached_tokens ?? 0}:${log.completion_tokens ?? 0}`
  if (parsedSnapshotCache.has(cacheKey)) {
    return parsedSnapshotCache.get(cacheKey) ?? null
  }
  let parsed: AuditContextSnapshot | null = null
  try {
    parsed = JSON.parse(log.context_json) as AuditContextSnapshot
  } catch {
    parsed = null
  }
  boundedCacheSet(parsedSnapshotCache, cacheKey, parsed)
  return parsed
}

function estimateTextTokens(text?: string): number {
  const trimmed = (text || '').trim()
  if (!trimmed) return 0
  const utf8Bytes = new TextEncoder().encode(trimmed).length
  const runes = Array.from(trimmed).length
  return Math.max(1, Math.floor((utf8Bytes + runes + 6) / 7))
}

function estimateToolCallsTokens(toolCallsJson?: string): number {
  const trimmed = (toolCallsJson || '').trim()
  if (!trimmed) return 0
  try {
    const parsed = JSON.parse(trimmed)
    if (Array.isArray(parsed)) {
      let sum = 0
      for (const item of parsed) {
        const argStr =
          typeof item?.args_json === 'string'
            ? item.args_json
            : typeof item?.arguments === 'string'
              ? item.arguments
              : ''
        sum += estimateTextTokens(argStr) + 6
      }
      if (sum > 0) return sum
    } else if (parsed && typeof parsed === 'object') {
      const argStr =
        typeof parsed.arguments === 'string'
          ? parsed.arguments
          : typeof parsed.args_json === 'string'
            ? parsed.args_json
            : ''
      if (argStr) return estimateTextTokens(argStr) + 6
    }
  } catch {
    // Fall through to raw text estimate
  }
  return estimateTextTokens(trimmed)
}

export function resolvePureOutputTokens(
  log?: AuditLogRecord | null,
  snap?: AuditContextSnapshot | null
): number {
  if (!log) return 0
  const recorded =
    snap?.output_tokens && snap.output_tokens > 0
      ? snap.output_tokens
      : log.completion_tokens || 0
  const pureEst =
    estimateTextTokens(log.response_text) + estimateToolCallsTokens(log.tool_calls_json)
  if (pureEst > 0) {
    return recorded > 0 ? Math.min(recorded, pureEst) : pureEst
  }
  return recorded
}

export interface RequestContextUsage {
  parsedSnap: AuditContextSnapshot | null
  inputTokens: number
  outputTokens: number
  totalTokens: number
  contextCapacity: number
  usedContextTokens: number
  usedPct: number
  remainingTokens: number
  estRemainingReqs: number | null
}

export function computeRequestContextUsage(log?: AuditLogRecord | null): RequestContextUsage {
  const parsedSnap = parseAuditContextSnapshot(log)
  const inputTokens =
    parsedSnap?.input_tokens && parsedSnap.input_tokens > 0
      ? parsedSnap.input_tokens
      : log?.prompt_tokens || 0
  const outputTokens = resolvePureOutputTokens(log, parsedSnap)
  const totalTokens = Math.max(inputTokens + outputTokens, log?.total_tokens || 0)
  const baseModelWindow = resolveEffectiveContextCapacity(log?.model_name || '', parsedSnap)
  const usedContextTokens = inputTokens > 0 ? inputTokens : totalTokens
  const contextCapacity =
    usedContextTokens > baseModelWindow
      ? Math.ceil((usedContextTokens * 1.15) / 10_000) * 10_000
      : baseModelWindow
  const usedPct =
    contextCapacity > 0 ? Math.min(100, Math.round((usedContextTokens / contextCapacity) * 100)) : 0
  const remainingTokens = Math.max(0, contextCapacity - usedContextTokens)
  const perTurnDelta = Math.max(800, outputTokens + Math.round(inputTokens * 0.06))
  const estRemainingReqs =
    usedContextTokens > 0 ? Math.max(1, Math.round(remainingTokens / perTurnDelta)) : null
  return {
    parsedSnap,
    inputTokens,
    outputTokens,
    totalTokens,
    contextCapacity,
    usedContextTokens,
    usedPct,
    remainingTokens,
    estRemainingReqs,
  }
}

export function resolveSessionLogs(
  activeLog: AuditLogRecord | undefined,
  auditLogs: AuditLogRecord[]
): AuditLogRecord[] {
  if (!activeLog) return []
  if (!activeLog.session_id) return [activeLog]
  const matched = auditLogs.filter((l) => l.session_id === activeLog.session_id)
  return matched.length > 0 ? matched : [activeLog]
}

export function formatPromptModeShort(mode?: string): string {
  switch (mode) {
    case 'pure_passthrough':
      return '纯透传'
    case 'prepend_global':
      return '全局前置'
    case 'native_hybrid':
      return '原生混合'
    default:
      return mode || '—'
  }
}

function cleanPromptSnippet(rawText: string): string {
  return rawText
    .replace(/<turn-context>[\s\S]*?(?:<\/turn-context>|$)/gi, '')
    .replace(/\n?\.\.\.\[(?:truncated|middle of large input truncated|historical output compressed)\]\.\.\.?/gi, '')
    .replace(/\n?\.\.\.\[truncated\]$/gi, '')
    .trim()
}

function extractUserGoalFromRawPrompt(rawText?: string): string {
  const cleanedRaw = cleanPromptSnippet(rawText || '')
  if (!cleanedRaw) return ''

  // Check if there is an explicit [Current User Goal] section
  const currentGoalMatch = cleanedRaw.match(/\[Current User Goal\]\s*\n+([^\n]+)/i)
  if (currentGoalMatch?.[1]?.trim()) {
    return currentGoalMatch[1].trim()
  }

  // If this is a pure [tool_result:...] step without conversation history, return empty so caller can check sessionLogs
  if (/^\[tool_result:[^\]]+\]/i.test(cleanedRaw) && !cleanedRaw.includes('[USER]:')) {
    return ''
  }

  // Extract from [USER]: blocks if present
  const userBlocks = cleanedRaw.split(/\[USER\]:\s*\n/i)
  if (userBlocks.length > 1) {
    for (let i = userBlocks.length - 1; i >= 1; i--) {
      const block = userBlocks[i].split(/\n\[(?:ASSISTANT|TOOL)\]:|\n<\/conversation_history>/i)[0] || ''
      if (block.includes('# Handoff Summary') || block.includes('# Resuming from a compaction')) {
        continue
      }
      const lines = block
        .split('\n')
        .map((l) => l.trim())
        .filter(
          (l) =>
            l &&
            !l.startsWith('Attachment reference:') &&
            !l.startsWith('- [Attachment:') &&
            !l.startsWith('<conversation_history>') &&
            !l.startsWith('</conversation_history>')
        )
      if (lines.length > 0) {
        return lines[0]
      }
    }
  }

  const lines = cleanedRaw
    .split('\n')
    .map((l) => l.trim())
    .filter(
      (l) =>
        l &&
        !l.startsWith('[USER]') &&
        !l.startsWith('[ASSISTANT]') &&
        !l.startsWith('[TOOL]') &&
        !l.startsWith('<conversation_history>') &&
        !l.startsWith('</conversation_history>') &&
        !l.startsWith('# Resuming from a compaction') &&
        !l.startsWith('# Handoff Summary') &&
        !l.startsWith('Attachment reference:') &&
        !l.startsWith('Please continue fulfilling') &&
        !/^\.\.\.\[.*\]\.\.\.$/.test(l)
    )
  if (lines.length === 0) return ''
  // Pick the last non-tool-result line or first clean user line
  for (let i = lines.length - 1; i >= 0; i--) {
    const ln = lines[i]
    if (!/^\[tool_result:/i.test(ln) && !ln.startsWith('Created At:') && !ln.startsWith('Completed At:')) {
      return ln.replace(/^\[user\]\s*/i, '').trim()
    }
  }
  return lines[0].replace(/^\[(?:user|tool_result:[^\]]+)\]\s*/i, '').trim()
}

function extractPromptGoalSummary(
  log?: AuditLogRecord,
  snap?: AuditContextSnapshot | null,
  sessionLogs?: AuditLogRecord[]
): string {
  const cacheKey = log?.request_id
    ? `${log.request_id}:${snap?.prompt_summary?.length ?? 0}:${log.prompt_text?.length ?? 0}:${sessionLogs?.length ?? 0}`
    : ''
  if (cacheKey && promptGoalCache.has(cacheKey)) {
    return promptGoalCache.get(cacheKey)!
  }
  if (snap?.prompt_summary && snap.prompt_summary.trim()) {
    const s = cleanPromptSnippet(snap.prompt_summary)
    if (s && !s.startsWith('...[truncated]')) {
      const res = s.length > 36 ? `${s.slice(0, 36)}...` : s
      if (cacheKey) boundedCacheSet(promptGoalCache, cacheKey, res)
      return res
    }
  }
  let candidate = extractUserGoalFromRawPrompt(log?.prompt_text)
  if (!candidate && sessionLogs && sessionLogs.length > 0) {
    for (const sLog of sessionLogs) {
      const fromSess = extractUserGoalFromRawPrompt(sLog.prompt_text)
      if (fromSess) {
        candidate = fromSess
        break
      }
    }
  }
  if (!candidate) {
    const raw = cleanPromptSnippet(log?.prompt_text || '')
    if (!raw) return '等待或处理客户端请求'
    const firstLine = raw.split('\n')[0]?.replace(/^\[(?:user|tool_result:[^\]]+)\]\s*/i, '').trim()
    candidate = firstLine || '执行智能体任务'
  }
  const finalSummary = candidate.length > 36 ? `${candidate.slice(0, 36)}...` : candidate
  if (cacheKey) {
    boundedCacheSet(promptGoalCache, cacheKey, finalSummary)
  }
  return finalSummary
}

interface ContextTreemapViewProps {
  contextCapacity: number
  remainingTokens: number
  categoryTotals: Record<Exclude<RequestContextCategory, 'all'>, number>
  groupedForTreemap: Record<Exclude<RequestContextCategory, 'all'>, NormalizedContextEntry[]>
  activeLogId?: string
  disableBuiltinTools?: boolean
  promptMode?: string
  onOpenModal: (
    key: TreemapDetailKey,
    logId?: string,
    moduleSelection?: ContextModuleSelection
  ) => void
}

const ContextTreemapView: React.FC<ContextTreemapViewProps> = React.memo(
  ({
    contextCapacity,
    remainingTokens,
    categoryTotals,
    groupedForTreemap,
    activeLogId,
    disableBuiltinTools,
    promptMode,
    onOpenModal,
  }) => {
    const cap = Math.max(1, contextCapacity)
    const calcPct = (tok: number) => (tok <= 0 ? 0 : Math.max(1, Math.round((tok / cap) * 100)))

    const tileHoverClass = (activeBorderClass: string) =>
      `group/tile transition-opacity duration-200 ease-[cubic-bezier(0.22,1,0.36,1)] motion-reduce:transition-none group-hover/treemap:opacity-35 hover:!opacity-100 hover:z-20 focus-visible:!opacity-100 focus-visible:z-20 ${activeBorderClass}`

    const renderHoverPopover = (
      titleText: string,
      categoryLabel: string,
      tokens: number,
      dotColorClass: string,
      alignRight = false
    ) => (
      <div
        className={`pointer-events-none invisible opacity-0 group-hover/tile:visible group-hover/tile:opacity-100 group-focus-visible/tile:visible group-focus-visible/tile:opacity-100 transition-opacity duration-200 ease-[cubic-bezier(0.22,1,0.36,1)] motion-reduce:transition-none absolute bottom-full mb-2 z-30 min-w-[180px] whitespace-nowrap rounded-xl border border-white/10 bg-zinc-900/95 px-3.5 py-2.5 text-start text-zinc-50 shadow-lg ${
          alignRight ? 'right-0' : 'left-0'
        }`}
      >
        <div className="flex items-center gap-2 text-xs font-bold text-zinc-50">
          <span className={`size-2.5 shrink-0 rounded-xs ${dotColorClass}`} />
          <span>{titleText}</span>
        </div>
        <div className="mt-0.5 text-xs text-zinc-400 tabular-nums">
          {`${categoryLabel} · ${formatCompact(tokens)} token · ${calcPct(tokens)}%`}
        </div>
      </div>
    )

    const sysMainItem = groupedForTreemap.system_prompt[0]
    const sysMainTitle =
      sysMainItem?.title ||
      (promptMode === 'pure_passthrough' ? '客户端系统提示词' : 'agent 提示词')
    const sysMainTokens = sysMainItem?.tokens || categoryTotals.system_prompt
    const sysSubItems = groupedForTreemap.system_prompt.slice(1)

    const hasTools = groupedForTreemap.tools.length > 0 && categoryTotals.tools > 0
    const toolMainItem = groupedForTreemap.tools[0]
    const toolMainTitle = hasTools
      ? toolMainItem?.title || '客户端工具定义'
      : disableBuiltinTools
        ? '已禁用内置工具'
        : '无工具调用'
    const toolMainTokens = hasTools ? toolMainItem?.tokens || categoryTotals.tools : 0
    const toolSubItems = groupedForTreemap.tools.slice(1)

    const skillMainItem = groupedForTreemap.skills[0]
    const skillTitle =
      skillMainItem?.title ||
      (promptMode === 'pure_passthrough' ? '未注入全局技能 (纯透传)' : 'AGENTS.md 与技能规范')
    const skillTokens = categoryTotals.skills

    const fileMain = groupedForTreemap.files[0]
    const fileMainTitle = fileMain?.title || (categoryTotals.files > 0 ? '工作区文件上下文' : '无文件引用')
    const fileMainTokens = fileMain?.tokens || categoryTotals.files

    const toolResMain = groupedForTreemap.tool_results[0]
    const toolResMainTitle = toolResMain?.title || '工具结果'
    const toolResMainTokens = toolResMain?.tokens || categoryTotals.tool_results
    const toolResSubItems = groupedForTreemap.tool_results.slice(1)

    const openModule = (
      modalKey: TreemapDetailKey,
      selection: ContextModuleSelection
    ) => {
      onOpenModal(modalKey, activeLogId, selection)
    }

    return (
      <div className="group/treemap flex flex-col gap-1.5 lg:flex-row lg:h-64 min-w-0">
        {/* Column 1: 系统提示词 (Violet) + 工具 (Sky) */}
        <div className="lg:w-[17%] min-w-0 flex flex-col gap-1.5 min-h-[200px]">
          {/* System Prompt Main + Sub */}
          <div
            className={`${
              sysSubItems.length > 0 && toolSubItems.length === 0 ? 'flex-[1.35]' : 'flex-1'
            } min-h-0 min-w-0 flex flex-col gap-1`}
          >
            <button
              type="button"
              onClick={() =>
                openModule('system_prompt', {
                  category: 'system_prompt',
                  title: sysMainTitle,
                  subtitle: sysMainItem?.subtitle || '系统提示词',
                  tag: sysMainItem?.tag || 'system',
                  tokens: sysMainTokens,
                  count: sysMainItem?.count,
                  items: sysMainItem ? [sysMainItem] : [],
                })
              }
              className={`relative flex-1 min-h-0 min-w-0 rounded-xl border border-violet-500/35 bg-violet-500/14 dark:bg-violet-500/18 p-2.5 flex flex-col justify-start text-start cursor-pointer hover:bg-violet-500/24 ${tileHoverClass(
                'hover:border-violet-500/80 hover:ring-1 hover:ring-violet-500/35'
              )}`}
            >
              {renderHoverPopover(
                sysMainTitle,
                '系统提示词',
                sysMainTokens,
                'bg-violet-500'
              )}
              <div className="w-full min-w-0 overflow-hidden">
                <div className="text-xs font-bold text-violet-700 dark:text-violet-100 truncate w-full">
                  {sysMainTitle}
                </div>
                <div className="text-xs text-violet-600/90 dark:text-violet-200/85 truncate w-full tabular-nums">
                  {`${formatCompact(sysMainTokens)} · 系统提示词`}
                </div>
              </div>
            </button>

            {sysSubItems.length > 0 && (
              <div className="flex gap-1 h-12 min-w-0 shrink-0">
                {sysSubItems.slice(0, 2).map((subItem, idx) => {
                  const isSummary = idx === 1 && sysSubItems.length > 2
                  const restItems = sysSubItems.slice(1)
                  const tok = isSummary
                    ? restItems.reduce((acc, it) => acc + it.tokens, 0)
                    : subItem.tokens
                  const label = isSummary
                    ? `另外 ${restItems.length} 项系统提示词`
                    : subItem.title
                  const flexWeight = Math.max(4, Math.min(16, label.length))
                  return (
                    <button
                      key={subItem.id}
                      type="button"
                      style={{ flex: `${flexWeight} 1 0%` }}
                      onClick={() =>
                        openModule('system_prompt', {
                          category: 'system_prompt',
                          title: label,
                          subtitle: isSummary ? '系统提示词附加项' : subItem.subtitle || '系统提示词',
                          tag: subItem.tag || 'system',
                          tokens: tok,
                          count: isSummary ? restItems.length : subItem.count,
                          items: isSummary ? restItems : [subItem],
                        })
                      }
                      className={`relative min-w-0 rounded-xl border border-violet-500/35 bg-violet-500/14 dark:bg-violet-500/18 px-2 py-1 flex flex-col justify-center text-start cursor-pointer hover:bg-violet-500/24 ${tileHoverClass(
                        'hover:border-violet-500/80 hover:ring-1 hover:ring-violet-500/35'
                      )}`}
                    >
                      {renderHoverPopover(label, '系统提示词', tok, 'bg-violet-500')}
                      <div className="w-full min-w-0 overflow-hidden">
                        <div className="text-xs font-bold text-violet-700 dark:text-violet-100 truncate w-full">
                          {label}
                        </div>
                        <div className="text-xs text-violet-600/90 dark:text-violet-200/85 truncate w-full tabular-nums">
                          {formatCompact(tok)}
                        </div>
                      </div>
                    </button>
                  )
                })}
              </div>
            )}
          </div>

          {/* Tools Main + Sub */}
          <div
            className={`${
              toolSubItems.length > 0 && sysSubItems.length === 0 ? 'flex-[1.35]' : 'flex-1'
            } min-h-0 min-w-0 flex flex-col gap-1`}
          >
            <button
              type="button"
              onClick={() =>
                openModule('protocols', {
                  category: 'tools',
                  title: toolMainTitle,
                  subtitle: toolMainItem?.subtitle || '客户端工具定义',
                  tag: toolMainItem?.tag || 'tool',
                  tokens: toolMainTokens,
                  count: toolMainItem?.count,
                  items: toolMainItem ? [toolMainItem] : [],
                })
              }
              className={`relative flex-1 min-h-0 min-w-0 rounded-xl border border-sky-500/35 bg-sky-500/14 dark:bg-sky-500/18 p-2.5 flex flex-col justify-start text-start cursor-pointer hover:bg-sky-500/24 ${tileHoverClass(
                'hover:border-sky-500/80 hover:ring-1 hover:ring-sky-500/35'
              )}`}
            >
              {renderHoverPopover(toolMainTitle, '工具', toolMainTokens, 'bg-sky-500')}
              <div className="w-full min-w-0 overflow-hidden">
                <div className="text-xs font-bold text-sky-700 dark:text-sky-100 truncate w-full">
                  {toolMainTitle}
                </div>
                <div className="text-xs text-sky-600/90 dark:text-sky-200/85 truncate w-full tabular-nums">
                  {`${formatCompact(toolMainTokens)} · 工具`}
                </div>
              </div>
            </button>

            {toolSubItems.length > 0 && (
              <div className="flex gap-1 h-12 min-w-0 shrink-0">
                {toolSubItems.slice(0, 2).map((subTool, idx) => {
                  const isSummary = idx === 1 && toolSubItems.length > 2
                  const restTools = toolSubItems.slice(1)
                  const tok = isSummary
                    ? restTools.reduce((acc, it) => acc + it.tokens, 0)
                    : subTool.tokens
                  const label = isSummary ? `另外 ${restTools.length} 项工具` : subTool.title
                  const flexWeight = Math.max(4, Math.min(16, label.length))
                  return (
                    <button
                      key={subTool.id}
                      type="button"
                      style={{ flex: `${flexWeight} 1 0%` }}
                      onClick={() =>
                        openModule('protocols', {
                          category: 'tools',
                          title: label,
                          subtitle: isSummary ? '客户端 MCP 工具定义' : subTool.subtitle || '工具定义',
                          tag: subTool.tag || 'tool',
                          tokens: tok,
                          count: isSummary ? restTools.length : subTool.count,
                          items: isSummary ? restTools : [subTool],
                        })
                      }
                      className={`relative min-w-0 rounded-xl border border-sky-500/35 bg-sky-500/14 dark:bg-sky-500/18 px-2 py-1 flex flex-col justify-center text-start cursor-pointer hover:bg-sky-500/24 ${tileHoverClass(
                        'hover:border-sky-500/80 hover:ring-1 hover:ring-sky-500/35'
                      )}`}
                    >
                      {renderHoverPopover(label, '工具', tok, 'bg-sky-500')}
                      <div className="w-full min-w-0 overflow-hidden">
                        <div className="text-xs font-bold text-sky-700 dark:text-sky-100 truncate w-full">
                          {label}
                        </div>
                        <div className="text-xs text-sky-600/90 dark:text-sky-200/85 truncate w-full tabular-nums">
                          {formatCompact(tok)}
                        </div>
                      </div>
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        </div>

        {/* Thin Vertical Pillar: 技能 (Slate Pill Column between Col 1 and Col 2) */}
        <button
          type="button"
          onClick={() =>
            openModule('system_prompt', {
              category: 'skills',
              title: skillTitle,
              subtitle: skillMainItem?.subtitle || '项目指令与技能规范',
              tag: skillMainItem?.tag || 'skill',
              tokens: skillTokens,
              count: skillMainItem?.count,
              items: groupedForTreemap.skills,
            })
          }
          aria-label={`技能 ${formatCompact(skillTokens)}`}
          className={`relative hidden lg:flex lg:w-3.5 shrink-0 rounded-xl border border-slate-500/35 bg-slate-500/14 dark:bg-slate-500/20 cursor-pointer hover:bg-slate-500/26 ${tileHoverClass(
            'hover:border-slate-500/80 hover:ring-1 hover:ring-slate-500/35'
          )}`}
        >
          {renderHoverPopover(skillTitle, '技能', skillTokens, 'bg-slate-500')}
        </button>

        {/* Column 2: 文件 (Emerald Green Multi-Tile Mosaic) */}
        <div className="lg:w-[35%] min-w-0 grid grid-cols-12 gap-1.5 min-h-[200px]">
          <button
            type="button"
            onClick={() =>
              openModule('prompt_input', {
                category: 'files',
                title: fileMainTitle,
                subtitle: fileMain?.subtitle || '工作区文件',
                tag: fileMain?.tag || 'file',
                tokens: fileMainTokens,
                count: fileMain?.count,
                items: fileMain ? [fileMain] : [],
              })
            }
            className={`relative min-w-0 min-h-0 ${
              groupedForTreemap.files.length > 1 ? 'col-span-7' : 'col-span-12'
            } rounded-xl border border-emerald-500/35 bg-emerald-500/14 dark:bg-emerald-500/18 p-3 flex flex-col justify-start text-start cursor-pointer hover:bg-emerald-500/24 ${tileHoverClass(
              'hover:border-emerald-500/80 hover:ring-1 hover:ring-emerald-500/35'
            )}`}
          >
            {renderHoverPopover(
              fileMainTitle,
              '文件',
              fileMainTokens,
              'bg-emerald-500'
            )}
            <div className="w-full min-w-0 overflow-hidden">
              <div className="text-xs font-bold text-emerald-700 dark:text-emerald-100 truncate w-full">
                {fileMainTitle}
              </div>
              <div className="text-xs text-emerald-600/90 dark:text-emerald-200/85 truncate w-full tabular-nums">
                {`${formatCompact(fileMainTokens)} · 文件`}
              </div>
            </div>
          </button>

          {groupedForTreemap.files.length > 1 && (
            <div className="col-span-5 min-w-0 min-h-0 flex flex-col gap-1.5">
              <div className="flex-1 min-h-0 min-w-0 grid grid-cols-2 gap-1.5">
                {groupedForTreemap.files.slice(1, 3).map((fileItem) => (
                  <button
                    key={fileItem.id}
                    type="button"
                    onClick={() =>
                      openModule('prompt_input', {
                        category: 'files',
                        title: fileItem.title,
                        subtitle: fileItem.subtitle || '工作区文件',
                        tag: fileItem.tag || 'file',
                        tokens: fileItem.tokens,
                        count: fileItem.count,
                        items: [fileItem],
                      })
                    }
                    className={`relative min-w-0 min-h-0 rounded-xl border border-emerald-500/35 bg-emerald-500/14 dark:bg-emerald-500/18 p-2 flex flex-col justify-start text-start cursor-pointer hover:bg-emerald-500/24 ${tileHoverClass(
                      'hover:border-emerald-500/80 hover:ring-1 hover:ring-emerald-500/35'
                    )}`}
                  >
                    {renderHoverPopover(
                      fileItem.title,
                      '文件',
                      fileItem.tokens,
                      'bg-emerald-500'
                    )}
                    <div className="w-full min-w-0 overflow-hidden">
                      <div className="text-xs font-bold text-emerald-700 dark:text-emerald-100 truncate w-full">
                        {fileItem.title}
                      </div>
                      <div className="text-xs text-emerald-600/90 dark:text-emerald-200/85 truncate w-full tabular-nums">
                        {formatCompact(fileItem.tokens)}
                      </div>
                    </div>
                  </button>
                ))}
              </div>

              {groupedForTreemap.files.length > 3 && (
                <div className="flex-1 min-h-0 min-w-0 grid grid-cols-2 gap-1.5">
                  {(() => {
                    const f3 = groupedForTreemap.files[3]
                    return (
                      <button
                        type="button"
                        onClick={() =>
                          openModule('prompt_input', {
                            category: 'files',
                            title: f3.title,
                            subtitle: f3.subtitle || '工作区文件',
                            tag: f3.tag || 'file',
                            tokens: f3.tokens,
                            count: f3.count,
                            items: [f3],
                          })
                        }
                        className={`relative min-w-0 min-h-0 rounded-xl border border-emerald-500/35 bg-emerald-500/14 dark:bg-emerald-500/18 p-2 flex flex-col justify-start text-start cursor-pointer hover:bg-emerald-500/24 ${tileHoverClass(
                          'hover:border-emerald-500/80 hover:ring-1 hover:ring-emerald-500/35'
                        )}`}
                      >
                        {renderHoverPopover(f3.title, '文件', f3.tokens, 'bg-emerald-500')}
                        <div className="w-full min-w-0 overflow-hidden">
                          <div className="text-xs font-bold text-emerald-700 dark:text-emerald-100 truncate w-full">
                            {f3.title}
                          </div>
                          <div className="text-xs text-emerald-600/90 dark:text-emerald-200/85 truncate w-full tabular-nums">
                            {formatCompact(f3.tokens)}
                          </div>
                        </div>
                      </button>
                    )
                  })()}
                  {groupedForTreemap.files.length > 4 && (
                    <div className="flex flex-col gap-1 min-w-0 min-h-0">
                      {(() => {
                        const f4 = groupedForTreemap.files[4]
                        return (
                          <button
                            type="button"
                            onClick={() =>
                              openModule('prompt_input', {
                                category: 'files',
                                title: f4.title,
                                subtitle: f4.subtitle || '工作区文件',
                                tag: f4.tag || 'file',
                                tokens: f4.tokens,
                                count: f4.count,
                                items: [f4],
                              })
                            }
                            className={`relative flex-1 min-h-0 min-w-0 rounded-xl border border-emerald-500/35 bg-emerald-500/14 dark:bg-emerald-500/18 px-2 py-1 flex flex-col justify-center text-start cursor-pointer hover:bg-emerald-500/24 ${tileHoverClass(
                              'hover:border-emerald-500/80 hover:ring-1 hover:ring-emerald-500/35'
                            )}`}
                          >
                            {renderHoverPopover(
                              f4.title,
                              '文件',
                              f4.tokens,
                              'bg-emerald-500'
                            )}
                            <div className="w-full min-w-0 overflow-hidden">
                              <div className="text-xs font-bold text-emerald-700 dark:text-emerald-100 truncate w-full">
                                {f4.title}
                              </div>
                              <div className="text-xs text-emerald-600/90 dark:text-emerald-200/85 truncate w-full tabular-nums">
                                {formatCompact(f4.tokens)}
                              </div>
                            </div>
                          </button>
                        )
                      })()}
                      {groupedForTreemap.files.length > 5 &&
                        (() => {
                          const extraFiles = groupedForTreemap.files.slice(5)
                          const extraTokens = extraFiles.reduce((acc, item) => acc + item.tokens, 0)
                          const extraLabel = `另外 ${extraFiles.length} 个文件`
                          return (
                            <button
                              type="button"
                              onClick={() =>
                                openModule('prompt_input', {
                                  category: 'files',
                                  title: extraLabel,
                                  subtitle: '其余工作区文件上下文',
                                  tag: 'file',
                                  tokens: extraTokens,
                                  count: extraFiles.length,
                                  items: extraFiles,
                                })
                              }
                              className={`relative flex-1 min-h-0 min-w-0 rounded-xl border border-emerald-500/35 bg-emerald-500/14 dark:bg-emerald-500/18 px-2 py-1 flex flex-col justify-center text-start cursor-pointer hover:bg-emerald-500/24 ${tileHoverClass(
                                'hover:border-emerald-500/80 hover:ring-1 hover:ring-emerald-500/35'
                              )}`}
                            >
                              {renderHoverPopover(
                                extraLabel,
                                '文件',
                                extraTokens,
                                'bg-emerald-500'
                              )}
                              <div className="w-full min-w-0 overflow-hidden">
                                <div className="text-xs font-bold text-emerald-700 dark:text-emerald-100 truncate w-full">
                                  {extraLabel}
                                </div>
                                <div className="text-xs text-emerald-600/90 dark:text-emerald-200/85 truncate w-full tabular-nums">
                                  {formatCompact(extraTokens)}
                                </div>
                              </div>
                            </button>
                          )
                        })()}
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Column 3: 工具结果 (Orange Tile + Optional Sub-Items) */}
        <div className="lg:w-[23%] min-w-0 flex flex-col gap-1.5 min-h-[140px]">
          <button
            type="button"
            onClick={() =>
              openModule('completion_tools', {
                category: 'tool_results',
                title: toolResMainTitle,
                subtitle: toolResMain?.subtitle || '工具调用返回结果',
                tag: toolResMain?.tag || 'result',
                tokens: toolResMainTokens,
                count: toolResMain?.count,
                items: toolResMain ? [toolResMain] : [],
              })
            }
            className={`relative flex-1 min-h-0 min-w-0 rounded-xl border border-orange-500/35 bg-orange-500/14 dark:bg-orange-500/18 p-3 flex flex-col justify-start text-start cursor-pointer hover:bg-orange-500/24 ${tileHoverClass(
              'hover:border-orange-500/80 hover:ring-1 hover:ring-orange-500/35'
            )}`}
          >
            {renderHoverPopover(
              toolResMainTitle,
              '工具结果',
              toolResMainTokens,
              'bg-orange-500'
            )}
            <div className="w-full min-w-0 overflow-hidden">
              <div className="text-xs font-bold text-orange-700 dark:text-orange-100 truncate w-full">
                {toolResMainTitle}
              </div>
              <div className="text-xs text-orange-600/90 dark:text-orange-200/85 truncate w-full tabular-nums">
                {formatCompact(toolResMainTokens)}
              </div>
            </div>
          </button>

          {toolResSubItems.length > 0 && (
            <div className="flex gap-1 h-12 min-w-0 shrink-0">
              {toolResSubItems.slice(0, 2).map((subRes, idx) => {
                const isSummary = idx === 1 && toolResSubItems.length > 2
                const restRes = toolResSubItems.slice(1)
                const tok = isSummary
                  ? restRes.reduce((acc, it) => acc + it.tokens, 0)
                  : subRes.tokens
                const label = isSummary ? `另外 ${restRes.length} 项结果` : subRes.title
                const flexWeight = Math.max(4, Math.min(16, label.length))
                return (
                  <button
                    key={subRes.id}
                    type="button"
                    style={{ flex: `${flexWeight} 1 0%` }}
                    onClick={() =>
                      openModule('completion_tools', {
                        category: 'tool_results',
                        title: label,
                        subtitle: isSummary ? '其余工具调用返回结果' : subRes.subtitle || '工具调用返回结果',
                        tag: subRes.tag || 'result',
                        tokens: tok,
                        count: isSummary ? restRes.length : subRes.count,
                        items: isSummary ? restRes : [subRes],
                      })
                    }
                    className={`relative min-w-0 rounded-xl border border-orange-500/35 bg-orange-500/14 dark:bg-orange-500/18 px-2 py-1 flex flex-col justify-center text-start cursor-pointer hover:bg-orange-500/24 ${tileHoverClass(
                      'hover:border-orange-500/80 hover:ring-1 hover:ring-orange-500/35'
                    )}`}
                  >
                    {renderHoverPopover(label, '工具结果', tok, 'bg-orange-500')}
                    <div className="w-full min-w-0 overflow-hidden">
                      <div className="text-xs font-bold text-orange-700 dark:text-orange-100 truncate w-full">
                        {label}
                      </div>
                      <div className="text-xs text-orange-600/90 dark:text-orange-200/85 truncate w-full tabular-nums">
                        {formatCompact(tok)}
                      </div>
                    </div>
                  </button>
                )
              })}
            </div>
          )}
        </div>

        {/* Column 4: 会话内容 (Indigo Real Multi-Turn Grid) */}
        <div className="lg:w-[17%] min-w-0 flex flex-col gap-1.5 min-h-[200px]">
          <div
            className={`flex-1 min-h-0 min-w-0 grid ${
              groupedForTreemap.conversation.length > 1 ? 'grid-cols-2' : 'grid-cols-1'
            } gap-1.5`}
          >
            {(groupedForTreemap.conversation.length > 0
              ? groupedForTreemap.conversation.slice(0, 2)
              : [
                  {
                    id: 'conv-default',
                    category: 'conversation' as const,
                    tag: 'turn',
                    title: '当前会话',
                    subtitle: '',
                    tokens: categoryTotals.conversation,
                    dotClass: 'bg-indigo-500',
                    barClass: 'bg-indigo-500',
                  },
                ]
            ).map((turnItem, idx) => {
              const turnLabel = turnItem.title || `第 ${idx + 1} 轮`
              const isMultiCol = groupedForTreemap.conversation.length > 1
              return (
                <button
                  key={turnItem.id}
                  type="button"
                  onClick={() =>
                    openModule('latest_turn', {
                      category: 'conversation',
                      title: turnLabel,
                      subtitle: turnItem.subtitle || '会话内容',
                      tag: turnItem.tag || 'turn',
                      tokens: turnItem.tokens,
                      count: turnItem.count,
                      items: [turnItem],
                    })
                  }
                  className={`relative min-w-0 min-h-0 rounded-xl border border-indigo-500/35 bg-indigo-500/14 dark:bg-indigo-500/18 p-2 flex flex-col justify-start text-start cursor-pointer hover:bg-indigo-500/24 ${tileHoverClass(
                    'hover:border-indigo-500/80 hover:ring-1 hover:ring-indigo-500/35'
                  )}`}
                >
                  {renderHoverPopover(
                    turnLabel,
                    '会话内容',
                    turnItem.tokens,
                    'bg-indigo-500',
                    true
                  )}
                  <div className="w-full min-w-0 overflow-hidden">
                    <div className="text-xs font-bold text-indigo-700 dark:text-indigo-100 truncate w-full">
                      {turnLabel}
                    </div>
                    <div className="text-xs text-indigo-600/90 dark:text-indigo-200/85 truncate w-full tabular-nums">
                      {isMultiCol
                        ? formatCompact(turnItem.tokens)
                        : `${formatCompact(turnItem.tokens)} · 会话内容`}
                    </div>
                  </div>
                </button>
              )
            })}
          </div>

          {groupedForTreemap.conversation.length > 2 && (
            <div className="flex flex-col gap-1 min-w-0 shrink-0">
              <div className="grid grid-cols-2 gap-1 h-16 min-w-0">
                {groupedForTreemap.conversation.slice(2, 4).map((turnItem) => (
                  <button
                    key={turnItem.id}
                    type="button"
                    onClick={() =>
                      openModule('latest_turn', {
                        category: 'conversation',
                        title: turnItem.title,
                        subtitle: turnItem.subtitle || '会话内容',
                        tag: turnItem.tag || 'turn',
                        tokens: turnItem.tokens,
                        count: turnItem.count,
                        items: [turnItem],
                      })
                    }
                    className={`relative min-w-0 min-h-0 rounded-xl border border-indigo-500/35 bg-indigo-500/14 dark:bg-indigo-500/18 p-1.5 flex flex-col justify-start text-start cursor-pointer hover:bg-indigo-500/24 ${tileHoverClass(
                      'hover:border-indigo-500/80 hover:ring-1 hover:ring-indigo-500/35'
                    )}`}
                  >
                    {renderHoverPopover(
                      turnItem.title,
                      '会话内容',
                      turnItem.tokens,
                      'bg-indigo-500',
                      true
                    )}
                    <div className="w-full min-w-0 overflow-hidden">
                      <div className="text-xs font-bold text-indigo-700 dark:text-indigo-100 truncate w-full">
                        {turnItem.title}
                      </div>
                      <div className="text-xs text-indigo-600/90 dark:text-indigo-200/85 truncate w-full tabular-nums">
                        {formatCompact(turnItem.tokens)}
                      </div>
                    </div>
                  </button>
                ))}
              </div>

              {groupedForTreemap.conversation.length > 4 && (
                <div className="grid grid-cols-2 gap-1 h-12 min-w-0">
                  {groupedForTreemap.conversation.slice(4, 6).map((turnItem, idx) => {
                    const isRest = idx === 1 && groupedForTreemap.conversation.length > 6
                    const restItems = groupedForTreemap.conversation.slice(5)
                    const tok = isRest
                      ? restItems.reduce((acc, it) => acc + it.tokens, 0)
                      : turnItem.tokens
                    const label = isRest ? `另外 ${restItems.length} 项会话` : turnItem.title
                    return (
                      <button
                        key={turnItem.id}
                        type="button"
                        onClick={() =>
                          openModule('latest_turn', {
                            category: 'conversation',
                            title: label,
                            subtitle: isRest ? '其余会话轮次消息' : turnItem.subtitle || '会话内容',
                            tag: turnItem.tag || 'turn',
                            tokens: tok,
                            count: isRest ? restItems.length : turnItem.count,
                            items: isRest ? restItems : [turnItem],
                          })
                        }
                        className={`relative min-w-0 min-h-0 rounded-xl border border-indigo-500/35 bg-indigo-500/14 dark:bg-indigo-500/18 p-1.5 flex flex-col justify-center text-start cursor-pointer hover:bg-indigo-500/24 ${tileHoverClass(
                          'hover:border-indigo-500/80 hover:ring-1 hover:ring-indigo-500/35'
                        )}`}
                      >
                        {renderHoverPopover(label, '会话内容', tok, 'bg-indigo-500', true)}
                        <div className="w-full min-w-0 overflow-hidden">
                          <div className="text-xs font-bold text-indigo-700 dark:text-indigo-100 truncate w-full">
                            {label}
                          </div>
                          <div className="text-xs text-indigo-600/90 dark:text-indigo-200/85 truncate w-full tabular-nums">
                            {formatCompact(tok)}
                          </div>
                        </div>
                      </button>
                    )
                  })}
                </div>
              )}
            </div>
          )}
        </div>

        {/* Column 5: 剩余空间 (Diagonal Hatched Strip) */}
        <button
          type="button"
          onClick={() =>
            openModule('remaining_capacity', {
              category: 'remaining_capacity',
              title: '剩余空间',
              subtitle: '当前模型上下文窗口剩余可用容量',
              tag: 'capacity',
              tokens: remainingTokens,
              items: [],
            })
          }
          style={{
            backgroundImage:
              'repeating-linear-gradient(-45deg, rgba(148,163,184,0.16), rgba(148,163,184,0.16) 4px, transparent 4px, transparent 9px)',
          }}
          className={`relative lg:flex-1 min-w-[24px] rounded-xl border border-border/80 bg-muted/20 p-2 flex flex-col justify-between text-start cursor-pointer hover:border-foreground/40 min-h-[80px] ${tileHoverClass(
            'hover:border-foreground/50 hover:ring-1 hover:ring-foreground/20'
          )}`}
        >
          {renderHoverPopover(
            '剩余空间',
            '剩余空间',
            remainingTokens,
            'bg-zinc-400',
            true
          )}
          <span className="sr-only">{`剩余空间 ${formatCompact(remainingTokens)}`}</span>
        </button>
      </div>
    )
  }
)

export const OverviewBoard: React.FC<OverviewBoardProps> = React.memo(({
  overview,
  accounts,
  auditLogs,
  selectedAuditLog,
  onSelectAuditLogId,
  gatewayHostPort,
  loading,
  loadError,
  syncError,
  fleetUsageSummary,
  onOpenModal,
  onNavigateTab,
  onRefresh,
}) => {
  const [contextCollapsed, setContextCollapsed] = useState<boolean>(false)
  const [mosaicViewMode, setMosaicViewMode] = useState<'treemap' | 'waffle'>('treemap')
  const [breakdownFilter, setBreakdownFilter] = useState<RequestContextCategory>('all')
  const [showAllItems, setShowAllItems] = useState<boolean>(false)
  const [requestDateFilter, setRequestDateFilter] = useState<string>('realtime')
  const [requestGroupMode, setRequestGroupMode] = useState<'request' | 'session'>('request')
  const [requestPurposeFilter, setRequestPurposeFilter] = useState<'all' | 'chat' | 'tool'>('all')
  const [requestMetricMode, setRequestMetricMode] = useState<'full' | 'compact'>('full')
  const [selectedAccountOrKeyFilter, setSelectedAccountOrKeyFilter] = useState<string>('')

  const overviewTotalReqs = overview?.stats.total_requests
  const overviewSuccessReqs = overview?.stats.success_requests
  const fleetTotalReqs = fleetUsageSummary.totalRequests
  const fleetErrorReqs = fleetUsageSummary.errorRequests

  // Compute all metrics strictly for the selected single request (`selectedAuditLog` or `auditLogs[0]`)
  const reqTelemetry = useMemo(() => {
    const enabledAccounts = accounts.filter((a) => a.account.enabled)
    const activeLog = selectedAuditLog || auditLogs[0]
    const sessionLogs = resolveSessionLogs(activeLog, auditLogs)

    const usage = computeRequestContextUsage(activeLog)
    const parsedSnap = usage.parsedSnap

    // Attribution comes only from what the gateway recorded for this request, never from
    // the live key/policy configuration, which may have changed since.
    const effectivePromptMode = parsedSnap?.prompt_mode || ''
    const effectiveSessionMode = parsedSnap?.session_mode || ''
    const effectiveDisableBuiltin = parsedSnap?.disable_builtin_tools
    const effectiveKeyName = parsedSnap?.key_name || ''

    const routedSnap = activeLog?.account_id
      ? accounts.find((a) => a.account.id === activeLog.account_id)
      : undefined
    const routedId = activeLog?.account_id || ''
    const routedName =
      routedSnap?.account.name && routedSnap.account.name !== 'Default Host Account'
        ? routedSnap.account.name
        : routedId || '—'
    const routedInitial = routedId ? (routedName.trim()[0] || '?').toUpperCase() : '—'

    const rawSnapClient =
      parsedSnap?.client_label && parsedSnap.client_label !== 'Codex'
        ? parsedSnap.client_label
        : ''
    const clientLabel =
      rawSnapClient || formatClientShortName(activeLog?.protocol, effectiveKeyName, activeLog)
    const requestedModel = activeLog?.model_name || ''
    const actualModel = requestedModel.includes('/')
      ? requestedModel.split('/').pop() || requestedModel
      : requestedModel
    const thinkingLevel = parsedSnap?.thinking_level || activeLog?.thinking_level || ''

    const {
      inputTokens: reqInputTokens,
      outputTokens: reqOutputTokens,
      totalTokens: reqTotalTokens,
      contextCapacity,
      usedContextTokens,
      usedPct,
      remainingTokens,
      estRemainingReqs,
    } = usage

    // Prompt cache metrics strictly from recorded telemetry
    const rawCachedTokens =
      parsedSnap?.cached_tokens && parsedSnap.cached_tokens > 0
        ? parsedSnap.cached_tokens
        : activeLog?.cached_tokens || 0
    const cachedTokens = Math.max(0, Math.min(reqInputTokens, rawCachedTokens))
    const newInputTokens = Math.max(0, reqInputTokens - cachedTokens)
    const cacheHitPct =
      reqInputTokens > 0
        ? cachedTokens >= reqInputTokens
          ? 100
          : Math.min(99, Math.round((cachedTokens / reqInputTokens) * 100))
        : 0

    // Per-request latency breakdown from real measured timestamps (prep_ms, ttfb_ms, latency_ms)
    const latencyMs: number | null =
      activeLog?.latency_ms && activeLog.latency_ms > 0 ? activeLog.latency_ms : null
    const gwMs: number | null =
      parsedSnap?.prep_ms && parsedSnap.prep_ms > 0
        ? latencyMs !== null
          ? Math.min(latencyMs, parsedSnap.prep_ms)
          : parsedSnap.prep_ms
        : null
    const firstWordMs: number | null =
      parsedSnap?.ttfb_ms && parsedSnap.ttfb_ms > 0
        ? latencyMs !== null
          ? Math.min(latencyMs, parsedSnap.ttfb_ms)
          : parsedSnap.ttfb_ms
        : null
    const providerTtfbMs: number | null =
      firstWordMs !== null && gwMs !== null ? Math.max(0, firstWordMs - gwMs) : null
    const streamMs: number | null =
      latencyMs !== null && firstWordMs !== null ? Math.max(0, latencyMs - firstWordMs) : null
    const providerMs: number | null =
      latencyMs !== null && gwMs !== null ? Math.max(0, latencyMs - gwMs) : null

    const totalReqs = overviewTotalReqs ?? fleetTotalReqs
    const successReqs =
      overviewSuccessReqs ??
      Math.max(0, totalReqs - fleetErrorReqs)
    const errorReqs = Math.max(0, totalReqs - successReqs, fleetErrorReqs)

    let switchCount = 0
    for (let i = 1; i < auditLogs.length; i += 1) {
      if (
        auditLogs[i].account_id &&
        auditLogs[i - 1].account_id &&
        auditLogs[i].account_id !== auditLogs[i - 1].account_id
      ) {
        switchCount += 1
      }
    }

    // Build per-request context breakdown items across the 6 categories.
    // Prefer the backend's recorded snapshot items directly without overwriting or stretching.
    const entries: NormalizedContextEntry[] = []
    const validCategories = new Set<Exclude<RequestContextCategory, 'all'>>([
      'system_prompt',
      'tools',
      'skills',
      'files',
      'tool_results',
      'conversation',
    ])

    if (parsedSnap?.items && parsedSnap.items.length > 0) {
      parsedSnap.items.forEach((it, idx) => {
        const cat = validCategories.has(it.category as Exclude<RequestContextCategory, 'all'>)
          ? (it.category as Exclude<RequestContextCategory, 'all'>)
          : 'conversation'
        if (cat === 'tools' && effectiveDisableBuiltin === true && it.title.includes('内置工具')) {
          return
        }
        const style = CATEGORY_STYLE[cat]
        entries.push({
          id: `snap-${idx}-${cat}-${it.title}`,
          category: cat,
          tag:
            it.tag ||
            (cat === 'system_prompt'
              ? 'system'
              : cat === 'tools'
                ? 'tool'
                : cat === 'skills'
                  ? 'skill'
                  : cat === 'files'
                    ? 'shell'
                    : cat === 'tool_results'
                      ? 'result'
                      : 'turn'),
          title: it.title,
          subtitle: it.subtitle || '',
          tokens: Math.max(1, it.tokens),
          count: it.count,
          content: it.content,
          dotClass: style.dotClass,
          barClass: style.barClass,
        })
      })
      if (usedContextTokens > 0 && entries.length > 0) {
        let fixedSum = 0
        let dynSum = 0
        for (const e of entries) {
          if (
            e.category === 'system_prompt' ||
            e.category === 'tools' ||
            e.category === 'skills'
          ) {
            fixedSum += e.tokens
          } else {
            dynSum += e.tokens
          }
        }
        const rawSum = fixedSum + dynSum
        if (rawSum > 0 && Math.abs(rawSum - usedContextTokens) > 64) {
          if (dynSum > 0 && fixedSum < usedContextTokens * 0.75) {
            const remDyn = usedContextTokens - fixedSum
            for (const e of entries) {
              if (
                e.category !== 'system_prompt' &&
                e.category !== 'tools' &&
                e.category !== 'skills'
              ) {
                e.tokens = Math.max(1, Math.round((e.tokens * remDyn) / dynSum))
              }
            }
          } else {
            for (const e of entries) {
              e.tokens = Math.max(1, Math.round((e.tokens * usedContextTokens) / rawSum))
            }
          }
        }
      }
    } else if (activeLog && usedContextTokens > 0) {
      const rawSysTok = estimateTextTokens(activeLog.system_prompt)
      const rawPromptTok = estimateTextTokens(activeLog.prompt_text)

      if (rawSysTok > 0) {
        entries.push({
          id: 'sys-agent',
          category: 'system_prompt',
          tag: 'system',
          title:
            effectivePromptMode === 'pure_passthrough'
              ? '客户端系统提示词'
              : effectivePromptMode === 'prepend_global'
                ? '全局前置与客户端提示词'
                : '系统提示词',
          subtitle: [
            effectivePromptMode ? `策略 ${effectivePromptMode}` : '',
            effectiveKeyName ? `Key:${effectiveKeyName}` : '',
          ]
            .filter(Boolean)
            .join(' · '),
          tokens: Math.max(1, rawSysTok),
          dotClass: CATEGORY_STYLE.system_prompt.dotClass,
          barClass: CATEGORY_STYLE.system_prompt.barClass,
        })
      }

      if (rawPromptTok > 0) {
        entries.push({
          id: `conv-turn-${activeLog.request_id}`,
          category: 'conversation',
          tag: 'turn',
          title: '当前请求上下文',
          subtitle: extractPromptGoalSummary(activeLog, null, sessionLogs),
          tokens: Math.max(1, usedContextTokens - rawSysTok),
          dotClass: CATEGORY_STYLE.conversation.dotClass,
          barClass: CATEGORY_STYLE.conversation.barClass,
        })
      }
    }

    // Category totals for this request
    const categoryTotals: Record<Exclude<RequestContextCategory, 'all'>, number> = {
      system_prompt: 0,
      tools: 0,
      skills: 0,
      files: 0,
      tool_results: 0,
      conversation: 0,
    }
    for (const item of entries) {
      categoryTotals[item.category] += item.tokens
    }

    const sortedEntries = [...entries].sort((a, b) => b.tokens - a.tokens)
    const promptSummary = extractPromptGoalSummary(activeLog, parsedSnap, sessionLogs)
    const sessionTitle = extractSessionTitle(activeLog, parsedSnap, sessionLogs)
    const clientSessionId =
      parsedSnap?.client_session_id ||
      activeLog?.session_id ||
      activeLog?.request_id ||
      ''

    // Session timeline sparkline (chronological from oldest to newest)
    const chronological = [...sessionLogs].reverse()
    const seriesLogs = chronological.length > 0 ? chronological : activeLog ? [activeLog] : []
    const seriesValues =
      seriesLogs.length > 0
        ? seriesLogs.map((l) =>
            Math.max(10, l.total_tokens || l.prompt_tokens + l.completion_tokens || 100)
          )
        : [0]

    const maxSeriesVal = Math.max(...seriesValues, 1)
    const svgW = 800
    const svgH = 42
    const padX = 10
    const padY = 6
    const usableW = svgW - padX * 2
    const usableH = svgH - padY * 2

    const sparkPoints = seriesValues.map((val, idx) => {
      const x =
        seriesValues.length === 1
          ? svgW - padX
          : padX + (idx / Math.max(1, seriesValues.length - 1)) * usableW
      const y = padY + usableH - (val / maxSeriesVal) * usableH
      const reqId = seriesLogs[idx]?.request_id || ''
      return {
        x: Math.round(x * 10) / 10,
        y: Math.round(y * 10) / 10,
        reqId,
        tokens: val,
        isSelected: Boolean(activeLog && reqId === activeLog.request_id),
      }
    })

    const linePath =
      sparkPoints.length >= 2
        ? sparkPoints.map((pt, idx) => `${idx === 0 ? 'M' : 'L'} ${pt.x} ${pt.y}`).join(' ')
        : `M ${padX} ${svgH - padY} L ${svgW - padX} ${sparkPoints[0]?.y ?? svgH / 2}`

    const firstX = sparkPoints.length >= 2 ? sparkPoints[0].x : padX
    const lastX = sparkPoints[sparkPoints.length - 1]?.x ?? svgW - padX
    const areaPath = `${linePath} L ${lastX} ${svgH} L ${firstX} ${svgH} Z`
    const selectedPt =
      sparkPoints.find((p) => p.isSelected) ||
      sparkPoints[sparkPoints.length - 1] || { x: svgW - padX, y: svgH / 2, reqId: '' }

    const turnCount = Math.max(
      parsedSnap?.turn_count || 0,
      sessionLogs.length,
      activeLog ? 1 : 0
    )

    return {
      enabledAccounts,
      activeLog,
      sessionLogs,
      routedId,
      routedName,
      routedInitial,
      clientLabel,
      requestedModel,
      actualModel,
      thinkingLevel,
      effectivePromptMode,
      effectiveSessionMode,
      effectiveDisableBuiltin,
      effectiveKeyName,
      reqInputTokens,
      reqOutputTokens,
      reqTotalTokens,
      contextCapacity,
      usedContextTokens,
      usedPct,
      remainingTokens,
      estRemainingReqs,
      cachedTokens,
      newInputTokens,
      cacheHitPct,
      latencyMs,
      gwMs,
      providerMs,
      providerTtfbMs,
      streamMs,
      firstWordMs,
      totalReqs,
      errorReqs,
      switchCount,
      entries: sortedEntries,
      categoryTotals,
      promptSummary,
      sessionTitle,
      clientSessionId,
      sparkPoints,
      linePath,
      areaPath,
      selectedPt,
      turnCount,
    }
  }, [
    accounts,
    auditLogs,
    fleetErrorReqs,
    fleetTotalReqs,
    overviewSuccessReqs,
    overviewTotalReqs,
    selectedAuditLog,
  ])

  const filteredEntries = useMemo(() => {
    const list =
      breakdownFilter === 'all'
        ? reqTelemetry.entries
        : reqTelemetry.entries.filter((e) => e.category === breakdownFilter)
    return showAllItems ? list : list.slice(0, 8)
  }, [breakdownFilter, reqTelemetry.entries, showAllItems])

  const totalFilteredCount = useMemo(() => {
    return breakdownFilter === 'all'
      ? reqTelemetry.entries.length
      : reqTelemetry.entries.filter((e) => e.category === breakdownFilter).length
  }, [breakdownFilter, reqTelemetry.entries])

  const maxEntryTokens = useMemo(() => {
    return Math.max(1, ...reqTelemetry.entries.map((e) => e.tokens))
  }, [reqTelemetry.entries])

  // Group entries by category for the 2D Treemap layout matching 图片 6.png
  const groupedForTreemap = useMemo(() => {
    const byCat: Record<Exclude<RequestContextCategory, 'all'>, NormalizedContextEntry[]> = {
      system_prompt: [],
      tools: [],
      skills: [],
      files: [],
      tool_results: [],
      conversation: [],
    }
    for (const item of reqTelemetry.entries) {
      byCat[item.category].push(item)
    }
    return byCat
  }, [reqTelemetry.entries])

  // Memoize date buckets, filtered logs, grouped rows, and worker process groups
  // so toggling UI state (collapse, mosaic mode, breakdown filter) does not re-traverse auditLogs.
  const bottomSelectorData = useMemo(() => {
    const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
    const formatDateKey = (d: Date) =>
      `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    const formatDateLabel = (d: Date) =>
      `${d.getMonth() + 1}月${d.getDate()}日 ${weekdays[d.getDay()]}`
    const formatTimeHMS = (iso?: string) => {
      if (!iso) return '00:00:00'
      const d = new Date(iso)
      if (Number.isNaN(d.getTime())) return '00:00:00'
      return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
    }
    const formatWeekdayHM = (iso?: string) => {
      if (!iso) return ''
      const d = new Date(iso)
      if (Number.isNaN(d.getTime())) return ''
      return `${weekdays[d.getDay()]} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
    }

    const bucketMap = new Map<
      string,
      {
        key: string
        label: string
        count: number
        maxTokens: number
        logs: AuditLogRecord[]
      }
    >()
    for (const log of auditLogs) {
      const d = new Date(log.created_at)
      if (Number.isNaN(d.getTime())) continue
      const key = formatDateKey(d)
      const existing = bucketMap.get(key)
      const tok = Math.max(0, log.total_tokens || log.prompt_tokens + log.completion_tokens)
      if (existing) {
        existing.count += 1
        existing.maxTokens = Math.max(existing.maxTokens, tok)
        existing.logs.push(log)
      } else {
        bucketMap.set(key, {
          key,
          label: formatDateLabel(d),
          count: 1,
          maxTokens: tok,
          logs: [log],
        })
      }
    }
    if (bucketMap.size === 0) {
      const now = new Date()
      const key = formatDateKey(now)
      bucketMap.set(key, {
        key,
        label: formatDateLabel(now),
        count: 0,
        maxTokens: 0,
        logs: [],
      })
    }
    const sortedDateBuckets = Array.from(bucketMap.values()).sort((a, b) =>
      b.key.localeCompare(a.key)
    )
    const visibleDateBuckets = sortedDateBuckets.slice(0, 3)
    const olderDateBuckets = sortedDateBuckets.slice(3)

    const filteredLogs = auditLogs.filter((log) => {
      if (requestDateFilter !== 'realtime' && requestDateFilter !== 'older') {
        const d = new Date(log.created_at)
        if (Number.isNaN(d.getTime()) || formatDateKey(d) !== requestDateFilter) {
          return false
        }
      } else if (requestDateFilter === 'older') {
        const d = new Date(log.created_at)
        const k = Number.isNaN(d.getTime()) ? '' : formatDateKey(d)
        if (!olderDateBuckets.some((b) => b.key === k)) {
          return false
        }
      }
      if (requestPurposeFilter !== 'all') {
        const isToolCont =
          Boolean(log.tool_calls_json) ||
          (log.prompt_text || '').includes('tool_result') ||
          (log.prompt_text || '').includes('function_call_output')
        if (requestPurposeFilter === 'tool' && !isToolCont) return false
        if (requestPurposeFilter === 'chat' && isToolCont) return false
      }
      if (selectedAccountOrKeyFilter) {
        const snap = parseAuditContextSnapshot(log)
        const keyMatch =
          snap?.key_name && `key:${snap.key_name}` === selectedAccountOrKeyFilter
        const accMatch = `acc:${log.account_id || 'default'}` === selectedAccountOrKeyFilter
        const pidMatch =
          Boolean(log.worker_pid) && `pid:${log.worker_pid}` === selectedAccountOrKeyFilter
        const rowSid = snap?.client_session_id || log.session_id || log.request_id
        const sessMatch =
          Boolean(rowSid) &&
          (`sess:${rowSid}` === selectedAccountOrKeyFilter ||
            `sess:${log.session_id}` === selectedAccountOrKeyFilter)
        const protoClient = formatClientShortName(log.protocol, snap?.key_name, log)
        const clientMatch = `client:${protoClient}` === selectedAccountOrKeyFilter
        if (!keyMatch && !accMatch && !pidMatch && !sessMatch && !clientMatch) return false
      }
      return true
    })

    const displayRows: {
      rowKey: string
      representative: AuditLogRecord
      sessionLogs: AuditLogRecord[]
      sessionCount: number
      totalOutputTokens: number
    }[] = (() => {
      if (requestGroupMode === 'request') {
        return filteredLogs.map((l) => {
          const snap = parseAuditContextSnapshot(l)
          return {
            rowKey: l.request_id,
            representative: l,
            sessionLogs: [l],
            sessionCount: 1,
            totalOutputTokens: resolvePureOutputTokens(l, snap),
          }
        })
      }
      const bySess = new Map<
        string,
        {
          representative: AuditLogRecord
          logs: AuditLogRecord[]
          count: number
          outputTokens: number
        }
      >()
      for (const l of filteredLogs) {
        const snap = parseAuditContextSnapshot(l)
        const sid = snap?.client_session_id || l.session_id || l.request_id
        const outTok = resolvePureOutputTokens(l, snap)
        const existing = bySess.get(sid)
        if (existing) {
          existing.count += 1
          existing.outputTokens += outTok
          existing.logs.push(l)
        } else {
          bySess.set(sid, {
            representative: l,
            logs: [l],
            count: 1,
            outputTokens: outTok,
          })
        }
      }
      return Array.from(bySess.entries()).map(([sid, val]) => ({
        rowKey: `sess-${sid}`,
        representative: val.representative,
        sessionLogs: val.logs,
        sessionCount: val.count,
        totalOutputTokens: val.outputTokens,
      }))
    })()

    interface BoundSessionSummary {
      sessionKey: string
      acpSessionId: string
      clientSessionId: string
      shortId: string
      sessionTitle: string
      modelName: string
      attempts: number
      successes: number
      failures: number
      lastAt: string
      latestRequestId: string
    }

    interface WorkerProcessCard {
      pid: number
      workerId: string
      accountId: string
      accountName: string
      state: string
      stateLabel: string
      stateVariant: 'success-light' | 'info-light' | 'warning-light' | 'destructive-light' | 'outline'
      lastActiveAt: string
      sessions: Map<string, BoundSessionSummary>
    }

    const workerMap = new Map<number, WorkerProcessCard>()
    const resolveAccName = (accId: string) => {
      const found = accounts.find((a) => a.account.id === accId)
      if (found?.account.name && found.account.name !== 'Default Host Account') {
        return found.account.name
      }
      return accId || 'default'
    }

    for (const w of overview?.workers || []) {
      if (!w.pid) continue
      const rawState = (w.state || 'Idle').trim()
      const hasBoundFromApi = Boolean(w.bound_sessions && w.bound_sessions.length > 0)
      const stateLabel =
        rawState === 'Generating'
          ? '生成中'
          : rawState === 'Parked' || hasBoundFromApi
            ? '会话驻留'
            : rawState === 'Dead'
              ? '已退出'
              : '空闲待命'
      const stateVariant: WorkerProcessCard['stateVariant'] =
        rawState === 'Generating'
          ? 'success-light'
          : rawState === 'Parked' || hasBoundFromApi
            ? 'info-light'
            : rawState === 'Dead'
              ? 'destructive-light'
              : 'outline'

      const card: WorkerProcessCard = {
        pid: w.pid,
        workerId: w.id || `w_${w.account_id || 'default'}`,
        accountId: w.account_id || 'default',
        accountName: resolveAccName(w.account_id || 'default'),
        state: rawState,
        stateLabel,
        stateVariant,
        lastActiveAt: w.last_active_at || '',
        sessions: new Map(),
      }

      if (w.bound_sessions) {
        for (const bs of w.bound_sessions) {
          const sid = bs.acp_session_id || bs.explicit_key || bs.bridge_id
          if (!sid) continue
          const clientSid = bs.explicit_key || bs.acp_session_id || bs.bridge_id
          card.sessions.set(sid, {
            sessionKey: sid,
            acpSessionId: sid,
            clientSessionId: clientSid,
            shortId: formatShortSessionId(clientSid),
            sessionTitle: '',
            modelName: '',
            attempts: 1,
            successes: 1,
            failures: 0,
            lastAt: bs.updated_at || w.last_active_at || '',
            latestRequestId: '',
          })
        }
      }

      workerMap.set(w.pid, card)
    }

    for (const log of auditLogs) {
      const pid = log.worker_pid || 0
      if (!pid) continue
      let card = workerMap.get(pid)
      if (!card) {
        const accId = log.account_id || 'default'
        card = {
          pid,
          workerId: `w_${accId}`,
          accountId: accId,
          accountName: resolveAccName(accId),
          state: 'History',
          stateLabel: '历史进程',
          stateVariant: 'outline',
          lastActiveAt: log.created_at,
          sessions: new Map(),
        }
        workerMap.set(pid, card)
      }

      if (log.created_at > card.lastActiveAt) {
        card.lastActiveAt = log.created_at
      }

      const snap = parseAuditContextSnapshot(log)
      const acpSid = log.session_id || snap?.client_session_id || log.request_id
      const clientSid = snap?.client_session_id || log.session_id || log.request_id
      const sessionKey = acpSid || clientSid
      if (!sessionKey) continue

      const rawModel = log.model_name || ''
      const cleanModel = rawModel.includes('/')
        ? rawModel.split('/').pop() || rawModel
        : rawModel
      const titleCandidate =
        extractSessionTitle(log, snap) || extractPromptGoalSummary(log, snap)
      const isOk = log.status_code >= 200 && log.status_code < 400

      let sess = card.sessions.get(sessionKey)
      if (!sess) {
        sess = {
          sessionKey,
          acpSessionId: acpSid,
          clientSessionId: clientSid,
          shortId: formatShortSessionId(clientSid),
          sessionTitle: titleCandidate,
          modelName: cleanModel,
          attempts: 0,
          successes: 0,
          failures: 0,
          lastAt: log.created_at,
          latestRequestId: log.request_id,
        }
        card.sessions.set(sessionKey, sess)
      } else {
        if (!sess.latestRequestId) {
          sess.attempts = 0
          sess.successes = 0
          sess.failures = 0
          sess.latestRequestId = log.request_id
        }
        if (!sess.sessionTitle && titleCandidate) {
          sess.sessionTitle = titleCandidate
        }
        if (cleanModel) {
          sess.modelName = cleanModel
        }
      }

      sess.attempts += 1
      if (isOk) sess.successes += 1
      else sess.failures += 1
      if (log.created_at >= sess.lastAt) {
        sess.lastAt = log.created_at
        sess.latestRequestId = log.request_id
      }

      if (card.state === 'Idle' && card.sessions.size > 0) {
        card.stateLabel = '会话驻留'
        card.stateVariant = 'info-light'
      }
    }

    const workerProcessGroups = Array.from(workerMap.values()).sort((a, b) => {
      const aLive = a.state !== 'History' && a.state !== 'Dead' ? 1 : 0
      const bLive = b.state !== 'History' && b.state !== 'Dead' ? 1 : 0
      if (aLive !== bLive) return bLive - aLive
      if (a.sessions.size !== b.sessions.size) return b.sessions.size - a.sessions.size
      return (b.lastActiveAt || '').localeCompare(a.lastActiveAt || '')
    })

    return {
      formatTimeHMS,
      formatWeekdayHM,
      visibleDateBuckets,
      olderDateBuckets,
      displayRows,
      workerProcessGroups,
    }
  }, [
    accounts,
    auditLogs,
    overview?.workers,
    requestDateFilter,
    requestGroupMode,
    requestPurposeFilter,
    selectedAccountOrKeyFilter,
  ])

  const selectNextRequest = () => {
    if (auditLogs.length <= 1) {
      onRefresh()
      return
    }
    const currIdx = auditLogs.findIndex((l) => l.request_id === reqTelemetry.activeLog?.request_id)
    const nextIdx = currIdx >= 0 && currIdx + 1 < auditLogs.length ? currIdx + 1 : 0
    onSelectAuditLogId(auditLogs[nextIdx].request_id)
  }

  const sessionDisplayId = useMemo(() => {
    const raw =
      reqTelemetry.clientSessionId ||
      reqTelemetry.activeLog?.session_id ||
      reqTelemetry.activeLog?.request_id ||
      ''
    return raw ? formatShortSessionId(raw) : '—'
  }, [reqTelemetry.activeLog, reqTelemetry.clientSessionId])

  if (!overview && auditLogs.length === 0) {
    const blockingError = loadError || syncError
    if (blockingError && !loading) {
      return (
        <Frame>
          <FramePanel className="p-5">
            <InlineError title="概览加载失败" message={blockingError} onRetry={onRefresh} />
          </FramePanel>
        </Frame>
      )
    }
    return (
      <Frame>
        <FramePanel className="p-5 flex flex-col gap-5">
          <div className="flex flex-col gap-2">
            <Skeleton className="h-5 w-48" />
            <Skeleton className="h-4 w-80" />
          </div>
          <div className="flex flex-col gap-3 lg:flex-row">
            <Skeleton className="h-20 lg:w-40" />
            <Skeleton className="h-20 lg:w-40" />
            <Skeleton className="h-20 flex-1" />
          </div>
          <Skeleton className="h-48 w-full" />
          <Skeleton className="h-64 w-full" />
        </FramePanel>
      </Frame>
    )
  }

  return (
    <Frame>
      <FramePanel className="p-5 flex flex-col gap-5">
        {/* 1. Top Account / Provider Header */}
        <div className="flex flex-col gap-1">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <span className="text-base font-bold tracking-tight text-foreground" translate="no">
                {reqTelemetry.routedName}
              </span>
              <span className="text-sm text-muted-foreground">
                {`· 启用 ${reqTelemetry.enabledAccounts.length} 个`}
              </span>
              {(() => {
                if (syncError) {
                  return <Badge variant="destructive-light">同步中断</Badge>
                }
                const busyCount = accounts.reduce((sum, a) => sum + (a.busy_workers || 0), 0)
                return busyCount > 0 ? (
                  <Badge variant="info-light">
                    <span className="relative mr-1.5 inline-flex size-1.5">
                      <span className="absolute inline-flex h-full w-full rounded-full bg-sky-400 opacity-75 animate-ping motion-reduce:animate-none" />
                      <span className="relative inline-flex size-1.5 rounded-full bg-sky-500" />
                    </span>
                    {`正在处理 ${busyCount} 个请求`}
                  </Badge>
                ) : (
                  <Badge variant="success-light">
                    <span className="relative mr-1.5 inline-flex size-1.5">
                      <span className="absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75 animate-ping motion-reduce:animate-none" />
                      <span className="relative inline-flex size-1.5 rounded-full bg-emerald-500" />
                    </span>
                    实时同步
                  </Badge>
                )
              })()}
            </div>
          </div>
          <p className="text-xs text-muted-foreground">
            {reqTelemetry.enabledAccounts.length <= 1
              ? '这里只开了一个账号或 Key，路由没有可选的。在供应商的编辑页再开一个，路由方式就在下面的「多账号 / 多 Key」里设置。'
              : `这里开启了 ${reqTelemetry.enabledAccounts.length} 个账号或 Key，网关按权重与健康状态在多账号池中自动路由。`}
          </p>
        </div>

        {/* 2. 3-Node Horizontal Topology Flow Diagram */}
        <div className="flex flex-col gap-3 lg:flex-row lg:items-center">
          {/* Left Node: Client / Agent */}
          <div className="flex flex-col items-center justify-center rounded-2xl border border-border bg-card px-5 py-3.5 min-w-[148px] text-center shadow-2xs transition-colors hover:border-indigo-500/50">
            <div className="mb-1.5 flex size-6 items-center justify-center rounded-md bg-indigo-500/12 text-indigo-600 dark:text-indigo-400">
              <Terminal className="size-3.5" />
            </div>
            <div className="flex items-center gap-1.5 text-xs font-bold text-foreground">
              <span className="relative flex size-1.5">
                <span className="absolute inline-flex h-full w-full rounded-full bg-indigo-400 opacity-75 animate-ping motion-reduce:animate-none" />
                <span className="relative inline-flex size-1.5 rounded-full bg-indigo-500" />
              </span>
              <span translate="no">{reqTelemetry.clientLabel}</span>
            </div>
            <div className="mt-0.5 font-mono text-xs text-muted-foreground truncate max-w-[144px]" translate="no">
              {reqTelemetry.requestedModel || '—'}
            </div>
          </div>

          {/* Connector Line 1 (Animated Northbound Stream) */}
          <div className="relative hidden lg:flex h-0.5 w-10 shrink-0 items-center overflow-hidden rounded-full bg-border/80">
            <span className="h-full w-1/2 rounded-full bg-indigo-500 animate-ag-flow-1 motion-reduce:animate-none" />
          </div>

          {/* Middle Node: Gateway (Dark Prominent Border) */}
          <div className="flex flex-col items-center justify-center rounded-2xl border-2 border-foreground/85 bg-card px-5 py-3.5 min-w-[148px] text-center shadow-2xs transition-colors hover:border-foreground">
            <div className="mb-1.5 flex size-6 items-center justify-center rounded-md bg-foreground text-background">
              <Sparkles className="size-3.5 animate-pulse motion-reduce:animate-none" />
            </div>
            <div className="text-xs font-bold text-foreground">antigravity</div>
            <div className="mt-0.5 font-mono text-xs text-muted-foreground">
              {gatewayHostPort}
            </div>
          </div>

          {/* Connector Line 2 (Animated Southbound ACP Stream) */}
          <div className="relative hidden lg:flex h-0.5 w-10 shrink-0 items-center overflow-hidden rounded-full bg-indigo-500/25">
            <span className="h-full w-1/2 rounded-full bg-indigo-500 dark:bg-indigo-400 animate-ag-flow-2 motion-reduce:animate-none" />
          </div>

          {/* Right Node: Routed Account Pill + Request Prompt Summary */}
          <div className="flex-1 rounded-2xl border border-foreground/75 bg-card px-4 py-3.5 flex flex-wrap items-center justify-between gap-3 shadow-2xs transition-colors hover:border-foreground">
            <div className="flex items-center gap-2.5 min-w-0">
              <span className="relative flex size-2 shrink-0">
                <span className="absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75 animate-ping motion-reduce:animate-none" />
                <span className="relative inline-flex size-2 rounded-full bg-emerald-500 dark:bg-foreground" />
              </span>
              <span className="flex size-5 shrink-0 items-center justify-center rounded bg-slate-900 text-xs font-bold text-sky-400 dark:bg-sky-500/20 dark:text-sky-300">
                {reqTelemetry.routedInitial}
              </span>
              <span className="text-xs font-bold text-foreground truncate" translate="no">
                {reqTelemetry.routedName}
              </span>
              <span className="rounded-md bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground truncate" translate="no">
                {reqTelemetry.actualModel || '—'}
              </span>
            </div>

            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground truncate max-w-full sm:max-w-[62%]">
              {reqTelemetry.activeLog ? (
                <>
                  <span className="truncate">
                    <span>回答了这个请求 · </span>
                    <span translate="no">
                      {reqTelemetry.sessionTitle || reqTelemetry.promptSummary}
                    </span>
                  </span>
                  <span
                    title={`会话 ID: ${reqTelemetry.clientSessionId || sessionDisplayId}`}
                    className="shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground"
                  >
                    #{sessionDisplayId}
                  </span>
                </>
              ) : (
                <span>等待首个客户端请求接入</span>
              )}
            </div>
          </div>
        </div>

        {/* 3. Summary Strip Below Flow + 3 Right-Aligned KPIs */}
        <div className="flex flex-wrap items-center justify-between gap-4 border-y border-border/70 py-3.5">
          <div className="text-xs font-semibold text-foreground">
            {reqTelemetry.enabledAccounts.length <= 1
              ? `${reqTelemetry.routedName} 只启用了一个 Key，无需选择。`
              : `${reqTelemetry.routedName} 从 ${reqTelemetry.enabledAccounts.length} 个启用账号中胜出承接此请求。`}
          </div>

          <div className="flex items-center gap-8 tabular-nums">
            <div>
              <div className="text-lg font-bold tracking-tight text-foreground">
                {formatCompact(reqTelemetry.totalReqs)}
              </div>
              <div className="text-xs text-muted-foreground">请求</div>
            </div>
            <div>
              <div className="text-lg font-bold tracking-tight text-foreground">
                {reqTelemetry.switchCount}
              </div>
              <div className="text-xs text-muted-foreground">改道</div>
            </div>
            <div>
              <div className="text-lg font-bold tracking-tight text-foreground">
                {reqTelemetry.errorReqs}
              </div>
              <div className="text-xs text-muted-foreground">Agent 收到的错误</div>
            </div>
          </div>
        </div>

        {/* 4. "最近一个请求是怎么路由的" Per-Request Trace & Timing */}
        <div className="flex flex-col gap-2.5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs font-bold text-foreground">
              最近一个请求是怎么路由的
            </span>
            <div className="flex items-center gap-4 text-xs text-muted-foreground">
              <button
                type="button"
                onClick={() =>
                  onOpenModal('latest_turn', reqTelemetry.activeLog?.request_id)
                }
                className="hover:text-foreground transition-colors"
              >
                请求与响应
              </button>
              <button
                type="button"
                onClick={() => onNavigateTab('accounts')}
                className="hover:text-foreground transition-colors"
              >
                查看用量
              </button>
              <button
                type="button"
                onClick={selectNextRequest}
                className="hover:text-foreground transition-colors"
              >
                下一个请求
              </button>
            </div>
          </div>

          <ul className="flex flex-col gap-1.5 text-xs text-foreground/90">
            {/* Bullet 1 */}
            <li className="flex items-center gap-2">
              <span className="size-1.5 shrink-0 rounded-full bg-muted-foreground/70" />
              <span>
                <span translate="no">{reqTelemetry.clientLabel}</span>
                {' 请求模型 '}
                <span className="font-mono" translate="no">{reqTelemetry.requestedModel || '—'}</span>
                {': 由 '}
                <strong className="font-semibold" translate="no">{reqTelemetry.routedName}</strong>
                {' 提供，向厂商请求的模型是 '}
                <span className="font-mono" translate="no">{reqTelemetry.actualModel || '—'}</span>
              </span>
            </li>

            {/* Bullet 2 */}
            <li className="flex items-center gap-2">
              <span className="size-1.5 shrink-0 rounded-full bg-foreground" />
              <span className="inline-flex items-center gap-1.5">
                <span className="inline-flex size-4 items-center justify-center rounded bg-slate-900 text-xs font-bold text-sky-400 dark:bg-sky-500/20 dark:text-sky-300">
                  {reqTelemetry.routedInitial}
                </span>
                <span>
                  {reqTelemetry.enabledAccounts.length <= 1
                    ? `${reqTelemetry.routedName} 只启用了一个 Key，无需选择。`
                    : `${reqTelemetry.routedName} 按多账号池权重调度提供服务。`}
                </span>
              </span>
            </li>

            {/* Bullet 3 */}
            <li className="flex items-center gap-2">
              <span className="size-1.5 shrink-0 rounded-full bg-emerald-500" />
              <span className="inline-flex flex-wrap items-center gap-1">
                <span className="inline-flex size-4 items-center justify-center rounded bg-slate-900 text-xs font-bold text-sky-400 dark:bg-sky-500/20 dark:text-sky-300">
                  {reqTelemetry.routedInitial}
                </span>
                <span>
                  <span translate="no">
                    {`${reqTelemetry.routedName} (${reqTelemetry.actualModel || '—'})`}
                  </span>
                  {' 以 '}
                  <span className="font-mono" translate="no">{reqTelemetry.thinkingLevel || '—'}</span>
                  {' 推理强度 用 '}
                  <strong className="font-semibold">
                    {formatDurationCn(reqTelemetry.latencyMs)}
                  </strong>
                  {` 完成回答 · ${formatCompact(reqTelemetry.reqTotalTokens)} token · ${formatDurationCn(reqTelemetry.firstWordMs)} 后出首字 · 请求缓存命中率 ${reqTelemetry.cacheHitPct}%。`}
                </span>
              </span>
            </li>

            {/* Bullet 4: 时间花在哪里 */}
            <li className="flex flex-col gap-2 pt-0.5">
              <div className="flex flex-wrap items-center gap-3 text-muted-foreground">
                <span className="inline-flex items-center gap-2 text-foreground">
                  <span className="size-1.5 shrink-0 rounded-full border border-muted-foreground" />
                  <span>时间花在哪里</span>
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <span className="size-2 rounded-xs bg-indigo-500" />
                  <span>{`网关 ${formatDurationCn(reqTelemetry.gwMs)}`}</span>
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <span className="size-2 rounded-xs bg-foreground/75" />
                  <span>{`供应商 ${formatDurationCn(reqTelemetry.providerMs)}`}</span>
                </span>
              </div>

              {/* Segmented Horizontal Timing Bar */}
              <div className="ps-3.5 flex flex-col gap-1.5">
                {(() => {
                  const total = reqTelemetry.latencyMs
                  if (total === null) return null
                  const segments: { key: string; cls: string; ms: number }[] = []
                  if (reqTelemetry.gwMs !== null) {
                    segments.push({ key: 'gw', cls: 'bg-indigo-500', ms: reqTelemetry.gwMs })
                  }
                  if (reqTelemetry.providerTtfbMs !== null) {
                    segments.push({ key: 'ttfb', cls: 'bg-foreground/75', ms: reqTelemetry.providerTtfbMs })
                  } else if (reqTelemetry.gwMs === null && reqTelemetry.firstWordMs !== null) {
                    segments.push({ key: 'ttfb', cls: 'bg-foreground/75', ms: reqTelemetry.firstWordMs })
                  }
                  if (reqTelemetry.streamMs !== null) {
                    segments.push({ key: 'stream', cls: 'bg-foreground/45', ms: reqTelemetry.streamMs })
                  }
                  return (
                    <div className="flex h-1.5 w-full max-w-xl overflow-hidden rounded-full bg-muted gap-0.5">
                      {segments.map((seg) => (
                        <div
                          key={seg.key}
                          className={`h-full ${seg.cls}`}
                          style={{ width: `${Math.min(100, (seg.ms / total) * 100)}%` }}
                        />
                      ))}
                    </div>
                  )
                })()}

                {/* 2-Row Detailed Sub-legend */}
                <div className="flex flex-wrap items-center gap-4 text-xs text-muted-foreground tabular-nums">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-1.5 rounded-xs bg-indigo-500" />
                    <span>{`网关内耗时 ${formatDurationCn(reqTelemetry.gwMs)}`}</span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-1.5 rounded-xs bg-foreground/75" />
                    <span>{`供应商（首字节） ${formatDurationCn(reqTelemetry.providerTtfbMs)}`}</span>
                  </span>
                </div>
                <div className="flex flex-wrap items-center gap-4 text-xs text-muted-foreground tabular-nums">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-1.5 rounded-xs bg-foreground/65" />
                    <span>{`首字耗时 ${formatDurationCn(reqTelemetry.firstWordMs)}`}</span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-1.5 rounded-xs bg-foreground/45" />
                    <span>{`流式输出 ${formatDurationCn(reqTelemetry.streamMs)}`}</span>
                  </span>
                </div>
              </div>
            </li>
          </ul>
        </div>

        {/* 5. Per-Request Context Window Card ("上下文窗口") */}
        <div className="rounded-2xl border border-border bg-card p-5 flex flex-col gap-4 shadow-2xs">
          {/* Card Header */}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <button
              type="button"
              onClick={() => setContextCollapsed((v) => !v)}
              className="inline-flex items-center gap-1.5 text-sm font-bold text-foreground cursor-pointer"
            >
              {contextCollapsed ? (
                <ChevronRight className="size-4 text-muted-foreground" />
              ) : (
                <ChevronDown className="size-4 text-muted-foreground" />
              )}
              <span>上下文窗口</span>
            </button>

            <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <span className="inline-flex items-center gap-1.5 font-medium text-foreground">
                <span className="inline-flex size-4 items-center justify-center rounded-full bg-indigo-500/15 text-indigo-600 dark:text-indigo-300">
                  <Terminal className="size-2.5" />
                </span>
                <span translate="no">{reqTelemetry.clientLabel}</span>
              </span>
              {reqTelemetry.effectiveKeyName && (
                <>
                  <span>›</span>
                  <span className="font-mono text-muted-foreground" translate="no">
                    Key:{reqTelemetry.effectiveKeyName}
                  </span>
                </>
              )}
              <span>›</span>
              <span className="inline-flex items-center gap-1 font-mono text-muted-foreground">
                <Sparkles className="size-3 text-muted-foreground" />
                <span translate="no">{reqTelemetry.requestedModel || '—'}</span>
              </span>
              {(reqTelemetry.effectivePromptMode ||
                reqTelemetry.effectiveDisableBuiltin !== undefined) && (
                <Badge variant="outline">
                  {[
                    reqTelemetry.effectivePromptMode
                      ? formatPromptModeShort(reqTelemetry.effectivePromptMode)
                      : '',
                    reqTelemetry.effectiveDisableBuiltin === undefined
                      ? ''
                      : reqTelemetry.effectiveDisableBuiltin
                        ? '仅客户端工具'
                        : '含内置工具',
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                </Badge>
              )}
              <span className="inline-flex items-center gap-1.5 rounded-full bg-emerald-500/15 px-2.5 py-0.5 text-xs font-medium text-emerald-700 dark:text-emerald-300">
                <span className="size-1.5 rounded-full bg-emerald-500" />
                <span>已计数</span>
              </span>
            </div>
          </div>

          {!contextCollapsed && (
            <>
              {/* Two-Column Per-Request Headline KPIs */}
              <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
                {/* Left Column: 已用上下文 */}
                <div className="flex flex-col gap-1">
                  <div className="flex items-center gap-2 text-xs text-muted-foreground">
                    <span>已用上下文</span>
                    <Badge
                      variant={
                        reqTelemetry.usedPct >= 75
                          ? 'warning-light'
                          : reqTelemetry.usedPct >= 40
                            ? 'info-light'
                            : 'success-light'
                      }
                    >
                      {reqTelemetry.usedPct >= 75
                        ? '渐满'
                        : reqTelemetry.usedPct >= 40
                          ? '适中'
                          : '充裕'}
                    </Badge>
                  </div>
                  <div className="flex items-baseline gap-1.5 tabular-nums">
                    <span className="text-3xl font-bold tracking-tight text-foreground">
                      {formatCompact(reqTelemetry.usedContextTokens)}
                    </span>
                    <span className="text-sm text-muted-foreground">
                      / {formatCompact(reqTelemetry.contextCapacity)} token
                    </span>
                  </div>
                  <div className="text-xs text-muted-foreground tabular-nums">
                    {reqTelemetry.estRemainingReqs === null
                      ? `已用 ${reqTelemetry.usedPct}%`
                      : `已用 ${reqTelemetry.usedPct}% · 约还能容纳 ${reqTelemetry.estRemainingReqs} 个请求`}
                  </div>
                </div>

                {/* Right Column: 提示词缓存 */}
                <div className="flex flex-col gap-1.5">
                  <div className="text-xs text-muted-foreground">提示词缓存</div>
                  <div className="flex items-baseline gap-1.5 tabular-nums">
                    <span className="text-3xl font-bold tracking-tight text-foreground">
                      {reqTelemetry.cacheHitPct}%
                    </span>
                    <span className="text-sm text-muted-foreground">命中</span>
                  </div>
                  <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
                    <div
                      className="h-full rounded-full bg-emerald-600 dark:bg-emerald-500"
                      style={{ width: `${reqTelemetry.cacheHitPct}%` }}
                    />
                  </div>
                  <div className="text-xs text-muted-foreground tabular-nums">
                    {`${formatCompact(reqTelemetry.cachedTokens)} 来自缓存 · ${formatCompact(reqTelemetry.newInputTokens)} 新增`}
                  </div>
                </div>
              </div>

              {/* 2D Proportional Treemap Mosaic / Waffle Grid View */}
              {mosaicViewMode === 'treemap' ? (
                <ContextTreemapView
                  contextCapacity={reqTelemetry.contextCapacity}
                  remainingTokens={reqTelemetry.remainingTokens}
                  categoryTotals={reqTelemetry.categoryTotals}
                  groupedForTreemap={groupedForTreemap}
                  activeLogId={reqTelemetry.activeLog?.request_id}
                  disableBuiltinTools={reqTelemetry.effectiveDisableBuiltin}
                  promptMode={reqTelemetry.effectivePromptMode}
                  onOpenModal={onOpenModal}
                />
              ) : (
                /* Waffle Grid View (100 proportional cells representing this request's context window) */
                <div className="rounded-xl border border-border bg-muted/20 p-3">
                  <div className="grid grid-cols-20 gap-1">
                    {Array.from({ length: 100 }).map((_, cellIdx) => {
                      const cap = Math.max(1, reqTelemetry.contextCapacity)
                      const sysCells = Math.round(
                        (reqTelemetry.categoryTotals.system_prompt / cap) * 100
                      )
                      const toolCells = Math.round((reqTelemetry.categoryTotals.tools / cap) * 100)
                      const skillCells = Math.round(
                        (reqTelemetry.categoryTotals.skills / cap) * 100
                      )
                      const fileCells = Math.round((reqTelemetry.categoryTotals.files / cap) * 100)
                      const resCells = Math.round(
                        (reqTelemetry.categoryTotals.tool_results / cap) * 100
                      )
                      const convCells = Math.round(
                        (reqTelemetry.categoryTotals.conversation / cap) * 100
                      )

                      let cellClass = 'bg-muted-foreground/15'
                      if (cellIdx < sysCells) cellClass = 'bg-violet-500'
                      else if (cellIdx < sysCells + toolCells) cellClass = 'bg-sky-500'
                      else if (cellIdx < sysCells + toolCells + skillCells)
                        cellClass = 'bg-slate-500'
                      else if (cellIdx < sysCells + toolCells + skillCells + fileCells)
                        cellClass = 'bg-emerald-500'
                      else if (
                        cellIdx <
                        sysCells + toolCells + skillCells + fileCells + resCells
                      )
                        cellClass = 'bg-orange-500'
                      else if (
                        cellIdx <
                        sysCells + toolCells + skillCells + fileCells + resCells + convCells
                      )
                        cellClass = 'bg-indigo-500'

                      return (
                        <div
                          key={cellIdx}
                          className={`h-3 rounded-xs ${cellClass}`}
                        />
                      )
                    })}
                  </div>
                </div>
              )}

              {/* Category Legend Bar + Right View Switcher Icons */}
              <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground tabular-nums">
                <div className="flex flex-wrap items-center gap-4">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-xs bg-violet-500" />
                    <span>
                      {`系统提示词 ${formatCompact(reqTelemetry.categoryTotals.system_prompt)}`}
                    </span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-xs bg-sky-500" />
                    <span>{`工具 ${formatCompact(reqTelemetry.categoryTotals.tools)}`}</span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-xs bg-slate-500" />
                    <span>{`技能 ${formatCompact(reqTelemetry.categoryTotals.skills)}`}</span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-xs bg-emerald-500" />
                    <span>{`文件 ${formatCompact(reqTelemetry.categoryTotals.files)}`}</span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-xs bg-orange-500" />
                    <span>
                      {`工具结果 ${formatCompact(reqTelemetry.categoryTotals.tool_results)}`}
                    </span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-xs bg-indigo-500" />
                    <span>
                      {`会话内容 ${formatCompact(reqTelemetry.categoryTotals.conversation)}`}
                    </span>
                  </span>
                  <span className="inline-flex items-center gap-1.5">
                    <span className="size-2 rounded-xs border border-border bg-muted/60" />
                    <span>{`剩余空间 ${formatCompact(reqTelemetry.remainingTokens)}`}</span>
                  </span>
                </div>

                {/* Treemap vs Waffle Grid View Toggle */}
                <div className="inline-flex items-center rounded-lg border border-border bg-muted/40 p-0.5">
                  <button
                    type="button"
                    aria-label="矩形树图视图"
                    onClick={() => setMosaicViewMode('treemap')}
                    className={`rounded-md p-1 transition-colors ${
                      mosaicViewMode === 'treemap'
                        ? 'bg-card text-foreground shadow-2xs'
                        : 'text-muted-foreground hover:text-foreground'
                    }`}
                  >
                    <LayoutGrid className="size-3.5" />
                  </button>
                  <button
                    type="button"
                    aria-label="网格点阵视图"
                    onClick={() => setMosaicViewMode('waffle')}
                    className={`rounded-md p-1 transition-colors ${
                      mosaicViewMode === 'waffle'
                        ? 'bg-card text-foreground shadow-2xs'
                        : 'text-muted-foreground hover:text-foreground'
                    }`}
                  >
                    <Grid className="size-3.5" />
                  </button>
                </div>
              </div>

              {/* 6. Per-Request Content Breakdown List ("内容") */}
              <div className="flex flex-col gap-2.5 border-t border-border/70 pt-3.5">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-xs font-medium text-muted-foreground">内容</span>

                  {/* 7 Filter Pills matching 图片 6.png */}
                  <div className="inline-flex flex-wrap items-center gap-0.5 rounded-lg border border-border/70 bg-muted/40 p-0.5">
                    {(
                      [
                        { id: 'all', label: '全部' },
                        { id: 'system_prompt', label: '系统提示词' },
                        { id: 'tools', label: '工具' },
                        { id: 'skills', label: '技能' },
                        { id: 'files', label: '文件' },
                        { id: 'tool_results', label: '工具结果' },
                        { id: 'conversation', label: '会话内容' },
                      ] as { id: RequestContextCategory; label: string }[]
                    ).map((tab) => (
                      <button
                        key={tab.id}
                        type="button"
                        onClick={() => setBreakdownFilter(tab.id)}
                        className={`rounded-md px-2.5 py-1 text-xs font-medium transition-colors ${
                          breakdownFilter === tab.id
                            ? 'bg-card text-foreground shadow-2xs font-semibold'
                            : 'text-muted-foreground hover:text-foreground'
                        }`}
                      >
                        {tab.label}
                      </button>
                    ))}
                  </div>
                </div>

                {/* Ranked Per-Request Items */}
                <div className="flex flex-col gap-1.5">
                  {filteredEntries.map((item) => {
                    const barPct = Math.max(
                      4,
                      Math.min(100, Math.round((item.tokens / maxEntryTokens) * 100))
                    )
                    const handleRowClick = () => {
                      const modalKey: TreemapDetailKey =
                        item.category === 'system_prompt' || item.category === 'skills'
                          ? 'system_prompt'
                          : item.category === 'tools'
                            ? 'protocols'
                            : item.category === 'files'
                              ? 'prompt_input'
                              : item.category === 'tool_results'
                                ? 'completion_tools'
                                : 'latest_turn'
                      onOpenModal(modalKey, reqTelemetry.activeLog?.request_id, {
                        category: item.category,
                        title: item.title,
                        subtitle: item.subtitle,
                        tag: item.tag,
                        tokens: item.tokens,
                        count: item.count,
                        items: [item],
                      })
                    }

                    return (
                      <button
                        key={item.id}
                        type="button"
                        onClick={handleRowClick}
                        className="w-full flex items-center justify-between gap-3 rounded-md px-1.5 py-1 -mx-1.5 text-xs text-start cursor-pointer transition-colors hover:bg-muted/50"
                      >
                        <div className="flex items-center gap-2 min-w-0 flex-1">
                          <span className={`size-2 shrink-0 rounded-xs ${item.dotClass}`} />
                          <span className="font-semibold text-foreground truncate">
                            {item.title}
                          </span>
                          {item.subtitle && (
                            <span className="text-muted-foreground truncate" translate="no">
                              {item.subtitle}
                            </span>
                          )}
                        </div>

                        <div className="flex items-center gap-3 shrink-0 tabular-nums">
                          {item.count !== undefined && item.count > 1 && (
                            <span className="text-muted-foreground">×{item.count}</span>
                          )}
                          <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
                            {item.tag}
                          </span>
                          <div className="w-20 h-1.5 rounded-full bg-muted overflow-hidden">
                            <div
                              className={`h-full rounded-full ${item.barClass}`}
                              style={{ width: `${barPct}%` }}
                            />
                          </div>
                          <span className="w-12 text-end font-mono font-medium text-foreground">
                            {formatCompact(item.tokens)}
                          </span>
                        </div>
                      </button>
                    )
                  })}
                </div>

                {totalFilteredCount > 8 && (
                  <div>
                    <button
                      type="button"
                      onClick={() => setShowAllItems((v) => !v)}
                      className="text-xs text-muted-foreground hover:text-foreground transition-colors"
                    >
                      {showAllItems ? '收起' : `显示全部 ${totalFilteredCount} 项`}
                    </button>
                  </div>
                )}
              </div>

              {/* 7. Bottom Session Timeline Footer ("本会话") */}
              <div className="flex flex-col gap-2 border-t border-border/70 pt-3">
                <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                  <div className="flex items-center gap-1.5 truncate">
                    <span translate="no">{reqTelemetry.clientLabel}</span>
                    <span>›</span>
                    <span
                      title={reqTelemetry.sessionTitle || reqTelemetry.promptSummary}
                      className="font-medium text-foreground truncate max-w-md"
                      translate="no"
                    >
                      {reqTelemetry.sessionTitle || reqTelemetry.promptSummary}
                    </span>
                    {reqTelemetry.sessionTitle &&
                      reqTelemetry.promptSummary &&
                      reqTelemetry.sessionTitle !== reqTelemetry.promptSummary && (
                        <>
                          <span>·</span>
                          <span className="truncate max-w-xs text-muted-foreground" translate="no">
                            {reqTelemetry.promptSummary}
                          </span>
                        </>
                      )}
                    <span>›</span>
                    <span
                      title={`客户端会话 ID: ${reqTelemetry.clientSessionId || sessionDisplayId}`}
                      className="font-mono text-foreground/90"
                    >
                      #{sessionDisplayId}
                    </span>
                  </div>
                  <span className="tabular-nums">{`${reqTelemetry.turnCount} 轮`}</span>
                </div>

                <div className="flex items-center gap-3">
                  <span className="text-xs text-muted-foreground shrink-0">本会话</span>
                  <div className="flex-1 overflow-hidden rounded-md bg-indigo-500/6 px-1">
                    <svg
                      viewBox="0 0 800 42"
                      className="h-10 w-full overflow-visible"
                      role="img"
                      aria-label="本会话请求上下文趋势"
                    >
                      <path
                        d={reqTelemetry.areaPath}
                        className="fill-indigo-500/15 dark:fill-indigo-400/15"
                      />
                      <path
                        d={reqTelemetry.linePath}
                        fill="none"
                        strokeWidth="1.75"
                        className="stroke-indigo-500 dark:stroke-indigo-400"
                      />
                      {reqTelemetry.sparkPoints.map((pt, idx) => (
                        <circle
                          key={pt.reqId || idx}
                          cx={pt.x}
                          cy={pt.y}
                          r={pt.isSelected ? 4 : 2.5}
                          onClick={() => {
                            if (pt.reqId) onSelectAuditLogId(pt.reqId)
                          }}
                          className={`cursor-pointer ${
                            pt.isSelected
                              ? 'fill-background stroke-indigo-600 dark:stroke-indigo-400'
                              : 'fill-indigo-500/50 hover:fill-indigo-500'
                          }`}
                          strokeWidth={pt.isSelected ? 2 : 0}
                        >
                          <title>
                            {`请求 #${idx + 1} · ${formatCompact(pt.tokens)} token (点击切换检视该请求)`}
                          </title>
                        </circle>
                      ))}
                    </svg>
                  </div>
                  <span className="text-xs text-muted-foreground tabular-nums shrink-0">
                    {`${reqTelemetry.sessionLogs.length} 个请求`}
                  </span>
                </div>
              </div>
            </>
          )}
        </div>

        {/* 8. Bottom Request Selector & Account/Key Telemetry Section (matching 图片 2.png) */}
        {(() => {
          const {
            formatTimeHMS,
            formatWeekdayHM,
            visibleDateBuckets,
            olderDateBuckets,
            displayRows,
            workerProcessGroups,
          } = bottomSelectorData

          return (
            <div className="grid grid-cols-1 gap-6 lg:grid-cols-12 items-start pt-1">
              {/* Left Column: 请求 (Requests Selector & Filter Bar) */}
              <div className="lg:col-span-8 flex flex-col gap-3">
                {/* Top Meta Header Row */}
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <span className="text-xs font-medium text-muted-foreground">请求</span>
                    {selectedAccountOrKeyFilter && (
                      <button
                        type="button"
                        onClick={() => setSelectedAccountOrKeyFilter('')}
                        className="rounded-md bg-muted px-2 py-0.5 text-xs text-foreground transition-colors hover:bg-accent cursor-pointer"
                      >
                        清除右侧筛选 ×
                      </button>
                    )}
                  </div>

                  <div className="flex flex-wrap items-center gap-3.5 text-xs text-muted-foreground">
                    <span className="tabular-nums">
                      {`控制台已加载的最近 ${auditLogs.length} 个请求`}
                    </span>
                    <button
                      type="button"
                      onClick={() =>
                        setRequestPurposeFilter((prev) =>
                          prev === 'all' ? 'chat' : prev === 'chat' ? 'tool' : 'all'
                        )
                      }
                      className="inline-flex items-center gap-1 transition-colors hover:text-foreground cursor-pointer"
                    >
                      <span>
                        {`用途${
                          requestPurposeFilter === 'chat'
                            ? ' · 对话'
                            : requestPurposeFilter === 'tool'
                              ? ' · 工具续接'
                              : ''
                        }`}
                      </span>
                      <ChevronDown className="size-3" />
                    </button>
                    <button
                      type="button"
                      onClick={() =>
                        setRequestMetricMode((prev) => (prev === 'full' ? 'compact' : 'full'))
                      }
                      className="inline-flex items-center gap-1 transition-colors hover:text-foreground cursor-pointer"
                    >
                      <span>{requestMetricMode === 'full' ? '指标 · 完整' : '指标 · 精简'}</span>
                      <ChevronDown className="size-3" />
                    </button>
                  </div>
                </div>

                {/* Second Filter Pills Bar: Single-Row Date Pills on Left + 按请求/按会话 on Right */}
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <div className="flex flex-wrap items-center gap-1.5">
                    <button
                      type="button"
                      onClick={() => {
                        setRequestDateFilter('realtime')
                        onSelectAuditLogId('')
                      }}
                      className={`rounded-full border px-3 py-1 text-xs whitespace-nowrap transition-colors cursor-pointer ${
                        requestDateFilter === 'realtime'
                          ? 'border-foreground bg-card font-semibold text-foreground shadow-2xs'
                          : 'border-border bg-muted/25 text-muted-foreground hover:text-foreground'
                      }`}
                    >
                      实时
                    </button>

                    {visibleDateBuckets.map((b) => {
                      const isActive = requestDateFilter === b.key
                      return (
                        <button
                          key={b.key}
                          type="button"
                          title={`共 ${b.count} 次请求 · 峰值上下文 ${formatCompact(b.maxTokens)} token`}
                          onClick={() => {
                            setRequestDateFilter(b.key)
                            if (b.logs[0]) {
                              onSelectAuditLogId(
                                b.logs[0].request_id === auditLogs[0]?.request_id
                                  ? ''
                                  : b.logs[0].request_id
                              )
                            }
                          }}
                          className={`inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs whitespace-nowrap transition-colors cursor-pointer tabular-nums ${
                            isActive
                              ? 'border-foreground bg-card font-semibold text-foreground shadow-2xs'
                              : 'border-border bg-muted/25 text-muted-foreground hover:text-foreground'
                          }`}
                        >
                          <span>{b.label}</span>
                          <span className="text-muted-foreground/80">{b.count}</span>
                        </button>
                      )
                    })}

                    {olderDateBuckets.length > 0 && (
                      <button
                        type="button"
                        onClick={() =>
                          setRequestDateFilter((prev) =>
                            prev === 'older' ? 'realtime' : 'older'
                          )
                        }
                        className={`inline-flex items-center gap-1 rounded-full border px-3 py-1 text-xs whitespace-nowrap transition-colors cursor-pointer ${
                          requestDateFilter === 'older'
                            ? 'border-foreground bg-card font-semibold text-foreground shadow-2xs'
                            : 'border-border bg-muted/25 text-muted-foreground hover:text-foreground'
                        }`}
                      >
                        <span>更早的日期</span>
                        <ChevronDown className="size-3" />
                      </button>
                    )}
                  </div>

                  <div className="flex items-center gap-1.5 shrink-0">
                    <button
                      type="button"
                      onClick={() => setRequestGroupMode('request')}
                      className={`rounded-full border px-3 py-1 text-xs whitespace-nowrap transition-colors cursor-pointer ${
                        requestGroupMode === 'request'
                          ? 'border-foreground bg-card font-semibold text-foreground shadow-2xs'
                          : 'border-border bg-muted/25 text-muted-foreground hover:text-foreground'
                      }`}
                    >
                      按请求
                    </button>
                    <button
                      type="button"
                      onClick={() => setRequestGroupMode('session')}
                      className={`rounded-full border px-3 py-1 text-xs whitespace-nowrap transition-colors cursor-pointer ${
                        requestGroupMode === 'session'
                          ? 'border-foreground bg-card font-semibold text-foreground shadow-2xs'
                          : 'border-border bg-muted/25 text-muted-foreground hover:text-foreground'
                      }`}
                    >
                      按会话
                    </button>
                  </div>
                </div>

                {/* Scrollable Request / Session Cards List */}
                <div className="rounded-2xl border border-border bg-card p-1.5 max-h-[340px] overflow-y-auto flex flex-col gap-1">
                  {displayRows.length === 0 ? (
                    <div className="flex flex-col items-center justify-center gap-2 py-10 text-center">
                      <div className="text-xs text-muted-foreground">
                        当前筛选条件下暂无匹配的请求记录
                      </div>
                      <button
                        type="button"
                        onClick={() => {
                          setRequestDateFilter('realtime')
                          setRequestPurposeFilter('all')
                          setSelectedAccountOrKeyFilter('')
                        }}
                        className="rounded-lg border border-border bg-muted/40 px-3 py-1 text-xs font-medium text-foreground transition-colors hover:bg-accent cursor-pointer"
                      >
                        重置筛选条件
                      </button>
                    </div>
                  ) : (
                    displayRows.map(
                      ({
                        rowKey,
                        representative: log,
                        sessionLogs: rowSessLogs,
                        sessionCount,
                        totalOutputTokens,
                      }) => {
                        const isSelected =
                          reqTelemetry.activeLog?.request_id === log.request_id ||
                          (requestGroupMode === 'session' &&
                            Boolean(log.session_id) &&
                            reqTelemetry.activeLog?.session_id === log.session_id)
                        const snap = parseAuditContextSnapshot(log)
                        const rowInputTok =
                          snap?.input_tokens && snap.input_tokens > 0
                            ? snap.input_tokens
                            : log.prompt_tokens || 0
                        const rowOutputTok = resolvePureOutputTokens(log, snap)
                        const rowTotalTok = rowInputTok + rowOutputTok
                        const rowCachedTok = Math.min(
                          rowInputTok,
                          snap?.cached_tokens && snap.cached_tokens > 0
                            ? snap.cached_tokens
                            : log.cached_tokens || 0
                        )
                        const rowCachePct =
                          rowInputTok > 0
                            ? rowCachedTok >= rowInputTok
                              ? 100
                              : Math.min(99, Math.round((rowCachedTok / rowInputTok) * 100))
                            : 0

                        const rowAccSnap = accounts.find((a) => a.account.id === log.account_id)
                        const rowAccName =
                          rowAccSnap?.account.name &&
                          rowAccSnap.account.name !== 'Default Host Account'
                            ? rowAccSnap.account.name
                            : log.account_id || '—'
                        const rowAccInitial = log.account_id
                          ? (rowAccName.trim()[0] || '?').toUpperCase()
                          : '—'

                        const rawModel = log.model_name || ''
                        const shortModel = rawModel.includes('/')
                          ? rawModel.split('/').pop() || rawModel
                          : rawModel
                        const clientPrefix = snap?.key_name || log.account_id || ''
                        const displayPath = rawModel.includes('/')
                          ? rawModel
                          : clientPrefix
                            ? `${clientPrefix}/${shortModel || '—'}`
                            : shortModel || '—'

                        const rowThinkingLevel =
                          snap?.thinking_level || log.thinking_level || '—'
                        const rowSessionIdFull =
                          snap?.client_session_id || log.session_id || log.request_id
                        const rowSessionShortId = formatShortSessionId(rowSessionIdFull)
                        const rowSessionTitle = extractSessionTitle(log, snap, rowSessLogs)
                        const rowTurnSummary = extractPromptGoalSummary(log, snap, rowSessLogs)
                        const rowFirstWordMs: number | null =
                          snap?.ttfb_ms && snap.ttfb_ms > 0
                            ? Math.min(log.latency_ms || snap.ttfb_ms, snap.ttfb_ms)
                            : null
                        const isUnparkedContinuation =
                          !log.response_text &&
                          Boolean(log.tool_calls_json) &&
                          rowFirstWordMs !== null &&
                          rowFirstWordMs <= 50
                        const rowStreamSec =
                          isUnparkedContinuation || rowFirstWordMs === null
                            ? 0
                            : Math.max(0, ((log.latency_ms || 0) - rowFirstWordMs) / 1000)
                        const tokPerSec =
                          rowOutputTok > 10 && rowStreamSec >= 0.4
                            ? Math.round(rowOutputTok / rowStreamSec)
                            : 0

                        const rowCap = resolveEffectiveContextCapacity(rawModel, snap)
                        const rowUsedPct =
                          rowCap > 0
                            ? Math.min(100, Math.round((rowInputTok / rowCap) * 100))
                            : 0
                        const isToolTurn =
                          Boolean(log.tool_calls_json) ||
                          (!log.response_text &&
                            ((log.prompt_text || '').includes('tool_result') ||
                              (log.prompt_text || '').includes('function_call_output')))
                        const isErr = log.status_code >= 400

                        return (
                          <div
                            key={rowKey}
                            role="button"
                            tabIndex={0}
                            onClick={() =>
                              onSelectAuditLogId(
                                log.request_id === auditLogs[0]?.request_id ? '' : log.request_id
                              )
                            }
                            onKeyDown={(e) => {
                              if (e.key === 'Enter' || e.key === ' ') {
                                e.preventDefault()
                                onSelectAuditLogId(
                                  log.request_id === auditLogs[0]?.request_id ? '' : log.request_id
                                )
                              }
                            }}
                            className={`w-full rounded-xl border px-3 py-2 text-start transition-colors cursor-pointer ${
                              isSelected
                                ? 'border-border bg-muted/40 shadow-2xs'
                                : 'border-transparent hover:bg-muted/25'
                            }`}
                          >
                            <div className="flex items-start gap-2.5">
                              {/* Fixed Left Timestamp Column */}
                              <span className="w-14 shrink-0 pt-0.5 font-mono text-xs text-muted-foreground tabular-nums">
                                {formatTimeHMS(log.created_at)}
                              </span>

                              {/* Right Compact Two-Row Column */}
                              <div className="flex-1 min-w-0 flex flex-col gap-1">
                                {/* Row 1: Single-Line Route + Right-Aligned Session Tag */}
                                <div className="flex items-center justify-between gap-2 text-xs whitespace-nowrap overflow-hidden">
                                  <div className="flex items-center gap-1.5 min-w-0 overflow-hidden">
                                    <span
                                      className={`size-2 shrink-0 rounded-full ${
                                        isErr ? 'bg-destructive' : 'bg-indigo-500'
                                      }`}
                                    />
                                    <span className="inline-flex size-4 shrink-0 items-center justify-center rounded-full bg-indigo-500/15 text-indigo-600 dark:text-indigo-400">
                                      <Terminal className="size-2.5" />
                                    </span>
                                    <span className="font-mono text-xs font-semibold text-foreground truncate max-w-[165px]" translate="no">
                                      {displayPath}
                                    </span>

                                    <span
                                      className={`ms-1 size-1.5 shrink-0 rounded-full ${
                                        isErr ? 'bg-destructive' : 'bg-emerald-500'
                                      }`}
                                    />
                                    <span className="inline-flex size-4 shrink-0 items-center justify-center rounded bg-slate-900 text-xs font-bold text-sky-400 dark:bg-sky-500/20 dark:text-sky-300">
                                      {rowAccInitial}
                                    </span>
                                    <span className="text-xs font-semibold text-foreground shrink-0" translate="no">
                                      {rowAccName}
                                    </span>

                                    <span className="text-muted-foreground shrink-0">·</span>
                                    <span className="font-mono text-xs text-foreground truncate max-w-[130px]" translate="no">
                                      {shortModel || '—'}
                                    </span>

                                    <span className="text-muted-foreground shrink-0">·</span>
                                    <span className="font-mono text-xs text-muted-foreground shrink-0" translate="no">
                                      {rowThinkingLevel}
                                    </span>
                                  </div>

                                  <div className="flex items-center gap-1.5 shrink-0">
                                    {(rowSessionTitle || rowTurnSummary) && (
                                      <span
                                        title={rowSessionTitle || rowTurnSummary}
                                        className="hidden sm:inline-block text-xs text-muted-foreground truncate max-w-[140px]"
                                        translate="no"
                                      >
                                        {rowSessionTitle || rowTurnSummary}
                                      </span>
                                    )}
                                    <span
                                      title={`客户端会话 ID: ${rowSessionIdFull}${
                                        rowSessionTitle ? ` · ${rowSessionTitle}` : ''
                                      }`}
                                      className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground tabular-nums"
                                    >
                                      #{rowSessionShortId}
                                    </span>
                                    {requestGroupMode === 'session' && (
                                      <Badge variant="info-light">{`${sessionCount} 轮`}</Badge>
                                    )}
                                  </div>
                                </div>

                                {/* Row 2: Single-Line Compact Metrics */}
                                <div className="flex items-center gap-1.5 text-xs text-muted-foreground tabular-nums whitespace-nowrap overflow-hidden">
                                  <span>{formatDurationCn(log.latency_ms)}</span>
                                  <span>·</span>
                                  <span>{`首字耗时 ${formatDurationCn(rowFirstWordMs)}`}</span>
                                  <span>·</span>
                                  <span>
                                    {`${formatCompact(rowTotalTok).toLowerCase()} token (${rowUsedPct}%)`}
                                  </span>
                                  <span>·</span>
                                  <span>{`缓存 ${rowCachePct}%`}</span>
                                  {requestGroupMode === 'session' && totalOutputTokens > 0 ? (
                                    <>
                                      <span>·</span>
                                      <span>{`累计输出 ${formatCompact(totalOutputTokens)}`}</span>
                                    </>
                                  ) : (
                                    rowOutputTok > 0 && (
                                      <>
                                        <span>·</span>
                                        <span>{`输出 ${formatCompact(rowOutputTok)}`}</span>
                                      </>
                                    )
                                  )}
                                  {requestMetricMode === 'full' && tokPerSec > 0 && (
                                    <>
                                      <span>·</span>
                                      <span>{`${tokPerSec} token/秒`}</span>
                                    </>
                                  )}
                                  <span className="text-muted-foreground/70">·</span>
                                  {isToolTurn || !rowTurnSummary ? (
                                    <span className="truncate text-muted-foreground/85">
                                      {isToolTurn ? '工具续接' : '对话'}
                                    </span>
                                  ) : (
                                    <span className="truncate text-muted-foreground/85" translate="no">
                                      {rowTurnSummary}
                                    </span>
                                  )}
                                </div>

                                {/* Row 3 (Session Mode Only): Quick Turn Switcher within Selected Session */}
                                {requestGroupMode === 'session' &&
                                  isSelected &&
                                  rowSessLogs.length > 1 && (
                                    <div className="mt-1 pt-1.5 border-t border-border/60 flex flex-wrap items-center gap-1">
                                      <span className="text-xs text-muted-foreground me-1">
                                        轮次:
                                      </span>
                                      {rowSessLogs.slice(0, 8).map((sLog, sIdx) => {
                                        const isTurnActive =
                                          reqTelemetry.activeLog?.request_id === sLog.request_id
                                        const turnNum = rowSessLogs.length - sIdx
                                        return (
                                          <button
                                            key={sLog.request_id}
                                            type="button"
                                            onClick={(e) => {
                                              e.stopPropagation()
                                              onSelectAuditLogId(
                                                sLog.request_id === auditLogs[0]?.request_id
                                                  ? ''
                                                  : sLog.request_id
                                              )
                                            }}
                                            className={`rounded-md border px-2 py-0.5 font-mono text-xs transition-colors cursor-pointer tabular-nums ${
                                              isTurnActive
                                                ? 'border-foreground bg-card font-semibold text-foreground shadow-2xs'
                                                : 'border-border/70 bg-muted/30 text-muted-foreground hover:text-foreground'
                                            }`}
                                          >
                                            #{turnNum} · {formatTimeHMS(sLog.created_at)}
                                          </button>
                                        )
                                      })}
                                    </div>
                                  )}
                              </div>
                            </div>
                          </div>
                        )
                      }
                    )
                  )}
                </div>
              </div>

              {/* Right Column: 底层进程与绑定会话 (Worker Processes & Bound Sessions) */}
              <div className="lg:col-span-4 flex flex-col gap-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs font-medium text-muted-foreground">
                    底层进程与绑定会话
                  </span>
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {`在线 ${overview?.workers?.length || 0} 个进程`}
                  </span>
                </div>

                <div className="rounded-2xl border border-border bg-card divide-y divide-border/70 overflow-hidden max-h-[380px] overflow-y-auto">
                  {workerProcessGroups.length === 0 ? (
                    <div className="flex flex-col items-center justify-center gap-3 py-10 px-4 text-center">
                      <div className="text-xs text-muted-foreground">当前暂无运行中的底层进程</div>
                      <div className="flex flex-wrap items-center justify-center gap-2">
                        <Button variant="outline" size="xs" onClick={onRefresh}>
                          <RefreshCw className="size-3.5" />
                          刷新
                        </Button>
                        <Button variant="outline" size="xs" onClick={() => onNavigateTab('accounts')}>
                          管理号池与进程
                        </Button>
                      </div>
                    </div>
                  ) : (
                    workerProcessGroups.map((wp) => {
                      const pidFilterKey = `pid:${wp.pid}`
                      const isPidFiltered = selectedAccountOrKeyFilter === pidFilterKey
                      const boundSessions = Array.from(wp.sessions.values())

                      return (
                        <div
                          key={wp.pid}
                          onClick={() =>
                            setSelectedAccountOrKeyFilter((prev) =>
                              prev === pidFilterKey ? '' : pidFilterKey
                            )
                          }
                          className={`p-3.5 flex flex-col gap-2.5 transition-colors cursor-pointer ${
                            isPidFiltered ? 'bg-muted/40' : 'hover:bg-muted/20'
                          }`}
                        >
                          {/* Process Row Header */}
                          <div className="flex items-center justify-between gap-2">
                            <div className="flex items-center gap-2 min-w-0">
                              <span className="inline-flex size-5 shrink-0 items-center justify-center rounded-md bg-muted text-foreground">
                                <Cpu className="size-3.5 text-sky-500" />
                              </span>
                              <span className="font-mono text-xs font-bold text-foreground tabular-nums shrink-0">
                                PID {wp.pid}
                              </span>
                              <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground truncate">
                                {wp.workerId}
                              </span>
                              <span className="text-xs text-muted-foreground truncate" translate="no">
                                · {wp.accountName}
                              </span>
                            </div>

                            <Badge variant={wp.stateVariant}>{wp.stateLabel}</Badge>
                          </div>

                          {/* Bound Sessions under this Worker Process */}
                          {boundSessions.length === 0 ? (
                            <div className="ps-7 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                              <span>暂未绑定会话 · 预热待命进程</span>
                              {wp.lastActiveAt && (
                                <span className="tabular-nums shrink-0">
                                  {formatTimeHMS(wp.lastActiveAt)}
                                </span>
                              )}
                            </div>
                          ) : (
                            <div className="ps-2 flex flex-col gap-1.5">
                              {boundSessions.map((bs) => {
                                const sessFilterKey = `sess:${bs.clientSessionId}`
                                const isSessSelected =
                                  selectedAccountOrKeyFilter === sessFilterKey ||
                                  (Boolean(bs.latestRequestId) &&
                                    reqTelemetry.activeLog?.request_id === bs.latestRequestId)
                                return (
                                  <div
                                    key={bs.sessionKey}
                                    onClick={(e) => {
                                      e.stopPropagation()
                                      setSelectedAccountOrKeyFilter((prev) =>
                                        prev === sessFilterKey ? '' : sessFilterKey
                                      )
                                      if (bs.latestRequestId) {
                                        onSelectAuditLogId(
                                          bs.latestRequestId === auditLogs[0]?.request_id
                                            ? ''
                                            : bs.latestRequestId
                                        )
                                      }
                                    }}
                                    className={`rounded-xl border px-2.5 py-2 flex flex-col gap-1 transition-colors cursor-pointer ${
                                      isSessSelected
                                        ? 'border-border bg-muted/50 shadow-2xs'
                                        : 'border-border/50 bg-muted/15 hover:bg-muted/35'
                                    }`}
                                  >
                                    <div className="flex items-center justify-between gap-2 min-w-0">
                                      <div className="flex items-center gap-1.5 min-w-0">
                                        <span
                                          className={`size-1.5 shrink-0 rounded-full ${
                                            bs.failures > 0 && bs.successes === 0
                                              ? 'bg-destructive'
                                              : 'bg-emerald-500'
                                          }`}
                                        />
                                        {bs.sessionTitle ? (
                                          <span
                                            title={bs.sessionTitle}
                                            className="text-xs font-semibold text-foreground truncate"
                                            translate="no"
                                          >
                                            {bs.sessionTitle}
                                          </span>
                                        ) : (
                                          <span className="text-xs font-semibold text-foreground truncate">
                                            {`会话 #${bs.shortId}`}
                                          </span>
                                        )}
                                      </div>

                                      <span
                                        title={`绑定会话 ID: ${bs.clientSessionId}`}
                                        className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground tabular-nums shrink-0"
                                      >
                                        #{bs.shortId}
                                      </span>
                                    </div>

                                    <div className="ps-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground tabular-nums">
                                      <span className="font-mono text-foreground/85" translate="no">
                                        {bs.modelName || '—'}
                                      </span>
                                      <span>·</span>
                                      <span>{`${bs.attempts} 轮请求`}</span>
                                      {bs.failures > 0 && (
                                        <>
                                          <span>·</span>
                                          <span className="text-destructive">{`${bs.failures} 失败`}</span>
                                        </>
                                      )}
                                      {bs.lastAt && (
                                        <>
                                          <span>·</span>
                                          <span>{`最近 ${formatWeekdayHM(bs.lastAt)}`}</span>
                                        </>
                                      )}
                                    </div>
                                  </div>
                                )
                              })}
                            </div>
                          )}
                        </div>
                      )
                    })
                  )}
                </div>

                <div className="flex justify-end pr-1">
                  <button
                    type="button"
                    onClick={() => onNavigateTab('accounts')}
                    className="text-xs text-muted-foreground transition-colors hover:text-foreground cursor-pointer"
                  >
                    管理号池与进程
                  </button>
                </div>
              </div>
            </div>
          )
        })()}
      </FramePanel>
    </Frame>
  )
})
