package kimiapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureFileBeforePreservesMissingAndRelativePath(t *testing.T) {
	root := t.TempDir()
	args, _ := json.Marshal(map[string]string{"path": "new.md"})
	s, ok := captureFileBefore(root, "write", args)
	if !ok || s.BeforeExists || s.RelPath != "new.md" || s.Root != root {
		t.Fatalf("snapshot=%+v ok=%v", s, ok)
	}
}

func TestCaptureFileBeforeRejectsParentSymlinkAndOversize(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	args, _ := json.Marshal(map[string]string{"path": "link/file"})
	if _, ok := captureFileBefore(root, "edit", args); ok {
		t.Fatal("parent symlink must be rejected")
	}
	big := filepath.Join(root, "big")
	if err := os.WriteFile(big, make([]byte, maxFileSnapshotBytes+1), 0644); err != nil {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]string{"path": "big"})
	if _, ok := captureFileBefore(root, "write", args); ok {
		t.Fatal("oversize snapshot must be skipped")
	}
}
