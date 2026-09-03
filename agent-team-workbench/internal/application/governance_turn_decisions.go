package application

import (
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// governanceTurnDecisionKind maps a real Coordinator control action and the
// typed PlanDecision to the closed TurnDecision vocabulary. The result is
// stored inside immutable Receipt phase 1; it is not a best-effort label on a
// later event.
func governanceTurnDecisionKind(run *domain.ExecutionRun, decision *domain.PlanDecisionV2) domain.TurnDecisionKind {
	if run != nil {
		action, _ := coordinatorContextOf(run)["action"].(string)
		switch action {
		case "repair_plan":
			return domain.TurnDecisionRepair
		case "recover", "replan", "message", "user_action_resolved":
			return domain.TurnDecisionReplan
		}
	}
	if decision != nil {
		hasDispatch := false
		hasWait := false
		finishOnly := len(decision.Steps) == 1 && decision.Steps[0].Verb == domain.PlanVerbFinish
		for _, step := range decision.Steps {
			switch step.Verb {
			case domain.PlanVerbDispatch:
				hasDispatch = true
				finishOnly = false
			case domain.PlanVerbDefer, domain.PlanVerbJoin:
				hasWait = true
				finishOnly = false
			case domain.PlanVerbFinish:
				// finishOnly is determined from the single-step shape above.
			case domain.PlanVerbConsultKnowledge:
				finishOnly = false
			}
		}
		if finishOnly {
			return domain.TurnDecisionFinish
		}
		if hasWait && !hasDispatch {
			return domain.TurnDecisionWait
		}
	}
	return domain.TurnDecisionExecute
}

func newGovernanceTurnDecision(key domain.TurnKey, kind domain.TurnDecisionKind,
	reason, nextAction, schemaVersion string, plan *domain.PlanDecisionV2, recordedAt time.Time,
	validationErrors ...[]domain.GovernanceValidationError) (*domain.TurnDecision, error) {
	validation := []domain.GovernanceValidationError{}
	if len(validationErrors) > 0 && validationErrors[0] != nil {
		validation = append(validation, validationErrors[0]...)
	}
	decision := &domain.TurnDecision{
		TurnKey: key, Decision: kind, Reason: reason, NextAction: nextAction,
		SchemaVersion: schemaVersion, PlanDecision: plan,
		ValidationErrors: validation, RecordedAt: recordedAt,
	}
	if err := decision.Validate(); err != nil {
		return nil, fmt.Errorf("%w: validate governance TurnDecision: %v", domain.ErrValidation, err)
	}
	return decision, nil
}
