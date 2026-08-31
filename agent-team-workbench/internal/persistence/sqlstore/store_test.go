package sqlstore

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteDriverDSNAppendsRequiredPragmasAfterExistingQuery(t *testing.T) {
	got, err := sqliteDriverDSN("sqlite://workbench.db?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	driverPath, rawQuery, ok := strings.Cut(got, "?")
	if !ok || driverPath != "workbench.db" {
		t.Fatalf("SQLite driver path = %q, want workbench.db", driverPath)
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("cache") != "shared" {
		t.Fatalf("SQLite 普通 query 丢失: %v", query)
	}
	wantPragmas := map[string]bool{
		"foreign_keys(1)": true, "busy_timeout(5000)": true, "journal_mode(WAL)": true,
	}
	if len(query["_pragma"]) != len(wantPragmas) {
		t.Fatalf("SQLite pragmas = %v", query["_pragma"])
	}
	for _, pragma := range query["_pragma"] {
		if !wantPragmas[pragma] {
			t.Fatalf("未知 SQLite pragma %q: %v", pragma, query["_pragma"])
		}
	}
}

func TestOpenEnforcesSQLiteConnectionContract(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, "sqlite://"+filepath.Join(t.TempDir(), "store.db")+"?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	var foreignKeys, busyTimeout int
	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf("SQLite pragmas = foreign_keys:%d busy_timeout:%d journal_mode:%q", foreignKeys, busyTimeout, journalMode)
	}
}

func TestOpenOverridesConflictingSQLitePragmas(t *testing.T) {
	ctx := context.Background()
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "forced.db") +
		"?_pragma=foreign_keys(0)&_pragma=busy_timeout(1)&_pragma=journal_mode(DELETE)"
	db, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var foreignKeys, busyTimeout int
	var journalMode string
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf("caller pragmas overrode SQLite contract: foreign_keys:%d busy_timeout:%d journal_mode:%q",
			foreignKeys, busyTimeout, journalMode)
	}
}

func TestOpenRejectsNonSQLiteOrPathlessDSN(t *testing.T) {
	for _, dsn := range []string{
		"", "postgres://localhost/workbench", "workbench.db", "sqlite://", "sqlite://?cache=shared",
		"sqlite://:memory:", "sqlite://file:memorydb?mode=memory&cache=shared",
	} {
		db, err := Open(context.Background(), dsn)
		if db != nil {
			_ = db.Close()
		}
		if err == nil {
			t.Errorf("Open(%q) succeeded, want SQLite DSN error", dsn)
		}
	}
}
