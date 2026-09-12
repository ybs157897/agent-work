package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

type CreateWorkspaceParams struct {
	Workspace         *domain.Workspace
	Project           *domain.WorkspaceProject
	Location          *domain.WorkspaceLocation
	SourceWorkspaceID string
}

type WorkspaceProvisionResult struct {
	Workspace *domain.Workspace
	Agents    []*domain.AgentProfile
	Pending   bool
}

// CreateWorkspaceWithProject is the DB half of U01 provisioning. It creates
// the project identity, default location, cloned runtime bindings and ordinary
// Agent profiles in one transaction. External YAML writes are represented by
// the existing per-Agent durable intents and reconciled by the HTTP layer;
// they never become a second truth or a second scheduler.
func (s *Service) CreateWorkspaceWithProject(ctx context.Context, p CreateWorkspaceParams) (*WorkspaceProvisionResult, error) {
	if p.Workspace == nil || p.Project == nil || p.Location == nil {
		return nil, fmt.Errorf("%w: workspace project and location are required", domain.ErrValidation)
	}
	if err := p.Project.Validate(); err != nil {
		return nil, err
	}
	if p.Location.WorkspaceID != p.Workspace.ID || p.Project.WorkspaceID != p.Workspace.ID ||
		p.Location.ID != p.Project.LocationID {
		return nil, fmt.Errorf("%w: workspace project/location identity mismatch", domain.ErrValidation)
	}
	if _, err := s.store.Workspaces().Get(ctx, p.Workspace.ID); err == nil {
		return nil, domain.ErrIdempotencyConflict
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	if p.Workspace.Version < 1 {
		p.Workspace.Version = 1
	}
	if p.Workspace.CreatedAt.IsZero() {
		p.Workspace.CreatedAt = now
	}
	p.Workspace.UpdatedAt = p.Workspace.CreatedAt
	if p.Project.CreatedAt.IsZero() {
		p.Project.CreatedAt = now
	}
	p.Project.UpdatedAt = p.Project.CreatedAt
	if p.Location.Version < 1 {
		p.Location.Version = 1
	}
	if p.Location.CreatedAt.IsZero() {
		p.Location.CreatedAt = now
	}
	p.Location.UpdatedAt = p.Location.CreatedAt

	var result = &WorkspaceProvisionResult{}
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.store.Workspaces().Create(ctx, p.Workspace); err != nil {
			return err
		}
		if err := s.store.WorkspaceLocations().Create(ctx, p.Location); err != nil {
			return err
		}
		if err := s.store.WorkspaceProjects().Create(ctx, p.Project); err != nil {
			return err
		}
		if strings.TrimSpace(p.SourceWorkspaceID) != "" {
			source, err := s.store.Workspaces().Get(ctx, p.SourceWorkspaceID)
			if err != nil {
				return fmt.Errorf("%w: source workspace is unavailable", domain.ErrValidation)
			}
			if source.ID == p.Workspace.ID {
				return fmt.Errorf("%w: source workspace must differ from target", domain.ErrValidation)
			}
			sourceCoordinator, sourceLibrarian, err := s.sourceSystemConfiguration(ctx, source.ID)
			if err != nil {
				return err
			}
			bindings, err := s.store.Bindings().List(ctx, source.ID)
			if err != nil {
				return err
			}
			for _, binding := range bindings {
				clone := *binding
				clone.ID = domain.NewID(domain.PrefixBinding)
				clone.WorkspaceID = p.Workspace.ID
				clone.Version = 1
				clone.CreatedAt, clone.UpdatedAt = now, now
				if err := s.store.Bindings().Create(ctx, &clone); err != nil {
					return err
				}
			}
			agents, err := s.store.Agents().List(ctx, source.ID)
			if err != nil {
				return err
			}
			for _, sourceAgent := range agents {
				if sourceAgent.Kind.IsSystem() {
					continue
				}
				if !s.agentConfigSyncIntentsEnabled || s.store.AgentConfigSyncIntents() == nil {
					return fmt.Errorf("%w: Agent 配置同步未启用，无法可靠复制源 Workspace 配置", domain.ErrCapabilityMissing)
				}
				clone := cloneWorkspaceAgent(sourceAgent, p.Workspace.ID, now)
				if err := s.store.Agents().Create(ctx, clone); err != nil {
					return err
				}
				intent, err := s.newAgentConfigSyncIntent(clone)
				if err != nil {
					return err
				}
				if err := s.store.AgentConfigSyncIntents().Create(ctx, intent); err != nil {
					return err
				}
				result.Agents = append(result.Agents, clone)
			}
			coordinator, err := s.store.TaskCoordinators().EnsureConfig(ctx, p.Workspace.ID)
			if err != nil {
				return err
			}
			if sourceCoordinator != nil {
				copyCoordinatorConfig(coordinator, sourceCoordinator)
				if err := s.store.TaskCoordinators().UpdateConfig(ctx, coordinator, coordinator.Version); err != nil {
					return err
				}
			}
			librarian, err := s.EnsureBuiltinKnowledgeLibrarian(ctx, p.Workspace.ID)
			if err != nil {
				return err
			}
			if sourceLibrarian != nil {
				candidate := *librarian
				candidate.RuntimePreference = cloneRuntimePreference(sourceLibrarian.RuntimePreference)
				candidate.ModelOverride = sourceLibrarian.ModelOverride
				candidate.UpdatedAt = now
				if err := s.store.Agents().UpdateSystemRuntimeModel(ctx, &candidate, librarian.Version); err != nil {
					return err
				}
			}
			result.Workspace = p.Workspace
			result.Pending = len(result.Agents) > 0
			return nil
		}
		if _, err := s.store.TaskCoordinators().EnsureConfig(ctx, p.Workspace.ID); err != nil {
			return err
		}
		if _, err := s.EnsureBuiltinKnowledgeLibrarian(ctx, p.Workspace.ID); err != nil {
			return err
		}
		result.Workspace = p.Workspace
		result.Pending = len(result.Agents) > 0
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) sourceSystemConfiguration(ctx context.Context, workspaceID string) (*domain.TaskCoordinatorConfig, *domain.AgentProfile, error) {
	var coordinator *domain.TaskCoordinatorConfig
	if current, err := s.store.TaskCoordinators().GetConfig(ctx, workspaceID); err == nil {
		coordinator = current
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, nil, err
	}

	var librarian *domain.AgentProfile
	if current, err := s.store.Agents().Get(ctx, domain.KnowledgeLibrarianAgentID(workspaceID)); err == nil {
		librarian = current
	} else if !errors.Is(err, domain.ErrNotFound) {
		return nil, nil, err
	}

	return coordinator, librarian, nil
}

func copyCoordinatorConfig(target, source *domain.TaskCoordinatorConfig) {
	target.RuntimeLabel = source.RuntimeLabel
	target.FallbackRuntimeLabel = source.FallbackRuntimeLabel
	target.ModelRef = source.ModelRef
	target.FallbackModelRef = source.FallbackModelRef
	target.ReasoningEffort = source.EffectiveReasoningEffort()
}

func cloneRuntimePreference(source domain.RuntimePreference) domain.RuntimePreference {
	source.Fallbacks = append([]string(nil), source.Fallbacks...)
	return source
}

func cloneWorkspaceAgent(source *domain.AgentProfile, workspaceID string, now time.Time) *domain.AgentProfile {
	clone := *source
	clone.ID = domain.NewID(domain.PrefixAgent)
	clone.WorkspaceID = workspaceID
	clone.Slug = durableAgentConfigSlug(clone.ID)
	clone.Kind = domain.AgentProfileKindUser
	clone.Availability = domain.AgentEnabled
	clone.Presence = domain.PresenceIdle
	clone.HeartbeatEnabled = false
	clone.HeartbeatIntervalSec = 0
	clone.WakeOnAssignment = true
	clone.WakeOnDemand = true
	clone.WakeOnAutomation = false
	clone.LastHeartbeatAt = nil
	clone.Version = 1
	clone.CreatedAt, clone.UpdatedAt = now, now
	clone.InstructionsEditable = true
	clone.Skills = append([]string(nil), source.Skills...)
	clone.RuntimePreference.Fallbacks = append([]string(nil), source.RuntimePreference.Fallbacks...)
	clone.Policy.Tools = append([]string(nil), source.Policy.Tools...)
	return &clone
}
