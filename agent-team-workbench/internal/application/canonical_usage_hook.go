package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// canonical usage 终态钩子：把 ProviderUsageReport（或受管 Run 的 absent
// evidence）固化为不可变 CanonicalUsageV1，并叠加二段制成本。唯一写点是
// canonicalizeRunUsageLocked；三处终态钩子链与 quota sweep 共享。

// runGovernanceTurnKey 从 Run input.governance 解析治理 Turn 身份（非受管
// Run 返回 false）。input 落库经 JSON 克隆往返，turn_seq 可能是 float64。
func runGovernanceTurnKey(run *domain.ExecutionRun) (domain.TurnKey, bool) {
	if run == nil || run.Input == nil {
		return domain.TurnKey{}, false
	}
	governance, _ := run.Input["governance"].(map[string]any)
	if governance == nil {
		return domain.TurnKey{}, false
	}
	goalID, _ := governance["goal_id"].(string)
	todoID, _ := governance["todo_id"].(string)
	turnSeq, ok := governanceInt64(governance["turn_seq"])
	if goalID == "" || todoID == "" || !ok {
		return domain.TurnKey{}, false
	}
	return domain.TurnKey{GoalID: goalID, TodoID: todoID, TurnSeq: turnSeq}, true
}

// governanceInt64 宽松解析治理身份里的整数字段：内存构造是 int64/int，
// JSON 克隆往返后是 float64，encoding/json 还可能给出 json.Number。
func governanceInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// maybeCanonicalizeRunUsage 终态钩子（RecordRunStatus / replayRunTerminalHooks /
// replayCoordinatorTerminalHooks 三处复用）：自带事务，失败只记日志不上返——
// report 已持久，canonical 可由 quota sweep / reconcile 重放补齐。
// 返回值（frozen, err）只是给 run journal 埋点的信号（runs.go post 相位）：
// frozen=canonical 已冻结（含幂等命中）；err=落账失败。控制流不变。
func (s *Service) maybeCanonicalizeRunUsage(ctx context.Context, run *domain.ExecutionRun) (bool, error) {
	if run == nil || !run.Status.IsTerminal() {
		return false, nil
	}
	frozen := false
	if err := s.store.InTx(ctx, func(txctx context.Context) error {
		existing, err := s.canonicalizeRunUsageLocked(txctx, run.ID, false)
		frozen = existing
		return err
	}); err != nil {
		log.Printf("usage: run %s canonical 终态落账失败（等待重放）: %v", run.ID, err)
		return false, err
	}
	return frozen, nil
}

// canonicalizeRunUsageLocked 是 canonical usage 的唯一写点：fresh 读 Run，
// 终态且未冻结时由 provider report 生成。allowAbsentEvidence 仅在「调用方为
// 该 Run 的结算证据背书」时授权合成 absent canonical（quota sweep 的关闭性
// 触发源）；进行式 pass（终态钩子 / 迟到上报兜底）恒为 false——无 report 的
// 受管 Run 在关闭前保持 canonical 缺失，迟到 report 随时可正常首写真实用量
// （复审裁决 #4：延迟即答案，不改 0028 不可变 trigger、不开升级旁路）。
// 返回 canonical 是否已存在/新写入。
func (s *Service) canonicalizeRunUsageLocked(ctx context.Context, runID string, allowAbsentEvidence bool) (bool, error) {
	r, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return false, err
	}
	if !r.Status.IsTerminal() {
		return false, nil
	}
	if r.CanonicalUsage != nil {
		return true, nil // 已冻结不可变：幂等命中
	}
	_, governed := runGovernanceTurnKey(r)
	var (
		canonical  *domain.CanonicalUsageV1
		nextAnchor *domain.ProviderUsageAnchorV1
		session    *domain.TaskSession
	)
	if report := r.ProviderUsageReport; report != nil {
		session, err = s.providerUsageSession(ctx, r)
		if err != nil {
			return false, err
		}
		contextGeneration, segmentSeq := 0, 1
		anchorOwnerRunID, anchorOwnerSequence := "", int64(0)
		if session != nil {
			contextGeneration = session.ContextGeneration
			if session.SegmentSeq > segmentSeq {
				segmentSeq = session.SegmentSeq
			}
			anchorOwnerRunID = session.LastRunID
			anchorOwnerSequence = session.AnchorRunSequence
		}
		result, err := CanonicalizeProviderUsageV1(CanonicalUsageRequestV1{
			Report: report, RunID: r.ID, AgentID: r.AgentProfileID,
			AdapterID:            report.Provenance.AdapterID,
			Anchor:               providerUsageAnchorOf(session),
			ContextGeneration:    contextGeneration,
			SegmentSeq:           segmentSeq,
			AnchorOwnershipKnown: true,
			AnchorOwnerRunID:     anchorOwnerRunID,
			AnchorOwnerSequence:  anchorOwnerSequence,
			FreshProviderSession: r.SessionBefore == "",
			ObservedAt:           time.Now().UTC(),
		})
		if err != nil {
			return false, err
		}
		canonical, nextAnchor = result.Canonical, result.NextAnchor
	} else if allowAbsentEvidence {
		canonical = absentCanonicalUsage(r)
	} else {
		return false, nil
	}
	if err := stampCanonicalCostLocked(r, canonical, governed); err != nil {
		return false, err
	}
	if err := canonical.Seal(); err != nil {
		return false, err
	}
	expected := r.Version
	r.CanonicalUsage = canonical
	r.CanonicalUsageDigest = canonical.Digest
	if err := s.store.Runs().Update(ctx, r, expected); err != nil {
		return false, err
	}
	// anchor 推进与 canonical 写入同事务：CAS 失败整事务回滚，钩子重放时按
	// 新水位重算，绝不覆盖并发 owner 的新基线。
	if nextAnchor != nil && session != nil {
		advanced, err := s.store.TaskSessions().UpdateProviderUsageAnchorCAS(ctx,
			session.WorkspaceID, session.AgentProfileID, session.AdapterID, session.TaskKey,
			nextAnchor, session.ProviderUsageAnchorSeq, r.ID, session.AnchorRunSequence)
		if err != nil {
			return false, err
		}
		if !advanced {
			return false, domain.ErrVersionConflict
		}
	}
	return true, nil
}

// providerUsageSession 读取 Run 的 TaskSession 锚点（ErrNotFound → nil 不算错）。
func (s *Service) providerUsageSession(ctx context.Context, r *domain.ExecutionRun) (*domain.TaskSession, error) {
	session, err := s.store.TaskSessions().Get(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return session, nil
}

func providerUsageAnchorOf(session *domain.TaskSession) *domain.ProviderUsageAnchorV1 {
	if session == nil {
		return nil
	}
	return session.ProviderUsageAnchor
}

// absentCanonicalUsage 为没有 provider report 的终态受管 Run 合成全 unresolved
// 的 canonical（absent evidence）：不伪造 0 消耗，缺口与原因显式留痕。
func absentCanonicalUsage(r *domain.ExecutionRun) *domain.CanonicalUsageV1 {
	adapterID := r.AdapterID
	if adapterID == "" {
		adapterID = "unknown"
	}
	agentID := r.AgentProfileID
	if agentID == "" {
		agentID = "unknown"
	}
	return &domain.CanonicalUsageV1{
		SchemaVersion: domain.CanonicalUsageSchemaVersionV1,
		RunID:         r.ID,
		Basis:         domain.UsageBasisPerRun,
		ResolvedKinds: []domain.QuotaKind{},
		UnresolvedKinds: []domain.QuotaKind{
			domain.QuotaCacheReadTokens, domain.QuotaCacheWriteTokens,
			domain.QuotaInputTokensTotal, domain.QuotaInputUncachedTokens,
			domain.QuotaOutputTokens,
		},
		UnresolvedReason: "provider did not report usage for this run",
		Provenance: domain.UsageProvenanceV1{
			AdapterID: adapterID, Protocol: "none", ProtocolVersion: "none",
			Source: "control_plane", ReportedBasis: domain.UsageBasisPerRun,
			AgentID: agentID, Mapping: "absent",
		},
	}
}

// stampCanonicalCostLocked 叠加二段制成本：价格只来自 Run input 固化的快照，
// 不重读当前模型价。价格缺失/桶缺失是显式 unresolved（缺口可审计）；算术
// 溢出等真错误 fail closed，不写 canonical。
func stampCanonicalCostLocked(r *domain.ExecutionRun, canonical *domain.CanonicalUsageV1, governed bool) error {
	price, err := domain.PriceSnapshotFromRunInput(r.Input)
	if err != nil {
		return err
	}
	if price == nil {
		// 无价且非受管：cost 不参与（domain 豁免分支，kind 不进任何列表）。
		if governed {
			canonical.UnresolvedKinds = appendUsageKindUniqueSorted(canonical.UnresolvedKinds, domain.QuotaCostMicroUSD)
			canonical.UnresolvedReason = joinUnresolvedReasons(canonical.UnresolvedReason,
				"cost_microusd: price snapshot unavailable")
		}
		return nil
	}
	cost, err := domain.ComputeCostMicroUSD(canonical.Counters, price)
	var costErr *domain.CostUnresolvedError
	switch {
	case err == nil:
		canonical.CostMicroUSD = &cost
		canonical.PriceDigest = price.Digest
		canonical.ResolvedKinds = appendUsageKindSorted(canonical.ResolvedKinds, domain.QuotaCostMicroUSD)
	case errors.As(err, &costErr):
		// 价在桶缺：cost 记缺口，但 PriceDigest 仍固化（0027 要求 cost spend
		// 行必带 price_digest）。
		canonical.PriceDigest = price.Digest
		canonical.UnresolvedKinds = appendUsageKindUniqueSorted(canonical.UnresolvedKinds, domain.QuotaCostMicroUSD)
		canonical.UnresolvedReason = joinUnresolvedReasons(canonical.UnresolvedReason, "cost_microusd: "+err.Error())
	default:
		return fmt.Errorf("compute cost_microusd for run %s: %w", r.ID, err)
	}
	return nil
}

// appendUsageKindSorted 追加 kind 并保持字符串升序（调用方保证不重复）。
func appendUsageKindSorted(kinds []domain.QuotaKind, kind domain.QuotaKind) []domain.QuotaKind {
	out := append(append([]domain.QuotaKind{}, kinds...), kind)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func appendUsageKindUniqueSorted(kinds []domain.QuotaKind, kind domain.QuotaKind) []domain.QuotaKind {
	for _, existing := range kinds {
		if existing == kind {
			return kinds
		}
	}
	return appendUsageKindSorted(kinds, kind)
}

// joinUnresolvedReasons 分号连接追加缺口原因（domain 要求 kinds 与 reason 同存在）。
func joinUnresolvedReasons(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}
