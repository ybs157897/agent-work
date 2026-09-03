-- 0030_governed_turn_recovery_checkpoint.sql — durable Coordinator admission
-- checkpoint.  The fields are nullable for generic WP1 receipts; a governed
-- admission writes all three before any decision phase or Plan transaction.

ALTER TABLE turn_receipt_headers ADD COLUMN source_run_id TEXT REFERENCES execution_runs(id);
ALTER TABLE turn_receipt_headers ADD COLUMN plan_client_key TEXT;
ALTER TABLE turn_receipt_headers ADD COLUMN decision_digest TEXT;

CREATE UNIQUE INDEX idx_turn_receipt_headers_source_run
    ON turn_receipt_headers(source_run_id)
    WHERE source_run_id IS NOT NULL;

CREATE UNIQUE INDEX idx_turn_receipt_headers_plan_client_key
    ON turn_receipt_headers(goal_id, todo_id, plan_client_key)
    WHERE plan_client_key IS NOT NULL;

-- A governed checkpoint is all-or-nothing.  The deterministic plan client key
-- binds the future Plan identity to this exact TurnKey; the source Run must be
-- the Goal root Run in the same workspace.  Generic receipts retain the
-- pre-0030 nullable shape.
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
)
BEGIN
    SELECT RAISE(ABORT, 'governed turn recovery checkpoint is incomplete or outside the Goal root');
END;

CREATE TRIGGER turn_receipt_headers_recovery_checkpoint_update
BEFORE UPDATE OF source_run_id, plan_client_key, decision_digest
ON turn_receipt_headers
WHEN NOT (
       NEW.source_run_id IS OLD.source_run_id
   AND NEW.plan_client_key IS OLD.plan_client_key
   AND NEW.decision_digest IS OLD.decision_digest
)
BEGIN
    SELECT RAISE(ABORT, 'governed turn recovery checkpoint is immutable');
END;

-- Repository validation is not enough for direct SQLite writers.  A committed
-- spend must equal the immutable canonical usage bucket for its kind; an
-- unresolved entry may carry zero only when the canonical snapshot explicitly
-- marks that kind unresolved or the resolved fact exceeded the frozen
-- reservation (the application records that reason without truncating fact).
CREATE TRIGGER quota_spend_canonical_amount_guard
BEFORE INSERT ON quota_spend_entries
WHEN NOT EXISTS (
    SELECT 1
      FROM execution_runs run
     WHERE run.id = NEW.run_id
       AND run.canonical_usage IS NOT NULL
       AND (
           (NEW.status = 'committed'
            AND EXISTS (
                SELECT 1 FROM json_each(run.canonical_usage, '$.resolved_kinds') k
                 WHERE k.value = NEW.quota_kind
            )
            AND CASE NEW.quota_kind
                WHEN 'input_tokens_total' THEN json_type(run.canonical_usage, '$.counters.input_tokens_total') = 'integer' AND json_extract(run.canonical_usage, '$.counters.input_tokens_total') = NEW.amount
                WHEN 'input_uncached_tokens' THEN json_type(run.canonical_usage, '$.counters.input_uncached_tokens') = 'integer' AND json_extract(run.canonical_usage, '$.counters.input_uncached_tokens') = NEW.amount
                WHEN 'cache_read_tokens' THEN json_type(run.canonical_usage, '$.counters.cache_read_tokens') = 'integer' AND json_extract(run.canonical_usage, '$.counters.cache_read_tokens') = NEW.amount
                WHEN 'cache_write_tokens' THEN json_type(run.canonical_usage, '$.counters.cache_write_tokens') = 'integer' AND json_extract(run.canonical_usage, '$.counters.cache_write_tokens') = NEW.amount
                WHEN 'output_tokens' THEN json_type(run.canonical_usage, '$.counters.output_tokens') = 'integer' AND json_extract(run.canonical_usage, '$.counters.output_tokens') = NEW.amount
                WHEN 'cost_microusd' THEN json_type(run.canonical_usage, '$.cost_microusd') = 'integer' AND json_extract(run.canonical_usage, '$.cost_microusd') = NEW.amount
                ELSE 0
            END
           )
        OR (NEW.status = 'unresolved'
            AND NEW.amount = 0
            AND length(trim(NEW.reason)) BETWEEN 1 AND 2000
            AND (
                EXISTS (
                    SELECT 1 FROM json_each(run.canonical_usage, '$.unresolved_kinds') k
                     WHERE k.value = NEW.quota_kind
                )
            )
            OR (
                EXISTS (
                    SELECT 1
                      FROM quota_reservations reservation
                     WHERE reservation.goal_id = NEW.goal_id
                       AND reservation.todo_id = NEW.todo_id
                       AND reservation.turn_seq = NEW.turn_seq
                       AND reservation.quota_kind = NEW.quota_kind
                       AND reservation.status = 'reserved'
                       AND reservation.reserved_amount >= COALESCE((
                           SELECT SUM(committed.amount)
                             FROM quota_spend_entries committed
                            WHERE committed.goal_id = NEW.goal_id
                              AND committed.todo_id = NEW.todo_id
                              AND committed.turn_seq = NEW.turn_seq
                              AND committed.quota_kind = NEW.quota_kind
                              AND committed.status = 'committed'
                       ), 0)
                       AND (
                           CASE NEW.quota_kind
                               WHEN 'input_tokens_total' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.input_tokens_total') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.input_tokens_total') END
                               WHEN 'input_uncached_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.input_uncached_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.input_uncached_tokens') END
                               WHEN 'cache_read_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.cache_read_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.cache_read_tokens') END
                               WHEN 'cache_write_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.cache_write_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.cache_write_tokens') END
                               WHEN 'output_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.output_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.output_tokens') END
                               WHEN 'cost_microusd' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.cost_microusd') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.cost_microusd') END
                           END
                       ) >= 0
                       AND (
                           CASE NEW.quota_kind
                               WHEN 'input_tokens_total' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.input_tokens_total') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.input_tokens_total') END
                               WHEN 'input_uncached_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.input_uncached_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.input_uncached_tokens') END
                               WHEN 'cache_read_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.cache_read_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.cache_read_tokens') END
                               WHEN 'cache_write_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.cache_write_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.cache_write_tokens') END
                               WHEN 'output_tokens' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.counters.output_tokens') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.counters.output_tokens') END
                               WHEN 'cost_microusd' THEN
                                   CASE WHEN json_type(run.canonical_usage, '$.cost_microusd') = 'integer'
                                        THEN json_extract(run.canonical_usage, '$.cost_microusd') END
                           END
                       ) > reservation.reserved_amount - COALESCE((
                           SELECT SUM(committed.amount)
                             FROM quota_spend_entries committed
                            WHERE committed.goal_id = NEW.goal_id
                              AND committed.todo_id = NEW.todo_id
                              AND committed.turn_seq = NEW.turn_seq
                              AND committed.quota_kind = NEW.quota_kind
                              AND committed.status = 'committed'
                       ), 0)
                )
            )
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
    SELECT RAISE(ABORT, 'quota spend amount must match the Run canonical usage bucket');
END;
