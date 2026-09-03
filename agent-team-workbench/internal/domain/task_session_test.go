package domain

import (
	"errors"
	"testing"
	"time"
)

func validProviderUsageAnchorForTest() *ProviderUsageAnchorV1 {
	value := int64(3)
	return &ProviderUsageAnchorV1{
		SchemaVersion: ProviderUsageAnchorSchemaVersionV1,
		State:         ProviderUsageAnchorReady, AdapterID: "mock", SessionRef: "mock://session",
		ContextGeneration: 1, SegmentSeq: 2,
		Counters:    UsageCountersV1{InputTokensTotal: &value},
		SourceRunID: "run_anchor", ObservedAt: time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestProviderUsageAnchorReadyAndInvalidatedContract(t *testing.T) {
	ready := validProviderUsageAnchorForTest()
	if err := ready.Validate(); err != nil {
		t.Fatal(err)
	}
	invalidated := *ready
	invalidated.State = ProviderUsageAnchorInvalidated
	invalidated.SessionRef = ""
	invalidated.Counters = UsageCountersV1{}
	invalidated.InvalidationReason = "provider session rotated"
	if err := invalidated.Validate(); err != nil {
		t.Fatalf("invalidated marker should validate: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*ProviderUsageAnchorV1)
	}{
		{"wrong schema", func(a *ProviderUsageAnchorV1) { a.SchemaVersion = "provider-usage-anchor/v2" }},
		{"ready without session", func(a *ProviderUsageAnchorV1) { a.SessionRef = "" }},
		{"ready without known counter", func(a *ProviderUsageAnchorV1) { a.Counters = UsageCountersV1{} }},
		{"invalidated retains counter", func(a *ProviderUsageAnchorV1) {
			a.State = ProviderUsageAnchorInvalidated
			a.InvalidationReason = "rotation"
		}},
		{"invalidated without reason", func(a *ProviderUsageAnchorV1) {
			a.State = ProviderUsageAnchorInvalidated
			a.Counters = UsageCountersV1{}
		}},
		{"segment zero", func(a *ProviderUsageAnchorV1) { a.SegmentSeq = 0 }},
		{"source missing", func(a *ProviderUsageAnchorV1) { a.SourceRunID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := *ready
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestProviderUsageAnchorAllowsPerKindWatermarkMerge(t *testing.T) {
	anchor := validProviderUsageAnchorForTest()
	anchor.Counters = UsageCountersV1{
		// These watermarks intentionally do not describe one provider snapshot:
		// the total is from an older observation while the input components have
		// already advanced independently.
		InputTokensTotal:    usageInt(100),
		InputUncachedTokens: usageInt(80),
		CacheReadTokens:     usageInt(30),
		CacheWriteTokens:    usageInt(10),
	}
	if err := anchor.Validate(); err != nil {
		t.Fatalf("per-kind merged anchor should validate: %v", err)
	}

	if err := anchor.Counters.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("strict usage counters must still reject the non-conserved merge: %v", err)
	}
}
