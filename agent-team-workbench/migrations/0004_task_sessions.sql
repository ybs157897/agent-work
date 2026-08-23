-- 0004_task_sessions.sql — 跨 Run 会话锚点 + 执行审计扩展（PostgreSQL 生产版）。
-- 对齐 Paperclip agent_task_sessions：会话连续性从「上一个 run 的 session_ref 推断」
-- 升级为独立实体，携带配置指纹用于漂移检测。

CREATE TABLE task_sessions (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id  TEXT NOT NULL DEFAULT '',
    adapter_id        TEXT NOT NULL,
    task_key          TEXT NOT NULL,
    session_params    JSONB NOT NULL DEFAULT '{}',
    display_id        TEXT,
    runs_count        INTEGER NOT NULL DEFAULT 0,
    input_tokens_cum  BIGINT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    UNIQUE (workspace_id, agent_profile_id, adapter_id, task_key)
);
CREATE INDEX idx_task_sessions_agent ON task_sessions(workspace_id, agent_profile_id);

-- execution_runs 会话/用量/错误族审计列。
ALTER TABLE execution_runs ADD COLUMN session_before TEXT;
ALTER TABLE execution_runs ADD COLUMN session_after  TEXT;
ALTER TABLE execution_runs ADD COLUMN usage_in       BIGINT;
ALTER TABLE execution_runs ADD COLUMN usage_out      BIGINT;
ALTER TABLE execution_runs ADD COLUMN usage_cached   BIGINT;
ALTER TABLE execution_runs ADD COLUMN usage_basis    TEXT;
ALTER TABLE execution_runs ADD COLUMN error_family   TEXT;
