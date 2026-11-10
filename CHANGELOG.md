# Changelog / 版本变更日志

**English** | [简体中文](#简体中文)

All notable changes to **Antigravity ACP Gateway** are documented in this file.
This project adheres to [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

### Security / 安全加固
- **Northbound API requires an API key except from loopback / 北向 API 除回环地址外必须使用 API Key**:
  - `/v1/*` no longer allows unauthenticated access when no API keys exist, except from `127.0.0.1` / `::1`. Create an API key in the console first. `?key=` is still accepted for the northbound API; the `?token=` query parameter is no longer accepted anywhere.
  - 未创建任何 API Key 时，`/v1/*` 仅允许 `127.0.0.1` / `::1` 免密访问，请先在控制台创建 API Key。`?key=` 仍可用于北向 API，`?token=` 查询参数已全面停用。
- **Per-key account binding & session isolation / API Key 账户绑定与会话隔离**:
  - An API key bound to an account can no longer be redirected to another account via `X-ACP-Account-ID` or `?account_id=`; sessions are isolated per API key.
  - 绑定账户的 API Key 无法再通过 `X-ACP-Account-ID` 或 `?account_id=` 改投其他账户，会话按 API Key 隔离。
- **ACP WebSocket workspace confinement / ACP WebSocket 工作区约束**:
  - `/v1/acp/ws` always runs sessions inside a gateway-managed workspace directory; a client-provided `cwd` outside it is rewritten.
  - `/v1/acp/ws` 会话始终运行在网关管理的工作区目录内，工作区外的客户端 `cwd` 会被改写。
- **Admin credentials hardening / 管理凭证加固**:
  - The admin password and token are no longer printed to logs; read `<data_dir>/admin_credentials.txt` (mode `0600`). The data dir is created with mode `0700`.
  - The admin password is stored as salted PBKDF2-SHA256 (legacy SHA-256 hashes are upgraded on the next successful login); minimum 8 characters, custom admin token minimum 12 characters.
  - `Authorization: Bearer` for admin APIs accepts the admin token or a session token, not the password. Failed credential attempts are rate-limited per IP on all endpoints.
  - 管理员密码与 Token 不再打印到日志，请查看 `<data_dir>/admin_credentials.txt`（权限 `0600`）；数据目录以 `0700` 创建。密码改为加盐 PBKDF2-SHA256 存储（旧 SHA-256 哈希在下次成功登录时自动升级），密码至少 8 位、自定义 Token 至少 12 位；管理 API 的 Bearer 仅接受管理 Token 或会话 Token，不再接受密码；所有端点的凭证失败尝试按 IP 限速。
- **Admin API CSRF / origin protection / 管理接口跨站防护**:
  - State-changing admin endpoints require POST/PUT/DELETE; cross-origin state-changing requests are rejected; the session cookie is `SameSite=Strict`; WebSocket upgrades check `Origin`.
  - 改变状态的管理接口必须使用 POST/PUT/DELETE，拒绝跨站状态变更请求，会话 Cookie 改为 `SameSite=Strict`，WebSocket 升级校验 `Origin`。
- **Secret redaction / 敏感字段脱敏**:
  - Admin API responses no longer contain account `api_key`; proxy passwords are masked as `********`.
  - 管理 API 响应不再返回账户 `api_key`，代理密码显示为 `********`。
- **Subprocess & internal endpoint isolation / 子进程与内部端点隔离**:
  - Agent subprocesses no longer inherit `AG_*` environment variables. The internal MCP bridge `/mcp/sessions/*` only accepts loopback connections. Account IDs are restricted to `[A-Za-z0-9._-]`, 1–64 characters.
  - ACP 子进程不再继承 `AG_*` 环境变量；内部 MCP 桥 `/mcp/sessions/*` 仅接受回环连接；账户 ID 限定为 `[A-Za-z0-9._-]`，长度 1–64。
- **Deployment hardening / 部署加固**:
  - `deploy.sh --mode service` creates a dedicated `antigravity` system user, installs root-owned binaries to `/usr/local/bin` (the service always runs `/usr/local/bin/agy`), and ships a hardened systemd unit (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome=tmpfs`, `PrivateTmp`, empty capabilities, `UMask=0077`, `StateDirectory`). Data lives in `/var/lib/antigravity-gateway/data`, the agent workspace in `/var/lib/antigravity-gateway/workspace`. Account `network_mode: netns` is not available under this unit.
  - The Docker image runs as non-root `agy` (UID `10001`) with pinned base image tags, `npm ci` and a `/healthz` `HEALTHCHECK`; the default-account credential volume moved from `/root/.gemini` to `/home/agy/.gemini`. Compose publishes on `${AG_BIND:-127.0.0.1}`; set `AG_BIND=0.0.0.0` to expose it.
  - Download verification fails closed: a missing hash, missing hashing tool or unreachable CLI manifest is an error unless `--insecure-skip-verify` is passed; a mismatch is always fatal. The Zed package requires a pinned `ZED_ACP_SHA256`. The official `install.sh` is only used with `--allow-install-script`, downloaded and shown first (no `curl | bash`).
  - The admin password is no longer written into `config/config.yaml`: it is passed via `AG_ADMIN_PASSWORD` (local process env, `/etc/antigravity-gateway/env` 0600 for systemd, gitignored `deploy/gateway.env` for Docker). New `--admin-password-stdin`.
  - `stop` / `restart` only kill the PID from the pid file after verifying it is the gateway binary (no more port-based kills); `start` / `restart` keep `backend_type` / `port` from `config.yaml` unless overridden and wait for `/healthz`. Lifecycle actions follow `--mode` (systemctl / launchctl / docker compose). Local logs are `0600` under a `0700` data dir.
  - `deploy.sh --mode service` 创建专用系统用户 `antigravity`，二进制以 root 所有安装到 `/usr/local/bin`（服务始终运行 `/usr/local/bin/agy`），并提供加固的 systemd 单元；Docker 镜像改为非 root 用户 `agy`（UID `10001`）运行，固定基础镜像版本、使用 `npm ci` 并内置 `/healthz` 健康检查，默认账户凭据卷由 `/root/.gemini` 迁移至 `/home/agy/.gemini`；Compose 默认仅绑定 `127.0.0.1`（`AG_BIND=0.0.0.0` 显式开放）。下载校验改为失败即中止，Zed 包必须提供固定的 `ZED_ACP_SHA256`，官方安装脚本仅在 `--allow-install-script` 时先展示后执行。管理员密码不再写入 `config/config.yaml`，改由 `AG_ADMIN_PASSWORD` 传递并新增 `--admin-password-stdin`。`stop` / `restart` 仅终止经命令行校验的 PID 文件进程，启停动作按 `--mode` 路由并等待 `/healthz`。

### Changed / 变更
- **Thinking level `low` honoured / 尊重客户端 `low` 思考等级**: client-requested thinking level `low` is now honoured (previously forced to `high`). / 客户端请求的 `low` 思考等级现已生效（此前会被强制提升为 `high`）。
- **Protocol-correct stream termination / 协议规范的流式终止事件**:
  - Streaming errors now end with a protocol-correct terminal error event, and stop reasons (`max_tokens`, `refusal`) are reported. Responses streams end with `response.failed` on error and `response.incomplete` for `max_tokens` / `refusal`; Anthropic `/v1/messages` errors use the Anthropic error envelope.
  - 流式错误现以符合协议的终止错误事件结束，并上报 `max_tokens`、`refusal` 等停止原因：Responses 流出错时以 `response.failed` 结束，`max_tokens` / `refusal` 以 `response.incomplete` 结束；`/v1/messages` 错误改用 Anthropic 标准错误结构。
- **Responses reasoning output (client-visible) / Responses 推理输出格式（客户端可见变更）**:
  - Reasoning is now emitted as a standard `reasoning` output item; the non-standard `reasoning_text` field was removed. Clients reading `reasoning_text` must switch to the `reasoning` item.
  - 推理内容改为标准的 `reasoning` 输出项，移除了非标准字段 `reasoning_text`；依赖 `reasoning_text` 的客户端需改为读取 `reasoning` 输出项。
- **Account network mode without isolation / 未开启隔离时的账户网络模式**: account `network_mode: proxy|bind_ip` now applies even when `isolation_enabled` is false; `netns` accounts use the namespace for login and the egress probe. / 账户 `network_mode: proxy|bind_ip` 在 `isolation_enabled` 为 false 时同样生效；`netns` 账户的登录与出口探测也在对应命名空间内执行。
- **Build & CI / 构建与 CI**:
  - The admin console is embedded with `go build -tags webui` after `npm run build`; a plain `go build ./...` works on a fresh clone and serves a placeholder page. The working-directory `web/dist` override now requires `AG_WEB_DIR`.
  - CI uses `go-version-file: go.mod`, SHA-pinned actions, `go test -race`, `go mod tidy -diff`, `shellcheck`, Linux/Windows cross-builds, plus non-blocking `govulncheck` and `npm audit`. Dependabot now tracks Docker base images.
  - 控制台需在 `npm run build` 后通过 `go build -tags webui` 内嵌；全新克隆直接 `go build ./...` 也可通过（提供占位页）。从工作目录读取 `web/dist` 现需显式设置 `AG_WEB_DIR`。CI 改用 `go.mod` 中的 Go 版本、按 SHA 固定 Actions，并新增竞态测试、`go mod tidy -diff`、`shellcheck`、跨平台编译以及非阻断的 `govulncheck` 与 `npm audit`；Dependabot 新增 Docker 镜像跟踪。

### Upgrade Notes / 升级说明
- Create an API key before using remote clients; update any client that relied on `?token=` or `reasoning_text`.
- Docker: fix ownership of existing volumes once (`docker compose -f deploy/docker-compose.yml run --rm --user root --entrypoint chown antigravity-gateway -R 10001:10001 /var/lib/antigravity-gateway /home/agy/.gemini`). Set admin passwords in `deploy/gateway.env` instead of passing `AG_ADMIN_PASSWORD` through the shell.
- If an older `deploy.sh` wrote `admin_password` into `config/config.yaml`, reset it to `"auto"`.
- 远程客户端使用前请先创建 API Key；依赖 `?token=` 或 `reasoning_text` 的客户端需要更新。Docker 用户需一次性修正已有数据卷属主（命令见上），并改用 `deploy/gateway.env` 设置管理员密码。若旧版 `deploy.sh` 曾把 `admin_password` 写入 `config/config.yaml`，请改回 `"auto"`。

---

## [1.6.0] - 2026-11-10

### Added / 新增特性
- **Client Context Compaction Priority with ACP Fallback / 客户端上下文压缩优先与 ACP 降级联动**:
  - Prioritize client-side context compaction state across multi-turn conversations while preserving sticky session routing (`X-ACP-Session-ID`, `prompt_cache_key`, `previous_response_id`) and seamlessly falling back to ACP session reconstruction when needed.
  - 优先识别并透传客户端主动发起的上下文压缩（Context Compaction）状态，保持会话 ID 强粘性路由，并在必要时平滑联动底层 ACP 会话重建。
- **Dual ACP Backend Auto-Download & Verification (`cli` Default) / 双 ACP 后端自动下载与哈希校验（默认 `cli`）**:
  - Default `acp_pool.backend_type` to `cli` (`agy acp`), with automatic OS/Arch (`darwin/linux/windows`, `amd64/arm64`) detection and SHA-512 verified installation in `deploy.sh`, `deploy.ps1`, and `Dockerfile`.
  - Retained full support for standalone Zed/Zen ACP packages (`agy_acp_server.par`). (Note: in this release `deploy.sh` and the `Dockerfile` did not hash-verify the Zed download, and verification passed when an expected hash was missing; fixed in [Unreleased].)
  - 默认切换为官方 `cli` 后端模式（`agy acp`），支持全平台自动下载与 SHA-512 校验安装；同时完整保留 Zed/Zen 独立 ACP 后端包（`agy_acp_server.par`）支持。（注：该版本的 `deploy.sh` 与 `Dockerfile` 未对 Zed 下载做哈希校验，且缺少预期哈希时校验会直接放行，已在 [Unreleased] 中修复。）
- **Adaptive 2D Context Window Treemap & Module Isolation / 自适应 2D 上下文树图与模块详情隔离**:
  - Upgraded the Overview dashboard's Context Window occupancy view to a proportional 2D Treemap + Waffle Grid with isolated per-module inspection modals (`system_prompt`, `tools_schema`, `conversation_history`, `tool_io`, `thinking`, `output_reserved`, `free_space`).
  - 运行总览面板新增自适应二维比例矩形树图（Treemap）与点阵图，支持按模块独立弹窗检视真实上下文构成与 Token 明细。
- **Zero-Python Pure Shell/Native Deployment & Docker ARM64 Support / 移除部署脚本 Python 依赖并原生支持 Docker ARM64**:
  - Removed `python3` runtime dependency from deployment scripts in favor of native shell/awk/sed utilities and added multi-arch (`linux/amd64` and `linux/arm64`) Docker builds.
  - 移除部署脚本对 `python3` 的强依赖，全面支持 `linux/amd64` 与 `linux/arm64` 容器化构建。
- **Bilingual Open-Source Governance Suite / 中英双语开源协议与 GitHub 维护文档体系**:
  - Added bilingual `LICENSE` (GNU GPL v3.0), `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `SUPPORT.md`, `CHANGELOG.md`, GitHub Issue/PR templates, Dependabot configuration, and CI workflow.
  - 完善中英双语 `LICENSE`（GNU GPL v3.0）及全套 GitHub 社区维护文档、Issue/PR 模板与 CI 自动构建校验工作流。

### Changed & Optimized / 优化与改进
- **Hot-Path Telemetry & Pure Output TPS Calculation / 热路径遥测优化与纯输出 TPS 速率计算**:
  - Decoupled gateway preparation, network round-trip, provider TTFB, and pure token streaming duration so output TPS reflects actual generation speed after first token arrival.
  - 精细拆解网关准备、网络传输、供应商首字耗时（TTFB）与流式生成阶段，基于首字到达后的纯流式输出窗口精确计算生成速率（TPS）。
- **Preemption-Safe Worker Affinity / 防抢占进程亲和性调度**:
  - Prioritized unbound idle workers (`BoundSessionCount() == 0`) for new sessions to prevent evicting warm in-memory ACP sessions, sustaining 94%–100% prompt cache hit rates.
  - 新会话优先调度未绑定活跃会话的空闲预热 Worker，保护内存驻留会话不被抢占，多轮对话缓存命中率稳定在 94%–100%。

### Fixed / 问题修复
- Fixed cross-OS binary header conflicts when `./bin` on a macOS/Windows host is mounted into a Linux Docker container by validating ELF/Mach-O/PE magic bytes before execution.
- 修复宿主机 `./bin` 挂载至 Linux 容器时的跨平台二进制冲突问题，执行前自动校验 ELF/Mach-O/PE 文件头魔数。
- Fixed consecutive streaming tool call continuations and parked worker resumption across `/v1/responses`, `/v1/chat/completions`, and `/v1/messages`.
- 修复 `/v1/responses`、`/v1/chat/completions` 与 `/v1/messages` 三大协议在连续流式工具调用挂起（Parked）与唤醒续接时的状态同步问题。
