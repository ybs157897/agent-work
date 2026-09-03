package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── Runner v2 事件原子入口（架构 RFC §8.3.1）──────────────────────────
//
// ApplyRunnerEvent/Accept/Reject 是所有 Runner 帧的统一应用命令：
// dedup 条件插入、Run/Session/Usage/Artifact/审批状态、canonical events/outbox、
// lease 释放在同一事务内完成。瞬态失败整体回滚并返回 error（网关不 ACK，
// Runner 保留 pending 重试）；duplicate/stale 标 ackable 不应用；
// 永久非法事件在同一事务落 failed(runner_event_invalid)+audit+dedup 后照常
// ACK（防毒帧循环）。ACK 只能在事务 commit 后发送。

// errDuplicateRunnerEvent 内部哨兵：dedup 冲突时从事务内返回以触发干净回滚
// （PG 中失败的 INSERT 会 abort 事务，必须回滚而非继续），外层转为 duplicate ACK。
var errDuplicateRunnerEvent = errors.New("runner event duplicate")

func runnerFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case float32:
		f := float64(n)
		return f, !math.IsNaN(f) && !math.IsInf(f, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func runnerNonNegativeInt(data map[string]any, key string) (int64, bool) {
	v, ok := runnerFloat(data[key])
	// float64(math.MaxInt64) rounds up to 2^63; use >= so that rounded boundary
	// cannot pass and overflow to a negative int64 during conversion.
	if !ok || v < 0 || math.Trunc(v) != v || v >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(v), true
}

func runnerSHA256(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// decodeProviderUsageReport 把 usage.updated 帧的 provider_report（sealed
// domain.ProviderUsageReportV1 的 JSON 形态，经网关 map[string]any 往返）
// 还原为 typed 报告。类型不对（如字符串）或字段畸形返回错误——调用方按毒帧
// 收口；digest 一致性由调用方 VerifyDigest 判定。
func decodeProviderUsageReport(raw any) (*domain.ProviderUsageReportV1, error) {
	marshaled, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var report domain.ProviderUsageReportV1
	if err := json.Unmarshal(marshaled, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

func parseRunnerSession(data map[string]any) (runtime.SessionUpdate, bool) {
	update := runtime.SessionUpdate{}
	if raw, exists := data["ref"]; exists {
		ref, ok := raw.(string)
		if !ok {
			return runtime.SessionUpdate{}, false
		}
		update.Ref = ref
	}
	if raw, exists := data["display_id"]; exists {
		displayID, ok := raw.(string)
		if !ok {
			return runtime.SessionUpdate{}, false
		}
		update.DisplayID = displayID
	}
	if raw, exists := data["clear"]; exists {
		clear, ok := raw.(bool)
		if !ok {
			return runtime.SessionUpdate{}, false
		}
		update.Clear = clear
	}
	if raw, exists := data["clear_reason"]; exists {
		reason, ok := raw.(string)
		if !ok {
			return runtime.SessionUpdate{}, false
		}
		update.ClearReason = reason
	}
	if raw, exists := data["params"]; exists {
		params, ok := raw.(map[string]any)
		if !ok {
			return runtime.SessionUpdate{}, false
		}
		update.Params = params
	}
	if !update.Clear && update.Ref == "" {
		return runtime.SessionUpdate{}, false
	}
	return update, true
}

// activeLeaseMatches 校验活动租约五元组中的三要素（epoch 是 transport 事实，
// 由网关在投递前校验，不参与事件身份）。
func activeLeaseMatches(l *RunLease, leaseID, runnerID string, fencing int64) bool {
	return l != nil && !l.Released && l.LeaseID == leaseID && l.RunnerID == runnerID && l.FencingToken == fencing
}

// LateTerminalObservationKind 报告 kind 是否可走「已释放租约 + 终态 Run」的
// 迟到终态观测例外：usage.updated（runnerd 终态后补发用量证据）与
// run.phase_entered/run.phase_closed（Run Journal 相位闭包——settle 闭包在
// 终态帧之后发出，落不了 run_events 就等于每个远程 run 都以未闭合 settle
// 收尾，违反「未闭合即故障点」）。其余 kind 一律不享受例外；网关侧放行
// 判断与本闭集共用同一事实源。
func LateTerminalObservationKind(kind string) bool {
	switch kind {
	case domain.EventUsageUpdated, domain.EventRunPhaseEntered, domain.EventRunPhaseClosed:
		return true
	}
	return false
}

// releasedLeaseObservationAllowed 判定「已释放租约 + 终态 Run」的迟到终态
// 观测帧（LateTerminalObservationKind 闭集）是否放行（终态观测例外）。租约
// 按 lease_id 读含已释放行，身份四要素（lease_id/run/runner/fencing）必须与
// 帧完全一致，且 Run 已终态——非终态 Run 命中已释放租约说明租约被
// sweep/接管收走，仍按 stale 拒绝。lease 不存在（ErrNotFound）按不满足处理；
// 其他存储错误上返（瞬态失败不 ACK，Runner 重试）。
func (s *Service) releasedLeaseObservationAllowed(ctx context.Context, in RunnerEventInput) (bool, error) {
	lease, err := s.store.Runners().GetLease(ctx, in.LeaseID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if lease == nil || !lease.Released ||
		lease.RunID != in.RunID || lease.RunnerID != in.RunnerID || lease.FencingToken != in.FencingToken {
		return false, nil
	}
	r, err := s.store.Runs().Get(ctx, in.RunID)
	if err != nil {
		return false, err
	}
	return r.Status.IsTerminal(), nil
}

// RunnerEventInput 是 Gateway 完成 transport 校验后交给应用层的事件命令。
// ConnectionEpoch 只识别当前 transport connection，不参与事件身份/dedup。
type RunnerEventInput struct {
	RunID           string
	LeaseID         string
	RunnerID        string
	ConnectionEpoch string
	FencingToken    int64
	// EventID/ProducerSeq 由 Runner 创建事件时分配，重连原样重发，身份不变。
	// dedup key = (run_id, lease_id, runner_id, producer_seq)。
	EventID     string
	ProducerSeq int64
	Kind        string // run.status_changed / run.session / usage.updated / approval.requested / artifact.manifest / message.* / tool.* / run.phase_* …
	Data        map[string]any
}

// RunnerEventOutcome 报告帧处置结果：duplicate/stale 可 ACK 但不应用。
type RunnerEventOutcome string

const (
	RunnerEventApplied   RunnerEventOutcome = "applied"
	RunnerEventDuplicate RunnerEventOutcome = "duplicate"
	RunnerEventStale     RunnerEventOutcome = "stale" // 旧 lease/旧 epoch/fencing 失配
)

// RunnerEventAck 按 (run, lease, producer_seq) 粒度 ACK；一个 Run 的 ACK
// 不能清理另一个 Run 的 pending。ACK 只能在事务 commit 后发送。
type RunnerEventAck struct {
	RunID            string
	LeaseID          string
	RunnerID         string
	FencingToken     int64
	AckedProducerSeq int64
	EventID          string
	Outcome          RunnerEventOutcome
}

// ApplyRunnerEvent 是所有 Runner event 的统一应用命令（RFC §8.3.1）。
// 白名单 kind：run.status_changed / run.progress_updated / run.session /
// usage.updated / approval.requested / artifact.manifest /
// message.* / tool.* / session.compacted / subagent.updated / file_changes.reverted /
// run.phase_entered / run.phase_closed（Run Journal internal 相位帧，D2——
// 只落 run_events，经通用白名单分支走 recordRunEventTx）。
func (s *Service) ApplyRunnerEvent(ctx context.Context, in RunnerEventInput) (RunnerEventAck, error) {
	ack := RunnerEventAck{
		RunID: in.RunID, LeaseID: in.LeaseID, RunnerID: in.RunnerID,
		FencingToken: in.FencingToken, AckedProducerSeq: in.ProducerSeq,
		EventID: in.EventID, Outcome: RunnerEventApplied,
	}
	terminal := false
	poison := false
	var autoGrant *domain.ApprovalGrant
	var autoApproval *domain.ApprovalRequest
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		// ① 活动 lease/runner/fencing 校验：不匹配 = stale（旧连接/旧 lease 帧），
		//    ACK 不应用，避免 Runner 永久重放（RFC §8.3）。
		lease, err := s.store.Runners().ActiveLease(ctx, in.RunID)
		if err != nil {
			return err
		}
		if !activeLeaseMatches(lease, in.LeaseID, in.RunnerID, in.FencingToken) {
			// 终态观测例外（LateTerminalObservationKind 闭集）：终态事件在步骤⑤
			// 同事务释放 lease，而 runnerd 终态后保留租约 framing 补发 session/
			// usage/相位闭包——丢帧重传/断线重连场景下合法的迟到终态观测帧
			// 不得被打成 stale。严格身份（lease_id/run/runner/fencing 四要素）+
			// 租约确已释放 + Run 已终态，三者齐备才放行；其余一律维持 stale。
			if !LateTerminalObservationKind(in.Kind) {
				ack.Outcome = RunnerEventStale
				return nil
			}
			allowed, err := s.releasedLeaseObservationAllowed(ctx, in)
			if err != nil {
				return err
			}
			if !allowed {
				ack.Outcome = RunnerEventStale
				return nil
			}
		}
		// ② 同事务 dedup 条件插入（dedup key 不含 connection_epoch）。
		if err := s.store.Runners().RunnerEventDedupV2(ctx, in.RunID, in.LeaseID, in.RunnerID, in.ProducerSeq, in.EventID); err != nil {
			if errors.Is(err, domain.ErrIdempotencyConflict) {
				return errDuplicateRunnerEvent
			}
			return err
		}
		// ③ 按 kind 应用（复用各写点的事务内核心，语义与进程内路径一致）。
		r, err := s.store.Runs().Get(ctx, in.RunID)
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
		data := in.Data
		switch in.Kind {
		case domain.EventRunStatusChanged:
			statusName, _ := data["status"].(string)
			to := domain.RunStatus(statusName)
			if statusName == "" {
				poison = true
			} else if err := s.transitionRunLocked(ctx, r, to, data); err != nil {
				// 非法迁移（schema/状态机违约）= 永久非法帧，走毒帧收口。
				if errors.Is(err, domain.ErrIllegalTransition) || errors.Is(err, domain.ErrTerminalImmutable) {
					poison = true
				} else {
					return err
				}
			} else {
				terminal = to.IsTerminal()
			}
		case domain.EventRunProgressUpdated:
			progress, ok := runnerFloat(data["progress"])
			if !ok || progress < 0 || progress > 1 {
				poison = true
				break
			}
			if _, err := s.recordRunProgressTx(ctx, in.RunID, progress); err != nil {
				return err
			}
		case "run.session":
			update, ok := parseRunnerSession(data)
			if !ok {
				poison = true
				break
			}
			if _, err := s.recordRunSessionUpdateTx(ctx, in.RunID, update); err != nil {
				return err
			}
		case domain.EventUsageUpdated:
			inTokens, validIn := runnerNonNegativeInt(data, "input_tokens")
			outTokens, validOut := runnerNonNegativeInt(data, "output_tokens")
			cachedTokens, validCached := runnerNonNegativeInt(data, "cached_tokens")
			basis, validBasis := data["basis"].(string)
			usage := runtime.Usage{InputTokens: inTokens, OutputTokens: outTokens, CachedTokens: cachedTokens, Basis: runtime.UsageBasis(basis)}
			if !validIn || !validOut || !validCached || !validBasis {
				poison = true
				break
			}
			if usage.Basis != runtime.UsagePerRun && usage.Basis != runtime.UsageSessionCumulative {
				poison = true
				break
			}
			// provider 原生报告（可选，sealed provider-usage/v1 的 JSON 形态）：
			// 存在则还原为 typed 报告并校验 digest；类型不对/字段畸形/digest
			// 失配与数值校验失败同语义——毒帧收口，不静默丢证据。recordRunUsageTx
			// 的 bindProviderUsageReport 还会再验 run/agent/adapter 身份（双重
			// 防线）；缺席时保持 legacy 行为（老 runner/无 report adapter 兼容）。
			if raw, exists := data["provider_report"]; exists {
				report, err := decodeProviderUsageReport(raw)
				if err == nil {
					err = report.VerifyDigest()
				}
				if err != nil {
					poison = true
					break
				}
				usage.ProviderReport = report
			}
			if _, err := s.recordRunUsageTx(ctx, in.RunID, usage); err != nil {
				return err
			}
		case domain.EventApprovalRequested:
			kind, validKind := data["kind"].(string)
			risk, validRisk := data["risk"].(string)
			summary, validSummary := data["summary"].(string)
			if !validKind || kind == "" || !validRisk || (risk != "low" && risk != "medium" && risk != "high") || !validSummary {
				poison = true
				break
			}
			runnerApprovalID, _ := data["approval_id"].(string)
			if runnerApprovalID == "" {
				poison = true
				break
			}
			a, grant, err := s.requestApprovalTx(ctx, in.RunID, kind, risk, summary, runnerApprovalID)
			if err != nil {
				return err
			}
			autoApproval, autoGrant = a, grant
		case "artifact.manifest":
			sha, validSHA := data["sha256"].(string)
			path, validPath := data["logical_path"].(string)
			size, validSize := runnerNonNegativeInt(data, "size")
			mime, validMime := data["mime"].(string)
			if !validSHA || !runnerSHA256(sha) || !validPath || path == "" || !validSize || !validMime {
				poison = true
				break
			}
			art := &domain.Artifact{Sha256: sha, LogicalPath: path, Size: size, Mime: mime}
			if _, err := s.recordArtifactTx(ctx, in.RunID, art); err != nil {
				return err
			}
		default:
			// message.* / tool.* / session.compacted / subagent.updated /
			// file_changes.reverted / run.phase_entered / run.phase_closed 等
			// Run 域事件：走 RecordRunEvent 同一核心（白名单校验在 emit 内，
			// 未知事件名即毒帧；internal 相位帧在 Append 处只落 run_events）。
			if !domain.IsKnownEventName(in.Kind) {
				poison = true
				break
			}
			if _, err := s.recordRunEventTx(ctx, in.RunID, in.Kind, data); err != nil {
				return err
			}
		}
		if poison {
			// ④ 永久非法 schema/event：同事务落 failed(runner_event_invalid) +
			//    audit（dedup 行已插入）→ 正常返回 Ack，防毒帧无限循环。
			r, err := s.store.Runs().Get(ctx, in.RunID)
			if err != nil {
				return err
			}
			if !r.Status.IsTerminal() {
				if err := s.transitionRunLocked(ctx, r, domain.RunFailed, map[string]any{
					"code": "runner_event_invalid", "retryable": false,
					"message": fmt.Sprintf("非法 Runner 事件 %q（run=%s lease=%s seq=%d）", in.Kind, in.RunID, in.LeaseID, in.ProducerSeq),
					"family":  string(runtime.FamilyInternal),
				}); err != nil {
					return err
				}
			}
			s.audit(ctx, r.WorkspaceID, "runner.event_invalid", in.RunID, map[string]any{
				"kind": in.Kind, "event_id": in.EventID, "producer_seq": in.ProducerSeq,
				"lease_id": in.LeaseID, "runner_id": in.RunnerID,
			})
			terminal = true
		}
		// ⑤ Run 终态：同事务释放 lease（网关只清内存活动记录）。
		if terminal {
			if err := s.store.Runners().ReleaseLease(ctx, in.LeaseID, time.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errDuplicateRunnerEvent) {
		ack.Outcome = RunnerEventDuplicate
		return ack, nil
	}
	if err != nil {
		return RunnerEventAck{}, err // 瞬态失败：整体回滚，不 ACK
	}
	if ack.Outcome != RunnerEventApplied {
		return ack, nil // duplicate/stale：commit（无写入）后照常 ACK
	}
	if autoApproval != nil && autoGrant != nil {
		// 与进程内 RequestApproval 相同：授权命中仍先持久审批、提交后异步决议，
		// 让 remote Runner 的 approval.requested 同样享受 grant。Gateway 会从
		// ApprovalRequest.RequestedBy 恢复 runner_approval_id 后再可靠转发。
		s.autoResolveFromGrant(autoApproval, autoGrant)
	}
	// commit 后：复用 RecordRunStatus 的终态钩子管线（Coordinator retry/replan、
	// 自愈、派发收口），保证远程路径与进程内路径的终态投影一致。
	if terminal {
		if r, getErr := s.store.Runs().Get(context.WithoutCancel(ctx), in.RunID); getErr == nil {
			s.notifier.Notify(r.WorkspaceID)
			s.replayRunTerminalHooks(context.WithoutCancel(ctx), r)
		}
		return ack, nil
	}
	if r, getErr := s.store.Runs().Get(context.WithoutCancel(ctx), in.RunID); getErr == nil {
		// internal 相位帧不唤醒 workspace（与进程内 RecordRunEvent 同一闸门）：
		// journal 观测面不许触发 SSE 唤醒。
		if !domain.IsInternalEventName(in.Kind) {
			s.notifier.Notify(r.WorkspaceID)
		}
	}
	return ack, nil
}

// replayRunTerminalHooks 是 RecordRunStatus 终态管线的复用点：远程事件驱动到
// 终态后，Coordinator 推进/自愈/收口与进程内路径共用同一套投影（尽力而为）。
func (s *Service) replayRunTerminalHooks(ctx context.Context, r *domain.ExecutionRun) {
	if r == nil || !r.Status.IsTerminal() {
		return
	}
	s.dispatchedRuns.Delete(r.ID)
	if !isGovernedCoordinatorRun(r) {
		s.maybeSelfHeal(ctx, r)
	}
	if wi, werr := s.store.WorkItems().Get(ctx, r.WorkItemID); werr == nil && isTaskWorkItem(wi) {
		s.maybeAdvancePlans(ctx, r)
		s.maybeProcessVerdict(ctx, r)
		s.maybeExtractPlan(ctx, r)
		s.maybeSummarizeSegment(ctx, r)
		// canonical usage 先于 Coordinator 终态决策（决策可能触发 admission 结算）；
		// quota sweep 随其后（retry checkpoint 先落，关闭判定才能看到 pending retry）。
		s.maybeCanonicalizeRunUsage(ctx, r)
		s.maybeAdvanceTaskCoordinator(ctx, r)
		s.maybeSettleGovernanceTurnQuota(ctx, r)
		s.maybeSettleDispatch(ctx, r)
	}
}

// RunnerAcceptInput 是 run.accept 帧的应用命令输入（RFC §8.3 framing 五字段）。
// accept 语义：校验 active lease/fence/epoch 后登记 Runner 侧已接单；
// digest 失配（Runner 重建身份后重算不一致）必须走 reject 而非 accept。
type RunnerAcceptInput struct {
	RunID           string
	LeaseID         string
	RunnerID        string
	ConnectionEpoch string
	FencingToken    int64
	SnapshotDigest  string
}

// RunnerRejectInput 是 run.reject 帧的应用命令输入。
// reject 固定行为（同一事务）：CAS 释放当前 lease、清 activeRuns、
// Run 落 failed(retryable=true, family=workspace|capacity)、canonical event/outbox；
// 重复 reject 幂等；Gateway 不自行改投另一 Runner。
type RunnerRejectInput struct {
	RunID           string
	LeaseID         string
	RunnerID        string
	ConnectionEpoch string
	FencingToken    int64
	// ReasonCode：workspace（alias/repo/ref/generation/digest 不匹配）| capacity 等。
	ReasonCode    string
	ReasonFamily  string
	ReasonMessage string
}

// ApplyRunnerAccept 校验 lease/fencing 与 snapshot digest 一致后登记接单
// （audit 留痕；Run 状态推进由后续 run.status_changed 事件驱动）。瞬态失败
// 返回 error；digest 失配属永久不一致——返回错误由网关记录、Runner 端应改走
// reject（RFC §7.5：只对 offer 重算 hash 无法发现重指向，digest 是接单闸门）。
func (s *Service) ApplyRunnerAccept(ctx context.Context, in RunnerAcceptInput) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		lease, err := s.store.Runners().ActiveLease(ctx, in.RunID)
		if err != nil {
			return err
		}
		if !activeLeaseMatches(lease, in.LeaseID, in.RunnerID, in.FencingToken) {
			// 旧 lease 的迟到 accept：幂等忽略（网关 transport 校验前置已滤掉大半）。
			return nil
		}
		r, err := s.store.Runs().Get(ctx, in.RunID)
		if err != nil {
			return err
		}
		if r.Status.IsTerminal() {
			return nil
		}
		snap, err := s.store.ContextSnapshots().GetByRun(ctx, in.RunID)
		if err != nil {
			return err
		}
		if in.SnapshotDigest != "" && in.SnapshotDigest != snap.SnapshotDigest {
			s.audit(ctx, r.WorkspaceID, "runner.accept_digest_mismatch", in.RunID, map[string]any{
				"offered": in.SnapshotDigest, "snapshot": snap.SnapshotDigest,
				"runner_id": in.RunnerID, "lease_id": in.LeaseID,
			})
			return fmt.Errorf("%w: run %s accept digest 失配（offered=%s，snapshot=%s）——应走 run.reject(workspace)",
				domain.ErrWorkspaceContextMismatch, in.RunID, in.SnapshotDigest, snap.SnapshotDigest)
		}
		s.audit(ctx, r.WorkspaceID, "runner.accepted", in.RunID, map[string]any{
			"runner_id": in.RunnerID, "lease_id": in.LeaseID,
			"fencing_token": in.FencingToken, "snapshot_id": snap.ID,
			"digest": snap.SnapshotDigest,
		})
		return nil
	})
}

// ApplyRunnerReject 固定行为（RFC §8.3.1，同一事务）：CAS 释放当前 lease →
// Run 落 failed(retryable=true, family=workspace|capacity) → canonical
// event/outbox → commit 后终态钩子驱动 Coordinator 有界 retry。重复 reject
// / 旧 lease 的 reject 幂等 no-op。
func (s *Service) ApplyRunnerReject(ctx context.Context, in RunnerRejectInput) error {
	applied := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		lease, err := s.store.Runners().ActiveLease(ctx, in.RunID)
		if err != nil {
			return err
		}
		// 幂等：无活动 lease、fencing 失配（旧 lease 重放）或 Run 已终态 → no-op。
		if !activeLeaseMatches(lease, in.LeaseID, in.RunnerID, in.FencingToken) {
			return nil
		}
		r, err := s.store.Runs().Get(ctx, in.RunID)
		if err != nil {
			return err
		}
		if r.Status.IsTerminal() {
			return nil
		}
		if err := s.store.Runners().ReleaseLease(ctx, in.LeaseID, time.Now().UTC()); err != nil {
			return err
		}
		family := in.ReasonFamily
		if family == "" {
			family = in.ReasonCode
		}
		if err := s.transitionRunLocked(ctx, r, domain.RunFailed, map[string]any{
			"code":      in.ReasonCode,
			"message":   in.ReasonMessage,
			"retryable": true,
			"family":    family,
		}); err != nil {
			// reject 到达时 Run 已被并发迁移：按幂等 no-op 处理（lease 已释放）。
			if errors.Is(err, domain.ErrIllegalTransition) || errors.Is(err, domain.ErrTerminalImmutable) {
				return nil
			}
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return err
	}
	if applied {
		if r, getErr := s.store.Runs().Get(context.WithoutCancel(ctx), in.RunID); getErr == nil {
			s.notifier.Notify(r.WorkspaceID)
			s.replayRunTerminalHooks(context.WithoutCancel(ctx), r)
		}
	}
	return nil
}
