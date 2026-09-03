// run_reconcile.go 控制面重启对账 sweeper（Run Journal M2 闭合与证据，设计
// notes/proposed/architecture/2026-09-02-run-journal-lifecycle-logging.md §3.5）：
// control-plane 进程死亡时，host_local 的在飞 run 随进程消亡且无主（进程内
// 模块执行不持有 lease），重启后需要带证据合成收口并可重驱。
//
// 与 leaseSweeper（runnergateway）的分工——防双扫：
//   - 远程 run 必有 run_leases 行：失联判定归 runnergateway.leaseSweeper
//     （ExpireLeases 释放过期 lease → markExpiredLeaseTerminal 收口，M2 起带
//     recovery 证据）。LeaselessActive 只返回无任何 lease 行的 run，远程 run
//     不会命中本查询，两扫天然互斥。
//   - host_local run 无 lease 行：归本 sweeper，控制面重启时一次收口。
//   - 若某 run 恰好同时被两边处理（理论窗口），状态机迁移的合法性天然幂等：
//     非终态→终态只成功一次，重复迁移返回 TransitionError 即跳过（钉测试
//     TestReconcileOrphanedLocalRunsIdempotent）。
//
// 与通用孤儿对账 ReconcileOrphanRuns（reconcile.go，M4 wakeup 簇）的分工：
// 本 sweeper 只扫「活动非终态且非 queued」，在 main.go 装配序列里先于它执行，
// 非终态在飞 run 由这里带证据收口（失败码 control_plane_restart）；queued 是
// 合法待派发态（可能正等 RecoverPendingSelfHealRuns / Dispatcher 处理），
// 本 sweeper 不扫，仍由 ReconcileOrphanRuns 按既有语义处理。
package application

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/observability"
)

// bootAt 进程启动时刻（包初始化 ≈ 进程 boot；新进程接管前旧进程必已死亡），
// 作为合成闭合 detail 里的接管方时间证据。
var bootAt = time.Now().UTC()

// orphanSweepStatuses 是重启对账的孤儿状态精确集合：13 态状态机（domain/run.go）
// 中「活动非终态且非 queued」。终态天然排除；queued 是合法待派发，不扫。
var orphanSweepStatuses = map[domain.RunStatus]struct{}{
	domain.RunStarting: {}, domain.RunRunning: {}, domain.RunWaitingApproval: {},
	domain.RunInterrupting: {}, domain.RunCancelling: {}, domain.RunReconnecting: {},
	domain.RunSucceeding: {},
}

// controlPlaneRestartCode 是重启对账的统一失败码。lost 恒 retryable 是既有
// 语义（可 resume 重建 / coordinator due-state 循环自动重驱受管 run）。
const controlPlaneRestartCode = "control_plane_restart"

// ReconcileOrphanedLocalRuns 把「活动非终态且无 lease」的孤儿 run 带证据收敛
// 到终态（lost 优先，状态机不可达时 failed 兜底）。对每个孤儿：
//
//  1. 合成闭合最后一个未闭合相位（崩溃点 = 控制面死亡时刻的在飞环节）；
//  2. 沿状态机合法路径收口（running→reconnecting→lost 模式，RecordRunStatus
//     是唯一状态入口；终态钩子管线由此自动接管）；
//  3. 收口成功后发 run.recovery_completed（一次性同步完成，单条承载全程证据、
//     不成对——对账动作在本次调用内终结，不存在后续恢复窗口）；收口失败发
//     run.recovery_failed；
//  4. 发 run.decision{kind: recovery_sweep} 留因果锚点。
//
// 观测事件发射失败只 slog，绝不改变收口控制流；状态迁移失败仍是主错误路径
// （聚合返回首个错误，不中断后续 run）。返回收敛数量。
func (s *Service) ReconcileOrphanedLocalRuns(ctx context.Context) (int, error) {
	orphans, err := s.store.Runs().LeaselessActive(ctx)
	if err != nil {
		return 0, err
	}
	swept := 0
	var firstErr error
	for _, run := range orphans {
		if _, ok := orphanSweepStatuses[run.Status]; !ok {
			continue // queued：合法待派发，不扫
		}
		if err := s.sweepOrphanedLocalRun(ctx, run.ID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reconcile orphaned local run %s: %w", run.ID, err)
			}
			continue
		}
		swept++
	}
	return swept, firstErr
}

// sweepOrphanedLocalRun 收口单个孤儿 run；已并发到达终态的 run 幂等跳过。
func (s *Service) sweepOrphanedLocalRun(ctx context.Context, runID string) error {
	r, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return err
	}
	if r.Status.IsTerminal() {
		return nil // 并发方（如 leaseSweeper）已收尾：幂等跳过
	}
	from := r.Status
	evidence := map[string]any{
		"code":    controlPlaneRestartCode,
		"boot_at": bootAt.UTC().Format(time.RFC3339Nano),
	}
	// 合成闭合未闭合相位（收口前）：崩溃点证据。
	s.closeUnclosedPhaseForSweep(ctx, runID, evidence)

	terminal, err := s.walkOrphanToTerminal(ctx, runID, from)
	if err != nil {
		// 收口失败 = 恢复动作失败；发射失败只 slog。
		s.emitRecoveryEvent(ctx, runID, domain.EventRunRecoveryFailed, map[string]any{
			"code": controlPlaneRestartCode, "retryable": true,
			"message":         fmt.Sprintf("控制面重启对账收口失败: %v", err),
			"previous_status": string(from),
		})
		return err
	}
	// 收口成功：恢复动作一次性同步完成，completed 单发承载全程证据。
	s.emitRecoveryEvent(ctx, runID, domain.EventRunRecoveryCompleted, map[string]any{
		"code":            controlPlaneRestartCode,
		"previous_status": string(from),
		"terminal_status": string(terminal),
	})
	j := observability.NewJournal(s.RecordRunEvent)
	if err := j.Decision(ctx, runID, observability.DecisionRecoverySweep,
		"控制面重启：无 lease 在飞 run 合成收口（可重驱）",
		map[string]any{
			"previous_status": string(from), "terminal_status": string(terminal),
			"code": controlPlaneRestartCode,
		}, ""); err != nil {
		slog.Warn("run journal: 恢复对账 decision 落库失败",
			"run_id", runID, "code", controlPlaneRestartCode, "error", err)
	}
	return nil
}

// walkOrphanToTerminal 沿状态机合法路径把孤儿 run 收口到终态：lost 可达走
// running→reconnecting→lost 同构路径（lostPath 只做内存行走，落库经
// RecordRunStatus 唯一入口）；不可达（当前 13 态全部可达 lost，防御分支）落
// failed(control_plane_restart, retryable)。返回实际落到的终态。
func (s *Service) walkOrphanToTerminal(ctx context.Context, runID string, from domain.RunStatus) (domain.RunStatus, error) {
	if path := lostPath(from); path != nil {
		for _, step := range path {
			var data map[string]any
			if step == domain.RunLost {
				data = map[string]any{"reason": controlPlaneRestartCode}
			}
			if err := s.RecordRunStatus(ctx, runID, step, data); err != nil {
				return "", err
			}
		}
		return domain.RunLost, nil
	}
	if err := s.RecordRunStatus(ctx, runID, domain.RunFailed, map[string]any{
		"code": controlPlaneRestartCode, "retryable": true,
		"message": "控制面重启对账：无租约在飞 run 判定失败（可重试）",
	}); err != nil {
		return "", err
	}
	return domain.RunFailed, nil
}

// closeUnclosedPhaseForSweep 收口前合成闭合该 run 最后一个未闭合相位
// （有 phase_entered 无配对 phase_closed = 崩溃/卡死环节）；找不到就不发。
// journal 读取或发射失败只 slog，绝不改变收口控制流。
func (s *Service) closeUnclosedPhaseForSweep(ctx context.Context, runID string, evidence map[string]any) {
	events, err := s.store.Events().ListRunEventsIncludeInternal(ctx, runID)
	if err != nil {
		slog.Warn("run journal: 读 run journal 失败，跳过相位合成闭合",
			"run_id", runID, "code", controlPlaneRestartCode, "error", err)
		return
	}
	phase, enteredAt, ok := unclosedJournalPhase(events)
	if !ok {
		return
	}
	var durationMS int64
	if !enteredAt.IsZero() {
		durationMS = time.Since(enteredAt).Milliseconds()
	}
	data := observability.PhaseClosedPayload(phase, observability.PhaseFailed, &observability.PhaseFailure{
		Code: controlPlaneRestartCode, Message: "控制面重启：进程死亡时在飞相位强制闭合", Retryable: true,
	}, durationMS, evidence)
	if data == nil {
		return // 词表外相位：不发（Journal 埋点只发词表内相位，此为防御）
	}
	if err := s.RecordRunEvent(ctx, runID, domain.EventRunPhaseClosed, data); err != nil {
		slog.Warn("run journal: 相位合成闭合落库失败",
			"run_id", runID, "code", controlPlaneRestartCode, "phase", phase, "error", err)
	}
}

// emitRecoveryEvent 落一条 run.recovery_*（surface 事件，契约已声明、由
// runnergateway 恢复路径与本 sweeper 激活）。发射失败只 slog——观测绝不
// 改变对账控制流。
func (s *Service) emitRecoveryEvent(ctx context.Context, runID, evType string, data map[string]any) {
	if err := s.RecordRunEvent(ctx, runID, evType, data); err != nil {
		slog.Warn("run journal: 恢复事件落库失败",
			"run_id", runID, "event", evType, "code", data["code"], "error", err)
	}
}

// unclosedJournalPhase 从 run 全量事件（含 internal）里找最后一个有 entered
// 无配对 closed 的相位。events 必须按 run_seq 升序（ListRunEventsIncludeInternal
// 的既有语义）。与 runnergateway 恢复路径的同名逻辑同构——两包互不依赖，
// 各持一份（RunEvent 属 application，下沉会引入反向 import）。
func unclosedJournalPhase(events []RunEvent) (phase string, enteredAt time.Time, ok bool) {
	open := map[string]time.Time{}
	var order []string
	for _, ev := range events {
		switch ev.EventType {
		case domain.EventRunPhaseEntered:
			name, _ := ev.Payload["phase"].(string)
			if name == "" {
				continue
			}
			if _, seen := open[name]; !seen {
				order = append(order, name)
			}
			open[name] = ev.OccurredAt
		case domain.EventRunPhaseClosed:
			name, _ := ev.Payload["phase"].(string)
			delete(open, name)
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		if at, openNow := open[order[i]]; openNow {
			return order[i], at, true
		}
	}
	return "", time.Time{}, false
}
