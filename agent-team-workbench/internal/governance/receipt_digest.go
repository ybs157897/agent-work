// Package governance contains provider-neutral governance integrity helpers.
// It depends only on domain contracts and can be used by both Application and
// persistence without creating an import cycle.
package governance

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gowebpki/jcs"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

const placeholderDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

type headerDigestPayload struct {
	TurnKey             domain.TurnKey `json:"turn_key"`
	Attempt             int            `json:"attempt"`
	SchemaVersion       string         `json:"schema_version"`
	InputSnapshotDigest string         `json:"input_snapshot_digest"`
	AdmissionClientKey  string         `json:"admission_client_key"`
	GovernedSourceRunID string         `json:"source_run_id,omitempty"`
	PlanClientKey       string         `json:"plan_client_key,omitempty"`
	DecisionDigest      string         `json:"decision_digest,omitempty"`
	CreatedAt           string         `json:"created_at"`
}

type phaseDigestPayload struct {
	TurnKey              domain.TurnKey                  `json:"turn_key"`
	PhaseSeq             int                             `json:"phase_seq"`
	Phase                domain.TurnReceiptPhaseName     `json:"phase"`
	Payload              map[string]any                  `json:"payload"`
	PlanID               string                          `json:"plan_id"`
	RunIDs               []string                        `json:"run_ids"`
	QuotaReservationKeys []string                        `json:"quota_reservation_keys"`
	Evidence             []domain.GovernanceEvidenceItem `json:"evidence"`
	CreatedAt            string                          `json:"created_at"`
}

func ComputeHeaderDigest(header *domain.TurnReceiptHeader) (string, error) {
	if header == nil {
		return "", fmt.Errorf("%w: turn receipt header required", domain.ErrValidation)
	}
	candidate := *header
	candidate.CanonicalDigest = placeholderDigest
	if err := candidate.Validate(); err != nil {
		return "", err
	}
	return canonicalJSONDigest(headerDigestPayload{
		TurnKey:             header.TurnKey,
		Attempt:             header.Attempt,
		SchemaVersion:       header.SchemaVersion,
		InputSnapshotDigest: header.InputSnapshotDigest,
		AdmissionClientKey:  header.AdmissionClientKey,
		GovernedSourceRunID: header.GovernedSourceRunID,
		PlanClientKey:       header.PlanClientKey,
		DecisionDigest:      header.DecisionDigest,
		CreatedAt:           header.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

func ComputePhaseDigest(phase *domain.TurnReceiptPhase) (string, error) {
	if phase == nil {
		return "", fmt.Errorf("%w: turn receipt phase required", domain.ErrValidation)
	}
	candidate := *phase
	candidate.CanonicalDigest = placeholderDigest
	if err := candidate.Validate(); err != nil {
		return "", err
	}
	return canonicalJSONDigest(phaseDigestPayload{
		TurnKey:              phase.TurnKey,
		PhaseSeq:             phase.PhaseSeq,
		Phase:                phase.Phase,
		Payload:              phase.Payload,
		PlanID:               phase.PlanID,
		RunIDs:               append([]string{}, phase.RunIDs...),
		QuotaReservationKeys: append([]string{}, phase.QuotaReservationKeys...),
		Evidence:             append([]domain.GovernanceEvidenceItem{}, phase.Evidence...),
		CreatedAt:            phase.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

func VerifyHeaderDigest(header *domain.TurnReceiptHeader) error {
	computed, err := ComputeHeaderDigest(header)
	if err != nil {
		return err
	}
	if !sameDigest(computed, header.CanonicalDigest) {
		return fmt.Errorf("%w: turn receipt header canonical digest mismatch", domain.ErrValidation)
	}
	return nil
}

func VerifyPhaseDigest(phase *domain.TurnReceiptPhase) error {
	computed, err := ComputePhaseDigest(phase)
	if err != nil {
		return err
	}
	if !sameDigest(computed, phase.CanonicalDigest) {
		return fmt.Errorf("%w: turn receipt phase canonical digest mismatch", domain.ErrValidation)
	}
	return nil
}

func sameDigest(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func canonicalJSONDigest(value any) (string, error) {
	canonical, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal receipt digest payload: %w", err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, fmt.Errorf("RFC8785 canonicalize receipt payload: %w", err)
	}
	return canonical, nil
}
