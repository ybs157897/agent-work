package application

import (
	"context"
	"fmt"
	"time"

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
}

// CreateRun：权威事务写入 queued Run 后才分派，避免幽灵任务（架构文档 §5）。
func (s *Service) CreateRun(ctx context.Context, workItemID string, p CreateRunParams) (*domain.ExecutionRun, error) {
	if p.Instruction == "" {
		return nil, fmt.Errorf("%w: instruction required", domain.ErrValidation)
	}
	var run *domain.ExecutionRun
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		wi, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := wi.CheckVersion(p.ExpectedWorkItemVer); err != nil {
			return err
		}
		if wi.Status.IsTerminal() {
			return fmt.Errorf("%w: work item is terminal", domain.ErrValidation)
		}
		var agent *domain.AgentProfile
		if p.AgentProfileID != "" {
			a, err := s.store.Agents().Get(ctx, p.AgentProfileID)
			if err != nil {
				return err
			}
			agent = a
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
		policy := orchestrator.PolicyFor(agent)
		provider, model := orchestrator.EffectiveModel(agent, binding)
		r := &domain.ExecutionRun{
			ID: runID, WorkspaceID: wi.WorkspaceID,
			WorkItemID: wi.ID, AgentProfileID: p.AgentProfileID, Status: domain.RunQueued,
			RuntimeLabel: label, CapabilitySnapshotID: capsID,
			Input: orchestrator.BuildInput(p.Instruction, p.AcceptanceCriteria, p.Requirements,
				p.RuntimePreference, agent, label, reason),
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		// 编排产物补充进快照：有效模型与裁剪后权限策略（adapter 从 run.Input 读取）。
		r.Input["model"] = map[string]string{"provider": provider, "model": model}
		r.Input["policy"] = map[string]any{
			"tools": policy.Tools, "approval_policy": policy.ApprovalPolicy, "sandbox": policy.Sandbox,
		}
		if err := s.store.Runs().Create(ctx, r); err != nil {
			return err
		}
		if caps != nil {
			if err := s.store.Caps().Create(ctx, caps); err != nil {
				return err
			}
		}
		// 首版串行策略：创建 Run 时把 todo 推进到 in_progress。
		if wi.Status == domain.WorkItemTodo {
			if err := wi.Transition(domain.WorkItemInProgress, now); err != nil {
				return err
			}
			if err := s.store.WorkItems().Update(ctx, wi, wi.Version-1); err != nil {
				return err
			}
			if err := s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemMoved,
				domain.AggregateWorkItem, wi.ID, wi.Version, nil,
				map[string]any{"from": "todo", "to": "in_progress"}); err != nil {
				return err
			}
		}
		if err := s.emit(ctx, wi.WorkspaceID, domain.EventRunCreated,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunCreated, Payload: map[string]any{
				"instruction": p.Instruction,
			}},
			map[string]any{"work_item_id": wi.ID, "status": string(r.Status), "instruction": p.Instruction}); err != nil {
			return err
		}
		run = r
		return s.activity(ctx, wi.WorkspaceID, "run.created",
			fmt.Sprintf("任务「%s」创建运行 %s", wi.Title, r.ID))
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(run.WorkspaceID)
	// 权威写入成功后才允许启动 Runtime 副作用。
	if s.dispatcher != nil {
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), run); err != nil {
			return nil, err
		}
	}
	return run, nil
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
		r := &domain.ExecutionRun{
			ID: domain.NewID(domain.PrefixRun), WorkspaceID: parent.WorkspaceID,
			WorkItemID: parent.WorkItemID, AgentProfileID: parent.AgentProfileID,
			Status: domain.RunQueued, RuntimeLabel: parent.RuntimeLabel,
			RetryOf: parent.ID, Input: parent.Input,
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
			return nil, err
		}
	}
	return retry, nil
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
		// queued 状态可直接终态，无需 Adapter 确认。
		if r.Status == domain.RunQueued {
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
	// 非 queued 的运行需要 Runner/Adapter 确认终态（协议文档 §8.3 取消语义）。
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
	// presence 投影：运行中 busy，终态回 idle。
	if r.AgentProfileID != "" {
		switch {
		case to == domain.RunRunning:
			_ = s.store.Agents().SetPresence(ctx, r.AgentProfileID, domain.PresenceBusy)
		case to.IsTerminal():
			_ = s.store.Agents().SetPresence(ctx, r.AgentProfileID, domain.PresenceIdle)
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
	return nil
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
		return s.emit(ctx, r.WorkspaceID, domain.EventRunProgressUpdated,
			domain.AggregateExecutionRun, r.ID, r.Version,
			&RunEventRecord{RunID: r.ID, EventType: domain.EventRunProgressUpdated},
			map[string]any{"progress": progress})
	})
	if err != nil {
		return err
	}
	s.notifier.Notify(workspaceID)
	return nil
}

// RecordRunEvent 追加 Run 域事件（message/tool 流），只写 run_events + stream。
func (s *Service) RecordRunEvent(ctx context.Context, runID, evType string, data map[string]any) error {
	var workspaceID string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
		if err != nil {
			return err
		}
		workspaceID = r.WorkspaceID
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

// RequestApproval：Run 进入 waiting_approval，等待幂等决定。
func (s *Service) RequestApproval(ctx context.Context, runID, kind, risk, summary string) (*domain.ApprovalRequest, error) {
	var approval *domain.ApprovalRequest
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r, err := s.store.Runs().Get(ctx, runID)
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
	return approval, nil
}

// ResolveApproval 幂等决定；通过后 Run 回到 running（Adapter 继续执行）。
func (s *Service) ResolveApproval(ctx context.Context, approvalID string, approved bool, by, reason string) (*domain.ApprovalRequest, error) {
	decision := domain.ApprovalRejected
	if approved {
		decision = domain.ApprovalApproved
	}
	var result *domain.ApprovalRequest
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		a, err := s.store.Runs().GetApproval(ctx, approvalID)
		if err != nil {
			return err
		}
		if a.Status == decision {
			result = a
			return nil // 幂等：重复相同决定直接返回
		}
		if err := a.Resolve(decision, by, reason, time.Now().UTC()); err != nil {
			return err
		}
		if err := s.store.Runs().UpdateApproval(ctx, a); err != nil {
			return err
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
		// 审批决定必须记录 approver、理由与范围（协议文档 §10.3）。
		s.audit(ctx, r.WorkspaceID, "approval.resolved", a.ID,
			map[string]any{"decision": string(decision), "approver": by, "reason": reason, "risk": a.Risk})
		result = a
		return s.activity(ctx, r.WorkspaceID, "approval.resolved",
			fmt.Sprintf("审批 %s：%s", a.ID, decision))
	})
	if err != nil {
		return nil, err
	}
	if result != nil {
		r, err := s.store.Runs().Get(ctx, result.RunID)
		if err == nil {
			s.notifier.Notify(r.WorkspaceID)
		}
		// 转发到 Runner/Adapter，使其继续或终止执行。
		if s.ApprovalForwarder != nil && result.Status == decision {
			s.ApprovalForwarder(ctx, result.RunID, result.ID, approved)
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
	if s.InputForwarder != nil {
		s.InputForwarder(ctx, runID, instruction)
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
