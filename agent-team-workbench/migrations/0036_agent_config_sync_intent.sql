-- 0036_agent_config_sync_intent.sql — durable Agent external-config intent.
--
-- Agent CAS/event and this intent are written by one application transaction.
-- The SQLite row is the recovery authority; agents/ and runtime config files
-- are replayable effects and never participate in a fake cross-medium commit.
CREATE TABLE agent_config_sync_intents (
    id              TEXT PRIMARY KEY,
    agent_profile_id TEXT NOT NULL REFERENCES agent_profiles(id),
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id),
    target_version  INTEGER NOT NULL CHECK (target_version > 0),
    target_snapshot TEXT NOT NULL CHECK (json_valid(target_snapshot)),
    target_digest   TEXT NOT NULL CHECK (length(target_digest) = 71 AND substr(target_digest, 1, 7) = 'sha256:'),
    status          TEXT NOT NULL CHECK (status IN ('pending','failed','conflict','applied')),
    last_error      TEXT NOT NULL DEFAULT '',
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL,
    applied_at      DATETIME
);

CREATE UNIQUE INDEX agent_config_sync_intents_one_active
    ON agent_config_sync_intents(agent_profile_id)
    WHERE status <> 'applied';
CREATE INDEX agent_config_sync_intents_active_order
    ON agent_config_sync_intents(status, updated_at, id)
    WHERE status <> 'applied';
CREATE INDEX agent_config_sync_intents_workspace
    ON agent_config_sync_intents(workspace_id, updated_at, id);

CREATE TRIGGER agent_config_sync_intent_workspace_guard
BEFORE INSERT ON agent_config_sync_intents
WHEN (SELECT workspace_id FROM agent_profiles WHERE id = NEW.agent_profile_id) <> NEW.workspace_id
BEGIN
    SELECT RAISE(ABORT, 'agent config sync intent crosses workspace boundary');
END;

CREATE TRIGGER agent_config_sync_intent_version_guard
BEFORE INSERT ON agent_config_sync_intents
WHEN (SELECT version FROM agent_profiles WHERE id = NEW.agent_profile_id) <> NEW.target_version
BEGIN
    SELECT RAISE(ABORT, 'agent config sync intent target version is not current');
END;

CREATE TRIGGER agent_config_sync_intent_target_immutable
BEFORE UPDATE OF id, agent_profile_id, workspace_id, target_version, target_snapshot, target_digest
ON agent_config_sync_intents
WHEN OLD.id <> NEW.id
  OR OLD.agent_profile_id <> NEW.agent_profile_id
  OR OLD.workspace_id <> NEW.workspace_id
  OR OLD.target_version <> NEW.target_version
  OR OLD.target_snapshot <> NEW.target_snapshot
  OR OLD.target_digest <> NEW.target_digest
BEGIN
    SELECT RAISE(ABORT, 'agent config sync intent target is immutable');
END;

CREATE TRIGGER agent_config_sync_intent_applied_monotonic
BEFORE UPDATE OF status ON agent_config_sync_intents
WHEN OLD.status = 'applied' AND NEW.status <> 'applied'
BEGIN
    SELECT RAISE(ABORT, 'applied agent config sync intent is immutable');
END;

CREATE TRIGGER agent_config_sync_intent_applied_at_guard
BEFORE INSERT ON agent_config_sync_intents
WHEN NEW.status = 'applied' OR NEW.applied_at IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'new agent config sync intent cannot be applied');
END;

CREATE TRIGGER agent_config_sync_intent_status_timestamp_guard
BEFORE UPDATE OF status, applied_at ON agent_config_sync_intents
WHEN (NEW.status = 'applied' AND NEW.applied_at IS NULL)
  OR (NEW.status <> 'applied' AND NEW.applied_at IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'agent config sync intent status/timestamp mismatch');
END;
