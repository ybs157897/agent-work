-- 0022_task_comments.sql — Task 评论流与验收读模型字段（RFC task-control-surface §6.2）。
-- TaskComment 是 append-only 资源：revision 在根 Task 维度单调分配（事务内锁
-- cursor 行 +1，禁止 MAX(revision)+1）；cursor 行与根 Coordinator state 同事务
-- 创建、永不物理删除。consumed_comment_revision 是 Coordinator 消费水位。
--
-- 回填（全部纯 SQL，不改 version/updated_at，遵循 0019 先例）：
--   * 每个已有 Coordinator root 插入 latest_revision=0 的 cursor。
--   * acceptance_criteria 只从持久 JSON 的唯一权威来源回填，不从 description 猜：
--       - 根 Task：task_coordinator_states.data->'acceptance_criteria'
--         （service.go 创建根 Task 时持久化，coordinator_engine.go 同键读取）；
--       - Plan child：plan_steps.payload->'acceptance'，且该 child 只被一个
--         dispatch step 认领（result_work_item_id 无唯一约束，多条认领即视为
--         无法唯一确认）。
--     仅当值为「元素全为字符串、且至少一个 trim 后非空」的 JSON 数组时逐字拷贝；
--     缺失 / 非数组 / 空数组 / 空白数组 / 含非字符串元素一律置 NULL（Delivery
--     Brief 会标 partial）。
--   * review/acceptance Task 的 phase_entered_at 用 updated_at 回填。
--   * state.data->'pending_instruction' 非空（trim 后 1..16384 字符，与 body CHECK
--     同口径）时迁成 requirement comment（system/legacy_migration、
--     source_ref='legacy:pending_instruction'、revision=1、保留原文），cursor 推到 1，
--     并用 jsonb 删除运算符从 state.data 删除该 key；超长无法入评论的保留原 key
--     交应用层处理，不在迁移里丢数据。

CREATE TABLE task_comment_cursors (
    root_work_item_id TEXT PRIMARY KEY REFERENCES work_items(id),
    latest_revision   BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE task_comments (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id),
    root_work_item_id TEXT NOT NULL REFERENCES work_items(id),
    work_item_id      TEXT NOT NULL REFERENCES work_items(id),
    revision          BIGINT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('note','requirement','review_feedback')),
    body              TEXT NOT NULL CHECK (length(trim(body)) BETWEEN 1 AND 16384),
    actor_kind        TEXT NOT NULL CHECK (actor_kind IN ('user','system','runtime')),
    actor_id          TEXT NOT NULL,
    source_run_id     TEXT REFERENCES execution_runs(id),
    source_ref        TEXT,
    client_key        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (root_work_item_id, revision)
);

-- client_key 幂等：同 key 不同 body 由应用层返回 idempotency_conflict。
CREATE UNIQUE INDEX idx_task_comments_client_key
    ON task_comments(root_work_item_id, client_key)
    WHERE client_key IS NOT NULL;

-- canonical 验收标准与评审排队时间（Review Queue 的 pending_since）。
ALTER TABLE work_items ADD COLUMN acceptance_criteria JSONB;
ALTER TABLE work_items ADD COLUMN phase_entered_at TIMESTAMPTZ;

ALTER TABLE task_coordinator_states
    ADD COLUMN consumed_comment_revision BIGINT NOT NULL DEFAULT 0;

-- 每个 Coordinator root 一个 cursor（states.root_work_item_id UNIQUE，无冲突）。
INSERT INTO task_comment_cursors (root_work_item_id, latest_revision)
SELECT root_work_item_id, 0
FROM task_coordinator_states;

-- 根 Task acceptance_criteria：state.data 数组元素全为字符串且至少一个非空。
WITH candidates AS (
    SELECT st.root_work_item_id AS work_item_id,
           st.data -> 'acceptance_criteria' AS arr
    FROM task_coordinator_states st
    WHERE jsonb_typeof(st.data -> 'acceptance_criteria') = 'array'
),
valid AS (
    SELECT c.work_item_id, c.arr
    FROM candidates c
    WHERE NOT EXISTS (
              SELECT 1 FROM jsonb_array_elements(c.arr) el
              WHERE jsonb_typeof(el) <> 'string'
          )
      AND EXISTS (
              SELECT 1 FROM jsonb_array_elements(c.arr) el
              WHERE jsonb_typeof(el) = 'string' AND btrim(el #>> '{}') <> ''
          )
)
UPDATE work_items wi
SET acceptance_criteria = v.arr
FROM valid v
WHERE wi.id = v.work_item_id;

-- Plan child acceptance_criteria：child 仅被一个 dispatch step 认领时才唯一。
-- 根回填优先：已有值的行跳过。
WITH claimed AS (
    SELECT ps.result_work_item_id AS work_item_id
    FROM plan_steps ps
    WHERE ps.verb = 'dispatch'
      AND ps.result_work_item_id IS NOT NULL
      AND ps.result_work_item_id <> ''
    GROUP BY ps.result_work_item_id
    HAVING COUNT(*) = 1
),
candidates AS (
    SELECT ps.result_work_item_id AS work_item_id,
           ps.payload -> 'acceptance' AS arr
    FROM plan_steps ps
    JOIN claimed c ON c.work_item_id = ps.result_work_item_id
    WHERE ps.verb = 'dispatch'
      AND jsonb_typeof(ps.payload -> 'acceptance') = 'array'
),
valid AS (
    SELECT c.work_item_id, c.arr
    FROM candidates c
    WHERE NOT EXISTS (
              SELECT 1 FROM jsonb_array_elements(c.arr) el
              WHERE jsonb_typeof(el) <> 'string'
          )
      AND EXISTS (
              SELECT 1 FROM jsonb_array_elements(c.arr) el
              WHERE jsonb_typeof(el) = 'string' AND btrim(el #>> '{}') <> ''
          )
)
UPDATE work_items wi
SET acceptance_criteria = v.arr
FROM valid v
WHERE wi.id = v.work_item_id
  AND wi.acceptance_criteria IS NULL;

-- 评审排队时间：仅存量 review/acceptance 行需要（Review Queue 排序键）。
UPDATE work_items
SET phase_entered_at = updated_at
WHERE phase IN ('review','acceptance');

-- legacy pending_instruction → requirement comment（同 CHECK 口径：trim 后
-- 1..16384 字符；body 保留原文不 trim）。一个 root 至多一个单槽指令，revision=1。
INSERT INTO task_comments
    (id, workspace_id, root_work_item_id, work_item_id, revision, kind, body,
     actor_kind, actor_id, source_run_id, source_ref, client_key, created_at)
SELECT 'cmt_legacy_' || st.root_work_item_id,
       st.workspace_id, st.root_work_item_id, st.root_work_item_id, 1,
       'requirement', st.data ->> 'pending_instruction',
       'system', 'legacy_migration', NULL, 'legacy:pending_instruction', NULL,
       st.updated_at
FROM task_coordinator_states st
WHERE jsonb_typeof(st.data -> 'pending_instruction') = 'string'
  AND length(btrim(st.data ->> 'pending_instruction')) BETWEEN 1 AND 16384;

UPDATE task_comment_cursors
SET latest_revision = 1
WHERE root_work_item_id IN (
    SELECT root_work_item_id FROM task_comments
    WHERE source_ref = 'legacy:pending_instruction'
);

-- 仅删除已成功迁成评论的 key；超长未迁移的保留原文，不在迁移里静默丢数据。
UPDATE task_coordinator_states
SET data = data - 'pending_instruction'
WHERE jsonb_typeof(data -> 'pending_instruction') = 'string'
  AND length(btrim(data ->> 'pending_instruction')) BETWEEN 1 AND 16384;
