package claudecode

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
	mu     sync.Mutex
	run    *domain.ExecutionRun
	events []string
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

func fakeCLIPath(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "testdata", "providers", "claude", "fake_cli.py")
}

func newTestAdapter(t *testing.T, engine *testEngine) *Adapter {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := fakeCLIPath(t)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("fake CLI 缺失: %v", err)
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

// 协议文档 §9：stream-json result subtype=success → succeeded。
func TestClaudeHappyPath(t *testing.T) {
	e := newTestEngine()
	a := newTestAdapter(t, e)
	run := e.run
	run.Input = map[string]any{"instruction": "claude fake run"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, domain.RunSucceeded, 15*time.Second)
	for _, want := range []string{"status:starting", "status:running", "message.completed", "status:succeeded"} {
		if !e.hasEvent(want) {
			t.Errorf("缺少事件 %s", want)
		}
	}
}

// result subtype=error_during_execution → failed（fail loud）。
func TestClaudeResultError(t *testing.T) {
	t.Setenv("CLAUDE_FAKE_FAIL", "1")
	e := newTestEngine()
	a := newTestAdapter(t, e)
	run := e.run
	run.Input = map[string]any{"instruction": "claude fake fail"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, domain.RunFailed, 15*time.Second)
}

// 进程组级取消（process_scoped）：cancel → cancelled。
func TestClaudeProcessScopedCancel(t *testing.T) {
	e := newTestEngine()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	// 用挂起脚本模拟长运行：只发 system.init 后 sleep。
	script := filepath.Join(t.TempDir(), "hang.py")
	hang := "import sys,json,time\n" +
		"sys.stdout.write(json.dumps({'type':'system','subtype':'init'})+'\\n');sys.stdout.flush()\n" +
		"time.sleep(300)\n"
	if err := os.WriteFile(script, []byte(hang), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(Config{BinPath: python, Args: []string{script}, WorkspaceRoot: t.TempDir(), GracePeriod: time.Second}, e)
	run := e.run
	run.Input = map[string]any{"instruction": "claude hang"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, domain.RunRunning, 10*time.Second)
	// 控制平面先置 cancelling，再由 Adapter 确认终态。
	e.mu.Lock()
	_ = e.run.Transition(domain.RunCancelling, time.Now().UTC())
	e.mu.Unlock()
	a.Control(run.ID, domain.RunCancelled)
	waitStatus(t, e, domain.RunCancelled, 10*time.Second)
}
