-- 0007_task_sessions_parent.sql — M2 task_sessions 树形化：会话树镜像任务树。
-- 子任务锚点写入时记父任务（同 agent+adapter）锚点 id（无则 NULL），
-- 轮换谱系沿 parent_anchor_id 链可查；父锚点由应用层解析（需读 work_items.parent_id）。

ALTER TABLE task_sessions ADD COLUMN parent_anchor_id TEXT;
