package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openRunnerDB opens a database the way the runner does: foreign keys on.
func openRunnerDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ensureSchemaTable(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE parent (id TEXT PRIMARY KEY);
		CREATE TABLE child (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES parent(id));
		INSERT INTO parent(id) VALUES ('p1');
		INSERT INTO child(id, parent_id) VALUES ('c1', 'p1');`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestWithoutTransactionFailureLeavesConnectionClean covers the failure path of
// a schema-rebuild migration: the open transaction is rolled back, the
// foreign-key switch is restored, and the same pool can be used again.
func TestWithoutTransactionFailureLeavesConnectionClean(t *testing.T) {
	db := openRunnerDB(t, "runner-failure.db")
	body := `-- migrate:no-transaction
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE parent_new (id TEXT PRIMARY KEY);
INSERT INTO parent_new(id) SELECT id FROM parent;
DROP TABLE parent;
CREATE TABLE parent (id TEXT PRIMARY KEY, note TEXT NOT NULL DEFAULT '');
INSERT INTO parent(id, note) VALUES ('p1', 'rebuilt');
SELECT raise_broken_sql_here;
COMMIT;
`
	if err := applyWithoutTransaction(db, "9999_broken", body); err == nil {
		t.Fatal("a broken migration must fail")
	}
	// The transaction was rolled back: the original schema is intact.
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM parent`).Scan(&rows); err != nil {
		t.Fatalf("the database must be usable after a failed migration: %v", err)
	}
	if rows != 1 {
		t.Fatalf("the failed migration must not have been applied: %d rows", rows)
	}
	// Foreign keys are back on, so an orphan insert is refused rather than
	// silently accepted on this connection.
	if _, err := db.Exec(`INSERT INTO child(id, parent_id) VALUES ('c2', 'missing')`); err == nil {
		t.Fatal("foreign key enforcement must be restored after a failed migration")
	}
	// And the same pool can apply a working migration afterwards.
	good := `-- migrate:no-transaction
PRAGMA foreign_keys = OFF;
BEGIN;
UPDATE parent SET id='p1' WHERE id='p1';
COMMIT;
PRAGMA foreign_keys = ON;
`
	if err := applyWithoutTransaction(db, "9999_good", good); err != nil {
		t.Fatalf("a retry on the same pool must work: %v", err)
	}
}

// TestWithoutTransactionReplayIsIdempotent covers the window where the rebuild
// transaction committed but the version row was not recorded: a replay must not
// lose the data the rebuild carried over.
func TestWithoutTransactionReplayIsIdempotent(t *testing.T) {
	db := openRunnerDB(t, "runner-replay.db")
	body, err := os.ReadFile(filepath.Join(repoMigrationsDir(t), "0062_knowledge_requirement_source_kind.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// A minimal schema the migration can rebuild, plus child rows that must
	// survive.
	if _, err := db.Exec(`CREATE TABLE knowledge_libraries (id TEXT PRIMARY KEY);
		INSERT INTO knowledge_libraries(id) VALUES ('lib1');
		CREATE TABLE knowledge_library_sources (
			id TEXT PRIMARY KEY, library_id TEXT NOT NULL REFERENCES knowledge_libraries(id),
			name TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('service','common','documents','other')),
			repo_path TEXT NOT NULL, default_ref TEXT NOT NULL DEFAULT '',
			include_globs TEXT NOT NULL DEFAULT '[]', exclude_globs TEXT NOT NULL DEFAULT '[]',
			usages_json TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
			version INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
			created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
			UNIQUE (library_id, name), CHECK (length(trim(repo_path)) > 0));
		INSERT INTO knowledge_library_sources
			(id, library_id, name, kind, repo_path, created_at, updated_at)
			VALUES ('src1','lib1','order-service','service','/repo',datetime('now'),datetime('now'));
		CREATE TABLE knowledge_snapshot_bindings (
			id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES knowledge_library_sources(id));
		INSERT INTO knowledge_snapshot_bindings(id, source_id) VALUES ('b1','src1');`); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(repoMigrationsDir(t), "0062_knowledge_requirement_source_kind.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWithoutTransaction(db, "0062_knowledge_requirement_source_kind", string(first)); err != nil {
		t.Fatalf("first application failed: %v", err)
	}
	// The version row is recorded on success; simulate the crash window by
	// removing it and replaying the file.
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version='0062_knowledge_requirement_source_kind'`); err != nil {
		t.Fatal(err)
	}
	if err := applyWithoutTransaction(db, "0062_knowledge_requirement_source_kind", string(body)); err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	var sources, bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_library_sources`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_snapshot_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || bindings != 1 {
		t.Fatalf("a replay must preserve rows: sources=%d bindings=%d", sources, bindings)
	}
	var leftover int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%_carry'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("the rebuild must not leave staging tables behind: %d", leftover)
	}
}
