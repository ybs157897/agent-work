package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyTaskIntakeAnalyzeRouteIsRemoved(t *testing.T) {
	server := newPlanTestServer(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/workspaces/ws_legacy/task-intake/analyze",
		strings.NewReader(`{"messages":[{"role":"user","content":"旧入口"}]}`))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("removed task-intake analyze endpoint must return 404, got %d: %s", rec.Code, rec.Body.String())
	}
}
