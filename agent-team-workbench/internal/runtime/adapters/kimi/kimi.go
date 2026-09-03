// Package kimi 实现 Kimi Code CLI Adapter（协议文档 §9：Kimi 行，Adapter SPI v2）。
//
// 传输：kimi CLI print mode（kimi -p --output-format stream-json），
// stdout NDJSON 以 role/type 区分（meta → assistant → result）；
// 诊断与 provider 错误走 stderr（"error: failed to run prompt: ..."），捕获后 fail loud。
// Execute 阻塞到本轮结束：spawn（进程组）→ stdout 流解析 → Callbacks
// （canonical 事件 / OnSession / OnLog）→ 结构化 ExecResult。
// resume 经 -S <session id>；恢复目标丢失（会话不存在）按 stderr 文案分类为
// session_unknown（不可重试）交应用层自愈清锚点——永不静默降级 fresh，也绝不
// 落 transient/io 让盲目重试原地打转；取消为进程组级终止（process_scoped）。
// ACP JSON-RPC 面（new/load/resume/prompt/cancel）在 M4 按部署需要接入；
// 能力声明以 Manifest 为准，禁止静默降级。
package kimi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/agentwork/kimiconfig"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Config 与旧版对齐；Args 仅测试回放桩使用。
type Config struct {
	BinPath       string   // kimi 可执行文件
	Args          []string // 覆盖默认参数（测试用回放桩）
	Home          string   // KIMI_CODE_HOME 项目空间（默认 .agent-work/kimi）
	Model         string   // 可选 -m
	MaxFrameBytes int
	GracePeriod   time.Duration
}

// Adapter 实现 runtime.AdapterModule；无内部执行面状态，一次 Execute 一个进程。
type Adapter struct {
	cfg Config
}

var _ runtime.AdapterModule = (*Adapter)(nil)

func New(cfg Config) *Adapter {
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = 8 << 20
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 3 * time.Second
	}
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "kimi", AdapterVersion: "1.0.0",
		Protocol: runtime.Protocol{Name: "kimi-cli-stream-json", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming":                               runtime.CapSupported,
			runtime.CapabilityStructuredTransport:     runtime.CapSupported,
			runtime.CapabilitySchemaConstrainedOutput: runtime.CapUnavailable,
			runtime.CapabilityControlToolCall:         runtime.CapUnavailable,
			"resume":                                  runtime.CapSupported, // -S <session id>
			"multi_turn":                              runtime.CapSupported,
			"system_prompt":                           runtime.CapSupported,         // --agent-file 在首轮绑定
			"modes":                                   runtime.CapSupported,         // --plan
			"permissions":                             runtime.CapAdapterTranslated, // --auto/--yolo/--plan + agent toolset
			// print mode 无精确取消：进程组终止（process_scoped）。
			"interrupt":       runtime.CapAdapterTranslated,
			"approval":        runtime.CapUnavailable, // M4 接 ACP permission
			"workspace_files": runtime.CapSupported,
			"terminal":        runtime.CapUnavailable,
			// stream-json is a transport framing mode, not provider-enforced
			// structured output.
			"structured_output": runtime.CapAdapterTranslated,
		},
		SchemaDigest: "sha256:kimi-cli-stream-json-v1",
	}, nil
}

// Probe 校验 CLI 可用；不启动业务 Run。
func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	if _, err := exec.LookPath(a.cfg.BinPath); err != nil {
		if _, serr := os.Stat(a.cfg.BinPath); serr != nil {
			return runtime.ProbeResult{OK: false, Manifest: &m, Error: "kimi CLI 不可用"}, nil
		}
	}
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

// Execute 阻塞执行一轮 Kimi print mode；事件映射与旧实现逐字段一致，
// 仅输出通道换为 ex.Callbacks。steering/approval 能力未声明（Manifest：
// approval=unavailable，无 steering 键）：ControlInput/ControlApproval 一律
// 忽略不消费；终态意图（interrupt/cancel）经 Ctx 取消 + TerminalIntent 传达。
func (a *Adapter) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	if ex.Instruction == "" {
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: configFailure("instruction_required", "instruction required")}
	}
	// 模型注册表快照（orchestrator 写入 run.Input）：per-run 覆盖 -m。
	snap := runtime.ModelSnapshotOf(ex.Run)
	if (snap.Model != "" || snap.Provider != "") && strings.TrimSpace(a.cfg.Home) != "" {
		if err := kimiconfig.ApplySnapshot(a.cfg.Home, snap); err != nil {
			return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
				Failure: configFailure("kimi_config", err.Error())}
		}
	}
	model := kimiconfig.ModelAlias(snap)
	if model == "" {
		model = a.cfg.Model
	}
	systemPrompt := runtime.SystemPromptOf(ex.Run)
	policy := runtime.PolicySnapshotOf(ex.Run)
	resumeSessionID := runtime.SessionIDFromRef(ex.Session.Ref, "kimi")

	var agentDir string
	args := a.cfg.Args
	if len(args) == 0 {
		args = []string{"-p", ex.Instruction, "--output-format", "stream-json"}
		if resumeSessionID != "" {
			args = append(args, "-S", resumeSessionID)
		} else if agentFile, dir, err := createAgentFile(systemPrompt, policy); err != nil {
			return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
				Failure: ioFailure("agent_config_failed", err.Error(), false)}
		} else if agentFile != "" {
			agentDir = dir
			args = append(args, "--agent-file", agentFile)
		}
		if policy.Mode == "plan" || policy.Sandbox == "read-only" {
			args = append(args, "--plan")
		} else if policy.ApprovalPolicy == "auto" {
			args = append(args, "--auto")
		}
		if model == "" {
			model = a.cfg.Model
		}
		if model != "" {
			args = append(args, "-m", model)
		}
	}
	if agentDir != "" {
		defer func() { _ = os.RemoveAll(agentDir) }()
	}

	cmd, err := runtime.TrustedCommand(a.cfg.BinPath, args...)
	if err != nil {
		// CLI 缺失/不可执行属于环境配置问题。
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: configFailure("spawn_failed", err.Error())}
	}
	// 工作目录只来自 Host resolver 的进程内可信产物（RFC §5.1.9）；
	// 无 Resolved（未注入 resolver 的测试装配）回退进程 cwd。
	cmd.Dir = ex.Resolved.CWD
	if cmd.Dir == "" {
		cmd.Dir = "."
	}
	cmd.Env = a.processEnv()
	setProcGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: configFailure("spawn_failed", err.Error())}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: configFailure("spawn_failed", err.Error())}
	}
	if err := cmd.Start(); err != nil {
		// CLI 缺失/不可执行属于环境配置问题。
		return runtime.ExecResult{Outcome: runtime.OutcomeFailed,
			Failure: configFailure("spawn_failed", err.Error())}
	}
	// pgid 必须在组长存活时采样缓存（对齐 claudecode/codexapp）：组长死亡
	// （僵尸/已被收尸）后 Getpgid 失败，现场重查会让组级 SIGINT/SIGKILL 打不
	// 出去——忽略 SIGINT 的孤儿成员持有 stdout/stderr 管道时 Execute 挂起。
	pgid := processGroupID(cmd)
	ex.Callbacks.OnSpawn(cmd.Process.Pid, pgid)

	state := &streamState{}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		drainStderr(stderr, ex.Callbacks, state)
	}()

	// Ctx 取消 → 进程组 SIGINT → 宽限 → SIGKILL（沿用旧 terminate 语义；
	// 不用 exec.CommandContext 的单进程 Kill，保证整组可靠终止）。
	waited := make(chan struct{})
	defer close(waited)
	go func() {
		select {
		case <-ex.Ctx.Done():
		case <-waited:
			return
		}
		signalGroup(cmd, pgid, sigInt)
		select {
		case <-waited:
		case <-time.After(a.cfg.GracePeriod):
			signalGroup(cmd, pgid, sigKill)
		}
	}()

	// io.LimitReader 约束读取上限（MaxFrameBytes+1）：超长行在进入内存前就被
	// 截停，readFrame 的长度检查随即报错——避免先整行缓冲再判超长。
	reader := bufio.NewReaderSize(io.LimitReader(stdout, int64(a.cfg.MaxFrameBytes)+1), 64*1024)
	var (
		finished      bool // result 帧已见
		resultOK      bool
		resultMessage string
		completed     bool // 显式完成（resume 短路或收尾 meta）
		readErr       error
		session       *runtime.SessionUpdate
	)
	for !completed {
		frame, err := readFrame(reader, a.cfg.MaxFrameBytes)
		if err != nil {
			readErr = err
			break
		}
		if frame == nil {
			continue
		}
		switch {
		case frame.Role == "meta":
			sessionID := firstNonEmpty(frame.SessionID, frame.SessionIDCamel)
			if sessionID == "" {
				sessionID = resumeSessionID
			}
			if sessionID != "" {
				session = &runtime.SessionUpdate{Ref: "kimi://" + sessionID}
				ex.Callbacks.OnSession(*session)
			}
			if finished && (frame.Type == "session.resume_hint" || sessionID != "") {
				completed = true
			}
		case frame.Role == "assistant":
			ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{
				"text": frame.Text,
			})
		case frame.Role == "result" || frame.Type == "result":
			finished = true
			resultOK = !frame.IsError
			resultMessage = firstNonEmpty(frame.Text, "kimi result error")
			if resumeSessionID != "" {
				completed = true
			}
		}
	}

	// 回收进程：SIGINT → 宽限 → SIGKILL，随后取退出码（同旧 terminate）。
	signalGroup(cmd, pgid, sigInt)
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-time.After(a.cfg.GracePeriod):
		signalGroup(cmd, pgid, sigKill)
		waitErr = <-waitCh
	}
	<-stderrDone

	return terminalResult(ex, terminalInputs{
		finished:      finished,
		resultOK:      resultOK,
		resultMessage: resultMessage,
		readErr:       readErr,
		waitErr:       waitErr,
		stderrErr:     state.stderrErr,
		session:       session,
	})
}

type terminalInputs struct {
	finished      bool
	resultOK      bool
	resultMessage string
	readErr       error
	waitErr       error
	stderrErr     string
	session       *runtime.SessionUpdate
}

// terminalResult 终态判定优先级：终态意图 > result 帧 > 退出码 > 流中断分类。
func terminalResult(ex *runtime.ExecContext, in terminalInputs) runtime.ExecResult {
	result := runtime.ExecResult{Session: in.session}
	if ex.Ctx.Err() != nil {
		// 进程被终止：按终态意图区分 cancelled/interrupted；
		// 无意图（如服务关停）按 interrupted（保留 resume 时机）。
		if kind, ok := ex.TerminalIntent(); ok && kind == runtime.ControlCancel {
			result.Outcome = runtime.OutcomeCancelled
		} else {
			result.Outcome = runtime.OutcomeInterrupted
		}
		return result
	}
	switch {
	case in.finished && !in.resultOK:
		result.Outcome = runtime.OutcomeFailed
		result.Failure = turnFailure("kimi_result_error", in.resultMessage)
	case in.finished && in.waitErr != nil && !exitedBySignal(in.waitErr):
		// result 帧成功但 CLI 非零退出（罕见）：按 stderr 文本诚实分类，无则 IO。
		result.Outcome = runtime.OutcomeFailed
		if in.stderrErr != "" {
			result.Failure = turnFailure("exit_failed", in.stderrErr)
		} else {
			result.Failure = ioFailure("exit_failed", fmt.Sprintf("kimi exited: %v", in.waitErr), true)
		}
	case in.finished:
		// waitErr 为信号终止时是回收 SIGINT 所致（同旧实现：结果帧已定终态）。
		result.Outcome = runtime.OutcomeSucceeded
	case in.readErr != nil && !errors.Is(in.readErr, io.ErrUnexpectedEOF):
		// 帧超限等流协议违约。
		result.Outcome = runtime.OutcomeFailed
		result.Failure = &runtime.Failure{
			Family: runtime.FamilyInternal, Code: "stream_failed",
			Message: truncateMessage(in.readErr.Error()), Retryable: false,
		}
	case in.stderrErr != "":
		// 流中断且 stderr 捕获到 provider 错误：fail loud。
		result.Outcome = runtime.OutcomeFailed
		result.Failure = turnFailure("provider_error", in.stderrErr)
	default:
		detail := "stream ended without result frame"
		if in.readErr != nil {
			detail = in.readErr.Error()
		}
		if in.waitErr != nil {
			detail = fmt.Sprintf("%s; exit: %v", detail, in.waitErr)
		}
		result.Outcome = runtime.OutcomeFailed
		result.Failure = ioFailure("stream_failed", detail, true)
	}
	return result
}

// turnFailure provider 侧本轮错误分类：quota/429/rate limit → provider_quota；
// 会话丢失（resume 目标已不存在）→ session_unknown 不可重试——死锚点盲目重试
// 只会原地失败，交应用层自愈清锚点后用全量历史 fresh 重试；其余 → transient_upstream。
// 丢失语义优先于 quota 判定（携带双语义的文本按不可重试处理）。
func turnFailure(code, message string) *runtime.Failure {
	low := strings.ToLower(message)
	family, retryable := runtime.FamilyTransientUpstream, true
	switch {
	case isSessionLostMessage(low):
		family, retryable = runtime.FamilySessionUnknown, false
	case strings.Contains(low, "quota") || strings.Contains(low, "429") || strings.Contains(low, "rate limit"):
		family = runtime.FamilyProviderQuota
	}
	return &runtime.Failure{Family: family, Code: code, Message: truncateMessage(message), Retryable: retryable}
}

// isSessionLostMessage 判断失败文本是否表达 resume 目标丢失。不用裸 "not
// found"——会误吞 "method not found"/"model not found" 等无关错误（同 codexapp
// 注释纪律）。kimi v0.38.0 对缺失会话的实测 stderr 为引号夹 id 形态，直述串
// 匹配不到：
//
//	error: failed to run prompt: Session "sess_x" not found.
func isSessionLostMessage(message string) bool {
	low := strings.ToLower(message)
	if containsAny(low,
		"session not found", "conversation not found", "no conversation found",
		"unknown session", "no such session", "could not resume", "invalid session") {
		return true
	}
	return strings.Contains(low, `session "`) && strings.Contains(low, `" not found`)
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func configFailure(code, message string) *runtime.Failure {
	return &runtime.Failure{Family: runtime.FamilyConfig, Code: code, Message: truncateMessage(message), Retryable: false}
}

func ioFailure(code, message string, retryable bool) *runtime.Failure {
	return &runtime.Failure{Family: runtime.FamilyIO, Code: code, Message: truncateMessage(message), Retryable: retryable}
}

// truncateMessage 与旧 failRun 一致：trim + 200 字符截断。
func truncateMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// streamState 由 stderr drain 协程独占写；Execute 在 stderrDone 关闭后读取（无锁）。
type streamState struct {
	stderrErr string // 捕获的 provider 错误（"error: " 前缀行）
}

// stream-json 帧：role/type 判别；尽力提取文本。
type streamFrame struct {
	Role           string `json:"role"`
	Type           string `json:"type"`
	Text           string `json:"text"`
	Content        string `json:"content"`
	IsError        bool   `json:"is_error"`
	SessionID      string `json:"session_id"`
	SessionIDCamel string `json:"sessionId"`
}

func createAgentFile(systemPrompt string, policy runtime.PolicySnapshot) (string, string, error) {
	needToolPolicy := len(policy.Tools) > 0 || policy.Sandbox == "read-only"
	if strings.TrimSpace(systemPrompt) == "" && !needToolPolicy {
		return "", "", nil
	}
	dir, err := os.MkdirTemp("", "atw-kimi-agent-")
	if err != nil {
		return "", "", err
	}
	prompt := strings.TrimSpace(systemPrompt)
	if prompt == "" {
		prompt = "${base_prompt}\n\nYou are an agent operating under the workbench policy."
	} else {
		prompt = "${base_prompt}\n\n" + prompt
	}
	if err := os.WriteFile(filepath.Join(dir, "system.md"), []byte(prompt+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	var b strings.Builder
	b.WriteString("version: 1\nagent:\n  extend: default\n  name: agent-team-workbench\n  system_prompt_path: ./system.md\n")
	tools := kimiTools(policy)
	if len(tools) > 0 {
		b.WriteString("  tools:\n")
		for _, tool := range tools {
			fmt.Fprintf(&b, "    - %q\n", tool)
		}
	}
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return path, dir, nil
}

func kimiTools(policy runtime.PolicySnapshot) []string {
	requested := policy.Tools
	if len(requested) == 0 && policy.Sandbox != "read-only" {
		return nil
	}
	if len(requested) == 0 {
		requested = []string{"fs", "todo"}
	}
	byName := map[string][]string{
		"fs":     {"kimi_cli.tools.file:ReadFile", "kimi_cli.tools.file:ReadMediaFile", "kimi_cli.tools.file:Glob", "kimi_cli.tools.file:Grep"},
		"editor": {"kimi_cli.tools.file:WriteFile", "kimi_cli.tools.file:StrReplaceFile"},
		"bash":   {"kimi_cli.tools.shell:Shell"}, "shell": {"kimi_cli.tools.shell:Shell"},
		"todo": {"kimi_cli.tools.todo:SetTodoList"},
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

// drainStderr：stderr → OnLog（截断同旧实现）；"error: " 前缀行捕获为 provider 错误。
func drainStderr(stderr io.Reader, cb runtime.Callbacks, state *streamState) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 16*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "error:") && state.stderrErr == "" {
			state.stderrErr = strings.TrimPrefix(line, "error: ")
		}
		if len(line) > 2048 {
			line = line[:2048] + "…(truncated)"
		}
		cb.OnLog("stderr", line)
	}
}

func readFrame(r *bufio.Reader, maxBytes int) (*streamFrame, error) {
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	if len(line) > maxBytes {
		return nil, fmt.Errorf("frame exceeds %d bytes", maxBytes)
	}
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		if err != nil {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, nil
	}
	var f streamFrame
	if err := json.Unmarshal([]byte(trimmed), &f); err != nil {
		return nil, nil // 非 JSON 行：隔离不执行
	}
	return &f, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *Adapter) processEnv() []string {
	if strings.TrimSpace(a.cfg.Home) == "" {
		return os.Environ()
	}
	return agentwork.WithEnv(os.Environ(), "KIMI_CODE_HOME", a.cfg.Home)
}
