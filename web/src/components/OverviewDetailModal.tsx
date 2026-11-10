import React from 'react'
import {
  ACPModelInfo,
  AccountSnapshot,
  AuditLogRecord,
  PromptPolicyConfig,
  SkillRecord,
} from '../api'
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
  | 'routed_account'
  | 'egress_pool'
  | 'completion_tools'
  | 'latest_turn'
  | 'calls_24h'
  | 'audit_history'
  | 'remaining_capacity'

interface OverviewDetailModalProps {
  activeModal: TreemapDetailKey | null
  activeModule?: ContextModuleSelection | null
  onClose: () => void
  onSelectModal?: (key: TreemapDetailKey) => void
  selectedAuditLog: AuditLogRecord | undefined
  onSelectAuditLogId: (id: string) => void
  auditLogs: AuditLogRecord[]
  accounts: AccountSnapshot[]
  skills: SkillRecord[]
  policy: PromptPolicyConfig
  discoveredModels: ACPModelInfo[]
  gatewayBaseUrl: string
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
    busyWorkers: number
    spawnedWorkers: number
    maxWorkers: number
  }
  overviewTelemetry: {
    routedId: string
    routedName: string
    routedProxy: string
    routedEgress: string
    routedWeight: number
    latestProtocol: string
    latestModel: string
    latestLatencyMs: number
    gwMs: number
    netMs: number
    providerMs: number
    ttfbMs: number
    streamMs: number
    totalReqs: number
    successReqs: number
    errorReqs: number
    successRatePct: number
    switchCount: number
    totalTokens: number
    promptTokens: number
    completionTokens: number
    sysPromptTokens: number
    toolsTokens?: number
    skillsTokens?: number
    filesTokens?: number
    toolResultsTokens?: number
    conversationTokens?: number
    contextInputTokens: number
    contextCapacity: number
    usedContextTokens: number
    usedPct: number
    remainingTokens: number
    estRemainingReqs: number
    effectivePromptMode?: string
    effectiveSessionMode?: string
    effectiveDisableBuiltin?: boolean
    effectivePrependPrompt?: string
    effectiveKeyName?: string
  }
  onNavigateTab: (tab: 'overview' | 'accounts' | 'clients' | 'keys') => void
  onProbeAccount: (id: string) => void
  onRefreshQuota: (id: string) => void
}

function fmtCompact(num: number): string {
  if (!num || num <= 0) return '0'
  if (num >= 1_000_000) return `${(num / 1_000_000).toFixed(2)}M`
  if (num >= 1_000) return `${(num / 1_000).toFixed(1)}K`
  return String(Math.round(num))
}

function fmtProtocol(proto?: string): string {
  if (!proto) return 'OpenAI 官方 (Responses)'
  const p = proto.toLowerCase()
  if (p.includes('responses')) return 'OpenAI 官方 (Responses)'
  if (p.includes('openai')) return 'OpenAI 兼容 (Chat)'
  if (p.includes('anthropic') || p.includes('claude')) return 'Claude 协议 (Messages)'
  return proto
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
  routed_account: {
    title: '当前路由隔离账户详情',
    subtitle: '查看承接流量的隔离账户容器状态、出口通道、周期模型额度剩余百分比与实时子进程并发',
    badge: '隔离账户',
  },
  egress_pool: {
    title: '多账户出口池与网络隔离拓扑',
    subtitle: '查看所有隔离账户节点的代理模式 (Direct / SOCKS5 / Bind IP / NetNS)、出口 IP、权重与健康状态',
    badge: '出口池',
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
  calls_24h: {
    title: '24h 调用与多维流量统计',
    subtitle: '近 24 小时网关总调用次数、有效成功率、账户间改道次数及按模型/账户的调用分布',
    badge: '24h 统计',
  },
  audit_history: {
    title: '历史审计记录流水表',
    subtitle: '点击任意一条历史调用记录即可检视该次请求的完整输入提示词、模型思考链与响应全文',
    badge: '历史审计',
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
  effectivePrependPrompt: string,
  effectivePromptMode: string,
  skills: SkillRecord[]
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
        code:
          effectivePrependPrompt.trim() ||
          '（当前启用网关思考标签与系统前置规则）',
        language: 'markdown',
      }
    }
    return {
      code:
        mainPrompt ||
        effectivePrependPrompt.trim() ||
        (effectivePromptMode === 'pure_passthrough'
          ? '（本轮请求未附带额外系统提示词）'
          : `（当前策略: ${effectivePromptMode}）`),
      language: 'markdown',
    }
  }

  if (item.category === 'skills') {
    if (skillsSection) {
      return { code: skillsSection, language: 'markdown' }
    }
    if (skills.length > 0) {
      const formatted = skills
        .map((sk) => `### ${sk.name} (${sk.category})\n${sk.content_json}`)
        .join('\n\n')
      return { code: formatted, language: 'markdown' }
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
    const lines: string[] = []
    earlierLogs.slice(-10).forEach((l, idx) => {
      const snippet = (l.prompt_text || l.response_text || '').trim()
      if (snippet) {
        lines.push(
          `### 请求 #${idx + 1} (${l.request_id.slice(0, 12)} · ${l.prompt_tokens} token)\n${snippet.slice(0, 600)}${snippet.length > 600 ? '\n...' : ''}`
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
  accounts,
  skills,
  policy,
  discoveredModels,
  gatewayBaseUrl,
  fleetUsageSummary,
  overviewTelemetry,
  onNavigateTab,
  onProbeAccount,
  onRefreshQuota,
}) => {
  const currentLog = selectedAuditLog || auditLogs[0]
  const sessionLogs = React.useMemo(() => {
    if (!currentLog) return auditLogs
    if (currentLog.session_id) {
      const matched = auditLogs.filter((l) => l.session_id === currentLog.session_id)
      if (matched.length > 0) return matched
    }
    return auditLogs
  }, [auditLogs, currentLog])

  if (!activeModal) return null

  const meta = MODAL_META[activeModal]
  const routedAccountSnap =
    accounts.find((a) => a.account.id === (currentLog?.account_id || overviewTelemetry.routedId)) ||
    accounts[0]

  const cap = Math.max(1, overviewTelemetry.contextCapacity)
  const modulePct = activeModule
    ? activeModule.tokens <= 0
      ? 0
      : Math.max(1, Math.round((activeModule.tokens / cap) * 100))
    : 0
  const activeCatMeta = activeModule ? CATEGORY_META[activeModule.category] : null

  const dialogTitle = activeModule ? activeModule.title : meta.title
  const dialogDescription = activeModule
    ? `${activeCatMeta?.label || '上下文模块'} · ${fmtCompact(activeModule.tokens)} token (占窗口 ${modulePct}%)${
        activeModule.subtitle && activeModule.subtitle !== activeModule.title
          ? ` · ${activeModule.subtitle}`
          : ''
      }`
    : meta.subtitle
  const dialogBadge = activeModule ? activeCatMeta?.label || meta.badge : meta.badge

  const renderTurnSelector = () => {
    if (auditLogs.length === 0) return null
    return (
      <div className="flex flex-col gap-1.5 rounded-lg border border-border bg-muted/30 p-2.5">
        <div className="flex items-center justify-between text-xs">
          <span className="font-semibold text-foreground">{`切换检视轮次 (最近 ${Math.min(auditLogs.length, 12)} 笔请求)`}</span>
          <span className="font-mono text-muted-foreground">
            {`当前: #${currentLog?.request_id?.slice(0, 14) || 'N/A'}`}
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
                <span>{log.model_name || 'model'}</span>
                <span className="opacity-75">({fmtCompact(log.total_tokens)} tok)</span>
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
    return (
      <div className="flex flex-col gap-3">
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 text-xs">
          <div className="rounded-lg border border-border bg-muted/20 p-2.5">
            <div className="text-muted-foreground">协议与模型</div>
            <div className="mt-0.5 font-semibold text-foreground truncate">{fmtProtocol(log.protocol)}</div>
            <div className="font-mono text-muted-foreground truncate">
              {log.model_name} {log.thinking_level ? `(thinking: ${log.thinking_level})` : ''}
            </div>
          </div>
          <div className="rounded-lg border border-border bg-muted/20 p-2.5">
            <div className="text-muted-foreground">调度账户与进程</div>
            <div className="mt-0.5 font-semibold text-foreground">{`账户 ${log.account_id || 'default'}`}</div>
            <div className="font-mono text-muted-foreground">
              {`PID ${log.worker_pid || '-'} · 状态 ${log.status_code}`}
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
          title={`系统提示词 (System Prompt · 策略 ${overviewTelemetry.effectivePromptMode || policy.mode}${overviewTelemetry.effectiveKeyName ? ` · Key:${overviewTelemetry.effectiveKeyName}` : ''})`}
          language="markdown"
          code={
            log.system_prompt?.trim() ||
            overviewTelemetry.effectivePrependPrompt?.trim() ||
            policy.global_prepend_prompt?.trim() ||
            (overviewTelemetry.effectivePromptMode === 'pure_passthrough'
              ? '（本轮请求未附带额外系统提示词，采用 pure_passthrough 纯透传模式）'
              : `（当前客户端密钥策略为 ${overviewTelemetry.effectivePromptMode || policy.mode}，由网关自动挂载 Antigravity 原生系统设定与项目规则）`)
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
            {(activeModule?.category === 'system_prompt' ||
              (!activeModule && activeModal === 'system_prompt')) && (
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
            {!activeModule && (activeModal === 'routed_account' || activeModal === 'egress_pool') && (
              <Button
                size="xs"
                variant="outline"
                onClick={() => {
                  onClose()
                  onNavigateTab('accounts')
                }}
              >
                前往多账户与额度页
              </Button>
            )}
            <Button size="xs" onClick={onClose}>
              关闭详情
            </Button>
          </div>
        </>
      }
    >
      {/* Focused Single-Module View when clicking any Context Window tile or breakdown row */}
      {activeModule ? (
        activeModule.category === 'remaining_capacity' ? (
          <div className="flex flex-col gap-3.5 text-xs">
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <div className="rounded-lg border border-border bg-muted/20 p-3">
                <div className="text-muted-foreground">当前可用上下文剩余空间</div>
                <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                  {fmtCompact(overviewTelemetry.remainingTokens)} token
                </div>
                <div className="mt-0.5 text-muted-foreground">
                  {`总窗口容量: ${fmtCompact(overviewTelemetry.contextCapacity)} token`}
                </div>
              </div>
              <div className="rounded-lg border border-border bg-muted/20 p-3">
                <div className="text-muted-foreground">已用上下文占比</div>
                <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                  {overviewTelemetry.usedPct}%
                </div>
                <div className="mt-0.5 text-muted-foreground">
                  {`已消耗: ${fmtCompact(overviewTelemetry.usedContextTokens)} token`}
                </div>
              </div>
              <div className="rounded-lg border border-border bg-muted/20 p-3">
                <div className="text-muted-foreground">预计还可容纳同规模请求</div>
                <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                  {`约 ${overviewTelemetry.estRemainingReqs} 个请求`}
                </div>
                <div className="mt-0.5 text-muted-foreground">
                  按单次平均消耗自动动态测算
                </div>
              </div>
            </div>

            <div className="rounded-lg border border-border p-3 flex flex-col gap-2">
              <div className="flex items-center justify-between">
                <span className="font-semibold text-foreground">剩余可用空间占比</span>
                <span className="font-mono text-muted-foreground tabular-nums">
                  {`${overviewTelemetry.remainingTokens.toLocaleString()} / ${overviewTelemetry.contextCapacity.toLocaleString()} token`}
                </span>
              </div>
              <ProgressBar
                value={overviewTelemetry.remainingTokens}
                max={overviewTelemetry.contextCapacity}
                tone="success"
                ariaLabel="上下文剩余可用空间占比"
              />
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-3.5 text-xs">
            <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
              <div className="rounded-lg border border-border bg-muted/20 p-3">
                <div className="text-muted-foreground">模块名称与分类</div>
                <div className="mt-1 flex items-center gap-2 min-w-0">
                  <span className="font-mono text-sm font-bold text-foreground truncate">
                    {activeModule.title}
                  </span>
                  {activeCatMeta && (
                    <Badge variant={activeCatMeta.badgeVariant}>{activeCatMeta.label}</Badge>
                  )}
                </div>
                {activeModule.subtitle && activeModule.subtitle !== activeModule.title && (
                  <div className="mt-0.5 font-mono text-muted-foreground truncate">
                    {activeModule.subtitle}
                  </div>
                )}
              </div>
              <div className="rounded-lg border border-border bg-muted/20 p-3">
                <div className="text-muted-foreground">模块 Token 占用</div>
                <div className="mt-1 font-mono text-xl font-bold text-foreground tabular-nums">
                  {`${fmtCompact(activeModule.tokens)} token`}
                </div>
                <div className="mt-0.5 font-mono text-muted-foreground tabular-nums">
                  {`精确计量: ${activeModule.tokens.toLocaleString()} token`}
                </div>
              </div>
              <div className="rounded-lg border border-border bg-muted/20 p-3">
                <div className="text-muted-foreground">占上下文窗口比例</div>
                <div className="mt-1 font-mono text-xl font-bold text-foreground tabular-nums">
                  {`${modulePct}%`}
                </div>
                <div className="mt-0.5 text-muted-foreground tabular-nums">
                  {activeModule.count && activeModule.count > 1
                    ? `包含 ${activeModule.count} 个子项`
                    : `当前窗口容量 ${fmtCompact(overviewTelemetry.contextCapacity)}`}
                </div>
              </div>
            </div>

            {activeModule.items.length === 0 ? (
              <div className="rounded-lg border border-border bg-muted/20 p-6 text-center text-xs text-muted-foreground">
                {`当前请求中「${activeModule.title}」模块暂无内容（0 token）。`}
              </div>
            ) : (
              <div className="flex flex-col gap-3">
                {activeModule.items.map((item) => {
                  const resolved = resolveSingleItemOwnContent(
                    item,
                    currentLog,
                    sessionLogs,
                    overviewTelemetry.effectivePrependPrompt ||
                      policy.global_prepend_prompt ||
                      '',
                    overviewTelemetry.effectivePromptMode || policy.mode,
                    skills
                  )
                  const blockTitle =
                    activeModule.items.length === 1
                      ? `${item.title}${
                          item.subtitle && item.subtitle !== item.title
                            ? ` (${item.subtitle})`
                            : ''
                        } · ${fmtCompact(item.tokens)} token`
                      : `${item.title}${
                          item.subtitle && item.subtitle !== item.title
                            ? ` (${item.subtitle})`
                            : ''
                        } · ${fmtCompact(item.tokens)} token`
                  return (
                    <CodeBlock
                      key={item.id}
                      title={blockTitle}
                      language={resolved.language}
                      code={resolved.code}
                    />
                  )
                })}
              </div>
            )}
          </div>
        )
      ) : (
        <>
          {/* 1. SYSTEM PROMPT DETAILS */}
          {activeModal === 'system_prompt' && (
            <div className="flex flex-col gap-3.5 text-xs">
              <CodeBlock
                title={`系统提示词 (${fmtCompact(overviewTelemetry.sysPromptTokens)} token)`}
                language="markdown"
                code={
                  splitSystemPromptAndSkills(currentLog?.system_prompt).mainPrompt ||
                  overviewTelemetry.effectivePrependPrompt?.trim() ||
                  policy.global_prepend_prompt?.trim() ||
                  '（本轮请求未附带额外系统提示词）'
                }
              />
            </div>
          )}

          {/* 2. THREE SESSION PROTOCOLS DETAILS */}
          {activeModal === 'protocols' && (
            <div className="flex flex-col gap-3.5 text-xs">
              <div className="grid grid-cols-1 gap-2.5 md:grid-cols-3">
                {[
                  {
                    title: 'OpenAI 官方协议 (Responses)',
                    endpoint: `${gatewayBaseUrl}/v1/responses`,
                    effortField: 'reasoning: { effort: "low" | "medium" | "high" }',
                    match: (p: string) => p.includes('responses'),
                  },
                  {
                    title: 'OpenAI 兼容协议 (Chat Completions)',
                    endpoint: `${gatewayBaseUrl}/v1/chat/completions`,
                    effortField: 'reasoning_effort: "low" | "medium" | "high"',
                    match: (p: string) =>
                      p === 'openai' || (p.includes('openai') && !p.includes('responses')),
                  },
                  {
                    title: 'Claude 协议 (Anthropic Messages)',
                    endpoint: `${gatewayBaseUrl}/v1/messages`,
                    effortField: 'thinking: { type: "enabled", budget_tokens: 16000 }',
                    match: (p: string) => p.includes('claude') || p.includes('anthropic'),
                  },
                ].map((proto) => {
                  const logsForProto = auditLogs.filter((l) =>
                    proto.match((l.protocol || '').toLowerCase())
                  )
                  const tokensForProto = logsForProto.reduce(
                    (acc, l) => acc + (l.total_tokens || 0),
                    0
                  )
                  return (
                    <div
                      key={proto.title}
                      className="rounded-lg border border-sky-500/30 bg-sky-500/8 p-3 flex flex-col justify-between gap-2"
                    >
                      <div>
                        <div className="font-semibold text-foreground">{proto.title}</div>
                        <div className="mt-1 font-mono text-muted-foreground break-all">
                          POST {proto.endpoint}
                        </div>
                      </div>
                      <div className="border-t border-border/60 pt-2">
                        <div className="text-muted-foreground">思考强度参数格式:</div>
                        <code className="font-mono text-foreground">{proto.effortField}</code>
                      </div>
                      <div className="flex items-center justify-between border-t border-border/60 pt-2 tabular-nums">
                        <span>{`近期调用: ${logsForProto.length} 次`}</span>
                        <span className="font-mono font-semibold text-foreground">
                          {`${fmtCompact(tokensForProto)} token`}
                        </span>
                      </div>
                    </div>
                  )
                })}
              </div>

              <div className="rounded-lg border border-border p-3 flex flex-col gap-2">
                <div className="flex items-center justify-between">
                  <span className="font-semibold text-foreground">
                    {`ACP 后端实时发现模型与支持的思考等级 (${discoveredModels.length} 个模型)`}
                  </span>
                  <Badge variant="success-light">GET /v1/models 实时同步</Badge>
                </div>
                <div className="divide-y divide-border">
                  {discoveredModels.map((m) => (
                    <div
                      key={m.modelId}
                      className="flex flex-wrap items-center justify-between gap-2 py-2"
                    >
                      <div className="flex items-center gap-2">
                        <span className="font-mono font-semibold text-foreground">{m.modelId}</span>
                        {m.name && m.name !== m.modelId && (
                          <span className="text-muted-foreground">({m.name})</span>
                        )}
                      </div>
                      <div className="flex items-center gap-1.5">
                        <span className="text-muted-foreground">思考等级:</span>
                        {(m.thinkingLevels && m.thinkingLevels.length > 0
                          ? m.thinkingLevels
                          : m.thinking_levels && m.thinking_levels.length > 0
                            ? m.thinking_levels
                            : m.modelId.toLowerCase().startsWith('gemini-')
                              ? ['low', 'high']
                              : ['low', 'medium', 'high']
                        ).map((lvl) => (
                          <Badge
                            key={lvl}
                            variant={
                              lvl === (m.defaultThinkingLevel || m.default_thinking_level || 'high')
                                ? 'info-light'
                                : 'outline'
                            }
                          >
                            {lvl}
                          </Badge>
                        ))}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* 3. CONTEXT INPUT (PROMPT) DETAILS */}
          {activeModal === 'prompt_input' && (
            <div className="flex flex-col gap-3.5 text-xs">
              {renderTurnSelector()}
              {renderLogPayloadBlocks(currentLog)}
            </div>
          )}

          {/* 4. ROUTED ACCOUNT DETAILS */}
          {activeModal === 'routed_account' && routedAccountSnap && (
            <div className="flex flex-col gap-3.5 text-xs">
              <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-4">
                <div className="rounded-lg border border-emerald-500/30 bg-emerald-500/10 p-3">
                  <div className="text-muted-foreground">隔离账户 ID / 名称</div>
                  <div className="mt-1 text-base font-bold text-foreground">
                    {routedAccountSnap.account.id} ({routedAccountSnap.account.name})
                  </div>
                  <div className="mt-0.5 font-mono text-muted-foreground">
                    {`状态: ${routedAccountSnap.account.status} · 权重 W=${routedAccountSnap.account.weight}`}
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">出口网络与代理 IP</div>
                  <div className="mt-1 font-mono font-bold text-foreground">
                    {routedAccountSnap.account.last_egress_ip || '本地直连'}
                  </div>
                  <div className="mt-0.5 font-mono text-muted-foreground truncate">
                    {`模式: ${routedAccountSnap.account.network_mode}`}
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">24h Token 消耗 / 调用</div>
                  <div className="mt-1 text-base font-bold text-foreground tabular-nums">
                    {`${fmtCompact(routedAccountSnap.usage?.tokens_24h || 0)} token`}
                  </div>
                  <div className="mt-0.5 text-muted-foreground tabular-nums">
                    {`今日成功 ${routedAccountSnap.usage?.today_success || 0} 次 · 失败 ${
                      routedAccountSnap.usage?.today_failed || 0
                    } 次`}
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">保活子进程槽位</div>
                  <div className="mt-1 text-base font-bold text-foreground tabular-nums">
                    {`${routedAccountSnap.busy_workers} 忙碌 / ${routedAccountSnap.worker_count} 已拉起 (上限 ${routedAccountSnap.account.max_workers})`}
                  </div>
                  <div className="mt-0.5 font-mono text-muted-foreground truncate">
                    {routedAccountSnap.gemini_home}
                  </div>
                </div>
              </div>

              <div className="rounded-lg border border-border p-3.5 flex flex-col gap-2.5">
                <div className="flex items-center justify-between">
                  <span className="font-semibold text-foreground">
                    模型周期额度剩余监控 (Quota Windows)
                  </span>
                  <div className="flex items-center gap-2">
                    <Button
                      size="xs"
                      variant="outline"
                      onClick={() => onProbeAccount(routedAccountSnap.account.id)}
                    >
                      检测出口 IP
                    </Button>
                    <Button
                      size="xs"
                      variant="outline"
                      onClick={() => onRefreshQuota(routedAccountSnap.account.id)}
                    >
                      刷新实时额度
                    </Button>
                  </div>
                </div>
                {(routedAccountSnap.usage?.quota_groups || []).map((grp) => (
                  <div
                    key={grp.group_name}
                    className="flex flex-col gap-1.5 border-t border-border/60 pt-2"
                  >
                    <div className="font-semibold text-foreground">{grp.group_name}</div>
                    {grp.windows.map((w) => (
                      <div key={`${grp.group_name}-${w.label}`} className="flex items-center gap-3">
                        <span className="w-14 text-muted-foreground">{w.label}</span>
                        <div className="flex-1">
                          <ProgressBar
                            value={w.remaining_pct}
                            max={100}
                            tone={w.remaining_pct > 20 ? 'success' : 'warning'}
                            ariaLabel={`${grp.group_name} ${w.label}`}
                          />
                        </div>
                        <span className="w-14 text-end font-mono font-semibold tabular-nums">
                          {w.remaining_pct}%
                        </span>
                        <span className="w-36 text-end text-muted-foreground">{w.reset_text}</span>
                      </div>
                    ))}
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* 5. EGRESS POOL DETAILS */}
          {activeModal === 'egress_pool' && (
            <div className="flex flex-col gap-3 text-xs">
              <div className="rounded-lg border border-border p-3">
                <div className="mb-2 flex items-center justify-between">
                  <span className="font-semibold text-foreground">
                    {`全部隔离账户与出口节点列表 (${accounts.length} 个节点)`}
                  </span>
                  <Badge variant="success-light">{`${fleetUsageSummary.readyCount} 个节点就绪`}</Badge>
                </div>
                <div className="divide-y divide-border">
                  {accounts.map((snap) => (
                    <div
                      key={snap.account.id}
                      className="flex flex-wrap items-center justify-between gap-3 py-2.5"
                    >
                      <div className="flex items-center gap-2">
                        <Badge
                          variant={
                            snap.account.status === 'ready' ? 'success-light' : 'warning-light'
                          }
                        >
                          {snap.account.status}
                        </Badge>
                        <span className="font-mono font-bold text-foreground">
                          {snap.account.id}
                        </span>
                        <span className="text-muted-foreground">({snap.account.name})</span>
                        <Badge variant="outline">{snap.account.network_mode}</Badge>
                      </div>
                      <div className="flex items-center gap-4 font-mono tabular-nums">
                        <span>{`出口: ${snap.account.last_egress_ip || '直连'}`}</span>
                        <span>{`权重: W=${snap.account.weight}`}</span>
                        <span>{`进程: ${snap.worker_count}/${snap.account.max_workers}`}</span>
                        <span>{`24h: ${fmtCompact(snap.usage?.tokens_24h || 0)} tok`}</span>
                        <Button
                          size="xs"
                          variant="outline"
                          onClick={() => onProbeAccount(snap.account.id)}
                        >
                          检测
                        </Button>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* 6. COMPLETION & TOOL RESULTS DETAILS */}
          {activeModal === 'completion_tools' && (
            <div className="flex flex-col gap-3.5 text-xs">
              {renderTurnSelector()}
              {renderLogPayloadBlocks(currentLog)}
            </div>
          )}

          {/* 7. LATEST TURN / FULL REQUEST & RESPONSE INSPECTOR */}
          {activeModal === 'latest_turn' && (
            <div className="flex flex-col gap-3.5 text-xs">
              {renderTurnSelector()}
              {renderLogPayloadBlocks(currentLog)}
            </div>
          )}

          {/* 8. 24H CALLS DETAILS */}
          {activeModal === 'calls_24h' && (
            <div className="flex flex-col gap-3.5 text-xs">
              <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
                <div className="rounded-lg border border-indigo-500/30 bg-indigo-500/10 p-3">
                  <div className="text-muted-foreground">24h 调用总数</div>
                  <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                    {`${fleetUsageSummary.requests24h} 次`}
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">24h Token 吞吐</div>
                  <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                    {`${fmtCompact(fleetUsageSummary.tokens24h)} token`}
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">有效命中率</div>
                  <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                    {overviewTelemetry.successRatePct}%
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">账户自动改道次数</div>
                  <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                    {`${overviewTelemetry.switchCount} 次`}
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* 9. AUDIT HISTORY DETAILS */}
          {activeModal === 'audit_history' && (
            <div className="flex flex-col gap-3.5 text-xs">
              {renderTurnSelector()}
              {renderLogPayloadBlocks(currentLog)}
            </div>
          )}

          {/* 10. REMAINING CAPACITY DETAILS */}
          {activeModal === 'remaining_capacity' && (
            <div className="flex flex-col gap-3.5 text-xs">
              <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-3">
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">当前可用上下文剩余空间</div>
                  <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                    {fmtCompact(overviewTelemetry.remainingTokens)} token
                  </div>
                  <div className="mt-0.5 text-muted-foreground">
                    {`总窗口容量: ${fmtCompact(overviewTelemetry.contextCapacity)} token`}
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">已用上下文占比</div>
                  <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                    {overviewTelemetry.usedPct}%
                  </div>
                  <div className="mt-0.5 text-muted-foreground">
                    {`已消耗: ${fmtCompact(overviewTelemetry.usedContextTokens)} token`}
                  </div>
                </div>
                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="text-muted-foreground">预计还可容纳同规模请求</div>
                  <div className="mt-1 text-xl font-bold text-foreground tabular-nums">
                    {`约 ${overviewTelemetry.estRemainingReqs} 个请求`}
                  </div>
                  <div className="mt-0.5 text-muted-foreground">
                    按单次平均消耗自动动态测算
                  </div>
                </div>
              </div>
            </div>
          )}
        </>
      )}
    </DialogModal>
  )
}
