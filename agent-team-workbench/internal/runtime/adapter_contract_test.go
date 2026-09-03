package runtime

import "testing"

func TestPlannerControlCapabilityVocabulary(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "transport", got: CapabilityStructuredTransport, want: "structured_transport"},
		{name: "schema", got: CapabilitySchemaConstrainedOutput, want: "schema_constrained_output"},
		{name: "tool", got: CapabilityControlToolCall, want: "control_tool_call"},
	}
	seen := map[string]bool{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("capability name drifted: got=%q want=%q", tc.got, tc.want)
			}
			if tc.got == "structured_output" {
				t.Fatal("planner control capability must not collapse into legacy structured_output")
			}
			if seen[tc.got] {
				t.Fatalf("planner control capabilities must be distinct: %q", tc.got)
			}
			seen[tc.got] = true
		})
	}
}
