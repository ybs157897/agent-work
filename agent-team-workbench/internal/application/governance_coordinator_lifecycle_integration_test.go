package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

func TestCoordinatedGoalPauseGatesTerminalWakeAndResumeReplaysOnce(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "pause and resume control line", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"paused output never advances until resume"},
	})
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
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != domain.GoalWaiting || todo.Status != domain.TodoWaiting {
		t.Fatalf("pause must stop governance intent: goal=%+v todo=%+v", paused, todo)
	}
	if started, err := svc.ResumeDueTaskCoordinators(ctx, wsID, 10); err != nil || started != 0 {
		t.Fatalf("paused Goal must be absent from due recovery: started=%d err=%v", started, err)
	}
	if _, err := svc.CreateRunForWakeup(ctx, wsID, root.AgentProfileID, root.ID,
		"paused wake must remain queued", map[string]any{"trigger": "timer"}); err == nil || errors.Is(err, scheduling.ErrWakeupNoop) {
		t.Fatalf("paused Goal wake must be retryable rather than consumed: %v", err)
	}
	if _, err := svc.CreateRunForWakeup(ctx, wsID, workerID, root.ID,
		"paused worker wake must remain queued", map[string]any{"trigger": "assignment"}); err == nil || errors.Is(err, scheduling.ErrWakeupNoop) {
		t.Fatalf("ordinary Worker wake bypassed the paused Goal: %v", err)
	}

	source := dispatcher.runs[0]
	startCoordinatorRunForPlanDecision(t, ctx, svc, source.ID)
	decision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"resume this exact decision","next_action":"wait for worker","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"paused worker","instruction":"run only after resume","acceptance":["worker starts after resume"]},{"verb":"join","children":"all"}]}`
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": decision}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("paused terminal output created another Run: %d", len(dispatcher.runs))
	}
	if plan, err := store.Plans().LatestByWorkItem(ctx, root.ID); err != nil || plan != nil {
		t.Fatalf("paused terminal output created a Plan: plan=%+v err=%v", plan, err)
	}

	paused, err = store.Goals().Get(ctx, paused.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := svc.ResumeGoal(ctx, paused.ID, paused.Version)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != domain.GoalActive || len(dispatcher.runs) != 2 {
		t.Fatalf("resume must replay the held terminal decision exactly once: goal=%+v runs=%d", resumed, len(dispatcher.runs))
	}
	plan, err := store.Plans().LatestByWorkItem(ctx, root.ID)
	if err != nil || plan == nil || plan.GovernanceTurnKey == nil || plan.SourceRunID != source.ID {
		t.Fatalf("resume did not preserve the governed source decision: plan=%+v err=%v", plan, err)
	}
	if _, err := svc.ResumeGoal(ctx, resumed.ID, resumed.Version); err == nil {
		t.Fatal("an already active Goal must not replay resume")
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("resume replay duplicated a Worker Run: %d", len(dispatcher.runs))
	}
}

func TestBlockedGoalCannotBypassWorkItemUnblock(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "blocked Goal resume fence", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"blocked recovery uses one authority"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "operator_block", Message: "must use WorkItem unblock", Source: "test",
	}, root.Version); err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResumeGoal(ctx, goal.ID, goal.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("blocked Goal bypassed WorkItem unblock: %v", err)
	}
	goal, _ = store.Goals().Get(ctx, goal.ID)
	root, _ = store.WorkItems().Get(ctx, root.ID)
	if goal.Status != domain.GoalBlocked || root.Status != domain.WorkItemBlocked {
		t.Fatalf("failed bypass changed blocker state: goal=%+v root=%+v", goal, root)
	}
}

func TestResumeGoalPreservesWaitingUserCheckpoint(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "resume waiting user", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"resume does not bypass user acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ControlRun(ctx, dispatcher.runs[0].ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	setCoordinatorWaitingUser(t, ctx, store, root.ID)
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	runsBefore := len(dispatcher.runs)
	if _, err := svc.ResumeGoal(ctx, paused.ID, paused.Version); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorWaitingUser || len(dispatcher.runs) != runsBefore {
		t.Fatalf("Goal resume bypassed waiting-user checkpoint: state=%+v runs=%d/%d", state, len(dispatcher.runs), runsBefore)
	}
}

func TestPausedGoalKeepsPlanApprovalPendingUntilResume(t *testing.T) {
	now := time.Now().UTC()
	manual := &domain.AgentProfile{
		ID: "agent_goal_pause_manual", Name: "Paused approval target", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         now, UpdatedAt: now,
	}
	ctx, db, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTestWithDatabase(t,
		"pause manual approval", []*domain.AgentProfile{manual})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	plan := usageDriveSourceDecision(t, ctx, svc, store, dispatcher.runs[0].ID,
		usageWorkerDecision(t, manual.ID))
	approvalID := pendingPlanApprovalID(t, ctx, store, root.WorkspaceID, plan.ID)
	goal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, approvalID, true, "user_approver", "approve while paused", domain.ApprovalScopeOnce); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("paused Goal allowed plan dispatch approval: %v", err)
	}
	approval, err := store.Runs().GetApproval(ctx, approvalID)
	if err != nil || approval.Status != domain.ApprovalPending || len(dispatcher.runs) != 1 {
		t.Fatalf("paused approval must remain pending without a Worker: approval=%+v runs=%d err=%v", approval, len(dispatcher.runs), err)
	}
	pausedTodo, err := store.Todos().Get(ctx, paused.CurrentTodoID)
	if err != nil || pausedTodo.Claim == nil {
		t.Fatalf("paused admitted Todo must retain its claim identity: todo=%+v err=%v", pausedTodo, err)
	}
	expiredClaimedAt := time.Now().UTC().Add(-2 * time.Minute)
	expiredAt := time.Now().UTC().Add(-time.Minute)
	if _, err := db.Exec(`UPDATE goal_todos SET claim_version=claim_version+1,
		claim_claimed_at=?, claim_expires_at=?, version=version+1 WHERE id=?`,
		expiredClaimedAt, expiredAt, pausedTodo.ID); err != nil {
		t.Fatal(err)
	}
	pausedTodo, err = store.Todos().Get(ctx, pausedTodo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResumeGoal(ctx, paused.ID, paused.Version); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("resume must not race the pending approval with a Coordinator Run: %d", len(dispatcher.runs))
	}
	resumedTodo, err := store.Todos().Get(ctx, pausedTodo.ID)
	if err != nil || resumedTodo.Claim == nil || resumedTodo.Claim.OwnerAgentID != plan.AgentProfileID ||
		resumedTodo.ClaimVersion != pausedTodo.ClaimVersion+2 {
		t.Fatalf("resume must renew an expired admitted claim without changing turn identity: before=%+v after=%+v err=%v",
			pausedTodo, resumedTodo, err)
	}
	if _, err := svc.ResolveApproval(ctx, approvalID, true, "user_approver", "approve after resume", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("resumed approval must create exactly one Worker Run: %d", len(dispatcher.runs))
	}
}

func TestPausedGoalPreservesSettlementWakeUntilResume(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "pause settlement wake", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"settlement wake survives pause"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	completeCoordinatorPlanDecision(t, ctx, svc, source.ID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch before pause","next_action":"settle after resume","steps":[{"verb":"dispatch","agent_id":"`+workerID+`","title":"worker","instruction":"finish","acceptance":["done"]},{"verb":"join","children":"all"}]}`)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("precondition: one Worker expected: %d", len(dispatcher.runs))
	}
	worker := dispatcher.runs[1]
	if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, worker.ID, "worker done"); err != nil {
		t.Fatal(err)
	}
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	var settlement *domain.WakeupRequest
	for index := range wakeups {
		if dispatchID, ok := wakeups[index].Context[domain.WakeupContextSettlementDispatchID].(string); ok && dispatchID == worker.DispatchID {
			settlement = &wakeups[index]
			break
		}
	}
	if settlement == nil {
		t.Fatalf("settlement wake missing: %+v", wakeups)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	if outcome, err := scheduler.ConsumeOne(ctx, *settlement, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeQueued {
		t.Fatalf("paused settlement wake must stay queued: outcome=%s err=%v", outcome, err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("paused settlement created a summary Run: %d", len(dispatcher.runs))
	}
	// Simulate a process dying after queued→consumed but before summary Run
	// creation. Resume must reconstruct one queued wake from the collecting
	// Dispatch instead of assuming the old row is still usable.
	if err := store.Wakeups().MarkWakeupStatus(ctx, settlement.ID, domain.WakeupStatusConsumed); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResumeGoal(ctx, paused.ID, paused.Version); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("resume must leave the collecting dispatch to its exact wake: %d", len(dispatcher.runs))
	}
	wakeups, err = store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	var repaired *domain.WakeupRequest
	settlementDispatchID := worker.DispatchID
	for index := range wakeups {
		if dispatchID, _ := wakeups[index].Context[domain.WakeupContextSettlementDispatchID].(string); dispatchID == settlementDispatchID {
			repaired = &wakeups[index]
			break
		}
	}
	if repaired == nil || repaired.ID == settlement.ID {
		t.Fatalf("consumed settlement wake was not reconstructed: old=%s wakeups=%+v", settlement.ID, wakeups)
	}
	if outcome, err := scheduler.ConsumeOne(ctx, *repaired, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeConsumed {
		t.Fatalf("resumed settlement wake did not execute: outcome=%s err=%v", outcome, err)
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("resumed settlement must create one summary Run: %d", len(dispatcher.runs))
	}
}

func TestPausedGoalKeepsRuntimeApprovalPendingUntilResume(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "pause runtime approval", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"runtime approval respects Goal pause"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"runtime approval","next_action":"wait","steps":[{"verb":"dispatch","agent_id":"`+workerID+`","title":"worker","instruction":"request approval","acceptance":["approved after resume"]},{"verb":"join","children":"all"}]}`)
	worker := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, worker.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	approval, err := svc.RequestApproval(ctx, worker.ID, domain.ApprovalKindCommand, "high", "run command")
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, approval.ID, true, "user_approver", "while paused", domain.ApprovalScopeOnce); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("paused Goal resumed a Runtime approval: %v", err)
	}
	approval, err = store.Runs().GetApproval(ctx, approval.ID)
	worker, runErr := store.Runs().Get(ctx, worker.ID)
	if err != nil || runErr != nil || approval.Status != domain.ApprovalPending || worker.Status != domain.RunWaitingApproval {
		t.Fatalf("paused Runtime approval did not remain pending: approval=%+v run=%+v err=%v runErr=%v",
			approval, worker, err, runErr)
	}
	if _, err := svc.ResumeGoal(ctx, paused.ID, paused.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, approval.ID, true, "user_approver", "after resume", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	worker, err = store.Runs().Get(ctx, worker.ID)
	if err != nil || worker.Status != domain.RunRunning {
		t.Fatalf("resumed Goal did not release Runtime approval: run=%+v err=%v", worker, err)
	}
}

func TestCancelGoalExpiresPendingRuntimeApprovalWithRunCancellation(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "cancel runtime approval", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"cancel closes runtime approval"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"runtime approval cancellation","next_action":"wait","steps":[{"verb":"dispatch","agent_id":"`+workerID+`","title":"worker","instruction":"request approval","acceptance":["approval closes on cancel"]},{"verb":"join","children":"all"}]}`)
	worker := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, worker.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	approval, err := svc.RequestApproval(ctx, worker.ID, domain.ApprovalKindCommand, "high", "cancelled command")
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelGoal(ctx, goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	approval, err = store.Runs().GetApproval(ctx, approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	worker, err = store.Runs().Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != domain.ApprovalExpired || worker.Status != domain.RunCancelling {
		t.Fatalf("Goal cancel must expire runtime approval and fence its Run: approval=%+v worker=%+v", approval, worker)
	}
	if _, err := svc.ResolveApproval(ctx, approval.ID, true, "late", "late approval", domain.ApprovalScopeOnce); err == nil {
		t.Fatal("expired runtime approval must not be resolved after Goal cancellation")
	}
	_ = wsID
}

func TestPausedGoalKeepsOrdinaryWorkerWakeQueuedPastWakeupAge(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "pause ordinary worker wake", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"ordinary worker wake remains deliverable while paused"},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "paused worker child", RecordKind: domain.RecordKindTask,
		ParentID: root.ID, AgentProfileID: workerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := store.Agents().Get(ctx, workerID)
	if err != nil {
		t.Fatal(err)
	}
	worker.WakeOnDemand = true
	worker.UpdatedAt = time.Now().UTC()
	if err := store.Agents().Update(ctx, worker, worker.Version); err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	wake, err := scheduling.EnqueueWakeup(ctx, store.Wakeups(), domain.WakeupSourceOnDemand,
		wsID, workerID, child.ID, map[string]any{"work_item_title": child.Title},
		time.Now().UTC().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	scheduler := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	if outcome, err := scheduler.ConsumeOne(ctx, *wake, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeQueued {
		t.Fatalf("paused ordinary Worker wake must remain queued past max age: outcome=%s err=%v", outcome, err)
	}
	queued, err := store.Wakeups().DueTimers(ctx, time.Now().UTC(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range queued {
		if candidate.ID == wake.ID && candidate.Status == domain.WakeupStatusQueued {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("paused ordinary Worker wake was coalesced or consumed: wake=%+v queued=%+v", wake, queued)
	}
	if len(dispatcher.runs) != 1 || paused.Status != domain.GoalWaiting {
		t.Fatalf("paused ordinary Worker wake changed execution state: goal=%+v runs=%d", paused, len(dispatcher.runs))
	}
}

func TestPausedGoalActiveWorkerWakeDoesNotForwardOrCoalesce(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "pause active worker wake", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"a paused wake must wait for resume"},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "active worker child", RecordKind: domain.RecordKindTask, ParentID: root.ID,
		AgentProfileID: workerID, AcceptanceCriteria: []string{"worker remains active while the Goal pauses"},
	})
	if err != nil {
		t.Fatal(err)
	}
	activeID, err := svc.CreateRunForWakeup(ctx, wsID, workerID, child.ID,
		"hold the child run open", map[string]any{"trigger": "assignment"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.Runs().Get(ctx, activeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, active.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PauseGoal(ctx, goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	wake, err := scheduling.EnqueueWakeup(ctx, store.Wakeups(), domain.WakeupSourceOnDemand,
		wsID, workerID, child.ID, map[string]any{
			"work_item_title": child.Title, "instruction": "paused steering must stay queued",
		}, time.Now().UTC().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	forwarded := 0
	scheduler := &scheduling.Scheduler{
		Store: store.Wakeups(), RunStarter: svc,
		ForwardInput: func(context.Context, string, string) error {
			forwarded++
			return nil
		},
	}
	if outcome, err := scheduler.ConsumeOne(ctx, *wake, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeQueued {
		t.Fatalf("paused active-worker wake must remain queued: outcome=%s err=%v", outcome, err)
	}
	if forwarded != 0 {
		t.Fatalf("paused wake must not forward steering to the active Run: forwarded=%d", forwarded)
	}
	queued, err := store.Wakeups().DueTimers(ctx, time.Now().UTC(), 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range queued {
		if candidate.ID == wake.ID && candidate.Status == domain.WakeupStatusQueued {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("paused active-worker wake was coalesced or consumed: wake=%+v queued=%+v", wake, queued)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("scheduler must not create a new Run while paused: runs=%d", len(dispatcher.runs))
	}
}

func TestActiveGoalRenewsExpiredPlanApprovalClaim(t *testing.T) {
	now := time.Now().UTC()
	manual := &domain.AgentProfile{
		ID: "agent_goal_active_late_approval", Name: "Active late approval target", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         now, UpdatedAt: now,
	}
	ctx, db, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTestWithDatabase(t,
		"active late approval", []*domain.AgentProfile{manual})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	plan := usageDriveSourceDecision(t, ctx, svc, store, dispatcher.runs[0].ID,
		usageWorkerDecision(t, manual.ID))
	approvalID := pendingPlanApprovalID(t, ctx, store, root.WorkspaceID, plan.ID)
	goal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil || todo.Claim == nil {
		t.Fatalf("precondition: governed Plan must retain its Todo claim: todo=%+v err=%v", todo, err)
	}
	expiredClaimVersion := todo.ClaimVersion + 1
	expiredClaimedAt := now.Add(-2 * time.Minute)
	expiredAt := now.Add(-time.Minute)
	if _, err := db.Exec(`UPDATE goal_todos SET claim_version=?,
		claim_claimed_at=?, claim_expires_at=?, version=version+1 WHERE id=?`,
		expiredClaimVersion, expiredClaimedAt, expiredAt, todo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, approvalID, true, "user_approver", "late but active", domain.ApprovalScopeOnce); err != nil {
		t.Fatalf("active Goal late plan approval must renew its fenced claim: %v", err)
	}
	updatedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedTodo.Claim == nil || updatedTodo.Claim.OwnerAgentID != plan.AgentProfileID ||
		updatedTodo.ClaimVersion != expiredClaimVersion+2 || !updatedTodo.Claim.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("late approval did not renew the same Plan owner claim: before=%+v after=%+v", todo, updatedTodo)
	}
	approval, err := store.Runs().GetApproval(ctx, approvalID)
	if err != nil || approval.Status != domain.ApprovalApproved {
		t.Fatalf("late approval did not resolve after claim renewal: approval=%+v err=%v", approval, err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("late approval must create exactly one Worker Run: %d", len(dispatcher.runs))
	}
}

func TestCancelRepairExhaustedGoalClearsRepairCheckpoint(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "cancel exhausted repair", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"exhausted repair remains cancellable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[index].ID, `{}`)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelGoal(ctx, goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorCancelled || state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 {
		t.Fatalf("cancel must clear the exhausted repair checkpoint: %+v", state)
	}
}

func TestCancelCoordinatedGoalClosesPlanApprovalQuotaAndFutureExecution(t *testing.T) {
	now := time.Now().UTC()
	manual := &domain.AgentProfile{
		ID: "agent_goal_cancel_manual", Name: "Manual cancel target", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         now, UpdatedAt: now,
	}
	ctx, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTest(t,
		"cancel governed control line", []*domain.AgentProfile{manual},
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1000, Enforcement: domain.QuotaEnforcementEnforce})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	plan := usageDriveSourceDecision(t, ctx, svc, store, dispatcher.runs[0].ID,
		usageWorkerDecision(t, manual.ID))
	if plan.GovernanceTurnKey == nil || plan.Status != domain.PlanWaiting {
		t.Fatalf("precondition: manual governed Plan must be waiting: %+v", plan)
	}
	approvalID := pendingPlanApprovalID(t, ctx, store, root.WorkspaceID, plan.ID)
	turnKey := *plan.GovernanceTurnKey
	goal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := svc.CancelGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, cancelled.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := store.Runs().GetApproval(ctx, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != domain.GoalCancelled || todo.Status != domain.TodoCancelled || todo.Claim != nil ||
		state.Status != domain.CoordinatorCancelled || state.CurrentRunID != "" || state.NextActionAt != nil ||
		root.Status != domain.WorkItemCancelled || plan.Status != domain.PlanCancelled ||
		approval.Status != domain.ApprovalExpired {
		t.Fatalf("Goal cancel left a live control projection: goal=%+v todo=%+v state=%+v root=%+v plan=%+v approval=%+v",
			cancelled, todo, state, root, plan, approval)
	}
	if active, err := store.Quotas().SumActiveReserved(ctx, cancelled.ID, domain.QuotaOutputTokens); err != nil || active != 0 {
		t.Fatalf("Goal cancel left an active quota reservation: active=%d err=%v", active, err)
	}
	if _, err := store.TurnReceipts().GetPhase(ctx, turnKey, 6); err != nil {
		t.Fatalf("Goal cancel must close the admitted Turn receipt: %v", err)
	}
	runsBefore := len(dispatcher.runs)
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRunForWakeup(ctx, root.WorkspaceID, state.CoordinatorAgentID, root.ID,
		"cancelled wake", map[string]any{"trigger": "timer"}); !errors.Is(err, scheduling.ErrWakeupNoop) {
		t.Fatalf("cancelled Goal accepted a wake: %v", err)
	}
	if _, err := svc.ResolveApproval(ctx, approvalID, true, "late_operator", "late", domain.ApprovalScopeOnce); err == nil {
		t.Fatal("late approval executed after Goal cancellation")
	}
	if len(dispatcher.runs) != runsBefore {
		t.Fatalf("cancelled Goal created a later Run: before=%d after=%d", runsBefore, len(dispatcher.runs))
	}
}

func TestCancelGoalSettlesEveryReservedTurnKey(t *testing.T) {
	now := time.Now().UTC()
	manual := &domain.AgentProfile{
		ID: "agent_goal_cancel_all_turns", Name: "Cancel all turns target", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         now, UpdatedAt: now,
	}
	ctx, db, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTestWithDatabase(t,
		"cancel all reserved turns", []*domain.AgentProfile{manual},
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1000, Enforcement: domain.QuotaEnforcementEnforce})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	firstPlan := usageDriveSourceDecision(t, ctx, svc, store, dispatcher.runs[0].ID,
		usageWorkerDecision(t, manual.ID))
	firstKey := *firstPlan.GovernanceTurnKey
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRunForWakeup(ctx, root.WorkspaceID, state.CoordinatorAgentID, root.ID,
		"start a second governed turn", map[string]any{"trigger": "on_demand"}); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("precondition: second Coordinator source Run expected: %d", len(dispatcher.runs))
	}
	secondPlan := usageDriveSourceDecision(t, ctx, svc, store, dispatcher.runs[1].ID,
		usageWorkerDecision(t, manual.ID))
	secondKey := *secondPlan.GovernanceTurnKey
	if firstKey.Equal(secondKey) {
		t.Fatalf("precondition: expected two distinct governed turns: first=%+v second=%+v", firstKey, secondKey)
	}
	for _, key := range []domain.TurnKey{firstKey, secondKey} {
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
			TurnKey: key, Kind: domain.QuotaOutputTokens,
		})
		if err != nil || reservation.Status != domain.QuotaReservationReserved {
			t.Fatalf("precondition: turn %s must retain a reserved quota: reservation=%+v err=%v", fmt.Sprintf("%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq), reservation, err)
		}
	}
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_cancel_all_turns_quota_settlement
		BEFORE UPDATE ON quota_reservations
		WHEN OLD.status='reserved'
		BEGIN SELECT RAISE(ABORT, 'injected all-turn cancellation settlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	cancelled, cancelErr := svc.CancelGoal(ctx, goal.ID, goal.Version)
	if cancelled == nil || cancelErr == nil {
		t.Fatalf("cancellation must commit before reporting the injected quota failure: goal=%+v err=%v", cancelled, cancelErr)
	}
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != domain.GoalCancelled {
		t.Fatalf("cancel should commit before quota recovery: %+v", goal)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, ok := state.Data["settle_cancelled_goal_turn_keys"].([]any)
	if state.Data["control_action"] != "settle_cancelled_goal" || !ok || len(checkpoint) != 2 {
		t.Fatalf("cancel must retain every reserved TurnKey in its durable checkpoint: state=%+v", state)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_cancel_all_turns_quota_settlement`); err != nil {
		t.Fatal(err)
	}
	if resumed, err := svc.ResumeDueTaskCoordinators(ctx, root.WorkspaceID, 10); err != nil || resumed != 1 {
		t.Fatalf("all cancelled Goal quota keys were not replayed: resumed=%d err=%v", resumed, err)
	}
	for _, key := range []domain.TurnKey{firstKey, secondKey} {
		reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
			TurnKey: key, Kind: domain.QuotaOutputTokens,
		})
		if err != nil || reservation.Status == domain.QuotaReservationReserved {
			t.Fatalf("Goal cancel left a reserved quota for turn %s: reservation=%+v err=%v", fmt.Sprintf("%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq), reservation, err)
		}
		if _, err := store.TurnReceipts().GetPhase(ctx, key, 6); err != nil {
			t.Fatalf("Goal cancel did not close quota phase6 for turn %s: %v", fmt.Sprintf("%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq), err)
		}
	}
}

func TestCancelGoalClosesOpenDispatchAsCancelled(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	workspaceID, _, leadID, rootID := seedDispatchSvcEnv(t, ctx, svc, store)
	goal, err := svc.CreateGoal(ctx, workspaceID, application.CreateGoalParams{
		RootWorkItemID: rootID, Objective: "cancel open dispatch",
		AcceptanceContract: []string{"cancellation is distinct from degradation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err = svc.StartGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	leadRun, err := svc.CreateRun(ctx, rootID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "keep this dispatch open",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatches, err := store.Dispatches().ListByWorkItem(ctx, rootID)
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("expected one open dispatch: %v %#v", err, dispatches)
	}
	if _, err := svc.CancelGoal(ctx, goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	dispatch, err := store.Dispatches().Get(ctx, dispatches[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Status != domain.DispatchCancelled || dispatch.ClosedAt == nil {
		t.Fatalf("Goal cancellation must close an open dispatch as cancelled: %+v", dispatch)
	}
	events, err := store.Events().Since(ctx, workspaceID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, event := range events {
		if event.Type == domain.EventDispatchUpdated && event.Aggregate.ID == dispatch.ID {
			updates++
			if event.Data["status"] != string(domain.DispatchCancelled) {
				t.Fatalf("cancelled dispatch event status mismatch: %+v", event.Data)
			}
		}
	}
	if updates != 1 {
		t.Fatalf("Goal cancellation must emit one cancelled dispatch update: %d", updates)
	}
	// The post-commit run cancellation is deliberately exercised as well; it
	// must not rewrite the already-cancelled dispatch as degraded.
	latest, err := store.Runs().Get(ctx, leadRun.ID)
	if err != nil || latest.Status != domain.RunCancelled {
		t.Fatalf("queued lead run should be cancelled after Goal cancel: %+v err=%v", latest, err)
	}
}

func TestCancelGoalFencesEveryNonTerminalRunInTheCommit(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, _ := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	seedCtx(t, store, ctx, workspaceID)
	ownerID := "agent_governance_owner"
	newRun := func(instruction string) *domain.ExecutionRun {
		run, err := svc.CreateRun(ctx, rootID, application.CreateRunParams{
			AgentProfileID: ownerID, Instruction: instruction,
		})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	queued := newRun("queued cancellation")
	starting := newRun("starting cancellation")
	running := newRun("running cancellation")
	if err := svc.RecordRunStatus(ctx, starting.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, running.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	forwarded := []string{}
	svc.ControlForwarder = func(_ context.Context, runID, action string) {
		if action == "cancel" {
			forwarded = append(forwarded, runID)
		}
	}
	latestGoal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelGoal(ctx, latestGoal.ID, latestGoal.Version); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     string
		status domain.RunStatus
	}{
		{queued.ID, domain.RunCancelled},
		{starting.ID, domain.RunCancelled},
		{running.ID, domain.RunCancelling},
	} {
		run, getErr := store.Runs().Get(ctx, tc.id)
		if getErr != nil || run.Status != tc.status {
			t.Fatalf("Goal cancel must fence Run %s in the same transaction: want=%s got=%+v err=%v", tc.id, tc.status, run, getErr)
		}
	}
	if len(forwarded) != 3 {
		t.Fatalf("Goal cancel must forward cancellation to every non-terminal Run after commit: forwarded=%v", forwarded)
	}
}

type failingCancelRunUpdateRepo struct {
	application.RunRepo
	targetID string
}

func (r *failingCancelRunUpdateRepo) Update(ctx context.Context, run *domain.ExecutionRun, expectedVersion int) error {
	if run != nil && run.ID == r.targetID {
		return fmt.Errorf("injected cancellation run transition failure")
	}
	return r.RunRepo.Update(ctx, run, expectedVersion)
}

type failingCancelRunStore struct {
	application.Store
	runs application.RunRepo
}

func (s *failingCancelRunStore) Runs() application.RunRepo { return s.runs }

func TestCancelGoalRunTransitionFailureRollsBackAuthoritativeState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	base := sqlstore.New(db)
	failingRuns := &failingCancelRunUpdateRepo{RunRepo: base.Runs()}
	store := &failingCancelRunStore{Store: base, runs: failingRuns}
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	workspaceID, _, leadID, rootID := seedDispatchSvcEnv(t, ctx, svc, base)
	goal, err := svc.CreateGoal(ctx, workspaceID, application.CreateGoalParams{
		RootWorkItemID: rootID, Objective: "rollback cancellation on run failure",
		AcceptanceContract: []string{"run transition failure cannot partially cancel a Goal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err = svc.StartGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, rootID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "run transition must fail inside cancel transaction",
	})
	if err != nil {
		t.Fatal(err)
	}
	failingRuns.targetID = run.ID
	if _, err := svc.CancelGoal(ctx, goal.ID, goal.Version); err == nil {
		t.Fatal("run transition failure must reject Goal cancellation")
	}
	goalAfter, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootAfter, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	runAfter, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goalAfter.Status != domain.GoalActive || rootAfter.Status == domain.WorkItemCancelled || runAfter.Status != domain.RunQueued {
		t.Fatalf("failed run transition left partial cancellation state: goal=%+v root=%+v run=%+v", goalAfter, rootAfter, runAfter)
	}
}

func TestCancelGoalCancelsEveryNonTerminalTodoAndPreservesCompletedHistory(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, current := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	now := time.Now().UTC()
	secondary := &domain.Todo{
		ID: domain.NewID(domain.PrefixTodo), GoalID: goal.ID, Class: domain.TodoValidation,
		Status: domain.TodoPending, Instruction: "validate the secondary branch",
		Acceptance: []string{"secondary validation is cancelled with the Goal"}, Priority: domain.PriorityMedium,
		Predecessors: []string{}, Successors: []string{}, DecisionScope: domain.DecisionScope{
			WorkItemIDs: []string{rootID}, AgentIDs: []string{"agent_governance_owner"},
			RuntimeCapabilities: []string{}, WriteScopes: []string{}, MaxDispatch: 1,
		}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Todos().Create(ctx, secondary); err != nil {
		t.Fatal(err)
	}
	claimedSecondary, err := svc.ClaimTodo(ctx, secondary.ID, "agent_governance_owner", secondary.Version, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	for _, advance := range []func(time.Time) error{
		func(at time.Time) error { return root.Transition(domain.WorkItemInProgress, at) },
		func(at time.Time) error { return root.EnterReview(at) },
		func(at time.Time) error { return root.EnterAcceptance(at) },
		func(at time.Time) error { return root.Accept(at) },
	} {
		expected := root.Version
		if err := advance(now); err != nil {
			t.Fatal(err)
		}
		if err := store.WorkItems().Update(ctx, root, expected); err != nil {
			t.Fatal(err)
		}
	}
	completed := &domain.Todo{
		ID: domain.NewID(domain.PrefixTodo), GoalID: goal.ID, Class: domain.TodoValidation,
		Status: domain.TodoPending, Instruction: "historical validation", Acceptance: []string{"history remains immutable"},
		Priority: domain.PriorityLow, Predecessors: []string{}, Successors: []string{}, DecisionScope: domain.DecisionScope{
			WorkItemIDs: []string{rootID}, AgentIDs: []string{"agent_governance_owner"},
			RuntimeCapabilities: []string{}, WriteScopes: []string{}, MaxDispatch: 1,
		}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Todos().Create(ctx, completed); err != nil {
		t.Fatal(err)
	}
	claimedCompleted, err := store.Todos().Claim(ctx, completed.ID, "agent_governance_owner",
		now, now.Add(time.Hour), completed.Version)
	if err != nil {
		t.Fatal(err)
	}
	completionKey := domain.TurnKey{GoalID: goal.ID, TodoID: completed.ID, TurnSeq: 1}
	header := &domain.TurnReceiptHeader{
		TurnKey: completionKey, Attempt: 1, SchemaVersion: "plan-decision/v2",
		InputSnapshotDigest: governanceDigest('c'), AdmissionClientKey: "historical-completion",
		CreatedAt: now,
	}
	header.CanonicalDigest, err = application.ComputeTurnReceiptHeaderDigest(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TurnReceipts().Admit(ctx, header, "agent_governance_owner", claimedCompleted.Version); err != nil {
		t.Fatal(err)
	}
	runningCompleted, err := store.Todos().Get(ctx, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err = store.Todos().Complete(ctx, completed.ID, completionKey, rootID, now, runningCompleted.Version)
	if err != nil {
		t.Fatal(err)
	}
	beforeCompleted := *completed
	latestGoal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelGoal(ctx, latestGoal.ID, latestGoal.Version); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{current.ID, claimedSecondary.ID} {
		todo, getErr := store.Todos().Get(ctx, id)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if todo.Status != domain.TodoCancelled || todo.Claim != nil {
			t.Fatalf("Goal cancel must close and release every live Todo: %+v", todo)
		}
	}
	afterCompleted, err := store.Todos().Get(ctx, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCompleted.Status != beforeCompleted.Status || afterCompleted.Version != beforeCompleted.Version ||
		afterCompleted.CompletionEvidenceID != beforeCompleted.CompletionEvidenceID {
		t.Fatalf("completed Todo history must remain immutable: before=%+v after=%+v", beforeCompleted, afterCompleted)
	}
}

func TestCancelledGoalQuotaSettlementHasDurableRecoveryCheckpoint(t *testing.T) {
	now := time.Now().UTC()
	manual := &domain.AgentProfile{
		ID: "agent_goal_cancel_recovery", Name: "Cancel recovery target", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Policy:            domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		CreatedAt:         now, UpdatedAt: now,
	}
	ctx, db, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTestWithDatabase(t,
		"cancel settlement recovery", []*domain.AgentProfile{manual},
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1000, Enforcement: domain.QuotaEnforcementEnforce})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	plan := usageDriveSourceDecision(t, ctx, svc, store, dispatcher.runs[0].ID,
		usageWorkerDecision(t, manual.ID))
	turnKey := *plan.GovernanceTurnKey
	if _, err := db.Exec(`CREATE TRIGGER fail_cancelled_goal_quota_settlement
		BEFORE UPDATE ON quota_reservations
		WHEN OLD.status='reserved'
		BEGIN SELECT RAISE(ABORT, 'injected cancelled Goal settlement failure'); END`); err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancelErr := svc.CancelGoal(ctx, goal.ID, goal.Version)
	if cancelled == nil || cancelErr == nil {
		t.Fatalf("cancellation must commit while surfacing the post-commit settlement failure: goal=%+v err=%v", cancelled, cancelErr)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorCancelled || state.Data["control_action"] != "settle_cancelled_goal" {
		t.Fatalf("failed settlement must retain a durable cancelled checkpoint: %+v", state)
	}
	if active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || active == 0 {
		t.Fatalf("fault injection did not preserve the reserved precondition: active=%d err=%v", active, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_cancelled_goal_quota_settlement`); err != nil {
		t.Fatal(err)
	}
	runsBefore := len(dispatcher.runs)
	if resumed, err := svc.ResumeDueTaskCoordinators(ctx, root.WorkspaceID, 10); err != nil || resumed != 1 {
		t.Fatalf("cancelled settlement checkpoint was not replayed: resumed=%d err=%v", resumed, err)
	}
	if len(dispatcher.runs) != runsBefore {
		t.Fatalf("settlement recovery created a Run: before=%d after=%d", runsBefore, len(dispatcher.runs))
	}
	if active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens); err != nil || active != 0 {
		t.Fatalf("replayed cancellation left active reservation: active=%d err=%v", active, err)
	}
	if _, err := store.TurnReceipts().GetPhase(ctx, turnKey, 6); err != nil {
		t.Fatalf("replayed cancellation did not append phase6: %v", err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := state.Data["control_action"]; pending {
		t.Fatalf("successful settlement replay did not clear durable checkpoint: %+v", state)
	}
}
