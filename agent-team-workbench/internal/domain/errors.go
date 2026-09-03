package domain

import (
	"errors"
	"fmt"
)

// 领域错误；httpapi 层映射为 problem+json（协议文档 §5.5）。
var (
	ErrNotFound            = errors.New("resource not found")
	ErrVersionConflict     = errors.New("resource version conflict")
	ErrIllegalTransition   = errors.New("illegal state transition")
	ErrTerminalImmutable   = errors.New("terminal state is immutable")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different request")
	// ErrIdempotencyClaimLost means a request no longer owns the durable
	// idempotency placeholder. Completion/release must fail closed instead of
	// mutating a replacement claimant's row.
	ErrIdempotencyClaimLost = errors.New("idempotency claim lost")
	ErrCapabilityMissing    = errors.New("required capability unavailable")
	ErrValidation           = errors.New("domain validation failed")
	// ErrAgentConfigSyncPending means an earlier external configuration bundle
	// is still the sole active intent for this Agent; callers must reconcile it
	// before changing the desired target.
	ErrAgentConfigSyncPending = errors.New("agent configuration sync pending")
	// ErrAgentConfigSyncConflict means the durable target and current Agent
	// projection disagree. It is intentionally fail-closed until an operator
	// resolves the external/database divergence.
	ErrAgentConfigSyncConflict = errors.New("agent configuration sync conflict")
	// ErrStateConflict 命令与当前资源状态冲突（非版本竞争）：如认领已被认领的
	// 任务、打回不在评审/验收态的任务。区别于 ErrVersionConflict（乐观锁失配）。
	ErrStateConflict = errors.New("command conflicts with current state")
	// ErrCursorExpired：SSE 游标已过保留窗口，客户端需重新 bootstrap（HTTP 410）。
	ErrCursorExpired = errors.New("event cursor expired")
)

// ── 执行上下文族 sentinel（任务控制面 RFC §9.7）─────────────────────────
//
// ContextError 携带稳定错误码与可重试性；httpapi 据此映射 problem+json。
// 判定一律用 errors.Is(err, domain.ErrXxx)——Detail 不同的具体实例经 %w 包装
// 仍可匹配 sentinel。

// ContextError 执行上下文族错误的稳定载体。
type ContextError struct {
	Code      string
	Retryable bool
}

func (e *ContextError) Error() string { return e.Code }

var (
	// ErrWorkspaceLocationRequired Workspace/Task 无可用 Location（422）。
	ErrWorkspaceLocationRequired = &ContextError{Code: "workspace_location_required"}
	// ErrWorkspaceLocationAmbiguous 多默认/多 mount 命中（409）。
	ErrWorkspaceLocationAmbiguous = &ContextError{Code: "workspace_location_ambiguous"}
	// ErrWorkspaceMountNotAdvertised Host 未广告 alias（422）。
	ErrWorkspaceMountNotAdvertised = &ContextError{Code: "workspace_mount_not_advertised"}
	// ErrWorkspaceContextMismatch repo/ref/digest 不一致（409）。
	ErrWorkspaceContextMismatch = &ContextError{Code: "workspace_context_mismatch"}
	// ErrWorkspaceMountGenerationChanged Host registry 已换代（409 retryable）。
	ErrWorkspaceMountGenerationChanged = &ContextError{Code: "workspace_mount_generation_changed", Retryable: true}
	// ErrWorkspaceBranchNotUnique branch 未唯一绑定 checkout（409）。
	ErrWorkspaceBranchNotUnique = &ContextError{Code: "workspace_branch_not_unique"}
	// ErrWorkspaceCheckoutBusy checkout 已被另一 Run 占用（409 retryable）。
	ErrWorkspaceCheckoutBusy = &ContextError{Code: "workspace_checkout_busy", Retryable: true}
	// ErrExecutionHostUnavailable 目标 Host 无可用 Runner（409 retryable）。
	ErrExecutionHostUnavailable = &ContextError{Code: "execution_host_unavailable", Retryable: true}
	// ErrDevelopmentContextBusy 存在非终态 Run（409 retryable）。
	ErrDevelopmentContextBusy = &ContextError{Code: "development_context_busy", Retryable: true}
	// ErrDevelopmentContextInvalid ref 组合非法（422）。
	ErrDevelopmentContextInvalid = &ContextError{Code: "development_context_invalid"}
	// ErrWorkspacePathForbidden 请求含越权 path/cwd（403）。
	ErrWorkspacePathForbidden = &ContextError{Code: "workspace_path_forbidden"}
)

// TransitionError 携带非法迁移的上下文。
type TransitionError struct {
	Entity string
	From   string
	To     string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s: illegal transition %s -> %s", e.Entity, e.From, e.To)
}

func (e *TransitionError) Unwrap() error { return ErrIllegalTransition }
