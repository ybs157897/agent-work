-- 0013_entity_client_keys.sql — 实体级幂等键：work_items/execution_runs 增加
-- client_key（客户端业务意图去重键，对齐 ClawTeam TaskItem.idempotency_key 语义）。
-- 命令级幂等（idempotency_keys 表）防同一请求重放；client_key 防同一业务意图
-- （队列 drain 重试、分叉双击）重复建实体。部分唯一索引：NULL 不参与唯一约束。

ALTER TABLE work_items ADD COLUMN client_key TEXT;
CREATE UNIQUE INDEX idx_work_items_ws_client_key
    ON work_items(workspace_id, client_key) WHERE client_key IS NOT NULL;

ALTER TABLE execution_runs ADD COLUMN client_key TEXT;
CREATE UNIQUE INDEX idx_execution_runs_ws_client_key
    ON execution_runs(workspace_id, client_key) WHERE client_key IS NOT NULL;
