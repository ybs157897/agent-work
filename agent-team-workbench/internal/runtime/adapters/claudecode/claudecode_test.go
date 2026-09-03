package claudecode

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── 回放桩：假 CLI（内联 shell 脚本，不依赖真实 claude CLI）────────────

const (
	frameSystemInit = `{"type":"system","subtype":"init","session_id":"sess_fake_1","model":"fake-model","cwd":"/tmp"}`
	frameAssistant  = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"fake claude 输出"}]}}`
	frameResultOK   = `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"sess_fake_1","usage":{"input_tokens":25,"output_tokens":82,"cache_creation_input_tokens":100,"cache_read_input_tokens":900}}`
	frameSystemHang = `{"type":"system","subtype":"init","session_id":"sess_hang"}`
)

func writeFakeCLI(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\n" + strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeArgvCLI 把收到的 argv 逐行落盘后再回放帧。
func writeArgvCLI(t *testing.T, frames ...string) (script, capture string) {
	t.Helper()
	capture = filepath.Join(t.TempDir(), "argv.txt")
	t.Setenv("CAPTURE_FILE", capture)
	var b strings.Builder
	b.WriteString(`printf '%s\n' "$@" > "$CAPTURE_FILE"` + "\n")
	for _, f := range frames {
		b.WriteString("echo '" + f + "'\n")
	}
	return writeFakeCLI(t, b.String()), capture
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func argPairs(args []string, flag string) []string {
	var values []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			values = append(values, args[i+1])
		}
	}
	return values
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// ── 测试桩：Callbacks / EngineSink ────────────────────────────────────

type recEvent struct {
	typ  string
	data map[string]any
}

type recCallbacks struct {
	mu       sync.Mutex
	events   []recEvent
	logs     []recLog
	sessions []runtime.SessionUpdate
	spawned  bool
	pid      int
	pgid     int
}

type recLog struct {
	stream, line string
}

func (c *recCallbacks) OnEvent(eventType string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, recEvent{typ: eventType, data: data})
}
func (c *recCallbacks) OnProgress(progress float64) {}
func (c *recCallbacks) OnLog(stream, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, recLog{stream: stream, line: line})
}
func (c *recCallbacks) OnSpawn(pid, processGroupID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spawned, c.pid, c.pgid = true, pid, processGroupID
}
func (c *recCallbacks) OnUsage(u runtime.Usage)                           {}
func (c *recCallbacks) RequestApproval(kind, risk, summary string) string { return "" }
func (c *recCallbacks) OnSession(update runtime.SessionUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, update)
}

func (c *recCallbacks) event(typ string) (recEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.typ == typ {
			return e, true
		}
	}
	return recEvent{}, false
}

func newRun(input map[string]any) *domain.ExecutionRun {
	return &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), AgentProfileID: "agent_test", Status: domain.RunQueued, Version: 1,
		AdapterID: "claude-code", Input: input,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func execModule(t *testing.T, m *Module, run *domain.ExecutionRun, sessionRef string, controls []runtime.Control) (runtime.ExecResult, *recCallbacks) {
	t.Helper()
	cb := &recCallbacks{}
	ctl := make(chan runtime.Control, 8)
	for _, c := range controls {
		ctl <- c
	}
	ex := &runtime.ExecContext{
		Ctx:         context.Background(),
		Run:         run,
		Resolved:    domain.ResolvedExecutionContext{CWD: t.TempDir(), AuthorizedRoot: t.TempDir()},
		Instruction: runtime.EffectiveInstruction(run),
		Session:     runtime.SessionState{Ref: sessionRef},
		Callbacks:   cb,
		Controls:    ctl,
	}
	return m.Execute(ex), cb
}

// fakeSink 实现 runtime.EngineSink，供 ModuleRunner 集成测试观察终态。
type fakeSink struct {
	mu       sync.Mutex
	statuses []domain.RunStatus
	events   []string
	session  *runtime.SessionUpdate
	usage    *runtime.Usage
}

func newFakeSink() *fakeSink { return &fakeSink{} }

func (s *fakeSink) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, to)
	return nil
}
func (s *fakeSink) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return nil
}
func (s *fakeSink) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evType)
	return nil
}
func (s *fakeSink) RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = &update
	return nil
}
func (s *fakeSink) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = &usage
	return nil
}
func (s *fakeSink) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	return nil, errors.New("approval unsupported")
}
func (s *fakeSink) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	return nil, errors.New("not implemented")
}

func (s *fakeSink) hasStatus(want domain.RunStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.statuses {
		if st == want {
			return true
		}
	}
	return false
}

func (s *fakeSink) waitStatus(t *testing.T, want domain.RunStatus, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.hasStatus(want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("等待状态 %s 超时，收到 %v", want, s.statuses)
}

func (s *fakeSink) waitSession(t *testing.T, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		sess := s.session
		s.mu.Unlock()
		if sess != nil {
			return sess.Ref
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待会话句柄超时")
	return ""
}

// ── Manifest / Probe ─────────────────────────────────────────────────

func TestModuleManifest(t *testing.T) {
	m := New(Config{BinPath: "claude"})
	mf, err := m.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mf.AdapterID != "claude-code" || mf.AdapterVersion != "1.0.0" {
		t.Fatalf("manifest 标识错误: %+v", mf)
	}
	if mf.SchemaDigest != "sha256:claude-cli-stream-json-v1" {
		t.Fatalf("schema digest 变化: %s", mf.SchemaDigest)
	}
	want := map[string]runtime.CapabilityLevel{
		"streaming": runtime.CapSupported, "resume": runtime.CapSupported,
		"multi_turn": runtime.CapSupported, "system_prompt": runtime.CapSupported,
		"modes": runtime.CapSupported, "permissions": runtime.CapSupported,
		"interrupt": runtime.CapAdapterTranslated, "approval": runtime.CapUnavailable,
		"workspace_files": runtime.CapSupported, "terminal": runtime.CapUnavailable,
		"structured_output": runtime.CapSupported,
	}
	if len(mf.Capabilities) != len(want) {
		t.Fatalf("能力集变化: %+v", mf.Capabilities)
	}
	for k, v := range want {
		if mf.Capabilities[k] != v {
			t.Errorf("能力 %s = %s，期望 %s", k, mf.Capabilities[k], v)
		}
	}
	if _, declared := mf.Capabilities["steering"]; declared {
		t.Error("print mode 不得声明 steering")
	}
}

func TestProbeMissingCLI(t *testing.T) {
	m := New(Config{BinPath: filepath.Join(t.TempDir(), "missing-claude")})
	got, err := m.Probe(context.Background(), runtime.ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Error == "" {
		t.Fatalf("CLI 缺失应 probe 失败: %+v", got)
	}
	script := writeFakeCLI(t, "exit 0")
	m2 := New(Config{BinPath: script})
	got2, err := m2.Probe(context.Background(), runtime.ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !got2.OK {
		t.Fatalf("CLI 存在应 probe 通过: %+v", got2)
	}
}

// ── Execute：帧→事件映射 / 会话 / 用量（协议文档 §9）──────────────────

// system.init → SessionUpdate(claude://id)；assistant → message.completed；result.usage → ExecResult.Usage。
func TestExecuteHappyPath(t *testing.T) {
	script := writeFakeCLI(t,
		"echo '"+frameSystemInit+"'",
		"echo '"+frameAssistant+"'",
		"echo '"+frameResultOK+"'",
	)
	m := New(Config{BinPath: script})
	res, cb := execModule(t, m, newRun(map[string]any{"instruction": "claude fake run"}), "", nil)

	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	if res.Failure != nil {
		t.Fatalf("不应有 Failure: %+v", res.Failure)
	}
	// 帧 → canonical 事件：message.completed {"text": ...}
	ev, ok := cb.event(domain.EventMessageCompleted)
	if !ok {
		t.Fatalf("缺少 message.completed，事件 %v", cb.events)
	}
	if ev.data["text"] != "fake claude 输出" {
		t.Fatalf("message.completed payload 错误: %+v", ev.data)
	}
	// 会话 id 解析：system 帧 + OnSession + ExecResult.Session
	if len(cb.sessions) == 0 || cb.sessions[0].Ref != "claude://sess_fake_1" {
		t.Fatalf("OnSession 未上报 claude://sess_fake_1: %+v", cb.sessions)
	}
	if res.Session == nil || res.Session.Ref != "claude://sess_fake_1" {
		t.Fatalf("ExecResult.Session 错误: %+v", res.Session)
	}
	// 用量：result 帧 → per_run
	if res.Usage == nil || res.Usage.Basis != runtime.UsagePerRun ||
		res.Usage.InputTokens != 25 || res.Usage.OutputTokens != 82 || res.Usage.CachedTokens != 1000 {
		t.Fatalf("用量解析错误: %+v", res.Usage)
	}
	if res.Usage.ProviderReport == nil || res.Usage.Canonical == nil {
		t.Fatalf("Claude per_run usage 应同时带 report/canonical: %+v", res.Usage)
	}
	if err := res.Usage.ProviderReport.VerifyDigest(); err != nil {
		t.Fatalf("Claude provider report digest 无法验证: %v", err)
	}
	if err := res.Usage.Canonical.VerifyDigest(); err != nil {
		t.Fatalf("Claude canonical usage digest 无法验证: %v", err)
	}
	got := res.Usage.ProviderReport.Counters
	if got.InputTokensTotal == nil || *got.InputTokensTotal != 1025 ||
		got.InputUncachedTokens == nil || *got.InputUncachedTokens != 25 ||
		got.CacheReadTokens == nil || *got.CacheReadTokens != 900 ||
		got.CacheWriteTokens == nil || *got.CacheWriteTokens != 100 ||
		got.OutputTokens == nil || *got.OutputTokens != 82 {
		t.Fatalf("Claude 原生 usage 四桶拆分错误: %+v", got)
	}
	// 进程上报
	if !cb.spawned || cb.pid <= 0 || cb.pgid <= 0 {
		t.Fatalf("OnSpawn 未正确上报: spawned=%v pid=%d pgid=%d", cb.spawned, cb.pid, cb.pgid)
	}
}

// stderr 原始行走 OnLog。
func TestExecuteStderrLogged(t *testing.T) {
	script := writeFakeCLI(t,
		"echo 'some stderr noise' >&2",
		"echo '"+frameResultOK+"'",
	)
	m := New(Config{BinPath: script})
	res, cb := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", nil)
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s", res.Outcome)
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if len(cb.logs) != 1 || cb.logs[0].stream != "stderr" || cb.logs[0].line != "some stderr noise" {
		t.Fatalf("stderr 未走 OnLog: %+v", cb.logs)
	}
}

// 未声明能力（steering/approval）的 Control 必须被忽略且不阻塞。
func TestExecuteIgnoresUndeclaredControls(t *testing.T) {
	script := writeFakeCLI(t, "echo '"+frameResultOK+"'")
	m := New(Config{BinPath: script})
	controls := []runtime.Control{
		{Kind: runtime.ControlInput, Instruction: "steer"},
		{Kind: runtime.ControlApproval, ApprovalID: "ap_1", Approved: true},
	}
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", controls)
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("未声明能力的 Control 应被忽略，得到 %s (%+v)", res.Outcome, res.Failure)
	}
}

// ── Execute：参数组装（--resume / 策略 / system prompt / model）────────

func TestExecuteArgsResume(t *testing.T) {
	script, capture := writeArgvCLI(t, frameResultOK)
	m := New(Config{BinPath: script})
	run := newRun(map[string]any{
		"instruction":   "continue",
		"system_prompt": "be terse",
	})
	res, _ := execModule(t, m, run, "claude://sess_123", nil)
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	args := readLines(t, capture)
	if got := argPairs(args, "--resume"); len(got) != 1 || got[0] != "sess_123" {
		t.Fatalf("--resume 参数错误: %v（完整 %v）", got, args)
	}
	if hasFlag(args, "--append-system-prompt") {
		t.Fatalf("resume 时不应附加 system prompt: %v", args)
	}
	if got := argPairs(args, "-p"); len(got) != 1 || got[0] != "continue" {
		t.Fatalf("-p instruction 错误: %v", got)
	}
	if !hasFlag(args, "--output-format") || !hasFlag(args, "--verbose") {
		t.Fatalf("print mode 参数缺失: %v", args)
	}
}

func TestExecuteArgsPolicyReadOnly(t *testing.T) {
	script, capture := writeArgvCLI(t, frameResultOK)
	m := New(Config{BinPath: script})
	run := newRun(map[string]any{
		"instruction": "read only",
		"policy":      map[string]any{"sandbox": "read-only"},
	})
	res, _ := execModule(t, m, run, "", nil)
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	args := readLines(t, capture)
	if got := argPairs(args, "--permission-mode"); len(got) != 1 || got[0] != "plan" {
		t.Fatalf("--permission-mode 错误: %v（完整 %v）", got, args)
	}
	if got := argPairs(args, "--tools"); len(got) != 1 || got[0] != "Read,Glob,Grep,TodoWrite" {
		t.Fatalf("--tools 错误: %v", got)
	}
}

func TestExecuteArgsSystemPromptAndModel(t *testing.T) {
	script, capture := writeArgvCLI(t, frameResultOK)
	m := New(Config{BinPath: script})
	run := newRun(map[string]any{
		"instruction":   "fresh",
		"system_prompt": "be terse",
		"model":         map[string]any{"model": "sonnet-x"},
	})
	res, _ := execModule(t, m, run, "", nil)
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
	args := readLines(t, capture)
	if got := argPairs(args, "--append-system-prompt"); len(got) != 1 || got[0] != "be terse" {
		t.Fatalf("--append-system-prompt 错误: %v（完整 %v）", got, args)
	}
	if got := argPairs(args, "--model"); len(got) != 1 || got[0] != "sonnet-x" {
		t.Fatalf("--model per-run 覆盖错误: %v", got)
	}
}

func TestExecuteArgsConfigModelFallback(t *testing.T) {
	script, capture := writeArgvCLI(t, frameResultOK)
	m := New(Config{BinPath: script, Model: "cfg-model"})
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", nil)
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("期望 succeeded，得到 %s", res.Outcome)
	}
	if got := argPairs(readLines(t, capture), "--model"); len(got) != 1 || got[0] != "cfg-model" {
		t.Fatalf("cfg.Model 回退错误: %v", got)
	}
}

// ── Execute：失败分类 ────────────────────────────────────────────────

// result subtype=error_* → failed，Code 沿用旧风格 claude_<subtype>。
func TestExecuteResultError(t *testing.T) {
	frame := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"fake execution error"}`
	script := writeFakeCLI(t, "echo '"+frame+"'", "exit 1")
	m := New(Config{BinPath: script})
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "fail"}), "", nil)
	if res.Outcome != runtime.OutcomeFailed {
		t.Fatalf("期望 failed，得到 %s", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Code != "claude_error_during_execution" {
		t.Fatalf("失败码错误: %+v", res.Failure)
	}
	if res.Failure.Message != "fake execution error" {
		t.Fatalf("失败消息错误: %q", res.Failure.Message)
	}
	if res.Failure.Family != runtime.FamilyInternal || res.Failure.Retryable {
		t.Fatalf("普通执行错误应 internal/不可重试: %+v", res.Failure)
	}
}

func TestExecuteQuotaClassification(t *testing.T) {
	frame := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Usage limit reached for your plan"}`
	script := writeFakeCLI(t, "echo '"+frame+"'", "exit 1")
	m := New(Config{BinPath: script})
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", nil)
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil {
		t.Fatalf("期望 failed: %+v", res)
	}
	if res.Failure.Family != runtime.FamilyProviderQuota || !res.Failure.Retryable {
		t.Fatalf("quota 应 provider_quota/可重试: %+v", res.Failure)
	}
}

func TestExecuteTransientClassification(t *testing.T) {
	frame := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Connection error: network request failed"}`
	script := writeFakeCLI(t, "echo '"+frame+"'", "exit 1")
	m := New(Config{BinPath: script})
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", nil)
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil {
		t.Fatalf("期望 failed: %+v", res)
	}
	if res.Failure.Family != runtime.FamilyTransientUpstream || !res.Failure.Retryable {
		t.Fatalf("网络错误应 transient_upstream/可重试: %+v", res.Failure)
	}
}

// CLI 不存在 → FamilyConfig / spawn_failed。
func TestExecuteSpawnFailure(t *testing.T) {
	m := New(Config{BinPath: filepath.Join(t.TempDir(), "missing-claude")})
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", nil)
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil {
		t.Fatalf("期望 failed: %+v", res)
	}
	if res.Failure.Code != "spawn_failed" || res.Failure.Family != runtime.FamilyConfig {
		t.Fatalf("spawn 失败应 config/spawn_failed: %+v", res.Failure)
	}
}

// 干净退出（退出码 0）且无 result 帧 → succeeded。
func TestExecuteExitZeroNoResult(t *testing.T) {
	script := writeFakeCLI(t, "exit 0")
	m := New(Config{BinPath: script})
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", nil)
	if res.Outcome != runtime.OutcomeSucceeded {
		t.Fatalf("退出码 0 应 succeeded，得到 %s (%+v)", res.Outcome, res.Failure)
	}
}

// 非零退出且无 result 帧 → failed stream_failed（fail loud）。
func TestExecuteExitNonZeroNoResult(t *testing.T) {
	script := writeFakeCLI(t, "exit 7")
	m := New(Config{BinPath: script})
	res, _ := execModule(t, m, newRun(map[string]any{"instruction": "x"}), "", nil)
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil {
		t.Fatalf("期望 failed: %+v", res)
	}
	if res.Failure.Code != "stream_failed" || res.Failure.Family != runtime.FamilyIO {
		t.Fatalf("非零退出应 stream_failed/io: %+v", res.Failure)
	}
}

// 空 instruction → config 失败，不启动进程。
func TestExecuteEmptyInstruction(t *testing.T) {
	script := writeFakeCLI(t, "exit 0")
	m := New(Config{BinPath: script})
	res, cb := execModule(t, m, newRun(map[string]any{"instruction": "  "}), "", nil)
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Code != "instruction_required" || res.Failure.Family != runtime.FamilyConfig {
		t.Fatalf("空 instruction 应 config/instruction_required: %+v", res)
	}
	if cb.spawned {
		t.Fatal("空 instruction 不应启动进程")
	}
}

// ── ctx 取消 → interrupted/cancelled（经 ModuleRunner 驱动终态意图）────

func TestRunnerInterrupt(t *testing.T) {
	sink := newFakeSink()
	runner := runtime.NewModuleRunner(sink)
	script := writeFakeCLI(t, "echo '"+frameSystemHang+"'", "sleep 300")
	runner.Register("claude-code", New(Config{BinPath: script, GracePeriod: time.Second}))
	run := newRun(map[string]any{"instruction": "hang"})
	if err := runner.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if ref := sink.waitSession(t, 10*time.Second); ref != "claude://sess_hang" {
		t.Fatalf("会话句柄错误: %s", ref)
	}
	runner.Control(run.ID, domain.RunInterrupted)
	sink.waitStatus(t, domain.RunInterrupted, 10*time.Second)
	if sink.hasStatus(domain.RunFailed) {
		t.Error("interrupt 不应落 failed")
	}
}

func TestRunnerCancel(t *testing.T) {
	sink := newFakeSink()
	runner := runtime.NewModuleRunner(sink)
	script := writeFakeCLI(t, "echo '"+frameSystemHang+"'", "sleep 300")
	runner.Register("claude-code", New(Config{BinPath: script, GracePeriod: time.Second}))
	run := newRun(map[string]any{"instruction": "hang"})
	if err := runner.Dispatch(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	sink.waitSession(t, 10*time.Second)
	runner.Control(run.ID, domain.RunCancelled)
	sink.waitStatus(t, domain.RunCancelled, 10*time.Second)
	if sink.hasStatus(domain.RunFailed) {
		t.Error("cancel 不应落 failed")
	}
}

// ctx 在 Execute 前已取消且无终态意图 → failed（per SPI 决策树）。
func TestExecutePreCancelledWithoutIntent(t *testing.T) {
	script := writeFakeCLI(t, "sleep 300")
	m := New(Config{BinPath: script})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cb := &recCallbacks{}
	ex := &runtime.ExecContext{
		Ctx: ctx, Run: newRun(map[string]any{"instruction": "x"}),
		Instruction: "x", Callbacks: cb, Controls: make(chan runtime.Control, 8),
	}
	res := m.Execute(ex)
	if res.Outcome != runtime.OutcomeFailed || res.Failure == nil || res.Failure.Code != "context_cancelled" {
		t.Fatalf("预取消且无意图应 failed/context_cancelled: %+v", res)
	}
	if cb.spawned {
		t.Fatal("预取消不应启动进程")
	}
}

// ── 纯函数：策略参数 / 帧解析 ─────────────────────────────────────────

func TestClaudePolicyArgs(t *testing.T) {
	args := strings.Join(claudePolicyArgs(runtime.PolicySnapshot{Mode: "plan", Sandbox: "read-only"}), " ")
	if !strings.Contains(args, "--permission-mode plan") || strings.Contains(args, "Bash") || strings.Contains(args, "Edit") {
		t.Fatalf("plan/read-only 参数错误: %s", args)
	}
}

func TestReadFrameIsolatesNonJSON(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("noise line\n" + frameAssistant + "\n\n" + frameResultOK + "\n"))
	if f, err := readFrame(r, 1<<20); f != nil || err != nil {
		t.Fatalf("非 JSON 行应被隔离: %+v %v", f, err)
	}
	f, err := readFrame(r, 1<<20)
	if err != nil || f == nil || f.Type != "assistant" || assistantText(f) != "fake claude 输出" {
		t.Fatalf("assistant 帧解析错误: %+v %v", f, err)
	}
	if f, err := readFrame(r, 1<<20); f != nil || err != nil {
		t.Fatalf("空行应被跳过: %+v %v", f, err)
	}
	f, err = readFrame(r, 1<<20)
	if err != nil || f == nil || f.Type != "result" || f.Subtype != "success" {
		t.Fatalf("result 帧解析错误: %+v %v", f, err)
	}
	if u := f.usage(); u == nil || u.InputTokens != 25 || u.CachedTokens != 1000 || u.Basis != runtime.UsagePerRun {
		t.Fatalf("usage 解析错误: %+v", u)
	}
	if _, err := readFrame(r, 1<<20); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("流结束应返回 EOF: %v", err)
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(strings.Repeat("a", 64) + "\n"))
	_, err := readFrame(r, 16)
	var tooLarge frameTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("超限帧应返回 frameTooLargeError: %v", err)
	}
	if !strings.Contains(err.Error(), "frame exceeds 16 bytes") {
		t.Fatalf("错误消息应保持旧样式: %v", err)
	}
}
