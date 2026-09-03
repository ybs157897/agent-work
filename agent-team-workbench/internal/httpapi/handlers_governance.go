package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Governance HTTP handlers expose the already-owned Goal/Todo/Receipt/Quota,
// Handoff, Evidence and projection services. They never mutate governance
// state through a repository directly.

const governanceClaimTTL = 15 * time.Minute

type goalCommandRequest struct {
	ExpectedVersion int `json:"expected_version"`
}

type todoClaimRequest struct {
	OwnerAgentID    string `json:"owner_agent_id"`
	ExpectedVersion int    `json:"expected_version"`
	ExpiresAt       string `json:"expires_at,omitempty"`
}

type todoReleaseRequest struct {
	OwnerAgentID    string `json:"owner_agent_id"`
	ExpectedVersion int    `json:"expected_version"`
}

type todoUserActionRequest struct {
	Resolution      string `json:"resolution"`
	ActorID         string `json:"actor_id"`
	ExpectedVersion int    `json:"expected_version"`
	ClientKey       string `json:"client_key,omitempty"`
}

type handoffCreateRequest struct {
	Source         domain.GovernanceActorRef       `json:"source"`
	Target         domain.GovernanceActorRef       `json:"target"`
	Reason         string                          `json:"reason"`
	ContextSummary string                          `json:"context_summary"`
	Evidence       []domain.GovernanceEvidenceItem `json:"evidence"`
	OpenRisks      []string                        `json:"open_risks"`
	Actor          domain.GovernanceActorRef       `json:"actor"`
	ClientKey      string                          `json:"client_key,omitempty"`
}

type handoffAcceptRequest struct {
	Target     domain.GovernanceActorRef `json:"target"`
	Acceptance string                    `json:"acceptance"`
}

type handoffRejectRequest struct {
	Reason string `json:"reason"`
}

type projectionRepairRequest struct {
	Scope     []domain.GovernanceProjectionScope `json:"scope"`
	ClientKey string                             `json:"client_key,omitempty"`
}

type deliveryBriefSnapshotCreateRequest struct {
	WorkItemID string `json:"work_item_id,omitempty"`
	ClientKey  string `json:"client_key,omitempty"`
}

type quotaGapReconciliationRequest struct {
	Target    domain.QuotaSpendKey          `json:"target"`
	Amount    int64                         `json:"amount"`
	Evidence  domain.GovernanceEvidenceItem `json:"evidence"`
	ActorID   string                        `json:"actor_id"`
	Reason    string                        `json:"reason"`
	ClientKey string                        `json:"client_key,omitempty"`
}

func ifMatchVersion(r *http.Request) (int, bool, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, false, nil
	}
	// Accept the strong ETag form emitted by clients ("7") and the compact
	// numeric form used by existing in-process callers.
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "W/"))
	raw = strings.Trim(raw, "\"")
	version, err := strconv.Atoi(raw)
	if err != nil || version < 1 {
		return 0, false, fmt.Errorf("If-Match 必须为正整数版本")
	}
	return version, true, nil
}

type goalDTO struct {
	ID                        string                          `json:"id"`
	WorkspaceID               string                          `json:"workspace_id"`
	RootWorkItemID            string                          `json:"root_work_item_id"`
	Objective                 string                          `json:"objective"`
	AcceptanceContract        []string                        `json:"acceptance_contract"`
	Status                    domain.GoalStatus               `json:"status"`
	Phase                     string                          `json:"phase"`
	CurrentTodoID             *string                         `json:"current_todo_id"`
	QuotaPolicies             []domain.QuotaPolicy            `json:"quota_policies"`
	CompletionEvidenceSummary []domain.GovernanceEvidenceItem `json:"completion_evidence_summary"`
	Version                   int                             `json:"version"`
	CreatedAt                 time.Time                       `json:"created_at"`
	UpdatedAt                 time.Time                       `json:"updated_at"`
}

type goalRecoveryCheckpointDTO struct {
	Kind           string `json:"kind"`
	RootWorkItemID string `json:"root_work_item_id"`
}

type goalCancellationAcceptedDTO struct {
	Goal               goalDTO                   `json:"goal"`
	RecoveryPending    bool                      `json:"recovery_pending"`
	RecoveryCheckpoint goalRecoveryCheckpointDTO `json:"recovery_checkpoint"`
}

func toGoalDTO(goal *domain.Goal) goalDTO {
	var currentTodoID *string
	if goal != nil && goal.CurrentTodoID != "" {
		value := goal.CurrentTodoID
		currentTodoID = &value
	}
	if goal == nil {
		return goalDTO{}
	}
	return goalDTO{
		ID: goal.ID, WorkspaceID: goal.WorkspaceID, RootWorkItemID: goal.RootWorkItemID,
		Objective: goal.Objective, AcceptanceContract: append([]string{}, goal.AcceptanceContract...),
		Status: goal.Status, Phase: goal.Phase, CurrentTodoID: currentTodoID,
		QuotaPolicies:             append([]domain.QuotaPolicy{}, goal.QuotaPolicies...),
		CompletionEvidenceSummary: append([]domain.GovernanceEvidenceItem{}, goal.CompletionEvidenceSummary...),
		Version:                   goal.Version, CreatedAt: goal.CreatedAt, UpdatedAt: goal.UpdatedAt,
	}
}

type todoClaimDTO struct {
	OwnerAgentID string    `json:"owner_agent_id"`
	Version      int       `json:"version"`
	ClaimedAt    time.Time `json:"claimed_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type todoDTO struct {
	ID                   string               `json:"id"`
	GoalID               string               `json:"goal_id"`
	Class                domain.TodoClass     `json:"class"`
	Status               domain.TodoStatus    `json:"status"`
	Instruction          string               `json:"instruction"`
	Acceptance           []string             `json:"acceptance"`
	ResumeCondition      *string              `json:"resume_condition"`
	Priority             domain.Priority      `json:"priority"`
	Predecessors         []string             `json:"predecessors"`
	Successors           []string             `json:"successors"`
	DecisionScope        domain.DecisionScope `json:"decision_scope"`
	Claim                *todoClaimDTO        `json:"claim"`
	ClaimVersion         int                  `json:"claim_version"`
	LastTurnSeq          int64                `json:"last_turn_seq"`
	CompletionTurnKey    *domain.TurnKey      `json:"completion_turn_key"`
	CompletionEvidenceID *string              `json:"completion_evidence_id"`
	Version              int                  `json:"version"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

func toTodoDTO(todo *domain.Todo) todoDTO {
	if todo == nil {
		return todoDTO{}
	}
	var resume *string
	if todo.ResumeCondition != "" {
		value := todo.ResumeCondition
		resume = &value
	}
	var claim *todoClaimDTO
	if todo.Claim != nil {
		claim = &todoClaimDTO{
			OwnerAgentID: todo.Claim.OwnerAgentID, Version: todo.Claim.Version,
			ClaimedAt: todo.Claim.ClaimedAt, ExpiresAt: todo.Claim.ExpiresAt,
		}
	}
	var completionTurnKey *domain.TurnKey
	if todo.CompletionTurnKey != nil {
		value := *todo.CompletionTurnKey
		completionTurnKey = &value
	}
	var completionEvidenceID *string
	if todo.CompletionEvidenceID != "" {
		value := todo.CompletionEvidenceID
		completionEvidenceID = &value
	}
	scope := todo.DecisionScope
	scope.WorkItemIDs = append([]string{}, scope.WorkItemIDs...)
	scope.AgentIDs = append([]string{}, scope.AgentIDs...)
	scope.RuntimeCapabilities = append([]string{}, scope.RuntimeCapabilities...)
	scope.WriteScopes = append([]string{}, scope.WriteScopes...)
	return todoDTO{
		ID: todo.ID, GoalID: todo.GoalID, Class: todo.Class, Status: todo.Status,
		Instruction: todo.Instruction, Acceptance: append([]string{}, todo.Acceptance...),
		ResumeCondition: resume, Priority: todo.Priority,
		Predecessors: append([]string{}, todo.Predecessors...), Successors: append([]string{}, todo.Successors...),
		DecisionScope: scope, Claim: claim, ClaimVersion: todo.ClaimVersion,
		LastTurnSeq: todo.LastTurnSeq, CompletionTurnKey: completionTurnKey,
		CompletionEvidenceID: completionEvidenceID,
		Version:              todo.Version, CreatedAt: todo.CreatedAt, UpdatedAt: todo.UpdatedAt,
	}
}

type turnReceiptDTO struct {
	Header domain.TurnReceiptHeader  `json:"header"`
	Phases []domain.TurnReceiptPhase `json:"phases"`
}

type quotaReservationDTO struct {
	GoalID            string                        `json:"goal_id"`
	TodoID            string                        `json:"todo_id"`
	TurnSeq           int64                         `json:"turn_seq"`
	QuotaKind         domain.QuotaKind              `json:"quota_kind"`
	Status            domain.QuotaReservationStatus `json:"status"`
	ReservedAmount    int64                         `json:"reserved_amount"`
	CommittedAmount   int64                         `json:"committed_amount"`
	ReleasedAmount    int64                         `json:"released_amount"`
	PolicyLimit       int64                         `json:"policy_limit"`
	PolicyEnforcement domain.QuotaEnforcement       `json:"policy_enforcement"`
	PolicyDigest      string                        `json:"policy_digest"`
	Version           int                           `json:"version"`
	CreatedAt         time.Time                     `json:"created_at"`
	UpdatedAt         time.Time                     `json:"updated_at"`
}

func toQuotaReservationDTO(reservation *domain.QuotaReservation) quotaReservationDTO {
	if reservation == nil {
		return quotaReservationDTO{}
	}
	return quotaReservationDTO{
		GoalID: reservation.Key.TurnKey.GoalID, TodoID: reservation.Key.TurnKey.TodoID,
		TurnSeq: reservation.Key.TurnKey.TurnSeq, QuotaKind: reservation.Key.Kind,
		Status: reservation.Status, ReservedAmount: reservation.ReservedAmount,
		CommittedAmount: reservation.CommittedAmount, ReleasedAmount: reservation.ReleasedAmount,
		PolicyLimit: reservation.PolicyLimit, PolicyEnforcement: reservation.PolicyEnforcement,
		PolicyDigest: reservation.PolicyDigest, Version: reservation.Version,
		CreatedAt: reservation.CreatedAt, UpdatedAt: reservation.UpdatedAt,
	}
}

type quotaSpendDTO struct {
	GoalID       string                  `json:"goal_id"`
	TodoID       string                  `json:"todo_id"`
	TurnSeq      int64                   `json:"turn_seq"`
	QuotaKind    domain.QuotaKind        `json:"quota_kind"`
	RunID        string                  `json:"run_id"`
	Amount       int64                   `json:"amount"`
	UsageBasis   string                  `json:"usage_basis"`
	UsageDigest  string                  `json:"usage_digest"`
	PolicyDigest string                  `json:"policy_digest"`
	PriceDigest  string                  `json:"price_digest,omitempty"`
	Status       domain.QuotaSpendStatus `json:"status"`
	Reason       string                  `json:"reason,omitempty"`
	CreatedAt    time.Time               `json:"created_at"`
}

func toQuotaSpendDTO(entry *domain.QuotaSpendEntry) quotaSpendDTO {
	if entry == nil {
		return quotaSpendDTO{}
	}
	return quotaSpendDTO{
		GoalID: entry.Key.TurnKey.GoalID, TodoID: entry.Key.TurnKey.TodoID,
		TurnSeq: entry.Key.TurnKey.TurnSeq, QuotaKind: entry.Key.Kind, RunID: entry.Key.RunID,
		Amount: entry.Amount, UsageBasis: entry.UsageBasis, UsageDigest: entry.UsageDigest,
		PolicyDigest: entry.PolicyDigest, PriceDigest: entry.PriceDigest,
		Status: entry.Status, Reason: entry.Reason, CreatedAt: entry.CreatedAt,
	}
}

type quotaTurnDTO struct {
	TurnKey      domain.TurnKey        `json:"turn_key"`
	Reservations []quotaReservationDTO `json:"reservations"`
	Spend        []quotaSpendDTO       `json:"spend"`
}

type quotaKindSummaryDTO struct {
	Kind           domain.QuotaKind `json:"kind"`
	Committed      int64            `json:"committed"`
	ActiveReserved int64            `json:"active_reserved"`
	ActiveWorker   *int             `json:"active_worker,omitempty"`
	Unresolved     []quotaSpendDTO  `json:"unresolved"`
}

type goalQuotaDTO struct {
	GoalID   string                `json:"goal_id"`
	Policies []domain.QuotaPolicy  `json:"policies"`
	Kinds    []quotaKindSummaryDTO `json:"kinds"`
}

func (s *Server) handleListGoals(w http.ResponseWriter, r *http.Request) {
	goals, err := s.svc.ListGoals(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]goalDTO, 0, len(goals))
	for _, goal := range goals {
		items = append(items, toGoalDTO(goal))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) goalForRequest(ctx context.Context, goalID, workspaceID string) (*domain.Goal, error) {
	goal, err := s.svc.GetGoal(ctx, goalID)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && goal.WorkspaceID != workspaceID {
		return nil, domain.ErrNotFound
	}
	return goal, nil
}

func (s *Server) handleGetGoal(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGoalDTO(goal))
}

func (s *Server) handleGetWorkItemGoal(w http.ResponseWriter, r *http.Request) {
	goal, err := s.svc.GetGoalForWorkItem(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if goal.WorkspaceID != r.PathValue("workspace_id") {
		fail(w, r, domain.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toGoalDTO(goal))
}

func (s *Server) goalCommand(w http.ResponseWriter, r *http.Request, command func(context.Context, string, int) (*domain.Goal, error)) {
	goalID := r.PathValue("goal_id")
	s.idempotent(w, r, goalID, func() (int, []byte) {
		if _, err := s.goalForRequest(r.Context(), goalID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req goalCommandRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if req.ExpectedVersion < 1 {
			return renderProblem(http.StatusBadRequest, "bad_request", "expected_version required", "expected_version 必须为正整数")
		}
		if headerVersion, ok, err := ifMatchVersion(r); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid If-Match", err.Error())
		} else if ok && headerVersion != req.ExpectedVersion {
			return problemBytes(domain.ErrVersionConflict)
		}
		goal, err := command(r.Context(), goalID, req.ExpectedVersion)
		if err != nil {
			var committed *application.CommittedWithRecoveryError
			if errors.As(err, &committed) && committed.Goal != nil {
				rootWorkItemID := committed.RootWorkItemID
				if rootWorkItemID == "" {
					rootWorkItemID = committed.Goal.RootWorkItemID
				}
				return renderJSON(w, r, http.StatusAccepted, goalCancellationAcceptedDTO{
					Goal:            toGoalDTO(committed.Goal),
					RecoveryPending: true,
					RecoveryCheckpoint: goalRecoveryCheckpointDTO{
						Kind:           committed.RecoveryKind,
						RootWorkItemID: rootWorkItemID,
					},
				})
			}
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toGoalDTO(goal))
	})
}

func (s *Server) handleStartGoal(w http.ResponseWriter, r *http.Request) {
	s.goalCommand(w, r, s.svc.StartGoal)
}

func (s *Server) handlePauseGoal(w http.ResponseWriter, r *http.Request) {
	s.goalCommand(w, r, s.svc.PauseGoal)
}

func (s *Server) handleResumeGoal(w http.ResponseWriter, r *http.Request) {
	s.goalCommand(w, r, s.svc.ResumeGoal)
}

func (s *Server) handleCancelGoal(w http.ResponseWriter, r *http.Request) {
	s.goalCommand(w, r, s.svc.CancelGoal)
}

func (s *Server) handleListGoalTodos(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	todos, err := s.svc.ListTodos(r.Context(), goal.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]todoDTO, 0, len(todos))
	for _, todo := range todos {
		items = append(items, toTodoDTO(todo))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) todoForRequest(ctx context.Context, todoID, workspaceID string) (*domain.Todo, *domain.Goal, error) {
	todo, err := s.svc.GetTodo(ctx, todoID)
	if err != nil {
		return nil, nil, err
	}
	goal, err := s.svc.GetGoal(ctx, todo.GoalID)
	if err != nil {
		return nil, nil, err
	}
	if workspaceID != "" && goal.WorkspaceID != workspaceID {
		return nil, nil, domain.ErrNotFound
	}
	return todo, goal, nil
}

func (s *Server) handleGetTodo(w http.ResponseWriter, r *http.Request) {
	todo, _, err := s.todoForRequest(r.Context(), r.PathValue("todo_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTodoDTO(todo))
}

func (s *Server) handleClaimTodo(w http.ResponseWriter, r *http.Request) {
	todoID := r.PathValue("todo_id")
	s.idempotent(w, r, todoID, func() (int, []byte) {
		if _, _, err := s.todoForRequest(r.Context(), todoID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req todoClaimRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if strings.TrimSpace(req.OwnerAgentID) == "" || req.ExpectedVersion < 1 {
			return renderProblem(http.StatusBadRequest, "bad_request", "owner_agent_id and expected_version required", "owner_agent_id 与 expected_version 必填")
		}
		expiresAt := time.Now().UTC().Add(governanceClaimTTL)
		if strings.TrimSpace(req.ExpiresAt) != "" {
			parsed, err := time.Parse(time.RFC3339Nano, req.ExpiresAt)
			if err != nil {
				return renderProblem(http.StatusBadRequest, "bad_request", "Invalid expires_at", "expires_at 必须为 RFC3339")
			}
			expiresAt = parsed.UTC()
		}
		if headerVersion, ok, err := ifMatchVersion(r); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid If-Match", err.Error())
		} else if ok && headerVersion != req.ExpectedVersion {
			return problemBytes(domain.ErrVersionConflict)
		}
		todo, err := s.svc.ClaimTodo(r.Context(), todoID, req.OwnerAgentID, req.ExpectedVersion, expiresAt)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toTodoDTO(todo))
	})
}

func (s *Server) handleReleaseTodo(w http.ResponseWriter, r *http.Request) {
	todoID := r.PathValue("todo_id")
	s.idempotent(w, r, todoID, func() (int, []byte) {
		if _, _, err := s.todoForRequest(r.Context(), todoID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req todoReleaseRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if strings.TrimSpace(req.OwnerAgentID) == "" || req.ExpectedVersion < 1 {
			return renderProblem(http.StatusBadRequest, "bad_request", "owner_agent_id and expected_version required", "owner_agent_id 与 expected_version 必填")
		}
		if headerVersion, ok, err := ifMatchVersion(r); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid If-Match", err.Error())
		} else if ok && headerVersion != req.ExpectedVersion {
			return problemBytes(domain.ErrVersionConflict)
		}
		todo, err := s.svc.ReleaseTodo(r.Context(), todoID, req.OwnerAgentID, req.ExpectedVersion)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toTodoDTO(todo))
	})
}

func (s *Server) handleResolveTodoUserAction(w http.ResponseWriter, r *http.Request) {
	todoID := r.PathValue("todo_id")
	s.idempotent(w, r, todoID, func() (int, []byte) {
		if _, _, err := s.todoForRequest(r.Context(), todoID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req todoUserActionRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if strings.TrimSpace(req.Resolution) == "" || strings.TrimSpace(req.ActorID) == "" {
			return renderProblem(http.StatusBadRequest, "bad_request", "actor_id and resolution required", "actor_id 与 resolution 必填")
		}
		resolved, err := s.svc.ResolveTodoUserAction(r.Context(), application.ResolveTodoUserActionParams{
			TodoID: todoID, Resolution: req.Resolution, ActorID: req.ActorID,
			ExpectedVersion: req.ExpectedVersion, ClientKey: req.ClientKey,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toTodoDTO(resolved))
	})
}

func parseTurnSeq(r *http.Request) (int64, error) {
	value, err := strconv.ParseInt(r.PathValue("turn_seq"), 10, 64)
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%w: turn_seq must be a positive integer", domain.ErrValidation)
	}
	return value, nil
}

func (s *Server) turnKeyForRequest(r *http.Request) (domain.TurnKey, error) {
	turnSeq, err := parseTurnSeq(r)
	if err != nil {
		return domain.TurnKey{}, err
	}
	goalID, todoID := r.PathValue("goal_id"), r.PathValue("todo_id")
	if _, err := s.goalForRequest(r.Context(), goalID, r.PathValue("workspace_id")); err != nil {
		return domain.TurnKey{}, err
	}
	todo, err := s.svc.GetTodo(r.Context(), todoID)
	if err != nil {
		return domain.TurnKey{}, err
	}
	if todo.GoalID != goalID {
		return domain.TurnKey{}, domain.ErrNotFound
	}
	return domain.TurnKey{GoalID: goalID, TodoID: todoID, TurnSeq: turnSeq}, nil
}

func (s *Server) handleGetTurnReceipt(w http.ResponseWriter, r *http.Request) {
	key, err := s.turnKeyForRequest(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	receipt, err := s.svc.GetTurnReceipt(r.Context(), key)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, turnReceiptDTO{Header: receipt.Header, Phases: receipt.Phases})
}

func (s *Server) handleGetGoalQuota(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	summary, err := s.svc.GetGoalQuotaSummary(r.Context(), goal.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	result := goalQuotaDTO{GoalID: summary.GoalID, Policies: append([]domain.QuotaPolicy{}, summary.Policies...), Kinds: []quotaKindSummaryDTO{}}
	for _, policy := range summary.Policies {
		item := quotaKindSummaryDTO{Kind: policy.Kind, Unresolved: []quotaSpendDTO{}}
		if policy.Kind == domain.QuotaActiveWorker {
			value := summary.ActiveWorkers
			item.ActiveWorker = &value
		} else {
			item.Committed = summary.Committed[policy.Kind]
			item.ActiveReserved = summary.ActiveReserved[policy.Kind]
			for _, entry := range summary.Unresolved {
				if entry != nil && entry.Key.Kind == policy.Kind {
					item.Unresolved = append(item.Unresolved, toQuotaSpendDTO(entry))
				}
			}
		}
		result.Kinds = append(result.Kinds, item)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListQuotaGapReconciliations(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items, err := s.svc.ListQuotaGapResolutions(r.Context(), goal.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if items == nil {
		items = []*domain.QuotaGapResolution{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleReconcileQuotaGap(w http.ResponseWriter, r *http.Request) {
	goalID := r.PathValue("goal_id")
	s.idempotent(w, r, goalID+":quota-reconciliation", func() (int, []byte) {
		if _, err := s.goalForRequest(r.Context(), goalID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req quotaGapReconciliationRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if req.Target.TurnKey.GoalID != goalID {
			return problemBytes(domain.ErrNotFound)
		}
		resolution, err := s.svc.ReconcileQuotaGap(r.Context(), application.ReconcileQuotaGapParams{
			Target: req.Target, Amount: req.Amount, Evidence: req.Evidence,
			ActorID: req.ActorID, Reason: req.Reason, ClientKey: req.ClientKey,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, resolution)
	})
}

func (s *Server) handleGetTurnQuota(w http.ResponseWriter, r *http.Request) {
	key, err := s.turnKeyForRequest(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	reservations, spend, err := s.svc.GetTurnQuota(r.Context(), key)
	if err != nil {
		fail(w, r, err)
		return
	}
	result := quotaTurnDTO{TurnKey: key, Reservations: []quotaReservationDTO{}, Spend: []quotaSpendDTO{}}
	for _, reservation := range reservations {
		result.Reservations = append(result.Reservations, toQuotaReservationDTO(reservation))
	}
	for _, entry := range spend {
		result.Spend = append(result.Spend, toQuotaSpendDTO(entry))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handoffForRequest(ctx context.Context, handoffID, workspaceID string) (*domain.Handoff, *domain.Goal, error) {
	handoff, err := s.svc.Handoff(ctx, handoffID)
	if err != nil {
		return nil, nil, err
	}
	goal, err := s.svc.GetGoal(ctx, handoff.GoalID)
	if err != nil {
		return nil, nil, err
	}
	if workspaceID != "" && goal.WorkspaceID != workspaceID {
		return nil, nil, domain.ErrNotFound
	}
	return handoff, goal, nil
}

func (s *Server) handleListGoalHandoffs(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	handoffs, err := s.svc.HandoffsByGoal(r.Context(), goal.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if handoffs == nil {
		handoffs = []*domain.Handoff{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": handoffs})
}

func (s *Server) handleListTodoHandoffs(w http.ResponseWriter, r *http.Request) {
	todo, goal, err := s.todoForRequest(r.Context(), r.PathValue("todo_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if goal == nil || todo.GoalID != goal.ID {
		fail(w, r, domain.ErrNotFound)
		return
	}
	handoffs, err := s.svc.HandoffsByTodo(r.Context(), todo.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if handoffs == nil {
		handoffs = []*domain.Handoff{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": handoffs})
}

func (s *Server) handleGetHandoff(w http.ResponseWriter, r *http.Request) {
	handoff, _, err := s.handoffForRequest(r.Context(), r.PathValue("handoff_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, handoff)
}

func (s *Server) handleCreateHandoff(w http.ResponseWriter, r *http.Request) {
	goalID, todoID, workspaceID := r.PathValue("goal_id"), r.PathValue("todo_id"), r.PathValue("workspace_id")
	s.idempotent(w, r, goalID+":"+todoID, func() (int, []byte) {
		goal, err := s.goalForRequest(r.Context(), goalID, workspaceID)
		if err != nil {
			return problemBytes(err)
		}
		todo, err := s.svc.GetTodo(r.Context(), todoID)
		if err != nil {
			return problemBytes(err)
		}
		if todo.GoalID != goal.ID {
			return problemBytes(domain.ErrNotFound)
		}
		var req handoffCreateRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		handoff, err := s.svc.CreateHandoff(r.Context(), application.CreateHandoffParams{
			GoalID: goal.ID, TodoID: todo.ID, Source: req.Source, Target: req.Target,
			Reason: req.Reason, ContextSummary: req.ContextSummary, Evidence: req.Evidence,
			OpenRisks: req.OpenRisks, Actor: req.Actor, ClientKey: req.ClientKey,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, handoff)
	})
}

func (s *Server) handleAcceptHandoff(w http.ResponseWriter, r *http.Request) {
	handoffID := r.PathValue("handoff_id")
	s.idempotent(w, r, handoffID, func() (int, []byte) {
		if _, _, err := s.handoffForRequest(r.Context(), handoffID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req handoffAcceptRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		handoff, err := s.svc.AcceptHandoff(r.Context(), handoffID, req.Target, req.Acceptance)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, handoff)
	})
}

func (s *Server) handleRejectHandoff(w http.ResponseWriter, r *http.Request) {
	handoffID := r.PathValue("handoff_id")
	s.idempotent(w, r, handoffID, func() (int, []byte) {
		if _, _, err := s.handoffForRequest(r.Context(), handoffID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req handoffRejectRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		handoff, err := s.svc.RejectHandoff(r.Context(), handoffID, req.Reason)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, handoff)
	})
}

func (s *Server) handleCancelHandoff(w http.ResponseWriter, r *http.Request) {
	handoffID := r.PathValue("handoff_id")
	s.idempotent(w, r, handoffID, func() (int, []byte) {
		if _, _, err := s.handoffForRequest(r.Context(), handoffID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		handoff, err := s.svc.CancelHandoff(r.Context(), handoffID)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, handoff)
	})
}

func (s *Server) handleGetGoalEvidence(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	evidence, err := s.svc.GetGoalEvidence(r.Context(), goal.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if evidence == nil {
		evidence = []domain.GovernanceEvidenceItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": evidence})
}

func (s *Server) handleCaptureDeliveryBriefSnapshot(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("workspace_id")
	goalID := r.PathValue("goal_id")
	todoID := r.PathValue("todo_id")
	s.idempotent(w, r, workspaceID+":"+goalID+":"+todoID, func() (int, []byte) {
		goal, err := s.goalForRequest(r.Context(), goalID, workspaceID)
		if err != nil {
			return problemBytes(err)
		}
		todo, todoGoal, err := s.todoForRequest(r.Context(), todoID, workspaceID)
		if err != nil {
			return problemBytes(err)
		}
		if todoGoal == nil || todo.GoalID != goal.ID {
			return problemBytes(domain.ErrNotFound)
		}
		var req deliveryBriefSnapshotCreateRequest
		if err := decodeBody(r, &req); err != nil && err != io.EOF {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		clientKey := req.ClientKey
		if clientKey == "" {
			clientKey = r.Header.Get("Idempotency-Key")
		}
		snapshot, err := s.svc.CaptureDeliveryBriefSnapshot(r.Context(), application.CaptureDeliveryBriefSnapshotParams{
			GoalID: goal.ID, TodoID: todo.ID, WorkItemID: req.WorkItemID, ClientKey: clientKey,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, snapshot)
	})
}

func (s *Server) handleGetDeliveryBriefSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.svc.GetDeliveryBriefSnapshot(r.Context(), r.PathValue("snapshot_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if _, err := s.goalForRequest(r.Context(), snapshot.GoalID, r.PathValue("workspace_id")); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleGetGovernanceProjection(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	projection, err := s.svc.GetGovernanceProjection(r.Context(), goal.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, projection)
}

func (s *Server) handleListProjectionRepairs(w http.ResponseWriter, r *http.Request) {
	goal, err := s.goalForRequest(r.Context(), r.PathValue("goal_id"), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	repairs, err := s.svc.GetProjectionRepairs(r.Context(), goal.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if repairs == nil {
		repairs = []*domain.ProjectionRepair{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": repairs})
}

func (s *Server) handleRepairGovernanceProjection(w http.ResponseWriter, r *http.Request) {
	goalID := r.PathValue("goal_id")
	s.idempotent(w, r, goalID, func() (int, []byte) {
		if _, err := s.goalForRequest(r.Context(), goalID, r.PathValue("workspace_id")); err != nil {
			return problemBytes(err)
		}
		var req projectionRepairRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		result, err := s.svc.RepairGoalProjection(r.Context(), goalID, req.Scope, req.ClientKey)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, result)
	})
}
