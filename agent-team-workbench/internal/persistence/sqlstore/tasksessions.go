package sqlstore

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type TaskSessionRepo struct{ store *Store }

const taskSessionCols = `id, workspace_id, agent_profile_id, adapter_id, task_key,
	session_params, display_id, runs_count, input_tokens_cum, created_at, updated_at`

func (r *TaskSessionRepo) scan(row interface{ Scan(...any) error }) (*domain.TaskSession, error) {
	t := &domain.TaskSession{}
	var displayID *string
	var params string
	var created, updated scanTime
	if err := row.Scan(&t.ID, &t.WorkspaceID, &t.AgentProfileID, &t.AdapterID, &t.TaskKey,
		&params, &displayID, &t.RunsCount, &t.InputTokensCum, &created, &updated); err != nil {
		return nil, err
	}
	_ = jsonInto(params, &t.SessionParams)
	if displayID != nil {
		t.DisplayID = *displayID
	}
	t.CreatedAt, t.UpdatedAt = mustTime(created), mustTime(updated)
	return t, nil
}

func (r *TaskSessionRepo) Get(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey string) (*domain.TaskSession, error) {
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+taskSessionCols+` FROM task_sessions
		 WHERE workspace_id=? AND agent_profile_id=? AND adapter_id=? AND task_key=?`,
		workspaceID, agentProfileID, adapterID, taskKey)
	t, err := r.scan(row)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return t, nil
}

// Upsert：params/display_id 整体替换；计数列按 delta 累加（见接口注释）。
func (r *TaskSessionRepo) Upsert(ctx context.Context, t *domain.TaskSession) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_sessions(id, workspace_id, agent_profile_id, adapter_id, task_key,
			session_params, display_id, runs_count, input_tokens_cum, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id, agent_profile_id, adapter_id, task_key) DO UPDATE SET
			session_params=excluded.session_params,
			display_id=excluded.display_id,
			runs_count=task_sessions.runs_count+excluded.runs_count,
			input_tokens_cum=task_sessions.input_tokens_cum+excluded.input_tokens_cum,
			updated_at=excluded.updated_at`,
		t.ID, t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey,
		jsonText(t.SessionParams), nullString(t.DisplayID),
		t.RunsCount, t.InputTokensCum, d.TimeParam(t.CreatedAt), d.TimeParam(t.UpdatedAt))
	return r.store.mapErr(err)
}

// StartGeneration 轮换换代：params/display 整体替换，计数按传入值覆盖重起、
// created_at 重置（新代际的轮换阈值从零计量；仅轮换 run 首次会话上报时调用）。
func (r *TaskSessionRepo) StartGeneration(ctx context.Context, t *domain.TaskSession) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_sessions(id, workspace_id, agent_profile_id, adapter_id, task_key,
			session_params, display_id, runs_count, input_tokens_cum, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id, agent_profile_id, adapter_id, task_key) DO UPDATE SET
			session_params=excluded.session_params,
			display_id=excluded.display_id,
			runs_count=excluded.runs_count,
			input_tokens_cum=excluded.input_tokens_cum,
			created_at=excluded.created_at,
			updated_at=excluded.updated_at`,
		t.ID, t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey,
		jsonText(t.SessionParams), nullString(t.DisplayID),
		t.RunsCount, t.InputTokensCum, d.TimeParam(t.CreatedAt), d.TimeParam(t.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *TaskSessionRepo) AddInputTokens(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey string, tokens int64) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE task_sessions SET input_tokens_cum=input_tokens_cum+?, updated_at=?
		 WHERE workspace_id=? AND agent_profile_id=? AND adapter_id=? AND task_key=?`,
		tokens, timeNow(), workspaceID, agentProfileID, adapterID, taskKey)
	return r.store.mapErr(err)
}

func (r *TaskSessionRepo) ListByAgent(ctx context.Context, workspaceID, agentProfileID string) ([]*domain.TaskSession, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+taskSessionCols+` FROM task_sessions
		 WHERE workspace_id=? AND agent_profile_id=? ORDER BY updated_at`,
		workspaceID, agentProfileID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.TaskSession
	for rows.Next() {
		t, err := r.scan(rows)
		if err != nil {
			return nil, r.store.mapErr(err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
