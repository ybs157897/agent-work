package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// KnowledgeRepo is the SQLite authority for the knowledge librarian.  The
// search index is only a projection of effective versions; all public reads
// join the item scope predicate so a private item cannot be reached through a
// version, relation, source, or snapshot identifier.
type KnowledgeRepo struct{ store *Store }

var _ application.KnowledgeRepo = (*KnowledgeRepo)(nil)

const (
	defaultKnowledgeLimit = 20
	maxKnowledgeLimit     = 1000
	maxKnowledgeDepth     = 8
)

const knowledgeItemCols = `id, workspace_id, owner_agent_id, visibility, kind, title,
	summary, tags, aliases, scope_json, current_version_id, current_version, status,
	repeal_reason, version, created_at, updated_at`

const knowledgeItemColsQualified = `i.id, i.workspace_id, i.owner_agent_id, i.visibility, i.kind, i.title,
	i.summary, i.tags, i.aliases, i.scope_json, i.current_version_id, i.current_version, i.status,
	i.repeal_reason, i.version, i.created_at, i.updated_at`

const knowledgeVersionColsQualified = `v.id, v.item_id, v.version, v.base_version, v.status, v.kind, v.title,
	v.summary, v.body_markdown, v.tags, v.aliases, v.scope_json, v.metadata_json, v.content_digest,
	v.created_by_agent_id, v.created_by_run_id, v.created_by_work_item_id,
	v.supersedes_version_id, v.published_at, v.created_at`

const knowledgeSourceColsQualified = `s.id, s.workspace_id, s.submitted_by_agent_id, s.kind, s.ref,
	s.locator, s.excerpt, s.digest, s.metadata_json, s.created_at`

const knowledgeRelationColsQualified = `r.id, r.workspace_id, r.source_version_id, r.from_item_id,
	r.to_item_id, r.relation_kind, r.condition, r.rationale, r.created_at`

const knowledgeVersionCols = `id, item_id, version, base_version, status, kind, title,
	summary, body_markdown, tags, aliases, scope_json, metadata_json, content_digest,
	created_by_agent_id, created_by_run_id, created_by_work_item_id,
	supersedes_version_id, published_at, created_at`

const knowledgeSourceCols = `id, workspace_id, submitted_by_agent_id, kind, ref,
	locator, excerpt, digest, metadata_json, created_at`

const knowledgeRelationCols = `id, workspace_id, source_version_id, from_item_id,
	to_item_id, relation_kind, condition, rationale, created_at`

const knowledgeSubmissionCols = `id, workspace_id, agent_id, run_id, work_item_id,
	client_key, request_json, request_digest, status, result_item_ids,
	result_version_ids, error_message, version, created_at, updated_at`

func scanKnowledgeItem(row interface{ Scan(...any) error }) (*domain.KnowledgeItem, error) {
	item := &domain.KnowledgeItem{}
	var ownerID, currentID *string
	var tags, aliases, scope string
	var created, updated scanTime
	if err := row.Scan(&item.ID, &item.WorkspaceID, &ownerID, &item.Visibility, &item.Kind,
		&item.Title, &item.Summary, &tags, &aliases, &scope, &currentID,
		&item.CurrentVersion, &item.Status, &item.RepealReason, &item.Version, &created, &updated); err != nil {
		return nil, err
	}
	if ownerID != nil {
		item.OwnerAgentID = *ownerID
	}
	if currentID != nil {
		item.CurrentVersionID = *currentID
	}
	if err := jsonInto(tags, &item.Tags); err != nil {
		return nil, err
	}
	if err := jsonInto(aliases, &item.Aliases); err != nil {
		return nil, err
	}
	if err := jsonInto(scope, &item.Scope); err != nil {
		return nil, err
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if item.Aliases == nil {
		item.Aliases = []string{}
	}
	if item.Scope == nil {
		item.Scope = domain.KnowledgeScope{}
	}
	item.CreatedAt, item.UpdatedAt = mustTime(created), mustTime(updated)
	return item, nil
}

func scanKnowledgeVersion(row interface{ Scan(...any) error }) (*domain.KnowledgeVersion, error) {
	version := &domain.KnowledgeVersion{}
	var created, published scanTime
	var tags, aliases, scope, metadata string
	var runID, workItemID, supersedes *string
	if err := row.Scan(&version.ID, &version.ItemID, &version.Version, &version.BaseVersion,
		&version.Status, &version.Kind, &version.Title, &version.Summary,
		&version.BodyMarkdown, &tags, &aliases, &scope, &metadata, &version.ContentDigest,
		&version.CreatedByAgentID, &runID, &workItemID, &supersedes, &published, &created); err != nil {
		return nil, err
	}
	if err := jsonInto(tags, &version.Tags); err != nil {
		return nil, err
	}
	if err := jsonInto(aliases, &version.Aliases); err != nil {
		return nil, err
	}
	if err := jsonInto(scope, &version.Scope); err != nil {
		return nil, err
	}
	if err := jsonInto(metadata, &version.Metadata); err != nil {
		return nil, err
	}
	if runID != nil {
		version.CreatedByRunID = *runID
	}
	if workItemID != nil {
		version.CreatedByWorkItemID = *workItemID
	}
	if supersedes != nil {
		version.SupersedesVersionID = *supersedes
	}
	version.PublishedAt = optTime(published)
	version.CreatedAt = mustTime(created)
	if version.Tags == nil {
		version.Tags = []string{}
	}
	if version.Aliases == nil {
		version.Aliases = []string{}
	}
	if len(version.Scope) == 0 {
		version.Scope = domain.KnowledgeScope{}
	}
	if version.Metadata == nil {
		version.Metadata = map[string]any{}
	}
	return version, nil
}

func scanKnowledgeSource(row interface{ Scan(...any) error }) (*domain.KnowledgeSource, error) {
	source := &domain.KnowledgeSource{}
	var metadata string
	var created scanTime
	if err := row.Scan(&source.ID, &source.WorkspaceID, &source.SubmittedByAgentID,
		&source.Kind, &source.Ref, &source.Locator, &source.Excerpt, &source.Digest,
		&metadata, &created); err != nil {
		return nil, err
	}
	if err := jsonInto(metadata, &source.Metadata); err != nil {
		return nil, err
	}
	if source.Metadata == nil {
		source.Metadata = map[string]any{}
	}
	source.CreatedAt = mustTime(created)
	return source, nil
}

func scanKnowledgeRelation(row interface{ Scan(...any) error }) (*domain.KnowledgeRelation, error) {
	relation := &domain.KnowledgeRelation{}
	var created scanTime
	if err := row.Scan(&relation.ID, &relation.WorkspaceID, &relation.SourceVersionID,
		&relation.FromItemID, &relation.ToItemID, &relation.Kind, &relation.Condition,
		&relation.Rationale, &created); err != nil {
		return nil, err
	}
	relation.CreatedAt = mustTime(created)
	relation.SourceIDs = []string{}
	return relation, nil
}

func scanKnowledgeSubmission(row interface{ Scan(...any) error }) (*domain.KnowledgeSubmission, error) {
	submission := &domain.KnowledgeSubmission{}
	var runID, workItemID *string
	var requestJSON, resultItemIDs, resultVersionIDs string
	var created, updated scanTime
	if err := row.Scan(&submission.ID, &submission.WorkspaceID, &submission.AgentID,
		&runID, &workItemID, &submission.ClientKey, &requestJSON, &submission.RequestDigest,
		&submission.Status, &resultItemIDs, &resultVersionIDs, &submission.ErrorMessage,
		&submission.Version, &created, &updated); err != nil {
		return nil, err
	}
	if runID != nil {
		submission.RunID = *runID
	}
	if workItemID != nil {
		submission.WorkItemID = *workItemID
	}
	if err := json.Unmarshal([]byte(requestJSON), &submission.Request); err != nil {
		return nil, err
	}
	if err := jsonInto(resultItemIDs, &submission.ResultItemIDs); err != nil {
		return nil, err
	}
	if err := jsonInto(resultVersionIDs, &submission.ResultVersionIDs); err != nil {
		return nil, err
	}
	if submission.ResultItemIDs == nil {
		submission.ResultItemIDs = []string{}
	}
	if submission.ResultVersionIDs == nil {
		submission.ResultVersionIDs = []string{}
	}
	submission.CreatedAt, submission.UpdatedAt = mustTime(created), mustTime(updated)
	return submission, nil
}

func requireKnowledgeReadScope(workspaceID, requesterAgentID string) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(requesterAgentID) == "" {
		return fmt.Errorf("%w: knowledge read requires workspace and requester agent", domain.ErrValidation)
	}
	return nil
}

// requireKnowledgeReadScopeForRepo closes the caller-supplied Agent identity
// gap. A workspace-scoped read may use the explicit human management sentinel,
// but every Agent requester must be a live Agent belonging to that same
// Workspace before visibility predicates are evaluated.
func (r *KnowledgeRepo) requireKnowledgeReadScopeForRepo(ctx context.Context, workspaceID, requesterAgentID string) error {
	if err := requireKnowledgeReadScope(workspaceID, requesterAgentID); err != nil {
		return err
	}
	if requesterAgentID == "human:shared" {
		return nil
	}
	ownerWorkspace, err := r.agentWorkspace(ctx, requesterAgentID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.ErrNotFound
		}
		return err
	}
	if ownerWorkspace != workspaceID {
		return domain.ErrNotFound
	}
	return nil
}

func (r *KnowledgeRepo) agentWorkspace(ctx context.Context, agentID string) (string, error) {
	if strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("%w: knowledge agent identity is required", domain.ErrValidation)
	}
	var workspaceID string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM agent_profiles WHERE id=?`, agentID).Scan(&workspaceID); err != nil {
		return "", r.store.mapErr(err)
	}
	return workspaceID, nil
}

func knowledgeLimit(limit int) int {
	if limit <= 0 {
		return defaultKnowledgeLimit
	}
	return min(limit, maxKnowledgeLimit)
}

func (r *KnowledgeRepo) CreateItem(ctx context.Context, item *domain.KnowledgeItem) error {
	return r.store.InTx(ctx, func(ctx context.Context) error {
		if item == nil {
			return fmt.Errorf("%w: knowledge item required", domain.ErrValidation)
		}
		if item.Version == 0 {
			item.Version = 1
		}
		if item.Status == "" {
			item.Status = domain.KnowledgeStatusCandidate
		}
		if item.Visibility == "" {
			item.Visibility = domain.KnowledgeVisibilityWorkspace
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = timeNow()
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = item.CreatedAt
		}
		item.Tags = domain.NormalizeKnowledgeTags(item.Tags)
		item.Aliases = domain.NormalizeKnowledgeTags(item.Aliases)
		if item.Scope == nil {
			item.Scope = domain.KnowledgeScope{}
		}
		if err := item.Validate(); err != nil {
			return err
		}
		if item.OwnerAgentID != "" {
			if ownerWorkspace, err := r.agentWorkspace(ctx, item.OwnerAgentID); err != nil {
				return err
			} else if ownerWorkspace != item.WorkspaceID {
				return fmt.Errorf("%w: knowledge item owner is outside workspace", domain.ErrValidation)
			}
		}
		_, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO knowledge_items(`+knowledgeItemCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.ID, item.WorkspaceID, nullString(item.OwnerAgentID), item.Visibility,
			item.Kind, item.Title, item.Summary, jsonText(item.Tags), jsonText(item.Aliases),
			jsonText(item.Scope), nullString(item.CurrentVersionID), item.CurrentVersion,
			item.Status, item.RepealReason, item.Version, timeParam(item.CreatedAt), timeParam(item.UpdatedAt))
		return r.store.mapErr(err)
	})
}

func (r *KnowledgeRepo) GetItem(ctx context.Context, workspaceID, requesterAgentID, itemID string) (*domain.KnowledgeItem, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	item, err := scanKnowledgeItem(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeItemCols+` FROM knowledge_items
		 WHERE id=? AND workspace_id=? AND (visibility=? OR owner_agent_id=?)`,
		itemID, workspaceID, domain.KnowledgeVisibilityWorkspace, requesterAgentID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return item, nil
}

func (r *KnowledgeRepo) ListVisibleItems(ctx context.Context, workspaceID, requesterAgentID string,
	status domain.KnowledgeStatus, limit int) ([]*domain.KnowledgeItem, error) {
	items, _, err := r.ListVisibleItemsPage(ctx, workspaceID, requesterAgentID, domain.KnowledgeListOptions{Status: status, Limit: limit})
	return items, err
}

func (r *KnowledgeRepo) ListVisibleItemsPage(ctx context.Context, workspaceID, requesterAgentID string,
	options domain.KnowledgeListOptions) ([]*domain.KnowledgeItem, string, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, "", err
	}
	options = options.Normalize()
	if err := options.Validate(); err != nil {
		return nil, "", err
	}
	if !options.Status.Valid() {
		return nil, "", fmt.Errorf("%w: invalid knowledge status %q", domain.ErrValidation, options.Status)
	}
	if options.Visibility != "" && !options.Visibility.Valid() {
		return nil, "", fmt.Errorf("%w: invalid knowledge visibility %q", domain.ErrValidation, options.Visibility)
	}
	if strings.TrimSpace(options.Query) != "" {
		return r.searchVisibleItemsPage(ctx, workspaceID, requesterAgentID, options)
	}
	args := []any{workspaceID}
	q := `SELECT ` + knowledgeItemCols + ` FROM knowledge_items
		WHERE workspace_id=? AND status=?`
	args = append(args, options.Status)
	switch options.Visibility {
	case domain.KnowledgeVisibilityPrivate:
		q += ` AND visibility=? AND owner_agent_id=?`
		args = append(args, domain.KnowledgeVisibilityPrivate, requesterAgentID)
	case domain.KnowledgeVisibilityWorkspace:
		q += ` AND visibility=?`
		args = append(args, domain.KnowledgeVisibilityWorkspace)
	default:
		q += ` AND (visibility=? OR owner_agent_id=?)`
		args = append(args, domain.KnowledgeVisibilityWorkspace, requesterAgentID)
	}
	if options.OwnerAgentID != "" {
		q += ` AND owner_agent_id=?`
		args = append(args, options.OwnerAgentID)
	}
	if options.Kind != "" {
		q += ` AND kind=?`
		args = append(args, options.Kind)
	}
	q, args = appendKnowledgeScopeFilters(q, args, "scope_json", options.Scope)
	if options.AfterID != "" {
		q += ` AND id<?`
		args = append(args, options.AfterID)
	}
	limit := options.Limit
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, "", r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeItem
	for rows.Next() {
		item, scanErr := scanKnowledgeItem(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	nextCursor := ""
	if len(out) > limit {
		// The cursor represents the last row returned to the caller.  The
		// extra row is only a sentinel proving that another page exists; using
		// it as the cursor would skip the first row of the next page.
		nextCursor = out[limit-1].ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

type knowledgeSearchItem struct {
	item      *domain.KnowledgeItem
	excerpt   string
	matchText string
}

// searchVisibleItemsPage keeps the list response on the published SQLite
// projection.  The keyset is item ID rather than FTS rank: ranking is useful
// for the excerpt, but an ID order is stable when a caller follows a cursor
// across multiple pages.
func (r *KnowledgeRepo) searchVisibleItemsPage(ctx context.Context, workspaceID, requesterAgentID string,
	options domain.KnowledgeListOptions) ([]*domain.KnowledgeItem, string, error) {
	terms := knowledgeSearchTerms(options.Query)
	if len(terms) == 0 {
		return []*domain.KnowledgeItem{}, "", nil
	}
	if options.Status == domain.KnowledgeStatusEffective {
		return r.searchEffectiveItemsPage(ctx, workspaceID, requesterAgentID, options, terms)
	}
	return r.searchNonEffectiveItemsPage(ctx, workspaceID, requesterAgentID, options, terms)
}

func (r *KnowledgeRepo) searchEffectiveItemsPage(ctx context.Context, workspaceID, requesterAgentID string,
	options domain.KnowledgeListOptions, terms []string) ([]*domain.KnowledgeItem, string, error) {
	q := `SELECT ` + knowledgeItemColsQualified + `,
		snippet(knowledge_index, 8, '[', ']', '…', 16),
		ki.title, ki.summary, ki.body, ki.aliases, ki.tags
		FROM knowledge_index ki JOIN knowledge_items i ON i.id=ki.item_id
			AND i.workspace_id=ki.workspace_id AND i.current_version_id=ki.version_id
		WHERE 1=1`
	args := make([]any, 0, len(terms)*2+12)
	q, args = appendKnowledgeItemReadFilters(q, args, workspaceID, requesterAgentID, options)
	q += ` AND knowledge_index MATCH ?`
	args = append(args, strings.Join(quoteKnowledgeTokens(terms), " OR "))
	if options.AfterID != "" {
		q += ` AND i.id<?`
		args = append(args, options.AfterID)
	}
	q += ` ORDER BY i.id DESC LIMIT ?`
	limit := options.Limit
	args = append(args, limit+1)
	ftsRows, err := r.scanKnowledgeSearchRows(ctx, q, args, terms)
	if err != nil {
		return nil, "", err
	}

	// SQLite FTS5 rejects MATCH when it is placed in an OR expression. Run the
	// existing Chinese substring fallback as a second bounded query and merge
	// the two sorted streams below. Each stream is limited to limit+1, so a
	// large corpus is never materialized in memory.
	var fallbackRows []*knowledgeSearchItem
	if containsKnowledgeHan(terms) || len(knowledgeExactItemIDs(terms)) > 0 {
		fallbackRows, err = r.searchEffectiveSubstringRows(ctx, workspaceID, requesterAgentID, options, terms)
		if err != nil {
			return nil, "", err
		}
	}
	items, nextCursor := mergeKnowledgeSearchRows(ftsRows, fallbackRows, limit)
	return items, nextCursor, nil
}

func (r *KnowledgeRepo) searchEffectiveSubstringRows(ctx context.Context, workspaceID, requesterAgentID string,
	options domain.KnowledgeListOptions, terms []string) ([]*knowledgeSearchItem, error) {
	q := `SELECT ` + knowledgeItemColsQualified + `,
		'', ki.title, ki.summary, ki.body, ki.aliases, ki.tags
		FROM knowledge_index ki JOIN knowledge_items i ON i.id=ki.item_id
			AND i.workspace_id=ki.workspace_id AND i.current_version_id=ki.version_id
		WHERE 1=1`
	args := make([]any, 0, len(terms)+12)
	q, args = appendKnowledgeItemReadFilters(q, args, workspaceID, requesterAgentID, options)
	matchPredicates := make([]string, 0, len(terms)+1)
	if containsKnowledgeHan(terms) {
		for _, term := range terms {
			matchPredicates = append(matchPredicates, `LOWER(COALESCE(ki.title,'') || ' ' || COALESCE(ki.summary,'') || ' ' ||
				COALESCE(ki.body,'') || ' ' || COALESCE(ki.aliases,'') || ' ' || COALESCE(ki.tags,'')) LIKE ? ESCAPE '!'`)
			args = append(args, knowledgeLikePattern(term))
		}
	}
	if exactIDs := knowledgeExactItemIDs(terms); len(exactIDs) > 0 {
		placeholders := make([]string, len(exactIDs))
		for i, id := range exactIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		matchPredicates = append(matchPredicates, `LOWER(i.id) IN (`+strings.Join(placeholders, ",")+")")
	}
	if len(matchPredicates) == 0 {
		return nil, nil
	}
	q += ` AND (` + strings.Join(matchPredicates, ` OR `) + `)`
	if options.AfterID != "" {
		q += ` AND i.id<?`
		args = append(args, options.AfterID)
	}
	q += ` ORDER BY i.id DESC LIMIT ?`
	args = append(args, options.Limit+1)
	return r.scanKnowledgeSearchRows(ctx, q, args, terms)
}

func (r *KnowledgeRepo) searchNonEffectiveItemsPage(ctx context.Context, workspaceID, requesterAgentID string,
	options domain.KnowledgeListOptions, terms []string) ([]*domain.KnowledgeItem, string, error) {
	q := `SELECT ` + knowledgeItemColsQualified + `,
		'', COALESCE(v.title, i.title), COALESCE(v.summary, i.summary),
		COALESCE(v.body_markdown, ''), COALESCE(v.aliases, i.aliases), COALESCE(v.tags, i.tags)
		FROM knowledge_items i
		LEFT JOIN knowledge_versions v ON v.id=i.current_version_id AND v.item_id=i.id
		WHERE 1=1`
	args := make([]any, 0, len(terms)+12)
	q, args = appendKnowledgeItemReadFilters(q, args, workspaceID, requesterAgentID, options)
	textExpr := `LOWER(COALESCE(i.title,'') || ' ' || COALESCE(i.summary,'') || ' ' ||
		COALESCE(v.title,'') || ' ' || COALESCE(v.summary,'') || ' ' || COALESCE(v.body_markdown,'') || ' ' ||
		COALESCE(v.aliases,'') || ' ' || COALESCE(v.tags,''))`
	matchPredicates := make([]string, 0, len(terms)+1)
	for _, term := range terms {
		matchPredicates = append(matchPredicates, textExpr+` LIKE ? ESCAPE '!'`)
		args = append(args, knowledgeLikePattern(term))
	}
	if exactIDs := knowledgeExactItemIDs(terms); len(exactIDs) > 0 {
		placeholders := make([]string, len(exactIDs))
		for i, id := range exactIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		matchPredicates = append(matchPredicates, `LOWER(i.id) IN (`+strings.Join(placeholders, ",")+")")
	}
	q += ` AND (` + strings.Join(matchPredicates, ` OR `) + `)`
	if options.AfterID != "" {
		q += ` AND i.id<?`
		args = append(args, options.AfterID)
	}
	q += ` ORDER BY i.id DESC LIMIT ?`
	limit := options.Limit
	args = append(args, limit+1)
	rows, err := r.scanKnowledgeSearchRows(ctx, q, args, terms)
	if err != nil {
		return nil, "", err
	}
	items, nextCursor := mergeKnowledgeSearchRows(rows, nil, limit)
	return items, nextCursor, nil
}

func appendKnowledgeItemReadFilters(sqlText string, args []any, workspaceID, requesterAgentID string,
	options domain.KnowledgeListOptions) (string, []any) {
	sqlText += ` AND i.workspace_id=? AND i.status=?`
	args = append(args, workspaceID, options.Status)
	switch options.Visibility {
	case domain.KnowledgeVisibilityPrivate:
		sqlText += ` AND i.visibility=? AND i.owner_agent_id=?`
		args = append(args, domain.KnowledgeVisibilityPrivate, requesterAgentID)
	case domain.KnowledgeVisibilityWorkspace:
		sqlText += ` AND i.visibility=?`
		args = append(args, domain.KnowledgeVisibilityWorkspace)
	default:
		sqlText += ` AND (i.visibility=? OR i.owner_agent_id=?)`
		args = append(args, domain.KnowledgeVisibilityWorkspace, requesterAgentID)
	}
	if options.OwnerAgentID != "" {
		sqlText += ` AND i.owner_agent_id=?`
		args = append(args, options.OwnerAgentID)
	}
	if options.Kind != "" {
		sqlText += ` AND i.kind=?`
		args = append(args, options.Kind)
	}
	return appendKnowledgeScopeFilters(sqlText, args, "i.scope_json", options.Scope)
}

func (r *KnowledgeRepo) scanKnowledgeSearchRows(ctx context.Context, sqlText string, args []any,
	terms []string) ([]*knowledgeSearchItem, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx), sqlText, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	out := make([]*knowledgeSearchItem, 0)
	for rows.Next() {
		result, scanErr := scanKnowledgeSearchItem(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if result.excerpt == "" {
			result.excerpt = knowledgeSubstringSnippet(result.matchText, terms)
		}
		result.item.SearchExcerpt = result.excerpt
		out = append(out, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func mergeKnowledgeSearchRows(primary, fallback []*knowledgeSearchItem, limit int) ([]*domain.KnowledgeItem, string) {
	merged := make([]*domain.KnowledgeItem, 0, min(limit+1, defaultKnowledgeLimit+1))
	seen := make(map[string]*domain.KnowledgeItem, len(primary)+len(fallback))
	for p, f := 0, 0; (p < len(primary) || f < len(fallback)) && len(merged) <= limit; {
		var next *knowledgeSearchItem
		if f >= len(fallback) || (p < len(primary) && primary[p].item.ID >= fallback[f].item.ID) {
			next = primary[p]
			p++
		} else {
			next = fallback[f]
			f++
		}
		if existing, ok := seen[next.item.ID]; ok {
			if existing.SearchExcerpt == "" && next.item.SearchExcerpt != "" {
				existing.SearchExcerpt = next.item.SearchExcerpt
			}
			continue
		}
		seen[next.item.ID] = next.item
		merged = append(merged, next.item)
	}
	nextCursor := ""
	if len(merged) > limit {
		nextCursor = merged[limit-1].ID
		merged = merged[:limit]
	}
	return merged, nextCursor
}

func scanKnowledgeSearchItem(row interface{ Scan(...any) error }) (*knowledgeSearchItem, error) {
	item := &domain.KnowledgeItem{}
	var ownerID, currentID *string
	var tags, aliases, scope string
	var created, updated scanTime
	var excerpt, title, summary, body, searchAliases, searchTags string
	if err := row.Scan(&item.ID, &item.WorkspaceID, &ownerID, &item.Visibility, &item.Kind,
		&item.Title, &item.Summary, &tags, &aliases, &scope, &currentID,
		&item.CurrentVersion, &item.Status, &item.RepealReason, &item.Version, &created, &updated,
		&excerpt, &title, &summary, &body, &searchAliases, &searchTags); err != nil {
		return nil, err
	}
	if ownerID != nil {
		item.OwnerAgentID = *ownerID
	}
	if currentID != nil {
		item.CurrentVersionID = *currentID
	}
	if err := jsonInto(tags, &item.Tags); err != nil {
		return nil, err
	}
	if err := jsonInto(aliases, &item.Aliases); err != nil {
		return nil, err
	}
	if err := jsonInto(scope, &item.Scope); err != nil {
		return nil, err
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	if item.Aliases == nil {
		item.Aliases = []string{}
	}
	if item.Scope == nil {
		item.Scope = domain.KnowledgeScope{}
	}
	item.CreatedAt, item.UpdatedAt = mustTime(created), mustTime(updated)
	return &knowledgeSearchItem{item: item, excerpt: excerpt,
		matchText: strings.Join([]string{title, summary, body, searchAliases, searchTags}, " ")}, nil
}

func (r *KnowledgeRepo) CreateVersion(ctx context.Context, version *domain.KnowledgeVersion) error {
	return r.store.InTx(ctx, func(ctx context.Context) error {
		return r.createVersion(ctx, version)
	})
}

func (r *KnowledgeRepo) createVersion(ctx context.Context, version *domain.KnowledgeVersion) error {
	if version == nil {
		return fmt.Errorf("%w: knowledge version required", domain.ErrValidation)
	}
	if version.Status == "" {
		version.Status = domain.KnowledgeStatusCandidate
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = timeNow()
	}
	version.Tags = domain.NormalizeKnowledgeTags(version.Tags)
	version.Aliases = domain.NormalizeKnowledgeTags(version.Aliases)
	if len(version.Scope) == 0 {
		version.Scope = domain.KnowledgeScope{}
		var inheritedScope string
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT scope_json FROM knowledge_items WHERE id=?`, version.ItemID).Scan(&inheritedScope); err != nil {
			return r.store.mapErr(err)
		}
		if err := jsonInto(inheritedScope, &version.Scope); err != nil {
			return err
		}
		if version.Scope == nil {
			version.Scope = domain.KnowledgeScope{}
		}
	}
	if version.Metadata == nil {
		version.Metadata = map[string]any{}
	}
	var itemWorkspace string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM knowledge_items WHERE id=?`, version.ItemID).Scan(&itemWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if agentWorkspace, err := r.agentWorkspace(ctx, version.CreatedByAgentID); err != nil {
		return err
	} else if agentWorkspace != itemWorkspace {
		return fmt.Errorf("%w: knowledge version creator is outside item workspace", domain.ErrValidation)
	}
	if version.Version == 0 {
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT COALESCE(MAX(version),0)+1 FROM knowledge_versions WHERE item_id=?`, version.ItemID).
			Scan(&version.Version); err != nil {
			return err
		}
	}
	if err := version.SealContent(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_versions(`+knowledgeVersionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		version.ID, version.ItemID, version.Version, version.BaseVersion, version.Status,
		version.Kind, version.Title, version.Summary, version.BodyMarkdown,
		jsonText(version.Tags), jsonText(version.Aliases), jsonText(version.Scope),
		jsonText(version.Metadata), version.ContentDigest, version.CreatedByAgentID,
		nullString(version.CreatedByRunID), nullString(version.CreatedByWorkItemID),
		nullString(version.SupersedesVersionID), nullTimeParam(version.PublishedAt),
		timeParam(version.CreatedAt))
	return r.store.mapErr(err)
}

func (r *KnowledgeRepo) CreateVersionBundle(ctx context.Context, version *domain.KnowledgeVersion,
	sources []*domain.KnowledgeSource, relations []*domain.KnowledgeRelation) error {
	return r.store.InTx(ctx, func(ctx context.Context) error {
		if err := r.createVersion(ctx, version); err != nil {
			return err
		}
		var itemWorkspace string
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT workspace_id FROM knowledge_items WHERE id=?`, version.ItemID).Scan(&itemWorkspace); err != nil {
			return r.store.mapErr(err)
		}
		for _, source := range sources {
			if source == nil {
				return fmt.Errorf("%w: nil knowledge source", domain.ErrValidation)
			}
			if source.ID == "" {
				source.ID = domain.NewID(domain.PrefixKnowledgeSource)
			}
			if source.WorkspaceID == "" {
				source.WorkspaceID = itemWorkspace
			}
			if source.WorkspaceID != itemWorkspace {
				return fmt.Errorf("%w: knowledge source is outside item workspace", domain.ErrValidation)
			}
			if err := r.createSource(ctx, source); err != nil {
				return err
			}
			if err := r.linkVersionSource(ctx, &domain.KnowledgeVersionSource{VersionID: version.ID, SourceID: source.ID, Role: "primary"}); err != nil {
				return err
			}
		}
		for _, relation := range relations {
			if relation == nil {
				return fmt.Errorf("%w: nil knowledge relation", domain.ErrValidation)
			}
			copyRelation := *relation
			if copyRelation.SourceVersionID == "" {
				copyRelation.SourceVersionID = version.ID
			}
			if copyRelation.FromItemID == "" {
				copyRelation.FromItemID = version.ItemID
			}
			if copyRelation.WorkspaceID == "" {
				copyRelation.WorkspaceID = itemWorkspace
			}
			if copyRelation.WorkspaceID != itemWorkspace {
				return fmt.Errorf("%w: knowledge relation is outside item workspace", domain.ErrValidation)
			}
			if err := r.createRelation(ctx, &copyRelation); err != nil {
				return err
			}
			for _, sourceID := range copyRelation.SourceIDs {
				if err := r.linkRelationSource(ctx, copyRelation.ID, sourceID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (r *KnowledgeRepo) GetVersion(ctx context.Context, workspaceID, requesterAgentID, versionID string) (*domain.KnowledgeVersion, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	version, err := scanKnowledgeVersion(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeVersionColsQualified+` FROM knowledge_versions v
		 JOIN knowledge_items i ON i.id=v.item_id
		 WHERE v.id=? AND i.workspace_id=? AND (i.visibility=? OR i.owner_agent_id=?)`,
		versionID, workspaceID, domain.KnowledgeVisibilityWorkspace, requesterAgentID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return version, nil
}

func (r *KnowledgeRepo) ListVersions(ctx context.Context, workspaceID, requesterAgentID, itemID string,
	status domain.KnowledgeStatus) ([]*domain.KnowledgeVersion, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	args := []any{itemID, workspaceID, domain.KnowledgeVisibilityWorkspace, requesterAgentID}
	q := `SELECT ` + knowledgeVersionColsQualified + ` FROM knowledge_versions v
		JOIN knowledge_items i ON i.id=v.item_id
		WHERE v.item_id=? AND i.workspace_id=? AND (i.visibility=? OR i.owner_agent_id=?)`
	if status != "" {
		if !status.Valid() {
			return nil, fmt.Errorf("%w: invalid knowledge status %q", domain.ErrValidation, status)
		}
		q += ` AND v.status=?`
		args = append(args, status)
	}
	q += ` ORDER BY v.version DESC`
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeVersion
	for rows.Next() {
		version, scanErr := scanKnowledgeVersion(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, version)
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) CreateSource(ctx context.Context, source *domain.KnowledgeSource) error {
	return r.store.InTx(ctx, func(ctx context.Context) error { return r.createSource(ctx, source) })
}

func (r *KnowledgeRepo) createSource(ctx context.Context, source *domain.KnowledgeSource) error {
	if source == nil {
		return fmt.Errorf("%w: knowledge source required", domain.ErrValidation)
	}
	if source.ID == "" {
		source.ID = domain.NewID(domain.PrefixKnowledgeSource)
	}
	if source.CreatedAt.IsZero() {
		source.CreatedAt = timeNow()
	}
	if source.Metadata == nil {
		source.Metadata = map[string]any{}
	}
	if err := source.Validate(); err != nil {
		return err
	}
	if sourceWorkspace, err := r.agentWorkspace(ctx, source.SubmittedByAgentID); err != nil {
		return err
	} else if sourceWorkspace != source.WorkspaceID {
		return fmt.Errorf("%w: knowledge source submitter is outside workspace", domain.ErrValidation)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_sources(`+knowledgeSourceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		source.ID, source.WorkspaceID, source.SubmittedByAgentID, source.Kind, source.Ref,
		source.Locator, source.Excerpt, source.Digest, jsonText(source.Metadata), timeParam(source.CreatedAt))
	return r.store.mapErr(err)
}

func (r *KnowledgeRepo) GetSource(ctx context.Context, workspaceID, requesterAgentID, sourceID string) (*domain.KnowledgeSource, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	source, err := scanKnowledgeSource(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeSourceColsQualified+` FROM knowledge_sources s
		 WHERE s.id=? AND s.workspace_id=? AND (EXISTS (
			SELECT 1 FROM knowledge_version_sources vs
			JOIN knowledge_versions v ON v.id=vs.version_id
			JOIN knowledge_items i ON i.id=v.item_id
			WHERE vs.source_id=s.id AND (i.visibility=? OR i.owner_agent_id=?))
		 OR EXISTS (
			SELECT 1 FROM knowledge_relation_sources rs
			JOIN knowledge_relations rel ON rel.id=rs.relation_id
			JOIN knowledge_items fi ON fi.id=rel.from_item_id
			JOIN knowledge_items ti ON ti.id=rel.to_item_id
			JOIN knowledge_versions sv ON sv.id=rel.source_version_id
			WHERE rs.source_id=s.id AND rel.workspace_id=? AND fi.workspace_id=? AND ti.workspace_id=?
			  AND sv.item_id=fi.id AND sv.status=? AND fi.current_version_id=sv.id
			  AND (fi.visibility=? OR fi.owner_agent_id=?)
			  AND ti.status=? AND ti.current_version_id IS NOT NULL
			  AND (ti.visibility=? OR ti.owner_agent_id=?)))`,
		sourceID, workspaceID, domain.KnowledgeVisibilityWorkspace, requesterAgentID,
		workspaceID, workspaceID, workspaceID, domain.KnowledgeStatusEffective,
		domain.KnowledgeVisibilityWorkspace, requesterAgentID,
		domain.KnowledgeStatusEffective, domain.KnowledgeVisibilityWorkspace, requesterAgentID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return source, nil
}

func (r *KnowledgeRepo) LinkVersionSource(ctx context.Context, link *domain.KnowledgeVersionSource) error {
	return r.store.InTx(ctx, func(ctx context.Context) error { return r.linkVersionSource(ctx, link) })
}

func (r *KnowledgeRepo) linkVersionSource(ctx context.Context, link *domain.KnowledgeVersionSource) error {
	if link == nil || link.VersionID == "" || link.SourceID == "" {
		return fmt.Errorf("%w: knowledge version source link is incomplete", domain.ErrValidation)
	}
	var versionWorkspace, sourceWorkspace string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT i.workspace_id FROM knowledge_versions v JOIN knowledge_items i ON i.id=v.item_id WHERE v.id=?`, link.VersionID).Scan(&versionWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM knowledge_sources WHERE id=?`, link.SourceID).Scan(&sourceWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if versionWorkspace != sourceWorkspace {
		return fmt.Errorf("%w: knowledge source is outside version workspace", domain.ErrValidation)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_version_sources(version_id, source_id, role) VALUES (?,?,?)`,
		link.VersionID, link.SourceID, link.Role)
	return r.store.mapErr(err)
}

func (r *KnowledgeRepo) ListVersionSources(ctx context.Context, workspaceID, requesterAgentID, versionID string) ([]*domain.KnowledgeSource, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeSourceColsQualified+` FROM knowledge_sources s
		JOIN knowledge_version_sources vs ON vs.source_id=s.id
		JOIN knowledge_versions v ON v.id=vs.version_id
		JOIN knowledge_items i ON i.id=v.item_id
		WHERE vs.version_id=? AND i.workspace_id=? AND (i.visibility=? OR i.owner_agent_id=?)
		ORDER BY s.id`, versionID, workspaceID, domain.KnowledgeVisibilityWorkspace, requesterAgentID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeSource
	for rows.Next() {
		source, scanErr := scanKnowledgeSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, source)
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) CreateRelation(ctx context.Context, relation *domain.KnowledgeRelation) error {
	return r.store.InTx(ctx, func(ctx context.Context) error {
		if err := r.createRelation(ctx, relation); err != nil {
			return err
		}
		for _, sourceID := range relation.SourceIDs {
			if err := r.linkRelationSource(ctx, relation.ID, sourceID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *KnowledgeRepo) createRelation(ctx context.Context, relation *domain.KnowledgeRelation) error {
	if relation == nil {
		return fmt.Errorf("%w: knowledge relation required", domain.ErrValidation)
	}
	if relation.ID == "" {
		relation.ID = domain.NewID(domain.PrefixKnowledgeRelation)
	}
	if relation.CreatedAt.IsZero() {
		relation.CreatedAt = timeNow()
	}
	if err := relation.Validate(); err != nil {
		return err
	}
	var fromWorkspace, toWorkspace, sourceWorkspace string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM knowledge_items WHERE id=?`, relation.FromItemID).Scan(&fromWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM knowledge_items WHERE id=?`, relation.ToItemID).Scan(&toWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT i.workspace_id FROM knowledge_versions v JOIN knowledge_items i ON i.id=v.item_id WHERE v.id=?`, relation.SourceVersionID).Scan(&sourceWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if relation.WorkspaceID != fromWorkspace || fromWorkspace != toWorkspace || fromWorkspace != sourceWorkspace {
		return fmt.Errorf("%w: knowledge relation endpoints must share a workspace", domain.ErrValidation)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_relations(`+knowledgeRelationCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		relation.ID, relation.WorkspaceID, relation.SourceVersionID, relation.FromItemID,
		relation.ToItemID, relation.Kind, relation.Condition, relation.Rationale,
		timeParam(relation.CreatedAt))
	return r.store.mapErr(err)
}

func (r *KnowledgeRepo) linkRelationSource(ctx context.Context, relationID, sourceID string) error {
	if relationID == "" || sourceID == "" {
		return fmt.Errorf("%w: relation source link is incomplete", domain.ErrValidation)
	}
	var relationWorkspace, sourceWorkspace string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM knowledge_relations WHERE id=?`, relationID).Scan(&relationWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM knowledge_sources WHERE id=?`, sourceID).Scan(&sourceWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if relationWorkspace != sourceWorkspace {
		return fmt.Errorf("%w: relation source is outside relation workspace", domain.ErrValidation)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_relation_sources(relation_id, source_id) VALUES (?,?)`, relationID, sourceID)
	return r.store.mapErr(err)
}

func (r *KnowledgeRepo) loadRelationSources(ctx context.Context, relation *domain.KnowledgeRelation) error {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT source_id FROM knowledge_relation_sources WHERE relation_id=? ORDER BY source_id`, relation.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		relation.SourceIDs = append(relation.SourceIDs, id)
	}
	return rows.Err()
}

func (r *KnowledgeRepo) ListRelations(ctx context.Context, workspaceID, requesterAgentID, itemID string,
	direction domain.KnowledgeRelationDirection, limit int) ([]*domain.KnowledgeRelation, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	if direction == "" {
		direction = domain.KnowledgeRelationBoth
	}
	if !direction.Valid() {
		return nil, fmt.Errorf("%w: invalid knowledge relation direction %q", domain.ErrValidation, direction)
	}
	q := `SELECT ` + knowledgeRelationColsQualified + ` FROM knowledge_relations r
		JOIN knowledge_items fi ON fi.id=r.from_item_id
		JOIN knowledge_items ti ON ti.id=r.to_item_id
		JOIN knowledge_versions sv ON sv.id=r.source_version_id
		WHERE r.workspace_id=?
		  AND fi.workspace_id=? AND ti.workspace_id=?
		  AND sv.item_id=fi.id AND sv.status=? AND fi.current_version_id=sv.id
		  AND (fi.visibility=? OR fi.owner_agent_id=?)
		  AND ti.status=? AND ti.current_version_id IS NOT NULL
		  AND (ti.visibility=? OR ti.owner_agent_id=?)`
	args := []any{workspaceID, workspaceID, workspaceID, domain.KnowledgeStatusEffective,
		domain.KnowledgeVisibilityWorkspace, requesterAgentID,
		domain.KnowledgeStatusEffective, domain.KnowledgeVisibilityWorkspace, requesterAgentID}
	switch direction {
	case domain.KnowledgeRelationOut:
		q += ` AND r.from_item_id=?`
		args = append(args, itemID)
	case domain.KnowledgeRelationIn:
		q += ` AND r.to_item_id=?`
		args = append(args, itemID)
	default:
		q += ` AND (r.from_item_id=? OR r.to_item_id=?)`
		args = append(args, itemID, itemID)
	}
	q += ` ORDER BY r.from_item_id, r.to_item_id, r.relation_kind, r.id LIMIT ?`
	args = append(args, knowledgeLimit(limit))
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	var out []*domain.KnowledgeRelation
	for rows.Next() {
		relation, scanErr := scanKnowledgeRelation(rows)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		out = append(out, relation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// The SQLite connection is single-writer/single-reader in tests and in the
	// control plane. Load relation evidence only after closing the result set;
	// issuing a nested query while rows is open blocks forever at MaxOpenConns(1).
	for _, relation := range out {
		if err := r.loadRelationSources(ctx, relation); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *KnowledgeRepo) SubmitCandidate(ctx context.Context, submission *domain.KnowledgeSubmission) (bool, *domain.KnowledgeSubmission, error) {
	returnValue := false
	var result *domain.KnowledgeSubmission
	err := r.store.InTx(ctx, func(ctx context.Context) error {
		if submission == nil {
			return fmt.Errorf("%w: knowledge submission required", domain.ErrValidation)
		}
		if submission.ID == "" {
			submission.ID = domain.NewID(domain.PrefixKnowledgeSubmission)
		}
		if submission.WorkspaceID == "" {
			submission.WorkspaceID = submission.Request.WorkspaceID
		}
		if submission.AgentID == "" {
			submission.AgentID = submission.Request.AgentID
		}
		if submission.ClientKey == "" {
			submission.ClientKey = submission.Request.ClientKey
		}
		if submission.Request.RunID == "" {
			submission.Request.RunID = submission.RunID
		}
		if submission.Request.WorkItemID == "" {
			submission.Request.WorkItemID = submission.WorkItemID
		}
		if submission.Request.WorkspaceID == "" {
			submission.Request.WorkspaceID = submission.WorkspaceID
		}
		if submission.Request.AgentID == "" {
			submission.Request.AgentID = submission.AgentID
		}
		if submission.Request.ClientKey == "" {
			submission.Request.ClientKey = submission.ClientKey
		}
		if submission.Status == "" {
			submission.Status = domain.KnowledgeSubmissionReceived
		}
		if submission.Version == 0 {
			submission.Version = 1
		}
		if submission.CreatedAt.IsZero() {
			submission.CreatedAt = timeNow()
		}
		if submission.UpdatedAt.IsZero() {
			submission.UpdatedAt = submission.CreatedAt
		}
		if agentWorkspace, agentErr := r.agentWorkspace(ctx, submission.AgentID); agentErr != nil {
			return agentErr
		} else if agentWorkspace != submission.WorkspaceID {
			return fmt.Errorf("%w: knowledge submission agent is outside workspace", domain.ErrValidation)
		}
		if err := submission.SealRequest(); err != nil {
			return err
		}
		requestJSON, err := json.Marshal(submission.Request)
		if err != nil {
			return fmt.Errorf("%w: marshal candidate request: %v", domain.ErrValidation, err)
		}
		_, err = r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO knowledge_submissions(`+knowledgeSubmissionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			submission.ID, submission.WorkspaceID, submission.AgentID,
			nullString(submission.RunID), nullString(submission.WorkItemID), submission.ClientKey,
			string(requestJSON), submission.RequestDigest, submission.Status,
			jsonText(submission.ResultItemIDs), jsonText(submission.ResultVersionIDs),
			submission.ErrorMessage, submission.Version, timeParam(submission.CreatedAt),
			timeParam(submission.UpdatedAt))
		if err == nil {
			returnValue = true
			result = submission
			return nil
		}
		if !sqliteUniqueViolation(err) {
			return r.store.mapErr(err)
		}
		// A duplicate client key is idempotent only when its full request digest
		// matches.  A duplicate generated ID without a key match remains a
		// normal uniqueness conflict.
		existing, lookupErr := r.getSubmissionByClientKey(ctx, submission.WorkspaceID, submission.AgentID, submission.ClientKey)
		if lookupErr != nil {
			return r.store.mapErr(lookupErr)
		}
		if existing.RequestDigest != submission.RequestDigest {
			return domain.ErrIdempotencyConflict
		}
		returnValue = false
		result = existing
		return nil
	})
	return returnValue, result, err
}

func (r *KnowledgeRepo) GetSubmission(ctx context.Context, workspaceID, requesterAgentID, submissionID string) (*domain.KnowledgeSubmission, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	return r.getSubmission(ctx,
		`id=? AND workspace_id=? AND agent_id=?`, submissionID, workspaceID, requesterAgentID)
}

// GetSubmissionForWorkspace is a management-only lookup.  The application
// service must authenticate the configured librarian before using this port;
// keeping it separate from GetSubmission prevents accidental owner bypasses
// in ordinary Agent reads.
func (r *KnowledgeRepo) GetSubmissionForWorkspace(ctx context.Context, workspaceID, submissionID string) (*domain.KnowledgeSubmission, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(submissionID) == "" {
		return nil, fmt.Errorf("%w: workspace_id and submission_id required", domain.ErrValidation)
	}
	return r.getSubmission(ctx, `id=? AND workspace_id=?`, submissionID, workspaceID)
}

func (r *KnowledgeRepo) getSubmission(ctx context.Context, predicate string, args ...any) (*domain.KnowledgeSubmission, error) {
	submission, err := scanKnowledgeSubmission(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeSubmissionCols+` FROM knowledge_submissions WHERE `+predicate, args...))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return submission, nil
}

func (r *KnowledgeRepo) getSubmissionByClientKey(ctx context.Context, workspaceID, agentID, clientKey string) (*domain.KnowledgeSubmission, error) {
	return r.getSubmission(ctx, `workspace_id=? AND agent_id=? AND client_key=?`, workspaceID, agentID, clientKey)
}

func (r *KnowledgeRepo) GetSubmissionByClientKey(ctx context.Context, workspaceID, agentID, clientKey string) (*domain.KnowledgeSubmission, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, agentID); err != nil {
		return nil, err
	}
	return r.getSubmissionByClientKey(ctx, workspaceID, agentID, clientKey)
}

func (r *KnowledgeRepo) ListSubmissions(ctx context.Context, workspaceID, requesterAgentID string,
	status domain.KnowledgeSubmissionStatus, limit int) ([]*domain.KnowledgeSubmission, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	args := []any{workspaceID, requesterAgentID}
	q := `SELECT ` + knowledgeSubmissionCols + ` FROM knowledge_submissions
		WHERE workspace_id=? AND agent_id=?`
	if status != "" {
		if !status.Valid() {
			return nil, fmt.Errorf("%w: invalid knowledge submission status %q", domain.ErrValidation, status)
		}
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, knowledgeLimit(limit))
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeSubmission
	for rows.Next() {
		submission, scanErr := scanKnowledgeSubmission(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, submission)
	}
	return out, rows.Err()
}

// ListSubmissionsForWorkspace is the configured librarian's candidate inbox.
// It intentionally omits the owner predicate; callers must establish the
// management identity before invoking it.
func (r *KnowledgeRepo) ListSubmissionsForWorkspace(ctx context.Context, workspaceID string,
	status domain.KnowledgeSubmissionStatus, limit int) ([]*domain.KnowledgeSubmission, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("%w: workspace_id required", domain.ErrValidation)
	}
	if status != "" && !status.Valid() {
		return nil, fmt.Errorf("%w: invalid knowledge submission status %q", domain.ErrValidation, status)
	}
	if limit <= 0 || limit > maxKnowledgeLimit {
		limit = defaultKnowledgeLimit
	}
	q := `SELECT ` + knowledgeSubmissionCols + ` FROM knowledge_submissions WHERE workspace_id=?`
	args := []any{workspaceID}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeSubmission
	for rows.Next() {
		submission, scanErr := scanKnowledgeSubmission(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, submission)
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) HasSubmissionForRun(ctx context.Context, workspaceID, agentID, runID string) (bool, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(runID) == "" {
		return false, fmt.Errorf("%w: knowledge submission run lookup requires workspace, agent, and run", domain.ErrValidation)
	}
	var exists bool
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT EXISTS(SELECT 1 FROM knowledge_submissions WHERE workspace_id=? AND agent_id=? AND run_id=?)`,
		workspaceID, agentID, runID).Scan(&exists); err != nil {
		return false, r.store.mapErr(err)
	}
	return exists, nil
}

func (r *KnowledgeRepo) UpdateSubmissionStatus(ctx context.Context, submissionID string,
	status domain.KnowledgeSubmissionStatus, resultItemIDs, resultVersionIDs []string,
	errorMessage string, expectedVersion int) error {
	if !status.Valid() {
		return fmt.Errorf("%w: invalid knowledge submission status %q", domain.ErrValidation, status)
	}
	if expectedVersion < 1 {
		return fmt.Errorf("%w: knowledge submission expected version must be positive", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE knowledge_submissions SET status=?, result_item_ids=?, result_version_ids=?,
			error_message=?, version=version+1, updated_at=? WHERE id=? AND version=?`,
		status, jsonText(resultItemIDs), jsonText(resultVersionIDs), errorMessage,
		timeParam(timeNow()), submissionID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *KnowledgeRepo) PublishVersion(ctx context.Context, itemID, versionID string,
	expectedCurrentVersion int64, publishedAt time.Time) error {
	if expectedCurrentVersion < 0 {
		return fmt.Errorf("%w: expected current knowledge version must be non-negative", domain.ErrValidation)
	}
	if publishedAt.IsZero() {
		publishedAt = timeNow()
	}
	return r.store.InTx(ctx, func(ctx context.Context) error {
		var workspaceID, kind, title, summary, tags, aliases, scope string
		var currentID *string
		var currentVersion int64
		var currentStatus string
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT workspace_id, current_version_id, current_version, status, kind, title,
			 summary, tags, aliases, scope_json FROM knowledge_items WHERE id=?`, itemID).
			Scan(&workspaceID, &currentID, &currentVersion, &currentStatus, &kind, &title,
				&summary, &tags, &aliases, &scope); err != nil {
			return r.store.mapErr(err)
		}
		version, err := scanKnowledgeVersion(r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT `+knowledgeVersionCols+` FROM knowledge_versions WHERE id=? AND item_id=?`, versionID, itemID))
		if err != nil {
			return r.store.mapErr(err)
		}
		if currentID != nil && *currentID == versionID && currentVersion == version.Version && version.Status == domain.KnowledgeStatusEffective {
			return nil
		}
		if currentVersion != expectedCurrentVersion {
			return domain.ErrVersionConflict
		}
		if version.Status != domain.KnowledgeStatusCandidate && version.Status != domain.KnowledgeStatusDraft {
			return fmt.Errorf("%w: knowledge version %s is not publishable", domain.ErrStateConflict, version.ID)
		}
		if version.BaseVersion != expectedCurrentVersion {
			return domain.ErrVersionConflict
		}
		if currentID != nil && *currentID != "" {
			if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
				`UPDATE knowledge_versions SET status=? WHERE id=? AND status=?`,
				domain.KnowledgeStatusSuperseded, *currentID, domain.KnowledgeStatusEffective); err != nil {
				return err
			}
		}
		if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE knowledge_versions SET status=?, published_at=? WHERE id=? AND item_id=?`,
			domain.KnowledgeStatusEffective, timeParam(publishedAt), versionID, itemID); err != nil {
			return err
		}
		if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE knowledge_items SET kind=?, title=?, summary=?, tags=?, aliases=?, scope_json=?,
			 current_version_id=?, current_version=?, status=?, version=version+1, updated_at=? WHERE id=? AND current_version=?`,
			version.Kind, version.Title, version.Summary, jsonText(version.Tags), jsonText(version.Aliases),
			jsonText(version.Scope), versionID, version.Version, domain.KnowledgeStatusEffective,
			timeParam(publishedAt), itemID, expectedCurrentVersion); err != nil {
			return err
		}
		if err := r.indexKnowledgeVersion(ctx, workspaceID, version, true); err != nil {
			return err
		}
		return r.bumpKnowledgeIndexState(ctx, workspaceID, publishedAt)
	})
}

// RepealItem withdraws the current effective version from the published
// projection while retaining the item pointer, immutable Markdown history,
// sources, and relations for authorized historical reads. The append-only
// repeal row makes an exact retry observable without treating a second reason
// as an idempotent replay.
func (r *KnowledgeRepo) RepealItem(ctx context.Context, itemID string, expectedCurrentVersion int64,
	reason string, at time.Time) error {
	if expectedCurrentVersion < 1 {
		return fmt.Errorf("%w: repeal requires a positive current version", domain.ErrValidation)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 4000 {
		return fmt.Errorf("%w: repeal reason must be 1..4000 bytes", domain.ErrValidation)
	}
	if at.IsZero() {
		at = timeNow()
	}
	return r.store.InTx(ctx, func(ctx context.Context) error {
		var existingReason string
		var existingVersion int64
		replayErr := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT reason, current_version FROM knowledge_repeals WHERE item_id=? AND current_version=?`,
			itemID, expectedCurrentVersion).Scan(&existingReason, &existingVersion)
		if replayErr == nil {
			if existingReason == reason {
				return nil
			}
			return domain.ErrIdempotencyConflict
		}
		if !errors.Is(replayErr, sql.ErrNoRows) {
			return r.store.mapErr(replayErr)
		}

		var workspaceID, currentID, status string
		var currentVersion int64
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT workspace_id, current_version_id, current_version, status FROM knowledge_items WHERE id=?`, itemID).
			Scan(&workspaceID, &currentID, &currentVersion, &status); err != nil {
			return r.store.mapErr(err)
		}
		if currentVersion != expectedCurrentVersion {
			return domain.ErrVersionConflict
		}
		if status != string(domain.KnowledgeStatusEffective) || currentID == "" {
			return fmt.Errorf("%w: knowledge item %s is not currently effective", domain.ErrStateConflict, itemID)
		}
		if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE knowledge_versions SET status=? WHERE id=? AND status=?`,
			domain.KnowledgeStatusRepealed, currentID, domain.KnowledgeStatusEffective); err != nil {
			return err
		}
		res, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE knowledge_items SET status=?, repeal_reason=?, version=version+1, updated_at=?
			 WHERE id=? AND current_version=? AND status=?`,
			domain.KnowledgeStatusRepealed, reason, timeParam(at), itemID,
			expectedCurrentVersion, domain.KnowledgeStatusEffective)
		if err != nil {
			return r.store.mapErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return domain.ErrVersionConflict
		}
		if _, err := r.store.execStmt(ctx, r.store.exec(ctx), `DELETE FROM knowledge_index WHERE item_id=?`, itemID); err != nil {
			return err
		}
		repeal := &domain.KnowledgeRepeal{ID: domain.NewID(domain.PrefixKnowledgeRepeal),
			WorkspaceID: workspaceID, ItemID: itemID, VersionID: currentID,
			CurrentVersion: currentVersion, Reason: reason, CreatedAt: at}
		if err := repeal.Validate(); err != nil {
			return err
		}
		if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO knowledge_repeals(id, workspace_id, item_id, version_id, current_version, reason, created_at)
			 VALUES (?,?,?,?,?,?,?)`, repeal.ID, repeal.WorkspaceID, repeal.ItemID,
			repeal.VersionID, repeal.CurrentVersion, repeal.Reason, timeParam(repeal.CreatedAt)); err != nil {
			return r.store.mapErr(err)
		}
		return r.bumpKnowledgeIndexState(ctx, workspaceID, at)
	})
}

type knowledgeSearchRow struct {
	ItemID    string
	VersionID string
	Score     float64
	Snippet   string
}

func (r *KnowledgeRepo) Search(ctx context.Context, query *domain.KnowledgeQuery) ([]*domain.KnowledgeHit, error) {
	if err := query.ValidateReadScope(); err != nil {
		return nil, err
	}
	if err := r.requireKnowledgeReadScopeForRepo(ctx, query.WorkspaceID, query.RequesterAgentID); err != nil {
		return nil, err
	}
	budget := query.Budget.Normalize()
	terms := domain.NormalizeKnowledgeTerms(query.Terms)
	if len(terms) == 0 {
		terms = domain.NormalizeKnowledgeTerms(strings.Fields(query.Question + " " + query.Context))
	}
	rows := make([]knowledgeSearchRow, 0, budget.MaxResults)
	seen := map[string]struct{}{}
	exactIDs := append([]string(nil), query.ItemIDs...)
	if query.Status == "" || query.Status == domain.KnowledgeStatusEffective {
		for _, term := range terms {
			if strings.HasPrefix(term, domain.PrefixKnowledgeItem) {
				exactIDs = append(exactIDs, term)
			}
		}
	}
	if len(exactIDs) > 0 && (query.Status == "" || query.Status == domain.KnowledgeStatusEffective) {
		exact, err := r.searchExact(ctx, query, exactIDs, budget.MaxResults)
		if err != nil {
			return nil, err
		}
		for _, hit := range exact {
			if _, ok := seen[hit.ItemID]; !ok {
				seen[hit.ItemID] = struct{}{}
				rows = append(rows, hit)
			}
		}
	}
	var err error
	if query.Status == "" || query.Status == domain.KnowledgeStatusEffective {
		if len(terms) > 0 && len(rows) < budget.MaxResults {
			var hits []knowledgeSearchRow
			hits, err = r.searchKnowledgeFTS(ctx, query, terms, budget.MaxResults-len(rows))
			if err != nil {
				return nil, err
			}
			for _, hit := range hits {
				if _, ok := seen[hit.ItemID]; ok {
					continue
				}
				seen[hit.ItemID] = struct{}{}
				rows = append(rows, hit)
			}
		}
		if len(terms) > 0 && len(rows) < budget.MaxResults && containsKnowledgeHan(terms) {
			var hits []knowledgeSearchRow
			hits, err = r.searchKnowledgeSubstring(ctx, query, terms, budget.MaxResults-len(rows))
			if err != nil {
				return nil, err
			}
			for _, hit := range hits {
				if _, ok := seen[hit.ItemID]; ok {
					continue
				}
				seen[hit.ItemID] = struct{}{}
				rows = append(rows, hit)
			}
		}
	} else if len(terms) > 0 {
		if !query.Status.Valid() {
			return nil, fmt.Errorf("%w: invalid knowledge status %q", domain.ErrValidation, query.Status)
		}
		hits, searchErr := r.searchKnowledgeVersions(ctx, query, terms, budget.MaxResults-len(rows))
		if searchErr != nil {
			return nil, searchErr
		}
		for _, hit := range hits {
			if _, ok := seen[hit.ItemID]; ok {
				continue
			}
			seen[hit.ItemID] = struct{}{}
			rows = append(rows, hit)
		}
	}
	if len(rows) > budget.MaxResults {
		rows = rows[:budget.MaxResults]
	}
	out := make([]*domain.KnowledgeHit, 0, len(rows))
	for _, row := range rows {
		item, itemErr := r.GetItem(ctx, query.WorkspaceID, query.RequesterAgentID, row.ItemID)
		if itemErr != nil {
			if itemErr == domain.ErrNotFound {
				continue
			}
			return nil, itemErr
		}
		version, versionErr := r.GetVersion(ctx, query.WorkspaceID, query.RequesterAgentID, row.VersionID)
		if versionErr != nil {
			if versionErr == domain.ErrNotFound {
				continue
			}
			return nil, versionErr
		}
		result := &domain.KnowledgeHit{Item: *item, Version: *version, Score: row.Score, Snippet: row.Snippet}
		if query.Direction != "" || budget.MaxRelations > 0 {
			relations, relationErr := r.ListRelations(ctx, query.WorkspaceID, query.RequesterAgentID,
				row.ItemID, query.Direction, budget.MaxRelations)
			if relationErr != nil {
				return nil, relationErr
			}
			result.Relations = make([]domain.KnowledgeRelation, 0, len(relations))
			for _, relation := range relations {
				if relation != nil {
					result.Relations = append(result.Relations, *relation)
				}
			}
		}
		sources, sourceErr := r.ListVersionSources(ctx, query.WorkspaceID, query.RequesterAgentID, row.VersionID)
		if sourceErr != nil {
			return nil, sourceErr
		}
		result.Sources = make([]domain.KnowledgeSource, 0, len(sources))
		for _, source := range sources {
			if source != nil {
				result.Sources = append(result.Sources, *source)
			}
		}
		out = append(out, result)
	}
	return out, nil
}

func (r *KnowledgeRepo) searchExact(ctx context.Context, query *domain.KnowledgeQuery, itemIDs []string, limit int) ([]knowledgeSearchRow, error) {
	ids := make([]string, 0, len(itemIDs))
	seen := map[string]struct{}{}
	for _, id := range itemIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+4)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	args = append(args, query.WorkspaceID, domain.KnowledgeVisibilityWorkspace, query.RequesterAgentID)
	q := `SELECT i.id, i.current_version_id FROM knowledge_items i
		WHERE LOWER(i.id) IN (LOWER(` + strings.Join(placeholders, `),LOWER(`) + `))
		AND i.workspace_id=? AND (i.visibility=? OR i.owner_agent_id=?)
		AND i.status=? AND i.current_version_id IS NOT NULL`
	args = append(args, domain.KnowledgeStatusEffective)
	q, args = appendKnowledgeScopeFilters(q, args, "i.scope_json", query.Scope)
	q += ` ORDER BY i.id LIMIT ?`
	args = append(args, knowledgeLimit(limit))
	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []knowledgeSearchRow
	for rows.Next() {
		var itemID, versionID string
		if err := rows.Scan(&itemID, &versionID); err != nil {
			return nil, err
		}
		out = append(out, knowledgeSearchRow{ItemID: itemID, VersionID: versionID, Score: 100})
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) searchKnowledgeFTS(ctx context.Context, query *domain.KnowledgeQuery, terms []string, limit int) ([]knowledgeSearchRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	args := []any{strings.Join(quoteKnowledgeTokens(terms), " OR "), query.WorkspaceID,
		domain.KnowledgeVisibilityWorkspace, query.RequesterAgentID}
	sqlText := `SELECT ki.item_id, ki.version_id, rank,
		snippet(knowledge_index, 8, '[', ']', '…', 16)
		FROM knowledge_index ki
		WHERE knowledge_index MATCH ? AND ki.workspace_id=?
		  AND (ki.visibility=? OR ki.owner_agent_id=?)`
	sqlText, args = appendKnowledgeScopeFilters(sqlText, args, "ki.scope", query.Scope)
	sqlText += ` ORDER BY rank, ki.item_id LIMIT ?`
	args = append(args, knowledgeLimit(limit))
	rows, err := r.store.query(ctx, r.store.exec(ctx), sqlText, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []knowledgeSearchRow
	for rows.Next() {
		var hit knowledgeSearchRow
		if err := rows.Scan(&hit.ItemID, &hit.VersionID, &hit.Score, &hit.Snippet); err != nil {
			return nil, err
		}
		if hit.Score < 0 {
			hit.Score = 1 / (1 + math.Abs(hit.Score))
		}
		out = append(out, hit)
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) searchKnowledgeSubstring(ctx context.Context, query *domain.KnowledgeQuery,
	terms []string, limit int) ([]knowledgeSearchRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	args := []any{query.WorkspaceID, domain.KnowledgeVisibilityWorkspace, query.RequesterAgentID}
	sqlText := `SELECT ki.item_id, ki.version_id, ki.title, ki.summary, ki.body, ki.aliases, ki.tags
		FROM knowledge_index ki WHERE ki.workspace_id=?
		AND (ki.visibility=? OR ki.owner_agent_id=?)`
	termPredicates := make([]string, 0, len(terms))
	for _, term := range terms {
		termPredicates = append(termPredicates, `LOWER(COALESCE(ki.title,'') || ' ' || COALESCE(ki.summary,'') || ' ' ||
			COALESCE(ki.body,'') || ' ' || COALESCE(ki.aliases,'') || ' ' || COALESCE(ki.tags,'')) LIKE ? ESCAPE '!'`)
		args = append(args, knowledgeLikePattern(term))
	}
	if len(termPredicates) > 0 {
		sqlText += ` AND (` + strings.Join(termPredicates, ` OR `) + `)`
	}
	sqlText, args = appendKnowledgeScopeFilters(sqlText, args, "ki.scope", query.Scope)
	sqlText += ` ORDER BY ki.item_id LIMIT ?`
	args = append(args, knowledgeLimit(limit))
	rows, err := r.store.query(ctx, r.store.exec(ctx), sqlText, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []knowledgeSearchRow
	for rows.Next() {
		var hit knowledgeSearchRow
		var title, summary, body, aliases, tags string
		if err := rows.Scan(&hit.ItemID, &hit.VersionID, &title, &summary, &body, &aliases, &tags); err != nil {
			return nil, err
		}
		hit.Score = 0.5
		hit.Snippet = knowledgeSubstringSnippet(title+" "+summary+" "+body+" "+aliases+" "+tags, terms)
		out = append(out, hit)
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) searchKnowledgeVersions(ctx context.Context, query *domain.KnowledgeQuery,
	terms []string, limit int) ([]knowledgeSearchRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	args := []any{query.WorkspaceID, domain.KnowledgeVisibilityWorkspace, query.RequesterAgentID, query.Status}
	sqlText := `SELECT i.id, v.id, v.title, v.summary, v.body_markdown, v.aliases, v.tags
		FROM knowledge_versions v JOIN knowledge_items i ON i.id=v.item_id
		WHERE i.workspace_id=? AND (i.visibility=? OR i.owner_agent_id=?) AND v.status=?`
	termPredicates := make([]string, 0, len(terms))
	for _, term := range terms {
		termPredicates = append(termPredicates, `LOWER(COALESCE(v.title,'') || ' ' || COALESCE(v.summary,'') || ' ' ||
			COALESCE(v.body_markdown,'') || ' ' || COALESCE(v.aliases,'') || ' ' || COALESCE(v.tags,'')) LIKE ? ESCAPE '!'`)
		args = append(args, knowledgeLikePattern(term))
	}
	if len(termPredicates) > 0 {
		sqlText += ` AND (` + strings.Join(termPredicates, ` OR `) + `)`
	}
	sqlText, args = appendKnowledgeScopeFilters(sqlText, args, "v.scope_json", query.Scope)
	sqlText += ` ORDER BY v.version DESC, v.id LIMIT ?`
	args = append(args, knowledgeLimit(limit))
	rows, err := r.store.query(ctx, r.store.exec(ctx), sqlText, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []knowledgeSearchRow
	for rows.Next() {
		var hit knowledgeSearchRow
		var title, summary, body, aliases, tags string
		if err := rows.Scan(&hit.ItemID, &hit.VersionID, &title, &summary, &body, &aliases, &tags); err != nil {
			return nil, err
		}
		hit.Score = 0.5
		hit.Snippet = knowledgeSubstringSnippet(title+" "+summary+" "+body+" "+aliases+" "+tags, terms)
		out = append(out, hit)
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) CreateQuerySnapshot(ctx context.Context, snapshot *domain.KnowledgeQuerySnapshot) error {
	return r.store.InTx(ctx, func(ctx context.Context) error {
		if snapshot == nil {
			return fmt.Errorf("%w: knowledge query snapshot required", domain.ErrValidation)
		}
		if err := r.requireKnowledgeReadScopeForRepo(ctx, snapshot.WorkspaceID, snapshot.RequesterAgentID); err != nil {
			return err
		}
		if snapshot.ID == "" {
			snapshot.ID = domain.NewID(domain.PrefixKnowledgeQuerySnapshot)
		}
		if snapshot.CreatedAt.IsZero() {
			snapshot.CreatedAt = timeNow()
		}
		snapshot.Budget = snapshot.Budget.Normalize()
		if snapshot.Scope == nil {
			snapshot.Scope = domain.KnowledgeScope{}
		}
		if snapshot.Results == nil {
			snapshot.Results = []domain.KnowledgeHit{}
		}
		if snapshot.Coverage.Entries == nil {
			snapshot.Coverage.Entries = []domain.KnowledgeCoverageEntry{}
		}
		if snapshot.Coverage.Budget.MaxResults == 0 {
			snapshot.Coverage.Budget = snapshot.Budget
		}
		if snapshot.Coverage.Status == "" {
			snapshot.Coverage.Status = domain.KnowledgeCoveragePartial
		}
		if snapshot.ResultJSON == "" {
			raw, err := json.Marshal(snapshot.Results)
			if err != nil {
				return fmt.Errorf("%w: marshal knowledge results: %v", domain.ErrValidation, err)
			}
			snapshot.ResultJSON = string(raw)
		}
		if snapshot.CoverageJSON == "" {
			raw, err := json.Marshal(snapshot.Coverage)
			if err != nil {
				return fmt.Errorf("%w: marshal knowledge coverage: %v", domain.ErrValidation, err)
			}
			snapshot.CoverageJSON = string(raw)
		}
		if err := snapshot.Validate(); err != nil {
			return err
		}
		_, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`INSERT INTO knowledge_query_snapshots(id, workspace_id, requester_agent_id, question,
				context, scope_json, budget_json, index_revision, result_json, coverage_json, created_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)`, snapshot.ID, snapshot.WorkspaceID,
			snapshot.RequesterAgentID, snapshot.Question, snapshot.Context,
			jsonText(snapshot.Scope), jsonText(snapshot.Budget), snapshot.IndexRevision, snapshot.ResultJSON,
			snapshot.CoverageJSON, timeParam(snapshot.CreatedAt))
		return r.store.mapErr(err)
	})
}

func (r *KnowledgeRepo) GetQuerySnapshot(ctx context.Context, workspaceID, requesterAgentID, snapshotID string) (*domain.KnowledgeQuerySnapshot, error) {
	if err := r.requireKnowledgeReadScopeForRepo(ctx, workspaceID, requesterAgentID); err != nil {
		return nil, err
	}
	snapshot := &domain.KnowledgeQuerySnapshot{}
	var budgetJSON, scopeJSON, resultJSON, coverageJSON string
	var created scanTime
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id, workspace_id, requester_agent_id, question, context, budget_json,
			scope_json, index_revision, result_json, coverage_json, created_at FROM knowledge_query_snapshots
			WHERE id=? AND workspace_id=? AND requester_agent_id=?`, snapshotID, workspaceID, requesterAgentID).
		Scan(&snapshot.ID, &snapshot.WorkspaceID, &snapshot.RequesterAgentID, &snapshot.Question,
			&snapshot.Context, &budgetJSON, &scopeJSON, &snapshot.IndexRevision, &resultJSON, &coverageJSON, &created); err != nil {
		return nil, r.store.mapErr(err)
	}
	if err := json.Unmarshal([]byte(budgetJSON), &snapshot.Budget); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &snapshot.Scope); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(resultJSON), &snapshot.Results); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(coverageJSON), &snapshot.Coverage); err != nil {
		return nil, err
	}
	snapshot.ResultJSON, snapshot.CoverageJSON = resultJSON, coverageJSON
	snapshot.CreatedAt = mustTime(created)
	if err := snapshot.Validate(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (r *KnowledgeRepo) GetIndexState(ctx context.Context, workspaceID string) (*domain.KnowledgeIndexState, error) {
	state := &domain.KnowledgeIndexState{}
	var built scanTime
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id, revision, item_count, built_at FROM knowledge_index_state WHERE workspace_id=?`, workspaceID).
		Scan(&state.WorkspaceID, &state.Revision, &state.ItemCount, &built); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &domain.KnowledgeIndexState{WorkspaceID: workspaceID}, nil
		}
		return nil, r.store.mapErr(err)
	}
	state.BuiltAt = mustTime(built)
	return state, nil
}

func (r *KnowledgeRepo) RebuildIndex(ctx context.Context, workspaceID string) error {
	return r.store.InTx(ctx, func(ctx context.Context) error {
		if workspaceID == "" {
			if _, err := r.store.execStmt(ctx, r.store.exec(ctx), `DELETE FROM knowledge_index`); err != nil {
				return err
			}
		} else if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
			`DELETE FROM knowledge_index WHERE workspace_id=?`, workspaceID); err != nil {
			return err
		}
		q := `SELECT i.workspace_id, ` + knowledgeVersionColsQualified + ` FROM knowledge_items i
			JOIN knowledge_versions v ON v.id=i.current_version_id
			WHERE i.status=? AND v.status=? AND i.current_version_id IS NOT NULL`
		args := []any{domain.KnowledgeStatusEffective, domain.KnowledgeStatusEffective}
		if workspaceID != "" {
			q += ` AND i.workspace_id=?`
			args = append(args, workspaceID)
		}
		rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
		if err != nil {
			return err
		}
		type rebuildEntry struct {
			workspaceID string
			version     *domain.KnowledgeVersion
		}
		entries := make([]rebuildEntry, 0)
		counts := map[string]int{}
		for rows.Next() {
			var ws string
			version, scanErr := scanKnowledgeVersionWithWorkspace(rows, &ws)
			if scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			entries = append(entries, rebuildEntry{workspaceID: ws, version: version})
			counts[ws]++
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		// The query cursor must be closed before indexKnowledgeVersion issues
		// writes; SQLite's single connection otherwise self-deadlocks.
		for _, entry := range entries {
			if err := r.indexKnowledgeVersion(ctx, entry.workspaceID, entry.version, false); err != nil {
				return err
			}
		}
		now := timeNow()
		if workspaceID != "" {
			return r.bumpKnowledgeIndexStateWithCount(ctx, workspaceID, now, counts[workspaceID])
		}
		for ws, count := range counts {
			if err := r.bumpKnowledgeIndexStateWithCount(ctx, ws, now, count); err != nil {
				return err
			}
		}
		return nil
	})
}

func scanKnowledgeVersionWithWorkspace(row interface{ Scan(...any) error }, workspaceID *string) (*domain.KnowledgeVersion, error) {
	version := &domain.KnowledgeVersion{}
	var created, published scanTime
	var tags, aliases, scope, metadata string
	var runID, workItemID, supersedes *string
	if err := row.Scan(workspaceID, &version.ID, &version.ItemID, &version.Version, &version.BaseVersion,
		&version.Status, &version.Kind, &version.Title, &version.Summary,
		&version.BodyMarkdown, &tags, &aliases, &scope, &metadata, &version.ContentDigest,
		&version.CreatedByAgentID, &runID, &workItemID, &supersedes, &published, &created); err != nil {
		return nil, err
	}
	if err := jsonInto(tags, &version.Tags); err != nil {
		return nil, err
	}
	if err := jsonInto(aliases, &version.Aliases); err != nil {
		return nil, err
	}
	if err := jsonInto(scope, &version.Scope); err != nil {
		return nil, err
	}
	if err := jsonInto(metadata, &version.Metadata); err != nil {
		return nil, err
	}
	if runID != nil {
		version.CreatedByRunID = *runID
	}
	if workItemID != nil {
		version.CreatedByWorkItemID = *workItemID
	}
	if supersedes != nil {
		version.SupersedesVersionID = *supersedes
	}
	version.PublishedAt, version.CreatedAt = optTime(published), mustTime(created)
	return version, nil
}

func (r *KnowledgeRepo) indexKnowledgeVersion(ctx context.Context, workspaceID string, version *domain.KnowledgeVersion, replace bool) error {
	if replace {
		if _, err := r.store.execStmt(ctx, r.store.exec(ctx), `DELETE FROM knowledge_index WHERE item_id=?`, version.ItemID); err != nil {
			return err
		}
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_index(version_id, item_id, workspace_id, owner_agent_id, visibility,
		 kind, title, summary, body, aliases, tags, scope)
		 SELECT ?, i.id, i.workspace_id, COALESCE(i.owner_agent_id,''), i.visibility,
		 ?, ?, ?, ?, ?, ?, ? FROM knowledge_items i WHERE i.id=?`,
		version.ID, version.Kind, version.Title, version.Summary, version.BodyMarkdown,
		strings.Join(version.Aliases, " "), strings.Join(version.Tags, " "), jsonText(version.Scope), version.ItemID)
	return r.store.mapErr(err)
}

func (r *KnowledgeRepo) bumpKnowledgeIndexState(ctx context.Context, workspaceID string, at time.Time) error {
	var count int
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT COUNT(*) FROM knowledge_items WHERE workspace_id=? AND status=?`, workspaceID, domain.KnowledgeStatusEffective).
		Scan(&count); err != nil {
		return err
	}
	return r.bumpKnowledgeIndexStateWithCount(ctx, workspaceID, at, count)
}

func (r *KnowledgeRepo) bumpKnowledgeIndexStateWithCount(ctx context.Context, workspaceID string, at time.Time, count int) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_index_state(workspace_id, revision, item_count, built_at)
		 VALUES (?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET revision=knowledge_index_state.revision+1,
		 item_count=excluded.item_count, built_at=excluded.built_at`, workspaceID, 1, count, timeParam(at))
	return r.store.mapErr(err)
}

func appendKnowledgeScopeFilters(sqlText string, args []any, column string, scope domain.KnowledgeScope) (string, []any) {
	keys := make([]string, 0, len(scope))
	for key := range scope {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		value, ok := scope[key]
		if !ok {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		var scalar any
		if json.Unmarshal(encoded, &scalar) != nil {
			continue
		}
		path := "$." + strings.ReplaceAll(key, `"`, ``)
		sqlText += ` AND json_extract(` + column + `, ?) = ?`
		args = append(args, path, scalar)
	}
	return sqlText, args
}

func quoteKnowledgeTokens(tokens []string) []string {
	quoted := make([]string, len(tokens))
	for i, token := range tokens {
		quoted[i] = `"` + strings.ReplaceAll(token, `"`, "") + `"`
	}
	return quoted
}

func containsKnowledgeHan(tokens []string) bool {
	for _, token := range tokens {
		for _, r := range token {
			if unicode.Is(unicode.Han, r) {
				return true
			}
		}
	}
	return false
}

func knowledgeSearchTerms(query string) []string {
	terms := domain.NormalizeKnowledgeTerms(strings.Fields(query))
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		var clean strings.Builder
		for _, r := range term {
			if !unicode.IsControl(r) {
				clean.WriteRune(r)
			}
		}
		term = strings.TrimSpace(clean.String())
		if term == "" {
			continue
		}
		for _, r := range term {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				out = append(out, term)
				break
			}
		}
	}
	return out
}

func knowledgeExactItemIDs(terms []string) []string {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, term := range terms {
		if !strings.HasPrefix(term, domain.PrefixKnowledgeItem) {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		ids = append(ids, term)
	}
	return ids
}

func knowledgeLikePattern(token string) string {
	escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(token))
	return "%" + escaped + "%"
}

func knowledgeSubstringSnippet(text string, terms []string) string {
	all := []rune(text)
	for _, term := range terms {
		needle := []rune(term)
		for start := 0; start+len(needle) <= len(all); start++ {
			matched := true
			for i := range needle {
				if !strings.EqualFold(string(all[start+i]), string(needle[i])) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			from, to := max(start-80, 0), min(start+len(needle)+80, len(all))
			prefix, suffix := "", ""
			if from > 0 {
				prefix = "…"
			}
			if to < len(all) {
				suffix = "…"
			}
			return prefix + string(all[from:start]) + "[" + string(all[start:start+len(needle)]) + "]" + string(all[start+len(needle):to]) + suffix
		}
	}
	return ""
}
