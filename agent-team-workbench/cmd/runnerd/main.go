// cmd/runnerd 是远程 Host 上的 Go Runner（Runner protocol v2，RFC §8）：
// 出站 WSS 连接 control-plane 的 /runner/v2/connect（enrollment 凭据
// atw_host_<host_id>_<secret>），hello 上报 host_id/connection_epoch/mount
// 广告（hostregistry）；run.offer 先本地 Resolve（alias/generation/identity/
// ref/digest/并发租约）通过才 run.accept，否则 run.reject(reason=workspace|
// capacity)。事件创建即分配稳定 event_id/producer_seq 并入 pending，
// 重连原样重发（事件身份不变），按 (run, lease, producer_seq) 消化 ACK。
// 执行面：adapter_id=dsh → 真实 DeepSeek Harness SDK 子进程；kimi-appserver →
// kap-server；其他 → mock 模拟。
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os"
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
	"github.com/oklog/ulid/v2"
	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
	rt "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/dsh"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/kimiapp"
)

// 协议常量（与 contracts/runner/v2 及 internal/runnergateway 对齐）。
const (
	protocolV2               = 2
	defaultControlPlaneWS    = "ws://127.0.0.1:8080/runner/v2/connect"
	defaultHeartbeatInterval = 15 * time.Second
)

// writeDeadline 单帧写超时：控制平面读端停顿时必须快速失败（帧留在 pending
// 等重连重发），否则一次阻塞写会卡死互斥锁、连心跳都无法发出。
const writeDeadline = 10 * time.Second

type envelope struct {
	V               int             `json:"v"`
	MessageID       string          `json:"message_id"`
	Kind            string          `json:"kind"` // request | response | event | ack
	Method          string          `json:"method"`
	RunnerID        string          `json:"runner_id,omitempty"`
	RunID           string          `json:"run_id,omitempty"`
	ReplyTo         string          `json:"reply_to,omitempty"`
	ConnectionEpoch string          `json:"connection_epoch,omitempty"` // heartbeat envelope 级字段
	SentAt          time.Time       `json:"sent_at"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// ── 线形态（contracts/runner/v2）─────────────────────────────────────

type runSpec struct {
	RunID           string         `json:"run_id"`
	AdapterID       string         `json:"adapter_id"`
	ContextSnapshot wireSnapshot   `json:"context_snapshot"`
	Input           map[string]any `json:"input"`
	Policy          map[string]any `json:"policy"`
}

type offerPayload struct {
	LeaseID      string  `json:"lease_id"`
	FencingToken int64   `json:"fencing_token"`
	RunSpec      runSpec `json:"run_spec"`
}

// wireSnapshot 是 offer 携带的执行上下文子集（不含宿主绝对路径）；
// 转换为领域快照后交给 hostregistry.Resolve。
type wireSnapshot struct {
	ID                  string         `json:"id"`
	SchemaVersion       string         `json:"schema_version"`
	ExecutionHostID     string         `json:"execution_host_id"`
	WorkspaceLocationID string         `json:"workspace_location_id"`
	WorkspaceAlias      string         `json:"workspace_alias"`
	MountGeneration     string         `json:"mount_generation"`
	RepositoryIdentity  string         `json:"repository_identity"`
	RefKind             domain.RefKind `json:"ref_kind"`
	BranchName          string         `json:"branch_name,omitempty"`
	CheckoutRef         *string        `json:"checkout_ref"`
	WorktreeRef         *string        `json:"worktree_ref"`
	BaseRevision        string         `json:"base_revision,omitempty"`
	LocationVersion     int            `json:"location_version"`
	ContextGeneration   int            `json:"context_generation"`
	SnapshotDigest      string         `json:"snapshot_digest"`
}

// toDomain 还原为领域快照。schema 非 execution-context/v1 拒绝执行。
func (w wireSnapshot) toDomain(runID string) (domain.ExecutionContextSnapshot, error) {
	if w.SchemaVersion != domain.SnapshotSchemaV1 {
		return domain.ExecutionContextSnapshot{}, fmt.Errorf("context_snapshot.schema_version %q 不受支持", w.SchemaVersion)
	}
	s := domain.ExecutionContextSnapshot{
		ID:                  w.ID,
		RunID:               runID,
		SchemaVersion:       w.SchemaVersion,
		WorkspaceLocationID: w.WorkspaceLocationID,
		LocationVersion:     w.LocationVersion,
		MountGeneration:     w.MountGeneration,
		ExecutionHostID:     w.ExecutionHostID,
		MountAlias:          w.WorkspaceAlias,
		RepositoryIdentity:  w.RepositoryIdentity,
		RefKind:             w.RefKind,
		BranchName:          w.BranchName,
		BaseRevision:        w.BaseRevision,
		ContextGeneration:   w.ContextGeneration,
		SnapshotDigest:      w.SnapshotDigest,
	}
	if w.CheckoutRef != nil {
		s.CheckoutRef = *w.CheckoutRef
	}
	if w.WorktreeRef != nil {
		s.WorktreeRef = *w.WorktreeRef
	}
	return s, nil
}

type acceptPayload struct {
	RunID           string `json:"run_id"`
	LeaseID         string `json:"lease_id"`
	RunnerID        string `json:"runner_id"`
	ConnectionEpoch string `json:"connection_epoch"`
	FencingToken    int64  `json:"fencing_token"`
	SnapshotDigest  string `json:"snapshot_digest"`
}

type rejectPayload struct {
	RunID           string `json:"run_id"`
	LeaseID         string `json:"lease_id"`
	RunnerID        string `json:"runner_id"`
	ConnectionEpoch string `json:"connection_epoch"`
	FencingToken    int64  `json:"fencing_token"`
	Reason          string `json:"reason"`
	Detail          string `json:"detail,omitempty"`
}

type welcomePayload struct {
	SelectedVersion          int `json:"selected_version"`
	HeartbeatIntervalSeconds int `json:"heartbeat_interval_seconds"`
	MaxFrameBytes            int `json:"max_frame_bytes,omitempty"`
}

type commandPayload struct {
	RunID           string         `json:"run_id"`
	LeaseID         string         `json:"lease_id"`
	RunnerID        string         `json:"runner_id"`
	ConnectionEpoch string         `json:"connection_epoch"`
	FencingToken    int64          `json:"fencing_token"`
	CommandID       string         `json:"command_id"`
	Command         string         `json:"command"`
	Body            map[string]any `json:"body,omitempty"`
}

type ackPayload struct {
	RunID            string `json:"run_id"`
	LeaseID          string `json:"lease_id"`
	RunnerID         string `json:"runner_id"`
	FencingToken     int64  `json:"fencing_token"`
	AckedProducerSeq int64  `json:"acked_producer_seq"`
	EventID          string `json:"event_id"`
}

// pendingKey 是事件身份：dedup 与 ACK 消化都以 (run, lease, producer_seq)
// 为粒度——一个 Run 的 ACK 不清另一个 Run 的 pending。
type pendingKey struct {
	RunID   string
	LeaseID string
	Seq     int64
}

// pendingEvent 是未 ACK 事件的稳定身份载荷：重连原样重发（新 envelope/新
// connection_epoch，payload 中的 event_id/producer_seq/run/lease/runner 不变）。
type pendingEvent struct {
	Key     pendingKey
	Fencing int64
	EventID string
	Kind    string
	Data    map[string]any
}

// leaseInfo 是 Run 的租约 framing 真相（accept 后不变）；终态后仍保留，
// 供终态 status 之后补发的 session/usage 事件使用。
type leaseInfo struct {
	LeaseID      string
	FencingToken int64
	Snapshot     domain.ExecutionContextSnapshot
	Resolved     domain.ResolvedExecutionContext
}

// runnerRun 是一个活动 Run 的本地状态；registry checkout 租约的 release 在
// Run 终态时调用（同 checkout 单活跃 Run 的 Runner 侧闸门）。
type runnerRun struct {
	release func()
}

type runner struct {
	id     string
	hostID string
	// bootID 只在进程启动时生成一次；网络重连保持同一 boot，Gateway 据此区分
	// 可恢复的 transport reconnect 与已丢内存执行状态的进程重启。
	bootID string
	// epoch 是当前 WSS 连接的 connection_epoch：每次新连接换代（RFC §4.2），
	// 由 connectOnce 在 hello 前写入；心跳/执行 goroutine 并发读，须经
	// currentEpoch()。
	epoch    string
	slots    int
	registry *hostregistry.Registry

	conn *websocket.Conn
	mu   sync.Mutex
	// seqs 每个 (run) 的 producer_seq 游标（单 Run 内从 1 单调）。
	seqs map[string]int64
	// pending 未 ACK 事件，按事件身份存取；重连后有序重发。
	pending map[pendingKey]*pendingEvent
	// runs 活动 Run（容量口径）；终态时删除并释放 checkout 租约。
	runs map[string]*runnerRun
	// leases 租约 framing（含 snapshot/resolved），终态后保留。
	leases map[string]*leaseInfo
	// terminalPending 标记已产生终态但仍有待 ACK 事件的 Run。只有该 Run 的
	// pending 清空后才释放 leases/seqs，既保证断线重发终态，又不让长驻 daemon
	// 无限积累已完成 Run 的上下文。
	terminalPending map[string]bool

	heartbeatInterval time.Duration

	// approvals 每个 run 的审批决定 channel（mock 路径）。
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

// 重连退避；包级变量供测试覆写。
var (
	reconnectBaseBackoff = time.Second
	reconnectMaxBackoff  = 15 * time.Second
)

func main() {
	gatewayURL := envOr("CONTROL_PLANE_WS", defaultControlPlaneWS)
	runnerID := envOr("RUNNER_ID", "runner_local_01")
	credential := os.Getenv("RUNNER_CREDENTIAL")
	if credential == "" {
		log.Fatal("runnerd: RUNNER_CREDENTIAL 必须设置（atw_host_<host_id>_<secret>）")
	}
	hostID, _, ok := parseCredentialHost(credential)
	if !ok {
		log.Fatal("runnerd: RUNNER_CREDENTIAL 格式非法（期望 atw_host_<host_id>_<secret>）")
	}
	registry, err := loadRegistry(envOr("HOST_REGISTRY", ""))
	if err != nil {
		log.Fatalf("runnerd: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	r := &runner{
		id: runnerID, hostID: hostID, bootID: newBootID(), slots: atoiEnv("RUNNER_SLOTS", 2),
		registry:          registry,
		heartbeatInterval: defaultHeartbeatInterval,
		seqs:              make(map[string]int64),
		pending:           make(map[pendingKey]*pendingEvent),
		runs:              make(map[string]*runnerRun),
		leases:            make(map[string]*leaseInfo),
		terminalPending:   make(map[string]bool),
		approvals:         make(map[string]chan bool),
		controls:          make(map[string]chan string),
		moduleRuns:        make(map[string]*domain.ExecutionRun),
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

	header := map[string][]string{"Authorization": {"Bearer " + credential}}
	runLoop(ctx, r, gatewayURL, header)
}

// loadRegistry 加载 HOST_REGISTRY；未配置时空 registry（一切 offer 以
// workspace_mount_not_advertised 拒绝——fail closed）。
func loadRegistry(path string) (*hostregistry.Registry, error) {
	if path == "" {
		log.Printf("runnerd: 未配置 HOST_REGISTRY，本 Runner 无 mount 广告（offer 将被拒绝）")
		return hostregistry.New(), nil
	}
	return hostregistry.Load(path)
}

// parseCredentialHost 从 enrollment 凭据 `atw_host_<host_id>_<secret>` 解析
// host_id（格式与 internal/runnergateway.ParseHostCredential 同口径：
// host_id 段不含 '_'）。
func parseCredentialHost(token string) (hostID string, secret string, ok bool) {
	const prefix = "atw_host_"
	if !strings.HasPrefix(token, prefix) {
		return "", "", false
	}
	rest := token[len(prefix):]
	const hostPrefix = "host_"
	if !strings.HasPrefix(rest, hostPrefix) {
		return "", "", false
	}
	rest = rest[len(hostPrefix):]
	i := strings.Index(rest, "_")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return hostPrefix + rest[:i], rest[i+1:], true
}

// runLoop 常驻连接循环：断线（含网关缓冲满主动重置）后指数退避重连，
// 未 ACK 事件（含终态帧）在每次重连后原样重发——终态必达靠这条路径兜底，
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

// connectOnce 建立一次连接：换新 connection_epoch（RFC §4.2：每次新连接
// 换代）→ hello → 原样重发未 ACK 帧（payload 事件身份不变，仅 envelope 以
// 新 epoch 重封）→ 心跳 + 读循环，直到连接断开或进程退出。
func connectOnce(ctx context.Context, r *runner, gateway string, header map[string][]string) (dialed bool, err error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, gateway, header)
	if err != nil {
		return false, err
	}
	dialed = true
	defer conn.Close()
	r.setConn(conn)
	r.setEpoch(newEpoch())

	r.hello()
	log.Printf("runnerd %s 已连接 %s（host=%s epoch=%s）", r.id, gateway, r.hostID, r.currentEpoch())
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

// setEpoch / currentEpoch 是 connection_epoch 的唯一读写口：epoch 由
// runLoop goroutine 在每次重连时换代，心跳/执行 goroutine 并发读。
func (r *runner) setEpoch(epoch string) {
	r.mu.Lock()
	r.epoch = epoch
	r.mu.Unlock()
}

func (r *runner) currentEpoch() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epoch
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// hello 上报 host 身份（凭据解析）、connection_epoch（每次进程启动换代）、
// mount 广告（hostregistry.Advertise，fail loud）与 adapter 能力。
func (r *runner) hello() {
	adapters := []map[string]any{{"adapter_id": "mock", "adapter_version": "1.0.0", "schema_digest": "sha256:mock"}}
	if r.dshGateway != nil {
		adapters = append(adapters, map[string]any{"adapter_id": "dsh", "adapter_version": "2.0.0", "schema_digest": "sha256:dsh-web-gateway-v1"})
	}
	if r.kimiModule != nil {
		adapters = append(adapters, map[string]any{"adapter_id": "kimi-appserver", "adapter_version": "1.0.0", "schema_digest": "sha256:kimi-kap-server-v2"})
	}
	payload, _ := json.Marshal(map[string]any{
		"host_id":           r.hostID,
		"runner_id":         r.id,
		"boot_id":           r.bootID,
		"connection_epoch":  r.currentEpoch(),
		"protocol_versions": []int{protocolV2},
		"runner_version":    "1.0.0",
		"os":                runtime.GOOS,
		"arch":              runtime.GOARCH,
		"slots":             r.slots,
		"adapters":          adapters,
		"mounts":            r.registry.Advertise(),
	})
	r.send(envelope{
		V: protocolV2, MessageID: newMsgID(), Kind: "request", Method: "runner.hello",
		RunnerID: r.id, SentAt: time.Now().UTC(), Payload: payload,
	})
}

func (r *runner) heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(r.heartbeatIntervalOf())
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
				V: protocolV2, MessageID: newMsgID(), Kind: "event", Method: "heartbeat",
				RunnerID: r.id, ConnectionEpoch: r.currentEpoch(),
				SentAt: time.Now().UTC(), Payload: []byte("{}"),
			})
		}
	}
}

func (r *runner) heartbeatIntervalOf() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.heartbeatInterval <= 0 {
		return defaultHeartbeatInterval
	}
	return r.heartbeatInterval
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
		if env.V != protocolV2 {
			log.Printf("runnerd: 忽略 v=%d 帧（method=%s）——仅支持 v2", env.V, env.Method)
			continue
		}
		r.handle(env)
	}
}

func (r *runner) handle(env envelope) {
	switch env.Method {
	case "server.welcome":
		var p welcomePayload
		_ = json.Unmarshal(env.Payload, &p)
		if p.SelectedVersion != protocolV2 {
			log.Printf("runnerd: WARN welcome selected_version=%d（期望 2）", p.SelectedVersion)
		}
		if p.HeartbeatIntervalSeconds > 0 {
			r.mu.Lock()
			r.heartbeatInterval = time.Duration(p.HeartbeatIntervalSeconds) * time.Second
			r.mu.Unlock()
		}
		log.Printf("runnerd: 协议协商完成（v2，heartbeat=%ds）", p.HeartbeatIntervalSeconds)
	case "run.offer":
		r.handleOffer(env)
	case "run.command":
		r.handleCommand(env)
	case "ack":
		r.handleAck(env)
	}
}

// handleOffer：容量 → hostregistry.Resolve（alias/generation/identity/ref/
// digest 逐项校验）→ 并发 checkout 租约；任一失败 run.reject（lease 释放与
// Run 落 failed 由控制面应用层单事务完成）；全部通过才 run.accept（带本地
// 重算的 digest），随后进入执行面。
func (r *runner) handleOffer(env envelope) {
	var p offerPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	reject := func(reason, detail string) {
		r.send(envelope{
			V: protocolV2, MessageID: newMsgID(), Kind: "response", Method: "run.reject",
			RunnerID: r.id, RunID: p.RunSpec.RunID, ReplyTo: env.MessageID,
			SentAt: time.Now().UTC(),
			Payload: mustJSON(rejectPayload{
				RunID: p.RunSpec.RunID, LeaseID: p.LeaseID, RunnerID: r.id,
				ConnectionEpoch: r.currentEpoch(), FencingToken: p.FencingToken,
				Reason: reason, Detail: detail,
			}),
		})
	}

	r.mu.Lock()
	active := len(r.runs)
	r.mu.Unlock()
	if active >= r.slots {
		reject("capacity", fmt.Sprintf("runner 容量已满（%d/%d）", active, r.slots))
		return
	}
	snap, err := p.RunSpec.ContextSnapshot.toDomain(p.RunSpec.RunID)
	if err != nil {
		reject("workspace", err.Error())
		return
	}
	resolved, err := r.registry.Resolve(&snap)
	if err != nil {
		reject("workspace", err.Error())
		return
	}
	release, err := r.registry.Acquire(&snap)
	if err != nil {
		// 同 checkout 已有活跃 Run：workspace 族拒绝（不同 worktree refs 可并行）。
		reject("workspace", err.Error())
		return
	}

	payload := mustJSON(acceptPayload{
		RunID: p.RunSpec.RunID, LeaseID: p.LeaseID, RunnerID: r.id,
		ConnectionEpoch: r.currentEpoch(), FencingToken: p.FencingToken,
		SnapshotDigest: snap.SnapshotDigest, // Resolve 已验证 == 本地重算值
	})
	r.send(envelope{
		V: protocolV2, MessageID: newMsgID(), Kind: "response", Method: "run.accept",
		RunnerID: r.id, RunID: p.RunSpec.RunID, ReplyTo: env.MessageID,
		SentAt: time.Now().UTC(), Payload: payload,
	})

	r.mu.Lock()
	r.runs[p.RunSpec.RunID] = &runnerRun{release: release}
	r.leases[p.RunSpec.RunID] = &leaseInfo{
		LeaseID: p.LeaseID, FencingToken: p.FencingToken,
		Snapshot: snap, Resolved: resolved,
	}
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

// finalize：Run 终态（status 帧已入 pending）后释放本地资源——registry
// checkout 租约、控制 channel、模块回查表；租约 framing 保留在 r.leases
// 供终态后补发的 session/usage 事件使用。
func (r *runner) finalize(runID string) {
	r.mu.Lock()
	rr := r.runs[runID]
	delete(r.runs, runID)
	delete(r.approvals, runID)
	delete(r.controls, runID)
	delete(r.moduleRuns, runID)
	r.mu.Unlock()
	if rr != nil {
		rr.release()
	}
}

func (r *runner) handleCommand(env envelope) {
	var p commandPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	if !r.commandCurrent(p) {
		log.Printf("runnerd: 丢弃过期 run.command（run=%s lease=%s epoch=%s）", p.RunID, p.LeaseID, p.ConnectionEpoch)
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

// commandCurrent 在 Runner 侧再次核验 server command 的 lease/fence/epoch。
// Gateway 已做一层 fencing，但重连前在途命令、过期 control 帧或错误服务端都
// 不得驱动本地进程；只有当前活跃 Run 的 framing 完整匹配才能被执行。
func (r *runner) commandCurrent(p commandPayload) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	lease := r.leases[p.RunID]
	_, active := r.runs[p.RunID]
	return active && lease != nil && p.RunnerID == r.id && p.ConnectionEpoch == r.epoch &&
		p.LeaseID == lease.LeaseID && p.FencingToken == lease.FencingToken
}

// handleAck 按 (run, lease, producer_seq) 精确删除 pending：一个 Run 的 ACK
// 不清另一个 Run 的 pending（v1 全局 contiguous_seq 水位已删除）。
func (r *runner) handleAck(env envelope) {
	var p ackPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return
	}
	key := pendingKey{RunID: p.RunID, LeaseID: p.LeaseID, Seq: p.AckedProducerSeq}
	r.mu.Lock()
	delete(r.pending, key)
	r.cleanupTerminalStateLocked(p.RunID)
	n := len(r.pending)
	r.mu.Unlock()
	log.Printf("runnerd: ACK run=%s seq=%d（剩余 pending %d）", p.RunID, p.AckedProducerSeq, n)
}

// cleanupTerminalStateLocked 在终态 Run 的所有 pending 已 ACK 后回收 lease
// framing、producer sequence 与 terminal 标记。调用方持 r.mu。
func (r *runner) cleanupTerminalStateLocked(runID string) {
	if !r.terminalPending[runID] {
		return
	}
	for key := range r.pending {
		if key.RunID == runID {
			return
		}
	}
	delete(r.leases, runID)
	delete(r.seqs, runID)
	delete(r.terminalPending, runID)
}

// emitEvent 上报一条 canonical 事件：创建即分配稳定 event_id（revt_ + ULID）
// 与 producer_seq（Run 内从 1 单调），先入 pending 再尝试写入——写失败只代表
// 本次连接不可用，帧保留在 pending 由重连原样重发（至少一次投递）。
// 终态 status 帧触发本地 finalize（租约/控制面资源释放），帧本身仍等 ACK。
func (r *runner) emitEvent(runID, kind string, data map[string]any) error {
	r.mu.Lock()
	li := r.leases[runID]
	if li == nil {
		r.mu.Unlock()
		return fmt.Errorf("run %s 不在本 runner（无租约 framing）", runID)
	}
	seq := r.seqs[runID] + 1
	r.seqs[runID] = seq
	ev := &pendingEvent{
		Key:     pendingKey{RunID: runID, LeaseID: li.LeaseID, Seq: seq},
		Fencing: li.FencingToken,
		EventID: "revt_" + newULID(),
		Kind:    kind,
		Data:    data,
	}
	r.pending[ev.Key] = ev
	_, active := r.runs[runID]
	if kind == "run.status_changed" && active && isTerminalStatus(data) {
		if r.terminalPending == nil {
			r.terminalPending = make(map[string]bool)
		}
		r.terminalPending[runID] = true
	}
	r.mu.Unlock()

	err := r.writeEvent(ev)
	if kind == "run.status_changed" && active && isTerminalStatus(data) {
		r.finalize(runID)
	}
	return err
}

// isTerminalStatus 判定 status 帧是否终态（终态才释放 checkout 租约）。
func isTerminalStatus(data map[string]any) bool {
	s, _ := data["status"].(string)
	return domain.RunStatus(s).IsTerminal()
}

// eventPayloadOf 把 pending 事件序列化为当前连接的 payload：事件身份
// （run/lease/runner/event_id/producer_seq/fencing）不变，connection_epoch
// 取当前连接（transport fencing 由网关按连接校验）。
func (r *runner) eventPayloadOf(ev *pendingEvent) json.RawMessage {
	return mustJSON(map[string]any{
		"run_id":           ev.Key.RunID,
		"lease_id":         ev.Key.LeaseID,
		"runner_id":        r.id,
		"connection_epoch": r.currentEpoch(),
		"fencing_token":    ev.Fencing,
		"event_id":         ev.EventID,
		"producer_seq":     ev.Key.Seq,
		"event":            map[string]any{"kind": ev.Kind, "data": ev.Data},
	})
}

// writeEvent 以当前连接发送一条 pending 事件。
func (r *runner) writeEvent(ev *pendingEvent) error {
	return r.writeLocked(mustJSON(envelope{
		V: protocolV2, MessageID: newMsgID(), Kind: "event", Method: "run.event",
		RunnerID: r.id, RunID: ev.Key.RunID, SentAt: time.Now().UTC(),
		Payload: r.eventPayloadOf(ev),
	}))
}

// resendPending 重连后按事件身份有序重发未 ACK 帧（run 内 seq 升序）。
// 乱序重发会被状态机拒收（如 succeeded 先于 running 到达），有序 + 服务端
// 按 (run, lease, producer_seq) 去重 = 至少一次且语义等价的投递。
func (r *runner) resendPending() error {
	r.mu.Lock()
	events := make([]*pendingEvent, 0, len(r.pending))
	for _, ev := range r.pending {
		events = append(events, ev)
	}
	r.mu.Unlock()
	sort.Slice(events, func(i, j int) bool {
		if events[i].Key.RunID != events[j].Key.RunID {
			return events[i].Key.RunID < events[j].Key.RunID
		}
		return events[i].Key.Seq < events[j].Key.Seq
	})
	for _, ev := range events {
		if err := r.writeEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

// moduleEngine 把 ModuleRunner 的引擎调用桥接为 WSS canonical 事件上报；
// 控制面 ingress 经 ApplyRunnerEvent 原子应用（含 approval_id / usage 透传）。
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
// Params）。wire key 固定为 ref，与控制面 ApplyRunnerEvent 同一语义，不能分叉
// 出 session_ref 别名，否则远程 Run 无法持久化会话锚点。
func (e *moduleEngine) RecordRunSessionUpdate(ctx context.Context, runID string, update rt.SessionUpdate) error {
	data := map[string]any{"ref": update.Ref}
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

// execContextModule 是 AdapterModule 包装：Execute 前把 offer 冻结的
// ExecutionContextSnapshot 与 hostregistry.Resolve 产物填入 ExecContext
// （ModuleRunner 原生装配接线前，由 runnerd 侧完成 SPI v2 上下文注入；
// 后续 ModuleRunner 支持后此包装可删）。
type execContextModule struct {
	inner rt.AdapterModule
	r     *runner
}

func (w *execContextModule) Manifest(ctx context.Context) (rt.AdapterManifest, error) {
	return w.inner.Manifest(ctx)
}

func (w *execContextModule) Probe(ctx context.Context, req rt.ProbeRequest) (rt.ProbeResult, error) {
	return w.inner.Probe(ctx, req)
}

func (w *execContextModule) Execute(ex *rt.ExecContext) rt.ExecResult {
	w.r.mu.Lock()
	li := w.r.leases[ex.Run.ID]
	w.r.mu.Unlock()
	if li != nil {
		ex.Execution = li.Snapshot
		ex.Resolved = li.Resolved
	}
	return w.inner.Execute(ex)
}

// newDshGateway 构造 dsh 网关执行面；默认与控制平面同端口，健康检查会
// 直接复用已运行的网关实例（不重复拉起）。工作目录语义由 ExecContext.
// Resolved.CWD 承载（RFC §8.4），模块配置不再持有全局 WorkspaceRoot。
func newDshGateway(r *runner) {
	workbenchRoot, _ := os.Getwd()
	projectSpace := agentwork.Resolve(workbenchRoot)
	_ = projectSpace.Ensure()
	repo := resolveDshRepo(workbenchRoot)
	gw := dsh.NewGateway(dsh.GatewayConfig{
		BaseURL: envOr("DSH_GATEWAY_URL", ""),
		Port:    atoiEnv("DSH_GATEWAY_PORT", 3090),
		RepoDir: repo,
		Home:    projectSpace.DSHHome(),
		Model:   envOr("DSH_MODEL", "deepseek-v4-flash"),
	})
	r.dshGateway = gw
	r.modules.Register("dsh", &execContextModule{inner: gw, r: r})
	log.Printf("runnerd: dsh 网关模块已注册（repo=%q）", repo)
}

// newKimiModule 构造 kimiapp 网关执行面（本机 kimi CLI 存在或显式直连 URL
// 时启用）；env 语义与 control-plane 的 ATW_KIMIAPP_* 一致。
func newKimiModule(r *runner) {
	workbenchRoot, _ := os.Getwd()
	kimiBin := agentwork.ResolveBundledBin(workbenchRoot, "ATW_KIMI_BIN", "kimi", "kimi")
	bin := agentwork.ResolveBundledBin(workbenchRoot, "ATW_KIMIAPP_BIN", "kimi", kimiBin)
	if envOr("ATW_KIMIAPP_URL", "") == "" {
		if !agentwork.ExecutableOK(bin) {
			return
		}
	}
	m := kimiapp.New(kimiapp.Config{
		BaseURL: envOr("ATW_KIMIAPP_URL", ""),
		Token:   envOr("ATW_KIMIAPP_TOKEN", ""),
		Port:    atoiEnv("ATW_KIMIAPP_PORT", 0),
		KimiBin: bin,
		Home:    envOr("ATW_KIMIAPP_HOME", agentwork.Resolve(workbenchRoot).KimiHome()),
		Model:   envOr("ATW_KIMIAPP_MODEL", ""),
	})
	r.kimiModule = m
	r.modules.Register("kimi-appserver", &execContextModule{inner: m, r: r})
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

// simulate 执行 mock 流程：事件按 producer_seq 递增上报，等待服务端 ACK。
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

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("runnerd: payload 序列化失败: %v", err))
	}
	return b
}

// newEpoch 生成 connection_epoch（ULID；每次新连接换代，由 connectOnce 调用）。
func newEpoch() string { return "epoch_" + newULID() }

// newBootID 是 runner 进程生命周期身份；同一进程的网络重连不会改变它。
func newBootID() string { return "boot_" + newULID() }

func newULID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

func newMsgID() string {
	return "msg_" + newULID()
}
