# U05 final gates

Date: 2026-09-09
Worktree: `codex/u05-decisions-changes`
Scope: late U05/U04 source, decision, delta-preserve, HTTP contract, persistence and runtime touch points. The known full application suite was not rerun because it exceeds the bounded gate window.

All commands ran under `agent-team-workbench/` and exited 0:

```text
go test ./internal/application -run 'TestChatAnalysis|TestResolveAnalysisCodeDigest|TestChatSource' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/application 15.033s

go test ./internal/httpapi -run 'TestContractGuardRoutesMatchOpenAPI|TestChatAnalysis|TestChatSource' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/httpapi 15.680s

go test ./internal/persistence/sqlstore -run '^$' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/persistence/sqlstore 2.103s [no tests to run]

go build ./...
exit 0

go vet ./internal/application ./internal/persistence/sqlstore ./internal/httpapi ./internal/runtime ./internal/orchestrator
exit 0
```

Frozen-base negative/lineage regressions:

```text
go test ./internal/application -run 'TestMergeBaseAnalysisConversationCatalogRejectsForeignScopeAndDigest|TestChatAnalysisRetryKeepsParentFrozenContextAfterCurrentRevisionChanges' -count=1
ok   github.com/ybs/agent-team-workbench/internal/application 1.649s

go test ./internal/application -run 'TestMergeBaseAnalysisConversationCatalogRejectsForeignScopeAndDigest|TestChatAnalysisRetryKeepsParentFrozenContextAfterCurrentRevisionChanges' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/application 6.657s
```

The first test covers foreign Workspace/Chat/Agent base Run scope and same conversation
ref with a wrong digest. The second creates revision 1, advances current analysis to
revision 2, retries the old Run, and asserts the retry keeps the parent context and
`base_revision` instead of rebuilding from current projection.

After frozen-base retry/self-heal/lost wiring, the final late gate was rerun:

```text
gofmt -w internal/domain/ids.go
git diff --check

go test ./internal/application -run 'TestChatAnalysis|TestChatSource|TestResolveAnalysisCodeDigest' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/application 15.055s

go test ./internal/httpapi -run 'TestContractGuardRoutesMatchOpenAPI|TestChatAnalysis|TestChatSource' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/httpapi 14.961s

go vet ./internal/application ./internal/persistence/sqlstore ./internal/httpapi ./internal/runtime ./internal/orchestrator
exit 0

go build ./...
exit 0
```

No control-plane process was restarted and no commit/merge was performed.

After the base-revision/delta-preserve wiring, the late gate was rerun:

```text
go test ./internal/application -run 'TestChatAnalysis|TestResolveAnalysisCodeDigest|TestChatSource' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/application 15.101s

go test ./internal/httpapi -run 'TestContractGuardRoutesMatchOpenAPI|TestChatAnalysis|TestChatSource' -race -count=1
ok   github.com/ybs/agent-team-workbench/internal/httpapi 14.405s

go build ./...
exit 0

go vet ./internal/application ./internal/persistence/sqlstore ./internal/httpapi ./internal/runtime ./internal/orchestrator
exit 0
```
