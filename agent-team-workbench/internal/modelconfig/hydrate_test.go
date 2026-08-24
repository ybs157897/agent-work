package modelconfig

import (
	"os"
	"testing"
)

func TestHydrateEnv(t *testing.T) {
	dir := t.TempDir()
	store := NewCredentialsStore(dir)
	if err := store.Set("prov-deepseek-official", "sk-ds"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")
	n := store.HydrateEnv([]ProviderDef{{
		ID: "prov-deepseek-official", APIKeyEnv: "DEEPSEEK_API_KEY",
	}})
	if n != 1 {
		t.Fatalf("hydrated=%d", n)
	}
	if os.Getenv("DEEPSEEK_API_KEY") != "sk-ds" {
		t.Fatalf("env=%q", os.Getenv("DEEPSEEK_API_KEY"))
	}
}
