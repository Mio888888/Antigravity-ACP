import { useEffect } from 'react'

export type Lang = 'zh' | 'en'

const LANG_STORAGE_KEY = 'ag_gateway_lang'

export function getStoredLang(): Lang {
  try {
    const saved = window.localStorage.getItem(LANG_STORAGE_KEY)
    if (saved === 'en' || saved === 'zh') return saved
  } catch {
    // ignore storage errors
  }
  return 'zh'
}

export function setStoredLang(lang: Lang): void {
  try {
    window.localStorage.setItem(LANG_STORAGE_KEY, lang)
    document.documentElement.lang = lang === 'en' ? 'en' : 'zh-CN'
  } catch {
    // ignore storage errors
  }
}

const WEEKDAY_MAP: Record<string, string> = {
  周日: 'Sun',
  周一: 'Mon',
  周二: 'Tue',
  周三: 'Wed',
  周四: 'Thu',
  周五: 'Fri',
  周六: 'Sat',
}

const MONTH_NAMES = [
  'Jan',
  'Feb',
  'Mar',
  'Apr',
  'May',
  'Jun',
  'Jul',
  'Aug',
  'Sep',
  'Oct',
  'Nov',
  'Dec',
]

const ZH_TO_EN_EXACT: Record<string, string> = {
  // Top Bar & Auth & Password Modal
  管理鉴权: 'Admin Auth',
  'Web 管理控制台受密码保护，必须登录才能查看与操作':
    'Web Admin Console is password-protected. Please sign in to view and manage the gateway.',
  登录验证未通过: 'Authentication Failed',
  管理员密码: 'Admin Password',
  '请输入管理员密码...': 'Enter admin password...',
  请输入管理员密码: 'Please enter admin password',
  隐藏密码: 'Hide password',
  显示密码: 'Show password',
  '保持登录状态 (7 天内免重复输入)': 'Keep me signed in (7 days)',
  '保持登录状态 7 天': 'Keep me signed in for 7 days',
  '正在验证密码...': 'Verifying password...',
  登录管理控制台: 'Sign In to Admin Console',
  '正在验证管理会话...': 'Verifying admin session...',
  '请稍候，正在检查访问权限': 'Please wait while access permissions are verified',
  'Antigravity ACP Gateway · 未授权禁止访问控制平面':
    'Antigravity ACP Gateway · Unauthorized access to control plane is prohibited',
  '服务端 agy acp 多账户隔离池、模型周期额度监控与三大会话协议统一网关':
    'Server-side agy acp multi-account isolation pool, model quota telemetry & unified multi-protocol gateway',
  服务端: 'Server-side',
  '多账户隔离池、模型周期额度监控与三大会话协议统一网关':
    'multi-account isolation pool, model quota telemetry & unified multi-protocol gateway',
  '配置项:': 'Config:',
  已登录: 'Signed In',
  修改密码: 'Password',
  刷新: 'Refresh',
  退出登录: 'Sign Out',
  切换深浅色主题: 'Toggle dark/light theme',
  修改: 'Edit',
  取消: 'Cancel',
  保存: 'Save',
  确认修改: 'Save Changes',
  '正在保存...': 'Saving...',
  修改Web管理控制台登录密码: 'Change Web Admin Console Password',
  '修改 Web 管理控制台登录密码': 'Change Web Admin Console Password',
  '验证当前密码后更新管理后台登录密码，修改后立即生效并持久化保存':
    'Verify current password and update admin console password immediately',
  '新密码长度不少于 8 位': 'New password must be at least 8 characters',
  当前管理员密码: 'Current Admin Password',
  输入当前正在使用的密码: 'Enter current password',
  新管理员密码: 'New Admin Password',
  '输入至少 8 位的新密码': 'Enter new password (min 8 chars)',
  确认新密码: 'Confirm New Password',
  再次输入新密码: 'Re-enter new password',
  '登录会话已失效，请重新输入密码登录': 'Session expired. Please sign in again.',
  '未登录或管理会话已过期，请重新输入密码登录':
    'Not signed in or session expired. Please sign in again.',
  '未登录或管理会话已过期，请先输入密码登录':
    'Not signed in or session expired. Please sign in first.',
  '未登录或管理会话已过期，请先登录': 'Not signed in or session expired. Please sign in.',
  '登录失败，请检查管理员密码': 'Sign-in failed. Please check admin password.',
  请输入当前管理员密码: 'Please enter current admin password',
  '新管理员密码长度不能少于 8 位': 'New admin password must be at least 8 characters',
  '新密码长度不能少于 8 位': 'New password must be at least 8 characters',
  '自定义 Admin Token 长度不能少于 12 位': 'Custom Admin Token must be at least 12 characters',
  两次输入的新密码不一致: 'New passwords do not match',
  管理员登录密码已更新: 'Admin password updated successfully',
  修改管理员密码失败: 'Failed to change admin password',
  加载管理数据失败: 'Failed to load admin console data',
  '登录失败次数过多，请 5 分钟后再试': 'Too many failed attempts. Please try again in 5 minutes.',
  无效的登录请求格式: 'Invalid login request format',
  '管理员密码错误，请重新输入': 'Incorrect admin password. Please try again.',
  无法创建登录会话: 'Failed to create login session',
  无效的请求参数: 'Invalid request parameters',
  当前管理员密码不正确: 'Current admin password is incorrect',
  '密码已更新，但重新签发会话失败': 'Password updated, but failed to issue a new session',

  // Navigation & Sidebar
  控制平面导航: 'Control Plane',
  主导航: 'Main Navigation',
  运行总览: 'Overview',
  多账户与额度卡片: 'Accounts & Quotas',
  远程客户端接入: 'Client Onboarding',
  '客户端 API Key': 'Client API Keys',
  '全池 24h Token': '24h Pool Tokens',
  操作异常: 'Operation Error',
  状态已同步: 'Status Synced',

  // Overview Board
  实时同步: 'Live Sync',
  '这里只开了一个账号或 Key，路由没有可选的。在供应商的编辑页再开一个，路由方式就在下面的「多账号 / 多 Key」里设置。':
    'Only 1 account is enabled, so routing is direct. Enable another account under Accounts & Quotas to activate weighted multi-account load balancing.',
  等待首个客户端请求接入: 'Waiting for first client request',
  等待或处理客户端请求: 'Waiting for or processing client request',
  执行智能体任务: 'Executing agent task',
  请求: 'Requests',
  改道: 'Failovers',
  'Agent 收到的错误': 'Agent Errors',
  最近一个请求是怎么路由的: 'How the Latest Request Was Routed',
  请求与响应: 'Request & Response',
  查看用量: 'View Quotas',
  ': 由': ': served by',
  '提供，向厂商请求的模型是': 'using upstream model',
  '推理强度 用': 'reasoning completed in',
  时间花在哪里: 'Where Time Was Spent',
  上下文窗口: 'Context Window',
  纯透传: 'Passthrough',
  全局前置: 'Prepend Global',
  原生混合: 'Native Hybrid',
  仅客户端工具: 'Client Tools Only',
  含内置工具: 'With Built-in Tools',
  '纯透传 · 仅客户端工具': 'Passthrough · Client Tools Only',
  '纯透传 · 含内置工具': 'Passthrough · With Built-in Tools',
  '全局前置 · 仅客户端工具': 'Prepend Global · Client Tools Only',
  '全局前置 · 含内置工具': 'Prepend Global · With Built-in Tools',
  '原生混合 · 仅客户端工具': 'Native Hybrid · Client Tools Only',
  '原生混合 · 含内置工具': 'Native Hybrid · With Built-in Tools',
  已计数: 'Counted',
  已用上下文: 'Used Context',
  充裕: 'Plenty',
  适中: 'Moderate',
  渐满: 'Filling Up',
  提示词缓存: 'Prompt Cache',
  命中: 'Hit Rate',
  内容: 'Breakdown',
  全部: 'All',
  系统提示词: 'System Prompt',
  'agent 提示词': 'Agent Prompt',
  客户端系统提示词: 'Client System Prompt',
  全局前置与客户端提示词: 'Global Prepend & Client Prompt',
  'pure_passthrough 原生透传': 'pure_passthrough verbatim passthrough',
  网关系统与思考规则: 'Gateway System & Reasoning Rules',
  网关前置与思考标签规范: 'Gateway prepend & reasoning tag rules',
  'AGENTS.md 与技能规范': 'AGENTS.md & Skill Rules',
  客户端附带项目规则: 'Client-attached project rules',
  项目指令与技能目录: 'Project instructions & skill catalog',
  '未注入全局技能 (纯透传)': 'No global skills injected (Passthrough)',
  客户端工具定义: 'Client Tool Definitions',
  '客户端 MCP 工具定义': 'Client MCP tool definitions',
  已禁用内置工具: 'Built-in Tools Disabled',
  无工具调用: 'No Tool Definitions',
  'ACP 内置工具集': 'ACP Built-in Toolset',
  'ACP 宿主终端与文件工具': 'ACP host terminal & file tools',
  工作区文件上下文: 'Workspace File Context',
  无文件引用: 'No File References',
  历史读取文件: 'Previously read files',
  图片: 'Image',
  工具调用返回结果: 'Tool call execution output',
  当前会话: 'Current Session',
  历史会话消息: 'Historical conversation turns',
  工具: 'Tools',
  技能: 'Skills',
  文件: 'Files',
  工具结果: 'Tool Results',
  会话内容: 'Conversation',
  剩余空间: 'Remaining Space',
  收起: 'Show Less',
  展开: 'Expand',
  本会话: 'This Session',
  当前请求上下文: 'Current Request Context',
  客户端透传系统提示词: 'Client System Prompt (Passthrough)',
  矩形树图视图: 'Treemap View',
  网格点阵视图: 'Waffle Grid View',
  本会话请求上下文趋势: 'Session Context Trend',
  'ACP 动态模型': 'ACP Dynamic Model',
  '<从 /v1/models 实时获取的模型ID>': '<Model ID from /v1/models>',

  // Bottom Request Selector & Worker Processes
  '清除右侧筛选 ×': 'Clear Filter ×',
  用途: 'Purpose',
  '用途 · 对话': 'Purpose · Chat',
  '用途 · 工具续接': 'Purpose · Tool',
  '指标 · 完整': 'Metrics · Full',
  '指标 · 精简': 'Metrics · Compact',
  实时: 'Live',
  更早的日期: 'Older Dates',
  按请求: 'By Request',
  按会话: 'By Session',
  当前筛选条件下暂无匹配的请求记录: 'No matching requests under current filter',
  重置筛选条件: 'Reset Filters',
  工具续接: 'Tool Continuation',
  对话: 'Chat',
  '轮次:': 'Turns:',
  底层进程与绑定会话: 'Worker Processes & Bound Sessions',
  当前暂无运行中的底层进程: 'No active ACP worker processes',
  生成中: 'Generating',
  会话驻留: 'Session Bound',
  空闲待命: 'Idle Ready',
  已退出: 'Exited',
  历史进程: 'Historical',
  '暂未绑定会话 · 预热待命进程': 'No bound session · Pre-warmed standby worker',
  管理号池与进程: 'Manage Pool & Workers',

  // Accounts Tab & Cards & Quota Windows
  多账户隔离池与模型额度监控卡片: 'Multi-Account Isolation Pool & Quota Cards',
  '实时展示每个隔离账户的代理与出口 IP、Gemini / Claude 周期额度剩余百分比、今日与近 7 天 Token 明细及快捷运维操作':
    'Real-time per-account proxy/egress IP, Gemini & Claude quota windows, daily/7-day token telemetry, and quick operations',
  卡片视图: 'Card View',
  表格视图: 'Table View',
  新增隔离账户: 'Add Account',
  正常: 'Ready',
  待登录: 'Auth Required',
  异常: 'Error',
  检测: 'Probe',
  刷新额度: 'Refresh Quota',
  模型: 'Models',
  重新登录: 'Re-login',
  换代理: 'Proxy',
  编辑: 'Edit',
  清除冷却: 'Clear Cooldown',
  删除: 'Delete',
  保存代理: 'Save Proxy',
  直连: 'Direct',
  本地直连: 'Local Direct',
  'direct://本地直连': 'direct://local',
  待检测: 'Pending Probe',
  待探测: 'Pending Discovery',
  网卡IP: 'Bind IP',
  '网卡 IP': 'Bind IP',
  'Gemini 模型': 'Gemini Models',
  'Claude / GPT 模型': 'Claude / GPT Models',
  每周: 'Weekly',
  每日: 'Daily',
  '5 小时': '5 Hours',
  即将重置: 'Resetting soon',
  '今日 成功': 'Today OK',
  '· 失败': '· Fail',
  '· Token': '· Token',
  '近 7 天': '7d Tokens',
  正在处理: 'Active',
  额度读取正常: 'Quota synced OK',
  待点击刷新额度读取: 'Click Refresh Quota to sync',
  冷却已清除: 'Cooldown cleared',
  '未返回 Quota Limits 段落': 'No Quota Limits section returned',
  未解析到有效额度分组: 'No valid quota groups parsed',
  '账户待登录 (Authentication required)': 'Account requires sign-in (Authentication required)',
  '读取 /usage 超时': 'Timed out reading /usage',
  '点击下方「刷新额度」通过 agy acp /usage 读取':
    'Click "Refresh Quota" below to fetch via agy acp /usage',
  '无头 Terminal Login 授权引导': 'Headless Terminal Login OAuth Guide',
  '浏览器打开授权链接：': 'Open authorization URL in browser:',
  '1. 在浏览器打开授权链接完成 Google 账号登录：':
    '1. Open the authorization URL in your browser to sign in with Google:',
  '2. 粘贴授权验证码或浏览器回调的完整 URL：':
    '2. Paste the authorization code or full redirect callback URL:',
  '粘贴 Google / Antigravity 授权验证码...': 'Paste Google / Antigravity OAuth code...',
  '粘贴 Google / Antigravity 授权码或回调链接...':
    'Paste Google / Antigravity OAuth code or callback URL...',
  复制链接: 'Copy URL',
  打开授权页: 'Open Auth Page',
  提交: 'Submit',
  提交验证: 'Verify',
  '提交中...': 'Submitting...',
  等待授权码: 'Awaiting Code',
  授权成功: 'Authorized',
  授权失败: 'Auth Failed',
  待命: 'Idle',
  '检测中...': 'Probing...',
  '刷新中...': 'Refreshing...',
  '启动中...': 'Starting...',
  '读取中...': 'Loading...',
  立即登录: 'Sign In',
  读取额度: 'Fetch Quota',
  尚未读取到模型额度: 'Model quota not fetched yet',
  '账户待登录授权，完成授权后可读取模型额度':
    'Account requires sign-in; complete OAuth to fetch model quota',
  尚未执行: 'Not run yet',
  '未自定义前每次部署动态生成随机密码与 Token，可在部署输出、':
    'Generated dynamically on each deploy until customized; view in deploy output, ',
  '中查看；登录后修改密码即永久固定。':
    ', or the credentials file; change password after sign-in to lock permanently.',
  '已登录 · 密码已固定': 'Signed In · Locked',
  '已登录 · 动态密码': 'Signed In · Dynamic',
  修改并固定管理员密码与Token: 'Change & Lock Admin Password / Token',
  '修改并固定管理员密码与 Token': 'Change & Lock Admin Password / Token',
  '当前凭证已由用户自定义固定，重部署不会重置':
    'Credentials are customized and locked; redeployments will not reset them',
  '当前使用的是每次部署动态生成的临时凭证；在此主动修改后将永久固定，后续重新部署不再变更':
    'Currently using dynamic deployment credentials; changing them here locks them permanently across redeployments',
  修改后立即持久化锁定至数据库: 'Locked and persisted in SQLite immediately upon saving',
  确认修改并固定: 'Save & Lock',
  '当前管理员密码或 Token': 'Current Admin Password or Token',
  '输入当前正在使用的密码或 Admin Token': 'Enter current Admin Password or Token',
  '输入至少 8 位的新密码（修改后永久固定）': 'Enter at least 8 characters (locks permanently)',
  '至少 12 位，用于脚本调用 /api/admin/*（留空保持当前值并固定）':
    'Min 12 characters, for scripts calling /api/admin/* (leave blank to keep and lock the current token)',
  '自定义 Admin Token（可选，留空则固定当前 Token）':
    'Custom Admin Token (optional; leave blank to lock current token)',
  '用于脚本调用 /api/admin/* 的静态 Token（留空保持当前值并固定）':
    'Static token for /api/admin/* scripts (leave blank to keep & lock current token)',
  '管理员密码与 Token 已更新并永久固定，后续重新部署不会再重置':
    'Admin Password and Token updated and locked permanently across redeployments',
  尚未: 'Never',
  刚刚: 'Just now',
  操作: 'Actions',
  '账户名称 / ID': 'Account Name / ID',
  鉴权方式: 'Auth Mode',
  '今日 Token (入/出)': 'Today Tokens (In/Out)',
  '今日 成功 / 失败': 'Today OK / Fail',
  '代理 / 出口 IP': 'Proxy / Egress IP',
  状态: 'Status',
  'ACP 可用模型': 'ACP Models',
  保存配置: 'Save Configuration',
  '配置独立 GEMINI_CLI_HOME 容器目录、ACP 认证方式、SOCKS5/HTTP 代理或源网卡 IP 绑定':
    'Configure isolated GEMINI_CLI_HOME directory, ACP auth mode, SOCKS5/HTTP proxy, or source IP binding',
  '账户标识 ID (例如 g1, g2)': 'Account ID (e.g. g1, g2)',
  '例如: g1 或 us-node-01': 'e.g. g1 or us-node-01',
  备注名称: 'Display Name',
  '例如: 主力账号 g1': 'e.g. Primary Account g1',
  'ACP 认证方式': 'ACP Auth Mode',
  'GEMINI_API_KEY (仅注入该账户环境，留空则保持原密钥)':
    'GEMINI_API_KEY (injected into this account environment only; leave blank to keep existing)',
  网络隔离与出口模式: 'Network Isolation & Egress Mode',
  'Direct 直连': 'Direct Connection',
  'SOCKS5 / HTTP 代理': 'SOCKS5 / HTTP Proxy',
  绑定源网卡IP: 'Bind Source IP',
  '绑定源网卡 IP': 'Bind Source IP',
  '代理地址 (socks5:// 或 http://)': 'Proxy URL (socks5:// or http://)',
  'Linux NetNS 名称': 'Linux NetNS Name',
  '源网卡 IP 地址': 'Source Interface IP Address',
  最大子进程并发槽位: 'Max Worker Concurrency Slots',
  负载均衡调度权重: 'Load Balancing Weight',
  'ACP 后端实时可用模型': 'Live ACP Available Models',
  '独立控制该账户允许调度的 ACP 模型；禁用后，北向请求在负载均衡时将自动跳过该账户':
    'Control which ACP models are enabled for this account; disabled models are automatically skipped during load balancing',
  '点击模型行或右侧开关即可实时切换启用/禁用状态':
    'Click any model row or toggle switch to enable/disable in real time',
  全部恢复启用: 'Enable All Models',
  关闭: 'Close',
  待拉取: 'Pending Fetch',
  '正在从 ACP 拉取...': 'Fetching from ACP...',
  刷新模型: 'Refresh Models',
  拉取账户ACP模型失败: 'Failed to Fetch Account ACP Models',
  '拉取账户 ACP 模型失败': 'Failed to Fetch Account ACP Models',
  重试: 'Retry',
  '思考强度:': 'Reasoning Levels:',
  已禁用: 'Disabled',
  启用中: 'Enabled',
  当前尚未缓存该账户的ACP模型列表: 'No cached ACP models for this account yet',
  '当前尚未缓存该账户的 ACP 模型列表': 'No cached ACP models for this account yet',
  点击右侧按钮从该账户的: 'Click the button on the right to fetch available models from this account\'s',
  实例实时拉取可用模型: 'instance in real time',
  立即探测: 'Discover Now',
  隔离账户列表视图: 'Isolated Accounts Table View',
  尚未配置任何账户: 'No Accounts Configured',
  点击上方新增隔离账户按钮创建账户: 'Click Add Account above to create an isolated account',
  '账户 ID 与名称不能为空': 'Account ID and Display Name are required',
  保存账户失败: 'Failed to save account',
  切换账户状态失败: 'Failed to toggle account status',
  删除账户失败: 'Failed to delete account',
  出口检测失败: 'Egress probe failed',
  刷新额度失败: 'Failed to refresh quota',
  清除冷却失败: 'Failed to clear cooldown',
  拉取模型失败: 'Failed to fetch models',
  探测账户模型失败: 'Failed to probe account models',
  更新账户模型启用状态失败: 'Failed to update model enabled state',
  恢复全部模型失败: 'Failed to re-enable all models',
  更换代理失败: 'Failed to update proxy settings',
  启动登录失败: 'Failed to start login session',
  提交验证码失败: 'Failed to submit verification code',

  // Tab 3: Remote Client Access
  远程客户端三大会话协议接入中心: 'Multi-Protocol Remote Client Onboarding Center',
  '统一支持 OpenAI 官方协议 (': 'Unified support for OpenAI Official (',
  ')、OpenAI 兼容协议 (': '), OpenAI Compatible (',
  ') 与 Claude 协议 (': '), and Claude Native (',
  ')，参数修改后实时联动下方接入代码': ') protocols — parameter edits update all snippets below in real time',
  随机生成会话ID: 'Randomize Session ID',
  '随机生成会话 ID': 'Randomize Session ID',
  '签发新 API Key': 'Issue New API Key',
  '1. 客户端 API Key': '1. Client API Key',
  填入新签发密钥: 'Use Newly Issued Key',
  鉴权凭证: 'Auth Token',
  选择已签发密钥联动参数: 'Select an issued API key to sync parameters',
  填入签发的: 'Enter an issued',
  密钥以生成鉴权头: 'key to generate auth headers',
  '2. 目标模型标识 (model)': '2. Target Model ID (model)',
  已复制: 'Copied',
  复制: 'Copy',
  '复制模型 ID': 'Copy Model ID',
  选择下方已启用模型或手动输入: 'Select an enabled model below or type manually',
  从账户可用模型中选择: 'Select from available account models',
  '当前生效:': 'Active:',
  '3. 调度账户 (X-ACP-Account-ID)': '3. Routed Account (X-ACP-Account-ID)',
  自动负载均衡: 'Auto Load Balance',
  'auto (按权重与健康度自动调度)': 'auto (Weighted & Health-aware Routing)',
  通过请求头强制指定隔离账户或保持: 'Pin a specific account via header or keep',
  故障转移: 'for automatic failover',
  '4. 会话亲和标识 (X-ACP-Session-ID)': '4. Session Affinity ID (X-ACP-Session-ID)',
  多轮上下文复用: 'Multi-turn Reuse',
  '相同 Session ID 自动路由至同一空闲': 'Same Session ID automatically routes to the same warm',
  会话: 'session',
  '1. OpenAI 官方协议': '1. OpenAI Official Protocol',
  '适用 OpenAI Codex CLI (': 'For OpenAI Codex CLI (',
  ') 与官方 SDK，原生支持': ') and official SDKs with native',
  '多轮状态续接与推理强度控制。': 'multi-turn state continuation and reasoning effort control.',
  '2. OpenAI 兼容协议': '2. OpenAI Compatible Protocol',
  '适用 Cursor、Cline、Roo Code、Continue、Cherry Studio 等生态客户端，支持自动调用':
    'For Cursor, Cline, Roo Code, Continue, Cherry Studio & ecosystem clients with automatic',
  '枚举实时模型。': 'live model discovery.',
  '3. Claude 原生协议': '3. Claude Native Protocol',
  '适用 Claude Code CLI (': 'For Claude Code CLI (',
  ') 与 Anthropic SDK，支持': ') and Anthropic SDKs with',
  '思维链块与多轮工具调用恢复。': 'thinking blocks and multi-turn tool call continuation.',
  当前选中: 'Selected',
  'Bearer 鉴权': 'Bearer Auth',
  'x-api-key 鉴权': 'x-api-key Auth',
  'OpenAI 官方协议 (/v1/responses) · 接入参数与代码模板':
    'OpenAI Official Protocol (/v1/responses) · Endpoints & Code Templates',
  'OpenAI 兼容协议 (/v1/chat/completions) · 接入参数与代码模板':
    'OpenAI Compatible Protocol (/v1/chat/completions) · Endpoints & Code Templates',
  'Claude 原生协议 (/v1/messages) · 接入参数与代码模板':
    'Claude Native Protocol (/v1/messages) · Endpoints & Code Templates',
  '支持一键复制客户端配置项，或按场景筛选 CLI、Python SDK、cURL 与模型发现代码':
    'One-click copy for client endpoints or filter by CLI, Python SDK, cURL, and model discovery snippets',
  '全部示例 (折叠列表)': 'All Snippets (Collapsible)',
  '客户端 / CLI 配置': 'Client / CLI Config',
  'cURL 流式调试': 'cURL Streaming',
  '模型枚举 (/v1/models)': 'Model Discovery (/v1/models)',
  'Anthropic Base URL (无需 /v1)': 'Anthropic Base URL (without /v1)',
  'OpenAI Base URL (含 /v1)': 'OpenAI Base URL (with /v1)',
  '会话请求端点 (POST)': 'Session Endpoint (POST)',
  '鉴权请求头 (Header)': 'Auth Header',
  '模型标识 (Model ID)': 'Model ID',
  '1. OpenAI Codex CLI 配置 (~/.codex/config.toml · 官方 Responses 协议)':
    '1. OpenAI Codex CLI Config (~/.codex/config.toml · Official Responses API)',
  '2. OpenAI 官方 Python SDK (动态获取 ACP 模型 + Responses 多轮续接)':
    '2. OpenAI Official Python SDK (Dynamic ACP Models + Multi-turn Responses)',
  '3. cURL 流式调用 OpenAI 官方协议 (POST /v1/responses)':
    '3. cURL Streaming Request — OpenAI Official (POST /v1/responses)',
  '4. OpenAI 官方协议获取可用模型 & 强制刷新缓存 (GET /v1/models)':
    '4. OpenAI Official — List Models & Force Cache Refresh (GET /v1/models)',
  '1. Cursor / Cline / Roo Code / Cherry Studio 接入参数 (OpenAI 兼容)':
    '1. Cursor / Cline / Roo Code / Cherry Studio Settings (OpenAI Compatible)',
  '2. OpenAI 兼容 SDK 多轮对话示例 (chat.completions.create)':
    '2. OpenAI Compatible SDK Multi-turn Example (chat.completions.create)',
  '3. cURL 流式调用 OpenAI 兼容协议 (POST /v1/chat/completions)':
    '3. cURL Streaming Request — OpenAI Compatible (POST /v1/chat/completions)',
  '4. OpenAI 兼容协议获取模型列表与单模型详情 (GET /v1/models)':
    '4. OpenAI Compatible — List Models & Model Detail (GET /v1/models)',
  '1. Claude Code CLI 环境变量一键接入 (Anthropic Messages 协议)':
    '1. Claude Code CLI Environment Setup (Anthropic Messages API)',
  '2. Anthropic 官方 Python SDK (拉取 ACP 模型 + Messages 对话)':
    '2. Anthropic Official Python SDK (List ACP Models + Messages API)',
  '3. cURL 流式调用 Claude 协议 (POST /v1/messages)':
    '3. cURL Streaming Request — Claude Native (POST /v1/messages)',
  '4. Claude 协议获取可用模型列表 (GET /v1/models · x-api-key 鉴权)':
    '4. Claude Protocol — List Available Models (GET /v1/models · x-api-key)',
  '每个客户端 API Key 均在北向入口独立生效其绑定的「系统提示词注入策略」与「ACP 会话复用模式」':
    'Each Client API Key independently enforces its bound System Prompt Injection Policy and ACP Session Affinity Mode',
  '前往管理 API Key 策略': 'Manage API Key Policies',

  // Tab 4: Client API Keys
  '客户端 API Key 与独立策略管理': 'Client API Keys & Independent Policies',
  '按密钥独立配置绑定隔离账户、RPM 限流、系统提示词注入策略与 ACP 会话复用模式':
    'Configure per-key account binding, RPM rate limit, system prompt injection mode, and ACP session affinity',
  '签发客户端 API Key': 'Issue Client API Key',
  新签发密钥: 'Newly Issued Key',
  '请立即复制保存客户端明文 API Key (仅显示一次)':
    'Copy and save your plaintext Client API Key now (shown only once)',
  '已保存，关闭提示': 'Saved, Dismiss',
  '签发客户端 API Key 与独立策略配置': 'Issue Client API Key & Policy Configuration',
  '为接入客户端签发专属密钥，并独立设定账户路由、系统提示词注入模式与 ACP 会话复用策略':
    'Issue a dedicated client key with custom account routing, system prompt policy, and ACP session reuse strategy',
  签发后将生成一次性的: 'Generates a one-time plaintext',
  明文密钥: 'client key upon issuance',
  '正在签发...': 'Issuing...',
  签发密钥并保存策略: 'Issue Key & Save Policy',
  '1. 基础标识与账户路由配额': '1. Identity, Routing & Rate Limit',
  '设置密钥备注名称、每分钟请求速率限制与绑定的下游隔离账户':
    'Set key label, requests-per-minute (RPM) limit, and bound downstream account',
  '修改密钥备注名称、每分钟请求速率限制与绑定的下游隔离账户':
    'Update key label, requests-per-minute (RPM) limit, and bound downstream account',
  路由与限流: 'Routing & RPM',
  密钥备注名称: 'Key Name / Label',
  '例如: MacBook-Zed-Pro / Cursor-Team / Claude-Code':
    'e.g. MacBook-Zed-Pro / Cursor-Team / Claude-Code',
  '每分钟限流 (RPM)': 'Rate Limit (RPM)',
  '绑定隔离账户 ID': 'Bound Account ID',
  留空则按全池健康度自动负载均衡: 'Leave blank for automatic pool-wide load balancing',
  '留空为全池自动负载均衡，或填写指定账户 ID (如 default)':
    'Leave blank for pool-wide load balancing, or enter an Account ID (e.g. default)',
  留空为全池自动负载均衡: 'Leave blank for pool-wide auto load balancing',
  '快捷选择:': 'Quick Select:',
  全池自动负载均衡: 'Pool-wide Auto Load Balance',
  '2. 系统提示词注入策略 (System Prompt Policy)': '2. System Prompt Injection Policy',
  控制该密钥向: 'Controls how this key injects system instructions into',
  传递系统指令的方式与内置工具可见性: 'and whether built-in tools are exposed',
  提示词注入模式: 'System Prompt Injection Mode',
  'Key 级独立生效': 'Per-Key Policy',
  '纯透传 (Passthrough)': 'Pure Passthrough',
  屏蔽原生身份: 'Strip Native Identity',
  '屏蔽 Antigravity 原生设定，100% 原样透传客户端 System Prompt':
    'Suppress native Antigravity persona and pass client System Prompt through 100% verbatim',
  全局前置指令: 'Prepend Global Prompt',
  '屏蔽原生 + 前置': 'Strip Native + Prepend',
  '屏蔽原生身份设定，在客户端 System Prompt 之前注入下方前置指令':
    'Suppress native persona and prepend custom instructions before client System Prompt',
  '原生混合 (Hybrid)': 'Native Hybrid',
  保留原生设定: 'Keep Native Persona',
  '保留 Antigravity 原生身份设定，并叠加下方前置指令与客户端提示词':
    'Retain native Antigravity persona and combine with custom prepend & client prompts',
  '屏蔽 ACP 内置文件与终端工具 (Disable Built-in Tools)':
    'Disable ACP Built-in File & Terminal Tools',
  屏蔽ACP内置文件与终端工具: 'Disable ACP Built-in File & Terminal Tools',
  '屏蔽 ACP 内置文件与终端工具': 'Disable ACP Built-in File & Terminal Tools',
  '开启后仅允许模型调用客户端请求体传入的自定义 Tools，防止内置工具抢占执行':
    'Only allow client-supplied custom tools and prevent built-in ACP tools from intercepting calls',
  '该密钥前置系统指令 (Prepend System Prompt)': 'Key Prepend System Prompt',
  纯透传模式下不注入: 'Inactive in Passthrough Mode',
  当前模式下生效: 'Active in Current Mode',
  '输入仅对该客户端 API Key 前置注入的团队规范、角色设定或语言偏好（留空则不注入额外前置指令）...':
    'Enter custom team rules, persona, or language preferences prepended for this API key (leave blank for none)...',
  '输入仅对该客户端 API Key 前置注入的系统指令（选「全局前置指令」或「原生混合」时生效）...':
    'Enter custom prepend instructions for this API key (active in Prepend Global or Native Hybrid mode)...',
  '3. ACP 会话复用模式 (Session Affinity)': '3. ACP Session Affinity Mode',
  决定该密钥的多轮对话请求如何复用底层保活的:
    'Controls how multi-turn requests from this key reuse warm',
  会话上下文: 'session contexts',
  'ACP 会话复用策略': 'ACP Session Affinity Strategy',
  多轮亲和性: 'Session Affinity',
  'YOLO 全自动 (推荐)': 'YOLO Full Auto (Recommended)',
  '结合历史消息哈希指纹与会话头自动路由至同一空闲进程，兼顾多轮速度与零配置':
    'Automatically routes multi-turn conversations to the same worker process using session ID & history hash',
  'X-Conversation-ID 粘性': 'Sticky X-Conversation-ID',
  '仅当客户端显式携带 X-Conversation-ID 请求头时复用对应会话，否则新建会话':
    'Reuses session only when client sends an explicit session/conversation ID header',
  单轮无状态: 'Stateless Single-Turn',
  '每次请求均创建独立的新会话上下文，不跨轮次保留底层 ACP 侧历史记忆':
    'Creates a fresh session for every request without retaining server-side ACP memory',
  'YOLO 全自动': 'YOLO Full Auto',
  'X-Conversation-ID 粘性复用': 'Sticky Session ID',
  配置密钥独立策略: 'Configure Key Policy',
  '修改该客户端 API Key 的账户绑定、RPM 限流、系统提示词注入模式与 ACP 会话复用策略':
    'Update account binding, RPM rate limit, system prompt injection mode, and ACP session affinity for this key',
  保存密钥策略: 'Save Key Policy',
  已签发密钥与独立策略列表: 'Issued Client API Keys & Policies',
  '点击右侧「配置策略」可随时调整任意密钥的系统提示词注入模式、前置指令、会话复用模式与限流配额':
    'Click "Configure" on any key to adjust its system prompt mode, prepend prompt, session affinity, and RPM limit',
  '密钥名称 / 前缀': 'Key Name / Prefix',
  绑定隔离账户: 'Bound Account',
  提示词注入策略: 'Prompt Injection Policy',
  含前置指令: 'Has Prepend',
  屏蔽内置工具: 'Built-in Disabled',
  会话复用模式: 'Session Affinity',
  'RPM 配额': 'RPM Limit',
  配置策略: 'Configure',
  吊销: 'Revoke',
  '暂未签发客户端 API Key': 'No Client API Keys Issued Yet',
  '点击右上角「签发客户端 API Key」按钮即可弹窗创建密钥并配置独立策略':
    'Click "Issue Client API Key" above to create a key and configure its independent policy',
  请输入客户端密钥名称: 'Please enter a name for the client API key',
  '已签发新的客户端 API Key 并同步至上下文窗口与客户端接入设置，请立即复制保存下方明文密钥':
    'Issued new Client API Key and synced policy settings. Please copy and save the plaintext key below immediately.',
  '签发 API Key 失败': 'Failed to issue API key',
  保存密钥独立策略失败: 'Failed to save key policy',
  '吊销 API Key 失败': 'Failed to revoke API key',

  // Overview Detail Modal
  'agent 提示词与系统指令详情': 'Agent Prompt & System Instructions Detail',
  '查看当前生效的系统提示词注入策略、全局前置指令、挂载的 Skills/MCP 以及最近请求的实际系统指令内容':
    'Inspect active system prompt injection policy, global prepend instructions, mounted Skills/MCP, and latest system prompt payload',
  系统指令: 'System Prompt',
  三大会话协议路由与思考强度兼容详情: 'Multi-Protocol Routing & Reasoning Compatibility',
  'OpenAI 官方 Responses (/v1/responses)、OpenAI 兼容 Chat (/v1/chat/completions) 与 Claude Messages (/v1/messages) 实时统计':
    'Live telemetry for OpenAI Responses (/v1/responses), OpenAI Chat (/v1/chat/completions), and Claude Messages (/v1/messages)',
  '上下文输入 (Prompt) 负载详情': 'Context Input (Prompt) Payload Detail',
  '查看客户端输入的 Prompt Token 构成、会话续接模式及每次请求的实际用户输入内容':
    'Inspect client prompt token breakdown, session continuation mode, and per-request user prompt payloads',
  主力负载: 'Prompt Load',
  当前路由隔离账户详情: 'Routed Isolated Account Detail',
  '查看承接流量的隔离账户容器状态、出口通道、周期模型额度剩余百分比与实时子进程并发':
    'Inspect routed account container status, egress channel, model quota windows, and live worker concurrency',
  隔离账户: 'Isolated Account',
  多账户出口池与网络隔离拓扑: 'Multi-Account Egress Pool & Network Topology',
  '查看所有隔离账户节点的代理模式 (Direct / SOCKS5 / Bind IP / NetNS)、出口 IP、权重与健康状态':
    'Inspect proxy modes (Direct / SOCKS5 / Bind IP / NetNS), egress IPs, weights, and health across all accounts',
  出口池: 'Egress Pool',
  '工具结果与模型生成 (Thinking & Output) 详情':
    'Tool Results & Model Generation (Thinking & Output)',
  '查看模型思考推理过程 (Reasoning/Thinking)、最终生成回复、MCP 工具调用载荷及阶段耗时拆解':
    'Inspect model reasoning/thinking trace, assistant response, MCP tool call payloads, and stage latency breakdown',
  生成与工具: 'Output & Tools',
  '请求与响应全链路检视器 (最新轮次)': 'End-to-End Request & Response Inspector',
  '端到端检视单次请求的协议、路由账户、Token 明细、四段式耗时、系统指令、用户 Prompt、模型思考与输出全文':
    'End-to-end inspection of protocol, routed account, token metrics, 4-stage timing, system prompt, user prompt, thinking trace, and output',
  全链路检视: 'Full Inspector',
  '24h 调用与多维流量统计': '24h Traffic & Call Telemetry',
  '近 24 小时网关总调用次数、有效成功率、账户间改道次数及按模型/账户的调用分布':
    'Last 24h total gateway calls, success rate, account failovers, and per-account traffic distribution',
  '24h 统计': '24h Telemetry',
  历史审计记录流水表: 'Historical Audit Logs',
  '点击任意一条历史调用记录即可检视该次请求的完整输入提示词、模型思考链与响应全文':
    'Click any historical audit record to inspect its full input prompt, reasoning trace, and assistant response',
  历史审计: 'Audit Logs',
  上下文窗口与周期额度剩余空间: 'Context Window & Quota Remaining Capacity',
  '查看当前上下文容量池使用率、预计剩余可容纳同规模请求数及各模型周期配额余量':
    'Inspect context window utilization, estimated remaining requests at current size, and category token breakdown',
  容量测算: 'Capacity',
  '最新轮次 / 请求响应': 'Latest Turn / Req & Resp',
  三大会话协议: '3 Session Protocols',
  '上下文输入 (Prompt)': 'Context Input (Prompt)',
  当前路由账户: 'Routed Account',
  工具结果与模型生成: 'Tools & Generation',
  '24h 调用': '24h Calls',
  历史审计记录: 'Audit History',
  支持点击上方标签在不同模块详情间快速切换:
    'Click any tab above to switch between detail views',
  '前往 API Key 策略配置': 'Go to API Key Policies',
  前往多账户与额度页: 'Go to Accounts & Quotas',
  前往客户端接入中心: 'Go to Client Onboarding',
  关闭详情: 'Close',
  '暂无网关请求审计记录。通过客户端发起一次请求后，此处将展示真实的系统指令、用户输入、思考过程与模型输出。':
    'No gateway request audit logs yet. Send a request from any client to inspect live system instructions, user prompts, thinking traces, and responses.',
  协议与模型: 'Protocol & Model',
  'OpenAI 官方 (Responses)': 'OpenAI Official (Responses)',
  'OpenAI 兼容 (Chat)': 'OpenAI Compatible (Chat)',
  'Claude 协议 (Messages)': 'Claude Protocol (Messages)',
  调度账户与进程: 'Routed Account & Worker',
  'Token 计量 (入 / 出 / 总)': 'Tokens (In / Out / Total)',
  端到端耗时与时间: 'End-to-End Latency & Time',
  '异常错误信息 (Error Message)': 'Error Message',
  '（本轮请求未附带额外系统提示词，采用 pure_passthrough 纯透传模式）':
    '(No additional system prompt in this request; running in pure_passthrough mode)',
  '（历史记录产生于文本审计字段升级前，新发起的请求将完整记录用户输入文本）':
    '(Historical record created before full-text audit capture; new requests record full user prompt text)',
  '（历史记录产生于文本审计字段升级前，新发起的请求将完整记录模型输出文本）':
    '(Historical record created before full-text audit capture; new requests record full assistant output text)',
  '工具调用载荷 (MCP / Client Tool Call)': 'Tool Call Payload (MCP / Client Tool Call)',
  系统指令与技能负载: 'System Prompt & Skills Load',
  生效客户端提示词策略: 'Effective Client Prompt Policy',
  工具注入与内置工具策略: 'Tool Injection & Built-in Tool Policy',
  '当前客户端密钥前置系统指令 (Prepend System Prompt)':
    'Active Client Key Prepend System Prompt',
  '（当前客户端密钥未设置额外前置指令）':
    '(No custom prepend instructions configured for the active client key)',
  '（客户端本轮未传递 system/instructions 字段，按 pure_passthrough 透传）':
    '(Client did not send system/instructions field in this turn; pure_passthrough active)',
  '已挂载的 Skills 与 MCP Server 清单': 'Mounted Skills & MCP Servers',
  'OpenAI 官方协议 (Responses)': 'OpenAI Official Protocol (Responses)',
  'OpenAI 兼容协议 (Chat Completions)': 'OpenAI Compatible Protocol (Chat Completions)',
  'Claude 协议 (Anthropic Messages)': 'Claude Protocol (Anthropic Messages)',
  '思考强度参数格式:': 'Reasoning Effort Parameter Format:',
  'GET /v1/models 实时同步': 'GET /v1/models Live Sync',
  '思考等级:': 'Reasoning Levels:',
  '主力上下文输入 (Prompt)': 'Primary Context Input (Prompt)',
  当前主力调度模型: 'Active Routed Model',
  会话上下文模式: 'Session Affinity Mode',
  '支持 previous_response_id 与历史哈希续接':
    'Supports previous_response_id & history-hash continuation',
  '隔离账户 ID / 名称': 'Isolated Account ID / Name',
  '出口网络与代理 IP': 'Egress Network & Proxy IP',
  '24h Token 消耗 / 调用': '24h Token Usage / Calls',
  保活子进程槽位: 'Warm Worker Process Slots',
  '模型周期额度剩余监控 (Quota Windows)': 'Model Quota Windows',
  '检测出口 IP': 'Probe Egress IP',
  刷新实时额度: 'Refresh Live Quota',
  累计模型生成与工具输出: 'Model Generation & Tool Output',
  最新轮次端到端耗时: 'Latest Turn End-to-End Latency',
  '供应商首字节 (TTFB)': 'Provider First Byte (TTFB)',
  流式生成输出耗时: 'Streaming Output Duration',
  '24h 调用总数': '24h Total Calls',
  '24h Token 吞吐': '24h Token Throughput',
  有效命中率: 'Success Rate',
  账户自动改道次数: 'Account Failovers',
  '按隔离账户统计 24h 调用与 Token 明细': '24h Calls & Token Breakdown by Account',
  '实时落盘 SQLite': 'Persisted in SQLite',
  当前可用上下文剩余空间: 'Remaining Context Capacity',
  已用上下文占比: 'Used Context Percentage',
  预计还可容纳同规模请求: 'Estimated Remaining Requests',
  按单次平均消耗自动动态测算: 'Dynamically estimated from average per-turn token usage',
  容量构成与测算说明: 'Context Capacity Breakdown',
  上下文容量占用比: 'Context capacity utilization',

  // Shared states, errors & confirmations
  复制失败: 'Copy failed',
  '复制失败，已选中文本': 'Copy failed — text selected',
  '重试中...': 'Retrying...',
  '正在处理...': 'Processing...',
  操作失败: 'Operation failed',
  保存失败: 'Save failed',
  修改失败: 'Update failed',
  签发失败: 'Issue failed',
  后台同步失败: 'Background sync failed',
  同步中断: 'Sync interrupted',
  概览加载失败: 'Failed to load overview',
  '后台同步中断，当前显示的数据可能已过期':
    'Background sync interrupted; the data shown may be stale',
  无法确认登录状态: 'Unable to verify sign-in status',
  '无法连接网关服务，请检查网络后重试': 'Unable to reach the gateway. Check your network and retry.',
  '请求网关超时，请稍后重试': 'Gateway request timed out. Please retry later.',
  确认删除隔离账户: 'Delete isolated account?',
  确认吊销: 'Revoke',
  '确认吊销 API Key': 'Revoke API key?',
  '删除后该账户的隔离目录与登录凭证将被移除，此操作不可撤销。':
    "The account's isolated directory and sign-in credentials will be removed. This cannot be undone.",
  '吊销后使用该密钥的客户端将立即无法访问网关，此操作不可撤销。':
    'Clients using this key will lose gateway access immediately. This cannot be undone.',
  删除账户: 'Delete account',
  吊销密钥: 'Revoke key',
  将要删除的账户: 'Account to delete',
  将要吊销的密钥: 'Key to revoke',

  // Auth & sign-in flow
  登录授权失败: 'Sign-in authorization failed',
  重新发起登录: 'Restart sign-in',
  '授权失败，请重新发起登录': 'Authorization failed. Please restart sign-in.',
  读取登录状态失败: 'Failed to read sign-in status',
  '或': 'or',

  // Accounts
  '账户标识 ID (创建后不可修改)': 'Account ID (cannot be changed after creation)',
  留空保持不变: 'Leave blank to keep unchanged',
  '最大并发进程数必须是不小于 1 的整数': 'Max concurrent workers must be an integer of at least 1',
  '调度权重必须是不小于 1 的整数': 'Scheduling weight must be an integer of at least 1',
  '请输入代理地址，例如 socks5://user:pass@127.0.0.1:1080':
    'Enter a proxy URL, e.g. socks5://user:pass@127.0.0.1:1080',
  '请输入要绑定的网卡 IP': 'Enter the interface IP to bind',
  更新模型启用状态失败: 'Failed to update model enablement',
  '新增一个隔离账户后，即可在此查看额度、出口 IP 与运维操作':
    'Add an isolated account to see its quota, egress IP and operations here',
  新增一个隔离账户后即可在此管理: 'Add an isolated account to manage it here',
  代理: 'Proxy',
  上次检测: 'Last probed',
  停用: 'Disable',
  启用: 'Enable',

  // API keys
  '每分钟请求上限 (RPM) 必须是不小于 1 的整数': 'Requests per minute (RPM) must be an integer of at least 1',
  '已签发新的客户端 API Key 并同步至客户端接入设置，请立即复制保存下方明文密钥':
    'New client API key issued and applied to client settings. Copy the plaintext key below now.',
  签发密钥后即可在此配置独立策略: 'Issue a key to configure its policy here',
  '配置密钥独立策略：': 'Key policy: ',
  '前缀：': 'Prefix: ',

  // Overview board
  下一个请求: 'Next request',
  请求模型: 'requested model',
  以: 'with',
  '回答了这个请求 ·': 'answered this request ·',
  '· 对话': '· Chat',
  '· 工具续接': '· Tool continuation',
  内置工具: 'Built-in tools',
  系统提示词附加项: 'System prompt extras',
  工具定义: 'Tool definitions',
  项目指令与技能规范: 'Project instructions & skills',
  工作区文件: 'Workspace files',
  其余工作区文件上下文: 'Other workspace file context',
  其余工具调用返回结果: 'Other tool call results',
  其余会话轮次消息: 'Other conversation turns',
  当前模型上下文窗口剩余可用容量: 'Remaining capacity of the current model context window',
  '· 默认': '· Default',

  // Overview detail modal
  系统提示词详情: 'System Prompt Details',
  查看当前请求生效的系统提示词内容: 'System prompt in effect for the selected request',
  客户端工具定义详情: 'Client Tool Definitions',
  '查看当前请求挂载的客户端工具定义与 Token 占用':
    'Client tool definitions attached to the selected request and their token usage',
  文件上下文详情: 'File Context Details',
  查看当前请求上下文中读取与引用的文件内容: 'Files read or referenced in the selected request context',
  工具调用返回结果详情: 'Tool Result Details',
  查看当前请求上下文中的工具执行输出内容: 'Tool execution output in the selected request context',
  请求与响应详情: 'Request & Response Details',
  '查看当前选中请求的协议、Token 明细、系统指令、用户输入、模型思考与输出全文':
    'Protocol, token breakdown, system prompt, user input, reasoning and full output of the selected request',
  剩余空间详情: 'Remaining Capacity Details',
  查看当前模型上下文窗口剩余可用容量与预计可容纳请求数:
    'Remaining context window capacity and estimated requests that still fit',
  网关系统: 'Gateway system',
  思考规则: 'Thinking rules',
  '（网关未在审计记录中保存该模块的原文）': '(The gateway did not store the raw text of this module)',
  '（本轮请求未附带额外系统提示词）': '(No additional system prompt in this request)',
  '（网关未记录本轮请求的系统提示词原文）': '(The gateway did not record the system prompt for this request)',
  '（当前请求未单独附带 AGENTS.md 或技能列表）': '(No separate AGENTS.md or skill list in this request)',
  上下文模块: 'Context module',
  账户: 'Account',
  '系统提示词 (System Prompt)': 'System Prompt',
  剩余可用空间占比: 'Remaining capacity share',
  上下文剩余可用空间占比: 'Remaining context capacity share',
  模块名称与分类: 'Module & category',
  '模块 Token 占用': 'Module token usage',
  占上下文窗口比例: 'Share of context window',
}

const ZH_TO_EN_PATTERNS: [RegExp, (...m: string[]) => string][] = [
  // Validation, notices & confirmations
  [
    /^新管理员密码长度不能少于\s*(\d+)\s*位$/,
    (_, n) => `New admin password must be at least ${n} characters`,
  ],
  [
    /^自定义 Admin Token 长度不能少于\s*(\d+)\s*位$/,
    (_, n) => `Custom Admin Token must be at least ${n} characters`,
  ],
  [
    /^账户 ID「(.+)」已存在，请使用其他 ID 或编辑现有账户$/,
    (_, id) => `Account ID "${id}" already exists. Use another ID or edit the existing account.`,
  ],
  [/^账户\s*(.+?)\s*授权验证成功，已恢复就绪状态$/, (_, id) => `Account ${id} verified and ready again`],
  [/^账户\s*(.+?)\s*已启用$/, (_, id) => `Account ${id} enabled`],
  [/^账户\s*(.+?)\s*已停用$/, (_, id) => `Account ${id} disabled`],
  [/^已吊销密钥「(.+)」$/, (_, name) => `Revoked key "${name}"`],
  [/^已更新密钥「(.+)」的独立策略$/, (_, name) => `Updated policy for key "${name}"`],
  [/^控制台已加载的最近\s*(\d+)\s*个请求$/, (_, n) => `Latest ${n} requests loaded in the console`],
  [/^已用\s*(\d+)%$/, (_, pct) => `${pct}% used`],
  [/^网关(?:内耗时)?\s*((?:\d+(?:\.\d+)?\s*(?:毫秒|秒))|—)$/, (_, t) => `Gateway ${translateDurationEn(t)}`],
  [/^另外\s*(\d+)\s*项结果$/, (_, n) => `+${n} more results`],
  [
    /^(.+?)\s*·\s*([0-9.]+[KMB]?)\s*token\s*\(占窗口\s*(\d+)%\)$/,
    (_, cat, tok, pct) => `${translateString(cat, 'en')} · ${tok} token (${pct}% of window)`,
  ],
  [
    /^当前请求中「(.+)」模块暂无内容（0 token）。$/,
    (_, name) => `The "${translateString(name, 'en')}" module has no content in this request (0 token).`,
  ],
  [/^模型思考过程 \(Reasoning \/ Thinking Trace(.*)\)$/, (_, rest) => `Reasoning / Thinking Trace${rest}`],
  [/^最近\s*(\d{2}:\d{2})$/, (_, hm) => `Last ${hm}`],
  [
    /^完成回答\s*·\s*([0-9.]+[KMB]?)\s*token\s*·\s*(.+?)\s*后出首字\s*·\s*请求缓存命中率\s*(\d+|—)%。?$/,
    (_, tok, ttfb, cache) =>
      `· ${tok} tokens · ${translateDurationEn(ttfb)} TTFB · ${cache}% cache hit.`,
  ],

  // Top bar & Overview header
  [/^(\d+)\/(\d+)\s*账户正常$/, (_, a, b) => `${a}/${b} Accounts Ready`],
  [/^·\s*启用\s*(\d+)\s*个$/, (_, n) => `· ${n} Enabled`],
  [/^正在处理\s*(\d+)\s*个请求$/, (_, n) => `Processing ${n} Req(s)`],
  [
    /^这里开启了\s*(\d+)\s*个账号或 Key，网关按权重与健康状态在多账号池中自动路由。$/,
    (_, n) =>
      `${n} accounts enabled — gateway automatically routes across healthy accounts by weight.`,
  ],
  [/^(.+?)\s*只启用了一个 Key，无需选择。$/, (_, name) => `${name} has 1 active key; direct routing.`],
  [
    /^(.+?)\s*从\s*(\d+)\s*个启用账号中胜出承接此请求。$/,
    (_, name, n) => `${name} selected from ${n} enabled accounts for this request.`,
  ],
  [/^(.+?)\s*按多账号池权重调度提供服务。$/, (_, name) => `${name} scheduled via weighted pool routing.`],
  [
    /^回答了这个请求\s*·\s*(.+)$/,
    (_, s) => `Served request · ${translateString(s, 'en')}`,
  ],

  // Context Window & Treemap
  [
    /^已用\s*(\d+)%\s*·\s*约还能容纳\s*(\d+)\s*个请求$/,
    (_, pct, rem) => `${pct}% used · ~${rem} more requests at current size`,
  ],
  [
    /^([0-9.]+[KMB]?)\s*来自缓存\s*·\s*([0-9.]+[KMB]?)\s*新增$/,
    (_, c, n) => `${c} cached · ${n} new`,
  ],
  [
    /^(.+?)\s*·\s*([0-9.]+[KMB]?)\s*token\s*·\s*(\d+)%$/,
    (_, cat, tok, pct) => `${translateString(cat, 'en')} · ${tok} token · ${pct}%`,
  ],
  [/^([0-9.]+[KMB]?)\s*·\s*系统提示词$/, (_, v) => `${v} · System Prompt`],
  [/^([0-9.]+[KMB]?)\s*·\s*工具$/, (_, v) => `${v} · Tools`],
  [/^([0-9.]+[KMB]?)\s*·\s*技能$/, (_, v) => `${v} · Skills`],
  [/^([0-9.]+[KMB]?)\s*·\s*文件$/, (_, v) => `${v} · Files`],
  [/^([0-9.]+[KMB]?)\s*·\s*工具结果$/, (_, v) => `${v} · Tool Results`],
  [/^([0-9.]+[KMB]?)\s*·\s*会话内容$/, (_, v) => `${v} · Conversation`],
  [/^系统提示词\s+([0-9.]+[KMB]?)$/, (_, v) => `System Prompt ${v}`],
  [/^工具\s+([0-9.]+[KMB]?)$/, (_, v) => `Tools ${v}`],
  [/^技能\s+([0-9.]+[KMB]?)$/, (_, v) => `Skills ${v}`],
  [/^文件\s+([0-9.]+[KMB]?)$/, (_, v) => `Files ${v}`],
  [/^工具结果\s+([0-9.]+[KMB]?)$/, (_, v) => `Tool Results ${v}`],
  [/^会话内容\s+([0-9.]+[KMB]?)$/, (_, v) => `Conversation ${v}`],
  [/^剩余空间\s+([0-9.]+[KMB]?)$/, (_, v) => `Remaining ${v}`],
  [/^另外\s*(\d+)\s*项系统提示词$/, (_, n) => `+${n} more system prompts`],
  [/^另外\s*(\d+)\s*项工具$/, (_, n) => `+${n} more tools`],
  [/^另外\s*(\d+)\s*个客户端工具$/, (_, n) => `+${n} more client tools`],
  [/^另外\s*(\d+)\s*个文件$/, (_, n) => `+${n} more files`],
  [/^另外\s*(\d+)\s*项会话$/, (_, n) => `+${n} more turns`],
  [/^更早的\s*(\d+)\s*轮$/, (_, n) => `${n} earlier turns`],
  [/^第\s*(\d+)\s*轮$/, (_, n) => `Turn #${n}`],
  [
    /^策略\s*([a-zA-Z0-9_-]+)(?:\s*·\s*Key:(.+))?$/,
    (_, mode, key) => `Policy ${mode}${key ? ` · Key:${key}` : ''}`,
  ],
  [/^显示全部\s*(\d+)\s*项$/, (_, n) => `Show all ${n} items`],
  [
    /^请求\s*#(\d+)\s*·\s*([0-9.]+[KMB]?)\s*token\s*\(点击切换检视该请求\)$/,
    (_, idx, tok) => `Request #${idx} · ${tok} token (Click to inspect)`,
  ],
  [
    /^共\s*(\d+)\s*次请求\s*·\s*峰值上下文\s*([0-9.]+[KMB]?)\s*token$/,
    (_, c, tok) => `${c} requests · Peak context ${tok} token`,
  ],
  [/^(\d+)\s*轮$/, (_, n) => `${n} turns`],
  [/^(\d+)\s*轮请求$/, (_, n) => `${n} turns`],
  [/^(\d+)\s*个请求$/, (_, n) => `${n} requests`],
  [/^约\s*(\d+)\s*个请求$/, (_, n) => `~${n} requests`],
  [/^(\d+)\s*次$/, (_, n) => `${n} calls`],
  [
    /^网关保留的最近\s*(\d+)\s*个请求$/,
    (_, n) => `Last ${n} requests retained by gateway`,
  ],
  [/^在线\s*(\d+)\s*个进程$/, (_, n) => `${n} Active Worker(s)`],
  [
    /^(\d+)月(\d+)日\s*(周[日一二三四五六])$/,
    (_, m, d, w) => `${WEEKDAY_MAP[w] || w}, ${MONTH_NAMES[Number(m) - 1] || m} ${d}`,
  ],
  [
    /^最近\s*(周[日一二三四五六])\s*(\d{2}:\d{2})$/,
    (_, w, hm) => `Last ${WEEKDAY_MAP[w] || w} ${hm}`,
  ],
  [
    /^最近应答\s*(周[日一二三四五六])\s*(\d{2}:\d{2})$/,
    (_, w, hm) => `Last ${WEEKDAY_MAP[w] || w} ${hm}`,
  ],
  [/^首字耗时\s*(.+)$/, (_, t) => `TTFB ${translateDurationEn(t)}`],
  [/^累计输出\s*(.+)$/, (_, t) => `Total Out ${t}`],
  [/^输出\s*(.+)$/, (_, t) => `Out ${t}`],
  [/^缓存\s*(\d+%)$/, (_, p) => `Cache ${p}`],
  [/^(\d+)\s*token\/秒$/, (_, n) => `${n} tok/s`],
  [/^(\d+)\s*失败$/, (_, n) => `${n} failed`],
  [/^网关\s*(\d+)\s*毫秒$/, (_, ms) => `Gateway ${ms}ms`],
  [/^网关内耗时\s*(\d+)\s*毫秒$/, (_, ms) => `Gateway ${ms}ms`],
  [/^网络\s*(.+)$/, (_, t) => `Network ${translateDurationEn(t)}`],
  [/^发送请求\s*(.+)$/, (_, t) => `Send Req ${translateDurationEn(t)}`],
  [/^供应商（首字节）\s*(.+)$/, (_, t) => `Provider TTFB ${translateDurationEn(t)}`],
  [/^供应商\s*(.+)$/, (_, t) => `Provider ${translateDurationEn(t)}`],
  [/^流式输出\s*(.+)$/, (_, t) => `Streaming ${translateDurationEn(t)}`],
  [/^(\d+(?:\.\d+)?)\s*毫秒$/, (_, v) => `${v}ms`],
  [/^(\d+(?:\.\d+)?)\s*秒$/, (_, v) => `${v}s`],
  [/^(\d+)\s*分\s*(\d+)\s*秒$/, (_, m, s) => `${m}m ${s}s`],
  [/^(\d+)\s*分钟前$/, (_, m) => `${m}m ago`],
  [/^(\d+)\s*小时前$/, (_, h) => `${h}h ago`],
  [/^(\d+)\s*天前$/, (_, d) => `${d}d ago`],
  [/^会话\s*#([0-9a-zA-Z_-]+)$/, (_, id) => `Session #${id}`],
  [/^会话 ID:\s*(.+)$/, (_, id) => `Session ID: ${id}`],
  [/^客户端会话 ID:\s*(.+)$/, (_, id) => `Client Session ID: ${id}`],
  [/^绑定会话 ID:\s*(.+)$/, (_, id) => `Bound Session ID: ${id}`],

  // Overview Routing Trace Bullets (split text nodes & full phrases — placed after specific "X 个请求" patterns)
  [/^([^\u4e00-\u9fa5]+?)\s*请求$/, (_, client) => `${client} requested`],
  [/^(.+?)\s*\((.+?)\)\s*以$/, (_, name, model) => `${name} (${model}) with`],
  [
    /^完成回答\s*·\s*([0-9.]+[KMB]?)\s*token\s*·\s*(.+?)\s*后出首字\s*·\s*请求缓存命中率\s*(\d+)%。?$/,
    (_, tok, ttfb, cache) =>
      `· ${tok} tokens · ${translateDurationEn(ttfb)} TTFB · ${cache}% cache hit.`,
  ],

  // Accounts & Quotas Card & Table & Modals
  [/^启用或停用账户\s*(.+)$/, (_, id) => `Enable or disable account ${id}`],
  [
    /^代理\s+(.+)$/,
    (_, p) => `Proxy ${p.replace('direct://本地直连', 'direct://local')}`,
  ],
  [
    /^出口\s+(.+)$/,
    (_, ip) => `Egress ${ip === '待检测' ? 'Pending Probe' : ip === '本地直连' || ip === '直连' ? 'Direct' : ip}`,
  ],
  [
    /^额度\s*·\s*(.+?)读取$/,
    (_, ago) => `Quota · Synced ${translateAgoEn(ago)}`,
  ],
  [
    /^(.+?)同步$/,
    (_, ago) => `Synced ${translateAgoEn(ago)}`,
  ],
  [
    /^上次检测\s*(.+?):$/,
    (_, ago) => `Checked ${translateAgoEn(ago)}:`,
  ],
  [
    /^(\d+)\s*天\s*(\d+)\s*小时后重置$/,
    (_, d, h) => `Resets in ${d}d ${h}h`,
  ],
  [
    /^(\d+)\s*小时\s*(\d+)\s*分后重置$/,
    (_, h, m) => `Resets in ${h}h ${m}m`,
  ],
  [/^(\d+)\s*分后重置$/, (_, m) => `Resets in ${m}m`],
  [/^(.+?)\s*重置$/, (_, t) => `Resets at ${t}`],
  [
    /^尚未读取到模型额度（(.+)）$/,
    (_, msg) => `Model quota not synced yet (${translateString(msg, 'en')})`,
  ],
  [/^快速更换代理\s*\((.+)\)$/, (_, id) => `Quick Proxy Switch (${id})`],
  [/^编辑隔离账户:\s*(.+)$/, (_, id) => `Edit Isolated Account: ${id}`],
  [/^目录:\s*(.+)$/, (_, p) => `Dir: ${p}`],
  [/^启用\s*(\d+)\/(\d+)$/, (_, a, b) => `Enabled ${a}/${b}`],
  [/^已启用\s*(\d+)\s*\/\s*(\d+)$/, (_, a, b) => `Enabled ${a} / ${b}`],
  [/^禁用\s*(\d+)$/, (_, n) => `Disabled ${n}`],
  [/^已禁用\s*(\d+)$/, (_, n) => `Disabled ${n}`],
  [
    /^ACP 后端实时可用模型\s*—\s*(.+)$/,
    (_, acc) => `Live ACP Available Models — ${acc}`,
  ],
  [/^([a-zA-Z0-9_-]+)\s*·\s*默认$/, (_, lvl) => `${lvl} · Default`],
  [
    /^在账户\s*(.+?)\s*下启用或禁用模型\s*(.+)$/,
    (_, acc, m) => `Enable or disable model ${m} under account ${acc}`,
  ],
  [
    /^(\S+)\s+(.+?)\s+(每周|5\s*小时|每日)\s*剩余额度$/,
    (_, id, grp, win) => `${id} ${translateString(grp, 'en')} ${translateString(win, 'en')} remaining quota`,
  ],

  // Status & Error Notices in App.tsx
  [/^出口检测异常:\s*(.+)$/, (_, e) => `Egress probe error: ${e}`],
  [/^启动 ACP 进程失败:\s*(.+)$/, (_, e) => `Failed to start ACP process: ${e}`],
  [/^创建 ACP 会话失败:\s*(.+)$/, (_, e) => `Failed to create ACP session: ${e}`],
  [/^执行 \/usage 失败:\s*(.+)$/, (_, e) => `Failed to run /usage: ${e}`],
  [/^账户\s*(.+?)\s*已保存并同步隔离目录$/, (_, id) => `Account ${id} saved and isolated directory synced`],
  [/^账户\s*(.+?)\s*已(启用|停用)$/, (_, id, st) => `Account ${id} ${st === '启用' ? 'enabled' : 'disabled'}`],
  [/^账户\s*(.+?)\s*已删除$/, (_, id) => `Account ${id} deleted`],
  [/^账户\s*(.+?)\s*检测通过，出口 IP:\s*(.+)$/, (_, id, ip) => `Account ${id} probe passed — Egress IP: ${ip}`],
  [/^已刷新账户\s*(.+?)\s*的模型额度状态$/, (_, id) => `Refreshed model quota for account ${id}`],
  [
    /^已清除账户\s*(.+?)\s*的冷却限制并恢复就绪状态$/,
    (_, id) => `Cleared cooldown for account ${id} and restored ready state`,
  ],
  [
    /^账户\s*(.+?)\s*已实时同步\s*(\d+)\s*个 ACP 可用模型$/,
    (_, id, n) => `Account ${id} synced ${n} live ACP models`,
  ],
  [
    /^已在账户\s*(.+?)\s*下禁用模型\s*(.+)$/,
    (_, id, m) => `Disabled model ${m} on account ${id}`,
  ],
  [
    /^已在账户\s*(.+?)\s*下恢复启用模型\s*(.+)$/,
    (_, id, m) => `Re-enabled model ${m} on account ${id}`,
  ],
  [
    /^已恢复启用账户\s*(.+?)\s*下的全部 ACP 模型$/,
    (_, id) => `Re-enabled all ACP models on account ${id}`,
  ],
  [
    /^账户\s*(.+?)\s*的代理与出口配置已热更新$/,
    (_, id) => `Hot-reloaded proxy & egress configuration for account ${id}`,
  ],
  [
    /^已为账户\s*(.+?)\s*启动重新登录会话$/,
    (_, id) => `Started terminal re-login session for account ${id}`,
  ],
  [
    /^已向账户\s*(.+?)\s*提交授权验证码$/,
    (_, id) => `Submitted OAuth verification code for account ${id}`,
  ],
  [
    /^已更新密钥「(.+?)」的独立策略，并已同步刷新上下文窗口与客户端设置$/,
    (_, name) => `Updated policy for key "${name}" and synced context window & client settings`,
  ],

  // Overview Detail Modal Dynamic Strings (placed before general `(.+?) (.*)` pattern)
  [
    /^切换检视轮次\s*\(最近\s*(\d+)\s*笔请求\)$/,
    (_, n) => `Switch Inspected Turn (Last ${n} Requests)`,
  ],
  [/^当前:\s*(.+)$/, (_, id) => `Current: ${id}`],
  [/^账户\s+(.+)$/, (_, id) => `Account ${id}`],
  [/^PID\s*(.+?)\s*·\s*状态\s*(\d+)$/, (_, pid, code) => `PID ${pid} · Status ${code}`],
  [/^请求 ID:\s*(.+)$/, (_, id) => `Request ID: ${id}`],
  [
    /^系统提示词\s*\(System Prompt\s*·\s*策略\s*(.+)\)$/,
    (_, pol) => `System Prompt (Policy: ${pol})`,
  ],
  [
    /^（当前客户端密钥策略为\s*(.+?)，由网关自动挂载 Antigravity 原生系统设定与项目规则）$/,
    (_, mode) =>
      `(Active client key policy is ${mode}; gateway automatically mounts Antigravity native system persona & project rules)`,
  ],
  [
    /^用户输入内容\s*\(User Prompt\s*·\s*(\d+)\s*tokens\)$/,
    (_, tok) => `User Prompt (${tok} tokens)`,
  ],
  [
    /^模型思考过程\s*\(Reasoning \/ Thinking Trace(.*)\)$/,
    (_, rest) => `Reasoning / Thinking Trace${rest}`,
  ],
  [
    /^模型生成输出\s*\(Assistant Response\s*·\s*(\d+)\s*tokens\)$/,
    (_, tok) => `Assistant Response (${tok} tokens)`,
  ],

  // Tab 3 & Tab 4 Dynamic Options & Headers
  [
    /^关联已签发密钥策略\s*\(共\s*(\d+)\s*个\)\.\.\.$/,
    (_, n) => `Link issued API key policy (${n} total)...`,
  ],
  [
    /^(.+?)\s*\((.+?)\s*·\s*(.+?)\)$/,
    (_, name, pfx, mode) => `${translateString(name, 'en')} (${pfx} · ${translateString(mode, 'en')})`,
  ],
  [
    /^切换可用模型\s*\((\d+)\s*个未禁用\)\.\.\.$/,
    (_, n) => `Switch available model (${n} enabled)...`,
  ],
  [/^定点:\s*(.+)$/, (_, id) => `Pinned: ${id}`],
  [/^配置密钥独立策略:\s*(.+)$/, (_, name) => `Configure Key Policy: ${name}`],
  [/^前缀:\s*(.*)$/, (_, pfx) => `Prefix: ${pfx}`],
  [/^共\s*(\d+)\s*个密钥$/, (_, n) => `${n} Key(s)`],
  [
    /^系统提示词\s*([0-9.]+[KMB]?)\s*·\s*技能\s*([0-9.]+[KMB]?)\s*\(占输入\s*(\d+)%\)$/,
    (_, s, sk, pct) => `System Prompt ${s} · Skills ${sk} (${pct}% of input)`,
  ],
  [
    /^会话模式:\s*([a-zA-Z0-9_-]+)(?:\s*·\s*密钥:\s*(.+))?$/,
    (_, sm, k) => `Session Mode: ${sm}${k ? ` · Key: ${k}` : ''}`,
  ],
  [
    /^内置工具屏蔽:\s*(.+)$/,
    (_, st) =>
      `Built-in Tools: ${st.includes('已禁用') ? 'Disabled (Client Tools Only)' : 'Enabled'}`,
  ],
  [
    /^最近一次请求实际生效的系统指令\s*\((.+)\)$/,
    (_, id) => `Effective System Prompt for Latest Request (${id})`,
  ],
  [
    /^（按客户端密钥策略\s*(.+?)\s*注入 Antigravity 原生系统指令与思考规范）$/,
    (_, mode) =>
      `(Injected Antigravity native system instructions & reasoning rules per key policy ${mode})`,
  ],
  [/^近期调用:\s*(\d+)\s*次$/, (_, n) => `Recent Calls: ${n}`],
  [
    /^ACP 后端实时发现模型与支持的思考等级\s*\((\d+)\s*个模型\)$/,
    (_, n) => `Live Discovered ACP Models & Supported Reasoning Levels (${n} models)`,
  ],
  [/^总输入提示词:\s*([0-9.]+[KMB]?)\s*token$/, (_, t) => `Total Input Prompt: ${t} token`],
  [
    /^状态:\s*(.+?)\s*·\s*权重\s*W=(\d+)$/,
    (_, st, w) => `Status: ${st} · Weight W=${w}`,
  ],
  [/^模式:\s*(.+)$/, (_, m) => `Mode: ${m}`],
  [
    /^今日成功\s*(\d+)\s*次\s*·\s*失败\s*(\d+)\s*次$/,
    (_, ok, fail) => `Today OK: ${ok} · Failed: ${fail}`,
  ],
  [
    /^(\d+)\s*忙碌\s*\/\s*(\d+)\s*已拉起\s*\(上限\s*(\d+)\)$/,
    (_, busy, up, max) => `${busy} busy / ${up} spawned (max ${max})`,
  ],
  [
    /^全部隔离账户与出口节点列表\s*\((\d+)\s*个节点\)$/,
    (_, n) => `All Isolated Accounts & Egress Nodes (${n} nodes)`,
  ],
  [/^(\d+)\s*个节点就绪$/, (_, n) => `${n} Node(s) Ready`],
  [
    /^出口:\s*(.+)$/,
    (_, ip) => `Egress: ${ip === '直连' || ip === '本地直连' ? 'Direct' : ip}`,
  ],
  [/^权重:\s*W=(\d+)$/, (_, w) => `Weight: W=${w}`],
  [/^进程:\s*(\d+)\/(\d+)$/, (_, a, b) => `Workers: ${a}/${b}`],
  [/^今日请求:\s*(\d+)\s*次$/, (_, n) => `24h Req: ${n}`],
  [/^成功\/失败:\s*(\d+)\s*\/\s*(\d+)$/, (_, a, b) => `OK/Fail: ${a} / ${b}`],
  [/^入\/出 Token:\s*(.+)$/, (_, t) => `In/Out Tokens: ${t}`],
  [
    /^最近\s*(\d+)\s*笔网关调用审计流水\s*\(点击任意行展开下方完整请求\/响应报文\)$/,
    (_, n) => `Last ${n} Gateway Audit Logs (Click any row to inspect full request & response below)`,
  ],
  [/^总窗口容量:\s*([0-9.]+[KMB]?)\s*token$/, (_, t) => `Total Window Capacity: ${t} token`],
  [/^已消耗:\s*([0-9.]+[KMB]?)\s*token$/, (_, t) => `Consumed: ${t} token`],
  [/^系统提示词:\s*(.+)$/, (_, t) => `System Prompt: ${t}`],
  [/^工具定义:\s*(.+)$/, (_, t) => `Tool Definitions: ${t}`],
  [/^技能规范:\s*(.+)$/, (_, t) => `Skill Rules: ${t}`],
  [/^文件上下文:\s*(.+)$/, (_, t) => `File Context: ${t}`],
  [/^工具结果:\s*(.+)$/, (_, t) => `Tool Results: ${t}`],
  [/^会话内容:\s*(.+)$/, (_, t) => `Conversation: ${t}`],
  [/^模型生成输出:\s*(.+)$/, (_, t) => `Assistant Output: ${t}`],
  [/^剩余可用容量:\s*(.+)$/, (_, t) => `Remaining Capacity: ${t}`],
  [
    /^当前仅展示「(.+?)」模块自身内容$/,
    (_, name) => `Showing only content for module "${name}"`,
  ],
  [
    /^精确计量:\s*(.+?)\s*token$/,
    (_, n) => `Exact Count: ${n} token`,
  ],
  [
    /^包含\s*(\d+)\s*个子项$/,
    (_, n) => `Contains ${n} sub-items`,
  ],
  [
    /^当前窗口容量\s*([0-9.]+[KMB]?)$/,
    (_, cap) => `Window Capacity ${cap}`,
  ],
]

const CODE_SNIPPET_REPLACEMENTS: [string, string][] = [
  ['# 终端导出密钥后直接运行 codex:', '# Export your API key in terminal and run codex:'],
  [
    '# 实时从 ACP 后端获取可用模型（缓存有效期内直接命中内存缓存）',
    '# Fetch available models from ACP backend (served from memory cache within TTL)',
  ],
  ['# 第一轮请求', '# Turn 1 request'],
  ['instructions="你是一个严谨的代码架构助手。"', 'instructions="You are a rigorous software architecture assistant."'],
  ['input="请分析当前仓库的模块结构。"', 'input="Analyze the module structure of this repository."'],
  [
    '# 第二轮直接通过 previous_response_id 续接同一 agy acp 会话',
    '# Turn 2 continues the same warm agy acp session via previous_response_id',
  ],
  [
    'input="基于刚才的分析，指出核心路由入口文件。"',
    'input="Based on your analysis, identify the core router entry file."',
  ],
  ['"instructions": "请用中文简洁回答。"', '"instructions": "Please answer concisely."'],
  [
    '{ "type": "input_text", "text": "你好，请验证 OpenAI 官方 Responses 会话协议。" }',
    '{ "type": "input_text", "text": "Hello, please verify the OpenAI Official Responses protocol." }',
  ],
  [
    '# 默认读取缓存（TTL 内零延迟，未命中或过期时实时向 agy acp 查询）：',
    '# Read cached model list (zero latency within TTL; queries agy acp on miss):',
  ],
  [
    '# 附加 ?refresh=1 强制穿透缓存，实时从 ACP 后端重新拉取模型：',
    '# Append ?refresh=1 to bypass cache and fetch live models from ACP backend:',
  ],
  [
    '{"role": "user", "content": "请总结多账户隔离池的工作方式。"}',
    '{"role": "user", "content": "Summarize how the multi-account isolation pool works."}',
  ],
  [
    '{ "role": "system", "content": "你是一个专业服务端工程师。" }',
    '{ "role": "system", "content": "You are a senior backend engineer." }',
  ],
  [
    '{ "role": "user", "content": "测试 OpenAI 兼容 Chat Completions 协议流式输出。" }',
    '{ "role": "user", "content": "Test OpenAI-compatible Chat Completions streaming output." }',
  ],
  [
    '# 获取全部 ACP 可用模型列表 (带 TTL 缓存)：',
    '# List all available ACP models (with TTL cache):',
  ],
  ['# 查询单个模型详情：', '# Retrieve details for a single model:'],
  [
    '# 在终端配置 Claude Code 直连当前 Antigravity 网关：',
    '# Configure Claude Code CLI in terminal to connect to Antigravity Gateway:',
  ],
  [
    '# 启动 Claude Code CLI（支持完整多轮对话与本地工具调用）：',
    '# Launch Claude Code CLI (supports multi-turn chat and local tool calls):',
  ],
  [
    '# 通过 Claude 协议 client.models.list() 获取 ACP 实时可用模型',
    '# Fetch live ACP models via Anthropic SDK client.models.list()',
  ],
  ['system="你是一个资深系统架构师。"', 'system="You are a senior systems architect."'],
  [
    '{"role": "user", "content": "请检查当前项目的协议转换设计。"}',
    '{"role": "user", "content": "Review the protocol translation design of this project."}',
  ],
  ['{ "type": "text", "text": "请用中文回答。" }', '{ "type": "text", "text": "Please answer concisely." }'],
  [
    '{ "role": "user", "content": "你好，请验证 Claude /v1/messages 会话协议。" }',
    '{ "role": "user", "content": "Hello, please verify the Claude /v1/messages protocol." }',
  ],
  [
    '# 使用 Claude 协议标准请求头 (x-api-key + anthropic-version) 获取 ACP 可用模型：',
    '# List available ACP models using standard Claude headers (x-api-key + anthropic-version):',
  ],
  [
    '# 强制跳过缓存实时向 agy acp 重新探测可用模型：',
    '# Bypass cache and probe live models from agy acp:',
  ],
]

function translateDurationEn(s: string): string {
  return s
    .replace(/(\d+(?:\.\d+)?)\s*毫秒/g, '$1ms')
    .replace(/(\d+)\s*分\s*(\d+)\s*秒/g, '$1m $2s')
    .replace(/(\d+(?:\.\d+)?)\s*秒/g, '$1s')
}

function translateAgoEn(s: string): string {
  const t = s.trim()
  if (t === '刚刚') return 'just now'
  if (t === '尚未') return 'never'
  return t
    .replace(/(\d+)\s*分钟前/g, '$1m ago')
    .replace(/(\d+)\s*小时前/g, '$1h ago')
    .replace(/(\d+)\s*天前/g, '$1d ago')
}

export function translateString(raw: string, lang: Lang): string {
  if (lang === 'zh' || !raw) return raw
  const trimmed = raw.trim()
  if (!trimmed || !/[\u4e00-\u9fa5]/.test(trimmed)) return raw

  const leadingMatch = raw.match(/^\s*/)
  const trailingMatch = raw.match(/\s*$/)
  const leading = leadingMatch ? leadingMatch[0] : ''
  const trailing = trailingMatch ? trailingMatch[0] : ''
  const norm = trimmed.replace(/\s+/g, ' ')

  const exact = ZH_TO_EN_EXACT[trimmed] ?? ZH_TO_EN_EXACT[norm]
  if (exact) {
    return `${leading}${exact}${trailing}`
  }

  if (!trimmed.includes('\n')) {
    for (const [regex, replacer] of ZH_TO_EN_PATTERNS) {
      const match = norm.match(regex)
      if (match) {
        const replaced = replacer(...match)
        return replaced === norm ? raw : `${leading}${replaced}${trailing}`
      }
    }
  }

  // Translate known code snippet comments & sample prompts
  let out = raw
  let changed = false
  for (const [zhSnippet, enSnippet] of CODE_SNIPPET_REPLACEMENTS) {
    if (out.includes(zhSnippet)) {
      out = out.split(zhSnippet).join(enSnippet)
      changed = true
    }
  }
  if (changed) {
    return out
  }

  return raw
}

const originalTextMap = new WeakMap<Node, { zh: string; en: string }>()
const TRANSLATABLE_ATTRS = ['placeholder', 'title', 'aria-label'] as const
const SKIP_SELECTOR = '[translate="no"], [data-i18n-skip]'

function isInSkippedSubtree(el: Element | null): boolean {
  return Boolean(el?.closest(SKIP_SELECTOR))
}

export function useAutoI18n(lang: Lang): void {
  useEffect(() => {
    setStoredLang(lang)
    const root = document.getElementById('root')
    if (!root) return

    let isApplying = false

    const processNode = (node: Node) => {
      if (node.nodeType === Node.TEXT_NODE) {
        const parentTag = node.parentElement?.tagName
        if (parentTag === 'SCRIPT' || parentTag === 'STYLE') {
          return
        }
        const currentVal = node.nodeValue || ''
        let record = originalTextMap.get(node)
        if (isInSkippedSubtree(node.parentElement)) {
          if (record && record.en !== record.zh && currentVal === record.en) {
            node.nodeValue = record.zh
          }
          return
        }

        if (!record) {
          const enVal = translateString(currentVal, 'en')
          record = { zh: currentVal, en: enVal }
          originalTextMap.set(node, record)
        } else if (currentVal !== record.zh && currentVal !== record.en) {
          // React updated this Text node with fresh dynamic data (e.g. '0' -> '242K' or '无文件引用' -> 'OverviewBoard.tsx')
          const enVal = translateString(currentVal, 'en')
          record = { zh: currentVal, en: enVal }
          originalTextMap.set(node, record)
        }

        const targetVal = lang === 'en' ? record.en : record.zh
        if (node.nodeValue !== targetVal) {
          node.nodeValue = targetVal
        }
        return
      }

      if (node.nodeType === Node.ELEMENT_NODE) {
        const el = node as HTMLElement
        if (isInSkippedSubtree(el)) return
        for (const attr of TRANSLATABLE_ATTRS) {
          const val = el.getAttribute(attr)
          if (!val) continue
          const dataZhKey = `data-i18n-zh-${attr}`
          const dataEnKey = `data-i18n-en-${attr}`
          let zhAttr = el.getAttribute(dataZhKey)
          let enAttr = el.getAttribute(dataEnKey)

          if (!zhAttr || (val !== zhAttr && val !== enAttr)) {
            zhAttr = val
            enAttr = translateString(val, 'en')
            el.setAttribute(dataZhKey, zhAttr)
            el.setAttribute(dataEnKey, enAttr)
          }

          const targetAttr = lang === 'en' ? (enAttr ?? translateString(zhAttr, 'en')) : zhAttr
          if (val !== targetAttr) {
            el.setAttribute(attr, targetAttr)
          }
        }
        for (let child = el.firstChild; child; child = child.nextSibling) {
          processNode(child)
        }
      }
    }

    const runPass = () => {
      if (isApplying) return
      isApplying = true
      try {
        processNode(root)
      } finally {
        isApplying = false
      }
    }

    runPass()

    if (lang === 'zh') {
      // In Chinese mode, once any English translations have been restored to their Chinese originals,
      // do not keep a MutationObserver running so React owns the DOM directly with zero interference.
      return
    }

    const observer = new MutationObserver((mutations) => {
      if (isApplying) return
      isApplying = true
      try {
        for (const m of mutations) {
          if (m.type === 'characterData' || m.type === 'attributes') {
            processNode(m.target)
          } else if (m.type === 'childList') {
            m.addedNodes.forEach((n) => processNode(n))
          }
        }
      } finally {
        isApplying = false
      }
    })

    observer.observe(root, {
      childList: true,
      subtree: true,
      characterData: true,
      attributes: true,
      attributeFilter: ['placeholder', 'title', 'aria-label'],
    })

    return () => observer.disconnect()
  }, [lang])
}
