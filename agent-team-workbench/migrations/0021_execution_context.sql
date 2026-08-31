-- 0021_execution_context.sql — 执行上下文（SQLite）。
-- immutable 用 SQLite TRIGGER 拒绝 UPDATE/DELETE。
--
-- runners.workspace_id：ownership 语义已删除（Runner 是基础设施，不属于单一
-- Workspace）。为兼容旧读模型保留列，但必须移除 NOT NULL/FK 约束；v2 Runner
-- 以 execution_host_id 为唯一归属，workspace_id 一律为 NULL。
--
-- 历史数据：单 Workspace 且无 Location 的库注册 host_local +
-- host_local/default + 默认 Location（repository_identity 哨兵
-- 'repo_legacy_local'，无既有事实源）；已终态 Run 回填 legacy-v0 审计 snapshot
-- （digest 哨兵 'legacy-v0'，位置字段全空，不可 resume）；非终态 legacy Run 不动。

CREATE TABLE execution_hosts (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL CHECK (kind IN ('local','remote')),
    status         TEXT NOT NULL DEFAULT 'offline' CHECK (status IN ('ready','degraded','offline')),
    enrollment_ref TEXT NOT NULL DEFAULT '',
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     DATETIME NOT NULL,
    updated_at     DATETIME NOT NULL
);

CREATE TABLE execution_host_mounts (
    execution_host_id   TEXT NOT NULL REFERENCES execution_hosts(id),
    alias               TEXT NOT NULL,
    repository_identity TEXT NOT NULL,
    display_label       TEXT NOT NULL DEFAULT '',
    default_branch      TEXT,
    supported_ref_kinds TEXT NOT NULL DEFAULT '[]',
    checkouts           TEXT NOT NULL DEFAULT '[]',
    registry_generation TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'unavailable' CHECK (status IN ('ready','unavailable')),
    last_seen_at        DATETIME,
    PRIMARY KEY (execution_host_id, alias)
);

CREATE TABLE workspace_locations (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id),
    execution_host_id   TEXT NOT NULL REFERENCES execution_hosts(id),
    mount_alias         TEXT NOT NULL,
    mount_generation    TEXT NOT NULL DEFAULT '',
    repository_identity TEXT NOT NULL,
    is_default          INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('ready','degraded','unavailable')),
    version             INTEGER NOT NULL DEFAULT 1,
    created_at          DATETIME NOT NULL,
    updated_at          DATETIME NOT NULL,
    UNIQUE (workspace_id, execution_host_id, mount_alias)
);

CREATE UNIQUE INDEX idx_workspace_locations_default
    ON workspace_locations(workspace_id)
    WHERE is_default;

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
    created_at            DATETIME NOT NULL,
    updated_at            DATETIME NOT NULL,
    CHECK (
        (ref_kind = 'root' AND branch_name IS NULL AND worktree_ref IS NULL)
        OR (ref_kind = 'branch' AND branch_name IS NOT NULL AND branch_name <> ''
            AND checkout_ref IS NOT NULL AND checkout_ref <> '')
        OR (ref_kind = 'worktree' AND worktree_ref IS NOT NULL AND worktree_ref <> '')
    )
);

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
    created_at            DATETIME NOT NULL
);

CREATE TRIGGER execution_context_snapshot_immutable_update
BEFORE UPDATE ON execution_context_snapshots
BEGIN
    SELECT RAISE(ABORT, 'execution context snapshots are immutable');
END;

CREATE TRIGGER execution_context_snapshot_immutable_delete
BEFORE DELETE ON execution_context_snapshots
BEGIN
    SELECT RAISE(ABORT, 'execution context snapshots are immutable');
END;

-- SQLite 不能 ALTER COLUMN/DROP FK。重建严格采用 0023 的暂存表
-- CREATE/DROP/INSERT 机械，禁止 ALTER TABLE ... RENAME：pinned modernc SQLite
-- 在 0019 后置门禁路径会重解析 0020 触发器体而失败。defer_foreign_keys 让
-- run_leases 的历史行在同一迁移事务内持续引用最终同名的新 runners 表。
PRAGMA defer_foreign_keys = ON;
CREATE TABLE runners_v2 AS
SELECT id, NULL AS workspace_id, label, runner_version, os, arch, slots, adapters,
       status, last_seen_at, created_at
FROM runners;
DROP TABLE runners;
CREATE TABLE runners (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT,
    execution_host_id TEXT REFERENCES execution_hosts(id),
    connection_epoch  TEXT,
	boot_id           TEXT,
    label             TEXT NOT NULL,
    runner_version    TEXT,
    os                TEXT,
    arch              TEXT,
    slots             INTEGER NOT NULL DEFAULT 1,
    adapters          TEXT NOT NULL DEFAULT '[]',
    status            TEXT NOT NULL DEFAULT 'offline' CHECK (status IN ('connected','degraded','offline')),
    last_seen_at      DATETIME,
    created_at        DATETIME NOT NULL
);
INSERT INTO runners(id, workspace_id, execution_host_id, connection_epoch, boot_id, label,
	                    runner_version, os, arch, slots, adapters, status, last_seen_at, created_at)
SELECT id, workspace_id, NULL, NULL, NULL, label, runner_version, os, arch, slots, adapters,
       status, last_seen_at, created_at
FROM runners_v2;
DROP TABLE runners_v2;
-- 与 PG 同口径：升级时只保留每个 Run 的最高 active fencing token；其余
-- 历史 lease 写 released_at，再建立 active 单例门禁。
UPDATE run_leases AS stale
SET released_at=COALESCE(stale.released_at, stale.acquired_at)
WHERE stale.released_at IS NULL
  AND EXISTS (
      SELECT 1 FROM run_leases AS current
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

-- 单 Workspace 引导：created_at/updated_at 沿用该 Workspace 的创建时间
--（SQLite 无 DEFAULT now()，且保留 legacy 时间与 PG 侧取值一致）。
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
       'repo_legacy_local', 1, 'ready', 1, w.created_at, w.created_at
FROM workspaces w
WHERE (SELECT COUNT(*) FROM workspaces) = 1
  AND NOT EXISTS (SELECT 1 FROM workspace_locations);

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
