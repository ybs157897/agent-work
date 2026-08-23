package domain

import "time"

// PlanStatus 状态机（M1 编排设计 note §生命周期 + M4 审批护栏）：
//
//	active ──所有 step 执行完──▶ finished
//	active ──遇到 defer/join───▶ waiting ──同主任务新 plan 提交──▶ finished（superseded_by 记新 plan）
//	active ──manual dispatch───▶ waiting ──审批放行（M4）──▶ active（唯一回拨；静默唤醒不回拨）
//	active/waiting ──用户取消──▶ cancelled        任一 step 失败 / 预算超限 ──▶ failed
//
// finished/cancelled/failed 为终态，不可逆。
type PlanStatus string

const (
	PlanActive    PlanStatus = "active"
	PlanWaiting   PlanStatus = "waiting"
	PlanFinished  PlanStatus = "finished"
	PlanCancelled PlanStatus = "cancelled"
	PlanFailed    PlanStatus = "failed"
)

var planTransitions = map[PlanStatus][]PlanStatus{
	PlanActive:  {PlanWaiting, PlanFinished, PlanCancelled, PlanFailed},
	PlanWaiting: {PlanActive, PlanFinished, PlanCancelled, PlanFailed},
}

func (s PlanStatus) IsTerminal() bool {
	return s == PlanFinished || s == PlanCancelled || s == PlanFailed
}

func (s PlanStatus) CanTransitionTo(to PlanStatus) bool {
	for _, ok := range planTransitions[s] {
		if ok == to {
			return true
		}
	}
	return false
}

// PlanVerb 词汇表：M1 三动词 + M2 consult_knowledge + M4 join；use_session 是
// 默认行为，未知 verb 由提交校验拒绝（不进状态机）。
type PlanVerb string

const (
	PlanVerbDispatch PlanVerb = "dispatch"
	PlanVerbDefer    PlanVerb = "defer"
	PlanVerbFinish   PlanVerb = "finish"
	// PlanVerbConsultKnowledge M2：预取检索知识语料，结果写进步骤 payload 的
	// results 键，供后续 dispatch 的 knowledge_from 确定性注入子任务指令。
	PlanVerbConsultKnowledge PlanVerb = "consult_knowledge"
	// PlanVerbJoin M4：带显式等待集的 defer 变体（children="all" 或子任务 id
	// 列表）；批次同样终止挂起，静默钩子只判定等待集内子任务。
	PlanVerbJoin PlanVerb = "join"
)

// ValidPlanVerb 报告 v 是否为支持的动词（dispatch/defer/finish/consult_knowledge/join）。
func ValidPlanVerb(v PlanVerb) bool {
	switch v {
	case PlanVerbDispatch, PlanVerbDefer, PlanVerbFinish, PlanVerbConsultKnowledge, PlanVerbJoin:
		return true
	}
	return false
}

// PlanStepStatus 步骤执行状态；defer/finish 之后的余下步骤标记 skipped。
type PlanStepStatus string

const (
	PlanStepPending  PlanStepStatus = "pending"
	PlanStepExecuted PlanStepStatus = "executed"
	PlanStepSkipped  PlanStepStatus = "skipped"
	PlanStepFailed   PlanStepStatus = "failed"
)

// PlanStep 单个动作：payload 为提交时的 JSON 原文（不含 verb），
// result_* 记录 dispatch 的落库产物（哪个子任务、哪个 run）。
// 唯一增补：consult_knowledge 执行后写入 payload.results（检索结果，M2）。
type PlanStep struct {
	PlanID           string
	Seq              int
	Verb             PlanVerb
	Payload          map[string]any
	Status           PlanStepStatus
	ResultWorkItemID string
	ResultRunID      string
	Error            string
	CreatedAt        time.Time
	ExecutedAt       *time.Time
}

// PlanGuardrails M4 预算护栏（提交时固化进 plan，plans.guardrails JSON 列）：
// nil 字段表示未设限。max_dispatch 提交时校验（整单拒绝）；max_tokens 在
// 子任务静默唤醒点核算（主任务树全部 run 的 UsageIn+UsageOut 合计）。
type PlanGuardrails struct {
	MaxDispatch *int   `json:"max_dispatch,omitempty"`
	MaxTokens   *int64 `json:"max_tokens,omitempty"`
}

// PlanErrorBudgetExceeded 预算护栏收口的 plan 级错误码（plans.error 列）。
const PlanErrorBudgetExceeded = "budget_exceeded"

// Plan 一份由 lead agent（或用户经 API）提交的有序动作批次。
// 执行器确定性推进：同一 plan 提交永远产生同样效果，不依赖任何模型行为。
type Plan struct {
	ID             string
	WorkspaceID    string
	WorkItemID     string
	AgentProfileID string
	SourceRunID    string
	Status         PlanStatus
	SupersededBy   string
	Steps          []PlanStep
	// Guardrails 提交时固化的预算护栏（零值表示未设限）。
	Guardrails PlanGuardrails
	// Error plan 级失败原因码（budget_exceeded）；步骤级失败原因在 PlanStep.Error。
	Error     string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Transition 状态机校验迁移；终态不可逆。成功后 bump version/updated_at
// （与 WorkItem.Transition 同约定：调用方以迁移前版本做存储层乐观锁守卫）。
func (p *Plan) Transition(to PlanStatus, now time.Time) error {
	if p.Status.IsTerminal() {
		return &TransitionError{Entity: "plan", From: string(p.Status), To: string(to)}
	}
	if !p.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "plan", From: string(p.Status), To: string(to)}
	}
	p.Status = to
	p.Version++
	p.UpdatedAt = now
	return nil
}

// MarkWaiting defer/join 挂起或 manual dispatch 审批挂起（M4）：本批次到此为止，
// 等待唤醒（唤醒 ≠ 继续，而是 owner 提交新 plan；审批挂起则等审批回调续跑）。
func (p *Plan) MarkWaiting(now time.Time) error {
	return p.Transition(PlanWaiting, now)
}

// MarkActive 审批放行恢复（M4 审批护栏）：waiting → active 的唯一合法来源是
// plan_dispatch 审批 approved 后批次从挂起步骤续跑；defer/join 的静默唤醒
// 不回拨 active（owner 提交新 plan，旧 plan 走 supersede）。
func (p *Plan) MarkActive(now time.Time) error {
	return p.Transition(PlanActive, now)
}

// Finish 落终态；supersededBy 非空表示同主任务新 plan 提交触发的取代（仅 waiting 出发合法）。
func (p *Plan) Finish(now time.Time, supersededBy string) error {
	if err := p.Transition(PlanFinished, now); err != nil {
		return err
	}
	p.SupersededBy = supersededBy
	return nil
}

// CheckVersion 乐观锁校验（与 WorkItem 同约定：0 表示跳过）。
func (p *Plan) CheckVersion(expected int) error {
	if expected != 0 && expected != p.Version {
		return ErrVersionConflict
	}
	return nil
}

// Step 按 seq 查找步骤；不存在返回 nil。
func (p *Plan) Step(seq int) *PlanStep {
	for i := range p.Steps {
		if p.Steps[i].Seq == seq {
			return &p.Steps[i]
		}
	}
	return nil
}
