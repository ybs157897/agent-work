package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentconfig"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

type nonDurableAgentConfigSync struct{}

func (nonDurableAgentConfigSync) Import(context.Context, string) (agentconfig.ImportResult, error) {
	return agentconfig.ImportResult{}, nil
}

func TestPatchAgentRejectsNonDurableSynchronizerBeforeDBMutation(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_agent_config_sync", Name: "agent config", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_config_sync", WorkspaceID: workspace.ID, Slug: "forge", Name: "Forge", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, nil)
	server.SetAgentConfigSync(nonDurableAgentConfigSync{})
	rec := patchAgentConfigBodyWithID(t, server, agent.ID, "non-durable-sync",
		`{"name":"Changed","expected_version":1}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "agent_config_sync_not_durable") {
		t.Fatalf("non-durable synchronizer must fail closed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != agent.Name || got.Version != agent.Version {
		t.Fatalf("non-durable synchronizer changed DB before rejection: before=%+v after=%+v", agent, got)
	}
}

func TestCreateAgentRejectsNonDurableSynchronizerBeforeDBMutation(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_create_non_durable", Name: "create non durable", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, nil)
	server.SetAgentConfigSync(nonDurableAgentConfigSync{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspace.ID+"/agent-profiles", strings.NewReader(`{"name":"Forge","role":"developer"}`))
	req.Header.Set("Idempotency-Key", "create-non-durable")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "agent_config_sync_not_durable") {
		t.Fatalf("non-durable Agent create must fail closed: status=%d body=%s", rec.Code, rec.Body.String())
	}
	agents, err := store.Agents().List(ctx, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("non-durable Agent create changed DB: %+v", agents)
	}
}

func TestPatchAgentDurableConfigIntentReconcilesBeforeResponse(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_agent_durable_http", Name: "agent durable", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_durable_http", WorkspaceID: workspace.ID, Slug: "forge", Name: "Forge", Role: "developer",
		Instructions: "old", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, nil)
	server.SetAgentConfigSync(agentconfig.NewImporter(dir, store))
	body := `{"instructions":"new","expected_version":1}`
	first := patchAgentConfigBodyWithID(t, server, "agent_durable_http", "durable-agent-patch", body)
	if first.Code != http.StatusOK {
		t.Fatalf("durable Agent PATCH status=%d body=%s", first.Code, first.Body.String())
	}
	if _, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("successful request must leave no active intent, got %v", err)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "forge", "prompt.md"))
	if err != nil || string(prompt) != "new" {
		t.Fatalf("durable prompt write = %q, err=%v", prompt, err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, ".sync", agent.ID+".json"))
	if err != nil || !strings.Contains(string(manifest), `"state":"complete"`) {
		t.Fatalf("complete bundle manifest missing: %s err=%v", manifest, err)
	}
	replay := patchAgentConfigBodyWithID(t, server, "agent_durable_http", "durable-agent-patch", body)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same hash must replay persisted response: status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	conflict := patchAgentConfigBodyWithID(t, server, "agent_durable_http", "durable-agent-patch", `{"instructions":"other","expected_version":2}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("different hash with same key must conflict: %d %s", conflict.Code, conflict.Body.String())
	}
}

func patchAgentConfigBodyWithID(t *testing.T, server *Server, agentID, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/agent-profiles/"+agentID, strings.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	return rec
}

func TestPatchAgentDurableFailureRetainsIntentAndSameKeyCanRetry(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_agent_durable_retry", Name: "agent durable retry", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_durable_retry", WorkspaceID: workspace.ID, Slug: "forge", Name: "Forge", Role: "developer",
		Instructions: "old", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	// A non-directory at the target slug forces a real external write failure
	// while leaving the DB intent committed and retryable.
	if err := os.WriteFile(filepath.Join(root, "forge"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, nil)
	server.SetAgentConfigSync(agentconfig.NewImporter(root, store))
	body := `{"instructions":"new","expected_version":1}`
	first := patchAgentConfigBodyWithID(t, server, agent.ID, "durable-retry", body)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first external failure status=%d body=%s", first.Code, first.Body.String())
	}
	intent, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID)
	if err != nil || intent.Status != domain.AgentConfigSyncFailed {
		t.Fatalf("failed intent must remain active: intent=%+v err=%v", intent, err)
	}
	if err := os.Remove(filepath.Join(root, "forge")); err != nil {
		t.Fatal(err)
	}
	second := patchAgentConfigBodyWithID(t, server, agent.ID, "durable-retry", body)
	if second.Code != http.StatusOK {
		t.Fatalf("same key retry after fixing external condition status=%d body=%s", second.Code, second.Body.String())
	}
	if _, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("successful retry must seal intent, got %v", err)
	}
}

func TestCreateAgentDurableSyncFailureSameKeyDoesNotDuplicateAgent(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_agent_create_retry", Name: "agent create retry", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, nil)
	server.SetAgentConfigSync(agentconfig.NewImporter(blocked, store))
	body := `{"name":"New Agent","role":"developer"}`
	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspace.ID+"/agent-profiles", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "create-agent-retry")
	server.Routes().ServeHTTP(first, request)
	if first.Code != http.StatusAccepted {
		t.Fatalf("external create failure should be durably accepted for retry: status=%d body=%s", first.Code, first.Body.String())
	}
	agents, err := store.Agents().List(ctx, workspace.ID)
	if err != nil || len(agents) != 1 {
		t.Fatalf("first create must persist exactly one Agent: agents=%+v err=%v", agents, err)
	}
	replay := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspace.ID+"/agent-profiles", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "create-agent-retry")
	server.Routes().ServeHTTP(replay, request)
	if replay.Code != http.StatusAccepted || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("same key must replay pending create without re-executing: status=%d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	agents, err = store.Agents().List(ctx, workspace.ID)
	if err != nil || len(agents) != 1 {
		t.Fatalf("same key replay created a duplicate Agent: agents=%+v err=%v", agents, err)
	}
}

func TestAgentConfigFailureResponseDoesNotLeakExternalPath(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_agent_path_redact", Name: "agent path redact", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{ID: "agent_path_redact", WorkspaceID: workspace.ID, Slug: "forge", Name: "Forge", Role: "developer", Instructions: "old", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, nil)
	server.SetAgentConfigSync(agentconfig.NewImporter(blocked, store))
	rec := patchAgentConfigBodyWithID(t, server, agent.ID, "path-redact", `{"instructions":"new","expected_version":1}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected durable sync failure: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), blocked) || strings.Contains(rec.Body.String(), filepath.Clean(blocked)) {
		t.Fatalf("external path leaked in HTTP failure body: %s", rec.Body.String())
	}
}

func TestAgentConfigReloadFailureResponseDoesNotLeakExternalPath(t *testing.T) {
	ctx := context.Background()
	store := openIdempotencyTestDB(t)
	now := time.Now().UTC()
	workspace := &domain.Workspace{ID: "ws_agent_reload_redact", Name: "agent reload redact", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "agent-config-root")
	forge := filepath.Join(root, "forge")
	if err := os.MkdirAll(filepath.Join(forge, "prompt.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forge, "agent.yaml"), []byte("name: Forge\nrole: developer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, planTestNotifier{}, atwruntime.NewRegistry())
	server := NewServer(svc, store, nil)
	server.SetAgentConfigSync(agentconfig.NewImporter(root, store))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/"+workspace.ID+"/agent-config/reload", nil)
	req.Header.Set("Idempotency-Key", "reload-path-redact")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), root) {
		t.Fatalf("reload must redact absolute config path: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
