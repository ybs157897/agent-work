# Goal pause、resume、cancel 控制真实执行线

Status: implemented

## 决策与理由

Goal 生命周期命令必须控制同一根任务的实际 Coordinator，而不是只改 Goal/Todo 两行投影。Pause 保留已有 Run 与 wake 的身份，但在 PlanDecision、verdict、terminal hook、due scan、approval 和 wake 创建边界统一拒绝继续推进；必达 settlement wake 保持 queued。Resume 只允许 `waiting → active`：活动或已终态的原控制轮按同一身份继续，observation checkpoint 才 durable queued；blocked Goal 必须经 WorkItem Unblock 恢复，不能绕过四层 blocker。

Cancel 在同一 SQLite 事务取消 Goal、全部非终态 Todo、Coordinator、根任务树和活动 Plan，释放 claim、跳过 pending step、过期 `plan_dispatch` 审批并以 `cancelled` 关闭 dispatch；全部非终态 Run 同事务前转（尚未起跑的 Run 直达 `cancelled`，已起跑的 Run 进入 `cancelling`，已有 `interrupting/cancelling` 保留合法中间态），提交后只向精确 Run 做匹配的无状态 adapter/Runner control forward。已 admission 的 quota 用 Coordinator 上的 `settle_cancelled_goal` durable checkpoint 重放，只有 active reservation 清零后才清除，因此进程在取消提交与结算之间退出也不会永久遗留 reserved。取消提交后的清理失败由 HTTP 返回可缓存的 202，而不是释放幂等占位让同一命令二次执行。

Scheduler 在 heartbeat claim、活跃 Run coalesce 和 steering forward 之前执行 Goal lifecycle preflight；waiting Goal 的 wake 保持 queued，terminal Goal 的 wake 才消费为 no-op。CreateRunForWakeup 与 Coordinator wake 写边界仍做第二次检查，防止 pause 与调度并发时穿透。

Pause/Resume 遇到已转移的 Handoff 时保留目标 Agent 的 ownership generation：过期但未被替换的 claim 只做 same-generation `RenewClaim`，不走 Release+Claim；claim 已被释放或其他 Agent 接管则 fail closed，不能静默回到系统 Coordinator。

`session_unknown` self-heal 也受同一生命周期围栏：同一事务重读 source Run/WorkItem/root Goal/Coordinator，只在执行中且 Goal active 时写锚点墓碑、以 `session-heal:<source_run_id>` 创建唯一 Run，并用 queued→starting 作 durable dispatch claim。启动恢复先重投已提交的 queued/starting self-heal，再执行通用 orphan 收敛；paused Goal 保留该 Run 到 Resume，blocked/cancelled 不会越过围栏分派。

## 放弃了什么

- 只依赖 `StartCoordinator` 看到 Goal 非 active 后 no-op：due scan 会空转，已经运行的 decision/verdict 和在 active-run coalesce 之前的直接 wake steering 仍可穿透。
- Pause 直接取消 Runner lease 或 Provider session：Goal waiting 是可恢复治理暂停，不等于销毁执行会话；现有 Run 输出保留到 Resume 后按原身份重放。
- Resume blocked Goal：会让 Goal active 而 WorkItem/Coordinator 仍 blocked，制造第二条恢复权威；统一要求 WorkItem Unblock。
- Cancel 后 best-effort 做一次 quota sweep：崩溃窗口会留下永久 reservation；改用 cancelled Coordinator 自身作为持久重放 checkpoint。
- self-heal 先单独清锚点、再创建 Run：中间崩溃会丢失会话但没有恢复 Run；两者改为同事务。
- 把 commit 后未 dispatch 的 self-heal 当通用 orphan 直接标 lost：会永久丢失自动恢复；改为 deterministic identity + durable dispatch claim + 启动/Resume 恢复。

## 复活条件

只有未来引入独立、持久且覆盖 UI/API/调度器的 Coordinator `paused` 状态，并完成旧 waiting 语义的数据迁移时，才可把 Pause 投影从 Goal 状态门控改为 Coordinator 状态机；仍不得把 Pause 等同于取消 lease。若取消语义改为“只取消 Goal、不取消 Task/Run”，必须先修改终态一致性合同并定义任务重新取得控制权的唯一命令，否则不得恢复该分裂行为。
