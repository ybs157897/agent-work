package codexconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/orchestrator"
)

func TestApplyRegistryProviderWritesConfig(t *testing.T) {
	home := t.TempDir()
	spec := orchestrator.ModelSpec{
		Ref: "kimi-2-7", ProviderID: "prov-kimi",
		ProviderLabel: "Kimi", Provider: "moonshot",
		Model: "kimi-k2.7-code", API: "openai-responses", APIKeyEnv: "MOONSHOT_API_KEY",
	}
	if err := Apply(home, spec); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		`model = "kimi-k2.7-code"`,
		`model_provider = "atw-prov-kimi"`,
		`web_search = "disabled"`,
		`model_reasoning_effort = "medium"`,
		`base_url = "https://api.kimi.com/coding/v1"`,
		`env_key = "MOONSHOT_API_KEY"`,
		`wire_api = "responses"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestApplyRequiresAPIKeyEnvForRegistryProvider(t *testing.T) {
	err := Apply(t.TempDir(), orchestrator.ModelSpec{
		Ref: "x", Provider: "openrouter", Model: "ox-alpha",
		API:     "openai-responses",
		BaseURL: "https://openrouter.ai/api/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("expected api_key_env error, got %v", err)
	}
}

func TestApplyRejectsCompletionsProviderBeforeWritingConfig(t *testing.T) {
	home := filepath.Join(t.TempDir(), "codex-home")
	err := Apply(home, orchestrator.ModelSpec{
		Provider: "deepseek-official", Model: "deepseek-v4-flash", API: "openai-completions",
		APIKeyEnv: "DEEPSEEK_API_KEY", BaseURL: "https://token.wasu.cn/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "api=openai-responses") {
		t.Fatalf("expected Responses-only validation error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("rejected provider must not write config, stat error=%v", err)
	}
}

func TestValidateSpecRejectsCredentialBearingBaseURL(t *testing.T) {
	for _, baseURL := range []string{
		"https://user:password@example.com/v1",
		"https://example.com/v1?token=secret",
		"https://example.com/v1#secret",
	} {
		if err := ValidateSpec(orchestrator.ModelSpec{
			Provider: "openrouter", API: "openai-responses", Model: "model",
			APIKeyEnv: "OPENROUTER_KEY", BaseURL: baseURL,
		}); err == nil {
			t.Fatalf("credential-bearing base_url %q must be rejected", baseURL)
		}
	}
}

func TestApplyUsesAgentReasoningEffort(t *testing.T) {
	home := t.TempDir()
	if err := Apply(home, orchestrator.ModelSpec{
		Ref: "ox-alpha", ProviderID: "prov-openrouter", Provider: "openrouter",
		Model: "ox-alpha", API: "openai-responses", APIKeyEnv: "OPENROUTER_API_KEY",
		BaseURL: "https://openrouter.ai/api/v1", ReasoningEffort: "high",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `model_reasoning_effort = "high"`) {
		t.Fatalf("expected high effort:\n%s", got)
	}
}

func TestEffectiveReasoningEffort(t *testing.T) {
	if got := EffectiveReasoningEffort(""); got != "medium" {
		t.Fatalf("empty = %q", got)
	}
	if got := EffectiveReasoningEffort("HIGH"); got != "high" {
		t.Fatalf("normalize = %q", got)
	}
	if got := EffectiveReasoningEffort("ULTRA"); got != "ultra" {
		t.Fatalf("ultra = %q", got)
	}
	if got := EffectiveReasoningEffort("nope"); got != "medium" {
		t.Fatalf("unknown = %q", got)
	}
}

func TestCustomProviderReady(t *testing.T) {
	home := t.TempDir()
	if CustomProviderReady(home) {
		t.Fatal("empty home should not be ready")
	}
	t.Setenv("MOONSHOT_API_KEY", "sk-test")
	if err := Apply(home, orchestrator.ModelSpec{
		ProviderID: "prov-kimi", Provider: "moonshot",
		Model: "kimi-k2.7-code", API: "openai-responses", APIKeyEnv: "MOONSHOT_API_KEY",
	}); err != nil {
		t.Fatal(err)
	}
	if !CustomProviderReady(home) {
		t.Fatal("expected custom provider ready")
	}
}
