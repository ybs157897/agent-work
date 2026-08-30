package agentconfig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	// 无配置目录 → 空结果而非错误
	if cfgs, err := LoadDir(filepath.Join(dir, "missing")); err != nil || len(cfgs) != 0 {
		t.Fatalf("missing dir: got %v, %v", cfgs, err)
	}

	one := filepath.Join(dir, "forge")
	if err := os.MkdirAll(one, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(one, "agent.yaml"), []byte(`name: Forge
role: developer
skills: [Go, React]
runtime: {preferred: dsh_local, fallbacks: [mock]}
model: {provider: deepseek, model: deepseek-v4-flash, reasoning_effort: ultra}
permissions: {tools: [bash, fs], approval_policy: approve_high_risk, sandbox: workspace-write}
`), 0o644)
	os.WriteFile(filepath.Join(one, "prompt.md"), []byte("# 角色：开发\n"), 0o644)

	cfgs, err := LoadDir(dir)
	if err != nil || len(cfgs) != 1 {
		t.Fatalf("LoadDir: %v, %v", cfgs, err)
	}
	c := cfgs[0]
	if c.Slug != "forge" || c.Name != "Forge" || c.Role != "developer" {
		t.Fatalf("bad config: %+v", c)
	}
	if c.Runtime.Preferred != "dsh_local" || len(c.Runtime.Fallbacks) != 1 {
		t.Fatalf("bad runtime pref: %+v", c.Runtime)
	}
	if c.Permissions.ApprovalPolicy != "approve_high_risk" || len(c.Permissions.Tools) != 2 {
		t.Fatalf("bad permissions: %+v", c.Permissions)
	}
	if c.Model.ReasoningEffort != "ultra" {
		t.Fatalf("bad reasoning effort: %+v", c.Model)
	}
	if c.Prompt != "# 角色：开发\n" {
		t.Fatalf("bad prompt: %q", c.Prompt)
	}
}

func TestLoadDirInvalid(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "agent.yaml"), []byte("name: ''\n"), 0o644)
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected validation error for missing name/role")
	}

	bad2 := filepath.Join(dir, "bad2")
	os.MkdirAll(bad2, 0o755)
	os.WriteFile(filepath.Join(bad2, "agent.yaml"), []byte("name: X\nrole: r\npermissions: {approval_policy: maybe}\n"), 0o644)
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("expected validation error for approval_policy")
	}
}

func TestWriteBackRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := &domain.AgentProfile{
		Name: "Forge", Role: "developer", Skills: []string{"Go"},
		Instructions:      "提示词内容",
		Policy:            domain.AgentPolicy{Tools: []string{"bash"}, ApprovalPolicy: "manual", Sandbox: "workspace-write", PermissionPreset: "workspace-write"},
		RuntimePreference: domain.RuntimePreference{Mode: "plan"},
	}
	slug, err := WriteBack(dir, a, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if slug != "forge" {
		t.Fatalf("slug = %q", slug)
	}

	cfgs, err := LoadDir(dir)
	if err != nil || len(cfgs) != 1 {
		t.Fatalf("LoadDir after write: %v, %v", cfgs, err)
	}
	if cfgs[0].Prompt != "提示词内容" || cfgs[0].Permissions.ApprovalPolicy != "manual" ||
		cfgs[0].Permissions.Preset != "workspace-write" || cfgs[0].Runtime.Mode != "plan" {
		t.Fatalf("round trip mismatch: %+v", cfgs[0])
	}

	// slug 冲突时加后缀
	slug2, err := WriteBack(dir, &domain.AgentProfile{Name: "Forge"}, map[string]bool{"forge": true})
	if err != nil {
		t.Fatal(err)
	}
	if slug2 != "forge-2" {
		t.Fatalf("dedup slug = %q", slug2)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Forge":       "forge",
		"Code Review": "code-review",
		"代码":          "agent",
		"  A B  ":     "a-b",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
