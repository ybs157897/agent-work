-- 0017_search_index.sql — 会话元模型 S4：FTS 检索索引（PostgreSQL 版）。
-- 派生存储：片段摘要（segment_summary）/ 决策台账（decision）/ 产物标题
--（artifact）三类条目；写入走定点重写（delete by (kind, source_id) + insert），
-- 天然幂等。PG 无 FTS5：普通表 + STORED tsvector 生成列（'simple' 配置显式
-- 传入，IMMUTABLE 可用于生成列）+ GIN；检索走 plainto_tsquery + ts_headline。

CREATE TABLE search_index (
    work_item_id TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('segment_summary','decision','artifact')),
    source_id    TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL DEFAULT '',
    tsv          tsvector GENERATED ALWAYS AS
                   (to_tsvector('simple', coalesce(title,'') || ' ' || coalesce(body,''))) STORED
);

CREATE INDEX search_index_tsv_idx ON search_index USING GIN (tsv);
CREATE INDEX search_index_work_item_idx ON search_index (work_item_id);
