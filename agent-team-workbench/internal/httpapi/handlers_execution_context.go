// handlers_execution_context.go 承载执行上下文系 REST 端点（任务控制面
// RFC §9.1–9.3）：Execution Host enrollment、mount 广告投影、Workspace
// Location 绑定与 probe、DevelopmentContext 读改、Run 快照只读投影。
// 全部端点不携带、不返回任何宿主绝对路径；越权 path/cwd 字段 403 fail closed。
package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// executionHostDTO 稳定宿主身份（enrollment_ref 受限存储，永不返回）。
type executionHostDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toExecutionHostDTO(h *domain.ExecutionHost) executionHostDTO {
	return executionHostDTO{
		ID: h.ID, Name: h.Name, Kind: string(h.Kind), Status: string(h.Status),
		Version: h.Version, CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}

// hostMountDTO mount 广告投影（无绝对路径）。
type hostMountDTO struct {
	ExecutionHostID    string                 `json:"execution_host_id"`
	Alias              string                 `json:"alias"`
	RepositoryIdentity string                 `json:"repository_identity"`
	DisplayLabel       string                 `json:"display_label,omitempty"`
	DefaultBranch      string                 `json:"default_branch,omitempty"`
	SupportedRefKinds  []domain.RefKind       `json:"supported_ref_kinds,omitempty"`
	Checkouts          []domain.MountCheckout `json:"checkouts,omitempty"`
	RegistryGeneration string                 `json:"registry_generation"`
	Status             string                 `json:"status"`
	LastSeenAt         *time.Time             `json:"last_seen_at,omitempty"`
}

func toHostMountDTO(m *domain.HostMount) hostMountDTO {
	var lastSeen *time.Time
	if !m.LastSeenAt.IsZero() {
		t := m.LastSeenAt
		lastSeen = &t
	}
	return hostMountDTO{
		ExecutionHostID: m.ExecutionHostID, Alias: m.Alias,
		RepositoryIdentity: m.RepositoryIdentity, DisplayLabel: m.DisplayLabel,
		DefaultBranch: m.DefaultBranch, SupportedRefKinds: m.SupportedRefKinds,
		Checkouts: m.Checkouts, RegistryGeneration: m.RegistryGeneration,
		Status: string(m.Status), LastSeenAt: lastSeen,
	}
}

type workspaceLocationDTO struct {
	ID                 string    `json:"id"`
	WorkspaceID        string    `json:"workspace_id"`
	ExecutionHostID    string    `json:"execution_host_id"`
	MountAlias         string    `json:"mount_alias"`
	MountGeneration    string    `json:"mount_generation"`
	RepositoryIdentity string    `json:"repository_identity"`
	IsDefault          bool      `json:"is_default"`
	Status             string    `json:"status"`
	Version            int       `json:"version"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func toWorkspaceLocationDTO(l *domain.WorkspaceLocation) workspaceLocationDTO {
	return workspaceLocationDTO{
		ID: l.ID, WorkspaceID: l.WorkspaceID, ExecutionHostID: l.ExecutionHostID,
		MountAlias: l.MountAlias, MountGeneration: l.MountGeneration,
		RepositoryIdentity: l.RepositoryIdentity, IsDefault: l.IsDefault,
		Status: string(l.Status), Version: l.Version,
		CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
}

type developmentContextDTO struct {
	WorkItemID          string    `json:"work_item_id"`
	ContextOwnerID      string    `json:"context_owner_id,omitempty"`
	WorkspaceLocationID string    `json:"workspace_location_id,omitempty"`
	RefKind             string    `json:"ref_kind"`
	BranchName          string    `json:"branch_name"`
	CheckoutRef         string    `json:"checkout_ref"`
	WorktreeRef         string    `json:"worktree_ref"`
	BaseRevision        string    `json:"base_revision"`
	Version             int       `json:"version"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func toDevelopmentContextDTO(c *domain.DevelopmentContext) developmentContextDTO {
	return developmentContextDTO{
		WorkItemID: c.WorkItemID, ContextOwnerID: c.ContextOwnerID,
		WorkspaceLocationID: c.WorkspaceLocationID, RefKind: string(c.RefKind),
		BranchName: c.BranchName, CheckoutRef: c.CheckoutRef, WorktreeRef: c.WorktreeRef,
		BaseRevision: c.BaseRevision, Version: c.Version,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type executionContextSnapshotDTO struct {
	ID                  string            `json:"id"`
	RunID               string            `json:"run_id"`
	SchemaVersion       string            `json:"schema_version"`
	WorkspaceID         string            `json:"workspace_id"`
	WorkspaceLocationID string            `json:"workspace_location_id"`
	LocationVersion     int               `json:"location_version"`
	MountGeneration     string            `json:"mount_generation"`
	ExecutionHostID     string            `json:"execution_host_id"`
	MountAlias          string            `json:"mount_alias"`
	RepositoryIdentity  string            `json:"repository_identity"`
	RefKind             string            `json:"ref_kind"`
	BranchName          string            `json:"branch_name"`
	CheckoutRef         string            `json:"checkout_ref"`
	WorktreeRef         string            `json:"worktree_ref"`
	BaseRevision        string            `json:"base_revision"`
	ContextGeneration   int               `json:"context_generation"`
	Source              string            `json:"source"`
	SourceSnapshotID    string            `json:"source_snapshot_id,omitempty"`
	SnapshotDigest      string            `json:"snapshot_digest"`
	HostMountStatus     map[string]string `json:"host_mount_status,omitempty"`
	CreatedAt           time.Time         `json:"created_at"`
}

func toExecutionContextSnapshotDTO(snap *domain.ExecutionContextSnapshot, view *application.RunExecutionContext) executionContextSnapshotDTO {
	out := executionContextSnapshotDTO{
		ID: snap.ID, RunID: snap.RunID, SchemaVersion: snap.SchemaVersion,
		WorkspaceID: snap.WorkspaceID, WorkspaceLocationID: snap.WorkspaceLocationID,
		LocationVersion: snap.LocationVersion, MountGeneration: snap.MountGeneration,
		ExecutionHostID: snap.ExecutionHostID, MountAlias: snap.MountAlias,
		RepositoryIdentity: snap.RepositoryIdentity, RefKind: string(snap.RefKind),
		BranchName: snap.BranchName, CheckoutRef: snap.CheckoutRef, WorktreeRef: snap.WorktreeRef,
		BaseRevision: snap.BaseRevision, ContextGeneration: snap.ContextGeneration,
		Source: string(snap.Source), SourceSnapshotID: snap.SourceSnapshotID,
		SnapshotDigest: snap.SnapshotDigest,
		CreatedAt:      snap.CreatedAt,
	}
	if view != nil {
		out.HostMountStatus = map[string]string{
			"execution_host_status": string(view.HostStatus),
			"mount_status":          string(view.MountStatus),
			"location_status":       string(view.LocationStatus),
		}
	}
	return out
}

// ── Execution Host（RFC §9.1）────────────────────────────────────────

func (s *Server) handleListExecutionHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.svc.ListExecutionHosts(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]executionHostDTO, 0, len(hosts))
	for _, h := range hosts {
		items = append(items, toExecutionHostDTO(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// pathForbiddenBytes fail closed：请求体携带 path/cwd 类字段时 403
// workspace_path_forbidden（RFC §9.1/§9.2：schema additionalProperties 之外的
// 红线字段）。在幂等闭包内调用（r.Body 已被 idempotent 读出并复位）。
func pathForbiddenBytes(r *http.Request) (int, []byte, bool) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err == nil {
		for _, key := range []string{"path", "cwd", "root_path", "absolute_path", "workspace_root"} {
			if _, ok := raw[key]; ok {
				b, _ := json.Marshal(Problem{
					Type:  "https://workbench.example/problems/workspace_path_forbidden",
					Title: "Workspace path forbidden", Status: http.StatusForbidden,
					Code:   "workspace_path_forbidden",
					Detail: "请求不接受 " + key + " 字段：Workspace 不等于路径，宿主绝对路径永不进入 API",
				})
				return http.StatusForbidden, b, true
			}
		}
	}
	return 0, nil, false
}

func (s *Server) handleCreateExecutionHost(w http.ResponseWriter, r *http.Request) {
	s.idempotent(w, r, "execution-hosts", func() (int, []byte) {
		var req struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		}
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		kind := domain.HostKind(req.Kind)
		if kind == "" {
			kind = domain.HostKindRemote
		}
		host, credential, err := s.svc.CreateExecutionHost(r.Context(), application.CreateExecutionHostParams{
			Name: req.Name, Kind: kind,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, map[string]any{
			"host": toExecutionHostDTO(host),
			// enrollment credential 明文只在本次响应出现；服务端只存 sha256 hex。
			"enrollment_credential": credential,
		})
	})
}

func (s *Server) handleRotateHostCredential(w http.ResponseWriter, r *http.Request) {
	s.idempotent(w, r, r.PathValue("host_id"), func() (int, []byte) {
		host, credential, err := s.svc.RotateExecutionHostCredential(r.Context(), r.PathValue("host_id"))
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, map[string]any{
			"host":                  toExecutionHostDTO(host),
			"enrollment_credential": credential,
		})
	})
}

func (s *Server) handleListHostMounts(w http.ResponseWriter, r *http.Request) {
	mounts, err := s.svc.ListHostMounts(r.Context(), r.PathValue("host_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]hostMountDTO, 0, len(mounts))
	for _, m := range mounts {
		items = append(items, toHostMountDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ── WorkspaceLocation（RFC §9.1）─────────────────────────────────────

func (s *Server) handleListWorkspaceLocations(w http.ResponseWriter, r *http.Request) {
	locations, err := s.svc.ListWorkspaceLocations(r.Context(), r.PathValue("workspace_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	items := make([]workspaceLocationDTO, 0, len(locations))
	for _, l := range locations {
		items = append(items, toWorkspaceLocationDTO(l))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateWorkspaceLocation(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.PathValue("workspace_id")
	s.idempotent(w, r, workspaceID, func() (int, []byte) {
		if status, body, forbidden := pathForbiddenBytes(r); forbidden {
			return status, body
		}
		var req struct {
			ExecutionHostID    string `json:"execution_host_id"`
			MountAlias         string `json:"mount_alias"`
			RepositoryIdentity string `json:"repository_identity"`
			MountGeneration    string `json:"mount_generation"`
			IsDefault          bool   `json:"is_default"`
		}
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		loc, err := s.svc.CreateWorkspaceLocation(r.Context(), workspaceID, application.CreateWorkspaceLocationParams{
			ExecutionHostID:    req.ExecutionHostID,
			MountAlias:         req.MountAlias,
			RepositoryIdentity: req.RepositoryIdentity,
			MountGeneration:    req.MountGeneration,
			IsDefault:          req.IsDefault,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusCreated, toWorkspaceLocationDTO(loc))
	})
}

func (s *Server) handleUpdateWorkspaceLocation(w http.ResponseWriter, r *http.Request) {
	locationID := r.PathValue("location_id")
	s.idempotent(w, r, locationID, func() (int, []byte) {
		if status, body, forbidden := pathForbiddenBytes(r); forbidden {
			return status, body
		}
		var req struct {
			RepositoryIdentity string `json:"repository_identity"`
			MountGeneration    string `json:"mount_generation"`
			IsDefault          *bool  `json:"is_default"`
			ExpectedVersion    int    `json:"expected_version"`
		}
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		loc, err := s.svc.UpdateWorkspaceLocation(r.Context(), locationID, application.UpdateWorkspaceLocationParams{
			RepositoryIdentity: req.RepositoryIdentity,
			MountGeneration:    req.MountGeneration,
			IsDefault:          req.IsDefault,
			ExpectedVersion:    req.ExpectedVersion,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toWorkspaceLocationDTO(loc))
	})
}

func (s *Server) handleProbeWorkspaceLocation(w http.ResponseWriter, r *http.Request) {
	s.idempotent(w, r, r.PathValue("location_id"), func() (int, []byte) {
		result, err := s.svc.ProbeWorkspaceLocation(r.Context(), r.PathValue("location_id"))
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusAccepted, map[string]any{
			"location_id": result.LocationID,
			"status":      string(result.Status),
			"detail":      nullableString(result.Detail),
			"checked_at":  result.CheckedAt,
		})
	})
}

// ── Development Context（RFC §9.2）───────────────────────────────────

func (s *Server) handleGetDevelopmentContext(w http.ResponseWriter, r *http.Request) {
	c, err := s.svc.GetDevelopmentContext(r.Context(), r.PathValue("work_item_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDevelopmentContextDTO(c))
}

func (s *Server) handleSetDevelopmentContext(w http.ResponseWriter, r *http.Request) {
	workItemID := r.PathValue("work_item_id")
	s.idempotent(w, r, workItemID, func() (int, []byte) {
		if status, body, forbidden := pathForbiddenBytes(r); forbidden {
			return status, body
		}
		var req struct {
			WorkspaceLocationID string `json:"workspace_location_id"`
			RefKind             string `json:"ref_kind"`
			BranchName          string `json:"branch_name"`
			CheckoutRef         string `json:"checkout_ref"`
			WorktreeRef         string `json:"worktree_ref"`
			BaseRevision        string `json:"base_revision"`
			ExpectedVersion     int    `json:"expected_version"`
		}
		if err := decodeBody(r, &req); err != nil {
			return renderProblem(http.StatusBadRequest, "bad_request", "Invalid request body", err.Error())
		}
		c, err := s.svc.SetDevelopmentContext(r.Context(), workItemID, application.SetDevelopmentContextParams{
			WorkspaceLocationID: req.WorkspaceLocationID,
			RefKind:             domain.RefKind(req.RefKind),
			BranchName:          req.BranchName,
			CheckoutRef:         req.CheckoutRef,
			WorktreeRef:         req.WorktreeRef,
			BaseRevision:        req.BaseRevision,
			ExpectedVersion:     req.ExpectedVersion,
		})
		if err != nil {
			return problemBytes(err)
		}
		return renderJSON(w, r, http.StatusOK, toDevelopmentContextDTO(c))
	})
}

// ── Run 快照只读投影（RFC §9.3）──────────────────────────────────────

func (s *Server) handleGetRunExecutionContext(w http.ResponseWriter, r *http.Request) {
	view, err := s.svc.GetRunExecutionContext(r.Context(), r.PathValue("run_id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	// 只读返回逻辑 Snapshot + 健康投影；绝对 cwd 不进入普通 DTO（RFC §9.3）。
	writeJSON(w, http.StatusOK, toExecutionContextSnapshotDTO(view.Snapshot, view))
}
