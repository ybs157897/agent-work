package kimiapp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRESTEmptyActionsDoNotClaimJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 && r.Header.Get("Content-Type") == "application/json" {
			writeKap(w, http.StatusBadRequest, codeInternalError, map[string]any{"error": "Body cannot be empty when content-type is set to application/json"})
			return
		}
		writeKap(w, http.StatusOK, 0, map[string]any{})
	}))
	defer server.Close()
	client := newRestClient(server.URL, testToken)
	if err := client.do(context.Background(), http.MethodPost, "/sessions/s/questions/q:dismiss", nil, nil, false); err != nil {
		t.Fatalf("empty native action rejected: %v", err)
	}
}
