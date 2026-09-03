-- 0035_governance_canonical_usage_terminal.sql — terminal-only canonical usage.
--
-- Provider usage reports are progress observations and may be written while
-- a Run is active.  canonical_usage is different: it is the immutable
-- accounting snapshot and must only exist after the Run has reached a terminal
-- state.  Keep this invariant in SQLite as well as at the Go repository
-- boundary so direct writers cannot create a premature settlement snapshot.

CREATE TRIGGER execution_runs_canonical_usage_terminal_insert
BEFORE INSERT ON execution_runs
WHEN (NEW.canonical_usage IS NOT NULL OR NEW.canonical_usage_digest IS NOT NULL)
 AND NEW.status NOT IN ('succeeded','interrupted','cancelled','lost','failed')
BEGIN
    SELECT RAISE(ABORT, 'execution run canonical usage requires terminal status');
END;

CREATE TRIGGER execution_runs_canonical_usage_terminal_update
BEFORE UPDATE OF status, canonical_usage, canonical_usage_digest ON execution_runs
WHEN (NEW.canonical_usage IS NOT NULL OR NEW.canonical_usage_digest IS NOT NULL)
 AND NEW.status NOT IN ('succeeded','interrupted','cancelled','lost','failed')
BEGIN
    SELECT RAISE(ABORT, 'execution run canonical usage requires terminal status');
END;

-- Do not silently grandfather a snapshot written through an old direct SQL
-- path. This no-op update invokes the trigger for every pre-existing
-- violation, aborting the migration transaction so an operator must reconcile
-- the row before the invariant is declared installed.
UPDATE execution_runs
SET canonical_usage = canonical_usage
WHERE (canonical_usage IS NOT NULL OR canonical_usage_digest IS NOT NULL)
  AND status NOT IN ('succeeded','interrupted','cancelled','lost','failed');
