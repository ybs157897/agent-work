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
	"github.com/ybs/agent-team-workbench/internal/scheduling"
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

// dispatchJoinDecisionForAcceptance 驱动首轮 Coordinator 决策 dispatch+join，
// 返回派生的 Worker Run（子任务处于 in_progress/execution 投影）。
func dispatchJoinDecisionForAcceptance(t *testing.T, ctx context.Context, svc *application.Service,
	dispatcher *captureDispatcher, workerID string) *domain.ExecutionRun {
	t.Helper()
	decision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"dispatch bounded work",` +
		`"next_action":"wait for settlement","steps":[{"verb":"dispatch","agent_id":"` + workerID +
		`","title":"work","instruction":"do work","acceptance":["done"]},{"verb":"join","children":"all"}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, dispatcher.runs[0].ID, decision)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("dispatch 决策必须创建恰好一个 Worker Run: runs=%d", len(dispatcher.runs))
	}
	return dispatcher.runs[1]
}

// consumeWorkerSettlementForAcceptance 消费 worker 终态后的 settlement 唤醒，
// 返回新一轮 summary Coordinator Run（治理 turn 2）。
func consumeWorkerSettlementForAcceptance(t *testing.T, ctx context.Context, svc *application.Service,
	store *sqlstore.Store, dispatcher *captureDispatcher) *domain.ExecutionRun {
	t.Helper()
	wakeups, err := store.Wakeups().DueTimers(ctx, time.Now().UTC().Add(time.Second), 20)
	if err != nil {
		t.Fatal(err)
	}
	var settlement *domain.WakeupRequest
	for index := range wakeups {
		if _, ok := wakeups[index].Context[domain.WakeupContextSettlementDispatchID].(string); ok {
			settlement = &wakeups[index]
			break
		}
	}
	if settlement == nil {
		t.Fatalf("worker settlement wakeup missing: %+v", wakeups)
	}
	scheduler := &scheduling.Scheduler{Store: store.Wakeups(), RunStarter: svc}
	if outcome, err := scheduler.ConsumeOne(ctx, *settlement, time.Now().UTC()); err != nil || outcome != scheduling.OutcomeConsumed {
		t.Fatalf("settlement wake failed: outcome=%s err=%v", outcome, err)
	}
	if len(dispatcher.runs) < 3 {
		t.Fatalf("settlement 必须创建 summary Coordinator Run: runs=%d", len(dispatcher.runs))
	}
	return dispatcher.runs[len(dispatcher.runs)-1]
}

// driveGovernedFinishEvaluationForAcceptance 在 summary turn 提交
// finish{evaluation:true}，跑通评估 verdict，把根任务送到待用户验收投影。
func driveGovernedFinishEvaluationForAcceptance(t *testing.T, ctx context.Context, svc *application.Service,
	store *sqlstore.Store, dispatcher *captureDispatcher, rootID string) *domain.WorkItem {
	t.Helper()
	summary := consumeWorkerSettlementForAcceptance(t, ctx, svc, store, dispatcher)
	finishDecision := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"all evidence is complete",` +
		`"next_action":"evaluate before user acceptance","steps":[{"verb":"finish","evaluation":true}]}`
	completeCoordinatorPlanDecision(t, ctx, svc, summary.ID, finishDecision)
	if len(dispatcher.runs) != 4 {
		t.Fatalf("finish{evaluation:true} 必须创建恰好一个评估 Run: runs=%d", len(dispatcher.runs))
	}
	evaluation := dispatcher.runs[3]
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
		t.Fatalf("前置条件：governed finish 必须把根任务送到待验收投影: %+v", root)
	}
	state, err := store.TaskCoordinators().GetState(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorWaitingUser {
		t.Fatalf("前置条件：Coordinator 必须 waiting_user: %+v", state)
	}
	return root
}

// TestPassedEvaluationWaitsForEveryDispatchSettlement 防回归：评估通过不能覆盖
// 仍为 running/collecting 的保险批。waiting_user 会让 settlement wake 在 preflight
// 被当作停止态 no-op，故 Coordinator 必须保持 running/settling 等待真正收口。
func TestPassedEvaluationWaitsForEveryDispatchSettlement(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "评估等待全部派发", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"所有 dispatch 已结算"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := dispatcher.runs[0]
	completeCoordinatorPlanDecision(t, ctx, svc, source.ID,
		`{"schema_version":"plan-decision/v2","kind":"plan","reason":"evidence ready","next_action":"evaluate","steps":[{"verb":"finish","evaluation":true}]}`)
	if len(dispatcher.runs) != 2 {
		t.Fatalf("finish{evaluation:true} 应创建评估 Run: %+v", dispatcher.runs)
	}
	openDispatch := &domain.Dispatch{
		ID: domain.NewID(domain.PrefixDispatch), WorkItemID: root.ID,
		Trigger: domain.DispatchTriggerLeadPlan, LeadRunID: source.ID,
		Status: domain.DispatchCollecting, CreatedAt: time.Now().UTC(),
	}
	if err := store.Dispatches().Create(ctx, openDispatch); err != nil {
		t.Fatal(err)
	}
	evaluation := dispatcher.runs[1]
	if err := svc.RecordRunStatus(ctx, evaluation.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, evaluation.ID,
		"评估通过。\n```verdict\n{\"pass\":true,\"reasons\":[\"证据满足\"]}\n```"); err != nil {
		t.Fatal(err)
	}
	state, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorRunning || state.Phase != "settling" ||
		state.CurrentAction != "等待派发批次收口" {
		t.Fatalf("未收口 dispatch 必须阻止 waiting_user: %+v", state)
	}
	openIDs, ok := state.Data["open_dispatch_ids"].([]any)
	if !ok || len(openIDs) != 1 || openIDs[0] != openDispatch.ID {
		t.Fatalf("Coordinator 必须固化未收口批次证据: %#v", state.Data["open_dispatch_ids"])
	}
}

// F4 防回归：dispatch 子任务 worker run succeeded 停在 review 投影后没有任何独立
// 完工路径（coordinated child 不能单独验收），根任务 AcceptWorkItem 必须在同一
// 事务内级联验收直系子任务，消除滞留 in_progress/review 的僵尸子任务。
func TestCoordinatedRootAcceptCascadesReviewChildren(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "级联验收根任务", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := dispatchJoinDecisionForAcceptance(t, ctx, svc, dispatcher, workerID)
	childID := worker.WorkItemID
	if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, worker.ID, "worker done"); err != nil {
		t.Fatal(err)
	}
	child, err := store.WorkItems().Get(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != domain.WorkItemInProgress || child.Phase != domain.PhaseReview {
		t.Fatalf("前置条件：worker 成功后子任务应停在 review 投影: %+v", child)
	}
	root = driveGovernedFinishEvaluationForAcceptance(t, ctx, svc, store, dispatcher, root.ID)
	accepted, err := svc.AcceptWorkItem(ctx, root.ID, root.Version)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != domain.WorkItemCompleted {
		t.Fatalf("根任务验收后应 completed: %+v", accepted)
	}
	child, err = store.WorkItems().Get(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != domain.WorkItemCompleted || child.Phase != "" {
		t.Fatalf("根验收必须级联完工 review 子任务: %+v", child)
	}
	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	childCompleted := 0
	for _, event := range events {
		if event.Type == domain.EventWorkItemCompleted && event.AggregateID == childID {
			childCompleted++
		}
	}
	if childCompleted != 1 {
		t.Fatalf("级联验收必须为子任务恰好写一条 completed 事件: %d", childCompleted)
	}
}

// F4 防回归：仍在执行（phase=execution）的子任务不被级联关闭，保持其活跃投影；
// 只有 review/acceptance 投影的直系子任务随根验收完工。
func TestCoordinatedRootAcceptSkipsExecutingChildren(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "执行中子任务不级联", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"test task acceptance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := dispatchJoinDecisionForAcceptance(t, ctx, svc, dispatcher, workerID)
	childID := worker.WorkItemID
	if err := svc.RecordRunStatus(ctx, worker.ID, domain.RunStarting, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, svc, worker.ID, "worker done"); err != nil {
		t.Fatal(err)
	}
	// 直接子任务之外再造一个仍在 execution 投影的直系子任务（活跃 worker 场景的
	// 等价投影），级联必须跳过它。
	now := time.Now().UTC()
	executing := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID, RecordKind: domain.RecordKindTask,
		ParentID: root.ID, Title: "仍在执行的子任务", Status: domain.WorkItemInProgress,
		Phase: domain.PhaseExecution, PhaseEnteredAt: &now,
		Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, executing); err != nil {
		t.Fatal(err)
	}
	root = driveGovernedFinishEvaluationForAcceptance(t, ctx, svc, store, dispatcher, root.ID)
	accepted, err := svc.AcceptWorkItem(ctx, root.ID, root.Version)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != domain.WorkItemCompleted {
		t.Fatalf("根任务验收后应 completed: %+v", accepted)
	}
	child, err := store.WorkItems().Get(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != domain.WorkItemCompleted {
		t.Fatalf("review 子任务必须随根验收级联完工: %+v", child)
	}
	executing, err = store.WorkItems().Get(ctx, executing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if executing.Status != domain.WorkItemInProgress || executing.Phase != domain.PhaseExecution {
		t.Fatalf("execution 子任务不得被级联关闭: %+v", executing)
	}
	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == domain.EventWorkItemCompleted && event.AggregateID == executing.ID {
			t.Fatalf("execution 子任务不得写 completed 事件: %+v", event)
		}
	}
}
