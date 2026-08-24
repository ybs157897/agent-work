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
		Ref: "deepseek-v4-flash", ProviderID: "prov-deepseek-official",
		ProviderLabel: "DeepSeek", Provider: "deepseek-official",
		Model: "deepseek-v4-flash", API: "openai-completions", APIKeyEnv: "DEEPSEEK_API_KEY",
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
		`model = "deepseek-v4-flash"`,
		`model_provider = "atw-prov-deepseek-official"`,
		`web_search = "disabled"`,
		`model_reasoning_effort = "medium"`,
		`base_url = "https://api.deepseek.com/v1"`,
		`env_key = "DEEPSEEK_API_KEY"`,
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
		BaseURL: "https://openrouter.ai/api/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("expected api_key_env error, got %v", err)
	}
}

func TestApplyUsesAgentReasoningEffort(t *testing.T) {
	home := t.TempDir()
	if err := Apply(home, orchestrator.ModelSpec{
		Ref: "ox-alpha", ProviderID: "prov-openrouter", Provider: "openrouter",
		Model: "ox-alpha", APIKeyEnv: "OPENROUTER_API_KEY",
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
	if got := EffectiveReasoningEffort("nope"); got != "medium" {
		t.Fatalf("unknown = %q", got)
	}
}

func TestCustomProviderReady(t *testing.T) {
	home := t.TempDir()
	if CustomProviderReady(home) {
		t.Fatal("empty home should not be ready")
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")
	if err := Apply(home, orchestrator.ModelSpec{
		ProviderID: "prov-deepseek-official", Provider: "deepseek-official",
		Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY",
	}); err != nil {
		t.Fatal(err)
	}
	if !CustomProviderReady(home) {
		t.Fatal("expected custom provider ready")
	}
}
