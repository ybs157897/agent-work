package application

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
)

// GetGovernanceMetrics recomputes governance counters from the canonical
// workspace event stream and authoritative Goal/receipt/quota/Run facts. It
// deliberately does not read or mutate a process-local accumulator, so a
// restart or a second control-plane instance returns the same result for the
// same database snapshot.
func (s *Service) GetGovernanceMetrics(ctx context.Context, workspaceID string) (*observability.GovernanceMetrics, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("%w: workspace id required", domain.ErrValidation)
	}
	var metrics observability.GovernanceMetrics
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		if _, err := s.store.Workspaces().Get(txctx, workspaceID); err != nil {
			return err
		}
		events := make([]*domain.CanonicalEvent, 0, 256)
		after := int64(0)
		for {
			batch, err := s.store.Events().Since(txctx, workspaceID, after, 1000)
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				break
			}
			for _, event := range batch {
				if event != nil {
					events = append(events, event)
				}
			}
			last := batch[len(batch)-1]
			if last == nil || last.StreamSeq <= after {
				return fmt.Errorf("%w: canonical event cursor did not advance", domain.ErrValidation)
			}
			after = last.StreamSeq
			if len(batch) < 1000 {
				break
			}
		}
		var aggregateErr error
		metrics, aggregateErr = observability.AggregateGovernanceMetrics(events)
		if aggregateErr != nil {
			return aggregateErr
		}
		metrics.WorkspaceID = workspaceID
		metrics.GoalSummaries, aggregateErr = s.recomputeGoalMetrics(txctx, workspaceID)
		if aggregateErr != nil {
			return aggregateErr
		}
		// Projection consistency is a current-state invariant rather than a
		// historical event count. Recompute it from Goals that actually exist;
		// legacy Task roots without a Goal are intentionally outside this metric.
		metrics.ProjectionDivergences, aggregateErr = s.countGovernanceConsistencyIssues(txctx, workspaceID)
		return aggregateErr
	})
	if err != nil {
		return nil, err
	}
	return &metrics, nil
}

func (s *Service) countGovernanceConsistencyIssues(ctx context.Context, workspaceID string) (int64, error) {
	goals, err := s.store.Goals().List(ctx, workspaceID)
	if err != nil {
		return 0, err
	}
	var count int64
	for _, goal := range goals {
		if goal == nil {
			continue
		}
		_, inconsistent, err := s.CheckGovernanceConsistency(ctx, goal.RootWorkItemID)
		if err != nil {
			return 0, err
		}
		if inconsistent {
			count++
		}
	}
	return count, nil
}

func (s *Service) recomputeGoalMetrics(ctx context.Context, workspaceID string) ([]observability.GoalMetrics, error) {
	goals, err := s.store.Goals().List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	sort.Slice(goals, func(i, j int) bool {
		if goals[i] == nil {
			return false
		}
		if goals[j] == nil {
			return true
		}
		return goals[i].ID < goals[j].ID
	})
	summaries := make([]observability.GoalMetrics, 0, len(goals))
	for _, goal := range goals {
		if goal == nil {
			continue
		}
		summary := observability.GoalMetrics{GoalID: goal.ID}
		headers, err := s.store.TurnReceipts().ListHeadersByGoal(ctx, goal.ID)
		if err != nil {
			return nil, err
		}
		seenRuns := map[string]struct{}{}
		for _, header := range headers {
			if header == nil {
				continue
			}
			if err := verifyReceiptHeader(header); err != nil {
				return nil, err
			}
			summary.TurnCount++
			phases, err := s.store.TurnReceipts().ListPhases(ctx, header.TurnKey)
			if err != nil {
				return nil, err
			}
			runIDs := make([]string, 0, len(phases))
			if header.GovernedSourceRunID != "" {
				runIDs = append(runIDs, header.GovernedSourceRunID)
			}
			for _, phase := range phases {
				if phase == nil {
					continue
				}
				if err := verifyReceiptPhase(phase); err != nil {
					return nil, err
				}
				if phase.PhaseSeq == 1 {
					if sourceID, ok := phase.Payload["source_run_id"].(string); ok && sourceID != "" {
						runIDs = append(runIDs, sourceID)
					}
				}
				runIDs = append(runIDs, phase.RunIDs...)
			}
			turnRuns, err := s.store.Runs().ListByGovernanceTurn(ctx, workspaceID,
				header.TurnKey.GoalID, header.TurnKey.TodoID, header.TurnKey.TurnSeq)
			if err != nil {
				return nil, err
			}
			for _, run := range turnRuns {
				if run != nil {
					runIDs = append(runIDs, run.ID)
				}
			}
			for _, runID := range runIDs {
				if runID == "" {
					continue
				}
				if _, seen := seenRuns[runID]; seen {
					continue
				}
				run, err := s.store.Runs().Get(ctx, runID)
				if err != nil {
					return nil, err
				}
				if run.WorkspaceID != workspaceID {
					return nil, fmt.Errorf("%w: governed Run is outside workspace", domain.ErrValidation)
				}
				seenRuns[runID] = struct{}{}
				summary.RunCount++
			}
			spend, err := s.store.Quotas().ListSpendByTurn(ctx, header.TurnKey)
			if err != nil {
				return nil, err
			}
			for _, entry := range spend {
				if entry == nil || entry.Status != domain.QuotaSpendCommitted {
					continue
				}
				if err := addGoalMetricSpend(&summary, entry.Key.Kind, entry.Amount); err != nil {
					return nil, err
				}
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func addGoalMetricSpend(summary *observability.GoalMetrics, kind domain.QuotaKind, amount int64) error {
	if summary == nil || amount < 0 {
		return fmt.Errorf("%w: invalid Goal metric spend amount", domain.ErrValidation)
	}
	add := func(current *int64) error {
		var err error
		*current, err = domain.CheckedAddNonNegative(*current, amount)
		return err
	}
	switch kind {
	case domain.QuotaInputTokensTotal:
		return add(&summary.InputTokensTotal)
	case domain.QuotaInputUncachedTokens:
		return add(&summary.InputUncachedTokens)
	case domain.QuotaCacheReadTokens:
		return add(&summary.CacheReadTokens)
	case domain.QuotaCacheWriteTokens:
		return add(&summary.CacheWriteTokens)
	case domain.QuotaOutputTokens:
		return add(&summary.OutputTokens)
	case domain.QuotaCostMicroUSD:
		return add(&summary.CostMicroUSD)
	default:
		return nil
	}
}
