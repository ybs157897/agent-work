package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// task_comments.go 承载 append-only TaskComment 仓储（任务控制面 RFC §4.9）。
// revision 由根级 cursor 行在事务内锁定自增分配（禁止 MAX(revision)+1）；
// 无 update/delete 写点——append-only 是 §5.2.1 不变式。

type TaskCommentRepo struct{ store *Store }

var _ application.TaskCommentRepo = (*TaskCommentRepo)(nil)

const taskCommentCols = `id, workspace_id, root_work_item_id, work_item_id, revision,
	kind, body, actor_kind, actor_id, source_run_id, source_ref, client_key, created_at`

// EnsureCursor 幂等创建 latest_revision=0 的 cursor 行；与根 Coordinator state
// 同事务调用（RFC §6.2：cursor 永不物理删除，重复创建无害）。
func (r *TaskCommentRepo) EnsureCursor(ctx context.Context, rootWorkItemID string) error {
	if rootWorkItemID == "" {
		return fmt.Errorf("%w: root work item id required", domain.ErrValidation)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_comment_cursors(root_work_item_id, latest_revision) VALUES (?,0)
		 ON CONFLICT (root_work_item_id) DO NOTHING`, rootWorkItemID)
	return r.store.mapErr(err)
}

// Append 锁定 cursor 行分配单调 revision 并插入评论。client_key 非空时撞唯一
// 索引：同 root 同 key 且 body 一致 → 幂等重放既有行；body 不同 →
// ErrIdempotencyConflict。调用方必须在事务内调用（revision 分配与插入原子）。
func (r *TaskCommentRepo) Append(ctx context.Context, c *domain.TaskComment) (*domain.TaskComment, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: comment required", domain.ErrValidation)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	// RFC §6.2 双验证的存储侧：root/work_item 同 Workspace 且均为 task。
	var scopeCount int
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT COUNT(*) FROM work_items root
		 JOIN work_items item ON item.id=?
		 WHERE root.id=? AND root.workspace_id=item.workspace_id
		   AND root.record_kind=? AND item.record_kind=?`,
		c.WorkItemID, c.RootWorkItemID, domain.RecordKindTask, domain.RecordKindTask,
	).Scan(&scopeCount); err != nil {
		return nil, r.store.mapErr(err)
	}
	if scopeCount != 1 {
		return nil, fmt.Errorf("%w: comment root/work_item 必须同 Workspace 且均为 task", domain.ErrValidation)
	}
	if c.ID == "" {
		c.ID = domain.NewID(domain.PrefixTaskComment)
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = timeNow()
	}
	// 先以无值变化的 UPDATE 锁住 cursor 行，再检查 client_key。这样同一 root
	// 的并发 Append 必须在这把锁后重查，也不会让幂等重放推进 revision 水位。
	var currentRevision int64
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`UPDATE task_comment_cursors SET latest_revision = latest_revision
		 WHERE root_work_item_id = ? RETURNING latest_revision`, c.RootWorkItemID,
	).Scan(&currentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 无 cursor = 历史非 Coordinator Task（comment_coordinator_required 的映射依据）。
			return nil, domain.ErrNotFound
		}
		return nil, r.store.mapErr(err)
	}
	if c.ClientKey != "" {
		if existing, getErr := r.getByClientKey(ctx, c.RootWorkItemID, c.ClientKey); getErr == nil && existing != nil {
			if existing.Body == c.Body {
				return existing, nil
			}
			return nil, domain.ErrIdempotencyConflict
		} else if !errors.Is(getErr, sql.ErrNoRows) && getErr != nil {
			return nil, r.store.mapErr(getErr)
		}
	}
	// 同一事务的第二次 UPDATE 才实际分配 revision；锁仍由上一步持有。
	var revision int64
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`UPDATE task_comment_cursors SET latest_revision = latest_revision + 1
		 WHERE root_work_item_id = ? RETURNING latest_revision`, c.RootWorkItemID,
	).Scan(&revision); err != nil {
		return nil, r.store.mapErr(err)
	}
	c.Revision = revision
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_comments(`+taskCommentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.WorkspaceID, c.RootWorkItemID, c.WorkItemID, c.Revision,
		c.Kind, c.Body, c.ActorKind, c.ActorID,
		nullString(c.SourceRunID), nullString(c.SourceRef), nullString(c.ClientKey),
		timeParam(c.CreatedAt))
	if err != nil {
		if sqliteUniqueViolation(err) {
			// A raw/legacy writer that bypasses the cursor lock can still collide.
			// Return an error so the enclosing transaction rolls back its allocated
			// revision before another writer can observe it.
			return nil, domain.ErrIdempotencyConflict
		}
		return nil, r.store.mapErr(err)
	}
	return c, nil
}

func (r *TaskCommentRepo) getByClientKey(ctx context.Context, rootWorkItemID, clientKey string) (*domain.TaskComment, error) {
	c := &domain.TaskComment{}
	if err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+taskCommentCols+` FROM task_comments WHERE root_work_item_id=? AND client_key=?`,
		rootWorkItemID, clientKey), c); err != nil {
		return nil, err
	}
	return c, nil
}

func (r *TaskCommentRepo) scan(row interface{ Scan(...any) error }, c *domain.TaskComment) error {
	var sourceRun, sourceRef, clientKey *string
	var created scanTime
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.RootWorkItemID, &c.WorkItemID, &c.Revision,
		&c.Kind, &c.Body, &c.ActorKind, &c.ActorID, &sourceRun, &sourceRef, &clientKey, &created); err != nil {
		return err
	}
	if sourceRun != nil {
		c.SourceRunID = *sourceRun
	}
	if sourceRef != nil {
		c.SourceRef = *sourceRef
	}
	if clientKey != nil {
		c.ClientKey = *clientKey
	}
	c.CreatedAt = mustTime(created)
	return nil
}

// ListByRoot 按 revision 正序分页（afterRevision 之后，limit 截断）。
func (r *TaskCommentRepo) ListByRoot(ctx context.Context, rootWorkItemID string, afterRevision int64, limit int) ([]*domain.TaskComment, error) {
	if rootWorkItemID == "" {
		return nil, fmt.Errorf("%w: root work item id required", domain.ErrValidation)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+taskCommentCols+` FROM task_comments
		 WHERE root_work_item_id=? AND revision>? ORDER BY revision ASC LIMIT ?`,
		rootWorkItemID, afterRevision, limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	out := []*domain.TaskComment{}
	for rows.Next() {
		c := &domain.TaskComment{}
		if err := r.scan(rows, c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestRevision 返回 cursor 水位；无 cursor 返回 ErrNotFound。
func (r *TaskCommentRepo) LatestRevision(ctx context.Context, rootWorkItemID string) (int64, error) {
	var latest int64
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT latest_revision FROM task_comment_cursors WHERE root_work_item_id=?`,
		rootWorkItemID).Scan(&latest); err != nil {
		return 0, r.store.mapErr(err)
	}
	return latest, nil
}

// HasUnconsumedActionable 报告是否存在 revision > consumed 的 actionable 评论
// （kind IN (requirement, review_feedback)；note 不算，RFC §7.11）。
func (r *TaskCommentRepo) HasUnconsumedActionable(ctx context.Context, rootWorkItemID string, consumedRevision int64) (bool, error) {
	if rootWorkItemID == "" {
		return false, fmt.Errorf("%w: root work item id required", domain.ErrValidation)
	}
	var has bool
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT EXISTS (SELECT 1 FROM task_comments
		 WHERE root_work_item_id=? AND revision>? AND kind IN (?,?))`,
		rootWorkItemID, consumedRevision,
		domain.CommentRequirement, domain.CommentReviewFeedback).Scan(&has); err != nil {
		return false, r.store.mapErr(err)
	}
	return has, nil
}

// ListUnconsumed 返回 revision > consumed 的全部评论（正序），供 Coordinator
// Run 创建事务快照进 Run input 并推进 consumed_comment_revision。
func (r *TaskCommentRepo) ListUnconsumed(ctx context.Context, rootWorkItemID string, consumedRevision int64) ([]*domain.TaskComment, error) {
	if rootWorkItemID == "" {
		return nil, fmt.Errorf("%w: root work item id required", domain.ErrValidation)
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+taskCommentCols+` FROM task_comments
		 WHERE root_work_item_id=? AND revision>? ORDER BY revision ASC`,
		rootWorkItemID, consumedRevision)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	out := []*domain.TaskComment{}
	for rows.Next() {
		c := &domain.TaskComment{}
		if err := r.scan(rows, c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
