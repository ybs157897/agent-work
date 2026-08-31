package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// execution_contexts.go 承载任务控制面 RFC §4 的执行上下文四仓储：
// ExecutionHost（宿主身份 + mount 广告投影）、WorkspaceLocation（业务绑定）、
// WorkItemContext（DevelopmentContext）、ContextSnapshot（不可变快照）。

type ExecutionHostRepo struct{ store *Store }

var _ application.ExecutionHostRepo = (*ExecutionHostRepo)(nil)

// EnsureLocalHost 幂等确保受保护本机 Host（domain.LocalHostID）。与本机
// bootstrap 迁移（0021）同形状：name=local、kind=local、status=ready、空
// enrollment_ref。已存在时不覆盖既有行（hello/启动重复调用安全）。
func (r *ExecutionHostRepo) EnsureLocalHost(ctx context.Context, now time.Time) (*domain.ExecutionHost, error) {
	if _, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO execution_hosts(id, name, kind, status, enrollment_ref, version, created_at, updated_at)
		 VALUES (?,?,?,?,?,1,?,?) ON CONFLICT (id) DO NOTHING`,
		domain.LocalHostID, "local", domain.HostKindLocal, domain.HostStatusReady, "",
		timeParam(now), timeParam(now)); err != nil {
		return nil, r.store.mapErr(err)
	}
	return r.Get(ctx, domain.LocalHostID)
}

func (r *ExecutionHostRepo) Get(ctx context.Context, id string) (*domain.ExecutionHost, error) {
	h := &domain.ExecutionHost{}
	var enrollment *string
	var created, updated scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT id, name, kind, status, enrollment_ref, version, created_at, updated_at
		 FROM execution_hosts WHERE id=?`, id).
		Scan(&h.ID, &h.Name, &h.Kind, &h.Status, &enrollment, &h.Version, &created, &updated)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	if enrollment != nil {
		h.EnrollmentRef = *enrollment
	}
	h.CreatedAt, h.UpdatedAt = mustTime(created), mustTime(updated)
	return h, nil
}

// Create 仅受保护 enrollment 命令调用；ID 冲突映射 ErrIdempotencyConflict。
func (r *ExecutionHostRepo) Create(ctx context.Context, h *domain.ExecutionHost) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO execution_hosts(id, name, kind, status, enrollment_ref, version, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		h.ID, h.Name, h.Kind, h.Status, h.EnrollmentRef, h.Version,
		timeParam(h.CreatedAt), timeParam(h.UpdatedAt))
	return r.store.mapErr(err)
}

// Update 乐观锁写回身份/健康字段；0 行 = 版本冲突（既有风格不区分不存在行）。
func (r *ExecutionHostRepo) Update(ctx context.Context, h *domain.ExecutionHost, expectedVersion int) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE execution_hosts SET name=?, kind=?, status=?, enrollment_ref=?,
		 version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		h.Name, h.Kind, h.Status, h.EnrollmentRef, timeParam(timeNow()), h.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *ExecutionHostRepo) List(ctx context.Context) ([]*domain.ExecutionHost, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT id, name, kind, status, enrollment_ref, version, created_at, updated_at
		 FROM execution_hosts ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.ExecutionHost
	for rows.Next() {
		h := &domain.ExecutionHost{}
		var enrollment *string
		var created, updated scanTime
		if err := rows.Scan(&h.ID, &h.Name, &h.Kind, &h.Status, &enrollment, &h.Version, &created, &updated); err != nil {
			return nil, err
		}
		if enrollment != nil {
			h.EnrollmentRef = *enrollment
		}
		h.CreatedAt, h.UpdatedAt = mustTime(created), mustTime(updated)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *ExecutionHostRepo) SetStatus(ctx context.Context, id string, status domain.HostStatus, at time.Time) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE execution_hosts SET status=?, updated_at=? WHERE id=?`,
		status, timeParam(at), id)
	return r.store.mapErr(err)
}

// UpsertMount 按 (host, alias) 覆盖广告投影（hello 上报换代，全列覆盖；
// checkouts/supported_ref_kinds 存 JSON 数组文本，nil 归一为 []）。
func (r *ExecutionHostRepo) UpsertMount(ctx context.Context, m *domain.HostMount) error {
	kinds := m.SupportedRefKinds
	if kinds == nil {
		kinds = []domain.RefKind{}
	}
	checkouts := m.Checkouts
	if checkouts == nil {
		checkouts = []domain.MountCheckout{}
	}
	var lastSeen any
	if !m.LastSeenAt.IsZero() {
		lastSeen = timeParam(m.LastSeenAt)
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO execution_host_mounts(execution_host_id, alias, repository_identity, display_label,
		 default_branch, supported_ref_kinds, checkouts, registry_generation, status, last_seen_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (execution_host_id, alias) DO UPDATE SET
			repository_identity=excluded.repository_identity, display_label=excluded.display_label,
			default_branch=excluded.default_branch, supported_ref_kinds=excluded.supported_ref_kinds,
			checkouts=excluded.checkouts, registry_generation=excluded.registry_generation,
			status=excluded.status, last_seen_at=excluded.last_seen_at`,
		m.ExecutionHostID, m.Alias, m.RepositoryIdentity, m.DisplayLabel,
		nullString(m.DefaultBranch), jsonText(kinds), jsonText(checkouts),
		m.RegistryGeneration, m.Status, lastSeen)
	return r.store.mapErr(err)
}

func scanMount(row interface{ Scan(...any) error }) (*domain.HostMount, error) {
	m := &domain.HostMount{}
	var displayLabel, defaultBranch, status *string
	var kinds, checkouts string
	var lastSeen scanTime
	if err := row.Scan(&m.ExecutionHostID, &m.Alias, &m.RepositoryIdentity, &displayLabel,
		&defaultBranch, &kinds, &checkouts, &m.RegistryGeneration, &status, &lastSeen); err != nil {
		return nil, err
	}
	if displayLabel != nil {
		m.DisplayLabel = *displayLabel
	}
	if defaultBranch != nil {
		m.DefaultBranch = *defaultBranch
	}
	// 广告投影是 hello 的结构化上报，解码失败说明存储被写坏：fail loud 不静默吞。
	if err := jsonInto(kinds, &m.SupportedRefKinds); err != nil {
		return nil, err
	}
	if err := jsonInto(checkouts, &m.Checkouts); err != nil {
		return nil, err
	}
	if status != nil {
		m.Status = domain.MountStatus(*status)
	}
	if lastSeen.Valid {
		m.LastSeenAt = mustTime(lastSeen)
	}
	return m, nil
}

func (r *ExecutionHostRepo) GetMount(ctx context.Context, hostID, alias string) (*domain.HostMount, error) {
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT execution_host_id, alias, repository_identity, display_label, default_branch,
		 supported_ref_kinds, checkouts, registry_generation, status, last_seen_at
		 FROM execution_host_mounts WHERE execution_host_id=? AND alias=?`, hostID, alias)
	m, err := scanMount(row)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return m, nil
}

func (r *ExecutionHostRepo) ListMounts(ctx context.Context, hostID string) ([]*domain.HostMount, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT execution_host_id, alias, repository_identity, display_label, default_branch,
		 supported_ref_kinds, checkouts, registry_generation, status, last_seen_at
		 FROM execution_host_mounts WHERE execution_host_id=? ORDER BY alias`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.HostMount
	for rows.Next() {
		m, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type WorkspaceLocationRepo struct{ store *Store }

var _ application.WorkspaceLocationRepo = (*WorkspaceLocationRepo)(nil)

const workspaceLocationCols = `id, workspace_id, execution_host_id, mount_alias, mount_generation,
	repository_identity, is_default, status, version, created_at, updated_at`

func scanWorkspaceLocation(row interface{ Scan(...any) error }) (*domain.WorkspaceLocation, error) {
	l := &domain.WorkspaceLocation{}
	var mountGeneration *string
	var isDefault *bool
	var created, updated scanTime
	if err := row.Scan(&l.ID, &l.WorkspaceID, &l.ExecutionHostID, &l.MountAlias, &mountGeneration,
		&l.RepositoryIdentity, &isDefault, &l.Status, &l.Version, &created, &updated); err != nil {
		return nil, err
	}
	if mountGeneration != nil {
		l.MountGeneration = *mountGeneration
	}
	if isDefault != nil {
		l.IsDefault = *isDefault
	}
	l.CreatedAt, l.UpdatedAt = mustTime(created), mustTime(updated)
	return l, nil
}

func (r *WorkspaceLocationRepo) Create(ctx context.Context, l *domain.WorkspaceLocation) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO workspace_locations(`+workspaceLocationCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		l.ID, l.WorkspaceID, l.ExecutionHostID, l.MountAlias, l.MountGeneration,
		l.RepositoryIdentity, l.IsDefault, l.Status, l.Version,
		timeParam(l.CreatedAt), timeParam(l.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *WorkspaceLocationRepo) Get(ctx context.Context, id string) (*domain.WorkspaceLocation, error) {
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+workspaceLocationCols+` FROM workspace_locations WHERE id=?`, id)
	l, err := scanWorkspaceLocation(row)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return l, nil
}

// Update 乐观锁写回（含 is_default——同一 Workspace 的默认唯一性由应用层保证，
// 仓储只忠实写入；并发切换撞 idx_workspace_locations_default 唯一索引映射冲突）。
func (r *WorkspaceLocationRepo) Update(ctx context.Context, l *domain.WorkspaceLocation, expectedVersion int) error {
	res, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE workspace_locations SET execution_host_id=?, mount_alias=?, mount_generation=?,
		 repository_identity=?, is_default=?, status=?, version=version+1, updated_at=?
		 WHERE id=? AND version=?`,
		l.ExecutionHostID, l.MountAlias, l.MountGeneration,
		l.RepositoryIdentity, l.IsDefault, l.Status, timeParam(timeNow()),
		l.ID, expectedVersion)
	if err != nil {
		return r.store.mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrVersionConflict
	}
	return nil
}

func (r *WorkspaceLocationRepo) ListByWorkspace(ctx context.Context, workspaceID string) ([]*domain.WorkspaceLocation, error) {
	rows, err := r.store.query(ctx, r.store.exec(ctx),
		`SELECT `+workspaceLocationCols+` FROM workspace_locations
		 WHERE workspace_id=? ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.WorkspaceLocation
	for rows.Next() {
		l, err := scanWorkspaceLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// DefaultFor 返回默认 Location；无默认返回 ErrNotFound（调用方映射
// workspace_location_required）。
func (r *WorkspaceLocationRepo) DefaultFor(ctx context.Context, workspaceID string) (*domain.WorkspaceLocation, error) {
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+workspaceLocationCols+` FROM workspace_locations
		 WHERE workspace_id=? AND is_default LIMIT 1`, workspaceID)
	l, err := scanWorkspaceLocation(row)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return l, nil
}

func (r *WorkspaceLocationRepo) SetStatus(ctx context.Context, id string, status domain.LocationStatus, at time.Time) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`UPDATE workspace_locations SET status=?, updated_at=? WHERE id=?`,
		status, timeParam(at), id)
	return r.store.mapErr(err)
}

type WorkItemContextRepo struct{ store *Store }

var _ application.WorkItemContextRepo = (*WorkItemContextRepo)(nil)

// Upsert 按 work_item_id 整体替换 DevelopmentContext（ref 变更 = 新行覆盖；
// created_at 保持首次创建）。ref 组合的域校验在应用层，CHECK 约束兜底。
func (r *WorkItemContextRepo) Upsert(ctx context.Context, c *domain.DevelopmentContext) error {
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO work_item_contexts(work_item_id, context_owner_id, workspace_location_id,
		 ref_kind, branch_name, checkout_ref, worktree_ref, base_revision, version, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (work_item_id) DO UPDATE SET
			context_owner_id=excluded.context_owner_id,
			workspace_location_id=excluded.workspace_location_id,
			ref_kind=excluded.ref_kind, branch_name=excluded.branch_name,
			checkout_ref=excluded.checkout_ref, worktree_ref=excluded.worktree_ref,
			base_revision=excluded.base_revision, version=excluded.version,
			updated_at=excluded.updated_at`,
		c.WorkItemID, c.ContextOwnerID, c.WorkspaceLocationID, c.RefKind,
		nullString(c.BranchName), nullString(c.CheckoutRef), nullString(c.WorktreeRef),
		nullString(c.BaseRevision), c.Version, timeParam(c.CreatedAt), timeParam(c.UpdatedAt))
	return r.store.mapErr(err)
}

func (r *WorkItemContextRepo) Get(ctx context.Context, workItemID string) (*domain.DevelopmentContext, error) {
	c := &domain.DevelopmentContext{}
	var branch, checkoutRef, worktreeRef, baseRevision *string
	var created, updated scanTime
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT work_item_id, context_owner_id, workspace_location_id, ref_kind, branch_name,
		 checkout_ref, worktree_ref, base_revision, version, created_at, updated_at
		 FROM work_item_contexts WHERE work_item_id=?`, workItemID).
		Scan(&c.WorkItemID, &c.ContextOwnerID, &c.WorkspaceLocationID, &c.RefKind, &branch,
			&checkoutRef, &worktreeRef, &baseRevision, &c.Version, &created, &updated)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	setStr := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setStr(&c.BranchName, branch)
	setStr(&c.CheckoutRef, checkoutRef)
	setStr(&c.WorktreeRef, worktreeRef)
	setStr(&c.BaseRevision, baseRevision)
	c.CreatedAt, c.UpdatedAt = mustTime(created), mustTime(updated)
	return c, nil
}

type ContextSnapshotRepo struct{ store *Store }

var _ application.ContextSnapshotRepo = (*ContextSnapshotRepo)(nil)

const ctxSnapshotCols = `id, run_id, schema_version, workspace_id, workspace_location_id,
	location_version, mount_generation, execution_host_id, mount_alias, repository_identity,
	ref_kind, branch_name, checkout_ref, worktree_ref, base_revision, context_generation,
	source, source_snapshot_id, snapshot_digest, created_at`

func scanSnapshot(row interface{ Scan(...any) error }) (*domain.ExecutionContextSnapshot, error) {
	s := &domain.ExecutionContextSnapshot{}
	var locationID, mountGen, hostID, mountAlias, repoIdentity *string
	var branch, checkoutRef, worktreeRef, baseRevision, sourceSnapshotID *string
	var locationVersion sql.NullInt64
	var created scanTime
	if err := row.Scan(&s.ID, &s.RunID, &s.SchemaVersion, &s.WorkspaceID, &locationID,
		&locationVersion, &mountGen, &hostID, &mountAlias, &repoIdentity,
		&s.RefKind, &branch, &checkoutRef, &worktreeRef, &baseRevision, &s.ContextGeneration,
		&s.Source, &sourceSnapshotID, &s.SnapshotDigest, &created); err != nil {
		return nil, err
	}
	setStr := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setStr(&s.WorkspaceLocationID, locationID)
	setStr(&s.MountGeneration, mountGen)
	setStr(&s.ExecutionHostID, hostID)
	setStr(&s.MountAlias, mountAlias)
	setStr(&s.RepositoryIdentity, repoIdentity)
	setStr(&s.BranchName, branch)
	setStr(&s.CheckoutRef, checkoutRef)
	setStr(&s.WorktreeRef, worktreeRef)
	setStr(&s.BaseRevision, baseRevision)
	setStr(&s.SourceSnapshotID, sourceSnapshotID)
	s.LocationVersion = int(locationVersion.Int64)
	s.CreatedAt = mustTime(created)
	return s, nil
}

// Create 落不可变 Snapshot：先域校验 fail fast（legacy-v0 的放宽由 domain 处理），
// immutability 由迁移 trigger 兜底（无 Update/Delete 方法，也不该有）。
func (r *ContextSnapshotRepo) Create(ctx context.Context, s *domain.ExecutionContextSnapshot) error {
	if err := s.Validate(); err != nil {
		return err
	}
	_, err := r.store.execStmt(ctx, r.store.exec(ctx),
		`INSERT INTO execution_context_snapshots(`+ctxSnapshotCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.RunID, s.SchemaVersion, s.WorkspaceID, nullString(s.WorkspaceLocationID),
		s.LocationVersion, nullString(s.MountGeneration), nullString(s.ExecutionHostID),
		nullString(s.MountAlias), nullString(s.RepositoryIdentity),
		s.RefKind, nullString(s.BranchName), nullString(s.CheckoutRef), nullString(s.WorktreeRef),
		nullString(s.BaseRevision), s.ContextGeneration, s.Source, nullString(s.SourceSnapshotID),
		s.SnapshotDigest, timeParam(s.CreatedAt))
	return r.store.mapErr(err)
}

func (r *ContextSnapshotRepo) Get(ctx context.Context, id string) (*domain.ExecutionContextSnapshot, error) {
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+ctxSnapshotCols+` FROM execution_context_snapshots WHERE id=?`, id)
	s, err := scanSnapshot(row)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return s, nil
}

// GetByRun 按 run_id 查回（UNIQUE，每 Run 至多一条）。
func (r *ContextSnapshotRepo) GetByRun(ctx context.Context, runID string) (*domain.ExecutionContextSnapshot, error) {
	row := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT `+ctxSnapshotCols+` FROM execution_context_snapshots WHERE run_id=?`, runID)
	s, err := scanSnapshot(row)
	if err != nil {
		return nil, r.store.mapErr(err)
	}
	return s, nil
}

// HasActiveRunOnCheckout 同 Host 同 checkout/worktree ref 是否已有非终态 Run
// （同 checkout 第一版单活跃 Run，RFC §4.8；命中即 workspace_checkout_busy）。
// legacy 回填快照 Host/ref 全空，NULL 比较恒 false，天然不参与占用判定。
func (r *ContextSnapshotRepo) HasActiveRunOnCheckout(ctx context.Context, hostID, checkoutRef string) (bool, error) {
	var exists bool
	err := r.store.queryRow(ctx, r.store.exec(ctx),
		`SELECT EXISTS(
			SELECT 1 FROM execution_context_snapshots s
			JOIN execution_runs r ON r.id = s.run_id
			WHERE s.execution_host_id=?
			  AND (s.checkout_ref=? OR s.worktree_ref=?)
			  AND r.status NOT IN ('succeeded','interrupted','cancelled','lost','failed'))`,
		hostID, checkoutRef, checkoutRef).Scan(&exists)
	return exists, r.store.mapErr(err)
}
