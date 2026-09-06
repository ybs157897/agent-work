package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func knowledgeCaptureRun(t *testing.T, store *Store, ws *domain.Workspace, agent *domain.AgentProfile) *domain.ExecutionRun {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	item := &domain.WorkItem{ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: ws.ID,
		RecordKind: domain.RecordKindTask, Title: "knowledge capture", Status: domain.WorkItemTodo,
		Priority: domain.PriorityMedium, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := store.WorkItems().Create(ctx, item); err != nil {
		t.Fatal(err)
	}
	run := &domain.ExecutionRun{ID: domain.NewID(domain.PrefixRun), WorkspaceID: ws.ID,
		WorkItemID: item.ID, AgentProfileID: agent.ID, Status: domain.RunSucceeded,
		Version: 1, Input: map[string]any{}, CreatedAt: now, UpdatedAt: now}
	if err := store.Runs().Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestKnowledgeRunCaptureQueueRetriesAndCompletesIdempotently(t *testing.T) {
	db, store, ws, alpha, _ := knowledgeTestDB(t)
	ctx := context.Background()
	run := knowledgeCaptureRun(t, store, ws, alpha)
	if err := store.Knowledge().EnqueueRunCapture(ctx, ws.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().EnqueueRunCapture(ctx, ws.ID, run.ID); err != nil {
		t.Fatalf("duplicate enqueue must be idempotent: %v", err)
	}
	pending, err := store.Knowledge().ListPendingRunCaptures(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].RunID != run.ID {
		t.Fatalf("pending capture = %+v err=%v", pending, err)
	}
	if err := store.Knowledge().RecordRunCaptureError(ctx, run.ID, "temporary source read failure"); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Knowledge().ListPendingRunCaptures(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("backoff capture was immediately retried: %+v err=%v", pending, err)
	}
	if _, err := db.Exec(`UPDATE knowledge_run_captures SET next_attempt_at=? WHERE run_id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), run.ID); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Knowledge().ListPendingRunCaptures(ctx, 10); err != nil || len(pending) != 1 {
		t.Fatalf("elapsed backoff capture not returned: %+v err=%v", pending, err)
	}
	request := domain.KnowledgeSubmitCandidate{WorkspaceID: ws.ID, AgentID: alpha.ID,
		RunID: run.ID, WorkItemID: run.WorkItemID, ClientKey: "capture-submit", NoChange: true}
	submission := &domain.KnowledgeSubmission{ID: domain.NewID(domain.PrefixKnowledgeSubmission),
		WorkspaceID: ws.ID, AgentID: alpha.ID, RunID: run.ID, WorkItemID: run.WorkItemID,
		ClientKey: request.ClientKey, Request: request}
	if _, _, err := store.Knowledge().SubmitCandidate(ctx, submission); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().CompleteRunCapture(ctx, run.ID, submission.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Knowledge().CompleteRunCapture(ctx, run.ID, submission.ID); err != nil {
		t.Fatalf("same completion replay must be idempotent: %v", err)
	}
	if err := store.Knowledge().RecordRunCaptureError(ctx, run.ID, "late error"); err != nil {
		t.Fatalf("late error must not reopen processed capture: %v", err)
	}
	if err := store.Knowledge().CompleteRunCapture(ctx, run.ID, "kss_other"); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different completion replay = %v, want conflict", err)
	}
	if pending, err := store.Knowledge().ListPendingRunCaptures(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("completed capture remains pending: %+v err=%v", pending, err)
	}
}

func TestKnowledgeRunCaptureEnqueueRejectsCrossWorkspaceRun(t *testing.T) {
	_, store, ws, alpha, _ := knowledgeTestDB(t)
	run := knowledgeCaptureRun(t, store, ws, alpha)
	if err := store.Knowledge().EnqueueRunCapture(context.Background(), "ws_other", run.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-workspace enqueue = %v, want not found", err)
	}
}
