package runnergateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// fakeEngine 记录 ingress 映射到应用层的全部调用，供事件入口语义断言。
type fakeEngine struct {
	statuses  []domain.RunStatus
	sessions  []runtime.SessionUpdate
	approvals []*domain.ApprovalRequest // RequestApproval 依次弹出
	run       *domain.ExecutionRun      // Run() 返回值；nil 时返回 running 态
}

func (f *fakeEngine) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	f.statuses = append(f.statuses, to)
	return nil
}

func (f *fakeEngine) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return nil
}

func (f *fakeEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	return nil
}

func (f *fakeEngine) RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error {
	f.sessions = append(f.sessions, update)
	return nil
}

func (f *fakeEngine) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	return nil
}

func (f *fakeEngine) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	if len(f.approvals) == 0 {
		return nil, domain.ErrNotFound
	}
	a := f.approvals[0]
	f.approvals = f.approvals[1:]
	return a, nil
}

func (f *fakeEngine) RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error {
	return nil
}

func (f *fakeEngine) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	if f.run != nil {
		return f.run, nil
	}
	return &domain.ExecutionRun{ID: id, Status: domain.RunRunning}, nil
}

// run.session 的 Clear 墓碑必须走到应用层全量语义：ingress 只映射 ref 时
// 墓碑永远到不了 RecordRunSessionUpdate，task_sessions 锚点不清理。
func TestRunSessionEventClearReachesTombstoneWrite(t *testing.T) {
	engine := &fakeEngine{}
	g := New(nil, engine, nil)

	g.applyEvent(context.Background(), "run_1", "run.session", map[string]any{
		"clear": true, "clear_reason": "session_lost",
	})

	if len(engine.sessions) != 1 {
		t.Fatalf("RecordRunSessionUpdate 调用 %d 次，期望 1", len(engine.sessions))
	}
	got := engine.sessions[0]
	if !got.Clear {
		t.Fatal("Clear 墓碑未被映射到应用层")
	}
	if got.ClearReason != "session_lost" {
		t.Fatalf("ClearReason = %q，期望 session_lost", got.ClearReason)
	}
	if got.Ref != "" {
		t.Fatalf("墓碑帧不应携带 Ref，实际 %q", got.Ref)
	}
}

// adapter 私有 resume 参数（Params）跨进程往返不丢。
func TestRunSessionEventParamsRoundTrip(t *testing.T) {
	engine := &fakeEngine{}
	g := New(nil, engine, nil)

	g.applyEvent(context.Background(), "run_1", "run.session", map[string]any{
		"session_ref": "dsh://sess_9",
		"display_id":  "S-9",
		"params":      map[string]any{"thread_id": "th_1", "gateway_port": float64(3090)},
	})

	if len(engine.sessions) != 1 {
		t.Fatalf("RecordRunSessionUpdate 调用 %d 次，期望 1", len(engine.sessions))
	}
	got := engine.sessions[0]
	if got.Ref != "dsh://sess_9" || got.DisplayID != "S-9" {
		t.Fatalf("ref/display 映射错误: %+v", got)
	}
	if got.Params["thread_id"] != "th_1" || got.Params["gateway_port"] != float64(3090) {
		t.Fatalf("Params 往返丢失: %+v", got.Params)
	}
	if got.Clear {
		t.Fatal("非墓碑帧不应置 Clear")
	}
}

// 旧 runnerd 只发 session_ref/display_id：clear 缺省 false、params 缺省空，行为不变。
func TestRunSessionEventLegacyShapeUnchanged(t *testing.T) {
	engine := &fakeEngine{}
	g := New(nil, engine, nil)

	g.applyEvent(context.Background(), "run_1", "run.session", map[string]any{
		"session_ref": "claude://sess_1", "display_id": "D-1",
	})
	// 空 ref 且无 clear 的帧按历史行为忽略。
	g.applyEvent(context.Background(), "run_1", "run.session", map[string]any{})

	if len(engine.sessions) != 1 {
		t.Fatalf("RecordRunSessionUpdate 调用 %d 次，期望 1（空帧应忽略）", len(engine.sessions))
	}
	got := engine.sessions[0]
	if got.Ref != "claude://sess_1" || got.DisplayID != "D-1" || got.Clear || got.Params != nil {
		t.Fatalf("旧协议形态映射应与历史一致: %+v", got)
	}
}

// readCommandFrame 从连接发送缓冲取一帧 run.command 并解析 body。
func readCommandFrame(t *testing.T, rc *runnerConn) map[string]any {
	t.Helper()
	select {
	case raw := <-rc.send:
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("帧解析失败: %v", err)
		}
		if env.Method != "run.command" {
			t.Fatalf("期望 run.command，实际 %s", env.Method)
		}
		var p struct {
			Command string         `json:"command"`
			Body    map[string]any `json:"body"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("payload 解析失败: %v", err)
		}
		if p.Command != "approval.resolve" {
			t.Fatalf("期望 approval.resolve，实际 %s", p.Command)
		}
		return p.Body
	case <-time.After(2 * time.Second):
		t.Fatal("等待 run.command 超时")
		return nil
	}
}

// 同一 run 的两个并发审批必须各自裁决：单槽映射（runID 一键）会让第二个
// approval.requested 覆盖第一个的翻译，裁决 A 误翻成 B。
func TestConcurrentApprovalsTranslateIndependently(t *testing.T) {
	engine := &fakeEngine{
		approvals: []*domain.ApprovalRequest{
			{ID: "srv_appr_1", RunID: "run_1", Status: domain.ApprovalPending},
			{ID: "srv_appr_2", RunID: "run_1", Status: domain.ApprovalPending},
		},
		// 终态 run：供末段 finalizeIfTerminal 清理断言。
		run: &domain.ExecutionRun{ID: "run_1", Status: domain.RunSucceeded},
	}
	g := New(&fakeLeaseStore{runners: &fakeRunnerRepo{}}, engine, nil)
	rc := &runnerConn{
		gw: g, runnerID: "r1", send: make(chan []byte, 8),
		activeRuns: map[string]string{"run_1": "lease_1"},
	}
	g.mu.Lock()
	g.conns["r1"] = rc
	g.mu.Unlock()
	t.Cleanup(func() {
		g.mu.Lock()
		delete(g.conns, "r1")
		g.mu.Unlock()
	})

	ctx := context.Background()
	g.applyEvent(ctx, "run_1", "approval.requested", map[string]any{
		"kind": "shell", "risk": "high", "summary": "审批 A", "approval_id": "apr_local_1",
	})
	g.applyEvent(ctx, "run_1", "approval.requested", map[string]any{
		"kind": "shell", "risk": "high", "summary": "审批 B", "approval_id": "apr_local_2",
	})

	g.ForwardApproval(ctx, "run_1", "srv_appr_1", true)
	if body := readCommandFrame(t, rc); body["approval_id"] != "apr_local_1" {
		t.Fatalf("裁决审批 1 应翻译为 apr_local_1，实际 %v", body["approval_id"])
	}
	g.ForwardApproval(ctx, "run_1", "srv_appr_2", false)
	if body := readCommandFrame(t, rc); body["approval_id"] != "apr_local_2" {
		t.Fatalf("裁决审批 2 应翻译为 apr_local_2（不得被先前的映射串扰），实际 %v", body["approval_id"])
	}

	// 终态清理整 run 的翻译表。
	g.finalizeIfTerminal("run_1")
	g.mu.Lock()
	_, leaked := g.runnerApprovals["run_1"]
	g.mu.Unlock()
	if leaked {
		t.Fatal("终态后应清理该 run 的全部审批翻译映射")
	}
}
