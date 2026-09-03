package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/gowebpki/jcs"
	workbenchcontracts "github.com/ybs/agent-team-workbench/contracts"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

const governancePlanClaimTTL = 15 * time.Minute

const governancePlanRetryRunIDKey = "governance_plan_retry_run_id"

// governancePlanRetryableError marks a storage/transaction failure while the
// governed Plan is still being compiled. The admission Header and any
// reservation are deliberately left in place so the next recovery pass can
// resume the same Turn; the Coordinator must not convert this infrastructure
// failure into a semantic blocker.
type governancePlanRetryableError struct{ err error }

func (e *governancePlanRetryableError) Error() string {
	if e == nil || e.err == nil {
		return "governed Plan submission is retryable"
	}
	return "governed Plan submission is retryable: " + e.err.Error()
}

func (e *governancePlanRetryableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// SubmitGovernedTodoPlanDecision is the durable Todo -> existing Plan seam.
// It owns governance admission and receipt phases, but execution remains
// exclusively inside SubmitPlan. Each committed boundary is replayable; no
// SQL transaction is held across post-commit Runtime dispatch.
func (s *Service) SubmitGovernedTodoPlanDecision(ctx context.Context, run *domain.ExecutionRun,
	decision *domain.PlanDecisionV2, candidate PlanCandidateSource) (*domain.Plan, error) {
	if run == nil || decision == nil {
		return nil, fmt.Errorf("%w: governed Plan requires source Run and decision", domain.ErrValidation)
	}
	turnLock := &s.governancePlanLocks[governancePlanLockIndex(run.ID)]
	turnLock.Lock()
	defer turnLock.Unlock()
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, run.WorkItemID)
	if err != nil {
		return nil, err
	}
	if goal.Status != domain.GoalActive || goal.CurrentTodoID == "" {
		return nil, fmt.Errorf("%w: governed Plan requires an active Goal/current Todo", domain.ErrStateConflict)
	}
	todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		return nil, err
	}
	decisionDigest, err := planDecisionDigest(decision)
	if err != nil {
		return nil, err
	}
	header, err := s.ensureGovernancePlanAdmission(ctx, goal, todo, run, decision.SchemaVersion, decisionDigest)
	if err != nil {
		return nil, fmt.Errorf("governance Plan admission: %w", err)
	}
	if phase, phaseErr := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 1); phaseErr == nil {
		if phase.Payload["decision_digest"] != decisionDigest {
			return nil, fmt.Errorf("governance decision_decode replay: %w", domain.ErrIdempotencyConflict)
		}
	} else if !errors.Is(phaseErr, domain.ErrNotFound) {
		return nil, phaseErr
	}
	if phase, phaseErr := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 2); phaseErr == nil {
		if valid, _ := phase.Payload["valid"].(bool); !valid {
			if stateErr := s.transitionGovernanceTodoTurn(ctx, header.TurnKey, domain.TodoBlocked); stateErr != nil {
				return nil, stateErr
			}
			return nil, governancePlanValidationErrorFromPhase(phase)
		}
	} else if !errors.Is(phaseErr, domain.ErrNotFound) {
		return nil, phaseErr
	}
	if phase, phaseErr := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 3); phaseErr == nil {
		if status, _ := phase.Payload["status"].(string); status == "rejected" {
			return nil, governancePlanFailureFromWritebackPhase(phase)
		}
	} else if !errors.Is(phaseErr, domain.ErrNotFound) {
		return nil, phaseErr
	}
	planClientKey := governancePlanClientKey(header.TurnKey)
	if existing, err := s.store.Plans().GetByClientKey(ctx, goal.WorkspaceID, planClientKey); err != nil {
		return nil, err
	} else if existing != nil {
		if !governedPlanMatches(existing, goal.RootWorkItemID, run.AgentProfileID, run.ID,
			header.TurnKey, decision.SchemaVersion, workbenchcontracts.PlanDecisionV2SchemaDigest(), decisionDigest) {
			return nil, fmt.Errorf("governance Plan identity replay: %w", domain.ErrIdempotencyConflict)
		}
		if err := s.appendGovernancePlanWritebackPhase(ctx, header, existing); err != nil {
			return existing, fmt.Errorf("append replayed governance writeback phase: %w", err)
		}
		if err := s.appendGovernancePlanCommittedPhases(ctx, header, existing); err != nil {
			return existing, fmt.Errorf("append replayed governance Plan phases: %w", err)
		}
		if err := s.clearGovernancePlanSubmissionRetry(ctx, run.WorkItemID); err != nil {
			return existing, fmt.Errorf("clear replayed Plan submission recovery checkpoint: %w", err)
		}
		if err := s.transitionGovernanceTodoTurn(ctx, header.TurnKey, domain.TodoWaiting); err != nil {
			return existing, fmt.Errorf("settle replayed governance Todo: %w", err)
		}
		return existing, nil
	}
	turnKind := governanceTurnDecisionKind(run, decision)
	turnDecision, err := newGovernanceTurnDecision(header.TurnKey, turnKind,
		decision.Reason, decision.NextAction, decision.SchemaVersion, decision, header.CreatedAt)
	if err != nil {
		return nil, err
	}

	if err := s.appendGovernancePlanPhase(ctx, header, 1, map[string]any{
		"candidate_source": string(candidate), "decision_digest": decisionDigest,
		"schema_version": decision.SchemaVersion, "schema_digest": workbenchcontracts.PlanDecisionV2SchemaDigest(),
		"source_run_id": run.ID, "turn_decision": turnDecision,
	}, "", nil); err != nil {
		return nil, fmt.Errorf("append governance decision_decode phase: %w", err)
	}
	compiled, err := s.CompileTodoPlan(ctx, TodoToPlanCompileInput{
		TurnKey: header.TurnKey, OwnerAgentID: run.AgentProfileID, SourceRunID: run.ID,
		Decision: decision, SchemaDigest: workbenchcontracts.PlanDecisionV2SchemaDigest(),
	})
	if err != nil {
		existing, lookupErr := s.store.Plans().GetByClientKey(ctx, goal.WorkspaceID, planClientKey)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if existing != nil && governedPlanMatches(existing, goal.RootWorkItemID, run.AgentProfileID, run.ID,
			header.TurnKey, decision.SchemaVersion, workbenchcontracts.PlanDecisionV2SchemaDigest(), decisionDigest) {
			if phaseErr := s.appendGovernancePlanWritebackPhase(ctx, header, existing); phaseErr != nil {
				return existing, fmt.Errorf("append concurrently committed governance writeback phase: %w", phaseErr)
			}
			if phaseErr := s.appendGovernancePlanCommittedPhases(ctx, header, existing); phaseErr != nil {
				return existing, fmt.Errorf("append concurrently committed governance Plan phases: %w", phaseErr)
			}
			if checkpointErr := s.clearGovernancePlanSubmissionRetry(ctx, run.WorkItemID); checkpointErr != nil {
				return existing, fmt.Errorf("clear concurrently committed Plan recovery checkpoint: %w", checkpointErr)
			}
			if stateErr := s.transitionGovernanceTodoTurn(ctx, header.TurnKey, domain.TodoWaiting); stateErr != nil {
				return existing, stateErr
			}
			return existing, nil
		}
		if !governancePlanSubmissionFailurePermanent(err) {
			retryErr := &governancePlanRetryableError{err: err}
			if checkpointErr := s.markGovernancePlanSubmissionRetry(ctx, run, retryErr); checkpointErr != nil {
				return nil, &governancePlanRetryableError{err: errors.Join(retryErr,
					fmt.Errorf("persist Plan submission recovery checkpoint: %w", checkpointErr))}
			}
			return nil, retryErr
		}
		validated, phaseErr := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 2)
		if phaseErr == nil {
			if validated.Payload["valid"] == true {
				if validated.Payload["decision_digest"] != decisionDigest {
					return nil, domain.ErrIdempotencyConflict
				}
				if governanceCompileAuthorityFailure(err) {
					current, getErr := s.store.Todos().Get(ctx, header.TurnKey.TodoID)
					if getErr != nil {
						return nil, getErr
					}
					if current.Status == domain.TodoRunning {
						if stateErr := s.transitionGovernanceTodoTurn(ctx, header.TurnKey, domain.TodoBlocked); stateErr != nil {
							return nil, stateErr
						}
					}
					return nil, fmt.Errorf("fresh authority rejected previously validated governance turn: %w", err)
				}
				return nil, fmt.Errorf("%w: validated governance turn awaits retryable Plan commit", domain.ErrVersionConflict)
			} else {
				if stateErr := s.transitionGovernanceTodoTurn(ctx, header.TurnKey, domain.TodoBlocked); stateErr != nil {
					return nil, stateErr
				}
				return nil, governancePlanValidationErrorFromPhase(validated)
			}
		} else {
			validation := governancePlanValidationPayload(err)
			if appendErr := s.appendGovernancePlanPhase(ctx, header, 2, validation, "", nil); appendErr != nil {
				return nil, appendErr
			}
			if stateErr := s.transitionGovernanceTodoTurn(ctx, header.TurnKey, domain.TodoBlocked); stateErr != nil {
				return nil, stateErr
			}
			return nil, fmt.Errorf("compile governed Todo Plan: %w", err)
		}
	}
	compiled.Audit.Candidate = candidate
	if compiled.DecisionDigest != decisionDigest {
		return nil, fmt.Errorf("compiled governance decision digest: %w", domain.ErrIdempotencyConflict)
	}
	if err := s.appendGovernancePlanPhase(ctx, header, 2, map[string]any{
		"valid": true, "authority": "passed", "decision_digest": compiled.DecisionDigest,
	}, "", nil); err != nil {
		return nil, fmt.Errorf("append governance validation phase: %w", err)
	}
	plan, submitErr := s.SubmitPlan(ctx, goal.WorkspaceID, SubmitPlanParams{
		WorkItemID: compiled.WorkItemID, AgentProfileID: compiled.AgentProfileID,
		SourceRunID: compiled.SourceRunID, Steps: compiled.Steps, Guardrails: compiled.Guardrails,
		DecisionAudit: compiled.Audit,
		Governance: &PlanGovernanceInput{
			ClientKey: compiled.PlanClientKey, TurnKey: compiled.TurnKey,
			SchemaVersion: compiled.Audit.SchemaVersion, SchemaDigest: compiled.SchemaDigest,
			DecisionDigest: compiled.DecisionDigest,
		},
	})
	if plan == nil && errors.Is(submitErr, domain.ErrIdempotencyConflict) {
		existing, lookupErr := s.store.Plans().GetByClientKey(ctx, goal.WorkspaceID, compiled.PlanClientKey)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if !governedPlanMatches(existing, compiled.WorkItemID, compiled.AgentProfileID, compiled.SourceRunID,
			compiled.TurnKey, compiled.Audit.SchemaVersion, compiled.SchemaDigest, compiled.DecisionDigest) {
			return nil, fmt.Errorf("governance Plan concurrent replay: %w", domain.ErrIdempotencyConflict)
		}
		plan = existing
		submitErr = nil
	}
	if plan == nil {
		if submitErr == nil {
			return nil, fmt.Errorf("%w: governed Plan submission returned no Plan", domain.ErrStateConflict)
		}
		if !governancePlanSubmissionFailurePermanent(submitErr) {
			retryErr := &governancePlanRetryableError{err: submitErr}
			if checkpointErr := s.markGovernancePlanSubmissionRetry(ctx, run, retryErr); checkpointErr != nil {
				return nil, &governancePlanRetryableError{err: errors.Join(retryErr,
					fmt.Errorf("persist Plan submission recovery checkpoint: %w", checkpointErr))}
			}
			return nil, retryErr
		}
		decisionErr := coordinatorPlanError(submitErr)
		if compensationErr := s.settleGovernancePlanSubmissionFailure(ctx, header, decisionErr); compensationErr != nil {
			if !governancePlanSubmissionFailurePermanent(compensationErr) {
				return nil, &governancePlanRetryableError{err: fmt.Errorf(
					"Plan failure compensation pending: %w", compensationErr)}
			}
			return nil, fmt.Errorf("Plan failure compensation rejected: %w", compensationErr)
		}
		return nil, decisionErr
	}
	if err := s.appendGovernancePlanWritebackPhase(ctx, header, plan); err != nil {
		return plan, fmt.Errorf("append governance durable_writeback phase: %w", err)
	}
	if phaseErr := s.appendGovernancePlanCommittedPhases(ctx, header, plan); phaseErr != nil {
		return plan, fmt.Errorf("append committed governance Plan phases: %w", phaseErr)
	}
	if err := s.clearGovernancePlanSubmissionRetry(ctx, run.WorkItemID); err != nil {
		return plan, fmt.Errorf("clear Plan submission recovery checkpoint: %w", err)
	}
	if stateErr := s.transitionGovernanceTodoTurn(ctx, header.TurnKey, domain.TodoWaiting); stateErr != nil {
		return plan, stateErr
	}
	return plan, submitErr
}

func governanceCompileAuthorityFailure(err error) bool {
	return governancePlanSubmissionFailurePermanent(err)
}

// governancePlanSubmissionFailurePermanent distinguishes a decision/authority
// rejection from an infrastructure failure. Only the former may close the
// admitted Todo and release its usage reservation; raw database/transaction
// errors must remain a recoverable checkpoint.
func governancePlanSubmissionFailurePermanent(err error) bool {
	var decisionErr *PlanDecisionError
	return errors.As(err, &decisionErr) || errors.Is(err, domain.ErrStateConflict) ||
		errors.Is(err, domain.ErrValidation) || errors.Is(err, domain.ErrWorkspaceContextMismatch) ||
		errors.Is(err, domain.ErrCapabilityMissing) || errors.Is(err, domain.ErrNotFound)
}

// markGovernancePlanSubmissionRetry keeps a terminal Coordinator source Run as
// the replay owner when a validated Plan cannot be persisted because storage is
// temporarily unavailable. The terminal hook checks this marker and will not
// interpret the missing Plan as a successful delivery.
func (s *Service) markGovernancePlanSubmissionRetry(ctx context.Context,
	run *domain.ExecutionRun, cause error) error {
	if run == nil {
		return fmt.Errorf("%w: Plan submission retry checkpoint requires source Run", domain.ErrValidation)
	}
	return s.store.InTx(ctx, func(txctx context.Context) error {
		state, err := s.store.TaskCoordinators().GetStateForWorkItem(txctx, run.WorkItemID)
		if err != nil {
			return err
		}
		if state.CurrentRunID != run.ID {
			return fmt.Errorf("%w: Plan submission retry source Run no longer owns Coordinator state", domain.ErrStateConflict)
		}
		if state.Data == nil {
			state.Data = map[string]any{}
		}
		if existing, _ := state.Data[governancePlanRetryRunIDKey].(string); existing == run.ID {
			return nil
		}
		expected := state.Version
		message := truncateGovernancePlanRetryError(cause)
		state.Data[governancePlanRetryRunIDKey] = run.ID
		state.Data["governance_plan_retry_error"] = message
		state.Phase = "plan_commit"
		state.CurrentAction = "重放已验证的治理 Plan"
		state.LastError = message
		state.NextActionAt = nil
		if err := s.store.TaskCoordinators().UpdateState(txctx, state, expected); err != nil {
			return err
		}
		state.Version = expected + 1
		return s.appendCoordinatorEvent(txctx, state, state.RootWorkItemID,
			domain.EventCoordinatorRetryScheduled, "治理 Plan 持久化失败，等待重放",
			run.ID, run.AgentProfileID, state.Attempt, state.LastError, nil,
			map[string]any{"stage": "plan_commit", "retry_of": run.ID,
				"failure_code": "governed_plan_storage", "failure_message": state.LastError,
				"retryable": true, "next_action": "重放已验证的治理 Plan"})
	})
}

func truncateGovernancePlanRetryError(cause error) string {
	if cause == nil {
		return "治理 Plan 持久化失败，等待重放"
	}
	message := cause.Error()
	if len(message) > 4000 {
		return message[:4000]
	}
	return message
}

func (s *Service) clearGovernancePlanSubmissionRetry(ctx context.Context, workItemID string) error {
	if workItemID == "" {
		return nil
	}
	return s.store.InTx(ctx, func(txctx context.Context) error {
		state, err := s.store.TaskCoordinators().GetStateForWorkItem(txctx, workItemID)
		if err != nil {
			return err
		}
		if state.Data == nil {
			return nil
		}
		if _, ok := state.Data[governancePlanRetryRunIDKey]; !ok {
			return nil
		}
		expected := state.Version
		delete(state.Data, governancePlanRetryRunIDKey)
		delete(state.Data, "governance_plan_retry_error")
		return s.store.TaskCoordinators().UpdateState(txctx, state, expected)
	})
}

func governancePlanSubmissionRetryPending(state *domain.TaskCoordinatorState, runID string) bool {
	if state == nil || runID == "" || state.Data == nil {
		return false
	}
	pending, _ := state.Data[governancePlanRetryRunIDKey].(string)
	return pending == runID
}

func governancePlanFailureFromWritebackPhase(phase *domain.TurnReceiptPhase) error {
	if phase == nil {
		return fmt.Errorf("%w: rejected governed Plan writeback phase is missing", domain.ErrValidation)
	}
	code, _ := phase.Payload["error_code"].(string)
	governanceCode := domain.GovernanceErrorCode(code)
	if !governanceCode.Valid() {
		governanceCode = domain.GovernanceErrorPlanSemanticValidation
	}
	path, _ := phase.Payload["path"].(string)
	if path == "" {
		path = "/"
	}
	message, _ := phase.Payload["message"].(string)
	if message == "" {
		message = "governed Plan submission was rejected"
	}
	return &PlanDecisionError{Code: governanceCode, Path: path, Message: message, Cause: domain.ErrValidation}
}

// appendGovernancePlanWritebackPhase records the durable boundary between a
// validated decision and a committed Plan. A pre-existing phase is accepted
// only when its immutable lineage matches the Plan; this also repairs older
// checkpoints which used the same fields before the status marker was added.
func (s *Service) appendGovernancePlanWritebackPhase(ctx context.Context,
	header *domain.TurnReceiptHeader, plan *domain.Plan) error {
	if header == nil || plan == nil {
		return fmt.Errorf("%w: governed Plan writeback requires Header and Plan", domain.ErrValidation)
	}
	if existing, err := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 3); err == nil {
		if status, _ := existing.Payload["status"].(string); status == "rejected" {
			return governancePlanFailureFromWritebackPhase(existing)
		}
		clientKey, _ := existing.Payload["plan_client_key"].(string)
		sourceRunID, _ := existing.Payload["source_run_id"].(string)
		decisionDigest, _ := existing.Payload["decision_digest"].(string)
		if clientKey != plan.ClientKey || sourceRunID != plan.SourceRunID ||
			decisionDigest != plan.DecisionDigest {
			return fmt.Errorf("%w: governance durable_writeback phase lineage differs from Plan", domain.ErrIdempotencyConflict)
		}
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return s.appendGovernancePlanPhase(ctx, header, 3, map[string]any{
		"status": "committed", "plan_client_key": plan.ClientKey,
		"source_run_id": plan.SourceRunID, "decision_digest": plan.DecisionDigest,
	}, "", nil)
}

// settleGovernancePlanSubmissionFailure closes the admitted Turn only after a
// permanent Plan/Run creation rejection. Phase3 carries the rejection and the
// Todo/quota changes share one transaction, so a crash cannot expose a blocked
// Todo with an active reservation or a rejection without its receipt evidence.
func (s *Service) settleGovernancePlanSubmissionFailure(ctx context.Context,
	header *domain.TurnReceiptHeader, decisionErr *PlanDecisionError) error {
	if header == nil || decisionErr == nil {
		return fmt.Errorf("%w: Plan failure compensation requires Header and decision error", domain.ErrValidation)
	}
	key := header.TurnKey
	quotaLock := &s.governanceQuotaLocks[governancePlanLockIndex(
		fmt.Sprintf("%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq))]
	quotaLock.Lock()
	defer quotaLock.Unlock()
	return s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, key.GoalID)
		if err != nil {
			return err
		}
		if existing, err := s.store.Plans().GetByClientKey(txctx, goal.WorkspaceID, governancePlanClientKey(key)); err != nil {
			return err
		} else if existing != nil {
			return fmt.Errorf("%w: Plan already exists while compensating a rejected Turn", domain.ErrStateConflict)
		}
		if goal.CurrentTodoID != key.TodoID {
			return fmt.Errorf("%w: rejected Turn is no longer the Goal current Todo", domain.ErrStateConflict)
		}
		todo, err := s.store.Todos().Get(txctx, key.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID || todo.LastTurnSeq != key.TurnSeq {
			return fmt.Errorf("%w: rejected Turn Todo lineage is no longer current", domain.ErrStateConflict)
		}
		writebackRejected := false
		if phase, phaseErr := s.store.TurnReceipts().GetPhase(txctx, key, 3); phaseErr == nil {
			if status, _ := phase.Payload["status"].(string); status == "rejected" {
				writebackRejected = true
			} else {
				return fmt.Errorf("%w: durable_writeback phase already committed", domain.ErrStateConflict)
			}
		} else if !errors.Is(phaseErr, domain.ErrNotFound) {
			return phaseErr
		}
		if todo.Status != domain.TodoBlocked {
			if todo.Status != domain.TodoRunning {
				return fmt.Errorf("%w: rejected Turn Todo status %s cannot be blocked", domain.ErrStateConflict, todo.Status)
			}
			from := todo.Status
			expected := todo.Version
			if err := todo.Transition(domain.TodoBlocked, time.Now().UTC()); err != nil {
				return err
			}
			if err := s.store.Todos().Update(txctx, todo, expected); err != nil {
				return err
			}
			if err := s.emitTodoStateChanged(txctx, goal.WorkspaceID, todo, from); err != nil {
				return err
			}
		}
		reservationKeys, err := s.settleGovernancePlanAbortQuotaLocked(txctx, key, goal)
		if err != nil {
			return err
		}
		if !writebackRejected {
			if err := s.appendGovernancePlanPhase(txctx, header, 3, map[string]any{
				"status": "rejected", "error_code": string(decisionErr.Code),
				"path": decisionErr.Path, "message": decisionErr.Message,
				"source_run_id":          header.GovernedSourceRunID,
				"decision_digest":        header.DecisionDigest,
				"quota_settled":          true,
				"quota_reservation_keys": append([]string(nil), reservationKeys...),
			}, "", nil); err != nil {
				return err
			}
		}
		return nil
	})
}

// settleGovernancePlanAbortQuotaLocked is the narrow compensation path for a
// permanent Plan submission rejection. It settles only the source Coordinator
// Run, closes every usage reservation, and deliberately does not append phase6:
// phase4/5 require a real Plan and must not be fabricated for an aborted turn.
// The caller owns the surrounding transaction and the per-Turn quota lock.
func (s *Service) settleGovernancePlanAbortQuotaLocked(ctx context.Context,
	key domain.TurnKey, goal *domain.Goal) ([]string, error) {
	if goal == nil {
		return nil, fmt.Errorf("%w: Plan abort quota settlement requires Goal", domain.ErrValidation)
	}
	reservations := make([]*domain.QuotaReservation, 0, len(usageQuotaKinds))
	reservationKeys := make([]string, 0, len(usageQuotaKinds))
	for _, kind := range usageQuotaKinds {
		reservation, err := s.store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: key, Kind: kind})
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		reservations = append(reservations, reservation)
		reservationKeys = append(reservationKeys, reservation.Key.String())
	}
	if len(reservations) == 0 {
		return reservationKeys, nil
	}
	phase1, err := s.store.TurnReceipts().GetPhase(ctx, key, 1)
	if err != nil {
		return nil, err
	}
	sourceRunID, _ := phase1.Payload["source_run_id"].(string)
	if sourceRunID == "" {
		return nil, fmt.Errorf("%w: Plan abort quota settlement requires source Run", domain.ErrValidation)
	}
	sourceRun, err := s.store.Runs().Get(ctx, sourceRunID)
	if err != nil {
		return nil, err
	}
	if !sourceRun.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: Plan abort quota settlement source Run is not terminal", domain.ErrStateConflict)
	}
	if _, err := s.canonicalizeRunUsageLocked(ctx, sourceRun.ID, true); err != nil {
		return nil, err
	}
	sourceRun, err = s.store.Runs().Get(ctx, sourceRun.ID)
	if err != nil {
		return nil, err
	}
	if sourceRun.CanonicalUsage == nil {
		return nil, fmt.Errorf("%w: Plan abort quota settlement source usage is unavailable", domain.ErrStateConflict)
	}
	spend, err := s.store.Quotas().ListSpendByTurn(ctx, key)
	if err != nil {
		return nil, err
	}
	for _, reservation := range reservations {
		if reservation.Status != domain.QuotaReservationReserved {
			continue
		}
		spendKey := domain.QuotaSpendKey{TurnKey: key, Kind: reservation.Key.Kind, RunID: sourceRun.ID}
		if _, getErr := s.store.Quotas().GetSpend(ctx, spendKey); getErr == nil {
			continue
		} else if !errors.Is(getErr, domain.ErrNotFound) {
			return nil, getErr
		}
		var settled bool
		spend, settled, err = s.appendTurnRunSpendLocked(ctx, goal.WorkspaceID, key, sourceRun, reservation, spend)
		if err != nil {
			return nil, err
		}
		if !settled {
			return nil, fmt.Errorf("%w: Plan abort quota settlement cannot prove %s spend", domain.ErrStateConflict, reservation.Key.Kind)
		}
	}
	now := time.Now().UTC()
	for _, reservation := range reservations {
		if reservation.Status != domain.QuotaReservationReserved {
			continue
		}
		committed := committedSpendTotal(spend, reservation.Key.Kind)
		if committed > reservation.ReservedAmount {
			return nil, fmt.Errorf("%w: Plan abort quota settlement exceeds %s reservation", domain.ErrStateConflict, reservation.Key.Kind)
		}
		expected := reservation.Version
		reservation.CommittedAmount = committed
		reservation.ReleasedAmount = reservation.ReservedAmount - committed
		target := domain.QuotaReservationReleased
		if committed > 0 {
			target = domain.QuotaReservationCommitted
		}
		if err := reservation.Transition(target, now); err != nil {
			return nil, err
		}
		if target == domain.QuotaReservationCommitted {
			if err := s.store.Quotas().Commit(ctx, reservation, expected); err != nil {
				return nil, err
			}
		} else if err := s.store.Quotas().Release(ctx, reservation, expected); err != nil {
			return nil, err
		}
		if err := s.emitQuotaReservationChanged(ctx, goal.WorkspaceID, reservation, "plan_submission_rejected"); err != nil {
			return nil, err
		}
	}
	return reservationKeys, nil
}

func (s *Service) ensureGovernancePlanAdmission(ctx context.Context, goal *domain.Goal, todo *domain.Todo,
	run *domain.ExecutionRun, schemaVersion, decisionDigest string) (*domain.TurnReceiptHeader, error) {
	clientKey := "plan-decision:" + run.ID
	var admitted *domain.TurnReceiptHeader
	created := false
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		// The durable source-run checkpoint is the primary replay identity. The
		// admission client key remains only as a migration fallback for headers
		// created before 0030; new governed headers always carry the explicit
		// source/plan/decision tuple and replay never derives identity from a
		// formatted client key.
		existing, lookupErr := s.store.TurnReceipts().GetHeaderBySourceRun(txctx, run.ID)
		if errors.Is(lookupErr, domain.ErrNotFound) {
			existing, lookupErr = s.store.TurnReceipts().GetHeaderByClientKey(txctx, goal.ID, todo.ID, clientKey)
		}
		if lookupErr == nil {
			if existing.GovernedSourceRunID != "" &&
				(existing.GovernedSourceRunID != run.ID ||
					existing.TurnKey.GoalID != goal.ID || existing.TurnKey.TodoID != todo.ID ||
					existing.PlanClientKey != governancePlanClientKey(existing.TurnKey) ||
					existing.DecisionDigest != decisionDigest) {
				return domain.ErrIdempotencyConflict
			}
			if _, quotaErr := s.ensureExistingTurnCountReservationLocked(txctx, goal, existing); quotaErr != nil {
				return quotaErr
			}
			freshGoal, getErr := s.store.Goals().Get(txctx, goal.ID)
			if getErr != nil {
				return getErr
			}
			// admission 重放同样冻结 usage reservation：补齐升级/崩溃窗口
			//（有 reservation 时 get-or-create 幂等复用冻结值）。
			if usageErr := s.ensureUsageQuotaReservationsLocked(txctx, freshGoal, existing.TurnKey); usageErr != nil {
				return usageErr
			}
			if settledPlan, planErr := s.store.Plans().GetByClientKey(txctx, goal.WorkspaceID,
				governancePlanClientKey(existing.TurnKey)); planErr == nil && settledPlan != nil {
				admitted = existing
				return nil
			} else if planErr != nil {
				return planErr
			}
			if validation, phaseErr := s.store.TurnReceipts().GetPhase(txctx, existing.TurnKey, 2); phaseErr == nil {
				if valid, _ := validation.Payload["valid"].(bool); !valid {
					admitted = existing
					return nil
				}
			} else if !errors.Is(phaseErr, domain.ErrNotFound) {
				return phaseErr
			}
			if writeback, phaseErr := s.store.TurnReceipts().GetPhase(txctx, existing.TurnKey, 3); phaseErr == nil {
				if status, _ := writeback.Payload["status"].(string); status == "rejected" {
					// A permanent Plan rejection already closed this Turn. Let the
					// caller replay the durable failure without trying to re-claim a
					// blocked Todo or reopening a settled reservation.
					admitted = existing
					return nil
				}
			} else if !errors.Is(phaseErr, domain.ErrNotFound) {
				return phaseErr
			}
			freshTodo, getErr := s.store.Todos().Get(txctx, todo.ID)
			if getErr != nil {
				return getErr
			}
			if isDelegatedCoordinatorRun(run) {
				if renewErr := s.renewDelegatedCoordinatorClaimForRun(txctx, run); renewErr != nil {
					return renewErr
				}
				freshTodo, getErr = s.store.Todos().Get(txctx, todo.ID)
				if getErr != nil {
					return getErr
				}
			}
			if freshGoal.Status != domain.GoalActive || freshGoal.CurrentTodoID != freshTodo.ID ||
				freshTodo.GoalID != freshGoal.ID || freshTodo.LastTurnSeq != existing.TurnKey.TurnSeq ||
				freshTodo.Claim == nil || freshTodo.Claim.OwnerAgentID != run.AgentProfileID {
				return domain.ErrStateConflict
			}
			switch freshTodo.Status {
			case domain.TodoRunning:
				if !freshTodo.Claim.ExpiresAt.After(time.Now().UTC()) {
					return domain.ErrStateConflict
				}
			case domain.TodoClaimed:
				from := freshTodo.Status
				freshTodo, getErr = s.store.Todos().ResumeAdmitted(txctx, freshTodo.ID, run.AgentProfileID,
					existing.TurnKey.TurnSeq, time.Now().UTC(), freshTodo.Version)
				if getErr != nil {
					return getErr
				}
				if getErr := s.emitTodoStateChanged(txctx, freshGoal.WorkspaceID, freshTodo, from); getErr != nil {
					return getErr
				}
				created = true // state resumed; notify only, Header remains immutable
			default:
				return fmt.Errorf("%w: admitted Todo replay cannot resume status %s", domain.ErrStateConflict, freshTodo.Status)
			}
			admitted = existing
			return nil
		} else if !errors.Is(lookupErr, domain.ErrNotFound) {
			return lookupErr
		}
		freshGoal, err := s.store.Goals().Get(txctx, goal.ID)
		if err != nil {
			return err
		}
		fresh, err := s.store.Todos().Get(txctx, todo.ID)
		if err != nil {
			return err
		}
		if isDelegatedCoordinatorRun(run) {
			if renewErr := s.renewDelegatedCoordinatorClaimForRun(txctx, run); renewErr != nil {
				return renewErr
			}
			fresh, err = s.store.Todos().Get(txctx, todo.ID)
			if err != nil {
				return err
			}
		}
		if freshGoal.Status != domain.GoalActive || fresh.GoalID != freshGoal.ID || freshGoal.CurrentTodoID != fresh.ID {
			return domain.ErrStateConflict
		}
		turnQuotaDecision, err := s.ShouldRunLocked(txctx, ShouldRunRequest{
			GoalID: freshGoal.ID, Kind: domain.QuotaTurnCount, Amount: 1,
		})
		if err != nil {
			return err
		}
		if turnQuotaDecision.Enabled && !turnQuotaDecision.Allowed {
			return quotaDeniedError(turnQuotaDecision)
		}
		inputDigest, err := ComputeTodoPlanInputSnapshotDigest(freshGoal, fresh, run)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		switch fresh.Status {
		case domain.TodoPending, domain.TodoWaiting, domain.TodoBlocked:
			from := fresh.Status
			fresh, err = s.store.Todos().Claim(txctx, fresh.ID, run.AgentProfileID,
				now, now.Add(governancePlanClaimTTL), fresh.Version)
			if err != nil {
				return err
			}
			if from != fresh.Status {
				if err := s.emitTodoStateChanged(txctx, freshGoal.WorkspaceID, fresh, from); err != nil {
					return err
				}
			}
			if err := s.emitTodoClaimChanged(txctx, freshGoal.WorkspaceID, fresh, "claimed",
				run.AgentProfileID, &fresh.Claim.ExpiresAt); err != nil {
				return err
			}
		case domain.TodoClaimed:
			if fresh.Claim == nil || fresh.Claim.OwnerAgentID != run.AgentProfileID || !fresh.Claim.ExpiresAt.After(now) {
				return domain.ErrStateConflict
			}
		case domain.TodoRunning:
			return fmt.Errorf("%w: current Todo already owns another admitted turn", domain.ErrStateConflict)
		default:
			return fmt.Errorf("%w: Todo status %s cannot admit a Plan turn", domain.ErrStateConflict, fresh.Status)
		}
		attempt := coordinatorAttemptValue(coordinatorContextOf(run)["attempt"])
		if attempt < 1 {
			attempt = 1
		}
		header := &domain.TurnReceiptHeader{
			TurnKey: domain.TurnKey{GoalID: freshGoal.ID, TodoID: fresh.ID, TurnSeq: fresh.LastTurnSeq + 1},
			Attempt: attempt, SchemaVersion: schemaVersion, InputSnapshotDigest: inputDigest,
			AdmissionClientKey: clientKey, CanonicalDigest: emptySHA256Digest, CreatedAt: now,
		}
		header.GovernedSourceRunID = run.ID
		header.PlanClientKey = governancePlanClientKey(header.TurnKey)
		header.DecisionDigest = decisionDigest
		digest, err := ComputeTurnReceiptHeaderDigest(header)
		if err != nil {
			return err
		}
		header.CanonicalDigest = digest
		from := fresh.Status
		admitted, err = s.store.TurnReceipts().Admit(txctx, header, run.AgentProfileID, fresh.Version)
		if err != nil {
			return err
		}
		after, err := s.store.Todos().Get(txctx, fresh.ID)
		if err != nil {
			return err
		}
		if err := s.emitTodoStateChanged(txctx, freshGoal.WorkspaceID, after, from); err != nil {
			return err
		}
		if err := s.emitTurnReceiptAppended(txctx, freshGoal.WorkspaceID, after.Version, admitted, nil); err != nil {
			return err
		}
		if _, err := s.ensureTurnCountReservationLocked(txctx, freshGoal, admitted, turnQuotaDecision); err != nil {
			return err
		}
		// usage 政策的 reservation 与 Header 同事务冻结（跨 Turn 不超订；
		// 事务回滚则 reservation 与 Header 一起不存在）。
		if err := s.ensureUsageQuotaReservationsLocked(txctx, freshGoal, admitted.TurnKey); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if created {
		s.notifier.Notify(goal.WorkspaceID)
	}
	return admitted, nil
}

func governancePlanLockIndex(identity string) uint8 {
	var hash uint32 = 2166136261
	for index := 0; index < len(identity); index++ {
		hash ^= uint32(identity[index])
		hash *= 16777619
	}
	return uint8(hash % 64)
}

func ComputeTodoPlanInputSnapshotDigest(goal *domain.Goal, todo *domain.Todo, run *domain.ExecutionRun) (string, error) {
	if goal == nil || todo == nil || run == nil {
		return "", fmt.Errorf("%w: Goal, Todo and source Run are required for input snapshot digest", domain.ErrValidation)
	}
	payload := struct {
		GoalID            string               `json:"goal_id"`
		GoalObjective     string               `json:"goal_objective"`
		GoalAcceptance    []string             `json:"goal_acceptance"`
		TodoID            string               `json:"todo_id"`
		Instruction       string               `json:"instruction"`
		Acceptance        []string             `json:"acceptance"`
		DecisionScope     domain.DecisionScope `json:"decision_scope"`
		SourceRunID       string               `json:"source_run_id"`
		ContextSnapshotID string               `json:"context_snapshot_id"`
	}{
		GoalID: goal.ID, GoalObjective: goal.Objective,
		GoalAcceptance: append([]string{}, goal.AcceptanceContract...),
		TodoID:         todo.ID, Instruction: todo.Instruction,
		Acceptance: append([]string{}, todo.Acceptance...), DecisionScope: todo.DecisionScope,
		SourceRunID: run.ID, ContextSnapshotID: run.ContextSnapshotID,
	}
	return canonicalGovernancePlanDigest(payload)
}

func planDecisionDigest(decision *domain.PlanDecisionV2) (string, error) {
	return canonicalGovernancePlanDigest(decision)
}

func canonicalGovernancePlanDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func governancePlanClientKey(key domain.TurnKey) string {
	return fmt.Sprintf("governance:%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq)
}

func governedPlanMatches(plan *domain.Plan, workItemID, agentID, sourceRunID string, key domain.TurnKey,
	schemaVersion, schemaDigest, decisionDigest string) bool {
	return plan != nil && plan.WorkItemID == workItemID && plan.AgentProfileID == agentID &&
		plan.SourceRunID == sourceRunID && plan.GovernanceTurnKey != nil && plan.GovernanceTurnKey.Equal(key) &&
		plan.ClientKey == governancePlanClientKey(key) && plan.DecisionSchemaVersion == schemaVersion &&
		plan.DecisionSchemaDigest == schemaDigest && plan.DecisionDigest == decisionDigest
}

func governancePlanValidationPayload(err error) map[string]any {
	payload := map[string]any{"valid": false, "error_code": string(domain.GovernanceErrorPlanSemanticValidation),
		"path": "/", "message": err.Error()}
	var decisionErr *PlanDecisionError
	if errors.As(err, &decisionErr) {
		payload["error_code"] = string(decisionErr.Code)
		payload["path"] = decisionErr.Path
		payload["message"] = decisionErr.Message
	}
	return payload
}

func governancePlanValidationErrorFromPhase(phase *domain.TurnReceiptPhase) error {
	if phase == nil {
		return fmt.Errorf("%w: validation phase required", domain.ErrValidation)
	}
	code, _ := phase.Payload["error_code"].(string)
	path, _ := phase.Payload["path"].(string)
	message, _ := phase.Payload["message"].(string)
	governanceCode := domain.GovernanceErrorCode(code)
	if !governanceCode.Valid() {
		governanceCode = domain.GovernanceErrorPlanSemanticValidation
	}
	if path == "" {
		path = "/"
	}
	if message == "" {
		message = "governance Plan validation failed"
	}
	return &PlanDecisionError{Code: governanceCode, Path: path, Message: message, Cause: domain.ErrValidation}
}

func (s *Service) transitionGovernanceTodoTurn(ctx context.Context, key domain.TurnKey, to domain.TodoStatus) error {
	changed := false
	workspaceID := ""
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		todo, err := s.store.Todos().Get(txctx, key.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != key.GoalID || todo.LastTurnSeq < key.TurnSeq {
			return fmt.Errorf("%w: Todo turn settlement identity mismatch", domain.ErrStateConflict)
		}
		if todo.LastTurnSeq > key.TurnSeq {
			return nil // a newer admitted turn owns the current Todo projection
		}
		if todo.Status == to {
			return nil
		}
		if todo.Status != domain.TodoRunning {
			return fmt.Errorf("%w: Todo status %s cannot settle turn to %s", domain.ErrStateConflict, todo.Status, to)
		}
		goal, err := s.store.Goals().Get(txctx, key.GoalID)
		if err != nil {
			return err
		}
		from := todo.Status
		expected := todo.Version
		if err := todo.Transition(to, time.Now().UTC()); err != nil {
			return err
		}
		if err := s.store.Todos().Update(txctx, todo, expected); err != nil {
			return err
		}
		if err := s.emitTodoStateChanged(txctx, goal.WorkspaceID, todo, from); err != nil {
			return err
		}
		workspaceID = goal.WorkspaceID
		changed = true
		return nil
	})
	if err != nil {
		return err
	}
	if to != domain.TodoRunning {
		// validation/authority 失败的 blocked 路径同样要关闭本 Turn 的 usage
		// reservation（尽力而为：失败留给下一恢复面重放）。Todo 收口是关闭性
		// 触发源：allowAbsentClose=true 允许在此合成缺证据 Run 的 absent evidence。
		if sweepErr := s.settleGovernanceTurnQuota(ctx, key, true); sweepErr != nil {
			log.Printf("quota: turn %s:%s:%d transition settlement sweep 失败（等待重放）: %v",
				key.GoalID, key.TodoID, key.TurnSeq, sweepErr)
		}
	}
	if to != domain.TodoRunning {
		if err := s.appendGovernanceProjectionPhaseIfReady(ctx, key); err != nil {
			return err
		}
	}
	if changed {
		s.notifier.Notify(workspaceID)
	}
	return nil
}

func (s *Service) appendGovernancePlanPhase(ctx context.Context, header *domain.TurnReceiptHeader,
	seq int, payload map[string]any, planID string, runIDs []string) error {
	name, ok := domain.TurnReceiptPhaseNameForSeq(seq)
	if !ok {
		return fmt.Errorf("%w: invalid governance Plan phase %d", domain.ErrValidation, seq)
	}
	phase := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: seq, Phase: name, Payload: payload,
		PlanID: planID, RunIDs: append([]string{}, runIDs...),
	}
	_, err := s.AppendTurnReceiptPhase(ctx, phase)
	return err
}

func (s *Service) appendGovernancePlanCommittedPhases(ctx context.Context,
	header *domain.TurnReceiptHeader, plan *domain.Plan) error {
	runIDs := planResultRunIDs(plan)
	if err := s.appendGovernancePlanPhase(ctx, header, 4, map[string]any{
		"plan_id": plan.ID, "plan_client_key": plan.ClientKey,
		"decision_digest": plan.DecisionDigest,
	}, plan.ID, runIDs); err != nil {
		return err
	}
	dispatchState := "no_runs"
	if len(runIDs) > 0 {
		dispatchState = "committed"
		for _, runID := range runIDs {
			run, err := s.store.Runs().Get(ctx, runID)
			if err != nil {
				return err
			}
			if run.Failure != nil && run.Failure.Code == "dispatch_failed" {
				dispatchState = "failed"
			}
		}
	}
	if err := s.appendGovernancePlanPhase(ctx, header, 5, map[string]any{
		"plan_id": plan.ID, "dispatch_state": dispatchState, "run_count": len(runIDs),
	}, plan.ID, runIDs); err != nil {
		return err
	}
	return s.appendQuotaPhaseIfReady(ctx, header, false)
}

func (s *Service) appendQuotaPhaseIfReady(ctx context.Context, header *domain.TurnReceiptHeader, allowMissingUsage bool) error {
	if _, err := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 6); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	// 本 Turn 已冻结 usage-kind reservation → 由 usage sweep 统一落 spend、
	// 关闭 reservation 并追加 phase6（WP4-A 的 turn_count-only 载荷不适用）。
	hasUsageReservation := false
	for _, kind := range usageQuotaKinds {
		if _, err := s.store.Quotas().Get(ctx, domain.QuotaReservationKey{
			TurnKey: header.TurnKey, Kind: kind,
		}); err == nil {
			hasUsageReservation = true
			break
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
	}
	if hasUsageReservation {
		// admission/phase5 后的触发属关闭性触发源（allowAbsentClose=true）。
		return s.settleGovernanceTurnQuota(ctx, header.TurnKey, true)
	}
	goal, err := s.store.Goals().Get(ctx, header.TurnKey.GoalID)
	if err != nil {
		return err
	}
	activeWorkerEnabled := false
	for _, candidate := range goal.QuotaPolicies {
		switch candidate.Kind {
		case domain.QuotaTurnCount:
		case domain.QuotaActiveWorker:
			activeWorkerEnabled = true
		default:
			if !allowMissingUsage {
				return nil // usage-backed quota settlement has not completed yet
			}
		}
	}
	reservation, err := s.store.Quotas().Get(ctx, domain.QuotaReservationKey{
		TurnKey: header.TurnKey, Kind: domain.QuotaTurnCount,
	})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	reservations := []any{}
	reservationKeys := []string{}
	if err == nil && (reservation.Status != domain.QuotaReservationCommitted || reservation.CommittedAmount != 1) {
		return fmt.Errorf("%w: turn_count reservation is not committed", domain.ErrStateConflict)
	}
	if err == nil {
		reservations = append(reservations, map[string]any{
			"quota_kind": string(domain.QuotaTurnCount), "status": string(reservation.Status),
			"amount": reservation.CommittedAmount, "policy_limit": reservation.PolicyLimit,
			"policy_enforcement": string(reservation.PolicyEnforcement), "policy_digest": reservation.PolicyDigest,
		})
		reservationKeys = append(reservationKeys, reservation.Key.String())
	}
	activeWorkerAccounting := "not_enabled"
	if activeWorkerEnabled {
		activeWorkerAccounting = "gauge_not_spend"
	}
	return s.appendGovernancePlanPhaseWithQuota(ctx, header, map[string]any{
		"reservations":             reservations,
		"active_worker_accounting": activeWorkerAccounting,
		"unresolved_kinds":         []any{}, "unresolved_reason": "",
	}, reservationKeys)
}

func (s *Service) appendGovernancePlanPhaseWithQuota(ctx context.Context, header *domain.TurnReceiptHeader,
	payload map[string]any, reservationKeys []string) error {
	name, _ := domain.TurnReceiptPhaseNameForSeq(6)
	phase := &domain.TurnReceiptPhase{
		TurnKey: header.TurnKey, PhaseSeq: 6, Phase: name, Payload: payload,
		QuotaReservationKeys: append([]string{}, reservationKeys...),
	}
	_, err := s.AppendTurnReceiptPhase(ctx, phase)
	return err
}

func planResultRunIDs(plan *domain.Plan) []string {
	if plan == nil {
		return []string{}
	}
	runIDs := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		if step.ResultRunID != "" {
			runIDs = append(runIDs, step.ResultRunID)
		}
	}
	return runIDs
}
