-- 0039_governance_todo_completion_identity.sql — bind Todo completion to
-- an admitted governance turn and the accepted evidence identity.
ALTER TABLE goal_todos ADD COLUMN completion_turn_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE goal_todos ADD COLUMN completion_evidence_id TEXT;

-- Acceptance closes the Todo from waiting_user as well as from running. The
-- original 0024 trigger intentionally covered only pre-acceptance lifecycle;
-- replace it with the same guard plus this one controlled terminal edge.
DROP TRIGGER goal_todos_status_transition_guard;
CREATE TRIGGER goal_todos_status_transition_guard
BEFORE UPDATE OF status ON goal_todos
WHEN NEW.status <> OLD.status
 AND NOT (
       (OLD.status = 'pending' AND NEW.status IN ('claimed','waiting','blocked','cancelled'))
    OR (OLD.status = 'claimed' AND NEW.status = 'running' AND (
           NEW.last_turn_seq = OLD.last_turn_seq + 1
        OR (NEW.last_turn_seq = OLD.last_turn_seq
            AND NEW.last_turn_seq > 0
            AND EXISTS (
                SELECT 1 FROM turn_receipt_headers h
                WHERE h.goal_id = NEW.goal_id
                  AND h.todo_id = NEW.id
                  AND h.turn_seq = NEW.last_turn_seq
            ))
       ))
    OR (OLD.status = 'claimed' AND NEW.status IN ('pending','waiting','blocked','cancelled'))
    OR (OLD.status = 'running' AND NEW.status IN ('waiting','completed','blocked','cancelled'))
    OR (OLD.status = 'waiting' AND NEW.status IN ('pending','claimed','completed','blocked','cancelled'))
    OR (OLD.status = 'blocked' AND NEW.status IN ('pending','claimed','waiting','cancelled'))
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal todo status transition');
END;

CREATE TRIGGER goal_todos_completion_identity_insert
BEFORE INSERT ON goal_todos
WHEN (NEW.status = 'completed'
      AND (NEW.completion_turn_seq < 1
           OR NEW.completion_turn_seq <> NEW.last_turn_seq
           OR NOT EXISTS (
               SELECT 1 FROM turn_receipt_headers h
               WHERE h.goal_id = NEW.goal_id
                 AND h.todo_id = NEW.id
                 AND h.turn_seq = NEW.completion_turn_seq)
           OR NEW.completion_evidence_id IS NULL
           OR length(trim(NEW.completion_evidence_id)) = 0
           OR NOT EXISTS (
               SELECT 1 FROM goals g JOIN work_items wi
                 ON wi.id = g.root_work_item_id AND wi.workspace_id = g.workspace_id
                WHERE g.id = NEW.goal_id
                  AND NEW.completion_evidence_id = g.root_work_item_id
                  AND wi.status = 'completed')))
   OR (NEW.status <> 'completed'
       AND (NEW.completion_turn_seq <> 0 OR NEW.completion_evidence_id IS NOT NULL))
BEGIN
    SELECT RAISE(ABORT, 'completed Todo requires completion turn and evidence');
END;

CREATE TRIGGER goal_todos_completion_identity_update
BEFORE UPDATE OF status, completion_turn_seq, completion_evidence_id, last_turn_seq ON goal_todos
WHEN (NEW.status = 'completed'
      AND (NEW.completion_turn_seq < 1
           OR NEW.completion_turn_seq <> NEW.last_turn_seq
           OR NOT EXISTS (
               SELECT 1 FROM turn_receipt_headers h
               WHERE h.goal_id = NEW.goal_id
                 AND h.todo_id = NEW.id
                 AND h.turn_seq = NEW.completion_turn_seq)
           OR NEW.completion_evidence_id IS NULL
           OR length(trim(NEW.completion_evidence_id)) = 0
           OR NOT EXISTS (
               SELECT 1 FROM goals g JOIN work_items wi
                 ON wi.id = g.root_work_item_id AND wi.workspace_id = g.workspace_id
                WHERE g.id = NEW.goal_id
                  AND NEW.completion_evidence_id = g.root_work_item_id
                  AND wi.status = 'completed')))
   OR (NEW.status <> 'completed'
       AND (NEW.completion_turn_seq <> 0 OR NEW.completion_evidence_id IS NOT NULL))
BEGIN
    SELECT RAISE(ABORT, 'completed Todo requires completion turn and evidence');
END;

CREATE TRIGGER goal_todos_completion_identity_immutable
BEFORE UPDATE OF completion_turn_seq, completion_evidence_id ON goal_todos
WHEN OLD.status = 'completed'
 AND (NEW.completion_turn_seq IS NOT OLD.completion_turn_seq
      OR NEW.completion_evidence_id IS NOT OLD.completion_evidence_id)
BEGIN
    SELECT RAISE(ABORT, 'completed Todo identity is immutable');
END;

-- Upgrade probe: rows that were already completed before 0039 must prove the
-- new completion contract immediately. Do not invent a TurnReceipt/evidence
-- backfill; force the existing row through the same validation trigger and
-- abort the migration if any legacy terminal row is unverifiable.
CREATE TRIGGER goal_todos_completion_upgrade_probe
BEFORE UPDATE OF completion_turn_seq, completion_evidence_id ON goal_todos
WHEN OLD.status = 'completed'
 AND (
       OLD.completion_turn_seq < 1
    OR OLD.completion_turn_seq <> OLD.last_turn_seq
    OR OLD.completion_evidence_id IS NULL
    OR length(trim(OLD.completion_evidence_id)) = 0
    OR NOT EXISTS (
        SELECT 1 FROM turn_receipt_headers h
        WHERE h.goal_id = OLD.goal_id
          AND h.todo_id = OLD.id
          AND h.turn_seq = OLD.completion_turn_seq)
 )
BEGIN
    SELECT RAISE(ABORT, 'legacy completed Todo lacks verifiable completion identity');
END;

UPDATE goal_todos
   SET completion_turn_seq = completion_turn_seq
 WHERE status = 'completed';

DROP TRIGGER goal_todos_completion_upgrade_probe;

-- A completed/cancelled Todo is a terminal audit record. The application
-- exposes no mutation path for it, and SQLite must enforce the same rule for
-- maintenance SQL or an older process: no business field, claim, watermark,
-- version, or timestamp may be rewritten after terminalization.
CREATE TRIGGER goal_todos_terminal_immutable
BEFORE UPDATE ON goal_todos
WHEN OLD.status IN ('completed', 'cancelled')
BEGIN
    SELECT RAISE(ABORT, 'terminal Todo is immutable');
END;
