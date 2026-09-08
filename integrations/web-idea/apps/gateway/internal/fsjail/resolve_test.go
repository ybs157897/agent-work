package fsjail

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRelative(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"", true},
		{"src/Main.java", true},
		{"a/b/c", true},
		{"../x", false},
		{"a/../b", false},
		{"/abs", false},
		{"~/.ssh", false},
		{`a\b`, false},
		{"C:/Windows", false},
		{"a//b", false},
		{".", false},
		{"a/./b", false},
		{"a/\x00b", false},
	}
	for _, c := range cases {
		err := validateRelative(c.in)
		if c.ok && err != nil {
			t.Errorf("validateRelative(%q): unexpected error %v", c.in, err)
		}
		if !c.ok && err == nil {
			t.Errorf("validateRelative(%q): expected error", c.in)
		}
	}
}

func TestResolveInside(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "A.java"), "class A {}")

	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := j.Resolve("src/A.java")
	if err != nil {
		t.Fatal(err)
	}
	if abs != filepath.Join(j.Root, "src", "A.java") {
		t.Fatalf("got %q", abs)
	}
	_, err = j.Resolve("missing.java")
	if err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestResolveTraversalForbidden(t *testing.T) {
	root := t.TempDir()
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.Resolve("../outside")
	if err != ErrInvalidPath {
		t.Fatalf("want ErrInvalidPath, got %v", err)
	}
}

func TestResolveSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	mustWrite(t, secret, "nope")

	link := filepath.Join(root, "leak")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.Resolve("leak/secret.txt")
	if err != ErrForbidden {
		t.Fatalf("want ErrForbidden for symlink escape, got %v", err)
	}
}

func TestResolveSymlinkInside(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "real", "A.java"), "ok")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	abs, err := j.Resolve("link/A.java")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(abs)
	if err != nil || string(data) != "ok" {
		t.Fatalf("read via symlink: %v %q", err, data)
	}
}

func TestTreeAndRead(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "Main.java"), "class Main {}")
	mustWrite(t, filepath.Join(root, "README.md"), "# hi")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := j.Tree("", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Entries) != 3 {
		t.Fatalf("entries=%d %+v", len(tr.Entries), tr.Entries)
	}

	data, err := j.ReadFile("src/Main.java")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "class Main {}" {
		t.Fatalf("body=%q", data)
	}

	_, err = j.ReadFile("src")
	if err != ErrNotFile {
		t.Fatalf("want ErrNotFile, got %v", err)
	}

	st, err := j.StatPath("README.md")
	if err != nil || st.Type != "file" || st.Size == nil {
		t.Fatalf("stat: %+v %v", st, err)
	}
}

func TestReadTooLargeAndBinary(t *testing.T) {
	root := t.TempDir()
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	j.MaxFileBytes = 8
	mustWrite(t, filepath.Join(root, "big.txt"), "0123456789")
	_, err = j.ReadFile("big.txt")
	if err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}

	mustWrite(t, filepath.Join(root, "bin.dat"), "a\x00b")
	j.MaxFileBytes = DefaultMaxFileBytes
	_, err = j.ReadFile("bin.dat")
	if err != ErrNotText {
		t.Fatalf("want ErrNotText, got %v", err)
	}
}

func TestWriteFileCreateAndOverwrite(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "A.java"), "class A {}\n")
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.WriteFile("src/A.java", []byte("class A { int x; }\n")); err != nil {
		t.Fatal(err)
	}
	got, err := j.ReadFile("src/A.java")
	if err != nil || string(got) != "class A { int x; }\n" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if err := j.WriteFile("src/B.java", []byte("class B {}\n")); err != nil {
		t.Fatal(err)
	}
	got, err = j.ReadFile("src/B.java")
	if err != nil || string(got) != "class B {}\n" {
		t.Fatalf("new file: %q err=%v", got, err)
	}
	if err := j.WriteFile("missing/C.java", []byte("x")); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for missing parent, got %v", err)
	}
	if err := j.WriteFile("src", []byte("x")); err != ErrNotFile {
		t.Fatalf("want ErrNotFile for dir, got %v", err)
	}
	if err := j.WriteFile("../x.java", []byte("x")); err != ErrInvalidPath {
		t.Fatalf("want ErrInvalidPath, got %v", err)
	}
	if err := j.WriteFile("src/bad.dat", []byte("a\x00b")); err != ErrNotText {
		t.Fatalf("want ErrNotText, got %v", err)
	}
}

func TestTreeTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		mustWrite(t, filepath.Join(root, filepath.FromSlash(pathJoin(i))), "x")
	}
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	j.MaxEntries = 3
	tr, err := j.Tree("", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Truncated || len(tr.Entries) != 3 {
		t.Fatalf("truncated=%v len=%d", tr.Truncated, len(tr.Entries))
	}
}

func pathJoin(i int) string {
	return fmt.Sprintf("f%d.txt", i)
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
