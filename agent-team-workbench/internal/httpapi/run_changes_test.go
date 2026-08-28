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

func TestRunChangesRoutesExposeUnavailableAndValidateRevert(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, leadID, _ := seedPlanHTTPEnv(t, s)
	wi, err := s.svc.CreateWorkItem(context.Background(), wsID, application.CreateWorkItemParams{Title: "changes", AgentProfileID: leadID})
	if err != nil {
		t.Fatal(err)
	}
	run := seedRunWithUsage(t, s, wsID, wi.ID, domain.ExecutionRun{})

	get := httptest.NewRecorder()
	mux.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+run+"/changes", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET changes=%d %s", get.Code, get.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(get.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["state"] != "unavailable" || body["can_revert"] != false {
		t.Fatalf("unexpected unavailable body: %v", body)
	}

	diff := httptest.NewRecorder()
	mux.ServeHTTP(diff, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+run+"/changes/diff?path=a.txt", nil))
	if diff.Code != http.StatusNotFound {
		t.Fatalf("GET diff=%d %s", diff.Code, diff.Body.String())
	}
	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+run+"/changes/diff?path=..%2Fsecret", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("GET invalid diff=%d %s", invalid.Code, invalid.Body.String())
	}

	revert := httptest.NewRecorder()
	mux.ServeHTTP(revert, httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run+"/commands/revert-changes", strings.NewReader(`{}`)))
	if revert.Code != http.StatusBadRequest {
		t.Fatalf("POST revert=%d %s", revert.Code, revert.Body.String())
	}
}
