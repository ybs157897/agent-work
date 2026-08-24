package codexapp

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	runlib "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── 回放桩：假 app-server（testdata/providers/codex/fake_server.py）─────

func fakeServerPath(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runlib.Caller(0)
	return filepath.Join(filepath.Dir(here), "..", "..", "..", "..", "testdata", "providers", "codex", "fake_server.py")
}

func newTestModule(t *testing.T) *Module {
	t.Helper()
	if runlib.GOOS == "windows" {
		t.Skip("fake server 需要 unix 进程语义")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 不可用")
	}
	script := fakeServerPath(t)
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("fake server 缺失: %v", err)
	}
	return New(Config{
		BinPath: python, Args: []string{script},
		WorkspaceRoot: t.TempDir(), GracePeriod: time.Second,
	})
}

func newRun(input map[string]any) *domain.ExecutionRun {
	if input == nil {
		input = map[string]any{}
	}
	if _, ok := input["instruction"]; !ok {
		input["instruction"] = "codex fake run"
	}
	if _, ok := input["conversation"]; !ok {
		input["conversation"] = map[string]any{"history": []any{}}
	}
	return &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), AdapterID: "codex-appserver",
		Status: domain.RunQueued, Version: 1, Input: input,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

// ── 测试桩：Callbacks（审批模式可配置）────────────────────────────────

type recEvent struct {
	typ  string
	data map[string]any
}

type recLog struct {
	stream, line string
}

type recApproval struct {
	id, kind, risk, summary string
}

// approvalMode 决定 RequestApproval 的行为：
//   - ""：发起失败（返回空串，模块应立即回拒）
//   - "approve"/"deny"：投递对应 ControlApproval
//   - "wrong_then_deny"：先投错误 ApprovalID 的批准（不得释放），再投正确拒绝
type recordCallbacks struct {
	ctl          chan atwruntime.Control
	approvalMode string

	mu        sync.Mutex
	events    []recEvent
	logs      []recLog
	sessions  []atwruntime.SessionUpdate
	approvals []recApproval
	spawned   bool
	pid       int
	pgid      int
}

func (c *recordCallbacks) OnEvent(eventType string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[string]any, len(data))
	for k, v := range data {
		cp[k] = v
	}
	c.events = append(c.events, recEvent{typ: eventType, data: cp})
}

func (c *recordCallbacks) OnProgress(progress float64) {}

func (c *recordCallbacks) OnLog(stream, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, recLog{stream: stream, line: line})
}

func (c *recordCallbacks) OnSpawn(pid, processGroupID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spawned, c.pid, c.pgid = true, pid, processGroupID
}

func (c *recordCallbacks) OnUsage(u atwruntime.Usage) {}

func (c *recordCallbacks) OnSession(update atwruntime.SessionUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, update)
}

func (c *recordCallbacks) RequestApproval(kind, risk, summary string) string {
	c.mu.Lock()
	c.approvals = append(c.approvals, recApproval{id: "ap_decide", kind: kind, risk: risk, summary: summary})
	c.mu.Unlock()
	switch c.approvalMode {
	case "approve":
		c.ctl <- atwruntime.Control{Kind: atwruntime.ControlApproval, ApprovalID: "ap_decide", Approved: true}
		return "ap_decide"
	case "deny":
		c.ctl <- atwruntime.Control{Kind: atwruntime.ControlApproval, ApprovalID: "ap_decide", Approved: false}
		return "ap_decide"
	case "wrong_then_deny":
		c.ctl <- atwruntime.Control{Kind: atwruntime.ControlApproval, ApprovalID: "approval_wrong", Approved: true}
		time.Sleep(100 * time.Millisecond)
		c.ctl <- atwruntime.Control{Kind: atwruntime.ControlApproval, ApprovalID: "ap_decide", Approved: false}
		return "ap_decide"
	default:
		return "" // 模拟发起失败
	}
}

func (c *recordCallbacks) eventNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.typ)
	}
	return out
}

func (c *recordCallbacks) findEvent(typ string) (recEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.typ == typ {
			return e, true
		}
	}
	return recEvent{}, false
}

func (c *recordCallbacks) completedText() string {
	if ev, ok := c.findEvent(domain.EventMessageCompleted); ok {
		text, _ := ev.data["text"].(string)
		return text
	}
	return ""
}

func (c *recordCallbacks) sessionRef() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sessions) == 0 {
		return ""
	}
	return c.sessions[len(c.sessions)-1].Ref
}

// isSubsequence 断言 want 中的事件名按顺序出现于 names。
func isSubsequence(names []string, want ...string) bool {
	i := 0
	for _, n := range names {
		if i < len(want) && n == want[i] {
			i++
		}
	}
	return i == len(want)
}

// ── 执行 harness ─────────────────────────────────────────────────────

type execRunner struct {
	m      *Module
	cb     *recordCallbacks
	ctl    chan atwruntime.Control
	cancel context.CancelFunc
	done   chan atwruntime.ExecResult
}

// startExecute 异步启动一轮 Execute；approvalMode 决定审批决定投递方式
// （字段必须在 goroutine 启动前定值，避免数据竞争）。
func startExecute(t *testing.T, m *Module, input map[string]any, sessionRef, approvalMode string) *execRunner {
	t.Helper()
	ctl := make(chan atwruntime.Control, 8)
	ctx, cancel := context.WithCancel(context.Background())
	cb := &recordCallbacks{ctl: ctl, approvalMode: approvalMode}
	run := newRun(input)
	r := &execRunner{m: m, cb: cb, ctl: ctl, cancel: cancel, done: make(chan atwruntime.ExecResult, 1)}
	go func() {
		r.done <- m.Execute(&atwruntime.ExecContext{
			Ctx: ctx, Run: run, Instruction: atwruntime.EffectiveInstruction(run),
			Session:   atwruntime.SessionState{Ref: sessionRef},
			Callbacks: cb, Controls: ctl,
		})
	}()
	return r
}

func (r *execRunner) wait(t *testing.T, timeout time.Duration) atwruntime.ExecResult {
	t.Helper()
	select {
	case res := <-r.done:
		return res
	case <-time.After(timeout):
		t.Fatalf("Execute 超时未返回（事件 %v）", r.cb.eventNames())
		return atwruntime.ExecResult{}
	}
}

func (r *execRunner) waitCond(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("超时：%s（事件 %v）", msg, r.cb.eventNames())
}

func (r *execRunner) waitSpawned(t *testing.T) {
	r.waitCond(t, 10*time.Second, func() bool {
		r.cb.mu.Lock()
		defer r.cb.mu.Unlock()
		return r.cb.spawned
	}, "进程未上报 OnSpawn")
}

func (r *execRunner) waitSession(t *testing.T) string {
	r.waitCond(t, 10*time.Second, func() bool { return r.cb.sessionRef() != "" }, "未收到会话句柄")
	return r.cb.sessionRef()
}

// ── Manifest / Probe ─────────────────────────────────────────────────

func TestModuleManifest(t *testing.T) {
	m := New(Config{})
	mf, err := m.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mf.AdapterID != "codex-appserver" || mf.AdapterVersion != adapterVersion {
		t.Fatalf("manifest 标识错误: %+v", mf)
	}
	if mf.Protocol.Name != "codex-app-server" || mf.Protocol.Version != "v2" {
		t.Fatalf("protocol 声明漂移: %+v", mf.Protocol)
	}
	if mf.SchemaDigest != "sha256:"+protocolSchemaSHA256 {
		t.Fatalf("schema digest 变化: %s", mf.SchemaDigest)
	}
	want := map[string]atwruntime.CapabilityLevel{
		"streaming": atwruntime.CapSupported, "interrupt": atwruntime.CapSupported,
		"resume": atwruntime.CapSupported, "multi_turn": atwruntime.CapSupported,
		"steering": atwruntime.CapSupported, "system_prompt": atwruntime.CapSupported,
		"modes": atwruntime.CapSupported, "permissions": atwruntime.CapAdapterTranslated,
		"approval": atwruntime.CapSupported, "workspace_files": atwruntime.CapSupported,
		"terminal": atwruntime.CapUnavailable, "structured_output": atwruntime.CapAdapterTranslated,
	}
	if len(mf.Capabilities) != len(want) {
		t.Fatalf("能力集漂移: %+v", mf.Capabilities)
	}
	for k, v := range want {
		if mf.Capabilities[k] != v {
			t.Errorf("能力 %s = %s，期望 %s", k, mf.Capabilities[k], v)
		}
	}
}

func TestProbePerformsHandshakeAuthAndModelChecks(t *testing.T) {
	m := newTestModule(t)
	result, err := m.Probe(context.Background(), atwruntime.ProbeRequest{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Manifest == nil || !strings.Contains(result.Manifest.ProviderVersion, "0.149.0-fake") {
		t.Fatalf("probe = %+v", result)
	}
}

func TestProbeRejectsMissingAuth(t *testing.T) {
	t.Setenv("CODEX_FAKE_NO_AUTH", "1")
	m := newTestModule(t)
	result, err := m.Probe(context.Background(), atwruntime.ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || !strings.Contains(result.Error, "尚未配置模型凭据") {
		t.Fatalf("probe should fail auth: %+v", result)
	}
}

// ── Execute：帧→事件映射 / 会话 / 进程上报（协议文档 §9）──────────────

// initialize → thread/start → turn/start；通知流映射 canonical 事件，
// 首个 threadId 经 OnSession 上报 codex://th_fake_1。
func TestExecuteHappyPath(t *testing.T) {
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "")
	res := r.wait(t, 15*time.Second)

	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	if res.Failure != nil {
		t.Fatalf("成功不应带 Failure: %+v", res.Failure)
	}
	// 通知流 → canonical 事件序列（与旧实现一致）。
	if !isSubsequence(r.cb.eventNames(),
		domain.EventMessageDelta, domain.EventToolStarted, domain.EventToolProgress,
		domain.EventToolCompleted, domain.EventMessageDelta, domain.EventMessageCompleted) {
		t.Fatalf("事件序列漂移: %v", r.cb.eventNames())
	}
	ev, ok := r.cb.findEvent(domain.EventMessageCompleted)
	if !ok {
		t.Fatal("缺少 message.completed")
	}
	if ev.data["text"] != "fake codex 输出" || ev.data["role"] != "assistant" || ev.data["item_type"] != "agentMessage" {
		t.Fatalf("message.completed payload 漂移: %+v", ev.data)
	}
	// 工具契约（与 kimiapp 对齐）：started 带 call_id/args_summary（command），
	// completed 带聚合 output 与 exit_code——此前输出被整体丢弃，UI 不可见。
	started, ok := r.cb.findEvent(domain.EventToolStarted)
	if !ok || started.data["tool"] != "shell" || started.data["call_id"] != "it_1" ||
		started.data["args_summary"] != "echo hi" {
		t.Fatalf("tool.started 契约漂移: %+v", started.data)
	}
	toolDone, ok := r.cb.findEvent(domain.EventToolCompleted)
	if !ok || toolDone.data["call_id"] != "it_1" || toolDone.data["output"] != "hi" {
		t.Fatalf("tool.completed 契约漂移: %+v", toolDone.data)
	}
	if ec, ok := toolDone.data["exit_code"].(float64); !ok || ec != 0 {
		t.Fatalf("tool.completed 缺 exit_code: %+v", toolDone.data)
	}
	// 会话句柄：thread/start 响应 → OnSession + ExecResult.Session。
	if ref := r.cb.sessionRef(); ref != "codex://th_fake_1" {
		t.Fatalf("OnSession 未上报 codex://th_fake_1: %q", ref)
	}
	if res.Session == nil || res.Session.Ref != "codex://th_fake_1" {
		t.Fatalf("ExecResult.Session 错误: %+v", res.Session)
	}
	// 进程上报
	r.cb.mu.Lock()
	spawned, pid, pgid := r.cb.spawned, r.cb.pid, r.cb.pgid
	r.cb.mu.Unlock()
	if !spawned || pid <= 0 || pgid <= 0 {
		t.Fatalf("OnSpawn 未正确上报: spawned=%v pid=%d pgid=%d", spawned, pid, pgid)
	}
}

// resume：Session.Ref=codex://th_existing → thread/resume，会话句柄沿用。
func TestExecuteResumesPreviousThread(t *testing.T) {
	t.Setenv("CODEX_EXPECT_RESUME", "1")
	m := newTestModule(t)
	r := startExecute(t, m, map[string]any{"instruction": "第二轮"}, "codex://th_existing", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	if res.Session == nil || res.Session.Ref != "codex://th_existing" {
		t.Fatalf("resume thread 未沿用: %+v", res.Session)
	}
	if ref := r.cb.sessionRef(); ref != "codex://th_existing" {
		t.Fatalf("OnSession 未沿用 resume thread: %q", ref)
	}
}

// plan 模式未指定模型：model/list 发现默认模型后继续 thread/start。
func TestExecutePlanModeDiscoversDefaultModel(t *testing.T) {
	m := newTestModule(t)
	input := map[string]any{
		"instruction": "制定计划",
		"mode":        "plan",
		"policy":      map[string]any{"sandbox": "read-only", "approval_policy": "manual"},
	}
	r := startExecute(t, m, input, "", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
}

// 空 instruction → config 失败，不启动进程。
func TestExecuteEmptyInstruction(t *testing.T) {
	m := newTestModule(t)
	r := startExecute(t, m, map[string]any{"instruction": "  "}, "", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Code != "instruction_required" || res.Failure.Family != atwruntime.FamilyConfig {
		t.Fatalf("空 instruction 应 config/instruction_required: %+v %+v", res.Outcome, res.Failure)
	}
	r.cb.mu.Lock()
	spawned := r.cb.spawned
	r.cb.mu.Unlock()
	if spawned {
		t.Fatal("空 instruction 不应启动进程")
	}
}

// ── Execute：失败分类 ────────────────────────────────────────────────

// turn/completed status=failed → OutcomeFailed，provider 细节不丢失。
func TestExecuteTurnFailed(t *testing.T) {
	t.Setenv("CODEX_FAKE_FAIL", "turn")
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("期望 failed，得到 %s", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Code != "turn_failed" {
		t.Fatalf("失败码错误: %+v", res.Failure)
	}
	if res.Failure.Message != "fixture turn failure" {
		t.Fatalf("provider failure detail lost: %q", res.Failure.Message)
	}
	if res.Failure.Family != atwruntime.FamilyTransientUpstream || !res.Failure.Retryable {
		t.Fatalf("普通 provider 错误应 transient_upstream/可重试: %+v", res.Failure)
	}
}

// codexFailure 分类：quota/429 → provider_quota；auth → config；thread 丢失类
// 文案 → session_unknown（不可重试）；其余 → transient。
func TestCodexFailureFamilies(t *testing.T) {
	cases := []struct {
		message   string
		family    atwruntime.ErrorFamily
		retryable bool
	}{
		{"You exceeded your monthly quota (429 rate limit)", atwruntime.FamilyProviderQuota, true},
		{"Unauthorized: invalid api key (401)", atwruntime.FamilyConfig, false},
		{"Forbidden: 403 login required", atwruntime.FamilyConfig, false},
		{"network error connecting to upstream (503)", atwruntime.FamilyTransientUpstream, true},
		{"fixture turn failure", atwruntime.FamilyTransientUpstream, true},
		// F4：thread/resume 目标丢失 → session_unknown（应用层清锚点自愈）。
		{"thread/resume: Thread not found: th_missing", atwruntime.FamilySessionUnknown, false},
		{"thread/resume: unknown thread id", atwruntime.FamilySessionUnknown, false},
		{"thread/resume: no such thread", atwruntime.FamilySessionUnknown, false},
		{"session not found: sess_x", atwruntime.FamilySessionUnknown, false},
		{"conversation not found", atwruntime.FamilySessionUnknown, false},
		// 防回归：codex 0.149.0 thread/resume 死锚点的真实文案（实测 code -32600），
		// 误归 transient 会让死会话被盲目重试、永远走不到自愈。
		{"no rollout found for thread id th_missing", atwruntime.FamilySessionUnknown, false},
		// 不得用裸 "not found" 误吞无关错误。
		{"method not found: -32601", atwruntime.FamilyTransientUpstream, true},
		{"model not found: gpt-x", atwruntime.FamilyTransientUpstream, true},
	}
	for _, tc := range cases {
		f := codexFailure("turn_failed", tc.message)
		if f.Family != tc.family || f.Retryable != tc.retryable {
			t.Errorf("%q = %+v，期望 family=%s retryable=%v", tc.message, f, tc.family, tc.retryable)
		}
		if !strings.Contains(f.Message, "429") && strings.Contains(tc.message, "429") {
			// quota 场景消息截断不丢关键信息（此处均在 200 字符内）。
			t.Errorf("quota 消息不应丢失 429: %+v", f)
		}
	}
}

// F4：resume 的 thread/resume 返回 not-found 错误 → FamilySessionUnknown/
// 不可重试，交应用层自愈（清锚点 + 全量历史 fresh 重建），不盲目重试。
func TestExecuteResumeNotFoundIsSessionUnknown(t *testing.T) {
	t.Setenv("CODEX_FAKE_RESUME_NOT_FOUND", "1")
	m := newTestModule(t)
	r := startExecute(t, m, map[string]any{"instruction": "第二轮"}, "codex://th_missing", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("期望 failed，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	if res.Failure == nil || res.Failure.Family != atwruntime.FamilySessionUnknown ||
		res.Failure.Retryable || !strings.Contains(res.Failure.Message, "Thread not found") {
		t.Fatalf("thread 丢失应 session_unknown/不可重试: %+v", res.Failure)
	}
	if res.Session != nil {
		t.Fatalf("resume 失败不应发布会话句柄: %+v", res.Session)
	}
}

// CLI 不存在 → FamilyConfig / spawn_failed。
func TestExecuteSpawnFailure(t *testing.T) {
	m := New(Config{BinPath: filepath.Join(t.TempDir(), "missing-codex"), WorkspaceRoot: t.TempDir()})
	r := startExecute(t, m, nil, "", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeFailed || res.Failure == nil {
		t.Fatalf("期望 failed: %+v", res)
	}
	if res.Failure.Code != "spawn_failed" || res.Failure.Family != atwruntime.FamilyConfig {
		t.Fatalf("spawn 失败应 config/spawn_failed: %+v", res.Failure)
	}
}

// ── 审批流：item/*/requestApproval → RequestApproval → ControlApproval ──

// 协议文档 §9：审批经 item/*/requestApproval 路由到工作台，批准后继续。
func TestExecuteApprovalApproved(t *testing.T) {
	t.Setenv("CODEX_FAKE_APPROVAL", "1")
	t.Setenv("CODEX_EXPECT_APPROVED", "1")
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "approve")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	r.cb.mu.Lock()
	approvals := append([]recApproval(nil), r.cb.approvals...)
	r.cb.mu.Unlock()
	if len(approvals) != 1 {
		t.Fatalf("应发起一次审批: %+v", approvals)
	}
	if approvals[0].kind != "command" || approvals[0].risk != "high" ||
		!strings.Contains(approvals[0].summary, "echo high-risk") {
		t.Fatalf("审批元数据漂移: %+v", approvals[0])
	}
	if text := r.cb.completedText(); text != "fake codex 输出" {
		t.Fatalf("批准后应继续输出最终消息: %q", text)
	}
}

// 审批拒绝 → turn/completed(interrupted) → OutcomeCancelled（旧 cancelling 语义）。
func TestExecuteApprovalDenied(t *testing.T) {
	t.Setenv("CODEX_FAKE_APPROVAL", "1")
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "deny")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeCancelled {
		t.Fatalf("期望 cancelled，得到 %s (%+v)", res.Outcome, res.Failure)
	}
}

// ApprovalID 精确匹配：错误 id 的决定不得释放 provider 请求。
func TestExecuteApprovalUsesExactWorkbenchID(t *testing.T) {
	t.Setenv("CODEX_FAKE_APPROVAL", "1")
	m := newTestModule(t)
	// wrong_then_deny：若错误 id 释放了请求（decision=accept），fake 会回
	// invalid approval response 错误 → OutcomeFailed；正确行为是 cancelled。
	r := startExecute(t, m, nil, "", "wrong_then_deny")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeCancelled {
		t.Fatalf("错误 approval id 不得释放 provider 请求，期望 cancelled，得到 %s (%+v)", res.Outcome, res.Failure)
	}
}

// RequestApproval 发起失败（返回空串）→ 立即回拒绝，防止服务端悬挂。
func TestExecuteApprovalBridgeFailureDeniesImmediately(t *testing.T) {
	t.Setenv("CODEX_FAKE_APPROVAL", "1")
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "")
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeCancelled {
		t.Fatalf("发起失败应立即回拒 → cancelled，得到 %s (%+v)", res.Outcome, res.Failure)
	}
}

// ── steering / 取消 ─────────────────────────────────────────────────

// ControlInput → turn/steer（threadId+expectedTurnId 前置条件）。
func TestExecuteSteering(t *testing.T) {
	t.Setenv("CODEX_FAKE_HANG", "1")
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "")
	r.waitSpawned(t)
	if ref := r.waitSession(t); ref != "codex://th_fake_1" {
		t.Fatalf("会话句柄错误: %s", ref)
	}
	// 等 turn/start 握手完成（turnId 就绪），再投递 steering。
	time.Sleep(500 * time.Millisecond)
	r.ctl <- atwruntime.Control{Kind: atwruntime.ControlInput, Instruction: "追加要求"}
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	if text := r.cb.completedText(); text != "steered" {
		t.Fatalf("steering 后的最终消息错误: %q", text)
	}
}

// discardCloser 吸掉 pump 握手期的上行写（initialize/initialized/thread/start/
// turn/start），使 pump 可离线直测：通知帧由脚本化 reader 供给。
type discardCloser struct{}

func (discardCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardCloser) Close() error                { return nil }

// 未识别的 app-server 通知不得静默丢弃：default 分支记 warn OnLog（含 method
// 名，params 截 200），且通知流继续消费到 turn/completed。
func TestPumpLogsUnrecognizedNotifications(t *testing.T) {
	frames := []string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.149.0-fake"}}`,
		`{"id":2,"result":{"thread":{"id":"th_1","sessionId":"th_1"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn_1","status":"inProgress"}}}`,
		`{"method":"item/mysteryStarted","params":{"itemId":"ms_1","detail":"` + strings.Repeat("A", 300) + `Z"}}`,
		`{"method":"turn/completed","params":{"threadId":"th_1","turn":{"id":"turn_1","status":"completed"}}}`,
	}
	reader := bufio.NewReader(strings.NewReader(strings.Join(frames, "\n") + "\n"))
	cb := &recordCallbacks{}
	s := &execStream{
		module: New(Config{}), ctx: context.Background(),
		ex: &atwruntime.ExecContext{
			Ctx: context.Background(), Run: newRun(nil), Instruction: "codex fake run",
			Callbacks: cb, Controls: make(chan atwruntime.Control, 1),
		},
		stdin: discardCloser{}, pendingRequests: map[int64]string{}, approvals: map[string]chan bool{},
	}

	res := s.pump(reader)

	if !res.finished || res.turnStatus != "completed" {
		t.Fatalf("未知通知不得影响通知流收尾: %+v", res)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	i := -1
	for j, l := range cb.logs {
		if strings.Contains(l.line, "item/mysteryStarted") {
			i = j
		}
	}
	if i < 0 {
		t.Fatalf("未知通知应记 warn OnLog: %v", cb.logs)
	}
	if line := cb.logs[i]; !strings.Contains(line.line, "warn") ||
		strings.Contains(line.line, "Z") { // params 截 200：300+A 的尾部不得进入日志
		t.Fatalf("warn 日志形状不符（应含 warn 标记且截断）: %+v", line)
	}
}

// runPumpFrames 以脚本化 reader 直驱 pump（离线；stdin 由 discardCloser 吸掉
// 握手期上行写），返回 pump 产出与终态裁决结果。
func runPumpFrames(t *testing.T, frames []string) (*pumpResult, atwruntime.ExecResult) {
	t.Helper()
	reader := bufio.NewReader(strings.NewReader(strings.Join(frames, "\n") + "\n"))
	cb := &recordCallbacks{}
	s := &execStream{
		module: New(Config{}), ctx: context.Background(),
		ex: &atwruntime.ExecContext{
			Ctx: context.Background(), Run: newRun(nil), Instruction: "codex fake run",
			Callbacks: cb, Controls: make(chan atwruntime.Control, 1),
		},
		stdin: discardCloser{}, pendingRequests: map[int64]string{}, approvals: map[string]chan bool{},
	}
	res := s.pump(reader)
	return res, composeResult(s.ex, context.Background(), res, nil)
}

// 用量帧 → ExecResult.Usage：只累计归因到活动 turn（turn_1）的通知增量——
// resume 重放帧（turn_prev）与异 turn 帧（turn_other）不得计入；snake_case
// 容错形状（info.last_token_usage.*）与权威 camelCase 形状等价累计。
func TestPumpAccumulatesTokenUsage(t *testing.T) {
	res, final := runPumpFrames(t, []string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.149.0-fake"}}`,
		`{"id":2,"result":{"thread":{"id":"th_1","sessionId":"th_1"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn_1","status":"inProgress"}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_prev","tokenUsage":{"total":{"inputTokens":9000,"cachedInputTokens":8000,"outputTokens":7000},"last":{"inputTokens":3000,"cachedInputTokens":2000,"outputTokens":1000}}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","tokenUsage":{"total":{"inputTokens":9100,"cachedInputTokens":8040,"outputTokens":7020},"last":{"inputTokens":100,"cachedInputTokens":40,"outputTokens":20}}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","info":{"total_token_usage":{"input_tokens":9250,"cached_input_tokens":8100,"output_tokens":7050},"last_token_usage":{"input_tokens":150,"cached_input_tokens":60,"output_tokens":30}}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_other","tokenUsage":{"last":{"inputTokens":999,"cachedInputTokens":999,"outputTokens":999}}}}`,
		`{"method":"turn/completed","params":{"threadId":"th_1","turn":{"id":"turn_1","status":"completed"}}}`,
	})

	if !res.finished || res.turnStatus != "completed" {
		t.Fatalf("用量通知不得影响通知流收尾: %+v", res)
	}
	want := &atwruntime.Usage{InputTokens: 250, OutputTokens: 50, CachedTokens: 100, Basis: atwruntime.UsagePerRun}
	if res.usage == nil || *res.usage != *want {
		t.Fatalf("pump 用量累计错误（期望 100+150/20+30/40+60 且只计 turn_1）: %+v", res.usage)
	}
	if final.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", final.Outcome, final.Failure)
	}
	if final.Usage == nil || *final.Usage != *want {
		t.Fatalf("ExecResult.Usage 映射错误: %+v", final.Usage)
	}
}

// 零值对照：无用量帧 → pump 与 ExecResult 的 Usage 均为 nil（不捏造上报）。
func TestPumpUsageZeroWithoutFrames(t *testing.T) {
	res, final := runPumpFrames(t, []string{
		`{"id":1,"result":{"userAgent":"codex-cli/0.149.0-fake"}}`,
		`{"id":2,"result":{"thread":{"id":"th_1","sessionId":"th_1"}}}`,
		`{"id":3,"result":{"turn":{"id":"turn_1","status":"inProgress"}}}`,
		`{"method":"turn/completed","params":{"threadId":"th_1","turn":{"id":"turn_1","status":"completed"}}}`,
	})

	if !res.finished || res.turnStatus != "completed" {
		t.Fatalf("收尾异常: %+v", res)
	}
	if res.usage != nil || final.Usage != nil {
		t.Fatalf("无用量帧不得捏造 Usage: pump=%+v exec=%+v", res.usage, final.Usage)
	}
}

// 无终态意图的 ctx 取消（如服务关停）默认 interrupted（保留 resume 时机）。
func TestExecuteContextCancelDefaultsToInterrupted(t *testing.T) {
	t.Setenv("CODEX_FAKE_HANG", "1")
	m := newTestModule(t)
	r := startExecute(t, m, nil, "", "")
	r.waitSpawned(t)
	r.cancel()
	res := r.wait(t, 15*time.Second)
	if res.Outcome != atwruntime.OutcomeInterrupted {
		t.Fatalf("期望 interrupted，得到 %s (%+v)", res.Outcome, res.Failure)
	}
}

// ── ModuleRunner：终态意图路径 ───────────────────────────────────────

// sinkStub 实现 runtime.EngineSink，供 ModuleRunner 集成测试观察终态。
type sinkStub struct {
	mu       sync.Mutex
	statuses []domain.RunStatus
	sessions []atwruntime.SessionUpdate
}

func (s *sinkStub) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, to)
	return nil
}
func (s *sinkStub) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return nil
}
func (s *sinkStub) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	return nil
}
func (s *sinkStub) RecordRunSessionUpdate(ctx context.Context, runID string, update atwruntime.SessionUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, update)
	return nil
}
func (s *sinkStub) RecordRunUsage(ctx context.Context, runID string, usage atwruntime.Usage) error {
	return nil
}
func (s *sinkStub) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	return nil, nil
}

// Run 返回带最新状态的 run 快照（recordTerminal 会读它补中间迁移，不能返回 nil）。
func (s *sinkStub) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	return &domain.ExecutionRun{ID: id, Status: s.last()}, nil
}

func (s *sinkStub) last() domain.RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.statuses) == 0 {
		return ""
	}
	return s.statuses[len(s.statuses)-1]
}

func (s *sinkStub) hasStatus(want domain.RunStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.statuses {
		if st == want {
			return true
		}
	}
	return false
}

func (s *sinkStub) history() []domain.RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.RunStatus(nil), s.statuses...)
}

func (s *sinkStub) waitStatus(t *testing.T, want domain.RunStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.last() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待状态 %s 超时，历史 %v", want, s.history())
}

// ModuleRunner.Control 的终态意图 → cancelled / interrupted（原生 turn/interrupt）。
func TestModuleRunnerInterruptAndCancel(t *testing.T) {
	for _, terminal := range []domain.RunStatus{domain.RunInterrupted, domain.RunCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			t.Setenv("CODEX_FAKE_HANG", "1")
			m := newTestModule(t)
			sink := &sinkStub{}
			runner := atwruntime.NewModuleRunner(sink)
			runner.Register("codex-appserver", m)
			run := newRun(nil)
			if err := runner.Dispatch(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			sink.waitStatus(t, domain.RunRunning, 10*time.Second)
			// 等 turn/start 握手完成，确保走原生 turn/interrupt 路径。
			time.Sleep(500 * time.Millisecond)
			runner.Control(run.ID, terminal)
			sink.waitStatus(t, terminal, 10*time.Second)
			if sink.hasStatus(domain.RunFailed) {
				t.Error("interrupt/cancel 不应落 failed")
			}
		})
	}
}

// ── 本地 conformance 套件（对齐 internal/runtime/conformance 场景）────

// strictSink 在状态机权威、事件白名单与终态不可变上对齐 conformance.fakeSink。
type strictSink struct {
	mu             sync.Mutex
	run            *domain.ExecutionRun
	statuses       []domain.RunStatus
	events         []string
	sessions       []atwruntime.SessionUpdate
	usages         []atwruntime.Usage
	unknownEvents  []string
	illegal        []string
	afterTerminal  []string
	runningSeen    bool
	terminalClosed bool
	done           chan struct{}
}

func newStrictSink(run *domain.ExecutionRun) *strictSink {
	return &strictSink{run: run, done: make(chan struct{})}
}

func (f *strictSink) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "status:"+string(to))
		return domain.ErrTerminalImmutable
	}
	from := f.run.Status
	if err := f.run.Transition(to, time.Now().UTC()); err != nil {
		f.illegal = append(f.illegal, string(from)+"->"+string(to))
		return err
	}
	f.statuses = append(f.statuses, to)
	if to == domain.RunRunning {
		f.runningSeen = true
	}
	if to.IsTerminal() && !f.terminalClosed {
		f.terminalClosed = true
		close(f.done)
	}
	return nil
}

func (f *strictSink) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "progress")
		return domain.ErrTerminalImmutable
	}
	return nil
}

func (f *strictSink) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "event:"+evType)
		return domain.ErrTerminalImmutable
	}
	if !domain.IsKnownEventName(evType) {
		f.unknownEvents = append(f.unknownEvents, evType)
	}
	f.events = append(f.events, evType)
	return nil
}

func (f *strictSink) RecordRunSessionUpdate(ctx context.Context, runID string, update atwruntime.SessionUpdate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "session:"+update.Ref)
		return domain.ErrTerminalImmutable
	}
	f.sessions = append(f.sessions, update)
	return nil
}

func (f *strictSink) RecordRunUsage(ctx context.Context, runID string, usage atwruntime.Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.run.Status.IsTerminal() {
		f.afterTerminal = append(f.afterTerminal, "usage")
		return domain.ErrTerminalImmutable
	}
	f.usages = append(f.usages, usage)
	return nil
}

func (f *strictSink) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	return nil, nil
}

func (f *strictSink) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := *f.run
	return &copied, nil
}

func (f *strictSink) status() domain.RunStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run.Status
}

func (f *strictSink) snapshot() (statuses []domain.RunStatus, events []string, sessions []atwruntime.SessionUpdate, usages []atwruntime.Usage, illegal, afterTerminal, unknown []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.RunStatus(nil), f.statuses...), append([]string(nil), f.events...),
		append([]atwruntime.SessionUpdate(nil), f.sessions...), append([]atwruntime.Usage(nil), f.usages...),
		append([]string(nil), f.illegal...), append([]string(nil), f.afterTerminal...),
		append([]string(nil), f.unknownEvents...)
}

type strictEnv struct {
	sink   *strictSink
	runner *atwruntime.ModuleRunner
	run    *domain.ExecutionRun
}

func newStrictEnv(t *testing.T, m *Module) *strictEnv {
	t.Helper()
	run := newRun(nil)
	sink := newStrictSink(run)
	runner := atwruntime.NewModuleRunner(sink)
	runner.Register("codex-appserver", m)
	return &strictEnv{sink: sink, runner: runner, run: run}
}

func (e *strictEnv) dispatchAndWaitRunning(t *testing.T) {
	t.Helper()
	if err := e.runner.Dispatch(context.Background(), e.run); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		e.sink.mu.Lock()
		seen := e.sink.runningSeen
		e.sink.mu.Unlock()
		if seen {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Run 未进入 running")
}

func (e *strictEnv) waitTerminal(t *testing.T) {
	t.Helper()
	select {
	case <-e.sink.done:
	case <-time.After(15 * time.Second):
		statuses, events, _, _, illegal, _, _ := e.sink.snapshot()
		t.Fatalf("Run 未到达终态：statuses=%v events=%v illegal=%v", statuses, events, illegal)
	}
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

// TestConformanceCodexApp：SPI v2 一致性场景（状态机权威 / 终态后无副作用 /
// 取消语义 / 会话早期上报），对齐 internal/runtime/conformance 的公共套件。
func TestConformanceCodexApp(t *testing.T) {
	ctx := context.Background()

	t.Run("StateMachineAuthority", func(t *testing.T) {
		e := newStrictEnv(t, newTestModule(t))
		e.dispatchAndWaitRunning(t)
		e.waitTerminal(t)

		statuses, events, sessions, usages, illegal, afterTerminal, unknown := e.sink.snapshot()
		if got := e.sink.status(); got != domain.RunSucceeded {
			t.Fatalf("期望 succeeded，实际 %s（statuses=%v）", got, statuses)
		}
		// 状态只能经 RecordRunStatus 迁移，序列满足 domain runTransitions。
		want := []domain.RunStatus{
			domain.RunStarting, domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded,
		}
		if !equalStatuses(statuses, want) {
			t.Fatalf("状态序列 %v != 期望 %v", statuses, want)
		}
		if len(illegal) > 0 || len(unknown) > 0 {
			t.Fatalf("非法迁移 %v / 白名单外事件 %v", illegal, unknown)
		}
		if len(afterTerminal) > 0 {
			t.Fatalf("终态后仍有写入: %v", afterTerminal)
		}
		if len(events) == 0 {
			t.Fatal("模块应至少回放一个 canonical 事件")
		}
		// 会话句柄：早期 OnSession + ExecResult.Session 各一份，ref 带 codex scheme。
		if len(sessions) < 2 {
			t.Fatalf("期望早期 OnSession 与 ExecResult.Session 共 >=2 次上报，实际 %d", len(sessions))
		}
		if !strings.HasPrefix(sessions[0].Ref, "codex://") {
			t.Fatalf("会话 ref 期望前缀 codex://，实际 %s", sessions[0].Ref)
		}
		// 用量零值对照：回放 fixture 不发 token 用量帧 → 不得捏造 Usage 上报；
		// 同时钉住 codexapp 不走 OnUsage 流式（用量唯一出口是 ExecResult.Usage）。
		// 正向映射（tokenUsage 帧 → Usage 三字段/per_run）见 TestPumpAccumulatesTokenUsage。
		if len(usages) != 0 {
			t.Fatalf("无用量帧不得上报 Usage: %+v", usages)
		}
	})

	t.Run("NoSideEffectsAfterTerminal", func(t *testing.T) {
		e := newStrictEnv(t, newTestModule(t))
		e.dispatchAndWaitRunning(t)
		e.waitTerminal(t)
		// 等 goroutine 尾巴完全跑完再断言。
		time.Sleep(200 * time.Millisecond)
		if _, _, _, _, _, afterTerminal, _ := e.sink.snapshot(); len(afterTerminal) > 0 {
			t.Fatalf("终态后仍有事件/状态/会话写入: %v", afterTerminal)
		}
		// 终态后下达控制命令：active 已清理，必须是 no-op。
		e.runner.Control(e.run.ID, domain.RunCancelled)
		if got := e.sink.status(); got != domain.RunSucceeded {
			t.Fatalf("终态不可被 Control 改写: %s", got)
		}
	})

	t.Run("CancelSemantics", func(t *testing.T) {
		t.Setenv("CODEX_FAKE_HANG", "1")
		e := newStrictEnv(t, newTestModule(t))
		e.dispatchAndWaitRunning(t)
		// 模拟 application：running 后进入 cancelling（starting 不可取消）。
		if err := e.sink.RecordRunStatus(ctx, e.run.ID, domain.RunCancelling, nil); err != nil {
			t.Fatalf("running→cancelling 应合法: %v", err)
		}
		// Control(cancel) → Ctx 取消 + terminalIntent=cancel → 模块返回 cancelled。
		e.runner.Control(e.run.ID, domain.RunCancelled)
		e.waitTerminal(t)

		statuses, _, _, _, illegal, afterTerminal, _ := e.sink.snapshot()
		if got := e.sink.status(); got != domain.RunCancelled {
			t.Fatalf("期望 cancelled，实际 %s（statuses=%v）", got, statuses)
		}
		want := []domain.RunStatus{
			domain.RunStarting, domain.RunRunning, domain.RunCancelling, domain.RunCancelled,
		}
		if !equalStatuses(statuses, want) {
			t.Fatalf("取消状态序列 %v != 期望 %v", statuses, want)
		}
		if len(illegal) > 0 {
			t.Fatalf("存在非法状态迁移: %v", illegal)
		}
		if len(afterTerminal) > 0 {
			t.Fatalf("终态后仍有写入: %v", afterTerminal)
		}
	})
}

// ── 真实安装冒烟（默认跳过）─────────────────────────────────────────

func TestCodexLiveAppServer(t *testing.T) {
	if os.Getenv("ATW_CODEX_LIVE") != "1" {
		t.Skip("set ATW_CODEX_LIVE=1 to exercise the installed Codex app-server")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not installed")
	}
	m := New(Config{BinPath: "codex", WorkspaceRoot: t.TempDir(), GracePeriod: 5 * time.Second})
	probe, err := m.Probe(context.Background(), atwruntime.ProbeRequest{WorkspaceID: "ws_live"})
	if err != nil || !probe.OK {
		t.Fatalf("live probe failed: result=%+v err=%v", probe, err)
	}
	input := map[string]any{
		"instruction":   "Reply with exactly ATW_CODEX_APP_SERVER_OK. Do not use tools or modify files.",
		"system_prompt": "Follow the user's exact output request.",
		"policy":        map[string]any{"sandbox": "read-only", "approval_policy": "manual"},
	}
	r := startExecute(t, m, input, "", "")
	res := r.wait(t, 2*time.Minute)
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("live run 未成功: %s (%+v)", res.Outcome, res.Failure)
	}
	if text := r.cb.completedText(); !strings.Contains(text, "ATW_CODEX_APP_SERVER_OK") {
		t.Fatalf("unexpected live response: %q", text)
	}
}
