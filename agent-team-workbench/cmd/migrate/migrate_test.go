// migrate_test.go 确保唯一 SQLite 迁移历史可全量建库、幂等重放，且不会重命名
// 已写入 SQLite 数据库 schema_migrations 的历史版本。
package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	// SQLite driver 由 main.go 引入的 sqlstore 包注册，本文件无需重复。
)

// repoMigrationsDir 以本测试文件为锚点定位唯一迁移目录。
func repoMigrationsDir(t *testing.T) string {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(current), "..", "..", "migrations")
}

var historicalSQLiteMigrationVersions = []string{
	"0001_init",
	"0002_runtime_binding_model_config",
	"0003_agent_config",
	"0004_task_sessions",
	"0005_wakeup",
	"0006_plans",
	"0007_task_sessions_parent",
	"0008_plan_source_run_unique",
	"0009_plan_consult_knowledge",
	"0010_plan_join_guardrails",
	"0011_activity_work_item",
	"0012_approval_grants",
	"0013_entity_client_keys",
	"0014_task_execution_lock",
	"0015_run_event_agent_identity",
	"0016_dispatches",
	"0017_task_ledger",
	"0018_search_index",
	"0019_record_kind",
	"0020_task_coordinator",
	"0021_execution_context",
	"0022_task_comments",
	"0023_runner_event_dedup_v2",
	"0024_native_governance",
	"0025_coordinator_plan_repair",
}

func TestNoDialectMigrationDirectory(t *testing.T) {
	legacyDir := filepath.Join(repoMigrationsDir(t), "sqlite")
	if _, err := os.Stat(legacyDir); err == nil {
		t.Fatalf("SQLite 迁移已在 migrations/ 成为唯一真相源，不得恢复 %s", legacyDir)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// TestMigrationVersionsKeepExistingSQLiteDatabaseCompatible freezes the
// basename-based versions already stored by deployed SQLite databases. Moving
// the SQLite DDL to migrations/ must not make old databases replay history.
func TestMigrationVersionsKeepExistingSQLiteDatabaseCompatible(t *testing.T) {
	files, err := discoverMigrations(repoMigrationsDir(t))
	if err != nil {
		t.Fatal(err)
	}
	versions := make([]string, 0, len(files))
	for _, file := range files {
		versions = append(versions, strings.TrimSuffix(filepath.Base(file), ".sql"))
	}
	if len(versions) < len(historicalSQLiteMigrationVersions) ||
		!slices.Equal(versions[:len(historicalSQLiteMigrationVersions)], historicalSQLiteMigrationVersions) {
		t.Fatalf("SQLite 历史迁移版本改变: got %v, want prefix %v", versions, historicalSQLiteMigrationVersions)
	}
}

// TestMigrationsApplyEndToEnd 把唯一 migrations/ 全量应用到临时 SQLite 库，
// 走与生产相同的 ensureSchemaTable/discoverMigrations/applyMigrations 路径，
// 并验证 schema_migrations 的幂等性。
func TestMigrationsApplyEndToEnd(t *testing.T) {
	dir := repoMigrationsDir(t)
	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("未在 %s 发现迁移文件", dir)
	}

	db, err := sql.Open("sqlite",
		filepath.Join(t.TempDir(), "guard.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := ensureSchemaTable(db); err != nil {
		t.Fatalf("初始化 schema_migrations 失败: %v", err)
	}
	if err := applyMigrations(db, files); err != nil {
		t.Fatalf("全量应用 migrations 失败: %v", err)
	}

	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Fatalf("schema_migrations 应记录 %d 条，实际 %d", len(files), applied)
	}

	// 幂等回归：重跑一遍不得重复记录、不得报错。
	if err := applyMigrations(db, files); err != nil {
		t.Fatalf("重复 apply 应幂等跳过: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Fatalf("重复 apply 后记录数应仍为 %d，实际 %d", len(files), applied)
	}
}

// TestTaskPublicationExpectedVersionUpgradeFrom0054 builds a database through
// the real 0054 migration, inserts rows in the schema that an already deployed
// 0054 database would have, and then applies only 0055. This protects the
// upgrade path from accidentally relying on a fresh-database ordering.
func TestTaskPublicationExpectedVersionUpgradeFrom0054(t *testing.T) {
	dir := repoMigrationsDir(t)
	files, err := discoverMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	idx0001, idx0054, idx0055 := -1, -1, -1
	for i, path := range files {
		switch strings.TrimSuffix(filepath.Base(path), ".sql") {
		case "0001_init":
			idx0001 = i
		case "0054_task_publication_client_keys":
			idx0054 = i
		case "0055_task_publication_expected_version":
			idx0055 = i
		}
	}
	if idx0001 < 0 || idx0054 < 0 || idx0055 != idx0054+1 {
		t.Fatalf("publication migration order missing or changed: 0001=%d 0054=%d 0055=%d", idx0001, idx0054, idx0055)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade-0054.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := ensureSchemaTable(db); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(db, files[:idx0001+1]); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}

	// Seed the rows before 0021 so its real legacy upgrade path creates the
	// host/location and terminal execution snapshot exactly as it does for an
	// existing installation.
	const now = "2026-09-09T00:00:00Z"
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("seed legacy 0054 row: %v", err)
		}
	}
	execSQL(`INSERT INTO workspaces(id, name, timezone, version, created_at, updated_at)
		VALUES ('ws_upgrade_0054', 'upgrade', 'UTC', 1, ?, ?)`, now, now)
	execSQL(`INSERT INTO agent_profiles(id, workspace_id, name, role, version, created_at, updated_at)
		VALUES ('agent_upgrade_0054', 'ws_upgrade_0054', 'Upgrade Agent', 'developer', 1, ?, ?)`, now, now)
	execSQL(`INSERT INTO work_items(id, workspace_id, title, description, status, priority, agent_profile_id, version, created_at, updated_at)
		VALUES ('chat_upgrade_0054', 'ws_upgrade_0054', 'Chat', '', 'todo', 'medium', 'agent_upgrade_0054', 1, ?, ?)`, now, now)
	execSQL(`INSERT INTO work_items(id, workspace_id, title, description, status, priority, agent_profile_id, version, created_at, updated_at)
		VALUES ('task_upgrade_0054', 'ws_upgrade_0054', 'Task', '', 'todo', 'medium', 'agent_upgrade_0054', 1, ?, ?)`, now, now)
	execSQL(`INSERT INTO execution_runs(id, workspace_id, work_item_id, agent_profile_id, status, input, version, created_at, updated_at)
		VALUES ('run_upgrade_0054', 'ws_upgrade_0054', 'chat_upgrade_0054', 'agent_upgrade_0054', 'succeeded', '{}', 1, ?, ?)`, now, now)

	if err := applyMigrations(db, files[idx0001+1:idx0054+1]); err != nil {
		t.Fatalf("apply migrations through 0054: %v", err)
	}
	var snapshotID string
	if err := db.QueryRow(`SELECT id FROM execution_context_snapshots WHERE run_id=?`, "run_upgrade_0054").Scan(&snapshotID); err != nil {
		t.Fatalf("0054 legacy terminal run should have a context snapshot: %v", err)
	}
	execSQL(`INSERT INTO task_publication_drafts(
		id, workspace_id, chat_work_item_id, agent_profile_id, analysis_revision,
		item_ids_json, confirmation_ids_json, source_dependencies_json, title,
		description, acceptance_criteria_json, context_snapshot_id, baseline_json,
		fingerprint, status, task_id, client_key, version, created_at, updated_at)
		VALUES ('draft_upgrade_v3', 'ws_upgrade_0054', 'chat_upgrade_0054', 'agent_upgrade_0054', 1,
		'[]', '[]', '[]', 'old v3', '', '[]', ?, '{}', 'fp-v3', 'published',
		'task_upgrade_0054', 'draft-v3', 3, ?, ?)`, snapshotID, now, now)
	execSQL(`INSERT INTO task_publication_drafts(
		id, workspace_id, chat_work_item_id, agent_profile_id, analysis_revision,
		item_ids_json, confirmation_ids_json, source_dependencies_json, title,
		description, acceptance_criteria_json, context_snapshot_id, baseline_json,
		fingerprint, status, task_id, client_key, version, created_at, updated_at)
		VALUES ('draft_upgrade_v1', 'ws_upgrade_0054', 'chat_upgrade_0054', 'agent_upgrade_0054', 1,
		'[]', '[]', '[]', 'old v1', '', '[]', ?, '{}', 'fp-v1', 'published',
		'task_upgrade_0054', 'draft-v1', 1, ?, ?)`, snapshotID, now, now)
	execSQL(`INSERT INTO task_publications(
		id, draft_id, workspace_id, chat_work_item_id, task_id, analysis_revision,
		fingerprint, created_at, client_key, confirmation_ids_json,
		source_dependencies_json, baseline_json, context_snapshot_id)
		VALUES ('pub_upgrade_v3', 'draft_upgrade_v3', 'ws_upgrade_0054', 'chat_upgrade_0054',
		'task_upgrade_0054', 1, 'fp-v3', ?, 'publish-v3', '[]', '[]', '{}', ?)`, now, snapshotID)
	execSQL(`INSERT INTO task_publications(
		id, draft_id, workspace_id, chat_work_item_id, task_id, analysis_revision,
		fingerprint, created_at, client_key, confirmation_ids_json,
		source_dependencies_json, baseline_json, context_snapshot_id)
		VALUES ('pub_upgrade_v1', 'draft_upgrade_v1', 'ws_upgrade_0054', 'chat_upgrade_0054',
		'task_upgrade_0054', 1, 'fp-v1', ?, 'publish-v1', '[]', '[]', '{}', ?)`, now, snapshotID)

	var before0055 int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, "0055_task_publication_expected_version").Scan(&before0055); err != nil {
		t.Fatal(err)
	}
	if before0055 != 0 {
		t.Fatalf("0054 fixture unexpectedly records 0055: %d", before0055)
	}
	if err := applyMigrations(db, files[idx0055:idx0055+1]); err != nil {
		t.Fatalf("apply 0055 to old database: %v", err)
	}
	for _, tc := range []struct {
		id   string
		want int
	}{
		{id: "pub_upgrade_v3", want: 2},
		{id: "pub_upgrade_v1", want: 1},
	} {
		var got int
		if err := db.QueryRow(`SELECT expected_version FROM task_publications WHERE id=?`, tc.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("0055 %s expected_version=%d, want %d", tc.id, got, tc.want)
		}
	}
}
