package kimiconfig

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

// Apply 把编排层模型快照与进程环境中的 API Key 写入 KIMI_CODE_HOME/config.toml。
// Kimi CLI 不读取 shell 环境变量中的凭据，必须落盘到 config.toml。
func Apply(home string, spec orchestrator.ModelSpec) error {
	return ApplySnapshot(home, runtime.ModelSnapshot{
		Ref: spec.Ref, ProviderID: spec.ProviderID, ProviderLabel: spec.ProviderLabel,
		Provider: spec.Provider, API: spec.API, Model: spec.Model,
		BaseURL: spec.BaseURL, APIKeyEnv: spec.APIKeyEnv,
		ContextWindow: spec.ContextWindow, MaxTokens: spec.MaxTokens,
	})
}

// ApplySnapshot 在 Run 执行前按 run.Input 模型快照同步 Kimi 配置。
func ApplySnapshot(home string, snap runtime.ModelSnapshot) error {
	_, err := ApplySnapshotIfChanged(home, snap)
	return err
}

// ApplySnapshotIfChanged 写入 config.toml；内容未变时跳过写盘并返回 changed=false。
func ApplySnapshotIfChanged(home string, snap runtime.ModelSnapshot) (bool, error) {
	spec := orchestrator.ModelSpec{
		Ref: snap.Ref, ProviderID: snap.ProviderID, ProviderLabel: snap.ProviderLabel,
		Provider: snap.Provider, API: snap.API, Model: snap.Model,
		BaseURL: snap.BaseURL, APIKeyEnv: snap.APIKeyEnv,
		ContextWindow: snap.ContextWindow, MaxTokens: snap.MaxTokens,
	}
	return applyIfChanged(home, spec)
}

func applyIfChanged(home string, spec orchestrator.ModelSpec) (bool, error) {
	home = strings.TrimSpace(home)
	if home == "" {
		return false, fmt.Errorf("%w: KIMI_CODE_HOME 未配置", domain.ErrValidation)
	}
	model := strings.TrimSpace(spec.Model)
	if model == "" {
		return false, fmt.Errorf("%w: Kimi 模型名不能为空", domain.ErrValidation)
	}
	envKey := strings.TrimSpace(spec.APIKeyEnv)
	if envKey == "" {
		return false, fmt.Errorf("%w: Kimi 使用注册表模型 %q 需要 api_key_env（请在模型页保存凭据）", domain.ErrValidation, spec.Ref)
	}
	apiKey := strings.TrimSpace(os.Getenv(envKey))
	if apiKey == "" {
		return false, fmt.Errorf("%w: Kimi 模型 %q 缺少 API Key（请在模型页保存凭据）", domain.ErrValidation, spec.Ref)
	}
	baseURL, err := ResolveBaseURL(spec)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return false, err
	}

	providerKey := providerKey(spec)
	alias := modelAlias(spec)
	ctx := spec.ContextWindow
	if ctx <= 0 {
		ctx = 256000
	}

	content := fmt.Sprintf(`default_model = %q

[providers.%s]
type = %q
base_url = %q
api_key = %q

[models.%s]
provider = %q
model = %q
max_context_size = %d
`, alias, providerKey, providerType(spec), baseURL, apiKey,
		modelsTableKey(alias), providerKey, model, ctx)

	path := filepath.Join(home, "config.toml")
	if prev, err := os.ReadFile(path); err == nil && string(prev) == content {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// ResolveBaseURL 返回 Kimi 可用的 OpenAI 兼容端点。
func ResolveBaseURL(spec orchestrator.ModelSpec) (string, error) {
	if base := strings.TrimSpace(spec.BaseURL); base != "" {
		return strings.TrimRight(base, "/"), nil
	}
	if strings.EqualFold(spec.Provider, "moonshot") {
		return "https://api.kimi.com/coding/v1", nil
	}
	return "", fmt.Errorf("%w: 模型 %q 缺少 base_url，无法在 Kimi 中路由", domain.ErrValidation, spec.Ref)
}

// ProviderReady 判断 KIMI_CODE_HOME 是否已配置 provider 且含 api_key。
func ProviderReady(home string) bool {
	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "api_key = ")
}

func providerType(spec orchestrator.ModelSpec) string {
	switch strings.ToLower(strings.TrimSpace(spec.API)) {
	case "anthropic", "anthropic-messages":
		return "anthropic"
	case "openai-responses", "openai_responses":
		return "openai_responses"
	default:
		if strings.EqualFold(spec.Provider, "moonshot") {
			return "kimi"
		}
		return "openai"
	}
}

func providerKey(spec orchestrator.ModelSpec) string {
	if id := strings.TrimSpace(spec.ProviderID); id != "" {
		key := "atw-" + providerIDPattern.ReplaceAllString(strings.ToLower(id), "-")
		return strings.Trim(key, "-")
	}
	key := "atw-" + providerIDPattern.ReplaceAllString(strings.ToLower(spec.Provider), "-")
	return strings.Trim(key, "-")
}

func modelAlias(spec orchestrator.ModelSpec) string {
	if ref := strings.TrimSpace(spec.Ref); ref != "" {
		return ref
	}
	return strings.TrimSpace(spec.Model)
}

// ModelAlias 返回 config.toml 中的模型别名（注册表 ref 优先于 API model id）。
func ModelAlias(snap runtime.ModelSnapshot) string {
	if ref := strings.TrimSpace(snap.Ref); ref != "" {
		return ref
	}
	return strings.TrimSpace(snap.Model)
}

func modelsTableKey(alias string) string {
	return `"` + strings.ReplaceAll(alias, `"`, `\"`) + `"`
}
