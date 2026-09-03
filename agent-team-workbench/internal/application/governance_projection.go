package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type ProjectionRepairResult struct {
	Repair     *domain.ProjectionRepair         `json:"repair"`
	Projection *domain.GovernanceGoalProjection `json:"projection"`
}

// GetGovernanceProjection returns only the derived read model. Missing rows do
// not trigger an implicit repair or create any governance entity.
func (s *Service) GetGovernanceProjection(ctx context.Context, goalID string) (*domain.GovernanceGoalProjection, error) {
	goal, err := s.store.Goals().Get(ctx, goalID)
	if err != nil {
		return nil, err
	}
	projection, err := s.store.GovernanceProjections().Get(ctx, goalID)
	if err != nil {
		return nil, err
	}
	if err := s.validateGovernanceProjectionCurrent(ctx, projection, goal); err != nil {
		return nil, err
	}
	return projection, nil
}

// RepairGoalProjection replays canonical Goal/Todo/Receipt/Event data into the
// derived projection. The repair checkpoint is durable and retryable; this
// method never creates a Goal/Todo/Receipt or mutates execution-plane state.
func (s *Service) RepairGoalProjection(ctx context.Context, goalID string,
	scope []domain.GovernanceProjectionScope, clientKey string) (*ProjectionRepairResult, error) {
	scope = normalizeProjectionScope(scope)
	if err := validateProjectionScope(scope); err != nil {
		return nil, err
	}
	lock := &s.governanceProjectionLocks[governancePlanLockIndex(goalID)]
	lock.Lock()
	defer lock.Unlock()

	repair, err := s.beginProjectionRepair(ctx, goalID, scope, clientKey)
	if err != nil {
		return nil, err
	}
	if repair.Status == domain.ProjectionRepairCompleted {
		projection, getErr := s.store.GovernanceProjections().Get(ctx, goalID)
		if getErr == nil {
			return &ProjectionRepairResult{Repair: repair, Projection: projection}, nil
		}
		if !errors.Is(getErr, domain.ErrNotFound) {
			return nil, getErr
		}
		// The derived row may have been lost independently of its completed
		// repair checkpoint. Reopen the same durable repair identity and rebuild.
		repair, err = s.beginProjectionRepair(ctx, goalID, scope, clientKey)
		if err != nil {
			return nil, err
		}
	}

	projection, events, receipts, err := s.buildGovernanceProjection(ctx, goalID)
	if err != nil {
		_ = s.failProjectionRepair(context.WithoutCancel(ctx), repair, err)
		return nil, err
	}
	if err := s.store.InTx(ctx, func(txctx context.Context) error {
		if err := s.store.GovernanceProjections().Upsert(txctx, projection); err != nil {
			return err
		}
		fresh, err := s.store.GovernanceProjections().GetRepair(txctx, repair.ID)
		if err != nil {
			return err
		}
		if fresh.Status == domain.ProjectionRepairCompleted {
			return nil
		}
		fresh.Status = domain.ProjectionRepairCompleted
		fresh.SourceCursor = projection.SourceCursor
		fresh.ReplayedEventCount = events
		fresh.ReplayedReceiptCount = receipts
		now := time.Now().UTC()
		fresh.CompletedAt = &now
		fresh.UpdatedAt = now
		fresh.Version++
		if err := s.store.GovernanceProjections().UpdateRepair(txctx, fresh, fresh.Version-1); err != nil {
			return err
		}
		if err := s.emitProjectionUpdated(txctx, projection, "repair"); err != nil {
			return err
		}
		return s.emitProjectionRepairStateChanged(txctx, fresh, string(repair.Status), string(fresh.Status))
	}); err != nil {
		_ = s.failProjectionRepair(context.WithoutCancel(ctx), repair, err)
		return nil, err
	}
	completed, err := s.store.GovernanceProjections().GetRepair(ctx, repair.ID)
	if err != nil {
		return nil, err
	}
	if s.notifier != nil {
		if goal, goalErr := s.store.Goals().Get(ctx, goalID); goalErr == nil {
			s.notifier.Notify(goal.WorkspaceID)
		}
	}
	return &ProjectionRepairResult{Repair: completed, Projection: projection}, nil
}

func normalizeProjectionScope(scope []domain.GovernanceProjectionScope) []domain.GovernanceProjectionScope {
	if len(scope) == 0 {
		return []domain.GovernanceProjectionScope{
			domain.ProjectionScopeGoalProgress,
			domain.ProjectionScopeTodoCurrentState,
			domain.ProjectionScopeReceiptTimeline,
			domain.ProjectionScopeEvidenceSummary,
			domain.ProjectionScopeNextActionCheckpoint,
		}
	}
	out := append([]domain.GovernanceProjectionScope(nil), scope...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func validateProjectionScope(scope []domain.GovernanceProjectionScope) error {
	if len(scope) != 5 {
		return fmt.Errorf("%w: projection repair currently requires the complete five-scope rebuild", domain.ErrValidation)
	}
	seen := map[domain.GovernanceProjectionScope]bool{}
	for _, item := range scope {
		if !item.Valid() || seen[item] {
			return fmt.Errorf("%w: invalid or duplicate projection scope %q", domain.ErrValidation, item)
		}
		seen[item] = true
	}
	return nil
}

func (s *Service) beginProjectionRepair(ctx context.Context, goalID string,
	scope []domain.GovernanceProjectionScope, clientKey string) (*domain.ProjectionRepair, error) {
	var repair *domain.ProjectionRepair
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		if _, err := s.store.Goals().Get(txctx, goalID); err != nil {
			return err
		}
		if clientKey != "" {
			existing, err := s.store.GovernanceProjections().GetRepairByClientKey(txctx, goalID, clientKey)
			if err == nil {
				if !reflect.DeepEqual(existing.Scope, scope) {
					return domain.ErrIdempotencyConflict
				}
				if existing.Status == domain.ProjectionRepairCompleted {
					projection, projectionErr := s.store.GovernanceProjections().Get(txctx, goalID)
					if projectionErr == nil {
						goal, goalErr := s.store.Goals().Get(txctx, goalID)
						if goalErr != nil {
							return goalErr
						}
						if s.validateGovernanceProjectionCurrent(txctx, projection, goal) == nil {
							repair = existing
							return nil
						}
					} else if errors.Is(projectionErr, domain.ErrNotFound) {
						// The completed checkpoint has lost its derived row; reopen it.
					}
				}
				fromStatus := existing.Status
				existing.Status = domain.ProjectionRepairRunning
				existing.ErrorCode, existing.ErrorMessage, existing.CompletedAt = "", "", nil
				existing.Version++
				existing.UpdatedAt = time.Now().UTC()
				if err := s.store.GovernanceProjections().UpdateRepair(txctx, existing, existing.Version-1); err != nil {
					return err
				}
				if fromStatus != existing.Status {
					if err := s.emitProjectionRepairStateChanged(txctx, existing,
						string(fromStatus), string(existing.Status)); err != nil {
						return err
					}
				}
				repair = existing
				return nil
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}
		now := time.Now().UTC()
		repair = &domain.ProjectionRepair{
			ID: domain.NewID(domain.PrefixProjectionRepair), GoalID: goalID,
			Status: domain.ProjectionRepairRunning, Scope: scope,
			ClientKey: clientKey, Version: 1, StartedAt: now,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.GovernanceProjections().CreateRepair(txctx, repair); err != nil {
			return err
		}
		return s.emitProjectionRepairStateChanged(txctx, repair,
			string(domain.ProjectionRepairPending), string(domain.ProjectionRepairRunning))
	})
	return repair, err
}

func (s *Service) failProjectionRepair(ctx context.Context, repair *domain.ProjectionRepair, cause error) error {
	if repair == nil || cause == nil {
		return nil
	}
	return s.store.InTx(ctx, func(txctx context.Context) error {
		fresh, err := s.store.GovernanceProjections().GetRepair(txctx, repair.ID)
		if err != nil {
			return err
		}
		if fresh.Status == domain.ProjectionRepairCompleted {
			return nil
		}
		fromStatus := fresh.Status
		if fromStatus == domain.ProjectionRepairFailed {
			return nil
		}
		fresh.Status = domain.ProjectionRepairFailed
		fresh.ErrorCode = "projection_repair_failed"
		fresh.ErrorMessage = truncateProjectionError(cause.Error())
		fresh.CompletedAt = nil
		fresh.Version++
		fresh.UpdatedAt = time.Now().UTC()
		if err := s.store.GovernanceProjections().UpdateRepair(txctx, fresh, fresh.Version-1); err != nil {
			return err
		}
		return s.emitProjectionRepairStateChanged(txctx, fresh, string(fromStatus), string(fresh.Status))
	})
}

func truncateProjectionError(message string) string {
	if len(message) <= 4000 {
		return message
	}
	return message[:4000]
}

func (s *Service) emitProjectionUpdated(ctx context.Context,
	projection *domain.GovernanceGoalProjection, cause string) error {
	if projection == nil {
		return fmt.Errorf("%w: projection update event requires projection", domain.ErrValidation)
	}
	goal, err := s.store.Goals().Get(ctx, projection.GoalID)
	if err != nil {
		return err
	}
	return s.emit(ctx, goal.WorkspaceID, domain.EventProjectionUpdated,
		domain.AggregateGovernanceProjection, projection.GoalID, projection.Version, nil,
		map[string]any{
			"goal_id": projection.GoalID, "digest": projection.Digest, "version": projection.Version,
			"event_stream_seq": projection.SourceCursor.EventStreamSeq,
			"through_turn_seq": projection.SourceCursor.ThroughTurnSeq, "cause": cause,
		})
}

func (s *Service) emitProjectionRepairStateChanged(ctx context.Context,
	repair *domain.ProjectionRepair, from, to string) error {
	if repair == nil {
		return fmt.Errorf("%w: projection repair event requires repair", domain.ErrValidation)
	}
	goal, err := s.store.Goals().Get(ctx, repair.GoalID)
	if err != nil {
		return err
	}
	data := map[string]any{
		"goal_id": repair.GoalID, "repair_id": repair.ID,
		"from_state": from, "to_state": to, "status": string(repair.Status),
		"event_stream_seq":       repair.SourceCursor.EventStreamSeq,
		"through_turn_seq":       repair.SourceCursor.ThroughTurnSeq,
		"replayed_event_count":   repair.ReplayedEventCount,
		"replayed_receipt_count": repair.ReplayedReceiptCount,
	}
	if repair.ErrorCode != "" {
		data["error_code"] = repair.ErrorCode
	}
	return s.emit(ctx, goal.WorkspaceID, domain.EventProjectionRepairChanged,
		domain.AggregateProjectionRepair, repair.ID, repair.Version, nil, data)
}

// buildGovernanceProjection reads only canonical authority and is safe to call
// inside a caller-owned transaction (normal phase-7 path) or its own read tx.
func (s *Service) buildGovernanceProjection(ctx context.Context, goalID string) (*domain.GovernanceGoalProjection, int, int, error) {
	var projection *domain.GovernanceGoalProjection
	var eventCount, receiptCount int
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		var err error
		projection, eventCount, receiptCount, err = s.buildGovernanceProjectionLocked(txctx, goalID)
		return err
	})
	return projection, eventCount, receiptCount, err
}

func (s *Service) buildGovernanceProjectionLocked(ctx context.Context, goalID string) (*domain.GovernanceGoalProjection, int, int, error) {
	var projection *domain.GovernanceGoalProjection
	goal, err := s.store.Goals().Get(ctx, goalID)
	if err != nil {
		return nil, 0, 0, err
	}
	todos, err := s.store.Todos().ListByGoal(ctx, goalID)
	if err != nil {
		return nil, 0, 0, err
	}
	progress := map[string]any{"goal_id": goal.ID, "status": string(goal.Status), "phase": goal.Phase,
		"todo_count": len(todos), "pending": int64(0), "claimed": int64(0), "running": int64(0),
		"waiting": int64(0), "completed": int64(0), "blocked": int64(0), "cancelled": int64(0)}
	for _, todo := range todos {
		if todo == nil {
			continue
		}
		key := string(todo.Status)
		progress[key] = progress[key].(int64) + 1
	}
	todoState := map[string]any{"goal_id": goal.ID, "todo_id": goal.CurrentTodoID, "status": "none", "version": int64(0)}
	if goal.CurrentTodoID != "" {
		current, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
		if err != nil {
			return nil, 0, 0, err
		}
		todoState = map[string]any{"goal_id": goal.ID, "todo_id": current.ID, "status": string(current.Status),
			"version": int64(current.Version), "last_turn_seq": current.LastTurnSeq, "claim_version": current.ClaimVersion}
	}

	heads, err := s.store.TurnReceipts().ListHeadersByGoal(ctx, goalID)
	if err != nil {
		return nil, 0, 0, err
	}
	timeline := make([]map[string]any, 0, len(heads)*2)
	allEvidence := append([]domain.GovernanceEvidenceItem(nil), goal.CompletionEvidenceSummary...)
	throughTurn := int64(0)
	receipts := 0
	for _, header := range heads {
		if header == nil {
			continue
		}
		if err := verifyReceiptHeader(header); err != nil {
			return nil, 0, 0, err
		}
		if header.TurnKey.TurnSeq > throughTurn {
			throughTurn = header.TurnKey.TurnSeq
		}
		receipts++
		timeline = append(timeline, map[string]any{"record_kind": "header", "goal_id": header.TurnKey.GoalID,
			"todo_id": header.TurnKey.TodoID, "turn_seq": header.TurnKey.TurnSeq,
			"digest": header.CanonicalDigest, "created_at": header.CreatedAt.UTC().Format(time.RFC3339Nano)})
		phases, err := s.store.TurnReceipts().ListPhases(ctx, header.TurnKey)
		if err != nil {
			return nil, 0, 0, err
		}
		for _, phase := range phases {
			if phase == nil {
				continue
			}
			if err := verifyReceiptPhase(phase); err != nil {
				return nil, 0, 0, err
			}
			// Phase 7 carries the digest produced from the stable phase-1..6
			// source prefix. Excluding it keeps normal projection and repair
			// deterministic instead of making the digest self-referential, but
			// its canonical digest is still verified above.
			if phase.PhaseSeq == 7 {
				continue
			}
			receipts++
			timeline = append(timeline, map[string]any{"record_kind": "phase", "goal_id": phase.TurnKey.GoalID,
				"todo_id": phase.TurnKey.TodoID, "turn_seq": phase.TurnKey.TurnSeq,
				"phase_seq": phase.PhaseSeq, "phase": string(phase.Phase),
				"digest": phase.CanonicalDigest, "plan_id": phase.PlanID,
				"run_ids":                append([]string(nil), phase.RunIDs...),
				"quota_reservation_keys": append([]string(nil), phase.QuotaReservationKeys...),
				"evidence":               append([]domain.GovernanceEvidenceItem(nil), phase.Evidence...),
				"created_at":             phase.CreatedAt.UTC().Format(time.RFC3339Nano)})
			allEvidence = append(allEvidence, phase.Evidence...)
		}
	}
	// EventRepo.Since is the canonical stream replay surface. Filter by the
	// aggregate identity; unrelated workspace events remain out of this Goal
	// projection but still advance the source cursor.
	events, latestSeq, err := s.replayGoalEventsLocked(ctx, goal.WorkspaceID, goalID)
	if err != nil {
		return nil, 0, 0, err
	}
	handoffs, err := s.store.Handoffs().ListByGoal(ctx, goalID)
	if err != nil {
		return nil, 0, 0, err
	}
	validationResults, err := s.store.ValidationResults().ListByGoal(ctx, goalID)
	if err != nil {
		return nil, 0, 0, err
	}
	evidence := dedupeEvidence(allEvidence)
	for _, result := range validationResults {
		if result == nil {
			continue
		}
		verification := domain.EvidenceVerificationFailed
		if result.Status == domain.ValidationResultPassed {
			verification = domain.EvidenceVerificationPassed
		}
		evidence = dedupeEvidence(append(evidence, domain.GovernanceEvidenceItem{
			SourceKind: domain.EvidenceSourceValidationResult, SourceID: result.ID,
			Verification: verification, Summary: result.Summary, RecordedAt: result.RecordedAt,
		}))
	}
	for _, handoff := range handoffs {
		if handoff != nil {
			evidence = dedupeEvidence(append(evidence, handoff.Evidence...))
		}
	}
	coordinator := map[string]any{}
	if state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, goal.RootWorkItemID); stateErr == nil {
		coordinator = map[string]any{"status": string(state.Status), "phase": state.Phase,
			"current_action": state.CurrentAction, "current_step": state.CurrentStep, "version": state.Version}
	} else if !errors.Is(stateErr, domain.ErrNotFound) {
		return nil, 0, 0, stateErr
	}
	next := map[string]any{"goal_status": string(goal.Status), "current_todo_id": goal.CurrentTodoID,
		"coordinator": coordinator}
	counters := map[string]int64{
		"receipt_headers": int64(len(heads)), "receipt_records": int64(receipts),
		"replayed_events": int64(events), "handoffs": int64(len(handoffs)),
		"validation_results": int64(len(validationResults)), "evidence_items": int64(len(evidence)),
	}
	for _, handoff := range handoffs {
		if handoff != nil && handoff.AcceptedAt != nil {
			if delta := handoff.AcceptedAt.Sub(handoff.CreatedAt).Milliseconds(); delta >= 0 {
				counters["handoff_latency_ms"] += delta
			}
		}
	}
	projection = &domain.GovernanceGoalProjection{
		GoalID: goal.ID, GoalProgress: progress, TodoCurrentState: todoState,
		ReceiptTimeline: timeline, EvidenceSummary: evidence, NextActionCheckpoint: next,
		Counters: counters, SourceCursor: domain.ProjectionCursor{EventStreamSeq: latestSeq, ThroughTurnSeq: throughTurn},
		Version: 1, UpdatedAt: time.Now().UTC(),
	}
	if existing, getErr := s.store.GovernanceProjections().Get(ctx, goalID); getErr == nil && existing != nil {
		projection.Version = existing.Version + 1
	}
	digest, err := projectionDigest(projection)
	if err != nil {
		return nil, 0, 0, err
	}
	projection.Digest = digest
	if err := projection.Validate(); err != nil {
		return nil, 0, 0, err
	}
	return projection, events, receipts, nil
}

func verifyReceiptHeader(header *domain.TurnReceiptHeader) error {
	want, err := ComputeTurnReceiptHeaderDigest(header)
	if err != nil {
		return err
	}
	if want != header.CanonicalDigest {
		return fmt.Errorf("%w: receipt header digest mismatch during projection replay", domain.ErrValidation)
	}
	return nil
}

func verifyReceiptPhase(phase *domain.TurnReceiptPhase) error {
	want, err := ComputeTurnReceiptPhaseDigest(phase)
	if err != nil {
		return err
	}
	if want != phase.CanonicalDigest {
		return fmt.Errorf("%w: receipt phase digest mismatch during projection replay", domain.ErrValidation)
	}
	return nil
}

func (s *Service) replayGoalEventsLocked(ctx context.Context, workspaceID, goalID string) (int, int64, error) {
	after := int64(0)
	relevant := 0
	latest := int64(0)
	for {
		events, err := s.store.Events().Since(ctx, workspaceID, after, 1000)
		if err != nil {
			return 0, 0, err
		}
		if len(events) == 0 {
			break
		}
		for _, event := range events {
			if event == nil {
				continue
			}
			if event.Type == domain.EventProjectionUpdated || event.Type == domain.EventProjectionRepairChanged {
				// These events describe the derived projection/checkpoint itself;
				// treating them as projection input would move the source cursor after
				// every write and make a repair digest self-referential.
				continue
			}
			if event.Type == domain.EventTurnReceiptAppended && event.Data != nil &&
				event.Data["record_kind"] == "phase" && integralProjectionValue(event.Data["phase_seq"]) == 7 {
				// The phase-7 event is the receipt of this projection itself;
				// including it would make the projection digest self-referential.
				continue
			}
			if event.StreamSeq > latest {
				latest = event.StreamSeq
			}
			if (event.AggregateType == domain.AggregateGoal && event.AggregateID == goalID) ||
				event.AggregateType == domain.AggregateTodo && event.Data != nil && event.Data["goal_id"] == goalID ||
				event.AggregateType == domain.AggregateHandoff && event.Data != nil && event.Data["goal_id"] == goalID ||
				event.AggregateType == domain.AggregateValidationResult && event.Data != nil && event.Data["goal_id"] == goalID {
				relevant++
			}
		}
		after = events[len(events)-1].StreamSeq
		if len(events) < 1000 {
			break
		}
	}
	return relevant, latest, nil
}

func integralProjectionValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func dedupeEvidence(items []domain.GovernanceEvidenceItem) []domain.GovernanceEvidenceItem {
	seen := map[string]bool{}
	out := make([]domain.GovernanceEvidenceItem, 0, len(items))
	for _, item := range items {
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%s", item.SourceKind, item.SourceID, item.Verification,
			item.RecordedAt.UTC().Format(time.RFC3339Nano))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func projectionDigest(p *domain.GovernanceGoalProjection) (string, error) {
	return canonicalGovernancePlanDigest(struct {
		GoalID               string                          `json:"goal_id"`
		GoalProgress         map[string]any                  `json:"goal_progress"`
		TodoCurrentState     map[string]any                  `json:"todo_current_state"`
		ReceiptTimeline      []map[string]any                `json:"receipt_timeline"`
		EvidenceSummary      []domain.GovernanceEvidenceItem `json:"evidence_summary"`
		NextActionCheckpoint map[string]any                  `json:"next_action_checkpoint"`
		Counters             map[string]int64                `json:"counters"`
		SourceCursor         domain.ProjectionCursor         `json:"source_cursor"`
	}{p.GoalID, p.GoalProgress, p.TodoCurrentState, p.ReceiptTimeline, p.EvidenceSummary,
		p.NextActionCheckpoint, p.Counters, p.SourceCursor})
}

// appendGovernanceProjectionPhaseLocked is the normal Turn phase-7 producer.
// It is called only after phase-6 settlement; repair paths intentionally never
// append a historical receipt phase.
func (s *Service) appendGovernanceProjectionPhaseLocked(ctx context.Context, key domain.TurnKey) error {
	if _, err := s.store.TurnReceipts().GetPhase(ctx, key, 7); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	projection, eventCount, receiptCount, err := s.buildGovernanceProjectionLocked(ctx, key.GoalID)
	if err != nil {
		return err
	}
	if err := s.store.GovernanceProjections().Upsert(ctx, projection); err != nil {
		return err
	}
	if err := s.emitProjectionUpdated(ctx, projection, "turn"); err != nil {
		return err
	}
	header := &domain.TurnReceiptHeader{TurnKey: key}
	return s.appendGovernancePlanPhase(ctx, header, 7, map[string]any{
		"status": "committed", "projection_goal_id": key.GoalID,
		"projection_digest": projection.Digest, "event_stream_seq": projection.SourceCursor.EventStreamSeq,
		"through_turn_seq":     projection.SourceCursor.ThroughTurnSeq,
		"replayed_event_count": eventCount, "replayed_receipt_count": receiptCount,
	}, "", nil)
}

func (s *Service) appendGovernanceProjectionPhaseIfReady(ctx context.Context, key domain.TurnKey) error {
	return s.store.InTx(ctx, func(txctx context.Context) error {
		if _, err := s.store.TurnReceipts().GetPhase(txctx, key, 7); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if _, err := s.store.TurnReceipts().GetPhase(txctx, key, 5); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		if _, err := s.store.TurnReceipts().GetPhase(txctx, key, 6); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		todo, err := s.store.Todos().Get(txctx, key.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != key.GoalID || todo.LastTurnSeq != key.TurnSeq ||
			todo.Status == domain.TodoRunning || todo.Status == domain.TodoClaimed || todo.Status == domain.TodoPending {
			return nil
		}
		return s.appendGovernanceProjectionPhaseLocked(txctx, key)
	})
}
