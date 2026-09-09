-- 0051_chat_analysis.sql — durable Chat requirement-analysis attempts,
-- immutable valid revisions, and developer answers.
CREATE TABLE chat_analyses (
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id   TEXT NOT NULL REFERENCES agent_profiles(id),
    version            INTEGER NOT NULL DEFAULT 0 CHECK (version >= 0),
    revision           INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    status             TEXT NOT NULL CHECK (status IN ('idle','analyzing','needs_answer','ready','failed','stale')),
    current_run_id    TEXT REFERENCES execution_runs(id),
    error              TEXT NOT NULL DEFAULT '',
    document_json      TEXT NOT NULL DEFAULT '',
    document_digest    TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    PRIMARY KEY (workspace_id, chat_work_item_id)
);

CREATE INDEX idx_chat_analyses_current_run ON chat_analyses(current_run_id);

CREATE TABLE chat_analysis_attempts (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id   TEXT NOT NULL REFERENCES agent_profiles(id),
    run_id             TEXT NOT NULL UNIQUE REFERENCES execution_runs(id),
    version            INTEGER NOT NULL CHECK (version >= 1),
    request_digest     TEXT NOT NULL,
    source_catalog_json TEXT NOT NULL,
    status             TEXT NOT NULL CHECK (status IN ('analyzing','succeeded','failed','stale')),
    revision           INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    error              TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE INDEX idx_chat_analysis_attempts_chat
    ON chat_analysis_attempts(workspace_id, chat_work_item_id, created_at, id);

CREATE TABLE chat_analysis_revisions (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id   TEXT NOT NULL REFERENCES agent_profiles(id),
    run_id             TEXT NOT NULL UNIQUE REFERENCES execution_runs(id),
    revision           INTEGER NOT NULL CHECK (revision >= 1),
    document_json      TEXT NOT NULL,
    document_digest    TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    UNIQUE (workspace_id, chat_work_item_id, revision)
);

CREATE INDEX idx_chat_analysis_revisions_chat
    ON chat_analysis_revisions(workspace_id, chat_work_item_id, revision);

CREATE TABLE chat_analysis_answers (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id      TEXT NOT NULL REFERENCES agent_profiles(id),
    revision              INTEGER NOT NULL CHECK (revision >= 1),
    question_id           TEXT NOT NULL,
    question_fingerprint  TEXT NOT NULL,
    selected_option_ids_json TEXT NOT NULL DEFAULT '[]',
    text                  TEXT NOT NULL DEFAULT '',
    disposition           TEXT NOT NULL CHECK (disposition IN ('answered','deferred')),
    source_dependencies_json TEXT NOT NULL DEFAULT '[]',
    client_key            TEXT NOT NULL,
    created_at            TEXT NOT NULL,
    UNIQUE (workspace_id, chat_work_item_id, client_key)
);

CREATE INDEX idx_chat_analysis_answers_revision
    ON chat_analysis_answers(workspace_id, chat_work_item_id, revision, created_at, id);
