# Kimi detached task completion and cancellation

Status: implemented

## 决策与理由

KAP 在主 turn 返回 `turn.ended(completed)` 后，仍可能为同一 `main` agent 投递 detached task 的终止事件、`turn.started(origin.kind=task, origin.taskId=...)` 和 `task.notified(sourceKind=background_task, sourceId=...)`。适配器因此把已观测的 `detached=true` task 作为本 Run 的后台工作，等待对应通知 turn 的 `turn.ended`，避免过早释放 Run-bound knowledge access 凭据。

若主 Agent 使用 `WaitFor` 消费后台结果，KAP 会另外记录 `task.waitDelivered`，其 `keys` 使用 `taskId\x00status\x00notificationId` 编码。适配器验证 `agentId=main`、key 三段和本 Run task 归属后清除对应待通知项，因为结果已经在当前 turn 交付，不应再等待自动通知 turn。

取消沿 KAP 的真实 REST 语义实现：session `:abort` 只取消当前 active turn，后台 task 通过 `/sessions/{session}/tasks/{task}:cancel` 单独取消。适配器只遍历本 Run 已登记的 task ID，并将 task 不存在或已完成视为幂等结果。

## 放弃了什么

不以首个 `turn.ended` 作为 Run 成功边界，也不通过 session 级任务枚举或全量停止来“清理”后台任务。前者会在知识查询尚未回传时关闭访问凭据，后者会影响同一 KAP session 中不属于本 Run 的任务。

真实证据来自 KAP 0.39.1 协议源码 `packages/protocol/src/events/origin.ts`、`events/task.ts`、`packages/agent-core-v2/src/agent/task/taskService.ts`，以及本仓库回放 `agent-team-workbench/.agent-work/preview/runtime/kimi/server/events/session_f4cc9443-daa8-483c-a920-19153fb061eb.jsonl` 的 seq 35–41。此前误写到 main 工作树的实验改动已由 root 精确恢复，本修复只保留在 `codex/knowledge-experience` worktree。
