package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// GetGoalForWorkItem resolves the root Goal for a Task or direct descendant;
// callers do not need to duplicate parent walking or scope checks.
func (s *Service) GetGoalForWorkItem(ctx context.Context, workItemID string) (*domain.Goal, error) {
	wi, err := s.store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	if !isTaskWorkItem(wi) {
		return nil, fmt.Errorf("%w: governance Goal is only available for Task records", domain.ErrValidation)
	}
	rootID, err := s.workItemTreeRootID(ctx, wi.ID)
	if err != nil {
		return nil, err
	}
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if goal.WorkspaceID != wi.WorkspaceID {
		return nil, domain.ErrNotFound
	}
	return goal, nil
}

// GetTurnReceipt returns a complete receipt and verifies every canonical
// digest before exposing it to HTTP/MCP callers.
func (s *Service) GetTurnReceipt(ctx context.Context, key domain.TurnKey) (*domain.TurnReceipt, error) {
	var receipt *domain.TurnReceipt
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		header, err := s.store.TurnReceipts().GetHeader(txctx, key)
		if err != nil {
			return err
		}
		if err := verifyReceiptHeader(header); err != nil {
			return err
		}
		phases, err := s.store.TurnReceipts().ListPhases(txctx, key)
		if err != nil {
			return err
		}
		receipt = &domain.TurnReceipt{Header: *header, Phases: make([]domain.TurnReceiptPhase, 0, len(phases))}
		for _, phase := range phases {
			if err := verifyReceiptPhase(phase); err != nil {
				return err
			}
			receipt.Phases = append(receipt.Phases, *phase)
		}
		if err := receipt.Validate(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return receipt, nil
}

func (s *Service) GetGoalQuota(ctx context.Context, goalID string) ([]*domain.QuotaReservation, error) {
	if _, err := s.store.Goals().Get(ctx, goalID); err != nil {
		return nil, err
	}
	return s.store.Quotas().ListByGoal(ctx, goalID)
}

type GoalQuotaSummary struct {
	GoalID         string
	Policies       []domain.QuotaPolicy
	Reservations   []*domain.QuotaReservation
	Unresolved     []*domain.QuotaSpendEntry
	Committed      map[domain.QuotaKind]int64
	ActiveReserved map[domain.QuotaKind]int64
	ActiveWorkers  int
}

// GetGoalQuotaSummary is the service-owned quota read model. HTTP/MCP callers
// must not recompute committed, reserved or unresolved amounts themselves.
func (s *Service) GetGoalQuotaSummary(ctx context.Context, goalID string) (*GoalQuotaSummary, error) {
	var summary *GoalQuotaSummary
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, goalID)
		if err != nil {
			return err
		}
		reservations, err := s.store.Quotas().ListByGoal(txctx, goalID)
		if err != nil {
			return err
		}
		summary = &GoalQuotaSummary{GoalID: goal.ID, Policies: append([]domain.QuotaPolicy(nil), goal.QuotaPolicies...),
			Reservations: reservations, Committed: map[domain.QuotaKind]int64{}, ActiveReserved: map[domain.QuotaKind]int64{}}
		for _, policy := range goal.QuotaPolicies {
			committed, err := s.store.Quotas().SumCommitted(txctx, goalID, policy.Kind)
			if err != nil {
				return err
			}
			active, err := s.store.Quotas().SumActiveReserved(txctx, goalID, policy.Kind)
			if err != nil {
				return err
			}
			summary.Committed[policy.Kind] = committed
			summary.ActiveReserved[policy.Kind] = active
			gaps, err := s.store.Quotas().ListUnresolved(txctx, goalID, policy.Kind)
			if err != nil {
				return err
			}
			summary.Unresolved = append(summary.Unresolved, gaps...)
		}
		summary.ActiveWorkers, err = s.store.Quotas().ActiveWorkerCount(txctx, goalID)
		return err
	})
	return summary, err
}

func (s *Service) GetTurnQuota(ctx context.Context, key domain.TurnKey) ([]*domain.QuotaReservation, []*domain.QuotaSpendEntry, error) {
	if err := key.Validate(); err != nil {
		return nil, nil, err
	}
	var reservations []*domain.QuotaReservation
	var spend []*domain.QuotaSpendEntry
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		if _, err := s.store.Goals().Get(txctx, key.GoalID); err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, key.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != key.GoalID {
			return fmt.Errorf("%w: Turn Todo does not belong to Goal", domain.ErrValidation)
		}
		header, err := s.store.TurnReceipts().GetHeader(txctx, key)
		if err != nil {
			return err
		}
		if err := verifyReceiptHeader(header); err != nil {
			return err
		}
		reservations = make([]*domain.QuotaReservation, 0, 8)
		for _, kind := range []domain.QuotaKind{
			domain.QuotaTurnCount, domain.QuotaActiveWorker, domain.QuotaInputTokensTotal,
			domain.QuotaInputUncachedTokens, domain.QuotaCacheReadTokens,
			domain.QuotaCacheWriteTokens, domain.QuotaOutputTokens, domain.QuotaCostMicroUSD,
		} {
			reservation, err := s.store.Quotas().Get(txctx, domain.QuotaReservationKey{TurnKey: key, Kind: kind})
			if errors.Is(err, domain.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			reservations = append(reservations, reservation)
		}
		spend, err = s.store.Quotas().ListSpendByTurn(txctx, key)
		return err
	})
	return reservations, spend, err
}

func (s *Service) GetGoalEvidence(ctx context.Context, goalID string) ([]domain.GovernanceEvidenceItem, error) {
	goal, err := s.store.Goals().Get(ctx, goalID)
	if err != nil {
		return nil, err
	}
	if projection, getErr := s.store.GovernanceProjections().Get(ctx, goalID); getErr == nil {
		if err := s.validateGovernanceProjectionCurrent(ctx, projection, goal); err != nil {
			return nil, err
		}
		return append([]domain.GovernanceEvidenceItem(nil), projection.EvidenceSummary...), nil
	} else if !errors.Is(getErr, domain.ErrNotFound) {
		return nil, getErr
	}
	return append([]domain.GovernanceEvidenceItem(nil), goal.CompletionEvidenceSummary...), nil
}

func (s *Service) validateGovernanceProjectionCurrent(ctx context.Context,
	projection *domain.GovernanceGoalProjection, goal *domain.Goal) error {
	if goal == nil {
		return fmt.Errorf("%w: governance projection Goal is required", domain.ErrValidation)
	}
	if err := validateGovernanceProjection(projection, goal.ID); err != nil {
		return err
	}
	_, latest, err := s.replayGoalEventsLocked(ctx, goal.WorkspaceID, goal.ID)
	if err != nil {
		return err
	}
	if latest > projection.SourceCursor.EventStreamSeq {
		return fmt.Errorf("%w: governance projection is stale and requires repair", domain.ErrStateConflict)
	}
	return nil
}

func validateGovernanceProjection(projection *domain.GovernanceGoalProjection, goalID string) error {
	if err := projection.Validate(); err != nil {
		return err
	}
	if projection.GoalID != goalID {
		return fmt.Errorf("%w: governance projection goal identity mismatch", domain.ErrValidation)
	}
	want, err := projectionDigest(projection)
	if err != nil {
		return err
	}
	if want != projection.Digest {
		return fmt.Errorf("%w: governance projection digest mismatch", domain.ErrValidation)
	}
	return nil
}

func (s *Service) GetProjectionRepairs(ctx context.Context, goalID string) ([]*domain.ProjectionRepair, error) {
	if _, err := s.store.Goals().Get(ctx, goalID); err != nil {
		return nil, err
	}
	return s.store.GovernanceProjections().ListRepairsByGoal(ctx, goalID)
}
