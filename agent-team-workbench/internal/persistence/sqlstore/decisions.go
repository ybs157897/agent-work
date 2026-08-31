package sqlstore

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type DecisionRepo struct{ store *Store }

const decisionCols = `id, work_item_id, quote, source_run_id, source_ref, created_at`

func (r *DecisionRepo) scan(row interface{ Scan(...any) error }, e *domain.DecisionEntry) error {
	var sourceRunID, sourceRef *string
	var created scanTime
	if err := row.Scan(&e.ID, &e.WorkItemID, &e.Quote, &sourceRunID, &sourceRef, &created); err != nil {
		return err
	}
	if sourceRunID != nil {
		e.SourceRunID = *sourceRunID
	}
	if sourceRef != nil {
		e.SourceRef = *sourceRef
	}
	e.CreatedAt = mustTime(created)
	return nil
}

// Create 落库决策原话；须与 decision.created 事件同事务。
func (r *DecisionRepo) Create(ctx context.Context, e *domain.DecisionEntry) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO decision_entries(id, work_item_id, quote, source_run_id, source_ref, created_at)
		 VALUES (?,?,?,?,?,?)`,
		e.ID, e.WorkItemID, e.Quote, nullString(e.SourceRunID), nullString(e.SourceRef),
		timeParam(e.CreatedAt))
	return r.store.mapErr(err)
}

// ListByWorkItem 按创建时间升序返回任务台账的决策原话。
func (r *DecisionRepo) ListByWorkItem(ctx context.Context, workItemID string) ([]*domain.DecisionEntry, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+decisionCols+` FROM decision_entries WHERE work_item_id=? ORDER BY created_at, id`, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.DecisionEntry
	for rows.Next() {
		e := &domain.DecisionEntry{}
		if err := r.scan(rows, e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
