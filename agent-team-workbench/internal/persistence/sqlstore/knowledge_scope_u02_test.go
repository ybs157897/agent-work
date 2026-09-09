package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestKnowledgeReadScopeRejectsAgentFromAnotherWorkspace(t *testing.T) {
	_, store, wsA, agentA, _ := knowledgeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	wsB := &domain.Workspace{ID: domain.NewID(domain.PrefixWorkspace), Name: "workspace-b", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, wsB); err != nil {
		t.Fatal(err)
	}
	agentB := &domain.AgentProfile{ID: domain.NewID(domain.PrefixAgent), WorkspaceID: wsB.ID, Name: agentA.Name, Role: agentA.Role, Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, agentB); err != nil {
		t.Fatal(err)
	}
	item := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), wsA.ID, agentA.ID, domain.KnowledgeVisibilityWorkspace, "跨空间隔离", nil)
	if err := store.Knowledge().CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	version := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), item.ID, agentA.ID, item.Title, "只能在 A 空间读取", 0)
	if err := store.Knowledge().CreateVersion(ctx, version); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, version.ID, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().RebuildIndex(ctx, wsA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Knowledge().GetItem(ctx, wsA.ID, agentB.ID, item.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace Agent read = %v, want not found", err)
	}
	if _, _, err := store.Knowledge().ListVisibleItemsPage(ctx, wsA.ID, agentB.ID, domain.KnowledgeListOptions{}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace Agent list = %v, want not found", err)
	}
	if _, err := store.Knowledge().Search(ctx, &domain.KnowledgeQuery{WorkspaceID: wsA.ID, RequesterAgentID: agentB.ID, Terms: []string{"跨空间"}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace Agent search = %v, want not found", err)
	}
	snapshot := &domain.KnowledgeQuerySnapshot{ID: domain.NewID(domain.PrefixKnowledgeQuerySnapshot), WorkspaceID: wsA.ID, RequesterAgentID: agentA.ID, Question: "隔离", Budget: domain.KnowledgeBudget{MaxResults: 1}, Coverage: domain.KnowledgeCoverage{Status: domain.KnowledgeCoveragePartial}}
	if err := store.Knowledge().CreateQuerySnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Knowledge().GetQuerySnapshot(ctx, wsA.ID, agentB.ID, snapshot.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace Agent snapshot read = %v, want not found", err)
	}
	if err := store.Knowledge().CreateQuerySnapshot(ctx, &domain.KnowledgeQuerySnapshot{ID: domain.NewID(domain.PrefixKnowledgeQuerySnapshot), WorkspaceID: wsA.ID, RequesterAgentID: agentB.ID, Question: "伪造", Budget: domain.KnowledgeBudget{MaxResults: 1}, Coverage: domain.KnowledgeCoverage{Status: domain.KnowledgeCoveragePartial}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace Agent snapshot write = %v, want not found", err)
	}
}
