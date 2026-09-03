package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	workbenchcontracts "github.com/ybs/agent-team-workbench/contracts"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func governancePlanDigest(seed string) string {
	return "sha256:" + strings.Repeat(seed, 64)
}

func seedSubmitPlanGovernanceLineage(t *testing.T, ctx context.Context, svc *application.Service,
	store application.Store, root *domain.WorkItem, source *domain.ExecutionRun, decisionDigest string) *application.PlanGovernanceInput {
	t.Helper()
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
		InputSnapshotDigest: inputDigest, AdmissionClientKey: "submit-plan-lineage:" + source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	phase := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{
			"decision_digest": decisionDigest, "schema_version": "plan-decision/v2",
			"schema_digest": workbenchcontracts.PlanDecisionV2SchemaDigest(), "source_run_id": source.ID,
		},
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, phase); err != nil {
		t.Fatal(err)
	}
	return &application.PlanGovernanceInput{
		ClientKey:      "governance:" + goal.ID + ":" + todo.ID + ":" + fmt.Sprint(header.TurnKey.TurnSeq),
		TurnKey:        header.TurnKey,
		SchemaVersion:  "plan-decision/v2",
		SchemaDigest:   workbenchcontracts.PlanDecisionV2SchemaDigest(),
		DecisionDigest: decisionDigest,
	}
}

func TestSubmitPlanGovernanceIdentityReplayDoesNotDuplicatePlanOrDispatch(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governance plan identity", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"Plan identity is replay safe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	governance := seedSubmitPlanGovernanceLineage(t, ctx, svc, store, root, source, governancePlanDigest("b"))
	turnKey := governance.TurnKey
	params := application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: source.AgentProfileID, SourceRunID: source.ID,
		Steps: []application.PlanStepInput{{
			Verb: "defer", Payload: map[string]any{"wake_at": "2099-01-01T00:00:00Z"},
		}},
		DecisionAudit: &application.PlanDecisionAuditInput{
			SchemaVersion: "plan-decision/v2", Candidate: application.PlanCandidateNativeText,
			Reason: "persist governance identity", NextAction: "wait", StepCount: 1,
		},
		Governance: governance,
	}
	first, err := svc.SubmitPlan(ctx, wsID, params)
	if err != nil {
		t.Fatal(err)
	}
	if first.ClientKey != governance.ClientKey || first.GovernanceTurnKey == nil ||
		!first.GovernanceTurnKey.Equal(turnKey) || first.DecisionSchemaDigest != governance.SchemaDigest ||
		first.DecisionDigest != governance.DecisionDigest {
		t.Fatalf("governance Plan identity was not persisted: %+v", first)
	}

	replayed, err := svc.SubmitPlan(ctx, wsID, params)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != first.ID {
		t.Fatalf("same governance intent must return the existing Plan: first=%s replay=%s", first.ID, replayed.ID)
	}
	byKey, err := store.Plans().GetByClientKey(ctx, wsID, governance.ClientKey)
	if err != nil || byKey == nil || byKey.ID != first.ID {
		t.Fatalf("governance Plan client key lookup mismatch: plan=%+v err=%v", byKey, err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("defer Plan replay must not create or dispatch another Run: %d", len(dispatcher.runs))
	}

	conflict := params
	conflictGovernance := *governance
	conflictGovernance.DecisionDigest = governancePlanDigest("c")
	conflict.Governance = &conflictGovernance
	if plan, err := svc.SubmitPlan(ctx, wsID, conflict); !errors.Is(err, domain.ErrIdempotencyConflict) || plan != nil {
		t.Fatalf("same client key with a different decision digest must conflict: plan=%+v err=%v", plan, err)
	}
	wrongKey := params
	wrongKeyGovernance := *governance
	wrongKeyGovernance.ClientKey = "governance:wrong"
	wrongKey.Governance = &wrongKeyGovernance
	if plan, err := svc.SubmitPlan(ctx, wsID, wrongKey); !errors.Is(err, domain.ErrValidation) || plan != nil {
		t.Fatalf("governance client key must be derived from TurnKey: plan=%+v err=%v", plan, err)
	}
}

func TestSubmitPlanRejectsPartialGovernanceIdentityBeforeWrites(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "partial governance identity", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	params := application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: dispatcher.runs[0].AgentProfileID,
		SourceRunID: dispatcher.runs[0].ID,
		Steps:       []application.PlanStepInput{{Verb: "finish", Payload: map[string]any{}}},
		Governance:  &application.PlanGovernanceInput{ClientKey: "partial"},
	}
	if plan, err := svc.SubmitPlan(ctx, wsID, params); !errors.Is(err, domain.ErrValidation) || plan != nil {
		t.Fatalf("partial governance identity must fail before Plan creation: plan=%+v err=%v", plan, err)
	}
	if existing, err := store.Plans().LatestByWorkItem(context.Background(), root.ID); err != nil || existing != nil {
		t.Fatalf("partial governance identity left a Plan: plan=%+v err=%v", existing, err)
	}
}

func TestSubmitPlanPropagatesGovernanceIdentityToWorkerRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "governance worker input", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"Worker input retains the admitted turn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	governance := seedSubmitPlanGovernanceLineage(t, ctx, svc, store, root, source, governancePlanDigest("d"))
	turnKey := governance.TurnKey
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: source.AgentProfileID, SourceRunID: source.ID,
		Steps: []application.PlanStepInput{
			{Verb: "dispatch", Payload: map[string]any{
				"agent_id": workerID, "title": "bounded work", "instruction": "do the bounded work",
				"acceptance": []any{"done"},
			}},
			{Verb: "join", Payload: map[string]any{"children": "all"}},
		},
		DecisionAudit: &application.PlanDecisionAuditInput{
			SchemaVersion: "plan-decision/v2", Candidate: application.PlanCandidateNativeText,
			Reason: "dispatch one scoped worker", NextAction: "wait", StepCount: 2,
		},
		Governance: governance,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("governance dispatch must create exactly one Worker Run: %d", len(dispatcher.runs))
	}
	worker := dispatcher.runs[1]
	contextData, _ := worker.Input["governance"].(map[string]any)
	if contextData["plan_id"] != plan.ID || contextData["goal_id"] != turnKey.GoalID || contextData["todo_id"] != turnKey.TodoID ||
		contextData["turn_seq"] != turnKey.TurnSeq || contextData["plan_client_key"] != governance.ClientKey ||
		contextData["decision_schema_version"] != governance.SchemaVersion ||
		contextData["decision_schema_digest"] != governance.SchemaDigest ||
		contextData["decision_digest"] != governance.DecisionDigest {
		t.Fatalf("Worker Run governance input mismatch: %#v", contextData)
	}
	if plan.Steps[0].ResultRunID != worker.ID {
		t.Fatalf("Plan dispatch result did not reference the governed Worker Run: plan=%+v worker=%+v", plan, worker)
	}
}

func TestSubmitPlanRejectsGovernanceLineageFromAnotherRoot(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	rootA, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "lineage root A", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"root A lineage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootB, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "lineage root B", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"root B lineage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("precondition: expected one Coordinator source per root, got %d", len(dispatcher.runs))
	}
	sourceA, sourceB := dispatcher.runs[0], dispatcher.runs[1]
	foreign := seedSubmitPlanGovernanceLineage(t, ctx, svc, store, rootB, sourceB, governancePlanDigest("f"))
	params := application.SubmitPlanParams{
		WorkItemID: rootA.ID, AgentProfileID: sourceA.AgentProfileID, SourceRunID: sourceA.ID,
		Steps: []application.PlanStepInput{{Verb: "finish", Payload: map[string]any{}}},
		DecisionAudit: &application.PlanDecisionAuditInput{
			SchemaVersion: "plan-decision/v2", Candidate: application.PlanCandidateNativeText,
			Reason: "must reject foreign lineage", NextAction: "none", StepCount: 1,
		},
		Governance: foreign,
	}
	if plan, err := svc.SubmitPlan(ctx, wsID, params); !errors.Is(err, domain.ErrWorkspaceContextMismatch) || plan != nil {
		t.Fatalf("root A must reject root B governance lineage: plan=%+v err=%v", plan, err)
	}
	if plan, err := store.Plans().LatestByWorkItem(ctx, rootA.ID); err != nil || plan != nil {
		t.Fatalf("foreign governance lineage left a Plan on root A: plan=%+v err=%v", plan, err)
	}
}
