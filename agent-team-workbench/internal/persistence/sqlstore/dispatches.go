package sqlstore

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type DispatchRepo struct{ store *Store }

const dispatchCols = `id, work_item_id, trigger, lead_run_id, status, created_at, closed_at`

func (r *DispatchRepo) scan(row interface{ Scan(...any) error }, d *domain.Dispatch) error {
	var leadRunID *string
	var created, closed scanTime
	if err := row.Scan(&d.ID, &d.WorkItemID, &d.Trigger, &leadRunID, &d.Status, &created, &closed); err != nil {
		return err
	}
	if leadRunID != nil {
		d.LeadRunID = *leadRunID
	}
	d.CreatedAt, d.ClosedAt = mustTime(created), optTime(closed)
	return nil
}

// Create 落库派发批次；必须在创建成员 run 的同一事务内、且先于成员行
// （execution_runs.dispatch_id 外键指向本表）。
func (r *DispatchRepo) Create(ctx context.Context, d *domain.Dispatch) error {
	dm := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO dispatches(id, work_item_id, trigger, lead_run_id, status, created_at, closed_at)
		 VALUES (?,?,?,?,?,?,?)`,
		d.ID, d.WorkItemID, d.Trigger, nullString(d.LeadRunID), d.Status,
		dm.TimeParam(d.CreatedAt), dm.NullTimeParam(d.ClosedAt))
	return r.store.mapErr(err)
}

func (r *DispatchRepo) Get(ctx context.Context, id string) (*domain.Dispatch, error) {
	d := &domain.Dispatch{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+dispatchCols+` FROM dispatches WHERE id=?`, id)
	if err := r.scan(row, d); err != nil {
		return nil, r.store.mapErr(err)
	}
	return d, nil
}

// ListByWorkItem 按创建时间升序返回任务的全部批次（派发卡片端点倒序展示）。
func (r *DispatchRepo) ListByWorkItem(ctx context.Context, workItemID string) ([]*domain.Dispatch, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+dispatchCols+` FROM dispatches WHERE work_item_id=? ORDER BY created_at, id`, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Dispatch
	for rows.Next() {
		d := &domain.Dispatch{}
		if err := r.scan(rows, d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
