-- 0005_wakeup.sql — wakeup 四源调度核心（timer / assignment / on_demand / automation）。
-- agent_wakeup_requests 是唤醒请求队列，状态机：queued → coalesced | consumed；
-- coalesced 仅为审计终态（心跳禁用 / 心跳间隔内 / 已有活跃 run / 超龄防堆积）。

CREATE TABLE agent_wakeup_requests (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id TEXT NOT NULL,
    source           TEXT NOT NULL CHECK (source IN ('timer','assignment','on_demand','automation')),
    task_key         TEXT NOT NULL,
    context          JSONB NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','coalesced','consumed')),
    wake_at          TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_agent_wakeup_scan ON agent_wakeup_requests(status, wake_at);
CREATE INDEX idx_agent_wakeup_coalesce ON agent_wakeup_requests(agent_profile_id, task_key, status);

-- agent_profiles 心跳与唤醒策略列：控制该 agent 是否参与 wakeup 调度及节奏。
ALTER TABLE agent_profiles ADD COLUMN heartbeat_enabled      BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE agent_profiles ADD COLUMN heartbeat_interval_sec INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_profiles ADD COLUMN wake_on_assignment     BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE agent_profiles ADD COLUMN wake_on_demand         BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE agent_profiles ADD COLUMN wake_on_automation     BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE agent_profiles ADD COLUMN prompt_template        TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN last_heartbeat_at      TIMESTAMPTZ;
