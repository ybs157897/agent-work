package sqlstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type WorkItemRepo struct{ store *Store }

const workItemCols = `id, workspace_id, record_kind, parent_id, title, description, status, phase, priority,
	due_date, agent_profile_id, client_key, locked_by_run_id, locked_at, rolling_digest,
	acceptance_criteria, phase_entered_at, version, created_at, updated_at`

func (r *WorkItemRepo) scan(row interface{ Scan(...any) error }, w *domain.WorkItem) error {
	var recordKind, parent, phase, dueDate, assignee, clientKey, lockedBy, acceptance *string
	var created, updated, lockedAt, phaseEntered scanTime
	if err := row.Scan(&w.ID, &w.WorkspaceID, &recordKind, &parent, &w.Title, &w.Description, &w.Status, &phase,
		&w.Priority, &dueDate, &assignee, &clientKey, &lockedBy, &lockedAt,
		&w.RollingDigest, &acceptance, &phaseEntered,
		&w.Version, &created, &updated); err != nil {
		return err
	}
	if recordKind != nil {
		w.RecordKind = domain.WorkItemRecordKind(*recordKind)
	}
	if parent != nil {
		w.ParentID = *parent
	}
	if phase != nil {
		w.Phase = domain.WorkItemPhase(*phase)
	}
	if dueDate != nil {
		if t, err := time.Parse("2006-01-02", *dueDate); err == nil {
			w.DueDate = &t
		}
	}
	if assignee != nil {
		w.AgentProfileID = *assignee
	}
	if clientKey != nil {
		w.ClientKey = *clientKey
	}
	if lockedBy != nil {
		w.LockedByRunID = *lockedBy
	}
	w.LockedAt = optTime(lockedAt)
	if acceptance != nil {
		if err := jsonInto(*acceptance, &w.AcceptanceCriteria); err != nil {
			return err
		}
	}
	w.PhaseEnteredAt = optTime(phaseEntered)
	w.CreatedAt, w.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

func (r *WorkItemRepo) Create(ctx context.Context, wi *domain.WorkItem) error {
	recordKind := wi.RecordKind
	// Direct/in-process callers predating record_kind represent task-board
	// work items; public Chat callers are normalized by application before
	// reaching the repository.
	if recordKind == "" {
		recordKind = domain.RecordKindTask
	}
	if !recordKind.Valid() {
		return fmt.Errorf("%w: record_kind 必须为 chat 或 task", domain.ErrValidation)
	}
	wi.RecordKind = recordKind
	var due any
	if wi.DueDate != nil {
		due = wi.DueDate.Format("2006-01-02")
	}
	// acceptance_criteria 存 JSON 数组文本；未设置为 NULL（不落 "null" 字面量）。
	var acceptance any
	if len(wi.AcceptanceCriteria) > 0 {
		acceptance = jsonText(wi.AcceptanceCriteria)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO work_items(`+workItemCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		wi.ID, wi.WorkspaceID, recordKind, nullString(wi.ParentID), wi.Title, wi.Description, wi.Status,
		nullString(string(wi.Phase)), wi.Priority, due, nullString(wi.AgentProfileID), nullString(wi.ClientKey),
		nullString(wi.LockedByRunID), nullTimeParam(wi.LockedAt), wi.RollingDigest,
		acceptance, nullTimeParam(wi.PhaseEnteredAt), wi.Version,
		timeParam(wi.CreatedAt), timeParam(wi.UpdatedAt))
	return r.store.mapErr(err)
}

// GetByClientKey 按 (workspace, client_key) 定位既有实体（幂等重放的查回路径）。
func (r *WorkItemRepo) GetByClientKey(ctx context.Context, workspaceID, clientKey string) (*domain.WorkItem, error) {
	w := &domain.WorkItem{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+workItemCols+` FROM work_items WHERE workspace_id=? AND client_key=?`, workspaceID, clientKey)
	if err := r.scan(row, w); err != nil {
		return nil, r.store.mapErr(err)
	}
	return w, nil
}

func (r *WorkItemRepo) Get(ctx context.Context, id string) (*domain.WorkItem, error) {
	w := &domain.WorkItem{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+workItemCols+` FROM work_items WHERE id=?`, id)
	if err := r.scan(row, w); err != nil {
		return nil, r.store.mapErr(err)
	}
	return w, nil
}

// List 支持 record_kind/status/priority/assignee 筛选与 cursor 分页。
// RFC3339Nano UTC 时间字符串定宽，字典序比较在两种方言下均成立。
func (r *WorkItemRepo) List(ctx context.Context, workspaceID string, f application.WorkItemFilter) ([]*domain.WorkItem, string, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"workspace_id=?"}
	args := []any{workspaceID}
	if f.Status != "" {
		args = append(args, f.Status)
		where = append(where, "status=?")
	}
	if f.Priority != "" {
		args = append(args, f.Priority)
		where = append(where, "priority=?")
	}
	if f.Assignee != "" {
		args = append(args, f.Assignee)
		where = append(where, "agent_profile_id=?")
	}
	if f.RecordKind != "" {
		if !f.RecordKind.Valid() {
			return nil, "", fmt.Errorf("%w: record_kind 必须为 chat 或 task", domain.ErrValidation)
		}
		args = append(args, f.RecordKind)
		where = append(where, "record_kind=?")
	}
	if f.ParentID != "" {
		if f.ParentID == "none" {
			where = append(where, "parent_id IS NULL")
		} else {
			args = append(args, f.ParentID)
			where = append(where, "parent_id=?")
		}
	}
	if f.Cursor != "" {
		createdAt, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, timeParam(createdAt), timeParam(createdAt), id)
		where = append(where, "(created_at < ? OR (created_at = ? AND id < ?))")
	}
	args = append(args, limit+1)
	q := `SELECT ` + workItemCols + ` FROM work_items WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY created_at DESC, id DESC LIMIT ?`

	rows, err := r.store.query(ctx, r.store.exec(ctx), q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var out []*domain.WorkItem
	for rows.Next() {
		w := &domain.WorkItem{}
		if err := r.scan(rows, w); err != nil {
			return nil, "", err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(out) > limit {
		last := out[limit-1]
		next = encodeCursor(last.CreatedAt, last.ID)
		out = out[:limit]
	}
	return out, next, nil
}

func (r *WorkItemRepo) Update(ctx context.Context, wi *domain.WorkItem, expectedVersion int) error {
	var due any
	if wi.DueDate != nil {
		due = wi.DueDate.Format("2006-01-02")
	}
	var acceptance any
	if len(wi.AcceptanceCriteria) > 0 {
		acceptance = jsonText(wi.AcceptanceCriteria)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE work_items SET title=?, description=?, status=?, phase=?, priority=?,
		due_date=?, agent_profile_id=?, locked_by_run_id=?, locked_at=?, rolling_digest=?,
		acceptance_criteria=?, phase_entered_at=?,
		version=version+1, updated_at=?
		WHERE id=? AND version=?`,
		wi.Title, wi.Description, wi.Status, nullString(string(wi.Phase)), wi.Priority,
		due, nullString(wi.AgentProfileID), nullString(wi.LockedByRunID), nullTimeParam(wi.LockedAt),
		wi.RollingDigest, acceptance, nullTimeParam(wi.PhaseEnteredAt),
		timeParam(timeNow()), wi.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

// TouchUpdatedAt 仅刷新记录的列表排序时间，不改变 version、status、phase 或执行锁。
// Chat 新消息使用该写点保持最近对话置顶，同时不套用任务状态机。
func (r *WorkItemRepo) TouchUpdatedAt(ctx context.Context, workItemID string, at time.Time) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE work_items SET updated_at=? WHERE id=?`, timeParam(at), workItemID)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpdateRollingDigest 台账摘要的守卫写（会话元模型 S2）：仅 rolling_digest 列 +
// version 乐观锁——并发终态钩子读改写互斥，冲突方重读重算收敛；不 bump
// updated_at，摘要刷新不是任务编辑，不得扰动 CompletedToday 的 updated_at 口径
// 与前端 updated_at 展示。
func (r *WorkItemRepo) UpdateRollingDigest(ctx context.Context, workItemID, digest string, expectedVersion int) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE work_items SET rolling_digest=?, version=version+1 WHERE id=? AND version=?`,
		digest, workItemID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

// ListByParent 按 created_at 升序返回直接子任务（子任务树先序遍历的基础查询）。
func (r *WorkItemRepo) ListByParent(ctx context.Context, parentID string) ([]*domain.WorkItem, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+workItemCols+` FROM work_items WHERE parent_id=? ORDER BY created_at ASC, id ASC`,
		parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.WorkItem
	for rows.Next() {
		w := &domain.WorkItem{}
		if err := r.scan(rows, w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r *WorkItemRepo) ActiveBlocker(ctx context.Context, workItemID string) (*domain.Blocker, error) {
	b := &domain.Blocker{}
	var created scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id, work_item_id, code, message, source, created_at FROM blockers
		 WHERE work_item_id=? AND resolved_at IS NULL ORDER BY created_at DESC LIMIT 1`, workItemID).
		Scan(&b.ID, &b.WorkItemID, &b.Code, &b.Message, &b.Source, &created)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	b.CreatedAt = mustTime(created)
	return b, nil
}

func (r *WorkItemRepo) CreateBlocker(ctx context.Context, b *domain.Blocker) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO blockers(id, work_item_id, code, message, source, created_at) VALUES (?,?,?,?,?,?)`,
		b.ID, b.WorkItemID, b.Code, b.Message, b.Source, timeParam(b.CreatedAt))
	return r.store.mapErr(err)
}

func (r *WorkItemRepo) ResolveBlockers(ctx context.Context, workItemID string, at time.Time) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE blockers SET resolved_at=? WHERE work_item_id=? AND resolved_at IS NULL`,
		timeParam(at), workItemID)
	return r.store.mapErr(err)
}

// runTerminalStatuses 与 domain.RunStatus.IsTerminal 的终态集保持一致：
// ReleaseStaleLocks 的 SQL IN 条件依赖此清单，Run 状态机新增终态必须同步这里。
var runTerminalStatuses = []string{
	string(domain.RunSucceeded), string(domain.RunInterrupted), string(domain.RunCancelled),
	string(domain.RunLost), string(domain.RunFailed),
}

// ReleaseStaleLocks 回收兜底：一把 UPDATE 清空 locked_at 早于 olderThan 且属主
// run 已终态的执行锁。正常路径属主 run 落终态时已在同事务内释放（application
// releaseTaskLock）；这里只兜「终态写入绕过状态机」的异常残留（调度循环低频扫描）。
// 属主 run 仍活跃的锁无论多旧都不动——活性判定只有 run 状态面这一套。
func (r *WorkItemRepo) ReleaseStaleLocks(ctx context.Context, olderThan time.Time) (int, error) {
	placeholders := strings.Repeat("?,", len(runTerminalStatuses))
	args := make([]any, 0, len(runTerminalStatuses)+1)
	args = append(args, timeParam(olderThan))
	for _, s := range runTerminalStatuses {
		args = append(args, s)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE work_items SET locked_by_run_id=NULL, locked_at=NULL
		 WHERE locked_at IS NOT NULL AND locked_at < ?
		   AND locked_by_run_id IN (SELECT id FROM execution_runs
		                            WHERE status IN (`+placeholders[:len(placeholders)-1]+`))`, args...)
	if err != nil {
		return 0, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (r *WorkItemRepo) LatestRunID(ctx context.Context, workItemID string) (string, int, error) {
	var id string
	var count int
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT COALESCE((SELECT id FROM execution_runs WHERE work_item_id=? ORDER BY created_at DESC LIMIT 1),''),
		 (SELECT count(*) FROM execution_runs WHERE work_item_id=?)`, workItemID, workItemID).Scan(&id, &count)
	if err != nil {
		return "", 0, r.store.mapErr(err)
	}
	return id, count, nil
}

func (r *WorkItemRepo) BoardCounts(ctx context.Context, workspaceID string) (map[domain.WorkItemStatus]int, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT status, count(*) FROM work_items WHERE workspace_id=? AND record_kind=? GROUP BY status`,
		workspaceID, domain.RecordKindTask)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.WorkItemStatus]int{
		domain.WorkItemTodo: 0, domain.WorkItemInProgress: 0,
		domain.WorkItemBlocked: 0, domain.WorkItemCompleted: 0, domain.WorkItemCancelled: 0,
	}
	for rows.Next() {
		var s domain.WorkItemStatus
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

func (r *WorkItemRepo) CompletedToday(ctx context.Context, workspaceID string, day time.Time) (int, error) {
	var n int
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT count(*) FROM work_items WHERE workspace_id=? AND record_kind=? AND status='completed' AND updated_at >= ?`,
		workspaceID, domain.RecordKindTask, timeParam(day)).Scan(&n)
	return n, r.store.mapErr(err)
}

// cursor 编码：base64(created_at_RFC3339Nano|id)，不透明 token。
func encodeCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", domain.ErrValidation
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", domain.ErrValidation
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", domain.ErrValidation
	}
	return t, parts[1], nil
}
