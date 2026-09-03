package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// 编译期保证 Service 满足 ModuleRunner 所需的应用面契约。
var _ runtime.EngineSink = (*Service)(nil)

// ── 会话轮换策略（Paperclip RotationPolicy；包级变量便于测试覆写）──────
var (
	RotationMaxRuns        = 40
	RotationMaxInputTokens = int64(1_000_000)
	RotationMaxAge         = 72 * time.Hour
	handoffMaxMessages     = 8
	handoffMaxRunesPerMsg  = 400
)

// shouldRotateSession 任一阈值超限即轮换：放弃 resume、开新会话并携带 handoff 摘要，
// 防止单会话无限膨胀（上下文窗口 / provider 会话深度限制）。
func shouldRotateSession(ts *domain.TaskSession) bool {
	if ts.RunsCount >= RotationMaxRuns {
		return true
	}
	if ts.InputTokensCum >= RotationMaxInputTokens {
		return true
	}
	if !ts.CreatedAt.IsZero() && time.Since(ts.CreatedAt) >= RotationMaxAge {
		return true
	}
	return false
}

// buildHandoffSummary 轮换交接摘要：任务状态 + 最近若干轮对话（逐条截断控制膨胀）。
// EffectiveInstruction 的轮换档将以此代替全量历史注入新会话。
func buildHandoffSummary(wi *domain.WorkItem, history []map[string]any) string {
	var b strings.Builder
	if isTaskWorkItem(wi) {
		fmt.Fprintf(&b, "【任务】%s（状态：%s）\n", wi.Title, string(wi.Status))
	} else {
		fmt.Fprintf(&b, "【对话】%s\n", wi.Title)
	}
	if len(history) > 0 {
		b.WriteString("【近期对话】\n")
		start := len(history) - handoffMaxMessages
		if start < 0 {
			start = 0
		}
		for _, m := range history[start:] {
			role, _ := m["role"].(string)
			text, _ := m["text"].(string)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if role == "assistant" {
				role = "助手"
			} else {
				role = "用户"
			}
			fmt.Fprintf(&b, "%s：%s\n", role, truncateRunes(text, handoffMaxRunesPerMsg))
		}
	}
	if isTaskWorkItem(wi) {
		b.WriteString("【继续指令】会话已轮换，请基于以上摘要继续推进该任务。")
	} else {
		b.WriteString("【继续指令】会话已轮换，请基于以上摘要继续这段对话。")
	}
	return b.String()
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// resumeOutcome resolveResume 的会话决策分类（session.decision 的 reason 输入）。
type resumeOutcome int

const (
	// resumeOutcomeNone 无锚点且无可播种旧 run → 普通全新会话。
	resumeOutcomeNone resumeOutcome = iota
	// resumeOutcomeHit 锚点有效（指纹一致）或旧 run 播种成功 → resume 候选。
	resumeOutcomeHit
	// resumeOutcomeRotate 锚点有效但轮换阈值超限（runs/tokens/age）。
	resumeOutcomeRotate
	// resumeOutcomeDrift 锚点指纹漂移 → 丢弃开新会话。
	resumeOutcomeDrift
	// resumeOutcomeTombstone 锚点空句柄（墓碑/清锚点）→ 开新会话。
	resumeOutcomeTombstone
)

// resolveResume 是 CreateRun 的会话决策点（Paperclip ResolveBeforeRun 对应物）。
// 数据源优先级：task_sessions（带组合指纹 config⊕context）> 旧 execution_runs.session_ref
// 推断（播种兼容）。指纹漂移（含 context 身份变化）→ 丢弃开新会话；轮换阈值超限 →
// Rotate；返回 (resumeRef, resumeFromRunID, outcome)：ref 非空且 outcome=Hit 表示
// resume 候选（是否注入还看 binding 能力）。
func (s *Service) resolveResume(ctx context.Context, wi *domain.WorkItem, agentProfileID, adapterID, runtimeLabel, fingerprint, configDigest string, previousRuns []*domain.ExecutionRun) (string, string, resumeOutcome) {
	if adapterID != "" {
		ts, err := s.store.TaskSessions().Get(ctx, wi.WorkspaceID, agentProfileID, adapterID, wi.ID)
		if err == nil && ts != nil {
			if ref := ts.SessionRef(); ref != "" && ts.Fingerprint() == fingerprint {
				if shouldRotateSession(ts) {
					return "", "", resumeOutcomeRotate
				}
				fromRunID, _ := ts.SessionParams["__from_run_id"].(string)
				return ref, fromRunID, resumeOutcomeHit
			}
			if ts.SessionRef() == "" {
				// 墓碑：主路径判定 fresh，播种兜底被阻断。
				return "", "", resumeOutcomeTombstone
			}
			// 指纹漂移：fresh；旧行由下一次会话上报整体覆盖。
			return "", "", resumeOutcomeDrift
		}
	}
	// 播种兼容：迁移前只有 execution_runs.session_ref；同事务把可续接的旧 run 播种进
	// task_sessions（config 半边用旧 run 的 config_digest 过滤，context 半边以当前
	// Run 的组合指纹落锚，保证后续代际比较口径一致）。
	if adapterID != "" {
		if previous := resumablePreviousRun(previousRuns, adapterID, runtimeLabel, configDigest); previous != nil {
			now := time.Now().UTC()
			_ = s.store.TaskSessions().Upsert(ctx, &domain.TaskSession{
				ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: wi.WorkspaceID,
				AgentProfileID: agentProfileID, AdapterID: adapterID, TaskKey: wi.ID,
				ParentAnchorID: s.anchorParent(ctx, wi.WorkspaceID, agentProfileID, adapterID, wi.ID),
				SessionParams:  map[string]any{"__ref": previous.SessionRef, "__fingerprint": fingerprint},
				CreatedAt:      now, UpdatedAt: now,
			})
			return previous.SessionRef, previous.ID, resumeOutcomeHit
		}
	}
	return "", "", resumeOutcomeNone
}

// anchorParent 解析子任务锚点的父锚点 id：work item 有 parent 且父任务存在
// 同 agent+adapter 锚点时返回其 id（会话树镜像任务树，轮换谱系沿链可查）；
// 否则空串（父无锚点落 NULL）。须在锚点写入事务内调用。
func (s *Service) anchorParent(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey string) string {
	if adapterID == "" {
		return ""
	}
	wi, err := s.store.WorkItems().Get(ctx, taskKey)
	if err != nil || wi.ParentID == "" {
		return ""
	}
	parent, err := s.store.TaskSessions().Get(ctx, workspaceID, agentProfileID, adapterID, wi.ParentID)
	if err != nil || parent == nil {
		return ""
	}
	return parent.ID
}

// RecordRunSessionUpdate 是会话句柄的唯一写点：execution_runs.session_ref/session_after
// 与 task_sessions 锚点（含指纹）同事务双写。Clear 时写墓碑，下一轮 Run 将开新会话。
// 锚点写入受 generation/anchor owner 门控（RFC §4.8）：旧 Run 迟到的 session/clear
// 不得覆盖新代际锚点（墓碑也不许）。
func (s *Service) RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error {
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		ws, err := s.recordRunSessionUpdateTx(ctx, runID, update)
		workspaceID = ws
		return err
	})
	if err == nil && workspaceID != "" {
		s.notifier.Notify(workspaceID)
	}
	return err
}

// recordRunSessionUpdateTx 是会话句柄写入的事务内核心（RecordRunSessionUpdate
// 与 ApplyRunnerEvent 的 run.session 全量语义应用共用）：锚点门控、墓碑、
// 锚点/轮换代际与 execution_runs 双写都在同一事务内。
func (s *Service) recordRunSessionUpdateTx(ctx context.Context, runID string, update runtime.SessionUpdate) (string, error) {
	r, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return "", err
	}
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		return "", err
	}
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return "", err
	}
	// 锚点门：generation 一致 ∧ 本 Run 是当前 anchor owner 才放行。
	allowed, ts, snap, err := s.taskSessionAnchorGate(ctx, r)
	if err != nil {
		return "", err
	}
	if !allowed {
		// 迟到帧（同 checkout 串行后旧 Run 的尾部上报）：静默丢弃，不覆盖新锚点。
		return r.WorkspaceID, nil
	}
	if update.Clear {
		// 墓碑而非删除：空 __ref 让下一轮 fresh，同时阻断播种兜底复活旧会话。
		if r.AdapterID == "" {
			return r.WorkspaceID, nil
		}
		updated, err := s.writeAnchorTombstoneForOwner(ctx, ts, update.ClearReason)
		if err != nil {
			return "", err
		}
		if !updated {
			return r.WorkspaceID, nil
		}
		return r.WorkspaceID, nil
	}
	if update.Ref == "" {
		return "", fmt.Errorf("%w: session ref required", domain.ErrValidation)
	}
	if r.AdapterID != "" {
		// 会话锚点：__ref/__fingerprint + adapter 私有参数。
		// runs_count 仅在该 run 首次报告新 ref 时 +1（OnSession 与 ExecResult.Session
		// 可能携带同一 ref 上报两次，幂等去重）。
		delta := 0
		if r.SessionRef != update.Ref {
			delta = 1
		}
		conversation, _ := r.Input["conversation"].(map[string]any)
		digest, _ := conversation["config_digest"].(string)
		rotated, _ := conversation["session_rotation"].(bool)
		fingerprint := digest
		if snap != nil {
			// 指纹含执行上下文身份：context generation/位置变化即漂移 → fresh。
			fingerprint = SessionFingerprint(digest, snap)
		}
		params := map[string]any{"__ref": update.Ref, "__from_run_id": runID}
		if fingerprint != "" {
			params["__fingerprint"] = fingerprint
		}
		for k, v := range update.Params {
			params[k] = v
		}
		now := time.Now().UTC()
		anchor := &domain.TaskSession{
			ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: r.WorkspaceID,
			AgentProfileID: r.AgentProfileID, AdapterID: r.AdapterID, TaskKey: r.WorkItemID,
			ParentAnchorID: s.anchorParent(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID),
			SessionParams:  params, DisplayID: update.DisplayID,
			CreatedAt: now, UpdatedAt: now,
		}
		s.carryAnchorOwnership(anchor, ts, snap)
		var updated bool
		if rotated && delta == 1 {
			// 轮换代际首报：锚点整体换代（params 替换、计数清零重起、created_at 重置），
			// 本 run 即新代第 1 次，旧代计数不再参与轮换判定。
			anchor.RunsCount = 1
			updated, err = s.store.TaskSessions().StartGenerationIfAnchorOwner(ctx, anchor, ts.LastRunID, ts.AnchorRunSequence)
			if err != nil {
				return "", err
			}
		} else {
			anchor.RunsCount = delta
			updated, err = s.store.TaskSessions().UpdateIfAnchorOwner(ctx, anchor, ts.LastRunID, ts.AnchorRunSequence)
			if err != nil {
				return "", err
			}
		}
		if !updated {
			// A newer Run claimed the anchor between the read gate and this callback.
			// Drop this late update exactly as taskSessionAnchorGate would.
			return r.WorkspaceID, nil
		}
	}
	if r.SessionRef == update.Ref && r.SessionAfter == update.Ref {
		return r.WorkspaceID, nil
	}
	if r.Status.IsTerminal() {
		return "", fmt.Errorf("%w: terminal run cannot change session ref", domain.ErrValidation)
	}
	expected := r.Version
	r.SessionRef = update.Ref
	r.SessionAfter = update.Ref
	if err := s.store.Runs().Update(ctx, r, expected); err != nil {
		return "", err
	}
	return r.WorkspaceID, nil
}

// carryAnchorOwnership 把 Run 的 anchor/context 归属写回锚点行（session 上报与
// claim 落同一套归属字段，防止 Upsert 的整列覆盖把归属清空）：
// context 列以本 Run 快照为准；last_run_id/anchor_run_sequence 保持既有 claim 值。
func (s *Service) carryAnchorOwnership(anchor *domain.TaskSession, ts *domain.TaskSession, snap *domain.ExecutionContextSnapshot) {
	if ts != nil {
		anchor.ContextSnapshotID = ts.ContextSnapshotID
		anchor.ContextGeneration = ts.ContextGeneration
		anchor.LastRunID = ts.LastRunID
		anchor.AnchorRunSequence = ts.AnchorRunSequence
	}
	if snap != nil {
		anchor.ContextSnapshotID = snap.ID
		anchor.ContextGeneration = snap.ContextGeneration
	}
}

// RecordRunUsage 落 execution_runs.usage_* 并累计 task_sessions 输入 token（轮换阈值输入）。
// 进程内（Callbacks.OnUsage）与远程（runnergateway usage.updated 帧）两条上报路径
// 汇聚于此：每次调用同事务发一条 usage.updated SSE（aggregate=execution_run，data 四字段
// 与 runDTO 一致），终态随行与过程观测帧语义对称（web 端 patch 幂等）。
func (s *Service) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		ws, err := s.recordRunUsageTx(ctx, runID, usage)
		workspaceID = ws
		return err
	})
	if err == nil && workspaceID != "" {
		s.notifier.Notify(workspaceID)
	}
	return err
}

// recordRunUsageTx 是用量落账的事务内核心（RecordRunUsage 与 ApplyRunnerEvent 的
// usage.updated 应用共用）：差值幂等累计口径保持一致。
func (s *Service) recordRunUsageTx(ctx context.Context, runID string, usage runtime.Usage) (string, error) {
	if err := validateRuntimeUsage(usage); err != nil {
		return "", err
	}
	r, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return "", err
	}
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		return "", err
	}
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return "", err
	}
	// 锚点输入 token 幂等累计：同一 run 有两条上报路径（Callbacks.OnUsage 与
	// ExecResult.Usage），且次数不限。按「本次值 − 该 run 上次已计入值」的差值
	// 计入（execution_runs.usage_in 是覆盖语义，正好充当 run 维度的已计入水位）：
	//   - 同值重复上报差值为 0，不双计；
	//   - 后报大于先报（增量成长）只补差；
	//   - 不同 run 从各自 run 行取水位，互不干扰。
	// 上次口径非 per_run（basis 切换）时水位视为 0，避免跨口径差值失真。
	prevIn, prevBasis := r.UsageIn, r.UsageBasis
	if prevBasis != "" && prevBasis != string(usage.Basis) {
		return "", fmt.Errorf("%w: usage basis cannot change within run %s", domain.ErrValidation, runID)
	}
	if prevBasis == string(runtime.UsagePerRun) && (usage.InputTokens < r.UsageIn ||
		usage.OutputTokens < r.UsageOut || usage.CachedTokens < r.UsageCached) {
		return "", fmt.Errorf("%w: per-run usage counters cannot regress for run %s", domain.ErrValidation, runID)
	}
	// 用量列不属于状态机；迟到上报直接覆盖列值（不改 status/finished_at）。
	r.UsageIn, r.UsageOut, r.UsageCached = usage.InputTokens, usage.OutputTokens, usage.CachedTokens
	r.UsageBasis = string(usage.Basis)
	// provider 原生报告与 legacy 投影同事务双写：身份/digest 校验失败是硬错误
	//（不静默）；canonical 已冻结时应用层跳过改写（0028 trigger 兜底）。
	if usage.ProviderReport != nil {
		if err := bindProviderUsageReport(r, usage.ProviderReport); err != nil {
			return "", err
		}
	}
	if err := s.store.Runs().Update(ctx, r, r.Version); err != nil {
		return "", err
	}
	data := map[string]any{
		"usage_in": usage.InputTokens, "usage_out": usage.OutputTokens,
		"usage_cached": usage.CachedTokens, "usage_basis": string(usage.Basis),
	}
	data["record_kind"] = string(workItemRecordKind(wi))
	// Update 在 DB 侧 version+1；emit 的 aggVersion 必须与落库后一致。
	if err := s.emit(ctx, r.WorkspaceID, domain.EventUsageUpdated,
		domain.AggregateExecutionRun, r.ID, r.Version+1,
		&RunEventRecord{RunID: r.ID, EventType: domain.EventUsageUpdated, Payload: data}, data); err != nil {
		return "", err
	}
	if r.AdapterID != "" && usage.Basis == runtime.UsagePerRun {
		accounted := int64(0)
		if prevBasis == string(runtime.UsagePerRun) {
			accounted = prevIn
		}
		if delta := usage.InputTokens - accounted; delta > 0 {
			if err := s.store.TaskSessions().AddInputTokens(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID, delta); err != nil {
				return "", err
			}
		}
	}
	// 终态随行 canonical 兜底（allowAbsentEvidence=false，复审裁决 #4）：
	// terminal + 有 report → 正常首写真 canonical；无 report → 不在报告路径
	// 合成 absent evidence（留给 sweep 的关闭时刻），保证关闭前迟到 report
	// 永远能落真实用量。canonical 写入与 anchor 推进同在 canonicalize 内收尾——
	// 若 anchor CAS 失败而 canonical 已在本事务落笔，吞错提交会留下
	// 「按旧基线结算、基线未推进」的半态，下一个 Run 会从旧水位重复做差。
	// 因此这里失败必须回滚整个 usage 事务（report/legacy 列一起回滚）：
	// 宁可丢一次迟到上报（受管 Run 由 sweep 补 unresolved），不留错误账本。
	if r.Status.IsTerminal() && r.CanonicalUsage == nil {
		if _, err := s.canonicalizeRunUsageLocked(ctx, r.ID, false); err != nil {
			return "", fmt.Errorf("terminal usage canonicalize run %s: %w", r.ID, err)
		}
	}
	return r.WorkspaceID, nil
}

func validateRuntimeUsage(usage runtime.Usage) error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CachedTokens < 0 {
		return fmt.Errorf("%w: runtime usage counters must be non-negative", domain.ErrValidation)
	}
	if usage.Basis != runtime.UsagePerRun && usage.Basis != runtime.UsageSessionCumulative {
		return fmt.Errorf("%w: runtime usage basis %q is invalid", domain.ErrValidation, usage.Basis)
	}
	if usage.ProviderReport != nil && string(usage.Basis) != usage.ProviderReport.Basis {
		return fmt.Errorf("%w: runtime usage basis differs from provider report", domain.ErrValidation)
	}
	return nil
}

// bindProviderUsageReport 把 provider 原生用量报告绑定到 Run 行的 latest report
// 槽：digest 与 run/agent/adapter 身份校验失败都是硬错误（防串账）；同一 digest
// 重复上报幂等不动，不同 digest 递增 seq（0028 trigger 兜底）。canonical 已冻结
// 后不再改写 latest report——只记日志跳过，legacy 投影列仍按既有覆盖语义更新。
func bindProviderUsageReport(r *domain.ExecutionRun, report *domain.ProviderUsageReportV1) error {
	if err := report.VerifyDigest(); err != nil {
		return fmt.Errorf("%w: provider usage report digest: %v", domain.ErrValidation, err)
	}
	if report.RunID != r.ID {
		return fmt.Errorf("%w: provider usage report run identity mismatch", domain.ErrValidation)
	}
	if r.AgentProfileID != "" && report.Provenance.AgentID != r.AgentProfileID {
		return fmt.Errorf("%w: provider usage report agent identity mismatch", domain.ErrValidation)
	}
	if r.AdapterID != "" && report.Provenance.AdapterID != r.AdapterID {
		return fmt.Errorf("%w: provider usage report adapter identity mismatch", domain.ErrValidation)
	}
	if r.CanonicalUsage != nil {
		if r.ProviderUsageReport != nil && r.ProviderUsageReport.Digest == report.Digest {
			return nil // 精确重放：既有 report 就是本报告
		}
		log.Printf("usage: run %s canonical 已落，拒绝改写 latest provider report（digest %s）", r.ID, report.Digest)
		return nil
	}
	switch {
	case r.ProviderUsageReport == nil:
		r.ProviderUsageReport = report
		r.ProviderUsageReportDigest = report.Digest
		r.ProviderUsageReportSeq = 1
	case r.ProviderUsageReport.Digest == report.Digest:
		// 同一报告重复上报（Callbacks.OnUsage 与 ExecResult 双路径）：幂等不动。
	default:
		r.ProviderUsageReport = report
		r.ProviderUsageReportDigest = report.Digest
		r.ProviderUsageReportSeq++
	}
	return nil
}

// ResetTaskSession 手动清除会话锚点（设置页 / 自愈路径）；写入墓碑，下一轮 Run 开新会话
// 且不会被旧 run 的 session_ref 播种兜底复活。
func (s *Service) ResetTaskSession(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey string) error {
	wi, err := s.store.WorkItems().Get(ctx, taskKey)
	if err != nil {
		return err
	}
	if wi.WorkspaceID != workspaceID {
		return domain.ErrNotFound
	}
	if err := requireTaskWorkItem(wi); err != nil {
		return err
	}
	return s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.writeAnchorTombstone(ctx, workspaceID, agentProfileID, adapterID, taskKey, "manual_reset"); err != nil {
			return err
		}
		message := fmt.Sprintf("已重置会话锚点（task %s / adapter %s）", taskKey, adapterID)
		return s.activityFor(ctx, workspaceID, wi.ID, "task_session.reset", message)
	})
}

// writeAnchorTombstone 是用户 reset 等显式控制面命令的 tombstone 写点。它只清
// 除调用开始时观察到的 owner；若并发 ClaimAnchor 已换代，则 CAS no-op，避免把
// 旧命令的 material 覆盖进新 owner。无行时仅 INSERT DO NOTHING。
func (s *Service) writeAnchorTombstone(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey, reason string) error {
	return s.writeAnchorTombstoneForRun(ctx, workspaceID, agentProfileID, adapterID, taskKey, "", reason)
}

// writeAnchorTombstoneForRun is the recovery form: expectedRunID non-empty
// means only that exact owner may be cleared. A retry/self-heal from an older
// Run must not clear a newer owner's provider session.
func (s *Service) writeAnchorTombstoneForRun(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey, expectedRunID, reason string) error {
	if adapterID == "" {
		return nil
	}
	now := time.Now().UTC()
	params := map[string]any{}
	if reason != "" {
		params["__cleared_reason"] = reason
	}
	anchor := &domain.TaskSession{
		ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: workspaceID,
		AgentProfileID: agentProfileID, AdapterID: adapterID, TaskKey: taskKey,
		ParentAnchorID: s.anchorParent(ctx, workspaceID, agentProfileID, adapterID, taskKey),
		SessionParams:  params, CreatedAt: now, UpdatedAt: now,
	}
	ts, err := s.store.TaskSessions().Get(ctx, workspaceID, agentProfileID, adapterID, taskKey)
	if errors.Is(err, domain.ErrNotFound) {
		_, insertErr := s.store.TaskSessions().InsertIfAbsent(ctx, anchor)
		return insertErr
	}
	if err != nil {
		return err
	}
	if expectedRunID != "" && ts.LastRunID != expectedRunID {
		return nil
	}
	_, err = s.writeAnchorTombstoneForOwner(ctx, ts, reason)
	return err
}

// writeAnchorTombstoneForOwner is the callback-side CAS form. Manual reset and
// retry orchestration intentionally use writeAnchorTombstone above; a provider
// callback must only clear the exact Run that still owns the claimed anchor.
func (s *Service) writeAnchorTombstoneForOwner(ctx context.Context, owner *domain.TaskSession, reason string) (bool, error) {
	params := map[string]any{}
	if reason != "" {
		params["__cleared_reason"] = reason
	}
	now := time.Now().UTC()
	anchor := &domain.TaskSession{
		WorkspaceID: owner.WorkspaceID, AgentProfileID: owner.AgentProfileID,
		AdapterID: owner.AdapterID, TaskKey: owner.TaskKey,
		ParentAnchorID: owner.ParentAnchorID, SessionParams: params,
		DisplayID: owner.DisplayID, CreatedAt: owner.CreatedAt, UpdatedAt: now,
	}
	return s.store.TaskSessions().UpdateIfAnchorOwner(ctx, anchor, owner.LastRunID, owner.AnchorRunSequence)
}

// TaskSessionsByAgent 列出 Agent 名下 Task 的会话锚点（设置页展示）。Chat
// session 仍由同一表作为执行基座续接，但不进入这个 Task 管理投影。
func (s *Service) TaskSessionsByAgent(ctx context.Context, workspaceID, agentProfileID string) ([]*domain.TaskSession, error) {
	sessions, err := s.store.TaskSessions().ListByAgent(ctx, workspaceID, agentProfileID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.TaskSession, 0, len(sessions))
	for _, session := range sessions {
		wi, err := s.store.WorkItems().Get(ctx, session.TaskKey)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				continue // orphan/tombstone task key is not a visible Task session
			}
			return nil, err
		}
		if wi.WorkspaceID != workspaceID || !isTaskWorkItem(wi) {
			continue
		}
		out = append(out, session)
	}
	return out, nil
}

// maybeSelfHeal：session_unknown 失败（provider 会话已丢失，resume 依据失效）的
// 终态自愈——清锚点后自动发起一次 fresh 重试，用户无需手动 reset+retry。
// 双重防护：仅当本轮确实携带 resume 且非自愈产物（input.auto_heal_of 为空）；
// 自愈 run 再次 session_unknown 失败时到此为止，失败链不会无限递归。
func (s *Service) maybeSelfHeal(ctx context.Context, r *domain.ExecutionRun) {
	if r == nil || r.Status != domain.RunFailed || r.AdapterID == "" || r.ErrorFamily != string(runtime.FamilySessionUnknown) {
		return
	}
	conversation, _ := r.Input["conversation"].(map[string]any)
	if resume, _ := conversation["resume_session_ref"].(string); resume == "" {
		return
	}
	if heal, _ := r.Input["auto_heal_of"].(string); heal != "" {
		return
	}
	instruction, _ := r.Input["instruction"].(string)
	if strings.TrimSpace(instruction) == "" {
		return
	}
	healCtx := context.WithoutCancel(ctx)
	var retry *domain.ExecutionRun
	created := false
	err := s.store.InTx(healCtx, func(txctx context.Context) error {
		source, getErr := s.store.Runs().Get(txctx, r.ID)
		if getErr != nil {
			return getErr
		}
		state, lifecycleErr := s.validateSelfHealLifecycleLocked(txctx, source)
		if lifecycleErr != nil {
			return lifecycleErr
		}
		p := selfHealRunParams(source, instruction)
		if coordinator := p.CoordinatorContext; coordinator != nil &&
			(stringValue(coordinator["role"]) == coordinatorRole || stringValue(coordinator["role"]) == coordinatorWorkerRole) {
			if state == nil {
				return fmt.Errorf("%w: coordinated self-heal has no protected state", domain.ErrStateConflict)
			}
			delegated, _ := coordinator["delegated"].(bool)
			p.coordinatorAdmission = &coordinatorRunAdmission{
				RootWorkItemID: state.RootWorkItemID, StateID: state.ID,
				SourceRunID: source.ID, Action: stringValue(coordinator["action"]), Delegated: delegated,
			}
		}
		if existing, lookupErr := s.store.Runs().GetByClientKey(txctx, source.WorkspaceID, p.ClientKey); lookupErr == nil {
			retry = existing
		} else if !errors.Is(lookupErr, domain.ErrNotFound) {
			return lookupErr
		} else {
			if err := s.writeAnchorTombstoneForRun(txctx, source.WorkspaceID, source.AgentProfileID,
				source.AdapterID, source.WorkItemID, source.ID, "session_unknown_heal"); err != nil {
				return err
			}
			retry, getErr = s.createRunLocked(txctx, source.WorkItemID, p)
			if getErr != nil {
				return getErr
			}
			created = true
		}
		return s.claimSelfHealDispatchLocked(txctx, retry)
	})
	if err != nil {
		log.Printf("session heal: atomic lifecycle/anchor/run recovery for source %s failed: %v", r.ID, err)
		_ = s.activityFor(healCtx, r.WorkspaceID, r.WorkItemID, "run.self_heal_failed",
			fmt.Sprintf("session_unknown 自愈重试创建失败（源 run %s）：%v", r.ID, err))
		return
	}
	if retry.ClientKey != "session-heal:"+r.ID {
		log.Printf("session heal: replayed Run for source %s has unexpected identity %q", r.ID, retry.ClientKey)
		return
	}
	if retry.Status != domain.RunStarting {
		return // an earlier recovery attempt already progressed this deterministic Run
	}
	if err := s.dispatchCommittedRun(healCtx, retry); err != nil {
		log.Printf("session heal: dispatch fresh run for source %s failed: %v", r.ID, err)
		return
	}
	if created {
		_ = s.activityFor(healCtx, r.WorkspaceID, r.WorkItemID, "run.self_healed",
			fmt.Sprintf("会话丢失（session_unknown）已自愈重试：%s → %s", r.ID, retry.ID))
	}
	s.recordCoordinatorSessionHeal(healCtx, r, retry)
}

func selfHealRunParams(source *domain.ExecutionRun, instruction string) CreateRunParams {
	p := CreateRunParams{AgentProfileID: source.AgentProfileID, Instruction: instruction,
		AutoHealOf: source.ID, DispatchID: source.DispatchID, ClientKey: "session-heal:" + source.ID}
	if coordinator, ok := source.Input["task_coordinator"].(map[string]any); ok {
		p.CoordinatorContext = mapsCloneAny(coordinator)
		p.CoordinatorContext["attempt"] = coordinatorAttemptValue(p.CoordinatorContext["attempt"]) + 1
		p.CoordinatorContext["retry_of"] = source.ID
	}
	if wake, ok := source.Input["wakeup"].(map[string]any); ok {
		p.WakeContext = mapsCloneAny(wake)
	}
	if evaluation, _ := source.Input["evaluation"].(bool); evaluation {
		p.Evaluation = true
	}
	if governance, ok := source.Input["governance"].(map[string]any); ok {
		p.governanceContext = mapsCloneAny(governance)
	}
	p.OutputContract, _ = source.Input["output_contract"].(string)
	if raw, ok := source.Input["acceptance_criteria"].([]any); ok {
		for _, item := range raw {
			if text, ok := item.(string); ok {
				p.AcceptanceCriteria = append(p.AcceptanceCriteria, text)
			}
		}
	}
	p.RuntimePreference = runtimePreferenceOf(source.Input["runtime_preference"])
	return p
}

func (s *Service) validateSelfHealLifecycleLocked(ctx context.Context,
	source *domain.ExecutionRun) (*domain.TaskCoordinatorState, error) {
	if err := validateCoordinatedRootHealSource(source); err != nil {
		return nil, err
	}
	workItem, err := s.store.WorkItems().Get(ctx, source.WorkItemID)
	if err != nil {
		return nil, err
	}
	if workItem.WorkspaceID != source.WorkspaceID || workItem.Status != domain.WorkItemInProgress {
		return nil, fmt.Errorf("%w: session self-heal requires an active source WorkItem", domain.ErrStateConflict)
	}
	if !isTaskWorkItem(workItem) {
		return nil, nil
	}
	if workItem.Phase != domain.PhaseExecution {
		return nil, fmt.Errorf("%w: session self-heal requires an executing Task", domain.ErrStateConflict)
	}
	root, err := s.workItemRoot(ctx, workItem)
	if err != nil {
		return nil, err
	}
	if root.WorkspaceID != source.WorkspaceID || root.Status != domain.WorkItemInProgress ||
		root.Phase != domain.PhaseExecution {
		return nil, fmt.Errorf("%w: session self-heal root Task is not active", domain.ErrStateConflict)
	}
	goal, goalErr := s.store.Goals().GetByRootWorkItem(ctx, root.ID)
	state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, root.ID)
	if goalErr != nil && !errors.Is(goalErr, domain.ErrNotFound) {
		return nil, goalErr
	}
	if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
		return nil, stateErr
	}
	if stateErr == nil && errors.Is(goalErr, domain.ErrNotFound) {
		return nil, fmt.Errorf("%w: coordinated session self-heal has no Goal", domain.ErrStateConflict)
	}
	if goalErr == nil && goal.Status != domain.GoalActive {
		return nil, fmt.Errorf("%w: Goal %s does not permit session self-heal", domain.ErrStateConflict, goal.Status)
	}
	if stateErr == nil && coordinatorStateExecutionStopped(state) {
		return nil, fmt.Errorf("%w: Coordinator %s does not permit session self-heal", domain.ErrStateConflict, state.Status)
	}
	if stateErr == nil {
		return state, nil
	}
	return nil, nil
}

func pendingSelfHealSourceID(run *domain.ExecutionRun) (string, bool) {
	if run == nil || (run.Status != domain.RunQueued && run.Status != domain.RunStarting) {
		return "", false
	}
	sourceID, _ := run.Input["auto_heal_of"].(string)
	if sourceID == "" || run.ClientKey != "session-heal:"+sourceID || run.RetryOf != sourceID {
		return "", false
	}
	return sourceID, true
}

func (s *Service) claimSelfHealDispatchLocked(ctx context.Context, run *domain.ExecutionRun) error {
	if _, ok := pendingSelfHealSourceID(run); !ok {
		return fmt.Errorf("%w: invalid pending session-heal Run", domain.ErrStateConflict)
	}
	if run.Status == domain.RunStarting {
		return nil
	}
	return s.transitionRunLocked(ctx, run, domain.RunStarting, map[string]any{
		"recovery": "session_heal_dispatch_claimed",
	})
}

// RecoverPendingSelfHealRuns closes the commit-before-dispatch crash window for
// deterministic session-heal Runs. It is called before generic orphan
// reconciliation; an active Goal/Task is dispatched once, while a paused Goal
// remains queued for ResumeGoal. Blocked/cancelled/corrupt rows fall through to
// the ordinary orphan terminalizer instead of silently starting work.
func (s *Service) RecoverPendingSelfHealRuns(ctx context.Context) (int, error) {
	return s.recoverPendingSelfHealRuns(ctx, "")
}

func (s *Service) recoverPendingSelfHealRuns(ctx context.Context, rootFilter string) (int, error) {
	if s.dispatcher == nil {
		return 0, fmt.Errorf("%w: queued self-heal recovery requires a Dispatcher", domain.ErrCapabilityMissing)
	}
	runs, err := s.store.Runs().LeaselessActive(ctx)
	if err != nil {
		return 0, err
	}
	dispatched := 0
	var firstErr error
	for _, retry := range runs {
		sourceID, ok := pendingSelfHealSourceID(retry)
		if !ok {
			continue
		}
		workItem, getErr := s.store.WorkItems().Get(ctx, retry.WorkItemID)
		if getErr != nil {
			if firstErr == nil {
				firstErr = getErr
			}
			continue
		}
		root, rootErr := s.workItemRoot(ctx, workItem)
		if rootErr != nil {
			if firstErr == nil {
				firstErr = rootErr
			}
			continue
		}
		if rootFilter != "" && root.ID != rootFilter {
			continue
		}
		source, sourceErr := s.store.Runs().Get(ctx, sourceID)
		if sourceErr != nil {
			if firstErr == nil {
				firstErr = sourceErr
			}
			continue
		}
		if source.WorkspaceID != retry.WorkspaceID || source.WorkItemID != retry.WorkItemID ||
			source.AgentProfileID != retry.AgentProfileID {
			if firstErr == nil {
				firstErr = fmt.Errorf("%w: queued self-heal Run/source identity mismatch", domain.ErrWorkspaceContextMismatch)
			}
			continue
		}
		lifecycleErr := s.store.InTx(ctx, func(txctx context.Context) error {
			freshSource, validateErr := s.store.Runs().Get(txctx, source.ID)
			if validateErr != nil {
				return validateErr
			}
			freshRetry, validateErr := s.store.Runs().Get(txctx, retry.ID)
			if validateErr != nil {
				return validateErr
			}
			if _, validateErr = s.validateSelfHealLifecycleLocked(txctx, freshSource); validateErr != nil {
				return validateErr
			}
			if validateErr = s.claimSelfHealDispatchLocked(txctx, freshRetry); validateErr != nil {
				return validateErr
			}
			retry = freshRetry
			return nil
		})
		if lifecycleErr != nil {
			if paused, pausedErr := s.selfHealPausedByGoal(ctx, retry); pausedErr == nil && paused {
				continue
			} else if pausedErr != nil && firstErr == nil {
				firstErr = pausedErr
			}
			continue // generic orphan reconciliation owns non-paused stopped rows
		}
		if _, already := s.dispatchedRuns.Load(retry.ID); already {
			continue
		}
		if dispatchErr := s.dispatchCommittedRun(context.WithoutCancel(ctx), retry); dispatchErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("dispatch queued self-heal %s: %w", retry.ID, dispatchErr)
			}
			continue
		}
		dispatched++
	}
	return dispatched, firstErr
}

func (s *Service) selfHealPausedByGoal(ctx context.Context, run *domain.ExecutionRun) (bool, error) {
	if run == nil {
		return false, nil
	}
	workItem, err := s.store.WorkItems().Get(ctx, run.WorkItemID)
	if err != nil {
		return false, err
	}
	if !isTaskWorkItem(workItem) {
		return false, nil
	}
	root, err := s.workItemRoot(ctx, workItem)
	if err != nil {
		return false, err
	}
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, root.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return goal.Status == domain.GoalWaiting && workItem.Status == domain.WorkItemInProgress &&
		workItem.Phase == domain.PhaseExecution && root.Status == domain.WorkItemInProgress &&
		root.Phase == domain.PhaseExecution, nil
}

// runtimePreferenceOf 从 run.Input 快照恢复 CreateRun 所需的显式偏好
// （经 JSON 序列化后是 map 形态，round-trip 还原）。
func runtimePreferenceOf(raw any) *domain.RuntimePreference {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var pref domain.RuntimePreference
	if json.Unmarshal(b, &pref) != nil {
		return nil
	}
	return &pref
}
