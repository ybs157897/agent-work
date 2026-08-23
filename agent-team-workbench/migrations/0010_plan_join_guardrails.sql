-- 0010_plan_join_guardrails.sql — M4 编排层：join 动词 + plans 预算护栏列 +
-- plan_dispatch 闸门审批的 run_id 放宽。
-- guardrails：{max_dispatch?, max_tokens?} 提交时固化；error 记 plan 级失败原因码
--（budget_exceeded；步骤级失败原因仍在 plan_steps.error）。

ALTER TABLE plans ADD COLUMN guardrails JSONB NOT NULL DEFAULT '{}';
ALTER TABLE plans ADD COLUMN error TEXT;

-- join 进入 verb 白名单（0009 的 CHECK 未命名约束名沿 PG 缺省命名重建）。
ALTER TABLE plan_steps DROP CONSTRAINT IF EXISTS plan_steps_verb_check;
ALTER TABLE plan_steps ADD CONSTRAINT plan_steps_verb_check
    CHECK (verb IN ('dispatch','defer','finish','consult_knowledge','join'));

-- plan_dispatch 闸门审批（kind=plan_dispatch，M4 审批护栏）无关联 run：
-- run_id 放宽为可空（FK 保留，非空值仍校验指向真实 run）。
ALTER TABLE approvals ALTER COLUMN run_id DROP NOT NULL;
