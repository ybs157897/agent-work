package sqlstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestTaskSessionClaimAnchorIsAtomicAndOwnerGuarded(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "anchors.db")+
		"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(12)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	now := time.Now().UTC()
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{
		ID: "wi_anchor", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "anchor", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	const claims = 12
	for i := 0; i < claims; i++ {
		runID := fmt.Sprintf("run_anchor_%02d", i)
		if err := store.Runs().Create(ctx, &domain.ExecutionRun{
			ID: runID, WorkspaceID: "ws_wk", WorkItemID: "wi_anchor", Status: domain.RunQueued,
			Input: map[string]any{}, Version: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ContextSnapshots().Create(ctx, &domain.ExecutionContextSnapshot{
		ID: "ctx_anchor", RunID: "run_anchor_00", WorkspaceID: "ws_wk", SchemaVersion: domain.SnapshotSchemaLegacy,
		RefKind: domain.RefRoot, Source: domain.SnapshotSourceLegacy, SnapshotDigest: "legacy-v0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		runID string
		seq   int64
		err   error
	}
	results := make(chan result, claims)
	var wg sync.WaitGroup
	for i := 0; i < claims; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			runID := fmt.Sprintf("run_anchor_%02d", n)
			claimed, claimErr := store.TaskSessions().ClaimAnchor(ctx, &domain.TaskSession{
				ID: "ts_" + runID, WorkspaceID: "ws_wk", AgentProfileID: "",
				AdapterID: "mock", TaskKey: "wi_anchor", ContextSnapshotID: "ctx_anchor",
				ContextGeneration: 1, LastRunID: runID, AnchorRunSequence: 1,
				SessionParams: map[string]any{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			})
			if claimErr != nil {
				results <- result{err: claimErr}
				return
			}
			results <- result{runID: runID, seq: claimed.AnchorRunSequence}
		}(i)
	}
	wg.Wait()
	close(results)

	seen := make(map[int64]string, claims)
	var highestRun string
	for res := range results {
		if res.err != nil {
			t.Fatalf("并发 claim 失败: %v", res.err)
		}
		if previous, duplicate := seen[res.seq]; duplicate {
			t.Fatalf("anchor sequence %d 重复分配给 %s 和 %s", res.seq, previous, res.runID)
		}
		seen[res.seq] = res.runID
		if res.seq == claims {
			highestRun = res.runID
		}
	}
	for seq := int64(1); seq <= claims; seq++ {
		if _, ok := seen[seq]; !ok {
			t.Fatalf("缺少连续 anchor sequence %d: %v", seq, seen)
		}
	}
	anchor, err := store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_anchor")
	if err != nil {
		t.Fatal(err)
	}
	if anchor.AnchorRunSequence != claims || anchor.LastRunID != highestRun {
		t.Fatalf("最终 anchor 应归属最高 sequence: %+v want owner=%s", anchor, highestRun)
	}
	// Generic Upsert is intentionally incapable of stealing ownership back.
	if err := store.TaskSessions().Upsert(ctx, &domain.TaskSession{
		ID: "ts_stale_upsert", WorkspaceID: "ws_wk", AdapterID: "mock", TaskKey: "wi_anchor",
		LastRunID: seen[1], AnchorRunSequence: 1, ContextGeneration: 0,
		SessionParams: map[string]any{"__ref": "mock://manual-reset"}, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	anchor, err = store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_anchor")
	if err != nil {
		t.Fatal(err)
	}
	if anchor.LastRunID != highestRun || anchor.AnchorRunSequence != claims {
		t.Fatalf("generic Upsert 不得夺回 owner: %+v", anchor)
	}
	if inserted, err := store.TaskSessions().InsertIfAbsent(ctx, &domain.TaskSession{
		ID: "ts_stale_tombstone", WorkspaceID: "ws_wk", AdapterID: "mock", TaskKey: "wi_anchor",
		SessionParams: map[string]any{"__cleared_reason": "stale"}, UpdatedAt: time.Now().UTC(),
	}); err != nil || inserted {
		t.Fatalf("stale tombstone 必须 INSERT DO NOTHING: inserted=%v err=%v", inserted, err)
	}
	anchor, err = store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_anchor")
	if err != nil {
		t.Fatal(err)
	}
	if anchor.LastRunID != highestRun || anchor.AnchorRunSequence != claims || anchor.SessionRef() != "mock://manual-reset" {
		t.Fatalf("InsertIfAbsent 不得污染现有 owner material: %+v", anchor)
	}

	// A late callback that belonged to an earlier claim must not overwrite the
	// latest owner or its session material.
	if updated, err := store.TaskSessions().UpdateIfAnchorOwner(ctx, &domain.TaskSession{
		WorkspaceID: "ws_wk", AgentProfileID: "", AdapterID: "mock", TaskKey: "wi_anchor",
		SessionParams: map[string]any{"__ref": "mock://late"}, UpdatedAt: time.Now().UTC(),
	}, seen[1], 1); err != nil || updated {
		t.Fatalf("旧 owner callback 应被 CAS 拒绝: updated=%v err=%v", updated, err)
	}
	after, err := store.TaskSessions().Get(ctx, "ws_wk", "", "mock", "wi_anchor")
	if err != nil {
		t.Fatal(err)
	}
	if after.LastRunID != highestRun || after.AnchorRunSequence != claims {
		t.Fatalf("迟到 callback 覆盖了最新 anchor: %+v", after)
	}
}
