-- 0050_chat_sources.sql — opaque original attachments owned by one Chat.
CREATE TABLE chat_sources (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id),
    chat_work_item_id TEXT NOT NULL REFERENCES work_items(id),
    agent_profile_id TEXT NOT NULL REFERENCES agent_profiles(id),
    client_key       TEXT NOT NULL,
    filename         TEXT NOT NULL,
    mime             TEXT NOT NULL DEFAULT '',
    size             INTEGER NOT NULL CHECK (size >= 0),
    opaque_key       TEXT NOT NULL UNIQUE,
    sha256           TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('saved','handed_to_agent','read','failed')),
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    handed_at        TEXT,
    UNIQUE (workspace_id, chat_work_item_id, client_key)
);

CREATE INDEX idx_chat_sources_chat ON chat_sources(workspace_id, chat_work_item_id, created_at, id);
