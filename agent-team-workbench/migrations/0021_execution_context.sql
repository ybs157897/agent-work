-- 0021_execution_context.sql — 执行上下文（RFC task-control-surface §6.1）。
-- Workspace 不再等价于路径：ExecutionHost / HostMount / WorkspaceLocation 成为
-- 显式领域事实；每个 Run 固化一条不可变 ExecutionContextSnapshot；runner 归属
-- 从 workspace 改为 execution host。
--
-- runners.workspace_id：ownership 语义已删除（Runner 是基础设施，不属于单一
-- Workspace）。为兼容旧读模型保留列，但必须移除 NOT NULL/FK 约束；v2 Runner
-- 以 execution_host_id 为唯一归属，workspace_id 一律为 NULL。
--
-- 历史数据：
--   * 单 Workspace 且无 Location 的库：注册 host_local + host_local/default
--     mount + 该 Workspace 默认 Location。repository_identity 用 'repo_legacy_local'
--     —— schema 此前没有任何 repository identity 事实源（无 git remote 配置表），
--     多 Workspace 的库不猜路径、不自动映射。
--   * 已终态 Run 回填 schema_version='execution-context/legacy-v0'、source='legacy'
--     的审计 snapshot（digest 用 'legacy-v0' 哨兵，位置字段全空，不可 resume）；
--     非终态 legacy Run 不动，启动对账归应用层。

CREATE TABLE execution_hosts (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('local','remote')),
    status         TEXT NOT NULL DEFAULT 'offline' CHECK (status IN ('ready','degraded','offline')),
    enrollment_ref TEXT NOT NULL DEFAULT '',
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Host 本地受信 registry 的广告投影；绝对路径只存在 Host 本地，不进入数据库。
CREATE TABLE execution_host_mounts (
    execution_host_id   TEXT NOT NULL REFERENCES execution_hosts(id),
    alias               TEXT NOT NULL,
    repository_identity TEXT NOT NULL,
    display_label       TEXT NOT NULL DEFAULT '',
    default_branch      TEXT,
    supported_ref_kinds JSONB NOT NULL DEFAULT '[]',
    checkouts           JSONB NOT NULL DEFAULT '[]',
    registry_generation TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'unavailable' CHECK (status IN ('ready','unavailable')),
    last_seen_at        TIMESTAMPTZ,
    PRIMARY KEY (execution_host_id, alias)
);

CREATE TABLE workspace_locations (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id),
    execution_host_id   TEXT NOT NULL REFERENCES execution_hosts(id),
    mount_alias         TEXT NOT NULL,
    mount_generation    TEXT NOT NULL DEFAULT '',
    repository_identity TEXT NOT NULL,
    is_default          BOOLEAN NOT NULL DEFAULT FALSE,
    status              TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('ready','degraded','unavailable')),
    version             INTEGER NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, execution_host_id, mount_alias)
);

-- 每 Workspace 至多一个默认 Location（部分唯一索引）。
CREATE UNIQUE INDEX idx_workspace_locations_default
    ON workspace_locations(workspace_id)
    WHERE is_default;

-- WorkItem 的逻辑开发选择；root/branch/worktree 三种 ref 组合由 CHECK 收口：
-- root 时 branch/worktree 全空；branch 时 branch_name+checkout_ref 非空；
-- worktree 时 worktree_ref 非空。不存在 path/cwd/absolute_path 字段。
CREATE TABLE work_item_contexts (
    work_item_id          TEXT PRIMARY KEY REFERENCES work_items(id),
    context_owner_id      TEXT NOT NULL REFERENCES work_items(id),
    workspace_location_id TEXT NOT NULL REFERENCES workspace_locations(id),
    ref_kind              TEXT NOT NULL CHECK (ref_kind IN ('root','branch','worktree')),
    branch_name           TEXT,
    checkout_ref          TEXT,
    worktree_ref          TEXT,
    base_revision         TEXT,
    version               INTEGER NOT NULL DEFAULT 1,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (ref_kind = 'root' AND branch_name IS NULL AND worktree_ref IS NULL)
        OR (ref_kind = 'branch' AND branch_name IS NOT NULL AND branch_name <> ''
            AND checkout_ref IS NOT NULL AND checkout_ref <> '')
        OR (ref_kind = 'worktree' AND worktree_ref IS NOT NULL AND worktree_ref <> '')
    )
);

-- 每 Run 恰好一条不可变 snapshot；retry/evaluation/recovery 克隆不新建时复用原行。
CREATE TABLE execution_context_snapshots (
    id                    TEXT PRIMARY KEY,
    run_id                TEXT NOT NULL UNIQUE REFERENCES execution_runs(id),
    schema_version        TEXT NOT NULL,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    workspace_location_id TEXT REFERENCES workspace_locations(id),
    location_version      INTEGER,
    mount_generation      TEXT,
    execution_host_id     TEXT REFERENCES execution_hosts(id),
    mount_alias           TEXT,
    repository_identity   TEXT,
    ref_kind              TEXT NOT NULL CHECK (ref_kind IN ('root','branch','worktree')),
    branch_name           TEXT,
    checkout_ref          TEXT,
    worktree_ref          TEXT,
    base_revision         TEXT,
    context_generation    INTEGER NOT NULL DEFAULT 0,
    source                TEXT NOT NULL CHECK (source IN ('current','inherited','retry','evaluation','recovery','legacy')),
    source_snapshot_id    TEXT REFERENCES execution_context_snapshots(id),
    snapshot_digest       TEXT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Database-level protection for callers that bypass the application port.
CREATE OR REPLACE FUNCTION reject_execution_context_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'execution context snapshots are immutable';
END;
$$;

CREATE TRIGGER execution_context_snapshot_immutable_update
BEFORE UPDATE ON execution_context_snapshots
FOR EACH ROW EXECUTE FUNCTION reject_execution_context_snapshot_mutation();

CREATE TRIGGER execution_context_snapshot_immutable_delete
BEFORE DELETE ON execution_context_snapshots
FOR EACH ROW EXECUTE FUNCTION reject_execution_context_snapshot_mutation();

-- Runner 归属改挂 ExecutionHost；workspace_id 保留为 nullable compatibility
-- column，绝不再引用某一个 Workspace。
ALTER TABLE runners DROP CONSTRAINT IF EXISTS runners_workspace_id_fkey;
ALTER TABLE runners ALTER COLUMN workspace_id DROP NOT NULL;
ALTER TABLE runners ADD COLUMN execution_host_id TEXT REFERENCES execution_hosts(id);
ALTER TABLE runners ADD COLUMN connection_epoch TEXT;
ALTER TABLE runners ADD COLUMN boot_id TEXT;
-- 旧版本在 PG 并发下可能为同一 Run 留下多个 active lease；v2 只允许当前
-- fencing token 继续生效。先确定性释放较低 token（同 token 按 lease_id），
-- 再建立 active 单例与 token 唯一门禁。
UPDATE run_leases stale
SET released_at=COALESCE(stale.released_at, stale.acquired_at)
WHERE stale.released_at IS NULL
  AND EXISTS (
      SELECT 1 FROM run_leases current
      WHERE current.run_id=stale.run_id
        AND current.released_at IS NULL
        AND (current.fencing_token > stale.fencing_token
             OR (current.fencing_token=stale.fencing_token AND current.lease_id > stale.lease_id))
  );
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_leases_run_fencing
    ON run_leases(run_id, fencing_token);
CREATE UNIQUE INDEX IF NOT EXISTS idx_run_leases_one_active_per_run
    ON run_leases(run_id) WHERE released_at IS NULL;

ALTER TABLE execution_runs ADD COLUMN context_snapshot_id TEXT REFERENCES execution_context_snapshots(id);
ALTER TABLE plans ADD COLUMN context_snapshot_id TEXT REFERENCES execution_context_snapshots(id);
ALTER TABLE plans ADD COLUMN context_generation INTEGER NOT NULL DEFAULT 0;

ALTER TABLE task_sessions ADD COLUMN context_snapshot_id TEXT REFERENCES execution_context_snapshots(id);
ALTER TABLE task_sessions ADD COLUMN context_generation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE task_sessions ADD COLUMN last_run_id TEXT REFERENCES execution_runs(id);
ALTER TABLE task_sessions ADD COLUMN anchor_run_sequence INTEGER NOT NULL DEFAULT 0;

-- 单 Workspace 引导（RFC §6.1）：只注册逻辑身份，不猜任何 Host 本地路径。
-- repository_identity 无既有事实源，用固定哨兵 'repo_legacy_local'；
-- mount_generation/registry_generation 用 'legacy-bootstrap' 哨兵；legacy registry
-- 只有 root checkout（branch/worktree 发现是 v2 runner 能力），因此
-- supported_ref_kinds 只含 root；default_branch/checkouts/last_seen_at 无事实，置空。
INSERT INTO execution_hosts (id, name, kind, status, enrollment_ref, version, created_at, updated_at)
SELECT 'host_local', 'local', 'local', 'ready', '', 1, w.created_at, w.created_at
FROM workspaces w
WHERE (SELECT COUNT(*) FROM workspaces) = 1
  AND NOT EXISTS (SELECT 1 FROM execution_hosts WHERE id = 'host_local');

INSERT INTO execution_host_mounts
    (execution_host_id, alias, repository_identity, display_label, default_branch,
     supported_ref_kinds, checkouts, registry_generation, status, last_seen_at)
SELECT 'host_local', 'default', 'repo_legacy_local', 'default (legacy bootstrap)', NULL,
       '["root"]', '[]', 'legacy-bootstrap', 'ready', NULL
FROM workspaces w
WHERE (SELECT COUNT(*) FROM workspaces) = 1
  AND NOT EXISTS (
      SELECT 1 FROM execution_host_mounts
      WHERE execution_host_id = 'host_local' AND alias = 'default'
  );

INSERT INTO workspace_locations
    (id, workspace_id, execution_host_id, mount_alias, mount_generation,
     repository_identity, is_default, status, version, created_at, updated_at)
SELECT 'wsloc_legacy_default', w.id, 'host_local', 'default', 'legacy-bootstrap',
       'repo_legacy_local', TRUE, 'ready', 1, w.created_at, w.created_at
FROM workspaces w
WHERE (SELECT COUNT(*) FROM workspaces) = 1
  AND NOT EXISTS (SELECT 1 FROM workspace_locations);

-- 已终态 Run（succeeded/interrupted/cancelled/lost/failed）回填 legacy 审计
-- snapshot：仅证明"该 Run 曾以 legacy 方式执行"，无位置身份、不可 resume；
-- 位置/digest 相关字段用哨兵或空值表达不可解析。多 Workspace 库同样回填
-- （审计不依赖 Location 映射）。created_at 沿用 Run.updated_at，保留 legacy 时间。
INSERT INTO execution_context_snapshots
    (id, run_id, schema_version, workspace_id, workspace_location_id, location_version,
     mount_generation, execution_host_id, mount_alias, repository_identity,
     ref_kind, branch_name, checkout_ref, worktree_ref, base_revision,
     context_generation, source, source_snapshot_id, snapshot_digest, created_at)
SELECT 'ctxsnap_legacy_' || r.id, r.id, 'execution-context/legacy-v0', r.workspace_id,
       NULL, NULL, NULL, NULL, NULL, NULL,
       'root', NULL, NULL, NULL, NULL,
       0, 'legacy', NULL, 'legacy-v0', r.updated_at
FROM execution_runs r
WHERE r.status IN ('succeeded','interrupted','cancelled','lost','failed');

UPDATE execution_runs
SET context_snapshot_id = 'ctxsnap_legacy_' || id
WHERE status IN ('succeeded','interrupted','cancelled','lost','failed')
  AND context_snapshot_id IS NULL;
