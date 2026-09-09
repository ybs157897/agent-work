package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type TaskPublicationDraftRepo struct{ store *Store }

var _ application.TaskPublicationDraftRepo = (*TaskPublicationDraftRepo)(nil)

func scanTaskPublicationDraft(row interface{ Scan(...any) error }) (*domain.TaskPublicationDraft, error) {
	d := &domain.TaskPublicationDraft{}
	var created, updated scanTime
	var taskID *string
	if err := row.Scan(&d.ID, &d.WorkspaceID, &d.ChatWorkItemID, &d.AgentProfileID, &d.AnalysisRevision,
		&d.ItemIDsJSON, &d.ConfirmationIDsJSON, &d.SourceDependenciesJSON, &d.Title, &d.Description,
		&d.AcceptanceCriteriaJSON, &d.ContextSnapshotID, &d.BaselineJSON, &d.Fingerprint, &d.Status,
		&taskID, &d.ClientKey, &d.Version, &created, &updated); err != nil {
		return nil, err
	}
	if taskID != nil {
		d.TaskID = *taskID
	}
	d.CreatedAt, d.UpdatedAt = mustTime(created), mustTime(updated)
	return d, nil
}

const taskPublicationDraftCols = `id,workspace_id,chat_work_item_id,agent_profile_id,analysis_revision,
item_ids_json,confirmation_ids_json,source_dependencies_json,title,description,acceptance_criteria_json,
context_snapshot_id,baseline_json,fingerprint,status,task_id,client_key,version,created_at,updated_at`

func (r *TaskPublicationDraftRepo) Create(ctx context.Context, draft *domain.TaskPublicationDraft) error {
	if err := draft.Validate(); err != nil {
		return err
	}
	if draft.CreatedAt.IsZero() {
		draft.CreatedAt = timeNow()
	}
	if draft.UpdatedAt.IsZero() {
		draft.UpdatedAt = draft.CreatedAt
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_publication_drafts(`+taskPublicationDraftCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		draft.ID, draft.WorkspaceID, draft.ChatWorkItemID, draft.AgentProfileID, draft.AnalysisRevision,
		draft.ItemIDsJSON, draft.ConfirmationIDsJSON, draft.SourceDependenciesJSON, draft.Title, draft.Description,
		draft.AcceptanceCriteriaJSON, draft.ContextSnapshotID, draft.BaselineJSON, draft.Fingerprint, draft.Status,
		nullString(draft.TaskID), draft.ClientKey, draft.Version, timeParam(draft.CreatedAt), timeParam(draft.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *TaskPublicationDraftRepo) Get(ctx context.Context, workspaceID, chatID, draftID string) (*domain.TaskPublicationDraft, error) {
	d, err := scanTaskPublicationDraft(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+taskPublicationDraftCols+` FROM task_publication_drafts WHERE workspace_id=? AND chat_work_item_id=? AND id=?`, workspaceID, chatID, draftID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return d, nil
}

func (r *TaskPublicationDraftRepo) GetByClientKey(ctx context.Context, workspaceID, chatID, clientKey string) (*domain.TaskPublicationDraft, error) {
	d, err := scanTaskPublicationDraft(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+taskPublicationDraftCols+` FROM task_publication_drafts WHERE workspace_id=? AND chat_work_item_id=? AND client_key=?`, workspaceID, chatID, clientKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return d, nil
}

func (r *TaskPublicationDraftRepo) GetByTaskID(ctx context.Context, taskID string) (*domain.TaskPublicationDraft, error) {
	d, err := scanTaskPublicationDraft(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+taskPublicationDraftCols+` FROM task_publication_drafts WHERE task_id=?`, taskID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return d, nil
}

func (r *TaskPublicationDraftRepo) List(ctx context.Context, workspaceID, chatID string, limit int) ([]*domain.TaskPublicationDraft, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+taskPublicationDraftCols+` FROM task_publication_drafts WHERE workspace_id=? AND chat_work_item_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, workspaceID, chatID, limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.TaskPublicationDraft
	for rows.Next() {
		draft, scanErr := scanTaskPublicationDraft(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, draft)
	}
	return out, rows.Err()
}

func (r *TaskPublicationDraftRepo) UpdateStatus(ctx context.Context, draftID string, status domain.PublicationDraftStatus, taskID string, expectedVersion int) error {
	if !status.Valid() || strings.TrimSpace(draftID) == "" || expectedVersion < 1 {
		return fmt.Errorf("%w: invalid publication draft status update", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE task_publication_drafts SET status=?,task_id=?,version=version+1,updated_at=? WHERE id=? AND version=?`,
		status, nullString(taskID), timeParam(timeNow()), draftID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *TaskPublicationDraftRepo) CreatePublication(ctx context.Context, publication *domain.TaskPublication) error {
	if publication == nil || strings.TrimSpace(publication.ID) == "" || strings.TrimSpace(publication.DraftID) == "" || strings.TrimSpace(publication.TaskID) == "" {
		return fmt.Errorf("%w: invalid publication", domain.ErrValidation)
	}
	if publication.CreatedAt.IsZero() {
		publication.CreatedAt = timeNow()
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_publications(id,draft_id,workspace_id,chat_work_item_id,task_id,analysis_revision,fingerprint,client_key,expected_version,confirmation_ids_json,source_dependencies_json,baseline_json,context_snapshot_id,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		publication.ID, publication.DraftID, publication.WorkspaceID, publication.ChatWorkItemID, publication.TaskID,
		publication.AnalysisRevision, publication.Fingerprint, publication.ClientKey, publication.ExpectedVersion, jsonArray(publication.ConfirmationIDsJSON), jsonArray(publication.SourceDependenciesJSON), jsonObject(publication.BaselineJSON), publication.ContextSnapshotID, timeParam(publication.CreatedAt))
	return r.store.mapErr(err)
}

func (r *TaskPublicationDraftRepo) GetPublicationByDraft(ctx context.Context, draftID string) (*domain.TaskPublication, error) {
	p := &domain.TaskPublication{}
	var created scanTime
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id,draft_id,workspace_id,chat_work_item_id,task_id,analysis_revision,fingerprint,client_key,expected_version,confirmation_ids_json,source_dependencies_json,baseline_json,context_snapshot_id,created_at FROM task_publications WHERE draft_id=?`, draftID).
		Scan(&p.ID, &p.DraftID, &p.WorkspaceID, &p.ChatWorkItemID, &p.TaskID, &p.AnalysisRevision, &p.Fingerprint, &p.ClientKey, &p.ExpectedVersion, &p.ConfirmationIDsJSON, &p.SourceDependenciesJSON, &p.BaselineJSON, &p.ContextSnapshotID, &created); err != nil {
		return nil, r.store.mapErr(err)
	}
	p.CreatedAt = mustTime(created)
	return p, nil
}

func jsonArray(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "[]"
	}
	return raw
}

func jsonObject(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}
