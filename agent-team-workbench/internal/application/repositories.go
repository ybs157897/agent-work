// Package application 承载用例与事务边界（控制平面模块）。
// 仓储接口在此定义、由 persistence/postgres 实现，
// 保证领域逻辑不依赖存储细节。
package application

import (
	"context"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Store 是所有仓储的门面；InTx 内的 ctx 自动切换到同一事务。
type Store interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
	Workspaces() WorkspaceRepo
	Agents() AgentRepo
	WorkItems() WorkItemRepo
	Runs() RunRepo
	Events() EventRepo
	Idempotency() IdempotencyRepo
	Bindings() RuntimeBindingRepo
	Runners() RunnerRepo
	Audit() AuditRepo
	Caps() CapabilitySnapshotRepo
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
}

// WorkItemFilter 查询条件；cursor 为不透明 token，改变筛选必须重新分页。
type WorkItemFilter struct {
	Status   domain.WorkItemStatus
	Priority domain.Priority
	Assignee string
	Cursor   string
	Limit    int
}

type WorkItemRepo interface {
	Create(ctx context.Context, wi *domain.WorkItem) error
	Get(ctx context.Context, id string) (*domain.WorkItem, error)
	List(ctx context.Context, workspaceID string, f WorkItemFilter) ([]*domain.WorkItem, string, error)
	Update(ctx context.Context, wi *domain.WorkItem, expectedVersion int) error
	ActiveBlocker(ctx context.Context, workItemID string) (*domain.Blocker, error)
	CreateBlocker(ctx context.Context, b *domain.Blocker) error
	ResolveBlockers(ctx context.Context, workItemID string, at time.Time) error
	LatestRunID(ctx context.Context, workItemID string) (string, int, error)
	// BoardCounts / CompletedToday 供 Dashboard Read Model 服务端聚合。
	BoardCounts(ctx context.Context, workspaceID string) (map[domain.WorkItemStatus]int, error)
	CompletedToday(ctx context.Context, workspaceID string, day time.Time) (int, error)
}

type RunRepo interface {
	Create(ctx context.Context, r *domain.ExecutionRun) error
	Get(ctx context.Context, id string) (*domain.ExecutionRun, error)
	Update(ctx context.Context, r *domain.ExecutionRun, expectedVersion int) error
	ListByWorkItem(ctx context.Context, workItemID string) ([]*domain.ExecutionRun, error)
	ActiveByAgent(ctx context.Context, agentProfileID string) ([]*domain.ExecutionRun, error)
	CreateApproval(ctx context.Context, a *domain.ApprovalRequest) error
	GetApproval(ctx context.Context, id string) (*domain.ApprovalRequest, error)
	ListApprovals(ctx context.Context, runID string) ([]*domain.ApprovalRequest, error)
	UpdateApproval(ctx context.Context, a *domain.ApprovalRequest) error
	CreateArtifact(ctx context.Context, art *domain.Artifact) error
	ListArtifacts(ctx context.Context, runID string) ([]*domain.Artifact, error)
	ActiveCount(ctx context.Context, workspaceID string) (int, error)
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
	ListActivities(ctx context.Context, workspaceID string, limit int) ([]Activity, error)
	// ListRunEvents 按 run_seq 回放单个 Run 的事件（对话历史用）。
	ListRunEvents(ctx context.Context, runID string) ([]RunEvent, error)
}

// RunEventRecord 可选：Run 域事件追加进 run_events（同事务）。
type RunEventRecord struct {
	RunID     string
	EventType string
	Payload   map[string]any
}

// RunEvent 是 run_events 的只读投影。
type RunEvent struct {
	RunSeq     int64          `json:"run_seq"`
	EventType  string         `json:"event_type"`
	Payload    map[string]any `json:"payload,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

type Activity struct {
	ID         string
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
type Runner struct {
	ID            string
	WorkspaceID   string
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
	ReleaseLease(ctx context.Context, leaseID string, at time.Time) error
	// ExpireLeases 把过期未续租的 lease 释放，返回受影响 run。
	ExpireLeases(ctx context.Context, now time.Time) ([]string, error)
	// RunnerEventDedup 按 (run_id, runner_id, runner_seq) 去重；重复返回 ErrIdempotencyConflict。
	RunnerEventDedup(ctx context.Context, runID, runnerID string, runnerSeq int64) error
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
