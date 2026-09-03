package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// PlanRepo 维护 plans / plan_steps（M1 编排层）。
// Create 必须在事务内调用（plan + steps 同事务落库）；Update/UpdateStep 走乐观锁。
type PlanRepo struct{ store *Store }

const planCols = `id, workspace_id, work_item_id, agent_profile_id, source_run_id,
	context_snapshot_id, context_generation, status, superseded_by, guardrails, error,
	version, created_at, updated_at, client_key, goal_id, todo_id, turn_seq,
	decision_schema_version, decision_schema_digest, decision_digest`

const planStepCols = `plan_id, seq, verb, payload, status, result_work_item_id,
	result_run_id, error, created_at, executed_at`

func (r *PlanRepo) scan(row interface{ Scan(...any) error }, p *domain.Plan) error {
	var sourceRunID, supersededBy, planErr, ctxSnapID *string
	var clientKey, goalID, todoID, schemaVersion, schemaDigest, decisionDigest *string
	var turnSeq sql.NullInt64
	var guardrails string
	var created, updated scanTime
	if err := row.Scan(&p.ID, &p.WorkspaceID, &p.WorkItemID, &p.AgentProfileID, &sourceRunID,
		&ctxSnapID, &p.ContextGeneration, &p.Status, &supersededBy, &guardrails, &planErr,
		&p.Version, &created, &updated, &clientKey, &goalID, &todoID, &turnSeq,
		&schemaVersion, &schemaDigest, &decisionDigest); err != nil {
		return err
	}
	if sourceRunID != nil {
		p.SourceRunID = *sourceRunID
	}
	if ctxSnapID != nil {
		p.ContextSnapshotID = *ctxSnapID
	}
	if supersededBy != nil {
		p.SupersededBy = *supersededBy
	}
	if err := jsonInto(guardrails, &p.Guardrails); err != nil {
		return err
	}
	if planErr != nil {
		p.Error = *planErr
	}
	if clientKey != nil {
		p.ClientKey = *clientKey
	}
	if schemaVersion != nil {
		p.DecisionSchemaVersion = *schemaVersion
	}
	if schemaDigest != nil {
		p.DecisionSchemaDigest = *schemaDigest
	}
	if decisionDigest != nil {
		p.DecisionDigest = *decisionDigest
	}
	governanceValues := 0
	for _, present := range []bool{clientKey != nil, goalID != nil, todoID != nil, turnSeq.Valid,
		schemaVersion != nil, schemaDigest != nil, decisionDigest != nil} {
		if present {
			governanceValues++
		}
	}
	if governanceValues != 0 {
		if governanceValues != 7 {
			return fmt.Errorf("plan governance identity columns are incomplete")
		}
		p.GovernanceTurnKey = &domain.TurnKey{GoalID: *goalID, TodoID: *todoID, TurnSeq: turnSeq.Int64}
	}
	p.CreatedAt, p.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

// Create 写入 plan 及其全部 steps；步骤 seq 从 0 起连续（由应用层构造保证）。
func (r *PlanRepo) Create(ctx context.Context, p *domain.Plan) error {
	var goalID, todoID, turnSeq any
	if p.GovernanceTurnKey != nil {
		goalID = nullString(p.GovernanceTurnKey.GoalID)
		todoID = nullString(p.GovernanceTurnKey.TodoID)
		turnSeq = p.GovernanceTurnKey.TurnSeq
	}
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO plans(`+planCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.WorkspaceID, p.WorkItemID, p.AgentProfileID, nullString(p.SourceRunID),
		nullString(p.ContextSnapshotID), p.ContextGeneration,
		p.Status, nullString(p.SupersededBy), jsonText(p.Guardrails), nullString(p.Error), p.Version,
		timeParam(p.CreatedAt), timeParam(p.UpdatedAt), nullString(p.ClientKey), goalID, todoID, turnSeq,
		nullString(p.DecisionSchemaVersion), nullString(p.DecisionSchemaDigest), nullString(p.DecisionDigest)); err != nil {
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
	var executedAt any
	if st.ExecutedAt != nil {
		executedAt = nullTimeParam(st.ExecutedAt)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO plan_steps(`+planStepCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		st.PlanID, st.Seq, st.Verb, jsonText(st.Payload), st.Status,
		nullString(st.ResultWorkItemID), nullString(st.ResultRunID), nullString(st.Error),
		timeParam(st.CreatedAt), executedAt)
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

func (r *PlanRepo) GetBySourceRun(ctx context.Context, sourceRunID string) (*domain.Plan, error) {
	if sourceRunID == "" {
		return nil, nil
	}
	p := &domain.Plan{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+planCols+` FROM plans WHERE source_run_id=?`, sourceRunID)
	if err := r.scan(row, p); err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, r.store.mapErr(err)
	}
	return p, r.loadSteps(ctx, p)
}

// GetByClientKey 按 (workspace, client_key) 定位既有治理 Plan（幂等重放的查回路径）。
func (r *PlanRepo) GetByClientKey(ctx context.Context, workspaceID, clientKey string) (*domain.Plan, error) {
	if workspaceID == "" || clientKey == "" {
		return nil, nil
	}
	p := &domain.Plan{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+planCols+` FROM plans WHERE workspace_id=? AND client_key=?`, workspaceID, clientKey)
	if err := r.scan(row, p); err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
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
// superseded_by 与 error 随对象字段一并落库；guardrails 提交后不可变（Create 固化）。
func (r *PlanRepo) Update(ctx context.Context, p *domain.Plan, expectedVersion int) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE plans SET status=?, superseded_by=?, error=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		p.Status, nullString(p.SupersededBy), nullString(p.Error), timeParam(p.UpdatedAt), p.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

// UpdateStep 写回单个 step 的执行结果（status/result_*/error/executed_at/payload；
// consult_knowledge 执行后 payload 增补 results 键，其余动词 payload 原样重写）。
// (plan_id, seq) 为主键，重入写同 seq 覆盖同一行（幂等）。
func (r *PlanRepo) UpdateStep(ctx context.Context, st *domain.PlanStep) error {
	var executedAt any
	if st.ExecutedAt != nil {
		executedAt = nullTimeParam(st.ExecutedAt)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE plan_steps SET status=?, result_work_item_id=?, result_run_id=?, error=?, executed_at=?, payload=?
		 WHERE plan_id=? AND seq=?`,
		st.Status, nullString(st.ResultWorkItemID), nullString(st.ResultRunID),
		nullString(st.Error), executedAt, jsonText(st.Payload), st.PlanID, st.Seq)
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
