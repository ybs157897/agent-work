-- 0020_task_coordinator.sql — 系统级 Task Coordinator 配置、状态与事件。
-- 每个 Workspace 只有一个受保护 coordinator profile；每个根 Task 只有一个
-- coordinator state，子 WorkItem 通过 parent_id 解析到该根控制线。

ALTER TABLE agent_profiles
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'user';
ALTER TABLE agent_profiles
    ADD COLUMN prompt_version TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles
    ADD COLUMN instructions_editable BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE agent_profiles
    ADD CONSTRAINT agent_profiles_kind_check
    CHECK (kind IN ('user', 'task_coordinator'));

CREATE UNIQUE INDEX idx_agent_profiles_one_task_coordinator
    ON agent_profiles(workspace_id)
    WHERE kind = 'task_coordinator';

CREATE OR REPLACE FUNCTION protect_task_coordinator_profile()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
	IF TG_OP = 'INSERT' AND NEW.kind = 'task_coordinator' AND (
		NEW.prompt_version <> 'task-coordinator.v1'
		OR NEW.instructions_editable
		OR COALESCE(NEW.policy->>'sandbox', '') <> 'read-only'
		OR COALESCE(jsonb_array_length(NEW.policy->'tools'), 0) <> 0
	) THEN
		RAISE EXCEPTION 'system task coordinator profile must use the built-in prompt';
	END IF;
	IF TG_OP = 'UPDATE' AND OLD.kind = 'task_coordinator' AND (
        NEW.kind <> OLD.kind
		OR NEW.instructions <> OLD.instructions
		OR NEW.prompt_version <> OLD.prompt_version
		OR NEW.instructions_editable <> OLD.instructions_editable
		OR NEW.policy <> OLD.policy
    ) THEN
        RAISE EXCEPTION 'system task coordinator profile is protected';
    END IF;
	IF TG_OP = 'UPDATE' AND OLD.kind <> 'task_coordinator' AND NEW.kind = 'task_coordinator' THEN
        RAISE EXCEPTION 'system task coordinator profile must be created by EnsureConfig';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_profiles_task_coordinator_protected
BEFORE INSERT OR UPDATE OF kind, instructions, prompt_version, instructions_editable, policy
ON agent_profiles
FOR EACH ROW EXECUTE FUNCTION protect_task_coordinator_profile();

CREATE TABLE task_coordinator_configs (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    agent_profile_id      TEXT NOT NULL UNIQUE REFERENCES agent_profiles(id),
    prompt_version        TEXT NOT NULL,
    runtime_label         TEXT NOT NULL DEFAULT 'mock',
    fallback_runtime_label TEXT,
    model_ref             JSONB NOT NULL DEFAULT '{}',
    fallback_model_ref    JSONB NOT NULL DEFAULT '{}',
    reasoning_effort      TEXT NOT NULL DEFAULT 'medium',
    version               INTEGER NOT NULL DEFAULT 1,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id)
);
CREATE INDEX idx_task_coordinator_configs_workspace
    ON task_coordinator_configs(workspace_id);

CREATE TABLE task_coordinator_states (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    root_work_item_id     TEXT NOT NULL UNIQUE REFERENCES work_items(id),
    coordinator_agent_id  TEXT NOT NULL REFERENCES agent_profiles(id),
    status                TEXT NOT NULL DEFAULT 'queued'
                          CHECK (status IN ('queued','running','waiting_retry',
                                            'waiting_user','blocked','completed','cancelled')),
    phase                 TEXT NOT NULL DEFAULT '',
    summary               TEXT NOT NULL DEFAULT '',
    current_action        TEXT NOT NULL DEFAULT '',
    current_step          TEXT NOT NULL DEFAULT '',
    current_agent_id      TEXT REFERENCES agent_profiles(id),
    current_run_id        TEXT REFERENCES execution_runs(id),
    attempt               INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    next_action_at        TIMESTAMPTZ,
    blocker_code          TEXT NOT NULL DEFAULT '',
    blocker_message       TEXT NOT NULL DEFAULT '',
    last_error            TEXT NOT NULL DEFAULT '',
    data                  JSONB NOT NULL DEFAULT '{}',
    version               INTEGER NOT NULL DEFAULT 1,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
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
    next_action_at    TIMESTAMPTZ,
    data              JSONB NOT NULL DEFAULT '{}',
    occurred_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_task_coordinator_events_timeline
    ON task_coordinator_events(root_work_item_id, occurred_at, id);
CREATE INDEX idx_task_coordinator_events_workspace
    ON task_coordinator_events(workspace_id, occurred_at, id);

-- Database-level protection for callers that bypass the application port.
CREATE OR REPLACE FUNCTION validate_task_coordinator_profile()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    profile_workspace TEXT;
    profile_kind TEXT;
BEGIN
    SELECT workspace_id, kind INTO profile_workspace, profile_kind
      FROM agent_profiles WHERE id = NEW.agent_profile_id;
    IF profile_workspace IS NULL
       OR profile_workspace <> NEW.workspace_id
       OR profile_kind <> 'task_coordinator' THEN
        RAISE EXCEPTION 'task coordinator config requires matching system profile';
    END IF;
    IF NEW.prompt_version <> 'task-coordinator.v1' THEN
        RAISE EXCEPTION 'unsupported task coordinator prompt version';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER task_coordinator_config_profile_check
BEFORE INSERT OR UPDATE OF workspace_id, agent_profile_id, prompt_version
ON task_coordinator_configs
FOR EACH ROW EXECUTE FUNCTION validate_task_coordinator_profile();

CREATE OR REPLACE FUNCTION validate_task_coordinator_state_root()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    root_workspace TEXT;
    root_parent TEXT;
    root_kind TEXT;
    profile_workspace TEXT;
    profile_kind TEXT;
BEGIN
    SELECT workspace_id, parent_id, record_kind
      INTO root_workspace, root_parent, root_kind
      FROM work_items WHERE id = NEW.root_work_item_id;
    SELECT workspace_id, kind INTO profile_workspace, profile_kind
      FROM agent_profiles WHERE id = NEW.coordinator_agent_id;
    IF root_workspace IS NULL OR root_workspace <> NEW.workspace_id
       OR root_parent IS NOT NULL OR root_kind <> 'task' THEN
        RAISE EXCEPTION 'coordinator state requires a root task';
    END IF;
    IF profile_workspace IS NULL OR profile_workspace <> NEW.workspace_id
       OR profile_kind <> 'task_coordinator' THEN
        RAISE EXCEPTION 'coordinator state requires matching system profile';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER task_coordinator_state_root_check
BEFORE INSERT OR UPDATE OF workspace_id, root_work_item_id, coordinator_agent_id
ON task_coordinator_states
FOR EACH ROW EXECUTE FUNCTION validate_task_coordinator_state_root();

CREATE OR REPLACE FUNCTION validate_task_coordinator_event_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    root_workspace TEXT;
    root_parent TEXT;
    root_kind TEXT;
    event_workspace TEXT;
    event_kind TEXT;
BEGIN
    SELECT workspace_id, parent_id, record_kind
      INTO root_workspace, root_parent, root_kind
      FROM work_items WHERE id = NEW.root_work_item_id;
    SELECT workspace_id, record_kind INTO event_workspace, event_kind
      FROM work_items WHERE id = NEW.work_item_id;
    IF root_workspace IS NULL OR root_workspace <> NEW.workspace_id
       OR root_parent IS NOT NULL OR root_kind <> 'task'
       OR event_workspace IS NULL OR event_workspace <> NEW.workspace_id
       OR event_kind <> 'task'
       OR NOT EXISTS (
           WITH RECURSIVE descendants(id) AS (
               SELECT id FROM work_items WHERE id = NEW.root_work_item_id
               UNION ALL
               SELECT child.id FROM work_items child
               JOIN descendants parent ON parent.id = child.parent_id
           )
           SELECT 1 FROM descendants WHERE id = NEW.work_item_id
       ) THEN
        RAISE EXCEPTION 'coordinator event requires task-scoped work items';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER task_coordinator_event_scope_check
BEFORE INSERT OR UPDATE OF workspace_id, root_work_item_id, work_item_id
ON task_coordinator_events
FOR EACH ROW EXECUTE FUNCTION validate_task_coordinator_event_scope();

CREATE OR REPLACE FUNCTION reject_task_coordinator_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'task coordinator events are append-only';
END;
$$;

CREATE TRIGGER task_coordinator_event_append_only_update
BEFORE UPDATE ON task_coordinator_events
FOR EACH ROW EXECUTE FUNCTION reject_task_coordinator_event_mutation();

CREATE TRIGGER task_coordinator_event_append_only_delete
BEFORE DELETE ON task_coordinator_events
FOR EACH ROW EXECUTE FUNCTION reject_task_coordinator_event_mutation();
