package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// CaptureDeliveryBriefSnapshotParams identifies the governance line and the
// Task whose deterministic Delivery Brief is to be sealed. A blank WorkItemID
// means the Goal root. A non-empty ClientKey makes the capture idempotent on
// (Goal, Todo, ClientKey); a new key is required for a later capture after the
// source state has advanced.
type CaptureDeliveryBriefSnapshotParams struct {
	GoalID     string
	TodoID     string
	WorkItemID string
	ClientKey  string
}

// CaptureDeliveryBriefSnapshot stores an immutable, digest-sealed snapshot of
// the existing DeliveryBrief read model. It deliberately captures partial
// briefs for audit, but their freshness state is checked by the evidence
// validator and can never satisfy passed evidence.
func (s *Service) CaptureDeliveryBriefSnapshot(ctx context.Context,
	p CaptureDeliveryBriefSnapshotParams) (*domain.DeliveryBriefSnapshot, error) {
	var captured *domain.DeliveryBriefSnapshot
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, p.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, p.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID {
			return fmt.Errorf("%w: delivery brief snapshot Todo is outside Goal", domain.ErrValidation)
		}
		workItemID := p.WorkItemID
		if workItemID == "" {
			workItemID = goal.RootWorkItemID
		}
		workItem, err := s.store.WorkItems().Get(txctx, workItemID)
		if err != nil {
			return err
		}
		if workItem.WorkspaceID != goal.WorkspaceID || !isTaskWorkItem(workItem) ||
			!s.workItemInGoalTree(txctx, goal, todo, workItem.ID) {
			return fmt.Errorf("%w: delivery brief snapshot WorkItem is outside Goal tree", domain.ErrValidation)
		}

		clientKey := p.ClientKey
		if clientKey != "" {
			if existing, getErr := s.store.DeliveryBriefSnapshots().GetByClientKey(txctx, goal.ID, todo.ID, clientKey); getErr == nil {
				// Replay is resolved before reading mutable sources. This is what
				// makes a client retry safe after the original source has moved on
				// or an optional read dependency is temporarily unavailable.
				if existing.WorkItemID != workItem.ID {
					return domain.ErrIdempotencyConflict
				}
				captured = existing
				return nil
			} else if !errors.Is(getErr, domain.ErrNotFound) {
				return getErr
			}
		}

		brief, err := s.DeliveryBrief(txctx, workItem.ID, BriefOptions{IncludeRestrictedArtifacts: true})
		if err != nil {
			return err
		}
		sourceVersions := cloneInt64Map(brief.Freshness.SourceVersions)
		// DeliveryBrief's public freshness covers its read-model sources. The
		// evidence binding also includes the governance aggregates that select
		// which Goal/Todo may consume this capture.
		sourceVersions["goal"] = int64(goal.Version)
		sourceVersions["todo"] = int64(todo.Version)
		brief.Freshness.SourceVersions = sourceVersions
		// Rebuild after adding governance watermarks so the closed payload and
		// the table metadata carry exactly the same source version map.
		payload, err := canonicalDeliveryBriefSnapshotPayload(brief)
		if err != nil {
			return err
		}
		snapshot := &domain.DeliveryBriefSnapshot{
			ID:             domain.NewID(domain.PrefixDeliveryBriefSnapshot),
			SchemaVersion:  domain.DeliveryBriefSnapshotSchemaVersion,
			GoalID:         goal.ID,
			TodoID:         todo.ID,
			WorkItemID:     workItem.ID,
			SnapshotJSON:   payload,
			AsOfEventSeq:   brief.Freshness.AsOfEventSeq,
			SourceVersions: sourceVersions,
			FreshnessState: brief.Freshness.State,
			CreatedAt:      time.Now().UTC(),
			ClientKey:      clientKey,
		}
		if err := snapshot.Seal(); err != nil {
			return err
		}
		if err := s.store.DeliveryBriefSnapshots().Create(txctx, snapshot); err != nil {
			// Two callers can both miss the replay row before either transaction
			// commits. Turn the unique-key loser into the same exact replay as the
			// preflight hit; only a different WorkItem remains a conflict.
			if clientKey != "" && errors.Is(err, domain.ErrIdempotencyConflict) {
				if existing, getErr := s.store.DeliveryBriefSnapshots().GetByClientKey(txctx, goal.ID, todo.ID, clientKey); getErr == nil {
					if existing.WorkItemID != workItem.ID {
						return domain.ErrIdempotencyConflict
					}
					captured = existing
					return nil
				} else if !errors.Is(getErr, domain.ErrNotFound) {
					return getErr
				}
			}
			return err
		}
		if err := s.emit(txctx, goal.WorkspaceID, domain.EventDeliveryBriefSnapshotCreated,
			domain.AggregateDeliveryBriefSnapshot, snapshot.ID, 1, nil, map[string]any{
				"schema_version": snapshot.SchemaVersion, "goal_id": goal.ID, "todo_id": todo.ID, "work_item_id": workItem.ID,
				"canonical_digest": snapshot.CanonicalDigest, "as_of_event_seq": snapshot.AsOfEventSeq,
				"source_versions": cloneInt64Map(snapshot.SourceVersions),
				"freshness_state": snapshot.FreshnessState,
			}); err != nil {
			return err
		}
		captured = snapshot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return captured, nil
}

// GetDeliveryBriefSnapshot returns one sealed capture after re-checking its
// Goal/Todo/WorkItem tree binding. It intentionally does not require current
// freshness: stale and partial captures remain useful audit material, while
// ValidateEvidenceReference applies the stricter finish-gate rule.
func (s *Service) GetDeliveryBriefSnapshot(ctx context.Context, snapshotID string) (*domain.DeliveryBriefSnapshot, error) {
	var snapshot *domain.DeliveryBriefSnapshot
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		var err error
		snapshot, err = s.store.DeliveryBriefSnapshots().Get(txctx, snapshotID)
		if err != nil {
			return err
		}
		return s.validateDeliveryBriefSnapshotScope(txctx, snapshot)
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Service) validateDeliveryBriefSnapshotScope(ctx context.Context,
	snapshot *domain.DeliveryBriefSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("%w: Delivery Brief snapshot required", domain.ErrValidation)
	}
	goal, err := s.store.Goals().Get(ctx, snapshot.GoalID)
	if err != nil {
		return err
	}
	todo, err := s.store.Todos().Get(ctx, snapshot.TodoID)
	if err != nil {
		return err
	}
	if todo.GoalID != goal.ID {
		return fmt.Errorf("%w: Delivery Brief snapshot Todo is outside Goal", domain.ErrValidation)
	}
	item, err := s.store.WorkItems().Get(ctx, snapshot.WorkItemID)
	if err != nil {
		return err
	}
	if item.WorkspaceID != goal.WorkspaceID || !isTaskWorkItem(item) ||
		!s.workItemInGoalTree(ctx, goal, todo, item.ID) {
		return fmt.Errorf("%w: Delivery Brief snapshot WorkItem is outside Goal tree", domain.ErrValidation)
	}
	return nil
}

// canonicalDeliveryBriefSnapshotPayload is the closed v1 DTO used for
// evidence. It mirrors the public Delivery Brief response, while deliberately
// omitting freshness.generated_at and all provider/model free-form input.
// Every field is explicit so an untrusted map cannot smuggle a second source
// of truth into the sealed record.
type deliveryBriefSnapshotPayload struct {
	WorkItem           deliveryBriefSnapshotWorkItem   `json:"work_item"`
	AcceptanceCriteria []string                        `json:"acceptance_criteria"`
	Conclusion         deliveryBriefSnapshotConclusion `json:"conclusion"`
	Attempts           []deliveryBriefSnapshotAttempt  `json:"attempts"`
	Runs               []deliveryBriefSnapshotRun      `json:"runs"`
	Changes            *deliveryBriefSnapshotChangeSet `json:"changes"`
	Artifacts          []deliveryBriefSnapshotArtifact `json:"artifacts"`
	Blocker            *deliveryBriefSnapshotBlocker   `json:"blocker"`
	Risks              []deliveryBriefSnapshotRisk     `json:"risks"`
	Comments           []deliveryBriefSnapshotComment  `json:"comments"`
	Freshness          deliveryBriefSnapshotFreshness  `json:"freshness"`
	Truncation         deliveryBriefSnapshotTruncation `json:"truncation"`
}

type deliveryBriefSnapshotWorkItem struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	RecordKind     string  `json:"record_kind"`
	ParentID       string  `json:"parent_id"`
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	Status         string  `json:"status"`
	Phase          string  `json:"phase"`
	Priority       string  `json:"priority"`
	DueDate        *string `json:"due_date"`
	AgentProfileID string  `json:"agent_profile_id"`
	LockedByRunID  string  `json:"locked_by_run_id"`
	LockedAt       *string `json:"locked_at"`
	RollingDigest  string  `json:"rolling_digest"`
	RunsCount      int     `json:"runs_count"`
	LatestRunID    string  `json:"latest_run_id"`
	Version        int     `json:"version"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

type deliveryBriefSnapshotConclusion struct {
	CoordinatorStatus string `json:"coordinator_status"`
	Stage             string `json:"stage"`
	Summary           string `json:"summary"`
	NextAction        string `json:"next_action"`
	Version           int    `json:"version"`
}

type deliveryBriefSnapshotFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type deliveryBriefSnapshotAttempt struct {
	Attempt    int                           `json:"attempt"`
	Role       string                        `json:"role"`
	RunID      string                        `json:"run_id"`
	AgentID    string                        `json:"agent_id"`
	AgentName  string                        `json:"agent_name"`
	Status     string                        `json:"status"`
	StartedAt  *string                       `json:"started_at"`
	FinishedAt *string                       `json:"finished_at"`
	RetryOf    string                        `json:"retry_of"`
	Failure    *deliveryBriefSnapshotFailure `json:"failure"`
}

type deliveryBriefSnapshotEvidence struct {
	ID         string `json:"id"`
	SourceKind string `json:"source_kind"`
	SourceID   string `json:"source_id"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Trust      string `json:"trust"`
	OccurredAt string `json:"occurred_at"`
}

type deliveryBriefSnapshotRun struct {
	Run       deliveryBriefSnapshotRunData    `json:"run"`
	Summary   string                          `json:"summary"`
	Evidence  []deliveryBriefSnapshotEvidence `json:"evidence"`
	Truncated bool                            `json:"truncated"`
}

type deliveryBriefSnapshotRunData struct {
	ID             string                        `json:"id"`
	WorkItemID     string                        `json:"work_item_id"`
	AgentProfileID string                        `json:"agent_profile_id"`
	Status         string                        `json:"status"`
	RuntimeLabel   string                        `json:"runtime_label"`
	Progress       *float64                      `json:"progress"`
	RetryOf        string                        `json:"retry_of"`
	Failure        *deliveryBriefSnapshotFailure `json:"failure"`
	UsageIn        int64                         `json:"usage_in"`
	UsageOut       int64                         `json:"usage_out"`
	UsageCached    int64                         `json:"usage_cached"`
	UsageBasis     string                        `json:"usage_basis"`
	Version        int                           `json:"version"`
	CreatedAt      string                        `json:"created_at"`
	UpdatedAt      string                        `json:"updated_at"`
}

type deliveryBriefSnapshotChangeSet struct {
	RunID        string                            `json:"run_id"`
	Files        []deliveryBriefSnapshotFileChange `json:"files"`
	TotalFiles   int                               `json:"total_files"`
	TotalAdded   int                               `json:"total_added"`
	TotalDeleted int                               `json:"total_deleted"`
	Truncated    bool                              `json:"truncated"`
}

type deliveryBriefSnapshotFileChange struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Status  string `json:"status"`
}

type deliveryBriefSnapshotArtifact struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	LogicalPath string `json:"logical_path"`
	Mime        string `json:"mime"`
	Size        int64  `json:"size"`
	Sha256      string `json:"sha256"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
}

type deliveryBriefSnapshotBlocker struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	Source    string  `json:"source"`
	RunID     *string `json:"run_id"`
	CreatedAt string  `json:"created_at"`
}

type deliveryBriefSnapshotRisk struct {
	SourceKind string `json:"source_kind"`
	SourceID   string `json:"source_id"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Severity   string `json:"severity"`
}

type deliveryBriefSnapshotComment struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace_id"`
	RootWorkItemID string `json:"root_work_item_id"`
	WorkItemID     string `json:"work_item_id"`
	Revision       int64  `json:"revision"`
	Kind           string `json:"kind"`
	Body           string `json:"body"`
	ActorKind      string `json:"actor_kind"`
	ActorID        string `json:"actor_id"`
	SourceRunID    string `json:"source_run_id"`
	SourceRef      string `json:"source_ref"`
	ClientKey      string `json:"client_key"`
	CreatedAt      string `json:"created_at"`
}

type deliveryBriefSnapshotFreshness struct {
	SourceVersions map[string]int64 `json:"source_versions"`
	State          string           `json:"state"`
	MissingSources []string         `json:"missing_sources"`
}

type deliveryBriefSnapshotTruncation struct {
	Attempts  bool `json:"attempts"`
	Runs      bool `json:"runs"`
	Files     bool `json:"files"`
	Artifacts bool `json:"artifacts"`
	Comments  bool `json:"comments"`
}

func canonicalDeliveryBriefSnapshotPayload(b *DeliveryBrief) (string, error) {
	if b == nil || b.WorkItem == nil {
		return "", fmt.Errorf("%w: Delivery Brief WorkItem is required for snapshot", domain.ErrValidation)
	}
	recordKind := b.WorkItem.RecordKind
	if recordKind == "" {
		recordKind = domain.RecordKindTask
	}
	wi := deliveryBriefSnapshotWorkItem{
		ID: b.WorkItem.ID, WorkspaceID: b.WorkItem.WorkspaceID, RecordKind: string(recordKind),
		ParentID: b.WorkItem.ParentID, Title: b.WorkItem.Title, Description: b.WorkItem.Description,
		Status: string(b.WorkItem.Status), Phase: string(b.WorkItem.Phase), Priority: string(b.WorkItem.Priority),
		AgentProfileID: b.WorkItem.AgentProfileID, LockedByRunID: b.WorkItem.LockedByRunID,
		RollingDigest: b.WorkItem.RollingDigest, RunsCount: b.RunsTotal, LatestRunID: b.LatestRunID,
		Version: b.WorkItem.Version, CreatedAt: briefSnapshotTime(b.WorkItem.CreatedAt),
		UpdatedAt: briefSnapshotTime(b.WorkItem.UpdatedAt),
	}
	if b.WorkItem.DueDate != nil {
		value := b.WorkItem.DueDate.Format("2006-01-02")
		wi.DueDate = &value
	}
	if b.WorkItem.LockedAt != nil {
		value := briefSnapshotTime(*b.WorkItem.LockedAt)
		wi.LockedAt = &value
	}

	attempts := make([]deliveryBriefSnapshotAttempt, 0, len(b.Attempts))
	for _, a := range b.Attempts {
		item := deliveryBriefSnapshotAttempt{
			Attempt: a.Attempt, Role: a.Role, RunID: a.RunID, AgentID: a.AgentID,
			AgentName: a.AgentName, Status: a.Status, StartedAt: briefSnapshotOptionalTime(a.StartedAt),
			FinishedAt: briefSnapshotOptionalTime(a.FinishedAt), RetryOf: a.RetryOf,
		}
		if a.Failure != nil {
			item.Failure = &deliveryBriefSnapshotFailure{Code: a.Failure.Code, Message: a.Failure.Message, Retryable: a.Failure.Retryable}
		}
		attempts = append(attempts, item)
	}

	runs := make([]deliveryBriefSnapshotRun, 0, len(b.Runs))
	for _, runEvidence := range b.Runs {
		if runEvidence.Run == nil {
			return "", fmt.Errorf("%w: Delivery Brief run evidence has no Run", domain.ErrValidation)
		}
		run := runEvidence.Run
		item := deliveryBriefSnapshotRun{
			Run: deliveryBriefSnapshotRunData{
				ID: run.ID, WorkItemID: run.WorkItemID, AgentProfileID: run.AgentProfileID,
				Status: string(run.Status), RuntimeLabel: run.RuntimeLabel, Progress: run.Progress,
				RetryOf: run.RetryOf, UsageIn: run.UsageIn, UsageOut: run.UsageOut,
				UsageCached: run.UsageCached, UsageBasis: run.UsageBasis, Version: run.Version,
				CreatedAt: briefSnapshotTime(run.CreatedAt), UpdatedAt: briefSnapshotTime(run.UpdatedAt),
			},
			Summary: runEvidence.Summary, Truncated: runEvidence.Truncated,
			Evidence: make([]deliveryBriefSnapshotEvidence, 0, len(runEvidence.Evidence)),
		}
		if run.Failure != nil {
			item.Run.Failure = &deliveryBriefSnapshotFailure{Code: run.Failure.Code, Message: run.Failure.Message, Retryable: run.Failure.Retryable}
		}
		for _, evidence := range runEvidence.Evidence {
			item.Evidence = append(item.Evidence, deliveryBriefSnapshotEvidence{
				ID: evidence.ID, SourceKind: evidence.SourceKind, SourceID: evidence.SourceID,
				Label: evidence.Label, Status: evidence.Status, Trust: evidence.Trust,
				OccurredAt: briefSnapshotTime(evidence.OccurredAt),
			})
		}
		runs = append(runs, item)
	}

	var changes *deliveryBriefSnapshotChangeSet
	if len(b.Changes) > 0 {
		latest := b.Changes[len(b.Changes)-1]
		changes = &deliveryBriefSnapshotChangeSet{
			RunID: latest.RunID, Files: make([]deliveryBriefSnapshotFileChange, 0, len(latest.Files)),
			TotalFiles: latest.TotalFiles, TotalAdded: latest.TotalAdded,
			TotalDeleted: latest.TotalDeleted, Truncated: latest.Truncated,
		}
		for _, file := range latest.Files {
			changes.Files = append(changes.Files, deliveryBriefSnapshotFileChange{
				Path: file.Path, Added: file.Added, Deleted: file.Deleted, Status: file.Status,
			})
		}
	}

	artifacts := make([]deliveryBriefSnapshotArtifact, 0, len(b.Artifacts))
	for _, artifact := range b.Artifacts {
		if artifact == nil {
			return "", fmt.Errorf("%w: Delivery Brief artifact is nil", domain.ErrValidation)
		}
		artifacts = append(artifacts, deliveryBriefSnapshotArtifact{
			ID: artifact.ID, RunID: artifact.RunID, LogicalPath: artifact.LogicalPath,
			Mime: artifact.Mime, Size: artifact.Size, Sha256: artifact.Sha256,
			Status: string(artifact.Status), CreatedAt: briefSnapshotTime(artifact.CreatedAt),
		})
	}

	var blocker *deliveryBriefSnapshotBlocker
	if b.Blocker != nil {
		blocker = &deliveryBriefSnapshotBlocker{
			Code: b.Blocker.Code, Message: b.Blocker.Message, Source: b.Blocker.Source,
			RunID: cloneStringPtr(b.Blocker.RunID), CreatedAt: briefSnapshotTime(b.Blocker.CreatedAt),
		}
	}
	risks := make([]deliveryBriefSnapshotRisk, 0, len(b.Risks))
	for _, risk := range b.Risks {
		risks = append(risks, deliveryBriefSnapshotRisk{
			SourceKind: risk.SourceKind, SourceID: risk.SourceID, Code: risk.Code,
			Message: risk.Message, Severity: risk.Severity,
		})
	}
	comments := make([]deliveryBriefSnapshotComment, 0, len(b.Comments))
	for _, comment := range b.Comments {
		if comment == nil {
			return "", fmt.Errorf("%w: Delivery Brief comment is nil", domain.ErrValidation)
		}
		comments = append(comments, deliveryBriefSnapshotComment{
			ID: comment.ID, WorkspaceID: comment.WorkspaceID, RootWorkItemID: comment.RootWorkItemID,
			WorkItemID: comment.WorkItemID, Revision: comment.Revision, Kind: string(comment.Kind),
			Body: comment.Body, ActorKind: string(comment.ActorKind), ActorID: comment.ActorID,
			SourceRunID: comment.SourceRunID, SourceRef: comment.SourceRef, ClientKey: comment.ClientKey,
			CreatedAt: briefSnapshotTime(comment.CreatedAt),
		})
	}

	payload := deliveryBriefSnapshotPayload{
		WorkItem:           wi,
		AcceptanceCriteria: append([]string{}, b.AcceptanceCriteria...),
		Conclusion: deliveryBriefSnapshotConclusion{
			CoordinatorStatus: b.Conclusion.CoordinatorStatus, Stage: b.Conclusion.Stage,
			Summary: b.Conclusion.Summary, NextAction: b.Conclusion.NextAction, Version: b.Conclusion.Version,
		},
		Attempts: attempts, Runs: runs, Changes: changes, Artifacts: artifacts, Blocker: blocker,
		Risks: risks, Comments: comments,
		Freshness: deliveryBriefSnapshotFreshness{
			SourceVersions: cloneInt64Map(b.Freshness.SourceVersions),
			State:          b.Freshness.State, MissingSources: append([]string{}, b.Freshness.MissingSources...),
		},
		Truncation: deliveryBriefSnapshotTruncation{
			Attempts: b.Truncation.Attempts, Runs: b.Truncation.Runs, Files: b.Truncation.Files,
			Artifacts: b.Truncation.Artifacts, Comments: b.Truncation.Comments,
		},
	}
	if payload.AcceptanceCriteria == nil {
		payload.AcceptanceCriteria = []string{}
	}
	if payload.Runs == nil {
		payload.Runs = []deliveryBriefSnapshotRun{}
	}
	if payload.Attempts == nil {
		payload.Attempts = []deliveryBriefSnapshotAttempt{}
	}
	if payload.Artifacts == nil {
		payload.Artifacts = []deliveryBriefSnapshotArtifact{}
	}
	if payload.Risks == nil {
		payload.Risks = []deliveryBriefSnapshotRisk{}
	}
	if payload.Comments == nil {
		payload.Comments = []deliveryBriefSnapshotComment{}
	}
	if payload.Freshness.SourceVersions == nil {
		payload.Freshness.SourceVersions = map[string]int64{}
	}
	if payload.Freshness.MissingSources == nil {
		payload.Freshness.MissingSources = []string{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("%w: marshal Delivery Brief snapshot payload: %v", domain.ErrValidation, err)
	}
	// domain.Seal performs the RFC 8785 canonicalization. Returning the
	// ordinary JSON here keeps this converter independent of digest internals.
	return string(raw), nil
}

func briefSnapshotTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func briefSnapshotOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := briefSnapshotTime(*value)
	return &formatted
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64Map(value map[string]int64) map[string]int64 {
	if value == nil {
		return map[string]int64{}
	}
	out := make(map[string]int64, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
