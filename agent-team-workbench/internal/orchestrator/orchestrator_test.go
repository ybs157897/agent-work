package orchestrator

import (
	"testing"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

func TestResolveRuntimeCandidates(t *testing.T) {
	agent := &domain.AgentProfile{
		RuntimePreference: domain.RuntimePreference{Preferred: "dsh_local", Fallbacks: []string{"mock"}},
	}
	explicit := &domain.RuntimePreference{Preferred: "scripted"}

	got := ResolveRuntimeCandidates(explicit, agent)
	want := []string{"scripted", "dsh_local", "mock"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}

	// 无显式无 agent：只剩兜底
	got = ResolveRuntimeCandidates(nil, nil)
	if len(got) != 1 || got[0] != DefaultRuntimeLabel {
		t.Fatalf("default candidates = %v", got)
	}
}

func TestBuildInput(t *testing.T) {
	agent := &domain.AgentProfile{
		Instructions:  "你是开发 Agent",
		Policy:        domain.AgentPolicy{Tools: []string{"bash", "fs"}, ApprovalPolicy: "approve_high_risk", Sandbox: "workspace_only"},
		ModelOverride: domain.ModelRef{Provider: "deepseek", Model: "deepseek-v4-flash"},
	}
	in := BuildInput("实现 X", []string{"测试通过"}, map[string]string{"streaming": "required"},
		nil, agent, "dsh_local", "requested")

	if in["instruction"] != "实现 X" {
		t.Fatalf("instruction 必须保持原文: %v", in["instruction"])
	}
	if in["system_prompt"] != "你是开发 Agent" {
		t.Fatalf("system_prompt 缺失: %v", in)
	}
	if _, ok := in["policy"]; !ok {
		t.Fatal("policy 快照缺失")
	}
	if _, ok := in["model"]; !ok {
		t.Fatal("model 快照缺失")
	}
	if in["runtime_label"] != "dsh_local" {
		t.Fatalf("runtime_label = %v", in["runtime_label"])
	}

	// 无 agent：不含 system_prompt/policy/model
	in = BuildInput("实现 X", nil, nil, nil, nil, "mock", "default")
	for _, k := range []string{"system_prompt", "policy", "model"} {
		if _, ok := in[k]; ok {
			t.Fatalf("无 agent 不应包含 %s", k)
		}
	}
}

func TestPolicyForManualStripsShell(t *testing.T) {
	agent := &domain.AgentProfile{
		Policy: domain.AgentPolicy{Tools: []string{"bash", "fs", "editor"}, ApprovalPolicy: "manual"},
	}
	p := PolicyFor(agent)
	for _, tool := range p.Tools {
		if tool == "bash" {
			t.Fatalf("manual 审批策略应剔除 bash: %v", p.Tools)
		}
	}
	if len(p.Tools) != 2 {
		t.Fatalf("tools = %v", p.Tools)
	}

	auto := &domain.AgentProfile{Policy: domain.AgentPolicy{Tools: []string{"bash"}, ApprovalPolicy: "auto"}}
	if p := PolicyFor(auto); len(p.Tools) != 1 {
		t.Fatalf("auto 不应裁剪: %v", p.Tools)
	}
}

func TestEffectiveModel(t *testing.T) {
	b := &domain.RuntimeBinding{Provider: "deepseek", Model: "deepseek-v4-flash"}
	agent := &domain.AgentProfile{ModelOverride: domain.ModelRef{Model: "deepseek-v4-pro"}}
	p, m := EffectiveModel(agent, b)
	if p != "deepseek" || m != "deepseek-v4-pro" {
		t.Fatalf("override: %s/%s", p, m)
	}
	p, m = EffectiveModel(nil, b)
	if p != "deepseek" || m != "deepseek-v4-flash" {
		t.Fatalf("binding 默认: %s/%s", p, m)
	}
}
