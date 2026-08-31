// tasklock_integration_test.go F1 任务级执行锁防回归：run 进 running 获取锁、
// 双跑被拒落 failed(work_item_locked)、死属主锁可抢占、终态释放、回收扫描语义。
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

// seedTaskLockEnv 建 workspace + agent + binding，返回 (service, store, wiID)。
func seedTaskLockEnv(t *testing.T) (*application.Service, *sqlstore.Store, string) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	now := time.Now().UTC()
	if err := store.Workspaces().Create(ctx, &domain.Workspace{ID: "ws_lock", Name: "lock", Timezone: "UTC", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	seedCtx(t, store, ctx, "ws_lock")
	if err := store.Agents().Create(ctx, &domain.AgentProfile{
		ID: "agent_lock", WorkspaceID: "ws_lock", Name: "LK", Role: "developer",
		Availability: domain.AgentEnabled, Presence: domain.PresenceIdle,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Bindings().Create(ctx, &domain.RuntimeBinding{
		ID: "rb_lock", WorkspaceID: "ws_lock", RuntimeLabel: "codex_local", AdapterID: "codex-appserver",
		Capabilities: map[string]string{}, Status: domain.BindingReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	wi, err := svc.CreateWorkItem(ctx, "ws_lock", application.CreateWorkItemParams{Title: "锁语义任务", AgentProfileID: "agent_lock"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, wi.ID
}

// newQueuedRun 建一个 queued run（captureDispatcher 不产生副作用）。
func newQueuedRun(t *testing.T, svc *application.Service, wiID string) *domain.ExecutionRun {
	t.Helper()
	r, err := svc.CreateRun(context.Background(), wiID, application.CreateRunParams{
		AgentProfileID: "agent_lock", Instruction: "干活",
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// runToRunning 按状态机推进 queued → starting → running（ModuleRunner 同路径）。
func runToRunning(t *testing.T, svc *application.Service, runID string) {
	t.Helper()
	ctx := context.Background()
	for _, to := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, runID, to, nil); err != nil {
			t.Fatalf("run %s 推进 %s 失败: %v", runID, to, err)
		}
	}
}

func mustLockHolder(t *testing.T, store *sqlstore.Store, wiID string) (string, *time.Time) {
	t.Helper()
	wi, err := store.WorkItems().Get(context.Background(), wiID)
	if err != nil {
		t.Fatal(err)
	}
	return wi.LockedByRunID, wi.LockedAt
}

// TestTaskExecutionLockLifecycle 锁生命周期主链路：
// 获取（首进 running）→ 拒双跑（第二个 run 落 failed(work_item_locked)）→
// waiting_approval 往返幂等 → 终态释放 → 死属主后新 run 可重新获取。
func TestTaskExecutionLockLifecycle(t *testing.T) {
	ctx := context.Background()
	svc, store, wiID := seedTaskLockEnv(t)

	runA := newQueuedRun(t, svc, wiID)
	runToRunning(t, svc, runA.ID)
	if holder, lockedAt := mustLockHolder(t, store, wiID); holder != runA.ID || lockedAt == nil {
		t.Fatalf("runA 进 running 应获取锁: holder=%q lockedAt=%v", holder, lockedAt)
	}

	// 双跑：runB 对同任务进 running 被拒，直接落 failed(work_item_locked) 终态。
	runB := newQueuedRun(t, svc, wiID)
	runToRunning(t, svc, runB.ID)
	gotB, err := store.Runs().Get(ctx, runB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Status != domain.RunFailed || gotB.Failure == nil || gotB.Failure.Code != "work_item_locked" {
		t.Fatalf("runB 应 failed(work_item_locked): status=%s failure=%+v", gotB.Status, gotB.Failure)
	}
	if holder, _ := mustLockHolder(t, store, wiID); holder != runA.ID {
		t.Fatalf("runB 失败释放不得误伤 runA 的锁: holder=%q", holder)
	}

	// waiting_approval 往返后再进 running：锁仍归本 run，幂等通过。
	if err := svc.RecordRunStatus(ctx, runA.ID, domain.RunWaitingApproval, nil); err != nil {
		t.Fatalf("runA 进 waiting_approval 应成功: %v", err)
	}
	if err := svc.RecordRunStatus(ctx, runA.ID, domain.RunRunning, nil); err != nil {
		t.Fatalf("runA 恢复 running 应成功: %v", err)
	}
	if holder, _ := mustLockHolder(t, store, wiID); holder != runA.ID {
		t.Fatalf("waiting_approval 往返后锁应保持: holder=%q", holder)
	}

	// 终态释放（succeeded 需经 succeeding 中间态，状态机既有约定）。
	for _, to := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, runA.ID, to, nil); err != nil {
			t.Fatalf("runA 推进 %s 应成功: %v", to, err)
		}
	}
	if holder, lockedAt := mustLockHolder(t, store, wiID); holder != "" || lockedAt != nil {
		t.Fatalf("runA 终态应释放锁: holder=%q lockedAt=%v", holder, lockedAt)
	}

	// 属主已终态：新 run 正常重新获取（无锁路径）。
	runC := newQueuedRun(t, svc, wiID)
	runToRunning(t, svc, runC.ID)
	if holder, _ := mustLockHolder(t, store, wiID); holder != runC.ID {
		t.Fatalf("runC 应获取锁: holder=%q", holder)
	}
}

// TestTaskExecutionLockPreemptsDeadOwner 死属主抢占：终态 run 残留的锁被下一个
// run 覆写，且抢占记 activity（属主活性只看 run 终态面）。
func TestTaskExecutionLockPreemptsDeadOwner(t *testing.T) {
	ctx := context.Background()
	svc, store, wiID := seedTaskLockEnv(t)

	// 制造「锁属主已终态」的残留：runA 正常终态后由仓储直写覆锁（模拟绕过
	// 状态机的异常残留——正常终态路径在同事务内已释放）。
	runA := newQueuedRun(t, svc, wiID)
	runToRunning(t, svc, runA.ID)
	if err := svc.RecordRunStatus(ctx, runA.ID, domain.RunFailed, nil); err != nil {
		t.Fatal(err)
	}
	wi, err := store.WorkItems().Get(ctx, wiID)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Hour)
	expected := wi.Version
	wi.LockedByRunID, wi.LockedAt, wi.Version = runA.ID, &old, wi.Version+1
	if err := store.WorkItems().Update(ctx, wi, expected); err != nil {
		t.Fatal(err)
	}

	// runB 进 running：抢占成功（不落 failed），锁属主易主，activity 记录抢占。
	runB := newQueuedRun(t, svc, wiID)
	runToRunning(t, svc, runB.ID)
	gotB, err := store.Runs().Get(ctx, runB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Status != domain.RunRunning {
		t.Fatalf("runB 应保持 running: %s", gotB.Status)
	}
	if holder, _ := mustLockHolder(t, store, wiID); holder != runB.ID {
		t.Fatalf("锁应易主到 runB: holder=%q", holder)
	}
	activities, err := store.Events().ListActivities(ctx, wi.WorkspaceID, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range activities {
		if a.Kind == "work_item.lock_preempted" && strings.Contains(a.Message, "抢占") {
			found = true
		}
	}
	if !found {
		t.Fatalf("抢占应记 activity: %+v", activities)
	}
}

// TestReleaseStaleLocksSweepsDeadOwnerLocks 回收扫描 SQL 语义：
// 只清「locked_at 超阈值 且 属主 run 已终态」的锁；属主活跃的锁与未超龄的死锁不动。
func TestReleaseStaleLocksSweepsDeadOwnerLocks(t *testing.T) {
	ctx := context.Background()
	svc, store, _ := seedTaskLockEnv(t)

	newLockedTask := func(title string, runStatus domain.RunStatus, age time.Duration) (string, string) {
		t.Helper()
		wi, err := svc.CreateWorkItem(ctx, "ws_lock", application.CreateWorkItemParams{Title: title, AgentProfileID: "agent_lock"})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.CreateRun(ctx, wi.ID, application.CreateRunParams{AgentProfileID: "agent_lock", Instruction: title})
		if err != nil {
			t.Fatal(err)
		}
		if runStatus == domain.RunRunning {
			runToRunning(t, svc, run.ID)
		} else if runStatus != domain.RunQueued {
			if err := svc.RecordRunStatus(ctx, run.ID, runStatus, nil); err != nil {
				t.Fatal(err)
			}
		}
		return wi.ID, run.ID
	}
	forceLock := func(wiID, runID string, age time.Duration) {
		t.Helper()
		wi, err := store.WorkItems().Get(ctx, wiID)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Add(-age)
		expected := wi.Version
		wi.LockedByRunID, wi.LockedAt, wi.Version = runID, &at, wi.Version+1
		if err := store.WorkItems().Update(ctx, wi, expected); err != nil {
			t.Fatal(err)
		}
	}

	wiOld, runOld := newLockedTask("死锁超龄", domain.RunFailed, 0)      // 属主终态 + 超龄 → 应回收
	wiFresh, runFresh := newLockedTask("死锁未超龄", domain.RunFailed, 0) // 属主终态 + 新鲜 → 保留
	wiAlive, runAlive := newLockedTask("活锁超龄", domain.RunRunning, 0) // 属主 running + 超龄 → 保留
	forceLock(wiOld, runOld, time.Hour)
	forceLock(wiFresh, runFresh, 5*time.Minute)
	forceLock(wiAlive, runAlive, time.Hour)

	released, err := store.WorkItems().ReleaseStaleLocks(ctx, time.Now().UTC().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("应只回收 1 个死属主锁: %d", released)
	}
	for _, tc := range []struct {
		wiID, want string
		desc       string
	}{
		{wiOld, "", "超龄死锁应被回收"},
		{wiFresh, runFresh, "未超龄死锁应保留"},
		{wiAlive, runAlive, "属主活跃的锁应保留"},
	} {
		if holder, _ := mustLockHolder(t, store, tc.wiID); holder != tc.want {
			t.Fatalf("%s: holder=%q want=%q", tc.desc, holder, tc.want)
		}
	}
}
