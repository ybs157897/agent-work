package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ybs/web-idea/apps/gateway/internal/api"
	"github.com/ybs/web-idea/apps/gateway/internal/config"
	"github.com/ybs/web-idea/apps/gateway/internal/jdtls"
	"github.com/ybs/web-idea/apps/gateway/internal/workspace"
)

func leaseTestConfig() config.Config {
	return config.Config{
		Listen:       "127.0.0.1:0",
		AuthMode:     "dev",
		DevToken:     "test-token",
		MaxFileBytes: 1 << 20,
		CORSOrigins:  []string{"http://127.0.0.1:5173"},
	}
}

func leaseRequest(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCreateWorkspaceIdempotencyHeaderAndConflict(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	store := workspace.NewStore(workspace.Options{DefaultReadOnlyTTL: time.Minute, MaxTTL: time.Hour})
	h := api.New(leaseTestConfig(), store, nil).Handler()
	body := `{"root":"` + root + `","read_only":true,"client_key":"create-1","ttl_seconds":1800}`
	first := leaseRequest(t, h, http.MethodPost, "/api/v1/workspaces", body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status=%d body=%s", first.Code, first.Body.String())
	}
	var got struct {
		ID        string `json:"id"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(first.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.ExpiresAt == "" {
		t.Fatalf("lease response=%+v", got)
	}
	second := leaseRequest(t, h, http.MethodPost, "/api/v1/workspaces", body)
	if second.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", second.Code, second.Body.String())
	}
	var replay struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(second.Body).Decode(&replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != got.ID {
		t.Fatalf("replay id=%q want=%q", replay.ID, got.ID)
	}
	conflict := leaseRequest(t, h, http.MethodPost, "/api/v1/workspaces", `{"root":"`+other+`","read_only":true,"client_key":"create-1"}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("root conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	mismatch := leaseRequest(t, h, http.MethodPost, "/api/v1/workspaces", `{"root":"`+root+`","read_only":true,"client_key":"body-key"}`)
	// The helper cannot add the custom header, so use a direct request for this
	// contract boundary below.
	if mismatch.Code != http.StatusCreated {
		// body-key is independent and is expected to create a separate workspace.
		t.Fatalf("independent key status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces", strings.NewReader(`{"root":"`+root+`","read_only":true,"client_key":"body-key"}`))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ATW-Code-Workspace-Create-ID", "header-key")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("header mismatch status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestExpiredWorkspaceStopsJDTLSAndCleansIndex(t *testing.T) {
	root := t.TempDir()
	launch := filepath.Join(root, "launch.sh")
	if err := os.WriteFile(launch, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := workspace.NewStore(workspace.Options{
		DefaultReadOnlyTTL: 15 * time.Millisecond,
		MaxTTL:             time.Second,
		MaxWorkspaces:      4,
	})
	mgr := jdtls.NewManager(jdtls.Config{LaunchScript: launch, DataRoot: filepath.Join(root, "jdtls-data")})
	srv := api.New(leaseTestConfig(), store, mgr)
	h := srv.Handler()
	create := leaseRequest(t, h, http.MethodPost, "/api/v1/workspaces", `{"root":"`+root+`","read_only":true}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(create.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ID == "" {
		t.Fatal("missing workspace id")
	}
	if _, err := mgr.Ensure(response.ID, root, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jdtls-data", response.ID, "marker"), []byte("index"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	get := leaseRequest(t, h, http.MethodGet, "/api/v1/workspaces/"+response.ID, "")
	if get.Code != http.StatusNotFound {
		t.Fatalf("expired Get status=%d body=%s", get.Code, get.Body.String())
	}
	if _, err := mgr.Session(response.ID); !errors.Is(err, jdtls.ErrNotReady) {
		t.Fatalf("jdtls session retained after expiry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "jdtls-data", response.ID)); !os.IsNotExist(err) {
		t.Fatalf("jdtls data retained after expiry: %v", err)
	}
	srv.Close()
}

func TestServerSweeperAndCloseAreExplicitlyOwned(t *testing.T) {
	store := workspace.NewStore(workspace.Options{DefaultReadOnlyTTL: 10 * time.Millisecond, MaxTTL: time.Second})
	cfg := leaseTestConfig()
	cfg.WorkspaceSweepInterval = 5 * time.Millisecond
	srv := api.New(cfg, store, nil)
	srv.Start(context.Background())
	h := srv.Handler()
	root := t.TempDir()
	create := leaseRequest(t, h, http.MethodPost, "/api/v1/workspaces", `{"root":"`+root+`","read_only":true}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(bytes.NewReader(create.Body.Bytes())).Decode(&response); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := store.Get(response.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("sweeper retained expired workspace: %v", err)
	}
	srv.Close()
	srv.Close()
}
