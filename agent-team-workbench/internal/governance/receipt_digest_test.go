package governance

import (
	"math"
	"testing"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testTurnKey() domain.TurnKey {
	return domain.TurnKey{GoalID: domain.NewID(domain.PrefixGoal), TodoID: domain.NewID(domain.PrefixTodo), TurnSeq: 1}
}

func testReceiptHeader() *domain.TurnReceiptHeader {
	return &domain.TurnReceiptHeader{
		TurnKey: testTurnKey(), Attempt: 1, SchemaVersion: "turn-receipt/v1",
		InputSnapshotDigest: testDigest, AdmissionClientKey: "admission-1", CanonicalDigest: testDigest,
		CreatedAt: time.Date(2026, 9, 1, 2, 3, 4, 500, time.FixedZone("offset", 8*60*60)),
	}
}

func TestHeaderDigestIsCanonicalAndExcludesDigestField(t *testing.T) {
	header := testReceiptHeader()
	first, err := ComputeHeaderDigest(header)
	if err != nil {
		t.Fatal(err)
	}
	header.CanonicalDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	second, err := ComputeHeaderDigest(header)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !domain.ValidCanonicalDigest(first) {
		t.Fatalf("canonical digest mismatch: first=%q second=%q", first, second)
	}
	header.CanonicalDigest = first
	if err := VerifyHeaderDigest(header); err != nil {
		t.Fatal(err)
	}
	header.Attempt++
	if err := VerifyHeaderDigest(header); err == nil {
		t.Fatal("tampered immutable Header field must fail digest verification")
	}
}

func TestPhaseDigestCanonicalizesNestedObjectOrder(t *testing.T) {
	base := &domain.TurnReceiptPhase{
		TurnKey: testTurnKey(), PhaseSeq: 1, Phase: domain.TurnReceiptPhaseDecisionDecode,
		Payload:         map[string]any{"z": []any{true, float64(4.5)}, "a": map[string]any{"b": "value", "a": 1}},
		CanonicalDigest: testDigest, CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	first, err := ComputePhaseDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := *base
	reordered.Payload = map[string]any{"a": map[string]any{"a": 1, "b": "value"}, "z": []any{true, float64(4.5)}}
	second, err := ComputePhaseDigest(&reordered)
	if err != nil || first != second {
		t.Fatalf("RFC8785 object order mismatch: first=%q second=%q err=%v", first, second, err)
	}
	reordered.CanonicalDigest = first
	if err := VerifyPhaseDigest(&reordered); err != nil {
		t.Fatal(err)
	}
	reordered.Payload["bad"] = math.NaN()
	if _, err := ComputePhaseDigest(&reordered); err == nil {
		t.Fatal("I-JSON-incompatible NaN must be rejected")
	}
}

func TestCanonicalJSONMatchesRFC8785Vector(t *testing.T) {
	value := map[string]any{
		"numbers": []any{333333333.33333329, 1e30, 4.50, 2e-3, 1e-27},
		"string":  "€$\u000f\nA'B\"\\\\\"/", "literals": []any{nil, true, false},
	}
	canonical, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	if string(canonical) != want {
		t.Fatalf("RFC8785 vector mismatch:\n got %s\nwant %s", canonical, want)
	}
}
