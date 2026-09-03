# 迁移 LoopX 不会自动让 ATW 更强：任务执行底座架构调研

- 日期：2026-09-01
- 证据截止：2026-08-31
- 状态：historical research recommendation；用户已于 2026-09-03 确认 B（原生语义移植）并进入实施

> 后续状态：本文的能力对比和估算保留 2026-08-31 的研究基线，不回写成事后数据。被采纳的路线、当前实现与验证以 [决策 note](../../notes/implemented/architecture/2026-09-01-loopx-native-governance.md)、[目标合同](../product/loopx-native-governance-goal.md) 和 [完成审计](../review/2026-09-02-loopx-native-governance-completion-audit.md) 为准。

## 结论

推荐路线不是把 LoopX 实现直接接入，也不是用 LoopX 的 Goal/Todo/Turn 模型替换现有底座；推荐的是**原生语义移植**：

- 保留 ATW 已有的 `WorkItem → Plan → Dispatch → Run → TaskSession → RunnerGateway` 执行底座、SQLite 事务、Outbox/SSE、审批和 Web 工作台。
- 将 LoopX 更成熟的 `Goal / Todo / Claim / Quota / Turn Receipt / Handoff / Evidence / Repair` 语义，以 Go + SQLite 的方式加入 ATW 上层控制面。
- 上层 Todo 只能经一个确定性的编译器写入现有 `SubmitPlan`；不得再拥有第二套 Run、Lease、Settlement 或事件真相源。
- 优先把 Coordinator 的自由文本 ` ```plan ` JSON 边界升级为 typed/schema-constrained decision + bounded repair；这是 LoopX 对当前 JSON 失败最直接的架构启示。

三条路线的分析模型评分如下。评分是基于源码证据的**决策建模**，不是生产基准测试：

| 路线 | 加权分 | 结论 |
|---|---:|---|
| A. 整体迁移 LoopX 底座 | 61 / 100 | 不推荐；长程治理增强，但会重写已成熟的事务、Runner、API 与 UI |
| B. 原生语义移植 | **96 / 100** | **推荐**；保留执行优势，补齐长程治理与 typed boundary |
| C. 外部混合层 | 72 / 100 | 仅适合短期实验或跨项目上层；生产态双真相源风险过高 |

一句话判断：**LoopX 的上层治理架构比 ATW 强，ATW 的任务执行底座比 LoopX 更适合当前产品；最强组合是“迁语义，不迁实现，不换底座”。**

## 范围与方法

### 固定基准

- LoopX：`main@9a9526aa33fa217efe4cb95841ed344848daf49a`。
- ATW：本 worktree `HEAD@82665d8`；只读取控制面源码，忽略工作区中与本调研无关的配置与本地运行文件。

### 证据标记

- **观察**：源码、契约或持久化结构中可直接验证的事实。
- **推断**：由多处观察归纳出的架构判断。
- **估算**：工期、成本和决策分值；不冒充实测。

### 关键源码

LoopX 固定 SHA：

- [Goal start contract](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/loopx/control_plane/goals/start_contract.py#L16-L78)
- [Turn driver routes](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/loopx/control_plane/turn_driver/driver.py#L46-L104)
- [Turn transaction contract](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/loopx/control_plane/turn_transaction_contract.json)
- [Authority core](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/loopx/control_plane/coordination/authority_core.py#L1-L82)
- [Task lease](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/loopx/control_plane/work_items/task_lease.py#L19-L56)
- [Repair delta](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/loopx/control_plane/work_items/repair_delta.py#L34-L78)
- [Evidence log](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/loopx/control_plane/runtime/agent_scoped_evidence_log.py#L13-L80)
- [Apache-2.0 License](https://github.com/huangruiteng/loopx/blob/9a9526aa33fa217efe4cb95841ed344848daf49a/LICENSE)

ATW：

- [C4 容器图](c4-container-diagram.md)
- [WorkItem 状态机](../../agent-team-workbench/internal/domain/workitem.go)
- [Plan 模型](../../agent-team-workbench/internal/domain/plan.go)
- [确定性 Plan 执行器](../../agent-team-workbench/internal/application/plan.go)
- [Coordinator 控制线](../../agent-team-workbench/internal/domain/task_coordinator.go)
- [Coordinator 引擎](../../agent-team-workbench/internal/application/coordinator_engine.go)
- [RunnerGateway](../../agent-team-workbench/internal/runnergateway/gateway.go)
- [SQLite Store](../../agent-team-workbench/internal/persistence/sqlstore/store.go)
- [Plan 文本提取](../../agent-team-workbench/internal/application/plan_extract.go)
- [系统 Coordinator 决策](../../notes/implemented/architecture/2026-08-30-system-task-coordinator.md)

## 两套架构的真实分层

### LoopX

LoopX 是 project-local 长程控制面，不是执行模型或通用 Runner：

```text
Goal / Registry
  → Todo / Claim / Decision Scope
  → Quota should-run
  → Turn Driver
  → Host Runtime
  → Typed Result / Validation
  → Durable Writeback / Quota Spend
  → Scheduler Apply / Ack
  → Handoff / Evidence / Repair
```

**观察**：它的强项集中在以下控制语义：

- Goal 启动前置规划，并要求 Todo 写入遵循 contract。
- Todo 带 task class、decision scope、claim/lease 与 resume 条件。
- Turn 有版本化 envelope、action signature、receipt 和七阶段 settlement。
- `READY_FOR_HOST / REPAIR / REPLAN / USER_ACTION / WAIT / BLOCKED` 是显式路由。
- Evidence、handoff、quota spend、projection repair 都是一等语义。
- 持久化以 JSON/Markdown、原子文件写和跨 runtime 文件锁为主，不提供 ATW 式跨实体 SQL ACID。

### ATW

ATW 是中心化、产品化的任务执行与多 Runtime 工作台：

```text
WorkItem / Task Coordinator
  → Plan
  → Dispatch
  → ExecutionRun / TaskSession
  → ExecutionContextSnapshot
  → ModuleRunner or RunnerGateway
  → Run events / Outbox / SSE
  → Evaluation / Review Queue / Human Accept
```

**观察**：ATW 已经有：

- 单 SQLite 事务中的状态、事件、Outbox 和幂等写入。
- 13 态 Run 状态机、不可变执行上下文、TaskSession 锚点。
- host-aware RunnerGateway v2、lease/fencing、远程事件去重和 ACK。
- 确定性 Plan 动词、dispatch 批次、join/defer、审批、重试、reconcile。
- 系统 Task Coordinator、完整 REST/SSE 契约和 React 工作台。

**缺口**主要在上层：跨多个 bounded turn 的 Goal/Todo 权威、全局 Quota/Spend、显式 ownership/handoff、receipt-bound settlement 和 projection repair。

## 谁更强

| 能力 | LoopX | ATW | 判断 |
|---|---|---|---|
| 长程 Goal 治理 | Goal/Todo/Quota/Turn/Handoff/Repair 完整 | 根 Task Coordinator 为主 | LoopX 强 |
| Typed command boundary | CLI/typed result，写前 decode | 最终文本 ` ```plan ` JSON，写后解析 | LoopX 强 |
| Quota 与结算收据 | should-run、spend、receipt、replay | Plan 级 max_dispatch/max_tokens | LoopX 强 |
| Claim / Handoff | 一等实体与 gate | 指派、Task lock、session summary | LoopX 强 |
| 中心化事务 | 原子文件写、事件/投影 | SQLite `InTx` 跨实体事务 + Outbox | ATW 强 |
| 多 Host 执行 | 依赖外部 harness/host surfaces | RunnerGateway v2 + host/context fencing | ATW 强 |
| Plan 执行确定性 | Todo/Turn 驱动 | 动词闭集、批次与等待屏障 | ATW 强 |
| Runtime 适配 | 多 harness surface | Codex/Kimi/DSH/Claude/ZCode adapters | 各有优势；ATW 更贴当前产品 |
| 审批与产品验收 | operator/promotion gates | Run/Plan 审批、evaluation、人工 Accept | ATW 更完整 |
| Web/API 产品连续性 | Dashboard/CLI 为主 | REST/SSE + React 全链路 | ATW 强 |

因此，LoopX 不是“全面更强的执行底座”；它是**更强的上层长程治理内核**。将它放到 ATW 执行层下面，会把它最强的层级放错位置。

## 三条路线

### A. 整体迁移 LoopX 底座

定义：让 Goal/Todo/Turn/Effect/Quota/Lease/Handoff 成为权威域模型，替换或降级 ATW 的 WorkItem/Plan/Dispatch/Coordinator。无论直接运行 Python/TS，还是用 Go 重写 LoopX 语义，都属于整体迁底座。

**优点**：

- 一次获得较完整的长程治理词汇和 Turn settlement 思维。
- 更接近 project-local、跨 harness 的便携式 Agent 控制面。
- 可减少一次性大 JSON Plan 的中心地位。

**缺点**：

- WorkItem/Plan/Dispatch/Coordinator、23 版迁移、Runner 路由、API、前端 store 和读模型都要重写或重新映射。
- LoopX 文件态与本地锁不替代 ATW 的中心化多 Host 事务；迁移后仍要重新实现 RunnerGateway、审批和 SQL 可靠性。
- 直接复用代码引入 Python + TypeScript + Node effect runtime；Go 重写则失去“直接复用”的工期收益。
- 产品空窗、历史数据迁移和回归风险最高。

**估算**：9–14 人月，6–9 个月完全收口；大部分 application、persistence、runnergateway、httpapi 与 web 任务路径都会被触及。当前这些目录约 465 个 Go/TS 源文件，说明迁移不是替换一个 orchestrator 包。

### B. 原生语义移植（推荐）

定义：在 ATW 内原生实现 LoopX 的上层语义，不运行 LoopX，不复制其文件状态；所有新增状态继续进入 SQLite、Outbox 和现有 API/事件体系。

目标分层：

```text
Goal                         新增：跨 bounded turn 的长期目标
  → Todo / Claim / Scope     新增：上层执行意图与所有权
  → Quota / ShouldRun        新增：turn / worker 准入与花费
  → TurnDecision / Receipt   新增：repair/replan/wait/user_action 与结算收据
  → TodoToPlanCompiler       新增：唯一写 Plan 的编译边界
  → SubmitPlan              保留：确定性、原子校验执行
  → Dispatch / Run / Session / RunnerGateway   全部保留
  → EvidenceProjection       新增：把 Run/Brief/Artifact 上浮到 Goal
```

关键不变式：

1. Goal/Todo 对“为什么做、下一步做什么”权威。
2. WorkItem/Plan/Run 对“实际执行了什么、结果是什么”权威。
3. Todo 不直接写 Run；只能经 `TodoToPlanCompiler → SubmitPlan`。
4. 不新增第二套 lease、runner、settlement、event store 或 completion 状态机。
5. Goal 投影失败可 repair；不能反向改写已终态 Run。

**优点**：

- 同时保留 ATW 的事务/多机/UI 优势和 LoopX 的长程治理优势。
- 可按阶段交付，每一步都有独立收益和回滚边界。
- 无双语言运行时、无文件态双写、无外部控制面运维。
- 可以针对 ATW 领域重新设计，而不是强行映射不等价的 Todo 与 Dispatch。

**缺点**：

- 仍需自己实现并验证 Goal/Todo/Quota/Receipt 语义。
- 只借鉴算法时必须避免选择性复制导致语义不完整。
- 会增加一层领域模型，必须严格定义 Goal/Todo 与 WorkItem/Plan 的所有权边界。

**估算**：3–5 人月；2–3 周可先解决结构化决策与 repair，8–14 周完成可用的 Goal/Todo/Quota/Handoff/Evidence 原生控制层。

### C. 外部混合层

定义：运行 LoopX 或兼容控制层作为上层 Goal 真相，通过桥接器驱动 ATW。

**优点**：

- 最快试用真实 LoopX 行为，少量代码即可验证 Todo/Quota/repair 产品价值。
- 对跨项目、跨 ATW 实例的 supervisor 场景有吸引力。
- 可作为研究沙箱，不立即改 ATW 核心域。

**缺点**：

- Goal/Todo 与 WorkItem/Plan 双真相；repair、blocker、retry 可能给出冲突 next action。
- Python/TS 文件态与 Go/SQLite 同时运维，调试链和故障归因更长。
- 必须构建双向同步、幂等和 divergence repair，长期维护成本不会自然消失。
- 一旦上层控制真正拥有调度权，最终仍要做一次单真相收口。

**估算**：5–8 人月；10–16 周达到可用 Beta，之后需持续承担约 1–1.5 FTE 的桥接/分歧治理，除非明确排期去掉一侧真相。

## 决策矩阵

评分范围 1–5；每项乘权重后折算为 100 分。评分是**模型估算**，用于显式化取舍，不是性能实测。

| 维度 | 权重 | A 整体迁移 | B 原生移植 | C 外部混合 |
|---|---:|---:|---:|---:|
| 产品/领域契合 | 15% | 2 | 5 | 3 |
| 事务与状态正确性 | 15% | 3 | 5 | 3 |
| 长程治理完整性 | 15% | 5 | 5 | 5 |
| 多 Host / Runtime 执行 | 15% | 2 | 5 | 4 |
| Planner 边界可靠性 | 10% | 4 | 5 | 4 |
| Evidence / Audit / Review | 10% | 4 | 5 | 4 |
| 迁移风险与可逆性 | 10% | 1 | 4 | 2 |
| 运维与可维护性 | 5% | 2 | 4 | 2 |
| 跨 harness 扩展性 | 5% | 5 | 4 | 5 |
| **加权总分** | **100%** | **61** | **96** | **72** |

敏感性：即使将“跨 harness 扩展性”权重翻倍，A/C 仍无法抵消事务、产品连续性和迁移风险的差距；只有产品战略转向 project-local CLI，A 才可能反超。

## JSON Plan 问题

LoopX 没有“更强的 JSON Plan parser”。它通过以下方式**规避**问题：

- Planner 产出 Todo/ordered steps，而不是控制面直接执行的一次性 JSON dispatch 树。
- Agent 通过 typed CLI/action packet 写回。
- Typed result 在 validation 阶段进入 settlement；错误路由到 repair/replan/user action。

ATW 当前要求模型输出 fenced ` ```plan ` JSON；[plan_extract.go](../../agent-team-workbench/internal/application/plan_extract.go) 在 Run 成功后才提取、反序列化和语义校验。围栏名、JSON 语法、字段或 join/defer 语义错误最终容易变成 blocker。

原生移植路线不应继续堆 JSON 清洗器，而应分三步：

1. 定义版本化 `PlanDecisionV2` / JSON Schema，并把 capability 区分为 structured transport 与 schema-constrained output。
2. provider 支持时使用原生 schema；不支持时把精确校验错误回送同一 Coordinator session，最多自动 repair 1–2 次，耗尽才 blocker。
3. 中期增加 provider-neutral `submit_plan` 或 staged `plan draft → validate → commit` 工具；工具不得逐步直接 dispatch，必须保留 `SubmitPlan` 原子性。

LoopX 的启示不是换一个 JSON 格式，而是把**不可信模型输出限制在意图层，把可执行动作压到 typed command + receipt-bound settlement**。

## 推荐实施序列

### WP0：结构化决策边界（2–3 周）

- `PlanDecisionV2` DTO/schema。
- 语法、schema、语义错误码拆分。
- Coordinator bounded repair 状态持久化。
- 原 fenced plan 保留只读迁移期 telemetry，不静默宽松执行。

退出条件：真实 smoke 中 plan 格式错误不再直接要求用户 unblock；repair 耗尽仍可解释地阻塞。

### WP1：Goal / Todo / TurnReceipt（3–4 周）

- 新增 Goal、Todo、Claim、TurnDecision、TurnReceipt 表与领域状态机。
- Todo 编译为现有 Plan，source identity 进入幂等键。
- 先只支持单根 Goal、顺序 bounded turn，不做通用 DAG。

退出条件：重启后能从 Goal/Todo/Receipt 恢复下一动作，且不会重复创建 Plan/Run。

### WP2：Quota / ShouldRun（2–3 周）

- turn、并发 Worker、token/cost 的 admission policy。
- spend 与 Run usage 同事务或通过 Outbox 精确结算。
- quota 不替代现有 Plan guardrails，而是更上层准入。

退出条件：同一 Turn 重放不重复 spend，超额时不创建 Run。

### WP3：Handoff / Evidence / Repair（3–4 周）

- 显式 ownership 与 handoff contract。
- DeliveryBrief、Artifacts、Run attempts 投影到 Goal evidence。
- projection repair 只修读模型/控制投影，不覆盖领域终态。

退出条件：换 Agent/Runtime 后能凭 receipt+evidence 继续；投影损坏可自动重建。

### WP4：UI 与收口（2–3 周）

- Task 页面增加 Goal/Todo/Quota/Handoff 视图，但保持一个任务时间线。
- 删除迁移期 fenced-plan 主路径和无消费者字段。
- 补全 API/AsyncAPI/迁移与端到端恢复证据。

总工期不是各包简单相加；2–3 名熟悉 Go/控制面的工程师并行，预计 8–14 周到生产可用。

## 主要风险与护栏

| 风险 | 路线 | 护栏 |
|---|---|---|
| Goal/Todo 与 WorkItem/Plan 双真相 | B/C | B 中按意图层/执行层分权；只有编译器可写 Plan |
| 复制 LoopX 语义不完整 | B | 逐能力写 contract test；不复制零散 helper |
| 外部桥接长期不收口 | C | 预先设置删除日期、divergence 指标和单真相里程碑 |
| 文件锁语义误用在多机 | A/C | 保留 ATW DB lease/fence；不复制文件 lease 实现 |
| Repair 无限循环 | B/C | 持久预算、退避、终止到 user action |
| Evidence 变成非权威摘要 | B/C | 证据引用 Run/Artifact/Brief ID，禁止模型自报作为完成依据 |
| Apache 代码复制合规 | 全部 | LoopX 为 Apache-2.0；复制源码前保留 NOTICE/归属并先补 ATW 自身 LICENSE 策略 |

## 停止与复活条件

### 原生移植停止条件

- 新层无法保持 Goal/Todo 与 WorkItem/Plan 的唯一所有权边界。
- 连续四周投影分歧率超过 1%，且 repair 无法自动收敛。
- Quota/Receipt 让 Run 创建 p99 增加超过 2 倍且无明确优化路径。
- 维护成本持续超过 1.5 FTE，而核心 JSON/恢复指标没有改善。

### 整体迁移复活条件

只有同时满足以下条件才重开 A：

- 产品战略从中心化 Web 工作台转向 project-local、可携带、CLI/harness-first。
- 现有 SQLite/RunnerGateway 被实证无法承载目标规模，而不是仅凭架构偏好判断。
- 团队愿意放弃或重写现有 API/UI/迁移历史，并接受 6–9 个月产品空窗。
- 已完成真实数据迁移演练和 ATW 能力 parity 清单。

### 外部混合层适用条件

- 只用于跨项目 supervisor、研究沙箱或 4–6 周限时实验。
- LoopX 只能给 advisory Todo/Quota，不得直接拥有 Run/Lease 终态。
- 实验开始前就定义删除桥接器或升级单真相的日期。

## 当时的最终建议

1. 批准 B 的 WP0，先解决 Planner 边界与 repair；这是收益最快、与底座替换无关的共同前置。
2. 用 WP1 的最小 Goal/Todo/Receipt 验证长程治理价值，再决定是否继续 Quota/Handoff。
3. 不批准 A 的全量迁移，也不把 C 作为生产目标架构。
4. 若需要验证 LoopX 原生体验，可单独做不写 ATW 状态的只读/离线实验，不能接管调度。

上述建议后续已被用户确认，并以 implemented decision note 留痕。本报告继续作为决策前的研究证据，不承担当前实现状态的事实源。
