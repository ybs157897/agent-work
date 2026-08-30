package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

type failSecondPlanDispatcher struct {
	runs  []*domain.ExecutionRun
	calls int
}

func (d *failSecondPlanDispatcher) Dispatch(_ context.Context, run *domain.ExecutionRun) error {
	d.calls++
	d.runs = append(d.runs, run)
	if d.calls == 2 {
		return errors.New("transient second dispatch failure")
	}
	return nil
}

func TestPlanDispatchFailureDoesNotStrandLaterCommittedRuns(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &failSecondPlanDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "多 Worker 派发"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "A", "执行 A"),
			dispatchStep(workerID, "B", "执行 B"),
			dispatchStep(workerID, "C", "执行 C"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "transient second dispatch failure") {
		t.Fatalf("second dispatch failure should surface after attempting siblings: %v", err)
	}
	if dispatcher.calls != 3 || len(dispatcher.runs) != 3 {
		t.Fatalf("all committed sibling Runs must reach dispatcher, calls=%d runs=%d", dispatcher.calls, len(dispatcher.runs))
	}
	failed, err := store.Runs().Get(ctx, dispatcher.runs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != domain.RunFailed {
		t.Fatalf("failed dispatch must be terminal for recovery, got %s", failed.Status)
	}
}
