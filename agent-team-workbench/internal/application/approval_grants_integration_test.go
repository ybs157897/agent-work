// approval_grants_integration_test.go 审批授权粒度回归：scope≠once 决议建授权、
// 命中授权的请求自动代答（可追溯）、pattern 不匹配仍走人工、once/拒绝不建授权。
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

// awaitApprovalStatus 轮询至审批到达期望状态（自动代答是提交后异步决议）。
func awaitApprovalStatus(t *testing.T, store *sqlstore.Store, approvalID string, want domain.ApprovalStatus) *domain.ApprovalRequest {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a, err := store.Runs().GetApproval(context.Background(), approvalID)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status == want {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("审批 %s 应在期限内到达 %s，实际 %s", approvalID, want, a.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// runAtRunning 创建 run 并迁移到 running（waiting_approval 只从 running 进入）。
func runAtRunning(t *testing.T, ctx context.Context, svc *application.Service, wi *domain.WorkItem, instruction string) *domain.ExecutionRun {
	t.Helper()
	// F1 执行锁防双跑：同任务存在非终态 run（running/waiting_approval 持锁）时，
	// 新 run 会在起跑点被拒落 failed(work_item_locked)。夹具先收敛旧轮到终态
	//（failed 释放锁）再开新轮，对齐「一个任务至多一个活跃 run」的新不变量。
	runs, err := svc.RunsByWorkItem(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, prev := range runs {
		if !prev.Status.IsTerminal() {
			if err := svc.RecordRunStatus(ctx, prev.ID, domain.RunFailed, nil); err != nil {
				t.Fatalf("收敛旧 run %s 失败: %v", prev.ID, err)
			}
		}
	}
	run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{
		AgentProfileID: wi.AgentProfileID, Instruction: instruction,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, to := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, run.ID, to, nil); err != nil {
			t.Fatalf("前置迁移 %s 失败: %v", to, err)
		}
	}
	return run
}

// TestGrantAutoApprovesMatchingRequest 授权全链路：thread 决议建 grant → 同
// work item 同 kind 且摘要前缀命中的新请求被自动代答批准（resolved_by=grant:*），
// activity 可查「已按授权自动批准」——自动批准可追溯是红线。
func TestGrantAutoApprovesMatchingRequest(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run1 := runAtRunning(t, ctx, svc, wi, "首轮")
	a1, err := svc.RequestApproval(ctx, run1.ID, domain.ApprovalKindCommand, "high",
		"Codex 请求执行命令：git push")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, a1.ID, true, "user_demo", "", domain.ApprovalScopeThread); err != nil {
		t.Fatal(err)
	}
	wsID := "ws_" + t.Name()
	g, err := store.ApprovalGrants().Matching(ctx, wsID, wi.AgentProfileID, wi.ID,
		domain.ApprovalKindCommand, "Codex 请求执行命令：git push")
	if err != nil {
		t.Fatal(err)
	}
	if g == nil {
		t.Fatal("thread 决议应创建授权且同 work item 命中")
	}
	if g.Scope != domain.ApprovalScopeThread || g.WorkItemID != wi.ID ||
		g.Pattern != "Codex 请求执行命令：git push" {
		t.Fatalf("授权字段不符： %+v", g)
	}

	run2 := runAtRunning(t, ctx, svc, wi, "次轮")
	a2, err := svc.RequestApproval(ctx, run2.ID, domain.ApprovalKindCommand, "high",
		"Codex 请求执行命令：git push --force")
	if err != nil {
		t.Fatal(err)
	}
	resolved := awaitApprovalStatus(t, store, a2.ID, domain.ApprovalApproved)
	if resolved.ResolvedBy != "grant:"+g.ID {
		t.Fatalf("代答应记录授权来源 resolved_by=grant:%s，实际 %q", g.ID, resolved.ResolvedBy)
	}
	activities, err := store.Events().ListActivities(ctx, wsID, 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, act := range activities {
		if act.Kind == "approval.resolved" && strings.Contains(act.Message, "已按授权自动批准") &&
			strings.Contains(act.Message, g.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("activity 应含「已按授权自动批准（grant 摘要）」，实际 %+v", activities)
	}
	// 代答回到 running：run2 不滞留 waiting_approval。
	r2, err := store.Runs().Get(ctx, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Status != domain.RunRunning {
		t.Fatalf("代答批准后 run 应回 running，实际 %s", r2.Status)
	}
}

// TestGrantPatternMismatchStaysManual pattern 不命中的请求不得自动代答（仍人工）。
func TestGrantPatternMismatchStaysManual(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	run1 := runAtRunning(t, ctx, svc, wi, "首轮")
	a1, err := svc.RequestApproval(ctx, run1.ID, domain.ApprovalKindCommand, "high",
		"Codex 请求执行命令：git push")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveApproval(ctx, a1.ID, true, "user_demo", "", domain.ApprovalScopeWorkspace); err != nil {
		t.Fatal(err)
	}

	run2 := runAtRunning(t, ctx, svc, wi, "次轮")
	a2, err := svc.RequestApproval(ctx, run2.ID, domain.ApprovalKindCommand, "high",
		"Codex 请求执行命令：rm -rf /tmp/x")
	if err != nil {
		t.Fatal(err)
	}
	// 自动代答命中时毫秒内完成；留足余量后仍 pending 即走人工。
	time.Sleep(200 * time.Millisecond)
	got, err := store.Runs().GetApproval(ctx, a2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ApprovalPending {
		t.Fatalf("pattern 不匹配应保持 pending 走人工，实际 %s（resolved_by=%q）", got.Status, got.ResolvedBy)
	}
}

// TestResolveOnceAndRejectCreateNoGrant once 决议与拒绝路径都不建授权（负向保证）。
func TestResolveOnceAndRejectCreateNoGrant(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	mkApproval := func(instruction string) *domain.ApprovalRequest {
		run := runAtRunning(t, ctx, svc, wi, instruction)
		a, err := svc.RequestApproval(ctx, run.ID, domain.ApprovalKindCommand, "high", "Codex 请求执行命令："+instruction)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	wsID := "ws_" + t.Name()
	assertNoGrant := func(label string) {
		t.Helper()
		g, err := store.ApprovalGrants().Matching(ctx, wsID, wi.AgentProfileID, wi.ID,
			domain.ApprovalKindCommand, "Codex 请求执行命令：once")
		if err != nil {
			t.Fatal(err)
		}
		if g != nil {
			t.Fatalf("%s 不得创建授权，实际 %+v", label, g)
		}
	}

	once := mkApproval("once")
	if _, err := svc.ResolveApproval(ctx, once.ID, true, "user_demo", "", domain.ApprovalScopeOnce); err != nil {
		t.Fatal(err)
	}
	assertNoGrant("once 决议")

	rejected := mkApproval("reject")
	if _, err := svc.ResolveApproval(ctx, rejected.ID, false, "user_demo", "不批", domain.ApprovalScopeThread); err != nil {
		t.Fatal(err)
	}
	assertNoGrant("拒绝（scope 被忽略）")
	got, err := store.Runs().GetApproval(ctx, rejected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ApprovalRejected {
		t.Fatalf("拒绝路径不受影响，实际 %s", got.Status)
	}
}

// TestResolveScopeRejectsNonGrantableKind plan_dispatch 等非工具审批携带
// scope≠once 必须响亮报错（不静默降级为 once）。
func TestResolveScopeRejectsNonGrantableKind(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db, sqlstore.SQLiteDialect())
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wi := seedRunEnv(t, ctx, svc, store)
	a := &domain.ApprovalRequest{
		ID: domain.NewID(domain.PrefixApproval), WorkItemID: wi.ID,
		Kind: domain.ApprovalKindPlanDispatch, Risk: "high", Status: domain.ApprovalPending,
		Summary: "plan 分派闸门", RequestedBy: map[string]any{"kind": "plan", "id": "plan_x", "seq": 1},
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Runs().CreateApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	_, err := svc.ResolveApproval(ctx, a.ID, true, "user_demo", "", domain.ApprovalScopeThread)
	if err == nil || !strings.Contains(err.Error(), "不支持授权") {
		t.Fatalf("plan_dispatch + scope=thread 应报 ErrValidation，实际 %v", err)
	}
}
