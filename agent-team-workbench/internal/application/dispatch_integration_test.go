package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// seedDispatchSvcEnv 会话元模型 S1 路由测试环境：workspace + Alice（developer）
// + Lead（lead）+ 主任务。
func seedDispatchSvcEnv(t *testing.T, ctx context.Context, svc *application.Service, store *sqlstore.Store) (wsID, aliceID, leadID, wiID string) {
	t.Helper()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_dsp", Name: "dsp", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_dsp")
	alice := &domain.AgentProfile{
		ID: "agent_alice", WorkspaceID: ws.ID, Name: "Alice", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	lead := &domain.AgentProfile{
		ID: "agent_lead", WorkspaceID: ws.ID, Name: "Lead", Role: "lead",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Agents().Create(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := store.Agents().Create(ctx, lead); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, ws.ID, application.CreateWorkItemParams{Title: "派发目标", AgentProfileID: lead.ID})
	if err != nil {
		t.Fatal(err)
	}
	return ws.ID, alice.ID, lead.ID, wi.ID
}

// dispatchesOf 读取任务的批次列表（失败即 Fatal）。
func dispatchesOf(t *testing.T, ctx context.Context, store *sqlstore.Store, wiID string) []*domain.Dispatch {
	t.Helper()
	list, err := store.Dispatches().ListByWorkItem(ctx, wiID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// TestDispatchUserMessageRouting 防回归：用户消息入口的 @直达（大小写不敏感、
// instruction 原文保留、批次无接诊 run）与未命中/无 @ 回退接诊（lead_run_id 记
// 本次 run），且每条消息独立成批、dispatch.created 走真实事件流。
func TestDispatchUserMessageRouting(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wsID, aliceID, leadID, wiID := seedDispatchSvcEnv(t, ctx, svc, store)

	// @命中（大小写不敏感）→ 直达 Alice；指令原文不改写；批次无接诊 run。
	atRun, err := svc.CreateRun(ctx, wiID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "@alice 帮我看看这个报错",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if atRun.AgentProfileID != aliceID {
		t.Fatalf("@直达应把 run 归给 Alice，实际 %q", atRun.AgentProfileID)
	}
	if instr, _ := atRun.Input["instruction"].(string); instr != "@alice 帮我看看这个报错" {
		t.Fatalf("@直达 instruction 应保留原文: %q", instr)
	}
	list := dispatchesOf(t, ctx, store, wiID)
	if len(list) != 1 {
		t.Fatalf("首条消息应成 1 批，实际 %d", len(list))
	}
	atD := list[0]
	if atD.Trigger != domain.DispatchTriggerUserMessage || atD.LeadRunID != "" {
		t.Fatalf("@直达批次形状异常: %+v", atD)
	}
	if atRun.DispatchID != atD.ID {
		t.Fatalf("@直达 run 未挂批次: run=%s dispatch=%s", atRun.DispatchID, atD.ID)
	}

	// 无 @ → 接诊：沿用既有 assignee，批次 lead_run_id 记本次 run。
	plain, err := svc.CreateRun(ctx, wiID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "普通追问",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.AgentProfileID != leadID {
		t.Fatalf("接诊应保持既有 assignee，实际 %q", plain.AgentProfileID)
	}
	list = dispatchesOf(t, ctx, store, wiID)
	if len(list) != 2 || list[1].LeadRunID != plain.ID || plain.DispatchID != list[1].ID {
		t.Fatalf("接诊批次应独立成批且 lead_run_id 记本次 run: %#v", list)
	}

	// @未命中 → 回退接诊。
	miss, err := svc.CreateRun(ctx, wiID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "@nobody 在吗",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if miss.AgentProfileID != leadID {
		t.Fatalf("@未命中应回退接诊，实际 %q", miss.AgentProfileID)
	}
	list = dispatchesOf(t, ctx, store, wiID)
	if len(list) != 3 || list[2].LeadRunID != miss.ID {
		t.Fatalf("@未命中应回退接诊批次: %#v", list)
	}

	// dispatch.created 走真实事件流（白名单 + 载荷形状）。
	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, ev := range events {
		if ev.Type != domain.EventDispatchCreated {
			continue
		}
		created++
		if ev.Aggregate.Type != domain.AggregateDispatch || ev.Data["work_item_id"] != wiID {
			t.Fatalf("dispatch.created 信封异常: %+v", ev)
		}
		if ev.Data["trigger"] != string(domain.DispatchTriggerUserMessage) {
			t.Fatalf("dispatch.created trigger 异常: %+v", ev.Data)
		}
	}
	if created != 3 {
		t.Fatalf("应发布 3 条 dispatch.created，实际 %d", created)
	}
}

// TestPlanDispatchChildrenInheritDispatch 防回归：plan dispatch verb 派生的子 run
// 继承 source run 的批次——成员（lead run + 子 run）同批，可经 ListByDispatch 成组。
func TestPlanDispatchChildrenInheritDispatch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wsID, aliceID, leadID, wiID := seedDispatchSvcEnv(t, ctx, svc, store)

	leadRun, err := svc.CreateRun(ctx, wiID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "拆解一下",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	list := dispatchesOf(t, ctx, store, wiID)
	if len(list) != 1 {
		t.Fatalf("lead run 应已成批: %d", len(list))
	}
	batchID := list[0].ID

	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: wiID, AgentProfileID: leadID, SourceRunID: leadRun.ID,
		Steps: []application.PlanStepInput{{
			Verb:    "dispatch",
			Payload: map[string]any{"agent_id": aliceID, "title": "子任务", "instruction": "干活"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != domain.PlanFinished {
		t.Fatalf("单 dispatch plan 应执行完，实际 %s", plan.Status)
	}
	childWI := plan.Steps[0].ResultWorkItemID
	if childWI == "" {
		t.Fatal("dispatch 步骤应记录子任务 id")
	}
	children, err := store.Runs().ListByWorkItem(ctx, childWI)
	if err != nil || len(children) != 1 {
		t.Fatalf("子任务应有 1 个 run: %v %#v", err, children)
	}
	if children[0].DispatchID != batchID {
		t.Fatalf("子 run 应继承 lead run 批次: got %q want %q", children[0].DispatchID, batchID)
	}
	members, err := store.Runs().ListByDispatch(ctx, batchID)
	if err != nil || len(members) != 2 {
		t.Fatalf("批次成员应为 lead run + 子 run: %v %#v", err, members)
	}
	// 继承路径不新建批次。
	if list = dispatchesOf(t, ctx, store, wiID); len(list) != 1 {
		t.Fatalf("继承路径不得新建批次: %d", len(list))
	}
}

// TestPlanDispatchLeadPlanFallback 防回归：无 source run 的手动 plan 落
// trigger=lead_plan 兜底批次，子 run 挂批；后续手动 plan 独立成批（不共享）。
func TestPlanDispatchLeadPlanFallback(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wsID, aliceID, leadID, wiID := seedDispatchSvcEnv(t, ctx, svc, store)

	submit := func() *domain.Plan {
		plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
			WorkItemID: wiID, AgentProfileID: leadID,
			Steps: []application.PlanStepInput{{
				Verb:    "dispatch",
				Payload: map[string]any{"agent_id": aliceID, "title": "子任务", "instruction": "干活"},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	first := submit()
	firstChild, err := store.Runs().ListByWorkItem(ctx, first.Steps[0].ResultWorkItemID)
	if err != nil || len(firstChild) != 1 {
		t.Fatalf("第一批子 run 缺失: %v %#v", err, firstChild)
	}
	list := dispatchesOf(t, ctx, store, wiID)
	if len(list) != 1 || list[0].Trigger != domain.DispatchTriggerLeadPlan {
		t.Fatalf("手动 plan 应落 lead_plan 批次: %#v", list)
	}
	if firstChild[0].DispatchID != list[0].ID || list[0].LeadRunID != "" {
		t.Fatalf("lead_plan 批次形状异常: run=%s dispatch=%+v", firstChild[0].DispatchID, list[0])
	}

	// 第二份手动 plan：独立成批，不与第一批共享。
	second := submit()
	secondChild, err := store.Runs().ListByWorkItem(ctx, second.Steps[0].ResultWorkItemID)
	if err != nil || len(secondChild) != 1 {
		t.Fatalf("第二批子 run 缺失: %v %#v", err, secondChild)
	}
	list = dispatchesOf(t, ctx, store, wiID)
	if len(list) != 2 {
		t.Fatalf("两次手动 plan 应各自成批: %d", len(list))
	}
	if firstChild[0].DispatchID == secondChild[0].DispatchID {
		t.Fatal("两批子 run 不得共享批次")
	}
}
