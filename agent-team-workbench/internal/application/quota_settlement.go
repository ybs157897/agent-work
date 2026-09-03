package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// usage-backed quota settlement sweep：受管 Run 终态后按 canonical usage 逐
// kind 落 spend 台账，并在关闭条件齐备时结算 reservation、追加 phase6。
// sweep 重放安全：spend 幂等键 (turn,kind,run)、reservation 非 reserved 跳过、
// phase6 已存在即短路。

// maybeSettleGovernanceTurnQuota 终态钩子（RecordRunStatus /
// replayRunTerminalHooks / replayCoordinatorTerminalHooks 复用）：受管 Run
// 终态后尝试关闭本 Turn 的 usage 台账。尽力而为：失败只记日志，重放安全。
// allowAbsentClose 恒为 false：进行式触发不得提前冻结 absent evidence
// （复审裁决 #4），关闭性收口只属于 StartCoordinator/admission/Todo 收口面。
func (s *Service) maybeSettleGovernanceTurnQuota(ctx context.Context, run *domain.ExecutionRun) {
	if run == nil {
		return
	}
	key, ok := runGovernanceTurnKey(run)
	if !ok {
		return
	}
	if err := s.settleGovernanceTurnQuota(ctx, key, false); err != nil {
		log.Printf("quota: turn %s:%s:%d settlement sweep 失败（等待重放）: %v",
			key.GoalID, key.TodoID, key.TurnSeq, err)
	}
}

// settleGovernanceTurnQuota 是 sweep 的共享入口（终态钩子、admission 重放、
// Todo 状态收口与 StartCoordinator 恢复面共用）：per-turn 锁 + 单事务，
// 保证同一 Turn 的关闭判定与关闭动作串行且原子。
// 锁数组独立于 governancePlanLocks：提交路径持 run.ID 锁时会嵌套调到本函数，
// 同数组桶碰撞会造成同 goroutine 自锁（已实测挂死）。
// allowAbsentClose（复审裁决 #4）：true 表示调用方是关闭性触发源，允许在其余
// 关闭条件齐备时为无 report 的受管 Run 合成 absent evidence 并落 unresolved
// spend 后收口；false 只做进行式结算，绝不合成、绝不提前关闭。
func (s *Service) settleGovernanceTurnQuota(ctx context.Context, key domain.TurnKey, allowAbsentClose bool) error {
	lock := &s.governanceQuotaLocks[governancePlanLockIndex(
		fmt.Sprintf("%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq))]
	lock.Lock()
	defer lock.Unlock()
	if err := s.store.InTx(ctx, func(txctx context.Context) error {
		return s.settleGovernanceTurnQuotaLocked(txctx, key, allowAbsentClose)
	}); err != nil {
		return err
	}
	return s.appendGovernanceProjectionPhaseIfReady(ctx, key)
}

// settleRejectedPlanDispatchTurn is the durable post-decision coordinator for a
// rejected plan_dispatch approval. The approval decision is its own durable
// fact; once committed, this function atomically closes that turn's quota and
// publishes the user-visible Todo/Coordinator blocker. A failure rolls back
// both halves, so the same rejected approval can safely retry the whole closure.
// Keep the lock and transaction here (rather than calling the two public
// helpers) so the two writes cannot leave a committed reservation beside an
// unblocked task, and so no nested quota lock can self-deadlock.
func (s *Service) settleRejectedPlanDispatchTurn(ctx context.Context, key domain.TurnKey) error {
	lock := &s.governanceQuotaLocks[governancePlanLockIndex(
		fmt.Sprintf("%s:%s:%d", key.GoalID, key.TodoID, key.TurnSeq))]
	lock.Lock()
	defer lock.Unlock()
	if err := s.store.InTx(ctx, func(txctx context.Context) error {
		if err := s.settleGovernanceTurnQuotaLocked(txctx, key, true); err != nil {
			return fmt.Errorf("rejected plan_dispatch quota settlement: %w", err)
		}
		if err := s.blockRejectedPlanDispatchTurnLocked(txctx, key); err != nil {
			return fmt.Errorf("rejected plan_dispatch blocker settlement: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}
	return s.appendGovernanceProjectionPhaseIfReady(ctx, key)
}

// settleGovernanceTurnQuotaLocked 在单事务内完成一个治理 Turn 的 usage 结算：
// 逐终态受管 Run 落 spend → 关闭判定 → 缺证据 Run 的 absent 合成 →
// reservation Commit/Release → phase6。
func (s *Service) settleGovernanceTurnQuotaLocked(ctx context.Context, key domain.TurnKey, allowAbsentClose bool) error {
	if _, err := s.store.TurnReceipts().GetPhase(ctx, key, 6); err == nil {
		return nil // phase6 已落：关闭幂等
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	// 收集本 Turn 的 usage-kind reservation；无 usage 政策的 Turn 交给 WP4-A
	// 路径管 phase6。
	reservations := make([]*domain.QuotaReservation, 0, len(usageQuotaKinds))
	for _, kind := range usageQuotaKinds {
		reservation, err := s.store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: key, Kind: kind})
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		reservations = append(reservations, reservation)
	}
	if len(reservations) == 0 {
		return nil
	}
	goal, err := s.store.Goals().Get(ctx, key.GoalID)
	if err != nil {
		return err
	}
	// 受管 Run 集合 = receipt phase1 引用的 source Run + governance 身份命中的
	// 全部 Run（天然覆盖 plan 派发、evaluation、retry/heal 克隆）。
	phase1, err := s.store.TurnReceipts().GetPhase(ctx, key, 1)
	sourceRunID := ""
	if errors.Is(err, domain.ErrNotFound) {
		// A cancellation checkpoint may outlive a process crash immediately
		// after admission, before the decision_decode phase was appended. The
		// immutable Header still names the governed source Run; use it only for
		// the terminal cancellation cleanup and keep ordinary settlement open.
		if goal.Status != domain.GoalCancelled {
			return nil
		}
		header, headerErr := s.store.TurnReceipts().GetHeader(ctx, key)
		if headerErr != nil {
			return headerErr
		}
		sourceRunID = header.GovernedSourceRunID
		if sourceRunID == "" {
			return nil
		}
	} else if err != nil {
		return err
	} else {
		sourceRunID, _ = phase1.Payload["source_run_id"].(string)
	}
	// 结算顺序必须确定性：容量先到先得依赖固定次序（source Run 最先创建，
	// 其余按 created_at 升序），重放与并发 sweep 才产出同一台账。
	turnRuns, err := s.store.Runs().ListByGovernanceTurn(ctx, goal.WorkspaceID, key.GoalID, key.TodoID, key.TurnSeq)
	if err != nil {
		return err
	}
	ordered := make([]string, 0, len(turnRuns)+1)
	if sourceRunID != "" {
		ordered = append(ordered, sourceRunID)
	}
	for _, turnRun := range turnRuns {
		if turnRun == nil || turnRun.ID == sourceRunID {
			continue
		}
		ordered = append(ordered, turnRun.ID)
	}
	// 本 Turn 台账快照：容量预检与 committed 聚合同源；sweep 内追加的 entry
	// 同步并入，同事务内可见。
	spend, err := s.store.Quotas().ListSpendByTurn(ctx, key)
	if err != nil {
		return err
	}
	// 进行式 pass（复审裁决 #4）：有 report/canonical 的 Run 照常结算 spend；
	// 无 report 的受管 Run 不在此合成 absent canonical——canonical 保持缺失，
	// 迟到 report 随时可正常首写真实用量进 committed，其 spend 推迟到关闭
	// 时刻与 absent 证据同事务落账。
	allTerminal, allSettled := true, true
	var absentPending []string
	for _, runID := range ordered {
		run, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		if !run.Status.IsTerminal() {
			allTerminal = false
			continue
		}
		if _, err := s.canonicalizeRunUsageLocked(ctx, run.ID, false); err != nil {
			return err // 整事务回滚，下一轮重放
		}
		// canonicalize 可能刚写过该 Run：重读拿到最新 canonical。
		if run, err = s.store.Runs().Get(ctx, run.ID); err != nil {
			return err
		}
		if run.CanonicalUsage == nil {
			// 缺证据 Run：登记待补，不在进行式 pass 当场合成。
			absentPending = append(absentPending, run.ID)
			continue
		}
		for _, reservation := range reservations {
			if _, err := s.store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
				TurnKey: key, Kind: reservation.Key.Kind, RunID: run.ID,
			}); err == nil {
				continue
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			var settled bool
			spend, settled, err = s.appendTurnRunSpendLocked(ctx, goal.WorkspaceID, key, run, reservation, spend)
			if err != nil {
				return err
			}
			if !settled {
				allSettled = false
			}
		}
	}
	if !allTerminal || !allSettled {
		return nil
	}
	// 关闭条件（复审裁决 #4）：存在缺证据 Run 时，仅关闭性触发源
	//（allowAbsentClose=true）可以收口；终态钩子的进行式触发不得提前冻结
	// absent evidence——否则迟到 report 永远被拒、真实用量永久丢失。
	if len(absentPending) > 0 && !allowAbsentClose {
		return nil
	}
	// 关闭判定：phase5 已落，或 Todo 已在本 Turn 上关闭。
	_, phase5Err := s.store.TurnReceipts().GetPhase(ctx, key, 5)
	if phase5Err != nil && !errors.Is(phase5Err, domain.ErrNotFound) {
		return phase5Err
	}
	if phase5Err != nil {
		todo, todoErr := s.store.Todos().Get(ctx, key.TodoID)
		if todoErr != nil {
			if errors.Is(todoErr, domain.ErrNotFound) {
				return nil
			}
			return todoErr
		}
		closed := todo.LastTurnSeq == key.TurnSeq && (todo.Status == domain.TodoBlocked ||
			todo.Status == domain.TodoCancelled || todo.Status == domain.TodoCompleted)
		if !closed {
			return nil
		}
	}
	// P1-2：本 Turn 仍处 active/waiting 且有 pending 步（如
	// approval_policy=manual 的 dispatch 在审批前不建 Run、无 ResultRunID）时不
	// 关闭；终态 Plan 的历史 pending 步已失去执行权，不应继续阻塞结算。
	plan, err := s.store.Plans().GetByClientKey(ctx, goal.WorkspaceID, governancePlanClientKey(key))
	if err != nil {
		return err
	}
	if plan != nil && !plan.Status.IsTerminal() {
		for _, step := range plan.Steps {
			if step.Status == domain.PlanStepPending {
				return nil
			}
		}
	}
	// 无 pending worker retry：retry checkpoint 先于 sweep 创建，指向本 Turn
	// Run 的待重试 Run 未终态前不得释放本 Turn 预算。
	state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, goal.RootWorkItemID)
	if stateErr == nil {
		if coordinatorControlAction(state) == "retry_worker" {
			if retryRunID, _ := state.Data["retry_worker_run_id"].(string); retryRunID != "" {
				retryRun, runErr := s.store.Runs().Get(ctx, retryRunID)
				if runErr != nil && !errors.Is(runErr, domain.ErrNotFound) {
					return runErr
				}
				if retryRun != nil {
					if retryKey, ok := runGovernanceTurnKey(retryRun); ok && retryKey.Equal(key) {
						return nil
					}
				}
			}
		}
	} else if !errors.Is(stateErr, domain.ErrNotFound) {
		return stateErr
	}
	// 关闭时刻合成 absent evidence（复审裁决 #4）：与 spend/结算同事务，任一
	// 失败整事务回滚，不留下半态。语义边界——关闭前 report 迟到 → canonical
	// 仍为 nil → 正常首写真实用量进 committed；关闭后 report 才到 → 越过结算
	// 边界，bindProviderUsageReport 拒绝改写并留日志。
	for _, runID := range absentPending {
		written, err := s.canonicalizeRunUsageLocked(ctx, runID, true)
		if err != nil {
			return err
		}
		if !written {
			return fmt.Errorf("%w: run %s absent evidence synthesis raced a concurrent freeze", domain.ErrStateConflict, runID)
		}
		run, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		for _, reservation := range reservations {
			if _, err := s.store.Quotas().GetSpend(ctx, domain.QuotaSpendKey{
				TurnKey: key, Kind: reservation.Key.Kind, RunID: run.ID,
			}); err == nil {
				continue
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			var settled bool
			spend, settled, err = s.appendTurnRunSpendLocked(ctx, goal.WorkspaceID, key, run, reservation, spend)
			if err != nil {
				return err
			}
			if !settled {
				// 缺 price digest 的 cost kind 无法落账：整事务回滚本次关闭，
				// Turn 保持开放等待人工对账，绝不静默丢一个 kind 收口。
				return fmt.Errorf("%w: run %s cost spend lacks price digest", domain.ErrValidation, run.ID)
			}
		}
	}
	// 关闭：committed=Σ committed spend、released=剩余；committed=0 则 released。
	now := time.Now().UTC()
	for _, reservation := range reservations {
		if reservation.Status != domain.QuotaReservationReserved {
			continue // 已结算：重放安全
		}
		committed := committedSpendTotal(spend, reservation.Key.Kind)
		expected := reservation.Version
		target := domain.QuotaReservationReleased
		if committed > 0 {
			target = domain.QuotaReservationCommitted
		}
		reservation.CommittedAmount = committed
		reservation.ReleasedAmount = reservation.ReservedAmount - committed
		if err := reservation.Transition(target, now); err != nil {
			return err
		}
		if target == domain.QuotaReservationCommitted {
			if err := s.store.Quotas().Commit(ctx, reservation, expected); err != nil {
				return err
			}
		} else if err := s.store.Quotas().Release(ctx, reservation, expected); err != nil {
			return err
		}
		if err := s.emitQuotaReservationChanged(ctx, goal.WorkspaceID, reservation, "turn_settlement"); err != nil {
			return err
		}
	}
	// phase6 只在 phase5 已有时追加：validation/authority 失败无 dispatch 的
	// Turn 保持 receipt 相位连续，reservation 关闭已落账。
	if phase5Err == nil {
		return s.appendGovernanceTurnQuotaPhaseLocked(ctx, key, goal)
	}
	return nil
}

// appendTurnRunSpendLocked 把一个终态受管 Run 在一种 usage kind 上的结算结果
// 追加进台账（幂等键 (turn,kind,run)，调用方已查重）。resolved 走 committed
// （容量预检不过则降级 unresolved 记缺口，不裁剪事实）；canonical unresolved 走
// amount=0 记缺口。同键不同内容是 bug/篡改，直接失败回滚。返回并入新 entry
// 后的台账快照与「该 run×kind 是否已落账」（cost 缺 price digest 时 false——
// 缺口不可证明，Turn 不得关闭）。
func (s *Service) appendTurnRunSpendLocked(ctx context.Context, workspaceID string, key domain.TurnKey,
	run *domain.ExecutionRun, reservation *domain.QuotaReservation,
	spend []*domain.QuotaSpendEntry) ([]*domain.QuotaSpendEntry, bool, error) {
	amount, resolved := canonicalQuotaAmountFor(run.CanonicalUsage, reservation.Key.Kind)
	entry := &domain.QuotaSpendEntry{
		Key:          domain.QuotaSpendKey{TurnKey: key, Kind: reservation.Key.Kind, RunID: run.ID},
		UsageBasis:   domain.QuotaUsageBasisPerRun,
		UsageDigest:  run.CanonicalUsageDigest,
		PolicyDigest: reservation.PolicyDigest,
		CreatedAt:    time.Now().UTC(),
	}
	switch {
	case resolved:
		remaining := reservation.ReservedAmount - committedSpendTotal(spend, reservation.Key.Kind)
		if amount > remaining {
			entry.Status = domain.QuotaSpendUnresolved
			entry.Reason = fmt.Sprintf("actual %d exceeds remaining reservation %d", amount, remaining)
		} else {
			entry.Status = domain.QuotaSpendCommitted
			entry.Amount = amount
		}
	default:
		entry.Status = domain.QuotaSpendUnresolved
		entry.Reason = run.CanonicalUsage.UnresolvedReason
		if entry.Reason == "" {
			entry.Reason = "canonical usage kind unresolved"
		}
	}
	if reservation.Key.Kind == domain.QuotaCostMicroUSD {
		if run.CanonicalUsage.PriceDigest == "" {
			// 创建侧 cost fail-closed 保证不会发生；真发生说明历史 Run 绕过了
			// 创建门。缺 price digest 的 cost entry 无法通过台账校验：记为未
			// 结算（settled=false），让 Turn 保持未关闭而不是静默丢一个 kind。
			log.Printf("quota: run %s cost spend 缺 price digest，该 kind 不可结算", run.ID)
			return spend, false, nil
		}
		entry.PriceDigest = run.CanonicalUsage.PriceDigest
	}
	created, err := s.store.Quotas().AppendSpend(ctx, entry)
	if err != nil {
		if errors.Is(err, domain.ErrIdempotencyConflict) {
			return spend, false, fmt.Errorf("quota spend %s: %w", entry.Key.String(), err)
		}
		return spend, false, err
	}
	if created {
		spend = append(spend, entry)
		if err := s.emitQuotaSpendRecorded(ctx, workspaceID, entry); err != nil {
			return spend, false, err
		}
	}
	return spend, true, nil
}

// appendGovernanceTurnQuotaPhaseLocked 从最终台账状态确定性构建 phase6 载荷并
// 追加（不含时间戳；重放同 identity 同 digest 幂等）。
func (s *Service) appendGovernanceTurnQuotaPhaseLocked(ctx context.Context,
	key domain.TurnKey, goal *domain.Goal) error {
	spend, err := s.store.Quotas().ListSpendByTurn(ctx, key)
	if err != nil {
		return err
	}
	kinds := append([]domain.QuotaKind{domain.QuotaTurnCount}, usageQuotaKinds...)
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	var (
		reservations    = []any{}
		reservationKeys = []string{}
		unresolvedSeen  = map[domain.QuotaKind]struct{}{}
		reasons         []string
	)
	for _, kind := range kinds {
		reservation, err := s.store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: key, Kind: kind})
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		reservations = append(reservations, map[string]any{
			"quota_kind": string(kind), "status": string(reservation.Status),
			"amount": reservation.CommittedAmount, "policy_limit": reservation.PolicyLimit,
			"policy_enforcement": string(reservation.PolicyEnforcement),
			"policy_digest":      reservation.PolicyDigest,
		})
		reservationKeys = append(reservationKeys, reservation.Key.String())
	}
	for _, entry := range spend {
		if entry.Status != domain.QuotaSpendUnresolved {
			continue
		}
		unresolvedSeen[entry.Key.Kind] = struct{}{}
		reason := entry.Reason
		if reason == "" {
			reason = "canonical usage kind unresolved"
		}
		reasons = append(reasons, fmt.Sprintf("%s/%s: %s", entry.Key.Kind, entry.Key.RunID, reason))
	}
	unresolvedKinds := make([]string, 0, len(unresolvedSeen))
	for kind := range unresolvedSeen {
		unresolvedKinds = append(unresolvedKinds, string(kind))
	}
	sort.Strings(unresolvedKinds)
	sort.Strings(reasons)
	unresolvedAny := make([]any, len(unresolvedKinds))
	for i, kind := range unresolvedKinds {
		unresolvedAny[i] = kind
	}
	activeWorkerAccounting := "not_enabled"
	for _, policy := range goal.QuotaPolicies {
		if policy.Kind == domain.QuotaActiveWorker {
			activeWorkerAccounting = "gauge_not_spend"
		}
	}
	return s.appendGovernancePlanPhaseWithQuota(ctx, &domain.TurnReceiptHeader{TurnKey: key}, map[string]any{
		"reservations":             reservations,
		"active_worker_accounting": activeWorkerAccounting,
		"unresolved_kinds":         unresolvedAny,
		"unresolved_reason":        strings.Join(reasons, ";"),
	}, reservationKeys)
}

// canonicalQuotaAmountFor 从 canonical usage 取一种 quota kind 的结算值（cost
// 取 CostMicroUSD；false = 该 kind 无值即 unresolved）。与 sqlstore 的
// canonicalQuotaAmount 同义，避免应用层反向依赖持久层。
func canonicalQuotaAmountFor(usage *domain.CanonicalUsageV1, kind domain.QuotaKind) (int64, bool) {
	if usage == nil {
		return 0, false
	}
	var value *int64
	switch kind {
	case domain.QuotaInputTokensTotal:
		value = usage.Counters.InputTokensTotal
	case domain.QuotaInputUncachedTokens:
		value = usage.Counters.InputUncachedTokens
	case domain.QuotaCacheReadTokens:
		value = usage.Counters.CacheReadTokens
	case domain.QuotaCacheWriteTokens:
		value = usage.Counters.CacheWriteTokens
	case domain.QuotaOutputTokens:
		value = usage.Counters.OutputTokens
	case domain.QuotaCostMicroUSD:
		value = usage.CostMicroUSD
	default:
		return 0, false
	}
	if value == nil {
		return 0, false
	}
	return *value, true
}

// committedSpendTotal 汇总台账快照中某 kind 的 committed 总量。domain 校验强制
// unresolved entry amount=0，因此该值同时等于 repo 容量检查口径的
// 「Σ 该 (turn,kind) 全部 spend.amount」与 reservation 关闭口径的
// 「Σ committed spend」——容量预检与关闭聚合同源，不会出现两侧口径漂移。
func committedSpendTotal(spend []*domain.QuotaSpendEntry, kind domain.QuotaKind) int64 {
	var total int64
	for _, entry := range spend {
		if entry != nil && entry.Key.Kind == kind && entry.Status == domain.QuotaSpendCommitted {
			total += entry.Amount
		}
	}
	return total
}

// usageQuotaKinds 是 canonical usage 支撑的六种 quota kind（遍历顺序固定）。
var usageQuotaKinds = []domain.QuotaKind{
	domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens,
	domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
	domain.QuotaOutputTokens, domain.QuotaCostMicroUSD,
}
