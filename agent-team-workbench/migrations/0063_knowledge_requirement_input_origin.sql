-- 0063_knowledge_requirement_input_origin.sql — how a frozen requirement body
-- was obtained, and whether the referenced document could be read. An inline
-- payload copy is a different source from the referenced file: recording the
-- origin and the reference failure keeps the two from being confused, and
-- keeps the gap visible instead of silently presenting a fallback as the
-- original text.
ALTER TABLE knowledge_requirement_inputs ADD COLUMN input_origin TEXT NOT NULL DEFAULT 'content_ref';
ALTER TABLE knowledge_requirement_inputs ADD COLUMN reference_error TEXT NOT NULL DEFAULT '';
