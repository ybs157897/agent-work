# wakeup 消费用 CAS 占位 + 补偿，不与 CreateRun 同事务

Status: implemented

## 决策与理由

wakeup 消费链为「先 CAS 占住（`UPDATE ... WHERE status='queued'`）→ 建 run → 失败则 RequeueWakeup 回退 + ReleaseHeartbeatClaim 带值比对回滚」。并发消费由 CAS 保证单胜者；崩溃窗口（占位后、建 run 前进程死）由 timer 源下一周期自动补产兜住。

依据：单进程调度循环 + CAS 已消除实际双跑路径；补偿路径（requeue/release）覆盖了可恢复失败，仅剩硬崩溃窗口。

## 放弃了什么

- **wakeup 消费与 CreateRun 同一事务**（Paperclip 原版语义）：需要把 application.CreateRun 拆成可传入外部事务的形态，重构面大；当前用 CAS+补偿换取了不等价但够用的原子性。
- **per-agent 事务内行锁串行**：多实例部署才需要；单进程由调度循环单 goroutine 天然串行。

## 复活条件

多 control-plane 实例部署提上日程 → 返工点：`internal/scheduling/loop.go` 消费路径 + `application.CreateRun` 事务边界重构；预埋要求：`MarkWakeupStatus` 已是 CAS 语义、`ReleaseHeartbeatClaim` 带值比对，多实例下只需补行锁与同事务，占位逻辑可直接复用。
