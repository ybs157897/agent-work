package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/security"
)

func (s *Server) registerKnowledgeRoutes(mux *http.ServeMux) {
	s.registerKnowledgeJobRoutes(mux)
	s.registerKnowledgeWriteRoutes(mux)
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/items", s.guard(security.PermRead, s.handleKnowledgeItems))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/items/{item_id}", s.guard(security.PermRead, s.handleKnowledgeItem))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/items/{item_id}/versions", s.guard(security.PermRead, s.handleKnowledgeVersions))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/items/{item_id}/versions/{version}", s.guard(security.PermRead, s.handleKnowledgeVersion))
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/knowledge/items/{item_id}/relations", s.guard(security.PermRead, s.handleKnowledgeRelations))
	// Agent endpoints deliberately do not inherit the local human/demo role.
	mux.HandleFunc("GET /api/v1/knowledge-agent/runs/{run_id}/items/{item_id}", s.handleKnowledgeAgentRead)
	mux.HandleFunc("GET /api/v1/knowledge-agent/runs/{run_id}/items/{item_id}/versions/{version}", s.handleKnowledgeAgentRead)
}

func (s *Server) knowledgeReadIdentity(r *http.Request) (string, string, error) {
	ws := r.PathValue("workspace_id")
	if _, err := s.store.Workspaces().Get(r.Context(), ws); err != nil {
		return "", "", err
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentID == "" {
		return ws, "human:shared", nil
	}
	// The desktop owner may inspect an Agent's view. A read-only viewer cannot
	// obtain a private view by supplying an arbitrary Agent ID in the URL.
	if !security.Allow(s.demoRole, security.PermAgentWrite) {
		return "", "", fmt.Errorf("%w: Agent private knowledge view requires management permission", domain.ErrValidation)
	}
	agent, err := s.store.Agents().Get(r.Context(), agentID)
	if err != nil || agent.WorkspaceID != ws {
		return "", "", domain.ErrNotFound
	}
	return ws, agentID, nil
}

func (s *Server) handleKnowledgeItems(w http.ResponseWriter, r *http.Request) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	status := domain.KnowledgeStatus(r.URL.Query().Get("status"))
	if status == "" {
		status = domain.KnowledgeStatusEffective
	}
	if !status.Valid() {
		fail(w, r, fmt.Errorf("%w: invalid knowledge status", domain.ErrValidation))
		return
	}
	if status != domain.KnowledgeStatusEffective && !security.Allow(s.demoRole, security.PermAgentWrite) {
		fail(w, r, domain.ErrNotFound)
		return
	}
	options := domain.KnowledgeListOptions{Status: status, Kind: r.URL.Query().Get("kind"), Visibility: domain.KnowledgeVisibility(r.URL.Query().Get("visibility")), AfterID: r.URL.Query().Get("cursor"), Limit: 100, Query: r.URL.Query().Get("q")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n < 1 || n > 200 {
			fail(w, r, fmt.Errorf("%w: limit must be 1..200", domain.ErrValidation))
			return
		}
		options.Limit = n
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("scope")); raw != "" {
		if strings.HasPrefix(raw, "{") {
			if err := json.Unmarshal([]byte(raw), &options.Scope); err != nil {
				fail(w, r, fmt.Errorf("%w: scope must be a JSON object", domain.ErrValidation))
				return
			}
		} else if key, value, ok := strings.Cut(raw, "="); ok && strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			options.Scope = domain.KnowledgeScope{strings.TrimSpace(key): strings.TrimSpace(value)}
		} else {
			fail(w, r, fmt.Errorf("%w: scope uses key=value or a JSON object", domain.ErrValidation))
			return
		}
	}
	items, nextCursor, err := s.store.Knowledge().ListVisibleItemsPage(r.Context(), ws, agent, options)
	if err != nil {
		fail(w, r, err)
		return
	}
	if items == nil {
		items = []*domain.KnowledgeItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nullableString(nextCursor)})
}

func (s *Server) handleKnowledgeItem(w http.ResponseWriter, r *http.Request) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.writeKnowledgeBundle(w, r, ws, agent)
}

func (s *Server) handleKnowledgeVersions(w http.ResponseWriter, r *http.Request) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	versions, err := s.store.Knowledge().ListVersions(r.Context(), ws, agent, r.PathValue("item_id"), "")
	if err != nil {
		fail(w, r, err)
		return
	}
	if versions == nil {
		versions = []*domain.KnowledgeVersion{}
	}
	if !security.Allow(s.demoRole, security.PermAgentWrite) {
		visible := make([]*domain.KnowledgeVersion, 0, len(versions))
		for _, v := range versions {
			if v.Status != domain.KnowledgeStatusCandidate && v.Status != domain.KnowledgeStatusDraft {
				visible = append(visible, v)
			}
		}
		versions = visible
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": versions})
}

func (s *Server) handleKnowledgeVersion(w http.ResponseWriter, r *http.Request) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	s.writeKnowledgeBundle(w, r, ws, agent)
}

func (s *Server) writeKnowledgeBundle(w http.ResponseWriter, r *http.Request, ws, agent string) {
	item, err := s.store.Knowledge().GetItem(r.Context(), ws, agent, r.PathValue("item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	versionID := item.CurrentVersionID
	if requested := r.PathValue("version"); requested != "" {
		if strings.HasPrefix(requested, domain.PrefixKnowledgeVersion) {
			versionID = requested
		} else {
			n, parseErr := strconv.ParseInt(requested, 10, 64)
			if parseErr != nil || n < 1 {
				fail(w, r, fmt.Errorf("%w: version must be a positive number or version ID", domain.ErrValidation))
				return
			}
			versions, listErr := s.store.Knowledge().ListVersions(r.Context(), ws, agent, item.ID, "")
			if listErr != nil {
				fail(w, r, listErr)
				return
			}
			versionID = ""
			for _, v := range versions {
				if v.Version == n {
					versionID = v.ID
					break
				}
			}
		}
	}
	if versionID == "" {
		fail(w, r, domain.ErrNotFound)
		return
	}
	version, err := s.store.Knowledge().GetVersion(r.Context(), ws, agent, versionID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if version.ItemID != item.ID {
		fail(w, r, domain.ErrNotFound)
		return
	}
	if (version.Status == domain.KnowledgeStatusCandidate || version.Status == domain.KnowledgeStatusDraft) && (r.PathValue("run_id") != "" || !security.Allow(s.demoRole, security.PermAgentWrite)) {
		fail(w, r, domain.ErrNotFound)
		return
	}
	sources, err := s.store.Knowledge().ListVersionSources(r.Context(), ws, agent, versionID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if sources == nil {
		sources = []*domain.KnowledgeSource{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "version": version, "sources": sources})
}

func (s *Server) handleKnowledgeRelations(w http.ResponseWriter, r *http.Request) {
	ws, agent, err := s.knowledgeReadIdentity(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	direction := domain.KnowledgeRelationDirection(r.URL.Query().Get("direction"))
	if direction == "" {
		direction = domain.KnowledgeRelationBoth
	}
	if !direction.Valid() {
		fail(w, r, fmt.Errorf("%w: invalid relationship direction", domain.ErrValidation))
		return
	}
	items, err := s.store.Knowledge().ListRelations(r.Context(), ws, agent, r.PathValue("item_id"), direction, 200)
	if err != nil {
		fail(w, r, err)
		return
	}
	if items == nil {
		items = []*domain.KnowledgeRelation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) knowledgeAgentRun(w http.ResponseWriter, r *http.Request) (*domain.ExecutionRun, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Unauthorized", Status: http.StatusUnauthorized, Code: "knowledge_access_denied", Detail: "缺少有效的Run知识访问凭据"})
		return nil, false
	}
	run, err := s.svc.AuthenticateKnowledgeRun(r.Context(), r.PathValue("run_id"), strings.TrimPrefix(header, "Bearer "))
	if err != nil {
		writeProblem(w, r, Problem{Type: "about:blank", Title: "Unauthorized", Status: http.StatusUnauthorized, Code: "knowledge_access_denied", Detail: "知识访问凭据无效或Run已结束"})
		return nil, false
	}
	return run, true
}

func (s *Server) handleKnowledgeAgentRead(w http.ResponseWriter, r *http.Request) {
	run, ok := s.knowledgeAgentRun(w, r)
	if !ok {
		return
	}
	s.writeKnowledgeBundle(w, r, run.WorkspaceID, run.AgentProfileID)
}
