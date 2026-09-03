-- 0025_coordinator_plan_repair.sql — persisted bounded PlanDecision repair.
-- Repair is a checkpoint on the existing Coordinator state/Run/event line;
-- there is intentionally no second repair store, scheduler or event table.

ALTER TABLE task_coordinator_states
    ADD COLUMN repair_status TEXT NOT NULL DEFAULT 'none'
    CHECK (repair_status IN ('none','pending','exhausted'));
ALTER TABLE task_coordinator_states
    ADD COLUMN repair_attempt INTEGER NOT NULL DEFAULT 0
    CHECK (typeof(repair_attempt) = 'integer' AND repair_attempt BETWEEN 0 AND 2);
ALTER TABLE task_coordinator_states
    ADD COLUMN repair_source_run_id TEXT REFERENCES execution_runs(id);
ALTER TABLE task_coordinator_states
    ADD COLUMN repair_error_class TEXT NOT NULL DEFAULT ''
    CHECK (repair_error_class IN ('','syntax','schema','semantic','authority','quota'));
ALTER TABLE task_coordinator_states
    ADD COLUMN repair_error_code TEXT NOT NULL DEFAULT ''
    CHECK (length(repair_error_code) <= 128);
ALTER TABLE task_coordinator_states
    ADD COLUMN repair_validation_errors TEXT NOT NULL DEFAULT '[]'
    CHECK (json_valid(repair_validation_errors) = 1
       AND json_type(repair_validation_errors) = 'array'
       AND json_array_length(repair_validation_errors) <= 128);

CREATE INDEX idx_task_coordinator_states_repair
    ON task_coordinator_states(repair_status, updated_at, root_work_item_id);

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
        AND NEW.repair_error_class IN ('syntax','schema')
        AND length(trim(NEW.repair_error_code)) BETWEEN 1 AND 128)
    OR (NEW.repair_status = 'exhausted'
        AND NEW.repair_attempt = 2
        AND NEW.repair_source_run_id IS NOT NULL
        AND NEW.repair_error_class IN ('syntax','schema')
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
        AND NEW.repair_error_class IN ('syntax','schema')
        AND length(trim(NEW.repair_error_code)) BETWEEN 1 AND 128)
    OR (NEW.repair_status = 'exhausted'
        AND NEW.repair_attempt = 2
        AND NEW.repair_source_run_id IS NOT NULL
        AND NEW.repair_error_class IN ('syntax','schema')
        AND length(trim(NEW.repair_error_code)) BETWEEN 1 AND 128
        AND NEW.status = 'blocked'
        AND NEW.current_run_id IS NULL
        AND NEW.next_action_at IS NULL)
)
BEGIN
    SELECT RAISE(ABORT, 'invalid coordinator repair checkpoint');
END;

-- Prompt v2 changes the protected wire contract from a step array to the
-- canonical PlanDecisionV2 envelope. Drop only the guards that intentionally
-- freeze prompt identity, migrate protected rows, then recreate equivalent v2
-- guards. Other system-profile protection triggers remain in place.
DROP TRIGGER agent_profiles_task_coordinator_insert_protected;
DROP TRIGGER agent_profiles_task_coordinator_protected;
DROP TRIGGER task_coordinator_config_profile_check;
DROP TRIGGER task_coordinator_config_profile_update_check;

UPDATE agent_profiles
SET prompt_version = 'task-coordinator.v2'
WHERE kind = 'task_coordinator';

UPDATE task_coordinator_configs
SET prompt_version = 'task-coordinator.v2';

CREATE TRIGGER agent_profiles_task_coordinator_insert_protected
BEFORE INSERT ON agent_profiles
WHEN NEW.kind = 'task_coordinator'
 AND (NEW.prompt_version <> 'task-coordinator.v2'
      OR NEW.instructions_editable <> 0
      OR COALESCE(json_extract(NEW.policy, '$.sandbox'), '') <> 'read-only'
      OR COALESCE(json_array_length(json_extract(NEW.policy, '$.tools')), 0) <> 0)
BEGIN
    SELECT RAISE(ABORT, 'system task coordinator profile must use the built-in prompt');
END;

CREATE TRIGGER agent_profiles_task_coordinator_protected
BEFORE UPDATE OF kind, instructions, prompt_version, instructions_editable, policy
ON agent_profiles
WHEN OLD.kind = 'task_coordinator'
 AND (NEW.kind <> OLD.kind
      OR NEW.instructions <> OLD.instructions
      OR NEW.prompt_version <> OLD.prompt_version
      OR NEW.instructions_editable <> OLD.instructions_editable
      OR NEW.policy <> OLD.policy)
BEGIN
    SELECT RAISE(ABORT, 'system task coordinator profile is protected');
END;

CREATE TRIGGER task_coordinator_config_profile_check
BEFORE INSERT ON task_coordinator_configs
WHEN NOT EXISTS (
    SELECT 1 FROM agent_profiles
    WHERE id = NEW.agent_profile_id
      AND workspace_id = NEW.workspace_id
      AND kind = 'task_coordinator'
      AND prompt_version = 'task-coordinator.v2'
      AND NEW.prompt_version = 'task-coordinator.v2'
)
BEGIN
    SELECT RAISE(ABORT, 'task coordinator config requires matching system profile');
END;

CREATE TRIGGER task_coordinator_config_profile_update_check
BEFORE UPDATE OF workspace_id, agent_profile_id, prompt_version
ON task_coordinator_configs
WHEN NOT EXISTS (
    SELECT 1 FROM agent_profiles
    WHERE id = NEW.agent_profile_id
      AND workspace_id = NEW.workspace_id
      AND kind = 'task_coordinator'
      AND prompt_version = 'task-coordinator.v2'
      AND NEW.prompt_version = 'task-coordinator.v2'
)
BEGIN
    SELECT RAISE(ABORT, 'task coordinator config requires matching system profile');
END;
