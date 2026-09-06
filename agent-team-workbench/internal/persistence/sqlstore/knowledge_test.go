package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	_ "modernc.org/sqlite"
)

func knowledgeTestDB(t *testing.T) (*sql.DB, *Store, *domain.Workspace, *domain.AgentProfile, *domain.AgentProfile) {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/knowledge.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: domain.NewID(domain.PrefixWorkspace), Name: "knowledge-test", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	newAgent := func(name string) *domain.AgentProfile {
		return &domain.AgentProfile{ID: domain.NewID(domain.PrefixAgent), WorkspaceID: ws.ID, Name: name,
			Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
			Version: 1, CreatedAt: now, UpdatedAt: now}
	}
	a, b := newAgent("alpha"), newAgent("beta")
	if err := store.Agents().Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return db, store, ws, a, b
}

func knowledgeItem(id, workspaceID, ownerID string, visibility domain.KnowledgeVisibility, title string, scope domain.KnowledgeScope) *domain.KnowledgeItem {
	return &domain.KnowledgeItem{ID: id, WorkspaceID: workspaceID, OwnerAgentID: ownerID,
		Visibility: visibility, Kind: "feature", Title: title, Scope: scope, Version: 1}
}

func knowledgeVersion(id, itemID, agentID, title, body string, base int64) *domain.KnowledgeVersion {
	return &domain.KnowledgeVersion{ID: id, ItemID: itemID, BaseVersion: base,
		Status: domain.KnowledgeStatusCandidate, Kind: "feature", Title: title,
		BodyMarkdown: body, CreatedByAgentID: agentID}
}

func TestKnowledgePublishSearchScopeAndPrivateRelations(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	a := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "取消订单", domain.KnowledgeScope{"project": "orders"})
	b := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "库存释放", domain.KnowledgeScope{"project": "orders"})
	c := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "结算规则", domain.KnowledgeScope{"project": "billing"})
	for _, item := range []*domain.KnowledgeItem{a, b, c} {
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	vA := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), a.ID, alpha.ID,
		"取消订单", "取消订单会触发库存释放事件。", 0)
	vB := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), b.ID, alpha.ID,
		"库存释放", "库存服务消费取消订单事件。", 0)
	vC := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), c.ID, alpha.ID,
		"结算规则", "billing settlement only", 0)
	relationSource := &domain.KnowledgeSource{ID: domain.NewID(domain.PrefixKnowledgeSource), WorkspaceID: ws.ID,
		SubmittedByAgentID: alpha.ID, Kind: domain.KnowledgeSourceCode, Ref: "orders/cancel.go@abc123", Excerpt: "emits cancellation event"}
	if err := store.Knowledge().CreateVersionBundle(ctx, vA, []*domain.KnowledgeSource{relationSource}, []*domain.KnowledgeRelation{{
		ID: domain.NewID(domain.PrefixKnowledgeRelation), FromItemID: a.ID, ToItemID: b.ID,
		WorkspaceID: ws.ID, SourceVersionID: vA.ID, Kind: domain.KnowledgeRelationTriggers,
		Condition: "已预占库存", Rationale: "取消事件由库存服务消费",
		SourceIDs: []string{relationSource.ID},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().CreateVersion(ctx, vB); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().CreateVersion(ctx, vC); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, a.ID, vA.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, b.ID, vB.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, c.ID, vC.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	hits, err := store.Knowledge().Search(ctx, &domain.KnowledgeQuery{
		WorkspaceID: ws.ID, RequesterAgentID: beta.ID, Terms: []string{"订单"},
		Scope: domain.KnowledgeScope{"project": "orders"}, Budget: domain.KnowledgeBudget{MaxResults: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := make(map[string]bool, len(hits))
	for _, hit := range hits {
		gotIDs[hit.Item.ID] = true
	}
	if len(hits) != 2 || !gotIDs[a.ID] || !gotIDs[b.ID] {
		t.Fatalf("scope/Chinese search = %v, want %s and %s", knowledgeHitIDs(hits), a.ID, b.ID)
	}
	if len(hits[0].Relations) != 1 || hits[0].Relations[0].ToItemID != b.ID {
		t.Fatalf("published relation missing from hit: %+v", hits[0].Relations)
	}
	if len(hits[0].Relations[0].SourceIDs) != 1 || hits[0].Relations[0].SourceIDs[0] != relationSource.ID {
		t.Fatalf("relation evidence missing: %+v", hits[0].Relations[0])
	}
	if source, err := store.Knowledge().GetSource(ctx, ws.ID, beta.ID, relationSource.ID); err != nil || source.Ref != relationSource.Ref {
		t.Fatalf("relation evidence not re-readable: source=%+v err=%v", source, err)
	}
	if _, err := store.Knowledge().GetItem(ctx, ws.ID, beta.ID, a.ID); err != nil {
		t.Fatalf("workspace item should be visible: %v", err)
	}
	if _, err := store.Knowledge().GetItem(ctx, ws.ID, beta.ID, c.ID); err != nil {
		t.Fatalf("second workspace item should be visible: %v", err)
	}
	if got, err := store.Knowledge().ListRelations(ctx, ws.ID, beta.ID, b.ID, domain.KnowledgeRelationIn, 10); err != nil || len(got) != 1 || got[0].FromItemID != a.ID {
		t.Fatalf("reverse relation lookup = %+v, err=%v", got, err)
	}
	wrongScope, err := store.Knowledge().Search(ctx, &domain.KnowledgeQuery{WorkspaceID: ws.ID, RequesterAgentID: beta.ID, Terms: []string{"结算"}, Scope: domain.KnowledgeScope{"project": "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(wrongScope) != 0 {
		t.Fatalf("project scope leaked billing item: %+v", wrongScope)
	}
}

func TestKnowledgeVisibleItemsPageFiltersBeforeKeysetLimit(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	items := []*domain.KnowledgeItem{
		knowledgeItem("kb_001", ws.ID, beta.ID, domain.KnowledgeVisibilityWorkspace, "one", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_002", ws.ID, beta.ID, domain.KnowledgeVisibilityWorkspace, "two", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_003", ws.ID, beta.ID, domain.KnowledgeVisibilityWorkspace, "three", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_004", ws.ID, beta.ID, domain.KnowledgeVisibilityWorkspace, "other", domain.KnowledgeScope{"project": "billing"}),
		knowledgeItem("kb_999", ws.ID, alpha.ID, domain.KnowledgeVisibilityPrivate, "private", domain.KnowledgeScope{"project": "orders"}),
	}
	for _, item := range items {
		item.Status = domain.KnowledgeStatusEffective
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	options := domain.KnowledgeListOptions{Scope: domain.KnowledgeScope{"project": "orders"}, Limit: 2}
	first, cursor, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "kb_003" || first[1].ID != "kb_002" || cursor != "kb_002" {
		t.Fatalf("first filtered page = ids(%v), cursor=%q", knowledgeItemIDs(first), cursor)
	}
	options.AfterID = cursor
	second, next, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].ID != "kb_001" || next != "" {
		t.Fatalf("second filtered page = ids(%v), cursor=%q", knowledgeItemIDs(second), next)
	}
	private, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, domain.KnowledgeListOptions{Visibility: domain.KnowledgeVisibilityPrivate, Scope: domain.KnowledgeScope{"project": "orders"}, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(private) != 0 {
		t.Fatalf("private item leaked in filtered page: %v", knowledgeItemIDs(private))
	}
}

func knowledgeItemIDs(items []*domain.KnowledgeItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func knowledgeHitIDs(hits []*domain.KnowledgeHit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		if hit != nil {
			ids = append(ids, hit.Item.ID)
		}
	}
	return ids
}

func TestKnowledgePrivateScopeAndVersionCAS(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	item := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityPrivate, "私有排查", domain.KnowledgeScope{"project": "secret"})
	if err := store.Knowledge().CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	v1 := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), item.ID, alpha.ID,
		"私有排查", "secret alias", 0)
	v1.Aliases = []string{"private-alias"}
	if err := store.Knowledge().CreateVersion(ctx, v1); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, v1.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Knowledge().GetItem(ctx, ws.ID, beta.ID, item.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private item leaked to another agent: %v", err)
	}
	if _, err := store.Knowledge().GetItem(ctx, ws.ID, "", item.ID); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("empty requester did not fail closed: %v", err)
	}
	hits, err := store.Knowledge().Search(ctx, &domain.KnowledgeQuery{WorkspaceID: ws.ID, RequesterAgentID: alpha.ID, Terms: []string{"private-alias"}})
	if err != nil || len(hits) != 1 {
		t.Fatalf("owner alias search = %+v, err=%v", hits, err)
	}
	newVersion := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), item.ID, alpha.ID,
		"私有排查修订", "secret revision", 1)
	if err := store.Knowledge().CreateVersion(ctx, newVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, newVersion.ID, 0, time.Time{}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale publish = %v, want version conflict", err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, newVersion.ID, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, newVersion.ID, 1, time.Time{}); err != nil {
		t.Fatalf("idempotent publish replay = %v", err)
	}
	if err := store.Knowledge().CreateVersion(ctx, &domain.KnowledgeVersion{ID: domain.NewID(domain.PrefixKnowledgeVersion), ItemID: item.ID, Version: 3, BaseVersion: 2, Status: domain.KnowledgeStatusCandidate, Kind: "feature", Title: "x", BodyMarkdown: "x", CreatedByAgentID: beta.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Knowledge().GetVersion(ctx, ws.ID, beta.ID, v1.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("private version leaked: %v", err)
	}
}

func TestKnowledgeRelationsDoNotRevealPrivateEndpoint(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	public := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "公开功能", nil)
	private := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityPrivate, "私有实现", nil)
	for _, item := range []*domain.KnowledgeItem{public, private} {
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	publicVersion := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), public.ID, alpha.ID, "公开功能", "public", 0)
	privateVersion := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), private.ID, alpha.ID, "私有实现", "private", 0)
	if err := store.Knowledge().CreateVersionBundle(ctx, publicVersion, nil, []*domain.KnowledgeRelation{{
		ID: domain.NewID(domain.PrefixKnowledgeRelation), WorkspaceID: ws.ID, SourceVersionID: publicVersion.ID,
		FromItemID: public.ID, ToItemID: private.ID, Kind: domain.KnowledgeRelationDependsOn,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().CreateVersion(ctx, privateVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, public.ID, publicVersion.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, private.ID, privateVersion.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if relations, err := store.Knowledge().ListRelations(ctx, ws.ID, beta.ID, public.ID, domain.KnowledgeRelationBoth, 10); err != nil || len(relations) != 0 {
		t.Fatalf("private endpoint relation leaked to beta: %+v err=%v", relations, err)
	}
	if relations, err := store.Knowledge().ListRelations(ctx, ws.ID, alpha.ID, public.ID, domain.KnowledgeRelationBoth, 10); err != nil || len(relations) != 1 || relations[0].ToItemID != private.ID {
		t.Fatalf("owner could not inspect private relation: %+v err=%v", relations, err)
	}
}

func TestKnowledgeSubmissionIdempotencyAndImmutableVersion(t *testing.T) {
	db, store, ws, alpha, _ := knowledgeTestDB(t)
	ctx := context.Background()
	submission := &domain.KnowledgeSubmission{ID: domain.NewID(domain.PrefixKnowledgeSubmission),
		WorkspaceID: ws.ID, AgentID: alpha.ID, ClientKey: "run:one",
		Request: domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: alpha.ID, ClientKey: "run:one",
			Changes: []domain.KnowledgeChange{{Title: "观察", Body: "A 与 B 有事件关系", Kind: "observation"}}}}
	created, first, err := store.Knowledge().SubmitCandidate(ctx, submission)
	if err != nil || !created || first.ID != submission.ID {
		t.Fatalf("first submission created=%v result=%+v err=%v", created, first, err)
	}
	replay := *submission
	replay.ID = domain.NewID(domain.PrefixKnowledgeSubmission)
	created, got, err := store.Knowledge().SubmitCandidate(ctx, &replay)
	if err != nil || created || got.ID != submission.ID {
		t.Fatalf("exact submission replay created=%v result=%+v err=%v", created, got, err)
	}
	conflict := replay
	conflict.Request.Changes[0].Body = "different"
	if _, _, err := store.Knowledge().SubmitCandidate(ctx, &conflict); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different payload replay = %v, want idempotency conflict", err)
	}
	item := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "不可变", nil)
	if err := store.Knowledge().CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	version := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), item.ID, alpha.ID, "不可变", "原始内容", 0)
	if err := store.Knowledge().CreateVersion(ctx, version); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE knowledge_versions SET body_markdown=? WHERE id=?`, "改写", version.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("direct Markdown rewrite err=%v, want immutable trigger", err)
	}
	state, err := store.Knowledge().GetIndexState(ctx, ws.ID)
	if err != nil || state.Revision != 0 {
		t.Fatalf("unpublished index state = %+v, err=%v", state, err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, version.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	state, err = store.Knowledge().GetIndexState(ctx, ws.ID)
	if err != nil || state.Revision != 1 || state.ItemCount != 1 {
		t.Fatalf("published index state = %+v, err=%v", state, err)
	}
}

func TestKnowledgeRepealKeepsHistoryAndWithdrawsDefaultSearch(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	item := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "可废止规则", domain.KnowledgeScope{"project": "orders"})
	if err := store.Knowledge().CreateItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	version := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), item.ID, alpha.ID,
		"可废止规则", "保留的历史 Markdown", 0)
	if err := store.Knowledge().CreateVersion(ctx, version); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, item.ID, version.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().RepealItem(ctx, item.ID, 1, "规则已被新流程替代", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().RepealItem(ctx, item.ID, 1, "规则已被新流程替代", time.Time{}); err != nil {
		t.Fatalf("exact repeal replay = %v", err)
	}
	if err := store.Knowledge().RepealItem(ctx, item.ID, 1, "different reason", time.Time{}); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different repeal replay = %v, want idempotency conflict", err)
	}
	if hits, err := store.Knowledge().Search(ctx, &domain.KnowledgeQuery{WorkspaceID: ws.ID, RequesterAgentID: beta.ID, Terms: []string{"可废止"}}); err != nil || len(hits) != 0 {
		t.Fatalf("repealed item remains in default search: hits=%+v err=%v", hits, err)
	}
	if _, err := store.Knowledge().GetVersion(ctx, ws.ID, beta.ID, version.ID); err != nil {
		t.Fatalf("historical version not readable after repeal: %v", err)
	}
	if versions, err := store.Knowledge().ListVersions(ctx, ws.ID, beta.ID, item.ID, domain.KnowledgeStatusRepealed); err != nil || len(versions) != 1 || versions[0].BodyMarkdown != "保留的历史 Markdown" {
		t.Fatalf("repealed history = %+v err=%v", versions, err)
	}
	if item, err := store.Knowledge().GetItem(ctx, ws.ID, beta.ID, item.ID); err != nil || item.Status != domain.KnowledgeStatusRepealed || item.RepealReason == "" {
		t.Fatalf("repeal metadata = %+v err=%v", item, err)
	}
	state, err := store.Knowledge().GetIndexState(ctx, ws.ID)
	if err != nil || state.ItemCount != 0 || state.Revision != 2 {
		t.Fatalf("repeal index state = %+v err=%v", state, err)
	}
}
