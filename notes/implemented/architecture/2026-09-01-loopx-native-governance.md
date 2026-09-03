# 原生移植 LoopX 长程治理语义，保留 ATW 执行底座

Status: implemented

## 决策与理由

在 Agent Team Workbench 内以 Go + SQLite 原生实现 Goal、Todo、Claim、DecisionScope、Quota、TurnDecision、TurnReceipt、Handoff、Evidence 与 ProjectionRepair；保留现有 `WorkItem → Plan → Dispatch → ExecutionRun → TaskSession → RunnerGateway` 作为唯一执行底座。

治理层只拥有长期意图、bounded action、准入、结算收据和证据投影；Todo 必须经确定性 `TodoToPlanCompiler → SubmitPlan` 才能触发执行。Goal/Todo、WorkItem/Plan/Run、TaskSession/Runner Lease 分别拥有意图、执行、Provider/进程三类真相，不建立第二套 `.loopx` 状态、Run、Lease、Scheduler、Settlement 或事件库。

优先顺序固定为 provider schema-constrained `PlanDecisionV2` → 1–2 次持久 `repair_plan` → provider-neutral `submit_plan`。本次收口已整体删除旧 fenced-plan 生产解析与模式开关；所有 Provider 路径进入同一 raw PlanDecisionV2 decoder。该路线保留 ATW 的中心化事务、多 Host Runner、审批和产品 UI，同时移植 LoopX 更成熟的长程治理协议。

TurnReceipt 是 append-only canonical stream：Todo admission CAS 分配 `(goal_id,todo_id,turn_seq)`，Header/Phase 以 RFC8785 canonical JSON + SHA-256 digest 固化；Goal/Todo/timeline/evidence summary 是可重建 projection，repair 不得创造或改写历史 Receipt。Quota 使用闭合整数单位和同 turn_key 的 reservation/commit/release；cost 按 uncached input/cache read/cache write/output billable buckets 与固化价格快照结算，禁止把 cached token 重复计入 input 价格。只有启用了对应 quota kind 的 Goal 在价格或 per-run usage 无法证明时 fail-closed。

Quota 的事务身份按单位区分：`turn_count` 是 admission-only，reservation 与 Header 同事务，Header 成功后即使后续 Plan 失败也不倒扣；`active_worker` 是瞬时 gauge，不建 spend/reservation，必须与 Worker/Worker retry Run insert 在同一事务 gate；token/cost 是 Turn 内多 Run 共享的 aggregate reservation，与 Header 同事务冻结并由 source Run/plan client key/decision digest checkpoint 保证可重放。永久 Plan 拒绝同事务关闭 reservation，存储故障保留 `plan_commit` retry。一个 Turn 可派发多个不同模型的 Run，因此 cost reservation 只冻结 turn-level policy/aggregate amount，不拥有单一 price snapshot；价格快照属于每个 immutable Run，per-Run spend 的 `price_digest` 必须回指该 Run 的快照。

DecisionScope 是 Todo 创建时的权限快照：`agent_ids` 同时约束 claim actor 与 dispatch target，初始 Todo 固化 Coordinator + 当时 enabled Worker roster；claim/admission 后不可修改，后续 roster 变化不得静默扩权。`work_item_ids` 中的 root 仅蕴含其 direct Plan child 的 join/观察权限，不授权任意 descendant 或跨 root 写入。治理 Plan 另以 immutable `(goal_id,todo_id,turn_seq)` 与 workspace-scoped client key 标识，同 key/same decision digest 返回原 Plan，different digest 冲突；`source_run_id` 仍保留 Planner 因果，但不能独自代表跨 repair/replay 的治理 Turn 身份。

Receipt phase 表达多个已提交边界，不用一个长 SQL 事务包住 Runtime 副作用：claim + admission Header 同事务；decision/validation/durable-writeback 依序追加；`SubmitPlan` 继续原子创建 Plan/child/queued Run，commit 后才 dispatch，再追加 plan_compile/dispatch phase。崩溃恢复以 TurnKey、Plan client key 与 phase digest 补齐缺口，禁止再次派发。

合同发布分为 live 与 proposed：`PlanDecisionV2` JSON Schema 是 Planner wire shape 的 canonical contract，WP0 的标准库门禁只证明 schema 结构不漂移；实例拒绝、RFC3339 assertion 与强类型解码必须在 production decoder 接入时用同一 schema 的正反 fixtures 证明。尚无 producer 的治理事件只登记为未挂载 channel 的 AsyncAPI `x-lifecycle: proposed` components，不进入 domain event whitelist、live aggregate enum 或前端 listener；promotion 必须与 producer、payload test 和 projection 同刀完成，并删除 proposal。

TurnReceipt 的 canonical digest 直接采用 `github.com/gowebpki/jcs` 的 RFC 8785 实现，再做 SHA-256；不自行维护通用 JSON 数字/Unicode/键排序算法。Header/Phase digest payload 使用闭合 DTO，排除 digest 字段本身并把空集合归一为 `[]`，避免 nil/empty 形成两种身份。

Goal/Todo/Receipt/Quota/Handoff/Evidence/Projection/Delivery Brief snapshot/quota reconciliation 事件均已与对应事务 producer、domain whitelist、AsyncAPI payload 和 Web invalidation 同步升为 live。无 producer 的 proposal 不保留在 live channel。

2026-09-03 收口事实：根控制线的 blocker 是四层原子状态转换——WorkItem、Task Coordinator、Goal、current Todo 同事务进入 blocked，Todo claim 同时释放，root 下所有 open Dispatch 由 CAS sweep 关闭为 `degraded`；并发或迟到终态不能复活批次。显式 `UnblockWorkItem` 在同一事务恢复 WorkItem、Goal active、Todo pending，保留 `last_turn_seq`、不复用旧 claim，并重开新的 Coordinator/repair budget；不创建伪 Receipt。

同一 fail-closed 边界适用于治理缺失：受管 system Coordinator 缺少 Goal 或 current Todo 时，在创建 Run 前阻塞并返回 `governance_state_unavailable`，不调用普通/legacy `SubmitPlan`。waiting Plan 被新 Plan supersede 时，旧 Plan 的 pending manual dispatch approval 同事务进入 `expired`，迟到决定不能创建 child Run。

Handoff 不再只是 claim transfer 记录：接受与 target claim、Coordinator durable checkpoint 同事务；source 终态后才由 target 创建 delegated Coordinator Run。same-generation 续租只能保持 owner/`claimed_at` 并延长 expiry；source 的迟到 PlanDecision 只作 evidence，settlement wake 在消费时重解析当前 target。blocker 释放 claim 并清除 continuation checkpoint，但不改写已 transferred Handoff 历史。

`repair / replan / user_action` 的无 Plan 结果使用与 execute/wait/finish 同一个 TurnDecision 闭集和 phase 1–7 receipt。重放必须重算 Header input digest；源 Run 已属旧 Turn 时先结算旧 reservation，不将 usage 二次归入新 control Turn。Todo 完成绑定最新 admitted TurnReceipt Header 和已验收 root WorkItem evidence，completed/cancelled 整行在 SQLite 不可变。

治理迁移为 0024–0035 与 0039–0042；0036–0038 是同分支已实现的独立 Agent config/HTTP 幂等加固。0035 将 `canonical_usage(+digest)` 的 terminal-only 约束写入 SQLite trigger；Go repository guard 与数据库 trigger 双重拒绝非终态 canonical，升级遇到既有违规时迁移失败而不是静默放过。REST/MCP 的治理集合空值统一编码为 `[]`；真实浏览器在 1440/1024 宽度验证 blocker 状态链一致且无横向溢出，截图见 [`docs/review/assets/`](../../../docs/review/assets/)。

## 放弃了什么

- **整体替换为 LoopX 底座**：无论直接运行 Python/TypeScript，还是用 Go 重写其 Goal/Todo/Turn/Effect 语义并替换 ATW 领域模型，都会重写现有迁移、事务、Runner、API、前端和历史数据，且仍需重新实现 ATW 已有多 Host 执行能力。
- **外部 LoopX 生产控制层**：Goal/Todo 与 WorkItem/Plan、文件态与 SQLite、LoopX lease 与 Runner lease 会形成双真相源；仅允许作为限时、只读或 advisory 的跨项目实验。
- **只改 Prompt 或宽松解析 JSON**：不能解决写后校验、幂等、repair、quota 和证据门禁；不新增永久 regex/JSON 清洗垫片。
- **永久保留 fenced-plan 双轨**：迁移完成后成建制删除 parser、旧 prompt 契约和无消费者字段。
- **把 Claim 与 Runner Lease 合并**：前者是治理所有权，后者是执行进程租约，生命周期和冲突语义不同。
- **把 session cumulative usage 当 per-run spend**：无法证明 delta 时必须停在 `usage_unresolved`，不能猜测或记零。
- **在 turn-level cost reservation 冻结单一模型价格**：同一 Turn 可包含 Coordinator、多个不同 Agent Worker 与 evaluation Run；单一 price snapshot 会错误拒绝或错误计价多模型 Turn。价格只能按 Run 冻结并由 spend 引用。
- **让 repair 重写 canonical Receipt**：repair 只重建 projection；Receipt identity/digest 冲突必须响亮失败。
- **只用 source_run_id 作为治理 Plan 幂等键**：repair/new source Run 会让同一 Turn 重复 Plan；必须使用 TurnKey + client key + decision digest。
- **用一个长事务覆盖 receipt、Plan 与 Runtime dispatch**：外部副作用无法随 SQLite 回滚；改为可重放 phase 边界与 commit 后 dispatch。
- **首版建设完整 Goal 编辑器或计费系统**：先实现服务端权威语义与最小可观测 UI；计费、套餐、多租户账单不在范围。
- **WP0 直接把未来治理事件加入 live SSE 白名单**：白名单代表允许发布，不是设计草稿；没有 producer 时只做 proposed contract，避免协议宣称超过代码事实。
- **把现有间接 JSON Schema 依赖当成已选定 validator**：间接依赖来自 MCP 传递树，不构成控制面技术选择；production decoder 选型时必须显式提升为 direct 并补实例 fixtures，或选择另一实现。
- **自写完整 RFC 8785 canonicalizer**：数字序列化、Unicode 与递归属性排序很容易产生跨语言漂移；只保留 Receipt 闭合 digest DTO，canonicalization 交给有 RFC vectors 的直接依赖。

## 复活条件

- **整体迁移 LoopX**：只有产品战略明确改为 project-local、CLI/harness-first；现有 SQLite/RunnerGateway 经容量实测无法满足目标；团队接受重写 API/UI/迁移历史和 6–9 个月 parity 空窗；并完成真实数据迁移演练时，才重新评估。
- **外部混合层**：只有出现跨多个 ATW 实例/仓库的真实 supervisor 需求，且能坚持单向 projection、LoopX 不拥有 Run/Lease 终态、实验有明确删除日期时，才允许限时启用。
- **暂缓删除 fenced plan**：只有目标 Provider 全部缺少 schema/tool 能力，且 bounded repair 的真实成功率仍无法达到验收门槛时，才延长迁移期；仍不得把旧路径恢复为长期主协议。
- **扩大 Quota**：出现真实多租户计费、套餐或账单需求后，另立产品目标；当前只做 turn、并发 Worker、token/cost 与幂等 spend。
- **治理事件转 live**：对应 Domain/Service producer、event/outbox 同事务、AsyncAPI payload、前端失效重拉和行为测试同时完成时，才从 proposed components 升级；升级同刀删除 proposal。

## 关联

- [系统架构事实源](../../../docs/architecture/system-architecture-handbook.md)
- [LoopX 任务底座架构评估](../../../docs/architecture/2026-09-01-loopx-task-foundation-architecture-assessment.md)
- [目标与需求合同](../../../docs/product/loopx-native-governance-goal.md)
