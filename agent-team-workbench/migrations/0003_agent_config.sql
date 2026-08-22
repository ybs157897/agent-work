-- 0003_agent_config.sql — AgentProfile 文件化配置（agents/<slug>/ 为真相源）。
-- slug 关联配置目录；policy/model_override 为 JSON。

ALTER TABLE agent_profiles ADD COLUMN slug TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN policy JSONB NOT NULL DEFAULT '{}';
ALTER TABLE agent_profiles ADD COLUMN model_override JSONB NOT NULL DEFAULT '{}';
CREATE INDEX idx_agent_profiles_slug ON agent_profiles(workspace_id, slug);
