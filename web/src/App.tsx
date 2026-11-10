import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  Cpu,
  Eye,
  EyeOff,
  FolderGit2,
  KeyRound,
  Layers,
  Lock,
  LogOut,
  Moon,
  Network,
  Plus,
  RefreshCw,
  Server,
  Sun,
  Terminal,
} from 'lucide-react'
import {
  ACPModelInfo,
  AccountRecord,
  AccountSnapshot,
  APIKeyRecord,
  AuditLogRecord,
  ModelQuotaGroup,
  OverviewResponse,
  PromptPolicyConfig,
  TerminalLoginStatus,
  UnauthorizedError,
  adminChangePassword,
  adminCheckSession,
  adminLogin,
  adminLogout,
  adminTerminalLoginStatus,
  apiFetch,
} from './api'
import { OverviewDetailModal } from './components/OverviewDetailModal'
import type { TreemapDetailKey } from './components/OverviewDetailModal'
import { OverviewBoard } from './components/OverviewBoard'
import type { ContextModuleSelection } from './components/OverviewBoard'
import { getStoredLang, Lang, useAutoI18n } from './i18n'
import { formatCompact } from './lib/format'
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Badge,
  BadgeVariant,
  Button,
  CodeBlock,
  ConfirmDialog,
  copyTextToClipboard,
  DataGridColumn,
  DataGridView,
  DialogModal,
  Frame,
  FrameDescription,
  FrameFooter,
  FrameHeader,
  FramePanel,
  FrameTitle,
  IconTile,
  InlineError,
  Input,
  ProgressBar,
  SelectField,
  Skeleton,
  Switch,
  Textarea,
} from './components/reui/primitives'

type NavTab = 'overview' | 'accounts' | 'clients' | 'keys'
type ClientProtocolTab = 'openai_official' | 'openai_compat' | 'claude'
type ClientSnippetFilter = 'all' | 'cli' | 'sdk' | 'curl' | 'models'
type AuthState = 'checking' | 'unknown' | 'unauthenticated' | 'authenticated'

const ADMIN_PASSWORD_MIN_LENGTH = 8
const ADMIN_TOKEN_MIN_LENGTH = 12
const LOGIN_STATUS_POLL_MS = 2000
const THEME_STORAGE_KEY = 'ag_gateway_theme'
const LEGACY_SESSION_STORAGE_KEYS = ['agy_admin_session', 'agy_admin_token', 'ag_admin_token']
const OVERVIEW_PATH = '/api/admin/overview?include_logs=1&logs_limit=60'

interface AccountFormState {
  id: string
  name: string
  auth_mode: string
  api_key: string
  isolation_enabled: boolean
  network_mode: string
  proxy_url: string
  bind_ip: string
  netns_name: string
  max_workers: string
  weight: string
  enabled: boolean
}

interface AccountWritePayload {
  id: string
  name: string
  auth_mode: string
  isolation_enabled: boolean
  network_mode: string
  proxy_url: string
  bind_ip: string
  netns_name: string
  max_workers: number
  weight: number
  enabled: boolean
  status: string
  api_key?: string
}

type PendingDeletion =
  | { kind: 'account'; id: string; name: string }
  | { kind: 'api_key'; id: number; name: string }

const DEFAULT_ACCOUNT_FORM: AccountFormState = {
  id: '',
  name: '',
  auth_mode: 'oauth_personal',
  api_key: '',
  isolation_enabled: true,
  network_mode: 'direct',
  proxy_url: '',
  bind_ip: '',
  netns_name: '',
  max_workers: '2',
  weight: '1',
  enabled: true,
}

function accountToForm(acc: AccountRecord): AccountFormState {
  return {
    id: acc.id,
    name: acc.name,
    auth_mode: acc.auth_mode,
    api_key: '',
    isolation_enabled: acc.isolation_enabled,
    network_mode: acc.network_mode,
    proxy_url: acc.proxy_url,
    bind_ip: acc.bind_ip,
    netns_name: acc.netns_name,
    max_workers: String(acc.max_workers),
    weight: String(acc.weight),
    enabled: acc.enabled,
  }
}

// The server upsert replaces whole records, so only user-editable fields plus the
// latest server-side status are sent; disabled_models / last_egress_ip are omitted.
function buildAccountPayload(
  acc: AccountRecord,
  overrides: Partial<AccountWritePayload> = {}
): AccountWritePayload {
  return {
    id: acc.id,
    name: acc.name,
    auth_mode: acc.auth_mode,
    isolation_enabled: acc.isolation_enabled,
    network_mode: acc.network_mode,
    proxy_url: acc.proxy_url,
    bind_ip: acc.bind_ip,
    netns_name: acc.netns_name,
    max_workers: acc.max_workers,
    weight: acc.weight,
    enabled: acc.enabled,
    status: acc.status,
    ...overrides,
  }
}

function parsePositiveInt(raw: string): number | null {
  const trimmed = raw.trim()
  if (!/^\d+$/.test(trimmed)) return null
  const n = Number(trimmed)
  return Number.isSafeInteger(n) && n >= 1 ? n : null
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback
}

function readStoredTheme(): boolean {
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY)
    if (stored === 'light') return false
    if (stored === 'dark') return true
  } catch {
    // ignore storage errors
  }
  return true
}

function purgeLegacySessionStorage(): void {
  for (const key of LEGACY_SESSION_STORAGE_KEYS) {
    try {
      window.localStorage.removeItem(key)
      window.sessionStorage.removeItem(key)
    } catch {
      // ignore storage errors
    }
  }
}

function statusBadgeVariant(status: string): BadgeVariant {
  switch (status.toLowerCase()) {
    case 'ready':
    case 'idle':
    case 'success':
      return 'success-light'
    case 'generating':
    case 'waiting_code':
      return 'info-light'
    case 'auth_required':
    case 'parked':
      return 'warning-light'
    default:
      return 'destructive-light'
  }
}

function formatLoginStateLabel(state?: string): string {
  switch ((state || '').toLowerCase()) {
    case 'waiting_code':
      return '等待授权码'
    case 'success':
      return '授权成功'
    case 'error':
      return '授权失败'
    case 'idle':
      return '待命'
    default:
      return state || '待命'
  }
}

function cleanTerminalLoginOutput(output?: string, authUrl?: string): string {
  if (!output) return ''
  return output
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => {
      if (!line) return false
      if (authUrl && line.includes(authUrl)) return false
      if (/^https:\/\/accounts\.google\.com\//i.test(line)) return false
      if (/^Starting agy acp --login/i.test(line)) return false
      if (/^(Please visit|Visit|Open) the following URL/i.test(line)) return false
      if (/^Then paste the authorization code/i.test(line)) return false
      if (/^Enter authorization code/i.test(line)) return false
      return true
    })
    .join('\n')
}

function formatPct(val: number): string {
  if (val === undefined || val === null) return '—'
  if (val >= 100) return '100%'
  return `${Number(val.toFixed(1))}%`
}

function formatRelativeAgo(iso?: string): string {
  if (!iso) return '尚未'
  const ts = Date.parse(iso)
  if (Number.isNaN(ts)) return '尚未'
  const nowMs = new Date().getTime()
  const diffSec = Math.max(0, Math.floor((nowMs - ts) / 1000))
  if (diffSec < 45) return '刚刚'
  const mins = Math.floor(diffSec / 60)
  if (mins < 60) return `${mins} 分钟前`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  return `${days} 天前`
}

function formatProxySubtitle(acc: AccountRecord): string {
  if (acc.network_mode === 'proxy' && acc.proxy_url) {
    return acc.proxy_url
  }
  if (acc.network_mode === 'bind_ip' && acc.bind_ip) {
    return `bind://${acc.bind_ip}`
  }
  if (acc.network_mode === 'netns' && acc.netns_name) {
    return `netns://${acc.netns_name}`
  }
  return acc.proxy_url || 'direct://本地直连'
}

function formatPromptModeLabel(mode?: string): string {
  switch (mode) {
    case 'pure_passthrough':
      return '纯透传 (Passthrough)'
    case 'prepend_global':
      return '全局前置指令'
    case 'native_hybrid':
    default:
      return '原生混合 (Hybrid)'
  }
}

function formatSessionModeLabel(mode?: string): string {
  switch (mode) {
    case 'stateless_single_turn':
      return '单轮无状态'
    case 'sticky_conversation':
      return 'X-Conversation-ID 粘性复用'
    case 'yolo':
    default:
      return 'YOLO 全自动'
  }
}

function mergeAuditLogsPreservingHydrated(
  prev: AuditLogRecord[],
  incoming: AuditLogRecord[]
): AuditLogRecord[] {
  if (incoming.length === 0) return prev.length === 0 ? prev : []
  if (
    prev.length === incoming.length &&
    prev[0]?.request_id === incoming[0]?.request_id &&
    prev[0]?.total_tokens === incoming[0]?.total_tokens &&
    prev[0]?.cached_tokens === incoming[0]?.cached_tokens &&
    prev[0]?.latency_ms === incoming[0]?.latency_ms
  ) {
    let allSame = true
    for (let i = 1; i < incoming.length; i += 1) {
      if (
        prev[i]?.request_id !== incoming[i]?.request_id ||
        prev[i]?.total_tokens !== incoming[i]?.total_tokens ||
        prev[i]?.cached_tokens !== incoming[i]?.cached_tokens
      ) {
        allSame = false
        break
      }
    }
    if (allSame) return prev
  }
  const prevById = new Map<string, AuditLogRecord>()
  for (const item of prev) {
    prevById.set(item.request_id, item)
  }
  return incoming.map((row) => {
    const existing = prevById.get(row.request_id)
    if (!existing) return row
    return {
      ...row,
      system_prompt: row.system_prompt || existing.system_prompt,
      prompt_text:
        row.prompt_text && row.prompt_text.length >= (existing.prompt_text?.length || 0)
          ? row.prompt_text
          : existing.prompt_text || row.prompt_text,
      thinking_text: row.thinking_text || existing.thinking_text,
      response_text: row.response_text || existing.response_text,
      tool_calls_json: row.tool_calls_json || existing.tool_calls_json,
    }
  })
}

function isCompactedLog(log: AuditLogRecord): boolean {
  return !log.response_text && !log.system_prompt && log.total_tokens > 0
}

export function App() {
  const [dark, setDark] = useState<boolean>(() => readStoredTheme())
  const [lang, setLang] = useState<Lang>(() => getStoredLang())
  useAutoI18n(lang)
  const [activeTab, setActiveTab] = useState<NavTab>('overview')
  const [authState, setAuthState] = useState<AuthState>('checking')
  const [sessionCheckError, setSessionCheckError] = useState<string>('')
  const [loginPassword, setLoginPassword] = useState<string>('')
  const [showLoginPassword, setShowLoginPassword] = useState<boolean>(false)
  const [rememberLogin, setRememberLogin] = useState<boolean>(false)
  const [loggingIn, setLoggingIn] = useState<boolean>(false)
  const [loginError, setLoginError] = useState<string>('')

  // Change password modal state
  const [showPasswordModal, setShowPasswordModal] = useState<boolean>(false)
  const [currentPasswordInput, setCurrentPasswordInput] = useState<string>('')
  const [newPasswordInput, setNewPasswordInput] = useState<string>('')
  const [confirmPasswordInput, setConfirmPasswordInput] = useState<string>('')
  const [newAdminTokenInput, setNewAdminTokenInput] = useState<string>('')
  const [changingPassword, setChangingPassword] = useState<boolean>(false)
  const [passwordModalError, setPasswordModalError] = useState<string>('')

  const [overview, setOverview] = useState<OverviewResponse | null>(null)
  const [accounts, setAccounts] = useState<AccountSnapshot[]>([])
  const [apiKeys, setApiKeys] = useState<APIKeyRecord[]>([])
  const [auditLogs, setAuditLogs] = useState<AuditLogRecord[]>([])
  const [policy, setPolicy] = useState<PromptPolicyConfig>({
    mode: 'native_hybrid',
    global_prepend_prompt: '',
    disable_builtin_tools: false,
    session_mode: 'yolo',
    internal_approval: 'auto_approve_safe',
  })
  const [loading, setLoading] = useState<boolean>(false)
  const [loadError, setLoadError] = useState<string>('')
  const [syncError, setSyncError] = useState<string>('')
  const [errorMsg, setErrorMsg] = useState<string>('')
  const [noticeMsg, setNoticeMsg] = useState<string>('')

  // Overview telemetry interactive state
  const [activeTreemapModal, setActiveTreemapModal] = useState<TreemapDetailKey | null>(null)
  const [activeTreemapModule, setActiveTreemapModule] = useState<ContextModuleSelection | null>(
    null
  )
  const [selectedAuditLogId, setSelectedAuditLogId] = useState<string>('')
  const [detailLogId, setDetailLogId] = useState<string>('')

  const selectedAuditLog = useMemo(() => {
    if (selectedAuditLogId) {
      const found = auditLogs.find((l) => l.request_id === selectedAuditLogId)
      if (found) return found
    }
    return auditLogs[0]
  }, [auditLogs, selectedAuditLogId])

  const boardLogIdRef = useRef<string>('')
  boardLogIdRef.current = selectedAuditLog?.request_id || ''

  // The detail modal is pinned to the request it was opened on, so new requests
  // arriving in the background never swap its content.
  const lastDetailLogRef = useRef<AuditLogRecord | undefined>(undefined)
  const detailLog = useMemo(() => {
    if (!detailLogId) return undefined
    return auditLogs.find((l) => l.request_id === detailLogId)
  }, [auditLogs, detailLogId])
  if (detailLog) {
    lastDetailLogRef.current = detailLog
  }
  const pinnedDetailLog =
    detailLog ||
    (lastDetailLogRef.current?.request_id === detailLogId ? lastDetailLogRef.current : undefined)

  const openTreemapModal = useCallback(
    (key: TreemapDetailKey, logId?: string, moduleSelection?: ContextModuleSelection) => {
      setDetailLogId(logId || boardLogIdRef.current)
      setActiveTreemapModule(moduleSelection || null)
      setActiveTreemapModal(key)
    },
    []
  )

  const closeTreemapModal = useCallback(() => {
    setActiveTreemapModal(null)
    setActiveTreemapModule(null)
    setDetailLogId('')
  }, [])

  // Account creation / edit form & view state
  const [accountForm, setAccountForm] = useState<AccountFormState>(DEFAULT_ACCOUNT_FORM)
  const [showAccountEditor, setShowAccountEditor] = useState<boolean>(false)
  const [isEditingAccount, setIsEditingAccount] = useState<boolean>(false)
  const [savingAccount, setSavingAccount] = useState<boolean>(false)
  const [accountFormError, setAccountFormError] = useState<string>('')
  const [accountViewMode, setAccountViewMode] = useState<'cards' | 'table'>('cards')
  const [loginState, setLoginState] = useState<Record<string, TerminalLoginStatus>>({})
  const [loginErrors, setLoginErrors] = useState<Record<string, string>>({})
  const [loginCodeInput, setLoginCodeInput] = useState<Record<string, string>>({})
  const [probingAccount, setProbingAccount] = useState<Record<string, boolean>>({})
  const [refreshingQuota, setRefreshingQuota] = useState<Record<string, boolean>>({})
  const [startingLogin, setStartingLogin] = useState<Record<string, boolean>>({})
  const [submittingLogin, setSubmittingLogin] = useState<Record<string, boolean>>({})
  const [proxyEditorOpen, setProxyEditorOpen] = useState<Record<string, boolean>>({})
  const [proxyDraft, setProxyDraft] = useState<
    Record<string, { network_mode: string; proxy_url: string; bind_ip: string }>
  >({})
  const [refreshingAccountModels, setRefreshingAccountModels] = useState<Record<string, boolean>>(
    {}
  )
  const [accountModelsError, setAccountModelsError] = useState<Record<string, string>>({})
  const [togglingAccountModel, setTogglingAccountModel] = useState<Record<string, boolean>>({})
  const [modelModalAccountId, setModelModalAccountId] = useState<string | null>(null)
  const [modelModalError, setModelModalError] = useState<string>('')

  // Remote Client Session Protocol Generator State (3 session protocols, dynamically populated from ACP backend)
  const [clientProtocolTab, setClientProtocolTab] = useState<ClientProtocolTab>('openai_official')
  const [clientSnippetFilter, setClientSnippetFilter] = useState<ClientSnippetFilter>('all')
  const [clientTokenPreview, setClientTokenPreview] = useState<string>('sk-ag-your-issued-key')
  const [clientSelectedKeyId, setClientSelectedKeyId] = useState<string>('')
  const [clientModelPreview, setClientModelPreview] = useState<string>('')
  const [clientAccountBind, setClientAccountBind] = useState<string>('auto')
  const [clientSessionId, setClientSessionId] = useState<string>('sess-dev-01')
  const [copyFeedback, setCopyFeedback] = useState<{ key: string; ok: boolean } | null>(null)

  const copyClientText = async (fieldKey: string, value: string) => {
    const ok = await copyTextToClipboard(value)
    setCopyFeedback({ key: fieldKey, ok })
    window.setTimeout(() => {
      setCopyFeedback((prev) => (prev?.key === fieldKey ? null : prev))
    }, 1500)
  }

  const copyLabel = (fieldKey: string, idleLabel: string): string => {
    if (copyFeedback?.key !== fieldKey) return idleLabel
    return copyFeedback.ok ? '已复制' : '复制失败'
  }

  // API Key modal & form (with per-key prompt & session policy)
  const [showCreateKeyModal, setShowCreateKeyModal] = useState<boolean>(false)
  const [creatingKey, setCreatingKey] = useState<boolean>(false)
  const [createKeyError, setCreateKeyError] = useState<string>('')
  const [newKeyName, setNewKeyName] = useState<string>('')
  const [newKeyAccount, setNewKeyAccount] = useState<string>('')
  const [newKeyRPM, setNewKeyRPM] = useState<string>('60')
  const [newKeyPromptMode, setNewKeyPromptMode] = useState<string>('native_hybrid')
  const [newKeySessionMode, setNewKeySessionMode] = useState<string>('yolo')
  const [newKeyPrependPrompt, setNewKeyPrependPrompt] = useState<string>('')
  const [newKeyDisableBuiltinTools, setNewKeyDisableBuiltinTools] = useState<boolean>(true)
  const [issuedPlainKey, setIssuedPlainKey] = useState<string>('')
  const [editingKey, setEditingKey] = useState<APIKeyRecord | null>(null)
  const [editKeyRPM, setEditKeyRPM] = useState<string>('')
  const [editKeyError, setEditKeyError] = useState<string>('')
  const [savingKeyEdit, setSavingKeyEdit] = useState<boolean>(false)

  const [pendingDeletion, setPendingDeletion] = useState<PendingDeletion | null>(null)
  const [deletingItem, setDeletingItem] = useState<boolean>(false)
  const [deletionError, setDeletionError] = useState<string>('')

  useEffect(() => {
    const root = document.documentElement
    if (dark) {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
    try {
      window.localStorage.setItem(THEME_STORAGE_KEY, dark ? 'dark' : 'light')
    } catch {
      // ignore storage errors
    }
  }, [dark])

  const accountsRef = useRef<AccountSnapshot[]>([])
  useEffect(() => {
    accountsRef.current = accounts
  }, [accounts])

  // Snapshot responses are applied only if they were issued after the last applied one
  // and after the latest local mutation, so slow older responses never overwrite newer state.
  const issuedSnapshotSeqRef = useRef<number>(0)
  const appliedSnapshotSeqRef = useRef<number>(0)
  const minAcceptedSnapshotSeqRef = useRef<number>(0)
  const nextSnapshotSeq = () => {
    issuedSnapshotSeqRef.current += 1
    return issuedSnapshotSeqRef.current
  }
  const claimSnapshotSeq = (seq: number): boolean => {
    if (seq <= appliedSnapshotSeqRef.current || seq < minAcceptedSnapshotSeqRef.current) {
      return false
    }
    appliedSnapshotSeqRef.current = seq
    return true
  }
  const invalidateInFlightSnapshots = () => {
    minAcceptedSnapshotSeqRef.current = issuedSnapshotSeqRef.current + 1
  }

  const applyOverviewSnapshot = useCallback((ov: OverviewResponse, logs?: AuditLogRecord[]) => {
    setOverview(ov)
    setAccounts(ov.accounts || [])
    accountsRef.current = ov.accounts || []
    const incomingLogs = logs || ov.logs
    if (incomingLogs) {
      setAuditLogs((prev) => mergeAuditLogsPreservingHydrated(prev, incomingLogs))
    }
    if (ov.models && ov.models.length > 0) {
      setClientModelPreview((prev) => prev || ov.models[0].modelId)
    }
    if (ov.prompt_policy) {
      setPolicy({
        ...ov.prompt_policy,
        session_mode: ov.session_mode || ov.prompt_policy.session_mode || 'yolo',
      })
    }
  }, [])

  const forceUnauthenticated = useCallback((msg = '登录会话已失效，请重新输入密码登录') => {
    setOverview(null)
    setAccounts([])
    accountsRef.current = []
    setApiKeys([])
    setAuditLogs([])
    setLoginState({})
    setLoadError('')
    setSyncError('')
    setActiveTreemapModal(null)
    setActiveTreemapModule(null)
    setDetailLogId('')
    setAuthState('unauthenticated')
    setLoginError(msg)
  }, [])

  const handleRequestError = useCallback(
    (err: unknown, fallbackMsg: string) => {
      if (err instanceof UnauthorizedError) {
        forceUnauthenticated(err.message || '未登录或管理会话已过期，请重新输入密码登录')
        return
      }
      setErrorMsg(errorMessage(err, fallbackMsg))
    },
    [forceUnauthenticated]
  )

  // Returns the message to show inline, or null when the session expired.
  const resolveInlineError = useCallback(
    (err: unknown, fallbackMsg: string): string | null => {
      if (err instanceof UnauthorizedError) {
        forceUnauthenticated(err.message || '未登录或管理会话已过期，请重新输入密码登录')
        return null
      }
      return errorMessage(err, fallbackMsg)
    },
    [forceUnauthenticated]
  )

  const inFlightTelemetryRef = useRef<boolean>(false)
  const pendingTelemetryFollowUpRef = useRef<boolean>(false)

  // Auto-dismiss transient status notices after 3.5s for clean UI disappearance
  useEffect(() => {
    if (!noticeMsg) return
    const timer = window.setTimeout(() => {
      setNoticeMsg('')
    }, 3500)
    return () => window.clearTimeout(timer)
  }, [noticeMsg])

  const refreshTelemetrySilently = useCallback(async () => {
    if (inFlightTelemetryRef.current) {
      pendingTelemetryFollowUpRef.current = true
      return
    }
    inFlightTelemetryRef.current = true
    const seq = nextSnapshotSeq()
    try {
      const ov = await apiFetch<OverviewResponse>(OVERVIEW_PATH)
      if (claimSnapshotSeq(seq)) {
        applyOverviewSnapshot(ov)
      }
      setSyncError('')
      setLoadError('')
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        pendingTelemetryFollowUpRef.current = false
        forceUnauthenticated(err.message || '未登录或管理会话已过期，请重新输入密码登录')
      } else {
        setSyncError(errorMessage(err, '后台同步失败'))
      }
    } finally {
      inFlightTelemetryRef.current = false
      if (pendingTelemetryFollowUpRef.current) {
        pendingTelemetryFollowUpRef.current = false
        window.setTimeout(() => {
          void refreshTelemetrySilently()
        }, 40)
      }
    }
  }, [applyOverviewSnapshot, forceUnauthenticated])

  const loadingCountRef = useRef<number>(0)
  const refreshAll = useCallback(
    async (options: { userInitiated?: boolean } = {}) => {
      const seq = nextSnapshotSeq()
      loadingCountRef.current += 1
      setLoading(true)
      if (options.userInitiated) {
        setErrorMsg('')
      }
      try {
        const [ov, keyRes] = await Promise.all([
          apiFetch<OverviewResponse>(OVERVIEW_PATH),
          apiFetch<{ api_keys: APIKeyRecord[] }>('/api/admin/api-keys'),
        ])
        const loadedKeys = keyRes.api_keys || []
        const incomingLogs =
          ov.logs ||
          (await apiFetch<{ logs: AuditLogRecord[] }>('/api/admin/logs?limit=60&compact=1'))
            .logs ||
          []
        setApiKeys(loadedKeys)
        setClientSelectedKeyId((prev) =>
          prev && loadedKeys.some((k) => String(k.id) === prev)
            ? prev
            : loadedKeys.length > 0
              ? String(loadedKeys[0].id)
              : ''
        )
        if (claimSnapshotSeq(seq)) {
          applyOverviewSnapshot(ov, incomingLogs)
        }
        setLoadError('')
        setSyncError('')
      } catch (err) {
        if (err instanceof UnauthorizedError) {
          forceUnauthenticated(err.message || '未登录或管理会话已过期，请重新输入密码登录')
        } else {
          setLoadError(errorMessage(err, '加载管理数据失败'))
        }
      } finally {
        loadingCountRef.current -= 1
        if (loadingCountRef.current <= 0) {
          loadingCountRef.current = 0
          setLoading(false)
        }
      }
    },
    [applyOverviewSnapshot, forceUnauthenticated]
  )

  const retryRefresh = useCallback(() => {
    void refreshAll({ userInitiated: true })
  }, [refreshAll])

  const navigateTab = useCallback((tab: NavTab) => {
    setActiveTab(tab)
  }, [])

  // Real-time event-driven WebSocket push (/api/admin/ws/wiretap?mode=telemetry) + visibility-aware background sync.
  // The socket authenticates with the same-origin HttpOnly session cookie only.
  useEffect(() => {
    if (authState !== 'authenticated') return
    let disposed = false
    let ws: WebSocket | null = null
    let reconnectTimer: number | undefined
    let debounceTimer: number | undefined
    let lastStreamRefreshAt = 0

    let wsConnected = false

    const scheduleImmediateSync = (delayMs = 35) => {
      if (disposed) return
      if (debounceTimer !== undefined) {
        window.clearTimeout(debounceTimer)
      }
      debounceTimer = window.setTimeout(() => {
        debounceTimer = undefined
        if (!disposed && document.visibilityState === 'visible') {
          void refreshTelemetrySilently()
        }
      }, delayMs)
    }

    const connectWiretap = () => {
      if (disposed) return
      try {
        const wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
        const wsUrl = `${wsProto}//${window.location.host}/api/admin/ws/wiretap?mode=telemetry`
        ws = new WebSocket(wsUrl)
        ws.onopen = () => {
          wsConnected = true
        }
        ws.onmessage = (evt) => {
          if (disposed) return
          try {
            const payload = JSON.parse(String(evt.data || '{}')) as { direction?: string }
            if (payload.direction === 'AUDIT' || payload.direction === 'STATE') {
              scheduleImmediateSync(30)
              return
            }
          } catch {
            // ignore malformed frame
          }
          const now = Date.now()
          if (now - lastStreamRefreshAt >= 450) {
            lastStreamRefreshAt = now
            scheduleImmediateSync(50)
          }
        }
        ws.onclose = () => {
          wsConnected = false
          if (!disposed) {
            reconnectTimer = window.setTimeout(connectWiretap, 1500)
          }
        }
      } catch {
        wsConnected = false
        if (!disposed) {
          reconnectTimer = window.setTimeout(connectWiretap, 1500)
        }
      }
    }

    connectWiretap()

    let lastPollAt = Date.now()
    const pollInterval = window.setInterval(() => {
      if (disposed || document.visibilityState !== 'visible') return
      const now = Date.now()
      // When WebSocket push is active, only run a fallback sync every 5s; when disconnected, poll every 1.5s
      const minInterval = wsConnected ? 5000 : 1500
      if (now - lastPollAt >= minInterval) {
        lastPollAt = now
        void refreshTelemetrySilently()
      }
    }, 1500)

    const onVisibilityOrFocus = () => {
      if (!disposed && document.visibilityState === 'visible') {
        void refreshTelemetrySilently()
      }
    }
    document.addEventListener('visibilitychange', onVisibilityOrFocus)
    window.addEventListener('focus', onVisibilityOrFocus)

    return () => {
      disposed = true
      if (debounceTimer !== undefined) window.clearTimeout(debounceTimer)
      if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer)
      window.clearInterval(pollInterval)
      document.removeEventListener('visibilitychange', onVisibilityOrFocus)
      window.removeEventListener('focus', onVisibilityOrFocus)
      if (ws) {
        try {
          ws.close()
        } catch {
          // ignore close error
        }
      }
    }
  }, [authState, refreshTelemetrySilently])

  // On-demand full log hydration while the detail modal is open on a compacted log.
  // Each request_id is fetched at most once per page session.
  const hydrationAttemptedRef = useRef<Set<string>>(new Set())
  const hydrateTargetId =
    activeTreemapModal && pinnedDetailLog && isCompactedLog(pinnedDetailLog)
      ? pinnedDetailLog.request_id
      : ''
  useEffect(() => {
    if (!hydrateTargetId || hydrationAttemptedRef.current.has(hydrateTargetId)) return
    hydrationAttemptedRef.current.add(hydrateTargetId)
    const hydrateFullLog = async () => {
      try {
        const res = await apiFetch<{ log?: AuditLogRecord; logs?: AuditLogRecord[] }>(
          `/api/admin/logs?request_id=${encodeURIComponent(hydrateTargetId)}`
        )
        const full = res.log || res.logs?.[0]
        if (full) {
          setAuditLogs((prev) =>
            prev.map((item) => (item.request_id === full.request_id ? full : item))
          )
        }
      } catch {
        // ignore background hydration failure
      }
    }
    void hydrateFullLog()
  }, [hydrateTargetId])

  const verifySession = useCallback(async () => {
    setAuthState('checking')
    setSessionCheckError('')
    try {
      const valid = await adminCheckSession()
      if (valid) {
        setAuthState('authenticated')
        void refreshAll()
      } else {
        setAuthState('unauthenticated')
      }
    } catch (err) {
      setSessionCheckError(errorMessage(err, '无法确认登录状态'))
      setAuthState('unknown')
    }
  }, [refreshAll])

  useEffect(() => {
    purgeLegacySessionStorage()
    void verifySession()
  }, [verifySession])

  // Seed in-progress terminal logins from the server snapshot (e.g. after a reload).
  const dismissedLoginRef = useRef<Set<string>>(new Set())
  useEffect(() => {
    const waiting = accounts.filter((snap) => snap.login_state === 'waiting_code')
    for (const snap of accounts) {
      if (snap.login_state !== 'waiting_code') {
        dismissedLoginRef.current.delete(snap.account.id)
      }
    }
    if (waiting.length === 0) return
    setLoginState((prev) => {
      let next = prev
      for (const snap of waiting) {
        const id = snap.account.id
        if (prev[id] || dismissedLoginRef.current.has(id)) continue
        if (next === prev) next = { ...prev }
        next[id] = { account_id: id, state: 'waiting_code', auth_url: snap.login_auth_url }
      }
      return next
    })
  }, [accounts])

  // Poll terminal login status every 2s while a login drawer is open and waiting for a code.
  const loginPollInFlightRef = useRef<Set<string>>(new Set())
  const waitingLoginIds = useMemo(
    () =>
      Object.entries(loginState)
        .filter(([, status]) => status.state === 'waiting_code')
        .map(([id]) => id)
        .sort()
        .join('\n'),
    [loginState]
  )
  useEffect(() => {
    if (authState !== 'authenticated' || !waitingLoginIds) return
    const ids = waitingLoginIds.split('\n')
    const controller = new AbortController()
    const pollOnce = () => {
      for (const id of ids) {
        if (loginPollInFlightRef.current.has(id)) continue
        loginPollInFlightRef.current.add(id)
        adminTerminalLoginStatus(id, controller.signal)
          .then((status) => {
            if (controller.signal.aborted) return
            setLoginState((prev) => {
              if (!prev[id]) return prev
              if (status.state === 'success') {
                const next = { ...prev }
                delete next[id]
                return next
              }
              return { ...prev, [id]: { ...prev[id], ...status } }
            })
            if (status.state === 'success') {
              setLoginErrors((prev) => ({ ...prev, [id]: '' }))
              setNoticeMsg(`账户 ${id} 授权验证成功，已恢复就绪状态`)
              void refreshAll()
            } else if (status.state === 'error') {
              setLoginErrors((prev) => ({
                ...prev,
                [id]: cleanTerminalLoginOutput(status.output, status.auth_url) || '授权失败，请重新发起登录',
              }))
            }
          })
          .catch((err: unknown) => {
            if (controller.signal.aborted) return
            const msg = resolveInlineError(err, '读取登录状态失败')
            if (msg) {
              setLoginErrors((prev) => ({ ...prev, [id]: msg }))
            }
          })
          .finally(() => {
            loginPollInFlightRef.current.delete(id)
          })
      }
    }
    const timer = window.setInterval(pollOnce, LOGIN_STATUS_POLL_MS)
    return () => {
      window.clearInterval(timer)
      controller.abort()
    }
  }, [authState, refreshAll, resolveInlineError, waitingLoginIds])

  const handleLoginSubmit = async (e?: React.FormEvent) => {
    if (e) e.preventDefault()
    if (!loginPassword) {
      setLoginError('请输入管理员密码')
      return
    }
    setLoggingIn(true)
    setLoginError('')
    setErrorMsg('')
    setNoticeMsg('')
    try {
      await adminLogin(loginPassword, rememberLogin)
      setLoginPassword('')
      setAuthState('authenticated')
      await refreshAll()
    } catch (err) {
      setLoginError(errorMessage(err, '登录失败，请检查管理员密码'))
    } finally {
      setLoggingIn(false)
    }
  }

  const handleLogout = async () => {
    await adminLogout()
    setOverview(null)
    setAccounts([])
    accountsRef.current = []
    setApiKeys([])
    setAuditLogs([])
    setLoginState({})
    setErrorMsg('')
    setNoticeMsg('')
    setLoginError('')
    setLoadError('')
    setSyncError('')
    setAuthState('unauthenticated')
  }

  const openPasswordModal = () => {
    setCurrentPasswordInput('')
    setNewPasswordInput('')
    setConfirmPasswordInput('')
    setNewAdminTokenInput('')
    setPasswordModalError('')
    setShowPasswordModal(true)
  }

  const closePasswordModal = () => {
    setShowPasswordModal(false)
    setPasswordModalError('')
  }

  const handleChangePassword = async () => {
    if (!currentPasswordInput) {
      setPasswordModalError('请输入当前管理员密码')
      return
    }
    if (newPasswordInput.length < ADMIN_PASSWORD_MIN_LENGTH) {
      setPasswordModalError(`新管理员密码长度不能少于 ${ADMIN_PASSWORD_MIN_LENGTH} 位`)
      return
    }
    if (newPasswordInput !== confirmPasswordInput) {
      setPasswordModalError('两次输入的新密码不一致')
      return
    }
    if (newAdminTokenInput && newAdminTokenInput.length < ADMIN_TOKEN_MIN_LENGTH) {
      setPasswordModalError(`自定义 Admin Token 长度不能少于 ${ADMIN_TOKEN_MIN_LENGTH} 位`)
      return
    }
    setChangingPassword(true)
    setPasswordModalError('')
    setNoticeMsg('')
    try {
      await adminChangePassword(
        currentPasswordInput,
        newPasswordInput,
        newAdminTokenInput || undefined
      )
      setOverview((prev) => (prev ? { ...prev, credentials_locked: true } : prev))
      setShowPasswordModal(false)
      setCurrentPasswordInput('')
      setNewPasswordInput('')
      setConfirmPasswordInput('')
      setNewAdminTokenInput('')
      setNoticeMsg('管理员密码与 Token 已更新并永久固定，后续重新部署不会再重置')
    } catch (err) {
      const msg = resolveInlineError(err, '修改管理员密码失败')
      if (msg) setPasswordModalError(msg)
    } finally {
      setChangingPassword(false)
    }
  }

  const discoveredModels = useMemo<ACPModelInfo[]>(() => {
    return overview?.models || []
  }, [overview])

  const enabledClientModels = useMemo<ACPModelInfo[]>(() => {
    const targetAccId = clientAccountBind && clientAccountBind !== 'auto' ? clientAccountBind : ''
    if (targetAccId) {
      const snap = accounts.find((a) => a.account.id === targetAccId)
      if (!snap) return discoveredModels
      const baseList = snap.models && snap.models.length > 0 ? snap.models : discoveredModels
      const disabledSet = new Set(
        (snap.account.disabled_models || []).map((m) => m.trim().toLowerCase())
      )
      return baseList.filter((m) => !disabledSet.has(m.modelId.toLowerCase()))
    }
    const enabledSnaps = accounts.filter((a) => a.account.enabled)
    if (enabledSnaps.length === 0) return discoveredModels
    return discoveredModels.filter((m) => {
      const lower = m.modelId.toLowerCase()
      return enabledSnaps.some(
        (s) => !(s.account.disabled_models || []).some((dm) => dm.trim().toLowerCase() === lower)
      )
    })
  }, [accounts, clientAccountBind, discoveredModels])

  const effectiveClientModel = useMemo<string>(() => {
    if (clientModelPreview.trim()) return clientModelPreview.trim()
    if (enabledClientModels.length > 0) return enabledClientModels[0].modelId
    if (discoveredModels.length > 0) return discoveredModels[0].modelId
    return '<从 /v1/models 实时获取的模型ID>'
  }, [clientModelPreview, enabledClientModels, discoveredModels])

  // Aggregate usage metrics across all accounts
  const fleetUsageSummary = useMemo(() => {
    return accounts.reduce(
      (acc, item) => {
        const u = item.usage
        acc.totalRequests += u?.total_requests || 0
        acc.requests24h += u?.requests_24h || 0
        acc.errorRequests += u?.error_requests || 0
        acc.promptTokens += u?.prompt_tokens || 0
        acc.completionTokens += u?.completion_tokens || 0
        acc.totalTokens += u?.total_tokens || 0
        acc.tokens24h += u?.tokens_24h || 0
        acc.busyWorkers += item.busy_workers
        acc.spawnedWorkers += item.worker_count
        acc.maxWorkers += item.account.max_workers || 0
        if (item.account.enabled) {
          acc.enabledCount += 1
        }
        if (item.account.enabled && item.account.status === 'ready') {
          acc.readyCount += 1
        }
        return acc
      },
      {
        enabledCount: 0,
        readyCount: 0,
        totalRequests: 0,
        requests24h: 0,
        errorRequests: 0,
        promptTokens: 0,
        completionTokens: 0,
        totalTokens: 0,
        tokens24h: 0,
        busyWorkers: 0,
        spawnedWorkers: 0,
        maxWorkers: 0,
      }
    )
  }, [accounts])

  const latestAccount = (id: string): AccountRecord | undefined =>
    accountsRef.current.find((snap) => snap.account.id === id)?.account

  const openCreateAccountEditor = () => {
    setAccountForm(DEFAULT_ACCOUNT_FORM)
    setIsEditingAccount(false)
    setAccountFormError('')
    setShowAccountEditor(true)
  }

  const closeAccountEditor = () => {
    setShowAccountEditor(false)
    setAccountFormError('')
  }

  const handleSaveAccount = async () => {
    const id = accountForm.id.trim()
    const name = accountForm.name.trim()
    if (!id || !name) {
      setAccountFormError('账户 ID 与名称不能为空')
      return
    }
    if (!isEditingAccount && accountsRef.current.some((snap) => snap.account.id === id)) {
      setAccountFormError(`账户 ID「${id}」已存在，请使用其他 ID 或编辑现有账户`)
      return
    }
    const maxWorkers = parsePositiveInt(accountForm.max_workers)
    if (maxWorkers === null) {
      setAccountFormError('最大并发进程数必须是不小于 1 的整数')
      return
    }
    const weight = parsePositiveInt(accountForm.weight)
    if (weight === null) {
      setAccountFormError('调度权重必须是不小于 1 的整数')
      return
    }
    const payload: AccountWritePayload = {
      id,
      name,
      auth_mode: accountForm.auth_mode,
      isolation_enabled: accountForm.isolation_enabled,
      network_mode: accountForm.network_mode,
      proxy_url: accountForm.proxy_url.trim(),
      bind_ip: accountForm.bind_ip.trim(),
      netns_name: accountForm.netns_name.trim(),
      max_workers: maxWorkers,
      weight,
      enabled: accountForm.enabled,
      status: latestAccount(id)?.status || 'ready',
    }
    if (accountForm.api_key) {
      payload.api_key = accountForm.api_key
    }
    setSavingAccount(true)
    setAccountFormError('')
    setNoticeMsg('')
    try {
      await apiFetch('/api/admin/accounts', {
        method: 'POST',
        body: JSON.stringify(payload),
      })
      invalidateInFlightSnapshots()
      setNoticeMsg(`账户 ${id} 已保存并同步隔离目录`)
      setAccountForm(DEFAULT_ACCOUNT_FORM)
      setShowAccountEditor(false)
      await refreshAll()
    } catch (err) {
      const msg = resolveInlineError(err, '保存账户失败')
      if (msg) setAccountFormError(msg)
    } finally {
      setSavingAccount(false)
    }
  }

  const handleEditAccount = (acc: AccountRecord) => {
    setAccountForm(accountToForm(latestAccount(acc.id) || acc))
    setIsEditingAccount(true)
    setAccountFormError('')
    setShowAccountEditor(true)
  }

  const handleToggleAccountEnabled = async (acc: AccountRecord) => {
    const current = latestAccount(acc.id) || acc
    setErrorMsg('')
    setNoticeMsg('')
    try {
      await apiFetch('/api/admin/accounts', {
        method: 'POST',
        body: JSON.stringify(buildAccountPayload(current, { enabled: !current.enabled })),
      })
      invalidateInFlightSnapshots()
      setNoticeMsg(`账户 ${current.id} 已${current.enabled ? '停用' : '启用'}`)
      await refreshAll()
    } catch (err) {
      handleRequestError(err, '切换账户状态失败')
    }
  }

  const requestDeleteAccount = (acc: AccountRecord) => {
    setDeletionError('')
    setPendingDeletion({ kind: 'account', id: acc.id, name: acc.name || acc.id })
  }

  const requestDeleteAPIKey = (key: APIKeyRecord) => {
    setDeletionError('')
    setPendingDeletion({ kind: 'api_key', id: key.id, name: key.name || key.key_prefix })
  }

  const cancelDeletion = () => {
    setPendingDeletion(null)
    setDeletionError('')
  }

  const confirmDeletion = async () => {
    if (!pendingDeletion) return
    setDeletingItem(true)
    setDeletionError('')
    try {
      if (pendingDeletion.kind === 'account') {
        await apiFetch(`/api/admin/accounts/${encodeURIComponent(pendingDeletion.id)}`, {
          method: 'DELETE',
        })
        setNoticeMsg(`账户 ${pendingDeletion.id} 已删除`)
      } else {
        await apiFetch(`/api/admin/api-keys/${pendingDeletion.id}`, { method: 'DELETE' })
        setNoticeMsg(`已吊销密钥「${pendingDeletion.name}」`)
      }
      invalidateInFlightSnapshots()
      setPendingDeletion(null)
      await refreshAll()
    } catch (err) {
      const msg = resolveInlineError(
        err,
        pendingDeletion.kind === 'account' ? '删除账户失败' : '吊销 API Key 失败'
      )
      if (msg) setDeletionError(msg)
    } finally {
      setDeletingItem(false)
    }
  }

  const handleProbeEgress = async (id: string) => {
    setProbingAccount((prev) => ({ ...prev, [id]: true }))
    setErrorMsg('')
    setNoticeMsg('')
    try {
      const res = await apiFetch<{ egress_ip: string }>(
        `/api/admin/accounts/${encodeURIComponent(id)}/probe`,
        { method: 'POST' }
      )
      setNoticeMsg(`账户 ${id} 检测通过，出口 IP: ${res.egress_ip}`)
      await refreshAll()
    } catch (err) {
      handleRequestError(err, '出口检测失败')
      if (!(err instanceof UnauthorizedError)) {
        await refreshAll()
      }
    } finally {
      setProbingAccount((prev) => ({ ...prev, [id]: false }))
    }
  }

  const handleRefreshQuota = async (id: string) => {
    setRefreshingQuota((prev) => ({ ...prev, [id]: true }))
    setErrorMsg('')
    setNoticeMsg('')
    try {
      await apiFetch(`/api/admin/accounts/${encodeURIComponent(id)}/refresh-quota`, {
        method: 'POST',
      })
      setNoticeMsg(`已刷新账户 ${id} 的模型额度状态`)
      await refreshAll()
    } catch (err) {
      handleRequestError(err, '刷新额度失败')
    } finally {
      setRefreshingQuota((prev) => ({ ...prev, [id]: false }))
    }
  }

  const handleClearCooldown = async (id: string) => {
    setErrorMsg('')
    setNoticeMsg('')
    try {
      await apiFetch(`/api/admin/accounts/${encodeURIComponent(id)}/clear-cooldown`, {
        method: 'POST',
      })
      setNoticeMsg(`已清除账户 ${id} 的冷却限制并恢复就绪状态`)
      await refreshAll()
    } catch (err) {
      handleRequestError(err, '清除冷却失败')
    }
  }

  const handleRefreshAccountModels = async (accountId: string) => {
    setRefreshingAccountModels((prev) => ({ ...prev, [accountId]: true }))
    setAccountModelsError((prev) => ({ ...prev, [accountId]: '' }))
    setNoticeMsg('')
    try {
      const res = await apiFetch<{
        account_id: string
        models: ACPModelInfo[]
        models_cached_at?: string
        error?: string
      }>(`/api/admin/accounts/${encodeURIComponent(accountId)}/refresh-models`, {
        method: 'POST',
      })
      if (res.error && (!res.models || res.models.length === 0)) {
        setAccountModelsError((prev) => ({ ...prev, [accountId]: res.error || '拉取模型失败' }))
        return
      }
      const nextModels = res.models || []
      invalidateInFlightSnapshots()
      setAccounts((prev) =>
        prev.map((snap) =>
          snap.account.id === accountId
            ? {
                ...snap,
                models: nextModels,
                models_cached_at: res.models_cached_at || snap.models_cached_at,
              }
            : snap
        )
      )
      if (nextModels.length > 0) {
        setNoticeMsg(`账户 ${accountId} 已实时同步 ${nextModels.length} 个 ACP 可用模型`)
      }
      void refreshTelemetrySilently()
    } catch (err) {
      const msg = resolveInlineError(err, '探测账户模型失败')
      if (msg) {
        setAccountModelsError((prev) => ({ ...prev, [accountId]: msg }))
      }
    } finally {
      setRefreshingAccountModels((prev) => ({ ...prev, [accountId]: false }))
    }
  }

  const applyDisabledModels = (accountId: string, disabledModels: string[]) => {
    invalidateInFlightSnapshots()
    setAccounts((prev) =>
      prev.map((snap) =>
        snap.account.id === accountId
          ? {
              ...snap,
              account: {
                ...snap.account,
                disabled_models: disabledModels,
              },
            }
          : snap
      )
    )
  }

  const handleToggleAccountModelDisabled = async (acc: AccountRecord, modelId: string) => {
    const toggleKey = `${acc.id}:${modelId}`
    setTogglingAccountModel((prev) => ({ ...prev, [toggleKey]: true }))
    setModelModalError('')
    setNoticeMsg('')
    try {
      const current = latestAccount(acc.id) || acc
      const currentlyDisabled = (current.disabled_models || []).some(
        (m) => m.trim().toLowerCase() === modelId.trim().toLowerCase()
      )
      const res = await apiFetch<{
        account_id: string
        disabled_models: string[]
        account: AccountRecord
      }>(`/api/admin/accounts/${encodeURIComponent(acc.id)}/disabled-models`, {
        method: 'PUT',
        body: JSON.stringify({
          model_id: modelId,
          disabled: !currentlyDisabled,
        }),
      })
      applyDisabledModels(acc.id, res.disabled_models || [])
      setNoticeMsg(
        !currentlyDisabled
          ? `已在账户 ${acc.id} 下禁用模型 ${modelId}`
          : `已在账户 ${acc.id} 下恢复启用模型 ${modelId}`
      )
    } catch (err) {
      const msg = resolveInlineError(err, '更新账户模型启用状态失败')
      if (msg) setModelModalError(msg)
    } finally {
      setTogglingAccountModel((prev) => ({ ...prev, [toggleKey]: false }))
    }
  }

  const handleEnableAllAccountModels = async (acc: AccountRecord) => {
    setModelModalError('')
    setNoticeMsg('')
    try {
      const res = await apiFetch<{
        account_id: string
        disabled_models: string[]
      }>(`/api/admin/accounts/${encodeURIComponent(acc.id)}/disabled-models`, {
        method: 'PUT',
        body: JSON.stringify({
          disabled_models: [],
        }),
      })
      applyDisabledModels(acc.id, res.disabled_models || [])
      setNoticeMsg(`已恢复启用账户 ${acc.id} 下的全部 ACP 模型`)
    } catch (err) {
      const msg = resolveInlineError(err, '恢复全部模型失败')
      if (msg) setModelModalError(msg)
    }
  }

  const openModelModal = (accountId: string) => {
    setModelModalError('')
    setModelModalAccountId(accountId)
  }

  const closeModelModal = () => {
    setModelModalAccountId(null)
    setModelModalError('')
  }

  const toggleQuickProxy = (acc: AccountRecord) => {
    const current = latestAccount(acc.id) || acc
    setProxyEditorOpen((prev) => ({ ...prev, [acc.id]: !prev[acc.id] }))
    setProxyDraft((prev) => ({
      ...prev,
      [acc.id]: {
        network_mode: current.network_mode === 'direct' ? 'proxy' : current.network_mode,
        proxy_url: current.proxy_url || '',
        bind_ip: current.bind_ip || '',
      },
    }))
  }

  const handleSaveQuickProxy = async (acc: AccountRecord) => {
    const draft = proxyDraft[acc.id]
    if (!draft) return
    const proxyUrl = draft.proxy_url.trim()
    const bindIp = draft.bind_ip.trim()
    if (draft.network_mode === 'proxy' && !proxyUrl) {
      setErrorMsg('请输入代理地址，例如 socks5://user:pass@127.0.0.1:1080')
      return
    }
    if (draft.network_mode === 'bind_ip' && !bindIp) {
      setErrorMsg('请输入要绑定的网卡 IP')
      return
    }
    const current = latestAccount(acc.id) || acc
    setErrorMsg('')
    setNoticeMsg('')
    try {
      await apiFetch('/api/admin/accounts', {
        method: 'POST',
        body: JSON.stringify(
          buildAccountPayload(current, {
            network_mode: draft.network_mode,
            proxy_url: proxyUrl,
            bind_ip: bindIp,
          })
        ),
      })
      invalidateInFlightSnapshots()
      setProxyEditorOpen((prev) => ({ ...prev, [acc.id]: false }))
      setNoticeMsg(`账户 ${acc.id} 的代理与出口配置已热更新`)
      await refreshAll()
    } catch (err) {
      handleRequestError(err, '更换代理失败')
    }
  }

  const closeLoginDrawer = (id: string) => {
    dismissedLoginRef.current.add(id)
    setLoginState((prev) => {
      const next = { ...prev }
      delete next[id]
      return next
    })
    setLoginErrors((prev) => ({ ...prev, [id]: '' }))
  }

  const handleStartTerminalLogin = async (id: string) => {
    setStartingLogin((prev) => ({ ...prev, [id]: true }))
    setLoginErrors((prev) => ({ ...prev, [id]: '' }))
    try {
      const status = await apiFetch<TerminalLoginStatus>(
        `/api/admin/accounts/${encodeURIComponent(id)}/login/start`,
        { method: 'POST' }
      )
      dismissedLoginRef.current.delete(id)
      setLoginState((prev) => ({ ...prev, [id]: status }))
      setNoticeMsg(`已为账户 ${id} 启动重新登录会话`)
    } catch (err) {
      const msg = resolveInlineError(err, '启动登录失败')
      if (msg) setLoginErrors((prev) => ({ ...prev, [id]: msg }))
    } finally {
      setStartingLogin((prev) => ({ ...prev, [id]: false }))
    }
  }

  const handleSubmitTerminalCode = async (id: string) => {
    const code = (loginCodeInput[id] || '').trim()
    if (!code) return
    setSubmittingLogin((prev) => ({ ...prev, [id]: true }))
    setLoginErrors((prev) => ({ ...prev, [id]: '' }))
    try {
      const status = await apiFetch<TerminalLoginStatus>(
        `/api/admin/accounts/${encodeURIComponent(id)}/login/submit`,
        {
          method: 'POST',
          body: JSON.stringify({ code }),
        }
      )
      setLoginCodeInput((prev) => ({ ...prev, [id]: '' }))
      if (status.state === 'success') {
        setLoginState((prev) => {
          const next = { ...prev }
          delete next[id]
          return next
        })
        setNoticeMsg(`账户 ${id} 授权验证成功，已恢复就绪状态`)
      } else {
        setLoginState((prev) => ({ ...prev, [id]: status }))
        if (status.state === 'error') {
          setLoginErrors((prev) => ({
            ...prev,
            [id]: cleanTerminalLoginOutput(status.output, status.auth_url) || '授权失败，请重新发起登录',
          }))
        } else {
          setNoticeMsg(`已向账户 ${id} 提交授权验证码`)
        }
      }
      await refreshAll()
    } catch (err) {
      const msg = resolveInlineError(err, '提交验证码失败')
      if (msg) setLoginErrors((prev) => ({ ...prev, [id]: msg }))
    } finally {
      setSubmittingLogin((prev) => ({ ...prev, [id]: false }))
    }
  }

  const openCreateAPIKeyModal = () => {
    setNewKeyName('')
    setNewKeyAccount('')
    setNewKeyRPM('60')
    setNewKeyPromptMode(policy.mode || 'native_hybrid')
    setNewKeySessionMode(policy.session_mode || 'yolo')
    setNewKeyPrependPrompt(policy.global_prepend_prompt || '')
    setNewKeyDisableBuiltinTools(policy.disable_builtin_tools ?? true)
    setCreateKeyError('')
    setShowCreateKeyModal(true)
  }

  const closeCreateAPIKeyModal = () => {
    setShowCreateKeyModal(false)
    setCreateKeyError('')
  }

  const handleCreateAPIKey = async () => {
    const name = newKeyName.trim()
    if (!name) {
      setCreateKeyError('请输入客户端密钥名称')
      return
    }
    const rpm = parsePositiveInt(newKeyRPM)
    if (rpm === null) {
      setCreateKeyError('每分钟请求上限 (RPM) 必须是不小于 1 的整数')
      return
    }
    const boundAccountId = newKeyAccount.trim()
    setCreatingKey(true)
    setCreateKeyError('')
    try {
      const res = await apiFetch<{ plain_key: string; record?: APIKeyRecord }>(
        '/api/admin/api-keys',
        {
          method: 'POST',
          body: JSON.stringify({
            name,
            bound_account_id: boundAccountId,
            rate_limit_rpm: rpm,
            prompt_mode: newKeyPromptMode,
            session_mode: newKeySessionMode,
            global_prepend_prompt: newKeyPrependPrompt,
            disable_builtin_tools: newKeyDisableBuiltinTools,
          }),
        }
      )
      setIssuedPlainKey(res.plain_key)
      setClientTokenPreview(res.plain_key)
      if (res.record?.id) {
        setClientSelectedKeyId(String(res.record.id))
      }
      setClientAccountBind(boundAccountId || 'auto')
      setNewKeyName('')
      setShowCreateKeyModal(false)
      setNoticeMsg('已签发新的客户端 API Key 并同步至客户端接入设置，请立即复制保存下方明文密钥')
      await refreshAll()
    } catch (err) {
      const msg = resolveInlineError(err, '签发 API Key 失败')
      if (msg) setCreateKeyError(msg)
    } finally {
      setCreatingKey(false)
    }
  }

  const openEditAPIKey = (key: APIKeyRecord) => {
    setEditingKey({
      ...key,
      bound_account_id: key.bound_account_id || '',
      prompt_mode: key.prompt_mode || 'native_hybrid',
      session_mode: key.session_mode || 'yolo',
      global_prepend_prompt: key.global_prepend_prompt || '',
      disable_builtin_tools: key.disable_builtin_tools ?? true,
    })
    setEditKeyRPM(String(key.rate_limit_rpm))
    setEditKeyError('')
  }

  const closeEditAPIKey = () => {
    setEditingKey(null)
    setEditKeyError('')
  }

  const handleSaveKeyEdit = async () => {
    if (!editingKey) return
    const name = editingKey.name.trim()
    if (!name) {
      setEditKeyError('请输入客户端密钥名称')
      return
    }
    const rpm = parsePositiveInt(editKeyRPM)
    if (rpm === null) {
      setEditKeyError('每分钟请求上限 (RPM) 必须是不小于 1 的整数')
      return
    }
    const boundAccountId = (editingKey.bound_account_id || '').trim()
    setSavingKeyEdit(true)
    setEditKeyError('')
    setNoticeMsg('')
    try {
      await apiFetch(`/api/admin/api-keys/${editingKey.id}`, {
        method: 'PUT',
        body: JSON.stringify({
          name,
          bound_account_id: boundAccountId,
          rate_limit_rpm: rpm,
          prompt_mode: editingKey.prompt_mode || 'native_hybrid',
          session_mode: editingKey.session_mode || 'yolo',
          global_prepend_prompt: editingKey.global_prepend_prompt || '',
          disable_builtin_tools: editingKey.disable_builtin_tools ?? true,
        }),
      })
      setEditingKey(null)
      setNoticeMsg(`已更新密钥「${name}」的独立策略`)
      await refreshAll()
    } catch (err) {
      const msg = resolveInlineError(err, '保存密钥独立策略失败')
      if (msg) setEditKeyError(msg)
    } finally {
      setSavingKeyEdit(false)
    }
  }

  const gatewayBaseUrl = useMemo(() => {
    if (overview?.public_base_url) return overview.public_base_url
    return window.location.origin
  }, [overview])

  const gatewayHostPort = useMemo(() => {
    return gatewayBaseUrl.replace(/^https?:\/\//, '')
  }, [gatewayBaseUrl])

  const accountColumns: DataGridColumn<AccountSnapshot>[] = [
    {
      id: 'account',
      header: '账户名称 / ID',
      cell: (row) => (
        <div translate="no">
          <div className="font-medium text-foreground">{row.account.name}</div>
          <div className="font-mono text-muted-foreground">{row.account.id}</div>
        </div>
      ),
    },
    {
      id: 'auth',
      header: '鉴权方式',
      cell: (row) => <Badge variant="info-light">{row.account.auth_mode}</Badge>,
    },
    {
      id: 'usage_tokens',
      header: '今日 Token (入/出)',
      align: 'end',
      cell: (row) => (
        <div className="font-mono tabular-nums">
          <span className="font-semibold text-foreground">
            {formatCompact(row.usage?.today_prompt_tokens || 0)} /{' '}
            {formatCompact(row.usage?.today_completion_tokens || 0)}
          </span>
        </div>
      ),
    },
    {
      id: 'usage_requests',
      header: '今日 成功 / 失败',
      align: 'end',
      cell: (row) => (
        <div className="font-mono tabular-nums">
          <span className="text-foreground">{row.usage?.today_success || 0}</span>
          <span className="ms-1 text-muted-foreground">/ {row.usage?.today_failed || 0}</span>
        </div>
      ),
    },
    {
      id: 'egress',
      header: '代理 / 出口 IP',
      cell: (row) => (
        <div className="flex items-center gap-1.5">
          <Badge variant="outline">{row.account.network_mode}</Badge>
          <span className="font-mono text-muted-foreground">
            {row.account.last_egress_ip || '待检测'}
          </span>
        </div>
      ),
    },
    {
      id: 'status',
      header: '状态',
      cell: (row) => (
        <Badge variant={statusBadgeVariant(row.account.status)}>{row.account.status}</Badge>
      ),
    },
    {
      id: 'acp_models',
      header: 'ACP 可用模型',
      cell: (row) => {
        const list = row.models && row.models.length > 0 ? row.models : discoveredModels
        const disabledSet = new Set(
          (row.account.disabled_models || []).map((m) => m.trim().toLowerCase())
        )
        const enabledNum = list.filter((m) => !disabledSet.has(m.modelId.toLowerCase())).length
        const disabledNum = list.length - enabledNum
        return (
          <div className="flex flex-wrap items-center gap-1">
            <Badge variant={list.length > 0 ? 'success-light' : 'warning-light'}>
              {list.length > 0 ? `启用 ${enabledNum}/${list.length}` : '待探测'}
            </Badge>
            {disabledNum > 0 && <Badge variant="destructive-light">{`禁用 ${disabledNum}`}</Badge>}
          </div>
        )
      },
    },
    {
      id: 'actions',
      header: '操作',
      align: 'end',
      cell: (row) => (
        <div className="inline-flex items-center justify-end gap-1">
          <Button size="xs" variant="outline" onClick={() => void handleProbeEgress(row.account.id)}>
            检测
          </Button>
          <Button
            size="xs"
            variant="outline"
            onClick={() => void handleRefreshQuota(row.account.id)}
          >
            刷新额度
          </Button>
          <Button
            size="xs"
            variant="outline"
            onClick={() => openModelModal(row.account.id)}
          >
            模型
          </Button>
          <Button size="xs" variant="outline" onClick={() => handleEditAccount(row.account)}>
            编辑
          </Button>
        </div>
      ),
    },
  ]

  // "运行总览" placed at the very top of navigation
  const navItems: { id: NavTab; label: string; badge?: string; icon: typeof Activity }[] = [
    { id: 'overview', label: '运行总览', icon: Activity },
    {
      id: 'accounts',
      label: '多账户与额度卡片',
      badge: String(accounts.length),
      icon: Network,
    },
    { id: 'clients', label: '远程客户端接入', icon: Terminal },
    {
      id: 'keys',
      label: '客户端 API Key',
      badge: apiKeys.length > 0 ? String(apiKeys.length) : undefined,
      icon: KeyRound,
    },
  ]

  const renderAccountQuotaCard = (snap: AccountSnapshot) => {
    const acc = snap.account
    const u = snap.usage
    const quotaGroups: ModelQuotaGroup[] = u?.quota_groups || []
    const isReadyState = acc.status === 'ready'
    const statusLabel =
      acc.status === 'ready' ? '正常' : acc.status === 'auth_required' ? '待登录' : '异常'
    const activeLogin = loginState[acc.id]
    const cleanedLoginOutput = activeLogin
      ? cleanTerminalLoginOutput(activeLogin.output, activeLogin.auth_url)
      : ''
    const isProbing = Boolean(probingAccount[acc.id])
    const isRefreshingQuota = Boolean(refreshingQuota[acc.id])
    const isStartingLogin = Boolean(startingLogin[acc.id])
    const isSubmittingLogin = Boolean(submittingLogin[acc.id])
    const hasCustomCheckMsg =
      Boolean(u?.last_check_msg) && u?.last_check_msg !== '待点击刷新额度读取'
    const activeProxyEdit = proxyEditorOpen[acc.id]
    const currentDraft = proxyDraft[acc.id] || {
      network_mode: acc.network_mode,
      proxy_url: acc.proxy_url,
      bind_ip: acc.bind_ip,
    }

    return (
      <Frame key={acc.id} spacing="xs" className="flex flex-col justify-between">
        <FramePanel className="p-3.5 flex flex-col gap-2.5">
          {/* 1. Top Header: Account ID/Name + Status Pill + Enable Switch */}
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2 min-w-0">
              <span className="text-sm font-bold tracking-tight text-foreground truncate" translate="no">
                {acc.id}
              </span>
              {acc.name && acc.name !== acc.id && (
                <span className="text-xs text-muted-foreground truncate" translate="no">
                  ({acc.name})
                </span>
              )}
              <Badge variant={isReadyState ? 'success-light' : 'warning-light'}>
                {statusLabel}
              </Badge>
            </div>

            <Switch
              checked={acc.enabled}
              onCheckedChange={() => void handleToggleAccountEnabled(acc)}
              ariaLabel={`启用或停用账户 ${acc.id}`}
            />
          </div>

          {/* 2. Proxy & Egress Subtitle + Quota Read Timestamp */}
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <div className="flex flex-wrap items-center gap-1 font-mono truncate">
              <span>代理</span>
              <span className="truncate" translate="no">
                {formatProxySubtitle(acc)}
              </span>
              <span>·</span>
              <span>{`出口 ${acc.last_egress_ip || '待检测'}`}</span>
            </div>
            {u?.quota_read_at && (
              <span className="shrink-0">{`额度 · ${formatRelativeAgo(u.quota_read_at)}同步`}</span>
            )}
          </div>

          {/* 3. Model Group Quota Bars (parsed from official agy acp /usage) */}
          <div className="flex flex-col gap-2 pt-0.5">
            {isRefreshingQuota && quotaGroups.length === 0 ? (
              <div className="rounded-md border border-border bg-muted/20 p-3 flex flex-col gap-2">
                <Skeleton className="h-3.5 w-28" />
                <Skeleton className="h-2 w-full" />
                <Skeleton className="h-2 w-4/5" />
              </div>
            ) : quotaGroups.length > 0 ? (
              quotaGroups.map((group) => (
                <div key={group.group_name} className="flex flex-col gap-1">
                  <div className="text-xs font-semibold text-foreground">{group.group_name}</div>
                  <div className="flex flex-col gap-1">
                    {group.windows.map((win) => (
                      <div key={`${group.group_name}-${win.label}`} className="flex flex-col gap-0.5">
                        <div className="flex items-center gap-2">
                          <span className="w-10 shrink-0 text-xs text-muted-foreground">
                            {win.label}
                          </span>
                          <div className="flex-1">
                            <ProgressBar
                              value={win.remaining_pct}
                              max={100}
                              size="default"
                              tone={
                                win.remaining_pct > 20
                                  ? 'success'
                                  : win.remaining_pct > 5
                                    ? 'warning'
                                    : 'destructive'
                              }
                              ariaLabel={`${acc.id} ${group.group_name} ${win.label} 剩余额度`}
                            />
                          </div>
                          <span className="w-11 text-end font-mono text-xs font-medium tabular-nums text-foreground">
                            {formatPct(win.remaining_pct)}
                          </span>
                        </div>
                        <div className="ps-12 text-xs text-muted-foreground leading-tight">
                          {win.reset_text}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              ))
            ) : (
              <div className="rounded-md border border-border bg-muted/25 px-3 py-2.5 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span className="min-w-0 truncate">
                  {hasCustomCheckMsg
                    ? `尚未读取到模型额度（${u?.last_check_msg}）`
                    : acc.status === 'auth_required'
                      ? '账户待登录授权，完成授权后可读取模型额度'
                      : '尚未读取到模型额度'}
                </span>
                {acc.status === 'auth_required' && !activeLogin ? (
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={isStartingLogin}
                    onClick={() => void handleStartTerminalLogin(acc.id)}
                  >
                    {isStartingLogin ? '启动中...' : '立即登录'}
                  </Button>
                ) : (
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={isRefreshingQuota}
                    onClick={() => void handleRefreshQuota(acc.id)}
                  >
                    {isRefreshingQuota ? '读取中...' : '读取额度'}
                  </Button>
                )}
              </div>
            )}
          </div>

          {/* 4. Compact Daily & 7-Day Telemetry Footer Lines */}
          <div className="flex flex-col gap-1 border-t border-border/60 pt-2 text-xs text-muted-foreground tabular-nums">
            <div className="flex flex-wrap items-center gap-1.5">
              <span>
                {'今日 成功 '}
                <strong className="font-semibold text-foreground">{u?.today_success ?? 0}</strong>
                {' · 失败 '}
                <strong className="font-semibold text-foreground">{u?.today_failed ?? 0}</strong>
                {' · Token '}
                <strong className="font-semibold text-foreground">
                  {`${formatCompact(u?.today_prompt_tokens ?? 0)} / ${formatCompact(
                    u?.today_completion_tokens ?? 0
                  )}`}
                </strong>
              </span>
              <span className="text-muted-foreground/40">|</span>
              <span>
                {'近 7 天 '}
                <strong className="font-semibold text-foreground">
                  {`${formatCompact(u?.week_prompt_tokens ?? 0)} / ${formatCompact(
                    u?.week_completion_tokens ?? 0
                  )}`}
                </strong>
              </span>
            </div>

            <div className="flex flex-wrap items-center gap-1.5">
              <span>
                {'正在处理 '}
                <strong className="font-semibold text-foreground">{snap.busy_workers}</strong>
              </span>
              <span className="text-muted-foreground/40">|</span>
              <span className="truncate">
                {'上次检测 '}
                {u?.last_check_at ? (
                  <>
                    <span>{`${formatRelativeAgo(u.last_check_at)} · `}</span>
                    <strong className="font-semibold text-foreground">
                      {u?.last_check_msg || '额度读取正常'}
                    </strong>
                  </>
                ) : (
                  <strong className="font-semibold text-foreground">
                    {hasCustomCheckMsg ? u?.last_check_msg : '尚未执行'}
                  </strong>
                )}
              </span>
            </div>
          </div>

          {/* Inline Quick Proxy Switcher (when 换代理 is clicked) */}
          {activeProxyEdit && (
            <div className="rounded-lg border border-border bg-muted/30 p-2.5 flex flex-col gap-2">
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-semibold text-foreground">
                  {`快速更换代理 (${acc.id})`}
                </span>
                <SelectField
                  value={currentDraft.network_mode}
                  onValueChange={(v) =>
                    setProxyDraft((prev) => ({
                      ...prev,
                      [acc.id]: { ...currentDraft, network_mode: v },
                    }))
                  }
                  options={[
                    { value: 'proxy', label: 'SOCKS5/HTTP' },
                    { value: 'bind_ip', label: '网卡 IP' },
                    { value: 'direct', label: '直连' },
                  ]}
                />
              </div>
              {currentDraft.network_mode !== 'direct' && (
                <Input
                  value={
                    currentDraft.network_mode === 'bind_ip'
                      ? currentDraft.bind_ip
                      : currentDraft.proxy_url
                  }
                  onChange={(v) =>
                    setProxyDraft((prev) => ({
                      ...prev,
                      [acc.id]:
                        currentDraft.network_mode === 'bind_ip'
                          ? { ...currentDraft, bind_ip: v }
                          : { ...currentDraft, proxy_url: v },
                    }))
                  }
                  placeholder={
                    currentDraft.network_mode === 'bind_ip'
                      ? '192.168.1.50'
                      : 'socks5://user:pass@127.0.0.1:1080'
                  }
                />
              )}
              <div className="flex items-center justify-end gap-1.5">
                <Button
                  size="xs"
                  variant="ghost"
                  onClick={() => setProxyEditorOpen((prev) => ({ ...prev, [acc.id]: false }))}
                >
                  取消
                </Button>
                <Button size="xs" onClick={() => void handleSaveQuickProxy(acc)}>
                  保存代理
                </Button>
              </div>
            </div>
          )}

          {loginErrors[acc.id] && !activeLogin && (
            <InlineError
              title="登录授权失败"
              message={loginErrors[acc.id]}
              onRetry={() => void handleStartTerminalLogin(acc.id)}
              retrying={isStartingLogin}
            />
          )}

          {/* Inline Terminal Login Drawer (when 重新登录 is clicked) */}
          {activeLogin && (
            <div className="rounded-lg border border-info/35 bg-info/8 p-3 flex flex-col gap-2.5">
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-semibold text-foreground">
                  无头 Terminal Login 授权引导
                </span>
                <div className="flex items-center gap-1.5">
                  <Badge variant={statusBadgeVariant(activeLogin.state)}>
                    {formatLoginStateLabel(activeLogin.state)}
                  </Badge>
                  <Button size="xs" variant="ghost" onClick={() => closeLoginDrawer(acc.id)}>
                    收起
                  </Button>
                </div>
              </div>

              {loginErrors[acc.id] && (
                <InlineError
                  title="登录授权失败"
                  message={loginErrors[acc.id]}
                  onRetry={() => void handleStartTerminalLogin(acc.id)}
                  retrying={isStartingLogin}
                  retryLabel="重新发起登录"
                />
              )}

              {activeLogin.auth_url && (
                <div className="flex flex-col gap-1.5">
                  <span className="text-xs text-muted-foreground">
                    1. 在浏览器打开授权链接完成 Google 账号登录：
                  </span>
                  <div className="flex items-center gap-1.5">
                    <div
                      title={activeLogin.auth_url}
                      translate="no"
                      className="flex-1 min-w-0 rounded-md border border-border bg-background/90 px-2.5 py-1.5 font-mono text-xs text-muted-foreground truncate select-all"
                    >
                      {activeLogin.auth_url}
                    </div>
                    <Button
                      size="xs"
                      variant="outline"
                      onClick={() =>
                        void copyClientText(`login-url-${acc.id}`, activeLogin.auth_url || '')
                      }
                    >
                      {copyLabel(`login-url-${acc.id}`, '复制链接')}
                    </Button>
                    <Button
                      size="xs"
                      onClick={() =>
                        window.open(activeLogin.auth_url, '_blank', 'noopener,noreferrer')
                      }
                    >
                      打开授权页
                    </Button>
                  </div>
                </div>
              )}

              <div className="flex flex-col gap-1.5">
                <span className="text-xs text-muted-foreground">
                  2. 粘贴授权验证码或浏览器回调的完整 URL：
                </span>
                <div className="flex items-center gap-1.5">
                  <Input
                    className="flex-1 min-w-0"
                    value={loginCodeInput[acc.id] || ''}
                    onChange={(v) => setLoginCodeInput((prev) => ({ ...prev, [acc.id]: v }))}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault()
                        void handleSubmitTerminalCode(acc.id)
                      }
                    }}
                    placeholder="粘贴 Google / Antigravity 授权码或回调链接..."
                    disabled={isSubmittingLogin}
                  />
                  <Button
                    size="xs"
                    disabled={isSubmittingLogin || !(loginCodeInput[acc.id] || '').trim()}
                    onClick={() => void handleSubmitTerminalCode(acc.id)}
                  >
                    {isSubmittingLogin ? '提交中...' : '提交验证'}
                  </Button>
                </div>
              </div>

              {cleanedLoginOutput && (
                <pre
                  translate="no"
                  className="max-h-24 overflow-y-auto rounded-md border border-border bg-background/90 p-2 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all"
                >
                  {cleanedLoginOutput}
                </pre>
              )}
            </div>
          )}

          {/* 5. Bottom Compact Action Buttons Row (with 模型 & 编辑 opening modals) */}
          <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
            <Button
              size="xs"
              variant="outline"
              disabled={isProbing}
              onClick={() => void handleProbeEgress(acc.id)}
            >
              {isProbing ? '检测中...' : '检测'}
            </Button>
            <Button
              size="xs"
              variant="outline"
              disabled={isRefreshingQuota}
              onClick={() => void handleRefreshQuota(acc.id)}
            >
              {isRefreshingQuota ? '刷新中...' : '刷新额度'}
            </Button>
            <Button
              size="xs"
              variant="outline"
              onClick={() => openModelModal(acc.id)}
            >
              模型
            </Button>
            <Button
              size="xs"
              variant="outline"
              disabled={isStartingLogin}
              onClick={() => void handleStartTerminalLogin(acc.id)}
            >
              {isStartingLogin ? '启动中...' : '重新登录'}
            </Button>
            <Button size="xs" variant="outline" onClick={() => toggleQuickProxy(acc)}>
              换代理
            </Button>
            <Button size="xs" variant="outline" onClick={() => handleEditAccount(acc)}>
              编辑
            </Button>
            <Button size="xs" variant="outline" onClick={() => void handleClearCooldown(acc.id)}>
              清除冷却
            </Button>
            <Button size="xs" variant="destructive" onClick={() => requestDeleteAccount(acc)}>
              删除
            </Button>
          </div>
        </FramePanel>
      </Frame>
    )
  }

  if (authState === 'checking' || authState === 'unknown') {
    return (
      <div className="min-h-screen bg-background text-foreground flex items-center justify-center p-4">
        <Frame className="w-full max-w-sm">
          <FramePanel className="flex flex-col items-center gap-3 py-8 text-center">
            <IconTile variant="elevated">
              <Lock />
            </IconTile>
            {authState === 'checking' ? (
              <>
                <div className="text-sm font-medium text-foreground">正在验证管理会话...</div>
                <div className="text-xs text-muted-foreground">请稍候，正在检查访问权限</div>
              </>
            ) : (
              <InlineError
                className="text-start"
                title="无法确认登录状态"
                message={sessionCheckError || '无法连接网关服务，请检查网络后重试'}
                onRetry={() => void verifySession()}
              />
            )}
          </FramePanel>
        </Frame>
      </div>
    )
  }

  if (authState !== 'authenticated') {
    return (
      <div className="min-h-screen bg-background text-foreground flex flex-col justify-between p-4">
        <div className="flex items-center justify-end gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setLang((l) => (l === 'zh' ? 'en' : 'zh'))}
          >
            {lang === 'zh' ? 'EN / 中文' : '中文 / EN'}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            ariaLabel="切换深浅色主题"
            onClick={() => setDark((d) => !d)}
          >
            {dark ? <Sun /> : <Moon />}
          </Button>
        </div>

        <div className="mx-auto flex w-full max-w-md flex-col gap-4 my-auto">
          <Frame>
            <FrameHeader>
              <div className="flex items-center gap-3">
                <IconTile variant="elevated">
                  <Lock />
                </IconTile>
                <div>
                  <FrameTitle>Antigravity ACP Gateway</FrameTitle>
                  <FrameDescription>
                    Web 管理控制台受密码保护，必须登录才能查看与操作
                  </FrameDescription>
                </div>
              </div>
              <Badge variant="info-light">管理鉴权</Badge>
            </FrameHeader>

            <FramePanel>
              <form
                onSubmit={(e) => void handleLoginSubmit(e)}
                className="flex flex-col gap-4"
              >
                {loginError && (
                  <Alert variant="destructive">
                    <div className="flex-1">
                      <AlertTitle>登录验证未通过</AlertTitle>
                      <AlertDescription>{loginError}</AlertDescription>
                    </div>
                  </Alert>
                )}

                <div className="flex flex-col gap-1.5">
                  <label
                    htmlFor="admin-login-password"
                    className="text-xs font-medium text-foreground"
                  >
                    管理员密码
                  </label>
                  <div className="flex items-center gap-1.5">
                    <Input
                      id="admin-login-password"
                      type={showLoginPassword ? 'text' : 'password'}
                      value={loginPassword}
                      onChange={setLoginPassword}
                      placeholder="请输入管理员密码..."
                      autoFocus
                      autoComplete="current-password"
                      disabled={loggingIn}
                    />
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      ariaLabel={showLoginPassword ? '隐藏密码' : '显示密码'}
                      onClick={() => setShowLoginPassword((v) => !v)}
                    >
                      {showLoginPassword ? <EyeOff /> : <Eye />}
                    </Button>
                  </div>
                  <p className="text-xs text-muted-foreground leading-relaxed">
                    未自定义前每次部署动态生成随机密码与 Token，可在部署输出、
                    <code className="mx-1 font-mono text-foreground">docker logs antigravity-gateway</code>
                    或
                    <code className="mx-1 font-mono text-foreground">data/admin_credentials.txt</code>
                    中查看；登录后修改密码即永久固定。
                  </p>
                </div>

                <div className="flex items-center justify-between gap-2">
                  <span className="text-xs text-muted-foreground">
                    保持登录状态 (7 天内免重复输入)
                  </span>
                  <Switch
                    checked={rememberLogin}
                    onCheckedChange={setRememberLogin}
                    ariaLabel="保持登录状态 7 天"
                    disabled={loggingIn}
                  />
                </div>

                <Button
                  type="submit"
                  size="default"
                  disabled={loggingIn || !loginPassword}
                  className="w-full"
                >
                  <Lock />
                  {loggingIn ? '正在验证密码...' : '登录管理控制台'}
                </Button>
              </form>
            </FramePanel>

            <FrameFooter>
              <span>
                配置项: <code className="font-mono">server.admin_password</code>
              </span>
              <span>Responses / Chat / Claude</span>
            </FrameFooter>
          </Frame>
        </div>

        <div className="text-center text-xs text-muted-foreground pb-2">
          Antigravity ACP Gateway · 未授权禁止访问控制平面
        </div>
      </div>
    )
  }

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col">
      {/* Top Bar */}
      <header className="sticky top-0 z-20 border-b border-border bg-card/85 backdrop-blur-md px-5 py-2.5">
        <div className="mx-auto flex max-w-[1440px] flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <IconTile variant="elevated">
              <Server />
            </IconTile>
            <div>
              <div className="flex items-center gap-2">
                <h1 className="text-sm font-semibold tracking-tight text-foreground">
                  Antigravity ACP Gateway
                </h1>
                <Badge variant="info-light">Responses / Chat / Claude</Badge>
                <Badge variant="success-light">
                  {`${fleetUsageSummary.readyCount}/${accounts.length} 账户正常`}
                </Badge>
              </div>
              <p className="text-xs text-muted-foreground">
                服务端 <code className="font-mono">agy acp</code> 多账户隔离池、模型周期额度监控与三大会话协议统一网关
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Badge variant={overview?.credentials_locked ? 'success-light' : 'warning-light'}>
              {overview?.credentials_locked ? '已登录 · 密码已固定' : '已登录 · 动态密码'}
            </Badge>
            <Button
              variant="outline"
              size="sm"
              onClick={openPasswordModal}
            >
              <KeyRound />
              修改密码
            </Button>
            <Button variant="outline" size="sm" onClick={retryRefresh} disabled={loading}>
              <RefreshCw />
              刷新
            </Button>
            <Button variant="outline" size="sm" onClick={() => void handleLogout()}>
              <LogOut />
              退出登录
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setLang((l) => (l === 'zh' ? 'en' : 'zh'))}
            >
              {lang === 'zh' ? 'EN / 中文' : '中文 / EN'}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              ariaLabel="切换深浅色主题"
              onClick={() => setDark((d) => !d)}
            >
              {dark ? <Sun /> : <Moon />}
            </Button>
          </div>
        </div>
      </header>

      <DialogModal
        open={showPasswordModal}
        onClose={closePasswordModal}
        title="修改并固定管理员密码与 Token"
        description={
          overview?.credentials_locked
            ? '当前凭证已由用户自定义固定，重部署不会重置'
            : '当前使用的是每次部署动态生成的临时凭证；在此主动修改后将永久固定，后续重新部署不再变更'
        }
        footer={
          <>
            <span className="text-xs text-muted-foreground">修改后立即持久化锁定至数据库</span>
            <div className="flex items-center gap-2">
              <Button size="sm" variant="outline" onClick={closePasswordModal}>
                取消
              </Button>
              <Button
                size="sm"
                disabled={changingPassword}
                onClick={() => void handleChangePassword()}
              >
                {changingPassword ? '正在保存...' : '确认修改并固定'}
              </Button>
            </div>
          </>
        }
      >
        <div className="flex flex-col gap-3">
          {passwordModalError && (
            <Alert variant="destructive">
              <div className="flex-1">
                <AlertTitle>修改失败</AlertTitle>
                <AlertDescription>{passwordModalError}</AlertDescription>
              </div>
            </Alert>
          )}
          <div className="flex flex-col gap-1">
            <label htmlFor="current-admin-pwd" className="text-xs font-medium text-foreground">
              当前管理员密码或 Token
            </label>
            <Input
              id="current-admin-pwd"
              type="password"
              value={currentPasswordInput}
              onChange={setCurrentPasswordInput}
              placeholder="输入当前正在使用的密码或 Admin Token"
              autoComplete="current-password"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="new-admin-pwd" className="text-xs font-medium text-foreground">
              新管理员密码
            </label>
            <Input
              id="new-admin-pwd"
              type="password"
              value={newPasswordInput}
              onChange={setNewPasswordInput}
              placeholder="输入至少 8 位的新密码（修改后永久固定）"
              autoComplete="new-password"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="confirm-admin-pwd" className="text-xs font-medium text-foreground">
              确认新密码
            </label>
            <Input
              id="confirm-admin-pwd"
              type="password"
              value={confirmPasswordInput}
              onChange={setConfirmPasswordInput}
              placeholder="再次输入新密码"
              autoComplete="new-password"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="new-admin-token" className="text-xs font-medium text-foreground">
              自定义 Admin Token（可选，留空则固定当前 Token）
            </label>
            <Input
              id="new-admin-token"
              type="password"
              value={newAdminTokenInput}
              onChange={setNewAdminTokenInput}
              placeholder="至少 12 位，用于脚本调用 /api/admin/*（留空保持当前值并固定）"
              autoComplete="new-password"
            />
          </div>
        </div>
      </DialogModal>

      <ConfirmDialog
        open={pendingDeletion !== null}
        title={pendingDeletion?.kind === 'api_key' ? '确认吊销 API Key' : '确认删除隔离账户'}
        description={
          pendingDeletion?.kind === 'api_key'
            ? '吊销后使用该密钥的客户端将立即无法访问网关，此操作不可撤销。'
            : '删除后该账户的隔离目录与登录凭证将被移除，此操作不可撤销。'
        }
        confirmLabel={pendingDeletion?.kind === 'api_key' ? '吊销密钥' : '删除账户'}
        busy={deletingItem}
        error={deletionError}
        onConfirm={() => void confirmDeletion()}
        onCancel={cancelDeletion}
      >
        {pendingDeletion && (
          <div className="flex flex-col gap-1 rounded-lg border border-border bg-muted/30 p-3 text-sm">
            <span className="text-xs text-muted-foreground">
              {pendingDeletion.kind === 'api_key' ? '将要吊销的密钥' : '将要删除的账户'}
            </span>
            <span className="font-semibold text-foreground" translate="no">
              {pendingDeletion.kind === 'account' && pendingDeletion.name !== pendingDeletion.id
                ? `${pendingDeletion.name} (${pendingDeletion.id})`
                : pendingDeletion.name}
            </span>
          </div>
        )}
      </ConfirmDialog>

      {/* Main Layout */}
      <div className="mx-auto flex w-full max-w-[1440px] flex-1 gap-5 px-5 py-5">
        {/* Sidebar Navigation */}
        <aside className="w-60 shrink-0">
          <Frame className="sticky top-16">
            <FrameHeader>
              <FrameTitle>控制平面导航</FrameTitle>
            </FrameHeader>
            <FramePanel className="p-1.5 flex flex-col gap-1">
              <nav aria-label="主导航" className="flex flex-col gap-0.5">
                {navItems.map((item) => {
                  const Icon = item.icon
                  const isCurrentTab = activeTab === item.id
                  return (
                    <button
                      key={item.id}
                      type="button"
                      aria-current={isCurrentTab ? 'page' : undefined}
                      onClick={() => setActiveTab(item.id)}
                      className={`flex w-full items-center justify-between rounded-md px-2.5 py-2 text-xs font-medium transition-colors duration-150 ${
                        isCurrentTab
                          ? 'bg-muted text-foreground font-semibold'
                          : 'text-muted-foreground hover:bg-accent hover:text-foreground'
                      }`}
                    >
                      <span className="flex items-center gap-2">
                        <Icon className="size-3.5 shrink-0" />
                        <span>{item.label}</span>
                      </span>
                      {item.badge && (
                        <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
                          {item.badge}
                        </span>
                      )}
                    </button>
                  )
                })}
              </nav>
            </FramePanel>
            <FrameFooter>
              <span>全池 24h Token</span>
              <span className="font-mono font-semibold text-foreground tabular-nums">
                {formatCompact(fleetUsageSummary.tokens24h)}
              </span>
            </FrameFooter>
          </Frame>
        </aside>

        {/* Content Column */}
        <main className="min-w-0 grow flex flex-col gap-4">
          {errorMsg && (
            <Alert variant="destructive">
              <div className="flex-1">
                <AlertTitle>操作异常</AlertTitle>
                <AlertDescription>{errorMsg}</AlertDescription>
              </div>
            </Alert>
          )}
          {(loadError || syncError) &&
            !(activeTab === 'overview' && !overview && auditLogs.length === 0) && (
              <InlineError
                title={loadError ? '加载管理数据失败' : '后台同步中断，当前显示的数据可能已过期'}
                message={loadError || syncError}
                onRetry={retryRefresh}
                retrying={loading}
              />
            )}
          {noticeMsg && (
            <Alert variant="success">
              <div className="flex-1">
                <AlertTitle>状态已同步</AlertTitle>
                <AlertDescription>{noticeMsg}</AlertDescription>
              </div>
            </Alert>
          )}

          {/* TAB 1 (TOP): OVERVIEW — PER-REQUEST ROUTING & CONTEXT WINDOW BOARD */}
          {activeTab === 'overview' && (
            <div className="flex flex-col gap-4">
              <OverviewBoard
                overview={overview}
                accounts={accounts}
                auditLogs={auditLogs}
                selectedAuditLog={selectedAuditLog}
                onSelectAuditLogId={setSelectedAuditLogId}
                gatewayHostPort={gatewayHostPort}
                loading={loading}
                loadError={loadError}
                syncError={syncError}
                fleetUsageSummary={fleetUsageSummary}
                onOpenModal={openTreemapModal}
                onNavigateTab={navigateTab}
                onRefresh={retryRefresh}
              />

              <OverviewDetailModal
                activeModal={activeTreemapModal}
                activeModule={activeTreemapModule}
                onClose={closeTreemapModal}
                selectedAuditLog={pinnedDetailLog}
                onSelectAuditLogId={setDetailLogId}
                auditLogs={auditLogs}
                onNavigateTab={navigateTab}
              />
            </div>
          )}

          {/* TAB 2: ACCOUNTS (REFERENCE QUOTA & TELEMETRY CARDS) */}
          {activeTab === 'accounts' && (
            <div className="flex flex-col gap-4">
              {/* Top Action & Summary Header */}
              <Frame>
                <FrameHeader>
                  <div className="flex items-center gap-2.5">
                    <IconTile variant="elevated">
                      <Layers />
                    </IconTile>
                    <div>
                      <FrameTitle>多账户隔离池与模型额度监控卡片</FrameTitle>
                      <FrameDescription>
                        实时展示每个隔离账户的代理与出口 IP、Gemini / Claude 周期额度剩余百分比、今日与近 7 天 Token 明细及快捷运维操作
                      </FrameDescription>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <SelectField
                      value={accountViewMode}
                      onValueChange={(v) => setAccountViewMode(v as 'cards' | 'table')}
                      options={[
                        { value: 'cards', label: '卡片视图' },
                        { value: 'table', label: '表格视图' },
                      ]}
                    />
                    <Button size="sm" onClick={openCreateAccountEditor}>
                      <Plus />
                      新增隔离账户
                    </Button>
                  </div>
                </FrameHeader>
              </Frame>

              {/* Modal Dialog for Account Creation / Editing */}
              <DialogModal
                open={showAccountEditor}
                onClose={closeAccountEditor}
                title={isEditingAccount ? `编辑隔离账户: ${accountForm.id}` : '新增隔离账户'}
                translateTitle={!isEditingAccount}
                description="配置独立 GEMINI_CLI_HOME 容器目录、ACP 认证方式、SOCKS5/HTTP 代理或源网卡 IP 绑定"
                footer={
                  <>
                    <span className="text-xs text-muted-foreground font-mono">
                      {`目录: data/accounts/${accountForm.id || '<id>'}/gemini_home`}
                    </span>
                    <div className="flex items-center gap-2">
                      <Button size="sm" variant="outline" onClick={closeAccountEditor}>
                        取消
                      </Button>
                      <Button
                        size="sm"
                        disabled={savingAccount}
                        onClick={() => void handleSaveAccount()}
                      >
                        {savingAccount ? '正在保存...' : '保存配置'}
                      </Button>
                    </div>
                  </>
                }
              >
                {accountFormError && (
                  <Alert variant="destructive">
                    <div className="flex-1">
                      <AlertTitle>保存失败</AlertTitle>
                      <AlertDescription>{accountFormError}</AlertDescription>
                    </div>
                  </Alert>
                )}
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <div className="flex flex-col gap-1">
                    <label htmlFor="acc-id" className="text-xs font-medium text-foreground">
                      {isEditingAccount ? '账户标识 ID (创建后不可修改)' : '账户标识 ID (例如 g1, g2)'}
                    </label>
                    <Input
                      id="acc-id"
                      value={accountForm.id}
                      onChange={(v) => setAccountForm({ ...accountForm, id: v })}
                      placeholder="例如: g1 或 us-node-01"
                      readOnly={isEditingAccount}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <label htmlFor="acc-name" className="text-xs font-medium text-foreground">
                      备注名称
                    </label>
                    <Input
                      id="acc-name"
                      value={accountForm.name}
                      onChange={(v) => setAccountForm({ ...accountForm, name: v })}
                      placeholder="例如: 主力账号 g1"
                    />
                  </div>
                </div>

                <div className="flex flex-col gap-1">
                  <label className="text-xs font-medium text-foreground">ACP 认证方式</label>
                  <div>
                    <SelectField
                      value={accountForm.auth_mode}
                      onValueChange={(v) => setAccountForm({ ...accountForm, auth_mode: v })}
                      options={[
                        { value: 'oauth_personal', label: 'Google Personal' },
                        { value: 'oauth_business', label: 'Gemini Enterprise' },
                        { value: 'gemini_api_key', label: 'GEMINI_API_KEY' },
                        { value: 'agent_platform', label: 'Vertex AI' },
                      ]}
                    />
                  </div>
                </div>

                {accountForm.auth_mode === 'gemini_api_key' && (
                  <div className="flex flex-col gap-1">
                    <label htmlFor="acc-apikey" className="text-xs font-medium text-foreground">
                      GEMINI_API_KEY (仅注入该账户环境，留空则保持原密钥)
                    </label>
                    <Input
                      id="acc-apikey"
                      type="password"
                      value={accountForm.api_key}
                      onChange={(v) => setAccountForm({ ...accountForm, api_key: v })}
                      placeholder={isEditingAccount ? '留空保持不变' : 'AIzaSy...'}
                      autoComplete="new-password"
                    />
                  </div>
                )}

                <div className="flex flex-col gap-1">
                  <label className="text-xs font-medium text-foreground">网络隔离与出口模式</label>
                  <div>
                    <SelectField
                      value={accountForm.network_mode}
                      onValueChange={(v) => setAccountForm({ ...accountForm, network_mode: v })}
                      options={[
                        { value: 'direct', label: 'Direct 直连' },
                        { value: 'proxy', label: 'SOCKS5 / HTTP 代理' },
                        { value: 'bind_ip', label: '绑定源网卡 IP' },
                        { value: 'netns', label: 'Linux NetNS' },
                      ]}
                    />
                  </div>
                </div>

                {accountForm.network_mode === 'proxy' && (
                  <div className="flex flex-col gap-1">
                    <label htmlFor="acc-proxy" className="text-xs font-medium text-foreground">
                      代理地址 (socks5:// 或 http://)
                    </label>
                    <Input
                      id="acc-proxy"
                      value={accountForm.proxy_url}
                      onChange={(v) => setAccountForm({ ...accountForm, proxy_url: v })}
                      placeholder="socks5://user:pass@127.0.0.1:1080"
                    />
                  </div>
                )}

                {(accountForm.network_mode === 'bind_ip' ||
                  accountForm.network_mode === 'netns') && (
                  <div className="flex flex-col gap-1">
                    <label htmlFor="acc-bindip" className="text-xs font-medium text-foreground">
                      {accountForm.network_mode === 'netns' ? 'Linux NetNS 名称' : '源网卡 IP 地址'}
                    </label>
                    <Input
                      id="acc-bindip"
                      value={
                        accountForm.network_mode === 'netns'
                          ? accountForm.netns_name
                          : accountForm.bind_ip
                      }
                      onChange={(v) =>
                        accountForm.network_mode === 'netns'
                          ? setAccountForm({ ...accountForm, netns_name: v })
                          : setAccountForm({ ...accountForm, bind_ip: v })
                      }
                      placeholder={
                        accountForm.network_mode === 'netns' ? 'netns-g1' : '192.168.1.50'
                      }
                    />
                  </div>
                )}

                <div className="grid grid-cols-2 gap-3">
                  <div className="flex flex-col gap-1">
                    <label htmlFor="acc-workers" className="text-xs font-medium text-foreground">
                      最大子进程并发槽位
                    </label>
                    <Input
                      id="acc-workers"
                      inputMode="numeric"
                      value={accountForm.max_workers}
                      invalid={
                        accountForm.max_workers !== '' &&
                        parsePositiveInt(accountForm.max_workers) === null
                      }
                      onChange={(v) => setAccountForm({ ...accountForm, max_workers: v })}
                    />
                  </div>
                  <div className="flex flex-col gap-1">
                    <label htmlFor="acc-weight" className="text-xs font-medium text-foreground">
                      负载均衡调度权重
                    </label>
                    <Input
                      id="acc-weight"
                      inputMode="numeric"
                      value={accountForm.weight}
                      invalid={
                        accountForm.weight !== '' && parsePositiveInt(accountForm.weight) === null
                      }
                      onChange={(v) => setAccountForm({ ...accountForm, weight: v })}
                    />
                  </div>
                </div>
              </DialogModal>

              {/* Modal Dialog for Per-Account ACP Real-time Models & Disable Controls */}
              {(() => {
                const modalSnap = accounts.find((s) => s.account.id === modelModalAccountId)
                const modalAcc = modalSnap?.account
                const accModels =
                  modalSnap?.models && modalSnap.models.length > 0
                    ? modalSnap.models
                    : discoveredModels
                const disabledSet = new Set(
                  (modalAcc?.disabled_models || []).map((m) => m.trim().toLowerCase())
                )
                const enabledCount = accModels.filter(
                  (m) => !disabledSet.has(m.modelId.toLowerCase())
                ).length
                const disabledCount = accModels.length - enabledCount
                const isRefreshingAccModels = modalAcc
                  ? Boolean(refreshingAccountModels[modalAcc.id])
                  : false
                const accModelErr = modalAcc ? accountModelsError[modalAcc.id] || '' : ''
                const cachedAtText = formatRelativeAgo(
                  modalSnap?.models_cached_at || overview?.models_cached_at
                )

                return (
                  <DialogModal
                    open={Boolean(modelModalAccountId && modalAcc)}
                    onClose={closeModelModal}
                    maxWidthClass="max-w-xl"
                    translateTitle={false}
                    title={
                      modalAcc
                        ? `ACP 后端实时可用模型 — ${modalAcc.id}${
                            modalAcc.name && modalAcc.name !== modalAcc.id
                              ? ` (${modalAcc.name})`
                              : ''
                          }`
                        : 'ACP 后端实时可用模型'
                    }
                    description="独立控制该账户允许调度的 ACP 模型；禁用后，北向请求在负载均衡时将自动跳过该账户"
                    footer={
                      <>
                        <span className="text-xs text-muted-foreground">
                          点击模型行或右侧开关即可实时切换启用/禁用状态
                        </span>
                        <div className="flex items-center gap-2">
                          {modalAcc && disabledCount > 0 && (
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={() => void handleEnableAllAccountModels(modalAcc)}
                            >
                              全部恢复启用
                            </Button>
                          )}
                          <Button size="sm" variant="outline" onClick={closeModelModal}>
                            关闭
                          </Button>
                        </div>
                      </>
                    }
                  >
                    {modalAcc && (
                      <div className="flex flex-col gap-3">
                        {/* Top status & refresh bar */}
                        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-border bg-muted/20 px-3 py-2.5">
                          <div className="flex flex-wrap items-center gap-2 text-xs">
                            <Cpu className="size-3.5 text-primary shrink-0" />
                            <Badge
                              variant={
                                accModels.length === 0
                                  ? 'warning-light'
                                  : enabledCount === 0
                                    ? 'destructive-light'
                                    : 'success-light'
                              }
                            >
                              {accModels.length > 0
                                ? `已启用 ${enabledCount} / ${accModels.length}`
                                : '待拉取'}
                            </Badge>
                            {disabledCount > 0 && (
                              <Badge variant="destructive-light">{`已禁用 ${disabledCount}`}</Badge>
                            )}
                            <span className="text-muted-foreground tabular-nums">
                              {`${cachedAtText}同步`}
                            </span>
                          </div>

                          <Button
                            size="xs"
                            variant="outline"
                            disabled={isRefreshingAccModels}
                            onClick={() => void handleRefreshAccountModels(modalAcc.id)}
                          >
                            <RefreshCw />
                            {isRefreshingAccModels ? '正在从 ACP 拉取...' : '刷新模型'}
                          </Button>
                        </div>

                        {/* Error / Skeleton / List / Empty states */}
                        {modelModalError && (
                          <Alert variant="destructive">
                            <div className="flex-1">
                              <AlertTitle>更新模型启用状态失败</AlertTitle>
                              <AlertDescription>{modelModalError}</AlertDescription>
                            </div>
                          </Alert>
                        )}
                        {accModelErr && (
                          <Alert variant="destructive">
                            <div className="flex items-center justify-between gap-3 w-full">
                              <div className="flex-1 min-w-0">
                                <AlertTitle>拉取账户 ACP 模型失败</AlertTitle>
                                <AlertDescription>{accModelErr}</AlertDescription>
                              </div>
                              <Button
                                size="xs"
                                variant="outline"
                                onClick={() => void handleRefreshAccountModels(modalAcc.id)}
                              >
                                重试
                              </Button>
                            </div>
                          </Alert>
                        )}
                        {isRefreshingAccModels && accModels.length === 0 ? (
                          <div className="flex flex-col gap-2">
                            <Skeleton className="h-11 w-full" />
                            <Skeleton className="h-11 w-full" />
                            <Skeleton className="h-11 w-full" />
                            <Skeleton className="h-11 w-full" />
                          </div>
                        ) : accModels.length > 0 ? (
                          <div className="flex flex-col gap-2">
                            {accModels.map((m) => {
                              const isDisabled = disabledSet.has(m.modelId.toLowerCase())
                              const isToggling = Boolean(
                                togglingAccountModel[`${modalAcc.id}:${m.modelId}`]
                              )
                              const rawLevels =
                                m.thinkingLevels && m.thinkingLevels.length > 0
                                  ? m.thinkingLevels
                                  : m.thinking_levels && m.thinking_levels.length > 0
                                    ? m.thinking_levels
                                    : []
                              const defaultLevel =
                                m.defaultThinkingLevel || m.default_thinking_level || ''
                              return (
                                <div
                                  key={m.modelId}
                                  role="button"
                                  tabIndex={0}
                                  onClick={() => {
                                    if (!isToggling) {
                                      void handleToggleAccountModelDisabled(modalAcc, m.modelId)
                                    }
                                  }}
                                  onKeyDown={(e) => {
                                    if ((e.key === 'Enter' || e.key === ' ') && !isToggling) {
                                      e.preventDefault()
                                      void handleToggleAccountModelDisabled(modalAcc, m.modelId)
                                    }
                                  }}
                                  className={`flex items-center justify-between gap-3 rounded-lg border px-3.5 py-2.5 transition-colors cursor-pointer ${
                                    isDisabled
                                      ? 'border-destructive/35 bg-destructive/8 text-muted-foreground hover:bg-destructive/12'
                                      : 'border-border bg-card text-foreground hover:bg-accent/50'
                                  }`}
                                >
                                  <div className="flex items-start gap-2.5 min-w-0">
                                    <span
                                      aria-hidden="true"
                                      className={`mt-1.5 size-2 rounded-full shrink-0 ${
                                        isDisabled ? 'bg-destructive' : 'bg-success'
                                      }`}
                                    />
                                    <div className="flex flex-col gap-1.5 min-w-0">
                                      <div className="flex flex-wrap items-center gap-2">
                                        <span
                                          className={`font-mono text-sm ${
                                            isDisabled
                                              ? 'line-through text-muted-foreground'
                                              : 'font-semibold text-foreground'
                                          }`}
                                        >
                                          {m.modelId}
                                        </span>
                                        {m.name && m.name !== m.modelId && (
                                          <span className="text-xs text-muted-foreground">
                                            ({m.name})
                                          </span>
                                        )}
                                      </div>

                                      <div className="flex flex-wrap items-center gap-1.5">
                                        <span className="text-xs text-muted-foreground">
                                          思考强度:
                                        </span>
                                        {rawLevels.length === 0 && (
                                          <span className="text-xs text-muted-foreground">—</span>
                                        )}
                                        {rawLevels.map((lvl) => {
                                          const isDefault =
                                            lvl.toLowerCase() === defaultLevel.toLowerCase()
                                          return (
                                            <Badge
                                              key={lvl}
                                              variant={
                                                isDisabled
                                                  ? 'outline'
                                                  : isDefault
                                                    ? 'info-light'
                                                    : 'outline'
                                              }
                                            >
                                              {`${lvl}${isDefault ? ' · 默认' : ''}`}
                                            </Badge>
                                          )
                                        })}
                                      </div>

                                      {m.description && m.description !== m.name && (
                                        <span className="text-xs text-muted-foreground truncate">
                                          {m.description}
                                        </span>
                                      )}
                                    </div>
                                  </div>

                                  <div
                                    className="flex items-center gap-2.5 shrink-0"
                                    onClick={(e) => e.stopPropagation()}
                                    onKeyDown={(e) => e.stopPropagation()}
                                  >
                                    <Badge
                                      variant={isDisabled ? 'destructive-light' : 'success-light'}
                                    >
                                      {isDisabled ? '已禁用' : '启用中'}
                                    </Badge>
                                    <Switch
                                      checked={!isDisabled}
                                      onCheckedChange={() => {
                                        if (!isToggling) {
                                          void handleToggleAccountModelDisabled(modalAcc, m.modelId)
                                        }
                                      }}
                                      ariaLabel={`在账户 ${modalAcc.id} 下启用或禁用模型 ${m.modelId}`}
                                    />
                                  </div>
                                </div>
                              )
                            })}
                          </div>
                        ) : (
                          <div className="flex items-center justify-between gap-3 rounded-lg border border-dashed border-border bg-muted/15 px-3.5 py-4 text-xs text-muted-foreground">
                            <div className="flex flex-col gap-0.5">
                              <span className="font-medium text-foreground">
                                当前尚未缓存该账户的 ACP 模型列表
                              </span>
                              <span>
                                点击右侧按钮从该账户的 <code className="font-mono">agy acp</code> 实例实时拉取可用模型
                              </span>
                            </div>
                            <Button
                              size="xs"
                              variant="default"
                              disabled={isRefreshingAccModels}
                              onClick={() => void handleRefreshAccountModels(modalAcc.id)}
                            >
                              <RefreshCw />
                              立即探测
                            </Button>
                          </div>
                        )}
                      </div>
                    )}
                  </DialogModal>
                )
              })()}

              {/* Account Cards Grid (compact 3-column on desktop) vs Table View */}
              {accountViewMode === 'cards' ? (
                accounts.length > 0 ? (
                  <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
                    {accounts.map((snap) => renderAccountQuotaCard(snap))}
                  </div>
                ) : !overview && (loading || !loadError) ? (
                  <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
                    <Skeleton className="h-64 w-full" />
                    <Skeleton className="h-64 w-full" />
                    <Skeleton className="h-64 w-full" />
                  </div>
                ) : (
                  <Frame>
                    <FramePanel className="flex flex-col items-center gap-3 py-10 text-center">
                      <IconTile variant="outline">
                        <Layers />
                      </IconTile>
                      <div className="flex flex-col gap-1">
                        <div className="text-sm font-medium text-foreground">尚未配置任何账户</div>
                        <div className="text-xs text-muted-foreground">
                          新增一个隔离账户后，即可在此查看额度、出口 IP 与运维操作
                        </div>
                      </div>
                      <Button size="sm" onClick={openCreateAccountEditor}>
                        <Plus />
                        新增隔离账户
                      </Button>
                    </FramePanel>
                  </Frame>
                )
              ) : (
                <Frame>
                  <FrameHeader>
                    <FrameTitle>隔离账户列表视图</FrameTitle>
                  </FrameHeader>
                  <FramePanel className="p-0 overflow-hidden">
                    <DataGridView
                      columns={accountColumns}
                      rows={accounts}
                      getRowId={(r) => r.account.id}
                      emptyTitle="尚未配置任何账户"
                      emptyDescription="新增一个隔离账户后即可在此管理"
                      emptyAction={
                        <Button size="sm" onClick={openCreateAccountEditor}>
                          <Plus />
                          新增隔离账户
                        </Button>
                      }
                    />
                  </FramePanel>
                </Frame>
              )}
            </div>
          )}

          {/* TAB 3: REMOTE CLIENT ACCESS — 3 SESSION PROTOCOLS (OPENAI OFFICIAL / OPENAI COMPAT / CLAUDE) */}
          {activeTab === 'clients' && (
            <div className="flex flex-col gap-6">
              {/* Section 1: Interactive Parameter Workbench & Live ACP Model Discovery */}
              <Frame>
                <FrameHeader>
                  <div className="flex items-center gap-2.5">
                    <IconTile variant="outline">
                      <FolderGit2 />
                    </IconTile>
                    <div>
                      <FrameTitle>远程客户端三大会话协议接入中心</FrameTitle>
                      <FrameDescription>
                        统一支持 OpenAI 官方协议 (<code className="font-mono">/v1/responses</code>)、OpenAI 兼容协议 (<code className="font-mono">/v1/chat/completions</code>) 与 Claude 协议 (<code className="font-mono">/v1/messages</code>)，参数修改后实时联动下方接入代码
                      </FrameDescription>
                    </div>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <Button
                      size="xs"
                      variant="outline"
                      onClick={() => {
                        setClientSessionId(`sess-${Math.random().toString(36).slice(2, 8)}`)
                      }}
                    >
                      随机生成会话 ID
                    </Button>
                    <Button
                      size="xs"
                      variant="secondary"
                      onClick={() => {
                        setActiveTab('keys')
                        openCreateAPIKeyModal()
                      }}
                    >
                      <KeyRound />
                      签发新 API Key
                    </Button>
                  </div>
                </FrameHeader>

                <FramePanel className="flex flex-col gap-4">
                  {/* 4-Column Smart Parameter Controls */}
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
                    {/* 1. Client API Key with Quick Key Selector */}
                    <div className="rounded-lg border border-border bg-muted/20 p-3 flex flex-col justify-between gap-2">
                      <div className="flex items-center justify-between gap-1.5">
                        <label
                          htmlFor="client-token-preview"
                          className="text-xs font-medium text-foreground"
                        >
                          1. 客户端 API Key
                        </label>
                        {issuedPlainKey ? (
                          <Button
                            size="xs"
                            variant="ghost"
                            onClick={() => setClientTokenPreview(issuedPlainKey)}
                          >
                            填入新签发密钥
                          </Button>
                        ) : (
                          <Badge variant="outline">鉴权凭证</Badge>
                        )}
                      </div>
                      <Input
                        id="client-token-preview"
                        value={clientTokenPreview}
                        onChange={setClientTokenPreview}
                        placeholder="sk-ag-..."
                        className="font-mono"
                      />
                      {apiKeys.length > 0 ? (
                        <div className="flex flex-col gap-1.5">
                          <select
                            aria-label="选择已签发密钥联动参数"
                            value={clientSelectedKeyId}
                            onChange={(e) => {
                              const nextId = e.target.value
                              setClientSelectedKeyId(nextId)
                              const found = apiKeys.find((k) => String(k.id) === nextId)
                              if (found) {
                                if (found.bound_account_id) {
                                  setClientAccountBind(found.bound_account_id)
                                } else {
                                  setClientAccountBind('auto')
                                }
                              }
                            }}
                            className="h-7 w-full rounded-md border border-input bg-background px-2 text-xs text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <option value="">{`关联已签发密钥策略 (共 ${apiKeys.length} 个)...`}</option>
                            {apiKeys.map((k) => (
                              <option key={k.id} value={String(k.id)}>
                                {k.name} ({k.key_prefix} · {formatPromptModeLabel(k.prompt_mode)})
                              </option>
                            ))}
                          </select>
                          {(() => {
                            const matchedKey = apiKeys.find((k) => String(k.id) === clientSelectedKeyId)
                            if (!matchedKey) return null
                            return (
                              <div className="flex flex-wrap items-center gap-1">
                                <Badge variant="info-light">
                                  {formatPromptModeLabel(matchedKey.prompt_mode)}
                                </Badge>
                                <Badge variant="outline">
                                  {formatSessionModeLabel(matchedKey.session_mode)}
                                </Badge>
                              </div>
                            )
                          })()}
                        </div>
                      ) : (
                        <span className="text-xs text-muted-foreground">
                          填入签发的 <code className="font-mono">sk-ag-*</code> 密钥以生成鉴权头
                        </span>
                      )}
                    </div>

                    {/* 2. Target Model Identifier */}
                    <div className="rounded-lg border border-border bg-muted/20 p-3 flex flex-col justify-between gap-2">
                      <div className="flex items-center justify-between gap-1.5">
                        <label
                          htmlFor="client-model-input"
                          className="text-xs font-medium text-foreground"
                        >
                          2. 目标模型标识 (model)
                        </label>
                        <Button
                          size="xs"
                          variant="ghost"
                          onClick={() => void copyClientText('model-input', effectiveClientModel)}
                        >
                          {copyLabel('model-input', '复制模型 ID')}
                        </Button>
                      </div>
                      <Input
                        id="client-model-input"
                        value={clientModelPreview}
                        onChange={setClientModelPreview}
                        placeholder={enabledClientModels[0]?.modelId || '选择下方已启用模型或手动输入'}
                        className="font-mono"
                      />
                      {enabledClientModels.length > 0 ? (
                        <select
                          aria-label="从账户可用模型中选择"
                          value={
                            enabledClientModels.some((m) => m.modelId === effectiveClientModel)
                              ? effectiveClientModel
                              : ''
                          }
                          onChange={(e) => {
                            if (e.target.value) {
                              setClientModelPreview(e.target.value)
                            }
                          }}
                          className="h-7 w-full rounded-md border border-input bg-background px-2 font-mono text-xs text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          <option value="" disabled>
                            {`切换可用模型 (${enabledClientModels.length} 个未禁用)...`}
                          </option>
                          {enabledClientModels.map((m) => (
                            <option key={m.modelId} value={m.modelId}>
                              {m.modelId}
                              {m.name && m.name !== m.modelId ? ` · ${m.name}` : ''}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <div className="flex items-center justify-between gap-1 text-xs text-muted-foreground">
                          <span className="truncate">
                            当前生效: <code className="font-mono text-foreground">{effectiveClientModel}</code>
                          </span>
                        </div>
                      )}
                    </div>

                    {/* 3. Account Binding (X-ACP-Account-ID) */}
                    <div className="rounded-lg border border-border bg-muted/20 p-3 flex flex-col justify-between gap-2">
                      <div className="flex items-center justify-between gap-1.5">
                        <label
                          htmlFor="client-account-bind"
                          className="text-xs font-medium text-foreground"
                        >
                          3. 调度账户 (X-ACP-Account-ID)
                        </label>
                        <Badge variant={clientAccountBind === 'auto' ? 'success-light' : 'info-light'}>
                          {clientAccountBind === 'auto' ? '自动负载均衡' : `定点: ${clientAccountBind}`}
                        </Badge>
                      </div>
                      <select
                        id="client-account-bind"
                        value={
                          clientAccountBind === 'auto' ||
                          accounts.some((a) => a.account.id === clientAccountBind)
                            ? clientAccountBind
                            : '__custom__'
                        }
                        onChange={(e) => {
                          if (e.target.value !== '__custom__') {
                            setClientAccountBind(e.target.value)
                          }
                        }}
                        className="h-8 w-full rounded-md border border-input bg-background px-2.5 text-xs text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <option value="auto">auto (按权重与健康度自动调度)</option>
                        {accounts.map((snap) => (
                          <option key={snap.account.id} value={snap.account.id}>
                            {snap.account.id} — {snap.account.name} ({snap.account.status})
                          </option>
                        ))}
                      </select>
                      <span className="text-xs text-muted-foreground">
                        通过请求头强制指定隔离账户或保持 <code className="font-mono">auto</code> 故障转移
                      </span>
                    </div>

                    {/* 4. Multi-turn Session Affinity (X-ACP-Session-ID) */}
                    <div className="rounded-lg border border-border bg-muted/20 p-3 flex flex-col justify-between gap-2">
                      <div className="flex items-center justify-between gap-1.5">
                        <label
                          htmlFor="client-session-id"
                          className="text-xs font-medium text-foreground"
                        >
                          4. 会话亲和标识 (X-ACP-Session-ID)
                        </label>
                        <Badge variant="outline">多轮上下文复用</Badge>
                      </div>
                      <Input
                        id="client-session-id"
                        value={clientSessionId}
                        onChange={setClientSessionId}
                        placeholder="sess-dev-01"
                        className="font-mono"
                      />
                      <span className="text-xs text-muted-foreground">
                        相同 Session ID 自动路由至同一空闲 <code className="font-mono">agy acp</code> 会话
                      </span>
                    </div>
                  </div>
                </FramePanel>
              </Frame>

              {/* Section 2: 3 Session Protocol Selector Cards */}
              <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
                {/* Protocol 1: OpenAI Official Protocol */}
                <div
                  role="button"
                  tabIndex={0}
                  onClick={() => setClientProtocolTab('openai_official')}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      setClientProtocolTab('openai_official')
                    }
                  }}
                  className={`cursor-pointer text-start rounded-xl border p-4 flex flex-col justify-between gap-3 transition-colors duration-150 ${
                    clientProtocolTab === 'openai_official'
                      ? 'border-foreground bg-card shadow-2xs'
                      : 'border-border bg-card/60 hover:bg-card'
                  }`}
                >
                  <div className="flex flex-col gap-2">
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-semibold text-foreground">
                          1. OpenAI 官方协议
                        </span>
                        {clientProtocolTab === 'openai_official' && (
                          <Badge variant="info-light">当前选中</Badge>
                        )}
                      </div>
                      <Badge variant={clientProtocolTab === 'openai_official' ? 'default' : 'outline'}>
                        /v1/responses
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground leading-relaxed">
                      适用 OpenAI Codex CLI (<code className="font-mono">wire_api=&quot;responses&quot;</code>) 与官方 SDK，原生支持 <code className="font-mono">previous_response_id</code> 多轮状态续接与推理强度控制。
                    </p>
                  </div>

                  <div className="flex flex-col gap-2 pt-2 border-t border-border/60">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge variant="outline">Codex CLI</Badge>
                      <Badge variant="outline">OpenAI Responses SDK</Badge>
                      <Badge variant="outline">Bearer 鉴权</Badge>
                    </div>
                    <div className="flex items-center justify-between gap-2 rounded-md bg-muted/40 px-2.5 py-1.5">
                      <code className="font-mono text-xs text-foreground truncate">
                        POST {gatewayBaseUrl}/v1/responses
                      </code>
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => {
                          copyClientText('card-ep-responses', `${gatewayBaseUrl}/v1/responses`)
                        }}
                      >
                        {copyLabel('card-ep-responses', '复制')}
                      </Button>
                    </div>
                  </div>
                </div>

                {/* Protocol 2: OpenAI Compatible Protocol */}
                <div
                  role="button"
                  tabIndex={0}
                  onClick={() => setClientProtocolTab('openai_compat')}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      setClientProtocolTab('openai_compat')
                    }
                  }}
                  className={`cursor-pointer text-start rounded-xl border p-4 flex flex-col justify-between gap-3 transition-colors duration-150 ${
                    clientProtocolTab === 'openai_compat'
                      ? 'border-foreground bg-card shadow-2xs'
                      : 'border-border bg-card/60 hover:bg-card'
                  }`}
                >
                  <div className="flex flex-col gap-2">
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-semibold text-foreground">
                          2. OpenAI 兼容协议
                        </span>
                        {clientProtocolTab === 'openai_compat' && (
                          <Badge variant="success-light">当前选中</Badge>
                        )}
                      </div>
                      <Badge variant={clientProtocolTab === 'openai_compat' ? 'default' : 'outline'}>
                        /v1/chat/completions
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground leading-relaxed">
                      适用 Cursor、Cline、Roo Code、Continue、Cherry Studio 等生态客户端，支持自动调用 <code className="font-mono">GET /v1/models</code> 枚举实时模型。
                    </p>
                  </div>

                  <div className="flex flex-col gap-2 pt-2 border-t border-border/60">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge variant="outline">Cursor / Cline</Badge>
                      <Badge variant="outline">Cherry Studio</Badge>
                      <Badge variant="outline">Bearer 鉴权</Badge>
                    </div>
                    <div className="flex items-center justify-between gap-2 rounded-md bg-muted/40 px-2.5 py-1.5">
                      <code className="font-mono text-xs text-foreground truncate">
                        POST {gatewayBaseUrl}/v1/chat/completions
                      </code>
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => {
                          copyClientText('card-ep-chat', `${gatewayBaseUrl}/v1/chat/completions`)
                        }}
                      >
                        {copyLabel('card-ep-chat', '复制')}
                      </Button>
                    </div>
                  </div>
                </div>

                {/* Protocol 3: Claude Protocol */}
                <div
                  role="button"
                  tabIndex={0}
                  onClick={() => setClientProtocolTab('claude')}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault()
                      setClientProtocolTab('claude')
                    }
                  }}
                  className={`cursor-pointer text-start rounded-xl border p-4 flex flex-col justify-between gap-3 transition-colors duration-150 ${
                    clientProtocolTab === 'claude'
                      ? 'border-foreground bg-card shadow-2xs'
                      : 'border-border bg-card/60 hover:bg-card'
                  }`}
                >
                  <div className="flex flex-col gap-2">
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-semibold text-foreground">
                          3. Claude 原生协议
                        </span>
                        {clientProtocolTab === 'claude' && (
                          <Badge variant="warning-light">当前选中</Badge>
                        )}
                      </div>
                      <Badge variant={clientProtocolTab === 'claude' ? 'default' : 'outline'}>
                        /v1/messages
                      </Badge>
                    </div>
                    <p className="text-xs text-muted-foreground leading-relaxed">
                      适用 Claude Code CLI (<code className="font-mono">ANTHROPIC_BASE_URL</code>) 与 Anthropic SDK，支持 <code className="font-mono">thinking</code> 思维链块与多轮工具调用恢复。
                    </p>
                  </div>

                  <div className="flex flex-col gap-2 pt-2 border-t border-border/60">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <Badge variant="outline">Claude Code CLI</Badge>
                      <Badge variant="outline">Anthropic SDK</Badge>
                      <Badge variant="outline">x-api-key 鉴权</Badge>
                    </div>
                    <div className="flex items-center justify-between gap-2 rounded-md bg-muted/40 px-2.5 py-1.5">
                      <code className="font-mono text-xs text-foreground truncate">
                        POST {gatewayBaseUrl}/v1/messages
                      </code>
                      <Button
                        size="xs"
                        variant="ghost"
                        onClick={() => {
                          copyClientText('card-ep-claude', `${gatewayBaseUrl}/v1/messages`)
                        }}
                      >
                        {copyLabel('card-ep-claude', '复制')}
                      </Button>
                    </div>
                  </div>
                </div>
              </div>

              {/* Section 3: Protocol Quick-Copy Parameters & Code Generator Frame */}
              <Frame>
                <FrameHeader>
                  <div>
                    <FrameTitle>
                      {clientProtocolTab === 'openai_official' &&
                        'OpenAI 官方协议 (/v1/responses) · 接入参数与代码模板'}
                      {clientProtocolTab === 'openai_compat' &&
                        'OpenAI 兼容协议 (/v1/chat/completions) · 接入参数与代码模板'}
                      {clientProtocolTab === 'claude' &&
                        'Claude 原生协议 (/v1/messages) · 接入参数与代码模板'}
                    </FrameTitle>
                    <FrameDescription>
                      支持一键复制客户端配置项，或按场景筛选 CLI、Python SDK、cURL 与模型发现代码
                    </FrameDescription>
                  </div>

                  <SelectField
                    value={clientSnippetFilter}
                    onValueChange={(v) => setClientSnippetFilter(v as ClientSnippetFilter)}
                    options={[
                      { value: 'all', label: '全部示例 (折叠列表)' },
                      { value: 'cli', label: '客户端 / CLI 配置' },
                      { value: 'sdk', label: 'Python SDK' },
                      { value: 'curl', label: 'cURL 流式调试' },
                      { value: 'models', label: '模型枚举 (/v1/models)' },
                    ]}
                  />
                </FrameHeader>

                <FramePanel className="flex flex-col gap-4">
                  {/* Protocol Specific Quick-Copy Parameter Strip */}
                  {(() => {
                    const baseUrlVal =
                      clientProtocolTab === 'claude' ? gatewayBaseUrl : `${gatewayBaseUrl}/v1`
                    const endpointVal =
                      clientProtocolTab === 'openai_official'
                        ? `${gatewayBaseUrl}/v1/responses`
                        : clientProtocolTab === 'openai_compat'
                          ? `${gatewayBaseUrl}/v1/chat/completions`
                          : `${gatewayBaseUrl}/v1/messages`
                    const authHeaderVal =
                      clientProtocolTab === 'claude'
                        ? `x-api-key: ${clientTokenPreview}`
                        : `Authorization: Bearer ${clientTokenPreview}`

                    const quickItems = [
                      {
                        key: 'quick-base',
                        label:
                          clientProtocolTab === 'claude'
                            ? 'Anthropic Base URL (无需 /v1)'
                            : 'OpenAI Base URL (含 /v1)',
                        value: baseUrlVal,
                      },
                      {
                        key: 'quick-endpoint',
                        label: '会话请求端点 (POST)',
                        value: endpointVal,
                      },
                      {
                        key: 'quick-auth',
                        label: '鉴权请求头 (Header)',
                        value: authHeaderVal,
                      },
                      {
                        key: 'quick-model',
                        label: '模型标识 (Model ID)',
                        value: effectiveClientModel,
                      },
                    ]

                    return (
                      <div className="grid grid-cols-1 gap-2.5 sm:grid-cols-2 lg:grid-cols-4">
                        {quickItems.map((item) => (
                          <div
                            key={item.key}
                            className="rounded-lg border border-border bg-muted/20 px-3 py-2 flex flex-col gap-1"
                          >
                            <div className="flex items-center justify-between gap-1.5">
                              <span className="text-xs text-muted-foreground">{item.label}</span>
                              <Button
                                size="xs"
                                variant="ghost"
                                onClick={() => void copyClientText(item.key, item.value)}
                              >
                                {copyLabel(item.key, '复制')}
                              </Button>
                            </div>
                            <code className="font-mono text-xs font-medium text-foreground truncate">
                              {item.value}
                            </code>
                          </div>
                        ))}
                      </div>
                    )
                  })()}

                  {/* Protocol 1 Details: OpenAI Official Protocol (/v1/responses) */}
                  {clientProtocolTab === 'openai_official' && (
                    <div className="flex flex-col gap-2.5">
                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'cli') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="1. OpenAI Codex CLI 配置 (~/.codex/config.toml · 官方 Responses 协议)"
                          language="toml"
                          code={`model = "${effectiveClientModel}"
model_provider = "antigravity"

[model_providers.antigravity]
name = "Antigravity ACP Gateway"
base_url = "${gatewayBaseUrl}/v1"
env_key = "ANTIGRAVITY_API_KEY"
wire_api = "responses"

# 终端导出密钥后直接运行 codex:
# export ANTIGRAVITY_API_KEY="${clientTokenPreview}"`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'sdk') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="2. OpenAI 官方 Python SDK (动态获取 ACP 模型 + Responses 多轮续接)"
                          language="python"
                          code={`from openai import OpenAI

client = OpenAI(
    base_url="${gatewayBaseUrl}/v1",
    api_key="${clientTokenPreview}",
    default_headers={
        "X-ACP-Account-ID": "${clientAccountBind}",
        "X-ACP-Session-ID": "${clientSessionId}",
    },
)

# 实时从 ACP 后端获取可用模型（缓存有效期内直接命中内存缓存）
models = [m.id for m in client.models.list().data]
print("ACP Available Models:", models)

# 第一轮请求
resp1 = client.responses.create(
    model="${effectiveClientModel}",
    instructions="你是一个严谨的代码架构助手。",
    reasoning={"effort": "high"},
    input="请分析当前仓库的模块结构。",
)
print(resp1.output_text)

# 第二轮直接通过 previous_response_id 续接同一 agy acp 会话
resp2 = client.responses.create(
    model="${effectiveClientModel}",
    previous_response_id=resp1.id,
    input="基于刚才的分析，指出核心路由入口文件。",
)
print(resp2.output_text)`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'curl') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="3. cURL 流式调用 OpenAI 官方协议 (POST /v1/responses)"
                          language="bash"
                          code={`curl -N "${gatewayBaseUrl}/v1/responses" \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${clientTokenPreview}" \\
  -H "X-ACP-Account-ID: ${clientAccountBind}" \\
  -H "X-ACP-Session-ID: ${clientSessionId}" \\
  -d '{
    "model": "${effectiveClientModel}",
    "stream": true,
    "instructions": "请用中文简洁回答。",
    "reasoning": { "effort": "high" },
    "input": [
      {
        "role": "user",
        "content": [
          { "type": "input_text", "text": "你好，请验证 OpenAI 官方 Responses 会话协议。" }
        ]
      }
    ]
  }'`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'models') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="4. OpenAI 官方协议获取可用模型 & 强制刷新缓存 (GET /v1/models)"
                          language="bash"
                          code={`# 默认读取缓存（TTL 内零延迟，未命中或过期时实时向 agy acp 查询）：
curl "${gatewayBaseUrl}/v1/models" \\
  -H "Authorization: Bearer ${clientTokenPreview}"

# 附加 ?refresh=1 强制穿透缓存，实时从 ACP 后端重新拉取模型：
curl "${gatewayBaseUrl}/v1/models?refresh=1" \\
  -H "Authorization: Bearer ${clientTokenPreview}"`}
                        />
                      )}
                    </div>
                  )}

                  {/* Protocol 2 Details: OpenAI Compatible Protocol (/v1/chat/completions) */}
                  {clientProtocolTab === 'openai_compat' && (
                    <div className="flex flex-col gap-2.5">
                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'cli') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="1. Cursor / Cline / Roo Code / Cherry Studio 接入参数 (OpenAI 兼容)"
                          language="json"
                          code={JSON.stringify(
                            {
                              protocol: 'OpenAI Compatible (Chat Completions)',
                              base_url: `${gatewayBaseUrl}/v1`,
                              chat_completions_endpoint: `${gatewayBaseUrl}/v1/chat/completions`,
                              models_endpoint: `${gatewayBaseUrl}/v1/models`,
                              api_key: clientTokenPreview,
                              model: effectiveClientModel,
                              available_acp_models: discoveredModels.map((m) => m.modelId),
                              custom_headers: {
                                'X-ACP-Account-ID': clientAccountBind,
                                'X-ACP-Session-ID': clientSessionId,
                              },
                            },
                            null,
                            2
                          )}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'sdk') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="2. OpenAI 兼容 SDK 多轮对话示例 (chat.completions.create)"
                          language="python"
                          code={`from openai import OpenAI

client = OpenAI(
    base_url="${gatewayBaseUrl}/v1",
    api_key="${clientTokenPreview}",
    default_headers={
        "X-ACP-Account-ID": "${clientAccountBind}",
        "X-ACP-Session-ID": "${clientSessionId}",
    },
)

stream = client.chat.completions.create(
    model="${effectiveClientModel}",
    stream=True,
    reasoning_effort="high",
    messages=[
        {"role": "system", "content": "You are a helpful coding assistant."},
        {"role": "user", "content": "请总结多账户隔离池的工作方式。"},
    ],
)

for chunk in stream:
    delta = chunk.choices[0].delta
    if getattr(delta, "content", None):
        print(delta.content, end="", flush=True)`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'curl') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="3. cURL 流式调用 OpenAI 兼容协议 (POST /v1/chat/completions)"
                          language="bash"
                          code={`curl -N "${gatewayBaseUrl}/v1/chat/completions" \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${clientTokenPreview}" \\
  -H "X-ACP-Account-ID: ${clientAccountBind}" \\
  -H "X-ACP-Session-ID: ${clientSessionId}" \\
  -d '{
    "model": "${effectiveClientModel}",
    "stream": true,
    "reasoning_effort": "high",
    "messages": [
      { "role": "system", "content": "你是一个专业服务端工程师。" },
      { "role": "user", "content": "测试 OpenAI 兼容 Chat Completions 协议流式输出。" }
    ]
  }'`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'models') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="4. OpenAI 兼容协议获取模型列表与单模型详情 (GET /v1/models)"
                          language="bash"
                          code={`# 获取全部 ACP 可用模型列表 (带 TTL 缓存)：
curl "${gatewayBaseUrl}/v1/models" \\
  -H "Authorization: Bearer ${clientTokenPreview}"

# 查询单个模型详情：
curl "${gatewayBaseUrl}/v1/models/${effectiveClientModel}" \\
  -H "Authorization: Bearer ${clientTokenPreview}"`}
                        />
                      )}
                    </div>
                  )}

                  {/* Protocol 3 Details: Claude Protocol (/v1/messages) */}
                  {clientProtocolTab === 'claude' && (
                    <div className="flex flex-col gap-2.5">
                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'cli') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="1. Claude Code CLI 环境变量一键接入 (Anthropic Messages 协议)"
                          language="bash"
                          code={`# 在终端配置 Claude Code 直连当前 Antigravity 网关：
export ANTHROPIC_BASE_URL="${gatewayBaseUrl}"
export ANTHROPIC_AUTH_TOKEN="${clientTokenPreview}"
export ANTHROPIC_MODEL="${effectiveClientModel}"

# 启动 Claude Code CLI（支持完整多轮对话与本地工具调用）：
claude`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'sdk') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="2. Anthropic 官方 Python SDK (拉取 ACP 模型 + Messages 对话)"
                          language="python"
                          code={`import anthropic

client = anthropic.Anthropic(
    base_url="${gatewayBaseUrl}",
    api_key="${clientTokenPreview}",
    default_headers={
        "X-ACP-Account-ID": "${clientAccountBind}",
        "X-ACP-Session-ID": "${clientSessionId}",
    },
)

# 通过 Claude 协议 client.models.list() 获取 ACP 实时可用模型
models_page = client.models.list()
print("ACP Models via Claude protocol:", [m.id for m in models_page.data])

msg = client.messages.create(
    model="${effectiveClientModel}",
    max_tokens=4096,
    thinking={"type": "enabled", "budget_tokens": 16000},
    system="你是一个资深系统架构师。",
    messages=[
        {"role": "user", "content": "请检查当前项目的协议转换设计。"}
    ],
)
print(msg.content)`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'curl') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="3. cURL 流式调用 Claude 协议 (POST /v1/messages)"
                          language="bash"
                          code={`curl -N "${gatewayBaseUrl}/v1/messages" \\
  -H "Content-Type: application/json" \\
  -H "x-api-key: ${clientTokenPreview}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "X-ACP-Account-ID: ${clientAccountBind}" \\
  -H "X-ACP-Session-ID: ${clientSessionId}" \\
  -d '{
    "model": "${effectiveClientModel}",
    "stream": true,
    "thinking": { "type": "enabled", "budget_tokens": 16000 },
    "system": [
      { "type": "text", "text": "请用中文回答。" }
    ],
    "messages": [
      { "role": "user", "content": "你好，请验证 Claude /v1/messages 会话协议。" }
    ]
  }'`}
                        />
                      )}

                      {(clientSnippetFilter === 'all' || clientSnippetFilter === 'models') && (
                        <CodeBlock
                          translatable
                          collapsible
                          defaultOpen={false}
                          title="4. Claude 协议获取可用模型列表 (GET /v1/models · x-api-key 鉴权)"
                          language="bash"
                          code={`# 使用 Claude 协议标准请求头 (x-api-key + anthropic-version) 获取 ACP 可用模型：
curl "${gatewayBaseUrl}/v1/models" \\
  -H "x-api-key: ${clientTokenPreview}" \\
  -H "anthropic-version: 2023-06-01"

# 强制跳过缓存实时向 agy acp 重新探测可用模型：
curl "${gatewayBaseUrl}/v1/models?refresh=1" \\
  -H "x-api-key: ${clientTokenPreview}" \\
  -H "anthropic-version: 2023-06-01"`}
                        />
                      )}
                    </div>
                  )}
                </FramePanel>

                <FrameFooter>
                  <span>
                    每个客户端 API Key 均在北向入口独立生效其绑定的「系统提示词注入策略」与「ACP 会话复用模式」
                  </span>
                  <Button
                    size="xs"
                    variant="ghost"
                    onClick={() => setActiveTab('keys')}
                  >
                    前往管理 API Key 策略
                  </Button>
                </FrameFooter>
              </Frame>
            </div>
          )}

          {/* TAB 4: CLIENT API KEYS WITH PER-KEY PROMPT & SESSION POLICY */}
          {activeTab === 'keys' && (
            <div className="flex flex-col gap-4">
              {/* Top Action Header */}
              <Frame>
                <FrameHeader>
                  <div className="flex items-center gap-2.5">
                    <IconTile variant="elevated">
                      <KeyRound />
                    </IconTile>
                    <div>
                      <FrameTitle>客户端 API Key 与独立策略管理</FrameTitle>
                      <FrameDescription>
                        按密钥独立配置绑定隔离账户、RPM 限流、系统提示词注入策略与 ACP 会话复用模式
                      </FrameDescription>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button size="sm" onClick={openCreateAPIKeyModal}>
                      <Plus />
                      签发客户端 API Key
                    </Button>
                  </div>
                </FrameHeader>
              </Frame>

              {/* Newly Issued Plaintext Key Banner */}
              {issuedPlainKey && (
                <Frame>
                  <FrameHeader>
                    <div className="flex items-center gap-2">
                      <Badge variant="success-light">新签发密钥</Badge>
                      <FrameTitle>请立即复制保存客户端明文 API Key (仅显示一次)</FrameTitle>
                    </div>
                    <Button size="xs" variant="ghost" onClick={() => setIssuedPlainKey('')}>
                      已保存，关闭提示
                    </Button>
                  </FrameHeader>
                  <FramePanel>
                    <CodeBlock
                      translateTitle={false}
                      title="Client API Key (Bearer / x-api-key)"
                      language="text"
                      code={issuedPlainKey}
                    />
                  </FramePanel>
                </Frame>
              )}

              {/* Modal for Issuing New Client API Key with Independent Policy */}
              <DialogModal
                open={showCreateKeyModal}
                onClose={closeCreateAPIKeyModal}
                title="签发客户端 API Key 与独立策略配置"
                description="为接入客户端签发专属密钥，并独立设定账户路由、系统提示词注入模式与 ACP 会话复用策略"
                maxWidthClass="max-w-2xl"
                footer={
                  <>
                    <span className="text-xs text-muted-foreground">
                      签发后将生成一次性的 <code className="font-mono">sk-ag-...</code> 明文密钥
                    </span>
                    <div className="flex items-center gap-2">
                      <Button size="sm" variant="outline" onClick={closeCreateAPIKeyModal}>
                        取消
                      </Button>
                      <Button
                        size="sm"
                        disabled={creatingKey}
                        onClick={() => void handleCreateAPIKey()}
                      >
                        <KeyRound />
                        {creatingKey ? '正在签发...' : '签发密钥并保存策略'}
                      </Button>
                    </div>
                  </>
                }
              >
                <div className="flex flex-col gap-3.5">
                  {createKeyError && (
                    <Alert variant="destructive">
                      <div className="flex-1">
                        <AlertTitle>签发失败</AlertTitle>
                        <AlertDescription>{createKeyError}</AlertDescription>
                      </div>
                    </Alert>
                  )}
                  {/* Section 1: Basic Identity & Traffic Quota */}
                  <div className="rounded-lg border border-border bg-muted/20 p-3.5 flex flex-col gap-3">
                    <div className="flex items-center justify-between gap-2">
                      <div>
                        <div className="text-xs font-semibold text-foreground">
                          1. 基础标识与账户路由配额
                        </div>
                        <div className="text-xs text-muted-foreground">
                          设置密钥备注名称、每分钟请求速率限制与绑定的下游隔离账户
                        </div>
                      </div>
                      <Badge variant="outline">路由与限流</Badge>
                    </div>

                    <div className="grid grid-cols-1 gap-3 sm:grid-cols-12">
                      <div className="flex flex-col gap-1 sm:col-span-8">
                        <label htmlFor="key-name" className="text-xs font-medium text-foreground">
                          密钥备注名称
                        </label>
                        <Input
                          id="key-name"
                          autoFocus
                          value={newKeyName}
                          onChange={setNewKeyName}
                          placeholder="例如: MacBook-Zed-Pro / Cursor-Team / Claude-Code"
                        />
                      </div>
                      <div className="flex flex-col gap-1 sm:col-span-4">
                        <label htmlFor="key-rpm" className="text-xs font-medium text-foreground">
                          每分钟限流 (RPM)
                        </label>
                        <Input
                          id="key-rpm"
                          inputMode="numeric"
                          value={newKeyRPM}
                          invalid={newKeyRPM !== '' && parsePositiveInt(newKeyRPM) === null}
                          onChange={setNewKeyRPM}
                        />
                      </div>
                    </div>

                    <div className="flex flex-col gap-1.5">
                      <div className="flex items-center justify-between gap-2">
                        <label htmlFor="key-acc" className="text-xs font-medium text-foreground">
                          绑定隔离账户 ID
                        </label>
                        <span className="text-xs text-muted-foreground">
                          留空则按全池健康度自动负载均衡
                        </span>
                      </div>
                      <Input
                        id="key-acc"
                        value={newKeyAccount}
                        onChange={setNewKeyAccount}
                        placeholder="留空为全池自动负载均衡，或填写指定账户 ID (如 default)"
                      />
                      {accounts.length > 0 && (
                        <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
                          <span className="text-xs text-muted-foreground">快捷选择:</span>
                          <button
                            type="button"
                            onClick={() => setNewKeyAccount('')}
                            className={`rounded-md border px-2 py-0.5 text-xs transition-colors duration-150 ${
                              newKeyAccount.trim() === ''
                                ? 'border-primary bg-primary/10 font-medium text-foreground'
                                : 'border-border bg-card text-muted-foreground hover:text-foreground'
                            }`}
                          >
                            全池自动负载均衡
                          </button>
                          {accounts.map((snap) => {
                            const accId = snap.account.id
                            const selected = newKeyAccount.trim() === accId
                            return (
                              <button
                                key={accId}
                                type="button"
                                onClick={() => setNewKeyAccount(accId)}
                                className={`rounded-md border px-2 py-0.5 font-mono text-xs transition-colors duration-150 ${
                                  selected
                                    ? 'border-primary bg-primary/10 font-medium text-foreground'
                                    : 'border-border bg-card text-muted-foreground hover:text-foreground'
                                }`}
                              >
                                {accId}
                              </button>
                            )
                          })}
                        </div>
                      )}
                    </div>
                  </div>

                  {/* Section 2: System Prompt Policy */}
                  <div className="rounded-lg border border-border bg-muted/20 p-3.5 flex flex-col gap-3">
                    <div className="flex items-center justify-between gap-2">
                      <div>
                        <div className="text-xs font-semibold text-foreground">
                          2. 系统提示词注入策略 (System Prompt Policy)
                        </div>
                        <div className="text-xs text-muted-foreground">
                          控制该密钥向 <code className="font-mono">agy acp</code> 传递系统指令的方式与内置工具可见性
                        </div>
                      </div>
                      <Badge variant="info-light">Key 级独立生效</Badge>
                    </div>

                    <div role="radiogroup" aria-label="提示词注入模式" className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                      {[
                        {
                          value: 'pure_passthrough',
                          title: '纯透传 (Passthrough)',
                          tag: '屏蔽原生身份',
                          desc: '屏蔽 Antigravity 原生设定，100% 原样透传客户端 System Prompt',
                        },
                        {
                          value: 'prepend_global',
                          title: '全局前置指令',
                          tag: '屏蔽原生 + 前置',
                          desc: '屏蔽原生身份设定，在客户端 System Prompt 之前注入下方前置指令',
                        },
                        {
                          value: 'native_hybrid',
                          title: '原生混合 (Hybrid)',
                          tag: '保留原生设定',
                          desc: '保留 Antigravity 原生身份设定，并叠加下方前置指令与客户端提示词',
                        },
                      ].map((opt) => {
                        const active = newKeyPromptMode === opt.value
                        return (
                          <button
                            key={opt.value}
                            type="button"
                            role="radio"
                            aria-checked={active}
                            onClick={() => setNewKeyPromptMode(opt.value)}
                            className={`flex flex-col justify-between gap-1.5 rounded-lg border p-2.5 text-start transition-colors duration-150 ${
                              active
                                ? 'border-primary bg-card shadow-2xs'
                                : 'border-border bg-card/60 hover:bg-card'
                            }`}
                          >
                            <div className="flex items-center justify-between gap-1">
                              <span className="text-xs font-semibold text-foreground">
                                {opt.title}
                              </span>
                              <span
                                className={`size-2 shrink-0 rounded-full ${
                                  active ? 'bg-primary' : 'bg-muted-foreground/30'
                                }`}
                              />
                            </div>
                            <p className="text-xs text-muted-foreground leading-snug">{opt.desc}</p>
                            <div>
                              <Badge variant={active ? 'info-light' : 'outline'}>{opt.tag}</Badge>
                            </div>
                          </button>
                        )
                      })}
                    </div>

                    <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-card px-3 py-2">
                      <div>
                        <div className="text-xs font-medium text-foreground">
                          屏蔽 ACP 内置文件与终端工具 (Disable Built-in Tools)
                        </div>
                        <div className="text-xs text-muted-foreground">
                          开启后仅允许模型调用客户端请求体传入的自定义 Tools，防止内置工具抢占执行
                        </div>
                      </div>
                      <Switch
                        checked={newKeyDisableBuiltinTools}
                        onCheckedChange={setNewKeyDisableBuiltinTools}
                        ariaLabel="屏蔽 ACP 内置文件与终端工具"
                      />
                    </div>

                    <div className="flex flex-col gap-1">
                      <div className="flex items-center justify-between gap-2">
                        <label
                          htmlFor="key-prepend-prompt"
                          className="text-xs font-medium text-foreground"
                        >
                          该密钥前置系统指令 (Prepend System Prompt)
                        </label>
                        <Badge
                          variant={
                            newKeyPromptMode === 'pure_passthrough' ? 'outline' : 'success-light'
                          }
                        >
                          {newKeyPromptMode === 'pure_passthrough'
                            ? '纯透传模式下不注入'
                            : '当前模式下生效'}
                        </Badge>
                      </div>
                      <Textarea
                        id="key-prepend-prompt"
                        rows={3}
                        value={newKeyPrependPrompt}
                        onChange={setNewKeyPrependPrompt}
                        placeholder="输入仅对该客户端 API Key 前置注入的团队规范、角色设定或语言偏好（留空则不注入额外前置指令）..."
                      />
                    </div>
                  </div>

                  {/* Section 3: ACP Session Reuse Strategy */}
                  <div className="rounded-lg border border-border bg-muted/20 p-3.5 flex flex-col gap-3">
                    <div className="flex items-center justify-between gap-2">
                      <div>
                        <div className="text-xs font-semibold text-foreground">
                          3. ACP 会话复用模式 (Session Affinity)
                        </div>
                        <div className="text-xs text-muted-foreground">
                          决定该密钥的多轮对话请求如何复用底层保活的 <code className="font-mono">agy acp</code> 会话上下文
                        </div>
                      </div>
                      <Badge variant="outline">多轮亲和性</Badge>
                    </div>

                    <div role="radiogroup" aria-label="ACP 会话复用策略" className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                      {[
                        {
                          value: 'yolo',
                          title: 'YOLO 全自动 (推荐)',
                          desc: '结合历史消息哈希指纹与会话头自动路由至同一空闲进程，兼顾多轮速度与零配置',
                        },
                        {
                          value: 'sticky_conversation',
                          title: 'X-Conversation-ID 粘性',
                          desc: '仅当客户端显式携带 X-Conversation-ID 请求头时复用对应会话，否则新建会话',
                        },
                        {
                          value: 'stateless_single_turn',
                          title: '单轮无状态',
                          desc: '每次请求均创建独立的新会话上下文，不跨轮次保留底层 ACP 侧历史记忆',
                        },
                      ].map((opt) => {
                        const active = newKeySessionMode === opt.value
                        return (
                          <button
                            key={opt.value}
                            type="button"
                            role="radio"
                            aria-checked={active}
                            onClick={() => setNewKeySessionMode(opt.value)}
                            className={`flex flex-col justify-between gap-1.5 rounded-lg border p-2.5 text-start transition-colors duration-150 ${
                              active
                                ? 'border-primary bg-card shadow-2xs'
                                : 'border-border bg-card/60 hover:bg-card'
                            }`}
                          >
                            <div className="flex items-center justify-between gap-1">
                              <span className="text-xs font-semibold text-foreground">
                                {opt.title}
                              </span>
                              <span
                                className={`size-2 shrink-0 rounded-full ${
                                  active ? 'bg-primary' : 'bg-muted-foreground/30'
                                }`}
                              />
                            </div>
                            <p className="text-xs text-muted-foreground leading-snug">{opt.desc}</p>
                          </button>
                        )
                      })}
                    </div>
                  </div>
                </div>
              </DialogModal>

              {/* Modal for Editing Existing API Key's Independent Policy */}
              <DialogModal
                open={Boolean(editingKey)}
                onClose={closeEditAPIKey}
                title={
                  editingKey ? (
                    <>
                      配置密钥独立策略：<span translate="no">{editingKey.name}</span>
                    </>
                  ) : (
                    '配置密钥独立策略'
                  )
                }
                description="修改该客户端 API Key 的账户绑定、RPM 限流、系统提示词注入模式与 ACP 会话复用策略"
                maxWidthClass="max-w-2xl"
                footer={
                  <>
                    <span className="font-mono text-xs text-muted-foreground">
                      前缀：<span translate="no">{editingKey?.key_prefix || ''}</span>
                    </span>
                    <div className="flex items-center gap-2">
                      <Button size="sm" variant="outline" onClick={closeEditAPIKey}>
                        取消
                      </Button>
                      <Button
                        size="sm"
                        disabled={savingKeyEdit}
                        onClick={() => void handleSaveKeyEdit()}
                      >
                        {savingKeyEdit ? '正在保存...' : '保存密钥策略'}
                      </Button>
                    </div>
                  </>
                }
              >
                {editingKey && (
                  <div className="flex flex-col gap-3.5">
                    {editKeyError && (
                      <Alert variant="destructive">
                        <div className="flex-1">
                          <AlertTitle>保存失败</AlertTitle>
                          <AlertDescription>{editKeyError}</AlertDescription>
                        </div>
                      </Alert>
                    )}
                    {/* Section 1: Basic Identity & Traffic Quota */}
                    <div className="rounded-lg border border-border bg-muted/20 p-3.5 flex flex-col gap-3">
                      <div className="flex items-center justify-between gap-2">
                        <div>
                          <div className="text-xs font-semibold text-foreground">
                            1. 基础标识与账户路由配额
                          </div>
                          <div className="text-xs text-muted-foreground">
                            修改密钥备注名称、每分钟请求速率限制与绑定的下游隔离账户
                          </div>
                        </div>
                        <Badge variant="outline">路由与限流</Badge>
                      </div>

                      <div className="grid grid-cols-1 gap-3 sm:grid-cols-12">
                        <div className="flex flex-col gap-1 sm:col-span-8">
                          <label
                            htmlFor="edit-key-name"
                            className="text-xs font-medium text-foreground"
                          >
                            密钥备注名称
                          </label>
                          <Input
                            id="edit-key-name"
                            value={editingKey.name}
                            onChange={(v) => setEditingKey({ ...editingKey, name: v })}
                          />
                        </div>
                        <div className="flex flex-col gap-1 sm:col-span-4">
                          <label
                            htmlFor="edit-key-rpm"
                            className="text-xs font-medium text-foreground"
                          >
                            每分钟限流 (RPM)
                          </label>
                          <Input
                            id="edit-key-rpm"
                            inputMode="numeric"
                            value={editKeyRPM}
                            invalid={editKeyRPM !== '' && parsePositiveInt(editKeyRPM) === null}
                            onChange={setEditKeyRPM}
                          />
                        </div>
                      </div>

                      <div className="flex flex-col gap-1.5">
                        <div className="flex items-center justify-between gap-2">
                          <label
                            htmlFor="edit-key-acc"
                            className="text-xs font-medium text-foreground"
                          >
                            绑定隔离账户 ID
                          </label>
                          <span className="text-xs text-muted-foreground">
                            留空则按全池健康度自动负载均衡
                          </span>
                        </div>
                        <Input
                          id="edit-key-acc"
                          value={editingKey.bound_account_id}
                          onChange={(v) => setEditingKey({ ...editingKey, bound_account_id: v })}
                          placeholder="留空为全池自动负载均衡"
                        />
                        {accounts.length > 0 && (
                          <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
                            <span className="text-xs text-muted-foreground">快捷选择:</span>
                            <button
                              type="button"
                              onClick={() =>
                                setEditingKey({ ...editingKey, bound_account_id: '' })
                              }
                              className={`rounded-md border px-2 py-0.5 text-xs transition-colors duration-150 ${
                                (editingKey.bound_account_id || '').trim() === ''
                                  ? 'border-primary bg-primary/10 font-medium text-foreground'
                                  : 'border-border bg-card text-muted-foreground hover:text-foreground'
                              }`}
                            >
                              全池自动负载均衡
                            </button>
                            {accounts.map((snap) => {
                              const accId = snap.account.id
                              const selected =
                                (editingKey.bound_account_id || '').trim() === accId
                              return (
                                <button
                                  key={accId}
                                  type="button"
                                  onClick={() =>
                                    setEditingKey({ ...editingKey, bound_account_id: accId })
                                  }
                                  className={`rounded-md border px-2 py-0.5 font-mono text-xs transition-colors duration-150 ${
                                    selected
                                      ? 'border-primary bg-primary/10 font-medium text-foreground'
                                      : 'border-border bg-card text-muted-foreground hover:text-foreground'
                                  }`}
                                >
                                  {accId}
                                </button>
                              )
                            })}
                          </div>
                        )}
                      </div>
                    </div>

                    {/* Section 2: System Prompt Policy */}
                    <div className="rounded-lg border border-border bg-muted/20 p-3.5 flex flex-col gap-3">
                      <div className="flex items-center justify-between gap-2">
                        <div>
                          <div className="text-xs font-semibold text-foreground">
                            2. 系统提示词注入策略 (System Prompt Policy)
                          </div>
                          <div className="text-xs text-muted-foreground">
                            控制该密钥向 <code className="font-mono">agy acp</code> 传递系统指令的方式与内置工具可见性
                          </div>
                        </div>
                        <Badge variant="info-light">Key 级独立生效</Badge>
                      </div>

                      <div role="radiogroup" aria-label="提示词注入模式" className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                        {[
                          {
                            value: 'pure_passthrough',
                            title: '纯透传 (Passthrough)',
                            tag: '屏蔽原生身份',
                            desc: '屏蔽 Antigravity 原生设定，100% 原样透传客户端 System Prompt',
                          },
                          {
                            value: 'prepend_global',
                            title: '全局前置指令',
                            tag: '屏蔽原生 + 前置',
                            desc: '屏蔽原生身份设定，在客户端 System Prompt 之前注入下方前置指令',
                          },
                          {
                            value: 'native_hybrid',
                            title: '原生混合 (Hybrid)',
                            tag: '保留原生设定',
                            desc: '保留 Antigravity 原生身份设定，并叠加下方前置指令与客户端提示词',
                          },
                        ].map((opt) => {
                          const active = (editingKey.prompt_mode || 'native_hybrid') === opt.value
                          return (
                            <button
                              key={opt.value}
                              type="button"
                              role="radio"
                              aria-checked={active}
                              onClick={() =>
                                setEditingKey({ ...editingKey, prompt_mode: opt.value })
                              }
                              className={`flex flex-col justify-between gap-1.5 rounded-lg border p-2.5 text-start transition-colors duration-150 ${
                                active
                                  ? 'border-primary bg-card shadow-2xs'
                                  : 'border-border bg-card/60 hover:bg-card'
                              }`}
                            >
                              <div className="flex items-center justify-between gap-1">
                                <span className="text-xs font-semibold text-foreground">
                                  {opt.title}
                                </span>
                                <span
                                  className={`size-2 shrink-0 rounded-full ${
                                    active ? 'bg-primary' : 'bg-muted-foreground/30'
                                  }`}
                                />
                              </div>
                              <p className="text-xs text-muted-foreground leading-snug">
                                {opt.desc}
                              </p>
                              <div>
                                <Badge variant={active ? 'info-light' : 'outline'}>{opt.tag}</Badge>
                              </div>
                            </button>
                          )
                        })}
                      </div>

                      <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-card px-3 py-2">
                        <div>
                          <div className="text-xs font-medium text-foreground">
                            屏蔽 ACP 内置文件与终端工具 (Disable Built-in Tools)
                          </div>
                          <div className="text-xs text-muted-foreground">
                            开启后仅允许模型调用客户端请求体传入的自定义 Tools，防止内置工具抢占执行
                          </div>
                        </div>
                        <Switch
                          checked={editingKey.disable_builtin_tools ?? true}
                          onCheckedChange={(next) =>
                            setEditingKey({ ...editingKey, disable_builtin_tools: next })
                          }
                          ariaLabel="屏蔽 ACP 内置文件与终端工具"
                        />
                      </div>

                      <div className="flex flex-col gap-1">
                        <div className="flex items-center justify-between gap-2">
                          <label
                            htmlFor="edit-key-prepend-prompt"
                            className="text-xs font-medium text-foreground"
                          >
                            该密钥前置系统指令 (Prepend System Prompt)
                          </label>
                          <Badge
                            variant={
                              editingKey.prompt_mode === 'pure_passthrough'
                                ? 'outline'
                                : 'success-light'
                            }
                          >
                            {editingKey.prompt_mode === 'pure_passthrough'
                              ? '纯透传模式下不注入'
                              : '当前模式下生效'}
                          </Badge>
                        </div>
                        <Textarea
                          id="edit-key-prepend-prompt"
                          rows={3}
                          value={editingKey.global_prepend_prompt || ''}
                          onChange={(v) =>
                            setEditingKey({ ...editingKey, global_prepend_prompt: v })
                          }
                          placeholder="输入仅对该客户端 API Key 前置注入的系统指令（选「全局前置指令」或「原生混合」时生效）..."
                        />
                      </div>
                    </div>

                    {/* Section 3: ACP Session Reuse Strategy */}
                    <div className="rounded-lg border border-border bg-muted/20 p-3.5 flex flex-col gap-3">
                      <div className="flex items-center justify-between gap-2">
                        <div>
                          <div className="text-xs font-semibold text-foreground">
                            3. ACP 会话复用模式 (Session Affinity)
                          </div>
                          <div className="text-xs text-muted-foreground">
                            决定该密钥的多轮对话请求如何复用底层保活的 <code className="font-mono">agy acp</code> 会话上下文
                          </div>
                        </div>
                        <Badge variant="outline">多轮亲和性</Badge>
                      </div>

                      <div role="radiogroup" aria-label="ACP 会话复用策略" className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                        {[
                          {
                            value: 'yolo',
                            title: 'YOLO 全自动 (推荐)',
                            desc: '结合历史消息哈希指纹与会话头自动路由至同一空闲进程，兼顾多轮速度与零配置',
                          },
                          {
                            value: 'sticky_conversation',
                            title: 'X-Conversation-ID 粘性',
                            desc: '仅当客户端显式携带 X-Conversation-ID 请求头时复用对应会话，否则新建会话',
                          },
                          {
                            value: 'stateless_single_turn',
                            title: '单轮无状态',
                            desc: '每次请求均创建独立的新会话上下文，不跨轮次保留底层 ACP 侧历史记忆',
                          },
                        ].map((opt) => {
                          const active = (editingKey.session_mode || 'yolo') === opt.value
                          return (
                            <button
                              key={opt.value}
                              type="button"
                              role="radio"
                              aria-checked={active}
                              onClick={() =>
                                setEditingKey({ ...editingKey, session_mode: opt.value })
                              }
                              className={`flex flex-col justify-between gap-1.5 rounded-lg border p-2.5 text-start transition-colors duration-150 ${
                                active
                                  ? 'border-primary bg-card shadow-2xs'
                                  : 'border-border bg-card/60 hover:bg-card'
                              }`}
                            >
                              <div className="flex items-center justify-between gap-1">
                                <span className="text-xs font-semibold text-foreground">
                                  {opt.title}
                                </span>
                                <span
                                  className={`size-2 shrink-0 rounded-full ${
                                    active ? 'bg-primary' : 'bg-muted-foreground/30'
                                  }`}
                                />
                              </div>
                              <p className="text-xs text-muted-foreground leading-snug">
                                {opt.desc}
                              </p>
                            </button>
                          )
                        })}
                      </div>
                    </div>
                  </div>
                )}
              </DialogModal>

              <Frame>
                <FrameHeader>
                  <div>
                    <FrameTitle>已签发密钥与独立策略列表</FrameTitle>
                    <FrameDescription>
                      点击右侧「配置策略」可随时调整任意密钥的系统提示词注入模式、前置指令、会话复用模式与限流配额
                    </FrameDescription>
                  </div>
                  <Badge variant="outline">{`共 ${apiKeys.length} 个密钥`}</Badge>
                </FrameHeader>
                <FramePanel className="p-0 overflow-hidden">
                  <DataGridView
                    columns={[
                      {
                        id: 'name',
                        header: '密钥名称 / 前缀',
                        cell: (r: APIKeyRecord) => (
                          <div translate="no">
                            <div className="font-medium text-foreground">{r.name}</div>
                            <div className="font-mono text-xs text-muted-foreground">
                              {r.key_prefix}
                            </div>
                          </div>
                        ),
                      },
                      {
                        id: 'bound',
                        header: '绑定隔离账户',
                        cell: (r: APIKeyRecord) =>
                          r.bound_account_id ? (
                            <span translate="no">
                              <Badge variant="outline">{r.bound_account_id}</Badge>
                            </span>
                          ) : (
                            <Badge variant="outline">全池自动负载均衡</Badge>
                          ),
                      },
                      {
                        id: 'prompt_mode',
                        header: '提示词注入策略',
                        cell: (r: APIKeyRecord) => (
                          <div className="flex flex-wrap items-center gap-1">
                            <Badge
                              variant={
                                r.prompt_mode === 'pure_passthrough'
                                  ? 'success-light'
                                  : r.prompt_mode === 'prepend_global'
                                    ? 'info-light'
                                    : 'outline'
                              }
                            >
                              {formatPromptModeLabel(r.prompt_mode)}
                            </Badge>
                            {r.global_prepend_prompt?.trim() &&
                              r.prompt_mode !== 'pure_passthrough' && (
                                <Badge variant="warning-light">含前置指令</Badge>
                              )}
                            {r.disable_builtin_tools !== false && (
                              <Badge variant="outline">屏蔽内置工具</Badge>
                            )}
                          </div>
                        ),
                      },
                      {
                        id: 'session_mode',
                        header: '会话复用模式',
                        cell: (r: APIKeyRecord) => (
                          <Badge variant="outline">{formatSessionModeLabel(r.session_mode)}</Badge>
                        ),
                      },
                      {
                        id: 'rpm',
                        header: 'RPM 配额',
                        align: 'end',
                        cell: (r: APIKeyRecord) => (
                          <span className="font-mono">{r.rate_limit_rpm}</span>
                        ),
                      },
                      {
                        id: 'act',
                        header: '操作',
                        align: 'end',
                        cell: (r: APIKeyRecord) => (
                          <div className="inline-flex items-center justify-end gap-1">
                            <Button
                              size="xs"
                              variant="outline"
                              onClick={() => openEditAPIKey(r)}
                            >
                              配置策略
                            </Button>
                            <Button
                              size="xs"
                              variant="destructive"
                              onClick={() => requestDeleteAPIKey(r)}
                            >
                              吊销
                            </Button>
                          </div>
                        ),
                      },
                    ]}
                    rows={apiKeys}
                    getRowId={(r) => String(r.id)}
                    emptyTitle="暂未签发客户端 API Key"
                    emptyDescription="签发密钥后即可在此配置独立策略"
                    emptyAction={
                      <Button size="sm" onClick={openCreateAPIKeyModal}>
                        <Plus />
                        签发客户端 API Key
                      </Button>
                    }
                  />
                </FramePanel>
              </Frame>
            </div>
          )}
        </main>
      </div>
    </div>
  )
}
export default App
