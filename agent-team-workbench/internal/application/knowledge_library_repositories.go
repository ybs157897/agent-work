package application

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// KnowledgeLibraryRepo is the persistence port for the unified knowledge
// library. It owns the FIFO queue invariants (at most one running write task
// per library) and the atomic publish transaction; Markdown interpretation
// stays in the library package.
type KnowledgeLibraryRepo interface {
	GetLibrary(ctx context.Context, workspaceID string) (*domain.KnowledgeLibrary, error)
	GetLibraryByID(ctx context.Context, libraryID string) (*domain.KnowledgeLibrary, error)
	ListLibraries(ctx context.Context) ([]*domain.KnowledgeLibrary, error)
	CreateLibrary(ctx context.Context, lib *domain.KnowledgeLibrary) error
	UpdateLibraryRoot(ctx context.Context, libraryID, rootPath, librarianAgentID string) error

	ListSources(ctx context.Context, libraryID string) ([]*domain.KnowledgeSource, error)
	GetSource(ctx context.Context, libraryID, sourceID string) (*domain.KnowledgeSource, error)
	CreateSource(ctx context.Context, s *domain.KnowledgeSource) error
	UpdateSource(ctx context.Context, s *domain.KnowledgeSource, expectedVersion int) error
	DeleteSource(ctx context.Context, libraryID, sourceID string) error

	CreateSnapshot(ctx context.Context, snap *domain.KnowledgeSnapshot) error
	GetSnapshot(ctx context.Context, snapshotID string) (*domain.KnowledgeSnapshot, error)

	EnsureRepresentation(ctx context.Context, rep *domain.KnowledgeRepresentation) (*domain.KnowledgeRepresentation, error)
	GetRepresentation(ctx context.Context, representationID string) (*domain.KnowledgeRepresentation, error)
	InsertEvidence(ctx context.Context, e *domain.KnowledgeEvidence) error
	GetEvidence(ctx context.Context, libraryID, evidenceID string) (*domain.KnowledgeEvidence, error)
	ListEvidenceByIDs(ctx context.Context, libraryID string, ids []string) ([]*domain.KnowledgeEvidence, error)
	EvidenceExists(ctx context.Context, libraryID string, ids []string) (map[string]bool, error)
	GetBinding(ctx context.Context, bindingID string) (*domain.KnowledgeSnapshotBinding, error)

	ListEntities(ctx context.Context, libraryID string) ([]*domain.KnowledgeEntity, error)
	UpsertEntity(ctx context.Context, e *domain.KnowledgeEntity) error
	ListDocuments(ctx context.Context, libraryID string) ([]*domain.KnowledgeDocument, error)
	GetDocument(ctx context.Context, libraryID, documentID string) (*domain.KnowledgeDocument, error)

	GetRelease(ctx context.Context, libraryID, releaseID string) (*domain.KnowledgeRelease, error)
	CurrentRelease(ctx context.Context, libraryID string) (*domain.KnowledgeRelease, error)
	ListReleases(ctx context.Context, libraryID string, limit int) ([]*domain.KnowledgeRelease, error)

	InsertEvent(ctx context.Context, e *domain.KnowledgeLibraryEvent) (bool, error)
	GetEventByClientKey(ctx context.Context, libraryID, clientKey string) (*domain.KnowledgeLibraryEvent, error)
	ListEvents(ctx context.Context, libraryID, status string, limit int) ([]*domain.KnowledgeLibraryEvent, error)
	UpdateEventStatus(ctx context.Context, eventID string, status domain.KnowledgeEventStatus, taskID string) error

	CreateTaskWithEvent(ctx context.Context, task *domain.KnowledgeWriteTask) error
	HeadTask(ctx context.Context, libraryID string) (*domain.KnowledgeWriteTask, error)
	TaskByWorkItem(ctx context.Context, workItemID string) (*domain.KnowledgeWriteTask, error)
	GetTask(ctx context.Context, libraryID, taskID string) (*domain.KnowledgeWriteTask, error)
	ListTasks(ctx context.Context, libraryID, status string, limit int) ([]*domain.KnowledgeWriteTask, error)
	ClaimTask(ctx context.Context, libraryID, taskID, ownerToken string) (bool, error)
	UpdateTask(ctx context.Context, task *domain.KnowledgeWriteTask) error
	CompleteHeadTask(ctx context.Context, taskID string, status domain.KnowledgeTaskStatus, lastErr, blocked, targetReleaseID string) error
	CountTasks(ctx context.Context, libraryID string) (pending int, blocked int, err error)

	CreateTurn(ctx context.Context, turn *domain.KnowledgeTaskTurn) error
	UpdateTurn(ctx context.Context, turnID, status, resultJSON, errMsg string) error
	ListTurns(ctx context.Context, taskID string) ([]*domain.KnowledgeTaskTurn, error)

	CreatePublication(ctx context.Context, p *domain.KnowledgePublication) error
	GetPublicationByTask(ctx context.Context, taskID string) (*domain.KnowledgePublication, error)
	ListPreparedPublications(ctx context.Context, libraryID string) ([]*domain.KnowledgePublication, error)
	ReleaseByProjectionDigest(ctx context.Context, libraryID, digest string) (*domain.KnowledgeRelease, error)
	CommitPublication(ctx context.Context, taskID string) error
	AbandonPublication(ctx context.Context, taskID string) error

	PublishProjection(ctx context.Context, in PublishInput) (*domain.KnowledgeRelease, error)

	SearchRelease(ctx context.Context, libraryID, releaseID string, terms []string, limit int) ([]domain.KnowledgeQueryHit, bool, int, error)
	ReleaseDocuments(ctx context.Context, releaseID, query, kind string, limit int) ([]*domain.KnowledgeDocument, error)
	ReleaseDocumentVersion(ctx context.Context, releaseID, documentID string) (*domain.KnowledgeDocumentVersion, error)
	AssertionsInRelease(ctx context.Context, releaseID string, assertionIDs []string) ([]domain.KnowledgeAssertion, error)
	DocumentVersionDetail(ctx context.Context, documentID string, version int) (*domain.KnowledgeDocumentVersion, []domain.KnowledgeAssertion, []domain.KnowledgeAssertionRelation, error)
	CurrentDocumentVersion(ctx context.Context, documentID string) (*domain.KnowledgeDocumentVersion, error)
	ListDocumentVersions(ctx context.Context, documentID string) ([]*domain.KnowledgeDocumentVersion, error)
	ReleaseAssertionsByIDs(ctx context.Context, assertionIDs []string) ([]domain.KnowledgeAssertion, error)
	ReleaseGraph(ctx context.Context, libraryID, releaseID string) ([]*domain.KnowledgeEntity, []domain.KnowledgeAssertionRelation, []*domain.KnowledgeDocument, error)
	ListBridges(ctx context.Context, libraryID, releaseID string) ([]*domain.KnowledgeBridgeView, error)
	OwnedContentPaths(ctx context.Context, libraryID string) ([]string, error)
	ReprojectRecordJSON(ctx context.Context, libraryID string, project func(markdown string) (map[string]struct {
		About, Scope, Evidence string
	}, map[string]string, error)) (int, error)
	RebuildSearchIndex(ctx context.Context, libraryID string) (int, error)
}

// PublishDocument is one document version plus its materialized records.
type PublishDocument struct {
	DocumentID      string
	Path            string
	Kind            string
	Title           string
	Summary         string
	Domains         []string
	ContentMarkdown string
	FrontmatterJSON string
	ContentDigest   string
	Assertions      []domain.KnowledgeAssertion
	Relations       []domain.KnowledgeAssertionRelation
}

// PublishEntity is one entity to upsert as part of a release.
type PublishEntity struct {
	ID            string
	Kind          string
	Namespace     string
	CanonicalName string
	Aliases       []string
	SourceID      string
}

// PublishInput is everything one atomic publish needs.
type PublishInput struct {
	LibraryID string
	TaskID    string
	// PublicationID names the publish journal entry. The journal row and the
	// release are written in one transaction, so recovery can tell whether an
	// interrupted publish already happened instead of guessing.
	PublicationID    string
	SnapshotID       string
	ParentReleaseID  string
	ProjectionDigest string
	CoverageJSON     string
	Notes            string
	Documents        []PublishDocument
	Entities         []PublishEntity
	Evidence         []*domain.KnowledgeEvidence
	Removals         []string
	RemovedNotes     map[string]string
	Renames          []PublishRename
	DocumentIDByPath map[string]string
	DocumentIDByID   map[string]string
}

// PublishRename records a logical document move that keeps its stable ID.
type PublishRename struct {
	DocumentID string
	FromPath   string
	ToPath     string
	Note       string
}
