package application

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledge"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

// Service 编排用例与事务边界。命令流程：
// 校验 → 领域状态机 → 同事务写状态 + 事件 + outbox → 提交后通知 SSE / 分派 Runtime。
type Service struct {
	changesMu       sync.Mutex
	revertedChanges map[string]string
	store           Store
	dispatcher      Dispatcher
	notifier        Notifier
	adapters        *runtime.Registry
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
	return &Service{store: store, dispatcher: dispatcher, notifier: notifier, adapters: adapters, revertedChanges: make(map[string]string)}
}

// SetDispatcher 用于打破 Service ↔ Gateway/Adapter 的构造环（启动时一次性注入）。
func (s *Service) SetDispatcher(d Dispatcher) { s.dispatcher = d }

// emit 在事务内追加 Canonical Event（stream_events + outbox 同事务）。
func (s *Service) emit(ctx context.Context, workspaceID, evType, aggType, aggID string, aggVersion int, runEvent *RunEventRecord, data map[string]any) error {
	if runEvent != nil {
		if runEvent.AgentID == "" {
			if id, ok := data["agent_id"].(string); ok && id != "" {
				runEvent.AgentID = id
			} else {
				runEvent.AgentID = "main"
			}
		}
		data = withoutAgentID(data)
		runEvent.Payload = data
	}
	ev, err := domain.NewCanonicalEvent(workspaceID, evType, aggType, aggID, aggVersion, data)
	if err != nil {
		return err
	}
	if runEvent != nil {
		ev.AgentID = runEvent.AgentID
	}
	if _, err := s.store.Events().Append(ctx, ev, runEvent); err != nil {
		return err
	}
	return nil
}

func withoutAgentID(data map[string]any) map[string]any {
	if len(data) == 0 {
		return data
	}
	out := maps.Clone(data)
	delete(out, "agent_id")
	return out
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
			if wi, err := s.store.WorkItems().Get(ctx, workItemID); err == nil {
				data["record_kind"] = string(workItemRecordKind(wi))
			}
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

// defaultWorkItemRecordKind preserves the historical application-level meaning
// of a WorkItem created without an explicit kind: direct/internal callers have
// always created task-board work. Public Chat callers must pass chat explicitly.
func defaultWorkItemRecordKind(kind domain.WorkItemRecordKind) (domain.WorkItemRecordKind, error) {
	if kind == "" {
		return domain.RecordKindTask, nil
	}
	if !kind.Valid() {
		return "", fmt.Errorf("%w: record_kind 必须为 chat 或 task", domain.ErrValidation)
	}
	return kind, nil
}

// workItemRecordKind treats an empty pre-record_kind in-memory object as the
// historical task kind. Persisted rows are always assigned an explicit
// chat/task value by migration; this normalization only protects direct
// test/in-process callers that construct a legacy zero-value object.
func workItemRecordKind(w *domain.WorkItem) domain.WorkItemRecordKind {
	if w == nil || w.RecordKind == "" {
		return domain.RecordKindTask
	}
	return w.RecordKind
}

func requireValidWorkItemRecordKind(w *domain.WorkItem) error {
	if w == nil {
		return fmt.Errorf("%w: work item required", domain.ErrValidation)
	}
	if !workItemRecordKind(w).Valid() {
		return fmt.Errorf("%w: work item %s 的 record_kind 无效", domain.ErrValidation, w.ID)
	}
	return nil
}

func requireTaskWorkItem(w *domain.WorkItem) error {
	if err := requireValidWorkItemRecordKind(w); err != nil {
		return err
	}
	if workItemRecordKind(w) != domain.RecordKindTask {
		return fmt.Errorf("%w: record_kind=chat 不支持任务操作", domain.ErrValidation)
	}
	return nil
}

func isTaskWorkItem(w *domain.WorkItem) bool {
	return w != nil && workItemRecordKind(w) == domain.RecordKindTask
}

func workItemNoun(w *domain.WorkItem) string {
	if w != nil && workItemRecordKind(w) == domain.RecordKindChat {
		return "对话"
	}
	return "任务"
}

type CreateWorkItemParams struct {
	Title          string
	Description    string
	Status         domain.WorkItemStatus
	Priority       domain.Priority
	DueDate        *time.Time
	AgentProfileID string
	// ParentID 非空表示作为子任务/分叉会话创建；父任务必须存在且同 workspace。
	ParentID string
	// ClientKey 非空时启用实体级幂等：同 workspace 下同 key 重复创建返回既有实体
	// （防队列 drain 重试/分叉双击这类业务意图重复建卡；命令级 Idempotency-Key 防的是请求重放）。
	ClientKey string
	// RecordKind 是不可变的记录边界。空值保留内部/历史调用的 task 语义；
	// Chat 创建入口必须显式传 RecordKindChat。
	RecordKind domain.WorkItemRecordKind
	// AutoCoordinate marks a public root Task for immediate system coordination.
	// Internal seeds, plan children, and generic test/setup calls leave this
	// false so creating a row never unexpectedly starts an execution.
	AutoCoordinate bool
	// AcceptanceCriteria is copied into the root coordinator state so the
	// planner receives the user's acceptance contract after a restart.
	// RFC §4.10：同一份 criteria 同时持久化到 work_items 行（验收读模型权威）；
	// 首轮 Run 后不允许原地修改，新增要求走 requirement comment。
	AcceptanceCriteria []string
}

// normalizeAcceptanceCriteria 验收条目归一：逐条 trim、丢弃空串；保留原话不改写
// （领域字段注释：元素为验收条目原话）。
func normalizeAcceptanceCriteria(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		if t := strings.TrimSpace(c); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Service) CreateWorkItem(ctx context.Context, workspaceID string, p CreateWorkItemParams) (*domain.WorkItem, error) {
	if p.Title == "" {
		return nil, fmt.Errorf("%w: title required", domain.ErrValidation)
	}
	recordKind, err := defaultWorkItemRecordKind(p.RecordKind)
	if err != nil {
		return nil, err
	}
	status := p.Status
	if status == "" {
		status = domain.WorkItemTodo
	}
	if p.AutoCoordinate && recordKind == domain.RecordKindTask && p.ParentID == "" &&
		status != domain.WorkItemTodo {
		return nil, fmt.Errorf("%w: 根 Task 创建时 status 必须为 todo，由系统 Coordinator 推进任务状态", domain.ErrValidation)
	}
	priority := p.Priority
	if priority == "" {
		priority = domain.PriorityMedium
	}
	if p.ParentID != "" {
		parent, err := s.store.WorkItems().Get(ctx, p.ParentID)
		if err != nil {
			return nil, fmt.Errorf("%w: parent work item 不存在", domain.ErrValidation)
		}
		if parent.WorkspaceID != workspaceID {
			return nil, fmt.Errorf("%w: parent work item 不属于当前 workspace", domain.ErrValidation)
		}
		if err := requireValidWorkItemRecordKind(parent); err != nil {
			return nil, err
		}
		if workItemRecordKind(parent) != recordKind {
			return nil, fmt.Errorf("%w: parent work item 的 record_kind=%s，与子项=%s 不一致",
				domain.ErrValidation, workItemRecordKind(parent), recordKind)
		}
		if recordKind == domain.RecordKindTask && parent.Status.IsTerminal() {
			return nil, fmt.Errorf("%w: terminal Task 不能创建子任务，请先重新打开根任务", domain.ErrValidation)
		}
		if recordKind == domain.RecordKindTask {
			if state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, parent.ID); stateErr == nil {
				if state != nil && (state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled) {
					return nil, fmt.Errorf("%w: Coordinator 已完成的 Task 不能创建子任务", domain.ErrValidation)
				}
			} else if !errors.Is(stateErr, domain.ErrNotFound) {
				return nil, stateErr
			}
		}
	}
	now := time.Now().UTC()
	wi := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: workspaceID,
		RecordKind: recordKind,
		Title:      p.Title, Description: p.Description, Status: status, Priority: priority,
		DueDate: p.DueDate, AgentProfileID: p.AgentProfileID, ParentID: p.ParentID,
		ClientKey: p.ClientKey, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	// RFC §4.10：根/子 Task 创建即持久化 canonical 验收标准（不再只放
	// Coordinator state/Run input）；Chat 记录没有验收读模型，criteria 只属于 Task。
	if recordKind == domain.RecordKindTask {
		wi.AcceptanceCriteria = normalizeAcceptanceCriteria(p.AcceptanceCriteria)
	}
	autoCoordinate := p.AutoCoordinate && recordKind == domain.RecordKindTask && p.ParentID == ""
	var coordinatorRootID string
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		var config *domain.TaskCoordinatorConfig
		if autoCoordinate {
			var err error
			config, err = s.store.TaskCoordinators().EnsureConfig(ctx, workspaceID)
			if err != nil {
				return err
			}
			// Root Task ownership is the protected system identity. A caller cannot
			// smuggle a manually selected worker into the root assignee field.
			wi.AgentProfileID = config.AgentProfileID
		}
		if err := s.store.WorkItems().Create(ctx, wi); err != nil {
			return err
		}
		if err := s.emit(ctx, workspaceID, domain.EventWorkItemCreated,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil, workItemEventData(wi)); err != nil {
			return err
		}
		if autoCoordinate {
			state := &domain.TaskCoordinatorState{
				ID:                 domain.NewID(domain.PrefixCoordinatorState),
				WorkspaceID:        workspaceID,
				RootWorkItemID:     wi.ID,
				CoordinatorAgentID: config.AgentProfileID,
				Status:             domain.CoordinatorQueued,
				CurrentAction:      "queued",
				Data:               map[string]any{"acceptance_criteria": append([]string(nil), p.AcceptanceCriteria...)},
				Version:            1,
				CreatedAt:          now,
				UpdatedAt:          now,
			}
			if err := s.store.TaskCoordinators().CreateState(ctx, state); err != nil {
				return err
			}
			// RFC §6.2：comment cursor 行与 Coordinator state 同事务创建，永不物理删除。
			if err := s.store.TaskComments().EnsureCursor(ctx, wi.ID); err != nil {
				return err
			}
			if err := s.appendCoordinatorEvent(ctx, state, wi.ID, domain.EventCoordinatorQueued,
				"任务已进入 Coordinator 队列", "", config.AgentProfileID, 0, "", nil, nil); err != nil {
				return err
			}
			coordinatorRootID = wi.ID
		} else if recordKind == domain.RecordKindTask && p.ParentID != "" {
			// A user-added child belongs to the existing root control line. Plan
			// children bypass this Service and therefore do not cause a second wake.
			// RFC §7.7 删除清单：同一事务追加 actor=user 的 requirement comment
			//（取代 pending_instruction 单槽），根 Coordinator durable queued。
			if state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, p.ParentID); stateErr == nil {
				root, err := s.store.WorkItems().Get(ctx, state.RootWorkItemID)
				if err != nil {
					return err
				}
				body := "用户新增子任务：" + wi.Title + "。请把它纳入根任务计划并继续推进。"
				comment := &domain.TaskComment{
					ID:             domain.NewID(domain.PrefixTaskComment),
					WorkspaceID:    workspaceID,
					RootWorkItemID: state.RootWorkItemID,
					WorkItemID:     wi.ID,
					Kind:           domain.CommentRequirement,
					Body:           body,
					ActorKind:      domain.CommentActorUser,
					ActorID:        commentActorUserID,
					SourceRef:      "work_item.child_added",
					CreatedAt:      now,
				}
				if _, err := s.store.TaskComments().Append(ctx, comment); err != nil {
					return err
				}
				if _, err := s.applyRequirementWakeLocked(ctx, root, "用户新增子任务", body); err != nil {
					return err
				}
				freshState, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
				if err != nil {
					return err
				}
				if err := s.emitTaskCommentCreated(ctx, comment, freshState); err != nil {
					return err
				}
				coordinatorRootID = state.RootWorkItemID
			} else if !errors.Is(stateErr, domain.ErrNotFound) {
				return stateErr
			}
		}
		return s.activityFor(ctx, workspaceID, wi.ID, "work_item.created", "创建"+workItemNoun(wi)+"「"+wi.Title+"」")
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	if coordinatorRootID != "" {
		// The row/state/event transaction is the durable hand-off. Starting after
		// commit prevents a runtime side effect from observing a rolled-back task;
		// a failure is persisted as blocked/retryable state by the engine.
		// §7.10：StartCoordinator 是 best-effort，失败只记日志，durable queued
		// 由恢复循环兜底。
		if startErr := s.StartCoordinator(context.WithoutCancel(ctx), coordinatorRootID); startErr != nil {
			log.Printf("coordinator: task %s 自动接取失败: %v", coordinatorRootID, startErr)
		}
		if refreshed, refreshErr := s.store.WorkItems().Get(ctx, wi.ID); refreshErr == nil {
			wi = refreshed
		}
	}
	return wi, nil
}

// CreateWorkItemIdempotent 实体级幂等创建：client_key 撞唯一索引时查回既有实体，
// replayed=true 返回（不产生重复事件/activity——冲突发生在事务内，整事务回滚）。
// 无 client_key 时与 CreateWorkItem 完全等价。
func (s *Service) CreateWorkItemIdempotent(ctx context.Context, workspaceID string, p CreateWorkItemParams) (wi *domain.WorkItem, replayed bool, err error) {
	wi, err = s.CreateWorkItem(ctx, workspaceID, p)
	if err == nil {
		return wi, false, nil
	}
	if p.ClientKey == "" || !errors.Is(err, domain.ErrIdempotencyConflict) {
		return nil, false, err
	}
	existing, gerr := s.store.WorkItems().GetByClientKey(ctx, workspaceID, p.ClientKey)
	if gerr != nil {
		return nil, false, err // 查回失败时报告原始冲突错误
	}
	requestedKind, kerr := defaultWorkItemRecordKind(p.RecordKind)
	if kerr != nil {
		return nil, false, kerr
	}
	if workItemRecordKind(existing) != requestedKind {
		return nil, false, fmt.Errorf("%w: client_key 已属于 record_kind=%s，不能重放为 %s",
			domain.ErrIdempotencyConflict, workItemRecordKind(existing), requestedKind)
	}
	if p.AutoCoordinate && requestedKind == domain.RecordKindTask && existing.ParentID == "" {
		// A network retry after the create transaction but before the immediate
		// start call actively kicks the durable queued state instead of waiting
		// for the periodic recovery scan.
		_ = s.StartCoordinator(context.WithoutCancel(ctx), existing.ID)
	}
	return existing, true, nil
}

// MoveWorkItem 看板移动：状态机校验 + 乐观锁。
func (s *Service) MoveWorkItem(ctx context.Context, workItemID string, to domain.WorkItemStatus, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		if _, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, w.ID); err == nil {
			return fmt.Errorf("%w: coordinated Task 的状态由系统 Coordinator 管理", domain.ErrValidation)
		} else if !errors.Is(err, domain.ErrNotFound) {
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
			map[string]any{"from": string(from), "to": string(to), "status": string(to),
				"record_kind": string(workItemRecordKind(w))}); err != nil {
			return err
		}
		wi = w
		return s.activityFor(ctx, w.WorkspaceID, w.ID, "work_item.moved",
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
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		if _, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, w.ID); err == nil {
			return fmt.Errorf("%w: coordinated Task 不接受手工指派", domain.ErrValidation)
		} else if !errors.Is(err, domain.ErrNotFound) {
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
		map[string]any{"agent_profile_id": agentID, "record_kind": string(workItemRecordKind(w))})
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
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		if err := s.blockLocked(ctx, w, p); err != nil {
			return err
		}
		if err := s.closeOpenDispatchesForBlockLocked(ctx, w); err != nil {
			return err
		}
		wi = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	s.markCoordinatorUserBlocked(context.WithoutCancel(ctx), wi.ID, p)
	return wi, nil
}

func (s *Service) closeOpenDispatchesForBlockLocked(ctx context.Context, workItem *domain.WorkItem) error {
	dispatches, err := s.store.Dispatches().ListByWorkItem(ctx, workItem.ID)
	if err != nil {
		return err
	}
	for _, dispatch := range dispatches {
		if dispatch == nil || dispatch.Status.IsTerminal() {
			continue
		}
		closedAt := time.Now().UTC()
		closed, err := s.store.Dispatches().CloseStatus(ctx, dispatch.ID, domain.DispatchDegraded, closedAt)
		if err != nil {
			return err
		}
		if !closed {
			continue
		}
		dispatch.Status, dispatch.ClosedAt = domain.DispatchDegraded, &closedAt
		if err := s.emitDispatchUpdated(ctx, workItem.WorkspaceID, dispatch); err != nil {
			return err
		}
	}
	return nil
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
		map[string]any{"code": p.Code, "message": p.Message,
			"record_kind": string(workItemRecordKind(w))}); err != nil {
		return err
	}
	// blocker activity 归因到任务（M4：plan_parse_failed / verdict_parse_failed /
	// budget_exceeded 等控制平面 blocker 需能回溯）。
	return s.activityFor(ctx, w.WorkspaceID, w.ID, "work_item.blocked", "任务「"+w.Title+"」被阻塞")
}

// UnblockWorkItem 解除阻塞回到 in_progress；存在系统 Coordinator 时在同一事务
// 内追加 actor=system、source_ref=work_item.unblocked 的 requirement comment
// （RFC §7.7 删除清单）并把 Coordinator durable queued，commit 后 StartCoordinator
// best-effort，用户不再手工选择 Agent 或创建 Run。
func (s *Service) UnblockWorkItem(ctx context.Context, workItemID string, expectedVersion int) (*domain.WorkItem, error) {
	var wi *domain.WorkItem
	queued := false
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		w, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		if err := w.CheckVersion(expectedVersion); err != nil {
			return err
		}
		var coordinatorState *domain.TaskCoordinatorState
		if st, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, w.ID); stateErr == nil {
			coordinatorState = st
		} else if !errors.Is(stateErr, domain.ErrNotFound) {
			return stateErr
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
			domain.AggregateWorkItem, w.ID, w.Version, nil,
			map[string]any{"record_kind": string(workItemRecordKind(w))}); err != nil {
			return err
		}
		if coordinatorState != nil && coordinatorState.Status != domain.CoordinatorCompleted &&
			coordinatorState.Status != domain.CoordinatorCancelled {
			// RFC §7.7 删除清单：Unblock 同事务追加 system requirement comment 并
			// durable queued；blocked → queued 是用户显式动作，不受 §5.2.6 约束。
			body := "用户解除阻塞，任务「" + w.Title + "」继续推进。"
			comment := &domain.TaskComment{
				ID:             domain.NewID(domain.PrefixTaskComment),
				WorkspaceID:    w.WorkspaceID,
				RootWorkItemID: coordinatorState.RootWorkItemID,
				WorkItemID:     w.ID,
				Kind:           domain.CommentRequirement,
				Body:           body,
				ActorKind:      domain.CommentActorSystem,
				ActorID:        "control_plane",
				SourceRef:      "work_item.unblocked",
				CreatedAt:      time.Now().UTC(),
			}
			if _, err := s.store.TaskComments().Append(ctx, comment); err != nil {
				return err
			}
			fresh, err := s.store.TaskCoordinators().GetState(ctx, coordinatorState.RootWorkItemID)
			if err != nil {
				return err
			}
			if fresh.Status == domain.CoordinatorBlocked {
				expected := fresh.Version
				fresh.Status = domain.CoordinatorQueued
				fresh.Phase = "recovering"
				fresh.CurrentAction = "用户解除阻塞后继续"
				fresh.CurrentRunID = ""
				fresh.BlockerCode, fresh.BlockerMessage = "", ""
				fresh.NextActionAt = nil
				if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
					return err
				}
				fresh.Version = expected + 1
				if err := s.appendCoordinatorEvent(ctx, fresh, w.ID, domain.EventCoordinatorRecoveryStarted,
					"用户解除阻塞后恢复 Coordinator", "", fresh.CoordinatorAgentID, fresh.Attempt,
					body, nil, map[string]any{"stage": "retry", "next_action": "重新规划并继续执行"}); err != nil {
					return err
				}
				queued = true
			}
			if err := s.emitTaskCommentCreated(ctx, comment, fresh); err != nil {
				return err
			}
		}
		wi = w
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(wi.WorkspaceID)
	if queued {
		// best-effort（§7.10）；durable queued 由恢复循环兜底。
		if err := s.StartCoordinator(context.WithoutCancel(ctx), wi.ID); err != nil {
			log.Printf("unblock: task %s StartCoordinator 失败（durable queued 由恢复循环兜底）: %v", wi.ID, err)
		}
	}
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
		if err := requireTaskWorkItem(w); err != nil {
			return err
		}
		var coordinatorState *domain.TaskCoordinatorState
		if state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, w.ID); stateErr == nil {
			if state == nil {
				return fmt.Errorf("%w: coordinated Task state required", domain.ErrValidation)
			}
			if state.RootWorkItemID != w.ID {
				return fmt.Errorf("%w: coordinated child Task 不能单独验收，请验收根 Task", domain.ErrValidation)
			}
			if state.Status != domain.CoordinatorWaitingUser {
				// Accept/Return/feedback 竞态门（§7.10）：actionable comment 先提交
				// 会把 waiting_user 原子改成 queued，Accept 在此失败且不覆盖。
				return fmt.Errorf("%w: Coordinator 尚未进入待用户验收状态", ErrReviewStateConflict)
			}
			coordinatorState = state
		} else if !errors.Is(stateErr, domain.ErrNotFound) {
			return stateErr
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
			domain.AggregateWorkItem, w.ID, w.Version, nil,
			map[string]any{"record_kind": string(workItemRecordKind(w))}); err != nil {
			return err
		}
		s.audit(ctx, w.WorkspaceID, "work_item.accepted", w.ID, map[string]any{"title": w.Title})
		if coordinatorState != nil {
			expected := coordinatorState.Version
			coordinatorState.Status = domain.CoordinatorCompleted
			coordinatorState.Phase = "acceptance"
			coordinatorState.Summary = "任务已由用户验收"
			coordinatorState.CurrentAction = "任务已由用户验收"
			coordinatorState.CurrentRunID = ""
			coordinatorState.NextActionAt = nil
			coordinatorState.BlockerCode, coordinatorState.BlockerMessage = "", ""
			coordinatorState.LastError = ""
			if err := s.store.TaskCoordinators().UpdateState(ctx, coordinatorState, expected); err != nil {
				return err
			}
			coordinatorState.Version = expected + 1
			if err := s.appendCoordinatorEvent(ctx, coordinatorState, w.ID, domain.EventCoordinatorCompleted,
				"用户已验收任务", "", coordinatorState.CoordinatorAgentID, coordinatorState.Attempt, "", nil,
				map[string]any{"stage": "acceptance", "status": "completed"}); err != nil {
				return err
			}
		}
		wi = w
		return s.activityFor(ctx, w.WorkspaceID, w.ID, "work_item.completed", "任务「"+w.Title+"」验收通过")
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
// AcceptanceCriteria 非 nil 表示请求原地改写验收标准：首轮 Run 之前允许（任务
// 尚未开工），之后一律拒绝（RFC §4.10——新增要求走 requirement comment，保持
// 历史可审计；HTTP PATCH 目前不暴露该字段，本守卫是命令层兜底）。
type WorkItemFieldPatch struct {
	Title              *string
	Description        *string
	Priority           *domain.Priority
	DueDate            *time.Time
	AcceptanceCriteria *[]string
	ExpectedVersion    int
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
		if patch.AcceptanceCriteria != nil {
			if err := s.checkAcceptanceCriteriaEditable(ctx, w); err != nil {
				return err
			}
			w.AcceptanceCriteria = normalizeAcceptanceCriteria(*patch.AcceptanceCriteria)
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
		"record_kind": string(workItemRecordKind(w)),
	}
}

// checkAcceptanceCriteriaEditable 验收标准原地改写门（RFC §4.10）：只有 Task 且
// 首轮 Run 之前可改；已有任意 Run（含首轮）的 Task 拒绝——新增要求必须走
// requirement comment 进入 Coordinator durable 控制线。
func (s *Service) checkAcceptanceCriteriaEditable(ctx context.Context, w *domain.WorkItem) error {
	if !isTaskWorkItem(w) {
		return fmt.Errorf("%w: Chat 记录没有验收标准", domain.ErrValidation)
	}
	_, runs, err := s.store.WorkItems().LatestRunID(ctx, w.ID)
	if err != nil {
		return err
	}
	if runs > 0 {
		return fmt.Errorf("%w: 首轮 Run 后不允许原地修改验收标准，请通过 requirement 评论追加", domain.ErrValidation)
	}
	return nil
}

// withWorkItemRecordKind annotates run/aggregate event payloads with the
// durable Chat/Task boundary. Event consumers can fail closed without first
// guessing which projection owns a run.
func withWorkItemRecordKind(data map[string]any, w *domain.WorkItem) map[string]any {
	out := make(map[string]any, len(data)+1)
	for k, v := range data {
		out[k] = v
	}
	out["record_kind"] = string(workItemRecordKind(w))
	return out
}
