package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func usageInt(value int64) *int64 { return &value }

func validUsageProvenance() UsageProvenanceV1 {
	return UsageProvenanceV1{
		AdapterID:       "codex-appserver",
		Protocol:        "codex-app-server",
		ProtocolVersion: "v2",
		Source:          "thread/tokenUsage/updated",
		ReportedBasis:   UsageBasisPerRun,
		AgentID:         "main",
		SessionRef:      "codex://thread_1",
		Mapping:         "last.inputTokens/cachedInputTokens/cacheWriteInputTokens/outputTokens",
	}
}

func validProviderUsageReport() ProviderUsageReportV1 {
	return ProviderUsageReportV1{
		SchemaVersion: ProviderUsageReportSchemaVersionV1,
		RunID:         "run_usage_1",
		Basis:         UsageBasisPerRun,
		Counters: UsageCountersV1{
			InputTokensTotal:    usageInt(150),
			InputUncachedTokens: usageInt(80),
			CacheReadTokens:     usageInt(50),
			CacheWriteTokens:    usageInt(20),
			OutputTokens:        usageInt(30),
		},
		Provenance: validUsageProvenance(),
	}
}

func TestUsageCountersV1DistinguishesUnknownFromZero(t *testing.T) {
	unknown := UsageCountersV1{}
	zero := UsageCountersV1{
		InputTokensTotal: usageInt(0), OutputTokens: usageInt(0),
	}
	if unknown.InputTokensTotal != nil || zero.InputTokensTotal == nil || *zero.InputTokensTotal != 0 {
		t.Fatalf("nullable counters must distinguish unknown from explicit zero: unknown=%+v zero=%+v", unknown, zero)
	}
	if err := unknown.Validate(); err != nil {
		t.Fatalf("unknown counters should be valid at the provider boundary: %v", err)
	}
	if unknown.AnyKnown() || !unknown.AllUnknown() {
		t.Fatalf("all-nil counters must be reported as all unknown: %+v", unknown)
	}
	if !zero.AnyKnown() || zero.AllUnknown() {
		t.Fatalf("explicit zero counters must be reported as known: %+v", zero)
	}
}

func TestUsageCountersV1UnknownSerializesAsAbsentKeys(t *testing.T) {
	// 0028's anchor triggers treat JSON null and a missing key differently
	// (SQLite json_type returns the text 'null' for an explicit null). Unknown
	// counters must therefore serialize as absent keys; an observed zero must
	// survive as a real number.
	raw, err := json.Marshal(UsageCountersV1{OutputTokens: usageInt(0)})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"input_tokens_total", "input_uncached_tokens", "cache_read_tokens", "cache_write_tokens"} {
		if _, present := doc[key]; present {
			t.Fatalf("unknown counter %q must be an absent key, got %s", key, doc[key])
		}
	}
	if got, ok := doc["output_tokens"]; !ok || string(got) != "0" {
		t.Fatalf("observed zero must serialize as 0: %s", got)
	}
	var roundtrip UsageCountersV1
	if err := json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.InputTokensTotal != nil || roundtrip.OutputTokens == nil || *roundtrip.OutputTokens != 0 {
		t.Fatalf("absent-key roundtrip must preserve unknown-vs-zero: %+v", roundtrip)
	}
}

func TestProviderUsageReportV1ValidateAndBasis(t *testing.T) {
	report := validProviderUsageReport()
	if err := report.Validate(); err != nil {
		t.Fatalf("valid provider report rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*ProviderUsageReportV1)
	}{
		{"missing schema", func(r *ProviderUsageReportV1) { r.SchemaVersion = "" }},
		{"wrong schema", func(r *ProviderUsageReportV1) { r.SchemaVersion = "provider-usage/v2" }},
		{"missing run", func(r *ProviderUsageReportV1) { r.RunID = "" }},
		{"unknown basis", func(r *ProviderUsageReportV1) { r.Basis = "unknown" }},
		{"provenance basis drift", func(r *ProviderUsageReportV1) { r.Provenance.ReportedBasis = UsageBasisSessionCumulative }},
		{"negative counter", func(r *ProviderUsageReportV1) { r.Counters.OutputTokens = usageInt(-1) }},
		{"input decomposition drift", func(r *ProviderUsageReportV1) { r.Counters.InputUncachedTokens = usageInt(81) }},
		{"missing adapter", func(r *ProviderUsageReportV1) { r.Provenance.AdapterID = "" }},
		{"missing source", func(r *ProviderUsageReportV1) { r.Provenance.Source = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := report
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestProviderUsageReportV1DigestIsStableAndVerified(t *testing.T) {
	report := validProviderUsageReport()
	if report.Digest != "" {
		t.Fatalf("fixture should start unsealed: %q", report.Digest)
	}
	if err := report.Seal(); err != nil {
		t.Fatalf("provider report seal failed: %v", err)
	}
	if !ValidCanonicalDigest(report.Digest) {
		t.Fatalf("provider report digest has invalid shape: %q", report.Digest)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("sealed provider report rejected: %v", err)
	}
	if err := report.VerifyDigest(); err != nil {
		t.Fatalf("provider report digest verification failed: %v", err)
	}

	mutated := report
	mutated.Counters.OutputTokens = usageInt(31)
	if err := mutated.VerifyDigest(); !errors.Is(err, ErrValidation) {
		t.Fatalf("mutating a sealed provider report must invalidate digest: %v", err)
	}
}

func TestCanonicalizeProviderUsageReportV1PerRun(t *testing.T) {
	report := validProviderUsageReport()
	usage, err := CanonicalizeProviderUsageReport(&report)
	if err != nil {
		t.Fatalf("canonicalization failed: %v", err)
	}
	if usage.SchemaVersion != CanonicalUsageSchemaVersionV1 || usage.RunID != report.RunID ||
		usage.Basis != UsageBasisPerRun || usage.Provenance.ReportedBasis != UsageBasisPerRun {
		t.Fatalf("canonical identity/basis mismatch: %+v", usage)
	}
	if usage.Counters.InputTokensTotal == nil || *usage.Counters.InputTokensTotal != 150 ||
		usage.Counters.InputUncachedTokens == nil || *usage.Counters.InputUncachedTokens != 80 ||
		usage.Counters.CacheReadTokens == nil || *usage.Counters.CacheReadTokens != 50 ||
		usage.Counters.CacheWriteTokens == nil || *usage.Counters.CacheWriteTokens != 20 ||
		usage.Counters.OutputTokens == nil || *usage.Counters.OutputTokens != 30 {
		t.Fatalf("canonical counters lost: %+v", usage.Counters)
	}
	if len(usage.ResolvedKinds) != 5 || len(usage.UnresolvedKinds) != 0 {
		t.Fatalf("fully reported counters should all resolve: resolved=%v unresolved=%v reason=%q",
			usage.ResolvedKinds, usage.UnresolvedKinds, usage.UnresolvedReason)
	}
	if usage.Digest == "" || !ValidCanonicalDigest(usage.Digest) {
		t.Fatalf("canonical digest missing/invalid: %q", usage.Digest)
	}
	if err := usage.Validate(); err != nil {
		t.Fatalf("sealed canonical usage rejected: %v", err)
	}
	if err := usage.VerifyDigest(); err != nil {
		t.Fatalf("canonical digest verification failed: %v", err)
	}

	mutated := *usage
	mutated.Counters.OutputTokens = usageInt(31)
	if err := mutated.VerifyDigest(); !errors.Is(err, ErrValidation) {
		t.Fatalf("mutating a sealed counter must invalidate digest: %v", err)
	}
}

func TestCanonicalizeProviderUsageReportV1PreservesUnknownCounters(t *testing.T) {
	report := validProviderUsageReport()
	report.Counters.CacheWriteTokens = nil
	report.Counters.OutputTokens = nil
	usage, err := CanonicalizeProviderUsageReport(&report)
	if err != nil {
		t.Fatalf("canonicalization failed: %v", err)
	}
	if usage.Counters.CacheWriteTokens != nil || usage.Counters.OutputTokens != nil {
		t.Fatalf("unknown provider counters must not become zero: %+v", usage.Counters)
	}
	if !containsQuotaKind(usage.UnresolvedKinds, QuotaCacheWriteTokens) ||
		!containsQuotaKind(usage.UnresolvedKinds, QuotaOutputTokens) {
		t.Fatalf("unknown counters must be marked unresolved: %+v", usage.UnresolvedKinds)
	}
	if strings.TrimSpace(usage.UnresolvedReason) == "" {
		t.Fatal("unresolved counters require an explanatory reason")
	}
	if err := usage.Validate(); err != nil {
		t.Fatalf("unknown counter canonical usage rejected: %v", err)
	}
}

func TestCanonicalizeProviderUsageReportV1RejectsCumulative(t *testing.T) {
	report := validProviderUsageReport()
	report.Basis = UsageBasisSessionCumulative
	report.Provenance.ReportedBasis = UsageBasisSessionCumulative
	if _, err := CanonicalizeProviderUsageReport(&report); !errors.Is(err, ErrValidation) {
		t.Fatalf("session cumulative report must wait for a persistent anchor: %v", err)
	}
}

func TestCanonicalUsageV1RejectsMalformedResolution(t *testing.T) {
	report := validProviderUsageReport()
	usage, err := CanonicalizeProviderUsageReport(&report)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		mut  func(*CanonicalUsageV1)
	}{
		{"missing digest", func(u *CanonicalUsageV1) { u.Digest = "" }},
		{"bad digest shape", func(u *CanonicalUsageV1) { u.Digest = "sha256:short" }},
		{"wrong basis", func(u *CanonicalUsageV1) { u.Basis = UsageBasisSessionCumulative }},
		{"counter without resolved kind", func(u *CanonicalUsageV1) { u.ResolvedKinds = u.ResolvedKinds[:4] }},
		{"unresolved without reason", func(u *CanonicalUsageV1) {
			u.Counters.OutputTokens = nil
			u.ResolvedKinds = removeQuotaKind(u.ResolvedKinds, QuotaOutputTokens)
			u.UnresolvedKinds = append(u.UnresolvedKinds, QuotaOutputTokens)
			u.UnresolvedReason = ""
			u.Digest = ""
			_ = u.Seal()
			u.UnresolvedReason = ""
		}},
		{"duplicate resolution", func(u *CanonicalUsageV1) {
			u.ResolvedKinds = append(u.ResolvedKinds, u.ResolvedKinds[0])
			u.Digest = ""
			_ = u.Seal()
		}},
		{"turn count is not usage", func(u *CanonicalUsageV1) {
			u.ResolvedKinds = append(u.ResolvedKinds, QuotaTurnCount)
			u.Digest = ""
			_ = u.Seal()
		}},
		{"input decomposition mismatch", func(u *CanonicalUsageV1) {
			u.Counters.CacheWriteTokens = usageInt(21)
			u.Digest = ""
			_ = u.Seal()
		}},
		{"negative cost", func(u *CanonicalUsageV1) {
			u.CostMicroUSD = usageInt(-1)
			u.ResolvedKinds = append(u.ResolvedKinds, QuotaCostMicroUSD)
			u.Digest = ""
			_ = u.Seal()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := *usage
			bad.ResolvedKinds = append([]QuotaKind(nil), usage.ResolvedKinds...)
			bad.UnresolvedKinds = append([]QuotaKind(nil), usage.UnresolvedKinds...)
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v (usage=%+v)", err, bad)
			}
		})
	}
}

func TestCanonicalUsageDigestIsStableAndRFC8785Backed(t *testing.T) {
	report := validProviderUsageReport()
	first, err := CanonicalizeProviderUsageReport(&report)
	if err != nil {
		t.Fatal(err)
	}
	report.Provenance.Mapping = "same semantics, different explanation"
	second, err := CanonicalizeProviderUsageReport(&report)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == second.Digest {
		t.Fatal("provenance is part of canonical usage identity; changing it must change digest")
	}

	clone := *first
	clone.Digest = ""
	got, err := clone.ComputeDigest()
	if err != nil || got != first.Digest {
		t.Fatalf("recomputing canonical digest must be deterministic: got=%q err=%v want=%q", got, err, first.Digest)
	}

	clone.Provenance.AnchorObservedAt = time.Time{}
	if err := clone.Seal(); err != nil {
		t.Fatalf("zero optional anchor timestamp should remain valid: %v", err)
	}
}

func removeQuotaKind(values []QuotaKind, wanted QuotaKind) []QuotaKind {
	out := make([]QuotaKind, 0, len(values))
	for _, value := range values {
		if value != wanted {
			out = append(out, value)
		}
	}
	return out
}

// P1-5 regression: when a total is reported but only some input components
// are known, the known components must not already exceed the total. The
// strict full-knowledge identity above this guard stays unchanged.
func TestUsageCountersV1RejectsTotalBelowKnownInputComponents(t *testing.T) {
	cases := []struct {
		name     string
		counters UsageCountersV1
		wantErr  bool
	}{
		{
			name: "total below known components rejects",
			counters: UsageCountersV1{
				InputTokensTotal:    usageInt(100),
				InputUncachedTokens: usageInt(60),
				CacheReadTokens:     usageInt(50),
			},
			wantErr: true,
		},
		{
			name: "total covering known components accepts",
			counters: UsageCountersV1{
				InputTokensTotal:    usageInt(110),
				InputUncachedTokens: usageInt(60),
				CacheReadTokens:     usageInt(50),
			},
		},
		{
			name: "unknown total skips the guard",
			counters: UsageCountersV1{
				InputUncachedTokens: usageInt(60),
				CacheReadTokens:     usageInt(50),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.counters.Validate()
			if tc.wantErr && !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}
