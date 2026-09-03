package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/modelconfig"
)

func TestDeleteModelRequiresIdempotencyAndReplays(t *testing.T) {
	store := openIdempotencyTestDB(t)
	registry := modelconfig.NewRegistry(t.TempDir())
	if err := registry.Upsert(&modelconfig.Entry{
		ID: "delete-me", DisplayName: "Delete me", ProviderID: "provider-test",
		Provider: "test", Model: "test-model", APIKeyEnv: "TEST_API_KEY",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: store, models: registry, demoRole: domain.RoleOwner}
	mux := s.Routes()

	missingKey := httptest.NewRecorder()
	mux.ServeHTTP(missingKey, httptest.NewRequest(http.MethodDelete, "/api/v1/models/delete-me", nil))
	if missingKey.Code != http.StatusBadRequest || !strings.Contains(missingKey.Body.String(), "missing_idempotency_key") {
		t.Fatalf("DELETE model without Idempotency-Key must fail: status=%d body=%s", missingKey.Code, missingKey.Body.String())
	}

	firstReq := httptest.NewRequest(http.MethodDelete, "/api/v1/models/delete-me", nil)
	firstReq.Header.Set("Idempotency-Key", "delete-model-1")
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, firstReq)
	if first.Code != http.StatusNoContent || first.Header().Get("Content-Type") != "" {
		t.Fatalf("first DELETE must be an idempotent 204 without a body type: status=%d headers=%v", first.Code, first.Header())
	}
	if got, err := registry.Get("delete-me"); err != nil || got != nil {
		t.Fatalf("model must be deleted: got=%+v err=%v", got, err)
	}

	replayReq := httptest.NewRequest(http.MethodDelete, "/api/v1/models/delete-me", nil)
	replayReq.Header.Set("Idempotency-Key", "delete-model-1")
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, replayReq)
	if replay.Code != http.StatusNoContent || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same DELETE must replay the 204: status=%d headers=%v", replay.Code, replay.Header())
	}

	conflictReq := httptest.NewRequest(http.MethodDelete, "/api/v1/models/delete-me", strings.NewReader(`{"different":true}`))
	conflictReq.Header.Set("Idempotency-Key", "delete-model-1")
	conflict := httptest.NewRecorder()
	mux.ServeHTTP(conflict, conflictReq)
	if conflict.Code != http.StatusConflict || conflict.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("same DELETE key with a different body must conflict as problem: status=%d headers=%v body=%s", conflict.Code, conflict.Header(), conflict.Body.String())
	}
}
