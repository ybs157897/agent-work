-- 0058_knowledge_evidence_aliases.sql — a published version keeps the
-- staged-evidence-key -> canonical-evidence-ID map it was written with.
-- Historical reads resolve their own citations through this map instead of
-- guessing, and a re-projection of an old version stays faithful to it.
ALTER TABLE knowledge_document_versions ADD COLUMN evidence_alias_json TEXT NOT NULL DEFAULT '{}';
