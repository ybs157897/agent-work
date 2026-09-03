package application_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	atwruntime "github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestCaptureDeliveryBriefSnapshotReplayConflictAndScope(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)

	first, err := svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: rootID, ClientKey: "brief-capture-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.CanonicalDigest == "" || first.FreshnessState != "current" {
		t.Fatalf("snapshot must be sealed/current: %+v", first)
	}
	if first.SnapshotJSON == "" {
		t.Fatal("snapshot payload must not be empty")
	}
	events, err := store.Events().Since(ctx, workspaceID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var createdEvents int
	for _, event := range events {
		if event.Type == domain.EventDeliveryBriefSnapshotCreated {
			createdEvents++
			if event.Aggregate.Type != domain.AggregateDeliveryBriefSnapshot || event.Aggregate.ID != first.ID {
				t.Fatalf("snapshot event aggregate identity mismatch: %+v", event)
			}
			if event.Data["schema_version"] != domain.DeliveryBriefSnapshotSchemaVersion {
				t.Fatalf("snapshot event must carry schema version: %#v", event.Data)
			}
			if _, containsPayload := event.Data["snapshot_json"]; containsPayload {
				t.Fatal("snapshot event must not contain the full snapshot payload")
			}
		}
	}
	if createdEvents != 1 {
		t.Fatalf("first capture must emit one snapshot event, got %d", createdEvents)
	}
	loaded, err := svc.GetDeliveryBriefSnapshot(ctx, first.ID)
	if err != nil || loaded.ID != first.ID || loaded.SnapshotJSON != first.SnapshotJSON {
		t.Fatalf("service snapshot read must verify and preserve sealed content: loaded=%+v err=%v", loaded, err)
	}

	replay, err := svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: rootID, ClientKey: "brief-capture-1",
	})
	if err != nil || replay.ID != first.ID || replay.CanonicalDigest != first.CanonicalDigest {
		t.Fatalf("same capture key must replay exact snapshot: replay=%+v err=%v", replay, err)
	}
	events, err = store.Events().Since(ctx, workspaceID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	createdEvents = 0
	for _, event := range events {
		if event.Type == domain.EventDeliveryBriefSnapshotCreated {
			createdEvents++
		}
	}
	if createdEvents != 1 {
		t.Fatalf("replay must not emit a second snapshot event, got %d", createdEvents)
	}

	child, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{
		Title: "snapshot child", ParentID: rootID, RecordKind: domain.RecordKindTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: child.ID, ClientKey: "brief-capture-1",
	}); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same key with a different WorkItem must conflict: %v", err)
	}

	other, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{Title: "other root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: other.ID, ClientKey: "brief-cross-root",
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("cross-root WorkItem must be rejected: %v", err)
	}
	_ = store
}

func TestDeliveryBriefSnapshotFinishFreshnessUsesSourceContent(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	snapshot, err := svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: rootID, ClientKey: "brief-freshness-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence := domain.GovernanceEvidenceItem{
		SourceKind: domain.EvidenceSourceDeliveryBrief, SourceID: snapshot.ID,
		Verification: domain.EvidenceVerificationPassed, Summary: "sealed delivery brief",
		RecordedAt: time.Now().UTC(),
	}
	if err := svc.ValidateEvidenceReference(ctx, goal.ID, todo.ID, evidence); err != nil {
		t.Fatalf("fresh snapshot should satisfy evidence gate: %v", err)
	}

	// The global Workspace stream watermark may advance for unrelated work. It
	// is only a monotonic observation watermark; unchanged source content stays
	// valid.
	if _, err := svc.CreateWorkItem(ctx, workspaceID, application.CreateWorkItemParams{Title: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateEvidenceReference(ctx, goal.ID, todo.ID, evidence); err != nil {
		t.Fatalf("unrelated Workspace event must not stale the snapshot: %v", err)
	}

	root, err := store.WorkItems().Get(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	expected := root.Version
	root.Title = "root changed after capture"
	if err := store.WorkItems().Update(ctx, root, expected); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateEvidenceReference(ctx, goal.ID, todo.ID, evidence); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("source WorkItem change must stale the snapshot: %v", err)
	}
}

func TestConcurrentDeliveryBriefSnapshotReplayHasOneRowAndEvent(t *testing.T) {
	ctx, db, svc, _, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	const n = 8
	results := make([]*domain.DeliveryBriefSnapshot, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
				GoalID: goal.ID, TodoID: todo.ID, WorkItemID: rootID, ClientKey: "brief-concurrent",
			})
		}(i)
	}
	wg.Wait()
	wantID := ""
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("concurrent capture %d failed: %v", i, errs[i])
		}
		if wantID == "" {
			wantID = results[i].ID
		}
		if results[i].ID != wantID || results[i].CanonicalDigest != results[0].CanonicalDigest {
			t.Fatalf("concurrent captures must replay one immutable row: first=%+v current=%+v", results[0], results[i])
		}
	}
	var rows, events int
	if err := db.QueryRow(`SELECT count(*) FROM governance_delivery_brief_snapshots WHERE goal_id=? AND todo_id=? AND client_key=?`, goal.ID, todo.ID, "brief-concurrent").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM stream_events WHERE workspace_id=? AND event_type=?`, workspaceID, domain.EventDeliveryBriefSnapshotCreated).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || events != 1 {
		t.Fatalf("concurrent capture must persist one row/event: rows=%d events=%d", rows, events)
	}
}

func TestPartialDeliveryBriefSnapshotCannotPassEvidence(t *testing.T) {
	ctx, db, svc, store, dispatcher, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	broken := &briefBrokenStore{Store: store}
	partialSvc := application.NewService(broken, dispatcher, noopNotifier{}, atwruntime.NewRegistry())
	partial, err := partialSvc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: rootID, ClientKey: "brief-partial-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if partial.FreshnessState != "partial" {
		t.Fatalf("injected source failure must be retained as partial: %+v", partial)
	}
	evidence := domain.GovernanceEvidenceItem{
		SourceKind: domain.EvidenceSourceDeliveryBrief, SourceID: partial.ID,
		Verification: domain.EvidenceVerificationPassed, Summary: "must not pass",
		RecordedAt: time.Now().UTC(),
	}
	if err := svc.ValidateEvidenceReference(ctx, goal.ID, todo.ID, evidence); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("partial snapshot must not satisfy passed evidence: %v", err)
	}
}

func TestDeliveryBriefSnapshotRepositoryRejectsDigestTamper(t *testing.T) {
	ctx, db, svc, store, _, workspaceID, rootID := seedGovernanceService(t)
	defer db.Close()
	goal, todo := createAndStartGovernanceGoal(t, ctx, svc, workspaceID, rootID)
	snapshot, err := svc.CaptureDeliveryBriefSnapshot(ctx, application.CaptureDeliveryBriefSnapshotParams{
		GoalID: goal.ID, TodoID: todo.ID, WorkItemID: rootID, ClientKey: "brief-tamper-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// This test simulates corruption below the repository. Production writes
	// cannot reach these UPDATEs because migration 0032 installs the immutable
	// trigger.
	if _, err := db.Exec(`DROP TRIGGER governance_delivery_brief_snapshot_identity_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE governance_delivery_brief_snapshots SET canonical_digest=? WHERE id=?`,
		"sha256:"+repeatSnapshotHex('0'), snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeliveryBriefSnapshots().Get(ctx, snapshot.ID); err == nil {
		t.Fatal("tampered digest must fail repository validation")
	}
}

func repeatSnapshotHex(ch byte) string {
	return repeatByte(ch, 64)
}

func repeatByte(ch byte, count int) string {
	b := make([]byte, count)
	for i := range b {
		b[i] = ch
	}
	return string(b)
}
