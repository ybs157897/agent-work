package domain

import "time"

// WorkItemRecordKind 区分工作台中的独立对话记录与任务发布记录。
// 这是持久化边界，不是 UI 展示模式：同一记录创建后不可在两类之间切换。
type WorkItemRecordKind string

const (
	// RecordKindChat 是单 Agent 多轮对话记录；只共享 Run/Session 执行基础。
	RecordKindChat WorkItemRecordKind = "chat"
	// RecordKindTask 是任务发布记录；启用计划、派发、台账与任务状态机。
	RecordKindTask WorkItemRecordKind = "task"
)

// Valid 报告记录类型是否在持久化闭集内。
func (k WorkItemRecordKind) Valid() bool {
	return k == RecordKindChat || k == RecordKindTask
}

// IsTask 报告该记录是否属于任务发布域。
func (k WorkItemRecordKind) IsTask() bool { return k == RecordKindTask }

// IsChat 报告该记录是否属于独立对话域。
func (k WorkItemRecordKind) IsChat() bool { return k == RecordKindChat }

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
//
// LockedByRunID/LockedAt 是任务执行锁（F1）：非空表示任务正被该 run 执行
// （防同一任务双跑）。锁归属 run 而非 agent——属主活性复用 run 状态/lease
// 判定面，不引入第二套判定；属主 run 落终态即死锁可抢占。锁是并发原语，
// 不参与 version 乐观锁比较，但读写必须与状态变更同一事务。
//
// RollingDigest 是任务台账滚动摘要（会话元模型 S2）：确定性生成（无 LLM），
// 终态钩子全量重算覆盖写；转述只允许进这里，决策原话走 decision_entries。
type WorkItem struct {
	ID          string
	WorkspaceID string
	// RecordKind 是不可变的 Chat/Task 记录边界；空值仅兼容迁移前的内存对象，
	// 应用层读取时按 task 解释，新建对象必须显式归一化。
	RecordKind     WorkItemRecordKind
	ParentID       string
	Title          string
	Description    string
	Status         WorkItemStatus
	Phase          WorkItemPhase
	Priority       Priority
	DueDate        *time.Time
	AgentProfileID string
	ClientKey      string
	LockedByRunID  string
	LockedAt       *time.Time
	RollingDigest  string
	// AcceptanceCriteria 验收读模型（0022）：元素为验收条目原话；非任务或未
	// 设置为 nil。PhaseEnteredAt 进入当前 phase 的精确时间（review/acceptance
	// 投影用；迁移前的历史行回填为 updated_at）。
	AcceptanceCriteria []string
	PhaseEnteredAt     *time.Time
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// HoldsLock 报告 runID 是否持有本任务的执行锁（空 runID 恒 false）。
func (w *WorkItem) HoldsLock(runID string) bool {
	return runID != "" && w.LockedByRunID == runID
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

// Transition 执行状态迁移；终态不可逆。离开 in_progress 时 phase 与
// phase_entered_at 一并清理（RFC §4.10：pending_since 只属于 in_progress 投影）；
// 进入 in_progress 落到 execution 并写精确进入时间。
func (w *WorkItem) Transition(to WorkItemStatus, now time.Time) error {
	if w.RecordKind != "" && w.RecordKind != RecordKindTask {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: string(to)}
	}
	if w.Status.IsTerminal() {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: string(to)}
	}
	if !w.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: string(to)}
	}
	w.Status = to
	if to != WorkItemInProgress {
		w.Phase = ""
		w.PhaseEnteredAt = nil
	} else {
		w.Phase = PhaseExecution
		w.PhaseEnteredAt = &now
	}
	w.bump(now)
	return nil
}

// EnterReview：Run succeeded 后进入评审投影；WorkItem 仍处 in_progress。
// phase_entered_at 每次进入都取精确时间（review→execution→review 得到新时间）。
func (w *WorkItem) EnterReview(now time.Time) error {
	if w.RecordKind != "" && w.RecordKind != RecordKindTask {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "review"}
	}
	if w.Status != WorkItemInProgress {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "review"}
	}
	w.Phase = PhaseReview
	w.PhaseEnteredAt = &now
	w.bump(now)
	return nil
}

// EnterAcceptance：评估 verdict pass 后进入待验收投影（M2 评估链路）。
// 仅 review 可入（评估 run succeeded 先经 EnterReview 既有联动）；WorkItem 仍处
// in_progress，唯一完工路径仍是 Accept()。
func (w *WorkItem) EnterAcceptance(now time.Time) error {
	if w.RecordKind != "" && w.RecordKind != RecordKindTask {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "acceptance"}
	}
	if w.Status != WorkItemInProgress || w.Phase != PhaseReview {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "acceptance"}
	}
	w.Phase = PhaseAcceptance
	w.PhaseEnteredAt = &now
	w.bump(now)
	return nil
}

// BeginExecution 在同一 WorkItem/会话创建下一轮 Run 时，把评审投影切回执行态。
// WorkItem 仍保持 in_progress；每一轮 Run 仍是不可覆盖的独立审计记录。
// phase_entered_at 只在真实离开 review/acceptance 时刷新；已在 execution 时是
// no-op（同一执行段不重置 pending_since）。
func (w *WorkItem) BeginExecution(now time.Time) {
	if w.RecordKind != "" && w.RecordKind != RecordKindTask {
		return
	}
	if w.Status != WorkItemInProgress || w.Phase == PhaseExecution {
		return
	}
	w.Phase = PhaseExecution
	w.PhaseEnteredAt = &now
	w.bump(now)
}

// Accept：Reviewer / 人工验收通过，唯一进入 completed 的路径。
// 离开 in_progress 投影：phase 与 phase_entered_at 一并清理。
func (w *WorkItem) Accept(now time.Time) error {
	if w.RecordKind != "" && w.RecordKind != RecordKindTask {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: string(WorkItemCompleted)}
	}
	if w.Status != WorkItemInProgress || w.Phase == "" || w.Phase == PhaseExecution {
		return &TransitionError{Entity: "work_item", From: string(w.Status), To: "completed"}
	}
	w.Status = WorkItemCompleted
	w.Phase = ""
	w.PhaseEnteredAt = nil
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
