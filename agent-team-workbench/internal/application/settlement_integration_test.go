package application_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
	"github.com/ybs/agent-team-workbench/internal/scheduling"
)

// settleEnv S3 收口测试环境：接诊批（lead run + plan 派生的 Alice worker 子
// run 同批），返回批 id 与两个成员 run。lead 开启心跳（automation 源受心跳
// 门控，ConsumeOne 需 policy.Enabled）。
func settleEnv(t *testing.T, ctx context.Context, svc *application.Service, store *sqlstore.Store) (wsID, leadID, wiID, batchID, leadRunID, childRunID string) {
	t.Helper()
	wsID, aliceID, leadID, wiID := seedDispatchSvcEnv(t, ctx, svc, store)
	lead, err := store.Agents().Get(ctx, leadID)
	if err != nil {
		t.Fatal(err)
	}
	lead.HeartbeatEnabled = true
	if err := store.Agents().Update(ctx, lead, lead.Version); err != nil {
		t.Fatal(err)
	}
	_ = aliceID
	leadRun, err := svc.CreateRun(ctx, wiID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "拆解任务",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: wiID, AgentProfileID: leadID, SourceRunID: leadRun.ID,
		Steps: []application.PlanStepInput{{
			Verb:    "dispatch",
			Payload: map[string]any{"agent_id": aliceID, "title": "子任务", "instruction": "worker 干活"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	children, err := store.Runs().ListByWorkItem(ctx, plan.Steps[0].ResultWorkItemID)
	if err != nil || len(children) != 1 {
		t.Fatalf("worker 子 run 缺失: %v %#v", err, children)
	}
	dispatches, err := store.Dispatches().ListByWorkItem(ctx, wiID)
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("应只有 1 个批次: %v %#v", err, dispatches)
	}
	return wsID, leadID, wiID, dispatches[0].ID, leadRun.ID, children[0].ID
}

// settleDriveRun 驱动 run 到 succeeded 或 failed（终态触发收口钩子）。
func settleDriveRun(t *testing.T, ctx context.Context, svc *application.Service, runID, assistantText string, succeeded bool) {
	t.Helper()
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if succeeded {
		if err := svc.RecordRunEvent(ctx, runID, domain.EventMessageCompleted,
			map[string]any{"role": "assistant", "text": assistantText}); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordRunStatus(ctx, runID, domain.RunSucceeding, nil); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordRunStatus(ctx, runID, domain.RunSucceeded, nil); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := svc.RecordRunStatus(ctx, runID, domain.RunFailed,
		map[string]any{"code": "boom", "message": assistantText}); err != nil {
		t.Fatal(err)
	}
}

// dispatchOf 读取批（失败即 Fatal）。
func dispatchOf(t *testing.T, ctx context.Context, store *sqlstore.Store, id string) *domain.Dispatch {
	t.Helper()
	d, err := store.Dispatches().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// settlementWakeups 统计 lead 名下 automation 源唤醒条数（过滤 Tick 顺带
// 生产的 timer 唤醒噪声）。
func settlementWakeups(t *testing.T, ctx context.Context, store *sqlstore.Store, leadID, wiID string) []domain.WakeupRequest {
	t.Helper()
	list, err := store.Wakeups().RecentByAgentTask(ctx, leadID, wiID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.WakeupRequest
	for _, w := range list {
		if w.Source == domain.WakeupSourceAutomation {
			out = append(out, w)
		}
	}
	return out
}

// findSettlementRun 识别汇总 run（input.wakeup 带 settle 标记）。
func findSettlementRun(t *testing.T, runs []*domain.ExecutionRun, batchID string) *domain.ExecutionRun {
	t.Helper()
	for _, r := range runs {
		wake, _ := r.Input["wakeup"].(map[string]any)
		if id, _ := wake["settle_dispatch_id"].(string); id == batchID {
			return r
		}
	}
	t.Fatalf("未找到汇总 run（批 %s）: %#v", batchID, runs)
	return nil
}

// TestSettlementHappyPath 全成功链路防回归：成员未齐不动 → 全终态 collecting+
// 唤醒恰一次 → 调度消费产生挂批汇总 run → 汇总终态收口 completed 且不再唤醒。
func TestSettlementHappyPath(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wsID, leadID, wiID, batchID, leadRunID, childRunID := settleEnv(t, ctx, svc, store)

	// 成员未齐：lead 终态、worker 仍 queued → 不动。
	settleDriveRun(t, ctx, svc, leadRunID, "lead 干完", true)
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchRunning {
		t.Fatalf("成员未齐批不得迁移，实际 %s", d.Status)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 0 {
		t.Fatalf("成员未齐不得唤醒，实际 %d", n)
	}

	// worker 终态 → collecting + 唤醒恰一次（automation 源，带 settle 标记与成员摘要）。
	settleDriveRun(t, ctx, svc, childRunID, "worker 干完", true)
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCollecting {
		t.Fatalf("成员齐后应 collecting，实际 %s", d.Status)
	}
	wakeups := settlementWakeups(t, ctx, store, leadID, wiID)
	if len(wakeups) != 1 || wakeups[0].Source != domain.WakeupSourceAutomation {
		t.Fatalf("应恰好入队 1 条 automation 唤醒: %#v", wakeups)
	}
	if wakeups[0].Context["settle_dispatch_id"] != batchID {
		t.Fatalf("唤醒 context 缺 settle 标记: %#v", wakeups[0].Context)
	}
	if instr, _ := wakeups[0].Context["instruction"].(string); !strings.Contains(instr, "Alice：succeeded") {
		t.Fatalf("汇总材料应含成员结局行: %q", instr)
	}

	// 调度消费 → 汇总 run 挂批（dispatch_id=原批、settle 标记固化）。
	sched := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	sched.Tick(ctx, time.Now().UTC().Add(time.Minute))
	runs, err := store.Runs().ListByWorkItem(ctx, wiID)
	if err != nil {
		t.Fatal(err)
	}
	settlement := findSettlementRun(t, runs, batchID)
	if settlement.DispatchID != batchID {
		t.Fatalf("汇总 run 应挂回原批: %q != %q", settlement.DispatchID, batchID)
	}

	// 汇总 run 终态 → 收口 completed；不再唤醒（防循环）。
	settleDriveRun(t, ctx, svc, settlement.ID, "汇总结论", true)
	d := dispatchOf(t, ctx, store, batchID)
	if d.Status != domain.DispatchCompleted || d.ClosedAt == nil {
		t.Fatalf("全成功批应收口 completed: %+v", d)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 1 {
		t.Fatalf("收口不得再次唤醒，唤醒应恒为 1，实际 %d", n)
	}

	// dispatch.updated 两条：collecting（无 closed_at）与 completed（带 closed_at）。
	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	updated := 0
	for _, ev := range events {
		if ev.Type != domain.EventDispatchUpdated || ev.Aggregate.ID != batchID {
			continue
		}
		updated++
		if ev.Data["work_item_id"] != wiID {
			t.Fatalf("dispatch.updated 缺 work_item_id: %+v", ev.Data)
		}
	}
	if updated != 2 {
		t.Fatalf("应有 collecting+completed 两条 dispatch.updated，实际 %d", updated)
	}

	// 已终态批的迟到成员：no-op（不复活、不再汇总、不再唤醒）。
	late := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: wsID, WorkItemID: wiID,
		Status: domain.RunQueued, DispatchID: batchID,
		Input: map[string]any{"instruction": "迟到成员"}, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.Runs().Create(ctx, late); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, late.ID, domain.RunCancelled, nil); err != nil {
		t.Fatal(err)
	}
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCompleted {
		t.Fatalf("迟到成员不得改动已收口批次，实际 %s", d.Status)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 1 {
		t.Fatalf("迟到成员不得触发唤醒，实际 %d", n)
	}
}

// TestSettlementPartialFailureDegraded 防回归：worker 失败 → 收口 degraded
// （部分失败是常态路径，不是异常路径）。
func TestSettlementPartialFailureDegraded(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	_, _, wiID, batchID, leadRunID, childRunID := settleEnv(t, ctx, svc, store)

	settleDriveRun(t, ctx, svc, leadRunID, "lead 干完", true)
	settleDriveRun(t, ctx, svc, childRunID, "worker 崩了", false)
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCollecting {
		t.Fatalf("成员齐后应 collecting，实际 %s", d.Status)
	}
	sched := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	sched.Tick(ctx, time.Now().UTC().Add(time.Minute))
	runs, err := store.Runs().ListByWorkItem(ctx, wiID)
	if err != nil {
		t.Fatal(err)
	}
	settlement := findSettlementRun(t, runs, batchID)
	settleDriveRun(t, ctx, svc, settlement.ID, "汇总（含失败项）", true)
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchDegraded {
		t.Fatalf("有失败成员批应收口 degraded，实际 %s", d.Status)
	}
}

// TestSettlementDirectHitNoWake 防回归：@直达批（lead_run_id NULL）成员终态
// 后直接收口 completed，全程无唤醒。
func TestSettlementDirectHitNoWake(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wsID, aliceID, leadID, wiID := seedDispatchSvcEnv(t, ctx, svc, store)
	_ = aliceID

	direct, err := svc.CreateRun(ctx, wiID, application.CreateRunParams{
		AgentProfileID: leadID, Instruction: "@alice 直达干活",
		DispatchTrigger: domain.DispatchTriggerUserMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatches, err := store.Dispatches().ListByWorkItem(ctx, wiID)
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("应只有 1 个批次: %v %#v", err, dispatches)
	}
	batchID := dispatches[0].ID
	if dispatches[0].LeadRunID != "" {
		t.Fatalf("@直达批 lead_run_id 应为空: %q", dispatches[0].LeadRunID)
	}

	settleDriveRun(t, ctx, svc, direct.ID, "直达干完", true)
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCompleted || d.ClosedAt == nil {
		t.Fatalf("@直达批应直接收口 completed: %+v", d)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 0 {
		t.Fatalf("@直达批不得唤醒，实际 %d", n)
	}
	_ = wsID
}

// TestSettlementAllCancelled 防回归：worker 全取消 → 收口 cancelled（不是
// degraded；用户整批喊停不是部分失败）。
func TestSettlementAllCancelled(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	_, leadID, wiID, batchID, leadRunID, childRunID := settleEnv(t, ctx, svc, store)

	if err := svc.RecordRunStatus(ctx, leadRunID, domain.RunCancelled, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, childRunID, domain.RunCancelled, nil); err != nil {
		t.Fatal(err)
	}
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCollecting {
		t.Fatalf("成员齐后应 collecting，实际 %s", d.Status)
	}
	sched := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	sched.Tick(ctx, time.Now().UTC().Add(time.Minute))
	runs, err := store.Runs().ListByWorkItem(ctx, wiID)
	if err != nil {
		t.Fatal(err)
	}
	settlement := findSettlementRun(t, runs, batchID)
	settleDriveRun(t, ctx, svc, settlement.ID, "全取消汇总", true)
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCancelled {
		t.Fatalf("全取消批应收口 cancelled，实际 %s", d.Status)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 1 {
		t.Fatalf("唤醒应恒为 1，实际 %d", n)
	}
}

// TestSettlementCollectingDuplicateTriggerNoOp 防回归：collecting 下的重复
// 终态触发（迟到挂批成员）不得二次唤醒——MarkCollecting CAS 0 行 no-op。
func TestSettlementCollectingDuplicateTriggerNoOp(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wsID, leadID, wiID, batchID, leadRunID, childRunID := settleEnv(t, ctx, svc, store)

	settleDriveRun(t, ctx, svc, leadRunID, "lead 干完", true)
	settleDriveRun(t, ctx, svc, childRunID, "worker 干完", true)
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCollecting {
		t.Fatalf("应 collecting，实际 %s", d.Status)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 1 {
		t.Fatalf("应已唤醒 1 次，实际 %d", n)
	}

	// collecting 下迟到挂批成员终态：CAS 0 行，不二次唤醒。
	late := &domain.ExecutionRun{
		ID: domain.NewID(domain.PrefixRun), WorkspaceID: wsID, WorkItemID: wiID,
		Status: domain.RunQueued, DispatchID: batchID,
		Input: map[string]any{"instruction": "迟到成员"}, Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.Runs().Create(ctx, late); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, late.ID, domain.RunCancelled, nil); err != nil {
		t.Fatal(err)
	}
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCollecting {
		t.Fatalf("collecting 不得被重复触发改动，实际 %s", d.Status)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 1 {
		t.Fatalf("不得二次唤醒，实际 %d", n)
	}
}

// TestSettlementConcurrentWorkerTerminals 防回归：并发终态触发收口（-race）——
// CAS 保证只有一个触发方获得唤醒资格，唤醒恰一次、收口值一致。
func TestSettlementConcurrentWorkerTerminals(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())
	wsID, leadID, wiID, batchID, leadRunID, childRunID := settleEnv(t, ctx, svc, store)
	_ = wsID

	for _, runID := range []string{leadRunID, childRunID} {
		if err := svc.RecordRunStatus(ctx, runID, domain.RunStarting, nil); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	// 并发段走 starting→succeeding→succeeded（不经 running 任务锁——同任务双
	// run 并发 running 本就被 F1 锁拒绝），只让终态写与收口钩子真正并发。
	converge := func(runID string) error {
		for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
			if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
				return err
			}
		}
		return nil
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- converge(leadRunID)
	}()
	go func() {
		defer wg.Done()
		errs <- converge(childRunID)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发终态不应泄漏错误: %v", err)
		}
	}
	if d := dispatchOf(t, ctx, store, batchID); d.Status != domain.DispatchCollecting {
		t.Fatalf("并发触发后应 collecting，实际 %s", d.Status)
	}
	if n := len(settlementWakeups(t, ctx, store, leadID, wiID)); n != 1 {
		t.Fatalf("并发下唤醒应恰一次，实际 %d", n)
	}
}
