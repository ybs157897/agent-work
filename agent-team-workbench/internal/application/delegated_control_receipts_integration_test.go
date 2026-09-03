package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type delegatedCoordinatorFixture struct {
	ctx        context.Context
	svc        *application.Service
	store      application.Store
	dispatcher *captureDispatcher
	root       *domain.WorkItem
	goal       *domain.Goal
	todo       *domain.Todo
	targetID   string
	handoff    *domain.Handoff
	delegated  *domain.ExecutionRun
}

func newDelegatedCoordinatorFixture(t *testing.T) *delegatedCoordinatorFixture {
	t.Helper()
	ctx, svc, store, dispatcher, workspaceID, targetID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "delegated control receipts", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"delegated control outcomes keep target ownership"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
		Reason: "target owns governance", ContextSummary: "target retains claim generation",
		Actor:     domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		ClientKey: "delegated-control-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "accepted"); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunStatus(ctx, source.ID, domain.RunFailed, map[string]any{
		"code": "handoff_source_failed", "message": "source relinquished", "retryable": false,
	}); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("handoff source should create exactly one delegated run: %d", len(dispatcher.runs))
	}
	delegated := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, delegated.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	return &delegatedCoordinatorFixture{ctx: ctx, svc: svc, store: store, dispatcher: dispatcher,
		root: root, goal: goal, todo: claimed, targetID: targetID, handoff: handoff, delegated: delegated}
}

func (f *delegatedCoordinatorFixture) claim(t *testing.T) *domain.Todo {
	t.Helper()
	todo, err := f.store.Todos().Get(f.ctx, f.todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	return todo
}

func TestDelegatedInvalidDecisionRepairKeepsTargetClaim(t *testing.T) {
	f := newDelegatedCoordinatorFixture(t)
	before := f.claim(t)
	if err := f.svc.RecordRunEvent(f.ctx, f.delegated.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": `{"schema_version":`}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := f.svc.RecordRunStatus(f.ctx, f.delegated.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.dispatcher.runs) != 3 {
		t.Fatalf("invalid delegated decision should create one repair Run: %d", len(f.dispatcher.runs))
	}
	repair := f.dispatcher.runs[2]
	if repair.AgentProfileID != f.targetID {
		t.Fatalf("delegated repair must stay on Handoff target: %+v", repair)
	}
	after := f.claim(t)
	if after.Claim == nil || after.Claim.OwnerAgentID != f.targetID || after.ClaimVersion != before.ClaimVersion {
		t.Fatalf("delegated repair must preserve target claim generation: before=%+v after=%+v", before, after)
	}
}

func TestDelegatedEvaluationRejectReplansOnTarget(t *testing.T) {
	f := newDelegatedCoordinatorFixture(t)
	evaluation := true
	decision := &domain.PlanDecisionV2{
		SchemaVersion: "plan-decision/v2", Kind: "plan", Reason: "finish then evaluate",
		NextAction: "evaluate", Steps: []domain.PlanDecisionStepV2{{Verb: domain.PlanVerbFinish,
			Finish: &domain.PlanFinishStepV2{Evaluation: &evaluation}}},
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RecordRunEvent(f.ctx, f.delegated.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(raw)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := f.svc.RecordRunStatus(f.ctx, f.delegated.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.dispatcher.runs) != 3 {
		t.Fatalf("delegated finish evaluation should create one evaluation Run: %d", len(f.dispatcher.runs))
	}
	evaluationRun := f.dispatcher.runs[2]
	if evaluationRun.AgentProfileID != f.targetID {
		t.Fatalf("delegated evaluation must stay on Handoff target: %+v", evaluationRun)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := f.svc.RecordRunStatus(f.ctx, evaluationRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.RecordRunEvent(f.ctx, evaluationRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": "```verdict\n{\"pass\":false,\"reasons\":[\"needs more work\"]}\n```"}); err != nil {
		t.Fatal(err)
	}
	before := f.claim(t)
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := f.svc.RecordRunStatus(f.ctx, evaluationRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.dispatcher.runs) != 4 {
		t.Fatalf("failed delegated evaluation should create one target replan Run: %d", len(f.dispatcher.runs))
	}
	replan := f.dispatcher.runs[3]
	if replan.AgentProfileID != f.targetID {
		t.Fatalf("delegated replan must stay on Handoff target: %+v", replan)
	}
	after := f.claim(t)
	if after.Claim == nil || after.Claim.OwnerAgentID != f.targetID || after.ClaimVersion != before.ClaimVersion {
		t.Fatalf("delegated evaluation replan must preserve target claim generation: before=%+v after=%+v", before, after)
	}
}

func TestDelegatedUserActionKeepsTargetClaim(t *testing.T) {
	f := newDelegatedCoordinatorFixture(t)
	state, err := f.store.TaskCoordinators().GetState(f.ctx, f.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo := f.claim(t)
	beforeVersion, beforeClaimVersion := todo.Version, todo.ClaimVersion
	if err := todo.Transition(domain.TodoBlocked, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Todos().Update(f.ctx, todo, beforeVersion); err != nil {
		t.Fatal(err)
	}
	stateExpected := state.Version
	state.Status = domain.CoordinatorBlocked
	state.Phase = "blocked"
	state.CurrentRunID = ""
	state.CurrentAction = "resolve user action"
	state.BlockerCode = "user_action_required"
	state.BlockerMessage = "user input required"
	state.NextActionAt = nil
	if err := f.store.TaskCoordinators().UpdateState(f.ctx, state, stateExpected); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.svc.ResolveTodoUserAction(f.ctx, application.ResolveTodoUserActionParams{
		TodoID: todo.ID, Resolution: "continue", ActorID: "user_review", ClientKey: "delegated-user-action",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != domain.TodoClaimed || resolved.Claim == nil ||
		resolved.Claim.OwnerAgentID != f.targetID || resolved.ClaimVersion != beforeClaimVersion {
		t.Fatalf("delegated user action must keep target claim generation: before=%+v after=%+v", todo, resolved)
	}
}
