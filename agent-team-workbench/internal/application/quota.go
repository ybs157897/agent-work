package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type ShouldRunRequest struct {
	GoalID string
	Kind   domain.QuotaKind
	Amount int64
}

type ShouldRunDecision struct {
	Enabled      bool
	Allowed      bool
	WouldDeny    bool
	Unresolved   bool
	Reason       string
	Kind         domain.QuotaKind
	Enforcement  domain.QuotaEnforcement
	Limit        int64
	Used         int64
	Requested    int64
	Remaining    int64
	PolicyDigest string
}

func (s *Service) ShouldRunLocked(ctx context.Context, request ShouldRunRequest) (ShouldRunDecision, error) {
	decision := ShouldRunDecision{Allowed: true, Kind: request.Kind, Requested: request.Amount}
	if !request.Kind.Valid() || request.Amount < 0 {
		return decision, fmt.Errorf("%w: invalid quota ShouldRun request", domain.ErrValidation)
	}
	goal, err := s.store.Goals().Get(ctx, request.GoalID)
	if err != nil {
		return decision, err
	}
	policy, ok := goalQuotaPolicy(goal, request.Kind)
	if !ok {
		return decision, nil
	}
	digest, err := canonicalGovernancePlanDigest(policy)
	if err != nil {
		return decision, err
	}
	decision.Enabled = true
	decision.Enforcement = policy.Enforcement
	decision.Limit = policy.Limit
	decision.PolicyDigest = digest
	switch request.Kind {
	case domain.QuotaTurnCount:
		decision.Used, err = s.store.Quotas().SumCommitted(ctx, goal.ID, domain.QuotaTurnCount)
	case domain.QuotaActiveWorker:
		var count int
		count, err = s.store.Quotas().ActiveWorkerCount(ctx, goal.ID)
		decision.Used = int64(count)
	default:
		// usage kind 由 canonical usage 台账支撑：used = committed spend +
		// active reserved（并发 Turn 冻结的预算），不再视作 Unresolved。
		committed, committedErr := s.store.Quotas().SumCommitted(ctx, goal.ID, request.Kind)
		if committedErr != nil {
			return decision, committedErr
		}
		active, activeErr := s.store.Quotas().SumActiveReserved(ctx, goal.ID, request.Kind)
		if activeErr != nil {
			return decision, activeErr
		}
		// P1-1（复审裁决 #1，R4 原文）：无法证明 delta 的 unresolved 缺口必须
		// 进入准入判定——存在缺口即 fail closed（audit 记录、enforce 拒绝），
		// 缺口永不自动清除，人工对账前不放行。
		gaps, gapErr := s.store.Quotas().ListUnresolved(ctx, goal.ID, request.Kind)
		if gapErr != nil {
			return decision, gapErr
		}
		if len(gaps) > 0 {
			decision.Unresolved = true
			decision.Reason = unresolvedGapReason(len(gaps), request.Kind)
		}
		decision.Used, err = domain.CheckedAddNonNegative(committed, active)
	}
	if err != nil {
		return decision, err
	}
	if decision.Used > decision.Limit {
		decision.Remaining = 0
	} else {
		decision.Remaining = decision.Limit - decision.Used
	}
	overflow := request.Amount > 0 && decision.Used > int64(^uint64(0)>>1)-request.Amount
	decision.WouldDeny = decision.Unresolved || overflow || request.Amount > decision.Remaining
	decision.Allowed = policy.Enforcement == domain.QuotaEnforcementAudit || !decision.WouldDeny
	return decision, nil
}

func goalQuotaPolicy(goal *domain.Goal, kind domain.QuotaKind) (domain.QuotaPolicy, bool) {
	if goal == nil {
		return domain.QuotaPolicy{}, false
	}
	for _, policy := range goal.QuotaPolicies {
		if policy.Kind == kind {
			return policy, true
		}
	}
	return domain.QuotaPolicy{}, false
}

func quotaDeniedError(decision ShouldRunDecision) error {
	reason := decision.Reason
	if reason == "" {
		reason = fmt.Sprintf("quota %s used=%d requested=%d limit=%d",
			decision.Kind, decision.Used, decision.Requested, decision.Limit)
	}
	return &PlanDecisionError{
		Code: domain.GovernanceErrorPlanQuotaDenied, Path: "/quota/" + string(decision.Kind),
		Message: reason, Cause: domain.ErrValidation,
	}
}

func (s *Service) ensureTurnCountReservationLocked(ctx context.Context, goal *domain.Goal,
	header *domain.TurnReceiptHeader, decision ShouldRunDecision) (*domain.QuotaReservation, error) {
	if !decision.Enabled {
		return nil, nil
	}
	if decision.Kind != domain.QuotaTurnCount || header == nil {
		return nil, fmt.Errorf("%w: turn_count reservation requires admitted Header", domain.ErrValidation)
	}
	now := time.Now().UTC()
	reservation := &domain.QuotaReservation{
		Key:    domain.QuotaReservationKey{TurnKey: header.TurnKey, Kind: domain.QuotaTurnCount},
		Status: domain.QuotaReservationReserved, ReservedAmount: 1,
		PolicyLimit: decision.Limit, PolicyEnforcement: decision.Enforcement,
		PolicyDigest: decision.PolicyDigest, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, err := s.store.Quotas().Reserve(ctx, reservation)
	if err != nil {
		return nil, err
	}
	if created {
		if err := s.emitQuotaReservationChanged(ctx, goal.WorkspaceID, reservation, "admission"); err != nil {
			return nil, err
		}
	}
	if !created {
		existing, err := s.store.Quotas().Get(ctx, reservation.Key)
		if err != nil {
			return nil, err
		}
		if existing.Status == domain.QuotaReservationCommitted {
			return existing, nil
		}
		reservation = existing
	}
	expected := reservation.Version
	reservation.CommittedAmount = 1
	reservation.ReleasedAmount = 0
	if err := reservation.Transition(domain.QuotaReservationCommitted, now); err != nil {
		return nil, err
	}
	if err := s.store.Quotas().Commit(ctx, reservation, expected); err != nil {
		if errors.Is(err, domain.ErrVersionConflict) {
			existing, getErr := s.store.Quotas().Get(ctx, reservation.Key)
			if getErr == nil && existing.Status == domain.QuotaReservationCommitted {
				return existing, nil
			}
		}
		return nil, err
	}
	if err := s.emitQuotaReservationChanged(ctx, goal.WorkspaceID, reservation, "admission_commit"); err != nil {
		return nil, err
	}
	return reservation, nil
}

func (s *Service) ensureExistingTurnCountReservationLocked(ctx context.Context, goal *domain.Goal,
	header *domain.TurnReceiptHeader) (*domain.QuotaReservation, error) {
	key := domain.QuotaReservationKey{TurnKey: header.TurnKey, Kind: domain.QuotaTurnCount}
	if existing, err := s.store.Quotas().Get(ctx, key); err == nil {
		switch existing.Status {
		case domain.QuotaReservationCommitted:
			return existing, nil
		case domain.QuotaReservationReserved:
			// Header admission is the authority fact. If a crash happened after
			// inserting the reservation but before its immediate commit, replay
			// must finish that same reservation instead of treating it as a new
			// turn or leaving phase 6 permanently blocked on "reserved".
			if existing.ReservedAmount != 1 {
				return nil, fmt.Errorf("%w: turn_count reservation amount must be 1, got %d", domain.ErrValidation, existing.ReservedAmount)
			}
			now := time.Now().UTC()
			expected := existing.Version
			existing.CommittedAmount = 1
			existing.ReleasedAmount = 0
			if err := existing.Transition(domain.QuotaReservationCommitted, now); err != nil {
				return nil, err
			}
			if err := s.store.Quotas().Commit(ctx, existing, expected); err != nil {
				if errors.Is(err, domain.ErrVersionConflict) {
					latest, getErr := s.store.Quotas().Get(ctx, key)
					if getErr == nil && latest.Status == domain.QuotaReservationCommitted {
						return latest, nil
					}
				}
				return nil, err
			}
			if err := s.emitQuotaReservationChanged(ctx, goal.WorkspaceID, existing, "admission_commit_replay"); err != nil {
				return nil, err
			}
			return existing, nil
		default:
			return nil, fmt.Errorf("%w: existing turn_count reservation is %s without a committed receipt phase",
				domain.ErrStateConflict, existing.Status)
		}
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	decision, err := s.ShouldRunLocked(ctx, ShouldRunRequest{GoalID: goal.ID, Kind: domain.QuotaTurnCount, Amount: 1})
	if err != nil || !decision.Enabled {
		return nil, err
	}
	// The Header proves this Turn was already admitted. Backfill ledger state
	// for an upgraded database even if today's enforce policy would deny a new
	// Turn; future admissions will count the committed reservation.
	decision.Allowed = true
	return s.ensureTurnCountReservationLocked(ctx, goal, header, decision)
}

func quotaDecisionPayload(decision ShouldRunDecision) map[string]any {
	return map[string]any{
		"enabled": decision.Enabled, "allowed": decision.Allowed, "would_deny": decision.WouldDeny,
		"unresolved": decision.Unresolved, "reason": decision.Reason, "quota_kind": string(decision.Kind),
		"enforcement": string(decision.Enforcement), "limit": decision.Limit, "used": decision.Used,
		"requested": decision.Requested, "remaining": decision.Remaining, "policy_digest": decision.PolicyDigest,
	}
}

// usageQuotaKind 报告 kind 是否属于 canonical usage 支撑的六种 quota kind
// （turn_count/active_worker 之外的全部治理配额单位）。
func usageQuotaKind(kind domain.QuotaKind) bool {
	switch kind {
	case domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens,
		domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
		domain.QuotaOutputTokens, domain.QuotaCostMicroUSD:
		return true
	default:
		return false
	}
}

// ensureUsageQuotaReservationsLocked 在 admission 事务内为 Goal 的全部 usage
// 政策冻结本 Turn 的 reservation（turn_count 由 ensureTurnCountReservationLocked
// 单独负责）。admission 重放同样调用，补齐升级/崩溃窗口的缺口。
func (s *Service) ensureUsageQuotaReservationsLocked(ctx context.Context,
	goal *domain.Goal, key domain.TurnKey) error {
	for _, policy := range goal.QuotaPolicies {
		if !usageQuotaKind(policy.Kind) {
			continue
		}
		if _, _, err := s.ensureUsageQuotaReservationLocked(ctx, goal, key, policy); err != nil {
			return err
		}
	}
	return nil
}

// ensureUsageQuotaReservationLocked 确保一个 usage kind 的 Turn reservation 存在
// （get-or-create：命中即复用冻结值，不按当前水位重算），并返回该政策的准入
// decision——gate（WouldDeny 只看本 Turn 冻结的剩余容量）与审计载荷共用。
func (s *Service) ensureUsageQuotaReservationLocked(ctx context.Context, goal *domain.Goal,
	key domain.TurnKey, policy domain.QuotaPolicy) (*domain.QuotaReservation, *ShouldRunDecision, error) {
	policyDigest, err := canonicalGovernancePlanDigest(policy)
	if err != nil {
		return nil, nil, err
	}
	committed, err := s.store.Quotas().SumCommitted(ctx, goal.ID, policy.Kind)
	if err != nil {
		return nil, nil, err
	}
	active, err := s.store.Quotas().SumActiveReserved(ctx, goal.ID, policy.Kind)
	if err != nil {
		return nil, nil, err
	}
	used, err := domain.CheckedAddNonNegative(committed, active)
	if err != nil {
		return nil, nil, err
	}
	reservationKey := domain.QuotaReservationKey{TurnKey: key, Kind: policy.Kind}
	existing, err := s.store.Quotas().Get(ctx, reservationKey)
	frozen := false
	switch {
	case err == nil:
		// 政策中途变更不重算冻结值：reservation 是 admission 时刻的快照。
		frozen = true
	case errors.Is(err, domain.ErrNotFound):
		remaining := policy.Limit - used
		if remaining < 0 {
			remaining = 0
		}
		now := time.Now().UTC()
		existing = &domain.QuotaReservation{
			Key: reservationKey, Status: domain.QuotaReservationReserved,
			ReservedAmount: remaining, PolicyLimit: policy.Limit,
			PolicyEnforcement: policy.Enforcement, PolicyDigest: policyDigest,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		created, err := s.store.Quotas().Reserve(ctx, existing)
		if err != nil {
			return nil, nil, err
		}
		if created {
			if err := s.emitQuotaReservationChanged(ctx, goal.WorkspaceID, existing, "admission"); err != nil {
				return nil, nil, err
			}
		}
		if !created {
			if existing, err = s.store.Quotas().Get(ctx, reservationKey); err != nil {
				return nil, nil, err
			}
			frozen = true
		}
	default:
		return nil, nil, err
	}
	remaining := policy.Limit - used
	if remaining < 0 {
		remaining = 0
	}
	// P1-1（复审裁决 #1）：worker 创建闸与 ShouldRun 同口径——reservation 有
	// 冻结余额但 Goal 存在无法证明的 usage 缺口时同样 fail closed。
	gaps, err := s.store.Quotas().ListUnresolved(ctx, goal.ID, policy.Kind)
	if err != nil {
		return nil, nil, err
	}
	// 已冻结 reservation 的 gate/审计政策面回指快照而不是当前 policy。
	limit, enforcement, digest := policy.Limit, policy.Enforcement, policyDigest
	if frozen {
		limit, enforcement, digest = existing.PolicyLimit, existing.PolicyEnforcement, existing.PolicyDigest
	}
	decision := &ShouldRunDecision{
		Enabled: true, Kind: policy.Kind, Enforcement: enforcement,
		Limit: limit, PolicyDigest: digest, Requested: 1,
		Used: used, Remaining: remaining,
		WouldDeny: existing.ReservedAmount == 0 || len(gaps) > 0,
	}
	if len(gaps) > 0 {
		decision.Unresolved = true
		decision.Reason = unresolvedGapReason(len(gaps), policy.Kind)
	}
	decision.Allowed = enforcement == domain.QuotaEnforcementAudit || !decision.WouldDeny
	return existing, decision, nil
}

// unresolvedGapReason 是 unresolved usage 缺口进入准入判定的统一话术
// （审计载荷与 quota_denied 错误信息共用，测试断言其语义）。
func unresolvedGapReason(count int, kind domain.QuotaKind) string {
	return fmt.Sprintf("存在 %d 条无法证明的 usage 结算缺口（%s），fail closed 直至人工对账", count, kind)
}

// emitQuotaReservationChanged publishes a durable quota invalidation only after
// the repository has created or CAS-transitioned the reservation. The event,
// reservation row and outbox record therefore share the caller's transaction;
// replaying an already-created/transitioned row does not publish a duplicate.
func (s *Service) emitQuotaReservationChanged(ctx context.Context, workspaceID string,
	reservation *domain.QuotaReservation, reason string) error {
	if reservation == nil {
		return fmt.Errorf("%w: quota reservation event requires reservation", domain.ErrValidation)
	}
	amount := reservation.ReservedAmount
	switch reservation.Status {
	case domain.QuotaReservationCommitted:
		amount = reservation.CommittedAmount
	case domain.QuotaReservationReleased, domain.QuotaReservationExpired:
		amount = reservation.ReleasedAmount
	case domain.QuotaReservationReserved:
	default:
		return fmt.Errorf("%w: quota reservation event has invalid status %q", domain.ErrValidation, reservation.Status)
	}
	aggregateVersion, err := s.governanceGoalAggregateVersion(ctx, workspaceID, reservation.Key.TurnKey.GoalID)
	if err != nil {
		return err
	}
	return s.emit(ctx, workspaceID, domain.EventQuotaReservationChanged,
		domain.AggregateGoal, reservation.Key.TurnKey.GoalID, aggregateVersion,
		nil, map[string]any{
			"goal_id":             reservation.Key.TurnKey.GoalID,
			"todo_id":             reservation.Key.TurnKey.TodoID,
			"turn_seq":            reservation.Key.TurnKey.TurnSeq,
			"quota_kind":          string(reservation.Key.Kind),
			"run_id":              "",
			"status":              string(reservation.Status),
			"reservation_state":   string(reservation.Status),
			"amount":              amount,
			"reserved_amount":     reservation.ReservedAmount,
			"committed_amount":    reservation.CommittedAmount,
			"released_amount":     reservation.ReleasedAmount,
			"policy_limit":        reservation.PolicyLimit,
			"policy_enforcement":  string(reservation.PolicyEnforcement),
			"policy_digest":       reservation.PolicyDigest,
			"usage_basis":         "",
			"usage_digest":        "",
			"price_digest":        "",
			"reason":              reason,
			"reservation_version": reservation.Version,
		})
}

// emitQuotaSpendRecorded publishes one ledger invalidation for a newly appended
// spend entry. The payload carries only canonical identifiers, digests and the
// already-redacted reason; raw provider output and credentials never enter the
// event stream.
func (s *Service) emitQuotaSpendRecorded(ctx context.Context, workspaceID string,
	entry *domain.QuotaSpendEntry) error {
	if entry == nil {
		return fmt.Errorf("%w: quota spend event requires entry", domain.ErrValidation)
	}
	aggregateVersion, err := s.governanceGoalAggregateVersion(ctx, workspaceID, entry.Key.TurnKey.GoalID)
	if err != nil {
		return err
	}
	return s.emit(ctx, workspaceID, domain.EventQuotaSpendRecorded,
		domain.AggregateGoal, entry.Key.TurnKey.GoalID, aggregateVersion, nil, map[string]any{
			"goal_id":       entry.Key.TurnKey.GoalID,
			"todo_id":       entry.Key.TurnKey.TodoID,
			"turn_seq":      entry.Key.TurnKey.TurnSeq,
			"quota_kind":    string(entry.Key.Kind),
			"run_id":        entry.Key.RunID,
			"status":        string(entry.Status),
			"amount":        entry.Amount,
			"usage_basis":   entry.UsageBasis,
			"usage_digest":  entry.UsageDigest,
			"policy_digest": entry.PolicyDigest,
			"price_digest":  entry.PriceDigest,
			"reason":        entry.Reason,
		})
}

// governanceGoalAggregateVersion reads the Goal version through the caller's
// transaction. Quota events aggregate on Goal, so reservation/spend row
// versions must never be substituted for the Goal's current version.
func (s *Service) governanceGoalAggregateVersion(ctx context.Context, workspaceID, goalID string) (int, error) {
	goal, err := s.store.Goals().Get(ctx, goalID)
	if err != nil {
		return 0, err
	}
	if goal.WorkspaceID != workspaceID {
		return 0, fmt.Errorf("%w: quota event Goal is outside workspace", domain.ErrValidation)
	}
	return goal.Version, nil
}

func rootGovernanceGoalID(ctx context.Context, store Store, rootWorkItemID string) (string, error) {
	goal, err := store.Goals().GetByRootWorkItem(ctx, rootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if goal.Status != domain.GoalActive || goal.CurrentTodoID == "" {
		return "", nil
	}
	return goal.ID, nil
}
