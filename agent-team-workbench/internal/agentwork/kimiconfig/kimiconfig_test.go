package kimiconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ybs/agent-team-workbench/internal/orchestrator"
	"github.com/ybs/agent-team-workbench/internal/runtime"
)

func TestApplyWritesProviderAndModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOONSHOT_API_KEY", "sk-test-kimi")
	spec := orchestrator.ModelSpec{
		Ref: "kimi-2-7", ProviderID: "prov-kimi", ProviderLabel: "Kimi",
		Provider: "moonshot", API: "openai-completions", Model: "kimi-k2.7-code",
		BaseURL: "https://api.kimi.com/coding/v1", APIKeyEnv: "MOONSHOT_API_KEY",
		ContextWindow: 256000,
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
		`default_model = "kimi-2-7"`,
		`type = "kimi"`,
		`base_url = "https://api.kimi.com/coding/v1"`,
		`api_key = "sk-test-kimi"`,
		`model = "kimi-k2.7-code"`,
		`max_context_size = 256000`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
	if !ProviderReady(home) {
		t.Fatal("expected provider ready")
	}
}

func TestApplySnapshotIfChangedSkipsRewrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MOONSHOT_API_KEY", "sk-test-kimi")
	spec := orchestrator.ModelSpec{
		Ref: "kimi-2-7", ProviderID: "prov-kimi", Provider: "moonshot",
		Model: "kimi-k2.7-code", APIKeyEnv: "MOONSHOT_API_KEY",
		BaseURL: "https://api.kimi.com/coding/v1",
	}
	changed, err := ApplySnapshotIfChanged(home, runtime.ModelSnapshot{
		Ref: spec.Ref, ProviderID: spec.ProviderID, Provider: spec.Provider,
		Model: spec.Model, BaseURL: spec.BaseURL, APIKeyEnv: spec.APIKeyEnv,
	})
	if err != nil || !changed {
		t.Fatalf("first apply changed=%v err=%v", changed, err)
	}
	changed, err = ApplySnapshotIfChanged(home, runtime.ModelSnapshot{
		Ref: spec.Ref, ProviderID: spec.ProviderID, Provider: spec.Provider,
		Model: spec.Model, BaseURL: spec.BaseURL, APIKeyEnv: spec.APIKeyEnv,
	})
	if err != nil || changed {
		t.Fatalf("second apply changed=%v err=%v", changed, err)
	}
}

func TestApplyRequiresAPIKey(t *testing.T) {
	err := Apply(t.TempDir(), orchestrator.ModelSpec{
		Ref: "kimi-2-7", Provider: "moonshot", Model: "kimi-k2.7-code",
		APIKeyEnv: "MOONSHOT_API_KEY",
	})
	if err == nil || !strings.Contains(err.Error(), "API Key") {
		t.Fatalf("expected API key error, got %v", err)
	}
}
