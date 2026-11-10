# Support & Troubleshooting Guide / 支持与排障指南

**English** | [简体中文](#简体中文)

---

## English

### 1. Where to Get Help

- **Deployment & Configuration Docs**: Start with [`README_EN.md`](./README_EN.md) (English) or [`README.md`](./README.md) (Simplified Chinese).
- **Bug Reports**: If you encounter a reproducible defect, open a [Bug Report Issue](../../issues/new/choose).
- **Feature Requests**: Have an idea for protocol adapters, observability, or account routing? Submit a [Feature Request](../../issues/new/choose).
- **Security Issues**: Do **not** open a public issue. Follow [`SECURITY.md`](./SECURITY.md) for private disclosure.

### 2. Common Troubleshooting Checklist

#### A. ACP Backend Binary Not Found or Incompatible (`ELF` / `Mach-O` / `PE`)
- **Symptom**: Gateway logs report `ACP binary not found` or `incompatible binary header`.
- **Solution**:
  - Run `./deploy/deploy.sh --action install-acp --backend cli` (or `.\deploy\deploy.ps1 -Action install-acp -Backend cli` on Windows) to download and verify the native `agy` binary for your OS/Architecture into `./bin/`.
  - In Docker containers, ensure you are not forcing a macOS/Windows host binary path via `AG_ACP_BINARY_PATH`. Leave `binary_path: "auto"` so the container automatically selects `/usr/local/bin/agy`.

#### B. Headless OAuth Sign-In Fails on Remote Linux Server
- **Symptom**: After clicking **Terminal Login (OAuth)** in the Account Pool tab, the browser redirects to `http://localhost:...` on your local laptop and fails to connect.
- **Solution**:
  - Copy the **entire redirected `http://localhost:.../?code=...` URL** from your local browser's address bar and paste it into the **OAuth Callback URL / Code** input box in the Web Admin Console modal. The gateway will forward it to the isolated headless login session inside the server container.

#### C. Prompt Cache Hit Rate Drops or Session Loses Context
- **Symptom**: Multi-turn conversations spawn new ACP sessions instead of reusing the bound worker.
- **Solution**:
  - Verify `sandbox_policy.session_mode` is set to `"yolo"` or `"sticky_conversation"` in `config/config.yaml`.
  - Ensure `acp_pool.max_workers` is large enough for your concurrent active conversations so active workers are not evicted under load.
  - Check whether the client is sending `X-ACP-Session-ID`, `prompt_cache_key`, `previous_response_id`, or consistent conversation history prefixes.

### 3. Information to Include When Asking for Help

When opening an issue, please include (with secrets redacted):
1. **OS & Architecture** (e.g., `Ubuntu 24.04 aarch64`, `macOS 15 arm64`, `Docker linux/amd64`).
2. **ACP Backend Mode** (`cli` / `zed` / `auto`) and output of `./antigravity-probe` if applicable.
3. **Client & Northbound Endpoint** (e.g., Codex CLI on `POST /v1/responses`, Claude Code on `POST /v1/messages`, Cursor on `POST /v1/chat/completions`).
4. **Sanitized Gateway Logs** around the failed request.

---

<a id="简体中文"></a>

## 简体中文

### 1. 获取帮助的渠道

- **部署与配置教程**：请首先查阅 [`README.md`](./README.md)（简体中文）或 [`README_EN.md`](./README_EN.md)（英文）。
- **缺陷反馈（Bug Report）**：若遇到可复现的程序错误，请通过 [Issue 模板](../../issues/new/choose) 提交 Bug 报告。
- **功能建议（Feature Request）**：若希望新增协议适配特性、监控指标或账户调度策略，请提交 [Feature Request](../../issues/new/choose)。
- **安全漏洞**：请**切勿**公开提交 Issue，请严格参照 [`SECURITY.md`](./SECURITY.md) 通过私密安全通道报告。

### 2. 常见问题快速自查清单

#### A. 找不到 ACP 后端二进制或提示跨平台文件头冲突（`ELF` / `Mach-O` / `PE`）
- **现象**：网关启动日志提示未找到 ACP 可执行文件，或跳过不兼容宿主机的二进制。
- **排查与解决**：
  - 执行 `./deploy/deploy.sh --action install-acp --backend cli`（Windows 下执行 `.\deploy\deploy.ps1 -Action install-acp -Backend cli`），脚本会自动按当前系统与架构下载并校验官方 `agy` 至 `./bin/`。
  - 若使用 Docker 部署，请保持 `binary_path: "auto"`，切勿通过环境变量将宿主机 macOS/Windows 的二进制路径硬编码注入容器。

#### B. 远程无桌面 Linux 服务器上进行 OAuth 终端登录失败
- **现象**：在控制台点击 **「终端登录（OAuth）」** 并用本地电脑浏览器打开授权链接后，浏览器跳转至 `http://localhost:...` 显示无法访问此网站。
- **排查与解决**：
  - 这是远程无头服务器的正常现象。只需将本地浏览器地址栏中跳转后的**完整 `http://localhost:.../?code=...` 回调 URL** 复制下来，粘贴回网关 Web 控制台弹窗的回调输入框并提交，网关即可在服务端隔离容器内完成认证。

#### C. 多轮对话未命中粘性会话（Sticky Session）或缓存命中率下降
- **现象**：同一客户端连续对话时新建了会话，未复用原 Worker 进程。
- **排查与解决**：
  - 检查 `config/config.yaml` 中 `sandbox_policy.session_mode` 是否配置为 `"yolo"` 或 `"sticky_conversation"`。
  - 检查 `acp_pool.max_workers` 是否大于当前并发活跃会话数，避免所有 Worker 均处于满载时触发会话淘汰。
  - 在控制台 **「运行总览（Overview）」** 底部的进程与绑定会话面板中观察客户端会话 ID（`X-ACP-Session-ID`、`prompt_cache_key`、`previous_response_id` 或历史指纹）是否一致。

### 3. 提交求助 Issue 时建议附带的诊断信息

为便于快速定位根因，提交问题时请提供以下脱敏信息（切勿包含真实密码、OAuth 凭据或 `sk-ag-*` 密钥）：
1. **操作系统与 CPU 架构**（例如 `Ubuntu 24.04 aarch64`、`macOS arm64`、`Docker linux/amd64`）。
2. **ACP 后端引擎模式**（`cli` / `zed` / `auto`）以及部署方式（`local` / `service` / `docker`）。
3. **接入客户端与调用的北向协议接口**（例如 Codex CLI 调用 `POST /v1/responses`、Claude Code 调用 `POST /v1/messages`、Cursor 调用 `POST /v1/chat/completions`）。
4. **脱敏后的网关关键日志片段**。
