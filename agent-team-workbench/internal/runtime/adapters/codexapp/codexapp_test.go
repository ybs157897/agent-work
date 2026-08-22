package codexapp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type testEngine struct {
	mu        sync.Mutex
	run       *domain.ExecutionRun
	events    []string
	approvals []*domain.ApprovalRequest
}

func newTestEngine() *testEngine {
	return &testEngine{run: &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), Status: domain.RunQueued, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}
}

func (e *testEngine) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	e.mu.Lock()
	err := e.run.Transition(to, time.Now().UTC())
	e.mu.Unlock()
	if err == nil {
		e.mu.Lock()
		e.events = append(e.events, "status:"+string(to))
		e.mu.Unlock()
	}
	return err
}

func (e *testEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, evType)
	return nil
}

func (e *testEngine) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	a := &domain.ApprovalRequest{
		ID: domain.NewID(domain.PrefixApproval), RunID: runID, Kind: kind,
		Risk: risk, Status: domain.ApprovalPending, Summary: summary,
	}
	e.mu.Lock()
	e.approvals = append(e.approvals, a)
	e.mu.Unlock()
	return a, nil
}

func (e *testEngine) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	copied := *e.run
	return &copied, nil
}

func (e *testEngine) status() domain.RunStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.run.Status
}

func (e *testEngine) hasEvent(prefix string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.events {
		if strings.HasPrefix(ev, prefix) {
			return true
		}
	}
	return false
}

func fakeServerPath(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "testdata", "providers", "codex", "fake_server.py")
}

func newTestAdapter(t *testing.T, engine *testEngine) *Adapter {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := fakeServerPath(t)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("fake server 缺失: %v", err)
	}
	return New(Config{
		BinPath: python, Args: []string{script},
		WorkspaceRoot: t.TempDir(), GracePeriod: time.Second,
	}, engine)
}

func waitStatus(t *testing.T, e *testEngine, want domain.RunStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if e.status() == want {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t.Fatalf("等待状态 %s 超时，当前 %s，事件 %v", want, e.run.Status, e.events)
}

func dispatch(t *testing.T, a *Adapter, e *testEngine) {
	t.Helper()
	run := e.run
	run.Input = map[string]any{"instruction": "codex fake run"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

// 协议文档 §9：initialize → thread/start → turn/start；turn/completed 权威终态。
func TestCodexHappyPath(t *testing.T) {
	e := newTestEngine()
	a := newTestAdapter(t, e)
	dispatch(t, a, e)
	waitStatus(t, e, domain.RunSucceeded, 15*time.Second)
	for _, want := range []string{"status:starting", "status:running", "tool.started", "tool.completed", "status:succeeded"} {
		if !e.hasEvent(want) {
			t.Errorf("缺少事件 %s", want)
		}
	}
}

// 协议文档 §9：审批经 item/*/requestApproval 路由到工作台，批准后继续。
func TestCodexApprovalApproved(t *testing.T) {
	t.Setenv("CODEX_FAKE_APPROVAL", "1")
	e := newTestEngine()
	a := newTestAdapter(t, e)
	dispatch(t, a, e)
	// 等待审批请求到达。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		n := len(e.approvals)
		e.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	e.mu.Lock()
	n := len(e.approvals)
	e.mu.Unlock()
	if n == 0 {
		t.Fatal("未收到审批请求")
	}
	a.ResolveApproval(e.run.ID, true)
	waitStatus(t, e, domain.RunSucceeded, 15*time.Second)
}

// 审批拒绝 → 中断映射为 cancelled（running→cancelling→cancelled，不可自动重试）。
func TestCodexApprovalDenied(t *testing.T) {
	t.Setenv("CODEX_FAKE_APPROVAL", "1")
	e := newTestEngine()
	a := newTestAdapter(t, e)
	dispatch(t, a, e)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		n := len(e.approvals)
		e.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	a.ResolveApproval(e.run.ID, false)
	waitStatus(t, e, domain.RunCancelled, 15*time.Second)
}

// turn/completed status=failed → Run failed（fail loud）。
func TestCodexTurnFailed(t *testing.T) {
	t.Setenv("CODEX_FAKE_FAIL", "turn")
	e := newTestEngine()
	a := newTestAdapter(t, e)
	dispatch(t, a, e)
	waitStatus(t, e, domain.RunFailed, 15*time.Second)
}
