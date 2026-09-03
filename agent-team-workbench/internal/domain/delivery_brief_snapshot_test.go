package domain

import (
	"strings"
	"testing"
	"time"
)

func deliveryBriefSnapshotTestPayload() string {
	return `{"work_item":{},"acceptance_criteria":[],"conclusion":{},"attempts":[],"runs":[],"changes":null,"artifacts":[],"blocker":null,"risks":[],"comments":[],"freshness":{"source_versions":{},"state":"current","missing_sources":[]},"truncation":{}}`
}

func TestDeliveryBriefSnapshotSealAndVerifyDigest(t *testing.T) {
	snapshot := &DeliveryBriefSnapshot{
		ID: "brief_test", SchemaVersion: DeliveryBriefSnapshotSchemaVersion,
		GoalID: "goal_test", TodoID: "todo_test", WorkItemID: "wi_test",
		SnapshotJSON: deliveryBriefSnapshotTestPayload(), SourceVersions: map[string]int64{"work_item": 1},
		FreshnessState: "current", AsOfEventSeq: 7, CreatedAt: time.Now().UTC(),
	}
	if err := snapshot.Seal(); err != nil {
		t.Fatal(err)
	}
	if !ValidCanonicalDigest(snapshot.CanonicalDigest) {
		t.Fatalf("Seal must compute canonical digest: %q", snapshot.CanonicalDigest)
	}
	if err := VerifyDeliveryBriefSnapshotDigest(snapshot); err != nil {
		t.Fatal(err)
	}
	mutated := *snapshot
	mutated.SnapshotJSON = strings.Replace(mutated.SnapshotJSON, `"current"`, `"partial"`, 1)
	if err := VerifyDeliveryBriefSnapshotDigest(&mutated); err == nil {
		t.Fatal("payload mutation must fail digest verification")
	}
}

func TestDeliveryBriefSnapshotRejectsGeneratedAtAndUnknownFields(t *testing.T) {
	base := &DeliveryBriefSnapshot{
		ID: "brief_test", SchemaVersion: DeliveryBriefSnapshotSchemaVersion,
		GoalID: "goal_test", TodoID: "todo_test", WorkItemID: "wi_test",
		SnapshotJSON: deliveryBriefSnapshotTestPayload(), SourceVersions: map[string]int64{},
		FreshnessState: "current", CreatedAt: time.Now().UTC(),
	}
	for name, payload := range map[string]string{
		"generated_at": strings.Replace(base.SnapshotJSON, `"work_item"`, `"generated_at":"now","work_item"`, 1),
		"unknown":      strings.Replace(base.SnapshotJSON, `"work_item"`, `"extra":{},"work_item"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *base
			candidate.SnapshotJSON = payload
			if err := candidate.Seal(); err == nil {
				t.Fatal("closed snapshot payload must reject", name)
			}
		})
	}
}
