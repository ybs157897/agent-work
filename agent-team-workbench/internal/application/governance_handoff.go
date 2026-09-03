package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type CreateHandoffParams struct {
	GoalID         string
	TodoID         string
	Source         domain.GovernanceActorRef
	Target         domain.GovernanceActorRef
	Reason         string
	ContextSummary string
	Evidence       []domain.GovernanceEvidenceItem
	OpenRisks      []string
	Actor          domain.GovernanceActorRef
	ClientKey      string
}

func (s *Service) CreateHandoff(ctx context.Context, p CreateHandoffParams) (*domain.Handoff, error) {
	if p.Actor.Kind == "" {
		p.Actor = p.Source
	}
	now := time.Now().UTC()
	h := &domain.Handoff{
		ID: domain.NewID(domain.PrefixHandoff), GoalID: p.GoalID, TodoID: p.TodoID,
		Source: p.Source, Target: p.Target, Reason: p.Reason, ContextSummary: p.ContextSummary,
		Evidence:  append([]domain.GovernanceEvidenceItem(nil), p.Evidence...),
		OpenRisks: append([]string(nil), p.OpenRisks...), Status: domain.HandoffPending,
		ClaimTransferState: domain.HandoffClaimRetainedBySource, Actor: p.Actor,
		ClientKey: p.ClientKey, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	var stored *domain.Handoff
	workspaceID := ""
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		goal, err := s.store.Goals().Get(txctx, h.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, h.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID || goal.CurrentTodoID != todo.ID {
			return fmt.Errorf("%w: handoff Goal/Todo is terminal or out of scope", domain.ErrStateConflict)
		}
		// Resolve an exact client-key replay before inspecting the mutable source
		// claim. A successful transfer necessarily changes that claim, so replaying
		// the original create request must still return the original record.
		if h.ClientKey != "" {
			if existing, getErr := s.store.Handoffs().GetByClientKey(txctx, h.GoalID, h.TodoID, h.ClientKey); getErr == nil {
				if !handoffIntentEqual(existing, h) {
					return domain.ErrIdempotencyConflict
				}
				stored = existing
				workspaceID = goal.WorkspaceID
				return nil
			} else if !errors.Is(getErr, domain.ErrNotFound) {
				return getErr
			}
		}
		if goal.Status.IsTerminal() || todo.Status.IsTerminal() {
			return fmt.Errorf("%w: handoff Goal/Todo is terminal or out of scope", domain.ErrStateConflict)
		}
		if todo.Claim == nil {
			return fmt.Errorf("%w: handoff requires an active source claim", domain.ErrStateConflict)
		}
		if !todo.Claim.ExpiresAt.After(time.Now().UTC()) {
			return fmt.Errorf("%w: handoff source claim has expired", domain.ErrStateConflict)
		}
		sourceAgent, err := s.resolveHandoffActorAgent(txctx, goal.WorkspaceID, h.Source)
		if err != nil {
			return err
		}
		targetAgent, err := s.resolveHandoffActorAgent(txctx, goal.WorkspaceID, h.Target)
		if err != nil {
			return err
		}
		if sourceAgent == targetAgent {
			return fmt.Errorf("%w: handoff source and target resolve to the same Agent", domain.ErrStateConflict)
		}
		if todo.Claim.OwnerAgentID != sourceAgent {
			return fmt.Errorf("%w: handoff source does not own current Todo claim", domain.ErrStateConflict)
		}
		if !scopeHasAgent(todo, targetAgent) {
			return fmt.Errorf("%w: handoff target is outside Todo DecisionScope", domain.ErrStateConflict)
		}
		if err := s.validateHandoffActor(txctx, goal.WorkspaceID, h.Actor); err != nil {
			return err
		}
		for _, evidence := range h.Evidence {
			if err := s.validateGovernanceEvidenceReferenceTx(txctx, goal.ID, todo.ID, evidence); err != nil {
				return fmt.Errorf("%w: handoff evidence: %v", domain.ErrValidation, err)
			}
		}
		h.SourceClaimVersion = todo.ClaimVersion
		workspaceID = goal.WorkspaceID
		if err := h.Validate(); err != nil {
			return err
		}
		if err := s.store.Handoffs().Create(txctx, h); err != nil {
			return err
		}
		stored = h
		return s.emitHandoffEvent(txctx, goal.WorkspaceID, todo.ID, h, "", string(h.Status))
	})
	if err != nil {
		return nil, err
	}
	if stored != nil && workspaceID != "" {
		s.notifier.Notify(workspaceID)
	}
	return stored, nil
}

func (s *Service) resolveHandoffActorAgent(ctx context.Context, workspaceID string, actor domain.GovernanceActorRef) (string, error) {
	if err := actor.Validate(); err != nil {
		return "", err
	}
	if actor.Kind == domain.GovernanceActorAgent {
		a, err := s.store.Agents().Get(ctx, actor.ID)
		if err != nil {
			return "", err
		}
		if a.WorkspaceID != workspaceID || a.Availability != domain.AgentEnabled {
			return "", fmt.Errorf("%w: handoff agent is unavailable or outside workspace", domain.ErrStateConflict)
		}
		return a.ID, nil
	}
	// A runtime is only a valid governance owner when exactly one enabled Agent
	// explicitly prefers that runtime. Runtime labels are not guessed as IDs.
	agents, err := s.store.Agents().List(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	var match string
	for _, a := range agents {
		if a == nil || a.Kind.IsSystem() || a.Availability != domain.AgentEnabled || a.RuntimePreference.Preferred != actor.ID {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("%w: runtime %q maps to multiple governance agents", domain.ErrStateConflict, actor.ID)
		}
		match = a.ID
	}
	if match == "" {
		return "", fmt.Errorf("%w: runtime %q has no explicit Agent mapping", domain.ErrStateConflict, actor.ID)
	}
	return match, nil
}

func (s *Service) validateHandoffActor(ctx context.Context, workspaceID string, actor domain.GovernanceActorRef) error {
	_, err := s.resolveHandoffActorAgent(ctx, workspaceID, actor)
	return err
}

func scopeHasAgent(todo *domain.Todo, agentID string) bool {
	if todo == nil {
		return false
	}
	for _, allowed := range todo.DecisionScope.AgentIDs {
		if allowed == agentID {
			return true
		}
	}
	return false
}

// latestTransferredHandoffForTodo returns the newest accepted ownership
// transfer for a current Todo without requiring the claim to be unexpired.
// Resume needs this pre-expiry-independent identity to distinguish an expired
// delegated claim from a claim that another actor already replaced.
func (s *Service) latestTransferredHandoffForTodo(ctx context.Context, goal *domain.Goal,
	todo *domain.Todo) (*domain.Handoff, string, error) {
	if goal == nil || todo == nil || goal.CurrentTodoID != todo.ID || todo.GoalID != goal.ID {
		return nil, "", nil
	}
	handoffs, err := s.store.Handoffs().ListByTodo(ctx, todo.ID)
	if err != nil {
		return nil, "", err
	}
	var latest *domain.Handoff
	var targetAgentID string
	for _, handoff := range handoffs {
		if handoff == nil || handoff.Status != domain.HandoffTransferred || handoff.GoalID != goal.ID {
			continue
		}
		resolved, resolveErr := s.resolveHandoffActorAgent(ctx, goal.WorkspaceID, handoff.Target)
		if resolveErr != nil {
			return nil, "", resolveErr
		}
		if latest == nil || handoff.TargetClaimVersion > latest.TargetClaimVersion ||
			(handoff.TargetClaimVersion == latest.TargetClaimVersion && handoff.UpdatedAt.After(latest.UpdatedAt)) {
			latest, targetAgentID = handoff, resolved
		}
	}
	return latest, targetAgentID, nil
}

// activeTransferredHandoff is the fresh authority check used by the
// Coordinator recovery path. A transferred Handoff remains the ownership
// record for the current Todo until a later handoff replaces its claim
// generation; matching the target claim version prevents an old handoff from
// silently reviving after release/reclaim. Expiry alone does not erase this
// identity: handoffForCoordinatorStart returns a same-owner/same-generation
// expired claim so the delegated path can renew it or surface a hard CAS
// conflict instead of silently switching to the system Coordinator.
func (s *Service) activeTransferredHandoff(ctx context.Context, goal *domain.Goal, todo *domain.Todo) (*domain.Handoff, string, error) {
	if goal == nil || todo == nil || goal.CurrentTodoID != todo.ID || todo.GoalID != goal.ID {
		return nil, "", nil
	}
	handoffs, err := s.store.Handoffs().ListByTodo(ctx, todo.ID)
	if err != nil {
		return nil, "", err
	}
	var active *domain.Handoff
	var targetAgentID string
	for _, handoff := range handoffs {
		if handoff == nil || handoff.Status != domain.HandoffTransferred ||
			handoff.TargetClaimVersion != todo.ClaimVersion || todo.Claim == nil ||
			todo.ClaimVersion < 1 || todo.Claim.OwnerAgentID == "" {
			continue
		}
		resolved, resolveErr := s.resolveHandoffActorAgent(ctx, goal.WorkspaceID, handoff.Target)
		if resolveErr != nil {
			return nil, "", resolveErr
		}
		if resolved != todo.Claim.OwnerAgentID || !scopeHasAgent(todo, resolved) {
			continue
		}
		if active != nil {
			return nil, "", fmt.Errorf("%w: multiple transferred handoffs match Todo claim generation %d",
				domain.ErrStateConflict, todo.ClaimVersion)
		}
		active, targetAgentID = handoff, resolved
	}
	return active, targetAgentID, nil
}

// handoffForCoordinatorStart resolves the accepted handoff checkpoint even if
// its target claim is close to (or just past) expiry. The delegated start path
// renews the same owner/generation before creating a Run; treating the expired
// row as “no handoff” would incorrectly fall back to the system Coordinator.
func (s *Service) handoffForCoordinatorStart(ctx context.Context, state *domain.TaskCoordinatorState) (*domain.Handoff, *domain.Goal, *domain.Todo, string, error) {
	if state == nil {
		return nil, nil, nil, "", nil
	}
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, state.RootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, nil, nil, "", nil
	}
	if err != nil {
		return nil, nil, nil, "", err
	}
	if goal.CurrentTodoID == "" {
		return nil, goal, nil, "", nil
	}
	todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		return nil, nil, nil, "", err
	}
	handoffID := stringValue(state.Data[coordinatorHandoffIDKey])
	if handoffID != "" {
		handoff, getErr := s.store.Handoffs().Get(ctx, handoffID)
		if getErr != nil {
			return nil, nil, nil, "", getErr
		}
		targetAgentID, resolveErr := s.resolveHandoffActorAgent(ctx, goal.WorkspaceID, handoff.Target)
		if resolveErr != nil {
			return nil, nil, nil, "", resolveErr
		}
		if handoff.Status != domain.HandoffTransferred || handoff.GoalID != goal.ID ||
			handoff.TodoID != todo.ID || todo.Claim == nil || todo.Claim.OwnerAgentID != targetAgentID ||
			todo.ClaimVersion != handoff.TargetClaimVersion {
			return nil, nil, nil, "", fmt.Errorf("%w: Handoff start checkpoint is stale", domain.ErrStateConflict)
		}
		return handoff, goal, todo, targetAgentID, nil
	}
	handoff, targetAgentID, err := s.activeTransferredHandoff(ctx, goal, todo)
	return handoff, goal, todo, targetAgentID, err
}

// renewHandoffTodoClaimLocked extends an accepted target claim while its
// delegated Coordinator Run is active. It deliberately permits an already
// expired claim only when owner and generation still match; if another actor
// won the claim CAS, renewal returns a hard conflict instead of reviving it.
func (s *Service) renewHandoffTodoClaimLocked(ctx context.Context, goal *domain.Goal,
	todo *domain.Todo, handoff *domain.Handoff, targetAgentID string) (*domain.Todo, error) {
	if goal == nil || todo == nil || handoff == nil || targetAgentID == "" ||
		handoff.Status != domain.HandoffTransferred || goal.CurrentTodoID != todo.ID ||
		todo.Claim == nil || todo.Claim.OwnerAgentID != targetAgentID ||
		todo.ClaimVersion != handoff.TargetClaimVersion {
		return nil, fmt.Errorf("%w: Handoff claim renewal proof is stale", domain.ErrStateConflict)
	}
	now := time.Now().UTC()
	if todo.Claim.ExpiresAt.After(now.Add(governancePlanClaimTTL / 2)) {
		return todo, nil
	}
	renewed, err := s.renewTodoClaimLocked(ctx, goal.WorkspaceID, todo.ID, targetAgentID,
		handoff.TargetClaimVersion, now, now.Add(governancePlanClaimTTL), todo.Version)
	if err != nil {
		return nil, err
	}
	return renewed, nil
}

func (s *Service) renewDelegatedCoordinatorClaimForRun(ctx context.Context, run *domain.ExecutionRun) error {
	if !isDelegatedCoordinatorRun(run) {
		return nil
	}
	control := coordinatorContextOf(run)
	handoffID := stringValue(control[coordinatorHandoffIDKey])
	if handoffID == "" {
		return fmt.Errorf("%w: delegated Coordinator run lacks handoff identity", domain.ErrStateConflict)
	}
	state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, run.WorkItemID)
	if stateErr != nil && !errors.Is(stateErr, domain.ErrNotFound) {
		return stateErr
	}
	if stateErr == nil && handoffContinuationPending(state) &&
		stringValue(state.Data[coordinatorHandoffSourceRunIDKey]) == run.ID &&
		stringValue(state.Data[coordinatorHandoffIDKey]) != handoffID {
		// A later accepted Handoff has fenced this Run as the relinquishing
		// source. Its late events remain evidence, but it no longer owns (and
		// therefore must not renew) the transferred target claim.
		return nil
	}
	goal, err := s.store.Goals().GetByRootWorkItem(ctx, run.WorkItemID)
	if err != nil {
		return err
	}
	if goal.Status != domain.GoalActive {
		return nil
	}
	todo, err := s.store.Todos().Get(ctx, goal.CurrentTodoID)
	if err != nil {
		return err
	}
	handoff, err := s.store.Handoffs().Get(ctx, handoffID)
	if err != nil {
		return err
	}
	_, err = s.renewHandoffTodoClaimLocked(ctx, goal, todo, handoff, run.AgentProfileID)
	return err
}

// handoffSourceRunForContinuation records only the current protected control
// Run as the recovery checkpoint. Guessing from arbitrary same-root history can
// clone an unrelated ContextSnapshot, especially after a delegated target has
// completed several turns. A quiet control line therefore means fresh context.
func (s *Service) handoffSourceRunForContinuation(ctx context.Context, state *domain.TaskCoordinatorState,
	sourceAgentID string) (string, error) {
	if state == nil || sourceAgentID == "" {
		return "", fmt.Errorf("%w: Handoff source state and Agent are required", domain.ErrValidation)
	}
	if state.CurrentRunID == "" {
		return "", nil
	}
	current, err := s.store.Runs().Get(ctx, state.CurrentRunID)
	if err != nil {
		return "", err
	}
	if current.WorkspaceID != state.WorkspaceID || current.WorkItemID != state.RootWorkItemID ||
		current.AgentProfileID != sourceAgentID {
		return "", fmt.Errorf("%w: current Coordinator Run does not belong to the Handoff source", domain.ErrStateConflict)
	}
	control := coordinatorContextOf(current)
	if stringValue(control["role"]) != coordinatorRole ||
		stringValue(control["root_work_item_id"]) != state.RootWorkItemID ||
		stringValue(control["state_id"]) != state.ID {
		return "", fmt.Errorf("%w: Handoff source Run lacks protected Coordinator identity", domain.ErrStateConflict)
	}
	if sourceAgentID == state.CoordinatorAgentID {
		if !isSystemCoordinatorRun(current) {
			return "", fmt.Errorf("%w: system Handoff source Run identity mismatch", domain.ErrStateConflict)
		}
		return current.ID, nil
	}
	if !isDelegatedCoordinatorRun(current) || state.Data == nil ||
		stringValue(control[coordinatorHandoffIDKey]) != stringValue(state.Data[coordinatorHandoffIDKey]) ||
		stringValue(control[coordinatorHandoffTargetAgentIDKey]) != sourceAgentID {
		return "", fmt.Errorf("%w: delegated Handoff source is not the current continuation", domain.ErrStateConflict)
	}
	runClaimVersion, runClaimOK := governanceInt64(control[coordinatorHandoffTargetClaimVersionKey])
	stateClaimVersion, stateClaimOK := governanceInt64(state.Data[coordinatorHandoffTargetClaimVersionKey])
	if !runClaimOK || !stateClaimOK || runClaimVersion != stateClaimVersion {
		return "", fmt.Errorf("%w: delegated Handoff source claim generation mismatch", domain.ErrStateConflict)
	}
	return current.ID, nil
}

// queueHandoffContinuationLocked persists the exact recovery identity in the
// root Coordinator checkpoint. It intentionally creates no Run: the next
// Coordinator recovery pass is the sole consumer and therefore the sole
// place that can choose target runtime/session resume versus fresh.
func (s *Service) queueHandoffContinuationLocked(ctx context.Context, goal *domain.Goal,
	todo *domain.Todo, handoff *domain.Handoff, sourceAgentID string) error {
	if goal == nil || todo == nil || handoff == nil {
		return fmt.Errorf("%w: handoff continuation requires Goal/Todo/Handoff", domain.ErrValidation)
	}
	state, err := s.store.TaskCoordinators().GetState(ctx, goal.RootWorkItemID)
	if errors.Is(err, domain.ErrNotFound) {
		// A claim-only standalone Goal has no controlled continuation path. Keep
		// the Handoff pending and the source claim untouched rather than
		// reporting a successful transfer that can never execute.
		return fmt.Errorf("%w: Handoff acceptance requires the protected Coordinator control line", domain.ErrStateConflict)
	}
	if err != nil {
		return err
	}
	if state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled {
		return fmt.Errorf("%w: terminal Coordinator cannot accept an active Handoff", domain.ErrStateConflict)
	}
	if existingID := stringValue(state.Data[coordinatorHandoffIDKey]); existingID != "" && existingID != handoff.ID {
		existing, existingErr := s.store.Handoffs().Get(ctx, existingID)
		if existingErr != nil {
			return existingErr
		}
		existingTargetAgentID, targetErr := s.resolveHandoffActorAgent(ctx, goal.WorkspaceID, existing.Target)
		if targetErr != nil {
			return targetErr
		}
		stateClaimVersion, stateClaimOK := governanceInt64(state.Data[coordinatorHandoffTargetClaimVersionKey])
		if existing.Status != domain.HandoffTransferred || existing.GoalID != goal.ID || existing.TodoID != todo.ID ||
			existingTargetAgentID != sourceAgentID || existing.TargetClaimVersion != handoff.SourceClaimVersion ||
			!stateClaimOK || stateClaimVersion != int64(handoff.SourceClaimVersion) {
			return fmt.Errorf("%w: another Handoff continuation is already pending", domain.ErrIdempotencyConflict)
		}
	}
	targetAgentID, resolveErr := s.resolveHandoffActorAgent(ctx, goal.WorkspaceID, handoff.Target)
	if resolveErr != nil {
		return resolveErr
	}
	if todo.Claim == nil || todo.Claim.OwnerAgentID != targetAgentID ||
		todo.ClaimVersion != handoff.TargetClaimVersion {
		return fmt.Errorf("%w: accepted Handoff target claim is not current", domain.ErrStateConflict)
	}
	sourceRunID, err := s.handoffSourceRunForContinuation(ctx, state, sourceAgentID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expected := state.Version
	if state.Data == nil {
		state.Data = map[string]any{}
	}
	state.Data[coordinatorHandoffIDKey] = handoff.ID
	state.Data[coordinatorHandoffTargetAgentIDKey] = targetAgentID
	state.Data[coordinatorHandoffTargetClaimVersionKey] = handoff.TargetClaimVersion
	state.Data[coordinatorHandoffSourceRunIDKey] = sourceRunID
	state.Data["handoff_source_agent_id"] = sourceAgentID
	state.Data["control_action"] = coordinatorHandoffAction
	state.Phase = "handoff"
	state.CurrentAction = coordinatorHandoffAction
	state.LastError = ""
	state.BlockerCode, state.BlockerMessage = "", ""
	if state.CurrentRunID == "" {
		state.Status = domain.CoordinatorQueued
		state.NextActionAt = &now
	} else if current, getErr := s.store.Runs().Get(ctx, state.CurrentRunID); getErr == nil && current.Status.IsTerminal() {
		state.Status = domain.CoordinatorQueued
		state.CurrentRunID = ""
		state.NextActionAt = &now
	} else if getErr != nil && !errors.Is(getErr, domain.ErrNotFound) {
		return getErr
	}
	if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
		return err
	}
	state.Version = expected + 1
	return s.appendCoordinatorEvent(ctx, state, goal.RootWorkItemID,
		domain.EventCoordinatorRecoveryStarted, "Handoff 已接受，等待目标继续治理 Turn",
		sourceRunID, targetAgentID, state.Attempt, "handoff_accepted", &now,
		map[string]any{"stage": "handoff", "handoff_id": handoff.ID,
			"target_agent_id": targetAgentID, "target_claim_version": handoff.TargetClaimVersion,
			"source_run_id": sourceRunID, "next_action": coordinatorHandoffAction})
}

// validateDelegatedCoordinatorContext is the transaction-side proof for an
// ordinary Agent Run on a coordinated root. The caller has already checked the
// protected Coordinator state and the Agent workspace; this method binds the
// persisted Handoff, current claim generation and optional source snapshot so
// a public CreateRun caller cannot forge the delegated Planner role.
func (s *Service) validateDelegatedCoordinatorContext(ctx context.Context, root *domain.WorkItem,
	state *domain.TaskCoordinatorState, agent *domain.AgentProfile, data map[string]any) error {
	if root == nil || state == nil || agent == nil || !delegatedCoordinatorContext(data) {
		return fmt.Errorf("%w: delegated Coordinator context proof is incomplete", domain.ErrValidation)
	}
	if stringValue(data["role"]) != coordinatorRole || root.ID != state.RootWorkItemID || root.WorkspaceID != state.WorkspaceID {
		return fmt.Errorf("%w: delegated Coordinator root/state mismatch", domain.ErrWorkspaceContextMismatch)
	}
	if state.Status == domain.CoordinatorCompleted || state.Status == domain.CoordinatorCancelled ||
		state.Status == domain.CoordinatorBlocked {
		return fmt.Errorf("%w: delegated Coordinator state is stopped", domain.ErrStateConflict)
	}
	if stateID := stringValue(data["state_id"]); stateID != state.ID {
		return fmt.Errorf("%w: delegated Coordinator state proof mismatch", domain.ErrStateConflict)
	}
	handoffID := stringValue(data[coordinatorHandoffIDKey])
	if state.Data == nil || stringValue(state.Data[coordinatorHandoffIDKey]) != handoffID {
		return fmt.Errorf("%w: delegated Coordinator Handoff is not the current state checkpoint", domain.ErrStateConflict)
	}
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
	if handoff.Status != domain.HandoffTransferred || handoff.GoalID != goal.ID ||
		handoff.TodoID != todo.ID || todo.Claim == nil || todo.Claim.OwnerAgentID != agent.ID ||
		todo.ClaimVersion != handoff.TargetClaimVersion || !todo.Claim.ExpiresAt.After(time.Now().UTC()) {
		return fmt.Errorf("%w: delegated Coordinator Handoff/claim proof is stale", domain.ErrStateConflict)
	}
	targetAgentID, err := s.resolveHandoffActorAgent(ctx, goal.WorkspaceID, handoff.Target)
	if err != nil {
		return err
	}
	if targetAgentID != agent.ID || stringValue(data[coordinatorHandoffTargetAgentIDKey]) != agent.ID ||
		stringValue(state.Data[coordinatorHandoffTargetAgentIDKey]) != agent.ID {
		return fmt.Errorf("%w: delegated Coordinator target proof mismatch", domain.ErrStateConflict)
	}
	if handoff.Target.Kind == domain.GovernanceActorRuntime &&
		stringValue(data["handoff_target_runtime"]) != handoff.Target.ID {
		return fmt.Errorf("%w: delegated Coordinator runtime target proof mismatch", domain.ErrStateConflict)
	}
	claimVersion, ok := governanceInt64(data[coordinatorHandoffTargetClaimVersionKey])
	if !ok || claimVersion != int64(todo.ClaimVersion) || claimVersion != int64(handoff.TargetClaimVersion) {
		return fmt.Errorf("%w: delegated Coordinator claim generation proof mismatch", domain.ErrStateConflict)
	}
	stateClaimVersion, stateClaimVersionOK := governanceInt64(state.Data[coordinatorHandoffTargetClaimVersionKey])
	if !stateClaimVersionOK || stateClaimVersion != claimVersion {
		return fmt.Errorf("%w: delegated Coordinator state claim generation proof mismatch", domain.ErrStateConflict)
	}
	sourceRunID := stringValue(data[coordinatorHandoffSourceRunIDKey])
	if sourceRunID != stringValue(state.Data[coordinatorHandoffSourceRunIDKey]) {
		return fmt.Errorf("%w: delegated Coordinator source checkpoint mismatch", domain.ErrStateConflict)
	}
	sourceAgentID := stringValue(state.Data["handoff_source_agent_id"])
	if sourceAgentID == "" {
		return fmt.Errorf("%w: delegated Coordinator source Agent checkpoint is missing", domain.ErrStateConflict)
	}
	resolvedSourceAgentID, resolveErr := s.resolveHandoffActorAgent(ctx, goal.WorkspaceID, handoff.Source)
	if resolveErr != nil {
		return resolveErr
	}
	if resolvedSourceAgentID == targetAgentID || resolvedSourceAgentID != sourceAgentID {
		return fmt.Errorf("%w: delegated Coordinator source Agent checkpoint mismatch", domain.ErrStateConflict)
	}
	if sourceRunID != "" {
		sourceRun, sourceErr := s.store.Runs().Get(ctx, sourceRunID)
		if sourceErr != nil {
			return sourceErr
		}
		if sourceRun.WorkspaceID != root.WorkspaceID || sourceRun.WorkItemID != root.ID ||
			sourceRun.ContextSnapshotID == "" || sourceRun.AgentProfileID != sourceAgentID {
			return fmt.Errorf("%w: delegated Coordinator source Run proof is invalid", domain.ErrWorkspaceContextMismatch)
		}
	}
	return nil
}

// activateHandoffContinuationAfterSourceTerminal closes the small gap between
// an accepted Handoff and the source Run's terminal hook. It is a durable CAS
// transition only; StartCoordinator performs the subsequent target Run
// creation. Returning false means another recovery caller already advanced the
// checkpoint.
func (s *Service) activateHandoffContinuationAfterSourceTerminal(ctx context.Context,
	state *domain.TaskCoordinatorState, sourceRun *domain.ExecutionRun) (bool, error) {
	if state == nil || sourceRun == nil || !handoffContinuationPending(state) ||
		stringValue(state.Data[coordinatorHandoffSourceRunIDKey]) != sourceRun.ID ||
		state.CurrentRunID != sourceRun.ID || !sourceRun.Status.IsTerminal() {
		return false, nil
	}
	activated := false
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(txctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if !handoffContinuationPending(fresh) || fresh.CurrentRunID != sourceRun.ID {
			return nil
		}
		now := time.Now().UTC()
		expected := fresh.Version
		fresh.Status = domain.CoordinatorQueued
		fresh.Phase = "handoff"
		fresh.CurrentAction = coordinatorHandoffAction
		fresh.CurrentRunID = ""
		fresh.CurrentAgentID = stringValue(fresh.Data[coordinatorHandoffTargetAgentIDKey])
		fresh.NextActionAt = &now
		fresh.LastError = ""
		if err := s.store.TaskCoordinators().UpdateState(txctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		if err := s.appendCoordinatorEvent(txctx, fresh, fresh.RootWorkItemID,
			domain.EventCoordinatorRecoveryStarted, "源 Run 已终态，目标 Handoff continuation 已排队",
			sourceRun.ID, fresh.CurrentAgentID, fresh.Attempt, "handoff_source_terminal", &now,
			map[string]any{"stage": "handoff", "handoff_id": stringValue(fresh.Data[coordinatorHandoffIDKey]),
				"source_run_id": sourceRun.ID, "next_action": coordinatorHandoffAction}); err != nil {
			return err
		}
		activated = true
		return nil
	})
	return activated, err
}

func handoffIntentEqual(a, b *domain.Handoff) bool {
	return a != nil && b != nil && a.GoalID == b.GoalID && a.TodoID == b.TodoID &&
		a.Source == b.Source && a.Target == b.Target && a.Reason == b.Reason &&
		a.ContextSummary == b.ContextSummary && a.Actor == b.Actor && a.ClientKey == b.ClientKey &&
		handoffEvidenceEqual(a.Evidence, b.Evidence) && handoffStringsEqual(a.OpenRisks, b.OpenRisks)
}

func handoffEvidenceEqual(a, b []domain.GovernanceEvidenceItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func handoffStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// AcceptHandoff performs target acceptance and claim replacement in one
// transaction. The persisted outcome is transferred: there is no committed
// state in which the source claim is released without the target claim.
func (s *Service) AcceptHandoff(ctx context.Context, handoffID string, target domain.GovernanceActorRef,
	acceptance string) (*domain.Handoff, error) {
	var stored *domain.Handoff
	workspaceID := ""
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		h, err := s.store.Handoffs().Get(txctx, handoffID)
		if err != nil {
			return err
		}
		if h.Status == domain.HandoffTransferred {
			if h.Target != target || h.Acceptance != acceptance {
				return domain.ErrIdempotencyConflict
			}
			stored = h
			return nil
		}
		if h.Status != domain.HandoffPending {
			return domain.ErrStateConflict
		}
		goal, err := s.store.Goals().Get(txctx, h.GoalID)
		if err != nil {
			return err
		}
		todo, err := s.store.Todos().Get(txctx, h.TodoID)
		if err != nil {
			return err
		}
		if todo.GoalID != goal.ID || goal.CurrentTodoID != todo.ID || todo.Claim == nil || todo.ClaimVersion != h.SourceClaimVersion ||
			!todo.Claim.ExpiresAt.After(time.Now().UTC()) {
			return fmt.Errorf("%w: source claim changed since handoff creation", domain.ErrStateConflict)
		}
		workspaceID = goal.WorkspaceID
		sourceAgent, err := s.resolveHandoffActorAgent(txctx, goal.WorkspaceID, h.Source)
		if err != nil {
			return err
		}
		targetAgent, err := s.resolveHandoffActorAgent(txctx, goal.WorkspaceID, target)
		if err != nil {
			return err
		}
		if target != h.Target || todo.Claim.OwnerAgentID != sourceAgent || !scopeHasAgent(todo, targetAgent) {
			return fmt.Errorf("%w: handoff target/source claim mismatch", domain.ErrStateConflict)
		}
		now := time.Now().UTC()
		claimed, err := s.store.Todos().TransferClaim(txctx, todo.ID, sourceAgent, targetAgent,
			now, now.Add(governancePlanClaimTTL), todo.Version, todo.ClaimVersion)
		if err != nil {
			return err
		}
		if err := h.Accept(target, acceptance, now, claimed.ClaimVersion); err != nil {
			return err
		}
		if err := h.Transfer(now); err != nil {
			return err
		}
		if err := s.store.Handoffs().Update(txctx, h, h.Version-2); err != nil {
			return err
		}
		// Claim transfer and the durable Coordinator continuation belong to the
		// same transaction. The continuation is only a checkpoint here; its
		// target Run is created later by StartCoordinator after fresh authority
		// checks, so AcceptHandoff never dispatches a side effect itself.
		if err := s.queueHandoffContinuationLocked(txctx, goal, claimed, h, sourceAgent); err != nil {
			return err
		}
		if err := s.emitTodoClaimChanged(txctx, goal.WorkspaceID, claimed, "claimed", targetAgent, &claimed.Claim.ExpiresAt); err != nil {
			return err
		}
		if err := s.emitHandoffEvent(txctx, goal.WorkspaceID, todo.ID, h, string(domain.HandoffPending), string(h.Status)); err != nil {
			return err
		}
		stored = h
		return nil
	})
	if err != nil {
		return nil, err
	}
	if stored != nil && workspaceID != "" {
		s.notifier.Notify(workspaceID)
	}
	return stored, nil
}

func (s *Service) RejectHandoff(ctx context.Context, handoffID, reason string) (*domain.Handoff, error) {
	return s.resolveHandoffTerminal(ctx, handoffID, false, reason)
}

func (s *Service) CancelHandoff(ctx context.Context, handoffID string) (*domain.Handoff, error) {
	return s.resolveHandoffTerminal(ctx, handoffID, true, "")
}

func (s *Service) resolveHandoffTerminal(ctx context.Context, handoffID string, cancel bool, reason string) (*domain.Handoff, error) {
	var stored *domain.Handoff
	workspaceID := ""
	err := s.store.InTx(ctx, func(txctx context.Context) error {
		h, err := s.store.Handoffs().Get(txctx, handoffID)
		if err != nil {
			return err
		}
		if cancel && h.Status == domain.HandoffCancelled {
			if strings.TrimSpace(reason) != "" || h.ResolutionReason != "" {
				return domain.ErrIdempotencyConflict
			}
			stored = h
			return nil
		}
		if !cancel && h.Status == domain.HandoffRejected {
			if strings.TrimSpace(reason) != h.ResolutionReason {
				return domain.ErrIdempotencyConflict
			}
			stored = h
			return nil
		}
		if h.Status != domain.HandoffPending {
			return domain.ErrStateConflict
		}
		resolutionReason := strings.TrimSpace(reason)
		if !cancel && resolutionReason == "" {
			return fmt.Errorf("%w: rejection reason required", domain.ErrValidation)
		}
		if !cancel {
			if err := h.SetResolutionReason(resolutionReason); err != nil {
				return err
			}
		}
		expected := h.Version
		if cancel {
			if err := h.Cancel(time.Now().UTC()); err != nil {
				return err
			}
		} else if err := h.Reject(time.Now().UTC()); err != nil {
			return err
		}
		if err := s.store.Handoffs().Update(txctx, h, expected); err != nil {
			return err
		}
		goal, err := s.store.Goals().Get(txctx, h.GoalID)
		if err != nil {
			return err
		}
		workspaceID = goal.WorkspaceID
		if err := s.emitHandoffEvent(txctx, goal.WorkspaceID, h.TodoID, h, string(domain.HandoffPending), string(h.Status)); err != nil {
			return err
		}
		stored = h
		return nil
	})
	if err != nil {
		return nil, err
	}
	if stored != nil && workspaceID != "" {
		s.notifier.Notify(workspaceID)
	}
	return stored, nil
}

func (s *Service) Handoff(ctx context.Context, id string) (*domain.Handoff, error) {
	return s.store.Handoffs().Get(ctx, id)
}

func (s *Service) HandoffsByTodo(ctx context.Context, todoID string) ([]*domain.Handoff, error) {
	return s.store.Handoffs().ListByTodo(ctx, todoID)
}

func (s *Service) HandoffsByGoal(ctx context.Context, goalID string) ([]*domain.Handoff, error) {
	return s.store.Handoffs().ListByGoal(ctx, goalID)
}

func (s *Service) emitHandoffEvent(ctx context.Context, workspaceID, todoID string, h *domain.Handoff, from, to string) error {
	if h == nil {
		return fmt.Errorf("%w: handoff event requires handoff", domain.ErrValidation)
	}
	if from == "" {
		return s.emit(ctx, workspaceID, domain.EventHandoffCreated, domain.AggregateHandoff, h.ID, h.Version, nil,
			map[string]any{"goal_id": h.GoalID, "handoff_id": h.ID, "status": string(h.Status)})
	}
	return s.emit(ctx, workspaceID, domain.EventHandoffStateChanged, domain.AggregateHandoff, h.ID, h.Version, nil,
		map[string]any{"goal_id": h.GoalID, "handoff_id": h.ID, "from_state": from, "to_state": to,
			"status": string(h.Status), "claim_transfer_state": string(h.ClaimTransferState)})
}
