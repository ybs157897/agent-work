package sqlstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestTaskCoordinatorConfigIsSingletonAndProtected(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)

	config, err := store.TaskCoordinators().EnsureConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if config.AgentProfileID == "" || config.PromptVersion != domain.TaskCoordinatorPromptVersion {
		t.Fatalf("EnsureConfig 应创建固定 system profile/prompt version: %+v", config)
	}
	if config.RuntimeLabel != "mock" {
		t.Fatalf("无 runtime binding 时应显式落 mock 配置，实际 %q", config.RuntimeLabel)
	}
	if config.ModelRef.Provider != "mock" || config.ModelRef.Model != "mock" {
		t.Fatalf("mock 默认模型快照缺失: %+v", config.ModelRef)
	}

	second, err := store.TaskCoordinators().EnsureConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != config.ID || second.AgentProfileID != config.AgentProfileID || second.Version != config.Version {
		t.Fatalf("EnsureConfig 非幂等: first=%+v second=%+v", config, second)
	}
	profiles, err := store.Agents().List(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("普通 Agent 列表不得暴露 system coordinator: %+v", profiles)
	}
	profile, err := store.Agents().Get(ctx, config.AgentProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Kind != domain.AgentProfileKindTaskCoordinator || profile.InstructionsEditable || profile.Policy.Sandbox != "read-only" {
		t.Fatalf("system coordinator profile 保护属性错误: %+v", profile)
	}

	profile.Instructions = "用户覆盖"
	if err := store.Agents().Update(ctx, profile, profile.Version); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("普通 Agent Update 应拒绝 system profile，实际 %v", err)
	}
	if _, err := db.Exec(`UPDATE agent_profiles SET instructions='用户覆盖' WHERE id=?`, config.AgentProfileID); err == nil {
		t.Fatal("直接 SQL 修改 system prompt 应被 trigger 拒绝")
	}
	if _, err := db.Exec(`UPDATE agent_profiles SET policy=? WHERE id=?`, `{"sandbox":"danger-full-access","tools":["bash"]}`, config.AgentProfileID); err == nil {
		t.Fatal("直接 SQL 扩大 system coordinator 权限应被 trigger 拒绝")
	}

	config.ModelRef = domain.ModelRef{Ref: "codex-fast", Provider: "openai", Model: "gpt-test"}
	config.ReasoningEffort = "high"
	if err := store.TaskCoordinators().UpdateConfig(ctx, config, config.Version); err != nil {
		t.Fatal(err)
	}
	updated, err := store.TaskCoordinators().GetConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != config.Version+1 || updated.ModelRef.Ref != "codex-fast" || updated.ModelRef.Model != "gpt-test" || updated.ReasoningEffort != "high" {
		t.Fatalf("Coordinator config 更新未持久化: %+v", updated)
	}
	updated.RuntimeLabel = "dsh_local"
	if err := store.TaskCoordinators().UpdateConfig(ctx, updated, updated.Version); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("非法 Coordinator runtime 应拒绝，实际 %v", err)
	}
}

func TestTaskCoordinatorDefaultPrefersReadyCodexThenKimi(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	now := time.Now().UTC()
	for _, binding := range []*domain.RuntimeBinding{
		{ID: "rb_coord_kimi", WorkspaceID: "ws_wk", RuntimeLabel: "kimi_local", AdapterID: "kimi-appserver", Status: domain.BindingReady, Provider: "moonshot", Model: "kimi-test", Version: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "rb_coord_codex", WorkspaceID: "ws_wk", RuntimeLabel: "codex_local", AdapterID: "codex-appserver", Status: domain.BindingReady, Provider: "openai", Model: "codex-test", Version: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.Bindings().Create(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	config, err := store.TaskCoordinators().EnsureConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if config.RuntimeLabel != "codex_local" || config.ModelRef.Model != "codex-test" {
		t.Fatalf("ready Codex 应优先于 Kimi: %+v", config)
	}
}

func TestTaskCoordinatorStateResolvesRootAndDueRecovery(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	config, err := store.TaskCoordinators().EnsureConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	root := &domain.WorkItem{ID: "wi_coord_root", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "root", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1,
		CreatedAt: now, UpdatedAt: now}
	child := &domain.WorkItem{ID: "wi_coord_child", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		ParentID: root.ID, Title: "child", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1,
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	other := &domain.WorkItem{ID: "wi_coord_other", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "other", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1,
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}
	for _, item := range []*domain.WorkItem{root, child, other} {
		if err := store.WorkItems().Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	state := &domain.TaskCoordinatorState{
		ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: root.WorkspaceID,
		RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID,
		Status: domain.CoordinatorQueued, CurrentAction: "start", Attempt: 0,
		NextActionAt: func() *time.Time { v := now.Add(-time.Minute); return &v }(),
		Version:      1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.TaskCoordinators().CreateState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := store.TaskCoordinators().CreateState(ctx, state); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("同 root 不得创建第二条 state，实际 %v", err)
	}
	resolved, err := store.TaskCoordinators().GetStateForWorkItem(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RootWorkItemID != root.ID {
		t.Fatalf("子 WorkItem 未解析到 root state: %+v", resolved)
	}
	if due, err := store.TaskCoordinators().ListDueStates(ctx, "ws_wk", now, 10); err != nil || len(due) != 1 || due[0].RootWorkItemID != root.ID {
		t.Fatalf("due state 恢复查询错误: err=%v due=%+v", err, due)
	}
	state.Summary = "updated"
	state.Status = domain.CoordinatorRunning
	if err := store.TaskCoordinators().UpdateState(ctx, state, state.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.TaskCoordinators().UpdateState(ctx, state, state.Version); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("旧 version 更新应 CAS 拒绝，实际 %v", err)
	}

	rootEvent := &domain.TaskCoordinatorEvent{
		ID: domain.NewID(domain.PrefixCoordinatorEvent), WorkspaceID: root.WorkspaceID,
		RootWorkItemID: root.ID, WorkItemID: child.ID, Kind: "coordinator.dispatch",
		Summary: "派发 child", AgentID: config.AgentProfileID, Attempt: 1,
		Data: map[string]any{"stage": "dispatch"}, OccurredAt: now,
	}
	if err := store.TaskCoordinators().AppendEvent(ctx, rootEvent); err != nil {
		t.Fatal(err)
	}
	otherEvent := *rootEvent
	otherEvent.ID = domain.NewID(domain.PrefixCoordinatorEvent)
	otherEvent.RootWorkItemID = other.ID
	otherEvent.WorkItemID = other.ID
	if err := store.TaskCoordinators().AppendEvent(ctx, &otherEvent); err != nil {
		t.Fatal(err)
	}
	wrongRoot := *rootEvent
	wrongRoot.ID = domain.NewID(domain.PrefixCoordinatorEvent)
	wrongRoot.RootWorkItemID = other.ID
	wrongRoot.WorkItemID = child.ID
	if err := store.TaskCoordinators().AppendEvent(ctx, &wrongRoot); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("跨 root event 应拒绝，实际 %v", err)
	}
	entries, err := store.TaskCoordinators().ListEvents(ctx, child.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].WorkItemID != child.ID {
		t.Fatalf("子项查询应返回 root timeline: %+v", entries)
	}
	if entries[0].Data["root_work_item_id"] != root.ID || entries[0].Data["work_item_id"] != child.ID || entries[0].Data["record_kind"] != string(domain.RecordKindTask) {
		t.Fatalf("Coordinator event scope 元数据缺失: %+v", entries[0].Data)
	}
	if _, err := db.Exec(`UPDATE task_coordinator_events SET summary='tampered' WHERE id=?`, rootEvent.ID); err == nil {
		t.Fatal("Coordinator event 必须 append-only，直接 UPDATE 应拒绝")
	}
	if _, err := db.Exec(`DELETE FROM task_coordinator_events WHERE id=?`, rootEvent.ID); err == nil {
		t.Fatal("Coordinator event 必须 append-only，直接 DELETE 应拒绝")
	}
}

func TestTaskCoordinatorDueScanSkipsIdleRunningAndKeepsPendingStatesVisible(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	config, err := store.TaskCoordinators().EnsureConfig(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	create := func(id string, status domain.TaskCoordinatorStateStatus, updated time.Time, data map[string]any) {
		t.Helper()
		root := &domain.WorkItem{ID: id + "_root", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
			Title: id, Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1,
			CreatedAt: updated, UpdatedAt: updated}
		if err := store.WorkItems().Create(ctx, root); err != nil {
			t.Fatal(err)
		}
		state := &domain.TaskCoordinatorState{
			ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: root.WorkspaceID,
			RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID,
			Status: status, CurrentAction: "等待 Worker 结果", Data: data,
			Version: 1, CreatedAt: updated, UpdatedAt: updated,
		}
		if err := store.TaskCoordinators().CreateState(ctx, state); err != nil {
			t.Fatal(err)
		}
	}

	// These observation-only checkpoints are older than the queued state and
	// must not consume a small due-scan LIMIT.
	for i := 0; i < 3; i++ {
		create("wi_coord_idle"+string(rune('a'+i)), domain.CoordinatorRunning,
			now.Add(-10*time.Minute+time.Duration(i)*time.Second), nil)
	}
	create("wi_coord_empty_action", domain.CoordinatorRunning, now.Add(-9*time.Minute),
		map[string]any{"control_action": ""})
	create("wi_coord_queued", domain.CoordinatorQueued, now.Add(-time.Minute), nil)
	due, err := store.TaskCoordinators().ListDueStates(ctx, "ws_wk", now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].RootWorkItemID != "wi_coord_queued_root" {
		t.Fatalf("idle running checkpoint 不得饿死 queued 状态: %+v", due)
	}

	// A running checkpoint with an explicit control action remains recoverable
	// even when it has no current Run or next_action_at.
	create("wi_coord_control", domain.CoordinatorRunning, now.Add(-30*time.Second),
		map[string]any{"control_action": "retry_worker"})
	due, err = store.TaskCoordinators().ListDueStates(ctx, "ws_wk", now, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundControl := false
	for _, state := range due {
		if state.RootWorkItemID == "wi_coord_control_root" {
			foundControl = true
		}
		if state.RootWorkItemID == "wi_coord_idlea_root" || state.RootWorkItemID == "wi_coord_idleb_root" || state.RootWorkItemID == "wi_coord_idlec_root" || state.RootWorkItemID == "wi_coord_empty_action_root" {
			t.Fatalf("idle running checkpoint 不应出现在 due scan: %+v", state)
		}
	}
	if !foundControl {
		t.Fatalf("显式 control_action 的 running 状态必须可恢复: %+v", due)
	}
}
