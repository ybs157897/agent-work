package application

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

var canonicalUsageTestObservedAt = time.Date(2026, time.September, 1, 1, 2, 3, 0, time.UTC)

func usageTestPtr(value int64) *int64 { return &value }

func cumulativeUsageTestReport(counters domain.UsageCountersV1, sessionRef string) *domain.ProviderUsageReportV1 {
	report := &domain.ProviderUsageReportV1{
		SchemaVersion: domain.ProviderUsageReportSchemaVersionV1,
		RunID:         "run_usage_b2",
		Basis:         domain.UsageBasisSessionCumulative,
		Counters:      counters,
		Provenance: domain.UsageProvenanceV1{
			AdapterID:       "mock",
			Protocol:        "mock",
			ProtocolVersion: "1",
			Source:          "mock.usage",
			ReportedBasis:   domain.UsageBasisSessionCumulative,
			AgentID:         "agent_usage_b2",
			SessionRef:      sessionRef,
			Mapping:         "test cumulative buckets",
		},
	}
	if err := report.Seal(); err != nil {
		panic(err)
	}
	return report
}

func usageTestAnchor(counters domain.UsageCountersV1, sessionRef string, generation, segment int) *domain.ProviderUsageAnchorV1 {
	return &domain.ProviderUsageAnchorV1{
		SchemaVersion:     domain.ProviderUsageAnchorSchemaVersionV1,
		State:             domain.ProviderUsageAnchorReady,
		AdapterID:         "mock",
		SessionRef:        sessionRef,
		ContextGeneration: generation,
		SegmentSeq:        segment,
		Counters:          counters,
		SourceRunID:       "run_usage_b2_anchor",
		ObservedAt:        canonicalUsageTestObservedAt,
	}
}

func usageTestRequest(report *domain.ProviderUsageReportV1, anchor *domain.ProviderUsageAnchorV1) CanonicalUsageRequestV1 {
	return CanonicalUsageRequestV1{
		Report:            report,
		Anchor:            anchor,
		RunID:             "run_usage_b2",
		AgentID:           "agent_usage_b2",
		AdapterID:         "mock",
		ContextGeneration: 3,
		SegmentSeq:        4,
		ObservedAt:        canonicalUsageTestObservedAt,
	}
}

func fullUsageCounters(total, uncached, read, write, output int64) domain.UsageCountersV1 {
	return domain.UsageCountersV1{
		InputTokensTotal:    usageTestPtr(total),
		InputUncachedTokens: usageTestPtr(uncached),
		CacheReadTokens:     usageTestPtr(read),
		CacheWriteTokens:    usageTestPtr(write),
		OutputTokens:        usageTestPtr(output),
	}
}

func assertUsageCounter(t *testing.T, value *int64, want int64) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("counter=%v, want %d", value, want)
	}
}

func assertKinds(t *testing.T, got []domain.QuotaKind, want ...domain.QuotaKind) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("kinds=%v, want %v", got, want)
	}
	for i, kind := range want {
		if got[i] != kind {
			t.Fatalf("kinds[%d]=%q, want %q (all=%v)", i, got[i], kind, got)
		}
	}
}

func TestCanonicalizeProviderUsageV1PerRunBindsIdentity(t *testing.T) {
	report := cumulativeUsageTestReport(fullUsageCounters(10, 4, 3, 3, 2), "mock://session")
	report.Basis = domain.UsageBasisPerRun
	report.Provenance.ReportedBasis = domain.UsageBasisPerRun
	if err := report.Seal(); err != nil {
		t.Fatal(err)
	}
	req := usageTestRequest(report, nil)
	result, err := CanonicalizeProviderUsageV1(req)
	if err != nil {
		t.Fatalf("per-run canonicalization failed: %v", err)
	}
	if result.NextAnchor != nil {
		t.Fatalf("per-run usage must not create cumulative anchor: %+v", result.NextAnchor)
	}
	if result.Canonical == nil || result.Canonical.RunID != req.RunID {
		t.Fatalf("canonical run identity not bound: %+v", result.Canonical)
	}
	if err := result.Canonical.Validate(); err != nil {
		t.Fatalf("canonical usage invalid: %v", err)
	}
}

func TestCanonicalizeProviderUsageV1SameRefDeltaAndNextAnchor(t *testing.T) {
	report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://same")
	anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://same", 3, 4)
	req := usageTestRequest(report, anchor)
	result, err := CanonicalizeProviderUsageV1(req)
	if err != nil {
		t.Fatalf("same-ref delta failed: %v", err)
	}
	if result.Canonical == nil || result.NextAnchor == nil {
		t.Fatalf("same-ref delta must return canonical and next anchor: %+v", result)
	}
	assertUsageCounter(t, result.Canonical.Counters.InputTokensTotal, 50)
	assertUsageCounter(t, result.Canonical.Counters.InputUncachedTokens, 30)
	assertUsageCounter(t, result.Canonical.Counters.CacheReadTokens, 10)
	assertUsageCounter(t, result.Canonical.Counters.CacheWriteTokens, 10)
	assertUsageCounter(t, result.Canonical.Counters.OutputTokens, 15)
	assertKinds(t, result.Canonical.ResolvedKinds,
		domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
		domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens, domain.QuotaOutputTokens)
	if len(result.Canonical.UnresolvedKinds) != 0 {
		t.Fatalf("same-ref delta unexpectedly unresolved: %+v", result.Canonical.UnresolvedKinds)
	}
	if result.Canonical.Provenance.AnchorBefore == nil || result.Canonical.Provenance.AnchorAfter == nil {
		t.Fatalf("anchor before/after provenance missing: %+v", result.Canonical.Provenance)
	}
	if !result.Canonical.Provenance.AnchorBefore.Equal(anchor.Counters) ||
		!result.Canonical.Provenance.AnchorAfter.Equal(report.Counters) {
		t.Fatalf("anchor provenance mismatch: %+v", result.Canonical.Provenance)
	}
	if result.NextAnchor.SessionRef != "mock://same" || result.NextAnchor.ContextGeneration != 3 || result.NextAnchor.SegmentSeq != 4 {
		t.Fatalf("next anchor identity mismatch: %+v", result.NextAnchor)
	}
	if err := result.NextAnchor.Validate(); err != nil {
		t.Fatalf("next anchor invalid: %v", err)
	}
	if err := result.Canonical.Validate(); err != nil {
		t.Fatalf("sealed delta canonical invalid: %v", err)
	}
}

func TestCanonicalizeProviderUsageV1FirstReportDoesNotAssumeZero(t *testing.T) {
	report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://first")
	result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, nil))
	if err != nil {
		t.Fatalf("first cumulative report failed: %v", err)
	}
	if result.Canonical == nil || result.NextAnchor == nil {
		t.Fatalf("first cumulative report should produce unresolved canonical plus baseline: %+v", result)
	}
	if result.Canonical.Counters.AnyKnown() {
		t.Fatalf("missing baseline must not treat cumulative values as per-run zero delta: %+v", result.Canonical.Counters)
	}
	assertKinds(t, result.Canonical.UnresolvedKinds,
		domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
		domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens, domain.QuotaOutputTokens)
	if !strings.Contains(result.Canonical.UnresolvedReason, "anchor is unavailable") {
		t.Fatalf("unresolved reason should explain missing anchor: %q", result.Canonical.UnresolvedReason)
	}
}

func TestCanonicalizeProviderUsageV1FreshProviderSessionUsesExplicitZero(t *testing.T) {
	report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://fresh")
	req := usageTestRequest(report, nil)
	req.FreshProviderSession = true
	result, err := CanonicalizeProviderUsageV1(req)
	if err != nil {
		t.Fatalf("fresh cumulative report failed: %v", err)
	}
	if result.Canonical == nil || result.NextAnchor == nil {
		t.Fatalf("fresh cumulative report must return canonical and anchor: %+v", result)
	}
	assertUsageCounter(t, result.Canonical.Counters.InputTokensTotal, 150)
	assertUsageCounter(t, result.Canonical.Counters.InputUncachedTokens, 100)
	assertUsageCounter(t, result.Canonical.Counters.CacheReadTokens, 30)
	assertUsageCounter(t, result.Canonical.Counters.CacheWriteTokens, 20)
	assertUsageCounter(t, result.Canonical.Counters.OutputTokens, 40)
	if result.Canonical.Provenance.AnchorBefore == nil {
		t.Fatal("fresh baseline provenance missing")
	}
	for name, value := range map[string]*int64{
		"input_total":    result.Canonical.Provenance.AnchorBefore.InputTokensTotal,
		"input_uncached": result.Canonical.Provenance.AnchorBefore.InputUncachedTokens,
		"cache_read":     result.Canonical.Provenance.AnchorBefore.CacheReadTokens,
		"cache_write":    result.Canonical.Provenance.AnchorBefore.CacheWriteTokens,
		"output":         result.Canonical.Provenance.AnchorBefore.OutputTokens,
	} {
		if value == nil || *value != 0 {
			t.Fatalf("fresh baseline %s must be explicit zero: %+v", name, result.Canonical.Provenance.AnchorBefore)
		}
	}
}

func TestCanonicalizeProviderUsageV1DuplicateIsResolvedZeroDelta(t *testing.T) {
	report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://duplicate")
	anchor := usageTestAnchor(report.Counters.Clone(), "mock://duplicate", 3, 4)
	result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
	if err != nil {
		t.Fatalf("duplicate cumulative report failed: %v", err)
	}
	if result.Canonical == nil || len(result.Canonical.UnresolvedKinds) != 0 {
		t.Fatalf("duplicate report should resolve zero delta: %+v", result.Canonical)
	}
	assertUsageCounter(t, result.Canonical.Counters.InputTokensTotal, 0)
	assertUsageCounter(t, result.Canonical.Counters.InputUncachedTokens, 0)
	assertUsageCounter(t, result.Canonical.Counters.CacheReadTokens, 0)
	assertUsageCounter(t, result.Canonical.Counters.CacheWriteTokens, 0)
	assertUsageCounter(t, result.Canonical.Counters.OutputTokens, 0)
}

func TestCanonicalizeProviderUsageV1CounterDecreaseIsPerKindUnresolved(t *testing.T) {
	// 只有 output 回退；input 四个累计水位仍然单调，因此合并后的 anchor
	// 仍是一个合法的最新水位，不能因单个 kind 失败而整体丢弃。
	report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 15), "mock://decrease")
	anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://decrease", 3, 4)
	result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
	if err != nil {
		t.Fatalf("decrease report failed: %v", err)
	}
	if result.Canonical == nil {
		t.Fatal("missing canonical result")
	}
	if result.NextAnchor == nil {
		t.Fatal("healthy counters must advance a merged anchor despite one regressed kind")
	}
	expectedAnchor := fullUsageCounters(150, 100, 30, 20, 25)
	if !result.NextAnchor.Counters.Equal(expectedAnchor) {
		t.Fatalf("merged anchor must preserve the regressed output watermark: got=%+v want=%+v",
			result.NextAnchor.Counters, expectedAnchor)
	}
	assertKinds(t, result.Canonical.UnresolvedKinds,
		domain.QuotaOutputTokens)
	assertUsageCounter(t, result.Canonical.Counters.InputTokensTotal, 50)
	assertUsageCounter(t, result.Canonical.Counters.InputUncachedTokens, 30)
	assertUsageCounter(t, result.Canonical.Counters.CacheReadTokens, 10)
	assertUsageCounter(t, result.Canonical.Counters.CacheWriteTokens, 10)
	if result.Canonical.Counters.OutputTokens != nil {
		t.Fatalf("decreased output must remain unresolved: %+v", result.Canonical.Counters)
	}
	if !strings.Contains(result.Canonical.UnresolvedReason, "regressed") {
		t.Fatalf("decrease reason missing: %q", result.Canonical.UnresolvedReason)
	}
}

func TestCanonicalizeProviderUsageV1IdentityCoordinatesForceUnresolved(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CanonicalUsageRequestV1)
		reason string
	}{
		{"ref", func(req *CanonicalUsageRequestV1) {
			req.Report.Provenance.SessionRef = "mock://new"
			_ = req.Report.Seal()
		}, "session identity changed"},
		{"generation", func(req *CanonicalUsageRequestV1) { req.ContextGeneration++ }, "context generation changed"},
		{"segment", func(req *CanonicalUsageRequestV1) { req.SegmentSeq++ }, "segment changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://coord")
			anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://coord", 3, 4)
			req := usageTestRequest(report, anchor)
			tc.mutate(&req)
			result, err := CanonicalizeProviderUsageV1(req)
			if err != nil {
				t.Fatalf("coordinate mismatch should be an unresolved decision: %v", err)
			}
			if result.Canonical == nil || result.Canonical.Counters.AnyKnown() {
				t.Fatalf("coordinate mismatch must not produce zero delta: %+v", result.Canonical)
			}
			if !strings.Contains(result.Canonical.UnresolvedReason, tc.reason) {
				t.Fatalf("reason=%q, want %q", result.Canonical.UnresolvedReason, tc.reason)
			}
		})
	}
}

func TestCanonicalizeProviderUsageV1InvalidatedNeedsExplicitFresh(t *testing.T) {
	report := cumulativeUsageTestReport(fullUsageCounters(10, 4, 3, 3, 2), "mock://rotated")
	anchor := usageTestAnchor(fullUsageCounters(10, 4, 3, 3, 2), "mock://old", 3, 4)
	anchor.State = domain.ProviderUsageAnchorInvalidated
	anchor.SessionRef = ""
	anchor.Counters = domain.UsageCountersV1{}
	anchor.InvalidationReason = "provider session lost"
	if err := anchor.Validate(); err != nil {
		t.Fatal(err)
	}
	req := usageTestRequest(report, anchor)
	withoutFresh, err := CanonicalizeProviderUsageV1(req)
	if err != nil {
		t.Fatal(err)
	}
	if withoutFresh.Canonical.Counters.AnyKnown() {
		t.Fatalf("invalidated anchor without fresh must remain unresolved: %+v", withoutFresh.Canonical)
	}
	req.FreshProviderSession = true
	withFresh, err := CanonicalizeProviderUsageV1(req)
	if err != nil {
		t.Fatal(err)
	}
	assertUsageCounter(t, withFresh.Canonical.Counters.InputTokensTotal, 10)
}

func TestCanonicalizeProviderUsageV1PartialBucketsRemainNullable(t *testing.T) {
	reportCounters := fullUsageCounters(150, 100, 30, 20, 40)
	reportCounters.CacheWriteTokens = nil
	report := cumulativeUsageTestReport(reportCounters, "mock://partial")
	anchorCounters := fullUsageCounters(100, 70, 20, 10, 25)
	anchorCounters.CacheWriteTokens = nil
	anchor := usageTestAnchor(anchorCounters, "mock://partial", 3, 4)
	result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
	if err != nil {
		t.Fatalf("partial cumulative report failed: %v", err)
	}
	assertUsageCounter(t, result.Canonical.Counters.InputTokensTotal, 50)
	assertUsageCounter(t, result.Canonical.Counters.InputUncachedTokens, 30)
	assertUsageCounter(t, result.Canonical.Counters.CacheReadTokens, 10)
	if result.Canonical.Counters.CacheWriteTokens != nil {
		t.Fatalf("unknown cache-write must not become zero: %+v", result.Canonical.Counters)
	}
	assertUsageCounter(t, result.Canonical.Counters.OutputTokens, 15)
	assertKinds(t, result.Canonical.UnresolvedKinds, domain.QuotaCacheWriteTokens)
}

func TestCanonicalizeProviderUsageV1RejectsDigestAndIdentityMismatch(t *testing.T) {
	identityCases := []struct {
		name   string
		mutate func(*CanonicalUsageRequestV1)
	}{
		{"run", func(req *CanonicalUsageRequestV1) { req.RunID = "run_other" }},
		{"agent", func(req *CanonicalUsageRequestV1) { req.AgentID = "agent_other" }},
		{"adapter", func(req *CanonicalUsageRequestV1) { req.AdapterID = "other" }},
	}
	for _, tc := range identityCases {
		t.Run(tc.name, func(t *testing.T) {
			report := cumulativeUsageTestReport(fullUsageCounters(10, 4, 3, 3, 2), "mock://identity")
			req := usageTestRequest(report, nil)
			tc.mutate(&req)
			if _, err := CanonicalizeProviderUsageV1(req); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("identity mismatch should reject with ErrValidation: %v", err)
			}
		})
	}

	report := cumulativeUsageTestReport(fullUsageCounters(10, 4, 3, 3, 2), "mock://digest")
	report.Counters.OutputTokens = usageTestPtr(3)
	if _, err := CanonicalizeProviderUsageV1(usageTestRequest(report, nil)); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("mutated report digest should reject: %v", err)
	}

	badCounter := cumulativeUsageTestReport(fullUsageCounters(math.MaxInt64, math.MaxInt64-2, 1, 1, 0), "mock://overflow")
	badCounter.Counters.InputUncachedTokens = usageTestPtr(-1)
	if _, err := CanonicalizeProviderUsageV1(usageTestRequest(badCounter, nil)); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("negative/overflow-shaped counter should reject: %v", err)
	}
}

// 复审裁决：NextAnchor 在同一 provider 身份下按 counter 独立合并。
// 回退或覆盖缺口的 kind 保留旧水位，健康 kind 继续推进；身份不兼容、
// invalidated 仍不推进。fresh 零基线与「从未有基线」例外保持原语义。
func TestCanonicalizeProviderUsageV1AnchorAdvancementGuard(t *testing.T) {
	t.Run("non-owner cumulative report stays unresolved and cannot advance", func(t *testing.T) {
		report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://owner-guard")
		anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://owner-guard", 3, 4)
		req := usageTestRequest(report, anchor)
		req.AnchorOwnershipKnown = true
		req.AnchorOwnerRunID = "run_new_owner"
		req.AnchorOwnerSequence = 2
		result, err := CanonicalizeProviderUsageV1(req)
		if err != nil {
			t.Fatal(err)
		}
		if result.NextAnchor != nil || result.Canonical.Counters.AnyKnown() {
			t.Fatalf("non-owner report must not use or advance the current anchor: %+v", result)
		}
		if !strings.Contains(result.Canonical.UnresolvedReason, "no longer the current anchor owner") {
			t.Fatalf("owner mismatch reason missing: %q", result.Canonical.UnresolvedReason)
		}
	})

	t.Run("regression preserves failed kind and advances healthy kinds", func(t *testing.T) {
		report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 15), "mock://guard-decrease")
		anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://guard-decrease", 3, 4)
		result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextAnchor == nil {
			t.Fatal("healthy counters must keep advancing the merged anchor")
		}
		expected := fullUsageCounters(150, 100, 30, 20, 25)
		if !result.NextAnchor.Counters.Equal(expected) {
			t.Fatalf("merged anchor mismatch: got=%+v want=%+v", result.NextAnchor.Counters, expected)
		}
		if result.Canonical.Provenance.AnchorAfter == nil ||
			!result.Canonical.Provenance.AnchorAfter.Equal(expected) {
			t.Fatalf("anchor_after must expose merged watermarks: %+v", result.Canonical.Provenance)
		}
	})

	t.Run("coverage gap preserves missing kind and advances known kinds", func(t *testing.T) {
		reportCounters := fullUsageCounters(140, 100, 30, 20, 40)
		reportCounters.CacheReadTokens = nil
		report := cumulativeUsageTestReport(reportCounters, "mock://guard-gap")
		anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://guard-gap", 3, 4)
		result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextAnchor == nil {
			t.Fatal("known counters must advance while the missing component keeps its old watermark")
		}
		expected := fullUsageCounters(140, 100, 20, 20, 40)
		if !result.NextAnchor.Counters.Equal(expected) {
			t.Fatalf("coverage-gap merge mismatch: got=%+v want=%+v", result.NextAnchor.Counters, expected)
		}
		assertKinds(t, result.Canonical.UnresolvedKinds, domain.QuotaCacheReadTokens)
	})

	t.Run("compatible monotonic report advances to report watermark", func(t *testing.T) {
		report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://guard-up")
		anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://guard-up", 3, 4)
		result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextAnchor == nil {
			t.Fatal("monotonic compatible report must advance the anchor")
		}
		if !result.NextAnchor.Counters.Equal(report.Counters) {
			t.Fatalf("advanced anchor must equal the report watermark: %+v vs %+v",
				result.NextAnchor.Counters, report.Counters)
		}
	})

	t.Run("nil anchor advances even without fresh", func(t *testing.T) {
		report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://guard-first")
		result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, nil))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextAnchor == nil {
			t.Fatal("first observed watermark must become the anchor even without fresh")
		}
		if !result.NextAnchor.Counters.Equal(report.Counters) {
			t.Fatalf("seeded anchor must equal the report watermark: %+v", result.NextAnchor.Counters)
		}
	})

	t.Run("incompatible identity does not advance", func(t *testing.T) {
		report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://guard-new")
		anchor := usageTestAnchor(fullUsageCounters(100, 70, 20, 10, 25), "mock://guard-old", 3, 4)
		result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextAnchor != nil {
			t.Fatalf("session identity change must not advance the anchor: %+v", result.NextAnchor)
		}
	})

	t.Run("invalidated anchor does not advance without fresh", func(t *testing.T) {
		report := cumulativeUsageTestReport(fullUsageCounters(150, 100, 30, 20, 40), "mock://guard-invalidated")
		anchor := usageTestAnchor(domain.UsageCountersV1{}, "", 3, 4)
		anchor.State = domain.ProviderUsageAnchorInvalidated
		anchor.InvalidationReason = "provider session lost"
		if err := anchor.Validate(); err != nil {
			t.Fatal(err)
		}
		result, err := CanonicalizeProviderUsageV1(usageTestRequest(report, anchor))
		if err != nil {
			t.Fatal(err)
		}
		if result.NextAnchor != nil {
			t.Fatalf("invalidated anchor must not advance without explicit fresh: %+v", result.NextAnchor)
		}
	})
}
