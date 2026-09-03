package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validGoalForTest() Goal {
	return Goal{
		ID:                        NewID(PrefixGoal),
		WorkspaceID:               NewID(PrefixWorkspace),
		RootWorkItemID:            NewID(PrefixWorkItem),
		Objective:                 "ship the bounded governance slice",
		AcceptanceContract:        []string{"domain tests pass"},
		Status:                    GoalDraft,
		Phase:                     "execution",
		QuotaPolicies:             []QuotaPolicy{{Kind: QuotaTurnCount, Limit: 10, Enforcement: QuotaEnforcementAudit}},
		CompletionEvidenceSummary: []GovernanceEvidenceItem{},
		Version:                   1,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
}

func validScopeForTest() DecisionScope {
	return DecisionScope{
		WorkItemIDs:         []string{NewID(PrefixWorkItem)},
		AgentIDs:            []string{NewID(PrefixAgent)},
		RuntimeCapabilities: []string{RuntimeCapabilityStructuredTransport},
		WriteScopes:         []string{"internal/domain"},
		MaxDispatch:         2,
	}
}

func validTodoForTest() Todo {
	goal := validGoalForTest()
	return Todo{
		ID:              NewID(PrefixTodo),
		GoalID:          goal.ID,
		Class:           TodoAdvancement,
		Status:          TodoPending,
		Instruction:     "implement and verify the next bounded action",
		Acceptance:      []string{"focused domain tests pass"},
		ResumeCondition: "resume after the focused check completes",
		Priority:        PriorityHigh,
		Predecessors:    []string{},
		Successors:      []string{},
		DecisionScope:   validScopeForTest(),
		LastTurnSeq:     0,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func TestGoalStateMachineIsClosedAndPauseMapsToWaiting(t *testing.T) {
	cases := []struct {
		from, to GoalStatus
		ok       bool
	}{
		{GoalDraft, GoalActive, true},
		{GoalDraft, GoalCancelled, true},
		{GoalActive, GoalWaiting, true},
		{GoalActive, GoalBlocked, true},
		{GoalActive, GoalCancelled, true},
		{GoalWaiting, GoalActive, true},
		{GoalWaiting, GoalBlocked, true},
		{GoalWaiting, GoalCancelled, true},
		{GoalBlocked, GoalActive, true},
		{GoalBlocked, GoalCancelled, true},
		{GoalCompleted, GoalActive, false},
		{GoalCancelled, GoalActive, false},
		{GoalActive, GoalDraft, false},
	}
	for _, tc := range cases {
		g := validGoalForTest()
		g.Status = tc.from
		err := g.Transition(tc.to, now)
		if tc.ok != (err == nil) {
			t.Errorf("%s -> %s: want ok=%v, err=%v", tc.from, tc.to, tc.ok, err)
		}
		if !tc.ok && err != nil && !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s -> %s: want ErrIllegalTransition, err=%v", tc.from, tc.to, err)
		}
	}

	g := validGoalForTest()
	g.Status = GoalActive
	if err := g.Pause(now); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if g.Status != GoalWaiting {
		t.Fatalf("pause must map to waiting, got %q", g.Status)
	}
	if err := g.Resume(now); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if g.Status != GoalActive {
		t.Fatalf("resume must return active, got %q", g.Status)
	}
}

func TestGoalCompletionRequiresEvidence(t *testing.T) {
	g := validGoalForTest()
	g.Status = GoalActive
	g.CompletionEvidenceSummary = []GovernanceEvidenceItem{{
		SourceKind:   EvidenceSourceValidationResult,
		SourceID:     "validation_1",
		Verification: EvidenceVerificationPassed,
		Summary:      "focused tests passed",
		RecordedAt:   now,
	}}
	if err := g.Transition(GoalCompleted, now); !errors.Is(err, ErrValidation) {
		t.Fatalf("passed validation alone must not complete a goal, got %v", err)
	}
	g.CompletionEvidenceSummary = append(g.CompletionEvidenceSummary, GovernanceEvidenceItem{
		SourceKind:   EvidenceSourceWorkItem,
		SourceID:     NewID(PrefixWorkItem),
		Verification: EvidenceVerificationAccepted,
		Summary:      "another work item accepted",
		RecordedAt:   now,
	})
	if err := g.Transition(GoalCompleted, now); !errors.Is(err, ErrValidation) {
		t.Fatalf("another work item accepted must not complete the goal, got %v", err)
	}
	g.CompletionEvidenceSummary = append(g.CompletionEvidenceSummary, GovernanceEvidenceItem{
		SourceKind:   EvidenceSourceWorkItem,
		SourceID:     g.RootWorkItemID,
		Verification: EvidenceVerificationAccepted,
		Summary:      "root task acceptance contract accepted",
		RecordedAt:   now,
	})
	if err := g.Transition(GoalCompleted, now); err != nil {
		t.Fatalf("root work item accepted: %v", err)
	}

	// Failed evidence remains in the audit summary; a later accepted projection
	// can still complete the Goal after the application acceptance gate passes.
	g = validGoalForTest()
	g.Status = GoalActive
	g.CompletionEvidenceSummary = []GovernanceEvidenceItem{
		{SourceKind: EvidenceSourceValidationResult, SourceID: "validation_old", Verification: EvidenceVerificationFailed, Summary: "old check failed", RecordedAt: now},
		{SourceKind: EvidenceSourceWorkItem, SourceID: g.RootWorkItemID, Verification: EvidenceVerificationAccepted, Summary: "later root task acceptance", RecordedAt: now},
	}
	if !g.CompletionReady() {
		t.Fatal("historical failed evidence must not veto a later accepted projection")
	}
}

func TestGoalValidateAndVersion(t *testing.T) {
	g := validGoalForTest()
	if err := g.Validate(); err != nil {
		t.Fatalf("valid goal rejected: %v", err)
	}
	if err := g.CheckVersion(2); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
	if err := g.CheckVersion(1); err != nil {
		t.Fatal(err)
	}
	if err := g.CheckVersion(0); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		mut  func(*Goal)
	}{
		{"missing id", func(g *Goal) { g.ID = "" }},
		{"wrong id prefix", func(g *Goal) { g.ID = "todo_bad" }},
		{"missing objective", func(g *Goal) { g.Objective = "  " }},
		{"missing acceptance", func(g *Goal) { g.AcceptanceContract = nil }},
		{"unknown status", func(g *Goal) { g.Status = GoalStatus("paused") }},
		{"empty phase", func(g *Goal) { g.Phase = "" }},
		{"bad current todo", func(g *Goal) { g.CurrentTodoID = "goal_bad" }},
		{"zero version", func(g *Goal) { g.Version = 0 }},
		{"missing created at", func(g *Goal) { g.CreatedAt = time.Time{} }},
		{"missing updated at", func(g *Goal) { g.UpdatedAt = time.Time{} }},
		{"duplicate quota kind", func(g *Goal) {
			g.QuotaPolicies = append(g.QuotaPolicies, g.QuotaPolicies[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := g
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestTodoValidateClaimScopeAndRunnerLeaseSeparation(t *testing.T) {
	todo := validTodoForTest()
	if err := todo.Validate(); err != nil {
		t.Fatalf("valid todo rejected: %v", err)
	}
	if strings.Contains(strings.ToLower(string(todo.Status)), "lease") {
		t.Fatal("todo status must not model runner lease")
	}

	claimedAt := now
	todo.Claim = &TodoClaim{
		OwnerAgentID: todo.DecisionScope.AgentIDs[0],
		Version:      1,
		ClaimedAt:    claimedAt,
		ExpiresAt:    claimedAt.Add(time.Hour),
	}
	todo.ClaimVersion = 1
	todo.Status = TodoClaimed
	if err := todo.Validate(); err != nil {
		t.Fatalf("valid governance claim rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*Todo)
	}{
		{"missing goal id", func(t *Todo) { t.GoalID = "" }},
		{"unknown class", func(t *Todo) { t.Class = TodoClass("other") }},
		{"unknown status", func(t *Todo) { t.Status = TodoStatus("paused") }},
		{"empty instruction", func(t *Todo) { t.Instruction = "" }},
		{"empty acceptance", func(t *Todo) { t.Acceptance = nil }},
		{"bad priority", func(t *Todo) { t.Priority = Priority("normal") }},
		{"missing scope work item", func(t *Todo) { t.DecisionScope.WorkItemIDs = nil }},
		{"bad claim owner", func(t *Todo) { t.Claim.OwnerAgentID = "runtime_lease_owner" }},
		{"claim owner outside scope", func(t *Todo) { t.Claim.OwnerAgentID = NewID(PrefixAgent) }},
		{"claim expiry before claim", func(t *Todo) { t.Claim.ExpiresAt = t.Claim.ClaimedAt.Add(-time.Second) }},
		{"running without claim", func(t *Todo) { t.Status = TodoRunning; t.Claim = nil }},
		{"claim generation mismatch", func(t *Todo) { t.ClaimVersion++ }},
		{"missing created at", func(t *Todo) { t.CreatedAt = time.Time{} }},
		{"missing updated at", func(t *Todo) { t.UpdatedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := todo
			bad.Predecessors = append([]string(nil), todo.Predecessors...)
			bad.Successors = append([]string(nil), todo.Successors...)
			bad.DecisionScope.WorkItemIDs = append([]string(nil), todo.DecisionScope.WorkItemIDs...)
			bad.DecisionScope.AgentIDs = append([]string(nil), todo.DecisionScope.AgentIDs...)
			bad.DecisionScope.RuntimeCapabilities = append([]string(nil), todo.DecisionScope.RuntimeCapabilities...)
			bad.DecisionScope.WriteScopes = append([]string(nil), todo.DecisionScope.WriteScopes...)
			if todo.Claim != nil {
				claim := *todo.Claim
				bad.Claim = &claim
			}
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestTodoRunningRequiresActiveClaim(t *testing.T) {
	todo := validTodoForTest()
	todo.Status = TodoClaimed
	if err := todo.Transition(TodoRunning, now); !errors.Is(err, ErrValidation) {
		t.Fatalf("running without active claim must fail, got %v", err)
	}
	if err := todo.ClaimBy(todo.DecisionScope.AgentIDs[0], now, now.Add(time.Hour)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := todo.Transition(TodoRunning, now); err != nil {
		t.Fatalf("running with active claim: %v", err)
	}
}

func TestTodoClaimVersionMonotonicAcrossRelease(t *testing.T) {
	todo := validTodoForTest()
	owner := todo.DecisionScope.AgentIDs[0]
	if err := todo.ClaimBy(owner, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if todo.Claim == nil || todo.Claim.Version != 1 || todo.ClaimVersion != 1 {
		t.Fatalf("first claim generation = %+v, claim_version=%d", todo.Claim, todo.ClaimVersion)
	}
	if err := todo.ReleaseClaim(now.Add(2 * time.Hour)); err != nil {
		t.Fatalf("release claim: %v", err)
	}
	if todo.Claim != nil || todo.ClaimVersion != 2 {
		t.Fatalf("release must clear active claim but retain generation, claim=%+v claim_version=%d", todo.Claim, todo.ClaimVersion)
	}
	if err := todo.ClaimBy(owner, now.Add(3*time.Hour), now.Add(4*time.Hour)); err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if todo.Claim == nil || todo.Claim.Version != 3 || todo.ClaimVersion != 3 {
		t.Fatalf("second claim generation = %+v, claim_version=%d", todo.Claim, todo.ClaimVersion)
	}
}

func TestDecisionScopeCapabilitiesAreDomainClosedSet(t *testing.T) {
	valid := validScopeForTest()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid scope rejected: %v", err)
	}
	for _, capability := range []string{
		RuntimeCapabilityStructuredTransport,
		RuntimeCapabilitySchemaConstrainedOutput,
		RuntimeCapabilityControlToolCall,
	} {
		scope := valid
		scope.RuntimeCapabilities = []string{capability}
		if err := scope.Validate(); err != nil {
			t.Errorf("capability %q rejected: %v", capability, err)
		}
	}
	for _, bad := range []string{"runtime_name", "resume", ""} {
		scope := valid
		scope.RuntimeCapabilities = []string{bad}
		if err := scope.Validate(); !errors.Is(err, ErrValidation) {
			t.Errorf("capability %q should be rejected, got %v", bad, err)
		}
	}

	cases := []struct {
		name string
		mut  func(*DecisionScope)
	}{
		{"duplicate work item", func(s *DecisionScope) { s.WorkItemIDs = append(s.WorkItemIDs, s.WorkItemIDs[0]) }},
		{"too many agents", func(s *DecisionScope) { s.AgentIDs = make([]string, 129) }},
		{"too many capabilities", func(s *DecisionScope) {
			s.RuntimeCapabilities = []string{"structured_transport", "schema_constrained_output", "control_tool_call", "extra"}
		}},
		{"negative dispatch", func(s *DecisionScope) { s.MaxDispatch = -1 }},
		{"dispatch over limit", func(s *DecisionScope) { s.MaxDispatch = 65 }},
		{"absolute write scope", func(s *DecisionScope) { s.WriteScopes = []string{"/tmp"} }},
		{"parent write scope", func(s *DecisionScope) { s.WriteScopes = []string{"../outside"} }},
		{"windows parent write scope", func(s *DecisionScope) { s.WriteScopes = []string{`..\outside`} }},
		{"windows drive write scope", func(s *DecisionScope) { s.WriteScopes = []string{`C:\tmp`} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := valid
			bad.WorkItemIDs = append([]string(nil), valid.WorkItemIDs...)
			bad.AgentIDs = append([]string(nil), valid.AgentIDs...)
			bad.RuntimeCapabilities = append([]string(nil), valid.RuntimeCapabilities...)
			bad.WriteScopes = append([]string(nil), valid.WriteScopes...)
			tc.mut(&bad)
			if err := bad.Validate(); !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
		})
	}
}

func TestGovernanceEvidenceAndTurnDecisionRequireRecordedTimestamps(t *testing.T) {
	evidence := GovernanceEvidenceItem{
		SourceKind:   EvidenceSourceRun,
		SourceID:     NewID(PrefixRun),
		Verification: EvidenceVerificationObserved,
		Summary:      "run observed",
		RecordedAt:   now,
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
	missingEvidenceTime := evidence
	missingEvidenceTime.RecordedAt = time.Time{}
	if err := missingEvidenceTime.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("evidence without recorded_at must fail, got %v", err)
	}

	decision := TurnDecision{
		TurnKey:       validTurnKeyForTest(),
		Decision:      TurnDecisionWait,
		Reason:        "wait for the next bounded event",
		NextAction:    "observe the todo",
		SchemaVersion: "turn-decision/v1",
		RecordedAt:    now,
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("valid turn decision rejected: %v", err)
	}
	decision.RecordedAt = time.Time{}
	if err := decision.Validate(); !errors.Is(err, ErrValidation) {
		t.Fatalf("turn decision without recorded_at must fail, got %v", err)
	}
}
