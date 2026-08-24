# ClawTeam 可借鉴功能的设计文档

> 来源调研：`references/clawteam-openclaw-comparison.md`（ClawTeam-OpenClaw v0.3.0 源码静态分析）。
> 本文回答：哪些功能值得借鉴、各自如何落地到 agent-team-workbench（Go 控制平面 + runnerd + adapter + React）。
> 日期：2026-08-24。状态：设计稿（未实施）。

## 设计约束（与现有架构红线对齐）

- Run 状态机 13 态是唯一权威；ModuleRunner 是进程内唯一推进点；任何 Outcome 必须能落终态。
- task_sessions 只写墓碑不 DELETE；runs_count/usage 按 run 维度幂等。
- 取消一律经控制面前转 + adapter 取消面。
- 迁移走 `migrations/` 与 `migrations/sqlite/` 双目录语义等价。
- 借鉴只引入**数据面/调度面增量**，不改变中心化控制平面路线（ClawTeam 的文件系统控制面、prompt 驱动协调明确不借）。

---

## F1. 任务级执行锁与死 owner 抢占

### 现状与差距

- `work_items`（migrations/sqlite/0001_init.sql:54）：`status` 5 态 + `agent_profile_id`（assignee）+ `version` 乐观锁；M4 的 `ClaimWorkItem`（internal/application/claim_return.go:20）是「todo 且无 assignee」的一次性指派。
- **缺口**：任务进入执行后没有任何锁。owner agent 宕机/卡死时，任务永远停在 `in_progress`，无人感知、无人能接。ClawTeam 的 `locked_by/locked_at` + 死 owner 抢占 + `release_stale_locks()` 正是填这个洞。

### 设计

**Schema**（新迁移 0013）：
```sql
ALTER TABLE work_items ADD COLUMN locked_by_run_id TEXT REFERENCES execution_runs(id);
ALTER TABLE work_items ADD COLUMN locked_at DATETIME;
```
语义：锁归属一个 **run**（不是 agent——agent 死活由 run 的 lease 判定，复用现有 `run_leases` 续租面，不引入第二套活性判定）。

**锁生命周期**：
- 获取：run 从 queued→running 的推进点（ModuleRunner）里，同事务写 `locked_by_run_id=run.id, locked_at=now`；已被活体 run 持有锁的任务拒启（409 version_conflict 语义无冲突）。
- 释放：run 落任何终态时同事务清空锁字段。
- 抢占：`ClaimWorkItem` / 调度器遇到「锁存在但属主 run 已终态或 lease 过期」的任务 → 允许抢占（旧锁作废，记 activity：`锁已被抢占（原 run {id} 已终止/失联）`）。
- 回收兜底：控制面周期性扫「locked_at 超阈值且属主 run 非活跃」的锁并释放（对齐 ClawTeam release_stale_locks；挂在现有 wakeup 调度循环里，tick 复用）。

**事件/UI**：`work_item.locked`/`work_item.lock_preempted` 进 SSE 事件面；看板任务卡显示锁标记（🔒 + run 链接），被抢占的任务标题旁显示提示。

### 实施步骤

1. 双目录迁移 + `sqlstore` work item 读写的锁字段。
2. application：`AcquireExecutionLock`（run 启动事务内）/释放（终态事务内）/抢占判定纯函数（`lockStealable(lockRun status, leaseExpiry, now)`）。
3. httpapi：409 冲突语义；ClaimWorkItem 增加抢占分支。
4. web：任务卡锁标记。
5. 测试：锁获取互斥（两个 run 抢同任务只有一个成功）、终态释放、可抢占/不可抢占边界、回收扫描。

### 风险与取舍

- 锁字段不进 `version` 乐观锁比较（锁本身是并发原语，不参与业务版本）——读写需同事务，否则出现「版本过了但锁丢了」。
- 不做 ClawTeam 的 `--force` 人工破拆入口（我们的抢占判定已覆盖死 owner 场景，人工面留 admin 直接改状态）。

---

## F2. Agent 健康分与熔断（调度侧）

### 现状与差距

- runner 级活性有 `run_leases`（runnergateway）；run 失败仅落状态，没有按 **agent 维度**的质量累计；M4 护栏只有预算闸门。
- ClawTeam：`healthy→degraded→open` 熔断 + quality_score 滑窗（成功 +0.1/失败 −0.2）+ 60s 冷却半开。

### 设计

**Schema**（0013 同刀）：
```sql
CREATE TABLE agent_health (
    agent_id              TEXT PRIMARY KEY REFERENCES agent_profiles(id),
    consecutive_failures  INTEGER NOT NULL DEFAULT 0,
    quality_score         REAL NOT NULL DEFAULT 1.0,   -- 滑窗 [0,1]
    circuit_open_until    DATETIME,                    -- 非空=熔断中
    updated_at            DATETIME NOT NULL
);
```

**记分**（在 run 落终态的同一事务里更新）：
- succeeded：`score = min(1, score + 0.1)`，consecutive_failures 清零；
- failed/lost：`score = max(0, score − 0.2)`，consecutive_failures+1；
- cancelled/interrupted：不计（人为动作不是 agent 质量问题）；
- consecutive_failures ≥ 3 → `circuit_open_until = now + 60s`。

**消费点**（两处闸门，都只读 agent_health，不阻塞人工操作）：
1. 调度/claim：熔断期内的 agent 不被自动 claim/dispatch（`ClaimWorkItem` 返回 409 + `circuit_open` 错误码；人工指派仍可行——护栏只挡自动路径）。
2. UI：agents 列表/详情显示健康点（绿/黄/红 = 正常/degraded/熔断）与 quality_score。

### 实施步骤

1. 迁移 + store 层 `AgentHealthStore`（UpsertScore / Get / List）。
2. run 终态事务里挂记分钩子（注意与 usage 幂等同事务）。
3. claim/dispatch 闸门 + SSE 事件 `agent.health_changed`。
4. web：agents 页健康点。
5. 测试：记分曲线（连败 3 次熔断、成功恢复）、冷却窗口边界、人工指派不受阻、取消不计分。

### 风险与取舍

- 阈值（3 次/60s/±0.1/0.2）先做成代码常量，不进配置——等真实数据再提配置面。
- 半开试探不做（ClawTeam 有时间窗半开）：我们的 claim 低频，冷却结束即全开足够。

---

## F3. 实体级幂等键（client_key）

### 现状与差距

- 命令级幂等已有：`idempotency_keys`（0001_init.sql:201，workspace+key → request_hash/result_ref），HTTP 写命令带 `Idempotency-Key`。
- **缺口**：实体自身没有客户端可指定的去重键。UI 双击/重试/断线重发会产生重复 work item；fork/队列等客户端编排场景（本仓刚做的队列 drain）尤其需要「同一条消息绝不建两次任务」。
- ClawTeam：`TaskItem.idempotency_key` 锁内查重；消息幂等键扫事件日志。

### 设计

**Schema**：`work_items ADD COLUMN client_key TEXT`；`CREATE UNIQUE INDEX idx_work_items_ws_clientkey ON work_items(workspace_id, client_key) WHERE client_key IS NOT NULL`（部分索引，空值不占唯一性）。runs 同理 `execution_runs ADD COLUMN client_key TEXT` + 同构部分唯一索引。

**API**：`POST /work-items` / `POST /work-items/{id}/runs` 接受可选 `client_key`：
- 命中唯一索引冲突 → 查既有实体返回 **200 + 实体 + `X-Idempotent-Replay: true`**（不 409），语义=「你的实体已存在」。
- 与命令级 Idempotency-Key 并存：命令级防「同请求重放」，实体级防「同业务意图重复创建」。

**消费点**：web 的 send/queue drain/fork 创建时携带 `client_key`（如 `chat:{conversationId}:{msgHash}`），drain 重试安全化。

### 实施步骤

1. 双迁移 + store insert 的唯一冲突捕获翻译。
2. application 层 create 路径加「先查 client_key」快速路径。
3. httpapi：200 replay 语义 + 测试（同 key 两次创建返回同一实体；不同 key 正常；空 key 不占唯一）。
4. web：send/drain/fork 带上 client_key。

### 风险与取舍

- client_key 只做「创建去重」，不做「内容变更检测」（request_hash 是命令级职责）。
- 不暴露给最终用户 UI，纯客户端机制。

---

## F4. Adapter 方言表（代码组织项）—— 已降级，不实施

> **2026-08-24 校准**：评审时高估。ClawTeam 的方言表管的是 CLI flag 方言
> （它 spawn 子进程拉 CLI）；我们的 adapter 走 app-server 协议（codexapp/kimiapp
> JSON-RPC），差异在协议状态机而非命令行 flag——一张表装不下协议差异，装了也
> 不省事。降级为一页附录式对照（各家 resume/session 参数差异），不做 Dialect 类型。

### 现状与差距（原始记录）

- 各 adapter（codexapp/kimiapp/dsh/claudecode/zcode）把「CLI 权限 flag、prompt 传递方式、session 捕获/隔离、resume 参数」散落在各自 Go 代码里；新增 runtime 要通读既有 adapter 才能抄全。
- ClawTeam 每家 CLI 一张方言卡（permission flag / prompt mode / session flag / resume command），集中可查。

### 设计

新增 `internal/runtime/adapters/dialect.go`：
```go
// Dialect 声明一个 agent CLI 的交互方言：权限、prompt 传递、会话锚点与恢复。
type Dialect struct {
    PermissionBypass []string // 例：codex 的 --dangerously-bypass-approvals-and-sandbox
    PromptMode       string   // "positional" | "flag:-p" | "flag:--message" | "stdin"
    SessionFlag      string   // 例：openclaw 的 --session；空=不支持显式会话隔离
    ResumeFlag       string   // 例：claude --continue / codex resume --last；空=不支持
}
```
各 adapter 在构造处声明自己的 Dialect 并导出（`codexapp.Dialect`），启动命令拼装统一走 `dialect.Apply(cmd)`。

**这是纯重构**：行为不变，只把隐式知识显式化。配套一张 `docs` 方言对照表（从声明生成或手维护）。

### 实施步骤

1. 定义 Dialect 类型 + 五个 adapter 逐个声明（每 adapter 一刀，便于审阅）。
2. 拼装逻辑收敛到 dialect.Apply；删除各 adapter 内重复的字面量。
3. 测试：每家方言的关键 flag 断言（防「改 codex 顺手改了 kimi」）。

### 风险与取舍

- 不为方言表引入注册中心/插件机制——adapter 仍是编译期注册（SPI v2 已定），Dialect 只是数据声明。

---

## F5. 控制面 MCP 工具化（只读先行）

### 现状与差距

- 控制面只有 HTTP+SSE；agent 无法「自查看板」——想要任务上下文只能靠人把文本塞进 instruction。
- ClawTeam 把整套控制面暴露成 26 个 MCP 工具，agent 原生可调。

### 设计

新增 `cmd/atw-mcp`（stdio MCP server，Go）：
- **第一批（只读）**：`task_list / task_get / run_get / run_events_tail / approval_list` —— 让 agent 能自查任务、run 状态与待审批。
- **第二批（写，小面）**：`task_claim / task_return`（走既有 application 命令，自带 Idempotency-Key + version 校验）。
- **明确不暴露**：approval resolve（agent 不能批自己的审批——安全红线）、work item 创建/删除、会话重置。
- 传输：stdio（被各 harness 的 MCP 配置拉起）；鉴权：启动参数带 workspace id + 只读 token（本地场景先不做多租户）。

### 实施步骤

1. cmd/atw-mcp 骨架 + stdio JSON-RPC（可用 mark3labs/mcp-go，需评估依赖）。
2. 只读 5 工具直通 application 查询面。
3. 写 2 工具 + 鉴权边界测试（resolve 类操作不存在于工具表）。
4. 文档：各 harness 的 MCP 配置接法。

### 风险与取舍

- 第一批只读，把「agent 能改看板」的攻击面推到第二批再评。
- 不在控制面主进程内嵌 MCP server——独立进程，崩了不影响调度。

---

## 里程碑与实施状态

> 2026-08-24 校准（按真实项目阶段重排，替代最初的 M1–M5 顺序）：
> F3/F5/F1 已实施合入 main；F2 挂起（等自动调度密度上来再做——熔断防的是
> 自动重试风暴，当前 agent 由人/lead 指派，没人消费）；F4 砍掉（见 F4 节校准注记）。

| 功能 | 状态 | 落点 |
|---|---|---|
| F3 实体级幂等键 | ✅ 已实施（main `635c310`） | 迁移 0013；CreateWorkItemIdempotent/CreateRunIdempotent；队列/fork 已携带 client_key；顺带修复 parent_id 透传丢失（fork 链路真实 bug） |
| F5 控制面 MCP 工具化 | ✅ 已实施（main `368a692`） | `cmd/atw-mcp` + `internal/mcpserver`；只读 7 工具 + 写面 task_claim/task_return；审批 resolve 等红线不暴露 |
| F1 任务级执行锁 | 🚧 实施中 | 迁移 0014；transitionRunLocked 事务内取放锁；死锁抢占与周期回收 |
| F2 健康分熔断 | ⏸ 挂起 | 触发条件：编排层自动派发密度上来（claim 高频无人值守）时重启 |
| F4 方言表 | ❌ 不实施 | 协议差异不在 CLI flag 层（见 F4 节） |

依赖关系：F1 依赖 F3 的迁移位（0013→0014 顺延）；其余相互独立。

## 不借清单（防回潮）

- 文件系统即控制面（吞吐/一致性天花板，且与我们 SQLite+事务路线冲突）。
- prompt 驱动协调（可靠性绑定模型自觉性；我们的 ModuleRunner 是唯一推进点）。
- SSE 快照轮询（我们是真事件流）。
- tmux 进程编排（我们的 runnerd+adapter 已覆盖，且支持远程 runner）。
