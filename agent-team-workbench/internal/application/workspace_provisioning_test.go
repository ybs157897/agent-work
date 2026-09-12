package application_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestCreateWorkspaceWithProjectCopiesSystemConfigurationWithoutBusinessState(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := newProvisioningStore(t, db)
	svc := application.NewService(store, nil, noopNotifier{}, runtime.NewRegistry())
	svc.EnableAgentConfigSyncIntents()
	now := time.Now().UTC()

	sourceID := "ws_provision_source"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{ID: sourceID, Name: "source", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SeedWorkspaceLocation(ctx, store, sourceID); err != nil {
		t.Fatal(err)
	}
	ordinary := &domain.AgentProfile{
		ID: "agent_source_developer", WorkspaceID: sourceID, Kind: domain.AgentProfileKindUser,
		Slug: "developer", Name: "Developer", Role: "developer", Skills: []string{"java"},
		Instructions: "source developer", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock", Fallbacks: []string{"kimi_local"}, Mode: "plan", AgentPreset: "code"},
		ModelOverride:     domain.ModelRef{Ref: "source-dev-model", Provider: "source-provider", Model: "source-dev", ReasoningEffort: "high"},
		Policy:            domain.AgentPolicy{Tools: []string{"read"}, ApprovalPolicy: domain.ApprovalPolicyManual, Sandbox: "read-only"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, ordinary); err != nil {
		t.Fatal(err)
	}

	coordinator, err := store.TaskCoordinators().EnsureConfig(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.RuntimeLabel = "kimi_local"
	coordinator.FallbackRuntimeLabel = "codex_local"
	coordinator.ModelRef = domain.ModelRef{Ref: "source-coordinator", Provider: "source-provider", Model: "source-coordinator-model", ReasoningEffort: "high"}
	coordinator.FallbackModelRef = domain.ModelRef{Ref: "source-coordinator-fallback", Provider: "fallback-provider", Model: "source-fallback-model", ReasoningEffort: "low"}
	coordinator.ReasoningEffort = "high"
	if err := store.TaskCoordinators().UpdateConfig(ctx, coordinator, coordinator.Version); err != nil {
		t.Fatal(err)
	}
	sourceCoordinator, err := store.TaskCoordinators().GetConfig(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}

	sourceLibrarian, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateAgent(ctx, sourceLibrarian.ID, application.AgentPatch{
		RuntimePreference: &domain.RuntimePreference{Preferred: "kimi_local", Fallbacks: []string{"mock"}, Mode: "default", AgentPreset: "knowledge"},
		ModelOverride:     &domain.ModelRef{Ref: "source-librarian", Provider: "source-provider", Model: "source-librarian-model", ReasoningEffort: "xhigh"},
		ExpectedVersion:   sourceLibrarian.Version,
	}); err != nil {
		t.Fatal(err)
	}
	sourceLibrarian, err = store.Agents().Get(ctx, sourceLibrarian.ID)
	if err != nil {
		t.Fatal(err)
	}

	targetID := "ws_provision_target"
	targetLocationID := "wsloc_provision_target"
	result, err := svc.CreateWorkspaceWithProject(ctx, application.CreateWorkspaceParams{
		Workspace: &domain.Workspace{ID: targetID, Name: "target", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now},
		Location: &domain.WorkspaceLocation{
			ID: targetLocationID, WorkspaceID: targetID, ExecutionHostID: domain.LocalHostID,
			MountAlias: "default", MountGeneration: "gen_seed", RepositoryIdentity: "repo_default",
			IsDefault: true, Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Project: &domain.WorkspaceProject{
			WorkspaceID: targetID, ExecutionHostID: domain.LocalHostID, MountAlias: "default",
			MountGeneration: "gen_seed", RepositoryIdentity: "repo_default",
			CanonicalKey: domain.WorkspaceProjectCanonicalKey(domain.LocalHostID, "default"), LocationID: targetLocationID,
			Status: domain.WorkspaceProjectReady, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		SourceWorkspaceID: sourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Agents) != 1 || result.Agents[0].ID == ordinary.ID || result.Agents[0].WorkspaceID != targetID {
		t.Fatalf("ordinary Agent should be cloned as an independent target profile: %+v", result.Agents)
	}
	clonedOrdinary := result.Agents[0]
	if clonedOrdinary.RuntimePreference.Preferred != ordinary.RuntimePreference.Preferred ||
		clonedOrdinary.RuntimePreference.Mode != ordinary.RuntimePreference.Mode ||
		clonedOrdinary.ModelOverride != ordinary.ModelOverride ||
		len(clonedOrdinary.Policy.Tools) != 1 || clonedOrdinary.Policy.Tools[0] != "read" {
		t.Fatalf("ordinary Agent configuration was not cloned: source=%+v target=%+v", ordinary, clonedOrdinary)
	}

	targetCoordinator, err := store.TaskCoordinators().GetConfig(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if targetCoordinator.AgentProfileID == sourceCoordinator.AgentProfileID ||
		targetCoordinator.RuntimeLabel != sourceCoordinator.RuntimeLabel ||
		targetCoordinator.FallbackRuntimeLabel != sourceCoordinator.FallbackRuntimeLabel ||
		targetCoordinator.ModelRef != sourceCoordinator.ModelRef ||
		targetCoordinator.FallbackModelRef != sourceCoordinator.FallbackModelRef ||
		targetCoordinator.ReasoningEffort != sourceCoordinator.ReasoningEffort {
		t.Fatalf("Coordinator configuration or identity was not copied correctly: source=%+v target=%+v", sourceCoordinator, targetCoordinator)
	}
	targetCoordinatorAgent, err := store.Agents().Get(ctx, targetCoordinator.AgentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if targetCoordinatorAgent.Instructions != "" || targetCoordinatorAgent.PromptVersion != domain.TaskCoordinatorPromptVersion ||
		targetCoordinatorAgent.InstructionsEditable || targetCoordinatorAgent.PromptTemplate != "" ||
		targetCoordinatorAgent.Policy.Sandbox != "read-only" || len(targetCoordinatorAgent.Policy.Tools) != 0 {
		t.Fatalf("Coordinator protected defaults must remain target-owned: %+v", targetCoordinatorAgent)
	}

	targetLibrarian, err := store.Agents().Get(ctx, domain.KnowledgeLibrarianAgentID(targetID))
	if err != nil {
		t.Fatal(err)
	}
	if targetLibrarian.ID == sourceLibrarian.ID || targetLibrarian.RuntimePreference.Preferred != sourceLibrarian.RuntimePreference.Preferred ||
		targetLibrarian.RuntimePreference.AgentPreset != sourceLibrarian.RuntimePreference.AgentPreset ||
		targetLibrarian.ModelOverride != sourceLibrarian.ModelOverride ||
		targetLibrarian.Instructions != application.KnowledgeLibrarianHarnessPrompt ||
		targetLibrarian.PromptVersion != application.KnowledgeLibrarianHarnessPromptVersion || targetLibrarian.InstructionsEditable ||
		targetLibrarian.PromptTemplate != "" || targetLibrarian.Policy.Sandbox != "workspace-write" || len(targetLibrarian.Policy.Tools) != 0 {
		t.Fatalf("Knowledge Librarian config/identity boundary mismatch: source=%+v target=%+v", sourceLibrarian, targetLibrarian)
	}

	reloadedSvc := application.NewService(store, nil, noopNotifier{}, runtime.NewRegistry())
	if err := reloadedSvc.EnsureBuiltinAgents(ctx); err != nil {
		t.Fatal(err)
	}
	reloadedCoordinator, err := store.TaskCoordinators().EnsureConfig(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedCoordinator.ModelRef != targetCoordinator.ModelRef || reloadedCoordinator.FallbackModelRef != targetCoordinator.FallbackModelRef ||
		reloadedCoordinator.ReasoningEffort != targetCoordinator.ReasoningEffort {
		t.Fatalf("Ensure/reload must retain inherited system configuration: coordinator=%+v", reloadedCoordinator)
	}

	for _, table := range []string{
		"work_items", "execution_runs", "task_sessions", "knowledge_libraries", "knowledge_library_sources",
		"knowledge_write_tasks", "knowledge_library_events", "knowledge_releases", "knowledge_documents",
		"knowledge_assertions", "chat_sources", "chat_analyses",
		"chat_analysis_attempts", "chat_analysis_revisions", "chat_analysis_answers", "chat_analysis_decisions",
		"task_publication_drafts", "task_publications", "task_coordinator_states", "task_coordinator_events",
	} {
		assertWorkspaceRowsEmpty(t, db, table, targetID)
	}
}

func newProvisioningStore(t *testing.T, db *sql.DB) application.Store {
	t.Helper()
	store := sqlstore.New(db)
	return store
}

func assertWorkspaceRowsEmpty(t *testing.T, db *sql.DB, table, workspaceID string) {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("inspect %s: %v", table, err)
	}
	hasWorkspaceID := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			t.Fatalf("inspect %s row: %v", table, err)
		}
		if name == "workspace_id" {
			hasWorkspaceID = true
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var count int
	query := "SELECT COUNT(*) FROM " + table
	var queryArgs []any
	if hasWorkspaceID {
		query += " WHERE workspace_id=?"
		queryArgs = append(queryArgs, workspaceID)
	}
	if err := db.QueryRow(query, queryArgs...).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != 0 {
		t.Fatalf("workspace provisioning must not copy %s, found %d rows", table, count)
	}
}
