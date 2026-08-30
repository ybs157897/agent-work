-- 0016_dispatches.sql — 会话元模型 S1：派发（dispatch）批次。
-- dispatch = 用户一次发送（或 lead plan 派生 / wakeup）触发的执行批次，是会话组
-- 的关联键：execution_runs.dispatch_id 让成员 run 免树遍历即可成组。
-- trigger 记批次成因（user_message | lead_plan | wakeup）；status 跟随成员收口
-- （running → collecting → completed/degraded，用户喊停 → cancelled），CHECK
-- 放新建表（SQLite ALTER 加列不能带 CHECK）。

CREATE TABLE dispatches (
    id            TEXT PRIMARY KEY,
    work_item_id  TEXT NOT NULL REFERENCES work_items(id),
    trigger       TEXT NOT NULL,
    lead_run_id   TEXT REFERENCES execution_runs(id),
    status        TEXT NOT NULL DEFAULT 'running'
                  CHECK (status IN ('running','collecting','completed','degraded','cancelled')),
    created_at    TIMESTAMPTZ NOT NULL,
    closed_at     TIMESTAMPTZ
);

-- 成员 run 归属批次（nullable：wakeup / 重试 / 存量 run 可无批次）。
ALTER TABLE execution_runs ADD COLUMN dispatch_id TEXT REFERENCES dispatches(id);

-- 参与线片段序号：同一 task_key 下第 N 段会话（轮换代际时 +1，缺省 1），
-- 片段边界显式化。
ALTER TABLE task_sessions ADD COLUMN segment_seq INTEGER NOT NULL DEFAULT 1;
