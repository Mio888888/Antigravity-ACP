# Changelog / 版本变更日志

**English** | [简体中文](#简体中文)

All notable changes to **Antigravity ACP Gateway** are documented in this file.
This project adheres to [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.6.0] - 2026-11-10

### Added / 新增特性
- **Client Context Compaction Priority with ACP Fallback / 客户端上下文压缩优先与 ACP 降级联动**:
  - Prioritize client-side context compaction state across multi-turn conversations while preserving sticky session routing (`X-ACP-Session-ID`, `prompt_cache_key`, `previous_response_id`) and seamlessly falling back to ACP session reconstruction when needed.
  - 优先识别并透传客户端主动发起的上下文压缩（Context Compaction）状态，保持会话 ID 强粘性路由，并在必要时平滑联动底层 ACP 会话重建。
- **Dual ACP Backend Auto-Download & Verification (`cli` Default) / 双 ACP 后端自动下载与哈希校验（默认 `cli`）**:
  - Default `acp_pool.backend_type` to `cli` (`agy acp`), with automatic OS/Arch (`darwin/linux/windows`, `amd64/arm64`) detection and SHA-512 verified installation in `deploy.sh`, `deploy.ps1`, and `Dockerfile`.
  - Retained full support for standalone Zed/Zen ACP packages (`agy_acp_server.par`) with SHA-256 verification.
  - 默认切换为官方 `cli` 后端模式（`agy acp`），支持全平台自动下载与 SHA-512 校验安装；同时完整保留 Zed/Zen 独立 ACP 后端包（`agy_acp_server.par`）及 SHA-256 校验支持。
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
