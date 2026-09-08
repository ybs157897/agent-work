package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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

func TestKnowledgeVisibleItemsPageOwnerFilterPreservesRequesterVisibilityAndCursor(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	items := []*domain.KnowledgeItem{
		knowledgeItem("kb_owner_alpha_003", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "alpha public 3", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_owner_alpha_002", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "alpha public 2", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_owner_alpha_001", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "alpha public 1", domain.KnowledgeScope{"project": "billing"}),
		knowledgeItem("kb_owner_alpha_000", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "alpha public 0", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_owner_alpha_private", ws.ID, alpha.ID, domain.KnowledgeVisibilityPrivate, "alpha private", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_owner_beta_public", ws.ID, beta.ID, domain.KnowledgeVisibilityWorkspace, "beta public", domain.KnowledgeScope{"project": "orders"}),
		knowledgeItem("kb_owner_beta_private", ws.ID, beta.ID, domain.KnowledgeVisibilityPrivate, "beta private", domain.KnowledgeScope{"project": "orders"}),
	}
	for _, item := range items {
		item.Status = domain.KnowledgeStatusEffective
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	options := domain.KnowledgeListOptions{OwnerAgentID: alpha.ID, Scope: domain.KnowledgeScope{"project": "orders"}, Limit: 2}
	first, cursor, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := knowledgeItemIDs(first); !slices.Equal(got, []string{"kb_owner_alpha_003", "kb_owner_alpha_002"}) || cursor != "kb_owner_alpha_002" {
		t.Fatalf("owner-filtered first page = ids(%v), cursor=%q", got, cursor)
	}
	options.AfterID = cursor
	second, next, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := knowledgeItemIDs(second); !slices.Equal(got, []string{"kb_owner_alpha_000"}) || next != "" {
		t.Fatalf("owner-filtered scope continuation leaked non-matching/private rows = ids(%v), cursor=%q", got, next)
	}

	options = domain.KnowledgeListOptions{OwnerAgentID: alpha.ID, Limit: 2}
	first, cursor, err = store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := knowledgeItemIDs(first); !slices.Equal(got, []string{"kb_owner_alpha_003", "kb_owner_alpha_002"}) || cursor != "kb_owner_alpha_002" {
		t.Fatalf("owner-filtered public page = ids(%v), cursor=%q", got, cursor)
	}
	options.AfterID = cursor
	second, next, err = store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
	if err != nil {
		t.Fatal(err)
	}
	if got := knowledgeItemIDs(second); !slices.Equal(got, []string{"kb_owner_alpha_001", "kb_owner_alpha_000"}) || next != "" {
		t.Fatalf("owner-filtered public continuation = ids(%v), cursor=%q", got, next)
	}

	ownerView, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, alpha.ID,
		domain.KnowledgeListOptions{OwnerAgentID: alpha.ID, Visibility: domain.KnowledgeVisibilityPrivate, Limit: 10})
	if err != nil || len(ownerView) != 1 || ownerView[0].ID != "kb_owner_alpha_private" {
		t.Fatalf("requester agent private permission changed by owner filter = ids(%v), err=%v", knowledgeItemIDs(ownerView), err)
	}
	otherOwnerView, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, alpha.ID,
		domain.KnowledgeListOptions{OwnerAgentID: beta.ID, Visibility: domain.KnowledgeVisibilityPrivate, Limit: 10})
	if err != nil || len(otherOwnerView) != 0 {
		t.Fatalf("owner filter bypassed requester private permission = ids(%v), err=%v", knowledgeItemIDs(otherOwnerView), err)
	}

	withoutOwner, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := knowledgeItemIDs(withoutOwner); !slices.Equal(got, []string{"kb_owner_beta_public", "kb_owner_beta_private", "kb_owner_alpha_003", "kb_owner_alpha_002", "kb_owner_alpha_001", "kb_owner_alpha_000"}) {
		t.Fatalf("omitting owner filter changed baseline visibility = ids(%v)", got)
	}
	otherWorkspace := &domain.Workspace{ID: "ws_owner_other", Name: "owner-other", Timezone: "UTC", Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.Workspaces().Create(ctx, otherWorkspace); err != nil {
		t.Fatal(err)
	}
	otherAgent := &domain.AgentProfile{ID: "agent_owner_other", WorkspaceID: otherWorkspace.ID, Name: "other", Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.Agents().Create(ctx, otherAgent); err != nil {
		t.Fatal(err)
	}
	crossWorkspace, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{OwnerAgentID: otherAgent.ID, Limit: 10})
	if err != nil || len(crossWorkspace) != 0 {
		t.Fatalf("cross-workspace owner should be a safe empty set = ids(%v), err=%v", knowledgeItemIDs(crossWorkspace), err)
	}
	unknown, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{OwnerAgentID: "agent_missing_owner", Limit: 10})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("unknown owner should be a safe empty set = ids(%v), err=%v", knowledgeItemIDs(unknown), err)
	}
	if _, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{OwnerAgentID: "invalid-owner", Limit: 10}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("invalid owner id = %v, want validation error", err)
	}
}

func TestKnowledgeSearchItemsPageMatchesBodyAndPaginatesFullProjection(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	const total = 205
	for i := 1; i <= total; i++ {
		itemID := fmt.Sprintf("kb_search_%03d", i)
		versionID := fmt.Sprintf("kbv_search_%03d", i)
		title := fmt.Sprintf("条目 %03d", i)
		item := knowledgeItem(itemID, ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, title,
			domain.KnowledgeScope{"project": "orders"})
		item.Status = domain.KnowledgeStatusEffective
		if i == 1 {
			item.Kind = "note"
		}
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		version := knowledgeVersion(versionID, itemID, alpha.ID, title,
			fmt.Sprintf("正文命中词；这是第 %03d 条正文。", i), 0)
		version.Kind = item.Kind
		if err := store.Knowledge().CreateVersion(ctx, version); err != nil {
			t.Fatal(err)
		}
		if err := store.Knowledge().PublishVersion(ctx, itemID, versionID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	options := domain.KnowledgeListOptions{Query: "正文命中词", Limit: 37}
	seen := make(map[string]struct{}, total)
	pages := 0
	for {
		items, cursor, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
		if err != nil {
			t.Fatalf("search page %d: %v", pages+1, err)
		}
		pages++
		if len(items) > options.Limit {
			t.Fatalf("page %d exceeded limit: %d", pages, len(items))
		}
		for _, item := range items {
			if _, ok := seen[item.ID]; ok {
				t.Fatalf("duplicate item across pages: %s", item.ID)
			}
			seen[item.ID] = struct{}{}
			if !strings.Contains(item.SearchExcerpt, "正文命中词") {
				t.Fatalf("body hit has no excerpt: %+v", item)
			}
		}
		if cursor == "" {
			break
		}
		options.AfterID = cursor
	}
	if len(seen) != total || pages < 6 {
		t.Fatalf("full search pagination returned %d/%d items in %d pages", len(seen), total, pages)
	}

	filtered, cursor, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{Query: "正文命中词", Kind: "note", Limit: 10})
	if err != nil || len(filtered) != 1 || filtered[0].ID != "kb_search_001" || cursor != "" {
		t.Fatalf("kind filter on body search = ids(%v), cursor=%q, err=%v", knowledgeItemIDs(filtered), cursor, err)
	}

	private := knowledgeItem("kb_search_private", ws.ID, alpha.ID, domain.KnowledgeVisibilityPrivate,
		"私有命中", domain.KnowledgeScope{"project": "orders"})
	private.Status = domain.KnowledgeStatusEffective
	if err := store.Knowledge().CreateItem(ctx, private); err != nil {
		t.Fatal(err)
	}
	privateVersion := knowledgeVersion("kbv_search_private", private.ID, alpha.ID, private.Title,
		"正文命中词 私有内容", 0)
	if err := store.Knowledge().CreateVersion(ctx, privateVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, private.ID, privateVersion.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if items, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{Query: "正文命中词", Visibility: domain.KnowledgeVisibilityPrivate, Limit: 10}); err != nil || len(items) != 0 {
		t.Fatalf("private search leaked to another agent: items=%v err=%v", knowledgeItemIDs(items), err)
	}
	if items, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, alpha.ID,
		domain.KnowledgeListOptions{Query: "正文命中词", Visibility: domain.KnowledgeVisibilityPrivate, Limit: 10}); err != nil || len(items) != 1 || items[0].ID != private.ID {
		t.Fatalf("owner private search = ids(%v), err=%v", knowledgeItemIDs(items), err)
	}

	draft := knowledgeItem("kb_search_draft", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace,
		"草稿命中", nil)
	draft.Status = domain.KnowledgeStatusDraft
	if err := store.Knowledge().CreateItem(ctx, draft); err != nil {
		t.Fatal(err)
	}
	if items, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{Query: "草稿命中", Status: domain.KnowledgeStatusEffective, Limit: 10}); err != nil || len(items) != 0 {
		t.Fatalf("draft item leaked into effective search: items=%v err=%v", knowledgeItemIDs(items), err)
	}
	if items, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, alpha.ID,
		domain.KnowledgeListOptions{Query: "草稿命中", Status: domain.KnowledgeStatusDraft, Limit: 10}); err != nil || len(items) != 1 || items[0].ID != draft.ID {
		t.Fatalf("draft search = ids(%v), err=%v", knowledgeItemIDs(items), err)
	}

	if items, cursor, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID,
		domain.KnowledgeListOptions{Query: `@@@(*`, Limit: 10}); err != nil || len(items) != 0 || cursor != "" {
		t.Fatalf("malicious FTS query = items(%v), cursor=%q, err=%v", knowledgeItemIDs(items), cursor, err)
	}
}

func TestKnowledgeSearchItemsPageEnforcesScopeWorkspaceKindAndVisibility(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	otherWorkspace := &domain.Workspace{ID: "ws_search_other", Name: "other", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, otherWorkspace); err != nil {
		t.Fatal(err)
	}
	otherAgent := &domain.AgentProfile{ID: "agent_search_other", WorkspaceID: otherWorkspace.ID, Name: "other",
		Role: "worker", Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Agents().Create(ctx, otherAgent); err != nil {
		t.Fatal(err)
	}
	add := func(item *domain.KnowledgeItem, body string) {
		t.Helper()
		item.Status = domain.KnowledgeStatusEffective
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		version := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), item.ID, item.OwnerAgentID,
			item.Title, body, 0)
		version.Kind = item.Kind
		if err := store.Knowledge().CreateVersion(ctx, version); err != nil {
			t.Fatal(err)
		}
		if err := store.Knowledge().PublishVersion(ctx, item.ID, version.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	orders := knowledgeItem("kb_search_orders", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace,
		"订单租约", domain.KnowledgeScope{"project": "orders"})
	orders.Kind = "procedure"
	billing := knowledgeItem("kb_search_billing", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace,
		"账单租约", domain.KnowledgeScope{"project": "billing"})
	billing.Kind = "decision"
	private := knowledgeItem("kb_search_private_scope", ws.ID, alpha.ID, domain.KnowledgeVisibilityPrivate,
		"私有租约", domain.KnowledgeScope{"project": "orders"})
	other := knowledgeItem("kb_search_other_scope", otherWorkspace.ID, otherAgent.ID,
		domain.KnowledgeVisibilityWorkspace, "外部租约", domain.KnowledgeScope{"project": "orders"})
	add(orders, "正文租约隔离词；订单空间正文")
	add(billing, "正文租约隔离词；账单空间正文")
	add(private, "正文租约隔离词；私有正文")
	add(other, "正文租约隔离词；另一个 workspace 正文")

	items, _, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, domain.KnowledgeListOptions{
		Query: "租约隔离词", Scope: domain.KnowledgeScope{"project": "orders"}, Limit: 20,
	})
	if err != nil || len(items) != 1 || items[0].ID != orders.ID {
		t.Fatalf("scope/workspace/private filters leaked: ids(%v), err=%v", knowledgeItemIDs(items), err)
	}
	items, _, err = store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, domain.KnowledgeListOptions{
		Query: "租约隔离词", Kind: "decision", Limit: 20,
	})
	if err != nil || len(items) != 1 || items[0].ID != billing.ID {
		t.Fatalf("kind filter on search = ids(%v), err=%v", knowledgeItemIDs(items), err)
	}
	items, _, err = store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, domain.KnowledgeListOptions{
		Query: "租约隔离词", Visibility: domain.KnowledgeVisibilityPrivate, Limit: 20,
	})
	if err != nil || len(items) != 0 {
		t.Fatalf("private visibility leaked to shared requester: ids(%v), err=%v", knowledgeItemIDs(items), err)
	}
	items, _, err = store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, alpha.ID, domain.KnowledgeListOptions{
		Query: "租约隔离词", Visibility: domain.KnowledgeVisibilityPrivate, Limit: 20,
	})
	if err != nil || len(items) != 1 || items[0].ID != private.ID {
		t.Fatalf("owner private visibility search = ids(%v), err=%v", knowledgeItemIDs(items), err)
	}
}

func TestKnowledgeSearchItemsPageOwnerFilterAppliesToFTSAndChineseFallback(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	add := func(item *domain.KnowledgeItem, body string) {
		t.Helper()
		item.Status = domain.KnowledgeStatusEffective
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
		version := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), item.ID, item.OwnerAgentID, item.Title, body, 0)
		version.Kind = item.Kind
		if err := store.Knowledge().CreateVersion(ctx, version); err != nil {
			t.Fatal(err)
		}
		if err := store.Knowledge().PublishVersion(ctx, item.ID, version.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []*domain.KnowledgeItem{
		knowledgeItem("kb_owner_search_alpha_003", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "alpha search 3", nil),
		knowledgeItem("kb_owner_search_alpha_002", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "alpha search 2", nil),
		knowledgeItem("kb_owner_search_alpha_001", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "alpha search 1", nil),
		knowledgeItem("kb_owner_search_alpha_private", ws.ID, alpha.ID, domain.KnowledgeVisibilityPrivate, "alpha private search", nil),
		knowledgeItem("kb_owner_search_beta_public", ws.ID, beta.ID, domain.KnowledgeVisibilityWorkspace, "beta search", nil),
	} {
		add(item, "ownerfiltertoken 归属筛选词正文")
	}

	collect := func(query, ownerID string) []string {
		t.Helper()
		options := domain.KnowledgeListOptions{Query: query, OwnerAgentID: ownerID, Limit: 2}
		var got []string
		for page := 0; page < 10; page++ {
			items, cursor, err := store.Knowledge().ListVisibleItemsPage(ctx, ws.ID, beta.ID, options)
			if err != nil {
				t.Fatalf("query %q page %d: %v", query, page+1, err)
			}
			for _, item := range items {
				got = append(got, item.ID)
				if query == "归属筛选词" && !strings.Contains(item.SearchExcerpt, query) {
					t.Fatalf("fallback result has no excerpt: %+v", item)
				}
			}
			if cursor == "" {
				return got
			}
			options.AfterID = cursor
		}
		t.Fatalf("query %q did not terminate", query)
		return nil
	}

	wantOwner := []string{"kb_owner_search_alpha_003", "kb_owner_search_alpha_002", "kb_owner_search_alpha_001"}
	for _, query := range []string{"ownerfiltertoken", "归属筛选词"} {
		if got := collect(query, alpha.ID); !slices.Equal(got, wantOwner) {
			t.Fatalf("owner-filtered %s search = %v, want %v", query, got, wantOwner)
		}
	}
	if got := collect("ownerfiltertoken", ""); !slices.Equal(got, []string{"kb_owner_search_beta_public", "kb_owner_search_alpha_003", "kb_owner_search_alpha_002", "kb_owner_search_alpha_001"}) {
		t.Fatalf("search without owner filter changed baseline visibility = %v", got)
	}
}

func TestKnowledgeSearchUsesORTermsWithoutLeakingPrivateItems(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	visibleB := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "库存释放", nil)
	visibleC := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "退款处理", nil)
	private := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityPrivate, "退款私有实现", nil)
	for _, item := range []*domain.KnowledgeItem{visibleB, visibleC, private} {
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	versions := []*domain.KnowledgeVersion{
		knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), visibleB.ID, alpha.ID, "库存释放", "库存释放由取消事件触发。", 0),
		knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), visibleC.ID, alpha.ID, "退款处理", "退款处理会生成退款记录。", 0),
		knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), private.ID, alpha.ID, "退款私有实现", "退款私有实现细节。", 0),
	}
	for _, version := range versions {
		if err := store.Knowledge().CreateVersion(ctx, version); err != nil {
			t.Fatal(err)
		}
		if err := store.Knowledge().PublishVersion(ctx, version.ItemID, version.ID, 0, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := store.Knowledge().Search(ctx, &domain.KnowledgeQuery{
		WorkspaceID: ws.ID, RequesterAgentID: beta.ID,
		Terms:  []string{"库存释放", "退款", "不存在的主体"},
		Budget: domain.KnowledgeBudget{MaxResults: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(hits))
	for _, hit := range hits {
		got[hit.Item.ID] = true
	}
	if len(hits) != 2 || !got[visibleB.ID] || !got[visibleC.ID] || got[private.ID] {
		t.Fatalf("multi-subject OR search = %v, want visible B/C only", knowledgeHitIDs(hits))
	}

	history := knowledgeItem(domain.NewID(domain.PrefixKnowledgeItem), ws.ID, alpha.ID,
		domain.KnowledgeVisibilityWorkspace, "历史规则", nil)
	if err := store.Knowledge().CreateItem(ctx, history); err != nil {
		t.Fatal(err)
	}
	old := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), history.ID, alpha.ID,
		"旧退款规则", "legacy refund behavior", 0)
	if err := store.Knowledge().CreateVersion(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, history.ID, old.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	current := knowledgeVersion(domain.NewID(domain.PrefixKnowledgeVersion), history.ID, alpha.ID,
		"当前订单规则", "current order behavior", 1)
	if err := store.Knowledge().CreateVersion(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, history.ID, current.ID, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	historyHits, err := store.Knowledge().Search(ctx, &domain.KnowledgeQuery{
		WorkspaceID: ws.ID, RequesterAgentID: beta.ID, Status: domain.KnowledgeStatusSuperseded,
		Terms: []string{"不存在的历史词", "legacy refund behavior"}, Budget: domain.KnowledgeBudget{MaxResults: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(historyHits) != 1 || historyHits[0].Item.ID != history.ID || historyHits[0].Version.ID != old.ID {
		t.Fatalf("historical OR search = %v, want superseded version %s", knowledgeHitIDs(historyHits), old.ID)
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

func TestKnowledgeSourcesAllowSameReferenceAcrossItemsAndVersions(t *testing.T) {
	_, store, ws, alpha, _ := knowledgeTestDB(t)
	ctx := context.Background()
	first := knowledgeItem("kb_source_a", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "A", nil)
	second := knowledgeItem("kb_source_b", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "B", nil)
	for _, item := range []*domain.KnowledgeItem{first, second} {
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	newSource := func(id string) *domain.KnowledgeSource {
		return &domain.KnowledgeSource{ID: id, WorkspaceID: ws.ID, SubmittedByAgentID: alpha.ID,
			Kind: domain.KnowledgeSourceDocument, Ref: "shared.md@rev1", Locator: "## 规则", Digest: "sha256:shared"}
	}
	v1 := knowledgeVersion("kbv_source_a1", first.ID, alpha.ID, "A", "A v1", 0)
	if err := store.Knowledge().CreateVersionBundle(ctx, v1, []*domain.KnowledgeSource{newSource("kbs_source_a1")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, first.ID, v1.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	v2 := knowledgeVersion("kbv_source_a2", first.ID, alpha.ID, "A", "A v2", 1)
	if err := store.Knowledge().CreateVersionBundle(ctx, v2, []*domain.KnowledgeSource{newSource("kbs_source_a2")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, first.ID, v2.ID, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	vB := knowledgeVersion("kbv_source_b1", second.ID, alpha.ID, "B", "B v1", 0)
	if err := store.Knowledge().CreateVersionBundle(ctx, vB, []*domain.KnowledgeSource{newSource("kbs_source_b1")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, second.ID, vB.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for _, versionID := range []string{v2.ID, vB.ID} {
		sources, err := store.Knowledge().ListVersionSources(ctx, ws.ID, alpha.ID, versionID)
		if err != nil || len(sources) != 1 || sources[0].Ref != "shared.md@rev1" {
			t.Fatalf("same reference version %s sources=%+v err=%v", versionID, sources, err)
		}
	}
}

func TestKnowledgeSearchWithRelationsOutsideTransactionCompletesBeforeDeadline(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	first := knowledgeItem("kb_deadline_a", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "A", nil)
	second := knowledgeItem("kb_deadline_b", ws.ID, alpha.ID, domain.KnowledgeVisibilityWorkspace, "B", nil)
	for _, item := range []*domain.KnowledgeItem{first, second} {
		if err := store.Knowledge().CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	v1 := knowledgeVersion("kbv_deadline_a1", first.ID, alpha.ID, "A", "A triggers B", 0)
	v2 := knowledgeVersion("kbv_deadline_b1", second.ID, alpha.ID, "B", "B runs after A", 0)
	if err := store.Knowledge().CreateVersionBundle(ctx, v1, nil, []*domain.KnowledgeRelation{{
		ID: "kbr_deadline", WorkspaceID: ws.ID, SourceVersionID: v1.ID,
		FromItemID: first.ID, ToItemID: second.ID, Kind: domain.KnowledgeRelationTriggers,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().CreateVersion(ctx, v2); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, first.ID, v1.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().PublishVersion(ctx, second.ID, v2.ID, 0, time.Time{}); err != nil {
		t.Fatal(err)
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	hits, err := store.Knowledge().Search(deadlineCtx, &domain.KnowledgeQuery{
		WorkspaceID: ws.ID, RequesterAgentID: beta.ID, Terms: []string{"A"}, Budget: domain.KnowledgeBudget{MaxResults: 10},
	})
	if err != nil {
		t.Fatalf("relation search outside transaction exceeded deadline or failed: %v", err)
	}
	if len(hits) == 0 || len(hits[0].Relations) != 1 || hits[0].Relations[0].ToItemID != second.ID {
		t.Fatalf("relation search result = %+v", hits)
	}
	rebuildCtx, rebuildCancel := context.WithTimeout(ctx, 2*time.Second)
	defer rebuildCancel()
	if err := store.Knowledge().RebuildIndex(rebuildCtx, ws.ID); err != nil {
		t.Fatalf("rebuild index outside nested query deadline: %v", err)
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

func TestKnowledgeHasSubmissionForRunRequiresExactWorkspaceAndAgent(t *testing.T) {
	_, store, ws, alpha, beta := knowledgeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	workItem := &domain.WorkItem{ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: ws.ID, RecordKind: domain.RecordKindTask,
		Title: "capture", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.WorkItems().Create(ctx, workItem); err != nil {
		t.Fatal(err)
	}
	run := &domain.ExecutionRun{ID: domain.NewID(domain.PrefixRun), WorkspaceID: ws.ID, WorkItemID: workItem.ID,
		AgentProfileID: alpha.ID, Status: domain.RunSucceeded, Version: 1, Input: map[string]any{}, CreatedAt: now, UpdatedAt: now}
	if err := store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	request := domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: alpha.ID, RunID: run.ID, WorkItemID: workItem.ID,
		ClientKey: "capture", NoChange: true}
	submission := &domain.KnowledgeSubmission{ID: domain.NewID(domain.PrefixKnowledgeSubmission), WorkspaceID: ws.ID,
		AgentID: alpha.ID, RunID: run.ID, WorkItemID: workItem.ID, ClientKey: request.ClientKey, Request: request}
	if _, _, err := store.Knowledge().SubmitCandidate(ctx, submission); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Knowledge().HasSubmissionForRun(ctx, ws.ID, alpha.ID, run.ID); err != nil || !ok {
		t.Fatalf("exact submission lookup = %v err=%v", ok, err)
	}
	if ok, err := store.Knowledge().HasSubmissionForRun(ctx, ws.ID, beta.ID, run.ID); err != nil || ok {
		t.Fatalf("cross-agent submission lookup = %v err=%v", ok, err)
	}
	if ok, err := store.Knowledge().HasSubmissionForRun(ctx, "ws_other", alpha.ID, run.ID); err != nil || ok {
		t.Fatalf("cross-workspace submission lookup = %v err=%v", ok, err)
	}
}
