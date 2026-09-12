package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// startRunWithQuestion 建一个 run，推进到 running 并挂一条原生 pending 提问，
// 返回 (run, question)。此时提问应可从 API 列表读到（run 非终态）。
func startRunWithQuestion(t *testing.T, ctx context.Context, svc *application.Service, agentID, workItemID, tag, sessionRef string) (*domain.ExecutionRun, *domain.QuestionRequest) {
	t.Helper()
	run, err := svc.CreateRun(ctx, workItemID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "提问后落终态 " + tag})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, run.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	q, err := svc.RequestQuestion(ctx, run.ID, domain.QuestionRequest{
		SessionRef: sessionRef, ProviderID: "provider_" + tag, AgentID: "main",
		Questions: []domain.QuestionItem{{ID: "q_0", Question: "继续？", Options: []domain.QuestionOption{
			{ID: "opt_0", Label: "是"}, {ID: "opt_1", Label: "否"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := svc.Questions(ctx, run.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != q.ID {
		t.Fatalf("非终态 run 的 pending 提问应可列出: n=%d err=%v", len(listed), err)
	}
	return run, q
}

// countRunEvents 统计某 run 名下指定类型的事件条数（去重靠事件本身 append-only）。
func countRunEvents(t *testing.T, store *sqlstore.Store, runID, eventType string) int {
	t.Helper()
	events, err := store.Events().ListRunEvents(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range events {
		if ev.EventType == eventType {
			n++
		}
	}
	return n
}

// requireQuestionExpired 断言提问已收敛为 expired（带 resolved_at），API 不再
// 列出，且 run 恰好有一条 question.expired 事件。
func requireQuestionExpired(t *testing.T, store *sqlstore.Store, svc *application.Service, ctx context.Context, runID, questionID string) {
	t.Helper()
	q, err := store.Questions().Get(ctx, questionID)
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != domain.QuestionExpired || q.ResolvedAt == nil {
		t.Fatalf("提问应为 expired 且带 resolved_at: status=%s resolved_at=%v", q.Status, q.ResolvedAt)
	}
	if listed, err := svc.Questions(ctx, runID); err != nil || len(listed) != 0 {
		t.Fatalf("终态 run 的提问不应再出现在列表: n=%d err=%v", len(listed), err)
	}
	if n := countRunEvents(t, store, runID, domain.EventQuestionExpired); n != 1 {
		t.Fatalf("question.expired 事件数 = %d, 期望 1", n)
	}
}

// TestRunTerminalExpiresPendingQuestions 防回归：run 落任一终态时，其名下仍
// pending 的原生提问必须收敛为 expired 并发 question.expired（此前只被
// ListPending 的终态过滤隐藏、数据永不回收，留下永久僵尸行）。
func TestRunTerminalExpiresPendingQuestions(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	agentID, wiA, wiB := seedReconcileFixture(t, ctx, svc, store, db)

	cases := []struct {
		terminal domain.RunStatus
		path     []domain.RunStatus // running → 终态 的合法状态机路径（不含 running 本身）
	}{
		{domain.RunFailed, []domain.RunStatus{domain.RunFailed}},
		{domain.RunCancelled, []domain.RunStatus{domain.RunCancelling, domain.RunCancelled}},
		{domain.RunInterrupted, []domain.RunStatus{domain.RunInterrupting, domain.RunInterrupted}},
		{domain.RunSucceeded, []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded}},
	}
	wis := []string{wiA, wiB, wiA, wiB}
	runIDs := make([]string, 0, len(cases))
	for i, tc := range cases {
		run, q := startRunWithQuestion(t, ctx, svc, agentID, wis[i], "expiry_"+string(tc.terminal), "session_"+string(tc.terminal))
		for _, status := range tc.path {
			var data map[string]any
			if status == domain.RunFailed {
				data = map[string]any{"code": "question_expiry_probe", "message": "终态收敛探针", "retryable": false}
			}
			if err := svc.RecordRunStatus(ctx, run.ID, status, data); err != nil {
				t.Fatalf("%s: %v", tc.terminal, err)
			}
		}
		requireQuestionExpired(t, store, svc, ctx, run.ID, q.ID)
		runIDs = append(runIDs, run.ID)
	}

	// 幂等：已收敛的提问不再产生第二次状态变化或重复事件。
	if n, err := svc.ReconcileStaleQuestions(ctx); err != nil || n != 0 {
		t.Fatalf("终态收敛后再清扫应无事可做: n=%d err=%v", n, err)
	}
	for i, tc := range cases {
		if n := countRunEvents(t, store, runIDs[i], domain.EventQuestionExpired); n != 1 {
			t.Fatalf("%s: 重复清扫后 question.expired 事件数 = %d, 期望 1", tc.terminal, n)
		}
	}
}

// TestNonTerminalRunKeepsQuestionsPending 防回归：running / waiting_approval 的
// run 名下 pending 提问保持 pending、仍可从 API 列出；存量清扫不得误伤。
func TestNonTerminalRunKeepsQuestionsPending(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	agentID, wiA, wiB := seedReconcileFixture(t, ctx, svc, store, db)

	runRunning, qRunning := startRunWithQuestion(t, ctx, svc, agentID, wiA, "keep_running", "session_keep_running")
	runWaiting, qWaiting := startRunWithQuestion(t, ctx, svc, agentID, wiB, "keep_waiting", "session_keep_waiting")
	if err := svc.RecordRunStatus(ctx, runWaiting.ID, domain.RunWaitingApproval, nil); err != nil {
		t.Fatal(err)
	}

	if n, err := svc.ReconcileStaleQuestions(ctx); err != nil || n != 0 {
		t.Fatalf("非终态 run 不应被清扫: n=%d err=%v", n, err)
	}
	for _, q := range []*domain.QuestionRequest{qRunning, qWaiting} {
		got, err := store.Questions().Get(ctx, q.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.QuestionPending {
			t.Fatalf("非终态 run 的提问应保持 pending: %s = %s", q.ID, got.Status)
		}
	}
	for _, run := range []*domain.ExecutionRun{runRunning, runWaiting} {
		listed, err := svc.Questions(ctx, run.ID)
		if err != nil || len(listed) != 1 {
			t.Fatalf("非终态 run 的提问应仍可列出: run=%s n=%d err=%v", run.ID, len(listed), err)
		}
		if n := countRunEvents(t, store, run.ID, domain.EventQuestionExpired); n != 0 {
			t.Fatalf("非终态 run 不应产生 question.expired: run=%s n=%d", run.ID, n)
		}
	}
}

// TestReconcileStaleQuestionsExpiresLegacyZombies 防回归：修复上线前遗留的
// 「run 已终态 + 提问仍 pending」僵尸数据，启动存量清扫后收敛为 expired 并逐条
// 发事件；清扫幂等可重复（第二次无事可做、无重复事件）。
func TestReconcileStaleQuestionsExpiresLegacyZombies(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	agentID, wiA, wiB := seedReconcileFixture(t, ctx, svc, store, db)

	insertOrphanRun(t, store, "run_zombie_failed", agentID, wiA, domain.RunFailed)
	insertOrphanRun(t, store, "run_zombie_lost", agentID, wiB, domain.RunLost)
	insertOrphanRun(t, store, "run_zombie_done", agentID, wiB, domain.RunSucceeded)
	insertQuestion := func(id, runID, workItemID string) {
		t.Helper()
		q := &domain.QuestionRequest{
			ID: id, RunID: runID, WorkItemID: workItemID, SessionRef: "session_" + id,
			ProviderID: "provider_" + id, AgentID: "main", TurnID: 1,
			Questions: []domain.QuestionItem{{ID: "q_0", Question: "继续？", Options: []domain.QuestionOption{
				{ID: "opt_0", Label: "是"}, {ID: "opt_1", Label: "否"},
			}}},
			Status: domain.QuestionPending, CreatedAt: time.Now().UTC(),
		}
		if err := store.Questions().Create(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	insertQuestion("q_zombie_1", "run_zombie_failed", wiA)
	insertQuestion("q_zombie_2", "run_zombie_failed", wiA)
	insertQuestion("q_zombie_3", "run_zombie_lost", wiB)
	insertQuestion("q_zombie_done", "run_zombie_done", wiB)

	expired, err := svc.ReconcileStaleQuestions(ctx)
	if err != nil || expired != 4 {
		t.Fatalf("首次清扫应收敛 4 条: n=%d err=%v", expired, err)
	}
	for _, id := range []string{"q_zombie_1", "q_zombie_2", "q_zombie_3", "q_zombie_done"} {
		q, err := store.Questions().Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if q.Status != domain.QuestionExpired || q.ResolvedAt == nil {
			t.Fatalf("%s 应为 expired 且带 resolved_at: status=%s", id, q.Status)
		}
	}
	// 逐条发事件：run_zombie_failed 名下 2 条提问应有 2 条事件。
	expectedEvents := map[string]int{
		"run_zombie_failed": 2, "run_zombie_lost": 1, "run_zombie_done": 1,
	}
	for runID, want := range expectedEvents {
		if n := countRunEvents(t, store, runID, domain.EventQuestionExpired); n != want {
			t.Fatalf("%s: question.expired 事件数 = %d, 期望 %d", runID, n, want)
		}
	}

	// 幂等可重复：第二次清扫无 stale 行、无重复事件。
	if expired, err = svc.ReconcileStaleQuestions(ctx); err != nil || expired != 0 {
		t.Fatalf("二次清扫应无事可做: n=%d err=%v", expired, err)
	}
	for runID, want := range expectedEvents {
		if n := countRunEvents(t, store, runID, domain.EventQuestionExpired); n != want {
			t.Fatalf("%s: 二次清扫后事件数 = %d, 期望仍为 %d", runID, n, want)
		}
	}
}
