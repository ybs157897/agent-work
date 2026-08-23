package agentwork

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDefaultLayout(t *testing.T) {
	wb := t.TempDir()
	r := Resolve(wb)
	want := filepath.Join(wb, RootDirName)
	if r.Root != want {
		t.Fatalf("root=%q want %q", r.Root, want)
	}
	if r.CodexHome() != filepath.Join(want, "codex") {
		t.Fatalf("codex home=%q", r.CodexHome())
	}
	if r.KimiHome() != filepath.Join(want, "kimi") {
		t.Fatalf("kimi home=%q", r.KimiHome())
	}
	if r.DSHHome() != filepath.Join(want, "dsh") {
		t.Fatalf("dsh home=%q", r.DSHHome())
	}
}

func TestEnvOverrides(t *testing.T) {
	wb := t.TempDir()
	t.Setenv("ATW_CODEX_HOME", filepath.Join(wb, "cx"))
	r := Resolve(wb)
	if r.CodexHome() != filepath.Join(wb, "cx") {
		t.Fatalf("codex override=%q", r.CodexHome())
	}
}

func TestWithEnvReplacesDuplicate(t *testing.T) {
	out := WithEnv([]string{"CODEX_HOME=/old", "PATH=/bin"}, "CODEX_HOME", "/new")
	if len(out) != 2 {
		t.Fatalf("len=%d", len(out))
	}
	if out[1] != "CODEX_HOME=/new" {
		t.Fatalf("got %v", out)
	}
}

func TestEnsureCreatesDirs(t *testing.T) {
	wb := t.TempDir()
	r := Resolve(wb)
	if err := r.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{r.Root, r.CodexHome(), r.KimiHome(), r.DSHHome()} {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			t.Fatalf("missing %s: %v", dir, err)
		}
	}
}
