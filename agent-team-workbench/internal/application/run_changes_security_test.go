package application

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureJoinRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	if _, err := secureJoin(root, "../outside"); err == nil {
		t.Fatal("traversal accepted")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := secureJoin(root, "link/file"); err == nil {
		t.Fatal("symlink component accepted")
	}
}
