## Summary / 变更概述

<!-- Briefly describe what problem this PR solves and the root-cause approach taken. -->
<!-- 请简明描述本 PR 解决的问题、功能背景以及直击根因的实现方案。 -->

## Related Issues / 关联 Issue

<!-- e.g., Closes #123 or Fixes #456 -->
- Closes #

## Type of Change / 变更类型

- [ ] `feat`: New feature / 新功能特性
- [ ] `fix`: Root-cause bug fix (no fallback hacks) / 根因缺陷修复（无兜底补丁）
- [ ] `perf`: Performance or telemetry optimization / 性能或遥测优化
- [ ] `refactor`: Code refactoring without behavior change / 代码重构
- [ ] `deploy`: Deployment script, Docker, or CI update / 部署脚本、Docker 或 CI 更新
- [ ] `docs`: Bilingual documentation update (`zh` & `en`) / 中英双语文档更新

## Affected Modules / 涉及模块

- [ ] `internal/acp` (Worker pool, CLI/Zed backend, session lifecycle, OAuth/quota)
- [ ] `internal/adapter` (OpenAI Responses / Chat Completions / Claude Messages translators)
- [ ] `internal/server` (HTTP handlers, SSE streaming, WebSocket bridge, middleware)
- [ ] `internal/storage` (SQLite persistence, accounts, API keys, audit logs)
- [ ] `web/` (React + TypeScript + ReUI bilingual admin console)
- [ ] `deploy/` (`deploy.sh`, `deploy.ps1`, `Dockerfile`, `docker-compose.yml`)
- [ ] Documentation (`README.md`, `README_EN.md`, governance files)

## Verification & Self-Check / 测试验证与自检清单

- [ ] **Go Tests / 后端测试**: `go test ./...` passes cleanly / 全部通过
- [ ] **Web Build / 前端构建**: `cd web && npm run build` succeeds with zero TypeScript errors / 零错误通过构建
- [ ] **No Fallback Workarounds / 无兜底硬编码逻辑**: Confirmed the change fixes the root cause directly without silent fallbacks or fabricated defaults / 确认直击根因，未引入静默降级或伪造默认值
- [ ] **Bilingual Sync / 中英双语同步**: Updated both Chinese and English UI strings (`web/src/i18n.tsx`) and docs (`README.md` & `README_EN.md`) where applicable / 界面文案与文档已保持中英双语同步
- [ ] **ReUI Design Rules / ReUI 规范检查**: If `web/` was modified, verified compliance with `AGENTS.md` (semantic tokens, `gap-*` spacing, empty/loading/error states) / 前端修改已符合 `AGENTS.md` 中的 ReUI 设计规范
- [ ] **Security & Credentials / 安全脱敏**: No real API keys (`sk-ag-*`), OAuth tokens, or `data/` files are included / 未包含任何真实密钥或凭据文件
- [ ] **License / 开源协议**: I agree that my contribution is licensed under the [GNU GPL v3.0](./LICENSE) / 我同意所提交的代码按本项目 [GNU GPL v3.0](./LICENSE) 协议开源授权
