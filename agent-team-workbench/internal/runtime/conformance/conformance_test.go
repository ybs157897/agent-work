// Package conformance 是 Adapter 共同合规测试套件（协议文档 §9.2）。
// 所有 Adapter（mock / scripted / 未来的 DSH / Codex / Kimi）必须通过同一场景集：
// 能力声明合法性、Streaming 权威终态、取消语义、终态后无副作用。
package conformance

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/mock"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/scripted"
)

// DispatcherAdapter 是套件对被测 Adapter 的最小要求。
type DispatcherAdapter interface {
	Manifest(ctx context.Context) (runtime.AdapterManifest, error)
	Dispatch(ctx context.Context, run *domain.ExecutionRun) error
}

// makeAdapter 每个场景独立构造 Adapter + 引擎，避免跨场景状态污染。
type makeAdapter func(fe *fakeEngine) DispatcherAdapter

// fakeEngine 记录 Adapter 上报的事件，并用领域状态机校验合法性。
type fakeEngine struct {
	mu            sync.Mutex
	run           *domain.ExecutionRun
	eventLog      []string
	afterTerminal []string
	approvals     []*domain.ApprovalRequest
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{run: &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), Status: domain.RunQueued, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}
}

func (f *fakeEngine) terminal() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run.Status.IsTerminal()
}

func (f *fakeEngine) status() domain.RunStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run.Status
}

func (f *fakeEngine) forceStatus(s domain.RunStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.run.Transition(s, time.Now().UTC())
}

func (f *fakeEngine) record(kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Status.IsTerminal() && !strings.HasPrefix(kind, "status:") {
		f.afterTerminal = append(f.afterTerminal, kind)
		return
	}
	f.eventLog = append(f.eventLog, kind)
}

func (f *fakeEngine) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	f.mu.Lock()
	err := f.run.Transition(to, time.Now().UTC())
	f.mu.Unlock()
	if err == nil {
		f.record("status:" + string(to))
	}
	return err
}

func (f *fakeEngine) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	f.record("progress")
	return nil
}

func (f *fakeEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	f.record(evType)
	return nil
}

func (f *fakeEngine) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	a := &domain.ApprovalRequest{
		ID: domain.NewID(domain.PrefixApproval), RunID: runID, Kind: kind,
		Risk: risk, Status: domain.ApprovalPending, Summary: summary,
	}
	f.mu.Lock()
	f.approvals = append(f.approvals, a)
	f.mu.Unlock()
	f.record("approval.requested")
	return a, nil
}

func (f *fakeEngine) Approvals(ctx context.Context, runID string) ([]*domain.ApprovalRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.approvals, nil
}

func (f *fakeEngine) RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error {
	f.record("artifact")
	return nil
}

func (f *fakeEngine) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := *f.run
	return &copied, nil
}

func (f *fakeEngine) dump() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "status=" + string(f.run.Status) + " events=[" + strings.Join(f.eventLog, ",") + "]"
}

func waitUntil(t *testing.T, cond func() bool, timeout time.Duration, msg string, detail func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	d := ""
	if detail != nil {
		d = detail()
	}
	t.Fatalf("超时：%s %s", msg, d)
}

// runSuite 对单个 Adapter 执行全部合规场景。
func runSuite(t *testing.T, make makeAdapter) {
	ctx := context.Background()

	t.Run("ManifestCapabilities", func(t *testing.T) {
		adapter := make(newFakeEngine())
		m, err := adapter.Manifest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m.SchemaDigest == "" {
			t.Fatal("manifest 必须带 schema_digest")
		}
		valid := map[runtime.CapabilityLevel]bool{
			runtime.CapSupported: true, runtime.CapExperimental: true,
			runtime.CapAdapterTranslated: true, runtime.CapUnavailable: true,
		}
		if len(m.Capabilities) == 0 {
			t.Fatal("能力声明不能为空")
		}
		for k, v := range m.Capabilities {
			if !valid[v] {
				t.Fatalf("能力 %s 使用了非法级别 %s（禁止单一 bool 掩盖限制）", k, v)
			}
		}
	})

	t.Run("StreamingTerminalAuthority", func(t *testing.T) {
		fe := newFakeEngine()
		adapter := make(fe)
		run := fe.run
		run.Input = map[string]any{"instruction": "conformance 无审批场景"}
		if err := adapter.Dispatch(ctx, run); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, fe.terminal, 20*time.Second, "Run 未到达终态", fe.dump)
		if got := fe.status(); got != domain.RunSucceeded {
			t.Fatalf("期望 succeeded，实际 %s", got)
		}
		// 终态后的事件必须被隔离，不得继续投影。
		time.Sleep(300 * time.Millisecond)
		if len(fe.afterTerminal) > 0 {
			t.Fatalf("终态后仍有事件写入: %v", fe.afterTerminal)
		}
	})

	t.Run("CancelSemantics", func(t *testing.T) {
		fe := newFakeEngine()
		adapter := make(fe)
		run := fe.run
		run.Input = map[string]any{"instruction": "conformance 取消场景"}
		if err := adapter.Dispatch(ctx, run); err != nil {
			t.Fatal(err)
		}
		// 状态机仅允许 running 之后进入 cancelling（starting 不可取消）。
		waitUntil(t, func() bool {
			return fe.status() == domain.RunRunning
		}, 10*time.Second, "Run 未进入 running", fe.dump)
		fe.forceStatus(domain.RunCancelling)
		waitUntil(t, fe.terminal, 10*time.Second, "取消未确认终态", fe.dump)
		if got := fe.status(); got != domain.RunCancelled {
			t.Fatalf("期望 cancelled，实际 %s", got)
		}
	})
}

// Adapter 必须通过同一套场景，不能仅凭 provider 名称推断行为（协议文档 §8.2）。
func TestConformanceMock(t *testing.T) {
	runSuite(t, func(fe *fakeEngine) DispatcherAdapter { return mock.New(fe) })
}

func TestConformanceScripted(t *testing.T) {
	runSuite(t, func(fe *fakeEngine) DispatcherAdapter { return scripted.New(fe) })
}
