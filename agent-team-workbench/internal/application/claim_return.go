// claim_return.go M4 认领模式与手动打回命令（设计 note
// notes/implemented/orchestration/2026-08-24-m4-claim-join-guardrails.md §1/§5）：
// claim = 发布→领取（指派+唤醒的复合命令，无一等实体）；return = 验收回流的
// 人工半环（acceptance/review → execution）。两者均以 ErrStateConflict 表达
// 「命令与当前状态冲突」（httpapi 映射 409）。
package application

import (
	"context"
	"fmt"
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

// ReturnWorkItem 手动打回（验收回流的人工半环）：in_progress 且 phase 为
// review/acceptance 时合法 → BeginExecution 回 execution 并把 reason 落
// activity；再交付路径 = 与对应 agent 继续 chat（会话锚点仍在）→ 新 run。
// todo/completed/cancelled/blocked → ErrStateConflict。
func (s *Service) ReturnWorkItem(ctx context.Context, workItemID string, reason string, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if w.Status != domain.WorkItemInProgress ||
			(w.Phase != domain.PhaseReview && w.Phase != domain.PhaseAcceptance) {
			return fmt.Errorf("%w: 仅 in_progress 且 phase=review/acceptance 可打回，当前 %s/%s",
				domain.ErrStateConflict, w.Status, w.Phase)
		}
		from := w.Phase
		expected := w.Version
		w.BeginExecution(time.Now().UTC())
		if err := s.store.WorkItems().Update(ctx, w, expected); err != nil {
			return err
		}
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemUpdated,
			domain.AggregateWorkItem, w.ID, w.Version, nil,
			map[string]any{"phase": string(w.Phase), "returned_from": string(from),
				"record_kind": string(workItemRecordKind(w))}); err != nil {
			return err
		}
		wi = w
		message := "任务「" + w.Title + "」打回重做（回到执行阶段）"
		if reason != "" {
			message += "：" + reason
		}
		return s.activityFor(ctx, w.WorkspaceID, w.ID, "work_item.returned", message)
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	return wi, nil
}
