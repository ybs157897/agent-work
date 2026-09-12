-- 0060_knowledge_task_client_keys.sql — a queue request may carry a caller
-- key so a retried submission returns the original receipt instead of
-- enqueueing a second write. Nullable: most tasks come from events, which are
-- already deduplicated by the event's own client_key.
ALTER TABLE knowledge_write_tasks ADD COLUMN client_key TEXT;
CREATE UNIQUE INDEX idx_knowledge_write_tasks_client_key
    ON knowledge_write_tasks(library_id, client_key) WHERE client_key IS NOT NULL;
