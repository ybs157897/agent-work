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

func TestValidChangePath(t *testing.T) {
	accept := []string{"a/b/c.go", "file.txt", "a.b/c-d_e/f"}
	for _, p := range accept {
		if !validChangePath(p) {
			t.Fatalf("合法相对路径被拒: %q", p)
		}
	}
	reject := []string{
		"", "/abs/path", `C:\windows`, "../up", "a/../../b", `a\..\..\b`,
		"..", "a/b/../../../etc/passwd", "lead/../..", "nul\x00path",
	}
	for _, p := range reject {
		if validChangePath(p) {
			t.Fatalf("非法路径被接受: %q", p)
		}
	}
}
