package domain

import (
	"strings"
	"testing"
	"time"
)

func quotaGapResolutionTestDigest(ch byte) string {
	return "sha256:" + strings.Repeat(string(ch), 64)
}

func quotaGapResolutionFixture(t *testing.T) *QuotaGapResolution {
	t.Helper()
	evidence := GovernanceEvidenceItem{
		SourceKind: EvidenceSourceWorkItem, SourceID: "wi_gap_test",
		Verification: EvidenceVerificationAccepted, Summary: "operator verified external usage statement",
		RecordedAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
	}
	evidenceDigest, err := ComputeGovernanceEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	return &QuotaGapResolution{
		ID: "qgap_test", SchemaVersion: QuotaGapResolutionSchemaVersion,
		Target:              QuotaSpendKey{TurnKey: TurnKey{GoalID: "goal_gap_test", TodoID: "todo_gap_test", TurnSeq: 1}, Kind: QuotaOutputTokens, RunID: "run_gap_test"},
		OriginalUsageDigest: quotaGapResolutionTestDigest('a'), OriginalPolicyDigest: quotaGapResolutionTestDigest('b'),
		Status: QuotaGapResolutionReconciled, Amount: 42, Evidence: evidence,
		EvidenceDigest: evidenceDigest, ActorKind: QuotaGapResolutionActorUser, ActorID: "user_operator",
		Reason: "verified by billing statement", CreatedAt: time.Date(2026, 9, 2, 0, 1, 0, 0, time.UTC),
	}
}

func TestQuotaGapResolutionValidatesEvidenceAndIdentity(t *testing.T) {
	resolution := quotaGapResolutionFixture(t)
	if err := resolution.Seal(); err != nil {
		t.Fatal(err)
	}
	mutatedAmount := *resolution
	mutatedAmount.Amount++
	if err := mutatedAmount.Validate(); err == nil {
		t.Fatal("amount mutation must fail canonical resolution digest validation")
	}
	mutated := *resolution
	mutated.Evidence.Verification = EvidenceVerificationObserved
	if err := mutated.Validate(); err == nil {
		t.Fatal("observed evidence must not reconcile a quota gap")
	}
	mutated = *resolution
	mutated.SchemaVersion = "quota-gap-resolution/v2"
	if err := mutated.Validate(); err == nil {
		t.Fatal("unknown resolution schema version must fail closed")
	}
}

func TestGovernanceEvidenceDigestIsStableAndDetectsMutation(t *testing.T) {
	evidence := GovernanceEvidenceItem{
		SourceKind: EvidenceSourceRun, SourceID: "run_gap_test", Verification: EvidenceVerificationPassed,
		Summary: "verified", RecordedAt: time.Date(2026, 9, 2, 1, 0, 0, 0, time.UTC),
	}
	first, err := ComputeGovernanceEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidence.Summary = "changed"
	second, err := ComputeGovernanceEvidenceDigest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("evidence content mutation must change canonical digest")
	}
}
