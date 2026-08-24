-- 0012_approval_grants_sqlite.sql — 审批授权粒度（SQLite 本地验证版）。
-- 与 migrations/0012_approval_grants.sql 语义等价。

CREATE TABLE approval_grants (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id TEXT NOT NULL REFERENCES agent_profiles(id),
    work_item_id     TEXT REFERENCES work_items(id),
    scope            TEXT NOT NULL CHECK (scope IN ('thread','workspace')),
    kind             TEXT NOT NULL CHECK (kind IN ('command','file_change','permissions')),
    pattern          TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL
);
CREATE INDEX idx_approval_grants_lookup ON approval_grants(workspace_id, agent_profile_id, kind);
