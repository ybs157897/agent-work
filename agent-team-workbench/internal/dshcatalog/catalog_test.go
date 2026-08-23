package dshcatalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListAgentPresets(t *testing.T) {
	root := t.TempDir()
	presetDir := filepath.Join(root, "demo")
	if err := os.MkdirAll(presetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(presetDir, compositionFile), []byte("- id: x\n  name: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(presetDir, metadataFile), []byte("name: 演示模式\ndescription: 测试\norder: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := ListAgentPresets([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "demo" || items[0].Name != "演示模式" {
		t.Fatalf("unexpected: %+v", items)
	}
}

func TestSandboxModeForPermission(t *testing.T) {
	if SandboxModeForPermission("read-only") != "read-only" {
		t.Fatal("read-only")
	}
	if SandboxModeForPermission("danger-full-access") != "danger-full-access" {
		t.Fatal("full")
	}
}

func TestForSDKAdapterDisablesHostPlanePresets(t *testing.T) {
	items := ForSDKAdapter([]AgentPresetEntry{
		{ID: "standard", Description: "host full"},
		{ID: "test-mode", Description: "host test"},
		{ID: "code"},
	})
	if items[0].Broken != "" || items[1].Broken != "" {
		t.Fatalf("SDK 支持的翻译模式不应禁用: %+v", items)
	}
	if items[2].Broken == "" {
		t.Fatal("Host-plane preset 必须在 UI 选择前禁用")
	}
	if items[0].Description == "host full" || items[1].Description == "host test" {
		t.Fatal("SDK 模式描述不得继续宣称 Host 完整能力")
	}
}

func TestResolvePresetRootsIncludesDshHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", "")
	userRoot := filepath.Join(home, ".dsh", ".agent-presets")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	roots := ResolvePresetRoots(t.TempDir())
	found := false
	for _, r := range roots {
		if r == userRoot {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %s in roots: %v", userRoot, roots)
	}
}

func TestResolvePresetRootsHonorsDshHomeEnv(t *testing.T) {
	custom := t.TempDir()
	t.Setenv("DSH_HOME", custom)
	userRoot := filepath.Join(custom, ".agent-presets")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	roots := ResolvePresetRoots(t.TempDir())
	found := false
	for _, r := range roots {
		if r == userRoot {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %s in roots: %v", userRoot, roots)
	}
}
