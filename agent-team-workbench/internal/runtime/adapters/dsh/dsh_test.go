package dsh

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

// testEngine 与 conformance 一致的记录引擎：用领域状态机校验合法性。
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

func (e *testEngine) forceStatus(s domain.RunStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.run.Transition(s, time.Now().UTC())
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
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "testdata", "providers", "dsh", "fake_server.py")
}

func newTestAdapter(t *testing.T, engine *testEngine) *Adapter {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用，跳过 DSH 适配器测试")
	}
	script := fakeServerPath(t)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("fake server 缺失: %v", err)
	}
	return New(Config{
		BinPath: python, ConfigPath: script,
		WorkspaceRoot: t.TempDir(), SessionRoot: t.TempDir(),
		Model: "fake-model", GracePeriod: time.Second,
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

// 协议文档 §9.2：Streaming/终态权威 —— session.event 投影 + idle 判定轮次结束。
func TestDshHappyPath(t *testing.T) {
	e := newTestEngine()
	a := newTestAdapter(t, e)
	run := e.run
	run.Input = map[string]any{"instruction": "fake dsh happy path"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, domain.RunSucceeded, 15*time.Second)
	for _, want := range []string{"status:starting", "status:running", "tool.started", "tool.completed", "message.completed", "status:succeeding", "status:succeeded"} {
		if !e.hasEvent(want) {
			t.Errorf("缺少事件 %s", want)
		}
	}
}

// 协议文档 §9.1：SDK 无 prompt cancel —— 进程组级终止实现 cancellation。
func TestDshProcessScopedCancel(t *testing.T) {
	t.Setenv("DSH_FAKE_HANG", "1")
	e := newTestEngine()
	a := newTestAdapter(t, e)
	run := e.run
	run.Input = map[string]any{"instruction": "fake dsh hang"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	// 等待子进程启动并接受 prompt（messageId 回执后挂起）。
	time.Sleep(800 * time.Millisecond)
	// 控制平面已先把 Run 置为 cancelling，再由 Adapter 确认终态。
	e.forceStatus(domain.RunCancelling)
	a.Control(run.ID, domain.RunCancelled)
	waitStatus(t, e, domain.RunCancelled, 10*time.Second)
}

// 协议文档 §9.2：Initialize 失败必须 fail loud。
func TestDshInitializeFailLoud(t *testing.T) {
	t.Setenv("DSH_FAKE_FAIL", "init")
	e := newTestEngine()
	a := newTestAdapter(t, e)
	run := e.run
	run.Input = map[string]any{"instruction": "fake dsh init fail"}
	if err := a.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, domain.RunFailed, 10*time.Second)
}
