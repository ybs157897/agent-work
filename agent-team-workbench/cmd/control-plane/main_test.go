package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveExecutionRootDefaultsToWorkbench(t *testing.T) {
	workbench := "/tmp/atw-workbench"
	got := resolveExecutionRoot(workbench, "")
	if got != workbench {
		t.Fatalf("got %q want %q", got, workbench)
	}
}

func TestResolveExecutionRootResolvesDot(t *testing.T) {
	workbench := "/tmp/atw-workbench"
	got := resolveExecutionRoot(workbench, ".")
	if got != workbench {
		t.Fatalf("got %q want %q", got, workbench)
	}
}

func TestResolveExecutionRootAbsolutizesRelative(t *testing.T) {
	workbench := "/tmp/atw-workbench"
	got := resolveExecutionRoot(workbench, "sandbox")
	want := filepath.Join(workbench, "sandbox")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDshRepoReadyRequiresEntrypointAndTsx(t *testing.T) {
	repo := t.TempDir()
	entry := filepath.Join(repo, "apps", "cli", "src", "bin.ts")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("// fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dshRepoReady(repo) {
		t.Fatal("missing tsx must not be ready")
	}
	tsx := filepath.Join(repo, "node_modules", "tsx", "package.json")
	if err := os.MkdirAll(filepath.Dir(tsx), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tsx, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !dshRepoReady(repo) {
		t.Fatal("entrypoint and tsx should be ready")
	}
}
