-- 0048_builtin_knowledge_librarian.sql — expose the protected workspace
-- Knowledge Librarian as a first-class system Agent.

-- The public Agent roster may contain the built-in librarian, while the
-- Task Coordinator remains hidden from ordinary Agent management and worker
-- selection.  Keep one librarian identity per workspace.
DROP TRIGGER agent_profiles_task_coordinator_kind_valid;
DROP TRIGGER agent_profiles_task_coordinator_kind_update_valid;

CREATE UNIQUE INDEX idx_agent_profiles_one_knowledge_librarian
    ON agent_profiles(workspace_id)
    WHERE kind = 'knowledge_librarian';

CREATE TRIGGER agent_profiles_system_kind_valid
BEFORE INSERT ON agent_profiles
WHEN NEW.kind NOT IN ('user', 'task_coordinator', 'knowledge_librarian')
BEGIN
    SELECT RAISE(ABORT, 'agent_profiles.kind must be user, task_coordinator, or knowledge_librarian');
END;

CREATE TRIGGER agent_profiles_system_kind_update_valid
BEFORE UPDATE OF kind ON agent_profiles
WHEN NEW.kind NOT IN ('user', 'task_coordinator', 'knowledge_librarian')
BEGIN
    SELECT RAISE(ABORT, 'agent_profiles.kind must be user, task_coordinator, or knowledge_librarian');
END;

CREATE TRIGGER agent_profiles_knowledge_librarian_insert_protected
BEFORE INSERT ON agent_profiles
WHEN NEW.kind = 'knowledge_librarian'
 AND (NEW.prompt_version <> 'knowledge-librarian-chat/v1'
      OR NEW.instructions_editable <> 0
      OR COALESCE(json_extract(NEW.policy, '$.sandbox'), '') <> 'read-only'
      OR COALESCE(json_array_length(json_extract(NEW.policy, '$.tools')), 0) <> 0)
BEGIN
    SELECT RAISE(ABORT, 'system knowledge librarian profile must use the built-in prompt');
END;

CREATE TRIGGER agent_profiles_knowledge_librarian_protected
BEFORE UPDATE OF kind, instructions, prompt_version, instructions_editable, policy
ON agent_profiles
WHEN OLD.kind = 'knowledge_librarian'
 AND (NEW.kind <> OLD.kind
      OR NEW.instructions <> OLD.instructions
      OR NEW.prompt_version <> OLD.prompt_version
      OR NEW.instructions_editable <> OLD.instructions_editable
      OR NEW.policy <> OLD.policy)
BEGIN
    SELECT RAISE(ABORT, 'system knowledge librarian profile is protected');
END;

CREATE TRIGGER agent_profiles_knowledge_librarian_promote_forbidden
BEFORE UPDATE OF kind ON agent_profiles
WHEN OLD.kind <> 'knowledge_librarian' AND NEW.kind = 'knowledge_librarian'
BEGIN
    SELECT RAISE(ABORT, 'system knowledge librarian profile must be created by system provisioning');
END;
