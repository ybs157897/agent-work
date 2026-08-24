# atw-mcp：任务看板的 stdio MCP 工具面

> 对应设计：`architecture/clawteam-borrowings-design.md` F5 节（控制面 MCP 工具化，只读先行）。
> 代码版本：`internal/mcpserver/` + `cmd/atw-mcp`（branch `agent/f5-mcp-readonly`，2026-08-24）。

## 定位

`atw-mcp` 是独立于控制面主进程的 stdio MCP server，被各 agent harness 的 MCP 配置拉起。
agent 借此**自查看板**（任务、run、事件、待审批），不再依赖人把上下文塞进 instruction；
写面只开 claim / return 两个小命令，直接复用 application 层既有用例（乐观锁 + 状态机校验）。

- 传输：stdio（stdout 只走 MCP 协议，日志一律 stderr）。
- 存储：与控制面同一个数据库（`-dsn` 或 `DATABASE_URL`；`sqlite://` 前缀走 SQLite，否则 postgres）。
- 鉴权：本地场景先不做多租户——进程能读到库即有读权限；写面受领域状态机约束（见红线）。

## 工具清单（9 个）

| 工具 | 入参 | 说明 |
|---|---|---|
| `workspace_list` | — | 列出全部 workspace（看板入口） |
| `task_list` | `workspace_id*`, `status?` | 任务列表（默认 50 条）；status 枚举 todo/in_progress/blocked/completed/cancelled |
| `task_get` | `work_item_id*` | 单个任务详情（含 assignee、phase、version） |
| `run_list` | `work_item_id*` | 任务的全部 run |
| `run_get` | `run_id*` | 单个 run 详情（状态、用量、失败信息） |
| `run_events_tail` | `run_id*`, `limit?` | run 事件尾部窗口：默认 50、上限 200；倒序取尾后按 run_seq 正序返回 |
| `approval_list` | `run_id*` | run 的审批请求（只查不批） |
| `task_claim` | `work_item_id*`, `agent_id*`, `expected_version*` | 认领无主 todo 任务；已被认领 / 非 todo 报错；同 agent 重复认领幂等 |
| `task_return` | `work_item_id*`, `reason?`, `expected_version*` | 把 review/acceptance 态任务打回重做（回 execution）；其他状态报错 |

`*` 为必填。所有错误统一以 MCP tool error 返回（进程不 crash）；`expected_version` 失配报
版本冲突，提示重新 `task_get` 拿最新 version。

### 输出形态

- 实体类输出直接 marshal domain 实体（字段名与 Go 结构一致，如 `ID` / `Status` / `AgentProfileID`）。
- 列表类输出包一层集合字段：`{"workspaces": [...]}`、`{"items": [...]}`、`{"runs": [...]}`、`{"approvals": [...]}`；空集返回 `[]`（非 `null`）。
- `run_events_tail` 每条事件精简为三字段：

```json
{
  "events": [
    { "event_type": "run.status_changed", "occurred_at": "2026-08-24T12:34:26.182161Z", "data": {"i": 3} }
  ]
}
```

### 入参示例

```json
// task_list
{"workspace_id": "ws_xxx", "status": "todo"}
// run_events_tail
{"run_id": "run_xxx", "limit": 100}
// task_claim（expected_version 来自 task_get 的 Version 字段）
{"work_item_id": "wi_xxx", "agent_id": "agent_xxx", "expected_version": 0}
```

## harness 接法

### Claude Code（`.mcp.json`，项目根或 `~/.claude.json`）

```json
{
  "mcpServers": {
    "atw": {
      "command": "/abs/path/agent-team-workbench/atw-mcp",
      "args": ["-dsn", "sqlite:///abs/path/agent-team-workbench/workbench.db"]
    }
  }
}
```

### Codex（`~/.codex/config.toml`）

```toml
[mcp_servers.atw]
command = "/abs/path/agent-team-workbench/atw-mcp"
args = ["-dsn", "sqlite:///abs/path/agent-team-workbench/workbench.db"]

# 或用环境变量给 DSN：
# [mcp_servers.atw]
# command = "/abs/path/agent-team-workbench/atw-mcp"
# env = { DATABASE_URL = "sqlite:///abs/path/agent-team-workbench/workbench.db" }
```

postgres 部署把 `-dsn` 换成 postgres 连接串即可（无 `sqlite://` 前缀即走 pgx）。
二进制构建：`cd agent-team-workbench && go build -o atw-mcp ./cmd/atw-mcp`。

## 红线（刻意不暴露）

以下操作**不存在于工具表**（`internal/mcpserver/tools.go` 顶部注释 + 测试
`TestToolRegistryRedLine` 双重钉死，防回潮）：

| 不暴露 | 原因 |
|---|---|
| approval resolve | agent 不能批自己的审批——安全红线，审批只归人（HTTP API + UI） |
| work item 创建 / 删除 | 看板结构变更只归人，agent 只能查与认领 |
| 会话重置 | 会话生命周期只归 ModuleRunner（resume 探测失败走自愈，不开放外部触发 |

设计取舍：第一批只读 + 第二批小写面，把「agent 能改看板」的攻击面压到最小；
MCP 进程与控制面主进程分离，崩了不影响调度。
