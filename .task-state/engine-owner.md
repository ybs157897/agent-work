# Knowledge engine owner state

Status: implementing

Worktree: /Users/yin/Documents/ybs/code/agent-work-knowledge-librarian
Branch: codex/knowledge-librarian

## Contract

- SQLite is the sole authority; Markdown stays in immutable version TEXT.
- Read paths require workspace and requester agent identity. Private rows never
  accept an empty or substituted requester.
- Effective publication is a CAS transaction that flips the item pointer,
  version lifecycle, source/relation visibility, FTS projection, and index
  revision together. The projection is rebuildable; reads do not walk files.
- Candidate submissions retain the original multi-change/no-change JSON and
  digest. Same workspace/agent/client key is idempotent; different content is
  a conflict.

## Owned files

- `agent-team-workbench/internal/domain/knowledge.go`
- `agent-team-workbench/internal/application/knowledge_repositories.go`
- `agent-team-workbench/internal/knowledge/engine.go`
- `agent-team-workbench/internal/persistence/sqlstore/knowledge.go`
- `agent-team-workbench/migrations/0044_knowledge_library.sql`

## Progress

- [x] Domain entities, scope, budget, candidate request, coverage and content
  digest.
- [x] Application persistence port and Store/sqlstore registration.
- [x] SQLite schema for immutable versions, evidence, versioned relations,
  submissions, query snapshots, index state and FTS projection.
- [x] Scoped CRUD, idempotent submissions, CAS publication, rebuildable index,
  FTS plus Chinese substring search, bidirectional adjacency, and bounded
  research engine.
- [x] Filtered keyset item listing and append-only item repeal with historical
  reads, reason audit, default-search withdrawal, and index revision bump.
- [x] Focused regression tests for scope, private access, relations, CAS,
  immutable Markdown, idempotency and index state.
- [x] Engine/scoped-retriever regressions for sentinel TopK, depth/node/relation/
  byte budgets, conflict coverage, nil-clock concurrency, and A→B evidence.
- [ ] Parent integration with HTTP/Run Harness and final end-to-end acceptance.

## Validation

Focused evidence now passes: `go test -race ./internal/knowledge -run
'TestEngine|TestScopedRetriever' -count=1`, `go test -race
./internal/persistence/sqlstore -run TestKnowledge -count=1`, and `go test
./internal/application -run TestKnowledge -count=1`. The full application race
run reached its 10-minute test timeout in an existing migration-heavy test
(`TestCancelGoalRunTransitionFailureRollsBackAuthoritativeState`); that timeout
is recorded as a timeout, not a pass.
