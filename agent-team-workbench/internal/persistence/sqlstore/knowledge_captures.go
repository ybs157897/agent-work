package sqlstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const knowledgeRunCaptureCols = `id, workspace_id, run_id, created_at, processed_at,
	submission_id, last_error, next_attempt_at, version`

func scanKnowledgeRunCapture(row interface{ Scan(...any) error }) (*domain.KnowledgeRunCapture, error) {
	capture := &domain.KnowledgeRunCapture{}
	var created, processed, nextAttempt scanTime
	var submissionID *string
	if err := row.Scan(&capture.ID, &capture.WorkspaceID, &capture.RunID, &created,
		&processed, &submissionID, &capture.LastError, &nextAttempt, &capture.Version); err != nil {
		return nil, err
	}
	capture.CreatedAt = mustTime(created)
	capture.ProcessedAt = optTime(processed)
	capture.NextAttemptAt = optTime(nextAttempt)
	if submissionID != nil {
		capture.SubmissionID = *submissionID
	}
	return capture, capture.Validate()
}

func (r *KnowledgeRepo) EnqueueRunCapture(ctx context.Context, workspaceID, runID string) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(runID) == "" {
		return fmt.Errorf("%w: knowledge capture requires workspace and run", domain.ErrValidation)
	}
	var runWorkspace string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM execution_runs WHERE id=?`, runID).Scan(&runWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if runWorkspace != workspaceID {
		return domain.ErrNotFound
	}
	now := timeNow()
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_run_captures(id, workspace_id, run_id, created_at, version)
		 VALUES (?,?,?,?,1) ON CONFLICT(run_id) DO NOTHING`,
		domain.NewID(domain.PrefixKnowledgeRunCapture), workspaceID, runID, timeParam(now))
	if err != nil {
		return r.store.mapErr(err)
	}
	var existingWorkspace string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM knowledge_run_captures WHERE run_id=?`, runID).Scan(&existingWorkspace); err != nil {
		return r.store.mapErr(err)
	}
	if existingWorkspace != workspaceID {
		return domain.ErrNotFound
	}
	return nil
}

func (r *KnowledgeRepo) ListPendingRunCaptures(ctx context.Context, limit int) ([]*domain.KnowledgeRunCapture, error) {
	if limit <= 0 || limit > maxKnowledgeLimit {
		limit = defaultKnowledgeLimit
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeRunCaptureCols+` FROM knowledge_run_captures
		 WHERE processed_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at<=?)
		 ORDER BY created_at ASC, id ASC LIMIT ?`, timeParam(timeNow()), limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeRunCapture
	for rows.Next() {
		capture, scanErr := scanKnowledgeRunCapture(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, capture)
	}
	return out, rows.Err()
}

func (r *KnowledgeRepo) CompleteRunCapture(ctx context.Context, runID, submissionID string) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(submissionID) == "" {
		return fmt.Errorf("%w: knowledge capture completion requires run and submission", domain.ErrValidation)
	}
	var captureWorkspace string
	var processed *string
	var existingSubmission *string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id, processed_at, submission_id FROM knowledge_run_captures WHERE run_id=?`, runID).
		Scan(&captureWorkspace, &processed, &existingSubmission); err != nil {
		return r.store.mapErr(err)
	}
	if processed != nil {
		if existingSubmission != nil && *existingSubmission == submissionID {
			return nil
		}
		return domain.ErrIdempotencyConflict
	}
	var submissionRunID, submissionWorkspace string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id, run_id FROM knowledge_submissions WHERE id=?`, submissionID).
		Scan(&submissionWorkspace, &submissionRunID); err != nil {
		return r.store.mapErr(err)
	}
	if submissionWorkspace != captureWorkspace || submissionRunID != runID {
		return fmt.Errorf("%w: knowledge capture submission is not bound to the captured Run", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE knowledge_run_captures SET processed_at=?, submission_id=?, last_error='', next_attempt_at=NULL, version=version+1
		 WHERE run_id=? AND processed_at IS NULL`, timeParam(timeNow()), submissionID, runID)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT processed_at, submission_id FROM knowledge_run_captures WHERE run_id=?`, runID).
		Scan(&processed, &existingSubmission); err != nil {
		return r.store.mapErr(err)
	}
	if processed != nil {
		if existingSubmission != nil && *existingSubmission == submissionID {
			return nil
		}
		return domain.ErrIdempotencyConflict
	}
	return domain.ErrVersionConflict
}

func (r *KnowledgeRepo) RecordRunCaptureError(ctx context.Context, runID, message string) error {
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("%w: knowledge capture run required", domain.ErrValidation)
	}
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("%w: knowledge capture error message required", domain.ErrValidation)
	}
	if len(message) > 4000 {
		message = message[:4000]
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE knowledge_run_captures SET last_error=?, next_attempt_at=?, version=version+1
		 WHERE run_id=? AND processed_at IS NULL`, message, timeParam(timeNow().Add(30*time.Second)), runID)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		getErr := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT 1 FROM knowledge_run_captures WHERE run_id=?`, runID).Scan(new(int))
		if getErr != nil {
			return r.store.mapErr(getErr)
		}
	}
	return nil
}
