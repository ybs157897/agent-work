package sqlstore

import (
	"context"
	"database/sql"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type RunRepo struct{ store *Store }

const runCols = `id, workspace_id, work_item_id, agent_profile_id, status, runtime_label,
	adapter_id, provider, capability_snapshot_id, session_ref, session_before, session_after,
	usage_in, usage_out, usage_cached, usage_basis, error_family, progress, retry_of,
	failure_code, failure_message, failure_retryable, input, version, created_at, updated_at, finished_at`

func (r *RunRepo) scan(row interface{ Scan(...any) error }, run *domain.ExecutionRun) error {
	var agentID, runtimeLabel, adapterID, provider, capsID, sessionRef, retryOf, fCode, fMsg *string
	var sessionBefore, sessionAfter, usageBasis, errorFamily *string
	var usageIn, usageOut, usageCached sql.NullInt64
	var fRetry *bool
	var input string
	var created, updated, finished scanTime
	if err := row.Scan(&run.ID, &run.WorkspaceID, &run.WorkItemID, &agentID, &run.Status,
		&runtimeLabel, &adapterID, &provider, &capsID, &sessionRef,
		&sessionBefore, &sessionAfter,
		&usageIn, &usageOut, &usageCached, &usageBasis, &errorFamily,
		&run.Progress, &retryOf, &fCode, &fMsg, &fRetry, &input,
		&run.Version, &created, &updated, &finished); err != nil {
		return err
	}
	setStr := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setStr(&run.AgentProfileID, agentID)
	setStr(&run.RuntimeLabel, runtimeLabel)
	setStr(&run.AdapterID, adapterID)
	setStr(&run.Provider, provider)
	setStr(&run.CapabilitySnapshotID, capsID)
	setStr(&run.SessionRef, sessionRef)
	setStr(&run.SessionBefore, sessionBefore)
	setStr(&run.SessionAfter, sessionAfter)
	setStr(&run.UsageBasis, usageBasis)
	setStr(&run.ErrorFamily, errorFamily)
	run.UsageIn, run.UsageOut, run.UsageCached = usageIn.Int64, usageOut.Int64, usageCached.Int64
	if retryOf != nil {
		run.RetryOf = *retryOf
	}
	if fCode != nil {
		f := &domain.RunFailure{Code: *fCode}
		if fMsg != nil {
			f.Message = *fMsg
		}
		if fRetry != nil {
			f.Retryable = *fRetry
		}
		run.Failure = f
	}
	_ = jsonInto(input, &run.Input)
	run.CreatedAt, run.UpdatedAt = mustTime(created), mustTime(updated)
	run.FinishedAt = optTime(finished)
	return nil
}

func (r *RunRepo) Create(ctx context.Context, run *domain.ExecutionRun) error {
	d := r.store.dialect
	var failureCode, failureMsg *string
	var failureRetry *bool
	if run.Failure != nil {
		failureCode = &run.Failure.Code
		failureMsg = &run.Failure.Message
		failureRetry = &run.Failure.Retryable
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO execution_runs(`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.WorkspaceID, run.WorkItemID, nullString(run.AgentProfileID), run.Status,
		nullString(run.RuntimeLabel), nullString(run.AdapterID), nullString(run.Provider),
		nullString(run.CapabilitySnapshotID), nullString(run.SessionRef),
		nullString(run.SessionBefore), nullString(run.SessionAfter),
		run.UsageIn, run.UsageOut, run.UsageCached, nullString(run.UsageBasis), nullString(run.ErrorFamily),
		run.Progress, nullString(run.RetryOf),
		failureCode, failureMsg, failureRetry, jsonText(run.Input), run.Version,
		d.TimeParam(run.CreatedAt), d.TimeParam(run.UpdatedAt), d.NullTimeParam(run.FinishedAt))
	return r.store.mapErr(err)
}

func (r *RunRepo) Get(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	run := &domain.ExecutionRun{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+runCols+` FROM execution_runs WHERE id=?`, id)
	if err := r.scan(row, run); err != nil {
		return nil, r.store.mapErr(err)
	}
	return run, nil
}

// Update 乐观锁：终态 Run 不允许被覆盖（状态机在领域层先拦截）。
func (r *RunRepo) Update(ctx context.Context, run *domain.ExecutionRun, expectedVersion int) error {
	d := r.store.dialect
	var failureCode, failureMsg *string
	var failureRetry *bool
	if run.Failure != nil {
		failureCode = &run.Failure.Code
		failureMsg = &run.Failure.Message
		failureRetry = &run.Failure.Retryable
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE execution_runs SET status=?, progress=?, session_ref=?,
			session_before=?, session_after=?,
			usage_in=?, usage_out=?, usage_cached=?, usage_basis=?, error_family=?,
			failure_code=?, failure_message=?, failure_retryable=?,
			version=version+1, updated_at=?, finished_at=?
		 WHERE id=? AND version=?`,
		run.Status, run.Progress, nullString(run.SessionRef),
		nullString(run.SessionBefore), nullString(run.SessionAfter),
		run.UsageIn, run.UsageOut, run.UsageCached, nullString(run.UsageBasis), nullString(run.ErrorFamily),
		failureCode, failureMsg, failureRetry,
		d.TimeParam(timeNow()), d.NullTimeParam(run.FinishedAt), run.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *RunRepo) list(ctx context.Context, where string, args ...any) ([]*domain.ExecutionRun, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+runCols+` FROM execution_runs WHERE `+where+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.ExecutionRun
	for rows.Next() {
		run := &domain.ExecutionRun{}
		if err := r.scan(rows, run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (r *RunRepo) ListByWorkItem(ctx context.Context, workItemID string) ([]*domain.ExecutionRun, error) {
	return r.list(ctx, "work_item_id=?", workItemID)
}

// ActiveByAgent 返回未终态 Run（disable 策略处置活动 Run 时使用）。
func (r *RunRepo) ActiveByAgent(ctx context.Context, agentProfileID string) ([]*domain.ExecutionRun, error) {
	return r.list(ctx,
		`agent_profile_id=? AND status NOT IN ('succeeded','interrupted','cancelled','lost','failed')`,
		agentProfileID)
}

// LeaselessActive 返回「无任何 run_leases 行且非终态」的 run——进程内模块执行的
// 孤儿（control-plane 崩溃/重启后遗留）。启动对账（ReconcileOrphanRuns）据此判定；
// runner 路径的 run 必有 lease，由 runnergateway sweeper 负责，不会被此查询命中。
func (r *RunRepo) LeaselessActive(ctx context.Context) ([]*domain.ExecutionRun, error) {
	return r.list(ctx,
		`status NOT IN ('succeeded','interrupted','cancelled','lost','failed')
		 AND NOT EXISTS(SELECT 1 FROM run_leases l WHERE l.run_id=execution_runs.id)`)
}

func (r *RunRepo) ActiveCount(ctx context.Context, workspaceID string) (int, error) {
	var n int
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT count(*) FROM execution_runs WHERE workspace_id=?
		 AND status NOT IN ('succeeded','interrupted','cancelled','lost','failed')`, workspaceID).Scan(&n)
	return n, r.store.mapErr(err)
}

func (r *RunRepo) CreateApproval(ctx context.Context, a *domain.ApprovalRequest) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO approvals(id, run_id, work_item_id, kind, risk, status, summary,
			requested_by, sensitive_input_ref, policy_snapshot_id, expires_at, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.RunID, a.WorkItemID, a.Kind, a.Risk, a.Status, a.Summary,
		jsonText(a.RequestedBy), nullString(a.SensitiveInputRef), nullString(a.PolicySnapshotID),
		d.NullTimeParam(a.ExpiresAt), d.TimeParam(a.CreatedAt))
	return r.store.mapErr(err)
}

const approvalCols = `id, run_id, work_item_id, kind, risk, status, summary, requested_by,
	sensitive_input_ref, policy_snapshot_id, expires_at, resolved_at, resolved_by, resolve_reason, created_at`

func (r *RunRepo) scanApproval(row interface{ Scan(...any) error }, a *domain.ApprovalRequest) error {
	var requestedBy string
	var sensitiveRef, policyID, resolvedBy, resolveReason *string
	var expires, resolved, created scanTime
	if err := row.Scan(&a.ID, &a.RunID, &a.WorkItemID, &a.Kind, &a.Risk, &a.Status, &a.Summary,
		&requestedBy, &sensitiveRef, &policyID, &expires,
		&resolved, &resolvedBy, &resolveReason, &created); err != nil {
		return err
	}
	_ = jsonInto(requestedBy, &a.RequestedBy)
	if sensitiveRef != nil {
		a.SensitiveInputRef = *sensitiveRef
	}
	if policyID != nil {
		a.PolicySnapshotID = *policyID
	}
	if resolvedBy != nil {
		a.ResolvedBy = *resolvedBy
	}
	if resolveReason != nil {
		a.ResolveReason = *resolveReason
	}
	a.ExpiresAt, a.ResolvedAt, a.CreatedAt = optTime(expires), optTime(resolved), mustTime(created)
	return nil
}

func (r *RunRepo) GetApproval(ctx context.Context, id string) (*domain.ApprovalRequest, error) {
	a := &domain.ApprovalRequest{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+approvalCols+` FROM approvals WHERE id=?`, id)
	if err := r.scanApproval(row, a); err != nil {
		return nil, r.store.mapErr(err)
	}
	return a, nil
}

func (r *RunRepo) ListApprovals(ctx context.Context, runID string) ([]*domain.ApprovalRequest, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+approvalCols+` FROM approvals WHERE run_id=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.ApprovalRequest
	for rows.Next() {
		a := &domain.ApprovalRequest{}
		if err := r.scanApproval(rows, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *RunRepo) UpdateApproval(ctx context.Context, a *domain.ApprovalRequest) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE approvals SET status=?, resolved_at=?, resolved_by=?, resolve_reason=? WHERE id=?`,
		a.Status, d.NullTimeParam(a.ResolvedAt), nullString(a.ResolvedBy), a.ResolveReason, a.ID)
	return r.store.mapErr(err)
}

func (r *RunRepo) CreateArtifact(ctx context.Context, art *domain.Artifact) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO artifacts(id, run_id, logical_path, mime, size, sha256, classification, status, storage_ref, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		art.ID, art.RunID, art.LogicalPath, art.Mime, art.Size, art.Sha256,
		art.Classification, art.Status, nullString(art.StorageRef), d.TimeParam(art.CreatedAt))
	return r.store.mapErr(err)
}

func (r *RunRepo) ListArtifacts(ctx context.Context, runID string) ([]*domain.Artifact, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id, run_id, logical_path, mime, size, sha256, classification, status, storage_ref, created_at
		 FROM artifacts WHERE run_id=? ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.Artifact
	for rows.Next() {
		a := &domain.Artifact{}
		var storageRef *string
		var created scanTime
		if err := rows.Scan(&a.ID, &a.RunID, &a.LogicalPath, &a.Mime, &a.Size, &a.Sha256,
			&a.Classification, &a.Status, &storageRef, &created); err != nil {
			return nil, err
		}
		if storageRef != nil {
			a.StorageRef = *storageRef
		}
		a.CreatedAt = mustTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}
