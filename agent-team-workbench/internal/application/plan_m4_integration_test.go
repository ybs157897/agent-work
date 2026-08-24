// plan_m4_integration_test.go M4 编排层集成测试（设计 note
// notes/implemented/orchestration/2026-08-24-m4-claim-join-guardrails.md §2-4）：
// join 等待集收窄、defer≡join{children:"all"} 等价、审批护栏（挂起/放行/拒绝）、
// 预算护栏（max_dispatch 整单拒绝 / max_tokens 静默核算收口）。
// 复用 plan_integration_test 的 openTestDB / seedPlanEnv / captureDispatcher /
// noopNotifier / finishRun 基建。
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

// manualAgent 建一个 approval_policy=manual 的 dispatch 目标 agent。
func manualAgent(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID, id string) string {
	t.Helper()
	now := time.Now().UTC()
	a := &domain.AgentProfile{
		ID: id, WorkspaceID: wsID, Name: "Manual", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Policy:  domain.AgentPolicy{ApprovalPolicy: domain.ApprovalPolicyManual},
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

// automationWakes 返回 (agent, task_key) 上已入队的 automation 唤醒数。
func automationWakes(t *testing.T, ctx context.Context, store *sqlstore.Store, agentID, taskKey string) int {
	t.Helper()
	wakes, err := store.Wakeups().RecentByAgentTask(ctx, agentID, taskKey, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range wakes {
		if w.Source == domain.WakeupSourceAutomation {
			n++
		}
	}
	return n
}

// finishQueuedRun 把 plan dispatch 建出的 queued run 推到 succeeded 终态
// （触发 maybeAdvancePlans 等终态钩子；captureDispatcher 不推进 run 状态，
// 先走合法 starting 再由 finishRun 收终态——同 plan_integration_test 惯例）。
func finishQueuedRun(t *testing.T, ctx context.Context, svc *application.Service, runID string) {
	t.Helper()
	if err := svc.RecordRunStatus(ctx, runID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, runID, ""); err != nil {
		t.Fatal(err)
	}
}

// pendingPlanApprovalID 从事件流找 plan 的挂起 plan_dispatch 审批 id：
// approvals 表只有按 run_id 的查询，plan_dispatch 审批 run_id 为 NULL 查不到；
// 权威发现面是 approval.requested 事件（聚合 id 即审批 id，data 带 plan_id）。
func pendingPlanApprovalID(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID, planID string) string {
	t.Helper()
	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Type != domain.EventApprovalRequested {
			continue
		}
		if id, _ := e.Data["plan_id"].(string); id == planID {
			return e.Aggregate.ID
		}
	}
	t.Fatalf("未找到 plan %s 的 plan_dispatch 审批事件", planID)
	return ""
}

// dispatchTwoChildren plan1：dispatch A、dispatch B 后 defer 挂起，返回两个子任务
// id 与各自 run id（runs 停在 queued 非终态 = 活跃）。
func dispatchTwoChildren(t *testing.T, ctx context.Context, svc *application.Service, wsID, leadID, workerID, mainID string) (childA, childB, runA, runB string) {
	t.Helper()
	plan1, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: mainID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "子任务A", "实现 A"),
			dispatchStep(workerID, "子任务B", "实现 B"),
			deferStep(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan1.Status != domain.PlanWaiting {
		t.Fatalf("plan1 应 waiting，实际 %s", plan1.Status)
	}
	return plan1.Steps[0].ResultWorkItemID, plan1.Steps[1].ResultWorkItemID,
		plan1.Steps[0].ResultRunID, plan1.Steps[1].ResultRunID
}

// TestSubmitPlanJoinNarrowsQuietWake 验收：join{children:[A]} —— A 静默（终态）即
// 唤醒，B 仍活跃不等（旧 defer 语义需全部子任务静默）。
func TestSubmitPlanJoinNarrowsQuietWake(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	childA, _, runA, runB := dispatchTwoChildren(t, ctx, svc, wsID, leadID, workerID, main.ID)

	// plan2：join 只等 A（A、B 此刻均有活跃 run，防死等校验通过）。
	plan2, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			{Verb: "join", Payload: map[string]any{"children": []any{childA}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Status != domain.PlanWaiting {
		t.Fatalf("plan2 应 waiting，实际 %s", plan2.Status)
	}

	// A 终态、B 仍活跃 → 等待集已静默 → automation 唤醒入队
	//（B 活跃不阻塞即收窄语义本身；全静默语义的反证见 JoinAll 等价测试）。
	finishQueuedRun(t, ctx, svc, runA)
	if n := automationWakes(t, ctx, store, leadID, "plan:"+plan2.ID); n != 1 {
		t.Fatalf("join 等待集静默后应入队 1 个 automation 唤醒，实际 %d（B 活跃不应阻塞）", n)
	}
	_ = runB
}

// TestSubmitPlanJoinAllEquivalentToDefer 验收：defer ≡ join{children:"all"} ——
// 等待集与唤醒触发点完全一致（任一子任务活跃不唤醒，全部静默才唤醒）。
func TestSubmitPlanJoinAllEquivalentToDefer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []application.PlanStepInput
	}{
		{"defer", []application.PlanStepInput{deferStep()}},
		{"join_all", []application.PlanStepInput{
			{Verb: "join", Payload: map[string]any{"children": "all"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t)
			defer db.Close()
			store := sqlstore.New(db, sqlstore.SQLiteDialect())
			svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

			wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
			main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
			if err != nil {
				t.Fatal(err)
			}
			_, _, runA, runB := dispatchTwoChildren(t, ctx, svc, wsID, leadID, workerID, main.ID)

			plan2, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
				WorkItemID: main.ID, AgentProfileID: leadID, Steps: tc.steps,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan2.Status != domain.PlanWaiting {
				t.Fatalf("应 waiting，实际 %s", plan2.Status)
			}
			// A 静默、B 活跃 → 全静默语义下不唤醒。
			finishQueuedRun(t, ctx, svc, runA)
			if n := automationWakes(t, ctx, store, leadID, "plan:"+plan2.ID); n != 0 {
				t.Fatalf("B 仍活跃不应唤醒，实际 %d", n)
			}
			// B 也静默 → 唤醒。
			finishQueuedRun(t, ctx, svc, runB)
			if n := automationWakes(t, ctx, store, leadID, "plan:"+plan2.ID); n != 1 {
				t.Fatalf("全部静默后应唤醒 1 次，实际 %d", n)
			}
		})
	}
}

// TestSubmitPlanJoinTargetMustBeChild 验收：join 目标非本 plan 主任务的子任务 → 400。
func TestSubmitPlanJoinTargetMustBeChild(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, _ := seedPlanEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "无关任务"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			{Verb: "join", Payload: map[string]any{"children": []any{other.ID}}},
		},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("join 目标非子任务应 ErrValidation，实际 %v", err)
	}
	if plans, _ := store.Plans().LatestByWorkItem(ctx, main.ID); plans != nil {
		t.Fatal("校验失败的 plan 不应落库")
	}
}

// TestPlanApprovalGuardrailManualDispatch 验收：dispatch 到 manual 审批 agent →
// plan waiting + ApprovalRequest(kind=plan_dispatch、RunID 空、无子任务)；批准 →
// 该步执行、批次继续至 finished；拒绝 → step failed + plan failed。
func TestPlanApprovalGuardrailManualDispatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, _ := seedPlanEnv(t, ctx, store)
	manualID := manualAgent(t, ctx, store, wsID, "agent_manual")
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			dispatchStep(manualID, "需审批的子任务", "敏感操作"),
			{Verb: "finish", Payload: map[string]any{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanWaiting {
		t.Fatalf("manual dispatch 应挂起 waiting，实际 %s", plan.Status)
	}
	// 挂起即建审批：kind=plan_dispatch、不挂 run、步骤 pending、无子任务。
	approvalID := pendingPlanApprovalID(t, ctx, store, wsID, plan.ID)
	a, err := store.Runs().GetApproval(ctx, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != domain.ApprovalKindPlanDispatch || a.Status != domain.ApprovalPending {
		t.Fatalf("审批 kind/status 异常: %s/%s", a.Kind, a.Status)
	}
	if a.RunID != "" {
		t.Fatalf("plan_dispatch 审批不应挂 run，实际 %s", a.RunID)
	}
	if id, _ := a.RequestedBy["id"].(string); id != plan.ID {
		t.Fatalf("RequestedBy.id 应为 %s，实际 %v", plan.ID, a.RequestedBy["id"])
	}
	if seq, ok := a.RequestedBy["seq"].(float64); !ok || int(seq) != 0 {
		t.Fatalf("RequestedBy.seq 应为 0，实际 %v", a.RequestedBy["seq"])
	}
	if st := plan.Step(0); st.Status != domain.PlanStepPending {
		t.Fatalf("挂起步骤应 pending，实际 %s", st.Status)
	}
	if children, _ := store.WorkItems().ListByParent(ctx, main.ID); len(children) != 0 {
		t.Fatalf("审批前不应建子任务，实际 %d 个", len(children))
	}

	// 批准 → 步骤执行（子任务 + run 落库）、批次继续 → finish → finished。
	if _, err := svc.ResolveApproval(ctx, approvalID, true, "user_op", "", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != domain.PlanFinished {
		t.Fatalf("批准后续跑应 finished，实际 %s", fresh.Status)
	}
	children, err := store.WorkItems().ListByParent(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].AgentProfileID != manualID {
		t.Fatalf("批准后应建 1 个 manual agent 子任务，实际 %+v", children)
	}
	if st := fresh.Step(0); st.Status != domain.PlanStepExecuted || st.ResultWorkItemID != children[0].ID {
		t.Fatalf("批准后步骤应 executed 并记录产物，实际 status=%s result=%s", st.Status, st.ResultWorkItemID)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("批准续跑应分派 1 个 run，实际 %d", len(dispatcher.runs))
	}

	// 拒绝路径：再提一份 manual plan，拒绝 → step failed + plan failed、无子任务增量。
	plan2, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{dispatchStep(manualID, "再审批", "再次敏感操作")},
	})
	if err != nil {
		t.Fatal(err)
	}
	approvalID = pendingPlanApprovalID(t, ctx, store, wsID, plan2.ID)
	if _, err := svc.ResolveApproval(ctx, approvalID, false, "user_op", "路线否决", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	fresh2, err := store.Plans().Get(ctx, plan2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh2.Status != domain.PlanFailed {
		t.Fatalf("拒绝后 plan 应 failed，实际 %s", fresh2.Status)
	}
	if st := fresh2.Step(0); st.Status != domain.PlanStepFailed || st.Error == "" {
		t.Fatalf("拒绝后步骤应 failed 且带原因，实际 status=%s error=%q", st.Status, st.Error)
	}
	if children, _ = store.WorkItems().ListByParent(ctx, main.ID); len(children) != 1 {
		t.Fatalf("拒绝后不应新增子任务，实际 %d 个", len(children))
	}
}

// TestSubmitPlanMaxDispatchRejectsWholePlan 验收：max_dispatch=1 提交 2 个
// dispatch 步骤 → 整单 400（不部分执行），plan 与子任务均不落库。
func TestSubmitPlanMaxDispatchRejectsWholePlan(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	maxDispatch := 1
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Guardrails: domain.PlanGuardrails{MaxDispatch: &maxDispatch},
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "子任务A", "实现 A"),
			dispatchStep(workerID, "子任务B", "实现 B"),
		},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("超出 max_dispatch 应整单 ErrValidation，实际 %v", err)
	}
	if p, _ := store.Plans().LatestByWorkItem(ctx, main.ID); p != nil {
		t.Fatal("被拒 plan 不应落库")
	}
	if children, _ := store.WorkItems().ListByParent(ctx, main.ID); len(children) != 0 {
		t.Fatalf("被拒 plan 不应建子任务，实际 %d 个", len(children))
	}
}

// TestPlanBudgetTokensExceededAtQuietWake 验收：max_tokens 在子任务静默唤醒点
// 核算——主任务树 run 用量合计超限 → plan failed（error=budget_exceeded）+
// 主任务 blocker，且不唤醒 owner。
func TestPlanBudgetTokensExceededAtQuietWake(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, runA, runB := dispatchTwoChildren(t, ctx, svc, wsID, leadID, workerID, main.ID)

	// 树内用量注入：A 的 run 记 UsageIn=900、UsageOut=200（合计 1100 > 1000）。
	run, err := store.Runs().Get(ctx, runA)
	if err != nil {
		t.Fatal(err)
	}
	expected := run.Version
	run.UsageIn, run.UsageOut = 900, 200
	if err := store.Runs().Update(ctx, run, expected); err != nil {
		t.Fatal(err)
	}

	maxTokens := int64(1000)
	plan2, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Guardrails: domain.PlanGuardrails{MaxTokens: &maxTokens},
		Steps:      []application.PlanStepInput{deferStep()},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 两个子任务全部静默 → 核算超限 → plan failed + blocker，不唤醒。
	finishQueuedRun(t, ctx, svc, runA)
	finishQueuedRun(t, ctx, svc, runB)
	fresh, err := store.Plans().Get(ctx, plan2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != domain.PlanFailed || fresh.Error != domain.PlanErrorBudgetExceeded {
		t.Fatalf("预算超限应 plan failed(budget_exceeded)，实际 status=%s error=%q", fresh.Status, fresh.Error)
	}
	mainFresh, err := store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mainFresh.Status != domain.WorkItemBlocked {
		t.Fatalf("预算超限主任务应 blocked，实际 %s", mainFresh.Status)
	}
	blocker, err := store.WorkItems().ActiveBlocker(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocker == nil || blocker.Code != domain.PlanErrorBudgetExceeded {
		t.Fatalf("应有 budget_exceeded blocker，实际 %+v", blocker)
	}
	if n := automationWakes(t, ctx, store, leadID, "plan:"+plan2.ID); n != 0 {
		t.Fatalf("预算超限不应唤醒 owner，实际 %d 次", n)
	}
}
