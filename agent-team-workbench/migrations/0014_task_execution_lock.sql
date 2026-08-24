-- 0014_task_execution_lock.sql — 任务级执行锁（F1，对齐 ClawTeam locked_by/locked_at）：
-- 一个活跃 run 持有其所属 work item 的执行锁，防止同一任务双跑；属主 run 死亡
--（终态/lost）后锁可被下一个 run 抢占或由调度循环兜底回收。锁归属 run 而非
-- agent：属主活性复用 run 状态/lease 判定面，不引入第二套活性判定。

ALTER TABLE work_items ADD COLUMN locked_by_run_id TEXT REFERENCES execution_runs(id);
ALTER TABLE work_items ADD COLUMN locked_at TIMESTAMPTZ;
