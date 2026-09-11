package sqlstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// seedQuestionRun 直插一条 run（不含 agent/lease，最小外键面）。
func seedQuestionRun(t *testing.T, store *sqlstore.Store, id, workItemID string, status domain.RunStatus) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.Runs().Create(context.Background(), &domain.ExecutionRun{
		ID: id, WorkspaceID: "ws_wk", WorkItemID: workItemID,
		Status: status, Input: map[string]any{}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// seedPendingQuestion 直插一条 pending 提问（Validate 在 Create 内强制约束）。
func seedPendingQuestion(t *testing.T, store *sqlstore.Store, id, runID, workItemID string) {
	t.Helper()
	q := &domain.QuestionRequest{
		ID: id, RunID: runID, WorkItemID: workItemID, SessionRef: "session_" + id,
		ProviderID: "provider_" + id, AgentID: "main", TurnID: 1,
		Questions: []domain.QuestionItem{{ID: "q_0", Question: "继续？", Options: []domain.QuestionOption{
			{ID: "opt_0", Label: "是"}, {ID: "opt_1", Label: "否"},
		}}},
		Status: domain.QuestionPending, CreatedAt: time.Now().UTC(),
	}
	if err := store.Questions().Create(context.Background(), q); err != nil {
		t.Fatal(err)
	}
}

// TestQuestionRepoExpirePendingByRun：批量收敛只命中目标 run 的 pending 行并写
// resolved_at；幂等（第二次命中 0 行）；活跃 run 的提问与非 pending 状态不受影响。
func TestQuestionRepoExpirePendingByRun(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "questions.db")+
		"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	insertWorkItem(t, db, "wi_q")

	seedQuestionRun(t, store, "run_active", "wi_q", domain.RunRunning)
	seedQuestionRun(t, store, "run_failed", "wi_q", domain.RunFailed)
	seedQuestionRun(t, store, "run_empty", "wi_q", domain.RunRunning)
	seedPendingQuestion(t, store, "q_active", "run_active", "wi_q")
	seedPendingQuestion(t, store, "q_stale", "run_failed", "wi_q")
	seedPendingQuestion(t, store, "q_stale2", "run_failed", "wi_q")
	// 终态 run 上已 answered 的提问不属于收敛面。
	answered := &domain.QuestionRequest{
		ID: "q_answered", RunID: "run_failed", WorkItemID: "wi_q", SessionRef: "session_qa",
		ProviderID: "provider_qa", AgentID: "main", TurnID: 1,
		Questions: []domain.QuestionItem{{ID: "q_0", Question: "颜色？", Options: []domain.QuestionOption{
			{ID: "opt_0", Label: "蓝"}, {ID: "opt_1", Label: "绿"},
		}}},
		Status: domain.QuestionAnswered, CreatedAt: time.Now().UTC(),
	}
	if err := store.Questions().Create(ctx, answered); err != nil {
		t.Fatal(err)
	}

	// 现有隐藏语义：终态 run 的 pending 提问在 ListPending 中不可见。
	hidden, err := store.Questions().ListPending(ctx, "run_failed")
	if err != nil || len(hidden) != 0 {
		t.Fatalf("ListPending(run_failed) 应被终态过滤隐藏: n=%d err=%v", len(hidden), err)
	}
	// 收敛路径的读取：不看 run 状态。
	pending, err := store.Questions().ListPendingByRun(ctx, "run_failed")
	if err != nil || len(pending) != 2 {
		t.Fatalf("ListPendingByRun(run_failed) = %d err=%v, 期望 2", len(pending), err)
	}
	stale, err := store.Questions().ListStalePending(ctx)
	if err != nil || len(stale) != 2 {
		t.Fatalf("ListStalePending = %d err=%v, 期望 2（仅终态 run 名下 pending）", len(stale), err)
	}

	now := time.Now().UTC()
	n, err := store.Questions().ExpirePendingByRun(ctx, "run_failed", now)
	if err != nil || n != 2 {
		t.Fatalf("ExpirePendingByRun = %d err=%v, 期望 2", n, err)
	}
	// 幂等：重复执行命中 0 行。
	if n, err := store.Questions().ExpirePendingByRun(ctx, "run_failed", now); err != nil || n != 0 {
		t.Fatalf("重复 ExpirePendingByRun = %d err=%v, 期望 0/nil", n, err)
	}
	// 无 pending 行的 run：0 行、不报错。注意本方法按 (run_id, status=pending)
	// 作用域更新、不自判 run 终态；「只在终态收敛」的守卫在服务层
	//（transitionRunLocked / ReconcileStaleQuestions）。
	if n, err := store.Questions().ExpirePendingByRun(ctx, "run_empty", now); err != nil || n != 0 {
		t.Fatalf("ExpirePendingByRun(run_empty) = %d err=%v, 期望 0/nil", n, err)
	}

	q, err := store.Questions().Get(ctx, "q_stale")
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != domain.QuestionExpired || q.ResolvedAt == nil {
		t.Fatalf("q_stale 应为 expired 且带 resolved_at: status=%s resolved_at=%v", q.Status, q.ResolvedAt)
	}
	qa, err := store.Questions().Get(ctx, "q_answered")
	if err != nil || qa.Status != domain.QuestionAnswered {
		t.Fatalf("answered 提问不应被动: status=%s err=%v", qa.Status, err)
	}
	still, err := store.Questions().ListPending(ctx, "run_active")
	if err != nil || len(still) != 1 || still[0].ID != "q_active" {
		t.Fatalf("活跃 run 的 pending 提问不受影响: n=%d err=%v", len(still), err)
	}
}
