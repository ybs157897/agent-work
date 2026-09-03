package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestGovernanceMetricsEndpointIsWorkspaceScopedServiceReadModel(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	workspaceID, leadID, _ := seedPlanHTTPEnv(t, s)
	root, err := s.svc.CreateWorkItem(context.Background(), workspaceID, application.CreateWorkItemParams{
		Title: "治理指标任务", AgentProfileID: leadID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"metrics endpoint is service-owned"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+workspaceID+"/governance/metrics", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("治理指标查询应返回 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["workspace_id"] != workspaceID || got["plan_decode_errors"] == nil {
		t.Fatalf("治理指标响应缺少 workspace/错误族读模型: %#v", got)
	}
	if got["source_event_seq"].(float64) < 1 {
		t.Fatalf("治理指标应从 canonical event stream 读取 source_event_seq: %#v", got)
	}
	_ = root

	wrong := httptest.NewRecorder()
	mux.ServeHTTP(wrong, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/ws_other/governance/metrics", nil))
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("未知 workspace 必须返回 404，实际 %d: %s", wrong.Code, wrong.Body.String())
	}
}

func TestQuotaGapReconciliationHTTPIsWorkspaceScopedAndApprovalOnly(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	workspaceID, leadID, _ := seedPlanHTTPEnv(t, s)
	root, err := s.svc.CreateWorkItem(context.Background(), workspaceID, application.CreateWorkItemParams{
		Title: "quota reconciliation API", AgentProfileID: leadID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"quota reconciliation remains evidence-bound"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := s.svc.GetGoalForWorkItem(context.Background(), root.ID)
	if err != nil {
		t.Fatal(err)
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/"+workspaceID+"/goals/"+goal.ID+"/quota/reconciliations", nil))
	if list.Code != http.StatusOK || strings.TrimSpace(list.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty reconciliation list must be a stable JSON array: status=%d body=%s", list.Code, list.Body.String())
	}

	requestBody := `{"target":{"turn_key":{"goal_id":"goal_other","todo_id":"todo_other","turn_seq":1},"quota_kind":"output_tokens","run_id":"run_other"},"amount":1,"evidence":{"source_kind":"run","source_id":"run_other","verification":"passed","summary":"x","recorded_at":"2026-09-02T00:00:00Z"},"actor_id":"user_1","reason":"x","client_key":"cross-goal"}`
	crossGoal := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+workspaceID+"/goals/"+goal.ID+"/quota/reconciliations",
		strings.NewReader(requestBody))
	crossGoal.Header.Set("Idempotency-Key", "cross-goal")
	crossGoalResponse := httptest.NewRecorder()
	mux.ServeHTTP(crossGoalResponse, crossGoal)
	if crossGoalResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-goal reconciliation target must be hidden: status=%d body=%s", crossGoalResponse.Code, crossGoalResponse.Body.String())
	}

	s.SetDemoRole(domain.RoleViewer)
	forbidden := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+workspaceID+"/goals/"+goal.ID+"/quota/reconciliations",
		strings.NewReader(requestBody))
	forbidden.Header.Set("Idempotency-Key", "forbidden")
	forbiddenResponse := httptest.NewRecorder()
	mux.ServeHTTP(forbiddenResponse, forbidden)
	if forbiddenResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer must not reconcile quota gaps: status=%d body=%s", forbiddenResponse.Code, forbiddenResponse.Body.String())
	}
}
