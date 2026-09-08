package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestKnowledgeConfigTakesOverBuiltinAndDefaultsEnabled(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, nil, nil, atwruntime.NewRegistry())
	now := time.Now().UTC()
	workspaceID := "ws_knowledge_builtin_config"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{ID: workspaceID, Name: "builtin config", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	legacy := &domain.AgentProfile{ID: "agent_legacy_config", WorkspaceID: workspaceID, Name: "legacy", Role: "librarian", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := store.KnowledgeJobs().CreateConfig(ctx, &domain.KnowledgeLibrarianConfig{WorkspaceID: workspaceID, LibrarianAgentID: legacy.ID, Enabled: false, AutoCollect: true, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, workspaceID); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.GetKnowledgeLibrarianConfig(ctx, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	wantBuiltin := domain.KnowledgeLibrarianAgentID(workspaceID)
	if cfg.LibrarianAgentID != wantBuiltin || cfg.Enabled || !cfg.AutoCollect || cfg.Version != 2 {
		t.Fatalf("legacy config was not CAS-repointed while preserving policy: %+v", cfg)
	}

	newWorkspaceID := "ws_knowledge_builtin_default"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{ID: newWorkspaceID, Name: "builtin default", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureBuiltinKnowledgeLibrarian(ctx, newWorkspaceID); err != nil {
		t.Fatal(err)
	}
	defaultCfg, err := svc.GetKnowledgeLibrarianConfig(ctx, newWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if defaultCfg.LibrarianAgentID != domain.KnowledgeLibrarianAgentID(newWorkspaceID) || !defaultCfg.Enabled || defaultCfg.Version != 1 {
		t.Fatalf("new workspace did not receive enabled builtin default: %+v", defaultCfg)
	}

	updated, err := svc.ConfigureKnowledgeLibrarian(ctx, workspaceID, domain.KnowledgeLibrarianConfig{WorkspaceID: workspaceID, LibrarianAgentID: legacy.ID, Enabled: true, AutoCollect: cfg.AutoCollect, Version: cfg.Version})
	if err != nil {
		t.Fatal(err)
	}
	if updated.LibrarianAgentID != wantBuiltin || updated.Version != cfg.Version+1 || !updated.Enabled {
		t.Fatalf("configure allowed legacy Agent target to survive: %+v", updated)
	}
}
