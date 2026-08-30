package application_test

import (
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCoordinatorWakeAtomicallyClaimsControlLineBeforeDispatch(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "atomic wake", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ControlRun(ctx, dispatcher.runs[0].ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorRunning
	state.Phase = "executing"
	state.CurrentRunID = ""
	state.CurrentAction = "等待控制面唤醒"
	state.Data = map[string]any{}
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	wakeContext := map[string]any{"plan_id": "plan_atomic", "trigger": "children_quiet"}
	firstID, err := svc.CreateRunForWakeup(ctx, wsID, state.CoordinatorAgentID, root.ID,
		"first wake", wakeContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRunForWakeup(ctx, wsID, state.CoordinatorAgentID, root.ID,
		"second wake", wakeContext); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("second wake must lose atomic ownership claim: %v", err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("only one wake Run may be dispatched, runs=%d", len(dispatcher.runs))
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentRunID != firstID || state.Status != domain.CoordinatorRunning {
		t.Fatalf("first wake must remain state owner: %+v", state)
	}
}
