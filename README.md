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
   - **`cli` 模式（默认）**：驱动官方 Antigravity CLI（`agy acp` / `antigravity acp`），支持 `--process-per-session` 等完整 CLI 参数；一键部署时若本机未安装，将自动按当前系统与架构（`darwin/linux/windows`，`amd64/arm64`）从官方清单下载并做 **SHA-512 校验**安装。校验**默认失败即中止**：清单缺少哈希、本机没有哈希工具或清单获取失败都会报错，哈希不匹配始终报错。
   - **`zed` / `zen` 模式**：直接驱动 Zed 的 Antigravity ACP 独立后端包（`agy_acp_server.par` / `agy_acp_server`）。脚本默认从官方 ACP Registry 自动查询最新版本与 **SHA-256** 校验和，也支持通过 `ZED_ACP_SHA256`（或 `--zed-sha256`）手动固定哈希。
   - **`auto` 自动探测模式**：跨 macOS、Windows、Linux 与 Docker 自动探测本机已安装的 `agy` CLI 或 Zed ACP 扩展包，并内置二进制文件头（ELF / Mach-O / PE）跨系统防冲突校验。

3. **四层粘性会话（Sticky Session）与进程亲和性（Worker Affinity）**
   - **工具挂起强粘性（Parked Tool Continuation）**：模型发起客户端工具调用时，底层 Worker 进程进入 `Parked` 独占挂起态；客户端回传 `tool_result` 时 100% 唤醒原进程与原会话，并自动折叠流式连续工具调用输出。
   - **显式会话 ID 与客户端会话提取**：自动从请求头（`X-ACP-Session-ID`、`X-Conversation-ID`）、请求体（`prompt_cache_key`、`previous_response_id`、`metadata.session_id`）及系统提示词中提取真实客户端会话 ID 与会话标题。
   - **历史消息前缀哈希粘性**：即使无状态客户端不传会话 ID，网关也会通过 SHA-256 历史指纹自动路由至同一 `ManagedSession`。
   - **防抢占空闲进程分配**：新会话优先分配未绑定会话的预热空闲 Worker，保护已驻留活跃会话的进程，实测多轮对话 Prompt 缓存命中率稳定在 **94%–100%**。

4. **多账户容器隔离池与出口路由**
   - 每个账户拥有独立的 `GEMINI_CLI_HOME` 容器目录（`data/accounts/<id>/.gemini`）与独立 Worker 子进程池。
   - 支持 **Direct 直连**、**SOCKS5 / HTTP 代理**、**源网卡 IP 绑定（Bind IP）** 与 **Linux Network Namespace（NetNS）** 四种网络隔离模式，支持无头浏览器 OAuth 登录引导与周期模型配额监控。

5. **中英双语 ReUI 实时可观测控制台与在线自更新**
   - 顶栏一键切换 **`中文 / EN`**，全界面实时翻译同步。
   - **在线版本更新（`系统更新`）**：控制台内置双通道在线更新面板——支持直接从 **GitHub Releases** 拉取全平台预编译网关包（`SHA256SUMS` 强校验 + ELF/Mach-O/PE 架构头验证 + 原地平滑重启并自动刷新页面），同时支持免重启热更新底层 **Antigravity CLI / Zed ACP 引擎**（平滑轮换空闲 Worker，不中断正在生成的会话；Docker 或只读 systemd 环境自动识别并给出宿主机更新命令）。
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

- **源码一键编译部署环境要求**：`Go >= 1.27.3`（以 `go.mod` 为准）、`Node.js >= 20`（含 `npm`）、`curl`，以及 `sha512sum` / `shasum` / `openssl` 其一（用于哈希校验）；Zed 后端还需要 `unzip`。
- **Docker 部署环境要求**：`Docker` 与 `Docker Compose v2.24+`（宿主机无需安装 Go / Node.js）。
- **预编译包部署环境要求**：目标服务器仅需 `curl` 与上述哈希工具（用于自动拉取 ACP 后端，或直接拷贝二进制免依赖运行）。

> **从源码构建说明**：Web 控制台需先构建（`cd web && npm ci && npm run build`），再用 `go build -tags webui ./cmd/gateway` 内嵌 `web/dist`。不带 `webui` 标签的 `go build ./...` 在全新克隆上也能通过，但只会内嵌一个提示“控制台未构建”的占位页。部署脚本、Dockerfile 与 CI 均已自动带上该标签。

---

### 二、 macOS 与 Linux（x86_64 / ARM64）一键部署（`deploy/deploy.sh`）

赋予部署脚本执行权限：

```bash
chmod +x deploy/deploy.sh deploy/update.sh
```

#### 1. 本地一键编译并后台启动（最简方式）
默认使用 `cli` 模式（`agy acp`）。若本机未检测到 `agy`，脚本会自动识别系统与 CPU 架构（如 `linux_arm64`、`linux_amd64`、`darwin_arm64`），从官方发布源下载并完成 SHA-512 校验后安装至 `./bin/agy`。启动后脚本会轮询 `/healthz`（最长 20 秒），确认健康后才报告成功：

```bash
./deploy/deploy.sh --mode local --port 8080 --admin-password-stdin   # 交互输入初始管理员密码（可省略，省略则自动生成）
```

- 管理员密码**不会写入** `config/config.yaml`，而是仅通过 `AG_ADMIN_PASSWORD` 环境变量传给网关进程；`--admin-password <pwd>` 仍可用，但会留在 shell 历史与 `ps` 中，脚本会给出警告。
- 本地模式以 `umask 077` 启动，数据目录权限为 `0700`，日志 `data/gateway.log` 权限为 `0600`。

#### 2. 注册为 Linux `systemd` / macOS `launchd` 开机自启服务（生产推荐）
在 Linux 服务器（含 x86_64 与 ARM64）上，以下命令会自动完成前端构建、Go 网关编译与 ACP 后端下载，并：
- 创建专用系统用户 `antigravity`（`useradd --system`，家目录 `/var/lib/antigravity-gateway`，shell 为 `nologin`），服务**不再以 root 运行**；
- 将网关与 ACP 后端以 root 所有、`0755` 权限安装到 `/usr/local/bin`（服务始终使用 `/usr/local/bin/agy`，不会执行源码目录中的二进制）；
- 将配置安装到 `/etc/antigravity-gateway/config.yaml`（旧配置会自动备份），管理员密码写入 `/etc/antigravity-gateway/env`（`root:root 0600`，通过 `EnvironmentFile=` 加载）；
- 执行 `systemctl daemon-reload`、`enable` 与 `restart`（重复部署也会重启生效），并等待 `/healthz` 通过。

```bash
./deploy/deploy.sh --mode service --port 8080 --admin-password-stdin
```

`deploy/antigravity-gateway.service` 启用了 systemd 沙箱加固：`NoNewPrivileges`、`ProtectSystem=strict`（仅 `/var/lib/antigravity-gateway` 可写）、`ProtectHome=tmpfs`、`PrivateTmp`、`PrivateDevices`、`ProtectKernelTunables/Modules/Logs`、`ProtectControlGroups`、`RestrictSUIDSGID`、`LockPersonality`、清空能力集与 `UMask=0077`。服务目录布局：

| 路径 | 用途 |
| :--- | :--- |
| `/var/lib/antigravity-gateway/data` | `AG_DATA_DIR`：`gateway.db`、`admin_credentials.txt`、多账户目录 |
| `/var/lib/antigravity-gateway/workspace` | `AG_WORKSPACE_DIR`：ACP 会话工作区（agent CLI 需要写入） |
| `/var/lib/antigravity-gateway/.gemini` | 服务用户默认账户的凭据目录（`HOME`） |

> 账户网络模式 `netns` 依赖 `ip netns exec`（需要 `CAP_SYS_ADMIN`），在该加固服务下不可用，请改用 `proxy` 或 `bind_ip`。

Linux `systemd` 服务常用运维命令（也可用 `./deploy/deploy.sh --mode service --action start|stop|restart|status`）：
```bash
sudo systemctl status antigravity-gateway.service     # 查看服务运行状态
sudo journalctl -u antigravity-gateway.service -f     # 实时查看网关日志
sudo systemctl restart antigravity-gateway.service    # 重启服务
sudo cat /var/lib/antigravity-gateway/data/admin_credentials.txt   # 查看动态管理凭证
```

macOS 下 `--mode service` 会在 `~/Library/LaunchAgents/com.antigravity.gateway.plist`（权限 `0600`）注册当前用户的 launchd 服务。

#### 3. 一键拉取最新代码并同步更新 CLI / Zed ACP 后端包（`deploy/update.sh`）
无论当前运行在 `local`、`service`（systemd / launchd）还是 `docker` 模式，直接运行 `./deploy/update.sh`（或 `./deploy/deploy.sh --action update`）即可完成全链路更新：
1. 自动暂存本地未提交改动并执行 `git pull --ff-only` 拉取最新源码（若目录是从压缩包解压部署、尚未初始化 `.git`，会自动关联官方远程仓库同步至最新提交），如脚本自身有更新会自动重载；
2. 自动识别当前活跃运行模式（`docker` / `service` / `local`）与后端类型；
3. **同步更新 ACP 后端包**：按当前模式或 `--backend` 参数重新从官方源拉取最新版本的 **Antigravity CLI（`agy`）** 或 **Zed ACP 独立后端包（`agy_acp_server.par`）** 并完成哈希校验（Docker 模式通过 `AG_ACP_CACHE_BUST` 强制刷新镜像内 ACP 后端层）；
4. 重新构建前端与 Go 网关并平滑重启，轮询 `/healthz` 确认健康。

```bash
# 一键拉取最新代码 + 更新当前已启用/已安装的 ACP 后端包 + 重新编译并重启服务
./deploy/update.sh

# 一键更新代码并指定更新（或切换为）Antigravity CLI / Zed ACP 独立后端包 / 同时更新两者
./deploy/update.sh --backend cli
./deploy/update.sh --backend zed
./deploy/update.sh --backend both

# 仅下载或更新 ACP 后端包到最新版本（不拉代码、不重启网关）
./deploy/update.sh --action update-acp --backend cli
./deploy/update.sh --action update-acp --backend zed
./deploy/update.sh --action update-acp --backend both

# 离线环境部署（手动指定已有二进制路径并跳过自动下载）
./deploy/deploy.sh --mode local --backend cli --acp-bin "/path/to/agy" --no-download
```

下载完整性校验规则（`deploy.sh`、`deploy.ps1`、`Dockerfile` 一致）：
- **CLI**：使用官方清单中的 SHA-512。清单获取失败、缺少哈希或本机无哈希工具时直接报错；仅当显式传入 `--insecure-skip-verify` 时才允许“缺少哈希/工具”的情况继续。
- **Zed 包**：默认自动查询官方 ACP Registry（`https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json` 中 `antigravity-acp` 条目的 `distribution.binary.<平台>.sha256`）获取最新版本与 SHA-256；也可通过 `ZED_ACP_SHA256` 环境变量或 `--zed-sha256`、`--zed-version` 手动固定。
- **哈希不匹配始终是致命错误**，不会回退到其他下载方式。
- 官方安装脚本 `https://antigravity.google/cli/install.sh` 仅在显式传入 `--allow-install-script` 且清单不可用时使用：脚本先下载到临时文件并完整展示（交互终端下需确认），绝不 `curl | bash`。

#### 4. 进程管理命令（按 `--mode` 路由）
```bash
./deploy/deploy.sh --action status                    # 本地模式：查看运行状态与 PID
./deploy/deploy.sh --action restart                   # 本地模式：平滑重启（等待旧进程退出，最长 15 秒）
./deploy/deploy.sh --action stop                      # 本地模式：停止后台进程
./deploy/deploy.sh --mode service --action restart    # 等价于 systemctl restart / launchctl 重新加载
./deploy/deploy.sh --mode docker --action status      # 等价于 docker compose ps
```

本地模式的 `stop` / `restart` 只会终止 PID 文件中记录、且命令行确认为本仓库 `bin/antigravity-gateway` 的进程；若端口被其他进程占用，只会提示“端口被 PID X 占用（非本网关进程）”，不会终止它。`start` / `restart` 未指定 `--backend` / `--port` 时沿用 `config.yaml` 中的值，不会再静默重置为 `cli` / `8080`。

#### 5. `deploy/deploy.sh` 参数速查表

| 参数 | 可选值 | 默认值 | 说明 |
| :--- | :--- | :--- | :--- |
| `--action` | `install` \| `update` \| `install-acp` \| `update-acp` \| `start` \| `stop` \| `restart` \| `status` \| `build` \| `build-all` | `install`（`update.sh` 为 `update`） | 执行动作（`update` 拉取最新代码+更新 ACP 包+重启；`update-acp` 仅更新 ACP 后端） |
| `--mode` | `local` \| `service` \| `docker` | `local`（`update` 自动识别） | 部署方式：本地后台进程、系统开机自启服务、Docker 容器；启停与状态动作同样按此路由 |
| `--backend` | `cli` \| `zed` \| `zen` \| `auto` \| `both` | 沿用 `config.yaml` | ACP 后端引擎类型（仓库默认 `cli`；`both` 同时更新 CLI 与 Zed ACP 包） |
| `--acp-bin` | 文件路径 | 空（自动发现/下载） | 显式指定 `agy` 或 `agy_acp_server.par` 路径 |
| `--no-download` | 无参数标志 | 关闭（默认自动下载） | 禁用自动下载/更新 ACP 后端 |
| `--no-git` / `--skip-git` | 无参数标志 | 关闭 | 在 `--action update` 时跳过 `git pull` 代码拉取 |
| `--port` | 端口号 | `AG_PORT`，否则 `config.yaml`（docker 为 `8080`） | 本地/服务模式为监听端口；docker 模式仅为宿主机映射端口（容器内固定监听 `8080`） |
| `--admin-password-stdin` | 无参数标志 | 关闭 | 从标准输入或交互提示读取初始管理员密码（至少 8 位） |
| `--admin-password` | 字符串 | 空（自动生成） | 同上但直接写在命令行（不推荐）；仅在控制台修改密码前生效 |
| `--zed-version` | 版本号 | 官方 ACP Registry 最新版 | 指定 Zed ACP 压缩包版本（或设置 `ZED_ACP_VERSION`） |
| `--zed-sha256` | 64 位十六进制 | 自动从 ACP Registry 解析 | Zed ACP 压缩包的预期 SHA-256（或设置 `ZED_ACP_SHA256`） |
| `--insecure-skip-verify` | 无参数标志 | 关闭 | 允许在缺少预期哈希或哈希工具时继续（哈希不匹配仍会失败） |
| `--allow-install-script` | 无参数标志 | 关闭 | 清单不可用时允许下载并执行官方安装脚本（先展示内容） |
| `--lang` | `zh` \| `en` | `zh` | 脚本终端输出语言 |

---

### 三、 Docker 与 Docker Compose 部署（原生支持 `linux/amd64` 与 `linux/arm64`）

`deploy/Dockerfile` 采用多阶段构建（基础镜像固定到具体版本标签，由 Dependabot 跟踪升级），前端使用 `npm ci` 按锁文件安装，并在运行时镜像阶段自动检测容器架构（`x86_64` 或 `aarch64`），下载并校验安装对应的 Linux ACP 后端（`/usr/local/bin/agy` 或 `agy_acp_server.par`）。运行时镜像以非 root 用户 `agy`（UID `10001`）运行，并内置基于 `/healthz` 的 `HEALTHCHECK`。

#### 1. 使用部署脚本一键启动容器
```bash
# 默认使用 cli 后端构建并启动容器（端口默认只绑定 127.0.0.1）
./deploy/deploy.sh --mode docker --port 8080 --admin-password-stdin

# 显式对所有网卡开放端口
AG_BIND=0.0.0.0 ./deploy/deploy.sh --mode docker --port 8080

# 如需在容器中使用 Zed ACP 独立后端包（必须提供固定 SHA-256，作为构建参数传入）
ZED_ACP_SHA256="<agy-acp-server-1.4.5-linux-<arch>.zip 的 sha256>" ./deploy/deploy.sh --mode docker --backend zed --port 8080
```

`--port` 在 docker 模式下只改变宿主机映射端口（`AG_PORT`），容器内始终监听 `8080`，不会改写挂载的 `config.yaml`。

#### 2. 直接使用 `docker compose` 启动与管理
```bash
# 可选：配置初始管理员密码（deploy/gateway.env 已被 git 忽略）
cp deploy/gateway.env.example deploy/gateway.env && chmod 600 deploy/gateway.env   # 编辑并取消注释 AG_ADMIN_PASSWORD

# 构建并后台启动（默认 AG_ACP_BACKEND=cli，支持 x86_64 与 ARM64 Docker 宿主机）
AG_ACP_BACKEND=cli AG_PORT=8080 docker compose -f deploy/docker-compose.yml up -d --build

# 需要从其他机器访问时，显式绑定所有网卡
AG_BIND=0.0.0.0 docker compose -f deploy/docker-compose.yml up -d

# 查看容器实时日志 / 查看动态管理凭证
docker compose -f deploy/docker-compose.yml logs -f
docker exec antigravity-gateway cat /var/lib/antigravity-gateway/admin_credentials.txt

# 停止并移除容器（数据卷保留）
docker compose -f deploy/docker-compose.yml down
```

Compose 支持的变量：`AG_BIND`（默认 `127.0.0.1`）、`AG_PORT`（默认 `8080`）、`AG_ACP_BACKEND`、`AG_ACP_BINARY_PATH`（默认 `auto`）、构建参数 `ZED_ACP_SHA256` / `ZED_ACP_VERSION` / `AG_INSECURE_SKIP_VERIFY`（设为 `1` 才允许在缺少预期哈希时构建）。注意：容器访问网关时来源地址不是回环地址，因此北向 API 必须先在控制台创建 API Key。

#### 3. 容器持久化目录说明
- `gateway-data`（挂载于容器内 `/var/lib/antigravity-gateway`）：持久化存储 SQLite 审计与配置数据库 `gateway.db`、`admin_credentials.txt`，以及多账户隔离凭据目录 `accounts/<id>/.gemini`。
- `gemini-home`（挂载于容器内 `/home/agy/.gemini`，旧版本为 `/root/.gemini`）：默认账户凭据缓存目录。
- `../bin`（只读挂载于容器内 `/opt/antigravity-acp`）：可选自定义 ACP 二进制目录（`deploy.sh` 会自动创建 `./bin`）。网关内置跨系统二进制文件头识别，即使宿主机 `./bin` 存在 macOS/Windows 编译产物，容器内也会自动跳过并使用容器内置的 Linux `/usr/local/bin/agy`。

> **从旧版（root 容器）升级**：已有数据卷中的文件归 root 所有，需一次性修正属主：
> `docker compose -f deploy/docker-compose.yml run --rm --user root --entrypoint chown antigravity-gateway -R 10001:10001 /var/lib/antigravity-gateway /home/agy/.gemini`

---

### 四、 免 Go/Node 环境部署（交叉编译后上传至 ARM64 / x86_64 服务器）

如果目标生产服务器（如轻量云主机或内网 ARM64 / x86_64 Linux 服务器）不想安装 Go 与 Node.js 编译环境，可在本地开发机一键交叉编译全平台独立发行包：

```bash
# 1. 在开发机执行全平台交叉编译
./deploy/deploy.sh --action build-all
```

编译完成后，`./dist/` 目录下会生成已内嵌前端资源的独立发行包（`./dist` 已被 git 忽略；包内 `config/config.yaml` 取自 git 中的原始版本，不含本地修改）：
- `dist/antigravity-gateway-linux-arm64/`（适用于 ARM64 / aarch64 Linux 服务器，如 AWS Graviton、Oracle ARM、树莓派、鲲鹏等）
- `dist/antigravity-gateway-linux-amd64/`（适用于常规 x86_64 Linux 服务器）
- `dist/antigravity-gateway-darwin-arm64/`、`dist/antigravity-gateway-darwin-amd64/`
- `dist/antigravity-gateway-windows-amd64/`、`dist/antigravity-gateway-windows-arm64/`

```bash
# 2. 将对应架构目录上传至目标 Linux 服务器
scp -r ./dist/antigravity-gateway-linux-arm64 deploy@your-server:/opt/antigravity-gateway

# 3. 登录目标服务器（建议使用非 root 用户），自动下载 ACP 后端并启动
ssh deploy@your-server
cd /opt/antigravity-gateway
chmod +x antigravity-gateway antigravity-probe deploy/deploy.sh
./deploy/deploy.sh --action install-acp --backend cli
mkdir -p -m 700 data && umask 077
nohup ./antigravity-gateway -config ./config/config.yaml >> data/gateway.log 2>&1 &
cat data/admin_credentials.txt   # 查看本次动态生成的管理凭证
```

---

### 五、 Windows 一键部署（PowerShell `deploy/deploy.ps1`）

在 Windows 10/11 或 Windows Server（支持 `x64` 与 `ARM64`）上打开 PowerShell 执行：

```powershell
# 方式 A：本地一键编译并后台启动（默认 cli 模式，缺失时自动下载 agy.exe 并完成 SHA-512 校验）
.\deploy\deploy.ps1 -Mode local -Port 8080 -AdminPasswordStdin

# 方式 B：一键拉取最新代码并同步更新 CLI / Zed ACP 后端包（.\deploy\update.ps1）
.\deploy\update.ps1
.\deploy\update.ps1 -Backend zed
.\deploy\update.ps1 -Backend both

# 方式 C：仅下载/更新 ACP 后端可执行文件到 .\bin
.\deploy\deploy.ps1 -Action update-acp -Backend cli

# 方式 D：注册为开机启动的 Windows 计划任务（需在管理员 PowerShell 中执行）
.\deploy\deploy.ps1 -Mode service -Port 8080 -AdminPasswordStdin

# 常用状态与启停管理（同样按 -Mode 路由）
.\deploy\deploy.ps1 -Action status
.\deploy\deploy.ps1 -Action restart
.\deploy\deploy.ps1 -Mode service -Action stop
```

- 与 `deploy.sh` 一致：哈希缺失即失败（`-InsecureSkipVerify` 可放行“缺少哈希”，不匹配始终失败），密码不会写入 `config.yaml`，`stop` 只终止经路径确认的 `bin\antigravity-gateway.exe`，未指定 `-Backend` / `-Port` 时沿用配置文件。
- 服务模式使用 `Register-ScheduledTask` 注册**开机触发**（`AtStartup`、`S4U` 当前用户、无执行时限）的任务 `AntigravityACPGateway`，通过 `data\run-gateway.cmd` 设置工作目录并将输出写入 `data\gateway.log`；`data` 目录 ACL 仅授予当前用户。

---

### 六、 部署后首次配置流程（控制台登录 -> 账户授权 -> 签发 API Key）

1. **登录管理控制台**
   - 浏览器打开 `http://<服务器IP>:8080`，输入管理员密码。默认配置为 `admin_password: "auto"` / `admin_token: "auto"`：每次启动都会随机生成密码与 Token，写入 `<data_dir>/admin_credentials.txt`（权限 `0600`，**不会打印到日志**）；部署脚本结束时会显示该文件内容及查看命令。
   - 登录后点击右上角「修改密码」（至少 8 位；自定义 Token 至少 12 位）即永久固定，凭证以加盐 PBKDF2-SHA256 哈希保存在 `gateway.db` 中，之后通过 `AG_ADMIN_PASSWORD` 或 `--admin-password-stdin` 传入的密码不再生效。
2. **添加账户并完成 OAuth 终端登录**
   - 进入 **「账户容器池（Account Pool）」** 标签页，可使用默认账户 `default` 或点击 **「新建隔离账户」**（可为每个账户独立配置 Direct 直连、SOCKS5/HTTP 代理、Bind IP 或 Linux NetNS）。
   - 点击账户卡片上的 **「终端登录（OAuth）」**，网关会在隔离容器目录内拉起无头登录会话并提取 Google OAuth 授权链接。
   - 在本地浏览器打开该授权链接完成登录（若部署在远程无桌面服务器上，将浏览器跳转后的回调 URL 或授权码粘贴回控制台弹窗即可完成认证）。
   - 认证成功后账户状态变为 `ready`，可点击 **「刷新额度」** 实时查看 Gemini 与 Claude/GPT 模型组的 5 小时及每周剩余配额百分比。
3. **签发北向 API Key**
   - 进入 **「北向令牌（API Keys）」** 标签页，创建或复制 `sk-ag-...` 令牌（可绑定指定账户或设为 `auto` 自动负载均衡）。
   - 尚未创建任何 API Key 时，`/v1/*` 仅允许本机回环地址（`127.0.0.1` / `::1`）免密访问；远程客户端与 Docker 宿主机访问都必须先创建 API Key。绑定了账户的 Key 无法再通过 `X-ACP-Account-ID` 或 `?account_id=` 切换到其他账户。

---

## 配置文件说明（`config/config.yaml`）

```yaml
server:
  host: "0.0.0.0"
  port: 8080
  public_base_url: "http://localhost:8080"
  data_dir: "./data"            # 启动时以 0700 权限创建
  admin_token: "auto"           # "auto": 每次启动随机生成，写入 <data_dir>/admin_credentials.txt
  admin_password: "auto"        # 请勿在此填写真实密码（该文件受 git 跟踪），改用 AG_ADMIN_PASSWORD

acp_pool:
  # backend_type 可选值:
  # - "cli" (默认)  : 使用 Antigravity CLI (agy acp / antigravity acp)
  # - "zed" / "zen" : 使用 Zed/Zen ACP 独立后端包 (agy_acp_server.par / agy_acp_server)
  # - "auto"        : 优先使用 binary_path，不存在时自动探测 agy CLI 或 Zed/Zen ACP 包
  backend_type: "cli"
  binary_path: "./bin/agy"      # 不存在时按 backend_type 自动探测；也可设为 "auto"
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
- `AG_ADMIN_PASSWORD` / `AG_ADMIN_TOKEN`：初始管理员密码（至少 8 位）与管理令牌（至少 12 位），仅在控制台修改密码之前生效
- `AG_WEB_DIR`：可选，前端开发时优先从该目录读取静态文件（未设置时始终使用内嵌资源）

---

## 安全默认行为

- **北向 API 鉴权**：没有任何 API Key 时，`/v1/*` 只接受来自回环地址（`127.0.0.1` / `::1`）的免密请求，请先在控制台创建 API Key。`?key=` 查询参数仍可用于北向 API；`?token=` 已不再接受。
- **账户与会话隔离**：绑定账户的 API Key 不能通过 `X-ACP-Account-ID` 或 `?account_id=` 改投其他账户；会话按 API Key 隔离。
- **ACP WebSocket 工作区**：`/v1/acp/ws` 会话始终运行在网关管理的工作区目录内，客户端传入的工作区外 `cwd` 会被改写。
- **管理凭证**：管理员密码与 Token 不再打印到日志，请查看 `<data_dir>/admin_credentials.txt`。密码以加盐 PBKDF2-SHA256 存储，密码至少 8 位、自定义 Token 至少 12 位；管理 API 的 `Authorization: Bearer` 只接受管理 Token 或登录会话 Token，不接受密码；所有端点的凭证失败尝试按 IP 限速。
- **管理接口防护**：改变状态的管理接口必须使用 POST/PUT/DELETE，跨站的状态变更请求会被拒绝，会话 Cookie 为 `SameSite=Strict`，WebSocket 升级会校验 `Origin`。管理 API 响应不再包含账户 `api_key`，代理密码显示为 `********`。
- **内部 MCP 桥**：`/mcp/sessions/*` 只接受回环连接。
- **子进程环境**：ACP 子进程不再继承 `AG_*` 环境变量。
- **账户 ID**：仅允许 `[A-Za-z0-9._-]`，长度 1–64。

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
  - 自定义请求头（可选）：`X-ACP-Context-Window: 1048576`、`X-ACP-Account-ID: auto`（仅对未绑定账户的 API Key 生效）

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

