-- 0054_task_publication_client_keys.sql — publish command entity idempotency.
ALTER TABLE task_publications ADD COLUMN client_key TEXT NOT NULL DEFAULT '';
ALTER TABLE task_publications ADD COLUMN confirmation_ids_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE task_publications ADD COLUMN source_dependencies_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE task_publications ADD COLUMN baseline_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE task_publications ADD COLUMN context_snapshot_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_task_publications_workspace_client_key
    ON task_publications(workspace_id, client_key)
    WHERE client_key <> '';
