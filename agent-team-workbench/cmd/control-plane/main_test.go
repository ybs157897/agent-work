package main

import (
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
