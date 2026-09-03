package dsh

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// phaseEvents 抽取指定相位的 run.phase_entered / run.phase_closed 事件（按到达序）。
func phaseEvents(cb *recordCallbacks, phase string) []recordedEvent {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var out []recordedEvent
	for _, ev := range cb.events {
		if ev.kind != domain.EventRunPhaseEntered && ev.kind != domain.EventRunPhaseClosed {
			continue
		}
		if p, _ := ev.data["phase"].(string); p == phase {
			out = append(out, ev)
		}
	}
	return out
}

func assertOrdered(t *testing.T, got []string, want ...string) {
	t.Helper()
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("相位序列缺少子序列 %v，实际 %v", want, got)
	}
}

func journalSequence(cb *recordCallbacks) []string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var out []string
	for _, ev := range cb.events {
		switch ev.kind {
		case domain.EventRunPhaseEntered, domain.EventRunPhaseClosed:
			p, _ := ev.data["phase"].(string)
			name := "entered"
			if ev.kind == domain.EventRunPhaseClosed {
				name = "closed"
			}
			out = append(out, name+"("+p+")")
		}
	}
	return out
}

// TestGatewayJournalResumeHitHandshakeOk：resume 命中轮的握手成对发出——
// entered 带 resume 意图，session.history 探测命中后 closed ok{resumed} 并
// 开启 first_event 等待本轮首个真实回调。
func TestGatewayJournalResumeHitHandshakeOk(t *testing.T) {
	f := newFakeGateway(t)
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	res := runExecuteScript(t, g, newTestExec(context.Background(), "dsh://s_known", cb, make(chan runtime.Control, 8)), f,
		func() { pushHappyTurn(f, "s_known", 1, 1) })

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望成功，得到 %s（%+v）", res.Outcome, res.Failure)
	}
	assertOrdered(t, journalSequence(cb), "entered(handshake)", "closed(handshake)", "entered(first_event)")
	handshake := phaseEvents(cb, observability.PhaseHandshake)
	if resume, _ := handshake[0].data["resume"].(bool); !resume {
		t.Fatalf("resume 轮 entered 应带 resume=true: %+v", handshake[0].data)
	}
	if handshake[1].data["outcome"] != string(observability.PhaseOK) {
		t.Fatalf("handshake closed 应为 ok: %+v", handshake[1].data)
	}
	if resumed, _ := handshake[1].data["resumed"].(bool); !resumed {
		t.Fatalf("resume 命中 closed 应带 resumed=true: %+v", handshake[1].data)
	}
}

// TestGatewayJournalResumeMissHandshakeFailed：恢复目标丢失时握手以 failed
// 收口且 failure 携带既有 session_unknown 分类（F1 语义的相位证据面）。
func TestGatewayJournalResumeMissHandshakeFailed(t *testing.T) {
	f := newFakeGateway(t)
	f.handle("session.history", func(payload map[string]any) (any, *rpcWireError) {
		return nil, &rpcWireError{Code: "session-not-found", Message: "no such session"}
	})
	f.handle("session.create", func(payload map[string]any) (any, *rpcWireError) {
		t.Error("resume 未命中不得静默 session.create 降级")
		return map[string]any{"sessionId": "s_reborn"}, nil
	})
	g := newTestGateway(f)
	cb := newRecordCallbacks()

	done := make(chan runtime.ExecResult, 1)
	go func() {
		done <- g.Execute(newTestExec(context.Background(), "dsh://s_gone", cb, make(chan runtime.Control, 8)))
	}()
	var res runtime.ExecResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Execute 超时未返回")
	}
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Family != runtime.FamilySessionUnknown {
		t.Fatalf("期望 session_unknown 失败，得到 %s (%+v)", res.Outcome, res.Failure)
	}

	assertOrdered(t, journalSequence(cb), "entered(handshake)", "closed(handshake)")
	handshake := phaseEvents(cb, observability.PhaseHandshake)
	if resume, _ := handshake[0].data["resume"].(bool); !resume {
		t.Fatalf("entered 应带 resume=true: %+v", handshake[0].data)
	}
	closed := handshake[1]
	if closed.data["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("handshake closed 应为 failed: %+v", closed.data)
	}
	failure, _ := closed.data["failure"].(map[string]any)
	if failure == nil {
		t.Fatalf("handshake closed 应带 failure: %+v", closed.data)
	}
	if family, _ := failure["family"].(string); family != string(runtime.FamilySessionUnknown) {
		t.Fatalf("handshake 失败分类应为 session_unknown: %+v", failure)
	}
}
