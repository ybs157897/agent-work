package application_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestSystemCoordinatorDecisionNeverFallsBackWhenGovernanceStateIsMissing(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, _ := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "missing governance decision", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"system decisions always stay governed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goals SET current_todo_id=NULL WHERE id=?`, goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM goal_todos WHERE goal_id=?`, goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM goals WHERE id=?`, goal.ID); err != nil {
		t.Fatal(err)
	}

	decision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"must remain governed","next_action":"finish","steps":[{"verb":"finish"}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, decision)
	if plan, err := store.Plans().LatestByWorkItem(ctx, root.ID); plan != nil ||
		(err != nil && !errors.Is(err, domain.ErrNotFound)) {
		t.Fatalf("missing governance must not fall back to an ordinary Plan: plan=%+v err=%v", plan, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || state.BlockerCode != "governance_state_unavailable" ||
		root.Status != domain.WorkItemBlocked {
		t.Fatalf("missing governance must fail closed at the root control line: root=%+v state=%+v", root, state)
	}
}

func TestGovernanceConsistencyReportsBlockedProjectionDrift(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "detect blocked projection drift", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"blocked projections remain aligned"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "consistency_probe", Message: "block before corrupting projection", Source: "test",
	}, root.Version); err != nil {
		t.Fatal(err)
	}
	if issue, inconsistent, err := svc.CheckGovernanceConsistency(ctx, root.ID); err != nil || inconsistent {
		t.Fatalf("fresh atomic blocker must be consistent: issue=%+v inconsistent=%v err=%v", issue, inconsistent, err)
	}

	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := goal.Version
	goal.Phase = "execution"
	if err := goal.Resume(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Update(ctx, goal, expected); err != nil {
		t.Fatal(err)
	}
	issue, inconsistent, err := svc.CheckGovernanceConsistency(ctx, root.ID)
	if err != nil || !inconsistent || issue.Code != "blocked_state_inconsistent" ||
		!strings.Contains(issue.Message, "root Task") {
		t.Fatalf("root/Goal blocked drift was not reported: issue=%+v inconsistent=%v err=%v", issue, inconsistent, err)
	}

	expected = goal.Version
	goal.Phase = "blocked"
	if err := goal.Transition(domain.GoalBlocked, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Update(ctx, goal, expected); err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	expected = todo.Version
	if err := todo.Transition(domain.TodoPending, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Todos().Update(ctx, todo, expected); err != nil {
		t.Fatal(err)
	}
	issue, inconsistent, err = svc.CheckGovernanceConsistency(ctx, root.ID)
	if err != nil || !inconsistent || issue.Code != "blocked_state_inconsistent" ||
		!strings.Contains(issue.Message, "current Todo") {
		t.Fatalf("Goal/Todo blocked drift was not reported: issue=%+v inconsistent=%v err=%v", issue, inconsistent, err)
	}
}

func TestStartCoordinatorFailsClosedForLegacyRootWithoutGovernanceContract(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, _ := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "legacy root without governance contract", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"seed a valid native Goal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE goals SET current_todo_id=NULL WHERE id=?`, goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM goal_todos WHERE goal_id=?`, goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM goals WHERE id=?`, goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE work_items SET acceptance_criteria='[]' WHERE id=?`, root.ID); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorQueued
	state.Phase = "recovering"
	state.CurrentRunID = ""
	state.CurrentAction = "recover"
	state.NextActionAt = nil
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}

	startErr := svc.StartCoordinator(ctx, root.ID)
	if startErr == nil || !strings.Contains(startErr.Error(), "acceptance_contract_missing") {
		t.Fatalf("legacy root without a governance contract must fail closed: %v", startErr)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("governance initialization failure created a legacy Coordinator Run: %d", len(dispatcher.runs))
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || state.BlockerCode != "governance_state_unavailable" ||
		root.Status != domain.WorkItemBlocked {
		t.Fatalf("governance initialization failure must block the control line: root=%+v state=%+v", root, state)
	}
}
