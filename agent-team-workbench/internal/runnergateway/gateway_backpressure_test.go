package runnergateway

import "testing"

// 发送缓冲满时不静默丢帧：丢弃 ack 会让 Runner pending（含终态帧）无法收敛，
// run 悬置到 lease 过期。期望行为是重置连接（连接级背压，Runner 重连重发），
// 且重置后的再次入队不得向已关闭 channel 发送（panic）。
func TestSendEnvelopeFullBufferResetsConnection(t *testing.T) {
	g := New(nil, nil, nil)
	rc := &runnerConn{gw: g, runnerID: "r1", send: make(chan []byte, 1), activeRuns: map[string]string{}}

	rc.send <- []byte(`{"m":"fill"}`) // 填满缓冲
	rc.sendEnvelope(Envelope{V: 1, Method: "ack", MessageID: "msg_x"})

	rc.mu.Lock()
	closed := rc.closed
	buffered := len(rc.send)
	rc.mu.Unlock()
	if !closed {
		t.Fatal("缓冲满时应重置连接而非静默丢帧")
	}
	if buffered != 1 {
		t.Fatalf("缓冲应只含原有 1 帧（新帧不得入队后被丢），实际 %d", buffered)
	}

	// 重置后再次入队：安全返回，不 panic。
	rc.sendEnvelope(Envelope{V: 1, Method: "run.command", MessageID: "msg_y"})
}
