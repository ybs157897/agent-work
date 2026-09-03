package runnergateway

// Run Journal dispatch 相位（remote 路径）的埋点回归：Gateway.Dispatch 在
// lease 授予 + offer 成功入队后补 run.phase_closed{ok}（detail 带 route/
// runner_id/lease_id/fencing_token），其余失败路径补 closed{failed}。
// entered 由调用方 chainDispatcher 发出（cmd 侧测试覆盖成对语义）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
)

// dispatchJournalEvents 抽出 run 的 dispatch 相位事件（追加序）。
func dispatchJournalEvents(t *testing.T, engine *fakeEngine, runID string) []recordedRunEvent {
	t.Helper()
	var out []recordedRunEvent
	for _, ev := range engine.events {
		if ev.RunID == runID &&
			(ev.EventType == domain.EventRunPhaseEntered || ev.EventType == domain.EventRunPhaseClosed) {
			out = append(out, ev)
		}
	}
	return out
}

// lease 授予 + offer 成功入队：恰发一条 closed{ok}，detail 与 store 内 lease
// 身份（runner_id/lease_id/fencing_token）完全一致。
func TestDispatchJournalClosedOKCarriesLeaseIdentity(t *testing.T) {
	g, rcA, _ := newDispatchGateway(t)
	engine := g.engine.(*fakeEngine)
	run := &domain.ExecutionRun{ID: "run_journal_ok", AgentProfileID: "agent_dispatch", Input: map[string]any{}}

	if err := g.Dispatch(context.Background(), run, rootSnapshotFor("host_a"), "mock"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	readOffer(t, rcA) // offer 已入队

	journal := dispatchJournalEvents(t, engine, "run_journal_ok")
	if len(journal) != 1 {
		t.Fatalf("remote 路径应只发一条 closed（entered 归 chainDispatcher），实际 %d：%+v", len(journal), journal)
	}
	ev := journal[0]
	if ev.EventType != domain.EventRunPhaseClosed {
		t.Fatalf("应发 run.phase_closed，实际 %s", ev.EventType)
	}
	if ev.Data["phase"] != observability.PhaseDispatch || ev.Data["outcome"] != string(observability.PhaseOK) {
		t.Fatalf("closed 载荷缺 phase/outcome: %+v", ev.Data)
	}
	if ev.Data["route"] != "remote" || ev.Data["runner_id"] != "runner_a" {
		t.Fatalf("closed detail 缺 route/runner_id: %+v", ev.Data)
	}
	if _, ok := ev.Data["duration_ms"]; !ok {
		t.Fatalf("closed 载荷应含 duration_ms: %+v", ev.Data)
	}
	lease := g.store.(*fakeStore).runners.leases[0]
	if ev.Data["lease_id"] != lease.LeaseID {
		t.Fatalf("closed detail lease_id 与 store 不符: %+v vs %s", ev.Data["lease_id"], lease.LeaseID)
	}
	if ev.Data["fencing_token"] != lease.FencingToken {
		t.Fatalf("closed detail fencing_token 与 store 不符: %+v vs %d", ev.Data["fencing_token"], lease.FencingToken)
	}
}

// CreateLease 失败：closed{failed} 携带原因（message 含错误原文），failure
// 分类与既有 dispatch 终态语义（execution_host_unavailable/retryable）对齐，
// 且不下发 offer、不落 ok。
func TestDispatchJournalClosedFailedOnLeaseCreateFailure(t *testing.T) {
	g, rcA, _ := newDispatchGateway(t)
	engine := g.engine.(*fakeEngine)
	reason := errors.New("lease table locked")
	g.store.(*fakeStore).runners.leaseErr = reason
	run := &domain.ExecutionRun{ID: "run_journal_lease", AgentProfileID: "agent_dispatch", Input: map[string]any{}}

	if err := g.Dispatch(context.Background(), run, rootSnapshotFor("host_a"), "mock"); err == nil {
		t.Fatal("CreateLease 失败必须返回错误")
	}

	journal := dispatchJournalEvents(t, engine, "run_journal_lease")
	if len(journal) != 1 || journal[0].EventType != domain.EventRunPhaseClosed {
		t.Fatalf("lease 失败应恰发一条 closed，实际：%+v", journal)
	}
	ev := journal[0]
	if ev.Data["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("失败路径 closed 应落 failed: %+v", ev.Data)
	}
	failure, ok := ev.Data["failure"].(map[string]any)
	if !ok {
		t.Fatalf("failed closed 应含 failure: %+v", ev.Data)
	}
	if failure["code"] != "execution_host_unavailable" || failure["retryable"] != true || failure["family"] != "workspace" {
		t.Fatalf("failure 分类应与既有 dispatch 终态语义对齐: %+v", failure)
	}
	if msg, _ := failure["message"].(string); !strings.Contains(msg, reason.Error()) {
		t.Fatalf("lease 创建失败必须携带原因: %+v", failure)
	}
	assertNoFrame(t, rcA)
}

// offer 入队失败（连接已关闭）：同样恰发一条 closed{failed}，不带 ok。
func TestDispatchJournalClosedFailedOnOfferEnqueueFailure(t *testing.T) {
	g, rcA, _ := newDispatchGateway(t)
	engine := g.engine.(*fakeEngine)
	rcA.mu.Lock()
	rcA.closed = true
	rcA.mu.Unlock()
	run := &domain.ExecutionRun{ID: "run_journal_enqueue", AgentProfileID: "agent_dispatch", Input: map[string]any{}}

	if err := g.Dispatch(context.Background(), run, rootSnapshotFor("host_a"), "mock"); err == nil {
		t.Fatal("offer 入队失败必须返回错误")
	}

	journal := dispatchJournalEvents(t, engine, "run_journal_enqueue")
	if len(journal) != 1 || journal[0].EventType != domain.EventRunPhaseClosed {
		t.Fatalf("入队失败应恰发一条 closed，实际：%+v", journal)
	}
	ev := journal[0]
	if ev.Data["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("入队失败 closed 应落 failed: %+v", ev.Data)
	}
	if ev.Data["route"] != "remote" {
		t.Fatalf("失败 closed detail 应含 route: %+v", ev.Data)
	}
	if lease := g.store.(*fakeStore).runners.leases; len(lease) != 1 || !lease[0].Released {
		t.Fatalf("入队失败必须释放 DB lease: %+v", lease)
	}
}
