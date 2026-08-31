// Package conformance 是 Adapter SPI v2 的共同合规套件（协议文档 §9.2）。
// 针对 ModuleRunner + EngineSink 验证：能力声明合法性、状态机权威（全部状态迁移
// 经 RecordRunStatus 且满足 domain runTransitions）、终态后无副作用、取消语义、
// steering 能力门控与审批流。所有模块必须通过同一场景集，不能仅凭 provider
// 名称推断行为（协议文档 §8.2）。
package conformance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/mock"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/scripted"
)

// ── fakeSink：EngineSink 的记录型实现 ────────────────────────────────

type eventRec struct {
	typ  string
	data map[string]any
}

// fakeSink 实现 runtime.EngineSink：所有写入用 domain 状态机权威校验，
// 终态后的写入被拒绝并隔离记录（模拟 application 投影层防线）；
// RequestApproval/decide 模拟 application 侧审批语义
// （发起 → waiting_approval；批准 → running；拒绝 → cancelling）。
type fakeSink struct {
	mu            sync.Mutex
	run           *domain.ExecutionRun
	statuses      []domain.RunStatus
	runningSeen   bool     // 是否观察到 running（run 可能瞬间走完全程，不能轮询瞬时状态）
	illegal       []string // 非法状态迁移（不满足 runTransitions）
	wrongRun      []string // runID 不匹配的写入
	events        []eventRec
	unknownEvents []string
	progress      []float64
	sessions      []runtime.SessionUpdate
	usages        []runtime.Usage
	approvals     map[string]*domain.ApprovalRequest
	afterTerminal []string
	done          chan struct{}
}

func newFakeSink(run *domain.ExecutionRun) *fakeSink {
	return &fakeSink{run: run, approvals: map[string]*domain.ApprovalRequest{}, done: make(chan struct{})}
}

func (f *fakeSink) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if runID != f.run.ID {
		f.wrongRun = append(f.wrongRun, "status:"+runID)
		return domain.ErrNotFound
	}
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "status:"+string(to))
		return domain.ErrTerminalImmutable
	}
	from := f.run.Status
	if err := f.run.Transition(to, time.Now().UTC()); err != nil {
		f.illegal = append(f.illegal, fmt.Sprintf("%s->%s", from, to))
		return err
	}
	f.statuses = append(f.statuses, to)
	if to == domain.RunRunning {
		f.runningSeen = true
	}
	if to.IsTerminal() {
		close(f.done)
	}
	return nil
}

func (f *fakeSink) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if runID != f.run.ID {
		f.wrongRun = append(f.wrongRun, "progress:"+runID)
		return domain.ErrNotFound
	}
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "progress")
		return domain.ErrTerminalImmutable
	}
	f.progress = append(f.progress, progress)
	return nil
}

func (f *fakeSink) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if runID != f.run.ID {
		f.wrongRun = append(f.wrongRun, "event:"+runID)
		return domain.ErrNotFound
	}
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "event:"+evType)
		return domain.ErrTerminalImmutable
	}
	if !domain.IsKnownEventName(evType) {
		f.unknownEvents = append(f.unknownEvents, evType)
	}
	f.events = append(f.events, eventRec{typ: evType, data: data})
	return nil
}

func (f *fakeSink) RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if runID != f.run.ID {
		f.wrongRun = append(f.wrongRun, "session:"+runID)
		return domain.ErrNotFound
	}
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "session:"+update.Ref)
		return domain.ErrTerminalImmutable
	}
	f.sessions = append(f.sessions, update)
	return nil
}

func (f *fakeSink) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if runID != f.run.ID {
		f.wrongRun = append(f.wrongRun, "usage:"+runID)
		return domain.ErrNotFound
	}
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "usage")
		return domain.ErrTerminalImmutable
	}
	f.usages = append(f.usages, usage)
	return nil
}

func (f *fakeSink) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &domain.ApprovalRequest{
		ID: domain.NewID(domain.PrefixApproval), RunID: runID, Kind: kind, Risk: risk,
		Status: domain.ApprovalPending, Summary: summary, CreatedAt: time.Now().UTC(),
	}
	f.approvals[a.ID] = a
	// 模拟 application：发起审批即进入 waiting_approval（runTransitions: running→waiting）。
	from := f.run.Status
	if err := f.run.Transition(domain.RunWaitingApproval, time.Now().UTC()); err != nil {
		f.illegal = append(f.illegal, fmt.Sprintf("approval:%s->waiting_approval", from))
		return nil, err
	}
	f.statuses = append(f.statuses, domain.RunWaitingApproval)
	return a, nil
}

func (f *fakeSink) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.run.ID {
		return nil, domain.ErrNotFound
	}
	copied := *f.run
	return &copied, nil
}

// decide 模拟 application.Service.ResolveApproval：记录决定、迁移状态
// （批准 → running；拒绝 → cancelling），并把决定经 runner 投递给模块。
func (f *fakeSink) decide(runner *runtime.ModuleRunner, runID, approvalID string, approved bool) {
	f.mu.Lock()
	decision := domain.ApprovalRejected
	if approved {
		decision = domain.ApprovalApproved
	}
	if a := f.approvals[approvalID]; a != nil {
		_ = a.Resolve(decision, "tester", "conformance", time.Now().UTC())
	}
	to := domain.RunRunning
	if !approved {
		to = domain.RunCancelling
	}
	from := f.run.Status
	if err := f.run.Transition(to, time.Now().UTC()); err != nil {
		f.illegal = append(f.illegal, fmt.Sprintf("decide:%s->%s", from, to))
	} else {
		f.statuses = append(f.statuses, to)
	}
	f.mu.Unlock()
	_ = runner.ResolveApproval(runID, approvalID, approved)
}

// ── 观测辅助 ─────────────────────────────────────────────────────────

func (f *fakeSink) status() domain.RunStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run.Status
}

// sawRunning 报告状态机是否已经过 running（run 可能瞬间完成，瞬时状态轮询不可靠）。
func (f *fakeSink) sawRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runningSeen
}

func (f *fakeSink) statusesSnapshot() []domain.RunStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.RunStatus(nil), f.statuses...)
}

func (f *fakeSink) pendingApprovalID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.approvals {
		if a.Status == domain.ApprovalPending {
			return a.ID
		}
	}
	return ""
}

func (f *fakeSink) afterTerminalSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.afterTerminal...)
}

func (f *fakeSink) illegalSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.illegal...)
}

func (f *fakeSink) unknownEventsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.unknownEvents...)
}

func (f *fakeSink) dump() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "status=%s statuses=[", f.run.Status)
	for i, s := range f.statuses {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(string(s))
	}
	fmt.Fprintf(&b, "] illegal=%v afterTerminal=%v unknownEvents=%v", f.illegal, f.afterTerminal, f.unknownEvents)
	return b.String()
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string, detail func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	d := ""
	if detail != nil {
		d = detail()
	}
	t.Fatalf("超时：%s %s", msg, d)
}

// ── 场景基建 ─────────────────────────────────────────────────────────

// env 一个场景的独立环境：fakeSink + runner + 直接构造的 run（不依赖数据库）。
type env struct {
	fe     *fakeSink
	runner *runtime.ModuleRunner
	run    *domain.ExecutionRun
}

func newEnv(t *testing.T, adapterID, instruction string, module runtime.AdapterModule) *env {
	run := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: "ws_conformance", Status: domain.RunQueued,
		AdapterID: adapterID, Version: 1,
		Input: map[string]any{
			"instruction":  instruction,
			"conversation": map[string]any{"history": []any{}},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	fe := newFakeSink(run)
	runner := runtime.NewModuleRunner(fe)
	// 执行上下文 fixture：conformance 的执行工作目录由 resolver 以 TempDir 提供
	//（adapter 只读 Resolved.CWD；RFC §5.1.9）。
	root := t.TempDir()
	runner.SetSnapshotResolver(func(ctx context.Context, runID string) (domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error) {
		return domain.ExecutionContextSnapshot{ID: "ctxsnap_conf", SchemaVersion: domain.SnapshotSchemaV1},
			domain.ResolvedExecutionContext{SnapshotID: "ctxsnap_conf", AuthorizedRoot: root, CWD: root, RefKind: domain.RefRoot},
			nil
	})
	runner.Register(adapterID, module)
	return &env{fe: fe, runner: runner, run: run}
}

// dispatch 启动异步执行并等待状态机经过 running（首个活动信号；
// run 可能瞬间完成，因此用 observed 标志而非瞬时状态轮询）。
func (e *env) dispatch(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := e.runner.Dispatch(ctx, e.run); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	waitUntil(t, 10*time.Second, e.fe.sawRunning, "Run 未进入 running", e.fe.dump)
}

// waitTerminal 同步等待 ModuleRunner 异步 goroutine 到达终态。
func (e *env) waitTerminal(t *testing.T) {
	t.Helper()
	select {
	case <-e.fe.done:
	case <-time.After(15 * time.Second):
		t.Fatalf("Run 未到达终态 %s", e.fe.dump())
	}
}

// assertNoIllegal 校验状态序列全部经 RecordRunStatus 且满足 runTransitions。
func (e *env) assertNoIllegal(t *testing.T) {
	t.Helper()
	if got := e.fe.illegalSnapshot(); len(got) > 0 {
		t.Fatalf("存在非法状态迁移（不满足 domain runTransitions）: %v %s", got, e.fe.dump())
	}
	if got := e.fe.wrongRunSnapshot(); len(got) > 0 {
		t.Fatalf("存在 runID 不匹配的写入: %v", got)
	}
	if got := e.fe.unknownEventsSnapshot(); len(got) > 0 {
		t.Fatalf("存在白名单外事件: %v", got)
	}
}

func (f *fakeSink) wrongRunSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.wrongRun...)
}

// ── 公共套件 ─────────────────────────────────────────────────────────

// suiteOpts 公共套件的每 adapter 差异点（默认值适配 mock/scripted）。
type suiteOpts struct {
	// scheme 会话 ref 的 scheme（缺省用 adapterID）。adapterID 与 provider
	// scheme 不一致时必填：claude-code→"claude"、codex-appserver→"codex"。
	scheme string
	// requireUsage 成功终态必须有用量记录；未接用量解析的 adapter
	// （kimi/codexapp，能力不静默捏造）置 false 跳过该断言。
	requireUsage bool
	// beforeHeld held（取消）场景 dispatch 前的前置钩子（如设置回放桩
	// 环境变量）；env 在子测试结束时自动还原。
	beforeHeld func(t *testing.T)
}

// runSuite 对单个模块执行公共场景：fast 快速完成（状态机/终态场景），
// held 长时间运行（取消/门控场景，保证控制窗口充足）。
func runSuite(t *testing.T, adapterID string, fast, held runtime.AdapterModule, opts suiteOpts) {
	if opts.scheme == "" {
		opts.scheme = adapterID
	}
	ctx := context.Background()

	t.Run("ManifestCapabilities", func(t *testing.T) {
		m, err := fast.Manifest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if m.AdapterID != adapterID {
			t.Fatalf("manifest adapter_id %s != 注册 id %s", m.AdapterID, adapterID)
		}
		if m.AdapterVersion == "" || m.SchemaDigest == "" {
			t.Fatalf("manifest 缺少版本或 schema_digest: %+v", m)
		}
		if m.Protocol.Name == "" || m.Protocol.Version == "" {
			t.Fatalf("manifest 缺少 protocol 声明: %+v", m.Protocol)
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

	t.Run("StateMachineAuthority", func(t *testing.T) {
		e := newEnv(t, adapterID, "conformance 状态机场景", fast)
		e.dispatch(t, ctx)
		e.waitTerminal(t)

		if got := e.fe.status(); got != domain.RunSucceeded {
			t.Fatalf("期望 succeeded，实际 %s %s", got, e.fe.dump())
		}
		// 状态只能经 RecordRunStatus 迁移，序列满足 queued→starting→running→…→终态。
		want := []domain.RunStatus{
			domain.RunStarting, domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded,
		}
		if got := e.fe.statusesSnapshot(); !equalStatuses(got, want) {
			t.Fatalf("状态序列 %v != 期望 %v %s", got, want, e.fe.dump())
		}
		e.assertNoIllegal(t)
		if got := e.fe.afterTerminalSnapshot(); len(got) > 0 {
			t.Fatalf("终态后仍有写入: %v", got)
		}
		// 事件全部经 RecordRunEvent 且在白名单内。
		if len(e.fe.events) == 0 {
			t.Fatal("模块应至少回放一个 canonical 事件")
		}
		// 会话句柄：早期 OnSession + ExecResult.Session 各一份，ref 带 scheme。
		if len(e.fe.sessions) < 2 {
			t.Fatalf("期望早期 OnSession 与 ExecResult.Session 共 >=2 次上报，实际 %d", len(e.fe.sessions))
		}
		if want := opts.scheme + "://"; !strings.HasPrefix(e.fe.sessions[0].Ref, want) {
			t.Fatalf("会话 ref 期望前缀 %s，实际 %s", want, e.fe.sessions[0].Ref)
		}
		// 用量经 RecordRunUsage 落账（未声明用量能力的模块跳过）。
		if opts.requireUsage && len(e.fe.usages) == 0 {
			t.Fatal("成功终态应有用量记录")
		}
	})

	t.Run("NoSideEffectsAfterTerminal", func(t *testing.T) {
		e := newEnv(t, adapterID, "conformance 终态后无副作用", fast)
		e.dispatch(t, ctx)
		e.waitTerminal(t)

		// 等 goroutine 尾巴完全跑完再断言。
		time.Sleep(200 * time.Millisecond)
		if got := e.fe.afterTerminalSnapshot(); len(got) > 0 {
			t.Fatalf("终态后仍有事件/状态/会话写入: %v", got)
		}
		// 终态后下达控制命令：active 已清理，必须是 no-op。
		e.runner.Control(e.run.ID, domain.RunCancelled)
		if got := e.fe.status(); got != domain.RunSucceeded {
			t.Fatalf("终态不可被 Control 改写: %s", got)
		}
		// 投影层防线：终态后的直写被状态机拒绝。
		if err := e.fe.RecordRunStatus(ctx, e.run.ID, domain.RunFailed, nil); !errors.Is(err, domain.ErrTerminalImmutable) {
			t.Fatalf("终态后直写应被拒绝，实际 err=%v", err)
		}
		if got := e.fe.status(); got != domain.RunSucceeded {
			t.Fatalf("终态不可逆: %s", got)
		}
	})

	t.Run("CancelSemantics", func(t *testing.T) {
		if opts.beforeHeld != nil {
			opts.beforeHeld(t)
		}
		e := newEnv(t, adapterID, "conformance 取消场景", held)
		e.dispatch(t, ctx)
		// 模拟 application：running 后进入 cancelling（starting 不可取消）。
		if err := e.fe.RecordRunStatus(ctx, e.run.ID, domain.RunCancelling, nil); err != nil {
			t.Fatalf("running→cancelling 应合法: %v", err)
		}
		// Control(cancel) → Ctx 取消 + terminalIntent=cancel → 模块返回 cancelled。
		e.runner.Control(e.run.ID, domain.RunCancelled)
		e.waitTerminal(t)

		if got := e.fe.status(); got != domain.RunCancelled {
			t.Fatalf("期望 cancelled，实际 %s %s", got, e.fe.dump())
		}
		want := []domain.RunStatus{
			domain.RunStarting, domain.RunRunning, domain.RunCancelling, domain.RunCancelled,
		}
		if got := e.fe.statusesSnapshot(); !equalStatuses(got, want) {
			t.Fatalf("取消状态序列 %v != 期望 %v %s", got, want, e.fe.dump())
		}
		e.assertNoIllegal(t)
		if got := e.fe.afterTerminalSnapshot(); len(got) > 0 {
			t.Fatalf("终态后仍有写入: %v", got)
		}
	})
}

func equalStatuses(got, want []domain.RunStatus) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ── steering 能力门控 ────────────────────────────────────────────────

// steeringStub 声明 steering 能力的最小模块：等待 ControlInput 后成功返回。
type steeringStub struct{ inputs chan string }

func (s *steeringStub) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "stub-steering", AdapterVersion: "1.0.0",
		Protocol:     runtime.Protocol{Name: "stub", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{"steering": runtime.CapSupported},
		SchemaDigest: "sha256:stub-steering",
	}, nil
}

func (s *steeringStub) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	return runtime.ProbeResult{OK: true}, nil
}

func (s *steeringStub) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{"role": "assistant", "text": "stub 等待 steering"})
	for {
		select {
		case <-ex.Ctx.Done():
			return runtime.ExecResult{Outcome: runtime.OutcomeInterrupted}
		case c, ok := <-ex.Controls:
			if !ok {
				return runtime.ExecResult{Outcome: runtime.OutcomeFailed}
			}
			if c.Kind == runtime.ControlInput {
				s.inputs <- c.Instruction
				return runtime.ExecResult{Outcome: runtime.OutcomeSucceeded}
			}
		}
	}
}

// TestSteeringCapabilityGate：未声明 steering 的模块 ForwardInput 报
// domain.ErrCapabilityMissing；声明的模块成功收到 ControlInput。
func TestSteeringCapabilityGate(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		adapterID string
		held      runtime.AdapterModule
	}{
		{"mock", mock.NewWithStep(100 * time.Millisecond)},
		{"scripted", scripted.NewWithSteps(
			scripted.Step{Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "长回放"}, DelayMS: 5},
			scripted.Step{Kind: domain.EventMessageCompleted, Data: map[string]any{"role": "assistant", "text": "完成"}, DelayMS: 30_000},
		)},
	}
	for _, tc := range cases {
		t.Run(tc.adapterID+" 未声明 steering", func(t *testing.T) {
			e := newEnv(t, tc.adapterID, "conformance steering 门控", tc.held)
			e.dispatch(t, ctx)
			err := e.runner.ForwardInput(ctx, e.run.ID, "中途转向")
			if !errors.Is(err, domain.ErrCapabilityMissing) {
				t.Fatalf("期望 ErrCapabilityMissing，实际 err=%v", err)
			}
			// 收尾：取消挂起的长回放。
			_ = e.fe.RecordRunStatus(ctx, e.run.ID, domain.RunCancelling, nil)
			e.runner.Control(e.run.ID, domain.RunCancelled)
			e.waitTerminal(t)
			e.assertNoIllegal(t)
		})
	}

	t.Run("声明 steering 的模块成功投递", func(t *testing.T) {
		stub := &steeringStub{inputs: make(chan string, 1)}
		e := newEnv(t, "stub-steering", "conformance steering 投递", stub)
		e.dispatch(t, ctx)
		if err := e.runner.ForwardInput(ctx, e.run.ID, "中途转向"); err != nil {
			t.Fatalf("steering 投递应成功: %v", err)
		}
		select {
		case got := <-stub.inputs:
			if got != "中途转向" {
				t.Fatalf("模块收到的 steering 输入 %q != %q", got, "中途转向")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("模块未收到 ControlInput")
		}
		e.waitTerminal(t)
		if got := e.fe.status(); got != domain.RunSucceeded {
			t.Fatalf("期望 succeeded，实际 %s", got)
		}
		e.assertNoIllegal(t)
	})
}

// ── 审批流 ───────────────────────────────────────────────────────────

// TestApprovalFlow：RequestApproval → waiting_approval → 决定投递
// ControlApproval → 模块恢复/终止 → 终态（mock 声明 approval 能力）。
func TestApprovalFlow(t *testing.T) {
	ctx := context.Background()

	t.Run("批准后恢复并成功", func(t *testing.T) {
		e := newEnv(t, "mock", "conformance 审批场景（批准）", mock.NewWithStep(2*time.Millisecond))
		e.dispatch(t, ctx)
		waitUntil(t, 10*time.Second, func() bool { return e.fe.status() == domain.RunWaitingApproval },
			"Run 未进入 waiting_approval", e.fe.dump)
		approvalID := e.fe.pendingApprovalID()
		if approvalID == "" {
			t.Fatal("应存在 pending 审批")
		}
		e.fe.decide(e.runner, e.run.ID, approvalID, true)
		e.waitTerminal(t)

		if got := e.fe.status(); got != domain.RunSucceeded {
			t.Fatalf("期望 succeeded，实际 %s %s", got, e.fe.dump())
		}
		want := []domain.RunStatus{
			domain.RunStarting, domain.RunRunning, domain.RunWaitingApproval,
			domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded,
		}
		if got := e.fe.statusesSnapshot(); !equalStatuses(got, want) {
			t.Fatalf("审批批准状态序列 %v != 期望 %v %s", got, want, e.fe.dump())
		}
		e.assertNoIllegal(t)
		if got := e.fe.afterTerminalSnapshot(); len(got) > 0 {
			t.Fatalf("终态后仍有写入: %v", got)
		}
	})

	t.Run("拒绝后确认取消终态", func(t *testing.T) {
		e := newEnv(t, "mock", "conformance 审批场景（拒绝）", mock.NewWithStep(2*time.Millisecond))
		e.dispatch(t, ctx)
		waitUntil(t, 10*time.Second, func() bool { return e.fe.status() == domain.RunWaitingApproval },
			"Run 未进入 waiting_approval", e.fe.dump)
		approvalID := e.fe.pendingApprovalID()
		e.fe.decide(e.runner, e.run.ID, approvalID, false)
		e.waitTerminal(t)

		if got := e.fe.status(); got != domain.RunCancelled {
			t.Fatalf("期望 cancelled，实际 %s %s", got, e.fe.dump())
		}
		want := []domain.RunStatus{
			domain.RunStarting, domain.RunRunning, domain.RunWaitingApproval,
			domain.RunCancelling, domain.RunCancelled,
		}
		if got := e.fe.statusesSnapshot(); !equalStatuses(got, want) {
			t.Fatalf("审批拒绝状态序列 %v != 期望 %v %s", got, want, e.fe.dump())
		}
		e.assertNoIllegal(t)
	})

	t.Run("scripted 未声明 approval 能力", func(t *testing.T) {
		m, err := scripted.New().Manifest(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Capabilities["approval"]; got != runtime.CapUnavailable {
			t.Fatalf("scripted approval 能力应为 unavailable，实际 %s", got)
		}
	})
}

// ── 终态后越界回调的投影层防线 ───────────────────────────────────────

// lateEventStub 在 Execute 返回后仍持有 Callbacks，用于验证投影层
// （EngineSink 实现）拒绝终态后的事件写入。
type lateEventStub struct{ kept runtime.Callbacks }

func (l *lateEventStub) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "stub-late", AdapterVersion: "1.0.0",
		Protocol:     runtime.Protocol{Name: "stub", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{},
		SchemaDigest: "sha256:stub-late",
	}, nil
}

func (l *lateEventStub) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	return runtime.ProbeResult{OK: true}, nil
}

func (l *lateEventStub) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{"role": "assistant", "text": "完成"})
	l.kept = ex.Callbacks
	return runtime.ExecResult{Outcome: runtime.OutcomeSucceeded}
}

// TestLateCallbackRejectedAfterTerminal：模块（恶意/缺陷）在终态后再发事件，
// 投影层必须拒绝且终态不可变。
func TestLateCallbackRejectedAfterTerminal(t *testing.T) {
	stub := &lateEventStub{}
	e := newEnv(t, "stub-late", "conformance 越界回调", stub)
	e.dispatch(t, context.Background())
	e.waitTerminal(t)

	stub.kept.OnEvent(domain.EventMessageDelta, map[string]any{"role": "assistant", "text": "迟到事件"})
	stub.kept.OnSession(runtime.SessionUpdate{Ref: "stub://late"})

	if got := e.fe.status(); got != domain.RunSucceeded {
		t.Fatalf("终态不可被迟到回调改写: %s", got)
	}
	if got := e.fe.afterTerminalSnapshot(); len(got) != 2 {
		t.Fatalf("迟到写入应被隔离记录，实际 %v", got)
	}
}

// ── 入口：所有模块通过同一套场景 ─────────────────────────────────────

func TestConformanceMock(t *testing.T) {
	runSuite(t, "mock",
		mock.NewWithStep(2*time.Millisecond),   // fast：全流程约 8ms
		mock.NewWithStep(150*time.Millisecond), // held：为取消/门控留足控制窗口
		suiteOpts{requireUsage: true},
	)
}

func TestConformanceScripted(t *testing.T) {
	runSuite(t, "scripted",
		scripted.NewWithSteps( // fast：覆盖 status no-op 与事件回放
			scripted.Step{Kind: "run.status_changed", Data: map[string]any{"status": "running"}},
			scripted.Step{Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "回放"}, DelayMS: 1},
			scripted.Step{Kind: "run.progress", Data: map[string]any{"progress": 0.5}, DelayMS: 1},
			scripted.Step{Kind: domain.EventMessageCompleted, Data: map[string]any{"role": "assistant", "text": "完成"}, DelayMS: 1},
			scripted.Step{Kind: "run.status_changed", Data: map[string]any{"status": "succeeded"}},
		),
		scripted.NewWithSteps( // held：running 后长时间回放
			scripted.Step{Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "长回放"}, DelayMS: 5},
			scripted.Step{Kind: domain.EventMessageCompleted, Data: map[string]any{"role": "assistant", "text": "完成"}, DelayMS: 30_000},
		),
		suiteOpts{requireUsage: true},
	)
}
