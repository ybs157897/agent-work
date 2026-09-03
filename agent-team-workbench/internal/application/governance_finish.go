package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type FinishGateResult struct {
	Allowed bool
	Reasons []string
}

type FinishGateError struct {
	GoalID  string
	Reasons []string
}

func goalTurnSeqForPlan(plan *domain.Plan) int64 {
	if plan == nil || plan.GovernanceTurnKey == nil {
		return 0
	}
	return plan.GovernanceTurnKey.TurnSeq
}

// CoordinatorFinishEvidenceReady is the pre-human-acceptance gate. It only
// proves that a governed plan has a canonical evaluation result; the root
// WorkItem still needs the separate user Accept command before Goal completion.
func (s *Service) CoordinatorFinishEvidenceReady(ctx context.Context, goalID, todoID string,
	plan *domain.Plan, sourceRun *domain.ExecutionRun) (bool, string, error) {
	if plan == nil || sourceRun == nil {
		return false, "governed finish requires Plan and source Run", nil
	}
	requiresEvaluation := false
	for _, step := range plan.Steps {
		if step.Verb != domain.PlanVerbFinish {
			continue
		}
		if evaluation, _ := step.Payload["evaluation"].(bool); evaluation {
			requiresEvaluation = true
		}
	}
	if !requiresEvaluation {
		return false, "finish decision has no canonical evaluation result", nil
	}
	results, err := s.store.ValidationResults().ListByGoal(ctx, goalID)
	if err != nil {
		return false, "validation results unavailable", err
	}
	for _, result := range results {
		if result == nil || result.GoalID != goalID || result.TodoID != todoID ||
			result.SourceRunID == "" || result.Status != domain.ValidationResultPassed {
			continue
		}
		run, getErr := s.store.Runs().Get(ctx, result.SourceRunID)
		if getErr != nil {
			return false, "validation source unavailable", getErr
		}
		if run == nil || !run.Status.IsTerminal() || run.Status != domain.RunSucceeded {
			continue
		}
		control := coordinatorContextOf(run)
		if planID, _ := control["plan_id"].(string); planID == plan.ID {
			return true, "", nil
		}
	}
	return false, "evaluation has not produced a canonical passed validation result", nil
}

func (e *FinishGateError) Error() string {
	return fmt.Sprintf("%s: goal finish gate denied: %v", domain.ErrValidation, e.Reasons)
}

func (e *FinishGateError) Unwrap() error { return domain.ErrValidation }

func (s *Service) EvaluateGoalFinish(ctx context.Context, goalID string) (*FinishGateResult, error) {
	var result *FinishGateResult
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, goalID)
		if err != nil {
			return err
		}
		result, err = s.evaluateGoalFinishLocked(txctx, goal)
		return err
	})
	return result, err
}

func (s *Service) evaluateGoalFinishLocked(ctx context.Context, goal *domain.Goal) (*FinishGateResult, error) {
	result := &FinishGateResult{Allowed: true, Reasons: []string{}}
	if goal == nil {
		return nil, fmt.Errorf("%w: Goal required", domain.ErrValidation)
	}
	root, err := s.store.WorkItems().Get(ctx, goal.RootWorkItemID)
	if err != nil {
		return nil, err
	}
	if root.Status != domain.WorkItemCompleted {
		result.Allowed = false
		result.Reasons = append(result.Reasons, "root WorkItem has not been accepted")
	}
	if state, stateErr := s.store.TaskCoordinators().GetState(ctx, goal.RootWorkItemID); stateErr == nil {
		if state.Status != domain.CoordinatorCompleted {
			result.Allowed = false
			result.Reasons = append(result.Reasons, "Coordinator is not completed")
		}
	} else if !errors.Is(stateErr, domain.ErrNotFound) {
		return nil, stateErr
	}
	todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		return nil, err
	}
	if err := s.ValidateEvidenceForFinish(ctx, goal.ID, todo.ID, goal.CompletionEvidenceSummary); err != nil {
		result.Allowed = false
		result.Reasons = append(result.Reasons, err.Error())
	}
	if ok, reason, err := s.latestPassedValidationForGoal(ctx, goal, todo); err != nil {
		return nil, err
	} else if !ok {
		result.Allowed = false
		result.Reasons = append(result.Reasons, reason)
	}
	validationTodos, err := listValidationTodos(ctx, s, goal.ID)
	if err != nil {
		return nil, err
	}
	for _, candidate := range validationTodos {
		if candidate.Status != domain.TodoCompleted {
			result.Allowed = false
			result.Reasons = append(result.Reasons, fmt.Sprintf("validation Todo %s is not completed", candidate.ID))
		}
	}
	return result, nil
}

func (s *Service) latestPassedValidationForGoal(ctx context.Context, goal *domain.Goal, todo *domain.Todo) (bool, string, error) {
	if goal == nil || todo == nil {
		return false, "Goal/Todo required for validation gate", nil
	}
	plan, err := s.store.Plans().LatestByWorkItem(ctx, goal.RootWorkItemID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return false, "latest governed Plan unavailable", err
	}
	if plan == nil || plan.Status != domain.PlanFinished || plan.GovernanceTurnKey == nil ||
		plan.GovernanceTurnKey.GoalID != goal.ID || plan.GovernanceTurnKey.TodoID != todo.ID {
		return false, "no finished governed Plan for the current Todo", nil
	}
	wantCriteria, err := canonicalGovernancePlanDigest(goal.AcceptanceContract)
	if err != nil {
		return false, "acceptance contract digest unavailable", err
	}
	result, ok, err := s.latestPassedValidationEvidence(ctx, goal, todo, plan)
	if err != nil {
		return false, "validation results unavailable", err
	}
	if ok {
		if result.CriteriaDigest != wantCriteria {
			return false, "validation result acceptance criteria digest is stale", nil
		}
		return true, "", nil
	}
	return false, "no canonical passed validation result for the finished Plan", nil
}

func (s *Service) latestPassedValidationEvidence(ctx context.Context, goal *domain.Goal, todo *domain.Todo,
	plan *domain.Plan) (*domain.ValidationResult, bool, error) {
	if goal == nil || todo == nil || plan == nil {
		return nil, false, nil
	}
	results, err := s.store.ValidationResults().ListByGoal(ctx, goal.ID)
	if err != nil {
		return nil, false, err
	}
	var latest *domain.ValidationResult
	for _, result := range results {
		if result == nil || result.TodoID != todo.ID || result.Status != domain.ValidationResultPassed {
			continue
		}
		run, runErr := s.store.Runs().Get(ctx, result.SourceRunID)
		if runErr != nil {
			return nil, false, runErr
		}
		if run == nil || run.Status != domain.RunSucceeded {
			continue
		}
		if eval, _ := run.Input["evaluation"].(bool); !eval {
			continue
		}
		control := coordinatorContextOf(run)
		if planID, _ := control["plan_id"].(string); planID != plan.ID {
			continue
		}
		if latest == nil || result.RecordedAt.After(latest.RecordedAt) ||
			(result.RecordedAt.Equal(latest.RecordedAt) && result.ID > latest.ID) {
			latest = result
		}
	}
	return latest, latest != nil, nil
}

func listValidationTodos(ctx context.Context, s *Service, goalID string) ([]*domain.Todo, error) {
	todos, err := s.store.Todos().ListByGoal(ctx, goalID)
	if err != nil {
		return nil, err
	}
	var out []*domain.Todo
	for _, todo := range todos {
		if todo != nil && todo.Class == domain.TodoValidation {
			out = append(out, todo)
		}
	}
	return out, nil
}

// CompleteGoal is the explicit governance finish command. It cannot bypass
// the root Task's human Accept gate and never infers evidence from prose.
func (s *Service) CompleteGoal(ctx context.Context, goalID string, expectedVersion int) (*domain.Goal, error) {
	var completed *domain.Goal
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, goalID)
		if err != nil {
			return err
		}
		if err := goal.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if goal.Status == domain.GoalCompleted {
			// Repair a legacy/replayed completed Goal whose current Todo was not
			// closed by the original acceptance transaction. The exact completion
			// identity is still required; never fabricate a TurnKey for an empty
			// watermark.
			if goal.CurrentTodoID != "" {
				todo, todoErr := s.store.Todos().Get(txctx, goal.CurrentTodoID)
				if todoErr != nil {
					return todoErr
				}
				if todo.Status != domain.TodoCompleted {
					if todo.LastTurnSeq < 1 {
						return fmt.Errorf("%w: completed Goal current Todo has no admitted Turn", domain.ErrStateConflict)
					}
					before := todo.Status
					key := domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: todo.LastTurnSeq}
					completedTodo, completeErr := s.store.Todos().Complete(txctx, todo.ID, key, goal.RootWorkItemID, time.Now().UTC(), todo.Version)
					if completeErr != nil {
						return completeErr
					}
					if err := s.emitTodoStateChanged(txctx, goal.WorkspaceID, completedTodo, before); err != nil {
						return err
					}
				}
			}
			completed = goal
			return nil
		}
		gate, err := s.evaluateGoalFinishLocked(txctx, goal)
		if err != nil {
			return err
		}
		if !gate.Allowed {
			return &FinishGateError{GoalID: goal.ID, Reasons: gate.Reasons}
		}
		todo, err := s.store.Todos().Get(txctx, goal.CurrentTodoID)
		if err != nil {
			return err
		}
		if todo.LastTurnSeq < 1 {
			return fmt.Errorf("%w: Goal completion requires an admitted current Todo Turn", domain.ErrStateConflict)
		}
		beforeTodoStatus := todo.Status
		completionKey := domain.TurnKey{GoalID: goal.ID, TodoID: todo.ID, TurnSeq: todo.LastTurnSeq}
		completedTodo, completeErr := s.store.Todos().Complete(txctx, todo.ID, completionKey,
			goal.RootWorkItemID, time.Now().UTC(), todo.Version)
		if completeErr != nil {
			return completeErr
		}
		if beforeTodoStatus != completedTodo.Status {
			if err := s.emitTodoStateChanged(txctx, goal.WorkspaceID, completedTodo, beforeTodoStatus); err != nil {
				return err
			}
		}
		from := goal.Status
		now := time.Now().UTC()
		if err := goal.Complete(now); err != nil {
			return err
		}
		if err := s.store.Goals().Update(txctx, goal, expectedVersion); err != nil {
			return err
		}
		if err := s.emitGoalStateChanged(txctx, goal, from); err != nil {
			return err
		}
		completed = goal
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completed != nil && s.notifier != nil {
		s.notifier.Notify(completed.WorkspaceID)
	}
	return completed, nil
}

// AcceptArtifact is the controlled reviewer write point for an Artifact. The
// artifact remains a Run-owned entity; this only advances its review status.
func (s *Service) AcceptArtifact(ctx context.Context, artifactID string) (*domain.Artifact, error) {
	var artifact *domain.Artifact
	var workspaceID string
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		art, err := s.store.Runs().GetArtifact(txctx, artifactID)
		if err != nil {
			return err
		}
		if art.Status == domain.ArtifactAccepted {
			artifact = art
			return nil
		}
		run, err := s.store.Runs().Get(txctx, art.RunID)
		if err != nil {
			return err
		}
		wi, err := s.store.WorkItems().Get(txctx, run.WorkItemID)
		if err != nil {
			return err
		}
		if !isTaskWorkItem(wi) {
			return fmt.Errorf("%w: Chat artifact cannot be accepted as Task evidence", domain.ErrValidation)
		}
		if err := s.store.Runs().UpdateArtifactStatus(txctx, artifactID, domain.ArtifactAccepted); err != nil {
			return err
		}
		workspaceID = wi.WorkspaceID
		if err := s.emit(txctx, wi.WorkspaceID, domain.EventArtifactUpdated, domain.AggregateArtifact,
			artifactID, 2, nil, map[string]any{"run_id": art.RunID, "status": string(domain.ArtifactAccepted),
				"record_kind": string(workItemRecordKind(wi))}); err != nil {
			return err
		}
		artifact, err = s.store.Runs().GetArtifact(txctx, artifactID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && s.notifier != nil {
		s.notifier.Notify(workspaceID)
	}
	return artifact, nil
}
