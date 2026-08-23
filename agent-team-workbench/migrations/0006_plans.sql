-- 0006_plans.sql — M1 编排层：plans / plan_steps 表与 work_items.parent_id 子任务树。
-- plan 状态机 active/waiting → finished/cancelled/failed；defer 即批次终止（无游标列），
-- 唤醒链路由 automation wakeup（children_quiet 钩子 / defer wake_at）承接。

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
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_plans_work_item ON plans(work_item_id, status);

-- 步骤级行级审计：哪个 dispatch 建了哪个子任务/run 直接可查；
-- (plan_id, seq) 联合主键同时是应用层重入写的安全约束。
CREATE TABLE plan_steps (
    plan_id             TEXT NOT NULL REFERENCES plans(id),
    seq                 INTEGER NOT NULL,
    verb                TEXT NOT NULL CHECK (verb IN ('dispatch','defer','finish')),
    payload             JSONB NOT NULL DEFAULT '{}',
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','executed','skipped','failed')),
    result_work_item_id TEXT,
    result_run_id       TEXT,
    error               TEXT,
    created_at          TIMESTAMPTZ NOT NULL,
    executed_at         TIMESTAMPTZ,
    PRIMARY KEY (plan_id, seq)
);

-- 子任务树：plan 绑定一个主任务，dispatch 派生的子任务以 parent_id 挂到主任务下。
ALTER TABLE work_items ADD COLUMN parent_id TEXT REFERENCES work_items(id);
CREATE INDEX idx_work_items_parent ON work_items(parent_id);
