package application_test

// review_surface_integration_test.go Review Queue / Delivery Brief read model
// 行为（任务控制面 RFC §4.10/§4.11/§15.5 验证矩阵）：
//   - queue 派生条件/固定排序/total_count 独立于分页/排序键 cursor 稳定；
//   - review→execution→review 的 pending_since 必须更新；status 离开
//     in_progress 时 phase/phase_entered_at 一并清理；
//   - criteria 创建即持久化、首轮 Run 后拒改、Plan child 持久化 step acceptance。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// seedQueueTask 创建普通 Task（无 Coordinator）并把 phase 投影钉到指定时间
// （fixture 播种：与 setCoordinatorWaitingUser 同风格，直接经领域字段写投影）。
func seedQueueTask(t *testing.T, ctx context.Context, svc *application.Service, store *sqlstore.Store,
	wsID, title string, phase domain.WorkItemPhase, priority domain.Priority, at time.Time) *domain.WorkItem {
	t.Helper()
	wi, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: title, Priority: priority})
	if err != nil {
		t.Fatal(err)
	}
	return pinPhaseDirect(t, ctx, store, wi.ID, phase, at)
}

// pinPhaseDirect 把 Task 钉进 in_progress + 指定 phase，phase_entered_at=at。
func pinPhaseDirect(t *testing.T, ctx context.Context, store *sqlstore.Store,
	workItemID string, phase domain.WorkItemPhase, at time.Time) *domain.WorkItem {
	t.Helper()
	wi, err := store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		t.Fatal(err)
	}
	expected := wi.Version
	wi.Status = domain.WorkItemInProgress
	wi.Phase = phase
	wi.PhaseEnteredAt = &at
	if err := store.WorkItems().Update(ctx, wi, expected); err != nil {
		t.Fatal(err)
	}
	return store_WorkItems(t, ctx, store, workItemID)
}

// store_WorkItems 重新读取（Update 只在 DB 侧 bump version）。
func store_WorkItems(t *testing.T, ctx context.Context, store *sqlstore.Store, id string) *domain.WorkItem {
	t.Helper()
	wi, err := store.WorkItems().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return wi
}

// driveRunSucceeded 把 queued Run 驱动到 succeeded（含 message.completed）。
func driveRunSucceeded(t *testing.T, ctx context.Context, svc *application.Service, runID, text string) {
	t.Helper()
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RecordRunEvent(ctx, runID, domain.EventMessageCompleted, map[string]any{"role": "assistant", "text": text}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReviewQueueProjection(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	_ = workerID
	base := time.Now().UTC().Add(-4 * time.Hour)
	t0, t1, t2 := base, base.Add(time.Hour), base.Add(2*time.Hour)

	// 非 in_progress / 非 review·acceptance / chat 均不入队。
	execTask, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "执行中"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRun(ctx, execTask.ID, application.CreateRunParams{
		AgentProfileID: "agent_coordinator_worker", Instruction: "x",
	}); err != nil {
		t.Fatal(err)
	}
	blockedRow := seedQueueTask(t, ctx, svc, store, wsID, "阻塞", domain.PhaseReview, domain.PriorityLow, t0)
	if _, err := svc.BlockWorkItem(ctx, blockedRow.ID, application.BlockParams{
		Code: "manual", Message: "测试阻塞", Source: "test",
	}, blockedRow.Version); err != nil {
		t.Fatal(err)
	}
	chat, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "对话", RecordKind: domain.RecordKindChat, AgentProfileID: "agent_coordinator_worker",
	})
	if err != nil {
		t.Fatal(err)
	}

	rev1 := seedQueueTask(t, ctx, svc, store, wsID, "评审1", domain.PhaseReview, domain.PriorityMedium, t1)
	acc := seedQueueTask(t, ctx, svc, store, wsID, "待验收", domain.PhaseAcceptance, domain.PriorityMedium, t0)
	revUrgent := seedQueueTask(t, ctx, svc, store, wsID, "评审加急", domain.PhaseReview, domain.PriorityUrgent, t2)
	revMed := seedQueueTask(t, ctx, svc, store, wsID, "评审普通", domain.PhaseReview, domain.PriorityMedium, t2)

	page, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range page.Items {
		got = append(got, item.WorkItem.ID)
	}
	want := []string{acc.ID, rev1.ID, revUrgent.ID, revMed.ID} // pending_since ASC, priority DESC, id ASC
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("排序不符:\n got %v\nwant %v", got, want)
	}
	if page.TotalCount != 4 {
		t.Fatalf("total_count 应为 4（独立于分页），实际 %d", page.TotalCount)
	}
	if page.NextCursor != "" {
		t.Fatalf("全量页不应有 next_cursor: %q", page.NextCursor)
	}

	// 执行态/阻塞/chat 绝不入队。
	for _, excluded := range []string{execTask.ID, blockedRow.ID, chat.ID} {
		for _, item := range page.Items {
			if item.WorkItem.ID == excluded {
				t.Fatalf("%s 不应出现在队列", excluded)
			}
		}
	}

	// 分页：limit=2 → 一页 2 条 + next_cursor；total_count 恒 4。
	p1, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 2 || p1.TotalCount != 4 || p1.NextCursor == "" {
		t.Fatalf("首页不符: n=%d total=%d cursor=%q", len(p1.Items), p1.TotalCount, p1.NextCursor)
	}
	p2, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID, Limit: 2, Cursor: p1.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 2 || p2.NextCursor != "" || p2.TotalCount != 4 {
		t.Fatalf("次页不符: n=%d total=%d cursor=%q", len(p2.Items), p2.TotalCount, p2.NextCursor)
	}
	if p2.Items[0].WorkItem.ID != want[2] || p2.Items[1].WorkItem.ID != want[3] {
		t.Fatalf("cursor 续页顺序不符: %s %s", p2.Items[0].WorkItem.ID, p2.Items[1].WorkItem.ID)
	}

	// cursor 稳定：limit=1 逐页走完，顺序与全量一致。
	cursor := ""
	var walked []string
	for {
		p, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID, Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Items) == 0 {
			break
		}
		walked = append(walked, p.Items[0].WorkItem.ID)
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if fmt.Sprint(walked) != fmt.Sprint(want) {
		t.Fatalf("limit=1 逐页顺序不符: %v vs %v", walked, want)
	}

	// phase/priority 过滤。
	onlyAcc, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID, Phase: domain.PhaseAcceptance})
	if err != nil {
		t.Fatal(err)
	}
	if onlyAcc.TotalCount != 1 || onlyAcc.Items[0].WorkItem.ID != acc.ID {
		t.Fatalf("phase=acceptance 过滤不符: %+v", onlyAcc)
	}
	onlyUrgent, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID, Priority: domain.PriorityUrgent})
	if err != nil {
		t.Fatal(err)
	}
	if onlyUrgent.TotalCount != 1 || onlyUrgent.Items[0].WorkItem.ID != revUrgent.ID {
		t.Fatalf("priority=urgent 过滤不符: %+v", onlyUrgent)
	}
	if _, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID, Phase: "bogus"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("非法 phase 应 validation 错误，实际 %v", err)
	}

	// 投影：watermark 来自同一 snapshot；非 Coordinator Task 的 coordinator 为 null。
	for _, item := range page.Items {
		if item.Watermark.AsOfEventSeq == 0 {
			t.Fatalf("as_of_event_seq 不应为 0")
		}
		if item.Watermark.WorkItemVersion != item.WorkItem.Version {
			t.Fatalf("work_item_version 不符: %d vs %d", item.Watermark.WorkItemVersion, item.WorkItem.Version)
		}
		if item.Coordinator != nil {
			t.Fatalf("普通 Task 的 coordinator 投影应为 null: %s", item.WorkItem.ID)
		}
		if item.LatestRunID != "" || item.Watermark.LatestRunVersion != 0 {
			t.Fatalf("无 run 的 Task latest run 投影应为空: %+v", item)
		}
		if item.Watermark.CommentRevision != 0 {
			t.Fatalf("无评论流的 Task comment_revision 应为 0: %+v", item)
		}
	}
	seq, err := store.Events().LatestSeq(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Watermark.AsOfEventSeq != seq {
		t.Fatalf("as_of_event_seq 应等于同库 MAX(stream_seq): %d vs %d",
			page.Items[0].Watermark.AsOfEventSeq, seq)
	}
}

func TestReviewQueuePendingSinceRefreshesAcrossReviewExecutionReview(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	wi, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "往返任务"})
	if err != nil {
		t.Fatal(err)
	}
	run1, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "第一轮"})
	if err != nil {
		t.Fatal(err)
	}
	driveRunSucceeded(t, ctx, svc, run1.ID, "第一轮完成")
	first, err := store.WorkItems().Get(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Phase != domain.PhaseReview || first.PhaseEnteredAt == nil {
		t.Fatalf("首轮成功后应进 review 且带时间: %+v", first)
	}
	firstEntered := *first.PhaseEnteredAt
	page, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].PendingSince.Equal(firstEntered) {
		t.Fatalf("pending_since 应等于 phase_entered_at: %v vs %v", page.Items[0].PendingSince, firstEntered)
	}

	// review → execution（第二轮 run 创建）→ review：时间必须更新。
	run2, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "第二轮"})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := store.WorkItems().Get(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mid.Phase != domain.PhaseExecution || mid.PhaseEnteredAt == nil || !mid.PhaseEnteredAt.After(firstEntered) {
		t.Fatalf("回 execution 应写新时间: %+v", mid)
	}
	driveRunSucceeded(t, ctx, svc, run2.ID, "第二轮完成")
	second, err := store.WorkItems().Get(ctx, wi.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Phase != domain.PhaseReview || second.PhaseEnteredAt == nil || !second.PhaseEnteredAt.After(firstEntered) {
		t.Fatalf("review→execution→review 必须得到新时间: %+v", second)
	}
}

func TestStatusLeavingInProgressClearsPhaseProjection(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)

	// Accept：completed 后 phase/phase_entered_at 清空。
	accept := seedQueueTask(t, ctx, svc, store, wsID, "待验收清理", domain.PhaseReview, domain.PriorityMedium, time.Now().UTC())
	if _, err := svc.AcceptWorkItem(ctx, accept.ID, 0); err != nil {
		t.Fatal(err)
	}
	done, err := store.WorkItems().Get(ctx, accept.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != domain.WorkItemCompleted || done.Phase != "" || done.PhaseEnteredAt != nil {
		t.Fatalf("Accept 后应清理 phase 投影: %+v", done)
	}

	// blocked：phase/phase_entered_at 清空；unblock 回 execution 写新时间。
	back := seedQueueTask(t, ctx, svc, store, wsID, "打回清理", domain.PhaseReview, domain.PriorityMedium, time.Now().UTC())
	if _, err := svc.BlockWorkItem(ctx, back.ID, application.BlockParams{
		Code: "manual", Message: "测试阻塞", Source: "test",
	}, 0); err != nil {
		t.Fatal(err)
	}
	blockedRow, err := store.WorkItems().Get(ctx, back.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blockedRow.Phase != "" || blockedRow.PhaseEnteredAt != nil {
		t.Fatalf("blocked 后应清理 phase 投影: %+v", blockedRow)
	}
	if _, err := svc.UnblockWorkItem(ctx, back.ID, 0); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.WorkItems().Get(ctx, back.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != domain.WorkItemInProgress || resumed.Phase != domain.PhaseExecution || resumed.PhaseEnteredAt == nil {
		t.Fatalf("unblock 应回 execution 且写进入时间: %+v", resumed)
	}
}

func TestAcceptanceCriteriaPersistenceAndImmutability(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)

	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "验收任务", AcceptanceCriteria: []string{"AC-1", "  ", "AC-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(stored.AcceptanceCriteria) != fmt.Sprint([]string{"AC-1", "AC-2"}) {
		t.Fatalf("criteria 应随创建持久化并归一: %v", stored.AcceptanceCriteria)
	}

	// 首轮 Run 之前允许原地修改。
	newCriteria := []string{"AC-0"}
	patched, err := svc.UpdateWorkItemFields(ctx, root.ID, application.WorkItemFieldPatch{
		AcceptanceCriteria: &newCriteria, ExpectedVersion: stored.Version,
	})
	if err != nil {
		t.Fatalf("首轮 Run 前改 criteria 应允许: %v", err)
	}
	if fmt.Sprint(patched.AcceptanceCriteria) != fmt.Sprint([]string{"AC-0"}) {
		t.Fatalf("criteria 应已更新: %v", patched.AcceptanceCriteria)
	}

	// 首轮 Run 后拒绝原地修改；新增要求走 requirement comment。
	if _, err := svc.CreateRun(ctx, root.ID, application.CreateRunParams{AgentProfileID: workerID, Instruction: "开工"}); err != nil {
		t.Fatal(err)
	}
	after, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	smuggled := []string{"偷改"}
	if _, err := svc.UpdateWorkItemFields(ctx, root.ID, application.WorkItemFieldPatch{
		AcceptanceCriteria: &smuggled, ExpectedVersion: after.Version,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("首轮 Run 后改 criteria 应拒绝，实际 %v", err)
	}
	unchanged, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(unchanged.AcceptanceCriteria) != fmt.Sprint([]string{"AC-0"}) {
		t.Fatalf("criteria 不应被改: %v", unchanged.AcceptanceCriteria)
	}

	// Chat 记录没有验收读模型。
	chat, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "对话", RecordKind: domain.RecordKindChat, AgentProfileID: workerID,
		AcceptanceCriteria: []string{"不该落库"},
	})
	if err != nil {
		t.Fatal(err)
	}
	chatRow, err := store.WorkItems().Get(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chatRow.AcceptanceCriteria) != 0 {
		t.Fatalf("chat 记录不应持久化 criteria: %v", chatRow.AcceptanceCriteria)
	}

	// Plan child 创建持久化对应 step acceptance。
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "主任务"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: workerID,
		Steps: []application.PlanStepInput{{Verb: "dispatch", Payload: map[string]any{
			"agent_id": workerID, "title": "子任务", "instruction": "做",
			"acceptance": []any{"子任务完成"}, "priority": "high",
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	children, err := store.WorkItems().ListByParent(ctx, main.ID)
	if err != nil || len(children) != 1 {
		t.Fatalf("应有 1 个 plan child: %v", err)
	}
	if fmt.Sprint(children[0].AcceptanceCriteria) != fmt.Sprint([]string{"子任务完成"}) {
		t.Fatalf("plan child 应持久化 step acceptance: %v", children[0].AcceptanceCriteria)
	}
}

func TestReviewQueueChildUsesRootCommentWatermark(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "带评论根任务", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := svc.AppendTaskComment(ctx, application.AppendTaskCommentParams{
		WorkItemID: root.ID, Kind: domain.CommentNote, Body: "根任务备注", ClientKey: "queue:root-comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	child := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
		ParentID: root.ID, Title: "待审子任务", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := child.Transition(domain.WorkItemInProgress, now); err != nil {
		t.Fatal(err)
	}
	if err := child.EnterReview(now); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Create(ctx, child); err != nil {
		t.Fatal(err)
	}
	page, err := svc.ReviewQueue(ctx, application.ReviewQueueQuery{WorkspaceID: wsID})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.WorkItem.ID != child.ID {
			continue
		}
		if item.Watermark.CommentRevision != comment.Revision {
			t.Fatalf("child queue 行必须带 root comment cursor: got=%d want=%d", item.Watermark.CommentRevision, comment.Revision)
		}
		return
	}
	t.Fatalf("Review Queue 缺少 child: %+v", page.Items)
}
