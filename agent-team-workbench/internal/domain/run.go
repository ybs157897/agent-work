package domain

import "time"

// RunStatus 状态机（协议文档 §4.3）。终态不可逆；重试总是创建新 Run。
type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunStarting        RunStatus = "starting"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunInterrupting    RunStatus = "interrupting"
	RunCancelling      RunStatus = "cancelling"
	RunReconnecting    RunStatus = "reconnecting"
	RunSucceeding      RunStatus = "succeeding"
	RunSucceeded       RunStatus = "succeeded"
	RunInterrupted     RunStatus = "interrupted"
	RunCancelled       RunStatus = "cancelled"
	RunLost            RunStatus = "lost"
	RunFailed          RunStatus = "failed"
)

var runTransitions = map[RunStatus][]RunStatus{
	// queued 未起跑：interrupt/cancel 落地直接终态（无 Adapter 需确认）。
	RunQueued: {RunStarting, RunCancelled, RunFailed, RunInterrupted},
	// starting 尚未产生外部副作用（与 queued 同理）：interrupt/cancel 可直达终态
	// 或经中间态；零事件空 turn（无任何回调）可从 starting 直入 succeeding。
	RunStarting:        {RunRunning, RunInterrupting, RunCancelling, RunInterrupted, RunCancelled, RunSucceeding, RunFailed},
	RunRunning:         {RunWaitingApproval, RunInterrupting, RunCancelling, RunReconnecting, RunSucceeding, RunFailed},
	RunWaitingApproval: {RunRunning, RunInterrupting, RunCancelling, RunFailed},
	RunInterrupting:    {RunInterrupted, RunFailed},
	RunCancelling:      {RunCancelled, RunFailed},
	// reconnecting：连接已失，无人能确认中间态——控制命令直达终态；
	// 重连失败本身也可能表现为 failed（不只有 lost）。
	RunReconnecting: {RunRunning, RunInterrupting, RunCancelling, RunInterrupted, RunCancelled, RunLost, RunFailed},
	// succeeding：终局已定但尚未落终态，控制命令可经中间态或直达终态
	//（与 ModuleRunner.recordTerminal 的补迁移配合，绝不卡死）。
	RunSucceeding: {RunSucceeded, RunInterrupting, RunCancelling, RunInterrupted, RunCancelled, RunFailed},
}

type RunFailure struct {
	Code      string
	Message   string
	Retryable bool
}

// ExecutionRun 是一次不可覆盖的执行尝试。
type ExecutionRun struct {
	ID                   string
	WorkspaceID          string
	WorkItemID           string
	AgentProfileID       string
	Status               RunStatus
	RuntimeLabel         string
	AdapterID            string
	Provider             string
	CapabilitySnapshotID string
	SessionRef           string // Adapter 私有句柄，受限存储
	// SessionBefore/After 审计：run 进入/离开时的会话句柄（task_sessions 决策依据）。
	SessionBefore string
	SessionAfter  string
	// Usage 本轮 token 用量（Adapter 上报）；UsageBasis 标注口径（per_run/session_cumulative）。
	UsageIn     int64
	UsageOut    int64
	UsageCached int64
	UsageBasis  string
	// ErrorFamily 跨 adapter 统一错误族，驱动重试与自愈策略。
	ErrorFamily string
	Progress    *float64
	RetryOf     string
	Failure     *RunFailure
	Input       map[string]any
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	FinishedAt  *time.Time
}

func (s RunStatus) IsTerminal() bool {
	switch s {
	case RunSucceeded, RunInterrupted, RunCancelled, RunLost, RunFailed:
		return true
	}
	return false
}

func (s RunStatus) CanTransitionTo(to RunStatus) bool {
	for _, ok := range runTransitions[s] {
		if ok == to {
			return true
		}
	}
	return false
}

// Transition 迁移 Run 状态；终态不可逆。
func (r *ExecutionRun) Transition(to RunStatus, now time.Time) error {
	if r.Status.IsTerminal() {
		return &TransitionError{Entity: "execution_run", From: string(r.Status), To: string(to)}
	}
	if !r.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "execution_run", From: string(r.Status), To: string(to)}
	}
	r.Status = to
	r.bump(now)
	if to.IsTerminal() {
		r.FinishedAt = &now
	}
	return nil
}

// MarkFailed 附加失败原因。权限/审批拒绝与 schema drift 等不可自动重试。
func (r *ExecutionRun) MarkFailed(f RunFailure, now time.Time) error {
	if err := r.Transition(RunFailed, now); err != nil {
		return err
	}
	r.Failure = &f
	return nil
}

func (r *ExecutionRun) SetProgress(p float64, now time.Time) {
	r.Progress = &p
	r.UpdatedAt = now
}

func (r *ExecutionRun) CheckVersion(expected int) error {
	if expected != 0 && expected != r.Version {
		return ErrVersionConflict
	}
	return nil
}

func (r *ExecutionRun) bump(now time.Time) {
	r.Version++
	r.UpdatedAt = now
}

// ApprovalStatus 审批状态；拒绝、过期不可自动重试。
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
	ApprovalExpired  ApprovalStatus = "expired"
)

// ApprovalKindPlanDispatch M4 审批护栏：plan dispatch 步骤的人工闸门。
// 与 run 内审批（工具调用等）不同，此类审批不挂 run（RunID 空，迁移 0010 放开
// approvals.run_id 非空约束）；RequestedBy={"kind":"plan","id":<planID>,"seq":<seq>}
// 定位挂起步骤，审批解决回调据此续跑或收口 plan。
const ApprovalKindPlanDispatch = "plan_dispatch"

// ApprovalRequest：UI 只展示最小必要摘要；敏感参数走 sensitive_input_ref。
type ApprovalRequest struct {
	ID                string
	RunID             string
	WorkItemID        string
	Kind              string
	Risk              string
	Status            ApprovalStatus
	Summary           string
	RequestedBy       map[string]any
	SensitiveInputRef string
	PolicySnapshotID  string
	ExpiresAt         *time.Time
	ResolvedAt        *time.Time
	ResolvedBy        string
	ResolveReason     string
	CreatedAt         time.Time
}

// Resolve 幂等决定：重复相同决定返回成功；冲突决定报错。
func (a *ApprovalRequest) Resolve(decision ApprovalStatus, by, reason string, now time.Time) error {
	if a.Status == decision {
		return nil
	}
	if a.Status != ApprovalPending {
		return &TransitionError{Entity: "approval", From: string(a.Status), To: string(decision)}
	}
	if decision != ApprovalApproved && decision != ApprovalRejected {
		return ErrValidation
	}
	a.Status = decision
	a.ResolvedAt = &now
	a.ResolvedBy = by
	a.ResolveReason = reason
	return nil
}

// ArtifactStatus：生成成功只是 draft；Review/Acceptance 后才 accepted。
type ArtifactStatus string

const (
	ArtifactDraft    ArtifactStatus = "draft"
	ArtifactAccepted ArtifactStatus = "accepted"
)

type Artifact struct {
	ID             string
	RunID          string
	LogicalPath    string
	Mime           string
	Size           int64
	Sha256         string
	Classification string
	Status         ArtifactStatus
	StorageRef     string
	CreatedAt      time.Time
}
