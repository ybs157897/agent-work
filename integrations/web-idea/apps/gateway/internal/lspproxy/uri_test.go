package lspproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestURIRoundTrip(t *testing.T) {
	id := "abc"
	root := "/Users/me/proj"
	abs := "/Users/me/proj/src/Main.java"
	cu, err := ToClientURI(id, root, abs)
	if err != nil {
		t.Fatal(err)
	}
	want := "webidea://ws/abc/src/Main.java"
	if cu != want {
		t.Fatalf("client uri: got %q want %q", cu, want)
	}
	fu, err := ToFileURI(id, root, cu)
	if err != nil {
		t.Fatal(err)
	}
	if fu != "file:///Users/me/proj/src/Main.java" {
		t.Fatalf("file uri: %q", fu)
	}
}

func TestRootURIToFile(t *testing.T) {
	fu, err := ToFileURI("abc", "/tmp/root", "webidea://ws/abc")
	if err != nil {
		t.Fatal(err)
	}
	if fu != "file:///tmp/root" {
		t.Fatalf("got %q", fu)
	}
	fu, err = ToFileURI("abc", "/tmp/root", "webidea://ws/abc/")
	if err != nil || fu != "file:///tmp/root" {
		t.Fatalf("trailing slash root: %q %v", fu, err)
	}
}

func TestToFileURIRejectsTraversalAndExternalFileURI(t *testing.T) {
	root := t.TempDir()
	if _, err := ToFileURI("abc", root, "webidea://ws/abc/../secret.txt"); err == nil {
		t.Fatal("expected traversal URI to be rejected")
	}
	if _, err := ToFileURI("abc", root, "webidea://ws/abc/%2e%2e/secret.txt"); err == nil {
		t.Fatal("expected encoded traversal URI to be rejected")
	}
	if _, err := ToFileURI("abc", root, "file:///etc/passwd"); err == nil {
		t.Fatal("expected external file URI to be rejected")
	}
}

func TestToFileURIRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.java"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.java")
	if err := os.Symlink(filepath.Join(outside, "secret.java"), link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ToFileURI("abc", root, "webidea://ws/abc/link.java"); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestRewriteJSONToServer(t *testing.T) {
	in := []byte(`{"textDocument":{"uri":"webidea://ws/abc/A.java"},"position":{"line":1,"character":2}}`)
	out, err := RewriteJSON(in, "abc", "/tmp/root", "toServer")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	td := v["textDocument"].(map[string]any)
	uri := td["uri"].(string)
	if uri != "file:///tmp/root/A.java" {
		t.Fatalf("got %q", uri)
	}
}

func TestRewriteJSONToServerRejectsHostFileURI(t *testing.T) {
	in := []byte(`{"textDocument":{"uri":"file:///etc/passwd"}}`)
	if _, err := RewriteJSON(in, "abc", "/tmp/root", "toServer"); err == nil {
		t.Fatal("expected host file URI to be rejected")
	}
}

func TestRewriteJSONToClientSanitizesExternalFileURI(t *testing.T) {
	in := []byte(`{"location":{"uri":"file:///etc/passwd"}}`)
	out, err := RewriteJSON(in, "abc", t.TempDir(), "toClient")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	loc := v["location"].(map[string]any)
	if got := loc["uri"].(string); got != "" {
		t.Fatalf("external URI leaked to client: %q", got)
	}
}
