// plan.go M1/M2/M4 编排计划：SubmitPlan 确定性执行器 + 子任务静默唤醒钩子（设计 note
// notes/implemented/orchestration/2026-08-23-m1-plan-executor.md、
// 2026-08-23-m2-lead-planner-evaluation.md、2026-08-24-m4-claim-join-guardrails.md）。
// 词汇表：dispatch（建子任务+首 run，manual 审批策略挂闸）/ defer（批次终止挂起）/
// join（带显式等待集的 defer 变体，M4）/ finish（收尾，evaluation=true 触发评估 run）/
// consult_knowledge（预取检索注入）。执行器同一提交事务内顺序推进，副作用
// （Dispatch/Notify/wakeup 入队）在提交后执行。
package application

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/knowledge"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// PlanStepInput 提交载荷的单个动作：payload 为动词专属字段的原文（不含 verb 键）。
type PlanStepInput struct {
	Verb    string
	Payload map[string]any
}

// SubmitPlanParams 创建 plan 的参数（API 契约见 openapi PlanSubmit）。
type SubmitPlanParams struct {
	WorkItemID     string
	AgentProfileID string
	SourceRunID    string
	Steps          []PlanStepInput
	// Guardrails M4 预算护栏（提交时固化进 plan；max_dispatch 提交期整单校验，
	// max_tokens 在子任务静默唤醒点核算）。零值表示未设限。
	Guardrails domain.PlanGuardrails
}

// planTask 逐动词解析后的步骤规约（校验在提交入口完成，执行期不再有引用错误）。
type planTask struct {
	verb    domain.PlanVerb
	payload map[string]any
	// dispatch 专属字段。
	agentID     string
	title       string
	instruction string
	acceptance  []string
	priority    domain.Priority
	// approvalGate M4 审批护栏：dispatch 目标 agent approval_policy=manual。
	// annotateDispatchSteps 在提交与审批续跑两处标注（planTask 不持久化，
	// 续跑从持久化 payload 重解析后重标注）；已批准步骤经 skipGate 免二次挂起。
	approvalGate bool
	// dispatch 引用的 consult_knowledge 步骤 seq（knowledge_from）；-1 未引用。
	knowledgeFrom int
	// consult_knowledge 专属字段（检索参数）。
	corpus string
	terms  []string
	limit  int
	// defer/join 共享字段；wakeAt nil 表示未指定 wake_at。
	wakeAt *time.Time
	// joinChildren join 的显式等待集（children 数组）；nil = 全部子任务
	//（defer 与 join{children:"all"} 语义，执行器内部统一）。
	joinChildren []string
	// finish 专属字段：evaluation=true 时 plan 落 finished 后自动创建评估 run。
	evaluation bool
}

// SubmitPlan 校验并同步执行一份 plan。同一 work item 同时最多一个 active/waiting plan：
// active 存在则拒绝；waiting 存在则同事务 supersede（旧 plan finished + superseded_by）。
// 校验失败（未知 verb / defer 无出口 / 缺字段 / join 目标非子任务 / 预算超限）返回
// ErrValidation 且 plan 不落库。
func (s *Service) SubmitPlan(ctx context.Context, workspaceID string, p SubmitPlanParams) (*domain.Plan, error) {
	tasks, err := parsePlanSteps(p.Steps)
	if err != nil {
		return nil, err
	}
	if p.AgentProfileID == "" {
		return nil, fmt.Errorf("%w: agent_profile_id required", domain.ErrValidation)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%w: steps required", domain.ErrValidation)
	}
	// M4 预算护栏——max_dispatch 提交期整单校验：超限整单拒绝，不部分执行。
	if p.Guardrails.MaxDispatch != nil {
		if *p.Guardrails.MaxDispatch < 0 {
			return nil, fmt.Errorf("%w: guardrails.max_dispatch 不能为负", domain.ErrValidation)
		}
		n := 0
		for _, t := range tasks {
			if t.verb == domain.PlanVerbDispatch {
				n++
			}
		}
		if n > *p.Guardrails.MaxDispatch {
			return nil, fmt.Errorf("%w: dispatch 步数 %d 超过 max_dispatch %d（整单拒绝，不部分执行）",
				domain.ErrValidation, n, *p.Guardrails.MaxDispatch)
		}
	}
	if p.Guardrails.MaxTokens != nil && *p.Guardrails.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: guardrails.max_tokens 不能为负", domain.ErrValidation)
	}
	var (
		plan        *domain.Plan
		createdRuns []*domain.ExecutionRun
		deferWakeAt *time.Time
	)
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		wi, err := s.store.WorkItems().Get(ctx, p.WorkItemID)
		if err != nil {
			return err
		}
		if wi.WorkspaceID != workspaceID {
			return domain.ErrNotFound
		}
		owner, err := s.store.Agents().Get(ctx, p.AgentProfileID)
		if err != nil {
			return err
		}
		if owner.WorkspaceID != workspaceID {
			return domain.ErrNotFound
		}
		// dispatch 步骤预校验（存在 + 同 workspace + 启用）并标注 M4 审批闸门：
		// 执行期的 createRunLocked 会再校验，但提前失败让整份 plan 不落库。
		if err := s.annotateDispatchSteps(ctx, workspaceID, tasks); err != nil {
			return err
		}
		// join 显式目标必须是本 plan 主任务的既有子任务（M4）：同批次 dispatch
		// 的子任务 id 要到执行期才生成，join 引用的总是先前批次的产物；引用
		// 不存在的 id 属提交错误，整单 400。
		var joins []string
		for _, t := range tasks {
			if t.verb == domain.PlanVerbJoin {
				joins = append(joins, t.joinChildren...)
			}
		}
		if len(joins) > 0 {
			children, err := s.store.WorkItems().ListByParent(ctx, wi.ID)
			if err != nil {
				return err
			}
			own := make(map[string]struct{}, len(children))
			for _, c := range children {
				own[c.ID] = struct{}{}
			}
			for _, id := range joins {
				if _, ok := own[id]; !ok {
					return fmt.Errorf("%w: join step 目标 %s 不是本 plan 主任务的子任务", domain.ErrValidation, id)
				}
			}
		}

		now := time.Now().UTC()
		plan = &domain.Plan{
			ID: domain.NewID(domain.PrefixPlan), WorkspaceID: workspaceID,
			WorkItemID: wi.ID, AgentProfileID: owner.ID, SourceRunID: p.SourceRunID,
			Guardrails: p.Guardrails,
			Status:     domain.PlanActive, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		for i, t := range tasks {
			plan.Steps = append(plan.Steps, domain.PlanStep{
				PlanID: plan.ID, Seq: i, Verb: t.verb, Payload: t.payload,
				Status: domain.PlanStepPending, CreatedAt: now,
			})
		}
		// 唯一活跃校验 + supersede 同事务：提交失败整体回滚（包括 supersede）。
		existing, err := s.store.Plans().ActiveByWorkItem(ctx, wi.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.Status == domain.PlanActive {
				return fmt.Errorf("%w: work item 已有 active plan（%s），等待其完成或取消后再提交", domain.ErrValidation, existing.ID)
			}
			if err := existing.Finish(now, plan.ID); err != nil {
				return err
			}
			if err := s.store.Plans().Update(ctx, existing, existing.Version-1); err != nil {
				return err
			}
			if err := s.emit(ctx, workspaceID, domain.EventPlanFinished,
				domain.AggregatePlan, existing.ID, existing.Version, nil,
				map[string]any{"work_item_id": wi.ID, "superseded_by": plan.ID}); err != nil {
				return err
			}
		}
		if err := s.store.Plans().Create(ctx, plan); err != nil {
			return err
		}
		if err := s.emit(ctx, workspaceID, domain.EventPlanSubmitted,
			domain.AggregatePlan, plan.ID, plan.Version, nil,
			map[string]any{"work_item_id": wi.ID, "steps": len(plan.Steps)}); err != nil {
			return err
		}

		return s.executePlanStepsFrom(ctx, wi, plan, tasks, 0, -1, &createdRuns, &deferWakeAt)
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	// 权威写入提交后才启动 Runtime 副作用（子任务 run 分派）。
	if err := s.dispatchCreatedRuns(ctx, createdRuns); err != nil {
		return plan, err
	}
	// defer/join 带 wake_at：入 timer 型 automation wakeup（同 task_key，coalescing 兜底）。
	if deferWakeAt != nil {
		s.enqueuePlanTimerWake(ctx, workspaceID, plan.AgentProfileID, plan.ID, *deferWakeAt)
	}
	return plan, nil
}

// dispatchCreatedRuns 权威写入提交后启动 Runtime 副作用（SubmitPlan 与审批放行
// 续跑共用）；分派失败回写 run failed（防幽灵 queued run）并返回错误。
func (s *Service) dispatchCreatedRuns(ctx context.Context, runs []*domain.ExecutionRun) error {
	for _, run := range runs {
		if s.dispatcher == nil {
			break
		}
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), run); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), run.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true})
			return fmt.Errorf("dispatch run %s: %w", run.ID, err)
		}
	}
	return nil
}

// enqueuePlanTimerWake defer/join 带 wake_at 的 timer 型 automation wakeup 入队
// （task_key="plan:"+planID，coalescing 以 (agent, task_key) 判定；失败只记日志，
// 终态事件不会再来，无需重试）。
func (s *Service) enqueuePlanTimerWake(ctx context.Context, workspaceID, agentID, planID string, at time.Time) {
	if _, err := scheduling.EnqueueWakeup(context.WithoutCancel(ctx), s.store.Wakeups(),
		domain.WakeupSourceAutomation, workspaceID, agentID, planTaskKey(planID),
		map[string]any{"plan_id": planID, "trigger": "defer_wake_at"}, at); err != nil {
		log.Printf("plan: defer wake_at 入队失败（plan %s）: %v", planID, err)
	}
}

// planTaskKey plan 唤醒锚定的稳定 key（children_quiet 钩子与 defer wake_at 共用，
// coalescing 以 (agent, task_key) 判定）。
func planTaskKey(planID string) string { return "plan:" + planID }

// annotateDispatchSteps dispatch 步骤预校验（存在 + 同 workspace + 启用）并标注
// M4 审批闸门（目标 agent approval_policy=manual）。提交与审批放行续跑共用：
// planTask 不持久化，续跑从持久化 payload 重解析后在此重标注；已批准步骤经
// skipGate 免二次挂起，其后步骤照常挂闸。
func (s *Service) annotateDispatchSteps(ctx context.Context, workspaceID string, tasks []planTask) error {
	for i := range tasks {
		t := &tasks[i]
		if t.verb != domain.PlanVerbDispatch {
			continue
		}
		a, err := s.store.Agents().Get(ctx, t.agentID)
		if err != nil {
			return fmt.Errorf("%w: dispatch step %q 目标 agent %s 不存在", domain.ErrValidation, t.title, t.agentID)
		}
		if a.WorkspaceID != workspaceID {
			return fmt.Errorf("%w: dispatch step %q 目标 agent %s 不属于当前 workspace", domain.ErrValidation, t.title, t.agentID)
		}
		if a.Availability != domain.AgentEnabled {
			return fmt.Errorf("%w: dispatch step %q 目标 agent %s 已停用", domain.ErrValidation, t.title, t.agentID)
		}
		t.approvalGate = a.Policy.ApprovalPolicy == domain.ApprovalPolicyManual
	}
	return nil
}

// executePlanStepsFrom 在提交/审批续跑事务内从 from 起顺序执行步骤：dispatch 建
// 子任务+run（manual 审批策略先挂 ApprovalRequest 并终止本趟——skipGate 步骤
// 免闸，供审批放行续跑）；defer/join 终止本批次（余下 skipped、plan waiting）；
// finish 落终态；全部执行完无 defer/finish 也落 finished。
func (s *Service) executePlanStepsFrom(ctx context.Context, wi *domain.WorkItem, plan *domain.Plan,
	tasks []planTask, from, skipGate int, createdRuns *[]*domain.ExecutionRun, deferWakeAt **time.Time) error {
	// 本批次派生子 run 的批次归属（会话元模型 S1）：首个 dispatch 步骤时解析，
	// 其后共享——同一 plan 的派发同批。
	planDispatchID := ""
	for i := from; i < len(plan.Steps); i++ {
		st := &plan.Steps[i]
		t := tasks[i]
		now := time.Now().UTC()
		switch t.verb {
		case domain.PlanVerbConsultKnowledge:
			// 预取注入：检索结果（条目全文）写进本步骤 payload.results；
			// 同 plan 的 dispatch 可经 knowledge_from 引用拼进子任务指令。
			if s.Knowledge == nil {
				return s.failStepAndPlan(ctx, plan, st, i+1, "no_retriever")
			}
			results, err := s.Knowledge.Retrieve(ctx, knowledge.Query{
				Corpus: t.corpus, Terms: t.terms, Limit: t.limit,
			})
			if err != nil {
				return s.failStepAndPlan(ctx, plan, st, i+1, err.Error())
			}
			if st.Payload == nil {
				st.Payload = map[string]any{}
			}
			st.Payload["results"] = knowledgeResultPayload(results)
			st.Status = domain.PlanStepExecuted
			st.ExecutedAt = &now
			if err := s.store.Plans().UpdateStep(ctx, st); err != nil {
				return err
			}
			if err := s.emitStep(ctx, plan, st); err != nil {
				return err
			}
		case domain.PlanVerbDispatch:
			if t.approvalGate && i != skipGate {
				return s.awaitPlanApproval(ctx, wi, plan, st, t, now)
			}
			child := &domain.WorkItem{
				ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: plan.WorkspaceID,
				ParentID: wi.ID, Title: t.title, Status: domain.WorkItemTodo,
				Priority: t.priority, AgentProfileID: t.agentID,
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}
			if err := s.store.WorkItems().Create(ctx, child); err != nil {
				return err
			}
			if err := s.emit(ctx, plan.WorkspaceID, domain.EventWorkItemCreated,
				domain.AggregateWorkItem, child.ID, child.Version, nil,
				map[string]any{"parent_id": wi.ID, "title": child.Title}); err != nil {
				return err
			}
			// 子 run 继承批次：优先 source run 的 dispatch，否则 lead_plan 兜底批
			//（同事务创建，见 resolvePlanDispatchID）。
			if planDispatchID == "" {
				id, err := s.resolvePlanDispatchID(ctx, plan)
				if err != nil {
					return err
				}
				planDispatchID = id
			}
			run, err := s.createRunLocked(ctx, child.ID, CreateRunParams{
				AgentProfileID:     t.agentID,
				Instruction:        t.instruction + knowledgeAppendix(plan.Step(t.knowledgeFrom)),
				AcceptanceCriteria: t.acceptance,
				DispatchID:         planDispatchID,
			})
			if err != nil {
				return fmt.Errorf("dispatch step %q 创建 run: %w", t.title, err)
			}
			st.Status = domain.PlanStepExecuted
			st.ResultWorkItemID = child.ID
			st.ResultRunID = run.ID
			st.ExecutedAt = &now
			if err := s.store.Plans().UpdateStep(ctx, st); err != nil {
				return err
			}
			*createdRuns = append(*createdRuns, run)
			if err := s.emitStep(ctx, plan, st); err != nil {
				return err
			}
		case domain.PlanVerbDefer, domain.PlanVerbJoin:
			// defer ≡ join{children:"all"}：M4 起执行器内部统一（API 保留两个
			// verb 保语义可读）。批次终止红线（M1）不变：余下步骤 skipped、
			// plan → waiting；静默钩子按本步骤等待集（joinChildren，空 = 全部
			// 子任务）收窄判定。
			// 合法性（防死等）：wake_at 与「等待集内存在未静默子任务」至少居其一。
			// 判定在执行期（本 plan 之前的 dispatch 已建子任务 run）。
			if t.wakeAt == nil {
				quiet, err := s.targetsQuiet(ctx, wi.ID, t.joinChildren)
				if err != nil {
					return err
				}
				if quiet {
					return fmt.Errorf("%w: %s 无出口：既无 wake_at，等待集内子任务也无活跃 run", domain.ErrValidation, t.verb)
				}
			}
			st.Status = domain.PlanStepExecuted
			st.ExecutedAt = &now
			if err := s.store.Plans().UpdateStep(ctx, st); err != nil {
				return err
			}
			if err := s.emitStep(ctx, plan, st); err != nil {
				return err
			}
			if err := s.skipRemainingSteps(ctx, plan, i+1); err != nil {
				return err
			}
			if err := plan.MarkWaiting(now); err != nil {
				return err
			}
			if err := s.store.Plans().Update(ctx, plan, plan.Version-1); err != nil {
				return err
			}
			if err := s.emit(ctx, plan.WorkspaceID, domain.EventPlanWaiting,
				domain.AggregatePlan, plan.ID, plan.Version, nil,
				map[string]any{"work_item_id": wi.ID}); err != nil {
				return err
			}
			*deferWakeAt = t.wakeAt
			return nil
		case domain.PlanVerbFinish:
			st.Status = domain.PlanStepExecuted
			st.ExecutedAt = &now
			if err := s.store.Plans().UpdateStep(ctx, st); err != nil {
				return err
			}
			if err := s.emitStep(ctx, plan, st); err != nil {
				return err
			}
			if err := s.finishPlan(ctx, plan, i+1, ""); err != nil {
				return err
			}
			if !t.evaluation {
				return nil
			}
			// finish{evaluation:true}：plan 落 finished 后自动为 plan owner 在
			// 主任务上创建评估 run（确定性模板 + input.evaluation=true 固化，
			// verdict 提取以此门控）；分派同 createdRuns 推迟到外层提交后。
			instruction, err := s.buildEvaluationInstruction(ctx, plan)
			if err != nil {
				return fmt.Errorf("构建评估指令: %w", err)
			}
			evalRun, err := s.createRunLocked(ctx, plan.WorkItemID, CreateRunParams{
				AgentProfileID: plan.AgentProfileID, Instruction: instruction, Evaluation: true,
			})
			if err != nil {
				return fmt.Errorf("创建评估 run: %w", err)
			}
			*createdRuns = append(*createdRuns, evalRun)
			return nil
		}
	}
	// 所有 step 执行完（无 defer/finish）：顺序执行完即 finished。
	return s.finishPlan(ctx, plan, len(plan.Steps), "")
}

// finishPlan 落终态：跳过 fromSeq 起的余下步骤并迁移 plan → finished。
func (s *Service) finishPlan(ctx context.Context, plan *domain.Plan, fromSeq int, supersededBy string) error {
	if err := s.skipRemainingSteps(ctx, plan, fromSeq); err != nil {
		return err
	}
	if err := plan.Finish(time.Now().UTC(), supersededBy); err != nil {
		return err
	}
	if err := s.store.Plans().Update(ctx, plan, plan.Version-1); err != nil {
		return err
	}
	data := map[string]any{"work_item_id": plan.WorkItemID}
	if supersededBy != "" {
		data["superseded_by"] = supersededBy
	}
	return s.emit(ctx, plan.WorkspaceID, domain.EventPlanFinished,
		domain.AggregatePlan, plan.ID, plan.Version, nil, data)
}

// failStepAndPlan 步骤失败收口：step 落 failed（error 记录原因），余下步骤
// skipped，plan → failed（M1 生命周期：任一 step 失败即 plan 终态）。
// 失败可观测面：plan.step_executed 事件 data.status=failed + data.error，
// 以及 plan.failed 事件（M4 起统一发布，plan 终态全部有事件）。
func (s *Service) failStepAndPlan(ctx context.Context, plan *domain.Plan, st *domain.PlanStep, fromSeq int, stepErr string) error {
	now := time.Now().UTC()
	st.Status = domain.PlanStepFailed
	st.Error = stepErr
	st.ExecutedAt = &now
	if err := s.store.Plans().UpdateStep(ctx, st); err != nil {
		return err
	}
	if err := s.emitStep(ctx, plan, st); err != nil {
		return err
	}
	if err := s.skipRemainingSteps(ctx, plan, fromSeq); err != nil {
		return err
	}
	if err := plan.Transition(domain.PlanFailed, time.Now().UTC()); err != nil {
		return err
	}
	if err := s.store.Plans().Update(ctx, plan, plan.Version-1); err != nil {
		return err
	}
	return s.emit(ctx, plan.WorkspaceID, domain.EventPlanFailed,
		domain.AggregatePlan, plan.ID, plan.Version, nil,
		map[string]any{"work_item_id": plan.WorkItemID, "error": stepErr})
}

// awaitPlanApproval M4 审批护栏挂起：dispatch 目标 agent approval_policy=manual
// 时该步骤不直接执行——创建 ApprovalRequest（kind=plan_dispatch，RunID 空：不挂
// run，迁移 0010 放开约束；RequestedBy 定位 plan 与步骤 seq）、plan → waiting
// （reason=pending_approval）、步骤保持 pending。批次由 ResolveApproval 审批回调
// 续跑（resolvePlanDispatchApproval）或收口。
func (s *Service) awaitPlanApproval(ctx context.Context, wi *domain.WorkItem, plan *domain.Plan,
	st *domain.PlanStep, t planTask, now time.Time) error {
	a := &domain.ApprovalRequest{
		ID:          domain.NewID(domain.PrefixApproval),
		WorkItemID:  wi.ID,
		Kind:        domain.ApprovalKindPlanDispatch,
		Risk:        "medium",
		Summary:     fmt.Sprintf("plan 派发「%s」→ agent %s", t.title, t.agentID),
		RequestedBy: map[string]any{"kind": "plan", "id": plan.ID, "seq": st.Seq},
		Status:      domain.ApprovalPending,
		CreatedAt:   now,
	}
	if err := s.store.Runs().CreateApproval(ctx, a); err != nil {
		return err
	}
	if err := s.emit(ctx, plan.WorkspaceID, domain.EventApprovalRequested,
		domain.AggregateApproval, a.ID, 1, nil,
		map[string]any{"kind": a.Kind, "risk": a.Risk, "summary": a.Summary,
			"plan_id": plan.ID, "seq": st.Seq}); err != nil {
		return err
	}
	if err := plan.MarkWaiting(now); err != nil {
		return err
	}
	if err := s.store.Plans().Update(ctx, plan, plan.Version-1); err != nil {
		return err
	}
	return s.emit(ctx, plan.WorkspaceID, domain.EventPlanWaiting,
		domain.AggregatePlan, plan.ID, plan.Version, nil,
		map[string]any{"work_item_id": wi.ID, "reason": "pending_approval", "approval_id": a.ID})
}

// resolvePlanDispatchApproval M4 审批护栏回调（ResolveApproval 事务内，审批决定
// 已持久化后）：RequestedBy={kind:plan,id,seq} 定位挂起步骤。批准 →
// waiting→active，该步免闸执行并继续批次（后续步骤照常——再遇 manual dispatch
// 挂新审批）；拒绝 → step failed + plan failed（guardrail 语义：人否决了路线）。
// plan 已被 supersede/取消/失败时迟到决定不再触碰 plan（决定本身已持久化，静默
// 钩子也不会再驱动它）。步骤规约从持久化 payload 重解析（planTask 不跨事务存活），
// dispatch 闸门随重解析重标注。
func (s *Service) resolvePlanDispatchApproval(ctx context.Context, a *domain.ApprovalRequest, approved bool, reason string,
	createdRuns *[]*domain.ExecutionRun, deferWakeAt **time.Time) (*domain.Plan, error) {
	planID, _ := a.RequestedBy["id"].(string)
	if planID == "" {
		return nil, fmt.Errorf("%w: 审批 %s 的 RequestedBy 缺少 plan id", domain.ErrValidation, a.ID)
	}
	seq, ok := asPlanInt(a.RequestedBy["seq"])
	if !ok {
		return nil, fmt.Errorf("%w: 审批 %s 的 RequestedBy 缺少步骤 seq", domain.ErrValidation, a.ID)
	}
	plan, err := s.store.Plans().Get(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan.Status != domain.PlanWaiting {
		log.Printf("plan: 迟到的 plan_dispatch 审批决定（审批 %s，plan %s 状态 %s），不再触碰 plan", a.ID, plan.ID, plan.Status)
		return plan, nil
	}
	inputs := make([]PlanStepInput, len(plan.Steps))
	for i, st := range plan.Steps {
		inputs[i] = PlanStepInput{Verb: string(st.Verb), Payload: st.Payload}
	}
	tasks, err := parsePlanSteps(inputs)
	if err != nil {
		return nil, fmt.Errorf("plan %s 步骤重解析: %w", plan.ID, err)
	}
	if err := s.annotateDispatchSteps(ctx, plan.WorkspaceID, tasks); err != nil {
		return nil, err
	}
	wi, err := s.store.WorkItems().Get(ctx, plan.WorkItemID)
	if err != nil {
		return nil, err
	}
	if !approved {
		st := plan.Step(seq)
		if st == nil {
			return nil, fmt.Errorf("%w: plan %s 无步骤 seq=%d", domain.ErrValidation, plan.ID, seq)
		}
		msg := "plan 派发审批被拒绝"
		if reason != "" {
			msg += "：" + reason
		}
		return plan, s.failStepAndPlan(ctx, plan, st, seq+1, msg)
	}
	if err := plan.MarkActive(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.store.Plans().Update(ctx, plan, plan.Version-1); err != nil {
		return nil, err
	}
	return plan, s.executePlanStepsFrom(ctx, wi, plan, tasks, seq, seq, createdRuns, deferWakeAt)
}

// skipRemainingSteps 把 fromSeq 起的步骤标记 skipped（defer/finish 之后的步骤不执行）。
func (s *Service) skipRemainingSteps(ctx context.Context, plan *domain.Plan, fromSeq int) error {
	for j := fromSeq; j < len(plan.Steps); j++ {
		if plan.Steps[j].Status != domain.PlanStepPending {
			continue
		}
		plan.Steps[j].Status = domain.PlanStepSkipped
		if err := s.store.Plans().UpdateStep(ctx, &plan.Steps[j]); err != nil {
			return err
		}
	}
	return nil
}

// emitStep 发布单个步骤执行事件（步骤级行级审计的事件面）。
// 事件信封契约（前端 store 路由依据）：aggregate.type=plan、aggregate id=plan id、
// data 携带 work_item_id。
func (s *Service) emitStep(ctx context.Context, plan *domain.Plan, st *domain.PlanStep) error {
	data := map[string]any{"work_item_id": plan.WorkItemID, "seq": st.Seq, "verb": string(st.Verb), "status": string(st.Status)}
	if st.ResultWorkItemID != "" {
		data["result_work_item_id"] = st.ResultWorkItemID
	}
	if st.ResultRunID != "" {
		data["result_run_id"] = st.ResultRunID
	}
	if st.Error != "" {
		data["error"] = st.Error
	}
	return s.emit(ctx, plan.WorkspaceID, domain.EventPlanStepExecuted,
		domain.AggregatePlan, plan.ID, plan.Version, nil, data)
}

// Plan 读取 plan（含步骤执行结果）。
func (s *Service) Plan(ctx context.Context, id string) (*domain.Plan, error) {
	return s.store.Plans().Get(ctx, id)
}

// LatestPlanForWorkItem 返回主任务最新一份 plan（按 created_at 最新，不限状态）；
// 无 plan 返回 ErrNotFound。任务详情页冷启动（无 SSE 回放）的 plan 投影入口。
func (s *Service) LatestPlanForWorkItem(ctx context.Context, workItemID string) (*domain.Plan, error) {
	plan, err := s.store.Plans().LatestByWorkItem(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, domain.ErrNotFound
	}
	return plan, nil
}

// WorkItemTree 先序返回以 workItemID 为根的整棵子树（含根；同级按创建序）。
func (s *Service) WorkItemTree(ctx context.Context, workItemID string) ([]*domain.WorkItem, error) {
	root, err := s.store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		return nil, err
	}
	out := []*domain.WorkItem{}
	var walk func(wi *domain.WorkItem) error
	walk = func(wi *domain.WorkItem) error {
		out = append(out, wi)
		children, err := s.store.WorkItems().ListByParent(ctx, wi.ID)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return out, nil
}

// targetsQuiet 判定等待集内子任务是否全部静默：任一目标子任务存在非终态 run 即
// 未静默（无子任务、或目标只有终态 run 均为静默——静默后不会再有终态事件触发
// 唤醒）。targets 空 = 全部子任务（defer 与 join{children:"all"} 语义）。
func (s *Service) targetsQuiet(ctx context.Context, workItemID string, targets []string) (bool, error) {
	if len(targets) == 0 {
		children, err := s.store.WorkItems().ListByParent(ctx, workItemID)
		if err != nil {
			return false, err
		}
		targets = make([]string, 0, len(children))
		for _, c := range children {
			targets = append(targets, c.ID)
		}
	}
	for _, id := range targets {
		runs, err := s.store.Runs().ListByWorkItem(ctx, id)
		if err != nil {
			return false, err
		}
		for _, r := range runs {
			if !r.Status.IsTerminal() {
				return false, nil
			}
		}
	}
	return true, nil
}

// waitingPlanJoinStep 返回 waiting plan 中已执行的 defer/join 步骤（批次挂起
// 锚点）；审批挂起（pending_approval）的 waiting plan 无此锚点（步骤仍 pending），
// 静默钩子据此跳过——它的续跑由审批回调驱动，与子任务静默无关。
func waitingPlanJoinStep(plan *domain.Plan) *domain.PlanStep {
	for i := len(plan.Steps) - 1; i >= 0; i-- {
		st := &plan.Steps[i]
		if st.Status != domain.PlanStepExecuted {
			continue
		}
		if st.Verb == domain.PlanVerbDefer || st.Verb == domain.PlanVerbJoin {
			return st
		}
	}
	return nil
}

// joinChildrenOf 从持久化步骤 payload 读取 join 显式等待集（children 数组）；
// defer、join{children:"all"} 或载荷缺失返回 nil（= 全部子任务）。
func joinChildrenOf(st *domain.PlanStep) []string {
	if st == nil || st.Verb != domain.PlanVerbJoin {
		return nil
	}
	switch list := st.Payload["children"].(type) {
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if id, ok := item.(string); ok && id != "" {
				out = append(out, id)
			}
		}
		if len(out) > 0 {
			return out
		}
	case []string:
		if len(list) > 0 {
			return list
		}
	}
	return nil
}

// treeTokenUsage 主任务树全部 run 的 token 合计（UsageIn+UsageOut）——M4 预算
// 护栏 max_tokens 的核算口径。
func (s *Service) treeTokenUsage(ctx context.Context, workItemID string) (int64, error) {
	tree, err := s.WorkItemTree(ctx, workItemID)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, wi := range tree {
		runs, err := s.store.Runs().ListByWorkItem(ctx, wi.ID)
		if err != nil {
			return 0, err
		}
		for _, r := range runs {
			total += r.UsageIn + r.UsageOut
		}
	}
	return total, nil
}

// failPlanForBudget M4 预算护栏收口（子任务静默唤醒点核算超限）：plan → failed
// （error=budget_exceeded + plan.failed 事件）为权威结果；主任务随后落 blocker
// （人可见）——主任务已终态无法迁移时只记日志（plan.failed 事件与 plan.Error
// 已可观测），不回滚 plan 失败。
func (s *Service) failPlanForBudget(ctx context.Context, plan *domain.Plan, used, limit int64) {
	wctx := context.WithoutCancel(ctx)
	err := s.store.InTx(wctx, func(ctx context.Context) error {
		fresh, err := s.store.Plans().Get(ctx, plan.ID)
		if err != nil {
			return err
		}
		if fresh.Status.IsTerminal() {
			return nil // 竞态下已终态：幂等收口，不重复迁移
		}
		fresh.Error = domain.PlanErrorBudgetExceeded
		if err := fresh.Transition(domain.PlanFailed, time.Now().UTC()); err != nil {
			return err
		}
		if err := s.store.Plans().Update(ctx, fresh, fresh.Version-1); err != nil {
			return err
		}
		if err := s.emit(ctx, fresh.WorkspaceID, domain.EventPlanFailed,
			domain.AggregatePlan, fresh.ID, fresh.Version, nil,
			map[string]any{"work_item_id": fresh.WorkItemID, "error": domain.PlanErrorBudgetExceeded,
				"tokens_used": used, "tokens_limit": limit}); err != nil {
			return err
		}
		return s.activity(ctx, fresh.WorkspaceID, "plan.budget_exceeded",
			fmt.Sprintf("预算超限：token 合计 %d 超过 max_tokens %d，plan %s 失败", used, limit, fresh.ID))
	})
	if err != nil {
		log.Printf("plan: 预算护栏收口失败（plan %s）: %v", plan.ID, err)
		return
	}
	s.notifier.Notify(plan.WorkspaceID)
	err = s.store.InTx(wctx, func(ctx context.Context) error {
		wi, err := s.store.WorkItems().Get(ctx, plan.WorkItemID)
		if err != nil {
			return err
		}
		return s.blockLocked(ctx, wi, BlockParams{
			Code:    domain.PlanErrorBudgetExceeded,
			Message: fmt.Sprintf("plan %s 预算超限：token 合计 %d 超过 max_tokens %d", plan.ID, used, limit),
			Source:  "control_plane",
		})
	})
	if err != nil {
		log.Printf("plan: 预算 blocker 落库失败（work item %s）: %v", plan.WorkItemID, err)
	}
}

// maybeAdvancePlans 子任务静默钩子（RecordRunStatus 终态提交后、事务外调用；尽力而为）：
// 终态 run 所属 work item 有 parent、parent 存在 waiting plan 且该 plan 挂起于
// defer/join（审批挂起无锚点，跳过）→ 按挂起步骤的等待集（join 显式收窄，defer/
// join{"all"} 为全部子任务）判定「等待集内无活跃 run」，成立则先核算 M4 预算
// （max_tokens，主任务树全部 run 的 UsageIn+UsageOut 合计，超限 → plan failed +
// 主任务 blocker），未超限入 automation wakeup 唤醒 plan owner（source=automation、
// task_key="plan:"+planID、context={plan_id, trigger:"children_quiet"}）。
// 唤醒 ≠ plan 继续：owner 观察全局后提交新 plan（旧 waiting plan 由 supersede 收口）。
func (s *Service) maybeAdvancePlans(ctx context.Context, r *domain.ExecutionRun) {
	if r == nil || !r.Status.IsTerminal() {
		return
	}
	wi, err := s.store.WorkItems().Get(ctx, r.WorkItemID)
	if err != nil || wi.ParentID == "" {
		return
	}
	parent, err := s.store.WorkItems().Get(ctx, wi.ParentID)
	if err != nil {
		return
	}
	plan, err := s.store.Plans().ActiveByWorkItem(ctx, parent.ID)
	if err != nil || plan == nil || plan.Status != domain.PlanWaiting {
		return
	}
	joinStep := waitingPlanJoinStep(plan)
	if joinStep == nil {
		return // pending_approval 挂起：续跑由审批回调驱动，与静默无关
	}
	quiet, err := s.targetsQuiet(ctx, parent.ID, joinChildrenOf(joinStep))
	if err != nil || !quiet {
		return
	}
	// M4 预算护栏：静默唤醒点核算，超限即收口不唤醒。
	if plan.Guardrails.MaxTokens != nil {
		used, err := s.treeTokenUsage(ctx, parent.ID)
		if err != nil {
			log.Printf("plan: token 用量核算失败（plan %s）: %v", plan.ID, err)
			return
		}
		if used > *plan.Guardrails.MaxTokens {
			s.failPlanForBudget(ctx, plan, used, *plan.Guardrails.MaxTokens)
			return
		}
	}
	wctx := context.WithoutCancel(ctx)
	if _, err := scheduling.EnqueueWakeup(wctx, s.store.Wakeups(), domain.WakeupSourceAutomation,
		plan.WorkspaceID, plan.AgentProfileID, planTaskKey(plan.ID),
		map[string]any{"plan_id": plan.ID, "trigger": "children_quiet"}, time.Time{}); err != nil {
		log.Printf("plan: children_quiet 唤醒入队失败（plan %s）: %v", plan.ID, err)
		return
	}
	_ = s.activity(wctx, plan.WorkspaceID, "plan.children_quiet",
		fmt.Sprintf("等待集子任务全部静默，唤醒 plan owner（plan %s / agent %s）", plan.ID, plan.AgentProfileID))
}

// parsePlanSteps 逐动词校验步骤载荷；任何失败让整份提交落 ErrValidation。
func parsePlanSteps(inputs []PlanStepInput) ([]planTask, error) {
	tasks := make([]planTask, 0, len(inputs))
	for i, in := range inputs {
		verb := domain.PlanVerb(in.Verb)
		if !domain.ValidPlanVerb(verb) {
			return nil, fmt.Errorf("%w: step %d 未知 verb %q（支持 dispatch/defer/join/finish/consult_knowledge）", domain.ErrValidation, i, in.Verb)
		}
		t := planTask{verb: verb, payload: in.Payload}
		if t.payload == nil {
			t.payload = map[string]any{}
		}
		switch verb {
		case domain.PlanVerbDispatch:
			t.agentID, _ = t.payload["agent_id"].(string)
			t.title, _ = t.payload["title"].(string)
			t.instruction, _ = t.payload["instruction"].(string)
			if raw, ok := t.payload["acceptance"].([]any); ok {
				for _, item := range raw {
					if text, ok := item.(string); ok {
						t.acceptance = append(t.acceptance, text)
					}
				}
			}
			if p, ok := t.payload["priority"].(string); ok && p != "" {
				t.priority = domain.Priority(p)
			} else {
				t.priority = domain.PriorityMedium
			}
			if t.agentID == "" || t.title == "" || t.instruction == "" {
				return nil, fmt.Errorf("%w: dispatch step %d 需要 agent_id/title/instruction", domain.ErrValidation, i)
			}
			switch t.priority {
			case domain.PriorityLow, domain.PriorityMedium, domain.PriorityHigh, domain.PriorityUrgent:
			default:
				return nil, fmt.Errorf("%w: dispatch step %d priority %q 无效", domain.ErrValidation, i, t.priority)
			}
			// knowledge_from 必须引用更早的 consult_knowledge 步骤（同批次内，
			// 执行序保证检索结果先于 dispatch 落在 payload）。
			t.knowledgeFrom = -1
			if v, ok := asPlanInt(t.payload["knowledge_from"]); ok {
				if v < 0 || v >= i {
					return nil, fmt.Errorf("%w: dispatch step %d knowledge_from=%d 必须指向更早的步骤", domain.ErrValidation, i, v)
				}
				if domain.PlanVerb(inputs[v].Verb) != domain.PlanVerbConsultKnowledge {
					return nil, fmt.Errorf("%w: dispatch step %d knowledge_from=%d 指向的不是 consult_knowledge 步骤", domain.ErrValidation, i, v)
				}
				t.knowledgeFrom = v
			}
		case domain.PlanVerbConsultKnowledge:
			t.corpus, _ = t.payload["corpus"].(string)
			if t.corpus == "" {
				return nil, fmt.Errorf("%w: consult_knowledge step %d 需要 corpus", domain.ErrValidation, i)
			}
			// corpus 直接拼进检索路径且来自不可信输入：单层目录名，拒绝分隔符与跳转。
			if t.corpus == "." || t.corpus == ".." || strings.ContainsAny(t.corpus, `/\`) {
				return nil, fmt.Errorf("%w: consult_knowledge step %d corpus %q 非法（单层目录名）", domain.ErrValidation, i, t.corpus)
			}
			if raw, ok := t.payload["terms"].([]any); ok {
				for _, item := range raw {
					if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
						t.terms = append(t.terms, text)
					}
				}
			}
			if v, ok := asPlanInt(t.payload["limit"]); ok {
				t.limit = v
			}
		case domain.PlanVerbDefer:
			if raw, ok := t.payload["wake_at"].(string); ok && raw != "" {
				wakeAt, err := time.Parse(time.RFC3339, raw)
				if err != nil {
					return nil, fmt.Errorf("%w: defer step %d wake_at 必须为 RFC3339", domain.ErrValidation, i)
				}
				t.wakeAt = &wakeAt
			}
		case domain.PlanVerbJoin:
			// join{children:"all"|[wi_id,...], wake_at?}：显式等待集的 defer
			// 变体。children 必填（等全静默用 "all"；省等待集请直接用 defer）。
			if raw, ok := t.payload["wake_at"].(string); ok && raw != "" {
				wakeAt, err := time.Parse(time.RFC3339, raw)
				if err != nil {
					return nil, fmt.Errorf("%w: join step %d wake_at 必须为 RFC3339", domain.ErrValidation, i)
				}
				t.wakeAt = &wakeAt
			}
			raw, ok := t.payload["children"]
			if !ok {
				return nil, fmt.Errorf("%w: join step %d 需要 children（\"all\" 或子任务 id 数组）", domain.ErrValidation, i)
			}
			switch v := raw.(type) {
			case string:
				if v != "all" {
					return nil, fmt.Errorf("%w: join step %d children 字符串仅可为 \"all\"", domain.ErrValidation, i)
				}
			case []any:
				seen := make(map[string]struct{}, len(v))
				for _, item := range v {
					id, ok := item.(string)
					if !ok || id == "" {
						return nil, fmt.Errorf("%w: join step %d children 数组元素必须为子任务 id", domain.ErrValidation, i)
					}
					if _, dup := seen[id]; dup {
						return nil, fmt.Errorf("%w: join step %d children 数组存在重复目标 %s", domain.ErrValidation, i, id)
					}
					seen[id] = struct{}{}
					t.joinChildren = append(t.joinChildren, id)
				}
				if len(t.joinChildren) == 0 {
					return nil, fmt.Errorf("%w: join step %d children 数组不能为空（等全静默用 \"all\"）", domain.ErrValidation, i)
				}
			default:
				return nil, fmt.Errorf("%w: join step %d children 必须为 \"all\" 或子任务 id 数组", domain.ErrValidation, i)
			}
		case domain.PlanVerbFinish:
			// finish 无必填字段（summary 留在 payload 原文里）；
			// evaluation=true 触发评估 run（M2）。
			if v, ok := t.payload["evaluation"].(bool); ok {
				t.evaluation = v
			}
		default:
			// 白名单内但解析器未覆盖的 verb（域层先于执行器扩展的中间态）：
			// 响亮拒绝，绝不静默跳过（提交期 400 胜过执行期无声 no-op）。
			return nil, fmt.Errorf("%w: step %d verb %q 未接入执行器", domain.ErrValidation, i, verb)
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// asPlanInt 兼容 JSON 解码（float64）与直接构造（int/int64）的整数载荷。
func asPlanInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

// knowledgeResultPayload 检索结果的结构化形态（consult_knowledge 执行后写入
// 步骤 payload 的 results 键）：dispatch knowledge_from 以此为唯一输入源。
func knowledgeResultPayload(results []knowledge.Result) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		out = append(out, map[string]any{
			"id": r.Entry.ID, "title": r.Entry.Title, "version": r.Entry.Version,
			"body": r.Entry.Body, "snippet": r.Snippet, "score": r.Score,
		})
	}
	return out
}

// knowledgeAppendix 把 consult_knowledge 步骤的检索结果条目全文确定性拼装为
// 子任务 instruction 的「## 参考条目」节（不经模型）。无结果返回空串。
func knowledgeAppendix(st *domain.PlanStep) string {
	if st == nil {
		return ""
	}
	var entries []map[string]any
	switch list := st.Payload["results"].(type) {
	case []map[string]any:
		entries = list
	case []any:
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				entries = append(entries, m)
			}
		}
	}
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## 参考条目\n")
	for _, e := range entries {
		id, _ := e["id"].(string)
		title, _ := e["title"].(string)
		body, _ := e["body"].(string)
		fmt.Fprintf(&b, "\n### %s %s（v%v）\n%s\n", id, title, e["version"], body)
	}
	return b.String()
}
