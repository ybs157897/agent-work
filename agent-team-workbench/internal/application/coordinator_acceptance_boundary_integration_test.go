package application_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func prepareWorkItemForCoordinatorAcceptance(t *testing.T, ctx context.Context, store *sqlstore.Store, rootID string) *domain.WorkItem {
	t.Helper()
	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.EnterReview(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, root, root.Version-1); err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.EnterAcceptance(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, root, root.Version-1); err != nil {
		t.Fatal(err)
	}
	return root
}

func setCoordinatorWaitingUser(t *testing.T, ctx context.Context, store *sqlstore.Store, rootID string) *domain.TaskCoordinatorState {
	t.Helper()
	state, err := store.TaskCoordinators().GetState(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	expected := state.Version
	state.Status = domain.CoordinatorWaitingUser
	state.Phase = "acceptance"
	state.CurrentRunID = ""
	state.CurrentAction = "等待用户验收"
	if err := store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		t.Fatal(err)
	}
	state, err = store.TaskCoordinators().GetState(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// prepareValidatedCoordinatorAcceptance drives the real governed finish path:
// the Coordinator submits finish{evaluation:true}, the evaluation Run emits a
// passing verdict, and only then is the root left in the human acceptance
// projection. Acceptance tests must carry this evidence because the finish
// gate intentionally rejects hand-written waiting_user projections.
func prepareValidatedCoordinatorAcceptance(t *testing.T, ctx context.Context, svc *application.Service,
	store *sqlstore.Store, dispatcher *captureDispatcher, rootID, workerID string) *domain.WorkItem {
	t.Helper()
	if len(dispatcher.runs) == 0 {
		t.Fatal("Coordinator source Run missing")
	}
	source := dispatcher.runs[0]
	markCompilerSourceSucceeded(t, ctx, store, source.ID)
	source, err := store.Runs().Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	decision := compilerDecision(domain.PlanVerbFinish, workerID)
	evaluate := true
	decision.Steps[0].Finish.Evaluation = &evaluate
	plan, err := svc.SubmitGovernedTodoPlanDecision(ctx, source, decision, application.PlanCandidateNativeText)
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || plan.Status != domain.PlanFinished {
		t.Fatalf("governed finish Plan must be finished before acceptance: %+v", plan)
	}
	if len(dispatcher.runs) != 2 {
		t.Fatalf("governed finish must create exactly one evaluation Run: %d", len(dispatcher.runs))
	}
	evaluation := dispatcher.runs[1]
	if err := svc.RecordRunStatus(ctx, evaluation.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, evaluation.ID,
		"评估通过。\n```verdict\n{\"pass\":true,\"reasons\":[\"验收标准已满足\"]}\n```"); err != nil {
		t.Fatal(err)
	}
	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if root.Status != domain.WorkItemInProgress || root.Phase != domain.PhaseAcceptance {
		t.Fatalf("通过的 governed finish 必须进入待验收投影: %+v", root)
	}
	state, err := store.TaskCoordinators().GetState(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorWaitingUser {
		t.Fatalf("通过的 governed finish 必须让 Coordinator waiting_user: %+v", state)
	}
	return root
}

func TestChatRejectsCoordinatorContextsEvenWithSystemAgent(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	config, err := store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "独立 Chat", RecordKind: domain.RecordKindChat, AgentProfileID: workerID,
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []application.CreateRunParams{
		{AgentProfileID: config.AgentProfileID, Instruction: "不应作为 Coordinator 运行",
			CoordinatorContext: map[string]any{"role": "coordinator"}},
		{AgentProfileID: config.AgentProfileID, Instruction: "不应携带 wake context",
			WakeContext: map[string]any{"source": "task"}},
		{AgentProfileID: config.AgentProfileID, Instruction: "system Coordinator 不能进入 Chat"},
	}
	for _, p := range cases {
		if _, err := svc.CreateRun(ctx, chat.ID, p); !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("Chat 使用 system Coordinator/context 应拒绝为 ErrValidation，实际 %v", err)
		}
	}
	runs, err := store.Runs().ListByWorkItem(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("被拒绝的 Chat Coordinator run 不得持久化: %+v", runs)
	}
}

func TestCoordinatedRootCreateRunRequiresInternalCoordinatorAdmission(t *testing.T) {
	ctx, db, svc, store, dispatcher, wsID, _ := seedCoordinatorEnvWithDatabase(t)
	defer db.Close()
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "protected coordinator entry", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"public callers cannot mint a root Coordinator Run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	currentRuns := len(dispatcher.runs)
	cases := []application.CreateRunParams{
		{AgentProfileID: config.AgentProfileID, Instruction: "forged coordinator",
			CoordinatorContext: map[string]any{
				"role": "coordinator", "root_work_item_id": root.ID, "state_id": state.ID,
				"action": "intake", "attempt": state.Attempt + 1,
			}},
		{AgentProfileID: config.AgentProfileID, Instruction: "forged wake",
			WakeContext: map[string]any{"source": "automation"}},
	}
	for _, p := range cases {
		if _, err := svc.CreateRun(ctx, root.ID, p); !errors.Is(err, domain.ErrValidation) && !errors.Is(err, domain.ErrStateConflict) {
			t.Fatalf("public coordinated root entry must fail closed: %v", err)
		}
	}
	if len(dispatcher.runs) != currentRuns {
		t.Fatalf("rejected root entries must not create Runs: got=%d want=%d", len(dispatcher.runs), currentRuns)
	}
}

func TestCoordinatedRootAcceptRequiresWaitingUser(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "尚未交付", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareWorkItemForCoordinatorAcceptance(t, ctx, store, root.ID)
	stateBefore, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptWorkItem(ctx, root.ID, root.Version); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("Coordinator 非 waiting_user 时验收应拒绝为 ErrStateConflict，实际 %v", err)
	}
	rootAfter, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rootAfter.Status != domain.WorkItemInProgress || rootAfter.Phase != domain.PhaseAcceptance ||
		stateAfter.Status != stateBefore.Status || stateAfter.Version != stateBefore.Version {
		t.Fatalf("非 waiting_user 验收不得改变根状态: root=%+v state=%+v", rootAfter, stateAfter)
	}
}

func TestCoordinatedRootAcceptCommitsWorkItemAndCoordinatorAtomically(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "待验收", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)
	eventsBefore, err := store.TaskCoordinators().ListEvents(ctx, root.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	completedBefore := 0
	for _, event := range eventsBefore {
		if event.Kind == domain.EventCoordinatorCompleted {
			completedBefore++
		}
	}

	accepted, err := svc.AcceptWorkItem(ctx, root.ID, root.Version)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != domain.WorkItemCompleted {
		t.Fatalf("根 Task 验收后应 completed: %+v", accepted)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorCompleted {
		t.Fatalf("根 Coordinator 验收后应 completed: %+v", state)
	}
	events, err := store.TaskCoordinators().ListEvents(ctx, root.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, event := range events {
		if event.Kind == domain.EventCoordinatorCompleted {
			completed++
		}
	}
	if completed != completedBefore+1 {
		t.Fatalf("人工验收应恰新增一条 Coordinator completed 事件：before=%d after=%d events=%+v", completedBefore, completed, events)
	}
}

type failOnceCoordinatorRepo struct {
	application.TaskCoordinatorRepo
	err  error
	once sync.Once
}

func (r *failOnceCoordinatorRepo) UpdateState(ctx context.Context, state *domain.TaskCoordinatorState, expectedVersion int) error {
	failed := false
	r.once.Do(func() { failed = true })
	if failed {
		return r.err
	}
	return r.TaskCoordinatorRepo.UpdateState(ctx, state, expectedVersion)
}

type coordinatorFaultStore struct {
	*sqlstore.Store
	coordinators application.TaskCoordinatorRepo
}

func (s *coordinatorFaultStore) TaskCoordinators() application.TaskCoordinatorRepo {
	return s.coordinators
}

func TestCoordinatedRootAcceptCASFailureRollsBackBothProjections(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "CAS 故障注入", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)
	eventsBefore, err := store.TaskCoordinators().ListEvents(ctx, root.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected coordinator CAS failure")
	faultStore := &coordinatorFaultStore{
		Store: store,
		coordinators: &failOnceCoordinatorRepo{
			TaskCoordinatorRepo: store.TaskCoordinators(), err: injected,
		},
	}
	faultSvc := application.NewService(faultStore, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	if _, err := faultSvc.AcceptWorkItem(ctx, root.ID, root.Version); !errors.Is(err, injected) {
		t.Fatalf("Coordinator CAS 故障应返回注入错误，实际 %v", err)
	}

	rootAfter, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rootAfter.Status != domain.WorkItemInProgress || rootAfter.Phase != domain.PhaseAcceptance ||
		stateAfter.Status != domain.CoordinatorWaitingUser || stateAfter.Version != stateBefore.Version {
		t.Fatalf("Coordinator CAS 失败必须回滚 WorkItem 与 state: root=%+v state=%+v before=%+v", rootAfter, stateAfter, stateBefore)
	}
	events, err := store.TaskCoordinators().ListEvents(ctx, root.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(eventsBefore) {
		t.Fatalf("CAS 失败不得追加 Coordinator 事件：before=%d after=%d events=%+v", len(eventsBefore), len(events), events)
	}
	stream, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range stream {
		if event.Type == domain.EventWorkItemCompleted && event.AggregateID == root.ID {
			t.Fatalf("CAS 失败不得写 WorkItem completed 事件: %+v", event)
		}
	}
}

func TestConcurrentCoordinatedRootAcceptHasOneWinner(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "并发验收", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)

	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.AcceptWorkItem(ctx, root.ID, root.Version)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, domain.ErrStateConflict) && !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("并发验收失败应为状态/CAS 冲突，实际 %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("并发验收应恰有一个成功者，实际 %d", winners)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorCompleted {
		t.Fatalf("并发验收最终 Coordinator 应 completed: %+v", state)
	}
}

func TestTerminalTaskCannotCreateChildOrRequeueCoordinator(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "已验收根任务", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)
	if _, err := svc.AcceptWorkItem(ctx, root.ID, root.Version); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "不应创建", ParentID: root.ID, RecordKind: domain.RecordKindTask,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("已 completed 的 Task parent 创建子项应拒绝为 ErrValidation，实际 %v", err)
	}
	stateAfter, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stateAfter.Status != domain.CoordinatorCompleted || stateAfter.Version != stateBefore.Version {
		t.Fatalf("拒绝终态 parent 子任务不得重新排队 Coordinator: before=%+v after=%+v", stateBefore, stateAfter)
	}
	children, err := store.WorkItems().ListByParent(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 0 {
		t.Fatalf("终态 parent 拒绝后不得留下子任务: %+v", children)
	}

	now := time.Now().UTC()
	cancelled := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
		Title: "已取消任务", Status: domain.WorkItemCancelled, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "不应挂到 cancelled", ParentID: cancelled.ID, RecordKind: domain.RecordKindTask,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("cancelled Task parent 创建子项应拒绝为 ErrValidation，实际 %v", err)
	}

	// Fail closed even if an older partial acceptance left the WorkItem active
	// while its Coordinator state was already completed.
	partial := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
		Title: "状态不一致的根任务", Status: domain.WorkItemInProgress, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, partial); err != nil {
		t.Fatal(err)
	}
	config, err := store.TaskCoordinators().GetConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TaskCoordinators().CreateState(ctx, &domain.TaskCoordinatorState{
		ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: wsID,
		RootWorkItemID: partial.ID, CoordinatorAgentID: config.AgentProfileID,
		Status: domain.CoordinatorCompleted, Phase: "acceptance",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "不应重新排队", ParentID: partial.ID, RecordKind: domain.RecordKindTask,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("Coordinator completed 即使 WorkItem 未终态也应拒绝子项，实际 %v", err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, partial.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorCompleted {
		t.Fatalf("拒绝子项不得把不一致的 Coordinator state 重排: %+v", state)
	}
}
