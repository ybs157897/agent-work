package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// seedCtx 为测试 workspace 幂等准备最小执行上下文（host_local + 默认 mount 广告
// + 默认 Location）。Run 创建在新架构下必须冻结 context snapshot（RFC §4.6），
// 未配置 Location 的 Run 创建一律 422 workspace_location_required。
func seedCtx(t *testing.T, store application.Store, ctx context.Context, workspaceID string) {
	t.Helper()
	if _, err := application.SeedWorkspaceLocation(ctx, store, workspaceID); err != nil {
		t.Fatal(err)
	}
}

// seedRunSnapshot 为直建 Run 的 fixture 补上 v1 context snapshot：0021 之后
// 所有 Run 必有唯一不可变快照（RFC §4.6），绕过 Service 直插 execution_runs
// 的场景（如 self-heal 源 run 构造）必须随行补快照，否则恢复链 fail closed。
func seedRunSnapshot(t *testing.T, store application.Store, ctx context.Context, run *domain.ExecutionRun) domain.ExecutionContextSnapshot {
	t.Helper()
	loc, err := store.WorkspaceLocations().DefaultFor(ctx, run.WorkspaceID)
	if err != nil {
		t.Fatalf("seedRunSnapshot: workspace %s 无默认 Location（先 seedCtx）: %v", run.WorkspaceID, err)
	}
	snap := &domain.ExecutionContextSnapshot{
		ID: domain.NewID(domain.PrefixCtxSnapshot), RunID: run.ID,
		SchemaVersion: domain.SnapshotSchemaV1, WorkspaceID: run.WorkspaceID,
		WorkspaceLocationID: loc.ID, LocationVersion: loc.Version,
		MountGeneration: loc.MountGeneration, ExecutionHostID: loc.ExecutionHostID,
		MountAlias: loc.MountAlias, RepositoryIdentity: loc.RepositoryIdentity,
		RefKind: domain.RefRoot, ContextGeneration: 1,
		Source: domain.SnapshotSourceCurrent, CreatedAt: time.Now().UTC(),
	}
	snap.SnapshotDigest = snap.ComputeDigest()
	if err := store.ContextSnapshots().Create(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := store.Runs().SetContextSnapshot(ctx, run.ID, snap.ID); err != nil {
		t.Fatal(err)
	}
	return *snap
}
