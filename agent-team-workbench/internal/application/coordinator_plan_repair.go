package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

const coordinatorMaxPlanRepairAttempts = 2

type planSubmissionFailureClass string

const (
	planSubmissionFailureAuthority planSubmissionFailureClass = "authority"
	planSubmissionFailureQuota     planSubmissionFailureClass = "quota"
)

type classifiedPlanSubmissionError struct {
	class planSubmissionFailureClass
	err   error
}

func (e *classifiedPlanSubmissionError) Error() string { return e.err.Error() }
func (e *classifiedPlanSubmissionError) Unwrap() error { return e.err }

func markPlanSubmissionFailure(class planSubmissionFailureClass, err error) error {
	if err == nil {
		return nil
	}
	return &classifiedPlanSubmissionError{class: class, err: err}
}

func clearCoordinatorRepairCheckpoint(state *domain.TaskCoordinatorState) {
	if state == nil {
		return
	}
	state.ClearRepair()
	if state.Data == nil {
		state.Data = map[string]any{}
	}
	delete(state.Data, "repair_of_run_id")
	delete(state.Data, "repair_origin_run_id")
	delete(state.Data, "repair_attempt")
	delete(state.Data, "repair_error_code")
	delete(state.Data, "repair_error_path")
}

func validateCoordinatorRepairSource(state *domain.TaskCoordinatorState, run *domain.ExecutionRun) error {
	if state == nil || run == nil {
		return fmt.Errorf("%w: repair source state and Run are required", domain.ErrValidation)
	}
	control := coordinatorContextOf(run)
	rootID, _ := control["root_work_item_id"].(string)
	stateID, _ := control["state_id"].(string)
	role, _ := control["role"].(string)
	if run.WorkspaceID != state.WorkspaceID || run.WorkItemID != state.RootWorkItemID ||
		run.AgentProfileID != state.CoordinatorAgentID || rootID != state.RootWorkItemID ||
		stateID != state.ID || role != coordinatorRole {
		return fmt.Errorf("%w: repair source Run %s does not belong to Coordinator state %s",
			domain.ErrWorkspaceContextMismatch, run.ID, state.ID)
	}
	return nil
}

func (s *Service) validateGovernedRepairSource(ctx context.Context, state *domain.TaskCoordinatorState,
	run *domain.ExecutionRun) error {
	if state == nil || run == nil {
		return fmt.Errorf("%w: repair source state and Run are required", domain.ErrValidation)
	}
	if isSystemCoordinatorRun(run) {
		return validateCoordinatorRepairSource(state, run)
	}
	if !isDelegatedCoordinatorRun(run) {
		return fmt.Errorf("%w: repair source Run is not a protected Coordinator", domain.ErrStateConflict)
	}
	root, err := s.store.WorkItems().Get(ctx, state.RootWorkItemID)
	if err != nil {
		return err
	}
	owner, err := s.store.Agents().Get(ctx, run.AgentProfileID)
	if err != nil {
		return err
	}
	if err := s.validateDelegatedCoordinatorContext(ctx, root, state, owner, coordinatorContextOf(run)); err != nil {
		return err
	}
	if run.WorkspaceID != state.WorkspaceID || run.WorkItemID != state.RootWorkItemID {
		return fmt.Errorf("%w: delegated repair source is outside Coordinator root", domain.ErrWorkspaceContextMismatch)
	}
	return nil
}

func (s *Service) processSystemCoordinatorPlanDecision(ctx context.Context, run *domain.ExecutionRun, text string) {
	goal, goalErr := s.store.Goals().GetByRootWorkItem(ctx, run.WorkItemID)
	if goalErr != nil {
		_ = s.blockCoordinatorPlanDecision(context.WithoutCancel(ctx), run,
			"governance_state_unavailable", goalErr.Error(),
			"修复根任务验收合同或治理状态后解除阻塞")
		return
	}
	// A paused/cancelled Goal may still have an already-running Provider turn.
	// Its late output is evidence only and must not create a Plan, repair turn,
	// or blocker until the user explicitly resumes the Goal.
	if goal.Status != domain.GoalActive {
		return
	}
	if state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, run.WorkItemID); stateErr == nil &&
		coordinatorRunRelinquishedToHandoff(state, run) {
		// Target acceptance fences the source outcome before this terminal hook.
		// Keep the source output as Run evidence only; the continuation is the
		// sole Planner turn.
		return
	}
	decision, source, found, err := DecodeCoordinatorPlanText(text)
	if !found {
		action, _ := coordinatorContextOf(run)["action"].(string)
		if action != "evaluation" {
			s.handleCoordinatorPlanDecisionFailure(ctx, run, &PlanDecisionError{
				Code: domain.GovernanceErrorPlanJSONSyntax, Path: "/",
				Message: "Coordinator turn returned no PlanDecisionV2 control candidate",
			})
		}
		return
	}
	if err != nil {
		s.handleCoordinatorPlanDecisionFailure(ctx, run, coordinatorPlanError(err))
		return
	}
	submitted, submitErr := s.SubmitGovernedTodoPlanDecision(ctx, run, decision, source)
	if submitErr != nil {
		if submitted != nil {
			return
		}
		var retryable *governancePlanRetryableError
		if errors.As(submitErr, &retryable) {
			// Admission/validated phases are durable checkpoints. A raw
			// storage failure must leave the Coordinator recoverable instead
			// of being misclassified as a PlanDecision blocker.
			return
		}
		s.handleCoordinatorPlanDecisionFailure(ctx, run, coordinatorPlanError(submitErr))
	}
}

func classifyPlanSubmissionError(err error) *PlanDecisionError {
	code := domain.GovernanceErrorPlanSemanticValidation
	var classified *classifiedPlanSubmissionError
	switch {
	case errors.As(err, &classified) && classified.class == planSubmissionFailureAuthority:
		code = domain.GovernanceErrorPlanAuthorityDenied
	case errors.As(err, &classified) && classified.class == planSubmissionFailureQuota:
		code = domain.GovernanceErrorPlanQuotaDenied
	case errors.Is(err, domain.ErrCapabilityMissing), errors.Is(err, domain.ErrNotFound),
		errors.Is(err, domain.ErrStateConflict), errors.Is(err, domain.ErrWorkspaceContextMismatch):
		code = domain.GovernanceErrorPlanAuthorityDenied
	}
	return &PlanDecisionError{Code: code, Path: "/steps", Message: err.Error(), Cause: err}
}

func (s *Service) finalizeCoordinatorPlanDecisionLocked(ctx context.Context, p SubmitPlanParams) error {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, p.WorkItemID)
	if err != nil {
		return err
	}
	source, err := s.store.Runs().Get(ctx, p.SourceRunID)
	if err != nil {
		return err
	}
	action, _ := coordinatorContextOf(source)["action"].(string)
	if action == "repair_plan" && state.RepairStatus == domain.CoordinatorRepairPending {
		expected := state.Version
		clearCoordinatorRepairCheckpoint(state)
		if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
			return err
		}
		state.Version = expected + 1
	}
	if p.DecisionAudit == nil {
		return nil
	}
	audit := p.DecisionAudit
	return s.appendCoordinatorEvent(ctx, state, state.RootWorkItemID,
		domain.EventCoordinatorPlanUpdated, "PlanDecisionV2 已通过严格校验",
		p.SourceRunID, source.AgentProfileID, state.Attempt, audit.Reason, nil, map[string]any{
			"stage": "decision", "schema_version": audit.SchemaVersion,
			"candidate_source": string(audit.Candidate), "reason": audit.Reason,
			"next_action": audit.NextAction, "step_count": audit.StepCount,
		})
}

func (s *Service) handleCoordinatorPlanDecisionFailure(ctx context.Context, run *domain.ExecutionRun, decisionErr *PlanDecisionError) {
	if run == nil || decisionErr == nil {
		return
	}
	switch decisionErr.Code {
	case domain.GovernanceErrorPlanJSONSyntax, domain.GovernanceErrorPlanSchemaValidation:
		if err := s.scheduleCoordinatorPlanRepair(context.WithoutCancel(ctx), run, decisionErr); err != nil {
			_ = s.blockCoordinatorPlanDecision(context.WithoutCancel(ctx), run,
				"coordinator_plan_repair_failed", err.Error(), "检查 Coordinator Runtime/上下文后重试")
		}
	case domain.GovernanceErrorPlanSemanticValidation,
		domain.GovernanceErrorPlanAuthorityDenied,
		domain.GovernanceErrorPlanQuotaDenied:
		_ = s.blockCoordinatorPlanDecision(context.WithoutCancel(ctx), run,
			string(decisionErr.Code), decisionErr.Error(), "修正计划语义、权限或预算后解除阻塞")
	default:
		_ = s.blockCoordinatorPlanDecision(context.WithoutCancel(ctx), run,
			"coordinator_plan_decision_failed", decisionErr.Error(), "检查 PlanDecisionV2 输出后解除阻塞")
	}
}

func (s *Service) blockCoordinatorPlanDecision(ctx context.Context, run *domain.ExecutionRun, code, message, nextAction string) error {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, run.WorkItemID)
	if err != nil {
		return err
	}
	return s.blockCoordinator(ctx, state, run, code, message, nextAction)
}

func (s *Service) scheduleCoordinatorPlanRepair(ctx context.Context, sourceRun *domain.ExecutionRun, decisionErr *PlanDecisionError) error {
	var repairRun *domain.ExecutionRun
	workspaceID := sourceRun.WorkspaceID
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, sourceRun.WorkItemID)
		if err != nil {
			return err
		}
		if state.CurrentRunID != sourceRun.ID || state.Status == domain.CoordinatorCompleted ||
			state.Status == domain.CoordinatorCancelled || state.RepairStatus == domain.CoordinatorRepairExhausted {
			return nil
		}
		originRunID := state.RepairSourceRunID
		if originRunID == "" {
			originRunID = sourceRun.ID
		}
		errorClass := domain.CoordinatorRepairErrorSyntax
		if decisionErr.Code == domain.GovernanceErrorPlanSchemaValidation {
			errorClass = domain.CoordinatorRepairErrorSchema
		}
		validationErrors := []domain.GovernanceValidationError{{
			Code: decisionErr.Code, Message: decisionErr.Message, Path: decisionErr.Path,
		}}
		if state.RepairAttempt >= coordinatorMaxPlanRepairAttempts {
			expected := state.Version
			state.RepairStatus = domain.CoordinatorRepairExhausted
			state.RepairAttempt = coordinatorMaxPlanRepairAttempts
			state.RepairSourceRunID = originRunID
			state.RepairErrorClass = errorClass
			state.RepairErrorCode = string(decisionErr.Code)
			state.RepairValidationErrors = validationErrors
			state.Status = domain.CoordinatorBlocked
			state.Phase = "blocked"
			state.BlockerCode = "coordinator_plan_repair_exhausted"
			state.BlockerMessage = "PlanDecisionV2 自动修复两次后仍无效"
			state.LastError = decisionErr.Error()
			state.CurrentAction = "检查 schema validation errors 后人工解除阻塞"
			state.CurrentRunID = ""
			state.NextActionAt = nil
			if state.Data == nil {
				state.Data = map[string]any{}
			}
			delete(state.Data, "control_action")
			handoffCleared := clearCoordinatorHandoffCheckpoint(state)
			if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
				return err
			}
			state.Version = expected + 1
			root, err := s.store.WorkItems().Get(ctx, state.RootWorkItemID)
			if err != nil {
				return err
			}
			if !root.Status.IsTerminal() && root.Status != domain.WorkItemBlocked {
				if err := s.blockLocked(ctx, root, BlockParams{
					Code: state.BlockerCode, Message: state.BlockerMessage, Source: "coordinator",
				}); err != nil {
					return err
				}
			}
			if err := s.blockCurrentGovernanceLocked(ctx, state.RootWorkItemID, time.Now().UTC()); err != nil {
				return err
			}
			return s.appendCoordinatorEvent(ctx, state, state.RootWorkItemID, domain.EventCoordinatorBlocked,
				"PlanDecision 自动修复预算耗尽", sourceRun.ID, sourceRun.AgentProfileID,
				state.Attempt, decisionErr.Error(), nil, map[string]any{
					"stage": "repair", "repair_attempt": state.RepairAttempt,
					"failure_code": state.BlockerCode, "plan_error_code": string(decisionErr.Code),
					"validation_errors": validationErrors, "retryable": false,
					"handoff_cleared": handoffCleared,
				})
		}

		expected := state.Version
		state.RepairStatus = domain.CoordinatorRepairPending
		state.RepairAttempt++
		state.RepairSourceRunID = originRunID
		state.RepairErrorClass = errorClass
		state.RepairErrorCode = string(decisionErr.Code)
		state.RepairValidationErrors = validationErrors
		state.Status = domain.CoordinatorQueued
		state.Phase = "repair"
		state.CurrentAction = "repair_plan"
		state.CurrentRunID = ""
		state.NextActionAt = nil
		state.LastError = decisionErr.Error()
		if state.Data == nil {
			state.Data = map[string]any{}
		}
		state.Data["control_action"] = "repair_plan"
		state.Data["repair_of_run_id"] = sourceRun.ID
		state.Data["repair_origin_run_id"] = originRunID
		state.Data["repair_attempt"] = state.RepairAttempt
		state.Data["repair_error_code"] = string(decisionErr.Code)
		state.Data["repair_error_path"] = decisionErr.Path
		if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
			return err
		}
		state.Version = expected + 1
		goal, err := s.store.Goals().GetByRootWorkItem(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
		if err != nil {
			return err
		}
		controlOwnerID := state.CoordinatorAgentID
		keepClaim := false
		if isDelegatedCoordinatorRun(sourceRun) {
			controlOwnerID = sourceRun.AgentProfileID
			keepClaim = true
		}
		controlReceipt, err := s.admitGovernanceControlDecisionLocked(ctx, governanceControlReceiptParams{
			Goal: goal, Todo: todo, OwnerAgentID: controlOwnerID,
			Kind: domain.TurnDecisionRepair, Reason: decisionErr.Error(),
			NextAction: "修复后重新提交完整 PlanDecisionV2", SourceRunID: sourceRun.ID,
			AdmissionKey:   fmt.Sprintf("control:repair:%s:%d", sourceRun.ID, state.RepairAttempt),
			ValidationCode: string(decisionErr.Code), ValidationPath: decisionErr.Path,
			ValidationMessage: decisionErr.Message, KeepClaim: keepClaim,
		})
		if err != nil {
			return err
		}
		if err := s.appendGovernanceProjectionPhaseLocked(ctx, controlReceipt.TurnKey); err != nil {
			return err
		}
		if err := s.appendCoordinatorEvent(ctx, state, state.RootWorkItemID,
			domain.EventCoordinatorAttemptUpdated, "PlanDecision 格式无效，启动自动修复",
			sourceRun.ID, sourceRun.AgentProfileID, state.Attempt, decisionErr.Error(), nil,
			map[string]any{"stage": "repair", "repair_attempt": state.RepairAttempt,
				"plan_error_code": string(decisionErr.Code), "validation_errors": validationErrors,
				"retryable": true}); err != nil {
			return err
		}
		repairRun, err = s.startCoordinatorTurn(ctx, state.RootWorkItemID)
		return err
	})
	if err != nil {
		return err
	}
	if repairRun == nil {
		return nil
	}
	s.notifier.Notify(workspaceID)
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), repairRun); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), repairRun.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true,
					"family": string(runtime.FamilyTransientUpstream)})
			return err
		}
	}
	return nil
}

func coordinatorPlanError(err error) *PlanDecisionError {
	var decisionErr *PlanDecisionError
	if errors.As(err, &decisionErr) {
		return decisionErr
	}
	return classifyPlanSubmissionError(err)
}

func repairRunContext(state *domain.TaskCoordinatorState) map[string]any {
	if state == nil {
		return nil
	}
	return map[string]any{
		"repair_attempt": state.RepairAttempt, "repair_source_run_id": state.RepairSourceRunID,
		"error_class": string(state.RepairErrorClass), "error_code": state.RepairErrorCode,
		"validation_errors": state.RepairValidationErrors, "recorded_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
}
