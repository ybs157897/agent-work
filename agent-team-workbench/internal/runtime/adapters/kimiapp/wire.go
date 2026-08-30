package kimiapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ---- REST 客户端 ----

// restClient 访问 /api/v1：统一 envelope 解码 + 鉴权头；healthz 豁免鉴权。
type restClient struct {
	base  string // 形如 http://127.0.0.1:58627（无尾斜杠）
	token string
	hc    *http.Client
}

func newRestClient(base, token string) *restClient {
	return &restClient{
		base:  strings.TrimRight(base, "/"),
		token: token,
		hc:    &http.Client{Timeout: 30 * time.Second},
	}
}

// do 发请求并解码 envelope：业务错误（code≠0）与 HTTP/传输错误统一折叠为
// *kapError，由 kapFailure 分类。ctx 取消的优先级由调用方在分类前处理。
func (c *restClient) do(ctx context.Context, method, path string, body, out any, skipAuth bool) *kapError {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return &kapError{Transport: true, Message: fmt.Sprintf("encode request: %v", err)}
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return &kapError{Transport: true, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if !skipAuth && c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return &kapError{Transport: true, Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &kapError{Transport: true, Message: err.Error()}
	}
	var env restEnvelope
	parseErr := json.Unmarshal(raw, &env)
	if resp.StatusCode >= 400 && env.Code == 0 {
		// HTTP 层错误且 envelope 缺失/无业务码：用状态码合成（如裸 401/404）。
		msg := strings.TrimSpace(env.Msg)
		if msg == "" {
			msg = resp.Status
		}
		return &kapError{Status: resp.StatusCode, Message: msg}
	}
	if parseErr != nil && resp.StatusCode < 400 {
		return &kapError{Transport: true, Message: fmt.Sprintf("decode envelope: %v", parseErr)}
	}
	if env.Code != 0 {
		return &kapError{Code: env.Code, Status: resp.StatusCode, Message: env.Msg}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return &kapError{Transport: true, Message: fmt.Sprintf("decode data: %v", err)}
		}
	}
	return nil
}

func (c *restClient) healthz(ctx context.Context) *kapError {
	var out struct {
		OK bool `json:"ok"`
	}
	return c.do(ctx, http.MethodGet, "/api/v1/healthz", nil, &out, true)
}

func (c *restClient) getSession(ctx context.Context, id string) (*sessionSummary, *kapError) {
	var out sessionSummary
	if kerr := c.do(ctx, http.MethodGet, "/api/v1/sessions/"+id, nil, &out, false); kerr != nil {
		return nil, kerr
	}
	return &out, nil
}

func (c *restClient) createSession(ctx context.Context, req *createSessionRequest) (*sessionSummary, *kapError) {
	var out sessionSummary
	if kerr := c.do(ctx, http.MethodPost, "/api/v1/sessions", req, &out, false); kerr != nil {
		return nil, kerr
	}
	return &out, nil
}

func (c *restClient) updateProfile(ctx context.Context, sessionID string, req *sessionProfileRequest) *kapError {
	path := "/api/v1/sessions/" + sessionID + "/profile"
	return c.do(ctx, http.MethodPost, path, req, nil, false)
}

func (c *restClient) getSessionStatus(ctx context.Context, sessionID string) (*sessionStatus, *kapError) {
	var out sessionStatus
	path := "/api/v1/sessions/" + sessionID + "/status"
	if kerr := c.do(ctx, http.MethodGet, path, nil, &out, false); kerr != nil {
		return nil, kerr
	}
	return &out, nil
}

func (c *restClient) submitPrompt(ctx context.Context, sessionID string, req *promptSubmitRequest) (*promptSubmitResult, *kapError) {
	var out promptSubmitResult
	path := "/api/v1/sessions/" + sessionID + "/prompts"
	if kerr := c.do(ctx, http.MethodPost, path, req, &out, false); kerr != nil {
		return nil, kerr
	}
	return &out, nil
}

// steer 走 collection 端点（路径含 "::"）：先 POST prompts 拿到 prompt_id，
// 再把该 prompt 升级为 steering（对齐 services/promptQueue 的 steer 语义）。
func (c *restClient) steer(ctx context.Context, sessionID string, promptIDs []string) *kapError {
	path := "/api/v1/sessions/" + sessionID + "/prompts::steer"
	return c.do(ctx, http.MethodPost, path, &steerRequest{PromptIDs: promptIDs}, nil, false)
}

// abortPrompt 中止指定 prompt；40903（已完成）与 40402（不存在）幂等吞掉。
func (c *restClient) abortPrompt(ctx context.Context, sessionID, promptID string) *kapError {
	path := "/api/v1/sessions/" + sessionID + "/prompts/" + promptID + ":abort"
	kerr := c.do(ctx, http.MethodPost, path, nil, nil, false)
	if kerr != nil && (kerr.Code == codePromptAlreadyDone || kerr.Code == codePromptNotFound) {
		return nil
	}
	return kerr
}

// abortSession 会话级中止（无 prompt_id 时的兜底）。
func (c *restClient) abortSession(ctx context.Context, sessionID string) *kapError {
	path := "/api/v1/sessions/" + sessionID + ":abort"
	kerr := c.do(ctx, http.MethodPost, path, nil, nil, false)
	if kerr != nil && (kerr.Code == codePromptAlreadyDone || kerr.Code == codeSessionNotFound) {
		return nil
	}
	return kerr
}

// resolveApproval 决议审批；40902（已决议）/40404（不存在）幂等吞掉。
func (c *restClient) resolveApproval(ctx context.Context, sessionID, approvalID, decision, feedback string) *kapError {
	path := "/api/v1/sessions/" + sessionID + "/approvals/" + approvalID
	kerr := c.do(ctx, http.MethodPost, path, &approvalResolveRequest{Decision: decision, Feedback: feedback}, nil, false)
	if kerr != nil && (kerr.Code == codeApprovalResolved || kerr.Code == codeApprovalNotFound) {
		return nil
	}
	return kerr
}

// ---- WS 事件流 ----

// wsStream 是 /api/v1/ws 事件流：握手（server_hello 采样 heartbeat）→ 订阅
// （subscribe/ack）→ 读循环。ping 由读循环就地回 pong（同 nonce）；其余帧
// 推入 frames 通道供执行泵消费。frames 关闭即流断开（由泵决定重连/失败）。
type wsStream struct {
	conn      *websocket.Conn
	wmu       sync.Mutex // 串行化 pong/subscribe 写（gorilla 不支持并发写）
	frames    chan wsFrame
	closed    chan struct{}
	closeOnce sync.Once
	heartbeat time.Duration
	pending   []wsFrame // subscribe 等 ack 期间先到的业务帧（正常为空），泵启动后先消费
}

func wsURL(base string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + "/api/v1/ws"
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + "/api/v1/ws"
	default:
		return "ws://" + strings.TrimRight(base, "/") + "/api/v1/ws"
	}
}

// dialEvents 建立 WS 连接并完成 server_hello 握手；不在此订阅。
func dialEvents(ctx context.Context, baseURL, token string) (*wsStream, error) {
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL(baseURL), header)
	if err != nil {
		return nil, fmt.Errorf("ws dial: %w", err)
	}
	s := &wsStream{
		conn:      conn,
		frames:    make(chan wsFrame, 64),
		closed:    make(chan struct{}),
		heartbeat: 10 * time.Second,
	}
	// server_hello：首帧必须在短时间内到达（连接建立即发）。
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		s.close()
		return nil, fmt.Errorf("ws server_hello: %w", err)
	}
	var hello wsFrame
	if err := json.Unmarshal(raw, &hello); err != nil || hello.Type != "server_hello" {
		s.close()
		return nil, fmt.Errorf("ws first frame is not server_hello")
	}
	var hp serverHelloPayload
	if len(hello.Payload) > 0 {
		_ = json.Unmarshal(hello.Payload, &hp)
	}
	if hp.HeartbeatMs > 0 {
		s.heartbeat = time.Duration(hp.HeartbeatMs) * time.Millisecond
	}
	go s.readLoop()
	return s, nil
}

// readLoop 持续读帧：ping 就地回 pong；其余帧入通道。读超时 = max(3×heartbeat, 30s)
// （服务端按 heartbeat 周期 ping，静默超时即判定连接死亡）。
func (s *wsStream) readLoop() {
	defer close(s.frames)
	for {
		deadline := 3 * s.heartbeat
		if deadline < 30*time.Second {
			deadline = 30 * time.Second
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(deadline))
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		var f wsFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		if f.Type == "ping" {
			var pp pongPayload
			if len(f.Payload) > 0 {
				_ = json.Unmarshal(f.Payload, &pp)
			}
			payload, _ := json.Marshal(pongPayload{Nonce: pp.Nonce})
			_ = s.writeFrame(wsFrame{Type: "pong", Payload: payload})
			continue
		}
		select {
		case s.frames <- f:
		case <-s.closed:
			return
		}
	}
}

func (s *wsStream) writeFrame(f wsFrame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, b)
}

// subscribe 订阅会话事件并等待 ack：not_found → session_unknown 场景由调用方
// 判定；resync_required → 调用方应以空 cursor 重订阅。
func (s *wsStream) subscribe(ctx context.Context, sessionID string, cursor *sessionCursor) (*subscribeAckPayload, error) {
	id := "sub_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	payload := subscribePayload{SessionIDs: []string{sessionID}}
	if cursor != nil {
		payload.Cursors = map[string]*sessionCursor{sessionID: cursor}
	}
	raw, _ := json.Marshal(payload)
	if err := s.writeFrame(wsFrame{Type: "subscribe", ID: id, Payload: raw}); err != nil {
		return nil, fmt.Errorf("ws subscribe write: %w", err)
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("ws subscribe ack timeout")
		case <-s.closed:
			return nil, fmt.Errorf("ws stream closed before subscribe ack")
		case f, ok := <-s.frames:
			if !ok {
				return nil, fmt.Errorf("ws stream closed before subscribe ack")
			}
			if f.Type == "ack" && f.ID == id {
				if f.Code != 0 {
					return nil, &kapError{Code: f.Code, Message: "subscribe rejected"}
				}
				var ack subscribeAckPayload
				if len(f.Payload) > 0 {
					_ = json.Unmarshal(f.Payload, &ack)
				}
				return &ack, nil
			}
			// 订阅生效前不应收到业务帧；缓存供泵启动后消费（不能塞回 frames：
			// subscribe 自身就在读该通道，会自旋）。
			s.pending = append(s.pending, f)
		}
	}
}

// drainPending 取走 subscribe 期间缓存的先到帧（一次性）。
func (s *wsStream) drainPending() []wsFrame {
	if len(s.pending) == 0 {
		return nil
	}
	out := s.pending
	s.pending = nil
	return out
}

func (s *wsStream) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.conn.Close()
	})
}
