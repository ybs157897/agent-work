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

// F3 契约：semantic 类进入有界自动修复白名单（pending 与 exhausted 两个检查点）；
// authority/quota 仍被拒。
func TestTaskCoordinatorRepairCheckpointAllowsSemanticClass(t *testing.T) {
	state := &TaskCoordinatorState{
		RepairStatus: CoordinatorRepairPending, RepairAttempt: 1,
		RepairSourceRunID: NewID(PrefixRun), RepairErrorClass: CoordinatorRepairErrorSemantic,
		RepairErrorCode: string(GovernanceErrorPlanSemanticValidation),
		RepairValidationErrors: []GovernanceValidationError{{
			Code: GovernanceErrorPlanSemanticValidation, Message: "barrier must be final", Path: "/steps/1",
		}},
	}
	if err := state.ValidateRepair(); err != nil {
		t.Fatalf("pending semantic repair checkpoint must be valid: %v", err)
	}
	state.RepairStatus = CoordinatorRepairExhausted
	state.RepairAttempt = 2
	state.Status = CoordinatorBlocked
	if err := state.ValidateRepair(); err != nil {
		t.Fatalf("exhausted semantic repair checkpoint must be valid: %v", err)
	}
	for _, rejected := range []CoordinatorRepairErrorClass{CoordinatorRepairErrorAuthority, CoordinatorRepairErrorQuota, "unknown"} {
		state.RepairErrorClass = rejected
		if err := state.ValidateRepair(); !errors.Is(err, ErrValidation) {
			t.Fatalf("class %q must stay outside the repair whitelist, got %v", rejected, err)
		}
	}
}
