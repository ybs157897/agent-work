package application_test

import (
	"context"
	"sync"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

// TestSupersededPlanExpiresPendingDispatchApproval proves that replacing a
// waiting plan closes its manual dispatch gate in the same durable transition.
// A late approval decision must remain a rejected replay and must not create a
// child Run. Concurrent late decisions must have the same outcome.
func TestSupersededPlanExpiresPendingDispatchApproval(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	defer db.Close()
	store := sqlstore.New(db)
	svc := application.NewService(store, &captureDispatcher{}, noopNotifier{}, atwruntime.NewRegistry())

	wsID, leadID, _ := seedPlanEnv(t, ctx, store)
	manualID := manualAgent(t, ctx, store, wsID, "agent_manual_supersede")
	main, err := svc.CreateWorkItem(ctx, wsID, application.CreateWorkItemParams{Title: "待批计划替换"})
	if err != nil {
		t.Fatal(err)
	}

	oldPlan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{dispatchStep(manualID, "待批旧路线", "旧路线")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if oldPlan.Status != domain.PlanWaiting {
		t.Fatalf("old plan should wait for approval, got %s", oldPlan.Status)
	}
	approvalID := pendingPlanApprovalID(t, ctx, store, wsID, oldPlan.ID)

	newPlan, err := svc.SubmitPlan(ctx, wsID, application.SubmitPlanParams{
		WorkItemID: main.ID, AgentProfileID: leadID,
		Steps: []application.PlanStepInput{{Verb: "finish", Payload: map[string]any{"summary": "新路线"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if newPlan.Status != domain.PlanFinished {
		t.Fatalf("new plan should finish, got %s", newPlan.Status)
	}

	approval, err := store.Runs().GetApproval(ctx, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != domain.ApprovalExpired {
		t.Fatalf("superseding the waiting plan should expire its pending approval, got %s", approval.Status)
	}
	if approval.ResolvedBy != "system:plan_superseded" || approval.ResolveReason == "" {
		t.Fatalf("supersede closure should record a system actor and reason, got by=%q reason=%q", approval.ResolvedBy, approval.ResolveReason)
	}

	// A late decision is not an alternate execution path: the approval is
	// terminal and ResolveApproval must reject both decisions.
	for _, approved := range []bool{true, false} {
		if _, err := svc.ResolveApproval(ctx, approvalID, approved, "late_operator", "迟到决定", domain.ApprovalScopeOnce); err == nil {
			t.Fatalf("late decision approved=%t should fail", approved)
		}
	}
	approval, err = store.Runs().GetApproval(ctx, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Status != domain.ApprovalExpired {
		t.Fatalf("late decisions must not change expired approval, got %s", approval.Status)
	}
	if children, err := store.WorkItems().ListByParent(ctx, main.ID); err != nil {
		t.Fatal(err)
	} else if len(children) != 0 {
		t.Fatalf("late approval must not create children, got %d", len(children))
	}

	// Replays arriving together must all observe the same terminal approval.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, approved := range []bool{true, true, false, false} {
		approved := approved
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.ResolveApproval(ctx, approvalID, approved, "concurrent_late_operator", "重放", domain.ApprovalScopeOnce)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("concurrent late decision unexpectedly succeeded")
		}
	}
	if children, err := store.WorkItems().ListByParent(ctx, main.ID); err != nil {
		t.Fatal(err)
	} else if len(children) != 0 {
		t.Fatalf("concurrent late approval must not create children, got %d", len(children))
	}

	events, err := store.Events().Since(ctx, wsID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var expired, resolved int
	for _, event := range events {
		if event.Aggregate.ID != approvalID {
			continue
		}
		switch event.Type {
		case domain.EventApprovalExpired:
			expired++
		case domain.EventApprovalResolved:
			resolved++
		}
	}
	if expired != 1 || resolved != 0 {
		t.Fatalf("supersede/replay approval events should be one expired and no resolved, got expired=%d resolved=%d", expired, resolved)
	}
}
