package domain

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// GoalStatus is the lifecycle of a long-running governance goal. A paused
// goal is represented by GoalWaiting; there is intentionally no paused state.
type GoalStatus string

const (
	GoalDraft     GoalStatus = "draft"
	GoalActive    GoalStatus = "active"
	GoalWaiting   GoalStatus = "waiting"
	GoalBlocked   GoalStatus = "blocked"
	GoalCompleted GoalStatus = "completed"
	GoalCancelled GoalStatus = "cancelled"
)

func (s GoalStatus) Valid() bool {
	switch s {
	case GoalDraft, GoalActive, GoalWaiting, GoalBlocked, GoalCompleted, GoalCancelled:
		return true
	default:
		return false
	}
}

func (s GoalStatus) IsTerminal() bool {
	return s == GoalCompleted || s == GoalCancelled
}

var goalTransitions = map[GoalStatus][]GoalStatus{
	GoalDraft:   {GoalActive, GoalBlocked, GoalCancelled},
	GoalActive:  {GoalWaiting, GoalBlocked, GoalCompleted, GoalCancelled},
	GoalWaiting: {GoalActive, GoalBlocked, GoalCompleted, GoalCancelled},
	GoalBlocked: {GoalActive, GoalWaiting, GoalCancelled},
}

func (s GoalStatus) CanTransitionTo(to GoalStatus) bool {
	for _, candidate := range goalTransitions[s] {
		if candidate == to {
			return true
		}
	}
	return false
}

// Goal is the durable intent aggregate above WorkItem/Plan/Run. It owns the
// objective, acceptance contract, current Todo projection and quota policy;
// it deliberately does not own a list of execution runs.
type Goal struct {
	ID                        string                   `json:"id"`
	WorkspaceID               string                   `json:"workspace_id"`
	RootWorkItemID            string                   `json:"root_work_item_id"`
	Objective                 string                   `json:"objective"`
	AcceptanceContract        []string                 `json:"acceptance_contract"`
	Status                    GoalStatus               `json:"status"`
	Phase                     string                   `json:"phase"`
	CurrentTodoID             string                   `json:"current_todo_id,omitempty"`
	QuotaPolicies             []QuotaPolicy            `json:"quota_policies"`
	CompletionEvidenceSummary []GovernanceEvidenceItem `json:"completion_evidence_summary"`
	Version                   int                      `json:"version"`
	CreatedAt                 time.Time                `json:"created_at"`
	UpdatedAt                 time.Time                `json:"updated_at"`
}

// Validate checks the static aggregate contract. Cross-aggregate ownership
// (for example, that RootWorkItemID belongs to WorkspaceID) remains an
// application/persistence concern.
func (g *Goal) Validate() error {
	if g == nil {
		return fmt.Errorf("%w: nil goal", ErrValidation)
	}
	if err := validateTypedID("goal.id", g.ID, PrefixGoal); err != nil {
		return err
	}
	if err := validateTypedID("goal.workspace_id", g.WorkspaceID, PrefixWorkspace); err != nil {
		return err
	}
	if err := validateTypedID("goal.root_work_item_id", g.RootWorkItemID, PrefixWorkItem); err != nil {
		return err
	}
	if err := validateText("goal.objective", g.Objective, 20000); err != nil {
		return err
	}
	if len(g.AcceptanceContract) == 0 || len(g.AcceptanceContract) > 64 {
		return fmt.Errorf("%w: goal.acceptance_contract must contain 1..64 items", ErrValidation)
	}
	for i, item := range g.AcceptanceContract {
		if err := validateText(fmt.Sprintf("goal.acceptance_contract[%d]", i), item, 2000); err != nil {
			return err
		}
	}
	if !g.Status.Valid() {
		return fmt.Errorf("%w: goal.status %q", ErrValidation, g.Status)
	}
	if err := validateText("goal.phase", g.Phase, 128); err != nil {
		return err
	}
	if err := validateOptionalTypedID("goal.current_todo_id", g.CurrentTodoID, PrefixTodo); err != nil {
		return err
	}
	if len(g.QuotaPolicies) > 8 {
		return fmt.Errorf("%w: goal.quota_policies exceeds 8 items", ErrValidation)
	}
	seenQuota := make(map[QuotaKind]struct{}, len(g.QuotaPolicies))
	for i := range g.QuotaPolicies {
		policy := &g.QuotaPolicies[i]
		if err := policy.Validate(); err != nil {
			return fmt.Errorf("%w: goal.quota_policies[%d]: %v", ErrValidation, i, err)
		}
		if _, exists := seenQuota[policy.Kind]; exists {
			return fmt.Errorf("%w: duplicate quota policy %q", ErrValidation, policy.Kind)
		}
		seenQuota[policy.Kind] = struct{}{}
	}
	if len(g.CompletionEvidenceSummary) > 128 {
		return fmt.Errorf("%w: goal.completion_evidence_summary exceeds 128 items", ErrValidation)
	}
	for i := range g.CompletionEvidenceSummary {
		if err := g.CompletionEvidenceSummary[i].Validate(); err != nil {
			return fmt.Errorf("%w: goal.completion_evidence_summary[%d]: %v", ErrValidation, i, err)
		}
	}
	if g.Version < 1 {
		return fmt.Errorf("%w: goal.version must be >= 1", ErrValidation)
	}
	if g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: goal timestamps are required", ErrValidation)
	}
	if g.Status == GoalCompleted && !g.CompletionReady() {
		return fmt.Errorf("%w: completed goal requires completion evidence", ErrValidation)
	}
	return nil
}

// CompletionReady is the domain evidence gate for the completed state. The
// acceptance contract itself is evaluated by the application, but the
// projection must contain an explicit accepted decision for this Goal's root
// WorkItem. Other WorkItems and validation results can explain
// progress but cannot substitute for the root Task's manual Accept gate.
// Historical failed evidence is retained for audit and does not permanently
// veto a later accepted projection; current acceptance is an application gate.
func (g *Goal) CompletionReady() bool {
	if g == nil || len(g.CompletionEvidenceSummary) == 0 {
		return false
	}
	accepted := false
	for _, evidence := range g.CompletionEvidenceSummary {
		if evidence.Verification == EvidenceVerificationAccepted &&
			evidence.SourceKind == EvidenceSourceWorkItem && evidence.SourceID == g.RootWorkItemID {
			accepted = true
		}
	}
	return accepted
}

// Transition applies a legal Goal lifecycle transition and bumps the
// optimistic-lock version. Completion is gated by authoritative evidence.
func (g *Goal) Transition(to GoalStatus, now time.Time) error {
	if g == nil {
		return fmt.Errorf("%w: nil goal", ErrValidation)
	}
	if g.Status.IsTerminal() || !g.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "goal", From: string(g.Status), To: string(to)}
	}
	if to == GoalCompleted && !g.CompletionReady() {
		return fmt.Errorf("%w: goal completion requires authoritative evidence", ErrValidation)
	}
	g.Status = to
	g.Version++
	g.UpdatedAt = now
	return nil
}

// Start moves a draft goal into the active execution window.
func (g *Goal) Start(now time.Time) error { return g.Transition(GoalActive, now) }

// Pause maps to waiting. No separate paused state exists in the domain.
func (g *Goal) Pause(now time.Time) error { return g.Transition(GoalWaiting, now) }

// Resume reopens a paused or blocked goal for the next bounded turn.
func (g *Goal) Resume(now time.Time) error { return g.Transition(GoalActive, now) }

// Complete applies the same evidence gate as Transition(GoalCompleted).
func (g *Goal) Complete(now time.Time) error { return g.Transition(GoalCompleted, now) }

// Cancel is the explicit terminal cancellation command.
func (g *Goal) Cancel(now time.Time) error { return g.Transition(GoalCancelled, now) }

// CheckVersion is the standard optimistic-lock guard. expected=0 means the
// caller intentionally delegates the zero-row/concurrency result to storage.
func (g *Goal) CheckVersion(expected int) error {
	if g == nil {
		return fmt.Errorf("%w: nil goal", ErrValidation)
	}
	if expected != 0 && expected != g.Version {
		return ErrVersionConflict
	}
	return nil
}

// TodoClass identifies the bounded action represented by a Todo.
type TodoClass string

const (
	TodoAdvancement TodoClass = "advancement"
	TodoMonitor     TodoClass = "monitor"
	TodoUserGate    TodoClass = "user_gate"
	TodoBlocker     TodoClass = "blocker"
	TodoValidation  TodoClass = "validation"
)

func (c TodoClass) Valid() bool {
	switch c {
	case TodoAdvancement, TodoMonitor, TodoUserGate, TodoBlocker, TodoValidation:
		return true
	default:
		return false
	}
}

// TodoStatus is the lifecycle of a single bounded governance action.
type TodoStatus string

const (
	TodoPending   TodoStatus = "pending"
	TodoClaimed   TodoStatus = "claimed"
	TodoRunning   TodoStatus = "running"
	TodoWaiting   TodoStatus = "waiting"
	TodoCompleted TodoStatus = "completed"
	TodoBlocked   TodoStatus = "blocked"
	TodoCancelled TodoStatus = "cancelled"
)

func (s TodoStatus) Valid() bool {
	switch s {
	case TodoPending, TodoClaimed, TodoRunning, TodoWaiting, TodoCompleted, TodoBlocked, TodoCancelled:
		return true
	default:
		return false
	}
}

func (s TodoStatus) IsTerminal() bool {
	return s == TodoCompleted || s == TodoCancelled
}

var todoTransitions = map[TodoStatus][]TodoStatus{
	TodoPending:   {TodoClaimed, TodoWaiting, TodoBlocked, TodoCancelled},
	TodoClaimed:   {TodoRunning, TodoPending, TodoWaiting, TodoBlocked, TodoCancelled},
	TodoRunning:   {TodoWaiting, TodoCompleted, TodoBlocked, TodoCancelled},
	TodoWaiting:   {TodoPending, TodoClaimed, TodoCompleted, TodoBlocked, TodoCancelled},
	TodoBlocked:   {TodoPending, TodoClaimed, TodoWaiting, TodoCancelled},
	TodoCompleted: {},
	TodoCancelled: {},
}

func (s TodoStatus) CanTransitionTo(to TodoStatus) bool {
	for _, candidate := range todoTransitions[s] {
		if candidate == to {
			return true
		}
	}
	return false
}

// TodoClaim is governance ownership. It intentionally has no runner lease or
// process/session fields; Runner lease remains an execution-plane concern.
type TodoClaim struct {
	OwnerAgentID string    `json:"owner_agent_id"`
	Version      int       `json:"version"`
	ClaimedAt    time.Time `json:"claimed_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (c *TodoClaim) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: nil todo claim", ErrValidation)
	}
	if err := validateTypedID("todo.claim.owner_agent_id", c.OwnerAgentID, PrefixAgent); err != nil {
		return err
	}
	if c.Version < 1 {
		return fmt.Errorf("%w: todo.claim.version must be >= 1", ErrValidation)
	}
	if c.ClaimedAt.IsZero() || c.ExpiresAt.IsZero() || !c.ExpiresAt.After(c.ClaimedAt) {
		return fmt.Errorf("%w: todo.claim expiry must be after claimed_at", ErrValidation)
	}
	return nil
}

// Runtime capability names are owned by the governance contract. Keeping the
// values here avoids a domain dependency on internal/runtime.
const (
	RuntimeCapabilityStructuredTransport     = "structured_transport"
	RuntimeCapabilitySchemaConstrainedOutput = "schema_constrained_output"
	RuntimeCapabilityControlToolCall         = "control_tool_call"

	// Short aliases keep call sites readable while retaining one wire vocabulary.
	CapabilityStructuredTransport     = RuntimeCapabilityStructuredTransport
	CapabilitySchemaConstrainedOutput = RuntimeCapabilitySchemaConstrainedOutput
	CapabilityControlToolCall         = RuntimeCapabilityControlToolCall
)

func ValidRuntimeCapability(capability string) bool {
	switch capability {
	case RuntimeCapabilityStructuredTransport, RuntimeCapabilitySchemaConstrainedOutput, RuntimeCapabilityControlToolCall:
		return true
	default:
		return false
	}
}

// DecisionScope bounds the WorkItems, Agents, runtime capabilities and write
// paths available to one Todo. runtime_capabilities uses contract strings, not
// runtime package types, to keep this aggregate dependency-free.
type DecisionScope struct {
	WorkItemIDs         []string `json:"work_item_ids"`
	AgentIDs            []string `json:"agent_ids"`
	RuntimeCapabilities []string `json:"runtime_capabilities"`
	WriteScopes         []string `json:"write_scopes"`
	MaxDispatch         int      `json:"max_dispatch"`
}

func (s *DecisionScope) Validate() error {
	if s == nil {
		return fmt.Errorf("%w: nil decision scope", ErrValidation)
	}
	if err := validateTypedIDList("decision_scope.work_item_ids", s.WorkItemIDs, 1, 128, PrefixWorkItem); err != nil {
		return err
	}
	if err := validateTypedIDList("decision_scope.agent_ids", s.AgentIDs, 1, 128, PrefixAgent); err != nil {
		return err
	}
	if len(s.RuntimeCapabilities) > 3 {
		return fmt.Errorf("%w: decision_scope.runtime_capabilities exceeds 3 items", ErrValidation)
	}
	if duplicate := firstDuplicate(s.RuntimeCapabilities); duplicate != "" {
		return fmt.Errorf("%w: duplicate runtime capability %q", ErrValidation, duplicate)
	}
	for i, capability := range s.RuntimeCapabilities {
		if !ValidRuntimeCapability(capability) {
			return fmt.Errorf("%w: decision_scope.runtime_capabilities[%d] %q", ErrValidation, i, capability)
		}
	}
	if len(s.WriteScopes) > 128 {
		return fmt.Errorf("%w: decision_scope.write_scopes exceeds 128 items", ErrValidation)
	}
	if duplicate := firstDuplicate(s.WriteScopes); duplicate != "" {
		return fmt.Errorf("%w: duplicate write scope %q", ErrValidation, duplicate)
	}
	for i, writeScope := range s.WriteScopes {
		if err := validateWriteScope(writeScope); err != nil {
			return fmt.Errorf("%w: decision_scope.write_scopes[%d]: %v", ErrValidation, i, err)
		}
	}
	if s.MaxDispatch < 0 || s.MaxDispatch > 64 {
		return fmt.Errorf("%w: decision_scope.max_dispatch must be in 0..64", ErrValidation)
	}
	return nil
}

// Todo is a bounded, claimable governance action. It owns intent and scope;
// creating or advancing a Runner lease is outside this aggregate.
type Todo struct {
	ID              string        `json:"id"`
	GoalID          string        `json:"goal_id"`
	Class           TodoClass     `json:"class"`
	Status          TodoStatus    `json:"status"`
	Instruction     string        `json:"instruction"`
	Acceptance      []string      `json:"acceptance"`
	ResumeCondition string        `json:"resume_condition,omitempty"`
	Priority        Priority      `json:"priority"`
	Predecessors    []string      `json:"predecessors"`
	Successors      []string      `json:"successors"`
	DecisionScope   DecisionScope `json:"decision_scope"`
	Claim           *TodoClaim    `json:"claim,omitempty"`
	// ClaimVersion is the durable monotonic claim generation. It survives
	// ReleaseClaim so a later claim cannot reuse an old generation (ABA).
	ClaimVersion int   `json:"claim_version"`
	LastTurnSeq  int64 `json:"last_turn_seq"`
	// CompletionTurnKey and CompletionEvidenceID make Todo completion an
	// auditable projection of one admitted governance turn and its accepted
	// evidence. They are immutable once set and are populated only by the
	// acceptance write point.
	CompletionTurnKey    *TurnKey  `json:"completion_turn_key,omitempty"`
	CompletionEvidenceID string    `json:"completion_evidence_id,omitempty"`
	Version              int       `json:"version"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func (t *Todo) Validate() error {
	if t == nil {
		return fmt.Errorf("%w: nil todo", ErrValidation)
	}
	if err := validateTypedID("todo.id", t.ID, PrefixTodo); err != nil {
		return err
	}
	if err := validateTypedID("todo.goal_id", t.GoalID, PrefixGoal); err != nil {
		return err
	}
	if !t.Class.Valid() {
		return fmt.Errorf("%w: todo.class %q", ErrValidation, t.Class)
	}
	if !t.Status.Valid() {
		return fmt.Errorf("%w: todo.status %q", ErrValidation, t.Status)
	}
	if err := validateText("todo.instruction", t.Instruction, 20000); err != nil {
		return err
	}
	if len(t.Acceptance) == 0 || len(t.Acceptance) > 64 {
		return fmt.Errorf("%w: todo.acceptance must contain 1..64 items", ErrValidation)
	}
	for i, item := range t.Acceptance {
		if err := validateText(fmt.Sprintf("todo.acceptance[%d]", i), item, 2000); err != nil {
			return err
		}
	}
	if t.ResumeCondition != "" {
		if err := validateText("todo.resume_condition", t.ResumeCondition, 4000); err != nil {
			return err
		}
	}
	if !t.Priority.Valid() {
		return fmt.Errorf("%w: todo.priority %q", ErrValidation, t.Priority)
	}
	if err := validateTypedIDList("todo.predecessors", t.Predecessors, 0, 128, PrefixTodo); err != nil {
		return err
	}
	if err := validateTypedIDList("todo.successors", t.Successors, 0, 128, PrefixTodo); err != nil {
		return err
	}
	if err := t.DecisionScope.Validate(); err != nil {
		return err
	}
	if t.Claim != nil {
		if err := t.Claim.Validate(); err != nil {
			return err
		}
		if t.ClaimVersion < 1 || t.Claim.Version != t.ClaimVersion {
			return fmt.Errorf("%w: todo.claim.version must equal claim_version", ErrValidation)
		}
		if !containsString(t.DecisionScope.AgentIDs, t.Claim.OwnerAgentID) {
			return fmt.Errorf("%w: todo.claim.owner_agent_id is outside decision scope", ErrValidation)
		}
	}
	if (t.Status == TodoClaimed || t.Status == TodoRunning) && t.Claim == nil {
		return fmt.Errorf("%w: todo.%s requires a governance claim", ErrValidation, t.Status)
	}
	if t.LastTurnSeq < 0 {
		return fmt.Errorf("%w: todo.last_turn_seq must be >= 0", ErrValidation)
	}
	if t.CompletionTurnKey != nil {
		if err := t.CompletionTurnKey.Validate(); err != nil {
			return err
		}
		if t.CompletionTurnKey.GoalID != t.GoalID || t.CompletionTurnKey.TodoID != t.ID ||
			t.CompletionTurnKey.TurnSeq != t.LastTurnSeq {
			return fmt.Errorf("%w: todo completion turn key does not match Todo watermark", ErrValidation)
		}
	}
	if t.CompletionEvidenceID != "" {
		if err := validateText("todo.completion_evidence_id", t.CompletionEvidenceID, 256); err != nil {
			return err
		}
	}
	if t.Status != TodoCompleted && (t.CompletionTurnKey != nil || t.CompletionEvidenceID != "") {
		return fmt.Errorf("%w: non-completed Todo must not retain completion identity", ErrValidation)
	}
	if t.Status == TodoCompleted && (t.CompletionTurnKey == nil || t.CompletionEvidenceID == "") {
		return fmt.Errorf("%w: completed Todo requires completion turn key and evidence", ErrValidation)
	}
	if t.ClaimVersion < 0 {
		return fmt.Errorf("%w: todo.claim_version must be >= 0", ErrValidation)
	}
	if t.Version < 1 {
		return fmt.Errorf("%w: todo.version must be >= 1", ErrValidation)
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: todo timestamps are required", ErrValidation)
	}
	return nil
}

func (t *Todo) Transition(to TodoStatus, now time.Time) error {
	if t == nil {
		return fmt.Errorf("%w: nil todo", ErrValidation)
	}
	if t.Status.IsTerminal() || !t.Status.CanTransitionTo(to) {
		return &TransitionError{Entity: "todo", From: string(t.Status), To: string(to)}
	}
	if to == TodoRunning {
		if t.Claim == nil {
			return fmt.Errorf("%w: todo.running requires an active governance claim", ErrValidation)
		}
		if err := t.Claim.Validate(); err != nil {
			return err
		}
		if t.ClaimVersion < 1 || t.Claim.Version != t.ClaimVersion {
			return fmt.Errorf("%w: todo.running requires claim_version to match claim", ErrValidation)
		}
	}
	t.Status = to
	t.Version++
	t.UpdatedAt = now
	return nil
}

// CompleteWithEvidence closes the current Todo at the human acceptance gate.
// The completion identity must point at an already admitted turn; this method
// never allocates a turn or accepts a synthetic evidence reference.
func (t *Todo) CompleteWithEvidence(key TurnKey, evidenceID string, now time.Time) error {
	if t == nil {
		return fmt.Errorf("%w: nil todo", ErrValidation)
	}
	if err := key.Validate(); err != nil {
		return err
	}
	if key.GoalID != t.GoalID || key.TodoID != t.ID || key.TurnSeq < 1 || key.TurnSeq != t.LastTurnSeq {
		return fmt.Errorf("%w: Todo completion turn key does not match Todo", ErrStateConflict)
	}
	if err := validateText("todo.completion_evidence_id", evidenceID, 256); err != nil {
		return err
	}
	if t.Status == TodoCompleted {
		if t.CompletionTurnKey != nil && t.CompletionTurnKey.Equal(key) && t.CompletionEvidenceID == evidenceID {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if t.Status != TodoWaiting && t.Status != TodoRunning {
		return &TransitionError{Entity: "todo", From: string(t.Status), To: string(TodoCompleted)}
	}
	if t.ClaimVersion == int(^uint(0)>>1) {
		return fmt.Errorf("%w: todo.claim_version overflow", ErrValidation)
	}
	t.Status = TodoCompleted
	t.Claim = nil
	// Clearing the claim on completion is a fencing mutation just like an
	// explicit release; keep the generation monotonic even when a legacy row
	// had no active claim.
	t.ClaimVersion++
	t.CompletionTurnKey = &key
	t.CompletionEvidenceID = evidenceID
	t.Version++
	t.UpdatedAt = now
	return nil
}

// ClaimBy creates or renews governance ownership. It never creates or
// modifies a Runner lease. Persistence should wrap this operation in its own
// Todo version CAS when multiple coordinators race.
func (t *Todo) ClaimBy(ownerAgentID string, claimedAt, expiresAt time.Time) error {
	if t == nil {
		return fmt.Errorf("%w: nil todo", ErrValidation)
	}
	claim := TodoClaim{OwnerAgentID: ownerAgentID, ClaimedAt: claimedAt, ExpiresAt: expiresAt}
	claim.Version = t.ClaimVersion + 1
	if claim.Version < 1 { // defensive overflow guard for corrupted in-memory state
		return fmt.Errorf("%w: todo.claim_version overflow", ErrValidation)
	}
	if err := claim.Validate(); err != nil {
		return err
	}
	if !containsString(t.DecisionScope.AgentIDs, ownerAgentID) {
		return fmt.Errorf("%w: claim owner is outside decision scope", ErrValidation)
	}
	if t.Status.IsTerminal() {
		return &TransitionError{Entity: "todo", From: string(t.Status), To: string(TodoClaimed)}
	}
	if t.Claim != nil {
		if t.Claim.OwnerAgentID != ownerAgentID && t.Claim.ExpiresAt.After(claimedAt) {
			return ErrStateConflict
		}
	}
	t.ClaimVersion = claim.Version
	t.Claim = &claim
	if t.Status == TodoPending || t.Status == TodoWaiting || t.Status == TodoBlocked {
		t.Status = TodoClaimed
	}
	t.Version++
	t.UpdatedAt = claimedAt
	return nil
}

// ReleaseClaim clears governance ownership; it does not release a Runner
// lease. A claimed Todo returns to pending, while a running Todo is rejected
// because execution ownership must be settled by the control plane first.
// Clearing an active claim is itself a lifecycle mutation, so ClaimVersion is
// bumped and retained to fence stale owners before the next claim.
func (t *Todo) ReleaseClaim(now time.Time) error {
	if t == nil {
		return fmt.Errorf("%w: nil todo", ErrValidation)
	}
	if t.Claim == nil {
		return nil
	}
	if t.Status == TodoRunning {
		return ErrStateConflict
	}
	if t.ClaimVersion < t.Claim.Version {
		t.ClaimVersion = t.Claim.Version
	}
	if t.ClaimVersion == int(^uint(0)>>1) {
		return fmt.Errorf("%w: todo.claim_version overflow", ErrValidation)
	}
	t.ClaimVersion++
	t.Claim = nil
	if t.Status == TodoClaimed {
		t.Status = TodoPending
	}
	t.Version++
	t.UpdatedAt = now
	return nil
}

// RenewClaim keeps the same governance owner and claim generation alive while
// a delegated Coordinator Run is still producing its bounded decision. It does
// not perform an ABA transition; a different owner or generation is rejected.
// Renewal is not a new claim: the original ClaimedAt remains immutable and only
// ExpiresAt plus the Todo bookkeeping timestamp move forward.
func (t *Todo) RenewClaim(ownerAgentID string, claimVersion int, renewedAt, expiresAt time.Time) error {
	if t == nil {
		return fmt.Errorf("%w: nil todo", ErrValidation)
	}
	if ownerAgentID == "" || claimVersion < 1 || !expiresAt.After(renewedAt) {
		return fmt.Errorf("%w: invalid Todo claim renewal", ErrValidation)
	}
	if t.Status.IsTerminal() || t.Claim == nil || t.Claim.OwnerAgentID != ownerAgentID ||
		t.ClaimVersion != claimVersion || t.Claim.Version != claimVersion {
		return ErrStateConflict
	}
	if !expiresAt.After(t.Claim.ExpiresAt) {
		return fmt.Errorf("%w: Todo claim renewal must extend expiry", ErrValidation)
	}
	t.Claim.ExpiresAt = expiresAt
	t.Version++
	t.UpdatedAt = renewedAt
	return nil
}

// GovernanceErrorCode is the closed set of validation/admission error codes
// shared by TurnDecision and its validation details.
type GovernanceErrorCode string

const (
	GovernanceErrorPlanJSONSyntax         GovernanceErrorCode = "plan_json_syntax"
	GovernanceErrorPlanSchemaValidation   GovernanceErrorCode = "plan_schema_validation"
	GovernanceErrorPlanSemanticValidation GovernanceErrorCode = "plan_semantic_validation"
	GovernanceErrorPlanAuthorityDenied    GovernanceErrorCode = "plan_authority_denied"
	GovernanceErrorPlanQuotaDenied        GovernanceErrorCode = "plan_quota_denied"
	GovernanceErrorCostPriceUnavailable   GovernanceErrorCode = "cost_price_unavailable"
	GovernanceErrorUsageUnresolved        GovernanceErrorCode = "usage_unresolved"
)

func (c GovernanceErrorCode) Valid() bool {
	switch c {
	case GovernanceErrorPlanJSONSyntax, GovernanceErrorPlanSchemaValidation,
		GovernanceErrorPlanSemanticValidation, GovernanceErrorPlanAuthorityDenied,
		GovernanceErrorPlanQuotaDenied, GovernanceErrorCostPriceUnavailable,
		GovernanceErrorUsageUnresolved:
		return true
	default:
		return false
	}
}

type GovernanceValidationError struct {
	Code    GovernanceErrorCode `json:"code"`
	Message string              `json:"message"`
	Path    string              `json:"path"`
}

func (e GovernanceValidationError) Validate() error {
	if !e.Code.Valid() {
		return fmt.Errorf("%w: governance validation error code %q", ErrValidation, e.Code)
	}
	if err := validateText("governance validation error.message", e.Message, 4000); err != nil {
		return err
	}
	if err := validateText("governance validation error.path", e.Path, 1024); err != nil {
		return err
	}
	return nil
}

// TurnDecisionKind is the closed set of control-plane outcomes for a bounded
// governance turn.
type TurnDecisionKind string

const (
	TurnDecisionExecute    TurnDecisionKind = "execute"
	TurnDecisionRepair     TurnDecisionKind = "repair"
	TurnDecisionReplan     TurnDecisionKind = "replan"
	TurnDecisionWait       TurnDecisionKind = "wait"
	TurnDecisionUserAction TurnDecisionKind = "user_action"
	TurnDecisionFinish     TurnDecisionKind = "finish"

	DecisionExecute    = TurnDecisionExecute
	DecisionRepair     = TurnDecisionRepair
	DecisionReplan     = TurnDecisionReplan
	DecisionWait       = TurnDecisionWait
	DecisionUserAction = TurnDecisionUserAction
	DecisionFinish     = TurnDecisionFinish
)

func (d TurnDecisionKind) Valid() bool {
	switch d {
	case TurnDecisionExecute, TurnDecisionRepair, TurnDecisionReplan,
		TurnDecisionWait, TurnDecisionUserAction, TurnDecisionFinish:
		return true
	default:
		return false
	}
}

// TurnDecision is the typed governance outcome. PlanDecisionV2 is the closed
// wire DTO installed by WP2; map[string]any is never the protocol truth.
type TurnDecision struct {
	TurnKey          TurnKey                     `json:"turn_key"`
	Decision         TurnDecisionKind            `json:"decision"`
	Reason           string                      `json:"reason"`
	NextAction       string                      `json:"next_action"`
	SchemaVersion    string                      `json:"schema_version"`
	PlanDecision     *PlanDecisionV2             `json:"plan_decision,omitempty"`
	ValidationErrors []GovernanceValidationError `json:"validation_errors"`
	RecordedAt       time.Time                   `json:"recorded_at"`
}

func (d *TurnDecision) Validate() error {
	if d == nil {
		return fmt.Errorf("%w: nil turn decision", ErrValidation)
	}
	if err := d.TurnKey.Validate(); err != nil {
		return err
	}
	if !d.Decision.Valid() {
		return fmt.Errorf("%w: turn decision %q", ErrValidation, d.Decision)
	}
	if err := validateText("turn_decision.reason", d.Reason, 4000); err != nil {
		return err
	}
	if err := validateText("turn_decision.next_action", d.NextAction, 2000); err != nil {
		return err
	}
	if err := validateText("turn_decision.schema_version", d.SchemaVersion, 128); err != nil {
		return err
	}
	if len(d.ValidationErrors) > 128 {
		return fmt.Errorf("%w: turn_decision.validation_errors exceeds 128 items", ErrValidation)
	}
	for i := range d.ValidationErrors {
		if err := d.ValidationErrors[i].Validate(); err != nil {
			return fmt.Errorf("%w: turn_decision.validation_errors[%d]: %v", ErrValidation, i, err)
		}
	}
	if d.RecordedAt.IsZero() {
		return fmt.Errorf("%w: turn_decision.recorded_at is required", ErrValidation)
	}
	return nil
}

// QuotaKind is the closed set of governance quota units. Reservation and
// spend records are persistence concerns and intentionally not part of this
// first domain policy type.
type QuotaKind string

const (
	QuotaTurnCount           QuotaKind = "turn_count"
	QuotaActiveWorker        QuotaKind = "active_worker"
	QuotaInputTokensTotal    QuotaKind = "input_tokens_total"
	QuotaInputUncachedTokens QuotaKind = "input_uncached_tokens"
	QuotaCacheReadTokens     QuotaKind = "cache_read_tokens"
	QuotaCacheWriteTokens    QuotaKind = "cache_write_tokens"
	QuotaOutputTokens        QuotaKind = "output_tokens"
	QuotaCostMicroUSD        QuotaKind = "cost_microusd"
)

func (k QuotaKind) Valid() bool {
	switch k {
	case QuotaTurnCount, QuotaActiveWorker, QuotaInputTokensTotal, QuotaInputUncachedTokens,
		QuotaCacheReadTokens, QuotaCacheWriteTokens, QuotaOutputTokens, QuotaCostMicroUSD:
		return true
	default:
		return false
	}
}

type QuotaEnforcement string

const (
	QuotaEnforcementAudit   QuotaEnforcement = "audit"
	QuotaEnforcementEnforce QuotaEnforcement = "enforce"
)

func (e QuotaEnforcement) Valid() bool {
	return e == QuotaEnforcementAudit || e == QuotaEnforcementEnforce
}

type QuotaPolicy struct {
	Kind        QuotaKind        `json:"kind"`
	Limit       int64            `json:"limit"`
	Enforcement QuotaEnforcement `json:"enforcement"`
}

func (p QuotaPolicy) Validate() error {
	if !p.Kind.Valid() {
		return fmt.Errorf("%w: quota policy kind %q", ErrValidation, p.Kind)
	}
	if p.Limit < 0 {
		return fmt.Errorf("%w: quota policy limit must be >= 0", ErrValidation)
	}
	if !p.Enforcement.Valid() {
		return fmt.Errorf("%w: quota policy enforcement %q", ErrValidation, p.Enforcement)
	}
	return nil
}

// GovernanceEvidenceSourceKind identifies an authoritative entity or
// validation result referenced by the projection. There is no standalone
// GovernanceEvidence aggregate.
type GovernanceEvidenceSourceKind string

const (
	EvidenceSourceWorkItem         GovernanceEvidenceSourceKind = "work_item"
	EvidenceSourcePlan             GovernanceEvidenceSourceKind = "plan"
	EvidenceSourceRun              GovernanceEvidenceSourceKind = "run"
	EvidenceSourceArtifact         GovernanceEvidenceSourceKind = "artifact"
	EvidenceSourceApproval         GovernanceEvidenceSourceKind = "approval"
	EvidenceSourceValidationResult GovernanceEvidenceSourceKind = "validation_result"
	EvidenceSourceDeliveryBrief    GovernanceEvidenceSourceKind = "delivery_brief"
)

func (k GovernanceEvidenceSourceKind) Valid() bool {
	switch k {
	case EvidenceSourceWorkItem, EvidenceSourcePlan, EvidenceSourceRun,
		EvidenceSourceArtifact, EvidenceSourceApproval,
		EvidenceSourceValidationResult, EvidenceSourceDeliveryBrief:
		return true
	default:
		return false
	}
}

type GovernanceEvidenceVerification string

const (
	EvidenceVerificationObserved GovernanceEvidenceVerification = "observed"
	EvidenceVerificationPassed   GovernanceEvidenceVerification = "passed"
	EvidenceVerificationFailed   GovernanceEvidenceVerification = "failed"
	EvidenceVerificationAccepted GovernanceEvidenceVerification = "accepted"
)

func (v GovernanceEvidenceVerification) Valid() bool {
	return v == EvidenceVerificationObserved || v == EvidenceVerificationPassed ||
		v == EvidenceVerificationFailed || v == EvidenceVerificationAccepted
}

type GovernanceEvidenceItem struct {
	SourceKind   GovernanceEvidenceSourceKind   `json:"source_kind"`
	SourceID     string                         `json:"source_id"`
	Verification GovernanceEvidenceVerification `json:"verification"`
	Summary      string                         `json:"summary"`
	RecordedAt   time.Time                      `json:"recorded_at"`
}

func (e GovernanceEvidenceItem) Validate() error {
	if !e.SourceKind.Valid() {
		return fmt.Errorf("%w: evidence source_kind %q", ErrValidation, e.SourceKind)
	}
	if err := validateEvidenceSourceID(e.SourceKind, e.SourceID); err != nil {
		return err
	}
	if !e.Verification.Valid() {
		return fmt.Errorf("%w: evidence verification %q", ErrValidation, e.Verification)
	}
	if err := validateText("evidence.summary", e.Summary, 4000); err != nil {
		return err
	}
	if e.RecordedAt.IsZero() {
		return fmt.Errorf("%w: evidence.recorded_at is required", ErrValidation)
	}
	return nil
}

func (p Priority) Valid() bool {
	return p == PriorityLow || p == PriorityMedium || p == PriorityHigh || p == PriorityUrgent
}

func validateTypedID(field, id, prefix string) error {
	if id == "" || strings.TrimSpace(id) != id || !strings.HasPrefix(id, prefix) || len(id) == len(prefix) {
		return fmt.Errorf("%w: %s must be a non-empty %s id", ErrValidation, field, prefix)
	}
	return nil
}

func validateOptionalTypedID(field, id, prefix string) error {
	if id == "" {
		return nil
	}
	return validateTypedID(field, id, prefix)
}

func validateTypedIDList(field string, ids []string, min, max int, prefix string) error {
	if len(ids) < min || len(ids) > max {
		return fmt.Errorf("%w: %s must contain %d..%d items", ErrValidation, field, min, max)
	}
	if duplicate := firstDuplicate(ids); duplicate != "" {
		return fmt.Errorf("%w: duplicate %s item %q", ErrValidation, field, duplicate)
	}
	for i, id := range ids {
		if err := validateTypedID(fmt.Sprintf("%s[%d]", field, i), id, prefix); err != nil {
			return err
		}
	}
	return nil
}

func firstDuplicate(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return value
		}
		seen[value] = struct{}{}
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func validateText(field, value string, maxRunes int) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s must be non-empty UTF-8 text", ErrValidation, field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%w: %s exceeds %d characters", ErrValidation, field, maxRunes)
	}
	return nil
}

func validateWriteScope(value string) error {
	if err := validateText("write scope", value, 1024); err != nil {
		return err
	}
	if strings.ContainsRune(value, 0) || strings.ContainsRune(value, '\\') ||
		strings.HasPrefix(value, "/") || isWindowsDrivePath(value) {
		return fmt.Errorf("%w: write scope must be relative", ErrValidation)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%w: write scope escapes its context", ErrValidation)
	}
	return nil
}

func isWindowsDrivePath(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	c := value[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func validateEvidenceSourceID(kind GovernanceEvidenceSourceKind, id string) error {
	switch kind {
	case EvidenceSourceWorkItem:
		return validateTypedID("evidence.source_id", id, PrefixWorkItem)
	case EvidenceSourcePlan:
		return validateTypedID("evidence.source_id", id, PrefixPlan)
	case EvidenceSourceRun:
		return validateTypedID("evidence.source_id", id, PrefixRun)
	case EvidenceSourceArtifact:
		return validateTypedID("evidence.source_id", id, PrefixArtifact)
	case EvidenceSourceApproval:
		return validateTypedID("evidence.source_id", id, PrefixApproval)
	case EvidenceSourceValidationResult:
		return validateTypedID("evidence.source_id", id, PrefixValidationResult)
	case EvidenceSourceDeliveryBrief:
		return validateTypedID("evidence.source_id", id, PrefixDeliveryBriefSnapshot)
	default:
		if id == "" || strings.TrimSpace(id) != id {
			return fmt.Errorf("%w: evidence.source_id must be non-empty", ErrValidation)
		}
		return nil
	}
}
