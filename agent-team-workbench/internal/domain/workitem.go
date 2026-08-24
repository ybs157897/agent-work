package domain

import "time"

// WorkItemStatus 是看板业务真相；只由控制平面状态机变更。
type WorkItemStatus string

const (
	WorkItemTodo       WorkItemStatus = "todo"
	WorkItemInProgress WorkItemStatus = "in_progress"
	WorkItemBlocked    WorkItemStatus = "blocked"
	WorkItemCompleted  WorkItemStatus = "completed"
	WorkItemCancelled  WorkItemStatus = "cancelled"
)

// WorkItemPhase：首版保留四列，评审态以 phase 投影（协议文档 §4.2）。
type WorkItemPhase string

const (
	PhaseExecution  WorkItemPhase = "execution"
	PhaseReview     WorkItemPhase = "review"
	PhaseAcceptance WorkItemPhase = "acceptance"
)

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// workItemTransitions 定义合法状态迁移；completed/cancelled 为终态。
// 纠正通过新的命令历史表达，不回写终态。todo→blocked 为 M4 预算护栏预留：
// 静默唤醒点核算超限时主任务可能尚未认领（todo），blocker 仍需人可见。
var workItemTransitions = map[WorkItemStatus][]WorkItemStatus{
	WorkItemTodo:       {WorkItemInProgress, WorkItemBlocked, WorkItemCancelled},
	WorkItemInProgress: {WorkItemBlocked, WorkItemCompleted, WorkItemCancelled},
	WorkItemBlocked:    {WorkItemInProgress, WorkItemCancelled},
}

// WorkItem 看板任务。version 为乐观锁，命令需带 expected_version。
// ParentID 非空表示本任务是 plan dispatch 派生的子任务（树以主任务为根）。
// ClientKey 非空时是客户端业务意图去重键（workspace 内唯一）：同一意图
// （如队列 drain 重试、分叉双击）重复创建返回既有实体而非重复建行。
type WorkItem struct {
	ID             string
	WorkspaceID    string
	ParentID       string
	Title          string
	Description    string
	Status         WorkItemStatus
	Phase          WorkItemPhase
	Priority       Priority
	DueDate        *time.Time
	AgentProfileID string
	ClientKey      string
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Blocker 结构化阻塞原因（原型 blockedReason 的 canonical 化）。
type Blocker struct {
	ID         string
	WorkItemID string
	Code       string
	Message    string
	Source     string
	CreatedAt  time.Time
	ResolvedAt *time.Time
}

// CanTransitionTo 校验状态机。
func (s WorkItemStatus) CanTransitionTo(to WorkItemStatus) bool {
	for _, ok := range workItemTransitions[s] {
		if ok == to {
			return true
		}
	}
	return false
}

func (s WorkItemStatus) IsTerminal() bool {
	return s == WorkItemCompleted || s == WorkItemCancelled
}

// Transition 执行状态迁移；终态不可逆。
func (w *WorkItem) Transition(to WorkItemStatus, now time.Time) error {
	if w.Status.IsTerminal() {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: string(to)}
	}
	if !w.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: string(to)}
	}
	w.Status = to
	if to != WorkItemInProgress {
		w.Phase = ""
	} else if w.Phase == "" {
		w.Phase = PhaseExecution
	}
	w.bump(now)
	return nil
}

// EnterReview：Run succeeded 后进入评审投影；WorkItem 仍处 in_progress。
func (w *WorkItem) EnterReview(now time.Time) error {
	if w.Status != WorkItemInProgress {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "review"}
	}
	w.Phase = PhaseReview
	w.bump(now)
	return nil
}

// EnterAcceptance：评估 verdict pass 后进入待验收投影（M2 评估链路）。
// 仅 review 可入（评估 run succeeded 先经 EnterReview 既有联动）；WorkItem 仍处
// in_progress，唯一完工路径仍是 Accept()。
func (w *WorkItem) EnterAcceptance(now time.Time) error {
	if w.Status != WorkItemInProgress || w.Phase != PhaseReview {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "acceptance"}
	}
	w.Phase = PhaseAcceptance
	w.bump(now)
	return nil
}

// BeginExecution 在同一 WorkItem/会话创建下一轮 Run 时，把评审投影切回执行态。
// WorkItem 仍保持 in_progress；每一轮 Run 仍是不可覆盖的独立审计记录。
func (w *WorkItem) BeginExecution(now time.Time) {
	if w.Status != WorkItemInProgress || w.Phase == PhaseExecution {
		return
	}
	w.Phase = PhaseExecution
	w.bump(now)
}

// Accept：Reviewer / 人工验收通过，唯一进入 completed 的路径。
func (w *WorkItem) Accept(now time.Time) error {
	if w.Status != WorkItemInProgress || w.Phase == "" || w.Phase == PhaseExecution {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "completed"}
	}
	w.Status = WorkItemCompleted
	w.Phase = ""
	w.bump(now)
	return nil
}

// CheckVersion 乐观锁校验：更新 0 行语义由存储层表达，领域层先做前置校验。
func (w *WorkItem) CheckVersion(expected int) error {
	if expected != 0 && expected != w.Version {
		return ErrVersionConflict
	}
	return nil
}

func (w *WorkItem) bump(now time.Time) {
	w.Version++
	w.UpdatedAt = now
}
