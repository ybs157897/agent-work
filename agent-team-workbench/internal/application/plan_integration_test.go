// plan_integration_test.go M1 编排层集成测试：复用 runs_integration_test 的
// openTestDB / captureDispatcher / noopNotifier / finishRun 基建，
// 覆盖设计 note 的验收矩阵（dispatch/defer/finish、supersede、幂等、防死等）。
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
)

// seedPlanEnv 建 workspace + lead（plan owner）+ worker（dispatch 目标）双 agent。
func seedPlanEnv(t *testing.T, ctx context.Context, store *sqlstore.Store) (wsID, leadID, workerID string) {
	t.Helper()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_plan", Name: "plan", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	lead := &domain.AgentProfile{
		ID: "agent_lead", WorkspaceID: ws.ID, Name: "Lead", Role: "architect",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	worker := &domain.AgentProfile{
		ID: "agent_worker", WorkspaceID: ws.ID, Name: "Worker", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, lead); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(ctx, worker); err != nil {
		t.Fatal(err)
	}
	return ws.ID, lead.ID, worker.ID
}

func dispatchStep(agentID, title, instruction string) application.PlanStepInput {
	return application.PlanStepInput{Verb: "dispatch", Payload: map[string]any{
		"agent_id": agentID, "title": title, "instruction": instruction,
		"acceptance": []any{"子任务完成"}, "priority": "high",
	}}
}

func deferStep() application.PlanStepInput {
	return application.PlanStepInput{Verb: "defer", Payload: map[string]any{"reason": "等子任务"}}
}

// TestSubmitPlanDoubleDispatch 验收 1：两个 dispatch step → 两个子 work item
// （ParentID=主任务、assignee 正确）+ dispatcher 收到 2 个 run + plan_steps 行记录
// result ids + 无 defer 时顺序执行完即 finished。
func TestSubmitPlanDoubleDispatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "子任务A", "实现 A"),
			dispatchStep(workerID, "子任务B", "实现 B"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFinished {
		t.Fatalf("无 defer 时顺序执行完应 finished，实际 %s", plan.Status)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("dispatch 数 = %d，应为 2", len(dispatcher.runs))
	}
	children, err := store.WorkItems().ListByParent(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("子任务数 = %d，应为 2", len(children))
	}
	for _, c := range children {
		if c.ParentID != main.ID {
			t.Fatalf("子任务 %s ParentID = %q", c.ID, c.ParentID)
		}
		if c.AgentProfileID != workerID {
			t.Fatalf("子任务 %s assignee = %q，应为 %s", c.ID, c.AgentProfileID, workerID)
		}
		if c.Status != domain.WorkItemInProgress {
			t.Fatalf("run 创建后子任务应推进 in_progress，实际 %s", c.Status)
		}
	}
	// plan_steps 行级审计：result ids 指向真实子任务与 run。
	stored, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Steps) != 2 {
		t.Fatalf("plan_steps 行数 = %d", len(stored.Steps))
	}
	for _, st := range stored.Steps {
		if st.Status != domain.PlanStepExecuted {
			t.Fatalf("step %d status = %s", st.Seq, st.Status)
		}
		child, err := store.WorkItems().Get(ctx, st.ResultWorkItemID)
		if err != nil || child.ParentID != main.ID {
			t.Fatalf("step %d result_work_item_id 未指向主任务子项: %v", st.Seq, err)
		}
		run, err := store.Runs().Get(ctx, st.ResultRunID)
		if err != nil || run.WorkItemID != st.ResultWorkItemID {
			t.Fatalf("step %d result_run_id 不匹配: %v", st.Seq, err)
		}
	}
}

// TestSubmitPlanDeferWaitsUntilChildrenQuiet 验收 2：dispatch + defer（无 wake_at，
// 有活跃子任务）→ plan waiting、余下 steps skipped；子任务 run 终态后 children_quiet
// 钩子入 automation wakeup（source/task_key/agent 按 note 契约）。
func TestSubmitPlanDeferWaitsUntilChildrenQuiet(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "子任务A", "实现 A"),
			deferStep(),
			{Verb: "finish", Payload: map[string]any{"summary": "收尾"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanWaiting {
		t.Fatalf("defer 后 plan 应 waiting，实际 %s", plan.Status)
	}
	stored, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Steps[0].Status != domain.PlanStepExecuted || stored.Steps[1].Status != domain.PlanStepExecuted {
		t.Fatalf("前两步应 executed: %s / %s", stored.Steps[0].Status, stored.Steps[1].Status)
	}
	if stored.Steps[2].Status != domain.PlanStepSkipped {
		t.Fatalf("defer 之后的步骤应 skipped，实际 %s", stored.Steps[2].Status)
	}
	// 子任务 run 落终态 → children_quiet 唤醒 owner。
	// captureDispatcher 不推进 run（停在 queued）：先走合法 starting，再由 finishRun 收终态。
	childRun := stored.Steps[0].ResultRunID
	if err := svc.RecordRunStatus(ctx, childRun, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, childRun, "子任务完成"); err != nil {
		t.Fatal(err)
	}
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range wakeups {
		if w.TaskKey != "plan:"+plan.ID {
			continue
		}
		found = true
		if w.Source != domain.WakeupSourceAutomation {
			t.Fatalf("唤醒源 = %s，应为 automation", w.Source)
		}
		if w.AgentProfileID != leadID {
			t.Fatalf("唤醒 agent = %s，应为 plan owner %s", w.AgentProfileID, leadID)
		}
		if w.Context["plan_id"] != plan.ID || w.Context["trigger"] != "children_quiet" {
			t.Fatalf("唤醒 context 异常: %#v", w.Context)
		}
	}
	if !found {
		t.Fatalf("children_quiet 唤醒未入队: %#v", wakeups)
	}
	// 唤醒 ≠ plan 继续：plan 仍 waiting，等待 owner 提交新 plan（supersede 收口）。
	after, err := store.Plans().Get(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.PlanWaiting {
		t.Fatalf("children_quiet 不应推进 plan 状态，实际 %s", after.Status)
	}
}

// TestSubmitPlanDeferWithoutOutletRejected 验收 3：defer 既无 wake_at 又无子任务
// → 校验错误且 plan 不落库（整个提交事务回滚）。
func TestSubmitPlanDeferWithoutOutletRejected(t *testing.T) {
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
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{deferStep()},
	})
	if err == nil {
		t.Fatal("defer 无出口（无 wake_at、无子任务）应被拒绝")
	}
	if !strings.Contains(err.Error(), "defer") {
		t.Fatalf("错误应指明 defer 无出口: %v", err)
	}
	active, err := store.Plans().ActiveByWorkItem(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active != nil {
		t.Fatalf("被拒的 plan 不应落库: %#v", active)
	}
	if children, err := store.WorkItems().ListByParent(ctx, main.ID); err != nil || len(children) != 0 {
		t.Fatalf("被拒提交不应留下子任务: %v %#v", err, children)
	}
}

// TestSubmitPlanSupersedesWaitingPlan 验收 4：waiting plan 存在时同主任务提交新
// plan → 旧 plan finished 且 superseded_by=新 plan id。
func TestSubmitPlanSupersedesWaitingPlan(t *testing.T) {
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
	oldPlan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{dispatchStep(workerID, "子任务A", "实现 A"), deferStep()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if oldPlan.Status != domain.PlanWaiting {
		t.Fatalf("前置条件：旧 plan 应 waiting，实际 %s", oldPlan.Status)
	}
	newPlan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{{Verb: "finish", Payload: map[string]any{"summary": "新批次"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Plans().Get(ctx, oldPlan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.PlanFinished || stored.SupersededBy != newPlan.ID {
		t.Fatalf("旧 plan 应 finished+superseded_by=%s，实际 %s/%q", newPlan.ID, stored.Status, stored.SupersededBy)
	}
	if newPlan.Status != domain.PlanFinished {
		t.Fatalf("新 plan（finish step）应 finished，实际 %s", newPlan.Status)
	}
	// supersede 的 plan.finished 事件信封：aggregate 指旧 plan、data 带
	// work_item_id + superseded_by（前端 store 路由与预建索引依据）。
	events, err := store.Events().Since(ctx, wsID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	supersedeSeen := false
	for _, ev := range events {
		if ev.Type != domain.EventPlanFinished || ev.Aggregate.ID != oldPlan.ID {
			continue
		}
		supersedeSeen = true
		if ev.Aggregate.Type != domain.AggregatePlan || ev.Data["work_item_id"] != main.ID ||
			ev.Data["superseded_by"] != newPlan.ID {
			t.Fatalf("supersede finished 事件信封异常: %#v", ev)
		}
	}
	if !supersedeSeen {
		t.Fatal("旧 plan 的 finished（superseded_by）事件未发布")
	}
	// 唯一活跃约束：active plan 存在时提交必须拒绝。同步执行路径下 active 只在事务内
	// 瞬时存在，这里直接落一行 active plan 钉住约束语义（防未来异步执行路径破坏不变量）。
	now := time.Now().UTC()
	rawActive := &domain.Plan{
		ID: "plan_raw_active", WorkspaceID: wsID, WorkItemID: main.ID, AgentProfileID: leadID,
		Status: domain.PlanActive, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Plans().Create(ctx, rawActive); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{{Verb: "finish", Payload: map[string]any{}}},
	}); err == nil || !strings.Contains(err.Error(), "active plan") {
		t.Fatalf("active plan 存在时提交应被唯一活跃约束拒绝: %v", err)
	}
}

// TestSubmitPlanResubmitSafety 验收 5：application 层重入安全——(plan_id, seq)
// 唯一约束钉死同 plan 内步骤行不可重复；不同 plan 各自独立建步骤（不互相污染）。
func TestSubmitPlanResubmitSafety(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	dispatcher := &captureDispatcher{}
	svc := application.NewService(store, dispatcher, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	steps := []application.PlanStepInput{dispatchStep(workerID, "子任务A", "实现 A")}
	first, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID, Steps: steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 旧 plan 已 finished → 同载荷再提交合法，产出独立新 plan 与新子任务。
	second, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID, Steps: steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("重复提交应产出独立 plan（幂等键语义归 httpapi 层）")
	}
	if children, err := store.WorkItems().ListByParent(ctx, main.ID); err != nil || len(children) != 2 {
		t.Fatalf("两次提交各建一个子任务: %v %#v", err, children)
	}
	// 唯一约束钉子：同 (plan_id, seq) 重复插入步骤行必须被 DB 拒绝。
	now := time.Now().UTC()
	dup := &domain.Plan{
		ID: "plan_dup", WorkspaceID: wsID, WorkItemID: main.ID, AgentProfileID: leadID,
		Status: domain.PlanActive, Version: 1, CreatedAt: now, UpdatedAt: now,
		Steps: []domain.PlanStep{
			{PlanID: "plan_dup", Seq: 0, Verb: domain.PlanVerbFinish, Status: domain.PlanStepPending, CreatedAt: now},
			{PlanID: "plan_dup", Seq: 0, Verb: domain.PlanVerbFinish, Status: domain.PlanStepPending, CreatedAt: now},
		},
	}
	if err := store.Plans().Create(ctx, dup); err == nil {
		t.Fatal("重复 (plan_id, seq) 应被唯一约束拒绝")
	} else if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("唯一约束冲突应映射 ErrIdempotencyConflict，实际 %v", err)
	}
}

// TestSubmitPlanUnknownVerbRejected 验收 6：未知 verb → 400/校验错误，plan 不落库。
func TestSubmitPlanUnknownVerbRejected(t *testing.T) {
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
	_, err = svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{
			dispatchStep(workerID, "子任务A", "实现 A"),
			{Verb: "join", Payload: map[string]any{"agents": []any{leadID}}},
		},
	})
	if err == nil {
		t.Fatal("未知 verb 应被拒绝")
	}
	if !strings.Contains(err.Error(), "join") {
		t.Fatalf("错误应指明未知 verb: %v", err)
	}
	active, err := store.Plans().ActiveByWorkItem(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active != nil {
		t.Fatalf("未知 verb 的 plan 不应落库: %#v", active)
	}
}

// TestWorkItemTreeThreeLevels 验收 7：三级树返回先序全部节点
// （root → childA → grandchild → childB）。
func TestWorkItemTreeThreeLevels(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, _, _ := seedPlanEnv(t, ctx, store)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "根"})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id, parent, title string, at time.Time) *domain.WorkItem {
		return &domain.WorkItem{
			ID: id, WorkspaceID: wsID, ParentID: parent, Title: title,
			Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
			Version: 1, CreatedAt: at, UpdatedAt: at,
		}
	}
	base := time.Now().UTC().Add(-time.Hour)
	// created_at 顺序：childA < grandchild < childB → 先序 [root, childA, grandchild, childB]。
	for _, wi := range []*domain.WorkItem{
		mk("wi_childA", root.ID, "A", base),
		mk("wi_grand", "wi_childA", "A-1", base.Add(time.Minute)),
		mk("wi_childB", root.ID, "B", base.Add(2*time.Minute)),
	} {
		if err := store.WorkItems().Create(ctx, wi); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := svc.WorkItemTree(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{root.ID, "wi_childA", "wi_grand", "wi_childB"}
	if len(tree) != len(want) {
		t.Fatalf("树节点数 = %d（%v），应为 %d", len(tree), treeIDs(tree), len(want))
	}
	for i, id := range want {
		if tree[i].ID != id {
			t.Fatalf("先序第 %d 位 = %s，应为 %s（实际序 %v）", i, tree[i].ID, id, treeIDs(tree))
		}
	}
}

// TestSubmitPlanDeferWakeAt：defer 带 wake_at（有出口）合法挂起，且提交后入
// timer 型 automation wakeup（同 task_key，wake_at 按 payload 指定）。
func TestSubmitPlanDeferWakeAt(t *testing.T) {
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
	wakeAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{{
			Verb:    "defer",
			Payload: map[string]any{"reason": "定时复查", "wake_at": wakeAt.Format(time.RFC3339)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanWaiting {
		t.Fatalf("defer 带 wake_at 应 waiting，实际 %s", plan.Status)
	}
	wakeups, err := store.Wakeups().DueTimers(ctx, wakeAt.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range wakeups {
		if w.TaskKey != "plan:"+plan.ID {
			continue
		}
		found = true
		if w.Source != domain.WakeupSourceAutomation || w.AgentProfileID != leadID {
			t.Fatalf("wake_at 唤醒源/agent 异常: %s / %s", w.Source, w.AgentProfileID)
		}
		if w.Context["trigger"] != "defer_wake_at" {
			t.Fatalf("wake_at 唤醒 trigger 异常: %#v", w.Context)
		}
	}
	if !found {
		t.Fatalf("defer wake_at 未入队 automation wakeup: %#v", wakeups)
	}
}

// TestPlanEventEnvelope 事件信封契约钉死（前端 store 路由依据）：全部 plan.* 事件
// aggregate.type="plan"、aggregate id=plan id、data 携带 work_item_id。
// 场景 A（dispatch+defer）覆盖 submitted/step_executed/waiting；场景 B（双 dispatch
// 自然收尾）覆盖 finished。
func TestPlanEventEnvelope(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, workerID := seedPlanEnv(t, ctx, store)
	mainA, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务A"})
	if err != nil {
		t.Fatal(err)
	}
	planA, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: mainA.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{dispatchStep(workerID, "子A", "做 A"), deferStep()},
	})
	if err != nil {
		t.Fatal(err)
	}
	mainB, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务B"})
	if err != nil {
		t.Fatal(err)
	}
	planB, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: mainB.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{dispatchStep(workerID, "子B", "做 B")},
	})
	if err != nil {
		t.Fatal(err)
	}

	events, err := store.Events().Since(ctx, wsID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, ev := range events {
		if !strings.HasPrefix(ev.Type, "plan.") {
			continue
		}
		expectMain, ok := map[string]string{planA.ID: mainA.ID, planB.ID: mainB.ID}[ev.Aggregate.ID]
		if !ok {
			t.Fatalf("plan 事件 aggregate id=%s 不属于任一测试 plan", ev.Aggregate.ID)
		}
		if ev.Aggregate.Type != domain.AggregatePlan {
			t.Fatalf("%s aggregate.type=%q，应为 %q", ev.Type, ev.Aggregate.Type, domain.AggregatePlan)
		}
		if ev.Data["work_item_id"] != expectMain {
			t.Fatalf("%s data.work_item_id=%v（aggregate %s），应为 %s", ev.Type, ev.Data["work_item_id"], ev.Aggregate.ID, expectMain)
		}
		seen[ev.Type+"/"+ev.Aggregate.ID] = true
	}
	for _, miss := range []string{
		domain.EventPlanSubmitted + "/" + planA.ID,
		domain.EventPlanStepExecuted + "/" + planA.ID,
		domain.EventPlanWaiting + "/" + planA.ID,
		domain.EventPlanFinished + "/" + planB.ID,
	} {
		if !seen[miss] {
			t.Fatalf("事件 %s 未出现（出现集合: %v）", miss, seen)
		}
	}
}

// TestLatestPlanForWorkItem 任务详情页冷启动拉取：返回主任务最新一份 plan
// （不限状态、被 supersede 的旧 plan 不再返回）；无 plan 报 ErrNotFound。
func TestLatestPlanForWorkItem(t *testing.T) {
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
	// 无 plan → ErrNotFound。
	if _, err := svc.LatestPlanForWorkItem(ctx, main.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("无 plan 应报 ErrNotFound，实际 %v", err)
	}
	// plan A（dispatch+defer → waiting）被 plan B supersede 后，最新 = plan B。
	if _, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{dispatchStep(workerID, "子任务A", "实现 A"), deferStep()},
	}); err != nil {
		t.Fatal(err)
	}
	planB, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{{Verb: "finish", Payload: map[string]any{"summary": "收口"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := svc.LatestPlanForWorkItem(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != planB.ID || latest.Status != domain.PlanFinished {
		t.Fatalf("最新 plan 应为 %s（finished），实际 %s（%s）", planB.ID, latest.ID, latest.Status)
	}
}

func treeIDs(items []*domain.WorkItem) []string {
	ids := make([]string, 0, len(items))
	for _, wi := range items {
		ids = append(ids, wi.ID)
	}
	return ids
}
