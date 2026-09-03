// journal.go — Run Journal 相位埋点的 adapter 侧发射器（设计
// notes/proposed/architecture/2026-09-02-run-journal-lifecycle-logging.md）。
//
// adapter 侧没有 Journal 实例（回调通道不持有 RecordFunc）：相位事件用
// observability 纯函数构造载荷后经 Callbacks.OnEvent 直发，internal 分道
// （只落 run_events，不进 SSE/回放）由存储层按事件类型完成。观测绝不打断
// 业务路径：发射没有返回值，载荷构造失败（未知环节名，编程错误）静默丢弃。
package codexapp

import (
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// emitPhase 经 OnEvent 直发 run.phase_* internal 事件。
func emitPhase(ex *runtime.ExecContext, eventType string, data map[string]any) {
	if ex == nil || ex.Callbacks == nil || data == nil {
		return
	}
	ex.Callbacks.OnEvent(eventType, data)
}

// enterPhase 发 run.phase_entered{phase, attempt, detail}。
func enterPhase(ex *runtime.ExecContext, phase string, attempt int, detail map[string]any) {
	emitPhase(ex, domain.EventRunPhaseEntered, observability.PhaseEnteredPayload(phase, attempt, detail))
}

// closePhase 发 run.phase_closed{phase, outcome, failure?, duration_ms, detail?}。
func closePhase(ex *runtime.ExecContext, phase string, outcome observability.PhaseOutcome, failure *runtime.Failure, started time.Time, detail map[string]any) {
	emitPhase(ex, domain.EventRunPhaseClosed, observability.PhaseClosedPayload(
		phase, outcome, phaseFailure(failure), msSince(started), detail))
}

// phaseFailure runtime.Failure → journal PhaseFailure：既有错误分类（code/
// family/retryable）原样随相位事件可读，resume 死锚点等判定证据不换词表。
func phaseFailure(f *runtime.Failure) *observability.PhaseFailure {
	if f == nil {
		return nil
	}
	return &observability.PhaseFailure{
		Code: f.Code, Message: f.Message, Family: string(f.Family), Retryable: f.Retryable,
	}
}

func msSince(t time.Time) int64 { return time.Since(t).Milliseconds() }
