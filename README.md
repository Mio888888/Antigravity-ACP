# Antigravity ACP Gateway

[English](./README_EN.md) | **简体中文** · [开源协议 (GPL-3.0)](./LICENSE) · [贡献指南](./CONTRIBUTING.md) · [安全策略](./SECURITY.md) · [排障支持](./SUPPORT.md) · [变更日志](./CHANGELOG.md)

**Antigravity ACP Gateway** 是一个生产级的多账户隔离池、实时上下文与缓存监控、以及多协议统一 AI 网关。它将底层的 **Antigravity ACP 协议**（默认驱动官方 **Antigravity CLI `agy acp`**，同时兼容 **Zed/Zen ACP 独立后端包 `agy_acp_server.par`**）转化为标准的北向三大协议接口，并提供基于 ReUI 设计系统的**中英双语**可视化管理控制台。

---

## 核心特性

1. **三大会话协议原生兼容（单端口复用）**
   - **OpenAI 官方 Responses 协议**：`POST /v1/responses`（原生兼容 Codex CLI `wire_api="responses"`、`previous_response_id` 状态续接、推理强度控制与工具续接）。
   - **OpenAI 兼容 Chat Completions 协议**：`POST /v1/chat/completions`（兼容 Cursor、Cline、Roo Code、Cherry Studio 等客户端）。
   - **Claude 原生 Messages 协议**：`POST /v1/messages`（兼容 Claude Code CLI `ANTHROPIC_BASE_URL`、Thinking 思考块与多轮工具调用）。
   - **动态模型发现与能力契约**：`GET /v1/models`、`GET /v1beta/models`（毫秒级内存缓存、支持 `?refresh=1` 实时穿透探测，透出真实上下文窗口容量：Gemini 1M、Claude 200K、GPT-5/Codex 272K、GPT-OSS 128K）。
   - **ACP 原生 WebSocket 桥接**：`WS /v1/acp/ws`。

2. **双 ACP 后端引擎可选，一键自动下载安装（默认 `cli`）**
   - **`cli` 模式（默认）**：驱动官方 Antigravity CLI（`agy acp` / `antigravity acp`），支持 `--process-per-session` 等完整 CLI 参数；一键部署时若本机未安装，将自动按当前系统与架构（`darwin/linux/windows`，`amd64/arm64`）从官方源下载并做 **SHA-512 校验**安装。
   - **`zed` / `zen` 模式**：直接驱动 Zed 的 Antigravity ACP 独立后端包（`agy_acp_server.par` / `agy_acp_server`），缺失时同样支持从 ACP Registry 自动下载并做 **SHA-256 校验**安装。
   - **`auto` 自动探测模式**：跨 macOS、Windows、Linux 与 Docker 自动探测本机已安装的 `agy` CLI 或 Zed ACP 扩展包，并内置二进制文件头（ELF / Mach-O / PE）跨系统防冲突校验。

3. **四层粘性会话（Sticky Session）与进程亲和性（Worker Affinity）**
   - **工具挂起强粘性（Parked Tool Continuation）**：模型发起客户端工具调用时，底层 Worker 进程进入 `Parked` 独占挂起态；客户端回传 `tool_result` 时 100% 唤醒原进程与原会话，并自动折叠流式连续工具调用输出。
   - **显式会话 ID 与客户端会话提取**：自动从请求头（`X-ACP-Session-ID`、`X-Conversation-ID`）、请求体（`prompt_cache_key`、`previous_response_id`、`metadata.session_id`）及系统提示词中提取真实客户端会话 ID 与会话标题。
   - **历史消息前缀哈希粘性**：即使无状态客户端不传会话 ID，网关也会通过 SHA-256 历史指纹自动路由至同一 `ManagedSession`。
   - **防抢占空闲进程分配**：新会话优先分配未绑定会话的预热空闲 Worker，保护已驻留活跃会话的进程，实测多轮对话 Prompt 缓存命中率稳定在 **94%–100%**。

4. **多账户容器隔离池与出口路由**
   - 每个账户拥有独立的 `GEMINI_CLI_HOME` 容器目录（`data/accounts/<id>/.gemini`）与独立 Worker 子进程池。
   - 支持 **Direct 直连**、**SOCKS5 / HTTP 代理**、**源网卡 IP 绑定（Bind IP）** 与 **Linux Network Namespace（NetNS）** 四种网络隔离模式，支持无头浏览器 OAuth 登录引导与周期模型配额监控。

5. **中英双语 ReUI 实时可观测控制台**
   - 顶栏一键切换 **`中文 / EN`**，全界面实时翻译同步。
   - **运行总览（Overview）**：单请求端到端路由追踪、四段式耗时拆解（网关 / 网络 / 供应商 TTFB / 流式输出）、纯输出 TPS 速率计算、1M/200K 上下文窗口 2D 矩形树图（Treemap）与点阵图、底部实时请求选择器（按请求 / 按会话聚合）以及**底层进程与绑定会话**实时监控面板。

---

## 完整部署教程

### 一、 平台支持矩阵与环境要求

网关采用纯 Go SQLite 引擎（`CGO_ENABLED=0`），并将前端构建产物内嵌至单一可执行文件中，无需额外部署外部数据库或 Web 服务器。

| 运行平台 | 支持架构 | 默认 ACP 后端自动下载源 | 支持部署方式 |
| :--- | :--- | :--- | :--- |
| **Linux 服务器** | `x86_64 (amd64)`、`ARM64 (aarch64)`（支持 glibc 与 musl） | 官方 `linux_amd64` / `linux_arm64` 清单（SHA-512 校验） | 本地后台、`systemd` 系统服务、Docker Compose、预编译二进制 |
| **macOS** | `Apple Silicon (arm64)`、`Intel (x86_64)` | 官方 `darwin_arm64` / `darwin_amd64` 清单（SHA-512 校验） | 本地后台、`launchd` 守护服务、Docker Desktop |
| **Windows** | `x64 (amd64)`、`ARM64` | 官方 `windows_amd64` / `windows_arm64` 清单（SHA-512 校验） | 本地后台、Windows 计划任务服务、Docker Desktop |
| **Docker 容器** | `linux/amd64`、`linux/arm64` | 镜像构建期按容器架构自动拉取并校验 | `deploy.sh --mode docker` 或 `docker compose` |

- **源码一键编译部署环境要求**：`Go >= 1.23`、`Node.js >= 20`（含 `npm`）、`python3`、`curl`。
- **Docker 部署环境要求**：仅需安装 `Docker` 与 `Docker Compose`（宿主机无需安装 Go / Node.js）。
- **预编译包部署环境要求**：目标服务器仅需 `curl` 与 `python3`（用于自动拉取 ACP 后端，或直接拷贝二进制免依赖运行）。

---

### 二、 macOS 与 Linux（x86_64 / ARM64）一键部署（`deploy/deploy.sh`）

赋予部署脚本执行权限：

```bash
chmod +x deploy/deploy.sh
```

#### 1. 本地一键编译并后台启动（最简方式）
默认使用 `cli` 模式（`agy acp`）。若本机未检测到 `agy`，脚本会自动识别系统与 CPU 架构（如 `linux_arm64`、`linux_amd64`、`darwin_arm64`），从官方发布源下载并完成 SHA-512 校验后安装至 `./bin/agy`：

```bash
./deploy/deploy.sh --mode local --port 8080 --admin-password "your-strong-password"
```

#### 2. 注册为 Linux `systemd` / macOS `launchd` 开机自启服务（生产推荐）
在 Linux 服务器（含 x86_64 与 ARM64）上，以下命令会自动完成前端构建、Go 网关编译、ACP 后端自动下载，并将二进制与配置安装至 `/usr/local/bin` 与 `/etc/antigravity-gateway/config.yaml`，最后注册并启动 `antigravity-gateway.service`：

```bash
./deploy/deploy.sh --mode service --port 8080 --admin-password "your-strong-password"
```

Linux `systemd` 服务常用运维命令：
```bash
sudo systemctl status antigravity-gateway.service     # 查看服务运行状态
sudo journalctl -u antigravity-gateway.service -f     # 实时查看网关日志
sudo systemctl restart antigravity-gateway.service    # 重启服务
sudo systemctl stop antigravity-gateway.service       # 停止服务
```

#### 3. 切换或单独下载/更新 ACP 后端引擎
```bash
# 仅下载或更新默认 CLI 后端 (./bin/agy)
./deploy/deploy.sh --action install-acp --backend cli

# 切换使用 Zed/Zen 的 ACP 独立后端包 (缺失时自动从 ACP Registry 下载 agy_acp_server.par 并做 SHA-256 校验)
./deploy/deploy.sh --mode local --backend zed

# 离线环境部署（手动指定已有二进制路径并跳过自动下载）
./deploy/deploy.sh --mode local --backend cli --acp-bin "/path/to/agy" --no-download
```

#### 4. 本地模式进程管理命令
```bash
./deploy/deploy.sh --action status    # 查看运行状态与 PID
./deploy/deploy.sh --action restart   # 平滑重启网关（缺失后端时自动补齐）
./deploy/deploy.sh --action stop      # 停止本地后台进程
```

#### 5. `deploy/deploy.sh` 参数速查表

| 参数 | 可选值 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--action` | `install` \| `install-acp` \| `start` \| `stop` \| `restart` \| `status` \| `build` \| `build-all` | `install` | 执行动作（`install-acp` 仅下载 ACP 后端，`build-all` 交叉编译全平台包） |
| `--mode` | `local` \| `service` \| `docker` | `local` | 部署方式：本地后台进程、系统开机自启服务、Docker 容器 |
| `--backend` | `cli` \| `zed` \| `zen` \| `auto` | `cli` | ACP 后端引擎类型（默认 `cli`） |
| `--acp-bin` | 文件路径 | 空（自动发现/下载） | 显式指定 `agy` 或 `agy_acp_server.par` 路径 |
| `--no-download` | 无参数标志 | 关闭（默认自动下载） | 禁用缺失 ACP 后端时的自动下载 |
| `--port` | 端口号 | `8080` | 网关 HTTP 监听端口 |
| `--admin-password` | 字符串 | 保持配置文件值 | 设置 Web 控制台登录密码 |
| `--lang` | `zh` \| `en` | `zh` | 脚本终端输出语言 |

---

### 三、 Docker 与 Docker Compose 部署（原生支持 `linux/amd64` 与 `linux/arm64`）

`deploy/Dockerfile` 采用多阶段构建，并在运行时镜像阶段自动检测容器架构（`x86_64` 或 `aarch64`），从官方源下载并校验安装对应的 Linux ACP 后端（`/usr/local/bin/agy` 或 `agy_acp_server.par`），无需手动准备二进制文件。

#### 1. 使用部署脚本一键启动容器
```bash
# 默认使用 cli 后端构建并启动容器
./deploy/deploy.sh --mode docker --port 8080 --admin-password "your-strong-password"

# 如需在容器中使用 Zed ACP 独立后端包
./deploy/deploy.sh --mode docker --backend zed --port 8080 --admin-password "your-strong-password"
```

#### 2. 直接使用 `docker compose` 启动与管理
```bash
# 构建并后台启动（默认 AG_ACP_BACKEND=cli，支持 x86_64 与 ARM64 Docker 宿主机）
AG_ACP_BACKEND=cli AG_PORT=8080 AG_ADMIN_PASSWORD="your-strong-password" \
  docker compose -f deploy/docker-compose.yml up -d --build

# 查看容器实时日志
docker compose -f deploy/docker-compose.yml logs -f

# 停止并移除容器（数据卷保留）
docker compose -f deploy/docker-compose.yml down
```

#### 3. 容器持久化目录说明
- `gateway-data`（挂载于容器内 `/var/lib/antigravity-gateway`）：持久化存储 SQLite 审计与配置数据库 `gateway.db`，以及多账户隔离凭据目录 `accounts/<id>/.gemini`。
- `gemini-home`（挂载于容器内 `/root/.gemini`）：默认根账户凭据缓存目录。
- `../bin`（只读挂载于容器内 `/opt/antigravity-acp`）：可选自定义 ACP 二进制目录。网关内置跨系统二进制文件头识别，即使宿主机 `./bin` 存在 macOS/Windows 编译产物，容器内也会自动跳过并使用容器内置的 Linux `/usr/local/bin/agy`。

---

### 四、 免 Go/Node 环境部署（交叉编译后上传至 ARM64 / x86_64 服务器）

如果目标生产服务器（如轻量云主机或内网 ARM64 / x86_64 Linux 服务器）不想安装 Go 与 Node.js 编译环境，可在本地开发机一键交叉编译全平台独立发行包：

```bash
# 1. 在开发机执行全平台交叉编译
./deploy/deploy.sh --action build-all
```

编译完成后，`./dist/` 目录下会生成已内嵌前端资源的独立发行包：
- `dist/antigravity-gateway-linux-arm64/`（适用于 ARM64 / aarch64 Linux 服务器，如 AWS Graviton、Oracle ARM、树莓派、鲲鹏等）
- `dist/antigravity-gateway-linux-amd64/`（适用于常规 x86_64 Linux 服务器）
- `dist/antigravity-gateway-darwin-arm64/`、`dist/antigravity-gateway-darwin-amd64/`
- `dist/antigravity-gateway-windows-amd64/`、`dist/antigravity-gateway-windows-arm64/`

```bash
# 2. 将对应架构目录上传至目标 Linux 服务器
scp -r ./dist/antigravity-gateway-linux-arm64 root@your-server:/opt/antigravity-gateway

# 3. 登录目标服务器，自动下载 ACP 后端并启动
ssh root@your-server
cd /opt/antigravity-gateway
chmod +x antigravity-gateway antigravity-probe deploy/deploy.sh
./deploy/deploy.sh --action install-acp --backend cli
nohup ./antigravity-gateway -config ./config/config.yaml -backend cli > gateway.log 2>&1 &
```

---

### 五、 Windows 一键部署（PowerShell `deploy/deploy.ps1`）

在 Windows 10/11 或 Windows Server（支持 `x64` 与 `ARM64`）上打开 PowerShell 执行：

```powershell
# 方式 A：本地一键编译并后台启动（默认 cli 模式，缺失时自动下载 agy.exe 并完成 SHA-512 校验）
.\deploy\deploy.ps1 -Mode local -Port 8080 -AdminPassword "your-strong-password"

# 方式 B：指定使用 Zed/Zen ACP 独立包（缺失时自动下载并做 SHA-256 校验）
.\deploy\deploy.ps1 -Mode local -Backend zed

# 方式 C：仅下载/更新 ACP 后端可执行文件到 .\bin
.\deploy\deploy.ps1 -Action install-acp -Backend cli

# 方式 D：注册为 Windows 开机自启计划任务服务
.\deploy\deploy.ps1 -Mode service -Port 8080 -AdminPassword "your-strong-password"

# 常用状态与启停管理
.\deploy\deploy.ps1 -Action status
.\deploy\deploy.ps1 -Action restart
.\deploy\deploy.ps1 -Action stop
```

---

### 六、 部署后首次配置流程（控制台登录 -> 账户授权 -> 签发 API Key）

1. **登录管理控制台**
   - 浏览器打开 `http://<服务器IP>:8080`，输入设置的管理员密码（默认密码为 `config/config.yaml` 中的 `ag-admin-secret-key`）。
2. **添加账户并完成 OAuth 终端登录**
   - 进入 **「账户容器池（Account Pool）」** 标签页，可使用默认账户 `default` 或点击 **「新建隔离账户」**（可为每个账户独立配置 Direct 直连、SOCKS5/HTTP 代理、Bind IP 或 Linux NetNS）。
   - 点击账户卡片上的 **「终端登录（OAuth）」**，网关会在隔离容器目录内拉起无头登录会话并提取 Google OAuth 授权链接。
   - 在本地浏览器打开该授权链接完成登录（若部署在远程无桌面服务器上，将浏览器跳转后的回调 URL 或授权码粘贴回控制台弹窗即可完成认证）。
   - 认证成功后账户状态变为 `ready`，可点击 **「刷新额度」** 实时查看 Gemini 与 Claude/GPT 模型组的 5 小时及每周剩余配额百分比。
3. **签发北向 API Key**
   - 进入 **「北向令牌（API Keys）」** 标签页，创建或复制 `sk-ag-...` 令牌（可绑定指定账户或设为 `auto` 自动负载均衡）。

---

## 配置文件说明（`config/config.yaml`）

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  public_base_url: "http://localhost:8080"
  data_dir: "./data"
  admin_token: "ag-admin-secret-key"
  admin_password: "ag-admin-secret-key"

acp_pool:
  # backend_type 可选值:
  # - "cli" (默认)  : 使用 Antigravity CLI (agy acp / antigravity acp)
  # - "zed" / "zen" : 使用 Zed/Zen ACP 独立后端包 (agy_acp_server.par / agy_acp_server)
  # - "auto"        : 优先使用 binary_path，不存在时自动探测 agy CLI 或 Zed/Zen ACP 包
  backend_type: "cli"
  binary_path: "auto"
  workspace_dir: "/tmp/antigravity-gateway-workspace"
  min_idle_workers: 1
  max_workers: 5
  max_idle_time_sec: 600
  tool_park_timeout_sec: 180

prompt_policy:
  mode: "pure_passthrough"      # pure_passthrough | prepend_global | native_hybrid
  disable_builtin_tools: true

sandbox_policy:
  session_mode: "yolo"          # yolo (全自动粘性复用) | sticky_conversation | stateless_single_turn
  internal_approval: "auto_approve"
```

支持的环境变量覆盖：
- `AG_ACP_BACKEND`：`cli`（默认） | `zed` | `zen` | `auto`
- `AG_ACP_BINARY_PATH`：ACP 后端可执行文件或 `.par` 包绝对路径
- `AG_HOST` / `AG_PORT`：监听地址与端口
- `AG_DATA_DIR` / `AG_WORKSPACE_DIR`：数据持久化目录与会话工作区目录
- `AG_ADMIN_PASSWORD` / `AG_ADMIN_TOKEN`：Web 控制台管理员密码与 API 令牌

---

## 客户端接入速览

启动后浏览器访问 `http://localhost:8080` 登录管理控制台（可在「远程客户端接入」标签页一键复制配置）：

- **OpenAI Codex CLI (`~/.codex/config.toml`)**：
  ```toml
  model = "gemini-4-argon"
  model_provider = "antigravity"

  [model_providers.antigravity]
  name = "Antigravity ACP Gateway"
  base_url = "http://localhost:8080/v1"
  env_key = "ANTIGRAVITY_API_KEY"
  wire_api = "responses"
  ```
- **Claude Code CLI**：
  ```bash
  export ANTHROPIC_BASE_URL="http://localhost:8080"
  export ANTHROPIC_AUTH_TOKEN="sk-ag-your-issued-key"
  export ANTHROPIC_MODEL="gemini-4-argon"
  claude
  ```
- **Cursor / Cline / Roo Code / Cherry Studio**：
  - Base URL: `http://localhost:8080/v1`
  - API Key: `sk-ag-your-issued-key`
  - 自定义请求头（可选）：`X-ACP-Context-Window: 1048576`、`X-ACP-Account-ID: auto`

---

## 社区维护与治理文档

本项目配套完整的中英双语开源治理与 GitHub 维护文档：

- **[开源协议 (`LICENSE`)](./LICENSE)**：GNU General Public License v3.0（GPL-3.0）官方英文全文与简体中文参考译文。
- **[贡献指南 (`CONTRIBUTING.md`)](./CONTRIBUTING.md)**：架构目录说明、前后端构建测试命令、开发规范与 Pull Request 流程。
- **[行为准则 (`CODE_OF_CONDUCT.md`)](./CODE_OF_CONDUCT.md)**：Contributor Covenant v2.1 社区行为准则（中英双语）。
- **[安全策略 (`SECURITY.md`)](./SECURITY.md)**：生产环境安全加固建议与安全漏洞私密报告流程。
- **[支持与排障指南 (`SUPPORT.md`)](./SUPPORT.md)**：常见部署、跨平台二进制校验、远程无头 OAuth 登录与粘性会话排障清单。
- **[版本变更日志 (`CHANGELOG.md`)](./CHANGELOG.md)**：各版本核心特性、性能优化与根因修复记录。

---

## 开源协议 (License)

本项目基于 **[GNU General Public License v3.0 (GPL-3.0)](./LICENSE)** 开源协议发布。您可以自由运行、研究、共享和修改本软件，但任何分发的副本或衍生修改作品必须同样遵循 GPL-3.0 协议开放完整对应源代码。

