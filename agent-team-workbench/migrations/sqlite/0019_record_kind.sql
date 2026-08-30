-- 0019_record_kind_sqlite.sql — Chat / Task 记录边界（SQLite 本地验证版）。
-- 新记录默认按 Chat 处理；Plan 根、Plan step 结果及确定的非 fork 后代回填 Task。

ALTER TABLE work_items ADD COLUMN record_kind TEXT NOT NULL DEFAULT 'chat';

WITH RECURSIVE task_items(id) AS (
    SELECT wi.id
    FROM work_items wi
    WHERE EXISTS (
        SELECT 1 FROM plans p WHERE p.work_item_id = wi.id
    )
       OR EXISTS (
        SELECT 1 FROM plan_steps ps
        WHERE ps.result_work_item_id = wi.id
          AND ps.result_work_item_id <> ''
    )
    UNION
    SELECT child.id
    FROM work_items child
    JOIN work_items parent ON parent.id = child.parent_id
    WHERE COALESCE(child.client_key, '') NOT LIKE 'fork:%'
      AND child.description NOT LIKE '【分叉上下文】%'
      AND COALESCE(parent.client_key, '') NOT LIKE 'fork:%'
      AND parent.description NOT LIKE '【分叉上下文】%'
    UNION
    SELECT parent.id
    FROM work_items child
    JOIN work_items parent ON parent.id = child.parent_id
    WHERE COALESCE(child.client_key, '') NOT LIKE 'fork:%'
      AND child.description NOT LIKE '【分叉上下文】%'
      AND COALESCE(parent.client_key, '') NOT LIKE 'fork:%'
      AND parent.description NOT LIKE '【分叉上下文】%'
    UNION
    SELECT child.id
    FROM work_items child
    JOIN task_items parent ON parent.id = child.parent_id
    WHERE COALESCE(child.client_key, '') NOT LIKE 'fork:%'
      AND child.description NOT LIKE '【分叉上下文】%'
)
UPDATE work_items
SET record_kind = 'task'
WHERE id IN (SELECT id FROM task_items);

CREATE INDEX idx_work_items_ws_record_kind
    ON work_items(workspace_id, record_kind, created_at DESC, id DESC);

-- SQLite ALTER TABLE 无法可靠地为既有列追加跨行 CHECK；触发器同时提供
-- 闭集校验、不可变约束与父子同类约束。
CREATE TRIGGER work_items_record_kind_valid_insert
BEFORE INSERT ON work_items
WHEN NEW.record_kind NOT IN ('chat', 'task')
BEGIN
    SELECT RAISE(ABORT, 'work_items.record_kind must be chat or task');
END;

CREATE TRIGGER work_items_record_kind_immutable
BEFORE UPDATE OF record_kind ON work_items
WHEN NEW.record_kind <> OLD.record_kind
BEGIN
    SELECT RAISE(ABORT, 'work_items.record_kind is immutable');
END;

CREATE TRIGGER work_items_parent_record_kind_check
BEFORE INSERT ON work_items
WHEN NEW.parent_id IS NOT NULL
 AND EXISTS (
    SELECT 1 FROM work_items parent
    WHERE parent.id = NEW.parent_id
      AND parent.record_kind <> NEW.record_kind
 )
BEGIN
    SELECT RAISE(ABORT, 'work_items parent and child record_kind must match');
END;

CREATE TRIGGER work_items_parent_record_kind_update_check
BEFORE UPDATE OF parent_id ON work_items
WHEN NEW.parent_id IS NOT NULL
 AND EXISTS (
    SELECT 1 FROM work_items parent
    WHERE parent.id = NEW.parent_id
      AND parent.record_kind <> NEW.record_kind
 )
BEGIN
    SELECT RAISE(ABORT, 'work_items parent and child record_kind must match');
END;
