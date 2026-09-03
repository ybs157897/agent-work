# Run Journal：全生命周期环节日志设计（参考 DSH turn 日志）

> 状态：proposed（未立项）
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

结论：ATW 不需要移植 DSH 的架构，需要移植的是它的**埋点纪律**——"环节的每个边界都必须有事件，未闭合即故障点"。

## 2. 设计目标与原则

目标：给一个 run_id，能用一条查询回答三个问题——**跑到哪个环节了、哪个环节出的问题、当时拿到了什么证据**。

原则：

1. **事件即真相，日志只是投影**。环节信息先进 `run_events`（append-only），结构化日志（slog）是事件的旁路输出，不允许反过来。
2. **同步事务写，不引入 write-behind**。SQLite 单机，提交即持久；高频流事件（message.delta）已在同事务体系内，不为此改分层。
3. **埋点收敛在边界，不散落**。所有环节事件经一个 `RunJournal` 薄封装发出，禁止各调用点手搓 data 结构。
4. **internal 与 surface 分层**。环节日志不进 SSE、不进对话回放（session 回放保真语义不变），只进调试查询面。
5. **不破坏硬约束**。13 态状态机权威不变、ModuleRunner 唯一推进点不变、墓碑语义不变、远程/进程内两路径同构不变。

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
| 1 | `dispatch` | 入队/选 runner/lease 授予（`cmd/control-plane/main.go:382-412`、`internal/runnergateway/ingress.go:29-113`） | **黑盒** |
| 2 | `spawn` | 子进程拉起、pid/pgid 落账（adapter Execute 内 OnSpawn，如 `codexapp.go:126-157`） | **黑盒** |
| 3 | `handshake` | initialize 握手 + thread start/resume 探测（`codexapp.go:521-625`）；resume 失败分类 session_unknown 在此闭环 | **黑盒** |
| 4 | `first_event` | 等待首个回调（`module.go:316-367` markRunning） | **黑盒** |
| 5 | `streaming` | 主对话流：message/tool/approval/plan 事件（已有契约，不重埋） | 已有 |
| 6 | `settle` | 终态裁决 composeResult + transitionRunLocked + usage/session 落账（`codexapp.go:1295-1329`、`module.go:170-231`） | 半黑盒（有终态事件，无裁决过程） |
| 7 | `post` | 终态钩子管线：maybeAdvancePlans/ProcessVerdict/ExtractPlan/SummarizeSegment/AdvanceTaskCoordinator/SettleDispatch（`runs.go:1003-1017`） | **黑盒** |

`streaming` 内部不重埋——message.*/tool.* 已是环节内日志；phase 事件只记它的进入与退出。

### 3.2 唯一写入口：RunJournal 落在 observability 包

`internal/observability/doc.go` 目前是 "M2+ 交付" 占位——正好由本设计兑现：

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

1. **合成闭合带证据**。recovery 路径在 transitionRunLocked 之外补一条 `phase_closed{phase=当前中断环节, outcome=failed, failure{code}, detail={last_heartbeat_at, lease_expired_at, fencing_token, boot_id}}`——区分"runner 真死"与"网络抖动误杀"从此有数据。
2. **控制面重启对账 sweeper**。boot 时扫 `execution_runs` 中非终态且属 host_local 的 run（进程死=全体消失，目前无 sweeper）：合成 `phase_closed{failed, control_plane_restart}` + 走既有 lost/failed 语义（lost 恒 retryable，重驱由 coordinator due-state 循环接住）。与现有 leaseSweeper 分工：它管远程 lease，这个管进程内 run。

### 3.6 决策因果链

- `journal.Decision` 统一收口三类"为什么"：自愈重试（`maybeSelfHeal`，`sessions.go:483-528`）、coordinator 重驱（`coordinator_engine.go`）、取消前转。data 带 reason + 关键输入摘要（如 session_ref、failure_code、attempt）。
- `correlation_id`（CanonicalEvent 已有字段，`events.go:179-194`）串起：原 run 的失败 → decision → 新 run 的 phase 链。跨 run 的完整因果可单查询展开。
- 三处直接调 `RecordRunStatus` 的兜底点（`main.go:406`、`coordinator_engine.go:244/391`、`gateway.go:357-374`）逐步改为经 Journal 带 reason 调用，消灭"裸状态写"。

### 3.7 与 loopx turn_receipt 的关系（不对抗、不重复）

loopx worktree（`codex/research-loopx-task-foundation`）的 `turn_receipt_headers/phases` 是**结算层证据链**（七阶段、canonical_digest、触发器强制不可变）；本设计是**过程层日志**。分工：

- phase 事件回答"环节发生了什么"，receipt 回答"这个 turn 结算了什么"。
- 对接点：receipt phases 的 `evidence[]` 可引用 run_events 的 event_id；`run_ids[]` 已天然关联。
- M3 再做对接；M1/M2 各自独立可用，loopx 合入与否都不阻塞本设计。

## 4. 故障定位 playbook（设计验收的场景尺）

| 症状 | 查询形态 | 定位 |
|---|---|---|
| run 卡 starting | 最后事件=`phase_entered{dispatch}` 无 closed | 看 detail 的 host 路由：远程→查 runner 在线/lease；本地→查 ModuleRunner |
| run 卡 starting（本地） | `phase_closed{dispatch}` ok，`phase_entered{spawn}` 无 closed | 二进制缺失/权限——`run.log_chunk` 有 stderr |
| resume 后失败 | `phase_closed{handshake, failed}` failure.code=session_unknown | 看 decision 事件是否触发自愈、新 run attempt=2 |
| 起完就挂 | `first_event` entered 无 closed | runtime 握手成功但不出帧——看 log_chunk + 该 runtime 的 SchemaDigest |
| 工具/审批后断 | streaming 内事件链 + `phase_closed{settle}` detail 的裁决分支 | 区分对端流中断 vs 意图取消 |
| 状态对了但没推进 | `phase_closed{post}` 某钩子 failed | 具体钩子名 + failure，不再无痕 |
| runner 掉线判死 | recovery 合成 closed 的 detail | last_heartbeat 与 lease_expired 时间差 < 阈值 = 疑似误杀 |

配套查询面（M3）：`GET /api/runs/{id}/journal?visibility=all` 返回按 occurred_at 排序的环节时间线；前端日志页（`module.go:339-341` 注释里预留的 "M5 日志页"）消费它。

## 5. 分期落地

**M1（黑盒清零，host_local 全覆盖）**
- Journal 薄封装 + 四个新事件类型过契约门禁；
- dispatch/spawn/handshake/first_event/settle/post 六处埋点（本地路径）；
- OnLog 收口为 `run.log_chunk`；
- internal 分流（不落 stream_events/outbox）+ 回放默认过滤；
- 验收：scripted adapter 构造每个环节各失败一次，断言最后一个 phase 事件指向正确环节；回放保真测试全绿。

**M2（闭合与因果）**
- 恢复路径合成闭合带证据；控制面重启对账 sweeper；
- Decision 事件收口三类决策点；三处裸 RecordRunStatus 调用改造；
- 决策点 D2（runner 协议 phase 帧）若通过，远程 runner 内环节埋点；
- 验收：kill -9 控制面进程后重启，host_local 非终态 run 全部有合成闭合且可重驱。

**M3（查询面与对接）**
- journal 调试 API + 前端日志页；
- 与 loopx turn_receipt evidence 对接（若 loopx 已合入）；
- slog 结构化旁路（handler 从 Journal 派生，替换 130+ 处 log.Printf 的关键路径）。

## 6. 决策点（待拍板）

- **D1 事件粒度**：`phase_entered`/`phase_closed` 成对（推荐，未闭合=故障点的语义是构造性的）vs 单事件 `run.phase_changed`（省一半事件量，丢闭合语义）。
- **D2 远程 runner 环节帧**：runner 协议 v2 加 phase 帧（真实但动协议）vs 远程 run 只覆盖网关可见环节（M1 够用，runner 内部仍黑盒）。
- **D3 log_chunk 上限**：64KB/run 环形截断（推荐，防爆表）vs 全量（排障更全，SQLite 体积风险）。
- **D4 slog 替换范围**：只换关键环节 log.Printf（推荐）vs 全量 130+ 处（噪音大，收益递减）。

## 7. 防回归清单

- 新事件类型走既有契约门禁 B（事件名常量 ↔ asyncapi enum 双向对账），不加新门禁种类。
- 每个环节埋点配一条"该环节失败 → 最后 phase 事件正确"的集成断言（证据匹配表面）。
- 回放保真测试加 internal 过滤断言（钉死可见性分层）。
- usage/runs_count 幂等路径不经 Journal 改道（phase 事件是旁路观测，不进事务关键路径的幂等键）。
- 不引入异步落盘；若未来事件量成问题，优先在 `EventRepo.Append` 内做同事务批量，而不是 write-behind。
