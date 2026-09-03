-- 0029_governance_handoff_projection.sql — ownership handoff, validation
-- evidence and rebuildable governance projections.
--
-- Handoffs and validation results are governance records. They do not introduce
-- another Run/Lease/Event/Settlement store. The projection table is mutable
-- read-model material; the canonical Goal/Todo/Receipt/Event rows remain the
-- only authority.

CREATE TABLE governance_handoffs (
    id                   TEXT NOT NULL PRIMARY KEY,
    goal_id              TEXT NOT NULL REFERENCES goals(id),
    todo_id              TEXT NOT NULL,
    source_kind          TEXT NOT NULL CHECK (source_kind IN ('agent','runtime')),
    source_id            TEXT NOT NULL CHECK (length(trim(source_id)) BETWEEN 1 AND 256),
    target_kind          TEXT NOT NULL CHECK (target_kind IN ('agent','runtime')),
    target_id            TEXT NOT NULL CHECK (length(trim(target_id)) BETWEEN 1 AND 256),
    reason               TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 4000),
    context_summary      TEXT NOT NULL CHECK (length(trim(context_summary)) BETWEEN 1 AND 20000),
    evidence             TEXT NOT NULL DEFAULT '[]'
                         CHECK (json_valid(evidence) = 1
                            AND json_type(evidence) = 'array'
                            AND json_array_length(evidence) <= 128),
    open_risks           TEXT NOT NULL DEFAULT '[]'
                         CHECK (json_valid(open_risks) = 1
                            AND json_type(open_risks) = 'array'
                            AND json_array_length(open_risks) <= 64),
    acceptance           TEXT NOT NULL DEFAULT '',
    resolution_reason   TEXT NOT NULL DEFAULT '',
    status               TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','accepted','transferred','rejected','cancelled')),
    claim_transfer_state TEXT NOT NULL DEFAULT 'retained_by_source'
                         CHECK (claim_transfer_state IN ('retained_by_source','claimed_by_target','transferred')),
    source_claim_version INTEGER NOT NULL CHECK (typeof(source_claim_version) = 'integer' AND source_claim_version >= 1),
    target_claim_version INTEGER NOT NULL DEFAULT 0 CHECK (typeof(target_claim_version) = 'integer' AND target_claim_version >= 0),
    actor_kind           TEXT NOT NULL CHECK (actor_kind IN ('agent','runtime')),
    actor_id             TEXT NOT NULL CHECK (length(trim(actor_id)) BETWEEN 1 AND 256),
    client_key           TEXT,
    accepted_by_kind     TEXT CHECK (accepted_by_kind IS NULL OR accepted_by_kind IN ('agent','runtime')),
    accepted_by_id       TEXT CHECK (accepted_by_id IS NULL OR length(trim(accepted_by_id)) BETWEEN 1 AND 256),
    accepted_at          DATETIME,
    version              INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version) = 'integer' AND version >= 1),
    created_at           DATETIME NOT NULL,
    updated_at           DATETIME NOT NULL,
    FOREIGN KEY (goal_id, todo_id) REFERENCES goal_todos(goal_id, id),
    UNIQUE (goal_id, todo_id, client_key),
    CHECK (source_id = trim(source_id) AND target_id = trim(target_id) AND actor_id = trim(actor_id)),
    CHECK (source_kind <> 'agent' OR substr(source_id, 1, 6) = 'agent_'),
    CHECK (target_kind <> 'agent' OR substr(target_id, 1, 6) = 'agent_'),
    CHECK (actor_kind <> 'agent' OR substr(actor_id, 1, 6) = 'agent_'),
    CHECK (accepted_by_kind IS NULL OR accepted_by_id IS NOT NULL),
    CHECK (accepted_by_kind IS NOT NULL OR accepted_by_id IS NULL),
    CHECK (status IN ('accepted','transferred')
        AND length(trim(acceptance)) BETWEEN 1 AND 4000
        AND accepted_by_kind IS NOT NULL AND accepted_at IS NOT NULL
        OR status IN ('pending','rejected','cancelled')
        AND accepted_by_kind IS NULL AND accepted_by_id IS NULL AND accepted_at IS NULL),
    CHECK (status <> 'rejected' OR length(trim(resolution_reason)) BETWEEN 1 AND 4000),
    CHECK (status <> 'pending' AND status <> 'rejected' AND status <> 'cancelled' OR acceptance = ''),
    CHECK (status = 'pending' AND claim_transfer_state = 'retained_by_source'
        OR status = 'accepted' AND claim_transfer_state = 'claimed_by_target'
        OR status = 'transferred' AND claim_transfer_state = 'transferred'
        OR status IN ('rejected','cancelled') AND claim_transfer_state = 'retained_by_source')
);

CREATE INDEX idx_governance_handoffs_goal ON governance_handoffs(goal_id, created_at, id);
CREATE INDEX idx_governance_handoffs_todo ON governance_handoffs(todo_id, created_at, id);
CREATE UNIQUE INDEX idx_governance_handoffs_client_key
    ON governance_handoffs(goal_id, todo_id, client_key)
    WHERE client_key IS NOT NULL;

CREATE TRIGGER governance_handoffs_scope_insert
BEFORE INSERT ON governance_handoffs
WHEN NOT EXISTS (
    SELECT 1
      FROM goals g
      JOIN goal_todos t ON t.goal_id = g.id AND t.id = NEW.todo_id
     WHERE g.id = NEW.goal_id
       AND g.workspace_id = (SELECT workspace_id FROM work_items WHERE id = g.root_work_item_id)
)
BEGIN
    SELECT RAISE(ABORT, 'governance handoff Goal/Todo scope mismatch');
END;

CREATE TRIGGER governance_handoffs_agent_scope_insert
BEFORE INSERT ON governance_handoffs
WHEN (NEW.source_kind = 'agent' OR NEW.target_kind = 'agent' OR NEW.actor_kind = 'agent')
 AND NOT (
       (NEW.source_kind <> 'agent' OR EXISTS (
            SELECT 1 FROM goals g JOIN agent_profiles a ON a.id = NEW.source_id
             WHERE g.id = NEW.goal_id AND a.workspace_id = g.workspace_id))
   AND (NEW.target_kind <> 'agent' OR EXISTS (
            SELECT 1 FROM goals g JOIN agent_profiles a ON a.id = NEW.target_id
             WHERE g.id = NEW.goal_id AND a.workspace_id = g.workspace_id))
   AND (NEW.actor_kind <> 'agent' OR EXISTS (
            SELECT 1 FROM goals g JOIN agent_profiles a ON a.id = NEW.actor_id
             WHERE g.id = NEW.goal_id AND a.workspace_id = g.workspace_id))
 )
BEGIN
    SELECT RAISE(ABORT, 'governance handoff agent is outside Goal workspace');
END;

CREATE TRIGGER governance_handoffs_identity_immutable
BEFORE UPDATE OF id, goal_id, todo_id, source_kind, source_id, target_kind, target_id,
                 reason, context_summary, evidence, open_risks, actor_kind, actor_id,
                 client_key, source_claim_version
ON governance_handoffs
WHEN NOT (
       NEW.id IS OLD.id
   AND NEW.goal_id IS OLD.goal_id
   AND NEW.todo_id IS OLD.todo_id
   AND NEW.source_kind IS OLD.source_kind
   AND NEW.source_id IS OLD.source_id
   AND NEW.target_kind IS OLD.target_kind
   AND NEW.target_id IS OLD.target_id
   AND NEW.reason IS OLD.reason
   AND NEW.context_summary IS OLD.context_summary
   AND NEW.evidence IS OLD.evidence
   AND NEW.open_risks IS OLD.open_risks
   AND NEW.actor_kind IS OLD.actor_kind
   AND NEW.actor_id IS OLD.actor_id
   AND NEW.client_key IS OLD.client_key
   AND NEW.source_claim_version IS OLD.source_claim_version
)
BEGIN
    SELECT RAISE(ABORT, 'governance handoff identity is immutable');
END;

CREATE TRIGGER governance_handoffs_status_transition
BEFORE UPDATE OF status ON governance_handoffs
WHEN NEW.status <> OLD.status
 AND NOT (
       (OLD.status = 'pending' AND NEW.status IN ('accepted','transferred','rejected','cancelled'))
    OR (OLD.status = 'accepted' AND NEW.status IN ('transferred','cancelled'))
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal governance handoff status transition');
END;

CREATE TABLE governance_validation_results (
    id              TEXT NOT NULL PRIMARY KEY,
    goal_id         TEXT NOT NULL REFERENCES goals(id),
    todo_id         TEXT NOT NULL,
    work_item_id    TEXT NOT NULL REFERENCES work_items(id),
    source_run_id   TEXT NOT NULL REFERENCES execution_runs(id),
    criteria_digest TEXT NOT NULL
                    CHECK (length(criteria_digest) = 71
                       AND substr(criteria_digest, 1, 7) = 'sha256:'
                       AND substr(criteria_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    status          TEXT NOT NULL CHECK (status IN ('pending','passed','failed')),
    summary         TEXT NOT NULL CHECK (length(trim(summary)) BETWEEN 1 AND 4000),
    produced_by     TEXT NOT NULL CHECK (produced_by = 'control_plane'),
    recorded_at     DATETIME NOT NULL,
    version         INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version) = 'integer' AND version >= 1),
    created_at      DATETIME NOT NULL,
    FOREIGN KEY (goal_id, todo_id) REFERENCES goal_todos(goal_id, id)
);

CREATE INDEX idx_governance_validation_results_goal
    ON governance_validation_results(goal_id, recorded_at, id);
CREATE UNIQUE INDEX idx_governance_validation_results_source_run
    ON governance_validation_results(source_run_id);

CREATE TRIGGER governance_validation_result_scope_insert
BEFORE INSERT ON governance_validation_results
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
      JOIN execution_runs r ON r.id = NEW.source_run_id
      JOIN subtree item ON item.id = NEW.work_item_id
     WHERE g.id = NEW.goal_id
       AND r.workspace_id = g.workspace_id
       AND r.work_item_id = NEW.work_item_id
       AND r.status IN ('succeeded','interrupted','cancelled','lost','failed')
)
BEGIN
    SELECT RAISE(ABORT, 'governance validation result scope/source mismatch');
END;

CREATE TRIGGER governance_validation_result_immutable_update
BEFORE UPDATE ON governance_validation_results
BEGIN
    SELECT RAISE(ABORT, 'governance validation results are immutable');
END;

CREATE TRIGGER governance_validation_result_immutable_delete
BEFORE DELETE ON governance_validation_results
BEGIN
    SELECT RAISE(ABORT, 'governance validation results are immutable');
END;

CREATE TABLE governance_goal_projections (
    goal_id                 TEXT NOT NULL PRIMARY KEY REFERENCES goals(id),
    goal_progress           TEXT NOT NULL CHECK (json_valid(goal_progress) = 1 AND json_type(goal_progress) = 'object'),
    todo_current_state      TEXT NOT NULL CHECK (json_valid(todo_current_state) = 1 AND json_type(todo_current_state) = 'object'),
    receipt_timeline        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(receipt_timeline) = 1 AND json_type(receipt_timeline) = 'array' AND json_array_length(receipt_timeline) <= 4096),
    evidence_summary        TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(evidence_summary) = 1 AND json_type(evidence_summary) = 'array' AND json_array_length(evidence_summary) <= 512),
    next_action_checkpoint  TEXT NOT NULL CHECK (json_valid(next_action_checkpoint) = 1 AND json_type(next_action_checkpoint) = 'object'),
    counters                TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(counters) = 1 AND json_type(counters) = 'object'),
    source_event_stream_seq INTEGER NOT NULL CHECK (typeof(source_event_stream_seq) = 'integer' AND source_event_stream_seq >= 0),
    through_turn_seq        INTEGER NOT NULL CHECK (typeof(through_turn_seq) = 'integer' AND through_turn_seq >= 0),
    digest                  TEXT NOT NULL
                            CHECK (length(digest) = 71
                               AND substr(digest, 1, 7) = 'sha256:'
                               AND substr(digest, 8) NOT GLOB '*[^0-9a-f]*'),
    version                 INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version) = 'integer' AND version >= 1),
    updated_at              DATETIME NOT NULL
);

CREATE TRIGGER governance_goal_projection_identity_immutable
BEFORE UPDATE OF goal_id ON governance_goal_projections
WHEN NEW.goal_id IS NOT OLD.goal_id
BEGIN
    SELECT RAISE(ABORT, 'governance projection identity is immutable');
END;

CREATE TABLE governance_projection_repairs (
    id                     TEXT NOT NULL PRIMARY KEY,
    goal_id                TEXT NOT NULL REFERENCES goals(id),
    status                 TEXT NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending','running','completed','failed')),
    scope                  TEXT NOT NULL
                           CHECK (json_valid(scope) = 1
                              AND json_type(scope) = 'array'
                              AND json_array_length(scope) BETWEEN 1 AND 5),
    source_event_stream_seq INTEGER NOT NULL DEFAULT 0 CHECK (typeof(source_event_stream_seq) = 'integer' AND source_event_stream_seq >= 0),
    through_turn_seq        INTEGER NOT NULL DEFAULT 0 CHECK (typeof(through_turn_seq) = 'integer' AND through_turn_seq >= 0),
    replayed_event_count    INTEGER NOT NULL DEFAULT 0 CHECK (typeof(replayed_event_count) = 'integer' AND replayed_event_count >= 0),
    replayed_receipt_count  INTEGER NOT NULL DEFAULT 0 CHECK (typeof(replayed_receipt_count) = 'integer' AND replayed_receipt_count >= 0),
    error_code              TEXT NOT NULL DEFAULT '',
    error_message           TEXT NOT NULL DEFAULT '',
    client_key              TEXT,
    version                 INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version) = 'integer' AND version >= 1),
    started_at              DATETIME NOT NULL,
    completed_at            DATETIME,
    created_at              DATETIME NOT NULL,
    updated_at              DATETIME NOT NULL,
    UNIQUE (goal_id, client_key),
    CHECK ((status = 'completed' AND completed_at IS NOT NULL) OR
           (status <> 'completed' AND completed_at IS NULL)),
    CHECK ((status = 'failed' AND length(trim(error_code)) BETWEEN 1 AND 128) OR
           (status <> 'failed' AND error_code = ''))
);

CREATE INDEX idx_governance_projection_repairs_goal
    ON governance_projection_repairs(goal_id, status, updated_at, id);

CREATE UNIQUE INDEX idx_governance_projection_repairs_client_key
    ON governance_projection_repairs(goal_id, client_key)
    WHERE client_key IS NOT NULL;

CREATE TRIGGER governance_projection_repair_identity_immutable
BEFORE UPDATE OF id, goal_id, client_key ON governance_projection_repairs
WHEN NOT (NEW.id IS OLD.id AND NEW.goal_id IS OLD.goal_id AND NEW.client_key IS OLD.client_key)
BEGIN
    SELECT RAISE(ABORT, 'projection repair identity is immutable');
END;

CREATE TRIGGER governance_projection_repair_status_transition
BEFORE UPDATE OF status ON governance_projection_repairs
WHEN NEW.status <> OLD.status
 AND NOT (
       (OLD.status = 'pending' AND NEW.status IN ('running','failed'))
    OR (OLD.status = 'running' AND NEW.status IN ('completed','failed','running'))
    OR (OLD.status = 'failed' AND NEW.status IN ('running','failed'))
    OR (OLD.status = 'completed' AND NEW.status = 'running')
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal projection repair status transition');
END;
