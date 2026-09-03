# Run Journal：全生命周期环节日志设计（参考 DSH turn 日志）

> 状态：**M1 已立项**（2026-09-03 用户拍板 D1–D4，实施分支 codex/run-journal-m1）
> 日期：2026-09-02
> 复核：2026-09-03 已对 LoopX 合入后的 main（ca0ab59，+61k 行治理全栈）做增量复核，修订各处；修订点以【2026-09-03】标注。
> 动机：参考 deepseek-harness 的 turn 日志（事件溯源 + 唯一写入口 + 边界埋点 + 崩溃闭合），让 ATW 的每个 run 在**每一个环节**都有可查询的日志锚点，故障定位 = 查日志，而不是猜。

## 1. DSH 模式四条，与 ATW 现状对账

| DSH 模式 | ATW 现状 | 结论 |
|---|---|---|
| 唯一写入口 `Session.append` | `Service.emit` → `EventRepo.Append` 已是事实单点；进程内/远程两路径复用同一组 `*Tx` 核心（`internal/application/runner_events.go:196-289`）。【2026-09-03 复核：不变，`service.go:89` 仍是唯一 `Events().Append` 调用点，emit 调用方约 86 处；`EventRepo.Append` 仍是 run_events+stream_events+outbox 固定三写】 | **保留**，缺的是把绕过事件语义的写点收敛进来 |
| 生命周期边界手动埋点 | 状态边界有事件（run.created/started/status_changed/终态）；coordinator 有因果事件族。【2026-09-03 复核：治理层新增 goal.*/todo.*/turn.receipt_appended/handoff.*/quota.* 等完整事件族，但全部作用于 goal/todo/turn 粒度，run 环节粒度无新增】 | **环节边界仍是黑盒**：dispatch→starting→首回调之间、lease 生命周期、adapter 原始输出（`module.go:344-346` OnLog 仍原样丢弃，注释"M5 日志页接入"还在） |
| write-behind + fail-closed flush 屏障 | 全同步事务写，提交即真相（SQLite 本地） | **不照搬**。DSH 的屏障问题在 ATW 不存在——事务边界天然是屏障。教训反向成立：**不要引入异步写入** |
| 崩溃合成闭合（interruptedTurnClosers） | 三条死 runner 恢复路径都补终态（`gateway.go:345-615`）；"Outcome 必落终态"有兜底（`module.go:222-230`）。【2026-09-03 复核：恢复路径原样，合成终态仍只带 code/message/retryable；治理侧新增 0030 恢复 checkpoint 与 admission proof（审计 §3.25 已闭 commit→dispatch 崩溃窗口），但那是治理回合粒度】 | **部分有**，缺：合成事件不带证据、**非受管** host_local run 的控制面重启无对账 |

结论：ATW 不需要移植 DSH 的架构，需要移植的是它的**埋点纪律**——"环节的每个边界都必须有事件，未闭合即故障点"。【2026-09-03 增补：LoopX 治理合并本身就是这个纪律在仓库里的既成先例——turn_receipt 用不可变触发器强制七阶段 contiguous（`domain/turn_receipt.go:12-29`），只不过它的粒度是治理回合（goal/todo/turn_seq），run 生命周期仍是空白。本设计实质是把同一纪律下沉到 run 维度。】

## 2. 设计目标与原则

目标：给一个 run_id，能用一条查询回答三个问题——**跑到哪个环节了、哪个环节出的问题、当时拿到了什么证据**。

原则：

1. **事件即真相，日志只是投影**。环节信息先进 `run_events`（append-only），结构化日志（slog）是事件的旁路输出，不允许反过来。
2. **同步事务写，不引入 write-behind**。SQLite 单机，提交即持久；高频流事件（message.delta）已在同事务体系内，不为此改分层。
3. **埋点收敛在边界，不散落**。所有环节事件经一个 `RunJournal` 薄封装发出，禁止各调用点手搓 data 结构。
4. **internal 与 surface 分层**。环节日志不进 SSE、不进对话回放（session 回放保真语义不变），只进调试查询面。
5. **不破坏硬约束**。13 态状态机权威不变、ModuleRunner 唯一推进点不变、墓碑语义不变、远程/进程内两路径同构不变；治理 receipt 链（不可变 + digest）不做任何写路径改道，只做引用。

## 3. 核心设计

### 3.1 环节（phase）模型：一个 run 的七段链

把 run 从创建到落账切成显式环节，每个环节有一对事件：

- `run.phase_entered` `{phase, attempt, detail?}` — 进入环节
- `run.phase_closed` `{phase, outcome: ok|failed|skipped, failure?{code,message,family}, duration_ms, detail?}` — 离开环节

**定位规则**（与 DSH "turn 永远闭合"同构）：

- 最后一个 `phase_closed.outcome=failed` 的 phase = 故障环节；
- 有 `phase_entered` 无配对 `phase_closed` = 崩溃/卡死环节；
- 七段全 ok = 正常 run，无需看日志。

phase 枚举（初版，按现网真实边界切）：

| # | phase | 覆盖区间（现有代码锚点） | 当前是否黑盒 |
|---|---|---|---|
| 1 | `dispatch` | 入队/选 runner/lease 授予（`cmd/control-plane/main.go:382-412`、`internal/runnergateway/ingress.go:29-113`）。【2026-09-03：治理侧已有 receipt phase5(dispatch) + admission proof，但那是 goal/todo 粒度；run 维度的 host 路由与 lease 授予仍无事件】 | **半黑盒** |
| 2 | `spawn` | 子进程拉起、pid/pgid 落账（adapter Execute 内 OnSpawn，如 `codexapp.go:126-157`） | **黑盒** |
| 3 | `handshake` | initialize 握手 + thread start/resume 探测（`codexapp.go:521-625`）；resume 失败分类 session_unknown 在此闭环 | **黑盒** |
| 4 | `first_event` | 等待首个回调（`module.go:316-367` markRunning） | **黑盒** |
| 5 | `streaming` | 主对话流：message/tool/approval/plan 事件（已有契约，不重埋） | 已有 |
| 6 | `settle` | 终态裁决 composeResult + transitionRunLocked + usage/session 落账（`codexapp.go:1295-1329`、`module.go:170-231`）。【2026-09-03：新增 0035 触发器强制 canonical usage 只能随终态写入，裁决面更硬，但裁决**过程**仍无事件】 | 半黑盒 |
| 7 | `post` | 终态钩子管线（`runs.go:1470-1505`）。【2026-09-03：钩子从 6 个增至 **8 个**——maybeAdvancePlans/maybeProcessVerdict/maybeExtractPlan/maybeSummarizeSegment/maybeCanonicalizeRunUsage/maybeAdvanceTaskCoordinator/maybeSettleGovernanceTurnQuota/maybeSettleDispatch，且有显式顺序契约（canonical 必须先于 Coordinator 决策，quota sweep 必须在推进后）；全部"尽力而为"，失败仍无痕——缺口比初版设计时**更大**】 | **黑盒** |

`streaming` 内部不重埋——message.*/tool.* 已是环节内日志；phase 事件只记它的进入与退出。

### 3.2 唯一写入口：RunJournal 落在 observability 包

`internal/observability/` 目前只有 governance_metrics（治理计数器重算），没有日志设施——本设计让它名实相符：

```go
// internal/observability/journal.go（示意，非最终实现）
type RunJournal struct{ sink runtime.EngineSink } // 复用 emit 单点，不另开写库路径

func (j *RunJournal) EnterPhase(ctx, runID, phase string, attempt int, detail map[string]any)
func (j *RunJournal) ClosePhase(ctx, runID, phase string, outcome PhaseOutcome, failure *Failure, dur time.Duration, detail map[string]any)
func (j *RunJournal) LogChunk(ctx, runID string, stream LogStream, chunk string) // OnLog 收口
func (j *RunJournal) Decision(ctx, runID, kind, reason string, inputs map[string]any) // 因果事件
```

- 内部全部走 `Service.emit` → `EventRepo.Append` 既有单点，**不新增第二条写库路径**。
- `attempt` 支持 retry/自愈重跑同一 phase（第二次 handshake 是 attempt=2），与 `retry_of` 链互补。
- `duration_ms` 让"哪个环节慢"零成本可查（性能定位是故障定位的副产品）。

### 3.3 可见性分层：internal 事件不进 SSE、不进对话回放

新增事件类型按契约流程走（`domain/events.go` 常量 + 白名单 → `contracts/events/asyncapi.yaml` enum → 门禁 B 双向对账），并在契约里标记 `x-internal: true`：

- `EventRepo.Append` 处按类型分流：**internal 类型只落 `run_events`，跳过 `stream_events` + outbox**（SSE 带宽与前端契约零影响）。
- 回放 API（`ListRunEvents`）默认只返 surface；加 `?visibility=all` 给调试面用。
- 回放保真测试补一条断言：internal 事件不改变对话投影（钉死"日志不污染回放"）。

零 schema 迁移：分流依据是事件类型（代码内白名单），不动表结构。

### 3.4 埋点地图（每个黑盒的精确接缝）

| 埋点 | 位置 | 事件 |
|---|---|---|
| dispatch 入队/路由 | `main.go:382-412` Dispatcher；远程侧 `ingress.go:29-113` lease 授予 | `phase_entered/closed{dispatch}`，detail 带 host_id/runner_id/lease_id/fencing_token |
| spawn | 各 adapter `OnSpawn` 回调处（pid/pgid 已有） | `phase_closed{spawn}` detail 带 pid/pgid/bin 路径摘要 |
| handshake | codexapp pump 前段（initialize :526-580、thread start/resume :593-625）；kimi `-S` resume（`kimi.go:117-126`） | resume 尝试结果进 detail；session_unknown 分类在 `phase_closed{handshake, failed}` 携带——自愈决策的输入证据从此可查 |
| first_event | `markRunning` 前（`module.go:316-367`） | 超时可由查询侧定义（entered 无 closed + 现在-entered_at > 阈值），M1 不加定时器 |
| settle | `composeResult`（`codexapp.go:1295-1329`）裁决三分支（意图/turn completed/流中断） | detail 带裁决依据分支名 |
| post | `RecordRunStatus` 提交后钩子管线（`runs.go:1003-1017`）逐钩子 | 每个钩子一条 closed，失败钩子带 failure——目前钩子失败完全无痕 |
| OnLog 收口 | `module.go:339-341`（当前丢弃点）+ 各 adapter stderr 读取处 | `run.log_chunk`（internal，data{stream, chunk, truncated}），每 run 64KB 环形截断 |

远程 runner 路径的 dispatch/settle/post 由网关/控制面直接可见，M1 全覆盖；**runner 进程内的 spawn/handshake/first_event 需要 runner 协议 v2 增加 phase 帧**——列为决策点 D2，M2 再做。

### 3.5 崩溃闭合与重启对账

已有三条死 runner 路径继续负责 run 终态；本设计补两块：

1. **合成闭合带证据**。recovery 路径在 transitionRunLocked 之外补一条 `phase_closed{phase=当前中断环节, outcome=failed, failure{code}, detail={last_heartbeat_at, lease_expired_at, fencing_token, boot_id}}`——区分"runner 真死"与"网络抖动误杀"从此有数据。【2026-09-03：契约层已预留现成槽位——`run.recovery_started/completed/failed` 三个事件在 asyncapi.yaml:251-253 已声明、Go 常量（`events.go:115-117`）已定义，但**全仓库零发出点**。M2 直接激活这三个事件承载恢复证据，不需要新增事件名；`domain/events.go` 白名单只改内部标记】
2. **控制面重启对账 sweeper**。【2026-09-03 范围收窄：受管 run 的 commit→dispatch 崩溃窗口已被 admission proof + 0030 checkpoint 闭环（审计 §3.25）；剩余缺口是**非受管** host_local run——boot 时扫 `execution_runs` 中非终态且属 host_local 的 run，合成 `phase_closed{failed, control_plane_restart}` + 走既有 lost/failed 语义（lost 恒 retryable，重驱由 coordinator due-state 循环接住）。与 leaseSweeper 分工不变：它管远程 lease，这个管进程内 run】

### 3.6 决策因果链

- 【2026-09-03 范围收窄：治理层决策留痕**已实现**——六类 TurnDecision 落进不可变 Receipt phase 1（`governance_turn_decisions.go`），不再是本设计的负担】`journal.Decision` 只收口治理域**之外**的"为什么"：session_unknown 自愈重试（`maybeSelfHeal`）、非受管 coordinator 重驱、取消前转。data 带 reason + 关键输入摘要（session_ref、failure_code、attempt）。
- `correlation_id`（CanonicalEvent 已有字段，`events.go:179-194`）串起：原 run 的失败 → decision → 新 run 的 phase 链。跨 run 的完整因果可单查询展开；受管 run 再通过 `run_ids[]` 挂上治理 receipt，两条链互链。
- 三处直接调 `RecordRunStatus` 的兜底点（`main.go:406`、`coordinator_engine.go:244/391`、`gateway.go:357-374`）逐步改为经 Journal 带 reason 调用，消灭"裸状态写"。

### 3.7 与治理 turn_receipt 的关系【2026-09-03 重写：已从"未来对接"变为"已落地对齐"】

LoopX 治理已合入 main（ca0ab59，迁移 0024–0042）。**turn_receipt 是治理回合（goal/todo/turn_seq 粒度）的结算证据链**：七阶段固定为 decision_decode → validation → durable_writeback → plan_compile → dispatch → quota_spend → projection_outbox（`domain/turn_receipt.go:12-29`），不可变 + JCS digest + 触发器强制 contiguous。**本设计是 run 生命周期的过程日志**。两者粒度不同、对象不同、互补不对抗：

- phase 事件回答"这个 run 的环节发生了什么"，receipt 回答"这个治理回合结算了什么"。
- 天然对接点已存在：`turn_receipt_phases.run_ids[]` ↔ `run_events.run_id`——从治理 receipt 的 dispatch 阶段可反查 run 的七段 phase 链，从 run 的 journal 可上溯所属治理回合。
- 治理链的故障定位已自给（receipt digest 链 + replay checkpoint）；本设计补的是它够不着的 run 内部（spawn/handshake/first_event）与非受管 run。
- M3 只做查询面打通（journal API 响应里带 receipt 引用），不写路径不做任何改道。

## 4. 故障定位 playbook（设计验收的场景尺）

| 症状 | 查询形态 | 定位 |
|---|---|---|
| run 卡 starting | 最后事件=`phase_entered{dispatch}` 无 closed | 看 detail 的 host 路由：远程→查 runner 在线/lease；本地→查 ModuleRunner；受管 run 另可查 receipt phase5(dispatch) 是否落账 |
| run 卡 starting（本地） | `phase_closed{dispatch}` ok，`phase_entered{spawn}` 无 closed | 二进制缺失/权限——`run.log_chunk` 有 stderr |
| resume 后失败 | `phase_closed{handshake, failed}` failure.code=session_unknown | 看 decision 事件是否触发自愈、新 run attempt=2 |
| 起完就挂 | `first_event` entered 无 closed | runtime 握手成功但不出帧——看 log_chunk + 该 runtime 的 SchemaDigest |
| 工具/审批后断 | streaming 内事件链 + `phase_closed{settle}` detail 的裁决分支 | 区分对端流中断 vs 意图取消 |
| 状态对了但没推进 | `phase_closed{post}` 某钩子 failed | 具体钩子名 + failure——8 个钩子的顺序契约（canonical→coordinator→quota sweep）使"哪一环断"直接可读 |
| runner 掉线判死 | recovery 合成 closed 的 detail（`run.recovery_*` 事件） | last_heartbeat 与 lease_expired 时间差 < 阈值 = 疑似误杀 |
| 治理回合卡住 | receipt 缺哪个 phase_seq / digest 校验失败 | **已有能力**（receipt 不可变链 + replay checkpoint），本设计不重建 |

配套查询面（M3）：`GET /api/runs/{id}/journal?visibility=all` 返回按 occurred_at 排序的环节时间线；前端新开 run 环节时间线视图消费它。【2026-09-03 澄清：现有 `logs.page.tsx` 是"活动登记簿"（activities 表 + SSE 前插的任务/审批动态），**不是** run 环节日志；`module.go:344` 注释预留的"M5 日志页"仍是新面】

## 5. 分期落地【2026-09-03 复核后修订】

**M1（黑盒清零，host_local 全覆盖）** — 全部仍然成立，post 段优先级上调
- Journal 薄封装 + 新事件类型过契约门禁（新 main 上门禁已扩到 6 个，含 governance 词表对账，新增事件走同流程）；
- dispatch/spawn/handshake/first_event/settle/post 六处埋点（本地路径）；post 段 8 个钩子逐一带名留痕——这是 LoopX 合并后**风险面最大**的黑盒，建议提到 M1 首位；
- OnLog 收口为 `run.log_chunk`；
- internal 分流（不落 stream_events/outbox，分流点在 `EventRepo.Append` 固定三写处按类型短路）+ 回放默认过滤；
- 验收：scripted adapter 构造每个环节各失败一次，断言最后一个 phase 事件指向正确环节；回放保真测试全绿。

**M2（闭合与因果）** — 范围收窄，成本下降
- 激活契约已预留的 `run.recovery_started/completed/failed` 三事件承载恢复证据（零新事件名）；
- 非受管 host_local run 的控制面重启对账 sweeper（受管路径已被 admission proof + 0030 checkpoint 覆盖，不重复做）；
- Decision 事件只收口非治理决策（自愈/取消前转/普通 coordinator 重驱）；治理决策已由 receipt phase1 承担；
- 决策点 D2（runner 协议 phase 帧）若通过，远程 runner 内环节埋点——可与审计 §7 的 Remote Runner 真实验收 gate 同期做，共享测试环境；
- 验收：kill -9 控制面进程后重启，非受管 host_local 非终态 run 全部有合成闭合且可重驱。

**M3（查询面与对接）** — 对接从"条件分支"变"直接做"
- journal 调试 API + 前端 run 环节时间线视图（区别于现有活动登记簿 logs 页）；
- receipt ↔ journal 互链查询（按 run_id 反查所属治理回合与 receipt digest）；
- slog 结构化旁路（handler 从 Journal 派生，替换关键路径的 log.Printf——gateway 恢复路径的 `log.Printf("...标记 lost 失败...")` 类裸日志优先）。

## 6. 决策点【2026-09-03 已全部拍板】

- **D1 事件粒度 = 成对**：`phase_entered`/`phase_closed` 成对，未闭合=故障点的语义是构造性的，与治理 receipt 的 contiguous phase 纪律同构。
- **D2 远程 runner 环节帧 = 加协议帧**：runner 协议 v2 增加 phase 帧，远程 runner 内环节（spawn/handshake/first_event）由 runner 侧上报；与审计 §7 的 Remote Runner 真实验收 gate 同期做（M2）。
- **D3 log_chunk 上限 = 64KB/run 环形截断**：超出标记 truncated，防表膨胀。
- **D4 slog 替换范围 = 只换关键环节**：gateway 恢复失败、钩子失败等"出了事才看得到"的点优先；不追全量 130+ 处。
