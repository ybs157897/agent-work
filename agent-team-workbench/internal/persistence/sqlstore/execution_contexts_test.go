package sqlstore_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// ctxTestDB 建库 + workspace 种子 + host_local（执行上下文测试公共前置）。
func ctxTestDB(t *testing.T) (*sql.DB, *sqlstore.Store) {
	t.Helper()
	db := openWakeupTestDB(t)
	seedWorkspace(t, db)
	return db, sqlstore.New(db, sqlstore.SQLiteDialect())
}

func mustEnsureLocalHost(t *testing.T, store *sqlstore.Store, now time.Time) *domain.ExecutionHost {
	t.Helper()
	h, err := store.ExecutionHosts().EnsureLocalHost(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// seedAgentID 预置 execution_runs.agent_profile_id 外键指向的 agent 行。
func seedAgentID(t *testing.T, store *sqlstore.Store, id string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.Agents().Create(context.Background(), &domain.AgentProfile{
		ID: id, WorkspaceID: "ws_wk", Name: id, Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// v1Snapshot 构造通过 Validate 的 v1 快照（digest 按身份字段现算），mutate 供
// 用例改写身份字段后重算 digest。
func v1Snapshot(t *testing.T, runID string, now time.Time, mutate func(*domain.ExecutionContextSnapshot)) *domain.ExecutionContextSnapshot {
	t.Helper()
	s := &domain.ExecutionContextSnapshot{
		ID:                  domain.NewID(domain.PrefixCtxSnapshot),
		RunID:               runID,
		SchemaVersion:       domain.SnapshotSchemaV1,
		WorkspaceID:         "ws_wk",
		WorkspaceLocationID: "wsloc_t1",
		LocationVersion:     3,
		MountGeneration:     "gen-7",
		ExecutionHostID:     domain.LocalHostID,
		MountAlias:          "default",
		RepositoryIdentity:  "repo/test",
		RefKind:             domain.RefBranch,
		BranchName:          "feature-a",
		CheckoutRef:         "feature-a",
		BaseRevision:        "abc123",
		ContextGeneration:   2,
		Source:              domain.SnapshotSourceCurrent,
		CreatedAt:           now,
	}
	if mutate != nil {
		mutate(s)
	}
	s.SnapshotDigest = s.ComputeDigest()
	return s
}

func TestEnsureLocalHostIdempotent(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Microsecond)

	h1 := mustEnsureLocalHost(t, store, now)
	if h1.ID != domain.LocalHostID || h1.Name != "local" || h1.Kind != domain.HostKindLocal ||
		h1.Status != domain.HostStatusReady || h1.EnrollmentRef != "" || h1.Version != 1 {
		t.Fatalf("本机 Host 形状不符: %+v", h1)
	}
	// 幂等：重复调用不覆盖既有行（version/时间戳不被重置）。
	h2 := mustEnsureLocalHost(t, store, now.Add(time.Hour))
	if h2.Version != h1.Version || !h2.CreatedAt.Equal(h1.CreatedAt) {
		t.Fatalf("EnsureLocalHost 应幂等不覆盖: %+v vs %+v", h1, h2)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM execution_hosts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应只有一行 host，实际 %d", n)
	}
}

func TestExecutionHostCRUDAndCAS(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustEnsureLocalHost(t, store, now)

	remote := &domain.ExecutionHost{
		ID: "host_remote_1", Name: "lab-1", Kind: domain.HostKindRemote,
		Status: domain.HostStatusOffline, EnrollmentRef: "enr_x", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.ExecutionHosts().Create(ctx, remote); err != nil {
		t.Fatal(err)
	}
	got, err := store.ExecutionHosts().Get(ctx, "host_remote_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.EnrollmentRef != "enr_x" || got.Kind != domain.HostKindRemote {
		t.Fatalf("Create 往返不符: %+v", got)
	}

	// CAS：错误版本拒绝。
	remote.Name = "lab-1-renamed"
	if err := store.ExecutionHosts().Update(ctx, remote, 99); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("过期版本应 ErrVersionConflict，实际 %v", err)
	}
	if err := store.ExecutionHosts().Update(ctx, remote, 1); err != nil {
		t.Fatal(err)
	}
	got, err = store.ExecutionHosts().Get(ctx, "host_remote_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "lab-1-renamed" || got.Version != 2 {
		t.Fatalf("Update 未生效: %+v", got)
	}

	// SetStatus 与 List。
	if err := store.ExecutionHosts().SetStatus(ctx, "host_remote_1", domain.HostStatusDegraded, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	hosts, err := store.ExecutionHosts().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("应列出 2 个 host，实际 %d", len(hosts))
	}
	if hosts[0].ID != domain.LocalHostID || hosts[1].Status != domain.HostStatusDegraded {
		t.Fatalf("List 结果不符: %+v", hosts)
	}
	if _, err := store.ExecutionHosts().Get(ctx, "host_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未知 host 应 ErrNotFound，实际 %v", err)
	}
}

func TestHostMountUpsertRoundtripAndOverwrite(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustEnsureLocalHost(t, store, now)

	m := &domain.HostMount{
		ExecutionHostID:    domain.LocalHostID,
		Alias:              "default",
		RepositoryIdentity: "repo/test",
		DisplayLabel:       "main repo",
		DefaultBranch:      "main",
		SupportedRefKinds:  []domain.RefKind{domain.RefRoot, domain.RefBranch, domain.RefWorktree},
		Checkouts: []domain.MountCheckout{
			{Ref: "feature-a", Kind: "branch", Branch: "feature-a", Head: "abc"},
			{Ref: "wt-1", Kind: "worktree"},
		},
		RegistryGeneration: "gen-1",
		Status:             domain.MountStatusReady,
		LastSeenAt:         now,
	}
	if err := store.ExecutionHosts().UpsertMount(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := store.ExecutionHosts().GetMount(ctx, domain.LocalHostID, "default")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepositoryIdentity != "repo/test" || got.DisplayLabel != "main repo" ||
		got.DefaultBranch != "main" || got.RegistryGeneration != "gen-1" ||
		got.Status != domain.MountStatusReady || len(got.SupportedRefKinds) != 3 ||
		len(got.Checkouts) != 2 || got.Checkouts[0].Branch != "feature-a" ||
		!got.LastSeenAt.Equal(now) {
		t.Fatalf("mount 往返不符: %+v", got)
	}

	// 覆盖换代：同 (host, alias) 全列覆盖，不产生第二行。
	m2 := &domain.HostMount{
		ExecutionHostID:    domain.LocalHostID,
		Alias:              "default",
		RepositoryIdentity: "repo/test",
		SupportedRefKinds:  []domain.RefKind{domain.RefRoot},
		Checkouts:          []domain.MountCheckout{{Ref: "root", Kind: "root"}},
		RegistryGeneration: "gen-2",
		Status:             domain.MountStatusUnavailable,
	}
	if err := store.ExecutionHosts().UpsertMount(ctx, m2); err != nil {
		t.Fatal(err)
	}
	got, err = store.ExecutionHosts().GetMount(ctx, domain.LocalHostID, "default")
	if err != nil {
		t.Fatal(err)
	}
	if got.RegistryGeneration != "gen-2" || got.Status != domain.MountStatusUnavailable ||
		len(got.Checkouts) != 1 || got.DefaultBranch != "" || !got.LastSeenAt.IsZero() {
		t.Fatalf("mount 换代应全列覆盖: %+v", got)
	}

	// ListMounts 按 alias 排序。
	if err := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
		ExecutionHostID: domain.LocalHostID, Alias: "aux", RepositoryIdentity: "repo/aux",
		RegistryGeneration: "gen-1", Status: domain.MountStatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	mounts, err := store.ExecutionHosts().ListMounts(ctx, domain.LocalHostID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 2 || mounts[0].Alias != "aux" || mounts[1].Alias != "default" {
		t.Fatalf("ListMounts 应按 alias 排序: %+v", mounts)
	}
	if _, err := store.ExecutionHosts().GetMount(ctx, domain.LocalHostID, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未知 mount 应 ErrNotFound，实际 %v", err)
	}

	// checkouts 列损坏必须 fail loud，不得静默吞成空切片。
	if _, err := db.Exec(`UPDATE execution_host_mounts SET checkouts='{bad json' WHERE alias='default'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExecutionHosts().GetMount(ctx, domain.LocalHostID, "default"); err == nil {
		t.Fatal("checkouts JSON 损坏应报错而非静默吞")
	}
}

func TestWorkspaceLocationCRUDAndDefault(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustEnsureLocalHost(t, store, now)

	loc := &domain.WorkspaceLocation{
		ID: "wsloc_t1", WorkspaceID: "ws_wk", ExecutionHostID: domain.LocalHostID,
		MountAlias: "default", MountGeneration: "gen-1", RepositoryIdentity: "repo/test",
		IsDefault: true, Status: domain.LocationReady, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkspaceLocations().Create(ctx, loc); err != nil {
		t.Fatal(err)
	}
	got, err := store.WorkspaceLocations().Get(ctx, "wsloc_t1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsDefault || got.MountGeneration != "gen-1" || got.Status != domain.LocationReady {
		t.Fatalf("location 往返不符: %+v", got)
	}

	// 同 workspace 第二个 default 撞部分唯一索引 → 冲突。
	dup := &domain.WorkspaceLocation{
		ID: "wsloc_t2", WorkspaceID: "ws_wk", ExecutionHostID: domain.LocalHostID,
		MountAlias: "aux", RepositoryIdentity: "repo/aux",
		IsDefault: true, Status: domain.LocationReady, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkspaceLocations().Create(ctx, dup); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("同 workspace 第二个 default 应冲突，实际 %v", err)
	}
	dup.IsDefault = false
	if err := store.WorkspaceLocations().Create(ctx, dup); err != nil {
		t.Fatal(err)
	}

	// Update CAS + is_default 忠实写入。
	dup.MountGeneration = "gen-9"
	if err := store.WorkspaceLocations().Update(ctx, dup, 42); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("过期版本应 ErrVersionConflict，实际 %v", err)
	}
	if err := store.WorkspaceLocations().Update(ctx, dup, 1); err != nil {
		t.Fatal(err)
	}
	got, err = store.WorkspaceLocations().Get(ctx, "wsloc_t2")
	if err != nil {
		t.Fatal(err)
	}
	if got.MountGeneration != "gen-9" || got.Version != 2 || got.IsDefault {
		t.Fatalf("Update 未生效: %+v", got)
	}

	// DefaultFor：命中 / 无默认 ErrNotFound。
	def, err := store.WorkspaceLocations().DefaultFor(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if def.ID != "wsloc_t1" {
		t.Fatalf("默认 location 应为 wsloc_t1，实际 %s", def.ID)
	}
	if _, err := store.WorkspaceLocations().DefaultFor(ctx, "ws_none"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("无默认应 ErrNotFound，实际 %v", err)
	}

	list, err := store.WorkspaceLocations().ListByWorkspace(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应列出 2 个 location，实际 %d", len(list))
	}
	if err := store.WorkspaceLocations().SetStatus(ctx, "wsloc_t2", domain.LocationUnavailable, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = store.WorkspaceLocations().Get(ctx, "wsloc_t2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.LocationUnavailable {
		t.Fatalf("SetStatus 未生效: %+v", got)
	}
}

func TestWorkItemContextUpsertGet(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustEnsureLocalHost(t, store, now)
	if err := store.WorkspaceLocations().Create(ctx, &domain.WorkspaceLocation{
		ID: "wsloc_t1", WorkspaceID: "ws_wk", ExecutionHostID: domain.LocalHostID,
		MountAlias: "default", RepositoryIdentity: "repo/test",
		Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	insertWorkItem(t, db, "wi_ctx")

	c := &domain.DevelopmentContext{
		WorkItemID: "wi_ctx", ContextOwnerID: "wi_ctx", WorkspaceLocationID: "wsloc_t1",
		RefKind: domain.RefBranch, BranchName: "feature-a", CheckoutRef: "feature-a",
		BaseRevision: "abc", Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItemContexts().Upsert(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := store.WorkItemContexts().Get(ctx, "wi_ctx")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefKind != domain.RefBranch || got.BranchName != "feature-a" ||
		got.CheckoutRef != "feature-a" || got.BaseRevision != "abc" || got.Version != 1 {
		t.Fatalf("context 往返不符: %+v", got)
	}

	// 覆盖：branch → worktree（整体替换；created_at 保持首次值）。
	c2 := &domain.DevelopmentContext{
		WorkItemID: "wi_ctx", ContextOwnerID: "wi_ctx", WorkspaceLocationID: "wsloc_t1",
		RefKind: domain.RefWorktree, WorktreeRef: "wt-1", Version: 2,
		CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}
	if err := store.WorkItemContexts().Upsert(ctx, c2); err != nil {
		t.Fatal(err)
	}
	got, err = store.WorkItemContexts().Get(ctx, "wi_ctx")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefKind != domain.RefWorktree || got.WorktreeRef != "wt-1" ||
		got.BranchName != "" || got.CheckoutRef != "" || got.Version != 2 ||
		!got.CreatedAt.Equal(now) {
		t.Fatalf("context 覆盖不符: %+v", got)
	}

	// root：ref 字段全空（CHECK 要求 branch/worktree 为 NULL）。
	c3 := &domain.DevelopmentContext{
		WorkItemID: "wi_ctx", ContextOwnerID: "wi_ctx", WorkspaceLocationID: "wsloc_t1",
		RefKind: domain.RefRoot, Version: 3, CreatedAt: now, UpdatedAt: now.Add(2 * time.Minute),
	}
	if err := store.WorkItemContexts().Upsert(ctx, c3); err != nil {
		t.Fatal(err)
	}
	if got, err = store.WorkItemContexts().Get(ctx, "wi_ctx"); err != nil {
		t.Fatal(err)
	}
	if got.RefKind != domain.RefRoot || got.Version != 3 {
		t.Fatalf("root context 覆盖不符: %+v", got)
	}

	if _, err := store.WorkItemContexts().Get(ctx, "wi_nocctx"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("无显式 context 应 ErrNotFound，实际 %v", err)
	}
}

func TestContextSnapshotCreateGetAndValidate(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustEnsureLocalHost(t, store, now)
	if err := store.WorkspaceLocations().Create(ctx, &domain.WorkspaceLocation{
		ID: "wsloc_t1", WorkspaceID: "ws_wk", ExecutionHostID: domain.LocalHostID,
		MountAlias: "default", RepositoryIdentity: "repo/test",
		Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	insertWorkItem(t, db, "wi_snap")
	seedAgentID(t, store, "agent_snap")
	insertRun(t, db, "run_snap", "agent_snap", "wi_snap", "running", now)

	s := v1Snapshot(t, "run_snap", now, nil)
	if err := store.ContextSnapshots().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := store.ContextSnapshots().Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != domain.SnapshotSchemaV1 || got.MountGeneration != "gen-7" ||
		got.ExecutionHostID != domain.LocalHostID || got.CheckoutRef != "feature-a" ||
		got.ContextGeneration != 2 || got.Source != domain.SnapshotSourceCurrent ||
		got.LocationVersion != 3 || !got.CreatedAt.Equal(now) {
		t.Fatalf("snapshot 往返不符: %+v", got)
	}
	if got.SnapshotDigest != got.ComputeDigest() {
		t.Fatalf("往返后 digest 应仍与身份字段一致: %s vs %s", got.SnapshotDigest, got.ComputeDigest())
	}
	byRun, err := store.ContextSnapshots().GetByRun(ctx, "run_snap")
	if err != nil {
		t.Fatal(err)
	}
	if byRun.ID != s.ID {
		t.Fatalf("GetByRun 应返回同一快照: %s vs %s", byRun.ID, s.ID)
	}

	// digest 与身份字段不一致：Validate 拦截。
	bad := v1Snapshot(t, "run_snap", now, nil)
	bad.MountGeneration = "gen-8" // 篡改身份字段但不重算 digest
	if err := store.ContextSnapshots().Create(ctx, bad); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("坏 digest 应 ErrValidation，实际 %v", err)
	}
	// 缺失身份字段同样拦截。
	empty := v1Snapshot(t, "run_snap", now, func(x *domain.ExecutionContextSnapshot) {
		x.WorkspaceLocationID = ""
	})
	if err := store.ContextSnapshots().Create(ctx, empty); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("身份不完整应 ErrValidation，实际 %v", err)
	}

	// legacy-v0 放宽（身份字段可空）可落库；归属真实存在的终态 run。
	insertRun(t, db, "run_legacy", "agent_snap", "wi_snap", "failed", now)
	legacy := &domain.ExecutionContextSnapshot{
		ID: domain.NewID(domain.PrefixCtxSnapshot), RunID: "run_legacy",
		SchemaVersion: domain.SnapshotSchemaLegacy, WorkspaceID: "ws_wk",
		RefKind: domain.RefRoot, Source: domain.SnapshotSourceLegacy,
		SnapshotDigest: "legacy-v0", CreatedAt: now,
	}
	if err := store.ContextSnapshots().Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	gotLegacy, err := store.ContextSnapshots().GetByRun(ctx, "run_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if gotLegacy.ExecutionHostID != "" || gotLegacy.MountAlias != "" {
		t.Fatalf("legacy 快照身份字段应为空: %+v", gotLegacy)
	}

	if _, err := store.ContextSnapshots().Get(ctx, "ctxsnap_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未知快照应 ErrNotFound，实际 %v", err)
	}

	// 不可变 trigger：UPDATE/DELETE 一律拒绝。
	if _, err := db.Exec(`UPDATE execution_context_snapshots SET snapshot_digest='x' WHERE id=?`, s.ID); err == nil {
		t.Fatal("快照 UPDATE 应被 trigger 拒绝")
	}
	if _, err := db.Exec(`DELETE FROM execution_context_snapshots WHERE id=?`, s.ID); err == nil {
		t.Fatal("快照 DELETE 应被 trigger 拒绝")
	}
}

func TestHasActiveRunOnCheckout(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustEnsureLocalHost(t, store, now)
	if err := store.WorkspaceLocations().Create(ctx, &domain.WorkspaceLocation{
		ID: "wsloc_t1", WorkspaceID: "ws_wk", ExecutionHostID: domain.LocalHostID,
		MountAlias: "default", RepositoryIdentity: "repo/test",
		Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	insertWorkItem(t, db, "wi_ck")
	seedAgentID(t, store, "agent_ck")
	insertRun(t, db, "run_busy", "agent_ck", "wi_ck", "running", now)
	insertRun(t, db, "run_done", "agent_ck", "wi_ck", "succeeded", now)
	insertRun(t, db, "run_wt", "agent_ck", "wi_ck", "running", now)
	insertRun(t, db, "run_legacy", "agent_ck", "wi_ck", "failed", now)

	mk := func(runID string, mutate func(*domain.ExecutionContextSnapshot)) *domain.ExecutionContextSnapshot {
		s := v1Snapshot(t, runID, now, mutate)
		if err := store.ContextSnapshots().Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	mk("run_busy", nil) // checkout_ref=feature-a
	mk("run_done", func(s *domain.ExecutionContextSnapshot) {
		s.CheckoutRef, s.BranchName = "feature-b", "feature-b"
	})
	mk("run_wt", func(s *domain.ExecutionContextSnapshot) {
		s.RefKind = domain.RefWorktree
		s.BranchName, s.CheckoutRef = "", ""
		s.WorktreeRef = "wt-1"
	})
	// legacy NULL-host 快照：不参与占用判定。
	legacy := &domain.ExecutionContextSnapshot{
		ID: domain.NewID(domain.PrefixCtxSnapshot), RunID: "run_legacy",
		SchemaVersion: domain.SnapshotSchemaLegacy, WorkspaceID: "ws_wk",
		RefKind: domain.RefRoot, Source: domain.SnapshotSourceLegacy,
		SnapshotDigest: "legacy-v0", CreatedAt: now,
	}
	if err := store.ContextSnapshots().Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	snaps := store.ContextSnapshots()
	// 正例：同 Host 同 checkout 有非终态 Run；worktree ref 同理。
	if busy, err := snaps.HasActiveRunOnCheckout(ctx, domain.LocalHostID, "feature-a"); err != nil || !busy {
		t.Fatalf("feature-a 应判忙: busy=%v err=%v", busy, err)
	}
	if busy, err := snaps.HasActiveRunOnCheckout(ctx, domain.LocalHostID, "wt-1"); err != nil || !busy {
		t.Fatalf("wt-1 应判忙: busy=%v err=%v", busy, err)
	}
	// 反例：终态 Run / 未见过的 ref / 其他 Host。
	if busy, err := snaps.HasActiveRunOnCheckout(ctx, domain.LocalHostID, "feature-b"); err != nil || busy {
		t.Fatalf("终态 run 的 feature-b 不应判忙: busy=%v err=%v", busy, err)
	}
	if busy, err := snaps.HasActiveRunOnCheckout(ctx, domain.LocalHostID, "feature-zzz"); err != nil || busy {
		t.Fatalf("未知 ref 不应判忙: busy=%v err=%v", busy, err)
	}
	if err := store.ExecutionHosts().Create(ctx, &domain.ExecutionHost{
		ID: "host_remote_2", Name: "r2", Kind: domain.HostKindRemote, Status: domain.HostStatusReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if busy, err := snaps.HasActiveRunOnCheckout(ctx, "host_remote_2", "feature-a"); err != nil || busy {
		t.Fatalf("其他 Host 不应判忙: busy=%v err=%v", busy, err)
	}
}

func TestRunnerEventDedupV2Conflict(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := store.Runners().RunnerEventDedupV2(ctx, "run_d1", "lease_d1", "runner_d", 1, "evt_1"); err != nil {
		t.Fatal(err)
	}
	// 同 (run, lease, runner, producer_seq) 重复 → 冲突（event_id 不同也拦）。
	if err := store.Runners().RunnerEventDedupV2(ctx, "run_d1", "lease_d1", "runner_d", 1, "evt_other"); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("重复 dedup 键应 ErrIdempotencyConflict，实际 %v", err)
	}
	// producer_seq / lease / run 不同均视为新事件。
	for _, c := range []struct {
		run, lease, runner string
		seq                int64
	}{
		{"run_d1", "lease_d1", "runner_d", 2},
		{"run_d1", "lease_d2", "runner_d", 1},
		{"run_d2", "lease_d1", "runner_d", 1},
	} {
		if err := store.Runners().RunnerEventDedupV2(ctx, c.run, c.lease, c.runner, c.seq, "evt_x"); err != nil {
			t.Fatalf("dedup(%s,%s,%d) 不应冲突: %v", c.lease, c.run, c.seq, err)
		}
	}
}

func TestExecutionContextColumnsRoundtrip(t *testing.T) {
	db, store := ctxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mustEnsureLocalHost(t, store, now)
	if err := store.WorkspaceLocations().Create(ctx, &domain.WorkspaceLocation{
		ID: "wsloc_t1", WorkspaceID: "ws_wk", ExecutionHostID: domain.LocalHostID,
		MountAlias: "default", RepositoryIdentity: "repo/test",
		Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	insertWorkItem(t, db, "wi_rt")
	seedAgentID(t, store, "agent_rt")
	insertRun(t, db, "run_rt_a", "agent_rt", "wi_rt", "succeeded", now)

	// 快照归属既有 run_rt_a；随后验证 run 行新列往返（FK 要求快照先在）。
	s := v1Snapshot(t, "run_rt_a", now, nil)
	if err := store.ContextSnapshots().Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	insertWorkItem(t, db, "wi_rt_b")
	runB := &domain.ExecutionRun{
		ID: "run_rt_b", WorkspaceID: "ws_wk", WorkItemID: "wi_rt_b",
		AgentProfileID: "agent_rt", Status: domain.RunRunning, Version: 1,
		ContextSnapshotID: s.ID,
		CreatedAt:         now, UpdatedAt: now,
	}
	if err := store.Runs().Create(ctx, runB); err != nil {
		t.Fatal(err)
	}
	gotRun, err := store.Runs().Get(ctx, "run_rt_b")
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.ContextSnapshotID != s.ID {
		t.Fatalf("run.context_snapshot_id 往返不符: %q", gotRun.ContextSnapshotID)
	}
	gotA, err := store.Runs().Get(ctx, "run_rt_a")
	if err != nil {
		t.Fatal(err)
	}
	if gotA.ContextSnapshotID != "" {
		t.Fatalf("NULL 快照引用应读为零值: %q", gotA.ContextSnapshotID)
	}

	// task_session 新列：ClaimAnchor 是唯一 owner 写点；通用 Upsert / StartGeneration
	// 只能更新 session material，绝不覆盖已 claim 的 owner/sequence。
	anchor := &domain.TaskSession{
		ID: "ts_rt1", WorkspaceID: "ws_wk", AgentProfileID: "agent_rt",
		AdapterID: "codex-appserver", TaskKey: "wi_rt",
		SessionParams:     map[string]any{"__ref": "codex://s1"},
		ContextSnapshotID: s.ID, ContextGeneration: 2,
		LastRunID: "run_rt_b", AnchorRunSequence: 1,
		RunsCount: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.TaskSessions().ClaimAnchor(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	gotTS, err := store.TaskSessions().Get(ctx, "ws_wk", "agent_rt", "codex-appserver", "wi_rt")
	if err != nil {
		t.Fatal(err)
	}
	if gotTS.ContextSnapshotID != s.ID || gotTS.ContextGeneration != 2 ||
		gotTS.LastRunID != "run_rt_b" || gotTS.AnchorRunSequence != 1 {
		t.Fatalf("task_session context 列往返不符: %+v", gotTS)
	}
	// 新 Run 通过 ClaimAnchor 原子转移归属；随后的通用 Upsert 只累加计数/更新 ref。
	anchor2 := &domain.TaskSession{
		ID: "ts_rt2", WorkspaceID: "ws_wk", AgentProfileID: "agent_rt",
		AdapterID: "codex-appserver", TaskKey: "wi_rt",
		SessionParams:     map[string]any{"__ref": "codex://s2"},
		ContextSnapshotID: s.ID, ContextGeneration: 2,
		LastRunID: "run_rt_a", AnchorRunSequence: 1,
		RunsCount: 1, CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}
	if _, err := store.TaskSessions().ClaimAnchor(ctx, anchor2); err != nil {
		t.Fatal(err)
	}
	if err := store.TaskSessions().Upsert(ctx, anchor2); err != nil {
		t.Fatal(err)
	}
	gotTS, err = store.TaskSessions().Get(ctx, "ws_wk", "agent_rt", "codex-appserver", "wi_rt")
	if err != nil {
		t.Fatal(err)
	}
	if gotTS.LastRunID != "run_rt_a" || gotTS.AnchorRunSequence != 2 || gotTS.RunsCount != 2 {
		t.Fatalf("anchor claim 覆盖/计数累加不符: %+v", gotTS)
	}
	// StartGeneration：先 ClaimAnchor 换 owner/context，再重起 session material/计数。
	anchor3 := &domain.TaskSession{
		ID: "ts_rt3", WorkspaceID: "ws_wk", AgentProfileID: "agent_rt",
		AdapterID: "codex-appserver", TaskKey: "wi_rt",
		SessionParams:     map[string]any{"__ref": "codex://s3"},
		ContextSnapshotID: s.ID, ContextGeneration: 3,
		LastRunID: "run_rt_b", AnchorRunSequence: 1,
		RunsCount: 1, CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute),
	}
	if _, err := store.TaskSessions().ClaimAnchor(ctx, anchor3); err != nil {
		t.Fatal(err)
	}
	if err := store.TaskSessions().StartGeneration(ctx, anchor3); err != nil {
		t.Fatal(err)
	}
	gotTS, err = store.TaskSessions().Get(ctx, "ws_wk", "agent_rt", "codex-appserver", "wi_rt")
	if err != nil {
		t.Fatal(err)
	}
	if gotTS.ContextGeneration != 3 || gotTS.AnchorRunSequence != 3 || gotTS.RunsCount != 1 {
		t.Fatalf("StartGeneration context 列不符: %+v", gotTS)
	}
	sessions, err := store.TaskSessions().ListByAgent(ctx, "ws_wk", "agent_rt")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ContextSnapshotID != s.ID {
		t.Fatalf("ListByAgent 扫描不符: %+v", sessions)
	}

	// plan 新列往返。
	if err := store.Plans().Create(ctx, &domain.Plan{
		ID: "plan_rt", WorkspaceID: "ws_wk", WorkItemID: "wi_rt",
		AgentProfileID: "agent_rt", ContextSnapshotID: s.ID, ContextGeneration: 4,
		Status: domain.PlanActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	gotPlan, err := store.Plans().Get(ctx, "plan_rt")
	if err != nil {
		t.Fatal(err)
	}
	if gotPlan.ContextSnapshotID != s.ID || gotPlan.ContextGeneration != 4 {
		t.Fatalf("plan context 列往返不符: %+v", gotPlan)
	}

	// work_item acceptance/phase 新列往返（Create/Update/Get/List）。
	entered := now.Add(time.Hour)
	wi := &domain.WorkItem{
		ID: "wi_acc", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "验收读模型", Status: domain.WorkItemInProgress, Phase: domain.PhaseReview,
		Priority: domain.PriorityMedium, AcceptanceCriteria: []string{"测试全绿", "文档齐"},
		PhaseEnteredAt: &entered, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, wi); err != nil {
		t.Fatal(err)
	}
	gotWI, err := store.WorkItems().Get(ctx, "wi_acc")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotWI.AcceptanceCriteria) != 2 || gotWI.AcceptanceCriteria[1] != "文档齐" ||
		gotWI.PhaseEnteredAt == nil || !gotWI.PhaseEnteredAt.Equal(entered) {
		t.Fatalf("work_item 新列往返不符: %+v", gotWI)
	}
	revised := now.Add(2 * time.Hour)
	wi.AcceptanceCriteria = []string{"口径唯一"}
	wi.PhaseEnteredAt = &revised
	if err := store.WorkItems().Update(ctx, wi, 1); err != nil {
		t.Fatal(err)
	}
	gotWI, err = store.WorkItems().Get(ctx, "wi_acc")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotWI.AcceptanceCriteria) != 1 || gotWI.AcceptanceCriteria[0] != "口径唯一" ||
		!gotWI.PhaseEnteredAt.Equal(revised) {
		t.Fatalf("work_item Update 新列不符: %+v", gotWI)
	}
	items, _, err := store.WorkItems().List(ctx, "ws_wk", application.WorkItemFilter{RecordKind: domain.RecordKindTask})
	if err != nil {
		t.Fatal(err)
	}
	var listed *domain.WorkItem
	for _, it := range items {
		if it.ID == "wi_acc" {
			listed = it
		}
	}
	if listed == nil || len(listed.AcceptanceCriteria) != 1 {
		t.Fatalf("List 扫描新列不符: %+v", listed)
	}
	// 未设置 = NULL → nil。
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{
		ID: "wi_plain", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "无验收", Status: domain.WorkItemTodo, Priority: domain.PriorityLow,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	gotPlain, err := store.WorkItems().Get(ctx, "wi_plain")
	if err != nil {
		t.Fatal(err)
	}
	if gotPlain.AcceptanceCriteria != nil || gotPlain.PhaseEnteredAt != nil {
		t.Fatalf("未设置的新列应读为 nil: %+v", gotPlain)
	}
}
