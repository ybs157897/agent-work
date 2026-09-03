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

func TestProjectionRepairResponseUsesPublicJSONFieldNames(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	workspaceID, leadID, _ := seedPlanHTTPEnv(t, s)
	root, err := s.svc.CreateWorkItem(context.Background(), workspaceID, application.CreateWorkItemParams{
		Title: "projection response", AgentProfileID: leadID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"projection repair response matches OpenAPI"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := s.svc.GetGoalForWorkItem(context.Background(), root.ID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/"+workspaceID+"/goals/"+goal.ID+"/projection/commands/repair",
		strings.NewReader(`{"client_key":"projection-http-shape"}`))
	req.Header.Set("Idempotency-Key", "projection-http-shape")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("projection repair status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["repair"] == nil || body["projection"] == nil {
		t.Fatalf("projection repair response must use lower-case contract fields: %s", rec.Body.String())
	}
	if body["Repair"] != nil || body["Projection"] != nil {
		t.Fatalf("Go field names leaked into public response: %s", rec.Body.String())
	}
}

func TestEmptyGovernanceCollectionsEncodeAsArrays(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	workspaceID, leadID, _ := seedPlanHTTPEnv(t, s)
	root, err := s.svc.CreateWorkItem(context.Background(), workspaceID, application.CreateWorkItemParams{
		Title: "empty governance collections", AgentProfileID: leadID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"empty collections stay arrays"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := s.svc.GetGoalForWorkItem(context.Background(), root.ID)
	if err != nil {
		t.Fatal(err)
	}

	get := func(path string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	goalBody := get("/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID)
	for _, field := range []string{"acceptance_contract", "quota_policies", "completion_evidence_summary"} {
		if _, ok := goalBody[field].([]any); !ok {
			t.Fatalf("Goal.%s must encode as an array, got %#v", field, goalBody[field])
		}
	}
	todos := get("/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID + "/todos")
	items, ok := todos["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("Todo list shape mismatch: %#v", todos)
	}
	todo := items[0].(map[string]any)
	for _, field := range []string{"acceptance", "predecessors", "successors"} {
		if _, ok := todo[field].([]any); !ok {
			t.Fatalf("Todo.%s must encode as an array, got %#v", field, todo[field])
		}
	}
	if todo["completion_turn_key"] != nil || todo["completion_evidence_id"] != nil {
		t.Fatalf("non-completed Todo completion identity must encode as null: %#v", todo)
	}
	handoffs := get("/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID + "/handoffs")
	if items, ok := handoffs["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("empty Handoff list must encode as []: %#v", handoffs)
	}
	quota := get("/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID + "/quota")
	if policies, ok := quota["policies"].([]any); !ok || len(policies) != 0 {
		t.Fatalf("empty GoalQuota policies must encode as []: %#v", quota)
	}
	repairs := get("/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID + "/projection/repairs")
	if items, ok := repairs["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("empty projection repairs must encode as []: %#v", repairs)
	}
}

func TestTodoDTOExposesCompletedTurnAndEvidenceIdentity(t *testing.T) {
	key := domain.TurnKey{GoalID: "goal_01J00000000000000000000000", TodoID: "todo_01J00000000000000000000000", TurnSeq: 3}
	todo := &domain.Todo{
		ID: key.TodoID, GoalID: key.GoalID, Status: domain.TodoCompleted,
		CompletionTurnKey: &key, CompletionEvidenceID: "wi_01J000000000000000000000000",
	}
	dto := toTodoDTO(todo)
	if dto.CompletionTurnKey == nil || !dto.CompletionTurnKey.Equal(key) {
		t.Fatalf("completion turn key missing from public Todo DTO: %+v", dto)
	}
	if dto.CompletionEvidenceID == nil || *dto.CompletionEvidenceID != todo.CompletionEvidenceID {
		t.Fatalf("completion evidence missing from public Todo DTO: %+v", dto)
	}
}
