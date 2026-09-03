package domain

import (
	"encoding/json"
	"testing"
)

func TestPlanDecisionV2TypedUnionRoundTrip(t *testing.T) {
	raw := []byte(`{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"consult_knowledge","corpus":"docs","terms":["goal"],"limit":3},{"verb":"dispatch","agent_id":"agent_01ARZ3NDEKTSV4RRFFQ69G5FAV","title":"t","instruction":"i","acceptance":["a"],"knowledge_from":0},{"verb":"join","children":"all"}]}`)
	var decision PlanDecisionV2
	if err := json.Unmarshal(raw, &decision); err != nil {
		t.Fatal(err)
	}
	if len(decision.Steps) != 3 || decision.Steps[0].ConsultKnowledge == nil ||
		decision.Steps[1].Dispatch == nil || decision.Steps[2].Join == nil || !decision.Steps[2].Join.Children.All {
		t.Fatalf("typed union mismatch: %+v", decision.Steps)
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	var replay PlanDecisionV2
	if err := json.Unmarshal(encoded, &replay); err != nil || len(replay.Steps) != 3 {
		t.Fatalf("round trip failed: %s err=%v", encoded, err)
	}
}

func TestPlanDecisionV2TypedUnionRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{"schema_version":"plan-decision/v2","kind":"plan","reason":"r","next_action":"n","steps":[{"verb":"finish","evaluation":false,"summary":"legacy"}]}`)
	var decision PlanDecisionV2
	if err := json.Unmarshal(raw, &decision); err == nil {
		t.Fatal("typed decoder must reject legacy/unknown step fields")
	}
}
