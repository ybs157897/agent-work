# Codex app-server 官方协议锚点

> 目的：把 workbench 对 codex app-server 协议的认知锚定到 OpenAI 官方文档，替代早期
> 「读源码 + 抓流量」的手工契约认知。适配契约（钉版本与 schema SHA）在
> [`docs/protocol/codex-app-server-v2.md`](../protocol/codex-app-server-v2.md)；本文是官方面的
> 检索地图与升级同步规程。核订日期：2026-08-28，对应官方 main。

## 权威入口

| 入口 | URL | 说明 |
|---|---|---|
| 官方文档站 | <https://developers.openai.com/docs/app-server> | developers.openai.com 文档主入口下的 app-server 篇（浏览器可读，有 bot 防护） |
| 协议 README（canonical） | <https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md> | 随源码演进的最全参考，raw 拉取无防护，适合脚本化对账 |
| 设计背景 | <https://openai.com/index/unlocking-the-codex-harness/> | OpenAI 把 App Server 立为正式产品面的公告：嵌入 Codex agent 的双向 JSON-RPC API |

## Schema 生成与版本钉死

官方承诺：生成的 schema 与生成它的 CLI 版本严格一致（每版一schema）。

```sh
codex app-server generate-ts --out DIR
codex app-server generate-json-schema --out DIR
# 含实验面（experimentalApi 门控的方法/字段）：
codex app-server generate-json-schema --experimental --out DIR
```

workbench 做法：`codex-app-server-v2.md` 钉「CLI 版本 + experimental v2 schema SHA-256」，
vendored CLI 在 `agent-team-workbench/runtimes/codex/`（git lfs）。当前基线 `0.149.0`
（vendored、本机 PATH、契约三者一致）。

## 核心原语与生命周期

- **Thread**（会话，持久，可跨 Run）/ **Turn**（一轮，单位OfWork）/ **Item**（轮内输入输出，tagged union）。
- 传输：默认 stdio，newline-delimited JSON；JSON-RPC 2.0 但线上省略 `"jsonrpc":"2.0"` 头。
- 握手：`initialize`（`clientInfo` 必填；`capabilities.experimentalApi: true` 打开实验面；
  `optOutNotificationMethods` 可按名退订通知）→ `initialized` 通知。未握手请求报
  `"Not initialized"`，重复握手报 `"Already initialized"`。
- 会话：`thread/start`（新）/ `thread/resume`（续，按 id）/ `thread/fork`（分支拷贝历史）。
- 回合：`turn/start` → 流式 `turn/started`、`item/*`、`turn/completed`（终态权威，
  status ∈ completed/interrupted/failed，成功轮携带最终 agentMessage）。
- 控制：`turn/steer`（在飞轮追加输入）、`turn/interrupt`（成功返回 `{}`，轮以
  interrupted 收尾）。
- 过载：入队饱和返回 JSON-RPC `-32001` "Server overloaded; retry later."，客户端应指数退避重试。

## workbench 相关方法面（官方 main 快照）

| 族 | 方法 |
|---|---|
| 会话 | `thread/start` `thread/resume` `thread/fork` `thread/list` `thread/read` `thread/archive` `thread/unarchive` `thread/delete` `thread/compact/start` |
| 历史分页 | `thread/turns/list` `thread/items/list`（resume 可 `excludeTurns:true` + 游标） |
| 回合 | `turn/start` `turn/steer` `turn/interrupt` `thread/inject_items` |
| 探测/目录 | `account/read` `model/list` `collaborationMode/list` `experimentalFeature/list` |
| 服务端执行 | `command/exec`（+write/resize/terminate/outputDelta）`fs/*` `process/*`（实验） |

## workbench 关心的通知面

| 通知 | 载荷要点 |
|---|---|
| `item/started` / `item/completed` | 全量 item；completed 是执行结果的权威状态 |
| `item/agentMessage/delta` `item/plan/delta`（实验） | 文本增量，按 itemId 顺序拼接 |
| `item/reasoning/summaryTextDelta` `/textDelta` `/summaryPartAdded` | 推理摘要与原始推理流 |
| `item/commandExecution/outputDelta` | 命令 stdout/stderr 流；终态 item 带 `commandActions`/`exitCode`/`durationMs` |
| `thread/tokenUsage/updated` | token 用量（独立流式通知；resume 冷启动会重放） |
| `turn/diff/updated` | 轮级聚合 unified diff 快照，每个 FileChange item 后重发 |
| `turn/plan/updated` | `{turnId, explanation?, plan[{step,status}]}` |
| `serverRequest/resolved` | 服务端发起的请求（审批等）被解决/清理的回执 |
| `error` | 轮中错误，携带 `codexErrorInfo` 枚举（ContextWindowExceeded/UsageLimitExceeded/rateLimitExceeded/…） |

Item 全族：`userMessage` `agentMessage` `plan` `reasoning` `commandExecution` `fileChange`
`mcpToolCall` `webSearch` `collabToolCall` `subAgentActivity` `imageGeneration` `imageView`
`sleep` `enteredReviewMode` `exitedReviewMode` `contextCompaction`（取代已废弃 `compacted`）等。

## 审批面

- 命令审批 `item/commandExecution/requestApproval`：decision 支持 `accept` `acceptForSession`
  `acceptWithExecpolicyAmendment` `applyNetworkPolicyAmendment` `decline` `cancel`。
- 文件审批 `item/fileChange/requestApproval`：`accept` `acceptForSession` `decline` `cancel`。
- 权限审批 `item/permissions/requestApproval`：响应给请求的**授予子集** `result.permissions`，
  可带 `result.scope: "turn"|"session"`；未列出的权限视为拒绝。
- 其余服务端请求：MCP elicitation（form / openai/form / url 三模式）、
  `item/tool/requestUserInput`（`isBlocking` 必填）、`attestation/generate`、`currentTime/read`。

## 稳定性承诺

- 默认（stable）面之外的一切 experimental 方法/字段**无向后兼容保证**；未 opt-in 调用实验面
  报 `<descriptor> requires experimentalApi capability`。
- experimentalApi 在 initialize 时一次性协商，进程生命周期内不可更改。
- 因此 workbench 的同步策略是「钉版本」而非「追 main」：升级 vendored CLI 即视为一次契约变更。

## 升级同步规程（vendored CLI 0.149.0 → 新版时执行）

1. 新 CLI 跑 `codex app-server generate-json-schema --experimental --out DIR`，与旧 schema diff，
   关注 workbench 在用的方法/事件/字段是否改名或删除；
2. 更新 [`docs/protocol/codex-app-server-v2.md`](../protocol/codex-app-server-v2.md) 头部基线（版本号 + 新 schema SHA-256）；
3. 修订 `internal/runtime/adapters/codexapp/protocol.go` 投影并跑 `go test -race
   ./internal/runtime/adapters/codexapp/...`；
4. 回到本文核对新增官方能力（usage/diff/plan 等通知面是否值得接入）。
