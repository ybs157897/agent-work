# F1 任务级执行锁：获取/释放/死属主抢占

Status: implemented

设计依据：[`docs/architecture/clawteam-borrowings-design.md`](../../../docs/architecture/clawteam-borrowings-design.md) F1 节。
锁归属 **run**（非 agent）：属主活性复用 run 状态/lease 面（终态=死），不引入第二套
活性判定。迁移 0014（双目录语义等价）：`work_items.locked_by_run_id/locked_at`。

## 决策与理由

- **获取点 = transitionRunLocked 的 queued/starting → running 分支**（同一事务）：
  无锁获取（发 `work_item.locked`）；本 run 持有幂等通过（覆盖 waiting_approval/
  reconnecting 往返后的再推进——这些 from 不重复获取，锁早已在手）；属主 run 已终态
  → 抢占覆写 + activity「执行锁已被抢占（原 run 已终态）」+ `work_item.lock_preempted`；
  属主 run 行缺失按死锁处理（残留引用不得永久卡死任务）。
- **被活锁拒绝的双跑 run 直接落 failed(code=work_item_locked, retryable=true)**，不走
  「报错卡 starting」：RecordRunStatus 的错误路径会回滚事务留下非终态半 run，违反
  「任何 Outcome 必须能落终态」红线。落终态由 transitionRunLocked 递归完成（from 仍是
  queued/starting，failed 迁移合法），失败原因进 run_events 供 UI 展示。
- **释放点 = 同函数 to.IsTerminal() 分支**：锁仍归本 run 才清空；已被抢占/回收的锁
  不误伤（属主判定 HoldsLock(runID)）。
- **锁字段不参与 version 乐观锁比较**（设计文档既有取舍），但读写与状态变更同事务；
  Update 语句 SET 清单带锁列，读-改-写同事务内 expected=读取版本，SQLite 写串行 +
  PG 行锁保证不会「版本过了但锁丢了」。
- **ClaimWorkItem 不改语义**：带锁任务必为 in_progress（建 run 即推进状态），天然不
  满足「todo 且无 assignee」前置；死属主锁的抢占发生在下一个 run 的起跑获取点，claim
  不做 in_progress → todo 自动重置——状态回流留给人工/编排路径。
- **回收兜底挂 Scheduler.Tick**：独立 5 分钟扫描周期（staleLockSweepInterval），清
  `locked_at` 超 30 分钟（staleLockAge）且属主 run 已终态的锁
  （WorkItemRepo.ReleaseStaleLocks 一把 UPDATE；正常路径终态事务内已释放，只兜异常
  残留）。端口独立成 scheduling.StaleLockReleaser，不并入唤醒仓储接口。

## 放弃了什么

- **拒锁时回 409 给调用方**：runnergateway/ModuleRunner 的状态上报面没有冲突重试
  语义，报错只会留半态；落 failed 终态 + retryable=true 让既有重试/人工路径接管。
- **waiting_approval 锁持有期间允许新 run 起跑**（审批等待视为「不活跃」）：审批中的
  run 仍是任务的执行主体（决议后要继续跑），放开会复活双跑；用户可先取消/决议。
- **`--force` 人工破拆入口**（对齐设计文档）：抢占判定已覆盖死属主；人工面走 admin
  改状态。

## 负向保证

- 属主 run 活跃（非终态）的锁无论多旧不被 ReleaseStaleLocks 回收；lease 过期面
  （runnergateway sweeper → lost 终态）收敛后锁随之在终态事务释放或被回收扫描清掉。
- workItemDTO 无锁时 locked_by_run_id/locked_at omitted（不出现空键）。
