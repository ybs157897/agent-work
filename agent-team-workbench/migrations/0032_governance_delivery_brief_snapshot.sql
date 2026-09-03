-- 0032_governance_delivery_brief_snapshot.sql — immutable Delivery Brief
-- captures for governance evidence.
--
-- DeliveryBrief itself remains a deterministic read model. This table stores
-- the exact canonical DTO and the source watermarks that produced it so a
-- later finish gate can prove the evidence was not silently read from a
-- different state.

CREATE TABLE governance_delivery_brief_snapshots (
    id               TEXT NOT NULL PRIMARY KEY
                     CHECK (substr(id, 1, 6) = 'brief_' AND length(id) > 6),
    schema_version   TEXT NOT NULL DEFAULT 'delivery-brief-snapshot/v1'
                     CHECK (schema_version = 'delivery-brief-snapshot/v1'),
    goal_id          TEXT NOT NULL REFERENCES goals(id),
    todo_id          TEXT NOT NULL,
    work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    snapshot_json    TEXT NOT NULL
                     CHECK (json_valid(snapshot_json) = 1
                        AND json_type(snapshot_json) = 'object'
                        AND json_type(snapshot_json, '$.generated_at') IS NULL
                        AND json_type(snapshot_json, '$.freshness.generated_at') IS NULL),
    canonical_digest TEXT NOT NULL
                     CHECK (length(canonical_digest) = 71
                        AND substr(canonical_digest, 1, 7) = 'sha256:'
                        AND substr(canonical_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    as_of_event_seq  INTEGER NOT NULL
                     CHECK (typeof(as_of_event_seq) = 'integer' AND as_of_event_seq >= 0),
    source_versions  TEXT NOT NULL
                     CHECK (json_valid(source_versions) = 1
                        AND json_type(source_versions) = 'object'),
    freshness_state   TEXT NOT NULL CHECK (freshness_state IN ('current','partial')),
    created_at        DATETIME NOT NULL,
    client_key        TEXT,
    FOREIGN KEY (goal_id, todo_id) REFERENCES goal_todos(goal_id, id),
    CHECK (length(trim(client_key)) BETWEEN 1 AND 256 OR client_key IS NULL),
    CHECK (client_key IS NULL OR client_key = trim(client_key))
);

CREATE INDEX idx_governance_delivery_brief_snapshots_goal
    ON governance_delivery_brief_snapshots(goal_id, todo_id, created_at, id);

CREATE UNIQUE INDEX idx_governance_delivery_brief_snapshots_client_key
    ON governance_delivery_brief_snapshots(goal_id, todo_id, client_key)
    WHERE client_key IS NOT NULL;

-- A capture may reference the Goal root or a task descendant, never an
-- unrelated same-workspace WorkItem. This mirrors the application tree gate
-- and keeps direct SQL writes fail closed as well.
CREATE TRIGGER governance_delivery_brief_snapshot_scope_insert
BEFORE INSERT ON governance_delivery_brief_snapshots
WHEN NOT EXISTS (
    WITH RECURSIVE subtree(id) AS (
        SELECT g.root_work_item_id FROM goals g WHERE g.id = NEW.goal_id
        UNION
        SELECT child.id FROM work_items child JOIN subtree parent ON parent.id = child.parent_id
        WHERE child.record_kind = 'task'
    )
    SELECT 1
      FROM goals g
      JOIN goal_todos t ON t.goal_id = g.id AND t.id = NEW.todo_id
      JOIN work_items wi ON wi.id = NEW.work_item_id
      JOIN subtree item ON item.id = wi.id
     WHERE g.id = NEW.goal_id
       AND wi.workspace_id = g.workspace_id
       AND wi.record_kind = 'task'
)
BEGIN
    SELECT RAISE(ABORT, 'delivery brief snapshot Goal/Todo/WorkItem scope mismatch');
END;

-- No lifecycle mutation is meaningful for an evidence capture. Keep a
-- no-op UPDATE legal for SQLite tooling that probes rows, but reject every
-- actual identity/content change and all deletes.
CREATE TRIGGER governance_delivery_brief_snapshot_identity_immutable
BEFORE UPDATE ON governance_delivery_brief_snapshots
WHEN NOT (
       NEW.id IS OLD.id
   AND NEW.schema_version IS OLD.schema_version
   AND NEW.goal_id IS OLD.goal_id
   AND NEW.todo_id IS OLD.todo_id
   AND NEW.work_item_id IS OLD.work_item_id
   AND NEW.snapshot_json IS OLD.snapshot_json
   AND NEW.canonical_digest IS OLD.canonical_digest
   AND NEW.as_of_event_seq IS OLD.as_of_event_seq
   AND NEW.source_versions IS OLD.source_versions
   AND NEW.freshness_state IS OLD.freshness_state
   AND NEW.created_at IS OLD.created_at
   AND NEW.client_key IS OLD.client_key
)
BEGIN
    SELECT RAISE(ABORT, 'delivery brief snapshot identity/content is immutable');
END;

CREATE TRIGGER governance_delivery_brief_snapshot_append_only_delete
BEFORE DELETE ON governance_delivery_brief_snapshots
BEGIN
    SELECT RAISE(ABORT, 'delivery brief snapshots are append-only');
END;
