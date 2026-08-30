package domain

import "time"

// 事件名白名单（contracts/events/asyncapi.yaml）；SSE 只允许这些 event name。
const (
	EventDashboardMetricsUpdated = "dashboard.metrics.updated"
	EventActivityCreated         = "activity.appended"
	EventSystemHealthChanged     = "system.health_changed"

	EventAgentProfileCreated      = "agent_profile.created"
	EventAgentProfileUpdated      = "agent_profile.updated"
	EventAgentAvailabilityChanged = "agent_availability.changed"
	EventAgentPresenceUpdated     = "agent_presence.updated"

	EventWorkItemCreated   = "work_item.created"
	EventWorkItemUpdated   = "work_item.updated"
	EventWorkItemMoved     = "work_item.moved"
	EventWorkItemAssigned  = "work_item.assigned"
	EventWorkItemBlocked   = "work_item.blocked"
	EventWorkItemUnblocked = "work_item.unblocked"
	EventWorkItemCompleted = "work_item.completed"

	// EventWorkItemLocked / EventWorkItemLockPreempted 任务执行锁（F1）：
	// run 进 running 获取锁 / 死属主（终态 run）锁被抢占；载荷带 run_id。
	EventWorkItemLocked        = "work_item.locked"
	EventWorkItemLockPreempted = "work_item.lock_preempted"

	// EventDispatchCreated / EventDispatchUpdated 派发批次（会话元模型 S1）：
	// 批次创建与行变更。S1 生产者只有 created；updated 的发布点随 S3 状态收口
	//（running→collecting→终态）接入。
	EventDispatchCreated = "dispatch.created"
	EventDispatchUpdated = "dispatch.updated"

	// EventDecisionCreated 决策台账写入（会话元模型 S2）：用户原话钉为决策时
	// 发布；载荷带 work_item_id/quote，前端台账区据此增量刷新。
	EventDecisionCreated = "decision.created"

	// Task Coordinator 是根 Task 的系统控制线；事件既驱动任务追踪 read model，
	// 也为重启恢复与失败重试保留可审计因果链。
	EventCoordinatorQueued          = "coordinator.queued"
	EventCoordinatorStarted         = "coordinator.started"
	EventCoordinatorMessageReceived = "coordinator.message_received"
	EventCoordinatorPlanUpdated     = "coordinator.plan_updated"
	EventCoordinatorAttemptUpdated  = "coordinator.attempt_updated"
	EventCoordinatorRetryScheduled  = "coordinator.retry_scheduled"
	EventCoordinatorRecoveryStarted = "coordinator.recovery_started"
	EventCoordinatorBlocked         = "coordinator.blocked"
	EventCoordinatorCompleted       = "coordinator.completed"
	EventCoordinatorStateChanged    = "coordinator.state_changed"

	EventRunCreated         = "run.created"
	EventRunStarted         = "run.started"
	EventRunStatusChanged   = "run.status_changed"
	EventRunProgressUpdated = "run.progress_updated"
	EventRunPlanUpdated     = "run.plan_updated"
	EventRunCompleted       = "run.completed"
	EventRunFailed          = "run.failed"
	EventRunCancelled       = "run.cancelled"
	EventRunLost            = "run.lost"

	// EventSessionDecision CreateRun 会话决议（resume/rotation/inline）的观测事件；
	// 纯审计面，不驱动任何状态。
	EventSessionDecision = "session.decision"

	// EventSessionCompacted adapter 侧上下文压缩事实（如 codex contextCompaction）；
	// 观测面，data 允许空对象或带 turnId。
	EventSessionCompacted = "session.compacted"

	EventPlanSubmitted    = "plan.submitted"
	EventPlanStepExecuted = "plan.step_executed"
	EventPlanWaiting      = "plan.waiting"
	EventPlanFinished     = "plan.finished"
	EventPlanFailed       = "plan.failed"

	EventMessageDelta     = "message.delta"
	EventMessageCompleted = "message.completed"

	EventToolStarted         = "tool.started"
	EventToolProgress        = "tool.progress"
	EventToolCompleted       = "tool.completed"
	EventToolFailed          = "tool.failed"
	EventSubagentUpdated     = "subagent.updated"
	EventFileChangesReverted = "file_changes.reverted"

	EventApprovalRequested = "approval.requested"
	EventApprovalResolved  = "approval.resolved"
	EventApprovalExpired   = "approval.expired"

	EventArtifactCreated = "artifact.created"
	EventArtifactUpdated = "artifact.updated"
	EventUsageUpdated    = "usage.updated"

	EventRuntimeHealthChanged = "runtime.health_changed"
	EventRunnerConnected      = "runner.connected"
	EventRunnerDisconnected   = "runner.disconnected"
	EventRunRecoveryStarted   = "run.recovery_started"
	EventRunRecoveryCompleted = "run.recovery_completed"
	EventRunRecoveryFailed    = "run.recovery_failed"
)

// eventNameWhitelist 用于发布前校验；未知事件名禁止进入 SSE。
var eventNameWhitelist = map[string]struct{}{
	EventDashboardMetricsUpdated: {}, EventActivityCreated: {}, EventSystemHealthChanged: {},
	EventAgentProfileCreated: {}, EventAgentProfileUpdated: {},
	EventAgentAvailabilityChanged: {}, EventAgentPresenceUpdated: {},
	EventWorkItemCreated: {}, EventWorkItemUpdated: {}, EventWorkItemMoved: {},
	EventWorkItemAssigned: {}, EventWorkItemBlocked: {}, EventWorkItemUnblocked: {},
	EventWorkItemCompleted: {}, EventWorkItemLocked: {}, EventWorkItemLockPreempted: {},
	EventDispatchCreated: {}, EventDispatchUpdated: {}, EventDecisionCreated: {},
	EventCoordinatorQueued: {}, EventCoordinatorStarted: {},
	EventCoordinatorMessageReceived: {}, EventCoordinatorPlanUpdated: {},
	EventCoordinatorAttemptUpdated: {}, EventCoordinatorRetryScheduled: {},
	EventCoordinatorRecoveryStarted: {}, EventCoordinatorBlocked: {},
	EventCoordinatorCompleted: {}, EventCoordinatorStateChanged: {},
	EventRunCreated: {}, EventRunStarted: {}, EventRunStatusChanged: {},
	EventRunProgressUpdated: {}, EventRunPlanUpdated: {}, EventRunCompleted: {},
	EventRunFailed: {}, EventRunCancelled: {}, EventRunLost: {}, EventSessionDecision: {},
	EventSessionCompacted: {},
	EventPlanSubmitted:    {}, EventPlanStepExecuted: {}, EventPlanWaiting: {}, EventPlanFinished: {},
	EventPlanFailed:   {},
	EventMessageDelta: {}, EventMessageCompleted: {},
	EventToolStarted: {}, EventToolProgress: {}, EventToolCompleted: {}, EventToolFailed: {},
	EventSubagentUpdated:     {},
	EventFileChangesReverted: {},
	EventApprovalRequested:   {}, EventApprovalResolved: {}, EventApprovalExpired: {},
	EventArtifactCreated: {}, EventArtifactUpdated: {}, EventUsageUpdated: {},
	EventRuntimeHealthChanged: {}, EventRunnerConnected: {}, EventRunnerDisconnected: {},
	EventRunRecoveryStarted: {}, EventRunRecoveryCompleted: {}, EventRunRecoveryFailed: {},
}

// IsKnownEventName 校验事件名是否在白名单内。
func IsKnownEventName(name string) bool {
	_, ok := eventNameWhitelist[name]
	return ok
}

// 聚合类型。
const (
	AggregateWorkspace       = "workspace"
	AggregateAgentProfile    = "agent_profile"
	AggregateWorkItem        = "work_item"
	AggregatePlan            = "plan"
	AggregateExecutionRun    = "execution_run"
	AggregateApproval        = "approval"
	AggregateArtifact        = "artifact"
	AggregateRuntimeBinding  = "runtime_binding"
	AggregateRunner          = "runner"
	AggregateDispatch        = "dispatch"
	AggregateDecision        = "decision"
	AggregateTaskCoordinator = "task_coordinator"
)

// CanonicalEvent 是工作台投影事实源（asyncapi CanonicalEventEnvelope）。
// stream_seq 由控制平面在提交事务时分配。
type CanonicalEvent struct {
	ContractVersion string         `json:"contract_version"` // 固定 events/v1
	EventID         string         `json:"event_id"`
	WorkspaceID     string         `json:"workspace_id"`
	StreamSeq       int64          `json:"stream_seq"`
	AggregateType   string         `json:"-"`
	AggregateID     string         `json:"-"`
	Aggregate       AggregateRef   `json:"aggregate"`
	RunSeq          int64          `json:"run_seq,omitempty"`
	AgentID         string         `json:"agent_id,omitempty"`
	Type            string         `json:"type"`
	OccurredAt      time.Time      `json:"occurred_at"`
	Actor           *EventActor    `json:"actor,omitempty"`
	CorrelationID   string         `json:"correlation_id,omitempty"`
	Data            map[string]any `json:"data,omitempty"`
}

type AggregateRef struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type EventActor struct {
	Kind string `json:"kind"` // user / runtime / system
	ID   string `json:"id"`
}

// NewCanonicalEvent 构造白名单内事件；未知事件名直接失败。
func NewCanonicalEvent(workspaceID, eventType, aggType, aggID string, aggVersion int, data map[string]any) (*CanonicalEvent, error) {
	if !IsKnownEventName(eventType) {
		return nil, &TransitionError{Entity: "event", From: eventType, To: "whitelist"}
	}
	return &CanonicalEvent{
		ContractVersion: "events/v1",
		EventID:         NewID(PrefixEvent),
		WorkspaceID:     workspaceID,
		AggregateType:   aggType,
		AggregateID:     aggID,
		Aggregate:       AggregateRef{Type: aggType, ID: aggID, Version: aggVersion},
		Type:            eventType,
		OccurredAt:      time.Now().UTC(),
		Data:            data,
	}, nil
}
