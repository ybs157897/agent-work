package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type userActionFixture struct {
	ctx   context.Context
	svc   *application.Service
	store application.Store
	wsID  string
	root  *domain.WorkItem
	goal  *domain.Goal
	todo  *domain.Todo
}

func newUserActionFixture(t *testing.T) *userActionFixture {
	t.Helper()
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "user action checkpoint", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"user action is replayable"},
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
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, state.CoordinatorAgentID, todo.Version,
		time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := claimed.Transition(domain.TodoBlocked, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Todos().Update(ctx, claimed, claimed.Version-1); err != nil {
		t.Fatal(err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorBlocked
	state.Phase = "blocked"
	state.CurrentRunID = ""
	state.CurrentAction = "resolve user action"
	state.BlockerCode = "user_action_required"
	state.BlockerMessage = "user input required"
	state.NextActionAt = nil
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.Todos().Get(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &userActionFixture{ctx: ctx, svc: svc, store: store, wsID: wsID, root: root, goal: goal, todo: claimed}
}

func TestResolveTodoUserActionScopesReplayToTodoAndReleasesClaim(t *testing.T) {
	f := newUserActionFixture(t)
	beforeVersion := f.todo.Version
	params := application.ResolveTodoUserActionParams{
		TodoID: f.todo.ID, Resolution: "用户确认继续", ActorID: "user_review",
		ExpectedVersion: beforeVersion, ClientKey: "resolve-1",
	}
	resolved, err := f.svc.ResolveTodoUserAction(f.ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != domain.TodoPending || resolved.Claim != nil {
		t.Fatalf("resolved Todo must be pending and claim-free: %+v", resolved)
	}
	state, err := f.store.TaskCoordinators().GetState(f.ctx, f.root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorQueued || state.Data["user_action_effects"] == nil {
		t.Fatalf("user action must persist a queued checkpoint and replay effect: %+v", state)
	}
	headers, err := f.store.TurnReceipts().ListHeadersByGoal(f.ctx, f.goal.ID)
	if err != nil || len(headers) != 1 {
		t.Fatalf("user action must allocate one canonical control receipt: headers=%d err=%v", len(headers), err)
	}
	phases, err := f.store.TurnReceipts().ListPhases(f.ctx, headers[0].TurnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("user action control receipt must be complete through phase7: phases=%d err=%v", len(phases), err)
	}
	decision, _ := phases[0].Payload["turn_decision"].(map[string]any)
	if decision["decision"] != string(domain.TurnDecisionUserAction) {
		t.Fatalf("user action receipt phase1 must carry typed decision: %#v", phases[0].Payload)
	}

	firstEvents, err := f.store.Events().Since(f.ctx, f.wsID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	claimReleases, stateChanges := 0, 0
	for _, event := range firstEvents {
		if event == nil || event.AggregateID != f.todo.ID {
			continue
		}
		if event.Type == domain.EventTodoClaimChanged && event.Data["claim_state"] == "released" {
			claimReleases++
		}
		if event.Type == domain.EventTodoStateChanged && event.Data["from_state"] == string(domain.TodoBlocked) &&
			event.Data["to_state"] == string(domain.TodoPending) {
			stateChanges++
		}
	}
	if claimReleases != 1 || stateChanges != 1 {
		t.Fatalf("user action must publish one precise claim release and real state transition: claims=%d states=%d", claimReleases, stateChanges)
	}

	replayed, err := f.svc.ResolveTodoUserAction(f.ctx, params)
	if err != nil || replayed.ID != resolved.ID || replayed.Version != resolved.Version {
		t.Fatalf("same Todo/payload replay must return the original effect: replay=%+v err=%v", replayed, err)
	}
	if _, err := f.svc.ResolveTodoUserAction(f.ctx, application.ResolveTodoUserActionParams{
		TodoID: f.todo.ID, Resolution: "另一条解决路径", ActorID: params.ActorID,
		ExpectedVersion: beforeVersion, ClientKey: params.ClientKey,
	}); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same client key with changed payload must conflict: %v", err)
	}
	secondEvents, err := f.store.Events().Since(f.ctx, f.wsID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondEvents) != len(firstEvents) {
		t.Fatalf("replay must not publish another claim/state event: before=%d after=%d", len(firstEvents), len(secondEvents))
	}
}

func TestResolveTodoUserActionRequiresActiveCurrentTodo(t *testing.T) {
	f := newUserActionFixture(t)
	secondary := *f.todo
	secondary.ID = domain.NewID(domain.PrefixTodo)
	secondary.Status = domain.TodoBlocked
	secondary.Instruction = "non-current action"
	secondary.Claim = nil
	secondary.ClaimVersion = 0
	secondary.LastTurnSeq = 0
	secondary.Version = 1
	secondary.CreatedAt = time.Now().UTC()
	secondary.UpdatedAt = secondary.CreatedAt
	if err := f.store.Todos().Create(f.ctx, &secondary); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ResolveTodoUserAction(f.ctx, application.ResolveTodoUserActionParams{
		TodoID: secondary.ID, Resolution: "resolve", ActorID: "user_review", ClientKey: "secondary",
	}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("non-current Todo must be rejected even with a valid checkpoint: %v", err)
	}

	goal, err := f.store.Goals().Get(f.ctx, f.goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PauseGoal(f.ctx, goal.ID, goal.Version); err != nil {
		t.Fatal(err)
	}
	current, err := f.store.Todos().Get(f.ctx, f.todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ResolveTodoUserAction(f.ctx, application.ResolveTodoUserActionParams{
		TodoID: current.ID, Resolution: "resolve after pause", ActorID: "user_review", ClientKey: "paused",
	}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("waiting Goal must not resolve a user action outside active Goal: %v", err)
	}
}
