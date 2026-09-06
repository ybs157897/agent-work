package sqlstore

import (
	"context"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// KnowledgeJobRepo stores the durable controller state around ordinary Chat
// Runs.  The Run and provider session remain the execution authority; this
// repository only records which bounded decision should be applied next.
type KnowledgeJobRepo struct{ store *Store }

var _ application.KnowledgeJobRepo = (*KnowledgeJobRepo)(nil)

const knowledgeJobCols = `id, workspace_id, requesting_agent_id, source_run_id, submission_id, agent_profile_id,
	work_item_id, current_run_id, mode, status, question, context, scope_json,
	budget_json, used_json, coverage_json, evidence_json, visited_versions_json,
	required_item_ids_json, required_relations_json, index_revision, snapshot_ids_json, frontier_json, observations_json, result_json, turn_seq, retry_count,
	repair_attempt, next_action_at, last_decision_digest, last_error, client_key,
	version, created_at, updated_at, finished_at`

const knowledgeJobActionCols = `id, job_id, turn_seq, run_id, action_kind, status,
	request_digest, request_json, result_json, error_message, version, created_at,
	updated_at`

func (r *KnowledgeJobRepo) scanJob(row interface{ Scan(...any) error }, job *domain.KnowledgeJob) error {
	var currentRunID *string
	var scope, budget, used, coverage, evidence, visited, requiredItems, requiredRelations, snapshots, frontier, observations, result string
	var created, updated, nextAction, finished scanTime
	var sourceRunID, submissionID *string
	if err := row.Scan(&job.ID, &job.WorkspaceID, &job.RequestingAgentID, &sourceRunID,
		&submissionID, &job.AgentProfileID, &job.WorkItemID, &currentRunID, &job.Mode, &job.Status,
		&job.Question, &job.Context, &scope, &budget, &used, &coverage, &evidence,
		&visited, &requiredItems, &requiredRelations, &job.IndexRevision, &snapshots, &frontier, &observations, &result, &job.TurnSeq, &job.RetryCount,
		&job.RepairAttempt, &nextAction, &job.LastDecisionDigest, &job.LastError,
		&job.ClientKey, &job.Version, &created, &updated, &finished); err != nil {
		return err
	}
	if currentRunID != nil {
		job.CurrentRunID = *currentRunID
	}
	if sourceRunID != nil {
		job.SourceRunID = *sourceRunID
	}
	if submissionID != nil {
		job.SubmissionID = *submissionID
	}
	for _, field := range []struct {
		raw string
		dst any
	}{
		{scope, &job.Scope}, {budget, &job.Budget}, {used, &job.Used},
		{coverage, &job.Coverage}, {evidence, &job.EvidenceIDs},
		{visited, &job.VisitedVersionIDs}, {requiredItems, &job.RequiredItemIDs},
		{requiredRelations, &job.RequiredRelations}, {snapshots, &job.SnapshotIDs}, {frontier, &job.Frontier},
		{observations, &job.Observations}, {result, &job.Result},
	} {
		if err := jsonInto(field.raw, field.dst); err != nil {
			return fmt.Errorf("decode knowledge job %s JSON: %w", job.ID, err)
		}
	}
	job.Budget = job.Budget.Normalize()
	job.CreatedAt, job.UpdatedAt = mustTime(created), mustTime(updated)
	job.NextActionAt, job.FinishedAt = optTime(nextAction), optTime(finished)
	return job.Validate()
}

func (r *KnowledgeJobRepo) GetConfig(ctx context.Context, workspaceID string) (*domain.KnowledgeLibrarianConfig, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace_id required", domain.ErrValidation)
	}
	c := &domain.KnowledgeLibrarianConfig{WorkspaceID: workspaceID}
	var agentID *string
	var created, updated scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT librarian_agent_id, enabled, auto_collect, version, created_at, updated_at
		 FROM knowledge_librarian_configs WHERE workspace_id=?`, workspaceID).
		Scan(&agentID, &c.Enabled, &c.AutoCollect, &c.Version, &created, &updated)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if agentID != nil {
		c.LibrarianAgentID = *agentID
	}
	c.CreatedAt, c.UpdatedAt = mustTime(created), mustTime(updated)
	return c, c.Validate()
}

func (r *KnowledgeJobRepo) CreateConfig(ctx context.Context, config *domain.KnowledgeLibrarianConfig) error {
	if config == nil {
		return fmt.Errorf("%w: knowledge librarian config required", domain.ErrValidation)
	}
	if config.Version == 0 {
		config.Version = 1
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if config.CreatedAt.IsZero() {
		config.CreatedAt = timeNow()
	}
	if config.UpdatedAt.IsZero() {
		config.UpdatedAt = config.CreatedAt
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_librarian_configs
		 (workspace_id, librarian_agent_id, enabled, auto_collect, version, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?)`, config.WorkspaceID, nullString(config.LibrarianAgentID),
		config.Enabled, config.AutoCollect, config.Version,
		timeParam(config.CreatedAt), timeParam(config.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *KnowledgeJobRepo) UpdateConfig(ctx context.Context, config *domain.KnowledgeLibrarianConfig, expectedVersion int) error {
	if config == nil {
		return fmt.Errorf("%w: knowledge librarian config required", domain.ErrValidation)
	}
	if err := config.Validate(); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE knowledge_librarian_configs
		 SET librarian_agent_id=?, enabled=?, auto_collect=?, version=version+1, updated_at=?
		 WHERE workspace_id=? AND version=?`, nullString(config.LibrarianAgentID), config.Enabled,
		config.AutoCollect, timeParam(config.UpdatedAt), config.WorkspaceID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	config.Version = expectedVersion + 1
	return nil
}

func (r *KnowledgeJobRepo) Create(ctx context.Context, job *domain.KnowledgeJob) error {
	if job == nil {
		return fmt.Errorf("%w: knowledge job required", domain.ErrValidation)
	}
	if job.ID == "" {
		job.ID = domain.NewID(domain.PrefixKnowledgeJob)
	}
	if job.Status == "" {
		job.Status = domain.KnowledgeJobQueued
	}
	if job.Version == 0 {
		job.Version = 1
	}
	job.Budget = job.Budget.Normalize()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = timeNow()
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	}
	if err := job.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_jobs(`+knowledgeJobCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		job.ID, job.WorkspaceID, job.RequestingAgentID, nullString(job.SourceRunID), nullString(job.SubmissionID), job.AgentProfileID, job.WorkItemID,
		nullString(job.CurrentRunID), job.Mode, job.Status, job.Question, job.Context,
		jsonTextOrEmptyObject(job.Scope), jsonTextOrEmptyObject(job.Budget), jsonTextOrEmptyObject(job.Used),
		jsonTextOrEmptyObject(job.Coverage), jsonTextOrEmptyArray(job.EvidenceIDs), jsonTextOrEmptyArray(job.VisitedVersionIDs),
		jsonTextOrEmptyArray(job.RequiredItemIDs), jsonTextOrEmptyArray(job.RequiredRelations),
		job.IndexRevision, jsonTextOrEmptyArray(job.SnapshotIDs),
		jsonTextOrEmptyArray(job.Frontier), jsonTextOrEmptyArray(job.Observations), jsonTextOrEmptyObject(job.Result),
		job.TurnSeq, job.RetryCount, job.RepairAttempt, nullTimeParam(job.NextActionAt), job.LastDecisionDigest,
		job.LastError, job.ClientKey, job.Version, timeParam(job.CreatedAt), timeParam(job.UpdatedAt),
		nullTimeParam(job.FinishedAt))
	return r.store.mapErr(err)
}

func jsonTextOrEmptyObject(v any) string {
	if v == nil {
		return "{}"
	}
	return jsonText(v)
}

func jsonTextOrEmptyArray(v any) string {
	if v == nil {
		return "[]"
	}
	return jsonText(v)
}

func (r *KnowledgeJobRepo) Get(ctx context.Context, id string) (*domain.KnowledgeJob, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: knowledge job id required", domain.ErrValidation)
	}
	job := &domain.KnowledgeJob{}
	if err := r.scanJob(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeJobCols+` FROM knowledge_jobs WHERE id=?`, id), job); err != nil {
		return nil, r.store.mapErr(err)
	}
	return job, nil
}

func (r *KnowledgeJobRepo) GetByClientKey(ctx context.Context, workspaceID, requestingAgentID, clientKey string) (*domain.KnowledgeJob, error) {
	if workspaceID == "" || requestingAgentID == "" || clientKey == "" {
		return nil, fmt.Errorf("%w: workspace_id, requesting_agent_id, and client_key required", domain.ErrValidation)
	}
	job := &domain.KnowledgeJob{}
	if err := r.scanJob(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeJobCols+` FROM knowledge_jobs WHERE workspace_id=? AND requesting_agent_id=? AND client_key=? ORDER BY created_at DESC LIMIT 1`, workspaceID, requestingAgentID, clientKey), job); err != nil {
		return nil, r.store.mapErr(err)
	}
	return job, nil
}

func (r *KnowledgeJobRepo) GetByWorkItem(ctx context.Context, workItemID string) (*domain.KnowledgeJob, error) {
	if workItemID == "" {
		return nil, fmt.Errorf("%w: work_item_id required", domain.ErrValidation)
	}
	job := &domain.KnowledgeJob{}
	if err := r.scanJob(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeJobCols+` FROM knowledge_jobs WHERE work_item_id=?`, workItemID), job); err != nil {
		return nil, r.store.mapErr(err)
	}
	return job, nil
}

func (r *KnowledgeJobRepo) GetByCurrentRun(ctx context.Context, runID string) (*domain.KnowledgeJob, error) {
	if runID == "" {
		return nil, fmt.Errorf("%w: run_id required", domain.ErrValidation)
	}
	job := &domain.KnowledgeJob{}
	if err := r.scanJob(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeJobCols+` FROM knowledge_jobs WHERE current_run_id=?`, runID), job); err != nil {
		return nil, r.store.mapErr(err)
	}
	return job, nil
}

func (r *KnowledgeJobRepo) List(ctx context.Context, workspaceID, agentProfileID string, status domain.KnowledgeJobStatus, limit int) ([]*domain.KnowledgeJob, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace_id required", domain.ErrValidation)
	}
	if status != "" && !status.Valid() {
		return nil, fmt.Errorf("%w: unknown knowledge job status %q", domain.ErrValidation, status)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + knowledgeJobCols + ` FROM knowledge_jobs WHERE workspace_id=?`
	args := []any{workspaceID}
	if agentProfileID != "" {
		query += ` AND agent_profile_id=?`
		args = append(args, agentProfileID)
	}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), query, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeJob
	for rows.Next() {
		job := &domain.KnowledgeJob{}
		if err := r.scanJob(rows, job); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// ListForRequester is deliberately separate from List: agentProfileID in
// List identifies the ordinary Agent executing the librarian Run, while the
// caller-facing view must filter by requesting_agent_id before applying the
// page limit. Filtering after a broad librarian list can hide a caller's own
// older jobs behind unrelated requests.
func (r *KnowledgeJobRepo) ListForRequester(ctx context.Context, workspaceID, requestingAgentID string,
	status domain.KnowledgeJobStatus, limit int) ([]*domain.KnowledgeJob, error) {
	if workspaceID == "" || requestingAgentID == "" {
		return nil, fmt.Errorf("%w: workspace_id and requesting_agent_id required", domain.ErrValidation)
	}
	if status != "" && !status.Valid() {
		return nil, fmt.Errorf("%w: unknown knowledge job status %q", domain.ErrValidation, status)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + knowledgeJobCols + ` FROM knowledge_jobs WHERE workspace_id=? AND requesting_agent_id=?`
	args := []any{workspaceID, requestingAgentID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at ASC, id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), query, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeJob
	for rows.Next() {
		job := &domain.KnowledgeJob{}
		if err := r.scanJob(rows, job); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (r *KnowledgeJobRepo) ListRecoverable(ctx context.Context, workspaceID, afterID string, limit int) ([]*domain.KnowledgeJob, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace_id required", domain.ErrValidation)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT ` + knowledgeJobCols + ` FROM knowledge_jobs
		WHERE workspace_id=? AND status IN (?,?,?)`
	args := []any{workspaceID, domain.KnowledgeJobQueued, domain.KnowledgeJobRunning, domain.KnowledgeJobWaitingRetry}
	if afterID != "" {
		query += ` AND id>?`
		args = append(args, afterID)
	}
	query += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := r.store.query(ctx, r.store.exec(ctx), query, args...)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeJob
	for rows.Next() {
		job := &domain.KnowledgeJob{}
		if err := r.scanJob(rows, job); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (r *KnowledgeJobRepo) ListCancellationPending(ctx context.Context, workspaceID string, limit int) ([]*domain.KnowledgeJob, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("%w: workspace_id required", domain.ErrValidation)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeJobCols+` FROM knowledge_jobs
		 WHERE workspace_id=? AND status IN (?,?,?,?,?) AND last_error LIKE 'cancel_pending:%'
		 ORDER BY updated_at ASC, id ASC LIMIT ?`, workspaceID,
		domain.KnowledgeJobCompleted, domain.KnowledgeJobIncomplete, domain.KnowledgeJobConflict,
		domain.KnowledgeJobCancelled, domain.KnowledgeJobFailed, limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.KnowledgeJob
	for rows.Next() {
		job := &domain.KnowledgeJob{}
		if err := r.scanJob(rows, job); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (r *KnowledgeJobRepo) Update(ctx context.Context, job *domain.KnowledgeJob, expectedVersion int) error {
	if job == nil {
		return fmt.Errorf("%w: knowledge job required", domain.ErrValidation)
	}
	job.Budget = job.Budget.Normalize()
	if err := job.Validate(); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE knowledge_jobs SET current_run_id=?, status=?, question=?, context=?, scope_json=?,
		 budget_json=?, used_json=?, coverage_json=?, evidence_json=?, visited_versions_json=?,
		 required_item_ids_json=?, required_relations_json=?, index_revision=?, snapshot_ids_json=?, frontier_json=?, observations_json=?, result_json=?, turn_seq=?, retry_count=?,
		 repair_attempt=?, next_action_at=?, last_decision_digest=?, last_error=?, version=version+1,
		 updated_at=?, finished_at=? WHERE id=? AND version=?`,
		nullString(job.CurrentRunID), job.Status, job.Question, job.Context,
		jsonTextOrEmptyObject(job.Scope), jsonTextOrEmptyObject(job.Budget), jsonTextOrEmptyObject(job.Used),
		jsonTextOrEmptyObject(job.Coverage), jsonTextOrEmptyArray(job.EvidenceIDs), jsonTextOrEmptyArray(job.VisitedVersionIDs),
		jsonTextOrEmptyArray(job.RequiredItemIDs), jsonTextOrEmptyArray(job.RequiredRelations),
		job.IndexRevision, jsonTextOrEmptyArray(job.SnapshotIDs),
		jsonTextOrEmptyArray(job.Frontier), jsonTextOrEmptyArray(job.Observations), jsonTextOrEmptyObject(job.Result),
		job.TurnSeq, job.RetryCount, job.RepairAttempt, nullTimeParam(job.NextActionAt), job.LastDecisionDigest,
		job.LastError, timeParam(job.UpdatedAt), nullTimeParam(job.FinishedAt), job.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	job.Version = expectedVersion + 1
	return nil
}

func (r *KnowledgeJobRepo) BindCurrentRun(ctx context.Context, jobID, runID string, turnSeq int64, expectedVersion int) error {
	if jobID == "" || runID == "" || turnSeq < 1 {
		return fmt.Errorf("%w: job_id/run_id/positive turn_seq required", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE knowledge_jobs SET current_run_id=?, status=?, turn_seq=?, version=version+1,
		 updated_at=? WHERE id=? AND version=?`, runID, domain.KnowledgeJobRunning, turnSeq,
		timeParam(timeNow()), jobID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *KnowledgeJobRepo) ClaimAction(ctx context.Context, action *domain.KnowledgeJobAction) (bool, *domain.KnowledgeJobAction, error) {
	if action == nil {
		return false, nil, fmt.Errorf("%w: knowledge job action required", domain.ErrValidation)
	}
	if action.ID == "" {
		action.ID = domain.NewID("kbja_")
	}
	if action.Status == "" {
		action.Status = domain.KnowledgeJobActionPending
	}
	if action.Version == 0 {
		action.Version = 1
	}
	if action.CreatedAt.IsZero() {
		action.CreatedAt = timeNow()
	}
	if action.UpdatedAt.IsZero() {
		action.UpdatedAt = action.CreatedAt
	}
	if err := action.Validate(); err != nil {
		return false, nil, err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO knowledge_job_actions(`+knowledgeJobActionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		action.ID, action.JobID, action.TurnSeq, action.RunID, action.Kind, action.Status,
		action.RequestDigest, jsonTextOrEmptyObject(action.Request), jsonTextOrEmptyObject(action.Result),
		action.ErrorMessage, action.Version, timeParam(action.CreatedAt), timeParam(action.UpdatedAt))
	if err == nil {
		return true, action, nil
	}
	if !sqliteUniqueViolation(err) {
		return false, nil, r.store.mapErr(err)
	}
	existing, getErr := r.GetAction(ctx, action.JobID, action.TurnSeq)
	if getErr != nil {
		return false, nil, getErr
	}
	if existing.RequestDigest != action.RequestDigest {
		return false, existing, domain.ErrIdempotencyConflict
	}
	return false, existing, nil
}

func (r *KnowledgeJobRepo) GetAction(ctx context.Context, jobID string, turnSeq int64) (*domain.KnowledgeJobAction, error) {
	action := &domain.KnowledgeJobAction{}
	var request, result string
	var created, updated scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+knowledgeJobActionCols+` FROM knowledge_job_actions WHERE job_id=? AND turn_seq=?`, jobID, turnSeq).
		Scan(&action.ID, &action.JobID, &action.TurnSeq, &action.RunID, &action.Kind, &action.Status,
			&action.RequestDigest, &request, &result, &action.ErrorMessage, &action.Version, &created, &updated)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if err := jsonInto(request, &action.Request); err != nil {
		return nil, err
	}
	if err := jsonInto(result, &action.Result); err != nil {
		return nil, err
	}
	action.CreatedAt, action.UpdatedAt = mustTime(created), mustTime(updated)
	return action, action.Validate()
}

func (r *KnowledgeJobRepo) UpdateAction(ctx context.Context, action *domain.KnowledgeJobAction, expectedVersion int) error {
	if action == nil {
		return fmt.Errorf("%w: knowledge job action required", domain.ErrValidation)
	}
	if err := action.Validate(); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE knowledge_job_actions SET status=?, result_json=?, error_message=?, version=version+1,
		 updated_at=? WHERE id=? AND version=?`, action.Status, jsonTextOrEmptyObject(action.Result),
		action.ErrorMessage, timeParam(action.UpdatedAt), action.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	action.Version = expectedVersion + 1
	return nil
}
