-- 0053_task_publication_drafts.sql — U06 server-owned task publication drafts.
CREATE TABLE task_publication_drafts (
    id                       TEXT PRIMARY KEY,
    workspace_id             TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id        TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id         TEXT NOT NULL REFERENCES agent_profiles(id),
    analysis_revision        INTEGER NOT NULL CHECK (analysis_revision >= 1),
    item_ids_json            TEXT NOT NULL,
    confirmation_ids_json    TEXT NOT NULL,
    source_dependencies_json TEXT NOT NULL,
    title                    TEXT NOT NULL,
    description              TEXT NOT NULL DEFAULT '',
    acceptance_criteria_json TEXT NOT NULL,
    context_snapshot_id      TEXT NOT NULL REFERENCES execution_context_snapshots(id),
    baseline_json            TEXT NOT NULL,
    fingerprint              TEXT NOT NULL,
    status                   TEXT NOT NULL CHECK (status IN ('ready','stale','published')),
    task_id                  TEXT REFERENCES work_items(id),
    client_key               TEXT NOT NULL,
    version                  INTEGER NOT NULL DEFAULT 1,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    UNIQUE (workspace_id, chat_work_item_id, client_key)
);

CREATE INDEX idx_task_publication_drafts_chat
    ON task_publication_drafts(workspace_id, chat_work_item_id, created_at, id);

CREATE TABLE task_publications (
    id               TEXT PRIMARY KEY,
    draft_id         TEXT NOT NULL UNIQUE REFERENCES task_publication_drafts(id),
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id TEXT NOT NULL REFERENCES work_items(id),
    task_id          TEXT NOT NULL REFERENCES work_items(id),
    analysis_revision INTEGER NOT NULL,
    fingerprint      TEXT NOT NULL,
    created_at       TEXT NOT NULL
);
