# U06 最终触面检查

2026-09-09，全部 exit 0。

- `go build -o ../.agent-work/u06-acceptance/control-plane.next ./cmd/control-plane`
- `go vet ./...`
- `go test -race ./internal/application -run '^TestPublication' -count=1`：24.558s，含实际 Git、WorkspaceProject、首 Run 快照及变更阻塞。
- `go test -race ./internal/httpapi -run 'TestPublishPublicationDraftRetriesSameHTTPKeyAfterInfrastructureFailure|TestContractGuardRoutesMatchOpenAPI' -count=1`：7.000s。
- backend owner：`go test -race ./cmd/migrate -run '^TestTaskPublicationExpectedVersionUpgradeFrom0054$' -count=1` 通过；实际旧0054数据库升级0055并回填原发布version。fresh迁移也通过。
- `pnpm test`：133 files / 1026 tests passed。
- `pnpm lint`、`pnpm tsc -b`、Vite build 通过。
- 71 个累计变更 Go 文件 gofmt 干净，`git diff --check` 通过。

全量go test磁盘不足的试跑未当作通过，未重跑已知耗时的全application race。Go cache仅清理两天以前的可重建产物。
