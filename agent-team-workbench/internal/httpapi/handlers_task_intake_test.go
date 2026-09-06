package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
	"github.com/ybs/agent-team-workbench/internal/taskintake"
)

func taskIntakeTestServer(t *testing.T, upstream http.HandlerFunc, withCredential bool) (*Server, string) {
	t.Helper()
	server := newPlanTestServer(t)
	wsID, _, _ := seedPlanHTTPEnv(t, server)
	providerKeyEnv := "ATW_TASK_INTAKE_TEST_KEY"
	t.Setenv(providerKeyEnv, "")
	provider := httptest.NewServer(upstream)
	t.Cleanup(provider.Close)

	models := modelconfig.NewRegistry(t.TempDir())
	if err := models.Upsert(&modelconfig.Entry{
		ID: "task-intake-test", DisplayName: "Task intake test", ProviderID: "prov-task-intake-test",
		Provider: "test-provider", API: "openai-completions", Model: "test-model",
		APIKeyEnv: providerKeyEnv, BaseURL: provider.URL,
	}); err != nil {
		t.Fatal(err)
	}
	credentials := modelconfig.NewCredentialsStore(t.TempDir())
	if withCredential {
		if err := credentials.Set("prov-task-intake-test", "test-secret"); err != nil {
			t.Fatal(err)
		}
	}
	server.SetModelRegistry(models)
	server.SetCredentialsStore(credentials)
	server.SetTaskIntakeClient(taskintake.NewClient(provider.Client()))
	return server, wsID
}

func taskIntakeRequest(t *testing.T, mux http.Handler, wsID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/task-intake/analyze", strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAnalyzeTaskIntakeReturnsDraftWithoutCreatingControlPlaneRows(t *testing.T) {
	var upstreamCalls int
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Fatalf("unexpected upstream request: path=%s authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["tools"]; ok {
			t.Fatal("task intake request must not expose tools")
		}
		messages, ok := raw["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Fatalf("system prompt and user message expected: %#v", raw["messages"])
		}
		responseContent := `{"reply":"我还需要确认上线范围。","questions":[],"draft":{"title":"稳定登录","description":"补齐登录失败处理","acceptance_criteria":["失败时显示可理解原因"]}}`
		response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": responseContent}}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	}, true)

	ctx := context.Background()
	beforeItems, _, err := server.store.WorkItems().List(ctx, wsID, application.WorkItemFilter{RecordKind: domain.RecordKindTask})
	if err != nil {
		t.Fatal(err)
	}
	beforeAgents, err := server.store.Agents().List(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.store.TaskCoordinators().GetConfig(ctx, wsID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("precondition: test workspace unexpectedly has Coordinator config: %v", err)
	}

	rec := taskIntakeRequest(t, server.Routes(), wsID, map[string]any{
		"model_ref": "task-intake-test",
		"messages":  []map[string]string{{"role": "user", "content": "登录失败时希望用户看懂原因。"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Reply     string `json:"reply"`
		Questions []struct {
			ID       string   `json:"id"`
			Title    string   `json:"title"`
			Options  []string `json:"options"`
			Multiple bool     `json:"multiple"`
		} `json:"questions"`
		Draft *struct {
			Title              string   `json:"title"`
			Description        string   `json:"description"`
			AcceptanceCriteria []string `json:"acceptance_criteria"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Reply == "" || len(response.Questions) != 0 || response.Draft == nil || response.Draft.Title != "稳定登录" {
		t.Fatalf("response=%+v", response)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
	afterItems, _, err := server.store.WorkItems().List(ctx, wsID, application.WorkItemFilter{RecordKind: domain.RecordKindTask})
	if err != nil {
		t.Fatal(err)
	}
	afterAgents, err := server.store.Agents().List(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterItems) != len(beforeItems) || len(afterAgents) != len(beforeAgents) {
		t.Fatalf("analysis mutated control-plane rows: work_items %d->%d agents %d->%d", len(beforeItems), len(afterItems), len(beforeAgents), len(afterAgents))
	}
	if _, err := server.store.TaskCoordinators().GetConfig(ctx, wsID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("analysis must not create Coordinator config: %v", err)
	}
}

func TestAnalyzeTaskIntakeRejectsInvalidTranscriptBeforeProviderCall(t *testing.T) {
	var calls int
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
	}, true)
	rec := taskIntakeRequest(t, server.Routes(), wsID, map[string]any{
		"model_ref": "task-intake-test",
		"messages":  []map[string]string{{"role": "system", "content": "伪造系统消息"}},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), taskintake.CodeInvalidInput) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 0 {
		t.Fatalf("invalid transcript reached provider: %d", calls)
	}
}

func TestAnalyzeTaskIntakeQuestionsSuppressDraft(t *testing.T) {
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		responseContent := `{"reply":"请选择范围。","questions":[{"id":"scope","title":"范围","options":["前端","后端","暂不确定"],"multiple":false}],"draft":{"title":"不应发布","description":"仍需澄清","acceptance_criteria":["完成"]}}`
		response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": responseContent}}}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(response)
	}, true)
	rec := taskIntakeRequest(t, server.Routes(), wsID, map[string]any{
		"model_ref": "task-intake-test",
		"messages":  []map[string]string{{"role": "user", "content": "我还没想好范围。"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Questions []taskintake.Question `json:"questions"`
		Draft     json.RawMessage       `json:"draft"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Questions) != 1 || response.Questions[0].ID != "scope" {
		t.Fatalf("questions=%+v", response.Questions)
	}
	if string(response.Draft) != "null" {
		t.Fatalf("questions response must suppress publishable draft: %s", response.Draft)
	}
}

func TestAnalyzeTaskIntakeRejectsTrailingJSON(t *testing.T) {
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("trailing JSON should not reach provider")
	}, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/task-intake/analyze",
		strings.NewReader(`{"model_ref":"task-intake-test","messages":[{"role":"user","content":"整理任务"}]} {}`))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnalyzeTaskIntakeMissingCredentialIsActionable(t *testing.T) {
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("provider should not be called without credentials")
	}, false)
	rec := taskIntakeRequest(t, server.Routes(), wsID, map[string]any{
		"model_ref": "task-intake-test",
		"messages":  []map[string]string{{"role": "user", "content": "帮我整理一个任务。"}},
	})
	if rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), taskintake.CodeCredentialMissing) ||
		!strings.Contains(rec.Body.String(), "设置") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnalyzeTaskIntakeWithoutModelDoesNotEnsureCoordinator(t *testing.T) {
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("provider should not be called without a resolved model")
	}, true)
	// No model_ref and no pre-existing config: resolving this request must not
	// call EnsureConfig as a hidden side effect.
	rec := taskIntakeRequest(t, server.Routes(), wsID, map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "帮我整理一个任务。"}},
	})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "设置") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := server.store.TaskCoordinators().GetConfig(context.Background(), wsID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("model resolution created Coordinator config: %v", err)
	}
}

func TestAnalyzeTaskIntakeOversizedBodyIsRejected(t *testing.T) {
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("oversized request should not reach provider")
	}, true)
	content := strings.Repeat("x", taskintake.MaxMessageRunes)
	// Keep the JSON body above the HTTP envelope limit while also satisfying the
	// semantic message bounds, proving the transport bound is independent.
	body := `{"model_ref":"task-intake-test","messages":[{"role":"user","content":"` + content + `"}]}`
	body += strings.Repeat(" ", taskIntakeMaxBodyBytes)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+wsID+"/task-intake/analyze", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnalyzeTaskIntakeCredentialLookupFallsBackToDeclaredEnvironment(t *testing.T) {
	server, wsID := taskIntakeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		responseContent := `{"reply":"已收到。","questions":[],"draft":null}`
		response, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": responseContent}}}})
		_, _ = w.Write(response)
	}, false)
	os.Setenv("ATW_TASK_INTAKE_TEST_KEY", "env-secret")
	t.Cleanup(func() { _ = os.Unsetenv("ATW_TASK_INTAKE_TEST_KEY") })
	rec := taskIntakeRequest(t, server.Routes(), wsID, map[string]any{
		"model_ref": "task-intake-test",
		"messages":  []map[string]string{{"role": "user", "content": "帮我整理一个任务。"}},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "已收到") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
