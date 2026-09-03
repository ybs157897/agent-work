package httpapi

import (
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/modelconfig"
)

// ── 模型注册表（models/ 目录为真相源）─────────────────────────────────
// API Key 明文存于 models/credentials.local.yaml（见 provider-credentials 端点）。

const modelRegistryDisabledDetail = "未配置 models/ 注册表目录"

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if s.models == nil {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/not-found", Title: "Resource not found",
			Status: http.StatusNotFound, Code: "model_registry_disabled", Detail: modelRegistryDisabledDetail,
		})
		return
	}
	entries, err := s.models.List()
	if err != nil {
		fail(w, r, err)
		return
	}
	if entries == nil {
		entries = []*modelconfig.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": entries})
}

type upsertModelRequest struct {
	ID            string `json:"id"` // POST 可空（由 display_name 生成）；PUT 以 path 为准
	DisplayName   string `json:"display_name"`
	Category      string `json:"category"`
	Provider      string `json:"provider"`
	API           string `json:"api"`
	Model         string `json:"model"`
	ProviderID    string `json:"provider_id"`
	APIKeyEnv     string `json:"api_key_env"`
	BaseURL       string `json:"base_url"`
	ContextWindow int    `json:"context_window"`
	MaxTokens     int    `json:"max_tokens"`
	Notes         string `json:"notes"`
}

func (r *upsertModelRequest) entry() *modelconfig.Entry {
	return &modelconfig.Entry{
		ID: r.ID, DisplayName: r.DisplayName, Category: r.Category, ProviderID: r.ProviderID, Provider: r.Provider, API: r.API, Model: r.Model,
		APIKeyEnv: r.APIKeyEnv, BaseURL: r.BaseURL,
		ContextWindow: r.ContextWindow, MaxTokens: r.MaxTokens, Notes: r.Notes,
	}
}

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	s.idempotent(w, r, "models", func() (int, []byte) {
		if s.models == nil {
			return renderProblem(http.StatusNotFound, "model_registry_disabled", "Resource not found", modelRegistryDisabledDetail)
		}
		var req upsertModelRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		entry := req.entry()
		if entry.ID != "" {
			if existing, err := s.models.Get(entry.ID); err != nil {
				return problemBytes(err)
			} else if existing != nil {
				return renderProblem(http.StatusConflict, "model_exists", "Model already exists",
					"模型 id 已存在："+entry.ID)
			}
		}
		if err := s.models.Upsert(entry); err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, entry)
	})
}

func (s *Server) handleUpsertModel(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model_id")
	s.idempotent(w, r, "model:"+modelID, func() (int, []byte) {
		if s.models == nil {
			return renderProblem(http.StatusNotFound, "model_registry_disabled", "Resource not found", modelRegistryDisabledDetail)
		}
		var req upsertModelRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		entry := req.entry()
		entry.ID = modelID // PUT 以 path 为权威 id（全量替换语义）
		if err := s.models.Upsert(entry); err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, entry)
	})
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	modelID := r.PathValue("model_id")
	s.idempotent(w, r, "model:"+modelID, func() (int, []byte) {
		if s.models == nil {
			return renderProblem(http.StatusNotFound, "model_registry_disabled", "Resource not found", modelRegistryDisabledDetail)
		}
		if err := s.models.Delete(modelID); err != nil {
			return problemBytes(err)
		}
		return http.StatusNoContent, nil
	})
}
