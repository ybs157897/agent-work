-- 0008_plan_source_run_unique.sql — M2 plan 提取幂等兜底（SQLite）。

CREATE UNIQUE INDEX idx_plans_source_run ON plans(source_run_id);
