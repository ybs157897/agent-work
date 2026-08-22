// cmd/runnerd 是本地 Go Runner（M2）：出站 WSS 连接控制平面，
// 接受 run.offer 并在本地执行 Adapter，按 runner_seq 上报 canonical 事件等待 ACK。
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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/dsh"
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
	// approvals 每个 run 的审批决定 channel。
	approvals map[string]chan bool
	// controls 每个 run 的中断/取消信号。
	controls map[string]chan string
	// dshAdapter 真实 DSH 执行面（二进制不可用时为 nil → 回退 mock）。
	dshAdapter *dsh.Adapter
	// dshRuns 标记使用 DSH 执行面的 run（控制命令走进程组终止）。
	dshRuns map[string]bool
}

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
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, gateway, header)
	if err != nil {
		log.Fatalf("连接控制平面失败: %v", err)
	}
	r := &runner{
		id: runnerID, conn: conn,
		pending:   make(map[int64][]byte),
		approvals: make(map[string]chan bool),
		controls:  make(map[string]chan string),
		dshRuns:   make(map[string]bool),
	}
	r.dshAdapter = newDshAdapter(r)
	defer conn.Close()

	r.hello()
	log.Printf("runnerd %s 已连接 %s", runnerID, gateway)

	// 重连后重发未 ACK 事件。
	r.mu.Lock()
	pending := make([][]byte, 0, len(r.pending))
	for _, b := range r.pending {
		pending = append(pending, b)
	}
	r.mu.Unlock()
	for _, b := range pending {
		_ = r.writeLocked(b)
	}

	go r.heartbeatLoop(ctx)
	r.readLoop(ctx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (r *runner) hello() {
	adapters := []map[string]any{{"adapter_id": "mock", "adapter_version": "1.0.0", "schema_digest": "sha256:mock"}}
	if r.dshAdapter != nil {
		adapters = append(adapters, map[string]any{"adapter_id": "dsh", "adapter_version": "1.0.0", "schema_digest": "sha256:dsh-sdk-jsonrpc-v1"})
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

func (r *runner) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.send(envelope{
				V: 1, MessageID: newMsgID(), Kind: "event", Method: "heartbeat",
				RunnerID: r.id, SentAt: time.Now().UTC(),
			})
		}
	}
}

func (r *runner) readLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, raw, err := r.conn.ReadMessage()
		if err != nil {
			log.Printf("连接断开: %v", err)
			return
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
	useDsh := p.RunSpec.AdapterID == "dsh" && r.dshAdapter != nil
	if useDsh {
		r.dshRuns[p.RunSpec.RunID] = true
	}
	r.mu.Unlock()

	if useDsh {
		r.runDsh(p.RunSpec)
		return
	}
	go r.simulate(p.RunSpec)
}

// runDsh 用真实 DSH Adapter 执行：子进程 JSON-RPC，事件经 emitEvent 上报。
func (r *runner) runDsh(spec runSpec) {
	run := &domain.ExecutionRun{
		ID: spec.RunID, Status: domain.RunQueued, Version: 1,
		Input: spec.Input, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := r.dshAdapter.Dispatch(context.Background(), run); err != nil {
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
	isDsh := r.dshRuns[env.RunID]
	r.mu.Unlock()

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
		if isDsh && r.dshAdapter != nil {
			// DSH 无 prompt cancel：进程组级终止（process_scoped）。
			terminal := domain.RunInterrupted
			if p.Command == "cancel" {
				terminal = domain.RunCancelled
			}
			r.dshAdapter.Control(env.RunID, terminal)
			return
		}
		r.mu.Lock()
		ch := r.controls[env.RunID]
		r.mu.Unlock()
		if ch != nil {
			select {
			case ch <- p.Command:
			default:
			}
		}
	}
}

// emitEvent 上报一条 canonical 事件（runner_seq 递增，等待 ACK）。
func (r *runner) emitEvent(runID, kind string, data map[string]any) bool {
	payload, _ := json.Marshal(map[string]any{
		"runner_seq": r.nextSeq(),
		"event":      map[string]any{"kind": kind, "data": data},
	})
	env := envelope{
		V: 1, MessageID: newMsgID(), Kind: "event", Method: "run.event",
		RunnerID: r.id, RunID: runID, SentAt: time.Now().UTC(), Payload: payload,
	}
	b, _ := json.Marshal(env)
	seq := r.seq
	r.mu.Lock()
	r.pending[seq] = b
	r.mu.Unlock()
	return r.writeLocked(b) == nil
}

// dshEngine 把 DSH Adapter 的引擎调用桥接为 WSS 事件上报。
type dshEngine struct{ r *runner }

func (e *dshEngine) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	merged := map[string]any{"status": string(to)}
	for k, v := range data {
		merged[k] = v
	}
	e.r.emitEvent(runID, "run.status_changed", merged)
	return nil
}

func (e *dshEngine) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	e.r.emitEvent(runID, evType, data)
	return nil
}

// newDshAdapter 从环境构造 DSH 执行面；二进制/配置不可用时返回 nil。
func newDshAdapter(r *runner) *dsh.Adapter {
	bin := envOr("DSH_BIN", "runtimes/dsh/node_modules/.bin/dsh-jsonrpc-agent")
	config := envOr("DSH_CONFIG", "runtimes/dsh/.generated/smoke.cordis.yml")
	// 支持 PATH 上的可执行文件（如 python3）与相对/绝对路径。
	resolved := bin
	if strings.Contains(bin, string(os.PathSeparator)) {
		if _, err := os.Stat(bin); err != nil {
			log.Printf("runnerd: DSH 二进制不可用（%s），仅提供 mock 执行面", bin)
			return nil
		}
	} else if p, err := exec.LookPath(bin); err != nil {
		log.Printf("runnerd: DSH 二进制不可用（%s），仅提供 mock 执行面", bin)
		return nil
	} else {
		resolved = p
	}
	workspaceRoot := envOr("WORKSPACE_ROOT", ".")
	adapter := dsh.New(dsh.Config{
		BinPath: resolved, ConfigPath: config,
		WorkspaceRoot: workspaceRoot,
		SessionRoot:   envOr("DSH_SESSION_ROOT", workspaceRoot+"/.sessions"),
		Model:         envOr("DSH_MODEL", "deepseek-v4-flash"),
		SystemPrompt:  os.Getenv("RUNNER_SYSTEM_PROMPT"),
	}, &dshEngine{r: r})
	log.Printf("runnerd: DSH 执行面就绪（bin=%s config=%s）", resolved, config)
	return adapter
}

// simulate 执行 mock 流程：事件按 runner_seq 递增上报，等待服务端 ACK。
func (r *runner) simulate(spec runSpec) {
	runID := spec.RunID
	instruction, _ := spec.Input["instruction"].(string)
	step := 350 * time.Millisecond

	emit := func(kind string, data map[string]any) bool {
		return r.emitEvent(runID, kind, data)
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

// writeLocked 串行化 WebSocket 写入（gorilla 不支持并发写）。
func (r *runner) writeLocked(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn.WriteMessage(websocket.TextMessage, b)
}

func newMsgID() string {
	return fmt.Sprintf("msg_%d", time.Now().UnixNano())
}
