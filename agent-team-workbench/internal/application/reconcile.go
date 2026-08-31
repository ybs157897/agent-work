// reconcile.go 启动对账（M4 wakeup 调度簇）：control-plane 进程重启后，
// 上一进程遗留的「无 lease 且非终态」run（进程内模块执行，随进程消亡）会被
// 活跃 run 判定视为 alive，导致该 (agent, task_key) 的后续唤醒被永久 coalesce
// 进死 run。ReconcileOrphanRuns 在启动时把它们收敛到终态：
//   - 能沿状态机合法走到 lost 的（queued/starting/running/waiting_approval/
//     reconnecting）→ lost（保留 ResumeRun 的重建语义）；
//   - 走不到 lost 的过渡态（interrupting/cancelling/succeeding 只能到各自终态
//     或 failed）→ failed（code=orphaned_after_restart，可重试）。
//
// runner 路径的 run 必有 run_leases 行，不会命中本查询——它们的失联判定
// 仍由 runnergateway sweeper（断连 → reconnecting → lease 过期 → lost）负责。
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// allRunStatuses 全量 run 状态（BFS 邻接枚举用；边合法性由 domain 状态机判定）。
var allRunStatuses = []domain.RunStatus{
	domain.RunQueued, domain.RunStarting, domain.RunRunning, domain.RunWaitingApproval,
	domain.RunInterrupting, domain.RunCancelling, domain.RunReconnecting, domain.RunSucceeding,
	domain.RunSucceeded, domain.RunInterrupted, domain.RunCancelled, domain.RunLost, domain.RunFailed,
}

// lostPath 返回 from → lost 的合法状态机最短路径（含 lost 终点；nil = 不可达，
// 调用方应回退 failed）。中间态只用于内存状态机行走，不落库、不发事件。
func lostPath(from domain.RunStatus) []domain.RunStatus {
	type node struct {
		status domain.RunStatus
		path   []domain.RunStatus
	}
	queue := []node{{status: from}}
	seen := map[domain.RunStatus]bool{from: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.status == domain.RunLost {
			return cur.path
		}
		if cur.status.IsTerminal() {
			continue
		}
		for _, next := range allRunStatuses {
			if seen[next] || next.IsTerminal() && next != domain.RunLost {
				continue
			}
			if !cur.status.CanTransitionTo(next) {
				continue
			}
			seen[next] = true
			queue = append(queue, node{status: next, path: append(append([]domain.RunStatus{}, cur.path...), next)})
		}
	}
	return nil
}

// ReconcileOrphanRuns 把「无 lease 且非终态」的孤儿 run 收敛到终态（lost 优先，
// 不可达用 failed），状态迁移 + run.lost/run.failed 事件 + outbox 同事务提交。
// 返回收敛数量；单条失败不中断后续（返回首个错误）。
func (s *Service) ReconcileOrphanRuns(ctx context.Context) (int, error) {
	orphans, err := s.store.Runs().LeaselessActive(ctx)
	if err != nil {
		return 0, err
	}
	marked := 0
	var firstErr error
	notified := map[string]bool{}
	for _, run := range orphans {
		if err := s.markOrphanTerminal(ctx, run.ID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reconcile run %s: %w", run.ID, err)
			}
			continue
		}
		marked++
		notified[run.WorkspaceID] = true
		if reconciled, getErr := s.store.Runs().Get(context.WithoutCancel(ctx), run.ID); getErr == nil {
			// Reconciliation commits the terminal Run first, then replays the
			// same idempotent terminal projections used by live status reports.
			// This lets a coordinated Worker enter retry/replan instead of
			// remaining a running observation after a control-plane restart.
			s.replayCoordinatorTerminalHooks(context.WithoutCancel(ctx), reconciled)
		}
	}
	for ws := range notified {
		s.notifier.Notify(ws)
	}
	return marked, firstErr
}

// ReconcileLegacyContextRuns 收敛 0021 前遗留的非终态无快照 Run（RFC §6.1 启动
// 对账）：落 failed(execution_context_missing, retryable=true) 并复用终态钩子
// 触发 Coordinator 创建带新鲜快照的新 Run 恢复。历史数据禁止 resume 旧轨——
// 无 Snapshot 的 Run 永不进入 Dispatcher（不变式 §5.1.3/§5.1.4）。返回收敛数量。
func (s *Service) ReconcileLegacyContextRuns(ctx context.Context) (int, error) {
	orphans, err := s.store.Runs().LeaselessActive(ctx)
	if err != nil {
		return 0, err
	}
	marked := 0
	var firstErr error
	notified := map[string]bool{}
	for _, run := range orphans {
		if _, snapErr := s.store.ContextSnapshots().GetByRun(ctx, run.ID); snapErr == nil {
			continue // 已有快照：正常 Run，交由 ReconcileOrphanRuns 处理
		} else if !errors.Is(snapErr, domain.ErrNotFound) {
			if firstErr == nil {
				firstErr = snapErr
			}
			continue
		}
		if err := s.markLegacyContextMissingTerminal(ctx, run.ID); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reconcile legacy run %s: %w", run.ID, err)
			}
			continue
		}
		marked++
		notified[run.WorkspaceID] = true
		if reconciled, getErr := s.store.Runs().Get(context.WithoutCancel(ctx), run.ID); getErr == nil {
			s.replayCoordinatorTerminalHooks(context.WithoutCancel(ctx), reconciled)
		}
	}
	for ws := range notified {
		s.notifier.Notify(ws)
	}
	return marked, firstErr
}

// markLegacyContextMissingTerminal 把无快照的遗留 Run 直接收敛到 failed
// （所有非终态对 failed 均为合法迁移），同事务发事件与 activity。
func (s *Service) markLegacyContextMissingTerminal(ctx context.Context, runID string) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
		if err != nil {
			return err
		}
		if err := requireValidWorkItemRecordKind(wi); err != nil {
			return err
		}
		if r.Status.IsTerminal() {
			return nil
		}
		expected := r.Version
		from := r.Status
		now := time.Now().UTC()
		if err := r.MarkFailed(domain.RunFailure{
			Code:      "execution_context_missing",
			Retryable: true,
			Message:   "启动对账：0021 前遗留 Run 无执行上下文快照，不可分派（Coordinator 以新 Run 恢复）",
		}, now); err != nil {
			return err
		}
		if err := s.store.Runs().Update(ctx, r, expected); err != nil {
			return err
		}
		r.Version = expected + 1
		data := map[string]any{
			"code": "execution_context_missing", "retryable": true,
			"record_kind": string(workItemRecordKind(wi)),
		}
		if err := s.emit(ctx, r.WorkspaceID, domain.EventRunFailed,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunFailed, Payload: data},
			map[string]any{"from": string(from), "status": string(domain.RunFailed),
				"record_kind": string(workItemRecordKind(wi))}); err != nil {
			return err
		}
		if r.AgentProfileID != "" {
			_ = s.store.Agents().SetPresence(ctx, r.AgentProfileID, domain.PresenceIdle)
		}
		return s.activityFor(ctx, r.WorkspaceID, r.WorkItemID, "run.reconciled",
			fmt.Sprintf("启动对账：遗留 run %s（%s，无上下文快照）收敛为 failed(execution_context_missing)", r.ID, from))
	})
}

// markOrphanTerminal 在单事务内把一个孤儿 run 迁移到 lost（或 failed）并发出
// 对应终态事件；已并发到达终态的 run 幂等跳过。
func (s *Service) markOrphanTerminal(ctx context.Context, runID string) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
		if err != nil {
			return err
		}
		if err := requireValidWorkItemRecordKind(wi); err != nil {
			return err
		}
		if r.Status.IsTerminal() {
			return nil // 并发方已收尾
		}
		expected := r.Version
		from := r.Status
		now := time.Now().UTC()

		target := domain.RunFailed
		if path := lostPath(r.Status); path != nil {
			target = domain.RunLost
			for _, step := range path {
				if err := r.Transition(step, now); err != nil {
					return err
				}
			}
		} else {
			if err := r.MarkFailed(domain.RunFailure{
				Code: "orphaned_after_restart", Retryable: true,
				Message: "控制平面重启对账：无租约非终态 run 判定失败（可重试）",
			}, now); err != nil {
				return err
			}
		}
		if err := s.store.Runs().Update(ctx, r, expected); err != nil {
			return err
		}
		// Update 的 SQL 固定 version=version+1，与内存多跳 Transition 解耦后对齐。
		r.Version = expected + 1

		evType := domain.EventRunFailed
		data := map[string]any{
			"code":      "orphaned_after_restart",
			"message":   "控制平面重启对账：无租约非终态 run 判定失败（可重试）",
			"retryable": true,
		}
		if target == domain.RunLost {
			evType = domain.EventRunLost
			data = map[string]any{
				"reason":  "startup_reconcile",
				"message": "控制平面重启对账：无租约非终态 run 判定 lost（可 resume 重建）",
			}
		}
		data["record_kind"] = string(workItemRecordKind(wi))
		if err := s.emit(ctx, r.WorkspaceID, evType,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: evType, Payload: data},
			map[string]any{"from": string(from), "status": string(target),
				"record_kind": string(workItemRecordKind(wi))}); err != nil {
			return err
		}
		// presence 投影对齐 transitionRunLocked：终态回 idle。
		if r.AgentProfileID != "" {
			_ = s.store.Agents().SetPresence(ctx, r.AgentProfileID, domain.PresenceIdle)
		}
		return s.activityFor(ctx, r.WorkspaceID, r.WorkItemID, "run.reconciled",
			fmt.Sprintf("启动对账：run %s 由 %s 收敛到 %s（进程重启孤儿）", r.ID, from, target))
	})
}
