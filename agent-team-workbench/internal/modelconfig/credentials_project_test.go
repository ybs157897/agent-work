package modelconfig

import (
	"path/filepath"
	"testing"
)

func TestCredentialsStoreLoadsProjectFile(t *testing.T) {
	store := NewCredentialsStore(filepath.Join("..", ".."))
	if _, ok, err := store.Get("prov-kimi"); err != nil || !ok {
		t.Fatalf("project credentials.local.yaml missing prov-kimi: ok=%v err=%v", ok, err)
	}
}
