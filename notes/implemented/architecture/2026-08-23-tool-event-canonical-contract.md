# 工具事件 canonical 契约：call_id 折叠 + 截断投影

Status: implemented

## 决策与理由

工具事件跨适配器统一为同一契约（kimiapp/codexapp 对齐）：

- `tool.started`：`{tool, call_id, args_summary?}`——args_summary 为 ≤200 字符的输入摘要（命令/路径类参数优先，其次 description、紧凑 JSON）。
- `tool.completed`/`tool.failed`：`{call_id, output?}`——output 为 ≤2000 字符的结果文本（kimi 的 string|ContentPart[] 提取、codex 的 aggregatedOutput/result）。
- `tool.progress`：`{call_id, text, percent?}`。

前端 chat 按 `call_id` 把 completed/failed 折叠回 started 同一行，output 渲染为等宽 detail 块。依据：契约消费的入口是 run_events 持久化 + SSE，原始工具输出可达 MB 级，必须适配器侧截断；折叠键必须是 provider 的 call_id/item id 而不是事件 id，否则 completed 无法对回 started。

## 放弃了什么

- **raw 全量透传（kimiapp 原行为：`{raw: <完整 payload>}`）**：保真但事件体积无界，且前端拿不到统一形状，每个适配器一套解析。截断投影牺牲了完整输出的可见性——完整输出本就属于 artifacts/日志面，不属于对话时间线。
- **前端按 item_type 做差异化渲染（命令 vs 文件变更 vs MCP）**：item_type 仍保留在 payload 里，但首版 UI 统一成行+等宽块；等真实使用证明需要差异化再各自加分支。

## 复活条件

若需要完整工具输出审查（合规/排障场景）→ 返工点：适配器把完整 output 落 artifact，事件里带 artifact 引用，前端 detail 块加「查看完整输出」跳转。

## 2026-08-24 增补：dsh 网关对齐

dsh 网关 adapter（`internal/runtime/adapters/dsh/gateway.go`）补入同一契约：

- `tool/call.arguments` 是模型原始 JSON 字符串（dsh-harness `appendToolCall`）：args_summary 按 command/cmd/path/file_path/query/url 键优先提取，其余原串截 200。
- `tool/result` 的 callId/isError/输出都在 `message.content[0]` 的 tool-result 块内（顶层无平铺字段）——旧实现读顶层 `data["isError"]` 永远漏判、整帧塞 `{raw: …}` 前端不可见，均修正为契约形状（output 取块内 text 块拼接、截 2000；无文本块则省略）。
- 附带修正：审批 risk 位误传 `frame.ToolName`（kimiapp 同款 bug 的 dsh 残留），统一固定 `"high"`——dsh 网关不提供风险分级，工具审批一律按 high 走人工确认；question/requested 映射为审批的行为不变。
