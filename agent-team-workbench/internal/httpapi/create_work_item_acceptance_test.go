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

func TestPublicRootTaskRequiresAcceptanceCriteria(t *testing.T) {
	s := newPlanTestServer(t)
	mux := s.Routes()
	workspaceID, agentID, _ := seedPlanHTTPEnv(t, s)

	for index, body := range []string{
		`{"title":"missing criteria","record_kind":"task"}`,
		`{"title":"empty criteria","record_kind":"task","acceptance_criteria":[]}`,
		`{"title":"blank criteria","record_kind":"task","acceptance_criteria":["   "]}`,
		`{"title":"null parent is root","record_kind":"task","parent_id":null}`,
		`{"title":"empty parent is root","record_kind":"task","parent_id":""}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspaceID+"/work-items", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "missing-criteria-"+string(rune('a'+index)))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("root Task without usable acceptance criteria must be rejected: case=%d status=%d body=%s", index, rec.Code, rec.Body.String())
		}
		var problem map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if problem["code"] != "validation_failed" {
			t.Fatalf("missing criteria must use validation problem: %#v", problem)
		}
	}
	items, _, err := s.svc.WorkItems(context.Background(), workspaceID, application.WorkItemFilter{RecordKind: domain.RecordKindTask})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("rejected root Task requests must not leave WorkItems: %d", len(items))
	}
	chat := postWorkItem(t, mux, workspaceID,
		`{"title":"chat without acceptance","record_kind":"chat","agent_profile_id":"`+agentID+`"}`)
	if chat.Code != http.StatusCreated {
		t.Fatalf("explicit Chat may omit task acceptance criteria: status=%d body=%s", chat.Code, chat.Body.String())
	}
}
