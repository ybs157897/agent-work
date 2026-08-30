package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	AgentProfileID     string
	RuntimePreference  *domain.RuntimePreference
	Requirements       map[string]string
	Instruction        string
	AcceptanceCriteria []string
	// OutputContract 是请求级展示协议；只影响 system prompt 快照，不改写用户 instruction。
	OutputContract      string
	ExpectedWorkItemVer int
	// AutoHealOf 非空表示本 run 是 session_unknown 失败的一次性自愈重试（源 run ID）。
	// 固化进 input.auto_heal_of，防止自愈链无限递归。
	AutoHealOf string
	// WakeContext 非空表示本 run 由 wakeup 消费产生；固化进 input.wakeup 供审计。
	WakeContext map[string]any
	// Evaluation 表示本 run 是 plan finish{evaluation:true} 触发的评估 run；
	// 固化进 input.evaluation（verdict 提取以此门控），对齐 wakeup/auto_heal_of 惯例。
	Evaluation bool
	// DispatchTrigger 非空（当前仅 user_message）表示本 run 是 Task 的「用户消息
	// 入口」：同事务创建 trigger=user_message 的派发批次，并按 @名字前缀做直达/
	// 接诊路由（会话元模型 S1）。Chat 记录会忽略该标记，不创建派发批次。
	DispatchTrigger domain.DispatchTrigger
	// DispatchID 非空表示挂到既有批次（plan 执行器 dispatch verb 派生的子 run
	// 继承父批次）；与 DispatchTrigger 互斥使用。
	DispatchID string
	// ClientKey 非空时启用实体级幂等：同 workspace 下同 key 重复创建返回既有 run
	// （队列 drain 重试等场景；撞键时事务整体回滚，不产生重复事件与重复分派）。
	ClientKey string
	// CoordinatorContext is an internal-only control-plane envelope. Public
	// Task callers cannot choose workers once a root Coordinator exists; Plan,
	// recovery and Coordinator turns populate this map explicitly.
	CoordinatorContext map[string]any
}

// CreateRun：权威事务写入 queued Run 后才分派，避免幽灵任务（架构文档 §5）。
func (s *Service) CreateRun(ctx context.Context, workItemID string, p CreateRunParams) (*domain.ExecutionRun, error) {
	if p.Instruction == "" {
		return nil, fmt.Errorf("%w: instruction required", domain.ErrValidation)
	}
	if !orchestrator.SupportsOutputContract(p.OutputContract) {
		return nil, fmt.Errorf("%w: unsupported output_contract %q", domain.ErrValidation, p.OutputContract)
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

// CreateRunIdempotent 实体级幂等创建：client_key 撞唯一索引时查回既有 run，
// replayed=true 返回。撞键发生在事务内的 insert 点，事务整体回滚——不重复
// 发事件，也绝不会走到 Dispatch（dispatch 只在事务成功提交后执行）。
func (s *Service) CreateRunIdempotent(ctx context.Context, workItemID string, p CreateRunParams) (run *domain.ExecutionRun, replayed bool, err error) {
	run, err = s.CreateRun(ctx, workItemID, p)
	if err == nil {
		return run, false, nil
	}
	if p.ClientKey == "" || !errors.Is(err, domain.ErrIdempotencyConflict) {
		return nil, false, err
	}
	// 查回需要 workspaceID（唯一键维度之一），从 work item 取。
	wi, gerr := s.store.WorkItems().Get(ctx, workItemID)
	if gerr != nil {
		return nil, false, err
	}
	existing, gerr := s.store.Runs().GetByClientKey(ctx, wi.WorkspaceID, p.ClientKey)
	if gerr != nil {
		return nil, false, err // 查回失败时报告原始冲突错误
	}
	return existing, true, nil
}

// historyDecisionStats session.decision.history_stats 载荷：本次请求实际注入的
// 结构化回放规模观测（契约锁定，web chat.store 并行消费）。
type historyDecisionStats struct {
	Turns     int
	EstTokens int64
}

// emitSessionDecision 发布 CreateRun 会话决议事件（session.decision，
// AggregateExecutionRun，纯观测面）。tier/reason 判定与 resolveResume 同源：
//
//	tier=resume    reason=resume_hit   锚点有效且 binding 声明 resume
//	tier=rotation  reason=threshold    锚点轮换阈值超限（runs/tokens/age）
//	tier=rotation  reason=budget       内联历史 digest 压缩后仍超窗口预算
//	tier=inline    reason=session_unknown  session_unknown 自愈 fresh 重试
//	tier=inline    reason=config_drift 锚点指纹漂移，丢弃开新会话
//	tier=inline    reason=fresh        无锚点/空墓碑/播种无果的全新会话
//
// data 额外携带回放档位信息（信息降级分档留痕，设计 note
// 2026-08-27-session-integrity）：
//
//	history_tier   full|digest|handoff——注入历史的实际档位；tier=resume 无回放
//	              省略；tier=rotation 恒为 handoff；tier=inline 取 planHistoryReplay 结果
//	history_stats  {"turns","est_tokens"} 回放规模；仅在存在结构化回放（full/digest
//	              且非空）时携带——handoff 注入自由文本摘要，不伪造结构化统计
func (s *Service) emitSessionDecision(ctx context.Context, r *domain.ExecutionRun, autoHealOf, resumeRef string,
	outcome resumeOutcome, resumeSupported bool, historyTier string, stats *historyDecisionStats) error {
	tier, reason := "inline", "fresh"
	sessionRef := ""
	switch {
	case resumeRef != "" && resumeSupported:
		tier, reason, sessionRef = "resume", "resume_hit", resumeRef
	case outcome == resumeOutcomeRotate:
		tier, reason = "rotation", "threshold"
	case historyTier == "handoff":
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
	if historyTier != "" {
		data["history_tier"] = historyTier
	}
	if stats != nil {
		data["history_stats"] = map[string]any{"turns": stats.Turns, "est_tokens": stats.EstTokens}
	}
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		return err
	}
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return err
	}
	data["record_kind"] = string(workItemRecordKind(wi))
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
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return nil, err
	}
	taskRecord := isTaskWorkItem(wi)
	var coordinatorState *domain.TaskCoordinatorState
	if taskRecord {
		state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, wi.ID)
		switch {
		case stateErr == nil:
			coordinatorState = state
		case errors.Is(stateErr, domain.ErrNotFound):
		default:
			return nil, stateErr
		}
	}
	if taskRecord && wi.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: work item is terminal", domain.ErrValidation)
	}
	// HTTP 的历史入口会为每条用户消息填 user_message；只有 Task 才能拥有
	// dispatch_id/@路由，Chat 必须在事务内剥离该上层标记。
	if !taskRecord {
		if p.CoordinatorContext != nil || p.WakeContext != nil {
			return nil, fmt.Errorf("%w: Chat run 不能携带 Coordinator 或 wake context", domain.ErrValidation)
		}
		if p.DispatchID != "" {
			return nil, fmt.Errorf("%w: chat run 不能加入 task dispatch", domain.ErrValidation)
		}
		p.DispatchTrigger = ""
	}
	// 会话元模型 S1 用户消息路由：instruction 以 @名字 开头且命中本 workspace
	// agent（大小写不敏感）→ 直达该 agent；未命中/无 @ 保持既有 assignee 行为
	//（接诊）。必须在 agent 校验与 runtime 选择之前改写目标。
	atMention := ""
	if taskRecord && coordinatorState != nil && p.CoordinatorContext == nil && p.WakeContext == nil {
		return nil, fmt.Errorf("%w: coordinated Task 只能由系统 Coordinator 或其 Plan 创建 Run", domain.ErrValidation)
	}
	if taskRecord && coordinatorState == nil && p.DispatchTrigger == domain.DispatchTriggerUserMessage {
		var err error
		atMention, err = s.resolveAtMentionAgent(ctx, wi.WorkspaceID, p.Instruction)
		if err != nil {
			return nil, err
		}
		if atMention != "" {
			p.AgentProfileID = atMention
		}
	}
	var agent *domain.AgentProfile
	var coordinatorConfig *domain.TaskCoordinatorConfig
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
		if a.Kind.IsSystem() {
			if p.CoordinatorContext == nil && p.WakeContext == nil {
				return nil, fmt.Errorf("%w: system Task Coordinator 只能由控制面启动", domain.ErrValidation)
			}
			config, err := s.store.TaskCoordinators().GetConfig(ctx, wi.WorkspaceID)
			if err != nil {
				return nil, err
			}
			coordinatorConfig = config
			copyAgent := *a
			copyAgent.Instructions = CoordinatorSystemPrompt
			copyAgent.PromptVersion = domain.TaskCoordinatorPromptVersion
			copyAgent.InstructionsEditable = false
			copyAgent.ModelOverride = config.ModelRef
			if useFallback, _ := p.CoordinatorContext["use_fallback"].(bool); useFallback && config.FallbackRuntimeLabel != "" {
				copyAgent.ModelOverride = config.FallbackModelRef
			}
			agent = &copyAgent
		}
	}
	// Harness 编排：runtime 选择 = 显式 > Agent 偏好（含 fallbacks）> 兜底；
	// 第一个存在 RuntimeBinding 的候选胜出，调度原因写入快照（协议 §8.2）。
	label, reason, binding := orchestrator.DefaultRuntimeLabel, "default", (*domain.RuntimeBinding)(nil)
	for i, candidate := range orchestrator.ResolveRuntimeCandidates(p.RuntimePreference, agent) {
		b, err := s.store.Bindings().GetByLabel(ctx, wi.WorkspaceID, candidate)
		if err == nil {
			label, binding = candidate, b
			switch i {
			case 0:
				reason = "requested"
			default:
				reason = "fallback"
			}
			break
		}
	}
	if coordinatorConfig != nil {
		configuredRuntime := label == coordinatorConfig.RuntimeLabel ||
			(coordinatorConfig.FallbackRuntimeLabel != "" && label == coordinatorConfig.FallbackRuntimeLabel)
		if !configuredRuntime {
			return nil, fmt.Errorf("%w: Coordinator 配置的 Runtime 不可用，禁止静默回退到 %s", domain.ErrValidation, label)
		}
		if binding == nil {
			return nil, fmt.Errorf("%w: Coordinator Runtime %q 未配置", domain.ErrValidation, label)
		}
		if binding.Status != domain.BindingReady {
			return nil, fmt.Errorf("%w: Coordinator Runtime %q 尚未就绪", domain.ErrValidation, label)
		}
		if !domain.TaskCoordinatorRuntimeMatchesAdapter(label, binding.AdapterID) {
			return nil, fmt.Errorf("%w: Coordinator Runtime %q 与 adapter %q 不匹配", domain.ErrValidation, label, binding.AdapterID)
		}
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
	runInput := orchestrator.BuildInput(p.Instruction, p.AcceptanceCriteria, p.Requirements,
		p.RuntimePreference, agent, label, reason)
	if !orchestrator.ApplyOutputContract(runInput, p.OutputContract) {
		return nil, fmt.Errorf("%w: unsupported output_contract %q", domain.ErrValidation, p.OutputContract)
	}
	r := &domain.ExecutionRun{
		ID: runID, WorkspaceID: wi.WorkspaceID,
		WorkItemID: wi.ID, AgentProfileID: p.AgentProfileID, Status: domain.RunQueued,
		RuntimeLabel: label, CapabilitySnapshotID: capsID, ClientKey: p.ClientKey,
		Input:   runInput,
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
	if spec.ReasoningEffort != "" {
		modelSnapshot["reasoning_effort"] = spec.ReasoningEffort
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
	// 内联档三档渐进压缩（full→digest→handoff，planHistoryReplay）取代「超预算
	// 即轮换」悬崖：超预算先试 digest 老化压缩（近轮全量 + 远轮规则截断，信息
	// 分档降级并落 session.decision.history_tier）；digest 仍装不下才升级轮换——
	// 砍头截断会移动请求前缀使 provider 缓存持续清零，轮换只付一次新前缀成本。
	// tier-1（resume 命中）上下文由 harness 持有，不适用本预算（增长归锚点阈值管）。
	replay, historyTier := history, "full"
	var stats *historyDecisionStats
	if resumeRef != "" && resumeSupported {
		historyTier = "" // provider 会话缓存命中：无结构化回放，payload 省略档位字段
	} else if outcome == resumeOutcomeRotate {
		historyTier = "handoff"
	} else if plan := planHistoryReplay(history, spec); !plan.Rotated {
		replay = plan.Replay
		historyTier = plan.Tier
	} else {
		// digest 压缩后仍超预算 → handoff 终档（reason=budget）。
		historyTier = "handoff"
	}
	conversation["history"] = replay
	if historyTier == "full" || historyTier == "digest" {
		if turns, tokens := replayStats(replay); len(replay) > 0 {
			stats = &historyDecisionStats{Turns: turns, EstTokens: tokens}
		}
	}
	if resumeRef != "" && resumeSupported {
		conversation["resume_session_ref"] = resumeRef
		if fromRunID != "" {
			conversation["resume_from_run_id"] = fromRunID
		}
		r.SessionBefore = resumeRef
	} else if outcome == resumeOutcomeRotate || historyTier == "handoff" {
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
	if p.CoordinatorContext != nil {
		r.Input["task_coordinator"] = mapsCloneAny(p.CoordinatorContext)
		r.Input["coordinator_prompt_version"] = domain.TaskCoordinatorPromptVersion
	}
	// 会话决议显式化（纯观测面）：为什么换了会话可查可审计；history_tier/
	// history_stats 记录本次请求注入历史的实际档位与规模（分档留痕）。
	if err := s.emitSessionDecision(ctx, r, p.AutoHealOf, resumeRef, outcome, resumeSupported, historyTier, stats); err != nil {
		return nil, err
	}
	// 派发批次同事务落库（先于成员 run 行：dispatch_id 外键指向本表）。
	// 接诊批次与 run 互指（lead_run_id ↔ dispatch_id）：建批时 lead_run_id 落
	// NULL，run 行落库后同事务回填；@直达批次 lead_run_id 恒为 NULL。
	var newDispatch *domain.Dispatch
	leadRunID := ""
	if taskRecord && p.DispatchTrigger == domain.DispatchTriggerUserMessage {
		newDispatch = &domain.Dispatch{
			ID: domain.NewID(domain.PrefixDispatch), WorkItemID: wi.ID,
			Trigger: domain.DispatchTriggerUserMessage, Status: domain.DispatchRunning,
			CreatedAt: now,
		}
		if atMention == "" {
			leadRunID = runID
		}
		if err := s.store.Dispatches().Create(ctx, newDispatch); err != nil {
			return nil, err
		}
		r.DispatchID = newDispatch.ID
	} else if p.DispatchID != "" {
		r.DispatchID = p.DispatchID
	}
	if err := s.store.Runs().Create(ctx, r); err != nil {
		return nil, err
	}
	if newDispatch != nil {
		if leadRunID != "" {
			newDispatch.LeadRunID = leadRunID
			if err := s.store.Dispatches().SetLeadRun(ctx, newDispatch.ID, leadRunID); err != nil {
				return nil, err
			}
		}
		if err := s.emitDispatchCreated(ctx, wi.WorkspaceID, newDispatch); err != nil {
			return nil, err
		}
	}
	if caps != nil {
		if err := s.store.Caps().Create(ctx, caps); err != nil {
			return nil, err
		}
	}
	// 任务状态机只属于 Task；Chat 记录保持自己的消息生命周期，不被
	// in_progress/review/acceptance 这些任务投影污染。
	if taskRecord {
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
				map[string]any{"from": "todo", "to": "in_progress", "record_kind": string(workItemRecordKind(wi))}); err != nil {
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
				map[string]any{"phase": string(wi.Phase), "record_kind": string(workItemRecordKind(wi))}); err != nil {
				return nil, err
			}
		}
	} else {
		// Keep Chat conversations ordered by their latest message without changing
		// any task state, phase, lock, or optimistic version.
		if err := s.store.WorkItems().TouchUpdatedAt(ctx, wi.ID, now); err != nil {
			return nil, err
		}
	}
	if err := s.emit(ctx, wi.WorkspaceID, domain.EventRunCreated,
		domain.AggregateExecutionRun, r.ID, r.Version,
		&RunEventRecord{RunID: r.ID, EventType: domain.EventRunCreated, Payload: map[string]any{
			"instruction": p.Instruction, "record_kind": string(workItemRecordKind(wi)),
		}},
		map[string]any{"work_item_id": wi.ID, "status": string(r.Status), "instruction": p.Instruction,
			"record_kind": string(workItemRecordKind(wi))}); err != nil {
		return nil, err
	}
	if err := s.activityFor(ctx, wi.WorkspaceID, wi.ID, "run.created",
		fmt.Sprintf("%s「%s」创建运行 %s", workItemNoun(wi), wi.Title, r.ID)); err != nil {
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
		if err := codexconfig.ValidateProviderAPI(spec); err != nil {
			return err
		}
		provider := strings.ToLower(strings.TrimSpace(spec.Provider))
		if provider == "" || provider == "codex" || provider == "openai" {
			return nil
		}
		if strings.TrimSpace(spec.APIKeyEnv) == "" {
			return fmt.Errorf("%w: Codex 使用注册表模型需要 api_key_env（请在模型页保存凭据）", domain.ErrValidation)
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
	return s.retryRun(ctx, runID, false)
}

func (s *Service) retryRun(ctx context.Context, runID string, coordinatorOwned bool) (*domain.ExecutionRun, error) {
	parent, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	wi, err := s.store.WorkItems().Get(ctx, parent.WorkItemID)
	if err != nil {
		return nil, err
	}
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return nil, err
	}
	if _, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, wi.ID); err == nil && !coordinatorOwned {
		return nil, fmt.Errorf("%w: coordinated Task 的 retry 由系统 Coordinator 管理", domain.ErrValidation)
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	if !parent.Status.IsTerminal() {
		return nil, fmt.Errorf("%w: only terminal runs can be retried", domain.ErrValidation)
	}
	var retry *domain.ExecutionRun
	retryClientKey := ""
	if coordinatorOwned {
		retryClientKey = "coordinator-retry:" + parent.ID
	}
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		var createErr error
		retry, createErr = s.createRetryRunLocked(ctx, parent, wi, retryClientKey)
		return createErr
	})
	if err != nil {
		if coordinatorOwned && errors.Is(err, domain.ErrIdempotencyConflict) {
			existing, getErr := s.store.Runs().GetByClientKey(ctx, parent.WorkspaceID, retryClientKey)
			if getErr == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	s.notifier.Notify(retry.WorkspaceID)
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), retry); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), retry.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true})
			// Keep the persisted retry in the result so Coordinator-owned callers
			// can observe the terminal hook's bounded retry/replan decision. Public
			// RetryRun still receives the dispatch error, but must not lose the
			// durable Run identity created before the side effect.
			return retry, err
		}
	}
	return retry, nil
}

func (s *Service) createRetryRunLocked(ctx context.Context, parent *domain.ExecutionRun,
	wi *domain.WorkItem, retryClientKey string) (*domain.ExecutionRun, error) {
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
	run := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: parent.WorkspaceID,
		WorkItemID: parent.WorkItemID, AgentProfileID: parent.AgentProfileID,
		Status: domain.RunQueued, RuntimeLabel: parent.RuntimeLabel,
		AdapterID: parent.AdapterID, Provider: parent.Provider,
		RetryOf: parent.ID, DispatchID: parent.DispatchID, ClientKey: retryClientKey, Input: input,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if coordinator, ok := input["task_coordinator"].(map[string]any); ok {
		coordinator["attempt"] = coordinatorAttemptValue(coordinator["attempt"]) + 1
		coordinator["retry_of"] = parent.ID
	}
	if err := s.store.Runs().Create(ctx, run); err != nil {
		return nil, err
	}
	if !isTaskWorkItem(wi) {
		// Retry is another Chat turn; keep its list ordering current without
		// applying the Task status/phase/lock projection.
		if err := s.store.WorkItems().TouchUpdatedAt(ctx, wi.ID, now); err != nil {
			return nil, err
		}
	}
	if err := s.emit(ctx, run.WorkspaceID, domain.EventRunCreated,
		domain.AggregateExecutionRun, run.ID, run.Version,
		&RunEventRecord{RunID: run.ID, EventType: domain.EventRunCreated, Payload: map[string]any{
			"instruction": parent.Input["instruction"],
			"record_kind": string(workItemRecordKind(wi)),
		}},
		map[string]any{"retry_of": parent.ID, "instruction": parent.Input["instruction"],
			"record_kind": string(workItemRecordKind(wi))}); err != nil {
		return nil, err
	}
	return run, nil
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
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		return err
	}
	if err := requireValidWorkItemRecordKind(wi); err != nil {
		return err
	}
	taskRecord := isTaskWorkItem(wi)
	expected := r.Version
	from := r.Status
	// F1 任务锁：run 首次进 running（queued/starting → running）先裁决任务执行锁，
	// 防同一任务双跑。被活跃锁拒绝时不留半态——本 run 直接落 failed(work_item_locked)
	// 终态，保证任何推进尝试都有终态归宿（红线：任何 Outcome 必须能落终态）。
	if taskRecord && to == domain.RunRunning && (from == domain.RunQueued || from == domain.RunStarting) {
		if err := s.acquireTaskLock(ctx, r); err != nil {
			if !errors.Is(err, ErrWorkItemLocked) {
				return err
			}
			return s.transitionRunLocked(ctx, r, domain.RunFailed, map[string]any{
				"code": "work_item_locked", "message": err.Error(), "retryable": true,
			})
		}
	}
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
	statusData := map[string]any{"from": string(from), "status": string(to),
		"record_kind": string(workItemRecordKind(wi))}
	if err := s.emit(ctx, r.WorkspaceID, evType,
		domain.AggregateExecutionRun, r.ID, r.Version,
		&RunEventRecord{RunID: r.ID, EventType: evType, Payload: withWorkItemRecordKind(data, wi)},
		statusData); err != nil {
		return err
	}
	// F1 任务锁：run 落终态时释放其持有的任务执行锁（同事务；属主已被抢占/回收
	// 时不误伤他人的锁）。
	if taskRecord && to.IsTerminal() {
		if err := s.releaseTaskLock(ctx, r); err != nil {
			return err
		}
	}
	if to == domain.RunRunning && (from == domain.RunQueued || from == domain.RunStarting) {
		if err := s.emit(ctx, r.WorkspaceID, domain.EventRunStarted,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunStarted,
				Payload: map[string]any{"record_kind": string(workItemRecordKind(wi))}},
			map[string]any{"record_kind": string(workItemRecordKind(wi))}); err != nil {
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
	if taskRecord && to == domain.RunSucceeded && !coordinatorRunDefersReview(r) {
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
					map[string]any{"phase": string(wi.Phase), "record_kind": string(workItemRecordKind(wi))}); err != nil {
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
	// session_unknown 失败自愈：系统 Coordinator 由自己的有界恢复策略负责，
	// 普通/worker Run 继续沿用既有一次性 fresh 自愈。
	if !isSystemCoordinatorRun(r) {
		s.maybeSelfHeal(ctx, r)
	}
	if r != nil {
		if wi, werr := s.store.WorkItems().Get(ctx, r.WorkItemID); werr == nil && isTaskWorkItem(wi) {
			// M1 编排：子任务静默钩子（waiting plan 的 children_quiet 唤醒；尽力而为）。
			s.maybeAdvancePlans(ctx, r)
			// M2 评估：verdict 处理先行（phase 迁移不依赖新 plan）。
			s.maybeProcessVerdict(ctx, r)
			// M2 lead-as-planner：从 lead 最终文本提取 plan（source_run_id 唯一索引兜底幂等）。
			s.maybeExtractPlan(ctx, r)
			// S2 任务台账：片段关闭（run 终态）自动重算滚动摘要（确定性，尽力而为）。
			s.maybeSummarizeSegment(ctx, r)
			// 系统 Coordinator：在派发收口前创建有界 retry/recovery，保证新成员
			// 能进入原 dispatch，避免批次先被错误收成 degraded。
			s.maybeAdvanceTaskCoordinator(ctx, r)
			// S3 派发收口：worker→lead 回流唤醒与批次终态收口（尽力而为）。
			s.maybeSettleDispatch(ctx, r)
		}
	}
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
		r.SetProgress(progress, time.Now().UTC())
		// 未 bump 版本：以当前 DB 版本做守卫。
		if err := s.store.Runs().Update(ctx, r, r.Version); err != nil {
			return err
		}
		workspaceID = r.WorkspaceID
		// Update 在 DB 侧 version+1；emit 的 aggVersion 必须与落库后一致。
		return s.emit(ctx, r.WorkspaceID, domain.EventRunProgressUpdated,
			domain.AggregateExecutionRun, r.ID, r.Version+1,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunProgressUpdated,
				Payload: map[string]any{"progress": progress, "record_kind": string(workItemRecordKind(wi))}},
			map[string]any{"progress": progress, "record_kind": string(workItemRecordKind(wi))})
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
		wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
		if err != nil {
			return err
		}
		if err := requireValidWorkItemRecordKind(wi); err != nil {
			return err
		}
		workspaceID = r.WorkspaceID
		eventData := withWorkItemRecordKind(data, wi)
		if evType == domain.EventArtifactCreated {
			s.projectArtifactEvent(ctx, r, eventData)
		}
		if err := s.emit(ctx, r.WorkspaceID, evType,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: evType, Payload: eventData}, eventData); err != nil {
			return err
		}
		if evType == domain.EventMessageDelta && eventData["role"] == "user" && !isTaskWorkItem(wi) {
			if err := s.store.WorkItems().TouchUpdatedAt(ctx, wi.ID, time.Now().UTC()); err != nil {
				return err
			}
		}
		return nil
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
		return
	}
	s.indexArtifact(ctx, r, art)
}

// indexArtifact 是两条产物入口共用的 S4 投影：mock 的 artifact.created
// 事件和真实 Runner 的 artifact.manifest 都必须用相同的 (kind, source_id)
// 定点索引，避免只在其中一条入口可搜到产物。
func (s *Service) indexArtifact(ctx context.Context, r *domain.ExecutionRun, art *domain.Artifact) {
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil {
		log.Printf("artifact: run %s 读取 work item %s 失败，跳过 Task 搜索索引: %v", r.ID, r.WorkItemID, err)
		return
	}
	if !isTaskWorkItem(wi) {
		return
	}
	// artifact 条目标题为 logical_path，正文暂为空；索引失败不阻塞产物事件/记录，
	// 可由后续投影重放或定点重写补齐。
	if err := s.store.Search().IndexEntry(ctx, &SearchEntry{
		WorkItemID: r.WorkItemID, Kind: SearchKindArtifact, SourceID: art.ID,
		Title: art.LogicalPath, Body: "",
	}); err != nil {
		log.Printf("artifact: run %s 检索索引写入失败（artifact %s）: %v", r.ID, art.ID, err)
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
		wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
		if err != nil {
			return err
		}
		if err := requireValidWorkItemRecordKind(wi); err != nil {
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
			&RunEventRecord{RunID: r.ID, EventType: domain.EventApprovalRequested,
				Payload: map[string]any{"record_kind": string(workItemRecordKind(wi))}},
			map[string]any{"kind": kind, "risk": risk, "summary": summary,
				"record_kind": string(workItemRecordKind(wi))}); err != nil {
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
				map[string]any{"decision": string(decision), "resolved_by": by,
					"record_kind": string(domain.RecordKindTask)}); err != nil {
				return err
			}
			s.audit(ctx, resolveWS, "approval.resolved", a.ID,
				map[string]any{"decision": string(decision), "approver": by, "reason": reason, "risk": a.Risk})
			result = a
			return s.activityFor(ctx, resolveWS, resolvePlan.WorkItemID, "approval.resolved",
				fmt.Sprintf("审批 %s：%s", a.ID, decision))
		}
		r, err := s.store.Runs().Get(ctx, a.RunID)
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
			&RunEventRecord{RunID: r.ID, EventType: domain.EventApprovalResolved,
				Payload: map[string]any{"record_kind": string(workItemRecordKind(wi))}},
			map[string]any{"decision": string(decision), "resolved_by": by,
				"record_kind": string(workItemRecordKind(wi))}); err != nil {
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
		return s.activityFor(ctx, r.WorkspaceID, r.WorkItemID, "approval.resolved", msg)
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
				if err := s.enqueuePlanTimerWake(ctx, resolveWS, resolvePlan.AgentProfileID, resolvePlan.ID, *resumeWakeAt); err != nil {
					if recoveryErr := s.schedulePlanTimerRecovery(ctx, resolvePlan, *resumeWakeAt, err); recoveryErr != nil {
						return result, recoveryErr
					}
				}
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
		wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
		if err != nil {
			return err
		}
		if err := requireValidWorkItemRecordKind(wi); err != nil {
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
		s.indexArtifact(ctx, r, art)
		eventData := withWorkItemRecordKind(map[string]any{"logical_path": art.LogicalPath, "size": art.Size}, wi)
		return s.emit(ctx, r.WorkspaceID, domain.EventArtifactCreated,
			domain.AggregateArtifact, art.ID, 1,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventArtifactCreated, Payload: eventData}, eventData)
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
		return s.activityFor(ctx, run.WorkspaceID, run.WorkItemID, "run.input",
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
		p.OutputContract, _ = run.Input["output_contract"].(string)
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
