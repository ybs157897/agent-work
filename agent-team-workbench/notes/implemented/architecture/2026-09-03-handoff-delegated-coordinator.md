# Handoff continuation uses a delegated Coordinator Run

Status: implemented

## 决策与理由

接受 Handoff 只在同一事务内完成 Todo claim transfer，并写入根 Task Coordinator 的 durable continuation checkpoint；它不直接创建或 dispatch Run。恢复循环读取这个 checkpoint 后，使用目标 Agent 的 Runtime 配置，经现有 `createRunLocked`、不可变 `ContextSnapshot` 和 `TaskSession` 锚点创建一个带 `handoff_id` proof 的 delegated Coordinator Run。首轮若存在 source Run 则克隆其 ContextSnapshot，后续轮次回到目标 Agent 的常规 session resume/fresh 判定。

delegated Run 与系统 Coordinator 共用 PlanDecision/TurnReceipt/Plan 编译路径，但只有当前 transferred Handoff、Todo claim generation、Coordinator state/current Run 和 target Agent 全部匹配时才允许普通 Agent 作为 root Planner。这个 proof 同时由 Go authority gate 与 SQLite governed-receipt trigger 检查。

## 放弃了什么

- **AcceptHandoff 内直接调用 CreateRun/Dispatch**：会把控制面事务和 Runtime 副作用耦合，并在崩溃或重放时留下重复 Run；改为 durable checkpoint + Coordinator recovery。
- **把目标 Agent 当普通 Worker 派发到 child WorkItem**：Handoff 的语义是接管当前 bounded Todo，而不是凭空合成新的 Plan child；该方案也无法保留 current Todo 的 TurnKey/claim identity。
- **仅把 source/target 写入自由文本 prompt**：文本不能证明当前 claim generation，不能阻止旧 Handoff 在 ABA 后复活；因此采用 Handoff row + state proof + claim CAS 的闭合身份链。

## 复活条件

【未来需要同一 Todo 并行多个有效接管者 → 扩展 Handoff/claim generation 的选择规则、SQLite trigger 与 activeTransferredHandoff 约束 → 先保留单一匹配原则，禁止静默选择任意一个 transferred Handoff】
