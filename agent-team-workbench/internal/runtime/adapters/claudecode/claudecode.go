// Package claudecode 实现 Claude Code CLI Adapter（协议文档 §9：Claude 行）。
//
// 传输：claude CLI print mode（-p --output-format stream-json --verbose），
// stdout NDJSON：system.init / assistant / result(subtype=success|error_*)。
// Adapter SPI v2：Execute 阻塞执行一轮 print-mode CLI，事件经 Callbacks 上报，
// 终态以 ExecResult.Outcome 表达。resume 通过 --resume <session_id> 支持；
// 取消为进程组级终止（process_scoped）。CLI flags / 事件 schema 随版本变化，
// 必须固定版本并以录制 fixture 做 conformance。
package claudecode

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

type Config struct {
	BinPath       string   // claude 可执行文件
	Args          []string // 覆盖默认参数（测试用回放桩）
	WorkspaceRoot string
	Model         string // 可选 --model
	MaxFrameBytes int
	GracePeriod   time.Duration
}

// Module 是 Claude Code 的 Adapter SPI v2 模块（无共享执行态，Execute 自包含）。
type Module struct {
	cfg Config
}

var _ runtime.AdapterModule = (*Module)(nil)

func New(cfg Config) *Module {
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = 8 << 20
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 3 * time.Second
	}
	return &Module{cfg: cfg}
}

func (m *Module) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "claude-code", AdapterVersion: "1.0.0",
		Protocol: runtime.Protocol{Name: "claude-cli-stream-json", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming":     runtime.CapSupported,
			"resume":        runtime.CapSupported, // --resume session_id
			"multi_turn":    runtime.CapSupported,
			"system_prompt": runtime.CapSupported,
			"modes":         runtime.CapSupported,
			"permissions":   runtime.CapSupported,
			// CLI 无精确取消：进程组终止（process_scoped）。
			"interrupt":         runtime.CapAdapterTranslated,
			"approval":          runtime.CapUnavailable, // MVP 不接 permission prompt tool
			"workspace_files":   runtime.CapSupported,
			"terminal":          runtime.CapUnavailable,
			"structured_output": runtime.CapSupported,
			// 无 steering：print mode 单 Run 单 prompt，不消费 mid-run input。
		},
		SchemaDigest: "sha256:claude-cli-stream-json-v1",
	}, nil
}

// Probe 校验 CLI 可用（路径存在）；不启动业务 Run。
func (m *Module) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	mf, _ := m.Manifest(ctx)
	if _, err := exec.LookPath(m.cfg.BinPath); err != nil {
		if _, serr := os.Stat(m.cfg.BinPath); serr != nil {
			return runtime.ProbeResult{OK: false, Manifest: &mf, Error: "claude CLI 不可用"}, nil
		}
	}
	return runtime.ProbeResult{OK: true, Manifest: &mf}, nil
}

// Execute 阻塞执行一轮 print-mode CLI；进展经 ex.Callbacks 上报，终态经返回值表达。
func (m *Module) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	// print mode 无 steering/approval 能力：消费并忽略对应 Control（终态意图经 Ctx 取消表达）。
	stopDrain := drainControls(ex)
	defer close(stopDrain)

	ctx := ex.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return terminalByIntent(ex, "context cancelled before spawn")
	}
	if strings.TrimSpace(ex.Instruction) == "" {
		f := runtime.Failure{Family: runtime.FamilyConfig, Code: "instruction_required",
			Message: "claude print mode 需要非空 instruction"}
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: &f}
	}

	resumeID := runtime.SessionIDFromRef(ex.Session.Ref, "claude")

	args := m.cfg.Args
	if len(args) == 0 {
		args = []string{"-p", ex.Instruction, "--output-format", "stream-json", "--verbose"}
		if resumeID != "" {
			args = append(args, "--resume", resumeID)
		} else if systemPrompt := strings.TrimSpace(runtime.SystemPromptOf(ex.Run)); systemPrompt != "" {
			args = append(args, "--append-system-prompt", systemPrompt)
		}
		args = append(args, claudePolicyArgs(runtime.PolicySnapshotOf(ex.Run))...)
		// 模型注册表快照（orchestrator 写入 run.Input）：per-run 覆盖 --model；凭据由 CLI 自身配置管理。
		if model := firstNonEmpty(runtime.ModelSnapshotOf(ex.Run).Model, m.cfg.Model); model != "" {
			args = append(args, "--model", model)
		}
	}

	cmd, err := runtime.TrustedCommand(m.cfg.BinPath, args...) // 终止走 watchCancel 的进程组语义
	if err != nil {
		return spawnFailure(err)
	}
	cmd.Dir = m.cfg.WorkspaceRoot
	cmd.Env = os.Environ()
	setProcGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return spawnFailure(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return spawnFailure(err)
	}
	if err := cmd.Start(); err != nil {
		return spawnFailure(err)
	}
	// pgid 必须在组长存活时采样：组长死亡（僵尸）后 Getpgid 失败，
	// 会导致组级 SIGKILL 升级打不出去（孤儿持管道 → 读循环永久阻塞）。
	pgid := processGroupID(cmd)
	ex.Callbacks.OnSpawn(cmd.Process.Pid, pgid)

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		drainStderr(stderr, ex.Callbacks)
	}()
	stopWatch := make(chan struct{})
	go watchCancel(ctx, cmd, pgid, m.cfg.GracePeriod, stopWatch)

	st := &streamState{callbacks: ex.Callbacks, resumeID: resumeID}
	// io.LimitReader 约束读取上限（MaxFrameBytes+1）：超长行在进入内存前就被
	// 截停，readFrame 的长度检查随即报错——避免先整行缓冲再判超长。
	reader := bufio.NewReaderSize(io.LimitReader(stdout, int64(m.cfg.MaxFrameBytes)+1), 64*1024)
	for {
		frame, err := readFrame(reader, m.cfg.MaxFrameBytes)
		if frame != nil {
			st.apply(frame)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				st.streamErr = err
			}
			break
		}
	}
	<-stderrDone
	close(stopWatch)
	waitErr := cmd.Wait()

	return m.finalize(ex, st, waitErr)
}

// finalize 汇总终态：result 帧权威，其次退出码，最后按 Ctx/终态意图。
func (m *Module) finalize(ex *runtime.ExecContext, st *streamState, waitErr error) runtime.ExecResult {
	result := runtime.ExecResult{Usage: st.usage, Session: st.session}
	switch {
	case st.failCode != "":
		f := classifyCLIError(st.failCode, st.failMsg)
		result.Outcome, result.Failure = runtime.OutcomeFailed, &f
	case st.succeeded:
		result.Outcome = runtime.OutcomeSucceeded
	case ex.Ctx != nil && ex.Ctx.Err() != nil:
		terminal := terminalByIntent(ex, "context cancelled")
		terminal.Usage, terminal.Session = st.usage, st.session
		return terminal
	case st.streamErr != nil:
		f := runtime.Failure{Family: runtime.FamilyIO, Code: "stream_failed",
			Message: truncateMessage(st.streamErr.Error())}
		var tooLarge frameTooLargeError
		if errors.As(st.streamErr, &tooLarge) {
			f.Family = runtime.FamilyInternal
		}
		result.Outcome, result.Failure = runtime.OutcomeFailed, &f
	case waitErr == nil:
		// 干净退出且无 result 帧：退出码 0 视为成功。
		result.Outcome = runtime.OutcomeSucceeded
	default:
		f := runtime.Failure{Family: runtime.FamilyIO, Code: "stream_failed",
			Message: truncateMessage("process exited without result frame: " + waitErr.Error())}
		result.Outcome, result.Failure = runtime.OutcomeFailed, &f
	}
	return result
}

// terminalByIntent Ctx 取消后的终态：有终态意图 → interrupted/cancelled，否则 failed。
func terminalByIntent(ex *runtime.ExecContext, detail string) runtime.ExecResult {
	if kind, ok := ex.TerminalIntent(); ok {
		if kind == runtime.ControlCancel {
			return runtime.ExecResult{Outcome: runtime.OutcomeCancelled}
		}
		return runtime.ExecResult{Outcome: runtime.OutcomeInterrupted}
	}
	f := runtime.Failure{Family: runtime.FamilyIO, Code: "context_cancelled",
		Message: truncateMessage(detail)}
	return runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: &f}
}

// streamState 累积一轮执行的流解析结果。
type streamState struct {
	callbacks runtime.Callbacks
	resumeID  string

	session   *runtime.SessionUpdate
	usage     *runtime.Usage
	succeeded bool
	failCode  string
	failMsg   string
	streamErr error
}

// apply 把 stream-json 帧映射为 canonical 事件与会话/用量（映射逻辑与旧版逐字段一致）。
func (s *streamState) apply(frame *streamFrame) {
	switch frame.Type {
	case "system":
		sessionID := firstNonEmpty(frame.SessionID, s.resumeID)
		if sessionID == "" {
			return
		}
		if s.session != nil && s.session.Ref == "claude://"+sessionID {
			return
		}
		s.session = &runtime.SessionUpdate{Ref: "claude://" + sessionID}
		s.callbacks.OnSession(*s.session)
	case "assistant":
		s.callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{
			"text": assistantText(frame),
		})
	case "result":
		if u := frame.usage(); u != nil {
			s.usage = u
		}
		if frame.Subtype == "success" {
			s.succeeded = true
		} else {
			s.failCode = "claude_" + frame.Subtype
			s.failMsg = resultError(frame)
		}
	}
}

// spawnFailure CLI 无法启动（不存在/无权限）：环境配置问题。
func spawnFailure(err error) runtime.ExecResult {
	f := runtime.Failure{Family: runtime.FamilyConfig, Code: "spawn_failed",
		Message: truncateMessage(err.Error())}
	return runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: &f}
}

// classifyCLIError 按输出内容诚实分类：quota/网络可重试，resume 目标丢失为 session_unknown，其余 internal。
func classifyCLIError(code, message string) runtime.Failure {
	msg := strings.ToLower(message)
	f := runtime.Failure{Family: runtime.FamilyInternal, Code: code,
		Message: truncateMessage(message)}
	switch {
	case containsAny(msg, "usage limit", "quota", "rate limit", "credit balance", "billing"):
		f.Family, f.Retryable = runtime.FamilyProviderQuota, true
	case containsAny(msg, "network", "connection", "econnrefused", "econnreset",
		"timeout", "timed out", "overloaded", "fetch failed", "api_error", "529"):
		f.Family, f.Retryable = runtime.FamilyTransientUpstream, true
	case containsAny(msg, "no conversation found", "session not found", "could not resume", "invalid session"):
		f.Family = runtime.FamilySessionUnknown
	}
	return f
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// truncateMessage 失败消息截断（与旧 failRun 的 200 字节上限一致）。
func truncateMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		return msg[:200]
	}
	return msg
}

func claudePolicyArgs(policy runtime.PolicySnapshot) []string {
	var args []string
	mode := "manual"
	if policy.Mode == "plan" || policy.Sandbox == "read-only" {
		mode = "plan"
	} else if policy.ApprovalPolicy == "auto" {
		mode = "auto"
	} else if policy.ApprovalPolicy == "approve_high_risk" {
		mode = "acceptEdits"
	}
	args = append(args, "--permission-mode", mode)
	if tools := claudeTools(policy); len(tools) > 0 {
		args = append(args, "--tools", strings.Join(tools, ","))
	}
	return args
}

func claudeTools(policy runtime.PolicySnapshot) []string {
	requested := policy.Tools
	if len(requested) == 0 && policy.Sandbox != "read-only" {
		return nil
	}
	if len(requested) == 0 {
		requested = []string{"fs", "todo"}
	}
	byName := map[string][]string{
		"fs":     {"Read", "Glob", "Grep"},
		"editor": {"Edit", "Write"},
		"bash":   {"Bash"}, "shell": {"Bash"},
		"todo": {"TodoWrite"},
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range requested {
		for _, tool := range byName[name] {
			if !seen[tool] {
				seen[tool] = true
				out = append(out, tool)
			}
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// drainStderr 把 stderr 原始行送 OnLog（超长截断，与旧实现一致）。
func drainStderr(stderr io.Reader, callbacks runtime.Callbacks) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 16*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 2048 {
			line = line[:2048] + "…(truncated)"
		}
		callbacks.OnLog("stderr", line)
	}
}

// drainControls 消费并忽略未声明能力的 Control（input/approval）。
func drainControls(ex *runtime.ExecContext) chan struct{} {
	stop := make(chan struct{})
	if ex.Controls == nil {
		return stop
	}
	go func() {
		for {
			select {
			case _, ok := <-ex.Controls:
				if !ok {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	return stop
}

// watchCancel Ctx 取消时做进程组级终止：先 SIGINT，宽限期后升级 SIGKILL。
// pgid 为启动时采样值（组长死后进程组在成员存活期间仍可寻址，故必须缓存）。
func watchCancel(ctx context.Context, cmd *exec.Cmd, pgid int, grace time.Duration, stop <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-stop:
		return
	}
	signalGroup(cmd, pgid, sigInt)
	select {
	case <-time.After(grace):
		signalGroup(cmd, pgid, sigKill)
	case <-stop:
	}
}
