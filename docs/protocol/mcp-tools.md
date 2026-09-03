# atw-mcp：任务看板的 stdio MCP 工具面

> 对应设计：`architecture/clawteam-borrowings-design.md` F5 节（控制面 MCP 工具化，只读先行）。
> 代码事实源：`internal/mcpserver/` + `cmd/atw-mcp`（2026-09-02）。

## 定位

`atw-mcp` 是独立于控制面主进程的 stdio MCP server，被各 agent harness 的 MCP 配置拉起。
agent 借此**自查看板与治理读模型**，不再依赖人把上下文塞进 instruction。写面只保留 WorkItem claim/return 和 Todo claim/release/user-action，全部调用 Application Service；不直读/直改 governance 表。

- 传输：stdio（stdout 只走 MCP 协议，日志一律 stderr）。
- 存储：与控制面同一个 SQLite 数据库（`-dsn` 或 `DATABASE_URL`，只接受 `sqlite://` DSN）。
- 鉴权：本地场景先不做多租户——进程能读到库即有读权限；写面受领域状态机约束（见红线）。

## 工具清单（25 个）

| 工具 | 入参 | 说明 |
|---|---|---|
| `workspace_list` | — | 列出全部 workspace（看板入口） |
| `task_list` | `workspace_id*`, `status?` | 任务列表（默认 50 条）；status 枚举 todo/in_progress/blocked/completed/cancelled |
| `task_get` | `workspace_id*`, `work_item_id*` | workspace-scoped 任务详情 |
| `run_list` | `workspace_id*`, `work_item_id*` | 任务的全部 run |
| `run_get` | `workspace_id*`, `run_id*` | 单个 run 详情 |
| `run_events_tail` | `workspace_id*`, `run_id*`, `limit?` | run 事件尾部窗口，上限 200 |
| `approval_list` | `workspace_id*`, `run_id*` | run 的审批请求（只查不批） |
| `task_claim` | `workspace_id*`, `work_item_id*`, `agent_id*`, `expected_version*` | Service-backed WorkItem 认领 |
| `task_return` | `workspace_id*`, `work_item_id*`, `reason?`, `expected_version*` | Service-backed 打回 |
| `goal_list` / `goal_get` | `workspace_id*` + goal identity | Goal 查询 |
| `governance_metrics_get` | `workspace_id*` | 确定性治理指标 |
| `todo_list` / `todo_get` | workspace/Goal/Todo identity | Todo 查询 |
| `todo_claim` / `todo_release` | workspace/Todo/owner/version | 受限治理 claim 命令 |
| `todo_resolve_user_action` | workspace/Todo/resolution/actor | 受限 user-action 解决 |
| `turn_receipt_get` | workspace + TurnKey | 不可变 Receipt Header/Phases |
| `quota_get` / `quota_turn_get` | workspace + Goal/TurnKey | policy、reserved、committed、unresolved 读模型 |
| `handoff_list` / `handoff_get` | workspace + Goal/Handoff | Handoff 只读 |
| `evidence_list` | workspace + Goal | Evidence 只读 |
| `projection_get` / `projection_repairs_list` | workspace + Goal | projection/repair record 只读 |

`*` 为必填。所有错误统一以 MCP tool error 返回（进程不 crash）；`expected_version` 失配报
版本冲突，提示重新 `task_get` 拿最新 version。

### 输出形态

- 实体类输出直接 marshal domain 实体，使用其 JSON tag（如 `id` / `status` / `agent_profile_id`）。
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

二进制构建：`cd agent-team-workbench && go build -o atw-mcp ./cmd/atw-mcp`。

## 红线（刻意不暴露）

以下操作**不存在于工具表**（`internal/mcpserver/tools.go` 顶部注释 + 测试
`TestToolRegistryRedLine` 双重钉死，防回潮）：

| 不暴露 | 原因 |
|---|---|
| approval resolve | agent 不能批自己的审批——安全红线，审批只归人（HTTP API + UI） |
| work item 创建 / 删除 | 看板结构变更只归人，agent 只能查与认领 |
| 会话重置 | 会话生命周期只归 ModuleRunner（resume 探测失败走自愈，不开放外部触发） |
| Handoff create/accept/reject/cancel | 不让无 RBAC identity 的 MCP 伪造所有权交接 |
| ProjectionRepair 写入 | repair 是受保护的服务端命令，MCP 只读 repair record |
| Delivery Brief snapshot | snapshot 含 restricted artifact metadata，只在 Approval-only REST 开放 capture/get |
| Quota reconciliation | 人工对账需 Approval permission 和正面 Evidence，MCP 不开写入 |

写面严格限制在上述五类 Service 命令，把「agent 能改看板」的攻击面压到最小；
MCP 进程与控制面主进程分离，崩了不影响调度。
