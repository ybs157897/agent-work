-- 0010_plan_join_guardrails_sqlite.sql — M4 编排层（SQLite 本地验证版）。
-- 与 migrations/0010_plan_join_guardrails.sql 语义等价；SQLite 的 CHECK 与
-- NOT NULL 不可 ALTER，重建 plan_steps / approvals 换宽约束（两表均无下游引用，
-- 重建安全）。

ALTER TABLE plans ADD COLUMN guardrails TEXT NOT NULL DEFAULT '{}';
ALTER TABLE plans ADD COLUMN error TEXT;

CREATE TABLE plan_steps_new (
    plan_id             TEXT NOT NULL REFERENCES plans(id),
    seq                 INTEGER NOT NULL,
    verb                TEXT NOT NULL CHECK (verb IN ('dispatch','defer','finish','consult_knowledge','join')),
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

-- approvals.run_id 放宽为可空（plan_dispatch 闸门审批无 run；FK 保留）。
CREATE TABLE approvals_new (
    id                  TEXT PRIMARY KEY,
    run_id              TEXT REFERENCES execution_runs(id),
    work_item_id        TEXT NOT NULL REFERENCES work_items(id),
    kind                TEXT NOT NULL,
    risk                TEXT NOT NULL CHECK (risk IN ('low','medium','high')),
    status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired')),
    summary             TEXT NOT NULL,
    requested_by        TEXT NOT NULL DEFAULT '{}',
    sensitive_input_ref TEXT,
    policy_snapshot_id  TEXT,
    expires_at          DATETIME,
    resolved_at         DATETIME,
    resolved_by         TEXT,
    resolve_reason      TEXT,
    created_at          DATETIME NOT NULL
);

INSERT INTO approvals_new (id, run_id, work_item_id, kind, risk, status, summary,
                           requested_by, sensitive_input_ref, policy_snapshot_id,
                           expires_at, resolved_at, resolved_by, resolve_reason, created_at)
    SELECT id, run_id, work_item_id, kind, risk, status, summary,
           requested_by, sensitive_input_ref, policy_snapshot_id,
           expires_at, resolved_at, resolved_by, resolve_reason, created_at
    FROM approvals;

DROP TABLE approvals;
ALTER TABLE approvals_new RENAME TO approvals;
CREATE INDEX idx_approvals_run ON approvals(run_id);
CREATE INDEX idx_approvals_pending ON approvals(status) WHERE status = 'pending';
