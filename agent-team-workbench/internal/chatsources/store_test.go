package chatsources

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func location(blob Blob) Location {
	return Location{WorkspaceID: "ws_a", ChatID: "wi_a", SourceID: "src_a", Key: blob.Key, SHA256: blob.SHA256}
}

func TestOriginalBytesArePreservedAndPathIsServerOwned(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sources")
	store := NewStore(root)
	data := []byte("<original>\x00\xffWord/PPT bytes remain unmodified</original>")
	blob, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "../../用户原件.docx", data)
	if err != nil {
		t.Fatal(err)
	}
	if blob.Key != "ws_a/wi_a/src_a/content.docx" || blob.Size != int64(len(data)) {
		t.Fatalf("unexpected blob %+v", blob)
	}
	path, err := store.Resolve(context.Background(), location(blob))
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(canonicalRoot, filepath.FromSlash(blob.Key)) {
		t.Fatalf("untrusted path: %s", path)
	}
	file, err := store.Open(context.Background(), location(blob))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("bytes changed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("original mode=%v", info.Mode())
	}
}

func TestStoredSourceScopeCannotBeSubstituted(t *testing.T) {
	store := NewStore(t.TempDir())
	blob, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", []byte("secret A"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"workspace", "chat", "source", "key"} {
		t.Run(kind, func(t *testing.T) {
			loc := location(blob)
			switch kind {
			case "workspace":
				loc.WorkspaceID = "ws_b"
			case "chat":
				loc.ChatID = "wi_b"
			case "source":
				loc.SourceID = "src_b"
			case "key":
				loc.Key = "../../escape.md"
			}
			if _, err := store.Resolve(context.Background(), loc); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("scope substitution accepted: %v", err)
			}
			if err := store.Remove(context.Background(), loc); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("foreign cleanup accepted: %v", err)
			}
		})
	}
	if _, err := store.Resolve(context.Background(), location(blob)); err != nil {
		t.Fatal("rejected cleanup removed original")
	}
}

func TestPutIsIdempotentAndNeverOverwritesOriginal(t *testing.T) {
	store := NewStore(t.TempDir())
	original := []byte("original bytes")
	first, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", original)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", original)
	if err != nil || first != second {
		t.Fatalf("retry=%+v err=%v", second, err)
	}
	if _, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", []byte("replacement")); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrite accepted: %v", err)
	}
	if _, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "renamed.docx", original); !errors.Is(err, ErrConflict) {
		t.Fatalf("same identity changed file kind: %v", err)
	}
	file, err := store.Open(context.Background(), location(first))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	actual, _ := io.ReadAll(file)
	if !bytes.Equal(actual, original) {
		t.Fatal("original overwritten")
	}
}

func TestConcurrentStoresCannotReplaceTheWinningFile(t *testing.T) {
	root := t.TempDir()
	data := [][]byte{[]byte("A data"), []byte("B data")}
	type outcome struct {
		blob Blob
		err  error
	}
	results := make([]outcome, 2)
	var wait sync.WaitGroup
	for i := range data {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			results[i].blob, results[i].err = NewStore(root).Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", data[i])
		}(i)
	}
	wait.Wait()
	success := 0
	for i, result := range results {
		if result.err != nil {
			if !errors.Is(result.err, ErrConflict) {
				t.Fatalf("unexpected error %v", result.err)
			}
			continue
		}
		success++
		file, err := NewStore(root).Open(context.Background(), location(result.blob))
		if err != nil {
			t.Fatal(err)
		}
		actual, _ := io.ReadAll(file)
		_ = file.Close()
		if !bytes.Equal(actual, data[i]) {
			t.Fatal("winning bytes replaced")
		}
	}
	if success != 1 {
		t.Fatalf("successful conflicting uploads=%d", success)
	}
}

func TestChangedMissingAndLinkedOriginalsAreRejected(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	blob, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(blob.Key))
	if err := os.WriteFile(path, []byte("corrupt!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), location(blob)); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed original accepted: %v", err)
	}
	if err := store.Remove(context.Background(), location(blob)); !errors.Is(err, ErrChanged) {
		t.Fatalf("cleanup deleted mismatched bytes: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), location(blob)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing original accepted: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(context.Background(), location(blob)); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("linked original accepted: %v", err)
	}
}

func TestPutRejectsDirectorySymlinkAndInvalidScope(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "ws_a")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", []byte("data")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("escaped source write: %v", err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside root")
	}
	if _, err := store.Put(context.Background(), "../ws_a", "wi_a", "src_a", "x.md", []byte("data")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("path scope accepted: %v", err)
	}
}

func TestLimitsCancellationAndPreciseCleanup(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", nil); !errors.Is(err, ErrEmpty) {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", make([]byte, MaxFileBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(ctx, "ws_a", "wi_a", "src_a", "x.md", []byte("data")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	a, err := store.Put(context.Background(), "ws_a", "wi_a", "src_a", "x.md", []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Put(context.Background(), "ws_a", "wi_a", "src_b", "x.md", []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), location(a)); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), location(a)); err != nil {
		t.Fatal("missing cleanup not idempotent")
	}
	if _, err := store.Resolve(context.Background(), Location{WorkspaceID: "ws_a", ChatID: "wi_a", SourceID: "src_b", Key: b.Key, SHA256: b.SHA256}); err != nil {
		t.Fatal("cleanup removed another upload")
	}
	missingRoot := NewStore(filepath.Join(t.TempDir(), "not-created"))
	if err := missingRoot.Remove(context.Background(), location(a)); err != nil {
		t.Fatalf("already absent store cleanup: %v", err)
	}
}

func TestRealOfficeOriginalsRoundTripWithoutExtraction(t *testing.T) {
	store := NewStore(t.TempDir())
	fixture := filepath.Join("..", "..", "..", "testdata", "requirements", "web-idea-java", "inputs")
	for i, name := range []string{"reading-scope.docx", "review-proposal.pptx"} {
		data, err := os.ReadFile(filepath.Join(fixture, name))
		if err != nil {
			t.Fatal(err)
		}
		id := []string{"src_word", "src_ppt"}[i]
		blob, err := store.Put(context.Background(), "ws_a", "wi_a", id, name, data)
		if err != nil {
			t.Fatal(err)
		}
		file, err := store.Open(context.Background(), Location{WorkspaceID: "ws_a", ChatID: "wi_a", SourceID: id, Key: blob.Key, SHA256: blob.SHA256})
		if err != nil {
			t.Fatal(err)
		}
		actual, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil || !bytes.Equal(actual, data) {
			t.Fatalf("%s original changed", name)
		}
	}
}
