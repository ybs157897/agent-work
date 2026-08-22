package kimi

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
	mu       sync.Mutex
	run      *domain.ExecutionRun
	events   []string
	lastFail map[string]any
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
	if err == nil {
		e.events = append(e.events, "status:"+string(to))
		if to == domain.RunFailed {
			e.lastFail = data
		}
	}
	e.mu.Unlock()
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
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "testdata", "providers", "kimi", "fake_cli.py")
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

// meta → assistant → result → succeeded。
func TestKimiHappyPath(t *testing.T) {
	e := newTestEngine()
	a := newTestAdapter(t, e)
	run := e.run
	run.Input = map[string]any{"instruction": "kimi fake run"}
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

// 真实 CLI 的 provider 错误形态：stderr error: 行 + 流中断 → fail loud。
func TestKimiProviderErrorFailLoud(t *testing.T) {
	t.Setenv("KIMI_FAKE_FAIL", "1")
	e := newTestEngine()
	a := newTestAdapter(t, e)
	run := e.run
	run.Input = map[string]any{"instruction": "kimi fake fail"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, domain.RunFailed, 15*time.Second)
	e.mu.Lock()
	defer e.mu.Unlock()
	msg, _ := e.lastFail["message"].(string)
	if !strings.Contains(msg, "provider.auth_error") {
		t.Fatalf("失败信息应携带 provider 错误，实际 %v", e.lastFail)
	}
}
