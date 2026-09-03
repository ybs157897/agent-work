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

func TestDeliveryBriefSnapshotEndpointsAreScopedAndApprovalProtected(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	workspaceID, leadID, _ := seedPlanHTTPEnv(t, s)
	root, err := s.svc.CreateWorkItem(context.Background(), workspaceID, application.CreateWorkItemParams{
		Title: "snapshot endpoint task", AgentProfileID: leadID, RecordKind: domain.RecordKindTask,
		AcceptanceCriteria: []string{"immutable brief evidence"},
	})
	if err != nil {
		t.Fatal(err)
	}
	goal, err := s.svc.GetGoalForWorkItem(context.Background(), root.ID)
	if err != nil {
		t.Fatal(err)
	}
	todo, err := s.svc.GetTodo(context.Background(), goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/workspaces/" + workspaceID + "/goals/" + goal.ID + "/todos/" + todo.ID + "/evidence/delivery-brief-snapshots"
	request := httptest.NewRequest(http.MethodPost, path,
		strings.NewReader(`{"work_item_id":"`+root.ID+`","client_key":"http-brief-1"}`))
	request.Header.Set("Idempotency-Key", "http-brief-1")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("capture snapshot status=%d body=%s", response.Code, response.Body.String())
	}
	var snapshot map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshotID, ok := snapshot["id"].(string)
	if !ok || !strings.HasPrefix(snapshotID, "brief_") || snapshot["canonical_digest"] == "" {
		t.Fatalf("capture response must be a sealed brief snapshot: %#v", snapshot)
	}

	replay := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, path,
		strings.NewReader(`{"work_item_id":"`+root.ID+`","client_key":"http-brief-1"}`))
	replayRequest.Header.Set("Idempotency-Key", "http-brief-1")
	mux.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same HTTP capture must replay: status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}

	getPath := "/api/v1/workspaces/" + workspaceID + "/delivery-brief-snapshots/" + snapshotID
	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, getPath, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), snapshotID) {
		t.Fatalf("snapshot GET must return the sealed capture: status=%d body=%s", get.Code, get.Body.String())
	}

	wrongWorkspace := httptest.NewRecorder()
	mux.ServeHTTP(wrongWorkspace, httptest.NewRequest(http.MethodGet,
		"/api/v1/workspaces/ws_other/delivery-brief-snapshots/"+snapshotID, nil))
	if wrongWorkspace.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace snapshot GET must be hidden: status=%d body=%s", wrongWorkspace.Code, wrongWorkspace.Body.String())
	}

	s.SetDemoRole(domain.RoleViewer)
	for _, methodPath := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, path, `{}`},
		{http.MethodGet, getPath, ""},
	} {
		var body *strings.Reader
		if methodPath.body == "" {
			body = strings.NewReader("")
		} else {
			body = strings.NewReader(methodPath.body)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(methodPath.method, methodPath.path, body)
		if methodPath.method == http.MethodPost {
			req.Header.Set("Idempotency-Key", "viewer-brief")
		}
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("viewer must not access snapshot %s %s: status=%d body=%s", methodPath.method, methodPath.path, rec.Code, rec.Body.String())
		}
	}
}
