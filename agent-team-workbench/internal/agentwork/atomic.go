package agentwork

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SyncDir persists directory-entry changes such as a newly created bundle
// directory. Callers use it when a successful external write must survive a
// crash immediately after the first publication.
func SyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// WriteAtomicDurable writes one runtime configuration file without exposing a
// partially written target. The file and its containing directory are synced
// before returning, so a successful call is the only point at which callers
// may consider the external effect complete.
func WriteAtomicDurable(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	dirExisted := true
	if _, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		dirExisted = false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cleanupAtomicTemps(dir)
	tmp, err := os.CreateTemp(dir, ".agent-work-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// Keep the temporary inode owner-only until after rename. A hard crash
	// cannot leave a partially written 0644 prompt/config behind.
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if err := SyncDir(dir); err != nil {
		return err
	}
	if !dirExisted {
		// Persist the new directory entry as well as its contents. This is
		// needed when a caller uses the primitive without a pre-created home.
		return SyncDir(filepath.Dir(dir))
	}
	return nil
}

const atomicTempRetention = 24 * time.Hour

func cleanupAtomicTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-atomicTempRetention)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".agent-work-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}
