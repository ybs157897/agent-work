-- 0014_task_execution_lock_sqlite.sql — 任务级执行锁（SQLite 本地验证版）。
-- 与 migrations/0014_task_execution_lock.sql 语义等价：work_items 增加
-- locked_by_run_id/locked_at 执行锁列；锁归属 run（活性复用 run 状态面）。

ALTER TABLE work_items ADD COLUMN locked_by_run_id TEXT REFERENCES execution_runs(id);
ALTER TABLE work_items ADD COLUMN locked_at DATETIME;
