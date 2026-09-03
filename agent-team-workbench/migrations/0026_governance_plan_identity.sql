-- 0026_governance_plan_identity.sql — governance Todo→Plan identity.
--
-- Existing API/legacy plans keep all seven governance columns NULL.  A Plan
-- compiled from a bounded turn carries the complete identity and decision
-- digests; there is no third partially populated representation.

ALTER TABLE plans ADD COLUMN client_key TEXT;
ALTER TABLE plans ADD COLUMN goal_id TEXT REFERENCES goals(id);
ALTER TABLE plans ADD COLUMN todo_id TEXT REFERENCES goal_todos(id);
ALTER TABLE plans ADD COLUMN turn_seq INTEGER;
ALTER TABLE plans ADD COLUMN decision_schema_version TEXT;
ALTER TABLE plans ADD COLUMN decision_schema_digest TEXT;
ALTER TABLE plans ADD COLUMN decision_digest TEXT;

CREATE UNIQUE INDEX idx_plans_ws_client_key
    ON plans(workspace_id, client_key)
    WHERE client_key IS NOT NULL;

CREATE UNIQUE INDEX idx_plans_governance_turn_identity
    ON plans(goal_id, todo_id, turn_seq)
    WHERE goal_id IS NOT NULL
      AND todo_id IS NOT NULL
      AND turn_seq IS NOT NULL;

-- SQLite has no portable regular-expression CHECK.  The suffix NOT GLOB
-- predicate rejects every non-hex character while length/prefix fixes the
-- exact sha256:<64 lowercase hex> shape used by the domain digest contract.
CREATE TRIGGER plans_governance_identity_insert
BEFORE INSERT ON plans
WHEN NOT (
       (NEW.client_key IS NULL
        AND NEW.goal_id IS NULL
        AND NEW.todo_id IS NULL
        AND NEW.turn_seq IS NULL
        AND NEW.decision_schema_version IS NULL
        AND NEW.decision_schema_digest IS NULL
        AND NEW.decision_digest IS NULL)
    OR (NEW.client_key IS NOT NULL
        AND NEW.goal_id IS NOT NULL
        AND NEW.todo_id IS NOT NULL
        AND NEW.turn_seq IS NOT NULL
        AND NEW.decision_schema_version IS NOT NULL
        AND NEW.decision_schema_digest IS NOT NULL
        AND NEW.decision_digest IS NOT NULL
        AND length(trim(NEW.client_key)) BETWEEN 1 AND 256
        AND length(trim(NEW.goal_id)) BETWEEN 1 AND 256
        AND length(trim(NEW.todo_id)) BETWEEN 1 AND 256
        AND typeof(NEW.turn_seq) = 'integer'
        AND NEW.turn_seq > 0
        AND NEW.client_key = trim(NEW.client_key)
        AND NEW.client_key = printf('governance:%s:%s:%d', NEW.goal_id, NEW.todo_id, NEW.turn_seq)
        AND length(trim(NEW.decision_schema_version)) BETWEEN 1 AND 128
        AND NEW.decision_schema_version = trim(NEW.decision_schema_version)
        AND length(NEW.decision_schema_digest) = 71
        AND substr(NEW.decision_schema_digest, 1, 7) = 'sha256:'
        AND substr(NEW.decision_schema_digest, 8) NOT GLOB '*[^0-9a-f]*'
        AND length(NEW.decision_digest) = 71
        AND substr(NEW.decision_digest, 1, 7) = 'sha256:'
        AND substr(NEW.decision_digest, 8) NOT GLOB '*[^0-9a-f]*'
        AND EXISTS (
            SELECT 1
            FROM goals g
            JOIN goal_todos t ON t.goal_id = g.id
            WHERE g.id = NEW.goal_id
              AND t.id = NEW.todo_id
              AND g.workspace_id = NEW.workspace_id
              AND g.root_work_item_id = NEW.work_item_id
        ))
)
BEGIN
    SELECT RAISE(ABORT, 'invalid plan governance identity');
END;

-- The governance identity is an immutable statement of which bounded turn
-- produced this Plan.  Plan lifecycle/status fields remain mutable through
-- the existing optimistic Update path.
CREATE TRIGGER plans_governance_identity_immutable
BEFORE UPDATE OF client_key, goal_id, todo_id, turn_seq,
                 decision_schema_version, decision_schema_digest, decision_digest
ON plans
WHEN NOT (
       NEW.client_key IS OLD.client_key
   AND NEW.goal_id IS OLD.goal_id
   AND NEW.todo_id IS OLD.todo_id
   AND NEW.turn_seq IS OLD.turn_seq
   AND NEW.decision_schema_version IS OLD.decision_schema_version
   AND NEW.decision_schema_digest IS OLD.decision_schema_digest
   AND NEW.decision_digest IS OLD.decision_digest
)
BEGIN
    SELECT RAISE(ABORT, 'plan governance identity is immutable');
END;

CREATE TRIGGER plans_governance_identity_update
BEFORE UPDATE ON plans
WHEN NOT (
       (NEW.client_key IS NULL
        AND NEW.goal_id IS NULL
        AND NEW.todo_id IS NULL
        AND NEW.turn_seq IS NULL
        AND NEW.decision_schema_version IS NULL
        AND NEW.decision_schema_digest IS NULL
        AND NEW.decision_digest IS NULL)
    OR (NEW.client_key IS NOT NULL
        AND NEW.goal_id IS NOT NULL
        AND NEW.todo_id IS NOT NULL
        AND NEW.turn_seq IS NOT NULL
        AND NEW.decision_schema_version IS NOT NULL
        AND NEW.decision_schema_digest IS NOT NULL
        AND NEW.decision_digest IS NOT NULL
        AND length(trim(NEW.client_key)) BETWEEN 1 AND 256
        AND length(trim(NEW.goal_id)) BETWEEN 1 AND 256
        AND length(trim(NEW.todo_id)) BETWEEN 1 AND 256
        AND typeof(NEW.turn_seq) = 'integer'
        AND NEW.turn_seq > 0
        AND NEW.client_key = trim(NEW.client_key)
        AND NEW.client_key = printf('governance:%s:%s:%d', NEW.goal_id, NEW.todo_id, NEW.turn_seq)
        AND length(trim(NEW.decision_schema_version)) BETWEEN 1 AND 128
        AND NEW.decision_schema_version = trim(NEW.decision_schema_version)
        AND length(NEW.decision_schema_digest) = 71
        AND substr(NEW.decision_schema_digest, 1, 7) = 'sha256:'
        AND substr(NEW.decision_schema_digest, 8) NOT GLOB '*[^0-9a-f]*'
        AND length(NEW.decision_digest) = 71
        AND substr(NEW.decision_digest, 1, 7) = 'sha256:'
        AND substr(NEW.decision_digest, 8) NOT GLOB '*[^0-9a-f]*'
        AND EXISTS (
            SELECT 1
            FROM goals g
            JOIN goal_todos t ON t.goal_id = g.id
            WHERE g.id = NEW.goal_id
              AND t.id = NEW.todo_id
              AND g.workspace_id = NEW.workspace_id
              AND g.root_work_item_id = NEW.work_item_id
        ))
)
BEGIN
    SELECT RAISE(ABORT, 'invalid plan governance identity');
END;

CREATE TRIGGER turn_receipt_plan_phase_governance_lineage
BEFORE INSERT ON turn_receipt_phases
WHEN NEW.phase_seq IN (4, 5)
 AND NOT EXISTS (
    SELECT 1
    FROM plans p
    WHERE p.id = NEW.plan_id
      AND p.goal_id = NEW.goal_id
      AND p.todo_id = NEW.todo_id
      AND p.turn_seq = NEW.turn_seq
 )
BEGIN
    SELECT RAISE(ABORT, 'turn receipt Plan phase requires matching governance Plan identity');
END;
