package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const (
	KnowledgeRecordIntentAgentRecord                 = "agent_record"
	KnowledgeRecordPublishIntentConfirmedRequirement = "confirmed_requirement"
)

// KnowledgeAgentRecordParams is the raw, Agent-bound knowledge intake. The
// caller identity comes from RunID at the application boundary; there is no
// user/Agent identity field that a model or browser can choose.
type KnowledgeAgentRecordParams struct {
	RunID         string
	Content       string
	Title         string
	PublishIntent string
	ClientKey     string
}

// KnowledgeAgentRecordResult exposes the two durable edges of one raw record:
// the source submission and the librarian curation Job. CurationStatus is a
// response projection, not another persisted lifecycle.
type KnowledgeAgentRecordResult struct {
	Submission        *domain.KnowledgeSubmission `json:"submission"`
	Job               *domain.KnowledgeJob        `json:"job,omitempty"`
	CurationStatus    string                      `json:"curation_status"`
	CurationError     string                      `json:"curation_error,omitempty"`
	PublicationStatus string                      `json:"publication_status,omitempty"`
}

// SubmitKnowledgeAgentRecord stores raw natural-language content and starts
// the built-in librarian curation path. A confirmed requirement carries an
// explicit, Run-bound publish intent; the librarian still structures it and
// the publication helper performs the normal evidence/version/permission CAS.
func (s *Service) SubmitKnowledgeAgentRecord(ctx context.Context, p KnowledgeAgentRecordParams) (*KnowledgeAgentRecordResult, error) {
	runID := strings.TrimSpace(p.RunID)
	content := p.Content
	clientKey := strings.TrimSpace(p.ClientKey)
	if runID == "" || strings.TrimSpace(content) == "" || clientKey == "" {
		return nil, fmt.Errorf("%w: active Run, content, and client_key are required", domain.ErrValidation)
	}
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status.IsTerminal() || run.Status == domain.RunCancelling || run.Status == domain.RunInterrupting {
		return nil, fmt.Errorf("%w: knowledge record requires an active Run", domain.ErrStateConflict)
	}
	if isKnowledgeLibrarianRun(run) {
		return nil, fmt.Errorf("%w: knowledge librarian cannot recursively record knowledge", domain.ErrValidation)
	}

	intent := strings.TrimSpace(p.PublishIntent)
	if intent != "" && intent != KnowledgeRecordPublishIntentConfirmedRequirement {
		return nil, fmt.Errorf("%w: unsupported knowledge record publish_intent %q", domain.ErrValidation, intent)
	}
	recordIntent := intent
	if recordIntent == "" {
		recordIntent = KnowledgeRecordIntentAgentRecord
	}
	cfg, err := s.GetKnowledgeLibrarianConfig(ctx, run.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || cfg.LibrarianAgentID == "" {
		return nil, fmt.Errorf("%w: knowledge librarian is disabled or unconfigured", domain.ErrCapabilityMissing)
	}
	if err := s.validateKnowledgeLibrarianConfig(ctx, cfg, true); err != nil {
		return nil, err
	}

	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = "待整理 Agent 记录"
	}
	durableKey := "run:" + run.ID + ":record:" + clientKey
	metadata := map[string]any{
		"origin_agent_id": run.AgentProfileID,
		"origin_run_id":   run.ID,
		"record_intent":   recordIntent,
	}
	kind := "observation"
	if intent == KnowledgeRecordPublishIntentConfirmedRequirement {
		kind = "requirement"
	}
	request := domain.KnowledgeSubmitCandidate{
		WorkspaceID: run.WorkspaceID,
		AgentID:     run.AgentProfileID,
		RunID:       run.ID,
		WorkItemID:  run.WorkItemID,
		ClientKey:   durableKey,
		Changes: []domain.KnowledgeChange{{
			Title:      title,
			Body:       content,
			Kind:       kind,
			Visibility: domain.KnowledgeVisibilityWorkspace,
			Sources: []domain.KnowledgeSourceInput{{
				Kind:     domain.KnowledgeSourceAgent,
				Ref:      run.AgentProfileID,
				Excerpt:  content,
				Metadata: metadata,
			}},
		}},
	}
	submission, err := s.SubmitKnowledgeCandidate(ctx, request)
	if err != nil {
		return nil, err
	}

	// An Agent record explicitly asks the librarian to process it. This is
	// independent of the task-terminal AutoCollect switch.
	job, curateErr := s.StartKnowledgeCuration(ctx, StartKnowledgeCurationParams{
		WorkspaceID:       run.WorkspaceID,
		RequestingAgentID: cfg.LibrarianAgentID,
		RequesterAgentID:  run.AgentProfileID,
		SourceRunID:       run.ID,
		SubmissionID:      submission.ID,
		ClientKey:         knowledgeCurationClientKey(submission.ID),
	})
	if curateErr != nil {
		result := &KnowledgeAgentRecordResult{
			Submission: submission, CurationStatus: string(domain.KnowledgeSubmissionReceived),
			CurationError: truncateKnowledgeRunes(curateErr.Error(), 2000),
		}
		if recordIntent != "" {
			result.PublicationStatus = "pending"
		}
		return result, nil
	}
	if current, getErr := s.GetKnowledgeSubmission(ctx, run.WorkspaceID, run.AgentProfileID, submission.ID); getErr == nil {
		submission = current
	}
	result := &KnowledgeAgentRecordResult{Submission: submission, Job: job, CurationStatus: string(job.Status)}
	if recordIntent != "" {
		result.PublicationStatus = knowledgeRecordPublicationStatus(job)
	}
	return result, nil
}

func knowledgeRecordPublicationStatus(job *domain.KnowledgeJob) string {
	if job != nil && job.Result != nil {
		if status, _ := job.Result["publication_status"].(string); status != "" {
			return status
		}
	}
	if job != nil && job.Status.IsTerminal() {
		return "needs_review"
	}
	return "pending"
}

func knowledgeCurationClientKey(submissionID string) string {
	return "auto-curate:" + submissionID
}

func knowledgeRecordIntent(submission *domain.KnowledgeSubmission) (string, bool) {
	if submission == nil || submission.AgentID == "" || submission.RunID == "" || len(submission.Request.Changes) == 0 {
		return "", false
	}
	for _, change := range submission.Request.Changes {
		for _, source := range change.Sources {
			if source.Kind != domain.KnowledgeSourceAgent || source.Ref != submission.AgentID || source.Metadata == nil {
				continue
			}
			intent, _ := source.Metadata["record_intent"].(string)
			originAgent, _ := source.Metadata["origin_agent_id"].(string)
			originRun, _ := source.Metadata["origin_run_id"].(string)
			if (intent == KnowledgeRecordIntentAgentRecord || intent == KnowledgeRecordPublishIntentConfirmedRequirement) &&
				originAgent == submission.AgentID && originRun == submission.RunID {
				return intent, true
			}
		}
	}
	return "", false
}

func knowledgeRecordPublishIntent(submission *domain.KnowledgeSubmission) bool {
	intent, ok := knowledgeRecordIntent(submission)
	return ok && intent == KnowledgeRecordPublishIntentConfirmedRequirement
}

func knowledgeRecordIsAgentRecord(submission *domain.KnowledgeSubmission) bool {
	_, ok := knowledgeRecordIntent(submission)
	return ok
}

func knowledgeRecordSourceRunCancelled(run *domain.ExecutionRun) bool {
	if run == nil {
		return false
	}
	return run.Status == domain.RunCancelling || run.Status == domain.RunInterrupting ||
		(run.Status.IsTerminal() && run.Status != domain.RunSucceeded)
}

func validateKnowledgeRecordAutoPublication(origin, curated *domain.KnowledgeSubmission) error {
	recordIntent, ok := knowledgeRecordIntent(origin)
	if !ok {
		return fmt.Errorf("%w: record intent is not bound to the origin Agent/Run", domain.ErrValidation)
	}
	if curated == nil || curated.WorkspaceID != origin.WorkspaceID || curated.AgentID == "" || len(curated.Request.Changes) == 0 {
		return fmt.Errorf("%w: curated record submission is incomplete", domain.ErrValidation)
	}
	for i, change := range curated.Request.Changes {
		if recordIntent == KnowledgeRecordPublishIntentConfirmedRequirement {
			if change.Kind != "requirement" && change.Kind != "agreement" {
				return fmt.Errorf("%w: confirmed record change %d is not a requirement/agreement", domain.ErrValidation, i)
			}
		} else if change.Kind != "observation" && change.Kind != "experience" {
			return fmt.Errorf("%w: Agent record change %d must remain an observation/experience", domain.ErrValidation, i)
		}
		if len(change.Sources) == 0 {
			return fmt.Errorf("%w: confirmed record change %d has no source", domain.ErrValidation, i)
		}
		for _, source := range change.Sources {
			if source.Kind != domain.KnowledgeSourceAgent || source.Ref != origin.AgentID || source.Metadata == nil {
				return fmt.Errorf("%w: confirmed record change %d source is not bound to the origin Agent", domain.ErrValidation, i)
			}
			sourceIntent, _ := source.Metadata["record_intent"].(string)
			originRun, _ := source.Metadata["origin_run_id"].(string)
			if sourceIntent != recordIntent || originRun != origin.RunID {
				return fmt.Errorf("%w: Agent record change %d source publish intent is not bound to the origin Run", domain.ErrValidation, i)
			}
		}
	}
	return nil
}

func knowledgeResultHasConflicts(result map[string]any) bool {
	if result == nil {
		return false
	}
	conflicts, ok := result["conflicts"].([]any)
	return ok && len(conflicts) > 0
}

// maybeAutoPublishKnowledgeRecord is deliberately post-curation and
// idempotent. An explicit Agent record may be published through the narrow
// observation/experience or confirmed requirement gate; ordinary submissions
// remain behind the existing human/owner publish gate.
func (s *Service) maybeAutoPublishKnowledgeRecord(ctx context.Context, jobID string) error {
	job, err := s.store.KnowledgeJobs().Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.SubmissionID == "" {
		if knowledgeRecordPublicationStatus(job) == "pending" {
			return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "自动发布记录缺少原始提交，已保留作业结果供复核", nil, nil)
		}
		return nil
	}
	origin, err := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, job.SubmissionID)
	if err != nil {
		return err
	}
	recordIntent, isRecord := knowledgeRecordIntent(origin)
	if !isRecord {
		if knowledgeRecordPublicationStatus(job) == "pending" {
			return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "原始记录缺少受信任的记录意图，已保留候选供复核", nil, nil)
		}
		return nil
	}
	curatedID, _ := job.Result["curated_submission_id"].(string)
	if curatedID == "" {
		if knowledgeRecordPublicationStatus(job) == "pending" {
			return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "整理结果缺少候选提交，已保留原始记录供复核", nil, nil)
		}
		return nil
	}
	if status, _ := job.Result["publication_status"].(string); status == "published" || status == "needs_review" || status == "unchanged" {
		return nil
	}
	if origin.RunID != "" {
		sourceRun, runErr := s.store.Runs().Get(ctx, origin.RunID)
		if runErr != nil {
			if errors.Is(runErr, domain.ErrNotFound) {
				return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "原始记录 Run 不存在，已阻止自动发布", nil, nil)
			}
			return runErr
		}
		if knowledgeRecordSourceRunCancelled(sourceRun) {
			return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "原始记录 Run 已失败或取消，已阻止自动发布", nil, nil)
		}
	}
	if job.Coverage.Status == domain.KnowledgeCoverageConflict || knowledgeResultHasConflicts(job.Result) {
		return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "存在未解决冲突，已保留候选供复核", nil, nil)
	}
	curated, err := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, curatedID)
	if err != nil {
		return err
	}
	if curated.Status == domain.KnowledgeSubmissionAccepted {
		refs, refErr := s.knowledgePublishedRecordRefs(ctx, curated)
		if refErr != nil {
			return refErr
		}
		return s.recordKnowledgePublicationFeedback(ctx, job.ID, "published", "", curated, refs)
	}
	if curated.Status != domain.KnowledgeSubmissionNeedsReview {
		return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "整理结果尚未达到自动发布门，已保留候选", curated, nil)
	}
	if err := validateKnowledgeRecordAutoPublication(origin, curated); err != nil {
		return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", err.Error(), curated, nil)
	}
	if _, err := s.publishKnowledgeSubmissionForAgent(ctx, job.WorkspaceID, job.AgentProfileID, origin.AgentID, origin.RunID, recordIntent, curated.ID); err != nil {
		return s.recordKnowledgePublicationFeedback(ctx, job.ID, "needs_review", "自动发布未通过版本/证据校验："+truncateKnowledgeRunes(err.Error(), 1800), curated, nil)
	}
	curated, err = s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, curated.ID)
	if err != nil {
		return err
	}
	refs, err := s.knowledgePublishedRecordRefs(ctx, curated)
	if err != nil {
		return err
	}
	return s.recordKnowledgePublicationFeedback(ctx, job.ID, "published", "", curated, refs)
}

func (s *Service) knowledgePublishedRecordRefs(ctx context.Context, submission *domain.KnowledgeSubmission) ([]map[string]any, error) {
	if submission == nil {
		return nil, fmt.Errorf("%w: published record submission required", domain.ErrValidation)
	}
	refs := make([]map[string]any, 0, len(submission.ResultVersionIDs))
	for _, versionID := range submission.ResultVersionIDs {
		version, owner, err := s.knowledgePreparedVersion(ctx, submission, versionID)
		if err != nil {
			return nil, err
		}
		sources, err := s.store.Knowledge().ListVersionSources(ctx, submission.WorkspaceID, owner, versionID)
		if err != nil {
			return nil, err
		}
		sourceIDs := make([]string, 0, len(sources))
		for _, source := range sources {
			if source != nil {
				sourceIDs = append(sourceIDs, source.ID)
			}
		}
		refs = append(refs, map[string]any{
			"item_id": version.ItemID, "version_id": version.ID,
			"title": version.Title, "status": string(version.Status), "source_ids": sourceIDs,
		})
	}
	return refs, nil
}

func (s *Service) recordKnowledgePublicationFeedback(ctx context.Context, jobID, status, feedback string, submission *domain.KnowledgeSubmission, refs []map[string]any) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		job, err := s.store.KnowledgeJobs().Get(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Result == nil {
			job.Result = map[string]any{}
		} else {
			job.Result = mapsCloneAny(job.Result)
		}
		job.Result["publication_status"] = status
		if feedback != "" {
			job.Result["publication_feedback"] = truncateKnowledgeRunes(feedback, 4000)
		}
		if submission != nil {
			job.Result["published_submission_id"] = submission.ID
			job.Result["published_item_ids"] = append([]string(nil), submission.ResultItemIDs...)
			job.Result["published_version_ids"] = append([]string(nil), submission.ResultVersionIDs...)
		}
		if refs != nil {
			job.Result["published_references"] = refs
		}
		job.UpdatedAt = time.Now().UTC()
		return s.store.KnowledgeJobs().Update(ctx, job, job.Version)
	})
}
