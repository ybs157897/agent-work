package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestEnsureBuiltinKnowledgeLibrarianIsIdempotentAndKeepsLegacyAgent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	workspaceID := "ws_builtin_librarian"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: "builtin", Timezone: "UTC", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	legacy := &domain.AgentProfile{
		ID: "agent_legacy_librarian", WorkspaceID: workspaceID,
		Name: "旧知识管理员", Role: "librarian",
		RuntimePreference: domain.RuntimePreference{Preferred: "kimi_local", Fallbacks: []string{"mock"}, Mode: "plan"},
		ModelOverride:     domain.ModelRef{Ref: "kimi-fast", Provider: "kimi", Model: "kimi-test"},
		Availability:      domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := store.KnowledgeJobs().CreateConfig(ctx, &domain.KnowledgeLibrarianConfig{
		WorkspaceID: workspaceID, LibrarianAgentID: legacy.ID, Enabled: false,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, runtime.NewRegistry())

	first, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != domain.KnowledgeLibrarianAgentID(workspaceID) ||
		first.Kind != domain.AgentProfileKindKnowledgeLibrarian ||
		first.Name != domain.KnowledgeLibrarianDisplayName || first.Role != domain.KnowledgeLibrarianRole ||
		first.PromptVersion != domain.KnowledgeLibrarianChatPromptVersion || first.InstructionsEditable ||
		first.Instructions == "" || first.RuntimePreference.Preferred != "kimi_local" ||
		first.RuntimePreference.Mode != "default" || first.ModelOverride.Model != "kimi-test" {
		t.Fatalf("builtin librarian profile mismatch: %+v", first)
	}
	second, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Version != first.Version {
		t.Fatalf("EnsureBuiltinKnowledgeLibrarian is not idempotent: first=%+v second=%+v", first, second)
	}
	profiles, err := store.Agents().List(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("public Agent roster should include ordinary + builtin librarian, got %+v", profiles)
	}
	kept, err := store.Agents().Get(ctx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Kind != domain.AgentProfileKindUser || kept.Name != legacy.Name {
		t.Fatalf("legacy librarian must remain an ordinary Agent: %+v", kept)
	}
	cfg, err := store.KnowledgeJobs().GetConfig(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LibrarianAgentID != legacy.ID {
		t.Fatalf("profile provisioning must not rewrite the knowledge config owned by the knowledge service: %+v", cfg)
	}
}

func TestUpdateBuiltinKnowledgeLibrarianOnlyChangesRuntimeAndModel(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	workspaceID := "ws_builtin_librarian_patch"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: "builtin patch", Timezone: "UTC", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, runtime.NewRegistry())
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock", Mode: "default"},
		ModelOverride:     &domain.ModelRef{Provider: "mock", Model: "mock-model"},
		ExpectedVersion:   librarian.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != librarian.Version+1 || updated.Kind != domain.AgentProfileKindKnowledgeLibrarian ||
		updated.Name != domain.KnowledgeLibrarianDisplayName || updated.Role != domain.KnowledgeLibrarianRole ||
		updated.RuntimePreference.Preferred != "mock" || updated.ModelOverride.Model != "mock-model" {
		t.Fatalf("builtin runtime/model patch mismatch: %+v", updated)
	}
	name := "越权名称"
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{Name: &name, ExpectedVersion: updated.Version}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("builtin identity patch must be rejected: %v", err)
	}
	policy := domain.AgentPolicy{Sandbox: "danger-full-access"}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{Policy: &policy, ExpectedVersion: updated.Version}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("builtin policy patch must be rejected: %v", err)
	}
	if _, err := svc.SetAgentAvailability(ctx, librarian.ID, false); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("builtin availability patch must be rejected: %v", err)
	}
}

func TestBuiltinKnowledgeLibrarianCanOwnNaturalLanguageChatButCoordinatorCannot(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	now := time.Now().UTC()
	workspaceID := "ws_builtin_chat"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: "builtin chat", Timezone: "UTC", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SeedWorkspaceLocation(ctx, store, workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_builtin_chat_mock", WorkspaceID: workspaceID, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingReady, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc := application.NewService(store, nil, noopNotifier{}, runtime.NewRegistry())
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, librarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "mock"}, ExpectedVersion: librarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate a profile created by an earlier build. The production trigger
	// protects the fixed prompt; dropping it here only lets the test construct
	// that historical database state, so the application-level presentation
	// overlay can be exercised without changing the migration contract.
	if _, err := db.Exec(`DROP TRIGGER agent_profiles_knowledge_librarian_protected`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_profiles SET instructions=? WHERE id=?`, "旧版管理员职责", librarian.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Agents().Get(ctx, librarian.ID)
	if err != nil || stored.Instructions == application.KnowledgeLibrarianChatPrompt {
		t.Fatalf("test fixture did not create stale persisted prompt: %+v err=%v", stored, err)
	}
	fresh, err := svc.Agent(ctx, librarian.ID)
	if err != nil || fresh.Instructions != application.KnowledgeLibrarianChatPrompt {
		t.Fatalf("single-Agent read did not refresh built-in prompt: %+v err=%v", fresh, err)
	}
	roster, err := svc.Agents(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	var listed *domain.AgentProfile
	for _, candidate := range roster {
		if candidate.ID == librarian.ID {
			listed = candidate
			break
		}
	}
	if listed == nil || listed.Instructions != application.KnowledgeLibrarianChatPrompt {
		t.Fatalf("Agent roster did not refresh built-in prompt: %+v", listed)
	}
	chat, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "管理员对话", RecordKind: domain.RecordKindChat, AgentProfileID: librarian.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: librarian.ID, Instruction: "请用自然语言说明你能做什么",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.AgentProfileID != librarian.ID || run.Input["system_prompt"] != application.KnowledgeLibrarianChatPrompt {
		t.Fatalf("builtin Chat must use the public natural-language persona: %+v", run.Input)
	}
	publicPrompt := run.Input["system_prompt"].(string)
	for _, want := range []string{"产品、开发等智能体", "核对来源", "知识库页面用于浏览和搜索"} {
		if !strings.Contains(publicPrompt, want) {
			t.Fatalf("public librarian prompt missing user-facing guidance %q: %s", want, publicPrompt)
		}
	}
	for _, internal := range []string{"命令行", "confirmed_requirement", "knowledge-librarian/v1", "JSON"} {
		if strings.Contains(publicPrompt, internal) {
			t.Fatalf("public librarian prompt must not expose internal term %q: %s", internal, publicPrompt)
		}
	}
	if _, marked := run.Input["knowledge_librarian"]; marked {
		t.Fatal("public librarian Chat must not masquerade as an internal JSON curation Run")
	}

	coordinator, err := store.TaskCoordinators().EnsureConfig(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorChat, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "非法 Coordinator Chat", RecordKind: domain.RecordKindChat, AgentProfileID: coordinator.AgentProfileID,
	})
	if err == nil {
		if _, err = svc.CreateRun(ctx, coordinatorChat.ID, application.CreateRunParams{
			AgentProfileID: coordinator.AgentProfileID, Instruction: "越过控制线",
		}); err == nil {
			t.Fatal("Task Coordinator must remain excluded from ordinary Chat")
		}
	}
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Coordinator Chat must fail with validation, got %v", err)
	}
}
