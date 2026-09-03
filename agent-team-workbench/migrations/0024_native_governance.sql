-- 0024_native_governance.sql — Goal/Todo/TurnReceipt 治理基座（SQLite）。
--
-- Governance owns intent and the canonical turn receipt stream.  It does not
-- create a second Run, Lease, quota, event, or .loopx store.  Nested OpenAPI
-- objects are kept as canonical JSON text; relational identity is flattened
-- into explicit composite keys so a receipt can never cross a Goal/Todo.

-- SQLite requires a UNIQUE parent key for a composite foreign key even when
-- work_items.id is already a PRIMARY KEY.  This index also makes the
-- workspace scope explicit at the root binding boundary.
CREATE UNIQUE INDEX idx_work_items_workspace_id
    ON work_items(workspace_id, id);

-- current_todo_id is nullable while a Goal has no admitted Todo.  The
-- composite FK (id,current_todo_id) makes a non-NULL current Todo belong to
-- this exact Goal.  goal_todos is created below; SQLite permits the forward
-- reference and validates it once the parent exists.
CREATE TABLE goals (
    id                         TEXT NOT NULL PRIMARY KEY,
    workspace_id               TEXT NOT NULL REFERENCES workspaces(id),
    root_work_item_id          TEXT NOT NULL,
    objective                  TEXT NOT NULL
                               CHECK (length(trim(objective)) BETWEEN 1 AND 20000),
    acceptance_contract        TEXT NOT NULL
                               CHECK (
                                   json_valid(acceptance_contract) = 1
                                   AND json_type(acceptance_contract) = 'array'
                                   AND json_array_length(acceptance_contract) BETWEEN 1 AND 64
                               ),
    status                     TEXT NOT NULL DEFAULT 'draft'
                               CHECK (status IN ('draft','active','waiting','blocked','completed','cancelled')),
    phase                      TEXT NOT NULL DEFAULT 'draft'
                               CHECK (length(trim(phase)) BETWEEN 1 AND 128),
    current_todo_id            TEXT,
    quota_policies             TEXT NOT NULL DEFAULT '[]'
                               CHECK (
                                   json_valid(quota_policies) = 1
                                   AND json_type(quota_policies) = 'array'
                                   AND json_array_length(quota_policies) <= 8
                               ),
    completion_evidence_summary TEXT NOT NULL DEFAULT '[]'
                               CHECK (
                                   json_valid(completion_evidence_summary) = 1
                                   AND json_type(completion_evidence_summary) = 'array'
                                   AND json_array_length(completion_evidence_summary) <= 128
                               ),
    version                    INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version) = 'integer' AND version >= 1),
    created_at                 DATETIME NOT NULL,
    updated_at                 DATETIME NOT NULL,
    UNIQUE (root_work_item_id),
    FOREIGN KEY (workspace_id, root_work_item_id)
        REFERENCES work_items(workspace_id, id),
    FOREIGN KEY (id, current_todo_id)
        REFERENCES goal_todos(goal_id, id)
);

CREATE TABLE goal_todos (
    id                TEXT NOT NULL PRIMARY KEY,
    goal_id           TEXT NOT NULL REFERENCES goals(id),
    class             TEXT NOT NULL
                      CHECK (class IN ('advancement','monitor','user_gate','blocker','validation')),
    status            TEXT NOT NULL
                      CHECK (status IN ('pending','claimed','running','waiting','completed','blocked','cancelled')),
    instruction       TEXT NOT NULL
                      CHECK (length(trim(instruction)) BETWEEN 1 AND 20000),
    acceptance        TEXT NOT NULL
                      CHECK (
                          json_valid(acceptance) = 1
                          AND json_type(acceptance) = 'array'
                          AND json_array_length(acceptance) BETWEEN 1 AND 64
                      ),
    resume_condition  TEXT
                      CHECK (
                          resume_condition IS NULL
                          OR length(trim(resume_condition)) BETWEEN 1 AND 4000
                      ),
    priority          TEXT NOT NULL
                      CHECK (priority IN ('low','medium','high','urgent')),
    predecessors      TEXT NOT NULL DEFAULT '[]'
                      CHECK (
                          json_valid(predecessors) = 1
                          AND json_type(predecessors) = 'array'
                          AND json_array_length(predecessors) <= 128
                      ),
    successors        TEXT NOT NULL DEFAULT '[]'
                      CHECK (
                          json_valid(successors) = 1
                          AND json_type(successors) = 'array'
                          AND json_array_length(successors) <= 128
                      ),
    decision_scope    TEXT NOT NULL
                      CHECK (json_valid(decision_scope) = 1 AND json_type(decision_scope) = 'object'),
    claim_owner_agent_id TEXT REFERENCES agent_profiles(id),
    claim_version        INTEGER NOT NULL DEFAULT 0 CHECK (typeof(claim_version) = 'integer' AND claim_version >= 0),
    claim_claimed_at     DATETIME,
    claim_expires_at      DATETIME,
    last_turn_seq        INTEGER NOT NULL DEFAULT 0 CHECK (typeof(last_turn_seq) = 'integer' AND last_turn_seq >= 0),
    version              INTEGER NOT NULL DEFAULT 1 CHECK (typeof(version) = 'integer' AND version >= 1),
    created_at           DATETIME NOT NULL,
    updated_at           DATETIME NOT NULL,
    CHECK (
        status NOT IN ('claimed','running')
        OR (claim_owner_agent_id IS NOT NULL
            AND claim_claimed_at IS NOT NULL
            AND claim_expires_at IS NOT NULL)
    ),
    CHECK (
        status NOT IN ('completed','cancelled')
        OR claim_owner_agent_id IS NULL
    ),
    CHECK (
        (claim_owner_agent_id IS NULL
         AND claim_claimed_at IS NULL
         AND claim_expires_at IS NULL)
        OR (claim_owner_agent_id IS NOT NULL
            AND substr(claim_owner_agent_id, 1, 6) = 'agent_'
            AND length(trim(claim_owner_agent_id)) BETWEEN 1 AND 256
            AND claim_version >= 1
            AND claim_claimed_at IS NOT NULL
            AND claim_expires_at IS NOT NULL)
    ),
    CHECK (
        claim_owner_agent_id IS NULL
        OR claim_expires_at > claim_claimed_at
    ),
    UNIQUE (goal_id, id)
);

CREATE INDEX idx_goals_workspace_status_updated
    ON goals(workspace_id, status, updated_at, id);

CREATE INDEX idx_goal_todos_goal_status_updated
    ON goal_todos(goal_id, status, updated_at, id);

CREATE INDEX idx_goal_todos_active_claim_expiry
    ON goal_todos(goal_id, claim_expires_at, id)
    WHERE claim_owner_agent_id IS NOT NULL;

-- Goal and Todo identity/binding keys are durable references.  Progress
-- fields remain mutable, but a caller may not move an aggregate across a
-- workspace/root or move a Todo to another Goal after creation.
CREATE TRIGGER goals_identity_immutable
BEFORE UPDATE OF id, workspace_id, root_work_item_id ON goals
BEGIN
    SELECT RAISE(ABORT, 'goal identity and root binding are immutable');
END;

CREATE TRIGGER goal_todos_identity_immutable
BEFORE UPDATE OF id, goal_id ON goal_todos
BEGIN
    SELECT RAISE(ABORT, 'todo identity and goal binding are immutable');
END;

-- A root Goal must be a task root in the same Workspace.  The composite FK
-- handles scope; this trigger supplies the existing WorkItem root/task
-- semantics which cannot be represented by a plain FK.
CREATE TRIGGER goals_root_scope_insert
BEFORE INSERT ON goals
WHEN NOT EXISTS (
    SELECT 1
    FROM work_items wi
    WHERE wi.id = NEW.root_work_item_id
      AND wi.workspace_id = NEW.workspace_id
      AND wi.parent_id IS NULL
      AND wi.record_kind = 'task'
)
BEGIN
    SELECT RAISE(ABORT, 'goal root must be a same-workspace root task');
END;

CREATE TRIGGER goals_root_scope_update
BEFORE UPDATE OF workspace_id, root_work_item_id ON goals
WHEN NOT EXISTS (
    SELECT 1
    FROM work_items wi
    WHERE wi.id = NEW.root_work_item_id
      AND wi.workspace_id = NEW.workspace_id
      AND wi.parent_id IS NULL
      AND wi.record_kind = 'task'
)
BEGIN
    SELECT RAISE(ABORT, 'goal root must be a same-workspace root task');
END;

-- Goal completion is a projection of the existing root Task's human Accept
-- boundary. It requires the accepted root row, an exact-root accepted evidence
-- reference, and (when present) the durable Coordinator acceptance state.
CREATE TRIGGER goals_completed_requires_root_accept_insert
BEFORE INSERT ON goals
WHEN NEW.status = 'completed'
 AND (
     NOT EXISTS (
         SELECT 1 FROM work_items wi
         WHERE wi.id = NEW.root_work_item_id
           AND wi.workspace_id = NEW.workspace_id
           AND wi.status = 'completed'
     )
     OR NOT EXISTS (
         SELECT 1 FROM json_each(NEW.completion_evidence_summary) evidence
         WHERE json_extract(evidence.value, '$.source_kind') = 'work_item'
           AND json_extract(evidence.value, '$.source_id') = NEW.root_work_item_id
           AND json_extract(evidence.value, '$.verification') = 'accepted'
     )
     OR EXISTS (
         SELECT 1 FROM task_coordinator_states state
         WHERE state.root_work_item_id = NEW.root_work_item_id
           AND state.status <> 'completed'
     )
 )
BEGIN
    SELECT RAISE(ABORT, 'completed goal requires accepted root task');
END;

CREATE TRIGGER goals_completed_requires_root_accept_update
BEFORE UPDATE OF status ON goals
WHEN NEW.status = 'completed'
 AND (
     NOT EXISTS (
         SELECT 1 FROM work_items wi
         WHERE wi.id = NEW.root_work_item_id
           AND wi.workspace_id = NEW.workspace_id
           AND wi.status = 'completed'
     )
     OR NOT EXISTS (
         SELECT 1 FROM json_each(NEW.completion_evidence_summary) evidence
         WHERE json_extract(evidence.value, '$.source_kind') = 'work_item'
           AND json_extract(evidence.value, '$.source_id') = NEW.root_work_item_id
           AND json_extract(evidence.value, '$.verification') = 'accepted'
     )
     OR EXISTS (
         SELECT 1 FROM task_coordinator_states state
         WHERE state.root_work_item_id = NEW.root_work_item_id
           AND state.status <> 'completed'
     )
 )
BEGIN
    SELECT RAISE(ABORT, 'completed goal requires accepted root task');
END;

CREATE TRIGGER goals_status_transition_guard
BEFORE UPDATE OF status ON goals
WHEN NEW.status <> OLD.status
 AND NOT (
       (OLD.status = 'draft' AND NEW.status IN ('active','cancelled'))
    OR (OLD.status = 'active' AND NEW.status IN ('waiting','blocked','completed','cancelled'))
    OR (OLD.status = 'waiting' AND NEW.status IN ('active','blocked','completed','cancelled'))
    OR (OLD.status = 'blocked' AND NEW.status IN ('active','waiting','cancelled'))
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal goal status transition');
END;

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
    OR (OLD.status = 'waiting' AND NEW.status IN ('pending','claimed','blocked','cancelled'))
    OR (OLD.status = 'blocked' AND NEW.status IN ('pending','claimed','waiting','cancelled'))
 )
BEGIN
    SELECT RAISE(ABORT, 'illegal todo status transition');
END;

-- Existing Goals must not become invalid when a root WorkItem is edited.
CREATE TRIGGER goals_root_work_item_update_guard
BEFORE UPDATE OF workspace_id, parent_id, record_kind, status ON work_items
WHEN EXISTS (
    SELECT 1
    FROM goals g
    WHERE g.root_work_item_id = OLD.id
      AND (
          NOT (
              NEW.workspace_id = g.workspace_id
              AND NEW.parent_id IS NULL
              AND NEW.record_kind = 'task'
          )
          OR (g.status = 'completed' AND NEW.status <> 'completed')
      )
)
BEGIN
    SELECT RAISE(ABORT, 'work item is bound as a goal root');
END;

-- The OpenAPI claim object is assembled from these four columns by the repo;
-- keeping the owner/version/timestamps relationally preserves the version
-- across release/expiry.  The owner FK covers existence, while these guards
-- provide the same-workspace relation and protect it after an agent move/delete.
CREATE TRIGGER goal_todos_claim_scope_insert
BEFORE INSERT ON goal_todos
WHEN NEW.claim_owner_agent_id IS NOT NULL
 AND NOT EXISTS (
     SELECT 1
     FROM goals g
     JOIN agent_profiles ap
       ON ap.id = NEW.claim_owner_agent_id
      AND ap.workspace_id = g.workspace_id
     WHERE g.id = NEW.goal_id
 )
BEGIN
    SELECT RAISE(ABORT, 'todo claim agent must belong to the goal workspace');
END;

CREATE TRIGGER goal_todos_claim_scope_update
BEFORE UPDATE OF goal_id, claim_owner_agent_id ON goal_todos
WHEN NEW.claim_owner_agent_id IS NOT NULL
 AND NOT EXISTS (
     SELECT 1
     FROM goals g
     JOIN agent_profiles ap
       ON ap.id = NEW.claim_owner_agent_id
      AND ap.workspace_id = g.workspace_id
     WHERE g.id = NEW.goal_id
 )
BEGIN
    SELECT RAISE(ABORT, 'todo claim agent must belong to the goal workspace');
END;

CREATE TRIGGER goal_todos_claim_agent_move_guard
BEFORE UPDATE OF workspace_id ON agent_profiles
WHEN EXISTS (
    SELECT 1
    FROM goal_todos t
    JOIN goals g ON g.id = t.goal_id
    WHERE t.claim_owner_agent_id = OLD.id
      AND g.workspace_id <> NEW.workspace_id
)
BEGIN
    SELECT RAISE(ABORT, 'claimed agent cannot move out of goal workspace');
END;

CREATE TRIGGER goal_todos_claim_agent_delete_guard
BEFORE DELETE ON agent_profiles
WHEN EXISTS (
    SELECT 1
    FROM goal_todos
    WHERE claim_owner_agent_id = OLD.id
)
BEGIN
    SELECT RAISE(ABORT, 'claimed agent cannot be deleted');
END;

-- Claim release/expiry must advance the durable version as well.  Keeping the
-- counter on the Todo (instead of inside a nullable claim object) prevents an
-- old owner from winning an ABA race after a release.
CREATE TRIGGER goal_todos_claim_version_step
BEFORE UPDATE OF claim_owner_agent_id, claim_version, claim_claimed_at, claim_expires_at ON goal_todos
WHEN NEW.claim_version <> OLD.claim_version + 1
BEGIN
    SELECT RAISE(ABORT, 'todo claim_version must increase by one');
END;

-- The Todo watermark is allocated by a version-CAS in admission.  Once
-- persisted, every direct change is exactly one step; no gap, rewind, or
-- arbitrary write is allowed.
CREATE TRIGGER goal_todos_last_turn_seq_step
BEFORE UPDATE OF last_turn_seq ON goal_todos
WHEN NEW.last_turn_seq <> OLD.last_turn_seq + 1
BEGIN
    SELECT RAISE(ABORT, 'todo last_turn_seq must increase by one');
END;

CREATE TABLE turn_receipt_headers (
    goal_id               TEXT NOT NULL,
    todo_id               TEXT NOT NULL,
    turn_seq              INTEGER NOT NULL CHECK (typeof(turn_seq) = 'integer' AND turn_seq >= 1),
    attempt               INTEGER NOT NULL CHECK (typeof(attempt) = 'integer' AND attempt >= 1),
    schema_version        TEXT NOT NULL
                          CHECK (length(trim(schema_version)) BETWEEN 1 AND 128),
    input_snapshot_digest TEXT NOT NULL
                          CHECK (
                              length(input_snapshot_digest) = 71
                              AND substr(input_snapshot_digest, 1, 7) = 'sha256:'
                              AND substr(input_snapshot_digest, 8) NOT GLOB '*[^0-9a-f]*'
                          ),
    admission_client_key  TEXT NOT NULL
                          CHECK (length(trim(admission_client_key)) BETWEEN 1 AND 256),
    canonical_digest      TEXT NOT NULL
                          CHECK (
                              length(canonical_digest) = 71
                              AND substr(canonical_digest, 1, 7) = 'sha256:'
                              AND substr(canonical_digest, 8) NOT GLOB '*[^0-9a-f]*'
                          ),
    created_at            DATETIME NOT NULL,
    PRIMARY KEY (goal_id, todo_id, turn_seq),
    UNIQUE (goal_id, todo_id, admission_client_key),
    FOREIGN KEY (goal_id) REFERENCES goals(id),
    FOREIGN KEY (goal_id, todo_id) REFERENCES goal_todos(goal_id, id)
);

-- A receipt cannot get ahead of the Todo CAS watermark.  Replayed headers are
-- intentionally handled by the immutable/idempotent boundary below rather
-- than by inserting a future row and fixing the Todo later.
CREATE TRIGGER turn_receipt_headers_todo_watermark
BEFORE INSERT ON turn_receipt_headers
WHEN NOT EXISTS (
    SELECT 1 FROM turn_receipt_headers h
    WHERE h.goal_id = NEW.goal_id
      AND h.todo_id = NEW.todo_id
      AND h.turn_seq = NEW.turn_seq
)
AND NEW.turn_seq <> (
    SELECT t.last_turn_seq
    FROM goal_todos t
    WHERE t.goal_id = NEW.goal_id
      AND t.id = NEW.todo_id
)
BEGIN
    SELECT RAISE(ABORT, 'turn receipt header must equal todo watermark');
END;

-- A retry of the exact canonical identity is a no-op.  Reusing the identity
-- with another digest is a durable conflict, never an overwrite.
CREATE TRIGGER turn_receipt_headers_idempotency
BEFORE INSERT ON turn_receipt_headers
WHEN EXISTS (
    SELECT 1
    FROM turn_receipt_headers h
    WHERE h.goal_id = NEW.goal_id
      AND h.todo_id = NEW.todo_id
      AND h.turn_seq = NEW.turn_seq
)
BEGIN
    SELECT CASE
        WHEN EXISTS (
            SELECT 1
            FROM turn_receipt_headers h
            WHERE h.goal_id = NEW.goal_id
              AND h.todo_id = NEW.todo_id
              AND h.turn_seq = NEW.turn_seq
              AND h.canonical_digest = NEW.canonical_digest
        ) THEN RAISE(IGNORE)
        ELSE RAISE(ABORT, 'turn receipt header digest conflict')
    END;
END;

CREATE TRIGGER turn_receipt_headers_contiguous
BEFORE INSERT ON turn_receipt_headers
WHEN NOT EXISTS (
    SELECT 1
    FROM turn_receipt_headers h
    WHERE h.goal_id = NEW.goal_id
      AND h.todo_id = NEW.todo_id
      AND h.turn_seq = NEW.turn_seq
)
AND NEW.turn_seq <> COALESCE(
    (
        SELECT MAX(h.turn_seq) + 1
        FROM turn_receipt_headers h
        WHERE h.goal_id = NEW.goal_id
          AND h.todo_id = NEW.todo_id
    ),
    1
)
BEGIN
    SELECT RAISE(ABORT, 'turn receipt headers must be contiguous');
END;

CREATE TRIGGER turn_receipt_headers_immutable_update
BEFORE UPDATE ON turn_receipt_headers
BEGIN
    SELECT RAISE(ABORT, 'turn receipt headers are immutable');
END;

CREATE TRIGGER turn_receipt_headers_immutable_delete
BEFORE DELETE ON turn_receipt_headers
BEGIN
    SELECT RAISE(ABORT, 'turn receipt headers are immutable');
END;

CREATE TABLE turn_receipt_phases (
    goal_id                TEXT NOT NULL,
    todo_id                TEXT NOT NULL,
    turn_seq               INTEGER NOT NULL CHECK (typeof(turn_seq) = 'integer' AND turn_seq >= 1),
    phase_seq              INTEGER NOT NULL CHECK (typeof(phase_seq) = 'integer' AND phase_seq BETWEEN 1 AND 7),
    phase                  TEXT NOT NULL,
    payload                TEXT NOT NULL
                           CHECK (json_valid(payload) = 1 AND json_type(payload) = 'object'),
    canonical_digest       TEXT NOT NULL
                           CHECK (
                               length(canonical_digest) = 71
                               AND substr(canonical_digest, 1, 7) = 'sha256:'
                               AND substr(canonical_digest, 8) NOT GLOB '*[^0-9a-f]*'
                           ),
    plan_id                TEXT
                           CHECK (plan_id IS NULL OR substr(plan_id, 1, 5) = 'plan_')
                           REFERENCES plans(id),
    run_ids                TEXT NOT NULL DEFAULT '[]'
                           CHECK (
                               json_valid(run_ids) = 1
                               AND json_type(run_ids) = 'array'
                               AND json_array_length(run_ids) <= 64
                           ),
    quota_reservation_keys TEXT NOT NULL DEFAULT '[]'
                           CHECK (
                               json_valid(quota_reservation_keys) = 1
                               AND json_type(quota_reservation_keys) = 'array'
                               AND json_array_length(quota_reservation_keys) <= 8
                           ),
    evidence               TEXT NOT NULL DEFAULT '[]'
                           CHECK (
                               json_valid(evidence) = 1
                               AND json_type(evidence) = 'array'
                               AND json_array_length(evidence) <= 128
                           ),
    created_at             DATETIME NOT NULL,
    PRIMARY KEY (goal_id, todo_id, turn_seq, phase_seq),
    FOREIGN KEY (goal_id, todo_id, turn_seq)
        REFERENCES turn_receipt_headers(goal_id, todo_id, turn_seq)
);

CREATE TRIGGER turn_receipt_phases_idempotency
BEFORE INSERT ON turn_receipt_phases
WHEN EXISTS (
    SELECT 1
    FROM turn_receipt_phases p
    WHERE p.goal_id = NEW.goal_id
      AND p.todo_id = NEW.todo_id
      AND p.turn_seq = NEW.turn_seq
      AND p.phase_seq = NEW.phase_seq
)
BEGIN
    SELECT CASE
        WHEN EXISTS (
            SELECT 1
            FROM turn_receipt_phases p
            WHERE p.goal_id = NEW.goal_id
              AND p.todo_id = NEW.todo_id
              AND p.turn_seq = NEW.turn_seq
              AND p.phase_seq = NEW.phase_seq
              AND p.canonical_digest = NEW.canonical_digest
        ) THEN RAISE(IGNORE)
        ELSE RAISE(ABORT, 'turn receipt phase digest conflict')
    END;
END;

CREATE TRIGGER turn_receipt_phases_name_order
BEFORE INSERT ON turn_receipt_phases
WHEN NOT EXISTS (
    SELECT 1
    FROM turn_receipt_phases p
    WHERE p.goal_id = NEW.goal_id
      AND p.todo_id = NEW.todo_id
      AND p.turn_seq = NEW.turn_seq
      AND p.phase_seq = NEW.phase_seq
)
AND NOT (
       (NEW.phase_seq = 1 AND NEW.phase = 'decision_decode')
    OR (NEW.phase_seq = 2 AND NEW.phase = 'validation')
    OR (NEW.phase_seq = 3 AND NEW.phase = 'durable_writeback')
    OR (NEW.phase_seq = 4 AND NEW.phase = 'plan_compile')
    OR (NEW.phase_seq = 5 AND NEW.phase = 'dispatch')
    OR (NEW.phase_seq = 6 AND NEW.phase = 'quota_spend')
    OR (NEW.phase_seq = 7 AND NEW.phase = 'projection_outbox')
)
BEGIN
    SELECT RAISE(ABORT, 'turn receipt phase sequence/name mismatch');
END;

CREATE TRIGGER turn_receipt_phases_semantic_contract
BEFORE INSERT ON turn_receipt_phases
WHEN NOT EXISTS (
    SELECT 1
    FROM turn_receipt_phases p
    WHERE p.goal_id = NEW.goal_id
      AND p.todo_id = NEW.todo_id
      AND p.turn_seq = NEW.turn_seq
      AND p.phase_seq = NEW.phase_seq
)
AND (
       (NEW.phase_seq = 4 AND NOT (
            NEW.plan_id IS NOT NULL
        AND json_extract(NEW.payload, '$.plan_id') = NEW.plan_id
        AND json_type(NEW.payload, '$.plan_client_key') = 'text'
        AND length(trim(json_extract(NEW.payload, '$.plan_client_key'))) BETWEEN 1 AND 256
        AND json_type(NEW.payload, '$.decision_digest') = 'text'
        AND length(json_extract(NEW.payload, '$.decision_digest')) = 71
        AND substr(json_extract(NEW.payload, '$.decision_digest'), 1, 7) = 'sha256:'
        AND substr(json_extract(NEW.payload, '$.decision_digest'), 8) NOT GLOB '*[^0-9a-f]*'
       ))
    OR (NEW.phase_seq = 5 AND NOT (
            NEW.plan_id IS NOT NULL
        AND json_extract(NEW.payload, '$.plan_id') = NEW.plan_id
        AND json_extract(NEW.payload, '$.dispatch_state') IN ('no_runs','committed','failed')
        AND json_type(NEW.payload, '$.run_count') = 'integer'
        AND json_extract(NEW.payload, '$.run_count') = json_array_length(NEW.run_ids)
        AND (json_extract(NEW.payload, '$.dispatch_state') <> 'no_runs'
             OR json_extract(NEW.payload, '$.run_count') = 0)
       ))
)
BEGIN
    SELECT RAISE(ABORT, 'turn receipt phase semantic contract violated');
END;

CREATE TRIGGER turn_receipt_phases_contiguous
BEFORE INSERT ON turn_receipt_phases
WHEN NOT EXISTS (
    SELECT 1
    FROM turn_receipt_phases p
    WHERE p.goal_id = NEW.goal_id
      AND p.todo_id = NEW.todo_id
      AND p.turn_seq = NEW.turn_seq
      AND p.phase_seq = NEW.phase_seq
)
AND NEW.phase_seq <> COALESCE(
    (
        SELECT MAX(p.phase_seq) + 1
        FROM turn_receipt_phases p
        WHERE p.goal_id = NEW.goal_id
          AND p.todo_id = NEW.todo_id
          AND p.turn_seq = NEW.turn_seq
    ),
    1
)
BEGIN
    SELECT RAISE(ABORT, 'turn receipt phases must not skip');
END;

CREATE TRIGGER turn_receipt_phases_immutable_update
BEFORE UPDATE ON turn_receipt_phases
BEGIN
    SELECT RAISE(ABORT, 'turn receipt phases are immutable');
END;

CREATE TRIGGER turn_receipt_phases_immutable_delete
BEFORE DELETE ON turn_receipt_phases
BEGIN
    SELECT RAISE(ABORT, 'turn receipt phases are immutable');
END;
