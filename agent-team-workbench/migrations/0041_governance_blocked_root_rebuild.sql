-- 0041_governance_blocked_root_rebuild.sql — materialize an already-blocked
-- root as blocked governance state without inventing a runnable cycle.

-- A blocked root may have a draft Goal created before the blocker was
-- projected. Rebuild is allowed to close that draft directly to blocked; no
-- other new Goal transition is introduced.
DROP TRIGGER goals_status_transition_guard;
CREATE TRIGGER goals_status_transition_guard
BEFORE UPDATE OF status ON goals
WHEN NEW.status <> OLD.status
 AND NOT (
       (OLD.status = 'draft' AND NEW.status IN ('active','blocked','cancelled'))
    OR (OLD.status = 'active' AND NEW.status IN ('waiting','blocked','completed','cancelled'))
    OR (OLD.status = 'waiting' AND NEW.status IN ('active','blocked','completed','cancelled'))
    OR (OLD.status = 'blocked' AND NEW.status IN ('active','waiting','cancelled'))
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal goal status transition');
END;
