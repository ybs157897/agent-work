-- 0055_task_publication_expected_version.sql — publish request CAS survives
-- databases that already applied 0054 before this field was introduced.
ALTER TABLE task_publications ADD COLUMN expected_version INTEGER NOT NULL DEFAULT 0;

-- Existing publication rows were created from the draft's previous version:
-- UpdateStatus increments the draft version after accepting the original publish
-- request. Preserve that request's expected version for deterministic replay.
UPDATE task_publications
SET expected_version = CASE
    WHEN (SELECT version FROM task_publication_drafts d WHERE d.id=task_publications.draft_id) > 1
    THEN (SELECT version - 1 FROM task_publication_drafts d WHERE d.id=task_publications.draft_id)
    ELSE 1
END
WHERE expected_version = 0;
