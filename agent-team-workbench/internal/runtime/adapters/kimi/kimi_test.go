package kimi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	runlib "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// fakeCLIScript 自包含假 CLI：argv 逐行落盘到 $KIMI_FAKE_ARGV_FILE，
// 按 KIMI_FAKE_MODE 回放 stream-json 帧（ok/fail/quota/resume_missing/exit5/result_error/hang/big）。
const fakeCLIScript = `#!/bin/sh
printf '%s\n' "$@" > "$KIMI_FAKE_ARGV_FILE"
mode="${KIMI_FAKE_MODE:-ok}"
case "$mode" in
  fail)
    echo "error: failed to run prompt: provider.auth_error: 403 forbidden" >&2
    exit 0
    ;;
  quota)
    echo "error: failed to run prompt: provider.quota_exceeded: 429 rate limit" >&2
    exit 0
    ;;
  resume_missing)
    printf '{"role":"meta","type":"system.version","version":"0.38.0"}\n'
    echo 'error: failed to run prompt: Session "sess_resume_9" not found.' >&2
    echo 'See log: /tmp/kimi-home/logs/kimi-code.log' >&2
    exit 1
    ;;
  exit5)
    printf '{"role":"meta","type":"system.version","version":"0.38.0"}\n'
    printf '{"role":"assistant","text":"partial"}\n'
    exit 5
    ;;
  result_error)
    printf '{"role":"meta","type":"system.version","version":"0.38.0"}\n'
    printf '{"role":"assistant","text":"partial"}\n'
    printf '{"role":"result","type":"result","text":"upstream 503 bad gateway","is_error":true}\n'
    exit 0
    ;;
  hang)
    sleep 30
    exit 0
    ;;
  big)
    printf '{"filler":"'
    head -c 20000 /dev/zero | tr '\0' 'a'
    printf '"}\n'
    exit 0
    ;;
esac
printf '{"role":"meta","type":"system.version","version":"0.38.0"}\n'
printf '{"role":"assistant","text":"fake kimi 输出"}\n'
printf '{"role":"result","type":"result","text":"done","is_error":false}\n'
printf '{"role":"meta","type":"session.resume_hint","session_id":"sess_kimi_fake_1"}\n'
exit 0
`

type fakeCLI struct {
	bin  string
	argv string
}

func newFakeCLI(t *testing.T) *fakeCLI {
	t.Helper()
	if runlib.GOOS == "windows" {
		t.Skip("shell 假 CLI 仅在 unix 环境执行")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "kimi-fake")
	if err := os.WriteFile(bin, []byte(fakeCLIScript), 0o755); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(dir, "argv")
	t.Setenv("KIMI_FAKE_ARGV_FILE", argv)
	return &fakeCLI{bin: bin, argv: argv}
}

func (f *fakeCLI) mode(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("KIMI_FAKE_MODE", mode)
}

func (f *fakeCLI) argvLines(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(f.argv)
	if err != nil {
		t.Fatalf("假 CLI 未落盘 argv: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func newAdapter(t *testing.T, bin string) *Adapter {
	t.Helper()
	return New(Config{BinPath: bin, GracePeriod: time.Second})
}

func newRun(input map[string]any) *domain.ExecutionRun {
	if input == nil {
		input = map[string]any{}
	}
	if _, ok := input["instruction"]; !ok {
		input["instruction"] = "kimi fake run"
	}
	return &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), AdapterID: "kimi",
		Status: domain.RunQueued, Version: 1, Input: input,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

type recordedEvent struct {
	Type string
	Data map[string]any
}

type recordCallbacks struct {
	mu       sync.Mutex
	events   []recordedEvent
	logs     []string
	sessions []atwruntime.SessionUpdate
	spawned  bool
	pid      int
	pgid     int
}

func (c *recordCallbacks) OnEvent(eventType string, data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[string]any, len(data))
	for k, v := range data {
		cp[k] = v
	}
	c.events = append(c.events, recordedEvent{Type: eventType, Data: cp})
}

func (c *recordCallbacks) OnProgress(progress float64) {}

func (c *recordCallbacks) OnLog(stream, line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, stream+":"+line)
}

func (c *recordCallbacks) OnSpawn(pid, processGroupID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spawned = true
	c.pid = pid
	c.pgid = processGroupID
}

func (c *recordCallbacks) OnUsage(u atwruntime.Usage) {}

func (c *recordCallbacks) OnSession(update atwruntime.SessionUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions = append(c.sessions, update)
}

func (c *recordCallbacks) RequestApproval(kind, risk, summary string) string { return "" }

func runExecute(t *testing.T, a *Adapter, run *domain.ExecutionRun, session atwruntime.SessionState) (atwruntime.ExecResult, *recordCallbacks) {
	t.Helper()
	cb := &recordCallbacks{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex := &atwruntime.ExecContext{
		Ctx: ctx, Run: run, Instruction: "kimi fake run",
		Resolved: domain.ResolvedExecutionContext{CWD: t.TempDir(), AuthorizedRoot: t.TempDir()},
		Session:  session, Callbacks: cb, Controls: make(chan atwruntime.Control, 8),
	}
	return a.Execute(ex), cb
}

// surfaceEvents 过滤掉 Run Journal internal 相位事件（run.phase_* / run.log_chunk）。
func surfaceEvents(cb *recordCallbacks) []recordedEvent {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	var out []recordedEvent
	for _, ev := range cb.events {
		if domain.IsInternalEventName(ev.Type) {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func containsArg(argv []string, want string) bool {
	for _, arg := range argv {
		if arg == want {
			return true
		}
	}
	return false
}

func containsPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// ── Manifest / Probe ────────────────────────────────────────────────

func TestManifestCapabilities(t *testing.T) {
	a := New(Config{})
	m, err := a.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.AdapterID != "kimi" || m.AdapterVersion != "1.0.0" ||
		m.Protocol.Name != "kimi-cli-stream-json" || m.SchemaDigest != "sha256:kimi-cli-stream-json-v1" {
		t.Fatalf("manifest 标识漂移: %+v", m)
	}
	want := map[string]atwruntime.CapabilityLevel{
		"streaming":                                  atwruntime.CapSupported,
		atwruntime.CapabilityStructuredTransport:     atwruntime.CapSupported,
		atwruntime.CapabilitySchemaConstrainedOutput: atwruntime.CapUnavailable,
		atwruntime.CapabilityControlToolCall:         atwruntime.CapUnavailable,
		"resume":                                     atwruntime.CapSupported,
		"multi_turn":                                 atwruntime.CapSupported,
		"system_prompt":                              atwruntime.CapSupported,
		"modes":                                      atwruntime.CapSupported,
		"permissions":                                atwruntime.CapAdapterTranslated,
		"interrupt":                                  atwruntime.CapAdapterTranslated,
		"approval":                                   atwruntime.CapUnavailable,
		"workspace_files":                            atwruntime.CapSupported,
		"terminal":                                   atwruntime.CapUnavailable,
		"structured_output":                          atwruntime.CapAdapterTranslated,
	}
	if len(m.Capabilities) != len(want) {
		t.Fatalf("capabilities 键集漂移: %+v", m.Capabilities)
	}
	for key, level := range want {
		if m.Capabilities[key] != level {
			t.Errorf("capability %s = %s, want %s", key, m.Capabilities[key], level)
		}
	}
}

func TestProbe(t *testing.T) {
	a := New(Config{BinPath: filepath.Join(t.TempDir(), "missing-kimi")})
	res, err := a.Probe(context.Background(), atwruntime.ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Manifest == nil {
		t.Fatalf("缺失 CLI 应探测失败: %+v", res)
	}
	f := newFakeCLI(t)
	res, err = New(Config{BinPath: f.bin}).Probe(context.Background(), atwruntime.ProbeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Manifest == nil {
		t.Fatalf("假 CLI 应探测通过: %+v", res)
	}
}

// ── Execute：happy path / 事件契约 / 会话 ───────────────────────────

// meta → assistant → result → 收尾 meta：succeeded + message.completed + kimi:// ref。
func TestExecuteHappyPath(t *testing.T) {
	f := newFakeCLI(t)
	a := newAdapter(t, f.bin)
	res, cb := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("outcome = %s, failure %+v", res.Outcome, res.Failure)
	}
	if res.Failure != nil || res.Usage != nil {
		t.Fatalf("成功不应带 failure/usage（旧实现无 token 解析）: %+v %+v", res.Failure, res.Usage)
	}
	// surface 事件按既有契约断言；Run Journal internal 相位事件另行断言
	// （journal_test.go），同一 OnEvent 通道但只落 run_events。
	surface := surfaceEvents(cb)
	if len(surface) != 1 || surface[0].Type != domain.EventMessageCompleted {
		t.Fatalf("事件映射漂移: %+v", surface)
	}
	data := surface[0].Data
	if len(data) != 1 || data["text"] != "fake kimi 输出" {
		t.Fatalf("message.completed payload 应逐字段等于 {text}: %+v", data)
	}
	if len(cb.sessions) != 1 || cb.sessions[0].Ref != "kimi://sess_kimi_fake_1" {
		t.Fatalf("OnSession 未上报会话: %+v", cb.sessions)
	}
	if res.Session == nil || res.Session.Ref != "kimi://sess_kimi_fake_1" {
		t.Fatalf("ExecResult.Session 缺失: %+v", res.Session)
	}
	if !cb.spawned || cb.pid <= 0 || cb.pgid <= 0 {
		t.Fatalf("OnSpawn 未上报 pid/pgid: spawned=%v pid=%d pgid=%d", cb.spawned, cb.pid, cb.pgid)
	}
	want := []string{"-p", "kimi fake run", "--output-format", "stream-json"}
	got := f.argvLines(t)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for _, arg := range got {
		switch arg {
		case "--schema", "--output-schema", "--json-schema", "--response-format",
			"--tools", "--tool", "--tool-definition", "--tool-definitions":
			t.Fatalf("Kimi CLI 当前 adapter 不应发送 schema/tool 参数: %v", got)
		}
	}
}

// resume：-S <id> 挂载；首个 meta 无 id 时回退 resume id 并在 result 帧短路完成。
func TestExecuteResumeArgsAndSession(t *testing.T) {
	f := newFakeCLI(t)
	a := newAdapter(t, f.bin)
	res, cb := runExecute(t, a, newRun(nil), atwruntime.SessionState{Ref: "kimi://sess_resume_9"})
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("outcome = %s, failure %+v", res.Outcome, res.Failure)
	}
	argv := f.argvLines(t)
	if !containsPair(argv, "-S", "sess_resume_9") {
		t.Fatalf("resume 参数缺失: %v", argv)
	}
	if containsArg(argv, "--agent-file") {
		t.Fatalf("resume 轮不应生成 agent file: %v", argv)
	}
	if res.Session == nil || res.Session.Ref != "kimi://sess_resume_9" {
		t.Fatalf("resume 会话应回写 kimi://sess_resume_9: %+v", res.Session)
	}
	if len(cb.sessions) == 0 || cb.sessions[0].Ref != "kimi://sess_resume_9" {
		t.Fatalf("meta 回退应触发 OnSession: %+v", cb.sessions)
	}
}

// ── Execute：参数断言（策略 flag / model 快照 / read-only agent file）──

func TestExecutePolicyFlags(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		check func(t *testing.T, argv []string)
	}{
		{
			name:  "plan_mode",
			input: map[string]any{"mode": "plan"}, // PolicySnapshotOf 从 run.Input["mode"] 读
			check: func(t *testing.T, argv []string) {
				if !containsArg(argv, "--plan") || containsArg(argv, "--auto") {
					t.Fatalf("plan 模式应加 --plan 且无 --auto: %v", argv)
				}
			},
		},
		{
			name:  "auto_approval",
			input: map[string]any{"policy": map[string]any{"approval_policy": "auto"}},
			check: func(t *testing.T, argv []string) {
				if !containsArg(argv, "--auto") || containsArg(argv, "--plan") {
					t.Fatalf("auto 审批应加 --auto 且无 --plan: %v", argv)
				}
			},
		},
		{
			name:  "model_snapshot",
			input: map[string]any{"model": map[string]any{"model": "kimi-test-model"}},
			check: func(t *testing.T, argv []string) {
				if !containsPair(argv, "-m", "kimi-test-model") {
					t.Fatalf("模型快照应映射 -m: %v", argv)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCLI(t)
			a := newAdapter(t, f.bin)
			res, _ := runExecute(t, a, newRun(tc.input), atwruntime.SessionState{})
			if res.Outcome != atwruntime.OutcomeSucceeded {
				t.Fatalf("outcome = %s, failure %+v", res.Outcome, res.Failure)
			}
			tc.check(t, f.argvLines(t))
		})
	}
}

func TestExecuteReadOnlySandboxUsesAgentFile(t *testing.T) {
	f := newFakeCLI(t)
	a := newAdapter(t, f.bin)
	run := newRun(map[string]any{
		"system_prompt": "自定义角色",
		"policy":        map[string]any{"sandbox": "read-only"},
	})
	res, _ := runExecute(t, a, run, atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeSucceeded {
		t.Fatalf("outcome = %s, failure %+v", res.Outcome, res.Failure)
	}
	argv := f.argvLines(t)
	if !containsArg(argv, "--plan") {
		t.Errorf("read-only 沙箱应加 --plan: %v", argv)
	}
	if !containsArg(argv, "--agent-file") {
		t.Errorf("system prompt 应经 --agent-file 绑定: %v", argv)
	}
}

func TestCreateAgentFileCarriesPromptAndReadOnlyTools(t *testing.T) {
	path, dir, err := createAgentFile("自定义角色", atwruntime.PolicySnapshot{Sandbox: "read-only"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	config, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "system.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "${base_prompt}") || !strings.Contains(string(prompt), "自定义角色") {
		t.Fatalf("prompt 未保留 base 与 persona: %s", prompt)
	}
	if strings.Contains(string(config), "WriteFile") || strings.Contains(string(config), "Shell") {
		t.Fatalf("read-only agent 不应挂载写入工具: %s", config)
	}
}

// ── Execute：失败分类 ───────────────────────────────────────────────

func TestExecuteProviderErrorFamilies(t *testing.T) {
	cases := []struct {
		mode    string
		code    string
		family  atwruntime.ErrorFamily
		msgPart string
	}{
		{"fail", "provider_error", atwruntime.FamilyTransientUpstream, "provider.auth_error"},
		{"quota", "provider_error", atwruntime.FamilyProviderQuota, "quota_exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFakeCLI(t)
			f.mode(t, tc.mode)
			a := newAdapter(t, f.bin)
			res, cb := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
			if res.Outcome != atwruntime.OutcomeFailed {
				t.Fatalf("outcome = %s, want failed", res.Outcome)
			}
			if res.Failure == nil || res.Failure.Code != tc.code || res.Failure.Family != tc.family {
				t.Fatalf("failure = %+v, want code=%s family=%s", res.Failure, tc.code, tc.family)
			}
			if !strings.Contains(res.Failure.Message, tc.msgPart) {
				t.Errorf("message %q 应包含 %q", res.Failure.Message, tc.msgPart)
			}
			if len(cb.logs) == 0 {
				t.Errorf("stderr 应经 OnLog 上报")
			}
		})
	}
}

// 非零退出且无 result 帧 → failed / io。
func TestExecuteNonZeroExit(t *testing.T) {
	f := newFakeCLI(t)
	f.mode(t, "exit5")
	a := newAdapter(t, f.bin)
	res, _ := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Code != "stream_failed" || res.Failure.Family != atwruntime.FamilyIO {
		t.Fatalf("failure = %+v, want code=stream_failed family=io", res.Failure)
	}
	if !strings.Contains(res.Failure.Message, "exit status 5") {
		t.Errorf("message 应携带退出码: %q", res.Failure.Message)
	}
}

// result 帧 is_error → kimi_result_error / transient_upstream。
func TestExecuteResultFrameError(t *testing.T) {
	f := newFakeCLI(t)
	f.mode(t, "result_error")
	a := newAdapter(t, f.bin)
	res, _ := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Code != "kimi_result_error" ||
		res.Failure.Family != atwruntime.FamilyTransientUpstream {
		t.Fatalf("failure = %+v, want code=kimi_result_error family=transient_upstream", res.Failure)
	}
	if !strings.Contains(res.Failure.Message, "upstream 503") {
		t.Errorf("message 应携带 result 帧文本: %q", res.Failure.Message)
	}
}

// 防回归（P0 硬约束：resume 探测失败永不静默降级）：kimi CLI 恢复失败且 stderr
// 含会话不存在语义时必须报 session_unknown（不可重试）触发应用层 maybeSelfHeal
// 清锚点自愈——绝不落 transient/io 让盲目重试在死锚点上原地打转。fixture 文案
// 逐字取自 vendored kimi v0.38.0 对缺失会话的真实输出（exit 1 + 仅 system.version
// 帧 + stderr error 行）。
func TestExecuteResumeMissingSessionIsSessionUnknown(t *testing.T) {
	f := newFakeCLI(t)
	f.mode(t, "resume_missing")
	a := newAdapter(t, f.bin)
	res, cb := runExecute(t, a, newRun(nil), atwruntime.SessionState{Ref: "kimi://sess_resume_9"})
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Family != atwruntime.FamilySessionUnknown || res.Failure.Retryable {
		t.Fatalf("会话丢失必须 session_unknown 且不可重试: %+v", res.Failure)
	}
	if !strings.Contains(res.Failure.Message, `Session "sess_resume_9" not found`) {
		t.Errorf("message 应携带 CLI 原文: %q", res.Failure.Message)
	}
	if !containsPair(f.argvLines(t), "-S", "sess_resume_9") {
		t.Fatalf("应确为 resume 轮: %v", f.argvLines(t))
	}
	if len(cb.sessions) == 0 {
		t.Errorf("meta 回退仍应上报 OnSession（锚点回收交应用层）")
	}
}

// 负例：resume 轮的普通 provider 失败不误报 session_unknown——否则会无谓清掉
// 活锚点触发全量 fresh 重放。
func TestExecuteResumeFailureWithoutLossSemanticsStaysUpstream(t *testing.T) {
	f := newFakeCLI(t)
	f.mode(t, "fail")
	a := newAdapter(t, f.bin)
	res, _ := runExecute(t, a, newRun(nil), atwruntime.SessionState{Ref: "kimi://sess_resume_9"})
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Family != atwruntime.FamilyTransientUpstream || !res.Failure.Retryable {
		t.Fatalf("非丢失类失败应保持 transient_upstream 可重试: %+v", res.Failure)
	}
}

// turnFailure 分类表：引号夹 id / 直述两种丢失形态 → session_unknown 不可重试，
// quota 与网络/provider 类保持原分类。
func TestTurnFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		message   string
		code      string
		want      atwruntime.ErrorFamily
		retryable bool
	}{
		{"quoted_kimi_real_shape", `failed to run prompt: Session "sess_1" not found.`, "provider_error", atwruntime.FamilySessionUnknown, false},
		{"plain_session_not_found", "session not found", "kimi_result_error", atwruntime.FamilySessionUnknown, false},
		{"could_not_resume", "could not resume conversation", "provider_error", atwruntime.FamilySessionUnknown, false},
		{"quota_kept", "provider.quota_exceeded: 429 rate limit", "provider_error", atwruntime.FamilyProviderQuota, true},
		{"network_kept", "failed to run prompt: provider.auth_error: 403 forbidden", "provider_error", atwruntime.FamilyTransientUpstream, true},
		{"method_not_found_not_misread", "method not found", "provider_error", atwruntime.FamilyTransientUpstream, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := turnFailure(tc.code, tc.message)
			if f == nil || f.Code != tc.code || f.Family != tc.want || f.Retryable != tc.retryable {
				t.Fatalf("failure = %+v, want code=%s family=%s retryable=%v", f, tc.code, tc.want, tc.retryable)
			}
		})
	}
}

// 帧超限（流协议违约）→ stream_failed / internal。
func TestExecuteFrameOversizeIsInternal(t *testing.T) {
	f := newFakeCLI(t)
	f.mode(t, "big")
	a := New(Config{BinPath: f.bin,
		MaxFrameBytes: 1024, GracePeriod: time.Second})
	res, _ := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Code != "stream_failed" ||
		res.Failure.Family != atwruntime.FamilyInternal || res.Failure.Retryable {
		t.Fatalf("failure = %+v, want code=stream_failed family=internal retryable=false", res.Failure)
	}
	if !strings.Contains(res.Failure.Message, "frame exceeds") {
		t.Errorf("message 应携带超限信息: %q", res.Failure.Message)
	}
}

func TestExecuteRequiresInstruction(t *testing.T) {
	a := New(Config{BinPath: filepath.Join(t.TempDir(), "missing-kimi")})
	ex := &atwruntime.ExecContext{
		Ctx: context.Background(), Run: newRun(nil), Instruction: "",
		Callbacks: &recordCallbacks{}, Controls: make(chan atwruntime.Control, 8),
	}
	res := a.Execute(ex)
	if res.Outcome != atwruntime.OutcomeFailed || res.Failure == nil ||
		res.Failure.Code != "instruction_required" || res.Failure.Family != atwruntime.FamilyConfig {
		t.Fatalf("res = %+v %+v, want instruction_required/config", res.Outcome, res.Failure)
	}
}

func TestExecuteSpawnFailureIsConfig(t *testing.T) {
	a := newAdapter(t, filepath.Join(t.TempDir(), "missing-kimi"))
	res, _ := runExecute(t, a, newRun(nil), atwruntime.SessionState{})
	if res.Outcome != atwruntime.OutcomeFailed {
		t.Fatalf("outcome = %s, want failed", res.Outcome)
	}
	if res.Failure == nil || res.Failure.Code != "spawn_failed" || res.Failure.Family != atwruntime.FamilyConfig {
		t.Fatalf("failure = %+v, want code=spawn_failed family=config", res.Failure)
	}
}

// ── Execute：ctx 取消 ───────────────────────────────────────────────

// 无终态意图的 ctx 取消（如服务关停）默认 interrupted。
func TestExecuteContextCancelDefaultsToInterrupted(t *testing.T) {
	f := newFakeCLI(t)
	f.mode(t, "hang")
	a := newAdapter(t, f.bin)
	cb := &recordCallbacks{}
	ctx, cancel := context.WithCancel(context.Background())
	ex := &atwruntime.ExecContext{
		Ctx: ctx, Run: newRun(nil), Instruction: "kimi fake run",
		Session: atwruntime.SessionState{}, Callbacks: cb,
		Controls: make(chan atwruntime.Control, 8),
	}
	done := make(chan atwruntime.ExecResult, 1)
	go func() { done <- a.Execute(ex) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if res.Outcome != atwruntime.OutcomeInterrupted {
			t.Fatalf("outcome = %s, want interrupted", res.Outcome)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute 未随 ctx 取消返回")
	}
}

// F5：signalGroup 必须使用启动时采样的缓存 pgid——组长退出并被 Wait 收尸后
// 现场 Getpgid 返回 ESRCH，组级 SIGKILL 会静默退化为对已死组长的单进程信号，
// 孤儿成员（同组、持忽略 SIGINT）永远回收不到。本用例固化缓存契约。
func TestSignalGroupUsesCachedPGIDAfterLeaderReaped(t *testing.T) {
	if runlib.GOOS == "windows" {
		t.Skip("unix 进程组语义")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(dir, "kimi-fake")
	script := "#!/bin/sh\n" +
		"sh -c 'echo $$ > " + pidFile + "; sleep 30' &\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, err := atwruntime.TrustedCommand(bin)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = dir
	cmd.Env = os.Environ()
	setProcGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := processGroupID(cmd)
	if pgid <= 0 {
		t.Fatalf("启动时采样 pgid 失败: %d", pgid)
	}
	// 等孤儿落盘 pid，再收尸组长（此后现场 Getpgid 必失败）。
	deadline := time.Now().Add(5 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				childPID = n
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("未取到孤儿 pid")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("组长收尸: %v", err)
	}
	// 组长已收尸：组级 SIGKILL 只能经缓存 pgid 送达。
	signalGroup(cmd, pgid, sigKill)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return // ESRCH：孤儿已被组级信号回收
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("孤儿 pid=%d 未被组级 SIGKILL 回收（pgid 缓存未生效？）", childPID)
}

// sinkStub 实现 runtime.EngineSink，用于经 ModuleRunner 验证终态意图路径。
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

func (s *sinkStub) history() []domain.RunStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.RunStatus(nil), s.statuses...)
}

func waitForStatus(t *testing.T, s *sinkStub, want domain.RunStatus, timeout time.Duration) {
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

// ModuleRunner.Control 的终态意图 → cancelled / interrupted。
func TestModuleRunnerInterruptAndCancel(t *testing.T) {
	for _, terminal := range []domain.RunStatus{domain.RunInterrupted, domain.RunCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			f := newFakeCLI(t)
			f.mode(t, "hang")
			a := newAdapter(t, f.bin)
			sink := &sinkStub{}
			runner := atwruntime.NewModuleRunner(sink)
			runner.Register("kimi", a)
			run := newRun(nil)
			if err := runner.Dispatch(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			waitForStatus(t, sink, domain.RunRunning, 10*time.Second)
			runner.Control(run.ID, terminal)
			waitForStatus(t, sink, terminal, 10*time.Second)
		})
	}
}

// ── readFrame 单元 ─────────────────────────────────────────────────

func TestReadFrame(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(strings.Repeat("a", 32) + "\n"))
	if _, err := readFrame(r, 16); err == nil || !strings.Contains(err.Error(), "frame exceeds") {
		t.Fatalf("超限帧应报错: %v", err)
	}
	r2 := bufio.NewReader(strings.NewReader("not-json\n\n{\"role\":\"assistant\",\"text\":\"hi\"}\n"))
	if f, err := readFrame(r2, 1024); err != nil || f != nil {
		t.Fatalf("非 JSON 行应隔离跳过: %v %v", f, err)
	}
	if f, err := readFrame(r2, 1024); err != nil || f != nil {
		t.Fatalf("空行应跳过: %v %v", f, err)
	}
	f, err := readFrame(r2, 1024)
	if err != nil || f == nil || f.Role != "assistant" || f.Text != "hi" {
		t.Fatalf("帧解析: %+v %v", f, err)
	}
	if _, err := readFrame(r2, 1024); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("流尾应返回 ErrUnexpectedEOF: %v", err)
	}
}
