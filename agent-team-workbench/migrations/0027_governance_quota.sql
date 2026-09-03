-- 0027_governance_quota.sql — immutable quota reservation and spend ledger.
--
-- Goal.quota_policies remains the policy source.  These tables freeze the
-- policy inputs actually admitted for a Turn and record per-Run spend;
-- they do not introduce a second Goal policy aggregate or billing ledger.

CREATE TABLE quota_reservations (
    goal_id             TEXT NOT NULL,
    todo_id             TEXT NOT NULL,
    turn_seq            INTEGER NOT NULL
                        CHECK (typeof(turn_seq) = 'integer' AND turn_seq >= 1),
    quota_kind          TEXT NOT NULL
                        CHECK (quota_kind IN (
                            'turn_count','active_worker','input_tokens_total',
                            'input_uncached_tokens','cache_read_tokens',
                            'cache_write_tokens','output_tokens','cost_microusd')),
    status              TEXT NOT NULL DEFAULT 'reserved'
                        CHECK (status IN ('reserved','committed','released','expired')),
    reserved_amount     INTEGER NOT NULL
                        CHECK (typeof(reserved_amount) = 'integer' AND reserved_amount >= 0),
    committed_amount    INTEGER NOT NULL DEFAULT 0
                        CHECK (typeof(committed_amount) = 'integer' AND committed_amount >= 0),
    released_amount     INTEGER NOT NULL DEFAULT 0
                        CHECK (typeof(released_amount) = 'integer' AND released_amount >= 0),
    policy_limit        INTEGER NOT NULL
                        CHECK (typeof(policy_limit) = 'integer' AND policy_limit >= 0),
    policy_enforcement  TEXT NOT NULL CHECK (policy_enforcement IN ('audit','enforce')),
    policy_digest       TEXT NOT NULL
                        CHECK (
                            length(policy_digest) = 71
                            AND substr(policy_digest, 1, 7) = 'sha256:'
                            AND substr(policy_digest, 8) NOT GLOB '*[^0-9a-f]*'
                        ),
    version             INTEGER NOT NULL DEFAULT 1
                        CHECK (typeof(version) = 'integer' AND version >= 1),
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL,
    PRIMARY KEY (goal_id, todo_id, turn_seq, quota_kind),
    FOREIGN KEY (goal_id, todo_id, turn_seq)
        REFERENCES turn_receipt_headers(goal_id, todo_id, turn_seq),
    CHECK (
        committed_amount <= reserved_amount
        AND released_amount <= reserved_amount
        AND committed_amount <= (9223372036854775807 - released_amount)
        AND committed_amount + released_amount <= reserved_amount
        AND (
            status = 'reserved'
            OR committed_amount + released_amount = reserved_amount
        )
    )
);

CREATE INDEX idx_quota_reservations_goal_status
    ON quota_reservations(goal_id, status, updated_at);

-- A newly admitted reservation always starts in reserved.  Existing-key
-- replay is handled before this guard so a later settlement remains replayable.
CREATE TRIGGER quota_reservations_insert_state
BEFORE INSERT ON quota_reservations
WHEN NEW.status <> 'reserved'
 AND NOT EXISTS (
     SELECT 1 FROM quota_reservations q
     WHERE q.goal_id = NEW.goal_id
       AND q.todo_id = NEW.todo_id
       AND q.turn_seq = NEW.turn_seq
       AND q.quota_kind = NEW.quota_kind
 )
BEGIN
    SELECT RAISE(ABORT, 'quota reservation must start reserved');
END;

-- Replay of the exact reservation intent is a no-op.  Mutable settlement
-- amounts/status are intentionally excluded: a reservation may be replayed
-- after its terminal settlement without reopening or rewriting it.
CREATE TRIGGER quota_reservations_idempotency
BEFORE INSERT ON quota_reservations
WHEN EXISTS (
    SELECT 1 FROM quota_reservations q
    WHERE q.goal_id = NEW.goal_id
      AND q.todo_id = NEW.todo_id
      AND q.turn_seq = NEW.turn_seq
      AND q.quota_kind = NEW.quota_kind
)
BEGIN
    SELECT CASE
        WHEN EXISTS (
            SELECT 1 FROM quota_reservations q
            WHERE q.goal_id = NEW.goal_id
              AND q.todo_id = NEW.todo_id
              AND q.turn_seq = NEW.turn_seq
              AND q.quota_kind = NEW.quota_kind
              AND q.reserved_amount = NEW.reserved_amount
              AND q.policy_limit = NEW.policy_limit
              AND q.policy_enforcement = NEW.policy_enforcement
              AND q.policy_digest = NEW.policy_digest
        ) THEN RAISE(IGNORE)
        ELSE RAISE(ABORT, 'quota reservation intent conflict')
    END;
END;

-- Identity, requested amount and policy are frozen at admission. Per-Run
-- prices are frozen on execution_runs because one Turn may use many models.
CREATE TRIGGER quota_reservations_identity_policy_immutable
BEFORE UPDATE OF goal_id, todo_id, turn_seq, quota_kind, reserved_amount,
                 policy_limit, policy_enforcement, policy_digest
ON quota_reservations
WHEN NOT (
       NEW.goal_id IS OLD.goal_id
   AND NEW.todo_id IS OLD.todo_id
   AND NEW.turn_seq IS OLD.turn_seq
   AND NEW.quota_kind IS OLD.quota_kind
   AND NEW.reserved_amount IS OLD.reserved_amount
   AND NEW.policy_limit IS OLD.policy_limit
   AND NEW.policy_enforcement IS OLD.policy_enforcement
   AND NEW.policy_digest IS OLD.policy_digest
)
BEGIN
    SELECT RAISE(ABORT, 'quota reservation identity/policy is immutable');
END;

-- Settlement updates are one-version CAS writes; terminal rows cannot have
-- their committed/released amounts rewritten.  The state transition guard
-- below prevents terminal->terminal or terminal->reserved reopening.
CREATE TRIGGER quota_reservations_settlement_amount_guard
BEFORE UPDATE OF committed_amount, released_amount ON quota_reservations
WHEN OLD.status <> 'reserved'
 AND (NEW.committed_amount IS NOT OLD.committed_amount
      OR NEW.released_amount IS NOT OLD.released_amount)
BEGIN
    SELECT RAISE(ABORT, 'terminal quota reservation amounts are immutable');
END;

CREATE TRIGGER quota_reservations_amount_monotonic
BEFORE UPDATE OF committed_amount, released_amount ON quota_reservations
WHEN NEW.committed_amount < OLD.committed_amount
 OR NEW.released_amount < OLD.released_amount
BEGIN
    SELECT RAISE(ABORT, 'quota reservation settlement amounts cannot decrease');
END;

CREATE TRIGGER quota_reservations_status_transition
BEFORE UPDATE OF status ON quota_reservations
WHEN NEW.status <> OLD.status
 AND NOT (OLD.status = 'reserved'
          AND NEW.status IN ('committed','released','expired'))
BEGIN
    SELECT RAISE(ABORT, 'illegal quota reservation status transition');
END;

CREATE TRIGGER quota_reservations_version_step
BEFORE UPDATE ON quota_reservations
WHEN NEW.version <> OLD.version + 1
BEGIN
    SELECT RAISE(ABORT, 'quota reservation version must increase by one');
END;

CREATE TABLE quota_spend_entries (
    goal_id       TEXT NOT NULL,
    todo_id       TEXT NOT NULL,
    turn_seq      INTEGER NOT NULL
                  CHECK (typeof(turn_seq) = 'integer' AND turn_seq >= 1),
    quota_kind    TEXT NOT NULL
                  CHECK (quota_kind IN (
                      'input_tokens_total','input_uncached_tokens',
                      'cache_read_tokens','cache_write_tokens','output_tokens',
                      'cost_microusd')),
    run_id        TEXT NOT NULL REFERENCES execution_runs(id),
    amount        INTEGER NOT NULL
                  CHECK (typeof(amount) = 'integer' AND amount >= 0),
    usage_basis   TEXT NOT NULL CHECK (usage_basis = 'per_run'),
    usage_digest  TEXT NOT NULL
                  CHECK (
                      length(usage_digest) = 71
                      AND substr(usage_digest, 1, 7) = 'sha256:'
                      AND substr(usage_digest, 8) NOT GLOB '*[^0-9a-f]*'
                  ),
    policy_digest TEXT NOT NULL
                  CHECK (
                      length(policy_digest) = 71
                      AND substr(policy_digest, 1, 7) = 'sha256:'
                      AND substr(policy_digest, 8) NOT GLOB '*[^0-9a-f]*'
                  ),
    price_digest  TEXT,
    status        TEXT NOT NULL CHECK (status IN ('committed','unresolved')),
    reason        TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 2000),
    created_at    DATETIME NOT NULL,
    PRIMARY KEY (goal_id, todo_id, turn_seq, quota_kind, run_id),
    FOREIGN KEY (goal_id, todo_id, turn_seq, quota_kind)
        REFERENCES quota_reservations(goal_id, todo_id, turn_seq, quota_kind),
    CHECK (
        (status = 'committed')
        OR (status = 'unresolved' AND amount = 0 AND length(trim(reason)) BETWEEN 1 AND 2000)
    ),
    CHECK (
        (quota_kind <> 'cost_microusd' AND price_digest IS NULL)
        OR (
            quota_kind = 'cost_microusd'
            AND price_digest IS NOT NULL
            AND length(price_digest) = 71
            AND substr(price_digest, 1, 7) = 'sha256:'
            AND substr(price_digest, 8) NOT GLOB '*[^0-9a-f]*'
        )
    )
);

CREATE INDEX idx_quota_spend_unresolved
    ON quota_spend_entries(goal_id, quota_kind, created_at)
    WHERE status = 'unresolved';
CREATE INDEX idx_quota_spend_sum
    ON quota_spend_entries(goal_id, quota_kind, status);

-- Spend must use the frozen policy/price snapshot for its reservation.  This
-- also makes direct SQL writers obey the same lineage as the Go repository.
CREATE TRIGGER quota_spend_lineage_guard
BEFORE INSERT ON quota_spend_entries
WHEN NOT EXISTS (
    SELECT 1
    FROM quota_reservations q
    WHERE q.goal_id = NEW.goal_id
      AND q.todo_id = NEW.todo_id
    AND q.turn_seq = NEW.turn_seq
      AND q.quota_kind = NEW.quota_kind
      AND q.status = 'reserved'
      AND q.policy_digest = NEW.policy_digest
	  AND NEW.amount <= q.reserved_amount - COALESCE((
	      SELECT SUM(s.amount)
	        FROM quota_spend_entries s
	       WHERE s.goal_id = NEW.goal_id
	         AND s.todo_id = NEW.todo_id
	         AND s.turn_seq = NEW.turn_seq
	         AND s.quota_kind = NEW.quota_kind
	  ), 0)
	  AND EXISTS (
	      WITH RECURSIVE subtree(id) AS (
	          SELECT root_work_item_id FROM goals WHERE id = NEW.goal_id
	          UNION
	          SELECT child.id
	            FROM work_items child
	            JOIN subtree parent ON parent.id = child.parent_id
	           WHERE child.record_kind = 'task'
	      )
	      SELECT 1
	        FROM execution_runs run
	        JOIN goals goal ON goal.id = NEW.goal_id
	        JOIN subtree item ON item.id = run.work_item_id
	       WHERE run.id = NEW.run_id
	         AND run.workspace_id = goal.workspace_id
	         AND run.status IN ('succeeded','interrupted','cancelled','lost','failed')
	         AND (
	             NEW.quota_kind <> 'cost_microusd'
	             OR json_extract(run.input, '$.price_snapshot.digest') = NEW.price_digest
	         )
	  )
)
AND NOT EXISTS (
    SELECT 1 FROM quota_spend_entries s
    WHERE s.goal_id = NEW.goal_id
      AND s.todo_id = NEW.todo_id
      AND s.turn_seq = NEW.turn_seq
      AND s.quota_kind = NEW.quota_kind
      AND s.run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'quota spend violates reservation lineage or capacity');
END;

-- Same spend digest/semantic payload is an idempotent replay; any changed
-- amount/status/reason or digest is a collision on the append-only key.
CREATE TRIGGER quota_spend_idempotency
BEFORE INSERT ON quota_spend_entries
WHEN EXISTS (
    SELECT 1 FROM quota_spend_entries s
    WHERE s.goal_id = NEW.goal_id
      AND s.todo_id = NEW.todo_id
      AND s.turn_seq = NEW.turn_seq
      AND s.quota_kind = NEW.quota_kind
      AND s.run_id = NEW.run_id
)
BEGIN
    SELECT CASE
        WHEN EXISTS (
            SELECT 1 FROM quota_spend_entries s
            WHERE s.goal_id = NEW.goal_id
              AND s.todo_id = NEW.todo_id
              AND s.turn_seq = NEW.turn_seq
              AND s.quota_kind = NEW.quota_kind
              AND s.run_id = NEW.run_id
              AND s.amount = NEW.amount
              AND s.usage_basis = NEW.usage_basis
              AND s.usage_digest = NEW.usage_digest
              AND s.policy_digest = NEW.policy_digest
              AND s.price_digest IS NEW.price_digest
              AND s.status = NEW.status
              AND s.reason = NEW.reason
        ) THEN RAISE(IGNORE)
        ELSE RAISE(ABORT, 'quota spend digest conflict')
    END;
END;

CREATE TRIGGER quota_spend_append_only_update
BEFORE UPDATE ON quota_spend_entries
BEGIN
    SELECT RAISE(ABORT, 'quota spend entries are append-only');
END;

CREATE TRIGGER quota_spend_append_only_delete
BEFORE DELETE ON quota_spend_entries
BEGIN
    SELECT RAISE(ABORT, 'quota spend entries are append-only');
END;
