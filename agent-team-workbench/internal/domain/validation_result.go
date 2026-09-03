package domain

import (
	"fmt"
	"time"
)

// ValidationResult is a durable control-plane validation fact. It is distinct
// from model-reported prose/verdicts and may therefore be cited by a finish
// gate or GovernanceEvidenceItem.
type ValidationResultStatus string

const (
	ValidationResultPending ValidationResultStatus = "pending"
	ValidationResultPassed  ValidationResultStatus = "passed"
	ValidationResultFailed  ValidationResultStatus = "failed"
)

func (s ValidationResultStatus) Valid() bool {
	return s == ValidationResultPending || s == ValidationResultPassed || s == ValidationResultFailed
}

type ValidationResult struct {
	ID             string                 `json:"id"`
	GoalID         string                 `json:"goal_id"`
	TodoID         string                 `json:"todo_id"`
	WorkItemID     string                 `json:"work_item_id"`
	SourceRunID    string                 `json:"source_run_id"`
	CriteriaDigest string                 `json:"criteria_digest"`
	Status         ValidationResultStatus `json:"status"`
	Summary        string                 `json:"summary"`
	ProducedBy     string                 `json:"produced_by"`
	RecordedAt     time.Time              `json:"recorded_at"`
	Version        int                    `json:"version"`
	CreatedAt      time.Time              `json:"created_at"`
}

func (r *ValidationResult) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil validation result", ErrValidation)
	}
	if err := validateTypedID("validation_result.id", r.ID, PrefixValidationResult); err != nil {
		return err
	}
	if err := validateTypedID("validation_result.goal_id", r.GoalID, PrefixGoal); err != nil {
		return err
	}
	if err := validateTypedID("validation_result.todo_id", r.TodoID, PrefixTodo); err != nil {
		return err
	}
	if err := validateTypedID("validation_result.work_item_id", r.WorkItemID, PrefixWorkItem); err != nil {
		return err
	}
	if err := validateTypedID("validation_result.source_run_id", r.SourceRunID, PrefixRun); err != nil {
		return err
	}
	if err := ValidateCanonicalDigest(r.CriteriaDigest); err != nil {
		return fmt.Errorf("%w: validation_result.criteria_digest: %v", ErrValidation, err)
	}
	if !r.Status.Valid() {
		return fmt.Errorf("%w: validation_result.status %q", ErrValidation, r.Status)
	}
	if err := validateText("validation_result.summary", r.Summary, 4000); err != nil {
		return err
	}
	if r.ProducedBy != "control_plane" {
		return fmt.Errorf("%w: validation_result.produced_by must be control_plane", ErrValidation)
	}
	if r.RecordedAt.IsZero() || r.CreatedAt.IsZero() || r.Version < 1 {
		return fmt.Errorf("%w: validation_result timestamps/version required", ErrValidation)
	}
	return nil
}
