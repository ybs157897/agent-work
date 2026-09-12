package sqlstore_test

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// legacyKnowledgeTables are the retired librarian's storage objects. They are
// preserved in place for recovery (migration 0057 never drops them), and this
// test pins the retirement as a code-level fact: no Go source may read or
// write them, which is what makes a double-write of the old and new
// administrator impossible.
//
// The names are assembled from fragments so this assertion does not match
// itself.
func legacyKnowledgeTables() []string {
	prefix := "knowledge_"
	return []string{
		prefix + "items",
		prefix + "versions",
		prefix + "sources",
		prefix + "version_sources",
		prefix + "relations",
		prefix + "relation_sources",
		prefix + "repeals",
		prefix + "submissions",
		prefix + "query_snapshots",
		prefix + "jobs",
		prefix + "job_actions",
		prefix + "run_captures",
		prefix + "librarian_configs",
	}
}

// TestLegacyKnowledgeTablesAreNotReferencedByGoSource fails if any Go file
// still names a retired table, which would mean an active code path could
// write the old schema alongside the new library.
func TestLegacyKnowledgeTablesAreNotReferencedByGoSource(t *testing.T) {
	root := moduleRoot(t)
	self := filepath.Base(currentFile(t))
	legacy := legacyKnowledgeTables()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "web", "testdata", "runtimes", ".agent-work":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || d.Name() == self {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(raw)
		rel, _ := filepath.Rel(root, path)
		for _, table := range legacy {
			if strings.Contains(text, table) {
				offenders = append(offenders, rel+": "+table)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("Go source still references retired knowledge tables:\n%s", strings.Join(offenders, "\n"))
	}
}

// TestLegacyKnowledgeTablesStillExist protects recoverability: the migration
// must preserve the retired tables rather than dropping user data.
func TestLegacyKnowledgeTablesStillExist(t *testing.T) {
	db := openKnowledgeLibraryTestDB(t)
	defer db.Close()
	for _, table := range legacyKnowledgeTables() {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("retired table %s must be preserved for recovery: %v", table, err)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func currentFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve the current test file")
	}
	return file
}

// openKnowledgeLibraryTestDB applies every migration to a throwaway database.
func openKnowledgeLibraryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openWakeupTestDB(t)
}
