package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	governancekernel "github.com/ybs/agent-team-workbench/internal/governance"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

const governanceNow = "2026-09-01T00:00:00Z"

func insertGovernanceWorkspace(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at)
		VALUES (?,?,'UTC',1,?,?)`, id, id, governanceNow, governanceNow); err != nil {
		t.Fatal(err)
	}
}

func insertGovernanceRoot(t *testing.T, db *sql.DB, id, workspaceID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO work_items
		(id,workspace_id,title,record_kind,status,priority,version,created_at,updated_at)
		VALUES (?,?,?,'task','todo','medium',1,?,?)`,
		id, workspaceID, id, governanceNow, governanceNow); err != nil {
		t.Fatal(err)
	}
}

func insertGovernanceAgent(t *testing.T, db *sql.DB, id, workspaceID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agent_profiles
		(id,workspace_id,name,role,created_at,updated_at) VALUES (?,?,?,'worker',?,?)`,
		id, workspaceID, id, governanceNow, governanceNow); err != nil {
		t.Fatal(err)
	}
}

func validGovernanceGoal(id, workspaceID, rootID string) *domain.Goal {
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	return &domain.Goal{
		ID:                        id,
		WorkspaceID:               workspaceID,
		RootWorkItemID:            rootID,
		Objective:                 "deliver the bounded governance slice",
		AcceptanceContract:        []string{"focused persistence tests pass"},
		Status:                    domain.GoalDraft,
		Phase:                     "planning",
		QuotaPolicies:             []domain.QuotaPolicy{},
		CompletionEvidenceSummary: []domain.GovernanceEvidenceItem{},
		Version:                   1,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
}

func validGovernanceTodo(id, goalID, workItemID, agentID string) *domain.Todo {
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	return &domain.Todo{
		ID:          id,
		GoalID:      goalID,
		Class:       domain.TodoAdvancement,
		Status:      domain.TodoPending,
		Instruction: "execute the next bounded action",
		Acceptance:  []string{"the action is evidenced"},
		Priority:    domain.PriorityMedium,
		DecisionScope: domain.DecisionScope{
			WorkItemIDs: []string{workItemID},
			AgentIDs:    []string{agentID},
		},
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func nativeDigest(ch byte) string {
	if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
		ch = "abcdef"[int(ch)%6]
	}
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func validGovernanceHeader(t *testing.T, goalID, todoID, clientKey string, seq int64) *domain.TurnReceiptHeader {
	t.Helper()
	now := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	header := &domain.TurnReceiptHeader{
		TurnKey:             domain.TurnKey{GoalID: goalID, TodoID: todoID, TurnSeq: seq},
		Attempt:             1,
		SchemaVersion:       "turn-receipt/v1",
		InputSnapshotDigest: nativeDigest('i'),
		AdmissionClientKey:  clientKey,
		CanonicalDigest:     nativeDigest('0'),
		CreatedAt:           now,
	}
	digest, err := governancekernel.ComputeHeaderDigest(header)
	if err != nil {
		t.Fatal(err)
	}
	header.CanonicalDigest = digest
	return header
}

func setGovernancePhaseDigest(t *testing.T, phase *domain.TurnReceiptPhase) {
	t.Helper()
	phase.CanonicalDigest = nativeDigest('0')
	digest, err := governancekernel.ComputePhaseDigest(phase)
	if err != nil {
		t.Fatal(err)
	}
	phase.CanonicalDigest = digest
}

func TestGovernanceReceiptRepositoryRejectsZeroCreatedAt(t *testing.T) {
	ctx := context.Background()
	db, store := setupGovernanceFixtures(t)
	defer db.Close()
	goal := validGovernanceGoal("goal_zero_receipt_time", "ws_wk", "wi_gov_root")
	if err := store.Goals().Create(ctx, goal); err != nil {
		t.Fatal(err)
	}
	header := validGovernanceHeader(t, goal.ID, "todo_zero_receipt_time", "zero-time-admit", 1)
	header.CreatedAt = time.Time{}
	if _, err := store.TurnReceipts().Admit(ctx, header, "agent_gov_one", 1); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("zero Header CreatedAt must fail closed, got %v", err)
	}
	phase := &domain.TurnReceiptPhase{
		TurnKey:         header.TurnKey,
		PhaseSeq:        1,
		Phase:           domain.TurnReceiptPhaseDecisionDecode,
		Payload:         map[string]any{"ok": true},
		CanonicalDigest: nativeDigest('b'),
	}
	if _, err := store.TurnReceipts().AppendPhase(ctx, phase); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("zero Phase CreatedAt must fail closed, got %v", err)
	}
}

func setupGovernanceFixtures(t *testing.T) (*sql.DB, *sqlstore.Store) {
	t.Helper()
	db := openWakeupTestDB(t)
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	insertGovernanceRoot(t, db, "wi_gov_root", "ws_wk")
	insertGovernanceAgent(t, db, "agent_gov_one", "ws_wk")
	insertGovernanceAgent(t, db, "agent_gov_two", "ws_wk")
	return db, store
}

func TestGovernanceGoalTodoCRUDCASAndWorkspaceScope(t *testing.T) {
	ctx := context.Background()
	db, store := setupGovernanceFixtures(t)
	defer db.Close()

	insertGovernanceWorkspace(t, db, "ws_gov_other")
	insertGovernanceRoot(t, db, "wi_gov_other_root", "ws_gov_other")
	insertGovernanceAgent(t, db, "agent_gov_other", "ws_gov_other")

	goal := validGovernanceGoal("goal_gov_one", "ws_wk", "wi_gov_root")
	if err := store.Goals().Create(ctx, goal); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Create(ctx, validGovernanceGoal("goal_gov_other", "ws_gov_other", "wi_gov_other_root")); err != nil {
		t.Fatal(err)
	}

	got, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != goal.WorkspaceID || got.RootWorkItemID != goal.RootWorkItemID ||
		!strings.EqualFold(got.Objective, goal.Objective) || len(got.AcceptanceContract) != 1 {
		t.Fatalf("Goal round-trip mismatch: got=%+v want=%+v", got, goal)
	}
	byRoot, err := store.Goals().GetByRootWorkItem(ctx, goal.RootWorkItemID)
	if err != nil {
		t.Fatal(err)
	}
	if byRoot.ID != goal.ID {
		t.Fatalf("GetByRootWorkItem returned %q, want %q", byRoot.ID, goal.ID)
	}
	goals, err := store.Goals().List(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 1 || goals[0].ID != goal.ID {
		t.Fatalf("workspace-scoped Goal list leaked another workspace: %+v", goals)
	}

	goal.Phase = "execution"
	if err := goal.Start(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Update(ctx, goal, 1); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Status != domain.GoalActive {
		t.Fatalf("Goal CAS update did not bump version/status: %+v", updated)
	}
	if err := store.Goals().Update(ctx, goal, 1); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale Goal update must conflict, got %v", err)
	}

	todo := validGovernanceTodo("todo_gov_one", goal.ID, "wi_gov_root", "agent_gov_one")
	if err := store.Todos().Create(ctx, todo); err != nil {
		t.Fatal(err)
	}
	otherTodo := validGovernanceTodo("todo_gov_other", "goal_gov_other", "wi_gov_other_root", "agent_gov_other")
	if err := store.Todos().Create(ctx, otherTodo); err != nil {
		t.Fatal(err)
	}
	if listed, err := store.Todos().ListByGoal(ctx, goal.ID); err != nil {
		t.Fatal(err)
	} else if len(listed) != 1 || listed[0].ID != todo.ID {
		t.Fatalf("Todo list leaked another Goal: %+v", listed)
	}

	todo.Instruction = "wait for the next wakeup"
	if err := todo.Transition(domain.TodoWaiting, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Todos().Update(ctx, todo, 1); err != nil {
		t.Fatal(err)
	}
	updatedTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedTodo.Version != 2 || updatedTodo.Status != domain.TodoWaiting || updatedTodo.Instruction != todo.Instruction {
		t.Fatalf("Todo CAS update did not persist: %+v", updatedTodo)
	}
	if err := store.Todos().Update(ctx, todo, 1); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale Todo update must conflict, got %v", err)
	}

	foreignGoal := validGovernanceGoal("goal_gov_bad", "ws_wk", "wi_gov_other_root")
	if err := store.Goals().Create(ctx, foreignGoal); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("cross-workspace Goal root must be rejected as validation, got %v", err)
	}
	foreignTodo := validGovernanceTodo("todo_gov_bad", goal.ID, "wi_gov_root", "agent_gov_one")
	foreignTodo.GoalID = "goal_gov_other"
	if err := store.Todos().Create(ctx, foreignTodo); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Todo with mismatched scope must be rejected as validation, got %v", err)
	}
}

func TestGovernanceRepoUpdateEnforcesStateMachineTerminalAndObjectVersion(t *testing.T) {
	ctx := context.Background()
	db, store := setupGovernanceFixtures(t)
	defer db.Close()
	goal := validGovernanceGoal("goal_state_guard", "ws_wk", "wi_gov_root")
	if err := store.Goals().Create(ctx, goal); err != nil {
		t.Fatal(err)
	}
	illegalGoal := *goal
	illegalGoal.Status = domain.GoalBlocked
	illegalGoal.Version = 2
	illegalGoal.UpdatedAt = time.Now().UTC()
	if err := store.Goals().Update(ctx, &illegalGoal, 1); err != nil {
		t.Fatalf("GoalRepo must allow draft→blocked materialization for an already blocked root, got %v", err)
	}
	blocked, err := store.Goals().Get(ctx, goal.ID)
	if err != nil || blocked.Status != domain.GoalBlocked || blocked.Version != 2 {
		t.Fatalf("draft→blocked materialization did not persist: goal=%+v err=%v", blocked, err)
	}
	if err := blocked.Cancel(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Update(ctx, blocked, 2); err != nil {
		t.Fatal(err)
	}
	if blocked.Version != 3 {
		t.Fatalf("Goal object version must match DB update: %+v", blocked)
	}
	reviveGoal := *blocked
	reviveGoal.Status = domain.GoalActive
	reviveGoal.Version = 4
	reviveGoal.UpdatedAt = time.Now().UTC()
	if err := store.Goals().Update(ctx, &reviveGoal, 3); !errors.Is(err, domain.ErrTerminalImmutable) {
		t.Fatalf("terminal Goal revival must fail, got %v", err)
	}

	insertGovernanceRoot(t, db, "wi_gov_state_todo", "ws_wk")
	secondGoal := validGovernanceGoal("goal_state_todo", "ws_wk", "wi_gov_state_todo")
	if err := store.Goals().Create(ctx, secondGoal); err != nil {
		t.Fatal(err)
	}
	todo := validGovernanceTodo("todo_state_guard", secondGoal.ID, "wi_gov_state_todo", "agent_gov_one")
	if err := store.Todos().Create(ctx, todo); err != nil {
		t.Fatal(err)
	}
	illegalTodo := *todo
	illegalTodo.Status = domain.TodoRunning
	illegalTodo.Version = 2
	illegalTodo.UpdatedAt = time.Now().UTC()
	if err := store.Todos().Update(ctx, &illegalTodo, 1); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("TodoRepo must reject pending→running without admission claim, got %v", err)
	}
	claimAt := time.Now().UTC()
	claimedTodo, err := store.Todos().Claim(ctx, todo.ID, "agent_gov_one", claimAt, claimAt.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	directRunning := *claimedTodo
	directRunning.Status = domain.TodoRunning
	directRunning.Version++
	directRunning.UpdatedAt = time.Now().UTC()
	if err := store.Todos().Update(ctx, &directRunning, claimedTodo.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("TodoRepo must reserve claimed→running for Admit, got %v", err)
	}
	cancelled, err := store.Todos().Cancel(ctx, todo.ID, time.Now().UTC(), claimedTodo.Version)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Version != 3 || cancelled.Status != domain.TodoCancelled || cancelled.Claim != nil ||
		cancelled.ClaimVersion != claimedTodo.ClaimVersion+1 {
		t.Fatalf("Todo Cancel object/DB mismatch: %+v", cancelled)
	}
	reviveTodo := *cancelled
	reviveTodo.Status = domain.TodoPending
	reviveTodo.Version = 4
	reviveTodo.UpdatedAt = time.Now().UTC()
	if err := store.Todos().Update(ctx, &reviveTodo, 3); !errors.Is(err, domain.ErrTerminalImmutable) {
		t.Fatalf("terminal Todo revival must fail, got %v", err)
	}
}

func TestGovernanceTodoClaimCASHasExactlyOneWinnerAndReleasePreservesGeneration(t *testing.T) {
	ctx := context.Background()
	db, store := setupGovernanceFixtures(t)
	defer db.Close()
	goal := validGovernanceGoal("goal_claim", "ws_wk", "wi_gov_root")
	if err := store.Goals().Create(ctx, goal); err != nil {
		t.Fatal(err)
	}
	todo := validGovernanceTodo("todo_claim", goal.ID, "wi_gov_root", "agent_gov_one")
	todo.DecisionScope.AgentIDs = []string{"agent_gov_one", "agent_gov_two"}
	if err := store.Todos().Create(ctx, todo); err != nil {
		t.Fatal(err)
	}
	scopedTodo := validGovernanceTodo("todo_scope_guard", goal.ID, "wi_gov_root", "agent_gov_one")
	if err := store.Todos().Create(ctx, scopedTodo); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Todos().Claim(ctx, scopedTodo.ID, "agent_gov_two",
		time.Date(2026, time.September, 1, 0, 0, 1, 0, time.UTC),
		time.Date(2026, time.September, 1, 1, 0, 1, 0, time.UTC), scopedTodo.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("claim owner outside DecisionScope must fail closed, got %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	type result struct {
		todo *domain.Todo
		err  error
	}
	results := make(chan result, 2)
	for _, owner := range []string{"agent_gov_one", "agent_gov_two"} {
		owner := owner
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := store.Todos().Claim(ctx, todo.ID, owner,
				time.Date(2026, time.September, 1, 0, 0, 1, 0, time.UTC),
				time.Date(2026, time.September, 1, 1, 0, 1, 0, time.UTC), 1)
			results <- result{todo: claimed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners := 0
	for got := range results {
		if got.err == nil {
			winners++
			if got.todo == nil || got.todo.Claim == nil || got.todo.Status != domain.TodoClaimed {
				t.Fatalf("winning claim returned invalid Todo: %+v", got.todo)
			}
		} else if !errors.Is(got.err, domain.ErrVersionConflict) && !errors.Is(got.err, domain.ErrStateConflict) {
			t.Fatalf("claim loser returned non-conflict: %v", got.err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent claim must have exactly one winner, got %d", winners)
	}
	claimed, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner := claimed.Claim.OwnerAgentID
	if claimed.ClaimVersion != 1 || claimed.Claim.Version != claimed.ClaimVersion {
		t.Fatalf("claim generation mismatch: %+v", claimed)
	}
	mutatedScope := *claimed
	mutatedScope.DecisionScope = claimed.DecisionScope
	mutatedScope.DecisionScope.MaxDispatch--
	mutatedScope.Version++
	mutatedScope.UpdatedAt = time.Now().UTC()
	if err := store.Todos().Update(ctx, &mutatedScope, claimed.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("claimed Todo DecisionScope must be immutable, got %v", err)
	}

	wrongOwner := "agent_gov_one"
	if owner == wrongOwner {
		wrongOwner = "agent_gov_two"
	}
	if _, err := store.Todos().Release(ctx, todo.ID, wrongOwner, time.Now().UTC(), claimed.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("non-owner release must be a state conflict, got %v", err)
	}
	if _, err := store.Todos().Release(ctx, todo.ID, owner, time.Now().UTC(), claimed.Version-1); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale release must be a version conflict, got %v", err)
	}
	released, err := store.Todos().Release(ctx, todo.ID, owner, time.Now().UTC(), claimed.Version)
	if err != nil {
		t.Fatal(err)
	}
	if released.Claim != nil || released.Status != domain.TodoPending || released.ClaimVersion != 2 || released.Version != claimed.Version+1 {
		t.Fatalf("release must clear claim and preserve monotonic generation: %+v", released)
	}
	mutatedReleasedScope := *released
	mutatedReleasedScope.DecisionScope = released.DecisionScope
	mutatedReleasedScope.DecisionScope.MaxDispatch--
	mutatedReleasedScope.Version++
	mutatedReleasedScope.UpdatedAt = time.Now().UTC()
	if err := store.Todos().Update(ctx, &mutatedReleasedScope, released.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("released Todo scope must remain frozen after its first claim, got %v", err)
	}
	reclaimed, err := store.Todos().Claim(ctx, todo.ID, "agent_gov_two",
		time.Date(2026, time.September, 1, 0, 1, 0, 0, time.UTC),
		time.Date(2026, time.September, 1, 1, 1, 0, 0, time.UTC), released.Version)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Claim == nil || reclaimed.Claim.Version != 3 || reclaimed.ClaimVersion != 3 {
		t.Fatalf("reclaim must not reuse pre-release generation: %+v", reclaimed)
	}

	sameOwnerTodo := validGovernanceTodo("todo_claim_same_owner", goal.ID, "wi_gov_root", "agent_gov_one")
	if err := store.Todos().Create(ctx, sameOwnerTodo); err != nil {
		t.Fatal(err)
	}
	start = make(chan struct{})
	results = make(chan result, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			<-start
			claimedAt := time.Now().UTC()
			claimed, err := store.Todos().Claim(ctx, sameOwnerTodo.ID, "agent_gov_one",
				claimedAt, claimedAt.Add(time.Hour+time.Duration(offset)*time.Minute), 1)
			results <- result{todo: claimed, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	winners = 0
	for got := range results {
		if got.err == nil {
			winners++
		} else if !errors.Is(got.err, domain.ErrVersionConflict) {
			t.Fatalf("same-owner CAS loser must be version conflict, got %v", got.err)
		}
	}
	if winners != 1 {
		t.Fatalf("same-owner concurrent claim must have one winner, got %d", winners)
	}
}

func TestGovernanceAdmissionCASReplayConcurrencyAndRollback(t *testing.T) {
	ctx := context.Background()
	db, store := setupGovernanceFixtures(t)
	defer db.Close()
	goal := validGovernanceGoal("goal_admit", "ws_wk", "wi_gov_root")
	if err := store.Goals().Create(ctx, goal); err != nil {
		t.Fatal(err)
	}
	goal.Phase = "execution"
	if err := goal.Start(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Update(ctx, goal, 1); err != nil {
		t.Fatal(err)
	}
	todo := validGovernanceTodo("todo_admit", goal.ID, "wi_gov_root", "agent_gov_one")
	if err := store.Todos().Create(ctx, todo); err != nil {
		t.Fatal(err)
	}
	claimAt := time.Now().UTC()
	claimed, err := store.Todos().Claim(ctx, todo.ID, "agent_gov_one",
		claimAt, claimAt.Add(time.Hour), todo.Version)
	if err != nil {
		t.Fatal(err)
	}

	header := validGovernanceHeader(t, goal.ID, todo.ID, "admit-client-1", 1)
	admitted, err := store.TurnReceipts().Admit(ctx, header, "agent_gov_one", claimed.Version)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.TurnKey.TurnSeq != 1 || header.TurnKey.TurnSeq != 1 {
		t.Fatalf("admission must allocate next turn_seq: admitted=%+v input=%+v", admitted, header)
	}
	freshTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if freshTodo.Status != domain.TodoRunning || freshTodo.LastTurnSeq != 1 || freshTodo.Version != claimed.Version+1 {
		t.Fatalf("admission did not atomically advance Todo: %+v", freshTodo)
	}

	replay := *admitted
	replay.CanonicalDigest = admitted.CanonicalDigest
	replayed, err := store.TurnReceipts().Admit(ctx, &replay, "agent_gov_one", 999)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.TurnKey.Equal(admitted.TurnKey) || replayed.CanonicalDigest != admitted.CanonicalDigest {
		t.Fatalf("same client key/digest must replay original header: got=%+v want=%+v", replayed, admitted)
	}
	byClient, err := store.TurnReceipts().GetHeaderByClientKey(ctx, goal.ID, todo.ID, admitted.AdmissionClientKey)
	if err != nil {
		t.Fatal(err)
	}
	if !byClient.TurnKey.Equal(admitted.TurnKey) {
		t.Fatalf("client-key lookup returned wrong header: got=%+v want=%+v", byClient, admitted)
	}
	unchanged, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.LastTurnSeq != 1 || unchanged.Version != freshTodo.Version {
		t.Fatalf("replay must not mutate Todo: %+v", unchanged)
	}
	conflict := *admitted
	conflict.Attempt++
	conflict.CanonicalDigest = nativeDigest('0')
	conflictDigest, err := governancekernel.ComputeHeaderDigest(&conflict)
	if err != nil {
		t.Fatal(err)
	}
	conflict.CanonicalDigest = conflictDigest
	if _, err := store.TurnReceipts().Admit(ctx, &conflict, "agent_gov_one", 999); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same admission key with different digest must conflict, got %v", err)
	}

	// Admission participates in the caller transaction. A caller failure after
	// the repository has allocated the turn must roll the Todo/header pair back.
	insertGovernanceRoot(t, db, "wi_gov_root_rollback", "ws_wk")
	rollbackGoal := validGovernanceGoal("goal_admit_rollback", "ws_wk", "wi_gov_root_rollback")
	if err := store.Goals().Create(ctx, rollbackGoal); err != nil {
		t.Fatal(err)
	}
	rollbackGoal.Phase = "execution"
	if err := rollbackGoal.Start(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Update(ctx, rollbackGoal, 1); err != nil {
		t.Fatal(err)
	}
	rollbackTodo := validGovernanceTodo("todo_admit_rollback", rollbackGoal.ID, "wi_gov_root_rollback", "agent_gov_one")
	if err := store.Todos().Create(ctx, rollbackTodo); err != nil {
		t.Fatal(err)
	}
	rollbackClaimAt := time.Now().UTC()
	rollbackClaimed, err := store.Todos().Claim(ctx, rollbackTodo.ID, "agent_gov_one",
		rollbackClaimAt, rollbackClaimAt.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	rollbackHeader := validGovernanceHeader(t, rollbackGoal.ID, rollbackTodo.ID, "admit-rollback", 1)
	if err := store.InTx(ctx, func(txctx context.Context) error {
		if _, err := store.TurnReceipts().Admit(txctx, rollbackHeader, "agent_gov_one", rollbackClaimed.Version); err != nil {
			return err
		}
		return errors.New("inject caller failure after admission")
	}); err == nil {
		t.Fatal("caller failure must roll back admission")
	}
	rollbackAfter, err := store.Todos().Get(ctx, rollbackTodo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rollbackAfter.LastTurnSeq != 0 || rollbackAfter.Version != rollbackClaimed.Version || rollbackAfter.Status != domain.TodoClaimed {
		t.Fatalf("failed admission must roll back Todo CAS: %+v", rollbackAfter)
	}
	var rollbackHeaders int
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_headers WHERE goal_id=? AND todo_id=?`, rollbackGoal.ID, rollbackTodo.ID).Scan(&rollbackHeaders); err != nil {
		t.Fatal(err)
	}
	if rollbackHeaders != 0 {
		t.Fatalf("failed admission must roll back Header insert, count=%d", rollbackHeaders)
	}

	// Two distinct client keys racing on the same expected Todo version may
	// allocate only one turn.
	concurrentTodo := validGovernanceTodo("todo_admit_concurrent", goal.ID, "wi_gov_root", "agent_gov_one")
	if err := store.Todos().Create(ctx, concurrentTodo); err != nil {
		t.Fatal(err)
	}
	concurrentClaimAt := time.Now().UTC()
	concurrentClaim, err := store.Todos().Claim(ctx, concurrentTodo.ID, "agent_gov_one",
		concurrentClaimAt, concurrentClaimAt.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"admit-race-1", "admit-race-2"} {
		key := key
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.TurnReceipts().Admit(ctx,
				validGovernanceHeader(t, goal.ID, concurrentTodo.ID, key, 1),
				"agent_gov_one", concurrentClaim.Version)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	winners := 0
	for err := range errs {
		if err == nil {
			winners++
		} else if !errors.Is(err, domain.ErrVersionConflict) && !errors.Is(err, domain.ErrStateConflict) && !errors.Is(err, domain.ErrIdempotencyConflict) {
			t.Fatalf("admission loser returned non-conflict: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent admission must have exactly one winner, got %d", winners)
	}
	var headerCount int
	if err := db.QueryRow(`SELECT count(*) FROM turn_receipt_headers WHERE goal_id=? AND todo_id=?`, goal.ID, concurrentTodo.ID).Scan(&headerCount); err != nil {
		t.Fatal(err)
	}
	if headerCount != 1 {
		t.Fatalf("concurrent admission inserted %d headers, want 1", headerCount)
	}
}

func TestGovernanceTurnReceiptPhaseAppendReplayGapAndImmutability(t *testing.T) {
	ctx := context.Background()
	db, store := setupGovernanceFixtures(t)
	defer db.Close()
	insertGovernanceWorkspace(t, db, "ws_phase_other")
	insertGovernanceRoot(t, db, "wi_phase_other", "ws_phase_other")
	insertGovernanceRoot(t, db, "wi_phase_same_workspace", "ws_wk")
	goal := validGovernanceGoal("goal_phase", "ws_wk", "wi_gov_root")
	if err := store.Goals().Create(ctx, goal); err != nil {
		t.Fatal(err)
	}
	goal.Phase = "execution"
	if err := goal.Start(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Goals().Update(ctx, goal, 1); err != nil {
		t.Fatal(err)
	}
	todo := validGovernanceTodo("todo_phase", goal.ID, "wi_gov_root", "agent_gov_one")
	if err := store.Todos().Create(ctx, todo); err != nil {
		t.Fatal(err)
	}
	claimAt := time.Now().UTC()
	claimed, err := store.Todos().Claim(ctx, todo.ID, "agent_gov_one",
		claimAt, claimAt.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	header, err := store.TurnReceipts().Admit(ctx,
		validGovernanceHeader(t, goal.ID, todo.ID, "phase-admit", 1),
		"agent_gov_one", claimed.Version)
	if err != nil {
		t.Fatal(err)
	}

	phase := &domain.TurnReceiptPhase{
		TurnKey:  header.TurnKey,
		PhaseSeq: 1,
		Phase:    domain.TurnReceiptPhaseDecisionDecode,
		Payload:  map[string]any{"accepted": true},
		Evidence: []domain.GovernanceEvidenceItem{{
			SourceKind: domain.EvidenceSourceWorkItem, SourceID: "wi_gov_root",
			Verification: domain.EvidenceVerificationObserved, Summary: "in-scope evidence",
			RecordedAt: time.Now().UTC(),
		}},
		CreatedAt: time.Now().UTC(),
	}
	setGovernancePhaseDigest(t, phase)
	crossWorkspace := *phase
	crossWorkspace.Evidence = []domain.GovernanceEvidenceItem{{
		SourceKind: domain.EvidenceSourceWorkItem, SourceID: "wi_phase_other",
		Verification: domain.EvidenceVerificationObserved, Summary: "foreign evidence",
		RecordedAt: time.Now().UTC(),
	}}
	setGovernancePhaseDigest(t, &crossWorkspace)
	if _, err := store.TurnReceipts().AppendPhase(ctx, &crossWorkspace); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("cross-workspace receipt evidence must fail closed, got %v", err)
	}
	first, err := store.TurnReceipts().AppendPhase(ctx, phase)
	if err != nil {
		t.Fatal(err)
	}
	runningTodo, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	runningExpected := runningTodo.Version
	runningTodo.DecisionScope.WorkItemIDs = []string{"wi_phase_same_workspace"}
	runningTodo.Version++
	runningTodo.UpdatedAt = time.Now().UTC()
	if err := store.Todos().Update(ctx, runningTodo, runningExpected); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("admitted Todo DecisionScope must be immutable, got %v", err)
	}
	replay := *phase
	replay.Payload = map[string]any{"accepted": true}
	setGovernancePhaseDigest(t, &replay)
	replayed, err := store.TurnReceipts().AppendPhase(ctx, &replay)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.CanonicalDigest != first.CanonicalDigest || replayed.PhaseSeq != first.PhaseSeq {
		t.Fatalf("same identity/digest must return original phase: got=%+v want=%+v", replayed, first)
	}
	conflict := *phase
	conflict.Payload = map[string]any{"accepted": false}
	setGovernancePhaseDigest(t, &conflict)
	if _, err := store.TurnReceipts().AppendPhase(ctx, &conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same phase identity with different digest must conflict, got %v", err)
	}
	gap := *phase
	gap.PhaseSeq = 3
	gap.Phase = domain.TurnReceiptPhaseDurableWriteback
	gap.Evidence = nil
	setGovernancePhaseDigest(t, &gap)
	if _, err := store.TurnReceipts().AppendPhase(ctx, &gap); err == nil {
		t.Fatal("phase gap must be rejected")
	}
	second := *phase
	second.PhaseSeq = 2
	second.Phase = domain.TurnReceiptPhaseValidation
	second.Payload = map[string]any{"valid": true}
	second.Evidence = nil
	setGovernancePhaseDigest(t, &second)
	if _, err := store.TurnReceipts().AppendPhase(ctx, &second); err != nil {
		t.Fatal(err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[0].PhaseSeq != 1 || phases[1].PhaseSeq != 2 {
		t.Fatalf("phase list must be ordered and contiguous: %+v", phases)
	}
	gotPhase, err := store.TurnReceipts().GetPhase(ctx, header.TurnKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotPhase.CanonicalDigest != first.CanonicalDigest {
		t.Fatalf("phase identity lookup returned wrong phase: got=%+v want=%+v", gotPhase, first)
	}
	if _, err := db.Exec(`UPDATE turn_receipt_phases SET payload='{"changed":true}
		WHERE goal_id=? AND todo_id=? AND turn_seq=? AND phase_seq=1`, header.TurnKey.GoalID, header.TurnKey.TodoID, header.TurnKey.TurnSeq); err == nil {
		t.Fatal("direct phase UPDATE must be rejected by append-only trigger")
	}
	if _, err := db.Exec(`DELETE FROM turn_receipt_phases
		WHERE goal_id=? AND todo_id=? AND turn_seq=? AND phase_seq=1`, header.TurnKey.GoalID, header.TurnKey.TodoID, header.TurnKey.TurnSeq); err == nil {
		t.Fatal("direct phase DELETE must be rejected by append-only trigger")
	}
	if _, err := store.TurnReceipts().GetHeader(ctx, header.TurnKey); err != nil {
		t.Fatal(fmt.Errorf("header must remain readable after phase checks: %w", err))
	}
}
