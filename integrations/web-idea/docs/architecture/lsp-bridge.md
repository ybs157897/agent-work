# LSP 桥接设计

Status: accepted

Date: 2026-08-30

## 1. 目标

把浏览器中的 Monaco Language Client 接到 **每工作区一个** 的 Eclipse JDT LS 进程，支持：

- `textDocument/didOpen|didChange|didClose`（只读场景可简化为 didOpen + 可选 didClose）
- `textDocument/definition`
- `textDocument/references`
- `textDocument/hover`（建议 MVP 包含，成本低）
- `initialize` / `initialized` / `shutdown`

## 2. 传输

```text
Monaco Language Client
  -- WSS (vscode-ws-jsonrpc 兼容帧) -->
Gateway lspproxy
  -- LSP over stdio (Content-Length headers) -->
eclipse.jdt.ls
```

依据：

- [monaco-languageclient](https://github.com/TypeFox/monaco-languageclient) 生态默认 WSS；
- jdtls 官方主路径为 stdio；Gateway 做协议换档，避免浏览器直连 Java 进程。

见 [ADR-003](../decisions/ADR-003-three-process-architecture.md)。

## 3. 会话绑定

1. 客户端先 `POST /workspaces` 取得 `id`；
2. Gateway 确保该 id 的 jdtls 处于启动中或已运行；
3. 客户端连接 `GET /workspaces/{id}/lsp`（WebSocket）；
4. **一工作区同时只接受一条主 LSP 连接**（第二条连接拒绝或踢旧——实现时二选一写死，默认拒绝）；
5. 连接关闭后：Gateway 停止该工作区的 jdtls 并释放代理连接；删除工作区时执行同一清理路径。

## 4. 工作区根与 URI

| 概念 | 约定 |
|------|------|
| 客户端 document URI | `webidea://ws/{workspaceId}/{relative/path}`；Gateway 拒绝客户端宿主机 `file://` URI |
| Gateway → jdtls | 映射为真实 `file:///abs/root/relative/path` |
| 返回 Location | Gateway **回写** 为客户端 URI 方案，剥掉绝对盘符 |

负向保证：浏览器永不收到宿主机绝对路径（日志除外，日志需脱敏）。

## 5. initialize 要点

Gateway 在转发或代发 `initialize` 时保证：

- `rootUri` / `workspaceFolders` 指向该工作区真实 root；
- `capabilities` 与只读客户端匹配（不宣称随意 workspace edit，除非未来开放写入）；
- jdtls `initializationOptions` 按 Red Hat / jdtls 惯例传递（Maven/Gradle 导入等）；细节钉在实现期 `contracts/lsp.md`。

## 6. `jdt://` 与依赖源码

jdtls 对 jar 内类型常返回 `jdt://` URI。

| 阶段 | 行为 |
|------|------|
| MVP (P2) | UI 提示「外部依赖，暂不打开」；Definition 若仅命中 jdt:// 则降级文案 |
| P3 | 经 jdtls 扩展请求取反编译文本，Gateway 提供只读虚拟文件 API |

不在 MVP 实现 FernFlower 旁路，避免双通道。

## 7. 背压与超时

- 单帧 body 大小上限为 16 MiB，超限 frame 在分配 body 缓冲区前拒绝；
- LSP 请求默认超时（建议 30s，definition/references 可单独配置）；
- jdtls 无响应：标记 workspace `failed`，断开 WSS，保留 FS。

## 8. 安全

- WSS 必须带与 HTTP API 相同的会话凭证（query 短时 ticket 或子协议头；**禁止** 长期 token 进 query 写进日志）；
- 代理层校验所有 `textDocument` URI 映射后仍落在 root 内，并拒绝 `..`、空段和出界符号链接；jdtls 返回的工作区外 `file://` 不向浏览器透传。

## 9. 参考实现锚点

- TypeFox `monaco-languageclient` 的 `eclipse.jdt.ls` example（Docker + WSS）；
- 本仓库实现可参考其帧格式，但 **生命周期与鉴权必须走 Gateway**，不照搬示例的无鉴权 Node 服务为生产形态。
