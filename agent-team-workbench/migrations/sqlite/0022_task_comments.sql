-- 0022_task_comments.sql — Task 评论流与验收读模型字段（SQLite 本地验证版）。
-- 与 migrations/0022_task_comments.sql 语义等价；差异：BIGINT→INTEGER、
-- TIMESTAMPTZ→DATETIME、JSONB→JSON 文本。JSON 操作对账：
--   * jsonb_typeof(x)='string'      ↔ json_type(x,'$.k')='text'（SQLite 对 JSON
--     字符串返回 'text'）；
--   * jsonb 数组元素遍历            ↔ WITH RECURSIVE 生成下标 + json_type/extract
--     的 '$.k[i]' 路径（JSON1 无集合展开函数）；
--   * data - 'pending_instruction'  ↔ json_remove(data,'$.pending_instruction')，
--     纯 SQL 完成删 key（优于置 null：RFC 要求"删除该 key"）。
-- 回填口径与 PG 侧逐条一致，见 PG 文件头注释：cursor 回填、acceptance_criteria
-- 仅从 state.data / plan step payload 的字符串数组逐字回填（否则置 NULL，不从
-- description 猜）、review/acceptance 的 phase_entered_at 用 updated_at、
-- pending_instruction 迁 requirement comment 后删 key。

CREATE TABLE task_comment_cursors (
    root_work_item_id TEXT PRIMARY KEY REFERENCES work_items(id),
    latest_revision   INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE task_comments (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    root_work_item_id TEXT NOT NULL REFERENCES work_items(id),
    work_item_id      TEXT NOT NULL REFERENCES work_items(id),
    revision          INTEGER NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('note','requirement','review_feedback')),
    body              TEXT NOT NULL CHECK (length(trim(body)) BETWEEN 1 AND 16384),
    actor_kind        TEXT NOT NULL CHECK (actor_kind IN ('user','system','runtime')),
    actor_id          TEXT NOT NULL,
    source_run_id     TEXT REFERENCES execution_runs(id),
    source_ref        TEXT,
    client_key        TEXT,
    created_at        DATETIME NOT NULL,
    UNIQUE (root_work_item_id, revision)
);

CREATE UNIQUE INDEX idx_task_comments_client_key
    ON task_comments(root_work_item_id, client_key)
    WHERE client_key IS NOT NULL;

ALTER TABLE work_items ADD COLUMN acceptance_criteria TEXT;
ALTER TABLE work_items ADD COLUMN phase_entered_at DATETIME;

ALTER TABLE task_coordinator_states
    ADD COLUMN consumed_comment_revision INTEGER NOT NULL DEFAULT 0;

INSERT INTO task_comment_cursors (root_work_item_id, latest_revision)
SELECT root_work_item_id, 0
FROM task_coordinator_states;

-- 根 Task acceptance_criteria：元素全为字符串且至少一个 trim 后非空时逐字拷贝。
WITH RECURSIVE
cand AS (
    SELECT s.root_work_item_id AS wi_id, s.data,
           json_array_length(s.data, '$.acceptance_criteria') AS len
    FROM task_coordinator_states s
    WHERE json_type(s.data, '$.acceptance_criteria') = 'array'
),
idx AS (
    SELECT wi_id, data, len, 0 AS i FROM cand WHERE len > 0
    UNION ALL
    SELECT wi_id, data, len, i + 1 FROM idx WHERE i + 1 < len
),
bad AS (
    SELECT DISTINCT wi_id FROM idx
    WHERE json_type(data, '$.acceptance_criteria[' || i || ']') <> 'text'
),
good AS (
    SELECT DISTINCT wi_id FROM idx
    WHERE json_type(data, '$.acceptance_criteria[' || i || ']') = 'text'
      AND trim(json_extract(data, '$.acceptance_criteria[' || i || ']')) <> ''
)
UPDATE work_items
SET acceptance_criteria = (
    SELECT json_extract(s.data, '$.acceptance_criteria')
    FROM task_coordinator_states s
    WHERE s.root_work_item_id = work_items.id
)
WHERE id IN (SELECT wi_id FROM good)
  AND id NOT IN (SELECT wi_id FROM bad);

-- Plan child acceptance_criteria：child 仅被一个 dispatch step 认领时才唯一；
-- 根回填优先，已有值的行跳过。
WITH RECURSIVE
claimed AS (
    SELECT result_work_item_id AS wi_id
    FROM plan_steps
    WHERE verb = 'dispatch'
      AND result_work_item_id IS NOT NULL
      AND result_work_item_id <> ''
    GROUP BY result_work_item_id
    HAVING COUNT(*) = 1
),
cand AS (
    SELECT ps.result_work_item_id AS wi_id, ps.payload,
           json_array_length(ps.payload, '$.acceptance') AS len
    FROM plan_steps ps
    JOIN claimed c ON c.wi_id = ps.result_work_item_id
    WHERE ps.verb = 'dispatch'
      AND json_type(ps.payload, '$.acceptance') = 'array'
),
idx AS (
    SELECT wi_id, payload, len, 0 AS i FROM cand WHERE len > 0
    UNION ALL
    SELECT wi_id, payload, len, i + 1 FROM idx WHERE i + 1 < len
),
bad AS (
    SELECT DISTINCT wi_id FROM idx
    WHERE json_type(payload, '$.acceptance[' || i || ']') <> 'text'
),
good AS (
    SELECT DISTINCT wi_id FROM idx
    WHERE json_type(payload, '$.acceptance[' || i || ']') = 'text'
      AND trim(json_extract(payload, '$.acceptance[' || i || ']')) <> ''
)
UPDATE work_items
SET acceptance_criteria = (
    SELECT json_extract(ps.payload, '$.acceptance')
    FROM plan_steps ps
    WHERE ps.result_work_item_id = work_items.id
      AND ps.verb = 'dispatch'
      AND ps.result_work_item_id <> ''
)
WHERE id IN (SELECT wi_id FROM good)
  AND id NOT IN (SELECT wi_id FROM bad)
  AND acceptance_criteria IS NULL;

UPDATE work_items
SET phase_entered_at = updated_at
WHERE phase IN ('review','acceptance');

-- legacy pending_instruction → requirement comment（trim 口径与 body CHECK 一致，
-- body 保留原文不 trim；一个 root 至多一个单槽指令，revision=1）。
INSERT INTO task_comments
    (id, workspace_id, root_work_item_id, work_item_id, revision, kind, body,
     actor_kind, actor_id, source_run_id, source_ref, client_key, created_at)
SELECT 'cmt_legacy_' || st.root_work_item_id,
       st.workspace_id, st.root_work_item_id, st.root_work_item_id, 1,
       'requirement', json_extract(st.data, '$.pending_instruction'),
       'system', 'legacy_migration', NULL, 'legacy:pending_instruction', NULL,
       st.updated_at
FROM task_coordinator_states st
WHERE json_type(st.data, '$.pending_instruction') = 'text'
  AND length(trim(json_extract(st.data, '$.pending_instruction'))) BETWEEN 1 AND 16384;

UPDATE task_comment_cursors
SET latest_revision = 1
WHERE root_work_item_id IN (
    SELECT root_work_item_id FROM task_comments
    WHERE source_ref = 'legacy:pending_instruction'
);

-- 仅删除已成功迁成评论的 key；超长未迁移的保留原文，不在迁移里静默丢数据。
UPDATE task_coordinator_states
SET data = json_remove(data, '$.pending_instruction')
WHERE json_type(data, '$.pending_instruction') = 'text'
  AND length(trim(json_extract(data, '$.pending_instruction'))) BETWEEN 1 AND 16384;
