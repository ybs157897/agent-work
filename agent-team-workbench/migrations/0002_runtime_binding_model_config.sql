-- 0002_runtime_binding_model_config.sql — RuntimeBinding 模型配置（设置页）
-- 模型配置归属 RuntimeBinding：provider + model + CredentialRef。
-- 凭据只存引用，明文留在 Runner 本地安全存储，绝不进入浏览器。
ALTER TABLE runtime_bindings ADD COLUMN provider TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_bindings ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE runtime_bindings ADD COLUMN credential_ref TEXT;
