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

1. **Change Default Admin Credentials Immediately**
   - Always override `server.admin_password` and `server.admin_token` in `config/config.yaml` (or via `AG_ADMIN_PASSWORD` / `AG_ADMIN_TOKEN` environment variables or `--admin-password` in `deploy.sh`) before exposing the gateway on any network.
2. **Protect Account & Database Storage (`data/`)**
   - The `data/` directory contains `gateway.db` (SQLite audit logs & issued API keys) and per-account `accounts/<id>/.gemini` OAuth session credentials. Ensure directory permissions are restricted to the service user (`chmod 700 data`).
3. **ACP Binary Integrity Verification**
   - The built-in deployment scripts (`deploy/deploy.sh`, `deploy/deploy.ps1`, `deploy/Dockerfile`) enforce **SHA-512** checksum verification for the official `agy` CLI and **SHA-256** verification for the Zed ACP package. Do not disable checksum verification when downloading binaries over untrusted networks.
4. **Northbound API Access Control**
   - Issue distinct `sk-ag-*` keys per client or team via the Admin Console so keys can be audited and revoked individually without disrupting other clients.

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

1. **立即修改默认管理员密码与令牌**
   - 在公网或内网暴露服务前，必须通过 `config/config.yaml`、环境变量（`AG_ADMIN_PASSWORD` / `AG_ADMIN_TOKEN`）或一键部署脚本参数（`--admin-password`）替换默认的 `ag-admin-secret-key`。
2. **严格保护数据与凭据持久化目录（`data/`）**
   - `data/` 目录存储了 SQLite 数据库 `gateway.db` 以及各隔离账户的 `accounts/<id>/.gemini` OAuth 认证状态文件。请确保该目录仅对网关运行用户可读写（建议设置 `chmod 700 data`），且切勿将其提交至版本控制系统。
3. **保持 ACP 后端二进制哈希校验**
   - `deploy/deploy.sh`、`deploy/deploy.ps1` 与 `deploy/Dockerfile` 在自动下载官方 `agy` CLI 时强制执行 **SHA-512** 校验，下载 Zed ACP 独立包时强制执行 **SHA-256** 校验。请勿在非受信网络下绕过校验逻辑。
4. **按客户端独立签发北向 API Key**
   - 建议在 Web 控制台为不同开发者或客户端工具（如 Codex CLI、Claude Code、Cursor）独立签发 `sk-ag-*` 密钥，以便按密钥进行细粒度审计或单独吊销。

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
