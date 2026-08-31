package domain

import "time"

// TaskCoordinatorPromptVersion identifies the system prompt installed in the
// control plane. The prompt itself is owned by the engine; settings can select
// a runtime/model but cannot replace this version or its contents.
const TaskCoordinatorPromptVersion = "task-coordinator.v1"

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
	Version                 int
	CreatedAt               time.Time
	UpdatedAt               time.Time
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
