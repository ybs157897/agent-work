package application_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func seedGovernanceService(t *testing.T) (context.Context, *sql.DB, *application.Service, *sqlstore.Store, *captureDispatcher, string, string) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	workspaceID := "ws_governance_app"
	agentID := "agent_governance_owner"
	rootID := "wi_governance_root"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: "governance", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: agentID, WorkspaceID: workspaceID, Name: "Owner", Role: "lead",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{
		ID: rootID, WorkspaceID: workspaceID, RecordKind: domain.RecordKindTask,
		Title: "Governed root", Status: domain.WorkItemTodo, Priority: domain.PriorityHigh,
		AgentProfileID: agentID, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return ctx, db, svc, store, dispatcher, workspaceID, rootID
}

func createAndStartGovernanceGoal(t *testing.T, ctx context.Context, svc *application.Service, workspaceID, rootID string) (*domain.Goal, *domain.Todo) {
	t.Helper()
	goal, err := svc.CreateGoal(ctx, workspaceID, application.CreateGoalParams{
		RootWorkItemID: rootID,
		Objective:      "deliver the native governance base",
		AcceptanceContract: []string{
			"Goal and Todo survive restart",
			"no Run is created by governance state creation",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err = svc.StartGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := svc.GetTodo(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	return goal, todo
}

func governanceEventCount(t *testing.T, db *sql.DB, eventType string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM stream_events WHERE event_type=?`, eventType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestGovernanceCreateStartIsIdempotentAndEmitsOutbox(t *testing.T) {
	ctx, db, svc, store, dispatcher, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()

	params := application.CreateGoalParams{
		RootWorkItemID: rootID,
		Objective:      "deliver the native governance base",
		AcceptanceContract: []string{
			"Goal and Todo survive restart",
			"no Run is created by governance state creation",
		},
	}
	first, err := svc.CreateGoal(ctx, workspaceID, params)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.CreateGoal(ctx, workspaceID, params)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID || governanceEventCount(t, db, domain.EventGoalCreated) != 1 {
		t.Fatalf("same root/intent must replay one Goal/event: first=%s replay=%s", first.ID, replay.ID)
	}
	params.Objective = "different intent on the same root"
	if _, err := svc.CreateGoal(ctx, workspaceID, params); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same root with different intent must conflict, got %v", err)
	}

	started, err := svc.StartGoal(ctx, first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != domain.GoalActive || started.CurrentTodoID == "" {
		t.Fatalf("StartGoal did not create active Todo: %+v", started)
	}
	todo, err := svc.GetTodo(ctx, started.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoPending || todo.GoalID != started.ID || todo.DecisionScope.AgentIDs[0] != "agent_governance_owner" {
		t.Fatalf("unexpected initial Todo: %+v", todo)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("Goal/Todo state creation must not dispatch a Run: %d", len(dispatcher.runs))
	}
	for table, want := range map[string]int{"plans": 0, "execution_runs": 0, "goal_todos": 1} {
		var got int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count=%d want=%d", table, got, want)
		}
	}
	if governanceEventCount(t, db, domain.EventTodoCreated) != 1 ||
		governanceEventCount(t, db, domain.EventGoalStateChanged) != 1 {
		t.Fatal("StartGoal must atomically publish Todo created and Goal state events")
	}
	var governanceEvents, governanceOutbox int
	if err := db.QueryRow(`SELECT count(*) FROM stream_events
		WHERE event_type IN ('goal.created','goal.state_changed','todo.created')`).Scan(&governanceEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM outbox_messages o
		JOIN stream_events e ON e.event_id=o.event_id
		WHERE e.event_type IN ('goal.created','goal.state_changed','todo.created')`).Scan(&governanceOutbox); err != nil {
		t.Fatal(err)
	}
	if governanceEvents != 3 || governanceOutbox != governanceEvents {
		t.Fatalf("governance event/outbox must be one-for-one: events=%d outbox=%d", governanceEvents, governanceOutbox)
	}
	if goals, err := store.Goals().List(ctx, workspaceID); err != nil || len(goals) != 1 {
		t.Fatalf("Goal restart read failed: goals=%+v err=%v", goals, err)
	}
	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	rootExpected := root.Version
	if err := root.Transition(domain.WorkItemCancelled, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, root, rootExpected); err != nil {
		t.Fatal(err)
	}
	terminalReplay, err := svc.CreateGoal(ctx, workspaceID, application.CreateGoalParams{
		RootWorkItemID: rootID,
		Objective:      "deliver the native governance base",
		AcceptanceContract: []string{
			"Goal and Todo survive restart",
			"no Run is created by governance state creation",
		},
	})
	if err != nil || terminalReplay.ID != first.ID || governanceEventCount(t, db, domain.EventGoalCreated) != 1 {
		t.Fatalf("existing Goal must replay after root becomes terminal: goal=%+v err=%v", terminalReplay, err)
	}
}

func TestStartGoalFreezesSystemCoordinatorAndEnabledWorkerRoster(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	workspaceID := "ws_governance_roster"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: "governance roster", Timezone: "UTC", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().EnsureConfig(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	createAgent := func(id string, availability domain.AgentAvailability, kind domain.AgentProfileKind) {
		t.Helper()
		if err := store.Agents().Create(ctx, &domain.AgentProfile{
			ID: id, WorkspaceID: workspaceID, Kind: kind, Name: id, Role: "developer",
			Availability: availability, Presence: domain.PresenceIdle,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Create in reverse lexical order to prove the persisted creation order is
	// not the DecisionScope order.
	createAgent("agent_roster_zeta", domain.AgentEnabled, domain.AgentProfileKindUser)
	createAgent("agent_roster_disabled", domain.AgentDisabled, domain.AgentProfileKindUser)
	createAgent("agent_roster_alpha", domain.AgentEnabled, domain.AgentProfileKindUser)
	rootID := "wi_governance_roster"
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{
		ID: rootID, WorkspaceID: workspaceID, RecordKind: domain.RecordKindTask,
		Title: "Roster root", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
		AgentProfileID: config.AgentProfileID, AcceptanceCriteria: []string{"roster is frozen"},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	goal, err := svc.CreateGoal(ctx, workspaceID, application.CreateGoalParams{
		RootWorkItemID: rootID, Objective: "freeze the worker roster",
		AcceptanceContract: []string{"scope is deterministic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.StartGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := svc.GetTodo(ctx, started.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{config.AgentProfileID, "agent_roster_alpha", "agent_roster_zeta"}
	if !slices.Equal(todo.DecisionScope.AgentIDs, want) {
		t.Fatalf("initial DecisionScope must be owner-first, sorted, enabled-only snapshot: got=%v want=%v", todo.DecisionScope.AgentIDs, want)
	}

	// A later roster change must not silently expand an already-created Todo.
	createAgent("agent_roster_later", domain.AgentEnabled, domain.AgentProfileKindUser)
	reloaded, err := svc.GetTodo(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reloaded.DecisionScope.AgentIDs, want) {
		t.Fatalf("existing Todo scope changed after a later Agent was enabled: got=%v want=%v", reloaded.DecisionScope.AgentIDs, want)
	}
}

func TestStartGoalRejectsDisabledOrdinaryOwner(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	owner, err := store.Agents().Get(ctx, "agent_governance_owner")
	if err != nil {
		t.Fatal(err)
	}
	expected := owner.Version
	owner.SetAvailability(domain.AgentDisabled, time.Now().UTC())
	if err := store.Agents().Update(ctx, owner, expected); err != nil {
		t.Fatal(err)
	}
	goal, err := svc.CreateGoal(ctx, workspaceID, application.CreateGoalParams{
		RootWorkItemID: rootID, Objective: "disabled owner must fail closed",
		AcceptanceContract: []string{"no Todo is created"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartGoal(ctx, goal.ID, goal.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("disabled ordinary owner must fail closed, got %v", err)
	}
	todos, err := store.Todos().ListByGoal(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 0 {
		t.Fatalf("failed StartGoal must not leave a partial Todo: %+v", todos)
	}
}

func TestCreateRootTaskAutomaticallyEnsuresGovernanceState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	workspaceID, agentID := "ws_governance_state_hook", "agent_governance_state_hook"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: "governance state hook", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: agentID, WorkspaceID: workspaceID, Name: "Governance State Hook Owner", Role: "lead",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		RecordKind: domain.RecordKindTask, Title: "Hooked root",
		Description: "project this root into native governance", AgentProfileID: agentID,
		AcceptanceCriteria: []string{"Goal and Todo state are durable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != domain.GoalActive || goal.CurrentTodoID == "" {
		t.Fatalf("root Task hook did not activate Goal: %+v", goal)
	}
	if _, err := store.Todos().Get(ctx, goal.CurrentTodoID); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("governance state hook dispatched %d Runs", len(dispatcher.runs))
	}
}

func TestGovernanceClaimPauseResumeCancelNeverTouchesRunnerLease(t *testing.T) {
	ctx, db, svc, _, dispatcher, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)

	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != domain.TodoClaimed || claimed.Claim == nil || claimed.ClaimVersion != 1 {
		t.Fatalf("claim failed: %+v", claimed)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != domain.GoalWaiting {
		t.Fatalf("pause must map Goal to waiting: %+v", paused)
	}
	waiting, err := svc.GetTodo(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != domain.TodoWaiting || waiting.Claim == nil {
		t.Fatalf("pause must retain claim and map Todo to waiting: %+v", waiting)
	}
	resumed, err := svc.ResumeGoal(ctx, goal.ID, paused.Version)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != domain.GoalActive {
		t.Fatalf("resume must reactivate Goal: %+v", resumed)
	}
	reclaimed, err := svc.GetTodo(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Status != domain.TodoClaimed || reclaimed.Claim == nil {
		t.Fatalf("valid retained claim must resume as claimed: %+v", reclaimed)
	}
	cancelled, err := svc.CancelGoal(ctx, goal.ID, resumed.Version)
	if err != nil {
		t.Fatal(err)
	}
	finalTodo, err := svc.GetTodo(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != domain.GoalCancelled || finalTodo.Status != domain.TodoCancelled || finalTodo.Claim != nil {
		t.Fatalf("cancel must terminalize governance only: goal=%+v todo=%+v", cancelled, finalTodo)
	}
	if finalTodo.ClaimVersion != 2 {
		t.Fatalf("claim release on cancel must bump generation: %+v", finalTodo)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("Goal lifecycle must not dispatch/cancel Runs: %d", len(dispatcher.runs))
	}
	var leases int
	if err := db.QueryRow(`SELECT count(*) FROM run_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("governance claim must not create Runner lease: %d", leases)
	}
}

func governanceDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func TestGovernanceAdmissionAndReceiptPhaseReplayAreCanonical(t *testing.T) {
	ctx, db, svc, store, dispatcher, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	_, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	params := application.AdmitTurnParams{
		GoalID: claimed.GoalID, TodoID: claimed.ID, OwnerAgentID: "agent_governance_owner",
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: governanceDigest('a'), AdmissionClientKey: "admit-app-1",
	}
	header, err := svc.AdmitTurn(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if header.TurnKey.TurnSeq != 1 || !domain.ValidCanonicalDigest(header.CanonicalDigest) {
		t.Fatalf("invalid admitted header: %+v", header)
	}
	wantDigest, err := application.ComputeTurnReceiptHeaderDigest(header)
	if err != nil || wantDigest != header.CanonicalDigest {
		t.Fatalf("stored Header digest is not RFC8785-derived: want=%q got=%q err=%v", wantDigest, header.CanonicalDigest, err)
	}
	replayed, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: params.GoalID, TodoID: params.TodoID, OwnerAgentID: params.OwnerAgentID,
		ExpectedTodoVersion: 999, Attempt: params.Attempt, SchemaVersion: params.SchemaVersion,
		InputSnapshotDigest: params.InputSnapshotDigest, AdmissionClientKey: params.AdmissionClientKey,
	})
	if err != nil || !replayed.TurnKey.Equal(header.TurnKey) || replayed.CanonicalDigest != header.CanonicalDigest {
		t.Fatalf("admission replay mismatch: got=%+v err=%v", replayed, err)
	}
	conflict := params
	conflict.InputSnapshotDigest = governanceDigest('b')
	if _, err := svc.AdmitTurn(ctx, conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same admission key with different payload must conflict, got %v", err)
	}

	phase := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"z": []any{true, 4.5}, "a": map[string]any{"b": "value", "a": 1}},
	}
	appended, err := svc.AppendTurnReceiptPhase(ctx, phase)
	if err != nil {
		t.Fatal(err)
	}
	phaseReplay := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"a": map[string]any{"a": 1, "b": "value"}, "z": []any{true, 4.5}},
	}
	replayedPhase, err := svc.AppendTurnReceiptPhase(ctx, phaseReplay)
	if err != nil || replayedPhase.CanonicalDigest != appended.CanonicalDigest || !replayedPhase.CreatedAt.Equal(appended.CreatedAt) {
		t.Fatalf("phase replay mismatch: got=%+v err=%v", replayedPhase, err)
	}
	changedPhase := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"different": true},
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, changedPhase); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same phase identity with different canonical payload must conflict, got %v", err)
	}
	gap := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 3, Phase: domain.TurnReceiptPhaseDurableWriteback,
		Payload: map[string]any{"skip": true},
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, gap); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("phase gap must fail closed, got %v", err)
	}

	receiptEvents, err := store.Events().Since(ctx, workspaceID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var receiptCount, replayCount, conflictCount int
	for _, event := range receiptEvents {
		if event == nil || event.Type != domain.EventTurnReceiptAppended {
			continue
		}
		receiptCount++
		switch event.Data["outcome"] {
		case "replayed":
			replayCount++
		case "conflict":
			conflictCount++
		}
	}
	if receiptCount != 6 || replayCount != 2 || conflictCount != 2 {
		t.Fatalf("receipt append-attempt outcomes must be durable: total=%d replay=%d conflict=%d", receiptCount, replayCount, conflictCount)
	}
	var headers, phases int
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_headers`).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_phases`).Scan(&phases); err != nil {
		t.Fatal(err)
	}
	if headers != 1 || phases != 1 || len(dispatcher.runs) != 0 {
		t.Fatalf("receipt replay duplicated state or dispatched: headers=%d phases=%d runs=%d", headers, phases, len(dispatcher.runs))
	}
}

func TestGovernanceAdmissionRejectsExpiredClaim(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	_, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	now := time.Now().UTC()
	claimed, err := store.Todos().Claim(ctx, todo.ID, "agent_governance_owner",
		now.Add(-2*time.Hour), now.Add(-time.Hour), todo.Version)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: claimed.GoalID, TodoID: claimed.ID, OwnerAgentID: "agent_governance_owner",
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: governanceDigest('e'), AdmissionClientKey: "expired-claim",
	})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("expired governance claim must not admit a turn, got %v", err)
	}
	var headers int
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_headers WHERE todo_id=?`, todo.ID).Scan(&headers); err != nil {
		t.Fatal(err)
	}
	if headers != 0 {
		t.Fatalf("expired claim created %d headers", headers)
	}
}

func TestGovernanceCancelRunningTodoReleasesClaimWithoutCancellingRun(t *testing.T) {
	ctx, db, svc, _, dispatcher, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: todo.ID, OwnerAgentID: "agent_governance_owner",
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: governanceDigest('f'), AdmissionClientKey: "cancel-running",
	}); err != nil {
		t.Fatal(err)
	}
	running, err := svc.GetTodo(ctx, todo.ID)
	if err != nil || running.Status != domain.TodoRunning || running.Claim == nil {
		t.Fatalf("admission did not produce claimed running Todo: %+v err=%v", running, err)
	}
	cancelledGoal, err := svc.CancelGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	cancelledTodo, err := svc.GetTodo(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelledGoal.Status != domain.GoalCancelled || cancelledTodo.Status != domain.TodoCancelled ||
		cancelledTodo.Claim != nil || cancelledTodo.ClaimVersion != running.ClaimVersion+1 {
		t.Fatalf("running cancel must release governance claim atomically: goal=%+v todo=%+v", cancelledGoal, cancelledTodo)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("governance cancel touched dispatcher: %d", len(dispatcher.runs))
	}
	var runs, leases int
	if err := db.QueryRow(`SELECT count(*) FROM execution_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM run_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || leases != 0 {
		t.Fatalf("governance cancel created/cancelled execution state: runs=%d leases=%d", runs, leases)
	}
}

func TestGovernanceStateRebuildSurvivesRestartAndReportsInconsistency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "governance-restart.db")
	open := func(apply bool) (*sql.DB, *sqlstore.Store, *application.Service, *captureDispatcher) {
		t.Helper()
		db, err := sqlstore.Open(ctx, "sqlite://"+path)
		if err != nil {
			t.Fatal(err)
		}
		if apply {
			if err := migtest.ApplyAll(db); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
		}
		store := sqlstore.New(db)
		dispatcher := &captureDispatcher{}
		return db, store, application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry()), dispatcher
	}

	db, store, svc, dispatcher := open(true)
	now := time.Now().UTC()
	workspaceID, rootID, agentID := "ws_governance_state_restart", "wi_governance_state_restart", "agent_governance_state_restart"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: "governance state", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: agentID, WorkspaceID: workspaceID, Name: "Governance State Owner", Role: "lead",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{
		ID: rootID, WorkspaceID: workspaceID, RecordKind: domain.RecordKindTask,
		Title: "Governance state root", Description: "rebuild governance state deterministically",
		Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, AgentProfileID: agentID,
		AcceptanceCriteria: []string{"restart preserves canonical receipts"},
		Version:            1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := svc.RebuildGovernanceState(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if first.CreatedGoals != 1 || first.CreatedTodos != 1 || len(first.Issues) != 0 {
		t.Fatalf("initial governance state rebuild mismatch: %+v", first)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, agentID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: todo.ID, OwnerAgentID: agentID, ExpectedTodoVersion: claimed.Version,
		Attempt: 1, SchemaVersion: "turn-receipt/v1", InputSnapshotDigest: governanceDigest('c'),
		AdmissionClientKey: "restart-admission",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"decision": "state"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("governance state setup dispatched %d Runs", len(dispatcher.runs))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, store, svc, dispatcher = open(false)
	defer db.Close()
	second, err := svc.RebuildGovernanceState(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedGoals != 0 || second.CreatedTodos != 0 || len(second.Issues) != 0 {
		t.Fatalf("restart governance state rebuild must be idempotent: %+v", second)
	}
	reloadedHeader, err := store.TurnReceipts().GetHeader(ctx, header.TurnKey)
	if err != nil || reloadedHeader.CanonicalDigest != header.CanonicalDigest {
		t.Fatalf("Header did not survive restart: %+v err=%v", reloadedHeader, err)
	}
	reloadedPhases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(reloadedPhases) != 1 {
		t.Fatalf("Phase did not survive restart: %+v err=%v", reloadedPhases, err)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("restart rebuild dispatched %d Runs", len(dispatcher.runs))
	}

	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	root.Description = "changed authoritative root intent"
	if err := store.WorkItems().Update(ctx, root, root.Version); err != nil {
		t.Fatal(err)
	}
	issue, inconsistent, err := svc.CheckGovernanceConsistency(ctx, rootID)
	if err != nil || !inconsistent || issue.Code != "intent_inconsistent" {
		t.Fatalf("intent inconsistency must be observable: issue=%+v inconsistent=%v err=%v", issue, inconsistent, err)
	}
	unchangedGoal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedGoal.Objective == root.Description {
		t.Fatal("consistency query must not silently overwrite Goal intent")
	}
}

func TestGovernanceStateRebuildConcurrentReplayHasNoFalseInconsistency(t *testing.T) {
	ctx, db, svc, store, dispatcher, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	root.AcceptanceCriteria = []string{"concurrent rebuild remains single-copy"}
	if err := store.WorkItems().Update(ctx, root, root.Version); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan application.GovernanceStateRebuildResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			result, err := svc.RebuildGovernanceState(ctx, workspaceID)
			results <- result
			errs <- err
		}()
	}
	close(start)
	createdGoals, createdTodos := 0, 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		result := <-results
		createdGoals += result.CreatedGoals
		createdTodos += result.CreatedTodos
		if len(result.Issues) != 0 {
			t.Fatalf("concurrent replay reported false inconsistency: %+v", result.Issues)
		}
	}
	if createdGoals != 1 || createdTodos != 1 {
		t.Fatalf("concurrent rebuild counts: goals=%d todos=%d", createdGoals, createdTodos)
	}
	if goals, err := store.Goals().List(ctx, workspaceID); err != nil || len(goals) != 1 {
		t.Fatalf("concurrent rebuild duplicated Goal: %+v err=%v", goals, err)
	}
	if len(dispatcher.runs) != 0 {
		t.Fatalf("concurrent governance state rebuild dispatched %d Runs", len(dispatcher.runs))
	}
}

var errGovernanceTodoRead = errors.New("injected governance Todo read failure")

type failingGovernanceTodoRepo struct{ application.TodoRepo }

func (f failingGovernanceTodoRepo) Get(context.Context, string) (*domain.Todo, error) {
	return nil, errGovernanceTodoRead
}

type failingGovernanceStore struct {
	application.Store
	todos application.TodoRepo
}

func (s failingGovernanceStore) Todos() application.TodoRepo { return s.todos }

func TestGovernanceConsistencyCheckPropagatesStorageFailure(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	root.Description = "storage failure must remain distinguishable"
	root.AcceptanceCriteria = []string{"storage errors propagate"}
	if err := store.WorkItems().Update(ctx, root, root.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RebuildGovernanceState(ctx, workspaceID); err != nil {
		t.Fatal(err)
	}
	failing := failingGovernanceStore{Store: store, todos: failingGovernanceTodoRepo{TodoRepo: store.Todos()}}
	failingService := application.NewService(failing, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	issue, inconsistent, err := failingService.CheckGovernanceConsistency(ctx, rootID)
	if !errors.Is(err, errGovernanceTodoRead) || issue != nil || inconsistent {
		t.Fatalf("storage failure was misclassified: issue=%+v inconsistent=%v err=%v", issue, inconsistent, err)
	}
}
