package sqlstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// QuotaGapResolutionRepo stores immutable manual adjudications separately
// from the original quota spend ledger. The migration trigger requires the
// target spend to be unresolved and digest-identical at insert time.
type QuotaGapResolutionRepo struct{ store *Store }

var _ application.QuotaGapResolutionRepo = (*QuotaGapResolutionRepo)(nil)

const quotaGapResolutionCols = `id, schema_version, goal_id, todo_id, turn_seq, quota_kind, run_id,
	original_usage_digest, original_policy_digest, original_price_digest, status, amount,
	evidence, evidence_digest, canonical_digest, actor_kind, actor_id, reason, client_key, created_at`

func (r *QuotaGapResolutionRepo) scan(row interface{ Scan(...any) error }) (*domain.QuotaGapResolution, error) {
	resolution := &domain.QuotaGapResolution{}
	var priceDigest *string
	var evidenceJSON string
	var clientKey *string
	var created scanTime
	if err := row.Scan(&resolution.ID, &resolution.SchemaVersion,
		&resolution.Target.TurnKey.GoalID, &resolution.Target.TurnKey.TodoID,
		&resolution.Target.TurnKey.TurnSeq, &resolution.Target.Kind, &resolution.Target.RunID,
		&resolution.OriginalUsageDigest, &resolution.OriginalPolicyDigest, &priceDigest,
		&resolution.Status, &resolution.Amount, &evidenceJSON, &resolution.EvidenceDigest,
		&resolution.CanonicalDigest, &resolution.ActorKind, &resolution.ActorID,
		&resolution.Reason, &clientKey, &created); err != nil {
		return nil, err
	}
	if priceDigest != nil {
		resolution.OriginalPriceDigest = *priceDigest
	}
	if err := jsonInto(evidenceJSON, &resolution.Evidence); err != nil {
		return nil, err
	}
	if clientKey != nil {
		resolution.ClientKey = *clientKey
	}
	resolution.CreatedAt = mustTime(created)
	if err := resolution.Validate(); err != nil {
		return nil, err
	}
	return resolution, nil
}

func (r *QuotaGapResolutionRepo) Create(ctx context.Context, resolution *domain.QuotaGapResolution) (bool, error) {
	if resolution == nil {
		return false, fmt.Errorf("%w: quota gap resolution required", domain.ErrValidation)
	}
	if err := resolution.Validate(); err != nil {
		return false, err
	}
	if existing, err := r.GetByTarget(ctx, resolution.Target); err == nil {
		if sameQuotaGapResolutionIntent(existing, resolution) {
			return false, nil
		}
		return false, domain.ErrIdempotencyConflict
	} else if !isNotFound(err) {
		return false, err
	}
	if resolution.ClientKey != "" {
		if existing, err := r.GetByClientKey(ctx, resolution.Target.TurnKey.GoalID,
			resolution.Target.TurnKey.TodoID, resolution.ClientKey); err == nil {
			if sameQuotaGapResolutionIntent(existing, resolution) {
				return false, nil
			}
			return false, domain.ErrIdempotencyConflict
		} else if !isNotFound(err) {
			return false, err
		}
	}
	result, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO governance_quota_gap_resolutions(`+quotaGapResolutionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		resolution.ID, resolution.SchemaVersion, resolution.Target.TurnKey.GoalID,
		resolution.Target.TurnKey.TodoID, resolution.Target.TurnKey.TurnSeq, resolution.Target.Kind,
		resolution.Target.RunID, resolution.OriginalUsageDigest, resolution.OriginalPolicyDigest,
		nullString(resolution.OriginalPriceDigest), resolution.Status, resolution.Amount,
		jsonText(resolution.Evidence), resolution.EvidenceDigest, resolution.CanonicalDigest, resolution.ActorKind,
		resolution.ActorID, resolution.Reason, nullString(resolution.ClientKey), timeParam(resolution.CreatedAt))
	if err == nil {
		if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 1 {
			return true, nil
		}
		if existing, getErr := r.GetByTarget(ctx, resolution.Target); getErr == nil {
			if sameQuotaGapResolutionIntent(existing, resolution) {
				return false, nil
			}
			return false, domain.ErrIdempotencyConflict
		}
		return false, fmt.Errorf("%w: quota gap resolution insert did not persist", domain.ErrStateConflict)
	}
	if !sqliteUniqueViolation(err) {
		return false, r.store.mapErr(err)
	}
	// Resolve the race on either immutable target or client key by reading the
	// canonical winner. A different payload is a real idempotency conflict.
	if existing, getErr := r.GetByTarget(ctx, resolution.Target); getErr == nil {
		if sameQuotaGapResolutionIntent(existing, resolution) {
			return false, nil
		}
		return false, domain.ErrIdempotencyConflict
	}
	return false, domain.ErrIdempotencyConflict
}

func isNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }

func sameQuotaGapResolutionIntent(existing, incoming *domain.QuotaGapResolution) bool {
	return existing != nil && incoming != nil && existing.Target.Equal(incoming.Target) &&
		existing.SchemaVersion == incoming.SchemaVersion &&
		existing.OriginalUsageDigest == incoming.OriginalUsageDigest &&
		existing.OriginalPolicyDigest == incoming.OriginalPolicyDigest &&
		existing.OriginalPriceDigest == incoming.OriginalPriceDigest &&
		existing.Status == incoming.Status && existing.Amount == incoming.Amount &&
		existing.EvidenceDigest == incoming.EvidenceDigest && existing.ActorKind == incoming.ActorKind &&
		existing.ActorID == incoming.ActorID && existing.Reason == incoming.Reason &&
		existing.ClientKey == incoming.ClientKey
}

func (r *QuotaGapResolutionRepo) Get(ctx context.Context, id string) (*domain.QuotaGapResolution, error) {
	resolution, err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+quotaGapResolutionCols+` FROM governance_quota_gap_resolutions WHERE id=?`, id))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return resolution, nil
}

func (r *QuotaGapResolutionRepo) GetByTarget(ctx context.Context, key domain.QuotaSpendKey) (*domain.QuotaGapResolution, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	resolution, err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+quotaGapResolutionCols+` FROM governance_quota_gap_resolutions
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? AND quota_kind=? AND run_id=?`,
		key.TurnKey.GoalID, key.TurnKey.TodoID, key.TurnKey.TurnSeq, key.Kind, key.RunID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return resolution, nil
}

func (r *QuotaGapResolutionRepo) GetByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.QuotaGapResolution, error) {
	if goalID == "" || todoID == "" || clientKey == "" {
		return nil, fmt.Errorf("%w: quota gap resolution replay key requires Goal, Todo and client key", domain.ErrValidation)
	}
	resolution, err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+quotaGapResolutionCols+` FROM governance_quota_gap_resolutions
		 WHERE goal_id=? AND todo_id=? AND client_key=?`, goalID, todoID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return resolution, nil
}

func (r *QuotaGapResolutionRepo) ListByGoal(ctx context.Context, goalID string) ([]*domain.QuotaGapResolution, error) {
	if goalID == "" {
		return nil, fmt.Errorf("%w: goal id required", domain.ErrValidation)
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+quotaGapResolutionCols+` FROM governance_quota_gap_resolutions
		 WHERE goal_id=? ORDER BY created_at, id`, goalID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.QuotaGapResolution
	for rows.Next() {
		resolution, scanErr := r.scan(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, resolution)
	}
	return out, rows.Err()
}
