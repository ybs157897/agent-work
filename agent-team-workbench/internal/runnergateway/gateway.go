// Package runnergateway 实现 Control Plane 侧的 Runner WSS 网关（协议文档 §7）。
// 职责：runner.hello/server.welcome 握手、run.offer/accept、run.event 入口
// （runner_seq 去重 → canonical run_seq → 同事务落库）、租约与 fencing。
package runnergateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Engine 是网关依赖的应用层能力（由 *application.Service 实现）。
type Engine interface {
	RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error
	RecordRunProgress(ctx context.Context, runID string, progress float64) error
	RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error
	RecordRunSessionRef(ctx context.Context, runID, sessionRef string) error
	// RecordRunUsage 落 execution_runs.usage_* 并累计 task_sessions 输入 token。
	RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error
	RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error)
	RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error
	Run(ctx context.Context, id string) (*domain.ExecutionRun, error)
}

var _ Engine = (*application.Service)(nil)

// Envelope 对应 contracts/runner/v1/schema.json。
type Envelope struct {
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
	// serverApprovals 记录控制平面为 runner 请求创建的审批 ID（供后续映射）。
	serverApprovals map[string]string
	// runnerApprovals 记录 runner 模块自己的审批 ID（approval.requested 事件携带），
	// 下发 approval.resolve 时翻译回 runner 的 ID。
	runnerApprovals map[string]string
	upgrader        websocket.Upgrader
}

type runnerConn struct {
	gw          *Gateway
	runnerID    string
	workspaceID string
	adapters    []string
	conn        *websocket.Conn
	send        chan []byte
	mu          sync.Mutex
	activeRuns  map[string]string // run_id -> lease_id
	closed      bool
}

func New(store application.Store, engine Engine, notifier application.Notifier) *Gateway {
	g := &Gateway{
		store: store, engine: engine, notifier: notifier,
		conns:           make(map[string]*runnerConn),
		serverApprovals: make(map[string]string),
		runnerApprovals: make(map[string]string),
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

// ServeHTTP 挂载在 /runner/v1/connect。
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Runner 使用独立服务凭据，不复用浏览器会话（协议文档 §7.2）。
	if token := os.Getenv("RUNNER_TOKEN"); token != "" {
		auth := r.Header.Get("Authorization")
		want := "Bearer " + token
		if len(auth) != len(want) || subtle.ConstantTimeCompare([]byte(auth), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	conn, err := g.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(maxFrameBytes)

	// 第一条消息必须是 runner.hello；不兼容版本直接拒绝连接。
	_, raw, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return
	}
	var hello Envelope
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Method != "runner.hello" {
		conn.Close()
		return
	}
	var hp struct {
		ProtocolVersions []int  `json:"protocol_versions"`
		RunnerVersion    string `json:"runner_version"`
		OS               string `json:"os"`
		Arch             string `json:"arch"`
		Slots            int    `json:"slots"`
		Adapters         []struct {
			AdapterID string `json:"adapter_id"`
		} `json:"adapters"`
	}
	if err := json.Unmarshal(hello.Payload, &hp); err != nil {
		conn.Close()
		return
	}
	compatible := false
	for _, v := range hp.ProtocolVersions {
		if v == 1 {
			compatible = true
		}
	}
	if !compatible {
		conn.Close()
		return
	}

	workspaceID := g.defaultWorkspace(r.Context())
	adapterIDs := make([]string, 0, len(hp.Adapters))
	for _, a := range hp.Adapters {
		adapterIDs = append(adapterIDs, a.AdapterID)
	}
	rc := &runnerConn{
		gw: g, runnerID: hello.RunnerID, workspaceID: workspaceID,
		adapters: adapterIDs, conn: conn, send: make(chan []byte, 64),
		activeRuns: make(map[string]string),
	}

	g.registerRunner(r.Context(), rc, hp.RunnerVersion, hp.OS, hp.Arch, hp.Slots)
	g.welcome(rc)

	go rc.writeLoop()
	go rc.readLoop()
}

func (g *Gateway) defaultWorkspace(ctx context.Context) string {
	ids, err := g.store.Workspaces().ListIDs(ctx)
	if err != nil || len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func (g *Gateway) registerRunner(ctx context.Context, rc *runnerConn, version, os_, arch string, slots int) {
	now := time.Now().UTC()
	_ = g.store.Runners().Upsert(ctx, &application.Runner{
		ID: rc.runnerID, WorkspaceID: rc.workspaceID, Label: rc.runnerID,
		RunnerVersion: version, OS: os_, Arch: arch, Slots: maxInt(slots, 1),
		Status: "connected", LastSeenAt: &now,
	})
	g.mu.Lock()
	// 同名旧连接顶替：关闭旧连接（fencing 保证旧实例不能继续写入）。
	if old, ok := g.conns[rc.runnerID]; ok {
		old.closeConn()
	}
	g.conns[rc.runnerID] = rc
	g.mu.Unlock()
	g.emitRunnerEvent(ctx, rc.workspaceID, domain.EventRunnerConnected, rc.runnerID)
	log.Printf("runnergateway: %s 已连接（adapters=%v）", rc.runnerID, rc.adapters)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (g *Gateway) welcome(rc *runnerConn) {
	payload, _ := json.Marshal(map[string]any{
		"selected_version":           1,
		"heartbeat_interval_seconds": int(heartbeatInterval.Seconds()),
		"lease_policy": map[string]any{
			"ttl_seconds":            int(leaseTTL.Seconds()),
			"renew_interval_seconds": int(leaseTTL.Seconds() / 3),
		},
		"max_frame_bytes": maxFrameBytes,
	})
	rc.sendEnvelope(Envelope{
		V: 1, MessageID: domain.NewID("msg_"), Kind: "response", Method: "server.welcome",
		RunnerID: rc.runnerID, SentAt: time.Now().UTC(), Payload: payload,
	})
}

func (g *Gateway) emitRunnerEvent(ctx context.Context, workspaceID, evType, runnerID string) {
	if workspaceID == "" {
		return
	}
	err := g.store.InTx(ctx, func(ctx context.Context) error {
		ev, err := domain.NewCanonicalEvent(workspaceID, evType, domain.AggregateRunner, runnerID, 0,
			map[string]any{"runner_id": runnerID})
		if err != nil {
			return err
		}
		_, err = g.store.Events().Append(ctx, ev, nil)
		return err
	})
	if err == nil {
		g.notifier.Notify(workspaceID)
	}
}

// ── 连接读写循环 ─────────────────────────────────────────────────────

func (rc *runnerConn) sendEnvelope(env Envelope) {
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	select {
	case rc.send <- b:
	default:
		log.Printf("runnergateway: %s 发送缓冲已满，丢弃 %s", rc.runnerID, env.Method)
	}
}

func (rc *runnerConn) writeLoop() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case b, ok := <-rc.send:
			if !ok {
				return
			}
			if err := rc.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				rc.gw.handleDisconnect(rc)
				return
			}
		case <-ticker.C:
			hb, _ := json.Marshal(Envelope{
				V: 1, MessageID: domain.NewID("msg_"), Kind: "event", Method: "heartbeat",
				RunnerID: rc.runnerID, SentAt: time.Now().UTC(),
			})
			if err := rc.conn.WriteMessage(websocket.TextMessage, hb); err != nil {
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
	_ = rc.conn.Close()
}

// handleDisconnect：Runner 失联不猜测 Provider 状态；活动 Run 进入 reconnecting，
// 租约到期未续由 sweeper 判定 lost（协议文档 §7.2）。
func (g *Gateway) handleDisconnect(rc *runnerConn) {
	rc.closeConn()
	g.mu.Lock()
	if cur, ok := g.conns[rc.runnerID]; ok && cur == rc {
		delete(g.conns, rc.runnerID)
	}
	runIDs := make([]string, 0, len(rc.activeRuns))
	for id := range rc.activeRuns {
		runIDs = append(runIDs, id)
	}
	g.mu.Unlock()

	ctx := context.Background()
	_ = g.store.Runners().SetStatus(ctx, rc.runnerID, "offline", time.Now().UTC())
	g.emitRunnerEvent(ctx, rc.workspaceID, domain.EventRunnerDisconnected, rc.runnerID)
	for _, runID := range runIDs {
		if err := g.engine.RecordRunStatus(ctx, runID, domain.RunReconnecting, nil); err != nil {
			log.Printf("runnergateway: run %s 标记 reconnecting 失败: %v", runID, err)
		}
		_ = g.store.Events().AppendActivity(ctx, rc.workspaceID, "runner.disconnected",
			"Runner "+rc.runnerID+" 失联，run "+runID+" 进入 reconnecting")
		g.notifier.Notify(rc.workspaceID)
	}
	log.Printf("runnergateway: %s 断开（活动 run %d 个进入 reconnecting）", rc.runnerID, len(runIDs))
}

// leaseSweeper 定期释放过期租约并把 reconnecting 的 Run 判定为 lost。
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
			run, err := g.engine.Run(ctx, runID)
			if err != nil || run.Status != domain.RunReconnecting {
				continue
			}
			if err := g.engine.RecordRunStatus(ctx, runID, domain.RunLost, nil); err != nil {
				log.Printf("runnergateway: run %s 判定 lost 失败: %v", runID, err)
			}
		}
	}
}
