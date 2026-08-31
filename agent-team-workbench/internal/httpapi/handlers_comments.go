package httpapi

// handlers_comments.go 任务评论流端点（任务控制面 RFC §9.4）：
//
//   - GET  /api/v1/work-items/{work_item_id}/comments：只读分页（after_revision
//     正序游标；非法值 400 comment_cursor_invalid）。
//   - POST /api/v1/work-items/{work_item_id}/comments：只接受 note|requirement
//     （review_feedback 只能由 return 命令生成）；HTTP Idempotency-Key 由
//     server.go 既有 idempotent 包装处理（重放原 comment/revision）；client_key
//     是实体级幂等（唯一域 (root, client_key)，同 key 不同 body 409）。

import (
	"net/http"
	"strconv"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func (s *Server) handleListWorkItemComments(w http.ResponseWriter, r *http.Request) {
	workItemID := r.PathValue("work_item_id")
	after := int64(0)
	if raw := r.URL.Query().Get("after_revision"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeProblem(w, r, Problem{
				Type:  "https://workbench.example/problems/comment-cursor-invalid",
				Title: "Invalid comment cursor", Status: http.StatusBadRequest,
				Code: "comment_cursor_invalid", Detail: "after_revision 必须是大于等于 0 的整数",
			})
			return
		}
		after = value
	}
	limit := int64(50)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && value > 0 {
			limit = value
		}
	}
	if limit > 200 {
		limit = 200
	}
	page, err := s.svc.ListTaskComments(r.Context(), workItemID, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]taskCommentDTO, 0, len(page.Items))
	for _, comment := range page.Items {
		items = append(items, toTaskCommentDTO(comment))
	}
	writeJSON(w, http.StatusOK, taskCommentListDTO{
		Items: items, NextRevision: page.NextRevision, LatestRevision: page.LatestRevision,
	})
}

func (s *Server) handleCreateTaskComment(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req createTaskCommentRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		comment, err := s.svc.AppendTaskComment(r.Context(), application.AppendTaskCommentParams{
			WorkItemID:              wiID,
			Kind:                    domain.CommentKind(req.Kind),
			Body:                    req.Body,
			SourceRunID:             req.SourceRunID,
			SourceRef:               req.SourceRef,
			ClientKey:               req.ClientKey,
			ExpectedWorkItemVersion: req.ExpectedWorkItemVersion,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, toTaskCommentDTO(comment))
	})
}
