-- 0006_plans_sqlite.sql — M1 编排层（SQLite 本地验证版）。
-- 与 migrations/0006_plans.sql 语义等价；差异：JSONB→TEXT、TIMESTAMPTZ→DATETIME。

CREATE TABLE plans (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    work_item_id      TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id  TEXT NOT NULL,
    source_run_id     TEXT,
    status            TEXT NOT NULL DEFAULT 'active'
                      CHECK (status IN ('active','waiting','finished','cancelled','failed')),
    superseded_by     TEXT,
    version           INTEGER NOT NULL DEFAULT 1,
    created_at        DATETIME NOT NULL,
    updated_at        DATETIME NOT NULL
);
CREATE INDEX idx_plans_work_item ON plans(work_item_id, status);

CREATE TABLE plan_steps (
    plan_id             TEXT NOT NULL REFERENCES plans(id),
    seq                 INTEGER NOT NULL,
    verb                TEXT NOT NULL CHECK (verb IN ('dispatch','defer','finish')),
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

ALTER TABLE work_items ADD COLUMN parent_id TEXT REFERENCES work_items(id);
CREATE INDEX idx_work_items_parent ON work_items(parent_id);
