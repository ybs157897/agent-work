package application

import (
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestSettlementWakeBlocksReplacementOnlyWhileQueuedOrFreshlyConsumed(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	base := domain.WakeupRequest{
		Context:   map[string]any{settleDispatchContextKey: "disp_target"},
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	tests := []struct {
		name string
		wake domain.WakeupRequest
		want bool
	}{
		{name: "queued remains authoritative", wake: func() domain.WakeupRequest {
			w := base
			w.Status = domain.WakeupStatusQueued
			w.UpdatedAt = now.Add(-time.Hour)
			return w
		}(), want: true},
		{name: "fresh consumed claim is in flight", wake: func() domain.WakeupRequest {
			w := base
			w.Status = domain.WakeupStatusConsumed
			w.UpdatedAt = now.Add(-settlementWakeClaimGrace / 2)
			return w
		}(), want: true},
		{name: "stale consumed claim is recoverable", wake: func() domain.WakeupRequest {
			w := base
			w.Status = domain.WakeupStatusConsumed
			w.UpdatedAt = now.Add(-2 * settlementWakeClaimGrace)
			return w
		}(), want: false},
		{name: "coalesced is not a continuation", wake: func() domain.WakeupRequest {
			w := base
			w.Status = domain.WakeupStatusCoalesced
			return w
		}(), want: false},
		{name: "other dispatch is irrelevant", wake: func() domain.WakeupRequest {
			w := base
			w.Status = domain.WakeupStatusQueued
			w.Context = map[string]any{settleDispatchContextKey: "disp_other"}
			return w
		}(), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := settlementWakeBlocksReplacement(test.wake, "disp_target", now); got != test.want {
				t.Fatalf("settlementWakeBlocksReplacement()=%v want=%v wake=%+v", got, test.want, test.wake)
			}
		})
	}
}

func TestSettlementCheckpointMatchesExactRunGeneration(t *testing.T) {
	state := &domain.TaskCoordinatorState{Data: map[string]any{
		"control_action":     coordinatorSettlementAction,
		"settle_dispatch_id": "disp_new",
		"settle_run_id":      "run_new",
	}}
	if !settlementCheckpointMatches(state, &domain.ExecutionRun{ID: "run_new", DispatchID: "disp_new"}) {
		t.Fatal("exact settlement generation should match")
	}
	for _, stale := range []*domain.ExecutionRun{
		{ID: "run_old", DispatchID: "disp_new"},
		{ID: "run_new", DispatchID: "disp_old"},
		{ID: "run_old", DispatchID: "disp_old"},
	} {
		if settlementCheckpointMatches(state, stale) {
			t.Fatalf("stale settlement generation must not match: %+v", stale)
		}
	}
}
