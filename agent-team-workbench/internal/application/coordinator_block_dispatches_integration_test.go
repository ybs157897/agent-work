package application_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func createOpenCoordinatorBlockDispatch(t *testing.T, ctx context.Context, store application.Store,
	workItemID string, createdAt time.Time) *domain.Dispatch {
	t.Helper()
	dispatch := &domain.Dispatch{
		ID: domain.NewID(domain.PrefixDispatch), WorkItemID: workItemID,
		Trigger: domain.DispatchTriggerWakeup, Status: domain.DispatchRunning,
		CreatedAt: createdAt,
	}
	if err := store.Dispatches().Create(ctx, dispatch); err != nil {
		t.Fatal(err)
	}
	return dispatch
}

func createCoordinatorBlockForeignRoot(t *testing.T, ctx context.Context, store application.Store,
	workspaceID, title string, createdAt time.Time) *domain.WorkItem {
	t.Helper()
	root := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: workspaceID,
		RecordKind: domain.RecordKindTask, Title: title, Status: domain.WorkItemTodo,
		Priority: domain.PriorityMedium, Version: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := store.WorkItems().Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	return root
}

func coordinatorBlockPlanWithTwoWorkers(workerID string) string {
	return fmt.Sprintf(`{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch two workers","next_action":"wait for both workers","steps":[{"verb":"dispatch","agent_id":"%s","title":"A","instruction":"执行 A","acceptance":["A 完成并可验证"]},{"verb":"dispatch","agent_id":"%s","title":"B","instruction":"执行 B","acceptance":["B 完成并可验证"]},{"verb":"join","children":"all"}]}`, workerID, workerID)
}

func startCoordinatorBlockWorker(t *testing.T, ctx context.Context, svc *application.Service, runID string) {
	t.Helper()
	for _, status := range []domain.RunStatus{domain.RunStarting, domain.RunRunning} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func finishRunningCoordinatorBlockWorker(t *testing.T, ctx context.Context, svc *application.Service, runID string) {
	t.Helper()
	if err := svc.RecordRunEvent(ctx, runID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": "迟到的兄弟结果"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, runID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func dispatchUpdateCount(events []*domain.CanonicalEvent, dispatchID string) int {
	count := 0
	for _, event := range events {
		if event != nil && event.Type == domain.EventDispatchUpdated && event.Aggregate.ID == dispatchID {
			count++
		}
	}
	return count
}

func TestCoordinatorBlockClosesEveryOpenRootDispatchWithoutCrossScope(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "Coordinator blocker dispatch scope", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"both sibling dispatches stop on a root blocker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	startCoordinatorBlockWorker(t, ctx, svc, coordinatorRun.ID)
	if err := svc.RecordRunEvent(ctx, coordinatorRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": coordinatorBlockPlanWithTwoWorkers(workerID)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("expected one Coordinator and two Worker runs, got %d", len(dispatcher.runs))
	}
	workerA, workerB := dispatcher.runs[1], dispatcher.runs[2]
	if workerA.DispatchID == "" || workerA.DispatchID != workerB.DispatchID {
		t.Fatalf("one Plan's sibling Workers must share one dispatch: A=%q B=%q", workerA.DispatchID, workerB.DispatchID)
	}
	rootDispatches, err := store.Dispatches().ListByWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootDispatches) != 1 || rootDispatches[0].ID != workerA.DispatchID {
		t.Fatalf("unexpected initial root dispatches: %+v", rootDispatches)
	}

	// A root can accumulate another open dispatch across independent turns. The
	// blocker must sweep both root-owned batches, not just the failed member's
	// batch. The foreign roots below pin the scope boundary explicitly.
	secondRootDispatch := createOpenCoordinatorBlockDispatch(t, ctx, store, root.ID,
		time.Now().UTC().Add(time.Millisecond))
	sameWorkspaceForeignRoot := createCoordinatorBlockForeignRoot(t, ctx, store, wsID,
		"same workspace foreign root", time.Now().UTC().Add(2*time.Millisecond))
	sameWorkspaceForeignDispatch := createOpenCoordinatorBlockDispatch(t, ctx, store,
		sameWorkspaceForeignRoot.ID, time.Now().UTC().Add(3*time.Millisecond))
	foreignWorkspaceID := "ws_coordinator_block_foreign"
	if err := store.Workspaces().Create(ctx, &domain.Workspace{
		ID: foreignWorkspaceID, Name: "foreign", Timezone: "UTC", Version: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	foreignWorkspaceRoot := createCoordinatorBlockForeignRoot(t, ctx, store, foreignWorkspaceID,
		"foreign workspace root", time.Now().UTC().Add(4*time.Millisecond))
	foreignWorkspaceDispatch := createOpenCoordinatorBlockDispatch(t, ctx, store,
		foreignWorkspaceRoot.ID, time.Now().UTC().Add(5*time.Millisecond))

	beforeSeq, err := store.Events().LatestSeq(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	startCoordinatorBlockWorker(t, ctx, svc, workerA.ID)
	startCoordinatorBlockWorker(t, ctx, svc, workerB.ID)
	if err := svc.RecordRunStatus(ctx, workerA.ID, domain.RunFailed, map[string]any{
		"code": "permission_denied", "message": "worker A lacks required permission", "retryable": false,
	}); err != nil {
		t.Fatal(err)
	}

	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootAfter, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked || rootAfter.Status != domain.WorkItemBlocked {
		t.Fatalf("non-retryable sibling failure must block root control line: state=%+v root=%+v", state, rootAfter)
	}
	for _, dispatchID := range []string{workerA.DispatchID, secondRootDispatch.ID} {
		dispatch, getErr := store.Dispatches().Get(ctx, dispatchID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if dispatch.Status != domain.DispatchDegraded || dispatch.ClosedAt == nil {
			t.Fatalf("root blocker must close every open root dispatch: %+v", dispatch)
		}
	}
	workerBAfter, err := store.Runs().Get(ctx, workerB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workerBAfter.Status != domain.RunRunning {
		t.Fatalf("the sibling Worker should remain running while the blocker closes its dispatch: %+v", workerBAfter)
	}
	for _, foreign := range []*domain.Dispatch{sameWorkspaceForeignDispatch, foreignWorkspaceDispatch} {
		dispatch, getErr := store.Dispatches().Get(ctx, foreign.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if dispatch.Status != domain.DispatchRunning || dispatch.ClosedAt != nil {
			t.Fatalf("root blocker crossed dispatch scope: %+v", dispatch)
		}
	}

	events, err := store.Events().Since(ctx, wsID, beforeSeq, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := dispatchUpdateCount(events, workerA.DispatchID); got != 1 {
		t.Fatalf("shared Worker dispatch must emit one degraded update, got %d", got)
	}
	if got := dispatchUpdateCount(events, secondRootDispatch.ID); got != 1 {
		t.Fatalf("second root dispatch must emit one degraded update, got %d", got)
	}
	if got := dispatchUpdateCount(events, sameWorkspaceForeignDispatch.ID); got != 0 {
		t.Fatalf("same-workspace foreign dispatch must not emit a blocker update, got %d", got)
	}

	// A late terminal sibling and a recovery-loop replay are both harmless after
	// the CAS sweep: they must not revive a root dispatch or append a second
	// dispatch.updated event.
	finishRunningCoordinatorBlockWorker(t, ctx, svc, workerB.ID)
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	events, err = store.Events().Since(ctx, wsID, beforeSeq, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := dispatchUpdateCount(events, workerA.DispatchID); got != 1 {
		t.Fatalf("replayed/late terminal must not duplicate shared dispatch update, got %d", got)
	}
	if got := dispatchUpdateCount(events, secondRootDispatch.ID); got != 1 {
		t.Fatalf("replayed/late terminal must not duplicate second dispatch update, got %d", got)
	}
	for _, dispatchID := range []string{workerA.DispatchID, secondRootDispatch.ID} {
		dispatch, getErr := store.Dispatches().Get(ctx, dispatchID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if dispatch.Status != domain.DispatchDegraded {
			t.Fatalf("replay must preserve degraded root dispatch: %+v", dispatch)
		}
	}
}

func TestCoordinatorBlockConcurrentWorkerFailuresCloseSharedDispatchOnce(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "concurrent Coordinator blocker dispatch", RecordKind: domain.RecordKindTask,
		AutoCoordinate: true, AcceptanceCriteria: []string{"concurrent sibling failures close the shared batch once"},
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinatorRun := dispatcher.runs[0]
	startCoordinatorBlockWorker(t, ctx, svc, coordinatorRun.ID)
	if err := svc.RecordRunEvent(ctx, coordinatorRun.ID, domain.EventMessageCompleted,
		map[string]any{"role": "assistant", "text": coordinatorBlockPlanWithTwoWorkers(workerID)}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.RunStatus{domain.RunSucceeding, domain.RunSucceeded} {
		if err := svc.RecordRunStatus(ctx, coordinatorRun.ID, status, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(dispatcher.runs) != 3 {
		t.Fatalf("expected one Coordinator and two Worker runs, got %d", len(dispatcher.runs))
	}
	workerA, workerB := dispatcher.runs[1], dispatcher.runs[2]
	if workerA.DispatchID == "" || workerA.DispatchID != workerB.DispatchID {
		t.Fatalf("one Plan's sibling Workers must share one dispatch: A=%q B=%q", workerA.DispatchID, workerB.DispatchID)
	}
	startCoordinatorBlockWorker(t, ctx, svc, workerA.ID)
	startCoordinatorBlockWorker(t, ctx, svc, workerB.ID)
	beforeSeq, err := store.Events().LatestSeq(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, runID := range []string{workerA.ID, workerB.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			errs <- svc.RecordRunStatus(ctx, id, domain.RunFailed, map[string]any{
				"code": "permission_denied", "message": "concurrent worker failure", "retryable": false,
			})
		}(runID)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent sibling terminal handling failed: %v", err)
		}
	}

	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorBlocked {
		t.Fatalf("concurrent non-retryable failures must block Coordinator: %+v", state)
	}
	dispatch, err := store.Dispatches().Get(ctx, workerA.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if dispatch.Status != domain.DispatchDegraded || dispatch.ClosedAt == nil {
		t.Fatalf("concurrent blocker must close the shared dispatch: %+v", dispatch)
	}
	events, err := store.Events().Since(ctx, wsID, beforeSeq, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := dispatchUpdateCount(events, workerA.DispatchID); got != 1 {
		t.Fatalf("CAS dispatch closure must emit exactly one update under concurrency, got %d", got)
	}
}
