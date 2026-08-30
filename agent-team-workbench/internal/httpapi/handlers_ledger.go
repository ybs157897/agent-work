// handlers_ledger.go 会话元模型 S2/S4 的 HTTP 面：决策原话写入与列表、
// FTS 检索端点（rolling_digest 随 work item 详情响应携带，见 handleGetWorkItem）。
package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// handleSearch GET /workspaces/{id}/search：FTS 检索（S4）。q 为空/纯符号返回
// 空 items（FTS 语法敏感，不 500）；record_kind 缺省或 task；work_item_id/kind
// 可选过滤；limit 1-100 缺省 20。Chat 记录不属于任务检索面。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	if _, err := s.store.Workspaces().Get(r.Context(), wsID); err != nil {
		fail(w, r, err)
		return
	}
	if raw := r.URL.Query().Get("record_kind"); raw != "" && raw != string(domain.RecordKindTask) {
		fail(w, r, fmt.Errorf("%w: record_kind 仅支持 task", domain.ErrValidation))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.Search().Search(r.Context(), wsID,
		r.URL.Query().Get("q"), r.URL.Query().Get("work_item_id"), r.URL.Query().Get("kind"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]searchItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toSearchItemDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleRecordDecision POST /work-items/{id}/decisions（写命令，幂等键必带）：
// quote 必填（trim 后非空），source_run_id 可选且必须属于该 work item。
func (s *Server) handleRecordDecision(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		wi, err := s.store.WorkItems().Get(r.Context(), wiID)
		if err != nil {
			return problemBytes(err)
		}
		if err := requireTaskWorkItemHTTP(wi); err != nil {
			return problemBytes(err)
		}
		var req createDecisionRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		entry, err := s.svc.RecordDecision(r.Context(), wiID, application.RecordDecisionParams{
			Quote: req.Quote, SourceRunID: req.SourceRunID, SourceRef: req.SourceRef,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, toDecisionEntryDTO(entry))
	})
}

// handleListWorkItemDecisions GET /work-items/{id}/decisions：台账决策原话列表
// （创建时间升序；只读投影，冷启动加载，增量走 decision.created 事件）。
func (s *Server) handleListWorkItemDecisions(w http.ResponseWriter, r *http.Request) {
	wi, err := s.store.WorkItems().Get(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := requireTaskWorkItemHTTP(wi); err != nil {
		fail(w, r, err)
		return
	}
	entries, err := s.svc.DecisionsByWorkItem(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]decisionEntryDTO, 0, len(entries))
	for _, e := range entries {
		items = append(items, toDecisionEntryDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
