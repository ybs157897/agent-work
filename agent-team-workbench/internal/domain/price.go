package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gowebpki/jcs"
)

// priceSnapshotDigestPayload is the versioned, digest-bearing model price
// payload without its digest field. EffectiveAt is serialized explicitly in
// UTC so equivalent timestamps cannot produce different snapshot identities.
type priceSnapshotDigestPayload struct {
	ModelRef                        string `json:"model_ref"`
	Currency                        string `json:"currency"`
	InputUncachedMicroUSDPerMillion int64  `json:"input_uncached_microusd_per_million"`
	CacheReadMicroUSDPerMillion     int64  `json:"cache_read_microusd_per_million"`
	CacheWriteMicroUSDPerMillion    int64  `json:"cache_write_microusd_per_million"`
	OutputMicroUSDPerMillion        int64  `json:"output_microusd_per_million"`
	EffectiveAt                     string `json:"effective_at"`
	PriceVersion                    string `json:"price_version"`
}

// Normalize validates a price snapshot, normalizes its timestamp to UTC, and
// fills a missing digest. A supplied digest is verified rather than replaced,
// so content tampering cannot be hidden by a re-normalization pass.
func (p *PriceSnapshotRef) Normalize() error {
	if p == nil {
		return fmt.Errorf("%w: nil price snapshot", ErrValidation)
	}
	p.EffectiveAt = p.EffectiveAt.UTC()
	if err := validatePriceSnapshotFields(p); err != nil {
		return err
	}
	expected, err := ComputePriceSnapshotDigest(p)
	if err != nil {
		return err
	}
	if p.Digest != "" && p.Digest != expected {
		return fmt.Errorf("%w: price_snapshot.digest does not match canonical content", ErrValidation)
	}
	p.Digest = expected
	return nil
}

// ComputePriceSnapshotDigest computes SHA-256 over the RFC 8785 canonical JSON
// representation of the price snapshot excluding digest. It never trusts or
// includes the caller-supplied Digest value.
func ComputePriceSnapshotDigest(p *PriceSnapshotRef) (string, error) {
	if p == nil {
		return "", fmt.Errorf("%w: nil price snapshot", ErrValidation)
	}
	candidate := *p
	candidate.EffectiveAt = candidate.EffectiveAt.UTC()
	if err := validatePriceSnapshotFields(&candidate); err != nil {
		return "", err
	}
	payload := priceSnapshotDigestPayload{
		ModelRef:                        candidate.ModelRef,
		Currency:                        candidate.Currency,
		InputUncachedMicroUSDPerMillion: candidate.InputUncachedMicroUSDPerMillion,
		CacheReadMicroUSDPerMillion:     candidate.CacheReadMicroUSDPerMillion,
		CacheWriteMicroUSDPerMillion:    candidate.CacheWriteMicroUSDPerMillion,
		OutputMicroUSDPerMillion:        candidate.OutputMicroUSDPerMillion,
		EffectiveAt:                     candidate.EffectiveAt.Format(time.RFC3339Nano),
		PriceVersion:                    candidate.PriceVersion,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal price snapshot: %v", ErrValidation, err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("%w: RFC8785 canonicalize price snapshot: %v", ErrValidation, err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// VerifyPriceSnapshotDigest validates the snapshot and requires a digest that
// matches its canonical content. Callers loading external/config data should
// use Normalize instead when a missing digest may be generated.
func VerifyPriceSnapshotDigest(p *PriceSnapshotRef) error {
	if p == nil {
		return fmt.Errorf("%w: nil price snapshot", ErrValidation)
	}
	if err := validatePriceSnapshotFields(p); err != nil {
		return err
	}
	if !ValidCanonicalDigest(p.Digest) {
		return fmt.Errorf("%w: price_snapshot.digest must be a canonical sha256 digest", ErrValidation)
	}
	expected, err := ComputePriceSnapshotDigest(p)
	if err != nil {
		return err
	}
	if p.Digest != expected {
		return fmt.Errorf("%w: price_snapshot.digest does not match canonical content", ErrValidation)
	}
	return nil
}

// PriceSnapshotFromRunInput extracts the immutable price snapshot frozen into a
// Run input at creation. A Run without a price snapshot returns (nil, nil); a
// present but malformed or digest-tampered snapshot is a validation error.
func PriceSnapshotFromRunInput(input map[string]any) (*PriceSnapshotRef, error) {
	value, ok := input["price_snapshot"]
	if !ok || value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode Run price snapshot: %v", ErrValidation, err)
	}
	var price PriceSnapshotRef
	if err := json.Unmarshal(raw, &price); err != nil {
		return nil, fmt.Errorf("%w: decode Run price snapshot: %v", ErrValidation, err)
	}
	if err := price.Validate(); err != nil {
		return nil, err
	}
	return &price, nil
}

func validatePriceSnapshotFields(p *PriceSnapshotRef) error {
	if p == nil {
		return fmt.Errorf("%w: nil price snapshot", ErrValidation)
	}
	if err := validateText("price_snapshot.model_ref", p.ModelRef, 256); err != nil {
		return err
	}
	if p.Currency != "USD" {
		return fmt.Errorf("%w: price_snapshot.currency must be USD", ErrValidation)
	}
	for field, value := range map[string]int64{
		"input_uncached_microusd_per_million": p.InputUncachedMicroUSDPerMillion,
		"cache_read_microusd_per_million":     p.CacheReadMicroUSDPerMillion,
		"cache_write_microusd_per_million":    p.CacheWriteMicroUSDPerMillion,
		"output_microusd_per_million":         p.OutputMicroUSDPerMillion,
	} {
		if value < 0 {
			return fmt.Errorf("%w: price_snapshot.%s must be >= 0", ErrValidation, field)
		}
	}
	if err := validateText("price_snapshot.price_version", p.PriceVersion, 128); err != nil {
		return err
	}
	if p.EffectiveAt.IsZero() {
		return fmt.Errorf("%w: price_snapshot.effective_at is required", ErrValidation)
	}
	return nil
}
