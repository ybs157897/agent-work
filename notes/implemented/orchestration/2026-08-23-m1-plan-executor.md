# M1：OrchestrationPlan 词汇表 + 确定性执行器 + 子任务树

日期：2026-08-23 · 状态：已实施（本 note 随实现同分支落地）· 对应 end-goal.md M1

## 决策

控制平面新增一等实体 **Plan**：一份由 lead agent（或用户经 API）提交的**有序动作批次**。
agent 出谋（产出 plan），控制平面执政（校验、执行、审计）——执行器是确定性的：
同一 plan 提交永远产生同样的效果，不依赖任何模型行为。

### 词汇表（M1 子集，三个动词）

| verb | 语义 | 立即效果 |
|---|---|---|
| `dispatch` | 派工：建子 work item（parent_id=plan 的主任务）+ 首个 run | 子任务 + run 落库并分发 |
| `defer` | 挂起：本批次到此为止，等待唤醒 | 余下 steps → skipped；plan → waiting；按需入 automation wakeup |
| `finish` | 收尾：本 plan 完成 | plan → finished（终态） |

`use_session` 是默认行为（run 创建已带会话锚点），`consult_knowledge` 归 M3，
`join` 归 M4（多 agent 全编排）——本批不实现，提交含未知 verb 一律 400。

### Plan 生命周期（防卡死设计）

```
active ──所有 step 执行完──▶ finished
active ──遇到 defer────────▶ waiting ──同主任务新 plan 提交──▶ finished（superseded_by 记新 plan）
active/waiting ──用户取消──▶ cancelled        任一 step 失败 ──▶ failed
```

- **defer 即批次终止**：不维护跨唤醒的游标。唤醒 ≠ plan 继续，而是唤醒 owner agent
  观察全局后提交**新 plan**；新 plan 提交时同主任务的旧 waiting plan 自动 supersede。
- **defer 合法性**：`wake_at` 与"存在未静默子任务"至少居其一，否则拒绝（防死等）。
- **唯一活跃**：同一 work_item 同时最多一个 active/waiting plan（提交时校验）。

### 唤醒链路（automation wakeup 的生产者就此补齐）

子任务静默钩子：`RecordRunStatus` 终态提交后（maybeSelfHeal 同款事务外位置）——
若该 run 的 work item 有 parent_id，且 parent 存在 waiting plan，且 parent 全部子任务
**无活跃 run**（终态集合外无 run）→ 入 automation wakeup：
`source=automation, agent=plan.agent_profile_id, task_key="plan:"+planID,
context={plan_id, trigger:"children_quiet"}`。幂等由 wakeup 既有 coalescing 保证。

defer 带 wake_at 时同时入 timer 型 automation wakeup（同 task_key）。

## 数据模型（迁移 0006，PG/SQLite 双目录语义等价）

- `plans`：`id TEXT PK`（plan_ 前缀）、`workspace_id`、`work_item_id`、`agent_profile_id`、
  `source_run_id NULL`、`status`、`superseded_by NULL`、`version`、`created_at`、`updated_at`。
- `plan_steps`：`(plan_id, seq)` 联合 PK、`verb`、`payload`（JSON 原文）、
  `status`（pending/executed/skipped/failed）、`result_work_item_id NULL`、
  `result_run_id NULL`、`error NULL`、`created_at`、`executed_at NULL`。
- `work_items` 加 `parent_id TEXT NULL REFERENCES work_items(id)` + `idx_work_items_parent`。
- 事件白名单新增：`plan.submitted / plan.step_executed / plan.waiting / plan.finished`
  （AggregatePlan="plan"）；web `EVENT_NAMES` 同步登记。

## API 契约（pin 死，前后端并行开发的唯一依据）

- `POST /api/v1/workspaces/{workspace_id}/plans`（Idempotency-Key + PermWorkItemWrite）：
  ```json
  {"work_item_id":"wi_..","agent_profile_id":"agent_..","source_run_id":"run_..?",
   "steps":[
     {"verb":"dispatch","agent_id":"agent_..","title":"..","instruction":"..",
      "acceptance":[".."],"priority":"high?"},
     {"verb":"defer","reason":"..","wake_at":"RFC3339?"},
     {"verb":"finish","summary":".."}]}
  ```
  201 返回 PlanDTO（含 steps 执行结果）；verb 未知 / defer 非法 / 校验失败 → 400 problem+json。
- `GET /api/v1/plans/{plan_id}` → PlanDTO。
- `GET /api/v1/work-items/{work_item_id}/tree` → `{items:[WorkItemDTO]}` 先序整棵子树。
- `WorkItemDTO` 增 `parent_id`（omitempty）；`listWorkItems` query 增 `parent_id`
  （值 `none` = 只看根任务）。
- PlanDTO：`{id, workspace_id, work_item_id, agent_profile_id, source_run_id, status,
  superseded_by, steps:[{seq, verb, status, payload, result_work_item_id?, result_run_id?,
  error?}], version, created_at, updated_at}`（全 snake_case）。

## 否决方案（留痕）

- **独立 orchestrator loop 进程**：否决。M1 执行器 = 提交时同步执行 + 终态事件钩子；
  延迟语义复用 wakeup ticker（已有 30s 循环），不引入第二个调度循环。
- **steps 内嵌 plans 表 JSON 列**：否决。步骤级审计（哪个 dispatch 建了哪个 run）
  需要行级可查询，且执行中状态翻转写 JSON 整体易丢更新。
- **dispatch 支持 depends_on 编排**：否决（M1 子任务并行，依赖编排归 M4）。
- **plan 跨 work item**：否决。plan 绑定一个主任务，树以主任务为根。
- **游标式 defer-resume**：否决。跨唤醒游标 = 半成品状态机，批次化重决策更简单且幂等。

## 验收

- 集成测试覆盖：双 dispatch 建子任务+分发、defer 挂起+wakeup 入队、子任务静默钩子唤醒
  owner、finish 落终、supersede、幂等重提交、defer 无出口被拒。
- `openTestDB` 迁移列表必须登记 0006（硬编码清单，漏了全测试红）。
- 触面：`go build ./... && go vet ./... && go test -race -count=1 ./internal/...` 触面包；
  web `pnpm tsc --noEmit && pnpm test && pnpm lint`。
