package httpapi

import (
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// DTO 全部 snake_case（协议文档 §5.1）；Provider 原始字段不得出现在 Web DTO。

type workspaceDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	Version  int    `json:"version"`
}

func toWorkspaceDTO(w *domain.Workspace) workspaceDTO {
	return workspaceDTO{ID: w.ID, Name: w.Name, Timezone: w.Timezone, Version: w.Version}
}

type agentDTO struct {
	ID                string                   `json:"id"`
	Slug              string                   `json:"slug,omitempty"`
	Name              string                   `json:"name"`
	Role              string                   `json:"role"`
	Skills            []string                 `json:"skills"`
	Instructions      string                   `json:"instructions,omitempty"`
	Availability      string                   `json:"availability"`
	Presence          string                   `json:"presence"`
	Avatar            string                   `json:"avatar,omitempty"`
	RuntimePreference domain.RuntimePreference `json:"runtime_preference,omitempty"`
	ModelOverride     domain.ModelRef          `json:"model_override,omitempty"`
	Policy            domain.AgentPolicy       `json:"policy,omitempty"`
	Version           int                      `json:"version"`
}

func toAgentDTO(a *domain.AgentProfile) agentDTO {
	return agentDTO{
		ID: a.ID, Slug: a.Slug, Name: a.Name, Role: a.Role, Skills: a.Skills,
		Instructions: a.Instructions,
		Availability: string(a.Availability), Presence: string(a.Presence),
		Avatar: a.Avatar, RuntimePreference: a.RuntimePreference,
		ModelOverride: a.ModelOverride, Policy: a.Policy, Version: a.Version,
	}
}

type blockerDTO struct {
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type workItemDTO struct {
	ID             string      `json:"id"`
	WorkspaceID    string      `json:"workspace_id"`
	Title          string      `json:"title"`
	Description    string      `json:"description"`
	Status         string      `json:"status"`
	Phase          string      `json:"phase,omitempty"`
	Priority       string      `json:"priority"`
	DueDate        *string     `json:"due_date"`
	AgentProfileID string      `json:"agent_profile_id,omitempty"`
	Blocker        *blockerDTO `json:"blocker,omitempty"`
	RunsCount      int         `json:"runs_count"`
	LatestRunID    string      `json:"latest_run_id,omitempty"`
	Version        int         `json:"version"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

func toWorkItemDTO(w *domain.WorkItem) workItemDTO {
	d := workItemDTO{
		ID: w.ID, WorkspaceID: w.WorkspaceID, Title: w.Title, Description: w.Description,
		Status: string(w.Status), Phase: string(w.Phase), Priority: string(w.Priority),
		AgentProfileID: w.AgentProfileID, Version: w.Version,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
	if w.DueDate != nil {
		s := w.DueDate.Format("2006-01-02")
		d.DueDate = &s
	}
	return d
}

type runDTO struct {
	ID             string      `json:"id"`
	WorkItemID     string      `json:"work_item_id"`
	AgentProfileID string      `json:"agent_profile_id,omitempty"`
	Status         string      `json:"status"`
	RuntimeLabel   string      `json:"runtime_label,omitempty"`
	Progress       *float64    `json:"progress,omitempty"`
	RetryOf        string      `json:"retry_of,omitempty"`
	Failure        *failureDTO `json:"failure,omitempty"`
	Version        int         `json:"version"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type failureDTO struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func toRunDTO(r *domain.ExecutionRun) runDTO {
	d := runDTO{
		ID: r.ID, WorkItemID: r.WorkItemID, AgentProfileID: r.AgentProfileID,
		Status: string(r.Status), RuntimeLabel: r.RuntimeLabel, Progress: r.Progress,
		RetryOf: r.RetryOf, Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.Failure != nil {
		d.Failure = &failureDTO{Code: r.Failure.Code, Message: r.Failure.Message, Retryable: r.Failure.Retryable}
	}
	return d
}

type approvalDTO struct {
	ID         string     `json:"id"`
	RunID      string     `json:"run_id"`
	WorkItemID string     `json:"work_item_id"`
	Kind       string     `json:"kind"`
	Risk       string     `json:"risk"`
	Status     string     `json:"status"`
	Summary    string     `json:"summary"`
	ResolvedBy string     `json:"resolved_by,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func toApprovalDTO(a *domain.ApprovalRequest) approvalDTO {
	return approvalDTO{
		ID: a.ID, RunID: a.RunID, WorkItemID: a.WorkItemID, Kind: a.Kind,
		Risk: a.Risk, Status: string(a.Status), Summary: a.Summary,
		ResolvedBy: a.ResolvedBy, ResolvedAt: a.ResolvedAt,
	}
}

type artifactDTO struct {
	ID          string    `json:"id"`
	RunID       string    `json:"run_id"`
	LogicalPath string    `json:"logical_path"`
	Mime        string    `json:"mime"`
	Size        int64     `json:"size"`
	Sha256      string    `json:"sha256"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

func toArtifactDTO(a *domain.Artifact) artifactDTO {
	return artifactDTO{
		ID: a.ID, RunID: a.RunID, LogicalPath: a.LogicalPath, Mime: a.Mime,
		Size: a.Size, Sha256: a.Sha256, Status: string(a.Status), CreatedAt: a.CreatedAt,
	}
}

type activityDTO struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurred_at"`
}

func toActivityDTO(a application.Activity) activityDTO {
	return activityDTO{ID: a.ID, Kind: a.Kind, Message: a.Message, OccurredAt: a.OccurredAt}
}

// ── 请求体 ───────────────────────────────────────────────────────────

type createAgentRequest struct {
	Name              string          `json:"name"`
	Role              string          `json:"role"`
	Skills            []string        `json:"skills"`
	Avatar            string          `json:"avatar"`
	RuntimePreference *runtimePrefDTO `json:"runtime_preference"`
}

type createWorkItemRequest struct {
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	Status         string  `json:"status"`
	Priority       string  `json:"priority"`
	DueDate        *string `json:"due_date"`
	AgentProfileID string  `json:"agent_profile_id"`
}

type moveWorkItemRequest struct {
	Status          string `json:"status"`
	ExpectedVersion int    `json:"expected_version"`
}

type assignWorkItemRequest struct {
	AgentProfileID  string `json:"agent_profile_id"`
	ExpectedVersion int    `json:"expected_version"`
}

type blockWorkItemRequest struct {
	Code            string `json:"code"`
	Message         string `json:"message"`
	Source          string `json:"source"`
	ExpectedVersion int    `json:"expected_version"`
}

type unblockWorkItemRequest struct {
	ExpectedVersion int `json:"expected_version"`
}

type createRunRequest struct {
	AgentProfileID    string            `json:"agent_profile_id"`
	RuntimePreference *runtimePrefDTO   `json:"runtime_preference"`
	Requirements      map[string]string `json:"requirements"`
	Input             struct {
		Instruction        string   `json:"instruction"`
		AcceptanceCriteria []string `json:"acceptance_criteria"`
	} `json:"input"`
}

type runtimePrefDTO struct {
	Preferred   string   `json:"preferred"`
	Fallbacks   []string `json:"fallbacks"`
	Mode        string   `json:"mode"`
	AgentPreset string   `json:"agent_preset"`
}

type resolveApprovalRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// ── TaskSession ──────────────────────────────────────────────────────

type taskSessionDTO struct {
	ID             string         `json:"id"`
	AgentProfileID string         `json:"agent_profile_id"`
	AdapterID      string         `json:"adapter_id"`
	TaskKey        string         `json:"task_key"`
	SessionRef     string         `json:"session_ref,omitempty"`
	SessionParams  map[string]any `json:"session_params,omitempty"`
	DisplayID      string         `json:"display_id,omitempty"`
	RunsCount      int            `json:"runs_count"`
	InputTokensCum int64          `json:"input_tokens_cum"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// toTaskSessionDTO 输出锚点投影；SessionParams 保留 __ref/__fingerprint 供诊断。
func toTaskSessionDTO(t *domain.TaskSession) taskSessionDTO {
	return taskSessionDTO{
		ID: t.ID, AgentProfileID: t.AgentProfileID, AdapterID: t.AdapterID, TaskKey: t.TaskKey,
		SessionRef: t.SessionRef(), SessionParams: t.SessionParams, DisplayID: t.DisplayID,
		RunsCount: t.RunsCount, InputTokensCum: t.InputTokensCum,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

type resetTaskSessionRequest struct {
	TaskKey   string `json:"task_key"`
	AdapterID string `json:"adapter_id"`
}
