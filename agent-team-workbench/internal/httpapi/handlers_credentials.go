package httpapi

import (
	"context"
	"log"
	"net/http"

	"github.com/ybs/agent-team-workbench/internal/modelconfig"
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
	if s.models != nil {
		if providers, err := s.models.Providers(); err == nil {
			for _, p := range providers {
				if p.ID == req.ProviderID {
					_ = s.credentials.HydrateEnv([]modelconfig.ProviderDef{p})
					s.reprobeCodexBindings(r.Context())
					break
				}
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// reprobeCodexBindings 让模型页保存凭据后立即刷新 codex_local 健康状态，
// 无需用户再重启控制平面或手动进入设置页点击 Probe。
func (s *Server) reprobeCodexBindings(ctx context.Context) {
	workspaceIDs, err := s.store.Workspaces().ListIDs(ctx)
	if err != nil {
		log.Printf("保存模型凭据后读取 workspace 失败: %v", err)
		return
	}
	for _, workspaceID := range workspaceIDs {
		bindings, err := s.store.Bindings().List(ctx, workspaceID)
		if err != nil {
			log.Printf("保存模型凭据后读取 Runtime binding 失败（%s）: %v", workspaceID, err)
			continue
		}
		for _, binding := range bindings {
			if binding.AdapterID != "codex-appserver" {
				continue
			}
			if _, result, err := s.svc.ProbeRuntimeBinding(ctx, binding.ID); err != nil {
				log.Printf("保存模型凭据后 Probe %s 失败: %v", binding.RuntimeLabel, err)
			} else if !result.OK {
				log.Printf("保存模型凭据后 Probe %s unavailable: %s", binding.RuntimeLabel, result.Error)
			}
		}
	}
}
