package knowledge

import (
	"context"
	"sync"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type fakeLibrary struct {
	mu            sync.Mutex
	hits          []*domain.KnowledgeHit
	items         map[string]*domain.KnowledgeItem
	versions      map[string]*domain.KnowledgeVersion
	relations     map[string][]*domain.KnowledgeRelation
	sources       map[string][]*domain.KnowledgeSource
	searchBudgets []int
	relationLimit []int
}

func (f *fakeLibrary) Search(_ context.Context, q *domain.KnowledgeQuery) ([]*domain.KnowledgeHit, error) {
	f.mu.Lock()
	f.searchBudgets = append(f.searchBudgets, q.Budget.MaxResults)
	f.mu.Unlock()
	return f.hits, nil
}

func (f *fakeLibrary) GetItem(_ context.Context, _, _, id string) (*domain.KnowledgeItem, error) {
	item, ok := f.items[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return item, nil
}

func (f *fakeLibrary) GetVersion(_ context.Context, _, _, id string) (*domain.KnowledgeVersion, error) {
	version, ok := f.versions[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return version, nil
}

func (f *fakeLibrary) ListVersionSources(_ context.Context, _, _, versionID string) ([]*domain.KnowledgeSource, error) {
	return f.sources[versionID], nil
}

func (f *fakeLibrary) ListRelations(_ context.Context, _, _, itemID string, _ domain.KnowledgeRelationDirection, limit int) ([]*domain.KnowledgeRelation, error) {
	f.mu.Lock()
	f.relationLimit = append(f.relationLimit, limit)
	f.mu.Unlock()
	relations := f.relations[itemID]
	if len(relations) > limit {
		relations = relations[:limit]
	}
	return relations, nil
}

func (f *fakeLibrary) GetSource(_ context.Context, _, _, id string) (*domain.KnowledgeSource, error) {
	for _, list := range f.sources {
		for _, source := range list {
			if source.ID == id {
				return source, nil
			}
		}
	}
	return nil, domain.ErrNotFound
}

func (f *fakeLibrary) GetIndexState(context.Context, string) (*domain.KnowledgeIndexState, error) {
	return &domain.KnowledgeIndexState{Revision: 7}, nil
}

func testHit(itemID, versionID, body string) *domain.KnowledgeHit {
	return &domain.KnowledgeHit{
		Item:    domain.KnowledgeItem{ID: itemID, CurrentVersionID: versionID, Scope: domain.KnowledgeScope{"project": "p"}},
		Version: domain.KnowledgeVersion{ID: versionID, ItemID: itemID, Version: 1, BodyMarkdown: body, Title: itemID},
	}
}

func TestEngineMarksSearchDepthAndRelationBudgetsPartial(t *testing.T) {
	a, b, c := testHit("kb_a", "kbv_a", "A"), testHit("kb_b", "kbv_b", "B"), testHit("kb_c", "kbv_c", "C")
	ab := &domain.KnowledgeRelation{ID: "kbr_ab", FromItemID: a.Item.ID, ToItemID: b.Item.ID, Kind: domain.KnowledgeRelationDependsOn}
	bc := &domain.KnowledgeRelation{ID: "kbr_bc", FromItemID: b.Item.ID, ToItemID: c.Item.ID, Kind: domain.KnowledgeRelationImpacts}
	f := &fakeLibrary{hits: []*domain.KnowledgeHit{a, b, c}, items: map[string]*domain.KnowledgeItem{
		a.Item.ID: &a.Item, b.Item.ID: &b.Item, c.Item.ID: &c.Item,
	}, versions: map[string]*domain.KnowledgeVersion{
		a.Version.ID: &a.Version, b.Version.ID: &b.Version, c.Version.ID: &c.Version,
	}, relations: map[string][]*domain.KnowledgeRelation{a.Item.ID: {ab}, b.Item.ID: {ab, bc}, c.Item.ID: {bc}}}
	engine := NewEngine(f)
	snapshot, err := engine.Query(context.Background(), &domain.KnowledgeQuery{
		WorkspaceID: "ws_test", RequesterAgentID: "agent_test", Question: "A",
		Scope: domain.KnowledgeScope{"project": "p"}, Budget: domain.KnowledgeBudget{MaxResults: 2, MaxDepth: 1, MaxRelations: 1, MaxNodes: 10, MaxBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Coverage.Status != domain.KnowledgeCoveragePartial || !snapshot.Coverage.Truncated {
		t.Fatalf("coverage = %+v, want partial/truncated", snapshot.Coverage)
	}
	if snapshot.Coverage.VisitedRelations != 1 {
		t.Fatalf("visited relations = %d, want 1", snapshot.Coverage.VisitedRelations)
	}
	f.mu.Lock()
	limits := append([]int(nil), f.relationLimit...)
	searchBudgets := append([]int(nil), f.searchBudgets...)
	f.mu.Unlock()
	if len(searchBudgets) != 1 || searchBudgets[0] != 3 {
		t.Fatalf("sentinel search budgets = %v, want [3]", searchBudgets)
	}
	for _, limit := range limits {
		if limit <= 0 {
			t.Fatalf("engine called relation repo with non-positive limit: %v", limits)
		}
	}
}

func TestEngineConflictCoverageAndByteBudget(t *testing.T) {
	a := testHit("kb_a", "kbv_a", "this body is intentionally long enough to force clipping")
	conflict := domain.KnowledgeRelation{ID: "kbr_conflict", FromItemID: a.Item.ID, ToItemID: "kb_b", Kind: domain.KnowledgeRelationConflictsWith}
	a.Relations = []domain.KnowledgeRelation{conflict}
	f := &fakeLibrary{hits: []*domain.KnowledgeHit{a}, items: map[string]*domain.KnowledgeItem{a.Item.ID: &a.Item}, versions: map[string]*domain.KnowledgeVersion{a.Version.ID: &a.Version}}
	engine := NewEngine(f)
	maxBytes := int64(180)
	snapshot, err := engine.Query(context.Background(), &domain.KnowledgeQuery{
		WorkspaceID: "ws_test", RequesterAgentID: "agent_test", Question: "A",
		Budget: domain.KnowledgeBudget{MaxResults: 5, MaxBytes: maxBytes, MaxDepth: 1, MaxRelations: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Coverage.Status != domain.KnowledgeCoverageConflict {
		t.Fatalf("conflict coverage = %+v, want conflict", snapshot.Coverage)
	}
	if len(snapshot.Results) != 1 || knowledgeHitBytes(&snapshot.Results[0]) > maxBytes {
		t.Fatalf("byte budget exceeded: results=%d bytes=%d max=%d", len(snapshot.Results), knowledgeHitBytes(&snapshot.Results[0]), maxBytes)
	}
}

func TestEngineNilClockIsConcurrentSafe(t *testing.T) {
	a := testHit("kb_a", "kbv_a", "A")
	f := &fakeLibrary{hits: []*domain.KnowledgeHit{a}, items: map[string]*domain.KnowledgeItem{a.Item.ID: &a.Item}, versions: map[string]*domain.KnowledgeVersion{a.Version.ID: &a.Version}}
	engine := &Engine{Library: f}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := engine.Query(context.Background(), &domain.KnowledgeQuery{WorkspaceID: "ws_test", RequesterAgentID: "agent_test", Question: "A"}); err != nil {
				t.Errorf("concurrent query: %v", err)
			}
		}()
	}
	wg.Wait()
	if engine.Now != nil {
		t.Fatal("nil clock must remain nil; Query must not mutate shared Engine")
	}
}
