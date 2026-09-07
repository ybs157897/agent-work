-- 0045_knowledge_jobs.sql — multi-turn knowledge Harness state.
--
-- A job is application control state around ordinary WorkItem/Run rows.  It
-- never replaces the Run state machine, provider session anchor, lease, or
-- usage ledger.  JSON columns hold the evolving bounded coverage/evidence
-- frontier; action receipts provide one replay fence per model turn.

CREATE TABLE knowledge_jobs (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    -- Human callers use the reserved identity "human:shared"; only the
    -- execution librarian is a persisted Agent foreign key.
    requesting_agent_id   TEXT NOT NULL,
    source_run_id         TEXT REFERENCES execution_runs(id),
    submission_id         TEXT REFERENCES knowledge_submissions(id),
    agent_profile_id      TEXT NOT NULL REFERENCES agent_profiles(id),
    work_item_id          TEXT NOT NULL UNIQUE REFERENCES work_items(id),
    current_run_id        TEXT REFERENCES execution_runs(id),
    mode                  TEXT NOT NULL CHECK (mode IN ('inquiry','curation')),
    status                TEXT NOT NULL CHECK (status IN (
                              'queued','running','waiting_retry','completed',
                              'incomplete','conflict','cancelled','failed')),
    question              TEXT NOT NULL,
    context               TEXT NOT NULL DEFAULT '',
    scope_json            TEXT NOT NULL DEFAULT '{}',
    budget_json           TEXT NOT NULL DEFAULT '{}',
    used_json             TEXT NOT NULL DEFAULT '{}',
    coverage_json         TEXT NOT NULL DEFAULT '{}',
    evidence_json         TEXT NOT NULL DEFAULT '[]',
    visited_versions_json TEXT NOT NULL DEFAULT '[]',
    required_item_ids_json TEXT NOT NULL DEFAULT '[]',
    required_relations_json TEXT NOT NULL DEFAULT '[]',
    index_revision         INTEGER NOT NULL DEFAULT 0 CHECK (index_revision >= 0),
    snapshot_ids_json      TEXT NOT NULL DEFAULT '[]',
    frontier_json         TEXT NOT NULL DEFAULT '[]',
    observations_json     TEXT NOT NULL DEFAULT '[]',
    result_json           TEXT NOT NULL DEFAULT '{}',
    turn_seq              INTEGER NOT NULL DEFAULT 0 CHECK (turn_seq >= 0),
    retry_count           INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    repair_attempt        INTEGER NOT NULL DEFAULT 0 CHECK (repair_attempt >= 0),
    next_action_at        DATETIME,
    last_decision_digest  TEXT NOT NULL DEFAULT '',
    last_error            TEXT NOT NULL DEFAULT '',
    client_key            TEXT NOT NULL,
    version               INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at            DATETIME NOT NULL,
    updated_at            DATETIME NOT NULL,
    finished_at           DATETIME,
    UNIQUE (workspace_id, requesting_agent_id, client_key)
);
CREATE INDEX idx_knowledge_jobs_runnable
    ON knowledge_jobs(status, next_action_at, updated_at);
CREATE INDEX idx_knowledge_jobs_workspace
    ON knowledge_jobs(workspace_id, requesting_agent_id, created_at DESC);
CREATE INDEX idx_knowledge_jobs_run
    ON knowledge_jobs(current_run_id);

CREATE TABLE knowledge_job_actions (
    id              TEXT PRIMARY KEY,
    job_id          TEXT NOT NULL REFERENCES knowledge_jobs(id),
    turn_seq        INTEGER NOT NULL CHECK (turn_seq >= 1),
    run_id          TEXT NOT NULL REFERENCES execution_runs(id),
    action_kind     TEXT NOT NULL CHECK (action_kind IN ('search','read','relations','finish')),
    status          TEXT NOT NULL CHECK (status IN ('pending','applied','failed')),
    request_digest  TEXT NOT NULL,
    request_json    TEXT NOT NULL DEFAULT '{}',
    result_json     TEXT NOT NULL DEFAULT '{}',
    error_message   TEXT NOT NULL DEFAULT '',
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL,
    UNIQUE (job_id, turn_seq)
);
CREATE INDEX idx_knowledge_job_actions_run
    ON knowledge_job_actions(run_id, turn_seq);

CREATE TRIGGER knowledge_job_actions_immutable_request_update
BEFORE UPDATE OF job_id, turn_seq, run_id, action_kind, request_digest,
                 request_json, created_at
ON knowledge_job_actions
WHEN OLD.job_id IS NOT NEW.job_id
  OR OLD.turn_seq IS NOT NEW.turn_seq
  OR OLD.run_id IS NOT NEW.run_id
  OR OLD.action_kind IS NOT NEW.action_kind
  OR OLD.request_digest IS NOT NEW.request_digest
  OR OLD.request_json IS NOT NEW.request_json
  OR OLD.created_at IS NOT NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'knowledge job action request is immutable');
END;

CREATE TABLE knowledge_librarian_configs (
    workspace_id       TEXT PRIMARY KEY REFERENCES workspaces(id),
    librarian_agent_id TEXT REFERENCES agent_profiles(id),
    enabled            INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    auto_collect       INTEGER NOT NULL DEFAULT 0 CHECK (auto_collect IN (0,1)),
    version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at         DATETIME NOT NULL,
    updated_at         DATETIME NOT NULL
);
