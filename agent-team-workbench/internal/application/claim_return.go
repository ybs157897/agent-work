// claim_return.go M4 认领模式与手动打回命令（设计 note
// notes/implemented/orchestration/2026-08-24-m4-claim-join-guardrails.md §1/§5）：
// claim = 发布→领取（指派+唤醒的复合命令，无一等实体）；return = 验收回流的
// 人工半环（acceptance/review → execution）。两者均以 ErrStateConflict 表达
// 「命令与当前状态冲突」（httpapi 映射 409）。
package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ClaimWorkItem 认领任务池中无主 todo 任务：仅 todo 且无 assignee 可认领；
// 认领 = 指派 + 复用 enqueueAssignmentWake（agent 开 wake_on_assignment 时
// 自动唤起认领者）。幂等：同 agent 重复认领返回现状不报错（不重复唤醒）。
// 已被他人认领 / 非 todo → ErrStateConflict。
//
// F1 执行锁与认领正交：带锁任务必为 in_progress（建 run 即推进状态），天然
// 不满足 claim 前置；死属主锁的抢占发生在下一个 run 进 running 的获取点
// （acquireTaskLock），claim 不做 in_progress → todo 的自动重置——状态回流
// 留给既有的人工/编排路径。
func (s *Service) ClaimWorkItem(ctx context.Context, workItemID, agentID string, expectedVersion int) (*domain.WorkItem, error) {
	var (
		wi      *domain.WorkItem
		claimed bool
	)
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		if _, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, w.ID); err == nil {
			return fmt.Errorf("%w: coordinated Task 由系统 Coordinator 自动接取", domain.ErrValidation)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if w.AgentProfileID == agentID {
			wi = w // 幂等：同 agent 重复认领返回现状
			return nil
		}
		if w.AgentProfileID != "" {
			return fmt.Errorf("%w: 任务已被 %s 认领", domain.ErrStateConflict, w.AgentProfileID)
		}
		if w.Status != domain.WorkItemTodo {
			return fmt.Errorf("%w: 仅 todo 任务可认领，当前 %s", domain.ErrStateConflict, w.Status)
		}
		if err := s.assignLocked(ctx, w, agentID); err != nil {
			return err
		}
		wi = w
		claimed = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	if claimed {
		s.enqueueAssignmentWake(context.WithoutCancel(ctx), wi, agentID)
	}
	return wi, nil
}

// ReturnWorkItem 手动打回（验收回流的人工半环，RFC §7.9）：reason 必填
// （trim 后为空 → ErrReviewFeedbackRequired），确保每次打回都有不可变反馈证据。
//
// 分支：
//   - coordinated root：review_feedback 评论（cursor 分配 revision）+ WorkItem
//     BeginExecution + Coordinator waiting_user→queued/message + 全部事件/
//     activity/audit/outbox 在同一事务收口（§7.9），commit 后 StartCoordinator
//     best-effort；
//   - coordinated child：拒绝 ErrChildReviewNotSupported，用户可在 child 上追加
//     requirement 由根 Coordinator 消费；
//   - legacy/non-coordinated Task：保留既有回流 + activity，但不创建 TaskComment。
func (s *Service) ReturnWorkItem(ctx context.Context, workItemID string, reason string, expectedVersion int) (*domain.WorkItem, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ErrReviewFeedbackRequired
	}
	var (
		wi          *domain.WorkItem
		coordinated bool
	)
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		var state *domain.TaskCoordinatorState
		if st, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, w.ID); stateErr == nil {
			state = st
			coordinated = true
			if state.RootWorkItemID != w.ID {
				return ErrChildReviewNotSupported
			}
		} else if !errors.Is(stateErr, domain.ErrNotFound) {
			return stateErr
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if w.Status != domain.WorkItemInProgress ||
			(w.Phase != domain.PhaseReview && w.Phase != domain.PhaseAcceptance) {
			if coordinated {
				return fmt.Errorf("%w: coordinated Task 打回要求 phase=review/acceptance，当前 %s/%s",
					ErrReviewStateConflict, w.Status, w.Phase)
			}
			return fmt.Errorf("%w: 仅 in_progress 且 phase=review/acceptance 可打回，当前 %s/%s",
				domain.ErrStateConflict, w.Status, w.Phase)
		}
		if coordinated && state.Status != domain.CoordinatorWaitingUser {
			// Accept/Return/feedback 竞态门（§7.10）：coordinated root 必须同时
			// 看到 review/acceptance phase 与 waiting_user。
			return fmt.Errorf("%w: coordinated Task 打回要求 Coordinator waiting_user，当前 %s",
				ErrReviewStateConflict, state.Status)
		}
		from := w.Phase
		now := time.Now().UTC()
		expected := w.Version
		w.BeginExecution(now)
		if err := s.store.WorkItems().Update(ctx, w, expected); err != nil {
			return err
		}
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemUpdated,
			domain.AggregateWorkItem, w.ID, w.Version, nil,
			map[string]any{"phase": string(w.Phase), "returned_from": string(from),
				"record_kind": string(workItemRecordKind(w))}); err != nil {
			return err
		}
		if coordinated {
			// review_feedback 评论：revision 由 cursor 行事务内分配（§7.9）。
			comment := &domain.TaskComment{
				ID:             domain.NewID(domain.PrefixTaskComment),
				WorkspaceID:    w.WorkspaceID,
				RootWorkItemID: state.RootWorkItemID,
				WorkItemID:     w.ID,
				Kind:           domain.CommentReviewFeedback,
				Body:           reason,
				ActorKind:      domain.CommentActorUser,
				ActorID:        commentActorUserID,
				SourceRef:      "work_item.returned",
				CreatedAt:      now,
			}
			if _, err := s.store.TaskComments().Append(ctx, comment); err != nil {
				return err
			}
			if _, err := s.applyRequirementWakeLocked(ctx, w, "用户打回重做", reason); err != nil {
				return err
			}
			freshState, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
			if err != nil {
				return err
			}
			if err := s.emitTaskCommentCreated(ctx, comment, freshState); err != nil {
				return err
			}
			// 事务内审计（s.audit 经 ctx 复用同一事务；失败只记日志不打断命令）。
			s.audit(ctx, w.WorkspaceID, "work_item.returned", w.ID, map[string]any{"reason": reason})
		}
		wi = w
		message := "任务「" + w.Title + "」打回重做（回到执行阶段）：" + reason
		return s.activityFor(ctx, w.WorkspaceID, w.ID, "work_item.returned", message)
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	if coordinated {
		// §7.10：commit 后 StartCoordinator 是 best-effort；失败只记日志，
		// durable queued 由恢复循环继续。
		if err := s.StartCoordinator(context.WithoutCancel(ctx), wi.ID); err != nil {
			log.Printf("return: task %s StartCoordinator 失败（durable queued 由恢复循环兜底）: %v", wi.ID, err)
		}
	}
	return wi, nil
}
