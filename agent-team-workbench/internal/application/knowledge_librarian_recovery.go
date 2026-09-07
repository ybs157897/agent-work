package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const knowledgeJobRecoveryPageSize = 200

// knowledgeJobDeadline is intentionally derived from CreatedAt. A restart
// must not reset a job's wall-clock budget by refreshing UpdatedAt or creating
// a continuation Run.
func knowledgeJobDeadline(job *domain.KnowledgeJob) time.Time {
	if job == nil || job.CreatedAt.IsZero() {
		return time.Time{}
	}
	budget := job.Budget.Normalize()
	return job.CreatedAt.Add(time.Duration(budget.MaxDurationSeconds) * time.Second)
}

func knowledgeJobExpired(job *domain.KnowledgeJob, now time.Time) bool {
	deadline := knowledgeJobDeadline(job)
	return !deadline.IsZero() && !now.Before(deadline)
}

// RecoverKnowledgeLibrarianJobs replays the application-owned edges that are
// not guaranteed to run in the same process as the terminal callback. It is
// safe to call repeatedly: job/action CAS and deterministic Run client keys
// make a losing recovery scan a no-op.
func (s *Service) RecoverKnowledgeLibrarianJobs(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, fmt.Errorf("%w: service/store required", domain.ErrValidation)
	}
	recoveryCtx := context.WithoutCancel(ctx)
	workspaceIDs, err := s.store.Workspaces().ListIDs(recoveryCtx)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	acted := 0
	for _, workspaceID := range workspaceIDs {
		afterID := ""
		for {
			jobs, listErr := s.store.KnowledgeJobs().ListRecoverable(recoveryCtx, workspaceID, afterID, knowledgeJobRecoveryPageSize)
			if listErr != nil {
				return acted, listErr
			}
			if len(jobs) == 0 {
				break
			}
			for _, job := range jobs {
				if job == nil {
					continue
				}
				changed, recoverErr := s.recoverKnowledgeJob(recoveryCtx, job, now)
				if recoverErr != nil {
					// A single malformed/stale job must not prevent unrelated
					// workspaces from recovering. The next scan retries transient
					// storage/provider failures.
					log.Printf("knowledge recovery: job %s: %v", job.ID, recoverErr)
					continue
				}
				if changed {
					acted++
				}
			}
			afterID = jobs[len(jobs)-1].ID
			if len(jobs) < knowledgeJobRecoveryPageSize {
				break
			}
		}
		pending, pendingErr := s.store.KnowledgeJobs().ListCancellationPending(recoveryCtx, workspaceID, knowledgeJobRecoveryPageSize)
		if pendingErr != nil {
			return acted, pendingErr
		}
		for _, job := range pending {
			if job == nil || job.CurrentRunID == "" {
				continue
			}
			changed, retryErr := s.retryKnowledgeCancellation(recoveryCtx, job)
			if retryErr != nil {
				log.Printf("knowledge recovery: pending cancel job %s: %v", job.ID, retryErr)
				continue
			}
			if changed {
				acted++
			}
		}
	}
	return acted, nil
}

func (s *Service) recoverKnowledgeJob(ctx context.Context, job *domain.KnowledgeJob, now time.Time) (bool, error) {
	if job == nil || job.Status.IsTerminal() {
		return false, nil
	}
	// The caller's source Run is immutable. Once that Run has ended, an
	// inquiry started from its bound CLI must not keep running in the
	// background; cancellation goes through the same Run control path as an
	// explicit user cancel, even if the source context is already gone.
	if job.Mode == domain.KnowledgeJobInquiry && job.SourceRunID != "" {
		if source, err := s.store.Runs().Get(ctx, job.SourceRunID); err == nil && source.Status.IsTerminal() {
			return s.finishKnowledgeJobFromRecovery(ctx, job.ID, domain.KnowledgeJobCancelled,
				"source Run 已终态，知识调查随调用方取消")
		} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return false, err
		}
	}
	if knowledgeJobExpired(job, now) {
		changed, err := s.finishKnowledgeJobFromRecovery(ctx, job.ID, domain.KnowledgeJobIncomplete,
			fmt.Sprintf("知识管理员作业超过 %d 秒墙钟预算", job.Budget.Normalize().MaxDurationSeconds))
		if err != nil || !changed {
			return changed, err
		}
		return true, nil
	}

	if job.CurrentRunID == "" {
		return false, nil
	}
	run, err := s.store.Runs().Get(ctx, job.CurrentRunID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) && job.Status == domain.KnowledgeJobRunning {
			// A missing current Run is unrecoverable without inventing a new
			// execution authority. Leave an explicit durable terminal result.
			return s.finishKnowledgeJobFromRecovery(ctx, job.ID, domain.KnowledgeJobIncomplete,
				"知识管理员当前 Run 不存在，无法安全恢复")
		}
		return false, err
	}
	if run.Status.IsTerminal() {
		advanced, advanceErr := s.maybeAdvanceKnowledgeLibrarian(ctx, run)
		return advanced, advanceErr
	}
	if job.Status == domain.KnowledgeJobWaitingRetry &&
		(job.NextActionAt == nil || !now.Before(*job.NextActionAt)) {
		_, resumeErr := s.ResumeKnowledgeJob(ctx, job.ID)
		if resumeErr != nil {
			return false, resumeErr
		}
		return true, nil
	}
	if run.Status == domain.RunQueued {
		if err := s.dispatchCommittedRun(ctx, run); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// finishKnowledgeJobFromRecovery closes a job first, then forwards cancel to
// its current Run. The two operations intentionally use separate transactions
// because ControlRun owns the Run state machine and may emit its own events.
func (s *Service) finishKnowledgeJobFromRecovery(ctx context.Context, jobID string,
	status domain.KnowledgeJobStatus, reason string) (bool, error) {
	if status != domain.KnowledgeJobCancelled && status != domain.KnowledgeJobIncomplete {
		return false, fmt.Errorf("%w: invalid recovery terminal status %q", domain.ErrValidation, status)
	}
	var runID string
	var changed bool
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		job, err := s.store.KnowledgeJobs().Get(ctx, jobID)
		if err != nil {
			return err
		}
		if job.Status.IsTerminal() {
			runID = job.CurrentRunID
			return nil
		}
		runID = job.CurrentRunID
		if err := s.finishKnowledgeJobLocked(ctx, job, status, reason); err != nil {
			if errors.Is(err, domain.ErrVersionConflict) {
				return nil // another terminal hook won the race
			}
			return err
		}
		changed = true
		return nil
	})
	if err != nil || !changed || runID == "" {
		return changed, err
	}
	run, getErr := s.store.Runs().Get(ctx, runID)
	if getErr != nil {
		if errors.Is(getErr, domain.ErrNotFound) {
			return true, nil
		}
		return true, getErr
	}
	if run.Status.IsTerminal() {
		return true, nil
	}
	if _, controlErr := s.ControlRun(ctx, runID, "cancel"); controlErr != nil && !errors.Is(controlErr, domain.ErrTerminalImmutable) {
		_ = s.markKnowledgeCancellationPending(ctx, jobID, runID, controlErr)
		return true, controlErr
	}
	return true, nil
}

func (s *Service) markKnowledgeCancellationPending(ctx context.Context, jobID, runID string, cancelErr error) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		job, err := s.store.KnowledgeJobs().Get(ctx, jobID)
		if err != nil {
			return err
		}
		if !job.Status.IsTerminal() || job.CurrentRunID != runID {
			return nil
		}
		job.LastError = fmt.Sprintf("cancel_pending:%s: %v", runID, cancelErr)
		job.UpdatedAt = time.Now().UTC()
		return s.store.KnowledgeJobs().Update(ctx, job, job.Version)
	})
}

func (s *Service) retryKnowledgeCancellation(ctx context.Context, job *domain.KnowledgeJob) (bool, error) {
	run, err := s.store.Runs().Get(ctx, job.CurrentRunID)
	if err != nil {
		return false, err
	}
	if run.Status.IsTerminal() {
		return s.clearKnowledgeCancellationPending(ctx, job)
	}
	if run.Status == domain.RunCancelling {
		// The transition itself is durable; a prior forwarding attempt may
		// have failed after entering the cancellation state. Do not retry an
		// illegal Cancelling -> Cancelling transition forever.
		return s.clearKnowledgeCancellationPending(ctx, job)
	}
	if _, err := s.ControlRun(ctx, run.ID, "cancel"); err != nil && !errors.Is(err, domain.ErrTerminalImmutable) {
		return false, err
	}
	return s.clearKnowledgeCancellationPending(ctx, job)
}

func (s *Service) clearKnowledgeCancellationPending(ctx context.Context, job *domain.KnowledgeJob) (bool, error) {
	if job == nil || !job.Status.IsTerminal() || !strings.HasPrefix(job.LastError, "cancel_pending:") {
		return false, nil
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.KnowledgeJobs().Get(ctx, job.ID)
		if err != nil {
			return err
		}
		if !fresh.Status.IsTerminal() || !strings.HasPrefix(fresh.LastError, "cancel_pending:") {
			return nil
		}
		fresh.LastError = ""
		fresh.UpdatedAt = time.Now().UTC()
		return s.store.KnowledgeJobs().Update(ctx, fresh, fresh.Version)
	})
	return err == nil, err
}

// RunKnowledgeLibrarianRecoveryLoop keeps UI-initiated jobs bounded even when
// no runner callback arrives. The loop owns no state beyond its ticker and
// exits with the supplied context.
func (s *Service) RunKnowledgeLibrarianRecoveryLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if _, err := s.DrainKnowledgeCaptures(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("knowledge capture initial drain: %v", err)
	}
	if _, err := s.RecoverKnowledgeLibrarianJobs(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("knowledge recovery initial scan: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.DrainKnowledgeCaptures(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("knowledge capture drain: %v", err)
			}
			if _, err := s.RecoverKnowledgeLibrarianJobs(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("knowledge recovery scan: %v", err)
			}
		}
	}
}
