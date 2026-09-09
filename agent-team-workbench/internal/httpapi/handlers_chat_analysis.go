package httpapi

import (
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/application"
)

type chatAnalysisAnswerRequest struct {
	ExpectedVersion   int      `json:"expected_version"`
	Revision          int64    `json:"revision"`
	QuestionID        string   `json:"question_id"`
	SelectedOptionIDs []string `json:"selected_option_ids"`
	Text              string   `json:"text"`
	Disposition       string   `json:"disposition"`
	ClientKey         string   `json:"client_key"`
}

func (s *Server) handleGetChatAnalysis(w http.ResponseWriter, r *http.Request) {
	view, err := s.svc.GetChatAnalysis(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toChatAnalysisDTO(view))
}

func (s *Server) handleSaveChatAnalysisAnswer(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("work_item_id")
	s.idempotent(w, r, chatID, func() (int, []byte) {
		var req chatAnalysisAnswerRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		view, replayed, err := s.svc.SaveChatAnalysisAnswer(r.Context(), application.SaveChatAnalysisAnswerParams{
			ChatWorkItemID: chatID, ExpectedVersion: req.ExpectedVersion, Revision: req.Revision,
			QuestionID: req.QuestionID, SelectedOptionIDs: req.SelectedOptionIDs, Text: req.Text,
			Disposition: req.Disposition, ClientKey: req.ClientKey,
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
