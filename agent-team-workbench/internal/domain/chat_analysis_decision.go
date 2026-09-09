package domain

import (
	"fmt"
	"strings"
	"time"
)

type ChatAnalysisDecisionOutcome string

const (
	ChatAnalysisDecisionConfirmed          ChatAnalysisDecisionOutcome = "confirmed"
	ChatAnalysisDecisionRejected           ChatAnalysisDecisionOutcome = "rejected"
	ChatAnalysisDecisionNeedsClarification ChatAnalysisDecisionOutcome = "needs_clarification"
)

func (o ChatAnalysisDecisionOutcome) Valid() bool {
	return o == ChatAnalysisDecisionConfirmed || o == ChatAnalysisDecisionRejected || o == ChatAnalysisDecisionNeedsClarification
}

type ChatAnalysisDecisionStatus string

const (
	ChatAnalysisDecisionValid               ChatAnalysisDecisionStatus = "valid"
	ChatAnalysisDecisionNeedsReconfirmation ChatAnalysisDecisionStatus = "needs_reconfirmation"
	ChatAnalysisDecisionStale               ChatAnalysisDecisionStatus = "stale"
)

func (s ChatAnalysisDecisionStatus) Valid() bool {
	return s == ChatAnalysisDecisionValid || s == ChatAnalysisDecisionNeedsReconfirmation || s == ChatAnalysisDecisionStale
}

// ChatAnalysisDecision is an immutable product conclusion event. Its current
// usability is projected separately so a source or analysis change never
// rewrites this historical fact.
type ChatAnalysisDecision struct {
	ID                     string                      `json:"id"`
	WorkspaceID            string                      `json:"workspace_id"`
	ChatWorkItemID         string                      `json:"chat_id"`
	AgentProfileID         string                      `json:"agent_id"`
	Revision               int64                       `json:"revision"`
	ItemID                 string                      `json:"item_id"`
	Outcome                ChatAnalysisDecisionOutcome `json:"outcome"`
	Conclusion             string                      `json:"conclusion"`
	Basis                  string                      `json:"basis"`
	ProductVersion         string                      `json:"product_version"`
	ItemFingerprint        string                      `json:"item_fingerprint"`
	SourceDependenciesJSON string                      `json:"-"`
	ActorID                string                      `json:"actor_id"`
	ClientKey              string                      `json:"client_key"`
	CreatedAt              time.Time                   `json:"created_at"`
}

func (d *ChatAnalysisDecision) Validate() error {
	if d == nil || strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.WorkspaceID) == "" ||
		strings.TrimSpace(d.ChatWorkItemID) == "" || strings.TrimSpace(d.AgentProfileID) == "" ||
		d.Revision < 1 || strings.TrimSpace(d.ItemID) == "" || !d.Outcome.Valid() ||
		strings.TrimSpace(d.Conclusion) == "" || strings.TrimSpace(d.Basis) == "" ||
		strings.TrimSpace(d.ProductVersion) == "" || strings.TrimSpace(d.ItemFingerprint) == "" ||
		strings.TrimSpace(d.ActorID) == "" || strings.TrimSpace(d.ClientKey) == "" {
		return fmt.Errorf("%w: invalid chat analysis decision", ErrValidation)
	}
	return nil
}

type ChatAnalysisDecisionState struct {
	WorkspaceID            string                      `json:"workspace_id"`
	ChatWorkItemID         string                      `json:"chat_id"`
	ItemID                 string                      `json:"item_id"`
	Revision               int64                       `json:"revision"`
	DecisionID             string                      `json:"decision_id"`
	Outcome                ChatAnalysisDecisionOutcome `json:"outcome"`
	Conclusion             string                      `json:"conclusion"`
	Basis                  string                      `json:"basis"`
	ProductVersion         string                      `json:"product_version"`
	ItemFingerprint        string                      `json:"item_fingerprint"`
	SourceDependenciesJSON string                      `json:"-"`
	Status                 ChatAnalysisDecisionStatus  `json:"status"`
	ReviewReason           string                      `json:"review_reason,omitempty"`
	UpdatedAt              time.Time                   `json:"updated_at"`
}

func (s *ChatAnalysisDecisionState) Validate() error {
	if s == nil || strings.TrimSpace(s.WorkspaceID) == "" || strings.TrimSpace(s.ChatWorkItemID) == "" ||
		strings.TrimSpace(s.ItemID) == "" || s.Revision < 1 || strings.TrimSpace(s.DecisionID) == "" ||
		!s.Outcome.Valid() || strings.TrimSpace(s.ItemFingerprint) == "" || !s.Status.Valid() {
		return fmt.Errorf("%w: invalid chat analysis decision state", ErrValidation)
	}
	return nil
}

type ChatAnalysisReopen struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	ChatWorkItemID string    `json:"chat_id"`
	ItemID         string    `json:"item_id"`
	FromRevision   int64     `json:"from_revision"`
	ToRevision     int64     `json:"to_revision"`
	Reason         string    `json:"reason"`
	SourceJSON     string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
}

func (r *ChatAnalysisReopen) Validate() error {
	if r == nil || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.WorkspaceID) == "" ||
		strings.TrimSpace(r.ChatWorkItemID) == "" || strings.TrimSpace(r.ItemID) == "" ||
		r.FromRevision < 1 || r.ToRevision < 1 || strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("%w: invalid chat analysis reopen", ErrValidation)
	}
	return nil
}
