package application

import (
	"context"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// KnowledgeRepo is the single persistence port for the knowledge librarian.
// It deliberately combines the authoritative item/version graph with its
// derived search index: publishing through one transaction is the only way a
// version becomes visible to readers.
type KnowledgeRepo interface {
	// Item metadata is mutable only through application commands.  Read methods
	// require both workspace and requester identity so a private item cannot be
	// reached through an unscoped ID lookup.
	CreateItem(ctx context.Context, item *domain.KnowledgeItem) error
	GetItem(ctx context.Context, workspaceID, requesterAgentID, itemID string) (*domain.KnowledgeItem, error)
	ListVisibleItems(ctx context.Context, workspaceID, requesterAgentID string, status domain.KnowledgeStatus, limit int) ([]*domain.KnowledgeItem, error)
	ListVisibleItemsPage(ctx context.Context, workspaceID, requesterAgentID string, options domain.KnowledgeListOptions) (items []*domain.KnowledgeItem, nextCursor string, err error)

	// Versions and their evidence/relations are immutable in content.  The
	// bundle method is the normal write path for a candidate so its exact
	// source and relation set is captured before publication.
	CreateVersion(ctx context.Context, version *domain.KnowledgeVersion) error
	CreateVersionBundle(ctx context.Context, version *domain.KnowledgeVersion, sources []*domain.KnowledgeSource, relations []*domain.KnowledgeRelation) error
	GetVersion(ctx context.Context, workspaceID, requesterAgentID, versionID string) (*domain.KnowledgeVersion, error)
	ListVersions(ctx context.Context, workspaceID, requesterAgentID, itemID string, status domain.KnowledgeStatus) ([]*domain.KnowledgeVersion, error)
	CreateSource(ctx context.Context, source *domain.KnowledgeSource) error
	GetSource(ctx context.Context, workspaceID, requesterAgentID, sourceID string) (*domain.KnowledgeSource, error)
	LinkVersionSource(ctx context.Context, link *domain.KnowledgeVersionSource) error
	ListVersionSources(ctx context.Context, workspaceID, requesterAgentID, versionID string) ([]*domain.KnowledgeSource, error)
	CreateRelation(ctx context.Context, relation *domain.KnowledgeRelation) error
	ListRelations(ctx context.Context, workspaceID, requesterAgentID, itemID string, direction domain.KnowledgeRelationDirection, limit int) ([]*domain.KnowledgeRelation, error)

	// SubmitCandidate persists an append-only post-run capture envelope.  The
	// returned created flag is false for an exact idempotent replay; a different
	// request under the same workspace/agent/client key is a conflict.
	SubmitCandidate(ctx context.Context, submission *domain.KnowledgeSubmission) (created bool, result *domain.KnowledgeSubmission, err error)
	GetSubmission(ctx context.Context, workspaceID, requesterAgentID, submissionID string) (*domain.KnowledgeSubmission, error)
	// Management reads are intentionally separate from requester-scoped reads:
	// the application layer must first prove the configured librarian/manager
	// identity before it can inspect another Agent's candidate inbox.
	GetSubmissionForWorkspace(ctx context.Context, workspaceID, submissionID string) (*domain.KnowledgeSubmission, error)
	GetSubmissionByClientKey(ctx context.Context, workspaceID, agentID, clientKey string) (*domain.KnowledgeSubmission, error)
	ListSubmissions(ctx context.Context, workspaceID, requesterAgentID string, status domain.KnowledgeSubmissionStatus, limit int) ([]*domain.KnowledgeSubmission, error)
	ListSubmissionsForWorkspace(ctx context.Context, workspaceID string, status domain.KnowledgeSubmissionStatus, limit int) ([]*domain.KnowledgeSubmission, error)
	// HasSubmissionForRun is a terminal-run收尾 guard. The complete tuple is
	// required so a run from another Agent or Workspace cannot satisfy capture.
	HasSubmissionForRun(ctx context.Context, workspaceID, agentID, runID string) (bool, error)
	UpdateSubmissionStatus(ctx context.Context, submissionID string, status domain.KnowledgeSubmissionStatus, resultItemIDs, resultVersionIDs []string, errorMessage string, expectedVersion int) error

	// Run captures are the durable post-terminal inbox. Pending rows are
	// returned only when their retry backoff has elapsed; processing completion
	// is idempotent by the immutable Run identity.
	EnqueueRunCapture(ctx context.Context, workspaceID, runID string) error
	ListPendingRunCaptures(ctx context.Context, limit int) ([]*domain.KnowledgeRunCapture, error)
	CompleteRunCapture(ctx context.Context, runID, submissionID string) error
	RecordRunCaptureError(ctx context.Context, runID, message string) error

	// PublishVersion is a compare-and-swap over the item's current version.
	// Application code authenticates the librarian/owner before calling it; the
	// repository only enforces item/version identity and atomic visibility.
	PublishVersion(ctx context.Context, itemID, versionID string, expectedCurrentVersion int64, publishedAt time.Time) error
	RepealItem(ctx context.Context, itemID string, expectedCurrentVersion int64, reason string, at time.Time) error

	// Search reads only the published FTS snapshot and applies the query's
	// workspace/agent visibility predicate.  It must not walk Markdown files or
	// construct a second database.
	Search(ctx context.Context, query *domain.KnowledgeQuery) ([]*domain.KnowledgeHit, error)
	CreateQuerySnapshot(ctx context.Context, snapshot *domain.KnowledgeQuerySnapshot) error
	GetQuerySnapshot(ctx context.Context, workspaceID, requesterAgentID, snapshotID string) (*domain.KnowledgeQuerySnapshot, error)
	GetIndexState(ctx context.Context, workspaceID string) (*domain.KnowledgeIndexState, error)
	RebuildIndex(ctx context.Context, workspaceID string) error
}
