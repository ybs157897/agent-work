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

// legacyKnowledgeTables are the retired librarian's storage objects. Migration
// 0057 preserved them byte-for-byte for recovery; migration 0065 drops them,
// because the product decision of 2026-09-12 is that none of the old
// administrator's functionality or data is wanted any more. They were exported
// to .acceptance/backups/2026-09-12-legacy-knowledge-tables/ before the drop.
//
// The names are assembled from fragments so the assertions below do not match
// themselves.
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

// legacyKnowledgeIndexObjects are the retired search index and its state table.
// knowledge_index is an FTS5 virtual table, so its shadow tables
// (knowledge_index_config/content/data/docsize/idx) disappear with it; naming
// them here keeps the drop honest instead of assumed.
func legacyKnowledgeIndexObjects() []string {
	base := "knowledge_" + "index"
	return []string{
		base,
		base + "_config",
		base + "_content",
		base + "_data",
		base + "_docsize",
		base + "_idx",
		base + "_state",
	}
}

// TestLegacyKnowledgeTablesAreNotReferencedByGoSource fails if any Go file
// still names a retired object, which would mean an active code path could
// query a schema that no longer exists.
func TestLegacyKnowledgeTablesAreNotReferencedByGoSource(t *testing.T) {
	root := moduleRoot(t)
	self := filepath.Base(currentFile(t))
	legacy := append(legacyKnowledgeTables(), legacyKnowledgeIndexObjects()...)
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
		t.Fatalf("Go source still references retired knowledge objects:\n%s", strings.Join(offenders, "\n"))
	}
}

// TestLegacyKnowledgeObjectsAreDropped pins the retirement as a schema fact:
// after every migration has run, no retired table and no FTS shadow table may
// remain, while the unified library's own tables must still be there. The
// positive control matters — an empty or half-applied schema must not satisfy
// this test.
func TestLegacyKnowledgeObjectsAreDropped(t *testing.T) {
	db := openKnowledgeLibraryTestDB(t)
	defer db.Close()

	for _, table := range append(legacyKnowledgeTables(), legacyKnowledgeIndexObjects()...) {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE name=?`, table).Scan(&name)
		if err == nil {
			t.Fatalf("retired object %s must be dropped by migration 0065", table)
		}
		if err != sql.ErrNoRows {
			t.Fatalf("querying sqlite_master for %s: %v", table, err)
		}
	}

	for _, table := range []string{
		"knowledge_libraries",
		"knowledge_write_tasks",
		"knowledge_releases",
		"knowledge_documents",
		"knowledge_evidence",
	} {
		var name string
		if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("unified library table %s must exist: %v", table, err)
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
