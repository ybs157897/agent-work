package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPriceSnapshotNormalizeComputesCanonicalDigestAndUTC(t *testing.T) {
	price := &PriceSnapshotRef{
		ModelRef:                        "model:test",
		Currency:                        "USD",
		InputUncachedMicroUSDPerMillion: 11,
		CacheReadMicroUSDPerMillion:     2,
		CacheWriteMicroUSDPerMillion:    3,
		OutputMicroUSDPerMillion:        19,
		EffectiveAt:                     time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
		PriceVersion:                    "price-v1",
	}
	if err := price.Normalize(); err != nil {
		t.Fatal(err)
	}
	if price.EffectiveAt.Location() != time.UTC || price.EffectiveAt.Hour() != 0 {
		t.Fatalf("effective_at must be normalized to UTC: %v", price.EffectiveAt)
	}
	if !ValidCanonicalDigest(price.Digest) {
		t.Fatalf("normalize must compute canonical digest: %q", price.Digest)
	}
	want, err := ComputePriceSnapshotDigest(price)
	if err != nil {
		t.Fatal(err)
	}
	if want != price.Digest {
		t.Fatalf("computed digest mismatch: want=%q got=%q", want, price.Digest)
	}
	if err := price.Validate(); err != nil {
		t.Fatalf("normalized price must validate: %v", err)
	}
}

func TestPriceSnapshotNormalizeRejectsTampering(t *testing.T) {
	price := validPriceSnapshotForTest(t)
	price.Digest = "sha256:" + strings.Repeat("0", 64)
	if err := price.Normalize(); !errors.Is(err, ErrValidation) {
		t.Fatalf("tampered digest must be rejected: %v", err)
	}

	price = validPriceSnapshotForTest(t)
	price.OutputMicroUSDPerMillion++
	if err := price.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("content mutation after digest must be rejected: %v", err)
	}
}

func TestPriceSnapshotDigestExcludesDigestAndNormalizesUTC(t *testing.T) {
	left := validPriceSnapshotForTest(t)
	right := *left
	right.Digest = ""
	// Express the same instant with a non-UTC offset and let Normalize restore
	// the canonical representation.
	right.EffectiveAt = time.Date(2026, 9, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if err := right.Normalize(); err != nil {
		t.Fatal(err)
	}
	if right.Digest != left.Digest || !right.EffectiveAt.Equal(left.EffectiveAt) || right.EffectiveAt.Location() != time.UTC {
		t.Fatalf("same instant with a different offset must canonicalize identically: left=%+v right=%+v", left, right)
	}
}

func validPriceSnapshotForTest(t *testing.T) *PriceSnapshotRef {
	t.Helper()
	price := &PriceSnapshotRef{
		ModelRef:                        "model:test",
		Currency:                        "USD",
		InputUncachedMicroUSDPerMillion: 11,
		CacheReadMicroUSDPerMillion:     2,
		CacheWriteMicroUSDPerMillion:    3,
		OutputMicroUSDPerMillion:        19,
		EffectiveAt:                     time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		PriceVersion:                    "price-v1",
	}
	if err := price.Normalize(); err != nil {
		t.Fatal(err)
	}
	return price
}
