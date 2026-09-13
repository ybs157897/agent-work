package hostregistry

import (
	"os"
	"path/filepath"
	"testing"
)

// 只读预览的可读根：Run 自己的 checkout 在前，同一仓库的 worktree 随后；
// 其他仓库/目录永不进入集合。
func TestReadableRootsCoversOwnCheckoutAndRepositoryWorktrees(t *testing.T) {
	repo, wt := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: default\n    root: "+repo+"\n    repository_identity: repo_test\n")

	roots, err := r.ReadableRoots(rootSnapshot("default", r.generation, "repo_test"))
	if err != nil {
		t.Fatalf("ReadableRoots: %v", err)
	}
	if len(roots) < 2 {
		t.Fatalf("可读根应含主工作树与 worktree，得到 %v", roots)
	}
	if roots[0] != realPath(t, repo) {
		t.Fatalf("root ref 的首个可读根必须是授权根，得到 %q", roots[0])
	}
	want := realPath(t, wt)
	found := false
	for _, root := range roots {
		if root == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("可读根缺少本仓库 worktree %q: %v", want, roots)
	}

	// 未被授权的目录不得出现：另一个仓库即便存在也不在集合里。
	other := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if root == realPath(t, other) {
			t.Fatalf("未授权目录进入可读根: %v", roots)
		}
	}
}

// worktree 目录已被删除（未 prune）时跳过该条，主工作树仍可用。
func TestReadableRootsSkipsMissingWorktree(t *testing.T) {
	repo, wt := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: default\n    root: "+repo+"\n    repository_identity: repo_test\n")
	removed := realPath(t, wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	roots, err := r.ReadableRoots(rootSnapshot("default", r.generation, "repo_test"))
	if err != nil {
		t.Fatalf("ReadableRoots: %v", err)
	}
	if len(roots) == 0 || roots[0] != realPath(t, repo) {
		t.Fatalf("worktree 缺失不应关掉主工作树，得到 %v", roots)
	}
	for _, root := range roots {
		if root == removed {
			t.Fatalf("已删除的 worktree 不应出现在可读根: %v", roots)
		}
	}
}

// 快照校验失败（identity 不匹配）时必须 fail closed，不返回任何目录。
func TestReadableRootsRejectsMismatchedSnapshot(t *testing.T) {
	repo, _ := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: default\n    root: "+repo+"\n    repository_identity: repo_test\n")
	if _, err := r.ReadableRoots(rootSnapshot("default", r.generation, "repo_other")); err == nil {
		t.Fatal("identity 不匹配的快照必须被拒绝")
	}
}
