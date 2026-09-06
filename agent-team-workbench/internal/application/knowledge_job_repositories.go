package application

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// KnowledgeJobRepo persists the application-owned control state for the
// multi-turn knowledge Harness.  KnowledgeJob is not a replacement Run: the
// current Run remains the only provider execution and accounting authority.
type KnowledgeJobRepo interface {
	GetConfig(ctx context.Context, workspaceID string) (*domain.KnowledgeLibrarianConfig, error)
	CreateConfig(ctx context.Context, config *domain.KnowledgeLibrarianConfig) error
	UpdateConfig(ctx context.Context, config *domain.KnowledgeLibrarianConfig, expectedVersion int) error
	Create(ctx context.Context, job *domain.KnowledgeJob) error
	Get(ctx context.Context, id string) (*domain.KnowledgeJob, error)
	GetByClientKey(ctx context.Context, workspaceID, requestingAgentID, clientKey string) (*domain.KnowledgeJob, error)
	GetByWorkItem(ctx context.Context, workItemID string) (*domain.KnowledgeJob, error)
	GetByCurrentRun(ctx context.Context, runID string) (*domain.KnowledgeJob, error)
	List(ctx context.Context, workspaceID, agentProfileID string, status domain.KnowledgeJobStatus, limit int) ([]*domain.KnowledgeJob, error)
	// ListRecoverable filters runnable statuses in SQL before paging. A broad
	// newest-jobs query can starve an old active job behind terminal history.
	ListRecoverable(ctx context.Context, workspaceID, afterID string, limit int) ([]*domain.KnowledgeJob, error)
	// ListCancellationPending returns terminal jobs whose current Run still
	// needs a forwarded cancel after a transient control-plane failure.
	ListCancellationPending(ctx context.Context, workspaceID string, limit int) ([]*domain.KnowledgeJob, error)
	// ListForRequester applies the requester predicate before LIMIT.  The
	// ordinary List method's agentProfileID is the librarian executor and is
	// therefore unsuitable for caller-scoped job history.
	ListForRequester(ctx context.Context, workspaceID, requestingAgentID string, status domain.KnowledgeJobStatus, limit int) ([]*domain.KnowledgeJob, error)
	// Update is the only mutable job write.  Callers must pass the version read
	// in the same transaction; a lost CAS is a replay/race signal.
	Update(ctx context.Context, job *domain.KnowledgeJob, expectedVersion int) error
	// BindCurrentRun atomically records the Run that owns the next model turn.
	// The job and Run are created in one transaction by the application layer;
	// this CAS prevents a late terminal callback from taking ownership back.
	BindCurrentRun(ctx context.Context, jobID, runID string, turnSeq int64, expectedVersion int) error

	// ClaimAction creates the one action receipt for (job, turn_seq).  An exact
	// replay returns created=false and the existing receipt; a different digest
	// for the same turn is an idempotency conflict.
	ClaimAction(ctx context.Context, action *domain.KnowledgeJobAction) (created bool, existing *domain.KnowledgeJobAction, err error)
	GetAction(ctx context.Context, jobID string, turnSeq int64) (*domain.KnowledgeJobAction, error)
	UpdateAction(ctx context.Context, action *domain.KnowledgeJobAction, expectedVersion int) error
}
