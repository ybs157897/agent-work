-- 0042_governance_control_receipt_phases.sql — permit complete no-Plan control
-- outcomes while preserving the Plan lineage contract for execution receipts.

DROP TRIGGER turn_receipt_phases_semantic_contract;
CREATE TRIGGER turn_receipt_phases_semantic_contract
BEFORE INSERT ON turn_receipt_phases
WHEN NOT EXISTS (
    SELECT 1 FROM turn_receipt_phases p
    WHERE p.goal_id = NEW.goal_id AND p.todo_id = NEW.todo_id
      AND p.turn_seq = NEW.turn_seq AND p.phase_seq = NEW.phase_seq
)
AND (
       (NEW.phase_seq = 4 AND NOT (
            (json_extract(NEW.payload, '$.control_outcome') = 1)
         OR (NEW.plan_id IS NOT NULL
             AND json_extract(NEW.payload, '$.plan_id') = NEW.plan_id
             AND json_type(NEW.payload, '$.plan_client_key') = 'text'
             AND length(trim(json_extract(NEW.payload, '$.plan_client_key'))) BETWEEN 1 AND 256
             AND json_type(NEW.payload, '$.decision_digest') = 'text'
             AND length(json_extract(NEW.payload, '$.decision_digest')) = 71
             AND substr(json_extract(NEW.payload, '$.decision_digest'), 1, 7) = 'sha256:'
             AND substr(json_extract(NEW.payload, '$.decision_digest'), 8) NOT GLOB '*[^0-9a-f]*')
       ))
    OR (NEW.phase_seq = 5 AND NOT (
            (json_extract(NEW.payload, '$.control_outcome') = 1
             AND json_extract(NEW.payload, '$.dispatch_state') = 'no_runs'
             AND json_type(NEW.payload, '$.run_count') = 'integer'
             AND json_extract(NEW.payload, '$.run_count') = 0
             AND json_array_length(NEW.run_ids) = 0)
         OR (NEW.plan_id IS NOT NULL
             AND json_extract(NEW.payload, '$.plan_id') = NEW.plan_id
             AND json_extract(NEW.payload, '$.dispatch_state') IN ('no_runs','committed','failed')
             AND json_type(NEW.payload, '$.run_count') = 'integer'
             AND json_extract(NEW.payload, '$.run_count') = json_array_length(NEW.run_ids)
             AND (json_extract(NEW.payload, '$.dispatch_state') <> 'no_runs'
                  OR json_extract(NEW.payload, '$.run_count') = 0))
       ))
)
BEGIN
    SELECT RAISE(ABORT, 'turn receipt phase semantic contract violated');
END;

DROP TRIGGER turn_receipt_plan_phase_governance_lineage;
CREATE TRIGGER turn_receipt_plan_phase_governance_lineage
BEFORE INSERT ON turn_receipt_phases
WHEN NEW.phase_seq IN (4, 5)
 AND json_extract(NEW.payload, '$.control_outcome') IS NOT 1
 AND NOT EXISTS (
    SELECT 1
    FROM plans p
    WHERE p.id = NEW.plan_id
      AND p.goal_id = NEW.goal_id
      AND p.todo_id = NEW.todo_id
      AND p.turn_seq = NEW.turn_seq
 )
BEGIN
    SELECT RAISE(ABORT, 'turn receipt Plan phase requires matching governance Plan identity');
END;
