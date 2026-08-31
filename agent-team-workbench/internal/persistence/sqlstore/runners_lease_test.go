package sqlstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestCreateLeaseAllowsOneActiveFenceAndMonotonicReissue(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_fence", WorkspaceID: "ws_wk", Name: "Fence", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)
	insertWorkItem(t, db, "wi_fence")
	now := time.Now().UTC()
	insertRun(t, db, "run_fence", agent.ID, "wi_fence", "running", now)
	if err := store.Runners().Upsert(ctx, &application.Runner{
		ID: "runner_fence", Label: "fence",
		Slots: 8, Status: "connected", LastSeenAt: &now,
	}); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	leases := make([]*application.RunLease, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		leases[i] = &application.RunLease{
			LeaseID: fmt.Sprintf("lease_fence_%d", i), RunID: "run_fence", RunnerID: "runner_fence",
			RenewedUntil: now.Add(time.Minute),
		}
		wg.Add(1)
		go func(lease *application.RunLease) {
			defer wg.Done()
			errs <- store.Runners().CreateLease(context.Background(), lease)
		}(leases[i])
	}
	wg.Wait()
	close(errs)
	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发抢同一 Run 只能产生一个 active lease，实际 %d", succeeded)
	}
	active, err := store.Runners().ListActiveLeasesByRunner(ctx, "runner_fence")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].FencingToken != 1 {
		t.Fatalf("恢复只应返回唯一 active fence: %+v", active)
	}
	if err := store.Runners().ReleaseLease(ctx, active[0].LeaseID, now); err != nil {
		t.Fatal(err)
	}
	next := &application.RunLease{LeaseID: "lease_fence_next", RunID: "run_fence", RunnerID: "runner_fence", RenewedUntil: now.Add(time.Minute)}
	if err := store.Runners().CreateLease(ctx, next); err != nil {
		t.Fatal(err)
	}
	if next.FencingToken != 2 {
		t.Fatalf("active lease 释放后 fencing 必须单调递增，实际 %d", next.FencingToken)
	}
}

// insertLeaseForRunner 与 insertLease 相同，但允许指定 runner（验证续租按 runner 维度隔离）。
func insertLeaseForRunner(t *testing.T, db *sql.DB, leaseID, runID, runnerID string, renewedUntil time.Time, released bool) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO runners(id, workspace_id, label, slots, status, created_at)
		 VALUES (?,'ws_wk','wk',1,'connected',?)`, runnerID, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var releasedAt any
	if released {
		releasedAt = now.Format(time.RFC3339Nano)
	}
	if _, err := db.Exec(
		`INSERT INTO run_leases(lease_id, run_id, runner_id, fencing_token, acquired_at, renewed_until, released_at)
		 VALUES (?,?,?,?,?,?,?)`,
		leaseID, runID, runnerID, 1,
		now.Format(time.RFC3339Nano), renewedUntil.UTC().Format(time.RFC3339Nano), releasedAt); err != nil {
		t.Fatal(err)
	}
}

// leaseTimes 回读 renewed_until / released_at（SQLite 方言存 RFC3339 文本）。
func leaseTimes(t *testing.T, db *sql.DB, leaseID string) (renewedUntil time.Time, released bool) {
	t.Helper()
	var renewed, releasedAt sql.NullString
	if err := db.QueryRow(
		`SELECT renewed_until, released_at FROM run_leases WHERE lease_id=?`, leaseID).
		Scan(&renewed, &releasedAt); err != nil {
		t.Fatal(err)
	}
	rt, err := time.Parse(time.RFC3339Nano, renewed.String)
	if err != nil {
		t.Fatal(err)
	}
	return rt, releasedAt.Valid
}

// ---- RenewLeasesByRunner 语义 ----

// 续租只作用于：该 runner 持有 + released_at IS NULL + run 非终态 的 lease；
// 终态 run 的残留 lease 被顺手回收；已释放 / 其他 runner 的 lease 不受影响。
func TestRenewLeasesByRunner(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_renew", WorkspaceID: "ws_wk", Name: "RN", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)
	insertWorkItem(t, db, "wi_renew")

	past := time.Now().UTC().Add(-time.Hour)
	renewTo := time.Now().UTC().Add(60 * time.Second)

	// runner_wk 名下 5 种形态：
	insertRun(t, db, "run_active", agent.ID, "wi_renew", "running", past)
	insertLeaseForRunner(t, db, "lease_active", "run_active", "runner_wk", past, false) // 应续
	insertRun(t, db, "run_wait", agent.ID, "wi_renew", "waiting_approval", past)
	insertLeaseForRunner(t, db, "lease_wait", "run_wait", "runner_wk", past, false) // 应续
	insertRun(t, db, "run_reconn", agent.ID, "wi_renew", "reconnecting", past)
	insertLeaseForRunner(t, db, "lease_reconn", "run_reconn", "runner_wk", past, false) // 应续
	insertRun(t, db, "run_term", agent.ID, "wi_renew", "succeeded", past)
	insertLeaseForRunner(t, db, "lease_term", "run_term", "runner_wk", past, false) // 终态：不续、回收
	insertRun(t, db, "run_rel", agent.ID, "wi_renew", "running", past)
	insertLeaseForRunner(t, db, "lease_rel", "run_rel", "runner_wk", past, true) // 已释放：不动
	// 其他 runner：不动。
	insertRun(t, db, "run_other", agent.ID, "wi_renew", "running", past)
	insertLeaseForRunner(t, db, "lease_other", "run_other", "runner_other", past, false)

	n, err := store.Runners().RenewLeasesByRunner(ctx, "runner_wk", renewTo)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("续租行数 = %d，期望 3（active/wait/reconn）", n)
	}

	for _, id := range []string{"lease_active", "lease_wait", "lease_reconn"} {
		renewed, released := leaseTimes(t, db, id)
		if !renewed.Equal(renewTo) {
			t.Fatalf("%s renewed_until = %v，期望推进到 %v", id, renewed, renewTo)
		}
		if released {
			t.Fatalf("%s 不应被释放", id)
		}
	}
	// 终态 run：renewed_until 保持原值，released_at 置位。
	if renewed, _ := leaseTimes(t, db, "lease_term"); !renewed.Equal(past) {
		t.Fatalf("lease_term renewed_until = %v，期望保持 %v", renewed, past)
	}
	if _, released := leaseTimes(t, db, "lease_term"); !released {
		t.Fatal("终态 run 的残留 lease 应被回收（released_at 置位）")
	}
	// 已释放：renewed_until 保持。
	if renewed, _ := leaseTimes(t, db, "lease_rel"); !renewed.Equal(past) {
		t.Fatalf("lease_rel renewed_until = %v，期望保持 %v", renewed, past)
	}
	// 其他 runner：不动。
	if renewed, released := leaseTimes(t, db, "lease_other"); !renewed.Equal(past) || released {
		t.Fatalf("lease_other 不应被 runner_wk 的续租触碰: renewed=%v released=%v", renewed, released)
	}

	// 幂等：重复续租结果一致。
	n2, err := store.Runners().RenewLeasesByRunner(ctx, "runner_wk", renewTo)
	if err != nil || n2 != 3 {
		t.Fatalf("重复续租: n=%d err=%v，期望 n=3", n2, err)
	}

	// 不存在的 runner：无错误、0 行。
	n3, err := store.Runners().RenewLeasesByRunner(ctx, "runner_nope", renewTo)
	if err != nil || n3 != 0 {
		t.Fatalf("未知 runner: n=%d err=%v", n3, err)
	}
}

// ---- 续租与 sweeper / zombie 判定的联动 ----

// 续租后的 lease 不再被 ExpireLeases 释放；未续的过期 lease 仍被释放（行为不回归）；
// 续租把「已过期 lease + 非终态 run」从 zombie 恢复为 alive。
func TestRenewLeasesSweeperAndZombieInteraction(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_zb", WorkspaceID: "ws_wk", Name: "ZB", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)
	insertWorkItem(t, db, "wi_zb")

	past := time.Now().UTC().Add(-time.Hour)
	now := time.Now().UTC()

	insertRun(t, db, "run_keep", agent.ID, "wi_zb", "running", past)
	insertLeaseForRunner(t, db, "lease_keep", "run_keep", "runner_wk", past, false)

	// 续租前：lease 已过期 → zombie（可穿透创建新 run → 同任务并发双跑）。
	runID, alive, err := store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_zb")
	if err != nil || runID != "run_keep" || alive {
		t.Fatalf("续租前应判 zombie: id=%q alive=%v err=%v", runID, alive, err)
	}

	// 心跳续租（renewed_until 推进到未来）。
	if n, err := store.Runners().RenewLeasesByRunner(ctx, "runner_wk", now.Add(60*time.Second)); err != nil || n != 1 {
		t.Fatalf("续租: n=%d err=%v", n, err)
	}

	// 续租后：alive（不再被 scheduling 穿透）。
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_zb")
	if err != nil || runID != "run_keep" || !alive {
		t.Fatalf("续租后应判 alive: id=%q alive=%v err=%v", runID, alive, err)
	}

	// sweeper：续租过的 lease 不被释放。
	if ids, err := store.Runners().ExpireLeases(ctx, now); err != nil {
		t.Fatal(err)
	} else {
		for _, id := range ids {
			if id == "run_keep" {
				t.Fatalf("已续租的 run_keep 不应被 ExpireLeases 释放: %v", ids)
			}
		}
	}
	if _, released := leaseTimes(t, db, "lease_keep"); released {
		t.Fatal("lease_keep 不应被释放")
	}

	// 未续的过期 lease（其他 runner）：sweeper 释放行为不回归。
	insertRun(t, db, "run_dead", agent.ID, "wi_zb", "running", past.Add(time.Minute))
	insertLeaseForRunner(t, db, "lease_dead", "run_dead", "runner_other", past, false)
	ids, err := store.Runners().ExpireLeases(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, id := range ids {
		if id == "run_dead" {
			found = true
		}
	}
	if !found {
		t.Fatalf("过期未续的 run_dead 应被 ExpireLeases 释放: %v", ids)
	}
	if _, released := leaseTimes(t, db, "lease_dead"); !released {
		t.Fatal("lease_dead 应被置 released_at")
	}
}
