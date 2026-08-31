package sqlstore

import (
	"context"
	"fmt"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type WorkspaceRepo struct{ store *Store }

func (r *WorkspaceRepo) Create(ctx context.Context, ws *domain.Workspace) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		ws.ID, ws.Name, ws.Timezone, ws.Version, timeParam(ws.CreatedAt), timeParam(ws.UpdatedAt))
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
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE workspaces SET name=?, timezone=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		ws.Name, ws.Timezone, timeParam(timeNow()), ws.ID, expectedVersion)
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

const agentCols = `id, workspace_id, kind, slug, name, role, skills, instructions, avatar,
	availability, presence, runtime_preference, model_override, policy,
	heartbeat_enabled, heartbeat_interval_sec, wake_on_assignment, wake_on_demand,
	wake_on_automation, prompt_template, last_heartbeat_at, version, created_at, updated_at,
	prompt_version, instructions_editable`

func (r *AgentRepo) scan(row interface{ Scan(...any) error }, a *domain.AgentProfile) error {
	var skills, pref, model, policy string
	var avatar *string
	var lastHeartbeat scanTime
	var created, updated scanTime
	var kind string
	if err := row.Scan(&a.ID, &a.WorkspaceID, &kind, &a.Slug, &a.Name, &a.Role, &skills, &a.Instructions, &avatar,
		&a.Availability, &a.Presence, &pref, &model, &policy,
		&a.HeartbeatEnabled, &a.HeartbeatIntervalSec, &a.WakeOnAssignment, &a.WakeOnDemand,
		&a.WakeOnAutomation, &a.PromptTemplate, &lastHeartbeat,
		&a.Version, &created, &updated, &a.PromptVersion, &a.InstructionsEditable); err != nil {
		return err
	}
	a.Kind = domain.AgentProfileKind(kind)
	if a.Kind == "" {
		a.Kind = domain.AgentProfileKindUser
	}
	// The persisted boolean is a convenience for DTOs; system identity is the
	// authority. This also keeps old/in-memory ordinary agents editable when
	// their zero value is loaded before migration.
	if !a.Kind.IsSystem() {
		a.InstructionsEditable = true
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
	kind := a.Kind
	if kind == "" {
		kind = domain.AgentProfileKindUser
	}
	if !kind.Valid() {
		return fmt.Errorf("%w: agent profile kind 无效", domain.ErrValidation)
	}
	a.Kind = kind
	if kind.IsSystem() {
		a.InstructionsEditable = false
		if a.PromptVersion == "" {
			a.PromptVersion = domain.TaskCoordinatorPromptVersion
		}
	} else {
		a.InstructionsEditable = true
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO agent_profiles(`+agentCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.WorkspaceID, a.Kind, a.Slug, a.Name, a.Role, jsonText(a.Skills), a.Instructions, nullString(a.Avatar),
		a.Availability, a.Presence, jsonText(a.RuntimePreference), jsonText(a.ModelOverride), jsonText(a.Policy),
		a.HeartbeatEnabled, a.HeartbeatIntervalSec, a.WakeOnAssignment, a.WakeOnDemand,
		a.WakeOnAutomation, a.PromptTemplate, nullTimeParam(a.LastHeartbeatAt),
		a.Version, timeParam(a.CreatedAt), timeParam(a.UpdatedAt), a.PromptVersion, a.InstructionsEditable)
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
		`SELECT `+agentCols+` FROM agent_profiles WHERE workspace_id=? AND kind=? ORDER BY created_at`,
		workspaceID, domain.AgentProfileKindUser)
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
	// A system profile is managed exclusively by TaskCoordinatorRepo. Check the
	// persisted identity rather than trusting a caller-provided object.
	var kind string
	if err := r.store.queryRow(ctx, r.store.exec(ctx), `SELECT kind FROM agent_profiles WHERE id=?`, a.ID).Scan(&kind); err != nil {
		return r.store.mapErr(err)
	}
	if domain.AgentProfileKind(kind).IsSystem() {
		return fmt.Errorf("%w: system task coordinator profile is protected", domain.ErrValidation)
	}
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
		timeParam(a.UpdatedAt), a.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *AgentRepo) SetPresence(ctx context.Context, id string, presence domain.AgentPresence) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_profiles SET presence=?, updated_at=? WHERE id=?`,
		presence, timeParam(timeNow()), id)
	return r.store.mapErr(err)
}

// ListHeartbeatEnabled 返回开启心跳自主唤醒（heartbeat_enabled）且可调度的 agent
// （timer 唤醒生产候选）。到期判定（last_heartbeat_at 距今 ≥ interval）由调度层
// 按 profile/global 缺省间隔完成，SQL 不做逐行间隔计算以保持双方言可移植。
func (r *AgentRepo) ListHeartbeatEnabled(ctx context.Context) ([]*domain.AgentProfile, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+agentCols+` FROM agent_profiles
		 WHERE availability=? AND heartbeat_enabled=1 AND kind=? ORDER BY created_at`,
		domain.AgentEnabled, domain.AgentProfileKindUser)
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
