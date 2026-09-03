package application_test

// Run Journal post 相位（终态钩子管线）埋点的集成验收：
//   - 8 个终态钩子各有配对 run.phase_entered/run.phase_closed{phase:post}，
//     run_seq 递增、顺序即 RecordRunStatus 管线契约；
//   - phase 事件是 internal 类：surface 查询（ListRunEvents）不出现——回放不污染；
//   - 钩子失败只进 closed{failed, post_hook}，run 终态与后续钩子不受影响
//     （尽力而为语义不变）；
//   - maybeSelfHeal 决策点：entered 带 failure 家族/code 输入证据，closed 带
//     触发与否与 heal_run_id。

import (
	"context"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// postHookPipelineOrder 是 RecordRunStatus 终态钩子管线的顺序契约
// （canonical 先于 Coordinator 决策，quota sweep 在其后）。
var postHookPipelineOrder = []string{
	"maybeAdvancePlans",
	"maybeProcessVerdict",
	"maybeExtractPlan",
	"maybeSummarizeSegment",
	"maybeCanonicalizeRunUsage",
	"maybeAdvanceTaskCoordinator",
	"maybeSettleGovernanceTurnQuota",
	"maybeSettleDispatch",
}

// journalPhaseEvents 取单个 run 的 phase 事件（按 run_seq 正序）。
func journalPhaseEvents(t *testing.T, ctx context.Context, store *sqlstore.Store, runID string) []application.RunEvent {
	t.Helper()
	events, err := store.Events().ListRunEventsIncludeInternal(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	var phase []application.RunEvent
	for _, ev := range events {
		if ev.EventType == domain.EventRunPhaseEntered || ev.EventType == domain.EventRunPhaseClosed {
			phase = append(phase, ev)
		}
	}
	return phase
}

func journalPayloadString(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	v, ok := payload[key].(string)
	if !ok {
		t.Fatalf("payload[%q] 应为 string，实际 %#v", key, payload[key])
	}
	return v
}

func journalPayloadBool(t *testing.T, payload map[string]any, key string) bool {
	t.Helper()
	v, ok := payload[key].(bool)
	if !ok {
		t.Fatalf("payload[%q] 应为 bool，实际 %#v", key, payload[key])
	}
	return v
}

// assertPostHookPairs 校验 phase 事件序列恰好是管线契约顺序的 entered/closed
// 交错配对，并顺带断言 run_seq 严格递增（事件溯源的可定位性根基）。
func assertPostHookPairs(t *testing.T, phase []application.RunEvent) {
	t.Helper()
	if len(phase) != 2*len(postHookPipelineOrder) {
		t.Fatalf("phase 事件数 = %d，应为 %d（8 钩子 × entered/closed）", len(phase), 2*len(postHookPipelineOrder))
	}
	for i, hook := range postHookPipelineOrder {
		entered, closed := phase[2*i], phase[2*i+1]
		if entered.EventType != domain.EventRunPhaseEntered || closed.EventType != domain.EventRunPhaseClosed {
			t.Fatalf("钩子 %s 位置事件类型错误：entered=%s closed=%s", hook, entered.EventType, closed.EventType)
		}
		if got := journalPayloadString(t, entered.Payload, "hook"); got != hook {
			t.Fatalf("entered[%d].hook = %q，应为 %s", i, got, hook)
		}
		if got := journalPayloadString(t, closed.Payload, "hook"); got != hook {
			t.Fatalf("closed[%d].hook = %q，应为 %s", i, got, hook)
		}
		for _, ev := range []*application.RunEvent{&entered, &closed} {
			if got := journalPayloadString(t, ev.Payload, "phase"); got != "post" {
				t.Fatalf("%s phase = %q，应为 post", ev.EventType, got)
			}
		}
		if got, ok := entered.Payload["attempt"].(float64); !ok || got != 1 {
			t.Fatalf("entered[%d].attempt = %#v，应为 1", i, entered.Payload["attempt"])
		}
		if entered.RunSeq >= closed.RunSeq {
			t.Fatalf("钩子 %s 的 entered.run_seq %d 应小于 closed.run_seq %d", hook, entered.RunSeq, closed.RunSeq)
		}
		if i > 0 && phase[2*i-1].RunSeq >= entered.RunSeq {
			t.Fatalf("钩子 %s 的 closed.run_seq %d 应小于下一钩子 entered.run_seq %d（顺序契约）",
				postHookPipelineOrder[i-1], phase[2*i-1].RunSeq, entered.RunSeq)
		}
	}
}

// assertSurfaceReplayClean 钉死「internal 事件不进 surface 回放」。
func assertSurfaceReplayClean(t *testing.T, ctx context.Context, store *sqlstore.Store, runID string) {
	t.Helper()
	surface, err := store.Events().ListRunEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	sawCompleted := false
	for _, ev := range surface {
		if domain.IsInternalEventName(ev.EventType) {
			t.Fatalf("surface 回放不得含 internal 事件：run_seq=%d type=%s", ev.RunSeq, ev.EventType)
		}
		if ev.EventType == domain.EventRunCompleted {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Fatalf("surface 回放缺少 run.completed，查询面疑似为空：%d 个事件", len(surface))
	}
}

func assertRunStatus(t *testing.T, ctx context.Context, store *sqlstore.Store, runID string, want domain.RunStatus) {
	t.Helper()
	run, err := store.Runs().Get(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != want {
		t.Fatalf("run %s 状态 = %s，应为 %s", runID, run.Status, want)
	}
}

// TestPostHookJournalPairsAllEightHooks 正常终态：8 个钩子各有配对
// entered/closed，run_seq 递增、顺序即契约；surface 回放零 internal 事件。
func TestPostHookJournalPairsAllEightHooks(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, _, workerID := seedM2Env(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "post 相位配对"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "直接完成"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, run)
	if err := finishRun(ctx, svc, run.ID, "完成，无 plan 块"); err != nil {
		t.Fatal(err)
	}

	assertPostHookPairs(t, journalPhaseEvents(t, ctx, store, run.ID))
	// 全部钩子正常跑完：closed 恒 ok 且带 duration_ms；acted 字段在
	//（值随钩子能力自报：no-op 为 false，摘要重算等实际动作可为 true）。
	phase := journalPhaseEvents(t, ctx, store, run.ID)
	for i := 0; i < len(phase); i += 2 {
		closed := phase[i+1]
		if got := journalPayloadString(t, closed.Payload, "outcome"); got != "ok" {
			t.Fatalf("closed[%s].outcome = %q，应为 ok", closed.Payload["hook"], got)
		}
		if _, ok := closed.Payload["duration_ms"].(float64); !ok {
			t.Fatalf("closed[%s].duration_ms 缺失：%#v", closed.Payload["hook"], closed.Payload)
		}
		journalPayloadBool(t, closed.Payload, "acted")
	}
	assertSurfaceReplayClean(t, ctx, store, run.ID)
	assertRunStatus(t, ctx, store, run.ID, domain.RunSucceeded)
}

// TestPostHookJournalRecordsHookFailureWithoutBlockingRun 注入 run 事件读取
// 失败（failRunEventsRepo 先例）→ maybeExtractPlan 的证据读取失败必须以
// closed{post, failed, post_hook} 留痕；同时 run 终态与后续钩子不受影响
// （尽力而为语义不变），surface 回放仍不污染。
func TestPostHookJournalRecordsHookFailureWithoutBlockingRun(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	base := sqlstore.New(db)
	events := &failRunEventsRepo{EventRepo: base.Events()}
	store := &failRunEventsStore{Store: base, events: events}
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, _ := seedM2Env(t, ctx, base)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "钩子失败留痕"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "产出计划"})
	if err != nil {
		t.Fatal(err)
	}
	events.target = run.ID
	startRun(t, ctx, svc, run)
	if err := finishRun(ctx, svc, run.ID, "正文暂时不可读"); err != nil {
		t.Fatal(err)
	}

	var extractEntered, extractClosed application.RunEvent
	extractFound := false
	closedByHook := map[string]int{}
	phase := journalPhaseEvents(t, ctx, base, run.ID)
	for i := 0; i+1 < len(phase); i += 2 {
		entered, closed := phase[i], phase[i+1]
		hook := journalPayloadString(t, closed.Payload, "hook")
		closedByHook[hook]++
		if hook == "maybeExtractPlan" {
			extractEntered, extractClosed, extractFound = entered, closed, true
		}
	}
	if !extractFound {
		t.Fatalf("maybeExtractPlan 缺少配对事件：%d 个 phase 事件", len(phase))
	}
	if got := journalPayloadString(t, extractClosed.Payload, "outcome"); got != "failed" {
		t.Fatalf("maybeExtractPlan closed.outcome = %q，应为 failed", got)
	}
	failure, ok := extractClosed.Payload["failure"].(map[string]any)
	if !ok || failure["code"] != "post_hook" {
		t.Fatalf("maybeExtractPlan closed.failure 缺失或 code 不为 post_hook：%#v", extractClosed.Payload["failure"])
	}
	if msg, _ := failure["message"].(string); !strings.Contains(msg, "injected run event read failure") {
		t.Fatalf("failure.message 应携带注入失败摘要：%q", msg)
	}
	if enteredHook := journalPayloadString(t, extractEntered.Payload, "hook"); enteredHook != "maybeExtractPlan" {
		t.Fatalf("failed 钩子的 entered.hook = %q，应为 maybeExtractPlan", enteredHook)
	}
	// 尽力而为：8 个钩子全部有 closed（失败钩子不截断管线），且派发收口在
	// 失败钩子之后照常执行。
	for _, hook := range postHookPipelineOrder {
		if closedByHook[hook] != 1 {
			t.Fatalf("钩子 %s closed 次数 = %d，应为 1", hook, closedByHook[hook])
		}
	}
	if extractClosed.RunSeq >= phase[len(phase)-1].RunSeq {
		t.Fatalf("maybeExtractPlan 之后应仍有后续钩子 closed（extractClosed.run_seq=%d）", extractClosed.RunSeq)
	}
	// 尽力而为语义不变：run 终态照常落 succeeded，surface 回放不污染。
	assertRunStatus(t, ctx, base, run.ID, domain.RunSucceeded)
	assertSurfaceReplayClean(t, ctx, base, run.ID)
}

// TestPostHookJournalSelfHealDecisionEvidence session_unknown 失败触发自愈：
// entered 携带 failure 家族/code 输入证据，closed 携带 acted=true 与
// heal_run_id；自愈产物是 fresh 重试 run（input.auto_heal_of 指向源 run）。
// fixture 用 seedCoordinatorEnv：agent 显式偏好 mock binding，run 创建即有
// AdapterID（self-heal 门控与 resume 锚点的前提）。
func TestPostHookJournalSelfHealDecisionEvidence(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "自愈证据", RecordKind: domain.RecordKindTask,
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
	if err := svc.RecordRunSessionRef(ctx, first.ID, "mock://journal-heal"); err != nil {
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
	conversation, _ := second.Input["conversation"].(map[string]any)
	if conversation["resume_session_ref"] != "mock://journal-heal" {
		t.Fatalf("前置条件：second run 应携带 resume 锚点：%#v", conversation)
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
	// 每个 run 推进到 starting 都会过一次 dispatcher；自愈产物用
	// input.auto_heal_of 定位，且必须恰好一个。
	var heal *domain.ExecutionRun
	for _, dispatched := range dispatcher.runs {
		if got, _ := dispatched.Input["auto_heal_of"].(string); got == second.ID {
			if heal != nil {
				t.Fatalf("自愈 fresh run 被重复派发：%s 与 %s", heal.ID, dispatched.ID)
			}
			heal = dispatched
		}
	}
	if heal == nil {
		t.Fatalf("session_unknown 失败未触发自愈 fresh run：dispatcher 共 %d 次", len(dispatcher.runs))
	}
	if got, _ := heal.Input["auto_heal_of"].(string); got != second.ID {
		t.Fatalf("自愈 run.input.auto_heal_of = %q，应为源 run %s", got, second.ID)
	}

	var healEntered, healClosed *application.RunEvent
	phase := journalPhaseEvents(t, ctx, store, second.ID)
	for i := 0; i+1 < len(phase); i += 2 {
		if journalPayloadString(t, phase[i].Payload, "hook") == "maybeSelfHeal" {
			healEntered, healClosed = &phase[i], &phase[i+1]
			break
		}
	}
	if healEntered == nil || healClosed == nil {
		t.Fatalf("maybeSelfHeal 缺少配对事件：%d 个 phase 事件", len(phase))
	}
	if got := journalPayloadString(t, healEntered.Payload, "failure_family"); got != string(atwruntime.FamilySessionUnknown) {
		t.Fatalf("entered.failure_family = %q，应为 session_unknown（输入证据）", got)
	}
	if got := journalPayloadString(t, healEntered.Payload, "failure_code"); got != "session_not_found" {
		t.Fatalf("entered.failure_code = %q，应为 session_not_found（输入证据）", got)
	}
	if got := journalPayloadString(t, healClosed.Payload, "outcome"); got != "ok" {
		t.Fatalf("closed.outcome = %q，应为 ok", got)
	}
	if !journalPayloadBool(t, healClosed.Payload, "acted") {
		t.Fatalf("自愈触发后 closed.acted 应为 true：%#v", healClosed.Payload)
	}
	if got := journalPayloadString(t, healClosed.Payload, "heal_run_id"); got != heal.ID {
		t.Fatalf("closed.heal_run_id = %q，应为自愈 run %s", got, heal.ID)
	}
	assertRunStatus(t, ctx, store, second.ID, domain.RunFailed)
}
