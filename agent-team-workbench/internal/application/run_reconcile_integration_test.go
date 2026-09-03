package application_test

// 控制面重启对账 sweeper（Run Journal M2 闭合与证据，run_reconcile.go）的
// sqlite 集成回归：
//   - starting/running（无 lease）→ 扫后 lost，recovery/decision 事件带证据，
//     未闭合相位被合成闭合（failure.code=control_plane_restart）；
//   - queued 不扫（合法待派发）；
//   - 带未释放 lease 的远程 run 不动（归 runnergateway.leaseSweeper）；
//   - 重复调用幂等（与 leaseSweeper 撞车的防双扫证明）。
//
// 断言一律经 store.Events().ListRunEventsIncludeInternal（internal 类事件只落
// run_events，surface 查询看不到）。

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

type reconcileRig struct {
	svc     *application.Service
	store   application.Store
	wsID    string
	wiID    string
	agentID string
}

func newReconcileRig(t *testing.T, wsID string) *reconcileRig {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	if err := store.Workspaces().Create(bg(), &domain.Workspace{
		ID: wsID, Name: wsID, Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, bg(), wsID)
	agent := &domain.AgentProfile{
		ID: "agent_reconcile_" + wsID, WorkspaceID: wsID, Name: "Reconciler", Role: "developer",
		Instructions: "", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(bg(), agent); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(bg(), wsID, application.CreateWorkItemParams{
		Title: "重启对账", AgentProfileID: agent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &reconcileRig{svc: svc, store: store, wsID: wsID, wiID: wi.ID, agentID: agent.ID}
}

// bg 是 context.Background() 的 shorthand（测试内全部调用共享同一背景上下文）。
func bg() context.Context { return context.Background() }

// seedRunAt 把一个新 run 直接推到指定非终态（queued 直接返回）。
func (r *reconcileRig) seedRunAt(t *testing.T, instruction string, status domain.RunStatus) *domain.ExecutionRun {
	t.Helper()
	run, err := r.svc.CreateRun(bg(), r.wiID, application.CreateRunParams{
		AgentProfileID: r.agentID, Instruction: instruction,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range lostPathPrefix(status) {
		if err := r.svc.RecordRunStatus(bg(), run.ID, step, nil); err != nil {
			t.Fatalf("seed run %s → %s: %v", run.ID, step, err)
		}
	}
	fresh, err := r.store.Runs().Get(bg(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

// lostPathPrefix 返回 queued → status 的状态机前缀路径（测试种子用）。
func lostPathPrefix(to domain.RunStatus) []domain.RunStatus {
	switch to {
	case domain.RunQueued:
		return nil
	case domain.RunStarting:
		return []domain.RunStatus{domain.RunStarting}
	case domain.RunRunning:
		return []domain.RunStatus{domain.RunStarting, domain.RunRunning}
	default:
		return nil
	}
}

// eventsByType 抽取某 run 指定类型的全部事件（run_seq 序）。
func (r *reconcileRig) eventsByType(t *testing.T, runID, eventType string) []application.RunEvent {
	t.Helper()
	all, err := r.store.Events().ListRunEventsIncludeInternal(bg(), runID)
	if err != nil {
		t.Fatalf("ListRunEventsIncludeInternal(%s): %v", runID, err)
	}
	var out []application.RunEvent
	for _, ev := range all {
		if ev.EventType == eventType {
			out = append(out, ev)
		}
	}
	return out
}

// starting/running 的无 lease 孤儿被带证据收口到 lost；queued 不动。
func TestReconcileOrphanedLocalRunsClosesInFlightWithEvidence(t *testing.T) {
	rig := newReconcileRig(t, "ws_reconcile")
	ctx := bg()
	starting := rig.seedRunAt(t, "卡在 starting", domain.RunStarting)
	running := rig.seedRunAt(t, "卡在 running", domain.RunRunning)
	queued := rig.seedRunAt(t, "合法待派发", domain.RunQueued)
	// running run 有一个未闭合相位（崩溃点）。
	if err := rig.svc.RecordRunEvent(ctx, running.ID, domain.EventRunPhaseEntered,
		observability.PhaseEnteredPayload(observability.PhaseStreaming, 1, nil)); err != nil {
		t.Fatal(err)
	}

	swept, err := rig.svc.ReconcileOrphanedLocalRuns(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphanedLocalRuns: %v", err)
	}
	if swept != 2 {
		t.Fatalf("应收敛 2 个孤儿（starting+running），实际 %d", swept)
	}
	for _, run := range []*domain.ExecutionRun{starting, running} {
		after, err := rig.store.Runs().Get(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != domain.RunLost {
			t.Fatalf("孤儿 run %s 应收敛 lost，实际 %s", run.ID, after.Status)
		}
		if after.FinishedAt == nil {
			t.Fatalf("孤儿 run %s 收敛后应有 FinishedAt", run.ID)
		}
	}
	if fresh, err := rig.store.Runs().Get(ctx, queued.ID); err != nil || fresh.Status != domain.RunQueued {
		t.Fatalf("queued 不扫：status=%v err=%v", fresh.Status, err)
	}

	// running 孤儿的证据链：合成闭合 + recovery_completed + decision + lost。
	closed := rig.eventsByType(t, running.ID, domain.EventRunPhaseClosed)
	var synth *application.RunEvent
	for i := range closed {
		if closed[i].Payload["phase"] == observability.PhaseStreaming {
			synth = &closed[i]
		}
	}
	if synth == nil {
		t.Fatalf("running 孤儿的未闭合相位 streaming 应被合成闭合: %+v", closed)
	}
	if synth.Payload["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("合成闭合应落 failed: %+v", synth.Payload)
	}
	failure, _ := synth.Payload["failure"].(map[string]any)
	if failure["code"] != "control_plane_restart" || failure["retryable"] != true {
		t.Fatalf("合成闭合 failure 应带 control_plane_restart/retryable: %+v", synth.Payload)
	}
	if _, ok := synth.Payload["boot_at"]; !ok {
		t.Fatalf("合成闭合 detail 应带进程新 boot 时间 boot_at: %+v", synth.Payload)
	}
	recovery := rig.eventsByType(t, running.ID, domain.EventRunRecoveryCompleted)
	if len(recovery) != 1 {
		t.Fatalf("收口成功应恰发一条 run.recovery_completed: %+v", recovery)
	}
	rec := recovery[0].Payload
	if rec["code"] != "control_plane_restart" || rec["previous_status"] != string(domain.RunRunning) ||
		rec["terminal_status"] != string(domain.RunLost) {
		t.Fatalf("recovery_completed 证据不全: %+v", rec)
	}
	if failed := rig.eventsByType(t, running.ID, domain.EventRunRecoveryFailed); len(failed) != 0 {
		t.Fatalf("收口成功不应发 recovery_failed: %+v", failed)
	}
	decisions := rig.eventsByType(t, running.ID, domain.EventRunDecision)
	if len(decisions) != 1 || decisions[0].Payload["kind"] != observability.DecisionRecoverySweep ||
		decisions[0].Payload["previous_status"] != string(domain.RunRunning) {
		t.Fatalf("应发一条 recovery_sweep decision 带 previous_status: %+v", decisions)
	}
	lostEvents := rig.eventsByType(t, running.ID, domain.EventRunLost)
	// run_events 对状态事件的投影恒为 {from, status, record_kind}（emit 既有
	// 语义，status data 只落到 run 行）；对账证据由 recovery/decision 事件承载。
	if len(lostEvents) != 1 {
		t.Fatalf("应恰发一条 run.lost: %+v", lostEvents)
	}
	if lostEvents[0].Payload["status"] != string(domain.RunLost) ||
		lostEvents[0].Payload["from"] != string(domain.RunReconnecting) {
		t.Fatalf("run.lost 应记录 running→reconnecting→lost 的最后一跳: %+v", lostEvents[0].Payload)
	}
	// starting 孤儿走 reconnecting→lost（gateway 同构路径），终局同样带证据。
	if reconnects := rig.eventsByType(t, starting.ID, domain.EventRunStatusChanged); len(reconnects) == 0 {
		t.Fatalf("starting 孤儿应经 reconnecting 收口: %+v", reconnects)
	}
	if recovery := rig.eventsByType(t, starting.ID, domain.EventRunRecoveryCompleted); len(recovery) != 1 {
		t.Fatalf("starting 孤儿也应发 recovery_completed: %+v", recovery)
	}
	// queued 无任何对账痕迹。
	for _, evType := range []string{domain.EventRunRecoveryCompleted, domain.EventRunDecision, domain.EventRunLost} {
		if evs := rig.eventsByType(t, queued.ID, evType); len(evs) != 0 {
			t.Fatalf("queued 不应产生 %s 事件: %+v", evType, evs)
		}
	}
}

// 带未释放 lease 的远程 run 不动：远程失联判定归 runnergateway.leaseSweeper，
// 本 sweeper 不重复收口（LeaselessActive 天然不命中带 lease 行的 run）。
func TestReconcileOrphanedLocalRunsSkipsLeasedRemoteRun(t *testing.T) {
	rig := newReconcileRig(t, "ws_reconcile_lease")
	ctx := bg()
	remote := rig.seedRunAt(t, "远程在飞", domain.RunRunning)
	now := time.Now().UTC()
	if err := rig.store.ExecutionHosts().Create(ctx, &domain.ExecutionHost{
		ID: "host_remote_rc", Name: "remote", Kind: domain.HostKindRemote,
		Status: domain.HostStatusOffline, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rig.store.Runners().Upsert(ctx, &application.Runner{
		ID: "runner_remote_rc", ExecutionHostID: "host_remote_rc", BootID: "boot_rc",
		ConnectionEpoch: "epoch_1", Label: "runner_remote_rc", Slots: 1, Status: "connected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := rig.store.Runners().CreateLease(ctx, &application.RunLease{
		LeaseID: "lease_remote_rc", RunID: remote.ID, RunnerID: "runner_remote_rc",
		RenewedUntil: now.Add(time.Hour).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	swept, err := rig.svc.ReconcileOrphanedLocalRuns(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphanedLocalRuns: %v", err)
	}
	if swept != 0 {
		t.Fatalf("带未释放 lease 的远程 run 不得被扫，实际收敛 %d", swept)
	}
	fresh, err := rig.store.Runs().Get(ctx, remote.ID)
	if err != nil || fresh.Status != domain.RunRunning {
		t.Fatalf("远程 run 应保持 running: status=%v err=%v", fresh.Status, err)
	}
	if evs := rig.eventsByType(t, remote.ID, domain.EventRunRecoveryCompleted); len(evs) != 0 {
		t.Fatalf("远程 run 不应产生 recovery 事件: %+v", evs)
	}
}

// 重复调用幂等：第二轮无可扫孤儿、无任何新事件/状态写入——与 leaseSweeper
// 或并发对账撞车时靠状态机迁移合法性自然幂等。
func TestReconcileOrphanedLocalRunsIdempotent(t *testing.T) {
	rig := newReconcileRig(t, "ws_reconcile_idem")
	ctx := bg()
	orphan := rig.seedRunAt(t, "幂等种子", domain.RunRunning)
	if err := rig.svc.RecordRunEvent(ctx, orphan.ID, domain.EventRunPhaseEntered,
		observability.PhaseEnteredPayload(observability.PhaseSettle, 1, nil)); err != nil {
		t.Fatal(err)
	}

	swept, err := rig.svc.ReconcileOrphanedLocalRuns(ctx)
	if err != nil || swept != 1 {
		t.Fatalf("第一轮应收敛 1 个孤儿: swept=%d err=%v", swept, err)
	}
	baseline := map[string]int{
		domain.EventRunLost:              len(rig.eventsByType(t, orphan.ID, domain.EventRunLost)),
		domain.EventRunRecoveryCompleted: len(rig.eventsByType(t, orphan.ID, domain.EventRunRecoveryCompleted)),
		domain.EventRunDecision:          len(rig.eventsByType(t, orphan.ID, domain.EventRunDecision)),
		domain.EventRunPhaseClosed:       len(rig.eventsByType(t, orphan.ID, domain.EventRunPhaseClosed)),
	}

	sweptAgain, err := rig.svc.ReconcileOrphanedLocalRuns(ctx)
	if err != nil {
		t.Fatalf("第二轮对账不应报错: %v", err)
	}
	if sweptAgain != 0 {
		t.Fatalf("第二轮应无可扫孤儿，实际收敛 %d", sweptAgain)
	}
	fresh, err := rig.store.Runs().Get(ctx, orphan.ID)
	if err != nil || fresh.Status != domain.RunLost {
		t.Fatalf("run 应保持 lost: status=%v err=%v", fresh.Status, err)
	}
	for evType, want := range baseline {
		if got := len(rig.eventsByType(t, orphan.ID, evType)); got != want {
			t.Fatalf("重复调用必须无副作用：%s 事件数 %d → %d", evType, want, got)
		}
	}
}
