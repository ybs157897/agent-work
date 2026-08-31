package sqlstore_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

// TestDispatchRoundTripAndList 批次往返：lead_run_id/closed_at 空值语义 + 按
// work item 升序列表（卡片端点倒序展示的底层序）。
func TestDispatchRoundTripAndList(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	seedWorkspace(t, db)
	store := sqlstore.New(db)
	insertWorkItem(t, db, "wi_disp")

	first := &domain.Dispatch{
		ID: domain.NewID(domain.PrefixDispatch), WorkItemID: "wi_disp",
		Trigger: domain.DispatchTriggerUserMessage, Status: domain.DispatchRunning,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := store.Dispatches().Create(ctx, first); err != nil {
		t.Fatal(err)
	}
	// 接诊批次：lead_run_id 记接诊 run（先落该 run 满足外键）。
	seedDispatchRun(t, db, "run_lead", "wi_disp", "")
	second := &domain.Dispatch{
		ID: domain.NewID(domain.PrefixDispatch), WorkItemID: "wi_disp",
		Trigger: domain.DispatchTriggerLeadPlan, LeadRunID: "run_lead",
		Status: domain.DispatchRunning, CreatedAt: time.Now().UTC(),
	}
	if err := store.Dispatches().Create(ctx, second); err != nil {
		t.Fatal(err)
	}

	got, err := store.Dispatches().Get(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger != domain.DispatchTriggerUserMessage || got.LeadRunID != "" || got.ClosedAt != nil {
		t.Fatalf("@直达批次往返失真: %+v", got)
	}
	if got.Status != domain.DispatchRunning {
		t.Fatalf("status 应为 running: %s", got.Status)
	}
	got2, err := store.Dispatches().Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.LeadRunID != "run_lead" || got2.Trigger != domain.DispatchTriggerLeadPlan {
		t.Fatalf("接诊批次往返失真: %+v", got2)
	}

	list, err := store.Dispatches().ListByWorkItem(ctx, "wi_disp")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("ListByWorkItem 应按 created_at 升序: %+v", list)
	}
	if empty, err := store.Dispatches().ListByWorkItem(ctx, "wi_none"); err != nil || len(empty) != 0 {
		t.Fatalf("无批次任务应返回空列表: %v %#v", err, empty)
	}
}

// seedDispatchRun 直插一条 run（可挂 dispatch_id），供成员往返与外键断言用。
func seedDispatchRun(t *testing.T, db *sql.DB, id, workItemID, dispatchID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var ref any
	if dispatchID != "" {
		ref = dispatchID
	}
	if _, err := db.Exec(
		`INSERT INTO execution_runs(id, workspace_id, work_item_id, status, version, created_at, updated_at, dispatch_id)
		 VALUES (?,'ws_wk',?,'queued',1,?,?,?)`, id, workItemID, now, now, ref); err != nil {
		t.Fatalf("seed run %s: %v", id, err)
	}
}

// TestRunDispatchMembership 成员 run 的 dispatch_id 往返、会话组查询与外键约束。
func TestRunDispatchMembership(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	seedWorkspace(t, db)
	store := sqlstore.New(db)
	insertWorkItem(t, db, "wi_group")

	d := &domain.Dispatch{
		ID: domain.NewID(domain.PrefixDispatch), WorkItemID: "wi_group",
		Trigger: domain.DispatchTriggerUserMessage, Status: domain.DispatchRunning,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Dispatches().Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	seedDispatchRun(t, db, "run_m1", "wi_group", d.ID)
	seedDispatchRun(t, db, "run_m2", "wi_group", d.ID)
	seedDispatchRun(t, db, "run_free", "wi_group", "")

	member, err := store.Runs().Get(ctx, "run_m1")
	if err != nil {
		t.Fatal(err)
	}
	if member.DispatchID != d.ID {
		t.Fatalf("成员 run dispatch_id 往返失真: %q", member.DispatchID)
	}
	free, err := store.Runs().Get(ctx, "run_free")
	if err != nil {
		t.Fatal(err)
	}
	if free.DispatchID != "" {
		t.Fatalf("无批次 run 的 dispatch_id 应为空: %q", free.DispatchID)
	}
	members, err := store.Runs().ListByDispatch(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].ID != "run_m1" || members[1].ID != "run_m2" {
		t.Fatalf("ListByDispatch 应返回全部成员且升序: %+v", members)
	}

	// 外键守卫：dispatch_id 指向不存在的批次必须被拒（会话组不许出现幽灵成员）。
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(
		`INSERT INTO execution_runs(id, workspace_id, work_item_id, status, version, created_at, updated_at, dispatch_id)
		 VALUES ('run_ghost','ws_wk','wi_group','queued',1,?,?, 'disp_missing')`, now, now); err == nil {
		t.Fatal("dispatch_id 外键应拒绝幽灵批次")
	}
}

// TestTaskSessionSegmentSeq 片段序号：首段落 1、续接不变、轮换代际 +1。
func TestTaskSessionSegmentSeq(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	seedWorkspace(t, db)
	store := sqlstore.New(db)
	insertWorkItem(t, db, "wi_seg")

	now := time.Now().UTC()
	anchor := func(runsCount int) *domain.TaskSession {
		return &domain.TaskSession{
			ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: "ws_wk",
			AgentProfileID: "agent_seg", AdapterID: "codex-appserver", TaskKey: "wi_seg",
			SessionParams: map[string]any{"__ref": "codex://s"}, RunsCount: runsCount,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	// 首段：构造方不感知序号（零值）→ 落 1。
	if err := store.TaskSessions().Upsert(ctx, anchor(1)); err != nil {
		t.Fatal(err)
	}
	ts, err := store.TaskSessions().Get(ctx, "ws_wk", "agent_seg", "codex-appserver", "wi_seg")
	if err != nil {
		t.Fatal(err)
	}
	if ts.SegmentSeq != 1 {
		t.Fatalf("首段 segment_seq 应为 1，实际 %d", ts.SegmentSeq)
	}
	// 续接：计数累加，片段不变。
	if err := store.TaskSessions().Upsert(ctx, anchor(1)); err != nil {
		t.Fatal(err)
	}
	if ts, err = store.TaskSessions().Get(ctx, "ws_wk", "agent_seg", "codex-appserver", "wi_seg"); err != nil {
		t.Fatal(err)
	}
	if ts.SegmentSeq != 1 || ts.RunsCount != 2 {
		t.Fatalf("续接不得推进片段: seq=%d runs=%d", ts.SegmentSeq, ts.RunsCount)
	}
	// 轮换代际：片段 +1。
	if err := store.TaskSessions().StartGeneration(ctx, anchor(1)); err != nil {
		t.Fatal(err)
	}
	if ts, err = store.TaskSessions().Get(ctx, "ws_wk", "agent_seg", "codex-appserver", "wi_seg"); err != nil {
		t.Fatal(err)
	}
	if ts.SegmentSeq != 2 || ts.RunsCount != 1 {
		t.Fatalf("轮换代际应推进片段且计数重起: seq=%d runs=%d", ts.SegmentSeq, ts.RunsCount)
	}
	if err := store.TaskSessions().StartGeneration(ctx, anchor(1)); err != nil {
		t.Fatal(err)
	}
	if ts, err = store.TaskSessions().Get(ctx, "ws_wk", "agent_seg", "codex-appserver", "wi_seg"); err != nil {
		t.Fatal(err)
	}
	if ts.SegmentSeq != 3 {
		t.Fatalf("再次轮换应推进到片段 3，实际 %d", ts.SegmentSeq)
	}
}
