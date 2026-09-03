package observability

import (
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func metricEvent(seq int64, eventType, aggregateType, aggregateID string, data map[string]any) *domain.CanonicalEvent {
	return &domain.CanonicalEvent{
		StreamSeq: seq, Type: eventType, AggregateType: aggregateType,
		AggregateID: aggregateID, Data: data,
	}
}

func TestAggregateGovernanceMetricsFromCanonicalEvents(t *testing.T) {
	events := []*domain.CanonicalEvent{
		metricEvent(1, domain.EventTurnReceiptAppended, domain.AggregateTodo, "todo_1", map[string]any{
			"goal_id": "goal_1", "record_kind": "phase", "phase": "decision_decode",
		}),
		metricEvent(2, domain.EventTurnReceiptAppended, domain.AggregateTodo, "todo_1", map[string]any{
			"goal_id": "goal_1", "record_kind": "phase", "phase": "validation", "valid": false,
			"error_code": "plan_schema_validation",
		}),
		metricEvent(3, domain.EventCoordinatorAttemptUpdated, domain.AggregateTaskCoordinator, "coord_1", map[string]any{
			"stage": "repair", "repair_attempt": 1, "plan_error_code": "plan_schema_validation",
		}),
		metricEvent(4, domain.EventCoordinatorPlanUpdated, domain.AggregateTaskCoordinator, "coord_1", map[string]any{
			"stage": "decision",
		}),
		metricEvent(5, domain.EventCoordinatorBlocked, domain.AggregateTaskCoordinator, "coord_2", map[string]any{
			"stage": "repair", "failure_code": "coordinator_plan_repair_exhausted",
		}),
		metricEvent(6, domain.EventTurnReceiptAppended, domain.AggregateTodo, "todo_1", map[string]any{
			"goal_id": "goal_1", "record_kind": "phase", "phase": "decision_decode", "outcome": "replayed",
		}),
		metricEvent(7, domain.EventTurnReceiptAppended, domain.AggregateTodo, "todo_1", map[string]any{
			"goal_id": "goal_1", "record_kind": "phase", "phase": "decision_decode", "outcome": "conflict",
		}),
		metricEvent(8, domain.EventProjectionUpdated, domain.AggregateGovernanceProjection, "goal_1", map[string]any{
			"cause": "consistency_issue",
		}),
		metricEvent(9, domain.EventProjectionRepairChanged, domain.AggregateProjectionRepair, "projection_repair_1", map[string]any{
			"status": "failed",
		}),
		metricEvent(10, domain.EventCoordinatorBlocked, domain.AggregateTaskCoordinator, "coord_3", map[string]any{
			"root_work_item_id": "wi_1", "failure_code": "governance_evidence_insufficient",
		}),
		metricEvent(11, domain.EventWorkItemBlocked, domain.AggregateWorkItem, "wi_1", map[string]any{
			"code": "governance_evidence_insufficient",
		}),
		metricEvent(12, domain.EventWorkItemBlocked, domain.AggregateWorkItem, "wi_2", map[string]any{
			"code": "evidence_missing",
		}),
		metricEvent(13, domain.EventWorkItemBlocked, domain.AggregateWorkItem, "wi_3", map[string]any{
			"code": "plan_json_syntax",
		}),
		metricEvent(14, domain.EventWorkItemUnblocked, domain.AggregateWorkItem, "wi_3", nil),
		metricEvent(15, domain.EventWorkItemBlocked, domain.AggregateWorkItem, "wi_4", map[string]any{
			"code": "manual_block",
		}),
		metricEvent(16, domain.EventWorkItemUnblocked, domain.AggregateWorkItem, "wi_4", nil),
		metricEvent(17, domain.EventHandoffCreated, domain.AggregateHandoff, "handoff_1", nil),
		metricEvent(18, domain.EventGoalEvidenceAdded, domain.AggregateGoal, "goal_1", map[string]any{
			"source_kind": "validation_result", "source_id": "validation_1", "verification": "passed",
		}),
		metricEvent(19, domain.EventValidationRecorded, domain.AggregateValidationResult, "validation_1", map[string]any{
			"validation_result_id": "validation_1", "status": "passed",
		}),
		metricEvent(20, domain.EventGoalEvidenceAdded, domain.AggregateGoal, "goal_1", map[string]any{
			"source_kind": "work_item", "source_id": "wi_1", "verification": "accepted",
		}),
		metricEvent(21, domain.EventQuotaSpendRecorded, domain.AggregateGoal, "goal_1", map[string]any{
			"goal_id": "goal_1", "quota_kind": string(domain.QuotaOutputTokens),
			"status": string(domain.QuotaSpendCommitted), "amount": int64(7),
		}),
		metricEvent(22, domain.EventQuotaSpendRecorded, domain.AggregateGoal, "goal_1", map[string]any{
			"goal_id": "goal_1", "quota_kind": string(domain.QuotaCostMicroUSD),
			"status": string(domain.QuotaSpendCommitted), "amount": int64(3),
		}),
	}

	got, err := AggregateGovernanceMetrics(events)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceEventSeq != 22 || got.PlanDecodeSuccess != 1 || got.RepairAttempts != 1 ||
		got.RepairSuccesses != 1 || got.RepairBlockers != 1 || got.ReceiptReplays != 1 ||
		got.ReceiptConflicts != 1 || got.ProjectionDivergences != 1 ||
		got.EvidenceFinishRejections != 2 || got.UserUnblocks != 1 || got.ProjectionUpdates != 1 ||
		got.Handoffs != 1 || got.EvidenceItems != 2 {
		t.Fatalf("canonical governance metrics mismatch: %+v", got)
	}
	if got.PlanDecodeErrors[ErrorFamilySchema] != 2 {
		t.Fatalf("schema error family must include validation and repair evidence: %+v", got.PlanDecodeErrors)
	}
	if len(got.GoalSummaries) != 1 || got.GoalSummaries[0].GoalID != "goal_1" ||
		got.GoalSummaries[0].TurnCount != 0 || got.GoalSummaries[0].OutputTokens != 7 ||
		got.GoalSummaries[0].CostMicroUSD != 3 {
		t.Fatalf("per-Goal event spend summary mismatch: %+v", got.GoalSummaries)
	}
}

func TestAggregateGovernanceMetricsRejectsSpendOverflow(t *testing.T) {
	max := int64(^uint64(0) >> 1)
	_, err := AggregateGovernanceMetrics([]*domain.CanonicalEvent{
		metricEvent(1, domain.EventQuotaSpendRecorded, domain.AggregateGoal, "goal_overflow", map[string]any{
			"goal_id": "goal_overflow", "quota_kind": string(domain.QuotaOutputTokens),
			"status": string(domain.QuotaSpendCommitted), "amount": max,
		}),
		metricEvent(2, domain.EventQuotaSpendRecorded, domain.AggregateGoal, "goal_overflow", map[string]any{
			"goal_id": "goal_overflow", "quota_kind": string(domain.QuotaOutputTokens),
			"status": string(domain.QuotaSpendCommitted), "amount": int64(1),
		}),
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("spend overflow must fail closed with validation error, got %v", err)
	}
}

func TestErrorFamilyIsClosedAndDeterministic(t *testing.T) {
	cases := map[string]string{
		"plan_json_syntax":            ErrorFamilySyntax,
		"plan_schema_validation":      ErrorFamilySchema,
		"plan_semantic_validation":    ErrorFamilySemantic,
		"plan_authority_denied":       ErrorFamilyAuthority,
		"plan_quota_denied":           ErrorFamilyQuota,
		"unexpected_provider_failure": ErrorFamilyUnknown,
	}
	for code, want := range cases {
		if got := ErrorFamily(code); got != want {
			t.Fatalf("ErrorFamily(%q)=%q want %q", code, got, want)
		}
	}
}
