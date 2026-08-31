// execution_context.go 承载执行上下文域（任务控制面 RFC §4/§7）：
//
//   - EffectiveDevelopmentContext：沿 parent 链解析有效开发选择（显式行 →
//     继承根 → Workspace 默认 Location + root），绝不持久化继承副本。
//   - resolveSnapshotForRun：按 RFC §4.7 来源策略产出待持久 Snapshot 并完成
//     全部静态校验（Location ready、mount 已广告、generation/identity 匹配、
//     ref 组合、branch 唯一 checkout、checkout 占用）——任一失败整事务回滚，
//     不留 queued Run。
//   - SetDevelopmentContext：上下文修改命令（修改门 + 新 context generation）。
//   - ExecutionHost / WorkspaceLocation 命令与只读投影（RFC §9.1–9.3）。
//
// Snapshot 不含宿主绝对路径；ResolvedExecutionContext 只存在于进程内
// （由 hostregistry 在执行前解析）。
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ── 有效 DevelopmentContext 解析（RFC §4.5）──────────────────────────

// EffectiveDevelopmentContext 解析 WorkItem 的有效开发选择：显式 context 行 →
// 沿 parent 链继承到根（Plan child/用户 child 默认继承根，不持久化副本）→
// 根无显式行时用 Workspace 默认 Location + root ref。无默认 Location 返回
// workspace_location_required。返回值同时携带显式性与代际来源：
// 显式行代际 = 行 version；隐式默认代际 = Location version（Location 换绑即换代）。
func (s *Service) EffectiveDevelopmentContext(ctx context.Context, workItemID string) (*domain.DevelopmentContext, bool, error) {
	wi, err := s.store.WorkItems().Get(ctx, workItemID)
	if err != nil {
		return nil, false, err
	}
	current := wi
	for {
		c, getErr := s.store.WorkItemContexts().Get(ctx, current.ID)
		if getErr == nil {
			return c, true, nil
		}
		if !errors.Is(getErr, domain.ErrNotFound) {
			return nil, false, getErr
		}
		if current.ParentID == "" {
			break
		}
		current, getErr = s.store.WorkItems().Get(ctx, current.ParentID)
		if getErr != nil {
			return nil, false, getErr
		}
	}
	// 树内无显式 context：Workspace 默认 Location + root ref（合成行，不落库）。
	loc, locErr := s.store.WorkspaceLocations().DefaultFor(ctx, wi.WorkspaceID)
	if locErr != nil {
		if errors.Is(locErr, domain.ErrNotFound) {
			return nil, false, fmt.Errorf("%w: workspace %s 无默认 Location", domain.ErrWorkspaceLocationRequired, wi.WorkspaceID)
		}
		return nil, false, locErr
	}
	if len(loc.ID) == 0 || loc.Status != domain.LocationReady {
		return nil, false, fmt.Errorf("%w: location %s 状态 %s", domain.ErrWorkspaceLocationRequired, loc.ID, loc.Status)
	}
	synthetic := &domain.DevelopmentContext{
		WorkItemID:          wi.ID,
		ContextOwnerID:      wi.ID,
		WorkspaceLocationID: loc.ID,
		RefKind:             domain.RefRoot,
		Version:             loc.Version,
		CreatedAt:           loc.CreatedAt,
		UpdatedAt:           loc.UpdatedAt,
	}
	return synthetic, false, nil
}

// contextGenerationOf 有效 context 的兼容代际：显式行用行 version；隐式默认用
// Location version（默认绑定变化即新代际，TaskSession 必须 fresh）。
func contextGenerationOf(c *domain.DevelopmentContext, explicit bool) int {
	if explicit {
		return c.Version
	}
	return c.Version // 合成行 version 已填 Location version
}

// ── Snapshot 来源策略与静态校验（RFC §4.6/§4.7/§5.1）─────────────────

// snapshotRequest Run 创建的快照来源策略。
type snapshotRequest struct {
	// source 决定快照来源（RFC §4.7）；current 重新解析当前 context 并冻结
	// location_version/mount_generation，其余从 sourceSnapshotID 克隆。
	source domain.SnapshotSource
	// sourceSnapshotID 克隆来源（inherited/retry/evaluation/recovery 必填；
	// 身份字段原样克隆，digest 不变——retry/evaluation/recovery 不切换 Snapshot）。
	sourceSnapshotID string
}

// resolveSnapshotForRun 产出待持久 Snapshot（调用方在同一事务内 INSERT）。
// current：逐项静态校验后以当前事实冻结身份；克隆：仅校验来源存在——
// 克隆路径不重读当前 Location/mount（RFC §4.7「不重新解析」），执行前的
// Host resolver（hostregistry.Resolve / 远端 Runner accept）负责可用性复核。
func (s *Service) resolveSnapshotForRun(ctx context.Context, wi *domain.WorkItem, runID string, req snapshotRequest, now time.Time) (*domain.ExecutionContextSnapshot, error) {
	if !req.source.Valid() || req.source == domain.SnapshotSourceLegacy {
		return nil, fmt.Errorf("%w: snapshot source %q", domain.ErrValidation, req.source)
	}
	if req.source != domain.SnapshotSourceCurrent {
		if req.sourceSnapshotID == "" {
			return nil, fmt.Errorf("%w: source=%s 缺 source_snapshot_id", domain.ErrValidation, req.source)
		}
		src, err := s.store.ContextSnapshots().Get(ctx, req.sourceSnapshotID)
		if err != nil {
			return nil, err
		}
		if src.WorkspaceID != wi.WorkspaceID {
			return nil, fmt.Errorf("%w: source snapshot %s 属于 workspace %s，目标 Run 属于 %s",
				domain.ErrWorkspaceContextMismatch, src.ID, src.WorkspaceID, wi.WorkspaceID)
		}
		sourceRun, err := s.store.Runs().Get(ctx, src.RunID)
		if err != nil {
			return nil, err
		}
		if sourceRun.WorkspaceID != wi.WorkspaceID {
			return nil, fmt.Errorf("%w: source Run %s workspace 与目标不一致", domain.ErrWorkspaceContextMismatch, sourceRun.ID)
		}
		targetRoot, err := s.workItemTreeRootID(ctx, wi.ID)
		if err != nil {
			return nil, err
		}
		sourceRoot, err := s.workItemTreeRootID(ctx, sourceRun.WorkItemID)
		if err != nil {
			return nil, err
		}
		if sourceRoot != targetRoot {
			return nil, fmt.Errorf("%w: source snapshot %s 不属于目标 Task 根 %s",
				domain.ErrWorkspaceContextMismatch, src.ID, targetRoot)
		}
		return src.CloneForRun(runID, req.source, now), nil
	}

	// current：重新解析当前 context，创建时一次冻结。
	c, explicit, err := s.EffectiveDevelopmentContext(ctx, wi.ID)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateRefCombo(c.RefKind, c.BranchName, c.CheckoutRef, c.WorktreeRef); err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrDevelopmentContextInvalid, err)
	}
	loc, err := s.store.WorkspaceLocations().Get(ctx, c.WorkspaceLocationID)
	if err != nil {
		return nil, err
	}
	if loc.WorkspaceID != wi.WorkspaceID {
		return nil, fmt.Errorf("%w: location %s belongs to workspace %s, not %s",
			domain.ErrWorkspaceContextMismatch, loc.ID, loc.WorkspaceID, wi.WorkspaceID)
	}
	if loc.Status != domain.LocationReady {
		return nil, fmt.Errorf("%w: location %s 状态 %s", domain.ErrWorkspaceLocationRequired, loc.ID, loc.Status)
	}
	host, err := s.store.ExecutionHosts().Get(ctx, loc.ExecutionHostID)
	if err != nil {
		return nil, err
	}
	// Host 明确 offline 是调度可用性：创建 Run 前直接拒绝（Coordinator 转
	// durable waiting_retry；直接 API 409 retryable），不创建无望分派的 Run。
	if host.Status == domain.HostStatusOffline {
		return nil, fmt.Errorf("%w: host %s offline", domain.ErrExecutionHostUnavailable, host.ID)
	}
	mount, err := s.store.ExecutionHosts().GetMount(ctx, loc.ExecutionHostID, loc.MountAlias)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("%w: host %s 未广告 alias %q", domain.ErrWorkspaceMountNotAdvertised, loc.ExecutionHostID, loc.MountAlias)
		}
		return nil, err
	}
	if mount.Status != domain.MountStatusReady {
		return nil, fmt.Errorf("%w: mount %s/%s 状态 %s", domain.ErrWorkspaceMountNotAdvertised, loc.ExecutionHostID, loc.MountAlias, mount.Status)
	}
	if mount.RegistryGeneration != loc.MountGeneration {
		return nil, fmt.Errorf("%w: mount %s generation 已变化（location=%s，advertised=%s）——alias 可能被重指向",
			domain.ErrWorkspaceMountGenerationChanged, loc.MountAlias, loc.MountGeneration, mount.RegistryGeneration)
	}
	if mount.RepositoryIdentity != loc.RepositoryIdentity {
		return nil, fmt.Errorf("%w: mount %s repository identity 不匹配（location=%s，advertised=%s）",
			domain.ErrWorkspaceContextMismatch, loc.MountAlias, loc.RepositoryIdentity, mount.RepositoryIdentity)
	}
	if err := s.validateRefAgainstMount(ctx, mount, c, host.ID); err != nil {
		return nil, err
	}

	snap := &domain.ExecutionContextSnapshot{
		ID:                  domain.NewID(domain.PrefixCtxSnapshot),
		RunID:               runID,
		SchemaVersion:       domain.SnapshotSchemaV1,
		WorkspaceID:         wi.WorkspaceID,
		WorkspaceLocationID: loc.ID,
		LocationVersion:     loc.Version,
		MountGeneration:     mount.RegistryGeneration,
		ExecutionHostID:     host.ID,
		MountAlias:          loc.MountAlias,
		RepositoryIdentity:  loc.RepositoryIdentity,
		RefKind:             c.RefKind,
		BranchName:          c.BranchName,
		CheckoutRef:         c.CheckoutRef,
		WorktreeRef:         c.WorktreeRef,
		BaseRevision:        c.BaseRevision,
		ContextGeneration:   contextGenerationOf(c, explicit),
		Source:              domain.SnapshotSourceCurrent,
		CreatedAt:           now,
	}
	snap.SnapshotDigest = snap.ComputeDigest()
	return snap, nil
}

// validateRefAgainstMount 对 branch/worktree ref 做 Host 广告面校验：
// branch 必须唯一命中一个已发现 checkout 且 ref 精确匹配；worktree_ref 必须
// 来自该 Host 对该 repository 的发现结果。root 无字段直接放行。
func (s *Service) validateRefAgainstMount(ctx context.Context, mount *domain.HostMount, c *domain.DevelopmentContext, hostID string) error {
	switch c.RefKind {
	case domain.RefRoot:
		return nil
	case domain.RefBranch:
		hits := 0
		hitRef := ""
		for _, co := range mount.Checkouts {
			if co.Branch != "" && co.Branch == c.BranchName {
				hits++
				hitRef = co.Ref
			}
		}
		if hits != 1 {
			return fmt.Errorf("%w: branch %q 命中 %d 个 checkout（要求恰好 1）", domain.ErrWorkspaceBranchNotUnique, c.BranchName, hits)
		}
		if hitRef != c.CheckoutRef {
			return fmt.Errorf("%w: branch %q checkout ref 不匹配（context=%s，advertised=%s）",
				domain.ErrWorkspaceContextMismatch, c.BranchName, c.CheckoutRef, hitRef)
		}
	case domain.RefWorktree:
		found := false
		for _, co := range mount.Checkouts {
			if co.Ref == c.WorktreeRef {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: worktree ref %q 不在 Host %s 的发现结果中（可能已删除/移动）",
				domain.ErrWorkspaceContextMismatch, c.WorktreeRef, mount.ExecutionHostID)
		}
	}
	// 同 checkout 第一版单活跃 Run（非终态占用即拒绝）；root ref 无 opaque
	// checkout 标识，不参与该判定（执行侧 lease 由 Host resolver 负责）。
	busyRef := c.CheckoutRef
	if c.RefKind == domain.RefWorktree {
		busyRef = c.WorktreeRef
	}
	if busyRef == "" {
		return nil
	}
	busy, err := s.store.ContextSnapshots().HasActiveRunOnCheckout(ctx, hostID, busyRef)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("%w: checkout %s 已有非终态 Run 占用", domain.ErrWorkspaceCheckoutBusy, busyRef)
	}
	return nil
}

// ── SetDevelopmentContext 命令（RFC §4.5 修改门 / §7.6）──────────────

// SetDevelopmentContextParams 是 set-development-context 命令输入。
type SetDevelopmentContextParams struct {
	WorkspaceLocationID string
	RefKind             domain.RefKind
	BranchName          string
	CheckoutRef         string
	WorktreeRef         string
	BaseRevision        string
	ExpectedVersion     int
}

// SetDevelopmentContext 修改 WorkItem 的显式开发上下文（同事务写事件 + outbox）：
//   - 终态拒绝；根树存在非终态 Run 拒绝 development_context_busy；
//   - 子 Task 禁止改变 workspace_location_id（跨 Host/Location 第一版禁止）；
//   - branch/worktree override 只允许落在根同 Location 内；
//   - waiting_user 修改使 Task BeginExecution + Coordinator 回 queued（新代际）；
//   - blocked/todo 且无非终态 Run 可改。
func (s *Service) SetDevelopmentContext(ctx context.Context, workItemID string, p SetDevelopmentContextParams) (*domain.DevelopmentContext, error) {
	if err := domain.ValidateRefCombo(p.RefKind, p.BranchName, p.CheckoutRef, p.WorktreeRef); err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrDevelopmentContextInvalid, err)
	}
	if p.WorkspaceLocationID == "" {
		return nil, fmt.Errorf("%w: workspace_location_id required", domain.ErrValidation)
	}
	var saved *domain.DevelopmentContext
	var startCoordinatorRoot string
	var notifyWorkspace string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		wi, err := s.store.WorkItems().Get(ctx, workItemID)
		if err != nil {
			return err
		}
		if err := requireTaskWorkItem(wi); err != nil {
			return err
		}
		if wi.Status.IsTerminal() {
			return fmt.Errorf("%w: Task 已终态不可修改 context", domain.ErrDevelopmentContextBusy)
		}
		if err := wi.CheckVersion(p.ExpectedVersion); err != nil {
			return err
		}
		// 根树存在非终态 Run → 拒绝（含子树：Run 全部锚定在各自 work item 上）。
		busy, err := s.rootTreeHasActiveRun(ctx, wi)
		if err != nil {
			return err
		}
		if busy {
			return fmt.Errorf("%w: 任务树存在非终态 Run", domain.ErrDevelopmentContextBusy)
		}
		root, err := s.workItemRoot(ctx, wi)
		if err != nil {
			return err
		}
		rootCtx, _, err := s.EffectiveDevelopmentContext(ctx, root.ID)
		if err != nil {
			return err
		}
		locationID := p.WorkspaceLocationID
		if wi.ID != root.ID {
			// 子 Task：禁止跨 Location；未显式指定时沿用根 Location。
			if locationID != "" && locationID != rootCtx.WorkspaceLocationID {
				return fmt.Errorf("%w: 子 Task 不允许改变 workspace_location_id（禁止跨 Host/Location）", domain.ErrDevelopmentContextInvalid)
			}
			if locationID == "" {
				locationID = rootCtx.WorkspaceLocationID
			}
		}
		loc, err := s.store.WorkspaceLocations().Get(ctx, locationID)
		if err != nil {
			return err
		}
		if loc.WorkspaceID != wi.WorkspaceID {
			return fmt.Errorf("%w: location %s belongs to workspace %s, not %s",
				domain.ErrWorkspaceContextMismatch, loc.ID, loc.WorkspaceID, wi.WorkspaceID)
		}
		// Location 必须可用且 mount 广告与 ref 一致（写入口径与 Run 冻结一致）。
		if loc.Status != domain.LocationReady {
			return fmt.Errorf("%w: location %s 状态 %s", domain.ErrWorkspaceLocationRequired, loc.ID, loc.Status)
		}
		mount, err := s.store.ExecutionHosts().GetMount(ctx, loc.ExecutionHostID, loc.MountAlias)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("%w: host %s 未广告 alias %q", domain.ErrWorkspaceMountNotAdvertised, loc.ExecutionHostID, loc.MountAlias)
			}
			return err
		}
		if mount.RepositoryIdentity != loc.RepositoryIdentity {
			return fmt.Errorf("%w: mount %s repository identity 不匹配", domain.ErrWorkspaceContextMismatch, loc.MountAlias)
		}
		if err := s.validateRefAgainstMount(ctx, mount, &domain.DevelopmentContext{
			RefKind: p.RefKind, BranchName: p.BranchName, CheckoutRef: p.CheckoutRef, WorktreeRef: p.WorktreeRef,
		}, loc.ExecutionHostID); err != nil {
			return err
		}
		notifyWorkspace = wi.WorkspaceID

		now := time.Now().UTC()
		saved = &domain.DevelopmentContext{
			WorkItemID:          wi.ID,
			ContextOwnerID:      root.ID,
			WorkspaceLocationID: loc.ID,
			RefKind:             p.RefKind,
			BranchName:          p.BranchName,
			CheckoutRef:         p.CheckoutRef,
			WorktreeRef:         p.WorktreeRef,
			BaseRevision:        p.BaseRevision,
			Version:             1,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		if existing, getErr := s.store.WorkItemContexts().Get(ctx, wi.ID); getErr == nil {
			saved.Version = existing.Version + 1
			saved.CreatedAt = existing.CreatedAt
		} else if !errors.Is(getErr, domain.ErrNotFound) {
			return getErr
		}
		if err := s.store.WorkItemContexts().Upsert(ctx, saved); err != nil {
			return err
		}
		if err := s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemDevelopmentContextUpdated,
			domain.AggregateWorkItem, wi.ID, wi.Version, nil,
			map[string]any{"work_item_id": wi.ID, "workspace_location_id": loc.ID,
				"ref_kind": string(p.RefKind), "context_version": saved.Version,
				"record_kind": string(workItemRecordKind(wi))}); err != nil {
			return err
		}
		// waiting_user 修改 context：Task 回 execution、Coordinator 回 queued，
		// 下一轮 Run 以新 context generation 创建（RFC §4.5 修改门）。
		if wi.Status == domain.WorkItemBlocked {
			// blocked 保持 blocked：context 修复不静默解除 blocker（§4.5）。
			return nil
		}
		if wi.Status == domain.WorkItemTodo || wi.Status == domain.WorkItemInProgress {
			if wi.Status == domain.WorkItemInProgress && wi.Phase != domain.PhaseExecution {
				expected := wi.Version
				wi.BeginExecution(now)
				if err := s.store.WorkItems().Update(ctx, wi, expected); err != nil {
					return err
				}
				if err := s.emit(ctx, wi.WorkspaceID, domain.EventWorkItemUpdated,
					domain.AggregateWorkItem, wi.ID, wi.Version, nil,
					map[string]any{"phase": string(wi.Phase), "record_kind": string(workItemRecordKind(wi))}); err != nil {
					return err
				}
			}
			if state, stateErr := s.store.TaskCoordinators().GetStateForWorkItem(ctx, wi.ID); stateErr == nil {
				if state.Status == domain.CoordinatorWaitingUser {
					expected := state.Version
					state.Status = domain.CoordinatorQueued
					state.CurrentAction = "queued"
					state.Summary = "开发上下文已更新，重新排队执行"
					state.BlockerCode, state.BlockerMessage = "", ""
					if err := s.store.TaskCoordinators().UpdateState(ctx, state, expected); err != nil {
						return err
					}
					state.Version = expected + 1
					if err := s.appendCoordinatorEvent(ctx, state, state.RootWorkItemID, domain.EventCoordinatorQueued,
						"开发上下文已更新，重新排队", "", state.CoordinatorAgentID, state.Attempt, "", nil,
						map[string]any{"stage": "plan", "next_action": "以新 context generation 创建 Run"}); err != nil {
						return err
					}
					startCoordinatorRoot = state.RootWorkItemID
				}
			} else if !errors.Is(stateErr, domain.ErrNotFound) {
				return stateErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if notifyWorkspace != "" {
		s.notifier.Notify(notifyWorkspace)
	}
	if startCoordinatorRoot != "" {
		if err := s.StartCoordinator(context.WithoutCancel(ctx), startCoordinatorRoot); err != nil {
			log.Printf("execution_context: task %s context 变更后重启 Coordinator 失败: %v", startCoordinatorRoot, err)
		}
	}
	return saved, nil
}

// workItemRoot 沿 parent 链走到根 Task。
func (s *Service) workItemRoot(ctx context.Context, wi *domain.WorkItem) (*domain.WorkItem, error) {
	current := wi
	for current.ParentID != "" {
		parent, err := s.store.WorkItems().Get(ctx, current.ParentID)
		if err != nil {
			return nil, err
		}
		current = parent
	}
	return current, nil
}

func savedWorkItemWorkspace(c *domain.DevelopmentContext, store Store) string {
	if c == nil {
		return ""
	}
	wi, err := store.WorkItems().Get(context.Background(), c.WorkItemID)
	if err != nil {
		return ""
	}
	return wi.WorkspaceID
}

// rootTreeHasActiveRun 报告根 Task 树内是否存在非终态 Run（context 修改门）。
// 以根为入口遍历子树；Run 全部锚定 work item，逐项检查 LatestRunID 不可靠
// （终态过滤在内存做）。
func (s *Service) rootTreeHasActiveRun(ctx context.Context, wi *domain.WorkItem) (bool, error) {
	root := wi
	for root.ParentID != "" {
		parent, err := s.store.WorkItems().Get(ctx, root.ParentID)
		if err != nil {
			return false, err
		}
		root = parent
	}
	queue := []*domain.WorkItem{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		runs, err := s.store.Runs().ListByWorkItem(ctx, cur.ID)
		if err != nil {
			return false, err
		}
		for _, r := range runs {
			if r != nil && !r.Status.IsTerminal() {
				return true, nil
			}
		}
		children, err := s.store.WorkItems().ListByParent(ctx, cur.ID)
		if err != nil {
			return false, err
		}
		queue = append(queue, children...)
	}
	return false, nil
}

// GetDevelopmentContext 返回有效 context 的只读投影（HTTP GET）。
func (s *Service) GetDevelopmentContext(ctx context.Context, workItemID string) (*domain.DevelopmentContext, error) {
	c, _, err := s.EffectiveDevelopmentContext(ctx, workItemID)
	return c, err
}

// ── TaskSession 锚点代际与 claim（RFC §4.8）──────────────────────────

// SessionFingerprint 组合会话指纹：既有 config digest ⊕ 执行上下文身份
// （snapshot digest 覆盖 location/host/mount generation/repository/ref/generation）。
// 任一半变化即漂移 → resolveResume 判 fresh（RFC §4.8：禁止跨 context resume）。
func SessionFingerprint(configDigest string, snap *domain.ExecutionContextSnapshot) string {
	sum := sha256.Sum256([]byte("session-fingerprint/v1\n" + configDigest + "\n" + snap.SnapshotDigest))
	return hex.EncodeToString(sum[:])
}

// claimTaskSessionAnchor 在 Run 创建事务内原子预 claim last_run_id 与单调
// anchor_run_sequence（RFC §4.8）。序号由仓储在唯一键冲突更新中递增，不能由
// 并发事务在内存中读改写；既有 session material（含 __ref）保持不变，直到新
// owner 报告自己的会话。adapterID 为空（无会话语义）时 no-op。
func (s *Service) claimTaskSessionAnchor(ctx context.Context, r *domain.ExecutionRun, snap *domain.ExecutionContextSnapshot) error {
	if r.AdapterID == "" {
		return nil
	}
	now := time.Now().UTC()
	anchor := &domain.TaskSession{
		ID: domain.NewID(domain.PrefixTaskSess), WorkspaceID: r.WorkspaceID,
		AgentProfileID: r.AgentProfileID, AdapterID: r.AdapterID, TaskKey: r.WorkItemID,
		ParentAnchorID:    s.anchorParent(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID),
		SessionParams:     map[string]any{},
		ContextSnapshotID: snap.ID, ContextGeneration: snap.ContextGeneration,
		LastRunID: r.ID, AnchorRunSequence: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	claimed, err := s.store.TaskSessions().ClaimAnchor(ctx, anchor)
	if err != nil {
		return err
	}
	// The durable sequence is authoritative. The mutable in-memory Run does not
	// carry it, but callers that re-read the TaskSession now observe the exact
	// owner/sequence installed by the atomic claim.
	anchor.AnchorRunSequence = claimed.AnchorRunSequence
	return nil
}

// taskSessionAnchorGate 是 run.session 写入的锚点门（RFC §4.8）：仅当锚点
// context generation 与本 Run 快照一致、且本 Run 是当前 anchor owner 时放行。
// 旧 Run 迟到的 session/墓碑（含 Clear）在这里被拒——不覆盖新代际锚点。
// 锚点不存在（无行）时放行：首次会话上报先于 claim 的防御路径。
func (s *Service) taskSessionAnchorGate(ctx context.Context, r *domain.ExecutionRun) (bool, *domain.TaskSession, *domain.ExecutionContextSnapshot, error) {
	if r.AdapterID == "" {
		return true, nil, nil, nil
	}
	snap, err := s.store.ContextSnapshots().GetByRun(ctx, r.ID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// A Run without a durable execution snapshot has no context generation
			// to prove ownership against. Legacy callbacks must fail closed rather
			// than create or clear session material for a newer v1 owner.
			return false, nil, nil, nil
		}
		return false, nil, nil, err
	}
	ts, err := s.store.TaskSessions().Get(ctx, r.WorkspaceID, r.AgentProfileID, r.AdapterID, r.WorkItemID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Every v1 Run claims its anchor in the same creation transaction. A
			// missing row is corrupt/partial state; a legacy-v0 row is equally unable
			// to prove current ownership. Both must fail closed.
			return false, nil, snap, nil
		}
		return false, nil, nil, err
	}
	if ts.ContextGeneration != snap.ContextGeneration {
		return false, ts, snap, nil
	}
	if ts.LastRunID != "" && ts.LastRunID != r.ID {
		return false, ts, snap, nil
	}
	return true, ts, snap, nil
}

// ── ExecutionHost / WorkspaceLocation 命令（RFC §9.1）────────────────

// EnrollmentCredentialPrefix 是 Runner enrollment bearer 凭据前缀（contracts/runner/v2）。
const EnrollmentCredentialPrefix = "atw_host_"

// enrollmentCredential 生成一次性 enrollment 凭据 `atw_host_<host_id>_<secret>`；
// 服务端只保存 sha256(secret) hex（与 runnergateway.enrollmentDigest 同算法）。
func enrollmentCredential(hostID string) (token, digest string, err error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	secret := hex.EncodeToString(raw)
	token = EnrollmentCredentialPrefix + hostID + "_" + secret
	sum := sha256.Sum256([]byte(secret))
	return token, hex.EncodeToString(sum[:]), nil
}

// ListExecutionHosts 列出全部宿主身份（含 host_local）。
func (s *Service) ListExecutionHosts(ctx context.Context) ([]*domain.ExecutionHost, error) {
	return s.store.ExecutionHosts().List(ctx)
}

// emitHostEvent 把 Host 级事实扇出到当前全部 Workspace 的 canonical 流
// （Host 是全局身份；stream_events 必须挂 workspace，RFC §10）。
func (s *Service) emitHostEvent(ctx context.Context, evType, hostID string, version int, data map[string]any) error {
	ids, err := s.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.emit(ctx, id, evType, domain.AggregateExecutionHost, hostID, version, nil, data); err != nil {
			return err
		}
	}
	return nil
}

// CreateExecutionHostParams Host enrollment 命令输入。
type CreateExecutionHostParams struct {
	Name string
	Kind domain.HostKind
}

// CreateExecutionHost 由 Workspace admin 创建稳定远程 Host identity，一次性返回
// enrollment credential（明文只出现在本次响应；服务端只存 digest）。
// host_local 是受保护本机 Host，不可通过该命令创建。
func (s *Service) CreateExecutionHost(ctx context.Context, p CreateExecutionHostParams) (*domain.ExecutionHost, string, error) {
	if p.Kind != domain.HostKindRemote {
		return nil, "", fmt.Errorf("%w: 只允许创建 remote Host（local 由系统内置）", domain.ErrValidation)
	}
	if p.Name == "" {
		return nil, "", fmt.Errorf("%w: name required", domain.ErrValidation)
	}
	now := time.Now().UTC()
	h := &domain.ExecutionHost{
		ID: domain.NewID(domain.PrefixExecutionHost), Name: p.Name,
		Kind: domain.HostKindRemote, Status: domain.HostStatusOffline,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	var credential string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		token, digest, err := enrollmentCredential(h.ID)
		if err != nil {
			return err
		}
		h.EnrollmentRef = digest
		credential = token
		if err := s.store.ExecutionHosts().Create(ctx, h); err != nil {
			return err
		}
		return s.emitHostEvent(ctx, domain.EventExecutionHostUpdated, h.ID, h.Version,
			map[string]any{"host_id": h.ID, "kind": string(h.Kind), "status": string(h.Status), "reason": "enrolled"})
	})
	if err != nil {
		return nil, "", err
	}
	return h, credential, nil
}

// RotateExecutionHostCredential 轮换 enrollment 凭据：旧凭据立即失效（digest
// 覆盖写），旧 connection epoch 由网关在重连时以 hello 校验拒绝。
func (s *Service) RotateExecutionHostCredential(ctx context.Context, hostID string) (*domain.ExecutionHost, string, error) {
	var updated *domain.ExecutionHost
	var credential string
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		h, err := s.store.ExecutionHosts().Get(ctx, hostID)
		if err != nil {
			return err
		}
		if h.Kind == domain.HostKindLocal {
			return fmt.Errorf("%w: 本机 Host 不使用 enrollment 凭据", domain.ErrValidation)
		}
		token, digest, err := enrollmentCredential(h.ID)
		if err != nil {
			return err
		}
		expected := h.Version
		h.EnrollmentRef = digest
		if err := s.store.ExecutionHosts().Update(ctx, h, expected); err != nil {
			return err
		}
		h.Version = expected + 1
		credential = token
		updated = h
		if err := s.emit(ctx, "", domain.EventExecutionHostUpdated,
			domain.AggregateExecutionHost, h.ID, h.Version, nil,
			map[string]any{"host_id": h.ID, "status": string(h.Status), "reason": "credential_rotated"}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return updated, credential, nil
}

// ListHostMounts 返回 Host 的 mount 广告投影（无绝对路径）。
func (s *Service) ListHostMounts(ctx context.Context, hostID string) ([]*domain.HostMount, error) {
	if _, err := s.store.ExecutionHosts().Get(ctx, hostID); err != nil {
		return nil, err
	}
	return s.store.ExecutionHosts().ListMounts(ctx, hostID)
}

// ListWorkspaceLocations 列出 Workspace 的 Location 绑定。
func (s *Service) ListWorkspaceLocations(ctx context.Context, workspaceID string) ([]*domain.WorkspaceLocation, error) {
	return s.store.WorkspaceLocations().ListByWorkspace(ctx, workspaceID)
}

// CreateWorkspaceLocationParams Location 绑定命令输入（无任何 path 字段）。
type CreateWorkspaceLocationParams struct {
	ExecutionHostID    string
	MountAlias         string
	RepositoryIdentity string
	MountGeneration    string
	IsDefault          bool
}

// CreateWorkspaceLocation 把 Workspace 绑定到 Host 已广告且 identity 匹配的
// mount；必须携带预期 mount_generation（失配 409 workspace_mount_generation_changed）。
func (s *Service) CreateWorkspaceLocation(ctx context.Context, workspaceID string, p CreateWorkspaceLocationParams) (*domain.WorkspaceLocation, error) {
	if workspaceID == "" || p.ExecutionHostID == "" || p.MountAlias == "" || p.RepositoryIdentity == "" || p.MountGeneration == "" {
		return nil, fmt.Errorf("%w: workspace/host/alias/repository_identity/mount_generation required", domain.ErrValidation)
	}
	var created *domain.WorkspaceLocation
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.store.Workspaces().Get(ctx, workspaceID); err != nil {
			return err
		}
		if _, err := s.store.ExecutionHosts().Get(ctx, p.ExecutionHostID); err != nil {
			return err
		}
		mount, err := s.store.ExecutionHosts().GetMount(ctx, p.ExecutionHostID, p.MountAlias)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return fmt.Errorf("%w: host %s 未广告 alias %q", domain.ErrWorkspaceMountNotAdvertised, p.ExecutionHostID, p.MountAlias)
			}
			return err
		}
		if mount.RepositoryIdentity != p.RepositoryIdentity {
			return fmt.Errorf("%w: mount %s repository identity 不匹配（advertised=%s）",
				domain.ErrWorkspaceContextMismatch, p.MountAlias, mount.RepositoryIdentity)
		}
		if p.MountGeneration != mount.RegistryGeneration {
			return fmt.Errorf("%w: mount %s generation 已变化（expected=%s，advertised=%s）",
				domain.ErrWorkspaceMountGenerationChanged, p.MountAlias, p.MountGeneration, mount.RegistryGeneration)
		}
		existing, err := s.store.WorkspaceLocations().ListByWorkspace(ctx, workspaceID)
		if err != nil {
			return err
		}
		for _, l := range existing {
			if l.ExecutionHostID == p.ExecutionHostID && l.MountAlias == p.MountAlias {
				return fmt.Errorf("%w: workspace 已绑定 %s/%s", domain.ErrWorkspaceLocationAmbiguous, p.ExecutionHostID, p.MountAlias)
			}
			if p.IsDefault && l.IsDefault {
				return fmt.Errorf("%w: workspace 已有默认 Location %s", domain.ErrWorkspaceLocationAmbiguous, l.ID)
			}
		}
		now := time.Now().UTC()
		created = &domain.WorkspaceLocation{
			ID: domain.NewID(domain.PrefixWorkspaceLocation), WorkspaceID: workspaceID,
			ExecutionHostID: p.ExecutionHostID, MountAlias: p.MountAlias,
			MountGeneration: mount.RegistryGeneration, RepositoryIdentity: p.RepositoryIdentity,
			IsDefault: p.IsDefault, Status: domain.LocationReady,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.WorkspaceLocations().Create(ctx, created); err != nil {
			return err
		}
		return s.emit(ctx, workspaceID, domain.EventWorkspaceLocationCreated,
			domain.AggregateWorkspaceLocation, created.ID, created.Version, nil,
			map[string]any{"workspace_id": workspaceID, "execution_host_id": p.ExecutionHostID,
				"mount_alias": p.MountAlias, "is_default": p.IsDefault})
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(created.WorkspaceID)
	return created, nil
}

// UpdateWorkspaceLocationParams PATCH 命令输入（expected_version 必填）。
type UpdateWorkspaceLocationParams struct {
	RepositoryIdentity string
	MountGeneration    string
	IsDefault          *bool
	ExpectedVersion    int
}

// UpdateWorkspaceLocation 显式命令修改 identity/default；每次都必须携带预期
// mount_generation，并在同一事务中与广告 identity/generation 比对。status 不经
// 此命令（健康投影）。
func (s *Service) UpdateWorkspaceLocation(ctx context.Context, locationID string, p UpdateWorkspaceLocationParams) (*domain.WorkspaceLocation, error) {
	if p.MountGeneration == "" {
		return nil, fmt.Errorf("%w: mount_generation required", domain.ErrValidation)
	}
	if p.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: expected_version required", domain.ErrValidation)
	}
	var updated *domain.WorkspaceLocation
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		l, err := s.store.WorkspaceLocations().Get(ctx, locationID)
		if err != nil {
			return err
		}
		if p.ExpectedVersion != l.Version {
			return domain.ErrVersionConflict
		}
		mount, merr := s.store.ExecutionHosts().GetMount(ctx, l.ExecutionHostID, l.MountAlias)
		if merr != nil {
			if errors.Is(merr, domain.ErrNotFound) {
				return fmt.Errorf("%w: host %s 未广告 alias %q", domain.ErrWorkspaceMountNotAdvertised, l.ExecutionHostID, l.MountAlias)
			}
			return merr
		}
		if p.MountGeneration != mount.RegistryGeneration {
			return fmt.Errorf("%w: mount %s generation 已变化（expected=%s，advertised=%s）",
				domain.ErrWorkspaceMountGenerationChanged, l.MountAlias, p.MountGeneration, mount.RegistryGeneration)
		}
		identity := l.RepositoryIdentity
		if p.RepositoryIdentity != "" {
			identity = p.RepositoryIdentity
		}
		if mount.RepositoryIdentity != identity {
			return fmt.Errorf("%w: mount %s repository identity 不匹配", domain.ErrWorkspaceContextMismatch, l.MountAlias)
		}
		l.RepositoryIdentity = identity
		l.MountGeneration = mount.RegistryGeneration
		if p.IsDefault != nil && *p.IsDefault && !l.IsDefault {
			existing, err := s.store.WorkspaceLocations().ListByWorkspace(ctx, l.WorkspaceID)
			if err != nil {
				return err
			}
			for _, other := range existing {
				if other.ID != l.ID && other.IsDefault {
					return fmt.Errorf("%w: workspace 已有默认 Location %s", domain.ErrWorkspaceLocationAmbiguous, other.ID)
				}
			}
		}
		if p.IsDefault != nil {
			l.IsDefault = *p.IsDefault
		}
		expected := l.Version
		if err := s.store.WorkspaceLocations().Update(ctx, l, expected); err != nil {
			return err
		}
		l.Version = expected + 1
		updated = l
		return s.emit(ctx, l.WorkspaceID, domain.EventWorkspaceLocationUpdated,
			domain.AggregateWorkspaceLocation, l.ID, l.Version, nil,
			map[string]any{"workspace_id": l.WorkspaceID, "mount_alias": l.MountAlias,
				"is_default": l.IsDefault, "mount_generation": l.MountGeneration})
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(updated.WorkspaceID)
	return updated, nil
}

// ProbeWorkspaceLocationResult Location probe 投影。
type ProbeWorkspaceLocationResult struct {
	LocationID string
	Status     domain.LocationStatus
	Detail     string
	CheckedAt  time.Time
}

// ProbeWorkspaceLocation 触发 Host/Mount 可用性探测并刷新 status 投影（不创建
// Run）：Host ready + mount 已广告且 generation/identity 匹配 → ready；
// Host offline → unavailable；generation 失配 → degraded + unavailable 事件。
func (s *Service) ProbeWorkspaceLocation(ctx context.Context, locationID string) (*ProbeWorkspaceLocationResult, error) {
	var result *ProbeWorkspaceLocationResult
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		l, err := s.store.WorkspaceLocations().Get(ctx, locationID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		status := domain.LocationReady
		detail := ""
		host, herr := s.store.ExecutionHosts().Get(ctx, l.ExecutionHostID)
		mount, merr := s.store.ExecutionHosts().GetMount(ctx, l.ExecutionHostID, l.MountAlias)
		switch {
		case herr != nil:
			return herr
		case host.Status == domain.HostStatusOffline:
			status, detail = domain.LocationUnavailable, "host "+host.ID+" offline"
		case merr != nil && errors.Is(merr, domain.ErrNotFound):
			status, detail = domain.LocationUnavailable, fmt.Sprintf("mount %q 未广告", l.MountAlias)
		case merr != nil:
			return merr
		case mount.RegistryGeneration != l.MountGeneration:
			status, detail = domain.LocationDegraded, fmt.Sprintf("mount generation 已变化（location=%s，advertised=%s）", l.MountGeneration, mount.RegistryGeneration)
		case mount.RepositoryIdentity != l.RepositoryIdentity:
			status, detail = domain.LocationUnavailable, fmt.Sprintf("repository identity 不匹配（location=%s，advertised=%s）", l.RepositoryIdentity, mount.RepositoryIdentity)
		}
		if err := s.store.WorkspaceLocations().SetStatus(ctx, l.ID, status, now); err != nil {
			return err
		}
		result = &ProbeWorkspaceLocationResult{LocationID: l.ID, Status: status, Detail: detail, CheckedAt: now}
		evType := domain.EventWorkspaceLocationUpdated
		if status == domain.LocationUnavailable {
			evType = domain.EventWorkspaceLocationUnavailable
		}
		return s.emit(ctx, l.WorkspaceID, evType,
			domain.AggregateWorkspaceLocation, l.ID, l.Version, nil,
			map[string]any{"workspace_id": l.WorkspaceID, "status": string(status), "detail": detail})
	})
	if err != nil {
		return nil, err
	}
	s.notifier.Notify(result.LocationID)
	if ws, err := s.store.WorkspaceLocations().Get(ctx, result.LocationID); err == nil {
		s.notifier.Notify(ws.WorkspaceID)
	}
	return result, nil
}

// ── Run 快照只读投影（RFC §9.3）──────────────────────────────────────

// RunExecutionContext 是 run execution-context 端点的只读投影：逻辑 Snapshot
// + Host/Mount 健康投影；不含任何宿主绝对路径。
type RunExecutionContext struct {
	Snapshot       *domain.ExecutionContextSnapshot `json:"snapshot"`
	HostStatus     domain.HostStatus                `json:"host_status"`
	MountStatus    domain.MountStatus               `json:"mount_status"`
	LocationStatus domain.LocationStatus            `json:"location_status"`
}

// GetRunExecutionContext 读取 Run 冻结的不可变 Snapshot 与当前健康投影。
func (s *Service) GetRunExecutionContext(ctx context.Context, runID string) (*RunExecutionContext, error) {
	if _, err := s.store.Runs().Get(ctx, runID); err != nil {
		return nil, err
	}
	snap, err := s.store.ContextSnapshots().GetByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := &RunExecutionContext{Snapshot: snap}
	if loc, err := s.store.WorkspaceLocations().Get(ctx, snap.WorkspaceLocationID); err == nil {
		out.LocationStatus = loc.Status
	}
	if host, err := s.store.ExecutionHosts().Get(ctx, snap.ExecutionHostID); err == nil {
		out.HostStatus = host.Status
	}
	if mount, err := s.store.ExecutionHosts().GetMount(ctx, snap.ExecutionHostID, snap.MountAlias); err == nil {
		out.MountStatus = mount.Status
	}
	return out, nil
}

// deferCoordinatorForHostUnavailable 把「创建 Run 前发现目标 Host offline」转成
// durable waiting_retry checkpoint（RFC §7.4）：不创建 Run，持久 next_action_at，
// 由既有恢复循环在 Host 回落后重驱。并发方已推进（CAS 冲突）时幂等跳过。
func (s *Service) deferCoordinatorForHostUnavailable(ctx context.Context, workItemID string, cause error) error {
	state, err := s.store.TaskCoordinators().GetStateForWorkItem(ctx, workItemID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(ctx context.Context) error {
		fresh, err := s.store.TaskCoordinators().GetState(ctx, state.RootWorkItemID)
		if err != nil {
			return err
		}
		if fresh.Status == domain.CoordinatorCompleted || fresh.Status == domain.CoordinatorCancelled ||
			fresh.Status == domain.CoordinatorWaitingRetry || fresh.CurrentRunID != "" {
			return nil
		}
		expected := fresh.Version
		now := time.Now().UTC()
		fresh.Status = domain.CoordinatorWaitingRetry
		fresh.BlockerCode = "execution_host_unavailable"
		fresh.BlockerMessage = cause.Error()
		fresh.NextActionAt = &now
		if err := s.store.TaskCoordinators().UpdateState(ctx, fresh, expected); err != nil {
			return err
		}
		fresh.Version = expected + 1
		return s.appendCoordinatorEvent(ctx, fresh, fresh.RootWorkItemID, domain.EventCoordinatorRetryScheduled,
			fmt.Sprintf("目标执行 Host 不可用，已进入 durable 等待重试（%s）", cause.Error()), "",
			fresh.CoordinatorAgentID, fresh.Attempt, "", nil,
			map[string]any{"stage": "retry", "blocker": "execution_host_unavailable", "retryable": true})
	})
}

// ── 本机 bootstrap（control-plane 启动装配）──────────────────────────
// BootstrapLocalExecution 确保受保护本机 Host 存在，并把本机 registry 广告
// 投影落库（HostMount；绝对路径永不落库）。locationBootstrap 回调由装配层
// 决定是否自动建默认 Location（单 Workspace 且无 Location 才建）。
func (s *Service) EnsureLocalHostBootstrap(ctx context.Context, mounts []domain.HostMount) error {
	return s.store.InTx(ctx, func(ctx context.Context) error {
		now := time.Now().UTC()
		if _, err := s.store.ExecutionHosts().EnsureLocalHost(ctx, now); err != nil {
			return err
		}
		for _, m := range mounts {
			m.ExecutionHostID = domain.LocalHostID
			m.Status = domain.MountStatusReady
			m.LastSeenAt = now
			if err := s.store.ExecutionHosts().UpsertMount(ctx, &m); err != nil {
				return err
			}
		}
		return s.emitHostEvent(ctx, domain.EventExecutionHostUpdated, domain.LocalHostID, 0,
			map[string]any{"host_id": domain.LocalHostID, "status": string(domain.HostStatusReady), "reason": "bootstrap"})
	})
}

// EnsureSingleWorkspaceDefaultLocation 单 Workspace 且无任何 Location 时，
// 把第一个已广告的本机 mount 注册为默认 Location（§6.1：多 Workspace 保持
// unmapped，任务进可解释 blocked）。
func (s *Service) EnsureSingleWorkspaceDefaultLocation(ctx context.Context) error {
	ids, err := s.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return nil
	}
	existing, err := s.store.WorkspaceLocations().ListByWorkspace(ctx, ids[0])
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	mounts, err := s.store.ExecutionHosts().ListMounts(ctx, domain.LocalHostID)
	if err != nil {
		return err
	}
	if len(mounts) == 0 {
		return nil
	}
	m := mounts[0]
	if _, err := s.CreateWorkspaceLocation(ctx, ids[0], CreateWorkspaceLocationParams{
		ExecutionHostID: domain.LocalHostID, MountAlias: m.Alias,
		RepositoryIdentity: m.RepositoryIdentity, MountGeneration: m.RegistryGeneration, IsDefault: true,
	}); err != nil {
		return err
	}
	log.Printf("execution_context: 单 Workspace %s 自动绑定默认 Location（host_local/%s）", ids[0], m.Alias)
	return nil
}

// SeedWorkspaceLocation 为测试与本地试验环境幂等准备好最小执行上下文：
// host_local + 本机默认 mount 广告（占位 generation）+ workspace 默认 Location。
// 生产装配不经过这里（main.go 走 EnsureLocalHostBootstrap +
// EnsureSingleWorkspaceDefaultLocation，mount 事实来自本机 registry yaml）。
func SeedWorkspaceLocation(ctx context.Context, store Store, workspaceID string) (*domain.WorkspaceLocation, error) {
	now := time.Now().UTC()
	if _, err := store.ExecutionHosts().EnsureLocalHost(ctx, now); err != nil {
		return nil, err
	}
	if err := store.ExecutionHosts().UpsertMount(ctx, &domain.HostMount{
		ExecutionHostID: domain.LocalHostID, Alias: "default",
		RepositoryIdentity: "repo_default", RegistryGeneration: "gen_seed",
		Status: domain.MountStatusReady, LastSeenAt: now,
	}); err != nil {
		return nil, err
	}
	existing, err := store.WorkspaceLocations().ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, l := range existing {
		return l, nil
	}
	loc := &domain.WorkspaceLocation{
		ID: domain.NewID(domain.PrefixWorkspaceLocation), WorkspaceID: workspaceID,
		ExecutionHostID: domain.LocalHostID, MountAlias: "default",
		MountGeneration: "gen_seed", RepositoryIdentity: "repo_default",
		IsDefault: true, Status: domain.LocationReady,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.WorkspaceLocations().Create(ctx, loc); err != nil {
		return nil, err
	}
	return loc, nil
}
