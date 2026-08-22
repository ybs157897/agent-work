package application

import (
	"context"
	"fmt"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
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
	InputForwarder func(ctx context.Context, runID, instruction string)
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

func (s *Service) activity(ctx context.Context, workspaceID, kind, message string) error {
	if err := s.store.Events().AppendActivity(ctx, workspaceID, kind, message); err != nil {
		return err
	}
	return s.emit(ctx, workspaceID, domain.EventActivityCreated,
		domain.AggregateWorkspace, workspaceID, 0, nil, map[string]any{
			"kind": kind, "message": message,
		})
}

// audit 写不可变审计记录（协议文档 §10.1：审批、运行控制、凭据变更、验收）。
// M1 演示用户固定；RBAC 会话接入后替换 actor。
func (s *Service) audit(ctx context.Context, workspaceID, action, target string, detail map[string]any) {
	actor := map[string]any{"kind": "user", "id": "user_demo"}
	_ = s.store.Audit().Append(ctx, workspaceID, actor, action, target, detail)
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
	now := time.Now().UTC()
	a := &domain.AgentProfile{
		ID: domain.NewID(domain.PrefixAgent), WorkspaceID: workspaceID,
		Name: p.Name, Role: p.Role, Skills: p.Skills, Avatar: p.Avatar,
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: p.RuntimePreference, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
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
				if err := s.transitionRunLocked(ctx, r, domain.RunInterrupting, nil); err != nil {
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
		// CheckVersion 通过后，以当前版本做事务内守卫。
		if err := s.store.WorkItems().Update(ctx, w, w.Version-1); err != nil {
			return err
		}
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemMoved,
			domain.AggregateWorkItem, w.ID, w.Version, nil,
			map[string]any{"from": string(from), "to": string(to), "status": string(to)}); err != nil {
			return err
		}
		if to == domain.WorkItemCompleted {
			if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemCompleted,
				domain.AggregateWorkItem, w.ID, w.Version, nil, nil); err != nil {
				return err
			}
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
		if agentID != "" {
			if _, err := s.store.Agents().Get(ctx, agentID); err != nil {
				return err
			}
		}
		w.AgentProfileID = agentID
		if err := s.store.WorkItems().Update(ctx, w, w.Version-1); err != nil {
			return err
		}
		if err := s.emit(ctx, w.WorkspaceID, domain.EventWorkItemAssigned,
			domain.AggregateWorkItem, w.ID, w.Version, nil,
			map[string]any{"agent_profile_id": agentID}); err != nil {
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
		wi = w
		return s.activity(ctx, w.WorkspaceID, "work_item.blocked", "任务「"+w.Title+"」被阻塞")
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	return wi, nil
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
		if err := s.store.WorkItems().Update(ctx, w, expectedVersion); err != nil {
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
