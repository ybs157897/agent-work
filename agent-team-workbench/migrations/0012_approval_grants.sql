-- 0012_approval_grants.sql — 审批授权粒度：「总是允许」三级授权（once/thread/workspace）
-- 的落库形态。thread 锚定 work_item_id（会话≈work item 锚点）；workspace 作用域
-- work_item_id 为 NULL（全局）。授权永不跨 workspace/agent：两列是匹配硬条件。

CREATE TABLE approval_grants (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id TEXT NOT NULL REFERENCES agent_profiles(id),
    work_item_id     TEXT REFERENCES work_items(id),
    scope            TEXT NOT NULL CHECK (scope IN ('thread','workspace')),
    kind             TEXT NOT NULL CHECK (kind IN ('command','file_change','permissions')),
    pattern          TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_approval_grants_lookup ON approval_grants(workspace_id, agent_profile_id, kind);
