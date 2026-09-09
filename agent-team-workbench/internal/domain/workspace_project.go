package domain

import (
	"fmt"
	"strings"
	"time"
)

type WorkspaceProjectStatus string

const (
	WorkspaceProjectPending     WorkspaceProjectStatus = "pending"
	WorkspaceProjectReady       WorkspaceProjectStatus = "ready"
	WorkspaceProjectUnavailable WorkspaceProjectStatus = "unavailable"
	WorkspaceProjectSetupFailed WorkspaceProjectStatus = "setup_failed"
)

func (s WorkspaceProjectStatus) Valid() bool {
	return s == WorkspaceProjectPending || s == WorkspaceProjectReady || s == WorkspaceProjectUnavailable || s == WorkspaceProjectSetupFailed
}

// WorkspaceProject is the path-free, durable identity of the one project
// directory owned by a Workspace. canonical_key is derived by the trusted
// Host registry and never comes from a browser path.
type WorkspaceProject struct {
	WorkspaceID        string                 `json:"workspace_id"`
	ExecutionHostID    string                 `json:"execution_host_id"`
	MountAlias         string                 `json:"mount_alias"`
	MountGeneration    string                 `json:"mount_generation"`
	RepositoryIdentity string                 `json:"repository_identity"`
	CanonicalKey       string                 `json:"canonical_key"`
	LocationID         string                 `json:"location_id"`
	Status             WorkspaceProjectStatus `json:"status"`
	Error              string                 `json:"error,omitempty"`
	SourceWorkspaceID  string                 `json:"source_workspace_id,omitempty"`
	Version            int                    `json:"version"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

func (p *WorkspaceProject) Validate() error {
	if p == nil || strings.TrimSpace(p.WorkspaceID) == "" ||
		strings.TrimSpace(p.ExecutionHostID) == "" || strings.TrimSpace(p.MountAlias) == "" ||
		strings.TrimSpace(p.MountGeneration) == "" || strings.TrimSpace(p.RepositoryIdentity) == "" ||
		strings.TrimSpace(p.CanonicalKey) == "" || !p.Status.Valid() || p.Version < 1 {
		return fmt.Errorf("%w: invalid workspace project", ErrValidation)
	}
	return nil
}

type WorkspaceProjectInput struct {
	ExecutionHostID    string `json:"execution_host_id"`
	MountAlias         string `json:"mount_alias"`
	MountGeneration    string `json:"mount_generation"`
	RepositoryIdentity string `json:"repository_identity"`
}

func (p WorkspaceProjectInput) Validate() error {
	if strings.TrimSpace(p.ExecutionHostID) == "" || strings.TrimSpace(p.MountAlias) == "" ||
		strings.TrimSpace(p.MountGeneration) == "" || strings.TrimSpace(p.RepositoryIdentity) == "" {
		return fmt.Errorf("%w: project host/mount identity is required", ErrValidation)
	}
	return nil
}

func WorkspaceProjectCanonicalKey(hostID, mountAlias string) string {
	return strings.TrimSpace(hostID) + ":" + strings.TrimSpace(mountAlias)
}
