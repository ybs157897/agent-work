// Package httpapi 实现浏览器 ↔ 控制平面 REST/SSE 协议（协议文档 §5）。
package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Problem 是 application/problem+json 错误体（协议文档 §5.5）。
type Problem struct {
	Type           string `json:"type"`
	Title          string `json:"title"`
	Status         int    `json:"status"`
	Code           string `json:"code,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Instance       string `json:"instance,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
	Retryable      bool   `json:"retryable,omitempty"`
	CurrentVersion int    `json:"current_version,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	p.Instance = r.URL.Path
	if rid, ok := r.Context().Value(ctxKeyRequestID).(string); ok {
		p.RequestID = rid
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// fail 把领域错误映射为 problem+json；区分 retryable 与 terminal。
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/not-found",
			Title: "Resource not found", Status: http.StatusNotFound,
			Code: "not_found", Detail: "资源在当前 Workspace 视角不可见",
		})
	case errors.Is(err, domain.ErrVersionConflict):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/version-conflict",
			Title: "Resource version conflict", Status: http.StatusConflict,
			Code: "version_conflict", Retryable: true,
			Detail: "资源版本已变化，请刷新快照后重试",
		})
	case errors.Is(err, application.ErrReviewStateConflict):
		// Accept/Return/feedback 竞态（RFC §9.7）；须先于 ErrStateConflict 判定
		//（该哨兵包装了 ErrStateConflict 保持兼容）。
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/review-state-conflict",
			Title: "Review state conflict", Status: http.StatusConflict,
			Code:   "review_state_conflict",
			Detail: err.Error(),
		})
	case errors.Is(err, domain.ErrStateConflict):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/state-conflict",
			Title: "Command conflicts with current state", Status: http.StatusConflict,
			Code:   "state_conflict",
			Detail: err.Error(),
		})
	case errors.Is(err, domain.ErrIdempotencyConflict):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/idempotency-conflict",
			Title: "Idempotency conflict", Status: http.StatusConflict,
			Code:   "idempotency_conflict",
			Detail: "相同 Idempotency-Key 提交了不同请求体",
		})
	case errors.Is(err, domain.ErrIllegalTransition):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/illegal-transition",
			Title: "Illegal state transition", Status: http.StatusUnprocessableEntity,
			Code: "illegal_transition", Detail: err.Error(),
		})
	case errors.Is(err, domain.ErrValidation):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/validation",
			Title: "Validation failed", Status: http.StatusUnprocessableEntity,
			Code: "validation_failed", Detail: err.Error(),
		})
	case errors.Is(err, domain.ErrCapabilityMissing):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/capability-missing",
			Title: "Required capability unavailable", Status: http.StatusUnprocessableEntity,
			Code: "capability_missing", Detail: err.Error(),
		})
	case errors.Is(err, domain.ErrCursorExpired):
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/cursor-expired",
			Title: "Event cursor expired", Status: http.StatusGone,
			Code: "cursor_expired", Retryable: true,
			Detail: "游标已过保留窗口，请重新 bootstrap",
		})
	default:
		// 执行上下文族（任务控制面 RFC §9.7）：sentinel → 稳定 code/HTTP/retryable。
		if p, ok := contextProblem(err); ok {
			writeProblem(w, r, p)
			return
		}
		// 评论族（任务控制面 RFC §9.7 comment 族）。
		if p, ok := commentProblem(err); ok {
			writeProblem(w, r, p)
			return
		}
		log.Printf("httpapi: unhandled error: %v", err)
		writeProblem(w, r, Problem{
			Type:  "https://workbench.example/problems/internal",
			Title: "Internal error", Status: http.StatusInternalServerError,
			Code: "internal", Retryable: true,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// contextProblemTable 执行上下文族错误码映射（RFC §9.7）；code/HTTP/Retryable
// 与契约逐行对应。
var contextProblemTable = []struct {
	sentinel  error
	code      string
	status    int
	retryable bool
	title     string
}{
	{domain.ErrWorkspaceLocationRequired, "workspace_location_required", http.StatusUnprocessableEntity, false, "Workspace location required"},
	{domain.ErrWorkspaceLocationAmbiguous, "workspace_location_ambiguous", http.StatusConflict, false, "Workspace location ambiguous"},
	{domain.ErrWorkspaceMountNotAdvertised, "workspace_mount_not_advertised", http.StatusUnprocessableEntity, false, "Workspace mount not advertised"},
	{domain.ErrWorkspaceContextMismatch, "workspace_context_mismatch", http.StatusConflict, false, "Workspace context mismatch"},
	{domain.ErrWorkspaceMountGenerationChanged, "workspace_mount_generation_changed", http.StatusConflict, true, "Workspace mount generation changed"},
	{domain.ErrWorkspaceBranchNotUnique, "workspace_branch_not_unique", http.StatusConflict, false, "Workspace branch not unique"},
	{domain.ErrWorkspaceCheckoutBusy, "workspace_checkout_busy", http.StatusConflict, true, "Workspace checkout busy"},
	{domain.ErrExecutionHostUnavailable, "execution_host_unavailable", http.StatusConflict, true, "Execution host unavailable"},
	{domain.ErrDevelopmentContextBusy, "development_context_busy", http.StatusConflict, true, "Development context busy"},
	{domain.ErrDevelopmentContextInvalid, "development_context_invalid", http.StatusUnprocessableEntity, false, "Development context invalid"},
	{domain.ErrWorkspacePathForbidden, "workspace_path_forbidden", http.StatusForbidden, false, "Workspace path forbidden"},
}

// contextProblem 把 ContextError sentinel 映射为 problem；Detail 保留原文。
func contextProblem(err error) (Problem, bool) {
	for _, e := range contextProblemTable {
		if errors.Is(err, e.sentinel) {
			return Problem{
				Type:      "https://workbench.example/problems/" + e.code,
				Title:     e.title,
				Status:    e.status,
				Code:      e.code,
				Retryable: e.retryable,
				Detail:    err.Error(),
			}, true
		}
	}
	return Problem{}, false
}

// commentProblemTable 评论族错误码映射（RFC §9.7 comment 族）；code/HTTP/Retryable
// 与契约逐行对应。
var commentProblemTable = []struct {
	sentinel  error
	code      string
	status    int
	retryable bool
	title     string
}{
	{application.ErrCommentKindInvalid, "comment_kind_invalid", http.StatusUnprocessableEntity, false, "Comment kind invalid"},
	{application.ErrCommentBodyEmpty, "comment_body_empty", http.StatusUnprocessableEntity, false, "Comment body empty"},
	{application.ErrCommentBodyTooLarge, "comment_body_too_large", http.StatusRequestEntityTooLarge, false, "Comment body too large"},
	{application.ErrCommentTerminalWorkItem, "comment_terminal_work_item", http.StatusConflict, false, "Work item is terminal"},
	{application.ErrCommentSourceRunMismatch, "comment_source_run_mismatch", http.StatusUnprocessableEntity, false, "Source run outside task tree"},
	{application.ErrCommentCursorInvalid, "comment_cursor_invalid", http.StatusBadRequest, false, "Invalid comment cursor"},
	{application.ErrCommentCoordinatorRequired, "comment_coordinator_required", http.StatusConflict, false, "Coordinator required for comments"},
	{application.ErrReviewFeedbackRequired, "review_feedback_required", http.StatusUnprocessableEntity, false, "Review feedback required"},
	{application.ErrChildReviewNotSupported, "child_review_not_supported", http.StatusConflict, false, "Child review not supported"},
}

// commentProblem 把评论族 sentinel 映射为 problem；Detail 保留原文。
func commentProblem(err error) (Problem, bool) {
	for _, e := range commentProblemTable {
		if errors.Is(err, e.sentinel) {
			return Problem{
				Type:      "https://workbench.example/problems/" + e.code,
				Title:     e.title,
				Status:    e.status,
				Code:      e.code,
				Retryable: e.retryable,
				Detail:    err.Error(),
			}, true
		}
	}
	return Problem{}, false
}
