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
	RunQueued:          {RunStarting, RunCancelled, RunFailed},
	RunStarting:        {RunRunning, RunFailed, RunCancelled},
	RunRunning:         {RunWaitingApproval, RunInterrupting, RunCancelling, RunReconnecting, RunSucceeding, RunFailed},
	RunWaitingApproval: {RunRunning, RunInterrupting, RunCancelling, RunFailed},
	RunInterrupting:    {RunInterrupted, RunFailed},
	RunCancelling:      {RunCancelled, RunFailed},
	RunReconnecting:    {RunRunning, RunLost},
	RunSucceeding:      {RunSucceeded, RunFailed},
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
	Progress             *float64
	RetryOf              string
	Failure              *RunFailure
	Input                map[string]any
	Version              int
	CreatedAt            time.Time
	UpdatedAt            time.Time
	FinishedAt           *time.Time
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
