package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type ResolveTodoUserActionParams struct {
	TodoID          string
	Resolution      string
	ActorID         string
	ExpectedVersion int
	ClientKey       string
}

const userActionReplayDataKey = "user_action_effects"

func userActionPayloadDigest(todoID, resolution, actorID string) (string, error) {
	return canonicalGovernancePlanDigest(struct {
		TodoID     string `json:"todo_id"`
		Resolution string `json:"resolution"`
		ActorID    string `json:"actor_id"`
	}{TodoID: todoID, Resolution: resolution, ActorID: actorID})
}

func userActionReplayRecord(data map[string]any, key string) (map[string]any, bool, error) {
	if data == nil {
		return nil, false, nil
	}
	raw, ok := data[userActionReplayDataKey]
	if !ok {
		return nil, false, nil
	}
	replays, ok := raw.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%w: malformed user-action replay ledger", domain.ErrValidation)
	}
	rawRecord, ok := replays[key]
	if !ok {
		return nil, false, nil
	}
	record, ok := rawRecord.(map[string]any)
	if !ok || record["todo_id"] == nil || record["payload_digest"] == nil {
		return nil, false, fmt.Errorf("%w: malformed user-action replay record", domain.ErrValidation)
	}
	return record, true, nil
}

func userActionReplayLedger(data map[string]any) (map[string]any, error) {
	if data == nil {
		return map[string]any{}, nil
	}
	if raw, ok := data[userActionReplayDataKey]; ok {
		if replays, ok := raw.(map[string]any); ok {
			return replays, nil
		}
		return nil, fmt.Errorf("%w: malformed user-action replay ledger", domain.ErrValidation)
	}
	return map[string]any{}, nil
}

// ResolveTodoUserAction clears a durable user-action checkpoint. It only
// restores governance state; the Coordinator recovery loop remains the sole
// path that can create a Run.
func (s *Service) ResolveTodoUserAction(ctx context.Context, p ResolveTodoUserActionParams) (*domain.Todo, error) {
	resolution := strings.TrimSpace(p.Resolution)
	actorID := strings.TrimSpace(p.ActorID)
	clientKey := strings.TrimSpace(p.ClientKey)
	if resolution == "" || len(resolution) > 4000 || actorID == "" || len(actorID) > 256 ||
		!strings.HasPrefix(actorID, "user_") || (p.ClientKey != "" && clientKey == "") || len(clientKey) > 256 {
		return nil, fmt.Errorf("%w: user action requires actor and resolution", domain.ErrValidation)
	}
	payloadDigest, err := userActionPayloadDigest(p.TodoID, resolution, actorID)
	if err != nil {
		return nil, err
	}
	var resolved *domain.Todo
	var workspaceID string
	err = s.store.InTx(ctx, func(txctx context.Context) error {
		todo, err := s.store.Todos().Get(txctx, p.TodoID)
		if err != nil {
			return err
		}
		goal, err := s.store.Goals().Get(txctx, todo.GoalID)
		if err != nil {
			return err
		}
		if goal.Status != domain.GoalActive || goal.Status.IsTerminal() || goal.CurrentTodoID != todo.ID {
			return fmt.Errorf("%w: user action requires the active Goal current Todo", domain.ErrStateConflict)
		}
		state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(txctx, goal.RootWorkItemID)
		if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
			return stateErr
		}
		if errors.Is(stateErr, domain.ErrNotFound) {
			return fmt.Errorf("%w: user action requires a Coordinator checkpoint", domain.ErrStateConflict)
		}
		if state == nil {
			return fmt.Errorf("%w: user action requires a Coordinator checkpoint", domain.ErrStateConflict)
		}
		if clientKey != "" {
			replayKey := p.TodoID + "\x00" + clientKey
			record, found, replayErr := userActionReplayRecord(state.Data, replayKey)
			if replayErr != nil {
				return replayErr
			}
			if found {
				recordedTodoID, _ := record["todo_id"].(string)
				recordedDigest, _ := record["payload_digest"].(string)
				if recordedTodoID != p.TodoID || recordedDigest != payloadDigest {
					return domain.ErrIdempotencyConflict
				}
				resolved = todo
				workspaceID = goal.WorkspaceID
				return nil
			}
		}
		if p.ExpectedVersion != 0 && todo.Version != p.ExpectedVersion {
			return domain.ErrVersionConflict
		}
		if (todo.Status != domain.TodoBlocked && todo.Status != domain.TodoWaiting) ||
			(todo.Status == domain.TodoWaiting && todo.Class != domain.TodoUserGate) {
			return fmt.Errorf("%w: user action only applies to user_gate/blocked/waiting Todo", domain.ErrStateConflict)
		}
		if stateErr == nil && state != nil {
			if state.Status != domain.CoordinatorBlocked && state.Status != domain.CoordinatorWaitingUser &&
				state.Status != domain.CoordinatorWaitingRetry {
				return fmt.Errorf("%w: Coordinator has no user-action checkpoint", domain.ErrStateConflict)
			}
		}
		fromTodo := todo.Status
		now := time.Now().UTC()
		controlOwnerID := state.CoordinatorAgentID
		keepHandoffClaim := false
		var controlReceipt *domain.TurnReceiptHeader
		if handoff, _, _, targetAgentID, handoffErr := s.handoffForCoordinatorStart(txctx, state); handoffErr != nil {
			return handoffErr
		} else if handoff != nil {
			controlOwnerID = targetAgentID
			keepHandoffClaim = true
		}
		if todo.Claim != nil {
			// Reuse the Coordinator claim when it already owns the checkpoint;
			// this keeps the user-action receipt to one claim release. A
			// different owner must be released before the control receipt can
			// claim the Todo.
			if todo.Claim.OwnerAgentID != controlOwnerID {
				released, releaseErr := s.store.Todos().Release(txctx, todo.ID, todo.Claim.OwnerAgentID, now, todo.Version)
				if releaseErr != nil {
					return releaseErr
				}
				todo = released
				if err := s.emitTodoClaimChanged(txctx, goal.WorkspaceID, todo, "released", "", nil); err != nil {
					return err
				}
			}
		}
		if todo.Status != domain.TodoPending {
			if err := todo.Transition(domain.TodoPending, now); err != nil {
				return err
			}
			if err := s.store.Todos().Update(txctx, todo, todo.Version-1); err != nil {
				return err
			}
			if err := s.emitTodoStateChanged(txctx, goal.WorkspaceID, todo, fromTodo); err != nil {
				return err
			}
			fromTodo = todo.Status
		}
		if stateErr == nil && state != nil {
			// Resolving a blocked/user-gated Todo is itself a durable control
			// outcome. Allocate a fresh receipt identity after the old claim has
			// been cleared; the control receipt is closed back to pending before
			// the Coordinator recovery state is queued.
			freshTodo, getErr := s.store.Todos().Get(txctx, todo.ID)
			if getErr != nil {
				return getErr
			}
			admissionKey := clientKey
			if admissionKey == "" {
				admissionKey = "control:user_action:" + payloadDigest
			}
			var receiptErr error
			controlReceipt, receiptErr = s.admitGovernanceControlDecisionLocked(txctx, governanceControlReceiptParams{
				Goal: goal, Todo: freshTodo, OwnerAgentID: controlOwnerID,
				Kind: domain.TurnDecisionUserAction, Reason: resolution,
				NextAction: "Coordinator 重新规划并继续执行", SourceRunID: stringValue(state.CurrentRunID),
				AdmissionKey: admissionKey, KeepClaim: keepHandoffClaim,
			})
			if receiptErr != nil {
				return receiptErr
			}
			todo, getErr = s.store.Todos().Get(txctx, todo.ID)
			if getErr != nil {
				return getErr
			}
			expected := state.Version
			state.Status = domain.CoordinatorQueued
			state.Phase = "user_action_resolved"
			state.CurrentAction = "用户动作已解决，等待 Coordinator 恢复"
			state.CurrentRunID = ""
			state.BlockerCode, state.BlockerMessage, state.LastError = "", "", ""
			state.NextActionAt = &now
			if state.Data == nil {
				state.Data = map[string]any{}
			}
			if clientKey != "" {
				replayKey := p.TodoID + "\x00" + clientKey
				replays, replayErr := userActionReplayLedger(state.Data)
				if replayErr != nil {
					return replayErr
				}
				replays[replayKey] = map[string]any{
					"todo_id": p.TodoID, "payload_digest": payloadDigest,
					"resolution": resolution, "actor_id": actorID,
				}
				state.Data[userActionReplayDataKey] = replays
			}
			delete(state.Data, "last_user_action_key")
			delete(state.Data, "last_user_action_resolution")
			delete(state.Data, "last_user_action_actor")
			if err := s.store.TaskCoordinators().UpdateState(txctx, state, expected); err != nil {
				return err
			}
			state.Version = expected + 1
			if controlReceipt != nil {
				if err := s.appendGovernanceProjectionPhaseLocked(txctx, controlReceipt.TurnKey); err != nil {
					return err
				}
			}
			eventData := map[string]any{"stage": "user_action", "actor_id": actorID, "resolution": resolution,
				"receipt_turn_seq": todo.LastTurnSeq}
			if err := s.appendCoordinatorEvent(txctx, state, goal.RootWorkItemID,
				domain.EventCoordinatorRecoveryStarted, "用户动作已解决，Coordinator 可恢复",
				"", actorID, state.Attempt, resolution, nil, eventData); err != nil {
				return err
			}
		}
		workspaceID = goal.WorkspaceID
		resolved = todo
		return nil
	})
	if err != nil {
		return nil, err
	}
	if resolved != nil && workspaceID != "" && s.notifier != nil {
		s.notifier.Notify(workspaceID)
	}
	return resolved, nil
}
