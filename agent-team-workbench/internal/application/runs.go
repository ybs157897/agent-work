package application

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentwork/codexconfig"
	"github.com/ybs/agent-team-workbench/internal/agentwork/kimiconfig"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// ── Run 创建与控制 ───────────────────────────────────────────────────

type CreateRunParams struct {
	AgentProfileID      string
	RuntimePreference   *domain.RuntimePreference
	Requirements        map[string]string
	Instruction         string
	AcceptanceCriteria  []string
	ExpectedWorkItemVer int
	// AutoHealOf 非空表示本 run 是 session_unknown 失败的一次性自愈重试（源 run ID）。
	// 固化进 input.auto_heal_of，防止自愈链无限递归。
	AutoHealOf string
	// WakeContext 非空表示本 run 由 wakeup 消费产生；固化进 input.wakeup 供审计。
	WakeContext map[string]any
	// Evaluation 表示本 run 是 plan finish{evaluation:true} 触发的评估 run；
	// 固化进 input.evaluation（verdict 提取以此门控），对齐 wakeup/auto_heal_of 惯例。
	Evaluation bool
}

// CreateRun：权威事务写入 queued Run 后才分派，避免幽灵任务（架构文档 §5）。
func (s *Service) CreateRun(ctx context.Context, workItemID string, p CreateRunParams) (*domain.ExecutionRun, error) {
	if p.Instruction == "" {
		return nil, fmt.Errorf("%w: instruction required", domain.ErrValidation)
	}
	var run *domain.ExecutionRun
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.createRunLocked(ctx, workItemID, p)
		if err != nil {
			return err
		}
		run = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(run.WorkspaceID)
	// 权威写入成功后才允许启动 Runtime 副作用。
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), run); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), run.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true})
			return nil, err
		}
	}
	return run, nil
}

// emitSessionDecision 发布 CreateRun 会话决议事件（session.decision，
// AggregateExecutionRun，纯观测面）。tier/reason 判定与 resolveResume 同源：
//
//	tier=resume    reason=resume_hit   锚点有效且 binding 声明 resume
//	tier=rotation  reason=threshold    锚点轮换阈值超限（runs/tokens/age）
//	tier=rotation  reason=budget       内联历史超模型窗口预算升级轮换
//	tier=inline    reason=session_unknown  session_unknown 自愈 fresh 重试
//	tier=inline    reason=config_drift 锚点指纹漂移，丢弃开新会话
//	tier=inline    reason=fresh        无锚点/空墓碑/播种无果的全新会话
func (s *Service) emitSessionDecision(ctx context.Context, r *domain.ExecutionRun, autoHealOf, resumeRef string,
	outcome resumeOutcome, resumeSupported, budgetRotated bool) error {
	tier, reason := "inline", "fresh"
	sessionRef := ""
	switch {
	case resumeRef != "" && resumeSupported:
		tier, reason, sessionRef = "resume", "resume_hit", resumeRef
	case outcome == resumeOutcomeRotate:
		tier, reason = "rotation", "threshold"
	case budgetRotated:
		tier, reason = "rotation", "budget"
	case autoHealOf != "":
		reason = "session_unknown"
	case outcome == resumeOutcomeDrift:
		reason = "config_drift"
	}
	data := map[string]any{"tier": tier, "reason": reason}
	if sessionRef != "" {
		data["session_ref"] = sessionRef
	}
	return s.emit(ctx, r.WorkspaceID, domain.EventSessionDecision,
		domain.AggregateExecutionRun, r.ID, r.Version, nil, data)
}

// createRunLocked 在事务内创建 queued Run（权威写入）：校验、编排快照、run 行、
// 事件与 WorkItem 联动全部同事务。副作用（Notify/Dispatch）由调用方在提交后执行——
// plan 执行器在 SubmitPlan 事务内复用本方法，子任务 run 的分派推迟到外层提交之后。
func (s *Service) createRunLocked(ctx context.Context, workItemID string, p CreateRunParams) (*domain.ExecutionRun, error) {
	wi, err := s.store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	if err := wi.CheckVersion(p.ExpectedWorkItemVer); err != nil {
		return nil, err
	}
	if wi.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: work item is terminal", domain.ErrValidation)
	}
	var agent *domain.AgentProfile
	if p.AgentProfileID != "" {
		a, err := s.store.Agents().Get(ctx, p.AgentProfileID)
		if err != nil {
			return nil, err
		}
		if a.WorkspaceID != wi.WorkspaceID {
			return nil, fmt.Errorf("%w: agent 不属于当前 workspace", domain.ErrValidation)
		}
		if a.Availability != domain.AgentEnabled {
			return nil, fmt.Errorf("%w: agent 已停用", domain.ErrValidation)
		}
		agent = a
	}
	// Harness 编排：runtime 选择 = 显式 > Agent 偏好（含 fallbacks）> 兜底；
	// 第一个 ready 的 RuntimeBinding 胜出。仅“存在”但尚未通过 Probe 的 binding
	// 不得阻断后续 fallback，否则 preferred 一次探测失败会永久锁死整个 Agent。
	label, reason, binding := orchestrator.DefaultRuntimeLabel, "default", (*domain.RuntimeBinding)(nil)
	foundCandidate := false
	for i, candidate := range orchestrator.ResolveRuntimeCandidates(p.RuntimePreference, agent) {
		b, err := s.store.Bindings().GetByLabel(ctx, wi.WorkspaceID, candidate)
		if err != nil {
			continue
		}
		foundCandidate = true
		if b.Status != domain.BindingReady {
			continue
		}
		label, binding = candidate, b
		switch i {
		case 0:
			reason = "requested"
		default:
			reason = "fallback"
		}
		break
	}
	if binding == nil && foundCandidate {
		return nil, fmt.Errorf("%w: 没有已就绪的运行环境，请检查 Runtime 探测结果", domain.ErrValidation)
	}
	if err := validateRequiredCapabilities(p.Requirements, binding); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	runID := domain.NewID(domain.PrefixRun)
	// 能力快照：Run 启动时固化 required/advertised，运行中配置变化不影响当前 Run（架构文档 §7）。
	var caps *CapabilitySnapshot
	if binding != nil {
		caps = &CapabilitySnapshot{
			ID: domain.NewID(domain.PrefixCaps), RunID: runID,
			Required:   map[string]any{"requirements": p.Requirements},
			Advertised: map[string]any{"capabilities": binding.Capabilities},
		}
	}
	capsID := ""
	if caps != nil {
		capsID = caps.ID
	}
	spec := orchestrator.EffectiveModel(agent, binding, s.ModelResolver)
	if agent != nil && agent.ModelOverride.Ref != "" && spec.Ref == "" {
		return nil, fmt.Errorf("%w: 模型注册表条目 %q 不存在", domain.ErrValidation, agent.ModelOverride.Ref)
	}
	if err := validateAdapterModel(binding, spec); err != nil {
		return nil, err
	}
	r := &domain.ExecutionRun{
		ID: runID, WorkspaceID: wi.WorkspaceID,
		WorkItemID: wi.ID, AgentProfileID: p.AgentProfileID, Status: domain.RunQueued,
		RuntimeLabel: label, CapabilitySnapshotID: capsID,
		Input: orchestrator.BuildInput(p.Instruction, p.AcceptanceCriteria, p.Requirements,
			p.RuntimePreference, agent, label, reason),
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if binding != nil {
		r.AdapterID = binding.AdapterID
		r.Provider = binding.Provider
	}
	// 编排产物补充进快照：有效模型（含注册表解析的协议/端点/凭据引用）与裁剪后权限策略（adapter 从 run.Input 读取）。
	modelSnapshot := map[string]any{}
	if spec.Ref != "" {
		modelSnapshot["ref"] = spec.Ref
	}
	if spec.ProviderID != "" {
		modelSnapshot["provider_id"] = spec.ProviderID
	}
	if spec.ProviderLabel != "" {
		modelSnapshot["provider_label"] = spec.ProviderLabel
	}
	if spec.Provider != "" {
		modelSnapshot["provider"] = spec.Provider
	}
	if spec.API != "" {
		modelSnapshot["api"] = spec.API
	}
	if spec.Model != "" {
		modelSnapshot["model"] = spec.Model
	}
	if spec.BaseURL != "" {
		modelSnapshot["base_url"] = spec.BaseURL
	}
	if spec.APIKeyEnv != "" {
		modelSnapshot["api_key_env"] = spec.APIKeyEnv
	}
	if spec.ContextWindow > 0 {
		modelSnapshot["context_window"] = spec.ContextWindow
	}
	if spec.MaxTokens > 0 {
		modelSnapshot["max_tokens"] = spec.MaxTokens
	}
	r.Input["model"] = modelSnapshot
	r.Input["mode"] = orchestrator.EffectiveMode(p.RuntimePreference, agent)
	r.Input["policy"] = orchestrator.PolicySnapshot(agent)
	configDigest := orchestrator.ConfigDigest(r.Input)
	previousRuns, err := s.store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil {
		return nil, err
	}
	history, err := s.conversationHistory(ctx, previousRuns)
	if err != nil {
		return nil, err
	}
	conversation := map[string]any{
		"id":            wi.ID,
		"turn_index":    len(previousRuns) + 1,
		"config_digest": configDigest,
		"history":       history,
	}
	resumeRef, fromRunID, outcome := s.resolveResume(ctx, wi, p.AgentProfileID, r.AdapterID, label, configDigest, previousRuns)
	// 能力协商（对齐 ResumeRun）：binding 未声明 resume=supported 时不注入
	// resume_session_ref——adapter 无法续接 provider 会话，落 tier-3 全量历史内联。
	resumeSupported := binding != nil && binding.Capabilities["resume"] == string(runtime.CapSupported)
	// 内联档（tier-3）历史超模型窗口预算 → 升级为轮换：砍头截断会移动请求
	// 前缀使 provider 缓存持续清零，轮换只付一次新前缀成本。tier-1（resume
	// 命中）上下文由 harness 持有，不适用本预算（其增长归锚点阈值管）。
	budgetRotated := false
	if !(resumeRef != "" && resumeSupported) && outcome != resumeOutcomeRotate && historyExceedsBudget(history, spec) {
		budgetRotated = true
	}
	if resumeRef != "" && resumeSupported {
		conversation["resume_session_ref"] = resumeRef
		if fromRunID != "" {
			conversation["resume_from_run_id"] = fromRunID
		}
		r.SessionBefore = resumeRef
	} else if outcome == resumeOutcomeRotate || budgetRotated {
		// 会话轮换：放弃 resume 开新会话，用 handoff 摘要代替全量历史
		//（EffectiveInstruction 轮换档）；新会话首次上报时锚点计数清零重起。
		conversation["session_rotation"] = true
		conversation["handoff_summary"] = buildHandoffSummary(wi, history)
	}
	r.Input["conversation"] = conversation
	if p.AutoHealOf != "" {
		r.Input["auto_heal_of"] = p.AutoHealOf
		// 自愈重试也是一次 retry：填 RetryOf 让重试链在领域层可追溯。
		r.RetryOf = p.AutoHealOf
	}
	if p.WakeContext != nil {
		r.Input["wakeup"] = p.WakeContext
	}
	if p.Evaluation {
		r.Input["evaluation"] = true
	}
	// 会话决议显式化（纯观测面）：为什么换了会话可查可审计。
	if err := s.emitSessionDecision(ctx, r, p.AutoHealOf, resumeRef, outcome, resumeSupported, budgetRotated); err != nil {
		return nil, err
	}
	if err := s.store.Runs().Create(ctx, r); err != nil {
		return nil, err
	}
	if caps != nil {
		if err := s.store.Caps().Create(ctx, caps); err != nil {
			return nil, err
		}
	}
	// 首版串行策略：创建 Run 时把 todo 推进到 in_progress。
	if wi.Status == domain.WorkItemTodo {
		if err := wi.Transition(domain.WorkItemInProgress, now); err != nil {
			return nil, err
		}
		if err := s.store.WorkItems().Update(ctx, wi, wi.Version-1); err != nil {
			return nil, err
		}
		if err := s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemMoved,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil,
			map[string]any{"from": "todo", "to": "in_progress"}); err != nil {
			return nil, err
		}
	} else if wi.Status == domain.WorkItemInProgress && wi.Phase != domain.PhaseExecution {
		expected := wi.Version
		wi.BeginExecution(now)
		if err := s.store.WorkItems().Update(ctx, wi, expected); err != nil {
			return nil, err
		}
		if err := s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemUpdated,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil,
			map[string]any{"phase": string(wi.Phase)}); err != nil {
			return nil, err
		}
	}
	if err := s.emit(ctx, wi.WorkspaceID, domain.EventRunCreated,
		domain.AggregateExecutionRun, r.ID, r.Version,
		&RunEventRecord{RunID: r.ID, EventType: domain.EventRunCreated, Payload: map[string]any{
			"instruction": p.Instruction,
		}},
		map[string]any{"work_item_id": wi.ID, "status": string(r.Status), "instruction": p.Instruction}); err != nil {
		return nil, err
	}
	if err := s.activity(ctx, wi.WorkspaceID, "run.created",
		fmt.Sprintf("任务「%s」创建运行 %s", wi.Title, r.ID)); err != nil {
		return nil, err
	}
	return r, nil
}

func validateRequiredCapabilities(requirements map[string]string, binding *domain.RuntimeBinding) error {
	for capability, level := range requirements {
		if level != "required" {
			continue
		}
		if binding == nil {
			return fmt.Errorf("%w: runtime capability %s", domain.ErrCapabilityMissing, capability)
		}
		actual := binding.Capabilities[capability]
		if actual == "" || actual == string(runtime.CapUnavailable) {
			return fmt.Errorf("%w: runtime capability %s", domain.ErrCapabilityMissing, capability)
		}
	}
	return nil
}

func validateAdapterModel(binding *domain.RuntimeBinding, spec orchestrator.ModelSpec) error {
	if binding == nil {
		return nil
	}
	switch binding.AdapterID {
	case "codex-appserver":
		provider := strings.ToLower(strings.TrimSpace(spec.Provider))
		if provider == "" || provider == "codex" || provider == "openai" {
			return nil
		}
		if strings.TrimSpace(spec.APIKeyEnv) == "" {
			return fmt.Errorf("%w: Codex 使用注册表模型需要 api_key_env（请在模型页保存凭据）", domain.ErrValidation)
		}
		if strings.TrimSpace(os.Getenv(spec.APIKeyEnv)) == "" {
			return fmt.Errorf("%w: 模型 %q 缺少凭据 %s（请在模型页保存对应供应商 API Key）",
				domain.ErrValidation, spec.Model, spec.APIKeyEnv)
		}
		if _, err := codexconfig.ResolveBaseURL(spec); err != nil {
			return err
		}
	case "kimi-appserver", "kimi":
		if strings.TrimSpace(spec.Model) == "" {
			return nil
		}
		if strings.TrimSpace(spec.APIKeyEnv) == "" {
			return fmt.Errorf("%w: Kimi 使用注册表模型需要 api_key_env（请在模型页保存凭据）", domain.ErrValidation)
		}
		if _, err := kimiconfig.ResolveBaseURL(spec); err != nil {
			return err
		}
	}
	return nil
}

// RetryRun 总是创建新 Run（retry_of），终态 Run 不可覆盖。
func (s *Service) RetryRun(ctx context.Context, runID string) (*domain.ExecutionRun, error) {
	parent, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if !parent.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: only terminal runs can be retried", domain.ErrValidation)
	}
	var retry *domain.ExecutionRun
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		now := time.Now().UTC()
		input := cloneInput(parent.Input)
		if conversation, ok := input["conversation"].(map[string]any); ok {
			delete(conversation, "resume_session_ref")
			delete(conversation, "resume_from_run_id")
		}
		// 重试语义 = 重新执行：锚点写墓碑（阻断播种复活），重试 run 自己的会话上报会重建锚点。
		if parent.AdapterID != "" {
			_ = s.writeAnchorTombstone(ctx, parent.WorkspaceID, parent.AgentProfileID, parent.AdapterID, parent.WorkItemID, "retry")
		}
		r := &domain.ExecutionRun{
			ID: domain.NewID(domain.PrefixRun), WorkspaceID: parent.WorkspaceID,
			WorkItemID: parent.WorkItemID, AgentProfileID: parent.AgentProfileID,
			Status: domain.RunQueued, RuntimeLabel: parent.RuntimeLabel,
			AdapterID: parent.AdapterID, Provider: parent.Provider,
			RetryOf: parent.ID, Input: input,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.Runs().Create(ctx, r); err != nil {
			return err
		}
		if err := s.emit(ctx, r.WorkspaceID, domain.EventRunCreated,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunCreated, Payload: map[string]any{
				"instruction": parent.Input["instruction"],
			}},
			map[string]any{"retry_of": parent.ID, "instruction": parent.Input["instruction"]}); err != nil {
			return err
		}
		retry = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(retry.WorkspaceID)
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), retry); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), retry.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true})
			return nil, err
		}
	}
	return retry, nil
}

func cloneInput(input map[string]any) map[string]any {
	b, _ := json.Marshal(input)
	var cloned map[string]any
	_ = json.Unmarshal(b, &cloned)
	if cloned == nil {
		cloned = map[string]any{}
	}
	return cloned
}

// ControlRun 处理 interrupt / cancel：先进入中间态，终态由 Adapter 确认。
func (s *Service) ControlRun(ctx context.Context, runID string, action string) (*domain.ExecutionRun, error) {
	var target domain.RunStatus
	switch action {
	case "interrupt":
		target = domain.RunInterrupting
	case "cancel":
		target = domain.RunCancelling
	default:
		return nil, fmt.Errorf("%w: unknown action %s", domain.ErrValidation, action)
	}
	var run *domain.ExecutionRun
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		// 未产生外部副作用（queued/starting）或连接已失（reconnecting）、
		// 终局已定（succeeding）的状态直达终态；其余经中间态由 Adapter 确认。
		switch r.Status {
		case domain.RunQueued, domain.RunStarting, domain.RunReconnecting, domain.RunSucceeding:
			if action == "cancel" {
				target = domain.RunCancelled
			} else {
				target = domain.RunInterrupted
			}
		}
		return s.transitionRunLocked(ctx, r, target, nil)
	})
	if err != nil {
		return nil, err
	}
	run, err = s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, run.WorkspaceID, "run."+action, runID, map[string]any{"status": string(run.Status)})
	s.notifier.Notify(run.WorkspaceID)
	// 迁移成功后通知 Runner/Adapter 停止执行（协议文档 §8.3 取消语义）；
	// 直达终态的迁移同样要通知（如 starting 态模块可能已在执行）。
	if s.ControlForwarder != nil && run.Status == target {
		s.ControlForwarder(ctx, runID, action)
	}
	return run, nil
}

// transitionRunLocked 在事务内迁移 Run 并发布事件；终态联动 WorkItem 与 presence。
func (s *Service) transitionRunLocked(ctx context.Context, r *domain.ExecutionRun, to domain.RunStatus, data map[string]any) error {
	expected := r.Version
	from := r.Status
	if err := r.Transition(to, time.Now().UTC()); err != nil {
		return err
	}
	// failed 时从事件 data 提取权威失败原因（脱敏后的 code/message）落库。
	if to == domain.RunFailed && data != nil {
		f := &domain.RunFailure{}
		if c, ok := data["code"].(string); ok {
			f.Code = c
		}
		if m, ok := data["message"].(string); ok {
			f.Message = m
		}
		if retry, ok := data["retryable"].(bool); ok {
			f.Retryable = retry
		}
		if fam, ok := data["family"].(string); ok {
			r.ErrorFamily = fam
		}
		if f.Code != "" || f.Message != "" {
			r.Failure = f
		}
	}
	if err := s.store.Runs().Update(ctx, r, expected); err != nil {
		return err
	}
	evType := domain.EventRunStatusChanged
	switch to {
	case domain.RunSucceeded:
		evType = domain.EventRunCompleted
	case domain.RunFailed:
		evType = domain.EventRunFailed
	case domain.RunCancelled:
		evType = domain.EventRunCancelled
	case domain.RunLost:
		evType = domain.EventRunLost
	}
	if err := s.emit(ctx, r.WorkspaceID, evType,
		domain.AggregateExecutionRun, r.ID, r.Version,
		&RunEventRecord{RunID: r.ID, EventType: evType, Payload: data},
		map[string]any{"from": string(from), "status": string(to)}); err != nil {
		return err
	}
	if to == domain.RunRunning && (from == domain.RunQueued || from == domain.RunStarting) {
		if err := s.emit(ctx, r.WorkspaceID, domain.EventRunStarted,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunStarted}, nil); err != nil {
			return err
		}
	}
	// presence 投影：运行中 busy，终态回 idle；变化时同事务发 agent_presence.updated
	//（前端对 agent_* 前缀事件刷新 agents 列表）。
	if r.AgentProfileID != "" {
		switch {
		case to == domain.RunRunning:
			if err := s.projectAgentPresence(ctx, r, domain.PresenceBusy); err != nil {
				return err
			}
		case to.IsTerminal():
			if err := s.projectAgentPresence(ctx, r, domain.PresenceIdle); err != nil {
				return err
			}
		}
	}
	// Run 成功 → WorkItem 进入评审投影；completed 只能由验收门决定。
	if to == domain.RunSucceeded {
		wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
		if err != nil {
			return err
		}
		if wi.Status == domain.WorkItemInProgress && wi.Phase != domain.PhaseReview {
			if err := wi.EnterReview(time.Now().UTC()); err == nil {
				if err := s.store.WorkItems().Update(ctx, wi, wi.Version-1); err != nil {
					return err
				}
				if err := s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemUpdated,
					domain.AggregateWorkItem, wi.ID, wi.Version, nil,
					map[string]any{"phase": string(wi.Phase)}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// projectAgentPresence 更新 agent presence 并在实际变化时同事务发
// agent_presence.updated（载荷带 agent_id/presence/run_id）。
// 事件必须与 Run 状态写入同一事务，避免前端看到状态与 presence 撕裂。
// presence 读写本身尽力而为：agent 缺失或写失败不阻塞 Run 状态迁移，只记日志。
func (s *Service) projectAgentPresence(ctx context.Context, r *domain.ExecutionRun, presence domain.AgentPresence) error {
	agent, err := s.store.Agents().Get(ctx, r.AgentProfileID)
	if err != nil {
		log.Printf("presence: run %s 读取 agent %s 失败: %v", r.ID, r.AgentProfileID, err)
		return nil
	}
	if agent.Presence == presence {
		return nil
	}
	if err := s.store.Agents().SetPresence(ctx, r.AgentProfileID, presence); err != nil {
		log.Printf("presence: run %s 更新 agent %s 失败: %v", r.ID, r.AgentProfileID, err)
		return nil
	}
	return s.emit(ctx, r.WorkspaceID, domain.EventAgentPresenceUpdated,
		domain.AggregateAgentProfile, r.AgentProfileID, agent.Version, nil,
		map[string]any{"agent_id": r.AgentProfileID, "presence": string(presence), "run_id": r.ID})
}

// ── Adapter 生命周期记录（Mock / 真实 Adapter 共用）─────────────────

// RecordRunStatus 供 Dispatcher / Adapter 上报状态变化。
func (s *Service) RecordRunStatus(ctx context.Context, runID string, to domain.RunStatus, data map[string]any) error {
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		return s.transitionRunLocked(ctx, r, to, data)
	})
	if err != nil {
		return err
	}
	r, _ := s.store.Runs().Get(ctx, runID)
	if r != nil {
		s.notifier.Notify(r.WorkspaceID)
	}
	// session_unknown 失败自愈：终态落库后清锚点并一次性 fresh 重试（maybeSelfHeal 自带防循环）。
	s.maybeSelfHeal(ctx, r)
	// M1 编排：子任务静默钩子（waiting plan 的 children_quiet 唤醒；尽力而为）。
	s.maybeAdvancePlans(ctx, r)
	// M2 评估：verdict 处理先行（phase 迁移不依赖新 plan）。
	s.maybeProcessVerdict(ctx, r)
	// M2 lead-as-planner：从 lead 最终文本提取 plan（source_run_id 唯一索引兜底幂等）。
	s.maybeExtractPlan(ctx, r)
	return nil
}

// RecordRunSessionRef 在 provider 创建/恢复真实会话后持久化私有句柄。
// 句柄只用于 Adapter 续接，不进入 Web DTO 或普通事件 payload。
// 过渡期统一入口：与 RecordRunSessionUpdate 同一写点（双写 task_sessions 锚点）。
func (s *Service) RecordRunSessionRef(ctx context.Context, runID, sessionRef string) error {
	return s.RecordRunSessionUpdate(ctx, runID, runtime.SessionUpdate{Ref: sessionRef})
}

func (s *Service) RecordRunProgress(ctx context.Context, runID string, progress float64) error {
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		if r.Status.IsTerminal() {
			return nil
		}
		r.SetProgress(progress, time.Now().UTC())
		// 未 bump 版本：以当前 DB 版本做守卫。
		if err := s.store.Runs().Update(ctx, r, r.Version); err != nil {
			return err
		}
		workspaceID = r.WorkspaceID
		// Update 在 DB 侧 version+1；emit 的 aggVersion 必须与落库后一致。
		return s.emit(ctx, r.WorkspaceID, domain.EventRunProgressUpdated,
			domain.AggregateExecutionRun, r.ID, r.Version+1,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunProgressUpdated},
			map[string]any{"progress": progress})
	})
	if err != nil {
		return err
	}
	s.notifier.Notify(workspaceID)
	return nil
}

// RecordRunEvent 追加 Run 域事件（message/tool 流），只写 run_events + stream；
// artifact.created 事件（mock 风格，载荷自带 sha256/logical_path）同时投影 artifacts 表。
func (s *Service) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		workspaceID = r.WorkspaceID
		if evType == domain.EventArtifactCreated {
			s.projectArtifactEvent(ctx, r, data)
		}
		return s.emit(ctx, r.WorkspaceID, evType,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: evType, Payload: data}, data)
	})
	if err != nil {
		return err
	}
	s.notifier.Notify(workspaceID)
	return nil
}

// projectArtifactEvent 把 artifact.created 事件载荷投影为 artifacts 行
// （draft）；载荷缺 sha256/logical_path 或落库失败时只保留事件，不建 artifact。
func (s *Service) projectArtifactEvent(ctx context.Context, r *domain.ExecutionRun, data map[string]any) {
	if data == nil {
		return
	}
	path, _ := data["logical_path"].(string)
	sha, _ := data["sha256"].(string)
	if strings.TrimSpace(path) == "" || strings.TrimSpace(sha) == "" {
		return
	}
	var size int64
	switch v := data["size"].(type) {
	case float64:
		size = int64(v)
	case int64:
		size = v
	case int:
		size = int64(v)
	}
	mime, _ := data["mime"].(string)
	art := &domain.Artifact{
		ID: domain.NewID(domain.PrefixArtifact), RunID: r.ID,
		LogicalPath: path, Mime: mime, Size: size, Sha256: sha,
		Status: domain.ArtifactDraft, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.Runs().CreateArtifact(ctx, art); err != nil {
		log.Printf("artifact: run %s artifact.created 投影失败: %v", r.ID, err)
	}
}

// RequestApproval：Run 进入 waiting_approval，等待幂等决定。命中「总是允许」
// 授权时代答：仍落 ApprovalRequest（否则决定投递早于 adapter 登记待决通道会被
// 静默丢弃，且无人工兜底），事务提交后由 grant 路径异步自决议——UI 只会看到
// 完成态行，不出现待批交互卡（设计取舍见
// notes/implemented/architecture/2026-08-24-approval-granularity.md）。
func (s *Service) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	var approval *domain.ApprovalRequest
	var grant *domain.ApprovalGrant
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		grant, err = s.store.ApprovalGrants().Matching(ctx, r.WorkspaceID, r.AgentProfileID, r.WorkItemID, kind, summary)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		a := &domain.ApprovalRequest{
			ID: domain.NewID(domain.PrefixApproval), RunID: r.ID, WorkItemID: r.WorkItemID,
			Kind: kind, Risk: risk, Status: domain.ApprovalPending, Summary: summary,
			RequestedBy: map[string]any{"kind": "runtime", "id": r.RuntimeLabel},
			CreatedAt:   now,
		}
		if err := s.store.Runs().CreateApproval(ctx, a); err != nil {
			return err
		}
		if err := s.transitionRunLocked(ctx, r, domain.RunWaitingApproval,
			map[string]any{"approval_id": a.ID}); err != nil {
			return err
		}
		if err := s.emit(ctx, r.WorkspaceID, domain.EventApprovalRequested,
			domain.AggregateApproval, a.ID, 1,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventApprovalRequested},
			map[string]any{"kind": kind, "risk": risk, "summary": summary}); err != nil {
			return err
		}
		approval = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	r, err := s.store.Runs().Get(ctx, approval.RunID)
	if err == nil {
		s.notifier.Notify(r.WorkspaceID)
	}
	if grant != nil {
		s.autoResolveFromGrant(approval, grant)
	}
	return approval, nil
}

// autoResolveFromGrant 按授权代答批准。必须异步：进程内 adapter 在
// RequestApproval 返回后才登记待决审批的消费通道，同步投递 ControlApproval
// 会在登记前被丢弃；本调用与登记之间隔着一次完整决议事务（毫秒级）vs 回调
// 返回后的直线代码（纳秒级）。失败不吞（日志留痕）且审批保持 pending 走人工
// ——退化为无授权行为。WithoutCancel：发起方请求上下文关闭不阻断代答。
func (s *Service) autoResolveFromGrant(a *domain.ApprovalRequest, g *domain.ApprovalGrant) {
	go func() {
		ctx := context.WithoutCancel(context.Background())
		reason := fmt.Sprintf("已按授权自动批准 grant %s · %s · %s 作用域", g.ID, g.Kind, g.Scope)
		if _, err := s.ResolveApproval(ctx, a.ID, true, "grant:"+g.ID, reason, domain.ApprovalScopeOnce); err != nil {
			log.Printf("approval: run %s 审批 %s 按授权 %s 自动批准失败（保持人工待决）: %v",
				a.RunID, a.ID, g.ID, err)
		}
	}()
}

// ResolveApproval 幂等决定；通过后 Run 回到 running（Adapter 继续执行）。
// plan_dispatch 审批（M4 审批护栏，RunID 空）不走 run 迁移与 forwarder，由
// resolvePlanDispatchApproval 续跑或收口 plan；副作用（分派、timer 唤醒）同
// SubmitPlan 惯例推迟到事务提交后。scope≠once 且 approved 时同事务创建授权
// （仅 command/file_change/permissions；拒绝永不建授权；幂等重放不重复建）。
func (s *Service) ResolveApproval(ctx context.Context, approvalID string, approved bool, by, reason string, scope domain.ApprovalScope) (*domain.ApprovalRequest, error) {
	if !scope.Valid() {
		return nil, fmt.Errorf("%w: 审批 scope %q 非法（once|thread|workspace）", domain.ErrValidation, string(scope))
	}
	decision := domain.ApprovalRejected
	if approved {
		decision = domain.ApprovalApproved
	}
	var result *domain.ApprovalRequest
	// changed 标记本次调用是否真实变更决定；幂等重放不重复转发 ApprovalForwarder。
	changed := false
	// plan_dispatch 放行续跑的产物（run 分派与 timer 唤醒在提交后执行）。
	var (
		resumedRuns  []*domain.ExecutionRun
		resumeWakeAt *time.Time
		resolvePlan  *domain.Plan
	)
	resolveWS := ""
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		a, err := s.store.Runs().GetApproval(ctx, approvalID)
		if err != nil {
			return err
		}
		// 授权只对工具审批开放：plan_dispatch（编排闸门）/question 等不得绕过人工。
		if scope != domain.ApprovalScopeOnce && !domain.ApprovalKindGrantable(a.Kind) {
			return fmt.Errorf("%w: kind %s 不支持授权（仅 command/file_change/permissions）",
				domain.ErrValidation, a.Kind)
		}
		if a.Status == decision {
			result = a
			return nil // 幂等：重复相同决定直接返回（不重复建授权）
		}
		if err := a.Resolve(decision, by, reason, time.Now().UTC()); err != nil {
			return err
		}
		if err := s.store.Runs().UpdateApproval(ctx, a); err != nil {
			return err
		}
		changed = true
		if a.Kind == domain.ApprovalKindPlanDispatch {
			resolvePlan, err = s.resolvePlanDispatchApproval(ctx, a, approved, reason, &resumedRuns, &resumeWakeAt)
			if err != nil {
				return err
			}
			resolveWS = resolvePlan.WorkspaceID
			if err := s.emit(ctx, resolveWS, domain.EventApprovalResolved,
				domain.AggregateApproval, a.ID, 1, nil,
				map[string]any{"decision": string(decision), "resolved_by": by}); err != nil {
				return err
			}
			s.audit(ctx, resolveWS, "approval.resolved", a.ID,
				map[string]any{"decision": string(decision), "approver": by, "reason": reason, "risk": a.Risk})
			result = a
			return s.activity(ctx, resolveWS, "approval.resolved",
				fmt.Sprintf("审批 %s：%s", a.ID, decision))
		}
		r, err := s.store.Runs().Get(ctx, a.RunID)
		if err != nil {
			return err
		}
		if approved {
			if err := s.transitionRunLocked(ctx, r, domain.RunRunning,
				map[string]any{"approval_id": a.ID, "decision": "approved"}); err != nil {
				return err
			}
		} else if r.Status == domain.RunWaitingApproval {
			// 拒绝不可自动重试：进入 cancelling，由 Adapter 确认终态。
			if err := s.transitionRunLocked(ctx, r, domain.RunCancelling,
				map[string]any{"approval_id": a.ID, "decision": "rejected"}); err != nil {
				return err
			}
		}
		if err := s.emit(ctx, r.WorkspaceID, domain.EventApprovalResolved,
			domain.AggregateApproval, a.ID, 1,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventApprovalResolved},
			map[string]any{"decision": string(decision), "resolved_by": by}); err != nil {
			return err
		}
		// scope≠once 且 approved：同事务创建「总是允许」授权（pattern 锚定本次
		// 摘要，前缀语义）。拒绝永不建授权；thread 锚定本 work item。
		if approved && scope != domain.ApprovalScopeOnce {
			g := &domain.ApprovalGrant{
				ID: domain.NewID(domain.PrefixGrant), WorkspaceID: r.WorkspaceID,
				AgentProfileID: r.AgentProfileID, Scope: scope, Kind: a.Kind,
				Pattern: a.Summary, CreatedAt: time.Now().UTC(),
			}
			if scope == domain.ApprovalScopeThread {
				g.WorkItemID = a.WorkItemID
			}
			if err := s.store.ApprovalGrants().Create(ctx, g); err != nil {
				return err
			}
			s.audit(ctx, r.WorkspaceID, "approval.grant_created", g.ID,
				map[string]any{"approval_id": a.ID, "scope": string(scope), "kind": g.Kind, "pattern": g.Pattern})
		}
		// 审批决定必须记录 approver、理由与范围（协议文档 §10.3）。
		s.audit(ctx, r.WorkspaceID, "approval.resolved", a.ID,
			map[string]any{"decision": string(decision), "approver": by, "reason": reason, "risk": a.Risk})
		result = a
		msg := fmt.Sprintf("审批 %s：%s", a.ID, decision)
		if reason != "" {
			msg += "（" + reason + "）"
		}
		return s.activity(ctx, r.WorkspaceID, "approval.resolved", msg)
	})
	if err != nil {
		return nil, err
	}
	if result != nil {
		if result.RunID != "" {
			r, err := s.store.Runs().Get(ctx, result.RunID)
			if err == nil {
				s.notifier.Notify(r.WorkspaceID)
			}
			// 转发到 Runner/Adapter，使其继续或终止执行；仅本次真实变更时转发，
			// 幂等重放不再重复投递（避免 adapter 收到重复审批决定）。
			if s.ApprovalForwarder != nil && changed {
				s.ApprovalForwarder(ctx, result.RunID, result.ID, approved)
			}
		} else if resolveWS != "" {
			// plan_dispatch 审批无 run 可转发：通知 workspace 即可；放行续跑的
			// 副作用与 SubmitPlan 同惯例（权威提交后才分派/入 timer 唤醒）。
			s.notifier.Notify(resolveWS)
			if err := s.dispatchCreatedRuns(ctx, resumedRuns); err != nil {
				return result, err
			}
			if resumeWakeAt != nil {
				s.enqueuePlanTimerWake(ctx, resolveWS, resolvePlan.AgentProfileID, resolvePlan.ID, *resumeWakeAt)
			}
		}
	}
	return result, nil
}

// RecordArtifact：服务端先校验 manifest 再记录，初始状态 draft。
func (s *Service) RecordArtifact(ctx context.Context, runID string, art *domain.Artifact) error {
	if art.Sha256 == "" || art.LogicalPath == "" {
		return fmt.Errorf("%w: sha256 and logical_path required", domain.ErrValidation)
	}
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		workspaceID = r.WorkspaceID
		art.ID = domain.NewID(domain.PrefixArtifact)
		art.RunID = r.ID
		art.Status = domain.ArtifactDraft
		art.CreatedAt = time.Now().UTC()
		if err := s.store.Runs().CreateArtifact(ctx, art); err != nil {
			return err
		}
		return s.emit(ctx, r.WorkspaceID, domain.EventArtifactCreated,
			domain.AggregateArtifact, art.ID, 1,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventArtifactCreated},
			map[string]any{"logical_path": art.LogicalPath, "size": art.Size})
	})
	if err != nil {
		return err
	}
	s.notifier.Notify(workspaceID)
	return nil
}

func (s *Service) Run(ctx context.Context, id string) (*domain.ExecutionRun, error) {
	return s.store.Runs().Get(ctx, id)
}

// RunsByWorkItem 列出一个任务的全部 Run（对话轮次历史；不可覆盖的执行尝试）。
func (s *Service) RunsByWorkItem(ctx context.Context, workItemID string) ([]*domain.ExecutionRun, error) {
	return s.store.Runs().ListByWorkItem(ctx, workItemID)
}

// RunEvents 回放单个 Run 的事件历史（对话页；replay 只重建已记录状态，不重跑 Runtime）。
func (s *Service) RunEvents(ctx context.Context, runID string) ([]RunEvent, error) {
	if _, err := s.store.Runs().Get(ctx, runID); err != nil {
		return nil, err
	}
	return s.store.Events().ListRunEvents(ctx, runID)
}

// SendRunInput 向活动 Run 追加用户输入 / steering（协议文档 §5.3）。
// 先持久化并投影事件（权威记录），再通过 InputForwarder 转发到执行端会话。
func (s *Service) SendRunInput(ctx context.Context, runID, instruction string) error {
	if instruction == "" {
		return fmt.Errorf("%w: instruction required", domain.ErrValidation)
	}
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.IsTerminal() {
		return fmt.Errorf("%w: run is terminal", domain.ErrValidation)
	}
	if s.InputForwarder == nil {
		return domain.ErrCapabilityMissing
	}
	// 先由执行端确认接收，再写 canonical 用户消息，避免 UI 显示实际未送达的 steering。
	if err := s.InputForwarder(ctx, runID, instruction); err != nil {
		return err
	}
	if err := s.RecordRunEvent(ctx, runID, domain.EventMessageDelta,
		map[string]any{"role": "user", "text": instruction}); err != nil {
		return err
	}
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		return s.activity(ctx, run.WorkspaceID, "run.input",
			fmt.Sprintf("运行 %s 收到用户输入", runID))
	})
	if err != nil {
		return err
	}
	return nil
}

// ResumeRun 仅在能力声明 resume 时可用，否则 422（协议文档 §5.3）。
// 失联不是失败：支持 resume 才恢复，否则由用户创建新 Run。
func (s *Service) ResumeRun(ctx context.Context, runID string) (*domain.ExecutionRun, error) {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Status != domain.RunReconnecting && run.Status != domain.RunLost {
		return nil, fmt.Errorf("%w: only reconnecting/lost runs can resume", domain.ErrValidation)
	}
	// 能力协商：binding 未声明 resume=supported 则显式拒绝，不静默降级。
	if run.RuntimeLabel != "" {
		binding, err := s.store.Bindings().GetByLabel(ctx, run.WorkspaceID, run.RuntimeLabel)
		if err == nil && binding.Capabilities["resume"] != string(runtime.CapSupported) {
			return nil, domain.ErrCapabilityMissing
		}
	} else {
		return nil, domain.ErrCapabilityMissing
	}
	// lost 是终态（终态不可逆，§4.3）：resume 不复活旧 run，而是基于同一会话锚点
	// 重新执行——task_sessions 锚点未清除，CreateRun 将按指纹续接 provider 会话。
	if run.Status == domain.RunLost {
		instruction, _ := run.Input["instruction"].(string)
		if strings.TrimSpace(instruction) == "" {
			return nil, fmt.Errorf("%w: lost run 缺少可恢复 instruction", domain.ErrValidation)
		}
		p := CreateRunParams{
			AgentProfileID:    run.AgentProfileID,
			Instruction:       instruction,
			RuntimePreference: runtimePreferenceOf(run.Input["runtime_preference"]),
		}
		if raw, ok := run.Input["acceptance_criteria"].([]any); ok {
			for _, item := range raw {
				if text, ok := item.(string); ok {
					p.AcceptanceCriteria = append(p.AcceptanceCriteria, text)
				}
			}
		}
		return s.CreateRun(ctx, run.WorkItemID, p)
	}
	if err := s.RecordRunStatus(ctx, runID, domain.RunRunning,
		map[string]any{"recovery": "resumed"}); err != nil {
		return nil, err
	}
	return s.store.Runs().Get(ctx, runID)
}

func (s *Service) Approvals(ctx context.Context, runID string) ([]*domain.ApprovalRequest, error) {
	return s.store.Runs().ListApprovals(ctx, runID)
}

func (s *Service) Artifacts(ctx context.Context, runID string) ([]*domain.Artifact, error) {
	return s.store.Runs().ListArtifacts(ctx, runID)
}
