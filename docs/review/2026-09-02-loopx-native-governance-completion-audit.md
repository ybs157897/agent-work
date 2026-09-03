# LoopX 原生治理 WP0–WP7 完成审计

- Status: WP0–WP7 repository implementation complete; local gates complete; external runtime/provider gates pending
- Created: 2026-09-02
- Last reviewed: 2026-09-03
- Worktree: `/Users/yin/.codex/worktrees/67d6/agent-work`
- Branch: `codex/research-loopx-task-foundation`
- Base: `82665d8`

Governance migration scope: `0024`–`0035` and `0039`–`0042`. `0036`–`0038` are
completed on the same branch as a separate Agent config / HTTP idempotency hardening line.

## 1. 结论

本分支已经把 LoopX 的长程治理语义原生落到 Agent Team Workbench 的 Go + SQLite 权威链中；没有运行或集成 LoopX，也没有引入第二套 Task/Run/Lease/Quota/Event 真相源。WP0–WP7 的仓库内功能均已有 Domain、Migration、Repository、Service、Contract、测试以及需要的 Web/MCP 表面。

阻塞语义现在也是一条原子控制线：根 WorkItem、Task Coordinator、Goal、当前 Todo 在同一事务内同步到 blocked，Todo claim 同时释放；显式 unblock 在同一事务内恢复 WorkItem、Goal、Todo，并清除旧 claim、重新开启新的有界 Coordinator/repair 周期。缺少 Goal 或当前 Todo 的受管系统 Coordinator 直接 fail closed，不转调用普通/legacy `SubmitPlan`，也不伪造 TurnReceipt 或 TurnSeq。

这里的“repository implementation complete”不等于伪称所有真实环境验收已完成。当前机器没有可用的 Remote Runner/Host 与对应 Provider 凭据；项目隔离的 Codex/Kimi home 也没有可用于真实全生命周期 smoke 的认证材料。因此真实 Codex/Kimi valid+repair、Remote Host/Runner 断线/boot/session_unknown 仍是外部 gate，不能用 mock、fixture 或 in-process Gateway 替代。

## 2. 最终权威链

```text
raw PlanDecisionV2
  -> strict schema + typed decoder
  -> Goal / Todo / Claim / DecisionScope authority
  -> ShouldRun / reservation
  -> append-only TurnReceipt
  -> TodoToPlanCompiler
  -> existing SubmitPlan
  -> Dispatch / ExecutionRun / TaskSession / RunnerGateway
  -> canonical usage / spend / Evidence
  -> ProjectionRepair / GovernancePanel
  -> human Accept
```

不可跨越的边界：Todo 不直接创建 Run；Runtime Adapter 不推进业务状态；Claim 不替代 Runner lease；Web/MCP 不重算治理状态；人工对账不改原 spend/canonical/reservation；用户 Accept 仍是最终完成门。

## 3. 本轮终审新增并修复的问题

1. Runner 在 `run.accept` 前建立本地 run/lease/control fence；ACK 除三元定位键外逐字核对 envelope/payload runner、run、fencing 和 event ID。
2. DSH supervisor 启动后立即缓存 pgid，组长被收尸后仍可回收同组孤儿进程。
3. SSE 在 replay 前订阅并二次按 cursor 回读，关闭 backlog→subscribe 丢事件窗口；0031 持久化 `aggregate_version`。
4. 公开 root Task 强制 1–64 条验收标准；Service、HTTP、OpenAPI、CreateTaskModal 同口径，旧测试 fixture 已全部迁移。
5. governed Plan 永久拒绝在 phase3 留 rejection，并与 source usage、reservation 关闭、Todo blocked 同事务提交；存储故障保留 `plan_commit` retry checkpoint，terminal replay 继续同一 Turn。
6. 缺 Goal/当前 Todo 的受管 Coordinator 不创建 legacy Plan：治理初始化错误在 Run 创建前转成 `governance_state_unavailable` blocker；没有普通 `SubmitPlan` 旁路。
7. 根 blocker sweep 所有该 root WorkItem 的 open Dispatch（而不是只关闭失败 Run 的批次），以 CAS 关闭为 `degraded` 并逐批发一次更新事件；并发/迟到终态不会复活批次。
8. blocker 四层（WorkItem、Coordinator、Goal、current Todo）及 claim 释放同事务、同 outbox；unblock 恢复 Goal active/Todo pending，保持 turn watermark、清除旧 claim，并启动新 repair budget。
9. unresolved usage 增加 0033 append-only `QuotaGapResolution`。v1 只有 `reconciled`、没有 waiver；必须绑定 passed/accepted Evidence，对账 amount 继续计入 committed/ShouldRun。
10. waiting Plan 被新 Plan supersede 时，旧 Plan 的所有 pending manual dispatch approval 在同一事务过期并发出 `approval.expired`；迟到 approve/reject 均不得再创建 child Run。
11. Delivery Brief 以 0032 immutable RFC8785 snapshot 进入 Evidence；无关 Workspace event 不误判 stale，source version/content 变化会拒绝。
12. Artifact 增加 Approval-only accept API，0034 禁止 accepted→draft。
13. 0035 在 Go repository guard 之外补 SQLite terminal-only trigger：`canonical_usage(+digest)` 只能随终态 Run 写入；既有非终态违规会使迁移本身 fail closed。终态钩子仍允许迟到真实 report 首次固化，canonical 一旦存在不可改。
14. ProjectionRepair 响应补齐 `repair`/`projection` JSON tag，消除 Go 字段名泄漏。
15. 幂等 4xx/replay 保持 `application/problem+json`；model DELETE 与 provider credential PUT 接入 claim-first idempotency。
16. Agent Create/PATCH 把 Agent CAS、event 与非敏感 target snapshot 写入 0036 durable sync intent；外部发布失败保留可重放 intent，Create 返回幂等 202 而不重复建 Agent。0037 用 owner token/expiry/Renew 围栏 HTTP 幂等，0038 禁止改写已 applied intent。
17. Restricted Delivery Brief snapshot 不暴露给无 RBAC identity 的 MCP；MCP 写面只保留 claim/release/user-action。
18. REST/MCP 治理集合字段统一保证空集合编码为 `[]` 而非 `null`；Goal/Todo/Quota/Receipt/Handoff/Evidence/Projection repair 等列表均有回归断言。
19. 终态 remote Run 的迟到 `usage.updated` 仅在 released lease 的 run/runner/fencing 精确匹配时接受；重放幂等，错身份仍 stale。cumulative anchor 只在身份兼容且逐分量不回退时推进。
20. 人工拒绝 `plan_dispatch` 在同事务结算 source/quota phase6，收口 Todo/Coordinator/Goal；旧 Turn 迟到拒绝不能 block 较新 Turn。
21. DSH legacy usage 与 canonical bucket 共用严格整数转换；负数、NaN/Inf、非整数、越界或非数值不得进入台账。
22. 六类 `TurnDecision` 均有生产路径；无 Plan 的 repair/replan/user_action 也形成 phase 1–7 control receipt，replay 重算 Header input digest，旧 governed source 先结算且不二次计费。
23. Handoff 已从 claim transfer 扩展为 durable continuation：target Run 继承精确 Handoff/claim/source proof，支持多轮与二次转交；late Plan/verdict 只作 evidence，settlement wake 按消费时 target 重路由，blocker 清 checkpoint 但保留历史。
24. Todo completion 绑定最新 admitted TurnReceipt Header 和 accepted root WorkItem evidence，通过 REST/OpenAPI/AsyncAPI/Web 暴露；completed/cancelled Todo 整行终态不可变。
25. 受管 root `CreateRun` 使用仅内部能铸造的 admission proof，伪造 Coordinator/Wake/AutoHeal context fail closed。`session_unknown` 的 lifecycle check、anchor tombstone、deterministic Run 和 durable dispatch claim 同事务，启动/Resume 可恢复 commit→dispatch 崩溃窗口。
26. 0039–0042 已按单一职责拆分为 Todo completion、Handoff continuation/renewal、blocked-root rebuild 与 control receipt phase 语义；迁移测试分别从 0038、0039、0040、0041 边界升级到 latest，并覆盖 latest 全量重跑。

## 4. R0–R8 取证矩阵

| Requirement | 仓库实现 | 关键证据 | 状态 |
|---|---|---|---|
| R0 PlanDecision/repair | canonical schema、strict typed decoder、2 次 durable repair、raw JSON only | syntax/schema/semantic/authority/restart/concurrent tests | code complete；real Provider gate |
| R1 Goal | 0024 Goal aggregate、root ensure、acceptance contract、state/version | Domain/Repo/Service/restart tests | complete |
| R2 Todo/Claim/Scope | closed state/class、claim CAS/expiry、frozen agent/work scope、latest-Turn completion identity | claim race、renewal、scope、completion Header/evidence/terminal immutability tests | complete |
| R3 Decision/Receipt | 六类 TurnDecision、Header + phase1–7、JCS/input digest、Plan/control lineage、crash replay | identity/conflict/header-only gap/plan_commit/control replay tests | complete |
| R4 Quota | 0027/0028/0030/0033/0035、canonical usage、anchor、price、abort compensation、reconciliation、terminal-only canonical trigger | normal/race/migration/SQL trigger tests | code complete；real usage gate |
| R5 Handoff | 0029 aggregate + 0040 continuation/renewal；atomic claim transfer、exact source snapshot、multi-hop、late-source fence、settlement reroute | concurrent accept/scope/delegated repair/eval/replan/user-action/blocker tests | complete |
| R6 Evidence/Repair | ValidationResult、Artifact/Approval/Run/Plan/WorkItem/Brief snapshot、ProjectionRepair | tamper/stale/partial/rebuild/finish tests | complete |
| R7 API/MCP/UI | workspace REST、RBAC/idempotency、live events、Service-backed MCP、GovernancePanel、Todo completion identity、空集合 `[]` 合同 | contract guard、HTTP/MCP/Web tests、真实浏览器 1440/1024 | local complete；external runtime gate |
| R8 Delete legacy | production fenced Plan parser、mode switch、shadow-only production naming removed | production-source zero searches + contract tests | complete |

## 5. AC-01–AC-15

| AC | 当前证据 | 判定 |
|---|---|---|
| AC-01 | root Task→Goal/Todo，idempotent create/rebuild | automated pass |
| AC-02 | provider-schema transport 汇入同一 decoder | adapter/contract pass；real Provider pending |
| AC-03 | unsupported provider 使用同一 raw text decoder，无旁路 parser；fenced/bare legacy-shaped input 不触发执行旁路 | automated pass |
| AC-04 | syntax/schema repair 1–2、exhausted 后四层 blocker 原子同步、显式 unblock 新 budget | automated pass |
| AC-05 | semantic/authority 无 partial Plan/Run；永久拒绝原子释放 reservation；缺 Goal/当前 Todo fail-closed 且不走 ordinary/legacy `SubmitPlan` | automated pass |
| AC-06 | claim CAS 并发单赢家 | automated race pass |
| AC-07 | reservation/spend/reconciliation replay、price/cache/anchor/trigger；0035 禁止非终态 canonical usage | automated pass；real usage pending |
| AC-08 | restart、turn_seq、Header/Phase/input digest、plan_commit/control/self-heal dispatch checkpoint | automated pass |
| AC-09 | Handoff target accept 后原子转 claim；delegated continuation、multi-hop、late-source fence、wake reroute | automated pass；real runtime handoff pending |
| AC-10 | 缺 Evidence/validation 拒绝 finish | automated pass |
| AC-11 | ProjectionRepair 只 replay，不改 Receipt/Run/Plan | automated pass |
| AC-12 | GovernancePanel 单链 Goal→Todo→Plan→Run→Evidence；Coordinator/Goal/Todo blocker 状态一致；1440/1024 截图与横向溢出探针通过 | render tests + real browser pass |
| AC-13 | MCP governance tools 走 Service，direct Store 搜索为零，restricted snapshot/write 禁止 | automated pass |
| AC-14 | production fenced parser/mode/shadow 搜索为零 | automated/source pass |
| AC-15 | finish{evaluation:true}+verdict pass 后仍需 Accept，Accept 原子完成 Todo/Task/Coordinator/Goal 并固化 completion identity | automated pass |

## 6. 验证台账

当前已通过：

- `gofmt -l .` 空；`go vet ./...`；`go build ./...`。
- `go test -count=1 ./...` 全包通过；最终 Application 单包 95.149s。
- Web `pnpm tsc -b`、94 files / 771 tests、`pnpm lint`、`pnpm build` 全绿。Vitest 的 React SSR `useLayoutEffect` 和 Vite >500 kB chunk 为非失败警告，本分支未将其冒充为本 Goal 回归。
- `git diff --check`。
- `go test -race -count=1 -timeout=20m ./...` 中除 Application 外的包全绿；Application 在 1202.735s 处仍执行 migration-heavy 用例而命中纯超时，未出现 data race/断言失败。原样改用 `go test -race -count=1 -timeout=35m ./internal/application` 后全绿（1293.962s）；最新 `internal/runtime` race 复跑 2.072s 全绿。
- fresh SQLite 已顺序应用 0001–0042；从 0038、0039、0040、0041 各边界升级到 latest 与 latest 全量重跑全绿。
- 最新二进制 + 迁移到 0042 的临时 SQLite 通过公开 API / GovernancePanel 浏览器验收：1440 与 1024 宽度无横向溢出，Coordinator/Goal/Todo 均 blocked；非 completed Todo 的两个 completion identity 字段均存在且为 `null`。

浏览器证据已随评审文档保留：

- [1440 light：Coordinator/Goal/Todo blocker 一致](assets/2026-09-03-governance-blocked-1440-light.png)
- [1440 dark-preference 请求：页面实际仍为浅色](assets/2026-09-03-governance-blocked-1440-dark-preference.png)
- [1024 light：窄视口状态链](assets/2026-09-03-governance-blocked-1024-light.png)
- [1024 dark-preference 请求：页面实际仍为浅色](assets/2026-09-03-governance-blocked-1024-dark-preference.png)
- [1440 治理链：Goal/Todo blocked、Plan/Run/Evidence 等待态](assets/2026-09-03-governance-chain-blocked-1440.png)
- [1024 治理链：Goal/Todo blocked、Plan/Run/Evidence 等待态](assets/2026-09-03-governance-chain-blocked-1024.png)

诚实边界：两张 `dark-preference` 截图不能证明全局 dark theme；页面实际仍渲染为浅色。验收中 Plan/Projection 404 是 UI 显式处理的空读模型探测，不是成功响应；远程字体 ORB 后页面使用系统字体 fallback。

## 7. 外部 gate 与非目标

必须在具备真实环境后执行：

1. Codex 与 Kimi 各一条 valid PlanDecision 和一条 repair，全程使用项目隔离 home。
2. Remote Host/Runner 完整 Goal；真实断线重传、boot 变化、late usage、session_unknown。
3. Provider 实际 usage/cost 与外部账单抽样对账。

不属于本 Goal：真实多用户 cookie/session authentication middleware、计费/套餐/发票、完整 Goal 图编辑器、跨 Goal/租户市场。

## 8. Git 与交付边界

本轮已获得用户明确授权，将 `codex/research-loopx-task-foundation` 按仓库纪律分刀提交并合并到 main；最终提交与合并记录以 Git history 为准。治理迁移与已完成的 0036–0038 Agent config/idempotency 加固同分支交付，但在文档中保持独立范围。主树中原有、与本 Goal 无关的未提交文件不纳入本分支提交。
