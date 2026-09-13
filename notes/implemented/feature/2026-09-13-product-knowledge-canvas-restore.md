# 产品知识画布：在新资料库 API 上恢复（产品专属、只读）

Status: implemented

## 决策与理由

旧画布（`9b93639` 引入）随知识系统重建在 `92a9b12` 被成建制删除——它的数据源（旧 per-item
knowledge API 与 knowledge_items 表）已不存在。本次按用户要求恢复，骨架从 `92a9b12~1` 取回，
数据层全部重接新资料库：列表/详情/版本走 `listDocuments/getDocument`（`?version=N` 为客户端
透传，后端已支持），全景走 `getGraph`。画布是**只读**阅读/全景工作区——新架构下普通 Agent
不写知识，写库仍是资料库管理员独占通道。

**产品专属**：`role=pm` 且启用中的普通成员聊天默认进入「左画布右对话」；非 pm（含系统身份、
停用成员）没有任何入口。旧的 localStorage `canvasPreference` 开关机制（含 `canReadAgentKnowledge`
的 owner/admin 特赦）整套删除，不迁移——"专属于产品"就是字面意思。

## 放弃了什么

- **按 Agent owner 过滤的文档列表**：新资料库是工作空间级发布物，没有 owner 概念。画布展示
  当前发布 release 的工作空间文档；要回 per-agent 私有视角等于重建旧知识体系，不做。
- **画布内知识编辑**：旧体系的写入口一律不恢复，与「普通 Agent 不直接改共享有效知识」一致。
- **`canvas=knowledge` URL 参数**：无开关就无需写 URL；pm 判定由 profile 事实驱动
  （`role==='pm' && enabled && isUserManagedAgent`），不靠客户端状态。
- **knowledge-chat-handoff**：旧知识页 → 管理员讨论的交接是另一个特性，不随画布复活。
- **全景补占位节点/猜测边**：只画两端都落在已渲染节点的服务端事实边，端点缺失就丢弃——
  半张图比明确的缺口更危险（与知识层 coverage 纪律同源）。

## 复活条件

- 需要 URL 可分享的画布深链时：在 chat 路由加 `canvas=knowledge` 解析（与 `canvas=code` 同构），
  写入侧仍不做开关。
- 产品以外的角色也需要知识阅读面时：先回答"为什么 /library 管理页不够用"，再谈把 pm 判定
  泛化为角色表——不提前抽象。

## 验证

2026-09-13 ego 隔离实例（`:8090`，主库快照 + 星轨罗盘语料 release，3 文档 2 实体 1 关系）：
pm 默认进画布且「产品智能体 的知识 · 已发布内容 · 只读画布」；开发智能体无画布无入口；
文档列表/阅读（frontmatter 已剥离）；全景 React Flow 5 节点 1 条事实边（星轨罗盘 —依赖→ 极光钟摆）；
选区「引用这段」→ composer 可移除 chip → 真实 run 发送 → 回答引用 42 小时事实，
消息渲染为引用卡片、`<atw-knowledge-reference-v1>` 标记不外泄。
