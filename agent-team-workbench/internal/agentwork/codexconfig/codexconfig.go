package codexconfig

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/domain"
	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

var providerIDPattern = regexp.MustCompile(`[^a-z0-9-]+`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrStaticConfig identifies a model target that can never be applied until
// its durable Agent target is changed (as opposed to a transient missing
// credential or filesystem failure).
var ErrStaticConfig = errors.New("static Codex configuration invalid")

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
	case "minimal", "low", "medium", "high", "xhigh", "ultra":
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
	if err := ValidateSpec(spec); err != nil {
		return err
	}
	model := strings.TrimSpace(spec.Model)
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	effort := EffectiveReasoningEffort(spec.ReasoningEffort)
	if provider == "" || provider == "codex" || provider == "openai" {
		content := fmt.Sprintf("model = %q\nmodel_reasoning_effort = %q\n", model, effort)
		return agentwork.WriteAtomicDurable(filepath.Join(home, "config.toml"), []byte(content), 0o644)
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

	return agentwork.WriteAtomicDurable(filepath.Join(home, "config.toml"), []byte(content), 0o644)
}

// ValidateSpec checks all static constraints before any home directory or
// config file is touched. Credential presence is intentionally checked by
// Apply at execution time because it can change without changing the target.
func ValidateSpec(spec orchestrator.ModelSpec) error {
	if strings.TrimSpace(spec.Model) == "" {
		return fmt.Errorf("%w: %w: Codex 模型名不能为空", ErrStaticConfig, domain.ErrValidation)
	}
	if err := validateBaseURL(spec.BaseURL); err != nil {
		return fmt.Errorf("%w: %w", ErrStaticConfig, err)
	}
	if err := ValidateProviderAPI(spec); err != nil {
		return fmt.Errorf("%w: %w", ErrStaticConfig, err)
	}
	if envKey := strings.TrimSpace(spec.APIKeyEnv); envKey != "" && !envNamePattern.MatchString(envKey) {
		return fmt.Errorf("%w: %w: Codex api_key_env 不是合法环境变量名", ErrStaticConfig, domain.ErrValidation)
	}
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	if provider == "" || provider == "codex" || provider == "openai" {
		return nil
	}
	if strings.TrimSpace(spec.APIKeyEnv) == "" {
		return fmt.Errorf("%w: %w: Codex 自定义 provider %q 需要 api_key_env", ErrStaticConfig, domain.ErrValidation, provider)
	}
	if _, err := ResolveBaseURL(spec); err != nil {
		return fmt.Errorf("%w: %w", ErrStaticConfig, err)
	}
	return nil
}

// ValidateProviderAPI enforces the Codex app-server wire contract. Codex's
// built-in OpenAI route is already Responses-native; every custom provider
// must explicitly opt into the same protocol in the model registry.
func ValidateProviderAPI(spec orchestrator.ModelSpec) error {
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	if provider == "" || provider == "codex" || provider == "openai" {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(spec.API)) != "openai-responses" {
		return fmt.Errorf("%w: Codex 自定义 provider %q 仅支持 api=openai-responses（当前为 %q）", domain.ErrValidation, provider, spec.API)
	}
	return nil
}

// ResolveBaseURL 返回 Codex 可用的 OpenAI 兼容端点；注册表未写时补常见默认值。
func ResolveBaseURL(spec orchestrator.ModelSpec) (string, error) {
	if base := strings.TrimSpace(spec.BaseURL); base != "" {
		if err := validateBaseURL(base); err != nil {
			return "", err
		}
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

func validateBaseURL(base string) error {
	base = strings.TrimSpace(base)
	if base == "" {
		return nil
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: Codex base_url 必须是无凭据、无 query/fragment 的 http(s) URL", domain.ErrValidation)
	}
	return nil
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
