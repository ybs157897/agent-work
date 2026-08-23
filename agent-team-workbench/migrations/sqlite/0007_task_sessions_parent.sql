-- 0007_task_sessions_parent_sqlite.sql — M2 task_sessions 树形化（SQLite 本地验证版）。
-- 与 migrations/0007_task_sessions_parent.sql 语义等价（双方言同构 ADD COLUMN）。

ALTER TABLE task_sessions ADD COLUMN parent_anchor_id TEXT;
