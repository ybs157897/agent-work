package domain

import (
	"errors"
	"testing"
)

func TestTaskCoordinatorRepairCheckpointValidation(t *testing.T) {
	state := &TaskCoordinatorState{}
	if err := state.ValidateRepair(); err != nil || state.RepairStatus != CoordinatorRepairNone {
		t.Fatalf("zero repair state invalid: %+v err=%v", state, err)
	}
	state.RepairStatus = CoordinatorRepairPending
	state.RepairAttempt = 1
	state.RepairSourceRunID = NewID(PrefixRun)
	state.RepairErrorClass = CoordinatorRepairErrorSchema
	state.RepairErrorCode = string(GovernanceErrorPlanSchemaValidation)
	state.RepairValidationErrors = []GovernanceValidationError{{
		Code: GovernanceErrorPlanSchemaValidation, Message: "unknown field", Path: "/steps/0/extra",
	}}
	if err := state.ValidateRepair(); err != nil {
		t.Fatal(err)
	}
	state.RepairStatus = CoordinatorRepairExhausted
	state.RepairAttempt = 2
	if err := state.ValidateRepair(); !errors.Is(err, ErrValidation) {
		t.Fatalf("exhausted repair must require a blocked control line, got %v", err)
	}
	state.Status = CoordinatorBlocked
	if err := state.ValidateRepair(); err != nil {
		t.Fatal(err)
	}
	state.ClearRepair()
	if err := state.ValidateRepair(); err != nil || state.RepairAttempt != 0 || len(state.RepairValidationErrors) != 0 {
		t.Fatalf("clear repair mismatch: %+v err=%v", state, err)
	}

	state.RepairStatus = CoordinatorRepairPending
	state.RepairAttempt = 3
	state.RepairSourceRunID = NewID(PrefixRun)
	state.RepairErrorClass = CoordinatorRepairErrorSchema
	state.RepairErrorCode = "plan_schema_validation"
	if err := state.ValidateRepair(); !errors.Is(err, ErrValidation) {
		t.Fatalf("attempt >2 must fail, got %v", err)
	}
}
