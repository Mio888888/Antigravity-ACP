import React from 'react'
import { AuditLogRecord } from '../api'
import { formatCompact, formatProtocolDisplay } from '../lib/format'
import {
  computeRequestContextUsage,
  formatPromptModeShort,
  resolveSessionLogs,
} from './OverviewBoard'
import type {
  ContextModuleSelection,
  NormalizedContextEntry,
  RequestContextCategory,
} from './OverviewBoard'
import {
  Badge,
  Button,
  CodeBlock,
  DialogModal,
  ProgressBar,
} from './reui/primitives'

export type TreemapDetailKey =
  | 'system_prompt'
  | 'protocols'
  | 'prompt_input'
  | 'completion_tools'
  | 'latest_turn'
  | 'remaining_capacity'

interface OverviewDetailModalProps {
  activeModal: TreemapDetailKey | null
  activeModule?: ContextModuleSelection | null
  onClose: () => void
  selectedAuditLog: AuditLogRecord | undefined
  onSelectAuditLogId: (id: string) => void
  auditLogs: AuditLogRecord[]
  onNavigateTab: (tab: 'overview' | 'accounts' | 'clients' | 'keys') => void
}

const CATEGORY_META: Record<
  Exclude<RequestContextCategory, 'all'> | 'remaining_capacity',
  { label: string; badgeVariant: 'info-light' | 'success-light' | 'warning-light' | 'outline' }
> = {
  system_prompt: { label: '系统提示词', badgeVariant: 'info-light' },
  tools: { label: '工具', badgeVariant: 'info-light' },
  skills: { label: '技能', badgeVariant: 'outline' },
  files: { label: '文件', badgeVariant: 'success-light' },
  tool_results: { label: '工具结果', badgeVariant: 'warning-light' },
  conversation: { label: '会话内容', badgeVariant: 'info-light' },
  remaining_capacity: { label: '剩余空间', badgeVariant: 'outline' },
}

const MODAL_META: Record<
  TreemapDetailKey,
  { title: string; subtitle: string; badge: string }
> = {
  system_prompt: {
    title: '系统提示词详情',
    subtitle: '查看当前请求生效的系统提示词内容',
    badge: '系统提示词',
  },
  protocols: {
    title: '客户端工具定义详情',
    subtitle: '查看当前请求挂载的客户端工具定义与 Token 占用',
    badge: '工具',
  },
  prompt_input: {
    title: '文件上下文详情',
    subtitle: '查看当前请求上下文中读取与引用的文件内容',
    badge: '文件',
  },
  completion_tools: {
    title: '工具调用返回结果详情',
    subtitle: '查看当前请求上下文中的工具执行输出内容',
    badge: '工具结果',
  },
  latest_turn: {
    title: '请求与响应详情',
    subtitle: '查看当前选中请求的协议、Token 明细、系统指令、用户输入、模型思考与输出全文',
    badge: '请求与响应',
  },
  remaining_capacity: {
    title: '剩余空间详情',
    subtitle: '查看当前模型上下文窗口剩余可用容量与预计可容纳请求数',
    badge: '剩余空间',
  },
}

const SKILL_SECTION_MARKERS = [
  'Available skills',
  'Project instructions',
  'Guide: AGENTS.md',
  '<!-- BEGIN:reui-agent-rules -->',
]

function splitSystemPromptAndSkills(rawSystemPrompt?: string): {
  mainPrompt: string
  skillsSection: string
} {
  const text = (rawSystemPrompt || '').trim()
  if (!text) {
    return { mainPrompt: '', skillsSection: '' }
  }
  for (const marker of SKILL_SECTION_MARKERS) {
    const idx = text.indexOf(marker)
    if (idx >= 0) {
      return {
        mainPrompt: text.slice(0, idx).trim(),
        skillsSection: text.slice(idx).trim(),
      }
    }
  }
  return { mainPrompt: text, skillsSection: '' }
}

function extractMcpToolNamesFromSystemPrompt(rawSystemPrompt?: string): string[] {
  const text = rawSystemPrompt || ''
  const mcpMatch = text.match(/<mcp_servers>([\s\S]*?)<\/mcp_servers>/i)
  if (!mcpMatch?.[1]) return []
  const lines = mcpMatch[1]
    .split('\n')
    .map((l) => l.trim())
    .filter(
      (l) =>
        l &&
        !l.startsWith('#') &&
        !l.startsWith('Each MCP') &&
        !l.startsWith('Eagerly') &&
        !l.startsWith('For lazily') &&
        !l.startsWith('The following') &&
        !l.startsWith('```') &&
        l !== 'Eager:' &&
        l !== 'Lazy:' &&
        /^[a-zA-Z0-9_-]+$/.test(l)
    )
  return Array.from(new Set(lines))
}

function resolveSingleItemOwnContent(
  item: NormalizedContextEntry,
  currentLog: AuditLogRecord | undefined,
  sessionLogs: AuditLogRecord[],
  recordedPromptMode: string
): { code: string; language: string } {
  if (item.content && item.content.trim()) {
    return {
      code: item.content.trim(),
      language:
        item.category === 'tools' || item.tag === 'result' ? 'json' : 'markdown',
    }
  }

  const { mainPrompt, skillsSection } = splitSystemPromptAndSkills(
    currentLog?.system_prompt
  )

  if (item.category === 'system_prompt') {
    if (item.title.includes('网关系统') || item.title.includes('思考规则')) {
      return {
        code: '（网关未在审计记录中保存该模块的原文）',
        language: 'markdown',
      }
    }
    return {
      code:
        mainPrompt ||
        (recordedPromptMode === 'pure_passthrough'
          ? '（本轮请求未附带额外系统提示词）'
          : recordedPromptMode
            ? `（当前策略: ${recordedPromptMode}）`
            : '（网关未记录本轮请求的系统提示词原文）'),
      language: 'markdown',
    }
  }

  if (item.category === 'skills') {
    if (skillsSection) {
      return { code: skillsSection, language: 'markdown' }
    }
    return {
      code: '（当前请求未单独附带 AGENTS.md 或技能列表）',
      language: 'markdown',
    }
  }

  if (item.category === 'tools') {
    if (item.title.startsWith('另外 ')) {
      const mcpTools = extractMcpToolNamesFromSystemPrompt(
        currentLog?.system_prompt
      )
      if (mcpTools.length > 0) {
        const excluded = new Set<string>()
        if (currentLog?.context_json) {
          try {
            const snap = JSON.parse(currentLog.context_json)
            if (Array.isArray(snap?.items)) {
              for (const it of snap.items) {
                if (
                  it?.category === 'tools' &&
                  typeof it.title === 'string' &&
                  !it.title.startsWith('另外 ')
                ) {
                  const t = it.title.toLowerCase()
                  excluded.add(t)
                  if (t === 'exec' || t === 'exec_command') {
                    excluded.add('run_command')
                  }
                }
              }
            }
          } catch {
            // ignore JSON parse error
          }
        }
        const filteredTools = mcpTools.filter(
          (name) => !excluded.has(name.toLowerCase())
        )
        const displayTools = filteredTools.length > 0 ? filteredTools : mcpTools
        const listText = displayTools
          .map((name, idx) => `${idx + 1}. ${name} (mcp_client_tools_${name})`)
          .join('\n')
        return {
          code: `${item.title} (${displayTools.length} 项):\n\n${listText}`,
          language: 'markdown',
        }
      }
      return {
        code: `${item.title}\n类型: ${item.subtitle || '客户端 MCP 工具定义'}\n占用: ${item.tokens} token`,
        language: 'markdown',
      }
    }

    const targetNames = new Set<string>([
      item.title.toLowerCase(),
      (item.subtitle || '').toLowerCase(),
      `mcp_client_tools_${item.title.toLowerCase()}`,
    ])
    if (item.title === 'exec') {
      targetNames.add('run_command')
      targetNames.add('mcp_client_tools_run_command')
      targetNames.add('exec_command')
    }

    const matchedCalls: string[] = []
    for (const l of sessionLogs) {
      if (!l.tool_calls_json) continue
      const lowerTC = l.tool_calls_json.toLowerCase()
      let hit = false
      for (const tn of targetNames) {
        if (tn && lowerTC.includes(tn)) {
          hit = true
          break
        }
      }
      if (hit) {
        matchedCalls.push(l.tool_calls_json.trim())
        if (matchedCalls.length >= 3) break
      }
    }

    const header = [
      `工具名称: ${item.title}`,
      item.subtitle && item.subtitle !== item.title
        ? `完整标识: ${item.subtitle}`
        : null,
      `定义占用: ${item.tokens} token`,
    ]
      .filter(Boolean)
      .join('\n')

    if (matchedCalls.length > 0) {
      return {
        code: `${header}\n\n本会话近期调用记录:\n${matchedCalls.join('\n\n')}`,
        language: 'json',
      }
    }
    return { code: header, language: 'markdown' }
  }

  if (item.category === 'files') {
    if (item.tag === 'image' || item.title === '图片') {
      return {
        code: `当前上下文中包含 ${item.count || 1} 张图片输入，合计占用约 ${item.tokens} token。`,
        language: 'markdown',
      }
    }
    const fileName = item.title.trim()
    const matchedBlocks: string[] = []
    for (const l of sessionLogs) {
      const pText = (l.prompt_text || '').trim()
      if (pText && pText.includes(fileName)) {
        matchedBlocks.push(pText)
        if (matchedBlocks.length >= 3) break
      }
    }
    if (matchedBlocks.length > 0) {
      return {
        code: matchedBlocks.join('\n\n---\n\n'),
        language: 'markdown',
      }
    }
    return {
      code: `文件名: ${item.title}\n路径: ${item.subtitle || '/'}\n上下文占用: ${item.tokens} token${item.count && item.count > 1 ? ` (读取 ${item.count} 次)` : ''}`,
      language: 'markdown',
    }
  }

  if (item.category === 'tool_results') {
    const toolKey = item.title.toLowerCase()
    const matchedOutputs: string[] = []
    for (const l of sessionLogs) {
      const pText = (l.prompt_text || '').trim()
      if (!pText) continue
      const isToolResult =
        pText.startsWith('[tool_result:') ||
        pText.includes('"command_result"') ||
        pText.includes('"ssh_outcome"') ||
        pText.includes('[TOOL]:')
      if (!isToolResult) continue
      if (
        toolKey.includes('exec') ||
        toolKey === 'tool_output' ||
        pText.toLowerCase().includes(toolKey) ||
        (l.tool_calls_json || '').toLowerCase().includes(toolKey)
      ) {
        matchedOutputs.push(pText)
        if (matchedOutputs.length >= 4) break
      }
    }
    if (matchedOutputs.length > 0) {
      return {
        code: matchedOutputs.join('\n\n---\n\n'),
        language: 'json',
      }
    }
    return {
      code: `工具结果模块: ${item.title}\n调用次数: ${item.count || 1} 次\n上下文占用: ${item.tokens} token`,
      language: 'markdown',
    }
  }

  // conversation
  if (item.title.startsWith('更早的 ')) {
    const chronological = [...sessionLogs].reverse()
    const earlierLogs = chronological.slice(0, Math.max(1, chronological.length - 4))
    const shownLogs = earlierLogs.slice(-10)
    const positionOffset = earlierLogs.length - shownLogs.length
    const lines: string[] = []
    shownLogs.forEach((l, idx) => {
      const snippet = (l.prompt_text || l.response_text || '').trim()
      if (snippet) {
        lines.push(
          `### 请求 #${positionOffset + idx + 1} (${l.request_id.slice(0, 12)} · ${l.prompt_tokens} token)\n${snippet.slice(0, 600)}${snippet.length > 600 ? '\n...' : ''}`
        )
      }
    })
    if (lines.length > 0) {
      return { code: lines.join('\n\n'), language: 'markdown' }
    }
  }

  const subPrefix = (item.subtitle || '').replace(/\.\.\.$/, '').trim()
  if (subPrefix) {
    for (const l of sessionLogs) {
      if (l.tool_calls_json && l.tool_calls_json.includes(subPrefix.slice(0, 18))) {
        return { code: l.tool_calls_json, language: 'json' }
      }
      if (l.prompt_text && l.prompt_text.includes(subPrefix.slice(0, 18))) {
        return { code: l.prompt_text, language: 'markdown' }
      }
      if (l.response_text && l.response_text.includes(subPrefix.slice(0, 18))) {
        return { code: l.response_text, language: 'markdown' }
      }
    }
  }

  const turnMatch = item.title.match(/第\s*(\d+)\s*轮/)
  if (turnMatch) {
    const turnNum = Number(turnMatch[1])
    const chronological = [...sessionLogs].reverse()
    const targetLog =
      chronological[turnNum - 1] ||
      chronological[Math.max(0, chronological.length - 1)]
    if (targetLog) {
      const parts = [
        targetLog.prompt_text?.trim()
          ? `[输入]\n${targetLog.prompt_text.trim()}`
          : '',
        targetLog.response_text?.trim()
          ? `[输出]\n${targetLog.response_text.trim()}`
          : '',
        targetLog.tool_calls_json?.trim()
          ? `[工具调用]\n${targetLog.tool_calls_json.trim()}`
          : '',
      ].filter(Boolean)
      if (parts.length > 0) {
        return { code: parts.join('\n\n'), language: 'markdown' }
      }
    }
  }

  return {
    code:
      item.subtitle?.trim() ||
      currentLog?.prompt_text?.trim() ||
      `轮次: ${item.title} (${item.tokens} token)`,
    language: 'markdown',
  }
}

export const OverviewDetailModal: React.FC<OverviewDetailModalProps> = ({
  activeModal,
  activeModule,
  onClose,
  selectedAuditLog,
  onSelectAuditLogId,
  auditLogs,
  onNavigateTab,
}) => {
  const currentLog = selectedAuditLog || auditLogs[0]
  const sessionLogs = React.useMemo(
    () => resolveSessionLogs(currentLog, auditLogs),
    [auditLogs, currentLog]
  )
  const usage = React.useMemo(() => computeRequestContextUsage(currentLog), [currentLog])

  if (!activeModal) return null

  const meta = MODAL_META[activeModal]
  const recordedPromptMode = usage.parsedSnap?.prompt_mode || ''
  const recordedKeyName = usage.parsedSnap?.key_name || ''

  const cap = Math.max(1, usage.contextCapacity)
  const modulePct = activeModule
    ? activeModule.tokens <= 0
      ? 0
      : Math.max(1, Math.round((activeModule.tokens / cap) * 100))
    : 0
  const activeCatMeta = activeModule ? CATEGORY_META[activeModule.category] : null

  const dialogTitle = activeModule ? activeModule.title : meta.title
  const dialogDescription = activeModule
    ? `${activeCatMeta?.label || '上下文模块'} · ${formatCompact(activeModule.tokens)} token (占窗口 ${modulePct}%)`
    : meta.subtitle
  const dialogBadge = activeModule ? activeCatMeta?.label || meta.badge : meta.badge

  const renderTurnSelector = () => {
    if (auditLogs.length === 0) return null
    return (
      <div className="flex flex-col gap-1.5 rounded-lg border border-border bg-muted/30 p-2.5">
        <div className="flex items-center justify-between text-xs">
          <span className="font-semibold text-foreground">{`切换检视轮次 (最近 ${Math.min(auditLogs.length, 12)} 笔请求)`}</span>
          <span className="font-mono text-muted-foreground" translate="no">
            {`#${currentLog?.request_id?.slice(0, 14) || '—'}`}
          </span>
        </div>
        <div className="flex flex-wrap gap-1.5">
          {auditLogs.slice(0, 12).map((log, idx) => {
            const isSelected = currentLog?.request_id === log.request_id
            const turnNum = Math.max(1, auditLogs.length - idx)
            return (
              <button
                key={log.request_id}
                type="button"
                onClick={() => onSelectAuditLogId(log.request_id)}
                className={`inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs font-mono transition-colors ${
                  isSelected
                    ? 'border-foreground bg-foreground text-background font-semibold'
                    : 'border-border bg-card text-foreground hover:bg-accent'
                }`}
              >
                <span>#{turnNum}</span>
                <span translate="no">{log.model_name || '—'}</span>
                <span className="opacity-75">({formatCompact(log.total_tokens)} tok)</span>
              </button>
            )
          })}
        </div>
      </div>
    )
  }

  const renderLogPayloadBlocks = (log?: AuditLogRecord) => {
    if (!log) {
      return (
        <div className="rounded-lg border border-border bg-muted/25 p-4 text-center text-xs text-muted-foreground">
          暂无网关请求审计记录。通过客户端发起一次请求后，此处将展示真实的系统指令、用户输入、思考过程与模型输出。
        </div>
      )
    }
    const systemPromptTitle = [
      recordedPromptMode ? `策略 ${formatPromptModeShort(recordedPromptMode)}` : '',
      recordedKeyName ? `Key:${recordedKeyName}` : '',
    ]
      .filter(Boolean)
      .join(' · ')
    return (
      <div className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 text-xs">
          <div className="rounded-lg border border-border bg-muted/20 p-2.5">
            <div className="text-muted-foreground">协议与模型</div>
            <div className="mt-0.5 font-semibold text-foreground truncate">{formatProtocolDisplay(log.protocol)}</div>
            <div className="font-mono text-muted-foreground truncate" translate="no">
              {log.model_name || '—'} {log.thinking_level ? `(thinking: ${log.thinking_level})` : ''}
            </div>
          </div>
          <div className="rounded-lg border border-border bg-muted/20 p-2.5">
            <div className="text-muted-foreground">调度账户与进程</div>
            <div className="mt-0.5 font-semibold text-foreground">
              <span>账户 </span>
              <span translate="no">{log.account_id || '—'}</span>
            </div>
            <div className="font-mono text-muted-foreground">
              {`PID ${log.worker_pid || '—'} · 状态 ${log.status_code}`}
            </div>
          </div>
          <div className="rounded-lg border border-border bg-muted/20 p-2.5">
            <div className="text-muted-foreground">Token 计量 (入 / 出 / 总)</div>
            <div className="mt-0.5 font-mono font-semibold text-foreground tabular-nums">
              {`${log.prompt_tokens} / ${log.completion_tokens} / ${log.total_tokens}`}
            </div>
            <div className="text-muted-foreground">{`请求 ID: ${log.request_id}`}</div>
          </div>
          <div className="rounded-lg border border-border bg-muted/20 p-2.5">
            <div className="text-muted-foreground">端到端耗时与时间</div>
            <div className="mt-0.5 font-mono font-semibold text-foreground tabular-nums">
              {log.latency_ms} ms
            </div>
            <div className="font-mono text-muted-foreground">
              {new Date(log.created_at).toLocaleString()}
            </div>
          </div>
        </div>

        {log.error_msg && (
          <CodeBlock title="异常错误信息 (Error Message)" language="text" code={log.error_msg} />
        )}

        <CodeBlock
          title={
            systemPromptTitle
              ? `系统提示词 (System Prompt · ${systemPromptTitle})`
              : '系统提示词 (System Prompt)'
          }
          translateTitle={!recordedKeyName}
          language="markdown"
          code={
            log.system_prompt?.trim() ||
            (recordedPromptMode === 'pure_passthrough'
              ? '（本轮请求未附带额外系统提示词，采用 pure_passthrough 纯透传模式）'
              : '（网关未记录本轮请求的系统提示词原文）')
          }
        />

        <CodeBlock
          title={`用户输入内容 (User Prompt · ${log.prompt_tokens} tokens)`}
          language="markdown"
          code={
            log.prompt_text?.trim() ||
            '（历史记录产生于文本审计字段升级前，新发起的请求将完整记录用户输入文本）'
          }
        />

        {log.thinking_text?.trim() && (
          <CodeBlock
            title={`模型思考过程 (Reasoning / Thinking Trace${log.thinking_level ? ` · ${log.thinking_level}` : ''})`}
            language="markdown"
            code={log.thinking_text}
          />
        )}

        <CodeBlock
          title={`模型生成输出 (Assistant Response · ${log.completion_tokens} tokens)`}
          language="markdown"
          code={
            log.response_text?.trim() ||
            '（历史记录产生于文本审计字段升级前，新发起的请求将完整记录模型输出文本）'
          }
        />

        {log.tool_calls_json?.trim() && (
          <CodeBlock
            title="工具调用载荷 (MCP / Client Tool Call)"
            language="json"
            code={log.tool_calls_json}
          />
        )}
      </div>
    )
  }

  const renderRemainingCapacity = () => (
    <div className="flex flex-col gap-3.5 text-xs">
      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
        <div className="rounded-lg border border-border bg-muted/20 p-3">
          <div className="text-muted-foreground">当前可用上下文剩余空间</div>
          <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
            {`${formatCompact(usage.remainingTokens)} token`}
          </div>
          <div className="mt-0.5 text-muted-foreground">
            {`总窗口容量: ${formatCompact(usage.contextCapacity)} token`}
          </div>
        </div>
        <div className="rounded-lg border border-border bg-muted/20 p-3">
          <div className="text-muted-foreground">已用上下文占比</div>
          <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
            {usage.usedPct}%
          </div>
          <div className="mt-0.5 text-muted-foreground">
            {`已消耗: ${formatCompact(usage.usedContextTokens)} token`}
          </div>
        </div>
        <div className="rounded-lg border border-border bg-muted/20 p-3">
          <div className="text-muted-foreground">预计还可容纳同规模请求</div>
          <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
            {usage.estRemainingReqs === null ? '—' : `约 ${usage.estRemainingReqs} 个请求`}
          </div>
          <div className="mt-0.5 text-muted-foreground">按单次平均消耗自动动态测算</div>
        </div>
      </div>

      <div className="rounded-lg border border-border p-3 flex flex-col gap-2">
        <div className="flex items-center justify-between">
          <span className="font-semibold text-foreground">剩余可用空间占比</span>
          <span className="font-mono text-muted-foreground tabular-nums">
            {`${usage.remainingTokens.toLocaleString()} / ${usage.contextCapacity.toLocaleString()} token`}
          </span>
        </div>
        <ProgressBar
          value={usage.remainingTokens}
          max={usage.contextCapacity}
          tone="success"
          ariaLabel="上下文剩余可用空间占比"
        />
      </div>
    </div>
  )

  const renderModule = (module: ContextModuleSelection) => (
    <div className="flex flex-col gap-3.5 text-xs">
      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
        <div className="rounded-lg border border-border bg-muted/20 p-3">
          <div className="text-muted-foreground">模块名称与分类</div>
          <div className="mt-1 flex items-center gap-2 min-w-0">
            <span className="font-mono text-sm font-bold text-foreground truncate">
              {module.title}
            </span>
            {activeCatMeta && (
              <Badge variant={activeCatMeta.badgeVariant}>{activeCatMeta.label}</Badge>
            )}
          </div>
          {module.subtitle && module.subtitle !== module.title && (
            <div className="mt-0.5 font-mono text-muted-foreground truncate" translate="no">
              {module.subtitle}
            </div>
          )}
        </div>
        <div className="rounded-lg border border-border bg-muted/20 p-3">
          <div className="text-muted-foreground">模块 Token 占用</div>
          <div className="mt-1 font-mono text-xl font-bold text-foreground tabular-nums">
            {`${formatCompact(module.tokens)} token`}
          </div>
          <div className="mt-0.5 font-mono text-muted-foreground tabular-nums">
            {`精确计量: ${module.tokens.toLocaleString()} token`}
          </div>
        </div>
        <div className="rounded-lg border border-border bg-muted/20 p-3">
          <div className="text-muted-foreground">占上下文窗口比例</div>
          <div className="mt-1 font-mono text-xl font-bold text-foreground tabular-nums">
            {`${modulePct}%`}
          </div>
          <div className="mt-0.5 text-muted-foreground tabular-nums">
            {module.count && module.count > 1
              ? `包含 ${module.count} 个子项`
              : `当前窗口容量 ${formatCompact(usage.contextCapacity)}`}
          </div>
        </div>
      </div>

      {module.items.length === 0 ? (
        <div className="rounded-lg border border-border bg-muted/20 p-6 text-center text-xs text-muted-foreground">
          {`当前请求中「${module.title}」模块暂无内容（0 token）。`}
        </div>
      ) : (
        <div className="flex flex-col gap-3">
          {module.items.map((item: NormalizedContextEntry) => {
            const resolved = resolveSingleItemOwnContent(
              item,
              currentLog,
              sessionLogs,
              recordedPromptMode
            )
            const showSubtitle = Boolean(item.subtitle && item.subtitle !== item.title)
            return (
              <div key={item.id} className="flex flex-col gap-1.5">
                {showSubtitle && (
                  <div className="font-mono text-xs text-muted-foreground truncate" translate="no">
                    {item.subtitle}
                  </div>
                )}
                <CodeBlock
                  title={`${item.title} · ${formatCompact(item.tokens)} token`}
                  language={resolved.language}
                  code={resolved.code}
                />
              </div>
            )
          })}
        </div>
      )}
    </div>
  )

  return (
    <DialogModal
      open={Boolean(activeModal)}
      onClose={onClose}
      title={dialogTitle}
      description={dialogDescription}
      maxWidthClass="max-w-4xl"
      footer={
        <>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant={activeCatMeta?.badgeVariant || 'outline'}>{dialogBadge}</Badge>
            <span>
              {activeModule
                ? `当前仅展示「${activeModule.title}」模块自身内容`
                : meta.subtitle}
            </span>
          </div>
          <div className="flex items-center gap-2">
            {activeModule?.category === 'system_prompt' && (
              <Button
                size="xs"
                variant="outline"
                onClick={() => {
                  onClose()
                  onNavigateTab('keys')
                }}
              >
                前往 API Key 策略配置
              </Button>
            )}
            <Button size="xs" onClick={onClose}>
              关闭详情
            </Button>
          </div>
        </>
      }
    >
      {activeModule ? (
        activeModule.category === 'remaining_capacity' ? (
          renderRemainingCapacity()
        ) : (
          renderModule(activeModule)
        )
      ) : (
        <div className="flex flex-col gap-3.5 text-xs">
          {renderTurnSelector()}
          {renderLogPayloadBlocks(currentLog)}
        </div>
      )}
    </DialogModal>
  )
}
