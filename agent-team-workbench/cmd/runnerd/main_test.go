package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/domain"
	rt "github.com/ybs/agent-team-workbench/internal/runtime"
)

// dialTestWS 建立一个只负责读帧丢弃的测试 WebSocket 服务端，
// 供 emitEvent 的 writeLocked 有真实连接可写。
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

// eventOf 解析 run.event 信封的 payload，返回 (kind, data)。
func eventOf(t *testing.T, env envelope) (string, map[string]any) {
	t.Helper()
	var p struct {
		RunnerSeq int64 `json:"runner_seq"`
		Event     struct {
			Kind string         `json:"kind"`
			Data map[string]any `json:"data"`
		} `json:"event"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("payload 解析失败: %v", err)
	}
	return p.Event.Kind, p.Event.Data
}

// eventSeqOf 解析 run.event 信封的 runner_seq。
func eventSeqOf(t *testing.T, env envelope) int64 {
	t.Helper()
	var p struct {
		RunnerSeq int64 `json:"runner_seq"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("payload 解析失败: %v", err)
	}
	return p.RunnerSeq
}

// readEventFrame 从捕获通道取一帧并解析出 (kind, data)。
func readEventFrame(t *testing.T, frames <-chan []byte) (string, map[string]any) {
	t.Helper()
	return eventOf(t, readEnvelope(t, frames))
}

// run.session 帧必须携带完整 SessionUpdate（Clear 墓碑 + Params 私有参数）：
// 桥接层只发 session_ref/display_id 会让墓碑与 resume 参数跨进程丢失。
func TestModuleEngineSessionUpdateSerialization(t *testing.T) {
	conn, frames := captureTestWS(t)
	r := &runner{id: "runner_test", conn: conn, pending: make(map[int64][]byte)}
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

	kind, data := readEventFrame(t, frames)
	if kind != "run.session" {
		t.Fatalf("事件类型 = %q，期望 run.session", kind)
	}
	if data["session_ref"] != "dsh://sess_1" || data["display_id"] != "D-1" {
		t.Fatalf("ref/display 丢失: %+v", data)
	}
	if data["clear"] != true {
		t.Fatalf("clear 墓碑未序列化: %+v", data)
	}
	if data["clear_reason"] != "provider_session_lost" {
		t.Fatalf("clear_reason 未序列化: %+v", data)
	}
	params, ok := data["params"].(map[string]any)
	if !ok || params["thread_id"] != "th_1" {
		t.Fatalf("params 未序列化: %+v", data["params"])
	}
}

// ACK 只清除 ≤ contiguous_seq 的帧：终态帧（seq=2）未确认前必须留在 pending。
func TestAckClearsPendingUpToContiguousSeq(t *testing.T) {
	r := &runner{id: "runner_ack", conn: dialTestWS(t), pending: make(map[int64][]byte)}
	_ = r.emitEvent("run_1", "message.delta", nil)
	_ = r.emitEvent("run_1", "run.status_changed", map[string]any{"status": "succeeded"})

	payload, _ := json.Marshal(map[string]any{"contiguous_seq": 1})
	r.handle(envelope{V: 1, Method: "ack", Payload: payload})

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pending[1]; ok {
		t.Fatal("seq=1 已 ACK，应从 pending 清除")
	}
	if _, ok := r.pending[2]; !ok {
		t.Fatal("seq=2 未 ACK，不得清除（终态帧必达）")
	}
}

// 发送失败必须可被 module 层观察：RecordRunStatus 恒返 nil 会让
// ModuleRunner 无从记日志或兜底；失败帧仍留在 pending 等重连重发。
func TestModuleEngineRecordRunStatusReportsSendFailure(t *testing.T) {
	conn := dialTestWS(t)
	_ = conn.Close() // 模拟死连接
	r := &runner{id: "runner_dead", conn: conn, pending: make(map[int64][]byte)}
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

// 断线（含网关缓冲满主动重置）后：runnerd 必须重连并按原 runner_seq 有序重发
// 未 ACK 的终态帧；收到 ACK 后 pending 清空。这是远程终态必达的兜底路径。
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

	r := &runner{
		id: "runner_reconn", pending: make(map[int64][]byte),
		approvals: make(map[string]chan bool), controls: make(map[string]chan string),
		moduleRuns: make(map[string]*domain.ExecutionRun),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runLoop(ctx, r, url, nil)

	// 第一次连接：hello 后发一条终态帧；服务端读帧后不回 ACK 直接断开。
	conn1 := <-connAccepted
	if env := readEnvelope(t, frames); env.Method != "runner.hello" {
		t.Fatalf("首帧应为 runner.hello，实际 %s", env.Method)
	}
	if err := r.emitEvent("run_t1", "run.status_changed", map[string]any{"status": "succeeded"}); err != nil {
		t.Fatalf("终态帧写入失败: %v", err)
	}
	terminal := readEnvelope(t, frames)
	seq := eventSeqOf(t, terminal)
	_ = conn1.Close() // 模拟连接重置（未 ACK）

	// 第二次连接：重发的 hello 之后必须是同 seq 的同一条终态帧。
	conn2 := <-connAccepted
	if env := readEnvelope(t, frames); env.Method != "runner.hello" {
		t.Fatalf("重连首帧应为 runner.hello，实际 %s", env.Method)
	}
	resent := readEnvelope(t, frames)
	if got := eventSeqOf(t, resent); got != seq {
		t.Fatalf("重发帧 runner_seq=%d，期望原 seq=%d", got, seq)
	}
	kind, data := eventOf(t, resent)
	if kind != "run.status_changed" || data["status"] != "succeeded" {
		t.Fatalf("重发帧内容失真: kind=%s data=%+v", kind, data)
	}

	// 回 ACK：pending 必须收敛到空。
	ackPayload, _ := json.Marshal(map[string]any{"contiguous_seq": seq})
	ack, _ := json.Marshal(envelope{
		V: 1, MessageID: "msg_ack_test", Kind: "ack", Method: "ack",
		RunnerID: r.id, RunID: "run_t1", SentAt: time.Now().UTC(), Payload: ackPayload,
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
//   - r.seq == 总事件数；
//   - pending 的 key 集合 == {1..总数}（无覆盖丢帧、无缺口）；
//   - 每个 pending 帧的 payload.runner_seq 与其 key 一致（修复点：禁止回读 r.seq）。
func TestEmitEventConcurrentSeqPendingConsistency(t *testing.T) {
	r := &runner{
		id:      "runner_test",
		conn:    dialTestWS(t),
		pending: make(map[int64][]byte),
	}

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
	if r.seq != int64(total) {
		t.Fatalf("r.seq = %d，期望 %d", r.seq, total)
	}
	if len(r.pending) != total {
		t.Fatalf("pending 帧数 = %d，期望 %d（存在覆盖丢帧）", len(r.pending), total)
	}
	seen := make(map[int64]bool, total)
	for seq, b := range r.pending {
		if seq < 1 || seq > int64(total) {
			t.Fatalf("非法 seq %d", seq)
		}
		if seen[seq] {
			t.Fatalf("seq %d 重复", seq)
		}
		seen[seq] = true
		var env envelope
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("seq %d 帧解析失败: %v", seq, err)
		}
		var p struct {
			RunnerSeq int64 `json:"runner_seq"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Fatalf("seq %d payload 解析失败: %v", seq, err)
		}
		if p.RunnerSeq != seq {
			t.Fatalf("帧内 runner_seq=%d 与 pending key=%d 不一致（回读 r.seq 竞态）", p.RunnerSeq, seq)
		}
	}
	// key 集合恰为 {1..total}：缺口 = 断线重连后永不重发、服务端 ACK 无法收敛的洞。
	for i := int64(1); i <= int64(total); i++ {
		if !seen[i] {
			t.Fatalf("seq %d 缺失", i)
		}
	}
}
