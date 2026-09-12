package application

import (
	"context"
	"strings"
)

// KnowledgeLibraryRetriever is the read port the Plan consult_knowledge verb
// uses. It is backed by the unified library so plan prefetch and direct
// queries read the same published release.
type KnowledgeLibraryRetriever interface {
	Retrieve(ctx context.Context, q KnowledgeRetrieveQuery) ([]KnowledgeRetrieveResult, error)
}

// KnowledgeRetrieveQuery is one prefetch request.
type KnowledgeRetrieveQuery struct {
	WorkspaceID string
	// Corpus narrows the search to documents whose kind contains this value.
	Corpus string
	Terms  []string
	Limit  int
}

// KnowledgeRetrieveResult is one retrieved knowledge unit.
type KnowledgeRetrieveResult struct {
	AssertionID    string
	DocumentID     string
	Title          string
	Version        int
	Body           string
	Snippet        string
	Score          int
	VersionID      string
	ReleaseID      string
	CoverageStatus string
	Truncated      bool
	Unknowns       []string
}

// NewKnowledgeLibraryRetriever adapts this service's library queries to the
// Plan verb. It never falls back to an unpublished draft.
func (s *Service) NewKnowledgeLibraryRetriever() KnowledgeLibraryRetriever {
	return &libraryRetriever{svc: s}
}

type libraryRetriever struct{ svc *Service }

func (r *libraryRetriever) Retrieve(ctx context.Context, q KnowledgeRetrieveQuery) ([]KnowledgeRetrieveResult, error) {
	answer, err := r.svc.QueryKnowledgeLibrary(ctx, KnowledgeLibraryQuery{
		WorkspaceID: q.WorkspaceID, Question: strings.Join(q.Terms, " "), Terms: q.Terms, Limit: q.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]KnowledgeRetrieveResult, 0, len(answer.Hits))
	for _, hit := range answer.Hits {
		if q.Corpus != "" && !strings.Contains(hit.Document.Kind, q.Corpus) {
			continue
		}
		result := KnowledgeRetrieveResult{
			AssertionID: hit.Assertion.ID, DocumentID: hit.Document.ID, Title: hit.Document.Title,
			Snippet: hit.Snippet, Score: int(hit.Score), VersionID: hit.Version.ID,
			CoverageStatus: answer.Coverage.Status, Truncated: answer.Coverage.Truncated,
			Unknowns: []string{},
		}
		if answer.Release != nil {
			result.ReleaseID = answer.Release.ID
		}
		if hit.Version.Version > 0 {
			result.Version = hit.Version.Version
		}
		var body strings.Builder
		body.WriteString(hit.Assertion.Statement)
		if len(hit.Assertion.About) > 0 {
			body.WriteString("\n\n涉及：" + strings.Join(hit.Assertion.About, "、"))
		}
		if hit.Assertion.ScopeJSON != "" && hit.Assertion.ScopeJSON != "{}" {
			body.WriteString("\n\n适用范围：" + hit.Assertion.ScopeJSON)
		}
		body.WriteString("\n\n视角：" + hit.Assertion.Perspective + "，依据：" + hit.Assertion.Basis)
		if hit.Assertion.UnknownNotes != "" {
			body.WriteString("\n\n说明与未知：" + hit.Assertion.UnknownNotes)
			result.Unknowns = append(result.Unknowns, hit.Assertion.UnknownNotes)
		}
		for _, ev := range hit.Evidence {
			body.WriteString("\n\n证据 " + ev.ID + "：" + strings.TrimSpace(ev.Excerpt))
		}
		result.Body = body.String()
		out = append(out, result)
	}
	return out, nil
}
