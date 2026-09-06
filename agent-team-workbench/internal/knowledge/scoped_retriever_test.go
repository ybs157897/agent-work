package knowledge

import (
	"context"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestScopedRetrieverExpandsRelatedItemAndCarriesEvidence(t *testing.T) {
	a := testHit("kb_a", "kbv_a", "A 的正文")
	b := testHit("kb_b", "kbv_b", "B 的正文与证据")
	a.Item.Scope = domain.KnowledgeScope{"domain": "prd"}
	b.Item.Scope = domain.KnowledgeScope{"domain": "prd"}
	relation := &domain.KnowledgeRelation{ID: "kbr_ab", FromItemID: a.Item.ID, ToItemID: b.Item.ID, Kind: domain.KnowledgeRelationImpacts}
	source := &domain.KnowledgeSource{ID: "kbs_b", WorkspaceID: "ws_test", SubmittedByAgentID: "agent_test", Kind: domain.KnowledgeSourceCode, Ref: "feature/b.go@abc", Excerpt: "B is called after A"}
	f := &fakeLibrary{
		hits:      []*domain.KnowledgeHit{a},
		items:     map[string]*domain.KnowledgeItem{a.Item.ID: &a.Item, b.Item.ID: &b.Item},
		versions:  map[string]*domain.KnowledgeVersion{a.Version.ID: &a.Version, b.Version.ID: &b.Version},
		relations: map[string][]*domain.KnowledgeRelation{a.Item.ID: {relation}},
		sources:   map[string][]*domain.KnowledgeSource{b.Version.ID: {source}},
	}
	r := NewScopedRetriever(f)
	results, err := r.Retrieve(context.Background(), Query{WorkspaceID: "ws_test", RequesterAgentID: "agent_test", Corpus: "prd", Terms: []string{"A"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expanded results = %d, want A and B: %+v", len(results), results)
	}
	var foundB bool
	for _, result := range results {
		if result.Entry.ID != b.Item.ID {
			continue
		}
		foundB = true
		if result.VersionID != b.Version.ID || len(result.Sources) != 1 || result.Sources[0].ID != source.ID {
			t.Fatalf("B evidence was not carried: %+v", result)
		}
		if len(result.Relations) != 1 || result.Relations[0].ID != relation.ID {
			t.Fatalf("A→B relation was not carried: %+v", result)
		}
		if result.CoverageStatus == domain.KnowledgeCoverageMissing {
			t.Fatalf("expanded result reported missing coverage: %+v", result)
		}
	}
	if !foundB {
		t.Fatalf("related item B missing from scoped result: %+v", results)
	}
}
