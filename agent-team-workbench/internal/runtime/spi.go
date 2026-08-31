// Package runtime — Adapter SPI v2（Paperclip 式阻塞 Execute + 回调 + 结构化结果）。
// v2 唯一执行轨：ModuleRunner 驱动状态机，旧 Start/Handle 接口已删除。
package runtime

import (
	"context"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ── 执行结果 ─────────────────────────────────────────────────────────

// Outcome 是一次 Execute 的权威终态；状态机映射由 ModuleRunner 完成，adapter 不得直写状态。
type Outcome string

const (
	OutcomeSucceeded   Outcome = "succeeded"
	OutcomeFailed      Outcome = "failed"
	OutcomeTimedOut    Outcome = "timed_out"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeInterrupted Outcome = "interrupted"
)

// ErrorFamily 错误分类族，驱动重试策略与会话自愈（对齐 Paperclip errorFamily）。
type ErrorFamily string

const (
	FamilyTransientUpstream ErrorFamily = "transient_upstream"
	FamilyProviderQuota     ErrorFamily = "provider_quota"
	FamilyConfig            ErrorFamily = "config"
	FamilyIO                ErrorFamily = "io"
	FamilySessionUnknown    ErrorFamily = "session_unknown"
	FamilyTimeout           ErrorFamily = "timeout"
	FamilyInternal          ErrorFamily = "internal"
)

// UsageBasis 声明用量口径：per_run 为本轮增量；session_cumulative 为会话累计（消费方做差量）。
type UsageBasis string

const (
	UsagePerRun            UsageBasis = "per_run"
	UsageSessionCumulative UsageBasis = "session_cumulative"
)

type Usage struct {
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CachedTokens int64      `json:"cached_tokens"`
	Basis        UsageBasis `json:"basis"`
}

// Failure 结构化失败；Code 保持 adapter 粒度，Family 用于跨 adapter 统一策略。
type Failure struct {
	Family         ErrorFamily `json:"family"`
	Code           string      `json:"code"`
	Message        string      `json:"message"`
	Retryable      bool        `json:"retryable"`
	RetryNotBefore time.Time   `json:"retry_not_before,omitempty"`
}

// SessionUpdate adapter 对会话层的输出；Ref 同时是 execution_runs.session_ref 的来源。
type SessionUpdate struct {
	Ref         string         `json:"ref,omitempty"` // 如 claude://<id>、dsh://<id>
	Params      map[string]any `json:"params,omitempty"`
	DisplayID   string         `json:"display_id,omitempty"`
	Clear       bool           `json:"clear,omitempty"`
	ClearReason string         `json:"clear_reason,omitempty"`
}

// ExecResult 是 Execute 的同步返回；事件流已经通过 Callbacks 上报完毕。
type ExecResult struct {
	Outcome Outcome
	Failure *Failure
	Usage   *Usage
	Session *SessionUpdate
}

// ── 执行上下文 ───────────────────────────────────────────────────────

// SessionState 是编排层给出的 resume 决策输入。
// adapter 用 SessionIDFromRef(Ref, scheme) 自解析 provider 会话 id，无需冗余字段。
type SessionState struct {
	Ref         string `json:"ref"`                   // 原始 session_ref（如 dsh://atw_x）；空表示新会话
	Fingerprint string `json:"fingerprint,omitempty"` // 配置指纹（config digest）
}

// ControlKind 运行期控制命令：中断/取消（终态意图）、steering 输入、审批决定。
type ControlKind string

const (
	ControlInterrupt ControlKind = "interrupt"
	ControlCancel    ControlKind = "cancel"
	ControlInput     ControlKind = "input"
	ControlApproval  ControlKind = "approval"
)

type Control struct {
	Kind        ControlKind
	Instruction string // ControlInput
	ApprovalID  string // ControlApproval
	Approved    bool   // ControlApproval
}

// Callbacks 是 adapter 上报执行进展的唯一通道；实现必须非阻塞或快速返回。
type Callbacks interface {
	// OnEvent 追加 Run 域 canonical 事件（message.*/tool.*/run.* 等，白名单校验在实现侧）。
	OnEvent(eventType string, data map[string]any)
	OnProgress(progress float64)
	// OnLog 记录进程原始输出行（stdout/stderr），供调试与日志页。
	OnLog(stream, line string)
	// OnSpawn 上报进程 pid 与进程组 id（可靠 kill 的前提）。
	OnSpawn(pid, processGroupID int)
	// OnUsage 增量用量（最终以 ExecResult.Usage 为权威）。
	OnUsage(u Usage)
	// OnSession 运行中即拿到会话句柄时提前上报（崩溃也不丢 resume 时机）。
	OnSession(update SessionUpdate)
	// RequestApproval 发起审批（Run 进入 waiting_approval）；决定经 Controls 送达。
	RequestApproval(kind, risk, summary string) string
}

// ExecContext 是一次 Run 执行的完整输入；adapter 不得越界直写存储。
type ExecContext struct {
	// Ctx 在 interrupt/cancel/服务关停时被取消；adapter 必须借此终止底层进程。
	Ctx context.Context
	Run *domain.ExecutionRun
	// Execution 是本 Run 的不可变执行上下文快照（持久身份，无宿主路径）。
	Execution domain.ExecutionContextSnapshot
	// Resolved 是 Host resolver 的进程内可信产物；adapter 只许用 Resolved.CWD
	// 定位工作目录，禁止读取全局/构造期 WorkspaceRoot（架构 RFC §5.1.9）。
	Resolved domain.ResolvedExecutionContext
	// Instruction 已由编排层应用会话策略（resume 只发当轮；fresh 可能内联历史）。
	Instruction string
	// Session resume 决策；Ref 为空表示新会话。
	Session SessionState
	// Callbacks 事件与状态上报。
	Callbacks Callbacks
	// Controls 运行期控制流；adapter 按声明的能力消费（input/approval），终态意图同时伴随 Ctx 取消。
	Controls <-chan Control

	intent intentSource
}

// intentSource 暴露终态意图（interrupt/cancel），adapter 结束时据此选择 Outcome。
type intentSource interface {
	terminalIntent() (ControlKind, bool)
}

// TerminalIntent 返回已下达的终态意图；adapter 在 Ctx 取消后据此返回
// OutcomeInterrupted / OutcomeCancelled（无意图时为 "", false）。
func (e *ExecContext) TerminalIntent() (ControlKind, bool) {
	if e.intent == nil {
		return "", false
	}
	return e.intent.terminalIntent()
}

// ── 模块接口 ─────────────────────────────────────────────────────────

// AdapterModule v2 SPI：Execute 阻塞到本轮结束；能力必须在 Manifest 声明，不静默降级。
type AdapterModule interface {
	Manifest(ctx context.Context) (AdapterManifest, error)
	Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error)
	// Execute 阻塞执行一轮；结果只经 ExecResult 与 Callbacks 表达。
	Execute(ex *ExecContext) ExecResult
}

// SteererModule 可选：声明支持运行期 steering 的模块（Manifest Capabilities["steering"]）。
// 不实现的模块，ModuleRunner.ForwardInput 返回 ErrCapabilityMissing。

// EngineSink 是 ModuleRunner 需要的应用层能力（application.Service 实现）；
// adapter 包只依赖本包，不再反向依赖 application。
type EngineSink interface {
	RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error
	RecordRunProgress(ctx context.Context, runID string, progress float64) error
	RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error
	// RecordRunSessionUpdate 持久化会话句柄与 task_sessions 参数（Clear 时清会话）。
	RecordRunSessionUpdate(ctx context.Context, runID string, update SessionUpdate) error
	// RecordRunUsage 记录本轮用量（execution_runs.usage_*）。
	RecordRunUsage(ctx context.Context, runID string, usage Usage) error
	RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error)
	Run(ctx context.Context, id string) (*domain.ExecutionRun, error)
}
