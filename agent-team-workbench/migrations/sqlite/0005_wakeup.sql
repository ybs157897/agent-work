-- 0005_wakeup_sqlite.sql — wakeup 四源调度核心（SQLite 本地验证版）。
-- 与 migrations/0005_wakeup.sql 结构一致；差异：JSONB→JSON 文本、TIMESTAMPTZ→DATETIME、
-- BOOLEAN→INTEGER 0/1。

CREATE TABLE agent_wakeup_requests (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id TEXT NOT NULL,
    source           TEXT NOT NULL CHECK (source IN ('timer','assignment','on_demand','automation')),
    task_key         TEXT NOT NULL,
    context          TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','coalesced','consumed')),
    wake_at          DATETIME NOT NULL,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL
);
CREATE INDEX idx_agent_wakeup_scan ON agent_wakeup_requests(status, wake_at);
CREATE INDEX idx_agent_wakeup_coalesce ON agent_wakeup_requests(agent_profile_id, task_key, status);

ALTER TABLE agent_profiles ADD COLUMN heartbeat_enabled      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_profiles ADD COLUMN heartbeat_interval_sec INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_profiles ADD COLUMN wake_on_assignment     INTEGER NOT NULL DEFAULT 1;
ALTER TABLE agent_profiles ADD COLUMN wake_on_demand         INTEGER NOT NULL DEFAULT 1;
ALTER TABLE agent_profiles ADD COLUMN wake_on_automation     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_profiles ADD COLUMN prompt_template        TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN last_heartbeat_at      DATETIME;
