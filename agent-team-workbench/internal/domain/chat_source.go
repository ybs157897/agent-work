package domain

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type ChatSourceStatus string

const (
	ChatSourceSaved         ChatSourceStatus = "saved"
	ChatSourceHandedToAgent ChatSourceStatus = "handed_to_agent"
	ChatSourceRead          ChatSourceStatus = "read"
	ChatSourceFailed        ChatSourceStatus = "failed"
)

func (s ChatSourceStatus) Valid() bool {
	return s == ChatSourceSaved || s == ChatSourceHandedToAgent || s == ChatSourceRead || s == ChatSourceFailed
}

// ChatSource is the durable metadata edge between one Chat WorkItem and an
// opaque FileStore object. The absolute filesystem path never enters this
// entity or any browser DTO.
type ChatSource struct {
	ID                string           `json:"id"`
	WorkspaceID       string           `json:"workspace_id"`
	ChatWorkItemID    string           `json:"chat_id"`
	AgentProfileID    string           `json:"agent_id"`
	ClientKey         string           `json:"client_key"`
	Filename          string           `json:"filename"`
	MIME              string           `json:"mime,omitempty"`
	Size              int64            `json:"size"`
	OpaqueKey         string           `json:"-"`
	SHA256            string           `json:"sha256"`
	Status            ChatSourceStatus `json:"status"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	HandedAt          *time.Time       `json:"handed_at,omitempty"`
	Available         bool             `json:"available"`
	AvailabilityError string           `json:"availability_error,omitempty"`
}

func (s *ChatSource) Validate() error {
	if s == nil || strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.WorkspaceID) == "" ||
		strings.TrimSpace(s.ChatWorkItemID) == "" || strings.TrimSpace(s.AgentProfileID) == "" ||
		strings.TrimSpace(s.ClientKey) == "" || strings.TrimSpace(s.Filename) == "" ||
		strings.TrimSpace(s.OpaqueKey) == "" || strings.TrimSpace(s.SHA256) == "" ||
		s.Size < 0 || !s.Status.Valid() {
		return fmt.Errorf("%w: invalid chat source", ErrValidation)
	}
	if len(s.SHA256) != 64 {
		return fmt.Errorf("%w: chat source sha256 must be 64-character hex", ErrValidation)
	}
	if _, err := hex.DecodeString(s.SHA256); err != nil {
		return fmt.Errorf("%w: chat source sha256 is invalid", ErrValidation)
	}
	return nil
}

type ChatSourceRef struct {
	SourceID string `json:"source_id"`
	SHA256   string `json:"sha256"`
}

func (r ChatSourceRef) Validate() error {
	if strings.TrimSpace(r.SourceID) == "" || strings.TrimSpace(r.SHA256) == "" {
		return fmt.Errorf("%w: source_id and sha256 are required", ErrValidation)
	}
	return nil
}
