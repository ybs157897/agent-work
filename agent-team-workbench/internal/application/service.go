package application

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledge"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Service 编排用例与事务边界。命令流程：
// 校验 → 领域状态机 → 同事务写状态 + 事件 + outbox → 提交后通知 SSE / 分派 Runtime。
type Service struct {
	store      Store
	dispatcher Dispatcher
	notifier   Notifier
	adapters   *runtime.Registry
	// ApprovalForwarder / ControlForwarder 把决定转发到 Runner WSS（M2）；
	// 无 Runner 的内置 Mock 路径不需要。
	ApprovalForwarder func(ctx context.Context, runID, approvalID string, approved bool)
	ControlForwarder  func(ctx context.Context, runID, action string)
	// InputForwarder 把用户 steering 输入转发到活动 Run 的执行端（adapter 同 session 追加 prompt）。
	InputForwarder func(ctx context.Context, runID, instruction string) error
	// ModelResolver 按 ref 查 models/ 注册表（装配层注入；nil 时跳过注册表层）。
	ModelResolver orchestrator.ModelResolver
	// Knowledge 知识语料检索器（M2 consult_knowledge 动词依赖，装配层注入，
	// 与 ModelResolver 同风格）；nil 时该步骤响亮失败（error=no_retriever），
	// 绝不静默降级。
	Knowledge knowledge.Retriever
}

func NewService(store Store, dispatcher Dispatcher, notifier Notifier, adapters *runtime.Registry) *Service {
	return &Service{store: store, dispatcher: dispatcher, notifier: notifier, adapters: adapters}
}

// SetDispatcher 用于打破 Service ↔ Gateway/Adapter 的构造环（启动时一次性注入）。
func (s *Service) SetDispatcher(d Dispatcher) { s.dispatcher = d }

// emit 在事务内追加 Canonical Event（stream_events + outbox 同事务）。
func (s *Service) emit(ctx context.Context, workspaceID, evType, aggType, aggID string, aggVersion int, runEvent *RunEventRecord, data map[string]any) error {
	ev, err := domain.NewCanonicalEvent(workspaceID, evType, aggType, aggID, aggVersion, data)
	if err != nil {
		return err
	}
	if _, err := s.store.Events().Append(ctx, ev, runEvent); err != nil {
		return err
	}
	return nil
}

// activity 写 activity 流并发布 activity.appended 事件（无归因形态）。
// 自包 InTx：外层无事务时独立成事务；外层已在事务内时 store.InTx 幂等复用。
// activities 行与 stream_events/outbox 必须同事务提交——两条独立 autocommit
// 在中途崩溃时会留下「有事件无活动」或反向的分裂状态。
func (s *Service) activity(ctx context.Context, workspaceID, kind, message string) error {
	return s.activityFor(ctx, workspaceID, "", kind, message)
}

// activityFor 写带 work item 归因的 activity（M4：verdict 处理与 blocker 落库
// 需能回溯到任务）。workItemID 非空时 activity 行与 activity.appended 事件
// data 同步携带 work_item_id；空串等价无归因。
func (s *Service) activityFor(ctx context.Context, workspaceID, workItemID, kind, message string) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Events().AppendActivityFor(ctx, workspaceID, workItemID, kind, message); err != nil {
			return err
		}
		data := map[string]any{
			"kind": kind, "message": message,
		}
		if workItemID != "" {
			data["work_item_id"] = workItemID
		}
		return s.emit(ctx, workspaceID, domain.EventActivityCreated,
			domain.AggregateWorkspace, workspaceID, 0, nil, data)
	})
}

// audit 写不可变审计记录（协议文档 §10.1：审批、运行控制、凭据变更、验收）。
// M1 演示用户固定；RBAC 会话接入后替换 actor。
func (s *Service) audit(ctx context.Context, workspaceID, action, target string, detail map[string]any) {
	actor := map[string]any{"kind": "user", "id": "user_demo"}
	if err := s.store.Audit().Append(ctx, workspaceID, actor, action, target, detail); err != nil {
		log.Printf("audit: workspace %s action %s target %s 写入失败: %v", workspaceID, action, target, err)
	}
}

// ── AgentProfile ─────────────────────────────────────────────────────

type CreateAgentParams struct {
	Name              string
	Role              string
	Skills            []string
	RuntimePreference domain.RuntimePreference
	Avatar            string
}

func (s *Service) CreateAgent(ctx context.Context, workspaceID string, p CreateAgentParams) (*domain.AgentProfile, error) {
	if p.Name == "" || p.Role == "" {
		return nil, fmt.Errorf("%w: name and role required", domain.ErrValidation)
	}
	if p.RuntimePreference.Mode != "" && p.RuntimePreference.Mode != "default" && p.RuntimePreference.Mode != "plan" {
		return nil, fmt.Errorf("%w: mode 必须是 default|plan", domain.ErrValidation)
	}
	if p.RuntimePreference.Preferred != "" {
		if _, err := s.store.Bindings().GetByLabel(ctx, workspaceID, p.RuntimePreference.Preferred); err != nil {
			return nil, fmt.Errorf("%w: runtime %q 不存在", domain.ErrValidation, p.RuntimePreference.Preferred)
		}
	}
	now := time.Now().UTC()
	a := &domain.AgentProfile{
		ID: domain.NewID(domain.PrefixAgent), WorkspaceID: workspaceID,
		Name: p.Name, Role: p.Role, Skills: p.Skills, Avatar: p.Avatar,
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: p.RuntimePreference, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	// M4 唤醒缺省与迁移列缺省一致：指派/手动唤醒默认开，心跳自主唤醒 opt-in。
	a.WakeOnAssignment, a.WakeOnDemand = true, true
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Agents().Create(ctx, a); err != nil {
			return err
		}
		if err := s.emit(ctx, workspaceID, domain.EventAgentProfileCreated,
			domain.AggregateAgentProfile, a.ID, a.Version, nil,
			map[string]any{"name": a.Name, "role": a.Role}); err != nil {
			return err
		}
		return s.activity(ctx, workspaceID, "agent.created", "添加智能体 "+a.Name)
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	return a, nil
}

// SetAgentAvailability 切换调度开关；disable 时按策略处置活动 Run（默认 interrupt）。
func (s *Service) SetAgentAvailability(ctx context.Context, agentID string, enabled bool) (*domain.AgentProfile, error) {
	var agent *domain.AgentProfile
	target := domain.AgentDisabled
	if enabled {
		target = domain.AgentEnabled
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		a, err := s.store.Agents().Get(ctx, agentID)
		if err != nil {
			return err
		}
		if a.Availability == target {
			agent = a
			return nil
		}
		expected := a.Version
		a.SetAvailability(target, time.Now().UTC())
		if err := s.store.Agents().Update(ctx, a, expected); err != nil {
			return err
		}
		if err := s.emit(ctx, a.WorkspaceID, domain.EventAgentAvailabilityChanged,
			domain.AggregateAgentProfile, a.ID, a.Version, nil,
			map[string]any{"availability": string(target)}); err != nil {
			return err
		}
		s.audit(ctx, a.WorkspaceID, "agent.availability_changed", a.ID,
			map[string]any{"availability": string(target)})
		agent = a
		// disable：活动 Run 默认 interrupt（M0 决策），通过引擎下发。
		if !enabled {
			runs, err := s.store.Runs().ActiveByAgent(ctx, agentID)
			if err != nil {
				return err
			}
			for _, r := range runs {
				// queued 未起跑：直接终态 interrupted；已起跑的进 interrupting 等引擎确认。
				target := domain.RunInterrupting
				if r.Status == domain.RunQueued {
					target = domain.RunInterrupted
				}
				if err := s.transitionRunLocked(ctx, r, target, nil); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if agent != nil {
		s.notifier.Notify(agent.WorkspaceID)
	}
	return agent, nil
}

func (s *Service) Agents(ctx context.Context, workspaceID string) ([]*domain.AgentProfile, error) {
	return s.store.Agents().List(ctx, workspaceID)
}

func (s *Service) Agent(ctx context.Context, id string) (*domain.AgentProfile, error) {
	return s.store.Agents().Get(ctx, id)
}

// UpdateWorkspace 更新名称与时区；Owner/Admin（RBAC 在 M4 全量化）。
func (s *Service) UpdateWorkspace(ctx context.Context, workspaceID string, name, timezone *string, expectedVersion int) (*domain.Workspace, error) {
	var updated *domain.Workspace
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		ws, err := s.store.Workspaces().Get(ctx, workspaceID)
		if err != nil {
			return err
		}
		if err := checkVersion(expectedVersion, ws.Version); err != nil {
			return err
		}
		expected := ws.Version
		if name != nil {
			ws.Name = *name
		}
		if timezone != nil {
			ws.Timezone = *timezone
		}
		if err := s.store.Workspaces().Update(ctx, ws, expected); err != nil {
			return err
		}
		ws.Version++
		updated = ws
		return s.activity(ctx, workspaceID, "workspace.updated", "更新 Workspace 配置")
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	return updated, nil
}

func checkVersion(expected, current int) error {
	if expected != 0 && expected != current {
		return domain.ErrVersionConflict
	}
	return nil
}

// AgentPatch 修改角色指令、技能、runtime preference、模型与权限策略（协议文档 §5.3）。
// 有文件关联（slug）的 agent 由 httpapi 层在成功后回写 agents/<slug>/。
type AgentPatch struct {
	Name              *string
	Role              *string
	Skills            []string
	Instructions      *string
	RuntimePreference *domain.RuntimePreference
	ModelOverride     *domain.ModelRef
	Policy            *domain.AgentPolicy
	ExpectedVersion   int
}

func (s *Service) UpdateAgent(ctx context.Context, agentID string, patch AgentPatch) (*domain.AgentProfile, error) {
	if patch.RuntimePreference != nil && patch.RuntimePreference.Mode != "" &&
		patch.RuntimePreference.Mode != "default" && patch.RuntimePreference.Mode != "plan" {
		return nil, fmt.Errorf("%w: mode 必须是 default|plan", domain.ErrValidation)
	}
	if patch.Policy != nil {
		switch patch.Policy.ApprovalPolicy {
		case "", "auto", "approve_high_risk", "manual":
		default:
			return nil, fmt.Errorf("%w: approval_policy 无效", domain.ErrValidation)
		}
		switch patch.Policy.Sandbox {
		case "", "read-only", "workspace-write", "danger-full-access":
		default:
			return nil, fmt.Errorf("%w: sandbox 无效", domain.ErrValidation)
		}
	}
	var updated *domain.AgentProfile
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		a, err := s.store.Agents().Get(ctx, agentID)
		if err != nil {
			return err
		}
		if err := checkVersion(patch.ExpectedVersion, a.Version); err != nil {
			return err
		}
		expected := a.Version
		if patch.Name != nil {
			a.Name = *patch.Name
		}
		if patch.Role != nil {
			a.Role = *patch.Role
		}
		if patch.Skills != nil {
			a.Skills = patch.Skills
		}
		if patch.Instructions != nil {
			a.Instructions = *patch.Instructions
		}
		if patch.RuntimePreference != nil {
			a.RuntimePreference = *patch.RuntimePreference
		}
		if patch.ModelOverride != nil {
			a.ModelOverride = *patch.ModelOverride
		}
		if patch.Policy != nil {
			a.Policy = *patch.Policy
		}
		a.UpdatedAt = time.Now().UTC()
		if err := s.store.Agents().Update(ctx, a, expected); err != nil {
			return err
		}
		a.Version++
		if err := s.emit(ctx, a.WorkspaceID, domain.EventAgentProfileUpdated,
			domain.AggregateAgentProfile, a.ID, a.Version, nil,
			map[string]any{"name": a.Name, "role": a.Role}); err != nil {
			return err
		}
		updated = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(updated.WorkspaceID)
	return updated, nil
}

// ── WorkItem ─────────────────────────────────────────────────────────

type CreateWorkItemParams struct {
	Title          string
	Description    string
	Status         domain.WorkItemStatus
	Priority       domain.Priority
	DueDate        *time.Time
	AgentProfileID string
}

func (s *Service) CreateWorkItem(ctx context.Context, workspaceID string, p CreateWorkItemParams) (*domain.WorkItem, error) {
	if p.Title == "" {
		return nil, fmt.Errorf("%w: title required", domain.ErrValidation)
	}
	status := p.Status
	if status == "" {
		status = domain.WorkItemTodo
	}
	priority := p.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}
	now := time.Now().UTC()
	wi := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: workspaceID,
		Title: p.Title, Description: p.Description, Status: status, Priority: priority,
		DueDate: p.DueDate, AgentProfileID: p.AgentProfileID, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.WorkItems().Create(ctx, wi); err != nil {
			return err
		}
		if err := s.emit(ctx, workspaceID, domain.EventWorkItemCreated,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil, workItemEventData(wi)); err != nil {
			return err
		}
		return s.activity(ctx, workspaceID, "work_item.created", "创建任务「"+wi.Title+"」")
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	return wi, nil
}

// MoveWorkItem 看板移动：状态机校验 + 乐观锁。
func (s *Service) MoveWorkItem(ctx context.Context, workItemID string, to domain.WorkItemStatus, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		from := w.Status
		if err := w.Transition(to, time.Now().UTC()); err != nil {
			return err
		}
		if to == domain.WorkItemBlocked {
			return fmt.Errorf("%w: block 必须走 commands/block", domain.ErrValidation)
		}
		// completed 仅经 commands/accept 验收门（含 phase 校验）；move 直达绕过验收。
		if to == domain.WorkItemCompleted {
			return fmt.Errorf("%w: completed 必须走 commands/accept 验收门", domain.ErrValidation)
		}
		// CheckVersion 通过后，以当前版本做事务内守卫。
		if err := s.store.WorkItems().Update(ctx, w, w.Version-1); err != nil {
			return err
		}
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemMoved,
			domain.AggregateWorkItem, w.ID, w.Version, nil,
			map[string]any{"from": string(from), "to": string(to), "status": string(to)}); err != nil {
			return err
		}
		wi = w
		return s.activity(ctx, w.WorkspaceID, "work_item.moved",
			fmt.Sprintf("任务「%s」移动到 %s", w.Title, to))
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	return wi, nil
}

func (s *Service) AssignWorkItem(ctx context.Context, workItemID, agentID string, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if err := s.assignLocked(ctx, w, agentID); err != nil {
			return err
		}
		wi = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	// M4：指派成功后按 agent 策略入队 assignment 唤醒（尽力而为，不影响指派结果）。
	if agentID != "" {
		s.enqueueAssignmentWake(context.WithoutCancel(ctx), wi, agentID)
	}
	return wi, nil
}

// assignLocked 事务内指派核心：写 assignee、乐观锁更新并发布 work_item.assigned
// 事件（AssignWorkItem 与 ClaimWorkItem 共用；agent 存在性在此校验）。
func (s *Service) assignLocked(ctx context.Context, w *domain.WorkItem, agentID string) error {
	if agentID != "" {
		if _, err := s.store.Agents().Get(ctx, agentID); err != nil {
			return err
		}
	}
	w.AgentProfileID = agentID
	// 与 Transition 路径同一约定：内存版本与 DB（version=version+1）保持同步。
	expected := w.Version
	w.Version++
	w.UpdatedAt = time.Now().UTC()
	if err := s.store.WorkItems().Update(ctx, w, expected); err != nil {
		return err
	}
	return s.emit(ctx, w.WorkspaceID, domain.EventWorkItemAssigned,
		domain.AggregateWorkItem, w.ID, w.Version, nil,
		map[string]any{"agent_profile_id": agentID})
}

type BlockParams struct {
	Code    string
	Message string
	Source  string
}

func (s *Service) BlockWorkItem(ctx context.Context, workItemID string, p BlockParams, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if err := s.blockLocked(ctx, w, p); err != nil {
			return err
		}
		wi = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	return wi, nil
}

// blockLocked 事务内落 blocker：状态迁移 + blocker 行 + 事件 + activity。
// BlockWorkItem（API 边界，带版本校验）与预算护栏收口（控制平面内部，无版本
// 期望——todo 主任务也可能落 blocker）共用。
func (s *Service) blockLocked(ctx context.Context, w *domain.WorkItem, p BlockParams) error {
	if err := w.Transition(domain.WorkItemBlocked, time.Now().UTC()); err != nil {
		return err
	}
	if err := s.store.WorkItems().Update(ctx, w, w.Version-1); err != nil {
		return err
	}
	b := &domain.Blocker{
		ID: domain.NewID("blk_"), WorkItemID: w.ID, Code: p.Code,
		Message: p.Message, Source: p.Source, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.WorkItems().CreateBlocker(ctx, b); err != nil {
		return err
	}
	if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemBlocked,
		domain.AggregateWorkItem, w.ID, w.Version, nil,
		map[string]any{"code": p.Code, "message": p.Message}); err != nil {
		return err
	}
	// blocker activity 归因到任务（M4：plan_parse_failed / verdict_parse_failed /
	// budget_exceeded 等控制平面 blocker 需能回溯）。
	return s.activityFor(ctx, w.WorkspaceID, w.ID, "work_item.blocked", "任务「"+w.Title+"」被阻塞")
}

// UnblockWorkItem 解除阻塞回到 in_progress；恢复执行由用户显式创建新 Run。
func (s *Service) UnblockWorkItem(ctx context.Context, workItemID string, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if err := w.Transition(domain.WorkItemInProgress, time.Now().UTC()); err != nil {
			return err
		}
		if err := s.store.WorkItems().Update(ctx, w, w.Version-1); err != nil {
			return err
		}
		if err := s.store.WorkItems().ResolveBlockers(ctx, w.ID, time.Now().UTC()); err != nil {
			return err
		}
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemUnblocked,
			domain.AggregateWorkItem, w.ID, w.Version, nil, nil); err != nil {
			return err
		}
		wi = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	return wi, nil
}

// AcceptWorkItem Reviewer / 人工验收：唯一进入 completed 的路径。
func (s *Service) AcceptWorkItem(ctx context.Context, workItemID string, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if err := w.Accept(time.Now().UTC()); err != nil {
			return err
		}
		// Accept 内部 Transition 已 bump 内存版本；CheckVersion(0) 放行时用 0 做
		// Update 守卫恒 0 行（version 从 1 起），故与 MoveWorkItem 同约定：
		// 以 DB 当前版本（bump 前）做事务内守卫。
		if err := s.store.WorkItems().Update(ctx, w, w.Version-1); err != nil {
			return err
		}
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemCompleted,
			domain.AggregateWorkItem, w.ID, w.Version, nil, nil); err != nil {
			return err
		}
		s.audit(ctx, w.WorkspaceID, "work_item.accepted", w.ID, map[string]any{"title": w.Title})
		wi = w
		return s.activity(ctx, w.WorkspaceID, "work_item.completed", "任务「"+w.Title+"」验收通过")
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	return wi, nil
}

func (s *Service) WorkItems(ctx context.Context, workspaceID string, f WorkItemFilter) ([]*domain.WorkItem, string, error) {
	return s.store.WorkItems().List(ctx, workspaceID, f)
}

func (s *Service) WorkItem(ctx context.Context, id string) (*domain.WorkItem, error) {
	return s.store.WorkItems().Get(ctx, id)
}

// WorkItemFieldPatch 普通字段修改；status 不允许任意 PATCH（走 commands）。
type WorkItemFieldPatch struct {
	Title           *string
	Description     *string
	Priority        *domain.Priority
	DueDate         *time.Time
	ExpectedVersion int
}

func (s *Service) UpdateWorkItemFields(ctx context.Context, workItemID string, patch WorkItemFieldPatch) (*domain.WorkItem, error) {
	var updated *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := checkVersion(patch.ExpectedVersion, w.Version); err != nil {
			return err
		}
		expected := w.Version
		if patch.Title != nil {
			w.Title = *patch.Title
		}
		if patch.Description != nil {
			w.Description = *patch.Description
		}
		if patch.Priority != nil {
			w.Priority = *patch.Priority
		}
		if patch.DueDate != nil {
			w.DueDate = patch.DueDate
		}
		if err := s.store.WorkItems().Update(ctx, w, expected); err != nil {
			return err
		}
		w.Version++
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemUpdated,
			domain.AggregateWorkItem, w.ID, w.Version, nil, workItemEventData(w)); err != nil {
			return err
		}
		updated = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(updated.WorkspaceID)
	return updated, nil
}

func workItemEventData(w *domain.WorkItem) map[string]any {
	return map[string]any{
		"title": w.Title, "status": string(w.Status), "priority": string(w.Priority),
	}
}
