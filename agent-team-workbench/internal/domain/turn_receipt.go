package domain

import (
	"fmt"
	"strings"
	"time"
)

// TurnReceiptPhaseName is the append-only settlement phase vocabulary. The
// sequence is part of the identity contract and must not be reordered.
type TurnReceiptPhaseName string

const (
	TurnReceiptPhaseDecisionDecode   TurnReceiptPhaseName = "decision_decode"
	TurnReceiptPhaseValidation       TurnReceiptPhaseName = "validation"
	TurnReceiptPhaseDurableWriteback TurnReceiptPhaseName = "durable_writeback"
	TurnReceiptPhasePlanCompile      TurnReceiptPhaseName = "plan_compile"
	TurnReceiptPhaseDispatch         TurnReceiptPhaseName = "dispatch"
	TurnReceiptPhaseQuotaSpend       TurnReceiptPhaseName = "quota_spend"
	TurnReceiptPhaseProjectionOutbox TurnReceiptPhaseName = "projection_outbox"
)

var turnReceiptPhases = [...]TurnReceiptPhaseName{
	TurnReceiptPhaseDecisionDecode,
	TurnReceiptPhaseValidation,
	TurnReceiptPhaseDurableWriteback,
	TurnReceiptPhasePlanCompile,
	TurnReceiptPhaseDispatch,
	TurnReceiptPhaseQuotaSpend,
	TurnReceiptPhaseProjectionOutbox,
}

func (p TurnReceiptPhaseName) Valid() bool {
	_, ok := TurnReceiptPhaseSeq(p)
	return ok
}

// TurnReceiptPhaseNameForSeq maps the one-based phase sequence to its fixed
// contract name. It returns false for values outside the seven-phase prefix.
func TurnReceiptPhaseNameForSeq(seq int) (TurnReceiptPhaseName, bool) {
	if seq < 1 || seq > len(turnReceiptPhases) {
		return "", false
	}
	return turnReceiptPhases[seq-1], true
}

// TurnReceiptPhaseSeq maps a phase name to its one-based sequence.
func TurnReceiptPhaseSeq(phase TurnReceiptPhaseName) (int, bool) {
	for seq, candidate := range turnReceiptPhases {
		if phase == candidate {
			return seq + 1, true
		}
	}
	return 0, false
}

// TurnKey is the sole immutable identity of a governance turn. There is no
// separate turn_id: retries/replays use the same goal/todo/sequence tuple.
type TurnKey struct {
	GoalID  string `json:"goal_id"`
	TodoID  string `json:"todo_id"`
	TurnSeq int64  `json:"turn_seq"`
}

func (k TurnKey) Validate() error {
	if err := validateTypedID("turn_key.goal_id", k.GoalID, PrefixGoal); err != nil {
		return err
	}
	if err := validateTypedID("turn_key.todo_id", k.TodoID, PrefixTodo); err != nil {
		return err
	}
	if k.TurnSeq < 1 {
		return fmt.Errorf("%w: turn_key.turn_seq must be >= 1", ErrValidation)
	}
	return nil
}

func (k TurnKey) Equal(other TurnKey) bool {
	return k.GoalID == other.GoalID && k.TodoID == other.TodoID && k.TurnSeq == other.TurnSeq
}

// TurnReceiptHeader is the immutable admission record. CanonicalDigest is
// supplied by the canonicalization layer; domain validation checks its shape,
// but intentionally does not implement RFC 8785 or hash computation.
type TurnReceiptHeader struct {
	TurnKey             TurnKey `json:"turn_key"`
	Attempt             int     `json:"attempt"`
	SchemaVersion       string  `json:"schema_version"`
	InputSnapshotDigest string  `json:"input_snapshot_digest"`
	AdmissionClientKey  string  `json:"admission_client_key"`
	// GovernedSourceRunID, PlanClientKey and DecisionDigest form an immutable
	// recovery checkpoint for a Coordinator-owned turn. Generic WP1 admission
	// headers may leave the checkpoint empty; governed admission writes all
	// three in the same transaction as the Header and quota reservations.
	GovernedSourceRunID string    `json:"source_run_id,omitempty"`
	PlanClientKey       string    `json:"plan_client_key,omitempty"`
	DecisionDigest      string    `json:"decision_digest,omitempty"`
	CanonicalDigest     string    `json:"canonical_digest"`
	CreatedAt           time.Time `json:"created_at"`
}

func (h *TurnReceiptHeader) Validate() error {
	if h == nil {
		return fmt.Errorf("%w: nil turn receipt header", ErrValidation)
	}
	if err := h.TurnKey.Validate(); err != nil {
		return err
	}
	if h.Attempt < 1 {
		return fmt.Errorf("%w: turn receipt header attempt must be >= 1", ErrValidation)
	}
	if err := validateText("turn receipt header.schema_version", h.SchemaVersion, 128); err != nil {
		return err
	}
	if err := ValidateCanonicalDigest(h.InputSnapshotDigest); err != nil {
		return fmt.Errorf("%w: input_snapshot_digest: %v", ErrValidation, err)
	}
	if err := validateText("turn receipt header.admission_client_key", h.AdmissionClientKey, 256); err != nil {
		return err
	}
	checkpointCount := 0
	if h.GovernedSourceRunID != "" {
		checkpointCount++
		if err := validateTypedID("turn receipt header.source_run_id", h.GovernedSourceRunID, PrefixRun); err != nil {
			return err
		}
	}
	if h.PlanClientKey != "" {
		checkpointCount++
		if err := validateText("turn receipt header.plan_client_key", h.PlanClientKey, 256); err != nil {
			return err
		}
		if strings.TrimSpace(h.PlanClientKey) != h.PlanClientKey {
			return fmt.Errorf("%w: turn receipt header.plan_client_key must be trimmed", ErrValidation)
		}
	}
	if h.DecisionDigest != "" {
		checkpointCount++
		if err := ValidateCanonicalDigest(h.DecisionDigest); err != nil {
			return fmt.Errorf("%w: decision_digest: %v", ErrValidation, err)
		}
	}
	if checkpointCount != 0 && checkpointCount != 3 {
		return fmt.Errorf("%w: governed turn recovery checkpoint must contain source_run_id, plan_client_key and decision_digest together", ErrValidation)
	}
	if err := ValidateCanonicalDigest(h.CanonicalDigest); err != nil {
		return fmt.Errorf("%w: canonical_digest: %v", ErrValidation, err)
	}
	if h.CreatedAt.IsZero() {
		return fmt.Errorf("%w: turn receipt header.created_at is required", ErrValidation)
	}
	return nil
}

// TurnReceiptPhase is an immutable append-only phase record. Payload is the
// decoded business object supplied by application/persistence; this layer
// validates presence and references but does not canonicalize it.
type TurnReceiptPhase struct {
	TurnKey              TurnKey                  `json:"turn_key"`
	PhaseSeq             int                      `json:"phase_seq"`
	Phase                TurnReceiptPhaseName     `json:"phase"`
	Payload              map[string]any           `json:"payload"`
	CanonicalDigest      string                   `json:"canonical_digest"`
	PlanID               string                   `json:"plan_id,omitempty"`
	RunIDs               []string                 `json:"run_ids,omitempty"`
	QuotaReservationKeys []string                 `json:"quota_reservation_keys,omitempty"`
	Evidence             []GovernanceEvidenceItem `json:"evidence,omitempty"`
	CreatedAt            time.Time                `json:"created_at"`
}

func (p *TurnReceiptPhase) Validate() error {
	if p == nil {
		return fmt.Errorf("%w: nil turn receipt phase", ErrValidation)
	}
	if err := p.TurnKey.Validate(); err != nil {
		return err
	}
	phase, ok := TurnReceiptPhaseNameForSeq(p.PhaseSeq)
	if !ok {
		return fmt.Errorf("%w: turn receipt phase_seq %d must be in 1..7", ErrValidation, p.PhaseSeq)
	}
	if p.Phase != phase {
		return fmt.Errorf("%w: phase_seq %d maps to %q, got %q", ErrValidation, p.PhaseSeq, phase, p.Phase)
	}
	if p.Payload == nil {
		return fmt.Errorf("%w: turn receipt phase payload is required", ErrValidation)
	}
	if err := ValidateCanonicalDigest(p.CanonicalDigest); err != nil {
		return fmt.Errorf("%w: canonical_digest: %v", ErrValidation, err)
	}
	if err := validateOptionalTypedID("turn receipt phase.plan_id", p.PlanID, PrefixPlan); err != nil {
		return err
	}
	if len(p.RunIDs) > 64 {
		return fmt.Errorf("%w: turn receipt phase.run_ids exceeds 64 items", ErrValidation)
	}
	if duplicate := firstDuplicate(p.RunIDs); duplicate != "" {
		return fmt.Errorf("%w: duplicate turn receipt run id %q", ErrValidation, duplicate)
	}
	for i, runID := range p.RunIDs {
		if err := validateTypedID(fmt.Sprintf("turn receipt phase.run_ids[%d]", i), runID, PrefixRun); err != nil {
			return err
		}
	}
	if len(p.QuotaReservationKeys) > 8 {
		return fmt.Errorf("%w: turn receipt phase.quota_reservation_keys exceeds 8 items", ErrValidation)
	}
	if duplicate := firstDuplicate(p.QuotaReservationKeys); duplicate != "" {
		return fmt.Errorf("%w: duplicate quota reservation key %q", ErrValidation, duplicate)
	}
	for i, key := range p.QuotaReservationKeys {
		if err := validateText(fmt.Sprintf("turn receipt phase.quota_reservation_keys[%d]", i), key, 512); err != nil {
			return err
		}
	}
	if len(p.Evidence) > 128 {
		return fmt.Errorf("%w: turn receipt phase.evidence exceeds 128 items", ErrValidation)
	}
	for i := range p.Evidence {
		if err := p.Evidence[i].Validate(); err != nil {
			return fmt.Errorf("%w: turn receipt phase.evidence[%d]: %v", ErrValidation, i, err)
		}
	}
	if p.CreatedAt.IsZero() {
		return fmt.Errorf("%w: turn receipt phase.created_at is required", ErrValidation)
	}
	if p.Phase == TurnReceiptPhaseValidation {
		valid, ok := p.Payload["valid"].(bool)
		if !ok {
			return fmt.Errorf("%w: validation phase requires boolean valid", ErrValidation)
		}
		if !valid {
			code, _ := p.Payload["error_code"].(string)
			if !GovernanceErrorCode(code).Valid() {
				return fmt.Errorf("%w: validation phase requires a known error_code", ErrValidation)
			}
			path, _ := p.Payload["path"].(string)
			if err := validateText("turn receipt validation.path", path, 1024); err != nil {
				return err
			}
		}
	}
	if p.Phase == TurnReceiptPhasePlanCompile {
		if control, _ := p.Payload["control_outcome"].(bool); control {
			return nil
		}
		if p.PlanID == "" || p.Payload["plan_id"] != p.PlanID {
			return fmt.Errorf("%w: plan_compile phase requires matching plan_id", ErrValidation)
		}
		clientKey, _ := p.Payload["plan_client_key"].(string)
		decisionDigest, _ := p.Payload["decision_digest"].(string)
		if strings.TrimSpace(clientKey) == "" || !ValidCanonicalDigest(decisionDigest) {
			return fmt.Errorf("%w: plan_compile phase requires client key and decision digest", ErrValidation)
		}
	}
	if p.Phase == TurnReceiptPhaseDispatch {
		if control, _ := p.Payload["control_outcome"].(bool); control {
			if state, _ := p.Payload["dispatch_state"].(string); state != "no_runs" {
				return fmt.Errorf("%w: control dispatch phase must be no_runs", ErrValidation)
			}
			if count, ok := integralPayloadCount(p.Payload["run_count"]); !ok || count != 0 || len(p.RunIDs) != 0 {
				return fmt.Errorf("%w: control dispatch phase must contain no runs", ErrValidation)
			}
			return nil
		}
		if p.PlanID == "" || p.Payload["plan_id"] != p.PlanID {
			return fmt.Errorf("%w: dispatch phase requires matching plan_id", ErrValidation)
		}
		dispatchState, _ := p.Payload["dispatch_state"].(string)
		if dispatchState != "no_runs" && dispatchState != "committed" && dispatchState != "failed" {
			return fmt.Errorf("%w: dispatch phase has invalid dispatch_state", ErrValidation)
		}
		runCount, ok := integralPayloadCount(p.Payload["run_count"])
		if !ok || runCount != len(p.RunIDs) || (dispatchState == "no_runs" && runCount != 0) {
			return fmt.Errorf("%w: dispatch phase run_count does not match Run references", ErrValidation)
		}
	}
	return nil
}

func integralPayloadCount(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, typed >= 0
	case int64:
		return int(typed), typed >= 0
	case float64:
		if typed < 0 || typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

// TurnReceipt groups one immutable header with an append-only phase prefix.
// A partially settled receipt is valid because recovery may append the next
// phase later; gaps, reordering and identity changes are never valid.
type TurnReceipt struct {
	Header TurnReceiptHeader  `json:"header"`
	Phases []TurnReceiptPhase `json:"phases"`
}

func (r *TurnReceipt) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil turn receipt", ErrValidation)
	}
	if err := r.Header.Validate(); err != nil {
		return err
	}
	if len(r.Phases) > len(turnReceiptPhases) {
		return fmt.Errorf("%w: turn receipt has more than 7 phases", ErrValidation)
	}
	for i := range r.Phases {
		phase := &r.Phases[i]
		if !phase.TurnKey.Equal(r.Header.TurnKey) {
			return fmt.Errorf("%w: phase %d has a different turn identity", ErrValidation, i+1)
		}
		if phase.PhaseSeq != i+1 {
			return fmt.Errorf("%w: turn receipt phases must be contiguous from phase_seq 1", ErrValidation)
		}
		if err := phase.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ValidCanonicalDigest reports whether value has the exact lower-case
// sha256:<64 hexadecimal digits> shape. It does not verify a digest against a
// payload; canonicalization belongs to application/persistence.
func ValidCanonicalDigest(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") && isLowerHex(value[len("sha256:"):])
}

func ValidateCanonicalDigest(value string) error {
	if !ValidCanonicalDigest(value) {
		return fmt.Errorf("canonical digest must match sha256:<64 lowercase hexadecimal digits>")
	}
	return nil
}

func isLowerHex(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
