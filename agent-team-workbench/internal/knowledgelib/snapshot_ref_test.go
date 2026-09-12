package knowledgelib_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/knowledgelib"
)

// refFixture builds a repository with two branches whose tips differ, so a
// test can tell whether the pinned commit came from the registered ref or from
// whatever happens to be checked out.
func refFixture(t *testing.T) (repo string, mainSHA, featureSHA string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	repo = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "main")
	mainSHA = run("rev-parse", "HEAD")
	run("checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "b.txt")
	run("commit", "-q", "-m", "feature")
	featureSHA = run("rev-parse", "HEAD")
	run("checkout", "-q", "main")
	return repo, mainSHA, featureSHA
}

// TestCaptureBindingUsesRegisteredRef pins the rule that a registered ref
// decides the input revision: an uncommitted edit in a checkout of another
// branch must not be published as part of the registered ref.
func TestCaptureBindingUsesRegisteredRef(t *testing.T) {
	repo, mainSHA, featureSHA := refFixture(t)
	// Uncommitted work in the main checkout: it belongs to main, not to the
	// registered feature ref.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binding, err := knowledgelib.CaptureBinding(context.Background(), knowledgelib.ExecGitRunner{}, "ksnap_ref", knowledgelib.SourceSpec{
		ID: "src_1", Name: "svc", Kind: "service", RepoPath: repo, DefaultRef: "feature",
		Usage: knowledgelib.Usage{Consumer: "svc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.CommitSHA != featureSHA {
		t.Fatalf("registered ref must decide the pinned commit: got %s want %s", binding.CommitSHA, featureSHA)
	}
	if binding.GitRef != "feature" || !binding.RefPinned {
		t.Fatalf("binding must report the ref it actually read: %+v", binding)
	}
	if binding.Dirty || len(binding.Untracked) != 0 {
		t.Fatalf("a worktree of another revision must not be overlaid: %+v", binding)
	}
	if binding.WorktreeNote == "" {
		t.Fatal("skipping the worktree must be explained, not silent")
	}

	// With no registered ref the checkout itself is the input and the dirty
	// file is part of it.
	binding, err = knowledgelib.CaptureBinding(context.Background(), knowledgelib.ExecGitRunner{}, "ksnap_head", knowledgelib.SourceSpec{
		ID: "src_1", Name: "svc", Kind: "service", RepoPath: repo,
		Usage: knowledgelib.Usage{Consumer: "svc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.CommitSHA != mainSHA {
		t.Fatalf("no registered ref must pin the checked-out commit: %s", binding.CommitSHA)
	}
	if !binding.Dirty || len(binding.Untracked) != 1 {
		t.Fatalf("the checked-out worktree dirty file is part of this input: %+v", binding)
	}
}

func TestCaptureBindingRejectsUnresolvableRef(t *testing.T) {
	repo, _, _ := refFixture(t)
	_, err := knowledgelib.CaptureBinding(context.Background(), knowledgelib.ExecGitRunner{}, "ksnap_missing", knowledgelib.SourceSpec{
		ID: "src_1", Name: "svc", Kind: "service", RepoPath: repo, DefaultRef: "release/9.9",
		Usage: knowledgelib.Usage{Consumer: "svc"},
	})
	if err == nil {
		t.Fatal("a ref that cannot be resolved must fail the capture")
	}
	if !strings.Contains(err.Error(), "release/9.9") {
		t.Fatalf("the error must name the ref: %v", err)
	}
}
