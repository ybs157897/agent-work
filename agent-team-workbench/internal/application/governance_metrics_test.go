package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
)

func appendGovernanceMetricEvent(t *testing.T, ctx context.Context, store application.Store,
	workspaceID, eventType, aggregateType, aggregateID string, data map[string]any) {
	t.Helper()
	event, err := domain.NewCanonicalEvent(workspaceID, eventType, aggregateType, aggregateID, 1, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Events().Append(ctx, event, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGetGovernanceMetricsRecomputesCanonicalWorkspaceEvents(t *testing.T) {
	ctx, svc, store, _, workspaceID, workerID := seedCoordinatorEnv(t)
	_, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "metrics root", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"metrics are queryable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendGovernanceMetricEvent(t, ctx, store, workspaceID, domain.EventCoordinatorAttemptUpdated,
		domain.AggregateTaskCoordinator, "coord_metrics", map[string]any{
			"stage": "repair", "repair_attempt": 1, "plan_error_code": "plan_schema_validation",
		})
	appendGovernanceMetricEvent(t, ctx, store, workspaceID, domain.EventCoordinatorPlanUpdated,
		domain.AggregateTaskCoordinator, "coord_metrics", map[string]any{"stage": "decision"})
	appendGovernanceMetricEvent(t, ctx, store, workspaceID, domain.EventHandoffCreated,
		domain.AggregateHandoff, "handoff_metrics", nil)
	appendGovernanceMetricEvent(t, ctx, store, workspaceID, domain.EventGoalEvidenceAdded,
		domain.AggregateGoal, "goal_metrics", map[string]any{
			"source_kind": "validation_result", "source_id": "validation_metrics", "verification": "passed",
		})
	appendGovernanceMetricEvent(t, ctx, store, workspaceID, domain.EventValidationRecorded,
		domain.AggregateValidationResult, "validation_metrics", map[string]any{
			"validation_result_id": "validation_metrics", "status": "passed",
		})

	// Exercise receipt outcome producers through the Service. Synthetic event
	// rows cannot prove that the replay/conflict paths are wired to metrics.
	realRoot, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "real receipt metrics", AgentProfileID: workerID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"receipt outcomes are observable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	realGoal, err := store.Goals().GetByRootWorkItem(ctx, realRoot.ID)
	if err != nil {
		t.Fatal(err)
	}
	realTodo, err := store.Todos().Get(ctx, realGoal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimTodo(ctx, realTodo.ID, workerID, realTodo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	admitParams := application.AdmitTurnParams{
		GoalID: realGoal.ID, TodoID: claimed.ID, OwnerAgentID: workerID,
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: governanceDigest('a'), AdmissionClientKey: "metrics-real-admission",
	}
	header, err := svc.AdmitTurn(ctx, admitParams)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AdmitTurn(ctx, admitParams); err != nil {
		t.Fatalf("exact admission replay must succeed: %v", err)
	}
	conflictParams := admitParams
	conflictParams.InputSnapshotDigest = governanceDigest('b')
	if _, err := svc.AdmitTurn(ctx, conflictParams); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("changed admission replay must conflict: %v", err)
	}
	sourceRun, err := svc.CreateRun(ctx, realRoot.ID, application.CreateRunParams{
		AgentProfileID: workerID, Instruction: "metrics source run",
	})
	if err != nil {
		t.Fatal(err)
	}
	phase := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"decision": "execute", "source_run_id": sourceRun.ID},
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, phase); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 2, Phase: domain.TurnReceiptPhaseValidation,
		Payload: map[string]any{"valid": false, "error_code": "plan_schema_validation", "path": "/steps"},
	}); err != nil {
		t.Fatal(err)
	}

	// Real WorkItem block/unblock paths pin the format-specific metric and its
	// negative guarantee: a normal user blocker is not counted as a format fix.
	formatRoot, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "format unblock", AgentProfileID: workerID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"format unblock is counted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := svc.BlockWorkItem(ctx, formatRoot.ID, application.BlockParams{
		Code: "plan_json_syntax", Message: "invalid plan JSON", Source: "metrics",
	}, formatRoot.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UnblockWorkItem(ctx, blocked.ID, blocked.Version); err != nil {
		t.Fatal(err)
	}
	normalRoot, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "normal unblock", AgentProfileID: workerID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"normal unblock is not format"},
	})
	if err != nil {
		t.Fatal(err)
	}
	normalBlocked, err := svc.BlockWorkItem(ctx, normalRoot.ID, application.BlockParams{
		Code: "manual_block", Message: "manual pause", Source: "metrics",
	}, normalRoot.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UnblockWorkItem(ctx, normalBlocked.ID, normalBlocked.Version); err != nil {
		t.Fatal(err)
	}

	first, err := svc.GetGovernanceMetrics(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspaceID != workspaceID || first.PlanDecodeSuccess != 1 || first.ReceiptReplays != 1 ||
		first.ReceiptConflicts != 1 || first.RepairAttempts != 1 || first.RepairSuccesses != 1 ||
		first.UserUnblocks != 1 || first.Handoffs != 1 || first.EvidenceItems != 1 {
		t.Fatalf("application metrics query mismatch: %+v", first)
	}
	if first.PlanDecodeErrors[observability.ErrorFamilySchema] != 2 {
		t.Fatalf("real phase2 validation and repair must both classify as schema: %+v", first.PlanDecodeErrors)
	}
	if len(first.GoalSummaries) < 4 {
		t.Fatalf("metrics must include only real Goal summaries: %+v", first.GoalSummaries)
	}
	var realSummary *observability.GoalMetrics
	for i := range first.GoalSummaries {
		if first.GoalSummaries[i].GoalID == realGoal.ID {
			realSummary = &first.GoalSummaries[i]
		}
	}
	if realSummary == nil || realSummary.TurnCount != 1 {
		t.Fatalf("real Goal turn summary mismatch: %+v", first.GoalSummaries)
	}
	if realSummary.RunCount != 1 {
		t.Fatalf("real Goal Run summary must include phase source Run: %+v", realSummary)
	}
	if first.ProjectionDivergences != 0 {
		t.Fatalf("consistent root Goal must not report projection divergence: %+v", first)
	}

	second, err := svc.GetGovernanceMetrics(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceEventSeq != first.SourceEventSeq || second.PlanDecodeSuccess != first.PlanDecodeSuccess ||
		second.ReceiptReplays != first.ReceiptReplays || second.UserUnblocks != first.UserUnblocks {
		t.Fatalf("metrics recomputation must be stable: first=%+v second=%+v", first, second)
	}

	// Phase replay/conflict telemetry is a separate append-attempt outcome. It
	// must be durable too, but is intentionally checked after the 1/1 admission
	// metrics assertion above.
	phaseReplay := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"decision": "execute", "source_run_id": sourceRun.ID},
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, phaseReplay); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendTurnReceiptPhase(ctx, &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload: map[string]any{"decision": "different", "source_run_id": sourceRun.ID},
	}); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("changed phase replay must conflict: %v", err)
	}
	third, err := svc.GetGovernanceMetrics(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if third.ReceiptReplays != 2 || third.ReceiptConflicts != 2 {
		t.Fatalf("header and phase append-attempt outcomes must both be counted: %+v", third)
	}
}

func TestGetGovernanceMetricsRejectsTamperedReceiptFacts(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	claimed, err := svc.ClaimTodo(ctx, todo.ID, "agent_governance_owner", todo.Version, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	header, err := svc.AdmitTurn(ctx, application.AdmitTurnParams{
		GoalID: goal.ID, TodoID: claimed.ID, OwnerAgentID: "agent_governance_owner",
		ExpectedTodoVersion: claimed.Version, Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: governanceDigest('a'), AdmissionClientKey: "metrics-tamper",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Direct SQL can bypass the application digest calculator, but it cannot
	// bypass the receipt identity/sequence triggers. Metrics must still fail
	// closed before counting a forged phase payload.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO turn_receipt_phases
		(goal_id,todo_id,turn_seq,phase_seq,phase,payload,canonical_digest,plan_id,run_ids,quota_reservation_keys,evidence,created_at)
		VALUES (?,?,?,?,?,?,?,NULL,'[]','[]','[]',?)`,
		goal.ID, claimed.ID, header.TurnKey.TurnSeq, 1, string(domain.TurnReceiptPhaseDecisionDecode),
		`{"tampered":true}`, governanceDigest('b'), now); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetGovernanceMetrics(ctx, workspaceID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("tampered receipt digest must fail closed, got %v", err)
	}
	_ = store
}
