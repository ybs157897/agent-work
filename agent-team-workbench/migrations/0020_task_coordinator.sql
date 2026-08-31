-- 0020_task_coordinator.sql — 系统级 Task Coordinator 配置、状态与事件（SQLite）。

ALTER TABLE agent_profiles ADD COLUMN kind TEXT NOT NULL DEFAULT 'user';
ALTER TABLE agent_profiles ADD COLUMN prompt_version TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN instructions_editable INTEGER NOT NULL DEFAULT 1;

CREATE UNIQUE INDEX idx_agent_profiles_one_task_coordinator
    ON agent_profiles(workspace_id)
    WHERE kind = 'task_coordinator';

CREATE TABLE task_coordinator_configs (
    id                     TEXT PRIMARY KEY,
    workspace_id           TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id       TEXT NOT NULL UNIQUE REFERENCES agent_profiles(id),
    prompt_version         TEXT NOT NULL,
    runtime_label          TEXT NOT NULL DEFAULT 'mock',
    fallback_runtime_label TEXT,
    model_ref              TEXT NOT NULL DEFAULT '{}',
    fallback_model_ref     TEXT NOT NULL DEFAULT '{}',
    reasoning_effort       TEXT NOT NULL DEFAULT 'medium',
    version                INTEGER NOT NULL DEFAULT 1,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    UNIQUE (workspace_id)
);
CREATE INDEX idx_task_coordinator_configs_workspace
    ON task_coordinator_configs(workspace_id);

CREATE TABLE task_coordinator_states (
    id                     TEXT PRIMARY KEY,
    workspace_id           TEXT NOT NULL REFERENCES workspaces(id),
    root_work_item_id      TEXT NOT NULL UNIQUE REFERENCES work_items(id),
    coordinator_agent_id   TEXT NOT NULL REFERENCES agent_profiles(id),
    status                 TEXT NOT NULL DEFAULT 'queued'
                           CHECK (status IN ('queued','running','waiting_retry',
                                             'waiting_user','blocked','completed','cancelled')),
    phase                  TEXT NOT NULL DEFAULT '',
    summary                TEXT NOT NULL DEFAULT '',
    current_action         TEXT NOT NULL DEFAULT '',
    current_step           TEXT NOT NULL DEFAULT '',
    current_agent_id       TEXT REFERENCES agent_profiles(id),
    current_run_id         TEXT REFERENCES execution_runs(id),
    attempt                INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    next_action_at         TEXT,
    blocker_code           TEXT NOT NULL DEFAULT '',
    blocker_message        TEXT NOT NULL DEFAULT '',
    last_error             TEXT NOT NULL DEFAULT '',
    data                   TEXT NOT NULL DEFAULT '{}',
    version                INTEGER NOT NULL DEFAULT 1,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);
CREATE INDEX idx_task_coordinator_states_due
    ON task_coordinator_states(status, next_action_at, updated_at);
CREATE INDEX idx_task_coordinator_states_workspace
    ON task_coordinator_states(workspace_id, status, next_action_at);

CREATE TABLE task_coordinator_events (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    root_work_item_id TEXT NOT NULL REFERENCES work_items(id),
    work_item_id      TEXT NOT NULL REFERENCES work_items(id),
    kind              TEXT NOT NULL,
    summary           TEXT NOT NULL DEFAULT '',
    run_id            TEXT,
    agent_id          TEXT,
    attempt           INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    reason            TEXT NOT NULL DEFAULT '',
    next_action_at    TEXT,
    data              TEXT NOT NULL DEFAULT '{}',
    occurred_at       TEXT NOT NULL
);
CREATE INDEX idx_task_coordinator_events_timeline
    ON task_coordinator_events(root_work_item_id, occurred_at, id);
CREATE INDEX idx_task_coordinator_events_workspace
    ON task_coordinator_events(workspace_id, occurred_at, id);

-- SQLite uses triggers instead of ALTER TABLE CHECK/PLpgSQL functions. These
-- guards keep direct SQL writers from creating a second or cross-scope line.
CREATE TRIGGER agent_profiles_task_coordinator_kind_valid
BEFORE INSERT ON agent_profiles
WHEN NEW.kind NOT IN ('user', 'task_coordinator')
BEGIN
    SELECT RAISE(ABORT, 'agent_profiles.kind must be user or task_coordinator');
END;

CREATE TRIGGER agent_profiles_task_coordinator_kind_update_valid
BEFORE UPDATE OF kind ON agent_profiles
WHEN NEW.kind NOT IN ('user', 'task_coordinator')
BEGIN
    SELECT RAISE(ABORT, 'agent_profiles.kind must be user or task_coordinator');
END;

CREATE TRIGGER agent_profiles_task_coordinator_insert_protected
BEFORE INSERT ON agent_profiles
WHEN NEW.kind = 'task_coordinator'
 AND (NEW.prompt_version <> 'task-coordinator.v1'
      OR NEW.instructions_editable <> 0
      OR COALESCE(json_extract(NEW.policy, '$.sandbox'), '') <> 'read-only'
      OR COALESCE(json_array_length(json_extract(NEW.policy, '$.tools')), 0) <> 0)
BEGIN
    SELECT RAISE(ABORT, 'system task coordinator profile must use the built-in prompt');
END;

CREATE TRIGGER agent_profiles_task_coordinator_protected
BEFORE UPDATE OF kind, instructions, prompt_version, instructions_editable, policy
ON agent_profiles
WHEN OLD.kind = 'task_coordinator'
 AND (NEW.kind <> OLD.kind
      OR NEW.instructions <> OLD.instructions
      OR NEW.prompt_version <> OLD.prompt_version
      OR NEW.instructions_editable <> OLD.instructions_editable
      OR NEW.policy <> OLD.policy)
BEGIN
    SELECT RAISE(ABORT, 'system task coordinator profile is protected');
END;

CREATE TRIGGER agent_profiles_task_coordinator_promote_forbidden
BEFORE UPDATE OF kind ON agent_profiles
WHEN OLD.kind <> 'task_coordinator' AND NEW.kind = 'task_coordinator'
BEGIN
    SELECT RAISE(ABORT, 'system task coordinator profile must be created by EnsureConfig');
END;

CREATE TRIGGER task_coordinator_config_profile_check
BEFORE INSERT ON task_coordinator_configs
WHEN NOT EXISTS (
    SELECT 1 FROM agent_profiles
    WHERE id = NEW.agent_profile_id
      AND workspace_id = NEW.workspace_id
      AND kind = 'task_coordinator'
      AND NEW.prompt_version = 'task-coordinator.v1'
)
BEGIN
    SELECT RAISE(ABORT, 'task coordinator config requires matching system profile');
END;

CREATE TRIGGER task_coordinator_config_profile_update_check
BEFORE UPDATE OF workspace_id, agent_profile_id, prompt_version
ON task_coordinator_configs
WHEN NOT EXISTS (
    SELECT 1 FROM agent_profiles
    WHERE id = NEW.agent_profile_id
      AND workspace_id = NEW.workspace_id
      AND kind = 'task_coordinator'
      AND NEW.prompt_version = 'task-coordinator.v1'
)
BEGIN
    SELECT RAISE(ABORT, 'task coordinator config requires matching system profile');
END;

CREATE TRIGGER task_coordinator_state_root_check
BEFORE INSERT ON task_coordinator_states
WHEN NOT EXISTS (
    SELECT 1 FROM work_items wi
    JOIN agent_profiles ap ON ap.id = NEW.coordinator_agent_id
    WHERE wi.id = NEW.root_work_item_id
      AND wi.workspace_id = NEW.workspace_id
      AND wi.parent_id IS NULL
      AND wi.record_kind = 'task'
      AND ap.workspace_id = NEW.workspace_id
      AND ap.kind = 'task_coordinator'
)
BEGIN
    SELECT RAISE(ABORT, 'coordinator state requires a root task and system profile');
END;

CREATE TRIGGER task_coordinator_state_root_update_check
BEFORE UPDATE OF workspace_id, root_work_item_id, coordinator_agent_id
ON task_coordinator_states
WHEN NOT EXISTS (
    SELECT 1 FROM work_items wi
    JOIN agent_profiles ap ON ap.id = NEW.coordinator_agent_id
    WHERE wi.id = NEW.root_work_item_id
      AND wi.workspace_id = NEW.workspace_id
      AND wi.parent_id IS NULL
      AND wi.record_kind = 'task'
      AND ap.workspace_id = NEW.workspace_id
      AND ap.kind = 'task_coordinator'
)
BEGIN
    SELECT RAISE(ABORT, 'coordinator state requires a root task and system profile');
END;

CREATE TRIGGER task_coordinator_event_scope_check
BEFORE INSERT ON task_coordinator_events
WHEN NOT EXISTS (
    SELECT 1
    FROM work_items root
    JOIN work_items item ON item.id = NEW.work_item_id
    WHERE root.id = NEW.root_work_item_id
      AND root.workspace_id = NEW.workspace_id
      AND root.parent_id IS NULL
      AND root.record_kind = 'task'
      AND item.workspace_id = NEW.workspace_id
      AND item.record_kind = 'task'
      AND EXISTS (
          WITH RECURSIVE descendants(id) AS (
              SELECT id FROM work_items WHERE id = NEW.root_work_item_id
              UNION ALL
              SELECT child.id FROM work_items child
              JOIN descendants parent ON parent.id = child.parent_id
          )
          SELECT 1 FROM descendants WHERE id = NEW.work_item_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'coordinator event requires task-scoped work items');
END;

CREATE TRIGGER task_coordinator_event_scope_update_check
BEFORE UPDATE OF workspace_id, root_work_item_id, work_item_id
ON task_coordinator_events
WHEN NOT EXISTS (
    SELECT 1
    FROM work_items root
    JOIN work_items item ON item.id = NEW.work_item_id
    WHERE root.id = NEW.root_work_item_id
      AND root.workspace_id = NEW.workspace_id
      AND root.parent_id IS NULL
      AND root.record_kind = 'task'
      AND item.workspace_id = NEW.workspace_id
      AND item.record_kind = 'task'
      AND EXISTS (
          WITH RECURSIVE descendants(id) AS (
              SELECT id FROM work_items WHERE id = NEW.root_work_item_id
              UNION ALL
              SELECT child.id FROM work_items child
              JOIN descendants parent ON parent.id = child.parent_id
          )
          SELECT 1 FROM descendants WHERE id = NEW.work_item_id
      )
)
BEGIN
    SELECT RAISE(ABORT, 'coordinator event requires task-scoped work items');
END;

CREATE TRIGGER task_coordinator_event_append_only_update
BEFORE UPDATE ON task_coordinator_events
BEGIN
    SELECT RAISE(ABORT, 'task coordinator events are append-only');
END;

CREATE TRIGGER task_coordinator_event_append_only_delete
BEFORE DELETE ON task_coordinator_events
BEGIN
    SELECT RAISE(ABORT, 'task coordinator events are append-only');
END;
