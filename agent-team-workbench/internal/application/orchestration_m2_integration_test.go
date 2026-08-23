// orchestration_m2_integration_test.go M2 编排层集成测试：plan 提取、评估
// run+verdict、session.decision 事件、task_sessions 锚点父子链（设计 note
// notes/implemented/orchestration/2026-08-23-m2-lead-planner-evaluation.md 验收矩阵）。
// 复用 runs_integration_test 的 openTestDB / captureDispatcher / noopNotifier /
// finishRun 与 plan_integration_test 的 dispatchStep 基建。
package application_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// seedM2Env M2 测试环境：lead（plan owner，role=lead）+ worker 双 agent，
// codex_local binding（resume supported）供会话链路测试。
func seedM2Env(t *testing.T, ctx context.Context, store *sqlstore.Store) (wsID, leadID, workerID string) {
	t.Helper()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_m2", Name: "m2", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	lead := &domain.AgentProfile{
		ID: "agent_m2_lead", WorkspaceID: ws.ID, Name: "Lead", Role: application.AgentRoleLead,
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}
	worker := &domain.AgentProfile{
		ID: "agent_m2_worker", WorkspaceID: ws.ID, Name: "Worker", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	for _, a := range []*domain.AgentProfile{lead, worker} {
		if err := store.Agents().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	binding := &domain.RuntimeBinding{
		ID: "rb_m2", WorkspaceID: ws.ID, RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{"multi_turn": "supported", "resume": "supported"},
		Status:       domain.BindingReady, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Bindings().Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	return ws.ID, lead.ID, worker.ID
}

// startRun 推进 run 到 starting（finishRun 的 running 迁移前置），并返回 run。
func startRun(t *testing.T, ctx context.Context, svc *application.Service, run *domain.ExecutionRun) *domain.ExecutionRun {
	t.Helper()
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestExtractPlanFromLeadRun 验收 1：role=lead 的 run succeeded 且最终文本含
// ```plan 围栏块 → 子任务+run 落库、plan.source_run_id=该 run；同 run 二次
// 终态事件不重复提取（终态不可逆 + source_run_id 唯一索引双兜底）。
func TestExtractPlanFromLeadRun(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "规划一下"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, run)
	text := "我来规划，先派一个子任务。\n```plan\n" +
		`[{"verb":"dispatch","agent_id":"` + workerID + `","title":"子任务A","instruction":"实现 A","acceptance":["完成"]}]` +
		"\n```"
	if err := finishRun(ctx, svc, run.ID, text); err != nil {
		t.Fatal(err)
	}

	plan, err := svc.LatestPlanForWorkItem(ctx, main.ID)
	if err != nil {
		t.Fatalf("plan 未落库: %v", err)
	}
	if plan.SourceRunID != run.ID {
		t.Fatalf("plan.source_run_id = %q，应为提取来源 run %s", plan.SourceRunID, run.ID)
	}
	if plan.AgentProfileID != leadID {
		t.Fatalf("plan owner = %q，应为 %s", plan.AgentProfileID, leadID)
	}
	children, err := store.WorkItems().ListByParent(ctx, main.ID)
	if err != nil || len(children) != 1 {
		t.Fatalf("子任务数 = %d（err %v），应为 1", len(children), err)
	}
	if children[0].AgentProfileID != workerID {
		t.Fatalf("子任务 assignee = %q，应为 %s", children[0].AgentProfileID, workerID)
	}
	stored, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Steps) != 1 || stored.Steps[0].ResultRunID == "" || stored.Steps[0].ResultWorkItemID != children[0].ID {
		t.Fatalf("plan_steps 审计行异常: %#v", stored.Steps)
	}
	if _, err := store.Runs().Get(ctx, stored.Steps[0].ResultRunID); err != nil {
		t.Fatalf("子任务 run 未落库: %v", err)
	}

	// 幂等：同 run 二次终态事件被状态机拒绝，不重复提取。
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunSucceeded, nil); err == nil {
		t.Fatal("终态 run 二次迁移应被状态机拒绝")
	}
	if children, err := store.WorkItems().ListByParent(ctx, main.ID); err != nil || len(children) != 1 {
		t.Fatalf("二次终态事件后子任务数 = %d（err %v），不应重复提取", len(children), err)
	}
}

// TestExtractPlanInvalidJSONBlocks 验收 2：非法 plan JSON → 主任务 blocked +
// blocker code=plan_parse_failed（不静默，人可见可修）。
func TestExtractPlanInvalidJSONBlocks(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, _ := seedM2Env(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "规划"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, run)
	if err := finishRun(ctx, svc, run.ID, "坏计划。\n```plan\n{\"steps\": 不是JSON\n```"); err != nil {
		t.Fatal(err)
	}
	wi, err := store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Status != domain.WorkItemBlocked {
		t.Fatalf("主任务应 blocked，实际 %s", wi.Status)
	}
	blocker, err := store.WorkItems().ActiveBlocker(ctx, main.ID)
	if err != nil || blocker == nil {
		t.Fatalf("blocker 未落库: %v %#v", err, blocker)
	}
	if blocker.Code != "plan_parse_failed" || blocker.Message == "" {
		t.Fatalf("blocker 异常: %#v", blocker)
	}
	if _, err := store.Plans().ActiveByWorkItem(ctx, main.ID); err != nil || planCount(t, ctx, store, main.ID) != 0 {
		t.Fatalf("解析失败的 plan 不应落库: %v", err)
	}
}

func planCount(t *testing.T, ctx context.Context, store *sqlstore.Store, workItemID string) int {
	t.Helper()
	plan, err := store.Plans().LatestByWorkItem(ctx, workItemID)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil {
		return 0
	}
	return 1
}

// TestExtractPlanIgnoresNonLead 验收 3：非 lead（role=developer）含 plan 块 →
// 无 plan 产生、无 blocker。
func TestExtractPlanIgnoresNonLead(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, _, workerID := seedM2Env(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "干活"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, run)
	text := "```plan\n" + `[{"verb":"finish","summary":"x"}]` + "\n```"
	if err := finishRun(ctx, svc, run.ID, text); err != nil {
		t.Fatal(err)
	}
	if planCount(t, ctx, store, main.ID) != 0 {
		t.Fatal("非 lead agent 的 plan 块不应产生 plan")
	}
	wi, err := store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Status == domain.WorkItemBlocked {
		t.Fatal("非 lead 的 plan 块不应阻塞主任务")
	}
	if blocker, _ := store.WorkItems().ActiveBlocker(ctx, main.ID); blocker != nil {
		t.Fatalf("非 lead 不应落 blocker: %#v", blocker)
	}
}

// createEvaluationEnv 构造评估链路前置：主任务（带验收标准的既往 run）+
// plan（dispatch 子任务 + finish{evaluation:true}）→ 返回评估 run。
func createEvaluationEnv(t *testing.T, ctx context.Context, svc *application.Service,
	wsID, leadID, workerID string) (main *domain.WorkItem, evalRun *domain.ExecutionRun) {
	t.Helper()
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "主任务", Description: "M2 评估链路",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.CreateRun(ctx, main.ID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "分析",
		AcceptanceCriteria: []string{"功能可用", "测试通过"},
	})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, first)
	if err := finishRun(ctx, svc, first.ID, "分析完成"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "子任务A", "实现 A"),
			{Verb: "finish", Payload: map[string]any{"summary": "收尾", "evaluation": true}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	runs, err := svc.RunsByWorkItem(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if eval, _ := r.Input["evaluation"].(bool); eval {
			return main, r
		}
	}
	t.Fatal("评估 run 未创建")
	return nil, nil
}

// TestFinishEvaluationCreatesRun 验收 4：finish{evaluation:true} → plan finished
// 后自动建评估 run：Input.evaluation=true、instruction 含主任务验收标准与
// 子任务结果摘要、agent=plan owner。
func TestFinishEvaluationCreatesRun(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, evalRun := createEvaluationEnv(t, ctx, svc, wsID, leadID, workerID)

	if evalRun.AgentProfileID != leadID {
		t.Fatalf("评估 run agent = %q，应为 plan owner %s", evalRun.AgentProfileID, leadID)
	}
	if evalRun.WorkItemID != main.ID {
		t.Fatalf("评估 run 应建在主任务上，实际 %s", evalRun.WorkItemID)
	}
	if eval, _ := evalRun.Input["evaluation"].(bool); !eval {
		t.Fatalf("评估 run 应固化 input.evaluation=true: %#v", evalRun.Input)
	}
	instruction, _ := evalRun.Input["instruction"].(string)
	for _, want := range []string{"验收标准", "功能可用", "测试通过", "子任务结果", "子任务A", "```verdict"} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("评估 instruction 缺少 %q:\n%s", want, instruction)
		}
	}
	// plan 本身已 finished（评估是 finished 之后的自动动作）。
	plan, err := svc.LatestPlanForWorkItem(ctx, main.ID)
	if err != nil || plan.Status != domain.PlanFinished {
		t.Fatalf("plan 应 finished: %v %s", err, plan.Status)
	}
}

// TestVerdictPassEntersAcceptance 验收 5a：评估 run succeeded 含
// ```verdict {"pass":true} → 主任务 phase=acceptance（等人工 Accept）。
func TestVerdictPassEntersAcceptance(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, evalRun := createEvaluationEnv(t, ctx, svc, wsID, leadID, workerID)
	startRun(t, ctx, svc, evalRun)
	if err := finishRun(ctx, svc, evalRun.ID,
		"全部达标。\n```verdict\n"+`{"pass":true,"reasons":["全部达标"]}`+"\n```"); err != nil {
		t.Fatal(err)
	}
	wi, err := store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Status != domain.WorkItemInProgress || wi.Phase != domain.PhaseAcceptance {
		t.Fatalf("verdict pass 后应 in_progress+acceptance，实际 %s/%s", wi.Status, wi.Phase)
	}
	// M4 归因防回归：verdict_passed activity 行与 activity.appended 事件 data
	// 均携带 work_item_id。
	assertActivityAttribution(t, ctx, store, wsID, main.ID, "plan.verdict_passed")
	// 人工验收仍可走 Accept 唯一完工路径。
	accepted, err := svc.AcceptWorkItem(ctx, main.ID, wi.Version)
	if err != nil {
		t.Fatalf("acceptance 应允许验收: %v", err)
	}
	if accepted.Status != domain.WorkItemCompleted {
		t.Fatalf("验收后应 completed，实际 %s", accepted.Status)
	}
}

// TestVerdictFailReturnsToExecution 验收 5b：{"pass":false,"reasons":[...]} →
// 主任务 phase 回 execution + activity 记录 reasons。
func TestVerdictFailReturnsToExecution(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, evalRun := createEvaluationEnv(t, ctx, svc, wsID, leadID, workerID)
	startRun(t, ctx, svc, evalRun)
	if err := finishRun(ctx, svc, evalRun.ID,
		"未达标。\n```verdict\n"+`{"pass":false,"reasons":["子任务A 未达标","缺测试"]}`+"\n```"); err != nil {
		t.Fatal(err)
	}
	wi, err := store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Phase != domain.PhaseExecution || wi.Status != domain.WorkItemInProgress {
		t.Fatalf("verdict fail 后应回 execution，实际 %s/%s", wi.Status, wi.Phase)
	}
	activities, err := store.Events().ListActivities(ctx, wsID, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range activities {
		if a.Kind == "plan.verdict_rejected" {
			found = true
			if !strings.Contains(a.Message, "子任务A 未达标") || !strings.Contains(a.Message, "缺测试") {
				t.Fatalf("verdict reasons 未落 activity: %q", a.Message)
			}
		}
	}
	if !found {
		t.Fatalf("plan.verdict_rejected activity 未记录: %#v", activities)
	}
	// M4 归因防回归：activity 行与 activity.appended 事件 data 均携带 work_item_id。
	assertActivityAttribution(t, ctx, store, wsID, main.ID, "plan.verdict_rejected")
}

// TestVerdictMissingBlocks 验收 5c：评估回复无 verdict 块 → blocker
// code=verdict_parse_failed。
func TestVerdictMissingBlocks(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedM2Env(t, ctx, store)
	main, evalRun := createEvaluationEnv(t, ctx, svc, wsID, leadID, workerID)
	startRun(t, ctx, svc, evalRun)
	if err := finishRun(ctx, svc, evalRun.ID, "我觉得可以了"); err != nil {
		t.Fatal(err)
	}
	wi, err := store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Status != domain.WorkItemBlocked {
		t.Fatalf("缺 verdict 应 blocked，实际 %s", wi.Status)
	}
	blocker, err := store.WorkItems().ActiveBlocker(ctx, main.ID)
	if err != nil || blocker == nil || blocker.Code != "verdict_parse_failed" {
		t.Fatalf("verdict_parse_failed blocker 未落库: %v %#v", err, blocker)
	}
	// M4 归因防回归：blocker 落库的 work_item.blocked activity 同样归因。
	assertActivityAttribution(t, ctx, store, wsID, main.ID, "work_item.blocked")
}

// assertActivityAttribution M4 归因不变量：kind 的 activity 行携带
// work_item_id == expectWorkItemID，且对应 activity.appended 事件 data 的
// work_item_id 一致（行与事件同步归因）。
func assertActivityAttribution(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID, expectWorkItemID, kind string) {
	t.Helper()
	activities, err := store.Events().ListActivities(ctx, wsID, 50)
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, a := range activities {
		if a.Kind != kind {
			continue
		}
		rows++
		if a.WorkItemID != expectWorkItemID {
			t.Fatalf("%s activity 行归因 %q，应为 %q", kind, a.WorkItemID, expectWorkItemID)
		}
	}
	if rows == 0 {
		t.Fatalf("%s activity 未记录: %#v", kind, activities)
	}
	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	emitted := false
	for _, e := range events {
		if e.Type != domain.EventActivityCreated {
			continue
		}
		if k, _ := e.Data["kind"].(string); k != kind {
			continue
		}
		emitted = true
		if id, _ := e.Data["work_item_id"].(string); id != expectWorkItemID {
			t.Fatalf("%s activity.appended 事件 data.work_item_id = %q，应为 %q", kind, id, expectWorkItemID)
		}
	}
	if !emitted {
		t.Fatalf("%s 的 activity.appended 事件未发射", kind)
	}
}

// sessionDecisionEvent 取 run 的 session.decision 事件（每 run 至多一条）。
func sessionDecisionEvent(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID, runID string) map[string]any {
	t.Helper()
	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Type == domain.EventSessionDecision && ev.Aggregate.ID == runID {
			if ev.Aggregate.Type != domain.AggregateExecutionRun {
				t.Fatalf("session.decision aggregate.type = %q", ev.Aggregate.Type)
			}
			return ev.Data
		}
	}
	t.Fatalf("run %s 未发射 session.decision 事件", runID)
	return nil
}

// TestSessionDecisionEvent 验收 6：resume 命中 / 超预算轮换 / 全新会话三路径
// 各断言一条事件，tier/reason 正确。
func TestSessionDecisionEvent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, _ := seedM2Env(t, ctx, store)
	// dsh_local：无 resume 能力（预算轮换路径用）。
	now := time.Now().UTC()
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_m2_dsh", WorkspaceID: wsID, RuntimeLabel: "dsh_local", AdapterID: "dsh",
		Capabilities: map[string]string{"multi_turn": "supported"}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: "agent_m2_dsh", WorkspaceID: wsID, Name: "Dsh", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		RuntimePreference: domain.RuntimePreference{Preferred: "dsh_local"},
		Version:           1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// 路径 A（resume 命中）：codex_local 首轮上报会话后，第二轮续接。
	wiA, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "会话A"})
	if err != nil {
		t.Fatal(err)
	}
	firstA, err := svc.CreateRun(ctx, wiA.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, firstA)
	if err := svc.RecordRunSessionRef(ctx, firstA.ID, "codex://thread_m2"); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, firstA.ID, "第一轮回复"); err != nil {
		t.Fatal(err)
	}
	secondA, err := svc.CreateRun(ctx, wiA.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	data := sessionDecisionEvent(t, ctx, store, wsID, secondA.ID)
	if data["tier"] != "resume" || data["reason"] != "resume_hit" || data["session_ref"] != "codex://thread_m2" {
		t.Fatalf("resume 命中路径 session.decision 异常: %#v", data)
	}

	// 路径 B（超预算轮换）：dsh binding 无 resume，长历史内联超窗口预算 → 轮换。
	wiB, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "会话B"})
	if err != nil {
		t.Fatal(err)
	}
	firstB, err := svc.CreateRun(ctx, wiB.ID, application.CreateRunParams{
		AgentProfileID: "agent_m2_dsh", Instruction: "长历史第一轮",
	})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, firstB)
	longText := strings.Repeat("很长的历史内容。", 2200) // ~17600 CJK token > 32768*35% 预算
	if err := finishRun(ctx, svc, firstB.ID, longText); err != nil {
		t.Fatal(err)
	}
	secondB, err := svc.CreateRun(ctx, wiB.ID, application.CreateRunParams{
		AgentProfileID: "agent_m2_dsh", Instruction: "第二轮",
	})
	if err != nil {
		t.Fatal(err)
	}
	data = sessionDecisionEvent(t, ctx, store, wsID, secondB.ID)
	if data["tier"] != "rotation" || data["reason"] != "budget" {
		t.Fatalf("超预算轮换路径 session.decision 异常: %#v", data)
	}

	// 路径 C（全新会话）：无锚点任务的首个 run。
	wiC, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "会话C"})
	if err != nil {
		t.Fatal(err)
	}
	firstC, err := svc.CreateRun(ctx, wiC.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "首轮"})
	if err != nil {
		t.Fatal(err)
	}
	data = sessionDecisionEvent(t, ctx, store, wsID, firstC.ID)
	if data["tier"] != "inline" || data["reason"] != "fresh" {
		t.Fatalf("全新会话路径 session.decision 异常: %#v", data)
	}
}

// TestTaskSessionAnchorParent 验收 7：子任务锚点创建时 parent_anchor_id=父任务
// 同 agent+adapter 锚点 id；父无锚点（或父任务无 parent）则空。
func TestTaskSessionAnchorParent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, _ := seedM2Env(t, ctx, store)
	parent, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "父任务", AgentProfileID: leadID})
	if err != nil {
		t.Fatal(err)
	}
	parentRun, err := svc.CreateRun(ctx, parent.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "父任务首跑"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, parentRun)
	if err := svc.RecordRunSessionRef(ctx, parentRun.ID, "codex://parent_thread"); err != nil {
		t.Fatal(err)
	}
	parentAnchor, err := store.TaskSessions().Get(ctx, wsID, leadID, "codex-appserver", parent.ID)
	if err != nil || parentAnchor == nil {
		t.Fatalf("父锚点未落库: %v %#v", err, parentAnchor)
	}
	if parentAnchor.ParentAnchorID != "" {
		t.Fatalf("根任务锚点 parent_anchor_id 应为空，实际 %q", parentAnchor.ParentAnchorID)
	}

	mkChild := func(id string) *domain.WorkItem {
		now := time.Now().UTC()
		return &domain.WorkItem{
			ID: id, WorkspaceID: wsID, ParentID: parent.ID, Title: "子任务" + id,
			Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, AgentProfileID: leadID,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	// 同 agent+adapter 的子任务锚点 → 记父锚点 id。
	childSame := mkChild("wi_m2_child_same")
	if err := store.WorkItems().Create(ctx, childSame); err != nil {
		t.Fatal(err)
	}
	childRun, err := svc.CreateRun(ctx, childSame.ID, application.CreateRunParams{AgentProfileID: leadID, Instruction: "子任务"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, childRun)
	if err := svc.RecordRunSessionRef(ctx, childRun.ID, "codex://child_thread"); err != nil {
		t.Fatal(err)
	}
	childAnchor, err := store.TaskSessions().Get(ctx, wsID, leadID, "codex-appserver", childSame.ID)
	if err != nil || childAnchor == nil {
		t.Fatalf("子锚点未落库: %v %#v", err, childAnchor)
	}
	if childAnchor.ParentAnchorID != parentAnchor.ID {
		t.Fatalf("子锚点 parent_anchor_id = %q，应为父锚点 %s", childAnchor.ParentAnchorID, parentAnchor.ID)
	}

	// 父无锚点（换 agent，父任务同 adapter 锚点不存在）→ NULL。
	other, err := svc.CreateAgent(ctx, wsID, application.CreateAgentParams{
		Name: "Other", Role: "developer",
		RuntimePreference: domain.RuntimePreference{Preferred: "codex_local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	childOther := mkChild("wi_m2_child_other")
	childOther.AgentProfileID = other.ID
	if err := store.WorkItems().Create(ctx, childOther); err != nil {
		t.Fatal(err)
	}
	otherRun, err := svc.CreateRun(ctx, childOther.ID, application.CreateRunParams{AgentProfileID: other.ID, Instruction: "子任务"})
	if err != nil {
		t.Fatal(err)
	}
	startRun(t, ctx, svc, otherRun)
	if err := svc.RecordRunSessionRef(ctx, otherRun.ID, "codex://other_thread"); err != nil {
		t.Fatal(err)
	}
	otherAnchor, err := store.TaskSessions().Get(ctx, wsID, other.ID, "codex-appserver", childOther.ID)
	if err != nil || otherAnchor == nil {
		t.Fatalf("other 锚点未落库: %v %#v", err, otherAnchor)
	}
	if otherAnchor.ParentAnchorID != "" {
		t.Fatalf("父任务无同 agent 锚点时 parent_anchor_id 应为空，实际 %q", otherAnchor.ParentAnchorID)
	}
}
