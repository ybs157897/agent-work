package codexapp

import (
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// phaseEvents 抽取指定相位的 run.phase_entered / run.phase_closed 事件（按到达序）。
func phaseEvents(cb *recordCallbacks, phase string) []recEvent {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var out []recEvent
	for _, ev := range cb.events {
		if ev.typ != domain.EventRunPhaseEntered && ev.typ != domain.EventRunPhaseClosed {
			continue
		}
		if p, _ := ev.data["phase"].(string); p == phase {
			out = append(out, ev)
		}
	}
	return out
}

// journalTypeSequence 返回相位事件类型与 phase 的交错序列（定位用）。
func journalTypeSequence(cb *recordCallbacks) []string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var out []string
	for _, ev := range cb.events {
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

// assertOrdered 断言 got 中按序包含 want 子序列。
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

// TestExecuteJournalSpawnAndHandshakePhases：完整 run 的 spawn/handshake 相位
// 成对发出、attempt=1 且顺序正确；spawn closed 带 pid/pgid，handshake entered
// 标注 resume 意图、closed 在 thread 建立后携带 resumed；thread 建立后进入
// first_event 等待首个真实回调。
func TestExecuteJournalSpawnAndHandshakePhases(t *testing.T) {
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}

	assertOrdered(t, journalTypeSequence(r.cb),
		"entered(spawn)", "closed(spawn)",
		"entered(handshake)", "closed(handshake)",
		"entered(first_event)")

	spawn := phaseEvents(r.cb, observability.PhaseSpawn)
	if len(spawn) != 2 {
		t.Fatalf("spawn 相位应恰一对事件: %d", len(spawn))
	}
	if attempt, _ := spawn[0].data["attempt"].(int); attempt != 1 {
		t.Fatalf("spawn attempt 应为 1: %+v", spawn[0].data)
	}
	if spawn[1].data["outcome"] != string(observability.PhaseOK) {
		t.Fatalf("spawn closed 应为 ok: %+v", spawn[1].data)
	}
	if pid, _ := spawn[1].data["pid"].(int); pid <= 0 {
		t.Fatalf("spawn closed 应带 pid: %+v", spawn[1].data)
	}
	if pgid, _ := spawn[1].data["pgid"].(int); pgid <= 0 {
		t.Fatalf("spawn closed 应带 pgid: %+v", spawn[1].data)
	}
	if _, ms := spawn[1].data["duration_ms"]; !ms {
		t.Fatalf("spawn closed 应带 duration_ms: %+v", spawn[1].data)
	}

	handshake := phaseEvents(r.cb, observability.PhaseHandshake)
	if len(handshake) != 2 {
		t.Fatalf("handshake 相位应恰一对事件: %d", len(handshake))
	}
	if resume, _ := handshake[0].data["resume"].(bool); resume {
		t.Fatalf("fresh run 的 handshake entered 不应带 resume: %+v", handshake[0].data)
	}
	if handshake[1].data["outcome"] != string(observability.PhaseOK) {
		t.Fatalf("handshake closed 应为 ok: %+v", handshake[1].data)
	}
	if resumed, _ := handshake[1].data["resumed"].(bool); resumed {
		t.Fatalf("fresh run 的 handshake closed 不应带 resumed: %+v", handshake[1].data)
	}

	// settle entered 由 composeResult 裁决入口发出（verdict_branch 可读）；
	// closed 由 ModuleRunner.recordTerminal 补齐（adapter 直测只应见到 entered）。
	settle := phaseEvents(r.cb, observability.PhaseSettle)
	if len(settle) != 1 || settle[0].typ != domain.EventRunPhaseEntered {
		t.Fatalf("adapter 直测应只见 settle entered: %+v", settle)
	}
	if settle[0].data["verdict_branch"] != "turn_completed" {
		t.Fatalf("成功 run 的裁决分支应为 turn_completed: %+v", settle[0].data)
	}
}

// TestExecuteJournalHandshakeFailureSessionUnknown：resume 一个不存在的会话，
// handshake 以 failed 收口且 failure 携带既有 session_unknown 分类——
// 「resume 失败→session_unknown→自愈」的判定证据在相位事件里直接可读。
func TestExecuteJournalHandshakeFailureSessionUnknown(t *testing.T) {
	t.Setenv("CODEX_FAKE_RESUME_NOT_FOUND", "1")
	m := newTestModule(t)
	r := startExecute(t, m, nil, "codex://th_missing", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Family != atwruntime.FamilySessionUnknown {
		t.Fatalf("期望 session_unknown 失败，得到 %s (%+v)", res.Outcome, res.Failure)
	}

	assertOrdered(t, journalTypeSequence(r.cb),
		"entered(spawn)", "closed(spawn)",
		"entered(handshake)", "closed(handshake)")

	handshake := phaseEvents(r.cb, observability.PhaseHandshake)
	if len(handshake) != 2 {
		t.Fatalf("handshake 相位应恰一对事件: %d", len(handshake))
	}
	if resume, _ := handshake[0].data["resume"].(bool); !resume {
		t.Fatalf("resume run 的 handshake entered 应带 resume=true: %+v", handshake[0].data)
	}
	closed := handshake[1]
	if closed.data["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("handshake closed 应为 failed: %+v", closed.data)
	}
	failure, _ := closed.data["failure"].(map[string]any)
	if failure == nil {
		t.Fatalf("handshake closed 应带 failure: %+v", closed.data)
	}
	if family, _ := failure["family"].(string); family != string(atwruntime.FamilySessionUnknown) {
		t.Fatalf("handshake 失败分类应为 session_unknown: %+v", failure)
	}
	if retryable, _ := failure["retryable"].(bool); retryable {
		t.Fatalf("session_unknown 不可重试: %+v", failure)
	}
}
