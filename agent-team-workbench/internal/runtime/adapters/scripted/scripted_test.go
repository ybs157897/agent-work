package scripted

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── 直连 Execute 的记录型回调 ────────────────────────────────────────

type eventCall struct {
	typ  string
	data map[string]any
}

type recordingCallbacks struct {
	events   []eventCall
	progress []float64
	usages   []runtime.Usage
	sessions []runtime.SessionUpdate
	logs     [][2]string
}

func (c *recordingCallbacks) OnEvent(eventType string, data map[string]any) {
	c.events = append(c.events, eventCall{typ: eventType, data: data})
}
func (c *recordingCallbacks) OnProgress(progress float64) { c.progress = append(c.progress, progress) }
func (c *recordingCallbacks) OnLog(stream, line string) {
	c.logs = append(c.logs, [2]string{stream, line})
}
func (c *recordingCallbacks) OnSpawn(pid, processGroupID int) {}
func (c *recordingCallbacks) OnUsage(u runtime.Usage)         { c.usages = append(c.usages, u) }
func (c *recordingCallbacks) OnSession(update runtime.SessionUpdate) {
	c.sessions = append(c.sessions, update)
}
func (c *recordingCallbacks) RequestApproval(kind, risk, summary string) string { return "" }

// newExecContext 构造直连 Execute 的上下文（无终态意图；意图是 runtime 包私有
// 注入点，只能经 ModuleRunner.Control 设置，见 runnerSink 场景）。
func newExecContext(ctx context.Context, cb runtime.Callbacks, instruction, sessionRef string,
	controls chan runtime.Control) *runtime.ExecContext {
	return &runtime.ExecContext{
		Ctx: ctx,
		Run: &domain.ExecutionRun{
			ID: "run_test", WorkspaceID: "ws_test", Status: domain.RunQueued,
			AdapterID: "scripted", Version: 1,
			Input: map[string]any{"instruction": instruction},
		},
		Instruction: instruction,
		Session:     runtime.SessionState{Ref: sessionRef},
		Callbacks:   cb,
		Controls:    controls,
	}
}

func TestDefaultFixtureLoaded(t *testing.T) {
	a := New()
	if got := len(a.Steps()); got != 10 {
		t.Fatalf("内置 fixture 应含 10 步，实际 %d", got)
	}
	p, err := a.Probe(context.Background(), runtime.ProbeRequest{WorkspaceID: "ws"})
	if err != nil || !p.OK || p.Manifest == nil {
		t.Fatalf("默认 fixture probe 应通过: %+v err=%v", p, err)
	}
	empty := NewWithSteps()
	p, err = empty.Probe(context.Background(), runtime.ProbeRequest{WorkspaceID: "ws"})
	if err != nil {
		t.Fatal(err)
	}
	if p.OK || p.Error != "fixture 为空" {
		t.Fatalf("空脚本 probe 应失败: %+v", p)
	}
}

func TestParseFixtureSkipsBadLines(t *testing.T) {
	raw := []byte("{\"kind\":\"message.delta\",\"data\":{\"text\":\"ok\"}}\nnot-json\n\n{\"kind\":\"run.progress\",\"data\":{\"progress\":0.5}}\n")
	steps := ParseFixture(raw)
	if len(steps) != 2 {
		t.Fatalf("应跳过坏行保留 2 步，实际 %d: %+v", len(steps), steps)
	}
	if steps[0].Kind != domain.EventMessageDelta || steps[1].Kind != "run.progress" {
		t.Fatalf("解析结果异常: %+v", steps)
	}
}

func TestManifestCapabilities(t *testing.T) {
	m, err := New().Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.AdapterID != "scripted" || m.AdapterVersion != "1.0.0" || m.ProviderVersion != "fixture-v1" {
		t.Fatalf("manifest 身份字段与旧版不一致: %+v", m)
	}
	if m.Protocol != (runtime.Protocol{Name: "fixture-replay", Version: "1"}) {
		t.Fatalf("protocol 不一致: %+v", m.Protocol)
	}
	if m.SchemaDigest != "sha256:scripted-fixture" {
		t.Fatalf("schema_digest 不一致: %s", m.SchemaDigest)
	}
	want := map[string]runtime.CapabilityLevel{
		"streaming": runtime.CapSupported, "resume": runtime.CapSupported,
		"interrupt": runtime.CapSupported, "approval": runtime.CapUnavailable,
		"workspace_files": runtime.CapSupported, "terminal": runtime.CapUnavailable,
		"structured_output": runtime.CapSupported,
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

// TestExecuteReplay 覆盖脚本回放的完整映射：事件/进度/用量/会话/日志，
// 以及 run.status_changed no-op 与未知事件忽略。
func TestExecuteReplay(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	a := NewWithSteps(
		Step{Kind: "run.status_changed", Data: map[string]any{"status": "running"}, DelayMS: 1},
		Step{Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "回放中"}, DelayMS: 1},
		Step{Kind: "run.progress", Data: map[string]any{"progress": 0.5}, DelayMS: 1},
		Step{Kind: domain.EventToolStarted, Data: map[string]any{"tool": "render"}, DelayMS: 1},
		Step{Kind: domain.EventToolCompleted, Data: map[string]any{"tool": "render"}, DelayMS: 1},
		Step{Kind: "usage", Data: map[string]any{"input_tokens": 11, "output_tokens": 22, "basis": "session_cumulative"}, DelayMS: 1},
		Step{Kind: "session", Data: map[string]any{"ref": "scripted://side", "params": map[string]any{"note": "脚本内句柄"}}, DelayMS: 1},
		Step{Kind: "log", Data: map[string]any{"stream": "stdout", "line": "hello"}, DelayMS: 1},
		Step{Kind: "message.unknown_event", Data: map[string]any{"x": 1}, DelayMS: 1},
		Step{Kind: domain.EventMessageCompleted, Data: map[string]any{"role": "assistant", "text": "回放完成"}, DelayMS: 1},
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := a.Execute(newExecContext(ctx, cb, "回放任务", "", controls))

	if result.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，实际 %s", result.Outcome)
	}
	wantEvents := []eventCall{
		{domain.EventMessageDelta, map[string]any{"role": "assistant", "text": "回放中"}},
		{domain.EventToolStarted, map[string]any{"tool": "render"}},
		{domain.EventToolCompleted, map[string]any{"tool": "render"}},
		{domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": "回放完成"}},
	}
	if len(cb.events) != len(wantEvents) {
		t.Fatalf("事件序列 %+v != 期望 %+v", cb.events, wantEvents)
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
	if len(cb.progress) != 1 || cb.progress[0] != 0.5 {
		t.Fatalf("进度回放异常: %+v", cb.progress)
	}
	if len(cb.usages) != 1 || cb.usages[0] != (runtime.Usage{InputTokens: 11, OutputTokens: 22, Basis: runtime.UsageSessionCumulative}) {
		t.Fatalf("用量回放异常: %+v", cb.usages)
	}
	// sessions：Execute 早期会话句柄 + 脚本内 session 步骤；ExecResult 再带一份。
	if len(cb.sessions) != 2 {
		t.Fatalf("期望 2 次 OnSession（早期 + 脚本步骤），实际 %d: %+v", len(cb.sessions), cb.sessions)
	}
	if !strings.HasPrefix(cb.sessions[0].Ref, "scripted://") {
		t.Fatalf("早期会话 ref 异常: %s", cb.sessions[0].Ref)
	}
	if cb.sessions[1].Ref != "scripted://side" {
		t.Fatalf("session 步骤 ref 未回放: %+v", cb.sessions[1])
	}
	if cb.sessions[1].Params["note"] != "脚本内句柄" {
		t.Fatalf("session 步骤 params 未回放: %+v", cb.sessions[1])
	}
	if result.Session == nil || !strings.HasPrefix(result.Session.Ref, "scripted://") {
		t.Fatalf("ExecResult 应携带会话 ref: %+v", result.Session)
	}
	if len(cb.logs) != 1 || cb.logs[0] != [2]string{"stdout", "hello"} {
		t.Fatalf("日志回放异常: %+v", cb.logs)
	}
	if result.Usage == nil || result.Usage.Basis != runtime.UsagePerRun {
		t.Fatalf("ExecResult usage 异常: %+v", result.Usage)
	}
}

// TestExecuteResume 覆盖 ref 复用。
func TestExecuteResume(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	ref := "scripted://scripted_atw_prev7"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := NewWithSteps(Step{Kind: domain.EventMessageDelta, Data: map[string]any{"text": "续跑"}}).
		Execute(newExecContext(ctx, cb, "续跑任务", ref, controls))

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

// TestExecuteShutdownWithoutIntent：无终态意图的 Ctx 取消（服务关停）保守按 interrupted。
func TestExecuteShutdownWithoutIntent(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := NewWithSteps(Step{Kind: domain.EventMessageDelta, Data: map[string]any{"text": "x"}, DelayMS: 60 * 1000}).
		Execute(newExecContext(ctx, cb, "长任务", "", controls))
	if result.Outcome != runtime.OutcomeInterrupted {
		t.Fatalf("期望 interrupted，实际 %s", result.Outcome)
	}
	if result.Session == nil || result.Session.Ref == "" {
		t.Fatal("关停路径也必须携带会话 ref")
	}
	if len(cb.events) != 0 {
		t.Fatalf("关停发生在长延迟步骤，不应回放后续事件: %+v", cb.events)
	}
	if result.Usage != nil {
		t.Fatalf("中断轮次不应携带 usage: %+v", result.Usage)
	}
}

// TestExecuteControlsIgnored：scripted 未声明 steering/approval，控制命令不改变回放。
func TestExecuteControlsIgnored(t *testing.T) {
	cb := &recordingCallbacks{}
	controls := make(chan runtime.Control, 4)
	controls <- runtime.Control{Kind: runtime.ControlInput, Instruction: "中途转向"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := NewWithSteps(Step{Kind: domain.EventMessageCompleted, Data: map[string]any{"text": "done"}, DelayMS: 5}).
		Execute(newExecContext(ctx, cb, "任务", "", controls))
	if result.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("控制命令不应中断回放：实际 %s", result.Outcome)
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
	return nil
}

func (s *runnerSink) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usages = append(s.usages, usage)
	return nil
}

func (s *runnerSink) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	return nil, domain.ErrValidation
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
				AdapterID: "scripted", Version: 1, Input: map[string]any{"instruction": "runner 终态意图场景"},
			}
			sink := newRunnerSink(run)
			runner := runtime.NewModuleRunner(sink)
			runner.Register("scripted", NewWithSteps(
				Step{Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "长回放"}, DelayMS: 5},
				Step{Kind: domain.EventMessageCompleted, Data: map[string]any{"role": "assistant", "text": "完成"}, DelayMS: 60 * 1000},
			))
			if err := runner.Dispatch(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			sink.waitRunning(t)
			sink.setPending(t, tc.pending)
			runner.Control(run.ID, tc.control)
			sink.waitTerminal(t, tc.terminal)

			sink.mu.Lock()
			usages := len(sink.usages)
			sink.mu.Unlock()
			if usages != 0 {
				t.Fatalf("中断轮次不应记录用量，实际 %d 次", usages)
			}
		})
	}
}
