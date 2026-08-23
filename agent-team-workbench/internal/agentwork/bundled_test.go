package agentwork

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundledRuntimeFindsPlatformLayout(t *testing.T) {
	wb := t.TempDir()
	platform := runtimePlatformDir()
	binName := "codex"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	dir := filepath.Join(wb, "runtimes", "codex", platform)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, binName)
	if err := os.WriteFile(path, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := BundledRuntime(wb, "codex")
	if got != path {
		t.Fatalf("got %q want %q", got, path)
	}
}

func TestBundledRuntimeLegacyFlatLayout(t *testing.T) {
	wb := t.TempDir()
	binName := "kimi"
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	dir := filepath.Join(wb, "runtimes", "kimi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, binName)
	if err := os.WriteFile(path, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := BundledRuntime(wb, "kimi")
	if got != path {
		t.Fatalf("got %q want %q", got, path)
	}
}

func TestResolveBundledBinPrefersEnv(t *testing.T) {
	t.Setenv("ATW_CODEX_BIN", "/custom/codex")
	got := ResolveBundledBin(t.TempDir(), "ATW_CODEX_BIN", "codex", "codex")
	if got != "/custom/codex" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveBundledBinFallsBackToPathName(t *testing.T) {
	got := ResolveBundledBin(t.TempDir(), "ATW_CODEX_BIN", "codex", "codex")
	if got != "codex" {
		t.Fatalf("got %q want codex", got)
	}
}
