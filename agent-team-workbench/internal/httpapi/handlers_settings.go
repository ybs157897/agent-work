package httpapi

import (
	"log"
	"net/http"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ── RuntimeBinding / 模型配置（设置页）───────────────────────────────

type bindingDTO struct {
	ID              string            `json:"id"`
	RuntimeLabel    string            `json:"runtime_label"`
	AdapterID       string            `json:"adapter_id"`
	AdapterVersion  string            `json:"adapter_version"`
	ProviderVersion string            `json:"provider_version,omitempty"`
	Provider        string            `json:"provider"`
	Model           string            `json:"model"`
	CredentialRef   string            `json:"credential_ref,omitempty"` // 只有引用，绝不明文
	Capabilities    map[string]string `json:"capabilities"`
	Status          string            `json:"status"`
	Version         int               `json:"version"`
}

func toBindingDTO(b *domain.RuntimeBinding) bindingDTO {
	return bindingDTO{
		ID: b.ID, RuntimeLabel: b.RuntimeLabel, AdapterID: b.AdapterID,
		AdapterVersion: b.AdapterVersion, ProviderVersion: b.ProviderVersion,
		Provider: b.Provider, Model: b.Model, CredentialRef: b.CredentialRef,
		Capabilities: b.Capabilities, Status: string(b.Status), Version: b.Version,
	}
}

type createBindingRequest struct {
	RuntimeLabel  string `json:"runtime_label"`
	AdapterID     string `json:"adapter_id"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	CredentialRef string `json:"credential_ref"`
}

type patchBindingRequest struct {
	Provider        *string `json:"provider"`
	Model           *string `json:"model"`
	CredentialRef   *string `json:"credential_ref"`
	ExpectedVersion int     `json:"expected_version"`
}

func (s *Server) handleListBindings(w http.ResponseWriter, r *http.Request) {
	bindings, err := s.svc.RuntimeBindings(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]bindingDTO, 0, len(bindings))
	for _, b := range bindings {
		items = append(items, toBindingDTO(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateBinding(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	s.idempotent(w, r, wsID, func() (int, []byte) {
		var req createBindingRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		b, err := s.svc.CreateRuntimeBinding(r.Context(), wsID, application.CreateBindingParams{
			RuntimeLabel: req.RuntimeLabel, AdapterID: req.AdapterID,
			Provider: req.Provider, Model: req.Model, CredentialRef: req.CredentialRef,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, toBindingDTO(b))
	})
}

func (s *Server) handlePatchBinding(w http.ResponseWriter, r *http.Request) {
	bindingID := r.PathValue("runtime_binding_id")
	s.idempotent(w, r, bindingID, func() (int, []byte) {
		var req patchBindingRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		b, err := s.svc.UpdateRuntimeBinding(r.Context(), bindingID, application.BindingPatch{
			Provider: req.Provider, Model: req.Model,
			CredentialRef: req.CredentialRef, ExpectedVersion: req.ExpectedVersion,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toBindingDTO(b))
	})
}

func (s *Server) handleProbeBinding(w http.ResponseWriter, r *http.Request) {
	bindingID := r.PathValue("runtime_binding_id")
	s.idempotent(w, r, bindingID, func() (int, []byte) {
		b, result, err := s.svc.ProbeRuntimeBinding(r.Context(), bindingID)
		if err != nil {
			return problemBytes(err)
		}
		var errMsg any
		if result.Error != "" {
			errMsg = result.Error
		}
		return renderJSON(w, r, http.StatusAccepted, map[string]any{
			"runtime_binding_id": b.ID,
			"ok":                 result.OK,
			"provider_version":   b.ProviderVersion,
			"capabilities":       b.Capabilities,
			"error":              errMsg,
		})
	})
}

// ── Workspace PATCH ──────────────────────────────────────────────────

type patchWorkspaceRequest struct {
	Name            *string `json:"name"`
	Timezone        *string `json:"timezone"`
	ExpectedVersion int     `json:"expected_version"`
}

func (s *Server) handlePatchWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	s.idempotent(w, r, wsID, func() (int, []byte) {
		var req patchWorkspaceRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		ws, err := s.svc.UpdateWorkspace(r.Context(), wsID, req.Name, req.Timezone, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toWorkspaceDTO(ws))
	})
}

// ── AgentProfile GET / PATCH ─────────────────────────────────────────

type patchAgentRequest struct {
	Name              *string             `json:"name"`
	Role              *string             `json:"role"`
	Skills            []string            `json:"skills"`
	Instructions      *string             `json:"instructions"`
	RuntimePreference *runtimePrefDTO     `json:"runtime_preference"`
	ModelOverride     *domain.ModelRef    `json:"model_override"`
	Policy            *domain.AgentPolicy `json:"policy"`
	ExpectedVersion   int                 `json:"expected_version"`
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	a, err := s.svc.Agent(r.Context(), r.PathValue("agent_profile_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentDTO(a))
}

func (s *Server) handlePatchAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_profile_id")
	s.idempotent(w, r, agentID, func() (int, []byte) {
		var req patchAgentRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		patch := application.AgentPatch{
			Name: req.Name, Role: req.Role, Skills: req.Skills,
			Instructions: req.Instructions, ExpectedVersion: req.ExpectedVersion,
		}
		if req.RuntimePreference != nil {
			patch.RuntimePreference = &domain.RuntimePreference{
				Preferred:   req.RuntimePreference.Preferred,
				Fallbacks:   req.RuntimePreference.Fallbacks,
				Mode:        req.RuntimePreference.Mode,
				AgentPreset: req.RuntimePreference.AgentPreset,
			}
		}
		patch.ModelOverride = req.ModelOverride
		patch.Policy = req.Policy
		a, err := s.svc.UpdateAgent(r.Context(), agentID, patch)
		if err != nil {
			return problemBytes(err)
		}
		// 文件为真相源：DB 更新成功后回写 agents/<slug>/；失败只记日志不阻断（reload 可修复）。
		if s.agentCfg != nil {
			if err := s.agentCfg.WriteBackOne(r.Context(), a); err != nil {
				log.Printf("agent 配置回写文件失败（%s）: %v", a.ID, err)
			}
		}
		return renderJSON(w, r, http.StatusOK, toAgentDTO(a))
	})
}

// handleReloadAgentConfig 重新扫描 agents/ 目录并导入 DB 投影（文件为真相源）。
func (s *Server) handleReloadAgentConfig(w http.ResponseWriter, r *http.Request) {
	wsID := r.PathValue("workspace_id")
	s.idempotent(w, r, wsID, func() (int, []byte) {
		if s.agentCfg == nil {
			return renderProblem(http.StatusNotFound, "agent_config_disabled", "Agent config disabled", "未配置 agents/ 目录")
		}
		res, err := s.agentCfg.Import(r.Context(), wsID)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, res)
	})
}

// ── WorkItem PATCH（普通字段；status 走 commands）────────────────────

type patchWorkItemRequest struct {
	Title           *string `json:"title"`
	Description     *string `json:"description"`
	Priority        *string `json:"priority"`
	DueDate         *string `json:"due_date"`
	ExpectedVersion int     `json:"expected_version"`
}

func (s *Server) handlePatchWorkItem(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req patchWorkItemRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		patch := application.WorkItemFieldPatch{
			Title: req.Title, Description: req.Description, ExpectedVersion: req.ExpectedVersion,
		}
		if req.Priority != nil {
			p := domain.Priority(*req.Priority)
			patch.Priority = &p
		}
		if req.DueDate != nil {
			d, err := time.Parse("2006-01-02", *req.DueDate)
			if err != nil {
				return renderProblem(http.StatusBadRequest, "bad_request", "Invalid due_date", "due_date 必须为 YYYY-MM-DD")
			}
			patch.DueDate = &d
		}
		wi, err := s.svc.UpdateWorkItemFields(r.Context(), wiID, patch)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, s.enrichWorkItem(r, wi))
	})
}

// ── Run input / resume ───────────────────────────────────────────────

type runInputRequest struct {
	Instruction string `json:"instruction"`
}

func (s *Server) handleRunInput(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	s.idempotent(w, r, runID, func() (int, []byte) {
		var req runInputRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if err := s.svc.SendRunInput(r.Context(), runID, req.Instruction); err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusAccepted, map[string]any{"accepted": true})
	})
}

func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("run_id")
	s.idempotent(w, r, runID, func() (int, []byte) {
		run, err := s.svc.ResumeRun(r.Context(), runID)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusAccepted, toRunDTO(run))
	})
}
