// Package codexapp 实现 OpenAI Codex app-server Adapter（协议文档 §9：Codex 行，
// Adapter SPI v2）。
//
// 传输：codex app-server 子进程，stdio JSONL。协议为省略 jsonrpc 字段的
// JSON-RPC：请求 initialize → initialized 通知 → thread/start（新会话）/
// thread/resume（恢复）→ turn/start{input}；服务端通知 turn/started、
// turn/completed（status: completed|interrupted|failed|inProgress）、item/* 事件；
// 审批以服务端请求（item/*/requestApproval）到达，映射到 Callbacks.RequestApproval，
// 决定经 Controls（ControlApproval）回写 JSON-RPC result；turn/steer 做 steering；
// turn/interrupt 原生精确取消。
//
// Execute 阻塞到本轮结束：spawn（进程组）→ initialize 握手 → thread/start|resume
// （首个 threadId 经 OnSession 上报 codex://<threadID>）→ turn/start → 消费通知流
// 到 turn/completed → 进程回收（SIGINT → 宽限 → SIGKILL 进程组）→ 结构化 ExecResult。
// 状态机由 internal/runtime ModuleRunner 驱动，本包不直写 Run 状态。
//
// 版本门：按安装版本运行时探测（initialize.userAgent），协议漂移由
// conformance/录制回放拦截。
package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentwork/codexconfig"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

type Config struct {
	BinPath       string   // codex 可执行文件
	Args          []string // 启动参数；缺省开启 multi_agent，原生 OpenAI provider 额外开启 v2（测试可替换为回放桩）
	Home          string   // CODEX_HOME 项目空间（默认 .agent-work/codex）
	WorkspaceRoot string   // thread/start.cwd
	Model         string   // 可选：thread/start.model
	MaxFrameBytes int
	GracePeriod   time.Duration
}

// Module 是 Codex app-server 的 Adapter SPI v2 模块；一次 Execute 一个子进程，
// 无跨 Run 共享执行态（providerVersion 仅作 Manifest 探测快照缓存）。
type Module struct {
	cfg Config

	mu              sync.Mutex
	providerVersion string
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
	m.mu.Lock()
	providerVersion := m.providerVersion
	m.mu.Unlock()
	return runtime.AdapterManifest{
		AdapterID: "codex-appserver", AdapterVersion: adapterVersion, ProviderVersion: providerVersion,
		Protocol: runtime.Protocol{Name: "codex-app-server", Version: "v2"},
		Capabilities: map[string]runtime.CapabilityLevel{
			"streaming":         runtime.CapSupported,
			"interrupt":         runtime.CapSupported, // turn/interrupt 原生支持
			"resume":            runtime.CapSupported, // thread/resume
			"multi_turn":        runtime.CapSupported,
			"steering":          runtime.CapSupported, // turn/steer
			"system_prompt":     runtime.CapSupported,
			"modes":             runtime.CapSupported,
			"subagents":         runtime.CapSupported,
			"permissions":       runtime.CapAdapterTranslated,
			"approval":          runtime.CapSupported, // item/*/requestApproval
			"workspace_files":   runtime.CapSupported,
			"terminal":          runtime.CapUnavailable,
			"structured_output": runtime.CapAdapterTranslated,
		},
		SchemaDigest: "sha256:" + protocolSchemaSHA256,
	}, nil
}

func (m *Module) setProviderVersion(version string) {
	m.mu.Lock()
	m.providerVersion = version
	m.mu.Unlock()
}

// Execute 阻塞执行一轮 app-server 会话；事件经 ex.Callbacks 上报，
// 终态以 ExecResult 表达（终态意图 > turn/completed > 流中断分类）。
func (m *Module) Execute(ex *runtime.ExecContext) runtime.ExecResult {
	ctx := ex.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return terminalByIntent(ex, "context cancelled before spawn")
	}
	if strings.TrimSpace(ex.Instruction) == "" {
		return failedResult(configFailure("instruction_required", "instruction required"))
	}
	policy := runtime.PolicySnapshotOf(ex.Run)
	if len(policy.Tools) > 0 {
		return failedResult(configFailure("tools_unsupported", "codex app-server 当前协议不支持内建工具白名单"))
	}

	snap := runtime.ModelSnapshotOf(ex.Run)
	if snap.Model != "" || snap.Provider != "" {
		if err := codexconfig.ApplySnapshot(m.cfg.Home, snap); err != nil {
			return failedResult(configFailure("codex_config", err.Error()))
		}
	}

	cmd, err := runtime.TrustedCommand(m.cfg.BinPath, m.commandArgs(snap)...)
	if err != nil {
		return failedResult(configFailure("spawn_failed", err.Error()))
	}
	cmd.Dir = m.cfg.WorkspaceRoot
	cmd.Env = m.processEnv()
	setProcGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return failedResult(configFailure("spawn_failed", err.Error()))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return failedResult(configFailure("spawn_failed", err.Error()))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return failedResult(configFailure("spawn_failed", err.Error()))
	}
	if err := cmd.Start(); err != nil {
		return failedResult(configFailure("spawn_failed", err.Error()))
	}
	// pgid 必须在组长存活时采样（组长死后 Getpgid 失败，组级信号打不出去）。
	pgid := processGroupID(cmd)
	ex.Callbacks.OnSpawn(cmd.Process.Pid, pgid)

	s := &execStream{
		module: m, ex: ex, ctx: ctx,
		cmd: cmd, pgid: pgid, stdin: stdin,
		done: make(chan error, 1),
		// 模型注册表快照（orchestrator 写入 run.Input）：per-run 覆盖 thread/start.model。
		model: runtime.ModelSnapshotOf(ex.Run).Model,
		// 未显式配置时用 Ultra 进入 proactive multi-agent；用户明确选择的
		// effort 仍是更高优先级的运行快照，不以“默认”名义覆盖。
		reasoningEffort: codexTurnReasoningEffort(runtime.ModelSnapshotOf(ex.Run).ReasoningEffort),
		systemPrompt:    runtime.SystemPromptOf(ex.Run),
		policy:          policy,
		resumeThreadID:  runtime.SessionIDFromRef(ex.Session.Ref, "codex"),
		pendingRequests: make(map[int64]string),
		approvals:       make(map[string]chan bool),
		childIDs:        make(map[string]struct{}), childEmitted: make(map[string]map[string]struct{}),
		childMeta: make(map[string]childMetadata),
	}
	go func() { s.done <- cmd.Wait() }()
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		drainStderr(stderr, ex.Callbacks)
	}()

	// 控制面：ControlInput → turn/steer；ControlApproval → 精确匹配回写。
	turnDone := make(chan struct{})
	stopControls := make(chan struct{})
	stopWatch := make(chan struct{})
	if ex.Controls != nil {
		go s.consumeControls(stopControls)
	}
	go s.watchCancel(m.cfg.GracePeriod, stopWatch, turnDone)
	defer close(stopControls)
	defer close(stopWatch)
	defer close(turnDone)

	// io.LimitReader 约束读取上限（MaxFrameBytes+1）：超长行在进入内存前就被
	// 截停，readFrame 的长度检查随即报错——避免先整行缓冲再判超长。
	pumped := s.pump(bufio.NewReaderSize(io.LimitReader(stdout, int64(m.cfg.MaxFrameBytes)+1), 64*1024))
	waitErr := s.reap(m.cfg.GracePeriod, stderrDone)
	return composeResult(ex, ctx, pumped, waitErr)
}

// ── 执行流状态 ───────────────────────────────────────────────────────

// execStream 一次 Execute 的子进程会话状态；stdin 写入与关键字段由 mu 串行化。
type execStream struct {
	module *Module
	ex     *runtime.ExecContext
	ctx    context.Context

	cmd   *exec.Cmd
	pgid  int
	stdin io.WriteCloser
	done  chan error // cmd.Wait 结果（单次）

	model           string // 编排快照的 per-run 模型覆盖（空回退 cfg.Model；plan 模式可经 model/list 补齐）
	reasoningEffort string
	systemPrompt    string
	policy          runtime.PolicySnapshot
	resumeThreadID  string

	mu                  sync.Mutex
	nextID              int64
	pendingRequests     map[int64]string
	threadID            string
	turnID              string
	answer              strings.Builder
	finalMessageEmitted bool
	approvals           map[string]chan bool

	// 本轮 token 用量（thread/tokenUsage/updated 的 last 增量逐通知累计）；
	// 过程经 OnUsage 流式观测（累计值逐帧覆盖），终态结算出口是 ExecResult.Usage。
	usageIn       int64
	usageOut      int64
	usageCached   int64
	usageSeen     bool
	childIDs      map[string]struct{}
	childEmitted  map[string]map[string]struct{}
	childMeta     map[string]childMetadata
	turnStartedAt time.Time
}

type childMetadata struct {
	name, description, preview, parent string
	createdAt                          time.Time
}

func (s *execStream) send(frame map[string]any) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stdin == nil {
		return fmt.Errorf("stdin closed")
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func (s *execStream) request(method string, params any) (int64, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.pendingRequests[id] = method
	s.mu.Unlock()
	if err := s.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		s.mu.Lock()
		delete(s.pendingRequests, id)
		s.mu.Unlock()
		return id, err
	}
	return id, nil
}

func (s *execStream) popRequest(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	method := s.pendingRequests[id]
	delete(s.pendingRequests, id)
	return method
}

// steer 把 steering 输入转为 turn/steer（需要活动 threadId+turnId 前置条件）。
func (s *execStream) steer(instruction string) error {
	s.mu.Lock()
	threadID, turnID := s.threadID, s.turnID
	s.mu.Unlock()
	if threadID == "" || turnID == "" {
		return fmt.Errorf("codex turn 尚未就绪")
	}
	_, err := s.request("turn/steer", map[string]any{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input":          []map[string]any{{"type": "text", "text": instruction}},
	})
	return err
}

// sendInterruptRequest 原生精确取消：先释放等待中的审批（cancel 决定本身会中断
// Turn；permissions 没有 cancel 决定，handler 回写空权限后会再次调用本方法），
// 再对活动 Turn 发送 turn/interrupt。
func (s *execStream) sendInterruptRequest() {
	if s.resolveAll(false) > 0 {
		return
	}
	s.mu.Lock()
	threadID, turnID := s.threadID, s.turnID
	s.mu.Unlock()
	if threadID == "" || turnID == "" {
		return
	}
	_, _ = s.request("turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID})
}

func (s *execStream) appendAnswer(text string) {
	s.mu.Lock()
	s.answer.WriteString(text)
	s.mu.Unlock()
}

func (s *execStream) resetAnswerState() {
	s.mu.Lock()
	s.answer.Reset()
	s.finalMessageEmitted = false
	s.mu.Unlock()
}

// accumulateUsage 累计一条归因到本 turn 的用量增量（调用方负责 turnId 过滤）。
func (s *execStream) accumulateUsage(ev tokenUsageEvent) {
	s.mu.Lock()
	s.usageIn += ev.Input
	s.usageOut += ev.Output
	s.usageCached += ev.Cached
	s.usageSeen = true
	s.mu.Unlock()
}

// usageSnapshot 收尾用量（per_run 增量，与 kimiapp 同口径）；未见用量帧或全零
// 返回 nil（不捏造上报）。
func (s *execStream) usageSnapshot() *runtime.Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.usageSeen || (s.usageIn == 0 && s.usageOut == 0) {
		return nil
	}
	return &runtime.Usage{
		InputTokens: s.usageIn, OutputTokens: s.usageOut,
		CachedTokens: s.usageCached, Basis: runtime.UsagePerRun,
	}
}

// emitUsageProgress 累计后即时过程观测：上报当前累计值（OnUsage 覆盖语义，
// 终态结算仍以 ExecResult.Usage 为准）。
func (s *execStream) emitUsageProgress() {
	s.mu.Lock()
	u := runtime.Usage{
		InputTokens: s.usageIn, OutputTokens: s.usageOut,
		CachedTokens: s.usageCached, Basis: runtime.UsagePerRun,
	}
	s.mu.Unlock()
	s.ex.Callbacks.OnUsage(u)
}

// finalAnswer 权威答案只发一次（同一 agent 消息段内 item/completed 与 turn/completed 去重）。
func (s *execStream) finalAnswer(authoritative string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalMessageEmitted {
		return "", false
	}
	text := strings.TrimSpace(authoritative)
	if text == "" {
		text = strings.TrimSpace(s.answer.String())
	}
	if text == "" {
		return "", false
	}
	s.finalMessageEmitted = true
	return text, true
}

func (s *execStream) resolveApproval(approvalID string, approved bool) bool {
	s.mu.Lock()
	ch := s.approvals[approvalID]
	s.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- approved:
		return true
	default:
		return false
	}
}

func (s *execStream) resolveAll(approved bool) int {
	s.mu.Lock()
	chs := make([]chan bool, 0, len(s.approvals))
	for _, ch := range s.approvals {
		chs = append(chs, ch)
	}
	s.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- approved:
		default:
		}
	}
	return len(chs)
}

func (s *execStream) closeStdin() {
	s.mu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	s.mu.Unlock()
}

// reap 正常完成时先关 stdin 并等待 app-server 自然退出；只有超时才升级信号。
func (s *execStream) reap(grace time.Duration, stderrDone <-chan struct{}) error {
	s.closeStdin()
	select {
	case err := <-s.done:
		<-stderrDone
		return err
	case <-time.After(grace):
		signalGroup(s.cmd, s.pgid, sigInt)
		select {
		case err := <-s.done:
			<-stderrDone
			return err
		case <-time.After(grace):
			signalGroup(s.cmd, s.pgid, sigKill)
			err := <-s.done
			<-stderrDone
			return err
		}
	}
}

// watchCancel Ctx 取消（终态意图/服务关停）：先回拒审批并发送原生 turn/interrupt，
// 等待 turn/completed 或宽限后升级为进程组 SIGINT → SIGKILL。
func (s *execStream) watchCancel(grace time.Duration, stop <-chan struct{}, turnDone <-chan struct{}) {
	select {
	case <-s.ctx.Done():
	case <-stop:
		return
	}
	s.sendInterruptRequest()
	select {
	case <-turnDone:
	case <-time.After(grace):
		signalGroup(s.cmd, s.pgid, sigInt)
		select {
		case <-turnDone:
		case <-time.After(grace):
			signalGroup(s.cmd, s.pgid, sigKill)
		}
	}
}

// consumeControls 消费已声明能力的控制流：input → turn/steer，approval → 精确
// 匹配 ApprovalID 回写（终态意图不经 Controls，由 Ctx 取消表达）。
func (s *execStream) consumeControls(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case c, ok := <-s.ex.Controls:
			if !ok {
				return
			}
			switch c.Kind {
			case runtime.ControlInput:
				_ = s.steer(c.Instruction)
			case runtime.ControlApproval:
				s.resolveApproval(c.ApprovalID, c.Approved)
			}
		}
	}
}

// ── 请求-通知流驱动 ─────────────────────────────────────────────────

// pumpResult 一轮流驱动的原始产出；终态裁决在 composeResult。
type pumpResult struct {
	session    *runtime.SessionUpdate
	usage      *runtime.Usage
	failure    *runtime.Failure
	finished   bool   // turn/completed 已见
	turnStatus string // completed | interrupted | failed
	turnErrMsg string
}

// pump 驱动 initialize 握手 → thread/start|resume → turn/start → 通知消费，
// 直到 turn/completed、协议失败或流中断。事件映射与旧实现逐字段一致。
func (s *execStream) pump(reader *bufio.Reader) *pumpResult {
	res := &pumpResult{}
	// 任意退出路径（含流中断/失败）都带上已观测到的本轮用量。
	defer func() { res.usage = s.usageSnapshot() }()
	// 1) initialize 握手（版本门）。
	if _, err := s.request("initialize", initializeParams()); err != nil {
		res.failure = ioFailure("io_failed", err.Error(), true)
		return res
	}
	for {
		frame, err := readFrame(reader, s.module.cfg.MaxFrameBytes)
		if err != nil {
			if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
				// 帧超限等流协议违约（对齐 kimi：internal、不可重试）。
				res.failure = &runtime.Failure{
					Family: runtime.FamilyInternal, Code: "stream_failed",
					Message: truncateMessage(err.Error()), Retryable: false,
				}
			}
			return res
		}
		if frame == nil {
			continue
		}

		// 服务端请求（带 id + method）：审批路由到工作台。
		if frame.ID != nil && frame.Method != "" {
			s.handleServerRequest(frame)
			continue
		}
		// 我方请求的响应。
		if frame.ID != nil {
			method := s.popRequest(*frame.ID)
			if frame.Error != nil {
				res.failure = codexFailure("codex_error", fmt.Sprintf("%s: %s", method, frame.Error.Message))
				return res
			}
			switch method {
			case "initialize":
				var initResult struct {
					UserAgent string `json:"userAgent"`
				}
				_ = json.Unmarshal(frame.Result, &initResult)
				s.module.setProviderVersion(initResult.UserAgent)
				// 官方握手要求 initialize 响应后发送 initialized 通知，再调用其他方法。
				if err := s.send(initializedNotification()); err != nil {
					res.failure = ioFailure("io_failed", err.Error(), true)
					return res
				}
				if s.policy.Mode == "plan" && effectiveCodexModel(s.module.cfg, s.model) == "" {
					// plan 模式未指定模型：先 model/list 发现默认模型。
					if _, err := s.request("model/list", map[string]any{"limit": 20, "includeHidden": false}); err != nil {
						res.failure = ioFailure("io_failed", err.Error(), true)
						return res
					}
					break
				}
				if err := s.requestThread(); err != nil {
					res.failure = ioFailure("io_failed", err.Error(), true)
					return res
				}
			case "model/list":
				model := defaultModelFromList(frame.Result)
				if model == "" {
					res.failure = configFailure("model_unavailable", "Codex 未返回 Plan 模式可用模型")
					return res
				}
				s.model = model
				if err := s.requestThread(); err != nil {
					res.failure = ioFailure("io_failed", err.Error(), true)
					return res
				}
			case "thread/start", "thread/resume":
				var r struct {
					Thread struct {
						ID        string `json:"id"`
						SessionID string `json:"sessionId"`
					} `json:"thread"`
				}
				_ = json.Unmarshal(frame.Result, &r)
				if r.Thread.ID == "" {
					res.failure = &runtime.Failure{
						Family: runtime.FamilyInternal, Code: "thread_start_failed",
						Message: "thread/start 未返回 threadId", Retryable: false,
					}
					return res
				}
				s.mu.Lock()
				s.threadID = r.Thread.ID
				s.mu.Unlock()
				// 首个 threadId 立即上报：崩溃也不丢 resume 时机。
				res.session = &runtime.SessionUpdate{Ref: "codex://" + r.Thread.ID}
				s.ex.Callbacks.OnSession(*res.session)
				turnParams := map[string]any{
					"threadId": r.Thread.ID,
					"input":    []map[string]any{{"type": "text", "text": s.ex.Instruction}},
					"effort":   s.reasoningEffort,
				}
				if mode := codexCollaborationMode(s.policy, s.module.cfg, s.model, s.systemPrompt); mode != nil {
					turnParams["collaborationMode"] = mode
				}
				if _, err := s.request("turn/start", turnParams); err != nil {
					res.failure = ioFailure("io_failed", err.Error(), true)
					return res
				}
			case "turn/start":
				var r struct {
					Turn struct {
						ID     string `json:"id"`
						Status string `json:"status"`
					} `json:"turn"`
				}
				_ = json.Unmarshal(frame.Result, &r)
				if r.Turn.ID != "" {
					s.mu.Lock()
					s.turnID = r.Turn.ID
					s.turnStartedAt = time.Now().UTC()
					s.mu.Unlock()
				}
			}
			continue
		}

		// 服务端通知。
		switch frame.Method {
		case "thread/started", "thread/status/changed":
			// thread 生命周期投影：无状态迁移，仅记录。
		case "thread/tokenUsage/updated":
			// 用量增量只认本 turn：每次通知的 last 即 thread 累计 total 的本次增量，
			// 逐通知求和 = 本轮用量（per_run）；resume 重放归因旧 turn、turn 未就绪
			// （turnId 未知/不匹配）的通知一律不计入。
			if ev, ok := parseTokenUsageEvent(frame.Params); ok {
				s.mu.Lock()
				turnID := s.turnID
				s.mu.Unlock()
				if turnID != "" && ev.TurnID == turnID {
					s.accumulateUsage(ev)
					s.emitUsageProgress()
				}
			}
		case "thread/compacted":
			// 弃用路径（schema 在册、0.149.0 不向 v2 发射）：防协议漂移的兜底，
			// 真实路径是 item/completed(contextCompaction)。
			s.ex.Callbacks.OnEvent(domain.EventSessionCompacted, compactedPayload(frame.Params))
		case "turn/started":
			var n struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(frame.Params, &n)
			if agent := s.eventAgent(frame.Params); agent != "" {
				s.emitChildSnapshot(agent, "running", "", "")
				continue
			}
			if n.Turn.ID != "" {
				s.mu.Lock()
				s.turnID = n.Turn.ID
				s.turnStartedAt = time.Now().UTC()
				s.mu.Unlock()
			}
		case "turn/plan/updated":
			// todo 清单（update_plan 工具）每帧携带全量 steps：canonical 替换语义
			// 由通知天然保证；与 plan 模式的 plan item（提案正文）是两条通道，互不抑制。
			if _, steps, ok := parsePlanUpdatedEvent(frame.Params); ok {
				s.ex.Callbacks.OnEvent(domain.EventRunPlanUpdated, map[string]any{"steps": steps})
			} else {
				// 缺 plan 键的畸形帧：schema 要求 plan 必填，显式 warn 保持可观测。
				s.ex.Callbacks.OnLog("codexapp", "warn malformed turn/plan/updated "+rawString(frame.Params))
			}
		case "item/started":
			item := parseItemEvent(frame.Params)
			agent := s.eventAgent(frame.Params)
			s.markChildItem(agent, item.ID)
			s.trackCollab(item)
			if item.isTool() {
				s.markChildKind(agent, "tool")
				payload := item.canonicalPayload()
				if agent != "" {
					payload["agent_id"] = agent
				}
				if s := item.argsSummary(); s != "" {
					payload["args_summary"] = s
				}
				if a := codexArgsJSON(frame.Params); a != "" {
					payload["args"] = a
				}
				s.ex.Callbacks.OnEvent(domain.EventToolStarted, payload)
			} else if (item.Type == "agentMessage" || item.Type == "plan") && agent == "" {
				s.resetAnswerState()
			}
		case "item/completed":
			item := parseItemEvent(frame.Params)
			agent := s.eventAgent(frame.Params)
			s.markChildItem(agent, item.ID)
			s.trackCollab(item)
			switch {
			case item.Type == "contextCompaction":
				// 0.149.0 v2 压缩事实的 canonical 路径：item/started 只表示压缩
				// 进行中，completed 才成立（天然去重，无 turnId 重放陷阱——resume
				// 不重放历史 item 通知）。
				s.ex.Callbacks.OnEvent(domain.EventSessionCompacted, compactedPayload(frame.Params))
			case item.Type == "agentMessage" || item.Type == "plan":
				if agent != "" {
					if item.Text != "" {
						s.markChildKind(agent, "message")
						s.ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{"agent_id": agent, "role": "assistant", "text": item.Text, "item_type": item.Type})
					}
					continue
				}
				if text, ok := s.finalAnswer(item.Text); ok {
					s.ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{
						"role": "assistant", "text": text, "item_type": item.Type,
					})
					s.resetAnswerState()
				}
			case item.isTool():
				s.markChildKind(agent, "tool")
				payload := item.canonicalPayload()
				if agent != "" {
					payload["agent_id"] = agent
				}
				if out := item.resultOutput(); out != "" {
					payload["output"] = out
				}
				if ec, ok := item.Raw["exitCode"]; ok {
					payload["exit_code"] = ec
				}
				s.ex.Callbacks.OnEvent(toolCompletionEvent(item), payload)
			}
		case "item/agentMessage/delta", "item/plan/delta":
			text := codexDeltaText(frame.Params)
			if text != "" {
				agent := s.eventAgent(frame.Params)
				if agent == "" {
					s.appendAnswer(text)
				}
				payload := map[string]any{
					"role": "assistant", "raw": map[string]any{"chunk": map[string]any{"type": "text-delta", "text": text}},
				}
				if agent != "" {
					payload["agent_id"] = agent
				}
				s.ex.Callbacks.OnEvent(domain.EventMessageDelta, payload)
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			if text := codexDeltaText(frame.Params); text != "" {
				payload := map[string]any{
					"role": "assistant", "raw": map[string]any{"chunk": map[string]any{"type": "reasoning-delta", "text": text}},
				}
				if agent := s.eventAgent(frame.Params); agent != "" {
					payload["agent_id"] = agent
					s.markChildKind(agent, "reasoning")
				}
				s.ex.Callbacks.OnEvent(domain.EventMessageDelta, payload)
			}
		case "item/commandExecution/outputDelta":
			if text := codexDeltaText(frame.Params); text != "" {
				payload := map[string]any{"tool": "shell", "text": truncateSummary(text)}
				if agent := s.eventAgent(frame.Params); agent != "" {
					payload["agent_id"] = agent
					s.markChildKind(agent, "tool")
				}
				var ref struct {
					ItemID string `json:"itemId"`
				}
				_ = json.Unmarshal(frame.Params, &ref)
				if ref.ItemID != "" {
					payload["call_id"] = ref.ItemID
				}
				s.ex.Callbacks.OnEvent(domain.EventToolProgress, payload)
			}
		case "turn/completed":
			if agent := s.eventAgent(frame.Params); agent != "" {
				s.emitChildSnapshot(agent, "completed", "", "")
				continue
			}
			var n struct {
				Turn struct {
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(frame.Params, &n)
			res.finished = true
			res.turnStatus = n.Turn.Status
			if n.Turn.Error != nil {
				res.turnErrMsg = n.Turn.Error.Message
			}
			if n.Turn.Status == "completed" {
				if answer, ok := s.finalAnswer(""); ok {
					s.ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{
						"role": "assistant", "text": answer,
					})
					s.resetAnswerState()
				}
			}
			s.hydrateChildren(reader)
			return res
		case "error":
			res.failure = codexFailure("codex_error", rawString(frame.Params))
			return res
		default:
			// 未识别的 app-server 通知：显式记 warn 日志保持可观测（协议词表
			// 演进时能发现），不改变通知流继续消费。
			s.ex.Callbacks.OnLog("codexapp", "warn unhandled notification "+frame.Method+" "+rawString(frame.Params))
		}
	}
}

// 记录本轮协作项声明的精确子线程集合。
func (s *execStream) trackCollab(item itemEvent) {
	if item.Type != "collabAgentToolCall" {
		return
	}
	for _, id := range item.ReceiverThreadIDs {
		s.mu.Lock()
		s.childIDs[id] = struct{}{}
		s.mu.Unlock()
		status := "running"
		if state, ok := item.AgentsStates[id].(map[string]any); ok {
			if v, ok := state["status"].(string); ok && v != "" {
				status = codexChildStatus(v)
			}
		}
		if item.argsSummary() != "" {
			s.mu.Lock()
			meta := s.childMeta[id]
			if meta.preview == "" {
				meta.preview = item.argsSummary()
			}
			s.childMeta[id] = meta
			s.mu.Unlock()
		}
		s.emitChildSnapshot(id, status, "", "")
	}
}

func (s *execStream) emitChildSnapshot(id, status, summary, failure string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	meta := s.childMeta[id]
	parent := s.threadID
	s.mu.Unlock()
	name, description := meta.name, meta.description
	if meta.parent != "" {
		parent = meta.parent
	}
	if name == "" {
		name = id
	}
	if description == "" {
		description = meta.preview
	}
	if description == "" {
		description = "Codex 子 Agent"
	}
	s.ex.Callbacks.OnEvent(domain.EventSubagentUpdated, map[string]any{
		"runtime": "codex", "role": "child", "id": id, "subagent_id": id,
		"name": name, "description": description, "status": codexChildStatus(status),
		"summary": truncateOutput(summary), "error": truncateMessage(failure), "parent_thread_id": parent,
	})
}

func codexChildStatus(status string) string {
	switch strings.ToLower(status) {
	case "pendinginit", "pending", "queued":
		return "queued"
	case "running", "inprogress":
		return "running"
	case "completed", "succeeded", "success":
		return "completed"
	case "interrupted", "shutdown", "cancelled", "canceled":
		return "stopped"
	case "errored", "error", "failed", "notfound":
		return "failed"
	default:
		return "running"
	}
}

func codexCreatedAt(value any) time.Time {
	switch v := value.(type) {
	case float64:
		if v > 1e12 {
			v /= 1000
		}
		return time.Unix(int64(v), 0).UTC()
	case json.Number:
		f, _ := v.Float64()
		if f > 1e12 {
			f /= 1000
		}
		return time.Unix(int64(f), 0).UTC()
	case string:
		t, _ := time.Parse(time.RFC3339Nano, v)
		return t
	}
	return time.Time{}
}

func codexChildBelongsToTurn(id string, createdAt any, started time.Time, exactIDs map[string]struct{}) bool {
	if len(exactIDs) > 0 {
		_, ok := exactIDs[id]
		return ok
	}
	if started.IsZero() {
		return true
	}
	created := codexCreatedAt(createdAt)
	return !created.IsZero() && !created.Before(started.Add(-2*time.Second))
}

func (s *execStream) currentThreadID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.threadID }

func (s *execStream) eventAgent(raw json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
		ItemID   string `json:"itemId"`
	}
	_ = json.Unmarshal(raw, &p)
	s.mu.Lock()
	root := s.threadID
	s.mu.Unlock()
	if p.ThreadID != "" && p.ThreadID != root {
		return p.ThreadID
	}
	return ""
}

func (s *execStream) markChildItem(agent, item string) {
	if agent == "" || item == "" {
		return
	}
	s.mu.Lock()
	if s.childEmitted[agent] == nil {
		s.childEmitted[agent] = map[string]struct{}{}
	}
	s.childEmitted[agent][item] = struct{}{}
	s.mu.Unlock()
}

func (s *execStream) markChildKind(agent, kind string) {
	if kind == "" {
		return
	}
	s.markChildItem(agent, "@kind/"+kind)
}

// 通过 app-server 补齐本轮子线程；不读取 rollout JSONL。
func (s *execStream) hydrateChildren(reader *bufio.Reader) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.childIDs))
	for id := range s.childIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parent := s.threadID
	s.mu.Unlock()
	if parent == "" {
		return
	}
	result, ok := s.rpcDuringPump(reader, "thread/list", map[string]any{"ancestorThreadId": parent, "limit": 100})
	if ok {
		var body struct {
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(result, &body) == nil {
			s.mu.Lock()
			started := s.turnStartedAt
			selected := make(map[string]struct{}, len(ids))
			for _, id := range ids {
				selected[id] = struct{}{}
			}
			for _, child := range body.Data {
				id, _ := child["id"].(string)
				if id == "" {
					continue
				}
				if !codexChildBelongsToTurn(id, child["createdAt"], started, s.childIDs) {
					continue
				}
				meta := childMetadata{parent: parent}
				if value, ok := child["parentThreadId"].(string); ok && value != "" {
					meta.parent = value
				}
				meta.name, _ = child["agentRole"].(string)
				if meta.name == "" {
					meta.name, _ = child["agentNickname"].(string)
				}
				meta.description, _ = child["preview"].(string)
				if meta.description == "" {
					meta.description, _ = child["task"].(string)
				}
				meta.preview, _ = child["preview"].(string)
				s.childMeta[id] = meta
				if _, exists := selected[id]; !exists {
					selected[id] = struct{}{}
					ids = append(ids, id)
				}
			}
			s.mu.Unlock()
		}
	}
	sort.Strings(ids)
	for _, child := range ids {
		status, summary, failure := s.hydrateChild(reader, child)
		s.emitChildSnapshot(child, status, summary, failure)
	}
}

func (s *execStream) hydrateChild(reader *bufio.Reader, child string) (string, string, string) {
	cursor := ""
	seen := map[string]struct{}{}
	status, summary, failure := "completed", "", ""
	for {
		params := map[string]any{"threadId": child, "itemsView": "full", "sortDirection": "asc", "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, ok := s.rpcDuringPump(reader, "thread/turns/list", params)
		if !ok {
			return status, summary, failure
		}
		var body struct {
			Data []struct {
				ID     string           `json:"id"`
				Status string           `json:"status"`
				Error  map[string]any   `json:"error"`
				Items  []map[string]any `json:"items"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(result, &body) != nil {
			return status, summary, failure
		}
		for _, turn := range body.Data {
			if turn.Status != "" {
				status = codexChildStatus(turn.Status)
			}
			if turn.Error != nil {
				failure, _ = turn.Error["message"].(string)
				if failure != "" {
					status = "failed"
				}
			}
			for _, item := range turn.Items {
				key, _ := item["id"].(string)
				if key == "" {
					key = fmt.Sprintf("%s:%v", turn.ID, item["type"])
				}
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				if typ, _ := item["type"].(string); typ == "agentMessage" {
					summary, _ = item["text"].(string)
				}
				s.emitHydratedItem(child, item)
			}
		}
		if body.NextCursor == "" {
			return status, summary, failure
		}
		cursor = body.NextCursor
	}
}

func (s *execStream) rpcDuringPump(reader *bufio.Reader, method string, params map[string]any) (json.RawMessage, bool) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.mu.Unlock()
	if err := s.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, false
	}
	for {
		frame, err := readFrame(reader, s.module.cfg.MaxFrameBytes)
		if err != nil || frame == nil {
			if err != nil {
				return nil, false
			}
			continue
		}
		if frame.ID != nil && *frame.ID == id {
			if frame.Error != nil {
				return nil, false
			}
			return frame.Result, true
		}
	}
}

func (s *execStream) emitHydratedItem(child string, item map[string]any) {
	typ, _ := item["type"].(string)
	id, _ := item["id"].(string)
	if id == "" {
		return
	}
	s.mu.Lock()
	if s.childEmitted[child] == nil {
		s.childEmitted[child] = map[string]struct{}{}
	}
	kind := ""
	switch typ {
	case "reasoning", "reasoningSummary":
		kind = "reasoning"
	case "agentMessage", "plan":
		kind = "message"
	default:
		if parseItemEvent(mustJSON(map[string]any{"item": item})).isTool() {
			kind = "tool"
		}
	}
	if kind != "" {
		if _, ok := s.childEmitted[child]["@kind/"+kind]; ok {
			s.mu.Unlock()
			return
		}
	}
	if _, ok := s.childEmitted[child][id]; ok {
		s.mu.Unlock()
		return
	}
	s.childEmitted[child][id] = struct{}{}
	s.mu.Unlock()
	text, _ := item["text"].(string)
	if text == "" {
		for _, key := range []string{"summary", "content"} {
			if parts, ok := item[key].([]any); ok {
				var ss []string
				for _, p := range parts {
					if v, ok := p.(string); ok {
						ss = append(ss, v)
					}
				}
				text = strings.Join(ss, "\n")
				if text != "" {
					break
				}
			}
		}
	}
	switch typ {
	case "reasoning", "reasoningSummary":
		if text != "" {
			s.ex.Callbacks.OnEvent(domain.EventMessageDelta, map[string]any{"agent_id": child, "role": "assistant", "raw": map[string]any{"chunk": map[string]any{"type": "reasoning-delta", "text": text}}})
		}
	case "agentMessage", "plan":
		if text != "" {
			s.ex.Callbacks.OnEvent(domain.EventMessageCompleted, map[string]any{"agent_id": child, "role": "assistant", "text": text, "item_type": typ})
		}
	default:
		ie := parseItemEvent(mustJSON(map[string]any{"item": item}))
		if !ie.isTool() {
			return
		}
		p := ie.canonicalPayload()
		p["agent_id"] = child
		if a := ie.argsSummary(); a != "" {
			p["args_summary"] = a
		}
		if out := ie.resultOutput(); out != "" {
			p["output"] = out
		}
		if ie.Status == "inProgress" || ie.Status == "started" {
			s.ex.Callbacks.OnEvent(domain.EventToolStarted, p)
		} else {
			// 历史完整项只有终态，补发 started 以保持统一卡片结构。
			s.ex.Callbacks.OnEvent(domain.EventToolStarted, p)
			s.ex.Callbacks.OnEvent(toolCompletionEvent(ie), p)
		}
	}
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// compactedPayload session.compacted 的 data：turnId 可得则带（thread/compacted
// 与 item/completed 信封均携 turnId），否则空对象——canonical 契约允许两形态。
func compactedPayload(raw json.RawMessage) map[string]any {
	data := map[string]any{}
	var n struct {
		TurnID string `json:"turnId"`
	}
	_ = json.Unmarshal(raw, &n)
	if n.TurnID != "" {
		data["turnId"] = n.TurnID
	}
	return data
}

// maxToolArgs tool.started args 全文截断上限（与 kimiapp/dsh 的 canonical 契约
// 一致：args_summary ≤200 只是一行摘要，args 供前端 IN/OUT 展开卡还原完整入参）。
const maxToolArgs = 2000

// codexArgsJSON 工具完整入参的紧凑 JSON 文本（截断 ≤maxToolArgs）：仅
// item.arguments 为 JSON 对象/数组时携带（commandExecution 只有纯文本 command
// 无 arguments 键，不携带）。从帧原文取 RawMessage 直接透传，不重新 marshal
// 已 map 化的 item 以保键序；截断发生在字符串层，不保证截断后仍是合法 JSON
// （前端只展示，不解析）。
func codexArgsJSON(raw json.RawMessage) string {
	var envelope struct {
		Item struct {
			Arguments json.RawMessage `json:"arguments"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	s := strings.TrimSpace(string(envelope.Item.Arguments))
	if s == "" {
		return ""
	}
	var v any
	if json.Unmarshal(envelope.Item.Arguments, &v) != nil {
		return ""
	}
	switch v.(type) {
	case map[string]any, []any:
		if len(s) > maxToolArgs {
			s = s[:maxToolArgs]
		}
		return s
	}
	return ""
}

// handleServerRequest 处理三类官方审批请求；决定经 Controls（ControlApproval）
// 送达，非审批类服务端请求显式拒绝（禁止静默降级）。当前执行循环串行处理审批
// （服务端在等待响应），与旧实现一致。
func (s *execStream) handleServerRequest(frame *rpcFrame) {
	if !isApprovalMethod(frame.Method) {
		_ = s.send(map[string]any{"id": *frame.ID, "error": map[string]any{
			"code": -32601, "message": "unsupported server request: " + frame.Method,
		}})
		return
	}
	params := parseApprovalParams(frame.Params)
	approvalID := s.ex.Callbacks.RequestApproval(approvalKind(frame.Method), "high",
		approvalSummary(frame.Method, params))
	if approvalID == "" {
		// 发起失败：立即回拒绝，防止服务端悬挂。
		_ = s.send(map[string]any{"id": *frame.ID, "result": approvalResponse(frame.Method, false, params)})
		return
	}
	ch := make(chan bool, 1)
	s.mu.Lock()
	s.approvals[approvalID] = ch
	s.mu.Unlock()
	var approved bool
	select {
	case approved = <-ch:
	case <-s.ctx.Done():
		approved = false
	}
	s.mu.Lock()
	delete(s.approvals, approvalID)
	s.mu.Unlock()
	_ = s.send(map[string]any{"id": *frame.ID, "result": approvalResponse(frame.Method, approved, params)})
	// permissions 响应没有 cancel 决定；拒绝后显式中断以匹配工作台的 cancelling 状态。
	if !approved && frame.Method == "item/permissions/requestApproval" {
		s.sendInterruptRequest()
	}
}

// ── 终态裁决与失败分类 ───────────────────────────────────────────────

// composeResult 终态判定优先级（对齐 kimi.terminalResult）：终态意图 >
// turn/completed > 流中断分类。审批拒绝等无意图中断映射 cancelled（旧实现
// running→cancelling→cancelled 语义）。
func composeResult(ex *runtime.ExecContext, ctx context.Context, in *pumpResult, waitErr error) runtime.ExecResult {
	// 用量对任意终态都随行（已消费的 token 不因取消/失败而消失）。
	result := runtime.ExecResult{Session: in.session, Usage: in.usage}
	if ctx.Err() != nil {
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
	case in.failure != nil:
		result.Outcome, result.Failure = runtime.OutcomeFailed, in.failure
	case in.finished && in.turnStatus == "completed":
		result.Outcome = runtime.OutcomeSucceeded
	case in.finished && in.turnStatus == "failed":
		message := strings.TrimSpace(in.turnErrMsg)
		if message == "" {
			message = "codex turn failed"
		}
		result.Outcome, result.Failure = runtime.OutcomeFailed, codexFailure("turn_failed", message)
	case in.finished:
		result.Outcome = runtime.OutcomeCancelled
	default:
		detail := "stream ended without turn/completed"
		if waitErr != nil {
			detail = fmt.Sprintf("%s; exit: %v", detail, waitErr)
		}
		result.Outcome, result.Failure = runtime.OutcomeFailed, ioFailure("stream_failed", detail, true)
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
	return runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: &runtime.Failure{
		Family: runtime.FamilyIO, Code: "context_cancelled", Message: truncateMessage(detail),
	}}
}

// codexFailure provider 侧本轮错误分类（对齐 kimi.turnFailure 语义）：
// quota/429/rate limit → provider_quota；auth 类 → config；thread/resume 目标
// 已丢失（not found/unknown thread 类文案）→ session_unknown（不可重试，交
// 应用层自愈清锚点重建）；其余（网络/5xx 等）→ transient_upstream，provider
// 错误默认可重试。
func codexFailure(code, message string) *runtime.Failure {
	low := strings.ToLower(message)
	family, retryable := runtime.FamilyTransientUpstream, true
	switch {
	case containsAny(low, "quota", "429", "rate limit"):
		family = runtime.FamilyProviderQuota
	case containsAny(low, "auth", "unauthorized", "forbidden", "401", "403", "login", "api key"):
		family, retryable = runtime.FamilyConfig, false
	case containsAny(low,
		"thread not found", "unknown thread", "no such thread",
		// codex 0.149.0 thread/resume 死锚点的真实文案（code -32600）。
		"no rollout found",
		"session not found", "unknown session", "no such session",
		"conversation not found", "no conversation found"):
		// 已丢失的 codex thread 盲目重试只会原地失败：显式 session_unknown
		// 让应用层清锚点并用全量历史 fresh 重建。注意不用裸 "not found"——
		// 会误吞 "method not found"/"model not found" 等无关错误。
		family, retryable = runtime.FamilySessionUnknown, false
	}
	return &runtime.Failure{Family: family, Code: code, Message: truncateMessage(message), Retryable: retryable}
}

func configFailure(code, message string) *runtime.Failure {
	return &runtime.Failure{Family: runtime.FamilyConfig, Code: code, Message: truncateMessage(message), Retryable: false}
}

func ioFailure(code, message string, retryable bool) *runtime.Failure {
	return &runtime.Failure{Family: runtime.FamilyIO, Code: code, Message: truncateMessage(message), Retryable: retryable}
}

func failedResult(f *runtime.Failure) runtime.ExecResult {
	return runtime.ExecResult{Outcome: runtime.OutcomeFailed, Failure: f}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// truncateMessage 与旧 failRun 一致：trim + 200 字符截断。
func truncateMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// ── 参数组装 ─────────────────────────────────────────────────────────

func (s *execStream) requestThread() error {
	method := "thread/start"
	params := threadStartParams(s.module.cfg, s.model, s.systemPrompt, s.policy)
	if s.resumeThreadID != "" {
		method = "thread/resume"
		params["threadId"] = s.resumeThreadID
	}
	_, err := s.request(method, params)
	return err
}

func threadStartParams(cfg Config, model, systemPrompt string, policy runtime.PolicySnapshot) map[string]any {
	params := map[string]any{"cwd": cfg.WorkspaceRoot, "serviceName": "agent-team-workbench"}
	// per-run 模型快照优先（模型注册表），缺省回退 adapter 配置。
	model = effectiveCodexModel(cfg, model)
	if model != "" {
		params["model"] = model
	}
	if strings.TrimSpace(systemPrompt) != "" {
		params["developerInstructions"] = systemPrompt
	}
	params["approvalPolicy"] = codexApprovalPolicy(policy.ApprovalPolicy)
	params["sandbox"] = codexSandbox(policy.Sandbox)
	return params
}

func codexApprovalPolicy(policy string) string {
	switch policy {
	case "auto":
		return "never"
	case "manual":
		return "untrusted"
	default:
		return "on-request"
	}
}

func codexSandbox(sandbox string) string {
	switch sandbox {
	case "read-only", "danger-full-access":
		return sandbox
	default:
		return "workspace-write"
	}
}

func codexCollaborationMode(policy runtime.PolicySnapshot, cfg Config, model, systemPrompt string) map[string]any {
	if policy.Mode != "plan" {
		return nil
	}
	model = effectiveCodexModel(cfg, model)
	if model == "" {
		return nil
	}
	settings := map[string]any{"model": model}
	if strings.TrimSpace(systemPrompt) != "" {
		settings["developer_instructions"] = systemPrompt
	} else {
		settings["developer_instructions"] = nil
	}
	return map[string]any{"mode": "plan", "settings": settings}
}

func effectiveCodexModel(cfg Config, model string) string {
	if strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	return strings.TrimSpace(cfg.Model)
}

func codexTurnReasoningEffort(effort string) string {
	normalized := strings.TrimSpace(strings.ToLower(effort))
	switch normalized {
	case "minimal", "low", "medium", "high", "xhigh", "ultra":
		return normalized
	default:
		return "ultra"
	}
}

func defaultModelFromList(raw json.RawMessage) string {
	var result struct {
		Data []struct {
			ID        string `json:"id"`
			Model     string `json:"model"`
			IsDefault bool   `json:"isDefault"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return ""
	}
	for _, model := range result.Data {
		if model.IsDefault {
			if model.Model != "" {
				return model.Model
			}
			return model.ID
		}
	}
	if len(result.Data) == 0 {
		return ""
	}
	if result.Data[0].Model != "" {
		return result.Data[0].Model
	}
	return result.Data[0].ID
}

// drainStderr：stderr 原始行 → OnLog（超长截断同旧实现）。
func drainStderr(stderr io.Reader, cb runtime.Callbacks) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 16*1024), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 2048 {
			line = line[:2048] + "…(truncated)"
		}
		cb.OnLog("stderr", line)
	}
}
