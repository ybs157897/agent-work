// Package runnergateway 实现 Control Plane 侧的 Runner WSS 网关 v2
// （contracts/runner/v2/schema.json，任务控制面 RFC §7.1/§7.5/§8）。
//
// 职责：enrollment 双重凭据校验（WSS 升级 + runner.hello）、新 connection
// epoch 顶替旧连接、mount 广告投影、host 状态投影、host-aware 精确路由分派
// （禁止跨 Host/本机回退）、heartbeat 按 epoch 续租；run.accept/reject/event
// 只做 transport 校验后统一进入 application.ApplyRunner* 原子命令——网关不再
// 自行 dedup、不再映射事件、不再自改 Run 状态。v1 运行时轨
// （workspace_alias="default"、defaultWorkspace、全局 contiguous_seq ACK、
// RunnerEventDedup 旧调用、第一个在线 Runner 选择）成建制删除。
package runnergateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Engine 是网关依赖的应用层能力（由 *application.Service 实现）。
// ApplyRunnerEvent/Accept/Reject 是 RFC §8.3 的原子应用命令：dedup、
// Run/Session/Artifact/lease 状态、canonical events/outbox 在应用层同一事务；
// 返回 error = 瞬态失败（网关不 ACK，Runner 保留 pending 重试），
// Ack.Outcome = duplicate/stale 照常 ACK 但不应用。
type Engine interface {
	RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error
	RecordRunProgress(ctx context.Context, runID string, progress float64) error
	RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error
	// RecordRunSessionUpdate 持久化会话句柄/参数；Clear 时写锚点墓碑
	//（run.session 事件必须映射到全量语义，不能只取 ref）。
	RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error
	// RecordRunUsage 落 execution_runs.usage_* 并累计 task_sessions 输入 token。
	RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error
	RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error)
	RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error
	Run(ctx context.Context, id string) (*domain.ExecutionRun, error)
	// ── Runner v2 原子入口（RFC §8.3.1）──────────────────────────────
	ApplyRunnerEvent(ctx context.Context, in application.RunnerEventInput) (application.RunnerEventAck, error)
	ApplyRunnerAccept(ctx context.Context, in application.RunnerAcceptInput) error
	ApplyRunnerReject(ctx context.Context, in application.RunnerRejectInput) error
}

var _ Engine = (*application.Service)(nil)

const (
	leaseTTL          = 60 * time.Second
	maxFrameBytes     = 1 << 20
	heartbeatInterval = 15 * time.Second
)

// Gateway 管理所有 Runner 连接。
type Gateway struct {
	store    application.Store
	engine   Engine
	notifier application.Notifier

	mu    sync.Mutex
	conns map[string]*runnerConn
	// runnerApprovals 记录 runner 模块自己的审批 ID（approval.requested 事件
	// 携带），下发 approval.resolve 时按 (run, server approval) 翻译回 runner
	// 的 ID。同一 run 可能有多个并发审批：runID → serverApprovalID → runnerID。
	runnerApprovals map[string]map[string]string
	upgrader        websocket.Upgrader
}

// activeRun 是网关内存中的活动租约（run → lease/fencing），transport 校验的
// 查证真相；权威状态在应用层与 DB。
type activeRun struct {
	LeaseID      string
	FencingToken int64
}

type runnerConn struct {
	gw *Gateway
	// runnerID/hostID/epoch/slots/adapters 等 hello 校验后不可变（重建连接
	// 才会换代），读取无需连接级锁。
	runnerID string
	hostID   string
	bootID   string
	epoch    string
	// enrollmentDigest 是 WSS upgrade 时凭据 secret 的 digest。连接存活期间
	// 每次 ingress/dispatch 都与 Host 当前 enrollment_ref 对账；轮换凭据后
	// 旧连接下一次动作必须被撤销，不能一直复用旧认证结果。
	enrollmentDigest string
	runnerVersion    string
	osName           string
	archName         string
	slots            int
	adapters         []adapterInfo
	helloMounts      []domain.HostMount
	conn             *websocket.Conn
	send             chan []byte
	mu               sync.Mutex
	activeRuns       map[string]*activeRun // run_id -> lease
	restartedRunIDs  []string
	closed           bool
	superseded       bool // 被新 epoch 连接顶替：后续帧只 ACK 不应用
}

// adapterIDs 返回承接的 adapter ID 列表（日志与在线性判断用）。
func (rc *runnerConn) adapterIDs() []string {
	ids := make([]string, 0, len(rc.adapters))
	for _, a := range rc.adapters {
		ids = append(ids, a.ID)
	}
	return ids
}

// hasAdapter 报告该连接是否广告承接 adapterID。
func (rc *runnerConn) hasAdapter(adapterID string) bool {
	for _, a := range rc.adapters {
		if a.ID == adapterID {
			return true
		}
	}
	return false
}

// capacity 报告是否还有并发余量（slots - 活跃 run 数）。
func (rc *runnerConn) capacity() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.activeRuns) < max(rc.slots, 1)
}

func New(store application.Store, engine Engine, notifier application.Notifier) *Gateway {
	g := &Gateway{
		store: store, engine: engine, notifier: notifier,
		conns:           make(map[string]*runnerConn),
		runnerApprovals: make(map[string]map[string]string),
		upgrader: websocket.Upgrader{
			ReadBufferSize: 4096, WriteBufferSize: 4096,
			// Runner 为服务端到服务端出站连接：无 Origin 头才放行；
			// 携带 Origin 的浏览器请求必须与 Host 精确同源。
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				return "http://"+r.Host == origin || "https://"+r.Host == origin
			},
		},
	}
	go g.leaseSweeper()
	return g
}

// ServeHTTP 挂载在 ConnectPath（/runner/v2/connect）。
//
// 第一重凭据校验（WSS 升级前）：Bearer 凭据 `atw_host_<host_id>_<secret>`
// 必须解析出已 enrollment 的 Host，且 sha256(secret) 与 enrollment_ref 恒定
// 时间一致；host_local 永不经过网关（本机 Host 的 enrollment_ref 为空，
// 自然拒绝）。
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	const bearer = "Bearer "
	if len(auth) <= len(bearer) || auth[:len(bearer)] != bearer {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hostID, secret, ok := ParseHostCredential(auth[len(bearer):])
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	host, err := g.store.ExecutionHosts().Get(r.Context(), hostID)
	if err != nil || host == nil || host.EnrollmentRef == "" ||
		subtle.ConstantTimeCompare([]byte(enrollmentDigest(secret)), []byte(host.EnrollmentRef)) != 1 {
		log.Printf("runnergateway: 拒绝未 enrollment 的 Host %s（凭据不匹配）", hostID)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := g.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameBytes)

	// 第二重凭据校验（hello）：首帧必须是 v=2 runner.hello，protocol_versions
	// 含 2、envelope v=2、hello.host_id 与凭据 subject 一致——否则断连。
	_, raw, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}
	rc, err := g.acceptHello(conn, hostID, raw)
	if err != nil {
		log.Printf("runnergateway: hello 拒绝（host %s）: %v", hostID, err)
		conn.Close()
		return
	}

	rc.enrollmentDigest = enrollmentDigest(secret)
	if err := g.registerRunner(r.Context(), rc); err != nil {
		log.Printf("runnergateway: runner %s 注册失败: %v", rc.runnerID, err)
		conn.Close()
		return
	}
	g.welcome(rc)
	// 审批映射与已决命令都从持久 ApprovalRequest 恢复；Gateway 内存重启、
	// 用户离线批准以及 Runner 同 boot 重连都不能丢掉 runner-local approval_id。
	g.restoreAndReplayApprovals(rc)
	// welcome 先进入该连接唯一 send queue；随后 process-restart 收口可能触发
	// Coordinator retry/Dispatch，但其 run.offer 只能排在 welcome 之后。
	g.settleRestartedRuns(rc.restartedRunIDs)

	go rc.writeLoop()
	go rc.readLoop()
}

// acceptHello 校验首帧并构造连接态。不创建 Host/Mount/Location——hello 只能
// 更新 Runner 与 mount 广告投影（RFC §7.1）。
func (g *Gateway) acceptHello(conn *websocket.Conn, credentialHostID string, raw []byte) (*runnerConn, error) {
	var hello Envelope
	if err := json.Unmarshal(raw, &hello); err != nil {
		return nil, errors.New("首帧不是合法 envelope")
	}
	if hello.V != ProtocolVersion || hello.Method != "runner.hello" {
		return nil, fmt.Errorf("envelope v=%d method=%s（要求 v=2 runner.hello）", hello.V, hello.Method)
	}
	var hp helloPayload
	if err := json.Unmarshal(hello.Payload, &hp); err != nil {
		return nil, errors.New("hello payload 解析失败")
	}
	if !hp.supportsV2() {
		return nil, fmt.Errorf("protocol_versions %v 不含 2", hp.ProtocolVersions)
	}
	if hp.HostID != credentialHostID {
		return nil, fmt.Errorf("hello.host_id %s 与凭据 subject %s 不一致", hp.HostID, credentialHostID)
	}
	if hp.RunnerID == "" || hp.BootID == "" || hp.ConnectionEpoch == "" {
		return nil, errors.New("hello 缺 runner_id/boot_id/connection_epoch")
	}
	return &runnerConn{
		gw: g, runnerID: hp.RunnerID, hostID: hp.HostID, bootID: hp.BootID, epoch: hp.ConnectionEpoch,
		runnerVersion: hp.RunnerVersion, osName: hp.OS, archName: hp.Arch,
		slots: hp.Slots, adapters: hp.Adapters, helloMounts: hp.Mounts,
		conn: conn, send: make(chan []byte, 64), activeRuns: make(map[string]*activeRun),
	}, nil
}

// registerRunner 在同一数据库事务内写 Runner、mount 广告与 host ready 投影。
// 事务提交后才把连接暴露给 Dispatcher；任何持久化失败都必须拒绝连接，不能让
// 内存 conn 看似在线而租约 FK 无法创建。新 epoch 同时从 DB 恢复未释放 lease，
// 使进程重启/重连后的 pending event 仍能通过 fencing 校验。
func (g *Gateway) registerRunner(ctx context.Context, rc *runnerConn) error {
	now := time.Now().UTC()
	var leases []*application.RunLease
	var restartedRunIDs []string
	if err := g.store.InTx(ctx, func(txCtx context.Context) error {
		if existing, err := g.store.Runners().Get(txCtx, rc.runnerID); err == nil {
			if existing.ExecutionHostID != "" && existing.ExecutionHostID != rc.hostID {
				return fmt.Errorf("%w: runner %s 已绑定 host %s，拒绝 host %s 接管",
					domain.ErrStateConflict, rc.runnerID, existing.ExecutionHostID, rc.hostID)
			}
			if existing.BootID != rc.bootID {
				stale, err := g.store.Runners().ReleaseActiveLeasesByRunner(txCtx, rc.runnerID, now)
				if err != nil {
					return err
				}
				restartedRunIDs = append(restartedRunIDs, stale...)
			}
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err := g.store.Runners().Upsert(txCtx, &application.Runner{
			ID: rc.runnerID, ExecutionHostID: rc.hostID, BootID: rc.bootID, ConnectionEpoch: rc.epoch,
			Label: rc.runnerID, RunnerVersion: rc.runnerVersion, OS: rc.osName, Arch: rc.archName,
			Slots: max(rc.slots, 1), Status: "connected", LastSeenAt: &now,
		}); err != nil {
			return err
		}
		// mount 广告投影（hello 不得创建 Host/Location）。同 host+alias 歧义
		//（hello 内重复 alias）按 RFC §13 投影为 unavailable——歧义 mount 不可接单。
		seenAliases := map[string]bool{}
		for i := range rc.helloMounts {
			m := rc.helloMounts[i]
			m.ExecutionHostID = rc.hostID // host 绑定以凭据校验后的连接态为准
			// Status/LastSeenAt 是服务端投影字段，不在线协议中由 Runner 提供；
			// 正常广告默认 ready，重复 alias 再降为 unavailable。
			if m.Status == "" {
				m.Status = domain.MountStatusReady
			}
			if seenAliases[m.Alias] {
				m.Status = domain.MountStatusUnavailable
			}
			seenAliases[m.Alias] = true
			m.LastSeenAt = now
			if err := g.store.ExecutionHosts().UpsertMount(txCtx, &m); err != nil {
				return err
			}
		}
		if err := g.store.ExecutionHosts().SetStatus(txCtx, rc.hostID, domain.HostStatusReady, now); err != nil {
			return err
		}
		if len(restartedRunIDs) > 0 {
			return nil
		}
		var err error
		leases, err = g.store.Runners().ListActiveLeasesByRunner(txCtx, rc.runnerID)
		return err
	}); err != nil {
		return err
	}
	rc.mu.Lock()
	for _, lease := range leases {
		current := rc.activeRuns[lease.RunID]
		if current == nil || lease.FencingToken > current.FencingToken {
			rc.activeRuns[lease.RunID] = &activeRun{LeaseID: lease.LeaseID, FencingToken: lease.FencingToken}
		}
	}
	rc.mu.Unlock()

	g.mu.Lock()
	// 同名旧连接顶替。新连接已从 DB 恢复 active leases；旧连接只作 transport
	// fence，后续 handleDisconnect 发现自己不再 current 后不得把新 epoch 下线。
	if old, ok := g.conns[rc.runnerID]; ok {
		old.mu.Lock()
		old.superseded = true
		old.mu.Unlock()
		old.closeConn()
	}
	g.conns[rc.runnerID] = rc
	g.mu.Unlock()
	log.Printf("runnergateway: %s 已连接（host=%s epoch=%s adapters=%v mounts=%d）",
		rc.runnerID, rc.hostID, rc.epoch, rc.adapterIDs(), len(rc.helloMounts))
	rc.restartedRunIDs = append(rc.restartedRunIDs, restartedRunIDs...)
	return nil
}

// settleRestartedRuns 收敛不同 boot_id 留下的 lease。新进程没有旧进程的
// pending/runs/provider state，绝不能把它当普通网络重连继续续租；running 先
// 经 reconnecting 再 lost，其他非终态直接 failed，由既有 terminal hooks 恢复。
func (g *Gateway) settleRestartedRuns(runIDs []string) {
	if g.engine == nil {
		return
	}
	ctx := context.Background()
	for _, runID := range runIDs {
		run, err := g.engine.Run(ctx, runID)
		if err != nil || run == nil || run.Status.IsTerminal() {
			continue
		}
		switch run.Status {
		case domain.RunRunning:
			if err := g.engine.RecordRunStatus(ctx, runID, domain.RunReconnecting, nil); err == nil {
				if err := g.engine.RecordRunStatus(ctx, runID, domain.RunLost, nil); err != nil {
					log.Printf("runnergateway: run %s 进程重启后标记 lost 失败: %v", runID, err)
				}
			} else {
				log.Printf("runnergateway: run %s 进程重启后标记 reconnecting 失败: %v", runID, err)
			}
		case domain.RunReconnecting:
			if err := g.engine.RecordRunStatus(ctx, runID, domain.RunLost, nil); err != nil {
				log.Printf("runnergateway: run %s 进程重启后标记 lost 失败: %v", runID, err)
			}
		default:
			if err := g.engine.RecordRunStatus(ctx, runID, domain.RunFailed, map[string]any{
				"code": "runner_process_restarted", "retryable": true,
				"message": "Runner boot_id 已变化，旧进程内执行状态不可恢复",
			}); err != nil {
				log.Printf("runnergateway: run %s 进程重启后标记 failed 失败: %v", runID, err)
			}
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (g *Gateway) welcome(rc *runnerConn) {
	payload, _ := json.Marshal(welcomePayload{
		SelectedVersion:          ProtocolVersion,
		HeartbeatIntervalSeconds: int(heartbeatInterval.Seconds()),
		LeasePolicy: leasePolicy{
			TTLSeconds:           int(leaseTTL.Seconds()),
			RenewIntervalSeconds: int(leaseTTL.Seconds() / 3),
		},
		MaxFrameBytes: maxFrameBytes,
	})
	rc.sendEnvelope(Envelope{
		V: ProtocolVersion, MessageID: domain.NewID("msg_"), Kind: "response", Method: "server.welcome",
		RunnerID: rc.runnerID, SentAt: time.Now().UTC(), Payload: payload,
	})
}

// ── 连接读写循环 ─────────────────────────────────────────────────────

// sendEnvelope 入队待写帧。缓冲满时不静默丢弃：丢弃 ack 会让 Runner 的
// pending（含终态帧）无法收敛、run 悬置到 lease 过期；丢弃 run.command 会
// 丢用户意图。改为重置连接（连接级背压）：Runner 重连后按事件身份有序
// 重发未 ACK 帧，服务端去重，等效于不丢帧。
func (rc *runnerConn) sendEnvelope(env Envelope) bool {
	b, err := json.Marshal(env)
	if err != nil {
		return false
	}
	rc.mu.Lock()
	if rc.closed {
		rc.mu.Unlock()
		return false
	}
	select {
	case rc.send <- b:
		rc.mu.Unlock()
		return true
	default:
		rc.mu.Unlock()
		log.Printf("runnergateway: WARN %s 发送缓冲已满，重置连接（%s 由 Runner 重连重发）", rc.runnerID, env.Method)
		rc.closeConn()
		return false
	}
}

func (rc *runnerConn) writeLoop() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	write := func(b []byte) error {
		// 半死连接（Runner 不再读）必须快速失败并触发断连处理，
		// 否则一次阻塞写会冻结该连接的全部下行帧。
		_ = rc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return rc.conn.WriteMessage(websocket.TextMessage, b)
	}
	for {
		select {
		case b, ok := <-rc.send:
			if !ok {
				return
			}
			if err := write(b); err != nil {
				rc.gw.handleDisconnect(rc)
				return
			}
		case <-ticker.C:
			hb, _ := json.Marshal(Envelope{
				V: ProtocolVersion, MessageID: domain.NewID("msg_"), Kind: "event", Method: "heartbeat",
				RunnerID: rc.runnerID, ConnectionEpoch: rc.epoch, SentAt: time.Now().UTC(),
				Payload: marshalPayload(heartbeatPayload{}),
			})
			if err := write(hb); err != nil {
				rc.gw.handleDisconnect(rc)
				return
			}
		}
	}
}

func (rc *runnerConn) readLoop() {
	defer rc.gw.handleDisconnect(rc)
	for {
		_, raw, err := rc.conn.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		// v1 帧拒绝：envelope v≠2 一律忽略（连接建立时已协商 v2，
		// 运行中不隐式降级）。
		if env.V != ProtocolVersion {
			log.Printf("runnergateway: 忽略 v=%d 帧（method=%s）——仅支持 v2", env.V, env.Method)
			continue
		}
		rc.gw.handleMessage(rc, env)
	}
}

func (rc *runnerConn) closeConn() {
	rc.mu.Lock()
	if rc.closed {
		rc.mu.Unlock()
		return
	}
	rc.closed = true
	close(rc.send)
	rc.mu.Unlock()
	if rc.conn != nil {
		_ = rc.conn.Close()
	}
}

// isCurrent 报告 rc 是否仍是该 runner 的当前连接（旧 epoch 连接的迟到帧
// 只 ACK 不应用）。
func (g *Gateway) isCurrent(rc *runnerConn) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.conns[rc.runnerID] == rc
}

// handleDisconnect：只有当前 connection epoch 才能使 Runner/Host 下线并把
// 活动 Run 标 reconnecting。被新 epoch 顶替的旧 readLoop 会随后退出，但绝不能
// 覆盖新连接的 ready/connected 投影或中断已恢复的 lease。
func (g *Gateway) handleDisconnect(rc *runnerConn) {
	rc.closeConn()
	g.mu.Lock()
	cur, current := g.conns[rc.runnerID]
	if !current || cur != rc {
		g.mu.Unlock()
		return
	}
	delete(g.conns, rc.runnerID)
	rc.mu.Lock()
	runIDs := make([]string, 0, len(rc.activeRuns))
	for id := range rc.activeRuns {
		runIDs = append(runIDs, id)
	}
	rc.mu.Unlock()
	hasHostPeer := false
	for _, other := range g.conns {
		if other.hostID == rc.hostID {
			hasHostPeer = true
			break
		}
	}
	g.mu.Unlock()

	ctx := context.Background()
	now := time.Now().UTC()
	_ = g.store.Runners().SetStatus(ctx, rc.runnerID, "offline", now)
	if !hasHostPeer {
		_ = g.store.ExecutionHosts().SetStatus(ctx, rc.hostID, domain.HostStatusOffline, now)
	}
	for _, runID := range runIDs {
		if g.engine != nil {
			g.markRunReconnecting(ctx, runID, "runner disconnected")
		}
	}
	log.Printf("runnergateway: %s 断开（host=%s，活动 run %d 个进入 reconnecting）",
		rc.runnerID, rc.hostID, len(runIDs))
}

// markRunReconnecting 把任意非终态 Run 收敛到 reconnecting；若状态读取或迁移
// 失败则直接 failed，绝不让已无 lease 的 Run 无限悬置。
func (g *Gateway) markRunReconnecting(ctx context.Context, runID, reason string) {
	run, err := g.engine.Run(ctx, runID)
	if err != nil || run == nil || run.Status.IsTerminal() {
		return
	}
	if run.Status == domain.RunReconnecting {
		return
	}
	if err := g.engine.RecordRunStatus(ctx, runID, domain.RunReconnecting, nil); err == nil {
		return
	}
	if err := g.engine.RecordRunStatus(ctx, runID, domain.RunFailed, map[string]any{
		"code": "runner_connection_lost", "retryable": true, "message": reason,
	}); err != nil {
		log.Printf("runnergateway: run %s 断连收口失败: %v", runID, err)
	}
}

// markExpiredLeaseTerminal 在 lease 已释放后确保非终态 Run 最终落 terminal。
// 先经 reconnecting 再 lost，若任一迁移失败则 failed 兜底。
func (g *Gateway) markExpiredLeaseTerminal(ctx context.Context, runID string) {
	if g.engine == nil {
		return
	}
	g.markRunReconnecting(ctx, runID, "runner lease expired")
	run, err := g.engine.Run(ctx, runID)
	if err != nil || run == nil || run.Status.IsTerminal() {
		return
	}
	if run.Status == domain.RunReconnecting {
		if err := g.engine.RecordRunStatus(ctx, runID, domain.RunLost, nil); err == nil {
			return
		}
	}
	if err := g.engine.RecordRunStatus(ctx, runID, domain.RunFailed, map[string]any{
		"code": "runner_lease_expired", "retryable": true, "message": "Runner lease 已过期且无法恢复",
	}); err != nil {
		log.Printf("runnergateway: run %s lease 过期收口失败: %v", runID, err)
	}
}

// leaseSweeper 定期释放过期租约，并把任何仍非终态的受影响 Run 收敛。
func (g *Gateway) leaseSweeper() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		ctx := context.Background()
		runIDs, err := g.store.Runners().ExpireLeases(ctx, time.Now().UTC())
		if err != nil {
			continue
		}
		for _, runID := range runIDs {
			g.markExpiredLeaseTerminal(ctx, runID)
		}
	}
}
