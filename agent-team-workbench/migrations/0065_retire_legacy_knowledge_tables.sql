-- 0065_retire_legacy_knowledge_tables.sql — the retired per-item librarian is
-- gone, not merely unreferenced.
--
-- 0057 kept the old tables byte-for-byte so existing knowledge stayed readable
-- for recovery, and pinned the retirement as a code-level fact only. That was a
-- deliberate compromise; the product decision on 2026-09-12 is that none of the
-- old administrator's functionality or data is wanted any more, so this
-- migration drops its storage objects outright.
--
-- Nothing in the new library depends on them: no Go source names them
-- (internal/persistence/sqlstore/knowledge_library_retirement_test.go), no
-- foreign key in any other table points at them, and no trigger on a retained
-- table reads them. The new library keeps its own namespace (knowledge_libraries,
-- knowledge_library_*, knowledge_document*, knowledge_assertion*, knowledge_release*,
-- knowledge_write_tasks, knowledge_search_index, ...).
--
-- Dropped (15 objects):
--   knowledge_items, knowledge_versions, knowledge_sources,
--   knowledge_version_sources, knowledge_relations, knowledge_relation_sources,
--   knowledge_repeals, knowledge_submissions, knowledge_query_snapshots,
--   knowledge_jobs, knowledge_job_actions, knowledge_run_captures,
--   knowledge_librarian_configs, knowledge_index, knowledge_index_state
--
-- Only four of them still held rows (39 total): knowledge_jobs 6,
-- knowledge_job_actions 19, knowledge_query_snapshots 13,
-- knowledge_librarian_configs 1. Those rows were exported before this migration
-- ran, for both the production and the acceptance database:
--   .acceptance/backups/2026-09-12-legacy-knowledge-tables/
--     main-workbench.backup / main-workbench-legacy.sql / main-workbench-legacy.json
--     acceptance-workbench.backup / acceptance-workbench-legacy.sql / ...json
--     manifest.txt  (sha256 of every artifact and of both source databases)
--
-- knowledge_index is an FTS5 virtual table, so dropping it also removes its
-- shadow tables (knowledge_index_config/content/data/docsize/idx); no separate
-- statement is needed. Every legacy trigger and index disappears with its table.

DROP TABLE IF EXISTS knowledge_index;
DROP TABLE IF EXISTS knowledge_index_state;
DROP TABLE IF EXISTS knowledge_job_actions;
DROP TABLE IF EXISTS knowledge_jobs;
DROP TABLE IF EXISTS knowledge_query_snapshots;
DROP TABLE IF EXISTS knowledge_librarian_configs;
DROP TABLE IF EXISTS knowledge_repeals;
DROP TABLE IF EXISTS knowledge_submissions;
DROP TABLE IF EXISTS knowledge_relation_sources;
DROP TABLE IF EXISTS knowledge_relations;
DROP TABLE IF EXISTS knowledge_version_sources;
DROP TABLE IF EXISTS knowledge_versions;
DROP TABLE IF EXISTS knowledge_sources;
DROP TABLE IF EXISTS knowledge_run_captures;
DROP TABLE IF EXISTS knowledge_items;
