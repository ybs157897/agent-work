# Agent Team Workbench 文档资料

该目录集中保存 Agent 开发团队工作台的架构、协议、渲染规格与参考资料。
视觉与交互的**事实源**是代码仓内的 `agent-team-workbench/web/DESIGN.md`，不在本目录。

## 活跃文档（随实现演进，动手前先读对应篇）

- end-goal.md：**最终目标文档**——产品收口愿景：正交 agent 配置、agent 自管理会话（前缀缓存契约）、lead/worker 编排角色层、知识层；文末含现状对账表与 M1–M4 路线进度（M1–M4 已于 2026-08-24 全量合入 main）。
- product-agent-charter.md：**产品 Agent 章程**——澄清（需求决策树访谈）→ 立法（法典化：应当·不得·可以 / 版本链 / ID 永不复用）→ 交付三段循环 + 全局红线自检；附录含提问 / 共识摘要 / 条目 / 树况四套模板。粘贴即 system prompt。
- architecture/c4-container-diagram.md：C4 Level 2 容器图（Web / Control Plane / Runner Daemon / MCP Server / 数据库）+ 跨容器契约索引，头注钉最新核订的代码版本。
- architecture/clawteam-borrowings-design.md：ClawTeam/OpenClaw 借鉴功能设计（F1 任务锁 / F3 幂等键 / F5 MCP 已合入 main；F2 挂起；F4 砍掉）。
- protocol/mcp-tools.md：atw-mcp 暴露给 agent harness 的 MCP 工具面（9 个工具 + 红线清单）。
- chat-rendering-spec.md：对话输出渲染规格——TranscriptSegment 段层 / LeAgent 正文骨架 / 工具块层 / 决策卡层的分层契约与剩余缺口。
- frontend-design-md-redesign.md：水墨重设计的决策记录与 M1/M2 迁移账本（按 2026-08-26 代码现状核订）。
- references/design-resource-library.md：**Web 设计素材库**——外部设计资源站检索入口（AICSS/ohwow/Curated 核验条目 + 候选池），做前端设计先查这里选站再进站搜。
- references/design-asset-index.md：**素材级索引**——AICSS 14 组件全量与 Curated 分类/区块的地址、描述、适用场景明细（配套上一条的选站库单）。

## 待实现（方向已确认、尚未落地）

- [待实现/README.md](./待实现/README.md)：待办索引（当前为 Obsidian 个人便携约定）；单条正文与 `notes/proposed/` 决策 note 成对。

## 时点快照（定格留痕，不随后续实现更新）

- design-audit-2026-08-25.md：redesign-skill 设计审计问题单（通过项 / P1 / P2）。问题项已并入 frontend-design-md-redesign.md 的路线消化，落实情况以该文档与 git log 为准。

## 外部系统参考（描述的是别人的系统，不是本项目实现）

- references/codex-desktop-rendering-comparison.md：Codex 桌面端对话渲染逆向对照。
- references/codex-desktop-markdown-tags-inventory.md：Codex markdown 标签清单逆向。
- references/clawteam-openclaw-comparison.md：ClawTeam 与 OpenClaw 架构对照（F1–F5 借鉴来源）。
- references/codex-appserver-official-protocol-reference.md：**codex app-server 官方协议锚点**——权威入口链接、schema 生成与版本钉死、生命周期/方法面/通知面/审批面速查、稳定性承诺与 vendored CLI 升级同步规程。
- references/cli-prompt-engineering.md：**Codex / Kimi Code 提示词工程对照**——输出形态纪律条款对照表、提示词堆叠两种范式（消息层 vs 模板槽位）、我们三条 adapter 路径的提示词位置实测结论、languagegui/v1 契约补丁设计建议；原文资产在 references/prompt-library/。

## 原型期归档材料（历史原件，现状以各 md 为准）

- architecture/runtime-agnostic-agent-team-workbench-system-design.docx：早期总体系统设计。
- protocol/agent-team-dashboard-web-protocol-go.docx：原型期的 Web 协议对接规范；现行权威契约是 `agent-team-workbench/contracts/`（openapi.yaml / asyncapi.yaml / runner v1 schema.json 等）。
- references/agent-team-dashboard-prototype.zip：用户提供的完整原型包。
- references/product-brief.md：原型产品简报。
- references/confirmed-ia.json：原型信息架构与视觉方向（IA 冻结依据）。
- references/wireframe.jpg：原型线框图。

归档采用复制方式，原始文件仍保留在原位置。
