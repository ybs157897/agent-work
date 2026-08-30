package domain

import "testing"

func TestTaskCoordinatorConfigClosedSets(t *testing.T) {
	for _, runtime := range []string{"mock", "codex_local", "kimi_local"} {
		if !ValidTaskCoordinatorRuntimeLabel(runtime) {
			t.Fatalf("known coordinator runtime %q rejected", runtime)
		}
	}
	for _, runtime := range []string{"dsh_local", "claude", "", "codex"} {
		if ValidTaskCoordinatorRuntimeLabel(runtime) {
			t.Fatalf("unsupported coordinator runtime %q accepted", runtime)
		}
	}
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh"} {
		if !ValidTaskCoordinatorReasoningEffort(effort) {
			t.Fatalf("known reasoning effort %q rejected", effort)
		}
	}
	if ValidTaskCoordinatorReasoningEffort("ultra") || ValidTaskCoordinatorReasoningEffort("") {
		t.Fatal("unsupported/empty reasoning effort accepted")
	}
}

func TestTaskCoordinatorRuntimeMatchesAdapter(t *testing.T) {
	tests := []struct {
		label, adapter string
		want           bool
	}{
		{label: "mock", adapter: "mock", want: true},
		{label: "codex_local", adapter: "codex-appserver", want: true},
		{label: "kimi_local", adapter: "kimi-appserver", want: true},
		{label: "kimi_local", adapter: "kimi", want: true},
		{label: "codex_local", adapter: "kimi-appserver", want: false},
		{label: "kimi_local", adapter: "codex-appserver", want: false},
		{label: "unknown", adapter: "codex-appserver", want: false},
	}
	for _, tt := range tests {
		if got := TaskCoordinatorRuntimeMatchesAdapter(tt.label, tt.adapter); got != tt.want {
			t.Errorf("TaskCoordinatorRuntimeMatchesAdapter(%q, %q) = %v, want %v", tt.label, tt.adapter, got, tt.want)
		}
	}
}
