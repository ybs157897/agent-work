package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
)

func (s *Server) handleRunChanges(w http.ResponseWriter, r *http.Request) {
	c, err := s.svc.RunChanges(r.Context(), r.PathValue("run_id"))
	if err != nil && !errors.Is(err, application.ErrRunChangesUnavailable) {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}
func (s *Server) handleRunChangeDiff(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	d, err := s.svc.RunChangeDiff(r.Context(), r.PathValue("run_id"), path)
	if err != nil {
		if errors.Is(err, application.ErrRunChangesInvalid) {
			writeProblem(w, r, Problem{Type: "https://workbench.example/problems/validation", Title: "Validation failed", Status: http.StatusBadRequest, Code: "invalid_change_path", Detail: err.Error()})
			return
		}
		if errors.Is(err, application.ErrRunChangesUnavailable) {
			writeProblem(w, r, Problem{Type: "https://workbench.example/problems/unavailable", Title: "Changes unavailable", Status: http.StatusNotFound, Code: "changes_unavailable", Detail: err.Error()})
			return
		}
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
func (s *Server) handleRevertRunChanges(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.IdempotencyKey == "" {
		writeProblem(w, r, Problem{Type: "https://workbench.example/problems/validation", Title: "Validation failed", Status: http.StatusBadRequest, Code: "idempotency_key_required"})
		return
	}
	c, err := s.svc.RevertRunChanges(r.Context(), r.PathValue("run_id"), req.IdempotencyKey)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, application.ErrRunChangesConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, application.ErrRunChangesUnavailable) {
			status = http.StatusNotFound
		}
		writeProblem(w, r, Problem{Type: "https://workbench.example/problems/changes", Title: "Changes command failed", Status: status, Code: "changes_command_failed", Detail: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, c)
}
