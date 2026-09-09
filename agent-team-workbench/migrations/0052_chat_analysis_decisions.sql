-- 0052_chat_analysis_decisions.sql — immutable product conclusions,
-- current decision usability, reopen history, and answer lineage.
ALTER TABLE chat_analysis_answers ADD COLUMN inherited_from_answer_id TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_analysis_answers ADD COLUMN lineage_reason TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_chat_analysis_answers_lineage
    ON chat_analysis_answers(workspace_id, chat_work_item_id, inherited_from_answer_id);

CREATE TABLE chat_analysis_decisions (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id      TEXT NOT NULL REFERENCES agent_profiles(id),
    revision              INTEGER NOT NULL CHECK (revision >= 1),
    item_id               TEXT NOT NULL,
    outcome               TEXT NOT NULL CHECK (outcome IN ('confirmed','rejected','needs_clarification')),
    conclusion            TEXT NOT NULL,
    basis                 TEXT NOT NULL,
    product_version       TEXT NOT NULL,
    item_fingerprint      TEXT NOT NULL,
    source_dependencies_json TEXT NOT NULL DEFAULT '[]',
    actor_id              TEXT NOT NULL,
    client_key            TEXT NOT NULL,
    created_at            TEXT NOT NULL,
    UNIQUE (workspace_id, chat_work_item_id, client_key)
);

CREATE INDEX idx_chat_analysis_decisions_chat
    ON chat_analysis_decisions(workspace_id, chat_work_item_id, revision, created_at, id);

CREATE TABLE chat_analysis_decision_states (
    workspace_id          TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id     TEXT NOT NULL REFERENCES work_items(id),
    item_id               TEXT NOT NULL,
    revision              INTEGER NOT NULL CHECK (revision >= 1),
    decision_id           TEXT NOT NULL REFERENCES chat_analysis_decisions(id),
    outcome               TEXT NOT NULL CHECK (outcome IN ('confirmed','rejected','needs_clarification')),
    conclusion            TEXT NOT NULL,
    basis                 TEXT NOT NULL,
    product_version       TEXT NOT NULL,
    item_fingerprint      TEXT NOT NULL,
    source_dependencies_json TEXT NOT NULL DEFAULT '[]',
    status                TEXT NOT NULL CHECK (status IN ('valid','needs_reconfirmation','stale')),
    review_reason         TEXT NOT NULL DEFAULT '',
    updated_at            TEXT NOT NULL,
    PRIMARY KEY (workspace_id, chat_work_item_id, item_id)
);

CREATE INDEX idx_chat_analysis_decision_states_revision
    ON chat_analysis_decision_states(workspace_id, chat_work_item_id, revision, status);

CREATE TABLE chat_analysis_reopens (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    item_id            TEXT NOT NULL,
    from_revision      INTEGER NOT NULL CHECK (from_revision >= 1),
    to_revision        INTEGER NOT NULL CHECK (to_revision >= 1),
    reason             TEXT NOT NULL,
    source_json        TEXT NOT NULL DEFAULT '[]',
    created_at         TEXT NOT NULL,
    UNIQUE (workspace_id, chat_work_item_id, item_id, from_revision, to_revision, reason)
);

CREATE INDEX idx_chat_analysis_reopens_chat
    ON chat_analysis_reopens(workspace_id, chat_work_item_id, created_at, id);
