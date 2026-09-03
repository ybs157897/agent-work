package agentwork

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestKimiAppHomeSharesCanonicalKimiHome(t *testing.T) {
	wb := t.TempDir()
	appHome := filepath.Join(wb, "kap-home")
	t.Setenv("ATW_KIMIAPP_HOME", appHome)
	r := Resolve(wb)
	if r.KimiHome() != appHome {
		t.Fatalf("kimi app home=%q, want canonical home %q", r.KimiHome(), appHome)
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

func TestWriteAtomicDurablePublishesCompleteFileAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := WriteAtomicDurable(path, []byte("model = \"test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "model = \"test\"\n" {
		t.Fatalf("atomic contents = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("atomic mode = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatalf("temporary file leaked after atomic publish: %+v", entries)
	}
}

func TestWriteAtomicDurableCleansStaleOwnerOnlyTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".agent-work-stale")
	if err := os.WriteFile(stale, []byte("partial prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomicDurable(filepath.Join(dir, "prompt.md"), []byte("complete"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale atomic temp should be cleaned, stat err=%v", err)
	}
}
