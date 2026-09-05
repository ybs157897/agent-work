package application_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestReviewAvailabilityRequiresCoordinatorUserCheckpoint(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "等待 Coordinator", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"根任务必须等待真实控制线"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareWorkItemForCoordinatorAcceptance(t, ctx, store, root.ID)

	availability, err := svc.ReviewAvailability(ctx, root, application.ReviewAvailabilityPermissions{
		CanApprove: true, CanWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if availability.Ready || availability.CanAccept || availability.CanReturn ||
		!strings.Contains(availability.AcceptReason, "Coordinator") {
		t.Fatalf("Coordinator 非 waiting_user 时不得暴露可操作验收：%+v", availability)
	}

	setCoordinatorWaitingUser(t, ctx, store, root.ID)
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	availability, err = svc.ReviewAvailability(ctx, root, application.ReviewAvailabilityPermissions{
		CanApprove: true, CanWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !availability.Ready || availability.CanAccept || !availability.CanReturn ||
		availability.AcceptReason == "" {
		t.Fatalf("waiting_user 但当前 Todo 没有 admitted turn 时应仅允许打回：%+v", availability)
	}
}

func TestReviewAvailabilityRequiresCurrentCanonicalValidation(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "规范验证门禁", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"规范验证必须通过"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)

	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal.AcceptanceContract[0] = "验收标准已被修改"
	goal.Version++
	goal.UpdatedAt = time.Now().UTC()
	if err := store.Goals().Update(ctx, goal, goal.Version-1); err != nil {
		t.Fatal(err)
	}

	availability, err := svc.ReviewAvailability(ctx, root, application.ReviewAvailabilityPermissions{
		CanApprove: true, CanWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !availability.Ready || availability.CanAccept || !availability.CanReturn ||
		!strings.Contains(availability.AcceptReason, "旧版验收标准") {
		t.Fatalf("canonical criteria digest 过期时应阻止验收但保留打回：%+v", availability)
	}
}

func TestReviewAvailabilityBlocksIncompleteValidationTodo(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "验证步骤门禁", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"所有验证步骤完成"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	validation := &domain.Todo{
		ID: domain.NewID(domain.PrefixTodo), GoalID: goal.ID, Class: domain.TodoValidation,
		Status: domain.TodoPending, Instruction: "补充验证", Acceptance: []string{"验证完成"},
		Priority: domain.PriorityMedium, Predecessors: []string{}, Successors: []string{},
		DecisionScope: domain.DecisionScope{
			WorkItemIDs: []string{root.ID}, AgentIDs: []string{workerID}, MaxDispatch: 1,
		}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Todos().Create(ctx, validation); err != nil {
		t.Fatal(err)
	}

	availability, err := svc.ReviewAvailability(ctx, root, application.ReviewAvailabilityPermissions{
		CanApprove: true, CanWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !availability.Ready || availability.CanAccept || !availability.CanReturn ||
		!strings.Contains(availability.AcceptReason, "验证步骤") {
		t.Fatalf("未完成 validation Todo 时应阻止验收但保留打回：%+v", availability)
	}
}

func TestReviewAvailabilityRejectsStalePositiveGoalEvidence(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "过期验收依据", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"已有依据必须保持有效"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)
	goal, err := store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	goal.CompletionEvidenceSummary = append(goal.CompletionEvidenceSummary, domain.GovernanceEvidenceItem{
		SourceKind: domain.EvidenceSourceWorkItem, SourceID: root.ID,
		Verification: domain.EvidenceVerificationPassed, Summary: "过期的根任务依据",
		RecordedAt: time.Now().UTC(),
	})
	goal.Version++
	goal.UpdatedAt = time.Now().UTC()
	if err := store.Goals().Update(ctx, goal, goal.Version-1); err != nil {
		t.Fatal(err)
	}

	availability, err := svc.ReviewAvailability(ctx, root, application.ReviewAvailabilityPermissions{
		CanApprove: true, CanWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !availability.Ready || availability.CanAccept || !availability.CanReturn ||
		availability.AcceptReason != "已有验收依据已失效" {
		t.Fatalf("已有失效 positive evidence 时不得显示可验收：%+v", availability)
	}
}

func TestReviewAvailabilitySeparatesReadinessFromPermissions(t *testing.T) {
	ctx, svc, store, dispatcher, wsID, workerID := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "权限投影", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
		AcceptanceCriteria: []string{"权限只影响操作按钮"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root = prepareValidatedCoordinatorAcceptance(t, ctx, svc, store, dispatcher, root.ID, workerID)

	availability, err := svc.ReviewAvailability(ctx, root, application.ReviewAvailabilityPermissions{})
	if err != nil {
		t.Fatal(err)
	}
	if !availability.Ready || availability.CanAccept || availability.CanReturn ||
		availability.AcceptReason != "当前角色无权验收任务" || availability.ReturnReason != "当前角色无权打回任务" {
		t.Fatalf("readiness 与当前命令权限应分离：%+v", availability)
	}
}

func TestReviewAvailabilityDoesNotBypassGoalWithoutCoordinatorState(t *testing.T) {
	ctx, svc, store, _, wsID, workerID := seedCoordinatorEnv(t)
	if _, err := store.TaskCoordinators().EnsureConfig(ctx, wsID); err != nil {
		t.Fatal(err)
	}
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "无 Coordinator 的治理任务", RecordKind: domain.RecordKindTask,
		AgentProfileID:     workerID,
		AcceptanceCriteria: []string{"仍需遵守治理验收门禁"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Transition(domain.WorkItemInProgress, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, root, root.Version-1); err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.EnterReview(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, root, root.Version-1); err != nil {
		t.Fatal(err)
	}
	root, err = store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.EnterAcceptance(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.WorkItems().Update(ctx, root, root.Version-1); err != nil {
		t.Fatal(err)
	}

	availability, err := svc.ReviewAvailability(ctx, root, application.ReviewAvailabilityPermissions{
		CanApprove: true, CanWrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !availability.Ready || availability.CanAccept || !availability.CanReturn ||
		availability.AcceptReason == "" {
		t.Fatalf("无 Coordinator 但有 Goal 时不得绕过 canonical 门禁：%+v", availability)
	}
}
