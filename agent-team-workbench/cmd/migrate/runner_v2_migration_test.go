package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestRunnerV2OwnershipMigrationUpgradesHistoricalLeases proves 0021 can
// remove the legacy Runner→Workspace ownership constraint without losing an
// already leased historical runner. This is deliberately an upgrade fixture,
// not a fresh-schema test: fresh application alone cannot exercise SQLite's
// parent-table rebuild under a run_leases foreign key.
func TestRunnerV2OwnershipMigrationUpgradesHistoricalLeases(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "runner-v2-upgrade.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ensureSchemaTable(db); err != nil {
		t.Fatal(err)
	}

	files, err := discoverMigrations(repoMigrationsDir(t, "migrations", "sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var before, target []string
	for _, file := range files {
		switch filepath.Base(file) {
		case "0021_execution_context.sql":
			target = append(target, file)
		case "0022_task_comments.sql", "0023_runner_event_dedup_v2.sql":
			continue
		default:
			before = append(before, file)
		}
	}
	if len(target) != 1 {
		t.Fatalf("应恰有一条 0021 execution context migration，实际 %d", len(target))
	}
	if err := applyMigrations(db, before); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workspaces(id,name,timezone,version,created_at,updated_at) VALUES ('ws_runner','runner','UTC',1,?,?)`, []any{now, now}},
		{`INSERT INTO work_items(id,workspace_id,title,record_kind,created_at,updated_at) VALUES ('wi_runner','ws_runner','runner task','chat',?,?)`, []any{now, now}},
		{`INSERT INTO execution_runs(id,workspace_id,work_item_id,status,input,version,created_at,updated_at) VALUES ('run_runner','ws_runner','wi_runner','running','{}',1,?,?)`, []any{now, now}},
		{`INSERT INTO runners(id,workspace_id,label,slots,status,created_at) VALUES ('runner_legacy','ws_runner','legacy',1,'connected',?)`, []any{now}},
		{`INSERT INTO run_leases(lease_id,run_id,runner_id,fencing_token,acquired_at,renewed_until) VALUES ('lease_legacy_low','run_runner','runner_legacy',1,?,?)`, []any{now, now}},
		{`INSERT INTO run_leases(lease_id,run_id,runner_id,fencing_token,acquired_at,renewed_until) VALUES ('lease_legacy_high','run_runner','runner_legacy',2,?,?)`, []any{now, now}},
	} {
		if _, err := db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}

	if err := applyMigrations(db, target); err != nil {
		t.Fatal(err)
	}

	var notNull int
	if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('runners') WHERE name='workspace_id'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("v2 runners.workspace_id 应 nullable，实际 pragma notnull=%d", notNull)
	}
	var workspace sql.NullString
	if err := db.QueryRow(`SELECT workspace_id FROM runners WHERE id='runner_legacy'`).Scan(&workspace); err != nil {
		t.Fatal(err)
	}
	if workspace.Valid {
		t.Fatalf("升级后 legacy runner 不应继续保留 Workspace 归属，实际 %q", workspace.String)
	}
	var leases, active, maxFence int
	if err := db.QueryRow(`SELECT count(*), SUM(CASE WHEN released_at IS NULL THEN 1 ELSE 0 END), max(fencing_token)
		FROM run_leases WHERE run_id='run_runner' AND runner_id='runner_legacy'`).Scan(&leases, &active, &maxFence); err != nil {
		t.Fatal(err)
	}
	if leases != 2 || active != 1 || maxFence != 2 {
		t.Fatalf("迁移必须保留历史 lease 且只留最高 active fence: total=%d active=%d max=%d", leases, active, maxFence)
	}
	var bootColumns int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('runners') WHERE name='boot_id'`).Scan(&bootColumns); err != nil {
		t.Fatal(err)
	}
	if bootColumns != 1 {
		t.Fatal("Runner v2 upgrade 必须创建 boot_id 列")
	}
	rows, err := db.Query(`PRAGMA foreign_key_list('runners')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if from == "workspace_id" {
			t.Fatalf("v2 runners 不得残留 workspace_id FK: %+v", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
