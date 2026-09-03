package mcpserver

import (
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// MCP task/run DTOs are intentionally local to this package. Adding JSON tags
// to the domain structs would silently change the HTTP/event surfaces, while
// marshaling those structs directly leaks Go field names and private provider
// session material.
type workItemDTO struct {
	ID                 string                    `json:"id"`
	WorkspaceID        string                    `json:"workspace_id"`
	RecordKind         domain.WorkItemRecordKind `json:"record_kind"`
	ParentID           string                    `json:"parent_id,omitempty"`
	Title              string                    `json:"title"`
	Description        string                    `json:"description"`
	Status             domain.WorkItemStatus     `json:"status"`
	Phase              domain.WorkItemPhase      `json:"phase,omitempty"`
	Priority           domain.Priority           `json:"priority"`
	DueDate            *time.Time                `json:"due_date,omitempty"`
	AgentProfileID     string                    `json:"agent_profile_id,omitempty"`
	ClientKey          string                    `json:"client_key,omitempty"`
	LockedByRunID      string                    `json:"locked_by_run_id,omitempty"`
	LockedAt           *time.Time                `json:"locked_at,omitempty"`
	RollingDigest      string                    `json:"rolling_digest,omitempty"`
	AcceptanceCriteria []string                  `json:"acceptance_criteria,omitempty"`
	PhaseEnteredAt     *time.Time                `json:"phase_entered_at,omitempty"`
	Version            int                       `json:"version"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
}

func newWorkItemDTO(item *domain.WorkItem) *workItemDTO {
	if item == nil {
		return nil
	}
	return &workItemDTO{
		ID: item.ID, WorkspaceID: item.WorkspaceID, RecordKind: item.RecordKind,
		ParentID: item.ParentID, Title: item.Title, Description: item.Description,
		Status: item.Status, Phase: item.Phase, Priority: item.Priority, DueDate: item.DueDate,
		AgentProfileID: item.AgentProfileID, ClientKey: item.ClientKey,
		LockedByRunID: item.LockedByRunID, LockedAt: item.LockedAt, RollingDigest: item.RollingDigest,
		AcceptanceCriteria: append([]string(nil), item.AcceptanceCriteria...),
		PhaseEnteredAt:     item.PhaseEnteredAt, Version: item.Version,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

type runFailureDTO struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type executionRunDTO struct {
	ID                   string           `json:"id"`
	WorkspaceID          string           `json:"workspace_id"`
	WorkItemID           string           `json:"work_item_id"`
	AgentProfileID       string           `json:"agent_profile_id,omitempty"`
	Status               domain.RunStatus `json:"status"`
	RuntimeLabel         string           `json:"runtime_label,omitempty"`
	AdapterID            string           `json:"adapter_id,omitempty"`
	Provider             string           `json:"provider,omitempty"`
	CapabilitySnapshotID string           `json:"capability_snapshot_id,omitempty"`
	ContextSnapshotID    string           `json:"context_snapshot_id,omitempty"`
	UsageIn              int64            `json:"usage_in"`
	UsageOut             int64            `json:"usage_out"`
	UsageCached          int64            `json:"usage_cached"`
	UsageBasis           string           `json:"usage_basis,omitempty"`
	ErrorFamily          string           `json:"error_family,omitempty"`
	ClientKey            string           `json:"client_key,omitempty"`
	DispatchID           string           `json:"dispatch_id,omitempty"`
	Progress             *float64         `json:"progress,omitempty"`
	RetryOf              string           `json:"retry_of,omitempty"`
	Failure              *runFailureDTO   `json:"failure,omitempty"`
	Version              int              `json:"version"`
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
	FinishedAt           *time.Time       `json:"finished_at,omitempty"`
}

func newExecutionRunDTO(run *domain.ExecutionRun) *executionRunDTO {
	if run == nil {
		return nil
	}
	dto := &executionRunDTO{
		ID: run.ID, WorkspaceID: run.WorkspaceID, WorkItemID: run.WorkItemID,
		AgentProfileID: run.AgentProfileID, Status: run.Status, RuntimeLabel: run.RuntimeLabel,
		AdapterID: run.AdapterID, Provider: run.Provider, CapabilitySnapshotID: run.CapabilitySnapshotID,
		ContextSnapshotID: run.ContextSnapshotID, UsageIn: run.UsageIn, UsageOut: run.UsageOut,
		UsageCached: run.UsageCached, UsageBasis: run.UsageBasis, ErrorFamily: run.ErrorFamily,
		ClientKey: run.ClientKey, DispatchID: run.DispatchID, Progress: run.Progress,
		RetryOf: run.RetryOf, Version: run.Version, CreatedAt: run.CreatedAt,
		UpdatedAt: run.UpdatedAt, FinishedAt: run.FinishedAt,
	}
	if run.Failure != nil {
		dto.Failure = &runFailureDTO{Code: run.Failure.Code, Message: run.Failure.Message, Retryable: run.Failure.Retryable}
	}
	return dto
}

func workItemDTOs(items []*domain.WorkItem) []*workItemDTO {
	out := make([]*workItemDTO, 0, len(items))
	for _, item := range items {
		out = append(out, newWorkItemDTO(item))
	}
	return out
}

func executionRunDTOs(runs []*domain.ExecutionRun) []*executionRunDTO {
	out := make([]*executionRunDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, newExecutionRunDTO(run))
	}
	return out
}
