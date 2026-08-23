-- 0008_plan_source_run_unique.sql — M2 plan 提取幂等兜底：同一 run 的终态
-- 事件只产出一份 plan（plans.source_run_id 唯一）。API 提交的 source_run_id
-- 可空（NULL 不参与唯一性），lead 提取路径必填。

CREATE UNIQUE INDEX idx_plans_source_run ON plans(source_run_id);
