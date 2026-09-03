-- 0038_agent_config_sync_intent_applied_immutable.sql — freeze terminal rows.
--
-- 0036 already prevented applied -> non-applied status transitions and target
-- mutation.  This closes the remaining direct-SQL path that could rewrite
-- recovery/audit metadata on an applied intent.
CREATE TRIGGER agent_config_sync_intent_applied_row_immutable
BEFORE UPDATE ON agent_config_sync_intents
WHEN OLD.status = 'applied'
 AND (NEW.id IS NOT OLD.id
      OR NEW.agent_profile_id IS NOT OLD.agent_profile_id
      OR NEW.workspace_id IS NOT OLD.workspace_id
      OR NEW.target_version IS NOT OLD.target_version
      OR NEW.target_snapshot IS NOT OLD.target_snapshot
      OR NEW.target_digest IS NOT OLD.target_digest
      OR NEW.status IS NOT OLD.status
      OR NEW.last_error IS NOT OLD.last_error
      OR NEW.attempts IS NOT OLD.attempts
      OR NEW.version IS NOT OLD.version
      OR NEW.created_at IS NOT OLD.created_at
      OR NEW.updated_at IS NOT OLD.updated_at
      OR NEW.applied_at IS NOT OLD.applied_at)
BEGIN
    SELECT RAISE(ABORT, 'applied agent config sync intent row is immutable');
END;

CREATE TRIGGER agent_config_sync_intent_applied_delete_guard
BEFORE DELETE ON agent_config_sync_intents
WHEN OLD.status = 'applied'
BEGIN
    SELECT RAISE(ABORT, 'applied agent config sync intent row is immutable');
END;
