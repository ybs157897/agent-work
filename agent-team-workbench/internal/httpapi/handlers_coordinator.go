package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// The coordinator DTOs intentionally expose a read model, not the internal
// state table shape. In particular, the system prompt is represented only by
// its immutable version; prompt text never crosses the settings API.

type coordinatorAgentRefDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Role    string `json:"role,omitempty"`
	Runtime string `json:"runtime,omitempty"`
}

type coordinatorFailureDTO struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Retryable *bool  `json:"retryable,omitempty"`
}

type coordinatorAttemptDTO struct {
	ID           string                  `json:"id"`
	RunID        string                  `json:"run_id,omitempty"`
	Agent        *coordinatorAgentRefDTO `json:"agent,omitempty"`
	Status       string                  `json:"status"`
	Attempt      int                     `json:"attempt"`
	MaxAttempts  int                     `json:"max_attempts,omitempty"`
	RetryOf      string                  `json:"retry_of,omitempty"`
	StartedAt    string                  `json:"started_at"`
	FinishedAt   string                  `json:"finished_at,omitempty"`
	Failure      *coordinatorFailureDTO  `json:"failure,omitempty"`
	NextAction   string                  `json:"next_action,omitempty"`
	NextActionAt *string                 `json:"next_action_at,omitempty"`
}

type coordinatorTimelineDTO struct {
	ID           string                  `json:"id"`
	Stage        string                  `json:"stage"`
	Kind         string                  `json:"kind,omitempty"`
	Title        string                  `json:"title"`
	Message      string                  `json:"message,omitempty"`
	Status       string                  `json:"status,omitempty"`
	OccurredAt   string                  `json:"occurred_at"`
	Agent        *coordinatorAgentRefDTO `json:"agent,omitempty"`
	Attempt      int                     `json:"attempt,omitempty"`
	RunID        string                  `json:"run_id,omitempty"`
	RetryOf      string                  `json:"retry_of,omitempty"`
	Failure      *coordinatorFailureDTO  `json:"failure,omitempty"`
	NextActionAt *string                 `json:"next_action_at,omitempty"`
}

type coordinatorSnapshotDTO struct {
	RootWorkItemID  string                   `json:"root_work_item_id"`
	WorkItemID      string                   `json:"work_item_id"`
	Status          string                   `json:"status"`
	Stage           string                   `json:"stage,omitempty"`
	Progress        *float64                 `json:"progress,omitempty"`
	CompletedSteps  int                      `json:"completed_steps,omitempty"`
	TotalSteps      int                      `json:"total_steps,omitempty"`
	CurrentAgent    *coordinatorAgentRefDTO  `json:"current_agent,omitempty"`
	NextAction      string                   `json:"next_action,omitempty"`
	NextActionAt    *string                  `json:"next_action_at,omitempty"`
	Blocker         *blockerDTO              `json:"blocker,omitempty"`
	RuntimeLabel    string                   `json:"runtime_label,omitempty"`
	ModelRef        string                   `json:"model_ref,omitempty"`
	ReasoningEffort string                   `json:"reasoning_effort,omitempty"`
	PromptVersion   string                   `json:"prompt_version,omitempty"`
	PromptLocked    bool                     `json:"prompt_locked"`
	Attempts        []coordinatorAttemptDTO  `json:"attempts"`
	Timeline        []coordinatorTimelineDTO `json:"timeline"`
	UpdatedAt       string                   `json:"updated_at"`
	Version         int                      `json:"version"`
}

type coordinatorConfigDTO struct {
	WorkspaceID          string `json:"workspace_id"`
	Kind                 string `json:"kind"`
	RuntimeLabel         string `json:"runtime_label"`
	ModelRef             string `json:"model_ref"`
	ReasoningEffort      string `json:"reasoning_effort"`
	FallbackRuntimeLabel string `json:"fallback_runtime_label,omitempty"`
	FallbackModelRef     string `json:"fallback_model_ref,omitempty"`
	PromptVersion        string `json:"prompt_version"`
	PromptLocked         bool   `json:"prompt_locked"`
	InstructionsEditable bool   `json:"instructions_editable"`
	Version              int    `json:"version"`
}

type patchCoordinatorConfigRequest struct {
	RuntimeLabel         *string `json:"runtime_label"`
	ModelRef             *string `json:"model_ref"`
	ReasoningEffort      *string `json:"reasoning_effort"`
	FallbackRuntimeLabel *string `json:"fallback_runtime_label"`
	FallbackModelRef     *string `json:"fallback_model_ref"`
	ExpectedVersion      int     `json:"expected_version"`
	// These fields are deliberately accepted only to return a clear error,
	// rather than silently ignoring an attempted prompt mutation.
	Instructions json.RawMessage `json:"instructions"`
	Prompt       json.RawMessage `json:"prompt"`
}

func modelRefName(ref domain.ModelRef) string {
	if ref.Ref != "" {
		return ref.Ref
	}
	return ref.Model
}

func toCoordinatorConfigDTO(c *domain.TaskCoordinatorConfig) coordinatorConfigDTO {
	return coordinatorConfigDTO{
		WorkspaceID: c.WorkspaceID, Kind: string(domain.AgentProfileKindTaskCoordinator),
		RuntimeLabel: c.RuntimeLabel, ModelRef: modelRefName(c.ModelRef),
		ReasoningEffort:      c.EffectiveReasoningEffort(),
		FallbackRuntimeLabel: c.FallbackRuntimeLabel, FallbackModelRef: modelRefName(c.FallbackModelRef),
		PromptVersion: c.PromptVersion, PromptLocked: true, InstructionsEditable: false,
		Version: c.Version,
	}
}

func coordinatorAgentRef(a *domain.AgentProfile, runtime string) *coordinatorAgentRefDTO {
	if a == nil {
		return nil
	}
	return &coordinatorAgentRefDTO{ID: a.ID, Name: a.Name, Role: a.Role, Runtime: runtime}
}

func coordinatorTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	value := t.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	return &value
}

func coordinatorString(data map[string]any, key string) string {
	if value, ok := data[key].(string); ok {
		return value
	}
	return ""
}

func coordinatorInt(data map[string]any, key string) int {
	switch value := data[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	}
	return 0
}

// coordinatorAttemptNumbers derives the stable attempt number for each Run.
// A retry-scheduled event is emitted against the failed source Run, but its
// attempt field describes the successor Run (source=1, next=2). Keep that
// control-plane hint out of the source attempt projection.
func coordinatorAttemptNumbers(events []*domain.TaskCoordinatorEvent) map[string]int {
	numbers := make(map[string]int)
	for _, event := range events {
		if event == nil || event.RunID == "" {
			continue
		}
		attempt := event.Attempt
		if coordinatorString(event.Data, "retry_of") == event.RunID && attempt > 1 {
			attempt--
		}
		if attempt <= 0 {
			attempt = 1
		}
		if attempt > numbers[event.RunID] {
			numbers[event.RunID] = attempt
		}
	}
	return numbers
}

func coordinatorFloat(data map[string]any, key string) (*float64, bool) {
	switch value := data[key].(type) {
	case float64:
		return &value, true
	case float32:
		f := float64(value)
		return &f, true
	case int:
		f := float64(value)
		return &f, true
	case int64:
		f := float64(value)
		return &f, true
	default:
		return nil, false
	}
}

func coordinatorStage(kind string, data map[string]any) string {
	if stage := coordinatorString(data, "stage"); stage != "" {
		return stage
	}
	kind = strings.ToLower(kind)
	switch {
	case strings.Contains(kind, "queued"), strings.Contains(kind, "accepted"), strings.Contains(kind, "planning"):
		return "plan"
	case strings.Contains(kind, "plan"):
		return "plan"
	case strings.Contains(kind, "dispatch"):
		return "dispatch"
	case strings.Contains(kind, "failure"), strings.Contains(kind, "failed"), strings.Contains(kind, "error"):
		return "failure"
	case strings.Contains(kind, "blocked"), strings.Contains(kind, "waiting_user"):
		return "failure"
	case strings.Contains(kind, "retry"):
		return "retry"
	case strings.Contains(kind, "reassign"), strings.Contains(kind, "replace"):
		return "reassign"
	case strings.Contains(kind, "attempt"), strings.Contains(kind, "run"):
		return "attempt"
	case strings.Contains(kind, "result"), strings.Contains(kind, "summary"):
		return "result"
	case strings.Contains(kind, "accept"), strings.Contains(kind, "completed"):
		return "acceptance"
	default:
		return "attempt"
	}
}

func coordinatorFailure(data map[string]any) *coordinatorFailureDTO {
	code := coordinatorString(data, "failure_code")
	message := coordinatorString(data, "failure_message")
	if message == "" {
		message = coordinatorString(data, "error")
	}
	if message == "" && code == "" {
		return nil
	}
	var retryable *bool
	if value, ok := data["retryable"].(bool); ok {
		retryable = &value
	}
	return &coordinatorFailureDTO{Code: code, Message: message, Retryable: retryable}
}

func (s *Server) coordinatorReadModel(ctx context.Context, workItemID string) (coordinatorSnapshotDTO, []coordinatorTimelineDTO, error) {
	wi, err := s.store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		return coordinatorSnapshotDTO{}, nil, err
	}
	if err := requireTaskWorkItemHTTP(wi); err != nil {
		return coordinatorSnapshotDTO{}, nil, err
	}
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID)
	if err != nil {
		return coordinatorSnapshotDTO{}, nil, err
	}
	config, err := s.store.TaskCoordinators().GetConfig(ctx, wi.WorkspaceID)
	if err != nil {
		return coordinatorSnapshotDTO{}, nil, err
	}
	agents, err := s.store.Agents().List(ctx, wi.WorkspaceID)
	if err != nil {
		return coordinatorSnapshotDTO{}, nil, err
	}
	agentByID := make(map[string]*domain.AgentProfile, len(agents)+1)
	for _, agent := range agents {
		agentByID[agent.ID] = agent
	}
	if coordinator, err := s.store.Agents().Get(ctx, config.AgentProfileID); err == nil {
		agentByID[coordinator.ID] = coordinator
	}
	events, err := s.store.TaskCoordinators().ListEvents(ctx, workItemID, 500)
	if err != nil {
		return coordinatorSnapshotDTO{}, nil, err
	}
	timeline := make([]coordinatorTimelineDTO, 0, len(events))
	attempts := make([]coordinatorAttemptDTO, 0)
	attemptIndex := make(map[string]int)
	attemptNumbers := coordinatorAttemptNumbers(events)
	lastStage := state.Phase
	for _, event := range events {
		stage := coordinatorStage(event.Kind, event.Data)
		lastStage = stage
		status := coordinatorString(event.Data, "status")
		retryOf := coordinatorString(event.Data, "retry_of")
		failure := coordinatorFailure(event.Data)
		timeline = append(timeline, coordinatorTimelineDTO{
			ID: event.ID, Stage: stage, Kind: event.Kind,
			Title: event.Summary, Message: event.Reason, Status: status,
			OccurredAt: event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			Agent:      coordinatorAgentRef(agentByID[event.AgentID], coordinatorString(event.Data, "runtime")),
			Attempt:    event.Attempt, RunID: event.RunID, RetryOf: retryOf, Failure: failure,
			NextActionAt: coordinatorTime(event.NextActionAt),
		})
		// One attempt can emit started, failed and retry-scheduled events.
		// The attempt chain is strictly Run-backed; user pause/recovery and other
		// run-less control events belong only to the causal timeline.
		isAttemptEvent := event.RunID != ""
		if isAttemptEvent {
			runAttempt := attemptNumbers[event.RunID]
			if runAttempt <= 0 {
				runAttempt = event.Attempt
			}
			if runAttempt <= 0 {
				runAttempt = 1
			}
			attemptRetryOf := retryOf
			if attemptRetryOf == event.RunID {
				// This event schedules the next Run; it is not a self-retry edge.
				attemptRetryOf = ""
			}
			attemptStatus := status
			if attemptStatus == "" {
				switch stage {
				case "failure":
					attemptStatus = "failed"
				case "retry", "reassign":
					attemptStatus = "retrying"
				case "result", "acceptance":
					attemptStatus = "succeeded"
				default:
					attemptStatus = "running"
				}
			}
			key := event.RunID
			if key == "" {
				key = event.ID
			}
			idx, exists := attemptIndex[key]
			if !exists {
				attemptIndex[key] = len(attempts)
				attempts = append(attempts, coordinatorAttemptDTO{
					ID: event.ID, RunID: event.RunID,
					Agent:  coordinatorAgentRef(agentByID[event.AgentID], coordinatorString(event.Data, "runtime")),
					Status: attemptStatus, Attempt: runAttempt,
					MaxAttempts: coordinatorInt(event.Data, "max_attempts"), RetryOf: attemptRetryOf,
					StartedAt: event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
					Failure:   failure, NextAction: coordinatorString(event.Data, "next_action"),
					NextActionAt: coordinatorTime(event.NextActionAt),
				})
				if failure != nil {
					attempts[len(attempts)-1].FinishedAt = event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
				}
				continue
			}
			attempt := &attempts[idx]
			if event.AgentID != "" {
				attempt.Agent = coordinatorAgentRef(agentByID[event.AgentID], coordinatorString(event.Data, "runtime"))
			}
			if runAttempt > attempt.Attempt {
				attempt.Attempt = runAttempt
			}
			if max := coordinatorInt(event.Data, "max_attempts"); max > 0 {
				attempt.MaxAttempts = max
			}
			if attemptRetryOf != "" {
				attempt.RetryOf = attemptRetryOf
			}
			if next := coordinatorString(event.Data, "next_action"); next != "" {
				attempt.NextAction = next
			}
			if nextAt := coordinatorTime(event.NextActionAt); nextAt != nil {
				attempt.NextActionAt = nextAt
			}
			if failure != nil {
				attempt.Status, attempt.Failure = "failed", failure
				attempt.FinishedAt = event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			} else if stage == "retry" || stage == "reassign" {
				attempt.Status = attemptStatus
				attempt.FinishedAt = event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			} else if stage == "result" || stage == "acceptance" {
				attempt.Status = attemptStatus
				attempt.FinishedAt = event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
			} else if status != "" {
				attempt.Status = status
			}
		}
	}
	progress, hasProgress := coordinatorFloat(state.Data, "progress")
	completed := coordinatorInt(state.Data, "completed_steps")
	total := coordinatorInt(state.Data, "total_steps")
	currentAgentID := state.CurrentAgentID
	if currentAgentID == "" {
		currentAgentID = coordinatorString(state.Data, "current_agent_id")
	}
	currentAgent := coordinatorAgentRef(agentByID[currentAgentID], coordinatorString(state.Data, "runtime"))
	nextAction := state.CurrentAction
	if nextAction == "" {
		nextAction = coordinatorString(state.Data, "next_action")
	}
	var blocker *blockerDTO
	if state.BlockerCode != "" || state.BlockerMessage != "" {
		blocker = &blockerDTO{Code: state.BlockerCode, Message: state.BlockerMessage, Source: "coordinator", CreatedAt: state.UpdatedAt}
	}
	snapshot := coordinatorSnapshotDTO{
		RootWorkItemID: state.RootWorkItemID, WorkItemID: workItemID, Status: string(state.Status), Stage: lastStage,
		Progress: func() *float64 {
			if hasProgress {
				return progress
			}
			return nil
		}(),
		CompletedSteps: completed, TotalSteps: total, CurrentAgent: currentAgent,
		NextAction: nextAction, NextActionAt: coordinatorTime(state.NextActionAt), Blocker: blocker,
		RuntimeLabel: config.RuntimeLabel, ModelRef: modelRefName(config.ModelRef),
		ReasoningEffort: config.EffectiveReasoningEffort(), PromptVersion: config.PromptVersion,
		PromptLocked: true, Attempts: attempts, Timeline: timeline,
		UpdatedAt: state.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Version: state.Version,
	}
	return snapshot, timeline, nil
}

func (s *Server) handleGetCoordinatorConfig(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.TaskCoordinators().EnsureConfig(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCoordinatorConfigDTO(config))
}

func (s *Server) validateCoordinatorRuntimeBinding(ctx context.Context, workspaceID, label string) error {
	if label == "mock" {
		return fmt.Errorf("%w: production coordinator settings 不允许 mock runtime", domain.ErrValidation)
	}
	binding, err := s.store.Bindings().GetByLabel(ctx, workspaceID, label)
	if err != nil {
		return fmt.Errorf("%w: coordinator runtime %q 未配置", domain.ErrValidation, label)
	}
	if binding.Status != domain.BindingReady {
		return fmt.Errorf("%w: coordinator runtime %q 尚未就绪", domain.ErrValidation, label)
	}
	if !domain.TaskCoordinatorRuntimeMatchesAdapter(label, binding.AdapterID) {
		return fmt.Errorf("%w: coordinator runtime %q 与 adapter %q 不匹配", domain.ErrValidation, label, binding.AdapterID)
	}
	return nil
}

func (s *Server) handlePatchCoordinatorConfig(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("workspace_id")
	s.idempotent(w, r, workspaceID, func() (int, []byte) {
		var req patchCoordinatorConfigRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if len(req.Instructions) > 0 || len(req.Prompt) > 0 {
			return renderProblem(http.StatusUnprocessableEntity, "validation_failed", "Coordinator prompt is locked", "Task Coordinator 提示词由系统内置，不允许修改")
		}
		c, err := s.store.TaskCoordinators().EnsureConfig(r.Context(), workspaceID)
		if err != nil {
			return problemBytes(err)
		}
		if req.RuntimeLabel != nil {
			c.RuntimeLabel = *req.RuntimeLabel
		}
		if req.ModelRef != nil {
			c.ModelRef = domain.ModelRef{Ref: *req.ModelRef, ReasoningEffort: c.EffectiveReasoningEffort()}
		}
		if req.ReasoningEffort != nil {
			c.ReasoningEffort = *req.ReasoningEffort
		}
		if req.FallbackRuntimeLabel != nil {
			c.FallbackRuntimeLabel = *req.FallbackRuntimeLabel
		}
		if req.FallbackModelRef != nil {
			c.FallbackModelRef = domain.ModelRef{Ref: *req.FallbackModelRef, ReasoningEffort: c.EffectiveReasoningEffort()}
		}
		if s.models != nil {
			for _, ref := range []string{c.ModelRef.Ref, c.FallbackModelRef.Ref} {
				if strings.TrimSpace(ref) == "" {
					continue
				}
				entry, err := s.models.Get(ref)
				if err != nil || entry == nil {
					return problemBytes(fmt.Errorf("%w: coordinator model_ref %q 不存在", domain.ErrValidation, ref))
				}
			}
		}
		if err := s.validateCoordinatorRuntimeBinding(r.Context(), workspaceID, c.RuntimeLabel); err != nil {
			return problemBytes(err)
		}
		if c.FallbackRuntimeLabel != "" {
			if err := s.validateCoordinatorRuntimeBinding(r.Context(), workspaceID, c.FallbackRuntimeLabel); err != nil {
				return problemBytes(err)
			}
		}
		if err := s.store.TaskCoordinators().UpdateConfig(r.Context(), c, req.ExpectedVersion); err != nil {
			return problemBytes(err)
		}
		updated, err := s.store.TaskCoordinators().GetConfig(r.Context(), workspaceID)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toCoordinatorConfigDTO(updated))
	})
}

func (s *Server) handleGetCoordinatorSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshot, _, err := s.coordinatorReadModel(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleListCoordinatorEvents(w http.ResponseWriter, r *http.Request) {
	_, timeline, err := s.coordinatorReadModel(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": timeline})
}

type coordinatorMessageRequest struct {
	Instruction string `json:"instruction"`
}

func (s *Server) handleSendCoordinatorInstruction(w http.ResponseWriter, r *http.Request) {
	wiID := r.PathValue("work_item_id")
	s.idempotent(w, r, wiID, func() (int, []byte) {
		var req coordinatorMessageRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if strings.TrimSpace(req.Instruction) == "" {
			return renderProblem(http.StatusBadRequest, "bad_request", "Instruction required", "instruction 不能为空")
		}
		if _, _, err := s.coordinatorReadModel(r.Context(), wiID); err != nil {
			return problemBytes(err)
		}
		runID, err := s.svc.SendCoordinatorInstruction(r.Context(), wiID, req.Instruction)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusAccepted, map[string]any{"accepted": true, "coordinator_run_id": runID})
	})
}
