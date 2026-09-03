package domain

import (
	"fmt"
	"strings"
	"time"
)

// GovernanceActorKind is the small identity vocabulary used by a governance
// handoff. Agent ownership remains the only claim primitive; a runtime actor
// must be resolved to exactly one agent by the application before transfer.
type GovernanceActorKind string

const (
	GovernanceActorAgent   GovernanceActorKind = "agent"
	GovernanceActorRuntime GovernanceActorKind = "runtime"
)

func (k GovernanceActorKind) Valid() bool {
	return k == GovernanceActorAgent || k == GovernanceActorRuntime
}

// GovernanceActorRef is an opaque control-plane identity. Runtime labels are
// not silently treated as Agent IDs; application scope validation owns that
// mapping.
type GovernanceActorRef struct {
	Kind GovernanceActorKind `json:"kind"`
	ID   string              `json:"id"`
}

func (a GovernanceActorRef) Validate() error {
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: governance actor kind %q", ErrValidation, a.Kind)
	}
	if err := validateText("governance actor id", a.ID, 256); err != nil {
		return err
	}
	if a.Kind == GovernanceActorAgent {
		if err := validateTypedID("governance actor id", a.ID, PrefixAgent); err != nil {
			return err
		}
	}
	return nil
}

type HandoffStatus string

const (
	HandoffPending     HandoffStatus = "pending"
	HandoffAccepted    HandoffStatus = "accepted"
	HandoffTransferred HandoffStatus = "transferred"
	HandoffRejected    HandoffStatus = "rejected"
	HandoffCancelled   HandoffStatus = "cancelled"
)

func (s HandoffStatus) Valid() bool {
	return s == HandoffPending || s == HandoffAccepted || s == HandoffTransferred ||
		s == HandoffRejected || s == HandoffCancelled
}

func (s HandoffStatus) IsTerminal() bool {
	return s == HandoffTransferred || s == HandoffRejected || s == HandoffCancelled
}

type HandoffClaimTransferState string

const (
	HandoffClaimRetainedBySource HandoffClaimTransferState = "retained_by_source"
	HandoffClaimedByTarget       HandoffClaimTransferState = "claimed_by_target"
	HandoffClaimTransferred      HandoffClaimTransferState = "transferred"
)

func (s HandoffClaimTransferState) Valid() bool {
	return s == HandoffClaimRetainedBySource || s == HandoffClaimedByTarget || s == HandoffClaimTransferred
}

// Handoff is a governance ownership transfer record. It never carries a
// provider session handle or transcript; the target must re-evaluate resume vs
// fresh from the existing TaskSession/ExecutionContextSnapshot surfaces.
type Handoff struct {
	ID                 string                    `json:"id"`
	GoalID             string                    `json:"goal_id"`
	TodoID             string                    `json:"todo_id"`
	Source             GovernanceActorRef        `json:"source"`
	Target             GovernanceActorRef        `json:"target"`
	Reason             string                    `json:"reason"`
	ContextSummary     string                    `json:"context_summary"`
	Evidence           []GovernanceEvidenceItem  `json:"evidence"`
	OpenRisks          []string                  `json:"open_risks"`
	Acceptance         string                    `json:"acceptance,omitempty"`
	ResolutionReason   string                    `json:"resolution_reason,omitempty"`
	Status             HandoffStatus             `json:"status"`
	ClaimTransferState HandoffClaimTransferState `json:"claim_transfer_state"`
	SourceClaimVersion int                       `json:"source_claim_version"`
	TargetClaimVersion int                       `json:"target_claim_version"`
	Actor              GovernanceActorRef        `json:"actor"`
	ClientKey          string                    `json:"client_key,omitempty"`
	AcceptedBy         *GovernanceActorRef       `json:"accepted_by,omitempty"`
	AcceptedAt         *time.Time                `json:"accepted_at,omitempty"`
	Version            int                       `json:"version"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
}

func (h *Handoff) Validate() error {
	if h == nil {
		return fmt.Errorf("%w: nil handoff", ErrValidation)
	}
	if err := validateTypedID("handoff.id", h.ID, PrefixHandoff); err != nil {
		return err
	}
	if err := validateTypedID("handoff.goal_id", h.GoalID, PrefixGoal); err != nil {
		return err
	}
	if err := validateTypedID("handoff.todo_id", h.TodoID, PrefixTodo); err != nil {
		return err
	}
	if err := h.Source.Validate(); err != nil {
		return fmt.Errorf("%w: handoff.source: %v", ErrValidation, err)
	}
	if err := h.Target.Validate(); err != nil {
		return fmt.Errorf("%w: handoff.target: %v", ErrValidation, err)
	}
	if h.Source == h.Target {
		return fmt.Errorf("%w: handoff source and target must differ", ErrValidation)
	}
	if err := validateText("handoff.reason", h.Reason, 4000); err != nil {
		return err
	}
	if err := validateText("handoff.context_summary", h.ContextSummary, 20000); err != nil {
		return err
	}
	if h.ResolutionReason != "" {
		if err := validateText("handoff.resolution_reason", h.ResolutionReason, 4000); err != nil {
			return err
		}
	}
	if len(h.Evidence) > 128 {
		return fmt.Errorf("%w: handoff.evidence exceeds 128 items", ErrValidation)
	}
	for i := range h.Evidence {
		if err := h.Evidence[i].Validate(); err != nil {
			return fmt.Errorf("%w: handoff.evidence[%d]: %v", ErrValidation, i, err)
		}
	}
	if len(h.OpenRisks) > 64 {
		return fmt.Errorf("%w: handoff.open_risks exceeds 64 items", ErrValidation)
	}
	for i, risk := range h.OpenRisks {
		if err := validateText(fmt.Sprintf("handoff.open_risks[%d]", i), risk, 2000); err != nil {
			return err
		}
	}
	if h.ClientKey != "" {
		if err := validateText("handoff.client_key", h.ClientKey, 256); err != nil {
			return err
		}
	}
	if !h.Status.Valid() {
		return fmt.Errorf("%w: handoff.status %q", ErrValidation, h.Status)
	}
	if !h.ClaimTransferState.Valid() {
		return fmt.Errorf("%w: handoff.claim_transfer_state %q", ErrValidation, h.ClaimTransferState)
	}
	if h.SourceClaimVersion < 1 || h.TargetClaimVersion < 0 {
		return fmt.Errorf("%w: handoff claim versions are invalid", ErrValidation)
	}
	if h.Actor.Validate() != nil {
		return fmt.Errorf("%w: handoff.actor is invalid", ErrValidation)
	}
	if h.AcceptedBy != nil {
		if err := h.AcceptedBy.Validate(); err != nil {
			return fmt.Errorf("%w: handoff.accepted_by: %v", ErrValidation, err)
		}
		if h.AcceptedAt == nil || h.AcceptedAt.IsZero() {
			return fmt.Errorf("%w: accepted handoff requires accepted_at", ErrValidation)
		}
	}
	if h.Status == HandoffPending || h.Status == HandoffRejected || h.Status == HandoffCancelled {
		if h.ClaimTransferState != HandoffClaimRetainedBySource {
			return fmt.Errorf("%w: non-transferred handoff must retain source claim", ErrValidation)
		}
	}
	if h.Status == HandoffAccepted && h.ClaimTransferState != HandoffClaimedByTarget {
		return fmt.Errorf("%w: accepted handoff must record target claim", ErrValidation)
	}
	if h.Status == HandoffTransferred && h.ClaimTransferState != HandoffClaimTransferred {
		return fmt.Errorf("%w: transferred handoff must record transfer", ErrValidation)
	}
	if h.Status == HandoffAccepted || h.Status == HandoffTransferred {
		if strings.TrimSpace(h.Acceptance) == "" || h.AcceptedBy == nil {
			return fmt.Errorf("%w: accepted handoff requires target acceptance", ErrValidation)
		}
	}
	if h.Version < 1 {
		return fmt.Errorf("%w: handoff.version must be >= 1", ErrValidation)
	}
	if h.CreatedAt.IsZero() || h.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: handoff timestamps are required", ErrValidation)
	}
	return nil
}

func (h *Handoff) Accept(target GovernanceActorRef, acceptance string, now time.Time, targetClaimVersion int) error {
	if h == nil {
		return fmt.Errorf("%w: nil handoff", ErrValidation)
	}
	if h.Status != HandoffPending {
		return &TransitionError{Entity: "handoff", From: string(h.Status), To: string(HandoffAccepted)}
	}
	if target != h.Target {
		return fmt.Errorf("%w: handoff target mismatch", ErrStateConflict)
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if err := validateText("handoff.acceptance", acceptance, 4000); err != nil {
		return err
	}
	if targetClaimVersion < 1 {
		return fmt.Errorf("%w: target claim version must be >= 1", ErrValidation)
	}
	h.Status = HandoffAccepted
	h.ClaimTransferState = HandoffClaimedByTarget
	h.Acceptance = acceptance
	h.AcceptedBy = &target
	h.AcceptedAt = &now
	h.TargetClaimVersion = targetClaimVersion
	h.Version++
	h.UpdatedAt = now
	return nil
}

func (h *Handoff) Transfer(now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: nil handoff", ErrValidation)
	}
	if h.Status != HandoffAccepted || h.ClaimTransferState != HandoffClaimedByTarget {
		return &TransitionError{Entity: "handoff", From: string(h.Status), To: string(HandoffTransferred)}
	}
	h.Status = HandoffTransferred
	h.ClaimTransferState = HandoffClaimTransferred
	h.Version++
	h.UpdatedAt = now
	return nil
}

func (h *Handoff) Reject(now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: nil handoff", ErrValidation)
	}
	if h.Status == HandoffRejected {
		return nil
	}
	if h.Status != HandoffPending {
		return &TransitionError{Entity: "handoff", From: string(h.Status), To: string(HandoffRejected)}
	}
	h.Status = HandoffRejected
	h.Version++
	h.UpdatedAt = now
	return nil
}

func (h *Handoff) SetResolutionReason(reason string) error {
	if h == nil {
		return fmt.Errorf("%w: nil handoff", ErrValidation)
	}
	if err := validateText("handoff.resolution_reason", reason, 4000); err != nil {
		return err
	}
	h.ResolutionReason = reason
	return nil
}

func (h *Handoff) Cancel(now time.Time) error {
	if h == nil {
		return fmt.Errorf("%w: nil handoff", ErrValidation)
	}
	if h.Status == HandoffCancelled {
		return nil
	}
	// An accepted handoff already owns the target claim. Cancellation would
	// require an atomic reverse claim operation, so this domain object refuses
	// the ambiguous half-transition; callers may only cancel before acceptance.
	if h.Status != HandoffPending {
		return &TransitionError{Entity: "handoff", From: string(h.Status), To: string(HandoffCancelled)}
	}
	h.Status = HandoffCancelled
	h.ClaimTransferState = HandoffClaimRetainedBySource
	h.Version++
	h.UpdatedAt = now
	return nil
}
