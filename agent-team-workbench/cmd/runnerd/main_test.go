package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
	rt "github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── 测试基建 ─────────────────────────────────────────────────────────

// requireGit 跳过无 git 的环境（offer→Resolve 正向路径依赖真实仓库）。
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用，跳过受信本地发现测试")
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newTestRepo 建真实 git 仓库 + registry yaml（mount agent-work 指向仓库），
// 返回 registry 与仓库 realpath。
func newTestRegistry(t *testing.T) *hostregistry.Registry {
	t.Helper()
	requireGit(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "init")

	yamlPath := filepath.Join(base, "registry.yaml")
	content := "version: 1\nmounts:\n  - alias: agent-work\n    root: " + repo + "\n    repository_identity: repo_aw\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := hostregistry.Load(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// captureTestWS 建立捕获型测试 WebSocket 服务端：收到的帧原样送入 frames 通道。
func captureTestWS(t *testing.T) (*websocket.Conn, <-chan []byte) {
	t.Helper()
	up := websocket.Upgrader{}
	frames := make(chan []byte, 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			frames <- raw
		}
	}))
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, frames
}

// dialTestWS 建立一个只读丢弃的测试 WebSocket，供 writeLocked 有真实连接。
func dialTestWS(t *testing.T) *websocket.Conn {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// readEnvelope 从捕获通道取一帧并解析为信封。
func readEnvelope(t *testing.T, frames <-chan []byte) envelope {
	t.Helper()
	select {
	case raw := <-frames:
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("帧解析失败: %v", err)
		}
		return env
	case <-time.After(2 * time.Second):
		t.Fatal("等待帧超时")
		return envelope{}
	}
}

// payloadEpoch 解析 hello / run.event 帧的 connection_epoch（RFC §4.2：
// 每次新连接换代）。
func payloadEpoch(t *testing.T, env envelope) string {
	t.Helper()
	var p struct {
		ConnectionEpoch string `json:"connection_epoch"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("payload 解析失败: %v", err)
	}
	return p.ConnectionEpoch
}

// eventIdentity 解析 run.event 信封 payload 的稳定事件身份。
func eventIdentity(t *testing.T, env envelope) (eventID string, seq int64, kind string, data map[string]any) {
	t.Helper()
	if env.V != protocolV2 || env.Method != "run.event" {
		t.Fatalf("期望 v2 run.event，实际 v=%d method=%s", env.V, env.Method)
	}
	var p struct {
		EventID     string `json:"event_id"`
		ProducerSeq int64  `json:"producer_seq"`
		RunID       string `json:"run_id"`
		LeaseID     string `json:"lease_id"`
		Event       struct {
			Kind string         `json:"kind"`
			Data map[string]any `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("payload 解析失败: %v", err)
	}
	return p.EventID, p.ProducerSeq, p.Event.Kind, p.Event.Data
}

// newTestRunner 构造带一个活动租约（run_1/lease_1）的 runner。
func newTestRunner(t *testing.T, conn *websocket.Conn, reg *hostregistry.Registry) *runner {
	t.Helper()
	r := &runner{
		id: "runner_test", hostID: "host_test", bootID: "boot_test", epoch: "epoch_1", slots: 2,
		registry:          reg,
		heartbeatInterval: defaultHeartbeatInterval,
		conn:              conn,
		seqs:              make(map[string]int64),
		pending:           make(map[pendingKey]*pendingEvent),
		runs:              make(map[string]*runnerRun),
		leases:            make(map[string]*leaseInfo),
		terminalPending:   make(map[string]bool),
		approvals:         make(map[string]chan bool),
		controls:          make(map[string]chan string),
		moduleRuns:        make(map[string]*domain.ExecutionRun),
	}
	if reg == nil {
		r.registry = hostregistry.New()
	}
	return r
}

// addLease 给 runner 登记活动租约（无 registry 释放的轻量路径）。
func addLease(r *runner, runID, leaseID string, fencing int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[runID] = &runnerRun{release: func() {}}
	r.leases[runID] = &leaseInfo{LeaseID: leaseID, FencingToken: fencing}
}

// ── 事件桥接与 pending（v2）──────────────────────────────────────────

// run.session 帧必须携带完整 SessionUpdate（Clear 墓碑 + Params 私有参数）。
func TestModuleEngineSessionUpdateSerialization(t *testing.T) {
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, nil)
	addLease(r, "run_1", "lease_1", 7)
	e := &moduleEngine{r: r}

	err := e.RecordRunSessionUpdate(context.Background(), "run_1", rt.SessionUpdate{
		Ref:         "dsh://sess_1",
		DisplayID:   "D-1",
		Clear:       true,
		ClearReason: "provider_session_lost",
		Params:      map[string]any{"thread_id": "th_1"},
	})
	if err != nil {
		t.Fatalf("RecordRunSessionUpdate: %v", err)
	}

	env := readEnvelope(t, frames)
	eventID, seq, kind, data := eventIdentity(t, env)
	if kind != "run.session" {
		t.Fatalf("事件类型 = %q，期望 run.session", kind)
	}
	if eventID == "" || !strings.HasPrefix(eventID, "revt_") || seq != 1 {
		t.Fatalf("事件身份缺失: id=%q seq=%d", eventID, seq)
	}
	if data["ref"] != "dsh://sess_1" || data["display_id"] != "D-1" {
		t.Fatalf("ref/display 丢失: %+v", data)
	}
	if data["clear"] != true || data["clear_reason"] != "provider_session_lost" {
		t.Fatalf("clear 墓碑未序列化: %+v", data)
	}
	params, ok := data["params"].(map[string]any)
	if !ok || params["thread_id"] != "th_1" {
		t.Fatalf("params 未序列化: %+v", data["params"])
	}
}

// 发送失败必须可被 module 层观察；失败帧仍留在 pending 等重连重发。
func TestModuleEngineRecordRunStatusReportsSendFailure(t *testing.T) {
	conn := dialTestWS(t)
	_ = conn.Close() // 模拟死连接
	r := newTestRunner(t, conn, nil)
	addLease(r, "run_1", "lease_1", 7)
	e := &moduleEngine{r: r}

	if err := e.RecordRunStatus(context.Background(), "run_1", domain.RunSucceeded, nil); err == nil {
		t.Fatal("发送失败时 RecordRunStatus 应返回 error 而非恒 nil")
	}
	r.mu.Lock()
	n := len(r.pending)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("失败帧应保留在 pending 待重发，实际 %d 帧", n)
	}
}

// ACK 按 (run, lease, producer_seq) 精确消化：一个 Run 的 ACK 不清另一个
// Run 的 pending（v1 全局 contiguous_seq 已删除）。
func TestAckIsolatesRuns(t *testing.T) {
	conn := dialTestWS(t)
	r := newTestRunner(t, conn, nil)
	addLease(r, "run_a", "lease_a", 1)
	addLease(r, "run_b", "lease_b", 2)

	_ = r.emitEvent("run_a", "message.delta", nil)                                        // seq 1
	_ = r.emitEvent("run_a", "run.status_changed", map[string]any{"status": "succeeded"}) // seq 2（终态）
	_ = r.emitEvent("run_b", "message.delta", nil)                                        // seq 1

	r.handle(envelope{
		V: protocolV2, Method: "ack",
		Payload: mustJSON(ackPayload{RunID: "run_a", LeaseID: "lease_a", AckedProducerSeq: 1, EventID: "revt_x"}),
	})

	r.mu.Lock()
	_, runASeq1 := r.pending[pendingKey{"run_a", "lease_a", 1}]
	_, runASeq2 := r.pending[pendingKey{"run_a", "lease_a", 2}]
	_, runBSeq1 := r.pending[pendingKey{"run_b", "lease_b", 1}]
	r.mu.Unlock()
	if runASeq1 {
		t.Fatal("run_a seq=1 已 ACK，应清除")
	}
	if !runASeq2 {
		t.Fatal("run_a seq=2 未 ACK，不得清除（终态帧必达）")
	}
	if !runBSeq1 {
		t.Fatal("run_b 的 pending 不得被 run_a 的 ACK 清除")
	}
}

// 终态 status 帧触发本地 finalize：checkout 租约释放、活动表清理，
// 但租约 framing 保留（终态后补发的 session/usage 事件仍有身份可挂）。
func TestTerminalStatusFinalizesRun(t *testing.T) {
	conn := dialTestWS(t)
	reg := newTestRegistry(t)
	r := newTestRunner(t, conn, reg)
	released := false
	r.mu.Lock()
	r.runs["run_1"] = &runnerRun{release: func() { released = true }}
	r.leases["run_1"] = &leaseInfo{LeaseID: "lease_1", FencingToken: 7}
	r.controls["run_1"] = make(chan string, 1)
	r.mu.Unlock()

	_ = r.emitEvent("run_1", "run.status_changed", map[string]any{"status": "succeeded"})

	r.mu.Lock()
	_, stillActive := r.runs["run_1"]
	_, stillControl := r.controls["run_1"]
	_, hasLease := r.leases["run_1"]
	r.mu.Unlock()
	if stillActive {
		t.Fatal("终态后应删除活动 run")
	}
	if stillControl {
		t.Fatal("终态后应清理控制 channel")
	}
	if !hasLease {
		t.Fatal("租约 framing 应保留供终态后事件使用")
	}
	if !released {
		t.Fatal("终态后必须释放 checkout 租约")
	}
	// 终态尚未 ACK 时必须保留 framing，保证断线重连仍可重发并带正确 lease/fence。
	r.mu.Lock()
	_, pendingTerminal := r.terminalPending["run_1"]
	r.mu.Unlock()
	if !pendingTerminal {
		t.Fatal("终态未 ACK 时应保留 terminal pending 标记")
	}
	r.handle(envelope{V: protocolV2, Method: "ack", Payload: mustJSON(ackPayload{
		RunID: "run_1", LeaseID: "lease_1", AckedProducerSeq: 1, EventID: "revt_terminal",
	})})
	r.mu.Lock()
	_, hasLeaseAfterAck := r.leases["run_1"]
	_, hasSeqAfterAck := r.seqs["run_1"]
	_, hasTerminalAfterAck := r.terminalPending["run_1"]
	r.mu.Unlock()
	if hasLeaseAfterAck || hasSeqAfterAck || hasTerminalAfterAck {
		t.Fatalf("终态全部 ACK 后必须回收 runner 内存: lease=%v seq=%v terminal=%v", hasLeaseAfterAck, hasSeqAfterAck, hasTerminalAfterAck)
	}
	// 释放幂等：重复 finalize 不 panic。
	r.finalize("run_1")
}

func TestTerminalAckCleansOnlyThatRunState(t *testing.T) {
	conn := dialTestWS(t)
	r := newTestRunner(t, conn, nil)
	addLease(r, "run_a", "lease_a", 1)
	addLease(r, "run_b", "lease_b", 2)
	_ = r.emitEvent("run_a", "run.status_changed", map[string]any{"status": "succeeded"})
	_ = r.emitEvent("run_b", "run.status_changed", map[string]any{"status": "succeeded"})
	r.handle(envelope{V: protocolV2, Method: "ack", Payload: mustJSON(ackPayload{
		RunID: "run_a", LeaseID: "lease_a", AckedProducerSeq: 1, EventID: "revt_a",
	})})
	r.mu.Lock()
	_, aLease := r.leases["run_a"]
	_, bLease := r.leases["run_b"]
	_, bPending := r.pending[pendingKey{RunID: "run_b", LeaseID: "lease_b", Seq: 1}]
	r.mu.Unlock()
	if aLease || !bLease || !bPending {
		t.Fatalf("ACK run_a 只能清其自身状态: aLease=%v bLease=%v bPending=%v", aLease, bLease, bPending)
	}
}

// 断线后重连：原样重发未 ACK 帧——事件身份（event_id/producer_seq）不变，
// envelope 重新封装；ACK 后 pending 收敛。
func TestRunLoopResendsUnackedTerminalFrameAfterReconnect(t *testing.T) {
	oldBackoff := reconnectBaseBackoff
	reconnectBaseBackoff = 10 * time.Millisecond
	t.Cleanup(func() { reconnectBaseBackoff = oldBackoff })

	connAccepted := make(chan *websocket.Conn, 2)
	frames := make(chan []byte, 64)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		connAccepted <- c
		for {
			if _, raw, err := c.ReadMessage(); err != nil {
				return
			} else {
				frames <- raw
			}
		}
	}))
	t.Cleanup(srv.Close)
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	r := newTestRunner(t, nil, hostregistry.New())
	addLease(r, "run_t1", "lease_t1", 7)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runLoop(ctx, r, url, nil)

	// 第一次连接：hello 后发一条终态帧；服务端不回 ACK 直接断开。
	conn1 := <-connAccepted
	firstHello := readEnvelope(t, frames)
	if firstHello.Method != "runner.hello" {
		t.Fatalf("首帧应为 runner.hello，实际 %s", firstHello.Method)
	}
	firstEpoch := payloadEpoch(t, firstHello)
	if err := r.emitEvent("run_t1", "run.status_changed", map[string]any{"status": "succeeded"}); err != nil {
		t.Fatalf("终态帧写入失败: %v", err)
	}
	first := readEnvelope(t, frames)
	firstID, firstSeq, firstKind, _ := eventIdentity(t, first)
	// 事件帧在首条连接上发出，payload 的 epoch 即该连接的 epoch。
	if got := payloadEpoch(t, first); got != firstEpoch {
		t.Fatalf("首条连接事件帧 epoch = %q，期望 %q", got, firstEpoch)
	}
	_ = conn1.Close() // 模拟连接重置（未 ACK）

	// 第二次连接：重连换代——hello 携带新 connection_epoch（RFC §4.2），
	// 之后原样重发同一事件身份的帧（仅 envelope/payload 的 epoch 重封）。
	conn2 := <-connAccepted
	secondHello := readEnvelope(t, frames)
	if secondHello.Method != "runner.hello" {
		t.Fatalf("重连首帧应为 runner.hello，实际 %s", secondHello.Method)
	}
	secondEpoch := payloadEpoch(t, secondHello)
	if secondEpoch == "" || secondEpoch == firstEpoch {
		t.Fatalf("重连必须换代 epoch：首连 %q，重连 %q", firstEpoch, secondEpoch)
	}
	resent := readEnvelope(t, frames)
	resentID, resentSeq, resentKind, resentData := eventIdentity(t, resent)
	if resentID != firstID || resentSeq != firstSeq {
		t.Fatalf("重发帧事件身份变化: id %q→%q seq %d→%d", firstID, resentID, firstSeq, resentSeq)
	}
	if resentKind != firstKind || resentData["status"] != "succeeded" {
		t.Fatalf("重发帧内容失真: kind=%s data=%+v", resentKind, resentData)
	}
	// 重发帧的 connection_epoch 必须已重封为新连接的 epoch。
	if got := payloadEpoch(t, resent); got != secondEpoch {
		t.Fatalf("重发帧 epoch = %q，期望重连后的 %q", got, secondEpoch)
	}

	// 按 (run, lease, seq) 回 ACK：pending 收敛到空。
	ack, _ := json.Marshal(envelope{
		V: protocolV2, MessageID: "msg_ack_test", Kind: "ack", Method: "ack",
		Payload: mustJSON(ackPayload{RunID: "run_t1", LeaseID: "lease_t1", AckedProducerSeq: firstSeq, EventID: firstID}),
	})
	if err := conn2.WriteMessage(websocket.TextMessage, ack); err != nil {
		t.Fatalf("发送 ACK 失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.pending)
		r.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ACK 后 pending 未清空，剩余 %d 帧", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestEmitEventConcurrentSeqPendingConsistency（配 -race 运行）：
// N 个 goroutine 并发 emitEvent，结束后
//   - seqs[run] == 总事件数；
//   - pending 的 key 集合 == {1..总数}（无覆盖丢帧、无缺口）；
//   - 每个 pending 帧的 payload.producer_seq 与其 key 一致。
func TestEmitEventConcurrentSeqPendingConsistency(t *testing.T) {
	conn := dialTestWS(t)
	r := newTestRunner(t, conn, nil)
	addLease(r, "run_x", "lease_x", 3)

	const goroutines = 8
	const perG = 50
	total := goroutines * perG

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				r.emitEvent("run_x", "message.delta", map[string]any{
					"text": "x", "g": g, "j": j,
				})
			}
		}(g)
	}
	wg.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seqs["run_x"] != int64(total) {
		t.Fatalf("seqs = %d，期望 %d", r.seqs["run_x"], total)
	}
	if len(r.pending) != total {
		t.Fatalf("pending 帧数 = %d，期望 %d（存在覆盖丢帧）", len(r.pending), total)
	}
	seen := make(map[int64]bool, total)
	for key, ev := range r.pending {
		if key.Seq < 1 || key.Seq > int64(total) {
			t.Fatalf("非法 seq %d", key.Seq)
		}
		if seen[key.Seq] {
			t.Fatalf("seq %d 重复", key.Seq)
		}
		seen[key.Seq] = true
		if ev.Key.Seq != key.Seq || ev.EventID == "" {
			t.Fatalf("pending 事件身份失真: key=%+v ev=%+v", key, ev)
		}
	}
	for i := int64(1); i <= int64(total); i++ {
		if !seen[i] {
			t.Fatalf("seq %d 缺失", i)
		}
	}
}

// ── hello / offer（v2）───────────────────────────────────────────────

// hello 帧契约：v=2、host_id（凭据解析）、connection_epoch、protocol_versions=[2]、
// mounts 广告；未配置 registry 时 mounts 为空。
func TestHelloCarriesV2Identity(t *testing.T) {
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, hostregistry.New()) // 空 registry：mounts=[]

	r.hello()

	env := readEnvelope(t, frames)
	if env.V != protocolV2 || env.Method != "runner.hello" || env.Kind != "request" {
		t.Fatalf("hello 形态错误: v=%d method=%s kind=%s", env.V, env.Method, env.Kind)
	}
	var p struct {
		HostID           string           `json:"host_id"`
		RunnerID         string           `json:"runner_id"`
		BootID           string           `json:"boot_id"`
		ConnectionEpoch  string           `json:"connection_epoch"`
		ProtocolVersions []int            `json:"protocol_versions"`
		Slots            int              `json:"slots"`
		Mounts           []map[string]any `json:"mounts"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.HostID != "host_test" || p.RunnerID != "runner_test" || p.BootID != "boot_test" || p.ConnectionEpoch != "epoch_1" {
		t.Fatalf("hello 身份错误: %+v", p)
	}
	if len(p.ProtocolVersions) != 1 || p.ProtocolVersions[0] != 2 {
		t.Fatalf("protocol_versions 错误: %v", p.ProtocolVersions)
	}
	if len(p.Mounts) != 0 {
		t.Fatalf("空 registry 的 mounts 应为空: %+v", p.Mounts)
	}
}

// offer 帧构造：把领域快照编码为 wire 形态（与网关 wireSnapshot 同映射）。
func offerEnvelope(t *testing.T, runID, leaseID string, fencing int64, snap domain.ExecutionContextSnapshot, input map[string]any) envelope {
	t.Helper()
	checkout := snap.CheckoutRef
	worktree := snap.WorktreeRef
	wire := wireSnapshot{
		ID: snap.ID, SchemaVersion: snap.SchemaVersion,
		ExecutionHostID: snap.ExecutionHostID, WorkspaceLocationID: snap.WorkspaceLocationID,
		WorkspaceAlias: snap.MountAlias, MountGeneration: snap.MountGeneration,
		RepositoryIdentity: snap.RepositoryIdentity, RefKind: snap.RefKind,
		BranchName: snap.BranchName, CheckoutRef: strPtr(checkout), WorktreeRef: strPtr(worktree),
		BaseRevision: snap.BaseRevision, LocationVersion: snap.LocationVersion,
		ContextGeneration: snap.ContextGeneration, SnapshotDigest: snap.SnapshotDigest,
	}
	_ = checkout
	_ = worktree
	payload := mustJSON(offerPayload{
		LeaseID: leaseID, FencingToken: fencing,
		RunSpec: runSpec{RunID: runID, AdapterID: "mock", ContextSnapshot: wire, Input: input},
	})
	return envelope{
		V: protocolV2, MessageID: "msg_offer", Kind: "request", Method: "run.offer",
		RunnerID: "runner_test", RunID: runID, Payload: payload,
	}
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// rootSnapshot 构造指向 registry mount 的 root 快照（digest 自洽）。
func rootSnapshot(t *testing.T, reg *hostregistry.Registry, hostID string) domain.ExecutionContextSnapshot {
	t.Helper()
	mounts := reg.Advertise()
	if len(mounts) == 0 {
		t.Fatal("registry 无 mount，测试前置失败")
	}
	s := domain.ExecutionContextSnapshot{
		ID: domain.NewID(domain.PrefixCtxSnapshot), RunID: domain.NewID(domain.PrefixRun),
		SchemaVersion: domain.SnapshotSchemaV1, WorkspaceID: "ws_1",
		WorkspaceLocationID: "wsloc_1", LocationVersion: 1,
		MountGeneration: mounts[0].RegistryGeneration, ExecutionHostID: hostID,
		MountAlias: mounts[0].Alias, RepositoryIdentity: mounts[0].RepositoryIdentity,
		RefKind: domain.RefRoot, ContextGeneration: 1, Source: domain.SnapshotSourceCurrent,
	}
	s.SnapshotDigest = s.ComputeDigest()
	return s
}

// mount 未广告（空 registry）：offer 必须 run.reject(reason=workspace)，
// 不 accept、不执行。
func TestOfferRejectsWhenMountMissing(t *testing.T) {
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, hostregistry.New()) // 空 registry

	snap := domain.ExecutionContextSnapshot{
		SchemaVersion: domain.SnapshotSchemaV1, WorkspaceLocationID: "wsloc_1",
		MountGeneration: "gen_x", ExecutionHostID: "host_test", MountAlias: "nope",
		RepositoryIdentity: "repo_x", RefKind: domain.RefRoot, ContextGeneration: 1,
	}
	snap.SnapshotDigest = snap.ComputeDigest()
	r.handle(offerEnvelope(t, "run_1", "lease_1", 7, snap, map[string]any{"instruction": "hi"}))

	env := readEnvelope(t, frames)
	if env.Method != "run.reject" || env.V != protocolV2 {
		t.Fatalf("期望 v2 run.reject，实际 v=%d method=%s", env.V, env.Method)
	}
	var p rejectPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Reason != "workspace" || p.RunID != "run_1" || p.LeaseID != "lease_1" ||
		p.ConnectionEpoch != "epoch_1" || p.FencingToken != 7 {
		t.Fatalf("reject 身份/原因错误: %+v", p)
	}
	r.mu.Lock()
	n := len(r.runs)
	r.mu.Unlock()
	if n != 0 {
		t.Fatal("被拒 offer 不得进入执行")
	}
}

// mount 匹配：Resolve 通过 → run.accept 带 snapshot_digest → 进入执行，
// ExecContext 注入 Execution/Resolved。
func TestOfferAcceptsWithResolvedWorkspace(t *testing.T) {
	reg := newTestRegistry(t)
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, reg)
	snap := rootSnapshot(t, reg, "host_test")

	r.handle(offerEnvelope(t, "run_1", "lease_1", 7, snap, map[string]any{"instruction": "hi"}))

	env := readEnvelope(t, frames)
	if env.Method != "run.accept" || env.V != protocolV2 {
		t.Fatalf("期望 v2 run.accept，实际 v=%d method=%s", env.V, env.Method)
	}
	var p acceptPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.SnapshotDigest != snap.SnapshotDigest {
		t.Fatalf("accept digest = %q，期望本地重算的 %q", p.SnapshotDigest, snap.SnapshotDigest)
	}
	if p.LeaseID != "lease_1" || p.ConnectionEpoch != "epoch_1" || p.FencingToken != 7 {
		t.Fatalf("accept framing 错误: %+v", p)
	}

	r.mu.Lock()
	li := r.leases["run_1"]
	r.mu.Unlock()
	if li == nil || li.Snapshot.ID != snap.ID || li.Resolved.CWD == "" || li.Resolved.SnapshotID != snap.ID {
		t.Fatalf("执行面上下文注入缺失: %+v", li)
	}
	// 收尾：释放 checkout 租约，不影响其他测试。
	r.finalize("run_1")
}

// digest 失配：offer 快照身份被改而 digest 未重算 → workspace reject。
func TestOfferRejectsOnDigestMismatch(t *testing.T) {
	reg := newTestRegistry(t)
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, reg)

	snap := rootSnapshot(t, reg, "host_test")
	snap.BaseRevision = "tampered" // 身份字段改动，digest 未重算
	r.handle(offerEnvelope(t, "run_1", "lease_1", 7, snap, nil))

	env := readEnvelope(t, frames)
	var p rejectPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if env.Method != "run.reject" || p.Reason != "workspace" ||
		!strings.Contains(p.Detail, "workspace_digest_mismatch") {
		t.Fatalf("digest 失配必须 workspace reject（detail=%s）", p.Detail)
	}
	r.finalize("run_1")
}

// mount generation 变化（snapshot 冻结后 registry 重指向）→ workspace reject。
func TestOfferRejectsOnGenerationChange(t *testing.T) {
	reg := newTestRegistry(t)
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, reg)

	snap := rootSnapshot(t, reg, "host_test")
	snap.MountGeneration = "stale_generation"
	snap.SnapshotDigest = snap.ComputeDigest()
	r.handle(offerEnvelope(t, "run_1", "lease_1", 7, snap, nil))

	env := readEnvelope(t, frames)
	var p rejectPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if env.Method != "run.reject" || !strings.Contains(p.Detail, "workspace_mount_generation_changed") {
		t.Fatalf("generation 变化必须 workspace reject（detail=%s）", p.Detail)
	}
	r.finalize("run_1")
}

// 同 checkout 并发租约占用：第二个 offer → workspace reject
// （workspace_checkout_busy）。
func TestOfferRejectsOnCheckoutBusy(t *testing.T) {
	reg := newTestRegistry(t)
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, reg)
	snap := rootSnapshot(t, reg, "host_test")

	// 手动占住 checkout。
	release, err := reg.Acquire(&snap)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	r.handle(offerEnvelope(t, "run_1", "lease_1", 7, snap, nil))

	env := readEnvelope(t, frames)
	var p rejectPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if env.Method != "run.reject" || !strings.Contains(p.Detail, "workspace_checkout_busy") {
		t.Fatalf("checkout 占用必须 workspace reject（detail=%s）", p.Detail)
	}
}

// 容量满：offer → capacity reject。
func TestOfferRejectsOnCapacity(t *testing.T) {
	reg := newTestRegistry(t)
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, reg)
	r.slots = 1
	snap := rootSnapshot(t, reg, "host_test")

	addLease(r, "run_full", "lease_full", 1)
	r.handle(offerEnvelope(t, "run_new", "lease_new", 9, snap, nil))

	env := readEnvelope(t, frames)
	var p rejectPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if env.Method != "run.reject" || p.Reason != "capacity" {
		t.Fatalf("容量满必须 capacity reject: %+v", p)
	}
}

// run.command 语义保持（v2 framing）：mock 路径 interrupt 投递控制 channel。
func TestRunCommandInterruptReachesMockControl(t *testing.T) {
	reg := newTestRegistry(t)
	conn, frames := captureTestWS(t)
	r := newTestRunner(t, conn, reg)
	addLease(r, "run_1", "lease_1", 7)
	r.mu.Lock()
	r.controls["run_1"] = make(chan string, 1)
	r.mu.Unlock()

	r.handle(envelope{
		V: protocolV2, Method: "run.command", RunID: "run_1",
		Payload: mustJSON(commandPayload{
			RunID: "run_1", LeaseID: "lease_1", RunnerID: "runner_test",
			ConnectionEpoch: "epoch_1", FencingToken: 7,
			CommandID: "cmd_1", Command: "interrupt",
		}),
	})

	select {
	case cmd := <-r.controls["run_1"]:
		if cmd != "interrupt" {
			t.Fatalf("控制信号 = %q", cmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt 未到达 mock 控制 channel")
	}
	_ = frames
}

func TestRunCommandRejectsStaleLeaseFenceAndEpoch(t *testing.T) {
	r := newTestRunner(t, dialTestWS(t), nil)
	addLease(r, "run_1", "lease_1", 7)
	r.mu.Lock()
	r.controls["run_1"] = make(chan string, 1)
	r.mu.Unlock()
	for _, p := range []commandPayload{
		{RunID: "run_1", LeaseID: "lease_old", RunnerID: "runner_test", ConnectionEpoch: "epoch_1", FencingToken: 7, Command: "interrupt"},
		{RunID: "run_1", LeaseID: "lease_1", RunnerID: "runner_test", ConnectionEpoch: "epoch_old", FencingToken: 7, Command: "interrupt"},
		{RunID: "run_1", LeaseID: "lease_1", RunnerID: "runner_test", ConnectionEpoch: "epoch_1", FencingToken: 8, Command: "interrupt"},
	} {
		r.handle(envelope{V: protocolV2, Method: "run.command", RunID: "run_1", Payload: mustJSON(p)})
	}
	select {
	case cmd := <-r.controls["run_1"]:
		t.Fatalf("过期 command 不得投递控制信号: %q", cmd)
	case <-time.After(50 * time.Millisecond):
	}
}

// parseCredentialHost：与网关 ParseHostCredential 同口径。
func TestParseCredentialHost(t *testing.T) {
	hostID, secret, ok := parseCredentialHost("atw_host_host_build1_s3cret")
	if !ok || hostID != "host_build1" || secret != "s3cret" {
		t.Fatalf("解析错误: %q %q %v", hostID, secret, ok)
	}
	if _, _, ok := parseCredentialHost("garbage"); ok {
		t.Fatal("非法格式必须拒绝")
	}
}
