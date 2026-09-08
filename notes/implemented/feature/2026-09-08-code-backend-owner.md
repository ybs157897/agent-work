# Developer Code Workspace 后端 Owner 状态

Status: implemented; local acceptance complete; uncommitted task worktree

## 已实现

- 在 `application.CodeWorkspaceService` 中按 `workspace -> Agent -> conversation/Run -> execution snapshot -> HostRegistry` 校验阅读资格。
- 仅允许 `kind=user`、`availability=enabled`、`role=developer`；Run 可为历史终态，必须有不可变 execution snapshot；无 Run 时使用 Workspace 默认 Location。
- 远程 Host 明确拒绝；默认/历史阅读不调用 checkout acquire，不创建 Run 或第二套任务状态。
- 每会话随机 ID、30 分钟 TTL、最多 32 个会话；关闭、过期、Agent 资格撤销和目录身份失效都会释放 Gateway workspace。
- HTTP 层提供同源 frame、bootstrap、只读 Gateway/LSP 代理；仅允许固定上游 workspace 的 GET 阅读端点，拒绝 root/path/cwd、跨 workspace、任意路径和 PUT/写入；浏览器看不到上游 token。
- Gateway restart 会使旧 session 失效并返回 410，不能静默切换到新 Gateway workspace；Gateway 构建/启动失败返回可恢复的 503。
- LSP 代理每 2 秒复核资格和目录身份，撤销后主动断开；closing session 保留失败释放句柄并由 sweeper 重试；服务关闭有 5 秒总 deadline，创建与关闭通过 inflight 闸门协调。
- upstream create 发送随机 client key、恢复 header 与 30 分钟 TTL；web-idea Gateway 需要实现相同 key 的幂等确认及未认领资源 TTL 回收，覆盖响应中断孤儿窗口。
- 所有 code-workspace 管理/读取请求均要求同源 Origin（无 Origin 的本地导航测试允许），拒绝 `Sec-Fetch-Site: cross-site`；真实 Go catch-all 路由由契约门禁规范化器映射到 OpenAPI 参数。
- 两端 WebSocket 均设置 16 MiB ReadLimit；超限会关闭连接并释放 session。

## 验证

- `go test ./internal/application`
- `go test ./internal/httpapi`
- `go test -race ./internal/httpapi -run 'TestCodeWorkspaceHTTPIsDeveloperReadOnlyAndSessionBound|TestContractGuardRoutesMatchOpenAPI' -count=1`
- `go vet ./internal/application ./internal/httpapi`
- `go test ./cmd/control-plane -run '^$' -count=1`
- `go test ./internal/codegateway -count=1`
- `go test -race ./internal/application -run 'TestCodeWorkspaceCloseWaitsInflightCreateAndRejectsLaterCreates|TestCodeWorkspaceSweepRetainsFailedReleaseForRetry' -count=1`
- `go test -race ./internal/httpapi -run 'TestCodeWorkspaceHTTPIsDeveloperReadOnlyAndSessionBound|TestContractGuardRoutesMatchOpenAPI' -count=1`

真实浏览器、Gateway 静态资源和 jdtls 的联合验收由根 Agent 负责。
