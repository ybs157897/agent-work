package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// 数据源优先级：task_sessions（带配置指纹）> 旧 execution_runs.session_ref 推断（播种兼容）。
// 指纹漂移 → 丢弃开新会话；轮换阈值超限 → Rotate；返回 (resumeRef, resumeFromRunID,
// outcome)：ref 非空且 outcome=Hit 表示 resume 候选（是否注入还看 binding 能力）。
func (s *Service) resolveResume(ctx context.Context, wi *domain.WorkItem, agentProfileID, adapterID, runtimeLabel, configDigest string, previousRuns []*domain.ExecutionRun) (string, string, resumeOutcome) {
	if adapterID != "" {
		ts, err := s.store.TaskSessions().Get(ctx, wi.WorkspaceID, agentProfileID, adapterID, wi.ID)
		if err == nil && ts != nil {
			if ref := ts.SessionRef(); ref != "" && ts.Fingerprint() == configDigest {
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
	// 播种兼容：迁移前只有 execution_runs.session_ref；同事务把可续接的旧 run 播种进 task_sessions。
	if adapterID != "" {
		if previous := resumablePreviousRun(previousRuns, adapterID, runtimeLabel, configDigest); previous != nil {
			now := time.Now().UTC()
			_ = s.store.TaskSessions().Upsert(ctx, &domain.TaskSession{
				ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: wi.WorkspaceID,
				AgentProfileID: agentProfileID, AdapterID: adapterID, TaskKey: wi.ID,
				ParentAnchorID: s.anchorParent(ctx, wi.WorkspaceID, agentProfileID, adapterID, wi.ID),
				SessionParams:  map[string]any{"__ref": previous.SessionRef, "__fingerprint": configDigest},
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
// 与 task_sessions 锚点（含指纹）同事务双写。Clear 时删除锚点，下一轮 Run 将开新会话。
func (s *Service) RecordRunSessionUpdate(ctx context.Context, runID string, update runtime.SessionUpdate) error {
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
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
		workspaceID = r.WorkspaceID
		if update.Clear {
			// 墓碑而非删除：空 __ref 让下一轮 fresh，同时阻断播种兜底复活旧会话。
			return s.writeAnchorTombstone(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID, update.ClearReason)
		}
		if update.Ref == "" {
			return fmt.Errorf("%w: session ref required", domain.ErrValidation)
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
			params := map[string]any{"__ref": update.Ref, "__from_run_id": runID}
			if digest != "" {
				params["__fingerprint"] = digest
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
			if rotated && delta == 1 {
				// 轮换代际首报：锚点整体换代（params 替换、计数清零重起、created_at 重置），
				// 本 run 即新代第 1 次，旧代计数不再参与轮换判定。
				anchor.RunsCount = 1
				if err := s.store.TaskSessions().StartGeneration(ctx, anchor); err != nil {
					return err
				}
			} else {
				anchor.RunsCount = delta
				if err := s.store.TaskSessions().Upsert(ctx, anchor); err != nil {
					return err
				}
			}
		}
		if r.SessionRef == update.Ref && r.SessionAfter == update.Ref {
			return nil
		}
		if r.Status.IsTerminal() {
			return fmt.Errorf("%w: terminal run cannot change session ref", domain.ErrValidation)
		}
		expected := r.Version
		r.SessionRef = update.Ref
		r.SessionAfter = update.Ref
		return s.store.Runs().Update(ctx, r, expected)
	})
	if err == nil && workspaceID != "" {
		s.notifier.Notify(workspaceID)
	}
	return err
}

// RecordRunUsage 落 execution_runs.usage_* 并累计 task_sessions 输入 token（轮换阈值输入）。
// 进程内（Callbacks.OnUsage）与远程（runnergateway usage.updated 帧）两条上报路径
// 汇聚于此：每次调用同事务发一条 usage.updated SSE（aggregate=execution_run，data 四字段
// 与 runDTO 一致），终态随行与过程观测帧语义对称（web 端 patch 幂等）。
func (s *Service) RecordRunUsage(ctx context.Context, runID string, usage runtime.Usage) error {
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
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
		workspaceID = r.WorkspaceID
		// 锚点输入 token 幂等累计：同一 run 有两条上报路径（Callbacks.OnUsage 与
		// ExecResult.Usage），且次数不限。按「本次值 − 该 run 上次已计入值」的差值
		// 计入（execution_runs.usage_in 是覆盖语义，正好充当 run 维度的已计入水位）：
		//   - 同值重复上报差值为 0，不双计；
		//   - 后报大于先报（增量成长）只补差；
		//   - 不同 run 从各自 run 行取水位，互不干扰。
		// 上次口径非 per_run（basis 切换）时水位视为 0，避免跨口径差值失真。
		prevIn, prevBasis := r.UsageIn, r.UsageBasis
		// 用量列不属于状态机；迟到上报直接覆盖列值（不改 status/finished_at）。
		r.UsageIn, r.UsageOut, r.UsageCached = usage.InputTokens, usage.OutputTokens, usage.CachedTokens
		r.UsageBasis = string(usage.Basis)
		if err := s.store.Runs().Update(ctx, r, r.Version); err != nil {
			return err
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
			return err
		}
		if r.AdapterID != "" && usage.Basis == runtime.UsagePerRun {
			accounted := int64(0)
			if prevBasis == string(runtime.UsagePerRun) {
				accounted = prevIn
			}
			if delta := usage.InputTokens - accounted; delta != 0 {
				return s.store.TaskSessions().AddInputTokens(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID, delta)
			}
		}
		return nil
	})
	if err == nil && workspaceID != "" {
		s.notifier.Notify(workspaceID)
	}
	return err
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

// writeAnchorTombstone 写入空 __ref 锚点：resolveResume 主路径判定 fresh，播种兜底被阻断。
func (s *Service) writeAnchorTombstone(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey, reason string) error {
	if adapterID == "" {
		return nil
	}
	now := time.Now().UTC()
	params := map[string]any{}
	if reason != "" {
		params["__cleared_reason"] = reason
	}
	return s.store.TaskSessions().Upsert(ctx, &domain.TaskSession{
		ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: workspaceID,
		AgentProfileID: agentProfileID, AdapterID: adapterID, TaskKey: taskKey,
		ParentAnchorID: s.anchorParent(ctx, workspaceID, agentProfileID, adapterID, taskKey),
		SessionParams:  params, CreatedAt: now, UpdatedAt: now,
	})
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
	if err := s.store.InTx(healCtx, func(ctx context.Context) error {
		return s.writeAnchorTombstone(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID, "session_unknown_heal")
	}); err != nil {
		return
	}
	p := CreateRunParams{AgentProfileID: r.AgentProfileID, Instruction: instruction, AutoHealOf: r.ID}
	p.OutputContract, _ = r.Input["output_contract"].(string)
	if raw, ok := r.Input["acceptance_criteria"].([]any); ok {
		for _, item := range raw {
			if text, ok := item.(string); ok {
				p.AcceptanceCriteria = append(p.AcceptanceCriteria, text)
			}
		}
	}
	p.RuntimePreference = runtimePreferenceOf(r.Input["runtime_preference"])
	retry, err := s.CreateRun(healCtx, r.WorkItemID, p)
	if err != nil {
		_ = s.activityFor(healCtx, r.WorkspaceID, r.WorkItemID, "run.self_heal_failed",
			fmt.Sprintf("session_unknown 自愈重试创建失败（源 run %s）：%v", r.ID, err))
		return
	}
	_ = s.activityFor(healCtx, r.WorkspaceID, r.WorkItemID, "run.self_healed",
		fmt.Sprintf("会话丢失（session_unknown）已自愈重试：%s → %s", r.ID, retry.ID))
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
