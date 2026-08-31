# 任务控制面最小补全：实现计划

Status: implemented

Architecture: complete

Implementation: complete

Review: passed 2026-08-31

Baseline: `main@0366666b4ed41762fe93692ffef43a25307f5358`

Branch: `codex/task-control-surface`

Architecture source: `agent-team-workbench-docs/architecture/task-control-surface-context-design.md`

## 决策与理由

本项目不移植或兼容 dashi-taskboard，只原生补齐三个高价值缺口：

1. ExecutionHost / WorkspaceLocation / DevelopmentContext / 每 Run 不可变 ExecutionContextSnapshot。
2. append-only TaskComment 与系统 Task Coordinator 的 durable 消费水位。
3. Workspace selector、服务端 Review Queue 与确定性 Delivery Brief。

现有 `WorkItem → Plan → Dispatch → ExecutionRun → TaskSession`、13 态 Run、系统 Task Coordinator、Run 级执行锁、Outbox/SSE、Runner lease 与人工 Accept 保持权威。实现 Agent 不再负责架构设计；它必须按已完成 RFC 实现。

## 放弃了什么

- 不复制 Tauri、CDP、DOM、companion 或 Codex cron。
- 不新增 Dashi 的 backlog/in_review/done 状态。
- 不新增 Task→Session 一对一 binding。
- 不让 Web/Control Plane 下发远程宿主绝对路径。
- 不以 Prompt/Skill 代替状态机。
- 不新增平行 Task store、EventSource 或正文渲染树。
- 不做附件、依赖 DAG、通用 Automation、手工根 Task claim、Dashi sync。
- 不保留全局 WorkspaceRoot、Runner defaultWorkspace、workspace_alias=default 的永久双轨。
- Runner protocol 直接升级 v2，不实现 v1 运行时兼容。

## 复活条件

- 附件：三个真实 Task 因缺文件输入阻塞后再设计。
- 依赖 DAG：出现跨根 Task 真实依赖后再设计。
- Automation：现有 Scheduler/Wakeup 无法表达真实定时需求后再设计。
- backlog/manual claim：明确需要“发布但不执行”或根 Task 自主领取后再设计 Draft/Publish。
- MCP API 化：出现多用户、远程 MCP 或 MCP 写评论后实施。
- Dashi sync：明确要求使用现有 Dashi 数据时另开任务。

## 1. 开工前必读

1. `AGENTS.md`
2. `agent-team-workbench-docs/architecture/task-control-surface-context-design.md`
3. `agent-team-workbench-docs/end-goal.md`
4. `agent-team-workbench-docs/architecture/c4-container-diagram.md`
5. `agent-team-workbench/notes/implemented/architecture/2026-08-30-system-task-coordinator.md`
6. `agent-team-workbench/notes/implemented/architecture/2026-08-30-chat-task-record-isolation.md`
7. `agent-team-workbench/web/DESIGN.md`

Architecture RFC 是本任务的决策权威。若源码事实与 RFC 冲突：

- 先核对代码；
- 在本 note 记录冲突；
- 停止受影响实施线并交回架构评审；
- 不用“合理默认”自行改变 Host 路由、Snapshot、Session、Comment revision 或 Accept/Return 语义。

## 2. 不可违反的不变式

### 2.1 Execution Context

- 每个 Run 有且只有一个不可变 Snapshot。
- Snapshot 在 Run 可分派前持久化。
- Coordinator、Worker、evaluation、retry、recovery、Chat Run 全覆盖。
- retry/evaluation/session_unknown recovery 不切换 Snapshot。
- Dispatcher 只读 Snapshot，不重新读取当前 Location。
- remote Host 不可用时不跨 Host 或回退本机。
- Adapter 只读 `ExecContext.Resolved`，不读全局 WorkspaceRoot。
- Web/API/offer 不含用户可写远程绝对路径。
- Context identity 进入 TaskSession fingerprint/generation。
- 旧 Run session/clear 回调不能覆盖新 generation anchor。

### 2.2 Task Feedback

- TaskComment append-only。
- 根 Task comment revision 单调。
- note 不单独触发 Run。
- requirement/review_feedback 必须进入 durable Coordinator 控制线。
- review_feedback 只能由 ReturnWorkItem 写入。
- consumed watermark 与 Coordinator Run 创建同事务。
- 评论永远位于受限、不可信 JSON envelope。
- Accept、Return、actionable comment 竞态只有一方成功。

### 2.3 Review / Frontend

- Review Queue 不新增状态，以服务端 phase 投影为准。
- Delivery Brief 不调用 LLM。
- Workspace selector 同时只保留一个 EventSource。
- 异步请求与事件同时做 workspace ID + generation fencing。
- Task 正文继续复用 `AgentOutput`。

## 3. 当前基线红灯

以下红灯与本功能无关，但不能被掩盖：

```bash
cd agent-team-workbench
go test -count=1 ./internal/runtime/conformance -run '^TestConformanceKimiApp$' -v
```

失败：

- `TestConformanceKimiApp/StateMachineAuthority`
- `TestConformanceKimiApp/NoSideEffectsAfterTerminal`
- `TestConformanceKimiApp/CancelSemantics`

当前根 CI 只有后端、契约与 PostgreSQL migration；缺少 frontend 与 SQLite migration job。CI 收口独立成刀，不与领域功能混 commit。

## 4. 实施序列

同一 worktree 顺序完成 I0–I4。每个阶段先跑触面检查，全部完成后再进入 PR/全量窗口。用户没有要求 commit；默认不提交。

### I0. 契约与红测先行

目标：先让目标契约和防回归测试变红，再写实现。

修改：

- `contracts/web/openapi.yaml`
- `contracts/events/asyncapi.yaml`
- 新建 `contracts/runner/v2/schema.json` 并删除未再接线的 v1 contract
- contract guard/tests
- migration parity test 预期

契约：

- ExecutionHost/HostMount/WorkspaceLocation
- WorkItem DevelopmentContext
- Run ExecutionContextSnapshot
- Runner hello/offer/accept/reject/event fencing
- TaskComment list/create
- Return review_feedback
- Review Queue
- Delivery Brief
- 新错误码与 canonical events

必须先钉住：

- v1 Runner hello 被拒绝；
- run.offer 必须有 context snapshot；
- task_comment.created 事件字段；
- Review Queue 条件；
- API 不接受 absolute_path/cwd/root_path；
- Accept/Return/comment 竞态契约。

I0 验收：

- JSON/YAML 契约可解析；
- guard test 精确失败在尚未实现的字段/路由；
- 不修改生产实现凑绿。

### I1. Execution Context

#### I1.1 双迁移

建议迁移：

- `0021_execution_context.sql`
- `migrations/sqlite/0021_execution_context.sql`

新增：

- execution_hosts
- execution_host_mounts
- workspace_locations
- work_item_contexts
- execution_context_snapshots

修改：

- runners 增加 host/epoch，删除 workspace ownership 语义；
- execution_runs 增加 context_snapshot_id；
- task_sessions 增加 context_snapshot_id/context_generation/last_run_id。

迁移验收：

- 全新数据库；
- 0020 升级；
- 单 Workspace local bootstrap；
- 多 Workspace 不猜路径；
- terminal legacy Run 可审计；
- nonterminal legacy Run fail closed 后由 Coordinator 恢复；
- 双方言索引/CHECK/trigger 等价。

#### I1.2 Domain / Repository / Resolver

新增建议文件：

- `internal/domain/execution_context.go`
- `internal/application/execution_context.go`
- `internal/persistence/sqlstore/execution_contexts.go`

实现：

- Host/Runner/Location/Context/Snapshot 类型与验证。
- root/user child context 继承；Plan 创建时冻结 source snapshot，Plan Worker 不重读当前 root context。
- mount_generation 进入 Location/Snapshot/digest，防 alias 重指向。
- 同 Location 内 branch/worktree override。
- 非终态 Run 下 context 修改拒绝。
- Host-local mount registry 与 opaque worktree ref resolver。
- snapshot digest。

#### I1.3 Run 原子冻结

在 `createRunLocked` 内：

```text
resolve context
create immutable snapshot
resolve runtime/model/capabilities/session
create Run/events/outbox
```

覆盖所有 Run 来源：

- 首轮/反馈/恢复 Coordinator；
- Plan Worker；
- Worker retry；
- evaluation；
- session_unknown 自愈；
- Chat。

static context 失败整事务回滚；不能留下 queued Run。

#### I1.4 Host-aware Dispatcher / Runner v2

修改：

- `cmd/control-plane/main.go`
- `internal/runnergateway`
- `cmd/runnerd`
- Runner schema/test

实现：

- stable ExecutionHost、受保护 enrollment 与 host-bound Runner credential；
- Runner connection epoch 只做 transport fencing；
- hello mounts advertisement；
- exact host + adapter + capacity 选择；
- Runner pre-accept alias/repo/ref resolve；
- run.reject(reason=workspace)；
- 全部 v2 envelope 使用 v=2；
- accept/reject/event/command 全量 lease/fence；
- stable event_id/producer_seq 重连原样重放，dedup 不含 connection_epoch；
- 新增单事务 ApplyRunnerEvent：lease/fence 校验、dedup、Run/Session/Artifact 应用、canonical event/outbox；
- 只在 ApplyRunnerEvent commit 后按 Run/lease ACK，瞬态失败不 ACK；
- ACK 回显 run/lease/runner/fence/producer_seq，禁止全 Runner 水位误删其他 Run pending；
- reject 原子释放 lease、清 active run、落 retryable failed；
- RFC8785 canonical JSON snapshot digest；
- 第一版每 Host 单活跃 Runner，slots 表示并发容量；
- 旧连接迟到帧 ACK 但不落库；
- 禁止 remote→local 或跨 Host fallback。

#### I1.5 Runtime SPI / Adapters

`ExecContext` 增加：

- serializable ExecutionContextSnapshot；
- Host-resolved trusted context。

改造全部 Adapter：

- codexapp
- kimiapp
- kimi CLI
- dsh
- claudecode
- scripted/mock/conformance fixtures

同一阶段删除运行期 `Config.WorkspaceRoot` 读取，不留双轨。

#### I1.6 TaskSession

实现：

- context digest 进入 fingerprint；
- context generation/current anchor run/anchor run sequence CAS；
- 旧 Run late session/clear 不覆盖新 anchor；
- retry/evaluation 保留原 generation。

I1 验收：

- 两 Workspace 不同 root；
- 同 Workspace 本机/远程不同 Host；
- 两 Host 同 Adapter 只选绑定 Host；
- hello Host 与 enrollment credential 不匹配被拒绝；
- mount generation 变化使 Location fail closed；
- 重连 pending 事件保持稳定 identity、不重复落库；
- branch/worktree/canonical path；
- branch 必须绑定唯一 Host-discovered checkout ref，不执行 git checkout；
- 同 checkout 有执行 lease，不同 worktree refs 才可并行；
- symlink / .. / 删除 worktree 均拒绝；
- Plan source snapshot 与所有 Run 类型 Snapshot；
- Context 变化 fresh；
- Run 13 态、取消、审批、PGID、lease 不回归。

### I2. Text TaskComment

#### I2.1 双迁移

建议：

- `0022_task_comments.sql`
- SQLite 同号迁移

新增：

- task_comment_cursors
- task_comments

修改：

- work_items.acceptance_criteria/phase_entered_at
- task_coordinator_states.consumed_comment_revision

迁移/创建：

- 已有 Coordinator root 回填 `latest_revision=0` cursor；
- 新根 Task 的 WorkItem、Coordinator state、cursor 同事务创建；
- cursor 永不删除；
- 历史非 Coordinator Task 不创建 cursor，新评论 API 返回 `comment_coordinator_required`。
- acceptance criteria 从 Coordinator/Plan 持久事实回填；无法唯一确认时置空并让 Brief 标 partial。
- 当前 review/acceptance 的 phase_entered_at 用 updated_at 回填，后续 phase transition 写精确时间。
- pending_instruction 迁成 system/legacy_migration requirement comment，并删除旧 key。

#### I2.2 Repository / Revision

新增：

- `internal/domain/task_comment.go`
- `internal/application/task_comments.go`
- `internal/persistence/sqlstore/task_comments.go`

实现：

- root Task 解析；
- cursor 行锁增量分配；
- append-only；
- source Run/child/root 归属；
- Idempotency-Key/client_key；
- revision 分页；
- client_key 唯一域 `(root_work_item_id, client_key)`，同 key 不同 body 冲突。

#### I2.3 Coordinator 集成

实现：

- note 不触发；
- requirement durable queued/recovery；
- active Run 终态后消费，不做必达 steering；
- running/current_run_id 为空且无活动 Worker/settlement 时，未消费 actionable comment 使 state durable queued/due；
- consumed watermark 与 Run 创建同事务；
- comment snapshot 进入 TASK_DATA_JSON_V1 和 Run input；
- terminal hook 不越过未消费 actionable comment；
- blocked 不被评论自动解除。
- 删除 coordinator/messages + pending_instruction 双轨；UI 追加指令改 requirement comment。
- 用户新增 child 与 Unblock 在各自事务内追加 durable requirement comment 并排队根 Coordinator。

#### I2.4 ReturnWorkItem

Return reason 改为必填。把 review_feedback、WorkItem BeginExecution、Coordinator waiting_user→queued、events/activity/audit/outbox 放同一事务。提交后 StartCoordinator 为 best-effort；失败仍返回已提交结果并由 recovery loop 继续。

分支：

- coordinated root 执行完整事务；
- coordinated child 返回 `child_review_not_supported`；
- legacy/non-coordinated Task 保留既有回流/activity，不创建 TaskComment。

竞态测试：

- Accept first / Return first；
- Accept first / requirement first；
- terminal hook / comment CAS；
- crash after comment commit/before start；
- duplicate recovery scan。

#### I2.5 API / SSE / UI

API：

- GET/POST Task comments；
- review_feedback 只走 Return。
- 删除旧 POST coordinator/messages。

事件：

- task_comment.created；
- 不在 SSE 放 body。

前端：

- comments store；
- Task detail CommentThread；
- loading/error/stale；
- note/requirement composer；
- review feedback 复用 Return modal。

I2 验收：

- revision 并发；
- append-only；
- cursor；
- 幂等重放无重复事件/唤醒；
- prompt injection；
- SQLite/Postgres 等价；
- SSE replay。

### I3. Review Surface

#### I3.1 Workspace selector

Workspace store 增加：

- workspaces
- selectedWorkspaceId
- generation

切换：

```text
generation++ → stop SSE → reset scoped stores
→ fetch target bootstrap → guarded hydrate → start one SSE
```

所有 refresh/event/status/cursor-expired 校验 workspace+generation。

所有 store 请求使用统一 `captureScope/isCurrent/resetForWorkspace`；只 reset 不足以阻止旧 Promise 回写。

每个合法 SSE 事件先推进 eventCursor。新增 backend event 时同一刀更新 AsyncAPI、backend whitelist、Web EVENT_NAMES、EventSource listener、routeEvent 和 replay tests。

必须 reset：

- dashboard/agents/tasks/logs
- plans/coordinator/dispatches/decisions
- runs/chat
- review queue/comments/brief

#### I3.2 Review Queue

服务端 read model：

```text
task + in_progress + phase(review|acceptance)
```

固定排序、cursor、total_count、水位。URL 使用 `?view=kanban|list&queue=review`，badge 取 total_count。WorkspaceSelector 放持久 SidebarContents，保证 Chat 页面也可切换。

#### I3.3 Delivery Brief

服务端确定性聚合：

- acceptance criteria
- Coordinator conclusion
- attempts/runs
- file changes
- artifacts
- blockers/risks
- requirement/review_feedback
- freshness watermarks

必须实现 RFC 定义的 AttemptSummary、EvidenceItem、RunEvidence、ChangeSet、RiskItem、字段来源、上限与 truncation；禁止用不定形 map 或空对象占位。Brief 在同一 read transaction 返回 as_of_event_seq，部分来源失败列出 missing_sources。

前端状态：

- loading
- current
- stale
- partial
- error
- empty 仅表示真的无证据

SSE 水位领先 Brief 时先 stale，再补拉；补拉失败保留旧内容。

I3 验收：

- A→B 切换旧请求/旧 SSE 不污染；
- 只有一个 EventSource；
- Review Queue 服务端条件/排序/计数；
- Brief 确定排序、freshness；
- 403/404/409/422/Host unavailable/context invalid 不伪装空态；
- 键盘与真实浏览器闭环；
- 继续复用 AgentOutput。

### I4. Hardening

- 补 frontend CI。
- 补 SQLite migration CI。
- 清理 global WorkspaceRoot/defaultWorkspace/v1 Runner 旧代码与文档。
- 对齐 end-goal/C4/README/notes。
- 处理或明确隔离 KimiApp conformance 基线红灯。
- 全量 PR 窗口。

## 5. 实施失败时的停手条件

出现下列任一项，停止受影响工作线并交回评审：

- 必须让 Web/API 传远程绝对路径才能继续；
- 必须跨 Host fallback 才能让测试通过；
- 任一 Run 无法在创建事务内冻结 Snapshot；
- Runner event 无法把 dedup、状态/Session/Artifact、event/outbox 收进同一事务并 commit 后 ACK；
- retry/evaluation 只能重新解析当前 context；
- 旧 Run session 事件无法用 generation/fencing 区分；
- comment watermark 无法与 Run 创建原子；
- Accept 与 actionable comment 可能同时成功；
- Workspace 切换必须保留多个 EventSource；
- 需要新增 canonical WorkItem/Run 状态；
- 需要引入附件、DAG、Automation 或 Dashi sync 才能完成当前切片。

## 6. 验证入口

后端：

```bash
cd agent-team-workbench
go build ./...
go vet ./...
go test -race -count=1 <affected-packages>
gofmt -l <changed-go-files>
```

前端：

```bash
cd agent-team-workbench/web
pnpm tsc -b
pnpm test
pnpm lint
pnpm build
```

迁移：

```bash
cd agent-team-workbench
go run ./cmd/migrate -dsn "sqlite://workbench.db"
```

触面检查阶段如实记录跳过项。PR 窗口再跑全量 race、PostgreSQL migration 和真实浏览器验收。

## 7. 建议提交切片

用户没有要求 commit，默认不提交。若之后获得授权：

1. `docs(architecture): define task control surface contracts`
2. `test(contracts): guard execution context and comments`
3. `feat(context): persist workspace locations and run snapshots`
4. `feat(runner): route offers by execution host`
5. `refactor(runtime): resolve cwd from execution context`
6. `feat(tasks): add append-only task feedback`
7. `feat(web): add workspace and review surfaces`
8. `ci: gate frontend and sqlite migrations`
9. `cleanup(runtime): remove global workspace root path`

每刀只暂存本任务文件，不用 `git add -A`，不加 Co-Authored-By。

## 8. 实现完成清单

- [x] I0 契约和红测。
- [x] 0021/0022/0023 双目录迁移。
- [x] Host/Location/Context/Snapshot。
- [x] Runner v2/fencing/Host routing。
- [x] 全 Adapter Resolved context。
- [x] TaskSession generation。
- [x] TaskComment/revision/consumption。
- [x] Return/Accept 竞态。
- [x] Workspace selector/generation fencing。
- [x] Review Queue/Delivery Brief。
- [x] 定向与全量后端 race tests。
- [x] 前端 typecheck/test/lint/build。
- [x] SQLite fresh/upgrade migration；PostgreSQL 语义 parity 与 CI gate。
- [x] 真实本机浏览器 + worktree 验收；远程 Host 走真实 SQLite Gateway/lease/offer 闭环。
- [x] 现有基线红灯没有被掩盖。
- [x] 独立代码复审通过并获用户授权合入 main。

## 9. 评审收口

独立后端、Runner 与前端审查在初版实现上发现并修复了跨 Workspace Location、
TaskSession 并发 owner、评论 cursor、Runner 注册/重连/进程重启、durable approval、
毒帧、Workspace 请求隔离与 legacy Task 展示等问题。最终证据：

- `go build ./...`、`go vet ./...`、`gofmt -l .`、`git diff --check` 全绿；
- `go test -race -count=1 ./...` 全绿；
- 0001→0023 全新 SQLite 迁移与升级/目录 parity 测试全绿；
- 前端 `tsc -b`、91 文件 761 测试、lint、production build 全绿；
- 真实浏览器验证 Review Queue、评论 SSE→Brief、Return 可访问性、Workspace A→B
  隔离与 legacy child 行为，控制台无 warning/error。

本机没有 PostgreSQL 服务；live PostgreSQL 迁移继续由仓库 CI 的 PostgreSQL job
执行，当前评审已通过双目录语义 parity 与 SQL/仓储测试。
