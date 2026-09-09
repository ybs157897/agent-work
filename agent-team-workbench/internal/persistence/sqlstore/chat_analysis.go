package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ChatAnalysisRepo is the SQLite implementation of the durable analysis
// projection. The application owns outer transaction boundaries; these
// methods deliberately use the current executor so attempts, revisions and
// answers can share one commit with a Run or HTTP command.
type ChatAnalysisRepo struct{ store *Store }

var _ application.ChatAnalysisRepo = (*ChatAnalysisRepo)(nil)

const chatAnalysisCols = `workspace_id, chat_work_item_id, agent_profile_id,
version, revision, status, current_run_id, error, document_json, document_digest,
created_at, updated_at`

func scanChatAnalysis(row interface{ Scan(...any) error }) (*domain.ChatAnalysis, error) {
	a := &domain.ChatAnalysis{}
	var currentRun *string
	var created, updated scanTime
	if err := row.Scan(&a.WorkspaceID, &a.ChatWorkItemID, &a.AgentProfileID,
		&a.Version, &a.Revision, &a.Status, &currentRun, &a.Error,
		&a.DocumentJSON, &a.DocumentDigest, &created, &updated); err != nil {
		return nil, err
	}
	if currentRun != nil {
		a.CurrentRunID = *currentRun
	}
	a.CreatedAt, a.UpdatedAt = mustTime(created), mustTime(updated)
	return a, nil
}

func scanChatAnalysisAttempt(row interface{ Scan(...any) error }) (*domain.ChatAnalysisAttempt, error) {
	a := &domain.ChatAnalysisAttempt{}
	var created, updated scanTime
	if err := row.Scan(&a.ID, &a.WorkspaceID, &a.ChatWorkItemID, &a.AgentProfileID,
		&a.RunID, &a.Version, &a.RequestDigest, &a.SourceCatalogJSON, &a.Status,
		&a.Revision, &a.Error, &created, &updated); err != nil {
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = mustTime(created), mustTime(updated)
	return a, nil
}

func scanChatAnalysisRevision(row interface{ Scan(...any) error }) (*domain.ChatAnalysisRevision, error) {
	r := &domain.ChatAnalysisRevision{}
	var created scanTime
	if err := row.Scan(&r.ID, &r.WorkspaceID, &r.ChatWorkItemID, &r.AgentProfileID,
		&r.RunID, &r.Revision, &r.DocumentJSON, &r.DocumentDigest, &created); err != nil {
		return nil, err
	}
	r.CreatedAt = mustTime(created)
	return r, nil
}

func scanChatAnalysisAnswer(row interface{ Scan(...any) error }) (*domain.ChatAnalysisAnswer, error) {
	a := &domain.ChatAnalysisAnswer{}
	var selected, deps string
	var created scanTime
	if err := row.Scan(&a.ID, &a.WorkspaceID, &a.ChatWorkItemID, &a.AgentProfileID,
		&a.Revision, &a.QuestionID, &a.QuestionFingerprint, &selected, &a.Text,
		&a.Disposition, &deps, &a.ClientKey, &a.InheritedFromAnswerID, &a.LineageReason, &created); err != nil {
		return nil, err
	}
	if err := jsonInto(selected, &a.SelectedOptionIDs); err != nil {
		return nil, err
	}
	a.SourceDependenciesJSON = deps
	a.CreatedAt = mustTime(created)
	return a, nil
}

func (r *ChatAnalysisRepo) Get(ctx context.Context, workspaceID, chatID string) (*domain.ChatAnalysis, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" {
		return nil, fmt.Errorf("%w: chat analysis scope is required", domain.ErrValidation)
	}
	a, err := scanChatAnalysis(r.store.queryRow(r.storeCtx(ctx), r.store.exec(ctx),
		`SELECT `+chatAnalysisCols+` FROM chat_analyses WHERE workspace_id=? AND chat_work_item_id=?`, workspaceID, chatID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return a, nil
}

// storeCtx exists only to keep the query call sites symmetric with other
// repositories. It preserves the transaction context untouched.
func (r *ChatAnalysisRepo) storeCtx(ctx context.Context) context.Context { return ctx }

func (r *ChatAnalysisRepo) StartAttempt(ctx context.Context, attempt *domain.ChatAnalysisAttempt) (*domain.ChatAnalysis, error) {
	if attempt == nil {
		return nil, fmt.Errorf("%w: analysis attempt is required", domain.ErrValidation)
	}
	now := attempt.CreatedAt
	if now.IsZero() {
		now = timeNow()
	}
	if attempt.UpdatedAt.IsZero() {
		attempt.UpdatedAt = now
	}
	if attempt.Status == "" {
		attempt.Status = domain.ChatAnalysisAttemptAnalyzing
	}
	if !attempt.Status.Valid() {
		return nil, fmt.Errorf("%w: invalid chat analysis attempt status", domain.ErrValidation)
	}
	if strings.TrimSpace(attempt.ID) == "" || strings.TrimSpace(attempt.WorkspaceID) == "" ||
		strings.TrimSpace(attempt.ChatWorkItemID) == "" || strings.TrimSpace(attempt.AgentProfileID) == "" ||
		strings.TrimSpace(attempt.RunID) == "" || strings.TrimSpace(attempt.RequestDigest) == "" ||
		strings.TrimSpace(attempt.SourceCatalogJSON) == "" {
		return nil, fmt.Errorf("%w: incomplete chat analysis attempt", domain.ErrValidation)
	}
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_analyses(`+chatAnalysisCols+`)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id, chat_work_item_id) DO UPDATE SET
		   agent_profile_id=excluded.agent_profile_id,
		   version=chat_analyses.version+1,
		   status=excluded.status,
		   current_run_id=excluded.current_run_id,
		   error='',
		   updated_at=excluded.updated_at`,
		attempt.WorkspaceID, attempt.ChatWorkItemID, attempt.AgentProfileID,
		1, 0, domain.ChatAnalysisAnalyzing, attempt.RunID, "", "", "", timeParam(now), timeParam(now)); err != nil {
		return nil, r.store.mapErr(err)
	}
	projection, err := r.Get(ctx, attempt.WorkspaceID, attempt.ChatWorkItemID)
	if err != nil {
		return nil, err
	}
	attempt.Version = projection.Version
	attempt.CreatedAt, attempt.UpdatedAt = now, now
	if err := attempt.Validate(); err != nil {
		return nil, err
	}
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_analysis_attempts
		 (id,workspace_id,chat_work_item_id,agent_profile_id,run_id,version,request_digest,source_catalog_json,status,revision,error,created_at,updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		attempt.ID, attempt.WorkspaceID, attempt.ChatWorkItemID, attempt.AgentProfileID,
		attempt.RunID, attempt.Version, attempt.RequestDigest, attempt.SourceCatalogJSON,
		domain.ChatAnalysisAttemptAnalyzing, 0, "", timeParam(now), timeParam(now)); err != nil {
		return nil, r.store.mapErr(err)
	}
	return projection, nil
}

func (r *ChatAnalysisRepo) GetAttemptByRun(ctx context.Context, runID string) (*domain.ChatAnalysisAttempt, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("%w: analysis run id is required", domain.ErrValidation)
	}
	a, err := scanChatAnalysisAttempt(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,run_id,version,request_digest,source_catalog_json,status,revision,error,created_at,updated_at
		 FROM chat_analysis_attempts WHERE run_id=?`, runID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return a, nil
}

func (r *ChatAnalysisRepo) GetRevision(ctx context.Context, workspaceID, chatID string, revision int64) (*domain.ChatAnalysisRevision, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" || revision < 1 {
		return nil, fmt.Errorf("%w: analysis revision scope is required", domain.ErrValidation)
	}
	v, err := scanChatAnalysisRevision(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,run_id,revision,document_json,document_digest,created_at
		 FROM chat_analysis_revisions WHERE workspace_id=? AND chat_work_item_id=? AND revision=?`, workspaceID, chatID, revision))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return v, nil
}

func (r *ChatAnalysisRepo) ListRevisions(ctx context.Context, workspaceID, chatID string, limit int) ([]*domain.ChatAnalysisRevision, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,run_id,revision,document_json,document_digest,created_at
		 FROM chat_analysis_revisions WHERE workspace_id=? AND chat_work_item_id=? ORDER BY revision DESC LIMIT ?`, workspaceID, chatID, limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ChatAnalysisRevision
	for rows.Next() {
		revision, scanErr := scanChatAnalysisRevision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, revision)
	}
	return out, rows.Err()
}

func (r *ChatAnalysisRepo) ListAnswers(ctx context.Context, workspaceID, chatID string, revision int64) ([]*domain.ChatAnalysisAnswer, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" || revision < 1 {
		return nil, fmt.Errorf("%w: analysis answer scope is required", domain.ErrValidation)
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,revision,question_id,question_fingerprint,selected_option_ids_json,text,disposition,source_dependencies_json,client_key,inherited_from_answer_id,lineage_reason,created_at
		 FROM chat_analysis_answers WHERE workspace_id=? AND chat_work_item_id=? AND revision=? ORDER BY created_at,id`, workspaceID, chatID, revision)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.ChatAnalysisAnswer
	for rows.Next() {
		answer, scanErr := scanChatAnalysisAnswer(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, answer)
	}
	return out, rows.Err()
}

func (r *ChatAnalysisRepo) GetAnswerByClientKey(ctx context.Context, workspaceID, chatID, clientKey string) (*domain.ChatAnalysisAnswer, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(chatID) == "" || strings.TrimSpace(clientKey) == "" {
		return nil, fmt.Errorf("%w: analysis answer identity is required", domain.ErrValidation)
	}
	return r.answerByClientKey(ctx, workspaceID, chatID, clientKey)
}

func (r *ChatAnalysisRepo) CreateInheritedAnswer(ctx context.Context, answer *domain.ChatAnalysisAnswer) error {
	if err := answer.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(answer.InheritedFromAnswerID) == "" || strings.TrimSpace(answer.LineageReason) == "" {
		return fmt.Errorf("%w: inherited answer lineage is required", domain.ErrValidation)
	}
	if answer.CreatedAt.IsZero() {
		answer.CreatedAt = timeNow()
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT OR IGNORE INTO chat_analysis_answers
		 (id,workspace_id,chat_work_item_id,agent_profile_id,revision,question_id,question_fingerprint,selected_option_ids_json,text,disposition,source_dependencies_json,client_key,inherited_from_answer_id,lineage_reason,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, answer.ID, answer.WorkspaceID, answer.ChatWorkItemID,
		answer.AgentProfileID, answer.Revision, answer.QuestionID, answer.QuestionFingerprint,
		jsonText(answer.SelectedOptionIDs), answer.Text, answer.Disposition,
		jsonArrayStringOrEmpty(answer.SourceDependenciesJSON), answer.ClientKey,
		answer.InheritedFromAnswerID, answer.LineageReason, timeParam(answer.CreatedAt))
	return r.store.mapErr(err)
}

func (r *ChatAnalysisRepo) MarkStale(ctx context.Context, workspaceID, chatID string, expectedVersion int, reason string) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analyses SET status=?,error=?,version=version+1,updated_at=?
		 WHERE workspace_id=? AND chat_work_item_id=? AND version=? AND status<>?`,
		domain.ChatAnalysisStale, truncateAnalysisError(reason), timeParam(timeNow()), workspaceID, chatID,
		expectedVersion, domain.ChatAnalysisStale)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		current, getErr := r.Get(ctx, workspaceID, chatID)
		if getErr != nil {
			return getErr
		}
		if current.Version != expectedVersion {
			return domain.ErrVersionConflict
		}
	}
	return nil
}

func (r *ChatAnalysisRepo) MarkRechecked(ctx context.Context, workspaceID, chatID string, expectedVersion int, status domain.ChatAnalysisStatus) error {
	if status != domain.ChatAnalysisNeedsAnswer && status != domain.ChatAnalysisReady {
		return fmt.Errorf("%w: invalid rechecked analysis status", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analyses SET status=?,error='',version=version+1,updated_at=?
		 WHERE workspace_id=? AND chat_work_item_id=? AND version=? AND error NOT LIKE ?`,
		status, timeParam(timeNow()), workspaceID, chatID, expectedVersion, "new_materials:%")
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *ChatAnalysisRepo) CommitRevision(ctx context.Context, attemptID, runID string,
	revision *domain.ChatAnalysisRevision, status domain.ChatAnalysisStatus,
	reconcile func(context.Context, int64, int64) error) (bool, error) {
	if strings.TrimSpace(attemptID) == "" || strings.TrimSpace(runID) == "" || revision == nil || !status.Valid() {
		return false, fmt.Errorf("%w: analysis revision commit input is invalid", domain.ErrValidation)
	}
	attempt, err := r.getAttemptByID(ctx, attemptID)
	if err != nil {
		return false, err
	}
	if attempt.RunID != runID {
		return false, fmt.Errorf("%w: analysis attempt/run mismatch", domain.ErrStateConflict)
	}
	if attempt.Status == domain.ChatAnalysisAttemptSucceeded {
		return false, nil
	}
	if attempt.Status != domain.ChatAnalysisAttemptAnalyzing {
		return false, nil
	}
	projection, err := r.Get(ctx, attempt.WorkspaceID, attempt.ChatWorkItemID)
	if err != nil {
		return false, err
	}
	if projection.CurrentRunID != runID || projection.Version != attempt.Version {
		_, _ = r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE chat_analysis_attempts SET status=?, updated_at=? WHERE id=? AND status=?`,
			domain.ChatAnalysisAttemptStale, timeParam(timeNow()), attemptID, domain.ChatAnalysisAttemptAnalyzing)
		return false, nil
	}
	revision.WorkspaceID = attempt.WorkspaceID
	revision.ChatWorkItemID = attempt.ChatWorkItemID
	revision.AgentProfileID = attempt.AgentProfileID
	revision.RunID = runID
	revision.Revision = projection.Revision + 1
	if revision.CreatedAt.IsZero() {
		revision.CreatedAt = timeNow()
	}
	if err := revision.Validate(); err != nil {
		return false, err
	}
	now := timeNow()
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analyses SET revision=?,status=?,error='',document_json=?,document_digest=?,updated_at=?,version=version+1
		 WHERE workspace_id=? AND chat_work_item_id=? AND current_run_id=? AND version=?`,
		revision.Revision, status, revision.DocumentJSON, revision.DocumentDigest, timeParam(now),
		attempt.WorkspaceID, attempt.ChatWorkItemID, runID, attempt.Version)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_analysis_revisions
		 (id,workspace_id,chat_work_item_id,agent_profile_id,run_id,revision,document_json,document_digest,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?)`, revision.ID, revision.WorkspaceID, revision.ChatWorkItemID,
		revision.AgentProfileID, revision.RunID, revision.Revision, revision.DocumentJSON,
		revision.DocumentDigest, timeParam(revision.CreatedAt)); err != nil {
		if existing, getErr := r.revisionByRun(ctx, runID); getErr == nil && existing.DocumentDigest == revision.DocumentDigest {
			return false, nil
		}
		return false, r.store.mapErr(err)
	}
	if reconcile != nil {
		if err := reconcile(ctx, projection.Revision, revision.Revision); err != nil {
			return false, err
		}
	}
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analysis_attempts SET status=?,revision=?,updated_at=? WHERE id=? AND status=?`,
		domain.ChatAnalysisAttemptSucceeded, revision.Revision, timeParam(now), attemptID, domain.ChatAnalysisAttemptAnalyzing); err != nil {
		return false, r.store.mapErr(err)
	}
	return true, nil
}

func (r *ChatAnalysisRepo) FailAttempt(ctx context.Context, attemptID, runID, message string) (bool, error) {
	attempt, err := r.getAttemptByID(ctx, attemptID)
	if err != nil {
		return false, err
	}
	if attempt.RunID != runID {
		return false, fmt.Errorf("%w: analysis attempt/run mismatch", domain.ErrStateConflict)
	}
	if attempt.Status != domain.ChatAnalysisAttemptAnalyzing {
		return false, nil
	}
	now := timeNow()
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analysis_attempts SET status=?,error=?,updated_at=? WHERE id=? AND status=?`,
		domain.ChatAnalysisAttemptFailed, truncateAnalysisError(message), timeParam(now), attemptID, domain.ChatAnalysisAttemptAnalyzing)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	res, err = r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analyses SET status=?,error=?,updated_at=?,version=version+1
		 WHERE workspace_id=? AND chat_work_item_id=? AND current_run_id=? AND version=?`,
		domain.ChatAnalysisFailed, truncateAnalysisError(message), timeParam(now),
		attempt.WorkspaceID, attempt.ChatWorkItemID, runID, attempt.Version)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return true, nil
	}
	return true, nil
}

func (r *ChatAnalysisRepo) AppendAnswer(ctx context.Context, answer *domain.ChatAnalysisAnswer,
	expectedVersion int, expectedRevision int64, nextStatus domain.ChatAnalysisStatus) (*domain.ChatAnalysisAnswer, *domain.ChatAnalysis, bool, error) {
	if err := answer.Validate(); err != nil {
		return nil, nil, false, err
	}
	if expectedVersion < 1 || expectedRevision < 1 || (nextStatus != domain.ChatAnalysisNeedsAnswer && nextStatus != domain.ChatAnalysisReady) {
		return nil, nil, false, fmt.Errorf("%w: invalid analysis answer CAS", domain.ErrValidation)
	}
	existing, err := r.answerByClientKey(ctx, answer.WorkspaceID, answer.ChatWorkItemID, answer.ClientKey)
	if err == nil {
		if !sameAnalysisAnswer(existing, answer) {
			return nil, nil, false, domain.ErrIdempotencyConflict
		}
		projection, pErr := r.Get(ctx, answer.WorkspaceID, answer.ChatWorkItemID)
		return existing, projection, true, pErr
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, nil, false, err
	}
	projection, err := r.Get(ctx, answer.WorkspaceID, answer.ChatWorkItemID)
	if err != nil {
		return nil, nil, false, err
	}
	if projection.Version != expectedVersion || projection.Revision != expectedRevision ||
		projection.Status != domain.ChatAnalysisNeedsAnswer {
		return nil, nil, false, domain.ErrVersionConflict
	}
	answer.CreatedAt = timeNow()
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO chat_analysis_answers
		 (id,workspace_id,chat_work_item_id,agent_profile_id,revision,question_id,question_fingerprint,selected_option_ids_json,text,disposition,source_dependencies_json,client_key,inherited_from_answer_id,lineage_reason,created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, answer.ID, answer.WorkspaceID, answer.ChatWorkItemID,
		answer.AgentProfileID, answer.Revision, answer.QuestionID, answer.QuestionFingerprint,
		jsonText(answer.SelectedOptionIDs), answer.Text, answer.Disposition,
		jsonArrayStringOrEmpty(answer.SourceDependenciesJSON), answer.ClientKey, answer.InheritedFromAnswerID, answer.LineageReason, timeParam(answer.CreatedAt)); err != nil {
		if existing, getErr := r.answerByClientKey(ctx, answer.WorkspaceID, answer.ChatWorkItemID, answer.ClientKey); getErr == nil {
			if sameAnalysisAnswer(existing, answer) {
				projection, pErr := r.Get(ctx, answer.WorkspaceID, answer.ChatWorkItemID)
				return existing, projection, true, pErr
			}
			return nil, nil, false, domain.ErrIdempotencyConflict
		}
		return nil, nil, false, r.store.mapErr(err)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE chat_analyses SET status=?,version=version+1,updated_at=?
		 WHERE workspace_id=? AND chat_work_item_id=? AND version=? AND revision=? AND status=?`,
		nextStatus, timeParam(answer.CreatedAt), answer.WorkspaceID, answer.ChatWorkItemID,
		expectedVersion, expectedRevision, domain.ChatAnalysisNeedsAnswer)
	if err != nil {
		return nil, nil, false, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, false, domain.ErrVersionConflict
	}
	projection, err = r.Get(ctx, answer.WorkspaceID, answer.ChatWorkItemID)
	return answer, projection, false, err
}

func (r *ChatAnalysisRepo) getAttemptByID(ctx context.Context, id string) (*domain.ChatAnalysisAttempt, error) {
	a, err := scanChatAnalysisAttempt(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,run_id,version,request_digest,source_catalog_json,status,revision,error,created_at,updated_at
		 FROM chat_analysis_attempts WHERE id=?`, id))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return a, nil
}

func (r *ChatAnalysisRepo) revisionByRun(ctx context.Context, runID string) (*domain.ChatAnalysisRevision, error) {
	v, err := scanChatAnalysisRevision(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,run_id,revision,document_json,document_digest,created_at
		 FROM chat_analysis_revisions WHERE run_id=?`, runID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return v, nil
}

func (r *ChatAnalysisRepo) answerByClientKey(ctx context.Context, workspaceID, chatID, clientKey string) (*domain.ChatAnalysisAnswer, error) {
	a, err := scanChatAnalysisAnswer(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id,workspace_id,chat_work_item_id,agent_profile_id,revision,question_id,question_fingerprint,selected_option_ids_json,text,disposition,source_dependencies_json,client_key,inherited_from_answer_id,lineage_reason,created_at
		 FROM chat_analysis_answers WHERE workspace_id=? AND chat_work_item_id=? AND client_key=?`, workspaceID, chatID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return a, nil
}

func sameAnalysisAnswer(a, b *domain.ChatAnalysisAnswer) bool {
	if a == nil || b == nil {
		return false
	}
	return a.WorkspaceID == b.WorkspaceID && a.ChatWorkItemID == b.ChatWorkItemID &&
		a.AgentProfileID == b.AgentProfileID && a.Revision == b.Revision &&
		a.QuestionID == b.QuestionID && a.QuestionFingerprint == b.QuestionFingerprint &&
		strings.Join(a.SelectedOptionIDs, "\x00") == strings.Join(b.SelectedOptionIDs, "\x00") &&
		a.Text == b.Text && a.Disposition == b.Disposition &&
		a.SourceDependenciesJSON == b.SourceDependenciesJSON && a.ClientKey == b.ClientKey &&
		a.InheritedFromAnswerID == b.InheritedFromAnswerID && a.LineageReason == b.LineageReason
}

func jsonArrayStringOrEmpty(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "[]"
	}
	return raw
}

func truncateAnalysisError(message string) string {
	message = strings.TrimSpace(message)
	if len([]rune(message)) > 1000 {
		return string([]rune(message)[:1000]) + "…"
	}
	return message
}
