package application

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// CanonicalUsageRequestV1 carries the control-plane identity and the durable
// provider-session coordinates needed to turn one provider report into a
// per-run accounting value. It is intentionally a value-only request: the
// caller owns persistence and decides when NextAnchor is committed.
type CanonicalUsageRequestV1 struct {
	Report *domain.ProviderUsageReportV1
	Anchor *domain.ProviderUsageAnchorV1

	RunID     string
	AgentID   string
	AdapterID string

	// SessionRef is an optional control-plane expectation. When supplied it
	// must equal the report's provider session ref; the report ref is otherwise
	// used as the observed identity.
	SessionRef string

	ContextGeneration int
	SegmentSeq        int
	// AnchorOwnershipKnown is set by the persistence owner. Pure callers may
	// omit it, but an application Run must prove that it still owns the
	// TaskSession anchor before using or advancing a cumulative baseline.
	AnchorOwnershipKnown bool
	AnchorOwnerRunID     string
	AnchorOwnerSequence  int64

	// A fresh provider session explicitly permits a zero baseline. No other
	// missing, invalidated, or mismatched anchor path may use zero implicitly.
	FreshProviderSession bool
	ObservedAt           time.Time
}

// CanonicalUsageResultV1 is the pure decision returned to the persistence
// owner. Canonical may contain unresolved quota kinds; NextAnchor is a ready
// provider baseline when the report exposes a usable session ref and at least
// one known counter.
type CanonicalUsageResultV1 struct {
	Canonical  *domain.CanonicalUsageV1
	NextAnchor *domain.ProviderUsageAnchorV1
}

// CanonicalizeProviderUsageV1 verifies identity and report integrity, then
// converts either a per-run report directly or a session-cumulative report by
// subtracting a compatible nullable anchor. It never turns an unknown value
// into zero. Persistence/settlement is deliberately outside this function.
func CanonicalizeProviderUsageV1(req CanonicalUsageRequestV1) (CanonicalUsageResultV1, error) {
	if err := validateCanonicalUsageRequest(req); err != nil {
		return CanonicalUsageResultV1{}, err
	}
	if err := req.Report.VerifyDigest(); err != nil {
		return CanonicalUsageResultV1{}, fmt.Errorf("%w: provider usage report digest: %v", domain.ErrValidation, err)
	}
	if err := validateReportIdentity(req); err != nil {
		return CanonicalUsageResultV1{}, err
	}

	if req.Report.Basis == domain.UsageBasisPerRun {
		canonical, err := domain.CanonicalizeProviderUsageReport(req.Report)
		if err != nil {
			return CanonicalUsageResultV1{}, err
		}
		return CanonicalUsageResultV1{Canonical: canonical}, nil
	}

	if req.ObservedAt.IsZero() {
		return CanonicalUsageResultV1{}, fmt.Errorf("%w: cumulative usage requires observed_at", domain.ErrValidation)
	}
	if req.Anchor != nil {
		if err := req.Anchor.Validate(); err != nil {
			return CanonicalUsageResultV1{}, fmt.Errorf("%w: provider usage anchor: %v", domain.ErrValidation, err)
		}
	}

	reportRef := strings.TrimSpace(req.Report.Provenance.SessionRef)
	ownerMatch := (!req.AnchorOwnershipKnown && req.AnchorOwnerRunID == "") ||
		(req.AnchorOwnerRunID == req.RunID && req.AnchorOwnerSequence >= 1)
	compatible := ownerMatch && compatibleProviderUsageAnchor(req)
	zeroBaseline := ownerMatch && req.FreshProviderSession && reportRef != ""
	var baseline *domain.UsageCountersV1
	var baselineReason string
	switch {
	case zeroBaseline:
		zero := zeroKnownCounters(req.Report.Counters)
		baseline = &zero
	case compatible:
		copy := req.Anchor.Counters.Clone()
		baseline = &copy
	default:
		baselineReason = cumulativeBaselineReason(req, reportRef)
	}
	if !ownerMatch {
		baselineReason = "provider usage Run is no longer the current anchor owner"
	}

	delta, resolved, unresolved, reasons := cumulativeDelta(req.Report.Counters, baseline, baselineReason)
	nextAnchor := nextProviderUsageAnchor(req, reportRef, ownerMatch, compatible, zeroBaseline)

	provenance := req.Report.Provenance
	if baseline != nil {
		before := baseline.Clone()
		provenance.AnchorBefore = &before
	}
	if nextAnchor != nil {
		after := nextAnchor.Counters.Clone()
		provenance.AnchorAfter = &after
	}
	provenance.AnchorGeneration = int64(req.ContextGeneration)
	provenance.AnchorSequence = int64(req.SegmentSeq)
	provenance.AnchorObservedAt = req.ObservedAt.UTC()

	canonical := &domain.CanonicalUsageV1{
		SchemaVersion:    domain.CanonicalUsageSchemaVersionV1,
		RunID:            req.RunID,
		Basis:            domain.UsageBasisPerRun,
		Counters:         delta,
		ResolvedKinds:    resolved,
		UnresolvedKinds:  unresolved,
		UnresolvedReason: joinUsageReasons(reasons),
		Provenance:       provenance,
	}
	if err := canonical.Seal(); err != nil {
		return CanonicalUsageResultV1{}, err
	}
	return CanonicalUsageResultV1{Canonical: canonical, NextAnchor: nextAnchor}, nil
}

// CanonicalizeProviderUsage is the unversioned spelling used by application
// callers; the wire/domain contract remains explicitly V1.
func CanonicalizeProviderUsage(req CanonicalUsageRequestV1) (CanonicalUsageResultV1, error) {
	return CanonicalizeProviderUsageV1(req)
}

func validateCanonicalUsageRequest(req CanonicalUsageRequestV1) error {
	if req.Report == nil {
		return fmt.Errorf("%w: provider usage report is required", domain.ErrValidation)
	}
	if strings.TrimSpace(req.RunID) == "" || !strings.HasPrefix(req.RunID, domain.PrefixRun) {
		return fmt.Errorf("%w: canonical usage run_id must be a run id", domain.ErrValidation)
	}
	if strings.TrimSpace(req.AgentID) == "" {
		return fmt.Errorf("%w: canonical usage agent_id is required", domain.ErrValidation)
	}
	if strings.TrimSpace(req.AdapterID) == "" {
		return fmt.Errorf("%w: canonical usage adapter_id is required", domain.ErrValidation)
	}
	if req.ContextGeneration < 0 {
		return fmt.Errorf("%w: canonical usage context_generation must be >= 0", domain.ErrValidation)
	}
	if req.SegmentSeq < 1 {
		return fmt.Errorf("%w: canonical usage segment_seq must be >= 1", domain.ErrValidation)
	}
	if req.SessionRef != "" && strings.TrimSpace(req.SessionRef) == "" {
		return fmt.Errorf("%w: canonical usage session_ref must not be blank", domain.ErrValidation)
	}
	return nil
}

func validateReportIdentity(req CanonicalUsageRequestV1) error {
	report := req.Report
	if report.RunID != req.RunID {
		return fmt.Errorf("%w: provider usage report run identity mismatch", domain.ErrValidation)
	}
	if report.Provenance.AgentID != req.AgentID {
		return fmt.Errorf("%w: provider usage report agent identity mismatch", domain.ErrValidation)
	}
	if report.Provenance.AdapterID != req.AdapterID {
		return fmt.Errorf("%w: provider usage report adapter identity mismatch", domain.ErrValidation)
	}
	if req.SessionRef != "" && report.Provenance.SessionRef != req.SessionRef {
		return fmt.Errorf("%w: provider usage report session identity mismatch", domain.ErrValidation)
	}
	return nil
}

func compatibleProviderUsageAnchor(req CanonicalUsageRequestV1) bool {
	a := req.Anchor
	return a != nil && a.State == domain.ProviderUsageAnchorReady &&
		a.AdapterID == req.AdapterID &&
		a.SessionRef != "" && a.SessionRef == req.Report.Provenance.SessionRef &&
		a.ContextGeneration == req.ContextGeneration && a.SegmentSeq == req.SegmentSeq
}

func cumulativeBaselineReason(req CanonicalUsageRequestV1, reportRef string) string {
	if reportRef == "" {
		return "provider session_ref is missing"
	}
	if req.Anchor == nil {
		return "provider usage anchor is unavailable"
	}
	if req.Anchor.State == domain.ProviderUsageAnchorInvalidated {
		return "provider usage anchor is invalidated"
	}
	if req.Anchor.AdapterID != req.AdapterID || req.Anchor.SessionRef != reportRef {
		return "provider usage session identity changed"
	}
	if req.Anchor.ContextGeneration != req.ContextGeneration {
		return "provider usage context generation changed"
	}
	if req.Anchor.SegmentSeq != req.SegmentSeq {
		return "provider usage segment changed"
	}
	return "provider usage anchor is not comparable"
}

type usageDeltaEntry struct {
	kind   domain.QuotaKind
	label  string
	value  *int64
	reason string
}

func cumulativeDelta(report domain.UsageCountersV1, baseline *domain.UsageCountersV1, baselineReason string) (
	delta domain.UsageCountersV1,
	resolved []domain.QuotaKind,
	unresolved []domain.QuotaKind,
	reasons []usageDeltaEntry,
) {
	// 空集必须序列化为 [] 而非 null：0028 触发器要求 resolved_kinds /
	// unresolved_kinds 是 JSON array（per_run 路径同规）。
	resolved = make([]domain.QuotaKind, 0, 5)
	unresolved = make([]domain.QuotaKind, 0, 5)
	reasons = make([]usageDeltaEntry, 0, 5)
	entries := []struct {
		kind  domain.QuotaKind
		label string
		read  func(domain.UsageCountersV1) *int64
		write func(*domain.UsageCountersV1, *int64)
	}{
		{domain.QuotaInputTokensTotal, "input_tokens_total", func(c domain.UsageCountersV1) *int64 { return c.InputTokensTotal }, func(c *domain.UsageCountersV1, v *int64) { c.InputTokensTotal = v }},
		{domain.QuotaInputUncachedTokens, "input_uncached_tokens", func(c domain.UsageCountersV1) *int64 { return c.InputUncachedTokens }, func(c *domain.UsageCountersV1, v *int64) { c.InputUncachedTokens = v }},
		{domain.QuotaCacheReadTokens, "cache_read_tokens", func(c domain.UsageCountersV1) *int64 { return c.CacheReadTokens }, func(c *domain.UsageCountersV1, v *int64) { c.CacheReadTokens = v }},
		{domain.QuotaCacheWriteTokens, "cache_write_tokens", func(c domain.UsageCountersV1) *int64 { return c.CacheWriteTokens }, func(c *domain.UsageCountersV1, v *int64) { c.CacheWriteTokens = v }},
		{domain.QuotaOutputTokens, "output_tokens", func(c domain.UsageCountersV1) *int64 { return c.OutputTokens }, func(c *domain.UsageCountersV1, v *int64) { c.OutputTokens = v }},
	}
	for _, entry := range entries {
		reportValue := entry.read(report)
		if reportValue == nil {
			unresolved = append(unresolved, entry.kind)
			reasons = append(reasons, usageDeltaEntry{kind: entry.kind, label: entry.label, reason: "provider counter is unknown"})
			continue
		}
		if baseline == nil {
			unresolved = append(unresolved, entry.kind)
			reasons = append(reasons, usageDeltaEntry{kind: entry.kind, label: entry.label, reason: baselineReason})
			continue
		}
		baselineValue := entry.read(*baseline)
		if baselineValue == nil {
			unresolved = append(unresolved, entry.kind)
			reasons = append(reasons, usageDeltaEntry{kind: entry.kind, label: entry.label, reason: "anchor counter is unknown"})
			continue
		}
		value, err := domain.CheckedSubNonNegative(*reportValue, *baselineValue)
		if err != nil {
			unresolved = append(unresolved, entry.kind)
			reasons = append(reasons, usageDeltaEntry{kind: entry.kind, label: entry.label, reason: "provider counter regressed or delta underflowed"})
			continue
		}
		entry.write(&delta, &value)
		resolved = append(resolved, entry.kind)
	}
	// A per-kind anchor merge can make the input total watermark and the
	// component watermarks come from different observations. In that case the
	// total delta is not independently provable from the healthy components
	// (for example, one cache bucket regressed), and retaining it would make the
	// canonical usage violate its strict decomposition contract. Keep all
	// independently proven component deltas, but make the affected aggregate
	// total unresolved instead of fabricating a reconciled value.
	if delta.InputTokensTotal != nil && !inputDeltaDecompositionValid(delta) {
		delta.InputTokensTotal = nil
		resolved = removeUsageKind(resolved, domain.QuotaInputTokensTotal)
		unresolved = append(unresolved, domain.QuotaInputTokensTotal)
		reasons = append(reasons, usageDeltaEntry{
			kind:   domain.QuotaInputTokensTotal,
			label:  "input_tokens_total",
			reason: "input delta decomposition is not provable after per-kind anchor merge",
		})
	}
	sort.Slice(resolved, func(i, j int) bool { return resolved[i] < resolved[j] })
	sort.Slice(unresolved, func(i, j int) bool { return unresolved[i] < unresolved[j] })
	sort.Slice(reasons, func(i, j int) bool { return reasons[i].kind < reasons[j].kind })
	return delta, resolved, unresolved, reasons
}

func inputDeltaDecompositionValid(delta domain.UsageCountersV1) bool {
	if delta.InputTokensTotal == nil {
		return true
	}
	knownSum := int64(0)
	known := true
	for _, component := range []*int64{
		delta.InputUncachedTokens,
		delta.CacheReadTokens,
		delta.CacheWriteTokens,
	} {
		if component == nil {
			known = false
			continue
		}
		var err error
		knownSum, err = domain.CheckedAddNonNegative(knownSum, *component)
		if err != nil {
			return false
		}
	}
	if known && knownSum != *delta.InputTokensTotal {
		return false
	}
	return knownSum <= *delta.InputTokensTotal
}

func removeUsageKind(kinds []domain.QuotaKind, wanted domain.QuotaKind) []domain.QuotaKind {
	filtered := make([]domain.QuotaKind, 0, len(kinds))
	for _, kind := range kinds {
		if kind != wanted {
			filtered = append(filtered, kind)
		}
	}
	return filtered
}

func zeroKnownCounters(counters domain.UsageCountersV1) domain.UsageCountersV1 {
	zero := func(value *int64) *int64 {
		if value == nil {
			return nil
		}
		result := int64(0)
		return &result
	}
	return domain.UsageCountersV1{
		InputTokensTotal:    zero(counters.InputTokensTotal),
		InputUncachedTokens: zero(counters.InputUncachedTokens),
		CacheReadTokens:     zero(counters.CacheReadTokens),
		CacheWriteTokens:    zero(counters.CacheWriteTokens),
		OutputTokens:        zero(counters.OutputTokens),
	}
}

// nextProviderUsageAnchor 决定本 Run 是否推进 provider 累计基线。anchor 的
// 语义是「最近观测水位」而非会话起点。身份兼容时，五个 counter 各自合并：
// report 已知且不低于旧水位就推进到 report；report 缺失或回退就保留旧水位。
// 这样一个分量的 provider reset 不会阻止健康分量推进，下一 Run 也不会从旧
// 健康水位重复计算。推进条件（CAS 写点不变）：
//   - zeroBaseline：fresh provider session，显式零基线；
//   - req.Anchor == nil：从未有基线（即使非 fresh）——当前 Run 的 delta 保持
//     unresolved，后续 Run 从该水位起量；
//   - 身份兼容：写入逐分量合并后的水位。
//
// 其余（身份不兼容 / invalidated）→ nil，不推进。
func nextProviderUsageAnchor(req CanonicalUsageRequestV1, sessionRef string, ownerMatch, compatible, zeroBaseline bool) *domain.ProviderUsageAnchorV1 {
	if !ownerMatch {
		return nil
	}
	var counters domain.UsageCountersV1
	switch {
	case zeroBaseline:
		counters = req.Report.Counters.Clone()
	case req.Anchor == nil:
		counters = req.Report.Counters.Clone()
	case compatible:
		counters = mergeProviderUsageAnchorCounters(req.Anchor.Counters, req.Report.Counters)
	default:
		return nil
	}
	return buildNextProviderUsageAnchor(req, sessionRef, counters)
}

// mergeProviderUsageAnchorCounters merges cumulative watermarks independently.
// A nil report value is an unknown observation, not zero. An older report value
// is a provider reset/regression and must not lower an already proven watermark.
// An anchor component that has never been observed can be seeded by any known
// report value from the same compatible provider session.
func mergeProviderUsageAnchorCounters(anchor, report domain.UsageCountersV1) domain.UsageCountersV1 {
	merge := func(oldValue, reportValue *int64) *int64 {
		switch {
		case reportValue == nil:
			return domainUsageCounterClone(oldValue)
		case oldValue == nil:
			return domainUsageCounterClone(reportValue)
		case *reportValue >= *oldValue:
			return domainUsageCounterClone(reportValue)
		default:
			return domainUsageCounterClone(oldValue)
		}
	}
	return domain.UsageCountersV1{
		InputTokensTotal:    merge(anchor.InputTokensTotal, report.InputTokensTotal),
		InputUncachedTokens: merge(anchor.InputUncachedTokens, report.InputUncachedTokens),
		CacheReadTokens:     merge(anchor.CacheReadTokens, report.CacheReadTokens),
		CacheWriteTokens:    merge(anchor.CacheWriteTokens, report.CacheWriteTokens),
		OutputTokens:        merge(anchor.OutputTokens, report.OutputTokens),
	}
}

func domainUsageCounterClone(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func buildNextProviderUsageAnchor(req CanonicalUsageRequestV1, sessionRef string, counters domain.UsageCountersV1) *domain.ProviderUsageAnchorV1 {
	if sessionRef == "" || !counters.AnyKnown() {
		return nil
	}
	anchor := &domain.ProviderUsageAnchorV1{
		SchemaVersion:     domain.ProviderUsageAnchorSchemaVersionV1,
		State:             domain.ProviderUsageAnchorReady,
		AdapterID:         req.AdapterID,
		SessionRef:        sessionRef,
		ContextGeneration: req.ContextGeneration,
		SegmentSeq:        req.SegmentSeq,
		Counters:          counters.Clone(),
		SourceRunID:       req.RunID,
		ObservedAt:        req.ObservedAt.UTC(),
	}
	if err := anchor.Validate(); err != nil {
		return nil
	}
	return anchor
}

func joinUsageReasons(reasons []usageDeltaEntry) string {
	if len(reasons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, string(reason.kind)+": "+reason.reason)
	}
	return strings.Join(parts, "; ")
}
