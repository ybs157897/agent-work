package domain

import (
	"fmt"
	"time"
)

// KnowledgeJob is the durable application state for one multi-turn knowledge
// inquiry or curation loop.  It deliberately does not own a second execution
// state machine: CurrentRunID points at the ordinary Run that is currently
// doing one bounded model turn.
const PrefixKnowledgeJob = "kbj_"

// KnowledgeLibrarianConfig selects the ordinary Agent profile that executes
// librarian Runs.  RequestingAgentID remains a separate identity on each job;
// the librarian's own private knowledge must never become the caller's view.
type KnowledgeLibrarianConfig struct {
	WorkspaceID      string    `json:"workspace_id"`
	LibrarianAgentID string    `json:"librarian_agent_id"`
	Enabled          bool      `json:"enabled"`
	AutoCollect      bool      `json:"auto_collect"`
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (c *KnowledgeLibrarianConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: knowledge librarian config required", ErrValidation)
	}
	if c.WorkspaceID == "" {
		return fmt.Errorf("%w: knowledge librarian config workspace_id required", ErrValidation)
	}
	if c.Enabled && c.LibrarianAgentID == "" {
		return fmt.Errorf("%w: enabled knowledge librarian requires librarian_agent_id", ErrValidation)
	}
	if c.Version < 0 {
		return fmt.Errorf("%w: knowledge librarian config version must be non-negative", ErrValidation)
	}
	return nil
}

type KnowledgeJobMode string

const (
	KnowledgeJobInquiry  KnowledgeJobMode = "inquiry"
	KnowledgeJobCuration KnowledgeJobMode = "curation"
)

func (m KnowledgeJobMode) Valid() bool {
	return m == KnowledgeJobInquiry || m == KnowledgeJobCuration
}

type KnowledgeJobStatus string

const (
	KnowledgeJobQueued       KnowledgeJobStatus = "queued"
	KnowledgeJobRunning      KnowledgeJobStatus = "running"
	KnowledgeJobWaitingRetry KnowledgeJobStatus = "waiting_retry"
	KnowledgeJobCompleted    KnowledgeJobStatus = "completed"
	KnowledgeJobIncomplete   KnowledgeJobStatus = "incomplete"
	KnowledgeJobConflict     KnowledgeJobStatus = "conflict"
	KnowledgeJobCancelled    KnowledgeJobStatus = "cancelled"
	KnowledgeJobFailed       KnowledgeJobStatus = "failed"
)

func (s KnowledgeJobStatus) Valid() bool {
	switch s {
	case KnowledgeJobQueued, KnowledgeJobRunning, KnowledgeJobWaitingRetry,
		KnowledgeJobCompleted, KnowledgeJobIncomplete, KnowledgeJobConflict,
		KnowledgeJobCancelled, KnowledgeJobFailed:
		return true
	default:
		return false
	}
}

func (s KnowledgeJobStatus) IsTerminal() bool {
	switch s {
	case KnowledgeJobCompleted, KnowledgeJobIncomplete, KnowledgeJobConflict,
		KnowledgeJobCancelled, KnowledgeJobFailed:
		return true
	default:
		return false
	}
}

var knowledgeJobTransitions = map[KnowledgeJobStatus][]KnowledgeJobStatus{
	KnowledgeJobQueued:       {KnowledgeJobRunning, KnowledgeJobCancelled, KnowledgeJobFailed},
	KnowledgeJobRunning:      {KnowledgeJobQueued, KnowledgeJobWaitingRetry, KnowledgeJobCompleted, KnowledgeJobIncomplete, KnowledgeJobConflict, KnowledgeJobCancelled, KnowledgeJobFailed},
	KnowledgeJobWaitingRetry: {KnowledgeJobQueued, KnowledgeJobRunning, KnowledgeJobCancelled, KnowledgeJobFailed},
}

func (s KnowledgeJobStatus) CanTransitionTo(to KnowledgeJobStatus) bool {
	for _, candidate := range knowledgeJobTransitions[s] {
		if candidate == to {
			return true
		}
	}
	return false
}

type KnowledgeJobActionKind string

const (
	KnowledgeJobActionSearch    KnowledgeJobActionKind = "search"
	KnowledgeJobActionRead      KnowledgeJobActionKind = "read"
	KnowledgeJobActionRelations KnowledgeJobActionKind = "relations"
	KnowledgeJobActionFinish    KnowledgeJobActionKind = "finish"
)

func (k KnowledgeJobActionKind) Valid() bool {
	switch k {
	case KnowledgeJobActionSearch, KnowledgeJobActionRead,
		KnowledgeJobActionRelations, KnowledgeJobActionFinish:
		return true
	default:
		return false
	}
}

type KnowledgeJobActionStatus string

const (
	KnowledgeJobActionPending KnowledgeJobActionStatus = "pending"
	KnowledgeJobActionApplied KnowledgeJobActionStatus = "applied"
	KnowledgeJobActionFailed  KnowledgeJobActionStatus = "failed"
)

func (s KnowledgeJobActionStatus) Valid() bool {
	return s == KnowledgeJobActionPending || s == KnowledgeJobActionApplied || s == KnowledgeJobActionFailed
}

// KnowledgeJobBudget combines the graph/query budget with the number and
// provider-token limits of the model turns that may be created for a job.
// The embedded KnowledgeBudget keeps the public JSON vocabulary compatible
// with the knowledge query API.
type KnowledgeJobBudget struct {
	KnowledgeBudget
	MaxTurns        int   `json:"max_turns"`
	MaxInputTokens  int64 `json:"max_input_tokens"`
	MaxOutputTokens int64 `json:"max_output_tokens"`
	// MaxDurationSeconds bounds the whole Harness lifetime, including time
	// spent waiting for a provider or for recovery after a process restart.
	// A turn/token budget alone cannot prevent a provider that never reaches a
	// terminal state from leaving the job running forever.
	MaxDurationSeconds int `json:"max_duration_seconds"`
}

func (b KnowledgeJobBudget) Normalize() KnowledgeJobBudget {
	b.KnowledgeBudget = b.KnowledgeBudget.Normalize()
	if b.MaxTurns <= 0 || b.MaxTurns > 32 {
		b.MaxTurns = 8
	}
	if b.MaxInputTokens < 0 {
		b.MaxInputTokens = 0
	}
	if b.MaxOutputTokens < 0 {
		b.MaxOutputTokens = 0
	}
	if b.MaxDurationSeconds <= 0 || b.MaxDurationSeconds > 3600 {
		b.MaxDurationSeconds = 300
	}
	return b
}

// KnowledgeJobRequiredRelation is a visible relation discovered while the
// Harness traversed the knowledge graph. Once an endpoint is known and in the
// caller's scope, the final structured answer must either deliver it with
// evidence or remain incomplete.
type KnowledgeJobRequiredRelation struct {
	ID              string                `json:"id"`
	SourceVersionID string                `json:"source_version_id,omitempty"`
	FromItemID      string                `json:"from_item_id"`
	ToItemID        string                `json:"to_item_id"`
	Kind            KnowledgeRelationKind `json:"kind"`
	SourceIDs       []string              `json:"source_ids,omitempty"`
}

type KnowledgeJobUsage struct {
	Turns        int   `json:"turns"`
	Searches     int   `json:"searches"`
	Reads        int   `json:"reads"`
	Relations    int   `json:"relations"`
	Bytes        int64 `json:"bytes"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type KnowledgeJobFrontier struct {
	Subject string `json:"subject"`
	Depth   int    `json:"depth"`
	Kind    string `json:"kind,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type KnowledgeJob struct {
	ID                string                         `json:"id"`
	WorkspaceID       string                         `json:"workspace_id"`
	RequestingAgentID string                         `json:"requesting_agent_id"`
	SourceRunID       string                         `json:"source_run_id,omitempty"`
	SubmissionID      string                         `json:"submission_id,omitempty"`
	AgentProfileID    string                         `json:"agent_profile_id"`
	WorkItemID        string                         `json:"work_item_id"`
	CurrentRunID      string                         `json:"current_run_id,omitempty"`
	Mode              KnowledgeJobMode               `json:"mode"`
	Status            KnowledgeJobStatus             `json:"status"`
	Question          string                         `json:"question"`
	Context           string                         `json:"context,omitempty"`
	Scope             KnowledgeScope                 `json:"scope,omitempty"`
	Budget            KnowledgeJobBudget             `json:"budget"`
	Used              KnowledgeJobUsage              `json:"used"`
	Coverage          KnowledgeCoverage              `json:"coverage"`
	EvidenceIDs       []string                       `json:"evidence_ids,omitempty"`
	VisitedVersionIDs []string                       `json:"visited_version_ids,omitempty"`
	RequiredItemIDs   []string                       `json:"required_item_ids,omitempty"`
	RequiredRelations []KnowledgeJobRequiredRelation `json:"required_relations,omitempty"`
	// IndexRevision is frozen when the job is created. A change means a
	// subsequent turn could otherwise mix old and newly published versions.
	IndexRevision      int64                  `json:"index_revision"`
	SnapshotIDs        []string               `json:"snapshot_ids,omitempty"`
	Frontier           []KnowledgeJobFrontier `json:"frontier,omitempty"`
	Observations       []string               `json:"observations,omitempty"`
	Result             map[string]any         `json:"result,omitempty"`
	TurnSeq            int64                  `json:"turn_seq"`
	RetryCount         int                    `json:"retry_count"`
	RepairAttempt      int                    `json:"repair_attempt"`
	NextActionAt       *time.Time             `json:"next_action_at,omitempty"`
	LastDecisionDigest string                 `json:"last_decision_digest,omitempty"`
	LastError          string                 `json:"last_error,omitempty"`
	ClientKey          string                 `json:"client_key"`
	Version            int                    `json:"version"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
	FinishedAt         *time.Time             `json:"finished_at,omitempty"`
}

// KnowledgeJobAction is the idempotent receipt for one model decision.  The
// unique (job_id, turn_seq) key is the replay fence: a terminal event or
// recovery scan cannot execute the same decision twice.
type KnowledgeJobAction struct {
	ID            string                   `json:"id"`
	JobID         string                   `json:"job_id"`
	TurnSeq       int64                    `json:"turn_seq"`
	RunID         string                   `json:"run_id"`
	Kind          KnowledgeJobActionKind   `json:"kind"`
	Status        KnowledgeJobActionStatus `json:"status"`
	RequestDigest string                   `json:"request_digest"`
	Request       map[string]any           `json:"request,omitempty"`
	Result        map[string]any           `json:"result,omitempty"`
	ErrorMessage  string                   `json:"error_message,omitempty"`
	Version       int                      `json:"version"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
}

func (j *KnowledgeJob) Validate() error {
	if j == nil {
		return fmt.Errorf("%w: knowledge job required", ErrValidation)
	}
	for name, value := range map[string]string{
		"workspace_id": j.WorkspaceID, "requesting_agent_id": j.RequestingAgentID,
		"agent_profile_id": j.AgentProfileID,
		"work_item_id":     j.WorkItemID, "question": j.Question, "client_key": j.ClientKey,
	} {
		if value == "" {
			return fmt.Errorf("%w: knowledge job %s required", ErrValidation, name)
		}
	}
	if !j.Mode.Valid() {
		return fmt.Errorf("%w: unknown knowledge job mode %q", ErrValidation, j.Mode)
	}
	if !j.Status.Valid() {
		return fmt.Errorf("%w: unknown knowledge job status %q", ErrValidation, j.Status)
	}
	if j.Version < 1 {
		return fmt.Errorf("%w: knowledge job version must be >= 1", ErrValidation)
	}
	if j.TurnSeq < 0 || j.RetryCount < 0 || j.RepairAttempt < 0 {
		return fmt.Errorf("%w: knowledge job counters must be non-negative", ErrValidation)
	}
	if j.IndexRevision < 0 {
		return fmt.Errorf("%w: knowledge job index_revision must be non-negative", ErrValidation)
	}
	for _, snapshotID := range j.SnapshotIDs {
		if err := validateTypedID("knowledge_job.snapshot_id", snapshotID, PrefixKnowledgeQuerySnapshot); err != nil {
			return err
		}
	}
	if j.Used.Turns < 0 || j.Used.Searches < 0 || j.Used.Reads < 0 ||
		j.Used.Relations < 0 || j.Used.Bytes < 0 || j.Used.InputTokens < 0 || j.Used.OutputTokens < 0 {
		return fmt.Errorf("%w: knowledge job usage must be non-negative", ErrValidation)
	}
	for _, itemID := range j.RequiredItemIDs {
		if err := validateTypedID("knowledge_job.required_item_id", itemID, PrefixKnowledgeItem); err != nil {
			return err
		}
	}
	for _, relation := range j.RequiredRelations {
		if err := validateTypedID("knowledge_job.required_relation.id", relation.ID, PrefixKnowledgeRelation); err != nil {
			return err
		}
		if relation.SourceVersionID != "" {
			if err := validateTypedID("knowledge_job.required_relation.source_version_id", relation.SourceVersionID, PrefixKnowledgeVersion); err != nil {
				return err
			}
		}
		if err := validateTypedID("knowledge_job.required_relation.from_item_id", relation.FromItemID, PrefixKnowledgeItem); err != nil {
			return err
		}
		if err := validateTypedID("knowledge_job.required_relation.to_item_id", relation.ToItemID, PrefixKnowledgeItem); err != nil {
			return err
		}
		if !relation.Kind.Valid() {
			return fmt.Errorf("%w: knowledge job required relation kind %q", ErrValidation, relation.Kind)
		}
	}
	return nil
}

func (a *KnowledgeJobAction) Validate() error {
	if a == nil {
		return fmt.Errorf("%w: knowledge job action required", ErrValidation)
	}
	if a.JobID == "" || a.RunID == "" || a.RequestDigest == "" {
		return fmt.Errorf("%w: knowledge job action job_id/run_id/request_digest required", ErrValidation)
	}
	if a.TurnSeq < 1 {
		return fmt.Errorf("%w: knowledge job action turn_seq must be positive", ErrValidation)
	}
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: unknown knowledge job action kind %q", ErrValidation, a.Kind)
	}
	if !a.Status.Valid() {
		return fmt.Errorf("%w: unknown knowledge job action status %q", ErrValidation, a.Status)
	}
	if a.Version < 1 {
		return fmt.Errorf("%w: knowledge job action version must be >= 1", ErrValidation)
	}
	return nil
}
