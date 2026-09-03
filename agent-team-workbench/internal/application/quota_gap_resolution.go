package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ReconcileQuotaGapParams describes one immutable adjudication of an already
// recorded unresolved spend. v1 deliberately accepts no waiver flag: the
// caller must present one existing passed/accepted GovernanceEvidenceItem and
// an explicitly reviewed additive amount.
type ReconcileQuotaGapParams struct {
	Target    domain.QuotaSpendKey
	Amount    int64
	Evidence  domain.GovernanceEvidenceItem
	ActorID   string
	Reason    string
	ClientKey string
}

// ReconcileQuotaGap records a reconciled additive adjustment without changing
// the original spend, Run canonical usage or reservation. Replay is resolved
// before reading mutable evidence sources so a retry remains safe after the
// source has changed; a concurrent unique-key loser is converted to the same
// exact replay.
func (s *Service) ReconcileQuotaGap(ctx context.Context, p ReconcileQuotaGapParams) (*domain.QuotaGapResolution, error) {
	var resolved *domain.QuotaGapResolution
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		if err := p.Target.Validate(); err != nil {
			return err
		}
		if p.Amount < 0 {
			return fmt.Errorf("%w: quota gap resolution amount must be >= 0", domain.ErrValidation)
		}
		evidenceDigest, err := domain.ComputeGovernanceEvidenceDigest(p.Evidence)
		if err != nil {
			return err
		}
		goal, err := s.store.Goals().Get(txctx, p.Target.TurnKey.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, p.Target.TurnKey.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID {
			return fmt.Errorf("%w: quota gap resolution Todo is outside Goal", domain.ErrValidation)
		}

		// Target identity is unique regardless of client key. This makes a
		// later command unable to add a second adjustment for the same gap.
		if existing, getErr := s.store.QuotaGapResolutions().GetByTarget(txctx, p.Target); getErr == nil {
			if quotaGapResolutionIntentMatchesRequest(existing, p, evidenceDigest) {
				resolved = existing
				return nil
			}
			return domain.ErrIdempotencyConflict
		} else if !errors.Is(getErr, domain.ErrNotFound) {
			return getErr
		}
		if p.ClientKey != "" {
			if existing, getErr := s.store.QuotaGapResolutions().GetByClientKey(txctx,
				p.Target.TurnKey.GoalID, p.Target.TurnKey.TodoID, p.ClientKey); getErr == nil {
				if quotaGapResolutionIntentMatchesRequest(existing, p, evidenceDigest) {
					resolved = existing
					return nil
				}
				return domain.ErrIdempotencyConflict
			} else if !errors.Is(getErr, domain.ErrNotFound) {
				return getErr
			}
		}

		spend, err := s.store.Quotas().GetSpend(txctx, p.Target)
		if err != nil {
			return err
		}
		if spend.Status != domain.QuotaSpendUnresolved {
			return fmt.Errorf("%w: quota gap target is not unresolved", domain.ErrStateConflict)
		}
		if err := s.validateGovernanceEvidenceReferenceTx(txctx, goal.ID, todo.ID, p.Evidence); err != nil {
			return fmt.Errorf("%w: quota gap reconciliation evidence is not current/authorized: %v", domain.ErrStateConflict, err)
		}
		resolution := &domain.QuotaGapResolution{
			ID:                   domain.NewID(domain.PrefixQuotaGapResolution),
			SchemaVersion:        domain.QuotaGapResolutionSchemaVersion,
			Target:               p.Target,
			OriginalUsageDigest:  spend.UsageDigest,
			OriginalPolicyDigest: spend.PolicyDigest,
			OriginalPriceDigest:  spend.PriceDigest,
			Status:               domain.QuotaGapResolutionReconciled,
			Amount:               p.Amount,
			Evidence:             p.Evidence,
			EvidenceDigest:       evidenceDigest,
			ActorKind:            domain.QuotaGapResolutionActorUser,
			ActorID:              p.ActorID,
			Reason:               p.Reason,
			ClientKey:            p.ClientKey,
			CreatedAt:            time.Now().UTC(),
		}
		if err := resolution.Seal(); err != nil {
			return err
		}
		created, err := s.store.QuotaGapResolutions().Create(txctx, resolution)
		if err != nil {
			return err
		}
		if !created {
			// The repository resolves a concurrent target/client-key race as an
			// idempotent no-op; return the durable winner, not a fresh object.
			winner, getErr := s.store.QuotaGapResolutions().GetByTarget(txctx, p.Target)
			if getErr != nil {
				return getErr
			}
			if !quotaGapResolutionIntentMatchesRequest(winner, p, evidenceDigest) {
				return domain.ErrIdempotencyConflict
			}
			resolved = winner
			return nil
		}
		eventData := map[string]any{
			"schema_version":         resolution.SchemaVersion,
			"resolution_id":          resolution.ID,
			"goal_id":                resolution.Target.TurnKey.GoalID,
			"todo_id":                resolution.Target.TurnKey.TodoID,
			"turn_seq":               resolution.Target.TurnKey.TurnSeq,
			"quota_kind":             resolution.Target.Kind,
			"run_id":                 resolution.Target.RunID,
			"original_usage_digest":  resolution.OriginalUsageDigest,
			"original_policy_digest": resolution.OriginalPolicyDigest,
			"status":                 resolution.Status,
			"amount":                 resolution.Amount,
			"evidence_digest":        resolution.EvidenceDigest,
			"canonical_digest":       resolution.CanonicalDigest,
			"actor_kind":             resolution.ActorKind,
			"actor_id":               resolution.ActorID,
			"reason":                 resolution.Reason,
		}
		if resolution.OriginalPriceDigest != "" {
			eventData["original_price_digest"] = resolution.OriginalPriceDigest
		}
		if err := s.emit(txctx, goal.WorkspaceID, domain.EventQuotaGapReconciled,
			domain.AggregateQuotaGapResolution, resolution.ID, 1, nil, eventData); err != nil {
			return err
		}
		resolved = resolution
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func quotaGapResolutionIntentMatchesRequest(existing *domain.QuotaGapResolution,
	p ReconcileQuotaGapParams, evidenceDigest string) bool {
	return existing != nil && existing.Target.Equal(p.Target) &&
		existing.SchemaVersion == domain.QuotaGapResolutionSchemaVersion &&
		existing.Status == domain.QuotaGapResolutionReconciled && existing.Amount == p.Amount &&
		existing.EvidenceDigest == evidenceDigest &&
		existing.ActorKind == domain.QuotaGapResolutionActorUser && existing.ActorID == p.ActorID &&
		existing.Reason == p.Reason && existing.ClientKey == p.ClientKey
}

// GetQuotaGapResolution reads and verifies one immutable reconciliation. It
// does not revalidate the historical evidence's current status: the outcome
// records what an authorized operator proved at reconciliation time.
func (s *Service) GetQuotaGapResolution(ctx context.Context, id string) (*domain.QuotaGapResolution, error) {
	var resolution *domain.QuotaGapResolution
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		var err error
		resolution, err = s.store.QuotaGapResolutions().Get(txctx, id)
		if err != nil {
			return err
		}
		goal, err := s.store.Goals().Get(txctx, resolution.Target.TurnKey.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, resolution.Target.TurnKey.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID {
			return fmt.Errorf("%w: quota gap resolution Todo is outside Goal", domain.ErrValidation)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resolution, nil
}

func (s *Service) ListQuotaGapResolutions(ctx context.Context, goalID string) ([]*domain.QuotaGapResolution, error) {
	if _, err := s.store.Goals().Get(ctx, goalID); err != nil {
		return nil, err
	}
	return s.store.QuotaGapResolutions().ListByGoal(ctx, goalID)
}
