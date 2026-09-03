package main

// Run Journal dispatch 相位（host_local 路径）的埋点回归：
// chainDispatcher 路由决策点恰发一对 run.phase_entered/run.phase_closed
// （internal 事件，只落 run_events，不进 stream_events）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/observability"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	"github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/sse"
)

// stubModule 立即成功的最小 AdapterModule：本测试只关心 chainDispatcher →
// ModuleRunner 的 dispatch 交接与 journal 事件，不关心 adapter 执行本身。
type stubModule struct{}

func (stubModule) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{}, nil
}

func (stubModule) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	return runtime.ProbeResult{}, nil
}

func (stubModule) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	return runtime.ExecResult{Outcome: runtime.OutcomeSucceeded}
}

// openDispatcherJournalDB 起一个全量迁移的临时库（单连接，贴近 SQLite 单写者部署）。
func openDispatcherJournalDB(t *testing.T) (*sql.DB, *sqlstore.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "dispatch-journal.db")+
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	return db, sqlstore.New(db)
}

// seedHostLocalRun 直插 workspace / work item / run / 本机快照，构造一条
// host_local 可分派 Run 的最小持久前置。
func seedHostLocalRun(t *testing.T, db *sql.DB, store *sqlstore.Store, runID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := db.Exec(
		`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at)
		 VALUES ('ws_rj','rj','UTC',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecutionHosts().EnsureLocalHost(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkspaceLocations().Create(ctx, &domain.WorkspaceLocation{
		ID: "wsloc_rj", WorkspaceID: "ws_rj", ExecutionHostID: domain.LocalHostID,
		MountAlias: "default", RepositoryIdentity: "repo/test",
		Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO work_items(id, workspace_id, record_kind, title, status, priority, version, created_at, updated_at)
		 VALUES ('wi_rj','ws_rj','task','t','todo','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO execution_runs(id, workspace_id, work_item_id, adapter_id, status, version, created_at, updated_at)
		 VALUES (?,'ws_rj','wi_rj','mock','queued',1,?,?)`, runID, now, now); err != nil {
		t.Fatal(err)
	}
	s := &domain.ExecutionContextSnapshot{
		ID: domain.NewID(domain.PrefixCtxSnapshot), RunID: runID,
		SchemaVersion: domain.SnapshotSchemaV1, WorkspaceID: "ws_rj",
		WorkspaceLocationID: "wsloc_rj", LocationVersion: 1,
		MountGeneration: "gen-1", ExecutionHostID: domain.LocalHostID, MountAlias: "default",
		RepositoryIdentity: "repo/test", RefKind: domain.RefBranch,
		BranchName: "main", CheckoutRef: "main", ContextGeneration: 1,
		Source: domain.SnapshotSourceCurrent, CreatedAt: now,
	}
	s.SnapshotDigest = s.ComputeDigest()
	if err := store.ContextSnapshots().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
}

// newJournalDispatcher 装配只服务 host_local 的 chainDispatcher；registerModule
// 控制是否注册本地执行面（失败用例用空 ModuleRunner 触发路由失败）。远程分支
// 不在本测试触面内，gw 留空。
func newJournalDispatcher(t *testing.T, db *sql.DB, registerModule bool) *chainDispatcher {
	t.Helper()
	store := sqlstore.New(db)
	svc := application.NewService(store, nil, sse.NewHub(), runtime.NewRegistry())
	modules := runtime.NewModuleRunner(svc)
	if registerModule {
		modules.Register("mock", stubModule{})
	}
	return &chainDispatcher{modules: modules, store: store, svc: svc,
		journal: observability.NewJournal(svc.RecordRunEvent)}
}

// dispatchJournalEvents 抽出 run 的 dispatch 相位事件（run_seq 即追加序）。
func dispatchJournalEvents(t *testing.T, store *sqlstore.Store, runID string) []application.RunEvent {
	t.Helper()
	events, err := store.Events().ListRunEventsIncludeInternal(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var journal []application.RunEvent
	for _, ev := range events {
		if ev.EventType == domain.EventRunPhaseEntered || ev.EventType == domain.EventRunPhaseClosed {
			journal = append(journal, ev)
		}
	}
	return journal
}

// assertStreamEventsEmpty 佐证 internal 分流端到端成立：phase 事件绝不落 stream_events。
func assertStreamEventsEmpty(t *testing.T, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM stream_events WHERE event_type IN (?,?)`,
		domain.EventRunPhaseEntered, domain.EventRunPhaseClosed).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("internal phase 事件泄漏进 stream_events：%d 行", n)
	}
}

// host_local 路由成功：entered/closed 恰好一对，detail 字段齐全（host_id/runtime/
// adapter、route），closed 落 ok。
func TestChainDispatcherHostLocalJournalPairOK(t *testing.T) {
	ctx := context.Background()
	db, store := openDispatcherJournalDB(t)
	seedHostLocalRun(t, db, store, "run_rj_ok")
	c := newJournalDispatcher(t, db, true)

	run, err := store.Runs().Get(ctx, "run_rj_ok")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Dispatch(ctx, run); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	journal := dispatchJournalEvents(t, store, "run_rj_ok")
	if len(journal) != 2 {
		t.Fatalf("dispatch 相位应恰有一对事件，实际 %d：%+v", len(journal), journal)
	}
	entered, closed := journal[0], journal[1]
	if entered.EventType != domain.EventRunPhaseEntered || closed.EventType != domain.EventRunPhaseClosed {
		t.Fatalf("事件顺序应为 entered→closed：%s → %s", entered.EventType, closed.EventType)
	}
	if entered.Payload["phase"] != observability.PhaseDispatch || entered.Payload["attempt"] != float64(1) {
		t.Fatalf("entered 载荷缺 phase/attempt: %+v", entered.Payload)
	}
	if entered.Payload["host_id"] != domain.LocalHostID || entered.Payload["adapter"] != "mock" {
		t.Fatalf("entered detail 缺 host_id/adapter: %+v", entered.Payload)
	}
	if runtimeLabel, ok := entered.Payload["runtime"].(string); !ok {
		t.Fatalf("entered detail 缺 runtime: %+v", entered.Payload)
	} else if runtimeLabel != "" {
		t.Fatalf("无 RuntimeLabel 的 run 应落空 runtime: %+v", entered.Payload)
	}
	if closed.Payload["phase"] != observability.PhaseDispatch || closed.Payload["outcome"] != string(observability.PhaseOK) {
		t.Fatalf("closed 载荷缺 phase/outcome: %+v", closed.Payload)
	}
	if closed.Payload["route"] != "host_local" {
		t.Fatalf("closed detail 应含 route=host_local: %+v", closed.Payload)
	}
	if _, ok := closed.Payload["duration_ms"]; !ok {
		t.Fatalf("closed 载荷应含 duration_ms: %+v", closed.Payload)
	}
	assertStreamEventsEmpty(t, db)
}

// host_local 路由失败（adapter 未注册本地执行面）：entered/closed 恰好一对，
// closed 落 failed 且 failure.code 与既有 dispatch 终态语义（dispatch_failed）对齐。
func TestChainDispatcherHostLocalJournalPairFailed(t *testing.T) {
	ctx := context.Background()
	db, store := openDispatcherJournalDB(t)
	seedHostLocalRun(t, db, store, "run_rj_fail")
	c := newJournalDispatcher(t, db, false)

	run, err := store.Runs().Get(ctx, "run_rj_fail")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Dispatch(ctx, run); err == nil {
		t.Fatal("adapter 未注册本地执行面必须报错")
	}

	journal := dispatchJournalEvents(t, store, "run_rj_fail")
	if len(journal) != 2 {
		t.Fatalf("失败路径也应恰有一对事件，实际 %d：%+v", len(journal), journal)
	}
	entered, closed := journal[0], journal[1]
	if entered.EventType != domain.EventRunPhaseEntered || closed.EventType != domain.EventRunPhaseClosed {
		t.Fatalf("事件顺序应为 entered→closed：%s → %s", entered.EventType, closed.EventType)
	}
	if closed.Payload["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("失败路径 closed 应落 failed: %+v", closed.Payload)
	}
	if closed.Payload["route"] != "host_local" {
		t.Fatalf("失败 closed detail 也应含 route: %+v", closed.Payload)
	}
	failure, ok := closed.Payload["failure"].(map[string]any)
	if !ok {
		t.Fatalf("failed closed 应含 failure: %+v", closed.Payload)
	}
	if failure["code"] != "dispatch_failed" || failure["retryable"] != true {
		t.Fatalf("failure 分类应与既有 dispatch_failed 语义对齐: %+v", failure)
	}
	if msg, _ := failure["message"].(string); msg == "" {
		t.Fatalf("failure.message 应携带原因: %+v", failure)
	}
	assertStreamEventsEmpty(t, db)
}
