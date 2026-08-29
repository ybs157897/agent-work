// handlers_ledger.go 会话元模型 S2 任务台账的 HTTP 面：决策原话写入与列表
// （rolling_digest 随 work item 详情响应携带，见 enrichWorkItem）。
package httpapi

import (
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
)

// handleRecordDecision POST /work-items/{id}/decisions（写命令，幂等键必带）：
// quote 必填（trim 后非空），source_run_id 可选且必须属于该 work item。
func (s *Server) handleRecordDecision(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
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
