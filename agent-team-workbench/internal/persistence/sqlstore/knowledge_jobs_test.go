package sqlstore

import (
	"context"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestKnowledgeJobListRecoverableFiltersBeforePageLimit(t *testing.T) {
	_, store, ws, agent, _ := knowledgeTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	newJob := func(id string, status domain.KnowledgeJobStatus) *domain.KnowledgeJob {
		wi := &domain.WorkItem{ID: domain.NewID(domain.PrefixWorkItem), WorkspaceID: ws.ID,
			RecordKind: domain.RecordKindChat, Title: id, Status: domain.WorkItemTodo,
			Priority: domain.PriorityMedium, AgentProfileID: agent.ID, Version: 1,
			CreatedAt: now, UpdatedAt: now}
		if err := store.WorkItems().Create(ctx, wi); err != nil {
			t.Fatal(err)
		}
		return &domain.KnowledgeJob{ID: id, WorkspaceID: ws.ID, RequestingAgentID: agent.ID,
			AgentProfileID: agent.ID, WorkItemID: wi.ID, Mode: domain.KnowledgeJobInquiry,
			Status: status, Question: id, ClientKey: id, Version: 1,
			Coverage:  domain.KnowledgeCoverage{Status: domain.KnowledgeCoveragePartial},
			CreatedAt: now, UpdatedAt: now}
	}
	// More than one page of terminal history must not hide the one runnable
	// job. The query itself filters status before LIMIT, so this remains true
	// even when the terminal rows have newer timestamps.
	for i := 0; i < 201; i++ {
		job := newJob("kbj_terminal_"+string(rune('a'+i/26))+string(rune('a'+i%26)), domain.KnowledgeJobCompleted)
		if err := store.KnowledgeJobs().Create(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	active := newJob("kbj_active", domain.KnowledgeJobRunning)
	if err := store.KnowledgeJobs().Create(ctx, active); err != nil {
		t.Fatal(err)
	}
	got, err := store.KnowledgeJobs().ListRecoverable(ctx, ws.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != active.ID {
		t.Fatalf("recoverable page = %+v, want active job despite terminal history", got)
	}
	pending := newJob("kbj_pending_cancel", domain.KnowledgeJobIncomplete)
	pending.LastError = "cancel_pending:run_pending: transient control error"
	if err := store.KnowledgeJobs().Create(ctx, pending); err != nil {
		t.Fatal(err)
	}
	pendingRows, err := store.KnowledgeJobs().ListCancellationPending(ctx, ws.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingRows) != 1 || pendingRows[0].ID != pending.ID {
		t.Fatalf("pending cancellation rows = %+v, want %s", pendingRows, pending.ID)
	}
}
