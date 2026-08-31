package sqlstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func TestWorkItemRecordKindPersistenceAndFilter(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	now := time.Now().UTC().Truncate(time.Microsecond)

	chat := &domain.WorkItem{
		ID: "wi_chat_kind", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindChat,
		Title: "普通对话", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	task := &domain.WorkItem{
		ID: "wi_task_kind", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "任务发布", Status: domain.WorkItemTodo, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}
	for _, item := range []*domain.WorkItem{chat, task} {
		if err := store.WorkItems().Create(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.WorkItems().Get(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RecordKind != domain.RecordKindChat {
		t.Fatalf("Chat record_kind 未持久化: %q", got.RecordKind)
	}
	got, err = store.WorkItems().Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RecordKind != domain.RecordKindTask {
		t.Fatalf("Task record_kind 未持久化: %q", got.RecordKind)
	}

	items, _, err := store.WorkItems().List(ctx, "ws_wk", application.WorkItemFilter{
		RecordKind: domain.RecordKindTask,
		Limit:      20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != task.ID {
		t.Fatalf("record_kind=task 列表混入 Chat: %#v", items)
	}

	if _, _, err := store.WorkItems().List(ctx, "ws_wk", application.WorkItemFilter{
		RecordKind: domain.WorkItemRecordKind("invalid"), Limit: 20,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("非法 record_kind 应返回 ErrValidation，实际 %v", err)
	}
}

func TestWorkItemTouchUpdatedAtDoesNotMutateTaskState(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	created := time.Date(2026, time.January, 1, 1, 2, 3, 0, time.UTC)
	item := &domain.WorkItem{
		ID: "wi_touch", WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask,
		Title: "触碰排序时间", Description: "keep", Status: domain.WorkItemInProgress,
		Phase: domain.PhaseReview, Priority: domain.PriorityHigh, Version: 7,
		CreatedAt: created, UpdatedAt: created,
	}
	if err := store.WorkItems().Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	before, err := store.WorkItems().Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	touched := created.Add(5 * time.Minute)
	if err := store.WorkItems().TouchUpdatedAt(ctx, item.ID, touched); err != nil {
		t.Fatal(err)
	}
	after, err := store.WorkItems().Get(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(touched) {
		t.Fatalf("TouchUpdatedAt 应刷新 updated_at=%s，实际 %s", touched, after.UpdatedAt)
	}
	if after.Version != before.Version || after.Status != before.Status || after.Phase != before.Phase ||
		after.Title != before.Title || after.Description != before.Description || after.RecordKind != before.RecordKind {
		t.Fatalf("TouchUpdatedAt 不得改变任务字段: before=%+v after=%+v", before, after)
	}
	if err := store.WorkItems().TouchUpdatedAt(ctx, "wi_missing", touched); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("未知 work item 应返回 ErrNotFound，实际 %v", err)
	}
}

func TestTaskAggregatesExcludeChatRecords(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	now := time.Now().UTC()
	create := func(id string, kind domain.WorkItemRecordKind, status domain.WorkItemStatus) {
		t.Helper()
		if err := store.WorkItems().Create(ctx, &domain.WorkItem{
			ID: id, WorkspaceID: "ws_wk", RecordKind: kind, Title: id,
			Status: status, Priority: domain.PriorityMedium, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	create("wi_task_todo", domain.RecordKindTask, domain.WorkItemTodo)
	create("wi_task_done", domain.RecordKindTask, domain.WorkItemCompleted)
	create("wi_chat_todo", domain.RecordKindChat, domain.WorkItemTodo)
	create("wi_chat_done", domain.RecordKindChat, domain.WorkItemCompleted)

	counts, err := store.WorkItems().BoardCounts(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if counts[domain.WorkItemTodo] != 1 || counts[domain.WorkItemCompleted] != 1 {
		t.Fatalf("BoardCounts 应只统计 Task: %#v", counts)
	}
	completed, err := store.WorkItems().CompletedToday(ctx, "ws_wk", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Fatalf("CompletedToday 应排除 Chat，实际 %d", completed)
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO execution_runs(id,workspace_id,work_item_id,status,version,created_at,updated_at)
		VALUES ('run_task_active','ws_wk','wi_task_todo','running',1,?,?),
		       ('run_chat_active','ws_wk','wi_chat_todo','running',1,?,?)`, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	active, err := store.Runs().ActiveCount(ctx, "ws_wk")
	if err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("ActiveCount 应只统计 Task run，实际 %d", active)
	}
}
