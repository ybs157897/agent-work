package sqlstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// seedSearchEntry 直插一条索引条目（可指定 workspace 对应的 work item）。
func seedSearchEntry(t *testing.T, store *sqlstore.Store, workItemID, kind, sourceID, title, body string) {
	t.Helper()
	if err := store.Search().IndexEntry(context.Background(), &application.SearchEntry{
		WorkItemID: workItemID, Kind: kind, SourceID: sourceID, Title: title, Body: body,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSearchIndexAndQuery 防回归（S4）：检索命中、定点重写幂等（旧快照不残留）、
// workspace 隔离、kind/work_item_id 过滤、空/纯符号/特殊字符 query 不报错。
func TestSearchIndexAndQuery(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	seedWorkspace(t, db)
	insertWorkItem(t, db, "wi_s1")
	insertWorkItem(t, db, "wi_s2")
	// 第二个 workspace + 挂它的 work item（workspace 隔离断言用）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at) VALUES ('ws_other','other','UTC',1,?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO work_items(id, workspace_id, title, status, priority, version, created_at, updated_at)
		 VALUES ('wi_other','ws_other','t','todo','medium',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}

	seedSearchEntry(t, store, "wi_s1", "decision", "dec_1",
		"用 PostgreSQL", "团队决定用 PostgreSQL 作为唯一存储，不引入第二套数据库。")
	seedSearchEntry(t, store, "wi_s1", "segment_summary", "wi_s1",
		"台账任务", "已完成 2 轮。结论：storage 选型 PostgreSQL。")

	// 命中：decision 关键词。
	hits, err := store.Search().Search(ctx, "ws_wk", "PostgreSQL", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("PostgreSQL 应命中 2 条，实际 %d: %#v", len(hits), hits)
	}
	for _, h := range hits {
		if h.WorkItemID != "wi_s1" || h.SourceID == "" || h.Snippet == "" {
			t.Fatalf("命中项形状异常: %+v", h)
		}
	}

	// 定点重写：同 (kind, source_id) 覆盖写 → 新内容命中、旧快照不残留。
	seedSearchEntry(t, store, "wi_s1", "segment_summary", "wi_s1",
		"台账任务", "已完成 3 轮。结论改判：storage 换成 SQLite。")
	if hits, err = store.Search().Search(ctx, "ws_wk", "SQLite", "", "", 20); err != nil || len(hits) != 1 {
		t.Fatalf("重写后应命中新内容 1 条: %v %#v", err, hits)
	}
	if hits, err = store.Search().Search(ctx, "ws_wk", "PostgreSQL", "", "", 20); err != nil || len(hits) != 1 {
		t.Fatalf("重写后旧内容只应剩 decision 1 条: %v %#v", err, hits)
	}

	// workspace 隔离：别 workspace 的条目搜不到。
	seedSearchEntry(t, store, "wi_other", "decision", "dec_other",
		"other decision", "other workspace PostgreSQL note")
	if hits, err = store.Search().Search(ctx, "ws_wk", "PostgreSQL", "", "", 20); err != nil || len(hits) != 1 {
		t.Fatalf("workspace 隔离失效: %v %#v", err, hits)
	}
	if hits, err = store.Search().Search(ctx, "ws_other", "PostgreSQL", "", "", 20); err != nil || len(hits) != 1 {
		t.Fatalf("ws_other 应搜到自己的条目: %v %#v", err, hits)
	}

	// kind / work_item_id 过滤。
	if hits, err = store.Search().Search(ctx, "ws_wk", "PostgreSQL", "wi_s1", "decision", 20); err != nil || len(hits) != 1 || hits[0].Kind != "decision" {
		t.Fatalf("kind+work_item 过滤异常: %v %#v", err, hits)
	}
	if hits, err = store.Search().Search(ctx, "ws_wk", "PostgreSQL", "", "artifact", 20); err != nil || len(hits) != 0 {
		t.Fatalf("artifact kind 应无命中: %v %#v", err, hits)
	}

	// 空 query / 纯符号 query / FTS 语法字符 → 空结果或安全降级，不报错。
	for _, q := range []string{"", "   ", "@@@", `*-:^(`, `scope:"admin" AND (*`} {
		hits, err := store.Search().Search(ctx, "ws_wk", q, "", "", 20)
		if err != nil {
			t.Fatalf("query %q 不应报错: %v", q, err)
		}
		if len(hits) != 0 {
			t.Fatalf("query %q 应无命中: %#v", q, hits)
		}
	}
}
