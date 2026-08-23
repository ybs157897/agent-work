-- 0009_plan_consult_knowledge.sql — M2 plan 词汇表扩展：consult_knowledge 动词。
-- 0006 的 plan_steps.verb CHECK 未命名，PostgreSQL 缺省名 <table>_<column>_check。

ALTER TABLE plan_steps DROP CONSTRAINT IF EXISTS plan_steps_verb_check;
ALTER TABLE plan_steps ADD CONSTRAINT plan_steps_verb_check
    CHECK (verb IN ('dispatch','defer','finish','consult_knowledge'));
