package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Execution Host / Mount / Location（架构 RFC §4.1–4.4）─────────────
//
// Workspace 不等于路径；HostMount 才拥有 Host-local 路径。绝对路径只存在
// Host 本地 registry，永不进入 Control Plane 业务字段或远程 Run offer。

// LocalHostID 是受保护的本机 Host 固定 ID，由 Control Plane 启动时确保存在。
const LocalHostID = "host_local"

// LocalMountAlias 是本机默认 mount 别名（单 Workspace 历史库 bootstrap 用）。
const LocalMountAlias = "default"

type HostKind string

const (
	HostKindLocal  HostKind = "local"
	HostKindRemote HostKind = "remote"
)

// HostStatus 是连接/健康投影，不是 Run 状态。
type HostStatus string

const (
	HostStatusReady    HostStatus = "ready"
	HostStatusDegraded HostStatus = "degraded"
	HostStatusOffline  HostStatus = "offline"
)

// ExecutionHost 是稳定宿主身份，不等于一次 Runner 连接或进程。
type ExecutionHost struct {
	ID            string
	Name          string
	Kind          HostKind
	Status        HostStatus
	EnrollmentRef string // 受限存储；本机为空
	Version       int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// MountStatus 是 HostMount 广告投影的健康状态。
type MountStatus string

const (
	MountStatusReady       MountStatus = "ready"
	MountStatusUnavailable MountStatus = "unavailable"
)

// RefKind 是 DevelopmentContext 的逻辑开发选择。
type RefKind string

const (
	RefRoot     RefKind = "root"
	RefBranch   RefKind = "branch"
	RefWorktree RefKind = "worktree"
)

func (k RefKind) Valid() bool {
	return k == RefRoot || k == RefBranch || k == RefWorktree
}

// MountCheckout 是 Host 本地发现（git worktree list 等）生成的 opaque ref；
// Control Plane 保存广告投影，不生成、不解析其路径语义。
type MountCheckout struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"` // root | branch | worktree
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head,omitempty"`
}

// HostMount 是 Host 本地受信 registry 的广告投影（hello 上报，非 API 写入）。
type HostMount struct {
	ExecutionHostID    string          `json:"-"`
	Alias              string          `json:"alias"`
	RepositoryIdentity string          `json:"repository_identity"`
	DisplayLabel       string          `json:"display_label,omitempty"`
	DefaultBranch      string          `json:"default_branch,omitempty"`
	SupportedRefKinds  []RefKind       `json:"supported_ref_kinds,omitempty"`
	Checkouts          []MountCheckout `json:"checkouts,omitempty"`
	RegistryGeneration string          `json:"registry_generation"`
	Status             MountStatus     `json:"-"`
	LastSeenAt         time.Time       `json:"-"`
}

// LocationStatus 可随 Host/Mount 健康变化；identity/version 只由显式命令修改。
type LocationStatus string

const (
	LocationReady       LocationStatus = "ready"
	LocationDegraded    LocationStatus = "degraded"
	LocationUnavailable LocationStatus = "unavailable"
)

// WorkspaceLocation 把业务 Workspace 绑定到一个 HostMount。
// 不含任何绝对路径；mount_generation 快照防 alias 重指向。
type WorkspaceLocation struct {
	ID                 string
	WorkspaceID        string
	ExecutionHostID    string
	MountAlias         string
	MountGeneration    string
	RepositoryIdentity string
	IsDefault          bool
	Status             LocationStatus
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ── DevelopmentContext（RFC §4.5）─────────────────────────────────────

// DevelopmentContext 是 WorkItem 的逻辑开发选择，不是已解析 cwd。
// 不存在 path/cwd/absolute_path 字段。
type DevelopmentContext struct {
	WorkItemID          string
	ContextOwnerID      string // 默认根 Task
	WorkspaceLocationID string
	RefKind             RefKind
	BranchName          string
	CheckoutRef         string
	WorktreeRef         string
	BaseRevision        string
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ValidateRef 校验 ref 组合（与 work_item_contexts CHECK 约束同规则）：
// root 全空；branch 要求 branch_name+checkout_ref 且 worktree_ref 空；
// worktree 要求 worktree_ref 非空，branch 仅作显示/校验信息。
func ValidateRefCombo(kind RefKind, branch, checkoutRef, worktreeRef string) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: ref_kind %q", ErrValidation, kind)
	}
	switch kind {
	case RefRoot:
		if branch != "" || checkoutRef != "" || worktreeRef != "" {
			return fmt.Errorf("%w: root ref 不允许 branch/checkout/worktree 字段", ErrValidation)
		}
	case RefBranch:
		if branch == "" || checkoutRef == "" {
			return fmt.Errorf("%w: branch ref 需要 branch_name 与 checkout_ref", ErrValidation)
		}
		if worktreeRef != "" {
			return fmt.Errorf("%w: branch ref 不允许 worktree_ref", ErrValidation)
		}
	case RefWorktree:
		if worktreeRef == "" {
			return fmt.Errorf("%w: worktree ref 需要 worktree_ref", ErrValidation)
		}
	}
	return nil
}

// Validate 校验 DevelopmentContext 必填身份与 ref 组合。
func (c *DevelopmentContext) Validate() error {
	if c.WorkItemID == "" || c.WorkspaceLocationID == "" {
		return fmt.Errorf("%w: development context 缺 work_item/location", ErrValidation)
	}
	return ValidateRefCombo(c.RefKind, c.BranchName, c.CheckoutRef, c.WorktreeRef)
}

// ── ExecutionContextSnapshot（RFC §4.6）───────────────────────────────

// Snapshot schema 版本。legacy-v0 仅用于历史 Run 审计，不可 resume。
const (
	SnapshotSchemaV1      = "execution-context/v1"
	SnapshotSchemaLegacy  = "execution-context/legacy-v0"
	SnapshotDigestVersion = SnapshotSchemaV1
)

// SnapshotSource 记录 Snapshot 来源（RFC §4.7）。
type SnapshotSource string

const (
	SnapshotSourceCurrent    SnapshotSource = "current"
	SnapshotSourceInherited  SnapshotSource = "inherited"
	SnapshotSourceRetry      SnapshotSource = "retry"
	SnapshotSourceEvaluation SnapshotSource = "evaluation"
	SnapshotSourceRecovery   SnapshotSource = "recovery"
	SnapshotSourceLegacy     SnapshotSource = "legacy"
)

func (s SnapshotSource) Valid() bool {
	switch s {
	case SnapshotSourceCurrent, SnapshotSourceInherited, SnapshotSourceRetry,
		SnapshotSourceEvaluation, SnapshotSourceRecovery, SnapshotSourceLegacy:
		return true
	}
	return false
}

// ExecutionContextSnapshot 是每个 Run 唯一、不可变的执行上下文身份。
// INSERT 后禁止 UPDATE/DELETE（迁移以 trigger 强制）。不含宿主绝对路径。
// JSON 标签服务于 Runner v2 run.offer 线协议（contracts/runner/v2）。
type ExecutionContextSnapshot struct {
	ID                  string         `json:"id"`
	RunID               string         `json:"run_id,omitempty"`
	SchemaVersion       string         `json:"schema_version"`
	WorkspaceID         string         `json:"workspace_id,omitempty"`
	WorkspaceLocationID string         `json:"workspace_location_id"`
	LocationVersion     int            `json:"location_version"`
	MountGeneration     string         `json:"mount_generation"`
	ExecutionHostID     string         `json:"execution_host_id"`
	MountAlias          string         `json:"mount_alias"`
	RepositoryIdentity  string         `json:"repository_identity"`
	RefKind             RefKind        `json:"ref_kind"`
	BranchName          string         `json:"branch_name,omitempty"`
	CheckoutRef         string         `json:"checkout_ref,omitempty"`
	WorktreeRef         string         `json:"worktree_ref,omitempty"`
	BaseRevision        string         `json:"base_revision,omitempty"`
	ContextGeneration   int            `json:"context_generation"`
	Source              SnapshotSource `json:"source"`
	SourceSnapshotID    string         `json:"source_snapshot_id,omitempty"`
	SnapshotDigest      string         `json:"snapshot_digest"`
	CreatedAt           time.Time      `json:"created_at,omitempty"`
}

// identityFieldNames 是 digest 覆盖的执行身份字段全集（RFC §8.3：Host/Location/
// mount generation/repository/ref/context generation；不含 instruction/comment/policy）。
// 键集合固定——空值以 "" 参与，杜绝缺省/空串二义。
var identityFieldNames = []string{
	"base_revision", "branch_name", "checkout_ref", "context_generation",
	"execution_host_id", "location_version", "mount_alias", "mount_generation",
	"ref_kind", "repository_identity", "schema_version", "workspace_location_id",
	"worktree_ref",
}

// identityFields 导出 digest 输入。Runner 用本地 registry 事实替换
// mount_alias/mount_generation/repository_identity/checkout 系字段后重算，
// 控制面字段（location_version/context_generation 等）沿用 offer 值。
func (s *ExecutionContextSnapshot) identityFields() map[string]any {
	return map[string]any{
		"base_revision":         s.BaseRevision,
		"branch_name":           s.BranchName,
		"checkout_ref":          s.CheckoutRef,
		"context_generation":    int64(s.ContextGeneration),
		"execution_host_id":     s.ExecutionHostID,
		"location_version":      int64(s.LocationVersion),
		"mount_alias":           s.MountAlias,
		"mount_generation":      s.MountGeneration,
		"ref_kind":              string(s.RefKind),
		"repository_identity":   s.RepositoryIdentity,
		"schema_version":        s.SchemaVersion,
		"workspace_location_id": s.WorkspaceLocationID,
		"worktree_ref":          s.WorktreeRef,
	}
}

// ComputeDigest：sha256(schema_version + "\n" + canonicalJSON(identity))。
// canonical JSON 为 RFC8785 子集（仅 string/int 标量、键按字节序、无空白）；
// 身份字段受 schema 约束为可打印 ASCII，Control Plane 与 Runner 共用本函数，
// golden fixture 见 execution_context_test.go 与 contracts/runner/v2。
func (s *ExecutionContextSnapshot) ComputeDigest() string {
	canonical := canonicalIdentityJSON(s.identityFields())
	sum := sha256.Sum256([]byte(s.SchemaVersion + "\n" + canonical))
	return hex.EncodeToString(sum[:])
}

// canonicalIdentityJSON 输出确定性 JSON：键按字节序排列，值仅允许
// string/int64（身份字段闭集），无空白。
func canonicalIdentityJSON(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(k))
		b.WriteByte(':')
		switch v := fields[k].(type) {
		case string:
			b.WriteString(strconv.Quote(v))
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		case int:
			b.WriteString(strconv.Itoa(v))
		default:
			// 身份字段闭集之外的类型是编程错误，fail loud。
			panic(fmt.Sprintf("execution context digest: unsupported identity field %s (%T)", k, v))
		}
	}
	b.WriteByte('}')
	return b.String()
}

// Validate 校验 Snapshot 身份完整性与 ref 组合。legacy-v0 放宽
// （历史回填只要求 run 关联与 schema 标记，身份字段可为空，永不用于分派）。
func (s *ExecutionContextSnapshot) Validate() error {
	if s.SchemaVersion == SnapshotSchemaLegacy {
		if s.RunID == "" {
			return fmt.Errorf("%w: legacy snapshot 缺 run_id", ErrValidation)
		}
		return nil
	}
	if s.SchemaVersion != SnapshotSchemaV1 {
		return fmt.Errorf("%w: snapshot schema_version %q", ErrValidation, s.SchemaVersion)
	}
	if s.ID == "" || s.RunID == "" || s.WorkspaceID == "" || s.WorkspaceLocationID == "" ||
		s.ExecutionHostID == "" || s.MountAlias == "" || s.RepositoryIdentity == "" ||
		s.MountGeneration == "" {
		return fmt.Errorf("%w: snapshot 身份字段不完整", ErrValidation)
	}
	if !s.Source.Valid() || s.Source == SnapshotSourceLegacy {
		return fmt.Errorf("%w: snapshot source %q", ErrValidation, s.Source)
	}
	if err := ValidateRefCombo(s.RefKind, s.BranchName, s.CheckoutRef, s.WorktreeRef); err != nil {
		return err
	}
	if s.SnapshotDigest == "" || s.SnapshotDigest != s.ComputeDigest() {
		return fmt.Errorf("%w: snapshot digest 缺失或与身份字段不一致", ErrValidation)
	}
	return nil
}

// CloneForRun 派生 retry/evaluation/recovery/inherited Snapshot：
// 身份字段原样克隆（digest 不变），重新分配 ID/RunID/时间戳。
// 调用方负责在同一事务内 INSERT；派生 Snapshot 永不回写原 Snapshot。
func (s *ExecutionContextSnapshot) CloneForRun(runID string, source SnapshotSource, now time.Time) *ExecutionContextSnapshot {
	clone := *s
	clone.ID = NewID(PrefixCtxSnapshot)
	clone.RunID = runID
	clone.Source = source
	clone.SourceSnapshotID = s.ID
	clone.CreatedAt = now
	return &clone
}

// ── ResolvedExecutionContext（RFC §4.6，仅进程内）──────────────────────

// ResolvedExecutionContext 是 Host resolver 产物的进程内可信上下文：
// 由 Host 本地 registry 解析而来，永不落库、永不进入线协议。
// Adapter 只许读 CWD（不变式 §5.1.9）。
type ResolvedExecutionContext struct {
	SnapshotID string
	// AuthorizedRoot 是 Host 本地受信根（registry 配置的真实路径）。
	AuthorizedRoot string
	// CWD 是本 Run 的工作目录：root ref 即 AuthorizedRoot；branch/worktree
	// ref 为 resolver 校验过的 checkout 路径（realpath 收敛，防 symlink/.. 逃逸）。
	CWD                string
	RepositoryIdentity string
	RefKind            RefKind
}
