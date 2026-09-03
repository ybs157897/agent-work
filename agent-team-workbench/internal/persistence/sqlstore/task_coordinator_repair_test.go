package sqlstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestTaskCoordinatorRepairCheckpointRoundTripAndClear(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	config, err := store.TaskCoordinators().EnsureConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	root := &domain.WorkItem{
		ID: "wi_repair_state", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "repair", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	run := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: root.WorkspaceID, WorkItemID: root.ID,
		AgentProfileID: config.AgentProfileID, Status: domain.RunQueued, RuntimeLabel: "mock",
		AdapterID: "mock", Provider: "mock", Input: map[string]any{"instruction": "repair"},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	state := &domain.TaskCoordinatorState{
		ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: root.WorkspaceID,
		RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID,
		Status: domain.CoordinatorQueued, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.TaskCoordinators().CreateState(ctx, state); err != nil {
		t.Fatal(err)
	}
	state.Status = domain.CoordinatorRunning
	state.Phase = "repair"
	state.CurrentRunID = run.ID
	state.RepairStatus = domain.CoordinatorRepairPending
	state.RepairAttempt = 1
	state.RepairSourceRunID = run.ID
	state.RepairErrorClass = domain.CoordinatorRepairErrorSchema
	state.RepairErrorCode = string(domain.GovernanceErrorPlanSchemaValidation)
	state.RepairValidationErrors = []domain.GovernanceValidationError{{
		Code: domain.GovernanceErrorPlanSchemaValidation, Message: "unknown field", Path: "/steps/0/extra",
	}}
	if err := store.TaskCoordinators().UpdateState(ctx, state, 1); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RepairStatus != domain.CoordinatorRepairPending || loaded.RepairAttempt != 1 ||
		loaded.RepairSourceRunID != run.ID || len(loaded.RepairValidationErrors) != 1 {
		t.Fatalf("repair checkpoint round trip mismatch: %+v", loaded)
	}
	loaded.ClearRepair()
	if err := store.TaskCoordinators().UpdateState(ctx, loaded, loaded.Version); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.RepairStatus != domain.CoordinatorRepairNone || cleared.RepairAttempt != 0 ||
		cleared.RepairSourceRunID != "" || len(cleared.RepairValidationErrors) != 0 {
		t.Fatalf("repair checkpoint did not clear: %+v", cleared)
	}

	cleared.RepairStatus = domain.CoordinatorRepairPending
	cleared.RepairAttempt = 1
	cleared.RepairErrorClass = domain.CoordinatorRepairErrorSyntax
	cleared.RepairErrorCode = string(domain.GovernanceErrorPlanJSONSyntax)
	if err := store.TaskCoordinators().UpdateState(ctx, cleared, cleared.Version); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("pending repair without source must fail, got %v", err)
	}
}
