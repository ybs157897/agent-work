// Package application 承载用例与事务边界（控制平面模块）。
// 仓储接口在此定义、由 persistence/sqlstore 的 SQLite 实现，
// 保证领域逻辑不依赖存储细节。
package application

import (
	"context"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// Store 是所有仓储的门面；InTx 内的 ctx 自动切换到同一事务。
type Store interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	Workspaces() WorkspaceRepo
	Agents() AgentRepo
	AgentConfigSyncIntents() AgentConfigSyncIntentRepo
	WorkItems() WorkItemRepo
	Plans() PlanRepo
	Runs() RunRepo
	Events() EventRepo
	Idempotency() IdempotencyRepo
	Bindings() RuntimeBindingRepo
	Runners() RunnerRepo
	Audit() AuditRepo
	Caps() CapabilitySnapshotRepo
	TaskSessions() TaskSessionRepo
	ApprovalGrants() ApprovalGrantRepo
	// Dispatches 派发批次仓储（会话元模型 S1）。
	Dispatches() DispatchRepo
	// DecisionEntries 决策台账仓储（会话元模型 S2）。
	DecisionEntries() DecisionRepo
	// Search FTS 检索索引仓储（会话元模型 S4）。
	Search() SearchRepo
	// Wakeups M4 唤醒调度端口：入队/查询/心跳/活跃 run（接口定义见 scheduling.Store，
	// 该包只依赖 domain，充当双方共享的端口描述）。
	Wakeups() scheduling.Store
	// TaskCoordinators 持久化系统级 Task Coordinator 配置、根任务控制线与追加事件。
	TaskCoordinators() TaskCoordinatorRepo
	// Goals 持久化跨 bounded turn 的长期治理意图（不拥有 Run/Lease）。
	Goals() GoalRepo
	// Todos 持久化 Goal 的 bounded intent 与治理 claim（不拥有 Run/Lease）。
	Todos() TodoRepo
	// TurnReceipts 持久化 admission header 与 append-only settlement phases。
	TurnReceipts() TurnReceiptRepo
	// Quotas 持久化 admitted Turn 的冻结预算与 per-Run spend ledger。
	Quotas() QuotaRepo
	// Handoffs 持久化 Goal/Todo 治理所有权交接记录；不拥有 Runner lease。
	Handoffs() HandoffRepo
	// GovernanceProjections 持久化可重建 Goal read model 与 repair checkpoint。
	GovernanceProjections() GovernanceProjectionRepo
	// ValidationResults 持久化由控制面/runtime 证明的 validation result；不接受模型文本冒充。
	ValidationResults() ValidationResultRepo
	// DeliveryBriefSnapshots 持久化确定性 Delivery Brief 的 immutable evidence
	// capture；它不拥有任何 Goal/Todo/WorkItem 状态。
	DeliveryBriefSnapshots() DeliveryBriefSnapshotRepo
	// QuotaGapResolutions 持久化对既有 unresolved spend 的 immutable 人工对账
	// 裁决；不修改原 spend/canonical usage/reservation。
	QuotaGapResolutions() QuotaGapResolutionRepo
	// ── Execution Context（任务控制面 RFC §4；接口由主智能体冻结，W2-CORE 实现）──
	ExecutionHosts() ExecutionHostRepo
	WorkspaceLocations() WorkspaceLocationRepo
	WorkItemContexts() WorkItemContextRepo
	ContextSnapshots() ContextSnapshotRepo
	// TaskComments append-only 任务反馈流（RFC §4.9；W3-CMT 实现）。
	TaskComments() TaskCommentRepo
}

// TaskCommentRepo 任务评论与根级 revision cursor 存储。
// 全部写点必须在调用方事务内；cursor 行与根 Coordinator state 同事务创建、永不物理删除。
type TaskCommentRepo interface {
	// EnsureCursor 幂等创建 latest_revision=0 的 cursor 行（与根 Coordinator state 同事务）。
	EnsureCursor(ctx context.Context, rootWorkItemID string) error
	// Append 锁定 cursor 行分配单调 revision 并插入 comment（禁止 MAX(revision)+1）；
	// 返回带 revision 的实体。client_key 非空时撞键（同 root 同 key）：
	// body 一致返回既有行（幂等重放），body 不同返回 ErrIdempotencyConflict。
	Append(ctx context.Context, c *domain.TaskComment) (*domain.TaskComment, error)
	// ListByRoot 按根 revision 正序分页（afterRevision 之后，limit 上限）。
	ListByRoot(ctx context.Context, rootWorkItemID string, afterRevision int64, limit int) ([]*domain.TaskComment, error)
	// LatestRevision 返回 cursor 水位；无 cursor 返回 ErrNotFound（comment_coordinator_required 映射依据）。
	LatestRevision(ctx context.Context, rootWorkItemID string) (int64, error)
	// HasUnconsumedActionable 报告是否存在 revision > consumed 的 actionable 评论
	//（Coordinator 终态钩子：存在则不得进入 waiting_user）。
	HasUnconsumedActionable(ctx context.Context, rootWorkItemID string, consumedRevision int64) (bool, error)
	// ListUnconsumed 返回 revision > consumed 的全部评论（正序），
	// 供 Coordinator Run 创建事务快照进 Run input 并推进 consumed_comment_revision。
	ListUnconsumed(ctx context.Context, rootWorkItemID string, consumedRevision int64) ([]*domain.TaskComment, error)
}

// ExecutionHostRepo 宿主身份与 mount 广告投影存储（RFC §4.1/§4.3）。
// hello 只能更新广告投影；ExecutionHost 的创建只走受保护 enrollment/本机 bootstrap。
type ExecutionHostRepo interface {
	// EnsureLocalHost 幂等确保受保护本机 Host（domain.LocalHostID）存在。
	EnsureLocalHost(ctx context.Context, now time.Time) (*domain.ExecutionHost, error)
	Get(ctx context.Context, id string) (*domain.ExecutionHost, error)
	// Create 仅受保护 enrollment 命令调用；hello 不得创建 Host。
	Create(ctx context.Context, h *domain.ExecutionHost) error
	Update(ctx context.Context, h *domain.ExecutionHost, expectedVersion int) error
	List(ctx context.Context) ([]*domain.ExecutionHost, error)
	SetStatus(ctx context.Context, id string, status domain.HostStatus, at time.Time) error
	// UpsertMount 按 (host, alias) 覆盖广告投影（generation/checkouts 随 hello 换代）。
	UpsertMount(ctx context.Context, m *domain.HostMount) error
	GetMount(ctx context.Context, hostID, alias string) (*domain.HostMount, error)
	ListMounts(ctx context.Context, hostID string) ([]*domain.HostMount, error)
}

// WorkspaceLocationRepo 业务 Workspace ↔ HostMount 绑定存储（RFC §4.4）。
// identity/version 只由显式命令修改；status 可随健康投影变化。
type WorkspaceLocationRepo interface {
	Create(ctx context.Context, l *domain.WorkspaceLocation) error
	Get(ctx context.Context, id string) (*domain.WorkspaceLocation, error)
	Update(ctx context.Context, l *domain.WorkspaceLocation, expectedVersion int) error
	ListByWorkspace(ctx context.Context, workspaceID string) ([]*domain.WorkspaceLocation, error)
	// DefaultFor 返回默认 Location；无默认返回 ErrNotFound（调用方映射 workspace_location_required）。
	DefaultFor(ctx context.Context, workspaceID string) (*domain.WorkspaceLocation, error)
	SetStatus(ctx context.Context, id string, status domain.LocationStatus, at time.Time) error
}

// WorkItemContextRepo DevelopmentContext 存储（RFC §4.5）。root 与 user child
// 有持久行；Plan child 默认继承根 context，不持久化重复副本。
type WorkItemContextRepo interface {
	Upsert(ctx context.Context, c *domain.DevelopmentContext) error
	// Get 返回该 WorkItem 的显式 context；无行返回 ErrNotFound（调用方沿父链继承）。
	Get(ctx context.Context, workItemID string) (*domain.DevelopmentContext, error)
}

// ContextSnapshotRepo 不可变 Snapshot 存储（RFC §4.6）。无 Update/Delete——
// 不可变性由迁移 trigger 强制，仓储只暴露 Create/Get。
type ContextSnapshotRepo interface {
	Create(ctx context.Context, s *domain.ExecutionContextSnapshot) error
	Get(ctx context.Context, id string) (*domain.ExecutionContextSnapshot, error)
	GetByRun(ctx context.Context, runID string) (*domain.ExecutionContextSnapshot, error)
	// HasActiveRunOnCheckout 报告同 Host 同 checkout/worktree ref 是否已有非终态 Run
	//（同 checkout 第一版单活跃 Run；命中即 workspace_checkout_busy）。
	HasActiveRunOnCheckout(ctx context.Context, hostID, checkoutRef string) (bool, error)
}

type WorkspaceRepo interface {
	Get(ctx context.Context, id string) (*domain.Workspace, error)
	Create(ctx context.Context, ws *domain.Workspace) error
	Update(ctx context.Context, ws *domain.Workspace, expectedVersion int) error
	ListIDs(ctx context.Context) ([]string, error)
}

type AgentRepo interface {
	Create(ctx context.Context, a *domain.AgentProfile) error
	Get(ctx context.Context, id string) (*domain.AgentProfile, error)
	List(ctx context.Context, workspaceID string) ([]*domain.AgentProfile, error)
	Update(ctx context.Context, a *domain.AgentProfile, expectedVersion int) error
	SetPresence(ctx context.Context, id string, presence domain.AgentPresence) error
	// ListHeartbeatEnabled 心跳自主唤醒候选（timer 唤醒生产用）。
	ListHeartbeatEnabled(ctx context.Context) ([]*domain.AgentProfile, error)
}

// AgentConfigSyncIntentRepo is the durable bridge between an Agent CAS/event
// transaction and external configuration files. The target is immutable for
// one intent; only recovery metadata and the terminal applied state may move.
// Implementations must reuse the transaction carried by ctx.
type AgentConfigSyncIntentRepo interface {
	// Create records one pending target. A partial unique index guarantees at
	// most one non-applied intent per Agent.
	Create(ctx context.Context, intent *domain.AgentConfigSyncIntent) error
	Get(ctx context.Context, id string) (*domain.AgentConfigSyncIntent, error)
	GetActiveByAgent(ctx context.Context, agentID string) (*domain.AgentConfigSyncIntent, error)
	ListActive(ctx context.Context) ([]*domain.AgentConfigSyncIntent, error)
	// MarkFailed retains the intent and records a retryable external failure.
	MarkFailed(ctx context.Context, id string, expectedVersion int, message string) error
	// MarkConflict retains the intent but prevents automatic application after
	// target/current-agent identity drift.
	MarkConflict(ctx context.Context, id string, expectedVersion int, message string) error
	// MarkApplied seals a successful full external bundle.
	MarkApplied(ctx context.Context, id string, expectedVersion int, appliedAt time.Time) error
}

// GoalRepo persists the long-running governance intent above the existing
// WorkItem/Plan/Run execution plane. RootWorkItemID is unique and belongs to
// the same workspace; GetByRootWorkItem supports idempotent Goal creation.
type GoalRepo interface {
	Create(ctx context.Context, goal *domain.Goal) error
	Get(ctx context.Context, id string) (*domain.Goal, error)
	GetByRootWorkItem(ctx context.Context, rootWorkItemID string) (*domain.Goal, error)
	List(ctx context.Context, workspaceID string) ([]*domain.Goal, error)
	Update(ctx context.Context, goal *domain.Goal, expectedVersion int) error
}

// TodoRepo persists bounded governance intent. Claim/Release are the only
// ownership write points; they CAS the Todo version and retain a monotonic
// claim generation across release/reclaim (separate from Runner leases).
type TodoRepo interface {
	Create(ctx context.Context, todo *domain.Todo) error
	Get(ctx context.Context, id string) (*domain.Todo, error)
	ListByGoal(ctx context.Context, goalID string) ([]*domain.Todo, error)
	Update(ctx context.Context, todo *domain.Todo, expectedVersion int) error
	Claim(ctx context.Context, todoID, ownerAgentID string, claimedAt, expiresAt time.Time, expectedVersion int) (*domain.Todo, error)
	// TransferClaim atomically replaces one active governance owner with another;
	// it never touches a Runner lease and advances claim_version to fence ABA.
	TransferClaim(ctx context.Context, todoID, sourceAgentID, targetAgentID string, claimedAt, expiresAt time.Time, expectedVersion, sourceClaimVersion int) (*domain.Todo, error)
	// RenewClaim extends the current owner's expiry without advancing
	// claim_version; an owner/generation mismatch is a hard conflict.
	RenewClaim(ctx context.Context, todoID, ownerAgentID string, claimVersion int, renewedAt, expiresAt time.Time, expectedVersion int) (*domain.Todo, error)
	// ResumeAdmitted resumes one already-allocated receipt turn after Goal
	// pause/resume. It never increments last_turn_seq or creates a Header.
	ResumeAdmitted(ctx context.Context, todoID, ownerAgentID string, turnSeq int64, resumedAt time.Time, expectedVersion int) (*domain.Todo, error)
	// Complete closes the current Todo only with an already-admitted TurnKey and
	// accepted evidence identity; replaying the exact identity is idempotent.
	Complete(ctx context.Context, todoID string, key domain.TurnKey, evidenceID string, completedAt time.Time, expectedVersion int) (*domain.Todo, error)
	Release(ctx context.Context, todoID, ownerAgentID string, releasedAt time.Time, expectedVersion int) (*domain.Todo, error)
	Cancel(ctx context.Context, todoID string, cancelledAt time.Time, expectedVersion int) (*domain.Todo, error)
}

// TurnReceiptRepo is the canonical bounded-turn persistence port. Admit
// allocates the next Todo turn sequence and must atomically update Todo plus
// insert the immutable Header. AppendPhase is append-only and idempotent by
// (turn key, phase sequence, digest).
type TurnReceiptRepo interface {
	Admit(ctx context.Context, header *domain.TurnReceiptHeader, ownerAgentID string, expectedTodoVersion int) (*domain.TurnReceiptHeader, error)
	GetHeaderByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.TurnReceiptHeader, error)
	GetHeaderBySourceRun(ctx context.Context, sourceRunID string) (*domain.TurnReceiptHeader, error)
	GetHeader(ctx context.Context, key domain.TurnKey) (*domain.TurnReceiptHeader, error)
	// ListHeadersByGoal enumerates canonical turn identities for projection replay.
	ListHeadersByGoal(ctx context.Context, goalID string) ([]*domain.TurnReceiptHeader, error)
	GetPhase(ctx context.Context, key domain.TurnKey, phaseSeq int) (*domain.TurnReceiptPhase, error)
	ListPhases(ctx context.Context, key domain.TurnKey) ([]*domain.TurnReceiptPhase, error)
	AppendPhase(ctx context.Context, phase *domain.TurnReceiptPhase) (*domain.TurnReceiptPhase, error)
	// LatestTurnHeaderByRunID 治理互链反查：任一 receipt phase 的 run_ids JSON
	// 数组包含 runID 的最新 turn 的 immutable header；无治理引用返回 (nil, nil)。
	// Run Journal 调试面专用（GET /runs/{run_id}/journal）。
	LatestTurnHeaderByRunID(ctx context.Context, runID string) (*domain.TurnReceiptHeader, error)
}

// QuotaRepo persists the quota admission/settlement ledger.  Reservations are
// keyed by an admitted Turn plus QuotaKind; spend entries are keyed by that
// identity plus RunID and are append-only.  All methods reuse the transaction
// carried by ctx, so callers can atomically reserve with Plan/Run creation.
type QuotaRepo interface {
	Get(ctx context.Context, key domain.QuotaReservationKey) (*domain.QuotaReservation, error)
	ListByGoal(ctx context.Context, goalID string) ([]*domain.QuotaReservation, error)
	// Reserve returns created=false for an exact replay of the frozen
	// reservation intent.  A different policy/price/amount for the same key is
	// ErrIdempotencyConflict.
	Reserve(ctx context.Context, reservation *domain.QuotaReservation) (created bool, err error)
	// Commit/Release/Expire expect the candidate's next version and status;
	// identity, requested amount, policy and price remain immutable.
	Commit(ctx context.Context, reservation *domain.QuotaReservation, expectedVersion int) error
	Release(ctx context.Context, reservation *domain.QuotaReservation, expectedVersion int) error
	Expire(ctx context.Context, reservation *domain.QuotaReservation, expectedVersion int) error
	GetSpend(ctx context.Context, key domain.QuotaSpendKey) (*domain.QuotaSpendEntry, error)
	// AppendSpend returns created=false for an exact semantic replay and
	// ErrIdempotencyConflict when an append-only identity is reused differently.
	AppendSpend(ctx context.Context, entry *domain.QuotaSpendEntry) (created bool, err error)
	// ListSpendByTurn returns every spend entry of one governance Turn ordered by
	// (quota_kind, run_id) so settlement can compute per-kind committed totals and
	// deterministic receipt payloads.
	ListSpendByTurn(ctx context.Context, key domain.TurnKey) ([]*domain.QuotaSpendEntry, error)
	ListUnresolved(ctx context.Context, goalID string, kinds ...domain.QuotaKind) ([]*domain.QuotaSpendEntry, error)
	SumCommitted(ctx context.Context, goalID string, kinds ...domain.QuotaKind) (int64, error)
	// SumActiveReserved returns the total reserved_amount of one kind still in
	// reserved status for the Goal: budget frozen by in-flight Turns. Admission
	// preflight adds it to committed spend so concurrent Turns cannot oversubscribe.
	SumActiveReserved(ctx context.Context, goalID string, kind domain.QuotaKind) (int64, error)
	ActiveWorkerCount(ctx context.Context, goalID string) (int, error)
}

// HandoffRepo persists one immutable-identity governance handoff and its CAS
// state. Claim transfer remains an application transaction because TodoRepo is
// the sole ownership write point.
type HandoffRepo interface {
	Create(ctx context.Context, handoff *domain.Handoff) error
	Get(ctx context.Context, id string) (*domain.Handoff, error)
	GetByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.Handoff, error)
	ListByTodo(ctx context.Context, todoID string) ([]*domain.Handoff, error)
	ListByGoal(ctx context.Context, goalID string) ([]*domain.Handoff, error)
	Update(ctx context.Context, handoff *domain.Handoff, expectedVersion int) error
}

// GovernanceProjectionRepo stores only derived read models and repair records;
// it must never expose a write that mutates Goal/Todo/Receipt/Run authority.
type GovernanceProjectionRepo interface {
	Get(ctx context.Context, goalID string) (*domain.GovernanceGoalProjection, error)
	Upsert(ctx context.Context, projection *domain.GovernanceGoalProjection) error
	CreateRepair(ctx context.Context, repair *domain.ProjectionRepair) error
	GetRepair(ctx context.Context, id string) (*domain.ProjectionRepair, error)
	GetRepairByClientKey(ctx context.Context, goalID, clientKey string) (*domain.ProjectionRepair, error)
	ListRepairsByGoal(ctx context.Context, goalID string) ([]*domain.ProjectionRepair, error)
	UpdateRepair(ctx context.Context, repair *domain.ProjectionRepair, expectedVersion int) error
}

// ValidationResultRepo is the canonical source behind
// EvidenceSourceValidationResult. It intentionally has no generic update or
// delete operation: a changed validation is a new result identity.
type ValidationResultRepo interface {
	Create(ctx context.Context, result *domain.ValidationResult) error
	Get(ctx context.Context, id string) (*domain.ValidationResult, error)
	GetBySourceRun(ctx context.Context, runID string) (*domain.ValidationResult, error)
	ListByGoal(ctx context.Context, goalID string) ([]*domain.ValidationResult, error)
}

// DeliveryBriefSnapshotRepo stores sealed deterministic Delivery Brief
// captures. There is no Update/Delete operation: append-only and identity /
// content immutability are enforced by the SQLite schema as well as domain
// validation. client_key replay is scoped to (Goal, Todo).
type DeliveryBriefSnapshotRepo interface {
	Create(ctx context.Context, snapshot *domain.DeliveryBriefSnapshot) error
	Get(ctx context.Context, id string) (*domain.DeliveryBriefSnapshot, error)
	GetByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.DeliveryBriefSnapshot, error)
}

// QuotaGapResolutionRepo stores one immutable reconciled adjustment per
// unresolved spend identity. The original quota spend remains append-only and
// unresolved for audit; this repo only records the separately adjudicated
// additive amount.
type QuotaGapResolutionRepo interface {
	Create(ctx context.Context, resolution *domain.QuotaGapResolution) (bool, error)
	Get(ctx context.Context, id string) (*domain.QuotaGapResolution, error)
	GetByTarget(ctx context.Context, key domain.QuotaSpendKey) (*domain.QuotaGapResolution, error)
	GetByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.QuotaGapResolution, error)
	ListByGoal(ctx context.Context, goalID string) ([]*domain.QuotaGapResolution, error)
}

// TaskCoordinatorRepo is the persistence port for the system Task Coordinator.
// A workspace has one protected config/profile; a root Task has one state and
// children resolve that state through GetStateForWorkItem. State updates use
// optimistic CAS. Events are append-only and ordered by occurred_at/id.
type TaskCoordinatorRepo interface {
	// EnsureConfig atomically ensures the workspace's hidden system profile and
	// its coordinator config. It is idempotent and returns the existing config
	// when called more than once.
	EnsureConfig(ctx context.Context, workspaceID string) (*domain.TaskCoordinatorConfig, error)
	GetConfig(ctx context.Context, workspaceID string) (*domain.TaskCoordinatorConfig, error)
	// UpdateConfig changes runtime/model/reasoning only. AgentProfileID and
	// PromptVersion are immutable and are ignored by the persistence update.
	UpdateConfig(ctx context.Context, c *domain.TaskCoordinatorConfig, expectedVersion int) error

	// CreateState creates the sole coordinator state for a root Task. The
	// repository rejects child/non-task WorkItems and duplicate roots.
	CreateState(ctx context.Context, state *domain.TaskCoordinatorState) error
	GetState(ctx context.Context, rootWorkItemID string) (*domain.TaskCoordinatorState, error)
	// GetStateForWorkItem resolves any child WorkItem to its root coordinator
	// state; the caller does not need to walk parent links itself.
	GetStateForWorkItem(ctx context.Context, workItemID string) (*domain.TaskCoordinatorState, error)
	UpdateState(ctx context.Context, state *domain.TaskCoordinatorState, expectedVersion int) error
	// ListDueStates returns queued/running/waiting_retry states whose next action
	// is due. workspaceID may be empty for startup recovery across workspaces.
	ListDueStates(ctx context.Context, workspaceID string, now time.Time, limit int) ([]*domain.TaskCoordinatorState, error)

	AppendEvent(ctx context.Context, event *domain.TaskCoordinatorEvent) error
	// ListEvents lists the root task timeline in causal order. A non-empty
	// workItemID is resolved to its root before querying.
	ListEvents(ctx context.Context, workItemID string, limit int) ([]*domain.TaskCoordinatorEvent, error)
}

// WorkItemFilter 查询条件；record_kind 是 Chat/Task 硬边界；cursor 为不透明 token，改变筛选必须重新分页。
// ParentID："" 不过滤；"none" 只看根任务（parent IS NULL）；其他值按该 parent 过滤。
type WorkItemFilter struct {
	RecordKind domain.WorkItemRecordKind
	Status     domain.WorkItemStatus
	Priority   domain.Priority
	Assignee   string
	ParentID   string
	Cursor     string
	Limit      int
}

type WorkItemRepo interface {
	Create(ctx context.Context, wi *domain.WorkItem) error
	Get(ctx context.Context, id string) (*domain.WorkItem, error)
	// TouchUpdatedAt 仅刷新记录的列表排序时间，不改变 status/phase/lock/version。
	// Chat 新消息使用该写点保持最近会话置顶，同时不套用任务状态机。
	TouchUpdatedAt(ctx context.Context, workItemID string, at time.Time) error
	// GetByClientKey 按 (workspace, client_key) 查回既有实体（实体级幂等重放路径）。
	GetByClientKey(ctx context.Context, workspaceID, clientKey string) (*domain.WorkItem, error)
	List(ctx context.Context, workspaceID string, f WorkItemFilter) ([]*domain.WorkItem, string, error)
	Update(ctx context.Context, wi *domain.WorkItem, expectedVersion int) error
	// ListByParent 按 created_at 升序返回直接子任务（子任务树遍历用）。
	ListByParent(ctx context.Context, parentID string) ([]*domain.WorkItem, error)
	ActiveBlocker(ctx context.Context, workItemID string) (*domain.Blocker, error)
	CreateBlocker(ctx context.Context, b *domain.Blocker) error
	ResolveBlockers(ctx context.Context, workItemID string, at time.Time) error
	LatestRunID(ctx context.Context, workItemID string) (string, int, error)
	// ReleaseStaleLocks 回收兜底：清空 locked_at 早于 olderThan 且属主 run 已终态的
	// 执行锁，返回释放行数（调度循环低频扫描用）。
	ReleaseStaleLocks(ctx context.Context, olderThan time.Time) (int, error)
	// UpdateRollingDigest 任务台账滚动摘要的守卫写（S2）：version 乐观锁互斥
	// 并发终态钩子，不 bump updated_at（摘要刷新不是任务编辑）。
	UpdateRollingDigest(ctx context.Context, workItemID, digest string, expectedVersion int) error
	// BoardCounts / CompletedToday 供 Dashboard Read Model 服务端聚合。
	BoardCounts(ctx context.Context, workspaceID string) (map[domain.WorkItemStatus]int, error)
	CompletedToday(ctx context.Context, workspaceID string, day time.Time) (int, error)
}

// PlanRepo M1 编排计划存储。Create 必须在事务内调用（plan + steps 同事务）；
// Update/UpdateStep 乐观锁写回。同一 work item 至多一个 active/waiting plan
// 由 SubmitPlan 在事务内校验（无 DB 部分唯一索引，SQLite 与 PG 保持同一语义）。
type PlanRepo interface {
	Create(ctx context.Context, p *domain.Plan) error
	Get(ctx context.Context, id string) (*domain.Plan, error)
	// GetByClientKey 按 (workspace, client_key) 查回治理编译产生的既有 Plan；
	// 不存在时返回 (nil, nil)，供同一治理 turn 的幂等重放使用。
	GetByClientKey(ctx context.Context, workspaceID, clientKey string) (*domain.Plan, error)
	// GetBySourceRun resolves the immutable Planner decision identity. A
	// terminal-hook replay must observe the existing Plan before active/waiting
	// supersession logic can reinterpret the same source Run.
	GetBySourceRun(ctx context.Context, sourceRunID string) (*domain.Plan, error)
	Update(ctx context.Context, p *domain.Plan, expectedVersion int) error
	UpdateStep(ctx context.Context, st *domain.PlanStep) error
	// ActiveByWorkItem 返回 active/waiting plan（至多一个；无则 nil）。
	ActiveByWorkItem(ctx context.Context, workItemID string) (*domain.Plan, error)
	// LatestByWorkItem 返回最新一份 plan（不限状态；无则 nil）。
	LatestByWorkItem(ctx context.Context, workItemID string) (*domain.Plan, error)
}

type RunRepo interface {
	Create(ctx context.Context, r *domain.ExecutionRun) error
	Get(ctx context.Context, id string) (*domain.ExecutionRun, error)
	// SetContextSnapshot 在 Run 创建事务内回填 context_snapshot_id（写序：
	// run.Create(snapshot 空) → snapshot.Create → SetContextSnapshot）。
	// 只允许回填一次：已置快照的 Run 再写返回 ErrStateConflict（Snapshot 一对一不可换绑）。
	SetContextSnapshot(ctx context.Context, runID, snapshotID string) error
	// GetByClientKey 按 (workspace, client_key) 查回既有 run（实体级幂等重放路径）。
	GetByClientKey(ctx context.Context, workspaceID, clientKey string) (*domain.ExecutionRun, error)
	Update(ctx context.Context, r *domain.ExecutionRun, expectedVersion int) error
	ListByWorkItem(ctx context.Context, workItemID string) ([]*domain.ExecutionRun, error)
	// ListByDispatch 按创建时间升序返回派发批次的成员 run（会话组查询键）。
	ListByDispatch(ctx context.Context, dispatchID string) ([]*domain.ExecutionRun, error)
	// ListByGovernanceTurn 按 input.governance 的 (goal_id,todo_id,turn_seq) 三元组
	// 返回该治理 Turn 的受管 Run（含 plan 派发、evaluation、retry/heal 克隆），
	// 按 created_at 升序。workspaceID 参与过滤以防跨工作区串账；Coordinator source
	// Run 不携带 governance 身份，不在本查询结果内（由 receipt phase1 引用）。
	ListByGovernanceTurn(ctx context.Context, workspaceID, goalID, todoID string, turnSeq int64) ([]*domain.ExecutionRun, error)
	ActiveByAgent(ctx context.Context, agentProfileID string) ([]*domain.ExecutionRun, error)
	// LeaselessActive 无任何 lease 行且非终态的 run（进程内执行孤儿，启动对账用）。
	LeaselessActive(ctx context.Context) ([]*domain.ExecutionRun, error)
	CreateApproval(ctx context.Context, a *domain.ApprovalRequest) error
	GetApproval(ctx context.Context, id string) (*domain.ApprovalRequest, error)
	ListApprovals(ctx context.Context, runID string) ([]*domain.ApprovalRequest, error)
	// ListPendingPlanDispatchApprovals returns unbound manual dispatch gates for
	// one work item. Plan dispatch approvals have no run_id, so the run-scoped
	// ListApprovals query cannot discover them for supersede cleanup.
	ListPendingPlanDispatchApprovals(ctx context.Context, workItemID string) ([]*domain.ApprovalRequest, error)
	UpdateApproval(ctx context.Context, a *domain.ApprovalRequest) error
	CreateArtifact(ctx context.Context, art *domain.Artifact) error
	GetArtifact(ctx context.Context, artifactID string) (*domain.Artifact, error)
	UpdateArtifactStatus(ctx context.Context, artifactID string, status domain.ArtifactStatus) error
	ListArtifacts(ctx context.Context, runID string) ([]*domain.Artifact, error)
	ActiveCount(ctx context.Context, workspaceID string) (int, error)
}

// ApprovalGrantRepo 「总是允许」授权存储（scope≠once 决议的落库形态）。
type ApprovalGrantRepo interface {
	Create(ctx context.Context, g *domain.ApprovalGrant) error
	// Matching 返回可代答请求的授权：同 workspace/agent/kind，workspace 作用域或
	// thread 作用域且锚定同一 work item，pattern 空或请求摘要前缀命中；多命中取
	// 最新。无命中返回 (nil, nil)。
	Matching(ctx context.Context, workspaceID, agentProfileID, workItemID, kind, summary string) (*domain.ApprovalGrant, error)
}

// EventRepo 负责 Canonical Event 持久化：stream_events + outbox 同事务写入，
// run_seq 域内事件同时写 run_events。
type EventRepo interface {
	// Append 在事务内分配 stream_seq（Run 事件同时分配 run_seq），返回补齐序号的事件。
	Append(ctx context.Context, ev *domain.CanonicalEvent, runEvent *RunEventRecord) (*domain.CanonicalEvent, error)
	// Since 从 afterSeq 之后补发；超过保留窗口返回 ErrCursorExpired。
	Since(ctx context.Context, workspaceID string, afterSeq int64, limit int) ([]*domain.CanonicalEvent, error)
	LatestSeq(ctx context.Context, workspaceID string) (int64, error)
	AppendActivity(ctx context.Context, workspaceID, kind, message string) error
	// AppendActivityFor 写带 work item 归因的 activity（M4：verdict/blocker 级
	// 事件需回溯到任务）；workItemID 空串落 NULL（无归因）。
	AppendActivityFor(ctx context.Context, workspaceID, workItemID, kind, message string) error
	ListActivities(ctx context.Context, workspaceID string, limit int) ([]Activity, error)
	// ListRunEvents 按 run_seq 回放单个 Run 的 surface 事件（对话历史用）；
	// internal 类事件（Run Journal，domain.IsInternalEventName）被过滤。
	ListRunEvents(ctx context.Context, runID string) ([]RunEvent, error)
	// ListRunEventsIncludeInternal 回放含 internal 类的全部事件；调试/日志面
	// 专用，对话回放与模型上下文重建不得使用。
	ListRunEventsIncludeInternal(ctx context.Context, runID string) ([]RunEvent, error)
}

// RunEventRecord 可选：Run 域事件追加进 run_events（同事务）。
type RunEventRecord struct {
	RunID     string
	AgentID   string
	EventType string
	Payload   map[string]any
}

// RunEvent 是 run_events 的只读投影。
type RunEvent struct {
	RunSeq     int64          `json:"run_seq"`
	AgentID    string         `json:"agent_id"`
	EventType  string         `json:"event_type"`
	Payload    map[string]any `json:"payload,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

type Activity struct {
	ID string
	// WorkItemID 归因任务（M4；verdict/blocker 级 activity 非空）。空串 =
	// 无归因（runner 级、workspace 级公告）。
	WorkItemID string
	Kind       string
	Message    string
	OccurredAt time.Time
}

// IdempotencyRecord 幂等记录；相同 key + 不同 request hash 返回冲突。
type IdempotencyRecord struct {
	RequestHash string
	StatusCode  int
	ResultBody  string
}

type IdempotencyRepo interface {
	Check(ctx context.Context, workspaceID, key string) (*IdempotencyRecord, error)
	Record(ctx context.Context, workspaceID, key string, rec IdempotencyRecord) error
}

// RuntimeBindingRepo 设置页 Runtime 与模型配置存储。
type RuntimeBindingRepo interface {
	Create(ctx context.Context, b *domain.RuntimeBinding) error
	Get(ctx context.Context, id string) (*domain.RuntimeBinding, error)
	GetByLabel(ctx context.Context, workspaceID, label string) (*domain.RuntimeBinding, error)
	List(ctx context.Context, workspaceID string) ([]*domain.RuntimeBinding, error)
	Update(ctx context.Context, b *domain.RuntimeBinding, expectedVersion int) error
}

// Dispatcher 把 queued Run 交给 Runtime Gateway / Mock Adapter 执行。
// 控制平面写入 Run 成功后才允许调用，避免幽灵任务。
type Dispatcher interface {
	Dispatch(ctx context.Context, run *domain.ExecutionRun) error
}

// Notifier 通知 SSE 层有新事件可补发（具体补发走 EventRepo replay）。
type Notifier interface {
	Notify(workspaceID string)
}

// Runner 注册与连接状态（M2 Runner WSS Gateway）。
// v2：Runner 是基础设施，不属于单一 Workspace（workspace_id 列语义废弃）；
// 归属真相是 execution_host_id；connection_epoch 每次新连接换代，只做 transport fencing。
type Runner struct {
	ID              string
	WorkspaceID     string // v2 起废弃：不再作为归属真相，仅为遗留行兼容保留
	ExecutionHostID string
	ConnectionEpoch string
	// BootID 在 runnerd 进程启动时生成、网络重连保持不变。Gateway 只允许同一
	// BootID 接回未释放 lease；不同 BootID 代表内存 pending/runs 已丢失。
	BootID        string
	Label         string
	RunnerVersion string
	OS            string
	Arch          string
	Slots         int
	Status        string // connected / degraded / offline
	LastSeenAt    *time.Time
}

type RunLease struct {
	LeaseID      string
	RunID        string
	RunnerID     string
	FencingToken int64
	RenewedUntil time.Time
	Released     bool
}

type RunnerRepo interface {
	Upsert(ctx context.Context, r *Runner) error
	SetStatus(ctx context.Context, runnerID, status string, at time.Time) error
	List(ctx context.Context, workspaceID string) ([]*Runner, error)
	// CreateLease 分配递增 fencing_token；旧连接恢复不能继续写入。
	CreateLease(ctx context.Context, l *RunLease) error
	ActiveLease(ctx context.Context, runID string) (*RunLease, error)
	// GetLease 按 lease_id 读租约（含已释放）：终态后补发的 usage 事件用它做
	// 「该 Run 已释放租约」的身份比对，而不是被活动租约检查一律打成 stale。
	GetLease(ctx context.Context, leaseID string) (*RunLease, error)
	ReleaseLease(ctx context.Context, leaseID string, at time.Time) error
	// ReleaseActiveLeasesByRunner 在 runner boot_id 变化时撤销该旧进程持有的
	// 全部 active lease，并返回受影响 Run；不能只释放最高 fence 留低 fence 续租。
	ReleaseActiveLeasesByRunner(ctx context.Context, runnerID string, at time.Time) ([]string, error)
	// ExpireLeases 把过期未续租的 lease 释放，返回受影响 run。
	ExpireLeases(ctx context.Context, now time.Time) ([]string, error)
	// RenewLeasesByRunner 续租该 runner 持有且 run 仍非终态的活跃 lease
	//（推进 renewed_until），并顺手释放已终态 run 的残留 lease；返回续租行数。
	RenewLeasesByRunner(ctx context.Context, runnerID string, renewUntil time.Time) (int, error)
	// RenewLeasesByRunnerIfEpoch v2：仅当 runner 当前 connection_epoch 与 boot_id
	// 均一致才续租（旧连接或重启后的空内存进程不得续孤儿 lease）；失配返回 0 行。
	RenewLeasesByRunnerIfEpoch(ctx context.Context, runnerID, epoch, bootID string, renewUntil time.Time) (int, error)
	// Get 按 ID 读 Runner（hello 的 epoch/host 校验用）。
	Get(ctx context.Context, runnerID string) (*Runner, error)
	// ListActiveLeasesByRunner 返回该 Runner 持有、尚未释放且关联 Run 非终态的
	// lease。Gateway 在进程重启或 connection epoch 换代时据此恢复 fencing。
	ListActiveLeasesByRunner(ctx context.Context, runnerID string) ([]*RunLease, error)
	// RunnerEventDedupV2 按 (run_id, lease_id, runner_id, producer_seq) 条件插入去重
	//（RFC §8.3：dedup 不含 connection_epoch；event_id 随行记录供 ACK 回显对账）。
	// 重复返回 ErrIdempotencyConflict。必须在 ApplyRunnerEvent 同事务内调用。
	RunnerEventDedupV2(ctx context.Context, runID, leaseID, runnerID string, producerSeq int64, eventID string) error
}

// AuditRepo 不可变审计记录：审批、运行控制、凭据变更、导出等。
type AuditRepo interface {
	Append(ctx context.Context, workspaceID string, actor map[string]any, action, target string, detail map[string]any) error
}

type CapabilitySnapshot struct {
	ID         string
	RunID      string
	Required   map[string]any
	Advertised map[string]any
}

type CapabilitySnapshotRepo interface {
	Create(ctx context.Context, s *CapabilitySnapshot) error
	Get(ctx context.Context, id string) (*CapabilitySnapshot, error)
}

// TaskSessionRepo 跨 Run 会话锚点存储（Paperclip agent_task_sessions 对应物）。
// 唯一键 (workspace_id, agent_profile_id, adapter_id, task_key)；AgentProfileID 空串表示匿名。
// 清除锚点统一走墓碑语义（Upsert 空 __ref），不做物理 DELETE。
type TaskSessionRepo interface {
	Get(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey string) (*domain.TaskSession, error)
	// ClaimAnchor atomically reserves the next anchor sequence for a newly-created
	// Run. The returned row is the durable owner after the claim; concurrent
	// claims must never reuse a sequence or overwrite a later owner.
	ClaimAnchor(ctx context.Context, t *domain.TaskSession) (*domain.TaskSession, error)
	// InsertIfAbsent creates session material only when no anchor exists. It must
	// never update a concurrently-claimed row.
	InsertIfAbsent(ctx context.Context, t *domain.TaskSession) (bool, error)
	// Upsert：params/display_id 整体替换；RunsCount/InputTokensCum 按 delta 原子累加
	//（ON CONFLICT 语义，并发 Run 不丢计数）。行不存在时以 delta 为初值插入。
	Upsert(ctx context.Context, t *domain.TaskSession) error
	// UpdateIfAnchorOwner updates session material only while the supplied Run is
	// still the claimed owner at the supplied sequence. It never changes anchor
	// ownership and reports false for a late callback.
	UpdateIfAnchorOwner(ctx context.Context, t *domain.TaskSession, runID string, sequence int64) (bool, error)
	// AddInputTokens 以 checked non-negative delta 累加会话输入 token；缺行或
	// overflow 必须返回错误，不能静默污染/丢失 rotation watermark。
	AddInputTokens(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey string, tokens int64) error
	// StartGeneration 轮换换代：params 整体替换、计数覆盖重起、created_at 重置。
	StartGeneration(ctx context.Context, t *domain.TaskSession) error
	// StartGenerationIfAnchorOwner is the rotation variant of UpdateIfAnchorOwner.
	StartGenerationIfAnchorOwner(ctx context.Context, t *domain.TaskSession, runID string, sequence int64) (bool, error)
	// UpdateProviderUsageAnchorCAS advances the provider cumulative baseline only
	// when both provider_usage_anchor_seq and the Run anchor owner identity still
	// match. It never changes anchor ownership or session material and reports
	// false on a CAS miss so a stale Run cannot clobber a newer owner.
	UpdateProviderUsageAnchorCAS(ctx context.Context, workspaceID, agentProfileID, adapterID, taskKey string, anchor *domain.ProviderUsageAnchorV1, expectedSeq int64, ownerRunID string, ownerRunSequence int64) (bool, error)
	ListByAgent(ctx context.Context, workspaceID, agentProfileID string) ([]*domain.TaskSession, error)
}

// SearchKind 检索索引条目三类（会话元模型 S4；schema CHECK 同闭集）。
const (
	SearchKindSegmentSummary = "segment_summary"
	SearchKindDecision       = "decision"
	SearchKindArtifact       = "artifact"
)

// SearchEntry 索引写入条目（定点重写键 = kind + source_id）。
type SearchEntry struct {
	WorkItemID string
	Kind       string
	SourceID   string
	Title      string
	Body       string
}

// SearchResult 检索命中项；Snippet 是 SQLite snippet() 生成的正文摘录，
// 使用 [] 高亮标记与 … 省略号。
type SearchResult struct {
	WorkItemID string
	Kind       string
	SourceID   string
	Title      string
	Snippet    string
}

// SearchRepo FTS5 检索索引（会话元模型 S4）：索引是派生存储，可随时全量重建，
// 不发 SSE 事件。
type SearchRepo interface {
	// IndexEntry 定点重写（delete by (kind, source_id) + insert），天然幂等。
	IndexEntry(ctx context.Context, e *SearchEntry) error
	// Search workspace 隔离；query 为空/纯符号返回空结果；workItemID/kind 可选过滤。
	Search(ctx context.Context, workspaceID, query, workItemID, kind string, limit int) ([]*SearchResult, error)
}

// DecisionRepo 决策台账存储（会话元模型 S2）。quote 是用户原话（禁止 LLM
// 转述）；Create 与 decision.created 事件同事务。
type DecisionRepo interface {
	Create(ctx context.Context, e *domain.DecisionEntry) error
	// ListByWorkItem 按创建时间升序返回任务台账的决策原话。
	ListByWorkItem(ctx context.Context, workItemID string) ([]*domain.DecisionEntry, error)
}

// DispatchRepo 派发批次存储（会话元模型 S1）。Create 必须在创建成员 run 的
// 同一事务内、且先于成员行（execution_runs.dispatch_id 外键）。批次状态流转
// 的写入随 S3 回流收口一并接入。
type DispatchRepo interface {
	Create(ctx context.Context, d *domain.Dispatch) error
	Get(ctx context.Context, id string) (*domain.Dispatch, error)
	// SetLeadRun 接诊批次回填接诊 run id：dispatch↔run 互指（lead_run_id ↔
	// dispatch_id）无法单语句成环，落成员 run 行后同事务补写。
	SetLeadRun(ctx context.Context, id, leadRunID string) error
	// MarkCollecting 回流前置迁移（S3）：running→collecting 的 CAS，成功方获得
	// 唤醒 lead 的资格；collecting 下重复触发 0 行——只唤醒一次的存储层硬保证。
	MarkCollecting(ctx context.Context, id string) (bool, error)
	// CloseStatus 批次收口 CAS：running/collecting → 终态，单向写（终态行不可
	// 再改写）；0 行 = 并发方已收口，调用方 no-op。
	CloseStatus(ctx context.Context, id string, to domain.DispatchStatus, closedAt time.Time) (bool, error)
	// ListByWorkItem 按创建时间升序返回任务的全部批次（卡片端点倒序展示）。
	ListByWorkItem(ctx context.Context, workItemID string) ([]*domain.Dispatch, error)
}
