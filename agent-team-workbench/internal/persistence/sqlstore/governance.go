package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	governancekernel "github.com/ybs/agent-team-workbench/internal/governance"
)

// Governance persistence deliberately keeps the intent aggregates separate
// from WorkItem/Plan/Run. Goal/Todo rows are mutable projections guarded by
// version CAS; receipt headers and phases are immutable canonical records.

type GoalRepo struct{ store *Store }
type TodoRepo struct{ store *Store }
type TurnReceiptRepo struct{ store *Store }

var _ application.GoalRepo = (*GoalRepo)(nil)
var _ application.TodoRepo = (*TodoRepo)(nil)
var _ application.TurnReceiptRepo = (*TurnReceiptRepo)(nil)

const goalCols = `id, workspace_id, root_work_item_id, objective,
	acceptance_contract, status, phase, current_todo_id, quota_policies,
	completion_evidence_summary, version, created_at, updated_at`

const todoCols = `id, goal_id, class, status, instruction, acceptance,
	resume_condition, priority, predecessors, successors, decision_scope,
	claim_owner_agent_id, claim_version, claim_claimed_at, claim_expires_at,
	last_turn_seq, completion_turn_seq, completion_evidence_id, version, created_at, updated_at`

const headerCols = `goal_id, todo_id, turn_seq, attempt, schema_version,
	input_snapshot_digest, admission_client_key, source_run_id, plan_client_key,
	decision_digest, canonical_digest, created_at`

const phaseCols = `goal_id, todo_id, turn_seq, phase_seq, phase, payload,
	canonical_digest, plan_id, run_ids, quota_reservation_keys, evidence, created_at`

func (r *GoalRepo) scan(row interface{ Scan(...any) error }, goal *domain.Goal) error {
	var acceptance, quotaPolicies, evidence string
	var currentTodoID *string
	var created, updated scanTime
	if err := row.Scan(&goal.ID, &goal.WorkspaceID, &goal.RootWorkItemID, &goal.Objective,
		&acceptance, &goal.Status, &goal.Phase, &currentTodoID, &quotaPolicies,
		&evidence, &goal.Version, &created, &updated); err != nil {
		return err
	}
	if currentTodoID != nil {
		goal.CurrentTodoID = *currentTodoID
	}
	if err := jsonInto(acceptance, &goal.AcceptanceContract); err != nil {
		return err
	}
	if goal.AcceptanceContract == nil {
		goal.AcceptanceContract = []string{}
	}
	if err := jsonInto(quotaPolicies, &goal.QuotaPolicies); err != nil {
		return err
	}
	if goal.QuotaPolicies == nil {
		goal.QuotaPolicies = []domain.QuotaPolicy{}
	}
	if err := jsonInto(evidence, &goal.CompletionEvidenceSummary); err != nil {
		return err
	}
	if goal.CompletionEvidenceSummary == nil {
		goal.CompletionEvidenceSummary = []domain.GovernanceEvidenceItem{}
	}
	goal.CreatedAt, goal.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

// Create persists a Goal after validating its root binding. The root is
// immutable in this repository; duplicate root/ID violations are surfaced as
// the existing idempotency conflict sentinel for the service replay path.
func (r *GoalRepo) Create(ctx context.Context, goal *domain.Goal) error {
	if goal == nil {
		return fmt.Errorf("%w: goal required", domain.ErrValidation)
	}
	if goal.CreatedAt.IsZero() {
		goal.CreatedAt = timeNow()
	}
	if goal.UpdatedAt.IsZero() {
		goal.UpdatedAt = goal.CreatedAt
	}
	if err := goal.Validate(); err != nil {
		return err
	}
	if err := r.validateGoalRoot(ctx, goal.WorkspaceID, goal.RootWorkItemID); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO goals(`+goalCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		goal.ID, goal.WorkspaceID, goal.RootWorkItemID, goal.Objective,
		jsonText(nonNilStrings(goal.AcceptanceContract)), goal.Status, goal.Phase,
		nullString(goal.CurrentTodoID), jsonText(nonNilQuotaPolicies(goal.QuotaPolicies)),
		jsonText(nonNilEvidence(goal.CompletionEvidenceSummary)), goal.Version,
		timeParam(goal.CreatedAt), timeParam(goal.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *GoalRepo) validateGoalRoot(ctx context.Context, workspaceID, rootWorkItemID string) error {
	var count int
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT COUNT(*) FROM work_items
		 WHERE id=? AND workspace_id=? AND parent_id IS NULL AND record_kind=?`,
		rootWorkItemID, workspaceID, domain.RecordKindTask).Scan(&count); err != nil {
		return r.store.mapErr(err)
	}
	if count != 1 {
		return fmt.Errorf("%w: Goal root must be a same-workspace root task", domain.ErrValidation)
	}
	return nil
}

func (r *GoalRepo) Get(ctx context.Context, id string) (*domain.Goal, error) {
	goal := &domain.Goal{}
	if err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+goalCols+` FROM goals WHERE id=?`, id), goal); err != nil {
		return nil, r.store.mapErr(err)
	}
	return goal, nil
}

func (r *GoalRepo) GetByRootWorkItem(ctx context.Context, rootWorkItemID string) (*domain.Goal, error) {
	if strings.TrimSpace(rootWorkItemID) == "" {
		return nil, fmt.Errorf("%w: root work item id required", domain.ErrValidation)
	}
	goal := &domain.Goal{}
	if err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+goalCols+` FROM goals WHERE root_work_item_id=?`, rootWorkItemID), goal); err != nil {
		return nil, r.store.mapErr(err)
	}
	return goal, nil
}

func (r *GoalRepo) List(ctx context.Context, workspaceID string) ([]*domain.Goal, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+goalCols+` FROM goals WHERE workspace_id=? ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.Goal
	for rows.Next() {
		goal := &domain.Goal{}
		if err := r.scan(rows, goal); err != nil {
			return nil, err
		}
		out = append(out, goal)
	}
	return out, rows.Err()
}

func (r *GoalRepo) Update(ctx context.Context, goal *domain.Goal, expectedVersion int) error {
	if goal == nil {
		return fmt.Errorf("%w: goal required", domain.ErrValidation)
	}
	current, err := r.Get(ctx, goal.ID)
	if err != nil {
		return err
	}
	if current.Version != expectedVersion || goal.Version != expectedVersion+1 {
		return domain.ErrVersionConflict
	}
	if current.WorkspaceID != goal.WorkspaceID || current.RootWorkItemID != goal.RootWorkItemID {
		return fmt.Errorf("%w: Goal workspace/root binding is immutable", domain.ErrValidation)
	}
	if current.Status.IsTerminal() {
		return domain.ErrTerminalImmutable
	}
	updatedAt := goal.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = timeNow()
		goal.UpdatedAt = updatedAt
	}
	if current.Status != goal.Status {
		probe := *current
		probe.CompletionEvidenceSummary = append([]domain.GovernanceEvidenceItem(nil), goal.CompletionEvidenceSummary...)
		if err := probe.Transition(goal.Status, updatedAt); err != nil {
			return err
		}
	}
	if err := goal.Validate(); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE goals SET objective=?, acceptance_contract=?, status=?, phase=?,
			current_todo_id=?, quota_policies=?, completion_evidence_summary=?,
			version=?, updated_at=?
		 WHERE id=? AND workspace_id=? AND root_work_item_id=? AND version=?`,
		goal.Objective, jsonText(nonNilStrings(goal.AcceptanceContract)), goal.Status, goal.Phase,
		nullString(goal.CurrentTodoID), jsonText(nonNilQuotaPolicies(goal.QuotaPolicies)),
		jsonText(nonNilEvidence(goal.CompletionEvidenceSummary)), goal.Version, timeParam(updatedAt),
		goal.ID, goal.WorkspaceID, goal.RootWorkItemID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilQuotaPolicies(values []domain.QuotaPolicy) []domain.QuotaPolicy {
	if values == nil {
		return []domain.QuotaPolicy{}
	}
	return values
}

func nonNilEvidence(values []domain.GovernanceEvidenceItem) []domain.GovernanceEvidenceItem {
	if values == nil {
		return []domain.GovernanceEvidenceItem{}
	}
	return values
}

func (r *TodoRepo) scan(row interface{ Scan(...any) error }, todo *domain.Todo) error {
	var acceptance, predecessors, successors, decisionScope string
	var resumeCondition, claimOwnerID, completionEvidenceID *string
	var completionTurnSeq sql.NullInt64
	var claimClaimedAt, claimExpiresAt scanTime
	var created, updated scanTime
	if err := row.Scan(&todo.ID, &todo.GoalID, &todo.Class, &todo.Status, &todo.Instruction,
		&acceptance, &resumeCondition, &todo.Priority, &predecessors, &successors,
		&decisionScope, &claimOwnerID, &todo.ClaimVersion, &claimClaimedAt,
		&claimExpiresAt, &todo.LastTurnSeq, &completionTurnSeq, &completionEvidenceID,
		&todo.Version, &created, &updated); err != nil {
		return err
	}
	if resumeCondition != nil {
		todo.ResumeCondition = *resumeCondition
	}
	if err := jsonInto(acceptance, &todo.Acceptance); err != nil {
		return err
	}
	if todo.Acceptance == nil {
		todo.Acceptance = []string{}
	}
	if err := jsonInto(predecessors, &todo.Predecessors); err != nil {
		return err
	}
	if todo.Predecessors == nil {
		todo.Predecessors = []string{}
	}
	if err := jsonInto(successors, &todo.Successors); err != nil {
		return err
	}
	if todo.Successors == nil {
		todo.Successors = []string{}
	}
	if err := jsonInto(decisionScope, &todo.DecisionScope); err != nil {
		return err
	}
	if claimOwnerID != nil {
		todo.Claim = &domain.TodoClaim{
			OwnerAgentID: *claimOwnerID,
			Version:      todo.ClaimVersion,
			ClaimedAt:    mustTime(claimClaimedAt),
			ExpiresAt:    mustTime(claimExpiresAt),
		}
	}
	if completionTurnSeq.Valid && completionTurnSeq.Int64 > 0 {
		todo.CompletionTurnKey = &domain.TurnKey{GoalID: todo.GoalID, TodoID: todo.ID, TurnSeq: completionTurnSeq.Int64}
	}
	if completionEvidenceID != nil {
		todo.CompletionEvidenceID = *completionEvidenceID
	}
	todo.CreatedAt, todo.UpdatedAt = mustTime(created), mustTime(updated)
	return nil
}

func (r *TodoRepo) validateTodoScope(ctx context.Context, todo *domain.Todo) error {
	var workspaceID string
	if err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT workspace_id FROM goals WHERE id=?`, todo.GoalID).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: Goal not found", domain.ErrValidation)
		}
		return r.store.mapErr(err)
	}
	for _, workItemID := range todo.DecisionScope.WorkItemIDs {
		var count int
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT COUNT(*) FROM work_items WHERE id=? AND workspace_id=?`, workItemID, workspaceID).Scan(&count); err != nil {
			return r.store.mapErr(err)
		}
		if count != 1 {
			return fmt.Errorf("%w: Todo decision scope work item is outside Goal workspace", domain.ErrValidation)
		}
	}
	for _, agentID := range todo.DecisionScope.AgentIDs {
		var count int
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT COUNT(*) FROM agent_profiles WHERE id=? AND workspace_id=?`, agentID, workspaceID).Scan(&count); err != nil {
			return r.store.mapErr(err)
		}
		if count != 1 {
			return fmt.Errorf("%w: Todo decision scope agent is outside Goal workspace", domain.ErrValidation)
		}
	}
	if todo.Claim != nil {
		if !todoAgentInScope(todo, todo.Claim.OwnerAgentID) {
			return fmt.Errorf("%w: Todo claim agent is not in DecisionScope", domain.ErrStateConflict)
		}
		var count int
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT COUNT(*) FROM agent_profiles WHERE id=? AND workspace_id=?`,
			todo.Claim.OwnerAgentID, workspaceID).Scan(&count); err != nil {
			return r.store.mapErr(err)
		}
		if count != 1 {
			return fmt.Errorf("%w: Todo claim agent is outside Goal workspace", domain.ErrValidation)
		}
	}
	return nil
}

func todoAgentInScope(todo *domain.Todo, agentID string) bool {
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

func (r *TodoRepo) Create(ctx context.Context, todo *domain.Todo) error {
	if todo == nil {
		return fmt.Errorf("%w: todo required", domain.ErrValidation)
	}
	if todo.CreatedAt.IsZero() {
		todo.CreatedAt = timeNow()
	}
	if todo.UpdatedAt.IsZero() {
		todo.UpdatedAt = todo.CreatedAt
	}
	if err := todo.Validate(); err != nil {
		return err
	}
	if err := r.validateTodoScope(ctx, todo); err != nil {
		return err
	}
	var claimOwner, claimClaimedAt, claimExpiresAt any
	if todo.Claim != nil {
		claimOwner = todo.Claim.OwnerAgentID
		claimClaimedAt = timeParam(todo.Claim.ClaimedAt)
		claimExpiresAt = timeParam(todo.Claim.ExpiresAt)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO goal_todos(`+todoCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		todo.ID, todo.GoalID, todo.Class, todo.Status, todo.Instruction,
		jsonText(nonNilStrings(todo.Acceptance)), nullString(todo.ResumeCondition), todo.Priority,
		jsonText(nonNilStrings(todo.Predecessors)), jsonText(nonNilStrings(todo.Successors)),
		jsonText(todo.DecisionScope), claimOwner, todo.ClaimVersion, claimClaimedAt,
		claimExpiresAt, todo.LastTurnSeq, todoCompletionTurnSeq(todo), nullString(todo.CompletionEvidenceID),
		todo.Version, timeParam(todo.CreatedAt), timeParam(todo.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *TodoRepo) Get(ctx context.Context, id string) (*domain.Todo, error) {
	todo := &domain.Todo{}
	if err := r.scan(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+todoCols+` FROM goal_todos WHERE id=?`, id), todo); err != nil {
		return nil, r.store.mapErr(err)
	}
	return todo, nil
}

func (r *TodoRepo) ListByGoal(ctx context.Context, goalID string) ([]*domain.Todo, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+todoCols+` FROM goal_todos WHERE goal_id=? ORDER BY created_at, id`, goalID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.Todo
	for rows.Next() {
		todo := &domain.Todo{}
		if err := r.scan(rows, todo); err != nil {
			return nil, err
		}
		out = append(out, todo)
	}
	return out, rows.Err()
}

// Update intentionally excludes claim, claim_version and last_turn_seq.
// Ownership and admission are the only write points for those fields.
func (r *TodoRepo) Update(ctx context.Context, todo *domain.Todo, expectedVersion int) error {
	if todo == nil {
		return fmt.Errorf("%w: todo required", domain.ErrValidation)
	}
	current, err := r.Get(ctx, todo.ID)
	if err != nil {
		return err
	}
	if current.Version != expectedVersion || todo.Version != expectedVersion+1 {
		return domain.ErrVersionConflict
	}
	if current.GoalID != todo.GoalID || current.LastTurnSeq != todo.LastTurnSeq ||
		current.ClaimVersion != todo.ClaimVersion || !todoClaimsEqual(current.Claim, todo.Claim) {
		return fmt.Errorf("%w: Todo identity/claim/watermark fields require dedicated write points", domain.ErrValidation)
	}
	if !todoCompletionKeysEqual(current, todo) {
		return fmt.Errorf("%w: Todo completion identity requires the dedicated completion write point", domain.ErrValidation)
	}
	if current.ClaimVersion > 0 && !todoDecisionScopesEqual(current.DecisionScope, todo.DecisionScope) {
		return fmt.Errorf("%w: Todo DecisionScope is immutable after claim/admission", domain.ErrStateConflict)
	}
	if current.Status.IsTerminal() {
		return domain.ErrTerminalImmutable
	}
	if todo.Status == domain.TodoRunning && current.Status != domain.TodoRunning {
		return fmt.Errorf("%w: Todo running is admission-only", domain.ErrStateConflict)
	}
	if current.Claim != nil && !todoAgentInScope(todo, current.Claim.OwnerAgentID) {
		return fmt.Errorf("%w: active Todo claim agent is not in DecisionScope", domain.ErrStateConflict)
	}
	updatedAt := todo.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = timeNow()
		todo.UpdatedAt = updatedAt
	}
	if current.Status != todo.Status {
		probe := *current
		if err := probe.Transition(todo.Status, updatedAt); err != nil {
			return err
		}
	}
	if err := todo.Validate(); err != nil {
		return err
	}
	if err := r.validateTodoScope(ctx, todo); err != nil {
		return err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE goal_todos SET class=?, status=?, instruction=?, acceptance=?,
			resume_condition=?, priority=?, predecessors=?, successors=?, decision_scope=?,
			completion_turn_seq=?, completion_evidence_id=?,
			version=?, updated_at=?
		 WHERE id=? AND goal_id=? AND version=?
		   AND (? NOT IN ('claimed','running') OR claim_owner_agent_id IS NOT NULL)`,
		todo.Class, todo.Status, todo.Instruction, jsonText(nonNilStrings(todo.Acceptance)),
		nullString(todo.ResumeCondition), todo.Priority, jsonText(nonNilStrings(todo.Predecessors)),
		jsonText(nonNilStrings(todo.Successors)), jsonText(todo.DecisionScope), todoCompletionTurnSeq(todo),
		nullString(todo.CompletionEvidenceID), todo.Version, timeParam(updatedAt),
		todo.ID, todo.GoalID, expectedVersion, todo.Status)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

// Complete is the dedicated acceptance write point. It is deliberately
// narrower than Update so callers cannot mark a Todo completed without tying
// it to an admitted TurnKey and an accepted evidence identity.
func (r *TodoRepo) Complete(ctx context.Context, todoID string, key domain.TurnKey,
	evidenceID string, completedAt time.Time, expectedVersion int) (*domain.Todo, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if _, err := r.store.turnReceipts.GetHeader(ctx, key); err != nil {
		return nil, fmt.Errorf("Todo completion requires an admitted TurnReceipt Header: %w", err)
	}
	if strings.TrimSpace(evidenceID) == "" {
		return nil, fmt.Errorf("%w: Todo completion evidence is required", domain.ErrValidation)
	}
	current, err := r.Get(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if current.Status == domain.TodoCompleted {
		if current.CompletionTurnKey != nil && current.CompletionTurnKey.Equal(key) &&
			current.CompletionEvidenceID == evidenceID {
			return current, nil
		}
		return nil, domain.ErrIdempotencyConflict
	}
	if current.Version != expectedVersion {
		return nil, domain.ErrVersionConflict
	}
	if current.GoalID != key.GoalID || current.ID != key.TodoID || current.LastTurnSeq != key.TurnSeq {
		return nil, fmt.Errorf("%w: Todo completion identity does not match current turn", domain.ErrStateConflict)
	}
	if current.Status != domain.TodoWaiting && current.Status != domain.TodoRunning {
		return nil, fmt.Errorf("%w: Todo status %s cannot be completed by acceptance", domain.ErrStateConflict, current.Status)
	}
	if err := current.CompleteWithEvidence(key, evidenceID, completedAt); err != nil {
		return nil, err
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE goal_todos SET status='completed', claim_owner_agent_id=NULL,
			claim_claimed_at=NULL, claim_expires_at=NULL,
			claim_version=claim_version+1, completion_turn_seq=?, completion_evidence_id=?, version=?, updated_at=?
		 WHERE id=? AND goal_id=? AND version=? AND last_turn_seq=?
		   AND status IN ('waiting','running')`,
		key.TurnSeq, evidenceID, current.Version, timeParam(completedAt),
		todoID, key.GoalID, expectedVersion, key.TurnSeq)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, domain.ErrVersionConflict
	}
	return r.Get(ctx, todoID)
}

func todoClaimsEqual(left, right *domain.TodoClaim) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.OwnerAgentID == right.OwnerAgentID && left.Version == right.Version &&
		left.ClaimedAt.Equal(right.ClaimedAt) && left.ExpiresAt.Equal(right.ExpiresAt)
}

func todoCompletionTurnSeq(todo *domain.Todo) any {
	if todo == nil || todo.CompletionTurnKey == nil {
		return int64(0)
	}
	return todo.CompletionTurnKey.TurnSeq
}

func todoCompletionKeysEqual(left, right *domain.Todo) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.CompletionEvidenceID != right.CompletionEvidenceID {
		return false
	}
	if left.CompletionTurnKey == nil || right.CompletionTurnKey == nil {
		return left.CompletionTurnKey == nil && right.CompletionTurnKey == nil
	}
	return left.CompletionTurnKey.Equal(*right.CompletionTurnKey)
}

func todoDecisionScopesEqual(left, right domain.DecisionScope) bool {
	return left.MaxDispatch == right.MaxDispatch &&
		slices.Equal(left.WorkItemIDs, right.WorkItemIDs) &&
		slices.Equal(left.AgentIDs, right.AgentIDs) &&
		slices.Equal(left.RuntimeCapabilities, right.RuntimeCapabilities) &&
		slices.Equal(left.WriteScopes, right.WriteScopes)
}

func (r *TodoRepo) Claim(ctx context.Context, todoID, ownerAgentID string, claimedAt, expiresAt time.Time, expectedVersion int) (*domain.Todo, error) {
	if ownerAgentID == "" {
		return nil, fmt.Errorf("%w: claim owner required", domain.ErrValidation)
	}
	claim := &domain.TodoClaim{OwnerAgentID: ownerAgentID, ClaimedAt: claimedAt, ExpiresAt: expiresAt, Version: 1}
	if err := claim.Validate(); err != nil {
		return nil, err
	}
	current, err := r.Get(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if current.Version != expectedVersion {
		return nil, domain.ErrVersionConflict
	}
	if err := r.validateTodoScope(ctx, current); err != nil {
		return nil, err
	}
	if current.Status.IsTerminal() {
		return nil, domain.ErrStateConflict
	}
	if !todoAgentInScope(current, ownerAgentID) {
		return nil, domain.ErrStateConflict
	}
	if current.Claim != nil {
		if current.Claim.OwnerAgentID != ownerAgentID && current.Claim.ExpiresAt.After(claimedAt) {
			return nil, domain.ErrStateConflict
		}
		if current.Status == domain.TodoRunning && current.Claim.OwnerAgentID != ownerAgentID {
			return nil, domain.ErrStateConflict
		}
	}
	if current.Status == domain.TodoRunning && (current.Claim == nil || current.Claim.OwnerAgentID != ownerAgentID) {
		return nil, domain.ErrStateConflict
	}
	// The trigger enforces claim_version = old + 1. This is the ABA guard
	// retained even when Release cleared the nullable claim columns.
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE goal_todos
		 SET claim_owner_agent_id=?, claim_version=claim_version+1,
			 claim_claimed_at=?, claim_expires_at=?,
			 status=CASE WHEN status='running' THEN 'running' ELSE 'claimed' END,
			 version=version+1, updated_at=?
		 WHERE id=? AND version=?
		   AND (status IN ('pending','claimed','waiting','blocked')
		        OR (status='running' AND claim_owner_agent_id=?))
		   AND (claim_owner_agent_id IS NULL OR claim_owner_agent_id=? OR claim_expires_at<=?)`,
		ownerAgentID, timeParam(claimedAt), timeParam(expiresAt), timeParam(claimedAt),
		todoID, expectedVersion, ownerAgentID, ownerAgentID, timeParam(claimedAt))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, domain.ErrVersionConflict
	}
	return r.Get(ctx, todoID)
}

// TransferClaim is the handoff-specific ownership primitive. It replaces the
// source owner in one CAS instead of releasing then claiming in two writes;
// therefore a failed target transfer cannot leave the Todo ownerless.
func (r *TodoRepo) TransferClaim(ctx context.Context, todoID, sourceAgentID, targetAgentID string,
	claimedAt, expiresAt time.Time, expectedVersion, sourceClaimVersion int) (*domain.Todo, error) {
	if sourceAgentID == "" || targetAgentID == "" || sourceAgentID == targetAgentID || sourceClaimVersion < 1 {
		return nil, fmt.Errorf("%w: handoff claim transfer identities are invalid", domain.ErrValidation)
	}
	if !expiresAt.After(claimedAt) {
		return nil, fmt.Errorf("%w: handoff target claim must expire after claim time", domain.ErrValidation)
	}
	current, err := r.Get(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if current.Version != expectedVersion || current.Claim == nil ||
		current.Claim.OwnerAgentID != sourceAgentID || current.ClaimVersion != sourceClaimVersion ||
		!current.Claim.ExpiresAt.After(claimedAt) || current.Status.IsTerminal() {
		return nil, domain.ErrStateConflict
	}
	if !todoAgentInScope(current, targetAgentID) {
		return nil, domain.ErrStateConflict
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE goal_todos
		 SET claim_owner_agent_id=?, claim_version=claim_version+1,
		     claim_claimed_at=?, claim_expires_at=?, version=version+1, updated_at=?
		 WHERE id=? AND version=? AND claim_owner_agent_id=? AND claim_version=?
		   AND claim_expires_at>? AND status NOT IN ('completed','cancelled')`,
		targetAgentID, timeParam(claimedAt), timeParam(expiresAt), timeParam(claimedAt),
		todoID, expectedVersion, sourceAgentID, sourceClaimVersion, timeParam(claimedAt))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, domain.ErrVersionConflict
	}
	return r.Get(ctx, todoID)
}

func (r *TodoRepo) RenewClaim(ctx context.Context, todoID, ownerAgentID string, claimVersion int,
	renewedAt, expiresAt time.Time, expectedVersion int) (*domain.Todo, error) {
	if ownerAgentID == "" || claimVersion < 1 || !expiresAt.After(renewedAt) {
		return nil, fmt.Errorf("%w: invalid Todo claim renewal", domain.ErrValidation)
	}
	current, err := r.Get(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if current.Version != expectedVersion || current.Claim == nil || current.Status.IsTerminal() ||
		current.Claim.OwnerAgentID != ownerAgentID || current.ClaimVersion != claimVersion ||
		current.Claim.Version != claimVersion {
		return nil, domain.ErrStateConflict
	}
	if !expiresAt.After(current.Claim.ExpiresAt) {
		return nil, fmt.Errorf("%w: Todo claim renewal must extend expiry", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE goal_todos SET claim_expires_at=?, version=version+1, updated_at=?
			 WHERE id=? AND version=? AND claim_owner_agent_id=? AND claim_version=?
			   AND status NOT IN ('completed','cancelled')`,
		timeParam(expiresAt), timeParam(renewedAt),
		todoID, expectedVersion, ownerAgentID, claimVersion)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, domain.ErrVersionConflict
	}
	return r.Get(ctx, todoID)
}

func (r *TodoRepo) ResumeAdmitted(ctx context.Context, todoID, ownerAgentID string,
	turnSeq int64, resumedAt time.Time, expectedVersion int) (*domain.Todo, error) {
	if ownerAgentID == "" || turnSeq < 1 {
		return nil, fmt.Errorf("%w: admitted Todo resume requires owner and turn_seq", domain.ErrValidation)
	}
	var resumed *domain.Todo
	err := r.store.InTx(ctx, func(txctx context.Context) error {
		current, err := r.Get(txctx, todoID)
		if err != nil {
			return err
		}
		if current.Version != expectedVersion {
			return domain.ErrVersionConflict
		}
		if current.Status != domain.TodoClaimed || current.Claim == nil ||
			current.Claim.OwnerAgentID != ownerAgentID || current.LastTurnSeq != turnSeq ||
			!current.Claim.ExpiresAt.After(resumedAt) || !todoAgentInScope(current, ownerAgentID) {
			return domain.ErrStateConflict
		}
		res, err := r.store.execStmt(txctx, r.store.exec(txctx),
			`UPDATE goal_todos SET status='running', version=version+1, updated_at=?
			 WHERE id=? AND version=? AND status='claimed' AND last_turn_seq=?
			   AND claim_owner_agent_id=? AND claim_expires_at>?`,
			timeParam(resumedAt), todoID, expectedVersion, turnSeq, ownerAgentID, timeParam(resumedAt))
		if err != nil {
			return r.store.mapErr(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return domain.ErrVersionConflict
		}
		resumed, err = r.Get(txctx, todoID)
		return err
	})
	return resumed, err
}

func (r *TodoRepo) Release(ctx context.Context, todoID, ownerAgentID string, releasedAt time.Time, expectedVersion int) (*domain.Todo, error) {
	current, err := r.Get(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if current.Version != expectedVersion {
		return nil, domain.ErrVersionConflict
	}
	if current.Claim == nil || current.Claim.OwnerAgentID != ownerAgentID {
		return nil, domain.ErrStateConflict
	}
	if current.Status == domain.TodoRunning {
		return nil, domain.ErrStateConflict
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE goal_todos
		 SET claim_owner_agent_id=NULL, claim_version=claim_version+1,
			 claim_claimed_at=NULL, claim_expires_at=NULL,
			 status=CASE WHEN status='claimed' THEN 'pending' ELSE status END,
			 version=version+1, updated_at=?
		 WHERE id=? AND version=? AND claim_owner_agent_id=?`,
		timeParam(releasedAt), todoID, expectedVersion, ownerAgentID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, domain.ErrVersionConflict
	}
	return r.Get(ctx, todoID)
}

// Cancel terminalizes governance intent and releases any active governance
// claim in the same CAS. It never touches a Run or Runner lease.
func (r *TodoRepo) Cancel(ctx context.Context, todoID string, cancelledAt time.Time, expectedVersion int) (*domain.Todo, error) {
	current, err := r.Get(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if current.Version != expectedVersion {
		return nil, domain.ErrVersionConflict
	}
	if current.Status == domain.TodoCancelled {
		return current, nil
	}
	if current.Status.IsTerminal() {
		return nil, domain.ErrTerminalImmutable
	}
	var res sql.Result
	if current.Claim != nil {
		res, err = r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE goal_todos
			 SET status='cancelled', claim_owner_agent_id=NULL,
				 claim_version=claim_version+1, claim_claimed_at=NULL, claim_expires_at=NULL,
				 version=version+1, updated_at=?
			 WHERE id=? AND version=? AND status NOT IN ('completed','cancelled')`,
			timeParam(cancelledAt), todoID, expectedVersion)
	} else {
		res, err = r.store.execStmt(ctx, r.store.exec(ctx),
			`UPDATE goal_todos SET status='cancelled', version=version+1, updated_at=?
			 WHERE id=? AND version=? AND status NOT IN ('completed','cancelled')`,
			timeParam(cancelledAt), todoID, expectedVersion)
	}
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, domain.ErrVersionConflict
	}
	return r.Get(ctx, todoID)
}

func (r *TurnReceiptRepo) scanHeader(row interface{ Scan(...any) error }, header *domain.TurnReceiptHeader) error {
	var created scanTime
	var sourceRunID, planClientKey, decisionDigest *string
	if err := row.Scan(&header.TurnKey.GoalID, &header.TurnKey.TodoID, &header.TurnKey.TurnSeq,
		&header.Attempt, &header.SchemaVersion, &header.InputSnapshotDigest,
		&header.AdmissionClientKey, &sourceRunID, &planClientKey, &decisionDigest,
		&header.CanonicalDigest, &created); err != nil {
		return err
	}
	if sourceRunID != nil {
		header.GovernedSourceRunID = *sourceRunID
	}
	if planClientKey != nil {
		header.PlanClientKey = *planClientKey
	}
	if decisionDigest != nil {
		header.DecisionDigest = *decisionDigest
	}
	header.CreatedAt = mustTime(created)
	return nil
}

func (r *TurnReceiptRepo) headerByIdentity(ctx context.Context, key domain.TurnKey) (*domain.TurnReceiptHeader, error) {
	header := &domain.TurnReceiptHeader{}
	if err := r.scanHeader(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+headerCols+` FROM turn_receipt_headers WHERE goal_id=? AND todo_id=? AND turn_seq=?`,
		key.GoalID, key.TodoID, key.TurnSeq), header); err != nil {
		return nil, r.store.mapErr(err)
	}
	return header, nil
}

func (r *TurnReceiptRepo) headerByClientKey(ctx context.Context, key domain.TurnKey, clientKey string) (*domain.TurnReceiptHeader, error) {
	header := &domain.TurnReceiptHeader{}
	if err := r.scanHeader(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+headerCols+` FROM turn_receipt_headers
		 WHERE goal_id=? AND todo_id=? AND admission_client_key=?`,
		key.GoalID, key.TodoID, clientKey), header); err != nil {
		return nil, r.store.mapErr(err)
	}
	return header, nil
}

// Admit atomically allocates Todo.last_turn_seq and writes the immutable
// receipt header. The caller supplies the next positive sequence; the Todo
// version CAS is the authority that confirms it. Replay checks intentionally
// precede the Todo CAS so retries never require a live claim/version.
func (r *TurnReceiptRepo) Admit(ctx context.Context, header *domain.TurnReceiptHeader, ownerAgentID string, expectedTodoVersion int) (*domain.TurnReceiptHeader, error) {
	if header == nil {
		return nil, fmt.Errorf("%w: receipt header required", domain.ErrValidation)
	}
	working := *header
	inputSeq := working.TurnKey.TurnSeq
	if err := working.Validate(); err != nil {
		return nil, err
	}
	if err := governancekernel.VerifyHeaderDigest(&working); err != nil {
		return nil, err
	}
	if ownerAgentID == "" {
		return nil, fmt.Errorf("%w: admission owner required", domain.ErrValidation)
	}

	var committed *domain.TurnReceiptHeader
	err := r.store.InTx(ctx, func(txctx context.Context) error {
		// Client-key replay is deliberately the first database check.
		if existing, err := r.headerByClientKey(txctx, working.TurnKey, working.AdmissionClientKey); err == nil {
			if existing.CanonicalDigest != working.CanonicalDigest {
				return domain.ErrIdempotencyConflict
			}
			committed = existing
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if inputSeq > 0 {
			if existing, err := r.headerByIdentity(txctx, working.TurnKey); err == nil {
				if existing.CanonicalDigest != working.CanonicalDigest {
					return domain.ErrIdempotencyConflict
				}
				committed = existing
				return nil
			} else if !errors.Is(err, domain.ErrNotFound) {
				return err
			}
		}

		goal, err := r.store.goals.Get(txctx, working.TurnKey.GoalID)
		if err != nil {
			return err
		}
		if goal.Status != domain.GoalActive {
			return domain.ErrStateConflict
		}
		todo, err := r.store.todos.Get(txctx, working.TurnKey.TodoID)
		if err != nil {
			return err
		}
		if err := r.store.todos.validateTodoScope(txctx, todo); err != nil {
			return err
		}
		if todo.GoalID != working.TurnKey.GoalID {
			return fmt.Errorf("%w: Todo does not belong to Goal", domain.ErrValidation)
		}
		if todo.Version != expectedTodoVersion {
			return domain.ErrVersionConflict
		}
		if todo.Status != domain.TodoClaimed || todo.Claim == nil || todo.Claim.OwnerAgentID != ownerAgentID {
			return domain.ErrStateConflict
		}
		if !todoAgentInScope(todo, ownerAgentID) {
			return domain.ErrStateConflict
		}
		admissionNow := timeNow()
		if !todo.Claim.ExpiresAt.After(admissionNow) || !todo.Claim.ExpiresAt.After(working.CreatedAt) {
			return domain.ErrStateConflict
		}
		nextSeq := todo.LastTurnSeq + 1
		if inputSeq != nextSeq {
			return domain.ErrVersionConflict
		}
		res, err := r.store.execStmt(txctx, r.store.exec(txctx),
			`UPDATE goal_todos
			 SET status='running', last_turn_seq=last_turn_seq+1,
				 version=version+1, updated_at=?
			 WHERE id=? AND goal_id=? AND version=? AND status='claimed'
			   AND claim_owner_agent_id=? AND claim_expires_at>?`,
			timeParam(working.CreatedAt), todo.ID, todo.GoalID, expectedTodoVersion,
			ownerAgentID, timeParam(admissionNow))
		if err != nil {
			return r.store.mapErr(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return domain.ErrVersionConflict
		}
		working.TurnKey.TurnSeq = nextSeq
		_, err = r.store.execStmt(txctx, r.store.exec(txctx),
			`INSERT INTO turn_receipt_headers(`+headerCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			working.TurnKey.GoalID, working.TurnKey.TodoID, working.TurnKey.TurnSeq,
			working.Attempt, working.SchemaVersion, working.InputSnapshotDigest,
			working.AdmissionClientKey, nullString(working.GovernedSourceRunID),
			nullString(working.PlanClientKey), nullString(working.DecisionDigest),
			working.CanonicalDigest, timeParam(working.CreatedAt))
		if err != nil {
			return r.store.mapErr(err)
		}
		stored, err := r.headerByIdentity(txctx, working.TurnKey)
		if err != nil {
			return err
		}
		committed = stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	*header = *committed
	return header, nil
}

func (r *TurnReceiptRepo) GetHeader(ctx context.Context, key domain.TurnKey) (*domain.TurnReceiptHeader, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	return r.headerByIdentity(ctx, key)
}

// ListHeadersByGoal enumerates immutable receipt headers in canonical turn
// order for projection replay. Digest verification remains an application
// concern so repair can fail closed on a forged payload.
func (r *TurnReceiptRepo) ListHeadersByGoal(ctx context.Context, goalID string) ([]*domain.TurnReceiptHeader, error) {
	if strings.TrimSpace(goalID) == "" {
		return nil, fmt.Errorf("%w: goal id required", domain.ErrValidation)
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+headerCols+` FROM turn_receipt_headers WHERE goal_id=? ORDER BY todo_id, turn_seq`, goalID)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.TurnReceiptHeader
	for rows.Next() {
		header := &domain.TurnReceiptHeader{}
		if err := r.scanHeader(rows, header); err != nil {
			return nil, err
		}
		out = append(out, header)
	}
	return out, rows.Err()
}

// GetHeaderByClientKey exposes the admission idempotency lookup used by the
// application replay path. Client keys are scoped to one Goal/Todo pair and
// do not need a TurnSeq to be resolved.
func (r *TurnReceiptRepo) GetHeaderByClientKey(ctx context.Context, goalID, todoID, clientKey string) (*domain.TurnReceiptHeader, error) {
	if strings.TrimSpace(goalID) == "" || strings.TrimSpace(todoID) == "" || strings.TrimSpace(clientKey) == "" {
		return nil, fmt.Errorf("%w: goal, todo and client key are required", domain.ErrValidation)
	}
	return r.headerByClientKey(ctx, domain.TurnKey{GoalID: goalID, TodoID: todoID}, clientKey)
}

// GetHeaderBySourceRun resolves the governed admission checkpoint without
// parsing the legacy admission_client_key. A source Run can own at most one
// governed turn; migration 0030 enforces the partial unique index.
func (r *TurnReceiptRepo) GetHeaderBySourceRun(ctx context.Context, sourceRunID string) (*domain.TurnReceiptHeader, error) {
	if !strings.HasPrefix(sourceRunID, domain.PrefixRun) || strings.TrimSpace(sourceRunID) != sourceRunID {
		return nil, fmt.Errorf("%w: source run id required", domain.ErrValidation)
	}
	header := &domain.TurnReceiptHeader{}
	if err := r.scanHeader(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+headerCols+` FROM turn_receipt_headers WHERE source_run_id=?`, sourceRunID), header); err != nil {
		return nil, r.store.mapErr(err)
	}
	return header, nil
}

func (r *TurnReceiptRepo) scanPhase(row interface{ Scan(...any) error }, phase *domain.TurnReceiptPhase) error {
	var payload, runIDs, reservationKeys, evidence string
	var planID *string
	var created scanTime
	if err := row.Scan(&phase.TurnKey.GoalID, &phase.TurnKey.TodoID, &phase.TurnKey.TurnSeq,
		&phase.PhaseSeq, &phase.Phase, &payload, &phase.CanonicalDigest, &planID,
		&runIDs, &reservationKeys, &evidence, &created); err != nil {
		return err
	}
	phase.CreatedAt = mustTime(created)
	if planID != nil {
		phase.PlanID = *planID
	}
	if err := jsonInto(payload, &phase.Payload); err != nil {
		return err
	}
	if phase.Payload == nil {
		phase.Payload = map[string]any{}
	}
	if err := jsonInto(runIDs, &phase.RunIDs); err != nil {
		return err
	}
	if phase.RunIDs == nil {
		phase.RunIDs = []string{}
	}
	if err := jsonInto(reservationKeys, &phase.QuotaReservationKeys); err != nil {
		return err
	}
	if phase.QuotaReservationKeys == nil {
		phase.QuotaReservationKeys = []string{}
	}
	if err := jsonInto(evidence, &phase.Evidence); err != nil {
		return err
	}
	if phase.Evidence == nil {
		phase.Evidence = []domain.GovernanceEvidenceItem{}
	}
	return nil
}

func (r *TurnReceiptRepo) phaseByIdentity(ctx context.Context, key domain.TurnKey, phaseSeq int) (*domain.TurnReceiptPhase, error) {
	phase := &domain.TurnReceiptPhase{}
	if err := r.scanPhase(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+phaseCols+` FROM turn_receipt_phases
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? AND phase_seq=?`,
		key.GoalID, key.TodoID, key.TurnSeq, phaseSeq), phase); err != nil {
		return nil, r.store.mapErr(err)
	}
	return phase, nil
}

func (r *TurnReceiptRepo) GetPhase(ctx context.Context, key domain.TurnKey, phaseSeq int) (*domain.TurnReceiptPhase, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if _, ok := domain.TurnReceiptPhaseNameForSeq(phaseSeq); !ok {
		return nil, fmt.Errorf("%w: receipt phase sequence must be in 1..7", domain.ErrValidation)
	}
	return r.phaseByIdentity(ctx, key, phaseSeq)
}

// AppendPhase writes one immutable settlement phase. PhaseSeq is part of the
// canonical identity and must use the fixed one-based contract sequence.
// SQLite triggers enforce contiguous order and reject mutation/deletion below
// this API.
func (r *TurnReceiptRepo) AppendPhase(ctx context.Context, phase *domain.TurnReceiptPhase) (*domain.TurnReceiptPhase, error) {
	if phase == nil {
		return nil, fmt.Errorf("%w: receipt phase required", domain.ErrValidation)
	}
	working := *phase
	if err := working.Validate(); err != nil {
		return nil, err
	}
	if err := governancekernel.VerifyPhaseDigest(&working); err != nil {
		return nil, err
	}
	var stored *domain.TurnReceiptPhase
	err := r.store.InTx(ctx, func(txctx context.Context) error {
		if _, err := r.headerByIdentity(txctx, working.TurnKey); err != nil {
			return err
		}
		if existing, err := r.phaseByIdentity(txctx, working.TurnKey, working.PhaseSeq); err == nil {
			if existing.CanonicalDigest != working.CanonicalDigest {
				return domain.ErrIdempotencyConflict
			}
			stored = existing
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err := r.validatePhaseReferences(txctx, &working); err != nil {
			return err
		}
		_, err := r.store.execStmt(txctx, r.store.exec(txctx),
			`INSERT INTO turn_receipt_phases(`+phaseCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			working.TurnKey.GoalID, working.TurnKey.TodoID, working.TurnKey.TurnSeq,
			working.PhaseSeq, working.Phase, jsonText(working.Payload), working.CanonicalDigest,
			nullString(working.PlanID), jsonText(nonNilStrings(working.RunIDs)),
			jsonText(nonNilStrings(working.QuotaReservationKeys)), jsonText(nonNilEvidence(working.Evidence)),
			timeParam(working.CreatedAt))
		if err != nil {
			return mapGovernanceConstraint(r.store.mapErr(err))
		}
		stored, err = r.phaseByIdentity(txctx, working.TurnKey, working.PhaseSeq)
		return err
	})
	if err != nil {
		return nil, err
	}
	*phase = *stored
	return phase, nil
}

func (r *TurnReceiptRepo) validatePhaseReferences(ctx context.Context, phase *domain.TurnReceiptPhase) error {
	todo, err := r.store.todos.Get(ctx, phase.TurnKey.TodoID)
	if err != nil {
		return err
	}
	if todo.GoalID != phase.TurnKey.GoalID {
		return fmt.Errorf("%w: receipt phase Todo is outside Goal", domain.ErrValidation)
	}
	goal, err := r.store.goals.Get(ctx, phase.TurnKey.GoalID)
	if err != nil {
		return err
	}
	var phasePlan *domain.Plan
	if phase.PlanID != "" {
		plan, getErr := r.store.plans.Get(ctx, phase.PlanID)
		if getErr != nil {
			return getErr
		}
		if plan.WorkspaceID != goal.WorkspaceID || !todoWorkItemInScope(todo, plan.WorkItemID) {
			return fmt.Errorf("%w: receipt Plan is outside Goal/Todo scope", domain.ErrValidation)
		}
		if (phase.Phase == domain.TurnReceiptPhasePlanCompile || phase.Phase == domain.TurnReceiptPhaseDispatch) &&
			(plan.GovernanceTurnKey == nil || !plan.GovernanceTurnKey.Equal(phase.TurnKey)) {
			return fmt.Errorf("%w: receipt Plan governance identity differs from TurnKey", domain.ErrValidation)
		}
		phasePlan = plan
	}
	for _, runID := range phase.RunIDs {
		run, getErr := r.store.runs.Get(ctx, runID)
		if getErr != nil {
			return getErr
		}
		inScope := todoWorkItemInScope(todo, run.WorkItemID) || planOwnsRun(phasePlan, run.ID)
		if run.WorkspaceID != goal.WorkspaceID || !inScope {
			return fmt.Errorf("%w: receipt Run is outside Goal/Todo scope", domain.ErrValidation)
		}
	}
	for _, evidence := range phase.Evidence {
		if err := r.validateEvidenceReference(ctx, goal.WorkspaceID, todo, evidence); err != nil {
			return err
		}
	}
	for i, reference := range phase.QuotaReservationKeys {
		key, err := parseQuotaReservationReference(reference)
		if err != nil {
			return fmt.Errorf("%w: receipt quota reservation reference %d: %v", domain.ErrValidation, i, err)
		}
		if !key.TurnKey.Equal(phase.TurnKey) {
			return fmt.Errorf("%w: receipt quota reservation %q belongs to another Turn", domain.ErrValidation, reference)
		}
		reservation, getErr := r.store.quotas.Get(ctx, key)
		if getErr != nil {
			if errors.Is(getErr, domain.ErrNotFound) {
				return fmt.Errorf("%w: receipt quota reservation %q does not exist", domain.ErrValidation, reference)
			}
			return getErr
		}
		if reservation == nil || !reservation.Key.Equal(key) {
			return fmt.Errorf("%w: receipt quota reservation %q identity mismatch", domain.ErrValidation, reference)
		}
	}
	return nil
}

func parseQuotaReservationReference(reference string) (domain.QuotaReservationKey, error) {
	parts := strings.Split(reference, ":")
	if len(parts) != 4 {
		return domain.QuotaReservationKey{}, fmt.Errorf("reference must be goal_id:todo_id:turn_seq:quota_kind")
	}
	turnSeq, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return domain.QuotaReservationKey{}, fmt.Errorf("turn_seq is invalid: %w", err)
	}
	key := domain.QuotaReservationKey{
		TurnKey: domain.TurnKey{GoalID: parts[0], TodoID: parts[1], TurnSeq: turnSeq},
		Kind:    domain.QuotaKind(parts[3]),
	}
	if err := key.Validate(); err != nil {
		return domain.QuotaReservationKey{}, err
	}
	return key, nil
}

func planOwnsRun(plan *domain.Plan, runID string) bool {
	if plan == nil || runID == "" {
		return false
	}
	for _, step := range plan.Steps {
		if step.ResultRunID == runID {
			return true
		}
	}
	return false
}

func todoWorkItemInScope(todo *domain.Todo, workItemID string) bool {
	if todo == nil {
		return false
	}
	for _, allowed := range todo.DecisionScope.WorkItemIDs {
		if allowed == workItemID {
			return true
		}
	}
	return false
}

func (r *TurnReceiptRepo) validateEvidenceReference(ctx context.Context, workspaceID string, todo *domain.Todo, evidence domain.GovernanceEvidenceItem) error {
	sameScope := func(actualWorkspace, workItemID, kind string) error {
		if actualWorkspace != workspaceID || !todoWorkItemInScope(todo, workItemID) {
			return fmt.Errorf("%w: receipt %s evidence is outside Goal/Todo scope", domain.ErrValidation, kind)
		}
		return nil
	}
	switch evidence.SourceKind {
	case domain.EvidenceSourceWorkItem:
		item, err := r.store.workItems.Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		return sameScope(item.WorkspaceID, item.ID, string(evidence.SourceKind))
	case domain.EvidenceSourcePlan:
		plan, err := r.store.plans.Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		return sameScope(plan.WorkspaceID, plan.WorkItemID, string(evidence.SourceKind))
	case domain.EvidenceSourceRun:
		run, err := r.store.runs.Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		return sameScope(run.WorkspaceID, run.WorkItemID, string(evidence.SourceKind))
	case domain.EvidenceSourceArtifact:
		var actualWorkspace, workItemID string
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT run.workspace_id, run.work_item_id FROM artifacts artifact
			 JOIN execution_runs run ON run.id=artifact.run_id
			 WHERE artifact.id=?`, evidence.SourceID).Scan(&actualWorkspace, &workItemID); err != nil {
			return r.store.mapErr(err)
		}
		return sameScope(actualWorkspace, workItemID, string(evidence.SourceKind))
	case domain.EvidenceSourceApproval:
		var actualWorkspace, workItemID string
		if err := r.store.queryRow(ctx, r.store.exec(ctx),
			`SELECT run.workspace_id, run.work_item_id FROM approvals approval
			 JOIN execution_runs run ON run.id=approval.run_id
			 WHERE approval.id=?`, evidence.SourceID).Scan(&actualWorkspace, &workItemID); err != nil {
			return r.store.mapErr(err)
		}
		return sameScope(actualWorkspace, workItemID, string(evidence.SourceKind))
	case domain.EvidenceSourceValidationResult:
		return fmt.Errorf("%w: validation_result evidence has no WP1 canonical source", domain.ErrValidation)
	case domain.EvidenceSourceDeliveryBrief:
		snapshot, err := r.store.deliveryBriefSnapshots.Get(ctx, evidence.SourceID)
		if err != nil {
			return err
		}
		if snapshot.GoalID != todo.GoalID || snapshot.TodoID != todo.ID {
			return fmt.Errorf("%w: receipt delivery_brief evidence Goal/Todo mismatch", domain.ErrValidation)
		}
		if !todoWorkItemInScope(todo, snapshot.WorkItemID) {
			return fmt.Errorf("%w: receipt delivery_brief evidence is outside Goal/Todo scope", domain.ErrValidation)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported receipt evidence kind %q", domain.ErrValidation, evidence.SourceKind)
	}
}

func (r *TurnReceiptRepo) ListPhases(ctx context.Context, key domain.TurnKey) ([]*domain.TurnReceiptPhase, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+phaseCols+` FROM turn_receipt_phases
		 WHERE goal_id=? AND todo_id=? AND turn_seq=? ORDER BY phase_seq`,
		key.GoalID, key.TodoID, key.TurnSeq)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	defer rows.Close()
	var out []*domain.TurnReceiptPhase
	for rows.Next() {
		phase := &domain.TurnReceiptPhase{}
		if err := r.scanPhase(rows, phase); err != nil {
			return nil, err
		}
		out = append(out, phase)
	}
	return out, rows.Err()
}

// LatestTurnHeaderByRunID 用 turn_receipt_phases.run_ids JSON 数组反查 run
// 所属治理回合：任一 phase 行的 run_ids 含 runID 即该 turn 的成员 run；
// 多 turn 命中取 created_at 最新（并列按 goal/todo/turn_seq 降序，保证确定性）。
// 无命中返回 (nil, nil)——journal 调试面对无治理引用的 run 下发 governance=null。
func (r *TurnReceiptRepo) LatestTurnHeaderByRunID(ctx context.Context, runID string) (*domain.TurnReceiptHeader, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("%w: run id required", domain.ErrValidation)
	}
	header := &domain.TurnReceiptHeader{}
	err := r.scanHeader(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+headerCols+` FROM turn_receipt_headers h
		 WHERE EXISTS (
		     SELECT 1 FROM turn_receipt_phases p, json_each(p.run_ids)
		     WHERE p.goal_id = h.goal_id AND p.todo_id = h.todo_id AND p.turn_seq = h.turn_seq
		       AND json_each.value = ?
		 )
		 ORDER BY h.created_at DESC, h.goal_id DESC, h.todo_id DESC, h.turn_seq DESC
		 LIMIT 1`, runID), header)
	if err != nil {
		if err = r.store.mapErr(err); errors.Is(err, domain.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return header, nil
}

func mapGovernanceConstraint(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "turn receipt phase") || strings.Contains(message, "turn_receipt_phases") {
		return fmt.Errorf("%w: %v", domain.ErrValidation, err)
	}
	return err
}
