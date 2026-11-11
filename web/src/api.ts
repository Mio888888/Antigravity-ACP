export interface WorkerBoundSession {
  bridge_id: string
  acp_session_id?: string
  explicit_key?: string
  updated_at: string
}

export interface WorkerSnapshot {
  id: string
  account_id: string
  pid: number
  state: 'Idle' | 'Generating' | 'Parked' | 'Dead' | string
  last_active_at: string
  bound_sessions?: WorkerBoundSession[]
}

export interface QuotaWindow {
  label: string
  remaining_pct: number
  reset_text: string
}

export interface ModelQuotaGroup {
  group_name: string
  windows: QuotaWindow[]
}

export interface AccountUsageStats {
  total_requests: number
  requests_24h: number
  success_requests: number
  error_requests: number
  success_rate_pct: number
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
  tokens_24h: number
  today_success: number
  today_failed: number
  today_prompt_tokens: number
  today_completion_tokens: number
  week_prompt_tokens: number
  week_completion_tokens: number
  avg_latency_ms: number
  last_used_at?: string
  quota_read_at?: string
  last_check_at?: string
  last_check_msg?: string
  quota_groups?: ModelQuotaGroup[]
}

export interface AccountRecord {
  id: string
  name: string
  auth_mode: 'oauth_personal' | 'oauth_business' | 'gemini_api_key' | 'agent_platform' | string
  api_key?: string
  isolation_enabled: boolean
  network_mode: 'direct' | 'proxy' | 'bind_ip' | 'netns' | string
  proxy_url: string
  bind_ip: string
  netns_name: string
  max_workers: number
  weight: number
  enabled: boolean
  status: 'ready' | 'auth_required' | 'error' | string
  last_egress_ip: string
  disabled_models?: string[]
}

export interface AccountSnapshot {
  account: AccountRecord
  gemini_home: string
  relay_listen: string
  worker_count: number
  busy_workers: number
  login_state: string
  login_auth_url?: string
  models?: ACPModelInfo[]
  models_cached_at?: string
  usage: AccountUsageStats
}

export interface AuditContextItem {
  category:
    | 'system_prompt'
    | 'tools'
    | 'skills'
    | 'files'
    | 'tool_results'
    | 'conversation'
    | string
  tag: string
  title: string
  subtitle?: string
  tokens: number
  count?: number
  content?: string
}

export interface AuditContextSnapshot {
  input_tokens: number
  cached_tokens: number
  output_tokens: number
  prep_ms?: number
  ttfb_ms: number
  turn_count: number
  prompt_summary?: string
  prompt_mode?: string
  session_mode?: string
  disable_builtin_tools?: boolean
  key_name?: string
  client_label?: string
  client_session_id?: string
  session_title?: string
  thinking_level?: string
  context_capacity?: number
  client_context_window?: number
  items: AuditContextItem[]
}

export interface AuditLogRecord {
  request_id: string
  protocol: string
  account_id: string
  session_id: string
  worker_pid: number
  model_name: string
  thinking_level?: string
  prompt_tokens: number
  cached_tokens?: number
  completion_tokens: number
  total_tokens: number
  latency_ms: number
  status_code: number
  error_msg: string
  system_prompt?: string
  prompt_text?: string
  thinking_text?: string
  response_text?: string
  tool_calls_json?: string
  context_json?: string
  created_at: string
}

export interface ACPModelInfo {
  modelId: string
  name: string
  description?: string
  thinkingLevels?: string[]
  defaultThinkingLevel?: string
  thinking_levels?: string[]
  default_thinking_level?: string
}

export interface OverviewResponse {
  stats: {
    total_accounts: number
    ready_accounts: number
    total_requests: number
    success_requests: number
    total_tokens: number
    avg_latency_ms: number
  }
  active_workers: number
  workers: WorkerSnapshot[]
  parked_tools: number
  models: ACPModelInfo[]
  models_cached_at?: string
  models_cache_ttl_sec?: number
  accounts: AccountSnapshot[]
  prompt_policy: PromptPolicyConfig
  session_mode: string
  public_base_url: string
  credentials_locked?: boolean
  gateway_version?: string
  logs?: AuditLogRecord[]
}

export interface UpdateRuntimeInfo {
  os: string
  arch: string
  deploy_mode: string
  executable_path: string
}

export interface GatewayUpdateStatus {
  repo: string
  current_version: string
  commit?: string
  latest_version: string
  release_available: boolean
  update_available: boolean
  can_self_update: boolean
  blocked_reason?: string
  manual_command?: string
  asset_name?: string
  asset_size?: number
  release_url?: string
  published_at?: string
  release_notes?: string
  check_error?: string
}

export interface ACPUpdateStatus {
  backend_type: string
  flavor: 'cli' | 'zed' | string
  binary_path: string
  target_install_dir: string
  current_version: string
  latest_version: string
  update_available: boolean
  can_hot_update: boolean
  blocked_reason?: string
  check_error?: string
}

export interface SystemUpdateStatusResponse {
  checked_at: string
  updating: boolean
  runtime: UpdateRuntimeInfo
  gateway: GatewayUpdateStatus
  acp: ACPUpdateStatus
}

export interface SystemUpdateApplyResponse {
  updated_gateway: boolean
  gateway_version?: string
  executable_path?: string
  updated_acp: boolean
  acp_version?: string
  acp_binary_path?: string
  retired_workers?: number
  restarting: boolean
}

export interface TerminalLoginStatus {
  account_id: string
  state: 'idle' | 'waiting_code' | 'success' | 'error' | string
  auth_url?: string
  output?: string
}

export interface PromptPolicyConfig {
  mode: 'pure_passthrough' | 'prepend_global' | 'native_hybrid' | string
  global_prepend_prompt: string
  disable_builtin_tools: boolean
  session_mode?: string
  internal_approval?: string
}

export interface SkillRecord {
  id: number
  name: string
  category: 'instruction' | string
  enabled: boolean
  content_json: string
  updated_at: string
}

export interface APIKeyRecord {
  id: number
  name: string
  key_prefix: string
  bound_account_id: string
  rate_limit_rpm: number
  prompt_mode?: 'pure_passthrough' | 'prepend_global' | 'native_hybrid' | string
  session_mode?: 'yolo' | 'stateless_single_turn' | 'sticky_conversation' | string
  global_prepend_prompt?: string
  disable_builtin_tools?: boolean
  created_at: string
}

export const API_TIMEOUT_MS = 15_000

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export class UnauthorizedError extends ApiError {
  constructor(message = '未登录或管理会话已过期，请先输入密码登录') {
    super(message, 401)
    this.name = 'UnauthorizedError'
  }
}

export class NetworkError extends Error {
  constructor(message = '无法连接网关服务，请检查网络后重试') {
    super(message)
    this.name = 'NetworkError'
  }
}

export interface ApiFetchOptions extends RequestInit {
  timeoutMs?: number
}

// Admin calls authenticate solely via the HttpOnly `ag_admin_session` cookie.
export async function apiFetch<T>(path: string, init?: ApiFetchOptions): Promise<T> {
  const headers = new Headers(init?.headers || {})
  if (init?.body !== undefined && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const timeoutMs = init?.timeoutMs && init.timeoutMs > 0 ? init.timeoutMs : API_TIMEOUT_MS
  const controller = new AbortController()
  const timer = window.setTimeout(() => controller.abort(), timeoutMs)
  const outerSignal = init?.signal
  const onOuterAbort = () => controller.abort()
  if (outerSignal) {
    if (outerSignal.aborted) controller.abort()
    else outerSignal.addEventListener('abort', onOuterAbort, { once: true })
  }
  let resp: Response
  try {
    resp = await fetch(path, {
      ...init,
      headers,
      credentials: 'same-origin',
      signal: controller.signal,
    })
  } catch (err) {
    if (controller.signal.aborted && !outerSignal?.aborted) {
      throw new NetworkError('请求网关超时，请稍后重试')
    }
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    throw new NetworkError()
  } finally {
    window.clearTimeout(timer)
    outerSignal?.removeEventListener('abort', onOuterAbort)
  }
  if (!resp.ok) {
    const errText = await resp.text().catch(() => '')
    let message = errText || `HTTP ${resp.status}`
    try {
      const parsed = JSON.parse(errText) as { error?: { message?: string } }
      if (parsed?.error?.message) {
        message = parsed.error.message
      }
    } catch {
      // ignore non-json error body
    }
    if (resp.status === 401) {
      throw new UnauthorizedError(message)
    }
    throw new ApiError(message, resp.status)
  }
  return resp.json() as Promise<T>
}

export async function adminLogin(password: string, remember: boolean): Promise<void> {
  await apiFetch<unknown>('/api/admin/auth/login', {
    method: 'POST',
    body: JSON.stringify({ password, remember }),
  })
}

export async function adminLogout(): Promise<void> {
  try {
    await apiFetch<{ logged_out: boolean }>('/api/admin/auth/logout', { method: 'POST' })
  } catch {
    // ignore logout network errors
  }
}

// Resolves to true/false only for a definitive answer (200 / 401); other failures throw.
export async function adminCheckSession(): Promise<boolean> {
  try {
    const res = await apiFetch<{ authenticated?: boolean }>('/api/admin/auth/session')
    return res.authenticated !== false
  } catch (err) {
    if (err instanceof UnauthorizedError) return false
    throw err
  }
}

export async function adminChangePassword(
  currentPassword: string,
  newPassword: string,
  newAdminToken?: string
): Promise<{ updated: boolean; credentials_locked?: boolean }> {
  return apiFetch<{ updated: boolean; credentials_locked?: boolean }>(
    '/api/admin/auth/password',
    {
      method: 'POST',
      body: JSON.stringify({
        current_password: currentPassword,
        new_password: newPassword,
        new_admin_token: newAdminToken || '',
      }),
    }
  )
}

export async function adminTerminalLoginStatus(
  accountId: string,
  signal?: AbortSignal
): Promise<TerminalLoginStatus> {
  return apiFetch<TerminalLoginStatus>(
    `/api/admin/accounts/${encodeURIComponent(accountId)}/login/status`,
    { signal }
  )
}

export async function adminGetSystemUpdateStatus(
  forceCheck = false,
  signal?: AbortSignal
): Promise<SystemUpdateStatusResponse> {
  const qs = forceCheck ? '?check=1' : ''
  return apiFetch<SystemUpdateStatusResponse>(`/api/admin/update/status${qs}`, {
    signal,
    timeoutMs: 30_000,
  })
}

export async function adminApplySystemUpdate(
  target: 'gateway' | 'acp' | 'all'
): Promise<SystemUpdateApplyResponse> {
  return apiFetch<SystemUpdateApplyResponse>('/api/admin/update/apply', {
    method: 'POST',
    body: JSON.stringify({ target }),
    timeoutMs: 300_000,
  })
}
