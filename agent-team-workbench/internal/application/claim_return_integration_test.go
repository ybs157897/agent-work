// claim_return_integration_test.go M4 认领模式与手动打回集成测试（设计 note §1/§5）：
// claim（todo 无主可领、已领 409、同 agent 幂等、assignment 唤醒入队）、
// return（review/acceptance 打回 execution + activity 记 reason、todo/completed 409）。
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

// seedClaimEnv 建 workspace + 双 agent（认领者 A 开 wake_on_assignment，
// 认领者 B 不开——分别断言唤醒入队与静默认领）。
func seedClaimEnv(t *testing.T, ctx context.Context, store *sqlstore.Store) (wsID, agentA, agentB string) {
	t.Helper()
	now := time.Now().UTC()
	ws := &domain.Workspace{ID: "ws_claim", Name: "claim", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.Workspaces().Create(ctx, ws); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_claim")
	a := &domain.AgentProfile{
		ID: "agent_claim_a", WorkspaceID: ws.ID, Name: "ClaimerA", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		WakeOnAssignment: true, WakeOnDemand: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	b := &domain.AgentProfile{
		ID: "agent_claim_b", WorkspaceID: ws.ID, Name: "ClaimerB", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	for _, ag := range []*domain.AgentProfile{a, b} {
		if err := store.Agents().Create(ctx, ag); err != nil {
			t.Fatal(err)
		}
	}
	return ws.ID, a.ID, b.ID
}

func assignmentWakeCount(t *testing.T, ctx context.Context, store *sqlstore.Store, taskKey, agentID string) int {
	t.Helper()
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Minute), 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, w := range wakeups {
		if w.TaskKey == taskKey && w.AgentProfileID == agentID && w.Source == domain.WakeupSourceAssignment {
			n++
		}
	}
	return n
}

// TestClaimWorkItem 验收 1：无 assignee 的 todo 任务被认领 → assignee 落定 +
// assignment wakeup 入队；已有 assignee → 409（ErrStateConflict）；同 agent
// 重复认领幂等（返回现状，不重复入队唤醒）。
func TestClaimWorkItem(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, agentA, agentB := seedClaimEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "池中任务"})
	if err != nil {
		t.Fatal(err)
	}
	if main.AgentProfileID != "" || main.Status != domain.WorkItemTodo {
		t.Fatalf("前置条件：todo 且无 assignee，实际 %s/%q", main.Status, main.AgentProfileID)
	}

	claimed, err := svc.ClaimWorkItem(ctx, main.ID, agentA, 0)
	if err != nil {
		t.Fatalf("无主 todo 认领应成功: %v", err)
	}
	if claimed.AgentProfileID != agentA {
		t.Fatalf("认领后 assignee = %q，应为 %s", claimed.AgentProfileID, agentA)
	}
	if got := assignmentWakeCount(t, ctx, store, main.ID, agentA); got != 1 {
		t.Fatalf("assignment 唤醒入队数 = %d，应为 1", got)
	}

	// 他 agent 认领已被认领任务 → ErrStateConflict（409）。
	if _, err := svc.ClaimWorkItem(ctx, main.ID, agentB, 0); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("已被认领的任务再认领应 ErrStateConflict，实际 %v", err)
	}
	// 已认领任务换 assignee 仍走 assign 命令，不受影响（语义分界钉子）。
	reassigned, err := svc.AssignWorkItem(ctx, main.ID, agentB, 0)
	if err != nil || reassigned.AgentProfileID != agentB {
		t.Fatalf("assign 覆盖认领结果应保持可用: %v", err)
	}

	// 同 agent 重复认领幂等：返回现状、版本不变、不重复入队。
	before, err := store.WorkItems().Get(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.ClaimWorkItem(ctx, main.ID, agentB, 0)
	if err != nil {
		t.Fatalf("同 agent 重复认领应幂等: %v", err)
	}
	if again.Version != before.Version || again.AgentProfileID != agentB {
		t.Fatalf("幂等认领应返回现状: version %d->%d assignee %q", before.Version, again.Version, again.AgentProfileID)
	}
	if got := assignmentWakeCount(t, ctx, store, main.ID, agentB); got != 0 {
		t.Fatalf("幂等认领不应入队唤醒（agentB 未开 wake_on_assignment，assign 覆盖与重复认领均 0 次），实际 %d", got)
	}

	// 非 todo（in_progress）无主任务不可认领。
	wip, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "进行中任务"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MoveWorkItem(ctx, wip.ID, domain.WorkItemInProgress, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimWorkItem(ctx, wip.ID, agentA, 0); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("in_progress 任务认领应 ErrStateConflict，实际 %v", err)
	}
}

// TestClaimWorkItemWakeupGatedByPolicy：wake_on_assignment 关闭的 agent 认领
// 成功但不入队唤醒（策略在 enqueueAssignmentWake 内判定）。
func TestClaimWorkItemWakeupGatedByPolicy(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, _, agentB := seedClaimEnv(t, ctx, store)
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "静默认领"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ClaimWorkItem(ctx, main.ID, agentB, 0); err != nil {
		t.Fatal(err)
	}
	if got := assignmentWakeCount(t, ctx, store, main.ID, agentB); got != 0 {
		t.Fatalf("wake_on_assignment 关闭时不应入队唤醒，实际 %d", got)
	}
}

// TestReturnWorkItem 验收 5：review/acceptance 态打回 → phase=execution +
// activity 含 reason；todo / completed 打回 → 409（ErrStateConflict）。
func TestReturnWorkItem(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, _, workerID := seedPlanEnv(t, ctx, store)

	// review 态：run 成功后 EnterReview 联动。
	reviewed, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "待评审"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, reviewed.ID, application.CreateRunParams{
		AgentProfileID: workerID, Instruction: "交付一版",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordRunStatus(ctx, run.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, run.ID, "交付完成"); err != nil {
		t.Fatal(err)
	}
	wi, err := store.WorkItems().Get(ctx, reviewed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wi.Phase != domain.PhaseReview {
		t.Fatalf("前置条件：run 成功后应 review，实际 %s", wi.Phase)
	}
	returned, err := svc.ReturnWorkItem(ctx, reviewed.ID, "评审意见：缺测试", 0)
	if err != nil {
		t.Fatalf("review 态打回应成功: %v", err)
	}
	if returned.Phase != domain.PhaseExecution || returned.Status != domain.WorkItemInProgress {
		t.Fatalf("打回后应 execution/in_progress，实际 %s/%s", returned.Status, returned.Phase)
	}
	activities, err := store.Events().ListActivities(ctx, wsID, 30)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range activities {
		if a.Kind == "work_item.returned" && strings.Contains(a.Message, "缺测试") {
			found = true
		}
	}
	if !found {
		t.Fatalf("打回 activity 未记录 reason: %#v", activities)
	}

	// acceptance 态打回同样合法（经 EnterAcceptance 域迁移构造）。
	// RFC §7.9：reason 从本版本起必填（trim 后为空 → review_feedback_required）。
	accepting, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "待验收"})
	if err != nil {
		t.Fatal(err)
	}
	wi2, err := store.WorkItems().Get(ctx, accepting.ID)
	if err != nil {
		t.Fatal(err)
	}
	// todo → in_progress（review）→ acceptance。
	if err := wi2.Transition(domain.WorkItemInProgress, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, wi2, wi2.Version-1); err != nil {
		t.Fatal(err)
	}
	if err := wi2.EnterReview(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, wi2, wi2.Version-1); err != nil {
		t.Fatal(err)
	}
	if err := wi2.EnterAcceptance(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, wi2, wi2.Version-1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReturnWorkItem(ctx, wi2.ID, "  \t ", 0); !errors.Is(err, application.ErrReviewFeedbackRequired) {
		t.Fatalf("空 reason 打回应 review_feedback_required，实际 %v", err)
	}
	if _, err := svc.ReturnWorkItem(ctx, wi2.ID, "验收口径未达成", 0); err != nil {
		t.Fatalf("acceptance 态打回应成功: %v", err)
	}
	after, err := store.WorkItems().Get(ctx, wi2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Phase != domain.PhaseExecution {
		t.Fatalf("acceptance 打回后应 execution，实际 %s", after.Phase)
	}

	// todo / completed 态打回 → ErrStateConflict。
	todo, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "未开始"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReturnWorkItem(ctx, todo.ID, "尚未开始无法打回", 0); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("todo 打回应 ErrStateConflict，实际 %v", err)
	}
	done, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "已完工"})
	if err != nil {
		t.Fatal(err)
	}
	wiDone, err := store.WorkItems().Get(ctx, done.ID)
	if err != nil {
		t.Fatal(err)
	}
	// todo → in_progress（execution）→ review → Accept（唯一完工路径）。
	if err := wiDone.Transition(domain.WorkItemInProgress, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, wiDone, wiDone.Version-1); err != nil {
		t.Fatal(err)
	}
	if err := wiDone.EnterReview(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, wiDone, wiDone.Version-1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptWorkItem(ctx, done.ID, 0); err != nil {
		t.Fatalf("构造 completed 前置失败: %v", err)
	}
	if _, err := svc.ReturnWorkItem(ctx, done.ID, "已完工不可打回", 0); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("completed 打回应 ErrStateConflict，实际 %v", err)
	}
}
