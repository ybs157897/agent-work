# Coordinator blocker 与原生治理状态原子同步

Status: implemented

## 决策与理由

根控制线进入 blocker 时，WorkItem、TaskCoordinator、Goal、当前 Todo 与事件/outbox 必须在同一 SQLite 事务内一起提交：Goal 的 `active|waiting` 进入 `blocked`，非终态 Todo 进入 `blocked`，活动治理 claim 同事务释放并推进 claim generation。显式 Unblock 做反向恢复：Goal 回 `active/execution`、Todo 回 `pending`，但保留 `last_turn_seq`，不复用旧 claim。

PlanDecision repair 耗尽、Runtime/Quota/Worker 致命失败、Plan 缺失、Evidence 不足、人工 Block 与预算 blocker 全部复用这一语义。系统 Coordinator 在 Goal/Todo 缺失且无法从根任务验收合同重建时 fail closed；不得退回普通 `SubmitPlan`。根 blocker 同时关闭该 root 的全部开放 dispatch，旧 Plan 被 supersede 时同事务过期其 pending `plan_dispatch` 审批。

该同步只表达控制线生命周期，不制造不存在的治理工作：block/unblock 永不创建 TurnReceipt，也不增加 `turn_seq`。重复 blocker、迟到审批、迟到 Worker 终态和 Idempotency-Key 重放均不得重复发布状态事件或复活已关闭 dispatch。

## 放弃了什么

- 只在 GovernancePanel 根据 Coordinator 状态推导 Goal/Todo 展示：会掩盖数据库真相分裂，API/MCP 与重启恢复仍不一致。
- blocker 时追加一个伪 Receipt 或虚构 turn_seq：repair 可能发生在 admission 之前，伪造收据会污染 quota、幂等和审计身份。
- WorkItem 提交后 best-effort 再改 Coordinator/Goal/Todo：第二次提交失败会留下用户已经实际观察到的半态。
- 只关闭失败 Run 自己的 dispatch：同一 root 的兄弟批次仍会保持 active，无法解释根控制线已经停止。
- Goal 缺失时继续旧的无治理 SubmitPlan：会重新引入已删除的双执行语义。

## 复活条件

只有未来把 Goal/Todo 从权威状态降级为纯可丢弃 projection，并同时删除 Claim、TurnReceipt、Quota 与 Evidence 对其身份的依赖时，才可重新评估展示层推导；在当前原生治理架构下不得恢复。若以后需要 blocker 本身成为一个可计费 bounded turn，必须新增显式 Decision/Receipt 合同和迁移，不能复用现有 repair failure 事件冒充。
