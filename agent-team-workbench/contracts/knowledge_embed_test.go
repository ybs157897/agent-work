package contracts

import (
	"bytes"
	"testing"
)

func TestKnowledgeContractRejectsMultipleActionsAndUnboundedReferences(t *testing.T) {
	valid := []byte(`{"schema_version":"knowledge-librarian/v1","action":"search","search":{"terms":["A"]}}`)
	if err := ValidateKnowledgeLibrarianJSON(valid); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		[]byte(`{"schema_version":"knowledge-librarian/v1","action":"search","search":{"terms":["A"]},"finish":{"status":"complete"}}`),
		[]byte(`{"schema_version":"knowledge-librarian/v1","action":"relations","relations":{"item_ids":["A"],"depth":999}}`),
		bytes.Replace(valid, []byte(`"terms"`), []byte(`"arbitrary_tool"`), 1),
	} {
		if err := ValidateKnowledgeLibrarianJSON(raw); err == nil {
			t.Fatalf("invalid decision accepted: %s", raw)
		}
	}
}
