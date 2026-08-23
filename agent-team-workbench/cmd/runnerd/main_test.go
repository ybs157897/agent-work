package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
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
