package domain

import "time"

// DispatchTrigger 派发批次成因（dispatches.trigger 闭集）。
type DispatchTrigger string

const (
	// DispatchTriggerUserMessage 用户消息入口：一次发送 = 一个批次。接诊批次
	// lead_run_id 记接诊 run；@直达批次 lead_run_id 为空。
	DispatchTriggerUserMessage DispatchTrigger = "user_message"
	// DispatchTriggerLeadPlan plan 执行器 dispatch verb 派生的批次：source run
	// 有 dispatch 时子 run 直接继承父批次，无 dispatch（API 手动提交 plan、
	// 存量 run）才落 lead_plan 兜底批次。
	DispatchTriggerLeadPlan DispatchTrigger = "lead_plan"
	// DispatchTriggerWakeup 唤醒消费产生的批次（S3 worker→lead 回流汇总走这里）。
	DispatchTriggerWakeup DispatchTrigger = "wakeup"
)

// DispatchStatus 派发批次状态机。批次自身不推进，跟随成员 run 收口（S3）：
// running --(成员全终态)--> collecting --(lead 汇总终态)--> completed/degraded；
// 任一成员 failed/cancelled/lost/interrupted → degraded；用户喊停 → cancelled。
type DispatchStatus string

const (
	DispatchRunning    DispatchStatus = "running"
	DispatchCollecting DispatchStatus = "collecting"
	DispatchCompleted  DispatchStatus = "completed"
	DispatchDegraded   DispatchStatus = "degraded"
	DispatchCancelled  DispatchStatus = "cancelled"
)

var dispatchTransitions = map[DispatchStatus][]DispatchStatus{
	// running：成员收口中。@直达批次（无唤醒）可跳过 collecting 直达 completed/
	// degraded——无 lead 汇总环节。
	DispatchRunning: {DispatchCollecting, DispatchCompleted, DispatchDegraded, DispatchCancelled},
	// collecting：等待 lead 汇总 run 终态；汇总即收口（防循环，不再入 collecting）。
	DispatchCollecting: {DispatchCompleted, DispatchDegraded, DispatchCancelled},
}

func (s DispatchStatus) IsTerminal() bool {
	switch s {
	case DispatchCompleted, DispatchDegraded, DispatchCancelled:
		return true
	}
	return false
}

func (s DispatchStatus) CanTransitionTo(to DispatchStatus) bool {
	for _, ok := range dispatchTransitions[s] {
		if ok == to {
			return true
		}
	}
	return false
}

// Dispatch 派发批次：用户一次发送形成的执行批次，会话组（组内 run）的关联键。
// 组的存在靠 execution_runs.dispatch_id 外键，不靠树遍历。
type Dispatch struct {
	ID         string
	WorkItemID string
	Trigger    DispatchTrigger
	// LeadRunID 接诊 run（user_message 未 @ 指名）或 plan source run（lead_plan）；
	// @直达批次为空。
	LeadRunID string
	Status    DispatchStatus
	CreatedAt time.Time
	ClosedAt  *time.Time
}

// Transition 迁移批次状态；终态不可逆。
func (d *Dispatch) Transition(to DispatchStatus, now time.Time) error {
	if d.Status.IsTerminal() || !d.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "dispatch", From: string(d.Status), To: string(to)}
	}
	d.Status = to
	if to.IsTerminal() {
		d.ClosedAt = &now
	}
	return nil
}
