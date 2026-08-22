-- 0003_agent_config_sqlite.sql — AgentProfile 文件化配置（SQLite 版，JSON 文本）。
ALTER TABLE agent_profiles ADD COLUMN slug TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN policy TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_profiles ADD COLUMN model_override TEXT NOT NULL DEFAULT '{}';
CREATE INDEX idx_agent_profiles_slug ON agent_profiles(workspace_id, slug);
