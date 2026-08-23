// Package httpapi 实现浏览器 ↔ 控制平面 REST/SSE 协议（协议文档 §5）。
package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

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
