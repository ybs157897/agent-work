package orchestrator

import (
	"strings"
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
		Policy:        domain.AgentPolicy{Tools: []string{"bash", "fs"}, ApprovalPolicy: "approve_high_risk", Sandbox: "workspace-write"},
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

func TestApplyOutputContractKeepsInstructionAndSystemPromptStable(t *testing.T) {
	in := BuildInput("用户原文", nil, nil, nil, &domain.AgentProfile{Instructions: "原有 Agent 章程"}, "mock", "requested")
	baseDigest := ConfigDigest(in)
	if !ApplyOutputContract(in, OutputContractLanguageGUIV1) {
		t.Fatal("languagegui/v1 应受支持")
	}
	if in["instruction"] != "用户原文" {
		t.Fatalf("output contract 不得改写 instruction: %#v", in["instruction"])
	}
	if in["output_contract"] != OutputContractLanguageGUIV1 {
		t.Fatalf("output_contract 未固化: %#v", in)
	}
	prompt, _ := in["system_prompt"].(string)
	if !strings.HasPrefix(prompt, "原有 Agent 章程\n\n") || !strings.Contains(prompt, languageGUIV1Marker) {
		t.Fatalf("system prompt 合并错误: %q", prompt)
	}
	for _, fragment := range []string{
		`review-summary`,
		`changes_requested`,
		`passed_with_warnings`,
		`inconclusive`,
		`critical`,
		`high`,
		`running`,
		`next_steps`,
		`canvas`,
		`at most 24 nodes`,
		`at most 30 findings`,
		`Do not repeat this contract`,
		`unsafe URLs`,
	} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("LanguageGUI contract 缺少 %q: %q", fragment, prompt)
		}
	}
	ApplyOutputContract(in, OutputContractLanguageGUIV1)
	if strings.Count(in["system_prompt"].(string), languageGUIV1Marker) != 1 {
		t.Fatalf("重复应用不得重复协议: %q", in["system_prompt"])
	}
	if ConfigDigest(in) == baseDigest {
		t.Fatal("output contract 必须改变 config digest，阻断旧 session 复用")
	}
	if SupportsOutputContract("unknown/v9") || ApplyOutputContract(in, "unknown/v9") {
		t.Fatal("未知 output contract 必须拒绝")
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
	if len(agent.Policy.Tools) != 3 {
		t.Fatalf("PolicyFor 不得修改 Agent 原始工具列表: %v", agent.Policy.Tools)
	}

	auto := &domain.AgentProfile{Policy: domain.AgentPolicy{Tools: []string{"bash"}, ApprovalPolicy: "auto"}}
	if p := PolicyFor(auto); len(p.Tools) != 1 {
		t.Fatalf("auto 不应裁剪: %v", p.Tools)
	}
}

func TestModePolicySnapshotAndConfigDigest(t *testing.T) {
	agent := &domain.AgentProfile{
		RuntimePreference: domain.RuntimePreference{Mode: "plan", AgentPreset: "standard"},
		Policy:            domain.AgentPolicy{PermissionPreset: "read-only"},
	}
	if got := EffectiveMode(nil, agent); got != "plan" {
		t.Fatalf("mode = %s", got)
	}
	policy := PolicySnapshot(agent)
	if policy["sandbox"] != "read-only" || policy["approval_policy"] != "approve_high_risk" {
		t.Fatalf("policy = %#v", policy)
	}
	input := map[string]any{"system_prompt": "a", "policy": policy, "mode": "plan"}
	d1 := ConfigDigest(input)
	input["system_prompt"] = "b"
	if d2 := ConfigDigest(input); d1 == d2 {
		t.Fatal("提示词变化必须切断 provider session 复用")
	}
}

func TestEffectiveModel(t *testing.T) {
	b := &domain.RuntimeBinding{Provider: "deepseek", Model: "deepseek-v4-flash"}
	agent := &domain.AgentProfile{ModelOverride: domain.ModelRef{Model: "deepseek-v4-pro"}}
	spec := EffectiveModel(agent, b, nil)
	if spec.Provider != "deepseek" || spec.Model != "deepseek-v4-pro" {
		t.Fatalf("override: %+v", spec)
	}
	spec = EffectiveModel(nil, b, nil)
	if spec.Provider != "deepseek" || spec.Model != "deepseek-v4-flash" {
		t.Fatalf("binding 默认: %+v", spec)
	}
}

func TestEffectiveModelRegistryRef(t *testing.T) {
	b := &domain.RuntimeBinding{Provider: "mock-provider", Model: "mock-model-v1"}
	resolve := func(ref string) (ModelSpec, bool) {
		if ref == "deepseek-v4-flash" {
			return ModelSpec{Provider: "deepseek-official", Model: "deepseek-v4-flash",
				BaseURL: "https://api.deepseek.com", APIKeyEnv: "DEEPSEEK_API_KEY"}, true
		}
		return ModelSpec{}, false
	}

	// ref 命中：注册表覆盖 binding 默认
	agent := &domain.AgentProfile{ModelOverride: domain.ModelRef{Ref: "deepseek-v4-flash"}}
	spec := EffectiveModel(agent, b, resolve)
	if spec.Ref != "deepseek-v4-flash" || spec.Provider != "deepseek-official" ||
		spec.Model != "deepseek-v4-flash" || spec.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("ref 命中: %+v", spec)
	}

	// ref 命中 + 显式字段覆盖注册表
	agent.ModelOverride.Model = "deepseek-v4-pro"
	spec = EffectiveModel(agent, b, resolve)
	if spec.Model != "deepseek-v4-pro" || spec.Provider != "deepseek-official" {
		t.Fatalf("显式覆盖注册表: %+v", spec)
	}

	// ref 未命中：回退 binding 默认，不泄漏注册表字段
	agent.ModelOverride = domain.ModelRef{Ref: "ghost"}
	spec = EffectiveModel(agent, b, resolve)
	if spec.Ref != "" || spec.Provider != "mock-provider" || spec.Model != "mock-model-v1" {
		t.Fatalf("ref 未命中应回退 binding: %+v", spec)
	}
}

func TestEffectiveModelReasoningEffort(t *testing.T) {
	agent := &domain.AgentProfile{ModelOverride: domain.ModelRef{Ref: "ox-alpha", ReasoningEffort: "high"}}
	spec := EffectiveModel(agent, nil, nil)
	if spec.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q", spec.ReasoningEffort)
	}
}
