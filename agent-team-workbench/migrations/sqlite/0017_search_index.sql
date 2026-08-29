-- 0017_search_index_sqlite.sql — 会话元模型 S4：FTS 检索索引（SQLite 本地验证版）。
-- 与 migrations/0017_search_index.sql 语义等价：SQLite 用 FTS5 虚表
--（unicode61 分词；已知限制：连续 CJK 串整块成 token，中文按词检索能力有限），
-- 检索走 MATCH + snippet()，排序用内置 rank。写入走定点重写
--（DELETE by (kind, source_id) + INSERT），天然幂等。

CREATE VIRTUAL TABLE search_index USING fts5(
    work_item_id,
    kind,
    source_id,
    title,
    body,
    tokenize = 'unicode61'
);
