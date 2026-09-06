-- 0046_knowledge_capture.sql — durable post-terminal knowledge inbox.
-- The row is an outbox-style receipt for an existing Run. It never becomes a
-- second execution authority and is retained after processing for audit.

CREATE TABLE knowledge_run_captures (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id),
    run_id          TEXT NOT NULL UNIQUE REFERENCES execution_runs(id),
    created_at      DATETIME NOT NULL,
    processed_at    DATETIME,
    submission_id   TEXT REFERENCES knowledge_submissions(id),
    last_error      TEXT NOT NULL DEFAULT '',
    next_attempt_at DATETIME,
    version         INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1)
);
CREATE INDEX idx_knowledge_run_captures_pending
    ON knowledge_run_captures(processed_at, next_attempt_at, created_at, id);

CREATE TRIGGER knowledge_run_captures_workspace_match_insert
BEFORE INSERT ON knowledge_run_captures
WHEN NOT EXISTS (
    SELECT 1 FROM execution_runs r
    WHERE r.id=NEW.run_id AND r.workspace_id=NEW.workspace_id
)
BEGIN
    SELECT RAISE(ABORT, 'knowledge capture Run is outside workspace');
END;

CREATE TRIGGER knowledge_run_captures_immutable_identity_update
BEFORE UPDATE OF id, workspace_id, run_id, created_at
ON knowledge_run_captures
WHEN OLD.id IS NOT NEW.id
  OR OLD.workspace_id IS NOT NEW.workspace_id
  OR OLD.run_id IS NOT NEW.run_id
  OR OLD.created_at IS NOT NEW.created_at
BEGIN
    SELECT RAISE(ABORT, 'knowledge capture identity is immutable');
END;

CREATE TRIGGER knowledge_run_captures_processed_shape_insert
BEFORE INSERT ON knowledge_run_captures
WHEN (NEW.processed_at IS NULL AND NEW.submission_id IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge capture processed shape');
END;

CREATE TRIGGER knowledge_run_captures_processed_shape_update
BEFORE UPDATE OF processed_at, submission_id ON knowledge_run_captures
WHEN (NEW.processed_at IS NULL AND NEW.submission_id IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'invalid knowledge capture processed shape');
END;

CREATE TRIGGER knowledge_run_captures_immutable_delete
BEFORE DELETE ON knowledge_run_captures
BEGIN
    SELECT RAISE(ABORT, 'knowledge capture history is immutable');
END;
