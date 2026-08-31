-- 0017_task_ledger.sql — 会话元模型 S2：任务台账（SQLite）。
-- work_items.rolling_digest +
-- decision_entries；时间列用 DATETIME（RFC3339Nano 文本）。

ALTER TABLE work_items ADD COLUMN rolling_digest TEXT NOT NULL DEFAULT '';

CREATE TABLE decision_entries (
    id            TEXT PRIMARY KEY,
    work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    quote         TEXT NOT NULL,
    source_run_id TEXT REFERENCES execution_runs(id),
    source_ref    TEXT,
    created_at    DATETIME NOT NULL
);
