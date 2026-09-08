package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestCodeWorkspaceHTTPIsDeveloperReadOnlyAndSessionBound(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := exec.Command("git", "init", "-q", root).Run(); err != nil {
		t.Fatal(err)
	}
	registryFile := filepath.Join(t.TempDir(), "host-registry.yaml")
	registryYAML := fmt.Sprintf("version: 1\nmounts:\n  - alias: default\n    root: %q\n    repository_identity: repo_code_workspace\n", root)
	if err := os.WriteFile(registryFile, []byte(registryYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := hostregistry.Load(registryFile)
	if err != nil {
		t.Fatal(err)
	}
	mounts := registry.Advertise()
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v", mounts)
	}

	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_code_workspace", Name: "code", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecutionHosts().EnsureLocalHost(ctx, now); err != nil {
		t.Fatal(err)
	}
	mount := mounts[0]
	mount.ExecutionHostID = domain.LocalHostID
	mount.Status = domain.MountStatusReady
	mount.LastSeenAt = now
	if err := store.ExecutionHosts().UpsertMount(ctx, &mount); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkspaceLocations().Create(ctx, &domain.WorkspaceLocation{
		ID: "loc_code_workspace", WorkspaceID: ws.ID, ExecutionHostID: domain.LocalHostID,
		MountAlias: mount.Alias, MountGeneration: mount.RegistryGeneration,
		RepositoryIdentity: mount.RepositoryIdentity, IsDefault: true,
		Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	dev := &domain.AgentProfile{ID: "agent_code_developer", WorkspaceID: ws.ID, Name: "Forge", Role: "developer", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, dev); err != nil {
		t.Fatal(err)
	}
	pm := &domain.AgentProfile{ID: "agent_code_pm", WorkspaceID: ws.ID, Name: "Nova", Role: "pm", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, pm); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, nil, atwruntime.NewRegistry())
	var created, deleted atomic.Bool
	var deleteAttempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gateway-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces":
			created.Store(true)
			body, _ := io.ReadAll(r.Body)
			var createPayload map[string]any
			if err := json.Unmarshal(body, &createPayload); err != nil || createPayload["read_only"] != true || createPayload["client_key"] == nil || createPayload["ttl_seconds"] == nil || r.Header.Get("X-ATW-Code-Workspace-Create-ID") == "" {
				t.Errorf("creation recovery metadata missing: headers=%v body=%s", r.Header, body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"upstream-1","status":"browsing","root_display":"repo","lsp_enabled":true,"lsp_status":"off"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/upstream-1/fs/tree":
			if r.Header.Get("Origin") != "" {
				t.Errorf("proxy leaked browser Origin: %q", r.Header.Get("Origin"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"","entries":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/workspaces/upstream-1/lsp":
			conn, upgradeErr := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if upgradeErr != nil {
				return
			}
			defer conn.Close()
			for {
				if _, _, readErr := conn.ReadMessage(); readErr != nil {
					return
				}
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/workspaces/upstream-1":
			if deleteAttempts.Add(1) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	server := NewServer(svc, store, nil)
	server.SetCodeWorkspaceGateway(func(context.Context) (string, string, error) { return upstream.URL, "gateway-secret", nil }, registry)
	t.Cleanup(server.CloseCodeWorkspaces)
	mux := server.Routes()

	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/agent-profiles/"+pm.ID+"/code-workspaces", strings.NewReader(`{}`))
	mux.ServeHTTP(bad, badReq)
	if bad.Code != http.StatusForbidden {
		t.Fatalf("pm create status = %d, body=%s", bad.Code, bad.Body.String())
	}
	crossOrigin := httptest.NewRecorder()
	crossOriginReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/agent-profiles/"+dev.ID+"/code-workspaces", strings.NewReader(`{}`))
	crossOriginReq.Header.Set("Origin", "http://evil.example")
	mux.ServeHTTP(crossOrigin, crossOriginReq)
	if crossOrigin.Code != http.StatusForbidden || created.Load() {
		t.Fatalf("cross-origin create status=%d created=%v body=%s", crossOrigin.Code, created.Load(), crossOrigin.Body.String())
	}
	fetchCrossOrigin := httptest.NewRecorder()
	fetchCrossOriginReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/agent-profiles/"+dev.ID+"/code-workspaces", strings.NewReader(`{}`))
	fetchCrossOriginReq.Header.Set("Sec-Fetch-Site", "cross-site")
	mux.ServeHTTP(fetchCrossOrigin, fetchCrossOriginReq)
	if fetchCrossOrigin.Code != http.StatusForbidden || created.Load() {
		t.Fatalf("cross-site fetch create status=%d created=%v body=%s", fetchCrossOrigin.Code, created.Load(), fetchCrossOrigin.Body.String())
	}

	create := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/agent-profiles/"+dev.ID+"/code-workspaces", strings.NewReader(`{"path":"/tmp"}`))
	mux.ServeHTTP(create, createReq)
	if create.Code != http.StatusForbidden || created.Load() {
		t.Fatalf("path escape create status=%d created=%v body=%s", create.Code, created.Load(), create.Body.String())
	}
	create = httptest.NewRecorder()
	createReq = httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/agent-profiles/"+dev.ID+"/code-workspaces", strings.NewReader(`{}`))
	mux.ServeHTTP(create, createReq)
	if create.Code != http.StatusCreated || !created.Load() {
		t.Fatalf("developer create status=%d body=%s", create.Code, create.Body.String())
	}
	var session codeWorkspaceResponse
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.ID == "" || !session.ReadOnly || strings.Contains(create.Body.String(), "gateway-secret") {
		t.Fatalf("unsafe session response: %s", create.Body.String())
	}

	bootstrap := httptest.NewRecorder()
	mux.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "/api/v1/code-workspaces/"+session.ID+"/bootstrap", nil))
	if bootstrap.Code != http.StatusOK || !strings.Contains(bootstrap.Body.String(), `"lsp_enabled":true`) || strings.Contains(bootstrap.Body.String(), "gateway-secret") {
		t.Fatalf("bootstrap status=%d body=%s", bootstrap.Code, bootstrap.Body.String())
	}

	proxy := httptest.NewRecorder()
	proxyReq := httptest.NewRequest(http.MethodGet, "/api/v1/code-workspaces/"+session.ID+"/gateway/api/v1/workspaces/upstream-1/fs/tree", nil)
	proxyReq.Header.Set("Origin", "http://example.com")
	mux.ServeHTTP(proxy, proxyReq)
	if proxy.Code != http.StatusOK || !strings.Contains(proxy.Body.String(), `"entries"`) {
		t.Fatalf("proxy status=%d body=%s", proxy.Code, proxy.Body.String())
	}

	put := httptest.NewRecorder()
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/code-workspaces/"+session.ID+"/gateway/api/v1/workspaces/upstream-1/fs/file", strings.NewReader("write"))
	mux.ServeHTTP(put, putReq)
	if put.Code != http.StatusMethodNotAllowed {
		t.Fatalf("write proxy status=%d body=%s", put.Code, put.Body.String())
	}

	// A large LSP frame is rejected at the BFF boundary and releases the
	// session instead of allocating an unbounded payload.
	bff := httptest.NewServer(mux)
	defer bff.Close()
	largeURL := "ws" + strings.TrimPrefix(bff.URL, "http") + "/api/v1/code-workspaces/" + session.ID + "/gateway/api/v1/workspaces/upstream-1/lsp"
	largeLSP, _, err := websocket.DefaultDialer.Dial(largeURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	largeLSP.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = largeLSP.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("x"), 17<<20))
	if _, _, readErr := largeLSP.ReadMessage(); readErr == nil {
		t.Fatal("oversized LSP frame was not rejected")
	}
	_ = largeLSP.Close()
	limitClose := httptest.NewRecorder()
	for attempt := 0; attempt < 3 && limitClose.Code != http.StatusNoContent; attempt++ {
		limitClose = httptest.NewRecorder()
		mux.ServeHTTP(limitClose, httptest.NewRequest(http.MethodDelete, "/api/v1/code-workspaces/"+session.ID, nil))
	}
	if limitClose.Code != http.StatusNoContent || deleteAttempts.Load() < 2 {
		t.Fatalf("oversized frame session cleanup status=%d attempts=%d body=%s", limitClose.Code, deleteAttempts.Load(), limitClose.Body.String())
	}

	create = httptest.NewRecorder()
	createReq = httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+ws.ID+"/agent-profiles/"+dev.ID+"/code-workspaces", strings.NewReader(`{}`))
	mux.ServeHTTP(create, createReq)
	if create.Code != http.StatusCreated {
		t.Fatalf("reopen after oversized frame status=%d body=%s", create.Code, create.Body.String())
	}
	if err := json.Unmarshal(create.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	// A long-lived LSP socket is rechecked on a bounded timer. Revoking the
	// Agent closes the socket and the session cleanup retries the upstream
	// DELETE when its first attempt fails.
	wsURL := "ws" + strings.TrimPrefix(bff.URL, "http") + "/api/v1/code-workspaces/" + session.ID + "/gateway/api/v1/workspaces/upstream-1/lsp"
	lsp, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lsp.Close()
	storedAgent, err := store.Agents().Get(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedAgent.Availability = domain.AgentDisabled
	if err := store.Agents().Update(ctx, storedAgent, storedAgent.Version); err != nil {
		t.Fatal(err)
	}
	_ = lsp.SetReadDeadline(time.Now().Add(6 * time.Second))
	if _, _, readErr := lsp.ReadMessage(); readErr == nil {
		t.Fatal("LSP remained readable after Agent qualification was revoked")
	}

	closeRec := httptest.NewRecorder()
	mux.ServeHTTP(closeRec, httptest.NewRequest(http.MethodDelete, "/api/v1/code-workspaces/"+session.ID, nil))
	if closeRec.Code != http.StatusNoContent || !deleted.Load() || deleteAttempts.Load() < 2 {
		t.Fatalf("close status=%d deleted=%v attempts=%d body=%s", closeRec.Code, deleted.Load(), deleteAttempts.Load(), closeRec.Body.String())
	}
}
