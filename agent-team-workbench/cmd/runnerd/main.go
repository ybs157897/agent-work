// cmd/runnerd 是本地 Go Runner（M2）：出站 WSS 连接控制平面，
// 接受 run.offer 并在本地执行 Adapter，按 runner_seq 上报 canonical 事件等待 ACK。
// 断线后指数退避重连并按 seq 有序重发未 ACK 事件（至少一次投递，服务端去重）。
// 执行面：adapter_id=dsh → 真实 DeepSeek Harness SDK 子进程；其他 → mock 模拟。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/domain"
	rt "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/dsh"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/kimiapp"
)

type envelope struct {
	V         int             `json:"v"`
	MessageID string          `json:"message_id"`
	Kind      string          `json:"kind"`
	Method    string          `json:"method"`
	RunnerID  string          `json:"runner_id"`
	RunID     string          `json:"run_id,omitempty"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	SentAt    time.Time       `json:"sent_at"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type runSpec struct {
	RunID     string         `json:"run_id"`
	AdapterID string         `json:"adapter_id"`
	Input     map[string]any `json:"input"`
}

type runner struct {
	id   string
	conn *websocket.Conn
	mu   sync.Mutex
	seq  int64
	// pending 未 ACK 事件：重连后重发（服务端按 runner_seq 去重）。
	pending map[int64][]byte
	// approvals 每个 run 的审批决定 channel（mock 路径）。mock 流程串行、
	// 同时至多一个待决审批，单 channel 即可；模块路径按 approval_id 多槽
	//（r.modules.ResolveApproval）。
	approvals map[string]chan bool
	// controls 每个 run 的中断/取消信号（mock 路径）。
	controls map[string]chan string
	// modules SPI v2 执行面（dsh 网关模块等）；状态机由 ModuleRunner 驱动。
	modules *rt.ModuleRunner
	// dshGateway 长驻网关守护（control-plane 侧语义一致）。
	dshGateway *dsh.Gateway
	// kimiModule 长驻 kap-server 守护（`kimi web`；control-plane 侧语义一致）。
	kimiModule *kimiapp.Module
	// moduleRuns 使用模块执行面的 run（EngineSink.Run 回查用）。
	moduleRuns map[string]*domain.ExecutionRun
}

// writeDeadline 单帧写超时：控制平面读端停顿时必须快速失败（帧留在 pending
// 等重连重发），否则一次阻塞写会卡死互斥锁、连心跳都无法发出。
const writeDeadline = 10 * time.Second

// 重连退避；包级变量供测试覆写。
var (
	reconnectBaseBackoff = time.Second
	reconnectMaxBackoff  = 15 * time.Second
)

func main() {
	gateway := envOr("RUNNER_GATEWAY", "ws://localhost:8080/runner/v1/connect")
	runnerID := envOr("RUNNER_ID", "runner_local_01")
	token := os.Getenv("RUNNER_TOKEN")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	header := map[string][]string{}
	if token != "" {
		header["Authorization"] = []string{"Bearer " + token}
	}
	r := &runner{
		id:         runnerID,
		pending:    make(map[int64][]byte),
		approvals:  make(map[string]chan bool),
		controls:   make(map[string]chan string),
		moduleRuns: make(map[string]*domain.ExecutionRun),
	}
	r.modules = rt.NewModuleRunner(&moduleEngine{r: r})
	newDshGateway(r)
	newKimiModule(r)
	defer func() {
		if r.kimiModule != nil {
			r.kimiModule.Close()
		}
		if r.dshGateway != nil {
			r.dshGateway.Close()
		}
	}()

	runLoop(ctx, r, gateway, header)
}

// runLoop 常驻连接循环：断线（含网关缓冲满主动重置）后指数退避重连，
// 未 ACK 事件（含终态帧）在每次重连后有序重发——终态必达靠这条路径兜底，
// 否则 run 会悬置到 lease 过期被误判 lost。
func runLoop(ctx context.Context, r *runner, gateway string, header map[string][]string) {
	backoff := reconnectBaseBackoff
	for ctx.Err() == nil {
		dialed, err := connectOnce(ctx, r, gateway, header)
		if dialed {
			backoff = reconnectBaseBackoff
		}
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		pendingCount := len(r.pending)
		r.mu.Unlock()
		log.Printf("runnerd: 连接断开（%v），%s 后重连（未 ACK 帧 %d）", err, backoff, pendingCount)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, reconnectMaxBackoff)
	}
}

// connectOnce 建立一次连接：hello → 有序重发未 ACK 帧 → 心跳 + 读循环，
// 直到连接断开或进程退出。dialed 表示 TCP/WSS 是否建立成功（供退避重置）。
func connectOnce(ctx context.Context, r *runner, gateway string, header map[string][]string) (dialed bool, err error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, gateway, header)
	if err != nil {
		return false, err
	}
	dialed = true
	defer conn.Close()
	r.setConn(conn)

	r.hello()
	log.Printf("runnerd %s 已连接 %s", r.id, gateway)
	if err := r.resendPending(); err != nil {
		return dialed, err
	}
	go r.heartbeatLoop(ctx, conn)
	return dialed, r.readLoop(ctx, conn)
}

func (r *runner) setConn(conn *websocket.Conn) {
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
}

func (r *runner) currentConn() *websocket.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (r *runner) hello() {
	adapters := []map[string]any{{"adapter_id": "mock", "adapter_version": "1.0.0", "schema_digest": "sha256:mock"}}
	if r.dshGateway != nil {
		adapters = append(adapters, map[string]any{"adapter_id": "dsh", "adapter_version": "2.0.0", "schema_digest": "sha256:dsh-web-gateway-v1"})
	}
	if r.kimiModule != nil {
		adapters = append(adapters, map[string]any{"adapter_id": "kimi-appserver", "adapter_version": "1.0.0", "schema_digest": "sha256:kimi-kap-server-v2"})
	}
	payload, _ := json.Marshal(map[string]any{
		"protocol_versions": []int{1},
		"runner_version":    "0.3.0",
		"os":                runtime.GOOS,
		"arch":              runtime.GOARCH,
		"slots":             2,
		"adapters":          adapters,
	})
	r.send(envelope{
		V: 1, MessageID: "msg_hello", Kind: "request", Method: "runner.hello",
		RunnerID: r.id, SentAt: time.Now().UTC(), Payload: payload,
	})
}

func (r *runner) heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 连接已换代：心跳随旧连接终止，避免重连后 goroutine 累积。
			if r.currentConn() != conn {
				return
			}
			r.send(envelope{
				V: 1, MessageID: newMsgID(), Kind: "event", Method: "heartbeat",
				RunnerID: r.id, SentAt: time.Now().UTC(),
			})
		}
	}
}

func (r *runner) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		r.handle(env)
	}
}

func (r *runner) handle(env envelope) {
	switch env.Method {
	case "server.welcome":
		log.Printf("runnerd: 协议协商完成")
	case "run.offer":
		r.handleOffer(env)
	case "run.command":
		r.handleCommand(env)
	case "ack":
		var p struct {
			ContiguousSeq int64 `json:"contiguous_seq"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		r.mu.Lock()
		for seq := range r.pending {
			if seq <= p.ContiguousSeq {
				delete(r.pending, seq)
			}
		}
		r.mu.Unlock()
	}
}

func (r *runner) handleOffer(env envelope) {
	var p struct {
		LeaseID      string  `json:"lease_id"`
		FencingToken int64   `json:"fencing_token"`
		RunSpec      runSpec `json:"run_spec"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	// 容量与能力校验后接受；接受后才准备 Workspace。
	accept, _ := json.Marshal(map[string]any{"accepted": true})
	r.send(envelope{
		V: 1, MessageID: newMsgID(), Kind: "response", Method: "run.accept",
		RunnerID: r.id, RunID: p.RunSpec.RunID, ReplyTo: env.MessageID,
		SentAt: time.Now().UTC(), Payload: accept,
	})

	r.mu.Lock()
	r.approvals[p.RunSpec.RunID] = make(chan bool, 1)
	r.controls[p.RunSpec.RunID] = make(chan string, 2)
	useModule := (p.RunSpec.AdapterID == "dsh" && r.dshGateway != nil) ||
		(p.RunSpec.AdapterID == "kimi-appserver" && r.kimiModule != nil)
	r.mu.Unlock()

	if useModule {
		r.runModule(p.RunSpec)
		return
	}
	go r.simulate(p.RunSpec)
}

// runModule 用 SPI v2 模块执行（dsh 网关）：状态机由 ModuleRunner 驱动，
// 事件经 moduleEngine 桥接为 run.event 上报。
func (r *runner) runModule(spec runSpec) {
	run := &domain.ExecutionRun{
		ID: spec.RunID, AdapterID: spec.AdapterID, Status: domain.RunQueued, Version: 1,
		Input: spec.Input, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	r.mu.Lock()
	r.moduleRuns[spec.RunID] = run
	r.mu.Unlock()
	if err := r.modules.Dispatch(context.Background(), run); err != nil {
		r.emitEvent(spec.RunID, "run.status_changed", map[string]any{
			"status": "failed", "code": "dispatch_failed", "message": err.Error(),
		})
	}
}

func (r *runner) handleCommand(env envelope) {
	var p struct {
		Command string         `json:"command"`
		Body    map[string]any `json:"body"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	r.mu.Lock()
	isModule := r.moduleRuns[env.RunID] != nil
	r.mu.Unlock()

	// 模块执行面：控制命令直达 ModuleRunner（含 steering input 分支）。
	if isModule {
		switch p.Command {
		case "approval.resolve":
			approvalID, _ := p.Body["approval_id"].(string)
			approved, _ := p.Body["approved"].(bool)
			if err := r.modules.ResolveApproval(env.RunID, approvalID, approved); err != nil {
				log.Printf("runnerd: 审批决定投递失败（%s/%s）: %v", env.RunID, approvalID, err)
			}
		case "interrupt", "cancel":
			terminal := domain.RunInterrupted
			if p.Command == "cancel" {
				terminal = domain.RunCancelled
			}
			r.modules.Control(env.RunID, terminal)
		case "input":
			instruction, _ := p.Body["instruction"].(string)
			if instruction == "" {
				return
			}
			if err := r.modules.ForwardInput(context.Background(), env.RunID, instruction); err != nil {
				log.Printf("runnerd: steering 投递失败（%s）: %v", env.RunID, err)
			}
		}
		return
	}

	switch p.Command {
	case "approval.resolve":
		r.mu.Lock()
		ch := r.approvals[env.RunID]
		r.mu.Unlock()
		if ch != nil {
			approved, _ := p.Body["approved"].(bool)
			select {
			case ch <- approved:
			default:
			}
		}
	case "interrupt", "cancel":
		r.mu.Lock()
		ch := r.controls[env.RunID]
		r.mu.Unlock()
		if ch != nil {
			select {
			case ch <- p.Command:
			default:
			}
		}
	case "input":
		// legacy/mock 执行面没有 steering 消费者（simulate 不消费中途指令）：
		// 显式记 Warn 日志，不静默丢弃——控制平面 ForwardInput 命中活动租约
		// 却被吞掉时，调用方可从 runner 日志定位。
		instruction, _ := p.Body["instruction"].(string)
		log.Printf("runnerd: WARN run %s 收到 steering input 但 legacy/mock 执行面无消费者，已丢弃（instruction=%q）",
			env.RunID, instruction)
	}
}

// emitEvent 上报一条 canonical 事件（runner_seq 递增，等待 ACK）。
// 帧先入 pending 再尝试写入：写失败只代表本次连接不可用，帧保留在 pending
// 由重连重发（至少一次投递）。返回写错误供桥接层上报/记日志。
// seq 必须直接使用 nextSeq 的返回值：若自增后回读 r.seq，两个并发 emit 交叉时
// 会拿到同一个值，导致 pending[seq] 覆盖丢帧（断线重连后永不重发、runner_seq 缺口）。
func (r *runner) emitEvent(runID, kind string, data map[string]any) error {
	seq := r.nextSeq()
	payload, _ := json.Marshal(map[string]any{
		"runner_seq": seq,
		"event":      map[string]any{"kind": kind, "data": data},
	})
	env := envelope{
		V: 1, MessageID: newMsgID(), Kind: "event", Method: "run.event",
		RunnerID: r.id, RunID: runID, SentAt: time.Now().UTC(), Payload: payload,
	}
	b, _ := json.Marshal(env)
	r.mu.Lock()
	r.pending[seq] = b
	r.mu.Unlock()
	return r.writeLocked(b)
}

// resendPending 重连后按 seq 有序重发未 ACK 帧。乱序重发会被状态机拒收
// （如 succeeded 先于 running 到达），有序 + 服务端按 (run_id, runner_seq)
// 去重 = 至少一次且语义等价的投递。
func (r *runner) resendPending() error {
	r.mu.Lock()
	seqs := make([]int64, 0, len(r.pending))
	for seq := range r.pending {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	frames := make([][]byte, 0, len(seqs))
	for _, seq := range seqs {
		frames = append(frames, r.pending[seq])
	}
	r.mu.Unlock()
	for _, b := range frames {
		if err := r.writeLocked(b); err != nil {
			return err
		}
	}
	return nil
}

// moduleEngine 把 ModuleRunner 的引擎调用桥接为 WSS canonical 事件上报；
// 控制平面 ingress 再映射回服务端引擎（含 approval_id / usage 透传）。
type moduleEngine struct {
	r *runner

	apprSeq atomic.Int64
}

var _ rt.EngineSink = (*moduleEngine)(nil)

// 桥接层的 Record* 在发送失败时返回 error（帧仍保留在 pending 等重发）：
// 恒返 nil 会让 module 层（ModuleRunner.status）无从记日志或兜底，
// 与「任何 Outcome 必落终态」的进程内承诺不等价。
func (e *moduleEngine) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	merged := map[string]any{"status": string(to)}
	for k, v := range data {
		merged[k] = v
	}
	if err := e.r.emitEvent(runID, "run.status_changed", merged); err != nil {
		return fmt.Errorf("上报状态帧失败（pending 待重发）: %w", err)
	}
	return nil
}

func (e *moduleEngine) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	return e.r.emitEvent(runID, "run.progress", map[string]any{"progress": progress})
}

func (e *moduleEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	return e.r.emitEvent(runID, evType, data)
}

// RecordRunSessionUpdate 序列化完整 SessionUpdate（含 Clear 墓碑与 adapter 私有
// Params）：只发 ref/display_id 会让控制面的锚点永不清理、resume 参数跨进程丢失。
func (e *moduleEngine) RecordRunSessionUpdate(ctx context.Context, runID string, update rt.SessionUpdate) error {
	data := map[string]any{"session_ref": update.Ref}
	if update.DisplayID != "" {
		data["display_id"] = update.DisplayID
	}
	if update.Clear {
		data["clear"] = true
		if update.ClearReason != "" {
			data["clear_reason"] = update.ClearReason
		}
	}
	if len(update.Params) > 0 {
		data["params"] = update.Params
	}
	return e.r.emitEvent(runID, "run.session", data)
}

func (e *moduleEngine) RecordRunUsage(ctx context.Context, runID string, usage rt.Usage) error {
	return e.r.emitEvent(runID, "usage.updated", map[string]any{
		"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens,
		"cached_tokens": usage.CachedTokens, "basis": string(usage.Basis),
	})
}

func (e *moduleEngine) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	id := fmt.Sprintf("apr_local_%s_%d", runID, e.apprSeq.Add(1))
	if err := e.r.emitEvent(runID, "approval.requested", map[string]any{
		"kind": kind, "risk": risk, "summary": summary, "approval_id": id,
	}); err != nil {
		// 帧已入 pending，重连重发后审批仍会到达控制面；这里只记日志，
		// 照常返回审批对象，避免 adapter 因瞬时断线拿不到审批 ID 而中断。
		log.Printf("runnerd: run %s 审批帧发送失败（pending 待重发）: %v", runID, err)
	}
	return &domain.ApprovalRequest{
		ID: id, RunID: runID, Kind: kind, Risk: risk, Summary: summary,
		Status: domain.ApprovalPending,
	}, nil
}

func (e *moduleEngine) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	e.r.mu.Lock()
	run := e.r.moduleRuns[id]
	e.r.mu.Unlock()
	if run == nil {
		return nil, fmt.Errorf("run %s 不在本 runner", id)
	}
	return run, nil
}

// newDshGateway 构造 dsh 网关执行面；默认与控制平面同端口，健康检查会
// 直接复用已运行的网关实例（不重复拉起）。
func newDshGateway(r *runner) {
	workbenchRoot, _ := os.Getwd()
	repo := resolveDshRepo(workbenchRoot)
	gw := dsh.NewGateway(dsh.GatewayConfig{
		BaseURL:       envOr("DSH_GATEWAY_URL", ""),
		Port:          atoiEnv("DSH_GATEWAY_PORT", 3090),
		RepoDir:       repo,
		WorkspaceRoot: envOr("WORKSPACE_ROOT", "."),
		Model:         envOr("DSH_MODEL", "deepseek-v4-flash"),
	})
	r.dshGateway = gw
	r.modules.Register("dsh", gw)
	log.Printf("runnerd: dsh 网关模块已注册（repo=%q）", repo)
}

// newKimiModule 构造 kimiapp 网关执行面（本机 kimi CLI 存在或显式直连 URL
// 时启用）；env 语义与 control-plane 的 ATW_KIMIAPP_* 一致。
func newKimiModule(r *runner) {
	bin := envOr("ATW_KIMIAPP_BIN", envOr("ATW_KIMI_BIN", "kimi"))
	if envOr("ATW_KIMIAPP_URL", "") == "" {
		if _, err := exec.LookPath(bin); err != nil {
			return
		}
	}
	m := kimiapp.New(kimiapp.Config{
		BaseURL:       envOr("ATW_KIMIAPP_URL", ""),
		Token:         envOr("ATW_KIMIAPP_TOKEN", ""),
		Port:          atoiEnv("ATW_KIMIAPP_PORT", 0),
		KimiBin:       bin,
		Home:          envOr("ATW_KIMIAPP_HOME", filepath.Join(".atw-data", "kimi-home")),
		WorkspaceRoot: envOr("WORKSPACE_ROOT", "."),
		Model:         envOr("ATW_KIMIAPP_MODEL", ""),
	})
	r.kimiModule = m
	r.modules.Register("kimi-appserver", m)
	log.Printf("runnerd: kimiapp 网关模块已注册（bin=%q）", bin)
}

func atoiEnv(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// resolveDshRepo 定位 deepseek-harness 仓库根：DSH_REPO 显式指定优先，
// 其次探测常见相邻目录（与控制平面 resolveDshRepo 同规则）。
func resolveDshRepo(workbenchRoot string) string {
	if v := os.Getenv("DSH_REPO"); v != "" {
		return v
	}
	for _, cand := range []string{
		filepath.Join(workbenchRoot, "..", "deepseek-harness"),
		filepath.Join(workbenchRoot, "..", "..", "deepseek-harness"),
		filepath.Join(workbenchRoot, "runtimes", "dsh-harness"),
	} {
		if _, err := os.Stat(filepath.Join(cand, "apps", "cli", "src", "bin.ts")); err == nil {
			return cand
		}
	}
	return ""
}

// simulate 执行 mock 流程：事件按 runner_seq 递增上报，等待服务端 ACK。
func (r *runner) simulate(spec runSpec) {
	runID := spec.RunID
	instruction, _ := spec.Input["instruction"].(string)
	step := 350 * time.Millisecond

	emit := func(kind string, data map[string]any) bool {
		return r.emitEvent(runID, kind, data) == nil
	}
	status := func(s string) bool {
		return emit("run.status_changed", map[string]any{"status": s})
	}
	// 控制信号检查：interrupt/cancel 优先于继续执行。
	checkControl := func() bool {
		r.mu.Lock()
		ch := r.controls[runID]
		r.mu.Unlock()
		select {
		case cmd := <-ch:
			if cmd == "cancel" {
				status("cancelled")
			} else {
				status("interrupted")
			}
			return false
		default:
			return true
		}
	}

	if !status("starting") {
		return
	}
	time.Sleep(step)
	if !checkControl() || !status("running") {
		return
	}
	emit("message.delta", map[string]any{"text": "runner 正在执行任务"})
	emit("run.progress", map[string]any{"progress": 0.4})
	time.Sleep(step)
	if !checkControl() {
		return
	}
	emit("run.progress", map[string]any{"progress": 0.7})

	if strings.Contains(instruction, "approval") || strings.Contains(instruction, "审批") {
		emit("approval.requested", map[string]any{
			"kind": "shell", "risk": "high", "summary": "Runner 请求执行高风险命令",
		})
		r.mu.Lock()
		ch := r.approvals[runID]
		r.mu.Unlock()
		approved := <-ch
		if !approved {
			status("cancelled")
			return
		}
		if !checkControl() {
			return
		}
	}

	time.Sleep(step)
	if !checkControl() {
		return
	}
	emit("message.completed", map[string]any{"text": "runner 执行完成"})
	emit("artifact.manifest", map[string]any{
		"logical_path": "output/runner-result.md", "mime": "text/markdown",
		"size": 1024, "sha256": strings.Repeat("b", 64),
	})
	if !status("succeeding") {
		return
	}
	time.Sleep(step / 2)
	status("succeeded")
}

func (r *runner) nextSeq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return r.seq
}

func (r *runner) send(env envelope) {
	b, _ := json.Marshal(env)
	_ = r.writeLocked(b)
}

// writeLocked 串行化 WebSocket 写入（gorilla 不支持并发写）；带写超时，
// 半死连接（对端不再读）必须快速失败让帧留在 pending 等重连重发。
func (r *runner) writeLocked(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return fmt.Errorf("runner 尚未连接控制平面")
	}
	_ = r.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	return r.conn.WriteMessage(websocket.TextMessage, b)
}

func newMsgID() string {
	return fmt.Sprintf("msg_%d", time.Now().UnixNano())
}
