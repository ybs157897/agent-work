package application_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// Run 文件预览在授权的仓库集合里解析仓库相对路径：Run 自己的 checkout 优先，
// 同一仓库的 worktree 兜底；逃逸路径、.git 内部文件与未授权目录一律拒绝。
func TestReadRunFileResolvesWithinAuthorizedRoots(t *testing.T) {
	svc, _, run, _ := seedRunChangesEnv(t)
	ctx := context.Background()
	checkout := t.TempDir()
	worktree := t.TempDir()
	outside := t.TempDir()

	writeRepoFile(t, checkout, "notes/plan.md", "# 计划\n")
	writeRepoFile(t, worktree, "notes/plan.md", "# worktree 版本\n")
	writeRepoFile(t, worktree, "notes/only-in-worktree.md", "# worktree 独有\n")
	writeRepoFile(t, outside, "secret.md", "不该读到\n")
	writeRepoFile(t, checkout, ".git/config", "[remote]\n")

	svc.SetRunFileRootsResolver(func(_ context.Context, _ *domain.ExecutionContextSnapshot) ([]string, error) {
		return []string{checkout, worktree}, nil
	})

	got, err := svc.ReadRunFile(ctx, run.ID, "notes/plan.md")
	if err != nil {
		t.Fatalf("预览失败: %v", err)
	}
	if got.Content != "# 计划\n" || got.Path != "notes/plan.md" || got.Name != "plan.md" {
		t.Fatalf("应命中 Run 自己的 checkout: %+v", got)
	}
	if got.MIME != "text/markdown" {
		t.Fatalf("markdown MIME 推断错误: %q", got.MIME)
	}
	if got.Size != int64(len("# 计划\n")) {
		t.Fatalf("size 不符: %d", got.Size)
	}

	got, err = svc.ReadRunFile(ctx, run.ID, "notes/only-in-worktree.md")
	if err != nil {
		t.Fatalf("worktree 兜底失败: %v", err)
	}
	if got.Content != "# worktree 独有\n" {
		t.Fatalf("worktree 内容不符: %+v", got)
	}

	if _, err := svc.ReadRunFile(ctx, run.ID, "../secret.md"); !errors.Is(err, application.ErrRunFileInvalidPath) {
		t.Fatalf("逃逸路径必须被拒: %v", err)
	}
	if _, err := svc.ReadRunFile(ctx, run.ID, "/etc/passwd"); !errors.Is(err, application.ErrRunFileInvalidPath) {
		t.Fatalf("绝对路径必须被拒: %v", err)
	}
	if _, err := svc.ReadRunFile(ctx, run.ID, ".git/config"); !errors.Is(err, application.ErrRunFileInvalidPath) {
		t.Fatalf(".git 内部文件必须被拒: %v", err)
	}
	if _, err := svc.ReadRunFile(ctx, run.ID, "secret.md"); !errors.Is(err, application.ErrRunFileNotFound) {
		t.Fatalf("未授权目录的文件不应可见: %v", err)
	}
	if _, err := svc.ReadRunFile(ctx, run.ID, "notes/missing.md"); !errors.Is(err, application.ErrRunFileNotFound) {
		t.Fatalf("缺失文件必须报 not found: %v", err)
	}
}

// 未挂载 Host 受信 registry 时响亮失败，绝不退化成“读任意路径”。
func TestReadRunFileFailsClosedWithoutResolver(t *testing.T) {
	svc, _, run, _ := seedRunChangesEnv(t)
	if _, err := svc.ReadRunFile(context.Background(), run.ID, "notes/plan.md"); !errors.Is(err, application.ErrRunFileUnavailable) {
		t.Fatalf("缺少受信解析钩子时必须 fail closed: %v", err)
	}
}

func writeRepoFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
