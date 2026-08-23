// wire.go — dsh web 网关的传输层客户端（协议经 2026-08-23 实测固化）。
//
// HTTP 面：POST /api/<method>，body 为 ClientRequest 信封
// {type:"client-request", rpcId, method, payload}；业务错误也回 HTTP 200 +
// ServerResponse {type:"server-response", rpcId, result:{ok,value|error}}。
// POST /api/respond 携带 ClientResponse 信封回应网关发起的审批/提问。
// 事件面：GET /api/events.mux 要求 WebSocket 升级（非升级请求 426）；
// 下行帧为 ServerRequest {type:"server-request", rpcId, method, payload}，
// payload.type ∈ session/event | session/subscribed | approval/* | question/* |
// session/queue | session/projection | stream/error。上行（客户端消息）是
// 协议违约：网关会以 1008 关闭连接，本客户端绝不发送。
package dsh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// wireClient 网关 HTTP+WS 客户端；base 形如 http://127.0.0.1:3090。
type wireClient struct {
	base string
	http *http.Client
}

func newWireClient(base string) *wireClient {
	return &wireClient{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// rpcWireError 网关 RpcError（code 见 apiproxy rpc-error 词表）。
type rpcWireError struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *rpcWireError) Error() string {
	return fmt.Sprintf("dsh gateway: %s: %s", e.Code, e.Message)
}

type wireResponse struct {
	Type   string          `json:"type"`
	RpcID  string          `json:"rpcId"`
	Result json.RawMessage `json:"result"`
}

type wireResult struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error *rpcWireError   `json:"error"`
}

// rpcSeq 跨并发 Run 与控制协程共享，必须原子递增（非原子自增会被 -race 抓获）。
var rpcSeq atomic.Int64

// call 发一次一元 RPC；成功时把 value 解到 out（out 为 nil 时丢弃）。
func (c *wireClient) call(ctx context.Context, method string, payload, out any) *rpcWireError {
	body, err := json.Marshal(map[string]any{
		"type": "client-request", "rpcId": fmt.Sprintf("atw-%d", nextRPCID()),
		"method": method, "payload": payload,
	})
	if err != nil {
		return &rpcWireError{Code: "internal", Message: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/"+method, bytes.NewReader(body))
	if err != nil {
		return &rpcWireError{Code: "internal", Message: err.Error()}
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return &rpcWireError{Code: "carrier_unreachable", Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return &rpcWireError{Code: "carrier_" + resp.Status, Message: strings.TrimSpace(string(raw))}
	}
	var envelope wireResponse
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &rpcWireError{Code: "carrier_bad_response", Message: err.Error()}
	}
	var result wireResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return &rpcWireError{Code: "carrier_bad_result", Message: err.Error()}
	}
	if !result.OK {
		if result.Error == nil {
			return &rpcWireError{Code: "internal", Message: "网关返回 ok=false 但缺少 error"}
		}
		return result.Error
	}
	if out != nil && len(result.Value) > 0 {
		if err := json.Unmarshal(result.Value, out); err != nil {
			return &rpcWireError{Code: "carrier_bad_value", Message: err.Error()}
		}
	}
	return nil
}

func nextRPCID() int64 {
	return rpcSeq.Add(1)
}

// respond 回应网关发起的请求（审批/提问）；rpcId 必须回显下发帧的 rpcId。
// 载荷为 ClientResponse 信封，HTTP 响应为 RpcReceipt {accepted[, reason]}。
func (c *wireClient) respond(ctx context.Context, rpcID string, value any) error {
	body, err := json.Marshal(map[string]any{
		"type": "client-response", "rpcId": rpcID,
		"result": map[string]any{"ok": true, "value": value},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/respond", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var receipt struct {
		Accepted bool   `json:"accepted"`
		Reason   string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&receipt)
	if !receipt.Accepted {
		return fmt.Errorf("respond not accepted (%s)", receipt.Reason)
	}
	return nil
}

// health 网关根路径探活（GET / 200 即健康；SPA 静态也走这条）。
func (c *wireClient) health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway health: %s", resp.Status)
	}
	return nil
}

// ready 完全就绪探针：根路径 200 且 mux 路由已挂载（GET /api/events.mux
// 返回 426 要求升级）。仅探活不够——启动早期 HTTP 面已通但 WS 面未挂载，
// 此刻升级握手会被对端断连（unexpected EOF）。
func (c *wireClient) ready(ctx context.Context) error {
	if err := c.health(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/events.mux", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusUpgradeRequired {
		return fmt.Errorf("gateway mux not ready: %s", resp.Status)
	}
	return nil
}

// ── mux 下行帧 ────────────────────────────────────────────────────────

// serverRequest WS 下行消息全文（method == payload.type）。
type serverRequest struct {
	RpcID   string          `json:"rpcId"`
	Payload json.RawMessage `json:"payload"`
}

// muxFrame 下行帧的判别联合；仅解出 Execute 关心的字段。
type muxFrame struct {
	Type      string        `json:"type"`
	SessionID string        `json:"sessionId,omitempty"`
	LastSeq   int64         `json:"lastSeq,omitempty"`
	Event     *sessionEvent `json:"event,omitempty"`

	// approval/requested
	ApprovalID string `json:"approvalId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	CallID     string `json:"callId,omitempty"`
	Reason     string `json:"reason,omitempty"`

	// approval/resolved
	Outcome string `json:"outcome,omitempty"`

	// question/requested
	Questions []wireQuestion `json:"questions,omitempty"`

	// stream/error
	Error *rpcWireError `json:"error,omitempty"`

	// rpcID 回显位：来自 ServerRequest 信封（payload 内无此字段），respond 时使用。
	rpcID string
	// raw 原始 payload 文本（session/queue、session/projection 等未映射帧记日志用）。
	raw string
}

// wireQuestion AskUserQuestionItem（仅回答所需字段）。
type wireQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Header   string `json:"header,omitempty"`
	Options  []struct {
		Label string `json:"label"`
	} `json:"options,omitempty"`
}

// sessionEvent 持久化事件信封（宽 data：词表见 dsh-session SessionEventMap）。
type sessionEvent struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

// muxSub 一次 mux 订阅；frames 关闭即流终止。
type muxSub struct {
	conn   *websocket.Conn
	frames chan muxFrame
}

// subscribe 建立 WS 下行。读循环把 payload 解析为 muxFrame 投递到 frames；
// ctx 取消或对端关闭时关闭 frames。
func (c *wireClient) subscribe(ctx context.Context) (*muxSub, error) {
	wsURL := "ws" + strings.TrimPrefix(c.base, "http") + "/api/events.mux"
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	sub := &muxSub{
		conn:   conn,
		frames: make(chan muxFrame, 64),
	}
	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var req serverRequest
			if json.Unmarshal(raw, &req) != nil || len(req.Payload) == 0 {
				continue
			}
			var frame muxFrame
			if err := json.Unmarshal(req.Payload, &frame); err != nil {
				continue
			}
			frame.rpcID = req.RpcID
			frame.raw = string(req.Payload)
			select {
			case sub.frames <- frame:
			case <-ctx.Done():
				goto out
			}
		}
	out:
		close(sub.frames)
		_ = conn.Close()
	}()
	return sub, nil
}

// close 立即终止订阅（幂等；frames 由读循环关闭）。
func (s *muxSub) close() {
	_ = s.conn.Close()
}
