// task_comments.go 任务反馈流应用命令（任务控制面 RFC §7.7/§7.8/§9.4/§9.7）。
//
// AppendTaskComment 把 note/requirement 评论原子收口：验证 → 幂等 → cursor 分配
// revision → task_comment.created + activity → kind 分支联动 Coordinator 控制线。
// note 不触发 Coordinator；requirement 是 actionable——waiting_user 原子回
// execution/queued，活动控制轮保留现场等下一 durable turn，blocked 不被评论
// 静默解除（§5.2.6）。commit 后 StartCoordinator 是 best-effort（§7.10）。
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

// 评论族稳定错误码（RFC §9.7 comment 族）：httpapi/problems.go 按哨兵映射
// code/HTTP/retryable。ErrReviewStateConflict 包装 ErrStateConflict，保持既有
// errors.Is 判定兼容（Accept 边界测试）。
var (
	ErrCommentKindInvalid         = errors.New("comment_kind_invalid")
	ErrCommentBodyEmpty           = errors.New("comment_body_empty")
	ErrCommentBodyTooLarge        = errors.New("comment_body_too_large")
	ErrCommentTerminalWorkItem    = errors.New("comment_terminal_work_item")
	ErrCommentSourceRunMismatch   = errors.New("comment_source_run_mismatch")
	ErrCommentCursorInvalid       = errors.New("comment_cursor_invalid")
	ErrCommentCoordinatorRequired = errors.New("comment_coordinator_required")
	ErrReviewFeedbackRequired     = errors.New("review_feedback_required")
	ErrChildReviewNotSupported    = errors.New("child_review_not_supported")
	ErrReviewStateConflict        = fmt.Errorf("%w: review_state_conflict", domain.ErrStateConflict)
)

// commentActorUserID 评论/打回的演示用户身份（与 audit 一致；RBAC 会话化后替换）。
const commentActorUserID = "user_demo"

// AppendTaskCommentParams POST /work-items/{id}/comments 的命令参数。
// HTTP Idempotency-Key 由 httpapi 既有包装处理；ClientKey 是实体级幂等
// （唯一域 (root_work_item_id, client_key)，同 key 不同 body 返回冲突）。
type AppendTaskCommentParams struct {
	WorkItemID              string
	Kind                    domain.CommentKind
	Body                    string
	SourceRunID             string
	SourceRef               string
	ClientKey               string
	ExpectedWorkItemVersion int
}

// AppendTaskComment 追加评论并在同一事务内完成 actionable 联动（RFC §7.7）。
func (s *Service) AppendTaskComment(ctx context.Context, p AppendTaskCommentParams) (*domain.TaskComment, error) {
	// review_feedback 只能由 ReturnWorkItem 写入；通用 POST 伪造一律拒绝。
	if p.Kind != domain.CommentNote && p.Kind != domain.CommentRequirement {
		return nil, ErrCommentKindInvalid
	}
	trimmed := strings.TrimSpace(p.Body)
	if trimmed == "" {
		return nil, ErrCommentBodyEmpty
	}
	if len(trimmed) > domain.CommentBodyMaxBytes {
		return nil, ErrCommentBodyTooLarge
	}
	var (
		created *domain.TaskComment
		queued  bool
	)
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		wi, err := s.store.WorkItems().Get(ctx, p.WorkItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(wi); err != nil {
			return err
		}
		// 评论流只开放给有 Coordinator state 的 Task 树（root 解析随 state 完成，
		// 子项与根天然同树同 Workspace）。
		state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, wi.ID)
		if errors.Is(err, domain.ErrNotFound) {
			return ErrCommentCoordinatorRequired
		}
		if err != nil {
			return err
		}
		root, err := s.store.WorkItems().Get(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if root.Status.IsTerminal() || state.Status == domain.CoordinatorCompleted ||
			state.Status == domain.CoordinatorCancelled {
			return ErrCommentTerminalWorkItem
		}
		if p.ExpectedWorkItemVersion != 0 {
			if err := wi.CheckVersion(p.ExpectedWorkItemVersion); err != nil {
				return err
			}
		}
		if p.SourceRunID != "" {
			run, err := s.store.Runs().Get(ctx, p.SourceRunID)
			if err != nil {
				return err
			}
			runRootID, err := s.workItemTreeRootID(ctx, run.WorkItemID)
			if err != nil {
				return err
			}
			if runRootID != state.RootWorkItemID {
				return ErrCommentSourceRunMismatch
			}
		}
		comment := &domain.TaskComment{
			ID:             domain.NewID(domain.PrefixTaskComment),
			WorkspaceID:    wi.WorkspaceID,
			RootWorkItemID: state.RootWorkItemID,
			WorkItemID:     wi.ID,
			Kind:           p.Kind,
			Body:           p.Body,
			ActorKind:      domain.CommentActorUser,
			ActorID:        commentActorUserID,
			SourceRunID:    p.SourceRunID,
			SourceRef:      p.SourceRef,
			ClientKey:      p.ClientKey,
			CreatedAt:      time.Now().UTC(),
		}
		created, err = s.store.TaskComments().Append(ctx, comment)
		if err != nil {
			return err
		}
		if p.Kind == domain.CommentRequirement {
			// actionable 联动先于事件发布，保证 task_comment.created 携带提交时
			// 的 durable coordinator_status（§10）。
			var wakeErr error
			queued, wakeErr = s.applyRequirementWakeLocked(ctx, root, "收到新的任务反馈", trimmed)
			if wakeErr != nil {
				return wakeErr
			}
		}
		freshState, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		// §5.2.9/§7.10 竞态收口：Accept 可能在本事务期间提交（任务转 completed）。
		// 事务末尾复检终态，输掉竞态的一方整体回滚——Accept 与评论不可能同时成功。
		freshRoot, err := s.store.WorkItems().Get(ctx, root.ID)
		if err != nil {
			return err
		}
		if freshRoot.Status.IsTerminal() || freshState.Status == domain.CoordinatorCompleted ||
			freshState.Status == domain.CoordinatorCancelled {
			return ErrCommentTerminalWorkItem
		}
		if err := s.emitTaskCommentCreated(ctx, created, freshState); err != nil {
			return err
		}
		return s.activityFor(ctx, root.WorkspaceID, wi.ID, "task_comment.created",
			"任务「"+root.Title+"」新增"+commentKindLabel(p.Kind)+"："+activityExcerpt(trimmed))
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(created.WorkspaceID)
	if queued {
		// §7.10：提交后的 StartCoordinator 是 best-effort；失败只记日志，
		// durable queued 由恢复循环继续（同 Idempotency-Key 重放返回原评论）。
		if err := s.StartCoordinator(context.WithoutCancel(ctx), created.RootWorkItemID); err != nil {
			log.Printf("comment: task %s StartCoordinator 失败（durable queued 由恢复循环兜底）: %v",
				created.RootWorkItemID, err)
		}
	}
	return created, nil
}

// TaskCommentListResult 评论分页（revision 正序；cursor 与 SSE stream_seq 严格分离）。
type TaskCommentListResult struct {
	Items          []*domain.TaskComment
	NextRevision   *int64
	LatestRevision int64
}

// ListTaskComments 根 Task 维度只读分页；历史非 Coordinator Task 返回
// ErrCommentCoordinatorRequired（GET/POST 同口径，§4.9）。
func (s *Service) ListTaskComments(ctx context.Context, workItemID string, afterRevision, limit int64) (*TaskCommentListResult, error) {
	if afterRevision < 0 {
		return nil, ErrCommentCursorInvalid
	}
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, ErrCommentCoordinatorRequired
	}
	if err != nil {
		return nil, err
	}
	latest, err := s.store.TaskComments().LatestRevision(ctx, state.RootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, ErrCommentCoordinatorRequired
	}
	if err != nil {
		return nil, err
	}
	items, err := s.store.TaskComments().ListByRoot(ctx, state.RootWorkItemID, afterRevision, int(limit))
	if err != nil {
		return nil, err
	}
	pageSize := int64(len(items))
	if limit > 0 && pageSize == limit {
		// after_revision 使用严格大于语义，下一页游标必须等于本页最大
		// revision；返回 max+1 会让调用方跳过紧邻的下一条评论。
		next := items[len(items)-1].Revision
		return &TaskCommentListResult{Items: items, NextRevision: &next, LatestRevision: latest}, nil
	}
	return &TaskCommentListResult{Items: items, LatestRevision: latest}, nil
}

// applyRequirementWakeLocked 处理 actionable 评论到达时的根控制线联动（§7.7）。
// 必须在调用方事务内执行；返回 durable queued（可 best-effort StartCoordinator）。
//
// 分支：
//   - waiting_user：WorkItem 回 execution + Coordinator CAS queued/message；
//   - running 且无 current Run、无控制动作：树内无活动 Worker/settlement 时
//     CAS queued/message，否则保留 observation checkpoint；
//   - queued：已是 durable queued，返回 true（StartCoordinator 幂等驱动）；
//   - waiting_retry：重试检查点不被覆盖，评论由重试后的下一轮消费；
//   - blocked：评论不静默解除 blocker（§5.2.6），由 Unblock 命令显式恢复；
//   - running 且有活动控制轮：保留现场（不对活动 Run 做必达 steering）。
func (s *Service) applyRequirementWakeLocked(ctx context.Context, root *domain.WorkItem, summary, reason string) (bool, error) {
	fresh, err := s.store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		return false, err
	}
	switch fresh.Status {
	case domain.CoordinatorCompleted, domain.CoordinatorCancelled, domain.CoordinatorBlocked:
		return false, nil
	case domain.CoordinatorWaitingUser:
		if root.Status == domain.WorkItemInProgress && root.Phase != domain.PhaseExecution {
			expected := root.Version
			root.BeginExecution(time.Now().UTC())
			if err := s.store.WorkItems().Update(ctx, root, expected); err != nil {
				return false, err
			}
			if err := s.emit(ctx, root.WorkspaceID, domain.EventWorkItemUpdated,
				domain.AggregateWorkItem, root.ID, root.Version, nil,
				map[string]any{"phase": string(root.Phase), "record_kind": string(workItemRecordKind(root))}); err != nil {
				return false, err
			}
		}
		expected := fresh.Version
		fresh.Status = domain.CoordinatorQueued
		fresh.Phase = "message"
		fresh.CurrentAction = "message"
		fresh.NextActionAt = nil
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return false, err
		}
		fresh.Version = expected + 1
		return true, s.appendCoordinatorEvent(ctx, fresh, root.ID, domain.EventCoordinatorMessageReceived,
			summary, "", fresh.CoordinatorAgentID, fresh.Attempt, reason, nil,
			map[string]any{"stage": "message", "next_action": "合并最新任务反馈重新规划"})
	case domain.CoordinatorRunning:
		if fresh.CurrentRunID != "" || fresh.NextActionAt != nil || coordinatorControlAction(fresh) != "" {
			return false, nil // 活动控制轮 / 退避重试检查点：保留现场
		}
		active, err := s.taskTreeHasActiveRuns(ctx, root.ID)
		if err != nil {
			return false, err
		}
		if active || coordinatorSettlementPending(fresh) {
			return false, nil // 活动 Worker/settlement：observation checkpoint，settlement 后消费
		}
		expected := fresh.Version
		fresh.Status = domain.CoordinatorQueued
		fresh.Phase = "message"
		fresh.CurrentAction = "message"
		fresh.NextActionAt = nil
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return false, err
		}
		fresh.Version = expected + 1
		return true, s.appendCoordinatorEvent(ctx, fresh, root.ID, domain.EventCoordinatorMessageReceived,
			summary, "", fresh.CoordinatorAgentID, fresh.Attempt, reason, nil,
			map[string]any{"stage": "message", "next_action": "消费未处理的任务反馈"})
	case domain.CoordinatorQueued:
		return true, nil
	default: // waiting_retry 等：保留检查点
		return false, nil
	}
}

// taskTreeHasActiveRuns 报告根任务子树内是否仍有非终态 Run 或未收口派发批次
// （§7.7 observation checkpoint 的「活动 Worker/settlement」判定）。
func (s *Service) taskTreeHasActiveRuns(ctx context.Context, rootID string) (bool, error) {
	seen := map[string]bool{rootID: true}
	queue := []string{rootID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		runs, err := s.store.Runs().ListByWorkItem(ctx, id)
		if err != nil {
			return false, err
		}
		for _, run := range runs {
			if run != nil && !run.Status.IsTerminal() {
				return true, nil
			}
		}
		children, err := s.store.WorkItems().ListByParent(ctx, id)
		if err != nil {
			return false, err
		}
		for _, child := range children {
			if child != nil && !seen[child.ID] {
				seen[child.ID] = true
				queue = append(queue, child.ID)
			}
		}
	}
	dispatches, err := s.store.Dispatches().ListByWorkItem(ctx, rootID)
	if err != nil {
		return false, err
	}
	for _, dispatch := range dispatches {
		if dispatch != nil && !dispatch.Status.IsTerminal() {
			return true, nil
		}
	}
	return false, nil
}

// workItemTreeRootID 沿父链解析根 WorkItem（source_run 归属校验用）。
func (s *Service) workItemTreeRootID(ctx context.Context, workItemID string) (string, error) {
	current := workItemID
	for hops := 0; hops < 100; hops++ {
		wi, err := s.store.WorkItems().Get(ctx, current)
		if err != nil {
			return "", err
		}
		if wi.ParentID == "" {
			return wi.ID, nil
		}
		current = wi.ParentID
	}
	return "", fmt.Errorf("%w: 任务层级超过 100 层", domain.ErrValidation)
}

// emitTaskCommentCreated 同事务发布 task_comment.created（§10 最小 data：不含
// body，前端按 work item/root 失效重取）；coordinator_status 为评论事务提交时
// 根 Coordinator 的 durable 状态。
func (s *Service) emitTaskCommentCreated(ctx context.Context, c *domain.TaskComment, state *domain.TaskCoordinatorState) error {
	data := map[string]any{
		"record_kind":       string(domain.RecordKindTask),
		"comment_id":        c.ID,
		"root_work_item_id": c.RootWorkItemID,
		"work_item_id":      c.WorkItemID,
		"revision":          c.Revision,
		"kind":              string(c.Kind),
		"actionable":        c.Kind.Actionable(),
	}
	if state != nil {
		data["coordinator_status"] = string(state.Status)
	}
	return s.emit(ctx, c.WorkspaceID, domain.EventTaskCommentCreated,
		domain.AggregateTaskComment, c.ID, 1, nil, data)
}

// coordinatorCommentSnapshot 读取消费水位之后的全部未消费评论，产出决定性
// 快照（§7.8）：同时进入 TASK_DATA_JSON_V1 envelope 与 Run input，保证重启后
// 可审计重建；from/to 为确定性 revision 闭包边界。
func (s *Service) coordinatorCommentSnapshot(ctx context.Context, rootWorkItemID string, consumedRevision int64) ([]CoordinatorComment, int64, int64, error) {
	unconsumed, err := s.store.TaskComments().ListUnconsumed(ctx, rootWorkItemID, consumedRevision)
	if err != nil {
		return nil, 0, 0, err
	}
	comments := make([]CoordinatorComment, 0, len(unconsumed))
	from, to := consumedRevision, consumedRevision
	for _, c := range unconsumed {
		if from == consumedRevision && len(comments) == 0 {
			from = c.Revision
		}
		to = c.Revision
		comments = append(comments, CoordinatorComment{
			ID: c.ID, WorkItemID: c.WorkItemID, Revision: c.Revision, Kind: string(c.Kind),
			Body: c.Body, ActorKind: string(c.ActorKind), ActorID: c.ActorID,
			SourceRunID: c.SourceRunID, SourceRef: c.SourceRef,
			CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return comments, from, to, nil
}

func commentKindLabel(kind domain.CommentKind) string {
	if kind == domain.CommentRequirement {
		return "需求反馈"
	}
	return "备注"
}

// activityExcerpt activity 文本摘录宽度（对齐派发卡片的一行可读约定）。
func activityExcerpt(text string) string {
	runes := []rune(text)
	if len(runes) <= 120 {
		return text
	}
	return string(runes[:120]) + "…"
}
