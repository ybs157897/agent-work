package runtime

import (
	"context"
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
	return &runnerCallbacks{runner: runner, runID: engine.run.ID}
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
