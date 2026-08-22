-- 0002_runtime_binding_model_config.sql — RuntimeBinding 模型配置（SQLite 版）
ALTER TABLE runtime_bindings ADD COLUMN provider TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_bindings ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_bindings ADD COLUMN credential_ref TEXT;
