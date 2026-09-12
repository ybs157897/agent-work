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

func seedLibrarianWorkspace(t *testing.T, ctx context.Context, store *sqlstore.Store, workspaceID string) time.Time {
	t.Helper()
	now := time.Now().UTC()
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: workspaceID, Name: workspaceID, Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return now
}

// TestEnsureBuiltinKnowledgeLibrarianContract pins the one workspace-scoped
// system library agent: deterministic identity, harness prompt, and a policy
// that lets it write inside the workspace while the harness only ingests its
// staging directory.
func TestEnsureBuiltinKnowledgeLibrarianContract(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	workspaceID := "ws_builtin_librarian"
	seedLibrarianWorkspace(t, ctx, store, workspaceID)
	svc := application.NewService(store, nil, noopNotifier{}, runtime.NewRegistry())

	first, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != domain.KnowledgeLibrarianAgentID(workspaceID) ||
		first.Kind != domain.AgentProfileKindKnowledgeLibrarian ||
		first.PromptVersion != application.KnowledgeLibrarianHarnessPromptVersion ||
		first.InstructionsEditable ||
		first.Instructions != application.KnowledgeLibrarianHarnessPrompt ||
		first.Policy.Sandbox != "workspace-write" ||
		len(first.Policy.Tools) != 0 ||
		first.Policy.ApprovalPolicy != "auto" {
		t.Fatalf("builtin library agent profile mismatch: %+v", first)
	}
	second, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Version != first.Version {
		t.Fatalf("EnsureBuiltinKnowledgeLibrarian is not idempotent: first=%+v second=%+v", first, second)
	}
}

// TestLibraryAgentPresentationOverlaysStaleText proves an existing workspace
// receives the current harness prompt without a destructive data rewrite: the
// SQLite protection trigger keeps the persisted row fixed while the read path
// overlays the current contract.
func TestLibraryAgentPresentationOverlaysStaleText(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	workspaceID := "ws_builtin_prompt_overlay"
	seedLibrarianWorkspace(t, ctx, store, workspaceID)
	svc := application.NewService(store, nil, noopNotifier{}, runtime.NewRegistry())
	librarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a row written by an earlier build. Dropping the trigger here
	// only lets the test construct that historical state.
	if _, err := db.Exec(`DROP TRIGGER agent_profiles_knowledge_librarian_protected`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_profiles SET instructions=? WHERE id=?`, "旧版管理员职责", librarian.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Agents().Get(ctx, librarian.ID)
	if err != nil || stored.Instructions == application.KnowledgeLibrarianHarnessPrompt {
		t.Fatalf("fixture did not create a stale persisted prompt: %+v err=%v", stored, err)
	}
	fresh, err := svc.Agent(ctx, librarian.ID)
	if err != nil || fresh.Instructions != application.KnowledgeLibrarianHarnessPrompt {
		t.Fatalf("single-Agent read did not refresh the library agent prompt: %+v err=%v", fresh, err)
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
	if listed == nil || listed.Instructions != application.KnowledgeLibrarianHarnessPrompt {
		t.Fatalf("agent roster did not refresh the library agent prompt: %+v", listed)
	}
}

// TestUpdateBuiltinKnowledgeLibrarianOnlyChangesRuntimeAndModel keeps the
// system identity immutable while allowing the operator to pick a runtime.
func TestUpdateBuiltinKnowledgeLibrarianOnlyChangesRuntimeAndModel(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	workspaceID := "ws_builtin_librarian_patch"
	seedLibrarianWorkspace(t, ctx, store, workspaceID)
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

// TestLibraryAgentPromptStaysInternal asserts the harness prompt describes the
// staging contract, which is what makes a fixed, checkable output possible.
func TestLibraryAgentPromptStaysInternal(t *testing.T) {
	prompt := application.KnowledgeLibrarianHarnessPrompt
	for _, want := range []string{"brief.md", "plan.json", "evidence.yaml", "暂存目录"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("harness prompt must describe %q: %s", want, prompt)
		}
	}
}
