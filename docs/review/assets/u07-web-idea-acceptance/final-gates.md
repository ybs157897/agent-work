# U07 最终检查

2026-09-09，以下均通过。

- Backend legacy route删除：实际HTTP404；160 HTTP routes与OpenAPI一致。
- `go build ./...`、`go vet ./...`。
- `go test -race ./internal/application -run '^(TestCreateWorkspaceWithProjectCopiesSystemConfigurationWithoutBusinessState|TestPublicationRealGitFreezesTaskContextAndFirstRunSnapshot|TestPublicationRealGitDriftBeforeFirstRunLeavesDurableBlockedTask)$' -count=1`：18.278s。
- `go test -race ./internal/httpapi -run '^(TestLegacyTaskIntakeAnalyzeRouteIsRemoved|TestContractGuardRoutesMatchOpenAPI)$' -count=1`：6.286s。
- 前端 `tsc -b`、ESLint、Vite build；最终 `pnpm test`：129 files / 1004 tests passed。
- 73个累计改动Go文件gofmt干净；diff whitespace检查通过。

内置配置回归用隔离SQLite：Source非默认Coordinator preferred/fallback/model/reasoning、Librarian runtime/model和允许的enabled/auto_collect设置克隆；新系统身份独立；保护提示/policy/template使用当前应用默认；EnsureBuiltin/EnsureConfig/新Service reload后保持；目标业务状态表为空。

全application race历史超时与全量测试磁盘不足未算通过。本轮检查选择触面，不声明所有仓库测试通过。
