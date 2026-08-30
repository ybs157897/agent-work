package application

import (
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

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
