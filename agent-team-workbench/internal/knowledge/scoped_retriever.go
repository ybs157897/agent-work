package knowledge

import (
	"context"
	"maps"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ScopedRetriever keeps the Plan verb on the same published knowledge source
// as librarian jobs. Caller identity must be supplied by the control plane.
type ScopedRetriever struct{ reader Library }

func NewScopedRetriever(reader Library) *ScopedRetriever {
	return &ScopedRetriever{reader: reader}
}

func (r *ScopedRetriever) Retrieve(ctx context.Context, q Query) ([]Result, error) {
	scope := domain.KnowledgeScope(maps.Clone(q.Scope))
	if scope == nil {
		scope = domain.KnowledgeScope{}
	}
	if q.Corpus != "" {
		scope["domain"] = q.Corpus
	}
	query := &domain.KnowledgeQuery{WorkspaceID: q.WorkspaceID, RequesterAgentID: q.RequesterAgentID, Question: strings.Join(q.Terms, " "), Terms: q.Terms, Scope: scope, Status: domain.KnowledgeStatusEffective, Budget: domain.KnowledgeBudget{MaxResults: q.Limit}.Normalize()}
	if err := query.ValidateReadScope(); err != nil {
		return nil, err
	}
	snapshot, err := NewEngine(r.reader).Query(ctx, query)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(snapshot.Results))
	for _, h := range snapshot.Results {
		results = append(results, Result{Entry: Entry{ID: h.Item.ID, Title: h.Version.Title, Version: int(h.Version.Version), Status: string(h.Version.Status), Corpus: q.Corpus, Path: h.Version.ID, Body: h.Version.BodyMarkdown}, Score: int(h.Score), Snippet: h.Snippet, VersionID: h.Version.ID, Sources: h.Sources, Relations: h.Relations, CoverageStatus: snapshot.Coverage.Status, Truncated: snapshot.Coverage.Truncated})
	}
	return results, nil
}
