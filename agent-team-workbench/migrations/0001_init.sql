-- 0001_init.sql — M1 初始 schema（协议文档 §4 / §11）
-- 约定：所有写命令同事务提交 状态变更 + run_events + outbox + idempotency。
-- 乐观锁：可变资源带 version；(run_id, run_seq) 唯一；终态 Run 不可逆。

CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    timezone    TEXT NOT NULL DEFAULT 'UTC',
    policies    JSONB NOT NULL DEFAULT '{}',
    version     INTEGER NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspace_members (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    user_id      TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('owner','admin','operator','approver','viewer')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

-- 持久角色配置，与 Runtime Session 解耦；availability 与 presence 分离
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
    runtime_preference JSONB NOT NULL DEFAULT '{}',
    version            INTEGER NOT NULL DEFAULT 1,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_agent_profiles_ws ON agent_profiles(workspace_id);

CREATE TABLE runtime_bindings (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    runtime_label    TEXT NOT NULL,
    adapter_id       TEXT NOT NULL,
    adapter_version  TEXT NOT NULL,
    provider_version TEXT,
    capabilities     JSONB NOT NULL DEFAULT '{}',
    status           TEXT NOT NULL DEFAULT 'unavailable' CHECK (status IN ('ready','probing','degraded','unavailable')),
    version          INTEGER NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, runtime_label)
);

CREATE TABLE work_items (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    title            TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'todo'
                     CHECK (status IN ('todo','in_progress','blocked','completed','cancelled')),
    -- 评审态投影：WorkItem 仍在 in_progress，phase=review/acceptance
    phase            TEXT CHECK (phase IN ('execution','review','acceptance')),
    priority         TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('low','medium','high','urgent')),
    due_date         DATE,
    agent_profile_id TEXT REFERENCES agent_profiles(id),
    version          INTEGER NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_work_items_ws_status ON work_items(workspace_id, status);
CREATE INDEX idx_work_items_assignee ON work_items(agent_profile_id);

CREATE TABLE blockers (
    id           TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL REFERENCES work_items(id),
    code         TEXT NOT NULL,
    message      TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'manual',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at  TIMESTAMPTZ
);
CREATE INDEX idx_blockers_open ON blockers(work_item_id) WHERE resolved_at IS NULL;

-- 一次不可覆盖的执行尝试；终态不可逆，retry 创建新行（retry_of）
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
    session_ref            TEXT, -- Adapter 私有句柄，受限存储
    progress               DOUBLE PRECISION,
    retry_of               TEXT REFERENCES execution_runs(id),
    failure_code           TEXT,
    failure_message        TEXT,
    failure_retryable      BOOLEAN,
    input                  JSONB NOT NULL DEFAULT '{}',
    version                INTEGER NOT NULL DEFAULT 1,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at            TIMESTAMPTZ
);
CREATE INDEX idx_runs_work_item ON execution_runs(work_item_id, created_at);
CREATE INDEX idx_runs_ws_status ON execution_runs(workspace_id, status);

-- Canonical Event 是投影事实源；Run 内 seq 单调
CREATE TABLE run_events (
    run_id      TEXT NOT NULL REFERENCES execution_runs(id),
    run_seq     BIGINT NOT NULL,
    event_type  TEXT NOT NULL,
    payload     JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, run_seq)
);

-- Runner 事件 ingress 去重（先于 canonical run_seq 分配）
CREATE TABLE runner_event_dedup (
    run_id     TEXT NOT NULL,
    runner_id  TEXT NOT NULL,
    runner_seq BIGINT NOT NULL,
    run_seq    BIGINT NOT NULL,
    PRIMARY KEY (run_id, runner_id, runner_seq)
);

CREATE TABLE capability_snapshots (
    id          TEXT PRIMARY KEY,
    run_id      TEXT REFERENCES execution_runs(id),
    required    JSONB NOT NULL DEFAULT '{}',
    advertised  JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE approvals (
    id               TEXT PRIMARY KEY,
    run_id           TEXT NOT NULL REFERENCES execution_runs(id),
    work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    kind             TEXT NOT NULL,
    risk             TEXT NOT NULL CHECK (risk IN ('low','medium','high')),
    status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected','expired')),
    summary          TEXT NOT NULL,
    requested_by     JSONB NOT NULL DEFAULT '{}',
    sensitive_input_ref TEXT,
    policy_snapshot_id  TEXT,
    expires_at       TIMESTAMPTZ,
    resolved_at      TIMESTAMPTZ,
    resolved_by      TEXT,
    resolve_reason   TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_approvals_run ON approvals(run_id);
CREATE INDEX idx_approvals_pending ON approvals(status) WHERE status = 'pending';

CREATE TABLE artifacts (
    id           TEXT PRIMARY KEY,
    run_id       TEXT NOT NULL REFERENCES execution_runs(id),
    logical_path TEXT NOT NULL,
    mime         TEXT NOT NULL DEFAULT 'application/octet-stream',
    size         BIGINT NOT NULL DEFAULT 0,
    sha256       TEXT NOT NULL,
    classification TEXT NOT NULL DEFAULT 'internal',
    status       TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','accepted')),
    storage_ref  TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_artifacts_run ON artifacts(run_id);

-- 活动流（日志页 + Dashboard 最近日志）
CREATE TABLE activities (
    id          TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    kind        TEXT NOT NULL,
    message     TEXT NOT NULL,
    actor       JSONB NOT NULL DEFAULT '{}',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_activities_ws ON activities(workspace_id, occurred_at DESC);

-- Workspace 投影事件流（SSE replay 数据源）；stream_seq 单调
CREATE TABLE stream_events (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    stream_seq   BIGINT NOT NULL,
    event_id     TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    payload      JSONB NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, stream_seq)
);
CREATE UNIQUE INDEX idx_stream_events_event_id ON stream_events(event_id);

-- 事务 Outbox：先提交再发布，至少一次投递
CREATE TABLE outbox_messages (
    id          BIGSERIAL PRIMARY KEY,
    event_id    TEXT NOT NULL,
    topic       TEXT NOT NULL,
    payload     JSONB NOT NULL,
    published_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_outbox_unpublished ON outbox_messages(id) WHERE published_at IS NULL;

CREATE TABLE idempotency_keys (
    workspace_id TEXT NOT NULL,
    key          TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    result_ref   TEXT,
    status_code  INTEGER,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, key)
);

-- Runner 注册与租约（M2 使用，schema 先行）
CREATE TABLE runners (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id),
    label         TEXT NOT NULL,
    runner_version TEXT,
    os            TEXT,
    arch          TEXT,
    slots         INTEGER NOT NULL DEFAULT 1,
    adapters      JSONB NOT NULL DEFAULT '[]',
    status        TEXT NOT NULL DEFAULT 'offline' CHECK (status IN ('connected','degraded','offline')),
    last_seen_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE run_leases (
    lease_id      TEXT PRIMARY KEY,
    run_id        TEXT NOT NULL REFERENCES execution_runs(id),
    runner_id     TEXT NOT NULL REFERENCES runners(id),
    fencing_token BIGINT NOT NULL,
    acquired_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    renewed_until TIMESTAMPTZ NOT NULL,
    released_at   TIMESTAMPTZ
);
CREATE INDEX idx_run_leases_run ON run_leases(run_id, fencing_token DESC);

CREATE TABLE audit_logs (
    id           BIGSERIAL PRIMARY KEY,
    workspace_id TEXT,
    actor        JSONB NOT NULL,
    action       TEXT NOT NULL,
    target       TEXT,
    detail       JSONB NOT NULL DEFAULT '{}',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_ws ON audit_logs(workspace_id, occurred_at DESC);
