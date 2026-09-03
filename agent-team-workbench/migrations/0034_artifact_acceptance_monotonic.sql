-- 0034_artifact_acceptance_monotonic.sql — accepted artifacts are immutable.
--
-- An accepted Artifact may be referenced as governance evidence. Allowing it
-- to move back to draft would make an already accepted evidence claim
-- reversible without a new reviewer decision or canonical event.
CREATE TRIGGER artifacts_acceptance_monotonic
BEFORE UPDATE OF status ON artifacts
WHEN OLD.status = 'accepted' AND NEW.status <> 'accepted'
BEGIN
    SELECT RAISE(ABORT, 'accepted artifacts are immutable');
END;
