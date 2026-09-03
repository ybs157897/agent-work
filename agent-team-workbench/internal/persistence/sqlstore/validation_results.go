package sqlstore

import (
	"context"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type ValidationResultRepo struct{ store *Store }

var _ application.ValidationResultRepo = (*ValidationResultRepo)(nil)

const validationResultCols = `id, goal_id, todo_id, work_item_id, source_run_id,
	criteria_digest, status, summary, produced_by, recorded_at, version, created_at`

func scanValidationResult(row interface{ Scan(...any) error }) (*domain.ValidationResult, error) {
	r := &domain.ValidationResult{}
	var recorded, created scanTime
	if err := row.Scan(&r.ID, &r.GoalID, &r.TodoID, &r.WorkItemID, &r.SourceRunID,
		&r.CriteriaDigest, &r.Status, &r.Summary, &r.ProducedBy, &recorded, &r.Version, &created); err != nil {
		return nil, err
	}
	r.RecordedAt, r.CreatedAt = mustTime(recorded), mustTime(created)
	return r, nil
}

func (r *ValidationResultRepo) Create(ctx context.Context, result *domain.ValidationResult) error {
	if result == nil {
		return fmt.Errorf("%w: validation result required", domain.ErrValidation)
	}
	if result.CreatedAt.IsZero() {
		result.CreatedAt = timeNow()
	}
	if result.RecordedAt.IsZero() {
		result.RecordedAt = result.CreatedAt
	}
	if result.Version == 0 {
		result.Version = 1
	}
	if err := result.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO governance_validation_results(`+validationResultCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		result.ID, result.GoalID, result.TodoID, result.WorkItemID, result.SourceRunID,
		result.CriteriaDigest, result.Status, result.Summary, result.ProducedBy,
		timeParam(result.RecordedAt), result.Version, timeParam(result.CreatedAt))
	return r.store.mapErr(err)
}

func (r *ValidationResultRepo) Get(ctx context.Context, id string) (*domain.ValidationResult, error) {
	result, err := scanValidationResult(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+validationResultCols+` FROM governance_validation_results WHERE id=?`, id))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return result, nil
}

func (r *ValidationResultRepo) GetBySourceRun(ctx context.Context, runID string) (*domain.ValidationResult, error) {
	result, err := scanValidationResult(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+validationResultCols+` FROM governance_validation_results WHERE source_run_id=?`, runID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return result, nil
}

func (r *ValidationResultRepo) ListByGoal(ctx context.Context, goalID string) ([]*domain.ValidationResult, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+validationResultCols+` FROM governance_validation_results WHERE goal_id=? ORDER BY recorded_at, id`, goalID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ValidationResult
	for rows.Next() {
		result, scanErr := scanValidationResult(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, result)
	}
	return out, rows.Err()
}
