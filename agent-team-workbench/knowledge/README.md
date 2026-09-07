# 知识层（knowledge/）

知识层的 Markdown 表达约定：本目录保存可导入/导出的领域正文，与 agents/<slug>/prompt.md（角色指令真相源）分层——prompt.md 定义「你是谁、怎么干活」，知识库运行时状态由 workbench SQLite 保存。

## 目录

- `prd/` — 产品法典（PRD 条目）落点。

## 条目规范

条目模板、编号规则、规范用词与修订流程以 [`docs/product/product-agent-charter.md`](../../docs/product/product-agent-charter.md) §3（立法阶段：法典化）为唯一依据，此处不重复定义。

## 运行时约定

- SQLite 是知识条目、版本、来源、关系、候选提交和调查作业的唯一有效状态入口；发布后的检索投影可重建，不拥有独立知识状态。
- Markdown 文件用于显式导入/导出和正文表达；直接改文件不会绕过候选、证据、版本和发布门。
- Agent 通过知识管理员 Harness 或 workspace HTTP API 查询和提交；未授权源码/测试不自动进入检索范围。
- `prd/`、未来的 `arch/` 和 `dev-std/` 是按领域组织的 Markdown 交换目录，条目规范仍以各自法典章程为准。
