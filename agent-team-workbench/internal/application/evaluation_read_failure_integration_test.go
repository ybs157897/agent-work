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

type failRunEventsRepo struct {
	application.EventRepo
	target string
}

func (r *failRunEventsRepo) ListRunEvents(ctx context.Context, runID string) ([]application.RunEvent, error) {
	if runID == r.target {
		return nil, errors.New("injected run event read failure")
	}
	return r.EventRepo.ListRunEvents(ctx, runID)
}

type failRunEventsStore struct {
	application.Store
	events *failRunEventsRepo
}

func (s *failRunEventsStore) Events() application.EventRepo { return s.events }

func TestEvaluationEvidenceReadFailureBlocksInsteadOfFailingOpen(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	base := sqlstore.New(db, sqlstore.SQLiteDialect())
	events := &failRunEventsRepo{EventRepo: base.Events()}
	store := &failRunEventsStore{Store: base, events: events}
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, base)
	main, evalRun := createEvaluationEnv(t, ctx, svc, wsID, leadID, workerID)
	events.target = evalRun.ID
	startRun(t, ctx, svc, evalRun)
	if err := finishRun(ctx, svc, evalRun.ID, "正文已写入但读取面故障"); err != nil {
		t.Fatal(err)
	}
	wi, err := base.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := base.WorkItems().ActiveBlocker(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Status != domain.WorkItemBlocked || blocker == nil || blocker.Code != "verdict_read_failed" {
		t.Fatalf("evaluation evidence read failure must block: work_item=%+v blocker=%+v", wi, blocker)
	}
}

func TestCoordinatorPlanEvidenceReadFailureBlocksInsteadOfAccepting(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	base := sqlstore.New(db, sqlstore.SQLiteDialect())
	events := &failRunEventsRepo{EventRepo: base.Events()}
	store := &failRunEventsStore{Store: base, events: events}
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	now := time.Now().UTC()
	wsID := "ws_plan_read_failure"
	if err := base.Workspaces().Create(ctx, &domain.Workspace{
		ID: wsID, Name: "Plan read failure", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := base.Agents().Create(ctx, &domain.AgentProfile{
		ID: "agent_plan_read_worker", WorkspaceID: wsID, Name: "Forge", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := base.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_plan_read_mock", WorkspaceID: wsID, RuntimeLabel: "mock", AdapterID: "mock",
		Provider: "mock", Model: "mock", Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "计划证据读取失败", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := dispatcher.runs[0]
	events.target = run.ID
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run.ID, "计划正文暂时不可读"); err != nil {
		t.Fatal(err)
	}
	wi, err := base.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := base.WorkItems().ActiveBlocker(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := base.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Status != domain.WorkItemBlocked || state.Status != domain.CoordinatorBlocked ||
		blocker == nil || blocker.Code != "plan_read_failed" {
		t.Fatalf("Coordinator plan read failure must fail closed: wi=%+v state=%+v blocker=%+v", wi, state, blocker)
	}
}
