package runtime

// context_resolution_test.go 回归（任务控制面 RFC §15.3）：ModuleRunner 在
// Dispatch 时经注入的 SnapshotResolver 构造完整 ExecContext.Execution/Resolved；
// 两个不同 Location 的 Run 各自拿到自己的 CWD，绝不串目录；resolver 失败 =
// 拒绝分派（fail closed，不进执行 goroutine）。
import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// cwdCapturingModule 捕获 Execute 观察到的 Resolved.CWD / Execution 快照身份。
type cwdCapturingModule struct {
	mu       sync.Mutex
	adapter  string
	cwd      string
	snapshot string
	done     chan struct{}
}

func (m *cwdCapturingModule) Manifest(ctx context.Context) (AdapterManifest, error) {
	return AdapterManifest{AdapterID: m.adapter, AdapterVersion: "1",
		Protocol:     Protocol{Name: "fake", Version: "1"},
		Capabilities: map[string]CapabilityLevel{}, SchemaDigest: "sha256:fake"}, nil
}

func (m *cwdCapturingModule) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	return ProbeResult{OK: true}, nil
}

func (m *cwdCapturingModule) Execute(ex *ExecContext) ExecResult {
	m.mu.Lock()
	m.cwd = ex.Resolved.CWD
	m.snapshot = ex.Execution.ID
	m.mu.Unlock()
	if m.done != nil {
		close(m.done)
	}
	return ExecResult{Outcome: OutcomeSucceeded}
}

func TestModuleRunnerDispatchInjectsResolvedContext(t *testing.T) {
	// 两个 Workspace / 两个 Location 的 run → resolver 按快照映射到不同 CWD。
	roots := map[string]string{
		"run_ws_a": "/host-private/repo-a",
		"run_ws_b": "/host-private/repo-b",
	}
	newEngine := func(runID string) EngineSink {
		return &statefulEngine{run: &domain.ExecutionRun{
			ID: runID, WorkspaceID: "ws_x", AdapterID: "fake",
			Status: domain.RunQueued, Version: 1,
		}}
	}
	wait := func(t *testing.T, ch chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("Execute 未执行")
		}
	}

	for runID, wantCWD := range roots {
		engine := newEngine(runID)
		runner := NewModuleRunner(engine)
		mod := &cwdCapturingModule{adapter: "fake", done: make(chan struct{})}
		runner.Register("fake", mod)
		runner.SetSnapshotResolver(func(ctx context.Context, id string) (domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
			cwd, ok := roots[id]
			if !ok {
				return domain.ExecutionContextSnapshot{}, domain.ResolvedExecutionContext{}, errors.New("no mount for " + id)
			}
			snap := domain.ExecutionContextSnapshot{ID: "ctxsnap_" + id, SchemaVersion: domain.SnapshotSchemaV1}
			return snap, domain.ResolvedExecutionContext{
				SnapshotID: snap.ID, AuthorizedRoot: cwd, CWD: cwd, RefKind: domain.RefRoot,
			}, nil
		})
		if err := runner.Dispatch(context.Background(), engine.(*statefulEngine).run); err != nil {
			t.Fatal(err)
		}
		wait(t, mod.done)
		if mod.cwd != wantCWD {
			t.Fatalf("run %s cwd = %q, want %q（不同 Workspace 不得串 cwd）", runID, mod.cwd, wantCWD)
		}
		if mod.snapshot != "ctxsnap_"+runID {
			t.Fatalf("run %s Execution.ID = %q（应为持久快照身份）", runID, mod.snapshot)
		}
		// 终态收尾，避免 goroutine 泄漏到下一个用例。
		_ = engine.RecordRunStatus(context.Background(), runID, domain.RunFailed, nil)
	}
}

func TestModuleRunnerDispatchFailsClosedWithoutSnapshot(t *testing.T) {
	engine := &statefulEngine{run: dispatchedRun()}
	runner := NewModuleRunner(engine)
	runner.Register("fake", &fakeModule{outcome: OutcomeSucceeded})
	runner.SetSnapshotResolver(func(ctx context.Context, runID string) (domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
		return domain.ExecutionContextSnapshot{}, domain.ResolvedExecutionContext{}, domain.ErrNotFound
	})
	// 无快照的 Run 永不分派（RFC §5.1.4）：Dispatch 必须同步失败，不留执行。
	if err := runner.Dispatch(context.Background(), engine.run); err == nil {
		t.Fatal("resolver 失败时 Dispatch 应 fail closed")
	}
}
