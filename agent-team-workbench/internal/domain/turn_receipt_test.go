package domain

import (
	"errors"
	"testing"
	"time"
)

func validTurnKeyForTest() TurnKey {
	return TurnKey{GoalID: NewID(PrefixGoal), TodoID: NewID(PrefixTodo), TurnSeq: 1}
}

func validHeaderForTest() TurnReceiptHeader {
	return TurnReceiptHeader{
		TurnKey:             validTurnKeyForTest(),
		Attempt:             1,
		SchemaVersion:       "turn-receipt/v1",
		InputSnapshotDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		AdmissionClientKey:  "admission-client-1",
		CanonicalDigest:     "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		CreatedAt:           now,
	}
}

func validPhaseForTest(seq int) TurnReceiptPhase {
	name, ok := TurnReceiptPhaseNameForSeq(seq)
	if !ok {
		panic("invalid phase seq in test")
	}
	phase := TurnReceiptPhase{
		TurnKey:         validTurnKeyForTest(),
		PhaseSeq:        seq,
		Phase:           name,
		Payload:         map[string]any{"ok": true},
		CanonicalDigest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CreatedAt:       now,
	}
	if name == TurnReceiptPhaseValidation {
		phase.Payload = map[string]any{"valid": true}
	}
	if name == TurnReceiptPhasePlanCompile {
		phase.PlanID = NewID(PrefixPlan)
		phase.Payload = map[string]any{
			"plan_id": phase.PlanID, "plan_client_key": "governance:test",
			"decision_digest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}
	}
	if name == TurnReceiptPhaseDispatch {
		phase.PlanID = NewID(PrefixPlan)
		phase.Payload = map[string]any{"plan_id": phase.PlanID, "dispatch_state": "no_runs", "run_count": 0}
	}
	return phase
}

func TestTurnKeyValidateAndIdentity(t *testing.T) {
	k := validTurnKeyForTest()
	if err := k.Validate(); err != nil {
		t.Fatalf("valid turn key rejected: %v", err)
	}
	if !k.Equal(k) {
		t.Fatal("turn key must equal itself")
	}
	other := k
	other.TurnSeq++
	if k.Equal(other) {
		t.Fatal("different turn sequence must be a different identity")
	}
	for _, bad := range []TurnKey{
		{GoalID: "todo_bad", TodoID: k.TodoID, TurnSeq: 1},
		{GoalID: k.GoalID, TodoID: "goal_bad", TurnSeq: 1},
		{GoalID: k.GoalID, TodoID: k.TodoID, TurnSeq: 0},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrValidation) {
			t.Errorf("bad key %+v: want ErrValidation, got %v", bad, err)
		}
	}
}

func TestTurnReceiptHeaderValidateDigestShape(t *testing.T) {
	h := validHeaderForTest()
	if err := h.Validate(); err != nil {
		t.Fatalf("valid header rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*TurnReceiptHeader)
	}{
		{"attempt zero", func(h *TurnReceiptHeader) { h.Attempt = 0 }},
		{"missing schema", func(h *TurnReceiptHeader) { h.SchemaVersion = "" }},
		{"input wrong prefix", func(h *TurnReceiptHeader) {
			h.InputSnapshotDigest = "sha512:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		}},
		{"input uppercase", func(h *TurnReceiptHeader) {
			h.InputSnapshotDigest = "sha256:0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"
		}},
		{"input short", func(h *TurnReceiptHeader) { h.InputSnapshotDigest = "sha256:0123" }},
		{"canonical uppercase", func(h *TurnReceiptHeader) {
			h.CanonicalDigest = "sha256:ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
		}},
		{"missing client key", func(h *TurnReceiptHeader) { h.AdmissionClientKey = "" }},
		{"missing created at", func(h *TurnReceiptHeader) { h.CreatedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := h
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestTurnReceiptPhaseSequenceMappingAndIdentity(t *testing.T) {
	want := []TurnReceiptPhaseName{
		TurnReceiptPhaseDecisionDecode,
		TurnReceiptPhaseValidation,
		TurnReceiptPhaseDurableWriteback,
		TurnReceiptPhasePlanCompile,
		TurnReceiptPhaseDispatch,
		TurnReceiptPhaseQuotaSpend,
		TurnReceiptPhaseProjectionOutbox,
	}
	for seq, name := range want {
		got, ok := TurnReceiptPhaseNameForSeq(seq + 1)
		if !ok || got != name {
			t.Fatalf("seq %d: got %q/%v, want %q/true", seq+1, got, ok, name)
		}
		gotSeq, ok := TurnReceiptPhaseSeq(name)
		if !ok || gotSeq != seq+1 {
			t.Fatalf("phase %q: got %d/%v, want %d/true", name, gotSeq, ok, seq+1)
		}
		phase := validPhaseForTest(seq + 1)
		if err := phase.Validate(); err != nil {
			t.Fatalf("phase %d valid fixture rejected: %v", seq+1, err)
		}
		phase.Phase = want[(seq+1)%len(want)]
		if err := phase.Validate(); !errors.Is(err, ErrValidation) {
			t.Fatalf("phase %d with mismatched name should fail, got %v", seq+1, err)
		}
	}
	if _, ok := TurnReceiptPhaseNameForSeq(0); ok {
		t.Fatal("phase sequence zero must be rejected")
	}
	if _, ok := TurnReceiptPhaseNameForSeq(8); ok {
		t.Fatal("phase sequence eight must be rejected")
	}
	if _, ok := TurnReceiptPhaseSeq(TurnReceiptPhaseName("unknown")); ok {
		t.Fatal("unknown phase must be rejected")
	}
}

func TestTurnReceiptPhaseValidateReferencesAndDigest(t *testing.T) {
	p := validPhaseForTest(1)
	p.PlanID = NewID(PrefixPlan)
	p.RunIDs = []string{NewID(PrefixRun)}
	p.QuotaReservationKeys = []string{"quota-key-1"}
	p.Evidence = []GovernanceEvidenceItem{{
		SourceKind:   EvidenceSourceRun,
		SourceID:     p.RunIDs[0],
		Verification: EvidenceVerificationObserved,
		Summary:      "run observed",
		RecordedAt:   now,
	}}
	if err := p.Validate(); err != nil {
		t.Fatalf("valid phase references rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*TurnReceiptPhase)
	}{
		{"nil payload", func(p *TurnReceiptPhase) { p.Payload = nil }},
		{"bad plan id", func(p *TurnReceiptPhase) { p.PlanID = "run_bad" }},
		{"bad run id", func(p *TurnReceiptPhase) { p.RunIDs = []string{"plan_bad"} }},
		{"duplicate run id", func(p *TurnReceiptPhase) { p.RunIDs = append(p.RunIDs, p.RunIDs[0]) }},
		{"bad quota key", func(p *TurnReceiptPhase) { p.QuotaReservationKeys = []string{""} }},
		{"bad digest", func(p *TurnReceiptPhase) {
			p.CanonicalDigest = "sha256:0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"
		}},
		{"missing created at", func(p *TurnReceiptPhase) { p.CreatedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := p
			bad.RunIDs = append([]string(nil), p.RunIDs...)
			bad.QuotaReservationKeys = append([]string(nil), p.QuotaReservationKeys...)
			bad.Evidence = append([]GovernanceEvidenceItem(nil), p.Evidence...)
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestTurnReceiptValidationPhaseCarriesDecisionOutcome(t *testing.T) {
	valid := validPhaseForTest(2)
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	missing := valid
	missing.Payload = map[string]any{}
	if err := missing.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("validation phase without valid must fail closed, got %v", err)
	}
	invalid := valid
	invalid.Payload = map[string]any{"valid": false, "error_code": string(GovernanceErrorPlanSchemaValidation)}
	if err := invalid.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid validation phase without path must fail closed, got %v", err)
	}
	invalid.Payload["path"] = "/steps/0"
	if err := invalid.Validate(); err != nil {
		t.Fatalf("invalid validation phase with error details rejected: %v", err)
	}
}

func TestTurnReceiptPlanCompileAndDispatchSemanticContract(t *testing.T) {
	planCompile := validPhaseForTest(4)
	if err := planCompile.Validate(); err != nil {
		t.Fatal(err)
	}
	missingPlan := planCompile
	missingPlan.PlanID = ""
	if err := missingPlan.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("plan_compile without Plan reference must fail, got %v", err)
	}
	missingClient := planCompile
	missingClient.Payload = map[string]any{"plan_id": missingClient.PlanID, "decision_digest": missingClient.Payload["decision_digest"]}
	if err := missingClient.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("plan_compile without client key must fail, got %v", err)
	}

	dispatch := validPhaseForTest(5)
	if err := dispatch.Validate(); err != nil {
		t.Fatal(err)
	}
	badState := dispatch
	badState.Payload = map[string]any{"plan_id": badState.PlanID, "dispatch_state": "unknown", "run_count": 0}
	if err := badState.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("dispatch with unknown state must fail, got %v", err)
	}
	badCount := dispatch
	badCount.RunIDs = []string{NewID(PrefixRun)}
	if err := badCount.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("dispatch run_count mismatch must fail, got %v", err)
	}
}

func TestTurnReceiptValidateAllowsAppendOnlyPrefix(t *testing.T) {
	header := validHeaderForTest()
	header.TurnKey = validTurnKeyForTest()
	receipt := TurnReceipt{Header: header, Phases: []TurnReceiptPhase{}}
	for seq := 1; seq <= 7; seq++ {
		phase := validPhaseForTest(seq)
		phase.TurnKey = header.TurnKey
		receipt.Phases = append(receipt.Phases, phase)
		if err := receipt.Validate(); err != nil {
			t.Fatalf("receipt prefix through phase %d rejected: %v", seq, err)
		}
	}
	receipt.Phases[1].PhaseSeq = 3
	if err := receipt.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-contiguous phase sequence should fail, got %v", err)
	}
}
