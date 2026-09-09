package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/hostregistry"
)

type createWorkspaceProjectRequest struct {
	ExecutionHostID    string `json:"execution_host_id"`
	MountAlias         string `json:"mount_alias"`
	MountGeneration    string `json:"mount_generation"`
	RepositoryIdentity string `json:"repository_identity"`
}

type createWorkspaceRequest struct {
	Name              string                        `json:"name"`
	Timezone          string                        `json:"timezone"`
	Project           createWorkspaceProjectRequest `json:"project"`
	SourceWorkspaceID string                        `json:"source_workspace_id,omitempty"`
}

type workspaceResponse struct {
	workspaceDTO
	Reused bool `json:"reused,omitempty"`
}

type agentConfigWorkspacePreparer interface {
	PrepareWorkspaceScope(context.Context, string) error
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := s.store.Workspaces().Get(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	dto, err := s.workspaceDTO(r.Context(), ws)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	s.idempotent(w, r, "workspace-create", func() (int, []byte) {
		var req createWorkspaceRequest
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		if strings.TrimSpace(req.Name) == "" {
			return renderProblem(http.StatusBadRequest, "bad_request", "Workspace name is required", "name is required")
		}
		projectInput := domain.WorkspaceProjectInput{
			ExecutionHostID: req.Project.ExecutionHostID, MountAlias: req.Project.MountAlias,
			MountGeneration: req.Project.MountGeneration, RepositoryIdentity: req.Project.RepositoryIdentity,
		}
		if err := projectInput.Validate(); err != nil {
			return problemBytes(err)
		}
		if projectInput.ExecutionHostID != domain.LocalHostID {
			return renderProblem(http.StatusUnprocessableEntity, "workspace_project_unavailable",
				"Project host unavailable", "U01 只允许绑定当前本机 Host")
		}
		if s.hostRegistry == nil {
			return renderProblem(http.StatusUnprocessableEntity, hostregistry.CodeMountNotAdvertised,
				"Project mount unavailable", "trusted local project registry is not configured")
		}
		mount, err := s.store.ExecutionHosts().GetMount(r.Context(), projectInput.ExecutionHostID, projectInput.MountAlias)
		if err != nil {
			return problemBytes(err)
		}
		if mount.Status != domain.MountStatusReady || mount.RegistryGeneration != projectInput.MountGeneration ||
			mount.RepositoryIdentity != projectInput.RepositoryIdentity {
			return renderProblem(http.StatusConflict, hostregistry.CodeGenerationChanged,
				"Project mount changed", "project mount identity or generation is stale")
		}
		mountKey, err := s.hostRegistry.ProjectCanonicalKey(r.Context(), projectInput.MountAlias,
			projectInput.MountGeneration, projectInput.RepositoryIdentity)
		if err != nil {
			return problemBytes(err)
		}
		canonicalKey := projectInput.ExecutionHostID + ":" + mountKey
		if existing, lookupErr := s.store.WorkspaceProjects().GetByCanonicalKey(r.Context(), canonicalKey); lookupErr == nil {
			if existing.RepositoryIdentity != projectInput.RepositoryIdentity {
				return renderProblem(http.StatusConflict, "workspace_project_unavailable",
					"Project identity changed", "同一目录当前对应的 repository identity 已变化")
			}
			if setupErr := s.reconcileWorkspaceSetup(r.Context(), existing); setupErr != nil {
				// The existing Workspace remains the durable identity; expose its
				// failed/pending setup state so a new idempotency key can retry it.
				if refreshed, getErr := s.store.WorkspaceProjects().Get(r.Context(), existing.WorkspaceID); getErr == nil {
					existing = refreshed
				}
			}
			ws, getErr := s.store.Workspaces().Get(r.Context(), existing.WorkspaceID)
			if getErr != nil {
				return problemBytes(getErr)
			}
			dto, dtoErr := s.workspaceDTO(r.Context(), ws)
			if dtoErr != nil {
				return problemBytes(dtoErr)
			}
			return renderJSON(w, r, http.StatusOK, workspaceResponse{workspaceDTO: *dto, Reused: true})
		} else if !errors.Is(lookupErr, domain.ErrNotFound) {
			return problemBytes(lookupErr)
		}
		if adopted, adoptErr := s.adoptLegacyWorkspaceProject(r.Context(), canonicalKey, projectInput); adoptErr != nil {
			return problemBytes(adoptErr)
		} else if adopted != nil {
			dto, dtoErr := s.workspaceDTO(r.Context(), adopted)
			if dtoErr != nil {
				return problemBytes(dtoErr)
			}
			return renderJSON(w, r, http.StatusOK, workspaceResponse{workspaceDTO: *dto, Reused: true})
		}
		if strings.TrimSpace(req.SourceWorkspaceID) != "" {
			if _, err := s.store.Workspaces().Get(r.Context(), req.SourceWorkspaceID); err != nil {
				return problemBytes(fmt.Errorf("%w: source workspace unavailable", domain.ErrValidation))
			}
		}
		if ids, err := s.store.Workspaces().ListIDs(r.Context()); err != nil {
			return problemBytes(err)
		} else if len(ids) == 1 {
			if preparer, ok := s.agentCfg.(agentConfigWorkspacePreparer); ok {
				if err := preparer.PrepareWorkspaceScope(r.Context(), ids[0]); err != nil {
					return s.agentConfigIntentFailure(err)
				}
			}
		}
		workspaceID := domain.NewID(domain.PrefixWorkspace)
		now := time.Now().UTC()
		locationID := domain.NewID(domain.PrefixWorkspaceLocation)
		ws := &domain.Workspace{ID: workspaceID, Name: strings.TrimSpace(req.Name), Timezone: strings.TrimSpace(req.Timezone), Version: 1, CreatedAt: now, UpdatedAt: now}
		if ws.Timezone == "" {
			ws.Timezone = "UTC"
		}
		location := &domain.WorkspaceLocation{ID: locationID, WorkspaceID: workspaceID,
			ExecutionHostID: projectInput.ExecutionHostID, MountAlias: projectInput.MountAlias,
			MountGeneration: projectInput.MountGeneration, RepositoryIdentity: projectInput.RepositoryIdentity,
			IsDefault: true, Status: domain.LocationReady, Version: 1, CreatedAt: now, UpdatedAt: now}
		project := &domain.WorkspaceProject{WorkspaceID: workspaceID, ExecutionHostID: projectInput.ExecutionHostID,
			MountAlias: projectInput.MountAlias, MountGeneration: projectInput.MountGeneration,
			RepositoryIdentity: projectInput.RepositoryIdentity, CanonicalKey: canonicalKey,
			LocationID: locationID, Status: domain.WorkspaceProjectPending, SourceWorkspaceID: req.SourceWorkspaceID,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		result, err := s.svc.CreateWorkspaceWithProject(r.Context(), application.CreateWorkspaceParams{
			Workspace: ws, Project: project, Location: location, SourceWorkspaceID: req.SourceWorkspaceID,
		})
		if err != nil {
			if errors.Is(err, domain.ErrIdempotencyConflict) {
				if existing, lookupErr := s.store.WorkspaceProjects().GetByCanonicalKey(r.Context(), canonicalKey); lookupErr == nil {
					if existingWS, getErr := s.store.Workspaces().Get(r.Context(), existing.WorkspaceID); getErr == nil {
						dto, dtoErr := s.workspaceDTO(r.Context(), existingWS)
						if dtoErr == nil {
							return renderJSON(w, r, http.StatusOK, workspaceResponse{workspaceDTO: *dto, Reused: true})
						}
					}
				}
			}
			return problemBytes(err)
		}
		if len(result.Agents) > 0 {
			for _, agent := range result.Agents {
				if durable, ok := s.agentCfg.(AgentConfigIntentSync); ok {
					if syncErr := durable.ReconcileAgent(r.Context(), agent.ID); syncErr != nil {
						if statusErr := s.store.WorkspaceProjects().UpdateStatus(r.Context(), workspaceID, domain.WorkspaceProjectSetupFailed, "Agent 配置尚未可靠落盘，请执行配置恢复", project.Version); statusErr != nil {
							return problemBytes(statusErr)
						}
						dto, _ := s.workspaceDTO(r.Context(), ws)
						if dto != nil {
							return renderJSON(w, r, http.StatusAccepted, workspaceResponse{workspaceDTO: *dto})
						}
						return s.agentConfigIntentFailure(syncErr)
					}
				} else {
					if statusErr := s.store.WorkspaceProjects().UpdateStatus(r.Context(), workspaceID, domain.WorkspaceProjectSetupFailed, "Agent 配置同步服务未挂载", project.Version); statusErr != nil {
						return problemBytes(statusErr)
					}
					dto, dtoErr := s.workspaceDTO(r.Context(), ws)
					if dtoErr != nil {
						return problemBytes(dtoErr)
					}
					return renderJSON(w, r, http.StatusAccepted, workspaceResponse{workspaceDTO: *dto})
				}
			}
			if current, getErr := s.store.WorkspaceProjects().Get(r.Context(), workspaceID); getErr == nil {
				if statusErr := s.store.WorkspaceProjects().UpdateStatus(r.Context(), workspaceID, domain.WorkspaceProjectReady, "", current.Version); statusErr != nil {
					return problemBytes(statusErr)
				}
			} else {
				return problemBytes(getErr)
			}
		} else {
			if statusErr := s.store.WorkspaceProjects().UpdateStatus(r.Context(), workspaceID, domain.WorkspaceProjectReady, "", project.Version); statusErr != nil {
				return problemBytes(statusErr)
			}
		}
		dto, err := s.workspaceDTO(r.Context(), ws)
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, workspaceResponse{workspaceDTO: *dto})
	})
}

// adoptLegacyWorkspaceProject closes the migration gap for databases created
// before workspace_projects existed. It adopts exactly one ready default
// location whose trusted realpath matches the requested project; ambiguous
// legacy multi-location data remains untouched and cannot be guessed.
func (s *Server) adoptLegacyWorkspaceProject(ctx context.Context, canonicalKey string,
	input domain.WorkspaceProjectInput) (*domain.Workspace, error) {
	ids, err := s.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []*domain.WorkspaceLocation
	for _, workspaceID := range ids {
		locations, listErr := s.store.WorkspaceLocations().ListByWorkspace(ctx, workspaceID)
		if listErr != nil {
			return nil, listErr
		}
		if len(locations) != 1 {
			// Legacy multi-location workspaces need explicit migration; choosing
			// one location would silently reassign their historical data. If one
			// of those locations is this directory, block a duplicate Workspace.
			for _, location := range locations {
				if location.ExecutionHostID != input.ExecutionHostID {
					continue
				}
				key, keyErr := s.hostRegistry.ProjectCanonicalKey(ctx, location.MountAlias, location.MountGeneration, location.RepositoryIdentity)
				if keyErr == nil && input.ExecutionHostID+":"+key == canonicalKey {
					return nil, fmt.Errorf("%w: legacy Workspace has multiple locations for this project; migrate explicitly before creating another Workspace", domain.ErrStateConflict)
				}
			}
			continue
		}
		for _, location := range locations {
			if !location.IsDefault || location.ExecutionHostID != input.ExecutionHostID || location.Status != domain.LocationReady {
				continue
			}
			key, keyErr := s.hostRegistry.ProjectCanonicalKey(ctx, location.MountAlias, location.MountGeneration, location.RepositoryIdentity)
			if keyErr == nil && input.ExecutionHostID+":"+key == canonicalKey {
				candidates = append(candidates, location)
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("%w: 同一项目目录已有多个历史 Workspace 绑定，无法猜测归属", domain.ErrIdempotencyConflict)
	}
	location := candidates[0]
	workspace, err := s.store.Workspaces().Get(ctx, location.WorkspaceID)
	if err != nil {
		return nil, err
	}
	project := &domain.WorkspaceProject{WorkspaceID: workspace.ID, ExecutionHostID: location.ExecutionHostID,
		MountAlias: location.MountAlias, MountGeneration: location.MountGeneration,
		RepositoryIdentity: location.RepositoryIdentity, CanonicalKey: canonicalKey, LocationID: location.ID,
		Status: domain.WorkspaceProjectReady, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.store.WorkspaceProjects().Create(ctx, project); err != nil && !errors.Is(err, domain.ErrIdempotencyConflict) {
		return nil, err
	}
	return workspace, nil
}

func (s *Server) reconcileWorkspaceSetup(ctx context.Context, project *domain.WorkspaceProject) error {
	if project == nil || (project.Status != domain.WorkspaceProjectPending && project.Status != domain.WorkspaceProjectSetupFailed) {
		return nil
	}
	durable, ok := s.agentCfg.(AgentConfigIntentSync)
	if !ok {
		return fmt.Errorf("%w: Agent 配置恢复服务未挂载", domain.ErrCapabilityMissing)
	}
	agents, err := s.store.Agents().List(ctx, project.WorkspaceID)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if agent.Kind.IsSystem() {
			continue
		}
		if err := durable.ReconcileAgent(ctx, agent.ID); err != nil {
			if current, getErr := s.store.WorkspaceProjects().Get(ctx, project.WorkspaceID); getErr == nil {
				_ = s.store.WorkspaceProjects().UpdateStatus(ctx, project.WorkspaceID, domain.WorkspaceProjectSetupFailed, "Agent 配置尚未可靠落盘，请执行配置恢复", current.Version)
			}
			return err
		}
	}
	current, err := s.store.WorkspaceProjects().Get(ctx, project.WorkspaceID)
	if err != nil {
		return err
	}
	return s.store.WorkspaceProjects().UpdateStatus(ctx, project.WorkspaceID, domain.WorkspaceProjectReady, "", current.Version)
}

func (s *Server) workspaceDTO(ctx context.Context, ws *domain.Workspace) (*workspaceDTO, error) {
	dto := toWorkspaceDTO(ws)
	project, err := s.store.WorkspaceProjects().Get(ctx, ws.ID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}
	dto.Project = toWorkspaceProjectDTO(project)
	if project != nil {
		if s.hostRegistry == nil {
			project.Status = domain.WorkspaceProjectUnavailable
			project.Error = "trusted local project registry is not configured"
		} else if mount, mountErr := s.store.ExecutionHosts().GetMount(ctx, project.ExecutionHostID, project.MountAlias); mountErr != nil ||
			mount.Status != domain.MountStatusReady || mount.RegistryGeneration != project.MountGeneration ||
			mount.RepositoryIdentity != project.RepositoryIdentity {
			project.Status = domain.WorkspaceProjectUnavailable
			project.Error = "项目目录或 HostMount 当前不可用"
		} else if _, keyErr := s.hostRegistry.ProjectCanonicalKey(ctx, project.MountAlias, project.MountGeneration, project.RepositoryIdentity); keyErr != nil {
			project.Status = domain.WorkspaceProjectUnavailable
			project.Error = "项目目录当前无法核验"
		}
		dto.Project = toWorkspaceProjectDTO(project)
		agents, agentErr := s.store.Agents().List(ctx, ws.ID)
		if agentErr != nil {
			return nil, agentErr
		}
		setupStatus := workspaceSetupStatus(project.Status)
		dto.Setup = &workspaceSetupDTO{Status: setupStatus, SourceWorkspaceID: project.SourceWorkspaceID, AgentCount: countUserAgents(agents)}
	}
	return &dto, nil
}

func workspaceSetupStatus(status domain.WorkspaceProjectStatus) string {
	switch status {
	case domain.WorkspaceProjectPending:
		return "pending"
	case domain.WorkspaceProjectReady:
		return "ready"
	default:
		return "failed"
	}
}

func countUserAgents(agents []*domain.AgentProfile) int {
	count := 0
	for _, agent := range agents {
		if agent != nil && !agent.Kind.IsSystem() {
			count++
		}
	}
	return count
}
