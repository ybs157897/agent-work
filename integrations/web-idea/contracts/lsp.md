# LSP 契约备注（实现期钉死）

Status: draft

配套：[docs/architecture/lsp-bridge.md](../docs/architecture/lsp-bridge.md)

## 传输

- 客户端 ↔ Gateway：WebSocket，帧格式兼容 `vscode-ws-jsonrpc`（实现 P2 时用黄金样例锁定）。
- Gateway ↔ jdtls：LSP over stdio（`Content-Length` headers）。

## 客户端 URI 方案（暂定）

```text
webidea://ws/{workspaceId}/{relative/posix/path}
```

Gateway 映射为：

```text
file://{absolute-root}/{relative}
```

响应 Location 必须逆映射。禁止把宿主机绝对路径返回给浏览器。
Gateway 只接受带匹配 workspaceId 的 `webidea://` URI；客户端发送的宿主机
`file://` URI 会被拒绝。jdtls 响应中的 `file://` 只有在当前 Workspace Root
内才逆映射，出界位置会被清空，不向浏览器泄露宿主机路径；`jdt://` 保持不变。
URI 路径拒绝 `..`、`.`、空段、反斜杠和符号链接出界。

## Frame 边界

stdio 的单个 LSP body 最大为 `16 MiB`。Gateway 在分配 body 缓冲区前校验
`Content-Length`，超出上限的 frame 会被拒绝；浏览器发往代理的 WebSocket
消息也受同一上限约束。WebSocket 断开或 Workspace DELETE 后，Gateway 会停止
对应 jdtls 进程并释放代理连接。

## MVP 方法白名单

客户端应使用：

- `initialize` / `initialized` / `shutdown` / `exit`
- `textDocument/didOpen` / `didChange` / `didClose` / `didSave`
- `textDocument/definition`
- `textDocument/references`
- `textDocument/hover`
- `textDocument/completion`（triggerCharacter 含 `.`）

Read-only workspaces additionally enforce this method allowlist at the Gateway.
Unknown methods and mutation methods receive JSON-RPC error `-32001`; file
writes are rejected by the FS API. Malformed, batch, non-object, and invalid
response frames are dropped at this boundary. The allowlist includes the lifecycle,
document synchronization, definition/declaration/typeDefinition/
implementation, references, hover, completion, signature help, document and
workspace symbol queries, semantic tokens, configuration changes, and request
cancellation.

其他方法：Gateway 可转发；UI 不依赖。

## jdt://

P2：不打开，UI 降级。

P3：另开 `GET /api/v1/workspaces/{id}/virtual?uri=` 或 LSP 扩展取内容——届时更新本文与 OpenAPI。
