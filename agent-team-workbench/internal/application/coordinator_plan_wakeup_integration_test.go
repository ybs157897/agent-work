package application_test

import (
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCoordinatedDispatchUsesSettlementWithoutChildrenQuietDoubleWake(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "单一汇总唤醒", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
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
		`[{"verb":"dispatch","agent_id":"` + workerID + `","title":"实现","instruction":"执行实现","acceptance":["完成"]},{"verb":"join","children":"all"}]` +
		"\n```"
	if err := svc.RecordRunEvent(ctx, coordinatorRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": planText}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	workerRun := dispatcher.runs[1]
	if err := svc.RecordRunStatus(ctx, workerRun.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, workerRun.ID, "worker final result"); err != nil {
		t.Fatal(err)
	}
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	settlementCount := 0
	for _, wakeup := range wakeups {
		if trigger, _ := wakeup.Context["trigger"].(string); trigger == "children_quiet" {
			t.Fatalf("coordinated dispatch 不得同时产生 children_quiet wake: %+v", wakeups)
		}
		if id, _ := wakeup.Context[domain.WakeupContextSettlementDispatchID].(string); id == workerRun.DispatchID {
			settlementCount++
		}
	}
	if settlementCount != 1 {
		t.Fatalf("coordinated dispatch 应只有一条 settlement wake，实际 %d: %+v", settlementCount, wakeups)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status == domain.CoordinatorWaitingUser {
		t.Fatalf("settlement Coordinator 尚未运行前不得提前等待验收: %+v", state)
	}
}
