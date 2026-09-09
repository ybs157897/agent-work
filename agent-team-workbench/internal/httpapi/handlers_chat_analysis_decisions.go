package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/chatanalysis"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type chatAnalysisDecisionRequest struct {
	ExpectedVersion int    `json:"expected_version"`
	Revision        int64  `json:"revision"`
	ItemID          string `json:"item_id"`
	Outcome         string `json:"outcome"`
	Conclusion      string `json:"conclusion"`
	Basis           string `json:"basis"`
	ProductVersion  string `json:"product_version"`
	ClientKey       string `json:"client_key"`
}

type chatAnalysisRecheckRequest struct {
	ExpectedVersion int `json:"expected_version"`
}

type chatAnalysisDecisionHistoryDTO struct {
	ID                 string           `json:"id"`
	WorkspaceID        string           `json:"workspace_id"`
	ChatID             string           `json:"chat_id"`
	AgentID            string           `json:"agent_id"`
	Revision           int64            `json:"revision"`
	ItemID             string           `json:"item_id"`
	Outcome            string           `json:"outcome"`
	Conclusion         string           `json:"conclusion"`
	Basis              string           `json:"basis"`
	ProductVersion     string           `json:"product_version"`
	ItemFingerprint    string           `json:"item_fingerprint"`
	SourceDependencies []map[string]any `json:"source_dependencies,omitempty"`
	ActorID            string           `json:"actor_id"`
	ClientKey          string           `json:"client_key"`
	CreatedAt          string           `json:"created_at"`
}

type chatAnalysisReopenDTO struct {
	ID           string `json:"id"`
	ItemID       string `json:"item_id"`
	FromRevision int64  `json:"from_revision"`
	ToRevision   int64  `json:"to_revision"`
	Reason       string `json:"reason"`
	CreatedAt    string `json:"created_at"`
}

type chatAnalysisRevisionDTO struct {
	Revision       int64                  `json:"revision"`
	RunID          string                 `json:"run_id"`
	DocumentDigest string                 `json:"document_digest"`
	Document       *chatanalysis.Document `json:"document,omitempty"`
	CreatedAt      string                 `json:"created_at"`
}

type chatAnalysisHistoryDTO struct {
	Revisions []chatAnalysisRevisionDTO        `json:"revisions"`
	Answers   []chatAnalysisAnswerDTO          `json:"answers"`
	Decisions []chatAnalysisDecisionHistoryDTO `json:"decisions"`
	Reopens   []chatAnalysisReopenDTO          `json:"reopens"`
}

func (s *Server) handleSaveChatAnalysisDecision(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("work_item_id")
	s.idempotent(w, r, chatID, func() (int, []byte) {
		var req chatAnalysisDecisionRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		view, replayed, err := s.svc.SaveChatAnalysisDecision(r.Context(), application.SaveChatAnalysisDecisionParams{
			ChatWorkItemID: chatID, ExpectedVersion: req.ExpectedVersion, Revision: req.Revision,
			ItemID: req.ItemID, Outcome: domain.ChatAnalysisDecisionOutcome(req.Outcome),
			Conclusion: req.Conclusion, Basis: req.Basis, ProductVersion: req.ProductVersion, ClientKey: req.ClientKey,
			ActorID: "user_demo",
		})
		if err != nil {
			return problemBytes(err)
		}
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		return renderJSON(w, r, http.StatusOK, toChatAnalysisDTO(view))
	})
}

func (s *Server) handleGetChatAnalysisHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			limit = value
		}
	}
	history, err := s.svc.ChatAnalysisHistory(r.Context(), r.PathValue("work_item_id"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := chatAnalysisHistoryDTO{Revisions: make([]chatAnalysisRevisionDTO, 0, len(history.Revisions)), Answers: make([]chatAnalysisAnswerDTO, 0, len(history.Answers)), Decisions: make([]chatAnalysisDecisionHistoryDTO, 0, len(history.Decisions)), Reopens: make([]chatAnalysisReopenDTO, 0, len(history.Reopens))}
	for _, revision := range history.Revisions {
		item := chatAnalysisRevisionDTO{Revision: revision.Revision, RunID: revision.RunID, DocumentDigest: revision.DocumentDigest, CreatedAt: revision.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")}
		var document chatanalysis.Document
		if json.Unmarshal([]byte(revision.DocumentJSON), &document) == nil {
			item.Document = &document
		}
		out.Revisions = append(out.Revisions, item)
	}
	for _, answer := range history.Answers {
		out.Answers = append(out.Answers, toChatAnalysisAnswerDTO(answer))
	}
	for _, decision := range history.Decisions {
		var dependencies []map[string]any
		_ = json.Unmarshal([]byte(decision.SourceDependenciesJSON), &dependencies)
		out.Decisions = append(out.Decisions, chatAnalysisDecisionHistoryDTO{ID: decision.ID, WorkspaceID: decision.WorkspaceID, ChatID: decision.ChatWorkItemID, AgentID: decision.AgentProfileID, Revision: decision.Revision, ItemID: decision.ItemID, Outcome: string(decision.Outcome), Conclusion: decision.Conclusion, Basis: decision.Basis, ProductVersion: decision.ProductVersion, ItemFingerprint: decision.ItemFingerprint, SourceDependencies: dependencies, ActorID: decision.ActorID, ClientKey: decision.ClientKey, CreatedAt: decision.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")})
	}
	for _, reopen := range history.Reopens {
		out.Reopens = append(out.Reopens, chatAnalysisReopenDTO{ID: reopen.ID, ItemID: reopen.ItemID, FromRevision: reopen.FromRevision, ToRevision: reopen.ToRevision, Reason: reopen.Reason, CreatedAt: reopen.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRecheckChatAnalysis(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("work_item_id")
	s.idempotent(w, r, chatID, func() (int, []byte) {
		var req chatAnalysisRecheckRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		view, err := s.svc.RecheckChatAnalysis(r.Context(), chatID, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toChatAnalysisDTO(view))
	})
}
