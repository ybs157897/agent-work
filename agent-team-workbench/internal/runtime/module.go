package runtime

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ModuleRunner 把 AdapterModule 适配为进程内执行面：实现 application.Dispatcher
// 与运行期控制接口（InputForwarder / ApprovalResolver / Control），
// 负责 Run 状态机推进、事件转发与会话/用量落库——adapter 只表达执行本身。
// Dispatch 前由 SnapshotResolver 解析持久快照与 Host 本地可信执行上下文，
// 构造完整 ExecContext.Execution/Resolved（adapter 只读 Resolved.CWD，
// RFC §5.1.9；未注入 resolver 的测试装配跳过解析）。
type ModuleRunner struct {
	engine EngineSink
	// resolver 由装配层注入：从持久 snapshot + Host registry 产出进程内可信
	// 执行上下文。本地执行只服务 host_local（路由决策在 Dispatcher）。
	resolver SnapshotResolver

	mu      sync.Mutex
	modules map[string]AdapterModule
	active  map[string]*activeModuleRun
}

// SnapshotResolver 按 runID 解析持久快照与 Host 本地可信执行上下文（RFC §4.6）。
type SnapshotResolver func(ctx context.Context, runID string) (domain.ExecutionContextSnapshot, domain.ResolvedExecutionContext, error)

// SetSnapshotResolver 注入执行上下文解析器（启动时一次性）。
func (r *ModuleRunner) SetSnapshotResolver(fn SnapshotResolver) { r.resolver = fn }

// activeModuleRun 一次进行中 Execute 的控制面。
type activeModuleRun struct {
	adapterID string
	cancel    context.CancelFunc
	controls  chan Control

	intentMu sync.Mutex
	intent   ControlKind // "" = 无终态意图
}

func (a *activeModuleRun) setIntent(k ControlKind) {
	a.intentMu.Lock()
	a.intent = k
	a.intentMu.Unlock()
}

func (a *activeModuleRun) terminalIntent() (ControlKind, bool) {
	a.intentMu.Lock()
	defer a.intentMu.Unlock()
	return a.intent, a.intent != ""
}

var _ intentSource = (*activeModuleRun)(nil)

func NewModuleRunner(engine EngineSink) *ModuleRunner {
	return &ModuleRunner{
		engine:  engine,
		modules: make(map[string]AdapterModule),
		active:  make(map[string]*activeModuleRun),
	}
}

func (r *ModuleRunner) Register(adapterID string, module AdapterModule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.modules[adapterID] = module
}

func (r *ModuleRunner) Has(adapterID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.modules[adapterID]
	return ok
}

func (r *ModuleRunner) Module(adapterID string) (AdapterModule, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.modules[adapterID]
	return m, ok
}

// Dispatch 实现 application.Dispatcher：构造 ExecContext 并异步驱动 Execute。
func (r *ModuleRunner) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	r.mu.Lock()
	module, ok := r.modules[run.AdapterID]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("module %q 未注册", run.AdapterID)
	}
	// 执行上下文解析失败 = 不可分派（fail closed，不进 goroutine）：
	// Run 不得在没有通过校验的 Snapshot 的情况下被执行（RFC §5.1.4）。
	var execution domain.ExecutionContextSnapshot
	var resolved domain.ResolvedExecutionContext
	if r.resolver != nil {
		var err error
		execution, resolved, err = r.resolver(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("resolve execution context for run %s: %w", run.ID, err)
		}
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ar := &activeModuleRun{adapterID: run.AdapterID, cancel: cancel, controls: make(chan Control, 8)}
	r.mu.Lock()
	if _, exists := r.active[run.ID]; exists {
		r.mu.Unlock()
		cancel()
		return nil
	}
	r.active[run.ID] = ar
	r.mu.Unlock()

	conversation := ConversationSnapshotOf(run)
	session := SessionState{
		Ref:         conversation.ResumeSessionRef,
		Fingerprint: conversation.ConfigDigest,
	}
	ex := &ExecContext{
		Ctx:         runCtx,
		Run:         run,
		Execution:   execution,
		Resolved:    resolved,
		Instruction: EffectiveInstruction(run),
		Session:     session,
		Callbacks:   &runnerCallbacks{runner: r, runID: run.ID},
		Controls:    ar.controls,
		intent:      ar,
	}
	go r.execute(module, ex, ar)
	return nil
}

// execute 是唯一的状态机推进点：Execute 返回后按 Outcome 映射终态并落会话/用量。
func (r *ModuleRunner) execute(module AdapterModule, ex *ExecContext, ar *activeModuleRun) {
	runID := ex.Run.ID
	defer func() {
		r.mu.Lock()
		delete(r.active, runID)
		r.mu.Unlock()
		ar.cancel()
	}()
	bg := context.Background()
	_ = r.engine.RecordRunStatus(bg, runID, domain.RunStarting, nil)

	var result ExecResult
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("module %s: run %s execute panic: %v", ex.Run.AdapterID, runID, rec)
				result = ExecResult{Outcome: OutcomeFailed, Failure: &Failure{
					Family: FamilyInternal, Code: "execute_panic", Message: fmt.Sprint(rec),
				}}
			}
		}()
		result = module.Execute(ex)
	}()

	if result.Usage != nil {
		_ = r.engine.RecordRunUsage(bg, runID, *result.Usage)
	}
	if result.Session != nil {
		_ = r.engine.RecordRunSessionUpdate(bg, runID, *result.Session)
	}
	r.recordTerminal(bg, ex, result)
}

// recordTerminal Outcome → Run 终态；终态后不再产生任何副作用。
// 补迁移或终态写入被状态机拒绝时回退 RunFailed：任何 Outcome 都必须能落终态，
// Run 绝不因非法迁移卡在非终态（吞错只记日志会让 Run 无从排查地悬置）。
func (r *ModuleRunner) recordTerminal(bg context.Context, ex *ExecContext, result ExecResult) {
	runID := ex.Run.ID
	done := false
	switch result.Outcome {
	case OutcomeSucceeded:
		// 中间态写入尽力而为（Run 已在 succeeding 时被拒属预期）；以终态写入定成败。
		r.status(bg, runID, domain.RunSucceeding, nil)
		done = r.status(bg, runID, domain.RunSucceeded, nil)
	case OutcomeFailed, OutcomeTimedOut:
		data := map[string]any{"retryable": false}
		family := FamilyInternal
		if result.Failure != nil {
			family = result.Failure.Family
			data["code"] = result.Failure.Code
			data["message"] = result.Failure.Message
			data["retryable"] = result.Failure.Retryable
			if !result.Failure.RetryNotBefore.IsZero() {
				data["retry_not_before"] = result.Failure.RetryNotBefore.UTC().Format(time.RFC3339)
			}
		}
		if result.Outcome == OutcomeTimedOut {
			data["code"] = "timed_out"
			family = FamilyTimeout
			data["retryable"] = true
		}
		data["family"] = string(family)
		done = r.status(bg, runID, domain.RunFailed, data)
	case OutcomeCancelled, OutcomeInterrupted:
		// application 语义：Control 前置 cancelling/interrupting 中间态。若 Control
		// 早于该迁移到达（或被跳过），这里补一次中间迁移，避免终态写入被状态机拒绝。
		intermediate := domain.RunCancelling
		target := domain.RunCancelled
		if result.Outcome == OutcomeInterrupted {
			intermediate, target = domain.RunInterrupting, domain.RunInterrupted
		}
		if run, err := r.engine.Run(bg, runID); err == nil {
			// 用户命令与 adapter 结果不同侧（如用户 interrupt、adapter 报 cancel）
			// 语义等价：按当前中间态对齐终态，避免非法迁移。
			if run.Status == domain.RunInterrupting && result.Outcome == OutcomeCancelled {
				target = domain.RunInterrupted
			} else if run.Status == domain.RunCancelling && result.Outcome == OutcomeInterrupted {
				target = domain.RunCancelled
			} else if run.Status != intermediate && !run.Status.IsTerminal() && run.Status.CanTransitionTo(intermediate) {
				r.status(bg, runID, intermediate, nil)
			}
		}
		done = r.status(bg, runID, target, nil)
	default:
		done = r.status(bg, runID, domain.RunFailed, map[string]any{
			"code": "invalid_outcome", "message": string(result.Outcome), "family": string(FamilyInternal),
		})
	}
	if !done {
		// 兜底回退：目标终态迁移被拒（如与用户控制命令竞态）时强制 failed，
		// 保证非终态 Run 不悬置；Run 已是终态时此写同样被拒，属预期（用户意图优先）。
		r.status(bg, runID, domain.RunFailed, map[string]any{
			"code":    "terminal_transition_rejected",
			"message": "outcome " + string(result.Outcome) + " 未能落终态（状态机迁移被拒，回退 failed）",
			"family":  string(FamilyConfig),
		})
	}
}

// status 推进 Run 状态；失败记日志并返回 false（调用方据此决定是否回退）。
func (r *ModuleRunner) status(bg context.Context, runID string, to domain.RunStatus, data map[string]any) bool {
	if err := r.engine.RecordRunStatus(bg, runID, to, data); err != nil {
		log.Printf("module: run %s 状态迁移 %s 失败: %v", runID, to, err)
		return false
	}
	return true
}

// ── 运行期控制（对齐旧 RuntimeHandle/Forwarder 语义）──────────────────

// ForwardInput 把 steering 输入投递给活动 Run 的模块；能力未声明或队列满时报错。
func (r *ModuleRunner) ForwardInput(ctx context.Context, runID, instruction string) error {
	ar, module := r.lookupActive(runID)
	if ar == nil {
		return fmt.Errorf("run %s 不在本进程执行", runID)
	}
	if !r.moduleCapability(ctx, module, "steering") {
		return fmt.Errorf("%w: steering", domain.ErrCapabilityMissing)
	}
	return ar.send(Control{Kind: ControlInput, Instruction: instruction})
}

// ResolveApproval 把审批决定投递给活动 Run 的模块；能力未声明（kimi/claude-code
// 等不消费 ControlApproval，投递只会被缓冲或静默丢弃造成 API 层假成功）或队列满时报错。
func (r *ModuleRunner) ResolveApproval(runID, approvalID string, approved bool) error {
	ar, module := r.lookupActive(runID)
	if ar == nil {
		return fmt.Errorf("run %s 不在本进程执行", runID)
	}
	if !r.moduleCapability(context.Background(), module, "approval") {
		return fmt.Errorf("%w: approval", domain.ErrCapabilityMissing)
	}
	return ar.send(Control{Kind: ControlApproval, ApprovalID: approvalID, Approved: approved})
}

// Control 下达终态意图（interrupt/cancel）并取消 ExecContext。
func (r *ModuleRunner) Control(runID string, terminal domain.RunStatus) {
	ar, _ := r.lookupActive(runID)
	if ar == nil {
		return
	}
	if terminal == domain.RunInterrupted {
		ar.setIntent(ControlInterrupt)
	} else {
		ar.setIntent(ControlCancel)
	}
	ar.cancel()
}

func (a *activeModuleRun) send(c Control) error {
	select {
	case a.controls <- c:
		return nil
	default:
		return fmt.Errorf("control queue full (run busy)")
	}
}

func (r *ModuleRunner) lookupActive(runID string) (*activeModuleRun, AdapterModule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ar := r.active[runID]
	if ar == nil {
		return nil, nil
	}
	return ar, r.modules[ar.adapterID]
}

// moduleCapability 查询 Manifest 能力声明；查询失败按不支持处理。
func (r *ModuleRunner) moduleCapability(ctx context.Context, module AdapterModule, capability string) bool {
	if module == nil {
		return false
	}
	m, err := module.Manifest(ctx)
	if err != nil {
		return false
	}
	return m.Capabilities[capability] == CapSupported
}

// ── 回调适配 ─────────────────────────────────────────────────────────

// runnerCallbacks 把 adapter 回调转发到 EngineSink；首个活动信号触发 running。
type runnerCallbacks struct {
	runner *ModuleRunner
	runID  string
	once   sync.Once
}

func (c *runnerCallbacks) markRunning() {
	c.once.Do(func() {
		_ = c.runner.engine.RecordRunStatus(context.Background(), c.runID, domain.RunRunning, nil)
	})
}

func (c *runnerCallbacks) OnEvent(eventType string, data map[string]any) {
	if domain.IsInternalEventName(eventType) {
		// Run Journal internal 事件（run.phase_* / run.log_chunk）是观测面：
		// 只落 run_events，绝不触发 markRunning——否则 adapter 在 spawn/handshake
		// 区间发的相位事件会把 starting→running 提前，run.started 语义被破坏。
		_ = c.runner.engine.RecordRunEvent(context.Background(), c.runID, eventType, data)
		return
	}
	c.markRunning()
	_ = c.runner.engine.RecordRunEvent(context.Background(), c.runID, eventType, data)
}

func (c *runnerCallbacks) OnProgress(progress float64) {
	c.markRunning()
	_ = c.runner.engine.RecordRunProgress(context.Background(), c.runID, progress)
}

func (c *runnerCallbacks) OnLog(stream, line string) {
	// 进程原始输出暂不进事件流（避免噪声）；M5 日志页接入。
}

func (c *runnerCallbacks) OnSpawn(pid, processGroupID int) {
	c.markRunning()
}

func (c *runnerCallbacks) OnUsage(u Usage) {
	c.markRunning()
	_ = c.runner.engine.RecordRunUsage(context.Background(), c.runID, u)
}

func (c *runnerCallbacks) OnSession(update SessionUpdate) {
	// 会话上报本身即活动信号：与 OnEvent 一致触发 running，
	// 否则零事件空 turn（仅 OnSession + 终态）会因 starting→succeeding 非法迁移卡死。
	c.markRunning()
	_ = c.runner.engine.RecordRunSessionUpdate(context.Background(), c.runID, update)
}

func (c *runnerCallbacks) RequestApproval(kind, risk, summary string) string {
	c.markRunning()
	req, err := c.runner.engine.RequestApproval(context.Background(), c.runID, kind, risk, summary)
	if err != nil || req == nil {
		log.Printf("module: run %s 审批发起失败: %v", c.runID, err)
		return ""
	}
	return req.ID
}

// ── 注册表条目（probe/binding 路径）──────────────────────────────────

// RegisterTo 把模块同时挂到 ModuleRunner（执行面）与 Registry（能力协商面）。
func (r *ModuleRunner) RegisterTo(registry *Registry, adapterID string, m AdapterModule) {
	r.Register(adapterID, m)
	registry.Register(adapterID, &moduleShim{runner: r, adapterID: adapterID})
}

type moduleShim struct {
	runner    *ModuleRunner
	adapterID string
}

func (s *moduleShim) Manifest(ctx context.Context) (AdapterManifest, error) {
	m, ok := s.runner.Module(s.adapterID)
	if !ok {
		return AdapterManifest{}, fmt.Errorf("module %q 未注册", s.adapterID)
	}
	return m.Manifest(ctx)
}

func (s *moduleShim) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	m, ok := s.runner.Module(s.adapterID)
	if !ok {
		return ProbeResult{}, fmt.Errorf("module %q 未注册", s.adapterID)
	}
	return m.Probe(ctx, req)
}
