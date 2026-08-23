# M4：认领模式 + join 动词 + 审批/预算护栏 + 手动打回

日期：2026-08-24 · 状态：已实施（随实现同分支落地）· 对应 end-goal.md M4
前置：M1（plan 执行器）、M2（lead planner + 评估）

## 决策

### 1. 认领模式（发布 → 领取）

- work item 允许**无 assignee 创建**（发布到任务池）——CreateWorkItemParams.AgentProfileID
  本就可选，无需 schema 变更；看板 API 无需改。
- 新命令：`POST /api/v1/work-items/{id}/commands/claim`，body `{agent_id, expected_version}`。
  语义：仅 todo 且当前无 assignee 可认领；认领 = AssignWorkItem + 复用既有
  enqueueAssignmentWake（assignment wakeup 自动唤起认领者）。幂等：同 agent 重复认领
  返回现状不报错。
- 谁可以认领：MVP 期任何调用者可为任何 agent 认领（人代领/自领同一入口）；权限点复用
  PermWorkItemWrite。agent 自领的自治触发（心跳拾取池任务）归后续自动化，不在 M4。

### 2. join 动词

plan steps 新增第五动词：`join{children: "all" | ["wi_..", ...], wake_at?}`。
语义 = 带显式等待集合的 defer 变体：**批次同样终止**（守 M1「defer 即终止」红线），
plan → waiting，并记录 join 目标集；子任务静默钩子（maybeAdvancePlans）判定从
「parent 全部子任务」收窄为「join 目标集内子任务无活跃 run」即触发唤醒。
defer 视为 join{children:"all"} 的别名（执行器内部统一，API 层保留两个 verb 以保
语义可读）。join 目标必须都是本 plan 主任务的子任务，否则 400。

### 3. 审批护栏（dispatch 的人工闸门）

SubmitPlan 校验期：任一 dispatch 目标 agent 的 `approval_policy == "manual"` →
该 step 不直接执行，转为挂起审批：创建 ApprovalRequest（kind="plan_dispatch"），
plan → waiting（reason=pending_approval），step → pending。审批解决（既有
approval.resolved 链路）→ 批准后执行该 step 并继续批次；拒绝 → step failed +
plan failed（guardrail 语义：人否决了路线）。
触发点：复用既有 ApprovalRequest 聚合与 approval.* 事件；批准回调挂在审批解决处。

### 4. 预算护栏

SubmitPlan 接受可选 `guardrails: {max_dispatch?: int, max_tokens?: int64}`，固化进
plan（plans 表加 `guardrails` JSON 列，迁移 0010 双方言）。
- `max_dispatch`：提交时校验，dispatch 步数超限 → 整单 400（不部分执行）。
- `max_tokens`：子任务静默唤醒时核算——主任务树全部 run 的 UsageIn+UsageOut 合计
  超限 → plan failed（error=budget_exceeded）+ 主任务落 blocker（人可见）。

### 5. 手动打回（验收回流的人工半环）

新命令：`POST /api/v1/work-items/{id}/commands/return`，body `{reason?, expected_version}`。
语义：in_progress 且 phase=acceptance/review 时合法 → BeginExecution（phase→execution）
+ activity 记录 reason；todo/completed/cancelled → 409。前端「打回重做」按钮消费此端点。
打回后的再交付路径：与对应 agent 继续 chat（会话锚点仍在，tier-1 续接）→ 新 run 交付。

## 否决方案

- **join 恢复跨唤醒游标**：否决（M1 已定 defer 即终止；join 只是收窄等待集的 defer）。
- **认领引入独立 claim 表**：否决。认领=指派+唤醒的复合命令，无一等实体必要。
- **预算护栏实时熔断（run 进行中）**：否决。MVP 在唤醒点核算；run 级流式预算
  熔断需要 adapter 侧用量回传协议，超 M4 范围。
- **审批走新审批类型表**：否决。ApprovalRequest 加 kind 值即可，既有解决链路复用。

## 验收

- claim：无 assignee 的 todo 任务被认领 → assignee 落定 + assignment wakeup 入队；
  已有 assignee → 409；同 agent 重复认领幂等。
- join{children:[A]}：仅 A 静默即唤醒（B 仍活跃不等）；join 目标非子任务 → 400。
- 审批护栏：dispatch 到 manual 审批 agent → plan waiting + ApprovalRequest(kind=
  plan_dispatch)；批准 → step 执行子任务落库；拒绝 → plan failed。
- 预算护栏：max_dispatch=1 提交 2 个 dispatch → 400；max_tokens 超限 → 唤醒核算后
  plan failed + blocker。
- return：acceptance 态打回 → phase=execution + activity；completed 态打回 → 409。
- 触面验证同前 + 迁移 0010 双跑。
