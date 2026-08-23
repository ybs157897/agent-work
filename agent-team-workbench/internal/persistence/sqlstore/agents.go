package sqlstore

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type WorkspaceRepo struct{ store *Store }

func (r *WorkspaceRepo) Create(ctx context.Context, ws *domain.Workspace) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		ws.ID, ws.Name, ws.Timezone, ws.Version, d.TimeParam(ws.CreatedAt), d.TimeParam(ws.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *WorkspaceRepo) Get(ctx context.Context, id string) (*domain.Workspace, error) {
	w := &domain.Workspace{}
	var created, updated scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id, name, timezone, version, created_at, updated_at FROM workspaces WHERE id=?`, id).
		Scan(&w.ID, &w.Name, &w.Timezone, &w.Version, &created, &updated)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	w.CreatedAt, w.UpdatedAt = mustTime(created), mustTime(updated)
	return w, nil
}

// Update 乐观锁：version 不匹配时更新 0 行 → ErrVersionConflict。
func (r *WorkspaceRepo) Update(ctx context.Context, ws *domain.Workspace, expectedVersion int) error {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE workspaces SET name=?, timezone=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		ws.Name, ws.Timezone, d.TimeParam(timeNow()), ws.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *WorkspaceRepo) ListIDs(ctx context.Context) ([]string, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx), `SELECT id FROM workspaces ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type AgentRepo struct{ store *Store }

const agentCols = `id, workspace_id, slug, name, role, skills, instructions, avatar,
	availability, presence, runtime_preference, model_override, policy,
	heartbeat_enabled, heartbeat_interval_sec, wake_on_assignment, wake_on_demand,
	wake_on_automation, prompt_template, last_heartbeat_at, version, created_at, updated_at`

func (r *AgentRepo) scan(row interface{ Scan(...any) error }, a *domain.AgentProfile) error {
	var skills, pref, model, policy string
	var avatar *string
	var lastHeartbeat scanTime
	var created, updated scanTime
	if err := row.Scan(&a.ID, &a.WorkspaceID, &a.Slug, &a.Name, &a.Role, &skills, &a.Instructions, &avatar,
		&a.Availability, &a.Presence, &pref, &model, &policy,
		&a.HeartbeatEnabled, &a.HeartbeatIntervalSec, &a.WakeOnAssignment, &a.WakeOnDemand,
		&a.WakeOnAutomation, &a.PromptTemplate, &lastHeartbeat,
		&a.Version, &created, &updated); err != nil {
		return err
	}
	_ = jsonInto(skills, &a.Skills)
	_ = jsonInto(pref, &a.RuntimePreference)
	_ = jsonInto(model, &a.ModelOverride)
	_ = jsonInto(policy, &a.Policy)
	if avatar != nil {
		a.Avatar = *avatar
	}
	a.LastHeartbeatAt = optTime(lastHeartbeat)
	a.CreatedAt, a.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

func (r *AgentRepo) Create(ctx context.Context, a *domain.AgentProfile) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO agent_profiles(`+agentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.WorkspaceID, a.Slug, a.Name, a.Role, jsonText(a.Skills), a.Instructions, nullString(a.Avatar),
		a.Availability, a.Presence, jsonText(a.RuntimePreference), jsonText(a.ModelOverride), jsonText(a.Policy),
		a.HeartbeatEnabled, a.HeartbeatIntervalSec, a.WakeOnAssignment, a.WakeOnDemand,
		a.WakeOnAutomation, a.PromptTemplate, d.NullTimeParam(a.LastHeartbeatAt),
		a.Version, d.TimeParam(a.CreatedAt), d.TimeParam(a.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *AgentRepo) Get(ctx context.Context, id string) (*domain.AgentProfile, error) {
	a := &domain.AgentProfile{}
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+agentCols+` FROM agent_profiles WHERE id=?`, id)
	if err := r.scan(row, a); err != nil {
		return nil, r.store.mapErr(err)
	}
	return a, nil
}

func (r *AgentRepo) List(ctx context.Context, workspaceID string) ([]*domain.AgentProfile, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+agentCols+` FROM agent_profiles WHERE workspace_id=? ORDER BY created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.AgentProfile
	for rows.Next() {
		a := &domain.AgentProfile{}
		if err := r.scan(rows, a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Update 乐观锁：version 不匹配时更新 0 行 → ErrVersionConflict。
// wakeup 策略列随 Update 持久化；last_heartbeat_at 由 ClaimHeartbeat 独占维护，此处不触碰。
func (r *AgentRepo) Update(ctx context.Context, a *domain.AgentProfile, expectedVersion int) error {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_profiles SET slug=?, name=?, role=?, skills=?, instructions=?, avatar=?,
			availability=?, presence=?, runtime_preference=?, model_override=?, policy=?,
			heartbeat_enabled=?, heartbeat_interval_sec=?, wake_on_assignment=?, wake_on_demand=?,
			wake_on_automation=?, prompt_template=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		a.Slug, a.Name, a.Role, jsonText(a.Skills), a.Instructions, nullString(a.Avatar),
		a.Availability, a.Presence, jsonText(a.RuntimePreference), jsonText(a.ModelOverride), jsonText(a.Policy),
		a.HeartbeatEnabled, a.HeartbeatIntervalSec, a.WakeOnAssignment, a.WakeOnDemand,
		a.WakeOnAutomation, a.PromptTemplate,
		d.TimeParam(a.UpdatedAt), a.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *AgentRepo) SetPresence(ctx context.Context, id string, presence domain.AgentPresence) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_profiles SET presence=?, updated_at=? WHERE id=?`,
		presence, d.TimeParam(timeNow()), id)
	return r.store.mapErr(err)
}

// ListHeartbeatEnabled 返回开启心跳自主唤醒（heartbeat_enabled）且可调度的 agent
// （timer 唤醒生产候选）。到期判定（last_heartbeat_at 距今 ≥ interval）由调度层
// 按 profile/global 缺省间隔完成，SQL 不做逐行间隔计算以保持双方言可移植。
func (r *AgentRepo) ListHeartbeatEnabled(ctx context.Context) ([]*domain.AgentProfile, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+agentCols+` FROM agent_profiles
		 WHERE availability=? AND heartbeat_enabled=1 ORDER BY created_at`,
		domain.AgentEnabled)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.AgentProfile
	for rows.Next() {
		a := &domain.AgentProfile{}
		if err := r.scan(rows, a); err != nil {
			return nil, r.store.mapErr(err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
