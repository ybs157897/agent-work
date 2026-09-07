package httpapi

import (
	"fmt"
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/security"
)

func (s *Server) registerKnowledgeJobRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/knowledge/inquiries", s.guard(security.PermRunControl, s.handleKnowledgeInquiry))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/jobs", s.guard(security.PermRead, s.handleKnowledgeJobs))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/jobs/{job_id}", s.guard(security.PermRead, s.handleKnowledgeJob))
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/knowledge/jobs/{job_id}/cancel", s.guard(security.PermRunControl, s.handleKnowledgeCancel))
	mux.HandleFunc("POST /api/v1/knowledge-agent/runs/{run_id}/inquiries", s.handleKnowledgeAgentInquiry)
	mux.HandleFunc("GET /api/v1/knowledge-agent/runs/{run_id}/jobs/{job_id}", s.handleKnowledgeAgentJob)
	mux.HandleFunc("POST /api/v1/knowledge-agent/runs/{run_id}/jobs/{job_id}/cancel", s.handleKnowledgeAgentCancel)
}

type knowledgeInquiryRequest struct {
	Question  string                    `json:"question"`
	Context   string                    `json:"context,omitempty"`
	Scope     domain.KnowledgeScope     `json:"scope,omitempty"`
	Budget    domain.KnowledgeJobBudget `json:"budget,omitempty"`
	ClientKey string                    `json:"client_key,omitempty"`
}

func (s *Server) handleKnowledgeInquiry(w http.ResponseWriter, r *http.Request) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.idempotent(w, r, ws, func() (int, []byte) {
		var req knowledgeInquiryRequest
		if err := decodeBody(r, &req); err != nil {
			return problemBytes(err)
		}
		if req.ClientKey == "" {
			req.ClientKey = r.Header.Get("Idempotency-Key")
		}
		job, err := s.svc.StartKnowledgeInquiry(r.Context(), application.StartKnowledgeInquiryParams{WorkspaceID: ws, RequestingAgentID: agent, Question: req.Question, Context: req.Context, Scope: req.Scope, Budget: req.Budget, ClientKey: req.ClientKey})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, job)
	})
}

func (s *Server) handleKnowledgeJobs(w http.ResponseWriter, r *http.Request) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	status := domain.KnowledgeJobStatus(r.URL.Query().Get("status"))
	if status != "" && !status.Valid() {
		fail(w, r, fmt.Errorf("%w: invalid job status", domain.ErrValidation))
		return
	}
	var jobs []*domain.KnowledgeJob
	if security.Allow(s.demoRole, security.PermAgentWrite) && r.URL.Query().Get("agent_id") == "" {
		jobs, err = s.store.KnowledgeJobs().List(r.Context(), ws, "", status, 101)
	} else {
		jobs, err = s.svc.ListKnowledgeJobs(r.Context(), ws, agent, status, 101)
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	truncated := len(jobs) > 100
	if truncated {
		jobs = jobs[:100]
	}
	if jobs == nil {
		jobs = []*domain.KnowledgeJob{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": jobs, "truncated": truncated})
}

func (s *Server) userKnowledgeJob(r *http.Request) (*domain.KnowledgeJob, error) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		return nil, err
	}
	job, err := s.svc.GetKnowledgeJob(r.Context(), r.PathValue("job_id"))
	if err != nil {
		return nil, err
	}
	if job.WorkspaceID != ws || (!security.Allow(s.demoRole, security.PermAgentWrite) && job.RequestingAgentID != agent) {
		return nil, domain.ErrNotFound
	}
	return job, nil
}

func (s *Server) handleKnowledgeJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.userKnowledgeJob(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleKnowledgeCancel(w http.ResponseWriter, r *http.Request) {
	job, err := s.userKnowledgeJob(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if _, err := s.svc.CancelKnowledgeJob(r.Context(), job.ID); err != nil {
		fail(w, r, err)
		return
	}
	job, err = s.svc.GetKnowledgeJob(r.Context(), job.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleKnowledgeAgentInquiry(w http.ResponseWriter, r *http.Request) {
	run, ok := s.knowledgeAgentRun(w, r)
	if !ok {
		return
	}
	var req knowledgeInquiryRequest
	if err := decodeBody(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.ClientKey == "" {
		req.ClientKey = r.Header.Get("Idempotency-Key")
	}
	if req.ClientKey == "" {
		fail(w, r, fmt.Errorf("%w: client_key required", domain.ErrValidation))
		return
	}
	job, err := s.svc.StartKnowledgeInquiry(r.Context(), application.StartKnowledgeInquiryParams{WorkspaceID: run.WorkspaceID, RequestingAgentID: run.AgentProfileID, SourceRunID: run.ID, Question: req.Question, Context: req.Context, Scope: req.Scope, Budget: req.Budget, ClientKey: "run:" + run.ID + ":" + req.ClientKey})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) agentKnowledgeJob(w http.ResponseWriter, r *http.Request) (*domain.KnowledgeJob, bool) {
	run, ok := s.knowledgeAgentRun(w, r)
	if !ok {
		return nil, false
	}
	job, err := s.svc.GetKnowledgeJob(r.Context(), r.PathValue("job_id"))
	if err != nil || job.WorkspaceID != run.WorkspaceID || job.RequestingAgentID != run.AgentProfileID || job.SourceRunID != run.ID {
		fail(w, r, domain.ErrNotFound)
		return nil, false
	}
	return job, true
}

func (s *Server) handleKnowledgeAgentJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.agentKnowledgeJob(w, r)
	if ok {
		writeJSON(w, http.StatusOK, job)
	}
}
func (s *Server) handleKnowledgeAgentCancel(w http.ResponseWriter, r *http.Request) {
	job, ok := s.agentKnowledgeJob(w, r)
	if !ok {
		return
	}
	if _, err := s.svc.CancelKnowledgeJob(r.Context(), job.ID); err != nil {
		fail(w, r, err)
		return
	}
	job, err := s.svc.GetKnowledgeJob(r.Context(), job.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
