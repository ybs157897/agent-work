package httpapi

import (
	"net/http"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type questionOptionDTO struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type questionItemDTO struct {
	ID               string              `json:"id"`
	Question         string              `json:"question"`
	Header           string              `json:"header,omitempty"`
	Body             string              `json:"body,omitempty"`
	Options          []questionOptionDTO `json:"options"`
	MultiSelect      bool                `json:"multi_select,omitempty"`
	AllowOther       bool                `json:"allow_other,omitempty"`
	OtherLabel       string              `json:"other_label,omitempty"`
	OtherDescription string              `json:"other_description,omitempty"`
}

type questionDTO struct {
	ID         string                   `json:"id"`
	RunID      string                   `json:"run_id"`
	WorkItemID string                   `json:"work_item_id"`
	SessionRef string                   `json:"session_ref"`
	ProviderID string                   `json:"provider_id"`
	AgentID    string                   `json:"agent_id,omitempty"`
	TurnID     int64                    `json:"turn_id,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
	Questions  []questionItemDTO        `json:"questions"`
	Status     domain.QuestionStatus    `json:"status"`
	Response   *domain.QuestionResponse `json:"response,omitempty"`
	CreatedAt  string                   `json:"created_at"`
	ResolvedAt *string                  `json:"resolved_at,omitempty"`
}

func toQuestionDTO(q *domain.QuestionRequest) questionDTO {
	items := make([]questionItemDTO, 0, len(q.Questions))
	for _, item := range q.Questions {
		options := make([]questionOptionDTO, 0, len(item.Options))
		for _, option := range item.Options {
			options = append(options, questionOptionDTO{ID: option.ID, Label: option.Label, Description: option.Description})
		}
		items = append(items, questionItemDTO{ID: item.ID, Question: item.Question, Header: item.Header, Body: item.Body, Options: options,
			MultiSelect: item.MultiSelect, AllowOther: item.AllowOther, OtherLabel: item.OtherLabel, OtherDescription: item.OtherDescription})
	}
	var resolvedAt *string
	if q.ResolvedAt != nil {
		value := q.ResolvedAt.UTC().Format(time.RFC3339Nano)
		resolvedAt = &value
	}
	return questionDTO{ID: q.ID, RunID: q.RunID, WorkItemID: q.WorkItemID, SessionRef: q.SessionRef, ProviderID: q.ProviderID,
		AgentID: q.AgentID, TurnID: q.TurnID, ToolCallID: q.ToolCallID, Questions: items, Status: q.Status,
		Response: q.Response, CreatedAt: q.CreatedAt.UTC().Format(time.RFC3339Nano), ResolvedAt: resolvedAt}
}

type resolveQuestionRequest struct {
	Answers map[string]domain.QuestionAnswer `json:"answers"`
	Method  string                           `json:"method,omitempty"`
	Note    string                           `json:"note,omitempty"`
}

func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	items, err := s.svc.Questions(r.Context(), r.PathValue("run_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]questionDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toQuestionDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleResolveQuestion(w http.ResponseWriter, r *http.Request) {
	runID, questionID := r.PathValue("run_id"), r.PathValue("question_id")
	s.idempotent(w, r, runID+":"+questionID, func() (int, []byte) {
		var req resolveQuestionRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		q, err := s.svc.ResolveQuestion(r.Context(), runID, questionID, domain.QuestionResponse{Answers: req.Answers, Method: req.Method, Note: req.Note}, "user_demo")
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toQuestionDTO(q))
	})
}

func (s *Server) handleDismissQuestion(w http.ResponseWriter, r *http.Request) {
	runID, questionID := r.PathValue("run_id"), r.PathValue("question_id")
	s.idempotent(w, r, runID+":"+questionID+":dismiss", func() (int, []byte) {
		q, err := s.svc.DismissQuestion(r.Context(), runID, questionID, "user_demo")
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toQuestionDTO(q))
	})
}
