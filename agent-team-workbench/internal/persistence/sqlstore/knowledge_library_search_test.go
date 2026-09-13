// knowledge_library_search_test.go 读端契约（防回归）：SearchRelease 的命中项
// 必须报出 release 固定的 document version（ID + 版本号），而不是零值。
// 历史缺口：hit.Version 恒为空 → 上层预取拼出并不存在的「v0」，引用元数据
// （version_id）永不输出；naive 修法（取文档行当期版本）在第 2 个 release
// 上就会答错，因此这里同时钉住「按 release 而不是按文档当期」这一语义。
package sqlstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

const (
	searchFixtureWorkspace = "ws_library_search"
	searchFixtureLibrary   = "klib_library_search"
	searchFixtureDocument  = "kdoc_library_search"
)

// publishSearchFixture 发布一个只含单条断言的新 release：第 n 次调用把同一
// 文档写成第 n 版（走生产写入路径 PublishProjection + CommitPublication）。
func publishSearchFixture(t *testing.T, ctx context.Context, store *sqlstore.Store, statement string) *domain.KnowledgeRelease {
	t.Helper()
	now := time.Now().UTC()
	taskID := domain.NewID("ktask_")
	if err := store.Library().CreateTask(ctx, &domain.KnowledgeWriteTask{
		ID: taskID, LibraryID: searchFixtureLibrary, Kind: "incremental",
		Status: domain.KnowledgeTaskCompleted, ViewID: "baseline",
		FocusJSON: "{}", PlanJSON: "{}", CoverageJSON: "{}",
		MaxAttempts: 3, MaxRepairAttempts: 2, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := &domain.KnowledgeSnapshot{
		ID: domain.NewID("ksnap_"), LibraryID: searchFixtureLibrary, ViewID: "baseline", CapturedAt: now,
	}
	if err := store.Library().CreateSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	release, err := store.Library().PublishProjection(ctx, application.PublishInput{
		LibraryID: searchFixtureLibrary, TaskID: taskID, SnapshotID: snapshot.ID,
		ProjectionDigest: "digest-" + taskID, CoverageJSON: "{}",
		Documents: []application.PublishDocument{{
			DocumentID: searchFixtureDocument, Path: "prd/login.md", Kind: "prd", Title: "登录需求",
			ContentMarkdown: "# 登录需求\n", ContentDigest: "digest-" + statement,
			Assertions: []domain.KnowledgeAssertion{{
				ID: "assertion:search-login", Heading: "登录", Statement: statement,
				Perspective: "normative", Basis: "source_statement", ScopeJSON: "{}", EvidenceJSON: "[]", Ordinal: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Library().CommitPublication(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	return release
}

func TestKnowledgeSearchReleaseHitCarriesPinnedVersion(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "library-search.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	now := time.Now().UTC()
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: searchFixtureWorkspace, Name: "library-search", Timezone: "UTC",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Library().CreateLibrary(ctx, &domain.KnowledgeLibrary{
		ID: searchFixtureLibrary, WorkspaceID: searchFixtureWorkspace, RootPath: t.TempDir(),
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	first := publishSearchFixture(t, ctx, store, "用户必须能用账号密码登录。")
	second := publishSearchFixture(t, ctx, store, "用户必须能用手机号验证码登录。")

	for _, tc := range []struct {
		name    string
		release *domain.KnowledgeRelease
	}{
		{name: "首个 release", release: first},
		{name: "增量 release（同一文档的下一版）", release: second},
	} {
		hits, _, _, err := store.Library().SearchRelease(ctx, searchFixtureLibrary, tc.release.ID, []string{"登录"}, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 0 {
			t.Fatalf("%s：检索应命中已发布断言", tc.name)
		}
		// 权威来源是 release 自己固定的版本行，不是文档行的当期指针。
		pinned, err := store.Library().ReleaseDocumentVersion(ctx, tc.release.ID, searchFixtureDocument)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range hits {
			if hit.Version.ID != pinned.ID {
				t.Fatalf("%s：命中项 version.id = %q，应为 release 固定的 %q", tc.name, hit.Version.ID, pinned.ID)
			}
			if hit.Version.Version < 1 || hit.Version.Version != pinned.Version {
				t.Fatalf("%s：命中项 version.version = %d，应为 release 固定的 %d", tc.name, hit.Version.Version, pinned.Version)
			}
			if hit.Version.DocumentID != searchFixtureDocument || hit.Version.LibraryID != searchFixtureLibrary {
				t.Fatalf("%s：命中项版本缺少已知的 document/library 归属: %+v", tc.name, hit.Version)
			}
		}
	}

	// 两个 release 固定的是同一文档的不同版本：读端必须跟着 release 走。
	hitOf := func(release *domain.KnowledgeRelease) domain.KnowledgeDocumentVersion {
		hits, _, _, err := store.Library().SearchRelease(ctx, searchFixtureLibrary, release.ID, []string{"登录"}, 10)
		if err != nil || len(hits) == 0 {
			t.Fatalf("检索 release %s 失败: %v %d", release.ID, err, len(hits))
		}
		return hits[0].Version
	}
	if firstHit, secondHit := hitOf(first), hitOf(second); firstHit.ID == secondHit.ID || secondHit.Version <= firstHit.Version {
		t.Fatalf("release 固定的版本必须各自独立: first=%+v second=%+v", firstHit, secondHit)
	}
}
