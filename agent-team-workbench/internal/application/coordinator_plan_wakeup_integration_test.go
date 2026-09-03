package application_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

func TestCoordinatedDispatchUsesSettlementWithoutChildrenQuietDoubleWake(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "单一汇总唤醒", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
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
	planText := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch one worker","next_action":"wait for the worker","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"实现","instruction":"执行实现","acceptance":["完成"]},{"verb":"join","children":"all"}]}`
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

func TestSettlementCoordinatorRedispatchStartsFreshBatch(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	_, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "结算后修复", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"修复结果通过结算"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	completeCoordinatorPlanDecision(t, ctx, svc, source.ID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"first attempt","next_action":"wait","steps":[{"verb":"dispatch","agent_id":"`+workerID+`","title":"首轮","instruction":"执行首轮","acceptance":["完成"]},{"verb":"join","children":"all"}]}`)
	firstWorker := dispatcher.runs[1]
	firstDispatchID := firstWorker.DispatchID
	if err := svc.RecordRunStatus(ctx, firstWorker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, firstWorker.ID, "首轮未通过，需修复"); err != nil {
		t.Fatal(err)
	}
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	var settlement *domain.WakeupRequest
	for index := range wakeups {
		if id, _ := wakeups[index].Context[domain.WakeupContextSettlementDispatchID].(string); id == firstDispatchID {
			settlement = &wakeups[index]
			break
		}
	}
	if settlement == nil {
		t.Fatalf("首批 settlement wake 缺失: %+v", wakeups)
	}
	scheduler := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	if outcome, err := scheduler.ConsumeOne(ctx, *settlement, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeConsumed {
		t.Fatalf("首批 settlement 消费失败: outcome=%s err=%v", outcome, err)
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("首批 settlement 必须创建 Coordinator summary Run: %+v", dispatcher.runs)
	}
	summary := dispatcher.runs[2]
	completeCoordinatorPlanDecision(t, ctx, svc, summary.ID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"repair rejected result","next_action":"wait for repair","steps":[{"verb":"dispatch","agent_id":"`+workerID+`","title":"修复","instruction":"修复并复核","acceptance":["通过"]},{"verb":"join","children":"all"}]}`)
	if len(dispatcher.runs) != 4 {
		t.Fatalf("summary 再派发必须创建修复 Worker: %+v", dispatcher.runs)
	}
	repairWorker := dispatcher.runs[3]
	if repairWorker.DispatchID == "" || repairWorker.DispatchID == firstDispatchID {
		t.Fatalf("修复 Worker 不得复用 collecting 批: first=%s repair=%s", firstDispatchID, repairWorker.DispatchID)
	}
	firstDispatch, err := store.Dispatches().Get(ctx, firstDispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if firstDispatch.Status != domain.DispatchCompleted || firstDispatch.ClosedAt == nil {
		t.Fatalf("summary 终态后首批应独立收口: %+v", firstDispatch)
	}
	repairDispatch, err := store.Dispatches().Get(ctx, repairWorker.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if repairDispatch.Status != domain.DispatchRunning || repairDispatch.LeadRunID != summary.ID {
		t.Fatalf("修复 Worker 新批次形状异常: %+v", repairDispatch)
	}

	if err := svc.RecordRunStatus(ctx, repairWorker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, repairWorker.ID, "修复验收通过"); err != nil {
		t.Fatal(err)
	}
	wakeups, err = store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	settlementCount := 0
	for _, wakeup := range wakeups {
		if trigger, _ := wakeup.Context["trigger"].(string); trigger == "children_quiet" {
			t.Fatalf("修复结果必须只走新批 settlement，不能并发 children_quiet: %+v", wakeups)
		}
		if id, _ := wakeup.Context[domain.WakeupContextSettlementDispatchID].(string); id == repairWorker.DispatchID {
			settlementCount++
		}
	}
	if settlementCount != 1 {
		t.Fatalf("修复批应生成一条 settlement wake，实际 %d: %+v", settlementCount, wakeups)
	}
}

// TestJoinOnlyObservationPlanStillDefersToExistingDispatchSettlement 防回归：
// Coordinator 可能在 Worker 仍运行时用一轮 join-only Plan 取代原 dispatch Plan。
// Worker 终态仍必须由其 dispatch settlement 唤醒，不能因当前 Plan 不含 dispatch
// 步骤而额外生成 children_quiet。
func TestJoinOnlyObservationPlanStillDefersToExistingDispatchSettlement(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "观察既有派发", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"只收到一条结算续点"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	completeCoordinatorPlanDecision(t, ctx, svc, source.ID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch","next_action":"wait","steps":[{"verb":"dispatch","agent_id":"`+workerID+`","title":"执行","instruction":"执行","acceptance":["完成"]},{"verb":"join","children":"all"}]}`)
	worker := dispatcher.runs[1]
	firstPlan, err := store.Plans().LatestByWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	wakeContext := map[string]any{"plan_id": firstPlan.ID, "trigger": "defer_wake_at"}
	wakeRunID, err := svc.CreateRunForWakeup(ctx, wsID, state.CoordinatorAgentID,
		fmt.Sprintf("plan:%s", firstPlan.ID), "检查仍在途的 Worker", wakeContext)
	if err != nil {
		t.Fatal(err)
	}
	completeCoordinatorPlanDecision(t, ctx, svc, wakeRunID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"worker still active","next_action":"wait for existing dispatch","steps":[{"verb":"join","children":"all"}]}`)
	currentPlan, err := store.Plans().LatestByWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range currentPlan.Steps {
		if step.Verb == domain.PlanVerbDispatch {
			t.Fatalf("前置要求当前 Plan 不含 dispatch: %+v", currentPlan.Steps)
		}
	}
	if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, worker.ID, "existing worker done"); err != nil {
		t.Fatal(err)
	}
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	settlementCount := 0
	for _, wakeup := range wakeups {
		if trigger, _ := wakeup.Context["trigger"].(string); trigger == "children_quiet" {
			t.Fatalf("join-only observation Plan 不得抢占 dispatch settlement: %+v", wakeups)
		}
		if id, _ := wakeup.Context[domain.WakeupContextSettlementDispatchID].(string); id == worker.DispatchID {
			settlementCount++
		}
	}
	if settlementCount != 1 {
		t.Fatalf("existing dispatch 应仅生成一条 settlement wake，实际 %d: %+v", settlementCount, wakeups)
	}
}
