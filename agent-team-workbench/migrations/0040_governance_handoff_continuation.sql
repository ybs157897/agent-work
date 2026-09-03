-- 0040_governance_handoff_continuation.sql — admit delegated Coordinator
-- sources and support fenced same-generation claim renewal.

-- 0030 originally limited governed receipt sources to the protected system
-- profile. A transferred Handoff is the only additional source shape: the
-- persisted Run must carry the delegated proof and its target claim generation
-- must still be current. Keep the original system branch unchanged.
DROP TRIGGER turn_receipt_headers_recovery_checkpoint_insert;
CREATE TRIGGER turn_receipt_headers_recovery_checkpoint_insert
BEFORE INSERT ON turn_receipt_headers
WHEN NOT (
       (NEW.source_run_id IS NULL
        AND NEW.plan_client_key IS NULL
        AND NEW.decision_digest IS NULL)
    OR (
        NEW.source_run_id IS NOT NULL
        AND NEW.plan_client_key IS NOT NULL
        AND NEW.decision_digest IS NOT NULL
        AND length(trim(NEW.source_run_id)) BETWEEN 5 AND 256
        AND substr(NEW.source_run_id, 1, 4) = 'run_'
        AND length(trim(NEW.plan_client_key)) BETWEEN 1 AND 256
        AND NEW.plan_client_key = 'governance:' || NEW.goal_id || ':' || NEW.todo_id || ':' || NEW.turn_seq
        AND length(NEW.decision_digest) = 71
        AND substr(NEW.decision_digest, 1, 7) = 'sha256:'
        AND substr(NEW.decision_digest, 8) NOT GLOB '*[^0-9a-f]*'
        AND EXISTS (
            SELECT 1
              FROM execution_runs run
              JOIN goals goal ON goal.id = NEW.goal_id
              JOIN agent_profiles owner ON owner.id = run.agent_profile_id
             WHERE run.id = NEW.source_run_id
               AND run.workspace_id = goal.workspace_id
               AND run.work_item_id = goal.root_work_item_id
               AND owner.workspace_id = goal.workspace_id
               AND owner.kind = 'task_coordinator'
        )
    )
    OR (
        NEW.source_run_id IS NOT NULL
        AND NEW.plan_client_key IS NOT NULL
        AND NEW.decision_digest IS NOT NULL
        AND length(trim(NEW.source_run_id)) BETWEEN 5 AND 256
        AND substr(NEW.source_run_id, 1, 4) = 'run_'
        AND length(trim(NEW.plan_client_key)) BETWEEN 1 AND 256
        AND NEW.plan_client_key = 'governance:' || NEW.goal_id || ':' || NEW.todo_id || ':' || NEW.turn_seq
        AND length(NEW.decision_digest) = 71
        AND substr(NEW.decision_digest, 1, 7) = 'sha256:'
        AND substr(NEW.decision_digest, 8) NOT GLOB '*[^0-9a-f]*'
        AND EXISTS (
            SELECT 1
              FROM execution_runs run
              JOIN goals goal ON goal.id = NEW.goal_id
              JOIN agent_profiles owner ON owner.id = run.agent_profile_id
              JOIN goal_todos todo ON todo.goal_id = NEW.goal_id AND todo.id = NEW.todo_id
              JOIN governance_handoffs handoff
                ON handoff.id = json_extract(run.input, '$.task_coordinator.handoff_id')
               AND handoff.goal_id = NEW.goal_id
               AND handoff.todo_id = NEW.todo_id
               AND handoff.status = 'transferred'
               AND handoff.target_claim_version = todo.claim_version
             WHERE run.id = NEW.source_run_id
               AND run.workspace_id = goal.workspace_id
               AND run.work_item_id = goal.root_work_item_id
               AND owner.workspace_id = goal.workspace_id
               AND owner.kind = 'user'
               AND json_extract(run.input, '$.task_coordinator.role') = 'coordinator'
               AND json_extract(run.input, '$.task_coordinator.delegated') = 1
               AND todo.status = 'running'
               AND todo.claim_owner_agent_id = run.agent_profile_id
               AND (
                   (handoff.target_kind = 'agent' AND handoff.target_id = run.agent_profile_id)
                   OR (handoff.target_kind = 'runtime'
                       AND handoff.target_id = json_extract(owner.runtime_preference, '$.preferred'))
               )
        )
    )
)
BEGIN
    SELECT RAISE(ABORT, 'governed turn recovery checkpoint is incomplete or outside the Goal root');
END;

-- Claim generation changes fence ownership transfers/releases. A same-owner
-- renewal is the one permitted in-place mutation for a long delegated
-- Coordinator turn; it keeps claim_version unchanged while still advancing
-- the Todo version through the application CAS.
DROP TRIGGER goal_todos_claim_version_step;
CREATE TRIGGER goal_todos_claim_version_step
BEFORE UPDATE OF claim_owner_agent_id, claim_version, claim_claimed_at, claim_expires_at ON goal_todos
WHEN NOT (
       NEW.claim_owner_agent_id IS OLD.claim_owner_agent_id
       AND NEW.claim_owner_agent_id IS NOT NULL
       AND NEW.claim_version = OLD.claim_version
     )
 AND NEW.claim_version <> OLD.claim_version + 1
BEGIN
    SELECT RAISE(ABORT, 'todo claim_version must increase by one');
END;

-- A same-owner/same-generation write is a renewal, not a new claim. Keep the
-- original ownership timestamp and require a strictly later expiry so direct
-- SQL cannot silently shorten or rewrite a delegated claim outside the CAS
-- repository operation.
CREATE TRIGGER goal_todos_same_generation_claimed_at_guard
BEFORE UPDATE OF claim_owner_agent_id, claim_version, claim_claimed_at ON goal_todos
WHEN NEW.claim_owner_agent_id IS OLD.claim_owner_agent_id
 AND NEW.claim_owner_agent_id IS NOT NULL
 AND NEW.claim_version = OLD.claim_version
 AND NEW.claim_claimed_at IS NOT OLD.claim_claimed_at
BEGIN
    SELECT RAISE(ABORT, 'same-generation Todo renewal must preserve claimed_at');
END;

CREATE TRIGGER goal_todos_same_generation_expiry_guard
BEFORE UPDATE OF claim_owner_agent_id, claim_version, claim_expires_at ON goal_todos
WHEN NEW.claim_owner_agent_id IS OLD.claim_owner_agent_id
 AND NEW.claim_owner_agent_id IS NOT NULL
 AND NEW.claim_version = OLD.claim_version
 AND (
       OLD.claim_expires_at IS NULL
    OR NEW.claim_expires_at IS NULL
    OR julianday(OLD.claim_expires_at) IS NULL
    OR julianday(NEW.claim_expires_at) IS NULL
    OR julianday(NEW.claim_expires_at) <= julianday(OLD.claim_expires_at)
 )
BEGIN
    SELECT RAISE(ABORT, 'same-generation Todo renewal must extend expiry');
END;
