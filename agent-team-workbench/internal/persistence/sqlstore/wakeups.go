package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// WakeupRepo 维护 agent_wakeup_requests 与 agent 心跳 claim（M4 wakeup 调度）。
// 方法签名与 internal/scheduling.Store 对齐：*WakeupRepo 可直接作为其实现注入调度循环。
type WakeupRepo struct{ store *Store }

const wakeupCols = `id, workspace_id, agent_profile_id, source, task_key, context, status, wake_at, created_at, updated_at`

// terminalRunStatuses 终态集合（与 domain.RunStatus.IsTerminal 对齐）。
// 活跃判定用 NOT IN 终态，保证 reconnecting/succeeding 等过渡态也被视为「有 run 在跑」，
// 避免 runner 断线重连/成功收尾窗口被判「无活跃 run」而穿透双跑。
const terminalRunStatuses = `('succeeded','interrupted','cancelled','lost','failed')`

// nonTerminalWorkItemStatuses 工作项非终态集合（timer 唤醒锚定的候选任务）。
const nonTerminalWorkItemStatuses = `('todo','in_progress','blocked')`

func (r *WakeupRepo) scan(row interface{ Scan(...any) error }) (*domain.WakeupRequest, error) {
	w := &domain.WakeupRequest{}
	var contextJSON string
	var wakeAt, created, updated scanTime
	if err := row.Scan(&w.ID, &w.WorkspaceID, &w.AgentProfileID, &w.Source, &w.TaskKey,
		&contextJSON, &w.Status, &wakeAt, &created, &updated); err != nil {
		return nil, err
	}
	_ = jsonInto(contextJSON, &w.Context)
	w.WakeAt, w.CreatedAt, w.UpdatedAt = mustTime(wakeAt), mustTime(created), mustTime(updated)
	return w, nil
}

// EnqueueWakeup 插入一条唤醒请求；context 为 nil 时落 '{}'。
func (r *WakeupRepo) EnqueueWakeup(ctx context.Context, w *domain.WakeupRequest) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO agent_wakeup_requests(id, workspace_id, agent_profile_id, source, task_key,
			context, status, wake_at, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		w.ID, w.WorkspaceID, w.AgentProfileID, w.Source, w.TaskKey,
		w.ContextJSON(), w.Status, d.TimeParam(w.WakeAt), d.TimeParam(w.CreatedAt), d.TimeParam(w.UpdatedAt))
	return r.store.mapErr(err)
}

// DueTimers 返回到期的 queued 唤醒（全部来源：timer 由循环驱动，assignment/on_demand
// 由事件入队后经同一循环消费，保证单消费者串行、coalescing 判定一致），按 wake_at 升序。
func (r *WakeupRepo) DueTimers(ctx context.Context, now time.Time, limit int) ([]domain.WakeupRequest, error) {
	d := r.store.dialect
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+wakeupCols+` FROM agent_wakeup_requests
		 WHERE status=? AND wake_at<=? ORDER BY wake_at LIMIT ?`,
		domain.WakeupStatusQueued, d.TimeParam(now), limit)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []domain.WakeupRequest
	for rows.Next() {
		w, err := r.scan(rows)
		if err != nil {
			return nil, r.store.mapErr(err)
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// MarkWakeupStatus 迁移请求状态（queued → coalesced | consumed），带 CAS：
// 仅当仍处于 queued 时生效。RowsAffected=0（已被并发消费者占住/迁移）返回
// domain.ErrWakeupNotQueued，调用方应视为已处理、不再建 run。
func (r *WakeupRepo) MarkWakeupStatus(ctx context.Context, id string, status domain.WakeupStatus) error {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_wakeup_requests SET status=?, updated_at=? WHERE id=? AND status=?`,
		status, d.TimeParam(timeNow()), id, domain.WakeupStatusQueued)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrWakeupNotQueued
	}
	return nil
}

// RequeueWakeup 消费补偿：CAS 把 consumed 回退为 queued（建 run 失败时配合
// ReleaseHeartbeatClaim 使用，保证唤醒不因一次瞬时故障被烧掉）。
// 仅当仍处于 consumed 时生效，不会覆盖并发方的其他迁移。
func (r *WakeupRepo) RequeueWakeup(ctx context.Context, id string) error {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_wakeup_requests SET status=?, updated_at=? WHERE id=? AND status=?`,
		domain.WakeupStatusQueued, d.TimeParam(timeNow()), id, domain.WakeupStatusConsumed)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrWakeupNotQueued
	}
	return nil
}

// SetWakeupContext 覆写请求的 context 列。coalescing 降级审计用：转发失败时把
// instruction 附加进 context 落库，避免静默丢弃。
func (r *WakeupRepo) SetWakeupContext(ctx context.Context, id string, wakeContext map[string]any) error {
	d := r.store.dialect
	w := &domain.WakeupRequest{Context: wakeContext}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_wakeup_requests SET context=?, updated_at=? WHERE id=?`,
		w.ContextJSON(), d.TimeParam(timeNow()), id)
	return r.store.mapErr(err)
}

// HasQueuedTimer 幂等判定：该 (agent, task_key) 是否已有 queued 的 timer 唤醒
// （timer 生产步骤防每 tick 堆积）。
func (r *WakeupRepo) HasQueuedTimer(ctx context.Context, agentProfileID, taskKey string) (bool, error) {
	var exists bool
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT EXISTS(SELECT 1 FROM agent_wakeup_requests
		 WHERE agent_profile_id=? AND task_key=? AND source=? AND status=?)`,
		agentProfileID, taskKey, domain.WakeupSourceTimer, domain.WakeupStatusQueued).
		Scan(&exists)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	return exists, nil
}

// ListHeartbeatAgents 委托 Agents().ListHeartbeatEnabled（timer 生产候选）。
func (r *WakeupRepo) ListHeartbeatAgents(ctx context.Context) ([]domain.AgentProfile, error) {
	agents, err := r.store.agents.ListHeartbeatEnabled(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.AgentProfile, 0, len(agents))
	for _, a := range agents {
		out = append(out, *a)
	}
	return out, nil
}

// AssignedTasks 返回 agent 名下非终态工作项（timer 自主唤醒的锚点，与 assignment
// 源一致：task_key 默认即 work item id；title 供唤醒 context 携带给模板渲染）。
func (r *WakeupRepo) AssignedTasks(ctx context.Context, agentProfileID string) ([]scheduling.TaskRef, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id, title FROM work_items
		 WHERE agent_profile_id=? AND status IN `+nonTerminalWorkItemStatuses+`
		 ORDER BY created_at`, agentProfileID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []scheduling.TaskRef
	for rows.Next() {
		var ref scheduling.TaskRef
		if err := rows.Scan(&ref.Key, &ref.Title); err != nil {
			return nil, r.store.mapErr(err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// RecentByAgentTask 返回该 (agent, task_key) 自 since 以来的唤醒记录（含已合并，审计用），按创建时间倒序。
func (r *WakeupRepo) RecentByAgentTask(ctx context.Context, agentProfileID, taskKey string, since time.Time) ([]domain.WakeupRequest, error) {
	d := r.store.dialect
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+wakeupCols+` FROM agent_wakeup_requests
		 WHERE agent_profile_id=? AND task_key=? AND created_at>=? ORDER BY created_at DESC`,
		agentProfileID, taskKey, d.TimeParam(since))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []domain.WakeupRequest
	for rows.Next() {
		w, err := r.scan(rows)
		if err != nil {
			return nil, r.store.mapErr(err)
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// ClaimHeartbeat 原子心跳 claim：单条 UPDATE 同时完成「检查间隔 + 写入心跳时间」，
// 距上次心跳不足 minInterval（rows affected=0）返回 false。last_heartbeat_at 为 NULL 视为可 claim。
func (r *WakeupRepo) ClaimHeartbeat(ctx context.Context, agentProfileID string, minInterval time.Duration, now time.Time) (bool, error) {
	d := r.store.dialect
	deadline := now.Add(-minInterval)
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_profiles SET last_heartbeat_at=?
		 WHERE id=? AND (last_heartbeat_at IS NULL OR last_heartbeat_at<=?)`,
		d.TimeParam(now), agentProfileID, d.TimeParam(deadline))
	if err != nil {
		return false, r.store.mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleaseHeartbeatClaim 回滚一次心跳 claim（建 run 失败的补偿）：仅当
// last_heartbeat_at 仍等于本次 claim 写入的 claimedAt 时才复位为 NULL，
// 防止覆盖其他消费者随后写入的新 claim。复位后下一次 claim 立即可命中，
// 唤醒可在下一 tick 重试而不是白等一个心跳周期。
func (r *WakeupRepo) ReleaseHeartbeatClaim(ctx context.Context, agentProfileID string, claimedAt time.Time) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE agent_profiles SET last_heartbeat_at=NULL
		 WHERE id=? AND last_heartbeat_at=?`,
		agentProfileID, d.TimeParam(claimedAt))
	return r.store.mapErr(err)
}

// ActiveRunKeyForAgentTask 查 (agent, task_key) 是否有活动 run（execution_runs.work_item_id = task_key）。
// 返回最新一条非终态 run 的 id 与 alive：
//   - 带活跃 lease（released_at IS NULL 且 renewed_until>now）→ alive（runner 执行中）；
//   - 有 lease 但全部已死/已释放 → zombie（alive=false，可穿透创建新 run）；
//   - 完全没有 lease 行 → 进程内模块执行（control-plane 自身进程，随进程生死）→ alive。
//
// 非终态判定用 NOT IN 终态集：reconnecting（runner 断线重连窗口）与 succeeding
// （成功收尾窗口）都算活动，防止这两个窗口被判「无活跃 run」穿透双跑。
func (r *WakeupRepo) ActiveRunKeyForAgentTask(ctx context.Context, agentProfileID, taskKey string) (runID string, alive bool, err error) {
	d := r.store.dialect
	var id string
	var activeLease, anyLease bool
	err = r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT r.id,
			EXISTS(SELECT 1 FROM run_leases l
			 WHERE l.run_id=r.id AND l.released_at IS NULL AND l.renewed_until > ?),
			EXISTS(SELECT 1 FROM run_leases l WHERE l.run_id=r.id)
		 FROM execution_runs r
		 WHERE r.agent_profile_id=? AND r.work_item_id=? AND r.status NOT IN `+terminalRunStatuses+`
		 ORDER BY r.created_at DESC, r.id DESC LIMIT 1`,
		d.TimeParam(timeNow()), agentProfileID, taskKey).
		Scan(&id, &activeLease, &anyLease)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, activeLease || !anyLease, nil
}

// GetAgentProfile 委托 Agents().Get，使 *WakeupRepo 单独满足 internal/scheduling.Store。
func (r *WakeupRepo) GetAgentProfile(ctx context.Context, id string) (*domain.AgentProfile, error) {
	return r.store.agents.Get(ctx, id)
}
