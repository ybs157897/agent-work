package runnergateway

// Run Journal M2 闭合与证据（设计 notes/proposed/architecture/
// 2026-09-02-run-journal-lifecycle-logging.md §3.5）：三条死 runner 恢复路径
// 激活 run.recovery_* 契约事件（此前全仓库零发出点），携带 last_heartbeat_at
// / lease_expired_at / fencing_token / boot_id / runner_id 证据，并在收口前
// 合成闭合未闭合相位（崩溃点）。断言一律经 fakeStore 的
// ListRunEventsIncludeInternal（与生产 run_events 读取路径同形）。

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
)

// recoveryTestRig 组装带 journal 仓的 store + 已接线的 engine：engine 发出的
// 事件同步镜像进 repo，断言经 ListRunEventsIncludeInternal 全量取回。
func recoveryTestRig(t *testing.T) (*Gateway, *fakeStore, *fakeEngine, *fakeEventRepo) {
	t.Helper()
	repo := newFakeEventRepo()
	store := &fakeStore{
		hosts:   newFakeHostRepo(testEnrollmentHost("host_a", "s3cret")),
		runners: newFakeRunnerRepo(), runs: &fakeRunRepo{}, events: repo,
	}
	engine := newFakeEngine()
	engine.recordSink = repo.append
	return New(store, engine, nil), store, engine, repo
}

// eventsByType 按 event_type 抽取某 run 的事件（追加序）。
func eventsByType(t *testing.T, repo *fakeEventRepo, runID, eventType string) []application.RunEvent {
	t.Helper()
	all, err := repo.ListRunEventsIncludeInternal(context.Background(), runID)
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

// assertSynthPhaseClosed 断言相位被合成闭合：outcome=failed、failure.code =
// 恢复原因码、证据字段（runner_id）并入 detail。
func assertSynthPhaseClosed(t *testing.T, repo *fakeEventRepo, runID, phase, code, runnerID string) {
	t.Helper()
	closed := eventsByType(t, repo, runID, domain.EventRunPhaseClosed)
	for _, ev := range closed {
		if ev.Payload["phase"] != phase {
			continue
		}
		if ev.Payload["outcome"] != string(observability.PhaseFailed) {
			t.Fatalf("合成闭合应落 failed: %+v", ev.Payload)
		}
		failure, ok := ev.Payload["failure"].(map[string]any)
		if !ok || failure["code"] != code {
			t.Fatalf("合成闭合 failure.code 应为 %s: %+v", code, ev.Payload)
		}
		if ev.Payload["runner_id"] != runnerID {
			t.Fatalf("合成闭合 detail 应带 runner_id 证据: %+v", ev.Payload)
		}
		return
	}
	t.Fatalf("run %s 的未闭合相位 %s 应被合成闭合，实际 closed 事件：%+v", runID, phase, closed)
}

// settleRestartedRuns（进程重启路径）：running→reconnecting→lost 同步收口后
// 单发 run.recovery_failed 带全程证据（不成对：boot_id 变化即判死，reconnecting
// 只是状态机跳板、无真实恢复窗口），未闭合相位（streaming）带证据合成闭合。
func TestSettleRestartedRunsEmitsRecoveryEvidence(t *testing.T) {
	g, store, engine, repo := recoveryTestRig(t)
	heartbeat := time.Now().Add(-time.Minute).UTC()
	store.runners.upserts = append(store.runners.upserts, &application.Runner{
		ID: "runner_a", ExecutionHostID: "host_a", BootID: "boot_new", LastSeenAt: &heartbeat,
	})
	store.runners.leases = append(store.runners.leases, &application.RunLease{
		LeaseID: "lease_r1", RunID: "run_r1", RunnerID: "runner_a", FencingToken: 7,
		RenewedUntil: time.Now().Add(-30 * time.Second).UTC(),
	})
	engine.runs["run_r1"] = domain.RunRunning
	repo.seed("run_r1", domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseStreaming, 1, nil))
	rc := &runnerConn{
		gw: g, runnerID: "runner_a", hostID: "host_a", bootID: "boot_new", epoch: "epoch_new",
		activeRuns: map[string]*activeRun{},
	}
	rc.restartedRunIDs = []string{"run_r1"}

	g.settleRestartedRuns(rc)

	if len(engine.statuses) != 2 || engine.statuses[0] != domain.RunReconnecting || engine.statuses[1] != domain.RunLost {
		t.Fatalf("进程重启 running 应收敛 reconnecting→lost: %v", engine.statuses)
	}
	recovery := eventsByType(t, repo, "run_r1", domain.EventRunRecoveryFailed)
	if len(recovery) != 1 {
		t.Fatalf("进程重启路径应恰发一条 run.recovery_failed（一次性同步收口，不成对）: %+v", recovery)
	}
	data := recovery[0].Payload
	if data["code"] != "runner_process_restarted" || data["retryable"] != true {
		t.Fatalf("recovery_failed 应带 code/retryable: %+v", data)
	}
	if data["runner_id"] != "runner_a" || data["boot_id"] != "boot_new" || data["connection_epoch"] != "epoch_new" {
		t.Fatalf("recovery_failed 应带 runner 身份证据: %+v", data)
	}
	if data["lease_id"] != "lease_r1" || data["fencing_token"] != int64(7) {
		t.Fatalf("recovery_failed 应带 lease 身份证据: %+v", data)
	}
	assertSynthPhaseClosed(t, repo, "run_r1", observability.PhaseStreaming, "runner_process_restarted", "runner_a")
	// started 不发：一次性同步收口不成对（见 settleRestartedRuns 注释）。
	if started := eventsByType(t, repo, "run_r1", domain.EventRunRecoveryStarted); len(started) != 0 {
		t.Fatalf("进程重启路径不应发 recovery_started: %+v", started)
	}
}

// markExpiredLeaseTerminal（lease 过期路径）：reconnecting→lost 后发
// recovery_failed，证据带 lease_expired_at / fencing_token / runner_id /
// last_heartbeat_at / boot_id；handshake 相位带证据合成闭合。
func TestMarkExpiredLeaseTerminalEmitsRecoveryEvidence(t *testing.T) {
	g, store, engine, repo := recoveryTestRig(t)
	heartbeat := time.Now().Add(-3 * time.Minute).UTC()
	expired := time.Now().Add(-90 * time.Second).UTC()
	store.runners.upserts = append(store.runners.upserts, &application.Runner{
		ID: "runner_b", ExecutionHostID: "host_a", BootID: "boot_b", LastSeenAt: &heartbeat,
	})
	store.runners.leases = append(store.runners.leases, &application.RunLease{
		LeaseID: "lease_e1", RunID: "run_e1", RunnerID: "runner_b", FencingToken: 3,
		RenewedUntil: expired, Released: true,
	})
	engine.runs["run_e1"] = domain.RunRunning
	repo.seed("run_e1", domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseHandshake, 1, nil))

	g.markExpiredLeaseTerminal(context.Background(), "run_e1")

	if len(engine.statuses) != 2 || engine.statuses[0] != domain.RunReconnecting || engine.statuses[1] != domain.RunLost {
		t.Fatalf("lease 过期 running 应收敛 reconnecting→lost: %v", engine.statuses)
	}
	recovery := eventsByType(t, repo, "run_e1", domain.EventRunRecoveryFailed)
	if len(recovery) != 1 {
		t.Fatalf("lease 过期路径应恰发一条 run.recovery_failed: %+v", recovery)
	}
	data := recovery[0].Payload
	if data["code"] != "runner_lease_expired" || data["retryable"] != true {
		t.Fatalf("recovery_failed 应带 code/retryable: %+v", data)
	}
	if data["lease_id"] != "lease_e1" || data["fencing_token"] != int64(3) || data["runner_id"] != "runner_b" {
		t.Fatalf("recovery_failed 应带 lease/runner 身份证据: %+v", data)
	}
	if data["lease_expired_at"] != expired.Format(time.RFC3339Nano) {
		t.Fatalf("recovery_failed 应带 lease_expired_at（=renewed_until）: %+v", data)
	}
	if data["last_heartbeat_at"] != heartbeat.Format(time.RFC3339Nano) || data["boot_id"] != "boot_b" {
		t.Fatalf("recovery_failed 应带 last_heartbeat_at/boot_id 证据: %+v", data)
	}
	assertSynthPhaseClosed(t, repo, "run_e1", observability.PhaseHandshake, "runner_lease_expired", "runner_b")
}

// handleDisconnect（断连路径）：run 进入 reconnecting（恢复窗口开启）时发
// run.recovery_started 带 runner/lease 证据并合成闭合未闭合相位；结局
// recovery_failed 由后续判死路径补齐，本路径不发。
func TestHandleDisconnectEmitsRecoveryStarted(t *testing.T) {
	g, store, engine, repo := recoveryTestRig(t)
	heartbeat := time.Now().Add(-20 * time.Second).UTC()
	store.runners.upserts = append(store.runners.upserts, &application.Runner{
		ID: "runner_c", ExecutionHostID: "host_a", BootID: "boot_c", LastSeenAt: &heartbeat,
	})
	engine.runs["run_d1"] = domain.RunRunning
	repo.seed("run_d1", domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseFirstEvent, 1, nil))
	rc := &runnerConn{
		gw: g, runnerID: "runner_c", hostID: "host_a", bootID: "boot_c", epoch: "epoch_1",
		activeRuns: map[string]*activeRun{"run_d1": {LeaseID: "lease_d1", FencingToken: 9}},
		send:       make(chan []byte, 4),
	}
	store.runners.leases = append(store.runners.leases, &application.RunLease{
		LeaseID: "lease_d1", RunID: "run_d1", RunnerID: "runner_c", FencingToken: 9,
		RenewedUntil: time.Now().Add(time.Minute).UTC(),
	})
	g.mu.Lock()
	g.conns["runner_c"] = rc
	g.mu.Unlock()

	g.handleDisconnect(rc)

	if len(engine.statuses) != 1 || engine.statuses[0] != domain.RunReconnecting {
		t.Fatalf("断连应把活动 run 收敛 reconnecting: %v", engine.statuses)
	}
	started := eventsByType(t, repo, "run_d1", domain.EventRunRecoveryStarted)
	if len(started) != 1 {
		t.Fatalf("断连应恰发一条 run.recovery_started: %+v", started)
	}
	data := started[0].Payload
	if data["code"] != "runner_disconnected" {
		t.Fatalf("recovery_started 应带 code: %+v", data)
	}
	if data["runner_id"] != "runner_c" || data["boot_id"] != "boot_c" || data["connection_epoch"] != "epoch_1" {
		t.Fatalf("recovery_started 应带 runner 身份证据: %+v", data)
	}
	if data["lease_id"] != "lease_d1" || data["fencing_token"] != int64(9) {
		t.Fatalf("recovery_started 应带 lease 身份证据: %+v", data)
	}
	if data["last_heartbeat_at"] != heartbeat.Format(time.RFC3339Nano) {
		t.Fatalf("recovery_started 应带 last_heartbeat_at（offline 投影覆写前读取）: %+v", data)
	}
	if failed := eventsByType(t, repo, "run_d1", domain.EventRunRecoveryFailed); len(failed) != 0 {
		t.Fatalf("断连路径不发恢复结局（判死路径补齐）: %+v", failed)
	}
	assertSynthPhaseClosed(t, repo, "run_d1", observability.PhaseFirstEvent, "runner_disconnected", "runner_c")
}

// 无未闭合相位的 run：恢复路径不发合成闭合（找不到就不发）。
func TestRecoveryWithoutUnclosedPhaseEmitsNoSyntheticClose(t *testing.T) {
	g, _, engine, repo := recoveryTestRig(t)
	engine.runs["run_clean"] = domain.RunRunning
	// 已配对的相位链：entered → closed，无未闭合相位。
	repo.seed("run_clean", domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(observability.PhaseDispatch, 1, nil))
	repo.seed("run_clean", domain.EventRunPhaseClosed, observability.PhaseClosedPayload(
		observability.PhaseDispatch, observability.PhaseOK, nil, 12, nil))

	g.markExpiredLeaseTerminal(context.Background(), "run_clean")

	if closed := eventsByType(t, repo, "run_clean", domain.EventRunPhaseClosed); len(closed) != 1 {
		t.Fatalf("已配对相位不得被重复合成闭合: %+v", closed)
	}
	if recovery := eventsByType(t, repo, "run_clean", domain.EventRunRecoveryFailed); len(recovery) != 1 {
		t.Fatalf("判死路径仍应发 recovery_failed: %+v", recovery)
	}
}
