package fsjail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchFindsSubstring(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "A.java"), []byte("class A {\n  int springBoot;\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("no match here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := j.Search("springboot", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("want 1 hit, got %#v", res.Hits)
	}
	if res.Hits[0].Path != "src/A.java" || res.Hits[0].Line != 2 {
		t.Fatalf("unexpected hit: %#v", res.Hits[0])
	}
}

func TestSearchSkipsSymlinkFilesOutsideJail(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.java"), []byte("class Secret {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-secret.java")
	if err := os.Symlink(filepath.Join(outside, "secret.java"), link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := j.Search("secret", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("symlink target was searched: %#v", res.Hits)
	}
}

func TestSearchFilesMatchesAndRanksPaths(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "MainWindow.java"), "ignored content")
	mustWrite(t, filepath.Join(root, "src", "menu", "WindowManager.java"), "ignored content")
	mustWrite(t, filepath.Join(root, "src", "Other.java"), "ignored content")
	for _, dir := range []string{".git", "node_modules", "build", "target", "dist"} {
		mustWrite(t, filepath.Join(root, dir, "MainWindow.java"), "ignored content")
	}
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := j.SearchFiles(context.Background(), "mainwin", 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paths) != 1 || res.Paths[0] != "src/MainWindow.java" {
		t.Fatalf("substring result=%#v", res.Paths)
	}
	res, err = j.SearchFiles(context.Background(), "wmn", 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paths) != 1 || res.Paths[0] != "src/menu/WindowManager.java" {
		t.Fatalf("subsequence results=%#v", res.Paths)
	}
}

func TestSearchFilesTruncatesResultsAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 4; i++ {
		mustWrite(t, filepath.Join(root, pathJoin(i)), "ignored content")
	}
	j, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := j.SearchFiles(context.Background(), "f", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Paths) != 2 {
		t.Fatalf("truncated=%v paths=%#v", res.Truncated, res.Paths)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := j.SearchFiles(ctx, "f", 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context cancellation, got %v", err)
	}
}
