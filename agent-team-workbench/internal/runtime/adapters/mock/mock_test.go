package mock

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── 直连 Execute 的记录型回调 ────────────────────────────────────────

type approvalCall struct {
	id, kind, risk, summary string
}

type eventCall struct {
	typ  string
	data map[string]any
}

// recordingCallbacks 记录全部回调；onApprovalRequest 在 RequestApproval 返回前触发，
// 用于在审批等待阶段注入取消等异步动作。
type recordingCallbacks struct {
	mu         sync.Mutex
	events     []eventCall
	progress   []float64
	usages     []runtime.Usage
	sessions   []runtime.SessionUpdate
	approvals  []approvalCall
	logs       [][2]string
	nextID     int
	onApproval func(id string)
}

func (c *recordingCallbacks) OnEvent(eventType string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, eventCall{typ: eventType, data: data})
}

func (c *recordingCallbacks) OnProgress(progress float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.progress = append(c.progress, progress)
}

func (c *recordingCallbacks) OnLog(stream, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, [2]string{stream, line})
}

func (c *recordingCallbacks) OnSpawn(pid, processGroupID int) {}

func (c *recordingCallbacks) OnUsage(u runtime.Usage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usages = append(c.usages, u)
}

func (c *recordingCallbacks) OnSession(update runtime.SessionUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, update)
}

func (c *recordingCallbacks) RequestApproval(kind, risk, summary string) string {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("approval_%d", c.nextID)
	c.approvals = append(c.approvals, approvalCall{id: id, kind: kind, risk: risk, summary: summary})
	hook := c.onApproval
	c.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return id
}

// newExecContext 构造直连 Execute 的上下文（无终态意图；意图是 runtime 包私有
// 注入点，只能经 ModuleRunner.Control 设置，见 runnerSink 场景）。
func newExecContext(ctx context.Context, cb runtime.Callbacks, instruction, sessionRef string,
	controls chan runtime.Control) *runtime.ExecContext {
	return &runtime.ExecContext{
		Ctx: ctx,
		Run: &domain.ExecutionRun{
			ID: "run_test", WorkspaceID: "ws_test", Status: domain.RunQueued,
			AdapterID: "mock", Version: 1,
			Input: map[string]any{"instruction": instruction},
		},
		Instruction: instruction,
		Session:     runtime.SessionState{Ref: sessionRef},
		Callbacks:   cb,
		Controls:    controls,
	}
}

func TestManifestCapabilities(t *testing.T) {
	m, err := New().Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.AdapterID != "mock" || m.AdapterVersion != "1.0.0" || m.ProviderVersion != "simulated" {
		t.Fatalf("manifest 身份字段与旧版不一致: %+v", m)
	}
	if m.Protocol != (runtime.Protocol{Name: "mock", Version: "1"}) {
		t.Fatalf("protocol 不一致: %+v", m.Protocol)
	}
	if m.SchemaDigest != "sha256:mock" {
		t.Fatalf("schema_digest 不一致: %s", m.SchemaDigest)
	}
	want := map[string]runtime.CapabilityLevel{
		"streaming": runtime.CapSupported, "resume": runtime.CapSupported,
		"interrupt": runtime.CapSupported, "approval": runtime.CapSupported,
		"workspace_files": runtime.CapSupported, "terminal": runtime.CapUnavailable,
		"structured_output": runtime.CapAdapterTranslated,
	}
	for k, v := range want {
		if got := m.Capabilities[k]; got != v {
			t.Fatalf("能力 %s = %s，期望 %s（键值不得偏离旧版）", k, got, v)
		}
	}
	if len(m.Capabilities) != len(want) {
		t.Fatalf("能力键集合偏离旧版: %+v", m.Capabilities)
	}
}

func TestProbe(t *testing.T) {
	p, err := New().Probe(context.Background(), runtime.ProbeRequest{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.OK || p.Manifest == nil || p.Manifest.AdapterID != "mock" {
		t.Fatalf("probe 结果异常: %+v", p)
	}
}

// TestExecuteSuccess 覆盖新会话创建 ref、事件序列与 payload、用量与成功终态。
func TestExecuteSuccess(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := NewWithStep(time.Millisecond).
		Execute(newExecContext(ctx, cb, "分析这个任务", "", controls))

	if result.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，实际 %s（failure=%+v）", result.Outcome, result.Failure)
	}
	if result.Failure != nil {
		t.Fatalf("成功结果不得携带 failure: %+v", result.Failure)
	}

	// 新会话 ref：mock://mock_atw_<ulid>，早期 OnSession 与 ExecResult 各带一份。
	wantPrefix := scheme + "://" + sessionPrefix
	if !strings.HasPrefix(result.Session.Ref, wantPrefix) {
		t.Fatalf("新会话 ref 期望前缀 %s，实际 %s", wantPrefix, result.Session.Ref)
	}
	if len(cb.sessions) != 1 {
		t.Fatalf("期望 Execute 早期 OnSession 恰好一次，实际 %d 次", len(cb.sessions))
	}
	if cb.sessions[0].Ref != result.Session.Ref {
		t.Fatalf("OnSession 与 ExecResult 的 ref 不一致: %s vs %s", cb.sessions[0].Ref, result.Session.Ref)
	}
	if resumed, _ := cb.sessions[0].Params["resumed"].(bool); resumed {
		t.Fatal("新会话不应标记 resumed")
	}

	// 事件序列与 payload 逐字段一致（前端契约）。
	wantEvents := []eventCall{
		{domain.EventMessageDelta, map[string]any{"role": "assistant", "text": "runtime 正在初始化"}},
		{domain.EventMessageDelta, map[string]any{"role": "assistant", "text": "开始分析任务要求"}},
		{domain.EventMessageDelta, map[string]any{"role": "assistant", "text": "正在生成实现方案"}},
		{domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": "任务执行完成，产物已生成"}},
		{domain.EventArtifactCreated, map[string]any{
			"logical_path": "output/result.md", "mime": "text/markdown",
			"size": 2048, "sha256": strings.Repeat("a", 64),
			"classification": "internal", "status": string(domain.ArtifactDraft),
		}},
	}
	if len(cb.events) != len(wantEvents) {
		t.Fatalf("事件序列长度 %d != %d: %+v", len(cb.events), len(wantEvents), cb.events)
	}
	for i, want := range wantEvents {
		if cb.events[i].typ != want.typ {
			t.Fatalf("事件 %d 类型 %s != %s", i, cb.events[i].typ, want.typ)
		}
		for k, v := range want.data {
			if got, ok := cb.events[i].data[k]; !ok || got != v {
				t.Fatalf("事件 %s 字段 %s = %v，期望 %v", want.typ, k, got, v)
			}
		}
	}

	wantProgress := []float64{0, 0.2, 0.55, 1.0}
	if len(cb.progress) != len(wantProgress) {
		t.Fatalf("进度序列 %+v != %+v", cb.progress, wantProgress)
	}
	for i, want := range wantProgress {
		if cb.progress[i] != want {
			t.Fatalf("进度 %d = %v，期望 %v", i, cb.progress[i], want)
		}
	}

	// 用量：per_run 口径进入 ExecResult。
	if result.Usage == nil {
		t.Fatal("成功结果必须携带 usage")
	}
	if result.Usage.Basis != runtime.UsagePerRun {
		t.Fatalf("usage 口径 %s != per_run", result.Usage.Basis)
	}
	if result.Usage.InputTokens <= 0 || result.Usage.OutputTokens <= 0 {
		t.Fatalf("模拟用量异常: %+v", result.Usage)
	}
	if len(cb.approvals) != 0 {
		t.Fatalf("无审批指令不应发起审批: %+v", cb.approvals)
	}
}

// TestExecuteResume 覆盖 resume：ref 可解析时复用同一会话 id。
func TestExecuteResume(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ref := "mock://mock_atw_resume42"
	result := NewWithStep(time.Millisecond).
		Execute(newExecContext(ctx, cb, "继续上次的任务", ref, controls))

	if result.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，实际 %s", result.Outcome)
	}
	if result.Session.Ref != ref {
		t.Fatalf("resume 应复用原 ref %s，实际 %s", ref, result.Session.Ref)
	}
	if cb.sessions[0].Ref != ref {
		t.Fatalf("OnSession 应复用原 ref，实际 %s", cb.sessions[0].Ref)
	}
	if resumed, _ := cb.sessions[0].Params["resumed"].(bool); !resumed {
		t.Fatal("resume 会话应标记 resumed=true")
	}
}

// TestExecuteApproval 覆盖审批两条分支与等待期关停。
func TestExecuteApproval(t *testing.T) {
	cases := []struct {
		name        string
		approved    *bool // nil 表示不投递决定，在等待期取消 Ctx（模拟关停）
		wantOutcome runtime.Outcome
	}{
		{
			name:        "批准后继续执行",
			approved:    boolPtr(true),
			wantOutcome: runtime.OutcomeSucceeded,
		},
		{
			name:        "拒绝确认取消终态",
			approved:    boolPtr(false),
			wantOutcome: runtime.OutcomeCancelled,
		},
		{
			name:        "等待期关停（无终态意图）",
			approved:    nil,
			wantOutcome: runtime.OutcomeInterrupted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cb := &recordingCallbacks{}
			controls := make(chan runtime.Control, 4)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// 决定必须在 RequestApproval 返回后（模块进入 awaitApproval 前）投递：
			// 借 onApproval 钩子在该时机写入 buffered controls，避免被 pause 提前消费。
			if tc.approved != nil {
				cb.onApproval = func(id string) {
					controls <- runtime.Control{Kind: runtime.ControlApproval, ApprovalID: id, Approved: *tc.approved}
				}
			} else {
				cb.onApproval = func(string) { cancel() }
			}
			result := NewWithStep(time.Millisecond).
				Execute(newExecContext(ctx, cb, "执行前需要审批", "", controls))

			if result.Outcome != tc.wantOutcome {
				t.Fatalf("期望 %s，实际 %s", tc.wantOutcome, result.Outcome)
			}
			if len(cb.approvals) != 1 {
				t.Fatalf("应发起一次审批，实际 %d", len(cb.approvals))
			}
			got := cb.approvals[0]
			if got.kind != "shell" || got.risk != "high" || got.summary != "准备执行模拟发布命令（Mock）" {
				t.Fatalf("审批参数偏离旧契约: %+v", got)
			}
			// 任何路径都必须已经拿到会话句柄（resume 时机不丢）。
			if result.Session == nil || !strings.HasPrefix(result.Session.Ref, "mock://") {
				t.Fatalf("结果应携带会话 ref: %+v", result.Session)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

// TestExecuteShutdownWithoutIntent：无终态意图的 Ctx 取消（服务关停）保守按 interrupted。
func TestExecuteShutdownWithoutIntent(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewWithStep(time.Millisecond).
		Execute(newExecContext(ctx, cb, "普通任务", "", controls))

	if result.Outcome != runtime.OutcomeInterrupted {
		t.Fatalf("期望 interrupted，实际 %s", result.Outcome)
	}
	if result.Session == nil || result.Session.Ref == "" {
		t.Fatal("关停路径也必须携带会话 ref（resume 时机不丢）")
	}
	if len(cb.sessions) == 0 {
		t.Fatal("关停前应已通过 OnSession 上报会话句柄")
	}
	if result.Usage != nil {
		t.Fatalf("中断轮次不应携带 usage: %+v", result.Usage)
	}
}

// TestExecuteSteeringIgnored：mock 未声明 steering，控制输入不得改变模拟节奏与结果。
func TestExecuteSteeringIgnored(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	controls <- runtime.Control{Kind: runtime.ControlInput, Instruction: "中途转向"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := NewWithStep(time.Millisecond).
		Execute(newExecContext(ctx, cb, "普通任务", "", controls))
	if result.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("steering 输入不应中断 mock：实际 %s", result.Outcome)
	}
}

// ── 经真实 ModuleRunner 驱动的终态意图场景 ──────────────────────────

// runnerSink 是驱动真实 ModuleRunner 所需的最小 EngineSink。终态意图
// （TerminalIntent）的注入点 intentSource 是 runtime 包私有接口，包外无法伪造，
// cancel/intent 意图只能经 ModuleRunner.Control 设置，因此走本链路验证。
type runnerSink struct {
	mu          sync.Mutex
	run         *domain.ExecutionRun
	statuses    []domain.RunStatus
	sessions    []runtime.SessionUpdate
	usages      []runtime.Usage
	runningSeen bool
	terminal    chan struct{}
}

func newRunnerSink(run *domain.ExecutionRun) *runnerSink {
	return &runnerSink{run: run, terminal: make(chan struct{})}
}

func (s *runnerSink) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.run.Transition(to, time.Now().UTC()); err != nil {
		return err
	}
	s.statuses = append(s.statuses, to)
	if to == domain.RunRunning {
		s.runningSeen = true
	}
	if to.IsTerminal() {
		close(s.terminal)
	}
	return nil
}

func (s *runnerSink) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return nil
}

func (s *runnerSink) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	return nil
}

func (s *runnerSink) RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, update)
	return nil
}

func (s *runnerSink) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usages = append(s.usages, usage)
	return nil
}

func (s *runnerSink) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := &domain.ApprovalRequest{
		ID: "approval_runner_1", RunID: runID, Kind: kind, Risk: risk,
		Status: domain.ApprovalPending, Summary: summary,
	}
	if err := s.run.Transition(domain.RunWaitingApproval, time.Now().UTC()); err == nil {
		s.statuses = append(s.statuses, domain.RunWaitingApproval)
	}
	return a, nil
}

func (s *runnerSink) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *s.run
	return &copied, nil
}

func (s *runnerSink) waitRunning(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		seen := s.runningSeen
		s.mu.Unlock()
		if seen {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("Run 未进入 running")
}

// setPending 模拟 application 语义：running 后进入 cancelling / interrupting。
func (s *runnerSink) setPending(t *testing.T, to domain.RunStatus) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.run.Transition(to, time.Now().UTC()); err != nil {
		t.Fatalf("running→%s 应合法: %v", to, err)
	}
	s.statuses = append(s.statuses, to)
}

func (s *runnerSink) waitTerminal(t *testing.T, want domain.RunStatus) {
	t.Helper()
	select {
	case <-s.terminal:
	case <-time.After(10 * time.Second):
		t.Fatal("Run 未到达终态")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.Status != want {
		t.Fatalf("终态 %s != 期望 %s（statuses=%v）", s.run.Status, want, s.statuses)
	}
}

// TestExecuteTerminalIntentViaRunner：runner.Control(cancel/interrupt) →
// Ctx 取消 + TerminalIntent → 模块返回对应终态。
func TestExecuteTerminalIntentViaRunner(t *testing.T) {
	cases := []struct {
		name     string
		pending  domain.RunStatus // application 侧先行状态
		control  domain.RunStatus // runner.Control 参数（决定意图）
		terminal domain.RunStatus // 期望终态
	}{
		{"取消意图→cancelled", domain.RunCancelling, domain.RunCancelled, domain.RunCancelled},
		{"中断意图→interrupted", domain.RunInterrupting, domain.RunInterrupted, domain.RunInterrupted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &domain.ExecutionRun{
				ID: domain.NewID(domain.PrefixRun), WorkspaceID: "ws_runner", Status: domain.RunQueued,
				AdapterID: "mock", Version: 1, Input: map[string]any{"instruction": "runner 终态意图场景"},
			}
			sink := newRunnerSink(run)
			runner := runtime.NewModuleRunner(sink)
			runner.Register("mock", NewWithStep(50*time.Millisecond))
			if err := runner.Dispatch(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			sink.waitRunning(t)
			sink.setPending(t, tc.pending)
			runner.Control(run.ID, tc.control)
			sink.waitTerminal(t, tc.terminal)

			// 中断轮次不产生用量，但会话句柄必须已上报（resume 时机不丢）。
			sink.mu.Lock()
			usages, sessions := len(sink.usages), len(sink.sessions)
			sink.mu.Unlock()
			if usages != 0 {
				t.Fatalf("中断轮次不应记录用量，实际 %d 次", usages)
			}
			if sessions == 0 {
				t.Fatal("中断前应已上报会话句柄")
			}
		})
	}
}
