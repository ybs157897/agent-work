// Package orchestrator 在 Run 创建时组装「Harness 编排」产物：
// 每个 Agent 的提示词（persona）、模型、权限策略如何落到一次 Runtime 会话。
// 纯函数，无副作用；快照写入 run.Input，运行中改配置不影响当前 Run（架构文档 §7）。
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
)

// DefaultRuntimeLabel 无任何偏好时的兜底 Runtime。
const DefaultRuntimeLabel = "mock"

// ResolveRuntimeCandidates 按优先级给出 runtime label 候选（显式 > Agent 偏好 > 兜底）。
// 调用方按序取第一个 ready 的 RuntimeBinding label。
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
	if len(agent.Policy.Tools) > 0 || agent.Policy.ApprovalPolicy != "" || agent.Policy.Sandbox != "" ||
		agent.Policy.PermissionPreset != "" || agent.RuntimePreference.AgentPreset != "" {
		input["policy"] = map[string]any{
			"agent_preset":      agent.RuntimePreference.AgentPreset,
			"permission_preset": agent.Policy.PermissionPreset,
		}
	}
	if agent.ModelOverride.Ref != "" || agent.ModelOverride.Provider != "" || agent.ModelOverride.Model != "" {
		input["model"] = map[string]string{
			"ref":      agent.ModelOverride.Ref,
			"provider": agent.ModelOverride.Provider,
			"model":    agent.ModelOverride.Model,
		}
	}
	return input
}

// EffectiveMode 返回一次 Run 的统一执行模式。显式 Run 配置优先于 Agent 默认值。
func EffectiveMode(explicit *domain.RuntimePreference, agent *domain.AgentProfile) string {
	if explicit != nil && strings.TrimSpace(explicit.Mode) != "" {
		return normalizeMode(explicit.Mode)
	}
	if agent != nil {
		return normalizeMode(agent.RuntimePreference.Mode)
	}
	return "default"
}

func normalizeMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "plan":
		return "plan"
	default:
		return "default"
	}
}

// PolicySnapshot 把跨 Runtime 的权限语义固化为普通 JSON 字段，Adapter 只做原生映射。
func PolicySnapshot(agent *domain.AgentProfile) map[string]any {
	p := PolicyFor(agent)
	sandbox := strings.TrimSpace(p.Sandbox)
	if sandbox == "" {
		sandbox = strings.TrimSpace(p.PermissionPreset)
	}
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	approval := strings.TrimSpace(p.ApprovalPolicy)
	if approval == "" {
		switch sandbox {
		case "danger-full-access":
			approval = "auto"
		default:
			approval = "approve_high_risk"
		}
	}
	tools := append([]string(nil), p.Tools...)
	return map[string]any{
		"agent_preset":      agentPreset(agent),
		"permission_preset": p.PermissionPreset,
		"tools":             tools,
		"approval_policy":   approval,
		"sandbox":           sandbox,
	}
}

func agentPreset(agent *domain.AgentProfile) string {
	if agent == nil {
		return ""
	}
	return agent.RuntimePreference.AgentPreset
}

// ConfigDigest 标识影响 provider 会话语义的配置。摘要变化时不得复用旧会话。
func ConfigDigest(input map[string]any) string {
	stable := map[string]any{
		"system_prompt": input["system_prompt"],
		"model":         input["model"],
		"policy":        input["policy"],
		"mode":          input["mode"],
	}
	b, _ := json.Marshal(stable)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ModelSpec 是一次 Run 的有效模型快照（固化进 run.Input["model"]，adapter 各自映射原生参数）。
// 词汇对齐 pi-ai provider profile：provider 路由 + api 线协议 + base_url/api_key_env + 模型目录参数。
type ModelSpec struct {
	Ref           string `json:"ref,omitempty"`
	ProviderID    string `json:"provider_id,omitempty"`
	ProviderLabel string `json:"provider_label,omitempty"`
	Provider      string `json:"provider,omitempty"`
	API           string `json:"api,omitempty"` // openai-completions | openai-responses | anthropic-messages
	Model         string `json:"model,omitempty"`
	BaseURL       string `json:"base_url,omitempty"`    // OpenAI 兼容端点（DSH cordis baseURL）
	APIKeyEnv     string `json:"api_key_env,omitempty"` // 凭据环境变量名（引用，非密钥）
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
}

// ModelResolver 按 ref 查 models/ 注册表；未命中返回 false。由装配层注入（orchestrator 保持纯函数）。
type ModelResolver func(ref string) (ModelSpec, bool)

// EffectiveModel 决定一次 Run 的有效模型：Binding 默认 < 注册表条目（agent ref）< Agent 显式字段。
func EffectiveModel(agent *domain.AgentProfile, binding *domain.RuntimeBinding, resolve ModelResolver) ModelSpec {
	var spec ModelSpec
	if binding != nil {
		spec.Provider, spec.Model = binding.Provider, binding.Model
	}
	if agent == nil {
		return spec
	}
	if ref := agent.ModelOverride.Ref; ref != "" && resolve != nil {
		if entry, ok := resolve(ref); ok {
			spec = entry
			spec.Ref = ref
		}
		// ref 未命中：保留 binding 默认并继续（显式字段仍可覆盖；调用方负责拒绝/告警）
	}
	if agent.ModelOverride.Provider != "" {
		spec.Provider = agent.ModelOverride.Provider
	}
	if agent.ModelOverride.Model != "" {
		spec.Model = agent.ModelOverride.Model
	}
	return spec
}

// PolicyFor 返回 Run 的权限快照；manual 审批策略剔除高风险工具（DSH SDK 无线审批通道，
// 协议 §8.2：不支持的能力显式拒绝/裁剪，不静默降级）。
func PolicyFor(agent *domain.AgentProfile) domain.AgentPolicy {
	if agent == nil {
		return domain.AgentPolicy{ApprovalPolicy: "auto"}
	}
	p := agent.Policy
	p.Tools = append([]string(nil), agent.Policy.Tools...)
	if p.ApprovalPolicy == "manual" {
		filtered := make([]string, 0, len(p.Tools))
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
