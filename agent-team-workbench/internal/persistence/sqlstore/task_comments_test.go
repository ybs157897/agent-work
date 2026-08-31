package sqlstore_test

// task_comments_test.go TaskComment 仓储行为（任务控制面 RFC §4.9/§15.1）：
// cursor 单调分配（并发无重复）、append-only、分页、client_key 幂等重放/冲突、
// 消费水位查询。仓储只承载存储语义；应用层联动见 application 集成测试。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/migtest"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func seedCommentTree(t *testing.T, ctx context.Context, store *sqlstore.Store, wsID, rootID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{ID: rootID, WorkspaceID: wsID,
		RecordKind: domain.RecordKindTask, Title: "root", Status: domain.WorkItemTodo,
		Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.TaskComments().EnsureCursor(ctx, rootID); err != nil {
		t.Fatal(err)
	}
}

func TestTaskCommentCursorAllocateRevisionsMonotonically(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	seedCommentTree(t, ctx, store, "ws_wk", "wi_cmt_root")

	// 已存在但无 cursor（历史非 Coordinator Task 形态）→ ErrNotFound
	//（comment_coordinator_required 的映射依据）。
	now := time.Now().UTC()
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{ID: "wi_cmt_legacy",
		WorkspaceID: "ws_wk", RecordKind: domain.RecordKindTask, Title: "legacy",
		Status: domain.WorkItemTodo, Priority: domain.PriorityMedium, Version: 1,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TaskComments().Append(ctx, &domain.TaskComment{
		WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_legacy", WorkItemID: "wi_cmt_legacy",
		Kind: domain.CommentNote, Body: "hi", ActorKind: domain.CommentActorUser,
		ActorID: "user_demo", CreatedAt: now,
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("无 cursor Append 应 ErrNotFound，实际 %v", err)
	}
	// 不存在的根：存储侧归属校验拒绝。
	if _, err := store.TaskComments().Append(ctx, &domain.TaskComment{
		ID: "cmt_x", WorkspaceID: "ws_wk", RootWorkItemID: "wi_missing", WorkItemID: "wi_missing",
		Kind: domain.CommentNote, Body: "hi", ActorKind: domain.CommentActorUser,
		ActorID: "user_demo", CreatedAt: now,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("不存在的 root 应被归属校验拒绝，实际 %v", err)
	}

	for want := int64(1); want <= 3; want++ {
		c, err := store.TaskComments().Append(ctx, &domain.TaskComment{
			WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_root", WorkItemID: "wi_cmt_root",
			Kind: domain.CommentNote, Body: fmt.Sprintf("body %d", want),
			ActorKind: domain.CommentActorUser, ActorID: "user_demo", CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if c.Revision != want {
			t.Fatalf("revision 应由 cursor 单调分配：want %d got %d", want, c.Revision)
		}
		if c.ID == "" || !strings.HasPrefix(c.ID, "cmt_") {
			t.Fatalf("comment id 应带 cmt_ 前缀: %q", c.ID)
		}
	}
	latest, err := store.TaskComments().LatestRevision(ctx, "wi_cmt_root")
	if err != nil || latest != 3 {
		t.Fatalf("cursor 水位应=3: latest=%d err=%v", latest, err)
	}
	// EnsureCursor 幂等，不重置水位。
	if err := store.TaskComments().EnsureCursor(ctx, "wi_cmt_root"); err != nil {
		t.Fatal(err)
	}
	if latest, err = store.TaskComments().LatestRevision(ctx, "wi_cmt_root"); err != nil || latest != 3 {
		t.Fatalf("EnsureCursor 不得重置水位: latest=%d err=%v", latest, err)
	}
}

func TestTaskCommentRevisionConcurrentAppendNoDuplicate(t *testing.T) {
	ctx := context.Background()
	// 多连接 + busy_timeout：让多个 goroutine 真正并发抢 cursor 行。
	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "race.db")+"?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(8)
	if err := migtest.ApplyAll(db); err != nil {
		t.Fatal(err)
	}
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	seedCommentTree(t, ctx, store, "ws_wk", "wi_cmt_race")

	const writers = 12
	revisions := make(chan int64, writers)
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c, err := store.TaskComments().Append(ctx, &domain.TaskComment{
				WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_race", WorkItemID: "wi_cmt_race",
				Kind: domain.CommentNote, Body: fmt.Sprintf("writer %d", n),
				ActorKind: domain.CommentActorUser, ActorID: "user_demo", CreatedAt: time.Now().UTC(),
			})
			if err != nil {
				errs <- err
				return
			}
			revisions <- c.Revision
		}(i)
	}
	wg.Wait()
	close(revisions)
	close(errs)
	for err := range errs {
		t.Fatalf("并发 Append 失败: %v", err)
	}
	seen := map[int64]bool{}
	for rev := range revisions {
		if seen[rev] {
			t.Fatalf("revision %d 重复分配（UNIQUE(root, revision) 应由 cursor 分配保证）", rev)
		}
		seen[rev] = true
	}
	if len(seen) != writers {
		t.Fatalf("应分配 %d 个互异 revision，实际 %d", writers, len(seen))
	}
	latest, err := store.TaskComments().LatestRevision(ctx, "wi_cmt_race")
	if err != nil || latest != writers {
		t.Fatalf("cursor 水位应=%d: latest=%d err=%v", writers, latest, err)
	}
}

type ctxKeyUnused struct{}

func TestTaskCommentAppendOnlyAndPagination(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	seedCommentTree(t, ctx, store, "ws_wk", "wi_cmt_page")

	for i := 1; i <= 5; i++ {
		if _, err := store.TaskComments().Append(ctx, &domain.TaskComment{
			WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_page", WorkItemID: "wi_cmt_page",
			Kind: domain.CommentNote, Body: fmt.Sprintf("n%d", i),
			ActorKind: domain.CommentActorUser, ActorID: "user_demo", CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.TaskComments().ListByRoot(ctx, "wi_cmt_page", 0, 2)
	if err != nil || len(items) != 2 || items[0].Revision != 1 || items[2-1].Revision != 2 {
		t.Fatalf("首页分页错误: items=%+v err=%v", items, err)
	}
	items, err = store.TaskComments().ListByRoot(ctx, "wi_cmt_page", 2, 50)
	if err != nil || len(items) != 3 || items[0].Revision != 3 {
		t.Fatalf("after_revision 分页错误: items=%+v err=%v", items, err)
	}
	if items, err = store.TaskComments().ListByRoot(ctx, "wi_cmt_page", 5, 50); err != nil || len(items) != 0 {
		t.Fatalf("越界分页应为空: items=%+v err=%v", items, err)
	}

	// append-only：仓储方法集不得出现 update/delete 写点（§5.2.1）。
	repoType := reflect.TypeOf(store.TaskComments())
	forbidden := []string{"Update", "Delete", "Remove", "Edit", "SetRevision"}
	for i := 0; i < repoType.NumMethod(); i++ {
		name := repoType.Method(i).Name
		for _, bad := range forbidden {
			if strings.Contains(strings.ToLower(name), strings.ToLower(bad)) {
				t.Fatalf("TaskCommentRepo 出现 append-only 违规方法 %q", name)
			}
		}
	}
	// 仓储零写点之外，revision 不可被绕过 cursor 复写：UPDATE 分配列后再次
	// Append 仍取 cursor 下一个值。
	if _, err := store.TaskComments().Append(ctx, &domain.TaskComment{
		WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_page", WorkItemID: "wi_cmt_page",
		Kind: domain.CommentNote, Body: "n6", ActorKind: domain.CommentActorUser,
		ActorID: "user_demo", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if latest, err := store.TaskComments().LatestRevision(ctx, "wi_cmt_page"); err != nil || latest != 6 {
		t.Fatalf("cursor 水位应=6: latest=%d err=%v", latest, err)
	}
}

func TestTaskCommentClientKeyReplayAndConflict(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	seedCommentTree(t, ctx, store, "ws_wk", "wi_cmt_idem")

	first := &domain.TaskComment{
		WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_idem", WorkItemID: "wi_cmt_idem",
		Kind: domain.CommentRequirement, Body: "同一段反馈",
		ActorKind: domain.CommentActorUser, ActorID: "user_demo",
		ClientKey: "feedback:1", CreatedAt: time.Now().UTC(),
	}
	created, err := store.TaskComments().Append(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.TaskComments().Append(ctx, &domain.TaskComment{
		WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_idem", WorkItemID: "wi_cmt_idem",
		Kind: domain.CommentRequirement, Body: "同一段反馈",
		ActorKind: domain.CommentActorUser, ActorID: "user_demo",
		ClientKey: "feedback:1", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != created.ID || replayed.Revision != created.Revision {
		t.Fatalf("同 client_key 同 body 应幂等重放原评论: first=%+v replay=%+v", created, replayed)
	}
	if _, err := store.TaskComments().Append(ctx, &domain.TaskComment{
		WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_idem", WorkItemID: "wi_cmt_idem",
		Kind: domain.CommentRequirement, Body: "不同的一段反馈",
		ActorKind: domain.CommentActorUser, ActorID: "user_demo",
		ClientKey: "feedback:1", CreatedAt: time.Now().UTC(),
	}); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("同 client_key 不同 body 应 ErrIdempotencyConflict，实际 %v", err)
	}
	latest, err := store.TaskComments().LatestRevision(ctx, "wi_cmt_idem")
	if err != nil || latest != 1 {
		t.Fatalf("幂等重放不得推进 cursor 水位: latest=%d err=%v", latest, err)
	}
}

func TestTaskCommentUnconsumedActionableQueries(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	seedCommentTree(t, ctx, store, "ws_wk", "wi_cmt_watermark")

	appendComment := func(kind domain.CommentKind, body string) *domain.TaskComment {
		t.Helper()
		c, err := store.TaskComments().Append(ctx, &domain.TaskComment{
			WorkspaceID: "ws_wk", RootWorkItemID: "wi_cmt_watermark", WorkItemID: "wi_cmt_watermark",
			Kind: kind, Body: body, ActorKind: domain.CommentActorUser, ActorID: "user_demo",
			CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	note := appendComment(domain.CommentNote, "仅备注")
	req := appendComment(domain.CommentRequirement, "需求变更")

	has, err := store.TaskComments().HasUnconsumedActionable(ctx, "wi_cmt_watermark", 0)
	if err != nil || !has {
		t.Fatalf("requirement 未消费应命中: has=%v err=%v", has, err)
	}
	// note 不算 actionable。
	all, err := store.TaskComments().ListUnconsumed(ctx, "wi_cmt_watermark", 0)
	if err != nil || len(all) != 2 || all[0].ID != note.ID || all[1].ID != req.ID {
		t.Fatalf("ListUnconsumed 应正序返回全部: %+v err=%v", all, err)
	}
	// 消费水位推进到 requirement revision 后不再命中。
	has, err = store.TaskComments().HasUnconsumedActionable(ctx, "wi_cmt_watermark", req.Revision)
	if err != nil || has {
		t.Fatalf("水位推进后应无未消费 actionable: has=%v err=%v", has, err)
	}
	if all, err = store.TaskComments().ListUnconsumed(ctx, "wi_cmt_watermark", req.Revision); err != nil || len(all) != 0 {
		t.Fatalf("水位推进后 ListUnconsumed 应为空: %+v err=%v", all, err)
	}
}

func TestTaskCommentAppendRejectsCrossWorkspaceScope(t *testing.T) {
	ctx := context.Background()
	db := openWakeupTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	seedWorkspace(t, db)
	// 第二个 workspace 的 work item 挂到 ws_wk 的 cursor 根 → 存储侧归属校验拒绝。
	now := time.Now().UTC()
	if _, err := db.Exec(
		`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at) VALUES ('ws_other','other','UTC',1,?,?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Create(ctx, &domain.WorkItem{ID: "wi_other_ws", WorkspaceID: "ws_other",
		RecordKind: domain.RecordKindTask, Title: "foreign", Status: domain.WorkItemTodo,
		Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seedCommentTree(t, ctx, store, "ws_wk", "wi_cmt_scope")
	if _, err := store.TaskComments().Append(ctx, &domain.TaskComment{
		WorkspaceID: "ws_other", RootWorkItemID: "wi_cmt_scope", WorkItemID: "wi_other_ws",
		Kind: domain.CommentNote, Body: "越界评论", ActorKind: domain.CommentActorUser,
		ActorID: "user_demo", CreatedAt: now,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("跨 workspace/root 归属评论应被存储侧拒绝，实际 %v", err)
	}
}
