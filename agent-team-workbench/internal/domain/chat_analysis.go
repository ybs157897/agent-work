package domain

import (
	"fmt"
	"strings"
	"time"
)

// ChatAnalysisStatus is the durable projection state for the optional
// requirement analysis workflow. It is independent from the Chat Run state.
type ChatAnalysisStatus string

const (
	ChatAnalysisIdle        ChatAnalysisStatus = "idle"
	ChatAnalysisAnalyzing   ChatAnalysisStatus = "analyzing"
	ChatAnalysisNeedsAnswer ChatAnalysisStatus = "needs_answer"
	ChatAnalysisReady       ChatAnalysisStatus = "ready"
	ChatAnalysisFailed      ChatAnalysisStatus = "failed"
	ChatAnalysisStale       ChatAnalysisStatus = "stale"
)

func (s ChatAnalysisStatus) Valid() bool {
	switch s {
	case ChatAnalysisIdle, ChatAnalysisAnalyzing, ChatAnalysisNeedsAnswer,
		ChatAnalysisReady, ChatAnalysisFailed, ChatAnalysisStale:
		return true
	default:
		return false
	}
}

// ChatAnalysisAttemptStatus records processing of one immutable analysis Run.
// A late attempt is retained as stale evidence and cannot replace the current
// projection.
type ChatAnalysisAttemptStatus string

const (
	ChatAnalysisAttemptAnalyzing ChatAnalysisAttemptStatus = "analyzing"
	ChatAnalysisAttemptSucceeded ChatAnalysisAttemptStatus = "succeeded"
	ChatAnalysisAttemptFailed    ChatAnalysisAttemptStatus = "failed"
	ChatAnalysisAttemptStale     ChatAnalysisAttemptStatus = "stale"
)

func (s ChatAnalysisAttemptStatus) Valid() bool {
	return s == ChatAnalysisAttemptAnalyzing || s == ChatAnalysisAttemptSucceeded ||
		s == ChatAnalysisAttemptFailed || s == ChatAnalysisAttemptStale
}

type ChatAnalysis struct {
	WorkspaceID    string             `json:"workspace_id"`
	ChatWorkItemID string             `json:"chat_id"`
	AgentProfileID string             `json:"agent_id"`
	Version        int                `json:"version"`
	Revision       int64              `json:"revision"`
	Status         ChatAnalysisStatus `json:"status"`
	CurrentRunID   string             `json:"run_id,omitempty"`
	Error          string             `json:"error,omitempty"`
	DocumentJSON   string             `json:"-"`
	DocumentDigest string             `json:"-"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

func (a *ChatAnalysis) Validate() error {
	if a == nil || strings.TrimSpace(a.WorkspaceID) == "" ||
		strings.TrimSpace(a.ChatWorkItemID) == "" || strings.TrimSpace(a.AgentProfileID) == "" ||
		a.Version < 0 || a.Revision < 0 || !a.Status.Valid() {
		return fmt.Errorf("%w: invalid chat analysis", ErrValidation)
	}
	return nil
}

type ChatAnalysisAttempt struct {
	ID                string                    `json:"id"`
	WorkspaceID       string                    `json:"workspace_id"`
	ChatWorkItemID    string                    `json:"chat_id"`
	AgentProfileID    string                    `json:"agent_id"`
	RunID             string                    `json:"run_id"`
	Version           int                       `json:"version"`
	RequestDigest     string                    `json:"request_digest"`
	SourceCatalogJSON string                    `json:"-"`
	Status            ChatAnalysisAttemptStatus `json:"status"`
	Revision          int64                     `json:"revision"`
	Error             string                    `json:"error,omitempty"`
	CreatedAt         time.Time                 `json:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at"`
}

func (a *ChatAnalysisAttempt) Validate() error {
	if a == nil || strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.WorkspaceID) == "" ||
		strings.TrimSpace(a.ChatWorkItemID) == "" || strings.TrimSpace(a.AgentProfileID) == "" ||
		strings.TrimSpace(a.RunID) == "" || a.Version < 1 || strings.TrimSpace(a.RequestDigest) == "" ||
		strings.TrimSpace(a.SourceCatalogJSON) == "" || !a.Status.Valid() || a.Revision < 0 {
		return fmt.Errorf("%w: invalid chat analysis attempt", ErrValidation)
	}
	return nil
}

type ChatAnalysisRevision struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	ChatWorkItemID string    `json:"chat_id"`
	AgentProfileID string    `json:"agent_id"`
	RunID          string    `json:"run_id"`
	Revision       int64     `json:"revision"`
	DocumentJSON   string    `json:"-"`
	DocumentDigest string    `json:"document_digest"`
	CreatedAt      time.Time `json:"created_at"`
}

func (r *ChatAnalysisRevision) Validate() error {
	if r == nil || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.WorkspaceID) == "" ||
		strings.TrimSpace(r.ChatWorkItemID) == "" || strings.TrimSpace(r.AgentProfileID) == "" ||
		strings.TrimSpace(r.RunID) == "" || r.Revision < 1 || strings.TrimSpace(r.DocumentJSON) == "" ||
		strings.TrimSpace(r.DocumentDigest) == "" {
		return fmt.Errorf("%w: invalid chat analysis revision", ErrValidation)
	}
	return nil
}

type ChatAnalysisAnswer struct {
	ID                     string    `json:"id"`
	WorkspaceID            string    `json:"workspace_id"`
	ChatWorkItemID         string    `json:"chat_id"`
	AgentProfileID         string    `json:"agent_id"`
	Revision               int64     `json:"revision"`
	QuestionID             string    `json:"question_id"`
	QuestionFingerprint    string    `json:"question_fingerprint"`
	SelectedOptionIDs      []string  `json:"selected_option_ids,omitempty"`
	Text                   string    `json:"text,omitempty"`
	Disposition            string    `json:"disposition"`
	SourceDependenciesJSON string    `json:"-"`
	ClientKey              string    `json:"client_key"`
	InheritedFromAnswerID  string    `json:"inherited_from_answer_id,omitempty"`
	LineageReason          string    `json:"lineage_reason,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

func (a *ChatAnalysisAnswer) Validate() error {
	if a == nil || strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.WorkspaceID) == "" ||
		strings.TrimSpace(a.ChatWorkItemID) == "" || strings.TrimSpace(a.AgentProfileID) == "" ||
		a.Revision < 1 || strings.TrimSpace(a.QuestionID) == "" ||
		strings.TrimSpace(a.QuestionFingerprint) == "" || strings.TrimSpace(a.ClientKey) == "" {
		return fmt.Errorf("%w: invalid chat analysis answer", ErrValidation)
	}
	if a.Disposition != "answered" && a.Disposition != "deferred" {
		return fmt.Errorf("%w: invalid chat analysis answer disposition", ErrValidation)
	}
	return nil
}
