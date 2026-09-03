package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type RunRepo struct{ store *Store }

const runCols = `id, workspace_id, work_item_id, agent_profile_id, status, runtime_label,
	adapter_id, provider, capability_snapshot_id, session_ref, session_before, session_after,
	usage_in, usage_out, usage_cached, usage_basis, error_family, client_key, progress, retry_of,
	failure_code, failure_message, failure_retryable, input, version, created_at, updated_at, finished_at,
	dispatch_id, context_snapshot_id, canonical_usage, canonical_usage_digest,
	provider_usage_report, provider_usage_report_digest, provider_usage_report_seq`

func (r *RunRepo) scan(row interface{ Scan(...any) error }, run *domain.ExecutionRun) error {
	var agentID, runtimeLabel, adapterID, provider, capsID, sessionRef, retryOf, fCode, fMsg *string
	var sessionBefore, sessionAfter, usageBasis, errorFamily, clientKey *string
	var dispatchID, ctxSnapID *string
	var canonicalUsageJSON, canonicalUsageDigest *string
	var providerUsageJSON, providerUsageDigest *string
	var providerUsageSeq sql.NullInt64
	var usageIn, usageOut, usageCached sql.NullInt64
	var fRetry *bool
	var input string
	var created, updated, finished scanTime
	if err := row.Scan(&run.ID, &run.WorkspaceID, &run.WorkItemID, &agentID, &run.Status,
		&runtimeLabel, &adapterID, &provider, &capsID, &sessionRef,
		&sessionBefore, &sessionAfter,
		&usageIn, &usageOut, &usageCached, &usageBasis, &errorFamily, &clientKey,
		&run.Progress, &retryOf, &fCode, &fMsg, &fRetry, &input,
		&run.Version, &created, &updated, &finished, &dispatchID, &ctxSnapID,
		&canonicalUsageJSON, &canonicalUsageDigest, &providerUsageJSON, &providerUsageDigest,
		&providerUsageSeq); err != nil {
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
	setStr(&run.ClientKey, clientKey)
	setStr(&run.DispatchID, dispatchID)
	setStr(&run.ContextSnapshotID, ctxSnapID)
	setStr(&run.CanonicalUsageDigest, canonicalUsageDigest)
	setStr(&run.ProviderUsageReportDigest, providerUsageDigest)
	run.ProviderUsageReportSeq = providerUsageSeq.Int64
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
	if canonicalUsageJSON != nil {
		if err := jsonInto(*canonicalUsageJSON, &run.CanonicalUsage); err != nil {
			return err
		}
	}
	if providerUsageJSON != nil {
		if err := jsonInto(*providerUsageJSON, &run.ProviderUsageReport); err != nil {
			return err
		}
	}
	run.CreatedAt, run.UpdatedAt = mustTime(created), mustTime(updated)
	run.FinishedAt = optTime(finished)
	if err := validateRunUsageSnapshots(run); err != nil {
		return err
	}
	return nil
}

func validateRunUsageSnapshots(run *domain.ExecutionRun) error {
	if run == nil {
		return fmt.Errorf("%w: run required", domain.ErrValidation)
	}
	if run.ProviderUsageReport == nil {
		if run.ProviderUsageReportDigest != "" || run.ProviderUsageReportSeq != 0 {
			return fmt.Errorf("%w: provider usage report requires paired digest and sequence", domain.ErrValidation)
		}
	} else {
		if run.ProviderUsageReport.RunID != run.ID {
			return fmt.Errorf("%w: provider usage report run_id differs from Run", domain.ErrValidation)
		}
		if run.ProviderUsageReportSeq < 1 || run.ProviderUsageReportDigest == "" {
			return fmt.Errorf("%w: provider usage report requires digest and positive sequence", domain.ErrValidation)
		}
		if run.ProviderUsageReportDigest != run.ProviderUsageReport.Digest {
			return fmt.Errorf("%w: provider usage report digest mismatch", domain.ErrValidation)
		}
		if err := run.ProviderUsageReport.VerifyDigest(); err != nil {
			return err
		}
	}
	if run.CanonicalUsage == nil {
		if run.CanonicalUsageDigest != "" {
			return fmt.Errorf("%w: canonical usage requires paired digest", domain.ErrValidation)
		}
	} else {
		if run.CanonicalUsage.RunID != run.ID {
			return fmt.Errorf("%w: canonical usage run_id differs from Run", domain.ErrValidation)
		}
		if run.CanonicalUsageDigest == "" || run.CanonicalUsageDigest != run.CanonicalUsage.Digest {
			return fmt.Errorf("%w: canonical usage digest mismatch", domain.ErrValidation)
		}
		if err := run.CanonicalUsage.VerifyDigest(); err != nil {
			return err
		}
	}
	return nil
}

func (r *RunRepo) Create(ctx context.Context, run *domain.ExecutionRun) error {
	if err := validateRunUsageSnapshots(run); err != nil {
		return err
	}
	var failureCode, failureMsg *string
	var failureRetry *bool
	if run.Failure != nil {
		failureCode = &run.Failure.Code
		failureMsg = &run.Failure.Message
		failureRetry = &run.Failure.Retryable
	}
	var canonicalUsageJSON, canonicalUsageDigest, providerUsageJSON, providerUsageDigest any
	if run.CanonicalUsage != nil {
		canonicalUsageJSON = jsonText(run.CanonicalUsage)
		canonicalUsageDigest = run.CanonicalUsageDigest
	}
	if run.ProviderUsageReport != nil {
		providerUsageJSON = jsonText(run.ProviderUsageReport)
		providerUsageDigest = run.ProviderUsageReportDigest
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO execution_runs(`+runCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.WorkspaceID, run.WorkItemID, nullString(run.AgentProfileID), run.Status,
		nullString(run.RuntimeLabel), nullString(run.AdapterID), nullString(run.Provider),
		nullString(run.CapabilitySnapshotID), nullString(run.SessionRef),
		nullString(run.SessionBefore), nullString(run.SessionAfter),
		run.UsageIn, run.UsageOut, run.UsageCached, nullString(run.UsageBasis), nullString(run.ErrorFamily),
		nullString(run.ClientKey), run.Progress, nullString(run.RetryOf),
		failureCode, failureMsg, failureRetry, jsonText(run.Input), run.Version,
		timeParam(run.CreatedAt), timeParam(run.UpdatedAt), nullTimeParam(run.FinishedAt),
		nullString(run.DispatchID), nullString(run.ContextSnapshotID), canonicalUsageJSON,
		canonicalUsageDigest, providerUsageJSON, providerUsageDigest, run.ProviderUsageReportSeq)
	return r.store.mapErr(err)
}

// GetByClientKey 按 (workspace, client_key) 定位既有 run（幂等重放的查回路径）。
func (r *RunRepo) GetByClientKey(ctx context.Context, workspaceID, clientKey string) (*domain.ExecutionRun, error) {
	run := &domain.ExecutionRun{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+runCols+` FROM execution_runs WHERE workspace_id=? AND client_key=?`, workspaceID, clientKey)
	if err := r.scan(row, run); err != nil {
		return nil, r.store.mapErr(err)
	}
	return run, nil
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

// SetContextSnapshot 在 Run 创建事务内回填 context_snapshot_id（写序：
// run.Create(snapshot 空) → snapshot.Create → SetContextSnapshot）。
// 只允许回填一次：已置快照的 Run 再写返回 ErrStateConflict（一对一不可换绑）。
func (r *RunRepo) SetContextSnapshot(ctx context.Context, runID, snapshotID string) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE execution_runs SET context_snapshot_id=? WHERE id=? AND context_snapshot_id IS NULL`,
		snapshotID, runID)
	if err != nil {
		return r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return domain.ErrStateConflict
	}
	return nil
}

// Update 乐观锁：终态 Run 不允许被覆盖（状态机在领域层先拦截）。
func (r *RunRepo) Update(ctx context.Context, run *domain.ExecutionRun, expectedVersion int) error {
	if err := validateRunUsageSnapshots(run); err != nil {
		return err
	}
	var failureCode, failureMsg *string
	var failureRetry *bool
	if run.Failure != nil {
		failureCode = &run.Failure.Code
		failureMsg = &run.Failure.Message
		failureRetry = &run.Failure.Retryable
	}
	var canonicalUsageJSON, canonicalUsageDigest, providerUsageJSON, providerUsageDigest any
	if run.CanonicalUsage != nil {
		canonicalUsageJSON = jsonText(run.CanonicalUsage)
		canonicalUsageDigest = run.CanonicalUsageDigest
	}
	if run.ProviderUsageReport != nil {
		providerUsageJSON = jsonText(run.ProviderUsageReport)
		providerUsageDigest = run.ProviderUsageReportDigest
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE execution_runs SET status=?, progress=?, session_ref=?,
			session_before=?, session_after=?,
			usage_in=?, usage_out=?, usage_cached=?, usage_basis=?, error_family=?,
			failure_code=?, failure_message=?, failure_retryable=?,
			canonical_usage=?, canonical_usage_digest=?, provider_usage_report=?,
			provider_usage_report_digest=?, provider_usage_report_seq=?,
			version=version+1, updated_at=?, finished_at=?
		 WHERE id=? AND version=?`,
		run.Status, run.Progress, nullString(run.SessionRef),
		nullString(run.SessionBefore), nullString(run.SessionAfter),
		run.UsageIn, run.UsageOut, run.UsageCached, nullString(run.UsageBasis), nullString(run.ErrorFamily),
		failureCode, failureMsg, failureRetry,
		canonicalUsageJSON, canonicalUsageDigest, providerUsageJSON, providerUsageDigest,
		run.ProviderUsageReportSeq,
		timeParam(timeNow()), nullTimeParam(run.FinishedAt), run.ID, expectedVersion)
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

// ListByDispatch 按创建时间升序返回派发批次的成员 run（会话组 = WHERE dispatch_id）。
func (r *RunRepo) ListByDispatch(ctx context.Context, dispatchID string) ([]*domain.ExecutionRun, error) {
	return r.list(ctx, "dispatch_id=?", dispatchID)
}

// ListByGovernanceTurn 按 input.governance 的 (goal_id, todo_id, turn_seq) 三元组
// 返回该治理 Turn 的受管 Run（plan 派发、evaluation、retry/heal 克隆），按
// created_at 升序；workspaceID 参与过滤以防跨工作区串账。Coordinator source
// Run 不携带 governance 身份（json_extract 为 SQL NULL），不在结果内。
func (r *RunRepo) ListByGovernanceTurn(ctx context.Context, workspaceID, goalID, todoID string, turnSeq int64) ([]*domain.ExecutionRun, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(goalID) == "" ||
		strings.TrimSpace(todoID) == "" || turnSeq < 1 {
		return nil, fmt.Errorf("%w: governance turn query requires workspace/goal/todo and turn_seq >= 1", domain.ErrValidation)
	}
	return r.list(ctx,
		`workspace_id=? AND json_extract(input,'$.governance.goal_id')=?
		 AND json_extract(input,'$.governance.todo_id')=? AND json_extract(input,'$.governance.turn_seq')=?`,
		workspaceID, goalID, todoID, turnSeq)
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
		`SELECT count(*)
		 FROM execution_runs r
		 JOIN work_items wi ON wi.id=r.work_item_id
		 WHERE r.workspace_id=? AND wi.record_kind=?
			 AND r.status NOT IN ('succeeded','interrupted','cancelled','lost','failed')`,
		workspaceID, domain.RecordKindTask).Scan(&n)
	return n, r.store.mapErr(err)
}

// CreateApproval 落库审批。plan_dispatch 闸门审批无关联 run（RunID 空串存 NULL，
// 0010 起 run_id 可空）；requested_by 记录请求方（runtime 或 plan 执行器）。
func (r *RunRepo) CreateApproval(ctx context.Context, a *domain.ApprovalRequest) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO approvals(id, run_id, work_item_id, kind, risk, status, summary,
			requested_by, sensitive_input_ref, policy_snapshot_id, expires_at, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, nullString(a.RunID), a.WorkItemID, a.Kind, a.Risk, a.Status, a.Summary,
		jsonText(a.RequestedBy), nullString(a.SensitiveInputRef), nullString(a.PolicySnapshotID),
		nullTimeParam(a.ExpiresAt), timeParam(a.CreatedAt))
	return r.store.mapErr(err)
}

const approvalCols = `id, run_id, work_item_id, kind, risk, status, summary, requested_by,
	sensitive_input_ref, policy_snapshot_id, expires_at, resolved_at, resolved_by, resolve_reason, created_at`

func (r *RunRepo) scanApproval(row interface{ Scan(...any) error }, a *domain.ApprovalRequest) error {
	var requestedBy string
	var runID *string
	var sensitiveRef, policyID, resolvedBy, resolveReason *string
	var expires, resolved, created scanTime
	if err := row.Scan(&a.ID, &runID, &a.WorkItemID, &a.Kind, &a.Risk, &a.Status, &a.Summary,
		&requestedBy, &sensitiveRef, &policyID, &expires,
		&resolved, &resolvedBy, &resolveReason, &created); err != nil {
		return err
	}
	if runID != nil {
		a.RunID = *runID
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

// ListPendingPlanDispatchApprovals returns unbound plan dispatch gates for a
// work item. A plan_dispatch approval intentionally has a NULL run_id and is
// therefore not visible through ListApprovals.
func (r *RunRepo) ListPendingPlanDispatchApprovals(ctx context.Context, workItemID string) ([]*domain.ApprovalRequest, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+approvalCols+` FROM approvals
		 WHERE work_item_id=? AND run_id IS NULL AND kind=? AND status=?
		 ORDER BY created_at`, workItemID, domain.ApprovalKindPlanDispatch, domain.ApprovalPending)
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
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE approvals SET status=?, resolved_at=?, resolved_by=?, resolve_reason=? WHERE id=?`,
		a.Status, nullTimeParam(a.ResolvedAt), nullString(a.ResolvedBy), a.ResolveReason, a.ID)
	return r.store.mapErr(err)
}

func (r *RunRepo) CreateArtifact(ctx context.Context, art *domain.Artifact) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO artifacts(id, run_id, logical_path, mime, size, sha256, classification, status, storage_ref, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		art.ID, art.RunID, art.LogicalPath, art.Mime, art.Size, art.Sha256,
		art.Classification, art.Status, nullString(art.StorageRef), timeParam(art.CreatedAt))
	return r.store.mapErr(err)
}

func (r *RunRepo) GetArtifact(ctx context.Context, artifactID string) (*domain.Artifact, error) {
	art := &domain.Artifact{}
	var storageRef *string
	var created scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id, run_id, logical_path, mime, size, sha256, classification, status, storage_ref, created_at
		 FROM artifacts WHERE id=?`, artifactID).Scan(&art.ID, &art.RunID, &art.LogicalPath, &art.Mime, &art.Size,
		&art.Sha256, &art.Classification, &art.Status, &storageRef, &created)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if storageRef != nil {
		art.StorageRef = *storageRef
	}
	art.CreatedAt = mustTime(created)
	return art, nil
}

func (r *RunRepo) UpdateArtifactStatus(ctx context.Context, artifactID string, status domain.ArtifactStatus) error {
	if status != domain.ArtifactDraft && status != domain.ArtifactAccepted {
		return fmt.Errorf("%w: invalid artifact status %q", domain.ErrValidation, status)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE artifacts SET status=? WHERE id=?`, status, artifactID)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound
	}
	return nil
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
