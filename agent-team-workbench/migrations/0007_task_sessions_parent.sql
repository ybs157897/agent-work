-- 0007_task_sessions_parent.sql — M2 task_sessions 树形化（SQLite）。

ALTER TABLE task_sessions ADD COLUMN parent_anchor_id TEXT;
