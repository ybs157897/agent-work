package agentconfig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func layoutFixture(t *testing.T) (string, *workspaceLayoutReceipt) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "agents")
	receipt := &workspaceLayoutReceipt{Version: 1, WorkspaceID: "ws_a", State: "pending"}
	for _, slug := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte("name: "+slug+"\nrole: developer\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte("original prompt "+slug), 0o600); err != nil {
			t.Fatal(err)
		}
		digest, err := workspaceBundleDigest(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		receipt.Bundles = append(receipt.Bundles, workspaceLayoutBundle{Slug: slug, Digest: digest})
	}
	return root, receipt
}

func TestWorkspaceLayoutMovesAllBundlesAndPreservesLaterEdits(t *testing.T) {
	root, _ := layoutFixture(t)
	if err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a"}); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(root, slug)); !os.IsNotExist(err) {
			t.Fatalf("legacy %s remained", slug)
		}
		content, err := os.ReadFile(filepath.Join(root, "workspaces", "ws_a", slug, "prompt.md"))
		if err != nil || string(content) != "original prompt "+slug {
			t.Fatalf("content=%s err=%v", content, err)
		}
	}
	r, err := readWorkspaceLayoutReceipt(filepath.Join(root, workspaceLayoutReceiptName))
	if err != nil || r.State != "complete" {
		t.Fatalf("receipt=%+v err=%v", r, err)
	}
	path := filepath.Join(root, "workspaces", "ws_a", "alpha", "prompt.md")
	if err := os.WriteFile(path, []byte("legitimate later edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateWorkspaceLayout(context.Background(), root, "ws_b", []string{"ws_a", "ws_b"}); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "legitimate later edit" {
		t.Fatal("completed receipt overwrote later configuration")
	}
}

func TestWorkspaceLayoutResumesAfterAnyRenameBoundary(t *testing.T) {
	for _, moved := range []int{0, 1, 2} {
		t.Run(string(rune('0'+moved)), func(t *testing.T) {
			root, receipt := layoutFixture(t)
			if err := writeWorkspaceLayoutReceipt(filepath.Join(root, workspaceLayoutReceiptName), receipt); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "workspaces", "ws_a")
			if err := os.MkdirAll(destination, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, bundle := range receipt.Bundles[:moved] {
				if err := os.Rename(filepath.Join(root, bundle.Slug), filepath.Join(destination, bundle.Slug)); err != nil {
					t.Fatal(err)
				}
			}
			if err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a"}); err != nil {
				t.Fatal(err)
			}
			r, err := readWorkspaceLayoutReceipt(filepath.Join(root, workspaceLayoutReceiptName))
			if err != nil || r.State != "complete" {
				t.Fatalf("receipt=%+v err=%v", r, err)
			}
			for _, bundle := range receipt.Bundles {
				actual, err := workspaceBundleDigest(context.Background(), filepath.Join(destination, bundle.Slug))
				if err != nil || actual != bundle.Digest {
					t.Fatalf("lost original bundle %s: %v", bundle.Slug, err)
				}
			}
		})
	}
}

func TestWorkspaceLayoutRejectsChangedPartialMigrationWithoutOverwriting(t *testing.T) {
	root, receipt := layoutFixture(t)
	if err := writeWorkspaceLayoutReceipt(filepath.Join(root, workspaceLayoutReceiptName), receipt); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "workspaces", "ws_a")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "alpha"), filepath.Join(destination, "alpha")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(destination, "alpha", "prompt.md")
	if err := os.WriteFile(path, []byte("unexpected replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a"})
	if !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != "unexpected replacement" {
		t.Fatal("overwrote conflicting configuration")
	}
	if _, err := os.Stat(filepath.Join(root, "beta", "agent.yaml")); err != nil {
		t.Fatal("lost not-yet-migrated bundle")
	}
}

func TestWorkspaceLayoutRefusesAmbiguousOwnershipAndSymlinks(t *testing.T) {
	root, _ := layoutFixture(t)
	if err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a", "ws_b"}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("ambiguous ownership: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("do not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "alpha", "linked")); err != nil {
		t.Fatal(err)
	}
	if err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "alpha", "agent.yaml")); err != nil {
		t.Fatal("invalid input caused partial move")
	}
}

func TestWorkspaceLayoutCancellationRetainsRecoverableReceipt(t *testing.T) {
	root, receipt := layoutFixture(t)
	if err := writeWorkspaceLayoutReceipt(filepath.Join(root, workspaceLayoutReceiptName), receipt); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := migrateWorkspaceLayout(ctx, root, "ws_a", []string{"ws_a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a"}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLayoutRejectsCorruptReceiptAndUnexpectedLegacyAdditions(t *testing.T) {
	root, receipt := layoutFixture(t)
	path := filepath.Join(root, workspaceLayoutReceiptName)
	if err := os.WriteFile(path, []byte(`{"version":1,"workspace_id":"ws_a","state":"complete","bundles":[{"slug":"../escape","digest":"bad"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a"}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("corrupt receipt accepted: %v", err)
	}
	receipt.Bundles = receipt.Bundles[:1]
	if err := writeWorkspaceLayoutReceipt(path, receipt); err != nil {
		t.Fatal(err)
	}
	if err := migrateWorkspaceLayout(context.Background(), root, "ws_a", []string{"ws_a"}); !errors.Is(err, domain.ErrStateConflict) {
		t.Fatalf("unrecorded bundle accepted: %v", err)
	}
}
