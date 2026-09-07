package modelconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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
	got, ok, err := store.Get("prov-openrouter")
	if err != nil || !ok || got != "sk-test-key" {
		t.Fatalf("got (%q, %v, %v)", got, ok, err)
	}
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("credentials file: %v", err)
	}
	if err := store.Delete("prov-openrouter"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get("prov-openrouter"); err != nil || ok {
		t.Fatalf("expected deleted, got (%v, %v)", ok, err)
	}
}

// P0 防回归：遗留路径 models/credentials.local.yaml 曾以 0644 落盘且含明文 Key；
// 只要被读到就必须立即收紧为 0600（owner-only）。
func TestCredentialsStoreTightensLegacyFilePermOnRead(t *testing.T) {
	dir := t.TempDir()
	legacyDir := filepath.Join(dir, "models")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(legacyDir, "credentials.local.yaml")
	content := "items:\n  - provider_id: prov-legacy\n    api_key: sk-legacy-secret\n"
	if err := os.WriteFile(legacy, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewCredentialsStore(dir)
	got, ok, err := store.Get("prov-legacy")
	if err != nil || !ok || got != "sk-legacy-secret" {
		t.Fatalf("Get 遗留凭据: (%q, %v, %v)", got, ok, err)
	}
	info, err := os.Stat(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("遗留凭据文件权限 = %o, 期望 600（读到即收紧）", perm)
	}
}

// Get 必须区分"未配置"与真实 IO 错误：后者向上返回 err，不得伪装成无凭据。
func TestCredentialsStoreGetDistinguishesIOError(t *testing.T) {
	dir := t.TempDir()
	store := NewCredentialsStore(dir)
	if _, ok, err := store.Get("prov-none"); err != nil || ok {
		t.Fatalf("空库 Get 应 (false, nil), got (%v, %v)", ok, err)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".agent-work", "credentials.local.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get("prov-none"); err == nil || ok {
		t.Fatalf("IO 错误被吞掉: ok=%v err=%v", ok, err)
	}
}

func TestCredentialsStoreConcurrentWritesPreserveEveryKeyAndValidYAML(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ATW_AGENT_WORK_DIR", filepath.Join(dir, "shared-work"))
	stores := []*CredentialsStore{NewCredentialsStore(dir), NewCredentialsStore(filepath.Join(dir, "."))}

	start := make(chan struct{})
	stop := make(chan struct{})
	readerErrors := make(chan error, 1)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, _, err := stores[0].Get("provider-00"); err != nil {
				select {
				case readerErrors <- err:
				default:
				}
				return
			}
			runtime.Gosched()
		}
	}()

	const count = 24
	errs := make(chan error, count)
	var workers sync.WaitGroup
	for i := 0; i < count; i++ {
		i := i
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errs <- stores[i%len(stores)].Set(fmt.Sprintf("provider-%02d", i), fmt.Sprintf("sk-test-%02d", i))
		}()
	}
	close(start)
	workers.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent credential write failed: %v", err)
		}
	}
	select {
	case err := <-readerErrors:
		t.Fatalf("concurrent credential read observed invalid YAML: %v", err)
	default:
	}

	for i := 0; i < count; i++ {
		providerID := fmt.Sprintf("provider-%02d", i)
		want := fmt.Sprintf("sk-test-%02d", i)
		got, ok, err := stores[0].Get(providerID)
		if err != nil || !ok || got != want {
			t.Fatalf("credential %s lost or corrupted: got=%q ok=%v err=%v", providerID, got, ok, err)
		}
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
