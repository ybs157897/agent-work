package application_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// searchHits 检索快捷入口（失败即 Fatal）。
func searchHits(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID, query, kind string) []*application.SearchResult {
	t.Helper()
	hits, err := store.Search().Search(ctx, wsID, query, "", kind, 20)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

// TestSearchIndexPipelines 防回归（S4 三类索引写入链路）：decision 写入后可
// 搜到；digest 重刷后命中新内容且旧快照不残留（定点重写）；artifact.created
// 投影的产物标题可搜。
func TestSearchIndexPipelines(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wi := seedRunEnv(t, ctx, svc, store)

	// decision：写入即索引（title=quote 前 80 字，body=quote 全文）。
	entry, err := svc.RecordDecision(ctx, wi.ID, application.RecordDecisionParams{
		Quote: "Storage engine: use PostgreSQL 16, no second database.",
	})
	if err != nil {
		t.Fatal(err)
	}
	hits := searchHits(t, ctx, store, "ws_"+t.Name(), "PostgreSQL", "decision")
	if len(hits) != 1 || hits[0].SourceID != entry.ID || hits[0].Kind != "decision" {
		t.Fatalf("decision 应可搜到: %#v", hits)
	}

	// segment_summary：run 终态触发 digest 索引；重刷后命中新内容、旧快照不残留
	//（把滚动窗口压到 2 条，ALPHA-1 轮次滚出窗口即应从索引消失——定点重写）。
	oldWindow := application.RollingDigestMaxMessages
	application.RollingDigestMaxMessages = 2
	defer func() { application.RollingDigestMaxMessages = oldWindow }()
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "Remember token ALPHA-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	settleDriveRun(t, ctx, svc, first.ID, "Done with ALPHA-1", true)
	hits = searchHits(t, ctx, store, "ws_"+t.Name(), "ALPHA-1", "segment_summary")
	if len(hits) != 1 || hits[0].SourceID != wi.ID {
		t.Fatalf("digest 首刷后应命中 ALPHA-1: %#v", hits)
	}

	second, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "Rotate token to BETA-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	settleDriveRun(t, ctx, svc, second.ID, "Rotated to BETA-2", true)
	if hits = searchHits(t, ctx, store, "ws_"+t.Name(), "BETA-2", "segment_summary"); len(hits) != 1 || hits[0].SourceID != wi.ID {
		t.Fatalf("digest 重刷后应命中 BETA-2: %#v", hits)
	}
	if hits = searchHits(t, ctx, store, "ws_"+t.Name(), "ALPHA-1", "segment_summary"); len(hits) != 0 {
		t.Fatalf("digest 重刷后旧快照不得残留: %#v", hits)
	}

	// artifact：artifact.created 投影的产物标题可搜（body 空）。
	art, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "produce artifact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunEvent(ctx, art.ID, domain.EventArtifactCreated, map[string]any{
		"logical_path": "docs/rollout-plan.md", "sha256": "abc123", "size": 3.0, "mime": "text/markdown",
	}); err != nil {
		t.Fatal(err)
	}
	hits = searchHits(t, ctx, store, "ws_"+t.Name(), "rollout-plan", "artifact")
	if len(hits) != 1 || hits[0].Kind != "artifact" || !strings.Contains(hits[0].Title, "rollout-plan.md") {
		t.Fatalf("artifact 标题应可搜到: %#v", hits)
	}

	// artifact.manifest：真实 Runner 走 RecordArtifact 时也必须复用同一
	// artifact 搜索投影，不能只有 mock 的 artifact.created 可搜。
	runnerRun, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: "produce runner artifact",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := &domain.Artifact{
		LogicalPath: "artifacts/runner-output.md", Mime: "text/markdown",
		Size: 7, Sha256: "runner-sha256", Classification: "internal",
	}
	if err := svc.RecordArtifact(ctx, runnerRun.ID, manifest); err != nil {
		t.Fatal(err)
	}
	hits = searchHits(t, ctx, store, "ws_"+t.Name(), "runner-output", "artifact")
	if len(hits) != 1 || hits[0].Kind != "artifact" || hits[0].SourceID != manifest.ID ||
		!strings.Contains(hits[0].Title, "runner-output.md") {
		t.Fatalf("真实 Runner 产物标题应可搜到: %#v", hits)
	}
}
