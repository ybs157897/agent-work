package httpapi

import "net/http"

// handleGetGovernanceMetrics exposes the Service-owned governance metrics read
// model. The handler does not aggregate events or maintain a second counter
// store; workspace validation and canonical replay belong to application.Service.
func (s *Server) handleGetGovernanceMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.svc.GetGovernanceMetrics(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}
