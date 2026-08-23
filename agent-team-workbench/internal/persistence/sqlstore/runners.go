package sqlstore

import (
	"context"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
)

type RunnerRepo struct{ store *Store }

func (r *RunnerRepo) Upsert(ctx context.Context, rn *application.Runner) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO runners(id, workspace_id, label, runner_version, os, arch, slots, status, last_seen_at, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO UPDATE SET
			runner_version=excluded.runner_version, os=excluded.os, arch=excluded.arch,
			slots=excluded.slots, status=excluded.status, last_seen_at=excluded.last_seen_at`,
		rn.ID, rn.WorkspaceID, rn.Label, rn.RunnerVersion, rn.OS, rn.Arch, rn.Slots,
		rn.Status, d.NullTimeParam(rn.LastSeenAt), d.TimeParam(timeNow()))
	return r.store.mapErr(err)
}

func (r *RunnerRepo) SetStatus(ctx context.Context, runnerID, status string, at time.Time) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE runners SET status=?, last_seen_at=? WHERE id=?`,
		status, d.TimeParam(at), runnerID)
	return r.store.mapErr(err)
}

func (r *RunnerRepo) List(ctx context.Context, workspaceID string) ([]*application.Runner, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id, workspace_id, label, runner_version, os, arch, slots, status, last_seen_at
		 FROM runners WHERE workspace_id=? ORDER BY created_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*application.Runner
	for rows.Next() {
		rn := &application.Runner{}
		var version, os_, arch *string
		var lastSeen scanTime
		if err := rows.Scan(&rn.ID, &rn.WorkspaceID, &rn.Label, &version, &os_, &arch,
			&rn.Slots, &rn.Status, &lastSeen); err != nil {
			return nil, err
		}
		if version != nil {
			rn.RunnerVersion = *version
		}
		if os_ != nil {
			rn.OS = *os_
		}
		if arch != nil {
			rn.Arch = *arch
		}
		rn.LastSeenAt = optTime(lastSeen)
		out = append(out, rn)
	}
	return out, rows.Err()
}

// CreateLease 分配递增 fencing_token：同一 run 的新 lease 总是大于旧值。
func (r *RunnerRepo) CreateLease(ctx context.Context, l *application.RunLease) error {
	d := r.store.dialect
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`INSERT INTO run_leases(lease_id, run_id, runner_id, fencing_token, acquired_at, renewed_until)
		 VALUES (?, ?, ?, (SELECT COALESCE(MAX(fencing_token),0)+1 FROM run_leases WHERE run_id=?), ?, ?)
		 RETURNING fencing_token`,
		l.LeaseID, l.RunID, l.RunnerID, l.RunID,
		d.TimeParam(timeNow()), d.TimeParam(l.RenewedUntil)).Scan(&l.FencingToken)
	return r.store.mapErr(err)
}

// ActiveLease 返回未释放的最大 fencing lease。
func (r *RunnerRepo) ActiveLease(ctx context.Context, runID string) (*application.RunLease, error) {
	l := &application.RunLease{}
	var renewed scanTime
	var released *time.Time
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT lease_id, run_id, runner_id, fencing_token, renewed_until, released_at
		 FROM run_leases WHERE run_id=? ORDER BY fencing_token DESC LIMIT 1`, runID).
		Scan(&l.LeaseID, &l.RunID, &l.RunnerID, &l.FencingToken, &renewed, &released)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	l.RenewedUntil = mustTime(renewed)
	l.Released = released != nil
	return l, nil
}

func (r *RunnerRepo) ReleaseLease(ctx context.Context, leaseID string, at time.Time) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE run_leases SET released_at=? WHERE lease_id=?`, d.TimeParam(at), leaseID)
	return r.store.mapErr(err)
}

func (r *RunnerRepo) ExpireLeases(ctx context.Context, now time.Time) ([]string, error) {
	d := r.store.dialect
	db := r.store.exec(ctx)
	rows, err := r.store.query(ctx, db,
		`SELECT run_id FROM run_leases WHERE released_at IS NULL AND renewed_until < ?`,
		d.TimeParam(now))
	if err != nil {
		return nil, err
	}
	var runIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		runIDs = append(runIDs, id)
	}
	rows.Close()
	if len(runIDs) == 0 {
		return nil, rows.Err()
	}
	if _, err := r.store.execStmt(ctx, db,
		`UPDATE run_leases SET released_at=? WHERE released_at IS NULL AND renewed_until < ?`,
		d.TimeParam(now), d.TimeParam(now)); err != nil {
		return nil, r.store.mapErr(err)
	}
	return runIDs, rows.Err()
}

type AuditRepo struct{ store *Store }

// RenewLeasesByRunner 兑现 welcome 广告的 lease_policy.renew_interval_seconds：
// heartbeat 是 runner 存活证明，推进该 runner 名下「run 仍非终态」的活跃 lease 的
// renewed_until（TTL 从续租时刻重新计时）；run 已终态的残留 lease 顺手置
// released_at（幂等，与 finalizeIfTerminal 的释放互不冲突）。返回续租行数。
// 不加 renewed_until>now 前置条件：心跳迟于 TTL 到达时只要 lease 未被 sweeper
// 释放即可续——续租由 runner 存活证据驱动，而非 lease 新鲜度；runner 真正失联后
// 无心跳 → sweeper 释放路径保持不变。
func (r *RunnerRepo) RenewLeasesByRunner(ctx context.Context, runnerID string, renewUntil time.Time) (int, error) {
	d := r.store.dialect
	db := r.store.exec(ctx)
	// 终态 run 的残留 lease：回收（finalizeIfTerminal 之外的路径置为终态时兜底）。
	if _, err := r.store.execStmt(ctx, db,
		`UPDATE run_leases SET released_at=?
		  WHERE runner_id=? AND released_at IS NULL
		    AND run_id IN (SELECT id FROM execution_runs
		                    WHERE status IN ('succeeded','interrupted','cancelled','lost','failed'))`,
		d.TimeParam(timeNow()), runnerID); err != nil {
		return 0, r.store.mapErr(err)
	}
	// 非终态 run 的活跃 lease：推进 renewed_until。
	res, err := r.store.execStmt(ctx, db,
		`UPDATE run_leases SET renewed_until=?
		  WHERE runner_id=? AND released_at IS NULL
		    AND run_id IN (SELECT id FROM execution_runs
		                    WHERE status NOT IN ('succeeded','interrupted','cancelled','lost','failed'))`,
		d.TimeParam(renewUntil), runnerID)
	if err != nil {
		return 0, r.store.mapErr(err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Append 写不可变审计记录（协议文档 §10.1）。
func (r *AuditRepo) Append(ctx context.Context, workspaceID string, actor map[string]any, action, target string, detail map[string]any) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO audit_logs(workspace_id, actor, action, target, detail, occurred_at)
		 VALUES (?,?,?,?,?,?)`,
		nullString(workspaceID), jsonText(actor), action, nullString(target),
		jsonText(detail), d.TimeParam(timeNow()))
	return r.store.mapErr(err)
}

type CapsRepo struct{ store *Store }

func (r *CapsRepo) Create(ctx context.Context, s *application.CapabilitySnapshot) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO capability_snapshots(id, run_id, required, advertised, created_at)
		 VALUES (?,?,?,?,?)`,
		s.ID, nullString(s.RunID), jsonText(s.Required), jsonText(s.Advertised), d.TimeParam(timeNow()))
	return r.store.mapErr(err)
}

func (r *CapsRepo) Get(ctx context.Context, id string) (*application.CapabilitySnapshot, error) {
	s := &application.CapabilitySnapshot{}
	var required, advertised string
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id, run_id, required, advertised FROM capability_snapshots WHERE id=?`, id).
		Scan(&s.ID, &s.RunID, &required, &advertised)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	_ = jsonInto(required, &s.Required)
	_ = jsonInto(advertised, &s.Advertised)
	return s, nil
}

// RunnerEventDedup 按 (run_id, runner_id, runner_seq) 去重；重复返回 ErrIdempotencyConflict。
func (r *RunnerRepo) RunnerEventDedup(ctx context.Context, runID, runnerID string, runnerSeq int64) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO runner_event_dedup(run_id, runner_id, runner_seq, run_seq) VALUES (?,?,?,0)`,
		runID, runnerID, runnerSeq)
	return r.store.mapErr(err)
}
