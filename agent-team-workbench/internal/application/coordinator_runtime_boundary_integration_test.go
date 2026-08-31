package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/runtime/adapters/mock"
)

func TestCoordinatorRuntimeBindingMustBeReadyAndMatchItsLabel(t *testing.T) {
	tests := []struct {
		name      string
		id        string
		adapterID string
		status    domain.RuntimeBindingStatus
		wantError string
	}{
		{name: "unavailable", id: "unavailable", adapterID: "codex-appserver", status: domain.BindingUnavailable, wantError: "尚未就绪"},
		{name: "mismatched adapter", id: "mismatched", adapterID: "kimi-appserver", status: domain.BindingReady, wantError: "不匹配"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
			config, err := store.TaskCoordinators().EnsureConfig(ctx, wsID)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
				ID: "rb_boundary_" + tt.id, WorkspaceID: wsID,
				RuntimeLabel: "codex_local", AdapterID: tt.adapterID,
				Provider: "codex", Status: tt.status, Version: 1,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			config.RuntimeLabel = "codex_local"
			config.ModelRef = domain.ModelRef{ReasoningEffort: "medium"}
			if err := store.TaskCoordinators().UpdateConfig(ctx, config, config.Version); err != nil {
				t.Fatal(err)
			}
			root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
				Title: "Runtime 边界", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(dispatcher.runs) != 0 {
				t.Fatalf("invalid Coordinator binding 不得产生 Run: %+v", dispatcher.runs)
			}
			state, err := store.TaskCoordinators().GetState(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Status != domain.CoordinatorBlocked || !strings.Contains(state.BlockerMessage, tt.wantError) {
				t.Fatalf("invalid binding 必须明确阻塞，want %q: %+v", tt.wantError, state)
			}
		})
	}
}

func TestCoordinatedPlanRequiresWaitBarrierAfterDispatch(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "等待 Worker 的计划", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().GetConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: config.AgentProfileID, SourceRunID: coordinatorRun.ID,
		Steps: []application.PlanStepInput{dispatchStep(workerID, "实现", "执行任务")},
	})
	if err == nil || !strings.Contains(err.Error(), "join/defer") {
		t.Fatalf("coordinated dispatch without wait barrier 应拒绝: %v", err)
	}
	if children, listErr := store.WorkItems().ListByParent(ctx, root.ID); listErr != nil || len(children) != 0 {
		t.Fatalf("被拒计划不得留下子任务: %v %+v", listErr, children)
	}
}

func TestCoordinatedPlanRejectsFinishBeforeWaitBarrier(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "finish 不得插队", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().GetConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: config.AgentProfileID, SourceRunID: coordinatorRun.ID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "实现", "执行任务"),
			{Verb: "finish"},
			{Verb: "join", Payload: map[string]any{"children": "all"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "finish") || !strings.Contains(err.Error(), "join/defer") {
		t.Fatalf("finish before wait barrier 应拒绝: %v", err)
	}
	if children, listErr := store.WorkItems().ListByParent(ctx, root.ID); listErr != nil || len(children) != 0 {
		t.Fatalf("被拒计划不得留下子任务: %v %+v", listErr, children)
	}
}

func TestCoordinatedPlanRejectsStoppedOrStaleCoordinatorState(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "过期 Plan", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().GetConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorWaitingUser
	state.CurrentRunID = ""
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: config.AgentProfileID, SourceRunID: source.ID,
		Steps: []application.PlanStepInput{{Verb: "finish"}},
	})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("waiting_user Coordinator 不得提交 Plan: %v", err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected = state.Version
	state.Status = domain.CoordinatorRunning
	state.CurrentRunID = ""
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: root.ID, AgentProfileID: config.AgentProfileID, SourceRunID: source.ID,
		Steps: []application.PlanStepInput{{Verb: "finish"}},
	})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("旧 Coordinator Run 不得提交 Plan: %v", err)
	}
}

func TestCoordinatorLazilyProbesBuiltinRuntimeBeforeFirstRun(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	registry := atwruntime.NewRegistry()
	registry.Register("mock", mock.NewWithStep(time.Millisecond))
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, registry)
	now := time.Now().UTC()
	wsID := "ws_coordinator_lazy_probe"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: wsID, Name: "Lazy probe", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	seedCtx(t, store, ctx, wsID)
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: "agent_lazy_worker", WorkspaceID: wsID, Name: "Forge", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_lazy_mock", WorkspaceID: wsID, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingUnavailable,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "创建即自动探测", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := store.Bindings().GetByLabel(ctx, wsID, "mock")
	if err != nil {
		t.Fatal(err)
	}
	if binding.Status != domain.BindingReady || len(dispatcher.runs) != 1 || root.Status != domain.WorkItemInProgress {
		t.Fatalf("首次接取应探测 Runtime 后直接创建 Run: binding=%+v runs=%d root=%+v", binding, len(dispatcher.runs), root)
	}
}
