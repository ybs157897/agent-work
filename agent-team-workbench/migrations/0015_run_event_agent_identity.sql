-- 0015_run_event_agent_identity.sql — 主/子 Agent 事件身份（可选，旧行视为 main）。
-- run_seq 仍是单个 Run 的全序；agent_id 仅用于同一序列中的事件归属。
ALTER TABLE run_events ADD COLUMN agent_id TEXT;
CREATE INDEX idx_run_events_run_agent_seq ON run_events(run_id, agent_id, run_seq);
