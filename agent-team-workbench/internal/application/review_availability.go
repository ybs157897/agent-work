package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ReviewAvailabilityPermissions carries the already-authorized command
// capabilities from the HTTP/session boundary. The read-side readiness itself
// is independent of these permissions so every caller sees the same lane.
type ReviewAvailabilityPermissions struct {
	CanApprove bool
	CanWrite   bool
}

// ReviewAvailability is the read-only command affordance for a root Task.
// Ready describes the task/control line reaching its user-review checkpoint;
// CanAccept additionally includes canonical evidence and caller permissions.
type ReviewAvailability struct {
	Ready        bool
	CanAccept    bool
	CanReturn    bool
	AcceptReason string
	ReturnReason string
}

const (
	reviewTaskNotReadyReason        = "任务尚未进入待验收阶段"
	reviewChildReason               = "子任务不能单独验收，请打开总任务"
	reviewCoordinatorNotReadyReason = "Coordinator 尚未进入待用户验收状态"
	reviewCanonicalNotReadyReason   = "任务的验收依据尚未完成"
	reviewPermissionAcceptReason    = "当前角色无权验收任务"
	reviewPermissionReturnReason    = "当前角色无权打回任务"
	reviewUnavailableReason         = "验收状态暂不可用，请刷新后重试"
)

// ReviewAvailability derives the same pre-human acceptance boundary used by
// the Coordinator. It is read-only: no state repair, evidence creation, or
// command dispatch happens here.
func (s *Service) ReviewAvailability(ctx context.Context, wi *domain.WorkItem,
	permissions ReviewAvailabilityPermissions) (ReviewAvailability, error) {
	availability := ReviewAvailability{}
	if wi == nil {
		return unavailableReviewAvailability(), fmt.Errorf("%w: work item required", domain.ErrValidation)
	}
	if !isTaskWorkItem(wi) {
		availability.AcceptReason = reviewTaskNotReadyReason
		availability.ReturnReason = reviewTaskNotReadyReason
		return availability, nil
	}
	if wi.ParentID != "" {
		availability.AcceptReason = reviewChildReason
		availability.ReturnReason = reviewChildReason
		return availability, nil
	}
	if wi.Status != domain.WorkItemInProgress ||
		(wi.Phase != domain.PhaseReview && wi.Phase != domain.PhaseAcceptance) {
		availability.AcceptReason = reviewTaskNotReadyReason
		availability.ReturnReason = reviewTaskNotReadyReason
		return availability, nil
	}

	returnReady := true
	state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, wi.ID)
	switch {
	case stateErr == nil:
		if state == nil {
			return unavailableReviewAvailability(), fmt.Errorf("%w: coordinator state missing", domain.ErrStateConflict)
		}
		if state.RootWorkItemID != wi.ID {
			availability.AcceptReason = reviewChildReason
			availability.ReturnReason = reviewChildReason
			return availability, nil
		}
		if state.Status != domain.CoordinatorWaitingUser {
			availability.AcceptReason = reviewCoordinatorNotReadyReason
			availability.ReturnReason = reviewCoordinatorNotReadyReason
			return availability, nil
		}
		// Coordinator waiting_user is the user-facing review checkpoint. The
		// canonical finish gate controls only acceptance; Return remains useful
		// when evidence is stale or incomplete so the task can be sent back.
		availability.Ready = true
	case errors.Is(stateErr, domain.ErrNotFound):
		// Legacy/non-coordinated Tasks retain their existing phase command
		// semantics; the canonical gate below still applies if a Goal exists.
		availability.Ready = true
	default:
		return unavailableReviewAvailability(), stateErr
	}

	canonicalReady, reason, err := s.reviewCanonicalFinishGate(ctx, wi.ID)
	if err != nil {
		return unavailableReviewAvailability(), err
	}
	if !canonicalReady {
		availability.AcceptReason = reason
	}
	availability.CanAccept = canonicalReady && permissions.CanApprove
	if !availability.CanAccept {
		if availability.AcceptReason == "" && !permissions.CanApprove {
			availability.AcceptReason = reviewPermissionAcceptReason
		}
	}
	availability.CanReturn = returnReady && permissions.CanWrite
	if !availability.CanReturn && !permissions.CanWrite {
		availability.ReturnReason = reviewPermissionReturnReason
	}
	return availability, nil
}

// reviewCanonicalFinishGate reuses the existing governed finish gate without
// calling the Goal completion path (which requires a completed root). Missing
// Goal means this is a legacy/non-governed task and the caller already checked
// the WorkItem phase boundary.
func (s *Service) reviewCanonicalFinishGate(ctx context.Context, rootWorkItemID string) (bool, string, error) {
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, rootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return true, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if goal == nil {
		return false, reviewCanonicalNotReadyReason, nil
	}
	if goal.Status != domain.GoalActive && goal.Status != domain.GoalWaiting {
		return false, "治理目标当前不可验收", nil
	}

	if goal.CurrentTodoID == "" {
		return false, "当前任务缺少可验收的治理步骤", nil
	}
	todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, "当前任务缺少可验收的治理步骤", nil
		}
		return false, "", err
	}
	if todo == nil || todo.LastTurnSeq < 1 {
		return false, "当前任务尚未形成可验收的治理轮次", nil
	}
	if todo.Status != domain.TodoWaiting && todo.Status != domain.TodoRunning {
		return false, "当前治理步骤尚未进入可验收状态", nil
	}
	passed, reason, err := s.latestPassedValidationForGoal(ctx, goal, todo)
	if err != nil {
		return false, "", err
	}
	if !passed {
		return false, localizeReviewGateReason(reason), nil
	}
	validationTodos, err := listValidationTodos(ctx, s, goal.ID)
	if err != nil {
		return false, "", err
	}
	for _, candidate := range validationTodos {
		if candidate == nil || candidate.Status != domain.TodoCompleted {
			return false, "仍有验证步骤未完成", nil
		}
	}
	for _, evidence := range goal.CompletionEvidenceSummary {
		if evidence.Verification != domain.EvidenceVerificationPassed &&
			evidence.Verification != domain.EvidenceVerificationAccepted {
			continue
		}
		if err := s.ValidateEvidenceReference(ctx, goal.ID, todo.ID, evidence); err != nil {
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrStateConflict) ||
				errors.Is(err, domain.ErrValidation) {
				return false, "已有验收依据已失效", nil
			}
			return false, "", err
		}
	}
	return true, "", nil
}

func localizeReviewGateReason(reason string) string {
	switch {
	case strings.Contains(reason, "no finished governed Plan"):
		return "当前任务尚未完成可验收的计划"
	case strings.Contains(reason, "acceptance criteria digest is stale"):
		return "验收依据仍基于旧版验收标准"
	case strings.Contains(reason, "no canonical passed validation result"):
		return "尚未获得通过的规范验证结果"
	case strings.Contains(reason, "acceptance contract digest unavailable"):
		return "验收标准当前不可用"
	case strings.Contains(reason, "Goal/Todo required"):
		return "验收依据缺少治理目标或当前步骤"
	default:
		return reviewCanonicalNotReadyReason
	}
}

func unavailableReviewAvailability() ReviewAvailability {
	return ReviewAvailability{
		AcceptReason: reviewUnavailableReason,
		ReturnReason: reviewUnavailableReason,
	}
}
