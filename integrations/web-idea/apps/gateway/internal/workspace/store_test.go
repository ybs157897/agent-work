package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCreateWithClientKeyIsConcurrentAndIdempotent(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Options{MaxWorkspaces: 8})
	const workers = 32
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws, _, err := store.Create(root, 0, true, CreateOptions{ClientKey: "create-1", TTL: time.Minute})
			if err != nil {
				errs <- err
				return
			}
			ids <- ws.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent create returned multiple ids: first=%q got=%q", first, id)
		}
	}
	if first == "" {
		t.Fatal("concurrent create returned no workspace")
	}
}

func TestCreateWithClientKeyRejectsPayloadConflict(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	store := NewStore()
	if _, _, err := store.Create(root, 0, true, CreateOptions{ClientKey: "same"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(other, 0, true, CreateOptions{ClientKey: "same"}); !errors.Is(err, ErrClientKeyConflict) {
		t.Fatalf("root conflict error=%v", err)
	}
	if _, _, err := store.Create(root, 0, false, CreateOptions{ClientKey: "same"}); !errors.Is(err, ErrClientKeyConflict) {
		t.Fatalf("read-only conflict error=%v", err)
	}
}

func TestReadOnlyLeaseUsesDefaultAndFixedDeadline(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Options{DefaultReadOnlyTTL: 40 * time.Millisecond, MaxTTL: 100 * time.Millisecond})
	ws, _, err := store.Create(root, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if ws.ExpiresAt.IsZero() {
		t.Fatal("read-only workspace did not receive a default lease")
	}
	deadline := ws.ExpiresAt
	time.Sleep(5 * time.Millisecond)
	got, err := store.Get(ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(deadline) {
		t.Fatalf("Get renewed lease: got=%s want=%s", got.ExpiresAt, deadline)
	}
	time.Sleep(time.Until(deadline) + 5*time.Millisecond)
	if _, err := store.Get(ws.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired workspace Get error=%v", err)
	}
}

func TestTTLUpperBoundAndWorkspaceLimit(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Options{MaxWorkspaces: 1, MaxTTL: 20 * time.Millisecond, DefaultReadOnlyTTL: 10 * time.Millisecond})
	if _, _, err := store.Create(root, 0, false, CreateOptions{TTL: 21 * time.Millisecond}); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("ttl upper bound error=%v", err)
	}
	first, _, err := store.Create(root, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(root, 0, false); !errors.Is(err, ErrLimitReached) {
		t.Fatalf("limit error=%v", err)
	}
	if err := store.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(root, 0, false); err != nil {
		t.Fatalf("slot not released by Delete: %v", err)
	}
}

func TestExpirationHookAndClientKeyRelease(t *testing.T) {
	root := t.TempDir()
	store := NewStore(Options{MaxTTL: time.Minute})
	var mu sync.Mutex
	var expired []string
	store.SetExpirationHook(func(ids []string) {
		mu.Lock()
		expired = append(expired, ids...)
		mu.Unlock()
	})
	ws, _, err := store.Create(root, 0, false, CreateOptions{ClientKey: "release", TTL: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	store.Expire(time.Now())
	mu.Lock()
	defer mu.Unlock()
	if len(expired) != 1 || expired[0] != ws.ID {
		t.Fatalf("expiration hook ids=%v", expired)
	}
	if _, _, err := store.Create(root, 0, false, CreateOptions{ClientKey: "release"}); err != nil {
		t.Fatalf("expired client key was not released: %v", err)
	}
}

func TestCreateBindsResolvedRootJail(t *testing.T) {
	root := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "root")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	store := NewStore()
	ws, _, err := store.Create(link, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Root != resolved {
		t.Fatalf("workspace root=%q want resolved root=%q", ws.Root, resolved)
	}
}
