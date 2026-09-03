package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gowebpki/jcs"
	workbenchcontracts "github.com/ybs/agent-team-workbench/contracts"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// TodoToPlanCompileInput is the trusted input to the pure Todo -> Plan
// boundary. Goal, Todo, receipt, Run and context snapshots are deliberately
// identified rather than supplied as caller-owned snapshots; Compile reloads
// all of them inside one transaction before producing an execution-neutral
// Plan representation.
type TodoToPlanCompileInput struct {
	TurnKey      domain.TurnKey
	OwnerAgentID string
	SourceRunID  string
	Decision     *domain.PlanDecisionV2
	SchemaDigest string
}

// CompiledTodoPlan is a provider-neutral compiler result. It contains the
// exact inputs that the existing SubmitPlan boundary will later consume, but
// Compile itself never persists this value or creates a Run.
type CompiledTodoPlan struct {
	TurnKey        domain.TurnKey
	WorkItemID     string
	AgentProfileID string
	SourceRunID    string
	PlanClientKey  string
	Steps          []PlanStepInput
	Guardrails     domain.PlanGuardrails
	Audit          *PlanDecisionAuditInput
	SchemaDigest   string
	DecisionDigest string
}

// CompileTodoPlan performs the fresh authority read and deterministic
// PlanDecision compilation. The transaction is intentionally read-only: a
// later orchestration slice owns receipt phases, Plan persistence and
// post-commit dispatch.
func (s *Service) CompileTodoPlan(ctx context.Context, p TodoToPlanCompileInput) (*CompiledTodoPlan, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("%w: service store required", domain.ErrValidation)
	}
	if err := p.TurnKey.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.OwnerAgentID) == "" {
		return nil, fmt.Errorf("%w: Todo compile owner agent required", domain.ErrValidation)
	}
	if strings.TrimSpace(p.SourceRunID) == "" {
		return nil, fmt.Errorf("%w: Todo compile source Run required", domain.ErrValidation)
	}
	canonicalSchemaDigest := workbenchcontracts.PlanDecisionV2SchemaDigest()
	if p.SchemaDigest != canonicalSchemaDigest {
		return nil, fmt.Errorf("%w: PlanDecisionV2 schema digest mismatch: got %q", domain.ErrValidation, p.SchemaDigest)
	}

	var compiled *CompiledTodoPlan
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, p.TurnKey.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, p.TurnKey.TodoID)
		if err != nil {
			return err
		}
		header, err := s.store.TurnReceipts().GetHeader(txctx, p.TurnKey)
		if err != nil {
			return err
		}
		sourceRun, err := s.store.Runs().Get(txctx, p.SourceRunID)
		if err != nil {
			return err
		}
		root, err := s.store.WorkItems().Get(txctx, goal.RootWorkItemID)
		if err != nil {
			return err
		}
		coordinatorState, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(txctx, root.ID)
		if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
			return stateErr
		}
		if errors.Is(stateErr, domain.ErrNotFound) {
			coordinatorState = nil
		}

		if err := validateTodoPlanGovernanceState(goal, todo, root, p); err != nil {
			return err
		}
		decision, steps, decisionDigest, err := normalizeTodoPlanDecision(p.Decision)
		if err != nil {
			return err
		}
		if header.GovernedSourceRunID != "" && header.GovernedSourceRunID != sourceRun.ID {
			return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
				"TurnReceipt recovery checkpoint source Run does not match compiler source Run")
		}
		if header.PlanClientKey != "" && header.PlanClientKey != fmt.Sprintf("governance:%s:%s:%d",
			p.TurnKey.GoalID, p.TurnKey.TodoID, p.TurnKey.TurnSeq) {
			return todoPlanAuthority(domain.ErrIdempotencyConflict,
				"TurnReceipt recovery checkpoint Plan client key does not match TurnKey")
		}
		if header.DecisionDigest != "" && header.DecisionDigest != decisionDigest {
			return todoPlanAuthority(domain.ErrIdempotencyConflict,
				"TurnReceipt recovery checkpoint decision digest does not match decision")
		}
		if err := validateTodoPlanReceipt(header, p.TurnKey, decision.SchemaVersion); err != nil {
			return err
		}
		inputDigest, err := ComputeTodoPlanInputSnapshotDigest(goal, todo, sourceRun)
		if err != nil {
			return err
		}
		if header.InputSnapshotDigest != inputDigest {
			return todoPlanAuthority(domain.ErrStateConflict,
				"TurnReceipt input snapshot digest does not match fresh Goal/Todo/Run authority")
		}
		owner, err := s.store.Agents().Get(txctx, p.OwnerAgentID)
		if err != nil {
			return err
		}
		if owner == nil {
			return todoPlanAuthority(domain.ErrNotFound, "claim owner agent could not be resolved")
		}
		if owner.WorkspaceID != goal.WorkspaceID {
			return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
				"claim owner agent belongs to another workspace")
		}
		if !owner.Kind.IsSystem() && owner.Availability != domain.AgentEnabled {
			return todoPlanAuthority(domain.ErrStateConflict,
				"ordinary Todo claim owner is disabled")
		}
		if todo.Claim == nil || todo.Claim.OwnerAgentID != owner.ID {
			return todoPlanState(domain.ErrStateConflict,
				"Todo claim owner does not match the compile owner")
		}
		if !scopeContains(todo.DecisionScope.AgentIDs, owner.ID) {
			return todoPlanAuthority(domain.ErrStateConflict,
				"Todo claim owner is outside DecisionScope")
		}
		now := time.Now().UTC()
		if !todo.Claim.ExpiresAt.After(now) {
			return todoPlanState(domain.ErrStateConflict, "Todo governance claim has expired")
		}
		if todo.Claim.Version != todo.ClaimVersion || todo.ClaimVersion < 1 {
			return todoPlanState(domain.ErrStateConflict, "Todo claim version is stale")
		}

		if err := s.validateTodoPlanSourceRun(txctx, sourceRun, root, owner, coordinatorState); err != nil {
			return err
		}
		if err := s.validateTodoPlanSourceContext(txctx, sourceRun, goal.WorkspaceID); err != nil {
			return err
		}

		if err := validateTodoPlanRuntimeCapabilities(sourceRun, todo.DecisionScope.RuntimeCapabilities,
			coordinatorState != nil || owner.Kind.IsSystem()); err != nil {
			return err
		}
		if err := s.validateTodoPlanDispatchScope(txctx, goal.WorkspaceID, todo.DecisionScope, decision); err != nil {
			return err
		}
		if err := s.validateTodoPlanJoinScope(txctx, root, todo.DecisionScope, decision); err != nil {
			return err
		}

		maxDispatch := todo.DecisionScope.MaxDispatch
		compiled = &CompiledTodoPlan{
			TurnKey:        p.TurnKey,
			WorkItemID:     root.ID,
			AgentProfileID: owner.ID,
			SourceRunID:    sourceRun.ID,
			PlanClientKey:  fmt.Sprintf("governance:%s:%s:%d", p.TurnKey.GoalID, p.TurnKey.TodoID, p.TurnKey.TurnSeq),
			Steps:          steps,
			Guardrails:     domain.PlanGuardrails{MaxDispatch: &maxDispatch},
			Audit: &PlanDecisionAuditInput{
				SchemaVersion: decision.SchemaVersion,
				Candidate:     PlanCandidateNativeText,
				Reason:        decision.Reason,
				NextAction:    decision.NextAction,
				StepCount:     len(decision.Steps),
			},
			SchemaDigest:   canonicalSchemaDigest,
			DecisionDigest: decisionDigest,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return compiled, nil
}

func validateTodoPlanGovernanceState(goal *domain.Goal, todo *domain.Todo, root *domain.WorkItem,
	p TodoToPlanCompileInput) error {
	if goal == nil || todo == nil || root == nil {
		return fmt.Errorf("%w: governance compile entities are required", domain.ErrValidation)
	}
	if goal.Status != domain.GoalActive {
		return todoPlanState(domain.ErrStateConflict, "Goal must be active")
	}
	if goal.ID != p.TurnKey.GoalID || todo.ID != p.TurnKey.TodoID || todo.GoalID != goal.ID {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"Goal/Todo/TurnKey identity does not match")
	}
	if goal.CurrentTodoID != todo.ID {
		return todoPlanState(domain.ErrStateConflict, "Todo is not the Goal current Todo")
	}
	if todo.Status != domain.TodoRunning {
		return todoPlanState(domain.ErrStateConflict, "Todo must be running after admission")
	}
	if root.ID != goal.RootWorkItemID || root.ParentID != "" || !isTaskWorkItem(root) {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"Goal root must be the same root Task")
	}
	if root.Status.IsTerminal() {
		return todoPlanState(domain.ErrStateConflict, "terminal root Task cannot compile a Todo Plan")
	}
	if root.WorkspaceID != goal.WorkspaceID {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"Goal root belongs to another workspace")
	}
	if err := todo.DecisionScope.Validate(); err != nil {
		return fmt.Errorf("%w: Todo DecisionScope invalid: %v", domain.ErrValidation, err)
	}
	if !scopeContains(todo.DecisionScope.WorkItemIDs, root.ID) {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"Goal root is outside Todo DecisionScope")
	}
	return nil
}

func validateTodoPlanReceipt(header *domain.TurnReceiptHeader, key domain.TurnKey, decisionSchemaVersion string) error {
	if header == nil {
		return fmt.Errorf("%w: turn receipt header required", domain.ErrValidation)
	}
	if !header.TurnKey.Equal(key) {
		return todoPlanAuthority(domain.ErrStateConflict,
			"TurnReceipt header identity does not match compiler input")
	}
	if err := header.Validate(); err != nil {
		return err
	}
	if header.SchemaVersion != decisionSchemaVersion {
		return todoPlanAuthority(domain.ErrStateConflict,
			"TurnReceipt header schema version does not match PlanDecisionV2")
	}
	want, err := ComputeTurnReceiptHeaderDigest(header)
	if err != nil {
		return err
	}
	if want != header.CanonicalDigest {
		return fmt.Errorf("%w: TurnReceipt header digest mismatch", domain.ErrValidation)
	}
	return nil
}

func (s *Service) validateTodoPlanSourceRun(ctx context.Context, sourceRun *domain.ExecutionRun,
	root *domain.WorkItem, owner *domain.AgentProfile, state *domain.TaskCoordinatorState) error {
	if sourceRun == nil || root == nil || owner == nil {
		return fmt.Errorf("%w: source Run, root and owner are required", domain.ErrValidation)
	}
	if sourceRun.Status != domain.RunSucceeded {
		return todoPlanState(domain.ErrStateConflict, "source Coordinator Run must be succeeded")
	}
	if sourceRun.WorkspaceID != root.WorkspaceID {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"source Run belongs to another workspace")
	}
	if sourceRun.WorkItemID != root.ID {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"source Run must be attached to the Goal root Task")
	}
	if sourceRun.AgentProfileID != owner.ID {
		return todoPlanAuthority(domain.ErrStateConflict,
			"source Run owner does not match the Todo claim owner")
	}
	if state == nil {
		return todoPlanAuthority(domain.ErrStateConflict,
			"Todo Plan compilation currently requires the protected system Coordinator control line")
	}
	if state.WorkspaceID != root.WorkspaceID || state.RootWorkItemID != root.ID {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"Coordinator state does not belong to the Goal root")
	}
	if state.Status != domain.CoordinatorRunning {
		return todoPlanState(domain.ErrStateConflict, "Coordinator state is not running")
	}
	if state.CurrentRunID != sourceRun.ID {
		return todoPlanState(domain.ErrStateConflict,
			"source Run is not the current Coordinator Run")
	}
	if owner.Kind.IsSystem() && state.CoordinatorAgentID == owner.ID && isSystemCoordinatorRun(sourceRun) {
		if err := validateCoordinatorRepairSource(state, sourceRun); err != nil {
			return todoPlanAuthority(err, "source Run Coordinator context is invalid")
		}
		return nil
	}
	if err := s.validateDelegatedTodoPlanSource(ctx, sourceRun, root, owner, state); err != nil {
		return err
	}
	return nil
}

// validateDelegatedTodoPlanSource keeps the ordinary target Agent inside the
// same PlanDecision pipeline as the system Coordinator, but only after the
// accepted Handoff, current Todo claim generation and Coordinator current Run
// all prove the delegation. It is intentionally separate from the generic
// child-worker context checks.
func (s *Service) validateDelegatedTodoPlanSource(ctx context.Context, sourceRun *domain.ExecutionRun,
	root *domain.WorkItem, owner *domain.AgentProfile, state *domain.TaskCoordinatorState) error {
	if owner.Kind.IsSystem() || !isDelegatedCoordinatorRun(sourceRun) {
		return todoPlanAuthority(domain.ErrStateConflict,
			"coordinated Plan source must be the system Coordinator or a proven Handoff target")
	}
	control := coordinatorContextOf(sourceRun)
	if sourceRun.AgentProfileID != owner.ID || sourceRun.WorkItemID != root.ID ||
		state.CurrentRunID != sourceRun.ID || state.Status != domain.CoordinatorRunning ||
		stringValue(control["root_work_item_id"]) != root.ID ||
		stringValue(control["state_id"]) != state.ID ||
		stringValue(control[coordinatorHandoffTargetAgentIDKey]) != owner.ID {
		return todoPlanAuthority(domain.ErrStateConflict,
			"delegated Handoff source Run is not the current Coordinator continuation")
	}
	handoffID := stringValue(control[coordinatorHandoffIDKey])
	// This function runs inside CompileTodoPlan's transaction, so use only the
	// immutable Handoff row and the fresh Todo claim supplied by the caller.
	handoff, err := s.store.Handoffs().Get(ctx, handoffID)
	if err != nil {
		return err
	}
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, root.ID)
	if err != nil {
		return err
	}
	todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		return err
	}
	if handoff == nil || handoff.Status != domain.HandoffTransferred || handoff.GoalID != goal.ID ||
		handoff.TodoID != todo.ID || handoff.TargetClaimVersion < 1 || todo.Claim == nil ||
		todo.Claim.OwnerAgentID != owner.ID || todo.ClaimVersion != handoff.TargetClaimVersion {
		return todoPlanAuthority(domain.ErrStateConflict, "delegated Handoff is not transferred")
	}
	return nil
}

func (s *Service) validateTodoPlanSourceContext(ctx context.Context, sourceRun *domain.ExecutionRun, workspaceID string) error {
	if sourceRun.ContextSnapshotID == "" {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"source Run has no execution context snapshot")
	}
	snapshot, err := s.readTodoPlanSnapshot(ctx, sourceRun)
	if err != nil {
		return err
	}
	if snapshot == nil || snapshot.ID != sourceRun.ContextSnapshotID || snapshot.RunID != sourceRun.ID ||
		snapshot.WorkspaceID != workspaceID {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"source Run context snapshot identity is inconsistent")
	}
	if err := snapshot.Validate(); err != nil {
		return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
			"source Run context snapshot is invalid")
	}
	return nil
}

// readTodoPlanSnapshot is a tiny indirection so source context validation stays
// side-effect free and uses the transaction-bound Store from the receiver.
func (s *Service) readTodoPlanSnapshot(ctx context.Context, sourceRun *domain.ExecutionRun) (*domain.ExecutionContextSnapshot, error) {
	return s.store.ContextSnapshots().GetByRun(ctx, sourceRun.ID)
}

func normalizeTodoPlanDecision(input *domain.PlanDecisionV2) (*domain.PlanDecisionV2, []PlanStepInput, string, error) {
	if input == nil {
		return nil, nil, "", &PlanDecisionError{
			Code: domain.GovernanceErrorPlanSemanticValidation, Path: "/", Message: "decision required",
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: marshal PlanDecisionV2: %v", domain.ErrValidation, err)
	}
	decision, err := DecodePlanDecisionV2(raw)
	if err != nil {
		return nil, nil, "", err
	}
	steps, err := PlanDecisionStepInputs(decision)
	if err != nil {
		return nil, nil, "", err
	}
	canonical, err := jcs.Transform(raw)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: canonicalize PlanDecisionV2: %v", domain.ErrValidation, err)
	}
	digest := sha256.Sum256(canonical)
	return decision, steps, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validateTodoPlanRuntimeCapabilities(sourceRun *domain.ExecutionRun, required []string, systemSource bool) error {
	if len(required) == 0 && !systemSource {
		return nil
	}
	control, ok := todoPlanControlDecision(sourceRun)
	if !ok {
		return todoPlanCapability("source Run has no control_decision capability snapshot")
	}
	schemaDigest, ok := control["schema_digest"].(string)
	if !ok || schemaDigest != workbenchcontracts.PlanDecisionV2SchemaDigest() {
		return todoPlanCapability("source Run control_decision schema digest is not canonical")
	}
	capabilities, ok := todoPlanStringMap(control["capabilities"])
	if !ok {
		return todoPlanCapability("source Run has no capability map")
	}
	for _, name := range required {
		if capabilities[name] != "supported" {
			return todoPlanCapability(fmt.Sprintf("runtime capability %s requires exact supported level, got %q", name, capabilities[name]))
		}
	}
	return nil
}

func todoPlanControlDecision(sourceRun *domain.ExecutionRun) (map[string]any, bool) {
	if sourceRun == nil || sourceRun.Input == nil {
		return nil, false
	}
	value, ok := sourceRun.Input["control_decision"]
	if !ok {
		return nil, false
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out, true
	default:
		return nil, false
	}
}

func todoPlanStringMap(value any) (map[string]string, bool) {
	switch typed := value.(type) {
	case map[string]string:
		return typed, true
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, value := range typed {
			stringValue, ok := value.(string)
			if !ok {
				return nil, false
			}
			out[key] = stringValue
		}
		return out, true
	default:
		return nil, false
	}
}

func (s *Service) validateTodoPlanDispatchScope(ctx context.Context, workspaceID string,
	scope domain.DecisionScope, decision *domain.PlanDecisionV2) error {
	dispatchCount := 0
	for _, step := range decision.Steps {
		if step.Verb != domain.PlanVerbDispatch || step.Dispatch == nil {
			continue
		}
		dispatchCount++
		if !scopeContains(scope.AgentIDs, step.Dispatch.AgentID) {
			return todoPlanAuthority(domain.ErrStateConflict,
				fmt.Sprintf("dispatch agent %s is outside DecisionScope", step.Dispatch.AgentID))
		}
		agent, err := s.store.Agents().Get(ctx, step.Dispatch.AgentID)
		if err != nil {
			return todoPlanAuthority(err,
				fmt.Sprintf("dispatch agent %s could not be resolved", step.Dispatch.AgentID))
		}
		if agent.WorkspaceID != workspaceID {
			return todoPlanAuthority(domain.ErrWorkspaceContextMismatch,
				fmt.Sprintf("dispatch agent %s belongs to another workspace", step.Dispatch.AgentID))
		}
		if agent.Kind.IsSystem() {
			return todoPlanAuthority(domain.ErrStateConflict,
				fmt.Sprintf("system Coordinator %s cannot be a Worker dispatch target", step.Dispatch.AgentID))
		}
		if agent.Availability != domain.AgentEnabled {
			return todoPlanAuthority(domain.ErrStateConflict,
				fmt.Sprintf("dispatch agent %s is disabled", step.Dispatch.AgentID))
		}
	}
	if dispatchCount > scope.MaxDispatch {
		return todoPlanQuota(fmt.Sprintf("dispatch count %d exceeds DecisionScope max_dispatch %d", dispatchCount, scope.MaxDispatch))
	}
	return nil
}

func (s *Service) validateTodoPlanJoinScope(ctx context.Context, root *domain.WorkItem,
	scope domain.DecisionScope, decision *domain.PlanDecisionV2) error {
	var explicit []string
	for _, step := range decision.Steps {
		if step.Verb == domain.PlanVerbJoin && step.Join != nil && !step.Join.Children.All {
			explicit = append(explicit, step.Join.Children.IDs...)
		}
	}
	if len(explicit) == 0 {
		return nil
	}
	children, err := s.store.WorkItems().ListByParent(ctx, root.ID)
	if err != nil {
		return err
	}
	childIDs := make(map[string]struct{}, len(children))
	for _, child := range children {
		if child != nil {
			childIDs[child.ID] = struct{}{}
		}
	}
	for _, childID := range explicit {
		if _, ok := childIDs[childID]; !ok {
			return todoPlanAuthority(domain.ErrStateConflict,
				fmt.Sprintf("join child %s is not a direct child of the root Task", childID))
		}
		// A scoped root grants observation/join authority for its direct Task
		// children. It does not grant arbitrary descendant or cross-root access,
		// and SubmitPlan repeats the direct-child check before execution.
		if !scopeContains(scope.WorkItemIDs, childID) && !scopeContains(scope.WorkItemIDs, root.ID) {
			return todoPlanAuthority(domain.ErrStateConflict,
				fmt.Sprintf("join child %s is outside DecisionScope", childID))
		}
	}
	return nil
}

func scopeContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func todoPlanState(cause error, message string) error {
	if cause == nil {
		cause = domain.ErrStateConflict
	}
	return fmt.Errorf("%w: %s", cause, message)
}

func todoPlanAuthority(cause error, message string) error {
	if cause == nil {
		cause = domain.ErrValidation
	}
	return &PlanDecisionError{
		Code:    domain.GovernanceErrorPlanAuthorityDenied,
		Path:    "/",
		Message: message,
		Cause:   cause,
	}
}

func todoPlanCapability(message string) error {
	return &PlanDecisionError{
		Code:    domain.GovernanceErrorPlanAuthorityDenied,
		Path:    "/runtime_capabilities",
		Message: message,
		Cause:   domain.ErrCapabilityMissing,
	}
}

func todoPlanQuota(message string) error {
	return &PlanDecisionError{
		Code:    domain.GovernanceErrorPlanQuotaDenied,
		Path:    "/steps",
		Message: message,
		Cause:   domain.ErrValidation,
	}
}
