-- 0001_init_sqlite.sql — M1 初始 schema（SQLite 本地验证版）
-- 与 migrations/0001_init.sql（PostgreSQL 生产版）结构一致；
-- 差异：数组→JSON 文本；无 advisory lock（SQLite 写串行）；时间统一 RFC3339 文本。

CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    timezone    TEXT NOT NULL DEFAULT 'UTC',
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);

CREATE TABLE workspace_members (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    user_id      TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('owner','admin','operator','approver','viewer')),
    created_at   DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE agent_profiles (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id),
    name               TEXT NOT NULL,
    role               TEXT NOT NULL,
    skills             TEXT NOT NULL DEFAULT '[]',
    instructions       TEXT NOT NULL DEFAULT '',
    avatar             TEXT,
    availability       TEXT NOT NULL DEFAULT 'enabled' CHECK (availability IN ('enabled','disabled')),
    presence           TEXT NOT NULL DEFAULT 'idle' CHECK (presence IN ('idle','busy','degraded','offline')),
    runtime_preference TEXT NOT NULL DEFAULT '{}',
    version            INTEGER NOT NULL DEFAULT 1,
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL
);
CREATE INDEX idx_agent_profiles_ws ON agent_profiles(workspace_id);

CREATE TABLE runtime_bindings (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    runtime_label    TEXT NOT NULL,
    adapter_id       TEXT NOT NULL,
    adapter_version  TEXT NOT NULL,
    provider_version TEXT,
    capabilities     TEXT NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'unavailable' CHECK (status IN ('ready','probing','degraded','unavailable')),
    version          INTEGER NOT NULL DEFAULT 1,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    UNIQUE (workspace_id, runtime_label)
);

CREATE TABLE work_items (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    title            TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'todo'
                     CHECK (status IN ('todo','in_progress','blocked','completed','cancelled')),
    phase            TEXT CHECK (phase IN ('execution','review','acceptance')),
    priority         TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('low','medium','high','urgent')),
    due_date         TEXT,
    agent_profile_id TEXT REFERENCES agent_profiles(id),
    version          INTEGER NOT NULL DEFAULT 1,
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL
);
CREATE INDEX idx_work_items_ws_status ON work_items(workspace_id, status);
CREATE INDEX idx_work_items_assignee ON work_items(agent_profile_id);

CREATE TABLE blockers (
    id           TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL REFERENCES work_items(id),
    code         TEXT NOT NULL,
    message      TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'manual',
    created_at   DATETIME NOT NULL,
    resolved_at  DATETIME
);
CREATE INDEX idx_blockers_open ON blockers(work_item_id) WHERE resolved_at IS NULL;

CREATE TABLE execution_runs (
    id                     TEXT PRIMARY KEY,
    workspace_id           TEXT NOT NULL REFERENCES workspaces(id),
    work_item_id           TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id       TEXT REFERENCES agent_profiles(id),
    status                 TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
                               'queued','starting','running','waiting_approval','interrupting',
                               'cancelling','reconnecting','succeeding','succeeded','interrupted',
                               'cancelled','lost','failed')),
    runtime_label          TEXT,
    adapter_id             TEXT,
    provider               TEXT,
    capability_snapshot_id TEXT,
    session_ref            TEXT,
    progress               REAL,
    retry_of               TEXT REFERENCES execution_runs(id),
    failure_code           TEXT,
    failure_message        TEXT,
    failure_retryable      INTEGER,
    input                  TEXT NOT NULL DEFAULT '{}',
    version                INTEGER NOT NULL DEFAULT 1,
    created_at             DATETIME NOT NULL,
    updated_at             DATETIME NOT NULL,
    finished_at            DATETIME
);
CREATE INDEX idx_runs_work_item ON execution_runs(work_item_id, created_at);
CREATE INDEX idx_runs_ws_status ON execution_runs(workspace_id, status);

CREATE TABLE run_events (
    run_id      TEXT NOT NULL REFERENCES execution_runs(id),
    run_seq     INTEGER NOT NULL,
    event_type  TEXT NOT NULL,
    payload     TEXT NOT NULL,
    occurred_at DATETIME NOT NULL,
    PRIMARY KEY (run_id, run_seq)
);

CREATE TABLE runner_event_dedup (
    run_id     TEXT NOT NULL,
    runner_id  TEXT NOT NULL,
    runner_seq INTEGER NOT NULL,
    run_seq    INTEGER NOT NULL,
    PRIMARY KEY (run_id, runner_id, runner_seq)
);

CREATE TABLE capability_snapshots (
    id          TEXT PRIMARY KEY,
    run_id      TEXT REFERENCES execution_runs(id),
    required    TEXT NOT NULL DEFAULT '{}',
    advertised  TEXT NOT NULL DEFAULT '{}',
    created_at  DATETIME NOT NULL
);

CREATE TABLE approvals (
    id               TEXT PRIMARY KEY,
    run_id           TEXT NOT NULL REFERENCES execution_runs(id),
    work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    kind             TEXT NOT NULL,
    risk             TEXT NOT NULL CHECK (risk IN ('low','medium','high')),
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired')),
    summary          TEXT NOT NULL,
    requested_by     TEXT NOT NULL DEFAULT '{}',
    sensitive_input_ref TEXT,
    policy_snapshot_id  TEXT,
    expires_at       DATETIME,
    resolved_at      DATETIME,
    resolved_by      TEXT,
    resolve_reason   TEXT,
    created_at       DATETIME NOT NULL
);
CREATE INDEX idx_approvals_run ON approvals(run_id);

CREATE TABLE artifacts (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL REFERENCES execution_runs(id),
    logical_path TEXT NOT NULL,
    mime         TEXT NOT NULL DEFAULT 'application/octet-stream',
    size         INTEGER NOT NULL DEFAULT 0,
    sha256       TEXT NOT NULL,
    classification TEXT NOT NULL DEFAULT 'internal',
    status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','accepted')),
    storage_ref  TEXT,
    created_at   DATETIME NOT NULL
);
CREATE INDEX idx_artifacts_run ON artifacts(run_id);

CREATE TABLE activities (
    id          TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    kind        TEXT NOT NULL,
    message     TEXT NOT NULL,
    occurred_at DATETIME NOT NULL
);
CREATE INDEX idx_activities_ws ON activities(workspace_id, occurred_at DESC);

CREATE TABLE stream_events (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    stream_seq   INTEGER NOT NULL,
    event_id     TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    payload      TEXT NOT NULL,
    occurred_at  DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, stream_seq)
);
CREATE UNIQUE INDEX idx_stream_events_event_id ON stream_events(event_id);

CREATE TABLE outbox_messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    TEXT NOT NULL,
    topic       TEXT NOT NULL,
    payload     TEXT NOT NULL,
    published_at DATETIME,
    created_at  DATETIME NOT NULL
);
CREATE INDEX idx_outbox_unpublished ON outbox_messages(id) WHERE published_at IS NULL;

CREATE TABLE idempotency_keys (
    workspace_id TEXT NOT NULL,
    key          TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    result_ref   TEXT,
    status_code  INTEGER,
    created_at   DATETIME NOT NULL,
    PRIMARY KEY (workspace_id, key)
);

CREATE TABLE runners (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
    label         TEXT NOT NULL,
    runner_version TEXT,
    os            TEXT,
    arch          TEXT,
    slots         INTEGER NOT NULL DEFAULT 1,
    adapters      TEXT NOT NULL DEFAULT '[]',
    status        TEXT NOT NULL DEFAULT 'offline' CHECK (status IN ('connected','degraded','offline')),
    last_seen_at  DATETIME,
    created_at    DATETIME NOT NULL
);

CREATE TABLE run_leases (
    lease_id      TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES execution_runs(id),
    runner_id     TEXT NOT NULL REFERENCES runners(id),
    fencing_token INTEGER NOT NULL,
    acquired_at   DATETIME NOT NULL,
    renewed_until DATETIME NOT NULL,
    released_at   DATETIME
);
CREATE INDEX idx_run_leases_run ON run_leases(run_id, fencing_token DESC);

CREATE TABLE audit_logs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT,
    actor        TEXT NOT NULL,
    action       TEXT NOT NULL,
    target       TEXT,
    detail       TEXT NOT NULL DEFAULT '{}',
    occurred_at  DATETIME NOT NULL
);
CREATE INDEX idx_audit_ws ON audit_logs(workspace_id, occurred_at DESC);
