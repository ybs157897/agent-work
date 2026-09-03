package domain

import (
	"fmt"
	"time"
)

// TaskCoordinatorPromptVersion identifies the system prompt installed in the
// control plane. The prompt itself is owned by the engine; settings can select
// a runtime/model but cannot replace this version or its contents.
const TaskCoordinatorPromptVersion = "task-coordinator.v2"

// TaskCoordinatorRuntimeLabel is intentionally a small closed set. Runtime
// bindings still own adapter availability; these labels describe the only
// production coordinator backends plus the explicit test backend.
func ValidTaskCoordinatorRuntimeLabel(label string) bool {
	switch label {
	case "mock", "codex_local", "kimi_local":
		return true
	default:
		return false
	}
}

// TaskCoordinatorRuntimeMatchesAdapter is the fail-closed binding contract for
// the protected system Agent. A familiar label must never be used to smuggle a
// different adapter into the Coordinator execution path.
func TaskCoordinatorRuntimeMatchesAdapter(label, adapterID string) bool {
	switch label {
	case "mock":
		return adapterID == "mock"
	case "codex_local":
		return adapterID == "codex-appserver"
	case "kimi_local":
		return adapterID == "kimi-appserver" || adapterID == "kimi"
	default:
		return false
	}
}

func ValidTaskCoordinatorReasoningEffort(effort string) bool {
	switch effort {
	case "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}

// TaskCoordinatorConfig is the workspace-level configuration for the single
// protected coordinator profile. Runtime/model fields are mutable; the
// profile identity and PromptVersion are not.
type TaskCoordinatorConfig struct {
	ID                   string
	WorkspaceID          string
	AgentProfileID       string
	PromptVersion        string
	RuntimeLabel         string
	FallbackRuntimeLabel string
	ModelRef             ModelRef
	FallbackModelRef     ModelRef
	ReasoningEffort      string
	Version              int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// EffectiveReasoningEffort keeps callers from having to know that older
// persisted configs stored the effort in ModelRef only.
func (c *TaskCoordinatorConfig) EffectiveReasoningEffort() string {
	if c == nil {
		return ""
	}
	if c.ReasoningEffort != "" {
		return c.ReasoningEffort
	}
	return c.ModelRef.ReasoningEffort
}

// TaskCoordinatorStateStatus is the durable lifecycle of a task's root
// coordinator. The state is a read model/control checkpoint, not a Run state.
type TaskCoordinatorStateStatus string

const (
	CoordinatorQueued       TaskCoordinatorStateStatus = "queued"
	CoordinatorRunning      TaskCoordinatorStateStatus = "running"
	CoordinatorWaitingRetry TaskCoordinatorStateStatus = "waiting_retry"
	CoordinatorWaitingUser  TaskCoordinatorStateStatus = "waiting_user"
	CoordinatorBlocked      TaskCoordinatorStateStatus = "blocked"
	CoordinatorCompleted    TaskCoordinatorStateStatus = "completed"
	CoordinatorCancelled    TaskCoordinatorStateStatus = "cancelled"
)

func (s TaskCoordinatorStateStatus) Valid() bool {
	switch s {
	case CoordinatorQueued, CoordinatorRunning, CoordinatorWaitingRetry,
		CoordinatorWaitingUser, CoordinatorBlocked, CoordinatorCompleted,
		CoordinatorCancelled:
		return true
	default:
		return false
	}
}

func (s TaskCoordinatorStateStatus) IsDue() bool {
	return s == CoordinatorQueued || s == CoordinatorWaitingRetry || s == CoordinatorRunning
}

type CoordinatorRepairStatus string

const (
	CoordinatorRepairNone      CoordinatorRepairStatus = "none"
	CoordinatorRepairPending   CoordinatorRepairStatus = "pending"
	CoordinatorRepairExhausted CoordinatorRepairStatus = "exhausted"
)

type CoordinatorRepairErrorClass string

const (
	CoordinatorRepairErrorSyntax    CoordinatorRepairErrorClass = "syntax"
	CoordinatorRepairErrorSchema    CoordinatorRepairErrorClass = "schema"
	CoordinatorRepairErrorSemantic  CoordinatorRepairErrorClass = "semantic"
	CoordinatorRepairErrorAuthority CoordinatorRepairErrorClass = "authority"
	CoordinatorRepairErrorQuota     CoordinatorRepairErrorClass = "quota"
)

// TaskCoordinatorState is one durable control line per root Task. Child
// WorkItems do not get their own state; callers resolve them through
// GetStateForWorkItem.
type TaskCoordinatorState struct {
	ID                 string
	WorkspaceID        string
	RootWorkItemID     string
	CoordinatorAgentID string
	Status             TaskCoordinatorStateStatus
	Phase              string
	Summary            string
	CurrentAction      string
	CurrentStep        string
	CurrentAgentID     string
	CurrentRunID       string
	Attempt            int
	NextActionAt       *time.Time
	BlockerCode        string
	BlockerMessage     string
	LastError          string
	Data               map[string]any
	// ConsumedCommentRevision 评论消费水位（RFC §4.9/§7.8）：revision ≤ 该值
	// 的评论已被某个持久 Coordinator Run 的输入快照收录；与 Run 创建同事务推进，
	// 不表示模型已正确执行。
	ConsumedCommentRevision int64
	RepairStatus            CoordinatorRepairStatus
	RepairAttempt           int
	RepairSourceRunID       string
	RepairErrorClass        CoordinatorRepairErrorClass
	RepairErrorCode         string
	RepairValidationErrors  []GovernanceValidationError
	Version                 int
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func (s *TaskCoordinatorState) ValidateRepair() error {
	if s == nil {
		return fmt.Errorf("%w: coordinator state required", ErrValidation)
	}
	if len(s.RepairValidationErrors) > 128 {
		return fmt.Errorf("%w: repair validation errors exceed 128", ErrValidation)
	}
	for i := range s.RepairValidationErrors {
		if err := s.RepairValidationErrors[i].Validate(); err != nil {
			return fmt.Errorf("%w: repair validation_errors[%d]: %v", ErrValidation, i, err)
		}
	}
	switch s.RepairStatus {
	case "", CoordinatorRepairNone:
		if s.RepairAttempt != 0 || s.RepairSourceRunID != "" || s.RepairErrorClass != "" ||
			s.RepairErrorCode != "" || len(s.RepairValidationErrors) != 0 {
			return fmt.Errorf("%w: empty repair state must not retain a checkpoint", ErrValidation)
		}
		s.RepairStatus = CoordinatorRepairNone
		s.RepairValidationErrors = []GovernanceValidationError{}
		return nil
	case CoordinatorRepairPending:
		if s.RepairAttempt < 1 || s.RepairAttempt > 2 || s.RepairSourceRunID == "" ||
			(s.RepairErrorClass != CoordinatorRepairErrorSyntax && s.RepairErrorClass != CoordinatorRepairErrorSchema) ||
			s.RepairErrorCode == "" {
			return fmt.Errorf("%w: invalid pending repair checkpoint", ErrValidation)
		}
	case CoordinatorRepairExhausted:
		if s.RepairAttempt != 2 || s.RepairSourceRunID == "" ||
			(s.RepairErrorClass != CoordinatorRepairErrorSyntax && s.RepairErrorClass != CoordinatorRepairErrorSchema) ||
			s.RepairErrorCode == "" {
			return fmt.Errorf("%w: invalid exhausted repair checkpoint", ErrValidation)
		}
		if s.Status != CoordinatorBlocked || s.CurrentRunID != "" || s.NextActionAt != nil {
			return fmt.Errorf("%w: exhausted repair checkpoint must be blocked without an active Run", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: invalid repair status %q", ErrValidation, s.RepairStatus)
	}
	if err := validateTypedID("repair_source_run_id", s.RepairSourceRunID, PrefixRun); err != nil {
		return err
	}
	if err := validateText("repair_error_code", s.RepairErrorCode, 128); err != nil {
		return err
	}
	return nil
}

func (s *TaskCoordinatorState) ClearRepair() {
	s.RepairStatus = CoordinatorRepairNone
	s.RepairAttempt = 0
	s.RepairSourceRunID = ""
	s.RepairErrorClass = ""
	s.RepairErrorCode = ""
	s.RepairValidationErrors = nil
}

// TaskCoordinatorEvent is append-only causal history for the task read model.
// WorkItemID can identify a dispatched child while RootWorkItemID keeps every
// event in the root task timeline.
type TaskCoordinatorEvent struct {
	ID             string
	WorkspaceID    string
	RootWorkItemID string
	WorkItemID     string
	Kind           string
	Summary        string
	RunID          string
	AgentID        string
	Attempt        int
	Reason         string
	NextActionAt   *time.Time
	Data           map[string]any
	OccurredAt     time.Time
}
