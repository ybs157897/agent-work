-- 0008_plan_source_run_unique_sqlite.sql — M2 plan 提取幂等兜底（SQLite 本地验证版）。
-- 与 migrations/0008_plan_source_run_unique.sql 语义等价。

CREATE UNIQUE INDEX idx_plans_source_run ON plans(source_run_id);
