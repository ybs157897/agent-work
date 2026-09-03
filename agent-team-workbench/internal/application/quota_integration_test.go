package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func setGoalQuotaPolicies(t *testing.T, ctx context.Context, store application.Store,
	goal *domain.Goal, policies ...domain.QuotaPolicy) *domain.Goal {
	t.Helper()
	expected := goal.Version
	goal.QuotaPolicies = append([]domain.QuotaPolicy{}, policies...)
	goal.Version++
	goal.UpdatedAt = time.Now().UTC()
	if err := store.Goals().Update(ctx, goal, expected); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func TestTurnCountQuotaAuditCommitsAdmissionOnce(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "turn quota audit", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"audit records without denying"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaTurnCount, Limit: 0, Enforcement: domain.QuotaEnforcementAudit,
	})
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	key := domain.QuotaReservationKey{TurnKey: *plan.GovernanceTurnKey, Kind: domain.QuotaTurnCount}
	reservation, err := store.Quotas().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Status != domain.QuotaReservationCommitted || reservation.ReservedAmount != 1 ||
		reservation.CommittedAmount != 1 || reservation.PolicyLimit != 0 ||
		reservation.PolicyEnforcement != domain.QuotaEnforcementAudit {
		t.Fatalf("turn_count audit reservation mismatch: %+v", reservation)
	}
	events, err := store.Events().Since(ctx, root.WorkspaceID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var quotaEvents []*domain.CanonicalEvent
	for _, event := range events {
		if event.Type == domain.EventQuotaReservationChanged && event.Data["quota_kind"] == string(domain.QuotaTurnCount) {
			quotaEvents = append(quotaEvents, event)
		}
	}
	if len(quotaEvents) != 2 || quotaEvents[0].Data["status"] != string(domain.QuotaReservationReserved) ||
		quotaEvents[1].Data["status"] != string(domain.QuotaReservationCommitted) {
		t.Fatalf("turn_count reservation lifecycle must emit one event per real state change: %+v", quotaEvents)
	}
	for _, event := range quotaEvents {
		if event.AggregateType != domain.AggregateGoal || event.AggregateID != goal.ID ||
			event.Aggregate.Version != goal.Version ||
			event.Data["goal_id"] != goal.ID || event.Data["todo_id"] != reservation.Key.TurnKey.TodoID ||
			event.Data["policy_digest"] != reservation.PolicyDigest {
			t.Fatalf("quota reservation event lost canonical identity/policy: %+v", event)
		}
	}
	quotaEventCountBeforeReplay := len(quotaEvents)
	phases, err := store.TurnReceipts().ListPhases(ctx, *plan.GovernanceTurnKey)
	if err != nil || len(phases) != 7 || phases[5].Phase != domain.TurnReceiptPhaseQuotaSpend ||
		len(phases[5].QuotaReservationKeys) != 1 || phases[5].QuotaReservationKeys[0] != reservation.Key.String() {
		t.Fatalf("turn_count reservation was not bound into receipt phase6: phases=%+v err=%v", phases, err)
	}
	if total, err := store.Quotas().SumCommitted(ctx, goal.ID, domain.QuotaTurnCount); err != nil || total != 1 {
		t.Fatalf("turn_count committed total mismatch: total=%d err=%v", total, err)
	}
	replayed, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil || replayed.ID != plan.ID {
		t.Fatalf("turn_count replay mismatch: plan=%+v err=%v", replayed, err)
	}
	events, err = store.Events().Since(ctx, root.WorkspaceID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	quotaEventCountAfterReplay := 0
	for _, event := range events {
		if event.Type == domain.EventQuotaReservationChanged && event.Data["quota_kind"] == string(domain.QuotaTurnCount) {
			quotaEventCountAfterReplay++
		}
	}
	if quotaEventCountAfterReplay != quotaEventCountBeforeReplay {
		t.Fatalf("turn_count reservation replay must not emit duplicate events: before=%d after=%d", quotaEventCountBeforeReplay, quotaEventCountAfterReplay)
	}
	if total, err := store.Quotas().SumCommitted(ctx, goal.ID, domain.QuotaTurnCount); err != nil || total != 1 {
		t.Fatalf("turn_count replay double charged: total=%d err=%v", total, err)
	}
	shouldRun, err := svc.ShouldRunLocked(ctx, application.ShouldRunRequest{
		GoalID: goal.ID, Kind: domain.QuotaTurnCount, Amount: 1,
	})
	if err != nil || !shouldRun.Allowed || !shouldRun.WouldDeny || shouldRun.Used != 1 {
		t.Fatalf("audit/enforce calculation diverged: decision=%+v err=%v", shouldRun, err)
	}
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaTurnCount, Limit: 99, Enforcement: domain.QuotaEnforcementEnforce,
	})
	if replayed, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText); err != nil || replayed.ID != plan.ID {
		t.Fatalf("receipt replay must use frozen quota policy: plan=%+v err=%v", replayed, err)
	}
	phase6, err := store.TurnReceipts().GetPhase(ctx, *plan.GovernanceTurnKey, 6)
	if err != nil || phase6.QuotaReservationKeys[0] != reservation.Key.String() {
		t.Fatalf("frozen quota receipt changed after Goal policy update: phase=%+v err=%v", phase6, err)
	}
}

func TestTurnCountQuotaEnforceDeniesBeforeClaimHeaderAndPlan(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "turn quota enforce", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"enforce denial leaves no admission"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaTurnCount, Limit: 0, Enforcement: domain.QuotaEnforcementEnforce,
	})
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
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
		compilerDecision(domain.PlanVerbFinish, workerID), application.PlanCandidateNativeText)
	var decisionErr *application.PlanDecisionError
	if plan != nil || !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied {
		t.Fatalf("turn_count enforce denial mismatch: plan=%+v err=%v", plan, err)
	}
	todo, err = store.Todos().Get(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.Status != domain.TodoPending || todo.Claim != nil || todo.LastTurnSeq != 0 {
		t.Fatalf("quota denial left claim/admission state: %+v", todo)
	}
	if _, err := store.TurnReceipts().GetHeader(ctx, domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: 1}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("quota denial created Header: %v", err)
	}
	if _, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: 1}, Kind: domain.QuotaTurnCount,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("quota denial created reservation: %v", err)
	}
	if existing, err := store.Plans().LatestByWorkItem(ctx, root.ID); err != nil || existing != nil {
		t.Fatalf("quota denial created Plan: plan=%+v err=%v", existing, err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("quota denial dispatched a Run: %d", len(dispatcher.runs))
	}
}

func TestTurnCountQuotaEnforceAllowsLimitThenDeniesNextCoordinatorRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "turn quota next turn", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"only one governance turn is admitted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaTurnCount, Limit: 1, Enforcement: domain.QuotaEnforcementEnforce,
	})
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
		t.Fatalf("first turn should be admitted: %+v", firstPlan)
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
	var decisionErr *application.PlanDecisionError
	err = svc.StartCoordinator(ctx, root.ID)
	if !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied {
		t.Fatalf("next Coordinator source should be quota denied before Run creation: err=%v", err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("quota denial created or dispatched a second Coordinator Run: %d", len(dispatcher.runs))
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if todo.LastTurnSeq != 1 || todo.Status != domain.TodoBlocked || todo.Claim != nil ||
		goal.Status != domain.GoalBlocked || goal.Phase != "blocked" {
		t.Fatalf("denied second turn must preserve admission watermark while blocking governance: goal=%+v todo=%+v", goal, todo)
	}
	if _, err := store.TurnReceipts().GetHeader(ctx, domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: 2}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("denied second turn created Header: %v", err)
	}
	if total, err := store.Quotas().SumCommitted(ctx, goal.ID, domain.QuotaTurnCount); err != nil || total != 1 {
		t.Fatalf("denied second turn changed committed turn count: total=%d err=%v", total, err)
	}
}

func TestActiveWorkerQuotaEnforceRollsBackWholePlan(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "active worker enforce", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"worker concurrency is atomic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaActiveWorker, Limit: 1, Enforcement: domain.QuotaEnforcementEnforce,
	})
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err = store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := &domain.PlanDecisionV2{
		SchemaVersion: "plan-decision/v2", Kind: "plan", Reason: "two workers exceed one slot", NextAction: "deny atomically",
		Steps: []domain.PlanDecisionStepV2{
			{Verb: domain.PlanVerbDispatch, Dispatch: &domain.PlanDispatchStepV2{
				AgentID: workerID, Title: "A", Instruction: "do A", Acceptance: []string{"A done"},
			}},
			{Verb: domain.PlanVerbDispatch, Dispatch: &domain.PlanDispatchStepV2{
				AgentID: workerID, Title: "B", Instruction: "do B", Acceptance: []string{"B done"},
			}},
			{Verb: domain.PlanVerbJoin, Join: &domain.PlanJoinStepV2{Children: domain.JoinChildren{All: true}}},
		},
	}
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	var decisionErr *application.PlanDecisionError
	if plan != nil || !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied {
		t.Fatalf("active_worker enforce denial mismatch: plan=%+v err=%v", plan, err)
	}
	if existing, err := store.Plans().LatestByWorkItem(ctx, root.ID); err != nil || existing != nil {
		t.Fatalf("active_worker denial left partial Plan: plan=%+v err=%v", existing, err)
	}
	children, err := store.WorkItems().ListByParent(ctx, root.ID)
	if err != nil || len(children) != 0 {
		t.Fatalf("active_worker denial left partial children: children=%+v err=%v", children, err)
	}
	if count, err := store.Quotas().ActiveWorkerCount(ctx, goal.ID); err != nil || count != 0 {
		t.Fatalf("active_worker rollback left active Run: count=%d err=%v", count, err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("active_worker denial dispatched partial Run: %d", len(dispatcher.runs))
	}
}

func TestActiveWorkerQuotaAuditRecordsWouldDenyOnWorkerRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "active worker audit", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"audit does not block execution"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaActiveWorker, Limit: 0, Enforcement: domain.QuotaEnforcementAudit,
	})
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
	if plan == nil || len(dispatcher.runs) != 2 {
		t.Fatalf("active_worker audit should allow one Worker: plan=%+v runs=%d", plan, len(dispatcher.runs))
	}
	worker := dispatcher.runs[1]
	admission, _ := worker.Input["quota_admission"].(map[string]any)
	if admission["quota_kind"] != string(domain.QuotaActiveWorker) || admission["allowed"] != true ||
		admission["would_deny"] != true || admission["enforcement"] != string(domain.QuotaEnforcementAudit) {
		t.Fatalf("Worker Run quota audit snapshot mismatch: %#v", admission)
	}
	if count, err := store.Quotas().ActiveWorkerCount(ctx, goal.ID); err != nil || count != 1 {
		t.Fatalf("active_worker gauge mismatch: count=%d err=%v", count, err)
	}
	storedWorker, err := store.Runs().Get(ctx, worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := storedWorker.Version
	if err := storedWorker.Transition(domain.RunCancelled, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Runs().Update(ctx, storedWorker, expected); err != nil {
		t.Fatal(err)
	}
	if count, err := store.Quotas().ActiveWorkerCount(ctx, goal.ID); err != nil || count != 0 {
		t.Fatalf("terminal Worker did not release active_worker gauge: count=%d err=%v", count, err)
	}
	if _, err := store.Quotas().SumCommitted(ctx, goal.ID, domain.QuotaActiveWorker); err == nil {
		t.Fatal("active_worker must remain a gauge, not cumulative spend")
	}
	phases, err := store.TurnReceipts().ListPhases(ctx, *plan.GovernanceTurnKey)
	if err != nil || len(phases) != 7 || phases[5].Phase != domain.TurnReceiptPhaseQuotaSpend ||
		len(phases[5].QuotaReservationKeys) != 0 || phases[5].Payload["active_worker_accounting"] != "gauge_not_spend" {
		t.Fatalf("active_worker-only turn must close quota phase without fake spend: phases=%+v err=%v", phases, err)
	}
}

func TestTurnCountQuotaGatesCoordinatorRunBeforeCreation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enforcement domain.QuotaEnforcement
		wantRun     bool
	}{
		{name: "enforce", enforcement: domain.QuotaEnforcementEnforce, wantRun: false},
		{name: "audit", enforcement: domain.QuotaEnforcementAudit, wantRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
			config, err := store.TaskCoordinators().EnsureConfig(ctx, wsID)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			root := &domain.WorkItem{
				ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
				Title: "coordinator quota " + tc.name, Status: domain.WorkItemInProgress,
				Priority: domain.PriorityMedium, AgentProfileID: config.AgentProfileID,
				AcceptanceCriteria: []string{"quota gates source run"},
				Version:            1, CreatedAt: now, UpdatedAt: now,
			}
			if err := store.WorkItems().Create(ctx, root); err != nil {
				t.Fatal(err)
			}
			goal, err := svc.CreateGoal(ctx, wsID, application.CreateGoalParams{
				RootWorkItemID: root.ID, Objective: root.Title,
				AcceptanceContract: root.AcceptanceCriteria,
				QuotaPolicies: []domain.QuotaPolicy{{
					Kind: domain.QuotaTurnCount, Limit: 0, Enforcement: tc.enforcement,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.StartGoal(ctx, goal.ID, goal.Version); err != nil {
				t.Fatal(err)
			}
			state := &domain.TaskCoordinatorState{
				ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: wsID,
				RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID,
				Status: domain.CoordinatorQueued, Phase: "queued", CurrentAction: "queued",
				Data:    map[string]any{"acceptance_criteria": root.AcceptanceCriteria},
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}
			if err := store.TaskCoordinators().CreateState(ctx, state); err != nil {
				t.Fatal(err)
			}
			if err := store.TaskComments().EnsureCursor(ctx, root.ID); err != nil {
				t.Fatal(err)
			}
			err = svc.StartCoordinator(ctx, root.ID)
			runs, listErr := store.Runs().ListByWorkItem(ctx, root.ID)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if !tc.wantRun {
				var decisionErr *application.PlanDecisionError
				if !errors.As(err, &decisionErr) || decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied ||
					len(runs) != 0 || len(dispatcher.runs) != 0 {
					t.Fatalf("enforce must deny before Coordinator Run: err=%v runs=%d dispatched=%d", err, len(runs), len(dispatcher.runs))
				}
				state, stateErr := store.TaskCoordinators().GetState(ctx, root.ID)
				if stateErr != nil || state.BlockerCode != string(domain.GovernanceErrorPlanQuotaDenied) {
					t.Fatalf("quota denial must remain explainable in Coordinator state: state=%+v err=%v", state, stateErr)
				}
				return
			}
			if err != nil || len(runs) != 1 || len(dispatcher.runs) != 1 {
				t.Fatalf("audit must preserve Coordinator Run: err=%v runs=%d dispatched=%d", err, len(runs), len(dispatcher.runs))
			}
			admission, _ := runs[0].Input["quota_admission"].(map[string]any)
			if admission["quota_kind"] != string(domain.QuotaTurnCount) || admission["allowed"] != true ||
				admission["would_deny"] != true || admission["enforcement"] != string(domain.QuotaEnforcementAudit) {
				t.Fatalf("Coordinator Run quota audit snapshot mismatch: %#v", admission)
			}
		})
	}
}

func TestActiveWorkerQuotaGatesCoordinatorOwnedRetry(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "retry respects active worker quota", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"retry waits for a worker slot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal = setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaActiveWorker, Limit: 1, Enforcement: domain.QuotaEnforcementAudit,
	})
	source := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	decision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch retryable work","next_action":"wait for the worker","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": decision}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("precondition: expected one governed Worker, got %d", len(dispatcher.runs))
	}
	parent := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, parent.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunStatus(ctx, parent.ID, domain.RunFailed, map[string]any{
		"code": "transport_stream", "message": "retryable worker failure", "retryable": true,
	}); err != nil {
		t.Fatal(err)
	}
	parent, err = store.Runs().Get(ctx, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	governance, ok := parent.Input["governance"].(map[string]any)
	if !ok || governance["goal_id"] != goal.ID {
		t.Fatalf("precondition: governed Worker lost Turn identity: %#v", parent.Input["governance"])
	}
	now := time.Now().UTC()
	sibling := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID, ParentID: root.ID,
		RecordKind: domain.RecordKindTask, Title: "active sibling", Status: domain.WorkItemTodo,
		Priority: domain.PriorityMedium, AgentProfileID: workerID,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, sibling); err != nil {
		t.Fatal(err)
	}
	if err := store.Runs().Create(ctx, &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: wsID, WorkItemID: sibling.ID,
		AgentProfileID: workerID, Status: domain.RunQueued, Input: map[string]any{},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	goal, err = store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaActiveWorker, Limit: 1, Enforcement: domain.QuotaEnforcementEnforce,
	})
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if control, _ := state.Data["control_action"].(string); state.Status != domain.CoordinatorWaitingRetry ||
		control != "retry_worker" || state.CurrentRunID != "" {
		t.Fatalf("precondition: Worker failure did not create retry checkpoint: %+v", state)
	}
	if count, countErr := store.Quotas().ActiveWorkerCount(ctx, goal.ID); countErr != nil || count != 1 {
		t.Fatalf("precondition: sibling must occupy the only Worker slot: count=%d err=%v", count, countErr)
	}
	if decision, decisionErr := svc.ShouldRunLocked(ctx, application.ShouldRunRequest{
		GoalID: goal.ID, Kind: domain.QuotaActiveWorker, Amount: 1,
	}); decisionErr != nil || decision.Allowed || !decision.WouldDeny {
		t.Fatalf("precondition: enforce policy must deny another Worker: decision=%+v err=%v", decision, decisionErr)
	}
	expected := state.Version
	due := time.Now().UTC().Add(-time.Second)
	state.NextActionAt = &due
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	var decisionErr *application.PlanDecisionError
	if err := svc.StartCoordinator(ctx, root.ID); !errors.As(err, &decisionErr) ||
		decisionErr.Code != domain.GovernanceErrorPlanQuotaDenied {
		after, _ := store.TaskCoordinators().GetState(ctx, root.ID)
		childRuns, _ := store.Runs().ListByWorkItem(ctx, parent.WorkItemID)
		t.Fatalf("active_worker must deny retry before Run creation: err=%v state=%+v child_runs=%+v", err, after, childRuns)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("quota-denied retry was dispatched: %d", len(dispatcher.runs))
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil || state.Status != domain.CoordinatorWaitingRetry || state.CurrentRunID != "" {
		t.Fatalf("quota-denied retry must retain durable wait checkpoint: state=%+v err=%v", state, err)
	}
}

func TestUsageBackedQuotaDoesNotAppendPhase6BeforeTerminalSettlement(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "usage settlement waits", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"phase6 follows terminal canonical usage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	setGoalQuotaPolicies(t, ctx, store, goal, domain.QuotaPolicy{
		Kind: domain.QuotaOutputTokens, Limit: 100, Enforcement: domain.QuotaEnforcementAudit,
	})
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
	phases, err := store.TurnReceipts().ListPhases(ctx, *plan.GovernanceTurnKey)
	if err != nil || len(phases) != 5 {
		t.Fatalf("usage-backed Turn must stop before quota settlement: phases=%+v err=%v", phases, err)
	}
	if _, err := store.TurnReceipts().GetPhase(ctx, *plan.GovernanceTurnKey, 6); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("queued child Run must not get immutable placeholder phase6: %v", err)
	}
}
