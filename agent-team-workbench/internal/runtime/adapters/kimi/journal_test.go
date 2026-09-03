package kimi

import (
	"path/filepath"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// phaseEvents 抽取指定相位的 run.phase_entered / run.phase_closed 事件（按到达序）。
func phaseEvents(cb *recordCallbacks, phase string) []recordedEvent {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var out []recordedEvent
	for _, ev := range cb.events {
		if ev.Type != domain.EventRunPhaseEntered && ev.Type != domain.EventRunPhaseClosed {
			continue
		}
		if p, _ := ev.Data["phase"].(string); p == phase {
			out = append(out, ev)
		}
	}
	return out
}

// journalSequence 返回相位事件 "entered(spawn)" 形态的交错序列。
func journalSequence(cb *recordCallbacks) []string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var out []string
	for _, ev := range cb.events {
		switch ev.Type {
		case domain.EventRunPhaseEntered, domain.EventRunPhaseClosed:
			p, _ := ev.Data["phase"].(string)
			name := "entered"
			if ev.Type == domain.EventRunPhaseClosed {
				name = "closed"
			}
			out = append(out, name+"("+p+")")
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

// TestExecuteSpawnFailureJournalPhase：坏二进制路径拉起失败必须以
// closed{spawn, failed, failure.code=spawn_failed} 收口（成对语义）。
func TestExecuteSpawnFailureJournalPhase(t *testing.T) {
	a := New(Config{BinPath: filepath.Join(t.TempDir(), "missing-kimi")})
	res, cb := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Code != "spawn_failed" {
		t.Fatalf("期望 spawn_failed 失败，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	spawn := phaseEvents(cb, observability.PhaseSpawn)
	if len(spawn) != 2 {
		t.Fatalf("spawn 相位应恰一对事件: %+v", spawn)
	}
	if spawn[0].Type != domain.EventRunPhaseEntered || spawn[1].Type != domain.EventRunPhaseClosed {
		t.Fatalf("spawn 相位顺序错误: %v", journalSequence(cb))
	}
	if spawn[1].Data["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("spawn closed 应为 failed: %+v", spawn[1].Data)
	}
	failure, _ := spawn[1].Data["failure"].(map[string]any)
	if failure == nil || failure["code"] != "spawn_failed" {
		t.Fatalf("spawn closed failure 应为 spawn_failed: %+v", spawn[1].Data)
	}
	// 拉起失败后不应有任何后续相位事件。
	if seq := journalSequence(cb); len(seq) != 2 {
		t.Fatalf("spawn 失败后不应有后续相位事件: %v", seq)
	}
}

// TestExecuteJournalHappyPathPhases：成功 run 的 spawn/handshake 成对发出；
// kimi CLI 无显式握手帧——会话句柄在收尾 meta 帧确立时握手 ok 收口并开启
// first_event（fresh 轮 session id 在 resume_hint meta 才出现，握手臂到那里）。
func TestExecuteJournalHappyPathPhases(t *testing.T) {
	f := newFakeCLI(t)
	a := newAdapter(t, f.bin)
	res, cb := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	assertOrdered(t, journalSequence(cb),
		"entered(spawn)", "closed(spawn)",
		"entered(handshake)", "closed(handshake)",
		"entered(first_event)")

	spawn := phaseEvents(cb, observability.PhaseSpawn)
	if pid, _ := spawn[1].Data["pid"].(int); pid <= 0 {
		t.Fatalf("spawn closed 应带 pid: %+v", spawn[1].Data)
	}
	handshake := phaseEvents(cb, observability.PhaseHandshake)
	if resume, _ := handshake[0].Data["resume"].(bool); resume {
		t.Fatalf("fresh run 的 handshake entered 不应带 resume: %+v", handshake[0].Data)
	}
	if handshake[1].Data["outcome"] != string(observability.PhaseOK) {
		t.Fatalf("handshake closed 应为 ok: %+v", handshake[1].Data)
	}
}

// TestExecuteJournalResumeMissingSessionUnknown：resume 一个不存在的会话，
// 会话句柄始终未确立、流以 stderr provider 错误终止——handshake 以 failed
// 收口且 failure 携带既有 session_unknown 分类（自愈判定的输入证据）。
func TestExecuteJournalResumeMissingSessionUnknown(t *testing.T) {
	f := newFakeCLI(t)
	f.mode(t, "resume_missing")
	a := newAdapter(t, f.bin)
	res, cb := runExecute(t, a, newRun(nil), atwruntime.SessionState{Ref: "kimi://sess_resume_9"})
	if res.Outcome != atwruntime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Family != atwruntime.FamilySessionUnknown {
		t.Fatalf("期望 session_unknown 失败，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	assertOrdered(t, journalSequence(cb),
		"entered(spawn)", "closed(spawn)",
		"entered(handshake)", "closed(handshake)")

	handshake := phaseEvents(cb, observability.PhaseHandshake)
	if resume, _ := handshake[0].Data["resume"].(bool); !resume {
		t.Fatalf("resume run 的 handshake entered 应带 resume=true: %+v", handshake[0].Data)
	}
	closed := handshake[1]
	if closed.Data["outcome"] != string(observability.PhaseFailed) {
		t.Fatalf("handshake closed 应为 failed: %+v", closed.Data)
	}
	failure, _ := closed.Data["failure"].(map[string]any)
	if failure == nil {
		t.Fatalf("handshake closed 应带 failure: %+v", closed.Data)
	}
	if family, _ := failure["family"].(string); family != string(atwruntime.FamilySessionUnknown) {
		t.Fatalf("handshake 失败分类应为 session_unknown: %+v", failure)
	}
	if retryable, _ := failure["retryable"].(bool); retryable {
		t.Fatalf("session_unknown 不可重试: %+v", failure)
	}
	if _, ms := closed.Data["duration_ms"]; !ms {
		t.Fatalf("handshake closed 应带 duration_ms: %+v", closed.Data)
	}
}
