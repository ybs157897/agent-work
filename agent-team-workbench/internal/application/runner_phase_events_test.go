// runner_phase_events_test.go 覆盖 Runner v2 相位帧（Run Journal internal
// 事件，D2）：远程 runner 内 spawn/handshake/first_event 环节的
// run.phase_entered/run.phase_closed 经 run.event 帧回传控制面后必须落
// run_events——活动租约期正常应用；终态后的迟到相位闭包（settle）走
// 终态观测例外，严格身份（lease/run/runner/fencing 四要素）+ 租约已释放 +
// Run 已终态三者齐备才放行；dedup 与 surface 帧同一语义（同帧重放幂等）。
package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// phaseEventInput 组装一条合法 framing 的相位事件命令（与 runnerd
// eventPayloadOf 同构：framing 五元组 + 稳定事件身份）。
func phaseEventInput(runID, leaseID, runnerID string, fencing, seq int64, eventID, kind string, data map[string]any) application.RunnerEventInput {
	return application.RunnerEventInput{
		RunID: runID, LeaseID: leaseID, RunnerID: runnerID, FencingToken: fencing,
		EventID: eventID, ProducerSeq: seq, Kind: kind, Data: data,
	}
}

// runPhaseEvents 取单个 run 的 phase 事件（按 run_seq 正序）。
func runPhaseEvents(t *testing.T, ctx context.Context, store *sqlstore.Store, runID string) []application.RunEvent {
	t.Helper()
	events, err := store.Events().ListRunEventsIncludeInternal(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var phase []application.RunEvent
	for _, ev := range events {
		if ev.EventType == domain.EventRunPhaseEntered || ev.EventType == domain.EventRunPhaseClosed {
			phase = append(phase, ev)
		}
	}
	return phase
}

// 活动租约期：相位帧 applied 并只落 run_events（surface 回放不可见——
// internal 分流）；同帧重放 duplicate 不重复落库。
func TestApplyRunnerPhaseFramesLandInRunEvents(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_runner_phase")
	run := remoteRunningRun(t, ctx, svc, store, wsID, agentID, "lease_runner_phase")
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_runner_phase")

	entered := map[string]any{"phase": "spawn", "attempt": 1, "detail": map[string]any{"adapter": "mock"}}
	ack, err := svc.ApplyRunnerEvent(ctx, phaseEventInput(run.ID, "lease_runner_phase", "runner_ctx",
		fencing, 1, "revt_phase_1", domain.EventRunPhaseEntered, entered))
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("相位帧应 applied: ack=%+v err=%v", ack, err)
	}

	phase := runPhaseEvents(t, ctx, store, run.ID)
	if len(phase) != 1 || phase[0].EventType != domain.EventRunPhaseEntered {
		t.Fatalf("phase_entered 应恰落一条: %+v", phase)
	}
	if phase[0].Payload["phase"] != "spawn" || phase[0].Payload["attempt"] != float64(1) {
		t.Fatalf("相位载荷失真: %+v", phase[0].Payload)
	}

	// surface 回放不得看到 internal 相位事件（internal 与 surface 分层）。
	surface, err := store.Events().ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range surface {
		if domain.IsInternalEventName(ev.EventType) {
			t.Fatalf("surface 回放不得含 internal 事件: %s", ev.EventType)
		}
	}

	// 同帧重放：dedup 键 (run, lease, runner, producer_seq) 不变 → duplicate，
	// 仍只有一条。
	dup, err := svc.ApplyRunnerEvent(ctx, phaseEventInput(run.ID, "lease_runner_phase", "runner_ctx",
		fencing, 1, "revt_phase_1", domain.EventRunPhaseEntered, entered))
	if err != nil || dup.Outcome != application.RunnerEventDuplicate {
		t.Fatalf("相位帧重放应 duplicate: ack=%+v err=%v", dup, err)
	}
	if got := runPhaseEvents(t, ctx, store, run.ID); len(got) != 1 {
		t.Fatalf("重放后应仍只有一条相位事件，实际 %d", len(got))
	}
}

// 终态后迟到的 settle 闭包必须 applied（终态观测例外扩面到相位帧）：
// status 帧应用时已释放 lease、网关已清活动镜像，此时到达的
// phase_closed{settle} 落不了 run_events 就等于每个远程 run 都以未闭合
// settle 收尾（假"崩溃"信号）。同帧重放 duplicate；例外不扩面——迟到
// message.delta 仍 stale。
func TestApplyRunnerLatePhaseClosureAfterTerminalApplies(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_late_phase")
	run, fencing := terminalRunWithReleasedLease(t, ctx, svc, store, wsID, agentID, "lease_late_phase")

	closure := map[string]any{
		"phase": "settle", "outcome": "ok", "duration_ms": int64(12),
		"detail": map[string]any{"terminal_status": "succeeded"},
	}
	ack, err := svc.ApplyRunnerEvent(ctx, phaseEventInput(run.ID, "lease_late_phase", "runner_ctx",
		fencing, 2, "revt_late_phase_1", domain.EventRunPhaseClosed, closure))
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("终态后迟到相位闭包应 applied: ack=%+v err=%v", ack, err)
	}
	phase := runPhaseEvents(t, ctx, store, run.ID)
	if len(phase) != 1 || phase[0].EventType != domain.EventRunPhaseClosed {
		t.Fatalf("迟到闭包应恰落一条: %+v", phase)
	}
	if phase[0].Payload["phase"] != "settle" || phase[0].Payload["outcome"] != "ok" {
		t.Fatalf("闭包载荷失真: %+v", phase[0].Payload)
	}

	dup, err := svc.ApplyRunnerEvent(ctx, phaseEventInput(run.ID, "lease_late_phase", "runner_ctx",
		fencing, 2, "revt_late_phase_1", domain.EventRunPhaseClosed, closure))
	if err != nil || dup.Outcome != application.RunnerEventDuplicate {
		t.Fatalf("迟到闭包重放应 duplicate: ack=%+v err=%v", dup, err)
	}
	if got := runPhaseEvents(t, ctx, store, run.ID); len(got) != 1 {
		t.Fatalf("重放后应仍只有一条闭包，实际 %d", len(got))
	}

	// 例外只覆盖相位帧与 usage.updated：终态后迟到的 message.delta 仍 stale。
	nonPhase, err := svc.ApplyRunnerEvent(ctx, phaseEventInput(run.ID, "lease_late_phase", "runner_ctx",
		fencing, 3, "revt_late_phase_2", domain.EventMessageDelta,
		map[string]any{"role": "assistant", "text": "late"}))
	if err != nil || nonPhase.Outcome != application.RunnerEventStale {
		t.Fatalf("非相位帧不得走终态观测例外: ack=%+v err=%v", nonPhase, err)
	}
}

// 迟到相位闭包的严格身份：fencing 失配 / 错 runner / 未知 lease 一律 stale，
// 不应用、不落库——例外不为 internal 开新洞。
func TestApplyRunnerLatePhaseStrictIdentityStaysStale(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_late_phase_identity")
	run, fencing := terminalRunWithReleasedLease(t, ctx, svc, store, wsID, agentID, "lease_late_id")

	closure := map[string]any{"phase": "settle", "outcome": "ok", "duration_ms": int64(5)}
	for name, in := range map[string]application.RunnerEventInput{
		"fencing mismatch": phaseEventInput(run.ID, "lease_late_id", "runner_ctx",
			fencing+1, 2, "revt_id_1", domain.EventRunPhaseClosed, closure),
		"runner mismatch": phaseEventInput(run.ID, "lease_late_id", "runner_other",
			fencing, 2, "revt_id_2", domain.EventRunPhaseClosed, closure),
		"unknown lease": phaseEventInput(run.ID, "lease_missing", "runner_ctx",
			fencing, 2, "revt_id_3", domain.EventRunPhaseClosed, closure),
	} {
		ack, err := svc.ApplyRunnerEvent(ctx, in)
		if err != nil || ack.Outcome != application.RunnerEventStale {
			t.Fatalf("%s 的迟到相位帧应 stale: ack=%+v err=%v", name, ack, err)
		}
	}
	if got := runPhaseEvents(t, ctx, store, run.ID); len(got) != 0 {
		t.Fatalf("stale 帧不得落库，实际 %d 条", len(got))
	}
}

// 防御：lease 已释放但 Run 未终态（如 sweep 收走租约）→ 迟到相位帧仍 stale。
func TestApplyRunnerLatePhaseNonTerminalRunStaysStale(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_late_phase_nonterm")
	run := remoteRunningRun(t, ctx, svc, store, wsID, agentID, "lease_phase_nonterm")
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_phase_nonterm")
	if err := store.Runners().ReleaseLease(ctx, "lease_phase_nonterm", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	ack, err := svc.ApplyRunnerEvent(ctx, phaseEventInput(run.ID, "lease_phase_nonterm", "runner_ctx",
		fencing, 1, "revt_nonterm_1", domain.EventRunPhaseClosed,
		map[string]any{"phase": "streaming", "outcome": "ok", "duration_ms": int64(1)}))
	if err != nil || ack.Outcome != application.RunnerEventStale {
		t.Fatalf("非终态 Run 的已释放租约相位帧应 stale: ack=%+v err=%v", ack, err)
	}
	if got := runPhaseEvents(t, ctx, store, run.ID); len(got) != 0 {
		t.Fatalf("stale 帧不得落库，实际 %d 条", len(got))
	}
}
