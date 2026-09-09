package hostregistry

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestResolveProjectBaselineTracksDirtyIndexWorktreeUntrackedDeleteAndSymlink(t *testing.T) {
	repo, _ := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_baseline\n")
	snapshot := rootSnapshot("agent-work", r.generation, "repo_baseline")
	scan := func() domain.ProjectBaseline {
		t.Helper()
		baseline, err := r.ResolveProjectBaseline(context.Background(), snapshot)
		if err != nil {
			t.Fatalf("ResolveProjectBaseline: %v", err)
		}
		if err := baseline.Validate(); err != nil {
			t.Fatalf("baseline validation: %v", err)
		}
		return baseline
	}
	clean := scan()
	if !clean.IsClean() || clean.Head == "" || clean.ContentDigest == "" {
		t.Fatalf("clean baseline = %+v", clean)
	}

	readme := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readme, []byte("working bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	unstaged := scan()
	if unstaged.IsClean() || unstaged.ContentDigest == clean.ContentDigest || unstaged.UnstagedDigest == clean.UnstagedDigest {
		t.Fatalf("unstaged bytes were not frozen: clean=%+v unstaged=%+v", clean, unstaged)
	}

	gitRun(t, repo, "add", "README.md")
	staged := scan()
	if staged.IsClean() || staged.ContentDigest == unstaged.ContentDigest || staged.StagedDigest == unstaged.StagedDigest {
		t.Fatalf("index bytes were not distinguished: staged=%+v unstaged=%+v", staged, unstaged)
	}

	// Change only the index blob while leaving worktree bytes untouched.
	cmd := exec.Command("git", "-C", repo, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader("different staged bytes")
	blob, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "update-index", "--cacheinfo", "100644,"+strings.TrimSpace(string(blob))+",README.md")
	indexOnly := scan()
	if indexOnly.ContentDigest == staged.ContentDigest {
		t.Fatal("index-only content change did not change baseline digest")
	}

	untracked := filepath.Join(repo, "untracked.txt")
	if err := os.WriteFile(untracked, []byte("new bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	withUntracked := scan()
	if withUntracked.ContentDigest == indexOnly.ContentDigest || withUntracked.UntrackedDigest == indexOnly.UntrackedDigest {
		t.Fatal("untracked bytes did not change baseline digest")
	}

	if err := os.Remove(readme); err != nil {
		t.Fatal(err)
	}
	withDelete := scan()
	if withDelete.ContentDigest == withUntracked.ContentDigest {
		t.Fatal("working-tree deletion did not change baseline digest")
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("must not be read"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, "external-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	withSymlink := scan()
	if withSymlink.ContentDigest == withDelete.ContentDigest || strings.Contains(withSymlink.ContentDigest, "must not be read") {
		t.Fatalf("symlink baseline handling is unsafe or ignored: %+v", withSymlink)
	}
}

func TestResolveProjectBaselineTracksHeadAndCheckoutIdentity(t *testing.T) {
	repo, wt := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_baseline\n")
	root := rootSnapshot("agent-work", r.generation, "repo_baseline")
	rootBaseline, err := r.ResolveProjectBaseline(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if rootBaseline.RefKind != domain.RefRoot || rootBaseline.CheckoutRef != rootCheckoutRef || rootBaseline.BranchName != "main" {
		t.Fatalf("root baseline identity = %+v", rootBaseline)
	}

	beforeHead := rootBaseline.Head
	if err := os.WriteFile(filepath.Join(repo, "committed.txt"), []byte("committed"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "committed.txt")
	gitRun(t, repo, "commit", "-q", "-m", "advance")
	afterHead, err := r.ResolveProjectBaseline(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if beforeHead == afterHead.Head || !afterHead.IsClean() {
		t.Fatalf("HEAD change not captured: before=%+v after=%+v", rootBaseline, afterHead)
	}
	gitRun(t, repo, "mv", "committed.txt", "renamed.txt")
	renamed, err := r.ResolveProjectBaseline(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.IsClean() || renamed.ContentDigest == afterHead.ContentDigest {
		t.Fatalf("staged rename did not change baseline digest: before=%+v after=%+v", afterHead, renamed)
	}

	adv := r.Advertise()[0]
	var wtRef string
	for _, checkout := range adv.Checkouts {
		if checkout.Kind == "worktree" && checkout.Branch == "feature/x" {
			wtRef = checkout.Ref
		}
	}
	if wtRef == "" || wt == "" {
		t.Fatal("worktree fixture missing")
	}
	worktree := rootSnapshot("agent-work", r.generation, "repo_baseline")
	worktree.RefKind = domain.RefWorktree
	worktree.WorktreeRef = wtRef
	worktree.SnapshotDigest = worktree.ComputeDigest()
	worktreeBaseline, err := r.ResolveProjectBaseline(context.Background(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if worktreeBaseline.RefKind != domain.RefWorktree || worktreeBaseline.CheckoutRef != wtRef || worktreeBaseline.BranchName != "feature/x" {
		t.Fatalf("worktree baseline identity = %+v", worktreeBaseline)
	}
}

func TestResolveProjectBaselineRejectsBoundsAndNonGitRoots(t *testing.T) {
	repo, _ := newTestRepo(t)
	r, _ := writeRegistry(t, "  - alias: agent-work\n    root: "+repo+"\n    repository_identity: repo_baseline\n")
	snapshot := rootSnapshot("agent-work", r.generation, "repo_baseline")
	if err := os.WriteFile(filepath.Join(repo, "large.txt"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanProjectBaseline(context.Background(), realPath(t, repo), snapshot, ProjectScanLimits{MaxFileBytes: 4}); err == nil {
		t.Fatal("file bound did not reject baseline")
	} else {
		var resolveErr *ResolveError
		if !errors.As(err, &resolveErr) || resolveErr.Code != CodeProjectBaselineScanTooLarge {
			t.Fatalf("file bound error = %v", err)
		}
	}

	plain := t.TempDir()
	plainRegistry, _ := writeRegistry(t, "  - alias: plain\n    root: "+plain+"\n    repository_identity: repo_plain\n")
	plainSnapshot := rootSnapshot("plain", plainRegistry.generation, "repo_plain")
	if _, err := plainRegistry.ResolveProjectBaseline(context.Background(), plainSnapshot); err == nil {
		t.Fatal("non-git root accepted as project baseline")
	} else {
		var resolveErr *ResolveError
		if !errors.As(err, &resolveErr) || resolveErr.Code != CodeProjectBaselineNotGit {
			t.Fatalf("non-git error = %v", err)
		}
	}
}
