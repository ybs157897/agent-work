package application_test

// Run Journal M2「决策因果」的集成验收：非治理域决策统一落 run.decision
// internal 事件（只进 run_events，不进 SSE/回放）：
//   - self_heal_retry：maybeSelfHeal 触发 fresh 自愈时落在失败的旧 run 上，
//     link_run_id 指向新 run；被抑制的自愈不落（保持沉默的大多数）；
//   - cancel_forward：ControlRun 的 ControlForwarder 统一前转点落锚，
//     inputs.action 区分 cancel/interrupt；
//   - coordinator_redrive：due-state 恢复循环重驱非受管 Worker 线
//     （retry_worker checkpoint 只由 Worker 终态写入）；治理 Coordinator 的
//     恢复（recover）决策留痕在 turn_receipt，全程零 run.decision。

import (
	"context"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// runDecisionEvents 取单个 run 的 run.decision 事件（按 run_seq 正序）。
func runDecisionEvents(t *testing.T, ctx context.Context, store *sqlstore.Store, runID string) []application.RunEvent {
	t.Helper()
	events, err := store.Events().ListRunEventsIncludeInternal(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []application.RunEvent
	for _, ev := range events {
		if ev.EventType == domain.EventRunDecision {
			decisions = append(decisions, ev)
		}
	}
	return decisions
}

// assertNoDecisions 钉死「治理路径零 run.decision」：给定 run 全部不应有决策锚点。
func assertNoDecisions(t *testing.T, ctx context.Context, store *sqlstore.Store, runIDs ...string) {
	t.Helper()
	for _, runID := range runIDs {
		if got := runDecisionEvents(t, ctx, store, runID); len(got) != 0 {
			t.Fatalf("run %s 应零 run.decision，实际 %d 条（kind=%v）", runID, len(got), got[0].Payload["kind"])
		}
	}
}

// TestDecisionSelfHealRetryAnchorsSourceRunAndLinksHealRun session_unknown 失败
// 触发自愈：run.decision{self_heal_retry} 落在失败的旧 run 上，inputs 带失败证据
// （failure_family/code、session_anchor_ref），link_run_id 指向新 run；新 run 自身
// 不写任何决策（反向关系只靠 link 字段）。
func TestDecisionSelfHealRetryAnchorsSourceRunAndLinksHealRun(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "自愈决策锚点", RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "建立 provider 会话"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, first.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunSessionRef(ctx, first.ID, "mock://decision-heal"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunRunning, domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, first.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	second, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "续接 provider 会话"})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, second.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunStatus(ctx, second.ID, domain.RunFailed, map[string]any{
		"family": string(atwruntime.FamilySessionUnknown), "code": "session_not_found",
		"message": "provider session missing", "retryable": true,
	}); err != nil {
		t.Fatal(err)
	}
	var heal *domain.ExecutionRun
	for _, dispatched := range dispatcher.runs {
		if got, _ := dispatched.Input["auto_heal_of"].(string); got == second.ID {
			heal = dispatched
			break
		}
	}
	if heal == nil {
		t.Fatalf("前置条件：session_unknown 失败应触发自愈 fresh run：dispatcher 共 %d 次", len(dispatcher.runs))
	}
	decisions := runDecisionEvents(t, ctx, store, second.ID)
	if len(decisions) != 1 {
		t.Fatalf("源 run 应恰好 1 条 run.decision，实际 %d 条", len(decisions))
	}
	payload := decisions[0].Payload
	if got := journalPayloadString(t, payload, "kind"); got != "self_heal_retry" {
		t.Fatalf("decision.kind = %q，应为 self_heal_retry", got)
	}
	if got := journalPayloadString(t, payload, "link_run_id"); got != heal.ID {
		t.Fatalf("decision.link_run_id = %q，应为自愈 run %s", got, heal.ID)
	}
	if got := journalPayloadString(t, payload, "failure_family"); got != string(atwruntime.FamilySessionUnknown) {
		t.Fatalf("decision.failure_family = %q，应为 session_unknown", got)
	}
	if got := journalPayloadString(t, payload, "failure_code"); got != "session_not_found" {
		t.Fatalf("decision.failure_code = %q，应为 session_not_found", got)
	}
	if got := journalPayloadString(t, payload, "session_anchor_ref"); got != "mock://decision-heal" {
		t.Fatalf("decision.session_anchor_ref = %q，应为失效的 resume 锚点", got)
	}
	if reason := journalPayloadString(t, payload, "reason"); !strings.Contains(reason, "session_unknown") {
		t.Fatalf("decision.reason 应携带失败摘要：%q", reason)
	}
	// 新 run 不写任何决策：跨 run 反向关系只靠 link 字段。
	assertNoDecisions(t, ctx, store, heal.ID)
}

// TestDecisionCancelForwardAtControlForwarder 取消/中断意图经 ControlRun 的
// ControlForwarder 统一前转点前转时落 cancel_forward 决策，inputs.action 区分
// 两种动作；直达终态（starting → cancelled）与经中间态（running → interrupting）
// 两条前转路径都要落。
func TestDecisionCancelForwardAtControlForwarder(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	forwarded := []string{}
	svc.ControlForwarder = func(_ context.Context, runID, action string) {
		forwarded = append(forwarded, runID+":"+action)
	}
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "取消前转决策"})
	if err != nil {
		t.Fatal(err)
	}
	cancelRun, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "待取消任务"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, cancelRun)
	if _, err := svc.ControlRun(ctx, cancelRun.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	decisions := runDecisionEvents(t, ctx, store, cancelRun.ID)
	if len(decisions) != 1 {
		t.Fatalf("cancel 前转应恰好 1 条 run.decision，实际 %d 条", len(decisions))
	}
	payload := decisions[0].Payload
	if got := journalPayloadString(t, payload, "kind"); got != "cancel_forward" {
		t.Fatalf("decision.kind = %q，应为 cancel_forward", got)
	}
	if got := journalPayloadString(t, payload, "action"); got != "cancel" {
		t.Fatalf("decision.action = %q，应为 cancel", got)
	}
	if _, ok := payload["link_run_id"]; ok {
		t.Fatalf("cancel_forward 不跨 run，不应有 link_run_id：%#v", payload)
	}

	interruptRun, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "待中断任务"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, interruptRun)
	if err := svc.RecordRunStatus(ctx, interruptRun.ID, domain.RunRunning, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ControlRun(ctx, interruptRun.ID, "interrupt"); err != nil {
		t.Fatal(err)
	}
	interruptDecisions := runDecisionEvents(t, ctx, store, interruptRun.ID)
	if len(interruptDecisions) != 1 {
		t.Fatalf("interrupt 前转应恰好 1 条 run.decision，实际 %d 条", len(interruptDecisions))
	}
	if got := journalPayloadString(t, interruptDecisions[0].Payload, "action"); got != "interrupt" {
		t.Fatalf("decision.action = %q，应为 interrupt", got)
	}
	if len(forwarded) != 2 || forwarded[0] != cancelRun.ID+":cancel" || forwarded[1] != interruptRun.ID+":interrupt" {
		t.Fatalf("ControlForwarder 前转记录异常：%#v", forwarded)
	}
}

// TestDecisionCoordinatorRedriveCoversNongovernedWorkerOnly 重驱决策的治理/
// 非治理分界：retry_worker checkpoint（Worker 终态写入）被 due-state 循环重驱时
// 落 coordinator_redrive（源 run 上，link 指向重试 run）；治理 Coordinator 的
// recover 恢复走 turn_receipt，全程零 run.decision。
func TestDecisionCoordinatorRedriveCoversNongovernedWorkerOnly(t *testing.T) {
	t.Run("worker_redrive_records_decision", func(t *testing.T) {
		ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
		root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
			Title: "Worker 重驱决策", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
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
		planText := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch worker","next_action":"wait for worker","steps":[{"verb":"dispatch","agent_id":"` + workerID + `","title":"实现","instruction":"执行","acceptance":["实现结果通过验证"]},{"verb":"join","children":"all"}]}`
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
			"code": "transport_stream", "message": "stream disconnected", "retryable": true,
		}); err != nil {
			t.Fatal(err)
		}
		state, err := store.TaskCoordinators().GetState(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status != domain.CoordinatorWaitingRetry {
			t.Fatalf("前置条件：Worker 可重试失败应进入 waiting_retry：%+v", state)
		}
		forceCoordinatorDue(t, ctx, svc, store, root.ID)
		if len(dispatcher.runs) != 3 || dispatcher.runs[2].RetryOf != worker.ID {
			t.Fatalf("前置条件：due 循环应重驱出 retry run：%#v", dispatcher.runs)
		}
		retry := dispatcher.runs[2]
		decisions := runDecisionEvents(t, ctx, store, worker.ID)
		if len(decisions) != 1 {
			t.Fatalf("被重驱的源 run 应恰好 1 条 run.decision，实际 %d 条", len(decisions))
		}
		payload := decisions[0].Payload
		if got := journalPayloadString(t, payload, "kind"); got != "coordinator_redrive" {
			t.Fatalf("decision.kind = %q，应为 coordinator_redrive", got)
		}
		if got := journalPayloadString(t, payload, "link_run_id"); got != retry.ID {
			t.Fatalf("decision.link_run_id = %q，应为重试 run %s", got, retry.ID)
		}
		if got := journalPayloadString(t, payload, "coordinator_id"); got != state.ID {
			t.Fatalf("decision.coordinator_id = %q，应为 %s", got, state.ID)
		}
		if got := journalPayloadString(t, payload, "due_state"); !strings.Contains(got, "retry_worker") {
			t.Fatalf("decision.due_state 应携带触发因（retry_worker）：%q", got)
		}
	})
	t.Run("governed_coordinator_recovery_has_zero_decisions", func(t *testing.T) {
		ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
		root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
			Title: "治理恢复零决策", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
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
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, domain.RunFailed, map[string]any{
			"code": "model_timeout", "message": "coordinator turn timed out", "retryable": true,
		}); err != nil {
			t.Fatal(err)
		}
		state, err := store.TaskCoordinators().GetState(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		controlAction, _ := state.Data["control_action"].(string)
		if state.Status != domain.CoordinatorWaitingRetry || controlAction != "recover" {
			t.Fatalf("前置条件：治理 Coordinator 可重试失败应进入 recover checkpoint：%+v", state)
		}
		forceCoordinatorDue(t, ctx, svc, store, root.ID)
		if len(dispatcher.runs) != 2 {
			t.Fatalf("前置条件：due 循环应重驱出新的治理 Coordinator run：%#v", dispatcher.runs)
		}
		recovery := dispatcher.runs[1]
		contextData, _ := recovery.Input["task_coordinator"].(map[string]any)
		if contextData["role"] != "coordinator" || contextData["action"] != "recover" {
			t.Fatalf("重驱产物应是治理 recover run：%#v", contextData)
		}
		// 治理恢复的决策留痕在 turn_receipt phase1：run 维度必须零 run.decision。
		assertNoDecisions(t, ctx, store, dispatcher.runs[0].ID, recovery.ID)
	})
}
