# Security Policy / 安全策略

**English** | [简体中文](#简体中文)

---

## English

### 1. Supported Versions

We actively maintain and provide security updates for the latest release branch of **Antigravity ACP Gateway**:

| Version | Supported | Notes |
| :--- | :--- | :--- |
| `v1.6.x` (`main`) | Yes | Active development & security patches |
| `< v1.6.0` | No | Please upgrade to the latest `main` release |

### 2. Security Architecture & Best Practices

Because **Antigravity ACP Gateway** manages multi-account OAuth credentials, northbound API keys (`sk-ag-*`), and local ACP subprocesses, operators should follow these deployment hardening practices:

1. **Lock In Your Admin Credentials**
   - By default (`admin_password: "auto"`, `admin_token: "auto"`) a random password and token are generated on every start and written to `<data_dir>/admin_credentials.txt` (mode `0600`); they are **not** printed to logs. Sign in with them and use **Change Password** in the console: the credentials are then fixed and stored as a salted PBKDF2-SHA256 hash in `gateway.db` (legacy unsalted SHA-256 hashes are upgraded on the next successful login). Minimum length: 8 characters for the password, 12 for a custom admin token.
   - An initial password can be supplied via `AG_ADMIN_PASSWORD` (`deploy.sh --admin-password-stdin` passes it this way; it is never written to the git-tracked `config/config.yaml`). It only applies until the password is changed in the console.
   - `Authorization: Bearer` on admin APIs accepts the admin token or a console session token, never the password. Failed credential attempts are rate-limited per IP on all endpoints.
2. **Protect Account & Database Storage (`data/`)**
   - The data directory contains `gateway.db` (SQLite audit logs & issued API keys), `admin_credentials.txt` and per-account `accounts/<id>/.gemini` OAuth session credentials. The gateway creates it with mode `0700`; `deploy.sh` also starts local processes with `umask 077` and keeps `gateway.log` at `0600`. Never commit it.
3. **Run Unprivileged**
   - `deploy.sh --mode service` runs the gateway as the dedicated system user `antigravity` under a hardened systemd unit (`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome=tmpfs`, `PrivateTmp`, empty capability set, `UMask=0077`). The Docker image runs as UID `10001` and Compose publishes the port on `127.0.0.1` unless you set `AG_BIND=0.0.0.0`.
4. **ACP Binary Integrity Verification (fail closed)**
   - `deploy/deploy.sh`, `deploy/deploy.ps1` and `deploy/Dockerfile` verify the official `agy` CLI against the **SHA-512** in the release manifest and the Zed ACP package against a **pinned SHA-256** you supply (`ZED_ACP_SHA256` / `--zed-sha256` / build arg). A missing hash, missing hashing tool or unreachable manifest is an error unless you pass `--insecure-skip-verify` (`AG_INSECURE_SKIP_VERIFY=1` for Docker); a mismatch is always fatal. The official `install.sh` is only used with `--allow-install-script`, and is downloaded and shown before it runs (never `curl | bash`).
5. **Northbound API Access Control**
   - While no API key exists, `/v1/*` is only reachable without a key from loopback (`127.0.0.1` / `::1`). Issue distinct `sk-ag-*` keys per client or team so they can be audited and revoked individually. Keys bound to an account cannot be redirected with `X-ACP-Account-ID` / `?account_id=`, and sessions are isolated per key. `?key=` is accepted for the northbound API only; `?token=` is not accepted.
6. **Built-in Isolation Defaults**
   - State-changing admin endpoints require POST/PUT/DELETE, cross-origin state-changing requests are rejected, the session cookie is `SameSite=Strict`, and WebSocket upgrades check `Origin`.
   - Admin API responses omit account `api_key` values and mask proxy passwords as `********`.
   - `/v1/acp/ws` sessions always run inside the gateway-managed workspace; client `cwd` values outside it are rewritten. The internal MCP bridge `/mcp/sessions/*` only accepts loopback connections.
   - ACP agent subprocesses do not inherit `AG_*` environment variables. Account IDs are restricted to `[A-Za-z0-9._-]` (1–64 characters).

### 3. Reporting a Vulnerability

**Please do NOT report security vulnerabilities through public GitHub Issues, Discussions, or Pull Requests.**

If you discover a security vulnerability (such as authentication bypass, cross-account credential leakage, command injection in subprocess spawning, or arbitrary file read/write), please report it privately:

1. **GitHub Private Vulnerability Reporting (Preferred)**:
   - Navigate to the repository's **Security** tab -> **Advisories** -> **Report a vulnerability** to open a private security advisory directly with the maintainers.
2. **What to Include in Your Report**:
   - Affected component (`internal/server`, `internal/acp`, `internal/storage`, `deploy`, or `web`).
   - Exact version or Git commit hash (`git rev-parse HEAD`).
   - Step-by-step Proof of Concept (PoC) or reproduction instructions (with all personal credentials redacted).
   - Assessment of potential impact and any suggested remediation.

### 4. Response Timeline & Disclosure Policy

- **Acknowledgment**: Within **48 hours** of receiving a private report.
- **Triage & Remediation Plan**: Within **5 business days**.
- **Coordinated Disclosure**: Once a fix is released and verified, we will publish a security advisory and credit the reporter (unless anonymity is requested).

---

<a id="简体中文"></a>

## 简体中文

### 1. 受支持的版本

我们当前针对 **Antigravity ACP Gateway** 的最新主干版本提供持续维护与安全更新：

| 版本范围 | 是否支持安全更新 | 说明 |
| :--- | :--- | :--- |
| `v1.6.x` (`main`) | 支持 | 当前活跃维护分支，优先提供安全补丁 |
| `< v1.6.0` | 不再支持 | 请升级至最新 `main` 发行版本 |

### 2. 安全架构与生产加固建议

由于 **Antigravity ACP Gateway** 负责管理多账户 OAuth 授权凭据、北向调用令牌（`sk-ag-*`）以及底层 ACP 子进程池，在生产环境部署时请务必遵循以下安全规范：

1. **尽快固定管理员凭证**
   - 默认配置（`admin_password: "auto"`、`admin_token: "auto"`）会在每次启动时随机生成密码与 Token，并写入 `<data_dir>/admin_credentials.txt`（权限 `0600`），**不会打印到日志**。请用其登录后在控制台「修改密码」：凭证随即固定，并以加盐 PBKDF2-SHA256 哈希保存在 `gateway.db` 中（旧版无盐 SHA-256 哈希会在下次成功登录时自动升级）。密码至少 8 位，自定义管理 Token 至少 12 位。
   - 初始密码可通过 `AG_ADMIN_PASSWORD` 提供（`deploy.sh --admin-password-stdin` 即以此方式传递，绝不会写入受 git 跟踪的 `config/config.yaml`），且仅在控制台修改密码之前生效。
   - 管理 API 的 `Authorization: Bearer` 只接受管理 Token 或控制台会话 Token，不接受密码；所有端点的凭证失败尝试均按 IP 限速。
2. **严格保护数据与凭据持久化目录（`data/`）**
   - 数据目录存储了 SQLite 数据库 `gateway.db`、`admin_credentials.txt` 以及各隔离账户的 `accounts/<id>/.gemini` OAuth 认证状态文件。网关以 `0700` 权限创建该目录；`deploy.sh` 本地模式以 `umask 077` 启动并将 `gateway.log` 设为 `0600`。切勿将其提交至版本控制系统。
3. **以非特权身份运行**
   - `deploy.sh --mode service` 以专用系统用户 `antigravity` 运行，并启用 systemd 加固（`NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome=tmpfs`、`PrivateTmp`、清空能力集、`UMask=0077`）。Docker 镜像以 UID `10001` 运行，Compose 默认只在 `127.0.0.1` 上发布端口，需显式设置 `AG_BIND=0.0.0.0` 才对外开放。
4. **ACP 后端二进制哈希校验（失败即中止）**
   - `deploy/deploy.sh`、`deploy/deploy.ps1` 与 `deploy/Dockerfile` 使用发布清单中的 **SHA-512** 校验官方 `agy` CLI，使用您提供的**固定 SHA-256**（`ZED_ACP_SHA256` / `--zed-sha256` / 构建参数）校验 Zed ACP 包。缺少哈希、缺少哈希工具或清单获取失败均直接报错，除非显式传入 `--insecure-skip-verify`（Docker 为 `AG_INSECURE_SKIP_VERIFY=1`）；哈希不匹配始终报错。官方 `install.sh` 仅在传入 `--allow-install-script` 时使用，且会先下载并展示内容再执行（绝不 `curl | bash`）。
5. **北向 API 访问控制**
   - 尚未创建 API Key 时，`/v1/*` 仅允许回环地址（`127.0.0.1` / `::1`）免密访问。建议为不同开发者或客户端工具独立签发 `sk-ag-*` 密钥，以便单独审计与吊销。绑定账户的 Key 无法通过 `X-ACP-Account-ID` / `?account_id=` 改投其他账户，会话按 Key 隔离。`?key=` 仅对北向 API 有效，`?token=` 已不再接受。
6. **内置隔离默认值**
   - 改变状态的管理接口必须使用 POST/PUT/DELETE，跨站的状态变更请求会被拒绝，会话 Cookie 为 `SameSite=Strict`，WebSocket 升级会校验 `Origin`。
   - 管理 API 响应不包含账户 `api_key`，代理密码显示为 `********`。
   - `/v1/acp/ws` 会话始终运行在网关管理的工作区内，工作区外的客户端 `cwd` 会被改写；内部 MCP 桥 `/mcp/sessions/*` 仅接受回环连接。
   - ACP 子进程不继承 `AG_*` 环境变量；账户 ID 仅允许 `[A-Za-z0-9._-]`（1–64 个字符）。

### 3. 安全漏洞私密报告流程

**请勿通过公开的 GitHub Issues、Discussions 或 Pull Requests 披露尚未修复的安全漏洞。**

如果您发现了潜在的安全隐患（例如鉴权绕过、多账户容器凭据越权访问、子进程参数注入、任意文件读写或敏感信息泄露等），请通过以下私密渠道联系维护团队：

1. **GitHub 私密漏洞报告（首选推荐）**：
   - 进入本仓库顶部的 **Security（安全）** 标签页 -> **Advisories（安全公告）** -> 点击 **Report a vulnerability（报告漏洞）**，向维护者提交私密安全报告。
2. **报告建议包含的信息**：
   - 受影响的模块路径（如 `internal/server`、`internal/acp`、`internal/storage`、`deploy` 或 `web`）。
   - 对应的版本号或 Git Commit 哈希值（`git rev-parse HEAD`）。
   - 详细的复现步骤或最小概念验证（PoC），请确保已对所有个人凭据脱敏。
   - 潜在影响范围评估及修复建议（如有）。

### 4. 响应时效与协同披露承诺

- **确认收到**：在收到私密安全报告后的 **48 小时内**予以确认回复。
- **漏洞评估与修复排期**：在 **5 个工作日内**完成根因确认并同步修复计划。
- **协同披露（Coordinated Disclosure）**：在补丁发布并允许用户合理升级时间后，我们将发布正式安全公告，并向报告者致谢（支持按报告者意愿保持匿名）。
