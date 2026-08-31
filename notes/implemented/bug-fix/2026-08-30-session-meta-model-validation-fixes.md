# 会话元模型真实验收修复

Status: implemented

Storage backend note: superseded by [SQLite 单一存储后端](../simplification/2026-08-31-sqlite-only-storage.md)。

## 决策与理由

派发汇总是控制平面的必达收口，不再借用可选 heartbeat 作为准入门槛。lead-only
批次在 lead 终态后直接收口；只有存在 worker 的批次才排队生成 settlement run。该内部
automation 唤醒不受 heartbeat 间隔与普通活跃 Run 合并影响：遇到同任务活跃 Run 时保持
queued，待锁释放后重试，禁止把必达汇总标成 coalesced 后永久丢失。

worker 回流材料读取每个 worker 最后一个 assistant `message.completed` 正文；只有 Runtime
确实没有完成正文时，才用带明确“无结果正文”标记的 instruction 摘要兜底。真实 Runner 的
`RecordArtifact` 与 mock `artifact.created` 走同一搜索索引投影。中文查询在 FTS token 匹配
之外走受 workspace/work-item/kind 限定的子串检索，保证常见中文局部词可发现。

前端消息身份以 Run 的 `agent_profile_id` 为准，不再用当前侧栏 Agent 覆盖整段 transcript。
Agent 会话列表合并“任务指派”与该 Agent 的 task session 参与线；停用 Agent 不进入
`@mention` 候选。详情子请求失败显示 `role=alert` 的就地错误和重试入口，不再伪装为空态或
无限加载。

## 放弃了什么

- 不默认开启全局 heartbeat：这会把“可靠收口”与周期自主运行捆绑，并给所有已指派任务
  额外制造 timer Run。
- 不让 lead-only 批次再自我汇总：小事直接回答不需要第二轮模型调用。
- 不继续用 worker instruction 冒充结果：任务描述不是交付证据。
- 当时不让两种数据库的检索语义分叉；SQLite-only 收口后保留中文子串 fallback，
  ASCII/结构化 token 继续走 FTS5。
- 不通过改任务 assignee 表达 `@mention` 参与关系：指派与参与是两种事实，目标 Agent 的
  会话入口从 task session 补齐。

## 复活条件

当独立的 durable job/automation policy 模型能声明必达等级、重试预算和冲突策略时，可把
settlement 特判迁移到统一任务队列；迁移前必须保留“不会 coalesced 丢失、不会依赖 heartbeat”
两条负向保证。
