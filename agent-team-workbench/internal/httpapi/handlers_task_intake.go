package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
	"github.com/ybs/agent-team-workbench/internal/taskintake"
)

const taskIntakeMaxBodyBytes = 1 << 20

type analyzeTaskIntakeRequest struct {
	Messages []taskintake.Message `json:"messages"`
	Draft    *taskintake.Draft    `json:"draft,omitempty"`
	ModelRef string               `json:"model_ref,omitempty"`
}

type analyzeTaskIntakeResponse struct {
	Reply     string                `json:"reply"`
	Questions []taskintake.Question `json:"questions"`
	Draft     *taskintake.Draft     `json:"draft"`
}

// taskIntakeConfigError is a safe configuration failure. It intentionally
// carries no provider response or credential path.
type taskIntakeConfigError struct {
	Code   string
	Detail string
}

func (e *taskIntakeConfigError) Error() string { return e.Detail }

func (s *Server) handleAnalyzeTaskIntake(w http.ResponseWriter, r *http.Request) {
	// This endpoint is intentionally outside idempotent(): it has no database
	// write and keeps the complete transcript in the client. Read the complete
	// bounded body before decoding; json.Decoder may otherwise stop after the
	// first value and let a large trailing payload bypass MaxBytesReader.
	body, readErr := io.ReadAll(io.LimitReader(r.Body, taskIntakeMaxBodyBytes+1))
	if readErr != nil || len(body) > taskIntakeMaxBodyBytes {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/bad-request", Title: "Invalid request body",
			Status: http.StatusBadRequest, Code: "bad_request", Detail: "request body is too large",
		})
		return
	}
	var req analyzeTaskIntakeRequest
	if err := decodeTaskIntakeBody(body, &req); err != nil {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/bad-request", Title: "Invalid request body",
			Status: http.StatusBadRequest, Code: "bad_request", Detail: err.Error(),
		})
		return
	}
	if err := taskintake.ValidateInput(req.Messages, req.Draft); err != nil {
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/analysis-invalid-input", Title: "Invalid analysis input",
			Status: http.StatusBadRequest, Code: taskintake.CodeInvalidInput, Detail: err.Error(),
		})
		return
	}
	model, err := s.resolveTaskIntakeModel(r.Context(), r.PathValue("workspace_id"), req.ModelRef)
	if err != nil {
		s.writeTaskIntakeError(w, r, err)
		return
	}
	if s.taskIntakeClient == nil {
		s.writeTaskIntakeError(w, r, &taskIntakeConfigError{
			Code: taskintake.CodeUnsupportedModel, Detail: "task intake analysis is not configured",
		})
		return
	}
	result, err := s.taskIntakeClient.Analyze(r.Context(), model, req.Messages, req.Draft)
	if err != nil {
		s.writeTaskIntakeError(w, r, err)
		return
	}
	questions := result.Questions
	if questions == nil {
		questions = []taskintake.Question{}
	}
	writeJSON(w, http.StatusOK, analyzeTaskIntakeResponse{Reply: result.Reply, Questions: questions, Draft: result.Draft})
}

func decodeTaskIntakeBody(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

// resolveTaskIntakeModel resolves an explicitly requested model first. When
// omitted, only an already persisted Coordinator ModelRef is consulted; the
// read path never calls EnsureConfig because analysis must not create a system
// Coordinator. A legacy provider/model pair may be used only when it matches
// exactly one registry entry.
func (s *Server) resolveTaskIntakeModel(ctx context.Context, workspaceID, requestedRef string) (taskintake.Model, error) {
	if _, err := s.store.Workspaces().Get(ctx, workspaceID); err != nil {
		return taskintake.Model{}, err
	}
	if s.models == nil {
		return taskintake.Model{}, &taskIntakeConfigError{
			Code: taskintake.CodeUnsupportedModel, Detail: "模型注册表未配置，无法进行任务需求分析；请在设置中配置任务分析使用的模型",
		}
	}
	ref := strings.TrimSpace(requestedRef)
	var configuredProvider, configuredModel string
	if ref == "" {
		config, err := s.store.TaskCoordinators().GetConfig(ctx, workspaceID)
		if err == nil {
			ref = strings.TrimSpace(config.ModelRef.Ref)
			configuredProvider = strings.TrimSpace(config.ModelRef.Provider)
			configuredModel = strings.TrimSpace(config.ModelRef.Model)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return taskintake.Model{}, err
		}
	}
	if ref == "" && configuredProvider != "" && configuredModel != "" {
		entries, err := s.models.List()
		if err != nil {
			return taskintake.Model{}, &taskIntakeConfigError{
				Code: taskintake.CodeUnsupportedModel, Detail: "模型注册表读取失败，无法进行任务需求分析；请稍后重试",
			}
		}
		var match *modelconfig.Entry
		for _, entry := range entries {
			if entry.Provider == configuredProvider && entry.Model == configuredModel {
				if match != nil {
					return taskintake.Model{}, &taskIntakeConfigError{
						Code: taskintake.CodeUnsupportedModel, Detail: "当前任务 Coordinator 模型在注册表中匹配不唯一，请在任务 Coordinator 设置中选择模型",
					}
				}
				match = entry
			}
		}
		if match != nil {
			ref = match.ID
		}
	}
	if ref == "" {
		return taskintake.Model{}, &taskIntakeConfigError{
			Code: taskintake.CodeUnsupportedModel, Detail: "尚未配置可直接调用的任务分析模型，请在设置中配置任务分析使用的模型",
		}
	}
	entry, err := s.models.Get(ref)
	if err != nil || entry == nil {
		return taskintake.Model{}, &taskIntakeConfigError{
			Code: taskintake.CodeUnsupportedModel, Detail: "指定的任务分析模型不存在或已从注册表移除",
		}
	}
	if entry.API != "openai-completions" && entry.API != "openai-responses" {
		return taskintake.Model{}, &taskIntakeConfigError{
			Code: taskintake.CodeUnsupportedModel, Detail: "当前任务分析只支持 openai-completions 或 openai-responses 模型，请在任务 Coordinator 设置中选择模型",
		}
	}
	if strings.TrimSpace(entry.BaseURL) == "" || strings.TrimSpace(entry.Model) == "" || strings.TrimSpace(entry.APIKeyEnv) == "" {
		return taskintake.Model{}, &taskIntakeConfigError{
			Code: taskintake.CodeUnsupportedModel, Detail: "当前任务分析模型缺少直接 HTTP endpoint 配置，请在任务 Coordinator 设置中选择模型",
		}
	}
	apiKey, ok, err := s.credentialsForProvider(entry)
	if err != nil {
		return taskintake.Model{}, err
	}
	if !ok {
		return taskintake.Model{}, &taskIntakeConfigError{
			Code: taskintake.CodeCredentialMissing, Detail: "当前任务分析模型的凭据未配置，请在设置中配置模型凭据",
		}
	}
	return taskintake.Model{
		Ref: ref, Provider: entry.Provider, API: entry.API, Model: entry.Model,
		BaseURL: entry.BaseURL, APIKey: apiKey,
	}, nil
}

func (s *Server) credentialsForProvider(entry *modelconfig.Entry) (string, bool, error) {
	if entry == nil {
		return "", false, &taskIntakeConfigError{
			Code: taskintake.CodeUnsupportedModel, Detail: "任务分析模型配置无效",
		}
	}
	if s.credentials != nil && strings.TrimSpace(entry.ProviderID) != "" {
		key, ok, err := s.credentials.Get(entry.ProviderID)
		if err != nil {
			return "", false, &taskIntakeConfigError{
				Code: taskintake.CodeCredentialMissing, Detail: "任务分析模型凭据读取失败，请在设置中检查模型凭据",
			}
		}
		if ok && strings.TrimSpace(key) != "" {
			return key, true, nil
		}
	}
	if value, ok := os.LookupEnv(entry.APIKeyEnv); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true, nil
	}
	return "", false, nil
}

func (s *Server) writeTaskIntakeError(w http.ResponseWriter, r *http.Request, err error) {
	var configErr *taskIntakeConfigError
	if errors.As(err, &configErr) {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, domain.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/" + configErr.Code, Title: "Task intake unavailable",
			Status: status, Code: configErr.Code, Detail: configErr.Detail,
		})
		return
	}
	var clientErr *taskintake.Error
	if errors.As(err, &clientErr) {
		status := http.StatusBadGateway
		title := "Task intake provider error"
		detail := "分析模型暂时不可用，请稍后重试"
		switch clientErr.Code {
		case taskintake.CodeInvalidInput:
			status, title, detail = http.StatusBadRequest, "Invalid analysis input", clientErr.Message
		case taskintake.CodeUnsupportedModel, taskintake.CodeCredentialMissing:
			status, title = http.StatusUnprocessableEntity, "Task intake unavailable"
			detail = "请在设置中配置可直接调用的任务分析模型"
		case taskintake.CodeTimeout:
			status, title, detail = http.StatusGatewayTimeout, "Task intake provider timeout", "分析模型响应超时，请稍后重试"
		case taskintake.CodeCancelled:
			status, title, detail = 499, "Task intake request cancelled", "分析请求已取消，请重新发送"
		case taskintake.CodeInvalidResponse:
			detail = "分析模型返回格式无法识别，请重试或更换分析模型"
		}
		writeProblem(w, r, Problem{
			Type: "https://workbench.example/problems/" + clientErr.Code, Title: title,
			Status: status, Code: clientErr.Code, Retryable: clientErr.Retryable, Detail: detail,
		})
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		fail(w, r, err)
		return
	}
	writeProblem(w, r, Problem{
		Type: "https://workbench.example/problems/internal", Title: "Internal error",
		Status: http.StatusInternalServerError, Code: "internal", Retryable: true,
	})
}
