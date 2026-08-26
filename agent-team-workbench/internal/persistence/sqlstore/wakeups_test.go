package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// openWakeupTestDB 临时文件 sqlite + 全量迁移（migtest 动态发现 migrations/sqlite，
// 新增迁移免同步清单）。并发写用例（MarkWakeupStatus CAS）对齐生产 sqlite DSN：
// busy_timeout + 单连接写串行。
func openWakeupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedWorkspace(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at) VALUES ('ws_wk','wk','UTC',1,?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
}

func seedAgent(t *testing.T, store *sqlstore.Store, a *domain.AgentProfile) {
	t.Helper()
	if err := store.Agents().Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func insertWorkItem(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO work_items(id, workspace_id, title, status, priority, version, created_at, updated_at)
		 VALUES (?,'ws_wk','t','todo','medium',1,?,?)`, id, now, now); err != nil {
		t.Fatal(err)
	}
}

func insertRun(t *testing.T, db *sql.DB, id, agentID, workItemID, status string, createdAt time.Time) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO execution_runs(id, workspace_id, work_item_id, agent_profile_id, status, version, created_at, updated_at)
		 VALUES (?,'ws_wk',?,?,?,1,?,?)`,
		id, workItemID, agentID, status,
		createdAt.UTC().Format(time.RFC3339Nano), createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func insertLease(t *testing.T, db *sql.DB, leaseID, runID string, renewedUntil time.Time, released bool) {
	t.Helper()
	now := time.Now().UTC()
	var runnerID = "runner_wk"
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

// ---- ClaimHeartbeat ----

func TestClaimHeartbeatIntervalGating(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_hb", WorkspaceID: "ws_wk", Name: "HB", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)

	now := time.Now().UTC()
	// NULL last_heartbeat_at → 首次 claim 命中。
	ok, err := store.Wakeups().ClaimHeartbeat(ctx, agent.ID, 30*time.Minute, now)
	if err != nil || !ok {
		t.Fatalf("首次 claim: ok=%v err=%v", ok, err)
	}
	// 间隔内第二次 → 不命中。
	ok, err = store.Wakeups().ClaimHeartbeat(ctx, agent.ID, 30*time.Minute, now.Add(10*time.Minute))
	if err != nil || ok {
		t.Fatalf("间隔内应不命中: ok=%v err=%v", ok, err)
	}
	// last_heartbeat_at 已持久化。
	got, err := store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHeartbeatAt == nil || !got.LastHeartbeatAt.Equal(now) {
		t.Fatalf("last_heartbeat_at 未落库: %v", got.LastHeartbeatAt)
	}
	// 间隔过后 → 命中，且时间推进。
	later := now.Add(31 * time.Minute)
	ok, err = store.Wakeups().ClaimHeartbeat(ctx, agent.ID, 30*time.Minute, later)
	if err != nil || !ok {
		t.Fatalf("间隔过后应命中: ok=%v err=%v", ok, err)
	}
	got, err = store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHeartbeatAt == nil || !got.LastHeartbeatAt.Equal(later) {
		t.Fatalf("last_heartbeat_at 未更新: %v", got.LastHeartbeatAt)
	}
	// 不存在的 agent → 不命中、不报错。
	ok, err = store.Wakeups().ClaimHeartbeat(ctx, "agent_nope", time.Minute, later)
	if err != nil || ok {
		t.Fatalf("未知 agent: ok=%v err=%v", ok, err)
	}
}

// ---- ActiveRunKeyForAgentTask 三态 ----

func TestActiveRunKeyForAgentTaskStates(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_ar", WorkspaceID: "ws_wk", Name: "AR", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)
	insertWorkItem(t, db, "wi_ar")

	base := time.Now().UTC().Add(-time.Hour)
	// 状态 1：无 run。
	runID, alive, err := store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_ar")
	if err != nil || runID != "" || alive {
		t.Fatalf("无 run: id=%q alive=%v err=%v", runID, alive, err)
	}

	// 状态 2：活 run + 活 lease → alive=true。
	insertRun(t, db, "run_live", agent.ID, "wi_ar", "running", base)
	insertLease(t, db, "lease_live", "run_live", time.Now().UTC().Add(time.Hour), false)
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_ar")
	if err != nil || runID != "run_live" || !alive {
		t.Fatalf("活 run+活 lease: id=%q alive=%v err=%v", runID, alive, err)
	}

	// 状态 3a：活 run + 死 lease（renewed_until 已过）→ zombie。
	insertRun(t, db, "run_dead", agent.ID, "wi_ar", "running", base.Add(time.Minute))
	insertLease(t, db, "lease_dead", "run_dead", time.Now().UTC().Add(-time.Minute), false)
	// run_dead 更新近，应被选中。
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_ar")
	if err != nil || runID != "run_dead" || alive {
		t.Fatalf("zombie: id=%q alive=%v err=%v", runID, alive, err)
	}

	// 状态 3b：活 run + 已释放 lease（renewed_until 未来但 released_at 非空）→ zombie。
	insertRun(t, db, "run_rel", agent.ID, "wi_ar", "waiting_approval", base.Add(2*time.Minute))
	insertLease(t, db, "lease_rel", "run_rel", time.Now().UTC().Add(time.Hour), true)
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_ar")
	if err != nil || runID != "run_rel" || alive {
		t.Fatalf("已释放 lease: id=%q alive=%v err=%v", runID, alive, err)
	}

	// 状态 4：进程内 run（非终态、无任何 lease 行）→ alive（control-plane 自身执行，随进程生死）。
	insertRun(t, db, "run_inproc", agent.ID, "wi_ar", "running", base.Add(2*time.Minute+30*time.Second))
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_ar")
	if err != nil || runID != "run_inproc" || !alive {
		t.Fatalf("进程内 run 应视为 alive: id=%q alive=%v err=%v", runID, alive, err)
	}

	// 终态 run 不算活动：只剩终态时返回无 run（先清 leases 再删 runs，避免 FK 约束）。
	insertRun(t, db, "run_done", agent.ID, "wi_ar", "succeeded", base.Add(3*time.Minute))
	if _, err := db.Exec(`DELETE FROM run_leases`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM execution_runs WHERE id != 'run_done'`); err != nil {
		t.Fatal(err)
	}
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_ar")
	if err != nil || runID != "" || alive {
		t.Fatalf("终态 run: id=%q alive=%v err=%v", runID, alive, err)
	}

	// 其他 task_key 不受影响。
	insertWorkItem(t, db, "wi_other")
	insertRun(t, db, "run_other", agent.ID, "wi_other", "running", base.Add(4*time.Minute))
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_ar")
	if err != nil || runID != "" || alive {
		t.Fatalf("task_key 隔离: id=%q alive=%v err=%v", runID, alive, err)
	}
}

// ---- Enqueue + DueTimers + MarkStatus + RecentByAgentTask 往返 ----

func TestWakeupEnqueueDueMarkRoundtrip(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_rq", WorkspaceID: "ws_wk", Name: "RQ", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)

	// 同时验证 sqlstore.Wakeups() 直接满足 scheduling.Store。
	var schedStore scheduling.Store = store.Wakeups()
	_ = schedStore

	now := time.Now().UTC()
	mk := func(id, source, taskKey string, wakeAt, createdAt time.Time, ctxMap map[string]any) *domain.WakeupRequest {
		return &domain.WakeupRequest{
			ID: id, WorkspaceID: "ws_wk", AgentProfileID: agent.ID,
			Source: domain.WakeupSource(source), TaskKey: taskKey,
			Context: ctxMap, Status: domain.WakeupStatusQueued,
			WakeAt: wakeAt, CreatedAt: createdAt, UpdatedAt: createdAt,
		}
	}
	requests := []*domain.WakeupRequest{
		mk("wkup_1", "timer", "wi_1", now.Add(-2*time.Minute), now.Add(-2*time.Minute), map[string]any{"instruction": "x", "n": 3}),
		mk("wkup_2", "timer", "wi_1", now.Add(-1*time.Minute), now.Add(-70*time.Second), nil),
		mk("wkup_3", "timer", "wi_1", now.Add(5*time.Minute), now.Add(-time.Minute), nil),
		mk("wkup_4", "assignment", "wi_1", now.Add(-50*time.Second), now.Add(-30*time.Second), nil),
		mk("wkup_5", "timer", "wi_2", now.Add(-3*time.Minute), now.Add(-3*time.Minute), nil),
	}
	for _, w := range requests {
		if err := store.Wakeups().EnqueueWakeup(ctx, w); err != nil {
			t.Fatalf("EnqueueWakeup %s: %v", w.ID, err)
		}
	}

	// DueTimers：取全部来源的到期请求（timer/assignment/on_demand 同循环消费），
	// 按 wake_at 升序；排除未到期。
	due, err := store.Wakeups().DueTimers(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, w := range due {
		ids = append(ids, w.ID)
	}
	if len(ids) != 4 || ids[0] != "wkup_5" || ids[1] != "wkup_1" || ids[2] != "wkup_2" || ids[3] != "wkup_4" {
		t.Fatalf("DueTimers 顺序/内容错误: %v", ids)
	}
	// context 往返（含非 string 值）。
	if due[1].Context["instruction"] != "x" || due[1].Context["n"].(float64) != 3 {
		t.Fatalf("context 往返失败: %#v", due[1].Context)
	}
	// nil context → 空 map 而非 NULL 报错。
	if due[2].Context == nil {
		t.Fatal("nil context 应回读为空 map")
	}

	// MarkStatus：coalesced 后不再出现在 DueTimers。
	if err := store.Wakeups().MarkWakeupStatus(ctx, "wkup_1", domain.WakeupStatusCoalesced); err != nil {
		t.Fatal(err)
	}
	due, err = store.Wakeups().DueTimers(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range due {
		if w.ID == "wkup_1" {
			t.Fatalf("coalesced 后不应再被取出: %v", w.ID)
		}
	}
	// consumed 同理。
	if err := store.Wakeups().MarkWakeupStatus(ctx, "wkup_5", domain.WakeupStatusConsumed); err != nil {
		t.Fatal(err)
	}
	due, err = store.Wakeups().DueTimers(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 || due[0].ID != "wkup_2" || due[1].ID != "wkup_4" {
		t.Fatalf("consumed 后剩余应为 wkup_2/wkup_4: %v", due)
	}

	// RecentByAgentTask：按 (agent, task_key) 过滤 + since 时间下界，倒序。
	recent, err := store.Wakeups().RecentByAgentTask(ctx, agent.ID, "wi_1", now.Add(-90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 { // wkup_4、wkup_2、wkup_3（wkup_1 早于 since 被排除）
		t.Fatalf("recent 数量 = %d", len(recent))
	}
	if recent[0].ID != "wkup_4" || recent[0].Status != domain.WakeupStatusQueued {
		t.Fatalf("recent 排序/状态错误: %#v", recent[0])
	}
	// created_at 倒序：wkup_3(-60s) 新于 wkup_2(-70s)。
	if recent[1].ID != "wkup_3" || recent[2].ID != "wkup_2" {
		t.Fatalf("recent 顺序错误: %v,%v", recent[1].ID, recent[2].ID)
	}
}

// ---- F3：reconnecting / succeeding 过渡态也算活动 run ----

// TestActiveRunKeyTreatsReconnectingSucceedingActive：断线重连窗口（reconnecting）
// 与成功收尾窗口（succeeding）不得被判「无活跃 run」——有活 lease 时 alive，
// 进程内（无 lease）时 alive，只有死 lease 的 reconnecting 才是可穿透 zombie。
func TestActiveRunKeyTreatsReconnectingSucceedingActive(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_rs", WorkspaceID: "ws_wk", Name: "RS", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)
	insertWorkItem(t, db, "wi_rs")
	base := time.Now().UTC().Add(-time.Hour)

	// reconnecting + 活 lease（runner 断连后重连中）→ alive。
	insertRun(t, db, "run_rc_live", agent.ID, "wi_rs", "reconnecting", base)
	insertLease(t, db, "lease_rc_live", "run_rc_live", time.Now().UTC().Add(time.Hour), false)
	runID, alive, err := store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_rs")
	if err != nil || runID != "run_rc_live" || !alive {
		t.Fatalf("reconnecting+活 lease: id=%q alive=%v err=%v", runID, alive, err)
	}

	// succeeding + 进程内（无 lease）→ alive（收尾窗口不得穿透双跑）。
	insertRun(t, db, "run_sc_inproc", agent.ID, "wi_rs", "succeeding", base.Add(time.Minute))
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_rs")
	if err != nil || runID != "run_sc_inproc" || !alive {
		t.Fatalf("succeeding 进程内: id=%q alive=%v err=%v", runID, alive, err)
	}

	// reconnecting + 死 lease → zombie（sweeper 会判 lost，唤醒可穿透重建）。
	if _, err := db.Exec(`DELETE FROM run_leases`); err != nil {
		t.Fatal(err)
	}
	insertRun(t, db, "run_rc_dead", agent.ID, "wi_rs", "reconnecting", base.Add(2*time.Minute))
	insertLease(t, db, "lease_rc_dead", "run_rc_dead", time.Now().UTC().Add(-time.Minute), false)
	runID, alive, err = store.Wakeups().ActiveRunKeyForAgentTask(ctx, agent.ID, "wi_rs")
	if err != nil || runID != "run_rc_dead" || alive {
		t.Fatalf("reconnecting+死 lease: id=%q alive=%v err=%v", runID, alive, err)
	}
}

// ---- F4：MarkWakeupStatus CAS + RequeueWakeup 补偿 ----

// TestMarkWakeupStatusConcurrentSingleWinner：并发两个消费者 CAS 同一唤醒，
// 只有一个占住（视为建 run 成功），另一个拿到 ErrWakeupNotQueued 不建 run。
func TestMarkWakeupStatusConcurrentSingleWinner(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_cas", WorkspaceID: "ws_wk", Name: "CAS", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)
	now := time.Now().UTC()
	w := &domain.WakeupRequest{
		ID: "wkup_cas", WorkspaceID: "ws_wk", AgentProfileID: agent.ID,
		Source: domain.WakeupSourceTimer, TaskKey: "wi_1", Status: domain.WakeupStatusQueued,
		WakeAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Wakeups().EnqueueWakeup(ctx, w); err != nil {
		t.Fatal(err)
	}

	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Wakeups().MarkWakeupStatus(ctx, "wkup_cas", domain.WakeupStatusConsumed); err == nil {
				created.Add(1) // 占住者才允许建 run
			} else if !errors.Is(err, domain.ErrWakeupNotQueued) {
				t.Errorf("CAS 失败应返回 ErrWakeupNotQueued, got %v", err)
			}
		}()
	}
	wg.Wait()
	if got := created.Load(); got != 1 {
		t.Fatalf("并发消费建 run 计数 = %d, 期望 1", got)
	}
	got, err := store.Wakeups().DueTimers(ctx, now.Add(time.Minute), 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("consumed 后不应再被取出: %v %v", got, err)
	}

	// RequeueWakeup 补偿：consumed → queued；非 consumed 出发返回 ErrWakeupNotQueued。
	if err := store.Wakeups().RequeueWakeup(ctx, "wkup_cas"); err != nil {
		t.Fatalf("RequeueWakeup: %v", err)
	}
	if err := store.Wakeups().RequeueWakeup(ctx, "wkup_cas"); !errors.Is(err, domain.ErrWakeupNotQueued) {
		t.Fatalf("重复 requeue 应 CAS 失败: %v", err)
	}
	due, err := store.Wakeups().DueTimers(ctx, now.Add(time.Minute), 10)
	if err != nil || len(due) != 1 || due[0].ID != "wkup_cas" {
		t.Fatalf("requeue 后应重新可见: %v %v", due, err)
	}
}

// TestSetWakeupContextRoundtrip：coalescing 降级审计的 context 覆写往返。
func TestSetWakeupContextRoundtrip(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_ctx", WorkspaceID: "ws_wk", Name: "CTX", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)
	now := time.Now().UTC()
	w := &domain.WakeupRequest{
		ID: "wkup_ctx", WorkspaceID: "ws_wk", AgentProfileID: agent.ID,
		Source: domain.WakeupSourceOnDemand, TaskKey: "wi_1",
		Context: map[string]any{"instruction": "追加指令"}, Status: domain.WakeupStatusQueued,
		WakeAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Wakeups().EnqueueWakeup(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := store.Wakeups().SetWakeupContext(ctx, "wkup_ctx",
		map[string]any{"instruction": "追加指令", "coalesced_instruction": "追加指令"}); err != nil {
		t.Fatal(err)
	}
	recent, err := store.Wakeups().RecentByAgentTask(ctx, agent.ID, "wi_1", now.Add(-time.Minute))
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent: %v %v", recent, err)
	}
	if recent[0].Context["coalesced_instruction"] != "追加指令" {
		t.Fatalf("context 覆写往返失败: %#v", recent[0].Context)
	}
}

// ---- F5：ReleaseHeartbeatClaim ----

// TestReleaseHeartbeatClaim：回滚仅当 last_heartbeat_at 仍是本次 claim 写入值；
// 复位后下一次 claim 立即可命中（不白等一个心跳周期），不覆盖别人的新 claim。
func TestReleaseHeartbeatClaim(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	agent := &domain.AgentProfile{
		ID: "agent_rel", WorkspaceID: "ws_wk", Name: "REL", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedAgent(t, store, agent)

	t1 := time.Now().UTC()
	if ok, err := store.Wakeups().ClaimHeartbeat(ctx, agent.ID, 30*time.Minute, t1); err != nil || !ok {
		t.Fatalf("首次 claim: %v %v", ok, err)
	}
	// 回滚本次 claim → last_heartbeat_at 复位 → 同刻可重新 claim。
	if err := store.Wakeups().ReleaseHeartbeatClaim(ctx, agent.ID, t1); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Wakeups().ClaimHeartbeat(ctx, agent.ID, 30*time.Minute, t1); err != nil || !ok {
		t.Fatalf("回滚后应可重新 claim: %v %v", ok, err)
	}
	// 过期值回滚（last 已被新 claim 覆盖）→ 不生效，保护别人的新 claim。
	t2 := t1.Add(31 * time.Minute)
	if ok, err := store.Wakeups().ClaimHeartbeat(ctx, agent.ID, 30*time.Minute, t2); err != nil || !ok {
		t.Fatalf("间隔过后应命中: %v %v", ok, err)
	}
	if err := store.Wakeups().ReleaseHeartbeatClaim(ctx, agent.ID, t1); err != nil {
		t.Fatal(err)
	}
	got, err := store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastHeartbeatAt == nil || !got.LastHeartbeatAt.Equal(t2) {
		t.Fatalf("旧值回滚不应覆盖新 claim: %v", got.LastHeartbeatAt)
	}
}

// ---- F1：timer 生产查询面 ----

func insertAssignedWorkItem(t *testing.T, db *sql.DB, id, agentID, title, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO work_items(id, workspace_id, title, status, priority, agent_profile_id, version, created_at, updated_at)
		 VALUES (?,'ws_wk',?,?,'medium',?,1,?,?)`, id, title, status, agentID, now, now); err != nil {
		t.Fatal(err)
	}
}

// TestWakeupProducerQueries：ListHeartbeatEnabled / AssignedTasks / HasQueuedTimer
// 的过滤语义——只有开启心跳且可调度的 agent 是候选；任务只取非终态；
// 幂等判定只认 queued 的 timer 唤醒。
func TestWakeupProducerQueries(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	mk := func(id string, heartbeat bool, availability domain.AgentAvailability) *domain.AgentProfile {
		return &domain.AgentProfile{
			ID: id, WorkspaceID: "ws_wk", Name: id, Role: "developer",
			Availability: availability, Presence: domain.PresenceIdle, Version: 1,
			HeartbeatEnabled: heartbeat, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
	}
	seedAgent(t, store, mk("agent_hb", true, domain.AgentEnabled))
	seedAgent(t, store, mk("agent_nohb", false, domain.AgentEnabled))
	seedAgent(t, store, mk("agent_off", true, domain.AgentDisabled))

	agents, err := store.Agents().ListHeartbeatEnabled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != "agent_hb" {
		t.Fatalf("ListHeartbeatEnabled: %#v", agents)
	}

	insertAssignedWorkItem(t, db, "wi_todo", "agent_hb", "待办", "todo")
	insertAssignedWorkItem(t, db, "wi_prog", "agent_hb", "进行中", "in_progress")
	insertAssignedWorkItem(t, db, "wi_block", "agent_hb", "阻塞", "blocked")
	insertAssignedWorkItem(t, db, "wi_done", "agent_hb", "已完成", "completed")
	insertAssignedWorkItem(t, db, "wi_other", "agent_nohb", "别人的", "todo")

	tasks, err := store.Wakeups().AssignedTasks(ctx, "agent_hb")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("非终态任务数 = %d: %#v", len(tasks), tasks)
	}
	for _, task := range tasks {
		if task.Title == "" {
			t.Fatalf("任务应带 title: %#v", task)
		}
	}

	now := time.Now().UTC()
	mkWakeup := func(id string, status domain.WakeupStatus) *domain.WakeupRequest {
		return &domain.WakeupRequest{
			ID: id, WorkspaceID: "ws_wk", AgentProfileID: "agent_hb",
			Source: domain.WakeupSourceTimer, TaskKey: "wi_todo", Status: status,
			WakeAt: now, CreatedAt: now, UpdatedAt: now,
		}
	}
	if err := store.Wakeups().EnqueueWakeup(ctx, mkWakeup("wkup_p1", domain.WakeupStatusQueued)); err != nil {
		t.Fatal(err)
	}
	if err := store.Wakeups().EnqueueWakeup(ctx, mkWakeup("wkup_p2", domain.WakeupStatusConsumed)); err != nil {
		t.Fatal(err)
	}
	queued, err := store.Wakeups().HasQueuedTimer(ctx, "agent_hb", "wi_todo")
	if err != nil || !queued {
		t.Fatalf("HasQueuedTimer(queued 存在): %v %v", queued, err)
	}
	queued, err = store.Wakeups().HasQueuedTimer(ctx, "agent_hb", "wi_prog")
	if err != nil || queued {
		t.Fatalf("HasQueuedTimer(无记录): %v %v", queued, err)
	}
}

// ---- agents.go 新列持久化往返 + 迁移缺省值 ----

func TestAgentProfileWakeupColumnsRoundtrip(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)

	lastHB := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	agent := &domain.AgentProfile{
		ID: "agent_cols", WorkspaceID: "ws_wk", Name: "COLS", Role: "reviewer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		HeartbeatEnabled:     true,
		HeartbeatIntervalSec: 600,
		WakeOnAssignment:     true,
		WakeOnDemand:         false,
		WakeOnAutomation:     true,
		PromptTemplate:       "去 {{work_item.title}} 看看",
		LastHeartbeatAt:      &lastHB,
	}
	seedAgent(t, store, agent)

	got, err := store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HeartbeatEnabled || got.HeartbeatIntervalSec != 600 ||
		!got.WakeOnAssignment || got.WakeOnDemand || !got.WakeOnAutomation ||
		got.PromptTemplate != "去 {{work_item.title}} 看看" {
		t.Fatalf("Get 新列往返失败: %#v", got)
	}
	if got.LastHeartbeatAt == nil || !got.LastHeartbeatAt.Equal(lastHB) {
		t.Fatalf("LastHeartbeatAt 往返失败: %v", got.LastHeartbeatAt)
	}

	// List 同样带出新列。
	listed, err := store.Agents().List(ctx, "ws_wk")
	if err != nil || len(listed) != 1 {
		t.Fatalf("List: %v %d", err, len(listed))
	}
	if !listed[0].HeartbeatEnabled || listed[0].WakeOnDemand {
		t.Fatalf("List 新列缺失: %#v", listed[0])
	}

	// Heartbeat() 投影。
	p := got.Heartbeat()
	if !p.Enabled || p.IntervalSec != 600 || !p.WakeOnAssignment || p.WakeOnDemand || !p.WakeOnAutomation ||
		p.PromptTemplate != "去 {{work_item.title}} 看看" {
		t.Fatalf("Heartbeat() 投影错误: %#v", p)
	}

	// Update 持久化新列；last_heartbeat_at 不被 Update 触碰（归 ClaimHeartbeat 维护）。
	claimed, err := store.Wakeups().ClaimHeartbeat(ctx, agent.ID, time.Minute, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	agent.HeartbeatEnabled = false
	agent.HeartbeatIntervalSec = 300
	agent.WakeOnDemand = true
	agent.PromptTemplate = "t2"
	if err := store.Agents().Update(ctx, agent, 1); err != nil {
		t.Fatal(err)
	}
	got, err = store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeartbeatEnabled || got.HeartbeatIntervalSec != 300 || !got.WakeOnDemand || got.PromptTemplate != "t2" {
		t.Fatalf("Update 新列失败: %#v", got)
	}
	if got.LastHeartbeatAt == nil {
		t.Fatal("Update 不应清空 last_heartbeat_at")
	}
	if got.Version != 2 {
		t.Fatalf("乐观锁 version = %d, 期望 2", got.Version)
	}

	// 迁移缺省值：绕过 Go 写入的行应带列缺省（0/0/1/1/0/''/NULL）。
	// 注：sqlite 版 0001 的 created_at 无 DEFAULT（postgres 有 now()），需显式提供。
	defNow := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO agent_profiles(id, workspace_id, name, role, created_at, updated_at)
		 VALUES ('agent_def','ws_wk','DEF','dev',?,?)`, defNow, defNow); err != nil {
		t.Fatal(err)
	}
	def, err := store.Agents().Get(ctx, "agent_def")
	if err != nil {
		t.Fatal(err)
	}
	if def.HeartbeatEnabled || def.HeartbeatIntervalSec != 0 ||
		!def.WakeOnAssignment || !def.WakeOnDemand || def.WakeOnAutomation ||
		def.PromptTemplate != "" || def.LastHeartbeatAt != nil {
		t.Fatalf("迁移缺省值错误: %#v", def)
	}
}
