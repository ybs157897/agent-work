package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// CreateGoalParams binds a new Goal to an existing root Task. WP1 does not
// create or start a WorkItem/Run as a side effect of creating governance state.
type CreateGoalParams struct {
	RootWorkItemID     string
	Objective          string
	AcceptanceContract []string
	QuotaPolicies      []domain.QuotaPolicy
}

func (s *Service) CreateGoal(ctx context.Context, workspaceID string, p CreateGoalParams) (*domain.Goal, error) {
	goal, _, err := s.createGoal(ctx, workspaceID, p)
	return goal, err
}

func (s *Service) createGoal(ctx context.Context, workspaceID string, p CreateGoalParams) (*domain.Goal, bool, error) {
	now := time.Now().UTC()
	goal := &domain.Goal{
		ID:                        domain.NewID(domain.PrefixGoal),
		WorkspaceID:               workspaceID,
		RootWorkItemID:            p.RootWorkItemID,
		Objective:                 p.Objective,
		AcceptanceContract:        append([]string(nil), p.AcceptanceContract...),
		Status:                    domain.GoalDraft,
		Phase:                     "draft",
		QuotaPolicies:             append([]domain.QuotaPolicy(nil), p.QuotaPolicies...),
		CompletionEvidenceSummary: []domain.GovernanceEvidenceItem{},
		Version:                   1,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if err := goal.Validate(); err != nil {
		return nil, false, err
	}

	created := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		root, err := s.store.WorkItems().Get(ctx, p.RootWorkItemID)
		if err != nil {
			return err
		}
		if root.WorkspaceID != workspaceID || root.ParentID != "" || !isTaskWorkItem(root) {
			return fmt.Errorf("%w: Goal root must be a same-workspace root Task", domain.ErrValidation)
		}
		existing, err := s.store.Goals().GetByRootWorkItem(ctx, p.RootWorkItemID)
		if err == nil {
			if !goalCreateIntentEqual(existing, goal) {
				return domain.ErrIdempotencyConflict
			}
			goal = existing
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if root.Status.IsTerminal() {
			return fmt.Errorf("%w: terminal root Task cannot start a Goal", domain.ErrStateConflict)
		}
		if err := s.store.Goals().Create(ctx, goal); err != nil {
			return err
		}
		if err := s.emit(ctx, workspaceID, domain.EventGoalCreated,
			domain.AggregateGoal, goal.ID, goal.Version, nil, map[string]any{
				"root_work_item_id": goal.RootWorkItemID,
				"state":             string(goal.Status),
				"version":           goal.Version,
			}); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if created {
		s.notifier.Notify(workspaceID)
	}
	return goal, created, nil
}

func goalCreateIntentEqual(existing, candidate *domain.Goal) bool {
	return existing != nil && candidate != nil &&
		existing.WorkspaceID == candidate.WorkspaceID &&
		existing.RootWorkItemID == candidate.RootWorkItemID &&
		existing.Objective == candidate.Objective &&
		slices.Equal(existing.AcceptanceContract, candidate.AcceptanceContract) &&
		slices.Equal(existing.QuotaPolicies, candidate.QuotaPolicies)
}

func (s *Service) GetGoal(ctx context.Context, goalID string) (*domain.Goal, error) {
	return s.store.Goals().Get(ctx, goalID)
}

func (s *Service) ListGoals(ctx context.Context, workspaceID string) ([]*domain.Goal, error) {
	return s.store.Goals().List(ctx, workspaceID)
}

func (s *Service) GetTodo(ctx context.Context, todoID string) (*domain.Todo, error) {
	return s.store.Todos().Get(ctx, todoID)
}

func (s *Service) ListTodos(ctx context.Context, goalID string) ([]*domain.Todo, error) {
	return s.store.Todos().ListByGoal(ctx, goalID)
}

func (s *Service) StartGoal(ctx context.Context, goalID string, expectedVersion int) (*domain.Goal, error) {
	var started *domain.Goal
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		goal, err := s.store.Goals().Get(ctx, goalID)
		if err != nil {
			return err
		}
		if err := goal.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if goal.Status != domain.GoalDraft || goal.CurrentTodoID != "" {
			return domain.ErrStateConflict
		}
		root, err := s.store.WorkItems().Get(ctx, goal.RootWorkItemID)
		if err != nil {
			return err
		}
		ownerID, err := s.goalOwnerAgentID(ctx, root)
		if err != nil {
			return err
		}
		scopeAgentIDs, err := s.initialTodoDecisionScopeAgentIDs(ctx, root.WorkspaceID, ownerID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		priority := root.Priority
		if !priority.Valid() {
			priority = domain.PriorityMedium
		}
		todo := &domain.Todo{
			ID:           domain.NewID(domain.PrefixTodo),
			GoalID:       goal.ID,
			Class:        domain.TodoAdvancement,
			Status:       domain.TodoPending,
			Instruction:  goal.Objective,
			Acceptance:   append([]string(nil), goal.AcceptanceContract...),
			Priority:     priority,
			Predecessors: []string{},
			Successors:   []string{},
			DecisionScope: domain.DecisionScope{
				WorkItemIDs:         []string{root.ID},
				AgentIDs:            scopeAgentIDs,
				RuntimeCapabilities: []string{},
				WriteScopes:         []string{},
				MaxDispatch:         64,
			},
			Version:   1,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := todo.Validate(); err != nil {
			return err
		}
		if err := s.store.Todos().Create(ctx, todo); err != nil {
			return err
		}
		from := goal.Status
		goal.CurrentTodoID = todo.ID
		goal.Phase = "execution"
		if err := goal.Start(now); err != nil {
			return err
		}
		if err := s.store.Goals().Update(ctx, goal, expectedVersion); err != nil {
			return err
		}
		if err := s.emitTodoCreated(ctx, goal.WorkspaceID, todo); err != nil {
			return err
		}
		if err := s.emitGoalStateChanged(ctx, goal, from); err != nil {
			return err
		}
		started = goal
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(started.WorkspaceID)
	return started, nil
}

func (s *Service) goalOwnerAgentID(ctx context.Context, root *domain.WorkItem) (string, error) {
	if root == nil || !isTaskWorkItem(root) || root.ParentID != "" {
		return "", fmt.Errorf("%w: Goal owner requires a root Task", domain.ErrValidation)
	}
	ownerID := root.AgentProfileID
	if state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, root.ID); err == nil {
		if ownerID != "" && ownerID != state.CoordinatorAgentID {
			return "", fmt.Errorf("%w: root Task and Coordinator owners diverged", domain.ErrStateConflict)
		}
		ownerID = state.CoordinatorAgentID
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	if ownerID == "" {
		return "", fmt.Errorf("%w: root Task has no governance owner", domain.ErrValidation)
	}
	agent, err := s.store.Agents().Get(ctx, ownerID)
	if err != nil {
		return "", err
	}
	if agent.WorkspaceID != root.WorkspaceID {
		return "", fmt.Errorf("%w: governance owner is outside root workspace", domain.ErrValidation)
	}
	return ownerID, nil
}

// initialTodoDecisionScopeAgentIDs snapshots the claim/dispatch allowlist at
// Todo creation time. AgentRepo.List intentionally omits the protected system
// profile, so the owner is loaded separately: a system Coordinator is always
// retained as the claim actor, while an ordinary disabled owner fails closed.
// The returned order is stable (owner first, then lexical IDs), and the
// snapshot is never recomputed for an existing Todo.
func (s *Service) initialTodoDecisionScopeAgentIDs(ctx context.Context, workspaceID, ownerID string) ([]string, error) {
	owner, err := s.store.Agents().Get(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if owner.WorkspaceID != workspaceID {
		return nil, fmt.Errorf("%w: governance owner is outside root workspace", domain.ErrValidation)
	}
	if !owner.Kind.IsSystem() && owner.Availability != domain.AgentEnabled {
		return nil, fmt.Errorf("%w: ordinary governance owner is disabled", domain.ErrStateConflict)
	}

	ids := []string{ownerID}
	seen := map[string]struct{}{ownerID: {}}
	agents, err := s.store.Agents().List(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, agent := range agents {
		if agent == nil || agent.Kind.IsSystem() || agent.Availability != domain.AgentEnabled {
			continue
		}
		if _, exists := seen[agent.ID]; exists {
			continue
		}
		seen[agent.ID] = struct{}{}
		ids = append(ids, agent.ID)
	}
	if len(ids) > 1 {
		slices.Sort(ids[1:])
	}
	return ids, nil
}

func (s *Service) PauseGoal(ctx context.Context, goalID string, expectedVersion int) (*domain.Goal, error) {
	return s.transitionGoalAndCurrentTodo(ctx, goalID, expectedVersion, "pause")
}

func (s *Service) ResumeGoal(ctx context.Context, goalID string, expectedVersion int) (*domain.Goal, error) {
	return s.transitionGoalAndCurrentTodo(ctx, goalID, expectedVersion, "resume")
}

// goalSettlementTurnKeys snapshots every still-open usage reservation for a
// Goal. Cancellation may race a later governed turn, so the latest Todo
// watermark alone is not sufficient to identify all quota work that must be
// replayed after the Coordinator is terminal.
func (s *Service) goalSettlementTurnKeys(ctx context.Context, goalID string, current *domain.TurnKey) ([]domain.TurnKey, error) {
	if strings.TrimSpace(goalID) == "" {
		return nil, fmt.Errorf("%w: Goal settlement requires a Goal id", domain.ErrValidation)
	}
	seen := map[domain.TurnKey]struct{}{}
	add := func(key domain.TurnKey) error {
		if err := key.Validate(); err != nil {
			return err
		}
		if key.GoalID != goalID {
			return fmt.Errorf("%w: quota reservation crosses Goal boundary", domain.ErrWorkspaceContextMismatch)
		}
		seen[key] = struct{}{}
		return nil
	}
	if current != nil {
		if err := add(*current); err != nil {
			return nil, err
		}
	}
	reservations, err := s.store.Quotas().ListByGoal(ctx, goalID)
	if err != nil {
		return nil, err
	}
	for _, reservation := range reservations {
		if reservation == nil || reservation.Status != domain.QuotaReservationReserved {
			continue
		}
		if err := add(reservation.Key.TurnKey); err != nil {
			return nil, err
		}
	}
	keys := make([]domain.TurnKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(left, right domain.TurnKey) int {
		if left.GoalID != right.GoalID {
			return strings.Compare(left.GoalID, right.GoalID)
		}
		if left.TodoID != right.TodoID {
			return strings.Compare(left.TodoID, right.TodoID)
		}
		if left.TurnSeq < right.TurnSeq {
			return -1
		}
		if left.TurnSeq > right.TurnSeq {
			return 1
		}
		return 0
	})
	return keys, nil
}

func encodeGoalSettlementTurnKeys(keys []domain.TurnKey) []any {
	encoded := make([]any, 0, len(keys))
	for _, key := range keys {
		encoded = append(encoded, map[string]any{
			"goal_id": key.GoalID, "todo_id": key.TodoID, "turn_seq": key.TurnSeq,
		})
	}
	return encoded
}

// decodeGoalSettlementTurnKeys accepts both the current JSON-cloned []any
// shape and the typed forms used by in-memory callers. A malformed checkpoint
// is an error: recovery must retry visibly instead of silently dropping a
// quota identity.
func decodeGoalSettlementTurnKeys(state *domain.TaskCoordinatorState, goalID string, fallback *domain.TurnKey) ([]domain.TurnKey, error) {
	if state == nil {
		return nil, fmt.Errorf("%w: cancelled Goal Coordinator state required", domain.ErrValidation)
	}
	seen := map[domain.TurnKey]struct{}{}
	add := func(key domain.TurnKey) error {
		if err := key.Validate(); err != nil {
			return err
		}
		if goalID != "" && key.GoalID != goalID {
			return fmt.Errorf("%w: cancelled Goal checkpoint crosses Goal boundary", domain.ErrWorkspaceContextMismatch)
		}
		seen[key] = struct{}{}
		return nil
	}
	if fallback != nil {
		if err := add(*fallback); err != nil {
			return nil, err
		}
	}
	if state.Data == nil {
		return sortedTurnKeys(seen), nil
	}
	raw, exists := state.Data[coordinatorCancelSettlementTurnKeysDataKey]
	if !exists {
		return sortedTurnKeys(seen), nil
	}
	items, ok := raw.([]any)
	if !ok {
		if typed, typedOK := raw.([]domain.TurnKey); typedOK {
			for _, key := range typed {
				if err := add(key); err != nil {
					return nil, err
				}
			}
			return sortedTurnKeys(seen), nil
		}
		return nil, fmt.Errorf("%w: cancelled Goal settlement checkpoint has invalid turn_keys", domain.ErrValidation)
	}
	for index, item := range items {
		payload, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: cancelled Goal settlement turn_keys[%d] is not an object", domain.ErrValidation, index)
		}
		goalValue, _ := payload["goal_id"].(string)
		todoValue, _ := payload["todo_id"].(string)
		turnSeq, ok := governanceInt64(payload["turn_seq"])
		if goalValue == "" || todoValue == "" || !ok {
			return nil, fmt.Errorf("%w: cancelled Goal settlement turn_keys[%d] is incomplete", domain.ErrValidation, index)
		}
		if err := add(domain.TurnKey{GoalID: goalValue, TodoID: todoValue, TurnSeq: turnSeq}); err != nil {
			return nil, err
		}
	}
	return sortedTurnKeys(seen), nil
}

func sortedTurnKeys(seen map[domain.TurnKey]struct{}) []domain.TurnKey {
	keys := make([]domain.TurnKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(left, right domain.TurnKey) int {
		if left.GoalID != right.GoalID {
			return strings.Compare(left.GoalID, right.GoalID)
		}
		if left.TodoID != right.TodoID {
			return strings.Compare(left.TodoID, right.TodoID)
		}
		if left.TurnSeq < right.TurnSeq {
			return -1
		}
		if left.TurnSeq > right.TurnSeq {
			return 1
		}
		return 0
	})
	return keys
}

// CommittedWithRecoveryError reports a command whose authoritative state was
// committed, while a post-commit cleanup/recovery action still needs replay.
// The embedded Goal is the response truth; callers must not retry the command
// as if the transaction had rolled back.
type CommittedWithRecoveryError struct {
	Goal           *domain.Goal
	RecoveryKind   string
	RootWorkItemID string
	Cause          error
}

func (e *CommittedWithRecoveryError) Error() string {
	if e == nil || e.Cause == nil {
		return "command committed with recovery pending"
	}
	return fmt.Sprintf("command committed with recovery pending: %v", e.Cause)
}

func (e *CommittedWithRecoveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CommittedWithRecoveryError) RecoveryPending() bool { return e != nil }

type cancelRunForward struct {
	runID  string
	action string
}

func cancelRunDecision(status domain.RunStatus) (target domain.RunStatus, action string, transition bool, err error) {
	action = "cancel"
	switch status {
	case domain.RunQueued, domain.RunStarting, domain.RunReconnecting, domain.RunSucceeding:
		return domain.RunCancelled, action, true, nil
	case domain.RunRunning, domain.RunWaitingApproval:
		return domain.RunCancelling, action, true, nil
	case domain.RunCancelling:
		return status, action, false, nil
	case domain.RunInterrupting:
		// Preserve the legal interrupting path; a cancel transition is not a
		// valid edge from this state. The existing interrupt signal remains the
		// matching external control operation.
		return status, "interrupt", false, nil
	default:
		return "", "", false, fmt.Errorf("%w: unknown non-terminal Run status %s during Goal cancellation", domain.ErrValidation, status)
	}
}

func (s *Service) CancelGoal(ctx context.Context, goalID string, expectedVersion int) (*domain.Goal, error) {
	var cancelled *domain.Goal
	var runsToForward []cancelRunForward
	var turnKeys []domain.TurnKey
	hasCoordinator := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		goal, err := s.store.Goals().Get(ctx, goalID)
		if err != nil {
			return err
		}
		if err := goal.CheckVersion(expectedVersion); err != nil {
			return err
		}
		now := time.Now().UTC()
		if goal.CurrentTodoID != "" {
			todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
			if err != nil {
				return err
			}
			var currentTurnKey *domain.TurnKey
			if todo.LastTurnSeq > 0 {
				key := domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: todo.LastTurnSeq}
				currentTurnKey = &key
			}
			turnKeys, err = s.goalSettlementTurnKeys(ctx, goal.ID, currentTurnKey)
			if err != nil {
				return err
			}
		} else {
			turnKeys, err = s.goalSettlementTurnKeys(ctx, goal.ID, nil)
			if err != nil {
				return err
			}
		}
		fromGoal := goal.Status
		if err := goal.Cancel(now); err != nil {
			return err
		}
		if err := s.store.Goals().Update(ctx, goal, expectedVersion); err != nil {
			return err
		}
		if err := s.emitGoalStateChanged(ctx, goal, fromGoal); err != nil {
			return err
		}
		todos, err := s.store.Todos().ListByGoal(ctx, goal.ID)
		if err != nil {
			return err
		}
		for _, todo := range todos {
			if err := s.cancelTodoLocked(ctx, goal, todo, now); err != nil {
				return err
			}
		}

		state, stateErr := s.store.TaskCoordinators().GetState(ctx, goal.RootWorkItemID)
		if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
			return stateErr
		}
		if stateErr == nil {
			hasCoordinator = true
			if state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled {
				return fmt.Errorf("%w: terminal Coordinator cannot be cancelled by a non-terminal Goal", domain.ErrStateConflict)
			}
			fromCoordinator := state.Status
			expected := state.Version
			clearCoordinatorRepairCheckpoint(state)
			state.Status = domain.CoordinatorCancelled
			state.Phase = "cancelled"
			state.Summary = "Goal 已取消"
			state.CurrentAction = "Goal 已取消"
			state.CurrentRunID = ""
			state.NextActionAt = nil
			state.BlockerCode, state.BlockerMessage, state.LastError = "", "", ""
			if state.Data == nil {
				state.Data = map[string]any{}
			}
			if len(turnKeys) > 0 {
				state.Data["control_action"] = coordinatorCancelSettlementAction
				state.Data[coordinatorCancelSettlementTurnKeysDataKey] = encodeGoalSettlementTurnKeys(turnKeys)
			} else {
				delete(state.Data, "control_action")
				delete(state.Data, coordinatorCancelSettlementTurnKeysDataKey)
			}
			if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
				return err
			}
			state.Version = expected + 1
			if err := s.appendCoordinatorEvent(ctx, state, goal.RootWorkItemID,
				domain.EventCoordinatorStateChanged, "Goal 取消，Coordinator 已停止",
				"", state.CoordinatorAgentID, state.Attempt, "goal_cancelled", nil,
				map[string]any{"stage": "cancelled", "from_state": string(fromCoordinator),
					"to_state": string(domain.CoordinatorCancelled)}); err != nil {
				return err
			}
		}

		plan, planErr := s.store.Plans().ActiveByWorkItem(ctx, goal.RootWorkItemID)
		if planErr != nil {
			return planErr
		}
		if plan != nil && !plan.Status.IsTerminal() {
			if err := s.expirePlanDispatchApprovals(ctx, plan, "system:goal_cancelled",
				fmt.Sprintf("goal %s cancelled", goal.ID), now,
				map[string]any{"goal_id": goal.ID}); err != nil {
				return err
			}
			if err := s.skipRemainingSteps(ctx, plan, 0); err != nil {
				return err
			}
			expected := plan.Version
			if err := plan.Transition(domain.PlanCancelled, now); err != nil {
				return err
			}
			if err := s.store.Plans().Update(ctx, plan, expected); err != nil {
				return err
			}
			if err := s.emit(ctx, plan.WorkspaceID, domain.EventPlanFinished,
				domain.AggregatePlan, plan.ID, plan.Version, nil,
				map[string]any{"work_item_id": plan.WorkItemID, "status": string(plan.Status),
					"goal_id": goal.ID, "record_kind": string(domain.RecordKindTask)}); err != nil {
				return err
			}
		}
		handoffs, err := s.store.Handoffs().ListByGoal(ctx, goal.ID)
		if err != nil {
			return err
		}
		for _, handoff := range handoffs {
			if handoff == nil || handoff.Status != domain.HandoffPending {
				continue
			}
			expected := handoff.Version
			if err := handoff.Cancel(now); err != nil {
				return err
			}
			if err := s.store.Handoffs().Update(ctx, handoff, expected); err != nil {
				return err
			}
			if err := s.emitHandoffEvent(ctx, goal.WorkspaceID, handoff.TodoID, handoff,
				string(domain.HandoffPending), string(domain.HandoffCancelled)); err != nil {
				return err
			}
		}

		tree, err := s.WorkItemTree(ctx, goal.RootWorkItemID)
		if err != nil {
			return err
		}
		for _, item := range tree {
			if item == nil {
				continue
			}
			if !item.Status.IsTerminal() {
				expected := item.Version
				if err := item.Transition(domain.WorkItemCancelled, now); err != nil {
					return err
				}
				if err := s.store.WorkItems().Update(ctx, item, expected); err != nil {
					return err
				}
				if err := s.store.WorkItems().ResolveBlockers(ctx, item.ID, now); err != nil {
					return err
				}
				if err := s.emit(ctx, item.WorkspaceID, domain.EventWorkItemUpdated,
					domain.AggregateWorkItem, item.ID, item.Version, nil,
					map[string]any{"status": string(item.Status), "record_kind": string(workItemRecordKind(item))}); err != nil {
					return err
				}
			}
			if err := s.cancelOpenDispatchesLocked(ctx, item); err != nil {
				return err
			}
			runs, err := s.store.Runs().ListByWorkItem(ctx, item.ID)
			if err != nil {
				return err
			}
			for _, run := range runs {
				if run == nil {
					continue
				}
				if !run.Status.IsTerminal() {
					target, action, shouldTransition, err := cancelRunDecision(run.Status)
					if err != nil {
						return err
					}
					if shouldTransition {
						if err := s.transitionRunLocked(ctx, run, target, nil); err != nil {
							return err
						}
					}
					runsToForward = append(runsToForward, cancelRunForward{runID: run.ID, action: action})
				}
				if err := s.expireRunApprovalsForCancellationLocked(ctx, goal, run, now); err != nil {
					return err
				}
			}
		}
		if !hasCoordinator {
			for _, turnKey := range turnKeys {
				if err := s.settleGovernanceTurnQuotaLocked(ctx, turnKey, true); err != nil {
					return err
				}
			}
		}
		if err := s.activityFor(ctx, goal.WorkspaceID, goal.RootWorkItemID,
			"goal.cancelled", "Goal 与任务控制线已取消"); err != nil {
			return err
		}
		cancelled = goal
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(cancelled.WorkspaceID)
	var postCommitErr error
	for _, forward := range runsToForward {
		if s.ControlForwarder != nil {
			s.ControlForwarder(context.WithoutCancel(ctx), forward.runID, forward.action)
		}
	}
	for _, turnKey := range turnKeys {
		if err := s.settleGovernanceTurnQuota(context.WithoutCancel(ctx), turnKey, true); err != nil {
			postCommitErr = errors.Join(postCommitErr, fmt.Errorf("settle cancelled Goal turn %s:%s:%d: %w",
				turnKey.GoalID, turnKey.TodoID, turnKey.TurnSeq, err))
		}
	}
	if hasCoordinator && len(turnKeys) > 0 {
		if err := s.clearCancelledGoalSettlementCheckpoint(context.WithoutCancel(ctx), cancelled.RootWorkItemID); err != nil {
			postCommitErr = errors.Join(postCommitErr, fmt.Errorf("clear cancelled Goal settlement checkpoint: %w", err))
		}
	}
	if postCommitErr != nil {
		return cancelled, &CommittedWithRecoveryError{
			Goal:           cancelled,
			RecoveryKind:   "goal_cancelled_cleanup",
			RootWorkItemID: cancelled.RootWorkItemID,
			Cause:          postCommitErr,
		}
	}
	return cancelled, nil
}

func (s *Service) transitionGoalAndCurrentTodo(ctx context.Context, goalID string, expectedVersion int, action string) (*domain.Goal, error) {
	var changed *domain.Goal
	startCoordinator := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		goal, err := s.store.Goals().Get(ctx, goalID)
		if err != nil {
			return err
		}
		if err := goal.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if (action == "resume" || action == "pause") && goal.Status == domain.GoalBlocked {
			return fmt.Errorf("%w: blocked Goal must be changed through WorkItem unblock", domain.ErrStateConflict)
		}
		now := time.Now().UTC()
		if goal.CurrentTodoID != "" {
			if err := s.transitionCurrentTodo(ctx, goal, action, now); err != nil {
				return err
			}
		}
		from := goal.Status
		switch action {
		case "pause":
			if err := goal.Pause(now); err != nil {
				return err
			}
		case "resume":
			if err := goal.Resume(now); err != nil {
				return err
			}
		case "cancel":
			if err := goal.Cancel(now); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unknown Goal action %q", domain.ErrValidation, action)
		}
		if err := s.store.Goals().Update(ctx, goal, expectedVersion); err != nil {
			return err
		}
		if err := s.emitGoalStateChanged(ctx, goal, from); err != nil {
			return err
		}
		if action == "resume" {
			var resumeErr error
			startCoordinator, resumeErr = s.resumeCoordinatorForGoalLocked(ctx, goal, now)
			if resumeErr != nil {
				return resumeErr
			}
		}
		changed = goal
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(changed.WorkspaceID)
	if action == "resume" {
		if _, recoverErr := s.recoverPendingSelfHealRuns(context.WithoutCancel(ctx), changed.RootWorkItemID); recoverErr != nil {
			log.Printf("goal: resume %s queued self-heal recovery failed: %v", changed.ID, recoverErr)
		}
	}
	if startCoordinator {
		if err := s.StartCoordinator(context.WithoutCancel(ctx), changed.RootWorkItemID); err != nil {
			log.Printf("goal: resume %s StartCoordinator 失败（durable checkpoint 由恢复循环兜底）: %v", changed.ID, err)
		}
	}
	return changed, nil
}

// cancelTodoLocked closes one non-terminal governance Todo and clears its
// claim in the same CAS. Goal cancellation enumerates every Todo rather than
// assuming CurrentTodoID is the only live intent; historical completed Todos
// remain immutable.
func (s *Service) cancelTodoLocked(ctx context.Context, goal *domain.Goal, todo *domain.Todo, now time.Time) error {
	if goal == nil || todo == nil || todo.Status.IsTerminal() {
		return nil
	}
	if todo.GoalID != goal.ID {
		return fmt.Errorf("%w: Goal cancellation found Todo outside Goal", domain.ErrStateConflict)
	}
	from := todo.Status
	hadClaim := todo.Claim != nil
	cancelled, err := s.store.Todos().Cancel(ctx, todo.ID, now, todo.Version)
	if err != nil {
		return err
	}
	if hadClaim {
		if err := s.emitTodoClaimChanged(ctx, goal.WorkspaceID, cancelled, "released", "", nil); err != nil {
			return err
		}
	}
	return s.emitTodoStateChanged(ctx, goal.WorkspaceID, cancelled, from)
}

func (s *Service) expireRunApprovalsForCancellationLocked(ctx context.Context, goal *domain.Goal,
	run *domain.ExecutionRun, now time.Time) error {
	if goal == nil || run == nil {
		return fmt.Errorf("%w: Goal cancellation approval sweep requires Goal and Run", domain.ErrValidation)
	}
	approvals, err := s.store.Runs().ListApprovals(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		if approval == nil || approval.Status != domain.ApprovalPending {
			continue
		}
		if err := approval.Expire("system:goal_cancelled", "Goal 已取消", now); err != nil {
			return err
		}
		if err := s.store.Runs().UpdateApproval(ctx, approval); err != nil {
			return err
		}
		if err := s.emit(ctx, goal.WorkspaceID, domain.EventApprovalExpired,
			domain.AggregateApproval, approval.ID, 1, nil,
			map[string]any{"kind": approval.Kind, "run_id": approval.RunID,
				"work_item_id": approval.WorkItemID, "goal_id": goal.ID,
				"resolved_by": "system:goal_cancelled", "reason": "Goal 已取消",
				"record_kind": string(domain.RecordKindTask)}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resumeCoordinatorForGoalLocked(ctx context.Context, goal *domain.Goal, now time.Time) (bool, error) {
	state, err := s.store.TaskCoordinators().GetState(ctx, goal.RootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state.Status == domain.CoordinatorBlocked {
		return false, fmt.Errorf("%w: blocked Coordinator must be resumed through WorkItem unblock", domain.ErrStateConflict)
	}
	if state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled {
		return false, fmt.Errorf("%w: terminal Coordinator cannot resume Goal", domain.ErrStateConflict)
	}
	if state.Status == domain.CoordinatorWaitingUser {
		return false, nil
	}
	dispatches, err := s.store.Dispatches().ListByWorkItem(ctx, goal.RootWorkItemID)
	if err != nil {
		return false, err
	}
	for _, dispatch := range dispatches {
		if dispatch != nil && dispatch.Status == domain.DispatchCollecting {
			// The queued settlement wake is the exact continuation. Leave the
			// observation checkpoint untouched so the scheduler can consume it
			// instead of racing a generic recovery turn.
			_, err := s.ensureCollectingDispatchWakeups(ctx, goal.RootWorkItemID)
			if err != nil {
				return false, err
			}
			// A summary Run may already own the Coordinator while the Dispatch is
			// collecting. Preserve that terminal replay path; without a current Run,
			// the reconstructed settlement wake is the sole continuation.
			return state.CurrentRunID != "", nil
		}
	}
	if state.CurrentRunID != "" {
		return true, nil
	}
	active, err := s.taskTreeHasActiveRuns(ctx, goal.RootWorkItemID)
	if err != nil {
		return false, err
	}
	if state.Status == domain.CoordinatorRunning && active &&
		!coordinatorRetryPending(state) && !coordinatorSettlementPending(state) {
		return false, nil
	}
	plan, err := s.store.Plans().ActiveByWorkItem(ctx, goal.RootWorkItemID)
	if err != nil {
		return false, err
	}
	if plan != nil && plan.Status == domain.PlanWaiting {
		// A pending approval/defer Plan already owns the next action. Resume
		// re-enables its gate; it must not create a competing Coordinator turn.
		return false, nil
	}

	expected := state.Version
	if coordinatorRetryPending(state) || coordinatorSettlementPending(state) {
		state.NextActionAt = nil
	} else {
		state.Status = domain.CoordinatorQueued
		state.Phase = "recovering"
		state.CurrentAction = "recover"
		state.NextActionAt = nil
		if state.Data == nil {
			state.Data = map[string]any{}
		}
		state.Data["control_action"] = "recover"
	}
	state.BlockerCode, state.BlockerMessage = "", ""
	if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		return false, err
	}
	state.Version = expected + 1
	if err := s.appendCoordinatorEvent(ctx, state, goal.RootWorkItemID,
		domain.EventCoordinatorRecoveryStarted, "Goal 已恢复，Coordinator 继续推进",
		"", state.CoordinatorAgentID, state.Attempt, "goal_resumed", nil,
		map[string]any{"stage": "recovery", "next_action": state.CurrentAction}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) transitionCurrentTodo(ctx context.Context, goal *domain.Goal, action string, now time.Time) error {
	todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		return err
	}
	if todo.GoalID != goal.ID {
		return fmt.Errorf("%w: current Todo does not belong to Goal", domain.ErrStateConflict)
	}
	if todo.Status.IsTerminal() {
		return nil
	}
	from := todo.Status
	expected := todo.Version
	switch action {
	case "pause":
		if todo.Status == domain.TodoWaiting {
			return nil
		}
		if err := todo.Transition(domain.TodoWaiting, now); err != nil {
			return err
		}
	case "resume":
		if todo.Status != domain.TodoWaiting {
			return domain.ErrStateConflict
		}
		if handoff, targetAgentID, handoffErr := s.latestTransferredHandoffForTodo(ctx, goal, todo); handoffErr != nil {
			return handoffErr
		} else if handoff != nil {
			if todo.Claim == nil || todo.Claim.OwnerAgentID != targetAgentID ||
				todo.ClaimVersion != handoff.TargetClaimVersion || todo.Claim.Version != handoff.TargetClaimVersion {
				return fmt.Errorf("%w: transferred Handoff claim was replaced during Goal pause", domain.ErrStateConflict)
			}
			if !todo.Claim.ExpiresAt.After(now) {
				renewed, renewErr := s.renewHandoffTodoClaimLocked(ctx, goal, todo, handoff, targetAgentID)
				if renewErr != nil {
					return renewErr
				}
				todo = renewed
				expected = todo.Version
			}
			if err := todo.Transition(domain.TodoClaimed, now); err != nil {
				return err
			}
		} else if todo.Claim != nil && todo.Claim.ExpiresAt.After(now) {
			if err := todo.Transition(domain.TodoClaimed, now); err != nil {
				return err
			}
		} else {
			expiredOwnerID := ""
			if todo.Claim != nil {
				expiredOwnerID = todo.Claim.OwnerAgentID
				released, err := s.store.Todos().Release(ctx, todo.ID, todo.Claim.OwnerAgentID, now, expected)
				if err != nil {
					return err
				}
				todo = released
				expected = todo.Version
				if err := s.emitTodoClaimChanged(ctx, goal.WorkspaceID, todo, "expired", "", nil); err != nil {
					return err
				}
			}
			if todo.LastTurnSeq > 0 && expiredOwnerID != "" {
				beforeClaim := todo.Status
				claimed, err := s.store.Todos().Claim(ctx, todo.ID, expiredOwnerID,
					now, now.Add(governancePlanClaimTTL), todo.Version)
				if err != nil {
					return err
				}
				if beforeClaim != claimed.Status {
					if err := s.emitTodoStateChanged(ctx, goal.WorkspaceID, claimed, beforeClaim); err != nil {
						return err
					}
				}
				return s.emitTodoClaimChanged(ctx, goal.WorkspaceID, claimed, "claimed", expiredOwnerID, &claimed.Claim.ExpiresAt)
			}
			if err := todo.Transition(domain.TodoPending, now); err != nil {
				return err
			}
		}
	case "cancel":
		hadClaim := todo.Claim != nil
		cancelled, err := s.store.Todos().Cancel(ctx, todo.ID, now, expected)
		if err != nil {
			return err
		}
		if hadClaim {
			if err := s.emitTodoClaimChanged(ctx, goal.WorkspaceID, cancelled, "released", "", nil); err != nil {
				return err
			}
		}
		return s.emitTodoStateChanged(ctx, goal.WorkspaceID, cancelled, from)
	default:
		return fmt.Errorf("%w: unknown Todo action %q", domain.ErrValidation, action)
	}
	if err := s.store.Todos().Update(ctx, todo, expected); err != nil {
		return err
	}
	return s.emitTodoStateChanged(ctx, goal.WorkspaceID, todo, from)
}

// blockCurrentGovernanceLocked keeps the native governance projections aligned
// with a root Task/Coordinator blocker. It deliberately does not allocate a
// TurnReceipt or advance last_turn_seq: a control-plane failure before the next
// admissible decision is a lifecycle transition, not a fabricated turn.
//
// The caller owns the surrounding transaction. Missing governance rows are
// tolerated for legacy Tasks, while every existing current Todo is made
// claim-free so a later explicit unblock cannot revive stale ownership.
func (s *Service) blockCurrentGovernanceLocked(ctx context.Context, rootWorkItemID string, now time.Time) error {
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, rootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if goal.Status.IsTerminal() || goal.Status == domain.GoalDraft {
		return nil
	}

	if goal.CurrentTodoID != "" {
		todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID {
			return fmt.Errorf("%w: current Todo does not belong to Goal", domain.ErrStateConflict)
		}
		if !todo.Status.IsTerminal() {
			if todo.Status != domain.TodoBlocked {
				from := todo.Status
				expected := todo.Version
				if err := todo.Transition(domain.TodoBlocked, now); err != nil {
					return err
				}
				if err := s.store.Todos().Update(ctx, todo, expected); err != nil {
					return err
				}
				if err := s.emitTodoStateChanged(ctx, goal.WorkspaceID, todo, from); err != nil {
					return err
				}
			}
			if todo.Claim != nil {
				released, err := s.store.Todos().Release(ctx, todo.ID, todo.Claim.OwnerAgentID, now, todo.Version)
				if err != nil {
					return err
				}
				todo = released
				if err := s.emitTodoClaimChanged(ctx, goal.WorkspaceID, todo, "released", "", nil); err != nil {
					return err
				}
			}
		}
	}

	if goal.Status == domain.GoalBlocked {
		return nil
	}
	if goal.Status != domain.GoalActive && goal.Status != domain.GoalWaiting {
		return fmt.Errorf("%w: Goal status %s cannot follow Coordinator blocker", domain.ErrStateConflict, goal.Status)
	}
	from := goal.Status
	expected := goal.Version
	goal.Phase = "blocked"
	if err := goal.Transition(domain.GoalBlocked, now); err != nil {
		return err
	}
	if err := s.store.Goals().Update(ctx, goal, expected); err != nil {
		return err
	}
	return s.emitGoalStateChanged(ctx, goal, from)
}

// resumeCurrentGovernanceLocked is the inverse of a root unblock. It reopens
// only non-terminal native governance state, clears any stale claim, and
// returns the current Todo to pending without changing its turn watermark.
func (s *Service) resumeCurrentGovernanceLocked(ctx context.Context, rootWorkItemID string, now time.Time) error {
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, rootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if goal.Status.IsTerminal() || goal.Status == domain.GoalDraft {
		return nil
	}

	if goal.CurrentTodoID != "" {
		todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID {
			return fmt.Errorf("%w: current Todo does not belong to Goal", domain.ErrStateConflict)
		}
		if !todo.Status.IsTerminal() && (goal.Status == domain.GoalBlocked || todo.Status == domain.TodoBlocked) {
			if todo.Status == domain.TodoRunning {
				from := todo.Status
				expected := todo.Version
				if err := todo.Transition(domain.TodoBlocked, now); err != nil {
					return err
				}
				if err := s.store.Todos().Update(ctx, todo, expected); err != nil {
					return err
				}
				if err := s.emitTodoStateChanged(ctx, goal.WorkspaceID, todo, from); err != nil {
					return err
				}
			}
			if todo.Claim != nil {
				beforeRelease := todo.Status
				released, err := s.store.Todos().Release(ctx, todo.ID, todo.Claim.OwnerAgentID, now, todo.Version)
				if err != nil {
					return err
				}
				todo = released
				if beforeRelease != todo.Status {
					if err := s.emitTodoStateChanged(ctx, goal.WorkspaceID, todo, beforeRelease); err != nil {
						return err
					}
				}
				if err := s.emitTodoClaimChanged(ctx, goal.WorkspaceID, todo, "released", "", nil); err != nil {
					return err
				}
			}
			if todo.Status != domain.TodoPending {
				from := todo.Status
				expected := todo.Version
				if err := todo.Transition(domain.TodoPending, now); err != nil {
					return err
				}
				if err := s.store.Todos().Update(ctx, todo, expected); err != nil {
					return err
				}
				if err := s.emitTodoStateChanged(ctx, goal.WorkspaceID, todo, from); err != nil {
					return err
				}
			}
		}
	}

	if goal.Status != domain.GoalBlocked {
		return nil
	}
	from := goal.Status
	expected := goal.Version
	goal.Phase = "execution"
	if err := goal.Resume(now); err != nil {
		return err
	}
	if err := s.store.Goals().Update(ctx, goal, expected); err != nil {
		return err
	}
	return s.emitGoalStateChanged(ctx, goal, from)
}

func (s *Service) ClaimTodo(ctx context.Context, todoID, ownerAgentID string, expectedVersion int, expiresAt time.Time) (*domain.Todo, error) {
	var claimed *domain.Todo
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		current, err := s.store.Todos().Get(ctx, todoID)
		if err != nil {
			return err
		}
		goal, err := s.store.Goals().Get(ctx, current.GoalID)
		if err != nil {
			return err
		}
		if goal.Status != domain.GoalActive {
			return domain.ErrStateConflict
		}
		now := time.Now().UTC()
		claimed, err = s.store.Todos().Claim(ctx, todoID, ownerAgentID, now, expiresAt.UTC(), expectedVersion)
		if err != nil {
			return err
		}
		workspaceID = goal.WorkspaceID
		if current.Status != claimed.Status {
			if err := s.emitTodoStateChanged(ctx, workspaceID, claimed, current.Status); err != nil {
				return err
			}
		}
		return s.emitTodoClaimChanged(ctx, workspaceID, claimed, "claimed", ownerAgentID, &claimed.Claim.ExpiresAt)
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	return claimed, nil
}

func (s *Service) ReleaseTodo(ctx context.Context, todoID, ownerAgentID string, expectedVersion int) (*domain.Todo, error) {
	var released *domain.Todo
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		current, err := s.store.Todos().Get(ctx, todoID)
		if err != nil {
			return err
		}
		goal, err := s.store.Goals().Get(ctx, current.GoalID)
		if err != nil {
			return err
		}
		if goal.Status.IsTerminal() {
			return domain.ErrStateConflict
		}
		released, err = s.store.Todos().Release(ctx, todoID, ownerAgentID, time.Now().UTC(), expectedVersion)
		if err != nil {
			return err
		}
		workspaceID = goal.WorkspaceID
		if current.Status != released.Status {
			if err := s.emitTodoStateChanged(ctx, workspaceID, released, current.Status); err != nil {
				return err
			}
		}
		return s.emitTodoClaimChanged(ctx, workspaceID, released, "released", "", nil)
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	return released, nil
}

type AdmitTurnParams struct {
	GoalID              string
	TodoID              string
	OwnerAgentID        string
	ExpectedTodoVersion int
	Attempt             int
	SchemaVersion       string
	InputSnapshotDigest string
	AdmissionClientKey  string
}

func (s *Service) AdmitTurn(ctx context.Context, p AdmitTurnParams) (*domain.TurnReceiptHeader, error) {
	var admitted *domain.TurnReceiptHeader
	var conflictHeader *domain.TurnReceiptHeader
	var workspaceID string
	created := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		existing, err := s.store.TurnReceipts().GetHeaderByClientKey(ctx, p.GoalID, p.TodoID, p.AdmissionClientKey)
		if err == nil {
			if existing.Attempt != p.Attempt || existing.SchemaVersion != p.SchemaVersion ||
				existing.InputSnapshotDigest != p.InputSnapshotDigest {
				conflictHeader = existing
				return domain.ErrIdempotencyConflict
			}
			admitted = existing
			goal, getErr := s.store.Goals().Get(ctx, p.GoalID)
			if getErr != nil {
				return getErr
			}
			workspaceID = goal.WorkspaceID
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		goal, err := s.store.Goals().Get(ctx, p.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(ctx, p.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID || todo.Version != p.ExpectedTodoVersion {
			return domain.ErrVersionConflict
		}
		now := time.Now().UTC()
		header := &domain.TurnReceiptHeader{
			TurnKey: domain.TurnKey{
				GoalID: goal.ID, TodoID: todo.ID, TurnSeq: todo.LastTurnSeq + 1,
			},
			Attempt:             p.Attempt,
			SchemaVersion:       p.SchemaVersion,
			InputSnapshotDigest: p.InputSnapshotDigest,
			AdmissionClientKey:  p.AdmissionClientKey,
			CanonicalDigest:     emptySHA256Digest,
			CreatedAt:           now,
		}
		digest, err := ComputeTurnReceiptHeaderDigest(header)
		if err != nil {
			return err
		}
		header.CanonicalDigest = digest
		admitted, err = s.store.TurnReceipts().Admit(ctx, header, p.OwnerAgentID, p.ExpectedTodoVersion)
		if err != nil {
			return err
		}
		freshTodo, err := s.store.Todos().Get(ctx, todo.ID)
		if err != nil {
			return err
		}
		workspaceID = goal.WorkspaceID
		if err := s.emitTodoStateChanged(ctx, workspaceID, freshTodo, todo.Status); err != nil {
			return err
		}
		if err := s.emitTurnReceiptAppended(ctx, workspaceID, freshTodo.Version, admitted, nil); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		if conflictHeader != nil {
			goal, getErr := s.store.Goals().Get(ctx, p.GoalID)
			if getErr != nil {
				return nil, errors.Join(err, getErr)
			}
			if outcomeErr := s.store.InTx(ctx, func(txctx context.Context) error {
				return s.emitTurnReceiptOutcome(txctx, goal.WorkspaceID, 0, conflictHeader, nil, "conflict")
			}); outcomeErr != nil {
				return nil, errors.Join(err, outcomeErr)
			}
		}
		return nil, err
	}
	if !created && admitted != nil {
		if err := s.store.InTx(ctx, func(txctx context.Context) error {
			return s.emitTurnReceiptOutcome(txctx, workspaceID, 0, admitted, nil, "replayed")
		}); err != nil {
			return nil, err
		}
	}
	if created {
		s.notifier.Notify(workspaceID)
	}
	return admitted, nil
}

func (s *Service) AppendTurnReceiptPhase(ctx context.Context, phase *domain.TurnReceiptPhase) (*domain.TurnReceiptPhase, error) {
	if phase == nil {
		return nil, fmt.Errorf("%w: turn receipt phase required", domain.ErrValidation)
	}
	working := *phase
	working.RunIDs = append([]string(nil), phase.RunIDs...)
	working.QuotaReservationKeys = append([]string(nil), phase.QuotaReservationKeys...)
	working.Evidence = append([]domain.GovernanceEvidenceItem(nil), phase.Evidence...)
	var appended *domain.TurnReceiptPhase
	var conflictPhase *domain.TurnReceiptPhase
	var workspaceID string
	created := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		existing, err := s.store.TurnReceipts().GetPhase(ctx, working.TurnKey, working.PhaseSeq)
		if err == nil {
			working.CreatedAt = existing.CreatedAt
			working.CanonicalDigest = emptySHA256Digest
			digest, digestErr := ComputeTurnReceiptPhaseDigest(&working)
			if digestErr != nil {
				return digestErr
			}
			if digest != existing.CanonicalDigest {
				conflictPhase = existing
				return domain.ErrIdempotencyConflict
			}
			appended = existing
			goal, getErr := s.store.Goals().Get(ctx, working.TurnKey.GoalID)
			if getErr != nil {
				return getErr
			}
			workspaceID = goal.WorkspaceID
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		goal, err := s.store.Goals().Get(ctx, working.TurnKey.GoalID)
		if err != nil {
			return err
		}
		working.CreatedAt = time.Now().UTC()
		working.CanonicalDigest = emptySHA256Digest
		digest, err := ComputeTurnReceiptPhaseDigest(&working)
		if err != nil {
			return err
		}
		working.CanonicalDigest = digest
		// Delivery Brief captures are evidence-grade only when their immutable
		// source watermark still matches the same transaction's authoritative
		// read model. The SQL repository also checks scope, but cannot rebuild
		// the Brief without depending on the application layer.
		for _, evidence := range working.Evidence {
			if evidence.SourceKind != domain.EvidenceSourceDeliveryBrief {
				continue
			}
			if err := s.validateGovernanceEvidenceReferenceTx(ctx, goal.ID, working.TurnKey.TodoID, evidence); err != nil {
				return err
			}
		}
		appended, err = s.store.TurnReceipts().AppendPhase(ctx, &working)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(ctx, working.TurnKey.TodoID)
		if err != nil {
			return err
		}
		workspaceID = goal.WorkspaceID
		if err := s.emitTurnReceiptAppended(ctx, workspaceID, todo.Version, nil, appended); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		if conflictPhase != nil {
			goal, getErr := s.store.Goals().Get(ctx, working.TurnKey.GoalID)
			if getErr != nil {
				return nil, errors.Join(err, getErr)
			}
			if outcomeErr := s.store.InTx(ctx, func(txctx context.Context) error {
				return s.emitTurnReceiptOutcome(txctx, goal.WorkspaceID, 0, nil, conflictPhase, "conflict")
			}); outcomeErr != nil {
				return nil, errors.Join(err, outcomeErr)
			}
		}
		return nil, err
	}
	if !created && appended != nil {
		if err := s.store.InTx(ctx, func(txctx context.Context) error {
			return s.emitTurnReceiptOutcome(txctx, workspaceID, 0, nil, appended, "replayed")
		}); err != nil {
			return nil, err
		}
	}
	if created {
		s.notifier.Notify(workspaceID)
	}
	*phase = *appended
	return phase, nil
}

func (s *Service) emitGoalStateChanged(ctx context.Context, goal *domain.Goal, from domain.GoalStatus) error {
	return s.emit(ctx, goal.WorkspaceID, domain.EventGoalStateChanged,
		domain.AggregateGoal, goal.ID, goal.Version, nil, map[string]any{
			"from_state": string(from), "to_state": string(goal.Status), "version": goal.Version,
		})
}

func (s *Service) emitGoalEvidenceAdded(ctx context.Context, goal *domain.Goal,
	evidence domain.GovernanceEvidenceItem) error {
	if goal == nil {
		return fmt.Errorf("%w: goal evidence event requires Goal", domain.ErrValidation)
	}
	return s.emit(ctx, goal.WorkspaceID, domain.EventGoalEvidenceAdded,
		domain.AggregateGoal, goal.ID, goal.Version, nil, map[string]any{
			"goal_id": goal.ID, "source_kind": string(evidence.SourceKind),
			"source_id": evidence.SourceID, "verification": string(evidence.Verification),
			"summary":     evidence.Summary,
			"recorded_at": evidence.RecordedAt.UTC().Format(time.RFC3339Nano),
		})
}

func (s *Service) emitTodoCreated(ctx context.Context, workspaceID string, todo *domain.Todo) error {
	return s.emit(ctx, workspaceID, domain.EventTodoCreated,
		domain.AggregateTodo, todo.ID, todo.Version, nil, map[string]any{
			"goal_id": todo.GoalID, "class": string(todo.Class), "state": string(todo.Status), "version": todo.Version,
		})
}

func (s *Service) emitTodoStateChanged(ctx context.Context, workspaceID string, todo *domain.Todo, from domain.TodoStatus) error {
	data := map[string]any{
		"goal_id": todo.GoalID, "from_state": string(from), "to_state": string(todo.Status), "version": todo.Version,
	}
	if todo.Status == domain.TodoCompleted && todo.CompletionTurnKey != nil {
		data["completion_turn_key"] = todo.CompletionTurnKey
		data["completion_evidence_id"] = todo.CompletionEvidenceID
	}
	return s.emit(ctx, workspaceID, domain.EventTodoStateChanged,
		domain.AggregateTodo, todo.ID, todo.Version, nil, data)
}

func (s *Service) emitTodoClaimChanged(ctx context.Context, workspaceID string, todo *domain.Todo, state, ownerID string, expiresAt *time.Time) error {
	data := map[string]any{
		"goal_id": todo.GoalID, "claim_version": todo.ClaimVersion, "claim_state": state,
	}
	if ownerID != "" {
		data["owner_agent_id"] = ownerID
	}
	if expiresAt != nil {
		data["expires_at"] = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	return s.emit(ctx, workspaceID, domain.EventTodoClaimChanged,
		domain.AggregateTodo, todo.ID, todo.Version, nil, data)
}

func (s *Service) emitTurnReceiptAppended(ctx context.Context, workspaceID string, todoVersion int, header *domain.TurnReceiptHeader, phase *domain.TurnReceiptPhase) error {
	key, data, err := turnReceiptEventData(header, phase)
	if err != nil {
		return err
	}
	return s.emit(ctx, workspaceID, domain.EventTurnReceiptAppended,
		domain.AggregateTodo, key.TodoID, todoVersion, nil, data)
}

func (s *Service) emitTurnReceiptOutcome(ctx context.Context, workspaceID string,
	todoVersion int, header *domain.TurnReceiptHeader, phase *domain.TurnReceiptPhase, outcome string) error {
	if outcome != "replayed" && outcome != "conflict" {
		return fmt.Errorf("%w: invalid receipt outcome %q", domain.ErrValidation, outcome)
	}
	key, data, err := turnReceiptEventData(header, phase)
	if err != nil {
		return err
	}
	if todoVersion < 1 {
		todo, err := s.store.Todos().Get(ctx, key.TodoID)
		if err != nil {
			return err
		}
		todoVersion = todo.Version
	}
	data["outcome"] = outcome
	return s.emit(ctx, workspaceID, domain.EventTurnReceiptAppended,
		domain.AggregateTodo, key.TodoID, todoVersion, nil, data)
}

func turnReceiptEventData(header *domain.TurnReceiptHeader, phase *domain.TurnReceiptPhase) (domain.TurnKey, map[string]any, error) {
	if (header == nil) == (phase == nil) {
		return domain.TurnKey{}, nil, fmt.Errorf("%w: receipt event requires exactly one header or phase", domain.ErrValidation)
	}
	data := map[string]any{}
	var key domain.TurnKey
	if header != nil {
		key = header.TurnKey
		data["record_kind"] = "header"
		data["schema_version"] = header.SchemaVersion
		data["input_snapshot_digest"] = header.InputSnapshotDigest
		data["digest"] = header.CanonicalDigest
	} else {
		key = phase.TurnKey
		data["record_kind"] = "phase"
		data["phase_seq"] = phase.PhaseSeq
		data["phase"] = string(phase.Phase)
		data["digest"] = phase.CanonicalDigest
		if phase.PhaseSeq == 2 {
			if valid, ok := phase.Payload["valid"].(bool); ok {
				data["valid"] = valid
			}
			for _, field := range []string{"error_code", "path"} {
				if value, ok := phase.Payload[field].(string); ok && value != "" {
					data[field] = value
				}
			}
		}
	}
	data["goal_id"] = key.GoalID
	data["turn_seq"] = key.TurnSeq
	return key, data, nil
}

type GovernanceConsistencyIssue struct {
	RootWorkItemID string `json:"root_work_item_id"`
	GoalID         string `json:"goal_id,omitempty"`
	Code           string `json:"code"`
	Message        string `json:"message"`
}

type GovernanceStateRebuildResult struct {
	CreatedGoals int                          `json:"created_goals"`
	CreatedTodos int                          `json:"created_todos"`
	Issues       []GovernanceConsistencyIssue `json:"issues"`
}

// RebuildGovernanceState idempotently creates missing Goal/Todo state for
// eligible root Tasks. It never overwrites an inconsistent intent and never
// creates a Plan, Run, lease or runtime side effect.
func (s *Service) RebuildGovernanceState(ctx context.Context, workspaceID string) (GovernanceStateRebuildResult, error) {
	result := GovernanceStateRebuildResult{Issues: []GovernanceConsistencyIssue{}}
	cursor := ""
	for {
		roots, next, err := s.store.WorkItems().List(ctx, workspaceID, WorkItemFilter{
			RecordKind: domain.RecordKindTask,
			ParentID:   "none",
			Cursor:     cursor,
			Limit:      200,
		})
		if err != nil {
			return result, err
		}
		for _, root := range roots {
			one, ensureErr := s.ensureGovernanceState(ctx, root)
			if ensureErr != nil {
				return result, ensureErr
			}
			result.CreatedGoals += one.CreatedGoals
			result.CreatedTodos += one.CreatedTodos
			result.Issues = append(result.Issues, one.Issues...)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return result, nil
}

// EnsureGovernanceState is the root-Task event hook. It applies the same
// idempotent state creation logic as startup rebuild without scanning a workspace.
func (s *Service) EnsureGovernanceState(ctx context.Context, rootWorkItemID string) (GovernanceStateRebuildResult, error) {
	root, err := s.store.WorkItems().Get(ctx, rootWorkItemID)
	if err != nil {
		return GovernanceStateRebuildResult{}, err
	}
	return s.ensureGovernanceState(ctx, root)
}

// ensureCoordinatorGovernanceReady repairs a missing native Goal/Todo when
// the root contract is sufficient, then validates the single governance chain
// before any system Coordinator Run may be created. It never invents missing
// acceptance criteria or falls back to an ordinary, ungoverned Plan.
func (s *Service) ensureCoordinatorGovernanceReady(ctx context.Context, rootWorkItemID string) (*domain.Goal, error) {
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, rootWorkItemID)
	needsRebuild := errors.Is(err, domain.ErrNotFound) ||
		(err == nil && goal.Status == domain.GoalDraft && goal.CurrentTodoID == "")
	if needsRebuild {
		result, ensureErr := s.EnsureGovernanceState(ctx, rootWorkItemID)
		if ensureErr != nil {
			return nil, fmt.Errorf("governance_state_rebuild_failed: %w", ensureErr)
		}
		if len(result.Issues) > 0 {
			issue := result.Issues[0]
			return nil, fmt.Errorf("%w: %s: %s", domain.ErrStateConflict, issue.Code, issue.Message)
		}
		goal, err = s.store.Goals().GetByRootWorkItem(ctx, rootWorkItemID)
	}
	if err != nil {
		return nil, err
	}
	if goal.Status == domain.GoalDraft {
		return nil, fmt.Errorf("%w: native Goal remained draft after rebuild", domain.ErrStateConflict)
	}
	if !goal.Status.IsTerminal() && goal.CurrentTodoID == "" {
		return nil, fmt.Errorf("%w: native Goal has no current Todo", domain.ErrStateConflict)
	}
	if goal.CurrentTodoID != "" {
		todo, todoErr := s.store.Todos().Get(ctx, goal.CurrentTodoID)
		if todoErr != nil {
			return nil, todoErr
		}
		if todo.GoalID != goal.ID {
			return nil, fmt.Errorf("%w: native Goal current Todo has a different owner", domain.ErrStateConflict)
		}
	}
	return goal, nil
}

func (s *Service) ensureGovernanceState(ctx context.Context, root *domain.WorkItem) (GovernanceStateRebuildResult, error) {
	result := GovernanceStateRebuildResult{Issues: []GovernanceConsistencyIssue{}}
	// A blocked root is an operator-visible stop signal. Rebuild may repair an
	// existing Goal/Todo projection into blocked, but it must never create a
	// fresh active Todo from that root (the old implementation treated blocked
	// as non-terminal and accidentally reactivated it).
	if root != nil && root.Status == domain.WorkItemBlocked {
		goal, getErr := s.store.Goals().GetByRootWorkItem(ctx, root.ID)
		if errors.Is(getErr, domain.ErrNotFound) {
			if len(root.AcceptanceCriteria) == 0 {
				result.Issues = append(result.Issues, GovernanceConsistencyIssue{
					RootWorkItemID: root.ID, Code: "acceptance_contract_missing",
					Message: "blocked root Task has no acceptance criteria; governance state was not invented",
				})
				return result, nil
			}
			var createdGoal, createdTodo bool
			var materializeErr error
			goal, _, createdGoal, createdTodo, materializeErr = s.storeBlockedGovernanceState(ctx, root, nil)
			if materializeErr != nil {
				result.Issues = append(result.Issues, GovernanceConsistencyIssue{
					RootWorkItemID: root.ID, Code: "blocked_root_governance_create_failed",
					Message: materializeErr.Error(),
				})
				return result, nil
			}
			if createdGoal {
				result.CreatedGoals++
			}
			if createdTodo {
				result.CreatedTodos++
			}
		} else if getErr != nil {
			return result, getErr
		}
		if goal != nil && goal.Status != domain.GoalCompleted && goal.Status != domain.GoalCancelled &&
			goal.Status != domain.GoalBlocked {
			if blockErr := s.store.InTx(ctx, func(txctx context.Context) error {
				return s.blockCurrentGovernanceLocked(txctx, root.ID, time.Now().UTC())
			}); blockErr != nil {
				result.Issues = append(result.Issues, GovernanceConsistencyIssue{
					RootWorkItemID: root.ID, GoalID: goal.ID, Code: "blocked_root_governance_sync_failed",
					Message: blockErr.Error(),
				})
				return result, nil
			}
		}
		if goal != nil && (goal.Status == domain.GoalBlocked || goal.Status == domain.GoalDraft) &&
			goal.CurrentTodoID == "" && len(root.AcceptanceCriteria) > 0 {
			goalID := goal.ID
			var materializeErr error
			var createdTodo bool
			goal, _, _, createdTodo, materializeErr = s.storeBlockedGovernanceState(ctx, root, goal)
			if materializeErr != nil {
				result.Issues = append(result.Issues, GovernanceConsistencyIssue{
					RootWorkItemID: root.ID, GoalID: goalID, Code: "blocked_root_todo_create_failed",
					Message: materializeErr.Error(),
				})
				return result, nil
			}
			if createdTodo {
				result.CreatedTodos++
			}
		}
		if issue, inconsistent, checkErr := s.CheckGovernanceConsistency(ctx, root.ID); checkErr != nil {
			return result, checkErr
		} else if inconsistent {
			result.Issues = append(result.Issues, *issue)
		}
		return result, nil
	}
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, root.ID)
	if errors.Is(err, domain.ErrNotFound) {
		if len(root.AcceptanceCriteria) == 0 {
			result.Issues = append(result.Issues, GovernanceConsistencyIssue{
				RootWorkItemID: root.ID,
				Code:           "acceptance_contract_missing",
				Message:        "root Task has no acceptance criteria; governance state was not invented",
			})
			return result, nil
		}
		var created bool
		goal, created, err = s.createGoal(ctx, root.WorkspaceID, CreateGoalParams{
			RootWorkItemID: root.ID, Objective: governanceObjectiveFromRoot(root),
			AcceptanceContract: append([]string(nil), root.AcceptanceCriteria...),
		})
		if err != nil {
			result.Issues = append(result.Issues, GovernanceConsistencyIssue{
				RootWorkItemID: root.ID, Code: "goal_create_failed", Message: err.Error(),
			})
			return result, nil
		}
		if created {
			result.CreatedGoals++
		}
	} else if err != nil {
		return result, err
	}

	if goal.Status == domain.GoalDraft && goal.CurrentTodoID == "" && !root.Status.IsTerminal() {
		objective := governanceObjectiveFromRoot(root)
		if goal.Objective != objective || !slices.Equal(goal.AcceptanceContract, root.AcceptanceCriteria) {
			expected := goal.Version
			goal.Objective = objective
			goal.AcceptanceContract = append([]string(nil), root.AcceptanceCriteria...)
			goal.Version++
			goal.UpdatedAt = time.Now().UTC()
			if err := s.store.Goals().Update(ctx, goal, expected); err != nil {
				result.Issues = append(result.Issues, GovernanceConsistencyIssue{
					RootWorkItemID: root.ID, GoalID: goal.ID,
					Code: "goal_intent_refresh_failed", Message: err.Error(),
				})
				return result, nil
			}
		}
		started, startErr := s.StartGoal(ctx, goal.ID, goal.Version)
		if startErr != nil {
			if errors.Is(startErr, domain.ErrStateConflict) || errors.Is(startErr, domain.ErrVersionConflict) {
				fresh, getErr := s.store.Goals().Get(ctx, goal.ID)
				if getErr == nil && fresh.Status == domain.GoalActive && fresh.CurrentTodoID != "" {
					return result, nil
				}
			}
			result.Issues = append(result.Issues, GovernanceConsistencyIssue{
				RootWorkItemID: root.ID, GoalID: goal.ID,
				Code: "governance_todo_create_failed", Message: startErr.Error(),
			})
			return result, nil
		}
		goal = started
		result.CreatedTodos++
	}
	if issue, inconsistent, checkErr := s.CheckGovernanceConsistency(ctx, root.ID); checkErr != nil {
		return result, checkErr
	} else if inconsistent {
		result.Issues = append(result.Issues, *issue)
	}
	return result, nil
}

// CheckGovernanceConsistency computes inconsistency from current authoritative rows.
// The query is read-only: a mismatch is returned to operators, never repaired
// by silently overwriting Goal/Todo or WorkItem state.
func (s *Service) CheckGovernanceConsistency(ctx context.Context, rootWorkItemID string) (*GovernanceConsistencyIssue, bool, error) {
	root, err := s.store.WorkItems().Get(ctx, rootWorkItemID)
	if err != nil {
		return nil, false, err
	}
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, rootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return &GovernanceConsistencyIssue{
			RootWorkItemID: root.ID, Code: "goal_missing", Message: "root Task has no governance Goal",
		}, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	issue := func(code, message string) (*GovernanceConsistencyIssue, bool, error) {
		return &GovernanceConsistencyIssue{
			RootWorkItemID: root.ID, GoalID: goal.ID, Code: code, Message: message,
		}, true, nil
	}
	if goal.WorkspaceID != root.WorkspaceID || goal.RootWorkItemID != root.ID {
		return issue("root_binding_inconsistent", "Goal root/workspace binding differs from WorkItem")
	}
	if goal.Objective != governanceObjectiveFromRoot(root) || !slices.Equal(goal.AcceptanceContract, root.AcceptanceCriteria) {
		return issue("intent_inconsistent", "Goal objective/acceptance differs from root Task source")
	}
	if goal.Status == domain.GoalActive && goal.CurrentTodoID == "" {
		return issue("current_todo_missing", "active Goal has no current Todo")
	}
	var currentTodo *domain.Todo
	if goal.CurrentTodoID != "" {
		todo, getErr := s.store.Todos().Get(ctx, goal.CurrentTodoID)
		if errors.Is(getErr, domain.ErrNotFound) {
			return issue("current_todo_missing", "Goal current Todo cannot be read")
		}
		if getErr != nil {
			return nil, false, getErr
		}
		if todo.GoalID != goal.ID {
			return issue("current_todo_scope_inconsistent", "Goal current Todo belongs to another Goal")
		}
		currentTodo = todo
	}
	coordinator, coordinatorErr := s.store.TaskCoordinators().GetState(ctx, root.ID)
	if coordinatorErr != nil && !errors.Is(coordinatorErr, domain.ErrNotFound) {
		return nil, false, coordinatorErr
	}
	if root.Status == domain.WorkItemBlocked && goal.Status != domain.GoalBlocked {
		return issue("blocked_state_inconsistent", "blocked root Task has a non-blocked Goal")
	}
	if coordinatorErr == nil && coordinator.Status == domain.CoordinatorBlocked && goal.Status != domain.GoalBlocked {
		return issue("blocked_state_inconsistent", "blocked Coordinator has a non-blocked Goal")
	}
	if goal.Status == domain.GoalBlocked {
		if coordinatorErr == nil && coordinator.Status != domain.CoordinatorBlocked {
			return issue("blocked_state_inconsistent", "blocked Goal has a non-blocked Coordinator")
		}
		if currentTodo != nil && !currentTodo.Status.IsTerminal() &&
			(currentTodo.Status != domain.TodoBlocked || currentTodo.Claim != nil) {
			return issue("blocked_state_inconsistent", "blocked Goal current Todo must be blocked and claim-free")
		}
	}
	if root.Status == domain.WorkItemCancelled && goal.Status != domain.GoalCancelled {
		return issue("terminal_state_inconsistent", "cancelled root Task has a non-cancelled Goal")
	}
	if root.Status == domain.WorkItemCompleted && goal.Status != domain.GoalCompleted {
		return issue("terminal_state_inconsistent", "completed root Task has a non-completed Goal")
	}
	if !root.Status.IsTerminal() && goal.Status.IsTerminal() {
		return issue("terminal_state_inconsistent", "non-terminal root Task has a terminal Goal")
	}
	return nil, false, nil
}

func governanceObjectiveFromRoot(root *domain.WorkItem) string {
	if root == nil {
		return ""
	}
	if objective := strings.TrimSpace(root.Description); objective != "" {
		return objective
	}
	return root.Title
}
