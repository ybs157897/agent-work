package application_test

import (
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestParallelWorkerSuccessDoesNotClearAnotherWorkersRetryCheckpoint(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "并行 Worker retry", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	planText := "```plan\n" +
		`[{"verb":"dispatch","agent_id":"` + workerID + `","title":"A","instruction":"执行 A"},` +
		`{"verb":"dispatch","agent_id":"` + workerID + `","title":"B","instruction":"执行 B"},` +
		`{"verb":"join","children":"all"}]` + "\n```"
	if err := svc.RecordRunEvent(ctx, coordinatorRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": planText}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("expected Coordinator + two Workers, got %d", len(dispatcher.runs))
	}
	workerA, workerB := dispatcher.runs[1], dispatcher.runs[2]
	driveRunToFailure(t, ctx, svc, workerA.ID, "stream disconnected")
	if err := svc.RecordRunStatus(ctx, workerB.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, workerB.ID, "B 已完成"); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	pendingRetryID, _ := state.Data["retry_worker_run_id"].(string)
	controlAction, _ := state.Data["control_action"].(string)
	if state.Status != domain.CoordinatorWaitingRetry || pendingRetryID != workerA.ID || controlAction != "retry_worker" {
		t.Fatalf("Worker B success must preserve Worker A retry checkpoint: %+v", state)
	}
	events, err := store.TaskCoordinators().ListEvents(ctx, root.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundWorkerBResult := false
	for _, event := range events {
		if event.RunID == workerB.ID && event.Kind == domain.EventCoordinatorAttemptUpdated {
			foundWorkerBResult = true
			break
		}
	}
	if !foundWorkerBResult {
		t.Fatalf("preserving Worker A retry must not hide Worker B success from the timeline: %+v", events)
	}
}

func TestNonRetryableWorkerFailureBlocksAndClosesDispatchDegraded(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "不可重试失败", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	planText := "```plan\n" +
		`[{"verb":"dispatch","agent_id":"` + workerID + `","title":"实现","instruction":"执行"},` +
		`{"verb":"join","children":"all"}]` + "\n```"
	if err := svc.RecordRunEvent(ctx, coordinatorRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": planText}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	worker := dispatcher.runs[1]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, worker.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunFailed, map[string]any{
		"code": "auth_failed", "message": "permission denied", "retryable": false,
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := store.Dispatches().Get(ctx, worker.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || dispatch.Status != domain.DispatchDegraded || dispatch.ClosedAt == nil {
		t.Fatalf("non-retryable failure must block and close dispatch degraded: state=%+v dispatch=%+v", state, dispatch)
	}
}

func TestUserBlockClosesOpenDispatchAndLateWorkerCannotReviveIt(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "用户暂停派发", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	planText := "```plan\n" +
		`[{"verb":"dispatch","agent_id":"` + workerID + `","title":"实现","instruction":"执行"},` +
		`{"verb":"join","children":"all"}]` + "\n```"
	if err := svc.RecordRunEvent(ctx, coordinatorRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": planText}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	worker := dispatcher.runs[1]
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BlockWorkItem(ctx, root.ID, application.BlockParams{
		Code: "user_pause", Message: "等待资料", Source: "user",
	}, root.Version); err != nil {
		t.Fatal(err)
	}
	dispatch, err := store.Dispatches().Get(ctx, worker.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Status != domain.DispatchDegraded || dispatch.ClosedAt == nil {
		t.Fatalf("user block must close existing dispatch degraded: %+v", dispatch)
	}
	if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, worker.ID, "迟到结果"); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err = store.Dispatches().Get(ctx, worker.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || dispatch.Status != domain.DispatchDegraded {
		t.Fatalf("late Worker terminal must not revive blocked control line: state=%+v dispatch=%+v", state, dispatch)
	}
}

func TestLateWorkerTerminalCannotClearNewCoordinatorRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "旧 Worker 不覆盖新统筹", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	planText := "```plan\n" +
		`[{"verb":"dispatch","agent_id":"` + workerID + `","title":"A","instruction":"执行 A"},` +
		`{"verb":"dispatch","agent_id":"` + workerID + `","title":"B","instruction":"执行 B"},` +
		`{"verb":"join","children":"all"}]` + "\n```"
	if err := svc.RecordRunEvent(ctx, source.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": planText}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, source.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	lateWorker := dispatcher.runs[2]
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorQueued
	state.CurrentRunID = ""
	state.CurrentAction = "recover"
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	newCoordinator := dispatcher.runs[len(dispatcher.runs)-1]
	if newCoordinator.ID == lateWorker.ID || !isSystemCoordinatorRunForTest(newCoordinator) {
		t.Fatalf("expected a new system Coordinator Run: %+v", newCoordinator)
	}
	if err := svc.RecordRunStatus(ctx, lateWorker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, lateWorker.ID, "迟到 Worker 结果"); err != nil {
		t.Fatal(err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentRunID != newCoordinator.ID || state.Status != domain.CoordinatorRunning {
		t.Fatalf("late Worker terminal must preserve new Coordinator ownership: %+v", state)
	}
}

func isSystemCoordinatorRunForTest(run *domain.ExecutionRun) bool {
	if run == nil {
		return false
	}
	control, _ := run.Input["task_coordinator"].(map[string]any)
	role, _ := control["role"].(string)
	return role == "coordinator"
}
