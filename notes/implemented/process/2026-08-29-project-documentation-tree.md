# 项目文档树统一到仓库根

Status: implemented

## 决策与理由

人读项目文档统一放在仓库根 `docs/`，跨项目决策留痕统一放在仓库根 `notes/`。原 `agent-team-workbench-docs/` 按产品、架构、协议、前端、参考与归档分类；原 `agent-team-workbench/notes/` 全量上移，并包含 Task Coordinator、任务控制面、Session 模型与 Runner v2 的最新实施留痕。人读的 Codex app-server 协议说明从机器契约目录移到 `docs/protocol/`，OpenAPI、AsyncAPI 与 JSON Schema 继续留在 `agent-team-workbench/contracts/`。

迁移以 `main@81f88fb` 为事实基线重新执行，不合并落后 40 个提交的旧 `docs/reorg-project-docs` 工作树，避免用旧 README、End Goal、C4、Chat Rendering 或 Swarm 文档覆盖已完成的 Task Control Surface 实现。

`agent-team-workbench/knowledge/` 保持原位。它是运行时 `KnowledgeRetriever` 的语料入口；根 `docs/` 只服务维护者，不参与默认运行时检索。

## 放弃了什么

- 直接合并旧重组分支：该分支没有主线之外的提交，独有内容全部是基于旧树的未提交改动。
- 继续保留两个项目文档根：会让架构事实、协议说明和决策记录长期分散，也违背根 `AGENTS.md` 对 `notes/` 的约定。
- 把外部提示词原文当作活跃项目文档：这些文件保留在 `docs/archive/prompt-library/` 作为证据，但不随项目路径重写。
- 把旧安全发现台账当作当前待办：其快照保留归档，当前处置状态以实施 note 和最新扫描为准。
- 保留 Obsidian 个人便携约定：它不是仓库能力，也没有运行时或产品实现。

## 负向保证

- 不把 `docs/` 并入 `knowledge/`，不改变 `ATW_KNOWLEDGE_ROOT` 或默认工作目录语义。
- 不移动机器可执行契约、Agent prompt、`web/DESIGN.md` 或运行时配置。
- 不自动纳入主工作树中其他会话留下的未跟踪可视化、模型配置、数据库或工具文件。
