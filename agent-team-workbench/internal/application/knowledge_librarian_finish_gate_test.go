package application

import (
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestKnowledgeUsageAccountsCanonicalPerRunDeltaOnce(t *testing.T) {
	in1, out1 := int64(100), int64(10)
	in2, out2 := int64(50), int64(5)
	job := &domain.KnowledgeJob{Budget: domain.KnowledgeJobBudget{MaxTurns: 4}, Result: map[string]any{}}
	run1 := &domain.ExecutionRun{ID: "run_knowledge_usage_1", UsageBasis: domain.UsageBasisSessionCumulative, CanonicalUsage: &domain.CanonicalUsageV1{Counters: domain.UsageCountersV1{InputTokensTotal: &in1, OutputTokens: &out1}}}
	run2 := &domain.ExecutionRun{ID: "run_knowledge_usage_2", UsageBasis: domain.UsageBasisSessionCumulative, CanonicalUsage: &domain.CanonicalUsageV1{Counters: domain.UsageCountersV1{InputTokensTotal: &in2, OutputTokens: &out2}}}
	if err := accountKnowledgeRun(job, run1); err != nil {
		t.Fatal(err)
	}
	if err := accountKnowledgeRun(job, run1); err != nil {
		t.Fatal(err)
	}
	if err := accountKnowledgeRun(job, run2); err != nil {
		t.Fatal(err)
	}
	if job.Used.Turns != 2 || job.Used.InputTokens != 150 || job.Used.OutputTokens != 15 {
		t.Fatalf("canonical usage was double-counted or lost: %+v", job.Used)
	}
}

func TestKnowledgeJobDeadlineUsesCreationTimeAcrossRecovery(t *testing.T) {
	created := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	job := &domain.KnowledgeJob{CreatedAt: created, Budget: domain.KnowledgeJobBudget{MaxDurationSeconds: 300}}
	deadline := knowledgeJobDeadline(job)
	if !deadline.Equal(created.Add(5 * time.Minute)) {
		t.Fatalf("deadline = %s, want %s", deadline, created.Add(5*time.Minute))
	}
	if !knowledgeJobExpired(job, deadline) || knowledgeJobExpired(job, deadline.Add(-time.Nanosecond)) {
		t.Fatalf("expiration boundary did not use creation timestamp")
	}
}

func TestKnowledgeFinishGateRejectsDroppedDiscoveredEndpoint(t *testing.T) {
	job := &domain.KnowledgeJob{
		EvidenceIDs:       []string{"kbs_relation"},
		VisitedVersionIDs: []string{"kbv_a_v1", "kbv_b_v1"},
		Coverage:          domain.KnowledgeCoverage{Status: domain.KnowledgeCoverageComplete},
		RequiredItemIDs:   []string{"kb_a", "kb_b"},
		RequiredRelations: []domain.KnowledgeJobRequiredRelation{{
			ID: "kbr_a_b", SourceVersionID: "kbv_a_v1", FromItemID: "kb_a", ToItemID: "kb_b",
			Kind: domain.KnowledgeRelationDependsOn, SourceIDs: []string{"kbs_relation"},
		}},
	}
	for _, subject := range knowledgeCoverageSubjects {
		job.Coverage.Entries = append(job.Coverage.Entries, domain.KnowledgeCoverageEntry{
			Subject: subject, Status: domain.KnowledgeCoverageComplete,
			EvidenceIDs: []string{"kbs_relation"},
		})
	}
	finish := &KnowledgeFinishDecision{
		Status:      domain.KnowledgeCoverageComplete,
		Coverage:    append([]domain.KnowledgeCoverageEntry(nil), job.Coverage.Entries...),
		EvidenceIDs: []string{"kbs_relation"},
		Citations:   []KnowledgeLibrarianCitation{{ItemID: "kb_a", VersionID: "kbv_a_v1", SourceID: "kbs_relation"}},
	}
	result := KnowledgeFinishGate(job, finish)
	if result.Status == domain.KnowledgeCoverageComplete {
		t.Fatalf("finish dropping discovered B must not pass: %+v", result)
	}
	if len(result.MissingItemIDs) != 1 || result.MissingItemIDs[0] != "kb_b" {
		t.Fatalf("missing B is not pinned: %+v", result)
	}
	if len(result.MissingRelationIDs) != 1 || result.MissingRelationIDs[0] != "kbr_a_b" {
		t.Fatalf("missing A->B relation is not pinned: %+v", result)
	}
	if !strings.Contains(strings.Join(result.Gaps, "\n"), "kb_b") {
		t.Fatalf("missing endpoint was not made explicit: %+v", result.Gaps)
	}
}

func TestKnowledgeFinishGateAcceptsDeliveredRelationAndEndpoint(t *testing.T) {
	job := &domain.KnowledgeJob{
		EvidenceIDs:       []string{"kbs_relation"},
		VisitedVersionIDs: []string{"kbv_a_v1", "kbv_b_v1"},
		Coverage:          domain.KnowledgeCoverage{Status: domain.KnowledgeCoverageComplete},
		RequiredItemIDs:   []string{"kb_a", "kb_b"},
		RequiredRelations: []domain.KnowledgeJobRequiredRelation{{
			ID: "kbr_a_b", SourceVersionID: "kbv_a_v1", FromItemID: "kb_a", ToItemID: "kb_b",
			Kind: domain.KnowledgeRelationDependsOn, SourceIDs: []string{"kbs_relation"},
		}},
	}
	for _, subject := range knowledgeCoverageSubjects {
		job.Coverage.Entries = append(job.Coverage.Entries, domain.KnowledgeCoverageEntry{
			Subject: subject, Status: domain.KnowledgeCoverageComplete,
			EvidenceIDs: []string{"kbs_relation"},
		})
	}
	finish := &KnowledgeFinishDecision{
		Status:      domain.KnowledgeCoverageComplete,
		Coverage:    append([]domain.KnowledgeCoverageEntry(nil), job.Coverage.Entries...),
		EvidenceIDs: []string{"kbs_relation"},
		Citations: []KnowledgeLibrarianCitation{{ItemID: "kb_a", VersionID: "kbv_a_v1", SourceID: "kbs_relation"}, {
			ItemID: "kb_b", VersionID: "kbv_b_v1", SourceID: "kbs_relation",
		}},
		Relations: []domain.KnowledgeRelationInput{{FromItemID: "kb_a", ToItemID: "kb_b", Kind: domain.KnowledgeRelationDependsOn, SourceIDs: []string{"kbs_relation"}}},
	}
	result := KnowledgeFinishGate(job, finish)
	if result.Status != domain.KnowledgeCoverageComplete || len(result.Gaps) != 0 {
		t.Fatalf("delivered relation and endpoint should pass static gate: %+v", result)
	}
}
