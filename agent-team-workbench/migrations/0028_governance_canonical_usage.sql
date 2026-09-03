-- 0028_governance_canonical_usage.sql — durable usage evidence.
--
-- Legacy usage_in/usage_out/usage_cached/usage_basis remain projections.  A
-- canonical usage snapshot is a separate immutable evidence object on the
-- terminal Run; provider cumulative baselines live on TaskSession and have
-- their own monotonic anchor sequence.

ALTER TABLE execution_runs ADD COLUMN canonical_usage TEXT;
ALTER TABLE execution_runs ADD COLUMN canonical_usage_digest TEXT;
ALTER TABLE execution_runs ADD COLUMN provider_usage_report TEXT;
ALTER TABLE execution_runs ADD COLUMN provider_usage_report_digest TEXT;
ALTER TABLE execution_runs ADD COLUMN provider_usage_report_seq INTEGER NOT NULL DEFAULT 0;

-- NULL/NULL is the legacy or not-yet-set state.  Once present, the snapshot
-- must be a JSON object paired with an exact sha256 digest shape.  Digest
-- equality to the canonicalized JSON is verified by the Go persistence
-- boundary, because SQLite has no RFC8785 canonicalizer.
CREATE TRIGGER execution_runs_canonical_usage_pair_insert
BEFORE INSERT ON execution_runs
WHEN NOT (
       (NEW.canonical_usage IS NULL AND NEW.canonical_usage_digest IS NULL)
    OR (NEW.canonical_usage IS NOT NULL
        AND NEW.canonical_usage_digest IS NOT NULL
        AND json_valid(NEW.canonical_usage) = 1
        AND json_type(NEW.canonical_usage) = 'object'
        AND json_type(NEW.canonical_usage, '$.schema_version') = 'text'
        AND json_extract(NEW.canonical_usage, '$.schema_version') = 'canonical-usage/v1'
        AND json_type(NEW.canonical_usage, '$.run_id') = 'text'
        AND length(trim(json_extract(NEW.canonical_usage, '$.run_id'))) BETWEEN 5 AND 256
        AND substr(json_extract(NEW.canonical_usage, '$.run_id'), 1, 4) = 'run_'
        AND json_extract(NEW.canonical_usage, '$.run_id') = NEW.id
        AND json_type(NEW.canonical_usage, '$.usage_basis') = 'text'
        AND json_extract(NEW.canonical_usage, '$.usage_basis') = 'per_run'
        AND json_type(NEW.canonical_usage, '$.counters') = 'object'
        AND json_type(NEW.canonical_usage, '$.resolved_kinds') = 'array'
        AND json_type(NEW.canonical_usage, '$.unresolved_kinds') = 'array'
        AND json_type(NEW.canonical_usage, '$.provenance') = 'object'
        AND json_type(NEW.canonical_usage, '$.digest') = 'text'
        AND length(json_extract(NEW.canonical_usage, '$.digest')) = 71
        AND substr(json_extract(NEW.canonical_usage, '$.digest'), 1, 7) = 'sha256:'
        AND substr(json_extract(NEW.canonical_usage, '$.digest'), 8) NOT GLOB '*[^0-9a-f]*'
        AND json_extract(NEW.canonical_usage, '$.digest') = NEW.canonical_usage_digest
        AND length(NEW.canonical_usage_digest) = 71
        AND substr(NEW.canonical_usage_digest, 1, 7) = 'sha256:'
        AND substr(NEW.canonical_usage_digest, 8) NOT GLOB '*[^0-9a-f]*')
)
BEGIN
    SELECT RAISE(ABORT, 'execution run canonical usage must be a paired JSON object and digest');
END;

-- A null snapshot can be filled once.  A non-null snapshot is immutable;
-- retrying the exact JSON+digest is a no-op at the repository layer and is
-- the only permitted update shape.
CREATE TRIGGER execution_runs_canonical_usage_immutable
BEFORE UPDATE OF canonical_usage, canonical_usage_digest ON execution_runs
WHEN (
       NOT (
           (NEW.canonical_usage IS NULL AND NEW.canonical_usage_digest IS NULL)
        OR (NEW.canonical_usage IS NOT NULL
            AND NEW.canonical_usage_digest IS NOT NULL
            AND json_valid(NEW.canonical_usage) = 1
            AND json_type(NEW.canonical_usage) = 'object'
            AND json_type(NEW.canonical_usage, '$.schema_version') = 'text'
            AND json_extract(NEW.canonical_usage, '$.schema_version') = 'canonical-usage/v1'
            AND json_type(NEW.canonical_usage, '$.run_id') = 'text'
            AND length(trim(json_extract(NEW.canonical_usage, '$.run_id'))) BETWEEN 5 AND 256
            AND substr(json_extract(NEW.canonical_usage, '$.run_id'), 1, 4) = 'run_'
            AND json_extract(NEW.canonical_usage, '$.run_id') = NEW.id
            AND json_type(NEW.canonical_usage, '$.usage_basis') = 'text'
            AND json_extract(NEW.canonical_usage, '$.usage_basis') = 'per_run'
            AND json_type(NEW.canonical_usage, '$.counters') = 'object'
            AND json_type(NEW.canonical_usage, '$.resolved_kinds') = 'array'
            AND json_type(NEW.canonical_usage, '$.unresolved_kinds') = 'array'
            AND json_type(NEW.canonical_usage, '$.provenance') = 'object'
            AND json_type(NEW.canonical_usage, '$.digest') = 'text'
            AND length(json_extract(NEW.canonical_usage, '$.digest')) = 71
            AND substr(json_extract(NEW.canonical_usage, '$.digest'), 1, 7) = 'sha256:'
            AND substr(json_extract(NEW.canonical_usage, '$.digest'), 8) NOT GLOB '*[^0-9a-f]*'
            AND json_extract(NEW.canonical_usage, '$.digest') = NEW.canonical_usage_digest
            AND length(NEW.canonical_usage_digest) = 71
            AND substr(NEW.canonical_usage_digest, 1, 7) = 'sha256:'
            AND substr(NEW.canonical_usage_digest, 8) NOT GLOB '*[^0-9a-f]*')
       )
    OR (OLD.canonical_usage IS NOT NULL
        AND NOT (NEW.canonical_usage IS OLD.canonical_usage
                 AND NEW.canonical_usage_digest IS OLD.canonical_usage_digest))
)
BEGIN
    SELECT RAISE(ABORT, 'execution run canonical usage is immutable after first write');
END;

-- Provider reports are the mutable latest observation used before terminal
-- canonicalization.  They are independently versioned so repeated progress
-- callbacks cannot regress a newer report or silently replace its digest.
CREATE TRIGGER execution_runs_provider_usage_report_pair_insert
BEFORE INSERT ON execution_runs
WHEN NOT (
       (NEW.provider_usage_report IS NULL
        AND NEW.provider_usage_report_digest IS NULL
        AND typeof(NEW.provider_usage_report_seq) = 'integer'
        AND NEW.provider_usage_report_seq = 0)
    OR (NEW.provider_usage_report IS NOT NULL
        AND NEW.provider_usage_report_digest IS NOT NULL
        AND json_valid(NEW.provider_usage_report) = 1
        AND json_type(NEW.provider_usage_report) = 'object'
        AND json_type(NEW.provider_usage_report, '$.schema_version') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.schema_version') = 'provider-usage/v1'
        AND json_type(NEW.provider_usage_report, '$.run_id') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.run_id') = NEW.id
        AND json_type(NEW.provider_usage_report, '$.basis') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.basis') IN ('per_run', 'session_cumulative')
        AND json_type(NEW.provider_usage_report, '$.counters') = 'object'
        AND json_type(NEW.provider_usage_report, '$.provenance') = 'object'
        AND json_type(NEW.provider_usage_report, '$.digest') = 'text'
        AND length(json_extract(NEW.provider_usage_report, '$.digest')) = 71
        AND substr(json_extract(NEW.provider_usage_report, '$.digest'), 1, 7) = 'sha256:'
        AND substr(json_extract(NEW.provider_usage_report, '$.digest'), 8) NOT GLOB '*[^0-9a-f]*'
        AND json_extract(NEW.provider_usage_report, '$.digest') = NEW.provider_usage_report_digest
        AND typeof(NEW.provider_usage_report_seq) = 'integer'
        AND NEW.provider_usage_report_seq >= 1)
)
BEGIN
    SELECT RAISE(ABORT, 'execution run provider usage report must be a paired JSON object, digest and sequence');
END;

CREATE TRIGGER execution_runs_provider_usage_report_update
BEFORE UPDATE OF provider_usage_report, provider_usage_report_digest,
                 provider_usage_report_seq ON execution_runs
WHEN NOT (
       -- Preserve an absent report exactly.
       (OLD.provider_usage_report IS NULL
        AND OLD.provider_usage_report_seq = 0
        AND NEW.provider_usage_report IS NULL
        AND NEW.provider_usage_report_digest IS NULL
        AND NEW.provider_usage_report_seq = 0)
       -- First observation starts at sequence one.
    OR (OLD.provider_usage_report IS NULL
        AND OLD.provider_usage_report_seq = 0
        AND NEW.provider_usage_report IS NOT NULL
        AND json_valid(NEW.provider_usage_report) = 1
        AND json_type(NEW.provider_usage_report) = 'object'
        AND json_type(NEW.provider_usage_report, '$.schema_version') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.schema_version') = 'provider-usage/v1'
        AND json_type(NEW.provider_usage_report, '$.run_id') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.run_id') = NEW.id
        AND json_type(NEW.provider_usage_report, '$.basis') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.basis') IN ('per_run', 'session_cumulative')
        AND json_type(NEW.provider_usage_report, '$.counters') = 'object'
        AND json_type(NEW.provider_usage_report, '$.provenance') = 'object'
        AND json_type(NEW.provider_usage_report, '$.digest') = 'text'
        AND length(json_extract(NEW.provider_usage_report, '$.digest')) = 71
        AND substr(json_extract(NEW.provider_usage_report, '$.digest'), 1, 7) = 'sha256:'
        AND substr(json_extract(NEW.provider_usage_report, '$.digest'), 8) NOT GLOB '*[^0-9a-f]*'
        AND json_extract(NEW.provider_usage_report, '$.digest') = NEW.provider_usage_report_digest
        AND NEW.provider_usage_report_seq = 1)
       -- Exact report replay keeps its sequence and digest.
    OR (OLD.provider_usage_report IS NOT NULL
        AND NEW.provider_usage_report IS OLD.provider_usage_report
        AND NEW.provider_usage_report_digest IS OLD.provider_usage_report_digest
        AND NEW.provider_usage_report_seq = OLD.provider_usage_report_seq)
       -- A changed latest report advances exactly one sequence.
    OR (OLD.provider_usage_report IS NOT NULL
        AND NEW.provider_usage_report IS NOT NULL
        AND json_valid(NEW.provider_usage_report) = 1
        AND json_type(NEW.provider_usage_report) = 'object'
        AND json_type(NEW.provider_usage_report, '$.schema_version') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.schema_version') = 'provider-usage/v1'
        AND json_type(NEW.provider_usage_report, '$.run_id') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.run_id') = NEW.id
        AND json_type(NEW.provider_usage_report, '$.basis') = 'text'
        AND json_extract(NEW.provider_usage_report, '$.basis') IN ('per_run', 'session_cumulative')
        AND json_type(NEW.provider_usage_report, '$.counters') = 'object'
        AND json_type(NEW.provider_usage_report, '$.provenance') = 'object'
        AND json_type(NEW.provider_usage_report, '$.digest') = 'text'
        AND length(json_extract(NEW.provider_usage_report, '$.digest')) = 71
        AND substr(json_extract(NEW.provider_usage_report, '$.digest'), 1, 7) = 'sha256:'
        AND substr(json_extract(NEW.provider_usage_report, '$.digest'), 8) NOT GLOB '*[^0-9a-f]*'
        AND json_extract(NEW.provider_usage_report, '$.digest') = NEW.provider_usage_report_digest
        AND NEW.provider_usage_report_seq = OLD.provider_usage_report_seq + 1
        AND NOT (OLD.canonical_usage IS NOT NULL
                 AND NOT (NEW.provider_usage_report IS OLD.provider_usage_report
                          AND NEW.provider_usage_report_digest IS OLD.provider_usage_report_digest
                          AND NEW.provider_usage_report_seq = OLD.provider_usage_report_seq)))
)
BEGIN
    SELECT RAISE(ABORT, 'execution run provider usage report sequence or canonicalization state is invalid');
END;

ALTER TABLE task_sessions ADD COLUMN provider_usage_anchor TEXT;
ALTER TABLE task_sessions ADD COLUMN provider_usage_anchor_seq INTEGER NOT NULL DEFAULT 0;

-- A session without a provider baseline has seq=0.  The first baseline is
-- seq=1; later baselines advance exactly one sequence.  The object itself is
-- validated by the typed Go anchor, while SQLite protects pairing and
-- monotonicity for direct writers.
CREATE TRIGGER task_sessions_provider_usage_anchor_insert
BEFORE INSERT ON task_sessions
WHEN NOT (
       (NEW.provider_usage_anchor IS NULL
        AND typeof(NEW.provider_usage_anchor_seq) = 'integer'
        AND NEW.provider_usage_anchor_seq = 0)
    OR (NEW.provider_usage_anchor IS NOT NULL
        AND json_valid(NEW.provider_usage_anchor) = 1
        AND json_type(NEW.provider_usage_anchor) = 'object'
        AND json_type(NEW.provider_usage_anchor, '$.schema_version') = 'text'
        AND json_extract(NEW.provider_usage_anchor, '$.schema_version') = 'provider-usage-anchor/v1'
        AND json_type(NEW.provider_usage_anchor, '$.state') = 'text'
        AND json_extract(NEW.provider_usage_anchor, '$.state') IN ('ready', 'invalidated')
        AND json_type(NEW.provider_usage_anchor, '$.adapter_id') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.adapter_id'))) BETWEEN 1 AND 512
        AND json_type(NEW.provider_usage_anchor, '$.context_generation') = 'integer'
        AND json_extract(NEW.provider_usage_anchor, '$.context_generation') >= 0
        AND json_type(NEW.provider_usage_anchor, '$.segment_seq') = 'integer'
        AND json_extract(NEW.provider_usage_anchor, '$.segment_seq') >= 1
        AND json_type(NEW.provider_usage_anchor, '$.counters') = 'object'
        AND json_type(NEW.provider_usage_anchor, '$.source_run_id') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.source_run_id'))) BETWEEN 5 AND 256
        AND substr(json_extract(NEW.provider_usage_anchor, '$.source_run_id'), 1, 4) = 'run_'
        AND json_type(NEW.provider_usage_anchor, '$.observed_at') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.observed_at'))) BETWEEN 1 AND 128
        AND (
            (json_extract(NEW.provider_usage_anchor, '$.state') = 'ready'
             AND json_type(NEW.provider_usage_anchor, '$.session_ref') = 'text'
             AND length(trim(json_extract(NEW.provider_usage_anchor, '$.session_ref'))) BETWEEN 1 AND 1024
             AND json_type(NEW.provider_usage_anchor, '$.invalidation_reason') IS NULL
             AND (
                 json_type(NEW.provider_usage_anchor, '$.counters.input_tokens_total') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.input_uncached_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.cache_read_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.cache_write_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.output_tokens') IS NOT NULL
             ))
         OR (json_extract(NEW.provider_usage_anchor, '$.state') = 'invalidated'
             AND json_type(NEW.provider_usage_anchor, '$.invalidation_reason') = 'text'
             AND length(trim(json_extract(NEW.provider_usage_anchor, '$.invalidation_reason'))) BETWEEN 1 AND 2000
             AND json_type(NEW.provider_usage_anchor, '$.counters.input_tokens_total') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.input_uncached_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.cache_read_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.cache_write_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.output_tokens') IS NULL)
        )
        AND typeof(NEW.provider_usage_anchor_seq) = 'integer'
        AND NEW.provider_usage_anchor_seq >= 1)
)
BEGIN
    SELECT RAISE(ABORT, 'task session provider usage anchor/sequence pairing is invalid');
END;

CREATE TRIGGER task_sessions_provider_usage_anchor_update
BEFORE UPDATE OF provider_usage_anchor, provider_usage_anchor_seq ON task_sessions
WHEN NOT (
       -- Preserve an absent anchor exactly.
       (OLD.provider_usage_anchor IS NULL
        AND OLD.provider_usage_anchor_seq = 0
        AND NEW.provider_usage_anchor IS NULL
        AND NEW.provider_usage_anchor_seq = 0)
       -- Install the first typed anchor at sequence one.
    OR (OLD.provider_usage_anchor IS NULL
        AND OLD.provider_usage_anchor_seq = 0
        AND NEW.provider_usage_anchor IS NOT NULL
        AND json_valid(NEW.provider_usage_anchor) = 1
        AND json_type(NEW.provider_usage_anchor) = 'object'
        AND json_type(NEW.provider_usage_anchor, '$.schema_version') = 'text'
        AND json_extract(NEW.provider_usage_anchor, '$.schema_version') = 'provider-usage-anchor/v1'
        AND json_type(NEW.provider_usage_anchor, '$.state') = 'text'
        AND json_extract(NEW.provider_usage_anchor, '$.state') IN ('ready', 'invalidated')
        AND json_type(NEW.provider_usage_anchor, '$.adapter_id') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.adapter_id'))) BETWEEN 1 AND 512
        AND json_type(NEW.provider_usage_anchor, '$.context_generation') = 'integer'
        AND json_extract(NEW.provider_usage_anchor, '$.context_generation') >= 0
        AND json_type(NEW.provider_usage_anchor, '$.segment_seq') = 'integer'
        AND json_extract(NEW.provider_usage_anchor, '$.segment_seq') >= 1
        AND json_type(NEW.provider_usage_anchor, '$.counters') = 'object'
        AND json_type(NEW.provider_usage_anchor, '$.source_run_id') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.source_run_id'))) BETWEEN 5 AND 256
        AND substr(json_extract(NEW.provider_usage_anchor, '$.source_run_id'), 1, 4) = 'run_'
        AND json_type(NEW.provider_usage_anchor, '$.observed_at') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.observed_at'))) BETWEEN 1 AND 128
        AND (
            (json_extract(NEW.provider_usage_anchor, '$.state') = 'ready'
             AND json_type(NEW.provider_usage_anchor, '$.session_ref') = 'text'
             AND length(trim(json_extract(NEW.provider_usage_anchor, '$.session_ref'))) BETWEEN 1 AND 1024
             AND json_type(NEW.provider_usage_anchor, '$.invalidation_reason') IS NULL
             AND (
                 json_type(NEW.provider_usage_anchor, '$.counters.input_tokens_total') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.input_uncached_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.cache_read_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.cache_write_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.output_tokens') IS NOT NULL
             ))
         OR (json_extract(NEW.provider_usage_anchor, '$.state') = 'invalidated'
             AND json_type(NEW.provider_usage_anchor, '$.invalidation_reason') = 'text'
             AND length(trim(json_extract(NEW.provider_usage_anchor, '$.invalidation_reason'))) BETWEEN 1 AND 2000
             AND json_type(NEW.provider_usage_anchor, '$.counters.input_tokens_total') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.input_uncached_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.cache_read_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.cache_write_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.output_tokens') IS NULL)
        )
        AND NEW.provider_usage_anchor_seq = 1)
       -- Exact replay preserves both object and sequence.
    OR (OLD.provider_usage_anchor IS NOT NULL
        AND NEW.provider_usage_anchor IS OLD.provider_usage_anchor
        AND NEW.provider_usage_anchor_seq = OLD.provider_usage_anchor_seq)
       -- A new baseline advances exactly one sequence.
    OR (OLD.provider_usage_anchor IS NOT NULL
        AND NEW.provider_usage_anchor IS NOT NULL
        AND json_valid(NEW.provider_usage_anchor) = 1
        AND json_type(NEW.provider_usage_anchor) = 'object'
        AND json_type(NEW.provider_usage_anchor, '$.schema_version') = 'text'
        AND json_extract(NEW.provider_usage_anchor, '$.schema_version') = 'provider-usage-anchor/v1'
        AND json_type(NEW.provider_usage_anchor, '$.state') = 'text'
        AND json_extract(NEW.provider_usage_anchor, '$.state') IN ('ready', 'invalidated')
        AND json_type(NEW.provider_usage_anchor, '$.adapter_id') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.adapter_id'))) BETWEEN 1 AND 512
        AND json_type(NEW.provider_usage_anchor, '$.context_generation') = 'integer'
        AND json_extract(NEW.provider_usage_anchor, '$.context_generation') >= 0
        AND json_type(NEW.provider_usage_anchor, '$.segment_seq') = 'integer'
        AND json_extract(NEW.provider_usage_anchor, '$.segment_seq') >= 1
        AND json_type(NEW.provider_usage_anchor, '$.counters') = 'object'
        AND json_type(NEW.provider_usage_anchor, '$.source_run_id') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.source_run_id'))) BETWEEN 5 AND 256
        AND substr(json_extract(NEW.provider_usage_anchor, '$.source_run_id'), 1, 4) = 'run_'
        AND json_type(NEW.provider_usage_anchor, '$.observed_at') = 'text'
        AND length(trim(json_extract(NEW.provider_usage_anchor, '$.observed_at'))) BETWEEN 1 AND 128
        AND (
            (json_extract(NEW.provider_usage_anchor, '$.state') = 'ready'
             AND json_type(NEW.provider_usage_anchor, '$.session_ref') = 'text'
             AND length(trim(json_extract(NEW.provider_usage_anchor, '$.session_ref'))) BETWEEN 1 AND 1024
             AND json_type(NEW.provider_usage_anchor, '$.invalidation_reason') IS NULL
             AND (
                 json_type(NEW.provider_usage_anchor, '$.counters.input_tokens_total') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.input_uncached_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.cache_read_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.cache_write_tokens') IS NOT NULL
              OR json_type(NEW.provider_usage_anchor, '$.counters.output_tokens') IS NOT NULL
             ))
         OR (json_extract(NEW.provider_usage_anchor, '$.state') = 'invalidated'
             AND json_type(NEW.provider_usage_anchor, '$.invalidation_reason') = 'text'
             AND length(trim(json_extract(NEW.provider_usage_anchor, '$.invalidation_reason'))) BETWEEN 1 AND 2000
             AND json_type(NEW.provider_usage_anchor, '$.counters.input_tokens_total') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.input_uncached_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.cache_read_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.cache_write_tokens') IS NULL
             AND json_type(NEW.provider_usage_anchor, '$.counters.output_tokens') IS NULL)
        )
        AND NEW.provider_usage_anchor_seq = OLD.provider_usage_anchor_seq + 1)
)
BEGIN
    SELECT RAISE(ABORT, 'task session provider usage anchor sequence must advance monotonically');
END;

-- A spend entry is evidence for one terminal Run, not an arbitrary digest
-- supplied by the caller.  The Run must belong to the Goal root Task subtree
-- and its immutable canonical snapshot digest must be the usage_digest.  A
-- previously appended key is exempt so replay remains idempotent after the
-- reservation has been settled.
CREATE TRIGGER quota_spend_canonical_usage_digest_guard
BEFORE INSERT ON quota_spend_entries
WHEN NOT EXISTS (
    WITH RECURSIVE subtree(id) AS (
        SELECT g.root_work_item_id
          FROM goals g
         WHERE g.id = NEW.goal_id
        UNION
        SELECT child.id
          FROM work_items child
          JOIN subtree parent ON parent.id = child.parent_id
         WHERE child.record_kind = 'task'
    )
    SELECT 1
      FROM execution_runs run
      JOIN subtree item ON item.id = run.work_item_id
      JOIN goals goal ON goal.id = NEW.goal_id
     WHERE run.id = NEW.run_id
       AND run.workspace_id = goal.workspace_id
       AND run.status IN ('succeeded','interrupted','cancelled','lost','failed')
       AND run.canonical_usage IS NOT NULL
       AND run.canonical_usage_digest = NEW.usage_digest
       AND (
           (
               json_type(run.input, '$.governance.goal_id') = 'text'
               AND json_extract(run.input, '$.governance.goal_id') = NEW.goal_id
               AND json_type(run.input, '$.governance.todo_id') = 'text'
               AND json_extract(run.input, '$.governance.todo_id') = NEW.todo_id
               AND json_type(run.input, '$.governance.turn_seq') = 'integer'
               AND json_extract(run.input, '$.governance.turn_seq') = NEW.turn_seq
           )
           OR EXISTS (
               SELECT 1
                 FROM turn_receipt_phases phase
                WHERE phase.goal_id = NEW.goal_id
                  AND phase.todo_id = NEW.todo_id
                  AND phase.turn_seq = NEW.turn_seq
                  AND phase.phase_seq = 1
                  AND json_extract(phase.payload, '$.source_run_id') = run.id
           )
       )
)
AND NOT EXISTS (
    SELECT 1
      FROM quota_spend_entries existing
     WHERE existing.goal_id = NEW.goal_id
       AND existing.todo_id = NEW.todo_id
       AND existing.turn_seq = NEW.turn_seq
       AND existing.quota_kind = NEW.quota_kind
       AND existing.run_id = NEW.run_id
)
BEGIN
    SELECT RAISE(ABORT, 'quota spend usage digest must match terminal in-scope Run canonical usage');
END;
