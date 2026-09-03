package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
)

// Usage schema versions are deliberately separate from the provider protocol
// version. A provider may change its wire shape without changing the
// workbench's canonical accounting contract.
const (
	ProviderUsageReportSchemaVersionV1 = "provider-usage/v1"
	CanonicalUsageSchemaVersionV1      = "canonical-usage/v1"
	UsageBasisPerRun                   = "per_run"
	UsageBasisSessionCumulative        = "session_cumulative"
)

// UsageCountersV1 is the nullable token counter set shared by provider reports
// and the persistent session anchor. A nil pointer means that the provider did
// not expose that counter; it is not an observed zero. Unknown counters
// serialize as absent object keys (never JSON null) so the 0028 trigger's
// "counter key IS NULL" checks and the typed model agree.
type UsageCountersV1 struct {
	InputTokensTotal    *int64 `json:"input_tokens_total,omitempty"`
	InputUncachedTokens *int64 `json:"input_uncached_tokens,omitempty"`
	CacheReadTokens     *int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens    *int64 `json:"cache_write_tokens,omitempty"`
	OutputTokens        *int64 `json:"output_tokens,omitempty"`
}

// Validate checks counter shape and, when all input dimensions are known,
// checks the conservation identity total = uncached + read + write. When only
// some input dimensions are known, it still rejects a total smaller than the
// sum of the components that were reported: unknown components are never zero,
// so a total below the known partial sum is a contradiction, not an estimate.
func (c UsageCountersV1) Validate() error {
	for name, value := range map[string]*int64{
		"input_tokens_total":    c.InputTokensTotal,
		"input_uncached_tokens": c.InputUncachedTokens,
		"cache_read_tokens":     c.CacheReadTokens,
		"cache_write_tokens":    c.CacheWriteTokens,
		"output_tokens":         c.OutputTokens,
	} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: usage counter %s must be >= 0", ErrValidation, name)
		}
	}
	// Partial-knowledge guard: a provider-reported total must cover the input
	// components it did expose.
	if c.InputTokensTotal != nil {
		knownSum := int64(0)
		var err error
		for _, component := range []*int64{c.InputUncachedTokens, c.CacheReadTokens, c.CacheWriteTokens} {
			if component == nil {
				continue
			}
			knownSum, err = CheckedAddNonNegative(knownSum, *component)
			if err != nil {
				return fmt.Errorf("%w: input counter decomposition overflow", ErrValidation)
			}
		}
		if knownSum > *c.InputTokensTotal {
			return fmt.Errorf("%w: known input components exceed input_tokens_total", ErrValidation)
		}
	}
	if c.InputTokensTotal != nil && c.InputUncachedTokens != nil &&
		c.CacheReadTokens != nil && c.CacheWriteTokens != nil {
		sum, err := CheckedAddNonNegative(*c.InputUncachedTokens, *c.CacheReadTokens)
		if err != nil {
			return fmt.Errorf("%w: input counter decomposition overflow", ErrValidation)
		}
		sum, err = CheckedAddNonNegative(sum, *c.CacheWriteTokens)
		if err != nil {
			return fmt.Errorf("%w: input counter decomposition overflow", ErrValidation)
		}
		if sum != *c.InputTokensTotal {
			return fmt.Errorf("%w: input counter decomposition does not equal total", ErrValidation)
		}
	}
	return nil
}

// Clone returns a deep copy suitable for an anchor snapshot or provenance.
func (c UsageCountersV1) Clone() UsageCountersV1 {
	return UsageCountersV1{
		InputTokensTotal:    cloneUsageCounter(c.InputTokensTotal),
		InputUncachedTokens: cloneUsageCounter(c.InputUncachedTokens),
		CacheReadTokens:     cloneUsageCounter(c.CacheReadTokens),
		CacheWriteTokens:    cloneUsageCounter(c.CacheWriteTokens),
		OutputTokens:        cloneUsageCounter(c.OutputTokens),
	}
}

func cloneUsageCounter(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (c UsageCountersV1) Equal(other UsageCountersV1) bool {
	return usageCounterEqual(c.InputTokensTotal, other.InputTokensTotal) &&
		usageCounterEqual(c.InputUncachedTokens, other.InputUncachedTokens) &&
		usageCounterEqual(c.CacheReadTokens, other.CacheReadTokens) &&
		usageCounterEqual(c.CacheWriteTokens, other.CacheWriteTokens) &&
		usageCounterEqual(c.OutputTokens, other.OutputTokens)
}

// AnyKnown reports whether the provider exposed at least one counter. It is
// useful to distinguish an absent usage report from an observed all-unknown
// report without treating unknown as zero.
func (c UsageCountersV1) AnyKnown() bool {
	return c.InputTokensTotal != nil || c.InputUncachedTokens != nil ||
		c.CacheReadTokens != nil || c.CacheWriteTokens != nil || c.OutputTokens != nil
}

// AllUnknown is the inverse of AnyKnown and is named for anchor/settlement
// callers that need to make an explicit fail-closed decision.
func (c UsageCountersV1) AllUnknown() bool { return !c.AnyKnown() }

func usageCounterEqual(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// UsageProvenanceV1 explains which provider fields produced a report and how
// a cumulative report was anchored. It contains no raw provider payload.
type UsageProvenanceV1 struct {
	AdapterID        string           `json:"adapter_id"`
	Protocol         string           `json:"protocol"`
	ProtocolVersion  string           `json:"protocol_version"`
	Source           string           `json:"source"`
	ReportedBasis    string           `json:"reported_basis"`
	AgentID          string           `json:"agent_id"`
	SessionRef       string           `json:"session_ref,omitempty"`
	Mapping          string           `json:"mapping"`
	AnchorBefore     *UsageCountersV1 `json:"anchor_before,omitempty"`
	AnchorAfter      *UsageCountersV1 `json:"anchor_after,omitempty"`
	AnchorGeneration int64            `json:"anchor_generation,omitempty"`
	AnchorSequence   int64            `json:"anchor_sequence,omitempty"`
	AnchorObservedAt time.Time        `json:"anchor_observed_at,omitempty"`
}

func (p UsageProvenanceV1) Validate() error {
	for field, value := range map[string]string{
		"adapter_id":       p.AdapterID,
		"protocol":         p.Protocol,
		"protocol_version": p.ProtocolVersion,
		"source":           p.Source,
		"reported_basis":   p.ReportedBasis,
		"agent_id":         p.AgentID,
		"mapping":          p.Mapping,
	} {
		if err := validateText("usage provenance."+field, value, 512); err != nil {
			return err
		}
	}
	if p.ReportedBasis != UsageBasisPerRun && p.ReportedBasis != UsageBasisSessionCumulative {
		return fmt.Errorf("%w: usage provenance reported_basis %q", ErrValidation, p.ReportedBasis)
	}
	if p.SessionRef != "" {
		if err := validateText("usage provenance.session_ref", p.SessionRef, 1024); err != nil {
			return err
		}
	}
	if p.AnchorGeneration < 0 || p.AnchorSequence < 0 {
		return fmt.Errorf("%w: usage provenance anchor coordinates must be >= 0", ErrValidation)
	}
	if p.AnchorBefore != nil {
		if err := validateProviderUsageAnchorCounters(*p.AnchorBefore); err != nil {
			return fmt.Errorf("%w: usage provenance.anchor_before: %v", ErrValidation, err)
		}
	}
	if p.AnchorAfter != nil {
		if err := validateProviderUsageAnchorCounters(*p.AnchorAfter); err != nil {
			return fmt.Errorf("%w: usage provenance.anchor_after: %v", ErrValidation, err)
		}
	}
	return nil
}

// ProviderUsageReportV1 is the adapter-to-control-plane report. Its counters
// retain provider semantics and may be session cumulative; only the
// canonicalizer can produce CanonicalUsageV1.
type ProviderUsageReportV1 struct {
	SchemaVersion string            `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Basis         string            `json:"basis"`
	Counters      UsageCountersV1   `json:"counters"`
	Provenance    UsageProvenanceV1 `json:"provenance"`
	Digest        string            `json:"digest,omitempty"`
}

func (r *ProviderUsageReportV1) Validate() error {
	if err := r.validateShape(false); err != nil {
		return err
	}
	if r.Digest != "" {
		return r.VerifyDigest()
	}
	return nil
}

func (r *ProviderUsageReportV1) validateShape(requireDigest bool) error {
	if r == nil {
		return fmt.Errorf("%w: nil provider usage report", ErrValidation)
	}
	if r.SchemaVersion != ProviderUsageReportSchemaVersionV1 {
		return fmt.Errorf("%w: provider usage report schema_version %q", ErrValidation, r.SchemaVersion)
	}
	if err := validateTypedID("provider usage report.run_id", r.RunID, PrefixRun); err != nil {
		return err
	}
	if r.Basis != UsageBasisPerRun && r.Basis != UsageBasisSessionCumulative {
		return fmt.Errorf("%w: provider usage report basis %q", ErrValidation, r.Basis)
	}
	if err := r.Counters.Validate(); err != nil {
		return err
	}
	if err := r.Provenance.Validate(); err != nil {
		return err
	}
	if r.Provenance.ReportedBasis != r.Basis {
		return fmt.Errorf("%w: provider usage report basis/provenance mismatch", ErrValidation)
	}
	if requireDigest {
		if err := ValidateCanonicalDigest(r.Digest); err != nil {
			return fmt.Errorf("%w: provider usage report.digest: %v", ErrValidation, err)
		}
	}
	return nil
}

// ComputeDigest returns the RFC 8785 + SHA-256 identity of the provider
// report, excluding Digest itself. Reports may be constructed unsealed so an
// adapter can call Seal once all provider fields are known.
func (r *ProviderUsageReportV1) ComputeDigest() (string, error) {
	if err := r.validateShape(false); err != nil {
		return "", err
	}
	payload := providerUsageReportDigestPayload{
		SchemaVersion: r.SchemaVersion,
		RunID:         r.RunID,
		Basis:         r.Basis,
		Counters:      r.Counters,
		Provenance:    r.Provenance,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal provider usage report: %v", ErrValidation, err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize provider usage report: %v", ErrValidation, err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// Seal computes and installs Digest after strict report-shape validation.
func (r *ProviderUsageReportV1) Seal() error {
	if err := r.validateShape(false); err != nil {
		return err
	}
	digest, err := r.ComputeDigest()
	if err != nil {
		return err
	}
	r.Digest = digest
	return nil
}

// VerifyDigest validates shape and proves Digest matches the provider report
// payload. It is intentionally separate from Validate so callers can accept
// an unsealed in-memory report while requiring a sealed persisted snapshot.
func (r *ProviderUsageReportV1) VerifyDigest() error {
	if err := r.validateShape(true); err != nil {
		return err
	}
	want, err := r.ComputeDigest()
	if err != nil {
		return err
	}
	if want != r.Digest {
		return fmt.Errorf("%w: provider usage report digest mismatch", ErrValidation)
	}
	return nil
}

type providerUsageReportDigestPayload struct {
	SchemaVersion string            `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Basis         string            `json:"basis"`
	Counters      UsageCountersV1   `json:"counters"`
	Provenance    UsageProvenanceV1 `json:"provenance"`
}

// CanonicalUsageV1 is the only usage shape accepted by the quota ledger. The
// basis is always per_run; converting a cumulative provider report requires a
// persistent anchor and is intentionally outside this value type.
type CanonicalUsageV1 struct {
	SchemaVersion    string            `json:"schema_version"`
	RunID            string            `json:"run_id"`
	Basis            string            `json:"usage_basis"`
	Counters         UsageCountersV1   `json:"counters"`
	CostMicroUSD     *int64            `json:"cost_microusd,omitempty"`
	PriceDigest      string            `json:"price_digest,omitempty"`
	ResolvedKinds    []QuotaKind       `json:"resolved_kinds"`
	UnresolvedKinds  []QuotaKind       `json:"unresolved_kinds"`
	UnresolvedReason string            `json:"unresolved_reason,omitempty"`
	Provenance       UsageProvenanceV1 `json:"provenance"`
	Digest           string            `json:"digest"`
}

func (u *CanonicalUsageV1) Validate() error {
	if err := u.validateShape(true); err != nil {
		return err
	}
	return u.VerifyDigest()
}

func (u *CanonicalUsageV1) validateShape(requireDigest bool) error {
	if u == nil {
		return fmt.Errorf("%w: nil canonical usage", ErrValidation)
	}
	if u.SchemaVersion != CanonicalUsageSchemaVersionV1 {
		return fmt.Errorf("%w: canonical usage schema_version %q", ErrValidation, u.SchemaVersion)
	}
	if err := validateTypedID("canonical usage.run_id", u.RunID, PrefixRun); err != nil {
		return err
	}
	if u.Basis != UsageBasisPerRun {
		return fmt.Errorf("%w: canonical usage basis must be per_run", ErrValidation)
	}
	if err := u.Counters.Validate(); err != nil {
		return err
	}
	if u.CostMicroUSD != nil && *u.CostMicroUSD < 0 {
		return fmt.Errorf("%w: canonical usage cost_microusd must be >= 0", ErrValidation)
	}
	if u.PriceDigest != "" {
		if err := ValidateCanonicalDigest(u.PriceDigest); err != nil {
			return fmt.Errorf("%w: canonical usage.price_digest: %v", ErrValidation, err)
		}
	}
	if u.CostMicroUSD != nil && u.PriceDigest == "" {
		return fmt.Errorf("%w: resolved cost requires price_digest", ErrValidation)
	}
	if err := validateUsageKindLists(u); err != nil {
		return err
	}
	if err := u.Provenance.Validate(); err != nil {
		return err
	}
	if requireDigest {
		if err := ValidateCanonicalDigest(u.Digest); err != nil {
			return fmt.Errorf("%w: canonical usage.digest: %v", ErrValidation, err)
		}
	}
	return nil
}

func validateUsageKindLists(u *CanonicalUsageV1) error {
	if !sortAndValidateUsageKinds(u.ResolvedKinds, "resolved_kinds") {
		return fmt.Errorf("%w: canonical usage.resolved_kinds must be sorted, unique usage kinds", ErrValidation)
	}
	if !sortAndValidateUsageKinds(u.UnresolvedKinds, "unresolved_kinds") {
		return fmt.Errorf("%w: canonical usage.unresolved_kinds must be sorted, unique usage kinds", ErrValidation)
	}
	for _, resolved := range u.ResolvedKinds {
		if containsQuotaKind(u.UnresolvedKinds, resolved) {
			return fmt.Errorf("%w: canonical usage kind %q is both resolved and unresolved", ErrValidation, resolved)
		}
	}
	if len(u.UnresolvedKinds) > 0 && strings.TrimSpace(u.UnresolvedReason) == "" {
		return fmt.Errorf("%w: unresolved usage requires unresolved_reason", ErrValidation)
	}
	if len(u.UnresolvedKinds) == 0 && u.UnresolvedReason != "" {
		return fmt.Errorf("%w: unresolved_reason requires unresolved kinds", ErrValidation)
	}

	usageCounters := map[QuotaKind]*int64{
		QuotaInputTokensTotal:    u.Counters.InputTokensTotal,
		QuotaInputUncachedTokens: u.Counters.InputUncachedTokens,
		QuotaCacheReadTokens:     u.Counters.CacheReadTokens,
		QuotaCacheWriteTokens:    u.Counters.CacheWriteTokens,
		QuotaOutputTokens:        u.Counters.OutputTokens,
		QuotaCostMicroUSD:        u.CostMicroUSD,
	}
	for kind, value := range usageCounters {
		resolved := containsQuotaKind(u.ResolvedKinds, kind)
		unresolved := containsQuotaKind(u.UnresolvedKinds, kind)
		if kind == QuotaCostMicroUSD && value == nil && !resolved && !unresolved {
			// Cost is a second-stage calculation requiring a price snapshot; a
			// provider report need not claim cost before that stage runs.
			continue
		}
		if value != nil && !resolved {
			return fmt.Errorf("%w: canonical usage counter %q lacks resolved kind", ErrValidation, kind)
		}
		if value == nil && resolved {
			return fmt.Errorf("%w: canonical usage resolved kind %q lacks counter", ErrValidation, kind)
		}
		if value == nil && !unresolved {
			return fmt.Errorf("%w: canonical usage counter %q must be resolved or unresolved", ErrValidation, kind)
		}
	}
	return nil
}

func sortAndValidateUsageKinds(values []QuotaKind, field string) bool {
	_ = field
	previous := ""
	for i, kind := range values {
		if !isUsageQuotaKind(kind) {
			return false
		}
		if i > 0 && string(kind) <= previous {
			return false
		}
		previous = string(kind)
	}
	return true
}

func isUsageQuotaKind(kind QuotaKind) bool {
	switch kind {
	case QuotaInputTokensTotal, QuotaInputUncachedTokens, QuotaCacheReadTokens,
		QuotaCacheWriteTokens, QuotaOutputTokens, QuotaCostMicroUSD:
		return true
	default:
		return false
	}
}

func containsQuotaKind(values []QuotaKind, wanted QuotaKind) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// ComputeDigest returns the RFC 8785 + SHA-256 identity of the canonical usage
// payload, excluding Digest itself. It accepts an unsealed value so callers
// can construct and then Seal it.
func (u *CanonicalUsageV1) ComputeDigest() (string, error) {
	if err := u.validateShape(false); err != nil {
		return "", err
	}
	payload := canonicalUsageDigestPayload{
		SchemaVersion:    u.SchemaVersion,
		RunID:            u.RunID,
		Basis:            u.Basis,
		Counters:         u.Counters,
		CostMicroUSD:     u.CostMicroUSD,
		PriceDigest:      u.PriceDigest,
		ResolvedKinds:    append([]QuotaKind(nil), u.ResolvedKinds...),
		UnresolvedKinds:  append([]QuotaKind(nil), u.UnresolvedKinds...),
		UnresolvedReason: u.UnresolvedReason,
		Provenance:       u.Provenance,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal canonical usage: %v", ErrValidation, err)
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize usage: %v", ErrValidation, err)
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type canonicalUsageDigestPayload struct {
	SchemaVersion    string            `json:"schema_version"`
	RunID            string            `json:"run_id"`
	Basis            string            `json:"usage_basis"`
	Counters         UsageCountersV1   `json:"counters"`
	CostMicroUSD     *int64            `json:"cost_microusd,omitempty"`
	PriceDigest      string            `json:"price_digest,omitempty"`
	ResolvedKinds    []QuotaKind       `json:"resolved_kinds"`
	UnresolvedKinds  []QuotaKind       `json:"unresolved_kinds"`
	UnresolvedReason string            `json:"unresolved_reason,omitempty"`
	Provenance       UsageProvenanceV1 `json:"provenance"`
}

// Seal computes and installs Digest after strict shape validation.
func (u *CanonicalUsageV1) Seal() error {
	if err := u.validateShape(false); err != nil {
		return err
	}
	digest, err := u.ComputeDigest()
	if err != nil {
		return err
	}
	u.Digest = digest
	return nil
}

// VerifyDigest validates shape and proves Digest matches the canonical payload.
func (u *CanonicalUsageV1) VerifyDigest() error {
	if err := u.validateShape(true); err != nil {
		return err
	}
	want, err := u.ComputeDigest()
	if err != nil {
		return err
	}
	if want != u.Digest {
		return fmt.Errorf("%w: canonical usage digest mismatch", ErrValidation)
	}
	return nil
}

// CanonicalizeProviderUsageReport accepts only a per-run report. A cumulative
// report must first pass through the persistent TaskSession anchor owner.
func CanonicalizeProviderUsageReport(report *ProviderUsageReportV1) (*CanonicalUsageV1, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	if report.Basis != UsageBasisPerRun {
		return nil, fmt.Errorf("%w: session cumulative usage requires a persistent anchor before canonicalization", ErrValidation)
	}
	counters := report.Counters.Clone()
	resolved := make([]QuotaKind, 0, 5)
	unresolved := make([]QuotaKind, 0, 5)
	missing := make([]string, 0, 5)
	add := func(kind QuotaKind, value *int64, label string) {
		if value != nil {
			resolved = append(resolved, kind)
		} else {
			unresolved = append(unresolved, kind)
			missing = append(missing, label)
		}
	}
	add(QuotaInputTokensTotal, counters.InputTokensTotal, "input_tokens_total")
	add(QuotaInputUncachedTokens, counters.InputUncachedTokens, "input_uncached_tokens")
	add(QuotaCacheReadTokens, counters.CacheReadTokens, "cache_read_tokens")
	add(QuotaCacheWriteTokens, counters.CacheWriteTokens, "cache_write_tokens")
	add(QuotaOutputTokens, counters.OutputTokens, "output_tokens")
	sort.Slice(resolved, func(i, j int) bool { return resolved[i] < resolved[j] })
	sort.Slice(unresolved, func(i, j int) bool { return unresolved[i] < unresolved[j] })
	usage := &CanonicalUsageV1{
		SchemaVersion: CanonicalUsageSchemaVersionV1,
		RunID:         report.RunID,
		Basis:         UsageBasisPerRun,
		Counters:      counters,
		ResolvedKinds: resolved, UnresolvedKinds: unresolved,
		Provenance: report.Provenance,
	}
	if len(missing) > 0 {
		usage.UnresolvedReason = "provider usage did not expose: " + strings.Join(missing, ", ")
	}
	if err := usage.Seal(); err != nil {
		return nil, err
	}
	return usage, nil
}

// CheckedAddNonNegative adds nonnegative int64 values without wrapping.
func CheckedAddNonNegative(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > maxInt64-right {
		return 0, fmt.Errorf("%w: non-negative int64 addition overflow", ErrValidation)
	}
	return left + right, nil
}

// CheckedSubNonNegative subtracts right from left without underflow. A lower
// cumulative report is a reset/identity error, not a negative usage delta.
func CheckedSubNonNegative(left, right int64) (int64, error) {
	if left < 0 || right < 0 || right > left {
		return 0, fmt.Errorf("%w: non-negative int64 subtraction underflow", ErrValidation)
	}
	return left - right, nil
}
