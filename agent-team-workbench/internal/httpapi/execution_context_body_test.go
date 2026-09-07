package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPathFieldCheckPreservesBodyForLocationDecoder(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/locations", strings.NewReader(`{"execution_host_id":"host_local","mount_alias":"workspace"}`))
	if _, _, forbidden := pathForbiddenBytes(req); forbidden {
		t.Fatal("safe mount request rejected")
	}
	var body struct {
		Host  string `json:"execution_host_id"`
		Alias string `json:"mount_alias"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Fatalf("path preflight consumed command body: %v", err)
	}
	if body.Alias != "workspace" || body.Host != "host_local" {
		t.Fatalf("command body changed: %+v", body)
	}
	req = httptest.NewRequest(http.MethodPost, "/locations", strings.NewReader(`{"path":"/private/host/path","mount_alias":"workspace"}`))
	if status, _, forbidden := pathForbiddenBytes(req); !forbidden || status != http.StatusForbidden {
		t.Fatal("host path boundary weakened")
	}
}
