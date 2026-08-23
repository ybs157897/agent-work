-- 0011_activity_work_item.sql — M4 追加：activity 归因。verdict 处理与
-- blocker 落库的 activity 携带 work_item_id（activities 行与 activity.appended
-- 事件 data 同步）；runner 级 activity 与历史行保持 NULL（无归因）。

ALTER TABLE activities ADD COLUMN work_item_id TEXT;
