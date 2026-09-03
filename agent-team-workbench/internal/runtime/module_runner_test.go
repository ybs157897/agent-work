package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// statefulEngine 以真实 domain 状态机承接 RecordRunStatus，
// 使 ModuleRunner 测试与 application 落库行为共享同一迁移规则。
type statefulEngine struct {
	mu          sync.Mutex
	run         *domain.ExecutionRun
	failedData  map[string]any
	statusCalls []domain.RunStatus
	// rejectStatus 命中时 RecordRunStatus 直接报错（模拟竞态下的非法迁移/写失败）。
	rejectStatus map[domain.RunStatus]error
}

func (e *statefulEngine) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.statusCalls = append(e.statusCalls, to)
	if err, ok := e.rejectStatus[to]; ok {
		return err
	}
	if err := e.run.Transition(to, time.Now().UTC()); err != nil {
		return err
	}
	if to == domain.RunFailed {
		e.failedData = data
	}
	return nil
}

func (e *statefulEngine) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return nil
}

func (e *statefulEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	return nil
}

func (e *statefulEngine) RecordRunSessionUpdate(ctx context.Context, runID string, update SessionUpdate) error {
	return nil
}

func (e *statefulEngine) RecordRunUsage(ctx context.Context, runID string, usage Usage) error {
	return nil
}

func (e *statefulEngine) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	return nil, errors.New("not implemented")
}

func (e *statefulEngine) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.run, nil
}

func (e *statefulEngine) status() domain.RunStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.run.Status
}

// waitTerminal 轮询等待 Run 落终态（Dispatch 是异步驱动）。
func (e *statefulEngine) waitTerminal(t *testing.T) domain.RunStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e.status().IsTerminal() {
			return e.status()
		}
		time.Sleep(2 * time.Millisecond)
	}
	return e.status()
}

// fakeModule 可编程 AdapterModule：只发指定回调并返回固定结果。
type fakeModule struct {
	caps        map[string]CapabilityLevel
	onSession   bool
	outcome     Outcome
	executeDone chan struct{}
}

type blockingModule struct {
	mu      sync.Mutex
	started chan struct{}
	release chan struct{}
	count   int
}

func (m *blockingModule) Manifest(context.Context) (AdapterManifest, error) {
	return AdapterManifest{AdapterID: "fake"}, nil
}

func (m *blockingModule) Probe(context.Context, ProbeRequest) (ProbeResult, error) {
	return ProbeResult{OK: true}, nil
}

func (m *blockingModule) Execute(*ExecContext) ExecResult {
	m.mu.Lock()
	m.count++
	if m.count == 1 {
		close(m.started)
	}
	m.mu.Unlock()
	<-m.release
	return ExecResult{Outcome: OutcomeSucceeded}
}

func (m *blockingModule) executions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

func (m *fakeModule) Manifest(ctx context.Context) (AdapterManifest, error) {
	return AdapterManifest{AdapterID: "fake", Capabilities: m.caps}, nil
}

func (m *fakeModule) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	return ProbeResult{OK: true}, nil
}

func (m *fakeModule) Execute(ex *ExecContext) ExecResult {
	defer func() {
		if m.executeDone != nil {
			close(m.executeDone)
		}
	}()
	if m.onSession {
		ex.Callbacks.OnSession(SessionUpdate{Ref: "fake://sess_1"})
	}
	return ExecResult{Outcome: m.outcome}
}

func dispatchedRun() *domain.ExecutionRun {
	return &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: "ws_mod",
		AdapterID: "fake", Status: domain.RunQueued, Version: 1,
	}
}

// TestModuleRunnerZeroEventSessionSucceeds 回归：仅 OnSession + Succeeded 的
// dsh 式空 turn 曾因 OnSession 不触发 running 而 starting→succeeding 非法迁移卡死。
func TestModuleRunnerZeroEventSessionSucceeds(t *testing.T) {
	engine := &statefulEngine{run: dispatchedRun()}
	runner := NewModuleRunner(engine)
	runner.Register("fake", &fakeModule{
		caps:      map[string]CapabilityLevel{"approval": CapSupported},
		onSession: true,
		outcome:   OutcomeSucceeded,
	})
	run := engine.run
	if err := runner.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if got := engine.waitTerminal(t); got != domain.RunSucceeded {
		t.Fatalf("零事件空 turn 应落 succeeded，实际 %s", got)
	}
}

// TestModuleRunnerSilentRunSucceedsFromStarting 回归：无任何回调的 run
// 依赖 starting→succeeding 直达边落 succeeded（不再卡 starting）。
func TestModuleRunnerSilentRunSucceedsFromStarting(t *testing.T) {
	engine := &statefulEngine{run: dispatchedRun()}
	runner := NewModuleRunner(engine)
	runner.Register("fake", &fakeModule{outcome: OutcomeSucceeded})
	if err := runner.Dispatch(context.Background(), engine.run); err != nil {
		t.Fatal(err)
	}
	if got := engine.waitTerminal(t); got != domain.RunSucceeded {
		t.Fatalf("静默 run 应经 starting→succeeding 落 succeeded，实际 %s", got)
	}
}

func TestModuleRunnerDuplicateDispatchUsesOneActiveExecution(t *testing.T) {
	engine := &statefulEngine{run: dispatchedRun()}
	runner := NewModuleRunner(engine)
	module := &blockingModule{started: make(chan struct{}), release: make(chan struct{})}
	runner.Register("fake", module)
	if err := runner.Dispatch(context.Background(), engine.run); err != nil {
		t.Fatal(err)
	}
	select {
	case <-module.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first module execution did not start")
	}
	if err := runner.Dispatch(context.Background(), engine.run); err != nil {
		t.Fatalf("duplicate active dispatch should be an idempotent no-op: %v", err)
	}
	if got := module.executions(); got != 1 {
		t.Fatalf("duplicate dispatch started %d module executions", got)
	}
	close(module.release)
	if got := engine.waitTerminal(t); got != domain.RunSucceeded {
		t.Fatalf("deduplicated Run should still complete normally, got %s", got)
	}
}

// TestRecordTerminalFallsBackToFailed 回归：终态写入被拒（竞态）时曾只 log，
// Run 悬置非终态；现在必须回退 RunFailed（terminal_transition_rejected）绝不卡死。
func TestRecordTerminalFallsBackToFailed(t *testing.T) {
	engine := &statefulEngine{
		run:          dispatchedRun(),
		rejectStatus: map[domain.RunStatus]error{domain.RunCancelled: errors.New("rejected")},
	}
	runner := NewModuleRunner(engine)
	ex := &ExecContext{Run: engine.run}
	// 先推进到 running（execute 正常前置），再让 OutcomeCancelled 的终态写入被拒。
	if err := engine.run.Transition(domain.RunStarting, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := engine.run.Transition(domain.RunRunning, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	runner.recordTerminal(context.Background(), ex, ExecResult{Outcome: OutcomeCancelled})
	if got := engine.status(); got != domain.RunFailed {
		t.Fatalf("终态写入被拒应回退 failed，实际 %s", got)
	}
	engine.mu.Lock()
	code, _ := engine.failedData["code"].(string)
	family, _ := engine.failedData["family"].(string)
	engine.mu.Unlock()
	if code != "terminal_transition_rejected" || family != string(FamilyConfig) {
		t.Fatalf("回退失败原因异常: code=%q family=%q", code, family)
	}
}

// TestRecordTerminalCrossIntermediateOutcome：用户 interrupt（interrupting）而
// adapter 报 OutcomeCancelled 时应落 interrupted（语义等价），而不是非法迁移到 cancelled。
func TestRecordTerminalCrossIntermediateOutcome(t *testing.T) {
	engine := &statefulEngine{run: dispatchedRun()}
	runner := NewModuleRunner(engine)
	run := engine.run
	for _, to := range []domain.RunStatus{domain.RunStarting, domain.RunRunning, domain.RunInterrupting} {
		if err := run.Transition(to, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	runner.recordTerminal(context.Background(), &ExecContext{Run: run}, ExecResult{Outcome: OutcomeCancelled})
	if got := engine.status(); got != domain.RunInterrupted {
		t.Fatalf("interrupting + OutcomeCancelled 应落 interrupted，实际 %s", got)
	}
}

// TestResolveApprovalCapabilityGate 回归：未声明 approval 能力的模块曾直接入队，
// kimi/claude-code 不消费导致 API 层假成功；现在返回 ErrCapabilityMissing。
func TestResolveApprovalCapabilityGate(t *testing.T) {
	engine := &statefulEngine{run: dispatchedRun()}
	runner := NewModuleRunner(engine)
	noApproval := &fakeModule{caps: map[string]CapabilityLevel{"approval": CapUnavailable}}
	runner.Register("fake", noApproval)
	ar := &activeModuleRun{adapterID: "fake", controls: make(chan Control, 8)}
	runner.mu.Lock()
	runner.active["run_gate"] = ar
	runner.mu.Unlock()

	if err := runner.ResolveApproval("run_gate", "approval_1", true); !errors.Is(err, domain.ErrCapabilityMissing) {
		t.Fatalf("未声明 approval 应返回 ErrCapabilityMissing，实际 %v", err)
	}
	select {
	case c := <-ar.controls:
		t.Fatalf("能力未声明时不应投递控制命令: %#v", c)
	default:
	}

	withApproval := &fakeModule{caps: map[string]CapabilityLevel{"approval": CapSupported}}
	runner.Register("fake", withApproval)
	if err := runner.ResolveApproval("run_gate", "approval_1", true); err != nil {
		t.Fatalf("已声明 approval 不应报错: %v", err)
	}
	select {
	case c := <-ar.controls:
		if c.Kind != ControlApproval || c.ApprovalID != "approval_1" || !c.Approved {
			t.Fatalf("审批决定投递异常: %#v", c)
		}
	default:
		t.Fatal("已声明 approval 应投递控制命令")
	}
}

// TestForwardInputCapabilityGate 对照：steering 门控既有行为不回归。
func TestForwardInputCapabilityGate(t *testing.T) {
	engine := &statefulEngine{run: dispatchedRun()}
	runner := NewModuleRunner(engine)
	runner.Register("fake", &fakeModule{caps: map[string]CapabilityLevel{}})
	ar := &activeModuleRun{adapterID: "fake", controls: make(chan Control, 8)}
	runner.mu.Lock()
	runner.active["run_steer"] = ar
	runner.mu.Unlock()
	err := runner.ForwardInput(context.Background(), "run_steer", "继续")
	if err == nil || !strings.Contains(err.Error(), "steering") {
		t.Fatalf("未声明 steering 应报能力缺失: %v", err)
	}
}
