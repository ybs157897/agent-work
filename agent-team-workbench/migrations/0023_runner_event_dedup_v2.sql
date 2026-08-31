-- 0023_runner_event_dedup_v2.sql — Runner v2 事件去重键换代（RFC task-control-surface §8.3）。
-- v2 dedup key = (run_id, lease_id, runner_id, producer_seq)；旧键
-- (run_id, runner_id, runner_seq) 无法表达 lease 维度，且事件身份以 event_id/
-- producer_seq 为准（重连原样重发）。本迁移**有意断轨**：删除 runner_seq 列，
-- 0023 之后旧 INSERT(run_id, runner_id, runner_seq) 形态不再可用——v1 dedup
-- 路径与 Runner Gateway v1 在同一波次退役，不留兼容分支。
--
-- 应用层冻结的写入形态（SQL 即契约）：
--   INSERT INTO runner_event_dedup(run_id, lease_id, runner_id, producer_seq, event_id, run_seq)
--   VALUES (?,?,?,?,?,0)
--
-- 重建方案（与 SQLite 侧逐句对齐）：RENAME 旧表 → 建新表（列序与 SQLite 完全
-- 一致）→ INSERT SELECT 迁移旧行（lease_id=''、event_id=''、producer_seq=
-- runner_seq、run_seq 保留）→ DROP 旧表。旧表无外键/索引引用，迁移行数 = 去重
-- 记录数，量级有限。producer_seq/run_seq 沿用 0001 的 BIGINT（SQLite 侧 INTEGER）。

ALTER TABLE runner_event_dedup RENAME TO runner_event_dedup_v1;

CREATE TABLE runner_event_dedup (
    run_id       TEXT NOT NULL,
    lease_id     TEXT NOT NULL DEFAULT '',
    runner_id    TEXT NOT NULL,
    producer_seq BIGINT NOT NULL DEFAULT 0,
    event_id     TEXT NOT NULL DEFAULT '',
    run_seq      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, lease_id, runner_id, producer_seq)
);

INSERT INTO runner_event_dedup (run_id, lease_id, runner_id, producer_seq, event_id, run_seq)
SELECT run_id, '', runner_id, runner_seq, '', run_seq
FROM runner_event_dedup_v1;

DROP TABLE runner_event_dedup_v1;
