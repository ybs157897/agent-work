-- 0011_activity_work_item_sqlite.sql — activity 归因（SQLite 本地验证版）。
-- 与 migrations/0011_activity_work_item.sql 语义等价。

ALTER TABLE activities ADD COLUMN work_item_id TEXT;
