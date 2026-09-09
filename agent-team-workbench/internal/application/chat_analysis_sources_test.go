package application_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/application"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestResolveAnalysisCodeDigestUsesRootBoundaryAndSizeLimit(t *testing.T) {
	root := t.TempDir()
	content := []byte("class Example {}\n")
	if err := os.WriteFile(filepath.Join(root, "Example.java"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	got, err := application.ResolveAnalysisCodeDigest(root, "Example.java")
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("code digest=%q err=%v", got, err)
	}
	outside := filepath.Join(t.TempDir(), "secret.java")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.java")); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ResolveAnalysisCodeDigest(root, "escape.java"); err == nil || !errors.Is(err, domain.ErrWorkspacePathForbidden) {
		t.Fatalf("symlink escape must be rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), make([]byte, 10<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ResolveAnalysisCodeDigest(root, "large.bin"); err == nil {
		t.Fatal("oversized code source must be rejected")
	}
}
