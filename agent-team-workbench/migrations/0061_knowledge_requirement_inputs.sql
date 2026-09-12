-- 0061_knowledge_requirement_inputs.sql — the requirement text an event
-- imported, frozen at acceptance time as a content-addressed input. The text a
-- task documents must be the text that was accepted, not whatever the
-- referenced path contains when the task reaches the head, and the librarian
-- must be able to cite it as evidence like any other source.
CREATE TABLE knowledge_requirement_inputs (
    id                  TEXT PRIMARY KEY,
    library_id          TEXT NOT NULL REFERENCES knowledge_libraries(id),
    event_id            TEXT REFERENCES knowledge_library_events(id),
    source_id           TEXT NOT NULL REFERENCES knowledge_library_sources(id),
    requirement_id      TEXT NOT NULL,
    requirement_version TEXT NOT NULL DEFAULT '',
    title               TEXT NOT NULL DEFAULT '',
    content_ref         TEXT NOT NULL DEFAULT '',
    stored_path         TEXT NOT NULL,
    content_digest      TEXT NOT NULL,
    byte_size           INTEGER NOT NULL CHECK (byte_size >= 0),
    payload_json        TEXT NOT NULL DEFAULT '{}',
    created_at          DATETIME NOT NULL
);
CREATE INDEX idx_knowledge_requirement_inputs_library
    ON knowledge_requirement_inputs(library_id, requirement_id, created_at DESC);

ALTER TABLE knowledge_write_tasks ADD COLUMN requirement_input_id TEXT;
