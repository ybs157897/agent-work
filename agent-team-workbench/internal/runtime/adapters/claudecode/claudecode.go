// Package claudecode 实现 Claude Code CLI Adapter（协议文档 §9：Claude 行）。
//
// 传输：claude CLI print mode（-p --output-format stream-json --verbose），
// stdout NDJSON：system.init / assistant / result(subtype=success|error_*)。
// resume 能力通过 --resume <session_id> 支持（M4 完整接入）；
// 取消为进程组级终止（process_scoped）。CLI flags / 事件 schema 随版本变化，
// 必须固定版本并以录制 fixture 做 conformance。
package claudecode

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
}

type Config struct {
	BinPath       string   // claude 可执行文件
	Args          []string // 覆盖默认参数（测试用回放桩）
	WorkspaceRoot string
	Model         string // 可选 --model
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
		AdapterID: "claude-code", AdapterVersion: "1.0.0",
		Protocol: runtime.Protocol{Name: "claude-cli-stream-json", Version: "1"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming": runtime.CapSupported,
			"resume":    runtime.CapSupported, // --resume session_id
			// CLI 无精确取消：进程组终止（process_scoped）。
			"interrupt":         runtime.CapAdapterTranslated,
			"approval":          runtime.CapUnavailable, // MVP 不接 permission prompt tool
			"workspace_files":   runtime.CapSupported,
			"terminal":          runtime.CapUnavailable,
			"structured_output": runtime.CapSupported,
		},
		SchemaDigest: "sha256:claude-cli-stream-json-v1",
	}, nil
}

// Probe 校验 CLI 可用（--version）；不启动业务 Run。
func (a *Adapter) Probe(ctx context.Context, req runtime.ProbeRequest) (runtime.ProbeResult, error) {
	m, _ := a.Manifest(ctx)
	if _, err := exec.LookPath(a.cfg.BinPath); err != nil {
		if _, serr := os.Stat(a.cfg.BinPath); serr != nil {
			return runtime.ProbeResult{OK: false, Manifest: &m, Error: "claude CLI 不可用"}, nil
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

// Dispatch 启动 CLI print mode 执行 Run。
func (a *Adapter) Dispatch(ctx context.Context, run *domain.ExecutionRun) error {
	instruction, _ := run.Input["instruction"].(string)
	if instruction == "" {
		return fmt.Errorf("%w: instruction required", domain.ErrValidation)
	}
	p := &runProc{adapter: a, runID: run.ID, instruction: instruction}
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

// Control 进程组级终止（cancel / interrupt 均为 process_scoped）。
func (a *Adapter) Control(runID string, terminal domain.RunStatus) {
	a.mu.Lock()
	p := a.active[runID]
	a.mu.Unlock()
	if p == nil {
		return
	}
	p.terminate(terminal)
}

func (a *Adapter) Close() {
	a.mu.Lock()
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

	mu          sync.Mutex
	cmd         *exec.Cmd
	wantTerm    domain.RunStatus
	terminating bool
}

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
	signalGroup(cmd, sigKill)
	<-done
}

func (p *runProc) terminated() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminating
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
		args = []string{"-p", p.instruction, "--output-format", "stream-json", "--verbose"}
		if cfg.Model != "" {
			args = append(args, "--model", cfg.Model)
		}
	}
	cmd := exec.CommandContext(ctx, cfg.BinPath, args...)
	cmd.Dir = cfg.WorkspaceRoot
	cmd.Env = os.Environ()
	setProcGroup(cmd)

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
	p.mu.Unlock()
	go drainStderr(stderr, runID)

	reader := bufio.NewReaderSize(stdout, 64*1024)
	started := false
	finished := false

	for {
		frame, err := readFrame(reader, cfg.MaxFrameBytes)
		if err != nil {
			if p.terminated() {
				a.emitTerminal(ctx, runID, p)
				return
			}
			if !finished {
				a.failRun(ctx, runID, "stream_failed", err.Error())
			}
			return
		}
		if frame == nil {
			continue
		}
		switch frame.Type {
		case "system":
			if !started {
				started = true
				_ = engine.RecordRunStatus(ctx, runID, domain.RunRunning, nil)
			}
		case "assistant":
			engine.RecordRunEvent(ctx, runID, domain.EventMessageCompleted, map[string]any{
				"text": assistantText(frame),
			})
		case "result":
			finished = true
			if frame.Subtype == "success" {
				if started {
					_ = engine.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil)
				}
				_ = engine.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil)
			} else {
				a.failRun(ctx, runID, "claude_"+frame.Subtype, resultError(frame))
			}
			p.terminate(domain.RunInterrupted) // 回收进程；终态已确定
			return
		}
	}
}

func (a *Adapter) emitTerminal(ctx context.Context, runID string, p *runProc) {
	p.mu.Lock()
	want := p.wantTerm
	p.mu.Unlock()
	switch want {
	case domain.RunCancelled:
		_ = a.engine.RecordRunStatus(ctx, runID, domain.RunCancelled, nil)
	case domain.RunInterrupted:
		_ = a.engine.RecordRunStatus(ctx, runID, domain.RunInterrupted, nil)
	}
}

func (a *Adapter) failRun(ctx context.Context, runID, code, detail string) {
	msg := strings.TrimSpace(detail)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	_ = a.engine.RecordRunStatus(ctx, runID, domain.RunFailed,
		map[string]any{"code": code, "message": msg})
}

// stream-json 帧（只解析映射需要的字段）。
type streamFrame struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Message json.RawMessage `json:"message"`
	Result  string          `json:"result"`
	IsError bool            `json:"is_error"`
}

func assistantText(f *streamFrame) string {
	var msg struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(f.Message, &msg) != nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range msg.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

func resultError(f *streamFrame) string {
	if f.Result != "" {
		return f.Result
	}
	return "claude result subtype=" + f.Subtype
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

func drainStderr(stderr io.Reader, runID string) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 16*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 2048 {
			line = line[:2048] + "…(truncated)"
		}
		log.Printf("claude-stderr[%s]: %s", runID, line)
	}
}

type handle struct {
	adapter *Adapter
	runID   string
}

func (h *handle) SessionRef() string { return "claude://" + h.runID }
func (h *handle) Send(ctx context.Context, instruction string) error {
	return fmt.Errorf("%w: claude print mode 单 Run 单 prompt（M4 接 resume）", domain.ErrValidation)
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
