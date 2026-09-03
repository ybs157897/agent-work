package application

import (
	"context"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func (s *Service) storeBlockedGovernanceState(ctx context.Context, root *domain.WorkItem,
	existing *domain.Goal) (goal *domain.Goal, todo *domain.Todo, createdGoal, createdTodo bool, err error) {
	err = s.store.InTx(ctx, func(txctx context.Context) error {
		goal, todo, createdGoal, createdTodo, err = s.materializeBlockedGovernanceState(txctx, root, existing)
		return err
	})
	return
}

// materializeBlockedGovernanceState creates the minimum native projection for
// a root that is already blocked. It intentionally bypasses StartGoal: no
// active Goal, running Todo, claim, Run or Receipt may appear while the root
// is stopped. The Goal row is created/updated before the Todo FK is connected,
// all inside the caller's transaction.
func (s *Service) materializeBlockedGovernanceState(ctx context.Context, root *domain.WorkItem,
	existing *domain.Goal) (*domain.Goal, *domain.Todo, bool, bool, error) {
	if root == nil || root.Status != domain.WorkItemBlocked {
		return nil, nil, false, false, fmt.Errorf("%w: blocked root Task required", domain.ErrValidation)
	}
	if len(root.AcceptanceCriteria) == 0 {
		return nil, nil, false, false, fmt.Errorf("%w: root Task has no acceptance criteria", domain.ErrValidation)
	}
	now := time.Now().UTC()
	goal := existing
	createdGoal := false
	if goal == nil {
		goal = &domain.Goal{
			ID: domain.NewID(domain.PrefixGoal), WorkspaceID: root.WorkspaceID,
			RootWorkItemID: root.ID, Objective: governanceObjectiveFromRoot(root),
			AcceptanceContract: append([]string(nil), root.AcceptanceCriteria...),
			Status:             domain.GoalBlocked, Phase: "blocked", QuotaPolicies: []domain.QuotaPolicy{},
			CompletionEvidenceSummary: []domain.GovernanceEvidenceItem{}, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := goal.Validate(); err != nil {
			return nil, nil, false, false, err
		}
		if err := s.store.Goals().Create(ctx, goal); err != nil {
			return nil, nil, false, false, err
		}
		createdGoal = true
		if err := s.emit(ctx, root.WorkspaceID, domain.EventGoalCreated,
			domain.AggregateGoal, goal.ID, goal.Version, nil,
			map[string]any{"root_work_item_id": goal.RootWorkItemID,
				"state": string(goal.Status), "version": goal.Version}); err != nil {
			return nil, nil, false, false, err
		}
	} else {
		if goal.WorkspaceID != root.WorkspaceID || goal.RootWorkItemID != root.ID {
			return nil, nil, false, false, fmt.Errorf("%w: blocked root Goal binding differs", domain.ErrWorkspaceContextMismatch)
		}
		if goal.Status.IsTerminal() {
			return goal, nil, false, false, nil
		}
		if goal.Status != domain.GoalDraft && goal.Status != domain.GoalBlocked {
			return nil, nil, false, false, fmt.Errorf("%w: cannot materialize blocked projection from Goal %s", domain.ErrStateConflict, goal.Status)
		}
		goal.Objective = governanceObjectiveFromRoot(root)
		goal.AcceptanceContract = append([]string(nil), root.AcceptanceCriteria...)
	}

	if goal.CurrentTodoID != "" {
		todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
		if err != nil {
			return nil, nil, false, false, err
		}
		return goal, todo, createdGoal, false, nil
	}
	ownerID, err := s.goalOwnerAgentID(ctx, root)
	if err != nil {
		return nil, nil, false, false, err
	}
	scopeAgentIDs, err := s.initialTodoDecisionScopeAgentIDs(ctx, root.WorkspaceID, ownerID)
	if err != nil {
		return nil, nil, false, false, err
	}
	priority := root.Priority
	if !priority.Valid() {
		priority = domain.PriorityMedium
	}
	todo := &domain.Todo{
		ID: domain.NewID(domain.PrefixTodo), GoalID: goal.ID, Class: domain.TodoAdvancement,
		Status: domain.TodoBlocked, Instruction: goal.Objective,
		Acceptance: append([]string(nil), goal.AcceptanceContract...), Priority: priority,
		Predecessors: []string{}, Successors: []string{}, DecisionScope: domain.DecisionScope{
			WorkItemIDs: []string{root.ID}, AgentIDs: scopeAgentIDs,
			RuntimeCapabilities: []string{}, WriteScopes: []string{}, MaxDispatch: 64,
		}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := todo.Validate(); err != nil {
		return nil, nil, false, false, err
	}
	if err := s.store.Todos().Create(ctx, todo); err != nil {
		return nil, nil, false, false, err
	}
	if err := s.emitTodoCreated(ctx, root.WorkspaceID, todo); err != nil {
		return nil, nil, false, false, err
	}
	from := goal.Status
	expected := goal.Version
	goal.CurrentTodoID = todo.ID
	goal.Phase = "blocked"
	if goal.Status == domain.GoalDraft {
		goal.Status = domain.GoalBlocked
	}
	goal.Version++
	goal.UpdatedAt = now
	if err := goal.Validate(); err != nil {
		return nil, nil, false, false, err
	}
	if err := s.store.Goals().Update(ctx, goal, expected); err != nil {
		return nil, nil, false, false, err
	}
	if from != goal.Status {
		if err := s.emitGoalStateChanged(ctx, goal, from); err != nil {
			return nil, nil, false, false, err
		}
	}
	return goal, todo, createdGoal, true, nil
}
