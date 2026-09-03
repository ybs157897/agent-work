package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/agentconfig"
	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestAgentPatchDurableIntentReplaysAndSealsAfterExternalBundle(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_intent", Name: "intent", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_intent", WorkspaceID: ws.ID, Name: "Forge", Role: "developer",
		Instructions: "old", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	changed := "new"
	updated, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Instructions: &changed, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Slug != "agent-intent" {
		t.Fatalf("Agent durable target must assign a stable unique slug: %+v", updated)
	}
	intent, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetVersion != updated.Version || intent.Status != domain.AgentConfigSyncPending || intent.TargetDigest == "" {
		t.Fatalf("unexpected durable intent: %+v", intent)
	}
	if intent.TargetSnapshot == "" || filepath.IsAbs(intent.TargetSnapshot) {
		t.Fatalf("intent target snapshot should be embedded non-sensitive JSON: %q", intent.TargetSnapshot)
	}

	// A new target cannot leapfrog a pending external effect.
	other := "different"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Instructions: &other, ExpectedVersion: 2}); !errors.Is(err, domain.ErrAgentConfigSyncPending) {
		t.Fatalf("pending target must block a different patch, got %v", err)
	}

	dir := t.TempDir()
	importer := agentconfig.NewImporter(dir, store)
	if _, err := importer.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("successful reconciliation must seal active intent, got %v", err)
	}
	eventsBeforeReplay, err := store.Events().Since(ctx, ws.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{
		Instructions: &changed, ExpectedVersion: 1,
	})
	if err != nil || replayed.Version != updated.Version {
		t.Fatalf("crash retry after applied intent must return the existing target: agent=%+v err=%v", replayed, err)
	}
	eventsAfterReplay, err := store.Events().Since(ctx, ws.ID, 0, 100)
	if err != nil || len(eventsAfterReplay) != len(eventsBeforeReplay) {
		t.Fatalf("applied-intent crash retry must not append another Agent event: before=%d after=%d err=%v",
			len(eventsBeforeReplay), len(eventsAfterReplay), err)
	}
	different := "different after applied"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{
		Instructions: &different, ExpectedVersion: 1,
	}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("a different stale patch must not use semantic replay: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "agent-intent", "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || !strings.Contains(string(data), "name: Forge") {
		t.Fatalf("reconciled Agent YAML missing target: %s", data)
	}
	prompt, err := os.ReadFile(filepath.Join(dir, "agent-intent", "prompt.md"))
	if err != nil || string(prompt) != changed {
		t.Fatalf("reconciled prompt = %q, err=%v", prompt, err)
	}

}

func TestAgentConfigIntentWriteFailureIsRetainedForRetry(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_intent_fail", Name: "intent fail", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_intent_fail", WorkspaceID: ws.ID, Slug: "forge", Name: "Forge", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	value := "target"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Instructions: &value, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentconfig.NewImporter(blocked, store).Reconcile(ctx); err == nil {
		t.Fatal("external bundle write failure must be returned")
	}
	intent, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != domain.AgentConfigSyncFailed || intent.Attempts != 1 || intent.LastError == "" {
		t.Fatalf("write failure must retain retryable intent metadata: %+v", intent)
	}
	if strings.Contains(intent.LastError, blocked) {
		t.Fatalf("intent diagnostic must not persist absolute agent-config path: %q", intent.LastError)
	}
}

type failingAgentConfigIntentRepo struct {
	application.AgentConfigSyncIntentRepo
	err error
}

func (r failingAgentConfigIntentRepo) Create(context.Context, *domain.AgentConfigSyncIntent) error {
	return r.err
}

type failingAgentConfigIntentStore struct {
	application.Store
	repo application.AgentConfigSyncIntentRepo
}

func (s failingAgentConfigIntentStore) AgentConfigSyncIntents() application.AgentConfigSyncIntentRepo {
	return s.repo
}

func TestAgentPatchRollsBackCASAndEventWhenIntentCreateFails(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	base := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_intent_tx", Name: "intent tx", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := base.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{ID: "agent_intent_tx", WorkspaceID: ws.ID, Slug: "forge", Name: "Forge", Role: "developer", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := base.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	intentErr := errors.New("injected intent insert failure")
	store := failingAgentConfigIntentStore{Store: base, repo: failingAgentConfigIntentRepo{AgentConfigSyncIntentRepo: base.AgentConfigSyncIntents(), err: intentErr}}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	name := "changed"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Name: &name, ExpectedVersion: 1}); !errors.Is(err, intentErr) {
		t.Fatalf("intent create error must abort Agent update: %v", err)
	}
	got, err := base.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != agent.Name || got.Version != agent.Version {
		t.Fatalf("Agent CAS escaped failed intent transaction: got=%+v want=%+v", got, agent)
	}
	events, err := base.Events().Since(ctx, ws.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("Agent event escaped failed intent transaction: %+v", events)
	}
}

func TestAgentConfigIntentConflictFailsClosed(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_intent_conflict", Name: "intent conflict", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{ID: "agent_intent_conflict", WorkspaceID: ws.ID, Slug: "forge", Name: "Forge", Role: "developer", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	value := "target"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Instructions: &value, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	current, err := store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Instructions = "out-of-band"
	if err := store.Agents().Update(ctx, current, current.Version); err != nil {
		t.Fatal(err)
	}
	importer := agentconfig.NewImporter(t.TempDir(), store)
	if _, err := importer.Reconcile(ctx); !errors.Is(err, domain.ErrAgentConfigSyncConflict) {
		t.Fatalf("target/current drift must fail closed: %v", err)
	}
	intent, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != domain.AgentConfigSyncConflict {
		t.Fatalf("conflicting intent must remain visible: %+v", intent)
	}
}

func TestAgentConfigBundleManifestRemainsStagedOnPartialWrite(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_intent_stage", Name: "intent stage", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{ID: "agent_intent_stage", WorkspaceID: ws.ID, Slug: "forge", Name: "Forge", Role: "developer", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	value := "target"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Instructions: &value, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "forge", "prompt.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := agentconfig.NewImporter(dir, store).Reconcile(ctx); err == nil {
		t.Fatal("partial bundle write must fail")
	}
	manifest, err := os.ReadFile(filepath.Join(dir, ".sync", agent.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), `"state":"staged"`) {
		t.Fatalf("partial write must leave staged manifest for replay: %s", manifest)
	}
	if err := os.RemoveAll(filepath.Join(dir, "forge", "prompt.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := agentconfig.NewImporter(dir, store).Reconcile(ctx); err != nil {
		t.Fatalf("a fresh process must replay the staged bundle: %v", err)
	}
	if _, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("restart replay must seal the staged intent: %v", err)
	}
	manifest, err = os.ReadFile(filepath.Join(dir, ".sync", agent.ID+".json"))
	if err != nil || !strings.Contains(string(manifest), `"state":"complete"`) {
		t.Fatalf("restart replay did not publish a complete manifest: %s err=%v", manifest, err)
	}
}

func TestAgentConfigIntentFreezesNonSensitiveResolvedModel(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_intent_model", Name: "intent model", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_intent_model", WorkspaceID: ws.ID, Slug: "forge", Name: "Forge", Role: "developer",
		ModelOverride: domain.ModelRef{Ref: "model-ref"}, Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.ModelResolver = func(ref string) (orchestrator.ModelSpec, bool) {
		if ref != "model-ref" {
			return orchestrator.ModelSpec{}, false
		}
		return orchestrator.ModelSpec{Ref: ref, ProviderID: "provider-ref", Provider: "openrouter", API: "openai-responses", Model: "model-v1", BaseURL: "https://example.invalid/v1", APIKeyEnv: "OPENROUTER_KEY"}, true
	}
	svc.EnableAgentConfigSyncIntents()
	preferred := "codex_local"
	updated, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: preferred}, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := intent.DecodeTarget()
	if err != nil {
		t.Fatal(err)
	}
	if target.ResolvedModel == nil || target.ResolvedModel.APIKeyEnv != "OPENROUTER_KEY" || target.ResolvedModel.BaseURL == "" {
		t.Fatalf("intent should freeze non-sensitive model routing fields: %+v", target.ResolvedModel)
	}
	if strings.Contains(intent.TargetSnapshot, "sk-") || strings.Contains(intent.TargetSnapshot, "/Users/") || strings.Contains(intent.TargetSnapshot, "OPENROUTER_SECRET_VALUE") {
		t.Fatalf("intent snapshot contains secret or absolute home: %s", intent.TargetSnapshot)
	}
	if _, err := agentconfig.NewImporter(t.TempDir(), store).Reconcile(ctx); !errors.Is(err, domain.ErrCapabilityMissing) {
		t.Fatalf("runtime-target intent without a configured project home must fail closed: %v", err)
	}
	intent, err = store.AgentConfigSyncIntents().GetActiveByAgent(ctx, updated.ID)
	if err != nil || intent.Status != domain.AgentConfigSyncFailed {
		t.Fatalf("missing runtime home must retain a retryable intent: intent=%+v err=%v", intent, err)
	}
}

func TestAgentPatchRejectsProtectedSystemCoordinator(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "protected Coordinator profile", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"system profile remains immutable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Agents().Get(ctx, state.CoordinatorAgentID)
	if err != nil {
		t.Fatal(err)
	}
	name := "mutated system profile"
	if _, err := svc.UpdateAgent(ctx, before.ID, application.AgentPatch{
		Name: &name, ExpectedVersion: before.Version,
	}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("system Coordinator profile must reject generic Agent PATCH: %v", err)
	}
	after, err := store.Agents().Get(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name || after.Version != before.Version {
		t.Fatalf("rejected system Agent PATCH changed the profile: before=%+v after=%+v", before, after)
	}
}

func TestAgentPatchRollsBackWhenRuntimeModelCannotBeFrozen(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_unresolved_model", Name: "unresolved model", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{
		ID: "agent_unresolved_model", WorkspaceID: ws.ID, Slug: "unresolved-model",
		Name: "Unresolved", Role: "developer", Availability: domain.AgentEnabled,
		Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "codex_local"},
		ModelOverride:     &domain.ModelRef{Ref: "missing-model"}, ExpectedVersion: 1,
	}); !errors.Is(err, domain.ErrCapabilityMissing) {
		t.Fatalf("unresolvable runtime model must fail before Agent/intent commit: %v", err)
	}
	after, err := store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != agent.Version || after.RuntimePreference.Preferred != "" {
		t.Fatalf("unresolvable model escaped the Agent transaction: before=%+v after=%+v", agent, after)
	}
	if _, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unresolvable model created an intent: %v", err)
	}
}

func TestAgentPatchRejectsStaticCodexTargetBeforeCommit(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_static_target", Name: "static target", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{ID: "agent_static_target", WorkspaceID: ws.ID, Slug: "forge", Name: "Forge", Role: "developer", ModelOverride: domain.ModelRef{Ref: "bad-codex-model"}, Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.ModelResolver = func(ref string) (orchestrator.ModelSpec, bool) {
		if ref != "bad-codex-model" {
			return orchestrator.ModelSpec{}, false
		}
		return orchestrator.ModelSpec{Ref: ref, ProviderID: "prov-bad", Provider: "custom", API: "openai-completions", Model: "custom-model", BaseURL: "https://example.invalid/v1", APIKeyEnv: "CUSTOM_KEY"}, true
	}
	svc.EnableAgentConfigSyncIntents()
	preferred := "codex_local"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{RuntimePreference: &domain.RuntimePreference{Preferred: preferred}, ExpectedVersion: 1}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("static Codex protocol mismatch must fail before commit: %v", err)
	}
	after, err := store.Agents().Get(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != agent.Version || after.RuntimePreference.Preferred != "" {
		t.Fatalf("invalid static target escaped transaction: before=%+v after=%+v", agent, after)
	}
	if _, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, agent.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("invalid static target must not create an intent: %v", err)
	}
}

func TestCreateAgentDurableIntentIsCreatedWithStableSlug(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_create_intent", Name: "create intent", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	a, err := svc.CreateAgent(ctx, ws.ID, application.CreateAgentParams{Name: "New Agent", Role: "developer"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Slug == "" {
		t.Fatalf("durable Agent creation must assign a stable slug: %+v", a)
	}
	intent, err := store.AgentConfigSyncIntents().GetActiveByAgent(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.TargetVersion != a.Version || intent.Status != domain.AgentConfigSyncPending {
		t.Fatalf("durable Agent creation intent mismatch: agent=%+v intent=%+v", a, intent)
	}
}

func TestAgentConfigBundleIsWorkspaceScopedWhenSlugsOverlap(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	workspaces := []*domain.Workspace{
		{ID: "ws_agent_scope_a", Name: "scope a", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "ws_agent_scope_b", Name: "scope b", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now},
	}
	for _, ws := range workspaces {
		if err := store.Workspaces().Create(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	agents := []*domain.AgentProfile{
		{ID: "agent_scope_a", WorkspaceID: workspaces[0].ID, Slug: "forge", Name: "Forge A", Role: "developer", Instructions: "workspace-a", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "agent_scope_b", WorkspaceID: workspaces[1].ID, Slug: "forge", Name: "Forge B", Role: "developer", Instructions: "workspace-b", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now},
	}
	for _, a := range agents {
		if err := store.Agents().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	for _, a := range agents {
		value := a.Instructions + " updated"
		if _, err := svc.UpdateAgent(ctx, a.ID, application.AgentPatch{Instructions: &value, ExpectedVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	if _, err := agentconfig.NewImporter(dir, store).Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		path := filepath.Join(dir, "workspaces", a.WorkspaceID, "forge", "prompt.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("workspace-scoped bundle missing for %s: %v", a.WorkspaceID, err)
		}
		if !strings.Contains(string(data), "updated") {
			t.Fatalf("workspace-scoped bundle for %s has wrong prompt: %q", a.WorkspaceID, data)
		}
	}
}

func TestAgentConfigBundleRejectsSlugSymlink(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_agent_symlink", Name: "symlink", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	agent := &domain.AgentProfile{ID: "agent_symlink", WorkspaceID: ws.ID, Slug: "forge", Name: "Forge", Role: "developer", Instructions: "secret", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, atwruntime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	updatedText := "updated secret"
	if _, err := svc.UpdateAgent(ctx, agent.ID, application.AgentPatch{Instructions: &updatedText, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "forge")); err != nil {
		t.Fatal(err)
	}
	if _, err := agentconfig.NewImporter(dir, store).Reconcile(ctx); err == nil {
		t.Fatal("slug symlink must be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "agent.yaml")); !os.IsNotExist(err) {
		t.Fatalf("slug symlink escaped into external directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "prompt.md")); !os.IsNotExist(err) {
		t.Fatalf("slug symlink escaped into external directory: %v", err)
	}
}
