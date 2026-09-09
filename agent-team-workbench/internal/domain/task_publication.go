package domain

import (
	"fmt"
	"strings"
	"time"
)

type PublicationDraftStatus string

const (
	PublicationDraftReady     PublicationDraftStatus = "ready"
	PublicationDraftStale     PublicationDraftStatus = "stale"
	PublicationDraftPublished PublicationDraftStatus = "published"
)

func (s PublicationDraftStatus) Valid() bool {
	return s == PublicationDraftReady || s == PublicationDraftStale || s == PublicationDraftPublished
}

type TaskPublicationDraft struct {
	ID                     string                 `json:"draft_id"`
	WorkspaceID            string                 `json:"workspace_id"`
	ChatWorkItemID         string                 `json:"chat_id"`
	AgentProfileID         string                 `json:"agent_id"`
	AnalysisRevision       int64                  `json:"analysis_revision"`
	ItemIDsJSON            string                 `json:"-"`
	ConfirmationIDsJSON    string                 `json:"-"`
	SourceDependenciesJSON string                 `json:"-"`
	Title                  string                 `json:"title"`
	Description            string                 `json:"description"`
	AcceptanceCriteriaJSON string                 `json:"-"`
	ContextSnapshotID      string                 `json:"context_snapshot_id"`
	BaselineJSON           string                 `json:"-"`
	Fingerprint            string                 `json:"fingerprint"`
	Status                 PublicationDraftStatus `json:"status"`
	TaskID                 string                 `json:"task_id,omitempty"`
	ClientKey              string                 `json:"client_key"`
	Version                int                    `json:"version"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

func (d *TaskPublicationDraft) Validate() error {
	if d == nil || strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.WorkspaceID) == "" ||
		strings.TrimSpace(d.ChatWorkItemID) == "" || strings.TrimSpace(d.AgentProfileID) == "" ||
		d.AnalysisRevision < 1 || strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Fingerprint) == "" ||
		!d.Status.Valid() || strings.TrimSpace(d.ClientKey) == "" || d.Version < 1 {
		return fmt.Errorf("%w: invalid publication draft", ErrValidation)
	}
	return nil
}

type TaskPublication struct {
	ID                     string    `json:"publication_id"`
	DraftID                string    `json:"draft_id"`
	WorkspaceID            string    `json:"workspace_id"`
	ChatWorkItemID         string    `json:"chat_id"`
	TaskID                 string    `json:"task_id"`
	AnalysisRevision       int64     `json:"analysis_revision"`
	Fingerprint            string    `json:"fingerprint"`
	ClientKey              string    `json:"client_key"`
	ExpectedVersion        int       `json:"expected_version"`
	ConfirmationIDsJSON    string    `json:"-"`
	SourceDependenciesJSON string    `json:"-"`
	BaselineJSON           string    `json:"-"`
	ContextSnapshotID      string    `json:"context_snapshot_id"`
	CreatedAt              time.Time `json:"created_at"`
}
