package application_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// seedReconcileFixture 建 workspace + agent + 两个任务，供对账测试挂 run。
func seedReconcileFixture(t *testing.T, ctx context.Context, svc *application.Service, store *sqlstore.Store, db *sql.DB) (agentID, wiA, wiB string) {
	t.Helper()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_rec", Name: "rec", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_rec")
	agent := &domain.AgentProfile{
		ID: "agent_rec", WorkspaceID: ws.ID, Name: "REC", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	wi1, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "A"})
	if err != nil {
		t.Fatal(err)
	}
	wi2, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	return agent.ID, wi1.ID, wi2.ID
}

// insertOrphanRun 直插一条 run（绕过 CreateRun 的事务副作用，模拟上一进程遗留）。
func insertOrphanRun(t *testing.T, store *sqlstore.Store, id, agentID, workItemID string, status domain.RunStatus) {
	t.Helper()
	now := time.Now().UTC().Add(-time.Hour)
	run := &domain.ExecutionRun{
		ID: id, WorkspaceID: "ws_rec", WorkItemID: workItemID, AgentProfileID: agentID,
		Status: status, Version: 1, CreatedAt: now, UpdatedAt: now,
		Input: map[string]any{"instruction": "旧进程指令"},
	}
	if err := store.Runs().Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

// insertLease 直插 runner + lease（runner 路径 run 的标记）。
func insertRunLease(t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO runners(id, workspace_id, label, slots, status, created_at)
		 VALUES ('runner_rec','ws_rec','rec',1,'connected',?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO run_leases(lease_id, run_id, runner_id, fencing_token, acquired_at, renewed_until, released_at)
		 VALUES (?,?,?,1,?,?,NULL)`,
		"lease_"+runID, runID, "runner_rec", now,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func runStatus(t *testing.T, store *sqlstore.Store, id string) domain.RunStatus {
	t.Helper()
	run, err := store.Runs().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return run.Status
}

func hasRunEvent(t *testing.T, store *sqlstore.Store, runID, eventType string) bool {
	t.Helper()
	events, err := store.Events().ListRunEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.EventType == eventType {
			return true
		}
	}
	return false
}

// TestReconcileOrphanRunsMarksLeaselessLost：queued/running 的无 lease run（进程内
// 孤儿）→ lost + run.lost 事件；有 lease 的（runner 路径）与终态的不受影响；
// 对账后该 (agent, task) 的活跃判定解除，wakeup 可穿透重建。
func TestReconcileOrphanRunsMarksLeaselessLost(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	agentID, wiA, wiB := seedReconcileFixture(t, ctx, svc, store, db)

	insertOrphanRun(t, store, "run_orphan_q", agentID, wiA, domain.RunQueued)
	insertOrphanRun(t, store, "run_orphan_r", agentID, wiB, domain.RunRunning)
	insertOrphanRun(t, store, "run_orphan_w", agentID, wiB, domain.RunWaitingApproval)
	insertOrphanRun(t, store, "run_leased", agentID, wiA, domain.RunRunning)
	insertRunLease(t, db, "run_leased")
	insertOrphanRun(t, store, "run_done", agentID, wiB, domain.RunSucceeded)

	marked, err := svc.ReconcileOrphanRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if marked != 3 {
		t.Fatalf("收敛数 = %d, 期望 3（queued/running/waiting_approval 无 lease）", marked)
	}
	if got := runStatus(t, store, "run_orphan_q"); got != domain.RunLost {
		t.Fatalf("queued 孤儿 = %s, 期望 lost", got)
	}
	if got := runStatus(t, store, "run_orphan_r"); got != domain.RunLost {
		t.Fatalf("running 孤儿 = %s, 期望 lost", got)
	}
	if got := runStatus(t, store, "run_orphan_w"); got != domain.RunLost {
		t.Fatalf("waiting_approval 孤儿 = %s, 期望 lost", got)
	}
	if got := runStatus(t, store, "run_leased"); got != domain.RunRunning {
		t.Fatalf("有 lease 的 run 不应被动: %s", got)
	}
	if got := runStatus(t, store, "run_done"); got != domain.RunSucceeded {
		t.Fatalf("终态 run 不应被动: %s", got)
	}
	if !hasRunEvent(t, store, "run_orphan_q", domain.EventRunLost) {
		t.Fatal("run_orphan_q 应有 run.lost 事件")
	}
	if !hasRunEvent(t, store, "run_orphan_r", domain.EventRunLost) {
		t.Fatal("run_orphan_r 应有 run.lost 事件")
	}
	if !hasRunEvent(t, store, "run_orphan_w", domain.EventRunLost) {
		t.Fatal("run_orphan_w 应有 run.lost 事件")
	}
	// 对账动机验证：死 run 不再挡住 wakeup 的活跃判定。
	runID, alive, err := store.Wakeups().ActiveRunKeyForAgentTask(ctx, agentID, wiA)
	if err != nil || runID != "run_leased" || !alive {
		t.Fatalf("wi_A 活跃判定: id=%q alive=%v err=%v（run_leased 有活 lease 应 alive）", runID, alive, err)
	}
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agentID, wiB)
	if err != nil || runID != "" || alive {
		t.Fatalf("wi_B 活跃判定: id=%q alive=%v err=%v（孤儿已 lost 应无活跃 run）", runID, alive, err)
	}
	// 幂等：再次对账无事可做。
	marked, err = svc.ReconcileOrphanRuns(ctx)
	if err != nil || marked != 0 {
		t.Fatalf("二次对账: marked=%d err=%v", marked, err)
	}
}

func TestReconcileLegacyContextRunsPrecedesGenericOrphans(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	agentID, wiA, _ := seedReconcileFixture(t, ctx, svc, store, db)
	insertOrphanRun(t, store, "run_legacy_context", agentID, wiA, domain.RunRunning)

	marked, err := svc.ReconcileLegacyContextRuns(ctx)
	if err != nil || marked != 1 {
		t.Fatalf("legacy context 对账应先收敛 1 条: marked=%d err=%v", marked, err)
	}
	run, err := store.Runs().Get(ctx, "run_legacy_context")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunFailed || run.Failure == nil || run.Failure.Code != "execution_context_missing" {
		t.Fatalf("legacy Run 应落 failed(execution_context_missing): %+v", run)
	}
	if hasRunEvent(t, store, run.ID, domain.EventRunLost) || !hasRunEvent(t, store, run.ID, domain.EventRunFailed) {
		t.Fatalf("legacy Run 不得先被 orphan 路径标记 lost")
	}
	if marked, err := svc.ReconcileOrphanRuns(ctx); err != nil || marked != 0 {
		t.Fatalf("legacy 收敛后 generic orphan 不应重复处理: marked=%d err=%v", marked, err)
	}
}

// TestReconcileOrphanRunsTransitionalStatesConvergeToLost：Runner 断连闭环让
// interrupting/cancelling/succeeding 可经 reconnecting 收敛 lost。启动时无 lease
// 说明没有可恢复执行面，因此统一落 lost/run.lost，并解除活跃判定。
func TestReconcileOrphanRunsTransitionalStatesConvergeToLost(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	agentID, wiA, _ := seedReconcileFixture(t, ctx, svc, store, db)

	insertOrphanRun(t, store, "run_int", agentID, wiA, domain.RunInterrupting)
	insertOrphanRun(t, store, "run_can", agentID, wiA, domain.RunCancelling)
	insertOrphanRun(t, store, "run_suc", agentID, wiA, domain.RunSucceeding)

	if _, err := svc.ReconcileOrphanRuns(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run_int", "run_can", "run_suc"} {
		if got := runStatus(t, store, id); got != domain.RunLost {
			t.Fatalf("%s = %s, 期望 lost", id, got)
		}
		run, err := store.Runs().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Failure != nil {
			t.Fatalf("%s 收敛 lost 不应伪造 failure: %#v", id, run.Failure)
		}
		if !hasRunEvent(t, store, id, domain.EventRunLost) {
			t.Fatalf("%s 应有 run.lost 事件", id)
		}
	}
	runID, alive, err := store.Wakeups().ActiveRunKeyForAgentTask(ctx, agentID, wiA)
	if err != nil || runID != "" || alive {
		t.Fatalf("对账后应无活跃 run: id=%q alive=%v err=%v", runID, alive, err)
	}
}
