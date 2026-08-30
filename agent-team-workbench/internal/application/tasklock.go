// tasklock.go F1 任务级执行锁：获取/释放/死属主抢占的裁决与落库（同事务）。
// 一个 work item 至多被一个活跃 run 执行；属主活性复用 run 状态面（终态=死），
// 不引入第二套 lease 判定。锁字段不参与 version 乐观锁比较，但读写必须与
// run 状态变更同一事务，否则出现「状态过了但锁丢了」。
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ErrWorkItemLocked 任务执行锁由其他活跃（非终态）run 持有，本 run 不得推进到
// running；包装错误携带属主 run id（调用方据此把本 run 落 failed 终态）。
var ErrWorkItemLocked = errors.New("work item locked by active run")

// acquireTaskLock 在 run 首次推进到 running（queued/starting → running）的同一
// 事务内裁决并写任务执行锁：
//   - 任务无锁 → 获取（locked_by_run_id=run.ID, locked_at=now），发 work_item.locked；
//   - 锁由本 run 持有 → 幂等通过（waiting_approval/reconnecting 往返后再推进）；
//   - 锁属主 run 已终态 → 死锁抢占：覆写属主并记 activity，发 work_item.lock_preempted；
//   - 锁属主 run 活跃 → 返回 ErrWorkItemLocked（拒绝双跑，调用方落终态兜底）。
//
// 属主 run 行缺失（外键下不应发生）按死锁处理——残留引用不得永久卡死任务。
func (s *Service) acquireTaskLock(ctx context.Context, r *domain.ExecutionRun) error {
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		return err
	}
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return err
	}
	if !isTaskWorkItem(wi) {
		return nil
	}
	if wi.HoldsLock(r.ID) {
		return nil
	}
	preemptFrom := ""
	if wi.LockedByRunID != "" {
		owner, gerr := s.store.Runs().Get(ctx, wi.LockedByRunID)
		if gerr != nil && !errors.Is(gerr, domain.ErrNotFound) {
			return gerr
		}
		if gerr == nil && !owner.Status.IsTerminal() {
			return fmt.Errorf("%w: %s", ErrWorkItemLocked, wi.LockedByRunID)
		}
		preemptFrom = wi.LockedByRunID
	}
	now := time.Now().UTC()
	expected := wi.Version
	wi.LockedByRunID = r.ID
	wi.LockedAt = &now
	// 内存版本与 DB（version=version+1）保持同步（assignLocked 同一约定）。
	wi.Version++
	wi.UpdatedAt = now
	if err := s.store.WorkItems().Update(ctx, wi, expected); err != nil {
		return err
	}
	if preemptFrom != "" {
		if err := s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemLockPreempted,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil,
			map[string]any{"run_id": r.ID, "preempted_from": preemptFrom,
				"record_kind": string(workItemRecordKind(wi))}); err != nil {
			return err
		}
		return s.activityFor(ctx, wi.WorkspaceID, wi.ID, "work_item.lock_preempted",
			fmt.Sprintf("任务「%s」执行锁已被 run %s 抢占（原 run %s 已终态）", wi.Title, r.ID, preemptFrom))
	}
	return s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemLocked,
		domain.AggregateWorkItem, wi.ID, wi.Version, nil,
		map[string]any{"run_id": r.ID, "record_kind": string(workItemRecordKind(wi))})
}

// releaseTaskLock 在 run 落终态的同一事务内释放其持有的任务执行锁；锁已被
// 抢占/回收（属主不再是本 run）时不碰他人的锁。
func (s *Service) releaseTaskLock(ctx context.Context, r *domain.ExecutionRun) error {
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		return err
	}
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return err
	}
	if !isTaskWorkItem(wi) {
		return nil
	}
	if !wi.HoldsLock(r.ID) {
		return nil
	}
	now := time.Now().UTC()
	expected := wi.Version
	wi.LockedByRunID = ""
	wi.LockedAt = nil
	wi.Version++
	wi.UpdatedAt = now
	return s.store.WorkItems().Update(ctx, wi, expected)
}
