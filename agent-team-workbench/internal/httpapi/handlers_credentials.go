package httpapi

import (
	"net/http"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/modelconfig"
)

// maskSuffixMinLength：密钥至少长于此值才回显末 4 位，短密钥不外泄任何字符。
const maskSuffixMinLength = 8

// maskAPIKey 生成脱敏提示：仅在长度足够时保留末 4 位，length 为字符数。
// 任何情况下都不得返回完整明文（GET 凭据写后不可读）。
func maskAPIKey(key string) (hint string, length int) {
	runes := []rune(strings.TrimSpace(key))
	length = len(runes)
	if length > maskSuffixMinLength {
		hint = string(runes[length-4:])
	}
	return hint, length
}

// getProviderCredentialResponse 响应体只含脱敏提示，绝不携带 api_key 明文字段。
type getProviderCredentialResponse struct {
	Configured bool   `json:"configured"`
	MaskedHint string `json:"masked_hint,omitempty"`
	Length     int    `json:"length,omitempty"`
}

// handleGetProviderCredential 返回凭据配置状态（脱敏提示，写后不可读；仅本机工作台使用）。
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
	apiKey, ok, err := s.credentials.Get(providerID)
	if err != nil {
		// 读失败≠未配置：向上报错而不是伪装成空凭据。
		fail(w, r, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, getProviderCredentialResponse{Configured: false})
		return
	}
	hint, length := maskAPIKey(apiKey)
	writeJSON(w, http.StatusOK, getProviderCredentialResponse{Configured: true, MaskedHint: hint, Length: length})
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
	if s.models != nil {
		if providers, err := s.models.Providers(); err == nil {
			for _, p := range providers {
				if p.ID == req.ProviderID {
					_ = s.credentials.HydrateEnv([]modelconfig.ProviderDef{p})
					break
				}
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
