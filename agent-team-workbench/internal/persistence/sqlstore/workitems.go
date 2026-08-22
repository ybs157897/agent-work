package sqlstore

import (
	"context"
	"encoding/base64"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type WorkItemRepo struct{ store *Store }

const workItemCols = `id, workspace_id, title, description, status, phase, priority,
	due_date, agent_profile_id, version, created_at, updated_at`

func (r *WorkItemRepo) scan(row interface{ Scan(...any) error }, w *domain.WorkItem) error {
	var phase, dueDate, assignee *string
	var created, updated scanTime
	if err := row.Scan(&w.ID, &w.WorkspaceID, &w.Title, &w.Description, &w.Status, &phase, &w.Priority,
		&dueDate, &assignee, &w.Version, &created, &updated); err != nil {
		return err
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
	w.CreatedAt, w.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

func (r *WorkItemRepo) Create(ctx context.Context, wi *domain.WorkItem) error {
	d := r.store.dialect
	var due any
	if wi.DueDate != nil {
		due = wi.DueDate.Format("2006-01-02")
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO work_items(`+workItemCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		wi.ID, wi.WorkspaceID, wi.Title, wi.Description, wi.Status, nullString(string(wi.Phase)),
		wi.Priority, due, nullString(wi.AgentProfileID), wi.Version,
		d.TimeParam(wi.CreatedAt), d.TimeParam(wi.UpdatedAt))
	return r.store.mapErr(err)
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

// List 支持 status/priority/assignee 筛选与 cursor 分页。
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
	if f.Cursor != "" {
		createdAt, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, r.store.dialect.TimeParam(createdAt), r.store.dialect.TimeParam(createdAt), id)
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
	d := r.store.dialect
	var due any
	if wi.DueDate != nil {
		due = wi.DueDate.Format("2006-01-02")
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE work_items SET title=?, description=?, status=?, phase=?, priority=?,
			due_date=?, agent_profile_id=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		wi.Title, wi.Description, wi.Status, nullString(string(wi.Phase)), wi.Priority,
		due, nullString(wi.AgentProfileID), d.TimeParam(timeNow()), wi.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
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
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO blockers(id, work_item_id, code, message, source, created_at) VALUES (?,?,?,?,?,?)`,
		b.ID, b.WorkItemID, b.Code, b.Message, b.Source, d.TimeParam(b.CreatedAt))
	return r.store.mapErr(err)
}

func (r *WorkItemRepo) ResolveBlockers(ctx context.Context, workItemID string, at time.Time) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE blockers SET resolved_at=? WHERE work_item_id=? AND resolved_at IS NULL`,
		d.TimeParam(at), workItemID)
	return r.store.mapErr(err)
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
		`SELECT status, count(*) FROM work_items WHERE workspace_id=? GROUP BY status`, workspaceID)
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
		`SELECT count(*) FROM work_items WHERE workspace_id=? AND status='completed' AND updated_at >= ?`,
		workspaceID, r.store.dialect.TimeParam(day)).Scan(&n)
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
