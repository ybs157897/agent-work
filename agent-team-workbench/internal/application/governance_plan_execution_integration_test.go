package application_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gowebpki/jcs"
	workbenchcontracts "github.com/ybs/agent-team-workbench/contracts"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

func TestSubmitGovernedTodoPlanDecisionPersistsReceiptPlanAndReplaysOnce(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governed Plan execution", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"receipt and Plan replay exactly once"},
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
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.GovernanceTurnKey == nil || plan.ClientKey == "" || plan.DecisionDigest == "" {
		t.Fatalf("governed Plan identity missing: %+v", plan)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("governed dispatch must create exactly one Worker Run: %d", len(dispatcher.runs))
	}
	worker := dispatcher.runs[1]
	if plan.Steps[0].ResultRunID != worker.ID {
		t.Fatalf("Plan/Worker reference mismatch: plan=%+v worker=%+v", plan, worker)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoWaiting || todo.LastTurnSeq != plan.GovernanceTurnKey.TurnSeq || todo.Claim == nil {
		t.Fatalf("submitted Plan did not settle the admitted Todo to waiting: %+v", todo)
	}
	header, err := store.TurnReceipts().GetHeader(ctx, *plan.GovernanceTurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if header.SchemaVersion != "plan-decision/v2" || header.AdmissionClientKey != "plan-decision:"+source.ID {
		t.Fatalf("admission Header mismatch: %+v", header)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 7 {
		t.Fatalf("governed Plan must commit receipt phases 1..7: %+v", phases)
	}
	for index, phase := range phases {
		if phase.PhaseSeq != index+1 {
			t.Fatalf("receipt phases are not contiguous: %+v", phases)
		}
	}
	if phases[3].PlanID != plan.ID || len(phases[3].RunIDs) != 1 || phases[3].RunIDs[0] != worker.ID ||
		phases[4].PlanID != plan.ID || phases[4].Payload["dispatch_state"] != "committed" {
		t.Fatalf("plan_compile/dispatch phases lost canonical references: phase4=%+v phase5=%+v", phases[3], phases[4])
	}

	replayed, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != plan.ID || len(dispatcher.runs) != 2 {
		t.Fatalf("same governed decision replay duplicated Plan/Run: first=%s replay=%s runs=%d", plan.ID, replayed.ID, len(dispatcher.runs))
	}
	replayedPhases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(replayedPhases) != 7 {
		t.Fatalf("receipt replay duplicated or lost phases: phases=%+v err=%v", replayedPhases, err)
	}

	changed := compilerDecision(domain.PlanVerbDefer, workerID)
	if conflict, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, changed, application.PlanCandidateNativeText); !errors.Is(err, domain.ErrIdempotencyConflict) || conflict != nil {
		t.Fatalf("same admitted source with a different decision must conflict: plan=%+v err=%v", conflict, err)
	}
}

func TestGovernedAdmissionCheckpointRecoversAfterRestart(t *testing.T) {
	ctx, db, svc, store, firstDispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "durable admission checkpoint", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a committed admission recovers exactly once"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(100)
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementAudit,
	})
	source := firstDispatcher.runs[0]
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	decisionDigest := testPlanDecisionDigest(t, decision)
	rawDecision, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted, map[string]any{
		"role": "assistant", "text": string(rawDecision),
	}); err != nil {
		t.Fatal(err)
	}
	// The terminal source state is persisted without invoking its hooks: this is
	// the process-crash boundary immediately after transaction A committed.
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, source.AgentProfileID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	key := domain.TurnKey{GoalID: goal.ID, TodoID: claimed.ID, TurnSeq: claimed.LastTurnSeq + 1}
	// The trigger interrupts the phase write after the admission transaction
	// has committed its Header, Todo watermark and usage reservation.
	if _, err := db.Exec(`CREATE TRIGGER checkpoint_phase1_failpoint
BEFORE INSERT ON turn_receipt_phases
WHEN NEW.phase_seq=1
BEGIN SELECT RAISE(ABORT, 'injected phase checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); err == nil {
		t.Fatal("injected phase-1 failure must interrupt Plan submission after durable admission")
	}
	if _, err := db.Exec(`DROP TRIGGER checkpoint_phase1_failpoint`); err != nil {
		t.Fatal(err)
	}
	header, err := store.TurnReceipts().GetHeaderBySourceRun(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !header.TurnKey.Equal(key) || header.GovernedSourceRunID != source.ID || header.PlanClientKey == "" || header.DecisionDigest == "" {
		t.Fatalf("durable checkpoint Header identity mismatch: got=%+v want=%+v", header, key)
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: key, Kind: domain.QuotaOutputTokens,
	})
	if err != nil || reservation.Status != domain.QuotaReservationReserved || reservation.ReservedAmount != limit {
		t.Fatalf("admission checkpoint reservation mismatch: %+v err=%v", reservation, err)
	}
	checkpointTodo, err := store.Todos().Get(ctx, header.TurnKey.TodoID)
	if err != nil || checkpointTodo.LastTurnSeq != header.TurnKey.TurnSeq || checkpointTodo.Status != domain.TodoRunning {
		t.Fatalf("admission checkpoint must retain the admitted Todo watermark: todo=%+v err=%v", checkpointTodo, err)
	}
	if plans, err := store.Plans().LatestByWorkItem(ctx, root.ID); err != nil || plans != nil {
		t.Fatalf("crash checkpoint must not have a Plan: plan=%+v err=%v", plans, err)
	}

	restartDispatcher := &captureDispatcher{}
	restarted := application.NewService(store, restartDispatcher, noopNotifier{}, atwruntime.NewRegistry())
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- restarted.StartCoordinator(ctx, root.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("concurrent checkpoint recovery failed: %v", callErr)
		}
	}
	if len(restartDispatcher.runs) != 1 {
		state, _ := store.TaskCoordinators().GetState(ctx, root.ID)
		plan, _ := store.Plans().LatestByWorkItem(ctx, root.ID)
		t.Fatalf("checkpoint recovery must create exactly one Worker Run: %d state=%+v plan=%+v", len(restartDispatcher.runs), state, plan)
	}
	plan, err := store.Plans().GetByClientKey(ctx, wsID, header.PlanClientKey)
	if err != nil || plan == nil {
		t.Fatalf("checkpoint recovery must create one governed Plan: plan=%+v err=%v", plan, err)
	}
	if plan.SourceRunID != source.ID || plan.GovernanceTurnKey == nil || !plan.GovernanceTurnKey.Equal(key) ||
		plan.DecisionDigest != decisionDigest {
		t.Fatalf("recovered Plan lineage mismatch: %+v", plan)
	}
	worker := restartDispatcher.runs[0]
	if worker.ID == source.ID {
		t.Fatal("recovery must create a distinct Worker Run")
	}
	if storedWorker, err := store.Runs().Get(ctx, worker.ID); err != nil || storedWorker.WorkItemID == root.ID {
		t.Fatalf("checkpoint recovery must persist a distinct Worker Run: root=%s captured=%+v stored=%+v err=%v", root.ID, worker, storedWorker, err)
	}
	children, err := store.WorkItems().ListByParent(ctx, root.ID)
	if err != nil || len(children) != 1 || children[0].ID != worker.WorkItemID {
		t.Fatalf("checkpoint recovery must retain exactly one Worker child: children=%+v err=%v", children, err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, key)
	if err != nil || len(phases) != 5 {
		t.Fatalf("pre-worker recovery must leave receipt phases 1..5: phases=%+v err=%v", phases, err)
	}
	for index, phase := range phases {
		if phase.PhaseSeq != index+1 {
			t.Fatalf("recovered receipt phases are not contiguous: %+v", phases)
		}
	}
	if phases[0].Payload["source_run_id"] != source.ID || phases[3].PlanID != plan.ID ||
		len(phases[4].RunIDs) != 1 || phases[4].RunIDs[0] != worker.ID {
		t.Fatalf("recovered receipt lost source/Plan/Worker lineage: %+v", phases)
	}

	// Finish the one recovered Worker, then use the restart recovery close
	// trigger to synthesize only the missing source/worker usage evidence and
	// close the frozen reservation exactly once.
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded} {
		if err := restarted.RecordRunStatus(ctx, worker.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := restarted.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	phases, err = store.TurnReceipts().ListPhases(ctx, key)
	if err != nil || len(phases) != 7 {
		t.Fatalf("closed checkpoint recovery must produce phases 1..7: phases=%+v err=%v", phases, err)
	}
	for index, phase := range phases {
		if phase.PhaseSeq != index+1 {
			t.Fatalf("closed recovery receipt phases are not contiguous: %+v", phases)
		}
	}
	settled, err := store.Quotas().Get(ctx, reservation.Key)
	if err != nil || settled.Status != domain.QuotaReservationReleased ||
		settled.CommittedAmount != 0 || settled.ReleasedAmount != limit {
		t.Fatalf("checkpoint reservation settlement mismatch: %+v err=%v", settled, err)
	}
	if spends, err := store.Quotas().ListSpendByTurn(ctx, key); err != nil || len(spends) != 2 {
		t.Fatalf("closed recovery must produce one idempotent spend per Run: spends=%+v err=%v", spends, err)
	}
	if err := restarted.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	finalPhases, err := store.TurnReceipts().ListPhases(ctx, key)
	if err != nil || len(finalPhases) != 7 {
		t.Fatalf("recovery replay duplicated receipt phases: phases=%+v err=%v", finalPhases, err)
	}
}

func TestGovernedAdmissionCheckpointRecoversAfterPhase3Crash(t *testing.T) {
	ctx, db, svc, store, firstDispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "durable phase3 checkpoint", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"validated admission resumes before Plan commit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(100)
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementAudit,
	})
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	rawDecision, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	source := firstDispatcher.runs[0]
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted, map[string]any{
		"role": "assistant", "text": string(rawDecision),
	}); err != nil {
		t.Fatal(err)
	}
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Claiming the Todo is the precondition for the admission transaction.
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimTodo(ctx, todo.ID, source.AgentProfileID, todo.Version, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER checkpoint_phase3_failpoint
BEFORE INSERT ON turn_receipt_phases
WHEN NEW.phase_seq=3
BEGIN SELECT RAISE(ABORT, 'injected phase-3 checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	if plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); plan == nil || err == nil {
		t.Fatalf("injected phase-3 failure must interrupt receipt writeback after Plan commit: plan=%+v err=%v", plan, err)
	}
	if _, err := db.Exec(`DROP TRIGGER checkpoint_phase3_failpoint`); err != nil {
		t.Fatal(err)
	}
	header, err := store.TurnReceipts().GetHeaderBySourceRun(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 2 || phases[0].PhaseSeq != 1 || phases[1].PhaseSeq != 2 {
		t.Fatalf("phase-3 crash must retain only the validated receipt prefix: phases=%+v err=%v", phases, err)
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: header.TurnKey, Kind: domain.QuotaOutputTokens,
	})
	if err != nil || reservation.Status != domain.QuotaReservationReserved || reservation.ReservedAmount != limit {
		t.Fatalf("phase-3 checkpoint reservation mismatch: %+v err=%v", reservation, err)
	}
	plan, err := store.Plans().GetByClientKey(ctx, wsID, header.PlanClientKey)
	if err != nil || plan == nil || plan.Status != domain.PlanWaiting {
		t.Fatalf("phase-3 crash must retain the committed waiting Plan: plan=%+v err=%v", plan, err)
	}
	if len(firstDispatcher.runs) != 2 {
		t.Fatalf("phase-3 crash occurs after the Worker transaction commits: dispatched=%d", len(firstDispatcher.runs))
	}

	restartDispatcher := &captureDispatcher{}
	restarted := application.NewService(store, restartDispatcher, noopNotifier{}, atwruntime.NewRegistry())
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- restarted.StartCoordinator(ctx, root.ID)
		}()
	}
	wg.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatalf("concurrent phase-3 checkpoint recovery failed: %v", callErr)
		}
	}
	if len(restartDispatcher.runs) != 0 {
		t.Fatalf("phase-3 checkpoint recovery must not dispatch a duplicate Worker Run: %d", len(restartDispatcher.runs))
	}
	plan, err = store.Plans().GetByClientKey(ctx, wsID, header.PlanClientKey)
	if err != nil || plan == nil || plan.SourceRunID != source.ID || plan.GovernanceTurnKey == nil ||
		!plan.GovernanceTurnKey.Equal(header.TurnKey) {
		t.Fatalf("phase-3 checkpoint Plan lineage mismatch: plan=%+v err=%v", plan, err)
	}
	worker := firstDispatcher.runs[1]
	children, err := store.WorkItems().ListByParent(ctx, root.ID)
	if err != nil || len(children) != 1 || children[0].ID != worker.WorkItemID {
		t.Fatalf("phase-3 recovery must retain one Worker child: children=%+v err=%v", children, err)
	}
	phases, err = store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 5 {
		t.Fatalf("phase-3 recovery must append phases 3..5 without phase6 before Worker terminal: phases=%+v err=%v", phases, err)
	}
	for index, phase := range phases {
		if phase.PhaseSeq != index+1 {
			t.Fatalf("phase-3 recovered prefix is not contiguous: %+v", phases)
		}
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded} {
		if err := restarted.RecordRunStatus(ctx, worker.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := restarted.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	phases, err = store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("phase-3 closed recovery must produce phases 1..7: phases=%+v err=%v", phases, err)
	}
	settled, err := store.Quotas().Get(ctx, reservation.Key)
	if err != nil || settled.Status != domain.QuotaReservationReleased || settled.CommittedAmount != 0 || settled.ReleasedAmount != limit {
		t.Fatalf("phase-3 recovery reservation settlement mismatch: %+v err=%v", settled, err)
	}
	spends, err := store.Quotas().ListSpendByTurn(ctx, header.TurnKey)
	if err != nil || len(spends) != 2 {
		t.Fatalf("phase-3 recovery must settle source and Worker exactly once: spends=%+v err=%v", spends, err)
	}
}

func TestSubmitGovernedTodoPlanDecisionAuthorityFailureWritesValidationOnly(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governed authority rejection", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"scope violations create no execution"},
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
	decision := compilerDecision(domain.PlanVerbDispatch, "agent_outside_frozen_scope")
	if plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); plan != nil || err == nil {
		t.Fatalf("scope violation must fail before Plan creation: plan=%+v err=%v", plan, err)
	} else {
		var decisionErr *application.PlanDecisionError
		if !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanAuthorityDenied {
			t.Fatalf("scope violation classification mismatch: %v", err)
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
	header, err := store.TurnReceipts().GetHeaderByClientKey(ctx, goal.ID, todo.ID, "plan-decision:"+source.ID)
	if err != nil {
		t.Fatal(err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 2 || phases[0].Phase != domain.TurnReceiptPhaseDecisionDecode ||
		phases[1].Phase != domain.TurnReceiptPhaseValidation || phases[1].Payload["valid"] != false ||
		phases[1].Payload["error_code"] != string(domain.GovernanceErrorPlanAuthorityDenied) {
		t.Fatalf("authority rejection must stop after validation phase: %+v", phases)
	}
	if plan, err := store.Plans().LatestByWorkItem(ctx, root.ID); err != nil || plan != nil {
		t.Fatalf("authority rejection persisted a Plan: plan=%+v err=%v", plan, err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("authority rejection created or dispatched a Run: %d", len(dispatcher.runs))
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoBlocked || todo.LastTurnSeq != header.TurnKey.TurnSeq {
		t.Fatalf("authority rejection must settle the admitted Todo to blocked: %+v", todo)
	}
	if replay, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); replay != nil || err == nil {
		t.Fatalf("same rejected decision replay must remain rejected: plan=%+v err=%v", replay, err)
	}
	replayedPhases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(replayedPhases) != 2 {
		t.Fatalf("rejected decision replay duplicated receipt phases: phases=%+v err=%v", replayedPhases, err)
	}
}

func TestSubmitGovernedTodoPlanDecisionConcurrentAuthorityReplayIsStable(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "concurrent authority rejection", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"rejected decision replay is stable"},
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
	decision := compilerDecision(domain.PlanVerbDispatch, "agent_outside_frozen_scope")
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
			if plan != nil {
				errs <- fmt.Errorf("unexpected Plan %s", plan.ID)
				return
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		var decisionErr *application.PlanDecisionError
		if !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanAuthorityDenied {
			t.Fatalf("concurrent rejection drifted: %v", err)
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
	header, err := store.TurnReceipts().GetHeaderByClientKey(ctx, goal.ID, todo.ID, "plan-decision:"+source.ID)
	if err != nil {
		t.Fatal(err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 2 || todo.Status != domain.TodoBlocked || len(dispatcher.runs) != 1 {
		t.Fatalf("concurrent rejection state mismatch: phases=%+v todo=%+v runs=%d err=%v", phases, todo, len(dispatcher.runs), err)
	}
}

func TestGovernedPlanSubmissionPermanentFailureReleasesUsageReservationOnce(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "permanent Plan rejection", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a rejected Plan releases its usage reservation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(100)
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementAudit,
	})
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	// SubmitPlan must reject this deterministic authority conflict after the
	// governance Header/phase1/phase2 admission has already committed.
	now := time.Now().UTC()
	blocking := &domain.Plan{
		ID: "plan_blocking_active", WorkspaceID: wsID, WorkItemID: root.ID,
		AgentProfileID: source.AgentProfileID, Status: domain.PlanActive,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Plans().Create(ctx, blocking); err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	if plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); plan != nil || err == nil {
		t.Fatalf("permanent Plan rejection must not create a governed Plan: plan=%+v err=%v", plan, err)
	} else {
		var decisionErr *application.PlanDecisionError
		if !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanSemanticValidation {
			t.Fatalf("permanent Plan rejection classification mismatch: %v", err)
		}
	}
	goal, err = store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoBlocked || todo.LastTurnSeq != 1 {
		t.Fatalf("permanent Plan rejection must block the admitted Todo: %+v", todo)
	}
	key := domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: todo.LastTurnSeq}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: key, Kind: domain.QuotaOutputTokens})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Status != domain.QuotaReservationReleased || reservation.CommittedAmount != 0 || reservation.ReleasedAmount != limit {
		t.Fatalf("permanent Plan rejection must release usage reservation: %+v", reservation)
	}
	active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens)
	if err != nil || active != 0 {
		t.Fatalf("rejected Plan must leave no active usage reservation: active=%d err=%v", active, err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, key)
	if err != nil || len(phases) != 3 {
		t.Fatalf("rejected Plan must stop at receipt phase3 without fabricating phase4/5: phases=%+v err=%v", phases, err)
	}
	if status, _ := phases[2].Payload["status"].(string); status != "rejected" {
		t.Fatalf("phase3 must explain the permanent Plan rejection: %+v", phases[2])
	}
	if settled, _ := phases[2].Payload["quota_settled"].(bool); !settled {
		t.Fatalf("phase3 must record quota compensation: %+v", phases[2])
	}
	if keys, ok := phases[2].Payload["quota_reservation_keys"].([]any); !ok || len(keys) != 1 || keys[0] != key.GoalID+":"+key.TodoID+":"+fmt.Sprint(key.TurnSeq)+":"+string(domain.QuotaOutputTokens) {
		t.Fatalf("phase3 must bind the settled reservation identity: %+v", phases[2].Payload)
	}
	beforeVersion := reservation.Version
	eventsBefore, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	quotaEventsBefore := 0
	for _, event := range eventsBefore {
		if event.Type == domain.EventQuotaReservationChanged || event.Type == domain.EventQuotaSpendRecorded {
			quotaEventsBefore++
		}
	}
	if replay, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); replay != nil || err == nil {
		t.Fatalf("replaying rejected Plan must remain rejected without a new Plan: plan=%+v err=%v", replay, err)
	}
	after, err := store.Quotas().Get(ctx, reservation.Key)
	if err != nil || after.Version != beforeVersion || after.Status != domain.QuotaReservationReleased {
		t.Fatalf("replaying rejected Plan must not settle quota twice: before=%+v after=%+v err=%v", reservation, after, err)
	}
	eventsAfter, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	quotaEventsAfter := 0
	for _, event := range eventsAfter {
		if event.Type == domain.EventQuotaReservationChanged || event.Type == domain.EventQuotaSpendRecorded {
			quotaEventsAfter++
		}
	}
	if quotaEventsAfter != quotaEventsBefore {
		t.Fatalf("replaying rejected Plan emitted duplicate quota settlement events: before=%d after=%d", quotaEventsBefore, quotaEventsAfter)
	}
}

func TestGovernedPlanSubmissionStorageFailureRetainsRecoveryCheckpoint(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "transient Plan storage failure", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a transient Plan write can be replayed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	const limit = int64(100)
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaOutputTokens, Limit: limit, Enforcement: domain.QuotaEnforcementAudit,
	})
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER governed_plan_insert_failpoint
BEFORE INSERT ON plans
WHEN NEW.client_key LIKE 'governance:%'
BEGIN SELECT RAISE(ABORT, 'injected governed Plan storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	if plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); plan != nil || err == nil || !strings.Contains(err.Error(), "injected governed Plan storage failure") {
		t.Fatalf("transient Plan storage failure must preserve a retryable error: plan=%+v err=%v", plan, err)
	}
	if _, err := db.Exec(`DROP TRIGGER governed_plan_insert_failpoint`); err != nil {
		t.Fatal(err)
	}
	goal, err = store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	key := domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: todo.LastTurnSeq}
	if todo.Status != domain.TodoRunning || todo.LastTurnSeq != 1 {
		t.Fatalf("transient Plan storage failure must retain the running recovery checkpoint: %+v", todo)
	}
	reservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: key, Kind: domain.QuotaOutputTokens})
	if err != nil || reservation.Status != domain.QuotaReservationReserved || reservation.ReservedAmount != limit {
		t.Fatalf("transient Plan storage failure must retain active reservation: %+v err=%v", reservation, err)
	}
	active, err := store.Quotas().SumActiveReserved(ctx, goal.ID, domain.QuotaOutputTokens)
	if err != nil || active != limit {
		t.Fatalf("transient Plan storage failure must not release active reservation: active=%d err=%v", active, err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, key)
	if err != nil || len(phases) != 2 {
		t.Fatalf("transient Plan storage failure must retain only validated receipt prefix: phases=%+v err=%v", phases, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentRunID != source.ID || state.Phase != "plan_commit" ||
		state.Data["governance_plan_retry_run_id"] != source.ID {
		t.Fatalf("transient Plan storage failure must retain a source-run recovery checkpoint: %+v", state)
	}
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil || plan == nil {
		t.Fatalf("replaying after transient Plan storage failure must commit the Plan: plan=%+v err=%v", plan, err)
	}
	phases, err = store.TurnReceipts().ListPhases(ctx, key)
	if err != nil || len(phases) < 5 || phases[2].Phase != domain.TurnReceiptPhaseDurableWriteback {
		t.Fatalf("replayed Plan must continue the receipt after the retained prefix: phases=%+v err=%v", phases, err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := state.Data["governance_plan_retry_run_id"]; pending {
		t.Fatalf("successful Plan replay must clear the storage recovery checkpoint: %+v", state)
	}
}

func TestGovernedPlanSubmissionStorageFailureDoesNotBlockTerminalCoordinator(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "terminal Plan storage failure", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"a terminal storage failure remains recoverable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER governed_plan_terminal_insert_failpoint
BEFORE INSERT ON plans
WHEN NEW.client_key LIKE 'governance:%'
BEGIN SELECT RAISE(ABORT, 'injected terminal Plan storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	decision, err := json.Marshal(compilerDecision(domain.PlanVerbDispatch, workerID))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(decision)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER governed_plan_terminal_insert_failpoint`); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status == domain.CoordinatorBlocked || state.Status == domain.CoordinatorWaitingUser ||
		state.Data["governance_plan_retry_run_id"] != source.ID || state.CurrentRunID != source.ID {
		t.Fatalf("transient terminal Plan storage failure must not block or deliver the Coordinator: %+v", state)
	}
	if plan, err := store.Plans().GetBySourceRun(ctx, source.ID); err != nil || plan != nil {
		t.Fatalf("transient terminal Plan storage failure must not create a partial Plan: plan=%+v err=%v", plan, err)
	}
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := store.Plans().GetBySourceRun(ctx, source.ID)
	if err != nil || plan == nil {
		t.Fatalf("replaying the terminal source Run must recover the governed Plan: plan=%+v err=%v", plan, err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := state.Data["governance_plan_retry_run_id"]; pending || state.Status == domain.CoordinatorBlocked {
		t.Fatalf("successful terminal replay must clear the Plan storage checkpoint: %+v", state)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("terminal replay must dispatch exactly one Worker Run: %d", len(dispatcher.runs))
	}
}

func TestCoordinatorPlanDecisionAutomaticallyUsesGovernanceCompilerWhenGovernanceStateExists(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "automatic governed coordinator", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"Coordinator decision is receipt-bound"},
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
	decision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"use the admitted Todo","next_action":"wait for the scoped worker","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": decision}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := store.Plans().GetBySourceRun(ctx, source.ID)
	if err != nil || plan == nil || plan.GovernanceTurnKey == nil {
		t.Fatalf("Coordinator did not use the governance compiler: plan=%+v err=%v", plan, err)
	}
	if len(dispatcher.runs) != 2 || plan.Steps[0].ResultRunID != dispatcher.runs[1].ID {
		t.Fatalf("governed Coordinator dispatch mismatch: plan=%+v runs=%+v", plan, dispatcher.runs)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, *plan.GovernanceTurnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("automatic governed Coordinator did not settle phases 1..7: phases=%+v err=%v", phases, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorRunning || state.Phase != "executing" || state.CurrentRunID != "" {
		t.Fatalf("governed Plan did not return the Coordinator to Worker observation: %+v", state)
	}
}

func TestSubmitGovernedTodoPlanDecisionConcurrentReplayCreatesOnePlan(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "concurrent governed replay", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"concurrent replay is exactly once"},
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
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	const callers = 8
	type result struct {
		plan *domain.Plan
		err  error
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
			results <- result{plan: plan, err: err}
		}()
	}
	wg.Wait()
	close(results)
	planID := ""
	for result := range results {
		if result.err != nil || result.plan == nil {
			t.Fatalf("concurrent governed replay failed: plan=%+v err=%v", result.plan, result.err)
		}
		if planID == "" {
			planID = result.plan.ID
		} else if result.plan.ID != planID {
			t.Fatalf("concurrent governed replay returned multiple Plans: first=%s got=%s", planID, result.plan.ID)
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
	if todo.LastTurnSeq != 1 {
		t.Fatalf("concurrent admission allocated more than one turn_seq: %+v", todo)
	}
	plan, err := store.Plans().GetByClientKey(ctx, wsID, governancePlanClientKeyForTest(goal.ID, todo.ID, 1))
	if err != nil || plan == nil || plan.ID != planID {
		t.Fatalf("concurrent governed replay client lookup mismatch: plan=%+v err=%v", plan, err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, *plan.GovernanceTurnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("concurrent governed replay receipt mismatch: phases=%+v err=%v", phases, err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("defer decision concurrent replay created a Run: %d", len(dispatcher.runs))
	}
}

func governancePlanClientKeyForTest(goalID, todoID string, turnSeq int64) string {
	return "governance:" + goalID + ":" + todoID + ":" + fmt.Sprint(turnSeq)
}

func TestSubmitGovernedTodoPlanDecisionAllocatesNextTurnAfterWaiting(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governed next turn", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"turn_seq advances after settlement"},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstSource := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, firstSource.ID)
	firstSource, err = store.Runs().Get(ctx, firstSource.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	firstPlan, err := svc.SubmitGovernedTodoPlanDecision(ctx, firstSource, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.GovernanceTurnKey == nil || firstPlan.GovernanceTurnKey.TurnSeq != 1 {
		t.Fatalf("first governed turn identity mismatch: %+v", firstPlan)
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
	if state.Data == nil {
		state.Data = map[string]any{}
	}
	state.Data["control_action"] = "recover"
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("second bounded turn did not create one Coordinator Run: %d", len(dispatcher.runs))
	}
	secondSource := dispatcher.runs[1]
	markCompilerSourceSucceeded(t, ctx, store, secondSource.ID)
	secondSource, err = store.Runs().Get(ctx, secondSource.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := svc.SubmitGovernedTodoPlanDecision(ctx, secondSource, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if secondPlan.GovernanceTurnKey == nil || secondPlan.GovernanceTurnKey.TurnSeq != 2 || secondPlan.ID == firstPlan.ID {
		t.Fatalf("second governed turn identity mismatch: first=%+v second=%+v", firstPlan, secondPlan)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoWaiting || todo.LastTurnSeq != 2 || todo.ClaimVersion < 2 {
		t.Fatalf("second turn did not renew/settle the same Todo: %+v", todo)
	}
	for _, key := range []domain.TurnKey{*firstPlan.GovernanceTurnKey, *secondPlan.GovernanceTurnKey} {
		phases, err := store.TurnReceipts().ListPhases(ctx, key)
		if err != nil || len(phases) != 7 {
			t.Fatalf("turn %d receipt phases mismatch: phases=%+v err=%v", key.TurnSeq, phases, err)
		}
	}
	oldReplay, err := svc.SubmitGovernedTodoPlanDecision(ctx, firstSource, decision, application.PlanCandidateNativeText)
	if err != nil || oldReplay.ID != firstPlan.ID || len(dispatcher.runs) != 2 {
		t.Fatalf("older turn replay must not disturb turn 2: plan=%+v err=%v runs=%d", oldReplay, err, len(dispatcher.runs))
	}
}

func TestGovernedCoordinatorRunsWorkerSettlementAndSecondTurn(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governed two-turn lifecycle", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"worker result is summarized before delivery"},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstSource := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, firstSource.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	firstDecision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch scoped work","next_action":"wait for settlement","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
	if err := svc.RecordRunEvent(ctx, firstSource.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": firstDecision}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, firstSource.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("first turn must dispatch one Worker: %d", len(dispatcher.runs))
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
		if _, ok := wakeups[index].Context[domain.WakeupContextSettlementDispatchID].(string); ok {
			settlement = &wakeups[index]
			break
		}
	}
	if settlement == nil {
		t.Fatalf("worker settlement wakeup missing: %+v", wakeups)
	}
	scheduler := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	if outcome, err := scheduler.ConsumeOne(ctx, *settlement, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeConsumed {
		t.Fatalf("settlement wake failed: outcome=%s err=%v", outcome, err)
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("settlement must create one summary Coordinator Run: %d", len(dispatcher.runs))
	}
	secondSource := dispatcher.runs[2]
	if err := svc.RecordRunStatus(ctx, secondSource.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	secondDecision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"observe the explicit governed child","next_action":"wait on the direct child reference","steps":[{"verb":"join","children":["` + worker.WorkItemID + `"],"wake_at":"2099-01-01T00:00:00Z"}]}`
	if err := finishRun(ctx, svc, secondSource.ID, secondDecision); err != nil {
		t.Fatal(err)
	}
	secondPlan, err := store.Plans().GetBySourceRun(ctx, secondSource.ID)
	if err != nil || secondPlan == nil || secondPlan.GovernanceTurnKey == nil || secondPlan.GovernanceTurnKey.TurnSeq != 2 {
		t.Fatalf("settlement summary did not create governed turn 2: plan=%+v err=%v", secondPlan, err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoWaiting || todo.LastTurnSeq != 2 {
		t.Fatalf("governed Todo did not settle two turns: %+v", todo)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorRunning || state.Phase != "executing" {
		t.Fatalf("explicit direct-child join did not create a governed waiting Plan: %+v", state)
	}
	for seq := int64(1); seq <= 2; seq++ {
		phases, err := store.TurnReceipts().ListPhases(ctx, domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: seq})
		if err != nil || len(phases) != 7 {
			t.Fatalf("governed turn %d receipt mismatch: phases=%+v err=%v", seq, phases, err)
		}
	}
}

func TestGovernedAuthorityBlockUnblockStartsFreshTurn(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governed unblock", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"user can recover a rejected governed turn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstSource := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, firstSource.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	invalid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"invalid worker","next_action":"must be rejected","steps":[{"verb":"dispatch","agent_id":"agent_outside_frozen_scope","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
	if err := svc.RecordRunEvent(ctx, firstSource.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": invalid}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, firstSource.ID, status, nil); err != nil {
			t.Fatal(err)
		}
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
	if root.Status != domain.WorkItemBlocked || state.Status != domain.CoordinatorBlocked ||
		goal.Status != domain.GoalBlocked || goal.Phase != "blocked" ||
		todo.Status != domain.TodoBlocked || todo.Claim != nil || todo.LastTurnSeq != 1 {
		t.Fatalf("authority rejection did not block all governed projections: root=%+v state=%+v goal=%+v todo=%+v", root, state, goal, todo)
	}
	if _, err := svc.UnblockWorkItem(ctx, root.ID, root.Version); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("unblock must create exactly one new Coordinator Run: %d", len(dispatcher.runs))
	}
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != domain.GoalActive || goal.Phase != "execution" ||
		todo.Status != domain.TodoPending || todo.Claim != nil {
		t.Fatalf("unblock must resume claim-free governance state before the fresh turn: goal=%+v todo=%+v", goal, todo)
	}
	secondSource := dispatcher.runs[1]
	if err := svc.RecordRunStatus(ctx, secondSource.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	valid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"recover with a valid finish","next_action":"wait for user acceptance","steps":[{"verb":"finish"}]}`
	if err := finishRun(ctx, svc, secondSource.ID, valid); err != nil {
		t.Fatal(err)
	}
	secondPlan, err := store.Plans().GetBySourceRun(ctx, secondSource.ID)
	if err != nil || secondPlan == nil || secondPlan.GovernanceTurnKey == nil || secondPlan.GovernanceTurnKey.TurnSeq != 2 {
		t.Fatalf("post-unblock valid decision did not create turn 2: plan=%+v err=%v", secondPlan, err)
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoBlocked || todo.LastTurnSeq != 2 || state.Status != domain.CoordinatorBlocked {
		t.Fatalf("post-unblock finish without evaluation must block: todo=%+v state=%+v", todo, state)
	}
}

func TestGovernedFinishEvaluationReceiptReferencesEvaluationRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governed evaluation", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"evaluation runs before acceptance"},
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
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	evaluate := true
	decision.Steps[0].Finish.Evaluation = &evaluate
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("governed finish evaluation must create one evaluation Run: %d", len(dispatcher.runs))
	}
	evaluation := dispatcher.runs[1]
	if plan.Steps[0].ResultRunID != evaluation.ID {
		t.Fatalf("finish step did not retain evaluation Run reference: plan=%+v eval=%+v", plan, evaluation)
	}
	governanceContext, _ := evaluation.Input["governance"].(map[string]any)
	if governanceContext["goal_id"] != plan.GovernanceTurnKey.GoalID ||
		governanceContext["todo_id"] != plan.GovernanceTurnKey.TodoID ||
		governanceContext["turn_seq"] != plan.GovernanceTurnKey.TurnSeq {
		t.Fatalf("evaluation Run lost governance identity: %#v", governanceContext)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, *plan.GovernanceTurnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("governed evaluation receipt missing: phases=%+v err=%v", phases, err)
	}
	if len(phases[3].RunIDs) != 1 || phases[3].RunIDs[0] != evaluation.ID ||
		len(phases[4].RunIDs) != 1 || phases[4].RunIDs[0] != evaluation.ID {
		t.Fatalf("receipt did not bind evaluation Run: phase4=%+v phase5=%+v", phases[3], phases[4])
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentRunID != evaluation.ID || state.Phase != "evaluation" {
		t.Fatalf("evaluation Run did not own the Coordinator control line: %+v", state)
	}
}

func TestGovernedEvaluationPublishesValidationResultEvent(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "validation result event", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"a canonical validation event is emitted"},
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
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	evaluate := true
	decision.Steps[0].Finish.Evaluation = &evaluate
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, evaluation.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunEvent(ctx, evaluation.ID, domain.EventMessageCompleted, map[string]any{
		"role": "assistant", "text": "```verdict\n{\"pass\":true,\"reasons\":[\"all checks passed\"]}\n```",
	}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, evaluation.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if plan == nil || plan.GovernanceTurnKey == nil {
		t.Fatalf("governed evaluation plan lost TurnKey: %+v", plan)
	}
	events, err := store.Events().Since(ctx, wsID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event != nil && event.Type == domain.EventValidationRecorded && event.AggregateType == domain.AggregateValidationResult {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("one canonical validation.result_recorded event required, got %d", count)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	acceptRequestVersion := root.Version
	if _, err := svc.AcceptWorkItem(ctx, root.ID, acceptRequestVersion); err != nil {
		t.Fatalf("human accept should close the validated governed Goal: %v", err)
	}
	events, err = store.Events().Since(ctx, wsID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	evidenceEvents := 0
	for _, event := range events {
		if event != nil && event.Type == domain.EventGoalEvidenceAdded && event.AggregateType == domain.AggregateGoal {
			evidenceEvents++
		}
	}
	if evidenceEvents != 2 {
		t.Fatalf("human accept must publish root and validation Goal evidence events, got %d", evidenceEvents)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != domain.GoalCompleted {
		t.Fatalf("human accept must complete the governed Goal: %+v", goal)
	}
	projection, err := svc.GetGovernanceProjection(ctx, goal.ID)
	if err != nil {
		t.Fatalf("accepted Goal projection must be immediately readable: %v", err)
	}
	if projection.GoalProgress["status"] != string(domain.GoalCompleted) {
		t.Fatalf("accepted Goal projection status is stale: %+v", projection.GoalProgress)
	}
	if len(projection.EvidenceSummary) < 2 {
		t.Fatalf("accepted Goal projection must include root and validation evidence: %+v", projection.EvidenceSummary)
	}
}

func TestGovernedPlanReplayRepairsCrashAfterPlanCommitBeforeReceiptPhase(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "repair receipt gap", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"receipt gap is replayable"},
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
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, source.AgentProfileID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := application.ComputeTodoPlanInputSnapshotDigest(goal, claimed, source)
	if err != nil {
		t.Fatal(err)
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: claimed.ID, OwnerAgentID: source.AgentProfileID,
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "plan-decision/v2",
		InputSnapshotDigest: inputDigest, AdmissionClientKey: "plan-decision:" + source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	compiled, err := svc.CompileTodoPlan(ctx, application.TodoToPlanCompileInput{
		TurnKey: header.TurnKey, OwnerAgentID: source.AgentProfileID, SourceRunID: source.ID,
		Decision: decision, SchemaDigest: workbenchcontracts.PlanDecisionV2SchemaDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	turnDecision := &domain.TurnDecision{
		TurnKey: header.TurnKey, Decision: domain.TurnDecisionExecute,
		Reason: decision.Reason, NextAction: decision.NextAction, SchemaVersion: decision.SchemaVersion,
		PlanDecision: decision, ValidationErrors: []domain.GovernanceValidationError{}, RecordedAt: header.CreatedAt,
	}
	appendPhase := func(seq int, payload map[string]any) {
		t.Helper()
		name, _ := domain.TurnReceiptPhaseNameForSeq(seq)
		if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
			TurnKey: header.TurnKey, PhaseSeq: seq, Phase: name, Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendPhase(1, map[string]any{
		"candidate_source": string(application.PlanCandidateNativeText),
		"decision_digest":  compiled.DecisionDigest, "schema_version": decision.SchemaVersion,
		"schema_digest": workbenchcontracts.PlanDecisionV2SchemaDigest(), "source_run_id": source.ID,
		"turn_decision": turnDecision,
	})
	appendPhase(2, map[string]any{"valid": true, "authority": "passed", "decision_digest": compiled.DecisionDigest})
	appendPhase(3, map[string]any{
		"plan_client_key": compiled.PlanClientKey, "source_run_id": source.ID,
		"decision_digest": compiled.DecisionDigest,
	})
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: compiled.WorkItemID, AgentProfileID: compiled.AgentProfileID,
		SourceRunID: compiled.SourceRunID, Steps: compiled.Steps, Guardrails: compiled.Guardrails,
		DecisionAudit: compiled.Audit,
		Governance: &application.PlanGovernanceInput{
			ClientKey: compiled.PlanClientKey, TurnKey: compiled.TurnKey,
			SchemaVersion: compiled.Audit.SchemaVersion, SchemaDigest: compiled.SchemaDigest,
			DecisionDigest: compiled.DecisionDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 3 {
		t.Fatalf("precondition: simulated crash must leave phases 1..3 only: phases=%+v err=%v", phases, err)
	}
	repaired, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.ID != plan.ID || len(dispatcher.runs) != 1 {
		t.Fatalf("receipt gap repair duplicated Plan/Run: original=%s repaired=%s runs=%d", plan.ID, repaired.ID, len(dispatcher.runs))
	}
	phases, err = store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 7 || phases[3].PlanID != plan.ID || phases[4].PlanID != plan.ID {
		t.Fatalf("receipt gap was not repaired to phases 1..7: phases=%+v err=%v", phases, err)
	}
}

func TestGovernedPlanReplayContinuesAfterValidatedPhaseCrash(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "resume after validation phase", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"validated receipt resumes compilation"},
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
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, source.AgentProfileID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := application.ComputeTodoPlanInputSnapshotDigest(goal, claimed, source)
	if err != nil {
		t.Fatal(err)
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: claimed.ID, OwnerAgentID: source.AgentProfileID,
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "plan-decision/v2",
		InputSnapshotDigest: inputDigest, AdmissionClientKey: "plan-decision:" + source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	compiled, err := svc.CompileTodoPlan(ctx, application.TodoToPlanCompileInput{
		TurnKey: header.TurnKey, OwnerAgentID: source.AgentProfileID, SourceRunID: source.ID,
		Decision: decision, SchemaDigest: workbenchcontracts.PlanDecisionV2SchemaDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	turnDecision := &domain.TurnDecision{
		TurnKey: header.TurnKey, Decision: domain.TurnDecisionFinish,
		Reason: decision.Reason, NextAction: decision.NextAction, SchemaVersion: decision.SchemaVersion,
		PlanDecision: decision, ValidationErrors: []domain.GovernanceValidationError{}, RecordedAt: header.CreatedAt,
	}
	appendPhase := func(seq int, payload map[string]any) {
		t.Helper()
		name, _ := domain.TurnReceiptPhaseNameForSeq(seq)
		if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
			TurnKey: header.TurnKey, PhaseSeq: seq, Phase: name, Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendPhase(1, map[string]any{
		"candidate_source": string(application.PlanCandidateNativeText),
		"decision_digest":  compiled.DecisionDigest, "schema_version": decision.SchemaVersion,
		"schema_digest": workbenchcontracts.PlanDecisionV2SchemaDigest(), "source_run_id": source.ID,
		"turn_decision": turnDecision,
	})
	appendPhase(2, map[string]any{"valid": true, "authority": "passed", "decision_digest": compiled.DecisionDigest})
	if phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey); err != nil || len(phases) != 2 {
		t.Fatalf("precondition: simulated crash must stop after validation: phases=%+v err=%v", phases, err)
	}
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.GovernanceTurnKey == nil || !plan.GovernanceTurnKey.Equal(header.TurnKey) {
		t.Fatalf("validated phase replay did not resume Plan compilation: %+v", plan)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("validated phase replay did not complete phases 3..7: phases=%+v err=%v", phases, err)
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil || todo.Status != domain.TodoWaiting {
		t.Fatalf("validated phase replay did not settle Todo: todo=%+v err=%v", todo, err)
	}
}

func TestGovernedPlanReplaySettlesInvalidValidationPhaseToBlocked(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "resume invalid validation phase", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"invalid receipt replay blocks Todo"},
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
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, source.AgentProfileID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := application.ComputeTodoPlanInputSnapshotDigest(goal, claimed, source)
	if err != nil {
		t.Fatal(err)
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: claimed.ID, OwnerAgentID: source.AgentProfileID,
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "plan-decision/v2",
		InputSnapshotDigest: inputDigest, AdmissionClientKey: "plan-decision:" + source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	compiled, err := svc.CompileTodoPlan(ctx, application.TodoToPlanCompileInput{
		TurnKey: header.TurnKey, OwnerAgentID: source.AgentProfileID, SourceRunID: source.ID,
		Decision: decision, SchemaDigest: workbenchcontracts.PlanDecisionV2SchemaDigest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	turnDecision := &domain.TurnDecision{
		TurnKey: header.TurnKey, Decision: domain.TurnDecisionExecute,
		Reason: decision.Reason, NextAction: decision.NextAction, SchemaVersion: decision.SchemaVersion,
		PlanDecision: decision, ValidationErrors: []domain.GovernanceValidationError{}, RecordedAt: header.CreatedAt,
	}
	appendPhase := func(seq int, payload map[string]any) {
		t.Helper()
		name, _ := domain.TurnReceiptPhaseNameForSeq(seq)
		if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
			TurnKey: header.TurnKey, PhaseSeq: seq, Phase: name, Payload: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendPhase(1, map[string]any{
		"candidate_source": string(application.PlanCandidateNativeText),
		"decision_digest":  compiled.DecisionDigest, "schema_version": decision.SchemaVersion,
		"schema_digest": workbenchcontracts.PlanDecisionV2SchemaDigest(), "source_run_id": source.ID,
		"turn_decision": turnDecision,
	})
	appendPhase(2, map[string]any{
		"valid": false, "error_code": string(domain.GovernanceErrorPlanAuthorityDenied),
		"path": "/steps/0/agent_id", "message": "agent is outside frozen scope",
	})
	if plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); plan != nil || err == nil {
		t.Fatalf("invalid validation replay must remain rejected: plan=%+v err=%v", plan, err)
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil || todo.Status != domain.TodoBlocked {
		t.Fatalf("invalid validation replay did not repair running→blocked gap: todo=%+v err=%v", todo, err)
	}
	if plan, err := store.Plans().LatestByWorkItem(ctx, root.ID); err != nil || plan != nil {
		t.Fatalf("invalid validation replay created a Plan: plan=%+v err=%v", plan, err)
	}
}

func TestGovernedAdmittedTurnResumesAfterGoalPauseResume(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "resume admitted turn", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"pause resume keeps one admitted turn"},
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
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, todo.ID, source.AgentProfileID, todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := application.ComputeTodoPlanInputSnapshotDigest(goal, claimed, source)
	if err != nil {
		t.Fatal(err)
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: claimed.ID, OwnerAgentID: source.AgentProfileID,
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "plan-decision/v2",
		InputSnapshotDigest: inputDigest, AdmissionClientKey: "plan-decision:" + source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbDispatch, workerID)
	decisionJSON, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": string(decisionJSON)}); err != nil {
		t.Fatal(err)
	}
	paused, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := svc.ResumeGoal(ctx, goal.ID, paused.Version)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != domain.GoalActive || before.Status != domain.TodoWaiting || before.LastTurnSeq != header.TurnKey.TurnSeq {
		t.Fatalf("pause/resume precondition mismatch: goal=%+v todo=%+v", resumed, before)
	}
	plan, err := store.Plans().GetBySourceRun(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.GovernanceTurnKey == nil || !plan.GovernanceTurnKey.Equal(header.TurnKey) {
		t.Fatalf("pause/resume allocated a different turn: header=%+v plan=%+v", header, plan)
	}
	after, err := store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.TodoWaiting || after.LastTurnSeq != 1 {
		t.Fatalf("resumed admitted turn did not settle once: %+v", after)
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, header.TurnKey)
	if err != nil || len(phases) != 7 {
		t.Fatalf("pause/resume admitted turn receipt mismatch: phases=%+v err=%v", phases, err)
	}
}

func testPlanDecisionDigest(t *testing.T, decision *domain.PlanDecisionV2) string {
	t.Helper()
	raw, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}
