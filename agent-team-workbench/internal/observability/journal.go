// Package observability 提供 Run Journal（全环节日志）的载荷构造与日志预算。
// 设计：notes/proposed/architecture/2026-09-02-run-journal-lifecycle-logging.md。
//
// Journal 不新开写库路径：所有事件经 RecordFunc（生产实现 =
// application.Service.RecordRunEvent）汇入既有 emit → EventRepo.Append 单点，
// internal 分流在存储层按事件类型完成（只落 run_events，不进 SSE/回放）。
package observability

import (
	"context"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Run 生命周期环节词表（七段）。顺序即因果链；新增环节只允许追加，
// 已有名字不得改语义——定位 playbook 依赖稳定词表。
const (
	PhaseDispatch   = "dispatch"    // 入队 / 选 runner / lease 授予
	PhaseSpawn      = "spawn"       // 子进程拉起（pid/pgid）
	PhaseHandshake  = "handshake"   // initialize 握手 + thread start/resume 探测
	PhaseFirstEvent = "first_event" // 等待首个回调（markRunning）
	PhaseStreaming  = "streaming"   // 主对话流（message/tool 事件区间）
	PhaseSettle     = "settle"      // 终态裁决 + 状态迁移 + usage/session 落账
	PhasePost       = "post"        // 终态钩子管线
)

var runPhases = map[string]struct{}{
	PhaseDispatch: {}, PhaseSpawn: {}, PhaseHandshake: {}, PhaseFirstEvent: {},
	PhaseStreaming: {}, PhaseSettle: {}, PhasePost: {},
}

// IsRunPhase 校验环节名是否在词表内。
func IsRunPhase(phase string) bool {
	_, ok := runPhases[phase]
	return ok
}

// PhaseOutcome 是环节收口的三种结局。
type PhaseOutcome string

const (
	PhaseOK      PhaseOutcome = "ok"
	PhaseFailed  PhaseOutcome = "failed"
	PhaseSkipped PhaseOutcome = "skipped"
)

// PhaseFailure 是 phase_closed.data.failure 的形状，与 run 终态 failure
// 家族（code/message/family/retryable）对齐，便于同一套分类码读两条链。
type PhaseFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Family    string `json:"family,omitempty"`
	Retryable bool   `json:"retryable"`
}

// RecordFunc 是 Journal 的唯一写出依赖；生产环境即 Service.RecordRunEvent。
type RecordFunc func(ctx context.Context, runID, eventType string, data map[string]any) error

// PhaseEnteredPayload 构造 run.phase_entered 的 data（纯函数版，供 adapter 侧
// 经 Callbacks.OnEvent 直发——回调通道没有 Journal；应用层请用 Journal.EnterPhase）。
// phase 必须在词表内，否则返回 nil（调用方应视为编程错误）。
func PhaseEnteredPayload(phase string, attempt int, detail map[string]any) map[string]any {
	if !IsRunPhase(phase) {
		return nil
	}
	data := map[string]any{"phase": phase, "attempt": attempt}
	for k, v := range detail {
		data[k] = v
	}
	return data
}

// PhaseClosedPayload 构造 run.phase_closed 的 data（纯函数版，约束同上）。
func PhaseClosedPayload(phase string, outcome PhaseOutcome, failure *PhaseFailure, durationMS int64, detail map[string]any) map[string]any {
	if !IsRunPhase(phase) {
		return nil
	}
	data := map[string]any{
		"phase":       phase,
		"outcome":     string(outcome),
		"duration_ms": durationMS,
	}
	if failure != nil {
		data["failure"] = map[string]any{
			"code": failure.Code, "message": failure.Message,
			"family": failure.Family, "retryable": failure.Retryable,
		}
	}
	for k, v := range detail {
		data[k] = v
	}
	return data
}

// LogChunkPayload 构造 run.log_chunk 的 data。
func LogChunkPayload(stream, chunk string, truncated bool) map[string]any {
	return map[string]any{"stream": stream, "chunk": chunk, "truncated": truncated}
}

// Decision 决策类别词表（非治理域；治理决策由 turn_receipt phase1 承担）。
const (
	DecisionSelfHealRetry      = "self_heal_retry"     // session_unknown 触发的 fresh 自愈重试
	DecisionCancelForward      = "cancel_forward"      // 取消意图前转到执行端
	DecisionCoordinatorRedrive = "coordinator_redrive" // 普通（非受管）coordinator 重驱
	DecisionRecoverySweep      = "recovery_sweep"      // 重启对账 sweeper 的合成收口
)

// DecisionPayload 构造 run.decision 的 data：kind（上表）+ reason + inputs
// （关键输入证据，如 failure_code/session_ref/lease_id）+ 可选 link_run_id
// （跨 run 因果：原 run → 新 run）。
func DecisionPayload(kind, reason string, inputs map[string]any, linkRunID string) map[string]any {
	data := map[string]any{"kind": kind, "reason": reason}
	for k, v := range inputs {
		data[k] = v
	}
	if linkRunID != "" {
		data["link_run_id"] = linkRunID
	}
	return data
}

// Decision 落一条 run.decision（决策因果链锚点）。
func (j *Journal) Decision(ctx context.Context, runID, kind, reason string, inputs map[string]any, linkRunID string) error {
	return j.recordSafe(ctx, runID, domain.EventRunDecision, DecisionPayload(kind, reason, inputs, linkRunID))
}

// Journal 是 run 环节事件的薄封装：负责载荷形状与成对语义，不持有状态。
type Journal struct {
	record RecordFunc
	// now 可注入假钟（测试）；nil 用真实时钟。
	now func() time.Time
}

// NewJournal 构造 Journal；record 为 nil 时所有方法静默丢弃（观测绝不能
// 反过来打断业务路径）。
func NewJournal(record RecordFunc) *Journal {
	return &Journal{record: record, now: time.Now}
}

// PhaseTimer 是一次环节进入的句柄；Close 时自动补 duration_ms。
// 用法：t := j.EnterPhase(...); defer/事后 t.Close(outcome, failure, detail)。
type PhaseTimer struct {
	j       *Journal
	ctx     context.Context
	runID   string
	phase   string
	started time.Time
}

// EnterPhase 记录 run.phase_entered{phase, attempt, detail} 并返回收口句柄。
// phase 必须在词表内（否则响亮报错，防止埋点造出无法定位的野环节）；
// attempt 从 1 起，retry/自愈重跑同一环节时递增。
func (j *Journal) EnterPhase(ctx context.Context, runID, phase string, attempt int, detail map[string]any) (*PhaseTimer, error) {
	data := PhaseEnteredPayload(phase, attempt, detail)
	if data == nil {
		return nil, fmt.Errorf("%w: unknown run phase %q", domain.ErrValidation, phase)
	}
	if err := j.recordSafe(ctx, runID, domain.EventRunPhaseEntered, data); err != nil {
		return nil, err
	}
	return &PhaseTimer{j: j, ctx: ctx, runID: runID, phase: phase, started: j.now()}, nil
}

// Close 记录配对的 run.phase_closed{phase, outcome, duration_ms, failure?, detail?}。
// 重复 Close 只会产生重复事件（append-only 语义），调用方保证一次。
func (t *PhaseTimer) Close(outcome PhaseOutcome, failure *PhaseFailure, detail map[string]any) error {
	return t.j.recordSafe(t.ctx, t.runID, domain.EventRunPhaseClosed,
		PhaseClosedPayload(t.phase, outcome, failure, t.j.now().Sub(t.started).Milliseconds(), detail))
}

// LogChunkBudgetBytes 是 D3 拍板的每 run 原始输出落库上限（64KB）。
const LogChunkBudgetBytes = 64 << 10

// LogBudget 是每 run 的日志落库预算（append-only 下的"环形截断"实现：
// 预算内原样落库；首次超出截断到剩余额度并标记 truncated=true；
// 预算耗尽后不再落库）。进程重启计数归零是可接受偏差——64KB 是防爆阀，
// 不是精确配额。
type LogBudget struct {
	remaining int
}

// NewLogBudget 构造满额预算。
func NewLogBudget() *LogBudget { return &LogBudget{remaining: LogChunkBudgetBytes} }

// Take 判定本次 chunk 的落库形态。ok=false 表示预算已耗尽，调用方应停止上报。
func (b *LogBudget) Take(chunk string) (stored string, truncated bool, ok bool) {
	if b.remaining <= 0 {
		return "", false, false
	}
	if len(chunk) <= b.remaining {
		b.remaining -= len(chunk)
		return chunk, false, true
	}
	stored = chunk[:b.remaining]
	b.remaining = 0
	return stored, true, true
}

// LogLine 落一条进程原始输出（run.log_chunk{stream, chunk, truncated}）；
// 预算耗尽返回 nil（静默丢弃）——日志永远不成为失败源。
func (j *Journal) LogLine(ctx context.Context, runID, stream string, budget *LogBudget, line string) error {
	stored, truncated, ok := budget.Take(line)
	if !ok {
		return nil
	}
	return j.recordSafe(ctx, runID, domain.EventRunLogChunk, LogChunkPayload(stream, stored, truncated))
}

// recordSafe 在 record 缺失（未接线）时静默丢弃。
func (j *Journal) recordSafe(ctx context.Context, runID, eventType string, data map[string]any) error {
	if j == nil || j.record == nil {
		return nil
	}
	return j.record(ctx, runID, eventType, data)
}
