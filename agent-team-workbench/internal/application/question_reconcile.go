// question_reconcile.go 原生提问的启动存量对账：修复上线前已落终态的 run 名下
// 仍 pending 的提问会被 ListPending 的终态过滤永久隐藏且永不回收（数据停在
// pending、API 永远查不到）。ReconcileStaleQuestions 在 control-plane 启动时把
// 这批僵尸数据收敛为 expired 并逐条发 question.expired，使存储状态与查询语义
// 一致。幂等可重复：无 stale 行时不动数据、不发事件；单 run 失败不中断后续。
// 终态转换的在线路径由 transitionRunLocked 的终态收敛钩子负责，两条路径共用
// expireRunPendingQuestions。
package application

import (
	"context"
	"fmt"
)

// ReconcileStaleQuestions 把「run 已终态但仍 pending」的原生提问收敛为 expired
// （同事务发 question.expired），返回收敛的提问数量。幂等：重复执行无 stale 行、
// 不产生重复事件。
func (s *Service) ReconcileStaleQuestions(ctx context.Context) (int, error) {
	stale, err := s.store.Questions().ListStalePending(ctx)
	if err != nil {
		return 0, err
	}
	runIDs := make([]string, 0, len(stale))
	seen := map[string]bool{}
	for _, q := range stale {
		if !seen[q.RunID] {
			seen[q.RunID] = true
			runIDs = append(runIDs, q.RunID)
		}
	}
	expired := 0
	var firstErr error
	notified := map[string]bool{}
	for _, runID := range runIDs {
		var wsID string
		n, err := func() (int, error) {
			var n int
			err := s.store.InTx(ctx, func(ctx context.Context) error {
				r, err := s.store.Runs().Get(ctx, runID)
				if err != nil {
					return err
				}
				if !r.Status.IsTerminal() {
					return nil // 并发方已推进；本清扫只处理终态 run 名下的提问
				}
				wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
				if err != nil {
					return err
				}
				wsID = r.WorkspaceID
				n, err = s.expireRunPendingQuestions(ctx, r, wi)
				return err
			})
			return n, err
		}()
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reconcile stale questions for run %s: %w", runID, err)
			}
			continue
		}
		expired += n
		if wsID != "" {
			notified[wsID] = true
		}
	}
	for ws := range notified {
		s.notifier.Notify(ws)
	}
	return expired, firstErr
}
