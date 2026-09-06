package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAccessFileReadsCapabilityWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.json")
	if err := os.WriteFile(path, []byte(`{"url":"http://127.0.0.1:8080/api/v1/knowledge-agent/runs/run_1","token":"secret-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoint, token, err := loadAccessFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint == "" || token != "secret-token" {
		t.Fatalf("loaded capability = endpoint %q token %q", endpoint, token)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("access file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadAccessFileRejectsWeakPermissionsAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	weak := filepath.Join(dir, "weak.json")
	if err := os.WriteFile(weak, []byte(`{"url":"http://localhost","token":"secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAccessFile(weak); err == nil {
		t.Fatal("weakly-permissioned access file was accepted")
	}
	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"url":"http://localhost","token":"secret","extra":"leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAccessFile(unknown); err == nil {
		t.Fatal("access file with unknown field was accepted")
	}
	valid := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(valid, []byte(`{"url":"http://localhost","token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAccessFile(link); err == nil {
		t.Fatal("symlinked access file was accepted")
	}
}
