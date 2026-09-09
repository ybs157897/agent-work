-- 0049_workspace_projects.sql — one path-free canonical project per Workspace.
-- The canonical key is Host identity + mount alias from the trusted registry;
-- no browser path or filesystem-derived string is accepted directly.
CREATE TABLE workspace_projects (
    workspace_id         TEXT PRIMARY KEY REFERENCES workspaces(id),
    execution_host_id    TEXT NOT NULL,
    mount_alias          TEXT NOT NULL,
    mount_generation     TEXT NOT NULL,
    repository_identity  TEXT NOT NULL,
    canonical_key        TEXT NOT NULL UNIQUE,
    location_id          TEXT NOT NULL UNIQUE REFERENCES workspace_locations(id),
    status               TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending','ready','unavailable','setup_failed')),
    setup_error          TEXT NOT NULL DEFAULT '',
    source_workspace_id  TEXT REFERENCES workspaces(id),
    version              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    UNIQUE (execution_host_id, mount_alias),
    CHECK (length(trim(mount_generation)) > 0),
    CHECK (length(trim(repository_identity)) > 0),
    CHECK (length(trim(canonical_key)) > 0)
);

CREATE INDEX idx_workspace_projects_status ON workspace_projects(status);
