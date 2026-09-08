package sqlstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestKnowledgeLibrarianProfileIsVisibleSingletonAndProtected(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	now := time.Now().UTC()
	profile := &domain.AgentProfile{
		ID: domain.KnowledgeLibrarianAgentID("ws_wk"), WorkspaceID: "ws_wk",
		Kind: domain.AgentProfileKindKnowledgeLibrarian, Slug: "knowledge-librarian",
		Name: domain.KnowledgeLibrarianDisplayName, Role: domain.KnowledgeLibrarianRole,
		Instructions: "fixed", PromptVersion: domain.KnowledgeLibrarianChatPromptVersion,
		Policy: domain.AgentPolicy{Sandbox: "read-only"}, Availability: domain.AgentEnabled,
		Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	profiles, err := store.Agents().List(ctx, "ws_wk")
	if err != nil || len(profiles) != 1 || profiles[0].Kind != domain.AgentProfileKindKnowledgeLibrarian {
		t.Fatalf("knowledge librarian should be visible in Agent roster: profiles=%+v err=%v", profiles, err)
	}
	if _, err := db.Exec(`UPDATE agent_profiles SET instructions='tampered' WHERE id=?`, profile.ID); err == nil {
		t.Fatal("knowledge librarian prompt must be protected")
	}
	second := *profile
	second.ID = "agent_knowledge_librarian_ws_wk_other"
	if err := store.Agents().Create(ctx, &second); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("workspace must have one knowledge librarian, got %v", err)
	}
}
