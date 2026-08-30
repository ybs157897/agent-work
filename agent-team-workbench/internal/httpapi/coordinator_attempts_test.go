package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestCoordinatorSnapshotKeepsRetryScheduledSourceAttemptNumber(t *testing.T) {
	ctx := context.Background()
	s := newPlanTestServer(t)
	mux := s.Routes()
	wsID, agentID, _ := seedPlanHTTPEnv(t, s)
	now := time.Now().UTC()
	config, err := s.store.TaskCoordinators().EnsureConfig(ctx, wsID)
	if err != nil {
		t.Fatal(err)
	}
	root := &domain.WorkItem{
		ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: wsID,
		RecordKind: domain.RecordKindTask, Title: "attempt chain",
		Status: domain.WorkItemInProgress, Priority: domain.PriorityMedium,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.WorkItems().Create(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := s.store.TaskCoordinators().CreateState(ctx, &domain.TaskCoordinatorState{
		ID: domain.NewID(domain.PrefixCoordinatorState), WorkspaceID: wsID,
		RootWorkItemID: root.ID, CoordinatorAgentID: config.AgentProfileID,
		Status: domain.CoordinatorRunning, Phase: "attempt", CurrentAction: "观察 Worker",
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sourceRunID := "run_attempt_source"
	retryRunID := "run_attempt_retry"
	events := []*domain.TaskCoordinatorEvent{
		{ID: "coordevt_attempt_source_started", WorkspaceID: wsID, RootWorkItemID: root.ID, WorkItemID: root.ID,
			Kind: domain.EventCoordinatorAttemptUpdated, Summary: "Worker started", RunID: sourceRunID,
			AgentID: agentID, Attempt: 1, Data: map[string]any{
				"stage": "attempt", "status": "running", "max_attempts": 3,
			}, OccurredAt: now},
		{ID: "coordevt_attempt_source_retry", WorkspaceID: wsID, RootWorkItemID: root.ID, WorkItemID: root.ID,
			Kind: domain.EventCoordinatorRetryScheduled, Summary: "Worker retry scheduled", RunID: sourceRunID,
			AgentID: agentID, Attempt: 2, Reason: "temporary transport failure", Data: map[string]any{
				"stage": "retry", "status": "waiting_retry", "retry_of": sourceRunID,
				"max_attempts": 3, "next_action": "退避后重试",
			}, OccurredAt: now.Add(time.Second)},
		{ID: "coordevt_attempt_retry_started", WorkspaceID: wsID, RootWorkItemID: root.ID, WorkItemID: root.ID,
			Kind: domain.EventCoordinatorAttemptUpdated, Summary: "Worker retry started", RunID: retryRunID,
			AgentID: agentID, Attempt: 2, Data: map[string]any{
				"stage": "attempt", "status": "running", "retry_of": sourceRunID,
				"max_attempts": 3,
			}, OccurredAt: now.Add(2 * time.Second)},
	}
	for _, event := range events {
		if err := s.store.TaskCoordinators().AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/work-items/"+root.ID+"/coordinator", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("Coordinator snapshot = %d: %s", rec.Code, rec.Body.String())
	}
	var snapshot struct {
		Attempts []struct {
			RunID   string `json:"run_id"`
			Attempt int    `json:"attempt"`
			RetryOf string `json:"retry_of"`
			Status  string `json:"status"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Attempts) != 2 {
		t.Fatalf("应按 run_id 聚合为两次尝试: %+v", snapshot.Attempts)
	}
	if snapshot.Attempts[0].RunID != sourceRunID || snapshot.Attempts[0].Attempt != 1 ||
		snapshot.Attempts[0].RetryOf != "" || snapshot.Attempts[0].Status != "waiting_retry" {
		t.Fatalf("源 Run 必须保持 attempt=1 且不自引用 retry_of: %+v", snapshot.Attempts[0])
	}
	if snapshot.Attempts[1].RunID != retryRunID || snapshot.Attempts[1].Attempt != 2 ||
		snapshot.Attempts[1].RetryOf != sourceRunID || snapshot.Attempts[1].Status != "running" {
		t.Fatalf("retry Run 必须为 attempt=2 并指向源 Run: %+v", snapshot.Attempts[1])
	}
}
