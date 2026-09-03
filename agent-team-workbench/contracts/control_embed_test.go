package contracts

import (
	"encoding/json"
	"testing"
)

func TestEmbeddedPlanDecisionV2SchemaIsCanonicalFile(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(PlanDecisionV2Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	properties, _ := schema["properties"].(map[string]any)
	version, _ := properties["schema_version"].(map[string]any)
	if version["const"] != "plan-decision/v2" {
		t.Fatalf("embedded schema version=%v", version["const"])
	}
}
