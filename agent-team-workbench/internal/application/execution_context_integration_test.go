// execution_context_integration_test.go 执行上下文行为回归（任务控制面 RFC
// §15.1/§15.3 验证矩阵的应用面切片）：
//   - retry/evaluation/recovery/inherited 快照身份一致（digest 相同、source 正确）；
//   - Coordinator Plan 冻结 source snapshot，根 context 变更不影响 Worker；
//   - context 变化强制 fresh（组合指纹漂移）；
//   - 旧 Run 迟到 session/clear 不得覆盖新代际 anchor（含墓碑）；
//   - 无 Location 的 Run 创建 422 不建行；
//   - 静态校验失败整事务回滚不留 queued Run（branch 不唯一 / checkout 占用）；
//   - Runner v2 事件原子入口：stale/duplicate ACK 不应用、毒帧落
//     failed(runner_event_invalid)、reject 幂等、accept digest 闸门。
package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// newContextTestSvc 搭建带默认 Location 的最小执行环境，返回 (svc, store, dispatcher, wsID, agentID)。
func newContextTestSvc(t *testing.T, wsID string) (*application.Service, *sqlstore.Store, *captureDispatcher, string, string) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: wsID, Name: wsID, Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, wsID)
	agent := &domain.AgentProfile{
		ID: "agent_" + wsID, WorkspaceID: wsID, Name: "Worker", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "mock"},
		Version:           1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.Agents().Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_" + wsID, WorkspaceID: wsID, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingReady,
		Capabilities: map[string]string{"resume": string(atwruntime.CapSupported)},
		Version:      1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return svc, store, dispatcher, wsID, agent.ID
}

func mustTask(t *testing.T, ctx context.Context, svc *application.Service, wsID, title string) *domain.WorkItem {
	t.Helper()
	wi, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: title, AgentProfileID: "agent_" + wsID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return wi
}

func snapshotOfRun(t *testing.T, ctx context.Context, store *sqlstore.Store, runID string) domain.ExecutionContextSnapshot {
	t.Helper()
	snap, err := store.ContextSnapshots().GetByRun(ctx, runID)
	if err != nil {
		t.Fatalf("run %s 缺 context snapshot: %v", runID, err)
	}
	return *snap
}

// TestSnapshotSourcePoliciesKeepIdentity retry/evaluation/recovery/inherited
// 克隆必须保持快照身份（digest 不变、source 正确、可溯源）——retry 偷换 cwd
// 是红线（RFC §4.7/§5.1.7）。
func TestSnapshotSourcePoliciesKeepIdentity(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_snapid")
	wi := mustTask(t, ctx, svc, wsID, "来源策略")

	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "首轮"})
	if err != nil {
		t.Fatal(err)
	}
	base := snapshotOfRun(t, ctx, store, first.ID)

	// retry：父快照克隆。
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, first.ID, "done"); err != nil {
		t.Fatal(err)
	}
	retry, err := svc.RetryRun(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	retrySnap := snapshotOfRun(t, ctx, store, retry.ID)
	if retrySnap.SnapshotDigest != base.SnapshotDigest || retrySnap.Source != domain.SnapshotSourceRetry ||
		retrySnap.SourceSnapshotID != base.ID {
		t.Fatalf("retry 快照身份不符: %+v（base %+v）", retrySnap, base)
	}

	// recovery（session_unknown 自愈）：AutoHealOf → 原 Run 快照克隆。
	heal, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "自愈重试", AutoHealOf: first.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	healSnap := snapshotOfRun(t, ctx, store, heal.ID)
	if healSnap.SnapshotDigest != base.SnapshotDigest || healSnap.Source != domain.SnapshotSourceRecovery {
		t.Fatalf("recovery 快照身份不符: %+v", healSnap)
	}

	// evaluation / inherited：显式来源参数走同一克隆语义（Plan Worker/评估的机制）。
	for _, tc := range []struct {
		source domain.SnapshotSource
	}{
		{domain.SnapshotSourceInherited}, {domain.SnapshotSourceEvaluation},
	} {
		r, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
			AgentProfileID: agentID, Instruction: "克隆来源", ContextSource: tc.source,
			ContextSourceSnapshotID: base.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		snap := snapshotOfRun(t, ctx, store, r.ID)
		if snap.SnapshotDigest != base.SnapshotDigest || snap.Source != tc.source {
			t.Fatalf("%s 快照身份不符: %+v", tc.source, snap)
		}
	}
}

// TestPlanFreezesSourceSnapshot Worker 身份不受根 context 变更影响：
// Coordinator Run 冻结 C1 → 根 context 切到 C2 → Plan 派生的 Worker 仍用 C1
// （inherited 克隆），绝不重读当前根 context（RFC §4.5/§4.7）。
func TestPlanFreezesSourceSnapshot(t *testing.T) {
	ctx := context.Background()
	svc, store, dispatcher, wsID, workerID := newContextTestSvc(t, "ws_planfreeze")
	seedCtx(t, store, ctx, wsID) // 幂等
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "冻结计划", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().GetConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	base := snapshotOfRun(t, ctx, store, coordinatorRun.ID)

	// Coordinator Run 终态后把根 context 切到第二 Location（新身份）。
	if _, err := svc.ControlRun(ctx, coordinatorRun.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	altMountErr := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
		ExecutionHostID: domain.LocalHostID, Alias: "alt",
		RepositoryIdentity: "repo_alt", RegistryGeneration: "gen_seed",
		Status: domain.MountStatusReady, LastSeenAt: time.Now().UTC(),
	})
	if altMountErr != nil {
		t.Fatal(altMountErr)
	}
	locAlt, err := svc.CreateWorkspaceLocation(ctx, wsID, application.CreateWorkspaceLocationParams{
		ExecutionHostID: domain.LocalHostID, MountAlias: "alt",
		RepositoryIdentity: "repo_alt", MountGeneration: "gen_seed", IsDefault: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetDevelopmentContext(ctx, root.ID, application.SetDevelopmentContextParams{
		WorkspaceLocationID: locAlt.ID, RefKind: domain.RefRoot,
		ExpectedVersion: fresh.Version,
	}); err != nil {
		t.Fatal(err)
	}

	// 强制 Coordinator 控制线回 Running（测试直接固化 checkpoint），提交 Plan：
	// dispatch 立即创建 Worker Run——其快照必须继承冻结的 C1，而非当前 C2。
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorRunning
	state.CurrentRunID = coordinatorRun.ID
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: config.AgentProfileID, SourceRunID: coordinatorRun.ID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "子任务", "执行"),
			deferStep(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ContextSnapshotID != base.ID || plan.ContextGeneration != base.ContextGeneration {
		t.Fatalf("Plan 未冻结 source snapshot: %+v（base %+v）", plan, base)
	}
	workerSnap := snapshotOfRun(t, ctx, store, plan.Steps[0].ResultRunID)
	if workerSnap.SnapshotDigest != base.SnapshotDigest ||
		workerSnap.Source != domain.SnapshotSourceInherited ||
		workerSnap.WorkspaceLocationID != base.WorkspaceLocationID {
		t.Fatalf("Worker 快照未继承 Plan 冻结身份: %+v（base %+v）", workerSnap, base)
	}
}

// TestContextChangeForcesFreshSession 根 context 变更（新 Location）后新 Run
// 必须 fresh：锚点组合指纹漂移，不注入 resume_session_ref（RFC §4.8）。
func TestContextChangeForcesFreshSession(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_ctxfresh")
	wi := mustTask(t, ctx, svc, wsID, "context fresh")

	run1, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run1.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, run1.ID, "mock://sess_a"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run1.ID, "done"); err != nil {
		t.Fatal(err)
	}
	run2, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	conv2 := run2.Input["conversation"].(map[string]any)
	if conv2["resume_session_ref"] != "mock://sess_a" {
		t.Fatalf("同 context 应续接会话: %#v", conv2)
	}
	if err := svc.RecordRunStatus(ctx, run2.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run2.ID, "done"); err != nil {
		t.Fatal(err)
	}

	// 切换 Location = context 身份变化：必须 fresh。
	if err := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
		ExecutionHostID: domain.LocalHostID, Alias: "alt",
		RepositoryIdentity: "repo_alt", RegistryGeneration: "gen_seed",
		Status: domain.MountStatusReady, LastSeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	locAlt, err := svc.CreateWorkspaceLocation(ctx, wsID, application.CreateWorkspaceLocationParams{
		ExecutionHostID: domain.LocalHostID, MountAlias: "alt",
		RepositoryIdentity: "repo_alt", MountGeneration: "gen_seed",
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.WorkItems().Get(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetDevelopmentContext(ctx, wi.ID, application.SetDevelopmentContextParams{
		WorkspaceLocationID: locAlt.ID, RefKind: domain.RefRoot, ExpectedVersion: fresh.Version,
	}); err != nil {
		t.Fatal(err)
	}
	run3, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "第三轮"})
	if err != nil {
		t.Fatal(err)
	}
	conv3 := run3.Input["conversation"].(map[string]any)
	if _, has := conv3["resume_session_ref"]; has {
		t.Fatalf("context 变化后必须 fresh，不得续接旧会话: %#v", conv3)
	}
}

// TestLateSessionUpdateCannotClobberNewAnchor 旧 Run 迟到的 session/clear
// （含墓碑）不得覆盖新 Run 的 anchor（RFC §4.8 写入门）。
func TestLateSessionUpdateCannotClobberNewAnchor(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_anchor")
	wi := mustTask(t, ctx, svc, wsID, "anchor 门")

	run1, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run1.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, run1.ID, "mock://s1"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run1.ID, "done"); err != nil {
		t.Fatal(err)
	}
	// run2 claim anchor（seq=2，owner=run2）。
	run2, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := store.TaskSessions().Get(ctx, wsID, agentID, "mock", wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.LastRunID != run2.ID || anchor.AnchorRunSequence != 2 {
		t.Fatalf("anchor 应被 run2 claim: %+v", anchor)
	}
	// run1 迟到 session：静默丢弃，anchor 不变。
	if err := svc.RecordRunSessionUpdate(ctx, run1.ID, atwruntime.SessionUpdate{Ref: "mock://late"}); err != nil {
		t.Fatal(err)
	}
	// run1 迟到 Clear（墓碑也不许清新代际）。
	if err := svc.RecordRunSessionUpdate(ctx, run1.ID, atwruntime.SessionUpdate{Clear: true, ClearReason: "late"}); err != nil {
		t.Fatal(err)
	}
	after, err := store.TaskSessions().Get(ctx, wsID, agentID, "mock", wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionRef() != "mock://s1" || after.LastRunID != run2.ID {
		t.Fatalf("迟到 session/clear 覆盖了新 anchor: %+v", after)
	}
	// A 0021-predecessor callback has no durable snapshot/generation. It must
	// fail closed too: otherwise its ref or clear tombstone could replace the
	// material of the newer v1 owner even while ownership columns stay intact.
	legacy := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: wsID, WorkItemID: wi.ID,
		AgentProfileID: agentID, AdapterID: "mock", Status: domain.RunRunning,
		Input: map[string]any{}, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.Runs().Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionUpdate(ctx, legacy.ID, atwruntime.SessionUpdate{Ref: "mock://legacy-late"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionUpdate(ctx, legacy.ID, atwruntime.SessionUpdate{Clear: true, ClearReason: "legacy-late"}); err != nil {
		t.Fatal(err)
	}
	afterLegacy, err := store.TaskSessions().Get(ctx, wsID, agentID, "mock", wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterLegacy.SessionRef() != "mock://s1" || afterLegacy.LastRunID != run2.ID || afterLegacy.AnchorRunSequence != 2 {
		t.Fatalf("无 snapshot 的 legacy callback 污染了新 owner material: %+v", afterLegacy)
	}
}

// TestChatRunWithoutLocationRejected 无 Location 的 Chat Run 创建必须 422
// workspace_location_required，且不落任何 Run 行（RFC §7.3）。
func TestChatRunWithoutLocationRejected(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	// 注意：不 seedCtx —— 该 Workspace 无任何 Location。
	now := time.Now().UTC()
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: "ws_noloc", Name: "noloc", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: "agent_noloc", WorkspaceID: "ws_noloc", Name: "W", Role: "dev",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	chat, err := svc.CreateWorkItem(ctx, "ws_noloc", application.CreateWorkItemParams{
		Title: "chat", RecordKind: domain.RecordKindChat, AgentProfileID: "agent_noloc",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateRun(ctx, chat.ID, application.CreateRunParams{
		AgentProfileID: "agent_noloc", Instruction: "你好",
	})
	if !errors.Is(err, domain.ErrWorkspaceLocationRequired) {
		t.Fatalf("应报 workspace_location_required，实际 %v", err)
	}
	runs, err := store.Runs().ListByWorkItem(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("失败创建不得留下 Run 行: %+v", runs)
	}
}

func TestSetDevelopmentContextRejectsForeignWorkspaceLocation(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_context_scope")
	wi := mustTask(t, ctx, svc, wsID, "context scope")
	now := time.Now().UTC()
	foreignWS := "ws_context_foreign"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: foreignWS, Name: "foreign", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	foreign, err := application.SeedWorkspaceLocation(ctx, store, foreignWS)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SetDevelopmentContext(ctx, wi.ID, application.SetDevelopmentContextParams{
		WorkspaceLocationID: foreign.ID, RefKind: domain.RefRoot, ExpectedVersion: wi.Version,
	}); !errors.Is(err, domain.ErrWorkspaceContextMismatch) {
		t.Fatalf("foreign workspace location 必须拒绝，实际 %v", err)
	}
	if _, err := store.WorkItemContexts().Get(ctx, wi.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("拒绝后不得落 foreign context，实际 %v", err)
	}

	// Defense in depth: a persisted legacy/corrupt foreign context must also be
	// rejected at Run creation before any queued Run or Snapshot is written.
	if err := store.WorkItemContexts().Upsert(ctx, &domain.DevelopmentContext{
		WorkItemID: wi.ID, ContextOwnerID: wi.ID, WorkspaceLocationID: foreign.ID,
		RefKind: domain.RefRoot, Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "不得越界"}); !errors.Is(err, domain.ErrWorkspaceContextMismatch) {
		t.Fatalf("foreign persisted context 的 CreateRun 必须拒绝，实际 %v", err)
	}
	runs, err := store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("foreign context 拒绝不得创建 Run: %+v", runs)
	}
}

func TestChildContextCannotLeaveImplicitRootLocation(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, _ := newContextTestSvc(t, "ws_context_child")
	root := mustTask(t, ctx, svc, wsID, "implicit root")
	if err := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
		ExecutionHostID: domain.LocalHostID, Alias: "alt", RepositoryIdentity: "repo_alt",
		RegistryGeneration: "gen_alt", Status: domain.MountStatusReady, LastSeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	alt, err := svc.CreateWorkspaceLocation(ctx, wsID, application.CreateWorkspaceLocationParams{
		ExecutionHostID: domain.LocalHostID, MountAlias: "alt", RepositoryIdentity: "repo_alt",
		MountGeneration: "gen_alt",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "child", ParentID: root.ID, RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetDevelopmentContext(ctx, child.ID, application.SetDevelopmentContextParams{
		WorkspaceLocationID: alt.ID, RefKind: domain.RefRoot, ExpectedVersion: child.Version,
	}); !errors.Is(err, domain.ErrDevelopmentContextInvalid) {
		t.Fatalf("child 不得离开 implicit root location，实际 %v", err)
	}
}

func TestInheritedSnapshotMustStayInWorkspaceAndTaskTree(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_snapshot_scope")
	target := mustTask(t, ctx, svc, wsID, "target")
	otherRoot := mustTask(t, ctx, svc, wsID, "other root")
	otherRun, err := svc.CreateRun(ctx, otherRoot.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "other"})
	if err != nil {
		t.Fatal(err)
	}
	otherSnap, err := store.ContextSnapshots().GetByRun(ctx, otherRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRun(ctx, target.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "must reject other root",
		ContextSource: domain.SnapshotSourceInherited, ContextSourceSnapshotID: otherSnap.ID,
	}); !errors.Is(err, domain.ErrWorkspaceContextMismatch) {
		t.Fatalf("跨根 inherited snapshot 必须拒绝，实际 %v", err)
	}

	now := time.Now().UTC()
	foreignWS := "ws_snapshot_foreign"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: foreignWS, Name: "foreign snapshot", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.SeedWorkspaceLocation(ctx, store, foreignWS); err != nil {
		t.Fatal(err)
	}
	foreignTask, err := svc.CreateWorkItem(ctx, foreignWS, application.CreateWorkItemParams{Title: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	foreignRun, err := svc.CreateRun(ctx, foreignTask.ID, application.CreateRunParams{Instruction: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	foreignSnap, err := store.ContextSnapshots().GetByRun(ctx, foreignRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRun(ctx, target.ID, application.CreateRunParams{
		AgentProfileID: agentID, Instruction: "must reject foreign workspace",
		ContextSource: domain.SnapshotSourceInherited, ContextSourceSnapshotID: foreignSnap.ID,
	}); !errors.Is(err, domain.ErrWorkspaceContextMismatch) {
		t.Fatalf("跨 workspace inherited snapshot 必须拒绝，实际 %v", err)
	}
	runs, err := store.Runs().ListByWorkItem(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("非法 source snapshot 不得创建 target Run: %+v", runs)
	}
}

func TestWorkspaceLocationRequiresAdvertisedGenerationAndIdentity(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, _ := newContextTestSvc(t, "ws_location_generation")
	if err := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
		ExecutionHostID: domain.LocalHostID, Alias: "alt", RepositoryIdentity: "repo_alt",
		RegistryGeneration: "gen_alt", Status: domain.MountStatusReady, LastSeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateWorkspaceLocation(ctx, wsID, application.CreateWorkspaceLocationParams{
		ExecutionHostID: domain.LocalHostID, MountAlias: "alt", RepositoryIdentity: "repo_alt",
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("缺 mount_generation 的 create 必须拒绝，实际 %v", err)
	}
	loc, err := store.WorkspaceLocations().DefaultFor(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateWorkspaceLocation(ctx, loc.ID, application.UpdateWorkspaceLocationParams{
		RepositoryIdentity: "repo_forged", MountGeneration: loc.MountGeneration, ExpectedVersion: loc.Version,
	}); !errors.Is(err, domain.ErrWorkspaceContextMismatch) {
		t.Fatalf("不匹配的 repository identity 必须拒绝，实际 %v", err)
	}
	if _, err := svc.UpdateWorkspaceLocation(ctx, loc.ID, application.UpdateWorkspaceLocationParams{
		ExpectedVersion: loc.Version,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("缺 mount_generation 的 update 必须拒绝，实际 %v", err)
	}
	if _, err := svc.UpdateWorkspaceLocation(ctx, loc.ID, application.UpdateWorkspaceLocationParams{
		MountGeneration: loc.MountGeneration,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("缺 expected_version 的 update 必须拒绝，实际 %v", err)
	}
}

// TestStaticValidationRollsBackRunCreation 静态校验失败整事务回滚：
// branch 不唯一 → workspace_branch_not_unique，无 queued Run、无快照残留；
// branch 唯一但 checkout 已被非终态 Run 占用 → workspace_checkout_busy。
func TestStaticValidationRollsBackRunCreation(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_static")
	wi := mustTask(t, ctx, svc, wsID, "静态校验")

	upsertMount := func(checkouts []domain.MountCheckout) {
		t.Helper()
		if err := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
			ExecutionHostID: domain.LocalHostID, Alias: "default",
			RepositoryIdentity: "repo_default", RegistryGeneration: "gen_seed",
			Status: domain.MountStatusReady, Checkouts: checkouts, LastSeenAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	upsertMount([]domain.MountCheckout{{Ref: "wt_a", Kind: "worktree", Branch: "feature/x"}})
	loc, err := svc.ListWorkspaceLocations(ctx, wsID)
	if err != nil || len(loc) == 0 {
		t.Fatalf("缺默认 Location: %v", err)
	}
	fresh, err := store.WorkItems().Get(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetDevelopmentContext(ctx, wi.ID, application.SetDevelopmentContextParams{
		WorkspaceLocationID: loc[0].ID, RefKind: domain.RefBranch,
		BranchName: "feature/x", CheckoutRef: "wt_a", ExpectedVersion: fresh.Version,
	}); err != nil {
		t.Fatal(err)
	}

	// 广告换代为两个同名 branch checkout → Run 创建必须整体回滚。
	upsertMount([]domain.MountCheckout{
		{Ref: "wt_a", Kind: "worktree", Branch: "feature/x"},
		{Ref: "wt_b", Kind: "worktree", Branch: "feature/x"},
	})
	before, err := store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "会失败"})
	if !errors.Is(err, domain.ErrWorkspaceBranchNotUnique) {
		t.Fatalf("应报 workspace_branch_not_unique，实际 %v", err)
	}
	after, err := store.Runs().ListByWorkItem(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("静态校验失败不得留下 queued Run：before=%d after=%d", len(before), len(after))
	}

	// 恢复唯一 checkout：第一个 Run 占用 wt_a；第二个 Run 命中 checkout_busy。
	upsertMount([]domain.MountCheckout{{Ref: "wt_a", Kind: "worktree", Branch: "feature/x"}})
	first, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "占用者"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "第二个"})
	if !errors.Is(err, domain.ErrWorkspaceCheckoutBusy) {
		t.Fatalf("应报 workspace_checkout_busy，实际 %v", err)
	}
	if first.Status != domain.RunQueued {
		t.Fatalf("占用者应保持 queued: %+v", first)
	}
}

// leaseFor 为 run 建 runner + lease，返回 fencing token。
func leaseFor(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID, runID, leaseID string) int64 {
	t.Helper()
	if err := store.Runners().Upsert(ctx, &application.Runner{
		ID: "runner_ctx", WorkspaceID: wsID, ExecutionHostID: domain.LocalHostID,
		Status: "connected", Slots: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Runners().CreateLease(ctx, &application.RunLease{
		LeaseID: leaseID, RunID: runID, RunnerID: "runner_ctx",
		RenewedUntil: time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Runners().ActiveLease(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return lease.FencingToken
}

// TestApplyRunnerEventAtomicEntry Runner v2 事件原子入口（RFC §8.3.1）：
// stale/duplicate ACK 不应用；应用进入白名单事件流；毒帧落
// failed(runner_event_invalid)；终态同事务释放 lease。
func TestApplyRunnerEventAtomicEntry(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_events")
	wi := mustTask(t, ctx, svc, wsID, "runner 事件")

	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "远程执行"})
	if err != nil {
		t.Fatal(err)
	}
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_1")

	// ① 正常应用：message.delta 进 run_events + stream。
	ack, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_1", RunnerID: "runner_ctx",
		FencingToken: fencing, EventID: "revt_1", ProducerSeq: 1,
		Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "hi"},
	})
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("首个事件应 applied: ack=%+v err=%v", ack, err)
	}
	events, err := store.Events().ListRunEvents(ctx, run.ID)
	if err != nil || len(events) == 0 {
		t.Fatalf("事件应已落 run_events: %v %+v", err, events)
	}

	// ② duplicate：同 (run, lease, runner, seq) 重发 → ACK 不应用。
	_, err = svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_1", RunnerID: "runner_ctx",
		FencingToken: fencing, EventID: "revt_1", ProducerSeq: 1,
		Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "hi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dup, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_1", RunnerID: "runner_ctx",
		FencingToken: fencing, EventID: "revt_1b", ProducerSeq: 1,
		Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "hi2"},
	})
	if err != nil || dup.Outcome != application.RunnerEventDuplicate {
		t.Fatalf("重复 producer_seq 应 duplicate: ack=%+v err=%v", dup, err)
	}
	after, err := store.Events().ListRunEvents(ctx, run.ID)
	if err != nil || len(after) != len(events) {
		t.Fatalf("duplicate 不得重复应用: before=%d after=%d", len(events), len(after))
	}

	// ③ stale：fencing 失配（旧 lease 帧）→ ACK 不应用。
	stale, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_1", RunnerID: "runner_ctx",
		FencingToken: fencing + 1, EventID: "revt_2", ProducerSeq: 2,
		Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "stale"},
	})
	if err != nil || stale.Outcome != application.RunnerEventStale {
		t.Fatalf("fencing 失配应 stale: ack=%+v err=%v", stale, err)
	}

	// ④ 毒帧：未知 kind → Run failed(runner_event_invalid)，lease 同事务释放。
	if _, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_1", RunnerID: "runner_ctx",
		FencingToken: fencing, EventID: "revt_3", ProducerSeq: 3,
		Kind: "bogus.frame", Data: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	final, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != domain.RunFailed || final.Failure == nil || final.Failure.Code != "runner_event_invalid" {
		t.Fatalf("毒帧应落 failed(runner_event_invalid): %+v %+v", final.Status, final.Failure)
	}
	lease, err := store.Runners().ActiveLease(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil || !lease.Released {
		t.Fatalf("终态应同事务释放 lease: %+v", lease)
	}

	// ⑤ 终态后迟到帧：stale ACK 不应用（不复活终态 Run）。
	late, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_1", RunnerID: "runner_ctx",
		FencingToken: fencing, EventID: "revt_4", ProducerSeq: 4,
		Kind: domain.EventMessageDelta, Data: map[string]any{"role": "assistant", "text": "late"},
	})
	if err != nil || late.Outcome != application.RunnerEventStale {
		t.Fatalf("终态后帧应 stale: ack=%+v err=%v", late, err)
	}
}

func TestRemoteApprovalEventPersistsRunnerIDAndHonorsGrant(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_remote_approval")
	wi := mustTask(t, ctx, svc, wsID, "远程审批")
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "remote approval"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_remote_approval")
	now := time.Now().UTC()
	if err := store.ApprovalGrants().Create(ctx, &domain.ApprovalGrant{
		ID: "grant_remote_approval", WorkspaceID: wsID, AgentProfileID: agentID, WorkItemID: wi.ID,
		Scope: domain.ApprovalScopeThread, Kind: domain.ApprovalKindCommand, Pattern: "runner danger",
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	forwarded := make(chan string, 1)
	svc.ApprovalForwarder = func(_ context.Context, _ string, approvalID string, approved bool) {
		if approved {
			forwarded <- approvalID
		}
	}
	ack, err := svc.ApplyRunnerEvent(ctx, application.RunnerEventInput{
		RunID: run.ID, LeaseID: "lease_remote_approval", RunnerID: "runner_ctx", FencingToken: fencing,
		EventID: "revt_remote_approval", ProducerSeq: 1, Kind: domain.EventApprovalRequested,
		Data: map[string]any{"kind": domain.ApprovalKindCommand, "risk": "high", "summary": "runner danger command", "approval_id": "apr_local_remote"},
	})
	if err != nil || ack.Outcome != application.RunnerEventApplied {
		t.Fatalf("remote approval event: ack=%+v err=%v", ack, err)
	}
	approvals, err := store.Runs().ListApprovals(ctx, run.ID)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("remote approval 应落库: %v %+v", err, approvals)
	}
	approval := approvals[0]
	if got, _ := approval.RequestedBy["runner_approval_id"].(string); got != "apr_local_remote" {
		t.Fatalf("runner approval_id 必须持久化: %+v", approval.RequestedBy)
	}
	select {
	case got := <-forwarded:
		if got != approval.ID {
			t.Fatalf("grant 自动决议转发错误: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote approval grant 命中后应自动决议并转发")
	}
	updated, err := store.Runs().GetApproval(ctx, approval.ID)
	if err != nil || updated.Status != domain.ApprovalApproved {
		t.Fatalf("grant 自动决议未持久化: %v %+v", err, updated)
	}
}

func TestRunnerPoisonFramesFailReleaseAndAckIdempotently(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_runner_poison")
	cases := []struct {
		name string
		kind string
		data map[string]any
	}{
		{"progress missing", domain.EventRunProgressUpdated, map[string]any{}},
		{"progress type", domain.EventRunProgressUpdated, map[string]any{"progress": "half"}},
		{"progress range", domain.EventRunProgressUpdated, map[string]any{"progress": 1.1}},
		{"session ref missing", "run.session", map[string]any{}},
		{"session params type", "run.session", map[string]any{"ref": "codex://x", "params": "bad"}},
		{"usage negative", domain.EventUsageUpdated, map[string]any{"input_tokens": -1, "output_tokens": 0, "cached_tokens": 0, "basis": "per_run"}},
		{"usage fractional", domain.EventUsageUpdated, map[string]any{"input_tokens": 1.5, "output_tokens": 0, "cached_tokens": 0, "basis": "per_run"}},
		{"usage overflow", domain.EventUsageUpdated, map[string]any{"input_tokens": 9223372036854775808.0, "output_tokens": 0, "cached_tokens": 0, "basis": "per_run"}},
		{"approval risk", domain.EventApprovalRequested, map[string]any{"kind": "command", "risk": "critical", "summary": "bad", "approval_id": "apr_bad"}},
		{"artifact size", "artifact.manifest", map[string]any{"logical_path": "out.md", "mime": "text/markdown", "size": -1, "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		{"artifact sha", "artifact.manifest", map[string]any{"logical_path": "out.md", "mime": "text/markdown", "size": 1, "sha256": "not-a-sha"}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wi := mustTask(t, ctx, svc, wsID, "poison "+tc.name)
			run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: tc.name})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordRunStatus(ctx, run.ID, domain.RunRunning, nil); err != nil {
				t.Fatal(err)
			}
			leaseID := "lease_poison_" + string(rune('a'+i))
			fencing := leaseFor(t, ctx, store, wsID, run.ID, leaseID)
			in := application.RunnerEventInput{
				RunID: run.ID, LeaseID: leaseID, RunnerID: "runner_ctx", FencingToken: fencing,
				EventID: "revt_poison_" + string(rune('a'+i)), ProducerSeq: 1, Kind: tc.kind, Data: tc.data,
			}
			ack, err := svc.ApplyRunnerEvent(ctx, in)
			if err != nil || ack.Outcome != application.RunnerEventApplied {
				t.Fatalf("poison 应提交 failed 后 ACK: ack=%+v err=%v", ack, err)
			}
			stored, err := store.Runs().Get(ctx, run.ID)
			if err != nil || stored.Status != domain.RunFailed || stored.Failure == nil || stored.Failure.Code != "runner_event_invalid" {
				t.Fatalf("poison Run 未收敛 failed: %v %+v", err, stored)
			}
			lease, err := store.Runners().ActiveLease(ctx, run.ID)
			if err != nil || !lease.Released {
				t.Fatalf("poison 必须同事务释放 lease: %v %+v", err, lease)
			}
			dup, err := svc.ApplyRunnerEvent(ctx, in)
			if err != nil || (dup.Outcome != application.RunnerEventStale && dup.Outcome != application.RunnerEventDuplicate) {
				t.Fatalf("poison 重放必须 ACK 且无副作用: ack=%+v err=%v", dup, err)
			}
		})
	}
}

// TestApplyRunnerRejectIdempotent reject 固定行为：CAS 释放 lease + Run 落
// failed(retryable, family=workspace)；重复 reject 幂等 no-op。
func TestApplyRunnerRejectIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_reject")
	wi := mustTask(t, ctx, svc, wsID, "reject")

	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "将被拒"})
	if err != nil {
		t.Fatal(err)
	}
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_r")

	rejectInput := application.RunnerRejectInput{
		RunID: run.ID, LeaseID: "lease_r", RunnerID: "runner_ctx",
		FencingToken: fencing, ReasonCode: "workspace_ref_not_resolvable",
		ReasonFamily: "workspace", ReasonMessage: "worktree 已删除",
	}
	if err := svc.ApplyRunnerReject(ctx, rejectInput); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != domain.RunFailed || failed.Failure == nil ||
		!failed.Failure.Retryable || failed.ErrorFamily != "workspace" {
		t.Fatalf("reject 应落 failed(retryable, workspace): %+v %+v", failed.Status, failed.Failure)
	}
	lease, err := store.Runners().ActiveLease(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil || !lease.Released {
		t.Fatalf("reject 应释放 lease: %+v", lease)
	}
	// 重复 reject：幂等 no-op，不产生第二次状态迁移/错误。
	if err := svc.ApplyRunnerReject(ctx, rejectInput); err != nil {
		t.Fatalf("重复 reject 应幂等: %v", err)
	}
	again, err := store.Runs().Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Version != failed.Version {
		t.Fatalf("重复 reject 不得改写终态 Run: %d → %d", failed.Version, again.Version)
	}
}

// TestApplyRunnerAcceptDigestGate accept 校验 lease/fencing + snapshot digest
// 一致；digest 失配必须报错（Runner 端应改走 workspace reject）。
func TestApplyRunnerAcceptDigestGate(t *testing.T) {
	ctx := context.Background()
	svc, store, _, wsID, agentID := newContextTestSvc(t, "ws_accept")
	wi := mustTask(t, ctx, svc, wsID, "accept")

	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: agentID, Instruction: "接单"})
	if err != nil {
		t.Fatal(err)
	}
	fencing := leaseFor(t, ctx, store, wsID, run.ID, "lease_a")
	snap := snapshotOfRun(t, ctx, store, run.ID)

	if err := svc.ApplyRunnerAccept(ctx, application.RunnerAcceptInput{
		RunID: run.ID, LeaseID: "lease_a", RunnerID: "runner_ctx",
		FencingToken: fencing, SnapshotDigest: snap.SnapshotDigest,
	}); err != nil {
		t.Fatalf("digest 一致的 accept 应通过: %v", err)
	}
	err = svc.ApplyRunnerAccept(ctx, application.RunnerAcceptInput{
		RunID: run.ID, LeaseID: "lease_a", RunnerID: "runner_ctx",
		FencingToken: fencing, SnapshotDigest: "deadbeef",
	})
	if !errors.Is(err, domain.ErrWorkspaceContextMismatch) {
		t.Fatalf("digest 失配的 accept 应报 workspace_context_mismatch，实际 %v", err)
	}
}
