package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type governanceControlReceiptParams struct {
	Goal              *domain.Goal
	Todo              *domain.Todo
	OwnerAgentID      string
	Kind              domain.TurnDecisionKind
	Reason            string
	NextAction        string
	SourceRunID       string
	AdmissionKey      string
	ValidationCode    string
	ValidationPath    string
	ValidationMessage string
	KeepClaim         bool
}

// admitGovernanceControlDecisionLocked records a non-Plan control outcome as
// its own immutable receipt. It allocates a fresh TurnKey through the same Todo
// CAS as Plan admission, appends a typed phase-1 decision plus explicit
// no-Plan/no-run phases 2–6, then returns the Todo to pending. The caller adds
// projection phase 7 only after its Coordinator state CAS, so the projection
// observes the state transition caused by this control outcome. No provider
// Run or synthetic Plan is created.
//
// When a source Run has no existing governance TurnKey (the malformed-Plan
// repair case), its usage quota reservation is bound to this control Turn and
// settled before phase 6. A source already owned by another Turn is evidence
// only; it is never charged a second time by this control receipt.
func (s *Service) admitGovernanceControlDecisionLocked(ctx context.Context,
	p governanceControlReceiptParams) (*domain.TurnReceiptHeader, error) {
	if p.Goal == nil || p.Todo == nil {
		return nil, fmt.Errorf("%w: control receipt requires Goal and Todo", domain.ErrValidation)
	}
	if p.Goal.Status != domain.GoalActive || p.Goal.CurrentTodoID != p.Todo.ID || p.Todo.GoalID != p.Goal.ID {
		return nil, fmt.Errorf("%w: control receipt requires the active Goal current Todo", domain.ErrStateConflict)
	}
	if p.Kind == domain.TurnDecisionExecute || !p.Kind.Valid() {
		return nil, fmt.Errorf("%w: invalid non-Plan control decision %q", domain.ErrValidation, p.Kind)
	}
	if strings.TrimSpace(p.OwnerAgentID) == "" || strings.TrimSpace(p.AdmissionKey) == "" {
		return nil, fmt.Errorf("%w: control receipt owner and admission key are required", domain.ErrValidation)
	}
	if strings.TrimSpace(p.Reason) == "" || strings.TrimSpace(p.NextAction) == "" {
		return nil, fmt.Errorf("%w: control receipt reason and next action are required", domain.ErrValidation)
	}
	if existing, err := s.store.TurnReceipts().GetHeaderByClientKey(ctx, p.Goal.ID, p.Todo.ID, p.AdmissionKey); err == nil {
		if err := s.validateControlReceiptReplayIntent(ctx, p, existing); err != nil {
			return nil, err
		}
		if err := s.replayGovernanceControlReceiptLocked(ctx, p, existing); err != nil {
			return nil, err
		}
		return existing, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	freshTodo, err := s.store.Todos().Get(ctx, p.Todo.ID)
	if err != nil {
		return nil, err
	}
	if freshTodo.GoalID != p.Goal.ID || freshTodo.ID != p.Todo.ID {
		return nil, fmt.Errorf("%w: control receipt Todo scope changed", domain.ErrWorkspaceContextMismatch)
	}
	needsUsage, sourceTurn, sourceErr := s.controlSourceNeedsUsage(ctx, p.Goal, freshTodo, p.SourceRunID)
	if sourceErr != nil {
		return nil, sourceErr
	}
	// A control decision is often emitted immediately after a governed source
	// Run reaches terminal. Close that source Turn before admitting the new one;
	// otherwise the new ShouldRun check can race an old reserved ledger row and
	// leave the old budget stranded. Source settlement is idempotent and uses
	// the source Turn's frozen policy/evidence, never the new control Turn.
	if err := s.settleControlSourceTurnLocked(ctx, sourceTurn); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	switch freshTodo.Status {
	case domain.TodoPending, domain.TodoWaiting, domain.TodoBlocked:
		if freshTodo.Claim != nil {
			if freshTodo.Claim.OwnerAgentID != p.OwnerAgentID {
				return nil, fmt.Errorf("%w: control receipt Todo claim belongs to another owner", domain.ErrStateConflict)
			}
			if !freshTodo.Claim.ExpiresAt.After(now) {
				renewed, renewErr := s.renewTodoClaimLocked(ctx, p.Goal.WorkspaceID, freshTodo.ID,
					p.OwnerAgentID, freshTodo.ClaimVersion, now, now.Add(governancePlanClaimTTL), freshTodo.Version)
				if renewErr != nil {
					return nil, renewErr
				}
				freshTodo = renewed
			}
			if freshTodo.Status != domain.TodoClaimed {
				from := freshTodo.Status
				if err := freshTodo.Transition(domain.TodoClaimed, now); err != nil {
					return nil, err
				}
				if err := s.store.Todos().Update(ctx, freshTodo, freshTodo.Version-1); err != nil {
					return nil, err
				}
				if err := s.emitTodoStateChanged(ctx, p.Goal.WorkspaceID, freshTodo, from); err != nil {
					return nil, err
				}
			}
		} else {
			claimed, claimErr := s.store.Todos().Claim(ctx, freshTodo.ID, p.OwnerAgentID,
				now, now.Add(governancePlanClaimTTL), freshTodo.Version)
			if claimErr != nil {
				return nil, claimErr
			}
			if freshTodo.Status != claimed.Status {
				if err := s.emitTodoStateChanged(ctx, p.Goal.WorkspaceID, claimed, freshTodo.Status); err != nil {
					return nil, err
				}
			}
			if err := s.emitTodoClaimChanged(ctx, p.Goal.WorkspaceID, claimed, "claimed", p.OwnerAgentID, &claimed.Claim.ExpiresAt); err != nil {
				return nil, err
			}
			freshTodo = claimed
		}
		if freshTodo.Claim != nil && freshTodo.Status == domain.TodoClaimed {
			// Existing same-generation claims are preserved; only a newly
			// created Claim above emits the claimed event.
			if err := freshTodo.Claim.Validate(); err != nil {
				return nil, err
			}
		}
	case domain.TodoClaimed:
		if freshTodo.Claim == nil || freshTodo.Claim.OwnerAgentID != p.OwnerAgentID {
			return nil, fmt.Errorf("%w: control receipt Todo claim belongs to another owner", domain.ErrStateConflict)
		}
		if !freshTodo.Claim.ExpiresAt.After(now) {
			renewed, renewErr := s.renewTodoClaimLocked(ctx, p.Goal.WorkspaceID, freshTodo.ID,
				p.OwnerAgentID, freshTodo.ClaimVersion, now, now.Add(governancePlanClaimTTL), freshTodo.Version)
			if renewErr != nil {
				return nil, renewErr
			}
			freshTodo = renewed
		}
	case domain.TodoRunning:
		return nil, fmt.Errorf("%w: control receipt cannot overlap a running Todo turn", domain.ErrStateConflict)
	default:
		return nil, fmt.Errorf("%w: Todo status %s cannot admit a control receipt", domain.ErrStateConflict, freshTodo.Status)
	}
	beforeAdmitStatus := freshTodo.Status
	p.Todo = freshTodo
	turnSeq := freshTodo.LastTurnSeq + 1
	inputDigest, err := controlReceiptInputDigest(p.Goal.ID, p.Todo.ID, turnSeq, p.Kind,
		p.Reason, p.NextAction, p.SourceRunID)
	if err != nil {
		return nil, err
	}
	header := &domain.TurnReceiptHeader{
		TurnKey: domain.TurnKey{GoalID: p.Goal.ID, TodoID: p.Todo.ID, TurnSeq: turnSeq},
		Attempt: 1, SchemaVersion: planDecisionSchemaVersion, InputSnapshotDigest: inputDigest,
		AdmissionClientKey: p.AdmissionKey, CanonicalDigest: emptySHA256Digest, CreatedAt: now,
	}
	header.CanonicalDigest, err = ComputeTurnReceiptHeaderDigest(header)
	if err != nil {
		return nil, err
	}
	turnQuotaDecision, err := s.ShouldRunLocked(ctx, ShouldRunRequest{GoalID: p.Goal.ID, Kind: domain.QuotaTurnCount, Amount: 1})
	if err != nil {
		return nil, err
	}
	if turnQuotaDecision.Enabled && !turnQuotaDecision.Allowed {
		return nil, quotaDeniedError(turnQuotaDecision)
	}
	admitted, err := s.store.TurnReceipts().Admit(ctx, header, p.OwnerAgentID, freshTodo.Version)
	if err != nil {
		return nil, err
	}
	freshTodo, err = s.store.Todos().Get(ctx, p.Todo.ID)
	if err != nil {
		return nil, err
	}
	if err := s.emitTodoStateChanged(ctx, p.Goal.WorkspaceID, freshTodo, beforeAdmitStatus); err != nil {
		return nil, err
	}
	if err := s.emitTurnReceiptAppended(ctx, p.Goal.WorkspaceID, freshTodo.Version, admitted, nil); err != nil {
		return nil, err
	}
	if _, err := s.ensureTurnCountReservationLocked(ctx, p.Goal, admitted, turnQuotaDecision); err != nil {
		return nil, err
	}
	if needsUsage {
		if err := s.ensureUsageQuotaReservationsLocked(ctx, p.Goal, admitted.TurnKey); err != nil {
			return nil, err
		}
	}
	if err := s.appendControlReceiptPhases(ctx, admitted, p); err != nil {
		return nil, err
	}
	if err := s.closeControlReceiptTodo(ctx, p.Goal, freshTodo, p.KeepClaim); err != nil {
		return nil, err
	}
	if err := s.finishControlReceiptQuota(ctx, admitted); err != nil {
		return nil, err
	}
	return admitted, nil
}

func controlReceiptInputDigest(goalID, todoID string, turnSeq int64, kind domain.TurnDecisionKind,
	reason, nextAction, sourceRunID string) (string, error) {
	return canonicalGovernancePlanDigest(struct {
		GoalID      string                  `json:"goal_id"`
		TodoID      string                  `json:"todo_id"`
		TurnSeq     int64                   `json:"turn_seq"`
		Kind        domain.TurnDecisionKind `json:"kind"`
		Reason      string                  `json:"reason"`
		NextAction  string                  `json:"next_action"`
		SourceRunID string                  `json:"source_run_id,omitempty"`
	}{GoalID: goalID, TodoID: todoID, TurnSeq: turnSeq, Kind: kind,
		Reason: reason, NextAction: nextAction, SourceRunID: sourceRunID})
}

func (s *Service) validateControlReceiptReplayIntent(ctx context.Context,
	p governanceControlReceiptParams, header *domain.TurnReceiptHeader) error {
	if header == nil {
		return domain.ErrIdempotencyConflict
	}
	sourceRunID := p.SourceRunID
	if sourceRunID == "" {
		if phase, err := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 1); err == nil {
			sourceRunID, _ = phase.Payload["source_run_id"].(string)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
	}
	digest, err := controlReceiptInputDigest(header.TurnKey.GoalID, header.TurnKey.TodoID,
		header.TurnKey.TurnSeq, p.Kind, p.Reason, p.NextAction, sourceRunID)
	if err != nil {
		return err
	}
	if digest != header.InputSnapshotDigest {
		return fmt.Errorf("%w: control receipt replay intent differs from admitted Header", domain.ErrIdempotencyConflict)
	}
	return nil
}

func (s *Service) appendControlReceiptPhases(ctx context.Context, header *domain.TurnReceiptHeader,
	p governanceControlReceiptParams) error {
	decision, err := newGovernanceTurnDecision(header.TurnKey, p.Kind, p.Reason, p.NextAction,
		planDecisionSchemaVersion, nil, header.CreatedAt, controlValidationErrors(p))
	if err != nil {
		return err
	}
	if err := s.appendGovernancePlanPhase(ctx, header, 1, map[string]any{
		"control_outcome": true, "decision": string(p.Kind), "source_run_id": p.SourceRunID,
		"turn_decision": decision,
	}, "", nil); err != nil {
		return err
	}
	validationPayload := map[string]any{
		"valid": p.ValidationCode == "", "control_outcome": true, "decision": string(p.Kind),
	}
	if p.ValidationCode != "" {
		validationPayload["error_code"] = p.ValidationCode
		validationPayload["path"] = validationPath(p)
		validationPayload["message"] = validationMessage(p)
	}
	if err := s.appendGovernancePlanPhase(ctx, header, 2, validationPayload, "", nil); err != nil {
		return err
	}
	if err := s.appendGovernancePlanPhase(ctx, header, 3, map[string]any{
		"status": "control_committed", "decision": string(p.Kind), "source_run_id": p.SourceRunID,
	}, "", nil); err != nil {
		return err
	}
	if err := s.appendGovernancePlanPhase(ctx, header, 4, map[string]any{
		"control_outcome": true, "decision": string(p.Kind), "source_run_id": p.SourceRunID,
	}, "", nil); err != nil {
		return err
	}
	return s.appendGovernancePlanPhase(ctx, header, 5, map[string]any{
		"control_outcome": true, "decision": string(p.Kind), "dispatch_state": "no_runs", "run_count": 0,
	}, "", nil)
}

func (s *Service) finishControlReceiptQuota(ctx context.Context, header *domain.TurnReceiptHeader) error {
	hasUsageReservation := false
	for _, kind := range usageQuotaKinds {
		if _, err := s.store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: header.TurnKey, Kind: kind}); err == nil {
			hasUsageReservation = true
			break
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
	}
	if hasUsageReservation {
		return s.settleGovernanceTurnQuotaLocked(ctx, header.TurnKey, true)
	}
	// A governed source Run is already charged by its own Turn, so this control
	// receipt deliberately creates no duplicate usage reservation. It still
	// needs a contiguous phase-6 record; allow the turn_count-only payload even
	// when the Goal has usage policies.
	return s.appendQuotaPhaseIfReady(ctx, header, true)
}

// settleControlSourceTurnLocked closes the governed source Turn before a new
// control Turn is admitted. The source receipt's phase-7 projection is added
// only after phase 6 exists; malformed/aborted source receipts intentionally
// remain without fabricated Plan phases.
func (s *Service) settleControlSourceTurnLocked(ctx context.Context, key *domain.TurnKey) error {
	if key == nil {
		return nil
	}
	lock := &s.governanceQuotaLocks[governancePlanLockIndex(
		fmt.Sprintf("%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq))]
	lock.Lock()
	defer lock.Unlock()
	return s.store.InTx(ctx, func(txctx context.Context) error {
		if err := s.settleGovernanceTurnQuotaLocked(txctx, *key, true); err != nil {
			return err
		}
		if _, phaseErr := s.store.TurnReceipts().GetPhase(txctx, *key, 6); phaseErr == nil {
			if err := s.appendGovernanceProjectionPhaseLocked(txctx, *key); err != nil {
				return err
			}
			return nil
		} else if !errors.Is(phaseErr, domain.ErrNotFound) {
			return phaseErr
		}
		for _, kind := range append([]domain.QuotaKind{domain.QuotaTurnCount}, usageQuotaKinds...) {
			reservation, reservationErr := s.store.Quotas().Get(txctx, domain.QuotaReservationKey{TurnKey: *key, Kind: kind})
			if errors.Is(reservationErr, domain.ErrNotFound) {
				continue
			}
			if reservationErr != nil {
				return reservationErr
			}
			if reservation != nil && reservation.Status == domain.QuotaReservationReserved {
				return fmt.Errorf("%w: source governance Turn quota settlement is still pending", domain.ErrStateConflict)
			}
		}
		return nil
	})
}

// renewTodoClaimLocked is the single application write path for a same-owner
// governance claim renewal. The repository keeps the original claimed_at
// immutable; this wrapper emits the matching lifecycle event in the same
// transaction so readers never observe a version/expiry change without the
// corresponding Todo claim event.
func (s *Service) renewTodoClaimLocked(ctx context.Context, workspaceID, todoID, ownerAgentID string,
	claimVersion int, renewedAt, expiresAt time.Time, expectedVersion int) (*domain.Todo, error) {
	renewed, err := s.store.Todos().RenewClaim(ctx, todoID, ownerAgentID, claimVersion,
		renewedAt, expiresAt, expectedVersion)
	if err != nil {
		return nil, err
	}
	if renewed == nil || renewed.Claim == nil {
		return nil, fmt.Errorf("%w: Todo claim renewal returned no active claim", domain.ErrStateConflict)
	}
	if err := s.emitTodoClaimChanged(ctx, workspaceID, renewed, "renewed", ownerAgentID, &renewed.Claim.ExpiresAt); err != nil {
		return nil, err
	}
	return renewed, nil
}

// controlSourceNeedsUsage validates a control receipt's source Run before
// deciding whether that Run is already covered by an existing governed turn.
// A missing, non-terminal, cross-workspace, cross-tree, or cross-Goal/Todo
// source is a hard error; silently treating it as an ungoverned repair would
// either hide storage corruption or charge the wrong authority line.
func (s *Service) controlSourceNeedsUsage(ctx context.Context, goal *domain.Goal, todo *domain.Todo, sourceRunID string) (bool, *domain.TurnKey, error) {
	if strings.TrimSpace(sourceRunID) == "" {
		return false, nil, nil
	}
	if goal == nil || todo == nil {
		return false, nil, fmt.Errorf("%w: control source validation requires Goal and Todo", domain.ErrValidation)
	}
	if todo.GoalID != goal.ID || goal.CurrentTodoID != todo.ID {
		return false, nil, fmt.Errorf("%w: control source Goal/Todo scope is stale", domain.ErrStateConflict)
	}
	run, err := s.store.Runs().Get(ctx, sourceRunID)
	if err != nil {
		return false, nil, err
	}
	if run == nil {
		return false, nil, domain.ErrNotFound
	}
	if run.WorkspaceID != goal.WorkspaceID {
		return false, nil, fmt.Errorf("%w: control source Run is outside Goal workspace", domain.ErrWorkspaceContextMismatch)
	}
	workItem, err := s.store.WorkItems().Get(ctx, run.WorkItemID)
	if err != nil {
		return false, nil, err
	}
	if workItem == nil || workItem.WorkspaceID != goal.WorkspaceID {
		return false, nil, fmt.Errorf("%w: control source Run WorkItem is outside Goal workspace", domain.ErrWorkspaceContextMismatch)
	}
	root, err := s.workItemRoot(ctx, workItem)
	if err != nil {
		return false, nil, err
	}
	if root == nil || root.ID != goal.RootWorkItemID || root.WorkspaceID != goal.WorkspaceID {
		return false, nil, fmt.Errorf("%w: control source Run is outside Goal task tree", domain.ErrWorkspaceContextMismatch)
	}
	if !run.Status.IsTerminal() {
		return false, nil, fmt.Errorf("%w: control source Run is not terminal", domain.ErrStateConflict)
	}
	if key, governed := runGovernanceTurnKey(run); governed {
		if key.GoalID != goal.ID || key.TodoID != todo.ID {
			return false, nil, fmt.Errorf("%w: control source Run belongs to another governance Turn", domain.ErrWorkspaceContextMismatch)
		}
		return false, &key, nil
	}
	return true, nil, nil
}

func (s *Service) closeControlReceiptTodo(ctx context.Context, goal *domain.Goal, todo *domain.Todo, keepClaim bool) error {
	if goal == nil || todo == nil {
		return fmt.Errorf("%w: control receipt Todo is not running", domain.ErrStateConflict)
	}
	if todo.Status == domain.TodoPending {
		return nil
	}
	if todo.Status != domain.TodoRunning || todo.Claim == nil {
		return fmt.Errorf("%w: control receipt Todo is not running", domain.ErrStateConflict)
	}
	now := time.Now().UTC()
	from := todo.Status
	if err := todo.Transition(domain.TodoWaiting, now); err != nil {
		return err
	}
	if err := s.store.Todos().Update(ctx, todo, todo.Version-1); err != nil {
		return err
	}
	if err := s.emitTodoStateChanged(ctx, goal.WorkspaceID, todo, from); err != nil {
		return err
	}
	if keepClaim {
		from = todo.Status
		if err := todo.Transition(domain.TodoClaimed, now); err != nil {
			return err
		}
		if err := s.store.Todos().Update(ctx, todo, todo.Version-1); err != nil {
			return err
		}
		return s.emitTodoStateChanged(ctx, goal.WorkspaceID, todo, from)
	}
	released, err := s.store.Todos().Release(ctx, todo.ID, todo.Claim.OwnerAgentID, now, todo.Version)
	if err != nil {
		return err
	}
	if err := s.emitTodoClaimChanged(ctx, goal.WorkspaceID, released, "released", "", nil); err != nil {
		return err
	}
	if released.Status == domain.TodoWaiting {
		from = released.Status
		if err := released.Transition(domain.TodoPending, now); err != nil {
			return err
		}
		if err := s.store.Todos().Update(ctx, released, released.Version-1); err != nil {
			return err
		}
		if err := s.emitTodoStateChanged(ctx, goal.WorkspaceID, released, from); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) replayGovernanceControlReceiptLocked(ctx context.Context,
	p governanceControlReceiptParams, header *domain.TurnReceiptHeader) error {
	if header == nil || header.TurnKey.GoalID != p.Goal.ID || header.TurnKey.TodoID != p.Todo.ID {
		return domain.ErrIdempotencyConflict
	}
	goal, err := s.store.Goals().Get(ctx, header.TurnKey.GoalID)
	if err != nil {
		return err
	}
	todo, err := s.store.Todos().Get(ctx, header.TurnKey.TodoID)
	if err != nil {
		return err
	}
	// A crash can leave the immutable Header committed while its immediate
	// turn_count reservation (or the reservation's commit transition) is still
	// missing. Replays must reconstruct that admission ledger before attempting
	// phase 6; otherwise an otherwise valid control receipt loops forever on a
	// reserved/missing quota row.
	if _, err := s.ensureExistingTurnCountReservationLocked(ctx, goal, header); err != nil {
		return err
	}
	sourceRunID := p.SourceRunID
	if sourceRunID == "" {
		if phase, phaseErr := s.store.TurnReceipts().GetPhase(ctx, header.TurnKey, 1); phaseErr == nil {
			sourceRunID, _ = phase.Payload["source_run_id"].(string)
		} else if !errors.Is(phaseErr, domain.ErrNotFound) {
			return phaseErr
		}
	}
	p.SourceRunID = sourceRunID
	needsUsage, sourceTurn, sourceErr := s.controlSourceNeedsUsage(ctx, goal, todo, sourceRunID)
	if sourceErr != nil {
		return sourceErr
	}
	if err := s.settleControlSourceTurnLocked(ctx, sourceTurn); err != nil {
		return err
	}
	if needsUsage {
		if err := s.ensureUsageQuotaReservationsLocked(ctx, goal, header.TurnKey); err != nil {
			return err
		}
	}
	if err := s.appendControlReceiptPhases(ctx, header, p); err != nil {
		return err
	}
	if err := s.closeControlReceiptTodo(ctx, goal, todo, p.KeepClaim); err != nil {
		return err
	}
	if err := s.finishControlReceiptQuota(ctx, header); err != nil {
		return err
	}
	return nil
}

func controlValidationErrors(p governanceControlReceiptParams) []domain.GovernanceValidationError {
	if p.ValidationCode == "" {
		return []domain.GovernanceValidationError{}
	}
	return []domain.GovernanceValidationError{{
		Code: domain.GovernanceErrorCode(p.ValidationCode), Message: validationMessage(p), Path: validationPath(p),
	}}
}

func validationPath(p governanceControlReceiptParams) string {
	if strings.TrimSpace(p.ValidationPath) == "" {
		return "/"
	}
	return p.ValidationPath
}

func validationMessage(p governanceControlReceiptParams) string {
	if strings.TrimSpace(p.ValidationMessage) == "" {
		return p.Reason
	}
	return p.ValidationMessage
}
