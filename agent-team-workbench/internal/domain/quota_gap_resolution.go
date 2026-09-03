package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

// QuotaGapResolutionSchemaVersion identifies the immutable reconciliation
// record shape. A resolution is an adjudication of one existing unresolved
// spend entry, not a replacement usage ledger.
const QuotaGapResolutionSchemaVersion = "quota-gap-resolution/v1"

type QuotaGapResolutionStatus string

const (
	// QuotaGapResolutionReconciled is the only v1 outcome. Waivers are
	// intentionally not represented: unknown usage must not silently become an
	// admission bypass.
	QuotaGapResolutionReconciled QuotaGapResolutionStatus = "reconciled"
)

func (s QuotaGapResolutionStatus) Valid() bool { return s == QuotaGapResolutionReconciled }

type QuotaGapResolutionActorKind string

const QuotaGapResolutionActorUser QuotaGapResolutionActorKind = "user"

func (k QuotaGapResolutionActorKind) Valid() bool { return k == QuotaGapResolutionActorUser }

// QuotaGapResolution is an immutable operator reconciliation for one
// unresolved quota spend. The original spend row remains unresolved and
// append-only; Amount is an additive, explicitly adjudicated adjustment used
// by future quota admission.
type QuotaGapResolution struct {
	ID                   string                      `json:"id"`
	SchemaVersion        string                      `json:"schema_version"`
	Target               QuotaSpendKey               `json:"target"`
	OriginalUsageDigest  string                      `json:"original_usage_digest"`
	OriginalPolicyDigest string                      `json:"original_policy_digest"`
	OriginalPriceDigest  string                      `json:"original_price_digest,omitempty"`
	Status               QuotaGapResolutionStatus    `json:"status"`
	Amount               int64                       `json:"amount"`
	Evidence             GovernanceEvidenceItem      `json:"evidence"`
	EvidenceDigest       string                      `json:"evidence_digest"`
	CanonicalDigest      string                      `json:"canonical_digest"`
	ActorKind            QuotaGapResolutionActorKind `json:"actor_kind"`
	ActorID              string                      `json:"actor_id"`
	Reason               string                      `json:"reason"`
	ClientKey            string                      `json:"client_key,omitempty"`
	CreatedAt            time.Time                   `json:"created_at"`
}

// ComputeGovernanceEvidenceDigest seals the exact evidence annotation used by
// a quota reconciliation. The timestamp is normalized to UTC and no mutable
// source content is included.
func ComputeGovernanceEvidenceDigest(e GovernanceEvidenceItem) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	payload := struct {
		SourceKind   GovernanceEvidenceSourceKind   `json:"source_kind"`
		SourceID     string                         `json:"source_id"`
		Verification GovernanceEvidenceVerification `json:"verification"`
		Summary      string                         `json:"summary"`
		RecordedAt   string                         `json:"recorded_at"`
	}{
		SourceKind: e.SourceKind, SourceID: e.SourceID, Verification: e.Verification,
		Summary: e.Summary, RecordedAt: e.RecordedAt.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal governance evidence digest: %v", ErrValidation, err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize governance evidence digest: %v", ErrValidation, err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (r *QuotaGapResolution) Validate() error {
	if r == nil {
		return fmt.Errorf("%w: nil quota gap resolution", ErrValidation)
	}
	if err := validateTypedID("quota_gap_resolution.id", r.ID, PrefixQuotaGapResolution); err != nil {
		return err
	}
	if r.SchemaVersion != QuotaGapResolutionSchemaVersion {
		return fmt.Errorf("%w: quota_gap_resolution.schema_version %q", ErrValidation, r.SchemaVersion)
	}
	if err := r.Target.Validate(); err != nil {
		return fmt.Errorf("%w: quota_gap_resolution.target: %v", ErrValidation, err)
	}
	if err := ValidateCanonicalDigest(r.OriginalUsageDigest); err != nil {
		return fmt.Errorf("%w: quota_gap_resolution.original_usage_digest: %v", ErrValidation, err)
	}
	if err := ValidateCanonicalDigest(r.OriginalPolicyDigest); err != nil {
		return fmt.Errorf("%w: quota_gap_resolution.original_policy_digest: %v", ErrValidation, err)
	}
	if r.Target.Kind == QuotaCostMicroUSD {
		if err := ValidateCanonicalDigest(r.OriginalPriceDigest); err != nil {
			return fmt.Errorf("%w: quota_gap_resolution.original_price_digest: %v", ErrValidation, err)
		}
	} else if r.OriginalPriceDigest != "" {
		return fmt.Errorf("%w: quota_gap_resolution.original_price_digest only applies to cost", ErrValidation)
	}
	if r.Status != QuotaGapResolutionReconciled {
		return fmt.Errorf("%w: quota_gap_resolution.status %q", ErrValidation, r.Status)
	}
	if r.Amount < 0 {
		return fmt.Errorf("%w: quota_gap_resolution.amount must be >= 0", ErrValidation)
	}
	if r.Evidence.Verification != EvidenceVerificationPassed &&
		r.Evidence.Verification != EvidenceVerificationAccepted {
		return fmt.Errorf("%w: quota_gap_resolution.evidence must be passed or accepted", ErrValidation)
	}
	if err := r.Evidence.Validate(); err != nil {
		return fmt.Errorf("%w: quota_gap_resolution.evidence: %v", ErrValidation, err)
	}
	if !ValidCanonicalDigest(r.EvidenceDigest) {
		return fmt.Errorf("%w: quota_gap_resolution.evidence_digest must be a canonical sha256 digest", ErrValidation)
	}
	wantEvidenceDigest, err := ComputeGovernanceEvidenceDigest(r.Evidence)
	if err != nil {
		return err
	}
	if wantEvidenceDigest != r.EvidenceDigest {
		return fmt.Errorf("%w: quota_gap_resolution.evidence_digest does not match evidence", ErrValidation)
	}
	if !ValidCanonicalDigest(r.CanonicalDigest) {
		return fmt.Errorf("%w: quota_gap_resolution.canonical_digest must be a canonical sha256 digest", ErrValidation)
	}
	wantDigest, err := ComputeQuotaGapResolutionDigest(r)
	if err != nil {
		return err
	}
	if wantDigest != r.CanonicalDigest {
		return fmt.Errorf("%w: quota_gap_resolution.canonical_digest does not match immutable content", ErrValidation)
	}
	if !r.ActorKind.Valid() {
		return fmt.Errorf("%w: quota_gap_resolution.actor_kind %q", ErrValidation, r.ActorKind)
	}
	if err := validateText("quota_gap_resolution.actor_id", r.ActorID, 256); err != nil {
		return err
	}
	if err := validateText("quota_gap_resolution.reason", r.Reason, 4000); err != nil {
		return err
	}
	if r.ClientKey != "" && (strings.TrimSpace(r.ClientKey) != r.ClientKey || len(r.ClientKey) > 256) {
		return fmt.Errorf("%w: quota_gap_resolution.client_key must be 1..256 trimmed bytes", ErrValidation)
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("%w: quota_gap_resolution.created_at is required", ErrValidation)
	}
	return nil
}

// Seal fills and verifies the resolution's immutable-content digest. As with
// canonical usage and receipt records, callers supplying an existing digest
// must match it rather than have it silently replaced.
func (r *QuotaGapResolution) Seal() error {
	if r == nil {
		return fmt.Errorf("%w: nil quota gap resolution", ErrValidation)
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	} else {
		r.CreatedAt = r.CreatedAt.UTC()
	}
	if r.EvidenceDigest == "" {
		digest, err := ComputeGovernanceEvidenceDigest(r.Evidence)
		if err != nil {
			return err
		}
		r.EvidenceDigest = digest
	}
	digest, err := ComputeQuotaGapResolutionDigest(r)
	if err != nil {
		return err
	}
	if r.CanonicalDigest != "" && r.CanonicalDigest != digest {
		return fmt.Errorf("%w: quota_gap_resolution.canonical_digest does not match immutable content", ErrValidation)
	}
	r.CanonicalDigest = digest
	return r.Validate()
}

// ComputeQuotaGapResolutionDigest hashes all immutable resolution content,
// including the evidence digest, adjudicated amount and creation timestamp.
// It intentionally excludes only CanonicalDigest itself.
func ComputeQuotaGapResolutionDigest(r *QuotaGapResolution) (string, error) {
	if r == nil {
		return "", fmt.Errorf("%w: nil quota gap resolution", ErrValidation)
	}
	payload := struct {
		SchemaVersion        string                      `json:"schema_version"`
		Target               QuotaSpendKey               `json:"target"`
		OriginalUsageDigest  string                      `json:"original_usage_digest"`
		OriginalPolicyDigest string                      `json:"original_policy_digest"`
		OriginalPriceDigest  string                      `json:"original_price_digest,omitempty"`
		Status               QuotaGapResolutionStatus    `json:"status"`
		Amount               int64                       `json:"amount"`
		EvidenceDigest       string                      `json:"evidence_digest"`
		ActorKind            QuotaGapResolutionActorKind `json:"actor_kind"`
		ActorID              string                      `json:"actor_id"`
		Reason               string                      `json:"reason"`
		ClientKey            string                      `json:"client_key,omitempty"`
		CreatedAt            string                      `json:"created_at"`
	}{
		SchemaVersion: r.SchemaVersion, Target: r.Target,
		OriginalUsageDigest: r.OriginalUsageDigest, OriginalPolicyDigest: r.OriginalPolicyDigest,
		OriginalPriceDigest: r.OriginalPriceDigest, Status: r.Status, Amount: r.Amount,
		EvidenceDigest: r.EvidenceDigest, ActorKind: r.ActorKind, ActorID: r.ActorID,
		Reason: r.Reason, ClientKey: r.ClientKey, CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal quota gap resolution digest: %v", ErrValidation, err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize quota gap resolution digest: %v", ErrValidation, err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
