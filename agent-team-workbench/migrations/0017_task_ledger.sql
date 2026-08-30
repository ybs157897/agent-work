-- 0017_task_ledger.sql — 会话元模型 S2：任务台账（Task Ledger）。
-- rolling_digest 是任务级滚动摘要（确定性生成，无 LLM；转述只允许进摘要），
-- 决策台账 decision_entries 存用户原话（quote 保真，禁止 LLM 转述——「当时
-- 怎么定的」必须可回溯）。台账不属于任何会话，是任务级共享记忆（D4：横向
-- 协作只走任务台账）。

ALTER TABLE work_items ADD COLUMN rolling_digest TEXT NOT NULL DEFAULT '';

CREATE TABLE decision_entries (
    id            TEXT PRIMARY KEY,
    work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    quote         TEXT NOT NULL,
    source_run_id TEXT REFERENCES execution_runs(id),
    source_ref    TEXT,
    created_at    TIMESTAMPTZ NOT NULL
);
