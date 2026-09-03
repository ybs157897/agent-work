package application_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/persistence/sqlstore"
)

func unresolvedQuotaGapFixture(t *testing.T) (context.Context, *application.Service, *sqlstore.Store,
	string, domain.QuotaSpendKey, domain.GovernanceEvidenceItem) {
	t.Helper()
	ctx, svc, store, dispatcher, root, goal := usageCoordinatorEnvForTest(t,
		"quota gap resolution", nil,
		domain.QuotaPolicy{Kind: domain.QuotaOutputTokens, Limit: 1_000, Enforcement: domain.QuotaEnforcementEnforce})
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	goal, err := store.Goals().Get(ctx, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.runs) != 1 {
		t.Fatalf("source Coordinator Run expected: %d", len(dispatcher.runs))
	}
	source := dispatcher.runs[0]
	usageDriveSourceDecision(t, ctx, svc, store, source.ID,
		usageWorkerDecision(t, "agent_coordinator_settlement_worker"))
	worker := dispatcher.runs[1]
	usageStartRun(t, ctx, svc, worker.ID)
	usageInjectRunUsage(t, ctx, svc, store, worker.ID, fullUsageCounters(0, 0, 0, 0, 0))
	usageDriveSourceSucceeded(t, ctx, svc, worker.ID) // source remains the close-time absent gap
	if err := svc.StartCoordinator(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	gaps, err := store.Quotas().ListUnresolved(ctx, goal.ID, domain.QuotaOutputTokens)
	if err != nil || len(gaps) == 0 {
		t.Fatalf("an unresolved output gap expected: gaps=%+v err=%v", gaps, err)
	}
	todo, err := store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		t.Fatal(err)
	}
	brief, err := svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: root.ID, ClientKey: "gap-resolution-evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := domain.GovernanceEvidenceItem{
		SourceKind: domain.EvidenceSourceDeliveryBrief, SourceID: brief.ID,
		Verification: domain.EvidenceVerificationAccepted,
		Summary:      "operator verified usage statement", RecordedAt: time.Now().UTC(),
	}
	return ctx, svc, store, goal.ID, gaps[0].Key, evidence
}

func TestReconcileQuotaGapAddsAuditedAdjustmentWithoutMutatingSpend(t *testing.T) {
	ctx, svc, store, goalID, target, evidence := unresolvedQuotaGapFixture(t)
	beforeSpend, err := store.Quotas().GetSpend(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	beforeReservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: target.TurnKey, Kind: target.Kind})
	if err != nil {
		t.Fatal(err)
	}
	resolutionParams := application.ReconcileQuotaGapParams{
		Target: target, Amount: 42, Evidence: evidence, ActorID: "user_operator",
		Reason: "verified against the signed usage statement", ClientKey: "resolve-gap-1",
	}
	resolution, err := svc.ReconcileQuotaGap(ctx, resolutionParams)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != domain.QuotaGapResolutionReconciled || resolution.Amount != 42 || resolution.CanonicalDigest == "" {
		t.Fatalf("resolution must be sealed/reconciled: %+v", resolution)
	}
	afterSpend, err := store.Quotas().GetSpend(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeSpend, afterSpend) {
		t.Fatalf("original unresolved spend must remain unchanged: before=%+v after=%+v", beforeSpend, afterSpend)
	}
	afterReservation, err := store.Quotas().Get(ctx, domain.QuotaReservationKey{TurnKey: target.TurnKey, Kind: target.Kind})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeReservation, afterReservation) {
		t.Fatalf("original reservation must remain unchanged: before=%+v after=%+v", beforeReservation, afterReservation)
	}
	gaps, err := store.Quotas().ListUnresolved(ctx, goalID, target.Kind)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("reconciled gap must leave open unresolved list: gaps=%+v err=%v", gaps, err)
	}
	committed, err := store.Quotas().SumCommitted(ctx, goalID, target.Kind)
	if err != nil || committed != 42 {
		t.Fatalf("reconciled amount must be additive committed usage: committed=%d err=%v", committed, err)
	}
	decision, err := svc.ShouldRunLocked(ctx, application.ShouldRunRequest{GoalID: goalID, Kind: target.Kind, Amount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Unresolved || decision.WouldDeny || !decision.Allowed || decision.Used != 42 {
		t.Fatalf("resolved gap must not block within remaining budget: %+v", decision)
	}
	loaded, err := svc.GetQuotaGapResolution(ctx, resolution.ID)
	if err != nil || loaded.CanonicalDigest != resolution.CanonicalDigest {
		t.Fatalf("service read must verify resolution: loaded=%+v err=%v", loaded, err)
	}
	resolutions, err := svc.ListQuotaGapResolutions(ctx, goalID)
	if err != nil || len(resolutions) != 1 {
		t.Fatalf("goal resolution list must contain one row: resolutions=%+v err=%v", resolutions, err)
	}

	replay, err := svc.ReconcileQuotaGap(ctx, resolutionParams)
	if err != nil || replay.ID != resolution.ID {
		t.Fatalf("same intent must replay exact resolution: replay=%+v err=%v", replay, err)
	}
	changed := resolutionParams
	changed.Amount = 43
	if _, err := svc.ReconcileQuotaGap(ctx, changed); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same target with changed amount must conflict: %v", err)
	}
	goal, err := store.Goals().Get(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events().Since(ctx, goal.WorkspaceID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for _, event := range events {
		if event.Type == domain.EventQuotaGapReconciled {
			eventCount++
			if _, hasEvidence := event.Data["evidence"]; hasEvidence {
				t.Fatal("reconciliation event must not carry full evidence payload")
			}
		}
	}
	if eventCount != 1 {
		t.Fatalf("same intent replay must emit one event: %d", eventCount)
	}
}

func TestConcurrentQuotaGapReconciliationHasOneWinnerAndEvent(t *testing.T) {
	ctx, svc, store, goalID, target, evidence := unresolvedQuotaGapFixture(t)
	params := application.ReconcileQuotaGapParams{
		Target: target, Amount: 7, Evidence: evidence, ActorID: "user_operator",
		Reason: "verified by signed statement", ClientKey: "resolve-gap-concurrent",
	}
	const n = 8
	results := make([]*domain.QuotaGapResolution, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = svc.ReconcileQuotaGap(ctx, params)
		}(i)
	}
	wg.Wait()
	winner := ""
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("concurrent reconciliation %d failed: %v", i, errs[i])
		}
		if winner == "" {
			winner = results[i].ID
		}
		if results[i].ID != winner {
			t.Fatalf("concurrent calls must replay one resolution: first=%s current=%s", winner, results[i].ID)
		}
	}
	resolutions, err := svc.ListQuotaGapResolutions(ctx, goalID)
	if err != nil || len(resolutions) != 1 {
		t.Fatalf("one resolution row expected: resolutions=%+v err=%v", resolutions, err)
	}
	goal, err := store.Goals().Get(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events().Since(ctx, goal.WorkspaceID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	eventCount := 0
	for _, event := range events {
		if event.Type == domain.EventQuotaGapReconciled {
			eventCount++
		}
	}
	if eventCount != 1 {
		t.Fatalf("one reconciliation event expected: %d", eventCount)
	}
}

func TestQuotaGapReconciliationRejectsOutOfScopeEvidence(t *testing.T) {
	ctx, svc, store, goalID, target, evidence := unresolvedQuotaGapFixture(t)
	goal, err := store.Goals().Get(ctx, goalID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateWorkItem(ctx, goal.WorkspaceID, application.CreateWorkItemParams{Title: "foreign evidence"})
	if err != nil {
		t.Fatal(err)
	}
	foreign := evidence
	foreign.SourceKind = domain.EvidenceSourceWorkItem
	foreign.SourceID = other.ID
	foreign.Verification = domain.EvidenceVerificationAccepted
	foreign.RecordedAt = time.Now().UTC()
	if _, err := svc.ReconcileQuotaGap(ctx, application.ReconcileQuotaGapParams{
		Target: target, Amount: 1, Evidence: foreign, ActorID: "user_operator",
		Reason: "out of scope", ClientKey: "resolve-gap-foreign",
	}); !errors.Is(err, domain.ErrStateConflict) && !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("out-of-scope evidence must be rejected: %v", err)
	}
}
