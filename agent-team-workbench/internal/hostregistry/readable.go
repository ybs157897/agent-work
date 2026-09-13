package hostregistry

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// ReadableRoots 返回一个 Run 可以只读预览的宿主目录集合：Run 自己的 checkout 在前
// （root ref 即授权根；branch/worktree ref 即那个 worktree），其后是同一仓库的其余
// 主工作树与链接 worktree。集合只由本机受信 registry 与 git 事实派生，调用方提供的
// 路径永远不参与——这是站内文件预览的唯一信任来源。
//
// 语义边界（RFC §4.3）：返回值是 Host 本地信任状态，绝不进入任何 DTO 或响应体。
// 快照校验全部走 Resolve（alias/generation/identity/digest/realpath 授权集合），
// 因此调用方拿到的一定是已授权集合的子集。
//
// worktree 目录缺失（已删除但未 prune）时跳过该条，不让一个陈旧条目关掉整条预览通道；
// 主工作树不可读时 git 失败，只返回 Run 自己的 checkout。
func (r *Registry) ReadableRoots(snapshot *domain.ExecutionContextSnapshot) ([]string, error) {
	resolved, err := r.Resolve(snapshot)
	if err != nil {
		return nil, err
	}
	roots := []string{resolved.CWD}
	appendRoot := func(path string) {
		if path == "" {
			return
		}
		for _, existing := range roots {
			if existing == path {
				return
			}
		}
		roots = append(roots, path)
	}
	appendRoot(resolved.AuthorizedRoot)
	out, err := exec.Command("git", "-C", resolved.AuthorizedRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return roots, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		// realpath 收敛：worktree 路径可能经 symlink 注册，且可能已被删除。
		real, realErr := filepath.EvalSymlinks(strings.TrimSpace(strings.TrimPrefix(line, "worktree ")))
		if realErr != nil {
			continue
		}
		appendRoot(real)
	}
	return roots, nil
}
