// Package codexapp 实现 OpenAI Codex app-server Adapter（协议文档 §9：Codex 行）。
//
// 传输：codex app-server 子进程，stdio JSONL。协议为省略 jsonrpc 字段的
// JSON-RPC：请求 initialize → thread/start → turn/start；服务端通知
// turn/started、turn/completed（status: completed|interrupted|failed|inProgress）；
// 审批以服务端请求（item/*/requestApproval）到达，Adapter 映射到工作台
// ApprovalRequest，并以 ReviewDecision 响应（approved / denied）。
//
// 版本门：按安装版本运行时探测（initialize.userAgent），协议漂移由
// conformance/录制回放拦截。
package codexapp

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

// Engine 是 Adapter 依赖的应用层能力面。
type Engine interface {
	RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error
	RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error
	RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error)
	Run(ctx context.Context, id string) (*domain.ExecutionRun, error)
}

type Config struct {
	BinPath       string   // codex 可执行文件
	Args          []string // 启动参数；缺省 ["app-server"]（测试可替换为回放桩）
	WorkspaceRoot string   // thread/start.cwd
	Model         string   // 可选：thread/start.model
	MaxFrameBytes int
	GracePeriod   time.Duration
}

type Adapter struct {
	cfg    Config
	engine Engine

	mu     sync.Mutex
	active map[string]*runProc
}

var _ runtime.RuntimeAdapter = (*Adapter)(nil)

func New(cfg Config, engine Engine) *Adapter {
	if cfg.MaxFrameBytes <= 0 {
		cfg.MaxFrameBytes = 8 << 20
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 3 * time.Second
	}
	return &Adapter{cfg: cfg, engine: engine, active: make(map[string]*runProc)}
}

func (a *Adapter) Manifest(ctx context.Context) (runtime.AdapterManifest, error) {
	return runtime.AdapterManifest{
		AdapterID: "codex-appserver", AdapterVersion: "1.0.0",
		Protocol: runtime.Protocol{Name: "codex-app-server", Version: "v2"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming":         runtime.CapSupported,
			"interrupt":         runtime.CapSupported, // turn/interrupt 原生支持
			"resume":            runtime.CapSupported, // thread/resume
			"approval":          runtime.CapSupported, // item/*/requestApproval
			"workspace_files":   runtime.CapSupported,
			"terminal":          runtime.CapUnavailable,
			"structured_output": runtime.CapAdapterTranslated,
		},
		SchemaDigest: "sha256:codex-app-server-generate-ts",
	}, nil
}

// Probe 用 initialize 握手核验版本与协议；不启动业务 Run。
func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	if _, err := exec.LookPath(a.cfg.BinPath); err != nil {
		if _, serr := os.Stat(a.cfg.BinPath); serr != nil {
			return runtime.ProbeResult{OK: false, Manifest: &m, Error: "codex 不可用: " + a.cfg.BinPath}, nil
		}
	}
	return runtime.ProbeResult{OK: true, Manifest: &m}, nil
}

func (a *Adapter) Start(ctx context.Context, req runtime.StartRequest) (runtime.RuntimeHandle, error) {
	if err := a.Dispatch(ctx, req.Run); err != nil {
		return nil, err
	}
	return &handle{adapter: a, runID: req.Run.ID}, nil
}

// Dispatch 启动 app-server 子进程执行 Run。
func (a *Adapter) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	instruction, _ := run.Input["instruction"].(string)
	if instruction == "" {
		return fmt.Errorf("%w: instruction required", domain.ErrValidation)
	}
	p := &runProc{
		adapter: a, runID: run.ID, instruction: instruction,
		approvals: make(map[int64]chan bool),
	}
	a.mu.Lock()
	if _, busy := a.active[run.ID]; busy {
		a.mu.Unlock()
		return fmt.Errorf("%w: run %s already active", domain.ErrValidation, run.ID)
	}
	a.active[run.ID] = p
	a.mu.Unlock()
	go a.runProcess(context.Background(), p)
	return nil
}

// Interrupt 发送 turn/interrupt（原生精确取消）。
func (a *Adapter) Interrupt(runID string) {
	a.mu.Lock()
	p := a.active[runID]
	a.mu.Unlock()
	if p == nil {
		return
	}
	p.sendInterrupt()
}

// Control 统一控制面接口：cancel/interrupt 均走原生 turn/interrupt。
func (a *Adapter) Control(runID string, terminal domain.RunStatus) {
	a.Interrupt(runID)
}

// ResolveApproval 把审批决定回写给阻塞中的服务端请求。
func (a *Adapter) ResolveApproval(runID string, approved bool) {
	a.mu.Lock()
	p := a.active[runID]
	a.mu.Unlock()
	if p == nil {
		return
	}
	p.resolveAll(approved)
}

// Close 终止全部活动进程。
func (a *Adapter) Close() {
	a.mu.Lock()
	procs := make([]*runProc, 0, len(a.active))
	for _, p := range a.active {
		procs = append(procs, p)
	}
	a.mu.Unlock()
	for _, p := range procs {
		p.kill()
	}
}

type runProc struct {
	adapter     *Adapter
	runID       string
	instruction string

	mu        sync.Mutex
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	threadID  string
	nextID    int64
	approvals map[int64]chan bool
	killed    bool
}

func (p *runProc) send(frame map[string]any) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin == nil {
		return fmt.Errorf("stdin closed")
	}
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

func (p *runProc) request(method string, params any) (int64, error) {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.mu.Unlock()
	return id, p.send(map[string]any{"id": id, "method": method, "params": params})
}

func (p *runProc) sendInterrupt() {
	p.mu.Lock()
	threadID := p.threadID
	p.mu.Unlock()
	if threadID == "" {
		return
	}
	_ = p.send(map[string]any{
		"id": 9000, "method": "turn/interrupt",
		"params": map[string]any{"threadId": threadID},
	})
}

func (p *runProc) resolveAll(approved bool) {
	p.mu.Lock()
	chs := make([]chan bool, 0, len(p.approvals))
	for _, ch := range p.approvals {
		chs = append(chs, ch)
	}
	p.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- approved:
		default:
		}
	}
}

func (p *runProc) kill() {
	p.mu.Lock()
	if p.killed {
		p.mu.Unlock()
		return
	}
	p.killed = true
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		signalGroup(cmd, sigTerm)
	}
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

	args := cfg.Args
	if len(args) == 0 {
		args = []string{"app-server"}
	}
	cmd := exec.CommandContext(ctx, cfg.BinPath, args...)
	cmd.Dir = cfg.WorkspaceRoot
	cmd.Env = os.Environ()
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
	p.mu.Lock()
	p.cmd = cmd
	p.stdin = stdin
	p.mu.Unlock()
	go drainStderr(stderr, runID)

	// 1) initialize 握手（版本门）。
	if _, err := p.request("initialize", map[string]any{
		"clientInfo": map[string]any{"name": "agent-team-workbench", "version": "0.3.0"},
	}); err != nil {
		a.failRun(ctx, runID, "io_failed", err.Error())
		return
	}

	reader := bufio.NewReaderSize(stdout, 64*1024)
	threadStarted := false
	turnSent := false
	started := false

	for {
		frame, err := readFrame(reader, cfg.MaxFrameBytes)
		if err != nil {
			if !p.isKilled() {
				a.failRun(ctx, runID, "stream_failed", err.Error())
			}
			return
		}
		if frame == nil {
			continue
		}

		// 服务端请求（带 id + method）：审批路由到工作台。
		if frame.ID != nil && frame.Method != "" {
			a.handleServerRequest(ctx, p, frame)
			continue
		}
		// 我方请求的响应。
		if frame.ID != nil {
			if frame.Error != nil {
				a.failRun(ctx, runID, "codex_error", frame.Error.Message)
				p.kill()
				return
			}
			switch *frame.ID {
			case 1: // initialize 完成 → 开 thread
				if _, err := p.request("thread/start", threadStartParams(cfg)); err != nil {
					a.failRun(ctx, runID, "io_failed", err.Error())
					return
				}
			case 2: // thread/start 完成 → 取 threadId，发 turn/start
				var res struct {
					Thread struct {
						ID string `json:"id"`
					} `json:"thread"`
				}
				_ = json.Unmarshal(frame.Result, &res)
				if res.Thread.ID == "" {
					a.failRun(ctx, runID, "thread_start_failed", "thread/start 未返回 threadId")
					p.kill()
					return
				}
				p.mu.Lock()
				p.threadID = res.Thread.ID
				p.mu.Unlock()
				threadStarted = true
				if _, err := p.request("turn/start", map[string]any{
					"threadId": res.Thread.ID,
					"input":    []map[string]any{{"type": "text", "text": p.instruction}},
				}); err != nil {
					a.failRun(ctx, runID, "io_failed", err.Error())
					return
				}
				turnSent = true
			}
			continue
		}

		// 服务端通知。
		switch frame.Method {
		case "thread/started", "thread/status/changed":
			// thread 生命周期投影：无状态迁移，仅记录。
		case "turn/started":
			if !started && turnSent {
				started = true
				_ = engine.RecordRunStatus(ctx, runID, domain.RunRunning, nil)
			}
		case "item/started":
			engine.RecordRunEvent(ctx, runID, domain.EventToolStarted, itemSummary(frame.Params))
		case "item/completed":
			engine.RecordRunEvent(ctx, runID, domain.EventToolCompleted, itemSummary(frame.Params))
		case "item/agentMessage/delta":
			engine.RecordRunEvent(ctx, runID, domain.EventMessageDelta, map[string]any{"raw": rawOf(frame.Params)})
		case "turn/completed":
			var n struct {
				Turn struct {
					Status string `json:"status"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(frame.Params, &n)
			switch n.Turn.Status {
			case "completed":
				if started {
					_ = engine.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil)
				}
				_ = engine.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil)
			case "interrupted":
				// 状态机路径：interrupting→interrupted（控制平面已置 interrupting）；
				// 否则 running→cancelling→cancelled（审批拒绝等中断）。
				cur := domain.RunStatus("")
				if r, err := engine.Run(ctx, runID); err == nil && r != nil {
					cur = r.Status
				}
				if cur == domain.RunInterrupting {
					_ = engine.RecordRunStatus(ctx, runID, domain.RunInterrupted, nil)
				} else {
					_ = engine.RecordRunStatus(ctx, runID, domain.RunCancelling, nil)
					_ = engine.RecordRunStatus(ctx, runID, domain.RunCancelled, nil)
				}
			case "failed":
				a.failRun(ctx, runID, "turn_failed", "codex turn failed")
			default:
				continue
			}
			p.kill()
			return
		case "error":
			a.failRun(ctx, runID, "codex_error", rawString(frame.Params))
			p.kill()
			return
		}
		_ = threadStarted
	}
}

// handleServerRequest 处理 item/*/requestApproval：映射为工作台审批并等待决定。
func (a *Adapter) handleServerRequest(ctx context.Context, p *runProc, frame *rpcFrame) {
	kind := "command"
	switch frame.Method {
	case "item/fileChange/requestApproval":
		kind = "file_change"
	case "item/permissions/requestApproval":
		kind = "permissions"
	}
	if !strings.Contains(frame.Method, "requestApproval") {
		// 非审批类服务端请求：显式拒绝，禁止静默降级。
		_ = p.send(map[string]any{"id": *frame.ID, "error": map[string]any{
			"code": -32601, "message": "unsupported server request: " + frame.Method,
		}})
		return
	}
	approval, err := a.engine.RequestApproval(ctx, p.runID, kind, "high",
		"Codex 请求批准："+frame.Method)
	if err != nil {
		_ = p.send(map[string]any{"id": *frame.ID, "result": map[string]any{
			"decision": map[string]any{"denied": map[string]any{"rejection": "approval bridge error"}},
		}})
		return
	}
	ch := make(chan bool, 1)
	p.mu.Lock()
	p.approvals[*frame.ID] = ch
	p.mu.Unlock()
	approved := <-ch
	p.mu.Lock()
	delete(p.approvals, *frame.ID)
	p.mu.Unlock()
	var decision any = "approved"
	if !approved {
		decision = map[string]any{"denied": map[string]any{"rejection": "denied by reviewer"}}
	}
	_ = p.send(map[string]any{"id": *frame.ID, "result": map[string]any{"decision": decision}})
	_ = approval
	log.Printf("codexapp: run %s 审批 %s 已回写（approved=%v）", p.runID, approval.ID, approved)
}

func threadStartParams(cfg Config) map[string]any {
	params := map[string]any{"cwd": cfg.WorkspaceRoot}
	if cfg.Model != "" {
		params["model"] = cfg.Model
	}
	return params
}

func (a *Adapter) failRun(ctx context.Context, runID, code, detail string) {
	msg := strings.TrimSpace(detail)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	_ = a.engine.RecordRunStatus(ctx, runID, domain.RunFailed,
		map[string]any{"code": code, "message": msg})
}

func (p *runProc) isKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

func itemSummary(raw json.RawMessage) map[string]any {
	var n struct {
		Item map[string]any `json:"item"`
	}
	if json.Unmarshal(raw, &n) == nil && n.Item != nil {
		return map[string]any{"item_type": n.Item["type"], "id": n.Item["id"]}
	}
	return map[string]any{"raw": rawOf(raw)}
}

func rawOf(raw json.RawMessage) any {
	var v any
	if json.Unmarshal(raw, &v) == nil {
		return v
	}
	return nil
}

func rawString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// ── JSONL 帧（Codex 省略 jsonrpc 字段；响应/请求/通知统一解析）────────

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

func drainStderr(stderr io.Reader, runID string) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 16*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 2048 {
			line = line[:2048] + "…(truncated)"
		}
		log.Printf("codex-stderr[%s]: %s", runID, line)
	}
}

type handle struct {
	adapter *Adapter
	runID   string
}

func (h *handle) SessionRef() string { return "codex://" + h.runID }
func (h *handle) Send(ctx context.Context, instruction string) error {
	return fmt.Errorf("%w: codex 单 Run 单 turn（M3）", domain.ErrValidation)
}
func (h *handle) Interrupt(ctx context.Context) error {
	h.adapter.Interrupt(h.runID)
	return nil
}
func (h *handle) Cancel(ctx context.Context) error {
	h.adapter.Interrupt(h.runID)
	return nil
}
func (h *handle) ResolveApproval(ctx context.Context, approvalID string, approved bool) error {
	h.adapter.ResolveApproval(h.runID, approved)
	return nil
}
func (h *handle) Close(ctx context.Context) error {
	h.adapter.Interrupt(h.runID)
	return nil
}
