package sqlstore

import (
	"context"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type RunnerRepo struct{ store *Store }

func (r *RunnerRepo) Upsert(ctx context.Context, rn *application.Runner) error {
	d := r.store.dialect
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO runners(id, workspace_id, execution_host_id, connection_epoch, boot_id, label, runner_version, os, arch, slots, status, last_seen_at, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (id) DO UPDATE SET
			workspace_id=NULL,
			execution_host_id=excluded.execution_host_id, connection_epoch=excluded.connection_epoch, boot_id=excluded.boot_id,
			runner_version=excluded.runner_version, os=excluded.os, arch=excluded.arch,
			slots=excluded.slots, status=excluded.status, last_seen_at=excluded.last_seen_at
		 WHERE runners.execution_host_id IS NULL OR runners.execution_host_id=excluded.execution_host_id`,
		rn.ID, nil, nullString(rn.ExecutionHostID), nullString(rn.ConnectionEpoch), nullString(rn.BootID),
		rn.Label, rn.RunnerVersion, rn.OS, rn.Arch, rn.Slots,
		rn.Status, d.NullTimeParam(rn.LastSeenAt), d.TimeParam(timeNow()))
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrStateConflict
	}
	return nil
}

// Get 按 ID 读 Runner（v2 hello 的 host/epoch 校验用）。
func (r *RunnerRepo) Get(ctx context.Context, runnerID string) (*application.Runner, error) {
	rn := &application.Runner{}
	var workspaceID, version, os_, arch, hostID, epoch, bootID *string
	var lastSeen scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id, workspace_id, execution_host_id, connection_epoch, boot_id, label, runner_version, os, arch, slots, status, last_seen_at
		 FROM runners WHERE id=?`, runnerID).
		Scan(&rn.ID, &workspaceID, &hostID, &epoch, &bootID, &rn.Label, &version, &os_, &arch,
			&rn.Slots, &rn.Status, &lastSeen)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if workspaceID != nil {
		rn.WorkspaceID = *workspaceID
	}
	if hostID != nil {
		rn.ExecutionHostID = *hostID
	}
	if epoch != nil {
		rn.ConnectionEpoch = *epoch
	}
	if bootID != nil {
		rn.BootID = *bootID
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
	return rn, nil
}

func (r *RunnerRepo) SetStatus(ctx context.Context, runnerID, status string, at time.Time) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE runners SET status=?, last_seen_at=? WHERE id=?`,
		status, d.TimeParam(at), runnerID)
	return r.store.mapErr(err)
}

// List 返回通过 WorkspaceLocation 映射到该 Workspace 的 Runner。Runner v2 是
// Host 基础设施，不能再以 runners.workspace_id 过滤；同一 Host 有多个 Location
// 时 DISTINCT 防止同一个 Runner 重复出现在健康投影。
func (r *RunnerRepo) List(ctx context.Context, workspaceID string) ([]*application.Runner, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT DISTINCT r.id, r.workspace_id, r.execution_host_id, r.connection_epoch, r.boot_id,
			r.label, r.runner_version, r.os, r.arch, r.slots, r.status, r.last_seen_at
		 FROM runners r
		 JOIN workspace_locations l ON l.execution_host_id=r.execution_host_id
		 WHERE l.workspace_id=?
		 ORDER BY r.id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*application.Runner
	for rows.Next() {
		rn := &application.Runner{}
		var workspaceID, version, os_, arch, hostID, epoch, bootID *string
		var lastSeen scanTime
		if err := rows.Scan(&rn.ID, &workspaceID, &hostID, &epoch, &bootID, &rn.Label, &version, &os_, &arch,
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
		if hostID != nil {
			rn.ExecutionHostID = *hostID
		}
		if workspaceID != nil {
			rn.WorkspaceID = *workspaceID
		}
		if epoch != nil {
			rn.ConnectionEpoch = *epoch
		}
		if bootID != nil {
			rn.BootID = *bootID
		}
		rn.LastSeenAt = optTime(lastSeen)
		out = append(out, rn)
	}
	return out, rows.Err()
}

// CreateLease 分配递增 fencing_token：同一 run 的新 lease 总是大于旧值。
func (r *RunnerRepo) CreateLease(ctx context.Context, l *application.RunLease) error {
	// PG advisory_xact_lock 只有与 INSERT 保持同一 transaction 才能串行化
	// MAX(fencing_token)+1；直接仓储调用也必须补这一层 transaction，避免调用者
	// 漏包事务后锁在 INSERT 前释放。SQLite 的 Store.InTx 同样保证写序。
	if ctx.Value(txKey{}) == nil {
		return r.store.InTx(ctx, func(txCtx context.Context) error {
			return r.CreateLease(txCtx, l)
		})
	}
	d := r.store.dialect
	db := r.store.exec(ctx)
	if err := d.AdvisoryLock(ctx, db,
		r.store.ph(`SELECT pg_advisory_xact_lock(hashtext(?))`), "run-lease:"+l.RunID); err != nil {
		return r.store.mapErr(err)
	}
	err := r.store.queryRow(ctx, db,
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

// ListActiveLeasesByRunner 恢复某 Runner 仍持有的非终态租约。Gateway 进程
// 重启或 Runner 连接换 epoch 后以数据库为租约真相重建内存 fencing 表，不能把
// pending event 当 stale ACK 掉。
func (r *RunnerRepo) ListActiveLeasesByRunner(ctx context.Context, runnerID string) ([]*application.RunLease, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT l.lease_id, l.run_id, l.runner_id, l.fencing_token, l.renewed_until
		 FROM run_leases l
		 JOIN execution_runs run ON run.id=l.run_id
		 WHERE l.runner_id=? AND l.released_at IS NULL
		   AND l.fencing_token=(
			   SELECT MAX(current.fencing_token) FROM run_leases current
			   WHERE current.run_id=l.run_id AND current.released_at IS NULL
		   )
		   AND run.status NOT IN ('succeeded','interrupted','cancelled','lost','failed')
		 ORDER BY l.run_id`, runnerID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*application.RunLease
	for rows.Next() {
		l := &application.RunLease{}
		var renewed scanTime
		if err := rows.Scan(&l.LeaseID, &l.RunID, &l.RunnerID, &l.FencingToken, &renewed); err != nil {
			return nil, r.store.mapErr(err)
		}
		l.RenewedUntil = mustTime(renewed)
		out = append(out, l)
	}
	return out, rows.Err()
}

func (r *RunnerRepo) ReleaseLease(ctx context.Context, leaseID string, at time.Time) error {
	d := r.store.dialect
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE run_leases SET released_at=? WHERE lease_id=?`, d.TimeParam(at), leaseID)
	return r.store.mapErr(err)
}

func (r *RunnerRepo) ReleaseActiveLeasesByRunner(ctx context.Context, runnerID string, at time.Time) ([]string, error) {
	d := r.store.dialect
	db := r.store.exec(ctx)
	rows, err := r.store.query(ctx, db,
		`SELECT DISTINCT run_id FROM run_leases WHERE runner_id=? AND released_at IS NULL ORDER BY run_id`, runnerID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	var runIDs []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return nil, r.store.mapErr(err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Close(); err != nil {
		return nil, r.store.mapErr(err)
	}
	if _, err := r.store.execStmt(ctx, db,
		`UPDATE run_leases SET released_at=? WHERE runner_id=? AND released_at IS NULL`, d.TimeParam(at), runnerID); err != nil {
		return nil, r.store.mapErr(err)
	}
	return runIDs, nil
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

// RenewLeasesByRunnerIfEpoch v2：仅当 runners.connection_epoch 与传入一致才续租
// （旧连接的 heartbeat 不得续租——hello 换代后旧 epoch 的帧只配 ACK，不生效）。
// epoch 失配返回 0 行 nil 错误。
func (r *RunnerRepo) RenewLeasesByRunnerIfEpoch(ctx context.Context, runnerID, epoch, bootID string, renewUntil time.Time) (int, error) {
	var currentEpoch, currentBoot *string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT connection_epoch, boot_id FROM runners WHERE id=?`, runnerID).Scan(&currentEpoch, &currentBoot); err != nil {
		return 0, r.store.mapErr(err)
	}
	if currentEpoch == nil || currentBoot == nil || *currentEpoch != epoch || *currentBoot != bootID {
		return 0, nil
	}
	return r.RenewLeasesByRunner(ctx, runnerID, renewUntil)
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

// RunnerEventDedupV2 按 (run_id, lease_id, runner_id, producer_seq) 去重（0023 表，
// RFC §8.3）；event_id 随行记录供 ACK 对账。重复返回 ErrIdempotencyConflict。
// 必须在 ApplyRunnerEvent 同事务内调用（dedup 与应用效果同生共死）。
func (r *RunnerRepo) RunnerEventDedupV2(ctx context.Context, runID, leaseID, runnerID string, producerSeq int64, eventID string) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO runner_event_dedup(run_id, lease_id, runner_id, producer_seq, event_id, run_seq)
		 VALUES (?,?,?,?,?,0)`,
		runID, leaseID, runnerID, producerSeq, eventID)
	return r.store.mapErr(err)
}
