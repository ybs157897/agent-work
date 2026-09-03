package application_test

import (
	"context"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestSessionUnknownSelfHealStopsAtGoalLifecycleFence(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(context.Context, *application.Service, *sqlstore.Store, *domain.WorkItem, *domain.Goal) error
	}{
		{name: "paused", stop: func(ctx context.Context, svc *application.Service, _ *sqlstore.Store, _ *domain.WorkItem, goal *domain.Goal) error {
			_, err := svc.PauseGoal(ctx, goal.ID, goal.Version)
			return err
		}},
		{name: "blocked", stop: func(ctx context.Context, svc *application.Service, store *sqlstore.Store, root *domain.WorkItem, _ *domain.Goal) error {
			fresh, err := store.WorkItems().Get(ctx, root.ID)
			if err != nil {
				return err
			}
			_, err = svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
				Code: "self_heal_blocked", Message: "stop recovery", Source: "test",
			}, fresh.Version)
			return err
		}},
		{name: "cancelled", stop: func(ctx context.Context, svc *application.Service, store *sqlstore.Store, _ *domain.WorkItem, goal *domain.Goal) error {
			fresh, err := store.Goals().Get(ctx, goal.ID)
			if err != nil {
				return err
			}
			_, err = svc.CancelGoal(ctx, fresh.ID, fresh.Version)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
			root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
				Title: "self-heal lifecycle fence " + tc.name, RecordKind: domain.RecordKindTask,
				AutoCoordinate: true, AcceptanceCriteria: []string{"stopped Goal cannot create recovery work"},
			})
			if err != nil {
				t.Fatal(err)
			}
			child, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
				Title: "resumable child", ParentID: root.ID, RecordKind: domain.RecordKindTask,
				AgentProfileID: workerID, AcceptanceCriteria: []string{"child result"},
			})
			if err != nil {
				t.Fatal(err)
			}
			first, err := svc.CreateRun(ctx, child.ID, application.CreateRunParams{
				AgentProfileID: workerID, Instruction: "establish provider session",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordRunSessionRef(ctx, first.ID, "mock://self-heal-"+tc.name); err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordRunStatus(ctx, first.ID, domain.RunRunning, nil); err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordRunStatus(ctx, first.ID, domain.RunSucceeding, nil); err != nil {
				t.Fatal(err)
			}
			if err := svc.RecordRunStatus(ctx, first.ID, domain.RunSucceeded, nil); err != nil {
				t.Fatal(err)
			}
			second, err := svc.CreateRun(ctx, child.ID, application.CreateRunParams{
				AgentProfileID: workerID, Instruction: "resume provider session",
			})
			if err != nil {
				t.Fatal(err)
			}
			conversation, _ := second.Input["conversation"].(map[string]any)
			if conversation["resume_session_ref"] != "mock://self-heal-"+tc.name {
				t.Fatalf("precondition: second Run must carry resume checkpoint: %#v", conversation)
			}
			for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
				if err := svc.RecordRunStatus(ctx, second.ID, status, nil); err != nil {
					t.Fatal(err)
				}
			}
			goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.stop(ctx, svc, store, root, goal); err != nil {
				t.Fatal(err)
			}
			beforeRuns := len(dispatcher.runs)
			if err := svc.RecordRunStatus(ctx, second.ID, domain.RunFailed, map[string]any{
				"family": "session_unknown", "code": "session_not_found", "message": "provider session missing", "retryable": true,
			}); err != nil {
				t.Fatal(err)
			}
			if len(dispatcher.runs) != beforeRuns {
				t.Fatalf("%s Goal must not dispatch a self-heal Run: before=%d after=%d", tc.name, beforeRuns, len(dispatcher.runs))
			}
			session, err := store.TaskSessions().Get(ctx, wsID, workerID, "mock", child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if session.SessionRef() != "mock://self-heal-"+tc.name {
				t.Fatalf("%s Goal must not clear the provider anchor before lifecycle admission: %+v", tc.name, session)
			}
		})
	}
}

func TestPendingSessionHealRecoversAfterCommitBeforeDispatchCrash(t *testing.T) {
	ctx, _, store, _, _, _, queued := seedPendingSessionHeal(t)
	recoveryDispatcher := &captureDispatcher{}
	recovery := application.NewService(store, recoveryDispatcher, noopNotifier{}, atwruntime.NewRegistry())
	recovered, err := recovery.RecoverPendingSelfHealRuns(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("startup recovery must dispatch the committed session-heal Run: recovered=%d err=%v", recovered, err)
	}
	if len(recoveryDispatcher.runs) != 1 || recoveryDispatcher.runs[0].ID != queued.ID {
		t.Fatalf("startup recovery dispatched the wrong Run: %+v", recoveryDispatcher.runs)
	}
	recovered, err = recovery.RecoverPendingSelfHealRuns(ctx)
	if err != nil || recovered != 0 || len(recoveryDispatcher.runs) != 1 {
		t.Fatalf("same-process replay must not duplicate dispatch: recovered=%d runs=%d err=%v",
			recovered, len(recoveryDispatcher.runs), err)
	}
	if _, err := recovery.ReconcileOrphanRuns(ctx); err != nil {
		t.Fatal(err)
	}
	stillQueued, err := store.Runs().Get(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillQueued.Status != domain.RunStarting {
		t.Fatalf("a recovered in-flight dispatch must not be orphaned by the following startup sweep: %+v", stillQueued)
	}
	if err := recovery.RecordRunStatus(ctx, queued.ID, domain.RunStarting, nil); err != nil {
		t.Fatalf("runtime start must acknowledge the durable self-heal dispatch claim idempotently: %v", err)
	}
	if err := recovery.RecordRunStatus(ctx, queued.ID, domain.RunRunning, nil); err != nil {
		t.Fatalf("recovered self-heal Run must advance from dispatch claim to running: %v", err)
	}
}

func TestPausedPendingSessionHealWaitsForResume(t *testing.T) {
	ctx, _, store, _, _, goal, queued := seedPendingSessionHeal(t)
	freshGoal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	pausingSvc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	paused, err := pausingSvc.PauseGoal(ctx, freshGoal.ID, freshGoal.Version)
	if err != nil {
		t.Fatal(err)
	}
	recoveryDispatcher := &captureDispatcher{}
	recovery := application.NewService(store, recoveryDispatcher, noopNotifier{}, atwruntime.NewRegistry())
	if recovered, err := recovery.RecoverPendingSelfHealRuns(ctx); err != nil || recovered != 0 {
		t.Fatalf("paused Goal must defer queued session-heal dispatch: recovered=%d err=%v", recovered, err)
	}
	if _, err := recovery.ReconcileOrphanRuns(ctx); err != nil {
		t.Fatal(err)
	}
	deferred, err := store.Runs().Get(ctx, queued.ID)
	if err != nil || deferred.Status != domain.RunStarting {
		t.Fatalf("paused queued session-heal must survive orphan reconciliation: run=%+v err=%v", deferred, err)
	}
	if _, err := recovery.ResumeGoal(ctx, paused.ID, paused.Version); err != nil {
		t.Fatal(err)
	}
	if len(recoveryDispatcher.runs) != 1 || recoveryDispatcher.runs[0].ID != queued.ID {
		t.Fatalf("Goal resume must dispatch the deferred session-heal Run exactly once: %+v", recoveryDispatcher.runs)
	}
}

func seedPendingSessionHeal(t *testing.T) (context.Context, *application.Service, *sqlstore.Store,
	*captureDispatcher, *domain.WorkItem, *domain.Goal, *domain.ExecutionRun) {
	t.Helper()
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "queued self-heal crash", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"committed recovery survives process restart"},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "queued self-heal child", ParentID: root.ID, RecordKind: domain.RecordKindTask,
		AgentProfileID: workerID, AcceptanceCriteria: []string{"resume the child"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateRun(ctx, child.ID, application.CreateRunParams{
		AgentProfileID: workerID, Instruction: "establish crash-test session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, first.ID, "mock://queued-self-heal"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, first.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	second, err := svc.CreateRun(ctx, child.ID, application.CreateRunParams{
		AgentProfileID: workerID, Instruction: "resume before crash",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, second.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	svc.SetDispatcher(nil) // simulate process exit immediately after the recovery transaction commits
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunFailed, map[string]any{
		"family": "session_unknown", "code": "session_not_found", "retryable": true,
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs().ListByWorkItem(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	var queued *domain.ExecutionRun
	for _, run := range runs {
		if run.ClientKey == "session-heal:"+second.ID {
			queued = run
			break
		}
	}
	if queued == nil || queued.Status != domain.RunStarting {
		t.Fatalf("crash fixture must leave one deterministic queued self-heal Run: %+v", runs)
	}
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, svc, store, dispatcher, root, goal, queued
}
