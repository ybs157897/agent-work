-- 0033_governance_quota_gap_resolution.sql — immutable manual reconciliation
-- of one already-recorded unresolved quota spend.
--
-- quota_spend_entries remains the usage fact ledger and is never updated by
-- this table. A resolution is a separately auditable, additive adjudication
-- whose evidence and amount are revalidated by the application before it is
-- admitted. v1 intentionally has no waiver outcome.

CREATE TABLE governance_quota_gap_resolutions (
    id                     TEXT NOT NULL PRIMARY KEY
                           CHECK (substr(id, 1, 5) = 'qgap_' AND length(id) > 5),
    schema_version         TEXT NOT NULL DEFAULT 'quota-gap-resolution/v1'
                           CHECK (schema_version = 'quota-gap-resolution/v1'),
    goal_id                TEXT NOT NULL,
    todo_id                TEXT NOT NULL,
    turn_seq               INTEGER NOT NULL
                           CHECK (typeof(turn_seq) = 'integer' AND turn_seq >= 1),
    quota_kind             TEXT NOT NULL
                           CHECK (quota_kind IN (
                               'input_tokens_total','input_uncached_tokens',
                               'cache_read_tokens','cache_write_tokens',
                               'output_tokens','cost_microusd')),
    run_id                 TEXT NOT NULL REFERENCES execution_runs(id),
    original_usage_digest  TEXT NOT NULL
                           CHECK (length(original_usage_digest) = 71
                              AND substr(original_usage_digest, 1, 7) = 'sha256:'
                              AND substr(original_usage_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    original_policy_digest TEXT NOT NULL
                           CHECK (length(original_policy_digest) = 71
                              AND substr(original_policy_digest, 1, 7) = 'sha256:'
                              AND substr(original_policy_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    original_price_digest  TEXT,
    status                 TEXT NOT NULL DEFAULT 'reconciled'
                           CHECK (status = 'reconciled'),
    amount                 INTEGER NOT NULL
                           CHECK (typeof(amount) = 'integer' AND amount >= 0),
    evidence               TEXT NOT NULL
                           CHECK (json_valid(evidence) = 1
                              AND json_type(evidence) = 'object'
                              AND json_type(evidence, '$.source_kind') = 'text'
                              AND json_type(evidence, '$.source_id') = 'text'
                              AND json_type(evidence, '$.verification') = 'text'
                              AND json_extract(evidence, '$.verification') IN ('passed','accepted')
                              AND json_type(evidence, '$.summary') = 'text'
                              AND json_type(evidence, '$.recorded_at') = 'text'),
    evidence_digest        TEXT NOT NULL
                           CHECK (length(evidence_digest) = 71
                              AND substr(evidence_digest, 1, 7) = 'sha256:'
                              AND substr(evidence_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    canonical_digest       TEXT NOT NULL
                           CHECK (length(canonical_digest) = 71
                              AND substr(canonical_digest, 1, 7) = 'sha256:'
                              AND substr(canonical_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    actor_kind             TEXT NOT NULL CHECK (actor_kind = 'user'),
    actor_id               TEXT NOT NULL CHECK (length(trim(actor_id)) BETWEEN 1 AND 256),
    reason                 TEXT NOT NULL CHECK (length(trim(reason)) BETWEEN 1 AND 4000),
    client_key             TEXT,
    created_at             DATETIME NOT NULL,
    FOREIGN KEY (goal_id, todo_id, turn_seq, quota_kind, run_id)
        REFERENCES quota_spend_entries(goal_id, todo_id, turn_seq, quota_kind, run_id),
    CHECK (quota_kind <> 'cost_microusd' OR (
        original_price_digest IS NOT NULL
        AND length(original_price_digest) = 71
        AND substr(original_price_digest, 1, 7) = 'sha256:'
        AND substr(original_price_digest, 8) NOT GLOB '*[^0-9a-f]*')),
    CHECK (quota_kind = 'cost_microusd' OR original_price_digest IS NULL),
    CHECK (client_key IS NULL OR (client_key = trim(client_key)
        AND length(trim(client_key)) BETWEEN 1 AND 256))
);

CREATE UNIQUE INDEX idx_governance_quota_gap_resolutions_target
    ON governance_quota_gap_resolutions(goal_id, todo_id, turn_seq, quota_kind, run_id);

CREATE UNIQUE INDEX idx_governance_quota_gap_resolutions_client_key
    ON governance_quota_gap_resolutions(goal_id, todo_id, client_key)
    WHERE client_key IS NOT NULL;

CREATE INDEX idx_governance_quota_gap_resolutions_goal
    ON governance_quota_gap_resolutions(goal_id, created_at, id);

-- A resolution can only be appended for the exact unresolved spend and must
-- copy its immutable digests. The application additionally verifies evidence
-- status/freshness and Goal/Todo/source scope.
CREATE TRIGGER governance_quota_gap_resolution_target_guard
BEFORE INSERT ON governance_quota_gap_resolutions
WHEN NOT EXISTS (
    SELECT 1
      FROM quota_spend_entries spend
     WHERE spend.goal_id = NEW.goal_id
       AND spend.todo_id = NEW.todo_id
       AND spend.turn_seq = NEW.turn_seq
       AND spend.quota_kind = NEW.quota_kind
       AND spend.run_id = NEW.run_id
       AND spend.status = 'unresolved'
       AND spend.usage_digest = NEW.original_usage_digest
       AND spend.policy_digest = NEW.original_policy_digest
       AND spend.price_digest IS NEW.original_price_digest
)
BEGIN
    SELECT RAISE(ABORT, 'quota gap resolution target is not an unresolved spend');
END;

CREATE TRIGGER governance_quota_gap_resolution_identity_immutable
BEFORE UPDATE ON governance_quota_gap_resolutions
WHEN NOT (
       NEW.id IS OLD.id
   AND NEW.schema_version IS OLD.schema_version
   AND NEW.goal_id IS OLD.goal_id
   AND NEW.todo_id IS OLD.todo_id
   AND NEW.turn_seq IS OLD.turn_seq
   AND NEW.quota_kind IS OLD.quota_kind
   AND NEW.run_id IS OLD.run_id
   AND NEW.original_usage_digest IS OLD.original_usage_digest
   AND NEW.original_policy_digest IS OLD.original_policy_digest
   AND NEW.original_price_digest IS OLD.original_price_digest
   AND NEW.status IS OLD.status
   AND NEW.amount IS OLD.amount
   AND NEW.evidence IS OLD.evidence
   AND NEW.evidence_digest IS OLD.evidence_digest
   AND NEW.canonical_digest IS OLD.canonical_digest
   AND NEW.actor_kind IS OLD.actor_kind
   AND NEW.actor_id IS OLD.actor_id
   AND NEW.reason IS OLD.reason
   AND NEW.client_key IS OLD.client_key
   AND NEW.created_at IS OLD.created_at
)
BEGIN
    SELECT RAISE(ABORT, 'quota gap resolutions are immutable');
END;

CREATE TRIGGER governance_quota_gap_resolution_append_only_delete
BEFORE DELETE ON governance_quota_gap_resolutions
BEGIN
    SELECT RAISE(ABORT, 'quota gap resolutions are append-only');
END;
