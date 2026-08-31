package sqlstore

import (
	"context"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type TaskSessionRepo struct{ store *Store }

const taskSessionCols = `id, workspace_id, agent_profile_id, adapter_id, task_key, parent_anchor_id,
	session_params, display_id, runs_count, input_tokens_cum, segment_seq,
	context_snapshot_id, context_generation, last_run_id, anchor_run_sequence, created_at, updated_at`

func (r *TaskSessionRepo) scan(row interface{ Scan(...any) error }) (*domain.TaskSession, error) {
	t := &domain.TaskSession{}
	var displayID, parentAnchor, ctxSnapID, lastRunID *string
	var params string
	var created, updated scanTime
	if err := row.Scan(&t.ID, &t.WorkspaceID, &t.AgentProfileID, &t.AdapterID, &t.TaskKey,
		&parentAnchor, &params, &displayID, &t.RunsCount, &t.InputTokensCum, &t.SegmentSeq,
		&ctxSnapID, &t.ContextGeneration, &lastRunID, &t.AnchorRunSequence,
		&created, &updated); err != nil {
		return nil, err
	}
	if parentAnchor != nil {
		t.ParentAnchorID = *parentAnchor
	}
	_ = jsonInto(params, &t.SessionParams)
	if displayID != nil {
		t.DisplayID = *displayID
	}
	if ctxSnapID != nil {
		t.ContextSnapshotID = *ctxSnapID
	}
	if lastRunID != nil {
		t.LastRunID = *lastRunID
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

// ClaimAnchor is the only Run-creation write path for anchor ownership. The
// sequence increment occurs inside the unique-key conflict update, so concurrent
// transactions cannot derive the next value from the same stale read. Existing
// session material is intentionally preserved: claiming a newer Run must not
// erase its resumable provider reference before that Run reports a replacement.
func (r *TaskSessionRepo) ClaimAnchor(ctx context.Context, t *domain.TaskSession) (*domain.TaskSession, error) {
	d := r.store.dialect
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`INSERT INTO task_sessions(id, workspace_id, agent_profile_id, adapter_id, task_key, parent_anchor_id,
			session_params, display_id, runs_count, input_tokens_cum, segment_seq,
			context_snapshot_id, context_generation, last_run_id, anchor_run_sequence, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id, agent_profile_id, adapter_id, task_key) DO UPDATE SET
			context_snapshot_id=excluded.context_snapshot_id,
			context_generation=excluded.context_generation,
			last_run_id=excluded.last_run_id,
			anchor_run_sequence=task_sessions.anchor_run_sequence+1,
			updated_at=excluded.updated_at
		 RETURNING `+taskSessionCols,
		t.ID, t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey, nullString(t.ParentAnchorID),
		jsonText(t.SessionParams), nullString(t.DisplayID), t.RunsCount, t.InputTokensCum, segmentSeq(t),
		nullString(t.ContextSnapshotID), t.ContextGeneration, nullString(t.LastRunID), t.AnchorRunSequence,
		d.TimeParam(t.CreatedAt), d.TimeParam(t.UpdatedAt))
	claimed, err := r.scan(row)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return claimed, nil
}

// InsertIfAbsent is deliberately narrower than Upsert: recovery paths use it
// when no anchor was observed, and a concurrent ClaimAnchor must win without
// having its provider session material replaced by a stale tombstone.
func (r *TaskSessionRepo) InsertIfAbsent(ctx context.Context, t *domain.TaskSession) (bool, error) {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_sessions(id, workspace_id, agent_profile_id, adapter_id, task_key, parent_anchor_id,
			session_params, display_id, runs_count, input_tokens_cum, segment_seq,
			context_snapshot_id, context_generation, last_run_id, anchor_run_sequence, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id, agent_profile_id, adapter_id, task_key) DO NOTHING`,
		t.ID, t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey, nullString(t.ParentAnchorID),
		jsonText(t.SessionParams), nullString(t.DisplayID), t.RunsCount, t.InputTokensCum, segmentSeq(t),
		nullString(t.ContextSnapshotID), t.ContextGeneration, nullString(t.LastRunID), t.AnchorRunSequence,
		d.TimeParam(t.CreatedAt), d.TimeParam(t.UpdatedAt))
	if err != nil {
		return false, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// segmentSeq 应用层片段序号归一：构造方（锚点写点）不感知序号，零值按首段 1 落库。
func segmentSeq(t *domain.TaskSession) int {
	if t.SegmentSeq < 1 {
		return 1
	}
	return t.SegmentSeq
}

// Upsert：params/display_id/parent_anchor_id 整体替换，计数 delta 原子累加。
// 冲突更新绝不改 context/anchor 四列：ClaimAnchor 是唯一能转移 Run ownership
// 的写点，通用 session 更新、墓碑和播种都不得把新 owner/sequence 回退。
// segment_seq 非冲突路径按传入值（首段 1）落库，冲突（续接）路径保持不变。
func (r *TaskSessionRepo) Upsert(ctx context.Context, t *domain.TaskSession) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_sessions(id, workspace_id, agent_profile_id, adapter_id, task_key, parent_anchor_id,
			session_params, display_id, runs_count, input_tokens_cum, segment_seq,
			context_snapshot_id, context_generation, last_run_id, anchor_run_sequence, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id, agent_profile_id, adapter_id, task_key) DO UPDATE SET
			parent_anchor_id=excluded.parent_anchor_id,
			session_params=excluded.session_params,
			display_id=excluded.display_id,
			runs_count=task_sessions.runs_count+excluded.runs_count,
			input_tokens_cum=task_sessions.input_tokens_cum+excluded.input_tokens_cum,
			updated_at=excluded.updated_at`,
		t.ID, t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey, nullString(t.ParentAnchorID),
		jsonText(t.SessionParams), nullString(t.DisplayID),
		t.RunsCount, t.InputTokensCum, segmentSeq(t),
		nullString(t.ContextSnapshotID), t.ContextGeneration, nullString(t.LastRunID), t.AnchorRunSequence,
		d.TimeParam(t.CreatedAt), d.TimeParam(t.UpdatedAt))
	return r.store.mapErr(err)
}

// UpdateIfAnchorOwner is the callback-side CAS companion to ClaimAnchor. A
// callback that lost the race to a newer Run must not resurrect its session ref,
// clear the newer ref, or roll anchor ownership back.
func (r *TaskSessionRepo) UpdateIfAnchorOwner(ctx context.Context, t *domain.TaskSession, runID string, sequence int64) (bool, error) {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE task_sessions SET parent_anchor_id=?, session_params=?, display_id=?,
		 runs_count=runs_count+?, input_tokens_cum=input_tokens_cum+?, updated_at=?
		 WHERE workspace_id=? AND agent_profile_id=? AND adapter_id=? AND task_key=?
		   AND last_run_id=? AND anchor_run_sequence=?`,
		nullString(t.ParentAnchorID), jsonText(t.SessionParams), nullString(t.DisplayID),
		t.RunsCount, t.InputTokensCum, d.TimeParam(t.UpdatedAt),
		t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey, runID, sequence)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// StartGeneration 轮换换代：params/display/parent 整体替换，计数按传入值覆盖
// 重起、created_at 重置（新代际的轮换阈值从零计量；仅轮换 run 首次会话上报时
// 调用）。冲突路径不改 owner/context；这些列仅由 ClaimAnchor 改写。
func (r *TaskSessionRepo) StartGeneration(ctx context.Context, t *domain.TaskSession) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO task_sessions(id, workspace_id, agent_profile_id, adapter_id, task_key, parent_anchor_id,
			session_params, display_id, runs_count, input_tokens_cum, segment_seq,
			context_snapshot_id, context_generation, last_run_id, anchor_run_sequence, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(workspace_id, agent_profile_id, adapter_id, task_key) DO UPDATE SET
			parent_anchor_id=excluded.parent_anchor_id,
			session_params=excluded.session_params,
			display_id=excluded.display_id,
			runs_count=excluded.runs_count,
			input_tokens_cum=excluded.input_tokens_cum,
			segment_seq=task_sessions.segment_seq+1,
			created_at=excluded.created_at,
			updated_at=excluded.updated_at`,
		t.ID, t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey, nullString(t.ParentAnchorID),
		jsonText(t.SessionParams), nullString(t.DisplayID),
		t.RunsCount, t.InputTokensCum, segmentSeq(t),
		nullString(t.ContextSnapshotID), t.ContextGeneration, nullString(t.LastRunID), t.AnchorRunSequence,
		d.TimeParam(t.CreatedAt), d.TimeParam(t.UpdatedAt))
	return r.store.mapErr(err)
}

// StartGenerationIfAnchorOwner is the rotation callback variant of
// UpdateIfAnchorOwner. The claim already installed the new context/owner, so
// this CAS only rotates session material and counters while ownership remains.
func (r *TaskSessionRepo) StartGenerationIfAnchorOwner(ctx context.Context, t *domain.TaskSession, runID string, sequence int64) (bool, error) {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE task_sessions SET parent_anchor_id=?, session_params=?, display_id=?,
		 runs_count=?, input_tokens_cum=?, segment_seq=segment_seq+1, created_at=?, updated_at=?
		 WHERE workspace_id=? AND agent_profile_id=? AND adapter_id=? AND task_key=?
		   AND last_run_id=? AND anchor_run_sequence=?`,
		nullString(t.ParentAnchorID), jsonText(t.SessionParams), nullString(t.DisplayID),
		t.RunsCount, t.InputTokensCum, d.TimeParam(t.CreatedAt), d.TimeParam(t.UpdatedAt),
		t.WorkspaceID, t.AgentProfileID, t.AdapterID, t.TaskKey, runID, sequence)
	if err != nil {
		return false, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
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
