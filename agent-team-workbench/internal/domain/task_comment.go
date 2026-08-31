package domain

import (
	"fmt"
	"strings"
	"time"
)

// ── TaskComment（任务控制面 RFC §4.9）──────────────────────────────────
//
// TaskComment 是独立 append-only 资源：普通备注、需求变更、验收打回不再混入
// activity/decision/单槽 pending_instruction。revision 在根 Task 维度单调分配；
// 无 update/delete。body/actor/source 进入模型前均是不可信数据（§11 红线）。

type CommentKind string

const (
	// CommentNote 不触发 Coordinator；只进 UI/审计。
	CommentNote CommentKind = "note"
	// CommentRequirement 是 actionable：触发 durable queued/recovery。
	CommentRequirement CommentKind = "requirement"
	// CommentReviewFeedback 只能由 ReturnWorkItem 命令写入；通用 POST 不允许伪造。
	CommentReviewFeedback CommentKind = "review_feedback"
)

func (k CommentKind) Valid() bool {
	return k == CommentNote || k == CommentRequirement || k == CommentReviewFeedback
}

// Actionable 报告该 kind 是否必须进入 durable Coordinator 控制线。
func (k CommentKind) Actionable() bool {
	return k == CommentRequirement || k == CommentReviewFeedback
}

type CommentActorKind string

const (
	CommentActorUser    CommentActorKind = "user"
	CommentActorSystem  CommentActorKind = "system"
	CommentActorRuntime CommentActorKind = "runtime"
)

func (k CommentActorKind) Valid() bool {
	return k == CommentActorUser || k == CommentActorSystem || k == CommentActorRuntime
}

// CommentBodyMaxBytes：trim 后 1..16384 字节（与 0022 CHECK 同口径）。
const CommentBodyMaxBytes = 16384

// TaskComment 一次追加的不可变记录。
type TaskComment struct {
	ID             string
	WorkspaceID    string
	RootWorkItemID string
	WorkItemID     string
	Revision       int64
	Kind           CommentKind
	Body           string
	ActorKind      CommentActorKind
	ActorID        string
	SourceRunID    string
	SourceRef      string
	ClientKey      string
	CreatedAt      time.Time
}

// Validate 校验静态形态（归属/树校验归应用层）。body 保留原文不 trim 存储，
// 但长度口径按 trim 后计算（与迁移 CHECK 一致）。
func (c *TaskComment) Validate() error {
	if !c.Kind.Valid() {
		return fmt.Errorf("%w: comment kind %q", ErrValidation, c.Kind)
	}
	if !c.ActorKind.Valid() {
		return fmt.Errorf("%w: comment actor_kind %q", ErrValidation, c.ActorKind)
	}
	trimmed := strings.TrimSpace(c.Body)
	if trimmed == "" {
		return fmt.Errorf("%w: comment_body_empty", ErrValidation)
	}
	if len(trimmed) > CommentBodyMaxBytes {
		return fmt.Errorf("%w: comment_body_too_large", ErrValidation)
	}
	if c.WorkspaceID == "" || c.RootWorkItemID == "" || c.WorkItemID == "" {
		return fmt.Errorf("%w: comment 缺 workspace/root/work_item", ErrValidation)
	}
	return nil
}
