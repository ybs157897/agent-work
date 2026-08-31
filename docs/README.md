# Agent Team Workbench 文档

`docs/` 保存面向维护者的产品目标、架构设计、协议说明、前端规格和外部研究资料。运行时知识语料仍位于 `agent-team-workbench/knowledge/`；文档重组不会改变检索入口或应用启动路径。

视觉与交互的事实源是 [`agent-team-workbench/web/DESIGN.md`](../agent-team-workbench/web/DESIGN.md)。跨项目决策留痕统一位于 [`notes/`](../notes/)。

## 活跃文档

### 产品

- [`product/end-goal.md`](product/end-goal.md)：最终产品目标、系统 Task Coordinator、会话管理与知识层的收口愿景。
- [`product/product-agent-charter.md`](product/product-agent-charter.md)：产品 Agent 的澄清、立法和交付章程。

### 架构

- [`architecture/c4-container-diagram.md`](architecture/c4-container-diagram.md)：当前容器边界、数据流和跨容器契约。
- [`architecture/task-control-surface-context-design.md`](architecture/task-control-surface-context-design.md)：Execution Host、Workspace Location、Execution Context Snapshot、Task Comment、Review Queue 与 Delivery Brief。
- [`architecture/clawteam-borrowings-design.md`](architecture/clawteam-borrowings-design.md)：ClawTeam/OpenClaw 借鉴设计及已采纳、挂起和否决项。
- [`../notes/implemented/simplification/2026-08-31-sqlite-only-storage.md`](../notes/implemented/simplification/2026-08-31-sqlite-only-storage.md)：SQLite 单一存储、唯一迁移目录与 PostgreSQL 复活条件。
- [`../notes/implemented/architecture/2026-08-30-task-control-surface-completion-plan.md`](../notes/implemented/architecture/2026-08-30-task-control-surface-completion-plan.md)：任务控制面实施边界、失败停手条件和验收矩阵。

### 协议

- [`protocol/mcp-tools.md`](protocol/mcp-tools.md)：atw-mcp 工具面与安全边界。
- [`protocol/codex-app-server-v2.md`](protocol/codex-app-server-v2.md)：当前 Codex app-server 版本的人工可读协议基线。

机器可执行契约仍保留在 `agent-team-workbench/contracts/`，包括 OpenAPI、AsyncAPI 与 Runner v2 schema。

### 前端

- [`frontend/chat-content-blocks-v1.md`](frontend/chat-content-blocks-v1.md)：`languagegui/v1` 内容块契约。
- [`frontend/chat-rendering-spec.md`](frontend/chat-rendering-spec.md)：Transcript、AgentOutput、工具活动、子代理与 swarm 正文渲染规格。

## 外部参考

[`references/`](references/) 中的 Codex、Kimi、ZCode 与 ClawTeam 资料描述外部系统或版本锁定的逆向结果，不是本项目当前实现的事实源。提示词原文保存在 [`archive/prompt-library/`](archive/prompt-library/)；项目内的对照分析见 [`references/cli-prompt-engineering.md`](references/cli-prompt-engineering.md)。

## 归档

- [`archive/prototype/`](archive/prototype/)：原型包、早期协议、产品简报、信息架构和线框原件。
- [`archive/design/`](archive/design/)：历史设计审计、旧版前端重设计方案和 swarm 演示稿。
- [`archive/prompt-library/`](archive/prompt-library/)：外部或提取的原始提示词证据，保留原文，不按本项目路径变更改写。
- `archive/reviews/`：按日期冻结的项目评审快照。
- `archive/security/`：历史安全扫描快照；当前处置状态以 `notes/implemented/bug-fix/` 和最新扫描为准。

归档材料由原位置迁入上述目录，原路径不再保留。归档只用于追溯，不作为现行产品、架构或安全结论。
