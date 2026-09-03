package domain

import (
	"fmt"
	"time"
)

type GovernanceProjectionScope string

const (
	ProjectionScopeGoalProgress         GovernanceProjectionScope = "goal_progress"
	ProjectionScopeTodoCurrentState     GovernanceProjectionScope = "todo_current_state"
	ProjectionScopeReceiptTimeline      GovernanceProjectionScope = "receipt_timeline"
	ProjectionScopeEvidenceSummary      GovernanceProjectionScope = "evidence_summary"
	ProjectionScopeNextActionCheckpoint GovernanceProjectionScope = "next_action_checkpoint"
)

func (s GovernanceProjectionScope) Valid() bool {
	return s == ProjectionScopeGoalProgress || s == ProjectionScopeTodoCurrentState ||
		s == ProjectionScopeReceiptTimeline || s == ProjectionScopeEvidenceSummary ||
		s == ProjectionScopeNextActionCheckpoint
}

type ProjectionRepairStatus string

const (
	ProjectionRepairPending   ProjectionRepairStatus = "pending"
	ProjectionRepairRunning   ProjectionRepairStatus = "running"
	ProjectionRepairCompleted ProjectionRepairStatus = "completed"
	ProjectionRepairFailed    ProjectionRepairStatus = "failed"
)

func (s ProjectionRepairStatus) Valid() bool {
	return s == ProjectionRepairPending || s == ProjectionRepairRunning ||
		s == ProjectionRepairCompleted || s == ProjectionRepairFailed
}

type ProjectionCursor struct {
	EventStreamSeq int64 `json:"event_stream_seq"`
	ThroughTurnSeq int64 `json:"through_turn_seq"`
}

func (c ProjectionCursor) Validate() error {
	if c.EventStreamSeq < 0 || c.ThroughTurnSeq < 0 {
		return fmt.Errorf("%w: projection cursor must be non-negative", ErrValidation)
	}
	return nil
}

// GovernanceGoalProjection is a derived read model. It intentionally has no
// command authority: Goal/Todo/Plan/Run and canonical Receipt/Event remain the
// sources of truth.
type GovernanceGoalProjection struct {
	GoalID               string                   `json:"goal_id"`
	GoalProgress         map[string]any           `json:"goal_progress"`
	TodoCurrentState     map[string]any           `json:"todo_current_state"`
	ReceiptTimeline      []map[string]any         `json:"receipt_timeline"`
	EvidenceSummary      []GovernanceEvidenceItem `json:"evidence_summary"`
	NextActionCheckpoint map[string]any           `json:"next_action_checkpoint"`
	Counters             map[string]int64         `json:"counters"`
	SourceCursor         ProjectionCursor         `json:"source_cursor"`
	Digest               string                   `json:"digest"`
	Version              int                      `json:"version"`
	UpdatedAt            time.Time                `json:"updated_at"`
}

func (p *GovernanceGoalProjection) Validate() error {
	if p == nil {
		return fmt.Errorf("%w: nil governance projection", ErrValidation)
	}
	if err := validateTypedID("projection.goal_id", p.GoalID, PrefixGoal); err != nil {
		return err
	}
	if p.GoalProgress == nil || p.TodoCurrentState == nil || p.NextActionCheckpoint == nil || p.Counters == nil {
		return fmt.Errorf("%w: governance projection maps are required", ErrValidation)
	}
	if p.ReceiptTimeline == nil {
		p.ReceiptTimeline = []map[string]any{}
	}
	if p.EvidenceSummary == nil {
		p.EvidenceSummary = []GovernanceEvidenceItem{}
	}
	if len(p.ReceiptTimeline) > 4096 || len(p.EvidenceSummary) > 512 {
		return fmt.Errorf("%w: governance projection exceeds bounded size", ErrValidation)
	}
	if err := p.SourceCursor.Validate(); err != nil {
		return err
	}
	if err := ValidateCanonicalDigest(p.Digest); err != nil {
		return fmt.Errorf("%w: projection.digest: %v", ErrValidation, err)
	}
	if p.Version < 1 || p.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: projection version/timestamp required", ErrValidation)
	}
	for i := range p.EvidenceSummary {
		if err := p.EvidenceSummary[i].Validate(); err != nil {
			return fmt.Errorf("%w: projection evidence[%d]: %v", ErrValidation, i, err)
		}
	}
	return nil
}

// ProjectionRepair is an operator-visible durable checkpoint for rebuilding a
// derived projection. It is not a Run and cannot mutate canonical history.
type ProjectionRepair struct {
	ID                   string                      `json:"id"`
	GoalID               string                      `json:"goal_id"`
	Status               ProjectionRepairStatus      `json:"status"`
	Scope                []GovernanceProjectionScope `json:"scope"`
	SourceCursor         ProjectionCursor            `json:"source_cursor"`
	ReplayedEventCount   int                         `json:"replayed_event_count"`
	ReplayedReceiptCount int                         `json:"replayed_receipt_count"`
	ErrorCode            string                      `json:"error_code,omitempty"`
	ErrorMessage         string                      `json:"error_message,omitempty"`
	ClientKey            string                      `json:"client_key,omitempty"`
	Version              int                         `json:"version"`
	StartedAt            time.Time                   `json:"started_at"`
	CompletedAt          *time.Time                  `json:"completed_at,omitempty"`
	CreatedAt            time.Time                   `json:"created_at"`
	UpdatedAt            time.Time                   `json:"updated_at"`
}

func (r *ProjectionRepair) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil projection repair", ErrValidation)
	}
	if err := validateTypedID("projection_repair.id", r.ID, PrefixProjectionRepair); err != nil {
		return err
	}
	if err := validateTypedID("projection_repair.goal_id", r.GoalID, PrefixGoal); err != nil {
		return err
	}
	if !r.Status.Valid() {
		return fmt.Errorf("%w: projection repair status %q", ErrValidation, r.Status)
	}
	if len(r.Scope) == 0 || len(r.Scope) > 5 {
		return fmt.Errorf("%w: projection repair scope must contain 1..5 items", ErrValidation)
	}
	seen := map[GovernanceProjectionScope]struct{}{}
	for _, scope := range r.Scope {
		if !scope.Valid() {
			return fmt.Errorf("%w: projection repair scope %q", ErrValidation, scope)
		}
		if _, ok := seen[scope]; ok {
			return fmt.Errorf("%w: duplicate projection repair scope %q", ErrValidation, scope)
		}
		seen[scope] = struct{}{}
	}
	if err := r.SourceCursor.Validate(); err != nil {
		return err
	}
	if r.ReplayedEventCount < 0 || r.ReplayedReceiptCount < 0 || r.Version < 1 {
		return fmt.Errorf("%w: projection repair counts/version invalid", ErrValidation)
	}
	if r.ErrorCode != "" {
		if err := validateText("projection repair.error_code", r.ErrorCode, 128); err != nil {
			return err
		}
	}
	if r.ErrorMessage != "" {
		if err := validateText("projection repair.error_message", r.ErrorMessage, 4000); err != nil {
			return err
		}
	}
	if r.ClientKey != "" {
		if err := validateText("projection repair.client_key", r.ClientKey, 256); err != nil {
			return err
		}
	}
	if r.StartedAt.IsZero() || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: projection repair timestamps required", ErrValidation)
	}
	if r.Status == ProjectionRepairCompleted && r.CompletedAt == nil {
		return fmt.Errorf("%w: completed projection repair requires completed_at", ErrValidation)
	}
	return nil
}
