package application_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCoordinatorPlanTimerEnqueueFailurePersistsDueRecovery(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, _ := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "定时 Plan 恢复", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER plan_timer_injected_failure
BEFORE INSERT ON agent_wakeup_requests
WHEN NEW.source = 'automation' AND NEW.context LIKE '%defer_wake_at%'
BEGIN SELECT RAISE(ABORT, 'injected plan timer enqueue failure'); END`); err != nil {
		t.Fatal(err)
	}
	wakeAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	planText := fmt.Sprintf(`{"schema_version":"plan-decision/v2","kind":"plan","reason":"wait until timer","next_action":"resume at the wake time","steps":[{"verb":"defer","wake_at":%q}]}`, wakeAt)
	if err := svc.RecordRunEvent(ctx, coordinatorRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": planText}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorWaitingRetry || coordinatorControlActionForTest(state) != "plan_timer" || state.NextActionAt == nil {
		t.Fatalf("timer enqueue failure must persist a due Coordinator checkpoint: %+v", state)
	}
	dispatch, err := store.Dispatches().Get(ctx, coordinatorRun.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Status != domain.DispatchCompleted {
		t.Fatalf("timer recovery checkpoint must not prevent source lead-only dispatch closure: %+v", dispatch)
	}
	if _, err := db.Exec(`DROP TRIGGER plan_timer_injected_failure`); err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	due := time.Now().UTC().Add(-time.Second)
	state.NextActionAt = &due
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResumeDueTaskCoordinators(ctx, wsID, 10); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("due timer checkpoint should start a recovery Coordinator Run: %d", len(dispatcher.runs))
	}
	control, _ := dispatcher.runs[1].Input["task_coordinator"].(map[string]any)
	if control["action"] != "plan_timer" {
		t.Fatalf("recovery Run must retain timer action: %#v", control)
	}
}
