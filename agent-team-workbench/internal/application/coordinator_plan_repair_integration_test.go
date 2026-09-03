package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	workbenchcontracts "github.com/ybs/agent-team-workbench/contracts"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

type failPlanWorkerDispatcher struct {
	runs []*domain.ExecutionRun
}

type failEvaluationDispatcher struct {
	runs []*domain.ExecutionRun
}

func (d *failEvaluationDispatcher) Dispatch(_ context.Context, run *domain.ExecutionRun) error {
	d.runs = append(d.runs, run)
	if evaluation, _ := run.Input["evaluation"].(bool); evaluation {
		return errors.New("evaluation dispatch unavailable")
	}
	return nil
}

func (d *failPlanWorkerDispatcher) Dispatch(_ context.Context, run *domain.ExecutionRun) error {
	d.runs = append(d.runs, run)
	control, _ := run.Input["task_coordinator"].(map[string]any)
	if control["role"] == "worker" {
		return errors.New("worker dispatch unavailable")
	}
	return nil
}

func startCoordinatorRunForPlanDecision(t *testing.T, ctx context.Context, svc *application.Service, runID string) {
	t.Helper()
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func completeCoordinatorPlanDecision(t *testing.T, ctx context.Context, svc *application.Service, runID, text string) {
	t.Helper()
	startCoordinatorRunForPlanDecision(t, ctx, svc, runID)
	if err := svc.RecordRunEvent(ctx, runID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": text}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func requireNoPlanForWorkItem(t *testing.T, ctx context.Context, store *sqlstore.Store, workItemID string) {
	t.Helper()
	plan, err := store.Plans().LatestByWorkItem(ctx, workItemID)
	if plan != nil {
		t.Fatalf("invalid decision must not create a Plan: plan=%+v err=%v", plan, err)
	}
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unexpected latest Plan error: %v", err)
	}
}

func TestCoordinatorPlanRepairPersistsCheckpointAndCreatesExactlyOnePlan(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "PlanDecision repair", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := dispatcher.runs[0]
	originalDecision, _ := original.Input["control_decision"].(map[string]any)
	if originalDecision["schema_version"] != "plan-decision/v2" ||
		originalDecision["schema_digest"] != workbenchcontracts.PlanDecisionV2SchemaDigest() ||
		originalDecision["transport_mode"] != "text_decoder" || originalDecision["repair_attempt"] != 0 {
		t.Fatalf("initial Coordinator Run lost the canonical decision contract: %#v", originalDecision)
	}
	startCoordinatorRunForPlanDecision(t, ctx, svc, original.ID)
	if err := svc.RecordRunSessionRef(ctx, original.ID, "provider-session-plan-repair"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, original.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": `{"schema_version":`}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, original.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("one malformed decision must create exactly one repair Run: runs=%d", len(dispatcher.runs))
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	controlHeaders, err := store.TurnReceipts().ListHeadersByGoal(ctx, goal.ID)
	if err != nil || len(controlHeaders) != 1 {
		t.Fatalf("malformed decision must leave one canonical repair control receipt: headers=%d err=%v", len(controlHeaders), err)
	}
	controlPhases, err := store.TurnReceipts().ListPhases(ctx, controlHeaders[0].TurnKey)
	if err != nil || len(controlPhases) != 7 {
		t.Fatalf("repair control receipt must be complete through phase7: phases=%d err=%v", len(controlPhases), err)
	}
	controlDecision, _ := controlPhases[0].Payload["turn_decision"].(map[string]any)
	if controlDecision["decision"] != string(domain.TurnDecisionRepair) {
		t.Fatalf("repair control receipt must carry repair decision: %#v", controlPhases[0].Payload)
	}
	if controlPhases[1].Payload["valid"] != false || controlPhases[1].Payload["error_code"] != string(domain.GovernanceErrorPlanJSONSyntax) {
		t.Fatalf("repair control receipt must carry invalid validation outcome: %#v", controlPhases[1].Payload)
	}
	repair := dispatcher.runs[1]
	control, _ := repair.Input["task_coordinator"].(map[string]any)
	if control["action"] != "repair_plan" {
		t.Fatalf("repair Run lost control action: %#v", control)
	}
	repairDecision, _ := repair.Input["control_decision"].(map[string]any)
	if repairDecision["schema_version"] != originalDecision["schema_version"] ||
		repairDecision["schema_digest"] != originalDecision["schema_digest"] ||
		repairDecision["transport_mode"] != "text_decoder" || repairDecision["repair_attempt"] != 1 {
		t.Fatalf("repair Run decision snapshot mismatch: initial=%#v repair=%#v", originalDecision, repairDecision)
	}
	if repair.RuntimeLabel != original.RuntimeLabel || repair.SessionBefore != "provider-session-plan-repair" {
		t.Fatalf("repair must retain source runtime/session: original=%+v repair=%+v", original, repair)
	}
	conversation, _ := repair.Input["conversation"].(map[string]any)
	if conversation["resume_session_ref"] != "provider-session-plan-repair" {
		t.Fatalf("repair must resume the same provider session: %#v", conversation)
	}
	originalConversation, _ := original.Input["conversation"].(map[string]any)
	if conversation["config_digest"] != originalConversation["config_digest"] {
		t.Fatalf("repair attempt metadata must not rotate the session contract: original=%#v repair=%#v",
			originalConversation, conversation)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairPending || state.RepairAttempt != 1 ||
		state.RepairSourceRunID != original.ID || state.CurrentRunID != repair.ID {
		t.Fatalf("repair checkpoint mismatch: %+v", state)
	}
	requireNoPlanForWorkItem(t, ctx, store, root.ID)

	valid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"wait deterministically","next_action":"wake at the persisted deadline","steps":[{"verb":"defer","wake_at":"2099-01-01T00:00:00Z"}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, repair.ID, valid)
	plan, err := store.Plans().LatestByWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceRunID != repair.ID {
		t.Fatalf("repair success must create one Plan owned by the repair Run: %+v", plan)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 ||
		state.RepairSourceRunID != "" || len(state.RepairValidationErrors) != 0 {
		t.Fatalf("successful repair must clear the durable checkpoint: %+v", state)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("valid defer repair must not create another execution Run: runs=%d", len(dispatcher.runs))
	}
}

func TestCoordinatorPlanRepairDispatchClearsCheckpoint(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "PlanDecision dispatch repair", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, `{}`)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("format failure must create repair attempt 1: runs=%d", len(dispatcher.runs))
	}
	repair := dispatcher.runs[1]
	valid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch bounded work","next_action":"wait for the worker","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, repair.ID, valid)
	if len(dispatcher.runs) != 3 {
		t.Fatalf("valid repaired dispatch must create exactly one Worker Run: runs=%d", len(dispatcher.runs))
	}
	worker := dispatcher.runs[2]
	if worker.AgentProfileID != workerID || worker.WorkItemID == root.ID {
		t.Fatalf("repaired Plan created the wrong Worker Run: %+v", worker)
	}
	plan, err := store.Plans().LatestByWorkItem(ctx, root.ID)
	if err != nil || plan == nil || plan.SourceRunID != repair.ID {
		t.Fatalf("repaired dispatch must create exactly one Plan: plan=%+v err=%v", plan, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 ||
		state.RepairSourceRunID != "" || len(state.RepairValidationErrors) != 0 {
		t.Fatalf("repaired dispatch must clear the durable checkpoint: %+v", state)
	}
	for _, key := range []string{"repair_of_run_id", "repair_origin_run_id", "repair_attempt", "repair_error_code", "repair_error_path"} {
		if _, exists := state.Data[key]; exists {
			t.Fatalf("repaired dispatch retained %s: %#v", key, state.Data)
		}
	}
}

func TestCoordinatorPlanRepairPostCommitWorkerDispatchFailureIsNotPlanSemanticFailure(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	dispatcher := &failPlanWorkerDispatcher{}
	svc.SetDispatcher(dispatcher)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "PlanDecision post-commit failure", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, `{}`)
	repair := dispatcher.runs[1]
	valid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch bounded work","next_action":"recover worker transport","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, repair.ID, valid)
	if len(dispatcher.runs) != 3 {
		t.Fatalf("repair must commit one Worker Run before its dispatch failure: runs=%d", len(dispatcher.runs))
	}
	worker, err := store.Runs().Get(ctx, dispatcher.runs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Status != domain.RunFailed || worker.Failure == nil || worker.Failure.Code != "dispatch_failed" {
		t.Fatalf("post-commit Worker failure did not enter the Run state machine: %+v", worker)
	}
	plan, err := store.Plans().GetBySourceRun(ctx, repair.ID)
	if err != nil || plan == nil {
		t.Fatalf("post-commit dispatch failure must retain the canonical Plan: plan=%+v err=%v", plan, err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 {
		t.Fatalf("post-commit dispatch failure must not retain format repair state: %+v", state)
	}
	if state.BlockerCode == string(domain.GovernanceErrorPlanSemanticValidation) ||
		state.BlockerCode == string(domain.GovernanceErrorPlanAuthorityDenied) ||
		state.BlockerCode == string(domain.GovernanceErrorPlanQuotaDenied) {
		t.Fatalf("post-commit runtime failure was misclassified as PlanDecision failure: %+v", state)
	}
}

func TestCoordinatorPlanRepairFinishClearsCheckpointAndDelivers(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "PlanDecision finish repair", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, `{}`)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("format failure must create repair attempt 1: runs=%d", len(dispatcher.runs))
	}
	repair := dispatcher.runs[1]
	valid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"all evidence is complete","next_action":"evaluate before user acceptance","steps":[{"verb":"finish","evaluation":true}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, repair.ID, valid)
	if len(dispatcher.runs) != 3 {
		t.Fatalf("finish repair must create exactly one evaluation Run: runs=%d", len(dispatcher.runs))
	}
	plan, err := store.Plans().LatestByWorkItem(ctx, root.ID)
	if err != nil || plan == nil || plan.SourceRunID != repair.ID || plan.Status != domain.PlanFinished {
		t.Fatalf("finish repair must persist a finished Plan: plan=%+v err=%v", plan, err)
	}
	evaluation := dispatcher.runs[2]
	if err := svc.RecordRunStatus(ctx, evaluation.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, evaluation.ID,
		"评估通过。\n```verdict\n{\"pass\":true,\"reasons\":[\"验收标准已满足\"]}\n```"); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorWaitingUser || state.Phase != "acceptance" ||
		state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 {
		t.Fatalf("finish repair must clear repair and enter user acceptance: %+v", state)
	}
}

func TestCoordinatorPlanDecisionEvaluationDispatchFailureDoesNotDeliver(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	dispatcher := &failEvaluationDispatcher{}
	svc.SetDispatcher(dispatcher)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "evaluation dispatch failure", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"evaluate before delivery","next_action":"wait for evaluation","steps":[{"verb":"finish","evaluation":true}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, decision)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("finish evaluation must create one evaluation Run: runs=%d", len(dispatcher.runs))
	}
	evaluation, err := store.Runs().Get(ctx, dispatcher.runs[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Status != domain.RunFailed || evaluation.Failure == nil || evaluation.Failure.Code != "dispatch_failed" {
		t.Fatalf("evaluation dispatch failure did not enter the Run state machine: %+v", evaluation)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorWaitingRetry || state.Phase != "recovering" ||
		state.CurrentRunID != "" || state.NextActionAt == nil || coordinatorControlActionForTest(state) != "recover" {
		t.Fatalf("failed evaluation must enter bounded Coordinator recovery, not delivery: %+v", state)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Phase == domain.PhaseReview || state.Status == domain.CoordinatorWaitingUser {
		t.Fatalf("failed evaluation must not expose user acceptance: root=%+v state=%+v", root, state)
	}
}

func TestCoordinatorPlanRepairExhaustsAfterTwoInvalidRepairTurns(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "PlanDecision repair exhausted", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid := `{}`
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, invalid)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("original failure must create repair attempt 1: runs=%d", len(dispatcher.runs))
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[1].ID, invalid)
	if len(dispatcher.runs) != 3 {
		t.Fatalf("first invalid repair must create repair attempt 2: runs=%d", len(dispatcher.runs))
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[2].ID, invalid)
	if len(dispatcher.runs) != 3 {
		t.Fatalf("second invalid repair must not create a third repair Run: runs=%d", len(dispatcher.runs))
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || state.RepairStatus != domain.CoordinatorRepairExhausted ||
		state.RepairAttempt != 2 || state.BlockerCode != "coordinator_plan_repair_exhausted" || state.CurrentRunID != "" {
		t.Fatalf("repair exhaustion checkpoint mismatch: %+v", state)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != domain.WorkItemBlocked {
		t.Fatalf("repair exhaustion must block the root Task: %+v", root)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != domain.GoalBlocked || goal.Phase != "blocked" ||
		todo.Status != domain.TodoBlocked || todo.Claim != nil {
		t.Fatalf("repair exhaustion must block every governance projection and release its claim: goal=%+v todo=%+v", goal, todo)
	}
	requireNoPlanForWorkItem(t, ctx, store, root.ID)
}

func TestCoordinatorPlanRepairBareArrayEntersRepairWithoutPlan(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "bare array repair", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, `[{"verb":"finish"}]`)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("bare legacy array must create repair attempt 1: runs=%d", len(dispatcher.runs))
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairPending || state.RepairAttempt != 1 ||
		state.RepairErrorCode != string(domain.GovernanceErrorPlanSchemaValidation) {
		t.Fatalf("bare array must be classified as schema repair: %+v", state)
	}
	requireNoPlanForWorkItem(t, ctx, store, root.ID)
}

func TestCoordinatorPlanRepairMissingInitialCandidateEntersRepair(t *testing.T) {
	for name, text := range map[string]string{
		"empty":         "",
		"plain prose":   "I will handle this without a control decision",
		"unknown fence": "```yaml\nsteps: []\n```",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
			root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
				Title: "missing candidate " + name, RecordKind: domain.RecordKindTask, AutoCoordinate: true,
				AcceptanceCriteria: []string{"test task acceptance"},
			})
			if err != nil {
				t.Fatal(err)
			}
			completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, text)
			if len(dispatcher.runs) != 2 {
				t.Fatalf("missing initial candidate must create repair attempt 1: runs=%d", len(dispatcher.runs))
			}
			state, err := store.TaskCoordinators().GetState(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.RepairStatus != domain.CoordinatorRepairPending || state.RepairAttempt != 1 ||
				state.RepairErrorCode != string(domain.GovernanceErrorPlanJSONSyntax) {
				t.Fatalf("missing candidate did not enter syntax repair: %+v", state)
			}
			requireNoPlanForWorkItem(t, ctx, store, root.ID)
		})
	}
}

func TestCoordinatorPlanRepairUserUnblockStartsNewRepairBudget(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "repair budget reset", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid := `{}`
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, invalid)
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[1].ID, invalid)
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[2].ID, invalid)
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairExhausted || state.RepairAttempt != 2 {
		t.Fatalf("precondition: repair cycle was not exhausted: %+v", state)
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
		t.Fatalf("exhausted repair cycle must be blocked consistently before unblock: goal=%+v todo=%+v", goal, todo)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UnblockWorkItem(ctx, root.ID, root.Version); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 4 {
		t.Fatalf("unblock must start exactly one new Coordinator turn: runs=%d", len(dispatcher.runs))
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 ||
		state.CurrentRunID != dispatcher.runs[3].ID {
		t.Fatalf("unblock must clear the exhausted repair checkpoint before restart: %+v", state)
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
		t.Fatalf("unblock must resume clean governance intent without reusing a claim: goal=%+v todo=%+v", goal, todo)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[3].ID, invalid)
	if len(dispatcher.runs) != 5 {
		t.Fatalf("first post-unblock format failure must create repair attempt 1: runs=%d", len(dispatcher.runs))
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairPending || state.RepairAttempt != 1 {
		t.Fatalf("post-unblock repair budget did not restart at attempt 1: %+v", state)
	}
}

func TestCoordinatorPlanRepairRuntimeFailurePreservesFormatAttempt(t *testing.T) {
	for _, terminal := range []domain.RunStatus{domain.RunFailed, domain.RunLost} {
		t.Run(string(terminal), func(t *testing.T) {
			ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
			root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
				Title: "repair runtime " + string(terminal), RecordKind: domain.RecordKindTask, AutoCoordinate: true,
				AcceptanceCriteria: []string{"test task acceptance"},
			})
			if err != nil {
				t.Fatal(err)
			}
			completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, `{}`)
			if len(dispatcher.runs) != 2 {
				t.Fatalf("format failure must create repair attempt 1: runs=%d", len(dispatcher.runs))
			}
			repair := dispatcher.runs[1]
			startCoordinatorRunForPlanDecision(t, ctx, svc, repair.ID)
			if terminal == domain.RunLost {
				if err := svc.RecordRunStatus(ctx, repair.ID, domain.RunReconnecting, nil); err != nil {
					t.Fatal(err)
				}
				if err := svc.RecordRunStatus(ctx, repair.ID, domain.RunLost, nil); err != nil {
					t.Fatal(err)
				}
			} else if err := svc.RecordRunStatus(ctx, repair.ID, domain.RunFailed, map[string]any{
				"code": "transport_stream", "message": "repair transport failed", "retryable": true,
			}); err != nil {
				t.Fatal(err)
			}
			state, err := store.TaskCoordinators().GetState(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Status != domain.CoordinatorWaitingRetry || state.RepairStatus != domain.CoordinatorRepairPending ||
				state.RepairAttempt != 1 || coordinatorControlActionForTest(state) != "repair_plan" {
				t.Fatalf("runtime failure must preserve the pending format attempt: %+v", state)
			}
			if terminal == domain.RunLost {
				if resumed, err := svc.ResumeRun(ctx, repair.ID); !errors.Is(err, domain.ErrStateConflict) || resumed != nil {
					t.Fatalf("generic ResumeRun must not bypass the Coordinator control line: resumed=%+v err=%v", resumed, err)
				}
				if len(dispatcher.runs) != 2 {
					t.Fatalf("rejected generic ResumeRun created an orphan Run: runs=%d", len(dispatcher.runs))
				}
			}
			requireNoPlanForWorkItem(t, ctx, store, root.ID)
			forceCoordinatorDue(t, ctx, svc, store, root.ID)
			if len(dispatcher.runs) != 3 {
				t.Fatalf("runtime recovery must create one replacement repair Run: runs=%d", len(dispatcher.runs))
			}
			retry := dispatcher.runs[2]
			control, _ := retry.Input["task_coordinator"].(map[string]any)
			decision, _ := retry.Input["control_decision"].(map[string]any)
			if control["action"] != "repair_plan" || decision["repair_attempt"] != 1 {
				t.Fatalf("runtime recovery was misclassified as another format attempt: control=%#v decision=%#v", control, decision)
			}
			valid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"runtime recovered","next_action":"wait","steps":[{"verb":"defer","wake_at":"2099-01-01T00:00:00Z"}]}`
			completeCoordinatorPlanDecision(t, ctx, svc, retry.ID, valid)
			plan, err := store.Plans().LatestByWorkItem(ctx, root.ID)
			if err != nil || plan == nil || plan.SourceRunID != retry.ID {
				t.Fatalf("recovered repair must create exactly one Plan: plan=%+v err=%v", plan, err)
			}
			state, err = store.TaskCoordinators().GetState(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 {
				t.Fatalf("successful runtime recovery must clear repair checkpoint: %+v", state)
			}
		})
	}
}

func TestCoordinatorPlanRepairRuntimeRetryUsesSourceRuntimeAfterConfigChange(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "repair source runtime", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, `{}`)
	repair := dispatcher.runs[1]
	startCoordinatorRunForPlanDecision(t, ctx, svc, repair.ID)
	if err := svc.RecordRunStatus(ctx, repair.ID, domain.RunFailed, map[string]any{
		"code": "transport_stream", "message": "repair transport failed", "retryable": true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_coordinator_kimi", WorkspaceID: wsID, RuntimeLabel: "kimi_local", AdapterID: "kimi",
		Provider: "kimi", Model: "kimi", Status: domain.BindingReady,
		Capabilities: map[string]string{"resume": string(atwruntime.CapSupported)},
		Version:      1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().GetConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeLabel = "kimi_local"
	if err := store.TaskCoordinators().UpdateConfig(ctx, config, config.Version); err != nil {
		t.Fatal(err)
	}
	forceCoordinatorDue(t, ctx, svc, store, root.ID)
	if len(dispatcher.runs) != 3 {
		t.Fatalf("runtime retry must create one replacement repair Run: runs=%d", len(dispatcher.runs))
	}
	retry := dispatcher.runs[2]
	if retry.RuntimeLabel != repair.RuntimeLabel || retry.RuntimeLabel != "mock" {
		t.Fatalf("repair retry must remain pinned to the source runtime: source=%s retry=%s", repair.RuntimeLabel, retry.RuntimeLabel)
	}
	control, _ := retry.Input["task_coordinator"].(map[string]any)
	if control["action"] != "repair_plan" || control["use_fallback"] != false {
		t.Fatalf("repair retry lost its pinned control envelope: %#v", control)
	}
}

func TestCoordinatorPlanDecisionSemanticAndAuthorityFailuresDoNotRepair(t *testing.T) {
	tests := []struct {
		name        string
		decision    func(workerID string) string
		blockerCode string
	}{
		{
			name: "semantic",
			decision: func(workerID string) string {
				return `{"schema_version":"plan-decision/v2","kind":"plan","reason":"missing wait","next_action":"invalid","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"work","instruction":"do work","acceptance":["done"]}]}`
			},
			blockerCode: string(domain.GovernanceErrorPlanSemanticValidation),
		},
		{
			name: "authority",
			decision: func(string) string {
				return `{"schema_version":"plan-decision/v2","kind":"plan","reason":"unknown worker","next_action":"invalid","steps":[{"verb":"dispatch","agent_id":"agent_missing_worker","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
			},
			blockerCode: string(domain.GovernanceErrorPlanAuthorityDenied),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
			root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
				Title: tc.name, RecordKind: domain.RecordKindTask, AutoCoordinate: true,
				AcceptanceCriteria: []string{"test task acceptance"},
			})
			if err != nil {
				t.Fatal(err)
			}
			completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, tc.decision(workerID))
			if len(dispatcher.runs) != 1 {
				t.Fatalf("non-format failure must not consume repair budget: runs=%d", len(dispatcher.runs))
			}
			state, err := store.TaskCoordinators().GetState(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Status != domain.CoordinatorBlocked || state.BlockerCode != tc.blockerCode ||
				state.RepairStatus != domain.CoordinatorRepairNone || state.RepairAttempt != 0 {
				t.Fatalf("non-format failure classification mismatch: %+v", state)
			}
			requireNoPlanForWorkItem(t, ctx, store, root.ID)
		})
	}
}

func TestCoordinatorPlanRepairRestartReplayCreatesOneRepairRun(t *testing.T) {
	ctx, svc, store, firstDispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "repair replay", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := firstDispatcher.runs[0]
	startCoordinatorRunForPlanDecision(t, ctx, svc, original.ID)
	if err := svc.RecordRunEvent(ctx, original.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": `{"schema_version":`}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, original.ID, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Runs().Get(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := stored.Version
	if err := stored.Transition(domain.RunSucceeded, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Runs().Update(ctx, stored, expected); err != nil {
		t.Fatal(err)
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
	for err := range errs {
		if err != nil {
			t.Fatalf("replayed StartCoordinator failed: %v", err)
		}
	}
	if len(restartDispatcher.runs) != 1 {
		t.Fatalf("concurrent terminal replay must create exactly one repair Run: %d", len(restartDispatcher.runs))
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.RepairStatus != domain.CoordinatorRepairPending || state.RepairAttempt != 1 ||
		state.RepairSourceRunID != original.ID || state.CurrentRunID != restartDispatcher.runs[0].ID {
		t.Fatalf("restart must retain one durable repair checkpoint: %+v", state)
	}
	requireNoPlanForWorkItem(t, ctx, store, root.ID)
}

func TestCoordinatorPlanRepairRejectsCrossRootSource(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	first, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "repair target", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "foreign source", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("precondition: expected two root Coordinator Runs, got %d", len(dispatcher.runs))
	}
	foreign := dispatcher.runs[1]
	if foreign.WorkItemID != second.ID {
		t.Fatalf("foreign source mismatch: %+v", foreign)
	}
	state, err := store.TaskCoordinators().GetState(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorQueued
	state.Phase = "repair"
	state.CurrentAction = "repair_plan"
	state.CurrentRunID = ""
	state.RepairStatus = domain.CoordinatorRepairPending
	state.RepairAttempt = 1
	state.RepairSourceRunID = foreign.ID
	state.RepairErrorClass = domain.CoordinatorRepairErrorSyntax
	state.RepairErrorCode = string(domain.GovernanceErrorPlanJSONSyntax)
	state.RepairValidationErrors = []domain.GovernanceValidationError{{
		Code: domain.GovernanceErrorPlanJSONSyntax, Path: "/", Message: "invalid json",
	}}
	state.Data = map[string]any{
		"control_action": "repair_plan", "repair_of_run_id": foreign.ID,
		"repair_origin_run_id": foreign.ID, "repair_attempt": 1,
	}
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartCoordinator(ctx, first.ID); !errors.Is(err, domain.ErrWorkspaceContextMismatch) {
		t.Fatalf("cross-root repair source must fail closed: %v", err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("cross-root repair source created an unauthorized Run: %d", len(dispatcher.runs))
	}
	state, err = store.TaskCoordinators().GetState(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || state.BlockerCode != "coordinator_start_failed" {
		t.Fatalf("cross-root repair source must leave an explainable blocker: %+v", state)
	}
}

func TestCoordinatorPlanDecisionRestartReplayCreatesOnePlanAndAudit(t *testing.T) {
	ctx, svc, store, firstDispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "valid decision replay", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := firstDispatcher.runs[0]
	startCoordinatorRunForPlanDecision(t, ctx, svc, original.ID)
	valid := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"persist once","next_action":"wait","steps":[{"verb":"defer","wake_at":"2099-01-01T00:00:00Z"}]}`
	if err := svc.RecordRunEvent(ctx, original.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": valid}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, original.ID, domain.RunSucceeding, nil); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Runs().Get(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := stored.Version
	if err := stored.Transition(domain.RunSucceeded, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.Runs().Update(ctx, stored, expected); err != nil {
		t.Fatal(err)
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
	for err := range errs {
		if err != nil {
			t.Fatalf("valid terminal replay failed: %v", err)
		}
	}
	if len(restartDispatcher.runs) != 0 {
		t.Fatalf("defer decision replay must not create an execution Run: %d", len(restartDispatcher.runs))
	}
	plan, err := store.Plans().GetBySourceRun(ctx, original.ID)
	if err != nil || plan == nil || plan.SourceRunID != original.ID {
		t.Fatalf("terminal replay must retain exactly one source-owned Plan: plan=%+v err=%v", plan, err)
	}
	events, err := store.TaskCoordinators().ListEvents(ctx, root.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	decisionEvents := 0
	for _, event := range events {
		if event.Kind == domain.EventCoordinatorPlanUpdated && event.RunID == original.ID && event.Data["stage"] == "decision" {
			decisionEvents++
		}
	}
	if decisionEvents != 1 {
		t.Fatalf("Plan and decision audit must commit exactly once: events=%d timeline=%+v", decisionEvents, events)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status == domain.CoordinatorBlocked || state.CurrentRunID != "" {
		t.Fatalf("terminal replay must finish the Coordinator projection: %+v", state)
	}
}
