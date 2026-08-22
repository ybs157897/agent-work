// Package dsh 实现 DeepSeek Harness SDK Adapter（协议文档 §9 / §9.1）。
//
// 传输：Go Runner 启动 dsh-jsonrpc-agent 子进程，stdin/stdout 承载
// JSON-RPC 2.0 NDJSON，stderr 仅诊断。initialize 固化 cwd/provider/model；
// session/prompt 返回 messageId 只表示 durable inbox 接受；Adapter 收集
// session.event 并在目标 session.status=idle 时判定本轮结束。
//
// 取消语义：SDK 无 prompt cancel —— 单 Runtime 单 Run，进程组级终止实现
// cancellation（interrupt 能力声明为 adapter_translated / process_scoped）。
package dsh

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Engine 是 Adapter 依赖的应用层能力面（控制平面或 Runner 侧均可实现）。
type Engine interface {
	RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error
	RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error
}

// Config 进程与环境通道。凭据经 DEEPSEEK_API_KEY 白名单传入，
// 绝不进入 argv / 普通日志。
type Config struct {
	BinPath       string // dsh-jsonrpc-agent 可执行文件
	ConfigPath    string // cordis.yml 组合配置
	WorkspaceRoot string // DSH_CWD：Runner 本地授权根目录
	SessionRoot   string // DSH_SESSION_ROOT：会话持久化根
	Model         string // DSH_MODEL
	Provider      string // initialize.provider（默认 deepseek）
	SystemPrompt  string // DSH_SYSTEM_PROMPT：Agent persona
	MaxFrameBytes int    // JSONL 帧上限；超限安全失败（默认 8MiB）
	GracePeriod   time.Duration
}

type Adapter struct {
	cfg    Config
	engine Engine

	mu       sync.Mutex
	active   map[string]*runProc
	shutdown bool
}

var _ runtime.RuntimeAdapter = (*Adapter)(nil)

func New(cfg Config, engine Engine) *Adapter {
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = 8 << 20
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 3 * time.Second
	}
	if cfg.Provider == "" {
		cfg.Provider = "deepseek"
	}
	return &Adapter{cfg: cfg, engine: engine, active: make(map[string]*runProc)}
}

func (a *Adapter) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "dsh", AdapterVersion: "1.0.0",
		Protocol: runtime.Protocol{Name: "dsh-sdk-jsonrpc", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming": runtime.CapSupported,
			// SDK 无 prompt cancel / session close：进程级终止（process_scoped）。
			"interrupt":         runtime.CapAdapterTranslated,
			"resume":            runtime.CapUnavailable,
			"approval":          runtime.CapUnavailable,
			"workspace_files":   runtime.CapSupported,
			"terminal":          runtime.CapSupported,
			"structured_output": runtime.CapAdapterTranslated,
		},
		SchemaDigest: "sha256:dsh-sdk-jsonrpc-v1",
	}, nil
}

// Probe 校验二进制与配置存在；不启动业务 Run。
func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	if _, err := os.Stat(a.cfg.BinPath); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &m, Error: "dsh 二进制不可用: " + a.cfg.BinPath}, nil
	}
	if _, err := os.Stat(a.cfg.ConfigPath); err != nil {
		return runtime.ProbeResult{OK: false, Manifest: &m, Error: "cordis 配置不可用: " + a.cfg.ConfigPath}, nil
	}
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

func (a *Adapter) Start(ctx context.Context, req runtime.StartRequest) (runtime.RuntimeHandle, error) {
	if err := a.Dispatch(ctx, req.Run); err != nil {
		return nil, err
	}
	return &handle{adapter: a, runID: req.Run.ID}, nil
}

// Dispatch 启动子进程执行 Run；权威状态已在前置事务写入。
func (a *Adapter) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	a.mu.Lock()
	if a.shutdown {
		a.mu.Unlock()
		return runtime.ErrStartUnsupported
	}
	if _, busy := a.active[run.ID]; busy {
		a.mu.Unlock()
		return fmt.Errorf("%w: run %s already active", domain.ErrValidation, run.ID)
	}
	a.mu.Unlock()

	instruction, _ := run.Input["instruction"].(string)
	if instruction == "" {
		return fmt.Errorf("%w: instruction required", domain.ErrValidation)
	}
	p := newRunProc(a, run.ID, instruction)
	// 编排快照（orchestrator 写入 run.Input）：per-run 覆盖 persona / 模型 / 工具白名单配置。
	p.systemPrompt, _ = run.Input["system_prompt"].(string)
	if provider, model := modelOverrideOf(run); provider != "" || model != "" {
		p.provider = dshProviderRoute(provider)
		p.model = model
	}
	if tools := policyToolsOf(run); tools != nil {
		if path, err := renderRunConfig(a.cfg.ConfigPath, run.ID, tools); err == nil {
			p.configPath = path
		} else {
			log.Printf("dsh: run %s 工具配置渲染失败，回退默认配置: %v", run.ID, err)
		}
	}

	a.mu.Lock()
	a.active[run.ID] = p
	a.mu.Unlock()

	go a.runProcess(context.Background(), p)
	return nil
}

// Control 终止指定 Run 的进程组（cancel / interrupt 均为 process_scoped）。
func (a *Adapter) Control(runID string, terminal domain.RunStatus) {
	a.mu.Lock()
	p := a.active[runID]
	a.mu.Unlock()
	if p == nil {
		return
	}
	p.terminate(terminal)
}

// Close 终止全部活动进程（Runner 退出前 flush）。
func (a *Adapter) Close() {
	a.mu.Lock()
	a.shutdown = true
	procs := make([]*runProc, 0, len(a.active))
	for _, p := range a.active {
		procs = append(procs, p)
	}
	a.mu.Unlock()
	for _, p := range procs {
		p.terminate(domain.RunInterrupted)
	}
}

type runProc struct {
	adapter     *Adapter
	runID       string
	instruction string

	// 编排快照的 per-run 覆盖（空值回退 cfg）。
	systemPrompt string
	provider     string
	model        string
	configPath   string

	mu          sync.Mutex
	cmd         *exec.Cmd
	wantTerm    domain.RunStatus // 外部请求的终态（cancel/interrupt）
	terminating bool
	failCode    string // turn/end 上报的权威错误（协议 §9.1：idle 前的错误必须判 failed）
	failMsg     string
}

func newRunProc(a *Adapter, runID, instruction string) *runProc {
	return &runProc{adapter: a, runID: runID, instruction: instruction}
}

// terminate SIGINT → grace → SIGTERM → kill group（协议文档 §8.3）。
func (p *runProc) terminate(terminal domain.RunStatus) {
	p.mu.Lock()
	if p.terminating {
		p.mu.Unlock()
		return
	}
	p.terminating = true
	p.wantTerm = terminal
	cmd := p.cmd
	grace := p.adapter.cfg.GracePeriod
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	signalGroup(cmd, sigInt)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	signalGroup(cmd, sigTerm)
	select {
	case <-done:
	case <-time.After(grace):
		signalGroup(cmd, sigKill)
		<-done
	}
}

// ── JSON-RPC 帧（容忍服务端省略 jsonrpc 字段）────────────────────────

type rpcFrame struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (a *Adapter) runProcess(ctx context.Context, p *runProc) {
	engine := a.engine
	runID := p.runID
	cfg := a.cfg

	defer func() {
		a.mu.Lock()
		delete(a.active, runID)
		a.mu.Unlock()
	}()

	_ = engine.RecordRunStatus(ctx, runID, domain.RunStarting, nil)

	cfgPath := cfg.ConfigPath
	if p.configPath != "" {
		cfgPath = p.configPath
	}
	cmd := exec.CommandContext(ctx, cfg.BinPath, cfgPath)
	cmd.Dir = cfg.WorkspaceRoot
	// per-run 覆盖优先：persona 与模型来自编排快照（Agent 配置目录）。
	systemPrompt := cfg.SystemPrompt
	if p.systemPrompt != "" {
		systemPrompt = p.systemPrompt
	}
	model := cfg.Model
	if p.model != "" {
		model = p.model
	}
	cmd.Env = append(os.Environ(),
		"DSH_CWD="+cfg.WorkspaceRoot,
		"DSH_SESSION_ROOT="+cfg.SessionRoot,
		"DSH_MODEL="+model,
	)
	if systemPrompt != "" {
		cmd.Env = append(cmd.Env, "DSH_SYSTEM_PROMPT="+systemPrompt)
	}
	setProcGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		a.failRun(ctx, runID, "spawn_failed", err.Error())
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.failRun(ctx, runID, "spawn_failed", err.Error())
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		a.failRun(ctx, runID, "spawn_failed", err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		a.failRun(ctx, runID, "spawn_failed", err.Error())
		return
	}
	// Start 完成后再发布 cmd：保证 terminate 读到的 Process 已初始化（happens-before）。
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()

	go drainStderr(stderr, runID)

	// stdout 只承载协议帧；超限即安全失败。
	reader := bufio.NewReaderSize(stdout, 64*1024)
	send := func(method string, id int64, params any) error {
		frame := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
		b, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(b, '\n'))
		return err
	}

	// 1) initialize 固化 cwd/provider/model（per-run 覆盖优先）。
	provider := cfg.Provider
	if p.provider != "" {
		provider = p.provider
	}
	if err := send("initialize", 1, map[string]any{
		"cwd": cfg.WorkspaceRoot, "provider": provider, "model": model,
	}); err != nil {
		a.failRun(ctx, runID, "io_failed", err.Error())
		p.terminate(domain.RunFailed)
		return
	}
	initRes, err := readResult(reader, 1, cfg.MaxFrameBytes)
	if err != nil {
		a.failRun(ctx, runID, "initialize_failed", err.Error())
		p.terminate(domain.RunFailed)
		return
	}
	log.Printf("dsh: run %s initialized serverInfo=%s", runID, string(initRes))

	// 2) session/prompt：messageId 只表示 durable inbox 接受。
	sessionID := "atw_" + runID
	if err := send("session/prompt", 2, map[string]any{
		"sessionId":     sessionID,
		"contentBlocks": []map[string]any{{"type": "text", "text": p.instruction}},
	}); err != nil {
		a.failRun(ctx, runID, "io_failed", err.Error())
		p.terminate(domain.RunFailed)
		return
	}

	// 3) 消费通知流直到目标 session idle（本轮结束）。
	started := false
	for {
		frame, err := readFrame(reader, cfg.MaxFrameBytes)
		if err != nil {
			if p.terminated() {
				a.emitTerminal(ctx, runID, p)
				return
			}
			a.failRun(ctx, runID, "stream_failed", err.Error())
			p.terminate(domain.RunFailed)
			return
		}
		if frame == nil {
			continue
		}
		if frame.ID != nil {
			if frame.Error != nil {
				a.failRun(ctx, runID, "dsh_error", frame.Error.Message)
				p.terminate(domain.RunFailed)
				return
			}
			continue // id=2 的 messageId 回执：已接受，执行由事件流投影
		}
		switch frame.Method {
		case "session.status":
			var n struct {
				SessionID string `json:"sessionId"`
				Status    string `json:"status"`
			}
			if json.Unmarshal(frame.Params, &n) != nil || n.SessionID != sessionID {
				continue
			}
			switch n.Status {
			case "running":
				if !started {
					started = true
					_ = engine.RecordRunStatus(ctx, runID, domain.RunRunning, nil)
				}
			case "idle":
				if !started {
					continue // 尚未进入运行：spurious idle，继续等待
				}
				// turn/end 已报告权威错误（如 MISSING_CREDENTIAL）：失败，不得误判成功。
				if code, msg := p.failure(); code != "" {
					a.failRun(ctx, runID, code, msg)
					p.terminate(domain.RunFailed)
					return
				}
				_ = engine.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil)
				_ = engine.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil)
				_ = send("shutdown", 3, nil)
				p.terminate(domain.RunInterrupted) // 回收进程；终态已是 succeeded
				return
			}
		case "session.event":
			a.mapSessionEvent(ctx, runID, sessionID, frame.Params)
		}
	}
}

func (p *runProc) terminated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminating
}

// setFailure / failure 记录与读取 turn/end 的权威错误。
func (p *runProc) setFailure(code, msg string) {
	p.mu.Lock()
	p.failCode, p.failMsg = code, msg
	p.mu.Unlock()
}

func (p *runProc) failure() (code, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failCode, p.failMsg
}

func (a *Adapter) emitTerminal(ctx context.Context, runID string, p *runProc) {
	p.mu.Lock()
	want := p.wantTerm
	p.mu.Unlock()
	switch want {
	case domain.RunCancelled:
		_ = a.engine.RecordRunStatus(ctx, runID, domain.RunCancelled, nil)
	case domain.RunInterrupted:
		// succeeded 之后回收进程：不再覆盖终态（状态机会拒绝）。
		_ = a.engine.RecordRunStatus(ctx, runID, domain.RunInterrupted, nil)
	}
}

func (a *Adapter) failRun(ctx context.Context, runID, code, detail string) {
	// 面向用户的 detail 必须脱敏：只保留错误类别与短描述。
	msg := detail
	if len(msg) > 200 {
		msg = msg[:200]
	}
	_ = a.engine.RecordRunStatus(ctx, runID, domain.RunFailed,
		map[string]any{"code": code, "message": msg})
}

// mapSessionEvent 把 SessionEvent 投影到 canonical 事件；
// 未知类型不投影（原始日志由 DSH session 持久化保留）。
func (a *Adapter) mapSessionEvent(ctx context.Context, runID, sessionID string, raw json.RawMessage) {
	var n struct {
		SessionID string          `json:"sessionId"`
		Event     json.RawMessage `json:"event"`
	}
	if json.Unmarshal(raw, &n) != nil || n.SessionID != sessionID {
		return
	}
	var ev struct {
		Type string `json:"type"`
		Seq  int64  `json:"seq"`
	}
	if json.Unmarshal(n.Event, &ev) != nil {
		return
	}
	var envelope map[string]any
	_ = json.Unmarshal(n.Event, &envelope)
	// SessionEvent 信封是 {type, seq, time, data}：负载在内层 data（dsh-session SessionEventMap）。
	data, _ := envelope["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}

	switch ev.Type {
	case "assistant/chunk":
		a.engine.RecordRunEvent(ctx, runID, domain.EventMessageDelta, map[string]any{"raw": data})
	case "assistant/message":
		a.engine.RecordRunEvent(ctx, runID, domain.EventMessageCompleted, map[string]any{
			"role": "assistant", "text": extractText(data),
		})
	case "tool/call":
		a.engine.RecordRunEvent(ctx, runID, domain.EventToolStarted, map[string]any{
			"tool": data["name"], "call_id": data["callId"],
		})
	case "tool/result":
		if d, _ := data["isError"].(bool); d {
			a.engine.RecordRunEvent(ctx, runID, domain.EventToolFailed, map[string]any{"raw": data})
		} else {
			a.engine.RecordRunEvent(ctx, runID, domain.EventToolCompleted, map[string]any{"raw": data})
		}
	case "turn/end":
		// turn 级权威错误（如 MISSING_CREDENTIAL）：记录失败，阻止 idle 误判成功。
		reason, _ := data["reason"].(map[string]any)
		if reason == nil || reason["kind"] != "error" {
			return
		}
		errObj, _ := reason["error"].(map[string]any)
		code, _ := errObj["code"].(string)
		msg, _ := errObj["message"].(string)
		if code == "" {
			code = "turn_error"
		}
		a.mu.Lock()
		p := a.active[runID]
		a.mu.Unlock()
		if p != nil {
			p.setFailure(code, msg)
		}
		a.engine.RecordRunEvent(ctx, runID, domain.EventRunFailed, map[string]any{
			"code": code, "message": msg,
		})
	}
}

// extractText 尽力提取 assistant 消息文本（message.content 中 type=text 块）。
func extractText(data map[string]any) string {
	msg, _ := data["message"].(map[string]any)
	if msg == nil {
		return ""
	}
	blocks, _ := msg["content"].([]any)
	var sb strings.Builder
	for _, b := range blocks {
		if m, ok := b.(map[string]any); ok {
			if m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
	}
	return sb.String()
}

// readFrame 读一帧 NDJSON；超过 maxBytes 安全失败。EOF 返回 io.ErrUnexpectedEOF。
func readFrame(r *bufio.Reader, maxBytes int) (*rpcFrame, error) {
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
	var f rpcFrame
	if err := json.Unmarshal([]byte(trimmed), &f); err != nil {
		return nil, nil // 非 JSON 行：隔离不执行
	}
	return &f, nil
}

// readResult 跳到指定 id 的响应帧（跳过其间通知）。
func readResult(r *bufio.Reader, id int64, maxBytes int) (json.RawMessage, error) {
	for {
		f, err := readFrame(r, maxBytes)
		if err != nil {
			return nil, err
		}
		if f == nil {
			continue
		}
		if f.ID != nil && *f.ID == id {
			if f.Error != nil {
				return nil, fmt.Errorf("rpc error %d: %s", f.Error.Code, f.Error.Message)
			}
			return f.Result, nil
		}
	}
}

// drainStderr 独立限流收集诊断输出；不阻塞协议通道。
func drainStderr(stderr io.Reader, runID string) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 16*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 2048 {
			line = line[:2048] + "…(truncated)"
		}
		log.Printf("dsh-stderr[%s]: %s", runID, line)
	}
}

type handle struct {
	adapter *Adapter
	runID   string
}

func (h *handle) SessionRef() string { return "dsh://" + h.runID }
func (h *handle) Send(ctx context.Context, instruction string) error {
	return fmt.Errorf("%w: dsh 单 Run 单 prompt（M2）", domain.ErrValidation)
}
func (h *handle) Interrupt(ctx context.Context) error {
	h.adapter.Control(h.runID, domain.RunInterrupted)
	return nil
}
func (h *handle) Cancel(ctx context.Context) error {
	h.adapter.Control(h.runID, domain.RunCancelled)
	return nil
}
func (h *handle) ResolveApproval(ctx context.Context, approvalID string, approved bool) error {
	return runtime.ErrStartUnsupported
}
func (h *handle) Close(ctx context.Context) error {
	h.adapter.Control(h.runID, domain.RunInterrupted)
	return nil
}
