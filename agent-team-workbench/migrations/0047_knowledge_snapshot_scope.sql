-- 0047_knowledge_snapshot_scope.sql — preserve the exact query scope beside
-- the immutable result/version/source snapshot.
ALTER TABLE knowledge_query_snapshots
    ADD COLUMN scope_json TEXT NOT NULL DEFAULT '{}';
