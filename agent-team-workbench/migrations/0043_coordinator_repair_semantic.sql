-- 0043_coordinator_repair_semantic.sql — semantic plan errors join the bounded
-- auto-repair budget. plan_semantic_validation is a high-frequency self-healable
-- model contract violation (e.g. finish placed in the same decision as a join
-- barrier); blocking for manual intervention on the first occurrence breaks the
-- unattended main path. Domain ValidateRepair is the authoritative contract;
-- these triggers only re-assert it at the SQL edge, so 'semantic' joins the
-- pending/exhausted class whitelist alongside syntax/schema. authority/quota
-- stay manual-only.

DROP TRIGGER task_coordinator_repair_checkpoint_insert;
DROP TRIGGER task_coordinator_repair_checkpoint_update;

CREATE TRIGGER task_coordinator_repair_checkpoint_insert
BEFORE INSERT ON task_coordinator_states
WHEN NOT (
       (NEW.repair_status = 'none'
        AND NEW.repair_attempt = 0
        AND NEW.repair_source_run_id IS NULL
        AND NEW.repair_error_class = ''
        AND NEW.repair_error_code = ''
        AND json_array_length(NEW.repair_validation_errors) = 0)
    OR (NEW.repair_status = 'pending'
        AND NEW.repair_attempt BETWEEN 1 AND 2
        AND NEW.repair_source_run_id IS NOT NULL
        AND NEW.repair_error_class IN ('syntax','schema','semantic')
        AND length(trim(NEW.repair_error_code)) BETWEEN 1 AND 128)
    OR (NEW.repair_status = 'exhausted'
        AND NEW.repair_attempt = 2
        AND NEW.repair_source_run_id IS NOT NULL
        AND NEW.repair_error_class IN ('syntax','schema','semantic')
        AND length(trim(NEW.repair_error_code)) BETWEEN 1 AND 128
        AND NEW.status = 'blocked'
        AND NEW.current_run_id IS NULL
        AND NEW.next_action_at IS NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'invalid coordinator repair checkpoint');
END;

CREATE TRIGGER task_coordinator_repair_checkpoint_update
BEFORE UPDATE OF status, current_run_id, next_action_at, repair_status,
                 repair_attempt, repair_source_run_id, repair_error_class,
                 repair_error_code, repair_validation_errors
ON task_coordinator_states
WHEN NOT (
       (NEW.repair_status = 'none'
        AND NEW.repair_attempt = 0
        AND NEW.repair_source_run_id IS NULL
        AND NEW.repair_error_class = ''
        AND NEW.repair_error_code = ''
        AND json_array_length(NEW.repair_validation_errors) = 0)
    OR (NEW.repair_status = 'pending'
        AND NEW.repair_attempt BETWEEN 1 AND 2
        AND NEW.repair_source_run_id IS NOT NULL
        AND NEW.repair_error_class IN ('syntax','schema','semantic')
        AND length(trim(NEW.repair_error_code)) BETWEEN 1 AND 128)
    OR (NEW.repair_status = 'exhausted'
        AND NEW.repair_attempt = 2
        AND NEW.repair_source_run_id IS NOT NULL
        AND NEW.repair_error_class IN ('syntax','schema','semantic')
        AND length(trim(NEW.repair_error_code)) BETWEEN 1 AND 128
        AND NEW.status = 'blocked'
        AND NEW.current_run_id IS NULL
        AND NEW.next_action_at IS NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'invalid coordinator repair checkpoint');
END;
