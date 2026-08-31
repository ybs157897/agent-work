// Package hostregistry 实现 Host 本地受信 registry 的加载、广告与执行上下文解析
// （任务控制面 RFC §4.3/§7.5，契约 contracts/runner/v2）。
//
// 职责边界：
//   - Load：读本机受信 yaml（version:1 + mounts[alias/root/repository_identity/
//     default_branch?]），root 一律 realpath 收敛；generation 为 registry 静态
//     身份的 sha256（算法见 Registry.generationCompute，稳定可复算）。
//   - Advertise：把 registry 投影为 hello 广告（alias/identity/generation/
//     checkouts）；checkouts 由受信本地发现（git worktree list --porcelain）
//     生成 opaque ref，宿主绝对路径不进广告。
//   - Resolve：按 RFC §7.5 Runner 接受条件逐项校验（alias/generation/identity/
//     ref 规则/digest 重算/realpath 授权集合），全部通过才产出进程内
//     ResolvedExecutionContext；任一失败返回带 code 的 workspace 族错误。
//   - Acquire：并发执行租约——同 checkout 第一版单活跃 Run，不同 worktree
//     refs 可并行（进程内 map + 互斥锁）。
//
// 本包只依赖 domain；被 cmd/runnerd 与 control-plane（host_local 轨）共用。
package hostregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"gopkg.in/yaml.v3"
)

// workspace 族错误码：Resolve/Acquire 失败的稳定标识。Runner 侧映射为
// run.reject(reason=workspace)，控制面据 code 落 failed(family=workspace)。
const (
	CodeMountNotAdvertised = "workspace_mount_not_advertised"
	CodeGenerationChanged  = "workspace_mount_generation_changed"
	CodeRepositoryMismatch = "workspace_repository_mismatch"
	CodeRefNotResolvable   = "workspace_ref_not_resolvable"
	CodeDigestMismatch     = "workspace_digest_mismatch"
	CodeCheckoutBusy       = "workspace_checkout_busy"
)

// ResolveError 携带 workspace 族 code 的解析失败；errors.As 可提取。
type ResolveError struct {
	Code    string
	Message string
}

func (e *ResolveError) Error() string {
	return fmt.Sprintf("hostregistry: %s: %s", e.Code, e.Message)
}

func resolveErr(code, format string, args ...any) *ResolveError {
	return &ResolveError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// rootCheckoutRef 是 root 工作树在广告与解析中的固定 opaque ref。
const rootCheckoutRef = "root"

// yamlFile / yamlMount 是 registry 文件形态（RFC §4.3）：
//
//	version: 1
//	mounts:
//	  - alias: agent-work
//	    root: /host-private/absolute/path
//	    repository_identity: repo_ybs_agent_work
type yamlFile struct {
	Version int         `yaml:"version"`
	Mounts  []yamlMount `yaml:"mounts"`
}

type yamlMount struct {
	Alias              string `yaml:"alias"`
	Root               string `yaml:"root"`
	RepositoryIdentity string `yaml:"repository_identity"`
	DefaultBranch      string `yaml:"default_branch,omitempty"`
	DisplayLabel       string `yaml:"display_label,omitempty"`
}

// checkout 是受信本地发现产物：Ref 是 opaque 稳定标识
// （root 工作树固定 "root"；链接 worktree 为 "wt_"+sha256(相对 root 路径)[:16]），
// Path 恒为 realpath 收敛后的本地路径，永不离开本进程。
type checkout struct {
	Ref    string
	Kind   string // root | worktree（branch 由 Branch 字段表达，见 resolveBranch）
	Branch string
	Head   string
	Path   string
}

// mount 是 registry 内的单个挂载点：Root 恒为 realpath。
type mount struct {
	Alias              string
	Root               string
	RepositoryIdentity string
	DefaultBranch      string
	DisplayLabel       string
}

// Registry 是 Host 本地受信 registry 的进程内形态。
type Registry struct {
	mu         sync.Mutex
	mounts     []*mount // 按 alias 升序（Load 时排序，canonical 输入稳定）
	generation string   // sha256(canonical registry 静态身份)，见 generationCompute
	busy       map[string]bool
}

// New 返回空 registry（未配置 HOST_REGISTRY 的 Runner 启动形态；
// 一切 Resolve 都会以 workspace_mount_not_advertised 失败——fail closed）。
func New() *Registry {
	return &Registry{busy: map[string]bool{}}
}

// Load 读取并校验 registry yaml。root 经 realpath（EvalSymlinks）收敛，
// 防 symlink/.. 逃逸；alias 必须唯一非空；version 必须为 1。
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hostregistry: 读取 %s 失败: %w", path, err)
	}
	var f yamlFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("hostregistry: 解析 %s 失败: %w", path, err)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("hostregistry: %s version=%d，仅支持 version: 1", path, f.Version)
	}
	r := New()
	seen := map[string]bool{}
	for i, m := range f.Mounts {
		if m.Alias == "" || m.RepositoryIdentity == "" || m.Root == "" {
			return nil, fmt.Errorf("hostregistry: mounts[%d] 缺 alias/root/repository_identity", i)
		}
		if seen[m.Alias] {
			return nil, fmt.Errorf("hostregistry: alias %q 重复（Host 级唯一约束）", m.Alias)
		}
		seen[m.Alias] = true
		abs, err := filepath.Abs(m.Root)
		if err != nil {
			return nil, fmt.Errorf("hostregistry: mounts[%d] root 非法: %w", i, err)
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("hostregistry: mount %q root realpath 失败（目录必须存在）: %w", m.Alias, err)
		}
		fi, err := os.Stat(real)
		if err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("hostregistry: mount %q root %q 不是目录", m.Alias, real)
		}
		r.mounts = append(r.mounts, &mount{
			Alias: m.Alias, Root: real, RepositoryIdentity: m.RepositoryIdentity,
			DefaultBranch: m.DefaultBranch, DisplayLabel: m.DisplayLabel,
		})
	}
	sort.Slice(r.mounts, func(i, j int) bool { return r.mounts[i].Alias < r.mounts[j].Alias })
	r.generation = generationCompute(f.Version, r.mounts)
	return r, nil
}

// generationCompute 计算 registry generation：
//
//	sha256( "hostregistry/v1\n" + Σ mount(alias 升序) "alias:<a>\nroot:<realpath>\nrepository:<id>\ndefault_branch:<b>\n" ) 的 hex。
//
// 只覆盖静态身份（alias/root/identity/default_branch）；checkouts（随提交变化）
// 不参与——generation 变化语义是「管理员改绑/换根」，不是仓库前进了。
// 控制面可用同一算法对同一文件复算。
func generationCompute(version int, mounts []*mount) string {
	var b strings.Builder
	b.WriteString("hostregistry/v")
	b.WriteString(strconv.Itoa(version))
	b.WriteByte('\n')
	for _, m := range mounts {
		b.WriteString("alias:" + m.Alias + "\n")
		b.WriteString("root:" + m.Root + "\n")
		b.WriteString("repository:" + m.RepositoryIdentity + "\n")
		b.WriteString("default_branch:" + m.DefaultBranch + "\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Advertise 投影为 hello 广告（RFC §4.3：只含 alias/identity/generation/
// capabilities/checkouts，宿主绝对路径不进协议）。checkouts 现场做受信本地
// 发现；发现失败 fail loud（签名无 error 返回，直接 panic——半残广告比
// 拒绝连接更危险）。ExecutionHostID/Status/LastSeenAt 由调用方（连接态）补齐。
func (r *Registry) Advertise() []domain.HostMount {
	r.mu.Lock()
	mounts := make([]*mount, len(r.mounts))
	copy(mounts, r.mounts)
	generation := r.generation
	r.mu.Unlock()

	out := make([]domain.HostMount, 0, len(mounts))
	for _, m := range mounts {
		checkouts, err := discoverCheckouts(m.Root)
		if err != nil {
			panic(fmt.Sprintf("hostregistry: mount %q checkout 发现失败（fail loud）: %v", m.Alias, err))
		}
		refs := make([]domain.MountCheckout, 0, len(checkouts))
		for _, c := range checkouts {
			refs = append(refs, domain.MountCheckout{Ref: c.Ref, Kind: c.Kind, Branch: c.Branch, Head: c.Head})
		}
		out = append(out, domain.HostMount{
			Alias:              m.Alias,
			RepositoryIdentity: m.RepositoryIdentity,
			DisplayLabel:       m.DisplayLabel,
			DefaultBranch:      m.DefaultBranch,
			SupportedRefKinds:  []domain.RefKind{domain.RefRoot, domain.RefBranch, domain.RefWorktree},
			Checkouts:          refs,
			RegistryGeneration: generation,
		})
	}
	return out
}

// discoverCheckouts 解析 `git -C <root> worktree list --porcelain`：
// 首条目是主工作树 → 固定 ref "root"；其余为链接 worktree →
// ref = "wt_" + sha256(相对 root 的路径)[:16]（同一路径稳定可复算）。
// 所有路径经 realpath 收敛。
func discoverCheckouts(root string) ([]checkout, error) {
	cmd := exec.Command("git", "-C", root, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list 失败: %w", err)
	}
	var checkouts []checkout
	var cur *checkout
	flush := func() {
		if cur != nil && cur.Path != "" {
			checkouts = append(checkouts, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			p := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			real, rerr := filepath.EvalSymlinks(p)
			if rerr != nil {
				return nil, fmt.Errorf("worktree %q realpath 失败: %w", p, rerr)
			}
			cur = &checkout{Path: real}
		case cur == nil:
			continue
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			cur.Branch = ""
		case line == "bare":
			cur.Branch = ""
		}
	}
	flush()
	if len(checkouts) == 0 {
		return nil, errors.New("git worktree list 无条目")
	}
	// 主工作树固定 ref；链接 worktree 用相对路径哈希派生 opaque ref。
	for i := range checkouts {
		if i == 0 {
			checkouts[i].Ref = rootCheckoutRef
			checkouts[i].Kind = "root"
			continue
		}
		rel := checkouts[i].Path
		if r, err := filepath.Rel(root, checkouts[i].Path); err == nil {
			rel = r
		}
		sum := sha256.Sum256([]byte(rel))
		checkouts[i].Ref = "wt_" + hex.EncodeToString(sum[:])[:16]
		checkouts[i].Kind = "worktree"
	}
	return checkouts, nil
}

// Resolve 按 RFC §7.5 Runner 接受条件校验 offer 快照并产出进程内可信上下文：
// alias 在 registry → generation 一致 → repository_identity 匹配 → ref 规则
// （root 直取；branch 唯一 checkout 精确匹配；worktree 按 ref 命中）→ 用本地
// 事实重建 identity 重算 digest → CWD realpath 后仍在授权集合内。
func (r *Registry) Resolve(snapshot *domain.ExecutionContextSnapshot) (domain.ResolvedExecutionContext, error) {
	if snapshot == nil {
		return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable, "snapshot 为空")
	}
	r.mu.Lock()
	generation := r.generation
	var m *mount
	for _, c := range r.mounts {
		if c.Alias == snapshot.MountAlias {
			m = c
			break
		}
	}
	r.mu.Unlock()
	if m == nil {
		return domain.ResolvedExecutionContext{}, resolveErr(CodeMountNotAdvertised,
			"mount alias %q 未由本 Host 广告", snapshot.MountAlias)
	}
	if generation != snapshot.MountGeneration {
		return domain.ResolvedExecutionContext{}, resolveErr(CodeGenerationChanged,
			"mount %q generation 已变化（snapshot=%s，本地=%s）——alias 可能被重指向",
			snapshot.MountAlias, snapshot.MountGeneration, generation)
	}
	if m.RepositoryIdentity != snapshot.RepositoryIdentity {
		return domain.ResolvedExecutionContext{}, resolveErr(CodeRepositoryMismatch,
			"mount %q repository identity 不匹配（snapshot=%s，本地=%s）",
			snapshot.MountAlias, snapshot.RepositoryIdentity, m.RepositoryIdentity)
	}

	var cwd string
	switch snapshot.RefKind {
	case domain.RefRoot:
		cwd = m.Root
	case domain.RefBranch:
		checkouts, err := discoverCheckouts(m.Root)
		if err != nil {
			return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable,
				"mount %q checkout 发现失败: %v", m.Alias, err)
		}
		var hits []checkout
		for _, c := range checkouts {
			if c.Branch != "" && c.Branch == snapshot.BranchName {
				hits = append(hits, c)
			}
		}
		if len(hits) != 1 {
			return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable,
				"branch %q 命中 %d 个 checkout（要求恰好 1）", snapshot.BranchName, len(hits))
		}
		if hits[0].Ref != snapshot.CheckoutRef {
			return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable,
				"branch %q checkout ref 不匹配（snapshot=%s，本地=%s）",
				snapshot.BranchName, snapshot.CheckoutRef, hits[0].Ref)
		}
		cwd = hits[0].Path
	case domain.RefWorktree:
		checkouts, err := discoverCheckouts(m.Root)
		if err != nil {
			return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable,
				"mount %q checkout 发现失败: %v", m.Alias, err)
		}
		var hit *checkout
		for i := range checkouts {
			if checkouts[i].Ref == snapshot.WorktreeRef {
				hit = &checkouts[i]
				break
			}
		}
		if hit == nil {
			return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable,
				"worktree ref %q 不在本 Host 发现结果中（可能已删除/移动）", snapshot.WorktreeRef)
		}
		cwd = hit.Path
	default:
		return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable, "ref_kind %q 非法", snapshot.RefKind)
	}

	// digest 重算：本地事实（alias/generation/identity）替换后重算，控制面侧
	// 字段（branch_name/base_revision/location_version/context_generation）沿用
	// offer 值。失配 = offer 身份与快照 digest 不自洽，fail closed。
	rebuilt := *snapshot
	rebuilt.MountAlias = m.Alias
	rebuilt.MountGeneration = generation
	rebuilt.RepositoryIdentity = m.RepositoryIdentity
	if got := rebuilt.ComputeDigest(); got != snapshot.SnapshotDigest {
		return domain.ResolvedExecutionContext{}, resolveErr(CodeDigestMismatch,
			"snapshot digest 重算失配（offer=%s，本地重算=%s）", snapshot.SnapshotDigest, got)
	}

	// realpath 防逃逸：CWD 必须 realpath 后仍落在授权集合（root + 发现的
	// checkout 路径）内；字符串前缀判断不做（RFC §13）。
	real, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable,
			"cwd %q realpath 失败: %v", cwd, err)
	}
	if !authorizedPath(m, real) {
		return domain.ResolvedExecutionContext{}, resolveErr(CodeRefNotResolvable,
			"cwd %q 逃逸出授权集合", real)
	}

	return domain.ResolvedExecutionContext{
		SnapshotID:         snapshot.ID,
		AuthorizedRoot:     m.Root,
		CWD:                real,
		RepositoryIdentity: m.RepositoryIdentity,
		RefKind:            snapshot.RefKind,
	}, nil
}

// authorizedPath 报告 real 是否属于授权集合：mount root 本身，或本地发现的
// 任一 checkout 路径（均 realpath 口径）。
func authorizedPath(m *mount, real string) bool {
	if real == m.Root {
		return true
	}
	checkouts, err := discoverCheckouts(m.Root)
	if err != nil {
		return false
	}
	for _, c := range checkouts {
		if c.Path == real {
			return true
		}
	}
	return false
}

// Acquire 获取 checkout 并发执行租约：同 checkout（alias+ref）第一版单活跃
// Run，不同 worktree refs 可并行。占用返回 *ResolveError{CodeCheckoutBusy}。
// 返回的 release 函数幂等安全（重复调用只释放一次）。
func (r *Registry) Acquire(snapshot *domain.ExecutionContextSnapshot) (func(), error) {
	if snapshot == nil {
		return nil, resolveErr(CodeRefNotResolvable, "snapshot 为空")
	}
	key := snapshot.MountAlias + "/" + checkoutKey(snapshot)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy == nil {
		r.busy = map[string]bool{}
	}
	if r.busy[key] {
		return nil, resolveErr(CodeCheckoutBusy,
			"checkout %s 正被另一个 Run 执行（同 checkout 单活跃）", key)
	}
	r.busy[key] = true
	released := false
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !released {
			released = true
			delete(r.busy, key)
		}
	}, nil
}

// checkoutKey 是并发租约的粒度键：root → 固定 root ref；branch/worktree →
// 各自的 opaque checkout ref。
func checkoutKey(snapshot *domain.ExecutionContextSnapshot) string {
	switch snapshot.RefKind {
	case domain.RefRoot:
		return rootCheckoutRef
	case domain.RefBranch:
		if snapshot.CheckoutRef != "" {
			return snapshot.CheckoutRef
		}
		return "branch:" + snapshot.BranchName
	default:
		return snapshot.WorktreeRef
	}
}
