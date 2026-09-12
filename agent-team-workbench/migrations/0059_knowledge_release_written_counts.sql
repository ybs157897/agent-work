-- 0059_knowledge_release_written_counts.sql — a release reports two different
-- numbers: what it contains (all documents, including carried-forward ones) and
-- what this publish actually wrote. Collapsing them into one field made an
-- incremental release look like the library had shrunk.
ALTER TABLE knowledge_releases ADD COLUMN written_document_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowledge_releases ADD COLUMN written_assertion_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowledge_releases ADD COLUMN written_relation_count INTEGER NOT NULL DEFAULT 0;
