-- 0023_runner_event_dedup_v2.sql — Runner v2 事件去重键换代（SQLite 本地验证版）。
-- 与 migrations/0023_runner_event_dedup_v2.sql 语义等价；差异：BIGINT→INTEGER
-- （0001 先例映射）。
--
-- v2 dedup key = (run_id, lease_id, runner_id, producer_seq)；本迁移**有意断轨**：
-- 删除 runner_seq 列，0023 之后旧 INSERT(run_id, runner_id, runner_seq) 形态
-- 不再可用——v1 dedup 路径与 Runner Gateway v1 同波次退役，不留兼容分支。
-- 应用层冻结写入形态：
--   INSERT INTO runner_event_dedup(run_id, lease_id, runner_id, producer_seq, event_id, run_seq)
--   VALUES (?,?,?,?,?,0)
--
-- 重建机械：runner_event_dedup_v2 先作为暂存表承接旧行（lease_id=''、
-- event_id=''、producer_seq=runner_seq、run_seq 保留），随后 DROP 旧表、按正式
-- 名建新表（新 PK）、拷回、DROP 暂存表。**不走 ALTER RENAME**：SQLite 的
-- RENAME 会强制全 schema 触发器体严格重解析，而迁移门禁存在 0019 后置的乱序
-- 应用路径（TestRecordKindMigrationBackfill），届时 0020 触发器体引用的
-- work_items.record_kind 尚未存在，RENAME 直接报错；CREATE/DROP/INSERT 无此
-- 语义（实测）。终态 schema 与 PG 侧一致。

CREATE TABLE runner_event_dedup_v2 AS
SELECT run_id, '' AS lease_id, runner_id, runner_seq AS producer_seq, '' AS event_id, run_seq
FROM runner_event_dedup;

DROP TABLE runner_event_dedup;

CREATE TABLE runner_event_dedup (
    run_id       TEXT NOT NULL,
    lease_id     TEXT NOT NULL DEFAULT '',
    runner_id    TEXT NOT NULL,
    producer_seq INTEGER NOT NULL DEFAULT 0,
    event_id     TEXT NOT NULL DEFAULT '',
    run_seq      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (run_id, lease_id, runner_id, producer_seq)
);

INSERT INTO runner_event_dedup (run_id, lease_id, runner_id, producer_seq, event_id, run_seq)
SELECT run_id, lease_id, runner_id, producer_seq, event_id, run_seq
FROM runner_event_dedup_v2;

DROP TABLE runner_event_dedup_v2;
