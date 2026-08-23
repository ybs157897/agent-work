package modelconfig

import (
	"path/filepath"
	"testing"
)

func TestCredentialsStoreLoadsProjectFile(t *testing.T) {
	dir := filepath.Join("..", "..", "models")
	store := NewCredentialsStore(dir)
	if _, ok := store.Get("prov-kimi"); !ok {
		t.Fatal("project credentials.local.yaml missing prov-kimi")
	}
}
