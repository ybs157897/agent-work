package application

import (
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestRefreshKnowledgeCoverageStatsDeduplicatesScopedVersionsAndRelations(t *testing.T) {
	repo := &sourceValidationKnowledgeRepo{versions: map[string]*domain.KnowledgeVersion{
		"kbv_a": {ID: "kbv_a", ItemID: "kb_a"},
		"kbv_b": {ID: "kbv_b", ItemID: "kb_b"},
	}}
	svc := &Service{store: sourceValidationStore{knowledge: repo}}
	job := &domain.KnowledgeJob{WorkspaceID: "ws", RequestingAgentID: "agent", VisitedVersionIDs: []string{"kbv_a", "kbv_a", "kbv_b"}, RequiredRelations: []domain.KnowledgeJobRequiredRelation{{ID: "kbr_1"}, {ID: "kbr_1"}, {ID: "kbr_2"}}}
	if err := svc.refreshKnowledgeCoverageStatsLocked(nil, job); err != nil {
		t.Fatal(err)
	}
	if job.Coverage.VisitedNodes != 2 || job.Coverage.VisitedRelations != 2 {
		t.Fatalf("coverage stats = nodes %d relations %d, want 2/2", job.Coverage.VisitedNodes, job.Coverage.VisitedRelations)
	}
	if repo.versionCalls != 3 {
		t.Fatalf("refresh must resolve each stored version ID through scoped repo: calls=%d", repo.versionCalls)
	}
}

func TestInheritKnowledgeCurationDefaultsPreservesPrivateScopeAcrossSplitChanges(t *testing.T) {
	origin := &domain.KnowledgeSubmission{WorkspaceID: "ws_private", AgentID: "agent_owner", Request: domain.KnowledgeSubmitCandidate{
		WorkspaceID: "ws_private", AgentID: "agent_owner", ClientKey: "origin",
		Changes: []domain.KnowledgeChange{{OwnerAgentID: "agent_owner", Visibility: domain.KnowledgeVisibilityPrivate,
			Scope: domain.KnowledgeScope{"project": "secret"}, Title: "source", Body: "source", Kind: "fact"}},
	}}
	changes := []domain.KnowledgeChange{{Title: "part A", Body: "A", Kind: "fact"}, {Title: "part B", Body: "B", Kind: "fact"}, {Title: "part C", Body: "C", Kind: "fact"}}
	if err := inheritKnowledgeCurationDefaults(origin, changes); err != nil {
		t.Fatal(err)
	}
	for i, change := range changes {
		if change.OwnerAgentID != "agent_owner" || change.Visibility != domain.KnowledgeVisibilityPrivate || !knowledgeScopesEqual(change.Scope, domain.KnowledgeScope{"project": "secret"}) {
			t.Fatalf("split change %d widened trusted origin: %+v", i, change)
		}
	}
}

func TestInheritKnowledgeCurationDefaultsRejectsPrivacyOrScopeWidening(t *testing.T) {
	origin := &domain.KnowledgeSubmission{WorkspaceID: "ws_private", AgentID: "agent_owner", Request: domain.KnowledgeSubmitCandidate{
		WorkspaceID: "ws_private", AgentID: "agent_owner", ClientKey: "origin",
		Changes: []domain.KnowledgeChange{{OwnerAgentID: "agent_owner", Visibility: domain.KnowledgeVisibilityPrivate,
			Scope: domain.KnowledgeScope{"project": "secret"}, Title: "source", Body: "source", Kind: "fact"}},
	}}
	cases := []domain.KnowledgeChange{{OwnerAgentID: "agent_other", Visibility: domain.KnowledgeVisibilityPrivate, Scope: domain.KnowledgeScope{"project": "secret"}, Title: "x", Body: "x", Kind: "fact"},
		{OwnerAgentID: "agent_owner", Visibility: domain.KnowledgeVisibilityWorkspace, Scope: domain.KnowledgeScope{"project": "secret"}, Title: "x", Body: "x", Kind: "fact"},
		{OwnerAgentID: "agent_owner", Visibility: domain.KnowledgeVisibilityPrivate, Scope: domain.KnowledgeScope{}, Title: "x", Body: "x", Kind: "fact"}}
	for i, change := range cases {
		if err := inheritKnowledgeCurationDefaults(origin, []domain.KnowledgeChange{change}); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("case %d widening error = %v, want validation", i, err)
		}
	}
}

func TestMergeKnowledgeFinishRelationsAttachesTopLevelEdgesToChanges(t *testing.T) {
	finish := &KnowledgeFinishDecision{Changes: []domain.KnowledgeChange{{ItemID: "kb_a", Title: "A", Body: "A", Kind: "fact"}, {Title: "B", Body: "B", Kind: "fact"}}, Relations: []domain.KnowledgeRelationInput{
		{FromItemID: "kb_a", ToItemID: "@change:1", Kind: domain.KnowledgeRelationDependsOn},
		{FromItemID: "kb_external", ToItemID: "@change:1", Kind: domain.KnowledgeRelationImpacts},
	}}
	if err := mergeKnowledgeFinishRelations(finish); err != nil {
		t.Fatal(err)
	}
	if len(finish.Changes[0].Relations) != 1 || len(finish.Changes[1].Relations) != 1 {
		t.Fatalf("top-level relations were not attached: %+v", finish.Changes)
	}
	if finish.Changes[0].Relations[0].ToItemID != "@change:1" || finish.Changes[1].Relations[0].FromItemID != "kb_external" {
		t.Fatalf("relation attachment target mismatch: %+v", finish.Changes)
	}
	if err := mergeKnowledgeFinishRelations(&KnowledgeFinishDecision{Changes: []domain.KnowledgeChange{{Title: "A", Body: "A", Kind: "fact"}}, Relations: []domain.KnowledgeRelationInput{{FromItemID: "kb_unrelated", ToItemID: "kb_other", Kind: domain.KnowledgeRelationRelatedTo}}}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unattached top-level relation error = %v, want validation", err)
	}
}
