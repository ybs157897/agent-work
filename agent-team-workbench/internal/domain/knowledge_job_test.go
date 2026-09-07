package domain

import "testing"

func TestKnowledgeJobBudgetNormalizesWallClockLimit(t *testing.T) {
	if got := (KnowledgeJobBudget{}).Normalize().MaxDurationSeconds; got != 300 {
		t.Fatalf("zero duration budget = %d, want default 300", got)
	}
	if got := (KnowledgeJobBudget{MaxDurationSeconds: -1}).Normalize().MaxDurationSeconds; got != 300 {
		t.Fatalf("negative duration budget = %d, want default 300", got)
	}
	if got := (KnowledgeJobBudget{MaxDurationSeconds: 3601}).Normalize().MaxDurationSeconds; got != 300 {
		t.Fatalf("over-limit duration budget = %d, want default 300", got)
	}
	if got := (KnowledgeJobBudget{MaxDurationSeconds: 3600}).Normalize().MaxDurationSeconds; got != 3600 {
		t.Fatalf("maximum duration budget = %d, want 3600", got)
	}
}
