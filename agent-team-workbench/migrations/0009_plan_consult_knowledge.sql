-- 0009_plan_consult_knowledge.sql — M2 plan 词汇表扩展（SQLite）。
-- SQLite 的 CHECK 不可
-- ALTER，重建 plan_steps 换宽约束（plan_steps 无下游引用，重建安全）。

CREATE TABLE plan_steps_new (
    plan_id             TEXT NOT NULL REFERENCES plans(id),
    seq                 INTEGER NOT NULL,
    verb                TEXT NOT NULL CHECK (verb IN ('dispatch','defer','finish','consult_knowledge')),
    payload             TEXT NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','executed','skipped','failed')),
    result_work_item_id TEXT,
    result_run_id       TEXT,
    error               TEXT,
    created_at          DATETIME NOT NULL,
    executed_at         DATETIME,
    PRIMARY KEY (plan_id, seq)
);

INSERT INTO plan_steps_new (plan_id, seq, verb, payload, status, result_work_item_id,
                            result_run_id, error, created_at, executed_at)
    SELECT plan_id, seq, verb, payload, status, result_work_item_id,
           result_run_id, error, created_at, executed_at
    FROM plan_steps;

DROP TABLE plan_steps;
ALTER TABLE plan_steps_new RENAME TO plan_steps;
