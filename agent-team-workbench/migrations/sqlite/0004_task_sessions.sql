-- 0004_task_sessions_sqlite.sql — 跨 Run 会话锚点 + 执行审计扩展（SQLite 本地验证版）。
-- 与 migrations/0004_task_sessions.sql 结构一致；差异：JSONB→JSON 文本、TIMESTAMPTZ→DATETIME。

CREATE TABLE task_sessions (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id  TEXT NOT NULL DEFAULT '',
    adapter_id        TEXT NOT NULL,
    task_key          TEXT NOT NULL,
    session_params    TEXT NOT NULL DEFAULT '{}',
    display_id        TEXT,
    runs_count        INTEGER NOT NULL DEFAULT 0,
    input_tokens_cum  INTEGER NOT NULL DEFAULT 0,
    created_at        DATETIME NOT NULL,
    updated_at        DATETIME NOT NULL,
    UNIQUE (workspace_id, agent_profile_id, adapter_id, task_key)
);
CREATE INDEX idx_task_sessions_agent ON task_sessions(workspace_id, agent_profile_id);

ALTER TABLE execution_runs ADD COLUMN session_before TEXT;
ALTER TABLE execution_runs ADD COLUMN session_after  TEXT;
ALTER TABLE execution_runs ADD COLUMN usage_in       INTEGER;
ALTER TABLE execution_runs ADD COLUMN usage_out      INTEGER;
ALTER TABLE execution_runs ADD COLUMN usage_cached   INTEGER;
ALTER TABLE execution_runs ADD COLUMN usage_basis    TEXT;
ALTER TABLE execution_runs ADD COLUMN error_family   TEXT;
