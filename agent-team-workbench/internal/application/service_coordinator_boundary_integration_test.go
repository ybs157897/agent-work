package application_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestAutoCoordinatedRootRejectsNonTodoInitialStatus(t *testing.T) {
	ctx, svc, _, _, wsID, _ := seedCoordinatorEnv(t)

	for _, status := range []domain.WorkItemStatus{
		domain.WorkItemInProgress,
		domain.WorkItemCompleted,
		domain.WorkItemCancelled,
		domain.WorkItemBlocked,
	} {
		_, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
			Title:          "绕过验收的根 Task",
			RecordKind:     domain.RecordKindTask,
			Status:         status,
			AutoCoordinate: true,
		})
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("Coordinator root 初始 status=%s 应拒绝为 ErrValidation，实际 %v", status, err)
		}
	}
}

func TestAcceptCoordinatedChildCannotCompleteRootCoordinator(t *testing.T) {
	ctx, svc, store, _, wsID, _ := seedCoordinatorEnv(t)
	root, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{
		Title: "根 Task", RecordKind: domain.RecordKindTask, AutoCoordinate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	child := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID,
		RecordKind: domain.RecordKindTask, ParentID: root.ID,
		Title: "Coordinator 子 Task", Status: domain.WorkItemInProgress,
		Phase: domain.PhaseAcceptance, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkItems().Create(ctx, child); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.AcceptWorkItem(ctx, child.ID, 0); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("coordinated child 验收应被拒绝为 ErrValidation，实际 %v", err)
	}
	childAfter, err := store.WorkItems().Get(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if childAfter.Status != domain.WorkItemInProgress || childAfter.Phase != domain.PhaseAcceptance {
		t.Fatalf("拒绝子任务验收后子项状态不应变化: %+v", childAfter)
	}
	rootAfter, err := store.WorkItems().Get(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rootAfter.Status == domain.WorkItemCompleted || stateAfter.Status == domain.CoordinatorCompleted {
		t.Fatalf("验收子任务不得完成根控制线: root=%+v before=%+v after=%+v", rootAfter, stateBefore, stateAfter)
	}
}
