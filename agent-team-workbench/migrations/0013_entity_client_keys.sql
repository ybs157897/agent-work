-- 0013_entity_client_keys.sql — 实体级幂等键（SQLite）。
-- work_items/execution_runs 增加
-- client_key 业务意图去重键；部分唯一索引，NULL 不参与唯一约束。
ALTER TABLE work_items ADD COLUMN client_key TEXT;
CREATE UNIQUE INDEX idx_work_items_ws_client_key
    ON work_items(workspace_id, client_key) WHERE client_key IS NOT NULL;

ALTER TABLE execution_runs ADD COLUMN client_key TEXT;
CREATE UNIQUE INDEX idx_execution_runs_ws_client_key
    ON execution_runs(workspace_id, client_key) WHERE client_key IS NOT NULL;
