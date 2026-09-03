package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
)

// recordEngine 在 statefulEngine 的状态机权威之上记录 RecordRunEvent 调用，
// 供 runnerCallbacks 的 journal 埋点断言使用。
type recordEngine struct {
	statefulEngine

	mu     sync.Mutex
	events []journaledEvent
}

func (e *recordEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cp := make(map[string]any, len(data))
	for k, v := range data {
		cp[k] = v
	}
	e.events = append(e.events, journaledEvent{typ: evType, data: cp})
	return nil
}

func (e *recordEngine) snapshot() ([]string, []journaledEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	types := make([]string, 0, len(e.events))
	for _, ev := range e.events {
		types = append(types, ev.typ)
	}
	return types, append([]journaledEvent(nil), e.events...)
}

func newRecordEngine() *recordEngine {
	return &recordEngine{statefulEngine: statefulEngine{run: dispatchedRun()}}
}

func newTestCallbacks(engine *recordEngine) *runnerCallbacks {
	runner := NewModuleRunner(engine)
	return &runnerCallbacks{
		runner:  runner,
		runID:   engine.run.ID,
		journal: observability.NewJournal(engine.RecordRunEvent),
		budget:  observability.NewLogBudget(),
	}
}

// phaseEvent 在记录的事件里找指定 phase 的指定相位事件。
func phaseEvent(t *testing.T, events []journaledEvent, eventType, phase string) (journaledEvent, bool) {
	t.Helper()
	for _, ev := range events {
		if ev.typ != eventType {
			continue
		}
		if p, _ := ev.data["phase"].(string); p == phase {
			return ev, true
		}
	}
	return journaledEvent{}, false
}

// TestInternalEventsDoNotAdvanceRun 回归：adapter 经 OnEvent 直发的 Run Journal
// internal 相位事件是观测面，绝不触发 markRunning——否则 run.started 会提前到
// spawn/handshake 区间，状态语义被破坏；首个 surface 事件照常推进 running。
func TestInternalEventsDoNotAdvanceRun(t *testing.T) {
	engine := newRecordEngine()
	cb := newTestCallbacks(engine)

	phaseData := observability.PhaseEnteredPayload(observability.PhaseSpawn, 1, nil)
	cb.OnEvent(domain.EventRunPhaseEntered, phaseData)
	cb.OnEvent(domain.EventRunPhaseClosed, observability.PhaseClosedPayload(
		observability.PhaseSpawn, observability.PhaseOK, nil, 5, map[string]any{"pid": 1, "pgid": 1}))

	if got := engine.status(); got != domain.RunQueued {
		t.Fatalf("internal 相位事件不得推进状态，实际 %s", got)
	}
	engine.mu.Lock()
	statusCalls := append([]domain.RunStatus(nil), engine.statusCalls...)
	engine.mu.Unlock()
	for _, s := range statusCalls {
		if s == domain.RunRunning {
			t.Fatalf("internal 事件触发了 starting→running: %v", statusCalls)
		}
	}
	// 事件本身照常落 run_events（internal 分道可见）。
	types, _ := engine.snapshot()
	if len(types) != 2 || types[0] != domain.EventRunPhaseEntered || types[1] != domain.EventRunPhaseClosed {
		t.Fatalf("internal 事件应原样转发: %v", types)
	}

	// 首个真实（surface）回调仍触发 markRunning（execute 常规前置 starting 已落）。
	if err := engine.run.Transition(domain.RunStarting, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	cb.OnEvent(domain.EventMessageDelta, map[string]any{"role": "assistant"})
	if got := engine.status(); got != domain.RunRunning {
		t.Fatalf("surface 事件应推进 running，实际 %s", got)
	}
}

// journaledEvent 一次 RecordRunEvent 调用的记录。
type journaledEvent struct {
	typ  string
	data map[string]any
}

// scriptModule 可编程 AdapterModule：Execute 内执行 run 脚本后返回固定 Outcome。
type scriptModule struct {
	run     func(ex *ExecContext)
	outcome Outcome
}

func (m *scriptModule) Manifest(context.Context) (AdapterManifest, error) {
	return AdapterManifest{AdapterID: "fake"}, nil
}

func (m *scriptModule) Probe(context.Context, ProbeRequest) (ProbeResult, error) {
	return ProbeResult{OK: true}, nil
}

func (m *scriptModule) Execute(ex *ExecContext) ExecResult {
	if m.run != nil {
		m.run(ex)
	}
	return ExecResult{Outcome: m.outcome}
}

// waitForPhaseEvent 轮询等待指定相位事件落库（closeSettle 在终态写入之后，
// Dispatch 异步驱动下可能晚于 waitTerminal 返回）。
func waitForPhaseEvent(t *testing.T, engine *recordEngine, eventType, phase string) journaledEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, events := engine.snapshot()
		if ev, ok := phaseEvent(t, events, eventType, phase); ok {
			return ev
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("超时未等到 %s(%s)", eventType, phase)
	return journaledEvent{}
}

// phaseSequence 返回相位事件的 "entered(first_event)" 形态交错序列。
func phaseSequence(events []journaledEvent) []string {
	var out []string
	for _, ev := range events {
		switch ev.typ {
		case domain.EventRunPhaseEntered, domain.EventRunPhaseClosed:
			p, _ := ev.data["phase"].(string)
			name := "entered"
			if ev.typ == domain.EventRunPhaseClosed {
				name = "closed"
			}
			out = append(out, name+"("+p+")")
		}
	}
	return out
}

func containsSubsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// TestModuleRunnerJournalPhaseChain：adapter 发 entered{first_event} 后，首个
// 真实回调（OnSession）收口 first_event 并开启 streaming；Execute 返回后
// streaming 收口、module 侧补发 entered{settle}，recordTerminal 完成时以
// terminal_status 收口——每 run 恰好一对 settle 事件。
func TestModuleRunnerJournalPhaseChain(t *testing.T) {
	engine := newRecordEngine()
	runner := NewModuleRunner(engine)
	runner.Register("fake", &scriptModule{outcome: OutcomeSucceeded, run: func(ex *ExecContext) {
		// 模拟 adapter：handshake 结束后布防 first_event。
		ex.Callbacks.OnEvent(domain.EventRunPhaseEntered,
			observability.PhaseEnteredPayload(observability.PhaseFirstEvent, 1, nil))
		ex.Callbacks.OnSession(SessionUpdate{Ref: "fake://s1"}) // 首个真实回调
	}})
	if err := runner.Dispatch(context.Background(), engine.run); err != nil {
		t.Fatal(err)
	}
	if got := engine.waitTerminal(t); got != domain.RunSucceeded {
		t.Fatalf("期望 succeeded，实际 %s", got)
	}
	waitForPhaseEvent(t, engine, domain.EventRunPhaseClosed, observability.PhaseSettle)

	_, events := engine.snapshot()
	seq := phaseSequence(events)
	if !containsSubsequence(seq, []string{
		"entered(first_event)", "closed(first_event)", "entered(streaming)",
		"closed(streaming)", "entered(settle)", "closed(settle)",
	}) {
		t.Fatalf("相位链漂移: %v", seq)
	}
	entered, ok := phaseEvent(t, events, domain.EventRunPhaseEntered, observability.PhaseSettle)
	if !ok {
		t.Fatal("缺少 settle entered")
	}
	if attempt, _ := entered.data["attempt"].(int); attempt != 1 {
		t.Fatalf("module 侧 settle entered attempt 应为 1: %+v", entered.data)
	}
	closed, _ := phaseEvent(t, events, domain.EventRunPhaseClosed, observability.PhaseSettle)
	if closed.data["terminal_status"] != string(domain.RunSucceeded) {
		t.Fatalf("settle closed 应带 terminal_status=succeeded: %+v", closed.data)
	}
	if _, has := closed.data["fallback"]; has {
		t.Fatalf("正常终态不应带 fallback: %+v", closed.data)
	}
	// settle 恰好一对（重复 entered 会破坏「未闭合即故障点」定位语义）。
	count := 0
	for _, s := range seq {
		if s == "entered(settle)" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("settle entered 应恰好一次，实际 %d: %v", count, seq)
	}
}

// TestModuleRunnerSettleEnteredDedup：adapter 已在裁决入口自发 entered{settle}
// （codexapp composeResult 形态）时，module 侧不得重复补发。
func TestModuleRunnerSettleEnteredDedup(t *testing.T) {
	engine := newRecordEngine()
	runner := NewModuleRunner(engine)
	runner.Register("fake", &scriptModule{outcome: OutcomeSucceeded, run: func(ex *ExecContext) {
		ex.Callbacks.OnEvent(domain.EventRunPhaseEntered,
			observability.PhaseEnteredPayload(observability.PhaseSettle, 1, map[string]any{"verdict_branch": "turn_completed"}))
	}})
	if err := runner.Dispatch(context.Background(), engine.run); err != nil {
		t.Fatal(err)
	}
	if got := engine.waitTerminal(t); got != domain.RunSucceeded {
		t.Fatalf("期望 succeeded，实际 %s", got)
	}
	waitForPhaseEvent(t, engine, domain.EventRunPhaseClosed, observability.PhaseSettle)

	_, events := engine.snapshot()
	count := 0
	for _, ev := range events {
		if ev.typ == domain.EventRunPhaseEntered {
			if p, _ := ev.data["phase"].(string); p == observability.PhaseSettle {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("settle entered 应恰好一次（去重失败）: %d", count)
	}
}

// TestModuleRunnerPanicClosesStreamingFailed：panic 兜底路径必须把未闭合的
// streaming 以 failed 收口（DSH 式「未闭合即故障点」依赖成对纪律）。
func TestModuleRunnerPanicClosesStreamingFailed(t *testing.T) {
	engine := newRecordEngine()
	runner := NewModuleRunner(engine)
	runner.Register("fake", &scriptModule{outcome: OutcomeSucceeded, run: func(ex *ExecContext) {
		ex.Callbacks.OnEvent(domain.EventRunPhaseEntered,
			observability.PhaseEnteredPayload(observability.PhaseFirstEvent, 1, nil))
		ex.Callbacks.OnSession(SessionUpdate{Ref: "fake://s1"})
		panic("boom")
	}})
	if err := runner.Dispatch(context.Background(), engine.run); err != nil {
		t.Fatal(err)
	}
	if got := engine.waitTerminal(t); got != domain.RunFailed {
		t.Fatalf("panic 应落 failed，实际 %s", got)
	}
	closed := waitForPhaseEvent(t, engine, domain.EventRunPhaseClosed, observability.PhaseStreaming)
	if closed.data["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("streaming 应以 failed 收口: %+v", closed.data)
	}
	failure, _ := closed.data["failure"].(map[string]any)
	if failure == nil || failure["code"] != "execute_panic" {
		t.Fatalf("streaming 失败应携带 execute_panic: %+v", closed.data)
	}
	settleClosed := waitForPhaseEvent(t, engine, domain.EventRunPhaseClosed, observability.PhaseSettle)
	if settleClosed.data["terminal_status"] != string(domain.RunFailed) {
		t.Fatalf("settle closed terminal_status 应为 failed: %+v", settleClosed.data)
	}
}

// TestModuleRunnerFallbackVisibleInSettleClose：「Outcome 必落终态」兜底回退
// failed 时，settle closed detail 必须可辨（terminal_status=failed + fallback）。
func TestModuleRunnerFallbackVisibleInSettleClose(t *testing.T) {
	engine := newRecordEngine()
	engine.rejectStatus = map[domain.RunStatus]error{domain.RunSucceeded: errors.New("rejected")}
	runner := NewModuleRunner(engine)
	cb := newTestCallbacks(engine)
	cb.enterSettle(context.Background())
	ex := &ExecContext{Run: engine.run, Callbacks: cb}
	// queued→succeeding/succeeded 均不合法 → 兜底回退 failed。
	runner.recordTerminal(context.Background(), ex, ExecResult{Outcome: OutcomeSucceeded})
	if got := engine.status(); got != domain.RunFailed {
		t.Fatalf("应回退 failed，实际 %s", got)
	}
	closed := waitForPhaseEvent(t, engine, domain.EventRunPhaseClosed, observability.PhaseSettle)
	if closed.data["terminal_status"] != string(domain.RunFailed) || closed.data["fallback"] != true {
		t.Fatalf("settle closed 应可辨 fallback: %+v", closed.data)
	}
	if code, _ := closed.data["failure_code"].(string); code != "" {
		t.Fatalf("无 Failure 的 Outcome 不应带 failure_code: %+v", closed.data)
	}
}

// TestOnLogLandsBudgetedChunks：OnLog 收口为 run.log_chunk——预算内原样落库，
// 首次超出截断标记 truncated，耗尽后静默丢弃；原始输出不推进 Run 状态。
func TestOnLogLandsBudgetedChunks(t *testing.T) {
	engine := newRecordEngine()
	cb := newTestCallbacks(engine)

	first := strings.Repeat("a", 40960)
	second := strings.Repeat("b", 40960)
	cb.OnLog("stderr", first)
	cb.OnLog("stderr", second) // 超出剩余额度：截断到剩余额度并标记 truncated
	cb.OnLog("stderr", "tail") // 预算耗尽：静默丢弃

	_, events := engine.snapshot()
	var chunks []journaledEvent
	for _, ev := range events {
		if ev.typ == domain.EventRunLogChunk {
			chunks = append(chunks, ev)
		}
	}
	if len(chunks) != 2 {
		t.Fatalf("预算内应落 2 条 log_chunk，实际 %d: %v", len(chunks), events)
	}
	if got, _ := chunks[0].data["chunk"].(string); got != first {
		t.Fatalf("预算内 chunk 应原样落库")
	}
	if truncated, _ := chunks[0].data["truncated"].(bool); truncated {
		t.Fatalf("首条不应标记 truncated: %+v", chunks[0].data)
	}
	if got, _ := chunks[1].data["chunk"].(string); len(got) != observability.LogChunkBudgetBytes-40960 {
		t.Fatalf("截断 chunk 应为剩余额度 %d 字节，实际 %d", observability.LogChunkBudgetBytes-40960, len(got))
	}
	if truncated, _ := chunks[1].data["truncated"].(bool); !truncated {
		t.Fatalf("截断 chunk 应标记 truncated: %+v", chunks[1].data)
	}
	if stream, _ := chunks[0].data["stream"].(string); stream != "stderr" {
		t.Fatalf("log_chunk 应携带 stream: %+v", chunks[0].data)
	}
	// OnLog 不触发 markRunning（原始输出不是活动信号）。
	if got := engine.status(); got != domain.RunQueued {
		t.Fatalf("OnLog 不得推进 Run 状态，实际 %s", got)
	}
}
