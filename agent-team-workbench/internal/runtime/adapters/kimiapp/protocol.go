// Package kimiapp 实现 Kimi Code app-server（kap-server）Adapter（SPI v2 网关形态）。
//
// 传输（协议源码核对自 /tmp/kimi-code packages/protocol + packages/kap-server）：
//   - REST /api/v1：统一 envelope {code,msg,data,request_id}，业务错误经 code 字段
//     表达（HTTP 多为 200）；鉴权 Authorization: Bearer <token>（healthz 豁免）。
//   - WS /api/v1/ws：server_hello → subscribe(+ack) → 事件帧；帧 type 即事件名，
//     volatile 帧（assistant.delta 等）带 offset，durable 帧带 seq/epoch 供 cursor
//     断线续传；服务端周期 ping，须回 pong（同 nonce），静默 2 个周期即被断开。
//   - abort 只能走 REST（WS 侧 switch 不处理 abort 帧）：prompts/{pid}:abort，
//     幂等（40903 视为已中止）。
//
// 执行模型（镜像 dsh 网关形态）：supervisor 长驻 `kimi web` 子进程（直连模式只探活），
// 一次 Execute = 会话解析（resume 探测 / fresh 创建）→ WS 订阅 → prompt 提交 →
// 事件泵推进到本 turn 的 turn.ended。事件映射为 canonical；未映射帧显式 OnLog。
package kimiapp

import (
	"encoding/json"
	"fmt"
	"strings"

	rt "github.com/ybs/agent-team-workbench/internal/runtime"
)

// ---- 错误码（packages/protocol/src/error-codes.ts）----

const (
	codeValidationFailed  = 40001 // 参数校验失败
	codeAuthFailed        = 40101 // 鉴权失败（HTTP 401）
	codeSessionNotFound   = 40401 // 会话不存在
	codePromptNotFound    = 40402 // prompt 不存在
	codeApprovalNotFound  = 40404 // 审批不存在
	codeSessionBusy       = 40901 // 会话忙
	codeApprovalResolved  = 40902 // 审批已决议（幂等）
	codePromptAlreadyDone = 40903 // prompt 已完成（abort 幂等）
	codeInternalError     = 50001 // 服务端内部错误
)

// ---- REST envelope / 载荷 ----

// restEnvelope 是 /api/v1 统一响应壳：code=0 成功；非 0 时 msg 为错误信息。
type restEnvelope struct {
	Code      int             `json:"code"`
	Msg       string          `json:"msg"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

// sessionSummary 只取决策字段（id）；其余（busy/title/usage）不参与适配器决策。
type sessionSummary struct {
	ID string `json:"id"`
}

// createSessionRequest 对齐 POST /sessions body：metadata.cwd 必填。
// main 与 0.38 都会静默忽略 create.agent_config，禁止发送假配置。
type createSessionRequest struct {
	Metadata map[string]string `json:"metadata"`
}

type agentConfig struct {
	PermissionMode string `json:"permission_mode"`
	SwarmMode      bool   `json:"swarm_mode"`
}

// sessionProfileRequest 是 resume 前重申的会话 profile；KAP 的 profile
// 更新契约要求 agent_config 嵌套在 body 中。
type sessionProfileRequest struct {
	AgentConfig agentConfig `json:"agent_config"`
}

type sessionStatus struct {
	Permission string `json:"permission"`
	SwarmMode  bool   `json:"swarm_mode"`
}

type promptContentPart struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

// promptSubmitRequest 对齐 PromptSubmission：content ≥1 段；model 与
// permission_mode(manual|yolo|auto) 服务端逐 prompt 应用。swarm_mode/plan_mode
// 虽在 schema 中，但 main 与 0.38 prompt 路由均不消费，不前向；模式只走 profile。
type promptSubmitRequest struct {
	Content        []promptContentPart `json:"content"`
	Model          string              `json:"model,omitempty"`
	PermissionMode string              `json:"permission_mode,omitempty"`
}

// promptSubmitResult：status ∈ running|queued|blocked。
type promptSubmitResult struct {
	PromptID string `json:"prompt_id"`
	Status   string `json:"status"`
}

type steerRequest struct {
	PromptIDs []string `json:"prompt_ids"`
}

type approvalResolveRequest struct {
	Decision string `json:"decision"` // approved|rejected|cancelled
	Feedback string `json:"feedback,omitempty"`
}

// ---- WS 帧（packages/protocol/src/ws-control.ts）----

// wsFrame 兼具事件帧与系统帧：事件帧 type=事件名 + payload=完整事件；
// ack 帧 type="ack" + id/code/payload{accepted,not_found,resync_required,cursors}；
// ping 帧 type="ping"（读循环直接回 pong，不进业务泵）。
type wsFrame struct {
	Type      string          `json:"type"`
	Seq       int64           `json:"seq,omitempty"`
	Epoch     string          `json:"epoch,omitempty"`
	Volatile  bool            `json:"volatile,omitempty"`
	Offset    *int64          `json:"offset,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`

	// ack 字段（仅 type=="ack"）
	ID   string `json:"id,omitempty"`
	Code int    `json:"code"` // ack 自身结果码；0 成功
}

type serverHelloPayload struct {
	WSConnectionID  string `json:"ws_connection_id"`
	ProtocolVersion int    `json:"protocol_version"`
	HeartbeatMs     int    `json:"heartbeat_ms,omitempty"`
	MaxEventBufSize int    `json:"max_event_buffer_size"`
}

type subscribePayload struct {
	SessionIDs []string                  `json:"session_ids"`
	Cursors    map[string]*sessionCursor `json:"cursors,omitempty"`
}

// sessionCursor：断线续传游标（seq + 可选 epoch）。
type sessionCursor struct {
	Seq   int64  `json:"seq"`
	Epoch string `json:"epoch,omitempty"`
}

type subscribeAckPayload struct {
	Accepted       []string                  `json:"accepted"`
	NotFound       []string                  `json:"not_found"`
	ResyncRequired []string                  `json:"resync_required"`
	Cursors        map[string]*sessionCursor `json:"cursors,omitempty"`
}

type pongPayload struct {
	Nonce string `json:"nonce"`
}

// ---- 事件载荷（packages/protocol/src/events.ts）----

type evTurnStarted struct {
	TurnID   int64  `json:"turnId"`
	AgentID  string `json:"agentId,omitempty"`
	PromptID string `json:"promptId,omitempty"`
}

// evTurnEnded：reason ∈ completed|cancelled|failed|blocked；error 为
// KimiErrorPayload{code,message,retryable}。
type evTurnEnded struct {
	TurnID  int64    `json:"turnId"`
	AgentID string   `json:"agentId,omitempty"`
	Reason  string   `json:"reason"`
	Error   *evError `json:"error,omitempty"`
}

type evError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable *bool  `json:"retryable,omitempty"`
}

type evDelta struct {
	TurnID  int64  `json:"turnId"`
	AgentID string `json:"agentId,omitempty"`
	Delta   string `json:"delta"`
}

// tokenUsage 对齐 protocol TokenUsage。指针保留协议字段的 presence：缺失
// 不得在 canonical usage 中伪装成显式 zero。
type tokenUsage struct {
	InputOther         *int64 `json:"inputOther"`
	Output             *int64 `json:"output"`
	InputCacheRead     *int64 `json:"inputCacheRead"`
	InputCacheCreation *int64 `json:"inputCacheCreation"`
}

type evStepCompleted struct {
	TurnID  int64       `json:"turnId"`
	AgentID string      `json:"agentId,omitempty"`
	Step    int         `json:"step"`
	Usage   *tokenUsage `json:"usage,omitempty"`
}

type evToolCallStarted struct {
	TurnID      int64           `json:"turnId"`
	AgentID     string          `json:"agentId,omitempty"`
	ToolCallID  string          `json:"toolCallId"`
	Name        string          `json:"name"`
	Args        json.RawMessage `json:"args,omitempty"`
	Description string          `json:"description,omitempty"`
}

type evToolResult struct {
	TurnID     int64           `json:"turnId"`
	AgentID    string          `json:"agentId,omitempty"`
	ToolCallID string          `json:"toolCallId"`
	Output     json.RawMessage `json:"output,omitempty"`
	IsError    *bool           `json:"isError,omitempty"`
}

type evToolProgress struct {
	TurnID     int64  `json:"turnId"`
	AgentID    string `json:"agentId,omitempty"`
	ToolCallID string `json:"toolCallId"`
	Update     struct {
		Kind    string   `json:"kind"`
		Text    string   `json:"text"`
		Percent *float64 `json:"percent,omitempty"`
	} `json:"update"`
}

// evApprovalRequested 对齐 toWireApproval 投影（蛇形命名）。
type evApprovalRequested struct {
	ApprovalID string `json:"approval_id"`
	SessionID  string `json:"session_id"`
	TurnID     int64  `json:"turn_id,omitempty"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	Action     string `json:"action"`
}

// ---- 错误模型 ----

// kapError 统一 REST 失败：envelope code（业务错）或传输/HTTP 层失败（Transport）。
type kapError struct {
	Code      int  // envelope code；传输错为 0
	Status    int  // HTTP 状态码；传输错为 0
	Transport bool // true = 连接/序列化层失败
	Message   string
}

func (e *kapError) Error() string {
	if e.Transport {
		return fmt.Sprintf("kap transport error: %s", e.Message)
	}
	return fmt.Sprintf("kap error %d: %s", e.Code, e.Message)
}

// kapFailure 按「kimiapp 错误码 → ErrorFamily」映射表分类：
//
//	40401 → session_unknown(false)
//	40101 / HTTP 401 → config(false)
//	40001 → config(false)
//	40901 → transient_upstream(true)
//	50001 / 5xx / 传输错 → transient_upstream(true)
//	其余 → internal(false)
//
// ctx 取消优先级高于一切错误分类，由调用方（Execute）在分类前判定。
func kapFailure(kerr *kapError) *rt.Failure {
	if kerr == nil {
		return nil
	}
	switch {
	case kerr.Code == codeSessionNotFound:
		return &rt.Failure{Family: rt.FamilySessionUnknown, Code: "session_not_found",
			Message: kerr.Message, Retryable: false}
	case kerr.Code == codeAuthFailed || kerr.Status == 401:
		return &rt.Failure{Family: rt.FamilyConfig, Code: "auth_failed",
			Message: kerr.Message, Retryable: false}
	case kerr.Code == codeValidationFailed:
		return &rt.Failure{Family: rt.FamilyConfig, Code: "validation_failed",
			Message: kerr.Message, Retryable: false}
	case kerr.Code == codeSessionBusy:
		return &rt.Failure{Family: rt.FamilyTransientUpstream, Code: "session_busy",
			Message: kerr.Message, Retryable: true}
	case kerr.Code == codeInternalError || kerr.Transport || kerr.Status >= 500:
		return &rt.Failure{Family: rt.FamilyTransientUpstream, Code: "upstream_unavailable",
			Message: kerr.Message, Retryable: true}
	default:
		return &rt.Failure{Family: rt.FamilyInternal, Code: "kap_error",
			Message: kerr.Message, Retryable: false}
	}
}

// turnEndFailure 将 turn.ended.error（KimiErrorPayload）映射为 Failure：
// provider 配额/限流 → provider_quota；auth → config；连接/过载/api 类 →
// transient_upstream；默认按 payload.retryable（缺省可重试）。
func turnEndFailure(ev *evError) *rt.Failure {
	if ev == nil {
		return nil
	}
	code := ev.Code
	if code == "" {
		code = "turn_failed"
	}
	retryable := true
	if ev.Retryable != nil {
		retryable = *ev.Retryable
	}
	switch {
	case containsAny(code, "rate_limit", "quota", "429"):
		return &rt.Failure{Family: rt.FamilyProviderQuota, Code: code, Message: ev.Message, Retryable: false}
	case containsAny(code, "auth", "api_key", "credential"):
		return &rt.Failure{Family: rt.FamilyConfig, Code: code, Message: ev.Message, Retryable: false}
	case containsAny(code, "connection", "overloaded", "api_error", "provider"):
		return &rt.Failure{Family: rt.FamilyTransientUpstream, Code: code, Message: ev.Message, Retryable: retryable}
	default:
		return &rt.Failure{Family: rt.FamilyTransientUpstream, Code: code, Message: ev.Message, Retryable: retryable}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
