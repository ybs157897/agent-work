package application_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestHandoffAcceptAtomicallyTransfersTodoClaim(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	targetID := "agent_governance_target"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{ID: targetID, WorkspaceID: workspaceID,
		Name: "Target", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
		Reason: "specialist takes over", ContextSummary: "continue from the durable checkpoint",
		Actor:     domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
		ClientKey: "handoff-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Status != domain.HandoffPending || handoff.SourceClaimVersion != claimed.ClaimVersion {
		t.Fatalf("unexpected pending handoff: %+v", handoff)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "accepted by target"); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("standalone Goal without Coordinator must fail closed on Handoff acceptance: %v", err)
	}
	current, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Claim == nil || current.Claim.OwnerAgentID != "agent_governance_owner" || current.ClaimVersion != claimed.ClaimVersion {
		t.Fatalf("failed standalone Handoff acceptance must retain source claim: %+v", current)
	}
	if _, err := db.Exec(`UPDATE governance_handoffs SET context_summary='tampered' WHERE id=?`, handoff.ID); err == nil {
		t.Fatal("handoff context must be immutable after creation")
	}
	if count := governanceEventCount(t, db, domain.EventHandoffStateChanged); count != 0 {
		t.Fatalf("failed standalone Handoff acceptance must not emit a transfer event: %d", count)
	}
	persisted, err := store.Handoffs().Get(ctx, handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domain.HandoffPending {
		t.Fatalf("failed standalone acceptance must retain pending Handoff: %+v", persisted)
	}
}

func TestHandoffRuntimeTargetRequiresExplicitUniqueAgentMapping(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorRuntime, ID: "runner_missing_mapping"},
		Reason: "runtime handoff", ContextSummary: "no provider transcript",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
	})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("runtime without explicit mapping must fail closed, got %v", err)
	}
	owner, err := store.Agents().Get(ctx, "agent_governance_owner")
	if err != nil {
		t.Fatal(err)
	}
	owner.RuntimePreference = domain.RuntimePreference{Preferred: "mock"}
	owner.Version++
	if err := store.Agents().Update(ctx, owner, owner.Version-1); err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorRuntime, ID: "mock"},
		Reason: "same resolved owner", ContextSummary: "cross-kind aliases must not create an impossible transfer",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
	})
	if !errors.Is(err, domain.ErrStateConflict) && !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("cross-kind source/target resolving to one Agent must fail closed, got %v", err)
	}
}

func TestSystemCoordinatorHandoffAndConcurrentAcceptHaveOneWinner(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "system owner handoff", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"system source can transfer ownership"},
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
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: workerID},
		Reason: "worker owns implementation", ContextSummary: "use the immutable task context",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan *domain.Handoff, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, callErr := svc.AcceptHandoff(ctx, handoff.ID,
				domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: workerID}, "worker accepted")
			results <- result
			errs <- callErr
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if result := <-results; result == nil || result.Status != domain.HandoffTransferred {
			t.Fatalf("concurrent accept result invalid: %+v", result)
		}
	}
	current, err := store.Todos().Get(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Claim == nil || current.Claim.OwnerAgentID != workerID || current.ClaimVersion != claimed.ClaimVersion+1 {
		t.Fatalf("concurrent accept moved claim more than once: %+v", current)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("handoff must not create a Run: %d", len(dispatcher.runs))
	}
}

func TestAcceptedHandoffResumesThroughDelegatedCoordinatorAndCompiler(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	targetID := "agent_handoff_delegated_target"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: targetID, WorkspaceID: wsID, Name: "Handoff target", Role: "worker",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "delegated continuation", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"handoff target continues through Coordinator"},
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
	handoff, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
		Reason: "target specialist continues", ContextSummary: "resume from the durable source snapshot",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "target accepts"); err != nil {
		t.Fatal(err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Data["control_action"] != "handoff_continuation" {
		t.Fatalf("accept must leave a durable handoff continuation checkpoint: %+v", state)
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
		t.Fatalf("source terminal should create exactly one delegated continuation: %d", len(dispatcher.runs))
	}
	delegated := dispatcher.runs[1]
	if delegated.AgentProfileID != targetID || delegated.RetryOf != "" {
		t.Fatalf("delegated continuation identity mismatch: %+v", delegated)
	}
	control, ok := delegated.Input["task_coordinator"].(map[string]any)
	if !ok || control["delegated"] != true || control["handoff_id"] != handoff.ID {
		t.Fatalf("delegated continuation must carry protected Handoff context: %#v", delegated.Input["task_coordinator"])
	}
	if delegated.ContextSnapshotID == "" || delegated.ContextSnapshotID == source.ContextSnapshotID {
		t.Fatalf("delegated continuation must clone a new recovery snapshot: source=%q target=%q", source.ContextSnapshotID, delegated.ContextSnapshotID)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, delegated.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	claimedBeforeRenewal, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	forceExpireTodoClaimFixture(t, db, todo.ID)
	if err := svc.RecordRunEvent(ctx, delegated.ID, domain.EventMessageDelta,
		map[string]any{"role": "assistant", "text": "still producing a bounded decision"}); err != nil {
		t.Fatal(err)
	}
	claimedAfterRenewal, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimedAfterRenewal.ClaimVersion != claimedBeforeRenewal.ClaimVersion ||
		claimedAfterRenewal.Claim == nil || !claimedAfterRenewal.Claim.ExpiresAt.After(time.Now().UTC()) ||
		claimedAfterRenewal.Version <= claimedBeforeRenewal.Version {
		t.Fatalf("delegated active event must renew same claim generation: before=%+v after=%+v", claimedBeforeRenewal, claimedAfterRenewal)
	}
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	text, _ := json.Marshal(decision)
	if err := svc.RecordRunEvent(ctx, delegated.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(text)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, delegated.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("delegated Coordinator Plan should create one Worker Run: %d", len(dispatcher.runs))
	}
	receipts, err := store.TurnReceipts().ListHeadersByGoal(ctx, goal.ID)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("delegated Plan should admit exactly one governed turn: receipts=%d err=%v", len(receipts), err)
	}
}

func TestDelegatedCoordinatorFinishEvaluationCarriesHandoffProof(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	targetID := "agent_handoff_evaluation_target"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: targetID, WorkspaceID: wsID, Name: "Evaluation target", Role: "worker",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "delegated evaluation", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"delegated finish can create evaluation"},
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
	handoff, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
		Reason: "target evaluates the delegated result", ContextSummary: "evaluation remains on the accepted Handoff line",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "target evaluates"); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunFailed} {
		data := map[string]any(nil)
		if status == domain.RunFailed {
			data = map[string]any{"code": "handoff_source_failed", "message": "source relinquished", "retryable": false}
		}
		if err := svc.RecordRunStatus(ctx, source.ID, status, data); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("accepted Handoff must create one delegated Coordinator Run: %d", len(dispatcher.runs))
	}
	delegated := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, delegated.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	evaluate := true
	decision.Steps[0].Finish.Evaluation = &evaluate
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, delegated.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(raw)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, delegated.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("delegated finish evaluation must create one evaluation Run: %d", len(dispatcher.runs))
	}
	evaluation := dispatcher.runs[2]
	control, ok := evaluation.Input["task_coordinator"].(map[string]any)
	if evaluation.AgentProfileID != targetID || !ok || control["delegated"] != true ||
		control["handoff_id"] != handoff.ID ||
		control["source_run_id"] != delegated.ID {
		t.Fatalf("evaluation Run must carry the delegated Handoff proof: %+v", evaluation)
	}
}

func TestBlockingDelegatedRootClearsHandoffCheckpointBeforeUnblock(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, _ := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	now := time.Now().UTC()
	targetID := "agent_handoff_block_target"
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: targetID, WorkspaceID: wsID, Name: "Block target", Role: "worker",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "blocked delegated root", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"unblock returns to a live system checkpoint"},
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
	handoff, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
		Reason: "target owns this bounded turn", ContextSummary: "block is an explicit return to system control",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "target accepts"); err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "user_block", Message: "pause for review", Source: "user",
	}, root.Version); err != nil {
		t.Fatal(err)
	}
	blockedState, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedTodo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := blockedState.Data["handoff_id"]; present || blockedTodo.Claim != nil {
		t.Fatalf("blocking must clear delegated checkpoint and target claim: state=%+v todo=%+v", blockedState, blockedTodo)
	}
	blockedRoot, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	unblocked, err := svc.UnblockWorkItem(ctx, root.ID, blockedRoot.Version)
	if err != nil {
		t.Fatal(err)
	}
	resumedState, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := resumedState.Data["handoff_id"]; present ||
		resumedState.CurrentAgentID != resumedState.CoordinatorAgentID ||
		(unblocked.Status != domain.WorkItemInProgress && unblocked.Status != domain.WorkItemTodo) {
		t.Fatalf("unblock must resume the system Coordinator without stale Handoff: root=%+v state=%+v", unblocked, resumedState)
	}
	if len(dispatcher.runs) < 2 {
		t.Fatalf("unblock should enqueue a fresh system Coordinator Run: runs=%d", len(dispatcher.runs))
	}
}

func TestHandoffRejectCancelKeepSourceClaimAndStaleAcceptFails(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	targetID := "agent_governance_target"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{ID: targetID, WorkspaceID: workspaceID,
		Name: "Target", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	newHandoff := func(key string) *domain.Handoff {
		handoff, createErr := svc.CreateHandoff(ctx, application.CreateHandoffParams{
			GoalID: goal.ID, TodoID: claimed.ID,
			Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
			Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
			Reason: "try transfer", ContextSummary: "durable context", ClientKey: key,
			Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return handoff
	}
	rejected := newHandoff("reject-1")
	if _, err := svc.RejectHandoff(ctx, rejected.ID, "not this owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RejectHandoff(ctx, rejected.ID, "different reason"); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("rejected handoff replay with changed reason must conflict: %v", err)
	}
	cancelled := newHandoff("cancel-1")
	if _, err := svc.CancelHandoff(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	current, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Claim == nil || current.Claim.OwnerAgentID != "agent_governance_owner" {
		t.Fatalf("reject/cancel must keep source claim: %+v", current)
	}
	stale := newHandoff("stale-1")
	if _, err := svc.ReleaseTodo(ctx, todo.ID, "agent_governance_owner", current.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, stale.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "late"); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("stale handoff accept must fail closed: %v", err)
	}
	current, err = store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Claim != nil {
		t.Fatalf("stale accept must not create a target claim: %+v", current)
	}
}

func TestPausedDelegatedHandoffResumeRenewsExpiredTargetClaimWithoutChangingGeneration(t *testing.T) {
	ctx, db, svc, store, _, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	targetID := "agent_handoff_pause_target"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: targetID, WorkspaceID: wsID, Name: "Paused handoff target", Role: "worker",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "pause delegated handoff", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"resume keeps the accepted Handoff target"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
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
	handoff, err := svc.CreateHandoff(ctx, application.CreateHandoffParams{
		GoalID: goal.ID, TodoID: claimed.ID,
		Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
		Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
		Reason: "target remains owner across pause", ContextSummary: "renew same claim generation",
		Actor: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: state.CoordinatorAgentID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptHandoff(ctx, handoff.ID,
		domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID}, "target accepts pause"); err != nil {
		t.Fatal(err)
	}
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	pausedTodo, err := store.Todos().Get(ctx, paused.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if pausedTodo.Status != domain.TodoWaiting || pausedTodo.Claim == nil || pausedTodo.Claim.OwnerAgentID != targetID {
		t.Fatalf("pause must retain the transferred target claim: %+v", pausedTodo)
	}
	claimVersion := pausedTodo.ClaimVersion
	forceExpireTodoClaimFixture(t, db, pausedTodo.ID)
	resumed, err := svc.ResumeGoal(ctx, paused.ID, paused.Version)
	if err != nil {
		t.Fatal(err)
	}
	resumedTodo, err := store.Todos().Get(ctx, resumed.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if resumedTodo.Status != domain.TodoClaimed || resumedTodo.Claim == nil ||
		resumedTodo.Claim.OwnerAgentID != targetID || resumedTodo.ClaimVersion != claimVersion ||
		!resumedTodo.Claim.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("resume must renew the same transferred target claim generation: before=%+v after=%+v", pausedTodo, resumedTodo)
	}
	_ = workerID
}

// forceExpireTodoClaimFixture simulates wall-clock expiry without weakening
// the production same-generation renewal guards. It temporarily removes only
// those two guards, writes the historical timestamps, and reinstalls their
// exact SQLite definitions before the product path under test runs.
func forceExpireTodoClaimFixture(t *testing.T, db *sql.DB, todoID string) {
	t.Helper()
	names := []string{
		"goal_todos_same_generation_claimed_at_guard",
		"goal_todos_same_generation_expiry_guard",
	}
	definitions := make([]string, 0, len(names))
	for _, name := range names {
		var definition string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&definition); err != nil {
			t.Fatalf("read renewal guard %s: %v", name, err)
		}
		definitions = append(definitions, definition)
		if _, err := db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatalf("drop renewal guard %s: %v", name, err)
		}
	}
	expiredClaimedAt := time.Now().UTC().Add(-2 * time.Minute)
	_, updateErr := db.Exec(`UPDATE goal_todos SET claim_claimed_at=?, claim_expires_at=? WHERE id=?`,
		expiredClaimedAt.Format(time.RFC3339Nano), time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), todoID)
	var restoreErr error
	for i, definition := range definitions {
		if _, err := db.Exec(definition); err != nil && restoreErr == nil {
			restoreErr = errors.New("restore renewal guard " + names[i] + ": " + err.Error())
		}
	}
	if updateErr != nil {
		t.Fatalf("write expired claim fixture: %v", updateErr)
	}
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
}

func TestCancelGoalCancelsEveryPendingHandoffExactlyOnce(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	targetID := "agent_governance_cancel_target"
	now := time.Now().UTC()
	if err := store.Agents().Create(ctx, &domain.AgentProfile{ID: targetID, WorkspaceID: workspaceID,
		Name: "Cancel target", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	newHandoff := func(clientKey string) *domain.Handoff {
		handoff, createErr := svc.CreateHandoff(ctx, application.CreateHandoffParams{
			GoalID: goal.ID, TodoID: claimed.ID,
			Source: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
			Target: domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: targetID},
			Reason: "cancel with the goal", ContextSummary: "pending handoff must not survive cancellation",
			Actor:     domain.GovernanceActorRef{Kind: domain.GovernanceActorAgent, ID: "agent_governance_owner"},
			ClientKey: clientKey,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return handoff
	}
	first, second := newHandoff("cancel-pending-1"), newHandoff("cancel-pending-2")
	preExisting, err := svc.CancelHandoff(ctx, newHandoff("cancel-existing").ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents := governanceEventCount(t, db, domain.EventHandoffStateChanged)
	latestGoal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelGoal(ctx, latestGoal.ID, latestGoal.Version); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ID, second.ID, preExisting.ID} {
		handoff, getErr := store.Handoffs().Get(ctx, id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if handoff.Status != domain.HandoffCancelled {
			t.Fatalf("Goal cancel must cancel pending handoff %s: %+v", id, handoff)
		}
	}
	afterEvents := governanceEventCount(t, db, domain.EventHandoffStateChanged)
	if afterEvents != beforeEvents+2 {
		t.Fatalf("Goal cancel must emit exactly one event per pending handoff: before=%d after=%d", beforeEvents, afterEvents)
	}
	if _, err := svc.CancelGoal(ctx, latestGoal.ID, latestGoal.Version); !errors.Is(err, domain.ErrVersionConflict) && !errors.Is(err, domain.ErrIllegalTransition) && !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("replaying the service command with a stale version must not emit more handoff events: %v", err)
	}
	if got := governanceEventCount(t, db, domain.EventHandoffStateChanged); got != afterEvents {
		t.Fatalf("terminal handoff replay emitted a duplicate event: %d vs %d", got, afterEvents)
	}
}

func TestProjectionRepairRebuildsLostDerivedRowAndKeepsCanonicalRows(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, _ := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	first, err := svc.RepairGoalProjection(ctx, goal.ID, nil, "projection-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Repair.Status != domain.ProjectionRepairCompleted || first.Projection.Digest == "" {
		t.Fatalf("repair did not complete: %+v", first)
	}
	var headers, phases int
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_headers`).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_phases`).Scan(&phases); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM governance_goal_projections WHERE goal_id=?`, goal.ID); err != nil {
		t.Fatal(err)
	}
	second, err := svc.RepairGoalProjection(ctx, goal.ID, nil, "projection-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.Repair.Status != domain.ProjectionRepairCompleted || second.Projection.Digest != first.Projection.Digest {
		t.Fatalf("lost projection did not rebuild deterministically: first=%+v second=%+v", first, second)
	}
	var headersAfter, phasesAfter int
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_headers`).Scan(&headersAfter); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_phases`).Scan(&phasesAfter); err != nil {
		t.Fatal(err)
	}
	if headersAfter != headers || phasesAfter != phases {
		t.Fatalf("repair changed canonical receipt rows: %d/%d -> %d/%d", headers, phases, headersAfter, phasesAfter)
	}
	if _, err := svc.GetGoalEvidence(ctx, goal.ID); err != nil {
		t.Fatal(err)
	}
	_ = store
}

func TestNormalGovernedTurnPhase7ProjectionDigestSurvivesRepair(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	_, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "phase seven projection", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"projection is deterministic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, compilerDecision(domain.PlanVerbDispatch, workerID), application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.GovernanceTurnKey == nil {
		t.Fatalf("governed plan missing turn identity: %+v", plan)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, *plan.GovernanceTurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 7 || phases[6].Phase != domain.TurnReceiptPhaseProjectionOutbox {
		t.Fatalf("normal governed turn must close phase7: %+v", phases)
	}
	if got := governanceEventCount(t, db, domain.EventProjectionUpdated); got != 1 {
		t.Fatalf("normal phase7 projection must publish one projection.updated event: %d", got)
	}
	before, err := svc.GetGovernanceProjection(ctx, plan.GovernanceTurnKey.GoalID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Digest == "" || before.SourceCursor.ThroughTurnSeq != plan.GovernanceTurnKey.TurnSeq {
		t.Fatalf("phase7 projection cursor/digest mismatch: %+v", before)
	}
	if _, err := db.Exec(`DELETE FROM governance_goal_projections WHERE goal_id=?`, plan.GovernanceTurnKey.GoalID); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := svc.RepairGoalProjection(ctx, plan.GovernanceTurnKey.GoalID, nil, "phase7-repair")
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Projection.Digest != before.Digest {
		t.Fatalf("phase7 must be excluded from projection source digest: before=%s rebuilt=%s", before.Digest, rebuilt.Projection.Digest)
	}
	if got := governanceEventCount(t, db, domain.EventProjectionUpdated); got != 2 {
		t.Fatalf("repair must publish one additional projection.updated event: %d", got)
	}
	if got := governanceEventCount(t, db, domain.EventProjectionRepairChanged); got != 2 {
		t.Fatalf("repair start/completion must publish two repair state events: %d", got)
	}
}

func TestGovernanceProjectionQueriesRejectCorruptDigest(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := svc.CreateGoal(ctx, workspaceID, application.CreateGoalParams{
		RootWorkItemID: root.ID, Objective: "projection read integrity",
		AcceptanceContract: []string{"corrupt projections are not exposed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RepairGoalProjection(ctx, goal.ID, nil, "projection-integrity"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE governance_goal_projections SET digest=? WHERE goal_id=?`,
		"sha256:"+strings.Repeat("0", 64), goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetGovernanceProjection(ctx, goal.ID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("corrupt projection must fail closed: err=%v", err)
	}
	if _, err := svc.GetGoalEvidence(ctx, goal.ID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("evidence query must reject the same corrupt projection: err=%v", err)
	}
}

func TestProjectionRepairRejectsTamperedReceiptAndPreservesExecutionState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger string
		update  string
	}{
		{name: "header", trigger: "turn_receipt_headers_immutable_update",
			update: `UPDATE turn_receipt_headers SET canonical_digest='sha256:` + strings.Repeat("0", 64) + `' WHERE goal_id=?`},
		{name: "phase7", trigger: "turn_receipt_phases_immutable_update",
			update: `UPDATE turn_receipt_phases SET canonical_digest='sha256:` + strings.Repeat("0", 64) + `' WHERE goal_id=? AND phase_seq=7`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
			defer db.Close()
			root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
				Title: "receipt tamper guard", RecordKind: domain.RecordKindTask,
				AutoCoordinate: true, AcceptanceCriteria: []string{"receipt corruption is fail closed"},
			})
			if err != nil {
				t.Fatal(err)
			}
			source := dispatcher.runs[0]
			markCompilerSourceSucceeded(t, ctx, store, source.ID)
			source, err = store.Runs().Get(ctx, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source,
				compilerDecision(domain.PlanVerbDispatch, workerID), application.PlanCandidateNativeText)
			if err != nil {
				t.Fatal(err)
			}
			beforeProjection, err := store.GovernanceProjections().Get(ctx, plan.GovernanceTurnKey.GoalID)
			if err != nil {
				t.Fatal(err)
			}
			beforePlan, err := store.Plans().Get(ctx, plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeRuns, err := store.Runs().ListByWorkItem(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("DROP TRIGGER " + tc.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.update, plan.GovernanceTurnKey.GoalID); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.RepairGoalProjection(ctx, plan.GovernanceTurnKey.GoalID, nil, "tamper-"+tc.name); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("tampered %s receipt must fail closed: %v", tc.name, err)
			}
			afterProjection, err := store.GovernanceProjections().Get(ctx, plan.GovernanceTurnKey.GoalID)
			if err != nil {
				t.Fatal(err)
			}
			if afterProjection.Digest != beforeProjection.Digest || afterProjection.Version != beforeProjection.Version {
				t.Fatalf("tampered %s repair must preserve the previous projection: before=%+v after=%+v", tc.name, beforeProjection, afterProjection)
			}
			afterPlan, err := store.Plans().Get(ctx, plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if afterPlan.Version != beforePlan.Version || afterPlan.Status != beforePlan.Status {
				t.Fatalf("tampered %s repair changed Plan state: before=%+v after=%+v", tc.name, beforePlan, afterPlan)
			}
			afterRuns, err := store.Runs().ListByWorkItem(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(afterRuns) != len(beforeRuns) {
				t.Fatalf("tampered %s repair changed execution Run count: before=%d after=%d", tc.name, len(beforeRuns), len(afterRuns))
			}
		})
	}
}

func TestGovernedAcceptRollsBackWhenNoCanonicalValidationEvidence(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "accept evidence gate", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a validation result is required"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, compilerDecision(domain.PlanVerbFinish, workerID), application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.Status != domain.PlanFinished {
		t.Fatalf("finish plan missing: %+v", plan)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	state.Status = domain.CoordinatorWaitingUser
	state.Phase = "acceptance"
	state.CurrentRunID = ""
	state.Version++
	if err := store.TaskCoordinators().UpdateState(ctx, state, state.Version-1); err != nil {
		t.Fatal(err)
	}
	wi, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectedWI := wi.Version
	if err := wi.EnterReview(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := wi.EnterAcceptance(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, wi, expectedWI); err != nil {
		t.Fatal(err)
	}
	before, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptWorkItem(ctx, root.ID, before.Version); err == nil {
		t.Fatal("accept without canonical validation evidence must fail closed")
	}
	after, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.Phase != before.Phase || after.Version != before.Version {
		t.Fatalf("failed acceptance must roll back root Task: before=%+v after=%+v", before, after)
	}
}
