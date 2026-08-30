-- 0019_record_kind.sql — Chat / Task 记录边界。
-- 新记录默认按 Chat 处理，避免把已有单 Agent 对话误纳入任务台账；只有有
-- 计划根、计划步骤产物或确定的非 fork 任务父子证据时才回填 Task。

ALTER TABLE work_items
    ADD COLUMN record_kind TEXT NOT NULL DEFAULT 'chat';

-- Plan 根和 dispatch 步骤产出的 WorkItem 是任务事实；其下没有 fork 标记的
-- 后代沿任务树继承 Task。fork 会话同时带 client_key= fork:* 和上下文标记，
-- 必须留在 Chat，不从标题/优先级推断。
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

ALTER TABLE work_items
    ADD CONSTRAINT work_items_record_kind_check
    CHECK (record_kind IN ('chat', 'task'));

CREATE INDEX idx_work_items_ws_record_kind
    ON work_items(workspace_id, record_kind, created_at DESC, id DESC);

-- record_kind 是创建时的不可变聚合边界；父子记录也必须同类。
CREATE OR REPLACE FUNCTION validate_work_item_record_kind()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.record_kind IS DISTINCT FROM OLD.record_kind THEN
        RAISE EXCEPTION 'work_items.record_kind is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER work_items_record_kind_immutable
BEFORE UPDATE OF record_kind ON work_items
FOR EACH ROW EXECUTE FUNCTION validate_work_item_record_kind();

CREATE OR REPLACE FUNCTION validate_work_item_parent_record_kind()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.parent_id IS NOT NULL AND EXISTS (
        SELECT 1
        FROM work_items parent
        WHERE parent.id = NEW.parent_id
          AND parent.record_kind <> NEW.record_kind
    ) THEN
        RAISE EXCEPTION 'work_items parent and child record_kind must match';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER work_items_parent_record_kind_check
BEFORE INSERT OR UPDATE OF parent_id, record_kind ON work_items
FOR EACH ROW EXECUTE FUNCTION validate_work_item_parent_record_kind();
