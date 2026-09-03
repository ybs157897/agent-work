# Delivery Brief snapshot 作为验收证据

Status: implemented

## 决策与理由

Delivery Brief 仍由服务端确定性聚合生成；需要进入 Goal 完成证据的版本通过 `governance_delivery_brief_snapshots` 追加一条不可变记录。记录保存 `delivery-brief-snapshot/v1` 闭合 canonical DTO、Goal/Todo/WorkItem 绑定、source versions、Workspace event watermark 与 freshness state，并用 canonical digest 覆盖 schema version、身份、内容和水位。这样 finish gate 能验证“当时看到的事实”仍是当前权威事实，而不是信任模型文本或当前重新生成的摘要。

`generated_at` 和重复的 `freshness.as_of_event_seq` 不进入 canonical DTO：前者是每次读取都会变化的观察时间，后者是 Workspace 全局单调观察水位；snapshot 表保留顶层水位，finish gate 只要求它不倒退，并按 source versions 与 canonical payload 判断相关事实是否变化。无关 WorkItem 的事件不会让有效证据失效。

## 放弃了什么

- 只保存 `delivery_brief` source ID 而不保存内容：无法在后续状态变化后证明验收人当时看到的证据，且会把完成 gate 退化为“重新读当前状态”。
- 把 snapshot JSON 塞入 Goal completion evidence：会突破现有 evidence source 的身份约束，也无法独立保证 append-only、重放和跨 Goal scope。
- 只比较 Workspace `MAX(stream_seq)` 是否相等：该水位包含无关任务事件，会把有效证据误判为 stale；现在只要求单调不倒退，并同时比较来源版本和闭合 payload。

## 复活条件

若 Delivery Brief DTO 增加字段，必须先更新 canonical snapshot DTO、digest 版本/迁移与 finish-gate 测试；不得让未纳入 canonical digest 的字段进入 passed evidence。
