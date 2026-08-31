package sqlstore

import (
	"context"
	"time"

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
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO dispatches(id, work_item_id, trigger, lead_run_id, status, created_at, closed_at)
		 VALUES (?,?,?,?,?,?,?)`,
		d.ID, d.WorkItemID, d.Trigger, nullString(d.LeadRunID), d.Status,
		timeParam(d.CreatedAt), nullTimeParam(d.ClosedAt))
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

// SetLeadRun 接诊批次回填接诊 run id（成员 run 行落库后同事务调用）。
func (r *DispatchRepo) SetLeadRun(ctx context.Context, id, leadRunID string) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE dispatches SET lead_run_id=? WHERE id=?`, leadRunID, id)
	return r.store.mapErr(err)
}

// MarkCollecting 回流前置迁移（会话元模型 S3）：running→collecting 的 CAS。
// 只有把批从 running 迁到 collecting 的触发方获得「唤醒 lead」资格；
// collecting 下的重复触发（并发终态事件、唤醒重放）一律 0 行 no-op——
// 这是「只唤醒一次」的存储层硬保证。返回是否真正迁移。
func (r *DispatchRepo) MarkCollecting(ctx context.Context, id string) (bool, error) {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE dispatches SET status='collecting' WHERE id=? AND status='running'`, id)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CloseStatus 批次收口 CAS：running/collecting → 终态（completed/degraded/
// cancelled），单向写入（终态行不可再被本路径改写）。0 行 = 已被并发方收口
// → 调用方 no-op。返回是否真正收口。
func (r *DispatchRepo) CloseStatus(ctx context.Context, id string, to domain.DispatchStatus, closedAt time.Time) (bool, error) {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE dispatches SET status=?, closed_at=? WHERE id=? AND status IN ('running','collecting')`,
		to, timeParam(closedAt), id)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
