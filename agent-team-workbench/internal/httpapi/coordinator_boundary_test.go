package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCreateChildUnderCompletedCoordinatorRootHTTPIsRejected(t *testing.T) {
	ctx := context.Background()
	s := newPlanTestServer(t)
	wsID, _, _ := seedPlanHTTPEnv(t, s)
	config, err := s.store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	root := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID,
		RecordKind: domain.RecordKindTask, Title: "已验收根 Task",
		Status: domain.WorkItemCompleted, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.WorkItems().Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := s.store.TaskCoordinators().CreateState(ctx, &domain.TaskCoordinatorState{
		ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: wsID,
		RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID,
		Status: domain.CoordinatorCompleted, Phase: "acceptance",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	child := postWorkItem(t, s.Routes(), wsID,
		`{"title":"不应挂到已验收根任务","record_kind":"task","parent_id":"`+root.ID+`"}`)
	if child.Code != http.StatusUnprocessableEntity || child.Body.Len() == 0 {
		t.Fatalf("终态 Coordinator root 创建子 Task 应返回 422: %d %s", child.Code, child.Body.String())
	}
	if _, err := s.store.WorkItems().Get(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	children, err := s.store.WorkItems().ListByParent(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 0 {
		t.Fatalf("HTTP 拒绝终态 parent 后不得落子项: %+v", children)
	}
	state, err := s.store.TaskCoordinators().GetState(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.CoordinatorCompleted {
		t.Fatalf("HTTP 拒绝终态 parent 不得重排 Coordinator: %+v", state)
	}

	partial := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID,
		RecordKind: domain.RecordKindTask, Title: "状态不一致根任务",
		Status: domain.WorkItemInProgress, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.WorkItems().Create(ctx, partial); err != nil {
		t.Fatal(err)
	}
	if err := s.store.TaskCoordinators().CreateState(ctx, &domain.TaskCoordinatorState{
		ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: wsID,
		RootWorkItemID: partial.ID, CoordinatorAgentID: config.AgentProfileID,
		Status: domain.CoordinatorCompleted, Phase: "acceptance",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	partialChild := postWorkItem(t, s.Routes(), wsID,
		`{"title":"不应重排控制线","record_kind":"task","parent_id":"`+partial.ID+`"}`)
	if partialChild.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Coordinator completed 但 WorkItem 未终态时也应拒绝子 Task: %d %s", partialChild.Code, partialChild.Body.String())
	}
}
