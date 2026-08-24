package codexconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

var providerIDPattern = regexp.MustCompile(`[^a-z0-9-]+`)

// Apply 把编排层模型快照写入 CODEX_HOME/config.toml，供 Codex app-server 使用注册表里的
// base_url / api_key_env，无需 codex login。
func Apply(home string, spec orchestrator.ModelSpec) error {
	return ApplySnapshot(home, runtime.ModelSnapshot{
		Ref: spec.Ref, ProviderID: spec.ProviderID, ProviderLabel: spec.ProviderLabel,
		Provider: spec.Provider, API: spec.API, Model: spec.Model,
		BaseURL: spec.BaseURL, APIKeyEnv: spec.APIKeyEnv,
		ContextWindow: spec.ContextWindow, MaxTokens: spec.MaxTokens,
		ReasoningEffort: spec.ReasoningEffort,
	})
}

// ApplySnapshot 在 Run 执行前按 run.Input 模型快照同步 Codex 配置。
func ApplySnapshot(home string, snap runtime.ModelSnapshot) error {
	spec := orchestrator.ModelSpec{
		Ref: snap.Ref, ProviderID: snap.ProviderID, ProviderLabel: snap.ProviderLabel,
		Provider: snap.Provider, API: snap.API, Model: snap.Model,
		BaseURL: snap.BaseURL, APIKeyEnv: snap.APIKeyEnv,
		ContextWindow: snap.ContextWindow, MaxTokens: snap.MaxTokens,
		ReasoningEffort: snap.ReasoningEffort,
	}
	return apply(home, spec)
}

// EffectiveReasoningEffort 归一化 Codex reasoning 等级；未知/空回退 medium。
func EffectiveReasoningEffort(effort string) string {
	switch strings.TrimSpace(strings.ToLower(effort)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.TrimSpace(strings.ToLower(effort))
	default:
		return "medium"
	}
}

func apply(home string, spec orchestrator.ModelSpec) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return fmt.Errorf("%w: CODEX_HOME 未配置", domain.ErrValidation)
	}
	model := strings.TrimSpace(spec.Model)
	if model == "" {
		return fmt.Errorf("%w: Codex 模型名不能为空", domain.ErrValidation)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	effort := EffectiveReasoningEffort(spec.ReasoningEffort)
	if provider == "" || provider == "codex" || provider == "openai" {
		content := fmt.Sprintf("model = %q\nmodel_reasoning_effort = %q\n", model, effort)
		return os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o644)
	}

	envKey := strings.TrimSpace(spec.APIKeyEnv)
	if envKey == "" {
		return fmt.Errorf("%w: Codex 使用注册表模型 %q 需要 api_key_env（请在模型页保存凭据）", domain.ErrValidation, spec.Ref)
	}
	baseURL, err := ResolveBaseURL(spec)
	if err != nil {
		return err
	}

	providerKey := providerKey(spec)
	name := strings.TrimSpace(spec.ProviderLabel)
	if name == "" {
		name = strings.TrimSpace(spec.Provider)
	}
	if name == "" {
		name = providerKey
	}

	content := fmt.Sprintf(`model = %q
model_provider = %q
web_search = "disabled"
model_reasoning_effort = %q

[model_providers.%s]
name = %q
base_url = %q
env_key = %q
wire_api = "responses"
`, model, providerKey, effort, providerKey, name, baseURL, envKey)

	return os.WriteFile(filepath.Join(home, "config.toml"), []byte(content), 0o644)
}

// ResolveBaseURL 返回 Codex 可用的 OpenAI 兼容端点；注册表未写时补常见默认值。
func ResolveBaseURL(spec orchestrator.ModelSpec) (string, error) {
	if base := strings.TrimSpace(spec.BaseURL); base != "" {
		return strings.TrimRight(base, "/"), nil
	}
	switch {
	case strings.EqualFold(spec.ProviderID, "prov-deepseek-official"),
		strings.EqualFold(spec.Provider, "deepseek-official"):
		return "https://api.deepseek.com/v1", nil
	case strings.EqualFold(spec.Provider, "moonshot"):
		return "https://api.kimi.com/coding/v1", nil
	}
	return "", fmt.Errorf("%w: 模型 %q 缺少 base_url，无法在 Codex 中路由", domain.ErrValidation, spec.Ref)
}

func providerKey(spec orchestrator.ModelSpec) string {
	if id := strings.TrimSpace(spec.ProviderID); id != "" {
		key := "atw-" + providerIDPattern.ReplaceAllString(strings.ToLower(id), "-")
		return strings.Trim(key, "-")
	}
	key := "atw-" + providerIDPattern.ReplaceAllString(strings.ToLower(spec.Provider), "-")
	return strings.Trim(key, "-")
}

// CustomProviderReady 判断 CODEX_HOME 是否已配置自定义 provider 且对应 env 已注入。
func CustomProviderReady(home string) bool {
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return false
	}
	envKey := parseEnvKey(string(data))
	if envKey == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv(envKey)) != ""
}

var envKeyLine = regexp.MustCompile(`(?m)^env_key\s*=\s*"([^"]+)"`)

func parseEnvKey(content string) string {
	m := envKeyLine.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}
