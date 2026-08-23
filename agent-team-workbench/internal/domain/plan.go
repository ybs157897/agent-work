package domain

import "time"

// PlanStatus 状态机（M1 编排设计 note §生命周期）：
//
//	active ──所有 step 执行完──▶ finished
//	active ──遇到 defer────────▶ waiting ──同主任务新 plan 提交──▶ finished（superseded_by 记新 plan）
//	active/waiting ──用户取消──▶ cancelled        任一 step 失败 ──▶ failed
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
	PlanWaiting: {PlanFinished, PlanCancelled, PlanFailed},
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

// PlanVerb M1 词汇表子集；use_session 是默认行为、consult_knowledge/join 归后续里程碑，
// 未知 verb 由提交校验拒绝（不进状态机）。
type PlanVerb string

const (
	PlanVerbDispatch PlanVerb = "dispatch"
	PlanVerbDefer    PlanVerb = "defer"
	PlanVerbFinish   PlanVerb = "finish"
)

// ValidPlanVerb 报告 v 是否为 M1 支持的动作词。
func ValidPlanVerb(v PlanVerb) bool {
	switch v {
	case PlanVerbDispatch, PlanVerbDefer, PlanVerbFinish:
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
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Transition 状态机校验迁移；终态不可逆。version/updated_at 由调用方在迁移成功后推进。
func (p *Plan) Transition(to PlanStatus, now time.Time) error {
	if p.Status.IsTerminal() {
		return &TransitionError{Entity: "plan", From: string(p.Status), To: string(to)}
	}
	if !p.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "plan", From: string(p.Status), To: string(to)}
	}
	p.Status = to
	p.UpdatedAt = now
	return nil
}

// MarkWaiting defer 挂起：本批次到此为止，等待唤醒（唤醒 ≠ 继续，而是 owner 提交新 plan）。
func (p *Plan) MarkWaiting(now time.Time) error {
	return p.Transition(PlanWaiting, now)
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
