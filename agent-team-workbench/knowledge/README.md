# 知识层（knowledge/）

知识层 bootstrap 约定：本目录存放 agent 可读的持久化领域知识（「法典」），与 agents/<slug>/prompt.md（角色指令真相源）分层——prompt.md 定义「你是谁、怎么干活」，knowledge/ 沉淀「大家共同遵守的产品事实」。

## 目录

- `prd/` — 产品法典（PRD 条目）落点。

## 条目规范

条目模板、编号规则、规范用词与修订流程以 `agent-team-workbench-docs/product-agent-charter.md` §3（立法阶段：法典化）为唯一依据，此处不重复定义。

## MVP 期约定

- 无检索器、无 frontmatter 校验器：agent 经 CLI 文件读写直接落 markdown，人工 review 兜底。
- 部署时 WorkspaceRoot 应指向包含 knowledge/ 的执行根目录，保证 agent 工作区内可见。
- 后续 architecture / dev-std 法典就位时，在本目录加同级 `arch/`、`dev-std/`，不另立根。
