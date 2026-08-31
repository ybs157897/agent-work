-- 0016_dispatches.sql — 会话元模型 S1：派发批次（SQLite）。
-- dispatches 表 + execution_runs.dispatch_id
-- + task_sessions.segment_seq；时间列用 DATETIME（RFC3339Nano 文本）。

CREATE TABLE dispatches (
    id            TEXT PRIMARY KEY,
    work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    trigger       TEXT NOT NULL,
    lead_run_id   TEXT REFERENCES execution_runs(id),
    status        TEXT NOT NULL DEFAULT 'running'
                  CHECK (status IN ('running','collecting','completed','degraded','cancelled')),
    created_at    DATETIME NOT NULL,
    closed_at     DATETIME
);

ALTER TABLE execution_runs ADD COLUMN dispatch_id TEXT REFERENCES dispatches(id);

ALTER TABLE task_sessions ADD COLUMN segment_seq INTEGER NOT NULL DEFAULT 1;
