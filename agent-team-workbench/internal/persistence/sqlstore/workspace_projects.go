package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

type WorkspaceProjectRepo struct{ store *Store }

var _ application.WorkspaceProjectRepo = (*WorkspaceProjectRepo)(nil)

const workspaceProjectCols = `workspace_id, execution_host_id, mount_alias, mount_generation,
	repository_identity, canonical_key, location_id, status, setup_error, source_workspace_id,
	version, created_at, updated_at`

func scanWorkspaceProject(row interface{ Scan(...any) error }) (*domain.WorkspaceProject, error) {
	p := &domain.WorkspaceProject{}
	var source *string
	var created, updated scanTime
	if err := row.Scan(&p.WorkspaceID, &p.ExecutionHostID, &p.MountAlias, &p.MountGeneration,
		&p.RepositoryIdentity, &p.CanonicalKey, &p.LocationID, &p.Status, &p.Error, &source,
		&p.Version, &created, &updated); err != nil {
		return nil, err
	}
	if source != nil {
		p.SourceWorkspaceID = *source
	}
	p.CreatedAt, p.UpdatedAt = mustTime(created), mustTime(updated)
	return p, nil
}

func (r *WorkspaceProjectRepo) Create(ctx context.Context, project *domain.WorkspaceProject) error {
	if err := project.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO workspace_projects(`+workspaceProjectCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		project.WorkspaceID, project.ExecutionHostID, project.MountAlias, project.MountGeneration,
		project.RepositoryIdentity, project.CanonicalKey, project.LocationID, project.Status,
		project.Error, nullString(project.SourceWorkspaceID), project.Version,
		timeParam(project.CreatedAt), timeParam(project.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *WorkspaceProjectRepo) Get(ctx context.Context, workspaceID string) (*domain.WorkspaceProject, error) {
	p, err := scanWorkspaceProject(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+workspaceProjectCols+` FROM workspace_projects WHERE workspace_id=?`, workspaceID))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return p, nil
}

func (r *WorkspaceProjectRepo) GetByCanonicalKey(ctx context.Context, canonicalKey string) (*domain.WorkspaceProject, error) {
	if strings.TrimSpace(canonicalKey) == "" {
		return nil, fmt.Errorf("%w: canonical key required", domain.ErrValidation)
	}
	p, err := scanWorkspaceProject(r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+workspaceProjectCols+` FROM workspace_projects WHERE canonical_key=?`, canonicalKey))
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return p, nil
}

func (r *WorkspaceProjectRepo) List(ctx context.Context) ([]*domain.WorkspaceProject, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+workspaceProjectCols+` FROM workspace_projects ORDER BY created_at, workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.WorkspaceProject
	for rows.Next() {
		p, err := scanWorkspaceProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *WorkspaceProjectRepo) UpdateStatus(ctx context.Context, workspaceID string,
	status domain.WorkspaceProjectStatus, message string, expectedVersion int) error {
	if !status.Valid() {
		return fmt.Errorf("%w: invalid workspace project status", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE workspace_projects SET status=?, setup_error=?, version=version+1, updated_at=?
		 WHERE workspace_id=? AND version=?`, status, message, timeParam(timeNow()), workspaceID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *WorkspaceProjectRepo) UpdateIdentity(ctx context.Context, workspaceID, repositoryIdentity,
	mountGeneration string, expectedVersion int) error {
	if strings.TrimSpace(repositoryIdentity) == "" || strings.TrimSpace(mountGeneration) == "" {
		return fmt.Errorf("%w: project identity and generation required", domain.ErrValidation)
	}
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE workspace_projects SET repository_identity=?, mount_generation=?, version=version+1,
		 updated_at=? WHERE workspace_id=? AND version=?`, repositoryIdentity, mountGeneration,
		timeParam(timeNow()), workspaceID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}
