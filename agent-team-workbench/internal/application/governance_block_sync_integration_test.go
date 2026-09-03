package application_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// governanceBlockEventKey keeps the assertions below scoped to the canonical
// stream. Coordinator events use the state aggregate, while Goal/Todo events
// use their own aggregate identities; treating them as one key would hide a
// duplicate emitted against the wrong aggregate.
func governanceBlockEventKey(event *domain.CanonicalEvent) string {
	if event == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s|%s|%s", event.Type, event.Aggregate.Type, event.Aggregate.ID)
}

func TestEnsureGovernanceStateKeepsBlockedRootBlocked(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "blocked rebuild", RecordKind: domain.RecordKindTask,
		AutoCoordinate:     true,
		AcceptanceCriteria: []string{"blocked root must not reactivate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "test_blocked", Message: "operator stop", Source: "test",
	}, root.Version); err != nil {
		t.Fatal(err)
	}
	result, err := svc.EnsureGovernanceState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != domain.GoalBlocked || goal.CurrentTodoID == "" {
		t.Fatalf("blocked rebuild must preserve a blocked Goal/Todo chain: goal=%+v result=%+v", goal, result)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoBlocked || todo.Claim != nil {
		t.Fatalf("blocked rebuild must keep current Todo blocked and claim-free: %+v", todo)
	}
	if len(result.Issues) != 0 {
		t.Fatalf("already consistent blocked root should not report a rebuild issue: %+v", result.Issues)
	}
}

func TestEnsureBlockedRootMaterializesGoalTodoWithoutRun(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	now := time.Now().UTC()
	root := &domain.WorkItem{
		ID: "wi_blocked_rebuild_missing_goal", WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
		Title: "blocked root without governance", Status: domain.WorkItemBlocked,
		Priority: domain.PriorityMedium, AgentProfileID: workerID, AcceptanceCriteria: []string{"preserve blocked state"},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	result, err := svc.EnsureGovernanceState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.CreatedGoals != 1 || result.CreatedTodos != 1 || len(result.Issues) != 0 {
		t.Fatalf("blocked rebuild should materialize one blocked Goal/Todo without issue: %+v", result)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != domain.GoalBlocked || todo.Status != domain.TodoBlocked || todo.Claim != nil {
		t.Fatalf("materialized blocked governance state is not stopped: goal=%+v todo=%+v", goal, todo)
	}
	runs, err := store.Runs().ListByWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("blocked rebuild must not create a Run: %d", len(runs))
	}
}

func governanceBlockEventsSince(t *testing.T, ctx context.Context, svcStore application.Store,
	workspaceID string, after int64) []*domain.CanonicalEvent {
	t.Helper()
	events, err := svcStore.Events().Since(ctx, workspaceID, after, 1000)
	if err != nil {
		t.Fatalf("read canonical events: %v", err)
	}
	return events
}

func requireGovernanceBlockEventOnce(t *testing.T, events []*domain.CanonicalEvent,
	eventType, aggregateType, aggregateID string) *domain.CanonicalEvent {
	t.Helper()
	var match *domain.CanonicalEvent
	count := 0
	for _, event := range events {
		if event == nil || event.Type != eventType || event.Aggregate.Type != aggregateType ||
			event.Aggregate.ID != aggregateID {
			continue
		}
		match = event
		count++
	}
	if count != 1 {
		t.Fatalf("expected exactly one %s for %s/%s, got %d", eventType, aggregateType, aggregateID, count)
	}
	return match
}

func requireGovernanceBlockTransition(t *testing.T, events []*domain.CanonicalEvent,
	eventType, aggregateType, aggregateID string, from, to string) *domain.CanonicalEvent {
	t.Helper()
	event := requireGovernanceBlockEventOnce(t, events, eventType, aggregateType, aggregateID)
	if got := event.Data["from_state"]; got != from {
		t.Fatalf("%s %s/%s from_state=%v, want %q", eventType, aggregateType, aggregateID, got, from)
	}
	if got := event.Data["to_state"]; got != to {
		t.Fatalf("%s %s/%s to_state=%v, want %q", eventType, aggregateType, aggregateID, got, to)
	}
	return event
}

func assertGovernanceBlockEventSet(t *testing.T, events []*domain.CanonicalEvent,
	expected map[string]struct{}) {
	t.Helper()
	relevantTypes := map[string]struct{}{
		domain.EventWorkItemBlocked:    {},
		domain.EventWorkItemUnblocked:  {},
		domain.EventCoordinatorBlocked: {},
		domain.EventGoalStateChanged:   {},
		domain.EventTodoStateChanged:   {},
		domain.EventTodoClaimChanged:   {},
	}
	counts := map[string]int{}
	for _, event := range events {
		if event == nil {
			continue
		}
		if _, relevant := relevantTypes[event.Type]; !relevant {
			continue
		}
		key := governanceBlockEventKey(event)
		if _, expectedKey := expected[key]; !expectedKey {
			t.Fatalf("unexpected %s aggregate for precise block transition: %s", event.Type, key)
		}
		counts[key]++
	}
	for key := range expected {
		if counts[key] != 1 {
			t.Fatalf("canonical event %s count=%d, want exactly one", key, counts[key])
		}
	}
	for key, count := range counts {
		if count != 1 {
			t.Fatalf("canonical event %s count=%d, want exactly one", key, count)
		}
	}
}

func TestBlockAndUnblockEmitOnePreciseTransitionPerAggregate(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "block event precision", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"block and unblock events are precise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
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
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, todo.Version,
		time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	beforeSeq, err := store.Events().LatestSeq(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "manual_block", Message: "precise event test", Source: "test",
	}, root.Version)
	if err != nil {
		t.Fatal(err)
	}
	blockedRoot, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedGoal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedState, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedEvents := governanceBlockEventsSince(t, ctx, store, wsID, beforeSeq)
	assertGovernanceBlockEventSet(t, blockedEvents, map[string]struct{}{
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventWorkItemBlocked,
			Aggregate: domain.AggregateRef{Type: domain.AggregateWorkItem, ID: root.ID}}): {},
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventCoordinatorBlocked,
			Aggregate: domain.AggregateRef{Type: domain.AggregateTaskCoordinator, ID: state.ID}}): {},
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventGoalStateChanged,
			Aggregate: domain.AggregateRef{Type: domain.AggregateGoal, ID: goal.ID}}): {},
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventTodoStateChanged,
			Aggregate: domain.AggregateRef{Type: domain.AggregateTodo, ID: todo.ID}}): {},
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventTodoClaimChanged,
			Aggregate: domain.AggregateRef{Type: domain.AggregateTodo, ID: todo.ID}}): {},
	})
	goalBlockedEvent := requireGovernanceBlockTransition(t, blockedEvents,
		domain.EventGoalStateChanged, domain.AggregateGoal, goal.ID,
		string(domain.GoalActive), string(domain.GoalBlocked))
	todoBlockedEvent := requireGovernanceBlockTransition(t, blockedEvents,
		domain.EventTodoStateChanged, domain.AggregateTodo, todo.ID,
		string(domain.TodoClaimed), string(domain.TodoBlocked))
	claimReleasedEvent := requireGovernanceBlockEventOnce(t, blockedEvents,
		domain.EventTodoClaimChanged, domain.AggregateTodo, todo.ID)
	if claimReleasedEvent.Data["claim_state"] != "released" ||
		fmt.Sprint(claimReleasedEvent.Data["claim_version"]) != fmt.Sprint(claimed.ClaimVersion+1) {
		t.Fatalf("block must release the claimed Todo exactly once with the next generation: %+v", claimReleasedEvent.Data)
	}
	workItemEvent := requireGovernanceBlockEventOnce(t, blockedEvents, domain.EventWorkItemBlocked,
		domain.AggregateWorkItem, root.ID)
	coordinatorEvent := requireGovernanceBlockEventOnce(t, blockedEvents, domain.EventCoordinatorBlocked,
		domain.AggregateTaskCoordinator, state.ID)
	if goalBlockedEvent.Aggregate.Version != blockedGoal.Version ||
		todoBlockedEvent.Aggregate.Version != blockedTodo.Version-1 ||
		claimReleasedEvent.Aggregate.Version != blockedTodo.Version ||
		workItemEvent.Aggregate.Version != blockedRoot.Version ||
		coordinatorEvent.Aggregate.Version != blockedState.Version {
		t.Fatalf("block event aggregate versions do not match committed rows: events work_item=%d coordinator=%d goal=%d todo_state=%d todo_claim=%d; rows root=%d state=%d goal=%d todo=%d",
			workItemEvent.Aggregate.Version, coordinatorEvent.Aggregate.Version, goalBlockedEvent.Aggregate.Version,
			todoBlockedEvent.Aggregate.Version, claimReleasedEvent.Aggregate.Version,
			blockedRoot.Version, blockedState.Version, blockedGoal.Version, blockedTodo.Version)
	}

	beforeSeq, err = store.Events().LatestSeq(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	unblocked, err := svc.UnblockWorkItem(ctx, blocked.ID, blockedRoot.Version)
	if err != nil {
		t.Fatal(err)
	}
	finalRoot, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalGoal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	finalTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	unblockEvents := governanceBlockEventsSince(t, ctx, store, wsID, beforeSeq)
	assertGovernanceBlockEventSet(t, unblockEvents, map[string]struct{}{
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventWorkItemUnblocked,
			Aggregate: domain.AggregateRef{Type: domain.AggregateWorkItem, ID: root.ID}}): {},
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventGoalStateChanged,
			Aggregate: domain.AggregateRef{Type: domain.AggregateGoal, ID: goal.ID}}): {},
		governanceBlockEventKey(&domain.CanonicalEvent{Type: domain.EventTodoStateChanged,
			Aggregate: domain.AggregateRef{Type: domain.AggregateTodo, ID: todo.ID}}): {},
	})
	goalResumedEvent := requireGovernanceBlockTransition(t, unblockEvents,
		domain.EventGoalStateChanged, domain.AggregateGoal, goal.ID,
		string(domain.GoalBlocked), string(domain.GoalActive))
	todoResumedEvent := requireGovernanceBlockTransition(t, unblockEvents,
		domain.EventTodoStateChanged, domain.AggregateTodo, todo.ID,
		string(domain.TodoBlocked), string(domain.TodoPending))
	if got := requireGovernanceBlockEventOnce(t, unblockEvents, domain.EventWorkItemUnblocked,
		domain.AggregateWorkItem, root.ID).Aggregate.Version; got != finalRoot.Version {
		t.Fatalf("unblock WorkItem event aggregate version=%d, want %d", got, finalRoot.Version)
	}
	if goalResumedEvent.Aggregate.Version != finalGoal.Version || todoResumedEvent.Aggregate.Version != finalTodo.Version {
		t.Fatalf("unblock governance event versions do not match committed rows: goal=%+v todo=%+v", finalGoal, finalTodo)
	}
	if unblocked.ID != root.ID {
		t.Fatalf("unblock returned the wrong WorkItem: %+v", unblocked)
	}
}

func TestBlockReleaseAndReclaimAdvancesTodoClaimVersion(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "claim generation ABA", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"claim generations never reuse an old owner"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
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
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, todo.Version,
		time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if first.ClaimVersion != 1 || first.Claim == nil || first.Claim.Version != first.ClaimVersion {
		t.Fatalf("first claim must allocate generation 1: %+v", first)
	}
	blocked, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "claim_generation_block", Message: "release claim", Source: "test",
	}, root.Version)
	if err != nil {
		t.Fatal(err)
	}
	blockedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedTodo.Claim != nil || blockedTodo.Status != domain.TodoBlocked ||
		blockedTodo.ClaimVersion != first.ClaimVersion+1 {
		t.Fatalf("blocking must release the claim and advance its generation: %+v", blockedTodo)
	}
	_, err = svc.UnblockWorkItem(ctx, blocked.ID, blocked.Version)
	if err != nil {
		t.Fatal(err)
	}
	resumedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumedTodo.Claim != nil || resumedTodo.Status != domain.TodoPending ||
		resumedTodo.ClaimVersion != blockedTodo.ClaimVersion {
		t.Fatalf("unblock must preserve the released generation without reviving the claim: %+v", resumedTodo)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, resumedTodo.Version,
		time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.Claim == nil || second.ClaimVersion != resumedTodo.ClaimVersion+1 ||
		second.Claim.Version != second.ClaimVersion {
		t.Fatalf("reclaim must allocate a strictly new generation after ABA: %+v", second)
	}
}

func TestGoalWaitingBlocksAndUnblocksToActivePending(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "waiting governance block", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"waiting goals resume as active"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
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
	waitingGoal, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	waitingTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waitingGoal.Status != domain.GoalWaiting || waitingGoal.Phase != "execution" ||
		waitingTodo.Status != domain.TodoWaiting {
		t.Fatalf("precondition must be GoalWaiting/TodoWaiting: goal=%+v todo=%+v", waitingGoal, waitingTodo)
	}
	blocked, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "waiting_block", Message: "block a waiting governance turn", Source: "test",
	}, root.Version)
	if err != nil {
		t.Fatal(err)
	}
	blockedGoal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedGoal.Status != domain.GoalBlocked || blockedGoal.Phase != "blocked" ||
		blockedTodo.Status != domain.TodoBlocked || blockedTodo.Claim != nil {
		t.Fatalf("GoalWaiting must transition to blocked with a claim-free Todo: goal=%+v todo=%+v", blockedGoal, blockedTodo)
	}
	if _, err := svc.UnblockWorkItem(ctx, blocked.ID, blocked.Version); err != nil {
		t.Fatal(err)
	}
	resumedGoal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumedGoal.Status != domain.GoalActive || resumedGoal.Phase != "execution" ||
		resumedTodo.Status != domain.TodoPending || resumedTodo.Claim != nil {
		t.Fatalf("unblock must resume GoalWaiting path as active/pending: goal=%+v todo=%+v", resumedGoal, resumedTodo)
	}
}

func TestChildWorkItemBlockUnblockProjectsRootGovernanceWithoutNewGoalOrTodo(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "child governance root", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"child blockers use root governance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todosBefore, err := store.Todos().ListByGoal(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "child blocker", ParentID: root.ID, RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TaskCoordinators().GetState(ctx, child.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("child must not receive a second Coordinator state: %v", err)
	}
	goalsAfterChild, err := store.Goals().List(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if len(goalsAfterChild) != 1 || goalsAfterChild[0].ID != goal.ID {
		t.Fatalf("child creation must not create another Goal: %+v", goalsAfterChild)
	}
	todosAfterChild, err := store.Todos().ListByGoal(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(todosAfterChild) != len(todosBefore) || todosAfterChild[0].ID != todosBefore[0].ID {
		t.Fatalf("child creation must not create another Todo: before=%+v after=%+v", todosBefore, todosAfterChild)
	}

	rootBeforeBlock, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedChild, err := svc.BlockWorkItem(ctx, child.ID, application.BlockParams{
		Code: "child_block", Message: "child requires user attention", Source: "test",
	}, child.Version)
	if err != nil {
		t.Fatal(err)
	}
	blockedRoot, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedGoal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blockedTodo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	blockedState, err := store.TaskCoordinators().GetStateForWorkItem(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedChild.Status != domain.WorkItemBlocked || blockedRoot.Status != rootBeforeBlock.Status ||
		blockedState.Status != domain.CoordinatorBlocked || blockedGoal.Status != domain.GoalBlocked ||
		blockedTodo.Status != domain.TodoBlocked {
		t.Fatalf("child block must affect root governance without blocking root WorkItem: child=%+v root=%+v state=%+v goal=%+v todo=%+v",
			blockedChild, blockedRoot, blockedState, blockedGoal, blockedTodo)
	}
	if blockedState.RootWorkItemID != root.ID || blockedState.ID == "" {
		t.Fatalf("child block resolved the wrong Coordinator state: %+v", blockedState)
	}
	if _, err := svc.UnblockWorkItem(ctx, child.ID, blockedChild.Version); err != nil {
		t.Fatal(err)
	}
	resumedChild, err := store.WorkItems().Get(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumedRoot, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumedGoal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	resumedTodo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if resumedChild.Status != domain.WorkItemInProgress || resumedRoot.Status != rootBeforeBlock.Status ||
		resumedGoal.Status != domain.GoalActive || resumedTodo.Status != domain.TodoPending || resumedTodo.Claim != nil {
		t.Fatalf("child unblock must resume root governance and leave root WorkItem intact: child=%+v root=%+v goal=%+v todo=%+v",
			resumedChild, resumedRoot, resumedGoal, resumedTodo)
	}
	goalsAfter, err := store.Goals().List(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	todosAfter, err := store.Todos().ListByGoal(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(goalsAfter) != 1 || goalsAfter[0].ID != goal.ID || len(todosAfter) != len(todosBefore) ||
		todosAfter[0].ID != todosBefore[0].ID {
		t.Fatalf("child block/unblock must not create Goal/Todo rows: goals=%+v todos=%+v", goalsAfter, todosAfter)
	}
}

func TestBlockWorkItemGovernanceFailureRollsBackEveryProjectionAndEvent(t *testing.T) {
	ctx, db, svc, store, _, wsID, _ := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "block transaction rollback", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"block governance mutation is atomic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
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
	beforeEvents, err := store.Events().Since(ctx, wsID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var beforeOutbox int
	if err := db.QueryRow(`SELECT count(*) FROM outbox_messages`).Scan(&beforeOutbox); err != nil {
		t.Fatal(err)
	}
	var beforeBlockers int
	if err := db.QueryRow(`SELECT count(*) FROM blockers WHERE work_item_id=?`, root.ID).Scan(&beforeBlockers); err != nil {
		t.Fatal(err)
	}
	rootBefore, stateBefore, goalBefore, todoBefore := *root, *state, *goal, *todo

	// The trigger aborts the first governance Todo write. Because BlockWorkItem
	// owns one Store.InTx, all preceding WorkItem/Coordinator/blocker/event
	// writes must disappear with the same rollback.
	if _, err := db.Exec(`CREATE TRIGGER governance_block_sync_injected_failure
		BEFORE UPDATE ON goal_todos
		BEGIN SELECT RAISE(ABORT, 'injected governance todo update failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "injected_failure", Message: "the Todo write must fail", Source: "test",
	}, root.Version); err == nil {
		t.Fatal("BlockWorkItem must surface the injected Todo update failure")
	}

	rootAfter, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goalAfter, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todoAfter, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rootBefore, *rootAfter) || !reflect.DeepEqual(stateBefore, *stateAfter) ||
		!reflect.DeepEqual(goalBefore, *goalAfter) || !reflect.DeepEqual(todoBefore, *todoAfter) {
		t.Fatalf("injected governance failure leaked a partial row mutation:\nroot before=%+v after=%+v\nstate before=%+v after=%+v\ngoal before=%+v after=%+v\ntodo before=%+v after=%+v",
			rootBefore, *rootAfter, stateBefore, *stateAfter, goalBefore, *goalAfter, todoBefore, *todoAfter)
	}
	afterEvents, err := store.Events().Since(ctx, wsID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEvents) != len(beforeEvents) {
		t.Fatalf("rollback must remove stream events: before=%d after=%d", len(beforeEvents), len(afterEvents))
	}
	var afterOutbox int
	if err := db.QueryRow(`SELECT count(*) FROM outbox_messages`).Scan(&afterOutbox); err != nil {
		t.Fatal(err)
	}
	if afterOutbox != beforeOutbox {
		t.Fatalf("rollback must remove outbox rows: before=%d after=%d", beforeOutbox, afterOutbox)
	}
	var afterBlockers int
	if err := db.QueryRow(`SELECT count(*) FROM blockers WHERE work_item_id=?`, root.ID).Scan(&afterBlockers); err != nil {
		t.Fatal(err)
	}
	if afterBlockers != beforeBlockers {
		t.Fatalf("rollback must remove the inserted blocker: before=%d after=%d", beforeBlockers, afterBlockers)
	}
}
