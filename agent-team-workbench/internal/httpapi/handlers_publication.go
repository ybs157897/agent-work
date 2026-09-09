package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type publicationDraftCreateRequest struct {
	ExpectedVersion int      `json:"expected_version"`
	Revision        int64    `json:"revision"`
	ItemIDs         []string `json:"item_ids"`
	Title           string   `json:"title"`
	ClientKey       string   `json:"client_key"`
}

type publicationPublishRequest struct {
	ExpectedVersion int    `json:"expected_version"`
	ClientKey       string `json:"client_key"`
}

type publicationRecheckRequest struct {
	ExpectedVersion int `json:"expected_version"`
}

type publicationDraftDTO struct {
	DraftID            string           `json:"draft_id"`
	WorkspaceID        string           `json:"workspace_id"`
	ChatID             string           `json:"chat_id"`
	AgentID            string           `json:"agent_id"`
	AnalysisRevision   int64            `json:"analysis_revision"`
	ItemIDs            []string         `json:"item_ids"`
	ConfirmationIDs    []string         `json:"confirmation_ids"`
	SourceDependencies []map[string]any `json:"source_dependencies"`
	Title              string           `json:"title"`
	Description        string           `json:"description"`
	AcceptanceCriteria []string         `json:"acceptance_criteria"`
	ContextSnapshotID  string           `json:"context_snapshot_id"`
	ProjectBaseline    map[string]any   `json:"project_baseline"`
	Fingerprint        string           `json:"fingerprint"`
	Status             string           `json:"status"`
	TaskID             string           `json:"task_id,omitempty"`
	ClientKey          string           `json:"client_key"`
	Version            int              `json:"version"`
}

func toPublicationDraftDTO(d *domain.TaskPublicationDraft) publicationDraftDTO {
	out := publicationDraftDTO{DraftID: d.ID, WorkspaceID: d.WorkspaceID, ChatID: d.ChatWorkItemID, AgentID: d.AgentProfileID, AnalysisRevision: d.AnalysisRevision, Title: d.Title, Description: d.Description, ContextSnapshotID: d.ContextSnapshotID, Fingerprint: d.Fingerprint, Status: string(d.Status), TaskID: d.TaskID, ClientKey: d.ClientKey, Version: d.Version}
	_ = json.Unmarshal([]byte(d.ItemIDsJSON), &out.ItemIDs)
	_ = json.Unmarshal([]byte(d.ConfirmationIDsJSON), &out.ConfirmationIDs)
	_ = json.Unmarshal([]byte(d.SourceDependenciesJSON), &out.SourceDependencies)
	_ = json.Unmarshal([]byte(d.AcceptanceCriteriaJSON), &out.AcceptanceCriteria)
	_ = json.Unmarshal([]byte(d.BaselineJSON), &out.ProjectBaseline)
	return out
}

// publicationProblemBytes keeps a storage or other unclassified failure out
// of the idempotency result cache.  The generic command mapper predates the
// publication path and maps its fallback to 422; publication writes must
// release their claim on such failures so the same key can retry after the
// infrastructure recovers.
func publicationProblemBytes(err error) (int, []byte) {
	status, body := problemBytes(err)
	if status != http.StatusUnprocessableEntity {
		return status, body
	}
	var problem Problem
	if json.Unmarshal(body, &problem) != nil || problem.Code != "domain_error" {
		return status, body
	}
	return renderRetryableProblem(http.StatusInternalServerError, "internal", "Internal error",
		"发布命令因基础设施故障未完成，请使用相同 Idempotency-Key 重试")
}

func (s *Server) handleCreatePublicationDraft(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("work_item_id")
	s.idempotent(w, r, chatID, func() (int, []byte) {
		var req publicationDraftCreateRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		view, replayed, err := s.svc.CreatePublicationDraft(r.Context(), application.CreatePublicationDraftParams{ChatWorkItemID: chatID, ExpectedVersion: req.ExpectedVersion, Revision: req.Revision, ItemIDs: req.ItemIDs, Title: req.Title, ClientKey: req.ClientKey})
		if err != nil {
			return publicationProblemBytes(err)
		}
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		return renderJSON(w, r, http.StatusOK, toPublicationDraftDTO(view.Draft))
	})
}

func (s *Server) handleListPublicationDrafts(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	drafts, err := s.svc.ListPublicationDrafts(r.Context(), r.PathValue("work_item_id"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]publicationDraftDTO, 0, len(drafts))
	for _, draft := range drafts {
		items = append(items, toPublicationDraftDTO(draft))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetPublicationDraft(w http.ResponseWriter, r *http.Request) {
	view, err := s.svc.GetPublicationDraft(r.Context(), r.PathValue("work_item_id"), r.PathValue("draft_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPublicationDraftDTO(view.Draft))
}

func (s *Server) handleRecheckPublicationDraft(w http.ResponseWriter, r *http.Request) {
	chatID, draftID := r.PathValue("work_item_id"), r.PathValue("draft_id")
	s.idempotent(w, r, chatID+":"+draftID, func() (int, []byte) {
		var req publicationRecheckRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		view, err := s.svc.RecheckPublicationDraft(r.Context(), chatID, draftID, req.ExpectedVersion)
		if err != nil {
			return publicationProblemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toPublicationDraftDTO(view.Draft))
	})
}

func (s *Server) handlePublishPublicationDraft(w http.ResponseWriter, r *http.Request) {
	chatID, draftID := r.PathValue("work_item_id"), r.PathValue("draft_id")
	s.idempotent(w, r, chatID+":"+draftID, func() (int, []byte) {
		var req publicationPublishRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		task, err := s.svc.PublishPublicationDraft(r.Context(), chatID, draftID, req.ExpectedVersion, req.ClientKey)
		if err != nil {
			return publicationProblemBytes(err)
		}
		draft, draftErr := s.svc.GetPublicationDraft(r.Context(), chatID, draftID)
		if draftErr != nil {
			return publicationProblemBytes(draftErr)
		}
		return renderJSON(w, r, http.StatusOK, map[string]any{"draft": toPublicationDraftDTO(draft.Draft), "task": s.enrichWorkItem(r, task)})
	})
}
