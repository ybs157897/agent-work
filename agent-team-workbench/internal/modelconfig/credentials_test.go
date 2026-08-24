package modelconfig

import (
	"os"
	"testing"
)

func TestSuggestAPIKeyEnv(t *testing.T) {
	if SuggestAPIKeyEnv("openrouter") != "OPENROUTER_API_KEY" {
		t.Fatal("openrouter env")
	}
	if SuggestAPIKeyEnv("智谱") != "ATW_8BCEF512_API_KEY" {
		t.Fatalf("zhipu env: %s", SuggestAPIKeyEnv("智谱"))
	}
}

func TestCredentialsStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewCredentialsStore(dir)
	if err := store.Set("prov-openrouter", "sk-test-key"); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("prov-openrouter")
	if !ok || got != "sk-test-key" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("credentials file: %v", err)
	}
	if err := store.Delete("prov-openrouter"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("prov-openrouter"); ok {
		t.Fatal("expected deleted")
	}
}

func TestGenerateProviderID(t *testing.T) {
	if GenerateProviderID("DeepSeek", "deepseek-official") != "prov-deepseek-official" {
		t.Fatal(GenerateProviderID("DeepSeek", "deepseek-official"))
	}
	id := GenerateProviderID("智谱", "智谱")
	if id == "" || id == "prov-model" {
		t.Fatalf("unexpected id: %s", id)
	}
}
