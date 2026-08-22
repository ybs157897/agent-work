// Package orchestrator 在 Run 创建时组装「Harness 编排」产物：
// 每个 Agent 的提示词（persona）、模型、权限策略如何落到一次 Runtime 会话。
// 纯函数，无副作用；快照写入 run.Input，运行中改配置不影响当前 Run（架构文档 §7）。
package orchestrator

import (
	"github.com/ybs/agent-team-workbench/internal/domain"
)

// DefaultRuntimeLabel 无任何偏好时的兜底 Runtime。
const DefaultRuntimeLabel = "mock"

// ResolveRuntimeCandidates 按优先级给出 runtime label 候选（显式 > Agent 偏好 > 兜底）。
// 调用方按序取第一个存在对应 RuntimeBinding 的 label。
func ResolveRuntimeCandidates(explicit *domain.RuntimePreference, agent *domain.AgentProfile) []string {
	var out []string
	seen := map[string]bool{}
	add := func(label string) {
		if label != "" && !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	if explicit != nil {
		add(explicit.Preferred)
		for _, f := range explicit.Fallbacks {
			add(f)
		}
	}
	if agent != nil {
		add(agent.RuntimePreference.Preferred)
		for _, f := range agent.RuntimePreference.Fallbacks {
			add(f)
		}
	}
	add(DefaultRuntimeLabel)
	return out
}

// BuildInput 组装 run.Input：
// instruction 保持用户原文（UI 展示用）；persona / policy / model 作为独立快照键。
func BuildInput(instruction string, acceptanceCriteria []string, requirements map[string]string,
	explicit *domain.RuntimePreference, agent *domain.AgentProfile, resolvedRuntime, scheduleReason string) map[string]any {

	input := map[string]any{
		"instruction":         instruction,
		"acceptance_criteria": acceptanceCriteria,
		"requirements":        requirements,
		"runtime_preference":  explicit,
		"runtime_label":       resolvedRuntime,
		"scheduling":          map[string]string{"reason": scheduleReason},
	}
	if agent == nil {
		return input
	}
	if agent.Instructions != "" {
		input["system_prompt"] = agent.Instructions
	}
	if len(agent.Policy.Tools) > 0 || agent.Policy.ApprovalPolicy != "" || agent.Policy.Sandbox != "" {
		input["policy"] = map[string]any{
			"tools":           agent.Policy.Tools,
			"approval_policy": agent.Policy.ApprovalPolicy,
			"sandbox":         agent.Policy.Sandbox,
		}
	}
	if agent.ModelOverride.Provider != "" || agent.ModelOverride.Model != "" {
		input["model"] = map[string]string{
			"provider": agent.ModelOverride.Provider,
			"model":    agent.ModelOverride.Model,
		}
	}
	return input
}

// EffectiveModel 决定一次 Run 的 provider/model：Agent 覆盖 > Binding 默认。
func EffectiveModel(agent *domain.AgentProfile, binding *domain.RuntimeBinding) (provider, model string) {
	if binding != nil {
		provider, model = binding.Provider, binding.Model
	}
	if agent != nil {
		if agent.ModelOverride.Provider != "" {
			provider = agent.ModelOverride.Provider
		}
		if agent.ModelOverride.Model != "" {
			model = agent.ModelOverride.Model
		}
	}
	return provider, model
}

// PolicyFor 返回 Run 的权限快照；manual 审批策略剔除高风险工具（DSH SDK 无线审批通道，
// 协议 §8.2：不支持的能力显式拒绝/裁剪，不静默降级）。
func PolicyFor(agent *domain.AgentProfile) domain.AgentPolicy {
	if agent == nil {
		return domain.AgentPolicy{ApprovalPolicy: "auto"}
	}
	p := agent.Policy
	if p.ApprovalPolicy == "manual" {
		filtered := p.Tools[:0]
		for _, t := range p.Tools {
			if t == "bash" || t == "shell" {
				continue
			}
			filtered = append(filtered, t)
		}
		p.Tools = filtered
	}
	return p
}
