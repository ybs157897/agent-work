package modelconfig

import (
	"path/filepath"
	"sync"
)

// fileLocks serializes read-modify-write operations by their normalized target
// path. The lock table is package-wide so separate Registry or CredentialsStore
// values that point at the same file still share one critical section.
var fileLocks = struct {
	sync.Mutex
	byPath map[string]*sync.Mutex
}{byPath: make(map[string]*sync.Mutex)}

func withFileLock(path string, fn func() error) error {
	key := fileLockKey(path)
	fileLocks.Lock()
	lock := fileLocks.byPath[key]
	if lock == nil {
		lock = &sync.Mutex{}
		fileLocks.byPath[key] = lock
	}
	fileLocks.Unlock()

	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func fileLockKey(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}
