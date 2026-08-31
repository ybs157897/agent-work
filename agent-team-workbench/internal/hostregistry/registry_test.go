package hostregistry

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// requireGit 跳过无 git 的环境（发现与解析测试依赖真实 git 仓库）。
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git 不可用，跳过受信本地发现测试")
	}
}

// gitRun 在 dir 内执行 git 命令，失败即 Fatal。
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newTestRepo 建一个真实 git 仓库（main 分支 + 1 commit）和一个链接 worktree
// （分支 feature/x），返回 repo 与 worktree 路径。
func newTestRepo(t *testing.T) (repo, wt string) {
	t.Helper()
	requireGit(t)
	base := t.TempDir()
	repo = filepath.Join(base, "repo")
	wt = filepath.Join(base, "wt-feature")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "init")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feature/x", wt)
	return repo, wt
}

// writeRegistry 落一份 registry yaml 并 Load，返回 registry 与文件路径。
func writeRegistry(t *testing.T, mounts string) (*Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.yaml")
	content := "version: 1\nmounts:\n" + mounts
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load %s: %v", content, err)
	}
	return r, path
}

// realPath 返回 p 的 realpath 口径（macOS 的 t.TempDir 形如 /var/...，其
// realpath 是 /private/var/...；registry/发现一律以 realpath 为准）。
func realPath(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("realpath %s: %v", p, err)
	}
	return real
}

// rootSnapshot 构造 ref_kind=root 的合法快照（digest 与身份字段自洽）。
func rootSnapshot(alias, generation, identity string) *domain.ExecutionContextSnapshot {
	s := &domain.ExecutionContextSnapshot{
		ID:                  domain.NewID(domain.PrefixCtxSnapshot),
		RunID:               domain.NewID(domain.PrefixRun),
		SchemaVersion:       domain.SnapshotSchemaV1,
		WorkspaceID:         "ws_1",
		WorkspaceLocationID: "wsloc_1",
		LocationVersion:     3,
		MountGeneration:     generation,
		ExecutionHostID:     "host_test",
		MountAlias:          alias,
		RepositoryIdentity:  identity,
		RefKind:             domain.RefRoot,
		ContextGeneration:   1,
		Source:              domain.SnapshotSourceCurrent,
	}
	s.SnapshotDigest = s.ComputeDigest()
	return s
}

// TestLoadCanonicalizesRootAndGeneration：root 必须 realpath 收敛（symlink 与
// .. 都不许带走字面路径）；generation 稳定可复算、内容变化即变化。
func TestLoadCanonicalizesRootAndGeneration(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "repo")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(base, "registry.yaml")
	content := "version: 1\nmounts:\n  - alias: agent-work\n    root: " +
		filepath.Join(link, "..", "link", ".", "") + "\n    repository_identity: repo_x\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := r.mounts[0].Root; got != realPath(t, real) {
		t.Fatalf("root 未 realpath 收敛：got %q want %q", got, realPath(t, real))
	}

	// 同内容重读：generation 稳定；identity 变化：generation 变化。
	r2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if r2.generation != r.generation {
		t.Fatal("同一 registry 内容两次 Load 的 generation 必须一致")
	}
	alt := strings.Replace(content, "repo_x", "repo_y", 1)
	altPath := filepath.Join(base, "alt.yaml")
	if err := os.WriteFile(altPath, []byte(alt), 0o644); err != nil {
		t.Fatal(err)
	}
	r3, err := Load(altPath)
	if err != nil {
		t.Fatal(err)
	}
	if r3.generation == r.generation {
		t.Fatal("repository_identity 变化必须改变 generation")
	}
}

// TestLoadRejectsBadConfig：version/重复 alias/缺失目录均 fail closed。
func TestLoadRejectsBadConfig(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, content string }{
		{"version 非法", "version: 2\nmounts: []\n"},
		{"alias 重复", "version: 1\nmounts:\n  - alias: a\n    root: " + dir + "\n    repository_identity: r1\n  - alias: a\n    root: " + dir + "\n    repository_identity: r2\n"},
		{"root 缺失", "version: 1\nmounts:\n  - alias: a\n    root: " + filepath.Join(base, "nope") + "\n    repository_identity: r1\n"},
	}
	for _, tc := range cases {
		path := filepath.Join(base, "case.yaml")
		if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("%s：Load 必须失败", tc.name)
		}
	}
}

// TestAdvertiseProjectsCheckouts：广告含 root/链接 worktree 两类 opaque
// checkout；ref 稳定可复算；投影 marshal 后不得出现宿主绝对路径。
func TestAdvertiseProjectsCheckouts(t *testing.T) {
	repo, wt := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_aw\n")

	mounts := r.Advertise()
	if len(mounts) != 1 {
		t.Fatalf("Advertise 应投影 1 个 mount，实际 %d", len(mounts))
	}
	m := mounts[0]
	if m.Alias != "agent-work" || m.RepositoryIdentity != "repo_aw" {
		t.Fatalf("广告身份错误: %+v", m)
	}
	if m.RegistryGeneration == "" || len(m.SupportedRefKinds) != 3 {
		t.Fatalf("generation/ref kinds 缺失: %+v", m)
	}

	refs := map[string]domain.MountCheckout{}
	for _, c := range m.Checkouts {
		refs[c.Ref] = c
	}
	rootC, ok := refs[rootCheckoutRef]
	if !ok || rootC.Kind != "root" || rootC.Branch != "main" {
		t.Fatalf("root checkout 缺失或形态错误: %+v", m.Checkouts)
	}
	var wtRef string
	for ref, c := range refs {
		if c.Kind == "worktree" {
			wtRef = ref
			if c.Branch != "feature/x" {
				t.Fatalf("worktree branch = %q，期望 feature/x", c.Branch)
			}
		}
	}
	if wtRef == "" || !strings.HasPrefix(wtRef, "wt_") || len(wtRef) != len("wt_")+16 {
		t.Fatalf("worktree ref 形态错误: %q（须为 wt_+sha256[:16]）", wtRef)

	}
	// 再次广告：opaque ref 稳定。
	if again := r.Advertise(); again[0].Checkouts[1].Ref != m.Checkouts[1].Ref {
		t.Fatal("同一 worktree 的 opaque ref 必须稳定可复算")
	}

	// 广告投影不含宿主路径。
	blob, _ := json.Marshal(mounts)
	if strings.Contains(string(blob), repo) || strings.Contains(string(blob), wt) {
		t.Fatal("广告投影不得携带宿主绝对路径")
	}
}

// TestAdvertiseFailsLoudOnNonGitRoot：发现失败必须 fail loud（panic）。
func TestAdvertiseFailsLoudOnNonGitRoot(t *testing.T) {
	requireGit(t)
	plain := t.TempDir() // 非 git 目录
	r, _ := writeRegistry(t, "  - alias: plain\n    root: "+plain+"\n    repository_identity: repo_p\n")

	defer func() {
		if recover() == nil {
			t.Fatal("非 git root 的发现失败必须 fail loud")
		}
	}()
	r.Advertise()
}

// TestResolveRootBranchWorktree：三类 ref 正向解析，CWD 落在授权路径。
func TestResolveRootBranchWorktree(t *testing.T) {
	repo, wt := newTestRepo(t)
	repoReal, wtReal := realPath(t, repo), realPath(t, wt)
	r, _ := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_aw\n")
	gen := r.generation

	root := rootSnapshot("agent-work", gen, "repo_aw")
	res, err := r.Resolve(root)
	if err != nil {
		t.Fatalf("root Resolve: %v", err)
	}
	if res.CWD != repoReal || res.AuthorizedRoot != repoReal || res.RefKind != domain.RefRoot {
		t.Fatalf("root 解析产物错误: %+v", res)
	}

	// branch：先广告拿 opaque checkout ref，再构造 snapshot。
	adv := r.Advertise()[0]
	var branchCheckout domain.MountCheckout
	for _, c := range adv.Checkouts {
		if c.Kind == "root" {
			branchCheckout = c
		}
	}
	bs := rootSnapshot("agent-work", gen, "repo_aw")
	bs.RefKind = domain.RefBranch
	bs.BranchName = branchCheckout.Branch // main（在主工作树 checkout）
	bs.CheckoutRef = branchCheckout.Ref
	bs.SnapshotDigest = bs.ComputeDigest()
	res, err = r.Resolve(bs)
	if err != nil {
		t.Fatalf("branch Resolve: %v", err)
	}
	if res.CWD != repoReal {
		t.Fatalf("branch 应解析到主工作树路径，实际 %q", res.CWD)
	}

	// worktree：按 worktree_ref 命中。
	var wtCheckout domain.MountCheckout
	for _, c := range adv.Checkouts {
		if c.Kind == "worktree" {
			wtCheckout = c
		}
	}
	ws := rootSnapshot("agent-work", gen, "repo_aw")
	ws.RefKind = domain.RefWorktree
	ws.WorktreeRef = wtCheckout.Ref
	ws.CheckoutRef = wtCheckout.Ref
	ws.SnapshotDigest = ws.ComputeDigest()
	res, err = r.Resolve(ws)
	if err != nil {
		t.Fatalf("worktree Resolve: %v", err)
	}
	if res.CWD != wtReal {
		t.Fatalf("worktree CWD = %q，期望 %q", res.CWD, wtReal)
	}
}

// TestResolveFailures：workspace 族逐项 fail closed。
func TestResolveFailures(t *testing.T) {
	repo, wt := newTestRepo(t)
	r, regPath := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_aw\n")
	gen := r.generation

	expectCode := func(t *testing.T, reg *Registry, s *domain.ExecutionContextSnapshot, wantCode string) {
		t.Helper()
		_, err := reg.Resolve(s)
		var re *ResolveError
		if !asResolveError(err, &re) {
			t.Fatalf("期望 ResolveError（%s），实际 %v", wantCode, err)
		}
		if re.Code != wantCode {
			t.Fatalf("错误码 = %q，期望 %q（%v）", re.Code, wantCode, err)
		}
	}

	// alias 未广告。
	expectCode(t, r, rootSnapshot("other", gen, "repo_aw"), CodeMountNotAdvertised)

	// generation 变化（default_branch 改动会换 generation，identity 不变）。
	alt := "version: 1\nmounts:\n  - alias: agent-work\n    root: " + repo +
		"\n    repository_identity: repo_aw\n    default_branch: main\n"
	if err := os.WriteFile(regPath, []byte(alt), 0o644); err != nil {
		t.Fatal(err)
	}
	r2, err := Load(regPath)
	if err != nil {
		t.Fatal(err)
	}
	if r2.generation == gen {
		t.Fatal("default_branch 变化必须改变 generation")
	}
	// 携带旧 generation 的 snapshot 打到新 registry：拒绝。
	expectCode(t, r2, rootSnapshot("agent-work", gen, "repo_aw"), CodeGenerationChanged)

	// repository identity 不匹配（generation 用新值绕过前一项）。
	expectCode(t, r2, rootSnapshot("agent-work", r2.generation, "repo_other"), CodeRepositoryMismatch)

	// digest 失配：身份字段被改而 digest 未重算。
	tampered := rootSnapshot("agent-work", r2.generation, "repo_aw")
	tampered.BaseRevision = "deadbeef"
	expectCode(t, r2, tampered, CodeDigestMismatch)

	// worktree_ref 不存在（已删除/移动）。
	ghost := rootSnapshot("agent-work", r2.generation, "repo_aw")
	ghost.RefKind = domain.RefWorktree
	ghost.WorktreeRef = "wt_does_not_exist"
	expectCode(t, r2, ghost, CodeRefNotResolvable)

	// branch 0 命中。
	nobranch := rootSnapshot("agent-work", r2.generation, "repo_aw")
	nobranch.RefKind = domain.RefBranch
	nobranch.BranchName = "no/such-branch"
	nobranch.CheckoutRef = "wt_whatever"
	nobranch.SnapshotDigest = nobranch.ComputeDigest()
	expectCode(t, r2, nobranch, CodeRefNotResolvable)

	// worktree 目录被删（git 元数据仍在）：发现整体 fail loud → 拒绝。
	// （先取 ref 再删目录：单条目缺失会使整个发现失败，这正是 fail loud。）
	adv0 := r2.Advertise()[0]
	ws := rootSnapshot("agent-work", r2.generation, "repo_aw")
	ws.RefKind = domain.RefWorktree
	for _, c := range adv0.Checkouts {
		if c.Kind == "worktree" {
			ws.WorktreeRef = c.Ref
		}
	}
	if ws.WorktreeRef == "" {
		t.Fatal("未发现 worktree checkout，测试前置失败")
	}
	ws.SnapshotDigest = ws.ComputeDigest()
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	expectCode(t, r2, ws, CodeRefNotResolvable)
}

// TestResolveAuthorizedSetMembership：授权集合外的路径不得作为 CWD 产出
// （防 symlink/.. 逃逸的后盾：realpath 口径的集合成员判断，不做前缀比较）。
func TestResolveAuthorizedSetMembership(t *testing.T) {
	repo, _ := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_aw\n")
	m := r.mounts[0]

	if !authorizedPath(m, realPath(t, repo)) {
		t.Fatal("root 本身必须在授权集合内")
	}
	outside := realPath(t, t.TempDir())
	if authorizedPath(m, outside) {
		t.Fatal("授权集合外路径不得通过成员判断")
	}
	// 前缀相似的路径也不得通过（.. 语义由 realpath 集合成员判断兜底）。
	if authorizedPath(m, realPath(t, repo)+"-sibling") {
		t.Fatal("字符串前缀近似路径不得通过")
	}
}

// TestAcquireCheckoutLease：同 checkout 串行、不同 worktree 并行、release 幂等。
func TestAcquireCheckoutLease(t *testing.T) {
	repo, _ := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_aw\n")
	gen := r.generation

	// worktree snapshot 构造。
	adv := r.Advertise()[0]
	var wtRef string
	for _, c := range adv.Checkouts {
		if c.Kind == "worktree" {
			wtRef = c.Ref
		}
	}
	wtSnap := func() *domain.ExecutionContextSnapshot {
		s := rootSnapshot("agent-work", gen, "repo_aw")
		s.RefKind = domain.RefWorktree
		s.WorktreeRef = wtRef
		s.SnapshotDigest = s.ComputeDigest()
		return s
	}

	release1, err := r.Acquire(wtSnap())
	if err != nil {
		t.Fatalf("首租应成功: %v", err)
	}
	_, err = r.Acquire(wtSnap())
	var re *ResolveError
	if !asResolveError(err, &re) || re.Code != CodeCheckoutBusy {
		t.Fatalf("同 checkout 第二租应 workspace_checkout_busy，实际 %v", err)
	}

	// root checkout 是另一把锁：可并行获取。
	rootSnap := rootSnapshot("agent-work", gen, "repo_aw")
	releaseRoot, err := r.Acquire(rootSnap)
	if err != nil {
		t.Fatalf("root 租约应可与 worktree 并行: %v", err)
	}

	release1()
	release1() // 幂等
	release2, err := r.Acquire(wtSnap())
	if err != nil {
		t.Fatalf("release 后应可再租: %v", err)
	}
	_ = release2
	releaseRoot()
}

// asResolveError 用 errors.As 语义提取 ResolveError（本包内避免重复 import 别名）。
func asResolveError(err error, target **ResolveError) bool {
	for err != nil {
		if re, ok := err.(*ResolveError); ok {
			*target = re
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
