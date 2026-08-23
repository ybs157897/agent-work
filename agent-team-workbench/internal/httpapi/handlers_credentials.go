package httpapi

import (
	"net/http"
)

// handleGetProviderCredential 返回本地保存的 API Key（仅本机工作台使用）。
func (s *Server) handleGetProviderCredential(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/not-found", Title: "Resource not found",
			Status: http.StatusNotFound, Code: "credentials_disabled", Detail: modelRegistryDisabledDetail,
		})
		return
	}
	providerID := r.URL.Query().Get("provider_id")
	if providerID == "" {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/bad-request", Title: "Bad request",
			Status: http.StatusBadRequest, Code: "bad_request", Detail: "缺少 provider_id 参数",
		})
		return
	}
	apiKey, ok := s.credentials.Get(providerID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"api_key": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": apiKey})
}

type putProviderCredentialRequest struct {
	ProviderID string `json:"provider_id"`
	APIKey     string `json:"api_key"`
}

func (s *Server) handlePutProviderCredential(w http.ResponseWriter, r *http.Request) {
	if s.credentials == nil {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/not-found", Title: "Resource not found",
			Status: http.StatusNotFound, Code: "credentials_disabled", Detail: modelRegistryDisabledDetail,
		})
		return
	}
	var req putProviderCredentialRequest
	if err := decodeBody(r, &req); err != nil {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/bad-request", Title: "Bad request",
			Status: http.StatusBadRequest, Code: "bad_request", Detail: err.Error(),
		})
		return
	}
	if req.ProviderID == "" {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/bad-request", Title: "Bad request",
			Status: http.StatusBadRequest, Code: "bad_request", Detail: "provider_id 必填",
		})
		return
	}
	if err := s.credentials.Set(req.ProviderID, req.APIKey); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
