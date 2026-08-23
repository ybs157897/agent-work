package runnergateway

import (
	"context"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// fakeEngine 记录 ingress 映射到应用层的全部调用，供事件入口语义断言。
type fakeEngine struct {
	statuses  []domain.RunStatus
	sessions  []runtime.SessionUpdate
	approvals []*domain.ApprovalRequest // RequestApproval 依次弹出
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
