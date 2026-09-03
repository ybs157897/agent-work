package application

import (
	"errors"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const validAgentID = "agent_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func requirePlanDecisionErrorCode(t *testing.T, err error, code domain.GovernanceErrorCode) *PlanDecisionError {
	t.Helper()
	var decisionErr *PlanDecisionError
	if !errors.As(err, &decisionErr) || decisionErr.Code != code {
		t.Fatalf("error=%v, want PlanDecisionError code=%s", err, code)
	}
	return decisionErr
}

func TestDecodePlanDecisionV2ValidTypedAndConverted(t *testing.T) {
	raw := []byte(`{"schema_version":"plan-decision/v2","kind":"plan","reason":"use evidence","next_action":"wait for workers","steps":[{"verb":"consult_knowledge","corpus":"docs","terms":["goal"],"limit":3},{"verb":"dispatch","agent_id":"` + validAgentID + `","title":"Implement","instruction":"Do the bounded work","acceptance":["tests pass"],"priority":"high","knowledge_from":0},{"verb":"join","children":"all","wake_at":"2026-09-01T12:00:00+08:00"}]}`)
	decision, err := DecodePlanDecisionV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Steps) != 3 || decision.Steps[1].Dispatch == nil || decision.Steps[2].Join == nil {
		t.Fatalf("typed decision mismatch: %+v", decision)
	}
	inputs, err := PlanDecisionStepInputs(decision)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 3 || inputs[1].Verb != "dispatch" || inputs[1].Payload["knowledge_from"] != 0 ||
		inputs[2].Payload["wake_at"] != "2026-09-01T04:00:00Z" {
		t.Fatalf("PlanStepInput conversion mismatch: %+v", inputs)
	}
	if _, isMap := any(decision.Steps[1]).(map[string]any); isMap {
		t.Fatal("wire step must remain a typed union")
	}
}

func TestDecodePlanDecisionV2ClassifiesSyntaxFailures(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "broken", raw: []byte(`{"schema_version":`)},
		{name: "duplicate", raw: []byte(`{"schema_version":"plan-decision/v2","schema_version":"plan-decision/v2"}`)},
		{name: "trailing", raw: []byte(`{} {}`)},
		{name: "utf8", raw: []byte{0xff, 0xfe}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePlanDecisionV2(tc.raw)
			requirePlanDecisionErrorCode(t, err, domain.GovernanceErrorPlanJSONSyntax)
		})
	}
}

func TestDecodePlanDecisionV2ClassifiesSchemaFailures(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown envelope", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"finish"}],"extra":true}`},
		{name: "unknown step", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"finish","summary":"legacy"}]}`},
		{name: "wrong type", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"dispatch","agent_id":"` + validAgentID + `","title":"t","instruction":"i","acceptance":"wrong"},{"verb":"join","children":"all"}]}`},
		{name: "empty agent id suffix", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"dispatch","agent_id":"agent_","title":"t","instruction":"i","acceptance":["a"]},{"verb":"join","children":"all"}]}`},
		{name: "whitespace in agent id", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"dispatch","agent_id":"agent_worker id","title":"t","instruction":"i","acceptance":["a"]},{"verb":"join","children":"all"}]}`},
		{name: "empty work item id suffix", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"join","children":["wi_"]}]}`},
		{name: "bad date", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"defer","wake_at":"tomorrow"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePlanDecisionV2([]byte(tc.raw))
			requirePlanDecisionErrorCode(t, err, domain.GovernanceErrorPlanSchemaValidation)
		})
	}
}

func TestDecodePlanDecisionV2AcceptsOpaqueTypedIDs(t *testing.T) {
	cases := map[string][2]string{
		"opaque fixture IDs":    {"agent_coordinator_worker", "wi_x"},
		"NewID constructor IDs": {domain.NewID(domain.PrefixAgent), domain.NewID(domain.PrefixWorkItem)},
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			raw := []byte(`{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"dispatch","agent_id":"` + ids[0] + `","title":"t","instruction":"i","acceptance":["a"]},{"verb":"join","children":["` + ids[1] + `"]}]}`)
			decision, err := DecodePlanDecisionV2(raw)
			if err != nil {
				t.Fatalf("opaque typed IDs must decode: %v", err)
			}
			if decision.Steps[0].Dispatch == nil || decision.Steps[0].Dispatch.AgentID != ids[0] ||
				decision.Steps[1].Join == nil || len(decision.Steps[1].Join.Children.IDs) != 1 || decision.Steps[1].Join.Children.IDs[0] != ids[1] {
				t.Fatalf("opaque ID round trip mismatch: %+v", decision)
			}
		})
	}
}

func TestDecodePlanDecisionV2ClassifiesCrossStepSemanticFailures(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "forward knowledge", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"dispatch","agent_id":"` + validAgentID + `","title":"t","instruction":"i","acceptance":["a"],"knowledge_from":1},{"verb":"consult_knowledge","corpus":"docs","terms":["x"]},{"verb":"join","children":"all"}]}`},
		{name: "missing barrier", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"dispatch","agent_id":"` + validAgentID + `","title":"t","instruction":"i","acceptance":["a"]}]}`},
		{name: "finish bypass", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"dispatch","agent_id":"` + validAgentID + `","title":"t","instruction":"i","acceptance":["a"]},{"verb":"finish"}]}`},
		{name: "after barrier", raw: `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"defer"},{"verb":"finish"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePlanDecisionV2([]byte(tc.raw))
			requirePlanDecisionErrorCode(t, err, domain.GovernanceErrorPlanSemanticValidation)
		})
	}
}

func TestDecodePlanDecisionV2AcceptsEachTerminalShape(t *testing.T) {
	for name, raw := range map[string]string{
		"finish": `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"finish","evaluation":true}]}`,
		"defer":  `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"defer","wake_at":"2026-09-01T12:00:00Z"}]}`,
		"join":   `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"join","children":["wi_01ARZ3NDEKTSV4RRFFQ69G5FAV"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePlanDecisionV2([]byte(raw)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeCoordinatorPlanTextUsesTheRawCanonicalDecoder(t *testing.T) {
	canonical := `{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"finish"}]}`
	decision, source, found, err := DecodeCoordinatorPlanText(canonical)
	if err != nil || !found || source != PlanCandidateNativeText || decision == nil || decision.Steps[0].Finish == nil {
		t.Fatalf("decision=%+v source=%s found=%v err=%v", decision, source, found, err)
	}
	if _, _, found, err := DecodeCoordinatorPlanText("```plan\n" + canonical + "\n```"); found || err != nil {
		t.Fatalf("Markdown fence must not enter the raw decoder: found=%v err=%v", found, err)
	}
}

func TestDecodeCoordinatorPlanTextRejectsMarkdownAndProse(t *testing.T) {
	for name, text := range map[string]string{
		"leading prose": "explanation {\"schema_version\":\"plan-decision/v2\"}",
		"fenced object": "```plan\n{\"schema_version\":\"plan-decision/v2\"}\n```",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, found, err := DecodeCoordinatorPlanText(text)
			if found || err != nil {
				t.Fatalf("non-raw output must remain a no-plan result: found=%v err=%v", found, err)
			}
		})
	}
	decision, _, found, err := DecodeCoordinatorPlanText("plain blocker prose")
	if found || decision != nil || err != nil {
		t.Fatalf("prose without a control candidate remains a non-format result: decision=%+v found=%v err=%v", decision, found, err)
	}
}

func TestDecodeCoordinatorPlanTextTreatsBareLegacyArrayAsSchemaFailure(t *testing.T) {
	_, source, found, err := DecodeCoordinatorPlanText(`[{"verb":"finish"}]`)
	if !found || source != PlanCandidateNativeText {
		t.Fatalf("bare JSON array must enter the canonical decoder: source=%s found=%v", source, found)
	}
	requirePlanDecisionErrorCode(t, err, domain.GovernanceErrorPlanSchemaValidation)
}
