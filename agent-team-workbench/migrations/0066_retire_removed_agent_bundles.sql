-- 0066_retire_removed_agent_bundles.sql — the removed roster is retired, not deleted.
--
-- The product decision on 2026-09-13 collapses the team roster to two agents,
-- the product agent and the developer agent: agents/ is the config source of
-- truth, and commit 420a824 deleted the nova, pixel and sentinel bundles. A
-- fresh database therefore never grows those profiles — but an existing one
-- keeps them, because the importer only ever creates and updates by slug and
-- deliberately leaves availability/presence alone (internal/agentconfig/
-- importer.go), so a profile whose bundle is gone is simply never written
-- again and stays enabled forever.
--
-- The leftovers cannot be DELETEd. execution_runs, work_items, approval_grants,
-- dispatches, task coordinators and other tables carry foreign keys into
-- agent_profiles(id); a hard delete would either fail or orphan the audit trail
-- of runs that really happened. Disabling is the honest projection of the roster
-- decision: the profile keeps its identity and history, CreateRun refuses to use
-- a disabled Agent (so neither Chat nor Task can pick it up), and the UI moves it
-- into the disabled group.
--
-- Scope is deliberately narrow: ordinary profiles only (kind is '' or 'user')
-- whose slug is one of the three removed bundles. The system identities — Task
-- Coordinator and Knowledge Librarian — carry other kind values and other slugs,
-- and no statement here can reach them.
--
-- Only availability and updated_at are written. version is left alone, matching
-- the on-table precedent of the previous data migration (0057): this is a
-- runtime-state retirement, and bumping version would collide with an in-flight
-- agent config sync intent for the same profile (importer.go compares
-- current.Version against the intent's frozen target version). updated_at uses
-- the same RFC3339-with-Z shape the store writes (sqlstore.timeParam →
-- time.RFC3339Nano) so the column stays byte-homogeneous with every row the
-- application writes.
--
-- The statement is re-entrant: it only touches rows that are still enabled, so
-- applying it twice (or to a database that already retired them) is a no-op.

UPDATE agent_profiles
SET availability = 'disabled',
    updated_at   = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE slug IN ('nova', 'pixel', 'sentinel')
  AND kind IN ('', 'user')
  AND availability <> 'disabled';
