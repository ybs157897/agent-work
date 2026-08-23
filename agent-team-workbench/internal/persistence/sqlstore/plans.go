package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// PlanRepo 维护 plans / plan_steps（M1 编排层）。
// Create 必须在事务内调用（plan + steps 同事务落库）；Update/UpdateStep 走乐观锁。
type PlanRepo struct{ store *Store }

const planCols = `id, workspace_id, work_item_id, agent_profile_id, source_run_id,
	status, superseded_by, version, created_at, updated_at`

const planStepCols = `plan_id, seq, verb, payload, status, result_work_item_id,
	result_run_id, error, created_at, executed_at`

func (r *PlanRepo) scan(row interface{ Scan(...any) error }, p *domain.Plan) error {
	var sourceRunID, supersededBy *string
	var created, updated scanTime
	if err := row.Scan(&p.ID, &p.WorkspaceID, &p.WorkItemID, &p.AgentProfileID, &sourceRunID,
		&p.Status, &supersededBy, &p.Version, &created, &updated); err != nil {
		return err
	}
	if sourceRunID != nil {
		p.SourceRunID = *sourceRunID
	}
	if supersededBy != nil {
		p.SupersededBy = *supersededBy
	}
	p.CreatedAt, p.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

// Create 写入 plan 及其全部 steps；步骤 seq 从 0 起连续（由应用层构造保证）。
func (r *PlanRepo) Create(ctx context.Context, p *domain.Plan) error {
	d := r.store.dialect
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO plans(`+planCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.WorkspaceID, p.WorkItemID, p.AgentProfileID, nullString(p.SourceRunID),
		p.Status, nullString(p.SupersededBy), p.Version,
		d.TimeParam(p.CreatedAt), d.TimeParam(p.UpdatedAt)); err != nil {
		return r.store.mapErr(err)
	}
	for i := range p.Steps {
		if err := r.insertStep(ctx, &p.Steps[i]); err != nil {
			return err
		}
	}
	return nil
}

func (r *PlanRepo) insertStep(ctx context.Context, st *domain.PlanStep) error {
	d := r.store.dialect
	var executedAt any
	if st.ExecutedAt != nil {
		executedAt = d.NullTimeParam(st.ExecutedAt)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO plan_steps(`+planStepCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		st.PlanID, st.Seq, st.Verb, jsonText(st.Payload), st.Status,
		nullString(st.ResultWorkItemID), nullString(st.ResultRunID), nullString(st.Error),
		d.TimeParam(st.CreatedAt), executedAt)
	return r.store.mapErr(err)
}

// Get 返回 plan（含按 seq 升序的全部 steps）。
func (r *PlanRepo) Get(ctx context.Context, id string) (*domain.Plan, error) {
	p := &domain.Plan{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+planCols+` FROM plans WHERE id=?`, id)
	if err := r.scan(row, p); err != nil {
		return nil, r.store.mapErr(err)
	}
	return p, r.loadSteps(ctx, p)
}

// loadSteps 按 seq 升序装载 p.Steps（Get / ActiveByWorkItem 内部使用）。
func (r *PlanRepo) loadSteps(ctx context.Context, p *domain.Plan) error {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+planStepCols+` FROM plan_steps WHERE plan_id=? ORDER BY seq ASC`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		st := domain.PlanStep{}
		var payload string
		var resultWI, resultRun, stepErr *string
		var created, executedAt scanTime
		if err := rows.Scan(&st.PlanID, &st.Seq, &st.Verb, &payload, &st.Status,
			&resultWI, &resultRun, &stepErr, &created, &executedAt); err != nil {
			return err
		}
		if err := jsonInto(payload, &st.Payload); err != nil {
			return err
		}
		if resultWI != nil {
			st.ResultWorkItemID = *resultWI
		}
		if resultRun != nil {
			st.ResultRunID = *resultRun
		}
		if stepErr != nil {
			st.Error = *stepErr
		}
		st.CreatedAt = mustTime(created)
		st.ExecutedAt = optTime(executedAt)
		p.Steps = append(p.Steps, st)
	}
	return rows.Err()
}

// Update 迁移 plan 状态（乐观锁 expectedVersion，成功后 DB version+1）。
// superseded_by 随对象字段一并落库。
func (r *PlanRepo) Update(ctx context.Context, p *domain.Plan, expectedVersion int) error {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE plans SET status=?, superseded_by=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		p.Status, nullString(p.SupersededBy), d.TimeParam(p.UpdatedAt), p.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

// UpdateStep 写回单个 step 的执行结果（status/result_*/error/executed_at）。
// (plan_id, seq) 为主键，重入写同 seq 覆盖同一行（幂等）。
func (r *PlanRepo) UpdateStep(ctx context.Context, st *domain.PlanStep) error {
	d := r.store.dialect
	var executedAt any
	if st.ExecutedAt != nil {
		executedAt = d.NullTimeParam(st.ExecutedAt)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE plan_steps SET status=?, result_work_item_id=?, result_run_id=?, error=?, executed_at=?
		 WHERE plan_id=? AND seq=?`,
		st.Status, nullString(st.ResultWorkItemID), nullString(st.ResultRunID),
		nullString(st.Error), executedAt, st.PlanID, st.Seq)
	return r.store.mapErr(err)
}

// ActiveByWorkItem 返回该 work item 当前 active/waiting 的 plan（唯一活跃约束下至多一个；
// 无则返回 nil）。按 created_at 倒序取最新。
func (r *PlanRepo) ActiveByWorkItem(ctx context.Context, workItemID string) (*domain.Plan, error) {
	p := &domain.Plan{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+planCols+` FROM plans
		 WHERE work_item_id=? AND status IN (?, ?) ORDER BY created_at DESC LIMIT 1`,
		workItemID, domain.PlanActive, domain.PlanWaiting)
	if err := r.scan(row, p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, r.store.mapErr(err)
	}
	return p, r.loadSteps(ctx, p)
}

// LatestByWorkItem 返回该 work item 最新一份 plan（按 created_at 倒序，不限状态；
// 无则返回 nil）。供任务详情页冷启动拉取当前 plan 投影。
func (r *PlanRepo) LatestByWorkItem(ctx context.Context, workItemID string) (*domain.Plan, error) {
	p := &domain.Plan{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+planCols+` FROM plans
		 WHERE work_item_id=? ORDER BY created_at DESC, id DESC LIMIT 1`, workItemID)
	if err := r.scan(row, p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, r.store.mapErr(err)
	}
	return p, r.loadSteps(ctx, p)
}

var _ application.PlanRepo = (*PlanRepo)(nil)
