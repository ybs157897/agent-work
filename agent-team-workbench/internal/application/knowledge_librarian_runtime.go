package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const (
	knowledgeJobMaxRetries = 3
	knowledgeJobMaxReadIDs = 100
)

// createKnowledgeContinuationLocked creates the next bounded model turn in
// the same Chat WorkItem and transaction as the job CAS. It is the only place
// that advances CurrentRunID after the first turn, so a terminal replay cannot
// mint a second continuation.
func (s *Service) createKnowledgeContinuationLocked(ctx context.Context, job *domain.KnowledgeJob,
	sourceRun *domain.ExecutionRun, instruction string, resetSession bool) (*domain.ExecutionRun, error) {
	if job == nil || sourceRun == nil {
		return nil, fmt.Errorf("%w: knowledge continuation requires job and source Run", domain.ErrValidation)
	}
	if job.Status.IsTerminal() || job.CurrentRunID != sourceRun.ID {
		return nil, fmt.Errorf("%w: knowledge continuation source is no longer current", domain.ErrStateConflict)
	}
	nextTurn := job.TurnSeq + 1
	if nextTurn < 1 || nextTurn > int64(job.Budget.Normalize().MaxTurns) {
		return nil, fmt.Errorf("%w: knowledge turn budget exhausted", domain.ErrStateConflict)
	}
	if strings.TrimSpace(instruction) == "" {
		return nil, fmt.Errorf("%w: knowledge continuation instruction required", domain.ErrValidation)
	}
	if resetSession {
		if err := s.writeAnchorTombstoneForRun(ctx, sourceRun.WorkspaceID, sourceRun.AgentProfileID,
			sourceRun.AdapterID, sourceRun.WorkItemID, sourceRun.ID, "knowledge_librarian_schema_repair"); err != nil {
			return nil, err
		}
	}
	clientKey := knowledgeJobRunClientKey(job.ID, nextTurn)
	var next *domain.ExecutionRun
	if existing, err := s.store.Runs().GetByClientKey(ctx, job.WorkspaceID, clientKey); err == nil {
		next = existing
		marker, valid := knowledgeLibrarianMarker(existing)
		if !valid || marker.JobID != job.ID || marker.TurnSeq != nextTurn || existing.WorkItemID != job.WorkItemID || existing.AgentProfileID != job.AgentProfileID {
			return nil, fmt.Errorf("%w: knowledge continuation client key is owned by another Run", domain.ErrIdempotencyConflict)
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	} else {
		outputContract, _ := sourceRun.Input["output_contract"].(string)
		next, err = s.createRunLocked(ctx, job.WorkItemID, CreateRunParams{
			AgentProfileID:          job.AgentProfileID,
			RuntimePreference:       runtimePreferenceOf(sourceRun.Input["runtime_preference"]),
			Instruction:             instruction,
			OutputContract:          outputContract,
			ClientKey:               clientKey,
			ContextSource:           domain.SnapshotSourceInherited,
			ContextSourceSnapshotID: sourceRun.ContextSnapshotID,
			knowledgeJobID:          job.ID,
			knowledgeTurnSeq:        nextTurn,
		})
		if err != nil {
			return nil, err
		}
	}
	job.CurrentRunID = next.ID
	job.TurnSeq = nextTurn
	job.Status = domain.KnowledgeJobRunning
	job.NextActionAt = nil
	job.LastError = ""
	job.UpdatedAt = time.Now().UTC()
	if err := s.store.KnowledgeJobs().Update(ctx, job, job.Version); err != nil {
		return nil, err
	}
	return next, nil
}

// maybeAdvanceKnowledgeLibrarian consumes one terminal librarian Run. It is
// deliberately idempotent: the durable job current-run fence and action
// receipt prevent a late callback from applying the same decision twice.
func (s *Service) maybeAdvanceKnowledgeLibrarian(ctx context.Context, run *domain.ExecutionRun) (bool, error) {
	marker, ok := knowledgeLibrarianMarker(run)
	if !ok || run == nil || !run.Status.IsTerminal() {
		return false, nil
	}
	// Canonical usage is the only safe source for session-cumulative reports.
	// Chat Runs have no governance quota hook, so this explicit terminal hook
	// freezes the same Run accounting before job budget accounting reads it.
	if _, err := s.maybeCanonicalizeRunUsage(context.WithoutCancel(ctx), run); err != nil {
		return false, err
	}
	freshRun, err := s.store.Runs().Get(context.WithoutCancel(ctx), run.ID)
	if err != nil {
		return false, err
	}
	var next *domain.ExecutionRun
	acted := false
	err = s.store.InTx(context.WithoutCancel(ctx), func(txctx context.Context) error {
		job, err := s.store.KnowledgeJobs().Get(txctx, marker.JobID)
		if err != nil {
			return err
		}
		if job.Status.IsTerminal() || job.CurrentRunID != freshRun.ID || job.TurnSeq != marker.TurnSeq {
			return nil // late/replayed terminal event
		}
		acted = true
		if job.Mode == domain.KnowledgeJobInquiry && job.SourceRunID != "" {
			if source, sourceErr := s.store.Runs().Get(txctx, job.SourceRunID); sourceErr == nil && source.Status.IsTerminal() {
				return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobCancelled,
					"source Run 已终态，知识调查随调用方取消")
			} else if sourceErr != nil && !errors.Is(sourceErr, domain.ErrNotFound) {
				return sourceErr
			}
		}
		if knowledgeJobExpired(job, time.Now().UTC()) {
			return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobIncomplete,
				"知识管理员作业超过墙钟预算")
		}
		if err := accountKnowledgeRun(job, freshRun); err != nil {
			return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobIncomplete, err.Error())
		}
		switch freshRun.Status {
		case domain.RunCancelled:
			return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobCancelled, "知识管理员 Run 已取消")
		case domain.RunInterrupted:
			return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobIncomplete, "知识管理员 Run 被中断")
		case domain.RunLost:
			if knowledgeJobBudgetExceeded(job) {
				return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobIncomplete, "知识管理员作业预算已耗尽")
			}
			return s.scheduleKnowledgeRetryLocked(txctx, job, "知识管理员 Run 丢失，等待恢复")
		case domain.RunFailed:
			if knowledgeJobBudgetExceeded(job) {
				return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobIncomplete, "知识管理员作业预算已耗尽")
			}
			if freshRun.Failure != nil && freshRun.Failure.Retryable && job.RetryCount < knowledgeJobMaxRetries {
				return s.scheduleKnowledgeRetryLocked(txctx, job, "知识管理员 Run 可重试失败")
			}
			return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobFailed,
				knowledgeRunFailureMessage(freshRun))
		case domain.RunSucceeded:
			// continue below; only a succeeded model turn can emit a decision
		default:
			return nil
		}
		if knowledgeJobBudgetExceeded(job) {
			return s.finishKnowledgeJobLocked(txctx, job, domain.KnowledgeJobIncomplete,
				"知识管理员作业预算已耗尽")
		}
		changed, revisionErr := s.ensureKnowledgeIndexRevisionLocked(txctx, job)
		if revisionErr != nil {
			return revisionErr
		}
		if changed {
			return nil
		}
		text, err := s.runFinalText(txctx, freshRun.ID)
		if err != nil {
			return err
		}
		decision, err := DecodeKnowledgeLibrarianDecision([]byte(text))
		if err != nil {
			return s.repairKnowledgeDecisionLocked(txctx, job, freshRun, err, &next)
		}
		return s.applyKnowledgeDecisionLocked(txctx, job, freshRun, decision, &next)
	})
	if err != nil {
		if knowledgeSemanticDecisionError(err) {
			// The transaction above intentionally rolls back action/job writes
			// on a model/domain validation failure. Close the current job in a
			// fresh transaction so a bad finish cannot leave the source receipt
			// in processing forever; storage/transport errors remain retryable.
			if settleErr := s.settleKnowledgeSemanticFailure(context.WithoutCancel(ctx), marker, freshRun.ID, err); settleErr != nil {
				return acted, settleErr
			}
			return true, nil
		}
		return acted, err
	}
	if next != nil {
		if err := s.dispatchCommittedRun(context.WithoutCancel(ctx), next); err != nil {
			return true, err
		}
	}
	return acted, nil
}

func knowledgeSemanticDecisionError(err error) bool {
	return errors.Is(err, domain.ErrValidation) || errors.Is(err, domain.ErrVersionConflict) ||
		errors.Is(err, domain.ErrStateConflict) || errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrIdempotencyConflict)
}

func (s *Service) settleKnowledgeSemanticFailure(ctx context.Context, marker KnowledgeLibrarianRunMarker, runID string, cause error) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		job, err := s.store.KnowledgeJobs().Get(ctx, marker.JobID)
		if err != nil {
			return err
		}
		if job.Status.IsTerminal() || job.CurrentRunID != runID {
			// A changed current run means another recovery owner won.
			return nil
		}
		return s.finishKnowledgeJobLocked(ctx, job, domain.KnowledgeJobIncomplete,
			truncateKnowledgeRunes("知识管理员决策未通过应用层校验："+cause.Error(), 4000))
	})
}

func (s *Service) ensureKnowledgeIndexRevisionLocked(ctx context.Context, job *domain.KnowledgeJob) (bool, error) {
	if job == nil {
		return false, fmt.Errorf("%w: knowledge job required", domain.ErrValidation)
	}
	state, err := s.store.Knowledge().GetIndexState(ctx, job.WorkspaceID)
	if err != nil {
		return false, err
	}
	revision := int64(0)
	if state != nil {
		revision = state.Revision
	}
	if revision == job.IndexRevision {
		return false, nil
	}
	return true, s.finishKnowledgeJobLocked(ctx, job, domain.KnowledgeJobIncomplete,
		fmt.Sprintf("知识索引版本从 %d 变为 %d，调查快照不可继续；请重新发起查询", job.IndexRevision, revision))
}

func accountKnowledgeRun(job *domain.KnowledgeJob, run *domain.ExecutionRun) error {
	if job == nil || run == nil {
		return fmt.Errorf("%w: knowledge usage requires job and Run", domain.ErrValidation)
	}
	if knowledgeRunAlreadyAccounted(job, run.ID) {
		return nil
	}
	inTokens, outTokens, inputKnown, outputKnown := knowledgeRunTokenUsage(run)
	if run.UsageBasis == domain.UsageBasisSessionCumulative && (!inputKnown || !outputKnown) {
		return fmt.Errorf("知识管理员 Run %s 的累计用量没有 canonical delta，无法安全计入预算", run.ID)
	}
	budget := job.Budget.Normalize()
	if (budget.MaxInputTokens > 0 && !inputKnown) || (budget.MaxOutputTokens > 0 && !outputKnown) {
		return fmt.Errorf("知识管理员 Run %s 的 token 用量不完整，无法安全校验预算", run.ID)
	}
	job.Used.Turns++
	job.Used.InputTokens += inTokens
	job.Used.OutputTokens += outTokens
	markKnowledgeRunAccounted(job, run.ID)
	return nil
}

func knowledgeRunTokenUsage(run *domain.ExecutionRun) (inTokens, outTokens int64, inputKnown, outputKnown bool) {
	if run == nil {
		return 0, 0, false, false
	}
	if canonical := run.CanonicalUsage; canonical != nil {
		if canonical.Counters.InputTokensTotal != nil {
			inTokens = *canonical.Counters.InputTokensTotal
			inputKnown = true
		} else if run.UsageBasis != domain.UsageBasisSessionCumulative {
			inTokens = run.UsageIn
			inputKnown = true
		}
		if canonical.Counters.OutputTokens != nil {
			outTokens = *canonical.Counters.OutputTokens
			outputKnown = true
		} else if run.UsageBasis != domain.UsageBasisSessionCumulative {
			outTokens = run.UsageOut
			outputKnown = true
		}
		return inTokens, outTokens, inputKnown, outputKnown
	}
	if run.UsageBasis == "" || run.UsageBasis == domain.UsageBasisPerRun {
		return run.UsageIn, run.UsageOut, true, true
	}
	return 0, 0, false, false
}

func knowledgeRunAlreadyAccounted(job *domain.KnowledgeJob, runID string) bool {
	if job == nil || job.Result == nil {
		return false
	}
	raw, _ := job.Result["_accounted_run_ids"].([]any)
	for _, value := range raw {
		if id, _ := value.(string); id == runID {
			return true
		}
	}
	if ids, ok := job.Result["_accounted_run_ids"].([]string); ok {
		for _, id := range ids {
			if id == runID {
				return true
			}
		}
	}
	return false
}

func markKnowledgeRunAccounted(job *domain.KnowledgeJob, runID string) {
	if job.Result == nil {
		job.Result = map[string]any{}
	}
	ids := make([]string, 0, 4)
	if raw, ok := job.Result["_accounted_run_ids"].([]any); ok {
		for _, value := range raw {
			if id, _ := value.(string); id != "" {
				ids = appendUniqueString(ids, id)
			}
		}
	} else if raw, ok := job.Result["_accounted_run_ids"].([]string); ok {
		ids = append(ids, raw...)
	}
	job.Result["_accounted_run_ids"] = appendUniqueString(ids, runID)
}

func knowledgeJobBudgetExceeded(job *domain.KnowledgeJob) bool {
	if job == nil {
		return true
	}
	b := job.Budget.Normalize()
	return job.Used.Turns > b.MaxTurns ||
		(b.MaxInputTokens > 0 && job.Used.InputTokens > b.MaxInputTokens) ||
		(b.MaxOutputTokens > 0 && job.Used.OutputTokens > b.MaxOutputTokens) ||
		job.Used.Searches > b.MaxSearches || job.Used.Relations > b.MaxRelations
}

func (s *Service) finishKnowledgeJobLocked(ctx context.Context, job *domain.KnowledgeJob,
	status domain.KnowledgeJobStatus, reason string) error {
	if job == nil {
		return fmt.Errorf("%w: knowledge job required", domain.ErrValidation)
	}
	if job.Status.IsTerminal() {
		return nil
	}
	job.Status = status
	job.LastError = truncateKnowledgeRunes(strings.TrimSpace(reason), 4000)
	job.NextActionAt = nil
	now := time.Now().UTC()
	job.UpdatedAt, job.FinishedAt = now, &now
	if job.Mode == domain.KnowledgeJobCuration && job.SubmissionID != "" {
		if origin, err := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, job.SubmissionID); err == nil && origin.Status == domain.KnowledgeSubmissionProcessing {
			if err := s.store.Knowledge().UpdateSubmissionStatus(ctx, origin.ID, domain.KnowledgeSubmissionNeedsReview,
				origin.ResultItemIDs, origin.ResultVersionIDs, truncateKnowledgeRunes(reason, 4000), origin.Version); err != nil {
				return err
			}
		}
	}
	return s.store.KnowledgeJobs().Update(ctx, job, job.Version)
}

func (s *Service) scheduleKnowledgeRetryLocked(ctx context.Context, job *domain.KnowledgeJob, reason string) error {
	job.RetryCount++
	if job.RetryCount > knowledgeJobMaxRetries {
		return s.finishKnowledgeJobLocked(ctx, job, domain.KnowledgeJobFailed, reason+"，重试次数已用尽")
	}
	delay := time.Duration(job.RetryCount) * time.Second
	next := time.Now().UTC().Add(delay)
	job.Status = domain.KnowledgeJobWaitingRetry
	job.NextActionAt = &next
	job.LastError = truncateKnowledgeRunes(reason, 4000)
	job.UpdatedAt = time.Now().UTC()
	return s.store.KnowledgeJobs().Update(ctx, job, job.Version)
}

func knowledgeRunFailureMessage(run *domain.ExecutionRun) string {
	if run == nil || run.Failure == nil {
		return "知识管理员 Run 失败"
	}
	if run.Failure.Message != "" {
		return truncateKnowledgeRunes(run.Failure.Message, 4000)
	}
	if run.Failure.Code != "" {
		return "知识管理员 Run 失败：" + run.Failure.Code
	}
	return "知识管理员 Run 失败"
}

func (s *Service) repairKnowledgeDecisionLocked(ctx context.Context, job *domain.KnowledgeJob,
	sourceRun *domain.ExecutionRun, decodeErr error, next **domain.ExecutionRun) error {
	job.LastError = truncateKnowledgeRunes(decodeErr.Error(), 4000)
	job.RepairAttempt++
	if job.RepairAttempt > maxKnowledgeRepairAttempts {
		return s.finishKnowledgeJobLocked(ctx, job, domain.KnowledgeJobFailed,
			"知识管理员输出连续未通过 schema 校验")
	}
	instruction := knowledgeJobInstruction(job, map[string]any{
		"repair_of": sourceRun.ID, "repair_attempt": job.RepairAttempt,
	}, knowledgeJobRepairMessage+" "+decodeErr.Error())
	returned, err := s.createKnowledgeContinuationLocked(ctx, job, sourceRun, instruction, false)
	if err != nil {
		return err
	}
	*next = returned
	return nil
}

func knowledgeDecisionMap(decision *KnowledgeLibrarianDecision) map[string]any {
	if decision == nil {
		return map[string]any{}
	}
	raw, _ := json.Marshal(decision)
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	if result == nil {
		result = map[string]any{}
	}
	return result
}

func knowledgeDecisionPayload(decision *KnowledgeLibrarianDecision) ([]byte, map[string]any, error) {
	if decision == nil {
		return nil, nil, fmt.Errorf("%w: knowledge decision required", domain.ErrValidation)
	}
	raw, err := json.Marshal(decision)
	if err != nil {
		return nil, nil, err
	}
	return raw, knowledgeDecisionMap(decision), nil
}

func (s *Service) applyKnowledgeDecisionLocked(ctx context.Context, job *domain.KnowledgeJob,
	run *domain.ExecutionRun, decision *KnowledgeLibrarianDecision, next **domain.ExecutionRun) error {
	raw, request, err := knowledgeDecisionPayload(decision)
	if err != nil {
		return err
	}
	digest := knowledgeDigest(raw)
	action := &domain.KnowledgeJobAction{JobID: job.ID, TurnSeq: runMarkerTurnSeq(run), RunID: run.ID,
		Kind: decision.Action, Status: domain.KnowledgeJobActionPending, RequestDigest: digest, Request: request,
		Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	created, existing, err := s.store.KnowledgeJobs().ClaimAction(ctx, action)
	if err != nil {
		return err
	}
	if !created {
		if existing.Status == domain.KnowledgeJobActionApplied {
			return nil
		}
		action = existing
	}
	var result map[string]any
	switch decision.Action {
	case domain.KnowledgeJobActionSearch:
		result, err = s.applyKnowledgeSearchLocked(ctx, job, decision.Search)
	case domain.KnowledgeJobActionRead:
		result, err = s.applyKnowledgeReadLocked(ctx, job, decision.Read)
	case domain.KnowledgeJobActionRelations:
		result, err = s.applyKnowledgeRelationsLocked(ctx, job, decision.Relations)
	case domain.KnowledgeJobActionFinish:
		result, err = s.applyKnowledgeFinishLocked(ctx, job, decision.Finish)
	default:
		err = fmt.Errorf("%w: unsupported knowledge action %q", domain.ErrValidation, decision.Action)
	}
	if err != nil {
		action.Status = domain.KnowledgeJobActionFailed
		action.ErrorMessage = truncateKnowledgeRunes(err.Error(), 4000)
		action.UpdatedAt = time.Now().UTC()
		_ = s.store.KnowledgeJobs().UpdateAction(ctx, action, action.Version)
		return err
	}
	action.Status = domain.KnowledgeJobActionApplied
	action.Result = result
	action.ErrorMessage = ""
	action.UpdatedAt = time.Now().UTC()
	if err := s.store.KnowledgeJobs().UpdateAction(ctx, action, action.Version); err != nil {
		return err
	}
	if job.Status.IsTerminal() {
		job.LastDecisionDigest = digest
		job.UpdatedAt = time.Now().UTC()
		return s.store.KnowledgeJobs().Update(ctx, job, job.Version)
	}
	job.LastDecisionDigest = digest
	if knowledgeJobBudgetExceeded(job) || job.Used.Turns >= job.Budget.Normalize().MaxTurns {
		return s.finishKnowledgeJobLocked(ctx, job, domain.KnowledgeJobIncomplete, "知识管理员作业预算已耗尽")
	}
	instruction := knowledgeJobInstruction(job, result, "")
	createdRun, err := s.createKnowledgeContinuationLocked(ctx, job, run, instruction, false)
	if err != nil {
		return err
	}
	*next = createdRun
	return nil
}

func runMarkerTurnSeq(run *domain.ExecutionRun) int64 {
	marker, ok := knowledgeLibrarianMarker(run)
	if !ok {
		return 0
	}
	return marker.TurnSeq
}

func (s *Service) applyKnowledgeSearchLocked(ctx context.Context, job *domain.KnowledgeJob,
	search *KnowledgeSearchDecision) (map[string]any, error) {
	if search == nil {
		return nil, fmt.Errorf("%w: search payload required", domain.ErrValidation)
	}
	budget := job.Budget.KnowledgeBudget.Normalize()
	if job.Used.Searches >= budget.MaxSearches {
		return nil, fmt.Errorf("%w: knowledge search budget exhausted", domain.ErrStateConflict)
	}
	question := strings.TrimSpace(search.Question)
	if question == "" {
		question = job.Question
	}
	query := &domain.KnowledgeQuery{WorkspaceID: job.WorkspaceID, RequesterAgentID: job.RequestingAgentID,
		Scope: job.Scope, Question: question, Context: job.Context, Terms: append([]string(nil), search.Terms...),
		ItemIDs: append([]string(nil), search.ItemIDs...), Direction: search.Direction, Budget: budget}
	hits, err := s.store.Knowledge().Search(ctx, query)
	if err != nil {
		return nil, err
	}
	job.Used.Searches++
	if hits == nil {
		hits = []*domain.KnowledgeHit{}
	}
	result := map[string]any{"action": "search", "hits": []any{}, "count": len(hits)}
	hitOutput := make([]any, 0, len(hits))
	for _, hit := range hits {
		if hit == nil {
			continue
		}
		job.VisitedVersionIDs = appendUniqueString(job.VisitedVersionIDs, hit.Version.ID)
		for _, source := range hit.Sources {
			job.EvidenceIDs = appendUniqueString(job.EvidenceIDs, source.ID)
		}
		for i := range hit.Relations {
			relation := hit.Relations[i]
			appendKnowledgeRequiredRelation(job, &relation)
			for _, sourceID := range relation.SourceIDs {
				job.EvidenceIDs = appendUniqueString(job.EvidenceIDs, sourceID)
			}
		}
		hitOutput = append(hitOutput, map[string]any{"item": hit.Item, "version": hit.Version, "snippet": hit.Snippet, "relations": hit.Relations, "sources": hit.Sources})
	}
	result["hits"] = hitOutput
	if job.RequestingAgentID != "human:shared" {
		snapshot, snapshotErr := s.createKnowledgeSearchSnapshotLocked(ctx, job, question, hits, budget)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		if snapshot != nil {
			job.SnapshotIDs = appendUniqueString(job.SnapshotIDs, snapshot.ID)
			result["snapshot_id"] = snapshot.ID
		}
	}
	return boundKnowledgeResult(job, result), nil
}

func (s *Service) createKnowledgeSearchSnapshotLocked(ctx context.Context, job *domain.KnowledgeJob,
	question string, hits []*domain.KnowledgeHit, budget domain.KnowledgeBudget) (*domain.KnowledgeQuerySnapshot, error) {
	if job == nil || job.RequestingAgentID == "" || job.RequestingAgentID == "human:shared" {
		return nil, nil
	}
	snapshot := &domain.KnowledgeQuerySnapshot{ID: domain.NewID(domain.PrefixKnowledgeQuerySnapshot),
		WorkspaceID: job.WorkspaceID, RequesterAgentID: job.RequestingAgentID, Question: question,
		Context: job.Context, Scope: job.Scope, Budget: budget, IndexRevision: job.IndexRevision,
		Results: []domain.KnowledgeHit{}, Coverage: domain.KnowledgeCoverage{Status: domain.KnowledgeCoverageComplete, Budget: budget, Entries: []domain.KnowledgeCoverageEntry{}}, CreatedAt: time.Now().UTC()}
	for _, hit := range hits {
		if hit == nil {
			continue
		}
		snapshot.Results = append(snapshot.Results, *hit)
		entry := domain.KnowledgeCoverageEntry{Subject: hit.Item.ID, Status: domain.KnowledgeCoverageComplete}
		for _, source := range hit.Sources {
			entry.EvidenceIDs = appendUniqueString(entry.EvidenceIDs, source.ID)
		}
		for _, relation := range hit.Relations {
			other := relation.ToItemID
			if other == hit.Item.ID {
				other = relation.FromItemID
			}
			entry.RelatedItemIDs = appendUniqueString(entry.RelatedItemIDs, other)
		}
		snapshot.Coverage.Entries = append(snapshot.Coverage.Entries, entry)
	}
	if len(snapshot.Results) == 0 {
		snapshot.Coverage.Status = domain.KnowledgeCoverageMissing
		snapshot.Coverage.Entries = append(snapshot.Coverage.Entries, domain.KnowledgeCoverageEntry{Subject: question, Status: domain.KnowledgeCoverageMissing})
	}
	if err := s.store.Knowledge().CreateQuerySnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Service) applyKnowledgeReadLocked(ctx context.Context, job *domain.KnowledgeJob,
	read *KnowledgeReadDecision) (map[string]any, error) {
	if read == nil {
		return nil, fmt.Errorf("%w: read payload required", domain.ErrValidation)
	}
	itemIDs := dedupeKnowledgeIDs(read.ItemIDs)
	versionIDs := dedupeKnowledgeIDs(read.VersionIDs)
	if len(itemIDs)+len(versionIDs) > knowledgeJobMaxReadIDs {
		return nil, fmt.Errorf("%w: read request exceeds %d IDs", domain.ErrValidation, knowledgeJobMaxReadIDs)
	}
	result := map[string]any{"action": "read", "items": []any{}, "versions": []any{}}
	items := make([]any, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		item, err := s.store.Knowledge().GetItem(ctx, job.WorkspaceID, job.RequestingAgentID, itemID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				job.Observations = append(job.Observations, "read 未找到或不可见条目 "+itemID)
				continue
			}
			return nil, err
		}
		entry := map[string]any{"item": item}
		if item.CurrentVersionID != "" {
			version, err := s.store.Knowledge().GetVersion(ctx, job.WorkspaceID, job.RequestingAgentID, item.CurrentVersionID)
			if err == nil {
				sources, sourceErr := s.store.Knowledge().ListVersionSources(ctx, job.WorkspaceID, job.RequestingAgentID, version.ID)
				if sourceErr != nil && !errors.Is(sourceErr, domain.ErrNotFound) {
					return nil, sourceErr
				}
				entry["version"], entry["sources"] = version, sources
				job.VisitedVersionIDs = appendUniqueString(job.VisitedVersionIDs, version.ID)
				for _, source := range sources {
					if source != nil {
						job.EvidenceIDs = appendUniqueString(job.EvidenceIDs, source.ID)
					}
				}
			} else if !errors.Is(err, domain.ErrNotFound) {
				return nil, err
			}
		}
		job.RequiredItemIDs = appendUniqueString(job.RequiredItemIDs, item.ID)
		items = append(items, entry)
	}
	versions := make([]any, 0, len(versionIDs))
	for _, versionID := range versionIDs {
		version, err := s.store.Knowledge().GetVersion(ctx, job.WorkspaceID, job.RequestingAgentID, versionID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				job.Observations = append(job.Observations, "read 未找到或不可见版本 "+versionID)
				continue
			}
			return nil, err
		}
		sources, err := s.store.Knowledge().ListVersionSources(ctx, job.WorkspaceID, job.RequestingAgentID, version.ID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		versions = append(versions, map[string]any{"version": version, "sources": sources})
		job.VisitedVersionIDs = appendUniqueString(job.VisitedVersionIDs, version.ID)
		for _, source := range sources {
			if source != nil {
				job.EvidenceIDs = appendUniqueString(job.EvidenceIDs, source.ID)
			}
		}
	}
	job.Used.Reads += len(items) + len(versions)
	result["items"], result["versions"] = items, versions
	return boundKnowledgeResult(job, result), nil
}

func (s *Service) applyKnowledgeRelationsLocked(ctx context.Context, job *domain.KnowledgeJob,
	decision *KnowledgeRelationsDecision) (map[string]any, error) {
	if decision == nil {
		return nil, fmt.Errorf("%w: relations payload required", domain.ErrValidation)
	}
	budget := job.Budget.KnowledgeBudget.Normalize()
	if job.Used.Relations >= budget.MaxRelations {
		return nil, fmt.Errorf("%w: knowledge relation budget exhausted", domain.ErrStateConflict)
	}
	itemIDs := dedupeKnowledgeIDs(decision.ItemIDs)
	if len(itemIDs) == 0 {
		return nil, fmt.Errorf("%w: relations requires item IDs", domain.ErrValidation)
	}
	direction := decision.Direction
	if direction == "" {
		direction = domain.KnowledgeRelationBoth
	}
	remaining := budget.MaxRelations - job.Used.Relations
	result := map[string]any{"action": "relations", "relations": []any{}}
	relations := make([]any, 0)
	for _, itemID := range itemIDs {
		if remaining <= 0 {
			break
		}
		found, err := s.store.Knowledge().ListRelations(ctx, job.WorkspaceID, job.RequestingAgentID, itemID, direction, remaining)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			return nil, err
		}
		for _, relation := range found {
			if relation == nil {
				continue
			}
			appendKnowledgeRequiredRelation(job, relation)
			job.Used.Relations++
			remaining--
			for _, sourceID := range relation.SourceIDs {
				job.EvidenceIDs = appendUniqueString(job.EvidenceIDs, sourceID)
			}
			relations = append(relations, relation)
		}
	}
	job.Used.Relations = minKnowledgeInt(job.Used.Relations, budget.MaxRelations)
	result["relations"] = relations
	return boundKnowledgeResult(job, result), nil
}

func (s *Service) applyKnowledgeFinishLocked(ctx context.Context, job *domain.KnowledgeJob,
	finish *KnowledgeFinishDecision) (map[string]any, error) {
	if finish == nil {
		return nil, fmt.Errorf("%w: finish payload required", domain.ErrValidation)
	}
	gate := KnowledgeFinishGate(job, finish)
	if len(finish.Coverage) > 0 {
		job.Coverage.Entries = append([]domain.KnowledgeCoverageEntry(nil), finish.Coverage...)
	}
	job.Coverage.Status = gate.Status
	if len(gate.Gaps) > 0 {
		finish.Gaps = unionKnowledgeStrings(finish.Gaps, gate.Gaps)
	}
	if finish.Status == domain.KnowledgeCoverageComplete && gate.Status != domain.KnowledgeCoverageComplete {
		finish.Status = gate.Status
	}
	if len(finish.EvidenceIDs) > 0 {
		for _, evidenceID := range finish.EvidenceIDs {
			job.EvidenceIDs = appendUniqueString(job.EvidenceIDs, evidenceID)
		}
	}
	result := map[string]any{"action": "finish", "status": string(gate.Status), "answer": truncateKnowledgeRunes(finish.Answer, knowledgeJobMaxObservationRunes), "summary": truncateKnowledgeRunes(finish.Summary, knowledgeJobMaxObservationRunes), "coverage": finish.Coverage, "evidence_ids": finish.EvidenceIDs, "citations": finish.Citations, "relations": finish.Relations, "gaps": finish.Gaps, "conflicts": finish.Conflicts, "snapshot_ids": append([]string(nil), job.SnapshotIDs...)}
	if job.Result != nil {
		if accounted, ok := job.Result["_accounted_run_ids"]; ok {
			result["_accounted_run_ids"] = accounted
		}
	}
	if job.Mode == domain.KnowledgeJobInquiry && (len(finish.Changes) > 0 || finish.NoChange) {
		return nil, fmt.Errorf("%w: inquiry finish cannot contain curation changes", domain.ErrValidation)
	}
	if job.Mode == domain.KnowledgeJobCuration {
		if !finish.NoChange && len(finish.Changes) == 0 {
			return nil, fmt.Errorf("%w: curation finish requires revised_changes or no_change", domain.ErrValidation)
		}
		submission, err := s.persistKnowledgeCurationSubmissionLocked(ctx, job, finish)
		if err != nil {
			return nil, err
		}
		if submission != nil {
			result["submission_id"] = submission.ID
			if job.SubmissionID != "" && submission.ID != job.SubmissionID {
				result["origin_submission_id"] = job.SubmissionID
				result["curated_submission_id"] = submission.ID
			}
		}
	}
	switch gate.Status {
	case domain.KnowledgeCoverageConflict:
		job.Status = domain.KnowledgeJobConflict
	case domain.KnowledgeCoverageComplete:
		job.Status = domain.KnowledgeJobCompleted
	default:
		job.Status = domain.KnowledgeJobIncomplete
	}
	job.Result = result
	job.LastDecisionDigest = knowledgeDigest(mustJSONBytes(finish))
	job.LastError = strings.Join(finish.Gaps, "; ")
	job.NextActionAt = nil
	now := time.Now().UTC()
	job.UpdatedAt, job.FinishedAt = now, &now
	return result, nil
}

func (s *Service) persistKnowledgeCurationSubmissionLocked(ctx context.Context, job *domain.KnowledgeJob,
	finish *KnowledgeFinishDecision) (*domain.KnowledgeSubmission, error) {
	if finish == nil || job == nil {
		return nil, fmt.Errorf("%w: curation finish/job required", domain.ErrValidation)
	}
	if finish.NoChange {
		if job.SubmissionID == "" {
			return nil, fmt.Errorf("%w: no_change curation requires source submission", domain.ErrValidation)
		}
		origin, err := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, job.SubmissionID)
		if err != nil {
			return nil, err
		}
		targetStatus := domain.KnowledgeSubmissionMerged
		errorMessage := ""
		if job.Coverage.Status != domain.KnowledgeCoverageComplete {
			targetStatus = domain.KnowledgeSubmissionNeedsReview
			errorMessage = strings.Join(finish.Gaps, "; ")
			if errorMessage == "" {
				errorMessage = "curation finish is incomplete"
			}
		}
		if err := s.store.Knowledge().UpdateSubmissionStatus(ctx, origin.ID, targetStatus,
			nil, nil, truncateKnowledgeRunes(errorMessage, 4000), origin.Version); err != nil {
			return nil, err
		}
		return origin, nil
	}
	if err := s.validateKnowledgeCurationSources(ctx, job, finish.Changes); err != nil {
		return nil, err
	}
	if err := s.validateKnowledgeCurationItemScope(ctx, job, finish.Changes); err != nil {
		return nil, err
	}
	changes := append([]domain.KnowledgeChange(nil), finish.Changes...)
	request := domain.KnowledgeSubmitCandidate{WorkspaceID: job.WorkspaceID, AgentID: job.AgentProfileID,
		RunID: job.CurrentRunID, WorkItemID: job.WorkItemID,
		ClientKey: "knowledge-curation:" + job.ID + ":" + fmt.Sprint(job.TurnSeq),
		Changes:   changes, NoChange: finish.NoChange}
	if err := s.normalizeKnowledgeCandidate(ctx, &request); err != nil {
		return nil, err
	}
	submission := &domain.KnowledgeSubmission{ID: domain.NewID(domain.PrefixKnowledgeSubmission), WorkspaceID: job.WorkspaceID,
		AgentID: job.AgentProfileID, RunID: job.CurrentRunID, WorkItemID: job.WorkItemID,
		ClientKey: request.ClientKey, Request: request, Status: domain.KnowledgeSubmissionReceived,
		Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	_, result, err := s.store.Knowledge().SubmitCandidate(ctx, submission)
	if err != nil {
		return nil, err
	}
	// The preparation helper creates candidate items/versions/sources/relations
	// atomically and moves the curated receipt to needs_review. It is supplied
	// by the publication owner so publication policy remains one authority.
	prepared, err := s.prepareKnowledgeSubmissionLocked(ctx, result)
	if err != nil {
		return nil, err
	}
	if job.SubmissionID != "" {
		origin, originErr := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, job.SubmissionID)
		if originErr != nil {
			return nil, originErr
		}
		if err := s.store.Knowledge().UpdateSubmissionStatus(ctx, origin.ID, domain.KnowledgeSubmissionMerged,
			prepared.ResultItemIDs, prepared.ResultVersionIDs, "", origin.Version); err != nil {
			return nil, err
		}
	}
	return prepared, nil
}

func (s *Service) validateKnowledgeCurationSources(ctx context.Context, job *domain.KnowledgeJob, changes []domain.KnowledgeChange) error {
	if s == nil || job == nil {
		return fmt.Errorf("%w: curation job required", domain.ErrValidation)
	}
	if len(changes) == 0 {
		return fmt.Errorf("%w: curation changes require evidence read by the librarian", domain.ErrValidation)
	}
	// Build an evidence set from source rows that the requesting Agent can
	// actually read. An ID appearing in job JSON is only a reference; it does
	// not grant access by itself.
	readSources := make(map[string]domain.KnowledgeSource, len(job.EvidenceIDs))
	for _, id := range job.EvidenceIDs {
		if id == "" {
			continue
		}
		source, err := s.store.Knowledge().GetSource(ctx, job.WorkspaceID, job.RequestingAgentID, id)
		if err != nil {
			// A curation of a producer's private candidate may use the
			// producer's already-authorized scope, while the librarian still
			// remains the executing Agent. This fallback is limited to the
			// immutable origin receipt below.
			if job.SubmissionID == "" {
				return fmt.Errorf("%w: curation evidence %s is not readable in requester scope", domain.ErrNotFound, id)
			}
			origin, originErr := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, job.SubmissionID)
			if originErr != nil {
				return originErr
			}
			if originErr := validateKnowledgeCurationOrigin(job, origin); originErr != nil {
				return originErr
			}
			source, err = s.store.Knowledge().GetSource(ctx, job.WorkspaceID, origin.AgentID, id)
			if err != nil {
				return fmt.Errorf("%w: curation evidence %s is outside authorized scope", domain.ErrNotFound, id)
			}
		}
		if source == nil || source.WorkspaceID != job.WorkspaceID {
			return fmt.Errorf("%w: curation evidence %s is outside workspace", domain.ErrValidation, id)
		}
		readSources[id] = *source
	}
	// The original producer receipt is itself a complete, immutable evidence
	// seed. Keep it as a separate list because it has no persisted source ID
	// until publication preparation creates one.
	seeded := make([]domain.KnowledgeSourceInput, 0)
	if job.SubmissionID != "" {
		if origin, err := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, job.SubmissionID); err == nil {
			if originErr := validateKnowledgeCurationOrigin(job, origin); originErr != nil {
				return originErr
			}
			for _, originalChange := range origin.Request.Changes {
				seeded = append(seeded, originalChange.Sources...)
			}
		} else {
			return err
		}
	}
	for i, change := range changes {
		if len(change.Sources) == 0 {
			return fmt.Errorf("%w: curation change %d has no evidence source", domain.ErrValidation, i)
		}
		for _, source := range change.Sources {
			if sourceID := knowledgeSourceID(source); sourceID != "" {
				actual, ok := readSources[sourceID]
				if !ok {
					return fmt.Errorf("%w: curation change %d source_id %q was not read or is unauthorized", domain.ErrValidation, i, sourceID)
				}
				if err := validateKnowledgeSourceEvidence(source, &actual); err != nil {
					return fmt.Errorf("%w: curation change %d source_id %q: %v", domain.ErrValidation, i, sourceID, err)
				}
				continue
			}
			matched := false
			for _, actual := range readSources {
				if actual.Kind != source.Kind || actual.Ref != source.Ref {
					continue
				}
				if err := validateKnowledgeSourceEvidence(source, &actual); err == nil {
					matched = true
					break
				}
			}
			if !matched {
				for _, actual := range seeded {
					if actual.Kind != source.Kind || actual.Ref != source.Ref {
						continue
					}
					if err := validateKnowledgeSourceEvidence(source, &actual); err == nil {
						matched = true
						break
					}
				}
			}
			if !matched {
				return fmt.Errorf("%w: curation change %d source %q was not an authorized exact evidence match", domain.ErrValidation, i, source.Ref)
			}
		}
	}
	return nil
}

func validateKnowledgeCurationOrigin(job *domain.KnowledgeJob, origin *domain.KnowledgeSubmission) error {
	if job == nil || origin == nil || origin.WorkspaceID != job.WorkspaceID || origin.AgentID == "" ||
		origin.Request.WorkspaceID != job.WorkspaceID || origin.Request.AgentID != origin.AgentID {
		return fmt.Errorf("%w: curation origin submission identity is inconsistent", domain.ErrValidation)
	}
	return nil
}

func knowledgeSourceID(source domain.KnowledgeSourceInput) string {
	if source.Metadata == nil {
		return ""
	}
	id, _ := source.Metadata["source_id"].(string)
	return strings.TrimSpace(id)
}

func validateKnowledgeSourceEvidence(candidate domain.KnowledgeSourceInput, actual interface{}) error {
	var kind domain.KnowledgeSourceKind
	var ref, locator, digest, excerpt string
	switch source := actual.(type) {
	case *domain.KnowledgeSource:
		if source == nil {
			return fmt.Errorf("actual source is nil")
		}
		kind, ref, locator, digest, excerpt = source.Kind, source.Ref, source.Locator, source.Digest, source.Excerpt
	case *domain.KnowledgeSourceInput:
		if source == nil {
			return fmt.Errorf("actual source is nil")
		}
		kind, ref, locator, digest, excerpt = source.Kind, source.Ref, source.Locator, source.Digest, source.Excerpt
	case domain.KnowledgeSourceInput:
		kind, ref, locator, digest, excerpt = source.Kind, source.Ref, source.Locator, source.Digest, source.Excerpt
	default:
		return fmt.Errorf("unsupported actual source type")
	}
	if candidate.Kind != kind || candidate.Ref != ref {
		return fmt.Errorf("kind/ref identity mismatch")
	}
	if candidate.Locator != "" && candidate.Locator != locator {
		return fmt.Errorf("locator mismatch")
	}
	if candidate.Digest != "" && candidate.Digest != digest {
		return fmt.Errorf("digest mismatch")
	}
	if strings.TrimSpace(candidate.Excerpt) != "" && !knowledgeEvidenceSubstring(excerpt, candidate.Excerpt) {
		return fmt.Errorf("excerpt is not contained in the authorized source")
	}
	return nil
}

func knowledgeEvidenceSubstring(actual, candidate string) bool {
	normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	actual, candidate = normalize(actual), normalize(candidate)
	return candidate != "" && strings.Contains(actual, candidate)
}

func (s *Service) validateKnowledgeCurationItemScope(ctx context.Context, job *domain.KnowledgeJob, changes []domain.KnowledgeChange) error {
	if s == nil || job == nil {
		return fmt.Errorf("%w: curation scope requires service/job", domain.ErrValidation)
	}
	scopeAgentID := job.RequestingAgentID
	if job.SubmissionID != "" {
		if origin, err := s.store.Knowledge().GetSubmissionForWorkspace(ctx, job.WorkspaceID, job.SubmissionID); err == nil && origin.AgentID != "" {
			// Curation is executed by the librarian, but the candidate's
			// original producer defines the read scope. This preserves
			// private-item isolation while allowing an explicit owner to be
			// used later by the verified manager path.
			scopeAgentID = origin.AgentID
		}
	}
	for _, change := range changes {
		if change.ItemID == "" {
			continue
		}
		if _, err := s.store.Knowledge().GetItem(ctx, job.WorkspaceID, scopeAgentID, change.ItemID); err != nil {
			return fmt.Errorf("%w: curation item %s is outside the source request scope", domain.ErrNotFound, change.ItemID)
		}
	}
	return nil
}

func boundKnowledgeResult(job *domain.KnowledgeJob, result map[string]any) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	raw, _ := json.Marshal(result)
	job.Used.Bytes += int64(len(raw))
	if len(raw) > knowledgeJobMaxObservationRunes*4 {
		job.Observations = append(job.Observations, truncateKnowledgeRunes(string(raw), knowledgeJobMaxObservationRunes))
		job.Coverage.Truncated = true
	} else {
		job.Observations = append(job.Observations, string(raw))
	}
	if len(job.Observations) > 32 {
		job.Observations = job.Observations[len(job.Observations)-32:]
	}
	return result
}

func dedupeKnowledgeIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = appendUniqueString(result, strings.TrimSpace(id))
	}
	return result
}

func minKnowledgeInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func mustJSONBytes(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}
