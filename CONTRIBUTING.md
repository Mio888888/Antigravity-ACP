# Contributing Guide / 贡献指南

**English** | [简体中文](#简体中文)

---

## English

Thank you for your interest in contributing to **Antigravity ACP Gateway**! This document provides guidelines and workflows for reporting issues, proposing features, developing locally, and submitting Pull Requests.

### 1. Code of Conduct & License

- By participating in this project, you agree to abide by our [Code of Conduct](./CODE_OF_CONDUCT.md).
- By submitting a Pull Request, you agree that your contributions will be licensed under the project's [GNU General Public License v3.0 (GPL-3.0)](./LICENSE).

### 2. Project Architecture Overview

```text
.
├── cmd/
│   ├── gateway/          # Main HTTP/WS gateway binary entrypoint
│   └── probe/            # Standalone ACP diagnostic & probing CLI tool
├── config/               # Default configuration template (config.yaml)
├── deploy/               # Cross-platform deployment scripts (deploy.sh, deploy.ps1, Dockerfile, systemd)
├── internal/
│   ├── acp/              # ACP subprocess pool, CLI/Zed backend discovery, JSON-RPC session lifecycle, quota & OAuth
│   ├── adapter/          # Protocol translators (OpenAI Responses, Chat Completions, Claude Messages <-> ACP)
│   ├── config/           # Configuration loader & environment variable overrides
│   ├── server/           # Northbound & Admin HTTP handlers, middleware, SSE & WebSocket bridges
│   └── storage/          # Pure-Go SQLite persistence (audit logs, accounts, API keys, sticky sessions)
└── web/                  # React 19 + TypeScript + Tailwind CSS v4 + ReUI Bilingual Admin Console
```

### 3. Development Prerequisites

- **Go**: `>= 1.23` (CGO is disabled by default; uses pure-Go `modernc.org/sqlite`)
- **Node.js**: `>= 20` (with `npm`)
- **ACP Backend** (for live end-to-end testing): Official Antigravity CLI (`agy`) or standalone Zed ACP binary (`agy_acp_server.par`), which can be auto-downloaded via `./deploy/deploy.sh --action install-acp --backend cli`.

### 4. Local Setup & Verification

#### Build & Test Backend (Go)
```bash
# Run all Go unit and integration tests
go test ./...

# Build gateway and probe binaries
CGO_ENABLED=0 go build -o ./antigravity-gateway ./cmd/gateway
CGO_ENABLED=0 go build -o ./antigravity-probe ./cmd/probe
```

#### Build & Develop Frontend (`web/`)
```bash
cd web
npm install

# Start Vite dev server for local UI development
npm run dev

# Type-check and build production SPA bundle (embedded by embed.go)
npm run build
```

### 5. Coding & Design Standards

1. **Root-Cause Engineering (No Fallback Hacks)**
   - Fix root causes directly rather than masking symptoms with silent fallbacks, fabricated default values, or hardcoded workaround strings.
   - Surface clear, actionable errors when prerequisites, credentials, or upstream protocol states fail.
2. **Bilingual UI & Documentation**
   - Every user-facing string in the Web Admin Console (`web/src/i18n.tsx`) must include both **Simplified Chinese (`zh`)** and **English (`en`)** translations.
   - Documentation updates in `README.md` and `README_EN.md` should stay synchronized.
3. **ReUI Design System Compliance**
   - Follow the project's ReUI design rules in [`AGENTS.md`](./AGENTS.md): use semantic color tokens, `gap-*` spacing, `text-sm` body / `text-xs` meta typography, and provide empty, loading (`Skeleton`), and error states for every data region.
4. **Security & Data Hygiene**
   - Never commit real OAuth tokens, `.gemini` account state (`data/`), `sk-ag-*` keys, or private server addresses/IPs.

### 6. Commit Message & Pull Request Workflow

1. **Branching**: Create a descriptive branch from `main` (e.g., `feat/acp-session-metrics` or `fix/responses-stream-parser`).
2. **Conventional Commits**: Format commit messages as:
   - `feat(scope): concise description`
   - `fix(scope): concise description`
   - `perf(scope): concise description`
   - `docs(scope): concise description`
   - `refactor(scope): concise description`
   - Common scopes: `server`, `acp`, `adapter`, `storage`, `web`, `deploy`, `docs`.
3. **Pre-PR Checklist**:
   - `go test ./...` passes cleanly.
   - `cd web && npm run build` succeeds with zero TypeScript or bundler errors.
   - Both `README.md` and `README_EN.md` are updated if configuration flags, endpoints, or deployment behaviors changed.

---

<a id="简体中文"></a>

## 简体中文

感谢您关注并参与 **Antigravity ACP Gateway** 开源项目！本文档说明了问题反馈、功能提议、本地开发调试以及提交 Pull Request（PR）的完整规范。

### 1. 行为准则与开源协议

- 参与本项目即表示您同意遵守我们的 [行为准则 (Code of Conduct)](./CODE_OF_CONDUCT.md)。
- 提交 Pull Request 即表示您同意将所贡献的代码按照本项目的 [GNU 通用公共许可证 v3.0 (GPL-3.0)](./LICENSE) 进行开源授权。

### 2. 项目目录与架构速览

```text
.
├── cmd/
│   ├── gateway/          # 网关主程序入口（HTTP / SSE / WebSocket 服务）
│   └── probe/            # 独立 ACP 协议探测与诊断命令行工具
├── config/               # 默认配置文件模板 (config.yaml)
├── deploy/               # 跨平台一键部署脚本 (deploy.sh, deploy.ps1, Dockerfile, systemd)
├── internal/
│   ├── acp/              # ACP 进程池、CLI/Zed 后端探测、JSON-RPC 会话生命周期、配额与 OAuth 认证
│   ├── adapter/          # 多协议转换器 (OpenAI Responses / Chat Completions / Claude Messages <-> ACP)
│   ├── config/           # 配置加载与环境变量覆盖
│   ├── server/           # 北向接口与管理后台 HTTP 路由、中间件、SSE 与 WebSocket 桥接
│   └── storage/          # 纯 Go SQLite 持久化层（审计日志、多账户容器、API 令牌、粘性会话）
└── web/                  # React 19 + TypeScript + Tailwind CSS v4 + ReUI 中英双语管理控制台
```

### 3. 本地开发环境要求

- **Go**：`>= 1.23`（默认禁用 CGO，采用纯 Go `modernc.org/sqlite` 引擎）
- **Node.js**：`>= 20`（含 `npm`）
- **ACP 后端二进制**（用于端到端联调）：官方 Antigravity CLI（`agy`）或 Zed 独立 ACP 后端包（`agy_acp_server.par`），可通过 `./deploy/deploy.sh --action install-acp --backend cli` 自动下载校验。

### 4. 本地构建与测试命令

#### 后端编译与单元测试（Go）
```bash
# 运行全部 Go 单元测试与集成测试
go test ./...

# 编译网关主程序与探测工具
CGO_ENABLED=0 go build -o ./antigravity-gateway ./cmd/gateway
CGO_ENABLED=0 go build -o ./antigravity-probe ./cmd/probe
```

#### 前端开发与构建（`web/`）
```bash
cd web
npm install

# 启动本地前端开发热更新服务
npm run dev

# 执行 TypeScript 类型检查并生成生产静态产物（由根目录 embed.go 内嵌）
npm run build
```

### 5. 代码与设计规范

1. **直击根因，严禁兜底补丁（No Fallback Hacks）**
   - 修复问题必须定位并解决根因，严禁编写静默降级、伪造默认值或硬编码替换词等掩盖症状的兜底逻辑。
   - 当依赖缺失、鉴权失败或上游协议异常时，应当返回清晰、可定位的真实错误信息。
2. **全量中英双语同步**
   - Web 管理控制台（`web/src/i18n.tsx`）新增或修改的任何界面文案，必须同时提供**简体中文（`zh`）**与**英文（`en`）**翻译。
   - 涉及部署参数、接口行为或配置项变更时，请同步更新 `README.md` 与 `README_EN.md`。
3. **遵循 ReUI 设计系统规范**
   - 前端改动须严格遵循 [`AGENTS.md`](./AGENTS.md) 中的 ReUI 规范：使用语义化颜色令牌、父容器 `gap-*` 间距、`text-sm` 正文与 `text-xs` 元数据字号，并为所有数据区域提供空状态、加载态（`Skeleton`）与错误重试状态。
4. **敏感凭据安全保护**
   - 严禁在代码库或提交记录中包含真实的 OAuth 凭据、`data/` 账户状态目录、`sk-ag-*` 密钥或私有服务器 IP/域名。

### 6. Commit 规范与 Pull Request 流程

1. **分支命名**：从 `main` 创建语义化分支（如 `feat/acp-session-metrics` 或 `fix/responses-stream-parser`）。
2. **提交信息规范（Conventional Commits）**：
   - `feat(scope): 简明描述新功能`
   - `fix(scope): 简明描述修复的根因问题`
   - `perf(scope): 性能优化描述`
   - `docs(scope): 文档完善描述`
   - `refactor(scope): 重构描述`
   - 常用 `scope`：`server`、`acp`、`adapter`、`storage`、`web`、`deploy`、`docs`。
3. **提交 PR 前自检清单**：
   - `go test ./...` 全部通过。
   - `cd web && npm run build` 零错误完成构建。
   - 变更内容已按 PR 模板填写完整说明与验证结果。
