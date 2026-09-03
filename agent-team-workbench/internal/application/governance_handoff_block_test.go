package application_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCreateHandoffRejectsSameResolvedAgent(t *testing.T) {
	ctx, svc, store, _, workspaceID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "reject self handoff", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"handoff must change ownership"},
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
	_, err = svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Reason: "self handoff", ContextSummary: "must be rejected", Actor: domain.GovernanceActorRef{
			Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID,
		},
	})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("same resolved source/target Agent must be rejected: %v", err)
	}
}

func TestBlockingDelegatedCoordinatorClearsHandoffCheckpoint(t *testing.T) {
	ctx, svc, store, _, workspaceID, targetID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "clear delegated blocker", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"blocked delegated state can return to system Coordinator"},
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
		Reason: "target owns the continuation", ContextSummary: "preserve target identity until a blocker",
		Actor:     domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		ClientKey: "block-clear-handoff",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "accepted"); err != nil {
		t.Fatal(err)
	}
	blockedRoot, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "operator_blocked", Message: "operator requested review", Source: "test",
	}, blockedRoot.Version); err != nil {
		t.Fatal(err)
	}
	blockedState, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedState.Data["handoff_id"] != nil || blockedState.Data["handoff_target_agent_id"] != nil ||
		blockedState.Data["handoff_source_run_id"] != nil || blockedState.Data["control_action"] == "handoff_continuation" {
		t.Fatalf("blocking must clear active Handoff checkpoint: %+v", blockedState.Data)
	}
	persistedHandoff, err := store.Handoffs().Get(ctx, handoff.ID)
	if err != nil || persistedHandoff.Status != domain.HandoffTransferred {
		t.Fatalf("blocking must preserve transferred Handoff as history: handoff=%+v err=%v", persistedHandoff, err)
	}
	blockedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedTodo.Status != domain.TodoBlocked || blockedTodo.Claim != nil {
		t.Fatalf("blocking must release target claim with the Todo blocker: %+v", blockedTodo)
	}
	unblocked, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UnblockWorkItem(ctx, root.ID, unblocked.Version); err != nil {
		t.Fatal(err)
	}
	stateAfter, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stateAfter.Data["handoff_id"] != nil || stateAfter.CurrentAgentID == targetID {
		t.Fatalf("unblock must resume the system Coordinator after delegated blocker: %+v", stateAfter)
	}
}

func TestAcceptedHandoffFencesLateSourcePlanDecision(t *testing.T) {
	ctx, svc, store, dispatcher, workspaceID, targetID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "fence late source plan", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a late source decision cannot double execute"},
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
		Reason: "target owns the continuation", ContextSummary: "late source output is fenced",
		Actor:     domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		ClientKey: "late-source-plan",
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
	raw, err := json.Marshal(compilerDecision(domain.PlanVerbDispatch, targetID))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(raw)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, source.ID, domain.RunFailed, map[string]any{
		"code": "handoff_source_failed", "message": "source relinquished", "retryable": false,
	}); err != nil {
		t.Fatal(err)
	}
	plan, planErr := store.Plans().LatestByWorkItem(ctx, root.ID)
	if plan != nil || planErr != nil {
		t.Fatalf("late source PlanDecision must be fenced: plan=%+v err=%v", plan, planErr)
	}
	if len(dispatcher.runs) != 2 || dispatcher.runs[1].AgentProfileID != targetID {
		t.Fatalf("exactly one delegated continuation should be created: runs=%+v", dispatcher.runs)
	}
}

func TestDelegatedHandoffCanTransferAgainWithoutCloningUnrelatedHistory(t *testing.T) {
	ctx, svc, store, dispatcher, workspaceID, firstTargetID := seedCoordinatorEnv(t)
	secondTargetID := "agent_handoff_second_target"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: secondTargetID, WorkspaceID: workspaceID, Name: "Second target", Role: "worker",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "two-hop Handoff", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"each accepted Handoff has one exact continuation"},
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
	claimed, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, todo.Version, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: firstTargetID},
		Reason: "first specialist", ContextSummary: "first bounded continuation",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, first.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: firstTargetID}, "first accepts"); err != nil {
		t.Fatal(err)
	}
	systemSource := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, systemSource.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunStatus(ctx, systemSource.ID, domain.RunFailed, map[string]any{
		"code": "handoff_source_failed", "message": "first target takes over", "retryable": false,
	}); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 || dispatcher.runs[1].AgentProfileID != firstTargetID {
		t.Fatalf("first Handoff must create its exact continuation: %+v", dispatcher.runs)
	}
	firstContinuation := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, firstContinuation.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	currentTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: currentTodo.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: firstTargetID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: secondTargetID},
		Reason: "second specialist", ContextSummary: "transfer the current continuation, not an old Run",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: firstTargetID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, second.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: secondTargetID}, "second accepts"); err != nil {
		t.Fatal(err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Data["handoff_id"] != second.ID || state.Data["handoff_source_run_id"] != firstContinuation.ID {
		t.Fatalf("second Handoff must replace the checkpoint with the exact current source Run: %+v", state.Data)
	}
	raw, err := json.Marshal(compilerDecision(domain.PlanVerbDispatch, secondTargetID))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, firstContinuation.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(raw)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, firstContinuation.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if plan, planErr := store.Plans().LatestByWorkItem(ctx, root.ID); planErr != nil || plan != nil {
		t.Fatalf("relinquished first target decision must remain evidence only: plan=%+v err=%v", plan, planErr)
	}
	if len(dispatcher.runs) != 3 || dispatcher.runs[2].AgentProfileID != secondTargetID {
		t.Fatalf("second Handoff must create exactly one new target continuation: %+v", dispatcher.runs)
	}
	control, _ := dispatcher.runs[2].Input["task_coordinator"].(map[string]any)
	if control["handoff_id"] != second.ID || control["handoff_source_run_id"] != firstContinuation.ID {
		t.Fatalf("second continuation must carry the replacement Handoff proof: %#v", control)
	}
}

func TestAcceptedHandoffFencesLateEvaluationVerdict(t *testing.T) {
	ctx, svc, store, dispatcher, workspaceID, targetID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "fence late evaluation", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"relinquished evaluation cannot advance acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	decision := compilerDecision(domain.PlanVerbFinish, targetID)
	evaluate := true
	decision.Steps[0].Finish.Evaluation = &evaluate
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(raw)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("finish evaluation must create one evaluation Run: %+v", dispatcher.runs)
	}
	evaluation := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, evaluation.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Claim == nil {
		t.Fatal("evaluation Turn must retain the source governance claim")
	}
	handoff, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: todo.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: todo.Claim.OwnerAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
		Reason: "target owns post-evaluation control", ContextSummary: "late verdict remains transcript evidence",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: todo.Claim.OwnerAgentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "target accepts"); err != nil {
		t.Fatal(err)
	}
	before, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, evaluation.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": "```verdict\n{\"pass\":true,\"reasons\":[\"late\"]}\n```"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, evaluation.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	after, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.WorkItemInProgress || after.Phase == domain.PhaseAcceptance {
		t.Fatalf("late relinquished verdict must not advance Task acceptance: before=%+v after=%+v", before, after)
	}
	if len(dispatcher.runs) != 3 || dispatcher.runs[2].AgentProfileID != targetID {
		t.Fatalf("evaluation Handoff must create exactly one target continuation: %+v", dispatcher.runs)
	}
}
