package knowledge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Library is the small authority surface needed by the research engine.  A
// persistent implementation may be SQLite today and another store later;
// the engine never reads corpus files directly.
type Library interface {
	Search(ctx context.Context, query *domain.KnowledgeQuery) ([]*domain.KnowledgeHit, error)
	GetItem(ctx context.Context, workspaceID, requesterAgentID, itemID string) (*domain.KnowledgeItem, error)
	GetVersion(ctx context.Context, workspaceID, requesterAgentID, versionID string) (*domain.KnowledgeVersion, error)
	ListVersionSources(ctx context.Context, workspaceID, requesterAgentID, versionID string) ([]*domain.KnowledgeSource, error)
	ListRelations(ctx context.Context, workspaceID, requesterAgentID, itemID string, direction domain.KnowledgeRelationDirection, limit int) ([]*domain.KnowledgeRelation, error)
	GetSource(ctx context.Context, workspaceID, requesterAgentID, sourceID string) (*domain.KnowledgeSource, error)
	GetIndexState(ctx context.Context, workspaceID string) (*domain.KnowledgeIndexState, error)
}

// Engine performs one bounded, deterministic knowledge inquiry.  Search is
// the first step; relation expansion is breadth-first and follows both ends
// of every visible relation.  An endpoint hidden by the repository's scope
// predicate is treated as absent, so private graph cardinality never leaks.
type Engine struct {
	Library Library
	Now     func() time.Time
}

func NewEngine(library Library) *Engine {
	return &Engine{Library: library, Now: func() time.Time { return time.Now().UTC() }}
}

func (e *Engine) Query(ctx context.Context, query *domain.KnowledgeQuery) (*domain.KnowledgeQuerySnapshot, error) {
	if e == nil || e.Library == nil {
		return nil, fmt.Errorf("%w: knowledge engine library is required", domain.ErrValidation)
	}
	if err := query.ValidateReadScope(); err != nil {
		return nil, err
	}
	q := *query
	q.Budget = query.Budget.Normalize()
	if q.Direction == "" {
		q.Direction = domain.KnowledgeRelationBoth
	}
	// Ask for one sentinel result so the engine can distinguish a complete
	// result set from a result set clipped at MaxResults.  The repository may
	// clamp the sentinel at its hard ceiling; equality with that ceiling still
	// remains a partial result rather than a completeness claim.
	searchQuery := q
	if searchQuery.Budget.MaxResults < 100 {
		searchQuery.Budget.MaxResults++
	}
	hits, err := e.Library.Search(ctx, &searchQuery)
	if err != nil {
		return nil, err
	}
	if hits == nil {
		hits = []*domain.KnowledgeHit{}
	}
	searchLimited := len(hits) > q.Budget.MaxResults
	if len(hits) > q.Budget.MaxResults {
		hits = hits[:q.Budget.MaxResults]
	}
	if q.Budget.MaxResults >= 100 && len(hits) == q.Budget.MaxResults && (q.Question != "" || len(q.Terms) > 0) {
		searchLimited = true
	}
	coverage := domain.KnowledgeCoverage{Status: domain.KnowledgeCoverageComplete, Budget: q.Budget, Entries: []domain.KnowledgeCoverageEntry{}}
	if len(hits) == 0 {
		coverage.Status = domain.KnowledgeCoverageMissing
		coverage.Entries = append(coverage.Entries, domain.KnowledgeCoverageEntry{
			Subject: q.Question, Status: domain.KnowledgeCoverageMissing,
			Missing: []string{"没有找到当前权限和 scope 下的有效知识条目"},
		})
	}
	if searchLimited {
		coverage.Status = domain.KnowledgeCoveragePartial
	}

	seenItems := make(map[string]struct{}, len(hits))
	retained := make([]*domain.KnowledgeHit, 0, len(hits))
	usedBytes := int64(0)
	bytesLimited := false
	for _, hit := range hits {
		if hit != nil {
			if usedBytes+knowledgeHitBytes(hit) > q.Budget.MaxBytes {
				remaining := q.Budget.MaxBytes - usedBytes
				if remaining > 0 {
					clipped := *hit
					clipped.Version = hit.Version
					baseBytes := knowledgeHitBytesWithoutBody(hit)
					if baseBytes > remaining {
						bytesLimited = true
						break
					}
					bodyBudget := remaining - baseBytes
					if bodyBudget < 0 {
						bodyBudget = 0
					}
					clipped.Version.BodyMarkdown = truncateKnowledgeText(clipped.Version.BodyMarkdown, bodyBudget)
					retained = append(retained, &clipped)
					usedBytes = q.Budget.MaxBytes
				}
				bytesLimited = true
				break
			}
			usedBytes += knowledgeHitBytes(hit)
			retained = append(retained, hit)
			seenItems[hit.Item.ID] = struct{}{}
		}
	}
	hits = retained
	seenRelations := make(map[string]struct{})
	truncated := searchLimited || bytesLimited
	depthLimited := false
	conflictFound := false
	coverage.VisitedNodes = len(hits)
	if coverage.VisitedNodes > q.Budget.MaxNodes {
		hits = hits[:q.Budget.MaxNodes]
		coverage.VisitedNodes = len(hits)
		truncated = true
	}
	// Rebuild the seed maps after node clipping.  A clipped result is not a
	// traversed seed and must not suppress the same item when it is encountered
	// through a retained relation.
	seenItems = make(map[string]struct{}, len(hits))
	frontier := make([]engineFrontier, 0, len(hits))
	for _, hit := range hits {
		if hit == nil || hit.Item.ID == "" {
			continue
		}
		seenItems[hit.Item.ID] = struct{}{}
		frontier = append(frontier, engineFrontier{itemID: hit.Item.ID, depth: 0})
	}
	conflictFound = false
	for _, hit := range hits {
		if hit == nil {
			continue
		}
		for _, relation := range hit.Relations {
			if relation.Kind == domain.KnowledgeRelationConflictsWith {
				conflictFound = true
			}
		}
	}
	for len(frontier) > 0 {
		current := frontier[0]
		frontier = frontier[1:]
		if current.depth >= q.Budget.MaxDepth {
			depthLimited = true
			continue
		}
		remainingRelations := q.Budget.MaxRelations - coverage.VisitedRelations
		if remainingRelations <= 0 {
			truncated = true
			break
		}
		relations, relationErr := e.Library.ListRelations(ctx, q.WorkspaceID, q.RequesterAgentID,
			current.itemID, q.Direction, remainingRelations)
		if relationErr != nil {
			if errors.Is(relationErr, domain.ErrNotFound) {
				continue
			}
			return nil, relationErr
		}
		for _, relation := range relations {
			if relation == nil {
				continue
			}
			if _, ok := seenRelations[relation.ID]; ok {
				continue
			}
			if coverage.VisitedRelations >= q.Budget.MaxRelations {
				truncated = true
				break
			}
			coverage.VisitedRelations++
			seenRelations[relation.ID] = struct{}{}
			if relation.Kind == domain.KnowledgeRelationConflictsWith {
				conflictFound = true
			}
			otherID := relation.ToItemID
			if otherID == current.itemID {
				otherID = relation.FromItemID
			}
			if otherID == "" {
				continue
			}
			if _, ok := seenItems[otherID]; ok {
				continue
			}
			item, itemErr := e.Library.GetItem(ctx, q.WorkspaceID, q.RequesterAgentID, otherID)
			if itemErr != nil {
				if errors.Is(itemErr, domain.ErrNotFound) {
					// A hidden endpoint is not a missing public relation for
					// coverage accounting; do not reveal its existence.
					continue
				}
				return nil, itemErr
			}
			if !knowledgeScopeMatches(item.Scope, q.Scope) {
				continue
			}
			if coverage.VisitedNodes >= q.Budget.MaxNodes {
				truncated = true
				break
			}
			if item.CurrentVersionID == "" {
				continue
			}
			version, versionErr := e.Library.GetVersion(ctx, q.WorkspaceID, q.RequesterAgentID, item.CurrentVersionID)
			if versionErr != nil {
				if errors.Is(versionErr, domain.ErrNotFound) {
					continue
				}
				return nil, versionErr
			}
			sources, sourceErr := e.Library.ListVersionSources(ctx, q.WorkspaceID, q.RequesterAgentID, version.ID)
			if sourceErr != nil {
				if errors.Is(sourceErr, domain.ErrNotFound) {
					continue
				}
				return nil, sourceErr
			}
			seenItems[otherID] = struct{}{}
			hit := &domain.KnowledgeHit{Item: *item, Version: *version, Score: 0.1, Relations: []domain.KnowledgeRelation{*relation}}
			for _, source := range sources {
				if source != nil {
					hit.Sources = append(hit.Sources, *source)
				}
			}
			if usedBytes+knowledgeHitBytes(hit) > q.Budget.MaxBytes {
				remaining := q.Budget.MaxBytes - usedBytes
				if remaining > 0 {
					clipped := *hit
					clipped.Version = hit.Version
					baseBytes := knowledgeHitBytesWithoutBody(hit)
					if baseBytes > remaining {
						truncated = true
						break
					}
					bodyBudget := remaining - baseBytes
					if bodyBudget < 0 {
						bodyBudget = 0
					}
					clipped.Version.BodyMarkdown = truncateKnowledgeText(clipped.Version.BodyMarkdown, bodyBudget)
					hits = append(hits, &clipped)
					usedBytes = q.Budget.MaxBytes
				}
				truncated = true
				break
			}
			usedBytes += knowledgeHitBytes(hit)
			hits = append(hits, hit)
			coverage.VisitedNodes++
			frontier = append(frontier, engineFrontier{itemID: otherID, depth: current.depth + 1})
		}
	}

	// Keep coverage tied to material actually returned.  The budget is an
	// explicit stopping reason; a bounded answer is never labelled complete.
	if depthLimited {
		truncated = true
	}
	if truncated {
		coverage.Truncated = true
		coverage.Status = domain.KnowledgeCoveragePartial
	}
	if conflictFound {
		coverage.Status = domain.KnowledgeCoverageConflict
	}
	for _, hit := range hits {
		if hit == nil {
			continue
		}
		entryStatus := domain.KnowledgeCoverageComplete
		if conflictFound {
			entryStatus = domain.KnowledgeCoverageConflict
		} else if truncated {
			entryStatus = domain.KnowledgeCoveragePartial
		}
		entry := domain.KnowledgeCoverageEntry{Subject: hit.Item.ID, Status: entryStatus}
		for _, source := range hit.Sources {
			entry.EvidenceIDs = append(entry.EvidenceIDs, source.ID)
		}
		for _, relation := range hit.Relations {
			other := relation.ToItemID
			if other == hit.Item.ID {
				other = relation.FromItemID
			}
			if other != "" {
				entry.RelatedItemIDs = append(entry.RelatedItemIDs, other)
			}
		}
		coverage.Entries = append(coverage.Entries, entry)
	}
	if len(coverage.Entries) == 0 && coverage.Status == domain.KnowledgeCoverageComplete {
		coverage.Status = domain.KnowledgeCoverageMissing
	}
	indexRevision := int64(0)
	if state, stateErr := e.Library.GetIndexState(ctx, q.WorkspaceID); stateErr != nil {
		return nil, stateErr
	} else if state != nil {
		indexRevision = state.Revision
	}
	now := time.Now().UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	snapshot := &domain.KnowledgeQuerySnapshot{ID: domain.NewID(domain.PrefixKnowledgeQuerySnapshot),
		WorkspaceID: q.WorkspaceID, RequesterAgentID: q.RequesterAgentID, Question: q.Question,
		Context: q.Context, Budget: q.Budget, IndexRevision: indexRevision,
		Results: make([]domain.KnowledgeHit, 0, len(hits)), Coverage: coverage, CreatedAt: now}
	for _, hit := range hits {
		if hit != nil {
			snapshot.Results = append(snapshot.Results, *hit)
		}
	}
	return snapshot, nil
}

func knowledgeHitBytes(hit *domain.KnowledgeHit) int64 {
	if hit == nil {
		return 0
	}
	var n int64
	n += int64(len(hit.Item.Title) + len(hit.Item.Summary) + len(hit.Version.Title) + len(hit.Version.Summary) + len(hit.Version.BodyMarkdown) + len(hit.Snippet))
	for _, source := range hit.Sources {
		n += int64(len(source.ID) + len(source.Ref) + len(source.Locator) + len(source.Excerpt))
	}
	for _, relation := range hit.Relations {
		n += int64(len(relation.ID) + len(relation.FromItemID) + len(relation.ToItemID) + len(relation.Condition) + len(relation.Rationale))
	}
	return n
}

func knowledgeHitBytesWithoutBody(hit *domain.KnowledgeHit) int64 {
	if hit == nil {
		return 0
	}
	clipped := *hit
	clipped.Version = hit.Version
	clipped.Version.BodyMarkdown = ""
	return knowledgeHitBytes(&clipped)
}

func truncateKnowledgeText(value string, maxBytes int64) string {
	if maxBytes <= 0 || int64(len(value)) <= maxBytes {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && int64(len(string(runes))) > maxBytes {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func knowledgeScopeMatches(actual, wanted domain.KnowledgeScope) bool {
	for key, value := range wanted {
		got, ok := actual[key]
		if !ok || !reflect.DeepEqual(got, value) {
			return false
		}
	}
	return true
}

type engineFrontier struct {
	itemID string
	depth  int
}
