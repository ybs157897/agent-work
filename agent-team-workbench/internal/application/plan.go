// plan.go M1/M2 编排计划：SubmitPlan 确定性执行器 + 子任务静默唤醒钩子（设计 note
// notes/implemented/orchestration/2026-08-23-m1-plan-executor.md 与
// 2026-08-23-m2-lead-planner-evaluation.md）。
// 词汇表：dispatch（建子任务+首 run）/ defer（批次终止挂起）/ finish（收尾，
// evaluation=true 触发评估 run）/ consult_knowledge（预取检索注入）。
// 执行器同一提交事务内顺序推进，副作用（Dispatch/Notify/wakeup 入队）在提交后执行。
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
	// dispatch 引用的 consult_knowledge 步骤 seq（knowledge_from）；-1 未引用。
	knowledgeFrom int
	// consult_knowledge 专属字段（检索参数）。
	corpus string
	terms  []string
	limit  int
	// defer 专属字段；nil 表示未指定 wake_at。
	wakeAt *time.Time
	// finish 专属字段：evaluation=true 时 plan 落 finished 后自动创建评估 run。
	evaluation bool
}

// SubmitPlan 校验并同步执行一份 plan。同一 work item 同时最多一个 active/waiting plan：
// active 存在则拒绝；waiting 存在则同事务 supersede（旧 plan finished + superseded_by）。
// 校验失败（未知 verb / defer 无出口 / 缺字段）返回 ErrValidation 且 plan 不落库。
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
		// dispatch 目标 agent 预校验（存在 + 同 workspace + 启用）：
		// 执行期的 createRunLocked 会再校验，但提前失败让整份 plan 不落库。
		for _, t := range tasks {
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
		}

		now := time.Now().UTC()
		plan = &domain.Plan{
			ID: domain.NewID(domain.PrefixPlan), WorkspaceID: workspaceID,
			WorkItemID: wi.ID, AgentProfileID: owner.ID, SourceRunID: p.SourceRunID,
			Status: domain.PlanActive, Version: 1, CreatedAt: now, UpdatedAt: now,
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

		return s.executePlanSteps(ctx, wi, plan, tasks, &createdRuns, &deferWakeAt)
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(workspaceID)
	// 权威写入提交后才启动 Runtime 副作用（子任务 run 分派）。
	for _, run := range createdRuns {
		if s.dispatcher == nil {
			break
		}
		if err := s.dispatcher.Dispatch(context.WithoutCancel(ctx), run); err != nil {
			_ = s.RecordRunStatus(context.WithoutCancel(ctx), run.ID, domain.RunFailed,
				map[string]any{"code": "dispatch_failed", "message": err.Error(), "retryable": true})
			return plan, fmt.Errorf("dispatch run %s: %w", run.ID, err)
		}
	}
	// defer 带 wake_at：入 timer 型 automation wakeup（同 task_key，coalescing 兜底）。
	if deferWakeAt != nil {
		if _, err := scheduling.EnqueueWakeup(context.WithoutCancel(ctx), s.store.Wakeups(),
			domain.WakeupSourceAutomation, workspaceID, plan.AgentProfileID, planTaskKey(plan.ID),
			map[string]any{"plan_id": plan.ID, "trigger": "defer_wake_at"}, *deferWakeAt); err != nil {
			log.Printf("plan: defer wake_at 入队失败（plan %s）: %v", plan.ID, err)
		}
	}
	return plan, nil
}

// planTaskKey plan 唤醒锚定的稳定 key（children_quiet 钩子与 defer wake_at 共用，
// coalescing 以 (agent, task_key) 判定）。
func planTaskKey(planID string) string { return "plan:" + planID }

// executePlanSteps 在提交事务内顺序执行步骤：dispatch 建子任务+run；defer 终止本批次
// （余下 skipped、plan waiting）；finish 落终态；全部执行完无 defer/finish 也落 finished。
func (s *Service) executePlanSteps(ctx context.Context, wi *domain.WorkItem, plan *domain.Plan,
	tasks []planTask, createdRuns *[]*domain.ExecutionRun, deferWakeAt **time.Time) error {
	for i := range plan.Steps {
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
			run, err := s.createRunLocked(ctx, child.ID, CreateRunParams{
				AgentProfileID:     t.agentID,
				Instruction:        t.instruction + knowledgeAppendix(plan.Step(t.knowledgeFrom)),
				AcceptanceCriteria: t.acceptance,
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
		case domain.PlanVerbDefer:
			// defer 合法性（防死等）：wake_at 与「存在未静默子任务」至少居其一。
			// 判定在执行期（本 plan 之前的 dispatch 已建子任务 run）。
			if t.wakeAt == nil {
				quiet, err := s.childrenQuiet(ctx, wi.ID)
				if err != nil {
					return err
				}
				if quiet {
					return fmt.Errorf("%w: defer 无出口：既无 wake_at，子任务也无活跃 run", domain.ErrValidation)
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
// 失败可观测面：plan.step_executed 事件 data.status=failed + data.error。
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
	return s.store.Plans().Update(ctx, plan, plan.Version-1)
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

// childrenQuiet 判定主任务全部子任务是否已静默：任一子任务存在非终态 run 即未静默
// （无子任务、或子任务只有终态 run 均为静默——静默后不会再有终态事件触发唤醒）。
func (s *Service) childrenQuiet(ctx context.Context, workItemID string) (bool, error) {
	children, err := s.store.WorkItems().ListByParent(ctx, workItemID)
	if err != nil {
		return false, err
	}
	for _, c := range children {
		runs, err := s.store.Runs().ListByWorkItem(ctx, c.ID)
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

// maybeAdvancePlans 子任务静默钩子（RecordRunStatus 终态提交后、事务外调用；尽力而为）：
// 终态 run 所属 work item 有 parent、parent 存在 waiting plan、parent 全部子任务无
// 活跃 run → 入 automation wakeup 唤醒 plan owner（source=automation、
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
	quiet, err := s.childrenQuiet(ctx, parent.ID)
	if err != nil || !quiet {
		return
	}
	wctx := context.WithoutCancel(ctx)
	if _, err := scheduling.EnqueueWakeup(wctx, s.store.Wakeups(), domain.WakeupSourceAutomation,
		plan.WorkspaceID, plan.AgentProfileID, planTaskKey(plan.ID),
		map[string]any{"plan_id": plan.ID, "trigger": "children_quiet"}, time.Time{}); err != nil {
		log.Printf("plan: children_quiet 唤醒入队失败（plan %s）: %v", plan.ID, err)
		return
	}
	_ = s.activity(wctx, plan.WorkspaceID, "plan.children_quiet",
		fmt.Sprintf("子任务全部静默，唤醒 plan owner（plan %s / agent %s）", plan.ID, plan.AgentProfileID))
}

// parsePlanSteps 逐动词校验步骤载荷；任何失败让整份提交落 ErrValidation。
func parsePlanSteps(inputs []PlanStepInput) ([]planTask, error) {
	tasks := make([]planTask, 0, len(inputs))
	for i, in := range inputs {
		verb := domain.PlanVerb(in.Verb)
		if !domain.ValidPlanVerb(verb) {
			return nil, fmt.Errorf("%w: step %d 未知 verb %q（支持 dispatch/defer/finish/consult_knowledge）", domain.ErrValidation, i, in.Verb)
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
		case domain.PlanVerbFinish:
			// finish 无必填字段（summary 留在 payload 原文里）；
			// evaluation=true 触发评估 run（M2）。
			if v, ok := t.payload["evaluation"].(bool); ok {
				t.evaluation = v
			}
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
