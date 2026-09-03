# WP4-B/C 落地：canonical usage 应用链路与 usage-backed quota 结算

Status: implemented

## 决策与理由

WP4-B/C 把 0028 的 usage 证据列与 0027 的 quota 台账接进现有执行链，不新增第二套
usage/settlement 库。关键裁决如下。

### 1. ProviderUsageReport 的持久化边界

`RecordRunUsage`（进程内 OnUsage/ExecResult 与 runnergateway usage.updated 共用
`recordRunUsageTx`）同事务持久最新 `ProviderUsageReportV1`：

- 首次写入 seq=1；digest 相同的重放保持 seq 不变；digest 变化 seq+1（0028 trigger 兜底）。
- report 的 run/agent/adapter 身份必须与 Run 行一致，否则拒绝写入（防串账）。
- canonical usage 已落之后，不同 digest 的 report 不再改写 latest report（0028 trigger 禁止），
  应用层跳过该写入但继续 legacy usage_in/out/cached 投影（迟到上报语义不变）。

### 2. canonical usage 的唯一生成点

nullable counter 的 JSON 语义在域层修复：`UsageCountersV1` 五字段全部 `omitempty`——
「provider 未暴露」序列化为键缺失而非 JSON null。SQLite `json_type` 对显式 null 返回
文本 'null'，0028 anchor trigger 的「键缺失」判定只有键真缺失才成立；写点侧 scrub
垫片（先序列化再删 null 键）已在评审中删除，digest 一律由 Seal 现算，无对外承诺负担。

`canonicalizeRunUsageLocked` 是唯一写点，被三处共享同一终态钩子管线触发
（RecordRunStatus / replayRunTerminalHooks / replayCoordinatorTerminalHooks），
以及 RecordRunUsage 对「已终态但未 canonical」的迟到上报兜底：

- per_run report：纯 `domain.CanonicalizeProviderUsageReport`。
- session_cumulative report：事务内 fresh 读 TaskSession anchor，纯
  `CanonicalizeProviderUsageV1` 做差；`FreshProviderSession = (run.SessionBefore == "")`
  是零基线的唯一许可。canonical 写入与 anchor 推进（专用 CAS，
  `UpdateProviderUsageAnchorCAS`，expectedSeq 守卫）必须在同一 SQLite 事务；
  CAS 失败整事务回滚，由下一次钩子重放重算。
- 受管 Run（input.governance 非空）终态但没有 provider report 时，进行式钩子不立即
  冻结 absent canonical；仅在 Turn 其余关闭条件齐备的关闭性 sweep 中，同事务合成
  全 unresolved canonical（reason=provider did not report usage）并结算。关闭前迟到
  report 仍可首写真值；非受管 Run 无 report 不写，保持 NULL/NULL legacy 语义。
- cost 二段制：canonical 先落 usage 桶；Run input 携带 price_snapshot 时再计算
  `ComputeCostMicroUSD`（checked integer，half-up）。价在桶齐 → cost 进 resolved 并
  固化 PriceDigest；价在桶缺 → cost 进 unresolved（reason 来自 CostUnresolvedError），
  PriceDigest 仍固化（0027 要求 cost spend 行必带 price_digest）；无价且受管 →
  cost unresolved（cost_price_unavailable）；无价且非受管 → cost 不参与（domain 豁免分支）。
- canonical 一旦落库不可改写：重放重算 digest 相同即 no-op；不同则保留既有值并记日志
  （trigger 是最终强制）。
- 迟到上报（RecordRunUsage 打在已终态 Run）的 canonical 兜底与 usage 写入同事务，
  且失败必须回滚整个 usage 事务：canonicalize 先写 canonical 再推进 anchor，
  若 anchor CAS 失败后吞错提交，会留下「按旧基线结算、基线未推进」的半态，
  下一个 Run 从旧水位重复做差（双倍计费）。宁可丢一次迟到上报（受管 Run 由
  sweep 补 unresolved），不留错误账本。钩子路径（专用事务）天然原子，不受影响。

### 3. usage-backed reservation 的创建与尺寸

- reservation 在 admission（`ensureGovernancePlanAdmission`，与 Header 同事务）按
  Goal 当前 policy 逐 kind 创建；admission 重放（existing Header）同样补齐 reservation。
  worker/eval/heal Run 创建路径只做 get-or-create（存在即复用冻结值，不按当前水位重算），
  满足「与对应 Run/client key 同事务」且不含半态。
- `reserved_amount = max(0, policy.limit − committed_total − Σ active reserved)`：
  跨 Turn 不超订；Turn 内多 Run 共享本 Turn 的 reservation（0027 trigger 聚合上限兜底）。
- audit/enforce 都创建 reservation（台账语义一致）；enforcement 只影响准入 gate。
- 价格不进 reservation：reservation 冻结 turn-level policy + aggregate amount；
  price snapshot 属于每个 Run 的 immutable input，cost spend 的 price_digest 回指该 Run。

### 4. 准入 gate 位置

- Coordinator Run 创建前（startCoordinatorTurn）：turn_count（WP4-A 已有）+
  每个启用的 usage kind 做 ShouldRun 预检（used = committed + active reserved，
  remaining < 1 即 deny）。enforce deny → 不创建 Run，Coordinator 走既有
  blockCoordinatorForStartFailure。
- Worker/eval/heal Run 创建时（createRunLocked / createRetryRunLocked）：
  先 ensure 本 Turn reservation；enforce 下 `reservation.ReservedAmount == 0`
  （admission 时预算已尽）→ quota_denied，不创建 Run。audit 只记录 would_deny。
- cost 价格 fail-closed：Goal 启用 cost policy（无论 audit/enforce，目标合同原文
  「必须 fail-closed 为 cost_price_unavailable」）且本 Run 解析出的 model 无价格快照 →
  拒绝创建（含 Coordinator source Run），错误码 cost_price_unavailable。
  这保证 cost 结算时 Run 必有价格，0027 的 price_digest 非空约束恒可满足。

### 5. settlement sweep 与 phase6 关闭条件

`maybeSettleGovernanceTurnQuota`（终态钩子链中位于 maybeAdvanceTaskCoordinator 之后、
maybeSettleDispatch 之前；admission/phase5 后经 appendQuotaPhaseIfReady 触发同一 sweep）：

- 受管 Run 集合 = phase1 payload 的 source_run_id + `ListByGovernanceTurn`
  （execution_runs.input.governance 三元组 JSON 查询，天然覆盖 retry/heal/eval Run）。
- 每个 terminal 受管 Run 逐 usage kind append spend（幂等键 (turn,kind,run)）：
  canonical 对应桶 resolved → committed（amount == canonical 值）；unresolved →
  amount=0 + reason。actual 超出 reservation 剩余容量时写 unresolved
  （reason 带 actual/capacity），不裁剪事实、不伪造 0 消耗——真实用量永远在
  Run 的 canonical usage 上。repo 层带对偶强制：resolved 且容量充足却报
  unresolved 会被「能落必须落」拒绝；committed 超额仍被容量不变式拒绝。
- failed/cancelled/interrupted/lost Run 同样按实际 usage 结算。
- reservation 结算与 phase6 追加的关闭条件（全部满足才执行，且在同一事务）：
  1. 所有受管 Run 终态且逐 kind 有 spend entry；
  2. phase5（plan_dispatch）已存在，或 Todo 已在本 Turn 上 blocked/cancelled/completed
     （编译/权限失败无 dispatch 的 Turn 用 source Run 单独关闭）；
  3. Coordinator state 不存在指向本 Turn Run 的 pending worker retry
     （retry checkpoint 先行创建于 sweep 之前，堵住 retry Run 晚到打到已结算
     reservation 的洞）。
- 关闭动作：reservation committed=Σ committed spend、released=剩余，CAS 转 committed
  （committed=0 则 released），随后追加 phase6（quota_spend）。
  phase6 payload 由最终台账状态确定性重算（不含时间戳），重放同 identity 同 digest
  幂等；不重写、不留占位。
- 进程内并发：sweep 持 per-turn 锁（governancePlanLocks 按 TurnKey 取模）+ SQLite
  单写事务 + spend 幂等键 + reservation CAS，并发 sweep 恰有一个完成关闭。
- quota 事件不新增 live 类型：结算证据经 phase6 的 `turn.receipt_appended`
  同事务出 outbox；proposed 的 quota 事件留待 WP6 read model 落地时同刀提升。

### 6. 自愈/重试的治理身份补齐

`maybeSelfHeal` 的 heal Run 原先经 `CreateRun` 重建且不携带 input.governance，
会逃出 usage 结算。现传播父 Run 的 governance 上下文（与 createRetryRunLocked
克隆语义一致），heal Run 进入同一 Turn 的受管集合、同一 reservation 与 active_worker gate。

## 放弃了什么

- **reservation 超额时裁剪或拆分 spend entry**：破坏「spend amount == canonical 值」
  与 append-only 键的唯一性；改用 unresolved + reason 记录缺口。
- **为 quota spend/reservation 新增 live SSE 事件类型**：WP6 前无 read model 消费者，
  提前进 live 白名单会让协议宣称超过代码事实；receipt 事件已承担同事务 outbox。
- **~~runner v2 协议扩展以透传 ProviderUsageReport~~**（2026-09-02 复审后撤销该放弃项）：
  远程 Run 缺 `agent_profile_id` 时连 report 都构造不了，落 absent 是真缺口而非可接受边界，
  已转入裁决 3 实施。
- **对 legacy（升级前）终态 Run 回填 canonical usage**：0028 明确 NULL/NULL 为
  legacy 态；回填无法证明当时的 report/anchor 序列。

## 复审裁决（2026-09-02 外部评审后修订）

外部只读评审提出 5 个 P1，逐条取证后全部属实，裁决如下：

1. **unresolved 必须进入准入判定**：R4 原文「无法证明 delta 时…只阻止启用了受影响
   quota kind 的 Goal 继续创建新 Run」。`ShouldRunLocked` 与 worker 创建闸除
   committed+active reserved 外，还须查询 `ListUnresolved(goal, kind)`：存在缺口即
   WouldDeny（audit 记录、enforce 拒绝），缺口永不自动清除（人工对账前不放行）。
   超容豁免 entry 场景下预算本已耗尽，不造成额外阻塞。
2. **phase6 关闭必须等 Plan 无 pending 步**：approval_policy=manual 的 dispatch 步在
   审批前不建 Run（step pending、无 ResultRunID），phase5 已落。sweep 关闭条件追加
   「本 Turn Plan 无 status=pending 的步骤」，否则审批后创建的 Worker 会撞上已关闭的
   reservation。
3. **Runner v2 wire 透传 ProviderUsageReport**：run_spec 增 `agent_profile_id`（否则
   runnerd 侧无法构造绑定身份的 report），usage.updated 帧增可选 `provider_report`
   （sealed ProviderUsageReportV1 JSON）；控制面解析后走既有 recordRunUsageTx 绑定
   （身份/digest 校验在 bindProviderUsageReport）。线格式 malformed 即 poison，不静默丢。
4. **absent canonical 延迟到关闭时刻**：终态钩子与 sweep 的进行式 pass 都不再合成
   absent canonical（force 语义改为 allowAbsent）；无 report 的受管 Run 在关闭前保持
   canonical 缺失，迟到 report 随时可正常落库（canonical==nil → 首写）。
   仅在关闭性触发源（StartCoordinator/admission/Todo 收口，allowAbsentClose=true）
   且其余关闭条件齐备时才合成 absent canonical 并落 unresolved entry。
   关闭后 report 才到 = 越过结算边界，拒绝改写（文档化）。
   不改 0028 不可变 trigger、不开升级旁路——延迟即答案。
5. **DSH 未知 cache-write 不得按 0 计**：`dshUsageBuckets.counters()` 只有在
   uncached/read/write 三者全知时才派生 input_tokens_total；domain
   `UsageCountersV1.Validate` 追加部分矛盾守卫：total 已知时不得小于任一已知
   输入分量之和（total ≥ uncached+read+write 的已知子集和）。

当时暂缓的 P2 清单（历史快照）包括：直接 SQL 写 spend 的 amount 未与
canonical 桶绑定、非终态 Run 可被直接 SQL 写 canonical、anchor 身份不兼容后的
水位推进策略、registry 价格与模型一致性校验、OpenAPI/AsyncAPI 治理 DTO 对齐，
以及分支分刀整理。前三项和合同/registry 项已在下述复审收口中由
0030/0035、anchor guard、registry 校验与 live contract 修复；分刀提交仍遵循
“用户未授权不提交”的会话边界，不属于运行时功能缺口。

## 复审裁决（2026-09-02 第二轮）

第二轮复审再提 4 项，取证后全部属实：

1. **remote terminal→late usage 例外**：runnerd 设计上允许终态后补发 usage 帧。
   Gateway 仅对当前连接/epoch/runner 且已摘除 active Run 的 `usage.updated` 放行到
   Application；Application 再要求 lease 已释放、lease/run/runner/fencing 全匹配且
   Run 已终态。dedup 键不变，重放幂等；status/session/message 不共享该例外。
2. **anchor 按 kind 合并水位**：身份兼容时，每个累计分量独立推进；report 已知且
   不低于旧值则采用新水位，回退/缺失则保留旧水位。健康 kind 当前 Run 正常结算且
   下一 Run 不重复计费；回退 kind unresolved。anchor 是跨观测 per-kind watermark，
   可暂时不满足同一报告的 input 守恒；ProviderUsageReport/CanonicalUsage 仍严格守恒。
   身份不兼容/invalidated 不推进；`anchor==nil` 仍建立最近观测水位。
3. **审批拒绝的治理收口**：approval 决定提交后，以 per-turn lock + 单一 SQLite 事务
   完成 quota sweep、Todo waiting→blocked 与 Coordinator
   blocked(plan_dispatch_rejected)。任一步失败整体回滚并上返；重复相同拒绝仍重建
   Plan/Turn 身份并补齐收口，phase6/spend/state 保持幂等。人否决路线不自动 replan。
4. **DSH legacy 数值与累计 fail-closed**：单值仅接受非负有限整数；legacy 三列使用
   独立 checked accumulator，不再直接 `+`/`+=`。非法分量跳过，任一维聚合溢出后
   该维固定为 0 且不恢复，不污染其他维；canonical bucket 继续以 nil 表达 unknown。

最终收口已落 0030 durable source/plan/decision checkpoint。永久 Plan/Run 拒绝在 phase3 写入 rejection，与 source usage spend、reservation 关闭和 Todo blocked 同事务提交；数据库/事务故障不被当成业务拒绝，而是保留 `plan_commit` retry checkpoint 并由 terminal replay 继续同一 Turn。

0030 的 SQL trigger 已将 spend amount 绑到 Run canonical bucket；CanonicalUsage OpenAPI DTO 与 Go 嵌套结构已对齐；quota 事件已升为 live AsyncAPI producer。0033 另增不可变 `QuotaGapResolution`：人工对账必须绑定 passed/accepted 权威 Evidence，v1 只允许 `reconciled`、不提供 waiver；原 unresolved spend、Run canonical usage 和 reservation 永不改写，reconciled amount 继续参与 ShouldRun。

## 关联

- [loopx-native-governance.md](2026-09-01-loopx-native-governance.md)
- [目标与需求合同](../../../docs/product/loopx-native-governance-goal.md)
- [实施计划](../../../docs/architecture/loopx-native-governance-implementation-plan.md)
