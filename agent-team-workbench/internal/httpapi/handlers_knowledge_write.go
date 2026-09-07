package httpapi

import (
	"fmt"
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/security"
)

func (s *Server) registerKnowledgeWriteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/knowledge/items/{item_id}/repeal", s.guard(security.PermAgentWrite, s.handleKnowledgeRepeal))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/config", s.guard(security.PermRead, s.handleKnowledgeConfig))
	mux.HandleFunc("PATCH /api/v1/workspaces/{workspace_id}/knowledge/config", s.guard(security.PermAgentWrite, s.handleKnowledgeConfigure))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/knowledge/submissions", s.guard(security.PermAgentWrite, s.handleKnowledgeSubmit))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/submissions", s.guard(security.PermAgentWrite, s.handleKnowledgeSubmissions))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/submissions/{submission_id}", s.guard(security.PermAgentWrite, s.handleKnowledgeSubmission))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/knowledge/submissions/{submission_id}/curate", s.guard(security.PermAgentWrite, s.handleKnowledgeCurate))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/knowledge/submissions/{submission_id}/publish", s.guard(security.PermAgentWrite, s.handleKnowledgePublish))
	mux.HandleFunc("POST /api/v1/knowledge-agent/runs/{run_id}/submissions", s.handleKnowledgeAgentSubmit)
}

func (s *Server) handleKnowledgeRepeal(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	actor, err := s.knowledgeSubmissionActor(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.idempotent(w, r, ws, func() (int, []byte) {
		var req struct {
			ExpectedVersion int64  `json:"expected_version"`
			Reason          string `json:"reason"`
		}
		if err := decodeBody(r, &req); err != nil {
			return problemBytes(err)
		}
		item, err := s.svc.RepealKnowledgeItem(r.Context(), ws, actor, r.PathValue("item_id"), req.ExpectedVersion, req.Reason)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, item)
	})
}

func (s *Server) handleKnowledgeConfig(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	cfg, err := s.svc.GetKnowledgeLibrarianConfig(r.Context(), ws)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleKnowledgeConfigure(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.idempotent(w, r, ws, func() (int, []byte) {
		var req struct {
			ExpectedVersion  int     `json:"expected_version"`
			LibrarianAgentID *string `json:"librarian_agent_id,omitempty"`
			Enabled          *bool   `json:"enabled,omitempty"`
			AutoCollect      *bool   `json:"auto_collect,omitempty"`
		}
		if err := decodeBody(r, &req); err != nil {
			return problemBytes(err)
		}
		current, err := s.svc.GetKnowledgeLibrarianConfig(r.Context(), ws)
		if err != nil {
			return problemBytes(err)
		}
		if current.Version != req.ExpectedVersion {
			return problemBytes(domain.ErrVersionConflict)
		}
		cfg := *current
		cfg.WorkspaceID = ws
		cfg.Version = req.ExpectedVersion
		if req.LibrarianAgentID != nil {
			cfg.LibrarianAgentID = *req.LibrarianAgentID
		}
		if req.Enabled != nil {
			cfg.Enabled = *req.Enabled
		}
		if req.AutoCollect != nil {
			cfg.AutoCollect = *req.AutoCollect
		}
		out, err := s.svc.ConfigureKnowledgeLibrarian(r.Context(), ws, cfg)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, out)
	})
}

func (s *Server) handleKnowledgeSubmit(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.idempotent(w, r, ws, func() (int, []byte) {
		var req domain.KnowledgeSubmitCandidate
		if err := decodeBody(r, &req); err != nil {
			return problemBytes(err)
		}
		req.WorkspaceID = ws
		if req.ClientKey == "" {
			req.ClientKey = r.Header.Get("Idempotency-Key")
		}
		if req.AgentID == "" {
			return problemBytes(fmt.Errorf("%w: choose the source Agent", domain.ErrValidation))
		}
		out, err := s.svc.SubmitKnowledgeCandidate(r.Context(), req)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, out)
	})
}

// Management commands may inspect one submitted Agent's evidence. The role
// check happens before selecting that scope; Run callers never use this path.
func (s *Server) knowledgeSubmissionActor(r *http.Request) (string, error) {
	if agent := r.URL.Query().Get("agent_id"); agent != "" {
		_, actor, err := s.knowledgeReadIdentity(r)
		return actor, err
	}
	cfg, err := s.svc.GetKnowledgeLibrarianConfig(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		return "", err
	}
	if cfg.LibrarianAgentID == "" {
		return "human:shared", nil
	}
	return cfg.LibrarianAgentID, nil
}

func (s *Server) handleKnowledgeSubmissions(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	actor, err := s.knowledgeSubmissionActor(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	status := domain.KnowledgeSubmissionStatus(r.URL.Query().Get("status"))
	if status != "" && !status.Valid() {
		fail(w, r, fmt.Errorf("%w: invalid submission status", domain.ErrValidation))
		return
	}
	items, err := s.svc.ListKnowledgeSubmissions(r.Context(), ws, actor, status, 201)
	if err != nil {
		fail(w, r, err)
		return
	}
	if items == nil {
		items = []*domain.KnowledgeSubmission{}
	}
	truncated := len(items) > 200
	if truncated {
		items = items[:200]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

func (s *Server) handleKnowledgeSubmission(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	actor, err := s.knowledgeSubmissionActor(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	item, err := s.svc.GetKnowledgeSubmission(r.Context(), ws, actor, r.PathValue("submission_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleKnowledgeCurate(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	actor, err := s.knowledgeSubmissionActor(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.idempotent(w, r, ws, func() (int, []byte) {
		job, err := s.svc.StartKnowledgeCuration(r.Context(), application.StartKnowledgeCurationParams{WorkspaceID: ws, RequestingAgentID: actor, SubmissionID: r.PathValue("submission_id"), ClientKey: r.Header.Get("Idempotency-Key")})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, job)
	})
}

func (s *Server) handleKnowledgePublish(w http.ResponseWriter, r *http.Request) {
	ws, _, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	actor, err := s.knowledgeSubmissionActor(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.idempotent(w, r, ws, func() (int, []byte) {
		out, err := s.svc.PublishKnowledgeSubmission(r.Context(), ws, actor, r.PathValue("submission_id"))
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, out)
	})
}

func (s *Server) handleKnowledgeAgentSubmit(w http.ResponseWriter, r *http.Request) {
	run, ok := s.knowledgeAgentRun(w, r)
	if !ok {
		return
	}
	var req domain.KnowledgeSubmitCandidate
	if err := decodeBody(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	// Any attempted owner/workspace/Run impersonation is discarded in favor of
	// the bearer-bound Run identity before application validation and storage.
	req.WorkspaceID = run.WorkspaceID
	req.AgentID = run.AgentProfileID
	req.RunID = run.ID
	req.WorkItemID = run.WorkItemID
	if req.ClientKey == "" {
		req.ClientKey = r.Header.Get("Idempotency-Key")
	}
	if req.ClientKey == "" {
		fail(w, r, fmt.Errorf("%w: client_key required", domain.ErrValidation))
		return
	}
	req.ClientKey = "run:" + run.ID + ":" + req.ClientKey
	out, err := s.svc.SubmitKnowledgeCandidate(r.Context(), req)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
